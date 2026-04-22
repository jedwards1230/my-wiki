package dispatch

import (
	"context"
	"log/slog"
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

// LoggingDispatcher is the Phase 2 stand-in Dispatcher. It logs enough
// structured fields to confirm routing decisions during development and
// testing. Phase 3 replaces it with an HTTP + HMAC implementation.
type LoggingDispatcher struct {
	logger *slog.Logger
}

// NewLoggingDispatcher returns a LoggingDispatcher using the given logger.
// If logger is nil, slog.Default is used.
func NewLoggingDispatcher(logger *slog.Logger) *LoggingDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &LoggingDispatcher{logger: logger}
}

// Dispatch logs the envelope summary and returns nil. It performs no
// network calls; it's a safe default when no HTTP dispatcher is wired.
// The consumer URL is sanitized before logging so credentials embedded
// in userinfo or tokens in query strings do not leak into log sinks.
func (d *LoggingDispatcher) Dispatch(_ context.Context, consumer Consumer, envelope Envelope) error {
	d.logger.Info("dispatch.stub",
		slog.String("consumer_name", consumer.Name),
		slog.String("consumer_url", SanitizeURL(consumer.URL)),
		slog.String("event", string(envelope.Event)),
		slog.String("delivery_id", envelope.DeliveryID),
		slog.Int("paths_count", len(envelope.Paths)),
		slog.String("source", string(envelope.Source)),
	)
	return nil
}

// SanitizeURL returns the URL stripped of userinfo, query, and fragment
// components — suitable for logs. Only scheme, host, and path are retained.
// If the input cannot be parsed, it is returned verbatim (the log still
// ends up with a string; better to see the literal than to swallow it).
// Exported so Phase 3's HTTP dispatcher can reuse the same redaction logic.
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
