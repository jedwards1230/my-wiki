package service

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// PageInfo describes a wiki page.
type PageInfo struct {
	Path    string `json:"path"`
	Title   string `json:"title,omitempty"`
	HasMeta bool   `json:"has_meta"`
}

// PageService provides wiki page CRUD operations.
type PageService struct {
	vaultDir string
}

// NewPageService creates a PageService for the given vault directory.
func NewPageService(vaultDir string) *PageService {
	return &PageService{vaultDir: vaultDir}
}

// Read returns the content of a wiki page at the given relative path.
func (s *PageService) Read(relPath string) (string, error) {
	absPath, err := s.resolve(relPath)
	if err != nil {
		return "", err
	}

	data, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Check if the path without .md is a directory
			dirPath := strings.TrimSuffix(absPath, ".md")
			if info, dirErr := os.Stat(dirPath); dirErr == nil && info.IsDir() {
				return "", fmt.Errorf("%s is a directory, not a page", strings.TrimSuffix(relPath, ".md"))
			}
			return "", fmt.Errorf("page not found: %s", relPath)
		}
		return "", err
	}

	return string(data), nil
}

// Write creates or overwrites a wiki page at the given relative path.
// Content is validated for required frontmatter before writing.
func (s *PageService) Write(relPath, content string) error {
	if err := validateFrontmatter(relPath, content); err != nil {
		return err
	}

	absPath, err := s.resolve(relPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(absPath, []byte(content), 0o644)
}

// dateRe matches YYYY-MM-DD format.
var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validateFrontmatter checks that content has the required frontmatter fields
// for the given path. Wiki pages require title, tags, and date. Raw files
// require title, source, and date-added.
func validateFrontmatter(relPath, content string) error {
	fm, err := vault.ParseFrontmatterString(content)
	if err != nil {
		return fmt.Errorf("failed to parse frontmatter: %w", err)
	}
	if fm == nil {
		return fmt.Errorf("missing frontmatter block (expected --- delimiters)")
	}

	isRaw := strings.HasPrefix(relPath, "raw/") || strings.HasPrefix(relPath, "raw\\")

	if isRaw {
		return validateRawFrontmatter(fm)
	}
	return validateWikiFrontmatter(fm)
}

func validateWikiFrontmatter(fm map[string]string) error {
	if fm["title"] == "" {
		return fmt.Errorf("missing required frontmatter field: title")
	}
	tags, hasTags := fm["tags"]
	if !hasTags || tags == "" {
		return fmt.Errorf("missing required frontmatter field: tags (must have at least one tag)")
	}
	dateVal, hasDate := fm["date"]
	if !hasDate || dateVal == "" {
		return fmt.Errorf("missing required frontmatter field: date")
	}
	if !dateRe.MatchString(dateVal) {
		return fmt.Errorf("invalid date format: expected YYYY-MM-DD, got %q", dateVal)
	}
	return nil
}

func validateRawFrontmatter(fm map[string]string) error {
	if fm["title"] == "" {
		return fmt.Errorf("missing required frontmatter field: title")
	}
	if fm["source"] == "" {
		return fmt.Errorf("missing required frontmatter field: source")
	}
	dateVal, hasDate := fm["date-added"]
	if !hasDate || dateVal == "" {
		return fmt.Errorf("missing required frontmatter field: date-added")
	}
	if !dateRe.MatchString(dateVal) {
		return fmt.Errorf("invalid date-added format: expected YYYY-MM-DD, got %q", dateVal)
	}
	return nil
}

// Delete removes a wiki page at the given relative path.
func (s *PageService) Delete(relPath string) error {
	absPath, err := s.resolve(relPath)
	if err != nil {
		return err
	}

	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		return fmt.Errorf("page not found: %s", relPath)
	}

	return os.Remove(absPath)
}

// List returns all wiki pages under the given prefix.
// If prefix is empty, lists all pages.
func (s *PageService) List(prefix string) ([]PageInfo, error) {
	searchDir := s.vaultDir
	if prefix != "" {
		searchDir = filepath.Clean(filepath.Join(s.vaultDir, prefix))
		vaultPrefix := filepath.Clean(s.vaultDir) + string(filepath.Separator)
		if !strings.HasPrefix(searchDir, vaultPrefix) && searchDir != filepath.Clean(s.vaultDir) {
			return nil, fmt.Errorf("path traversal not allowed")
		}
	}

	var pages []PageInfo
	err := filepath.WalkDir(searchDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(s.vaultDir, p)
		if d.IsDir() {
			switch rel {
			case "raw", "private", ".obsidian":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(p) != ".md" {
			return nil
		}

		info := PageInfo{Path: rel}

		// Try to get title from frontmatter
		fm, fmErr := readFrontmatterTitle(p)
		if fmErr == nil && fm != "" {
			info.Title = fm
			info.HasMeta = true
		}

		pages = append(pages, info)
		return nil
	})
	if err != nil {
		return nil, err
	}

	return pages, nil
}

// Patch applies a series of find-and-replace operations to an existing page.
// If any find string is not found, it returns an error without writing.
func (s *PageService) Patch(relPath string, ops []PatchOp) (string, error) {
	if len(ops) == 0 {
		return "", fmt.Errorf("operations must not be empty")
	}
	for i, op := range ops {
		if op.Find == "" {
			return "", fmt.Errorf("operation %d: find must be non-empty", i)
		}
		_ = i // validated
	}

	content, err := s.Read(relPath)
	if err != nil {
		return "", err
	}

	for i, op := range ops {
		if !strings.Contains(content, op.Find) {
			return "", fmt.Errorf("patch op %d: find string not found in %s", i, relPath)
		}
		content = strings.Replace(content, op.Find, op.Replace, 1)
	}

	if err := s.Write(relPath, content); err != nil {
		return "", err
	}

	return content, nil
}

// resolve converts a relative path to an absolute path within the vault,
// ensuring it doesn't escape the vault directory.
func (s *PageService) resolve(relPath string) (string, error) {
	// Ensure .md extension
	if !strings.HasSuffix(relPath, ".md") {
		relPath += ".md"
	}

	absPath := filepath.Join(s.vaultDir, relPath)
	absPath = filepath.Clean(absPath)

	// Prevent path traversal: use separator suffix to avoid prefix collisions
	// e.g., /data/vault-evil would match /data/vault without the separator check
	vaultPrefix := filepath.Clean(s.vaultDir) + string(filepath.Separator)
	if !strings.HasPrefix(absPath, vaultPrefix) && absPath != filepath.Clean(s.vaultDir) {
		return "", fmt.Errorf("path traversal not allowed")
	}

	return absPath, nil
}

func readFrontmatterTitle(path string) (string, error) {
	fm, err := readSimpleFrontmatter(path)
	if err != nil {
		return "", err
	}
	return fm, nil
}

// readSimpleFrontmatter is a minimal frontmatter title reader that
// doesn't depend on vault package to avoid circular deps.
func readSimpleFrontmatter(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 1024)
	n, err := f.Read(buf)
	if err != nil {
		return "", err
	}
	content := string(buf[:n])

	if !strings.HasPrefix(content, "---\n") {
		return "", nil
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return "", nil
	}

	fm := content[4 : 4+end]
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(line, "title:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "title:"))
			if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
				val = val[1 : len(val)-1]
			}
			return val, nil
		}
	}

	return "", nil
}
