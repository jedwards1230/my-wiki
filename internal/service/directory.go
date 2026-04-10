package service

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// DirectoryEntry describes a wiki page for the directory listing.
type DirectoryEntry struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Tags        string `json:"tags,omitempty"`
}

// GenerateResult is the result of generating index files across the vault.
type GenerateResult struct {
	FilesWritten int `json:"files_written"`
	PagesIndexed int `json:"pages_indexed"`
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

// excludedDirs are directories that should not get index files.
var excludedDirs = map[string]bool{
	"raw":            true,
	"private":        true,
	".obsidian":      true,
	"meta/activity":  true,
	"meta\\activity": true, // Windows
}

// isExcludedDir checks if a relative directory path should be excluded.
func isExcludedDir(rel string) bool {
	if excludedDirs[rel] {
		return true
	}
	// Check if any parent is excluded
	for excluded := range excludedDirs {
		if strings.HasPrefix(rel, excluded+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// IsGeneratedIndex returns true if the given relative path is a generated index file.
// Use this to exclude indexes from search results.
func IsGeneratedIndex(rel string) bool {
	base := filepath.Base(rel)
	return base == "index.md"
}

// dirNode represents a directory in the vault tree.
type dirNode struct {
	rel      string           // relative path from vault root ("" for root)
	pages    []DirectoryEntry // non-index pages directly in this dir
	children []*dirNode       // subdirectories
}

// Generate writes index.md files across the vault: one per directory containing
// wiki pages. Root gets a folder tree + vault-wide tag overview. Leaf dirs get
// flat page tables. Mid-level dirs get subtree structure + scoped tags.
func (s *DirectoryService) Generate() (string, int, error) {
	entries, err := s.List()
	if err != nil {
		return "", 0, err
	}

	// Filter out existing index.md files and excluded dirs
	var pages []DirectoryEntry
	for _, e := range entries {
		if IsGeneratedIndex(e.Path) {
			continue
		}
		dir := filepath.Dir(e.Path)
		if dir == "." {
			dir = ""
		}
		if isExcludedDir(dir) {
			continue
		}
		pages = append(pages, e)
	}

	// Build directory tree
	root := buildDirTree(pages)

	// Write index files
	filesWritten := 0
	today := time.Now().Format("2006-01-02")

	var writeIndexes func(node *dirNode) error
	writeIndexes = func(node *dirNode) error {
		content := renderIndex(node, root, pages, today)
		indexPath := filepath.Join(s.vault.Dir, node.rel, "index.md")

		if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(indexPath, []byte(content), 0o644); err != nil {
			return err
		}
		filesWritten++

		for _, child := range node.children {
			if err := writeIndexes(child); err != nil {
				return err
			}
		}
		return nil
	}

	if err := writeIndexes(root); err != nil {
		return "", 0, err
	}

	return filepath.Join(s.vault.Dir, "index.md"), len(pages), nil
}

// buildDirTree organizes pages into a tree of directories.
func buildDirTree(pages []DirectoryEntry) *dirNode {
	nodes := map[string]*dirNode{
		"": {rel: ""},
	}

	// Ensure all directories exist in the tree
	for _, p := range pages {
		dir := filepath.Dir(p.Path)
		if dir == "." {
			dir = ""
		}
		ensureNode(nodes, dir)
		nodes[dir].pages = append(nodes[dir].pages, p)
	}

	// Sort pages within each node
	for _, n := range nodes {
		sort.Slice(n.pages, func(i, j int) bool {
			return n.pages[i].Path < n.pages[j].Path
		})
	}

	// Sort children of each node
	for _, n := range nodes {
		sort.Slice(n.children, func(i, j int) bool {
			return n.children[i].rel < n.children[j].rel
		})
	}

	return nodes[""]
}

// ensureNode creates a dirNode and all its ancestors.
func ensureNode(nodes map[string]*dirNode, dir string) *dirNode {
	if n, ok := nodes[dir]; ok {
		return n
	}

	n := &dirNode{rel: dir}
	nodes[dir] = n

	parent := filepath.Dir(dir)
	if parent == "." || parent == dir {
		parent = ""
	}
	parentNode := ensureNode(nodes, parent)
	parentNode.children = append(parentNode.children, n)

	return n
}

// countPages returns total pages under a node (recursive).
func countPages(node *dirNode) int {
	count := len(node.pages)
	for _, child := range node.children {
		count += countPages(child)
	}
	return count
}

// collectPages returns all pages under a node (recursive).
func collectPages(node *dirNode) []DirectoryEntry {
	var all []DirectoryEntry
	all = append(all, node.pages...)
	for _, child := range node.children {
		all = append(all, collectPages(child)...)
	}
	return all
}

// collectTags returns a sorted tag → count map for a set of pages.
func collectTags(pages []DirectoryEntry) []tagCount {
	counts := map[string]int{}
	for _, p := range pages {
		if p.Tags == "" {
			continue
		}
		for _, t := range strings.Split(p.Tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				counts[t]++
			}
		}
	}

	var result []tagCount
	for tag, count := range counts {
		result = append(result, tagCount{Tag: tag, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Tag < result[j].Tag
	})
	return result
}

type tagCount struct {
	Tag   string
	Count int
}

// renderIndex generates the markdown content for an index file.
func renderIndex(node *dirNode, root *dirNode, allPages []DirectoryEntry, today string) string {
	isRoot := node.rel == ""
	hasChildren := len(node.children) > 0

	var b strings.Builder

	// Frontmatter
	title := "Home Wiki"
	desc := "Shared knowledge base for the Edwards homelab"
	if !isRoot {
		title = titleCase(filepath.Base(node.rel))
		desc = fmt.Sprintf("Index of %s", node.rel)
	}

	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", title)
	b.WriteString("tags:\n  - meta\n")
	fmt.Fprintf(&b, "date: %s\n", today)
	fmt.Fprintf(&b, "description: %s\n", desc)
	b.WriteString("---\n\n")

	if isRoot {
		renderRootIndex(&b, node, allPages, today)
	} else if hasChildren {
		renderMidIndex(&b, node, today)
	} else {
		renderLeafIndex(&b, node)
	}

	return b.String()
}

func renderRootIndex(b *strings.Builder, root *dirNode, allPages []DirectoryEntry, today string) {
	b.WriteString("A shared knowledge base — maintained by humans and AI agents.\n\n")
	b.WriteString("See [[meta/schema]] for the operating manual. See [[meta/log]] for recent activity.\n\n")
	fmt.Fprintf(b, "**%d pages** across %d directories. Auto-generated by `wiki-server directory --generate`.\n", len(allPages), len(root.children))

	// Directory tree
	b.WriteString("\n## Directory\n\n")
	b.WriteString("```\n")
	renderTree(b, root, "")
	b.WriteString("```\n")

	// Tag overview
	tags := collectTags(allPages)
	if len(tags) > 0 {
		b.WriteString("\n## Tags\n\n")
		b.WriteString("| Tag | Count |\n")
		b.WriteString("|-----|-------|\n")
		for _, tc := range tags {
			fmt.Fprintf(b, "| %s | %d |\n", tc.Tag, tc.Count)
		}
	}
}

func renderMidIndex(b *strings.Builder, node *dirNode, today string) {
	allBelow := collectPages(node)
	fmt.Fprintf(b, "**%d pages** in %s/. Auto-generated.\n", len(allBelow), node.rel)

	// Direct pages in this dir
	if len(node.pages) > 0 {
		b.WriteString("\n## Pages\n\n")
		renderPageTable(b, node.pages)
	}

	// Subdirectories
	if len(node.children) > 0 {
		b.WriteString("\n## Subdirectories\n\n")
		for _, child := range node.children {
			childCount := countPages(child)
			childName := filepath.Base(child.rel)
			fmt.Fprintf(b, "- [[%s/index|%s/]] (%d pages)\n", child.rel, childName, childCount)
		}
	}

	// Scoped tags
	tags := collectTags(allBelow)
	if len(tags) > 0 {
		b.WriteString("\n## Tags\n\n")
		b.WriteString("| Tag | Count |\n")
		b.WriteString("|-----|-------|\n")
		for _, tc := range tags {
			fmt.Fprintf(b, "| %s | %d |\n", tc.Tag, tc.Count)
		}
	}
}

func renderLeafIndex(b *strings.Builder, node *dirNode) {
	fmt.Fprintf(b, "**%d pages** in %s/. Auto-generated.\n", len(node.pages), node.rel)
	b.WriteString("\n")
	renderPageTable(b, node.pages)
}

func renderPageTable(b *strings.Builder, pages []DirectoryEntry) {
	b.WriteString("| Page | Description | Tags |\n")
	b.WriteString("|------|-------------|------|\n")
	for _, e := range pages {
		wikilink := makeWikilink(e.Path, e.Title)
		desc := e.Description
		if desc == "" {
			desc = "—"
		}
		desc = strings.ReplaceAll(desc, "|", "\\|")
		desc = strings.ReplaceAll(desc, "\n", " ")
		tags := e.Tags
		if tags == "" {
			tags = "—"
		}
		tags = strings.ReplaceAll(tags, ",", ", ")
		fmt.Fprintf(b, "| %s | %s | %s |\n", wikilink, desc, tags)
	}
}

// renderTree writes a tree-style directory listing.
func renderTree(b *strings.Builder, node *dirNode, prefix string) {
	for i, child := range node.children {
		isLast := i == len(node.children)-1
		connector := "├── "
		childPrefix := prefix + "│   "
		if isLast {
			connector = "└── "
			childPrefix = prefix + "    "
		}

		childName := filepath.Base(child.rel) + "/"
		childCount := countPages(child)
		fmt.Fprintf(b, "%s%s%s (%d pages)\n", prefix, connector, childName, childCount)

		// Show direct pages in this child (not recursing further)
		for j, p := range child.pages {
			pageIsLast := j == len(child.pages)-1 && len(child.children) == 0
			pageConnector := "├── "
			if pageIsLast {
				pageConnector = "└── "
			}
			pageName := strings.TrimSuffix(filepath.Base(p.Path), ".md")
			fmt.Fprintf(b, "%s%s%s\n", childPrefix, pageConnector, pageName)
		}

		// Show subdirectories
		if len(child.children) > 0 {
			renderTree(b, child, childPrefix)
		}
	}
}

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// makeWikilink builds [[path|Title]] from a relative .md path.
func makeWikilink(relPath, title string) string {
	link := strings.TrimSuffix(relPath, ".md")
	return fmt.Sprintf("[[%s\\|%s]]", link, title)
}
