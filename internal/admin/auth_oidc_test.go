package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func newTestAuthenticator(t *testing.T, m *mockIdP, adminGroups []string) *oidcAuthenticator {
	t.Helper()
	a, err := newOIDCAuthenticator(context.Background(), oidcConfig{
		IssuerURL:   m.issuer,
		ClientID:    "web-client",
		RedirectURL: "http://localhost/_/admin/callback",
		AdminGroups: adminGroups,
		SessionKeys: []string{testKeyA},
		Logger:      slog.Default(),
	})
	if err != nil {
		t.Fatalf("newOIDCAuthenticator: %v", err)
	}
	return a
}

func callbackReq(t *testing.T, a *oidcAuthenticator, flow flowState, q url.Values) *http.Request {
	t.Helper()
	sealed, err := a.codec.seal(flow)
	if err != nil {
		t.Fatalf("seal flow: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/_/admin/callback?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: flowCookieName, Value: sealed})
	return req
}

func validFlow() flowState {
	return flowState{
		State:    "state-123",
		Nonce:    "nonce-abc",
		Verifier: "verifier-xyz-1234567890",
		Return:   "/_/admin/lint-config",
		Expiry:   time.Now().Add(flowTTL).Unix(),
	}
}

func TestCompleteAuthSuccess(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	m.idTokenClaims = map[string]any{
		"nonce":              flow.Nonce,
		"groups":             []string{"admins"},
		"preferred_username": "alice",
		"email":              "alice@example.com",
	}
	req := callbackReq(t, a, flow, url.Values{"code": {"abc"}, "state": {flow.State}})

	sess, ret, err := a.completeAuth(context.Background(), req)
	if err != nil {
		t.Fatalf("completeAuth: %v", err)
	}
	if sess.Subject != "u1" || sess.Username != "alice" {
		t.Fatalf("unexpected session: %+v", sess)
	}
	if ret != "/_/admin/lint-config" {
		t.Fatalf("unexpected return path %q", ret)
	}
}

func TestCompleteAuthStateMismatch(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	req := callbackReq(t, a, flow, url.Values{"code": {"abc"}, "state": {"wrong-state"}})

	_, _, err := a.completeAuth(context.Background(), req)
	var ae authError
	if !errors.As(err, &ae) {
		t.Fatalf("expected authError for state mismatch, got %v", err)
	}
}

func TestCompleteAuthNonceMismatch(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	m.idTokenClaims = map[string]any{"nonce": "different-nonce", "groups": []string{"admins"}}
	req := callbackReq(t, a, flow, url.Values{"code": {"abc"}, "state": {flow.State}})

	_, _, err := a.completeAuth(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nonce mismatch")
	}
}

func TestCompleteAuthExpiredFlow(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	flow.Expiry = time.Now().Add(-time.Minute).Unix()
	req := callbackReq(t, a, flow, url.Values{"code": {"abc"}, "state": {flow.State}})

	_, _, err := a.completeAuth(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for expired flow")
	}
}

func TestCompleteAuthForbiddenGroup(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	m.idTokenClaims = map[string]any{"nonce": flow.Nonce, "groups": []string{"users"}}
	req := callbackReq(t, a, flow, url.Values{"code": {"abc"}, "state": {flow.State}})

	_, _, err := a.completeAuth(context.Background(), req)
	if !errors.Is(err, errForbidden) {
		t.Fatalf("expected errForbidden, got %v", err)
	}
}

func TestCompleteAuthGroupsFromUserinfo(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	// No groups in the ID token — must fall back to the userinfo endpoint.
	m.idTokenClaims = map[string]any{"nonce": flow.Nonce}
	m.userinfoClaims = map[string]any{"sub": "u1", "groups": []string{"admins"}}
	req := callbackReq(t, a, flow, url.Values{"code": {"abc"}, "state": {flow.State}})

	sess, _, err := a.completeAuth(context.Background(), req)
	if err != nil {
		t.Fatalf("completeAuth with userinfo fallback: %v", err)
	}
	if !contains(sess.Groups, "admins") {
		t.Fatalf("expected admins group from userinfo, got %v", sess.Groups)
	}
}

func TestCompleteAuthMissingCode(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	req := callbackReq(t, a, flow, url.Values{"state": {flow.State}})

	_, _, err := a.completeAuth(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for missing code")
	}
}

func TestCompleteAuthIdPError(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	flow := validFlow()
	req := callbackReq(t, a, flow, url.Values{"error": {"access_denied"}, "state": {flow.State}})

	_, _, err := a.completeAuth(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when IdP returns error param")
	}
}

func TestEstablishAndReadSession(t *testing.T) {
	m := newMockIdP(t)
	a := newTestAuthenticator(t, m, []string{"admins"})
	rec := httptest.NewRecorder()
	sess := &session{Subject: "u1", Groups: []string{"admins"}}
	if err := a.establishSession(rec, sess); err != nil {
		t.Fatalf("establishSession: %v", err)
	}
	if sess.CSRF == "" {
		t.Fatal("expected CSRF token to be set")
	}

	// The flow cookie must be cleared and a session cookie set.
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge >= 0 {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected a session cookie to be set")
	}
	if !sessionCookie.HttpOnly || !sessionCookie.Secure || sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie flags wrong: %+v", sessionCookie)
	}

	req := httptest.NewRequest(http.MethodGet, "/_/admin/", nil)
	req.AddCookie(sessionCookie)
	got, ok := a.sessionFromRequest(req)
	if !ok || got.Subject != "u1" || got.CSRF != sess.CSRF {
		t.Fatalf("sessionFromRequest round-trip failed: %+v ok=%v", got, ok)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
