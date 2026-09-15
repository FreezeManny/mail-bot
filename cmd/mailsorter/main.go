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
	"github.com/manuelfritz/mail-bot/internal/notify"
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

	// Everything the workers run on is resolved here, before any of them
	// starts: config entries, secrets out of the environment and notifier
	// names into notifier instances. A worker is then handed a finished
	// account.Props and looks nothing up for itself, so a missing env var
	// or an unknown notifier name is a startup failure with a clear message
	// rather than something that surfaces mid-run inside one account.
	props, err := accountProps(cfg, dryRun, log)
	if err != nil {
		log.Error("failed to resolve accounts", "error", err)
		return 1
	}

	if dryRun {
		log.Warn("DRY_RUN enabled: no folders will be created, no messages moved, no notifications sent")
	}

	var wg sync.WaitGroup
	for _, p := range props {
		w := account.New(p)
		wg.Add(1)
		go func() {
			defer wg.Done()
			w.Run(ctx)
		}()
	}

	log.Info("mailsorter started", "accounts", len(props))
	<-ctx.Done()
	log.Info("shutting down")
	wg.Wait()
	return 0
}

// accountProps turns the loaded config into one fully resolved account.Props
// per account.
func accountProps(cfg *config.Config, dryRun bool, log *slog.Logger) ([]account.Props, error) {
	props := make([]account.Props, 0, len(cfg.Accounts))
	for _, acc := range cfg.Accounts {
		notifiers, err := resolveNotifiers(acc, cfg.Notifiers)
		if err != nil {
			return nil, err
		}
		props = append(props, account.Props{
			Name:         acc.Name,
			Host:         acc.Host,
			Port:         acc.Port,
			Username:     acc.Username,
			Password:     acc.Password(),
			FolderRules:  acc.FolderRules,
			Notifiers:    notifiers,
			PollInterval: cfg.PollInterval,
			DryRun:       dryRun,
			Logger:       log,
		})
	}
	return props, nil
}

// resolveNotifiers looks acc's notifier names up in the built notifiers.
// config.Load has already rejected unknown names, so a miss here means the
// two have drifted apart - reported rather than silently dropped, since a
// dropped notifier means an account that quietly stops alerting.
func resolveNotifiers(acc config.Account, byName map[string]notify.Notifier) ([]notify.Notifier, error) {
	notifiers := make([]notify.Notifier, 0, len(acc.Notifiers))
	for _, name := range acc.Notifiers {
		n, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("account %q: unknown notifier %q", acc.Name, name)
		}
		notifiers = append(notifiers, n)
	}
	return notifiers, nil
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
