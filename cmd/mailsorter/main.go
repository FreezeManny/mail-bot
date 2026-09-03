// Command mailsorter watches a set of IMAP accounts and sorts incoming mail
// into folders based on per-account rules, replacing the n8n Email Sorter
// workflow. See config.example.yaml for the config format.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/manuelfritz/mail-bot/internal/account"
	"github.com/manuelfritz/mail-bot/internal/config"
)

func main() {
	os.Exit(run())
}

func run() int {
	// The level has to be resolved before there is a logger to report a bad
	// one with, so fall back to info for the report itself and then exit.
	level, levelErr := parseLogLevel(envOr("LOG_LEVEL", "info"))
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
	}))
	if levelErr != nil {
		log.Error("invalid environment", "error", levelErr)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// DRY_RUN is the switch for checking rule changes without touching any
	// mailbox, so a value that isn't a boolean has to stop the process.
	// Falling back to false would quietly start moving real mail, which is
	// the exact outcome whoever set the variable was trying to avoid.
	dryRun, err := envBool("DRY_RUN", false)
	if err != nil {
		log.Error("invalid environment", "error", err)
		return 1
	}

	configPath := envOr("CONFIG_PATH", "/app/config.yaml")
	cfg, err := config.Load(configPath)
	if err != nil {
		log.Error("failed to load config", "error", err)
		return 1
	}

	if dryRun {
		log.Warn("DRY_RUN enabled: no folders will be created, no messages moved, no notifications sent")
	}

	var wg sync.WaitGroup
	for _, acc := range cfg.Accounts {
		w := account.New(acc, cfg.Notifiers, cfg.PollInterval, dryRun, log)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Run(ctx)
		}()
	}

	log.Info("mailsorter started", "accounts", len(cfg.Accounts))
	<-ctx.Done()
	log.Info("shutting down")
	wg.Wait()
	return 0
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean: use true or false", key, v)
	}
	return b, nil
}

func parseLogLevel(s string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL=%q is not a level: use debug, info, warn or error", s)
	}
	return level, nil
}
