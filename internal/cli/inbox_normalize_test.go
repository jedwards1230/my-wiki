package cli

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeInboxFilenames(t *testing.T) {
	dir := t.TempDir()
	mkdirs := []string{"inbox/clippings", "inbox/review-needed"}
	for _, d := range mkdirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	body := "---\ntitle: x\n---\nbody\n"
	files := map[string]string{
		"inbox/clippings/Thinking Machines’ Murati on AI’s Next Chapter.md": body,
		"inbox/clippings/Staff archetypes.md":                               body,
		"inbox/clippings/index.md":                                          body, // generated — must be left alone
		"inbox/clippings/already-clean.md":                                  body, // no rename
		"inbox/review-needed/Human Curated.md":                              body, // skipped dir
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	renamed := normalizeInboxFilenames(dir, logger)
	if renamed != 2 {
		t.Fatalf("expected 2 renames, got %d", renamed)
	}

	mustExist := []string{
		"inbox/clippings/thinking-machines-murati-on-ais-next-chapter.md",
		"inbox/clippings/staff-archetypes.md",
		"inbox/clippings/index.md",
		"inbox/clippings/already-clean.md",
		"inbox/review-needed/Human Curated.md", // untouched
	}
	for _, p := range mustExist {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}

	mustNotExist := []string{
		"inbox/clippings/Thinking Machines’ Murati on AI’s Next Chapter.md",
		"inbox/clippings/Staff archetypes.md",
	}
	for _, p := range mustNotExist {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); !os.IsNotExist(err) {
			t.Errorf("expected %s to be gone, err=%v", p, err)
		}
	}
}

func TestNormalizeInboxFilenamesCollision(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "inbox/clippings"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\ntitle: x\n---\nbody\n"
	// An unsafe name that slugifies onto an already-existing clean file.
	for _, name := range []string{"inbox/clippings/staff-archetypes.md", "inbox/clippings/Staff Archetypes.md"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if renamed := normalizeInboxFilenames(dir, logger); renamed != 1 {
		t.Fatalf("expected 1 rename, got %d", renamed)
	}
	// Collision avoided with a numeric suffix; original clean file preserved.
	for _, p := range []string{"inbox/clippings/staff-archetypes.md", "inbox/clippings/staff-archetypes-2.md"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
}

func TestNormalizeInboxFilenamesNoInbox(t *testing.T) {
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if renamed := normalizeInboxFilenames(dir, logger); renamed != 0 {
		t.Fatalf("expected 0 renames with no inbox, got %d", renamed)
	}
}
