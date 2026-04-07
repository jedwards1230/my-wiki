package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupLogVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(dir, "meta", "activity"), 0o755)

	// Create log index
	logContent := `---
title: Activity Log
tags:
  - meta
date: 2026-01-01
---

## [2026-04-06] 3 changes | ` + "`abcdef`" + ` | Last edit | [[meta/activity/2026-04-06]]
## [2026-04-05] 2 changes | ` + "`123456`" + ` | Something | [[meta/activity/2026-04-05]]
## [2026-04-04] 1 changes | ` + "`fedcba`" + ` | First entry | [[meta/activity/2026-04-04]]
`
	os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte(logContent), 0o644)

	// Create activity files
	activityContent := `---
title: "2026-04-06"
tags:
  - meta/activity
date: 2026-04-06
---

### 10:00 | create | First thing
Created a page.

### 14:30 | edit | Second thing
Edited stuff.

### 16:00 | note | Third thing
Just a note.
`
	os.WriteFile(filepath.Join(dir, "meta", "activity", "2026-04-06.md"), []byte(activityContent), 0o644)

	return dir
}

func TestLogIndex(t *testing.T) {
	dir := setupLogVault(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "log"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	// Should show all index lines
	if !strings.Contains(output, "2026-04-06") {
		t.Errorf("expected 2026-04-06 in output:\n%s", output)
	}
	if !strings.Contains(output, "2026-04-04") {
		t.Errorf("expected 2026-04-04 in output:\n%s", output)
	}
}

func TestLogIndexN(t *testing.T) {
	dir := setupLogVault(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "log", "-n", "1"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(output, "2026-04-04") {
		t.Errorf("expected last entry (2026-04-04), got:\n%s", output)
	}
}

func TestLogDay(t *testing.T) {
	dir := setupLogVault(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "log", "2026-04-06"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	// Should show only ### lines
	if !strings.Contains(output, "### 10:00") {
		t.Errorf("expected activity headers, got:\n%s", output)
	}
	// Should NOT show full content
	if strings.Contains(output, "Created a page") {
		t.Errorf("should not show detail without --detail, got:\n%s", output)
	}
}

func TestLogDayDetail(t *testing.T) {
	dir := setupLogVault(t)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "log", "2026-04-06", "--detail"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatal(err)
	}

	var buf [4096]byte
	n, _ := r.Read(buf[:])
	output := string(buf[:n])

	if !strings.Contains(output, "Created a page") {
		t.Errorf("expected full content with --detail, got:\n%s", output)
	}
}

func TestLogDayMissing(t *testing.T) {
	dir := setupLogVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "log", "2099-01-01"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("expected error for missing day, got nil")
	}
}

func TestLogLint(t *testing.T) {
	dir := setupLogVault(t)

	// The hash in the fixture won't match the actual file content
	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "log", "lint"})
	err := cmd.Execute()

	// Should report hash mismatch and missing activity files
	if err == nil {
		t.Fatal("expected lint errors, got nil")
	}
}

func TestLogLint_Clean(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "meta", "activity"), 0o755)

	// Create an empty log index (no entries to mismatch)
	os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte("---\ntitle: Log\n---\n"), 0o644)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "log", "lint"})
	err := cmd.Execute()

	if err != nil {
		t.Fatalf("expected clean lint, got: %v", err)
	}
}
