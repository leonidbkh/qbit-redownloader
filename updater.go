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

// updatePlan describes a planned updatePlan for a stale torrent.
type updatePlan struct {
	torrent  Torrent
	topicURL string
	topicID  string
	info     *TopicInfo // from api.rutracker.cc
}

func (u *Updater) Run(ctx context.Context) error {
	if err := u.qbit.Login(ctx); err != nil {
		return err
	}

	stale, err := u.detectStale(ctx)
	if err != nil {
		return err
	}
	u.log.Info("stale torrents identified", "count", len(stale))

	plans := u.planReplacements(ctx, stale)
	u.log.Info("replacements planned",
		"stale", len(stale), "replaceable", len(plans), "skipped", len(stale)-len(plans))

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

// detectStale fetches all torrents and returns those whose tracker status
// indicates the torrent is no longer registered on the tracker.
func (u *Updater) detectStale(ctx context.Context) ([]Torrent, error) {
	torrents, err := u.qbit.ListTorrents(ctx)
	if err != nil {
		return nil, err
	}
	u.log.Info("fetched torrents", "count", len(torrents))

	var stale []Torrent
	for _, t := range torrents {
		trackers, err := u.qbit.Trackers(ctx, t.Hash)
		if err != nil {
			u.log.Warn("failed to read trackers", "hash", t.Hash, "name", t.Name, "err", err)
			continue
		}
		if reason := staleReason(trackers); reason != "" {
			u.log.Debug("stale match", "name", t.Name, "reason", reason)
			stale = append(stale, t)
		}
	}
	return stale, nil
}

// planReplacements resolves each stale torrent against api.rutracker.cc and
// returns the plans that are safe to execute (topic still alive, info_hash
// changed). Unresolvable entries are logged and dropped.
func (u *Updater) planReplacements(ctx context.Context, stale []Torrent) []updatePlan {
	var plans []updatePlan
	for _, t := range stale {
		plan, err := u.resolvePlan(ctx, t)
		if err != nil {
			u.log.Warn("skipping — cannot resolve replacement",
				"name", t.Name, "hash", t.Hash, "reason", err)
			continue
		}
		if plan == nil {
			u.log.Info("skipping — info_hash unchanged (transient tracker error?)",
				"name", t.Name, "hash", t.Hash)
			continue
		}
		plans = append(plans, *plan)
	}
	return plans
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
	)
}

// resolvePlan asks the rutracker public API whether the torrent's topic is
// still alive under the same id with a different info_hash (i.e. a re-pack).
// Returns:
//   - (nil, nil) — info_hash unchanged, don't touch
//   - (*updatePlan, nil) — topic alive with new info_hash, safe to replace
//   - (nil, err) — cannot determine (topic gone, non-rutracker, missing comment, etc.)
func (u *Updater) resolvePlan(ctx context.Context, t Torrent) (*updatePlan, error) {
	props, err := u.qbit.Properties(ctx, t.Hash)
	if err != nil {
		return nil, fmt.Errorf("get properties: %w", err)
	}
	topicURL := strings.TrimSpace(props.Comment)
	if topicURL == "" || !strings.HasPrefix(topicURL, "http") {
		return nil, fmt.Errorf("torrent has no topic URL in comment")
	}
	if !IsRutrackerTopic(topicURL) {
		return nil, fmt.Errorf("non-rutracker topic (%s) — unsupported", topicURL)
	}
	topicID := extractTopicID(topicURL)
	if topicID == "" {
		return nil, fmt.Errorf("cannot extract topic id from %s", topicURL)
	}

	info, err := u.rutracker.GetTopic(ctx, topicID)
	if err != nil {
		return nil, fmt.Errorf("rutracker api: %w", err)
	}
	if info == nil {
		return nil, fmt.Errorf("topic %s no longer exists on rutracker (deleted, not a re-pack)", topicID)
	}
	if strings.EqualFold(info.InfoHash, t.Hash) {
		// Same hash — tracker is confused, nothing to do.
		return nil, nil
	}
	return &updatePlan{
		torrent:  t,
		topicURL: topicURL,
		topicID:  topicID,
		info:     info,
	}, nil
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
	// Take the first segment before "/" (usually the Russian title) — rutracker
	// search matches any segment; shorter queries work better.
	if i := strings.Index(title, "/"); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return title
}

