package search

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// SubstringSearcher performs case-insensitive substring matching across all
// wiki pages. It walks the filesystem on every query — no index required.
type SubstringSearcher struct {
	vault *vault.Vault
}

// NewSubstringSearcher creates a SubstringSearcher for the given vault.
func NewSubstringSearcher(v *vault.Vault) *SubstringSearcher {
	return &SubstringSearcher{vault: v}
}

// Name returns "substring".
func (s *SubstringSearcher) Name() string { return "substring" }

// Search walks all wiki pages and returns matches sorted by score.
// Scoring: title match = 100, tag match = 50, content match = 10.
// Scores are additive — a query matching both title and content scores 110.
func (s *SubstringSearcher) Search(query string, limit int) ([]Result, error) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return nil, err
	}

	lowerQ := strings.ToLower(query)
	var results []Result

	for _, absPath := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, absPath)

		// Skip activity log files (OS-aware separator)
		activityPrefix := filepath.Join("meta", "activity") + string(filepath.Separator)
		if strings.HasPrefix(rel, activityPrefix) {
			continue
		}

		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		content := string(data)

		fm, _ := vault.ParseFrontmatter(absPath)

		title := strings.TrimSuffix(filepath.Base(absPath), ".md")
		if fm != nil && fm["title"] != "" {
			title = fm["title"]
		}

		tags := ""
		if fm != nil {
			tags = fm["tags"]
		}

		body := StripFrontmatter(content)

		var score float64
		match := ""

		if strings.Contains(strings.ToLower(title), lowerQ) {
			score += 100
			match = "title"
		}

		if strings.Contains(strings.ToLower(tags), lowerQ) {
			score += 50
			if match == "" {
				match = "tags"
			}
		}

		if strings.Contains(strings.ToLower(body), lowerQ) {
			score += 10
			if match == "" {
				match = "content"
			}
		}

		if score == 0 {
			continue
		}

		snippet := ExtractSnippet(body, query, 40, 80)

		results = append(results, Result{
			Path:    rel,
			Title:   title,
			Score:   score,
			Snippet: snippet,
			Match:   match,
			Engine:  s.Name(),
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].Title < results[j].Title
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}
