package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jedwards1230/my-wiki/internal/middleware"
	"golang.org/x/oauth2"
)

// authenticator gates admin routes. Two implementations exist: oidcAuthenticator
// (real browser SSO) and devAuthenticator (local-dev synthetic identity).
type authenticator interface {
	// middleware ensures an authenticated admin session is present, injecting
	// the UserInfo and session into the request context; otherwise it redirects
	// unauthenticated browsers to the login page.
	middleware(next http.Handler) http.Handler
}

// errForbidden signals a successfully-authenticated user who is not a member of
// any admin group. errAuthFlow signals a recoverable login-flow failure with a
// user-facing message.
var errForbidden = errors.New("admin: not authorized")

type authError struct{ msg string }

func (e authError) Error() string { return e.msg }

func errAuthFlow(msg string) error { return authError{msg: msg} }

// oidcConfig configures the browser OIDC login subsystem.
type oidcConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	RedirectURL  string // absolute, e.g. https://wiki.example.com/_/admin/callback
	AdminGroups  []string
	SessionKeys  []string
	SessionTTL   time.Duration
	Logger       *slog.Logger
}

// oidcAuthenticator runs the Authorization Code + PKCE flow against the same
// issuer as the Bearer-token middleware, and manages the encrypted session
// cookie that backs the admin panel.
type oidcAuthenticator struct {
	codec       *secretCodec
	oauth2Cfg   *oauth2.Config
	verifier    *oidc.IDTokenVerifier
	provider    *oidc.Provider
	adminGroups []string
	sessionTTL  time.Duration
	logger      *slog.Logger
}

// newOIDCAuthenticator performs OIDC discovery and builds the login subsystem.
// It fails closed: missing client ID, redirect URL, admin groups, or a weak
// session key all stop startup.
func newOIDCAuthenticator(ctx context.Context, cfg oidcConfig) (*oidcAuthenticator, error) {
	if cfg.ClientID == "" {
		return nil, errors.New("admin: " + "client ID is required")
	}
	if cfg.RedirectURL == "" {
		return nil, errors.New("admin: redirect URL is required (set the site base URL)")
	}
	if len(cfg.AdminGroups) == 0 {
		return nil, errors.New("admin: at least one admin group is required")
	}
	codec, err := newSecretCodec(cfg.SessionKeys)
	if err != nil {
		return nil, err
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, err
	}
	ttl := cfg.SessionTTL
	if ttl <= 0 {
		ttl = 8 * time.Hour
	}
	return &oidcAuthenticator{
		codec: codec,
		oauth2Cfg: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email", "groups"},
		},
		verifier:    provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		provider:    provider,
		adminGroups: cfg.AdminGroups,
		sessionTTL:  ttl,
		logger:      cfg.Logger,
	}, nil
}

func (a *oidcAuthenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.sessionFromRequest(r)
		if !ok {
			redirectToLogin(w, r)
			return
		}
		ctx := withSession(middleware.WithUser(r.Context(), userInfoFromSession(sess)), sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *oidcAuthenticator) sessionFromRequest(r *http.Request) (*session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil, false
	}
	var s session
	if err := a.codec.open(c.Value, &s); err != nil {
		return nil, false
	}
	if s.expired() {
		return nil, false
	}
	return &s, true
}

// beginAuth mints state/nonce/PKCE, stashes them in the flow cookie, and
// redirects the browser to the IdP.
func (a *oidcAuthenticator) beginAuth(w http.ResponseWriter, r *http.Request, returnPath string) error {
	state, err := randomToken(24)
	if err != nil {
		return err
	}
	nonce, err := randomToken(24)
	if err != nil {
		return err
	}
	verifier := oauth2.GenerateVerifier()
	flow := flowState{
		State:    state,
		Nonce:    nonce,
		Verifier: verifier,
		Return:   sanitizeReturn(returnPath),
		Expiry:   time.Now().Add(flowTTL).Unix(),
	}
	sealed, err := a.codec.seal(flow)
	if err != nil {
		return err
	}
	setCookie(w, flowCookieName, sealed, int(flowTTL.Seconds()))
	authURL := a.oauth2Cfg.AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	)
	http.Redirect(w, r, authURL, http.StatusSeeOther)
	return nil
}

