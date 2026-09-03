// Package config loads and validates the service's config.yaml plus the
// environment variables it references for secrets.
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/manuelfritz/mail-bot/internal/notify"
	"github.com/manuelfritz/mail-bot/internal/rules"
)

// Account is one IMAP mailbox to watch and sort. If Notifiers is omitted in
// config.yaml, it defaults to every notifier declared in the top-level
// notifiers: list, so accounts that all use the same one channel (the
// common case) don't need to repeat it.
type Account struct {
	Name        string             `yaml:"name"`
	Host        string             `yaml:"host"`
	Port        int                `yaml:"port"`
	Username    string             `yaml:"username"`
	PasswordEnv string             `yaml:"password_env"`
	Notifiers   []string           `yaml:"notifiers"`
	FolderRules []rules.FolderRule `yaml:"folder_rules"`
}

// Password reads the account's IMAP password from its configured env var.
func (a Account) Password() string {
	return os.Getenv(a.PasswordEnv)
}

// Config is the fully loaded and validated service configuration.
type Config struct {
	PollInterval time.Duration
	Notifiers    map[string]notify.Notifier
	Accounts     []Account
}

type rawConfig struct {
	PollIntervalSeconds int                `yaml:"poll_interval_seconds"`
	Notifiers           []notify.RawConfig `yaml:"notifiers"`
	Accounts            []Account          `yaml:"accounts"`
}

const (
	defaultPollIntervalSeconds = 60
	defaultIMAPPort            = 993
	maxPort                    = 65535
)

// Load reads and validates the config file at path, building every
// configured notifier along the way.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: reading %s: %w", path, err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("config: parsing %s: %w", path, err)
	}

	if raw.PollIntervalSeconds <= 0 {
		raw.PollIntervalSeconds = defaultPollIntervalSeconds
	}

	notifiers, err := notify.Build(raw.Notifiers)
	if err != nil {
		return nil, err
	}
	allNotifierNames := make([]string, len(raw.Notifiers))
	for i, n := range raw.Notifiers {
		allNotifierNames[i] = n.Name
	}

	if len(raw.Accounts) == 0 {
		return nil, fmt.Errorf("config: no accounts defined")
	}

	for i, acc := range raw.Accounts {
		if acc.Name == "" || acc.Host == "" || acc.Username == "" || acc.PasswordEnv == "" {
			return nil, fmt.Errorf("config: account %d: name, host, username and password_env are all required", i)
		}
		if acc.Password() == "" {
			return nil, fmt.Errorf("config: account %q: env var %s (password_env) is not set", acc.Name, acc.PasswordEnv)
		}
		if acc.Port < 0 || acc.Port > maxPort {
			return nil, fmt.Errorf("config: account %q: port %d is not a valid TCP port", acc.Name, acc.Port)
		}
		if acc.Port == 0 {
			raw.Accounts[i].Port = defaultIMAPPort
		}
		if len(acc.FolderRules) == 0 {
			return nil, fmt.Errorf("config: account %q: no folder_rules defined", acc.Name)
		}
		// Normalized once here so rules.Match doesn't redo it for every
		// rule on every message, and validated so a rule that could never
		// match anything fails at startup instead of silently sorting
		// nothing.
		for j := range raw.Accounts[i].FolderRules {
			rule := &raw.Accounts[i].FolderRules[j]
			rule.Normalize()
			if err := rule.Validate(); err != nil {
				return nil, fmt.Errorf("config: account %q: folder_rules[%d]: %w", acc.Name, j, err)
			}
		}
		if acc.Notifiers == nil {
			raw.Accounts[i].Notifiers = allNotifierNames
			continue
		}
		for _, n := range acc.Notifiers {
			if _, ok := notifiers[n]; !ok {
				return nil, fmt.Errorf("config: account %q: references unknown notifier %q", acc.Name, n)
			}
		}
	}

	return &Config{
		PollInterval: time.Duration(raw.PollIntervalSeconds) * time.Second,
		Notifiers:    notifiers,
		Accounts:     raw.Accounts,
	}, nil
}
