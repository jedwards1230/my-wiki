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
