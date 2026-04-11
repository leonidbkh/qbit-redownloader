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

	qbit, err := NewQbitClient(cfg.Qbit.URL, cfg.Qbit.Username, cfg.Qbit.Password)
	if err != nil {
		log.Error("qbit client", "err", err)
		os.Exit(1)
	}
	prowlarr := NewProwlarrClient(cfg.Prowlarr.URL, cfg.Prowlarr.APIKey)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	u := &Updater{
		qbit:      qbit,
		prowlarr:  prowlarr,
		rutracker: NewRutrackerAPI(),
		log:       log,
		dryRun:    *dryRun,
	}
	if err := u.Run(ctx); err != nil {
		log.Error("run finished with errors", "err", err)
		os.Exit(1)
	}
	log.Info("done")
}
