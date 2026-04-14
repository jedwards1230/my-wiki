package service

import (
	"fmt"
	"io/fs"
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
	storage      vault.Storage
	excludedDirs []string
	onMutation   func(MutationEvent)
}

// PageOption configures optional PageService behavior.
type PageOption func(*PageService)

// WithOnMutation sets a callback invoked after successful page mutations.
func WithOnMutation(fn func(MutationEvent)) PageOption {
	return func(s *PageService) { s.onMutation = fn }
}

// WithExcludedDirs sets the directories to exclude from page listing.
func WithExcludedDirs(dirs []string) PageOption {
	return func(s *PageService) { s.excludedDirs = dirs }
}

// NewPageService creates a PageService backed by the given storage.
func NewPageService(storage vault.Storage, opts ...PageOption) *PageService {
	s := &PageService{
		storage:      storage,
		excludedDirs: vault.DefaultExcludedDirs,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ensureMD ensures the path has a .md extension.
func ensureMD(relPath string) string {
	if !strings.HasSuffix(relPath, ".md") {
		relPath += ".md"
	}
	return relPath
}

// Read returns the content of a wiki page at the given relative path.
func (s *PageService) Read(relPath string) (string, error) {
	relPath = ensureMD(relPath)

	data, err := s.storage.ReadFile(relPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Check if the path without .md is a directory
			dirPath := strings.TrimSuffix(relPath, ".md")
			if info, dirErr := s.storage.Stat(dirPath); dirErr == nil && info.IsDir() {
				return "", fmt.Errorf("%s is a directory, not a page", dirPath)
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

	relPath = ensureMD(relPath)

	// Check existence before writing to distinguish create vs edit.
	existed := false
	if _, statErr := s.storage.Stat(relPath); statErr == nil {
		existed = true
	} else if !os.IsNotExist(statErr) {
		return statErr
	}

	if err := s.storage.WriteFile(relPath, []byte(content), 0o644); err != nil {
		return err
	}

	if s.onMutation != nil {
		kind := MutationEdit
		if !existed {
			kind = MutationCreate
		}
		s.onMutation(MutationEvent{Kind: kind, Path: filepath.ToSlash(relPath)})
	}

	return nil
}

// ValidationError is returned when page content fails frontmatter validation.
// Callers can type-assert to distinguish validation failures from filesystem errors.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// dateRe matches YYYY-MM-DD format.
var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// validateFrontmatter checks that content has the required frontmatter fields
// for the given path. Wiki pages require title, tags, and date. Raw files
// require title, source, and date-added.
func validateFrontmatter(relPath, content string) error {
	fm, err := vault.ParseFrontmatterString(content)
	if err != nil {
		return &ValidationError{Message: fmt.Sprintf("failed to parse frontmatter: %v", err)}
	}
	if fm == nil {
		return &ValidationError{Message: "missing frontmatter block (expected --- delimiters)"}
	}

	isRaw := strings.HasPrefix(relPath, "raw/") || strings.HasPrefix(relPath, "raw\\")

	if isRaw {
		return validateRawFrontmatter(fm)
	}
	return validateWikiFrontmatter(fm)
}

func validateWikiFrontmatter(fm map[string]string) error {
	if fm["title"] == "" {
		return &ValidationError{Message: "missing required frontmatter field: title"}
	}
	tags, hasTags := fm["tags"]
	if !hasTags || tags == "" {
		return &ValidationError{Message: "missing required frontmatter field: tags (must have at least one tag)"}
	}
	dateVal, hasDate := fm["date"]
	if !hasDate || dateVal == "" {
		return &ValidationError{Message: "missing required frontmatter field: date"}
	}
	if !dateRe.MatchString(dateVal) {
		return &ValidationError{Message: fmt.Sprintf("invalid date format: expected YYYY-MM-DD, got %q", dateVal)}
	}
	return nil
}

func validateRawFrontmatter(fm map[string]string) error {
	if fm["title"] == "" {
		return &ValidationError{Message: "missing required frontmatter field: title"}
	}
	if fm["source"] == "" {
		return &ValidationError{Message: "missing required frontmatter field: source"}
	}
	dateVal, hasDate := fm["date-added"]
	if !hasDate || dateVal == "" {
		return &ValidationError{Message: "missing required frontmatter field: date-added"}
	}
	if !dateRe.MatchString(dateVal) {
		return &ValidationError{Message: fmt.Sprintf("invalid date-added format: expected YYYY-MM-DD, got %q", dateVal)}
	}
	return nil
}

// Delete removes a wiki page at the given relative path.
func (s *PageService) Delete(relPath string) error {
	relPath = ensureMD(relPath)

	if _, err := s.storage.Stat(relPath); os.IsNotExist(err) {
		return fmt.Errorf("page not found: %s", relPath)
	}

	if err := s.storage.Remove(relPath); err != nil {
		return err
	}

	if s.onMutation != nil {
		s.onMutation(MutationEvent{Kind: MutationDelete, Path: filepath.ToSlash(relPath)})
	}

	return nil
}

// List returns all wiki pages under the given prefix.
// If prefix is empty, lists all pages.
func (s *PageService) List(prefix string) ([]PageInfo, error) {
	searchDir := ""
	if prefix != "" {
		searchDir = prefix
	}

	var pages []PageInfo
	err := s.storage.WalkDir(searchDir, func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			for _, excluded := range s.excludedDirs {
				if rel == excluded {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if filepath.Ext(rel) != ".md" {
			return nil
		}

		info := PageInfo{Path: rel}

		// Try to get title from frontmatter (read only a small prefix)
		f, readErr := s.storage.OpenFile(rel, os.O_RDONLY, 0)
		if readErr == nil {
			buf := make([]byte, 1024)
			n, _ := f.Read(buf)
			_ = f.Close()
			if title := extractTitle(buf[:n]); title != "" {
				info.Title = title
				info.HasMeta = true
			}
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

// extractTitle extracts the title field from YAML frontmatter bytes.
func extractTitle(data []byte) string {
	content := string(data)
	if !strings.HasPrefix(content, "---\n") {
		return ""
	}

	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return ""
	}

	fm := content[4 : 4+end]
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(line, "title:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "title:"))
			if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
				val = val[1 : len(val)-1]
			}
			return val
		}
	}

	return ""
}
