package notify

import (
	"sync"
	"time"
)

// RebuildNotifier batches vault mutations and triggers a single rebuild
// callback after a debounce period of inactivity. This ensures rapid writes
// (e.g. batch page creation) result in one rebuild instead of many.
type RebuildNotifier struct {
	mu       sync.Mutex
	dirty    map[string]struct{}
	timer    *time.Timer
	gen      uint64 // generation counter — stale timer callbacks no-op
	debounce time.Duration
	onFlush  func(paths []string)
}

// New creates a RebuildNotifier that calls onFlush with all dirty paths
// after debounce duration of inactivity following the last MarkDirty call.
// Panics if onFlush is nil.
func New(debounce time.Duration, onFlush func(paths []string)) *RebuildNotifier {
	if onFlush == nil {
		panic("notify: onFlush must not be nil")
	}
	return &RebuildNotifier{
		dirty:    make(map[string]struct{}),
		debounce: debounce,
		onFlush:  onFlush,
	}
}

// MarkDirty records a mutated path and resets the debounce timer. The
// action parameter is accepted to satisfy the Sink interface but ignored
// — rebuild cares about "something changed", not what kind of change.
func (n *RebuildNotifier) MarkDirty(path string, _ ChangeKind) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.dirty[path] = struct{}{}

	// Increment generation so any in-flight timer callback becomes stale.
	n.gen++
	curGen := n.gen

	if n.timer != nil {
		n.timer.Stop()
	}
	n.timer = time.AfterFunc(n.debounce, func() {
		n.flushIfCurrent(curGen)
	})
}

// flushIfCurrent runs flush only if the generation matches (no newer MarkDirty
// calls have been made since this callback was scheduled).
func (n *RebuildNotifier) flushIfCurrent(gen uint64) {
	n.mu.Lock()
	if n.gen != gen {
		n.mu.Unlock()
		return
	}
	n.mu.Unlock()
	n.flush()
}

// flush collects dirty paths, clears the set, and calls onFlush.
func (n *RebuildNotifier) flush() {
	n.mu.Lock()
	if len(n.dirty) == 0 {
		n.mu.Unlock()
		return
	}

	paths := make([]string, 0, len(n.dirty))
	for p := range n.dirty {
		paths = append(paths, p)
	}
	n.dirty = make(map[string]struct{})
	n.mu.Unlock()

	n.onFlush(paths)
}

// Close stops the timer and flushes any remaining dirty paths synchronously.
func (n *RebuildNotifier) Close() {
	n.mu.Lock()
	if n.timer != nil {
		n.timer.Stop()
		n.timer = nil
	}
	n.gen++ // invalidate any in-flight callbacks
	n.mu.Unlock()

	n.flush()
}
