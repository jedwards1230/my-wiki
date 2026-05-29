package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

func setupLogVault(t *testing.T) vault.Storage {
	t.Helper()
	dir := t.TempDir()

	_ = os.MkdirAll(filepath.Join(dir, "meta", "activity"), 0o755)

	logContent := "---\ntitle: Activity Log\ntags:\n  - meta\ndate: 2026-01-01\n---\n\n" +
		"## [[meta/activity/2026-04-06|2026-04-06]] 3 changes | `abcdef` | Last edit\n" +
		"## [[meta/activity/2026-04-05|2026-04-05]] 2 changes | `123456` | Something\n" +
		"## [[meta/activity/2026-04-04|2026-04-04]] 1 changes | `fedcba` | First entry\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte(logContent), 0o644)

	activityContent := "---\ntitle: \"2026-04-06\"\ntags:\n  - meta/activity\ndate: 2026-04-06\n---\n\n" +
		"### 10:00 | create | First thing\nCreated a page.\n\n" +
		"### 14:30 | edit | Second thing\nEdited stuff.\n\n" +
		"### 16:00 | note | Third thing\nJust a note.\n"
	_ = os.WriteFile(filepath.Join(dir, "meta", "activity", "2026-04-06.md"), []byte(activityContent), 0o644)

	return vault.NewFilesystemStorage(dir)
}

func TestLogService_Index(t *testing.T) {
	storage := setupLogVault(t)
	svc := NewLogService(storage)

	entries, err := svc.Index(0)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].Date != "2026-04-06" {
		t.Errorf("expected first date 2026-04-06, got %s", entries[0].Date)
	}
	if entries[0].Changes != 3 {
		t.Errorf("expected 3 changes, got %d", entries[0].Changes)
	}
	if entries[0].Hash != "abcdef" {
		t.Errorf("expected hash abcdef, got %s", entries[0].Hash)
	}
}

func TestLogService_IndexN(t *testing.T) {
	storage := setupLogVault(t)
	svc := NewLogService(storage)

	entries, err := svc.Index(1)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Date != "2026-04-04" {
		t.Errorf("expected last entry 2026-04-04, got %s", entries[0].Date)
	}
}

func TestLogService_Day(t *testing.T) {
	storage := setupLogVault(t)
	svc := NewLogService(storage)

	dayLog, err := svc.Day("2026-04-06", false)
	if err != nil {
		t.Fatal(err)
	}

	if dayLog.Date != "2026-04-06" {
		t.Errorf("expected date 2026-04-06, got %s", dayLog.Date)
	}
	if len(dayLog.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(dayLog.Entries))
	}
	if dayLog.Entries[0].Time != "10:00" {
		t.Errorf("expected time 10:00, got %s", dayLog.Entries[0].Time)
	}
	if dayLog.Entries[0].Type != "create" {
		t.Errorf("expected type create, got %s", dayLog.Entries[0].Type)
	}
	// Without detail, summary should be empty
	if dayLog.Entries[0].Summary != "" {
		t.Errorf("expected empty summary without detail, got %q", dayLog.Entries[0].Summary)
	}
}

func TestLogService_DayDetail(t *testing.T) {
	storage := setupLogVault(t)
	svc := NewLogService(storage)

	dayLog, err := svc.Day("2026-04-06", true)
	if err != nil {
		t.Fatal(err)
	}

	if dayLog.Entries[0].Summary != "Created a page." {
		t.Errorf("expected summary 'Created a page.', got %q", dayLog.Entries[0].Summary)
	}
}

func TestLogService_DayMissing(t *testing.T) {
	storage := setupLogVault(t)
	svc := NewLogService(storage)

	_, err := svc.Day("2099-01-01", false)
	if err == nil {
		t.Fatal("expected error for missing day")
	}
}

func TestLogService_Lint(t *testing.T) {
	storage := setupLogVault(t)
	svc := NewLogService(storage)

	issues, err := svc.Lint()
	if err != nil {
		t.Fatal(err)
	}

	// Should find hash mismatches and missing activity files
	if len(issues) == 0 {
		t.Fatal("expected lint issues")
	}
}

func TestLogService_LintClean(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "meta", "activity"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "meta", "log.md"), []byte("---\ntitle: Log\n---\n"), 0o644)

	svc := NewLogService(vault.NewFilesystemStorage(dir))

	issues, err := svc.Lint()
	if err != nil {
		t.Fatal(err)
	}

	if len(issues) != 0 {
		t.Errorf("expected 0 issues, got %d", len(issues))
	}
}
