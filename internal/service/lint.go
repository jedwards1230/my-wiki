package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

// LintService provides vault lint operations.
type LintService struct {
	vault     *vault.Vault
	logSvc    *LogService
	tagSvc    *TagService
	config    LintConfig
	configErr error // non-nil when meta/lint-config.yaml exists but failed to parse
}

// NewLintService creates a LintService for the given vault. It eagerly
// loads meta/lint-config.yaml (best-effort): a missing file is fine and
// uses defaults; a malformed file is kept as configErr and surfaced as a
// lint issue under the "config" check rather than failing construction.
func NewLintService(v *vault.Vault, logSvc *LogService) *LintService {
	cfg, err := LoadLintConfig(v.Dir)
	return &LintService{
		vault:     v,
		logSvc:    logSvc,
		tagSvc:    NewTagService(v),
		config:    cfg,
		configErr: err,
	}
}

// Run executes the specified lint check and returns a report.
// Valid checks: "all", "frontmatter", "tags", "links", "orphans", "size", "clippings", "stub", "log".
func (s *LintService) Run(check string) (*LintReport, error) {
	report := &LintReport{}

	switch check {
	case "all":
		s.checkFrontmatter(report)
		s.checkTags(report, false)
		s.checkLinks(report)
		s.checkOrphans(report)
		s.checkSize(report)
		s.checkClippings(report)
		s.checkStub(report)
		s.checkLog(report)
	case "frontmatter":
		s.checkFrontmatter(report)
	case "tags":
		s.checkTags(report, true)
	case "links":
		s.checkLinks(report)
	case "orphans":
		s.checkOrphans(report)
	case "size":
		s.checkSize(report)
	case "clippings":
		s.checkClippings(report)
	case "stub":
		s.checkStub(report)
	case "log":
		s.checkLog(report)
	default:
		return nil, fmt.Errorf("unknown check %q: must be all, frontmatter, tags, links, orphans, size, clippings, stub, or log", check)
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

func (s *LintService) checkTags(report *LintReport, _ bool) {
	tagCounts, err := s.tagSvc.CountTags()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "tags", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "tags", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	// Per-page structural checks.
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

			// Structural: must be kebab-case.
			if !IsKebabCase(tag) {
				report.Issues = append(report.Issues, LintIssue{
					File: rel, Check: "tags", Level: "WARN",
					Message: fmt.Sprintf("tag %q is not kebab-case", tag),
				})
			}
		}
	}

	// Vault-wide checks.
	for tag, count := range tagCounts {
		// Single-page tags are likely typos or cleanup candidates.
		if count == 1 {
			report.Issues = append(report.Issues, LintIssue{
				Check: "tags", Level: "INFO",
				Message: fmt.Sprintf("tag %q used on only 1 page", tag),
			})
		}

		// Sub-tags whose parent domain has no pages.
		if strings.Contains(tag, "/") {
			domain := ParentDomain(tag)
			if tagCounts[domain] == 0 {
				report.Issues = append(report.Issues, LintIssue{
					Check: "tags", Level: "INFO",
					Message: fmt.Sprintf("tag %q has no pages using parent domain %q", tag, domain),
				})
			}
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
			if _, ok := slugs[key]; ok {
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
		if _, ok := slugs[slug]; !ok {
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
			_, stillExists := slugs[target]
			if deletedSlugs[target] && !stillExists {
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
	const check = "frontmatter"

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

	required := []string{"title", "tags", "date"}

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
		if _, ok := slugs[target]; !ok {
			issues = append(issues, LintIssue{
				File: relPath, Check: "links", Level: "WARN",
				Message: fmt.Sprintf("broken link [[%s]]", link),
			})
		}
	}
	return issues
}

// stripFrontmatter returns the body of a markdown document with any leading
// `---\n…\n---` YAML frontmatter block removed. Returns the original string
// unchanged when no frontmatter is present or the block is unterminated.
//
// Handles the empty-frontmatter edge case (`---\n---\nBody`) where the
// closing marker sits directly after the opener with no inner content —
// the substring `\n---` doesn't appear in `content[4:]` for that input,
// so we have to check for a `---` prefix on the remainder before falling
// back to the substring scan.
func stripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	rest := content[4:]
	if strings.HasPrefix(rest, "---") {
		// Empty frontmatter: skip the closing marker and the line break after it.
		return strings.TrimPrefix(strings.TrimPrefix(rest, "---"), "\n")
	}
	if idx := strings.Index(rest, "\n---"); idx >= 0 {
		return strings.TrimPrefix(rest[idx+4:], "\n")
	}
	return content
}

// checkClippings enforces the [[meta/schema#Clippings pattern]] rule: every
// page tagged with the configured clipping tag must have at least one
// navigable link to its verbatim raw file in the body — either a markdown
// link to a path under the configured raw-path prefix or a wikilink to
// one. The frontmatter `raw:` field is not enough on its own: it isn't
// rendered as a link in Quartz, and stripping frontmatter before scanning
// means the body must carry the connection independently.
//
// Both the tag name and the raw-path prefix are read from
// meta/lint-config.yaml so this check stays in sync with the schema
// without a code change. See [[LintConfig]].
func (s *LintService) checkClippings(report *LintReport) {
	// Surface a config parse error up-front: if the file exists but
	// failed to load, the check is effectively running on defaults and
	// the user almost certainly wants to know.
	if s.configErr != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "clippings", Level: "ERROR",
			Message: fmt.Sprintf("using default config: %v", s.configErr),
		})
	}

	clippingTag := s.config.Clippings.Tag
	rawPrefix := s.config.Clippings.RawPathPrefix

	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "clippings", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	for _, page := range pages {
		fm, err := vault.ParseFrontmatter(page)
		if err != nil || fm == nil {
			continue
		}

		hasClippingTag := false
		for _, t := range strings.Split(fm["tags"], ",") {
			if strings.TrimSpace(t) == clippingTag {
				hasClippingTag = true
				break
			}
		}
		if !hasClippingTag {
			continue
		}

		data, err := os.ReadFile(page)
		if err != nil {
			continue
		}
		body := stripFrontmatter(string(data))

		// Permissive substring match — accepts markdown URLs, root-relative
		// paths, and wikilinks all targeting the raw-path prefix.
		if !strings.Contains(body, rawPrefix) {
			rel, _ := filepath.Rel(s.vault.Dir, page)
			report.Issues = append(report.Issues, LintIssue{
				File: rel, Check: "clippings", Level: "WARN",
				Message: fmt.Sprintf("page tagged %q has no link into %q in body — add a '## Sources' section", clippingTag, rawPrefix),
			})
		}
	}
}

