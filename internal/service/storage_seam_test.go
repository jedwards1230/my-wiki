package service

import (
	"strings"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

// TestServices_MemStorage proves the read path runs entirely through the
// vault.Storage seam: DirectoryService, TagService, and LintService produce
// correct results against a MemStorage-backed vault with no filesystem access
// and no temp dir. This is the Phase-4 proof that FindWikiPages +
// ParseFrontmatter/ValidateYAMLSyntax/ExtractWikilinks are Storage-routable.
func TestServices_MemStorage(t *testing.T) {
	mem := vault.NewMemStorage()
	mem.AddFile("index.md", "---\ntitle: Home\ntags:\n  - root\ndate: 2026-01-01\n---\n\nSee [[project/alpha]] and [[meta/schema]].\n")
	mem.AddFile("meta/schema.md", "---\ntitle: Schema\ntags:\n  - meta\ndate: 2026-01-01\n---\n\nLinks to [[index]].\n")
	mem.AddFile("project/alpha.md", "---\ntitle: Alpha\ntags:\n  - project\ndate: 2026-02-01\n---\n\nSee [[meta/schema]] and [[ghost]].\n")
	mem.AddFile("guide.md", "---\ntitle: Guide\ntags:\n  - project\ndate: 2026-03-01\n---\n\nNobody links here.\n")
	mem.AddFile("draft.md", "Just text without frontmatter.\n")
	mem.AddFile(".obsidian/config.json", "{}")

	v := vault.New("/mem-vault", vault.WithStorage(mem))

	t.Run("DirectoryService.List", func(t *testing.T) {
		entries, err := NewDirectoryService(v).List("")
		if err != nil {
			t.Fatal(err)
		}

		byPath := make(map[string]DirectoryEntry, len(entries))
		for _, e := range entries {
			byPath[e.Path] = e
		}

		// .obsidian/ excluded; every markdown page present.
		want := []string{"index.md", "meta/schema.md", "project/alpha.md", "guide.md", "draft.md"}
		if len(entries) != len(want) {
			t.Fatalf("List returned %d entries, want %d: %v", len(entries), len(want), entries)
		}
		for _, p := range want {
			if _, ok := byPath[p]; !ok {
				t.Errorf("missing directory entry for %q", p)
			}
		}

		// Frontmatter (read through Storage) drives Title/Tags.
		if got := byPath["project/alpha.md"]; got.Title != "Alpha" || got.Tags != "project" {
			t.Errorf("project/alpha.md entry = {Title:%q Tags:%q}, want {Alpha project}", got.Title, got.Tags)
		}
		// No-frontmatter page falls back to the filename-derived title.
		if got := byPath["draft.md"]; got.Title != "draft" {
			t.Errorf("draft.md title = %q, want %q", got.Title, "draft")
		}
	})

	t.Run("TagService.List", func(t *testing.T) {
		report, err := NewTagService(v).List()
		if err != nil {
			t.Fatal(err)
		}
		if report.Total != 5 {
			t.Errorf("Total = %d, want 5", report.Total)
		}
		counts := make(map[string]int)
		for _, u := range report.Used {
			counts[u.Tag] = u.Count
		}
		for tag, want := range map[string]int{"project": 2, "root": 1, "meta": 1} {
			if counts[tag] != want {
				t.Errorf("tag %q count = %d, want %d", tag, counts[tag], want)
			}
		}
	})

	t.Run("LintService.Links", func(t *testing.T) {
		report, err := NewLintService(v, nil).Run("links")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, iss := range report.Issues {
			if iss.Check == "links" && iss.Level == "WARN" && strings.Contains(iss.Message, "ghost") {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a broken-link WARN mentioning [[ghost]]; got %+v", report.Issues)
		}
	})

	t.Run("LintService.Frontmatter", func(t *testing.T) {
		report, err := NewLintService(v, nil).Run("frontmatter")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, iss := range report.Issues {
			if iss.File == "draft.md" && iss.Check == "frontmatter" && iss.Level == "FAIL" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected a frontmatter FAIL for draft.md; got %+v", report.Issues)
		}
	})

	t.Run("LintService.Orphans", func(t *testing.T) {
		report, err := NewLintService(v, nil).Run("orphans")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, iss := range report.Issues {
			if iss.File == "guide.md" && iss.Check == "orphans" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected an orphans WARN for guide.md; got %+v", report.Issues)
		}
	})
}
