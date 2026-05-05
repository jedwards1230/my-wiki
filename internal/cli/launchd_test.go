package cli

import (
	"strings"
	"testing"
)

func TestRenderPlist_IncludesAllRequiredFields(t *testing.T) {
	cfg := plistConfig{
		Label:      "cloud.lilbro.home-wiki.lint.work-wiki",
		BinaryPath: "/usr/local/bin/wiki-server",
		VaultDir:   "/Users/justin/Obsidian/work-wiki",
		Hour:       9,
		Minute:     30,
		StdoutLog:  "/Users/justin/Library/Logs/home-wiki/lint/lint.log",
		StderrLog:  "/Users/justin/Library/Logs/home-wiki/lint/lint.err.log",
	}
	out, err := renderPlist(cfg)
	if err != nil {
		t.Fatalf("renderPlist: %v", err)
	}
	got := string(out)

	mustContain := []string{
		`<string>cloud.lilbro.home-wiki.lint.work-wiki</string>`,
		`<string>/usr/local/bin/wiki-server</string>`,
		`<string>/Users/justin/Obsidian/work-wiki</string>`,
		`<string>lint</string>`,
		`<integer>9</integer>`,
		`<integer>30</integer>`,
		`<string>/Users/justin/Library/Logs/home-wiki/lint/lint.log</string>`,
		`<string>/Users/justin/Library/Logs/home-wiki/lint/lint.err.log</string>`,
		`<key>RunAtLoad</key>`,
		`<false/>`,
	}
	for _, want := range mustContain {
		if !strings.Contains(got, want) {
			t.Errorf("plist missing %q\n--- output ---\n%s", want, got)
		}
	}
}

func TestSanitizeLabel(t *testing.T) {
	cases := map[string]string{
		"work-wiki":         "work-wiki",
		"home_wiki":         "home_wiki",
		"my.instance":       "my.instance",
		"weird name":        "weird-name",
		"slash/path":        "slash-path",
		"with$special@char": "with-special-char",
		"":                  "",
		"123":               "123",
	}
	for in, want := range cases {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPlistPathFor_UsesHomeLaunchAgents(t *testing.T) {
	got, err := plistPathFor("cloud.lilbro.home-wiki.lint")
	if err != nil {
		t.Fatalf("plistPathFor: %v", err)
	}
	// We don't hardcode $HOME, but the suffix must be stable.
	if !strings.HasSuffix(got, "/Library/LaunchAgents/cloud.lilbro.home-wiki.lint.plist") {
		t.Errorf("unexpected plist path: %s", got)
	}
}
