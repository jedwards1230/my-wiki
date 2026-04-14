package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

func setupLintVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()

	for _, d := range []string{"raw", "private", ".obsidian", "meta", "project"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}

	files := map[string]string{
		"index.md":          "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[project/alpha]] and [[meta/schema]].\n",
		"meta/schema.md":    "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nLinks to [[index]].\n",
		"project/alpha.md":  "---\ntitle: Alpha\ntags:\n  - project\ndate: 2026-02-01\n---\n\nSee [[meta/schema]].\n",
		"orphan.md":         "---\ntitle: Orphan\ntags:\n  - test\ndate: 2026-03-01\n---\n\nNo links here.\n",
		"no-frontmatter.md": "Just text.\n",
		"missing-tags.md":   "---\ntitle: No Tags\ndate: 2026-01-01\n---\n\nMissing tags.\n",
		"raw/good.md":       "---\ntitle: Good\nsource: https://example.com\ndate-added: 2026-01-01\n---\n\nContent.\n",
		"raw/bad.md":        "---\ntitle: Bad Raw\n---\n\nMissing source and date-added.\n",
	}

	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	return vault.New(dir)
}

func TestLintService_Frontmatter(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	report, err := svc.Run("frontmatter")
	if err != nil {
		t.Fatal(err)
	}

	if report.Total == 0 {
		t.Fatal("expected frontmatter issues")
	}

	// Should find no-frontmatter.md and missing-tags.md
	found := map[string]bool{}
	for _, issue := range report.Issues {
		found[issue.File] = true
	}
	if !found["no-frontmatter.md"] {
		t.Error("expected issue for no-frontmatter.md")
	}
	if !found["missing-tags.md"] {
		t.Error("expected issue for missing-tags.md")
	}
}

func TestLintService_Raw(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	report, err := svc.Run("raw")
	if err != nil {
		t.Fatal(err)
	}

	if report.Total == 0 {
		t.Fatal("expected raw issues")
	}

	found := false
	for _, issue := range report.Issues {
		if issue.File == "raw/bad.md" {
			found = true
		}
	}
	if !found {
		t.Error("expected issue for raw/bad.md")
	}
}

func TestLintService_Links(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	report, err := svc.Run("links")
	if err != nil {
		t.Fatal(err)
	}

	if report.Total != 0 {
		t.Errorf("expected 0 broken links, got %d", report.Total)
	}
}

func TestLintService_Orphans(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	report, err := svc.Run("orphans")
	if err != nil {
		t.Fatal(err)
	}

	if report.Total == 0 {
		t.Fatal("expected orphan issues")
	}
}

func TestLintService_All(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	report, err := svc.Run("all")
	if err != nil {
		t.Fatal(err)
	}

	if report.Total == 0 {
		t.Fatal("expected issues in all check")
	}
}

func TestLintService_InvalidCheck(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	_, err := svc.Run("invalid")
	if err == nil {
		t.Fatal("expected error for invalid check")
	}
}

func TestLintService_CleanVault(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"index.md": "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[about]]\n",
		"about.md": "---\ntitle: About\ntags:\n  - info\ndate: 2026-01-01\n---\n\n[[index]]\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)

	report, err := svc.Run("all")
	if err != nil {
		t.Fatal(err)
	}

	if report.Total != 0 {
		t.Errorf("expected clean vault, got %d issues", report.Total)
		for _, issue := range report.Issues {
			t.Logf("  %s: %s - %s", issue.File, issue.Check, issue.Message)
		}
	}
}
