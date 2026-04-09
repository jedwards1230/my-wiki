package search

import (
	"strings"
	"unicode"
)

// Searcher is the interface that search backends must implement.
type Searcher interface {
	// Search returns results matching the query, capped at limit.
	Search(query string, limit int) ([]Result, error)
	// Name returns the engine identifier (e.g., "substring", "index").
	Name() string
}

// Result represents a single search hit.
type Result struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
	Match   string  `json:"match"`  // "title", "tags", or "content"
	Engine  string  `json:"engine"` // which backend produced this result
}

// StripFrontmatter removes YAML frontmatter (between --- markers) from content,
// returning only the body text.
func StripFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		// Check for frontmatter at very end of file (no trailing newline after ---)
		if idx := strings.Index(content[4:], "\n---"); idx >= 0 && idx+4+4 == len(content) {
			return ""
		}
		return content
	}
	// Skip past the closing ---\n
	return strings.TrimSpace(content[4+end+5:])
}

// ExtractSnippet finds the first occurrence of query (case-insensitive) in content
// and returns a window of text around it. Returns the first windowSize characters
// of content if query is not found.
func ExtractSnippet(content, query string, windowBefore, windowAfter int) string {
	if content == "" {
		return ""
	}

	lower := strings.ToLower(content)
	lowerQ := strings.ToLower(query)

	idx := strings.Index(lower, lowerQ)
	if idx < 0 {
		// No match in content — return first chunk
		end := windowBefore + windowAfter
		if end > len(content) {
			end = len(content)
		}
		snippet := content[:end]
		snippet = trimToWordBoundary(snippet)
		if end < len(content) {
			snippet += "..."
		}
		return snippet
	}

	start := idx - windowBefore
	prefix := ""
	if start < 0 {
		start = 0
	} else {
		prefix = "..."
	}

	end := idx + len(query) + windowAfter
	suffix := ""
	if end > len(content) {
		end = len(content)
	} else {
		suffix = "..."
	}

	snippet := content[start:end]
	snippet = trimToWordBoundary(snippet)
	return prefix + snippet + suffix
}

// trimToWordBoundary trims partial words from the end of a snippet.
func trimToWordBoundary(s string) string {
	if len(s) == 0 {
		return s
	}
	// If the last char is whitespace or punctuation, it's already at a boundary
	last := rune(s[len(s)-1])
	if unicode.IsSpace(last) || unicode.IsPunct(last) {
		return strings.TrimRightFunc(s, unicode.IsSpace)
	}
	// Find the last space and trim there
	i := strings.LastIndexFunc(s, unicode.IsSpace)
	if i > len(s)/2 { // Only trim if we don't lose too much
		return s[:i]
	}
	return s
}
