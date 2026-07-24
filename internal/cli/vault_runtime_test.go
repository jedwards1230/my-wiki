package cli

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

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

// TestStartFreshnessObserverDisabled: a non-positive interval must skip
// registration entirely, so a disabled observer leaves no half-configured
// collector on the default registry.
func TestStartFreshnessObserverDisabled(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	t.Setenv(EnvVaultFreshnessInterval, "0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Called twice: with registration skipped, the second call cannot collide
	// on the default registry.
	startFreshnessObserver(ctx, t.TempDir(), logger)
	startFreshnessObserver(ctx, t.TempDir(), logger)
}
