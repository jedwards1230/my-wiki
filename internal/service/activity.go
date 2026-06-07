package service

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

// ValidActivityTypes lists the allowed activity types.
var ValidActivityTypes = []string{"edit", "create", "delete", "lint", "note", "migrate", "move"}

// ActivityService provides activity logging operations.
type ActivityService struct {
	storage vault.Storage
}

// NewActivityService creates an ActivityService backed by the given storage.
func NewActivityService(storage vault.Storage) *ActivityService {
	return &ActivityService{storage: storage}
}

func (s *ActivityService) activityDir() string {
	return filepath.Join("meta", "activity")
}

func (s *ActivityService) logIndexPath() string {
	return filepath.Join("meta", "log.md")
}

// Append adds an activity entry to today's log file and updates the log index.
func (s *ActivityService) Append(entry ActivityEntry) error {
	// Validate type
	valid := false
	for _, t := range ValidActivityTypes {
		if entry.Type == t {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid type %q: must be one of %s", entry.Type, strings.Join(ValidActivityTypes, ", "))
	}

	// Sanitize
	entry.Title = Sanitize(entry.Title)
	if entry.Title == "" {
		return fmt.Errorf("title cannot be empty after sanitization")
	}
	entry.Summary = SanitizeBody(entry.Summary)

	if entry.Time == "" {
		entry.Time = time.Now().Format("15:04")
	}

	// Validate time format
	if _, err := time.Parse("15:04", entry.Time); err != nil {
		if _, err := time.Parse("3:04", entry.Time); err != nil {
			return fmt.Errorf("invalid time format %q: use HH:MM", entry.Time)
		}
	}

	activityDir := s.activityDir()
	today := time.Now().Format("2006-01-02")
	dailyRelPath := filepath.Join(activityDir, today+".md")

	if err := s.storage.MkdirAll(activityDir, 0o755); err != nil {
		return err
	}

	if _, err := s.storage.Stat(dailyRelPath); os.IsNotExist(err) {
		content := fmt.Sprintf("---\ntitle: \"%s\"\ntags:\n  - meta/activity\ndate: %s\n---\n", today, today)
		if err := s.storage.WriteFile(dailyRelPath, []byte(content), 0o644); err != nil {
			return err
		}
	}

	// Build entry line
	entryLine := fmt.Sprintf("### %s | %s | %s", entry.Time, entry.Type, entry.Title)

	// Append entry
	f, err := s.storage.OpenFile(dailyRelPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintln(f)
	_, _ = fmt.Fprintln(f, entryLine)

	// Auto-logged entries are compact audit lines — no description body.
	if !entry.AutoLogged {
		desc := BuildDescription(entry.Summary, entry.Touched)
		if desc != "" {
			_, _ = fmt.Fprintln(f, desc)
		}
	}

	// Close the day file before re-reading it (a day_summary update and the
	// log-index hash both read the file back).
	_ = f.Close()

	// Tier 1: an explicit day summary is persisted to the day file's
	// frontmatter so it survives subsequent appends and is editable later by
	// the maintenance pass. updateLogIndex reads it back as the index line.
	if ds := Sanitize(entry.DaySummary); ds != "" {
		data, err := s.storage.ReadFile(dailyRelPath)
		if err != nil {
			return err
		}
		updated := setFrontmatterSummary(string(data), ds)
		if err := s.storage.WriteFile(dailyRelPath, []byte(updated), 0o644); err != nil {
			return err
		}
	}

	return s.updateLogIndex(dailyRelPath, today)
}

// Sanitize removes pipe and backtick characters and normalizes whitespace.
// Used for the H3 header title, which is pipe-delimited (`### time | type | title`)
// — a raw '|' there would corrupt the record structure.
func Sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '|' || r == '`' {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// SanitizeBody normalizes whitespace in an activity description body while
// preserving pipe and backtick characters. Unlike Sanitize (used for the
// pipe-delimited header), the body is free markdown: stripping '|' corrupts
// aliased wikilinks ([[path|Display]]) and stripping '`' breaks code spans.
// Collapsing whitespace still prevents a newline from injecting a fake header.
func SanitizeBody(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// BuildDescription builds a description string from summary and touched pages.
func BuildDescription(summary string, touched []string) string {
	var desc string

	if summary != "" {
		desc = summary
	} else if len(touched) > 0 {
		desc = fmt.Sprintf("Updated %d page(s).", len(touched))
	}

	if len(touched) > 0 {
		var links []string
		for _, page := range touched {
			page = strings.TrimSuffix(page, ".md")
			links = append(links, fmt.Sprintf("[[%s]]", page))
		}
		linkStr := strings.Join(links, ", ")
		if desc != "" {
			desc += " Touched: " + linkStr + "."
		} else {
			desc = "Touched: " + linkStr + "."
		}
	}

	return desc
}

func (s *ActivityService) updateLogIndex(dailyRelPath, today string) error {
	logIndex := s.logIndexPath()

	data, err := s.storage.ReadFile(dailyRelPath)
	if err != nil {
		return err
	}
	content := string(data)

	entryCount := 0
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "### ") {
			entryCount++
		}
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(data))[:6]

	// Summary precedence: an explicit frontmatter summary (tier 1) wins;
	// otherwise compute a digest from the day's entries (tier 2). Sanitize so
	// the chosen summary can never inject the " | " column delimiter.
	summary := Sanitize(DaySummary(content))
	if summary == "" {
		summary = fmt.Sprintf("%d change(s)", entryCount)
	}

	indexLine := fmt.Sprintf("## [[meta/activity/%s|%s]] %d changes | `%s` | %s", today, today, entryCount, hash, summary)

	// Ensure log index exists
	if _, err := s.storage.Stat(logIndex); os.IsNotExist(err) {
		if err := s.storage.MkdirAll(filepath.Dir(logIndex), 0o755); err != nil {
			return err
		}
		if err := s.storage.WriteFile(logIndex, []byte(""), 0o644); err != nil {
			return err
		}
	}

	existing, err := s.storage.ReadFile(logIndex)
	if err != nil {
		return err
	}

	todayPrefix := fmt.Sprintf("## [[meta/activity/%s|%s]]", today, today)
	lines := strings.Split(string(existing), "\n")
	found := false
	for i, line := range lines {
		if strings.HasPrefix(line, todayPrefix) {
			lines[i] = indexLine
			found = true
			break
		}
	}

	if found {
		return s.storage.WriteFile(logIndex, []byte(strings.Join(lines, "\n")), 0o644)
	}

	// Append
	f, err := s.storage.OpenFile(logIndex, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(f, indexLine)
	_ = f.Close()

	return nil
}

// activityTypeOrder is the conventional display order for type counts in a
// computed day digest (mutations first, then narrative kinds).
var activityTypeOrder = []string{"create", "edit", "delete", "move", "note", "lint", "migrate"}

var activityWikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// DaySummary returns the index-line summary for a day file's content: the
// explicit frontmatter `summary:` if present (tier 1), else a computed digest
// of the day's entries (tier 2). Returns "" only when neither is available.
func DaySummary(content string) string {
	if fm, err := vault.ParseFrontmatterString(content); err == nil && fm != nil {
		if s := strings.TrimSpace(fm["summary"]); s != "" {
			return s
		}
	}
	return computeDaySummary(content)
}

// computeDaySummary builds a deterministic whole-day digest from the day file:
// type counts plus the most-touched directories, e.g.
// "14 creates, 12 edits, 5 notes · research/neuroscience, research/clippings".
func computeDaySummary(content string) string {
	typeCounts := map[string]int{}
	dirCounts := map[string]int{}

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "### ") {
			parts := strings.SplitN(strings.TrimPrefix(line, "### "), " | ", 3)
			if len(parts) >= 2 {
				typeCounts[strings.TrimSpace(parts[1])]++
			}
		}
		for _, m := range activityWikilinkRe.FindAllStringSubmatch(line, -1) {
			target := m[1]
			if i := strings.IndexByte(target, '|'); i >= 0 {
				target = target[:i]
			}
			target = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(target), ".md"))
			i := strings.LastIndexByte(target, '/')
			if i < 0 {
				continue // root-level page, no directory to attribute
			}
			dir := target[:i]
			if dir == "" || strings.HasPrefix(dir, "meta/activity") {
				continue
			}
			dirCounts[dir]++
		}
	}

	typeStr := formatTypeCounts(typeCounts)
	dirStr := topDirs(dirCounts, 3)
	switch {
	case typeStr != "" && dirStr != "":
		return typeStr + " · " + dirStr
	case typeStr != "":
		return typeStr
	case dirStr != "":
		return dirStr
	default:
		return ""
	}
}

