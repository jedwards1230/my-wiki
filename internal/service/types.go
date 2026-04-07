package service

// LintIssue represents a single lint finding.
type LintIssue struct {
	File    string `json:"file"`
	Check   string `json:"check"`
	Level   string `json:"level"` // "FAIL", "WARN", "ERROR"
	Message string `json:"message"`
}

// LintReport is the result of running lint checks.
type LintReport struct {
	Issues []LintIssue `json:"issues"`
	Total  int         `json:"total"`
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
type ActivityEntry struct {
	Type    string   `json:"type"`
	Title   string   `json:"title"`
	Time    string   `json:"time"`
	Summary string   `json:"summary,omitempty"`
	Touched []string `json:"touched,omitempty"`
}
