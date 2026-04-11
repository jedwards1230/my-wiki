package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestAuthMissingToken(t *testing.T) {
	auth := Auth(AuthConfig{
		IssuerURL: "https://auth.example.com/application/o/wiki/",
		Audience:  "wiki",
	})

	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestAuthAllowAnonymous(t *testing.T) {
	auth := Auth(AuthConfig{
		IssuerURL:      "https://auth.example.com/application/o/wiki/",
		Audience:       "wiki",
		AllowAnonymous: true,
	})

	var user *UserInfo
	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
	if user != nil {
		t.Error("expected nil user for anonymous request")
	}
}

func TestAuthMalformedToken(t *testing.T) {
	auth := Auth(AuthConfig{
		IssuerURL: "https://auth.example.com/application/o/wiki/",
		Audience:  "wiki",
	})

	handler := auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer not-a-jwt")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestValidateTokenWithRealJWT(t *testing.T) {
	// Generate an RSA key pair for testing
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	kid := "test-key-1"

	// Start a fake JWKS server
	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := jwksResponse{
			Keys: []jwksKey{{
				Kid: kid,
				Kty: "RSA",
				Alg: "RS256",
				N:   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
				E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jwksServer.Close()

	// Build a JWT
	issuer := "https://auth.example.com/application/o/wiki/"
	token := buildTestJWT(t, privateKey, kid, map[string]any{
		"iss":                issuer,
		"sub":                "user-123",
		"aud":                "wiki",
		"exp":                float64(time.Now().Add(1 * time.Hour).Unix()),
		"preferred_username": "justin",
		"email":              "justin@example.com",
		"name":               "Justin Edwards",
		"groups":             []string{"admins"},
	})

	cache := &jwksCache{url: jwksServer.URL}
	user, err := validateToken(token, cache, issuer, "wiki")
	if err != nil {
		t.Fatalf("validateToken: %v", err)
	}

	if user.Subject != "user-123" {
		t.Errorf("subject = %q, want user-123", user.Subject)
	}
	if user.Username != "justin" {
		t.Errorf("username = %q, want justin", user.Username)
	}
	if user.Email != "justin@example.com" {
		t.Errorf("email = %q, want justin@example.com", user.Email)
	}
	if user.Name != "Justin Edwards" {
		t.Errorf("name = %q, want Justin Edwards", user.Name)
	}
	if len(user.Groups) != 1 || user.Groups[0] != "admins" {
		t.Errorf("groups = %v, want [admins]", user.Groups)
	}
}

func TestValidateTokenExpired(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}

	kid := "test-key-1"

	jwksServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := jwksResponse{
			Keys: []jwksKey{{
				Kid: kid,
				Kty: "RSA",
				Alg: "RS256",
				N:   base64.RawURLEncoding.EncodeToString(privateKey.PublicKey.N.Bytes()),
				E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(privateKey.PublicKey.E)).Bytes()),
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer jwksServer.Close()

	token := buildTestJWT(t, privateKey, kid, map[string]any{
		"iss": "https://auth.example.com/",
		"sub": "user-123",
		"exp": float64(time.Now().Add(-1 * time.Hour).Unix()),
	})

	cache := &jwksCache{url: jwksServer.URL}
	_, err = validateToken(token, cache, "https://auth.example.com/", "")
	if err == nil {
		t.Fatal("expected error for expired token")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("expected expiry error, got: %v", err)
	}
}

func TestAudienceUnmarshal(t *testing.T) {
	tests := []struct {
		input string
		want  audience
	}{
		{`"wiki"`, audience{"wiki"}},
		{`["wiki","other"]`, audience{"wiki", "other"}},
	}

	for _, tt := range tests {
		var a audience
		if err := json.Unmarshal([]byte(tt.input), &a); err != nil {
			t.Errorf("unmarshal %s: %v", tt.input, err)
			continue
		}
		if len(a) != len(tt.want) {
			t.Errorf("unmarshal %s: got %v, want %v", tt.input, a, tt.want)
		}
	}
}

func TestUserFromContextNil(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	user := UserFromContext(r.Context())
	if user != nil {
		t.Error("expected nil user from empty context")
	}
}

// buildTestJWT creates a signed RS256 JWT for testing.
func buildTestJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	payload, _ := json.Marshal(claims)

	headerB64 := base64.RawURLEncoding.EncodeToString(header)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signingInput := headerB64 + "." + payloadB64

	hashed := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, 0x05, hashed[:]) // 0x05 = crypto.SHA256
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	return fmt.Sprintf("%s.%s", signingInput, base64.RawURLEncoding.EncodeToString(sig))
}
