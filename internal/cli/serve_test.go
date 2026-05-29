package cli

import (
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/middleware"
)

// requireAuthConfig calls t.Fatal if cfg is nil and returns it typed for
// subsequent field access. The nolint suppresses SA5011 which cannot see
// through t.Fatal as a control-flow terminator.
func requireAuthConfig(t *testing.T, cfg *middleware.AuthConfig) middleware.AuthConfig {
	t.Helper()
	if cfg == nil { //nolint:staticcheck // SA5011 false positive: t.Fatal terminates
		t.Fatal("expected non-nil config")
	}
	return *cfg //nolint:staticcheck // SA5011: unreachable when cfg is nil (t.Fatal above)
}

func TestAuthConfigFromEnvUnset(t *testing.T) {
	t.Setenv("WIKI_AUTH_ISSUER", "")
	t.Setenv("WIKI_AUTH_AUDIENCE", "")
	t.Setenv("WIKI_AUTH_ALLOWED_GROUPS", "")
	t.Setenv("WIKI_AUTH_ALLOW_ANY_USER", "")
	if cfg := authConfigFromEnv(); cfg != nil {
		t.Errorf("expected nil when WIKI_AUTH_ISSUER is unset, got %+v", cfg)
	}
}

func TestAuthConfigFromEnvBasic(t *testing.T) {
	t.Setenv("WIKI_AUTH_ISSUER", "https://auth.example.com")
	t.Setenv("WIKI_AUTH_AUDIENCE", "wiki")
	t.Setenv("WIKI_AUTH_ALLOWED_GROUPS", "admins, wiki-editors ,")
	t.Setenv("WIKI_AUTH_ALLOW_ANY_USER", "")

	cfg := requireAuthConfig(t, authConfigFromEnv())
	if cfg.IssuerURL != "https://auth.example.com" {
		t.Errorf("IssuerURL = %q", cfg.IssuerURL)
	}
	if cfg.Audience != "wiki" {
		t.Errorf("Audience = %q", cfg.Audience)
	}
	want := []string{"admins", "wiki-editors"}
	if !reflect.DeepEqual(cfg.AllowedGroups, want) {
		t.Errorf("AllowedGroups = %v, want %v", cfg.AllowedGroups, want)
	}
	if cfg.AllowAnyUser {
		t.Error("AllowAnyUser should default to false")
	}
}

func TestAuthConfigFromEnvResourceMetadataURL(t *testing.T) {
	t.Setenv("WIKI_AUTH_ISSUER", "https://auth.example.com")
	t.Setenv("WIKI_AUTH_AUDIENCE", "wiki")
	t.Setenv("WIKI_AUTH_ALLOWED_GROUPS", "admins")
	t.Setenv("WIKI_AUTH_ALLOW_ANY_USER", "")
	t.Setenv("WIKI_AUTH_RESOURCE_METADATA_URL", "https://wiki.example.com/.well-known/oauth-protected-resource")

	cfg := requireAuthConfig(t, authConfigFromEnv())
	if cfg.ResourceMetadataURL != "https://wiki.example.com/.well-known/oauth-protected-resource" {
		t.Errorf("ResourceMetadataURL = %q", cfg.ResourceMetadataURL)
	}
}

func TestAuthConfigFromEnvResourceMetadataURLEmpty(t *testing.T) {
	t.Setenv("WIKI_AUTH_ISSUER", "https://auth.example.com")
	t.Setenv("WIKI_AUTH_AUDIENCE", "wiki")
	t.Setenv("WIKI_AUTH_ALLOWED_GROUPS", "admins")
	t.Setenv("WIKI_AUTH_ALLOW_ANY_USER", "")
	t.Setenv("WIKI_AUTH_RESOURCE_METADATA_URL", "")

	cfg := requireAuthConfig(t, authConfigFromEnv())
	if cfg.ResourceMetadataURL != "" {
		t.Errorf("ResourceMetadataURL should be empty, got %q", cfg.ResourceMetadataURL)
	}
}

func TestExcludeDirsFromEnv(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want []string
	}{
		{"empty uses defaults", "", defaultWatchExcludeDirs},
		{"override single", "ephemeral", []string{"ephemeral"}},
		{"override multi", ".obsidian,raw,private,trash", []string{".obsidian", "raw", "private", "trash"}},
		{"trims whitespace", " foo , bar ,baz", []string{"foo", "bar", "baz"}},
		{"skips blank entries", "foo,,bar,", []string{"foo", "bar"}},
		{"whitespace-only disables exclusions", "  ", []string{}},
		{"lone comma disables exclusions", ",", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WIKI_WATCH_EXCLUDE_DIRS", tc.env)
			got := excludeDirsFromEnv()
			// Treat nil and []string{} as equal — both represent "no exclusions".
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("excludeDirsFromEnv() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAuthConfigFromEnvAllowAnyUser(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"true", true},
		{"TRUE", true},
		{"True", true},
		{"false", false},
		{"", false},
		{"1", false}, // only accept "true" (case-insensitive); explicit opt-in
		{"yes", false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("WIKI_AUTH_ISSUER", "https://auth.example.com")
			t.Setenv("WIKI_AUTH_AUDIENCE", "wiki")
			t.Setenv("WIKI_AUTH_ALLOWED_GROUPS", "")
			t.Setenv("WIKI_AUTH_ALLOW_ANY_USER", tc.value)
			cfg := requireAuthConfig(t, authConfigFromEnv())
			if cfg.AllowAnyUser != tc.want {
				t.Errorf("AllowAnyUser = %v, want %v (env=%q)", cfg.AllowAnyUser, tc.want, tc.value)
			}
		})
	}
}

func TestRequireAuthOrAck(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Auth configured → always nil regardless of WIKI_AUTH_DISABLED.
	t.Setenv(EnvAuthDisabled, "")
	if err := requireAuthOrAck(logger, true, "HTTP REST API"); err != nil {
		t.Errorf("authConfigured=true: got err %v, want nil", err)
	}

	// Auth NOT configured and not acknowledged → error (fail closed).
	t.Setenv(EnvAuthDisabled, "")
	if err := requireAuthOrAck(logger, false, "HTTP REST API"); err == nil {
		t.Error("authConfigured=false, unset: got nil, want error (fail closed)")
	}

	// Auth NOT configured but explicitly acknowledged → nil (with warning).
	for _, v := range []string{"true", "TRUE", "True", "1"} {
		t.Setenv(EnvAuthDisabled, v)
		if err := requireAuthOrAck(logger, false, "MCP HTTP server"); err != nil {
			t.Errorf("WIKI_AUTH_DISABLED=%q: got err %v, want nil", v, err)
		}
	}

	// Non-truthy values do not acknowledge → error.
	for _, v := range []string{"false", "0", "no", "yes", "2"} {
		t.Setenv(EnvAuthDisabled, v)
		if err := requireAuthOrAck(logger, false, "HTTP REST API"); err == nil {
			t.Errorf("WIKI_AUTH_DISABLED=%q: got nil, want error", v)
		}
	}
}
