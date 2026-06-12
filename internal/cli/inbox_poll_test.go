package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jedwards1230/my-wiki/internal/notify"
)

// recordedChange is one RecordInboxFSChange call captured by fakeRecorder.
type recordedChange struct {
	Path   string
	Action notify.ChangeKind
}

// fakeRecorder records RecordInboxFSChange calls so a test can assert exactly
// which (path, action) pairs the poller emitted, without a real router.
type fakeRecorder struct{ calls []recordedChange }

func (f *fakeRecorder) RecordInboxFSChange(path string, action notify.ChangeKind) {
	f.calls = append(f.calls, recordedChange{Path: path, Action: action})
}

// writeInboxFile writes content to vault/inbox/<rel> and stamps its mtime.
func writeInboxFile(t *testing.T, vault, rel string, mtime time.Time) {
	t.Helper()
	writeInboxFileContent(t, vault, rel, "x", mtime)
}

// writeInboxFileContent is writeInboxFile with explicit content, so a test can
// vary file size while pinning mtime (exercising the size leg of the diff).
func writeInboxFileContent(t *testing.T, vault, rel, content string, mtime time.Time) {
	t.Helper()
	abs := filepath.Join(vault, "inbox", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	if err := os.Chtimes(abs, mtime, mtime); err != nil {
		t.Fatalf("chtimes %s: %v", rel, err)
	}
}

func TestScanInboxSnapshot(t *testing.T) {
	vault := t.TempDir()
	base := time.Unix(1_700_000_000, 0)
	writeInboxFile(t, vault, "clippings/a.md", base)
	writeInboxFile(t, vault, "b.md", base.Add(time.Hour))
	// Skipped: generated index, review-needed subtree, and a non-.md file.
	writeInboxFile(t, vault, "index.md", base)
	writeInboxFile(t, vault, "review-needed/c.md", base)
	if err := os.WriteFile(filepath.Join(vault, "inbox", "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}

	got, err := scanInboxSnapshot(vault)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 tracked files, got %d: %v", len(got), got)
	}
	if _, ok := got["inbox/clippings/a.md"]; !ok {
		t.Errorf("missing inbox/clippings/a.md in %v", got)
	}
	if _, ok := got["inbox/b.md"]; !ok {
		t.Errorf("missing inbox/b.md in %v", got)
	}
	for skipped := range map[string]bool{
		"inbox/index.md": true, "inbox/review-needed/c.md": true, "inbox/note.txt": true,
	} {
		if _, ok := got[skipped]; ok {
			t.Errorf("%s should be skipped but was tracked", skipped)
		}
	}
}

func TestScanInboxSnapshotNoInbox(t *testing.T) {
	got, err := scanInboxSnapshot(t.TempDir())
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map, got %v", got)
	}
}

func TestInboxPollerBaselineSilent(t *testing.T) {
	vault := t.TempDir()
	writeInboxFile(t, vault, "a.md", time.Unix(1_700_000_000, 0))

	rec := &fakeRecorder{}
	p := newInboxPoller(vault, rec, time.Minute, discardLogger())
	p.poll() // nothing changed since construction

	if len(rec.calls) != 0 {
		t.Fatalf("baseline files must not dispatch; got %v", rec.calls)
	}
}

func TestInboxPollerCreate(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "inbox"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &fakeRecorder{}
	p := newInboxPoller(vault, rec, time.Minute, discardLogger())

	writeInboxFile(t, vault, "clippings/new.md", time.Unix(1_700_000_100, 0))
	p.poll()

	if len(rec.calls) != 1 || rec.calls[0] != (recordedChange{"inbox/clippings/new.md", notify.ChangeCreated}) {
		t.Fatalf("want one ChangeCreated for inbox/clippings/new.md, got %v", rec.calls)
	}
}

func TestInboxPollerModify(t *testing.T) {
	vault := t.TempDir()
	writeInboxFile(t, vault, "a.md", time.Unix(1_700_000_000, 0))

	rec := &fakeRecorder{}
	p := newInboxPoller(vault, rec, time.Minute, discardLogger())

	writeInboxFile(t, vault, "a.md", time.Unix(1_700_000_500, 0)) // later mtime
	p.poll()

	if len(rec.calls) != 1 || rec.calls[0] != (recordedChange{"inbox/a.md", notify.ChangeModified}) {
		t.Fatalf("want one ChangeModified for inbox/a.md, got %v", rec.calls)
	}

	// A second poll with no further change must stay silent.
	rec.calls = nil
	p.poll()
	if len(rec.calls) != 0 {
		t.Fatalf("unchanged file must not re-dispatch; got %v", rec.calls)
	}
}

// TestInboxPollerModifySameMtimeDifferentSize covers the NFS 1s-resolution
// delete-recreate case: the file is replaced with identical mtime but a
// different byte count. mtime alone would miss it; the size leg of the
// signature catches it.
func TestInboxPollerModifySameMtimeDifferentSize(t *testing.T) {
	vault := t.TempDir()
	mtime := time.Unix(1_700_000_000, 0)
	writeInboxFileContent(t, vault, "a.md", "short", mtime)

	rec := &fakeRecorder{}
	p := newInboxPoller(vault, rec, time.Minute, discardLogger())

	writeInboxFileContent(t, vault, "a.md", "a much longer body", mtime) // same mtime, larger
	p.poll()

	if len(rec.calls) != 1 || rec.calls[0] != (recordedChange{"inbox/a.md", notify.ChangeModified}) {
		t.Fatalf("want one ChangeModified for same-mtime size change, got %v", rec.calls)
	}
}

func TestInboxPollerDelete(t *testing.T) {
	vault := t.TempDir()
	writeInboxFile(t, vault, "a.md", time.Unix(1_700_000_000, 0))

	rec := &fakeRecorder{}
	p := newInboxPoller(vault, rec, time.Minute, discardLogger())

	if err := os.Remove(filepath.Join(vault, "inbox", "a.md")); err != nil {
		t.Fatal(err)
	}
	p.poll()

	if len(rec.calls) != 1 || rec.calls[0] != (recordedChange{"inbox/a.md", notify.ChangeDeleted}) {
		t.Fatalf("want one ChangeDeleted for inbox/a.md, got %v", rec.calls)
	}
}

func TestInboxPollIntervalFromEnv(t *testing.T) {
	logger := discardLogger()
	tests := []struct {
		name string
		set  bool
		val  string
		want time.Duration
	}{
		{"unset uses default", false, "", defaultInboxPollInterval},
		{"empty uses default", true, "", defaultInboxPollInterval},
		{"custom duration", true, "30s", 30 * time.Second},
		{"zero disables", true, "0", 0},
		{"negative disables", true, "-5s", 0},
		{"invalid falls back to default", true, "banana", defaultInboxPollInterval},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(EnvInboxPollInterval, tt.val)
			} else if err := os.Unsetenv(EnvInboxPollInterval); err != nil {
				t.Fatalf("unsetenv: %v", err)
			}
			if got := inboxPollIntervalFromEnv(logger); got != tt.want {
				t.Fatalf("want %v, got %v", tt.want, got)
			}
		})
	}
}