// formatTypeCounts renders type tallies in conventional order, pluralized:
// "14 creates, 1 edit, 5 notes".
func formatTypeCounts(counts map[string]int) string {
	var parts []string
	for _, t := range activityTypeOrder {
		n := counts[t]
		if n == 0 {
			continue
		}
		word := t
		if n != 1 {
			word += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", n, word))
	}
	return strings.Join(parts, ", ")
}

// topDirs returns the up-to-max most-referenced directories, highest first,
// ties broken alphabetically.
func topDirs(counts map[string]int, max int) string {
	type dirCount struct {
		dir string
		n   int
	}
	ranked := make([]dirCount, 0, len(counts))
	for d, n := range counts {
		ranked = append(ranked, dirCount{d, n})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].n != ranked[j].n {
			return ranked[i].n > ranked[j].n
		}
		return ranked[i].dir < ranked[j].dir
	})
	if len(ranked) > max {
		ranked = ranked[:max]
	}
	dirs := make([]string, len(ranked))
	for i, dc := range ranked {
		dirs[i] = dc.dir
	}
	return strings.Join(dirs, ", ")
}

// setFrontmatterSummary returns content with the frontmatter `summary:` field
// set to summary, replacing any existing top-level summary line. The value is
// written as a double-quoted YAML scalar so colons and other punctuation stay
// valid. If content has no frontmatter block, it is returned unchanged.
func setFrontmatterSummary(content, summary string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return content
	}

	var fm []string
	for i := 1; i < end; i++ {
		l := lines[i]
		// Drop any existing top-level summary key (leave indented list items
		// belonging to other keys untouched).
		if !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") &&
			strings.HasPrefix(strings.TrimSpace(l), "summary:") {
			continue
		}
		fm = append(fm, l)
	}
	fm = append(fm, fmt.Sprintf("summary: %q", summary))

	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[0])
	out = append(out, fm...)
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n")
}
