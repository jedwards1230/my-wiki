package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/jedwards1230/my-wiki/internal/freshness"
)

func TestVaultFreshnessIntervalFromEnv(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name string
		env  string // "" means leave the variable unset
		want time.Duration
	}{
		{name: "unset uses the default", env: "", want: freshness.DefaultInterval},
		{name: "whitespace uses the default", env: "   ", want: freshness.DefaultInterval},
		{name: "valid duration", env: "90s", want: 90 * time.Second},
		{name: "valid duration with surrounding space", env: " 15m ", want: 15 * time.Minute},
		{name: "invalid falls back to the default", env: "banana", want: freshness.DefaultInterval},
		{name: "bare number is invalid, falls back", env: "300", want: freshness.DefaultInterval},
		{name: "zero disables", env: "0", want: 0},
		{name: "negative disables", env: "-1s", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvVaultFreshnessInterval, tt.env)
			if got := vaultFreshnessIntervalFromEnv(logger); got != tt.want {
				t.Errorf("vaultFreshnessIntervalFromEnv() = %v, want %v", got, tt.want)
			}
		})
	}
}

// defaultRegistryHasFreshness reports whether the freshness collector is
// currently registered on the default registry, by looking for a metric only it
// emits. wiki_vault_scan_errors_total is the right probe: it is exported from
// zero, so it is present whenever the collector is registered — even before any
// scan has succeeded.
func defaultRegistryHasFreshness(t *testing.T) bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather default registry: %v", err)
	}
	for _, f := range families {
		if f.GetName() == "wiki_vault_scan_errors_total" {
			return true
		}
	}
	return false
}

// TestStartFreshnessObserverDisabled: a non-positive interval must skip
// registration entirely, so a disabled observer leaves no collector behind on
// the default registry.
//
// This asserts against the default registry on purpose. An earlier version of
// this test merely called the function twice and asserted nothing — it passed
// even with the interval guard deleted, because the second registration just
// returned an AlreadyRegisteredError that got logged and swallowed.
func TestStartFreshnessObserverDisabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv(EnvVaultFreshnessInterval, "0")

	if defaultRegistryHasFreshness(t) {
		t.Skip("freshness collector already on the default registry; another test registered it")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	startFreshnessObserver(ctx, t.TempDir(), logger)

	if defaultRegistryHasFreshness(t) {
		t.Fatal("disabled observer registered a collector on the default registry")
	}
}

// TestStartFreshnessObserverEnabled covers the wiring path that actually ships:
// a positive interval must register the collector and start scanning. Without
// this, nothing tests that startFreshnessObserver does anything at all.
func TestStartFreshnessObserverEnabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv(EnvVaultFreshnessInterval, "50ms")

	if defaultRegistryHasFreshness(t) {
		t.Skip("freshness collector already on the default registry; another test registered it")
	}

	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "note.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write note: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	observer := startFreshnessObserver(ctx, vaultDir, logger)
	if observer == nil {
		t.Fatal("startFreshnessObserver returned nil for a positive interval")
	}
	// Leave the default registry as we found it, so test ordering (including
	// -shuffle) cannot make one test's registration break another's.
	t.Cleanup(func() { prometheus.DefaultRegisterer.Unregister(observer) })

	if !defaultRegistryHasFreshness(t) {
		t.Fatal("enabled observer did not register a collector on the default registry")
	}

	// Run's immediate first scan should publish a last-scan heartbeat promptly.
	deadline := time.Now().Add(5 * time.Second)
	for {
		families, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		found := false
		for _, f := range families {
			if f.GetName() == "wiki_vault_last_scan_timestamp_seconds" {
				found = true
			}
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no wiki_vault_last_scan_timestamp_seconds after 5s; observer never scanned")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
