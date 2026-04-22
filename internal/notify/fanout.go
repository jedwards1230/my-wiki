package notify

import "sync"

// FanoutSink forwards every MarkDirty call to each registered Sink. It is
// safe for concurrent use: MarkDirty may be called from multiple goroutines
// while sinks are added via Add. Order of delivery matches registration
// order (slice, not map).
//
// FanoutSink itself satisfies Sink, so it can be composed with other sinks.
type FanoutSink struct {
	mu    sync.RWMutex
	sinks []Sink
}

// NewFanoutSink returns a FanoutSink pre-populated with the given sinks.
func NewFanoutSink(sinks ...Sink) *FanoutSink {
	return &FanoutSink{sinks: append([]Sink(nil), sinks...)}
}

// Add registers an additional sink. Calls made after Add receive the new
// sink; calls already in flight are unaffected.
func (f *FanoutSink) Add(sink Sink) {
	f.mu.Lock()
	f.sinks = append(f.sinks, sink)
	f.mu.Unlock()
}

// MarkDirty forwards path to every registered sink, in registration order.
// It takes a snapshot of the sink slice under a read lock so downstream
// sinks can block or hop goroutines without stalling concurrent callers.
func (f *FanoutSink) MarkDirty(path string) {
	f.mu.RLock()
	snapshot := f.sinks
	f.mu.RUnlock()

	for _, s := range snapshot {
		s.MarkDirty(path)
	}
}
