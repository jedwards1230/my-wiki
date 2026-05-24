package dispatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "dispatch.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const validMinimal = `
events:
  inbox.changed:
    prompt: inbox-manager

consumers:
  - name: n8n-primary
    url: https://n8n.example.com/webhook/my-wiki
    events:
      - inbox.changed
    secret_env: WIKI_WEBHOOK_N8N_SECRET

wiki:
  base_url: https://wiki.example.com
  mcp_url: https://wiki.example.com/mcp
`

func TestLoadConfig_EmptyPath(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil cfg, got %+v", cfg)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil cfg, got %+v", cfg)
	}
}

func TestLoadConfig_ValidMinimal(t *testing.T) {
	path := writeTempConfig(t, validMinimal)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	// Defaults applied.
	if cfg.Defaults.Timeout.D != 10*time.Second {
		t.Errorf("timeout default: got %v, want 10s", cfg.Defaults.Timeout.D)
	}
	if cfg.Defaults.Retries.MaxAttempts != 5 {
		t.Errorf("max_attempts default: got %d, want 5", cfg.Defaults.Retries.MaxAttempts)
	}
	if cfg.Defaults.Retries.InitialBackoff.D != 2*time.Second {
		t.Errorf("initial_backoff default: got %v, want 2s", cfg.Defaults.Retries.InitialBackoff.D)
	}
	if cfg.Defaults.Retries.MaxBackoff.D != 2*time.Minute {
		t.Errorf("max_backoff default: got %v, want 2m", cfg.Defaults.Retries.MaxBackoff.D)
	}
	if w := cfg.DebounceWindow(EventInboxChanged); w != 90*time.Second {
		t.Errorf("inbox.changed debounce default: got %v, want 90s", w)
	}

	if len(cfg.Consumers) != 1 {
		t.Fatalf("expected 1 consumer, got %d", len(cfg.Consumers))
	}
	c := cfg.Consumers[0]
	if c.Name != "n8n-primary" {
		t.Errorf("consumer name: got %q", c.Name)
	}
	if c.SecretEnv != "WIKI_WEBHOOK_N8N_SECRET" {
		t.Errorf("secret_env: got %q", c.SecretEnv)
	}
}

func TestLoadConfig_OverridesAndExplicitDebounce(t *testing.T) {
	body := `
defaults:
  timeout: 5s
  retries:
    max_attempts: 2
    initial_backoff: 500ms
    max_backoff: 30s
  debounce:
    inbox.changed: 45s

events:
  inbox.changed:
    prompt: inbox-manager

consumers:
  - name: n8n
    url: https://n8n.example.com/webhook
    events: [inbox.changed]
    secret_env: WIKI_WEBHOOK_N8N_SECRET
    timeout: 20s
    retries:
      max_attempts: 7

wiki:
  base_url: https://wiki.example.com
  mcp_url: https://wiki.example.com/mcp
`
	cfg, err := LoadConfig(writeTempConfig(t, body))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Defaults.Timeout.D != 5*time.Second {
		t.Errorf("timeout override: %v", cfg.Defaults.Timeout.D)
	}
	if cfg.DebounceWindow(EventInboxChanged) != 45*time.Second {
		t.Errorf("debounce override: %v", cfg.DebounceWindow(EventInboxChanged))
	}

	c := cfg.Consumers[0]
	to := c.EffectiveTimeout(cfg.Defaults)
	if to != 20*time.Second {
		t.Errorf("EffectiveTimeout: got %v want 20s", to)
	}
	r := c.EffectiveRetries(cfg.Defaults)
	if r.MaxAttempts != 7 {
		t.Errorf("EffectiveRetries.MaxAttempts: got %d want 7", r.MaxAttempts)
	}
	// Unset per-consumer backoff values should fall back to defaults.
	if r.InitialBackoff.D != 500*time.Millisecond {
		t.Errorf("EffectiveRetries.InitialBackoff: got %v want 500ms", r.InitialBackoff.D)
	}
	if r.MaxBackoff.D != 30*time.Second {
		t.Errorf("EffectiveRetries.MaxBackoff: got %v want 30s", r.MaxBackoff.D)
	}
}

func TestLoadConfig_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring of error
	}{
		{
			name: "empty consumer name",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: ""
    url: https://x/y
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "name must be non-empty",
		},
		{
			name: "duplicate consumer name",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: dup
    url: https://x/1
    events: [inbox.changed]
    secret_env: S
  - name: dup
    url: https://x/2
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "duplicate consumer name",
		},
		{
			name: "bad url scheme",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: ftp://x.example.com/webhook
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "scheme must be http or https",
		},
		{
			name: "missing url host",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https:///path
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "url must include a host",
		},
		{
			name: "empty events list",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: []
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "events list must be non-empty",
		},
		{
			name: "unknown event reference",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [some.other]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: `references unknown event "some.other"`,
		},
		{
			name: "empty secret_env",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    secret_env: ""
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "secret_env must be non-empty",
		},
		{
			name: "empty prompt",
			body: `
events:
  inbox.changed: {prompt: ""}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "prompt must be non-empty",
		},
		{
			name: "include filter empty entry",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    path_filters:
      include: ["inbox/", ""]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "path_filters.include[1]",
		},
		{
			name: "include filter entry with trailing whitespace",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    path_filters:
      include: ["inbox/ "]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "path_filters.include[0] must not have leading or trailing whitespace",
		},
		{
			name: "exclude filter entry with leading whitespace",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    path_filters:
      include: ["inbox/"]
      exclude: [" inbox/drafts/"]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "path_filters.exclude[0] must not have leading or trailing whitespace",
		},
		{
			name: "prompt with trailing whitespace",
			body: `
events:
  inbox.changed: {prompt: "inbox-manager "}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "prompt must not have leading or trailing whitespace",
		},
		{
			name: "event key with leading whitespace",
			body: `
events:
  " inbox.changed": {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "event key must not have leading or trailing whitespace",
		},
		{
			name: "missing wiki base_url with consumers",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    secret_env: S
wiki: {mcp_url: https://w/m}
`,
			want: "wiki.base_url must be set",
		},
		{
			name: "missing wiki mcp_url with consumers",
			body: `
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w}
`,
			want: "wiki.mcp_url must be set",
		},
		{
			name: "negative timeout",
			body: `
defaults:
  timeout: -1s
events:
  inbox.changed: {prompt: inbox-manager}
consumers:
  - name: x
    url: https://x/y
    events: [inbox.changed]
    secret_env: S
wiki: {base_url: https://w, mcp_url: https://w/m}
`,
			want: "timeout must be non-negative",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(writeTempConfig(t, tc.body))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestLoadConfig_NoConsumersAllowsMissingWikiURLs(t *testing.T) {
	body := `
events:
  inbox.changed:
    prompt: inbox-manager
`
	cfg, err := LoadConfig(writeTempConfig(t, body))
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if cfg.Wiki.BaseURL != "" {
		t.Errorf("expected empty base_url, got %q", cfg.Wiki.BaseURL)
	}
}

func TestDurationParsesString(t *testing.T) {
	body := `
defaults:
  timeout: 7s
events:
  inbox.changed: {prompt: p}
`
	cfg, err := LoadConfig(writeTempConfig(t, body))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Timeout.D != 7*time.Second {
		t.Errorf("timeout: got %v want 7s", cfg.Defaults.Timeout.D)
	}
}
