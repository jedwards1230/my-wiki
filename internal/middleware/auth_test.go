package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/coreos/go-oidc/v3/oidc/oidctest"
)

const (
	testKeyID    = "test-key-1"
	testAudience = "wiki"
)

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		header string
		want   string
	}{
		{"Bearer abc123", "abc123"},
		{"Bearer ", ""},
		{"Basic abc123", ""},
		{"", ""},
	}

	for _, tt := range tests {
		r := httptest.NewRequest("GET", "/", nil)
		if tt.header != "" {
			r.Header.Set("Authorization", tt.header)
		}
		got := extractBearerToken(r)
		if got != tt.want {
			t.Errorf("extractBearerToken(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestUserFromContextNil(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if user := UserFromContext(r.Context()); user != nil {
		t.Error("expected nil user from empty context")
	}
}

func TestWithUserRoundTrip(t *testing.T) {
	ctx := context.Background()
	want := &UserInfo{Subject: "u", Username: "u", Email: "u@e", Groups: []string{"g"}}
	got := UserFromContext(WithUser(ctx, want))
	if got != want {
		t.Errorf("round-trip: got %v, want %v", got, want)
	}
}

func TestNewAuthValidation(t *testing.T) {
	tests := []struct {
		name    string
		cfg     AuthConfig
		wantErr string
	}{
		{"missing issuer", AuthConfig{Audience: "x"}, "IssuerURL is required"},
		{"missing audience", AuthConfig{IssuerURL: "https://example.com"}, "Audience is required"},
		{"http non-loopback", AuthConfig{IssuerURL: "http://example.com", Audience: "x"}, "https://"},
		{"ftp scheme", AuthConfig{IssuerURL: "ftp://example.com", Audience: "x"}, "https://"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewAuth(context.Background(), tt.cfg)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("got error %q, want contains %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestNewAuthHTTPSLoopbackAllowed(t *testing.T) {
	// Loopback http:// is allowed for tests. NewProvider will still fail because
	// there's no server at that port, but the scheme validation should have passed.
	_, err := NewAuth(context.Background(), AuthConfig{
		IssuerURL: "http://127.0.0.1:1/",
		Audience:  "x",
	})
	if err == nil {
		t.Fatal("expected OIDC discovery failure, got nil")
	}
	if strings.Contains(err.Error(), "https://") {
		t.Errorf("loopback http should not fail scheme check: %v", err)
	}
}

// --- End-to-end tests using oidctest ---

type testOIDC struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	issuer string
}

func newTestOIDC(t *testing.T) *testOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ts := &oidctest.Server{
		PublicKeys: []oidctest.PublicKey{{
			PublicKey: key.Public(),
			KeyID:     testKeyID,
			Algorithm: oidc.RS256,
		}},
	}
	srv := httptest.NewServer(ts)
	ts.SetIssuer(srv.URL)
	t.Cleanup(srv.Close)
	return &testOIDC{srv: srv, key: key, issuer: srv.URL}
}

func (o *testOIDC) signToken(t *testing.T, overrides map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss":                o.issuer,
		"aud":                testAudience,
		"sub":                "user-123",
		"exp":                time.Now().Add(1 * time.Hour).Unix(),
		"iat":                time.Now().Unix(),
		"preferred_username": "justin",
		"email":              "justin@example.com",
		"name":               "Justin Edwards",
		"groups":             []string{"admins"},
	}
	for k, v := range overrides {
		if v == nil {
			delete(claims, k)
		} else {
			claims[k] = v
		}
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return oidctest.SignIDToken(o.key, testKeyID, oidc.RS256, string(raw))
}

func runAuth(t *testing.T, cfg AuthConfig, token string) (*httptest.ResponseRecorder, *UserInfo) {
	t.Helper()
	mw, err := NewAuth(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewAuth: %v", err)
	}
	var captured *UserInfo
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest("GET", "/", nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w, captured
}

func TestAuthValidToken(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, nil)

	w, user := runAuth(t, AuthConfig{IssuerURL: o.issuer, Audience: testAudience}, token)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if user == nil {
		t.Fatal("expected user in context")
	}
	if user.Subject != "user-123" || user.Username != "justin" || user.Email != "justin@example.com" {
		t.Errorf("unexpected user: %+v", user)
	}
	if len(user.Groups) != 1 || user.Groups[0] != "admins" {
		t.Errorf("groups = %v, want [admins]", user.Groups)
	}
}

func TestAuthMissingToken(t *testing.T) {
	o := newTestOIDC(t)
	w, _ := runAuth(t, AuthConfig{IssuerURL: o.issuer, Audience: testAudience}, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthMalformedToken(t *testing.T) {
	o := newTestOIDC(t)
	w, _ := runAuth(t, AuthConfig{IssuerURL: o.issuer, Audience: testAudience}, "not-a-jwt")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthExpiredToken(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, map[string]any{"exp": time.Now().Add(-1 * time.Hour).Unix()})
	w, _ := runAuth(t, AuthConfig{IssuerURL: o.issuer, Audience: testAudience}, token)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for expired, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthWrongIssuer(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, map[string]any{"iss": "https://evil.example.com"})
	w, _ := runAuth(t, AuthConfig{IssuerURL: o.issuer, Audience: testAudience}, token)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong issuer, got %d", w.Code)
	}
}

func TestAuthWrongAudience(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, map[string]any{"aud": "other-app"})
	w, _ := runAuth(t, AuthConfig{IssuerURL: o.issuer, Audience: testAudience}, token)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong audience, got %d", w.Code)
	}
}

func TestAuthGroupAllowlistMatch(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, map[string]any{"groups": []string{"devs", "admins"}})
	cfg := AuthConfig{IssuerURL: o.issuer, Audience: testAudience, AllowedGroups: []string{"admins"}}
	w, user := runAuth(t, cfg, token)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if user == nil {
		t.Fatal("expected user in context")
	}
}

func TestAuthGroupAllowlistMiss(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, map[string]any{"groups": []string{"visitors"}})
	cfg := AuthConfig{IssuerURL: o.issuer, Audience: testAudience, AllowedGroups: []string{"admins"}}
	w, _ := runAuth(t, cfg, token)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", w.Code, w.Body.String())
	}
}

func TestAuthGroupAllowlistEmptyUserGroups(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, map[string]any{"groups": []string{}})
	cfg := AuthConfig{IssuerURL: o.issuer, Audience: testAudience, AllowedGroups: []string{"admins"}}
	w, _ := runAuth(t, cfg, token)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 when user has no groups but allowlist set, got %d", w.Code)
	}
}

func TestAuthEmptyAllowlistAllowsAny(t *testing.T) {
	o := newTestOIDC(t)
	token := o.signToken(t, map[string]any{"groups": []string{"whoever"}})
	cfg := AuthConfig{IssuerURL: o.issuer, Audience: testAudience} // no AllowedGroups
	w, _ := runAuth(t, cfg, token)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200 with empty allowlist, got %d", w.Code)
	}
}

func TestAuthBadSignature(t *testing.T) {
	o := newTestOIDC(t)
	// Sign with a different key — should fail verification against the server's JWKS.
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	claims := fmt.Sprintf(`{"iss":%q,"aud":%q,"sub":"u","exp":%d}`,
		o.issuer, testAudience, time.Now().Add(1*time.Hour).Unix())
	token := oidctest.SignIDToken(otherKey, testKeyID, oidc.RS256, claims)

	w, _ := runAuth(t, AuthConfig{IssuerURL: o.issuer, Audience: testAudience}, token)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for bad signature, got %d", w.Code)
	}
}
