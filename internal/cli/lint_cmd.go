package cli

import (
	"fmt"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newLintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint [all|frontmatter|raw|tags|links|orphans|size|log]",
		Short: "Run mechanical health checks on the wiki vault",
		Long:  "Check frontmatter, tags, broken wikilinks, orphan pages, page size, raw source compliance, and activity log integrity.",
		Args:  cobra.MaximumNArgs(1),
		RunE:  runLint,
	}
	return cmd
}

func runLint(cmd *cobra.Command, args []string) error {
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	v := vault.New(vaultDir)

	check := "all"
	if len(args) > 0 {
		check = args[0]
	}

	svc := service.NewLintService(v, nil)
	report, err := svc.Run(check)
	if err != nil {
		return err
	}

	// Format output for terminal
	if check == "all" {
		printLintSection("Frontmatter Check", report, "frontmatter")
		printLintSection("Raw Source Frontmatter Check", report, "raw")
		printLintSection("Tag Structure", report, "tags")
		printLintSection("Broken Wikilinks", report, "links")
		printLintSection("Orphan Pages (no inbound links)", report, "orphans")
		printLintSection("Page Size", report, "size")
		printLintSection("Activity Log", report, "log")
		fmt.Println("=== Summary ===")
		if report.Total == 0 {
			fmt.Println("All checks passed.")
		} else {
			fmt.Printf("%d issue(s) found", report.Total)
			if report.Errors < report.Total {
				fmt.Printf(" (%d errors, %d info)", report.Errors, report.Total-report.Errors)
			}
			fmt.Println(".")
		}
	} else {
		printLintSection(check, report, check)
	}

	// Only fail on actual errors (FAIL/WARN/ERROR), not INFO-level findings.
	if report.Errors > 0 {
		cmd.SilenceErrors = true
		return fmt.Errorf("%d issue(s) found", report.Errors)
	}
	return nil
}

func printLintSection(title string, report *service.LintReport, check string) {
	fmt.Printf("=== %s ===\n", title)
	count := 0
	for _, issue := range report.Issues {
		if issue.Check == check {
			if issue.File != "" {
				fmt.Printf("  %s: %s — %s\n", issue.Level, issue.File, issue.Message)
			} else {
				fmt.Printf("  %s: %s\n", issue.Level, issue.Message)
			}
			count++
		}
	}
	if count == 0 {
		fmt.Println("  OK")
	}
	fmt.Println()
}
