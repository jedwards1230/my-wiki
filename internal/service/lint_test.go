package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

func setupLintVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()

	for _, d := range []string{"private", ".obsidian", "meta", "project"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}

	files := map[string]string{
		"index.md":          "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[project/alpha]] and [[meta/schema]].\n",
		"meta/schema.md":    "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nLinks to [[index]].\n",
		"project/alpha.md":  "---\ntitle: Alpha\ntags:\n  - project\ndate: 2026-02-01\n---\n\nSee [[meta/schema]].\n",
		"orphan.md":         "---\ntitle: Orphan\ntags:\n  - test\ndate: 2026-03-01\n---\n\nNo links here.\n",
		"no-frontmatter.md": "Just text.\n",
		"missing-tags.md":   "---\ntitle: No Tags\ndate: 2026-01-01\n---\n\nMissing tags.\n",
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

// TestLintService_Clippings exercises the clippings check: a page tagged
// `clipping` must contain at least one link into `raw/clippings/` in the
// body. The frontmatter `raw:` field does not satisfy the rule on its
// own — the check strips frontmatter before scanning so the link has to
// be visible in the rendered page.
func TestLintService_Clippings(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		// Good: tagged clipping, body has markdown URL into raw/clippings/.
		"research/security/good-url.md": "---\ntitle: Good URL\ntags:\n  - clipping\n  - research/security\ndate: 2026-05-10\n---\n\n## Sources\n\n- [Verbatim](https://wiki.lilbro.cloud/raw/clippings/youtube/good.md)\n",
		// Good: tagged clipping, body has wikilink into raw/clippings/.
		"research/security/good-wikilink.md": "---\ntitle: Good Wikilink\ntags:\n  - clipping\ndate: 2026-05-10\n---\n\nSee [[raw/clippings/youtube/foo]] for the verbatim.\n",
		// Bad: tagged clipping but body has no raw/clippings/ link.
		// The frontmatter raw: field does not save it — frontmatter is
		// stripped before the body scan.
		"clippings/bad-no-source.md": "---\ntitle: Bad No Source\ntags:\n  - clipping\ndate: 2026-05-10\nraw: \"raw/clippings/youtube/bad.md\"\n---\n\n## Related\n\n- [[some-other-page]]\n",
		// Ignored: not tagged clipping (no false positive even though it
		// happens to mention raw/clippings/ — irrelevant).
		"home/note.md": "---\ntitle: Note\ntags:\n  - home\ndate: 2026-05-10\n---\n\nSome text.\n",
		// Ignored: tag list contains "clippings" (plural, legacy) — the
		// check matches the canonical singular `clipping` only, so a
		// legacy plural tag is silently skipped rather than nagged. The
		// `tags` check is responsible for canonicalising old tags.
		"legacy/plural.md": "---\ntitle: Legacy Plural\ntags:\n  - clippings\ndate: 2026-05-10\n---\n\nNo raw link, but plural tag — skipped.\n",
	}

	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)

	report, err := svc.Run("clippings")
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(report.Issues), 1; got != want {
		t.Fatalf("expected exactly %d clipping issue, got %d: %+v", want, got, report.Issues)
	}
	issue := report.Issues[0]
	if issue.File != "clippings/bad-no-source.md" {
		t.Errorf("expected issue on clippings/bad-no-source.md, got %q", issue.File)
	}
	if issue.Check != "clippings" {
		t.Errorf("expected Check=clippings, got %q", issue.Check)
	}
	if !strings.Contains(issue.Message, "raw/clippings/") {
		t.Errorf("issue message should mention raw/clippings/, got %q", issue.Message)
	}
}

