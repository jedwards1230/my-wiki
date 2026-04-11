package middleware

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

type contextKey int

const userContextKey contextKey = iota

// UserInfo holds the authenticated user's identity extracted from JWT claims.
type UserInfo struct {
	Subject  string   // sub claim — unique user ID from Authentik
	Username string   // preferred_username claim
	Email    string   // email claim
	Name     string   // name claim (display name)
	Groups   []string // groups claim
}

// UserFromContext returns the authenticated user from the request context, or nil
// if the request is unauthenticated.
func UserFromContext(ctx context.Context) *UserInfo {
	u, _ := ctx.Value(userContextKey).(*UserInfo)
	return u
}

// AuthConfig configures the OIDC JWT validation middleware.
type AuthConfig struct {
	// IssuerURL is the Authentik OIDC issuer (e.g., "https://auth.example.com/application/o/home-wiki/").
	// The JWKS endpoint is derived as IssuerURL + "/jwks/".
	IssuerURL string

	// Audience is the expected "aud" claim. Typically the OAuth2 client ID.
	Audience string

	// Optional: if true, unauthenticated requests are allowed through with no UserInfo
	// (the handler can check UserFromContext to decide). If false, 401 is returned.
	AllowAnonymous bool
}

// Auth returns middleware that validates Bearer JWT tokens against Authentik's JWKS endpoint.
// On success, it adds UserInfo to the request context. On failure, it returns 401.
func Auth(cfg AuthConfig) func(http.Handler) http.Handler {
	jwks := &jwksCache{
		url: strings.TrimRight(cfg.IssuerURL, "/") + "/jwks/",
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearerToken(r)
			if token == "" {
				if cfg.AllowAnonymous {
					next.ServeHTTP(w, r)
					return
				}
				http.Error(w, "missing authorization header", http.StatusUnauthorized)
				return
			}

			user, err := validateToken(token, jwks, cfg.IssuerURL, cfg.Audience)
			if err != nil {
				http.Error(w, fmt.Sprintf("invalid token: %v", err), http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// --- Minimal JWT validation (RS256 only, no external deps) ---

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type jwtClaims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"`
	Audience audience `json:"aud"`
	Exp      float64  `json:"exp"`
	Iat      float64  `json:"iat"`

	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	Name              string   `json:"name"`
	Groups            []string `json:"groups"`
}

// audience handles the JWT "aud" claim which can be a string or []string.
type audience []string

func (a *audience) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*a = []string{s}
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	*a = arr
	return nil
}

func validateToken(token string, jwks *jwksCache, issuer, aud string) (*UserInfo, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("malformed JWT")
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("decode header: %w", err)
	}

	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, fmt.Errorf("parse header: %w", err)
	}

	if header.Alg != "RS256" {
		return nil, fmt.Errorf("unsupported algorithm: %s", header.Alg)
	}

	// Verify signature
	key, err := jwks.getKey(header.Kid)
	if err != nil {
		return nil, fmt.Errorf("get signing key: %w", err)
	}

	if err := verifyRS256(parts[0]+"."+parts[1], parts[2], key); err != nil {
		return nil, fmt.Errorf("signature verification failed: %w", err)
	}

	// Parse and validate claims
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}

	var claims jwtClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return nil, fmt.Errorf("parse claims: %w", err)
	}

	now := float64(time.Now().Unix())
	if claims.Exp > 0 && now > claims.Exp {
		return nil, fmt.Errorf("token expired")
	}

	if issuer != "" && claims.Issuer != issuer {
		return nil, fmt.Errorf("issuer mismatch: got %q", claims.Issuer)
	}

	if aud != "" && !claims.Audience.contains(aud) {
		return nil, fmt.Errorf("audience mismatch")
	}

	return &UserInfo{
		Subject:  claims.Subject,
		Username: claims.PreferredUsername,
		Email:    claims.Email,
		Name:     claims.Name,
		Groups:   claims.Groups,
	}, nil
}

func (a audience) contains(target string) bool {
	for _, v := range a {
		if v == target {
			return true
		}
	}
	return false
}

// --- JWKS fetching and caching ---

type jwksKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type jwksResponse struct {
	Keys []jwksKey `json:"keys"`
}

type jwksCache struct {
	url  string
	mu   sync.RWMutex
	keys map[string]*rsa.PublicKey
	exp  time.Time
}

const jwksCacheDuration = 1 * time.Hour

func (c *jwksCache) getKey(kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	if time.Now().Before(c.exp) {
		if key, ok := c.keys[kid]; ok {
			c.mu.RUnlock()
			return key, nil
		}
	}
	c.mu.RUnlock()

	// Fetch fresh keys
	if err := c.refresh(); err != nil {
		return nil, err
	}

	c.mu.RLock()
	defer c.mu.RUnlock()
	key, ok := c.keys[kid]
	if !ok {
		return nil, fmt.Errorf("key %q not found in JWKS", kid)
	}
	return key, nil
}

func (c *jwksCache) refresh() error {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(c.url)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS returned %d", resp.StatusCode)
	}

	var jwks jwksResponse
	if err := json.NewDecoder(resp.Body).Decode(&jwks); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey, len(jwks.Keys))
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := parseRSAPublicKey(k)
		if err != nil {
			continue
		}
		keys[k.Kid] = pub
	}

	c.mu.Lock()
	c.keys = keys
	c.exp = time.Now().Add(jwksCacheDuration)
	c.mu.Unlock()

	return nil
}

func parseRSAPublicKey(k jwksKey) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}

	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	if !e.IsInt64() {
		return nil, fmt.Errorf("exponent too large")
	}

	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

func verifyRS256(signingInput, signatureB64 string, key *rsa.PublicKey) error {
	signature, err := base64.RawURLEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("decode signature: %w", err)
	}

	hashed := sha256.Sum256([]byte(signingInput))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, hashed[:], signature)
}
