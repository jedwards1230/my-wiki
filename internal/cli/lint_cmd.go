package cli

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
	"github.com/spf13/cobra"
)

func newLintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint [all|frontmatter|raw|links|orphans]",
		Short: "Run mechanical health checks on the wiki vault",
		Long:  "Check frontmatter, broken wikilinks, orphan pages, and raw source compliance.",
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

	var errors int

	switch check {
	case "all":
		errors += checkFrontmatter(v)
		errors += checkRawFrontmatter(v)
		errors += checkLinks(v)
		errors += checkOrphans(v)
		fmt.Println("=== Summary ===")
		if errors == 0 {
			fmt.Println("All checks passed.")
		} else {
			fmt.Printf("%d issue(s) found.\n", errors)
		}
	case "frontmatter":
		errors += checkFrontmatter(v)
	case "raw":
		errors += checkRawFrontmatter(v)
	case "links":
		errors += checkLinks(v)
	case "orphans":
		errors += checkOrphans(v)
	default:
		return fmt.Errorf("unknown check %q: must be all, frontmatter, raw, links, or orphans", check)
	}

	if errors > 0 {
		// Return a non-nil error so cobra sets exit code 1
		cmd.SilenceErrors = true
		return fmt.Errorf("%d issue(s) found", errors)
	}
	return nil
}

func checkFrontmatter(v *vault.Vault) int {
	fmt.Println("=== Frontmatter Check ===")
	pages, err := v.FindWikiPages()
	if err != nil {
		fmt.Printf("  ERROR: %v\n\n", err)
		return 1
	}

	count := 0
	for _, page := range pages {
		rel, _ := filepath.Rel(v.Dir, page)
		fm, err := vault.ParseFrontmatter(page)
		if err != nil {
			fmt.Printf("  FAIL: %s — %v\n", rel, err)
			count++
			continue
		}
		if fm == nil {
			fmt.Printf("  FAIL: %s — missing frontmatter\n", rel)
			count++
			continue
		}

		var missing []string
		for _, key := range []string{"title", "tags", "date"} {
			if _, ok := fm[key]; !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("  WARN: %s — missing: %s\n", rel, strings.Join(missing, " "))
			count++
		}
	}

	if count == 0 {
		fmt.Println("  OK")
	}
	fmt.Println()
	return count
}

func checkRawFrontmatter(v *vault.Vault) int {
	fmt.Println("=== Raw Source Frontmatter Check ===")
	files, err := v.FindRawFiles()
	if err != nil {
		fmt.Printf("  ERROR: %v\n\n", err)
		return 1
	}

	count := 0
	for _, file := range files {
		rel, _ := filepath.Rel(v.Dir, file)
		fm, err := vault.ParseFrontmatter(file)
		if err != nil {
			fmt.Printf("  FAIL: %s — %v\n", rel, err)
			count++
			continue
		}
		if fm == nil {
			fmt.Printf("  FAIL: %s — missing frontmatter\n", rel)
			count++
			continue
		}

		var missing []string
		for _, key := range []string{"title", "source", "date-added"} {
			if _, ok := fm[key]; !ok {
				missing = append(missing, key)
			}
		}
		if len(missing) > 0 {
			fmt.Printf("  WARN: %s — missing: %s\n", rel, strings.Join(missing, " "))
			count++
		}
	}

	if count == 0 {
		fmt.Println("  OK")
	}
	fmt.Println()
	return count
}

func checkLinks(v *vault.Vault) int {
	fmt.Println("=== Broken Wikilinks ===")
	slugs, err := v.BuildSlugIndex()
	if err != nil {
		fmt.Printf("  ERROR: %v\n\n", err)
		return 1
	}

	pages, err := v.FindWikiPages()
	if err != nil {
		fmt.Printf("  ERROR: %v\n\n", err)
		return 1
	}

	count := 0
	for _, page := range pages {
		rel, _ := filepath.Rel(v.Dir, page)
		links, err := vault.ExtractWikilinks(page)
		if err != nil {
			continue
		}
		for _, link := range links {
			target := strings.ToLower(link)
			if !slugs[target] {
				fmt.Printf("  WARN: %s → [[%s]] (not found)\n", rel, link)
				count++
			}
		}
	}

	if count == 0 {
		fmt.Println("  OK")
	}
	fmt.Println()
	return count
}

func checkOrphans(v *vault.Vault) int {
	fmt.Println("=== Orphan Pages (no inbound links) ===")

	// Build set of all link targets
	pages, err := v.FindWikiPages()
	if err != nil {
		fmt.Printf("  ERROR: %v\n\n", err)
		return 1
	}

	targets := make(map[string]bool)
	for _, page := range pages {
		links, err := vault.ExtractWikilinks(page)
		if err != nil {
			continue
		}
		for _, link := range links {
			targets[strings.ToLower(link)] = true
		}
	}

	count := 0
	for _, page := range pages {
		rel, _ := filepath.Rel(v.Dir, page)
		base := strings.TrimSuffix(filepath.Base(page), ".md")

		// Skip index and log
		if base == "index" || rel == "meta/log.md" {
			continue
		}

		relNoExt := strings.TrimSuffix(rel, ".md")
		baseLower := strings.ToLower(base)
		relLower := strings.ToLower(relNoExt)

		if !targets[baseLower] && !targets[relLower] {
			fmt.Printf("  WARN: %s — no inbound links\n", rel)
			count++
		}
	}

	if count == 0 {
		fmt.Println("  OK")
	}
	fmt.Println()
	return count
}
