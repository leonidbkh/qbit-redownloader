package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type Updater struct {
	qbit      *QbitClient
	prowlarr  *ProwlarrClient
	rutracker *RutrackerAPI
	log       *slog.Logger
	dryRun    bool
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
	torrent  Torrent
	topicURL string
	topicID  string
	info     *TopicInfo // from api.rutracker.cc
	reason   string     // "info_hash changed" / "tor_status=8 (дубликат)" / etc.
}

// candidate is an intermediate value: a rutracker torrent paired with its
// resolved topic id, before we know whether it's stale.
type candidate struct {
	torrent  Torrent
	topicID  string
	topicURL string
}

func (u *Updater) Run(ctx context.Context) error {
	if err := u.qbit.Login(ctx); err != nil {
		return err
	}

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
		u.log.Warn("rutracker API failed — falling back to tracker messages", "err", err)
		return u.fallbackToTrackerMessages(ctx, candidates), nil
	}

	var plans []updatePlan
	for _, c := range candidates {
		if plan := u.classify(c, apiResult[c.topicID]); plan != nil {
			plans = append(plans, *plan)
		}
	}
	return plans, nil
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
		if !IsRutrackerTopic(topicURL) {
			continue
		}
		topicID := extractTopicID(topicURL)
		if topicID == "" {
			u.log.Warn("cannot extract topic id from comment", "name", t.Name, "comment", topicURL)
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
		torrent:  t,
		topicURL: c.topicURL,
		topicID:  c.topicID,
		info:     info,
		reason:   reason,
	}
}

// fallbackToTrackerMessages is used only when the rutracker API is
// unreachable. It scans tracker messages for "torrent removed" markers and
// logs matches — but cannot produce an updatePlan, since we don't know the
// new info_hash without the API.
func (u *Updater) fallbackToTrackerMessages(ctx context.Context, candidates []candidate) []updatePlan {
	for _, c := range candidates {
		trackers, err := u.qbit.Trackers(ctx, c.torrent.Hash)
		if err != nil {
			continue
		}
		if reason := staleReasonFromTracker(trackers); reason != "" {
			u.log.Warn("tracker reports stale (API down — cannot resolve replacement)",
				"name", c.torrent.Name, "hash", c.torrent.Hash, "msg", reason)
		}
	}
	return nil
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
			"new_hash", strings.ToLower(plan.info.InfoHash),
			"category", plan.torrent.Category,
		)
	}
	return errs
}

func (u *Updater) logPlan(plan updatePlan) {
	u.log.Info("[dry-run] would replace",
		"name", plan.torrent.Name,
		"hash", plan.torrent.Hash,
		"new_info_hash", plan.info.InfoHash,
		"topic", plan.topicURL,
		"topic_title", plan.info.TopicTitle,
		"category", plan.torrent.Category,
		"save_path", plan.torrent.SavePath,
		"reason", plan.reason,
	)
}

func (u *Updater) applyPlan(ctx context.Context, plan updatePlan) error {
	// Use Prowlarr only as a download proxy: search and pick the result with
	// the matching topic id to obtain an encrypted downloadUrl we can fetch.
	query := searchQueryFromTitle(plan.info.TopicTitle)
	u.log.Debug("prowlarr search", "query", query, "topic_id", plan.topicID)

	result, err := u.prowlarr.FindByTopicID(ctx, plan.topicURL, query)
	if err != nil {
		return fmt.Errorf("find in prowlarr: %w", err)
	}
	torrentBytes, err := u.prowlarr.Download(ctx, result.DownloadURL)
	if err != nil {
		return fmt.Errorf("download fresh torrent: %w", err)
	}

	if err := u.qbit.AddTorrent(ctx, torrentBytes, plan.torrent.Name, plan.torrent.SavePath, plan.torrent.Category, plan.torrent.Tags); err != nil {
		return fmt.Errorf("add new torrent: %w", err)
	}
	if err := u.qbit.Delete(ctx, plan.torrent.Hash, false); err != nil {
		return fmt.Errorf("delete old torrent (new one already added): %w", err)
	}
	return nil
}

// searchQueryFromTitle picks a short, distinctive query from a rutracker
// topic_title. Rutracker titles look like "Russian / English / Another (Dir)
// [year, country, ...]" — the Prowlarr indexer matches on the raw title, so
// we pass a shortened version that avoids overly specific characters.
func searchQueryFromTitle(title string) string {
	if i := strings.IndexAny(title, "[("); i >= 0 {
		title = title[:i]
	}
	if i := strings.Index(title, "/"); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return title
}