// checkStub surfaces stray vault-root markdown files that look like
// Obsidian-created placeholders. When the user clicks a wikilink to a
// page that doesn't exist (e.g. `[[Anthropic]]` inside a clipping
// descriptor), Obsidian writes an empty `.md` at the vault root with
// that name. These pile up over time and clutter the navigation.
//
// Detection (all must hold):
//   - file lives at vault root (no directory separator in relative path)
//   - filename is not "index.md" (auto-generated homepage)
//   - body is empty after stripping frontmatter and trimming whitespace
//   - mtime is older than `now - Stub.MinIdleSeconds`
//
// The mtime cooldown is the load-bearing safety: if you're actively
// filling in a stub, every keystroke bumps mtime, so the check ignores
// the file until you've been quiet for at least the configured window.
// Default 1 hour, override via meta/lint-config.yaml.
//
// Action is delegated to the maintenance audit agent (LLM judgment in
// the loop) — this check only emits WARNs.
func (s *LintService) checkStub(report *LintReport) {
	// Surface a config parse error up-front: if the file exists but
	// failed to load, the check is running on default cooldown and the
	// user almost certainly wants to know — silently honoring defaults
	// would make a misconfigured min_idle_seconds invisible.
	if s.configErr != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "stub", Level: "ERROR",
			Message: fmt.Sprintf("using default config: %v", s.configErr),
		})
	}

	pages, err := s.vault.FindWikiPages()
	if err != nil {
		report.Issues = append(report.Issues, LintIssue{
			Check: "stub", Level: "ERROR", Message: err.Error(),
		})
		return
	}

	idle := time.Duration(s.config.Stub.MinIdleSeconds) * time.Second
	cutoff := time.Now().Add(-idle)

	for _, page := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, page)
		rel = filepath.ToSlash(rel)

		// Vault root only.
		if strings.ContainsRune(rel, '/') {
			continue
		}
		// Skip the auto-generated homepage.
		if rel == "index.md" {
			continue
		}

		info, err := os.Stat(page)
		if err != nil {
			continue
		}
		// Active-editing cooldown.
		if info.ModTime().After(cutoff) {
			continue
		}

		data, err := os.ReadFile(page)
		if err != nil {
			continue
		}
		body := strings.TrimSpace(stripFrontmatter(string(data)))
		if body != "" {
			continue
		}

		report.Issues = append(report.Issues, LintIssue{
			File: rel, Check: "stub", Level: "WARN",
			Message: fmt.Sprintf("vault-root file is empty and idle ≥ %ds — looks like an Obsidian stub from a clicked broken wikilink", s.config.Stub.MinIdleSeconds),
		})
	}
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

		content = stripFrontmatter(content)
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