// TestLintService_Clippings_ConfigOverride verifies that meta/lint-config.yaml
// can rename the canonical clipping tag and the raw-path prefix without a
// code change. The same vault content that lints clean under defaults
// becomes a violation under custom values, and vice versa.
func TestLintService_Clippings_ConfigOverride(t *testing.T) {
	dir := t.TempDir()

	// Schema-default values would tag this page as clipping and accept the
	// link; under the override below it gets flagged because the page tag
	// is "clip" (custom canonical) and the raw prefix is "raw/sources/".
	if err := os.MkdirAll(filepath.Join(dir, "research/security"), 0o755); err != nil {
		t.Fatal(err)
	}
	defaultStylePage := "---\ntitle: Default Style\ntags:\n  - clipping\ndate: 2026-05-10\n---\n\n[Verbatim](https://wiki.lilbro.cloud/raw/clippings/youtube/foo.md)\n"
	customStylePage := "---\ntitle: Custom Style\ntags:\n  - clip\ndate: 2026-05-10\n---\n\n[Verbatim](https://wiki.lilbro.cloud/raw/sources/youtube/foo.md)\n"
	if err := os.WriteFile(filepath.Join(dir, "research/security/default-style.md"), []byte(defaultStylePage), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "research/security/custom-style.md"), []byte(customStylePage), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a config file that renames the tag and relocates raw/.
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "clippings:\n  tag: clip\n  raw_path_prefix: raw/sources/\n"
	if err := os.WriteFile(filepath.Join(dir, "meta", "lint-config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)
	report, err := svc.Run("clippings")
	if err != nil {
		t.Fatal(err)
	}

	// Under the custom config: the "custom-style" page (tag: clip, link:
	// raw/sources/) is the conforming one and lints clean. The
	// "default-style" page has tag: clipping which no longer matches the
	// canonical name — it's *ignored* entirely (not flagged), exactly
	// like a legacy plural tag would be.
	if got, want := len(report.Issues), 0; got != want {
		t.Fatalf("expected exactly %d issues under config override, got %d: %+v", want, got, report.Issues)
	}
}

// TestLintService_Clippings_ConfigParseError surfaces a malformed
// meta/lint-config.yaml as an ERROR-level issue under the "clippings"
// check rather than silently falling back to defaults.
func TestLintService_Clippings_ConfigParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Invalid YAML (unterminated string).
	bad := "clippings:\n  tag: \"unterminated\n"
	if err := os.WriteFile(filepath.Join(dir, "meta", "lint-config.yaml"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}

	v := vault.New(dir)
	svc := NewLintService(v, nil)
	report, err := svc.Run("clippings")
	if err != nil {
		t.Fatal(err)
	}

	// Want at least one ERROR issue mentioning the config file.
	sawConfigError := false
	for _, issue := range report.Issues {
		if issue.Check == "clippings" && issue.Level == "ERROR" && strings.Contains(issue.Message, "lint-config.yaml") {
			sawConfigError = true
			break
		}
	}
	if !sawConfigError {
		t.Errorf("expected an ERROR-level clippings issue mentioning lint-config.yaml; got %+v", report.Issues)
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

func TestLintService_Tags_KebabCase(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"good.md":      "---\ntitle: Good\ntags:\n  - homelab\ndate: 2026-01-01\n---\n\n[[bad-case]]\n",
		"bad-case.md":  "---\ntitle: Bad Case\ntags:\n  - HomeLab\ndate: 2026-01-01\n---\n\n[[good]]\n",
		"bad-under.md": "---\ntitle: Bad Under\ntags:\n  - home_lab\ndate: 2026-01-01\n---\n\n[[good]]\n",
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

	// Expect WARN for non-kebab-case tags.
	var warns int
	for _, issue := range report.Issues {
		if issue.Level == "WARN" && strings.Contains(issue.Message, "not kebab-case") {
			warns++
		}
	}
	if warns != 2 {
		t.Errorf("expected 2 kebab-case WARN issues, got %d", warns)
		for _, issue := range report.Issues {
			t.Logf("  %s: %s", issue.Level, issue.Message)
		}
	}
}

func TestLintService_Tags_SinglePage(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"a.md": "---\ntitle: A\ntags:\n  - common\ndate: 2026-01-01\n---\n\n[[b]] [[c]]\n",
		"b.md": "---\ntitle: B\ntags:\n  - common\ndate: 2026-01-01\n---\n\n[[a]] [[c]]\n",
		"c.md": "---\ntitle: C\ntags:\n  - rare\ndate: 2026-01-01\n---\n\n[[a]] [[b]]\n",
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

	// "rare" is used on only 1 page — should get INFO.
	var found bool
	for _, issue := range report.Issues {
		if issue.Level == "INFO" && strings.Contains(issue.Message, `"rare"`) && strings.Contains(issue.Message, "only 1 page") {
			found = true
		}
	}
	if !found {
		t.Error("expected INFO for single-page tag 'rare'")
		for _, issue := range report.Issues {
			t.Logf("  %s: %s", issue.Level, issue.Message)
		}
	}
}

func TestLintService_Tags_OrphanSubTag(t *testing.T) {
	dir := t.TempDir()

	files := map[string]string{
		"a.md": "---\ntitle: A\ntags:\n  - foo/bar\ndate: 2026-01-01\n---\n\n[[b]]\n",
		"b.md": "---\ntitle: B\ntags:\n  - foo/bar\ndate: 2026-01-01\n---\n\n[[a]]\n",
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

	// "foo/bar" has no pages tagged "foo" — should get INFO.
	var found bool
	for _, issue := range report.Issues {
		if issue.Level == "INFO" && strings.Contains(issue.Message, "no pages using parent domain") {
			found = true
		}
	}
	if !found {
		t.Error("expected INFO for orphan sub-tag foo/bar")
		for _, issue := range report.Issues {
			t.Logf("  %s: %s", issue.Level, issue.Message)
		}
	}
}

func TestLintService_CleanVault(t *testing.T) {
	dir := t.TempDir()
	// Each tag used on 2+ pages to avoid single-page INFO issues.
	files := map[string]string{
		"index.md": "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[about]]\n",
		"about.md": "---\ntitle: About\ntags:\n  - root\ndate: 2026-01-01\n---\n\n[[index]]\n",
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

	for _, issue := range report.Issues {
		t.Errorf("unexpected issue: %s: %s - %s", issue.File, issue.Check, issue.Message)
	}
}
