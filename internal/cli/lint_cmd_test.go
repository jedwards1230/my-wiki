package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func setupLintVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	dirs := []string{"private", ".obsidian", "meta", "project"}
	for _, d := range dirs {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}

	files := map[string]string{
		"index.md":          "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\nWelcome. See [[project/alpha]] and [[meta/schema]].\n",
		"meta/schema.md":    "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nLinks to [[index]].\n",
		"project/alpha.md":  "---\ntitle: Alpha\ntags:\n  - project\ndate: 2026-02-01\n---\n\nSee [[meta/schema]].\n",
		"orphan.md":         "---\ntitle: Orphan\ntags:\n  - test\ndate: 2026-03-01\n---\n\nNo links here.\n",
		"no-frontmatter.md": "Just text.\n",
		"missing-tags.md":   "---\ntitle: No Tags\ndate: 2026-01-01\n---\n\nMissing tags.\n",
	}

	for name, content := range files {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(content), 0o644)
	}

	return dir
}

func TestLintFrontmatter(t *testing.T) {
	dir := setupLintVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "lint", "frontmatter"})

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := cmd.Execute()
	// Should fail because of missing frontmatter and missing tags
	if err == nil {
		t.Fatal("expected error for missing frontmatter, got nil")
	}
}

func TestLintLinks(t *testing.T) {
	dir := setupLintVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "lint", "links"})

	err := cmd.Execute()
	// All links in the vault should resolve
	if err != nil {
		t.Fatalf("expected no broken links, got: %v", err)
	}
}

func TestLintLinks_Broken(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "page.md"), []byte("---\ntitle: Page\ntags:\n  - test\ndate: 2026-01-01\n---\n\n[[nonexistent]]\n"), 0o644)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "lint", "links"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for broken link, got nil")
	}
}

func TestLintOrphans(t *testing.T) {
	dir := setupLintVault(t)

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "lint", "orphans"})

	err := cmd.Execute()
	// orphan.md, no-frontmatter.md, missing-tags.md have no inbound links
	if err == nil {
		t.Fatal("expected error for orphan pages, got nil")
	}
}

func TestLintAll_Clean(t *testing.T) {
	dir := t.TempDir()

	// Minimal clean vault — each tag used on 2+ pages, all links resolve.
	files := map[string]string{
		"index.md":  "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[about]] and [[page-a]] and [[page-b]]\n",
		"about.md":  "---\ntitle: About\ntags:\n  - info\ndate: 2026-01-01\n---\n\n[[index]] and [[page-a]] and [[page-b]]\n",
		"page-a.md": "---\ntitle: Page A\ntags:\n  - root\n  - info\ndate: 2026-01-01\n---\n\n[[index]] and [[about]] and [[page-b]]\n",
		"page-b.md": "---\ntitle: Page B\ntags:\n  - root\n  - info\ndate: 2026-01-01\n---\n\n[[index]] and [[about]] and [[page-a]]\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	cmd := NewRootCmd()
	cmd.SetArgs([]string{"--vault", dir, "lint"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("expected clean lint, got: %v", err)
	}
}
