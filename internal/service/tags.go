package service

import (
	"regexp"
	"sort"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// TagUsage describes a tag and how many pages use it.
type TagUsage struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// TagReport is the output of listing tags — all used tags with counts.
type TagReport struct {
	Used  []TagUsage `json:"used"`
	Total int        `json:"total_pages"`
}

// TagService provides tag listing and structural validation.
type TagService struct {
	vault *vault.Vault
}

// NewTagService creates a TagService for the given vault.
func NewTagService(v *vault.Vault) *TagService {
	return &TagService{vault: v}
}

// kebabSegmentRe matches a valid kebab-case segment (lowercase letters, digits, hyphens).
var kebabSegmentRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// IsKebabCase returns true if every segment of the tag (split by /) is kebab-case.
func IsKebabCase(tag string) bool {
	for _, segment := range strings.Split(tag, "/") {
		if !kebabSegmentRe.MatchString(segment) {
			return false
		}
	}
	return true
}

// ParentDomain returns the top-level domain of a hierarchical tag.
// For "homelab/service" it returns "homelab". For "homelab" it returns "homelab".
func ParentDomain(tag string) string {
	if idx := strings.IndexByte(tag, '/'); idx >= 0 {
		return tag[:idx]
	}
	return tag
}

// CountTags walks all wiki pages and returns tag usage counts.
// Skips generated pages.
func (s *TagService) CountTags() (map[string]int, error) {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return nil, err
	}

	counts := make(map[string]int)
	for _, page := range pages {
		fm, fmErr := vault.ParseFrontmatter(page)
		if fmErr != nil || fm == nil {
			continue
		}
		if fm["generated"] == "true" {
			continue
		}
		tags := fm["tags"]
		if tags == "" {
			continue
		}
		for _, t := range strings.Split(tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				counts[t]++
			}
		}
	}
	return counts, nil
}

// List returns all used tags with counts, sorted by frequency descending.
func (s *TagService) List() (*TagReport, error) {
	counts, err := s.CountTags()
	if err != nil {
		return nil, err
	}

	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return nil, err
	}

	var used []TagUsage
	for tag, count := range counts {
		used = append(used, TagUsage{Tag: tag, Count: count})
	}
	sort.Slice(used, func(i, j int) bool {
		if used[i].Count != used[j].Count {
			return used[i].Count > used[j].Count
		}
		return used[i].Tag < used[j].Tag
	})

	return &TagReport{
		Used:  used,
		Total: len(pages),
	}, nil
}
