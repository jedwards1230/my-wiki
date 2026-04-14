package vault

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v2"
)

// DefaultExcludedDirs are directories excluded from wiki page discovery by default.
var DefaultExcludedDirs = []string{".obsidian", "raw", "private"}

// Vault provides operations on a wiki vault directory.
type Vault struct {
	Dir          string
	Storage      Storage
	ExcludedDirs []string
}

// Option configures a Vault.
type Option func(*Vault)

// WithStorage overrides the default FilesystemStorage backend.
func WithStorage(s Storage) Option {
	return func(v *Vault) { v.Storage = s }
}

// New creates a Vault rooted at dir. Each instance gets its own copy of
// DefaultExcludedDirs so mutations on one Vault cannot affect others.
// Without options, it uses FilesystemStorage.
func New(dir string, opts ...Option) *Vault {
	excl := make([]string, len(DefaultExcludedDirs))
	copy(excl, DefaultExcludedDirs)
	v := &Vault{
		Dir:          dir,
		ExcludedDirs: excl,
	}
	for _, o := range opts {
		o(v)
	}
	if v.Storage == nil {
		v.Storage = NewFilesystemStorage(dir)
	}
	return v
}

// IsExcluded reports whether the given relative path matches one of the
// vault's excluded directories. Paths are normalized to forward slashes
// before comparison so the check works on all platforms.
func (v *Vault) IsExcluded(rel string) bool {
	normalized := filepath.ToSlash(rel)
	for _, d := range v.ExcludedDirs {
		if normalized == filepath.ToSlash(d) {
			return true
		}
	}
	return false
}

