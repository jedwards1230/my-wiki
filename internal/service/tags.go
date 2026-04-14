package service

import (
	"bufio"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

// TagTaxonomyEntry describes one row from the schema's tag taxonomy table.
type TagTaxonomyEntry struct {
	Domain    string   `json:"domain"`
	UseFor    string   `json:"use_for"`
	SubTags   []string `json:"sub_tags,omitempty"`
	PageCount int      `json:"page_count"`
}

// TagUsage describes a tag and how many pages use it.
type TagUsage struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// TagReport is the combined output of listing tags.
type TagReport struct {
	Taxonomy []TagTaxonomyEntry `json:"taxonomy"`
	Used     []TagUsage         `json:"used"`
	Total    int                `json:"total_pages"`
}

// TagService provides tag listing and validation.
type TagService struct {
	vault *vault.Vault
}

// NewTagService creates a TagService for the given vault.
func NewTagService(v *vault.Vault) *TagService {
	return &TagService{vault: v}
}

// schema tag block markers
const (
	tagBlockOpen  = "<!-- begin:tag-taxonomy -->"
	tagBlockClose = "<!-- end:tag-taxonomy -->"
)

// ParseTaxonomy reads the schema page and extracts the tag taxonomy from between
// the open/close markers. Returns domain -> TagTaxonomyEntry (without page counts).
func (s *TagService) ParseTaxonomy() ([]TagTaxonomyEntry, error) {
	schemaPath := filepath.Join(s.vault.Dir, "meta", "schema.md")
	data, err := s.vault.Storage.ReadFile("meta/schema.md")
	if err != nil {
		return nil, fmt.Errorf("read schema: %w (expected at %s)", err, schemaPath)
	}

	return parseTaxonomyFromContent(string(data))
}

// parseTaxonomyFromContent extracts taxonomy entries from schema content.
func parseTaxonomyFromContent(content string) ([]TagTaxonomyEntry, error) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	inBlock := false
	var entries []TagTaxonomyEntry

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if trimmed == tagBlockOpen {
			inBlock = true
			continue
		}
		if trimmed == tagBlockClose {
			break
		}
		if !inBlock {
			continue
		}

		// Skip table header and separator rows
		if strings.HasPrefix(trimmed, "|--") || strings.HasPrefix(trimmed, "| Domain") || strings.HasPrefix(trimmed, "| -") {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") {
			continue
		}

		entry, ok := parseTagRow(trimmed)
		if ok {
			entries = append(entries, entry)
		}
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no tag taxonomy found between %s and %s markers in meta/schema.md", tagBlockOpen, tagBlockClose)
	}

	return entries, scanner.Err()
}

// parseTagRow parses a markdown table row into a TagTaxonomyEntry.
func parseTagRow(line string) (TagTaxonomyEntry, bool) {
	// Split by | and trim
	parts := strings.Split(line, "|")
	if len(parts) < 4 {
		return TagTaxonomyEntry{}, false
	}

	domain := strings.TrimSpace(parts[1])
	useFor := strings.TrimSpace(parts[2])
	subTagsRaw := strings.TrimSpace(parts[3])

	if domain == "" {
		return TagTaxonomyEntry{}, false
	}

	// Remove backticks from domain and sub-tags
	domain = strings.Trim(domain, "`")

	var subTags []string
	if subTagsRaw != "" {
		for _, st := range strings.Split(subTagsRaw, ",") {
			st = strings.TrimSpace(st)
			st = strings.Trim(st, "`")
			if st != "" {
				subTags = append(subTags, st)
			}
		}
	}

	return TagTaxonomyEntry{
		Domain:  domain,
		UseFor:  useFor,
		SubTags: subTags,
	}, true
}

// AllowedTags returns the set of valid tags from the taxonomy.
// Includes each domain as a bare tag and all sub-tags.
func (s *TagService) AllowedTags() (map[string]bool, error) {
	entries, err := s.ParseTaxonomy()
	if err != nil {
		return nil, err
	}

	allowed := make(map[string]bool)
	for _, e := range entries {
		allowed[e.Domain] = true
		for _, st := range e.SubTags {
			allowed[st] = true
		}
	}
	return allowed, nil
}

// List returns a full tag report: taxonomy with page counts + all used tags.
func (s *TagService) List() (*TagReport, error) {
	taxonomy, err := s.ParseTaxonomy()
	if err != nil {
		return nil, err
	}

	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return nil, err
	}

	// Count tag usage across all pages.
	tagCounts := make(map[string]int)
	for _, page := range pages {
		fm, fmErr := vault.ParseFrontmatter(page)
		if fmErr != nil || fm == nil {
			continue
		}
		tags := fm["tags"]
		if tags == "" {
			continue
		}
		for _, t := range strings.Split(tags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tagCounts[t]++
			}
		}
	}

	// Annotate taxonomy with page counts.
	for i, e := range taxonomy {
		count := tagCounts[e.Domain]
		for _, st := range e.SubTags {
			count += tagCounts[st]
		}
		taxonomy[i].PageCount = count
	}

	// Build used tag list sorted by count desc.
	var used []TagUsage
	for tag, count := range tagCounts {
		used = append(used, TagUsage{Tag: tag, Count: count})
	}
	sort.Slice(used, func(i, j int) bool {
		if used[i].Count != used[j].Count {
			return used[i].Count > used[j].Count
		}
		return used[i].Tag < used[j].Tag
	})

	return &TagReport{
		Taxonomy: taxonomy,
		Used:     used,
		Total:    len(pages),
	}, nil
}
