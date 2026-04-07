package cli

import (
	"bufio"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newLogCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "log [today|YYYY-MM-DD|lint]",
		Short: "View and lint the wiki activity log",
		Long:  "Show the activity log index, a specific day's activity, or lint for issues.",
		Args:  cobra.MaximumNArgs(2),
		RunE:  runLog,
	}

	cmd.Flags().IntP("number", "n", 0, "show last N entries from the log index")
	cmd.Flags().Bool("detail", false, "show full content for a day")

	return cmd
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func runLog(cmd *cobra.Command, args []string) error {
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	logIndex := filepath.Join(vaultDir, "meta", "log.md")
	activityDir := filepath.Join(vaultDir, "meta", "activity")

	n, _ := cmd.Flags().GetInt("number")
	detail, _ := cmd.Flags().GetBool("detail")

	// If -n is set with no args, show last N
	if n > 0 && len(args) == 0 {
		return showIndex(logIndex, n)
	}

	if len(args) == 0 {
		return showIndex(logIndex, 0)
	}

	switch args[0] {
	case "lint":
		return lintLog(logIndex, activityDir)
	case "today":
		today := time.Now().Format("2006-01-02")
		return showDay(activityDir, today, detail)
	default:
		if dateRe.MatchString(args[0]) {
			// Check if second arg is --detail (positional)
			if len(args) > 1 && args[1] == "--detail" {
				detail = true
			}
			return showDay(activityDir, args[0], detail)
		}
		return fmt.Errorf("unknown argument %q: expected today, YYYY-MM-DD, or lint", args[0])
	}
}

func showIndex(logIndex string, n int) error {
	f, err := os.Open(logIndex)
	if err != nil {
		return fmt.Errorf("no log index found at %s", logIndex)
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "## [") {
			lines = append(lines, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	if n > 0 && n < len(lines) {
		lines = lines[len(lines)-n:]
	}

	for _, l := range lines {
		fmt.Println(l)
	}
	return nil
}

func showDay(activityDir, date string, detail bool) error {
	file := filepath.Join(activityDir, date+".md")
	if _, err := os.Stat(file); os.IsNotExist(err) {
		return fmt.Errorf("no activity file for %s", date)
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if detail {
			fmt.Println(line)
		} else if strings.HasPrefix(line, "### ") {
			fmt.Println(line)
		}
	}
	return scanner.Err()
}

var (
	indexDateRe = regexp.MustCompile(`\[(\d{4}-\d{2}-\d{2})\]`)
	indexHashRe = regexp.MustCompile("`([a-f0-9]{6})`")
)

func lintLog(logIndex, activityDir string) error {
	fmt.Println("=== Activity Log Lint ===")
	fmt.Println()

	errors := 0

	if _, err := os.Stat(logIndex); os.IsNotExist(err) {
		fmt.Println("FAIL: Log index missing at", logIndex)
		return fmt.Errorf("log index missing")
	}

	// Check each activity file has a matching index entry
	indexContent, err := os.ReadFile(logIndex)
	if err != nil {
		return err
	}
	indexStr := string(indexContent)

	if entries, err := os.ReadDir(activityDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			date := strings.TrimSuffix(entry.Name(), ".md")
			if !strings.Contains(indexStr, "["+date+"]") {
				fmt.Printf("WARN: %s has activity file but no index entry\n", date)
				errors++
			}
		}
	}

	// Check hash mismatches
	f, err := os.Open(logIndex)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "## [") {
			continue
		}

		dateMatch := indexDateRe.FindStringSubmatch(line)
		hashMatch := indexHashRe.FindStringSubmatch(line)
		if dateMatch == nil || hashMatch == nil {
			continue
		}

		date := dateMatch[1]
		storedHash := hashMatch[1]

		actFile := filepath.Join(activityDir, date+".md")
		data, err := os.ReadFile(actFile)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("WARN: Index references %s but no activity file exists\n", date)
				errors++
			}
			continue
		}

		actualHash := fmt.Sprintf("%x", md5.Sum(data))[:6]
		if storedHash != actualHash {
			fmt.Printf("WARN: Hash mismatch for %s (index: %s, actual: %s)\n", date, storedHash, actualHash)
			errors++
		}
	}

	// Check for activity files without frontmatter
	if entries, err := os.ReadDir(activityDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			file := filepath.Join(activityDir, entry.Name())
			data, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			if !strings.HasPrefix(string(data), "---") {
				fmt.Printf("WARN: %s missing frontmatter\n", entry.Name())
				errors++
			}
		}
	}

	fmt.Println()
	if errors == 0 {
		fmt.Println("OK: All checks passed")
	} else {
		fmt.Printf("FOUND: %d issue(s)\n", errors)
		return fmt.Errorf("%d issue(s) found", errors)
	}
	return nil
}

