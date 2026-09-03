package rules

import "testing"

func gmxRules() []FolderRule {
	return []FolderRule{
		{Folder: "Sort/#Luftfahrt/LSVRP", Domains: []string{"lsvrp.de"}},
		{Folder: "Sort/Bahn", Domains: []string{"bahn.de", "deutschebahn.com"}},
		{Folder: "Sort/PayPal", Domains: []string{"paypal.de"}},
		{Folder: "Sort/VIP", Exact: []string{"boss@example.com"}},
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		name       string
		sender     string
		wantFolder string
		wantOK     bool
	}{
		{"domain match", "noreply@paypal.de", "Sort/PayPal", true},
		{"domain match mixed case and spacing", "  NoReply@PayPal.DE  ", "Sort/PayPal", true},
		{"domain match subdomain", "info@service.deutschebahn.com", "Sort/Bahn", true},
		{"domain match does not match unrelated domain sharing a suffix", "info@notdeutschebahn.com", "", false},
		{"domain match does not match domain as suffix of a longer host", "info@deutschebahn.com.evil.com", "", false},
		{"domain match does not match domain string in local part", "deutschebahn.com@evil.com", "", false},
		{"exact match", "boss@example.com", "Sort/VIP", true},
		{"exact match wrong domain still falls through", "boss@example.de", "", false},
		{"no match", "someone@unrelated.org", "", false},
		{"empty sender", "", "", false},
		{"first rule wins", "x@lsvrp.de", "Sort/#Luftfahrt/LSVRP", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			folder, ok := Match(tc.sender, gmxRules())
			if ok != tc.wantOK || folder != tc.wantFolder {
				t.Errorf("Match(%q) = (%q, %v), want (%q, %v)", tc.sender, folder, ok, tc.wantFolder, tc.wantOK)
			}
		})
	}
}

func TestMatchFirstRuleWinsOverLaterMatchingRule(t *testing.T) {
	rules := []FolderRule{
		{Folder: "Sort/First", Domains: []string{"example.com"}},
		{Folder: "Sort/Second", Domains: []string{"example.com"}},
	}
	folder, ok := Match("a@example.com", rules)
	if !ok || folder != "Sort/First" {
		t.Errorf("Match() = (%q, %v), want (\"Sort/First\", true)", folder, ok)
	}
}

func TestNormalizeLowercasesMatchListsButNotFolder(t *testing.T) {
	r := FolderRule{
		Folder:  "  Sort/PayPal  ",
		Exact:   []string{"  Boss@Example.COM "},
		Domains: []string{" PayPal.DE "},
	}
	r.Normalize()

	// IMAP mailbox names are case-sensitive, so the folder is only trimmed.
	if r.Folder != "Sort/PayPal" {
		t.Errorf("Folder = %q, want %q", r.Folder, "Sort/PayPal")
	}
	if r.Exact[0] != "boss@example.com" {
		t.Errorf("Exact[0] = %q, want %q", r.Exact[0], "boss@example.com")
	}
	if r.Domains[0] != "paypal.de" {
		t.Errorf("Domains[0] = %q, want %q", r.Domains[0], "paypal.de")
	}

	if folder, ok := Match("NoReply@PayPal.de", []FolderRule{r}); !ok || folder != "Sort/PayPal" {
		t.Errorf("Match() after Normalize = (%q, %v), want (\"Sort/PayPal\", true)", folder, ok)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		rule    FolderRule
		wantErr bool
	}{
		{"ok with domains", FolderRule{Folder: "Sort/A", Domains: []string{"a.example"}}, false},
		{"ok with exact", FolderRule{Folder: "Sort/A", Exact: []string{"a@a.example"}}, false},
		{"missing folder", FolderRule{Domains: []string{"a.example"}}, true},
		// The common typo: `domain:` instead of `domains:` leaves a rule
		// that parses fine and can never match anything.
		{"no exact and no domains", FolderRule{Folder: "Sort/A"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.rule.Validate(); (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
