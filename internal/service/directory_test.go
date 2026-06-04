package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jedwards1230/my-wiki/internal/vault"
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
	if !strings.Contains(string(data), "My Wiki") {
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

// TestDirectoryService_Generate_RewritesIndexWhenDirGoesEmpty is the
// regression for the observed staleness bug: when the last content
// page under a directory is deleted, buildDirTree previously dropped
// the directory from the tree (it only visited dirs mentioned by the
// page list), so its index.md was never overwritten and the stale
// listing — still referencing the now-deleted page — persisted.
//
// The fix enumerates every non-excluded directory separately and seeds
// a tree node for each, guaranteeing every directory's index is
// rewritten on every Generate call.
func TestDirectoryService_Generate_RewritesIndexWhenDirGoesEmpty(t *testing.T) {
	v := setupDirectoryVault(t)
	svc := NewDirectoryService(v)

	// Add a short-lived page under a subtree that only holds sub-dirs
	// once the page is gone. We use a fresh subtree — "ephemeral/" —
	// with one page and one empty sub-directory. When we delete the
	// page, "ephemeral/" should still be regenerated so its index no
	// longer references the deleted page.
	ephemeralDir := filepath.Join(v.Dir, "ephemeral")
	if err := os.MkdirAll(filepath.Join(ephemeralDir, "keepers"), 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(ephemeralDir, "transient.md")
	if err := os.WriteFile(page, []byte("---\ntitle: Transient\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Marker page anchors keepers/ so it isn't pruned when transient.md
	// is deleted — keeps ephemeral/ alive too so the stale-index check
	// below still has a file to inspect.
	keeperPage := filepath.Join(ephemeralDir, "keepers", "marker.md")
	if err := os.WriteFile(keeperPage, []byte("---\ntitle: Marker\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First Generate: ephemeral/index.md should exist and mention the page.
	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}
	ephemeralIndex := filepath.Join(ephemeralDir, "index.md")
	before, err := os.ReadFile(ephemeralIndex)
	if err != nil {
		t.Fatalf("ephemeral/index.md missing after first Generate: %v", err)
	}
	if !strings.Contains(string(before), "transient") && !strings.Contains(string(before), "Transient") {
		t.Fatalf("ephemeral/index.md didn't list the page on first Generate; got:\n%s", before)
	}

	// Delete the page so ephemeral/ is left with only the keepers/
	// subdir. This is the exact scenario that triggered the production
	// bug — a directory whose only remaining content is a subdirectory
	// (itself containing only a generated index).
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}

	// Second Generate: without the fix, buildDirTree doesn't create a
	// node for ephemeral/ at all, so its stale index is left untouched
	// and the test's after-content assertion below fails.
	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	after, err := os.ReadFile(ephemeralIndex)
	if err != nil {
		t.Fatalf("ephemeral/index.md missing after second Generate: %v", err)
	}
	if strings.Contains(string(after), "Transient") || strings.Contains(string(after), "transient") {
		t.Fatalf("ephemeral/index.md still references the deleted page after Generate; got:\n%s", after)
	}
	// The subdir listing should still appear so users can navigate
	// into what's left of the tree.
	if !strings.Contains(string(after), "keepers") {
		t.Errorf("ephemeral/index.md lost its remaining subdir 'keepers' after regeneration; got:\n%s", after)
	}
}

// TestDirectoryService_Generate_PrunesEmptyDirs verifies the cleanup
// behavior: a directory whose pages have all been moved or deleted is
// removed entirely on the next Generate — stale index.md gone, dir
// rmdir'd if truly empty. Recursive: a parent that becomes empty when
// its child is pruned is itself pruned in the same pass.
func TestDirectoryService_Generate_PrunesEmptyDirs(t *testing.T) {
	v := setupDirectoryVault(t)
	svc := NewDirectoryService(v)

	// Seed: a clippings/ subtree with a single page (mirrors the real
	// "after we moved everything out of clippings/" state).
	clippingsDir := filepath.Join(v.Dir, "clippings")
	if err := os.MkdirAll(clippingsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(clippingsDir, "robot-dogs.md")
	if err := os.WriteFile(page, []byte("---\ntitle: Robot Dogs\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First Generate establishes clippings/index.md alongside the page.
	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(clippingsDir, "index.md")); err != nil {
		t.Fatalf("clippings/index.md should exist while page is present: %v", err)
	}

	// Remove the page — clippings/ now has only the auto-generated index.
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}

	// Second Generate should clean up the whole clippings/ subtree.
	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(clippingsDir, "index.md")); !os.IsNotExist(err) {
		t.Errorf("clippings/index.md should be deleted after page removal; stat err=%v", err)
	}
	if _, err := os.Stat(clippingsDir); !os.IsNotExist(err) {
		t.Errorf("clippings/ directory should be removed after page removal; stat err=%v", err)
	}
}

// TestDirectoryService_Generate_PrunesRecursively verifies that pruning
// propagates upward: when the only child of a parent is itself pruned,
// the parent becomes a leaf with no pages and is pruned in the same
// Generate pass.
func TestDirectoryService_Generate_PrunesRecursively(t *testing.T) {
	v := setupDirectoryVault(t)
	svc := NewDirectoryService(v)

	// Seed: a brand-new experiments/alpha/ subtree with a single page.
	// experiments/ doesn't exist in the base vault, so the recursive
	// prune is exclusively driven by this scenario (no sibling content
	// keeping the parent alive).
	deepDir := filepath.Join(v.Dir, "experiments", "alpha")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(deepDir, "draft.md")
	if err := os.WriteFile(page, []byte("---\ntitle: Draft\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	// Sanity check: both indexes exist while the page is present.
	for _, rel := range []string{"experiments/index.md", "experiments/alpha/index.md"} {
		if _, err := os.Stat(filepath.Join(v.Dir, rel)); err != nil {
			t.Fatalf("expected %s after first Generate: %v", rel, err)
		}
	}

	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	// Both experiments/alpha/ and experiments/ should be gone — the leaf
	// is pruned first, then the parent becomes empty and is pruned too.
	for _, rel := range []string{"experiments/alpha", "experiments"} {
		if _, err := os.Stat(filepath.Join(v.Dir, rel)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed after recursive prune; stat err=%v", rel, err)
		}
	}
}

// TestDirectoryService_Generate_PreservesDirWithNonMdFiles verifies the
// safety case: if a directory holds non-markdown files (e.g. dropped
// attachments, hidden metadata), the stale index.md is still deleted
// but the directory itself is preserved because os.Remove fails on a
// non-empty dir.
func TestDirectoryService_Generate_PreservesDirWithNonMdFiles(t *testing.T) {
	v := setupDirectoryVault(t)
	svc := NewDirectoryService(v)

	attachDir := filepath.Join(v.Dir, "with-attachments")
	if err := os.MkdirAll(attachDir, 0o755); err != nil {
		t.Fatal(err)
	}
	page := filepath.Join(attachDir, "note.md")
	if err := os.WriteFile(page, []byte("---\ntitle: Note\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-md sibling — should keep the directory alive after the page is gone.
	if err := os.WriteFile(filepath.Join(attachDir, "image.png"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(page); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	// index.md should be removed (no longer indexes any pages).
	if _, err := os.Stat(filepath.Join(attachDir, "index.md")); !os.IsNotExist(err) {
		t.Errorf("with-attachments/index.md should be removed; stat err=%v", err)
	}
	// But the directory and its non-md file must survive.
	if _, err := os.Stat(attachDir); err != nil {
		t.Errorf("with-attachments/ should survive (non-md content present); stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(attachDir, "image.png")); err != nil {
		t.Errorf("with-attachments/image.png should survive; stat err=%v", err)
	}
}

func TestDirectoryService_Generate_RecentsSection(t *testing.T) {
	dir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(dir, "research/aerospace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "home/homelab"), 0o755); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	// A large subtree (> recentsMinPages) with staggered mtimes so ordering is
	// deterministic. page-09 is newest, page-00 oldest.
	const n = 10
	for i := 0; i < n; i++ {
		name := filepath.Join(dir, "research/aerospace", fmt.Sprintf("page-%02d.md", i))
		content := fmt.Sprintf("---\ntitle: Page %02d\ntags: research\n---\n\nBody.\n", i)
		if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(name, mt, mt); err != nil {
			t.Fatal(err)
		}
	}

	// A small subtree (1 page) that must NOT get a recents section.
	smallPage := filepath.Join(dir, "home/homelab/cluster.md")
	if err := os.WriteFile(smallPage, []byte("---\ntitle: Cluster\ntags: homelab\n---\n\nBody.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	svc := NewDirectoryService(vault.New(dir))
	if _, _, err := svc.Generate(); err != nil {
		t.Fatal(err)
	}

	// Root index always has recents, newest first.
	rootData, err := os.ReadFile(filepath.Join(dir, "index.md"))
	if err != nil {
		t.Fatal(err)
	}
	root := string(rootData)
	if !strings.Contains(root, "## Recently Updated") {
		t.Errorf("root index missing Recently Updated section:\n%s", root)
	}
	newestIdx := strings.Index(root, "[[research/aerospace/page-09\\|Page 09]]")
	olderIdx := strings.Index(root, "[[research/aerospace/page-08\\|Page 08]]")
	if newestIdx < 0 || olderIdx < 0 {
		t.Fatalf("expected recent pages as wikilinks in root:\n%s", root)
	}
	if newestIdx > olderIdx {
		t.Errorf("expected page-09 before page-08 (newest first), got root:\n%s", root)
	}
	if !strings.Contains(root, "— 2026-01-01") {
		t.Errorf("expected absolute mtime date in recents, got:\n%s", root)
	}

	// Recents is capped at recentsLimit entries.
	recentsBlock := root[strings.Index(root, "## Recently Updated"):]
	if end := strings.Index(recentsBlock, "## Directory"); end >= 0 {
		recentsBlock = recentsBlock[:end]
	}
	if got := strings.Count(recentsBlock, "\n- "); got != recentsLimit {
		t.Errorf("expected %d recent entries, got %d:\n%s", recentsLimit, got, recentsBlock)
	}

	// Large subtree index gets its own recents.
	aeroData, err := os.ReadFile(filepath.Join(dir, "research/aerospace/index.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(aeroData), "## Recently Updated") {
		t.Errorf("large subtree index missing Recently Updated:\n%s", string(aeroData))
	}

	// Small subtree index must be gated out.
	smallData, err := os.ReadFile(filepath.Join(dir, "home/homelab/index.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(smallData), "## Recently Updated") {
		t.Errorf("small subtree should not get Recently Updated:\n%s", string(smallData))
	}
}
