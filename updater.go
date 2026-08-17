package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"
)

type Updater struct {
	qbit           *QbitClient
	rutracker      *RutrackerAPI
	resolver       TopicResolver
	log            *slog.Logger
	dryRun         bool
	confirmTimeout time.Duration
	topicIDFilter  string
}

type updateError struct {
	hash string
	name string
	err  error
}

func (e updateError) Error() string {
	return fmt.Sprintf("%s (%s): %v", e.name, e.hash, e.err)
}

// updatePlan describes a planned update for a stale torrent.
type updatePlan struct {
	torrent      Torrent // primary old torrent, retained for concise logging
	torrents     []Torrent
	topicURL     string
	topicID      string
	expectedHash string
	magnet       string
	reason       string // "info_hash changed" / "tor_status=8 (дубликат)" / etc.
}

// candidate is an intermediate value: a rutracker torrent paired with its
// resolved topic id, before we know whether it's stale.
type candidate struct {
	torrent  Torrent
	topicID  string
	topicURL string
}

const topicTagPrefix = "rutracker-topic-"

func (u *Updater) Run(ctx context.Context) error {
	plans, err := u.detectAndPlan(ctx)
	if err != nil {
		return err
	}
	u.log.Info("replacements planned", "count", len(plans))

	errs := u.executePlans(ctx, plans)
	if len(errs) > 0 {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("%d torrent(s) failed to update:\n", len(errs)))
		for _, e := range errs {
			b.WriteString("  - ")
			b.WriteString(e.Error())
			b.WriteString("\n")
		}
		return fmt.Errorf("%s", b.String())
	}
	return nil
}

// detectAndPlan walks every torrent in qBit, queries the rutracker API in
// bulk, and returns plans for the ones that need replacing. It logs (but
// does not return) torrents that look stale yet have no replacement (deleted
// topic, obsolete status without a newer release on the same topic).
func (u *Updater) detectAndPlan(ctx context.Context) ([]updatePlan, error) {
	torrents, err := u.qbit.ListTorrents(ctx)
	if err != nil {
		return nil, err
	}
	u.log.Info("fetched torrents", "count", len(torrents))

	candidates := u.collectCandidates(ctx, torrents)
	u.log.Info("rutracker torrents identified", "count", len(candidates))
	if len(candidates) == 0 {
		return nil, nil
	}

	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.topicID
	}
	apiResult, err := u.rutracker.GetTopics(ctx, ids)
	if err != nil {
		u.log.Warn("rutracker API failed — resolving stale torrents by exact topic id", "err", err)
		return u.fallbackToExactTopics(ctx, candidates)
	}

	var plans []updatePlan
	for _, c := range candidates {
		if plan := u.classify(c, apiResult[c.topicID]); plan != nil {
			plans = append(plans, *plan)
		}
	}
	return u.groupCompatiblePlans(plans), nil
}

// collectCandidates filters torrents to ones whose comment URL points at a
// rutracker topic and pairs them with the parsed topic id. Torrents without
// a rutracker comment are silently dropped — we have no way to resolve a
// replacement for them anyway.
func (u *Updater) collectCandidates(ctx context.Context, torrents []Torrent) []candidate {
	var out []candidate
	for _, t := range torrents {
		props, err := u.qbit.Properties(ctx, t.Hash)
		if err != nil {
			u.log.Warn("failed to read properties", "hash", t.Hash, "name", t.Name, "err", err)
			continue
		}
		topicURL := strings.TrimSpace(props.Comment)
		topicID := ""
		if IsRutrackerTopic(topicURL) {
			topicID = extractTopicID(topicURL)
		}
		if topicID == "" {
			topicID = topicIDFromTags(t.Tags)
			if topicID != "" {
				topicURL = canonicalTopicURL(topicID)
			}
		}
		if topicID == "" {
			continue
		}
		if u.topicIDFilter != "" && topicID != u.topicIDFilter {
			continue
		}
		out = append(out, candidate{torrent: t, topicID: topicID, topicURL: topicURL})
	}
	return out
}

