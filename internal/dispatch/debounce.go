package dispatch

import (
	"sort"
	"sync"
	"time"
)

// DebounceKey identifies one debounce bucket. The router keys on
// (event, consumer) so each consumer's timer is independent.
type DebounceKey struct {
	Event    EventType
	Consumer string
}

// debounceEntry tracks pending paths and the timer for one key. The
// generation counter defeats stale timer callbacks — pattern lifted from
// internal/notify/rebuild.go.
type debounceEntry struct {
	paths map[string]struct{}
	timer *time.Timer
	gen   uint64
}

// Debouncer coalesces per-key events: every Observe(key, window, paths)
// call merges paths into the key's bucket and (re)arms a timer for window.
// When the timer fires without further activity the bucket is drained and
// the flush callback is called exactly once with the accumulated path list.
//
// Keys are independent — a flurry of activity on one key does not affect
// another. The Debouncer itself is safe for concurrent use.
type Debouncer struct {
	mu      sync.Mutex
	entries map[DebounceKey]*debounceEntry
	flush   func(key DebounceKey, paths []string)
	closed  bool
}

// NewDebouncer returns a debouncer that calls flush when a key's window
// elapses with no new activity. flush is invoked from a timer goroutine;
// it must not block for long. Panics if flush is nil.
func NewDebouncer(flush func(key DebounceKey, paths []string)) *Debouncer {
	if flush == nil {
		panic("dispatch: flush must not be nil")
	}
	return &Debouncer{
		entries: make(map[DebounceKey]*debounceEntry),
		flush:   flush,
	}
}

// Observe records paths for key and resets the key's timer to window.
// Subsequent Observe calls before window elapses merge their paths into
// the same bucket and re-arm the timer. Calling Observe after Close is a
// no-op.
func (d *Debouncer) Observe(key DebounceKey, window time.Duration, paths ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.closed {
		return
	}

	entry, ok := d.entries[key]
	if !ok {
		entry = &debounceEntry{paths: make(map[string]struct{})}
		d.entries[key] = entry
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		entry.paths[p] = struct{}{}
	}

	entry.gen++
	curGen := entry.gen
	if entry.timer != nil {
		entry.timer.Stop()
	}
	// Capture key and entry by value/pointer so the callback closure has a
	// stable view; the map lookup inside flushIfCurrent guards against the
	// entry being replaced.
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
	paths := make([]string, 0, len(entry.paths))
	for p := range entry.paths {
		paths = append(paths, p)
	}
	// Remove the entry entirely so an idle key doesn't leak memory.
	delete(d.entries, key)
	d.mu.Unlock()

	if len(paths) == 0 {
		return
	}
	// Sort before flushing so downstream envelope payloads (and future
	// HMAC signatures) are stable across runs. Map iteration order is
	// deliberately nondeterministic in Go.
	sort.Strings(paths)
	d.flush(key, paths)
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
