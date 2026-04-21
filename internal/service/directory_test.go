package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

func setupDirectoryVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()

	for _, sub := range []string{"home/homelab", "meta", "research/aerospace"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	files := map[string]string{
		"home/note.md":               "---\ntitle: Home Note\ntags: home\n---\n\nBody.\n",
		"home/homelab/cluster.md":    "---\ntitle: Cluster\ntags: homelab\ndescription: k3s cluster\n---\n\nBody.\n",
		"meta/schema.md":             "---\ntitle: Schema\ntags: meta\n---\n\nBody.\n",
		"research/aerospace/nasa.md": "---\ntitle: NASA\ntags: research\n---\n\nBody.\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return vault.New(dir)
}

func TestDirectoryService_Generate(t *testing.T) {
	v := setupDirectoryVault(t)
	svc := NewDirectoryService(v)

	rootPath, pageCount, err := svc.Generate()
	if err != nil {
		t.Fatal(err)
	}

	if pageCount != 4 {
		t.Errorf("expected 4 pages, got %d", pageCount)
	}

	// Root and each directory containing pages should have an index.md.
	for _, rel := range []string{
		"index.md",
		"home/index.md",
		"home/homelab/index.md",
		"meta/index.md",
		"research/index.md",
		"research/aerospace/index.md",
	} {
		full := filepath.Join(v.Dir, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("expected index file at %s: %v", rel, err)
		}
	}

	data, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Home Wiki") {
		t.Error("missing root title in generated index")
	}
}

// stampPast sets the given file's atime and mtime to a fixed past instant and
// returns the actual mtime the filesystem stored (which can differ from the
// requested time under coarse-resolution mtime, e.g. 1s on HFS+). Comparing
// against the stored value — not the requested value — is what makes the
// idempotency assertions deterministic without sleeping.
func stampPast(t *testing.T, path string) time.Time {
	t.Helper()
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime()
}

// TestDirectoryService_Generate_Idempotent guards against an fsnotify feedback
// loop: when nothing in the vault has changed, a second Generate call must not
// rewrite any index files. Rewriting with identical bytes still bumps mtime,
// which fsnotify reports as a Write event — causing the watcher to re-queue a
// rebuild that calls Generate again, forever.
//
// We force each index file's mtime backwards via os.Chtimes rather than
// sleeping past filesystem mtime resolution: it's deterministic, ~instant, and
// immune to unlikely-but-possible date-rollover during the test (the root
// index embeds today's date, so a sleep that crossed midnight would produce a
// false positive).
func TestDirectoryService_Generate_Idempotent(t *testing.T) {
	v := setupDirectoryVault(t)
	svc := NewDirectoryService(v)

	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	indexFiles := []string{
		"index.md",
		"home/index.md",
		"home/homelab/index.md",
		"meta/index.md",
		"research/index.md",
		"research/aerospace/index.md",
	}
	stamped := make(map[string]time.Time, len(indexFiles))
	for _, rel := range indexFiles {
		stamped[rel] = stampPast(t, filepath.Join(v.Dir, rel))
	}

	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	for _, rel := range indexFiles {
		info, err := os.Stat(filepath.Join(v.Dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(stamped[rel]) {
			t.Errorf("index %s was rewritten on second Generate (mtime changed %v → %v); "+
				"this will cause an fsnotify rebuild loop in serve mode",
				rel, stamped[rel], info.ModTime())
		}
	}
}

// TestDirectoryService_Generate_WritesOnContentChange ensures the idempotency
// guard doesn't swallow legitimate updates: adding a new page must produce a
// write to the index files that now need to list it.
func TestDirectoryService_Generate_WritesOnContentChange(t *testing.T) {
	v := setupDirectoryVault(t)
	svc := NewDirectoryService(v)

	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	homeIndex := filepath.Join(v.Dir, "home/index.md")
	stamped := stampPast(t, homeIndex)

	// Add a new page under home/ — the home/index.md should be rewritten.
	newPage := filepath.Join(v.Dir, "home/new-page.md")
	if err := os.WriteFile(newPage, []byte("---\ntitle: New Page\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(homeIndex)
	if err != nil {
		t.Fatal(err)
	}
	if info.ModTime().Equal(stamped) {
		t.Error("home/index.md mtime unchanged after adding a new page — fix is too aggressive")
	}

	data, err := os.ReadFile(homeIndex)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "new-page") {
		t.Error("home/index.md does not reference the newly added page")
	}
}
