package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/manuelfritz/mail-bot/internal/config"
	"github.com/manuelfritz/mail-bot/internal/notify"
	"github.com/manuelfritz/mail-bot/internal/rules"
)

type fakeNotifier struct{ name string }

func (f fakeNotifier) Name() string                               { return f.name }
func (f fakeNotifier) Notify(context.Context, notify.Event) error { return nil }

func testConfig() *config.Config {
	return &config.Config{
		PollInterval: 30 * time.Second,
		Notifiers: map[string]notify.Notifier{
			"telegram-main": fakeNotifier{name: "telegram-main"},
			"ops-webhook":   fakeNotifier{name: "ops-webhook"},
		},
		Accounts: []config.Account{{
			Name:        "gmx",
			Host:        "imap.gmx.net",
			Port:        993,
			Username:    "user@gmx.de",
			PasswordEnv: "TEST_GMX_PASSWORD",
			Notifiers:   []string{"telegram-main", "ops-webhook"},
			FolderRules: []rules.FolderRule{{
				Folder:  "Sort/PayPal",
				Domains: []string{"paypal.de"},
			}},
		}},
	}
}

// Every worker input has to be resolved here rather than looked up later,
// so the props a worker is handed carry the secret and the poll interval
// and the process-wide dry-run switch, not the means to find them.
func TestAccountPropsResolvesEverything(t *testing.T) {
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	props, err := accountProps(testConfig(), true, log)
	if err != nil {
		t.Fatalf("accountProps() error = %v", err)
	}
	if len(props) != 1 {
		t.Fatalf("len(props) = %d, want 1", len(props))
	}

	p := props[0]
	if p.Name != "gmx" || p.Host != "imap.gmx.net" || p.Port != 993 || p.Username != "user@gmx.de" {
		t.Errorf("mailbox props = %+v, want the config's account identity", p)
	}
	if p.Password != "hunter2" {
		t.Errorf("Password = %q, want the value of password_env resolved at startup", p.Password)
	}
	if p.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want the config's 30s", p.PollInterval)
	}
	if !p.DryRun {
		t.Error("DryRun = false, want the process-wide switch passed through")
	}
	if p.Logger == nil {
		t.Error("Logger = nil, want the process logger")
	}
	if len(p.FolderRules) != 1 || p.FolderRules[0].Folder != "Sort/PayPal" {
		t.Errorf("FolderRules = %+v, want the account's one rule", p.FolderRules)
	}
	if len(p.Notifiers) != 2 {
		t.Fatalf("len(Notifiers) = %d, want 2 notifier instances, not names", len(p.Notifiers))
	}
	for i, want := range []string{"telegram-main", "ops-webhook"} {
		if got := p.Notifiers[i].Name(); got != want {
			t.Errorf("Notifiers[%d].Name() = %q, want %q (config order preserved)", i, got, want)
		}
	}
}

// An account that names no notifier is silent, which is a valid choice and
// not an error.
func TestAccountPropsAllowsNoNotifiers(t *testing.T) {
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")
	cfg := testConfig()
	cfg.Accounts[0].Notifiers = nil

	props, err := accountProps(cfg, false, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("accountProps() error = %v", err)
	}
	if len(props[0].Notifiers) != 0 {
		t.Errorf("len(Notifiers) = %d, want 0", len(props[0].Notifiers))
	}
}

// A name with no notifier behind it would mean an account that silently
// stops alerting, so it has to stop startup instead.
func TestAccountPropsRejectsUnknownNotifier(t *testing.T) {
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")
	cfg := testConfig()
	cfg.Accounts[0].Notifiers = []string{"telegram-main", "gone"}

	if _, err := accountProps(cfg, false, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("accountProps() error = nil, want an error naming the unknown notifier")
	}
}
