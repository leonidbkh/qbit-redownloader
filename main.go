package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	var (
		configPath = flag.String("config", "", "path to YAML config file (optional, env vars also supported)")
		dryRun     = flag.Bool("dry-run", false, "report stale torrents without updating")
		debug      = flag.Bool("debug", false, "verbose logging")
		topicID    = flag.String("topic-id", "", "process only this RuTracker topic id (optional canary filter)")
		adoptHash  = flag.String("adopt-hash", "", "attach -topic-id identity to this existing qBittorrent hash and exit")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(2)
	}

	qbit, err := NewQbitClient(cfg.Qbit.URL, cfg.Qbit.APIKey)
	if err != nil {
		log.Error("qbit client", "err", err)
		os.Exit(1)
	}
	resolver := NewRutrackerForumResolver(cfg.Rutracker.ForumURL, cfg.Rutracker.FlareSolverrURL)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *adoptHash != "" {
		if !topicIDPattern.MatchString(*topicID) || !infoHashOnlyPattern.MatchString(*adoptHash) {
			log.Error("adoption requires a numeric -topic-id and a 40-hex -adopt-hash")
			os.Exit(2)
		}
		if torrent, err := qbit.TorrentByHash(ctx, *adoptHash); err != nil {
			log.Error("find torrent for adoption", "err", err)
			os.Exit(1)
		} else if torrent == nil {
			log.Error("torrent for adoption does not exist", "hash", *adoptHash)
			os.Exit(1)
		}
		if err := qbit.SetComment(ctx, *adoptHash, canonicalTopicURL(*topicID)); err != nil {
			log.Error("set torrent topic comment", "err", err)
			os.Exit(1)
		}
		if err := qbit.AddTags(ctx, *adoptHash, topicTag(*topicID)); err != nil {
			log.Error("set torrent topic tag", "err", err)
			os.Exit(1)
		}
		log.Info("adopted torrent topic identity", "hash", *adoptHash, "topic_id", *topicID)
		return
	}

	u := &Updater{
		qbit:          qbit,
		rutracker:     NewRutrackerAPI(),
		resolver:      resolver,
		log:           log,
		dryRun:        *dryRun,
		topicIDFilter: *topicID,
	}
	if err := u.Run(ctx); err != nil {
		log.Error("run finished with errors", "err", err)
		os.Exit(1)
	}
	log.Info("done")
}
