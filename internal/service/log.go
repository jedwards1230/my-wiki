package service

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

// LogService provides activity log operations.
type LogService struct {
	storage vault.Storage
}

// NewLogService creates a LogService backed by the given storage.
func NewLogService(storage vault.Storage) *LogService {
	return &LogService{storage: storage}
}

var (
	logIndexDateRe    = regexp.MustCompile(`[|\[](\d{4}-\d{2}-\d{2})\]`)
	logIndexHashRe    = regexp.MustCompile("(?:^|[^a-f0-9])([a-f0-9]{6})(?:[^a-f0-9]|$)")
	logIndexChangesRe = regexp.MustCompile(`(\d+) changes?`)
	logIndexRefRe     = regexp.MustCompile(`\[\[([^|\]]+)`)
)

// Index returns the last n entries from the log index. If n <= 0, all entries.
func (s *LogService) Index(n int) ([]LogEntry, error) {
	f, err := s.storage.OpenFile(metaLogIndexPath(), os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("no log index found: %w", err)
	}
	defer func() { _ = f.Close() }()

	var entries []LogEntry
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "## [") {
			continue
		}

		entry := parseLogIndexLine(line)
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if n > 0 && n < len(entries) {
		entries = entries[len(entries)-n:]
	}

	return entries, nil
}

func parseLogIndexLine(line string) LogEntry {
	// Format: ## [[meta/activity/2026-04-06|2026-04-06]] 3 changes | `abcdef` | Last edit
	entry := LogEntry{}

	if m := logIndexDateRe.FindStringSubmatch(line); m != nil {
		entry.Date = m[1]
	}
	if m := logIndexHashRe.FindStringSubmatch(line); m != nil {
		entry.Hash = m[1]
	}
	if m := logIndexChangesRe.FindStringSubmatch(line); m != nil {
		entry.Changes, _ = strconv.Atoi(m[1])
	}

	// Extract title: last pipe-separated segment
	// Pattern: | `hash` | TITLE
	parts := strings.Split(line, " | ")
	if len(parts) >= 2 {
		entry.Title = strings.TrimSpace(parts[len(parts)-1])
	}

	if m := logIndexRefRe.FindStringSubmatch(line); m != nil {
		entry.ActivityRef = m[1]
	}

	return entry
}

// validDate checks that date is a valid YYYY-MM-DD string with no path components.
func validDate(date string) bool {
	if len(date) != 10 {
		return false
	}
	for _, c := range date {
		if c != '-' && (c < '0' || c > '9') {
			return false
		}
	}
	return date[4] == '-' && date[7] == '-'
}

// Day returns activity entries for a specific date.
// If detail is false, only headers (### lines) are returned.
func (s *LogService) Day(date string, detail bool) (*DayLog, error) {
	if !validDate(date) {
		return nil, fmt.Errorf("invalid date format: %s (expected YYYY-MM-DD)", date)
	}
	fileRelPath := filepath.Join(metaActivityDir(), date+".md")
	if _, err := s.storage.Stat(fileRelPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("no activity file for %s", date)
	}

	f, err := s.storage.OpenFile(fileRelPath, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	dayLog := &DayLog{Date: date}
	var current *ActivityEntry

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "### ") {
			if current != nil {
				dayLog.Entries = append(dayLog.Entries, *current)
			}
			current = parseActivityHeader(line)
		} else if detail && current != nil {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				if current.Summary != "" {
					current.Summary += "\n"
				}
				current.Summary += trimmed
			}
		}
	}
	if current != nil {
		dayLog.Entries = append(dayLog.Entries, *current)
	}

	return dayLog, scanner.Err()
}

func parseActivityHeader(line string) *ActivityEntry {
	// Format: ### HH:MM | type | title
	line = strings.TrimPrefix(line, "### ")
	parts := strings.SplitN(line, " | ", 3)
	entry := &ActivityEntry{}
	if len(parts) >= 1 {
		entry.Time = strings.TrimSpace(parts[0])
	}
	if len(parts) >= 2 {
		entry.Type = strings.TrimSpace(parts[1])
	}
	if len(parts) >= 3 {
		entry.Title = strings.TrimSpace(parts[2])
	}
	return entry
}

// LogLintIssue represents an issue found during log lint.
type LogLintIssue struct {
	Message string `json:"message"`
}

// Lint checks the activity log for issues.
func (s *LogService) Lint() ([]LogLintIssue, error) {
	logIndex := metaLogIndexPath()
	activityDir := metaActivityDir()

	if _, err := s.storage.Stat(logIndex); os.IsNotExist(err) {
		return nil, fmt.Errorf("log index missing at %s", logIndex)
	}

	var issues []LogLintIssue

	indexContent, err := s.storage.ReadFile(logIndex)
	if err != nil {
		return nil, err
	}
	indexStr := string(indexContent)

	// Check activity files have matching index entries
	if entries, err := s.storage.ReadDir(activityDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			date := strings.TrimSuffix(entry.Name(), ".md")
			if !strings.Contains(indexStr, date) {
				issues = append(issues, LogLintIssue{
					Message: fmt.Sprintf("%s has activity file but no index entry", date),
				})
			}
		}
	}

	// Check hash mismatches
	f, err := s.storage.OpenFile(logIndex, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "## [") {
			continue
		}

		dateMatch := logIndexDateRe.FindStringSubmatch(line)
		hashMatch := logIndexHashRe.FindStringSubmatch(line)
		if dateMatch == nil || hashMatch == nil {
			continue
		}

		date := dateMatch[1]
		storedHash := hashMatch[1]

		actFileRelPath := filepath.Join(activityDir, date+".md")
		data, err := s.storage.ReadFile(actFileRelPath)
		if err != nil {
			if os.IsNotExist(err) {
				issues = append(issues, LogLintIssue{
					Message: fmt.Sprintf("Index references %s but no activity file exists", date),
				})
			}
			continue
		}

		actualHash := shortHash(data)
		if storedHash != actualHash {
			issues = append(issues, LogLintIssue{
				Message: fmt.Sprintf("Hash mismatch for %s (index: %s, actual: %s)", date, storedHash, actualHash),
			})
		}
	}

	// Check for activity files without frontmatter
	if entries, err := s.storage.ReadDir(activityDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			fileRelPath := filepath.Join(activityDir, entry.Name())
			data, err := s.storage.ReadFile(fileRelPath)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(string(data), "---") {
				issues = append(issues, LogLintIssue{
					Message: fmt.Sprintf("%s missing frontmatter", entry.Name()),
				})
			}
		}
	}

	return issues, nil
}
