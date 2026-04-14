package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

func setupTestVault(t *testing.T) *vault.Vault {
	t.Helper()
	dir := t.TempDir()

	for _, d := range []string{"raw", "meta", "meta/activity", "project", "private", ".obsidian"} {
		_ = os.MkdirAll(filepath.Join(dir, d), 0o755)
	}

	files := map[string]string{
		"index.md":                    "---\ntitle: Home Wiki\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nAuto-generated index with kubernetes references.\n",
		"about.md":                    "---\ntitle: About\ntags:\n  - info\ndate: 2026-01-01\n---\n\nThis is about the wiki.\n",
		"project/alpha.md":            "---\ntitle: Alpha Project\ntags:\n  - project\n  - kubernetes\ndate: 2026-02-01\n---\n\nAlpha uses kubernetes for orchestration.\n",
		"project/beta.md":             "---\ntitle: Beta Service\ntags:\n  - project\n  - networking\ndate: 2026-03-01\n---\n\nBeta handles networking between services.\n",
		"raw/source.md":               "---\ntitle: Source\nsource: https://example.com\ndate-added: 2026-01-15\n---\n\nRaw content about kubernetes.\n",
		"meta/activity/2026-04-06.md": "---\ntitle: \"2026-04-06\"\ntags:\n  - meta/activity\ndate: 2026-04-06\n---\n\n### 10:00 | create | Test\n",
		"project/index.md":            "---\ntitle: Project\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nAuto-generated project index with kubernetes.\n",
	}
	for name, content := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}

	return vault.New(dir)
}

func TestSubstringSearchByTitle(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	results, err := s.Search("Alpha", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected results for 'Alpha'")
	}

	if results[0].Match != "title" {
		t.Errorf("expected match=title, got %q", results[0].Match)
	}

	if results[0].Score < 100 {
		t.Errorf("expected score >= 100 for title match, got %v", results[0].Score)
	}

	if results[0].Engine != "substring" {
		t.Errorf("expected engine=substring, got %q", results[0].Engine)
	}
}

func TestSubstringSearchByTag(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	results, err := s.Search("networking", 10)
	if err != nil {
		t.Fatal(err)
	}

	// Beta has "networking" as a tag and in content
	found := false
	for _, r := range results {
		if r.Path == "project/beta.md" {
			found = true
			if r.Score < 50 {
				t.Errorf("expected score >= 50 for tag match, got %v", r.Score)
			}
		}
	}
	if !found {
		t.Error("expected Beta in results for 'networking'")
	}
}

func TestSubstringSearchByContent(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	results, err := s.Search("orchestration", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected results for 'orchestration'")
	}

	found := false
	for _, r := range results {
		if r.Path == "project/alpha.md" {
			found = true
			// "orchestration" is only in content, not title or tags
			if r.Score < 10 {
				t.Errorf("expected score >= 10 for content match, got %v", r.Score)
			}
		}
	}
	if !found {
		t.Error("expected Alpha in results for 'orchestration'")
	}
}

func TestSubstringSearchCaseInsensitive(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	results, err := s.Search("ALPHA", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected case-insensitive match for 'ALPHA'")
	}
}

func TestSubstringSearchNoResults(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	results, err := s.Search("zzzznonexistent", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestSubstringSearchLimit(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	// "wiki" appears in multiple pages
	results, err := s.Search("wiki", 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) > 1 {
		t.Errorf("expected at most 1 result with limit=1, got %d", len(results))
	}
}

func TestSubstringSearchScoreOrdering(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	// "kubernetes" appears in Alpha's tags AND content, so it should score higher
	// than a page with only content match
	results, err := s.Search("kubernetes", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected results for 'kubernetes'")
	}

	// Results should be sorted by score desc
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted by score: [%d].Score=%v > [%d].Score=%v",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestSubstringSearchExcludesActivityLogs(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	results, err := s.Search("create", 20)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if r.Path == "meta/activity/2026-04-06.md" {
			t.Error("activity log files should be excluded from search results")
		}
	}
}

func TestSubstringSearchSnippet(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	results, err := s.Search("orchestration", 10)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if r.Snippet == "" {
			t.Errorf("expected non-empty snippet for %s", r.Path)
		}
	}
}

func TestSubstringSearchExcludesIndexFiles(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	// "kubernetes" appears in index.md and project/index.md but they should be excluded
	results, err := s.Search("Auto-generated index", 20)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if filepath.Base(r.Path) == "index.md" {
			t.Errorf("generated index files should be excluded from search, found: %s", r.Path)
		}
	}
}

func TestSubstringSearchName(t *testing.T) {
	v := setupTestVault(t)
	s := NewSubstringSearcher(v)

	if s.Name() != "substring" {
		t.Errorf("expected name 'substring', got %q", s.Name())
	}
}
