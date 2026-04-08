package cli

import (
	"fmt"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newIngestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ingest",
		Short: "List unprocessed raw sources",
		Long:  "Show raw files missing the 'ingested' frontmatter field.",
		RunE:  runIngest,
	}

	cmd.Flags().Bool("count", false, "just print the count")
	cmd.Flags().Bool("generate", false, "regenerate meta/ingest-queue.md")

	return cmd
}

func runIngest(cmd *cobra.Command, _ []string) error {
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	v := vault.New(vaultDir)

	countOnly, _ := cmd.Flags().GetBool("count")
	generate, _ := cmd.Flags().GetBool("generate")

	svc := service.NewIngestService(v)

	switch {
	case countOnly:
		items, err := svc.List()
		if err != nil {
			return err
		}
		fmt.Printf("%d unprocessed raw source(s)\n", len(items))
	case generate:
		path, count, err := svc.Generate()
		if err != nil {
			return err
		}
		fmt.Printf("Generated %s (%d item(s))\n", path, count)
	default:
		items, err := svc.List()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Println("All raw sources have been ingested.")
			return nil
		}
		for _, item := range items {
			dateStr := item.DateAdded
			if dateStr == "" {
				dateStr = "--"
			}
			fmt.Printf("%-50s  %s  %s\n", item.Path, dateStr, item.Title)
		}
	}

	return nil
}
