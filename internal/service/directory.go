package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// DirectoryEntry describes a wiki page for the directory listing.
type DirectoryEntry struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Tags        string `json:"tags,omitempty"`
}

// DirectoryService provides page catalog operations.
type DirectoryService struct {
	vault *vault.Vault
}

// NewDirectoryService creates a DirectoryService for the given vault.
func NewDirectoryService(v *vault.Vault) *DirectoryService {
	return &DirectoryService{vault: v}
}

// List returns all wiki pages with their frontmatter metadata.
func (s *DirectoryService) List() ([]DirectoryEntry, error) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return nil, err
	}

	var result []DirectoryEntry
	for _, p := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, p)
		entry := DirectoryEntry{
			Path:  rel,
			Title: strings.TrimSuffix(filepath.Base(p), ".md"),
		}

		fm, err := vault.ParseFrontmatter(p)
		if err == nil && fm != nil {
			if t := fm["title"]; t != "" {
				entry.Title = t
			}
			if d := fm["description"]; d != "" {
				entry.Description = d
			}
			if tags := fm["tags"]; tags != "" {
				entry.Tags = tags
			}
		}

		result = append(result, entry)
	}

	return result, nil
}

// Generate writes meta/directory.md grouped by top-level tag domain.
// Returns the file path and page count.
func (s *DirectoryService) Generate() (string, int, error) {
	entries, err := s.List()
	if err != nil {
		return "", 0, err
	}

	// Group by top-level tag domain
	groups := make(map[string][]DirectoryEntry)
	for _, e := range entries {
		group := "Uncategorized"
		if e.Tags != "" {
			tags := strings.Split(e.Tags, ",")
			if len(tags) > 0 && tags[0] != "" {
				first := strings.TrimSpace(tags[0])
				if idx := strings.IndexByte(first, '/'); idx >= 0 {
					group = first[:idx]
				} else {
					group = first
				}
			}
		}
		groups[group] = append(groups[group], e)
	}

	// Sort group names
	groupNames := make([]string, 0, len(groups))
	for g := range groups {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	// Sort pages within each group
	for _, pages := range groups {
		sort.Slice(pages, func(i, j int) bool {
			return pages[i].Path < pages[j].Path
		})
	}

	// Build markdown
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: Page Directory\n")
	b.WriteString("tags:\n")
	b.WriteString("  - meta\n")
	b.WriteString("date: 2026-04-06\n")
	b.WriteString("---\n\n")
	b.WriteString("Agent-readable catalog of all wiki pages. Auto-generated — do not edit.\n")
	b.WriteString("Regenerate: `wiki-server directory --generate` / `POST /api/directory/generate` / `wiki_directory_generate` MCP tool.\n")

	for _, group := range groupNames {
		pages := groups[group]
		_, _ = fmt.Fprintf(&b, "\n## %s (%d pages)\n\n", group, len(pages))
		b.WriteString("| Page | Description |\n")
		b.WriteString("|------|-------------|\n")
		for _, e := range pages {
			wikilink := makeWikilink(e.Path, e.Title)
			desc := e.Description
			if desc == "" {
				desc = "—"
			}
			// Escape pipes in description
			desc = strings.ReplaceAll(desc, "|", "\\|")
			desc = strings.ReplaceAll(desc, "\n", " ")
			_, _ = fmt.Fprintf(&b, "| %s | %s |\n", wikilink, desc)
		}
	}

	dirFile := filepath.Join(s.vault.Dir, "meta", "directory.md")
	if err := os.MkdirAll(filepath.Dir(dirFile), 0o755); err != nil {
		return "", 0, err
	}
	if err := os.WriteFile(dirFile, []byte(b.String()), 0o644); err != nil {
		return "", 0, err
	}

	return dirFile, len(entries), nil
}

// makeWikilink builds [[path|Title]] from a relative .md path.
func makeWikilink(relPath, title string) string {
	link := strings.TrimSuffix(relPath, ".md")
	return fmt.Sprintf("[[%s\\|%s]]", link, title)
}
