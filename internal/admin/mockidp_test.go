package admin

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mockIdP is a minimal OpenID Connect provider for tests: it serves discovery,
// a JWKS, a token endpoint, and a userinfo endpoint, signing ID tokens with an
// ephemeral RSA key. Tests set idTokenClaims / userinfoClaims to control what
// the token and userinfo endpoints return.
type mockIdP struct {
	srv            *httptest.Server
	key            *rsa.PrivateKey
	issuer         string
	idTokenClaims  map[string]any
	userinfoClaims map[string]any
}

func newMockIdP(t *testing.T) *mockIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	m := &mockIdP{key: key}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                m.issuer,
			"authorization_endpoint":                m.issuer + "/auth",
			"token_endpoint":                        m.issuer + "/token",
			"jwks_uri":                              m.issuer + "/keys",
			"userinfo_endpoint":                     m.issuer + "/userinfo",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		pub := m.key.Public().(*rsa.PublicKey)
		writeJSON(w, map[string]any{
			"keys": []map[string]any{{
				"kty": "RSA",
				"kid": "test",
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     m.signIDToken(t),
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		claims := m.userinfoClaims
		if claims == nil {
			claims = map[string]any{"sub": "u1"}
		}
		writeJSON(w, claims)
	})

	m.srv = httptest.NewServer(mux)
	m.issuer = m.srv.URL
	t.Cleanup(m.srv.Close)
	return m
}

// signIDToken builds an RS256 JWT from base claims merged with idTokenClaims.
func (m *mockIdP) signIDToken(t *testing.T) string {
	t.Helper()
	claims := map[string]any{
		"iss": m.issuer,
		"aud": "web-client",
		"sub": "u1",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	for k, v := range m.idTokenClaims {
		claims[k] = v
	}
	header := map[string]any{"alg": "RS256", "typ": "JWT", "kid": "test"}
	enc := base64.RawURLEncoding
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := enc.EncodeToString(hb) + "." + enc.EncodeToString(cb)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, m.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + enc.EncodeToString(sig)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
