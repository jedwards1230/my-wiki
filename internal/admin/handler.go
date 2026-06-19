// Package admin implements the wiki's admin/settings panel served under
// /_/admin. It is a server-rendered surface (Go html/template) gated by a
// dedicated OIDC browser-login flow and a separate admin-group allow-list,
// kept deliberately minimal. Authentication is additive to the Bearer-token
// middleware that protects the REST API and MCP surfaces; the admin session
// is a separate, cookie-backed identity used only for /_/admin/*.
package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"
)

// Config holds the resolved (not raw-env) values surfaced on the read-only
// dashboard. It is assembled by the caller so this package needs no knowledge
// of CLI flags or env-var names.
type Config struct {
	VaultDir     string
	Port         string
	BaseURL      string
	Version      string
	WatchEnabled bool

	AuthMode    string
	AuthIssuer  string
	AdminGroups []string
}

// Options configures the admin Handler. It encapsulates the registration
// truth table: an issuer enables the OIDC login gate; with no issuer the panel
// registers only when DevInsecure is set (local dev), otherwise New returns a
// nil handler and the panel is not mounted.
type Options struct {
	// Dashboard / resolved config.
	VaultDir     string
	Port         string
	BaseURL      string
	Version      string
	WatchEnabled bool

	// Auth. When IssuerURL is non-empty the OIDC gate is built and ClientID,
	// SessionKeys, AdminGroups, and BaseURL are required.
	IssuerURL    string
	ClientID     string
	ClientSecret string
	AdminGroups  []string
	SessionKeys  []string
	// DevInsecure is only honored when IssuerURL is empty (auth disabled).
	DevInsecure bool

	// Providers for live dashboard data.
	IndexStats func() (docCount int, lastBuilt time.Time, registered bool)
	PageCount  func() int

	Logger *slog.Logger
}

// Handler serves the admin panel.
type Handler struct {
	cfg  Config
	tmpl *template.Template
	auth authenticator
	// oidc is non-nil only in OIDC mode; it backs the login/callback routes
	// and logout cookie clearing.
	oidc *oidcAuthenticator

	indexStats func() (docCount int, lastBuilt time.Time, registered bool)
	pageCount  func() int

	logger *slog.Logger
}

// New builds the admin Handler, performing OIDC discovery when an issuer is
// configured. It returns (nil, nil) when the panel should not be registered
// (auth disabled and DevInsecure unset), and an error on misconfiguration.
func New(ctx context.Context, o Options) (*Handler, error) {
	logger := o.Logger
	if logger == nil {
		logger = slog.Default()
	}

	var (
		auth     authenticator
		oidcAuth *oidcAuthenticator
		authMode string
	)

	switch {
	case o.IssuerURL != "":
		// Auth enabled. The admin panel is opt-in: with no admin groups
		// configured there is nothing to gate on, so leave it unmounted
		// rather than force every auth-enabled deployment to configure it.
		if len(o.AdminGroups) == 0 {
			return nil, nil
		}
		if o.BaseURL == "" {
			return nil, fmt.Errorf("admin: WIKI_BASE_URL is required to derive the OIDC redirect URL when the admin panel is enabled")
		}
		redirect := trimRightSlash(o.BaseURL) + adminPathPrefix + "/callback"
		a, err := newOIDCAuthenticator(ctx, oidcConfig{
			IssuerURL:    o.IssuerURL,
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			RedirectURL:  redirect,
			AdminGroups:  o.AdminGroups,
			SessionKeys:  o.SessionKeys,
			Logger:       logger,
		})
		if err != nil {
			return nil, err
		}
		auth, oidcAuth, authMode = a, a, "OIDC"
		logger.Info("admin panel enabled", "issuer", o.IssuerURL, "client_id", o.ClientID, "admin_groups", o.AdminGroups, "redirect_url", redirect)

	case o.DevInsecure:
		logger.Warn("admin panel running WITHOUT authentication (WIKI_ADMIN_DEV_INSECURE) — for LOCAL DEVELOPMENT ONLY; never expose this listener to a network")
		auth, authMode = devAuthenticator{}, "DISABLED (dev-insecure)"

	default:
		return nil, nil
	}

	tmpl, err := parseTemplates()
	if err != nil {
		return nil, fmt.Errorf("admin: parse templates: %w", err)
	}

	return &Handler{
		cfg: Config{
			VaultDir:     o.VaultDir,
			Port:         o.Port,
			BaseURL:      o.BaseURL,
			Version:      o.Version,
			WatchEnabled: o.WatchEnabled,
			AuthMode:     authMode,
			AuthIssuer:   o.IssuerURL,
			AdminGroups:  o.AdminGroups,
		},
		tmpl:       tmpl,
		auth:       auth,
		oidc:       oidcAuth,
		indexStats: o.IndexStats,
		pageCount:  o.PageCount,
		logger:     logger,
	}, nil
}

