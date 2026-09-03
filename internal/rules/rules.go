// Package rules implements the sender-to-folder matching used to decide
// where an email should be sorted.
package rules

import (
	"errors"
	"fmt"
	"strings"
)

// FolderRule maps a set of exact sender addresses or sender domains to a
// target IMAP folder. The first rule in a list that matches a sender wins.
type FolderRule struct {
	Folder  string   `yaml:"folder"`
	Exact   []string `yaml:"exact"`
	Domains []string `yaml:"domains"`
}

// Normalize lowercases and trims the rule's match lists in place, so Match
// can compare against them directly instead of re-normalizing every rule
// for every message. Folder is only trimmed, never lowercased: IMAP
// mailbox names are case-sensitive. config.Load calls this once per rule
// at startup.
func (r *FolderRule) Normalize() {
	r.Folder = strings.TrimSpace(r.Folder)
	for i, exact := range r.Exact {
		r.Exact[i] = normalize(exact)
	}
	for i, domain := range r.Domains {
		r.Domains[i] = normalize(domain)
	}
}

// Validate reports whether the rule can actually do anything. A rule with
// no folder has no move target, and a rule with neither exact: nor
// domains: can never match - almost always a typo (`domain:` for
// `domains:`), which would otherwise silently sort nothing at all.
func (r FolderRule) Validate() error {
	if r.Folder == "" {
		return errors.New("folder is required")
	}
	if len(r.Exact) == 0 && len(r.Domains) == 0 {
		return fmt.Errorf("folder %q: needs at least one of exact: or domains:", r.Folder)
	}
	return nil
}

// Match returns the target folder for sender against folderRules, and
// whether any rule matched. sender is normalized here; folderRules are
// expected to have been normalized already (see Normalize). Matching is
// therefore case-insensitive; a domain rule matches the sender's address
// host exactly or as a subdomain (so "example.com" matches
// "user@example.com" and "user@mail.example.com", but not
// "user@notexample.com" or "user@example.com.evil.com").
func Match(sender string, folderRules []FolderRule) (string, bool) {
	sender = normalize(sender)
	if sender == "" {
		return "", false
	}
	_, host, _ := strings.Cut(sender, "@")

	for _, rule := range folderRules {
		for _, exact := range rule.Exact {
			if sender == exact {
				return rule.Folder, true
			}
		}
		for _, domain := range rule.Domains {
			if matchesDomain(host, domain) {
				return rule.Folder, true
			}
		}
	}

	return "", false
}

func matchesDomain(host, domain string) bool {
	return host != "" && domain != "" && (host == domain || strings.HasSuffix(host, "."+domain))
}

func normalize(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
