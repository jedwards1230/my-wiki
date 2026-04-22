package dispatch

import (
	"sync"
	"testing"
	"time"
)

type flushCapture struct {
	mu     sync.Mutex
	events []flushEvent
}

type flushEvent struct {
	key   DebounceKey
	paths []string
}

func (f *flushCapture) record(key DebounceKey, paths []string) {
	// Paths arrive already sorted from the debouncer; copy defensively so
	// later mutations by the caller don't affect what we captured.
	cp := make([]string, len(paths))
	copy(cp, paths)
	f.mu.Lock()
	f.events = append(f.events, flushEvent{key: key, paths: cp})
	f.mu.Unlock()
}

func (f *flushCapture) snapshot() []flushEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]flushEvent, len(f.events))
	copy(out, f.events)
	return out
}

func (f *flushCapture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.events)
}

func TestDebouncer_SingleObserveFlushesAfterWindow(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 40 * time.Millisecond
	d.Observe(key, window, "inbox/a.md")

	if cap.count() != 0 {
		t.Fatal("flushed too early")
	}

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d: %+v", len(got), got)
	}
	if got[0].key != key {
		t.Fatalf("wrong key: %+v", got[0].key)
	}
	if len(got[0].paths) != 1 || got[0].paths[0] != "inbox/a.md" {
		t.Fatalf("wrong paths: %v", got[0].paths)
	}
}

func TestDebouncer_RapidObservesCoalesce(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 80 * time.Millisecond

	d.Observe(key, window, "inbox/a.md")
	time.Sleep(20 * time.Millisecond)
	d.Observe(key, window, "inbox/b.md")
	time.Sleep(20 * time.Millisecond)
	d.Observe(key, window, "inbox/c.md")

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d: %+v", len(got), got)
	}
	want := []string{"inbox/a.md", "inbox/b.md", "inbox/c.md"}
	if !equalStrings(got[0].paths, want) {
		t.Fatalf("expected %v, got %v", want, got[0].paths)
	}
}

func TestDebouncer_MultipleKeysIndependent(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	k1 := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	k2 := DebounceKey{Event: EventInboxChanged, Consumer: "c2"}

	window := 30 * time.Millisecond
	d.Observe(k1, window, "a")
	d.Observe(k2, window, "b")

	waitUntil(t, window*4, func() bool { return cap.count() >= 2 })

	got := cap.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected 2 flushes, got %d: %+v", len(got), got)
	}
	seen := map[string][]string{}
	for _, ev := range got {
		seen[ev.key.Consumer] = ev.paths
	}
	if !equalStrings(seen["c1"], []string{"a"}) {
		t.Errorf("c1: got %v", seen["c1"])
	}
	if !equalStrings(seen["c2"], []string{"b"}) {
		t.Errorf("c2: got %v", seen["c2"])
	}
}

func TestDebouncer_DedupesDuplicatePaths(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 40 * time.Millisecond
	d.Observe(key, window, "inbox/same.md")
	d.Observe(key, window, "inbox/same.md")
	d.Observe(key, window, "inbox/same.md")

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d", len(got))
	}
	if len(got[0].paths) != 1 {
		t.Fatalf("expected 1 deduped path, got %v", got[0].paths)
	}
}

func TestDebouncer_FlushedPathsAreSorted(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 40 * time.Millisecond

	// Observe in reverse-sorted order; expect flush to sort ascending.
	d.Observe(key, window, "inbox/z.md", "inbox/m.md", "inbox/a.md")

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d", len(got))
	}
	want := []string{"inbox/a.md", "inbox/m.md", "inbox/z.md"}
	if !equalStrings(got[0].paths, want) {
		t.Fatalf("expected sorted %v, got %v", want, got[0].paths)
	}
}

func TestDebouncer_CloseDropsPending(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)

	window := 40 * time.Millisecond
	d.Observe(DebounceKey{Event: EventInboxChanged, Consumer: "c1"}, window, "inbox/x.md")
	d.Close()

	// Even after a generous wait, no flush should fire.
	waitStable(t, window*3, window*5, func() bool { return cap.count() == 0 })

	// Observe after Close is a no-op.
	d.Observe(DebounceKey{Event: EventInboxChanged, Consumer: "c2"}, window, "y")
	waitStable(t, window*3, window*5, func() bool { return cap.count() == 0 })
}

func TestDebouncer_NilFlushPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewDebouncer(nil)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
