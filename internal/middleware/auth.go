package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
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

// WithUser attaches a UserInfo to a context. Exported for testing.
func WithUser(ctx context.Context, u *UserInfo) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

// AuthConfig configures the OIDC JWT validation middleware.
type AuthConfig struct {
	// IssuerURL is the OIDC issuer (e.g., "https://auth.example.com/application/o/home-wiki/").
	// Must exactly match the `iss` value returned by the provider's discovery document.
	IssuerURL string

	// Audience is the expected "aud" claim. Typically the OAuth2 client ID. Required.
	Audience string

	// AllowedGroups, if non-empty, is a list of group names; the token's "groups" claim
	// must contain at least one entry from this list. Empty allows any authenticated user.
	AllowedGroups []string
}

// NewAuth builds a JWT validation middleware backed by go-oidc. It performs OIDC
// discovery against cfg.IssuerURL at call time; on failure it returns an error so
// callers can fail fast at startup.
//
// The returned middleware:
//   - Extracts a Bearer token from the Authorization header (401 on missing)
//   - Verifies signature + iss + aud + exp against the provider's JWKS (401 on failure)
//   - Checks AllowedGroups against the token's groups claim when non-empty (403 on miss)
//   - Injects UserInfo into the request context for handlers
//
// Error responses are intentionally generic ("unauthorized" / "forbidden") to avoid
// leaking which claim failed.
func NewAuth(ctx context.Context, cfg AuthConfig) (func(http.Handler) http.Handler, error) {
	if cfg.IssuerURL == "" {
		return nil, errors.New("auth: IssuerURL is required")
	}
	if cfg.Audience == "" {
		return nil, errors.New("auth: Audience is required")
	}
	if err := validateIssuerScheme(cfg.IssuerURL); err != nil {
		return nil, err
	}

	provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("auth: OIDC discovery failed: %w", err)
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: cfg.Audience,
	})

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := extractBearerToken(r)
			if raw == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			tok, err := verifier.Verify(r.Context(), raw)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			var claims struct {
				Subject           string   `json:"sub"`
				PreferredUsername string   `json:"preferred_username"`
				Email             string   `json:"email"`
				Name              string   `json:"name"`
				Groups            []string `json:"groups"`
			}
			if err := tok.Claims(&claims); err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			if len(cfg.AllowedGroups) > 0 && !hasAllowedGroup(claims.Groups, cfg.AllowedGroups) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			user := &UserInfo{
				Subject:  claims.Subject,
				Username: claims.PreferredUsername,
				Email:    claims.Email,
				Name:     claims.Name,
				Groups:   claims.Groups,
			}
			next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), user)))
		})
	}, nil
}

// validateIssuerScheme requires https:// for the issuer URL, with an allow-list
// for loopback hosts so tests using httptest.NewServer can run.
func validateIssuerScheme(issuer string) error {
	u, err := url.Parse(issuer)
	if err != nil {
		return fmt.Errorf("auth: invalid IssuerURL: %w", err)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if host == "127.0.0.1" || host == "localhost" || host == "::1" {
			return nil
		}
		return fmt.Errorf("auth: IssuerURL must use https:// (got %q)", issuer)
	default:
		return fmt.Errorf("auth: IssuerURL must use https:// (got %q)", issuer)
	}
}

func hasAllowedGroup(userGroups, allowed []string) bool {
	for _, g := range userGroups {
		for _, a := range allowed {
			if g == a {
				return true
			}
		}
	}
	return false
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}
