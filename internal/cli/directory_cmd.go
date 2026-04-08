package cli

import (
	"fmt"

	"github.com/jedwards1230/home-wiki/internal/service"
	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newDirectoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "directory",
		Short: "List all wiki pages with metadata",
		Long:  "Show all wiki pages grouped by tag, with titles and descriptions.",
		RunE:  runDirectory,
	}

	cmd.Flags().Bool("count", false, "just print the count")
	cmd.Flags().Bool("generate", false, "regenerate meta/directory.md")

	return cmd
}

func runDirectory(cmd *cobra.Command, _ []string) error {
	vaultDir, _ := cmd.Root().Flags().GetString("vault")
	v := vault.New(vaultDir)

	countOnly, _ := cmd.Flags().GetBool("count")
	generate, _ := cmd.Flags().GetBool("generate")

	svc := service.NewDirectoryService(v)

	switch {
	case countOnly:
		entries, err := svc.List()
		if err != nil {
			return err
		}
		fmt.Printf("%d wiki page(s)\n", len(entries))
	case generate:
		path, count, err := svc.Generate()
		if err != nil {
			return err
		}
		fmt.Printf("Generated %s (%d page(s))\n", path, count)
	default:
		entries, err := svc.List()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No wiki pages found.")
			return nil
		}
		for _, e := range entries {
			desc := e.Description
			if desc == "" {
				desc = "—"
			}
			fmt.Printf("%-50s  %-30s  %s\n", e.Path, e.Title, desc)
		}
	}

	return nil
}