// classify decides whether a single candidate needs replacement.
// Returns:
//   - non-nil plan — torrent is stale and the same topic carries a newer info_hash
//   - nil          — healthy, or stale-but-unrecoverable (logged, not returned)
func (u *Updater) classify(c candidate, info *TopicInfo) *updatePlan {
	t := c.torrent
	if info == nil {
		u.log.Warn("topic deleted on rutracker — manual action needed",
			"name", t.Name, "hash", t.Hash, "topic", c.topicURL)
		return nil
	}
	sameHash := strings.EqualFold(info.InfoHash, t.Hash)
	if sameHash {
		if isObsoleteStatus(info.TorStatus) {
			u.log.Warn("torrent obsolete on tracker, no newer version on same topic — manual action",
				"name", t.Name, "hash", t.Hash, "tor_status", info.TorStatus,
				"status_meaning", obsoleteStatuses[info.TorStatus], "topic", c.topicURL)
		} else {
			u.log.Debug("healthy", "name", t.Name, "tor_status", info.TorStatus)
		}
		return nil
	}

	reason := "info_hash changed"
	if isObsoleteStatus(info.TorStatus) {
		reason = fmt.Sprintf("info_hash changed + tor_status=%d (%s)",
			info.TorStatus, obsoleteStatuses[info.TorStatus])
	}
	u.log.Info("stale: replacement available",
		"name", t.Name, "old_hash", t.Hash, "new_hash", strings.ToLower(info.InfoHash),
		"tor_status", info.TorStatus, "topic", c.topicURL)
	return &updatePlan{
		torrent:      t,
		torrents:     []Torrent{t},
		topicURL:     c.topicURL,
		topicID:      c.topicID,
		expectedHash: strings.ToLower(info.InfoHash),
		reason:       reason,
	}
}

// fallbackToExactTopics is used when the public JSON API is unavailable. It
// limits expensive browser requests to torrents whose RuTracker announcer says
// they are stale, then resolves each immutable topic id directly.
func (u *Updater) fallbackToExactTopics(ctx context.Context, candidates []candidate) ([]updatePlan, error) {
	if u.resolver == nil {
		return nil, fmt.Errorf("rutracker exact-topic resolver is not configured")
	}
	var plans []updatePlan
	var retryable []error
	type cachedResolution struct {
		topic *ResolvedTopic
		err   error
	}
	cache := make(map[string]cachedResolution)
	for _, c := range candidates {
		trackers, err := u.qbit.Trackers(ctx, c.torrent.Hash)
		if err != nil {
			retryable = append(retryable, fmt.Errorf("read trackers for %s: %w", c.torrent.Hash, err))
			continue
		}
		reason := staleReasonFromTracker(trackers)
		if reason == "" {
			continue
		}
		cached, ok := cache[c.topicID]
		if !ok {
			cached.topic, cached.err = u.resolver.Resolve(ctx, c.topicID)
			cache[c.topicID] = cached
		}
		resolved, err := cached.topic, cached.err
		if errors.Is(err, ErrTopicHasNoMagnet) {
			u.log.Warn("tracker reports stale but exact topic has no magnet — manual action needed",
				"name", c.torrent.Name, "hash", c.torrent.Hash, "topic", c.topicURL)
			continue
		}
		if err != nil {
			u.log.Warn("failed to resolve exact rutracker topic",
				"name", c.torrent.Name, "hash", c.torrent.Hash, "topic", c.topicURL, "err", err)
			retryable = append(retryable, fmt.Errorf("resolve topic %s: %w", c.topicID, err))
			continue
		}
		if strings.EqualFold(resolved.InfoHash, c.torrent.Hash) {
			u.log.Warn("tracker reports stale but exact topic still has the old hash — manual action needed",
				"name", c.torrent.Name, "hash", c.torrent.Hash, "topic", c.topicURL)
			continue
		}
		u.log.Info("stale: exact replacement available",
			"name", c.torrent.Name, "old_hash", c.torrent.Hash,
			"new_hash", resolved.InfoHash, "topic", c.topicURL)
		plans = append(plans, updatePlan{
			torrent:      c.torrent,
			torrents:     []Torrent{c.torrent},
			topicURL:     c.topicURL,
			topicID:      c.topicID,
			expectedHash: resolved.InfoHash,
			magnet:       resolved.Magnet,
			reason:       reason,
		})
	}
	if len(plans) == 0 && len(retryable) > 0 {
		return nil, errors.Join(retryable...)
	}
	return u.groupCompatiblePlans(plans), nil
}

