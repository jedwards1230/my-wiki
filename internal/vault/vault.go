package vault

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Vault provides operations on a wiki vault directory.
type Vault struct {
	Dir string
}

// New creates a Vault rooted at dir.
func New(dir string) *Vault {
	return &Vault{Dir: dir}
}

// FindWikiPages returns all .md files excluding raw/, private/, .obsidian/.
func (v *Vault) FindWikiPages() ([]string, error) {
	var pages []string
	err := filepath.WalkDir(v.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(v.Dir, p)
		if d.IsDir() {
			switch rel {
			case "raw", "private", ".obsidian":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) == ".md" {
			pages = append(pages, p)
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
	rawDir := filepath.Join(v.Dir, "raw")
	if _, err := os.Stat(rawDir); os.IsNotExist(err) {
		return nil, nil
	}
	var files []string
	err := filepath.WalkDir(rawDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(p) == ".md" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// ParseFrontmatter parses YAML frontmatter between --- markers into key-value pairs.
// For keys like "tags:" that have list values below, the value is empty string but the
// key is present in the map.
func ParseFrontmatter(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	fm := make(map[string]string)
	scanner := bufio.NewScanner(f)

	// First line must be ---
	if !scanner.Scan() {
		return fm, scanner.Err()
	}
	if strings.TrimSpace(scanner.Text()) != "---" {
		return nil, nil // no frontmatter
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		// Only parse top-level key: value lines (not indented list items)
		if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
			continue
		}
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
	}

	return fm, scanner.Err()
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
			// Strip display text after |
			if idx := strings.IndexByte(target, '|'); idx >= 0 {
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

// BuildSlugIndex builds a set of lowercase slugs for all pages in the vault
// (excluding .obsidian/ and private/). Both the basename and full relative path
// (without .md) are included.
func (v *Vault) BuildSlugIndex() (map[string]bool, error) {
	slugs := make(map[string]bool)
	err := filepath.WalkDir(v.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(v.Dir, p)
		if d.IsDir() {
			switch rel {
			case ".obsidian", "private":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".md" {
			return nil
		}
		base := strings.TrimSuffix(filepath.Base(p), ".md")
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
