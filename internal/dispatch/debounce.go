package dispatch

import (
	"sort"
	"sync"
	"time"

	"github.com/jedwards1230/home-wiki/internal/notify"
)

// DebounceKey identifies one debounce bucket. The router keys on
// (event, consumer) so each consumer's timer is independent.
type DebounceKey struct {
	Event    EventType
	Consumer string
}

// DebounceBatch is the aggregated result of one debounce cycle, handed to
// the flush callback. Changes is ordered by path for stable HMAC bodies.
type DebounceBatch struct {
	// Changes is the per-path action list, one entry per unique path,
	// sorted by path. When the same path is observed multiple times in a
	// cycle, the latest action wins (reflects the file's terminal state
	// at flush time).
	Changes []notify.PathChange

	// Count is the total number of Observe calls that contributed to this
	// bucket, including duplicates. A burst of 50 rapid writes to the same
	// file yields Count=50 but len(Changes)=1.
	Count int

	// Window is the debounce window that was in effect when the bucket
	// opened. Echoed into Envelope.Coalesced.
	Window time.Duration

	// EarliestAt is the Observe timestamp of the first event in the bucket
	// (i.e., the moment the bucket opened). Echoed into Envelope.Coalesced.
	EarliestAt time.Time
}

// debounceEntry tracks pending paths and the timer for one key. The
// generation counter defeats stale timer callbacks — pattern lifted from
// internal/notify/rebuild.go.
type debounceEntry struct {
	// paths maps path → latest observed action. Last-action-wins when the
	// same path is seen multiple times in the window: reflects whether the
	// file exists at flush time rather than summarizing intermediate state.
	paths    map[string]notify.ChangeKind
	count    int
	earliest time.Time
	window   time.Duration
	timer    *time.Timer
	gen      uint64
}

// Debouncer coalesces per-key events: every Observe(key, window, change)
// call merges the change into the key's bucket and (re)arms a timer for
// window. When the timer fires without further activity the bucket is
// drained and the flush callback is called exactly once with the
// accumulated batch.
//
// Keys are independent — a flurry of activity on one key does not affect
// another. The Debouncer itself is safe for concurrent use.
type Debouncer struct {
	mu      sync.Mutex
	entries map[DebounceKey]*debounceEntry
	flush   func(key DebounceKey, batch DebounceBatch)
	now     func() time.Time
	closed  bool
}

// NewDebouncer returns a debouncer that calls flush when a key's window
// elapses with no new activity. flush is invoked from a timer goroutine;
// it must not block for long. Panics if flush is nil.
func NewDebouncer(flush func(key DebounceKey, batch DebounceBatch)) *Debouncer {
	if flush == nil {
		panic("dispatch: flush must not be nil")
	}
	return &Debouncer{
		entries: make(map[DebounceKey]*debounceEntry),
		flush:   flush,
		now:     time.Now,
	}
}

// Observe records a single path change for key and resets the key's timer
// to window. Subsequent Observe calls before window elapses merge their
// changes into the same bucket and re-arm the timer. Calling Observe after
// Close is a no-op.
func (d *Debouncer) Observe(key DebounceKey, window time.Duration, change notify.PathChange) {
	if change.Path == "" {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return
	}

	entry, ok := d.entries[key]
	if !ok {
		entry = &debounceEntry{
			paths:    make(map[string]notify.ChangeKind),
			earliest: d.now(),
			window:   window,
		}
		d.entries[key] = entry
	}
	entry.paths[change.Path] = change.Action
	entry.count++

	entry.gen++
	curGen := entry.gen
	if entry.timer != nil {
		entry.timer.Stop()
	}
	// Capture key by value so the closure has a stable view; the map
	// lookup inside flushIfCurrent guards against the entry being replaced.
	entry.timer = time.AfterFunc(window, func() {
		d.flushIfCurrent(key, curGen)
	})
}

// flushIfCurrent drains and flushes the bucket for key only if no newer
// Observe has bumped the generation in the meantime.
func (d *Debouncer) flushIfCurrent(key DebounceKey, gen uint64) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	entry, ok := d.entries[key]
	if !ok || entry.gen != gen {
		d.mu.Unlock()
		return
	}
	changes := make([]notify.PathChange, 0, len(entry.paths))
	for p, a := range entry.paths {
		changes = append(changes, notify.PathChange{Path: p, Action: a})
	}
	batch := DebounceBatch{
		Changes:    changes,
		Count:      entry.count,
		Window:     entry.window,
		EarliestAt: entry.earliest,
	}
	// Remove the entry entirely so an idle key doesn't leak memory.
	delete(d.entries, key)
	d.mu.Unlock()

	if len(changes) == 0 {
		return
	}
	// Sort by path before flushing so downstream envelope payloads and
	// HMAC signatures are stable across runs. Map iteration order is
	// deliberately nondeterministic in Go.
	sort.Slice(batch.Changes, func(i, j int) bool { return batch.Changes[i].Path < batch.Changes[j].Path })
	d.flush(key, batch)
}

// Close stops all pending timers without flushing. Intended for graceful
// shutdown where dropping in-flight events is acceptable (Phase 2 policy).
func (d *Debouncer) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return
	}
	d.closed = true
	for _, entry := range d.entries {
		if entry.timer != nil {
			entry.timer.Stop()
		}
	}
	d.entries = map[DebounceKey]*debounceEntry{}
}
