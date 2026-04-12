package notify

import (
	"sync"
	"time"
)

// RebuildNotifier batches vault mutations and triggers a single rebuild
// callback after a debounce period of inactivity. This ensures rapid writes
// (e.g. batch page creation) result in one Quartz rebuild instead of many.
type RebuildNotifier struct {
	mu       sync.Mutex
	dirty    map[string]struct{}
	timer    *time.Timer
	debounce time.Duration
	onFlush  func(paths []string)
}

// New creates a RebuildNotifier that calls onFlush with all dirty paths
// after debounce duration of inactivity following the last MarkDirty call.
func New(debounce time.Duration, onFlush func(paths []string)) *RebuildNotifier {
	return &RebuildNotifier{
		dirty:    make(map[string]struct{}),
		debounce: debounce,
		onFlush:  onFlush,
	}
}

// MarkDirty records a mutated path and resets the debounce timer.
func (n *RebuildNotifier) MarkDirty(path string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.dirty[path] = struct{}{}

	if n.timer != nil {
		n.timer.Stop()
	}
	n.timer = time.AfterFunc(n.debounce, n.flush)
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
	n.mu.Unlock()

	n.flush()
}
