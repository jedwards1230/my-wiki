package service

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLintPage_BrokenLinks(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// Create a page with a broken wikilink.
	content := "---\ntitle: Broken\ntags:\n  - test\ndate: 2026-01-01\n---\n\n[[nonexistent-page]] and [[project/alpha]].\n"
	_ = os.WriteFile(filepath.Join(v.Dir, "broken-links.md"), []byte(content), 0o644)

	issues := svc.LintPage("broken-links.md")

	var found bool
	for _, issue := range issues {
		if issue.Check == "links" && strings.Contains(issue.Message, "nonexistent-page") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected broken link warning for [[nonexistent-page]], got: %v", issues)
	}
}

func TestLintPage_CleanPage(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// index.md links to project/alpha and meta/schema — both exist.
	issues := svc.LintPage("index.md")
	if len(issues) != 0 {
		t.Errorf("expected no issues for clean page, got: %v", issues)
	}
}

func TestLintPage_RawFile(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// raw/bad.md is missing source and date-added.
	issues := svc.LintPage("raw/bad.md")
	if len(issues) == 0 {
		t.Fatal("expected issues for raw/bad.md")
	}
	if issues[0].Check != "raw" {
		t.Errorf("expected 'raw' check, got %q", issues[0].Check)
	}
}

func TestLintPage_AddsExtension(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// Path without .md should still work.
	issues := svc.LintPage("index")
	if len(issues) != 0 {
		t.Errorf("expected no issues for index (without .md), got: %v", issues)
	}
}

func TestLintDelete_CausesBrokenLinks(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// meta/schema.md is linked from index.md and project/alpha.md.
	// Delete it and check for broken link warnings.
	_ = os.Remove(filepath.Join(v.Dir, "meta", "schema.md"))

	issues := svc.LintDelete("meta/schema.md")

	if len(issues) == 0 {
		t.Fatal("expected broken link warnings after deleting meta/schema.md")
	}

	// Both index.md and project/alpha.md should have broken links.
	files := map[string]bool{}
	for _, issue := range issues {
		files[issue.File] = true
		if issue.Check != "links" {
			t.Errorf("expected 'links' check, got %q", issue.Check)
		}
		if !strings.Contains(issue.Message, "target was deleted") {
			t.Errorf("expected 'target was deleted' message, got %q", issue.Message)
		}
	}
	if !files["index.md"] {
		t.Error("expected index.md to have broken link after deleting meta/schema")
	}
	if !files["project/alpha.md"] {
		t.Error("expected project/alpha.md to have broken link after deleting meta/schema")
	}
}

func TestLintDelete_NoImpact(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// orphan.md has no inbound links — deleting it should break nothing.
	_ = os.Remove(filepath.Join(v.Dir, "orphan.md"))

	issues := svc.LintDelete("orphan.md")
	if len(issues) != 0 {
		t.Errorf("expected no issues after deleting orphan, got: %v", issues)
	}
}

func TestLintDelete_SlugStillResolvesToOtherPage(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// Create two pages with the same basename: schema.md and meta/schema.md
	content := "---\ntitle: Other Schema\ntags:\n  - test\ndate: 2026-01-01\n---\n\nAnother schema.\n"
	_ = os.WriteFile(filepath.Join(v.Dir, "schema.md"), []byte(content), 0o644)

	// Delete meta/schema.md — the "schema" slug should still resolve to schema.md.
	_ = os.Remove(filepath.Join(v.Dir, "meta", "schema.md"))

	issues := svc.LintDelete("meta/schema.md")

	// Links to [[schema]] should still resolve (to the root schema.md).
	// Only links to [[meta/schema]] should break.
	for _, issue := range issues {
		if strings.Contains(issue.Message, "[[schema]]") {
			t.Errorf("[[schema]] should still resolve to schema.md, but got warning: %s", issue.Message)
		}
	}
}

func TestLintPage_InvalidYAML(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	// Create a page with broken YAML frontmatter.
	content := "---\ntitle: Broken\ntags:\n  - [unclosed bracket\ndate: 2026-01-01\n---\n\nBody.\n"
	_ = os.WriteFile(filepath.Join(v.Dir, "bad-yaml.md"), []byte(content), 0o644)

	issues := svc.LintPage("bad-yaml.md")
	if len(issues) == 0 {
		t.Fatal("expected YAML syntax error, got no issues")
	}
	if issues[0].Level != "FAIL" {
		t.Errorf("expected FAIL level, got %q", issues[0].Level)
	}
	if !strings.Contains(issues[0].Message, "invalid YAML") {
		t.Errorf("expected 'invalid YAML' in message, got: %s", issues[0].Message)
	}
}

func TestLintPage_InvalidYAML_Raw(t *testing.T) {
	v := setupLintVault(t)
	svc := NewLintService(v, nil)

	content := "---\ntitle: Broken Raw\nsource: {not closed\n---\n\nBody.\n"
	_ = os.MkdirAll(filepath.Join(v.Dir, "raw"), 0o755)
	_ = os.WriteFile(filepath.Join(v.Dir, "raw", "bad-yaml.md"), []byte(content), 0o644)

	issues := svc.LintPage("raw/bad-yaml.md")
	if len(issues) == 0 {
		t.Fatal("expected YAML syntax error for raw file, got no issues")
	}
	if issues[0].Check != "raw" {
		t.Errorf("expected 'raw' check, got %q", issues[0].Check)
	}
}