// RegisterRoutes mounts the admin routes on mux. Auth-flow routes (login,
// authorize, callback) are unauthenticated and registered only in OIDC mode;
// everything under /_/admin/ is wrapped by the session gate.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	if h.oidc != nil {
		mux.HandleFunc("GET "+adminPathPrefix+"/login", h.handleLogin)
		mux.HandleFunc("GET "+adminPathPrefix+"/authorize", h.handleAuthorize)
		mux.HandleFunc("GET "+adminPathPrefix+"/callback", h.handleCallback)
	}

	gated := http.NewServeMux()
	gated.HandleFunc("GET "+adminPathPrefix+"/{$}", h.handleDashboard)
	gated.HandleFunc("POST "+adminPathPrefix+"/logout", h.handleLogout)

	// Subtree handler. A bare /_/admin (no trailing slash) is 301'd here by
	// ServeMux. More specific patterns above (login/etc.) take precedence.
	mux.Handle(adminPathPrefix+"/", h.auth.middleware(gated))
}

// --- auth-flow routes (OIDC only) -------------------------------------------

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "login", loginView{
		baseView: baseView{Title: "Sign in", Version: h.cfg.Version},
		Return:   sanitizeReturn(r.URL.Query().Get("return")),
	})
}

func (h *Handler) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	ret := sanitizeReturn(r.URL.Query().Get("return"))
	if err := h.oidc.beginAuth(w, r, ret); err != nil {
		h.logger.Error("admin: begin auth failed", "error", err)
		h.renderMessage(w, http.StatusInternalServerError, "Sign-in error", "Could not start sign-in. Please try again.")
	}
}

func (h *Handler) handleCallback(w http.ResponseWriter, r *http.Request) {
	sess, ret, err := h.oidc.completeAuth(r.Context(), r)
	if err != nil {
		switch {
		case errors.Is(err, errForbidden):
			h.renderMessage(w, http.StatusForbidden, "Access denied", "Your account is not a member of an authorized admin group.")
		default:
			var ae authError
			msg := "Sign-in failed. Please try again."
			if errors.As(err, &ae) {
				msg = ae.msg
			}
			h.render(w, http.StatusOK, "login", loginView{
				baseView: baseView{Title: "Sign in", Version: h.cfg.Version},
				Return:   adminRoot,
				Error:    msg,
			})
		}
		return
	}
	if err := h.oidc.establishSession(w, sess); err != nil {
		h.logger.Error("admin: establish session failed", "error", err)
		h.renderMessage(w, http.StatusInternalServerError, "Sign-in error", "Could not establish your session. Please try again.")
		return
	}
	http.Redirect(w, r, ret, http.StatusSeeOther)
}

// --- gated routes -----------------------------------------------------------

func (h *Handler) handleDashboard(w http.ResponseWriter, r *http.Request) {
	h.render(w, http.StatusOK, "dashboard", h.dashboardData(sessionFromContext(r.Context())))
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !h.verifyCSRF(r) {
		h.renderMessage(w, http.StatusForbidden, "Invalid request", "Your session token did not match. Please go back and try again.")
		return
	}
	if h.oidc != nil {
		h.oidc.clearSession(w)
		http.Redirect(w, r, adminPathPrefix+"/login", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, adminRoot, http.StatusSeeOther)
}

// --- helpers ----------------------------------------------------------------

// verifyCSRF checks the double-submit token in the POST body against the
// session's CSRF token (constant-time).
func (h *Handler) verifyCSRF(r *http.Request) bool {
	sess := sessionFromContext(r.Context())
	if sess == nil {
		return false
	}
	if err := r.ParseForm(); err != nil {
		return false
	}
	return constantTimeEqual(r.PostFormValue("csrf_token"), sess.CSRF)
}

func (h *Handler) render(w http.ResponseWriter, status int, name string, data any) {
	var buf bytes.Buffer
	if err := h.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		h.logger.Error("admin: template render failed", "template", name, "error", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

func (h *Handler) renderMessage(w http.ResponseWriter, status int, heading, message string) {
	h.render(w, status, "message", messageView{
		baseView: baseView{Title: heading, Version: h.cfg.Version},
		Heading:  heading,
		Message:  message,
	})
}

func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
