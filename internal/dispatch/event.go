// Package dispatch implements the webhook dispatcher pipeline: events are
// observed from multiple producers (filesystem watchers, API mutation hooks,
// startup reconciles), debounced per (event, consumer) pair, and eventually
// handed to a Dispatcher implementation for delivery.
package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/jedwards1230/home-wiki/internal/notify"
)

// SchemaVersion is the value stamped into Envelope.SchemaVersion. It lets
// consumers detect format drift. Bump only when the envelope shape changes
// in a way consumers could misinterpret; additive optional fields do not
// count.
const SchemaVersion = "1"

// EventType is the string-serialized identifier for a dispatched event.
// Using a string type keeps JSON/YAML round-trips obvious and makes future
// additions (e.g. directory changes, lint events) cheap.
type EventType string

const (
	// EventInboxChanged fires when files under the vault's inbox area change.
	// The v1 webhook dispatcher only supports this event type.
	EventInboxChanged EventType = "inbox.changed"
)

// Source identifies the producer that observed an event.
type Source string

const (
	// SourceFsnotify is the filesystem watcher feed.
	SourceFsnotify Source = "fsnotify"
	// SourceAPI is the REST/MCP API mutation callback.
	SourceAPI Source = "api"
	// SourceReconcile is the startup scan that catches pending inbox files.
	SourceReconcile Source = "reconcile"
)

// Event is the internal representation of something that happened in the
// vault. It is not serialized over the wire; the outbound envelope is built
// per-consumer by the router.
type Event struct {
	Type      EventType
	Paths     []string
	Source    Source
	Timestamp time.Time
}

// PromptRef is the reference to the Claude prompt a consumer should load to
// handle this event. Consumers fetch the URL to retrieve the prompt markdown.
type PromptRef struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// WikiLocators identifies the wiki instance that produced the event so a
// consumer can call back into it (e.g. via MCP) without being pre-configured.
type WikiLocators struct {
	BaseURL string `json:"base_url"`
	MCPURL  string `json:"mcp_url"`
}

// CoalesceInfo describes the burst of producer events collapsed into a
// single outbound delivery. Consumers can use it to reason about load, to
// decide between per-file vs batch processing, or to log diagnostics.
type CoalesceInfo struct {
	// Count is the total number of producer observations merged into this
	// delivery, including duplicates on the same path.
	Count int `json:"count"`

	// WindowSeconds is the debounce window that was in effect, in seconds.
	// Paired with EarliestAt it gives consumers a rough burst signature.
	WindowSeconds float64 `json:"window_seconds"`

	// EarliestAt is the timestamp of the first observation that opened this
	// debounce bucket.
	EarliestAt time.Time `json:"earliest_at"`
}

// Envelope is the JSON payload posted to each consumer. Field tags match the
// cross-service spec exactly; do not rename without coordinating with the
// consumer side.
//
// Paths is the legacy v1 field — a de-duplicated, sorted list of paths. It
// is populated for back-compat with consumers that were written against the
// original envelope; new consumers should prefer Changes, which carries
// per-path action information.
type Envelope struct {
	SchemaVersion string              `json:"schema_version"`
	DeliveryID    string              `json:"delivery_id"`
	Event         EventType           `json:"event"`
	Timestamp     time.Time           `json:"timestamp"`
	Source        Source              `json:"source"`
	Paths         []string            `json:"paths,omitempty"`
	Changes       []notify.PathChange `json:"changes,omitempty"`
	Coalesced     *CoalesceInfo       `json:"coalesced,omitempty"`
	Prompt        PromptRef           `json:"prompt"`
	Wiki          WikiLocators        `json:"wiki"`
}

// NewDeliveryID returns a random opaque identifier suitable for the
// Envelope.DeliveryID field. It uses 16 bytes from crypto/rand hex-encoded,
// which is plenty unique for webhook deliveries without pulling in a ULID
// dependency.
func NewDeliveryID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand.Read on Linux/macOS only fails under catastrophic
		// conditions (no entropy source). Fall back to a timestamp so the
		// caller still gets a non-empty ID for logging, but this branch is
		// effectively unreachable.
		return "err-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
