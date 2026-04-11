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

// replacement describes a planned replacement for a stale torrent.
type replacement struct {
	torrent  Torrent
	topicURL string
	topicID  string
	info     *TopicInfo // from api.rutracker.cc
}

func (u *Updater) Run(ctx context.Context) error {
	if err := u.qbit.Login(ctx); err != nil {
		return err
	}
	torrents, err := u.qbit.ListTorrents(ctx)
	if err != nil {
		return err
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
	u.log.Info("stale torrents identified", "count", len(stale))

	// Resolve replacements via api.rutracker.cc.
	var plans []replacement
	var skipped int
	for _, t := range stale {
		plan, err := u.resolvePlan(ctx, t)
		if err != nil {
			u.log.Warn("skipping — cannot resolve replacement",
				"name", t.Name, "hash", t.Hash, "reason", err)
			skipped++
			continue
		}
		if plan == nil {
			// Same info_hash — not a real re-upload, tracker hiccup.
			u.log.Info("skipping — info_hash unchanged (transient tracker error?)",
				"name", t.Name, "hash", t.Hash)
			skipped++
			continue
		}
		plans = append(plans, *plan)
	}
	u.log.Info("replacements planned",
		"total_stale", len(stale), "replaceable", len(plans), "skipped", skipped)

	var errs []updateError
	for _, plan := range plans {
		if u.dryRun {
			u.log.Info("[dry-run] would replace",
				"name", plan.torrent.Name,
				"hash", plan.torrent.Hash,
				"new_info_hash", plan.info.InfoHash,
				"topic", plan.topicURL,
				"topic_title", plan.info.TopicTitle,
				"category", plan.torrent.Category,
				"save_path", plan.torrent.SavePath,
			)
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

// resolvePlan asks the rutracker public API whether the torrent's topic is
// still alive under the same id with a different info_hash (i.e. a re-pack).
// Returns:
//   - (nil, nil) — info_hash unchanged, don't touch
//   - (*replacement, nil) — topic alive with new info_hash, safe to replace
//   - (nil, err) — cannot determine (topic gone, non-rutracker, missing comment, etc.)
func (u *Updater) resolvePlan(ctx context.Context, t Torrent) (*replacement, error) {
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
	return &replacement{
		torrent:  t,
		topicURL: topicURL,
		topicID:  topicID,
		info:     info,
	}, nil
}

func (u *Updater) applyPlan(ctx context.Context, plan replacement) error {
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

// staleReason returns the matched tracker message if the torrent looks stale
// (re-uploaded / removed from tracker), or empty string otherwise.
func staleReason(trackers []Tracker) string {
	markers := []string{
		"not registered",
		"unregistered torrent",
		"torrent not found",
		"torrent does not exist",
		"torrent not exist",
		"torrent has been deleted",
		"torrent was deleted",
		"infohash not found",
	}
	for _, tr := range trackers {
		if strings.HasPrefix(tr.URL, "**") {
			continue
		}
		msg := strings.ToLower(strings.TrimSpace(tr.Msg))
		if msg == "" {
			continue
		}
		for _, m := range markers {
			if strings.Contains(msg, m) {
				return tr.Msg
			}
		}
	}
	return ""
}