func (u *Updater) groupCompatiblePlans(plans []updatePlan) []updatePlan {
	grouped := make([]updatePlan, 0, len(plans))
	byTopic := make(map[string]int, len(plans))
	for _, plan := range plans {
		index, exists := byTopic[plan.topicID]
		if !exists {
			byTopic[plan.topicID] = len(grouped)
			grouped = append(grouped, plan)
			continue
		}
		existing := &grouped[index]
		if !strings.EqualFold(existing.expectedHash, plan.expectedHash) ||
			existing.torrent.SavePath != plan.torrent.SavePath ||
			existing.torrent.Category != plan.torrent.Category {
			u.log.Warn("duplicate topic has incompatible torrent placement — leaving duplicate for manual action",
				"topic_id", plan.topicID, "hash", plan.torrent.Hash,
				"save_path", plan.torrent.SavePath, "category", plan.torrent.Category)
			continue
		}
		existing.torrents = append(existing.torrents, plan.torrents...)
		existing.torrent.Tags = mergeTags(existing.torrent.Tags, plan.torrent.Tags)
		u.log.Info("grouped compatible torrents with the same rutracker topic",
			"topic_id", plan.topicID, "count", len(existing.torrents))
	}
	return grouped
}

// executePlans applies each updatePlan (or logs dry-run preview) and
// collects per-torrent errors without aborting on the first failure.
func (u *Updater) executePlans(ctx context.Context, plans []updatePlan) []updateError {
	var errs []updateError
	for _, plan := range plans {
		if u.dryRun {
			u.logPlan(plan)
			continue
		}
		if err := u.applyPlan(ctx, plan); err != nil {
			u.log.Error("update failed",
				"name", plan.torrent.Name, "hash", plan.torrent.Hash, "err", err)
			errs = append(errs, updateError{hash: plan.torrent.Hash, name: plan.torrent.Name, err: err})
			continue
		}
		u.log.Info("updated",
			"name", plan.torrent.Name,
			"old_hash", plan.torrent.Hash,
			"new_hash", plan.expectedHash,
			"category", plan.torrent.Category,
			"old_torrent_count", len(plan.torrents),
		)
	}
	return errs
}

func (u *Updater) logPlan(plan updatePlan) {
	u.log.Info("[dry-run] would replace",
		"name", plan.torrent.Name,
		"hash", plan.torrent.Hash,
		"new_info_hash", plan.expectedHash,
		"topic", plan.topicURL,
		"category", plan.torrent.Category,
		"save_path", plan.torrent.SavePath,
		"reason", plan.reason,
		"old_torrent_count", len(plan.torrents),
	)
}

