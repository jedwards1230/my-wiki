package cli

import (
	"fmt"
	"time"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

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

	actType := args[0]
	title := args[1]
	touched, _ := cmd.Flags().GetStringSlice("touched")
	summary, _ := cmd.Flags().GetString("summary")
	timeStr, _ := cmd.Flags().GetString("time")

	if timeStr == "" {
		timeStr = time.Now().Format("15:04")
	}

	svc := service.NewActivityService(vault.NewFilesystemStorage(vaultDir))

	entry := service.ActivityEntry{
		Type:    actType,
		Title:   title,
		Time:    timeStr,
		Summary: summary,
		Touched: touched,
	}

	if err := svc.Append(entry); err != nil {
		return err
	}

	fmt.Printf("Logged: %s | %s | %s\n", timeStr, actType, service.Sanitize(title))

	today := time.Now().Format("2006-01-02")
	fmt.Printf("Updated meta/log.md (activity/%s)\n", today)

	return nil
}

// Keep these exported for test compatibility
func sanitize(s string) string {
	return service.Sanitize(s)
}

func buildDescription(summary string, touched []string) string {
	return service.BuildDescription(summary, touched)
}
