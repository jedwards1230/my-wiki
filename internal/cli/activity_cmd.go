package cli

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var validActivityTypes = []string{"ingest", "edit", "create", "lint", "note", "migrate"}

func newActivityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "activity <type> <title>",
		Short: "Append a structured entry to today's activity log",
		Long: `Append a structured entry to today's activity log.

Types: ingest, edit, create, lint, note, migrate`,
		Args: cobra.MinimumNArgs(2),
		RunE: runActivity,
	}

	cmd.Flags().StringSlice("touched", nil, "pages created or edited (auto-linked as [[wikilinks]])")
	cmd.Flags().String("summary", "", "description of what was done")
	cmd.Flags().String("time", "", "override timestamp (HH:MM format, default: current time)")

	return cmd
}

func runActivity(cmd *cobra.Command, args []string) error {
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	activityDir := filepath.Join(vaultDir, "meta", "activity")
	logIndex := filepath.Join(vaultDir, "meta", "log.md")

	actType := args[0]
	title := args[1]

	// Validate type
	valid := false
	for _, t := range validActivityTypes {
		if actType == t {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("invalid type %q: must be one of %s", actType, strings.Join(validActivityTypes, ", "))
	}

	// Sanitize title
	title = sanitize(title)
	if title == "" {
		return fmt.Errorf("title cannot be empty after sanitization")
	}

	touched, _ := cmd.Flags().GetStringSlice("touched")
	summary, _ := cmd.Flags().GetString("summary")
	timeStr, _ := cmd.Flags().GetString("time")

	if timeStr == "" {
		timeStr = time.Now().Format("15:04")
	}

	// Validate time format
	if _, err := time.Parse("15:04", timeStr); err != nil {
		// Also accept single-digit hour like 9:30
		if _, err := time.Parse("3:04", timeStr); err != nil {
			return fmt.Errorf("invalid time format %q: use HH:MM", timeStr)
		}
	}

	// Sanitize summary
	summary = sanitize(summary)

	today := time.Now().Format("2006-01-02")
	dailyFile := filepath.Join(activityDir, today+".md")

	// Create daily file if needed
	if err := os.MkdirAll(activityDir, 0o755); err != nil {
		return err
	}

	created := false
	if _, err := os.Stat(dailyFile); os.IsNotExist(err) {
		content := fmt.Sprintf("---\ntitle: \"%s\"\ntags:\n  - meta/activity\ndate: %s\n---\n", today, today)
		if err := os.WriteFile(dailyFile, []byte(content), 0o644); err != nil {
			return err
		}
		created = true
		fmt.Printf("Created %s\n", dailyFile)
	}

	// Build entry
	entry := fmt.Sprintf("### %s | %s | %s", timeStr, actType, title)

	// Build description
	desc := buildDescription(summary, touched)

	// Append entry
	f, err := os.OpenFile(dailyFile, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, _ = fmt.Fprintln(f)
	_, _ = fmt.Fprintln(f, entry)
	if desc != "" {
		_, _ = fmt.Fprintln(f, desc)
	}

	_ = created // suppress unused warning
	fmt.Printf("Logged: %s | %s | %s\n", timeStr, actType, title)

	// Update log index
	return updateLogIndex(logIndex, dailyFile, today, title)
}

func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '|' || r == '`' {
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	return s
}

func buildDescription(summary string, touched []string) string {
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

func updateLogIndex(logIndex, dailyFile, today, title string) error {
	// Count entries
	data, err := os.ReadFile(dailyFile)
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

	// Compute hash
	hash := fmt.Sprintf("%x", md5.Sum(data))[:6]

	indexLine := fmt.Sprintf("## [%s] %d changes | `%s` | %s | [[meta/activity/%s]]", today, entryCount, hash, title, today)

	// Ensure log index exists
	if _, err := os.Stat(logIndex); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(logIndex), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(logIndex, []byte(""), 0o644); err != nil {
			return err
		}
	}

	// Read existing log
	existing, err := os.ReadFile(logIndex)
	if err != nil {
		return err
	}

	todayPrefix := fmt.Sprintf("## [%s]", today)
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
		if err := os.WriteFile(logIndex, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return err
		}
	} else {
		// Append
		f, err := os.OpenFile(logIndex, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(f, indexLine)
		_ = f.Close()
	}

	fmt.Printf("Updated meta/log.md (%d entries, hash: %s)\n", entryCount, hash)
	return nil
}
