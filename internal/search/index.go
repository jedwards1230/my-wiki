package search

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jedwards1230/home-wiki/internal/vault"
)

var tokenizeRe = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// document is an indexed wiki page.
type document struct {
	path    string
	title   string
	tags    string
	content string // body with frontmatter stripped
}

// posting records a term's presence in a document.
type posting struct {
	docID int
	tf    float64 // term frequency: count / total tokens in doc
}

// indexSnapshot is an immutable search index. A new snapshot is built on each
// reindex and swapped in atomically.
type indexSnapshot struct {
	docs     []document
	postings map[string][]posting // token → posting list
	docCount int
}

// IndexSearcher is an in-memory inverted index with TF-IDF scoring.
// The index is rebuilt periodically; readers see a consistent snapshot
// via atomic.Pointer with zero contention.
type IndexSearcher struct {
	snapshot atomic.Pointer[indexSnapshot]
	vault    *vault.Vault
}

// NewIndexSearcher creates an IndexSearcher for the given vault.
// Call Build() to populate the index before searching.
func NewIndexSearcher(v *vault.Vault) *IndexSearcher {
	s := &IndexSearcher{vault: v}
	// Store an empty snapshot so Search works before Build
	s.snapshot.Store(&indexSnapshot{
		postings: make(map[string][]posting),
	})
	return s
}

// Name returns "index".
func (s *IndexSearcher) Name() string { return "index" }

// Build scans all wiki pages and builds a new index snapshot.
func (s *IndexSearcher) Build() error {
	pages, err := s.vault.FindWikiPages()
	if err != nil {
		return err
	}

	snap := &indexSnapshot{
		postings: make(map[string][]posting),
	}

	for _, absPath := range pages {
		rel, _ := filepath.Rel(s.vault.Dir, absPath)

		// Skip activity logs (OS-aware separator)
		activityPrefix := filepath.Join("meta", "activity") + string(filepath.Separator)
		if strings.HasPrefix(rel, activityPrefix) {
			continue
		}

		// Skip generated index files
		if filepath.Base(rel) == "index.md" {
			continue
		}

		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		content := string(data)

		fm, _ := vault.ParseFrontmatter(absPath)

		title := strings.TrimSuffix(filepath.Base(absPath), ".md")
		tags := ""
		if fm != nil {
			if fm["title"] != "" {
				title = fm["title"]
			}
			tags = fm["tags"]
		}

		body := StripFrontmatter(content)

		doc := document{
			path:    rel,
			title:   title,
			tags:    tags,
			content: body,
		}

		docID := len(snap.docs)
		snap.docs = append(snap.docs, doc)

		// Tokenize and index. Weight title tokens higher by repeating them.
		var allText strings.Builder
		// Title tokens are weighted 5x
		for range 5 {
			allText.WriteString(title)
			allText.WriteByte(' ')
		}
		// Tag tokens weighted 3x
		for range 3 {
			allText.WriteString(tags)
			allText.WriteByte(' ')
		}
		allText.WriteString(body)

		tokens := tokenize(allText.String())
		if len(tokens) == 0 {
			continue
		}

		// Count term frequencies
		counts := make(map[string]int)
		for _, tok := range tokens {
			counts[tok]++
		}

		totalTokens := float64(len(tokens))
		for tok, count := range counts {
			snap.postings[tok] = append(snap.postings[tok], posting{
				docID: docID,
				tf:    float64(count) / totalTokens,
			})
		}
	}

	snap.docCount = len(snap.docs)
	s.snapshot.Store(snap)
	return nil
}

// Search queries the index using TF-IDF scoring.
func (s *IndexSearcher) Search(query string, limit int) ([]Result, error) {
	snap := s.snapshot.Load()
	if snap.docCount == 0 {
		return nil, nil
	}

	queryTokens := tokenize(query)
	if len(queryTokens) == 0 {
		return nil, nil
	}

	// Accumulate scores per document
	scores := make(map[int]float64)

	for _, tok := range queryTokens {
		postings, ok := snap.postings[tok]
		if !ok {
			continue
		}

		// IDF = log(N / df) where df = number of docs containing the term
		idf := math.Log(float64(snap.docCount) / float64(len(postings)))
		if idf < 0 {
			idf = 0
		}

		for _, p := range postings {
			scores[p.docID] += p.tf * idf
		}
	}

	if len(scores) == 0 {
		return nil, nil
	}

	// Build results
	type scored struct {
		docID int
		score float64
	}
	var hits []scored
	for docID, score := range scores {
		hits = append(hits, scored{docID, score})
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return snap.docs[hits[i].docID].title < snap.docs[hits[j].docID].title
	})

	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}

	results := make([]Result, len(hits))
	for i, h := range hits {
		doc := snap.docs[h.docID]

		match := classifyMatch(doc, query)
		snippet := ExtractSnippet(doc.content, query, 40, 80)

		results[i] = Result{
			Path:    doc.path,
			Title:   doc.title,
			Score:   h.score,
			Snippet: snippet,
			Match:   match,
			Engine:  s.Name(),
		}
	}

	return results, nil
}

// StartAutoRebuild runs Build() on a timer until ctx is cancelled.
func (s *IndexSearcher) StartAutoRebuild(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = s.Build()
			}
		}
	}()
}

// tokenize splits text into lowercase tokens, filtering short ones.
func tokenize(text string) []string {
	parts := tokenizeRe.Split(strings.ToLower(text), -1)
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) >= 2 {
			tokens = append(tokens, p)
		}
	}
	return tokens
}

// classifyMatch determines the best match category for a document.
func classifyMatch(doc document, query string) string {
	lowerQ := strings.ToLower(query)
	if strings.Contains(strings.ToLower(doc.title), lowerQ) {
		return "title"
	}
	if strings.Contains(strings.ToLower(doc.tags), lowerQ) {
		return "tags"
	}
	return "content"
}
