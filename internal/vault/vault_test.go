package vault

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func setupTestVault(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Create directory structure
	dirs := []string{
		"raw",
		"private",
		".obsidian",
		"meta",
		"meta/activity",
		"project",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Create test files
	files := map[string]string{
		"index.md": `---
title: Home
tags:
  - root
date: 2026-01-01
---

Welcome to the wiki. See [[project/alpha]] and [[meta/schema]].
`,
		"meta/schema.md": `---
title: Schema
tags:
  - meta
date: 2026-01-01
---

The schema page. Links to [[index]].
`,
		"project/alpha.md": `---
title: Project Alpha
tags:
  - project
date: 2026-02-01
---

Alpha project. See [[meta/schema]].

` + "```\n[[not-a-link]]\n```\n\nInline `[[also-not-a-link]]` code.\n",
		"orphan.md": `---
title: Orphan Page
date: 2026-03-01
---

Nobody links here.
`,
		"no-frontmatter.md": `Just some text without frontmatter.
`,
		"raw/source1.md": `---
title: Source One
source: https://example.com
date-added: 2026-01-15
---

Raw content.
`,
		"raw/source2.md": `---
title: Source Two
source: https://example.com/2
date-added: 2026-02-01
ingested: true
---

Already ingested.
`,
		"raw/missing-fields.md": `---
title: Missing Source
---

No source or date-added.
`,
		"private/secret.md": `---
title: Secret
---

Private content.
`,
		".obsidian/config.md": `Obsidian config file.
`,
	}

	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func TestFindWikiPages(t *testing.T) {
	dir := setupTestVault(t)
	v := New(dir)

	pages, err := v.FindWikiPages()
	if err != nil {
		t.Fatal(err)
	}

	// Convert to relative paths for easier testing
	var rels []string
	for _, p := range pages {
		rel, _ := filepath.Rel(dir, p)
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	// Should include wiki pages, not raw/, private/, .obsidian/
	expected := []string{
		"index.md",
		"meta/schema.md",
		"no-frontmatter.md",
		"orphan.md",
		"project/alpha.md",
	}

	if len(rels) != len(expected) {
		t.Fatalf("expected %d pages, got %d: %v", len(expected), len(rels), rels)
	}
	for i, exp := range expected {
		if rels[i] != exp {
			t.Errorf("page %d: expected %s, got %s", i, exp, rels[i])
		}
	}
}

func TestFindRawFiles(t *testing.T) {
	dir := setupTestVault(t)
	v := New(dir)

	files, err := v.FindRawFiles()
	if err != nil {
		t.Fatal(err)
	}

	var rels []string
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	expected := []string{
		"raw/missing-fields.md",
		"raw/source1.md",
		"raw/source2.md",
	}

	if len(rels) != len(expected) {
		t.Fatalf("expected %d raw files, got %d: %v", len(expected), len(rels), rels)
	}
	for i, exp := range expected {
		if rels[i] != exp {
			t.Errorf("file %d: expected %s, got %s", i, exp, rels[i])
		}
	}
}

func TestFindRawFiles_NoRawDir(t *testing.T) {
	dir := t.TempDir()
	v := New(dir)

	files, err := v.FindRawFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("expected no files, got %d", len(files))
	}
}

func TestParseFrontmatter(t *testing.T) {
	dir := setupTestVault(t)

	tests := []struct {
		name     string
		file     string
		wantKeys []string
		wantNil  bool
	}{
		{
			name:     "full frontmatter",
			file:     "index.md",
			wantKeys: []string{"title", "tags", "date"},
		},
		{
			name:     "raw frontmatter",
			file:     "raw/source1.md",
			wantKeys: []string{"title", "source", "date-added"},
		},
		{
			name:    "no frontmatter",
			file:    "no-frontmatter.md",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm, err := ParseFrontmatter(filepath.Join(dir, tt.file))
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNil {
				if fm != nil {
					t.Errorf("expected nil frontmatter, got %v", fm)
				}
				return
			}
			for _, key := range tt.wantKeys {
				if _, ok := fm[key]; !ok {
					t.Errorf("missing key %q in frontmatter: %v", key, fm)
				}
			}
		})
	}
}

func TestParseFrontmatter_Values(t *testing.T) {
	dir := setupTestVault(t)

	fm, err := ParseFrontmatter(filepath.Join(dir, "raw/source1.md"))
	if err != nil {
		t.Fatal(err)
	}

	if fm["title"] != "Source One" {
		t.Errorf("title = %q, want %q", fm["title"], "Source One")
	}
	if fm["source"] != "https://example.com" {
		t.Errorf("source = %q, want %q", fm["source"], "https://example.com")
	}
	if fm["date-added"] != "2026-01-15" {
		t.Errorf("date-added = %q, want %q", fm["date-added"], "2026-01-15")
	}
}

func TestParseFrontmatter_QuotedValues(t *testing.T) {
	dir := t.TempDir()
	content := "---\ntitle: \"Quoted Title\"\ndate: 2026-01-01\n---\n\nBody.\n"
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	fm, err := ParseFrontmatter(filepath.Join(dir, "test.md"))
	if err != nil {
		t.Fatal(err)
	}
	if fm["title"] != "Quoted Title" {
		t.Errorf("title = %q, want %q", fm["title"], "Quoted Title")
	}
}

func TestExtractWikilinks(t *testing.T) {
	dir := setupTestVault(t)

	links, err := ExtractWikilinks(filepath.Join(dir, "project/alpha.md"))
	if err != nil {
		t.Fatal(err)
	}

	// Should find [[meta/schema]] but NOT [[not-a-link]] (code block) or [[also-not-a-link]] (inline code)
	if len(links) != 1 {
		t.Fatalf("expected 1 link, got %d: %v", len(links), links)
	}
	if links[0] != "meta/schema" {
		t.Errorf("link = %q, want %q", links[0], "meta/schema")
	}
}

func TestExtractWikilinks_WithAliasAndAnchor(t *testing.T) {
	dir := t.TempDir()
	content := "---\ntitle: Test\n---\n\n[[page|alias]] and [[other#heading]] and [[both#h|a]].\n"
	if err := os.WriteFile(filepath.Join(dir, "test.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	links, err := ExtractWikilinks(filepath.Join(dir, "test.md"))
	if err != nil {
		t.Fatal(err)
	}

	expected := []string{"page", "other", "both"}
	if len(links) != len(expected) {
		t.Fatalf("expected %d links, got %d: %v", len(expected), len(links), links)
	}
	for i, exp := range expected {
		if links[i] != exp {
			t.Errorf("link %d = %q, want %q", i, links[i], exp)
		}
	}
}

func TestBuildSlugIndex(t *testing.T) {
	dir := setupTestVault(t)
	v := New(dir)

	slugs, err := v.BuildSlugIndex()
	if err != nil {
		t.Fatal(err)
	}

	// Should include basenames and full paths
	for _, slug := range []string{
		"index",
		"schema",
		"meta/schema",
		"alpha",
		"project/alpha",
		"source1",
		"raw/source1",
	} {
		if !slugs[slug] {
			t.Errorf("missing slug %q", slug)
		}
	}

	// Should NOT include private or .obsidian
	for _, slug := range []string{"secret", "config"} {
		if slugs[slug] {
			t.Errorf("unexpected slug %q (should be excluded)", slug)
		}
	}
}
