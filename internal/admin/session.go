package admin

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	sessionCookieName = "wiki_admin_session"
	flowCookieName    = "wiki_admin_flow"

	// adminPathPrefix is the route prefix every admin surface lives under.
	adminPathPrefix = "/_/admin"
	// adminRoot is the canonical dashboard path and the safe default for
	// post-login redirects.
	adminRoot = "/_/admin/"

	// flowTTL bounds how long an in-progress login (state/nonce/PKCE) is
	// valid between the authorize redirect and the callback.
	flowTTL = 10 * time.Minute
)

// secretCodec authenticated-encrypts small cookie payloads with AES-256-GCM.
// It supports key rotation: payloads are sealed with the primary (first) key
// and opened by trying every key in order, so a key can be retired without
// invalidating live sessions.
type secretCodec struct {
	aeads []cipher.AEAD
}

// newSecretCodec derives one AES-256-GCM AEAD per secret (key = SHA-256(secret)).
// Each secret must be at least 32 bytes; an empty list is rejected. This fails
// closed so a missing or too-weak key stops startup rather than silently
// producing forgeable cookies.
func newSecretCodec(secrets []string) (*secretCodec, error) {
	if len(secrets) == 0 {
		return nil, errors.New("admin: at least one session key is required")
	}
	c := &secretCodec{}
	for i, s := range secrets {
		if len(s) < 32 {
			return nil, fmt.Errorf("admin: session key #%d is too short (%d bytes); need at least 32", i+1, len(s))
		}
		key := sha256.Sum256([]byte(s))
		block, err := aes.NewCipher(key[:])
		if err != nil {
			return nil, fmt.Errorf("admin: cipher init: %w", err)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, fmt.Errorf("admin: gcm init: %w", err)
		}
		c.aeads = append(c.aeads, aead)
	}
	return c, nil
}

// seal JSON-encodes v, encrypts it with the primary key, and returns a
// base64url token (nonce prepended to the ciphertext).
func (c *secretCodec) seal(v any) (string, error) {
	plaintext, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	aead := c.aeads[0]
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, plaintext, nil)
	return base64.RawURLEncoding.EncodeToString(ct), nil
}

// open decodes and decrypts a token produced by seal into v, trying each key
// in turn (newest first) to tolerate key rotation.
func (c *secretCodec) open(token string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return err
	}
	for _, aead := range c.aeads {
		ns := aead.NonceSize()
		if len(raw) < ns {
			continue
		}
		nonce, ct := raw[:ns], raw[ns:]
		pt, err := aead.Open(nil, nonce, ct, nil)
		if err != nil {
			continue
		}
		return json.Unmarshal(pt, v)
	}
	return errors.New("admin: cookie authentication failed")
}

// session is the authenticated admin identity persisted in the session cookie.
// It carries only the minimal claims needed to render the panel and enforce
// the admin-group gate, plus a per-session CSRF token.
type session struct {
	Subject  string   `json:"sub"`
	Username string   `json:"username,omitempty"`
	Email    string   `json:"email,omitempty"`
	Name     string   `json:"name,omitempty"`
	Groups   []string `json:"groups,omitempty"`
	Expiry   int64    `json:"exp"`  // unix seconds
	CSRF     string   `json:"csrf"` // double-submit token
}

func (s *session) expired() bool { return time.Now().Unix() >= s.Expiry }

// flowState is the short-lived login context stashed in the flow cookie
// between the authorize redirect and the callback.
type flowState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"` // PKCE code verifier
	Return   string `json:"return"`   // sanitized post-login path
	Expiry   int64  `json:"exp"`      // unix seconds
}

func (f *flowState) expired() bool { return time.Now().Unix() >= f.Expiry }

// randomToken returns nBytes of cryptographically random data as a base64url
// string, used for state, nonce, and CSRF tokens.
func randomToken(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// constantTimeEqual reports whether a and b are equal without leaking timing.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// --- cookie helpers ---------------------------------------------------------

func setCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     adminPathPrefix,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		// Lax (not Strict) so the cookie still rides the top-level GET
		// navigation back from the IdP to /_/admin/callback.
		SameSite: http.SameSiteLaxMode,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     adminPathPrefix,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// --- request context --------------------------------------------------------

type ctxKey int

const sessionCtxKey ctxKey = iota

func withSession(ctx context.Context, s *session) context.Context {
	return context.WithValue(ctx, sessionCtxKey, s)
}

func sessionFromContext(ctx context.Context) *session {
	s, _ := ctx.Value(sessionCtxKey).(*session)
	return s
}
