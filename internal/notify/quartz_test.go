package notify

import (
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQuartzBuilderRunsBuild(t *testing.T) {
	tmpDir := t.TempDir()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	qb := NewQuartzBuilder(tmpDir, "/fake/vault", "/fake/output", logger)
	// Use a stub command that exits immediately.
	qb.command = []string{"true"}

	qb.Build()

	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for builder to return to idle")
		case <-ticker.C:
			qb.mu.Lock()
			building := qb.building
			qb.mu.Unlock()
			if !building {
				return // success
			}
		}
	}
}

func TestQuartzBuilderCoalesces(t *testing.T) {
	tmpDir := t.TempDir()
	counterFile := tmpDir + "/count"
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a stub script that increments a counter file.
	script := "#!/bin/sh\ncount=$(cat \"" + counterFile + "\")\necho $((count + 1)) > \"" + counterFile + "\"\n"
	scriptPath := tmpDir + "/build.sh"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	qb := NewQuartzBuilder(tmpDir, "/fake/vault", "/fake/output", logger)
	qb.command = []string{scriptPath}

	// Fire many builds rapidly.
	for i := 0; i < 10; i++ {
		qb.Build()
	}

	// Wait for all builds to complete.
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for builder to return to idle")
		case <-ticker.C:
			qb.mu.Lock()
			building := qb.building
			qb.mu.Unlock()
			if !building {
				goto done
			}
		}
	}
done:

	qb.mu.Lock()
	pending := qb.pendingBuild
	qb.mu.Unlock()

	if pending {
		t.Fatal("expected no pending build after completion")
	}

	// The counter should be <= 2: one initial build + at most one coalesced build.
	data, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if count > 2 {
		t.Fatalf("expected at most 2 builds from coalescing, got %d", count)
	}
	if count < 1 {
		t.Fatalf("expected at least 1 build, got %d", count)
	}
}

func TestQuartzBuilderCoalescesWithCounter(t *testing.T) {
	tmpDir := t.TempDir()
	counterFile := tmpDir + "/count"
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a script that increments and sleeps briefly to simulate work.
	script := "#!/bin/sh\ncount=$(cat \"" + counterFile + "\")\necho $((count + 1)) > \"" + counterFile + "\"\nsleep 0.1\n"
	scriptPath := tmpDir + "/build.sh"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	qb := NewQuartzBuilder(tmpDir, "/fake/vault", "/fake/output", logger)
	qb.command = []string{scriptPath}

	// Trigger first build, then rapidly queue more while it runs.
	qb.Build()
	// Small delay to ensure the first build goroutine starts.
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 20; i++ {
		qb.Build()
	}

	// Wait for all builds to complete.
	deadline := time.After(10 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for builder to return to idle")
		case <-ticker.C:
			qb.mu.Lock()
			building := qb.building
			qb.mu.Unlock()
			if !building {
				goto done
			}
		}
	}
done:

	data, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatal(err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	// Coalescing means at most 2 builds: the initial one + one coalesced follow-up.
	if count > 2 {
		t.Fatalf("expected at most 2 builds from coalescing, got %d", count)
	}
	if count < 1 {
		t.Fatalf("expected at least 1 build, got %d", count)
	}
}
