package service

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jedwards1230/home-wiki/internal/vault"
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
	entry.Summary = Sanitize(entry.Summary)

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
	defer func() { _ = f.Close() }()

	_, _ = fmt.Fprintln(f)
	_, _ = fmt.Fprintln(f, entryLine)

	// Auto-logged entries are compact audit lines — no description body.
	if !entry.AutoLogged {
		desc := BuildDescription(entry.Summary, entry.Touched)
		if desc != "" {
			_, _ = fmt.Fprintln(f, desc)
		}
	}

	return s.updateLogIndex(dailyRelPath, today, entry.Title)
}

// Sanitize removes pipe and backtick characters and normalizes whitespace.
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

func (s *ActivityService) updateLogIndex(dailyRelPath, today, title string) error {
	logIndex := s.logIndexPath()

	data, err := s.storage.ReadFile(dailyRelPath)
	if err != nil {
		return err
	}

	entryCount := 0
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "### ") {
			entryCount++
		}
	}

	hash := fmt.Sprintf("%x", sha256.Sum256(data))[:6]
	indexLine := fmt.Sprintf("## [[meta/activity/%s|%s]] %d changes | `%s` | %s", today, today, entryCount, hash, title)

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
