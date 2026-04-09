package service

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// RecentEntry describes a recently modified wiki page.
type RecentEntry struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Modified string `json:"modified"` // RFC 3339
}

// RecentService provides recently-modified page listings.
type RecentService struct {
	vault *vault.Vault
}

// NewRecentService creates a RecentService for the given vault.
func NewRecentService(v *vault.Vault) *RecentService {
	return &RecentService{vault: v}
}

// List returns wiki pages sorted by modification time (newest first).
// If limit > 0, at most limit entries are returned.
func (s *RecentService) List(limit int) ([]RecentEntry, error) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return nil, err
	}

	type pageWithMtime struct {
		entry RecentEntry
		mtime time.Time
	}

	var items []pageWithMtime
	for _, p := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, p)

		// Exclude meta/activity/ files
		if strings.HasPrefix(rel, "meta/activity/") || strings.HasPrefix(rel, filepath.Join("meta", "activity")+string(filepath.Separator)) {
			continue
		}

		info, err := os.Stat(p)
		if err != nil {
			continue // skip files we can't stat
		}

		title := strings.TrimSuffix(filepath.Base(p), ".md")
		fm, fmErr := vault.ParseFrontmatter(p)
		if fmErr == nil && fm != nil {
			if t := fm["title"]; t != "" {
				title = t
			}
		}

		items = append(items, pageWithMtime{
			entry: RecentEntry{
				Path:     rel,
				Title:    title,
				Modified: info.ModTime().UTC().Format(time.RFC3339),
			},
			mtime: info.ModTime(),
		})
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].mtime.After(items[j].mtime)
	})

	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}

	result := make([]RecentEntry, len(items))
	for i, item := range items {
		result[i] = item.entry
	}

	return result, nil
}
