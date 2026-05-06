package cli

import (
	"fmt"
	"regexp"
	"time"

	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/jedwards1230/my-wiki/internal/vault"
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
	n, _ := cmd.Flags().GetInt("number")
	detail, _ := cmd.Flags().GetBool("detail")

	logSvc := service.NewLogService(vault.NewFilesystemStorage(vaultDir))

	// If -n is set with no args, show last N
	if n > 0 && len(args) == 0 {
		return printLogIndex(logSvc, n)
	}

	if len(args) == 0 {
		return printLogIndex(logSvc, 0)
	}

	switch args[0] {
	case "lint":
		return printLogLint(logSvc)
	case "today":
		today := time.Now().Format("2006-01-02")
		return printLogDay(logSvc, today, detail)
	default:
		if dateRe.MatchString(args[0]) {
			return printLogDay(logSvc, args[0], detail)
		}
		return fmt.Errorf("unknown argument %q: expected today, YYYY-MM-DD, or lint", args[0])
	}
}

func printLogIndex(svc *service.LogService, n int) error {
	entries, err := svc.Index(n)
	if err != nil {
		return err
	}

	for _, e := range entries {
		fmt.Printf("## [%s] %d changes | %s | %s\n", e.Date, e.Changes, e.Title, e.ActivityRef)
	}
	return nil
}

func printLogDay(svc *service.LogService, date string, detail bool) error {
	dayLog, err := svc.Day(date, detail)
	if err != nil {
		return err
	}

	for _, e := range dayLog.Entries {
		fmt.Printf("### %s | %s | %s\n", e.Time, e.Type, e.Title)
		if detail && e.Summary != "" {
			fmt.Println(e.Summary)
			fmt.Println()
		}
	}
	return nil
}

func printLogLint(svc *service.LogService) error {
	fmt.Println("=== Activity Log Lint ===")
	fmt.Println()

	issues, err := svc.Lint()
	if err != nil {
		return err
	}

	for _, issue := range issues {
		fmt.Printf("WARN: %s\n", issue.Message)
	}

	fmt.Println()
	if len(issues) == 0 {
		fmt.Println("OK: All checks passed")
	} else {
		fmt.Printf("FOUND: %d issue(s)\n", len(issues))
		return fmt.Errorf("%d issue(s) found", len(issues))
	}
	return nil
}
