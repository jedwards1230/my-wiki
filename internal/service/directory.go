package service

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jedwards1230/my-wiki/internal/vault"
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

// List returns wiki pages with their frontmatter metadata.
// If prefix is non-empty, only pages under that directory are returned.
func (s *DirectoryService) List(prefix string) ([]DirectoryEntry, error) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return nil, err
	}

	normalizedPrefix := filepath.ToSlash(strings.TrimRight(prefix, "/\\"))

	var result []DirectoryEntry
	for _, p := range pages {
		rel, err := filepath.Rel(s.vault.Dir, p)
		if err != nil {
			return nil, fmt.Errorf("compute relative path for %q from %q: %w", p, s.vault.Dir, err)
		}
		rel = filepath.ToSlash(rel)

		if normalizedPrefix != "" && rel != normalizedPrefix && !strings.HasPrefix(rel, normalizedPrefix+"/") {
			continue
		}

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

// directoryExcludedDirs are additional dirs excluded from directory index generation
// beyond the vault-level exclusions.
var directoryExcludedDirs = map[string]bool{
	"meta/activity":  true,
	"meta\\activity": true, // Windows
}

// isExcludedDir checks if a relative directory path should be excluded.
func (s *DirectoryService) isExcludedDir(rel string) bool {
	if s.vault.IsExcluded(rel) {
		return true
	}
	if directoryExcludedDirs[rel] {
		return true
	}
	for excluded := range directoryExcludedDirs {
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

// Generate writes index.md files across the vault: one per directory
// under the vault root that isn't excluded. Root gets a folder tree +
// vault-wide tag overview; mid-level dirs get subtree structure + scoped
// tags; leaf dirs get flat page tables.
//
// A directory is covered by Generate regardless of whether it currently
// holds content pages. Directories that contain only sub-directories
// still get a regenerated index.md reflecting the current subtree.
//
// Directories that became *fully* empty — no pages, no surviving
// children after recursive pruning — are removed: the stale index.md
// is deleted and `os.Remove` is attempted on the directory itself.
// The rmdir succeeds only when the directory is truly empty (no
// non-md files, no Obsidian metadata), so directories holding
// non-wiki content are preserved with their index.md gone.
func (s *DirectoryService) Generate() (string, int, error) {
	entries, err := s.List("")
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
		if s.isExcludedDir(dir) {
			continue
		}
		pages = append(pages, e)
	}

	// Enumerate non-excluded directories separately so the tree covers
	// every directory on disk, not just those that currently hold pages.
	dirs, err := s.listDirs()
	if err != nil {
		return "", 0, err
	}

	// Build directory tree
	root := buildDirTree(pages, dirs)

	// Write index files
	filesWritten := 0
	today := time.Now().Format("2006-01-02")

	// writeOrPrune walks the tree post-order. Non-root nodes that hold no
	// pages and no surviving children are pruned: their stale index.md is
	// removed and the directory itself is `os.Remove`d (succeeds only when
	// the dir is truly empty). The pruned bool returned to the caller lets
	// the parent recompute whether it too has become empty after children
	// were removed.
	var writeOrPrune func(node *dirNode, isRoot bool) (bool, error)
	writeOrPrune = func(node *dirNode, isRoot bool) (bool, error) {
		// Post-order: handle children first so we know which survived.
		var survivors []*dirNode
		for _, child := range node.children {
			pruned, err := writeOrPrune(child, false)
			if err != nil {
				return false, err
			}
			if !pruned {
				survivors = append(survivors, child)
			}
		}
		node.children = survivors

		// Prune this node when it's an empty non-root leaf after children
		// settled. Root is never pruned — the wiki always has a homepage.
		if !isRoot && len(node.pages) == 0 && len(node.children) == 0 {
			indexPath := filepath.Join(s.vault.Dir, node.rel, "index.md")
			if err := os.Remove(indexPath); err != nil && !os.IsNotExist(err) {
				return false, err
			}
			// Attempt rmdir. ENOTEMPTY is the intentional "keep the dir,
			// drop the index" case — a dir holding non-md content or
			// hidden metadata stays put. ENOENT means we already cleaned
			// up in an earlier run. Any other error (permissions, IO) is
			// surfaced so unexpected failures aren't silently masked.
			if err := os.Remove(filepath.Join(s.vault.Dir, node.rel)); err != nil &&
				!os.IsNotExist(err) && !errors.Is(err, syscall.ENOTEMPTY) {
				return false, err
			}
			return true, nil
		}

		// Write index for this surviving node.
		content := renderIndex(node, pages, today)
		indexPath := filepath.Join(s.vault.Dir, node.rel, "index.md")
		if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
			return false, err
		}
		newContent := []byte(content)
		// Skip the write if bytes are unchanged. Rewriting with identical content
		// still bumps mtime, which fsnotify reports as a Write event — causing a
		// self-sustaining rebuild loop when Generate is wired to the vault watcher
		// in serve mode.
		//
		// Only treat "does not exist" as reason to fall through to the write.
		// Other read errors (permissions, IO) are surfaced rather than quietly
		// overwritten — otherwise a transient read failure would mask itself
		// *and* reintroduce the feedback loop on every call.
		existing, readErr := os.ReadFile(indexPath)
		if readErr != nil && !os.IsNotExist(readErr) {
			return false, readErr
		}
		if readErr != nil || !bytes.Equal(existing, newContent) {
			if err := os.WriteFile(indexPath, newContent, 0o644); err != nil {
				return false, err
			}
			filesWritten++
		}
		return false, nil
	}

	if _, err := writeOrPrune(root, true); err != nil {
		return "", 0, err
	}

	return filepath.Join(s.vault.Dir, "index.md"), len(pages), nil
}

// listDirs returns every non-excluded directory under the vault root,
// relative to the root, forward-slash separated. The root ("") is not
// included — callers always have it. Excluded subtrees are pruned via
// filepath.SkipDir so walks don't descend into them.
func (s *DirectoryService) listDirs() ([]string, error) {
	var dirs []string
	err := s.vault.Storage.WalkDir("", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "" || rel == "." {
			return nil
		}
		if s.isExcludedDir(rel) {
			return filepath.SkipDir
		}
		dirs = append(dirs, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return dirs, nil
}

// buildDirTree organizes pages and directories into a tree. dirs lists
// every non-excluded directory in the vault; it guarantees empty dirs
// and dirs-of-subdirs still get a node (so their index.md gets
// regenerated, rather than staying frozen at whatever state they had
// when a content page last lived under them).
func buildDirTree(pages []DirectoryEntry, dirs []string) *dirNode {
	nodes := map[string]*dirNode{
		"": {rel: ""},
	}

	// Seed nodes for every known directory first so empty/subdir-only
	// dirs show up in the tree even when no page points into them.
	for _, d := range dirs {
		ensureNode(nodes, filepath.ToSlash(d))
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
func renderIndex(node *dirNode, allPages []DirectoryEntry, today string) string {
	isRoot := node.rel == ""
	hasChildren := len(node.children) > 0

	var b strings.Builder

	// Frontmatter
	title := "My Wiki"
	desc := "Shared knowledge base for the Edwards homelab"
	if !isRoot {
		title = titleCase(filepath.Base(node.rel))
		desc = fmt.Sprintf("Index of %s", node.rel)
	}

	pageCount := countPages(node)

	b.WriteString("---\n")
	fmt.Fprintf(&b, "title: %s\n", title)
	if !isRoot {
		b.WriteString("tags:\n  - meta\n")
	}
	fmt.Fprintf(&b, "date: %s\n", today)
	fmt.Fprintf(&b, "description: %s\n", desc)
	fmt.Fprintf(&b, "pages: %d\n", pageCount)
	b.WriteString("generated: true\n")
	b.WriteString("---\n\n")

	if isRoot {
		renderRootIndex(&b, node, allPages)
	} else if hasChildren {
		renderMidIndex(&b, node)
	} else {
		renderLeafIndex(&b, node)
	}

	return b.String()
}

func renderRootIndex(b *strings.Builder, root *dirNode, allPages []DirectoryEntry) {
	b.WriteString("A shared knowledge base — maintained by humans and AI agents.\n\n")
	b.WriteString("See [[meta/schema]] for the operating manual. See [[meta/log]] for recent activity.\n")

	// Directory tree
	b.WriteString("\n## Directory\n\n")
	renderTreeWikilinks(b, root, 0, 2)

	// Tag overview
	tags := collectTags(allPages)
	if len(tags) > 0 {
		b.WriteString("\n## Tags\n\n")
		renderTagList(b, tags)
	}
}

func renderMidIndex(b *strings.Builder, node *dirNode) {
	allBelow := collectPages(node)

	b.WriteString("\n## Directory\n\n")
	renderSubdirTree(b, node, 0, 1)

	// Scoped tags
	tags := collectTags(allBelow)
	if len(tags) > 0 {
		b.WriteString("\n## Tags\n\n")
		renderTagList(b, tags)
	}
}

func renderLeafIndex(b *strings.Builder, node *dirNode) {
	b.WriteString("\n## Directory\n\n")
	renderSubdirTree(b, node, 0, 0)

	// Scoped tags
	tags := collectTags(node.pages)
	if len(tags) > 0 {
		b.WriteString("\n## Tags\n\n")
		renderTagList(b, tags)
	}
}

// renderSubdirTree writes a bullet list of pages and subdirectories for
// subdirectory index files. Pages are listed first, then child directories.
// maxDepth controls how many levels of children to expand (0 = pages only).
func renderSubdirTree(b *strings.Builder, node *dirNode, depth, maxDepth int) {
	indent := strings.Repeat("  ", depth)

	// Pages first
	for _, p := range node.pages {
		wikilink := makeWikilink(p.Path, p.Title)
		if p.Description != "" {
			description := strings.Join(strings.Fields(p.Description), " ")
			fmt.Fprintf(b, "%s- %s — %s\n", indent, wikilink, description)
		} else {
			fmt.Fprintf(b, "%s- %s\n", indent, wikilink)
		}
	}

	// Child directories
	for _, child := range node.children {
		childName := filepath.Base(child.rel)
		childCount := countPages(child)
		fmt.Fprintf(b, "%s- [[%s/index\\|%s/]] (%d pages)\n", indent, child.rel, childName, childCount)

		if depth < maxDepth {
			renderSubdirTree(b, child, depth+1, maxDepth)
		}
	}
}

// renderTagList writes tags as a compact inline list: `tag (count) · tag (count) · ...`
func renderTagList(b *strings.Builder, tags []tagCount) {
	for i, tc := range tags {
		if i > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(b, "#%s (%d)", tc.Tag, tc.Count)
	}
	b.WriteString("\n")
}

// renderTreeWikilinks writes a directory tree using markdown lists with wikilinks,
// capped at maxDepth levels.
func renderTreeWikilinks(b *strings.Builder, node *dirNode, depth, maxDepth int) {
	indent := strings.Repeat("  ", depth)

	// Show direct pages at this level
	for _, p := range node.pages {
		wikilink := makeWikilink(p.Path, p.Title)
		fmt.Fprintf(b, "%s- %s\n", indent, wikilink)
	}

	// Show child directories
	for _, child := range node.children {
		childName := filepath.Base(child.rel)
		childCount := countPages(child)
		// Link to the child's index
		fmt.Fprintf(b, "%s- [[%s/index\\|%s/]] (%d pages)\n", indent, child.rel, childName, childCount)

		if depth+1 < maxDepth {
			renderTreeWikilinks(b, child, depth+1, maxDepth)
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
