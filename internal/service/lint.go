package service

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// LintService provides vault lint operations.
type LintService struct {
	vault  *vault.Vault
	logSvc *LogService
}

// NewLintService creates a LintService for the given vault.
func NewLintService(v *vault.Vault, logSvc *LogService) *LintService {
	return &LintService{vault: v, logSvc: logSvc}
}

// Run executes the specified lint check and returns a report.
// Valid checks: "all", "frontmatter", "raw", "links", "orphans", "log".
func (s *LintService) Run(check string) (*LintReport, error) {
	report := &LintReport{}

	switch check {
	case "all":
		s.checkFrontmatter(report)
		s.checkRawFrontmatter(report)
		s.checkLinks(report)
		s.checkOrphans(report)
		s.checkLog(report)
	case "frontmatter":
		s.checkFrontmatter(report)
	case "raw":
		s.checkRawFrontmatter(report)
	case "links":
		s.checkLinks(report)
	case "orphans":
		s.checkOrphans(report)
	case "log":
		s.checkLog(report)
	default:
		return nil, fmt.Errorf("unknown check %q: must be all, frontmatter, raw, links, orphans, or log", check)
	}

	report.Total = len(report.Issues)
	return report, nil
}

func (s *LintService) checkLog(report *LintReport) {
	if s.logSvc == nil {
		return
	}
	issues, err := s.logSvc.Lint()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "log", Level: "ERROR", Message: err.Error(),
		})
		return
	}
	for _, issue := range issues {
		report.Issues = append(report.Issues, LintIssue{
			Check: "log", Level: "WARN", Message: issue.Message,
		})
	}
}

func (s *LintService) checkFrontmatter(report *LintReport) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "frontmatter", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)
		fm, err := vault.ParseFrontmatter(page)
		if err != nil {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "frontmatter", Level: "FAIL", Message: err.Error(),
			})
			continue
		}
		if fm == nil {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "frontmatter", Level: "FAIL", Message: "missing frontmatter",
			})
			continue
		}

		var missing []string
		for _, key := range []string{"title", "tags", "date"} {
			if _, ok := fm[key]; !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "frontmatter", Level: "WARN",
				Message: "missing: " + strings.Join(missing, " "),
			})
		}
	}
}

func (s *LintService) checkRawFrontmatter(report *LintReport) {
	files, err := s.vault.FindRawFiles()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "raw", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	for _, file := range files {
		rel, _ := filepath.Rel(s.vault.Dir, file)
		fm, err := vault.ParseFrontmatter(file)
		if err != nil {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "raw", Level: "FAIL", Message: err.Error(),
			})
			continue
		}
		if fm == nil {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "raw", Level: "FAIL", Message: "missing frontmatter",
			})
			continue
		}

		var missing []string
		for _, key := range []string{"title", "source", "date-added"} {
			if _, ok := fm[key]; !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "raw", Level: "WARN",
				Message: "missing: " + strings.Join(missing, " "),
			})
		}
	}
}

func (s *LintService) checkLinks(report *LintReport) {
	slugs, err := s.vault.BuildSlugIndex()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "links", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "links", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)
		links, err := vault.ExtractWikilinks(page)
		if err != nil {
			continue
		}
		for _, link := range links {
			target := strings.ToLower(link)
			if !slugs[target] {
				report.Issues = append(report.Issues, LintIssue{
					File: rel, Check: "links", Level: "WARN",
					Message: fmt.Sprintf("broken link [[%s]]", link),
				})
			}
		}
	}
}

// LintPage runs scoped lint checks on a single page after a create or edit
// mutation. It checks frontmatter completeness and outbound wikilink validity.
// Returns only issues introduced by this specific page — not vault-wide issues.
func (s *LintService) LintPage(relPath string) []LintIssue {
	if !strings.HasSuffix(relPath, ".md") {
		relPath += ".md"
	}

	var issues []LintIssue
	issues = append(issues, s.lintPageFrontmatter(relPath)...)
	issues = append(issues, s.lintPageLinks(relPath)...)
	return issues
}