// completeAuth validates the callback (state, code, ID token, nonce, admin
// group) and returns a session (CSRF/expiry not yet set) plus the sanitized
// post-login return path. Returns errForbidden for a non-admin user and an
// authError for recoverable flow failures.
func (a *oidcAuthenticator) completeAuth(ctx context.Context, r *http.Request) (*session, string, error) {
	c, err := r.Cookie(flowCookieName)
	if err != nil {
		return nil, "", errAuthFlow("missing login state — please sign in again")
	}
	var flow flowState
	if err := a.codec.open(c.Value, &flow); err != nil {
		return nil, "", errAuthFlow("invalid login state — please sign in again")
	}
	if flow.expired() {
		return nil, "", errAuthFlow("login timed out — please sign in again")
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		return nil, "", errAuthFlow("identity provider error: " + e)
	}
	if st := q.Get("state"); st == "" || !constantTimeEqual(st, flow.State) {
		return nil, "", errAuthFlow("state mismatch — please sign in again")
	}
	code := q.Get("code")
	if code == "" {
		return nil, "", errAuthFlow("missing authorization code")
	}

	tok, err := a.oauth2Cfg.Exchange(ctx, code, oauth2.VerifierOption(flow.Verifier))
	if err != nil {
		return nil, "", errAuthFlow("token exchange failed")
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return nil, "", errAuthFlow("no id_token in token response")
	}
	idToken, err := a.verifier.Verify(ctx, rawID)
	if err != nil {
		return nil, "", errAuthFlow("id_token verification failed")
	}
	if !constantTimeEqual(idToken.Nonce, flow.Nonce) {
		return nil, "", errAuthFlow("nonce mismatch — please sign in again")
	}

	var claims struct {
		Subject           string   `json:"sub"`
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, "", errAuthFlow("cannot read identity claims")
	}

	groups := claims.Groups
	// Handle-both: some IdPs only expose groups via the userinfo endpoint, not
	// the ID token. Fall back to userinfo when the ID token carries no groups.
	if len(groups) == 0 {
		if ui, uerr := a.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok)); uerr == nil {
			var uiClaims struct {
				Groups []string `json:"groups"`
			}
			if ui.Claims(&uiClaims) == nil {
				groups = uiClaims.Groups
			}
		}
	}

	if !middleware.HasAllowedGroup(groups, a.adminGroups) {
		return nil, "", errForbidden
	}

	return &session{
		Subject:  claims.Subject,
		Username: claims.PreferredUsername,
		Email:    claims.Email,
		Name:     claims.Name,
		Groups:   groups,
	}, flow.Return, nil
}

// establishSession assigns a fresh CSRF token + expiry (defeating fixation),
// seals the session cookie, and clears the now-spent flow cookie.
func (a *oidcAuthenticator) establishSession(w http.ResponseWriter, sess *session) error {
	csrf, err := randomToken(24)
	if err != nil {
		return err
	}
	sess.CSRF = csrf
	sess.Expiry = time.Now().Add(a.sessionTTL).Unix()
	sealed, err := a.codec.seal(sess)
	if err != nil {
		return err
	}
	clearCookie(w, flowCookieName)
	setCookie(w, sessionCookieName, sealed, int(a.sessionTTL.Seconds()))
	return nil
}

func (a *oidcAuthenticator) clearSession(w http.ResponseWriter) {
	clearCookie(w, sessionCookieName)
}

// devAuthenticator injects a synthetic local-dev admin without any OIDC flow.
// It is only wired when auth is disabled AND the dev-insecure opt-in is set.
type devAuthenticator struct{}

func (devAuthenticator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := devSession()
		ctx := withSession(middleware.WithUser(r.Context(), userInfoFromSession(sess)), sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func devSession() *session {
	return &session{
		Subject:  "dev-admin",
		Username: "dev-admin",
		Name:     "Local Dev Admin",
		Groups:   []string{"dev"},
		CSRF:     "dev-insecure-csrf",
		Expiry:   time.Now().Add(time.Hour).Unix(),
	}
}

func userInfoFromSession(s *session) *middleware.UserInfo {
	return &middleware.UserInfo{
		Subject:  s.Subject,
		Username: s.Username,
		Email:    s.Email,
		Name:     s.Name,
		Groups:   s.Groups,
	}
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	loginURL := adminPathPrefix + "/login"
	if r.Method == http.MethodGet {
		loginURL += "?return=" + url.QueryEscape(r.URL.Path)
	}
	http.Redirect(w, r, loginURL, http.StatusSeeOther)
}

// sanitizeReturn permits only same-origin paths under /_/admin/, defeating
// open-redirects via the post-login return parameter.
func sanitizeReturn(p string) string {
	if p == "" || !strings.HasPrefix(p, adminPathPrefix) {
		return adminRoot
	}
	if strings.HasPrefix(p, "//") || strings.Contains(p, "..") {
		return adminRoot
	}
	u, err := url.Parse(p)
	if err != nil || u.IsAbs() || u.Host != "" {
		return adminRoot
	}
	return u.EscapedPath()
}
