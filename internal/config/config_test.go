package config

import (
	"os"
	"path/filepath"
	"testing"
)

const testYAML = `
poll_interval_seconds: 30

notifiers:
  - name: telegram-main
    type: telegram
    bot_token_env: TEST_BOT_TOKEN
    chat_id_env: TEST_CHAT_ID
  - name: ops-webhook
    type: telegram
    bot_token_env: TEST_BOT_TOKEN
    chat_id_env: TEST_CHAT_ID

accounts:
  - name: gmx
    host: imap.gmx.net
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    notifiers: [telegram-main, ops-webhook]
    folder_rules:
      - folder: "Sort/PayPal"
        domains: ["paypal.de"]
`

func TestLoad(t *testing.T) {
	t.Setenv("TEST_BOT_TOKEN", "token123")
	t.Setenv("TEST_CHAT_ID", "chat123")
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(testYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.PollInterval.Seconds() != 30 {
		t.Errorf("PollInterval = %v, want 30s", cfg.PollInterval)
	}
	if len(cfg.Notifiers) != 2 {
		t.Errorf("len(Notifiers) = %d, want 2 (multi-notifier fan-out)", len(cfg.Notifiers))
	}
	if len(cfg.Accounts) != 1 {
		t.Fatalf("len(Accounts) = %d, want 1", len(cfg.Accounts))
	}
	acc := cfg.Accounts[0]
	if acc.Port != defaultIMAPPort {
		t.Errorf("Port = %d, want default %d", acc.Port, defaultIMAPPort)
	}
	if acc.Password() != "hunter2" {
		t.Errorf("Password() = %q, want hunter2", acc.Password())
	}
	if len(acc.Notifiers) != 2 {
		t.Errorf("account notifiers = %v, want 2 entries", acc.Notifiers)
	}
}

func TestLoadDefaultsOmittedNotifiersToAll(t *testing.T) {
	t.Setenv("TEST_BOT_TOKEN", "token123")
	t.Setenv("TEST_CHAT_ID", "chat123")
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yaml := `
notifiers:
  - name: telegram-main
    type: telegram
    bot_token_env: TEST_BOT_TOKEN
    chat_id_env: TEST_CHAT_ID
  - name: ops-webhook
    type: telegram
    bot_token_env: TEST_BOT_TOKEN
    chat_id_env: TEST_CHAT_ID

accounts:
  - name: gmx
    host: imap.gmx.net
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    folder_rules:
      - folder: "Sort/PayPal"
        domains: ["paypal.de"]
  - name: also-omitted
    host: imap.gmx.net
    username: user2@gmx.de
    password_env: TEST_GMX_PASSWORD
    notifiers: []
    folder_rules:
      - folder: "Sort/PayPal"
        domains: ["paypal.de"]
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	got := cfg.Accounts[0].Notifiers
	if len(got) != 2 || got[0] != "telegram-main" || got[1] != "ops-webhook" {
		t.Errorf("account with omitted notifiers = %v, want [telegram-main ops-webhook]", got)
	}

	explicit := cfg.Accounts[1].Notifiers
	if len(explicit) != 0 {
		t.Errorf("account with explicit notifiers: [] = %v, want empty (opt-out honored, not defaulted)", explicit)
	}
}

func TestLoadRejectsUnknownNotifier(t *testing.T) {
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	bad := `
accounts:
  - name: gmx
    host: imap.gmx.net
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    notifiers: [does-not-exist]
    folder_rules:
      - folder: "Sort/PayPal"
        domains: ["paypal.de"]
`
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want error for unknown notifier reference")
	}
}

// loadYAML writes body to a temp config.yaml and loads it.
func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestLoadRejectsUnusableFolderRules(t *testing.T) {
	cases := map[string]string{
		// `domain:` instead of `domains:` - parses, but could never match.
		"rule with neither exact nor domains": `
accounts:
  - name: gmx
    host: imap.gmx.net
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    folder_rules:
      - folder: "Sort/PayPal"
        domain: ["paypal.de"]
`,
		"rule with no folder": `
accounts:
  - name: gmx
    host: imap.gmx.net
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    folder_rules:
      - domains: ["paypal.de"]
`,
		"port out of range": `
accounts:
  - name: gmx
    host: imap.gmx.net
    port: 99999
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    folder_rules:
      - folder: "Sort/PayPal"
        domains: ["paypal.de"]
`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TEST_GMX_PASSWORD", "hunter2")
			if _, err := loadYAML(t, body); err == nil {
				t.Fatal("Load() error = nil, want a startup error rather than a silent no-op at runtime")
			}
		})
	}
}

func TestLoadRejectsDuplicateNotifierNames(t *testing.T) {
	t.Setenv("TEST_BOT_TOKEN", "token123")
	t.Setenv("TEST_CHAT_ID", "chat123")
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")

	_, err := loadYAML(t, `
notifiers:
  - name: telegram-main
    type: telegram
    bot_token_env: TEST_BOT_TOKEN
    chat_id_env: TEST_CHAT_ID
  - name: telegram-main
    type: telegram
    bot_token_env: TEST_BOT_TOKEN
    chat_id_env: TEST_CHAT_ID

accounts:
  - name: gmx
    host: imap.gmx.net
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    folder_rules:
      - folder: "Sort/PayPal"
        domains: ["paypal.de"]
`)
	if err == nil {
		t.Fatal("Load() error = nil, want error for two notifiers sharing one name")
	}
}

func TestLoadNormalizesFolderRules(t *testing.T) {
	t.Setenv("TEST_GMX_PASSWORD", "hunter2")

	cfg, err := loadYAML(t, `
accounts:
  - name: gmx
    host: imap.gmx.net
    username: user@gmx.de
    password_env: TEST_GMX_PASSWORD
    folder_rules:
      - folder: "Sort/PayPal"
        exact: ["Service@PayPal.DE"]
        domains: ["  PayPal.DE  "]
`)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// rules.Match relies on Load having normalized the rules, so a
	// mixed-case config entry has to arrive lowercased.
	rule := cfg.Accounts[0].FolderRules[0]
	if rule.Domains[0] != "paypal.de" || rule.Exact[0] != "service@paypal.de" {
		t.Errorf("folder rule = %+v, want lowercased exact/domains", rule)
	}
	if rule.Folder != "Sort/PayPal" {
		t.Errorf("Folder = %q, want case preserved (IMAP names are case-sensitive)", rule.Folder)
	}
}
