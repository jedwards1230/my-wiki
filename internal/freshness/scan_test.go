package freshness

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

func TestScanTree(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)

	tests := []struct {
		name       string
		files      map[string]time.Time // rel path -> mtime
		dirs       map[string]time.Time // rel path -> mtime (no files inside)
		exclude    []string
		wantFiles  int
		wantNewest time.Time
	}{
		{
			name: "newest across nested subdirectories",
			files: map[string]time.Time{
				"a.md":                base,
				"one/b.md":            base.Add(2 * time.Hour),
				"one/two/three/c.md":  base.Add(time.Hour),
				"raw/attachment.pdf":  base.Add(30 * time.Minute),
				"one/two/d.canvas":    base,
				"one/two/three/e.txt": base,
			},
			wantFiles:  6,
			wantNewest: base.Add(2 * time.Hour),
		},
		{
			name: "excluded top-level dir contributes nothing",
			files: map[string]time.Time{
				"note.md":                     base,
				".obsidian/workspace.json":    base.Add(time.Hour),
				".obsidian/plugins/p/main.js": base.Add(2 * time.Hour),
			},
			exclude:    vault.DefaultExcludedDirs,
			wantFiles:  1,
			wantNewest: base,
		},
		{
			name: "same name nested deeper is not excluded",
			files: map[string]time.Time{
				"note.md":                 base,
				"sub/.obsidian/keep.json": base.Add(time.Hour),
			},
			exclude:    vault.DefaultExcludedDirs,
			wantFiles:  2,
			wantNewest: base.Add(time.Hour),
		},
		{
			name:       "directories never contribute",
			files:      map[string]time.Time{"note.md": base},
			dirs:       map[string]time.Time{"empty": base.Add(5 * time.Hour)},
			wantFiles:  1,
			wantNewest: base,
		},
		{
			name:       "empty tree",
			wantFiles:  0,
			wantNewest: time.Time{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, mtime := range tt.files {
				writeFile(t, root, rel, mtime)
			}
			for rel, mtime := range tt.dirs {
				abs := filepath.Join(root, filepath.FromSlash(rel))
				if err := os.MkdirAll(abs, 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", rel, err)
				}
				if err := os.Chtimes(abs, mtime, mtime); err != nil {
					t.Fatalf("chtimes %s: %v", rel, err)
				}
			}

			got, err := scanTree(root, tt.exclude)
			if err != nil {
				t.Fatalf("scanTree: %v", err)
			}
			if got.files != tt.wantFiles {
				t.Errorf("files = %d, want %d", got.files, tt.wantFiles)
			}
			if !got.newest.Equal(tt.wantNewest) {
				t.Errorf("newest = %v, want %v", got.newest, tt.wantNewest)
			}
		})
	}
}

// TestScanTreeMissingRoot: only a failure to stat the root is an error — that
// is what distinguishes "the vault is gone" from an unreadable single file.
func TestScanTreeMissingRoot(t *testing.T) {
	_, err := scanTree(filepath.Join(t.TempDir(), "nope"), nil)
	if err == nil {
		t.Fatal("scanTree on a missing root: want error, got nil")
	}
	if !os.IsNotExist(err) {
		t.Errorf("want a not-exist error, got %v", err)
	}
}

// TestScanTreeSymlinkedRoot: os.Stat follows symlinks but filepath.WalkDir
// lstats its root, so an unresolved symlinked vault would walk exactly one
// entry — the link itself — and report a single "file" whose mtime never
// changes. That is a permanently frozen gauge with no scan error to explain it,
// i.e. exactly the false staleness alarm this package exists to prevent.
func TestScanTreeSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	mtime := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, rel := range []string{"a.md", "notes/b.md", "notes/deep/c.md"} {
		abs := filepath.Join(real, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(abs, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	link := filepath.Join(t.TempDir(), "vault-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	got, err := scanTree(link, nil)
	if err != nil {
		t.Fatalf("scanTree(symlinked root): %v", err)
	}
	if got.files != 3 {
		t.Errorf("files = %d, want 3 (a symlinked root must walk the real tree)", got.files)
	}
	if !got.newest.Equal(mtime) {
		t.Errorf("newest = %v, want %v (got the link's own mtime?)", got.newest, mtime)
	}
}

// TestScanTreeRootNotADirectory: pointing --vault at a regular file would
// otherwise "succeed" with files=1 and that file's mtime, silently masquerading
// as a healthy one-file vault. It must be a scan error so the error counter
// moves and the gauges stay absent.
func TestScanTreeRootNotADirectory(t *testing.T) {
	f := filepath.Join(t.TempDir(), "not-a-dir.md")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := scanTree(f, nil); err == nil {
		t.Fatal("scanTree(regular file) returned nil error, want an error")
	}
}