func TestLintService_Frontmatter_InvalidYAML(t *testing.T) {
	v := setupLintVault(t)

	// Add a page with broken YAML to the vault.
	content := "---\ntitle: Broken\nextra: {not closed\n---\n\nBody.\n"
	_ = os.WriteFile(filepath.Join(v.Dir, "broken-yaml.md"), []byte(content), 0o644)

	svc := NewLintService(v, nil)
	report, err := svc.Run("frontmatter")
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, issue := range report.Issues {
		if issue.File == "broken-yaml.md" && strings.Contains(issue.Message, "invalid YAML") {
			found = true
		}
	}
	if !found {
		t.Error("expected YAML syntax error for broken-yaml.md in frontmatter check")
	}
}

func TestLintService_Links_Dedup(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"index.md":   "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[missing-page]] and [[about]].\n",
		"about.md":   "---\ntitle: About\ntags:\n  - info\ndate: 2026-01-01\n---\n\n[[missing-page]] and [[index]].\n",
		"project.md": "---\ntitle: Project\ntags:\n  - dev\ndate: 2026-01-01\n---\n\n[[missing-page]] and [[also-missing]].\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)

	report, err := svc.Run("links")
	if err != nil {
		t.Fatal(err)
	}

	// [[missing-page]] linked from 3 files should be a single issue.
	// [[also-missing]] linked from 1 file should be another.
	if report.Total != 2 {
		t.Errorf("expected 2 deduped issues, got %d", report.Total)
		for _, issue := range report.Issues {
			t.Logf("  %s", issue.Message)
		}
	}

	for _, issue := range report.Issues {
		if strings.Contains(issue.Message, "missing-page") {
			if !strings.Contains(issue.Message, "linked from:") {
				t.Errorf("expected 'linked from:' in message, got: %s", issue.Message)
			}
			// Should mention all 3 source files.
			for _, src := range []string{"index.md", "about.md", "project.md"} {
				if !strings.Contains(issue.Message, src) {
					t.Errorf("expected %s in sources, got: %s", src, issue.Message)
				}
			}
		}
	}
}

func TestLintService_GeneratedExempt(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"index.md": "---\ntitle: Home\ndate: 2026-01-01\ngenerated: true\n---\n\n[[about]]\n",
		"about.md": "---\ntitle: About\ntags:\n  - info\ndate: 2026-01-01\n---\n\n[[index]]\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)

	report, err := svc.Run("frontmatter")
	if err != nil {
		t.Fatal(err)
	}

	// index.md has no tags but is generated: true — should be exempt.
	for _, issue := range report.Issues {
		if issue.File == "index.md" {
			t.Errorf("expected generated page to be exempt, got issue: %s", issue.Message)
		}
	}
}

func TestLintService_Size(t *testing.T) {
	dir := t.TempDir()
	// Create a page with >1000 words
	bigBody := strings.Repeat("word ", 1100)
	files := map[string]string{
		"big.md":   "---\ntitle: Big\ntags:\n  - test\ndate: 2026-01-01\n---\n\n" + bigBody,
		"small.md": "---\ntitle: Small\ntags:\n  - test\ndate: 2026-01-01\n---\n\nShort page.\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)

	report, err := svc.Run("size")
	if err != nil {
		t.Fatal(err)
	}

	if report.Total != 1 {
		t.Errorf("expected 1 size issue, got %d", report.Total)
	}
	if report.Total > 0 && report.Issues[0].File != "big.md" {
		t.Errorf("expected issue for big.md, got %s", report.Issues[0].File)
	}
}

func TestLintService_Tags(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "meta"), 0o755)

	schema := "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\n" +
		"<!-- begin:tag-taxonomy -->\n" +
		"| Domain | Use for | Sub-tag examples |\n" +
		"|--------|---------|------------------|\n" +
		"| `homelab` | Infrastructure | `homelab/service`, `homelab/host` |\n" +
		"| `meta` | Wiki ops | `meta/activity` |\n" +
		"<!-- end:tag-taxonomy -->\n"

	files := map[string]string{
		"meta/schema.md": schema,
		"good.md":        "---\ntitle: Good\ntags:\n  - homelab\ndate: 2026-01-01\n---\n\n[[meta/schema]]\n",
		"bad-tag.md":     "---\ntitle: Bad\ntags:\n  - invalid-domain\ndate: 2026-01-01\n---\n\n[[meta/schema]]\n",
		"new-sub.md":     "---\ntitle: New Sub\ntags:\n  - homelab/new-thing\ndate: 2026-01-01\n---\n\n[[meta/schema]]\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)

	report, err := svc.Run("tags")
	if err != nil {
		t.Fatal(err)
	}

	// Expect WARN for bad-tag.md (unknown domain) and new-sub.md (missing sub-tag),
	// plus INFO issues for under-threshold tags.
	var warns, infos int
	for _, issue := range report.Issues {
		switch issue.Level {
		case "WARN":
			warns++
		case "INFO":
			infos++
		}
		if issue.File == "bad-tag.md" && !strings.Contains(issue.Message, "unknown domain") {
			t.Errorf("expected 'unknown domain' for bad-tag.md, got: %s", issue.Message)
		}
		if issue.File == "new-sub.md" && !strings.Contains(issue.Message, "add sub-tag to schema") {
			t.Errorf("expected 'add sub-tag' for new-sub.md, got: %s", issue.Message)
		}
	}
	if warns != 2 {
		t.Errorf("expected 2 WARN issues, got %d", warns)
	}
	if infos == 0 {
		t.Error("expected INFO issues for under-threshold tags")
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

	// The only expected issue is a WARN about tags check being skipped
	// (no meta/schema.md with taxonomy markers in this test vault).
	for _, issue := range report.Issues {
		if issue.Check == "tags" && issue.Level == "WARN" && strings.Contains(issue.Message, "skipped") {
			continue
		}
		t.Errorf("unexpected issue: %s: %s - %s", issue.File, issue.Check, issue.Message)
	}
}
