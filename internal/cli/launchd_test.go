package cli

import (
	"strings"
	"testing"
)

// Tests use synthetic paths under /example/... rather than real developer
// paths so the source doesn't bake in a username and stays portable across
// machines.

func TestRenderPlist_IncludesAllRequiredFields(t *testing.T) {
	cfg := plistConfig{
		Label:      "io.github.jedwards1230.my-wiki.lint.work-vault",
		BinaryPath: "/usr/local/bin/wiki-server",
		VaultDir:   "/example/Obsidian/work-vault",
		Hour:       9,
		Minute:     30,
		StdoutLog:  "/example/Library/Logs/my-wiki/lint/lint.log",
		StderrLog:  "/example/Library/Logs/my-wiki/lint/lint.err.log",
	}
	out, err := renderPlist(cfg)
	if err != nil {
		t.Fatalf("renderPlist: %v", err)
	}
	got := string(out)

	mustContain := []string{
		`<string>io.github.jedwards1230.my-wiki.lint.work-vault</string>`,
		`<string>/usr/local/bin/wiki-server</string>`,
		`<string>/example/Obsidian/work-vault</string>`,
		`<string>lint</string>`,
		`<integer>9</integer>`,
		`<integer>30</integer>`,
		`<string>/example/Library/Logs/my-wiki/lint/lint.log</string>`,
		`<string>/example/Library/Logs/my-wiki/lint/lint.err.log</string>`,
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
		"work-vault":        "work-vault",
		"home_vault":        "home_vault",
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

func TestRenderPlist_EscapesXMLSignificantChars(t *testing.T) {
	// Vault path with '&' would produce invalid XML if not escaped, and
	// launchctl load would reject the plist.
	cfg := plistConfig{
		Label:      "io.github.jedwards1230.my-wiki.lint",
		BinaryPath: "/usr/local/bin/wiki-server",
		VaultDir:   "/example/notes & <stuff>",
		Hour:       9,
		Minute:     0,
		StdoutLog:  "/tmp/log",
		StderrLog:  "/tmp/err",
	}
	out, err := renderPlist(cfg)
	if err != nil {
		t.Fatalf("renderPlist: %v", err)
	}
	got := string(out)

	// Raw chars must NOT appear in the body (only their escaped form).
	if strings.Contains(got, "& <stuff>") {
		t.Errorf("plist contains unescaped XML-significant chars; output:\n%s", got)
	}
	// Escaped form must be present.
	if !strings.Contains(got, "&amp; &lt;stuff&gt;") {
		t.Errorf("plist missing escaped form; output:\n%s", got)
	}
}

func TestPlistPathFor_UsesHomeLaunchAgents(t *testing.T) {
	got, err := plistPathFor("io.github.jedwards1230.my-wiki.lint")
	if err != nil {
		t.Fatalf("plistPathFor: %v", err)
	}
	// We don't hardcode $HOME, but the suffix must be stable.
	if !strings.HasSuffix(got, "/Library/LaunchAgents/io.github.jedwards1230.my-wiki.lint.plist") {
		t.Errorf("unexpected plist path: %s", got)
	}
}