// LintDelete checks for newly broken inbound links after a page deletion.
// It identifies pages that now reference a slug that no longer resolves.
func (s *LintService) LintDelete(relPath string) []LintIssue {
	if !strings.HasSuffix(relPath, ".md") {
		relPath += ".md"
	}

	// Compute slugs the deleted page contributed.
	base := strings.TrimSuffix(filepath.Base(relPath), ".md")
	relNoExt := strings.TrimSuffix(relPath, ".md")
	deletedSlugs := map[string]bool{
		strings.ToLower(base):     true,
		strings.ToLower(relNoExt): true,
	}

	// Build current slug index (post-deletion — the file is already removed).
	slugs, err := s.vault.BuildSlugIndex()
	if err != nil {
		return []LintIssue{{
			Check: "links", Level: "ERROR",
			Message: fmt.Sprintf("failed to build slug index: %v", err),
		}}
	}

	// If the deleted slugs still resolve (another page has the same basename),
	// no links are broken.
	anyOrphaned := false
	for slug := range deletedSlugs {
		if !slugs[slug] {
			anyOrphaned = true
			break
		}
	}
	if !anyOrphaned {
		return nil
	}

	// Walk wiki pages to find links that now point to nothing.
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return []LintIssue{{
			Check: "links", Level: "ERROR",
			Message: fmt.Sprintf("failed to find wiki pages: %v", err),
		}}
	}

	var issues []LintIssue
	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)
		links, err := vault.ExtractWikilinks(page)
		if err != nil {
			continue
		}
		for _, link := range links {
			target := strings.ToLower(link)
			if deletedSlugs[target] && !slugs[target] {
				issues = append(issues, LintIssue{
					File: rel, Check: "links", Level: "WARN",
					Message: fmt.Sprintf("broken link [[%s]] (target was deleted)", link),
				})
			}
		}
	}
	return issues
}

// lintPageFrontmatter checks frontmatter for a single page file.
func (s *LintService) lintPageFrontmatter(relPath string) []LintIssue {
	absPath := filepath.Join(s.vault.Dir, relPath)
	isRaw := strings.HasPrefix(relPath, "raw/") || strings.HasPrefix(relPath, "raw\\")

	fm, err := vault.ParseFrontmatter(absPath)
	if err != nil {
		check := "frontmatter"
		if isRaw {
			check = "raw"
		}
		return []LintIssue{{File: relPath, Check: check, Level: "FAIL", Message: err.Error()}}
	}
	if fm == nil {
		check := "frontmatter"
		if isRaw {
			check = "raw"
		}
		return []LintIssue{{File: relPath, Check: check, Level: "FAIL", Message: "missing frontmatter"}}
	}

	var required []string
	check := "frontmatter"
	if isRaw {
		required = []string{"title", "source", "date-added"}
		check = "raw"
	} else {
		required = []string{"title", "tags", "date"}
	}

	var missing []string
	for _, key := range required {
		if _, ok := fm[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return []LintIssue{{
			File: relPath, Check: check, Level: "WARN",
			Message: "missing: " + strings.Join(missing, " "),
		}}
	}
	return nil
}

// lintPageLinks checks outbound wikilinks for a single page.
func (s *LintService) lintPageLinks(relPath string) []LintIssue {
	if strings.HasPrefix(relPath, "raw/") || strings.HasPrefix(relPath, "raw\\") {
		return nil
	}

	absPath := filepath.Join(s.vault.Dir, relPath)
	slugs, err := s.vault.BuildSlugIndex()
	if err != nil {
		return []LintIssue{{
			File: relPath, Check: "links", Level: "ERROR",
			Message: fmt.Sprintf("failed to build slug index: %v", err),
		}}
	}

	links, err := vault.ExtractWikilinks(absPath)
	if err != nil {
		return []LintIssue{{
			File: relPath, Check: "links", Level: "ERROR",
			Message: fmt.Sprintf("failed to extract wikilinks: %v", err),
		}}
	}

	var issues []LintIssue
	for _, link := range links {
		target := strings.ToLower(link)
		if !slugs[target] {
			issues = append(issues, LintIssue{
				File: relPath, Check: "links", Level: "WARN",
				Message: fmt.Sprintf("broken link [[%s]]", link),
			})
		}
	}
	return issues
}

func (s *LintService) checkOrphans(report *LintReport) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "orphans", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	targets := make(map[string]bool)
	for _, page := range pages {
		links, err := vault.ExtractWikilinks(page)
		if err != nil {
			continue
		}
		for _, link := range links {
			targets[strings.ToLower(link)] = true
		}
	}

	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)
		base := strings.TrimSuffix(filepath.Base(page), ".md")

		// Skip index and log
		if base == "index" || rel == "meta/log.md" {
			continue
		}

		relNoExt := strings.TrimSuffix(rel, ".md")
		baseLower := strings.ToLower(base)
		relLower := strings.ToLower(relNoExt)

		if !targets[baseLower] && !targets[relLower] {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "orphans", Level: "WARN",
				Message: "no inbound links",
			})
		}
	}
}
