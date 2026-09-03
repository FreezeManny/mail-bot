// Package notify defines a pluggable notification channel abstraction.
//
// Telegram ships as the only built-in implementation, but the channel type
// is never hardcoded into the sorting logic: each channel implements the
// small Notifier interface and registers a Factory under a config `type`
// name (see Register). Adding a new channel (Slack, a generic webhook,
// SMTP, ntfy, ...) later is purely additive.
package notify

import (
	"context"
	"fmt"
	"time"
)

// Event is passed to every Notifier attached to the account it originated
// from. Either it describes a single sorted message (Subject/Sender/Folder
// set), or it's a freeform status update (Message set) - e.g. "connection
// failed: ...", "connection restored" - fired once on a failure/recovery
// transition rather than on every occurrence. Account and Time are always
// set.
type Event struct {
	Account string
	Subject string
	Sender  string
	Folder  string
	Message string
	Time    time.Time
}

// Notifier delivers a notification for a sorted message over one channel.
type Notifier interface {
	// Name is the notifier instance's config name, used for logging.
	Name() string
	Notify(ctx context.Context, ev Event) error
}

// RawConfig is one entry of the top-level `notifiers:` config list. Fields
// specific to a given `type` are captured in Options rather than modeled
// here, so adding a new notifier type never requires changing this struct.
type RawConfig struct {
	Name    string            `yaml:"name"`
	Type    string            `yaml:"type"`
	Options map[string]string `yaml:",inline"`
}

// Factory builds a Notifier instance from one RawConfig entry's Options.
type Factory func(name string, opts map[string]string) (Notifier, error)

var factories = map[string]Factory{}

// Register adds a Factory for notifier config `type` typ. Implementations
// call this from an init() function so registering a new channel is a
// self-contained, one-line addition.
func Register(typ string, f Factory) {
	factories[typ] = f
}

// Build constructs every notifier described by cfgs, keyed by its config
// name.
func Build(cfgs []RawConfig) (map[string]Notifier, error) {
	result := make(map[string]Notifier, len(cfgs))
	for _, cfg := range cfgs {
		if cfg.Name == "" {
			return nil, fmt.Errorf("notify: notifier entry missing required field \"name\"")
		}
		// A name identifies one channel, and accounts reference notifiers
		// by name, so two entries sharing a name would silently collapse
		// into one here while still being listed twice for an account that
		// defaults to "all notifiers" - notifying that channel twice per
		// event.
		if _, dup := result[cfg.Name]; dup {
			return nil, fmt.Errorf("notify: duplicate notifier name %q", cfg.Name)
		}
		factory, ok := factories[cfg.Type]
		if !ok {
			return nil, fmt.Errorf("notify: notifier %q: unknown type %q", cfg.Name, cfg.Type)
		}
		n, err := factory(cfg.Name, cfg.Options)
		if err != nil {
			return nil, fmt.Errorf("notify: building notifier %q: %w", cfg.Name, err)
		}
		result[cfg.Name] = n
	}
	return result, nil
}
