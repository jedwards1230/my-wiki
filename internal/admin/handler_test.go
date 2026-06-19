package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func serveAdmin(t *testing.T, h *Handler, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func devOptions() Options {
	return Options{
		Version:     "testver",
		DevInsecure: true,
		PageCount:   func() int { return 7 },
		IndexStats:  func() (int, time.Time, bool) { return 3, time.Unix(1700000000, 0), true },
	}
}

func TestNewNotRegistered(t *testing.T) {
	// Auth off, no dev opt-in → not mounted.
	h, err := New(context.Background(), Options{})
	if err != nil || h != nil {
		t.Fatalf("expected (nil,nil), got h=%v err=%v", h, err)
	}

	// Auth on but no admin groups → opt-out, not mounted (no discovery needed).
	h, err = New(context.Background(), Options{IssuerURL: "https://issuer.example.com"})
	if err != nil || h != nil {
		t.Fatalf("expected (nil,nil) for empty admin groups, got h=%v err=%v", h, err)
	}
}

func TestNewOIDCRequiresBaseURL(t *testing.T) {
	_, err := New(context.Background(), Options{
		IssuerURL:   "https://issuer.example.com",
		AdminGroups: []string{"admins"},
		ClientID:    "web",
		SessionKeys: []string{testKeyA},
		// BaseURL omitted
	})
	if err == nil {
		t.Fatal("expected error when BaseURL is missing for OIDC mode")
	}
}

func TestNewDevInsecure(t *testing.T) {
	h, err := New(context.Background(), devOptions())
	if err != nil {
		t.Fatalf("New dev: %v", err)
	}
	if h == nil || h.oidc != nil {
		t.Fatalf("expected dev handler with nil oidc, got %+v", h)
	}
}

func TestRegisterRoutesNoCatchAllConflict(t *testing.T) {
	// The real server registers a catch-all "GET /{path...}". Admin routes must
	// be strictly more specific so registration doesn't panic.
	h, _ := New(context.Background(), devOptions())
	mux := http.NewServeMux()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("route registration panicked alongside catch-all: %v", r)
		}
	}()
	h.RegisterRoutes(mux)
	mux.HandleFunc("GET /{path...}", func(http.ResponseWriter, *http.Request) {})

	// Bare /_/admin redirects to the trailing-slash form.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_/admin", nil))
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("bare /_/admin status = %d, want 301", rec.Code)
	}
}

func TestDashboardRendersDevMode(t *testing.T) {
	h, err := New(context.Background(), devOptions())
	if err != nil {
		t.Fatal(err)
	}
	rec := serveAdmin(t, h, httptest.NewRequest(http.MethodGet, "/_/admin/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"testver", "Wiki pages", "active (TF-IDF)", "DISABLED (dev-insecure)", "Sign out"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard body missing %q", want)
		}
	}
}

func TestLogoutCSRF(t *testing.T) {
	h, _ := New(context.Background(), devOptions())

	// Missing token → 403.
	rec := serveAdmin(t, h, httptest.NewRequest(http.MethodPost, "/_/admin/logout", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF: status = %d, want 403", rec.Code)
	}

	// Correct dev token → redirect.
	req := httptest.NewRequest(http.MethodPost, "/_/admin/logout", strings.NewReader("csrf_token=dev-insecure-csrf"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = serveAdmin(t, h, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("logout with CSRF: status = %d, want 303", rec.Code)
	}
}

func TestOIDCGateRedirectsToLogin(t *testing.T) {
	m := newMockIdP(t)
	h, err := New(context.Background(), Options{
		Version:     "testver",
		BaseURL:     "https://wiki.example.com",
		IssuerURL:   m.issuer,
		ClientID:    "web-client",
		AdminGroups: []string{"admins"},
		SessionKeys: []string{testKeyA},
	})
	if err != nil {
		t.Fatalf("New OIDC: %v", err)
	}

	// Unauthenticated dashboard request → redirect to login.
	rec := serveAdmin(t, h, httptest.NewRequest(http.MethodGet, "/_/admin/", nil))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, adminPathPrefix+"/login") {
		t.Fatalf("expected redirect to login, got %q", loc)
	}

	// Login page renders.
	rec = serveAdmin(t, h, httptest.NewRequest(http.MethodGet, "/_/admin/login", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign in with SSO") {
		t.Fatal("login page missing sign-in button")
	}
}

func TestCallbackForbiddenRendersDenied(t *testing.T) {
	m := newMockIdP(t)
	h, err := New(context.Background(), Options{
		Version:     "testver",
		BaseURL:     "https://wiki.example.com",
		IssuerURL:   m.issuer,
		ClientID:    "web-client",
		AdminGroups: []string{"admins"},
		SessionKeys: []string{testKeyA},
	})
	if err != nil {
		t.Fatal(err)
	}
	flow := validFlow()
	m.idTokenClaims = map[string]any{"nonce": flow.Nonce, "groups": []string{"users"}}
	req := callbackReq(t, h.oidc, flow, mustValues("code", "abc", "state", flow.State))
	rec := serveAdmin(t, h, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for non-admin, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Access denied") {
		t.Fatal("expected access-denied page")
	}
}

func mustValues(kv ...string) (v map[string][]string) {
	v = map[string][]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		v[kv[i]] = []string{kv[i+1]}
	}
	return v
}
