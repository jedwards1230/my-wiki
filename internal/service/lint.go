package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// LintService provides vault lint operations.
type LintService struct {
	vault  *vault.Vault
	logSvc *LogService
	tagSvc *TagService
}

// NewLintService creates a LintService for the given vault.
func NewLintService(v *vault.Vault, logSvc *LogService) *LintService {
	return &LintService{vault: v, logSvc: logSvc, tagSvc: NewTagService(v)}
}

// Run executes the specified lint check and returns a report.
// Valid checks: "all", "frontmatter", "raw", "links", "orphans", "log".
func (s *LintService) Run(check string) (*LintReport, error) {
	report := &LintReport{}

	switch check {
	case "all":
		s.checkFrontmatter(report)
		s.checkRawFrontmatter(report)
		s.checkTags(report, false)
		s.checkLinks(report)
		s.checkOrphans(report)
		s.checkSize(report)
		s.checkLog(report)
	case "frontmatter":
		s.checkFrontmatter(report)
	case "raw":
		s.checkRawFrontmatter(report)
	case "tags":
		s.checkTags(report, true)
	case "links":
		s.checkLinks(report)
	case "orphans":
		s.checkOrphans(report)
	case "size":
		s.checkSize(report)
	case "log":
		s.checkLog(report)
	default:
		return nil, fmt.Errorf("unknown check %q: must be all, frontmatter, raw, tags, links, orphans, size, or log", check)
	}

	report.Total = len(report.Issues)
	for _, issue := range report.Issues {
		if issue.Level != "INFO" {
			report.Errors++
		}
	}
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

		if err := vault.ValidateYAMLSyntax(page); err != nil {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "frontmatter", Level: "FAIL", Message: err.Error(),
			})
			continue
		}

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

		// Generated pages (e.g. index.md) are exempt from required-field checks.
		if fm["generated"] == "true" {
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

		if err := vault.ValidateYAMLSyntax(file); err != nil {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "raw", Level: "FAIL", Message: err.Error(),
			})
			continue
		}

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

// minPagesPerTag is the minimum page count before a tag is considered established.
const minPagesPerTag = 3

func (s *LintService) checkTags(report *LintReport, required bool) {
	taxonomy, err := s.tagSvc.ParseTaxonomy()
	if err != nil {
		level := "WARN"
		message := fmt.Sprintf("tags check skipped: %v", err)
		if required {
			level = "ERROR"
			message = fmt.Sprintf("failed to parse tag taxonomy: %v", err)
		}
		report.Issues = append(report.Issues, LintIssue{
			Check: "tags", Level: level, Message: message,
		})
		return
	}

	// Build allow-set from taxonomy.
	allowed := make(map[string]bool)
	for _, e := range taxonomy {
		allowed[e.Domain] = true
		for _, st := range e.SubTags {
			allowed[st] = true
		}
	}

	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "tags", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	// Single pass: validate per-page tags and count usage (skipping generated pages).
	tagCounts := make(map[string]int)
	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)
		fm, fmErr := vault.ParseFrontmatter(page)
		if fmErr != nil || fm == nil {
			continue
		}
		if fm["generated"] == "true" {
			continue
		}

		tags := fm["tags"]
		if tags == "" {
			continue // checkFrontmatter already catches missing tags
		}

		for _, tag := range strings.Split(tags, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "" {
				continue
			}
			tagCounts[tag]++

			if !allowed[tag] {
				domain := tag
				if idx := strings.IndexByte(tag, '/'); idx >= 0 {
					domain = tag[:idx]
				}
				if allowed[domain] {
					report.Issues = append(report.Issues, LintIssue{
						File: rel, Check: "tags", Level: "WARN",
						Message: fmt.Sprintf("tag %q not in taxonomy (domain %q is valid — add sub-tag to schema)", tag, domain),
					})
				} else {
					report.Issues = append(report.Issues, LintIssue{
						File: rel, Check: "tags", Level: "WARN",
						Message: fmt.Sprintf("tag %q not in taxonomy (unknown domain %q)", tag, domain),
					})
				}
			}
		}
	}

	// Taxonomy-level: flag unused domains and under-threshold tags.
	for _, e := range taxonomy {
		domainTotal := tagCounts[e.Domain]
		for _, st := range e.SubTags {
			domainTotal += tagCounts[st]
		}
		if domainTotal == 0 {
			report.Issues = append(report.Issues, LintIssue{
				Check: "tags", Level: "INFO",
				Message: fmt.Sprintf("taxonomy domain %q has 0 pages — consider removing or populating", e.Domain),
			})
		}
	}
	for tag, count := range tagCounts {
		if !allowed[tag] {
			continue // already reported per-page above
		}
		if count < minPagesPerTag {
			report.Issues = append(report.Issues, LintIssue{
				Check: "tags", Level: "INFO",
				Message: fmt.Sprintf("tag %q used on %d page(s) (schema recommends %d+ before adding a tag)", tag, count, minPagesPerTag),
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

	// Collect broken links grouped by target to deduplicate.
	type brokenLink struct {
		target  string // original case from first occurrence
		sources []string
	}
	seen := make(map[string]*brokenLink) // keyed by lowercase target
	var order []string                   // insertion order

	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)
		links, err := vault.ExtractWikilinks(page)
		if err != nil {
			continue
		}
		for _, link := range links {
			key := strings.ToLower(link)
			if slugs[key] {
				continue
			}
			bl, ok := seen[key]
			if !ok {
				bl = &brokenLink{target: link}
				seen[key] = bl
				order = append(order, key)
			}
			bl.sources = append(bl.sources, rel)
		}
	}

	for _, key := range order {
		bl := seen[key]
		report.Issues = append(report.Issues, LintIssue{
			Check: "links", Level: "WARN",
			Message: fmt.Sprintf("missing page [[%s]], linked from: %s",
				bl.target, strings.Join(bl.sources, ", ")),
		})
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

	check := "frontmatter"
	if isRaw {
		check = "raw"
	}

	// Validate YAML syntax before field checks — catches malformed YAML
	// that the lenient key-value parser would silently skip.
	if err := vault.ValidateYAMLSyntax(absPath); err != nil {
		return []LintIssue{{File: relPath, Check: check, Level: "FAIL", Message: err.Error()}}
	}

	fm, err := vault.ParseFrontmatter(absPath)
	if err != nil {
		return []LintIssue{{File: relPath, Check: check, Level: "FAIL", Message: err.Error()}}
	}
	if fm == nil {
		return []LintIssue{{File: relPath, Check: check, Level: "FAIL", Message: "missing frontmatter"}}
	}

	var required []string
	if isRaw {
		required = []string{"title", "source", "date-added"}
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

// maxPageWords is the word-count threshold above which a page gets a size warning.
const maxPageWords = 1000

func (s *LintService) checkSize(report *LintReport) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "size", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)

		data, err := os.ReadFile(page)
		if err != nil {
			continue
		}
		content := string(data)

		// Strip frontmatter before counting words.
		if strings.HasPrefix(content, "---\n") {
			if idx := strings.Index(content[4:], "\n---"); idx >= 0 {
				content = content[4+idx+4:]
			}
		}

		words := len(strings.Fields(content))
		if words > maxPageWords {
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "size", Level: "WARN",
				Message: fmt.Sprintf("%d words (limit %d) — consider splitting", words, maxPageWords),
			})
		}
	}
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
