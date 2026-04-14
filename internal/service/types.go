package service

// LintIssue represents a single lint finding.
type LintIssue struct {
	File    string `json:"file"`
	Check   string `json:"check"`
	Level   string `json:"level"` // "FAIL", "WARN", "ERROR", "INFO"
	Message string `json:"message"`
}

// LintReport is the result of running lint checks.
type LintReport struct {
	Issues []LintIssue `json:"issues"`
	Total  int         `json:"total"`
	Errors int         `json:"errors"` // count of FAIL + WARN + ERROR (excludes INFO)
}

// RawFileInfo describes an unprocessed raw source file.
type RawFileInfo struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	DateAdded string `json:"date_added,omitempty"`
}

// LogEntry represents one line from the log index.
type LogEntry struct {
	Date        string `json:"date"`
	Changes     int    `json:"changes"`
	Hash        string `json:"hash"`
	Title       string `json:"title"`
	ActivityRef string `json:"activity_ref"`
}

// DayLog represents activity entries for a single day.
type DayLog struct {
	Date    string          `json:"date"`
	Entries []ActivityEntry `json:"entries"`
}

// ActivityEntry represents a single activity log entry.
// When AutoLogged is true, Append writes only the H3 header line with no
// description body — used for compact per-file audit entries from PageService.
type ActivityEntry struct {
	Type       string   `json:"type"`
	Title      string   `json:"title"`
	Time       string   `json:"time"`
	Summary    string   `json:"summary,omitempty"`
	Touched    []string `json:"touched,omitempty"`
	AutoLogged bool     `json:"auto_logged,omitempty"`
}

// PatchOp represents a single find-and-replace operation.
type PatchOp struct {
	Find    string `json:"find"`
	Replace string `json:"replace"`
}

// SearchResponse is the result of a search across one or more engines.
type SearchResponse struct {
	Results   []SearchResult     `json:"results"`
	Engines   []string           `json:"engines"`
	ElapsedMs map[string]float64 `json:"elapsed_ms"`
}

// SearchResult is a single search hit (mirrors search.Result for JSON output).
type SearchResult struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet"`
	Match   string  `json:"match"`
	Engine  string  `json:"engine"`
}
