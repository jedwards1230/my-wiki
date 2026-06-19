package admin

import "time"

// baseView holds fields every admin page needs for the shared chrome.
type baseView struct {
	Title   string
	Version string
	// User is the active admin session, used to render identity + the CSRF
	// token in forms. Nil on unauthenticated pages (login, error).
	User *session
}

// dashboardView is the read-only status dashboard model.
type dashboardView struct {
	baseView

	VaultDir     string
	Port         string
	BaseURL      string
	WatchEnabled bool

	AuthMode    string
	AuthIssuer  string
	AdminGroups []string

	PageCount int

	IndexRegistered bool
	IndexDocCount   int
	IndexLastBuilt  string // human-readable, or "never"
}

// loginView backs the sign-in landing page.
type loginView struct {
	baseView
	// Return is the sanitized post-login path, threaded through to authorize.
	Return string
	// Error is a user-facing message when a prior login attempt failed.
	Error string
}

// messageView backs the forbidden / generic error pages.
type messageView struct {
	baseView
	Heading string
	Message string
}

// dashboardData assembles the dashboard model from the handler's resolved
// config and live providers.
func (h *Handler) dashboardData(sess *session) dashboardView {
	v := dashboardView{
		baseView: baseView{
			Title:   "Admin",
			Version: h.cfg.Version,
			User:    sess,
		},
		VaultDir:       h.cfg.VaultDir,
		Port:           h.cfg.Port,
		BaseURL:        h.cfg.BaseURL,
		WatchEnabled:   h.cfg.WatchEnabled,
		AuthMode:       h.cfg.AuthMode,
		AuthIssuer:     h.cfg.AuthIssuer,
		AdminGroups:    h.cfg.AdminGroups,
		IndexLastBuilt: "never",
	}
	if h.pageCount != nil {
		v.PageCount = h.pageCount()
	}
	if h.indexStats != nil {
		docs, lastBuilt, registered := h.indexStats()
		v.IndexRegistered = registered
		v.IndexDocCount = docs
		if !lastBuilt.IsZero() {
			v.IndexLastBuilt = lastBuilt.Format(time.RFC3339)
		}
	}
	return v
}