// FindWikiPages returns all .md files, excluding directories listed in
// ExcludedDirs (default: .obsidian/, raw/, private/).
func (v *Vault) FindWikiPages() ([]string, error) {
	var pages []string
	err := v.Storage.WalkDir("", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if v.IsExcluded(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(rel) == ".md" {
			pages = append(pages, filepath.Join(v.Dir, rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return pages, nil
}

// FindRawFiles returns all .md files in raw/.
func (v *Vault) FindRawFiles() ([]string, error) {
	if _, err := v.Storage.Stat("raw"); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	err := v.Storage.WalkDir("raw", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(rel) == ".md" {
			files = append(files, filepath.Join(v.Dir, rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ParseFrontmatter parses YAML frontmatter between --- markers into key-value pairs.
// List values (e.g. tags with "- item" entries) are joined as comma-separated strings.
func ParseFrontmatter(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	return parseFrontmatterScanner(scanner)
}

// ParseFrontmatterString parses YAML frontmatter from a content string.
// It behaves identically to ParseFrontmatter but operates on a string instead of a file.
func ParseFrontmatterString(content string) (map[string]string, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	return parseFrontmatterScanner(scanner)
}

// parseFrontmatterScanner is the shared implementation for frontmatter parsing.
func parseFrontmatterScanner(scanner *bufio.Scanner) (map[string]string, error) {
	fm := make(map[string]string)

	// First line must be ---
	if !scanner.Scan() {
		return fm, scanner.Err()
	}
	if strings.TrimSpace(scanner.Text()) != "---" {
		return nil, nil // no frontmatter
	}

	var listKey string // current key accumulating list items
	var listItems []string

	flushList := func() {
		if listKey != "" && len(listItems) > 0 {
			fm[listKey] = strings.Join(listItems, ",")
		}
		listKey = ""
		listItems = nil
	}

	closed := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			flushList()
			closed = true
			break
		}

		// Indented line: could be a list item for the current key
		if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
			trimmed := strings.TrimSpace(line)
			if listKey != "" && strings.HasPrefix(trimmed, "- ") {
				item := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
				// Strip surrounding quotes
				if len(item) >= 2 && item[0] == '"' && item[len(item)-1] == '"' {
					item = item[1 : len(item)-1]
				}
				listItems = append(listItems, item)
			}
			continue
		}

		// Top-level line: flush any pending list
		flushList()

		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// Strip surrounding quotes
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		fm[key] = val

		// If value is empty, this key might have list items below
		if val == "" {
			listKey = key
		}
	}

	if !closed {
		return nil, fmt.Errorf("unterminated frontmatter block (missing closing ---)")
	}

	return fm, scanner.Err()
}

// ValidateYAMLSyntax checks whether the frontmatter between --- markers is
// valid YAML by running yaml.Unmarshal. Returns nil if valid or no frontmatter,
// an error describing the parse failure otherwise.
func ValidateYAMLSyntax(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return ValidateYAMLSyntaxString(string(data))
}

// ValidateYAMLSyntaxString checks whether frontmatter in a content string is
// valid YAML. Returns nil if valid or no frontmatter present.
func ValidateYAMLSyntaxString(content string) error {
	raw, ok := extractRawFrontmatter(content)
	if !ok {
		return nil // no frontmatter — not a syntax error
	}
	var dest any
	if err := yaml.Unmarshal([]byte(raw), &dest); err != nil {
		return fmt.Errorf("invalid YAML in frontmatter: %v", err)
	}
	return nil
}

// extractRawFrontmatter returns the raw YAML string between --- markers.
// Returns ("", false) if no frontmatter block is found.
func extractRawFrontmatter(content string) (string, bool) {
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", false
	}

	// Find start (first ---) and end (second ---) markers.
	lines := strings.SplitAfter(content, "\n")
	var start, end int
	found := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "---" {
			if found == 0 {
				start = i + 1
				found++
			} else {
				end = i
				found++
				break
			}
		}
	}
	if found < 2 {
		return "", false
	}

	return strings.Join(lines[start:end], ""), true
}

var (
	wikilinkRe    = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	fencedBlockRe = regexp.MustCompile("^```")
)

// ExtractWikilinks extracts [[wikilink]] targets from a file, skipping fenced
// code blocks and inline code.
func ExtractWikilinks(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var links []string
	scanner := bufio.NewScanner(f)
	inFence := false

	for scanner.Scan() {
		line := scanner.Text()
		if fencedBlockRe.MatchString(line) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		// Remove inline code spans
		cleaned := removeInlineCode(line)
		matches := wikilinkRe.FindAllStringSubmatch(cleaned, -1)
		for _, m := range matches {
			target := m[1]
			// Strip display text after | or escaped \| (Obsidian uses
			// \| inside tables to avoid conflicting with Markdown pipe
			// syntax).
			if idx := strings.Index(target, `\|`); idx >= 0 {
				target = target[:idx]
			} else if idx := strings.IndexByte(target, '|'); idx >= 0 {
				target = target[:idx]
			}
			// Strip heading anchor after #
			if idx := strings.IndexByte(target, '#'); idx >= 0 {
				target = target[:idx]
			}
			target = strings.TrimSpace(target)
			if target != "" {
				links = append(links, target)
			}
		}
	}
	return links, scanner.Err()
}

// removeInlineCode removes `...` spans from a line.
func removeInlineCode(s string) string {
	var result strings.Builder
	inCode := false
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			inCode = !inCode
			continue
		}
		if !inCode {
			result.WriteByte(s[i])
		}
	}
	return result.String()
}

// slugExcludedDirs are directories excluded from slug indexing. This is
// narrower than ExcludedDirs because raw/ files are valid wikilink targets
// even though they are not wiki pages.
var slugExcludedDirs = map[string]bool{
	".obsidian": true,
	"private":   true,
}

// BuildSlugIndex builds a set of lowercase slugs for all pages in the vault
// (excluding .obsidian/ and private/). Both the basename and full relative path
// (without .md) are included. Note: raw/ is intentionally included because raw
// files can be wikilink targets.
func (v *Vault) BuildSlugIndex() (map[string]bool, error) {
	slugs := make(map[string]bool)
	err := v.Storage.WalkDir("", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if slugExcludedDirs[rel] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(rel) != ".md" {
			return nil
		}
		base := strings.TrimSuffix(filepath.Base(rel), ".md")
		relNoExt := strings.TrimSuffix(rel, ".md")
		slugs[strings.ToLower(base)] = true
		slugs[strings.ToLower(relNoExt)] = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return slugs, nil
}