func (u *Updater) applyPlan(ctx context.Context, plan updatePlan) error {
	if plan.magnet == "" {
		resolved, err := u.resolver.Resolve(ctx, plan.topicID)
		if err != nil {
			return fmt.Errorf("resolve exact rutracker topic: %w", err)
		}
		if !strings.EqualFold(resolved.InfoHash, plan.expectedHash) {
			return fmt.Errorf("topic changed during update: planned hash %s, resolved hash %s", plan.expectedHash, resolved.InfoHash)
		}
		plan.magnet = resolved.Magnet
	}
	if !strings.EqualFold(infoHashFromMagnet(plan.magnet), plan.expectedHash) {
		return fmt.Errorf("replacement magnet does not contain expected hash %s", plan.expectedHash)
	}
	const stagingTag = "qbit-redownloader-staging"
	existing, err := u.qbit.TorrentByHash(ctx, plan.expectedHash)
	if err != nil {
		return fmt.Errorf("check replacement absence: %w", err)
	}
	if existing != nil && !hasTag(existing.Tags, stagingTag) {
		return fmt.Errorf("replacement hash %s already exists; refusing to delete old torrent", plan.expectedHash)
	}

	if existing == nil {
		tags := mergeTags(plan.torrent.Tags, stagingTag, topicTag(plan.topicID))
		if err := u.qbit.AddMagnet(ctx, plan.magnet, plan.torrent.SavePath, plan.torrent.Category, tags); err != nil {
			return fmt.Errorf("add replacement magnet: %w", err)
		}
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if torrent, err := u.qbit.TorrentByHash(cleanupCtx, plan.expectedHash); err == nil && torrent != nil && hasTag(torrent.Tags, stagingTag) {
				if err := u.qbit.Delete(cleanupCtx, plan.expectedHash, false); err != nil {
					u.log.Error("failed to roll back staged replacement", "hash", plan.expectedHash, "err", err)
				}
			}
		}
	}()

	confirmTimeout := u.confirmTimeout
	if confirmTimeout <= 0 {
		confirmTimeout = 2 * time.Minute
	}
	if _, err := u.qbit.WaitForTorrentReady(ctx, plan.expectedHash, confirmTimeout); err != nil {
		return fmt.Errorf("confirm replacement: %w", err)
	}
	if err := u.qbit.SetComment(ctx, plan.expectedHash, canonicalTopicURL(plan.topicID)); err != nil {
		return fmt.Errorf("persist rutracker topic identity: %w", err)
	}
	props, err := u.qbit.Properties(ctx, plan.expectedHash)
	if err != nil {
		return fmt.Errorf("verify rutracker topic identity: %w", err)
	}
	if extractTopicID(props.Comment) != plan.topicID {
		return fmt.Errorf("qBittorrent did not persist topic id %s on replacement", plan.topicID)
	}
	committed = true
	for _, oldTorrent := range plan.torrents {
		if err := u.qbit.Delete(ctx, oldTorrent.Hash, false); err != nil {
			return fmt.Errorf("delete old torrent %s after replacement confirmation: %w", oldTorrent.Hash, err)
		}
	}
	if allStopped(plan.torrents) {
		if err := u.qbit.Stop(ctx, plan.expectedHash); err != nil {
			return fmt.Errorf("restore stopped state: %w", err)
		}
	}
	if err := u.qbit.RemoveTags(ctx, plan.expectedHash, stagingTag); err != nil {
		u.log.Warn("replacement committed but staging tag could not be removed", "hash", plan.expectedHash, "err", err)
	}
	return nil
}

func infoHashFromMagnet(magnet string) string {
	parsed, err := url.Parse(magnet)
	if err != nil {
		return ""
	}
	match := infoHashPattern.FindStringSubmatch("?" + parsed.RawQuery)
	if len(match) != 2 {
		return ""
	}
	return strings.ToLower(match[1])
}

func appendTag(tags, tag string) string {
	if hasTag(tags, tag) {
		return tags
	}
	if strings.TrimSpace(tags) == "" {
		return tag
	}
	return strings.TrimSpace(tags) + ", " + tag
}

func mergeTags(tagSets ...string) string {
	var merged string
	for _, tags := range tagSets {
		for _, tag := range strings.Split(tags, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				merged = appendTag(merged, tag)
			}
		}
	}
	return merged
}

func hasTag(tags, wanted string) bool {
	for _, tag := range strings.Split(tags, ",") {
		if strings.TrimSpace(tag) == wanted {
			return true
		}
	}
	return false
}

func isStoppedState(state string) bool {
	return strings.HasPrefix(state, "stopped") || strings.HasPrefix(state, "paused")
}

func topicTag(topicID string) string {
	return topicTagPrefix + topicID
}

func topicIDFromTags(tags string) string {
	for _, tag := range strings.Split(tags, ",") {
		id, ok := strings.CutPrefix(strings.TrimSpace(tag), topicTagPrefix)
		if ok && topicIDPattern.MatchString(id) {
			return id
		}
	}
	return ""
}

func canonicalTopicURL(topicID string) string {
	return "https://rutracker.org/forum/viewtopic.php?t=" + topicID
}

func allStopped(torrents []Torrent) bool {
	if len(torrents) == 0 {
		return false
	}
	for _, torrent := range torrents {
		if !isStoppedState(torrent.State) {
			return false
		}
	}
	return true
}
