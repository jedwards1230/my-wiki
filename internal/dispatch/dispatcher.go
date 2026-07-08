package dispatch

import (
	"context"
	"net/url"
)

// Dispatcher delivers an Envelope to a single Consumer. Implementations may
// post over HTTP, log, no-op, or buffer. Dispatch returns an error when
// delivery fails in a way the caller should react to; the router logs the
// error and moves on (retry policy is implemented inside the Dispatcher,
// not around it).
type Dispatcher interface {
	Dispatch(ctx context.Context, consumer Consumer, envelope Envelope) error
}

// SanitizeURL returns the URL stripped of userinfo, query, and fragment
// components — suitable for logs. Only scheme, host, and path are retained.
// If the input cannot be parsed, it is returned verbatim (the log still
// ends up with a string; better to see the literal than to swallow it).
// Exported so the HTTP dispatcher can reuse the same redaction logic.
func SanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	// Reconstruct scheme://host/path; dropping User (userinfo),
	// RawQuery/ForceQuery, and Fragment.
	safe := url.URL{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   u.Path,
	}
	return safe.String()
}
