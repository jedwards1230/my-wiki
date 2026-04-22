package dispatch

import (
	"sort"
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
	sort.Strings(paths)
	f.mu.Lock()
	f.events = append(f.events, flushEvent{key: key, paths: paths})
	f.mu.Unlock()
}

func (f *flushCapture) snapshot() []flushEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]flushEvent, len(f.events))
	copy(out, f.events)
	return out
}

func TestDebouncer_SingleObserveFlushesAfterWindow(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	d.Observe(key, 40*time.Millisecond, "inbox/a.md")

	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("flushed too early: %v", got)
	}

	time.Sleep(100 * time.Millisecond)

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

	// After 160ms total, < window * 2, no flush should have occurred yet
	// because each Observe resets the timer.
	time.Sleep(30 * time.Millisecond)
	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("flushed too early: %+v", got)
	}

	time.Sleep(120 * time.Millisecond)
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

	d.Observe(k1, 30*time.Millisecond, "a")
	d.Observe(k2, 30*time.Millisecond, "b")

	time.Sleep(100 * time.Millisecond)
	got := cap.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected 2 flushes, got %d: %+v", len(got), got)
	}
	// Collect by key.
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
	d.Observe(key, 40*time.Millisecond, "inbox/same.md")
	d.Observe(key, 40*time.Millisecond, "inbox/same.md")
	d.Observe(key, 40*time.Millisecond, "inbox/same.md")

	time.Sleep(120 * time.Millisecond)

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d", len(got))
	}
	if len(got[0].paths) != 1 {
		t.Fatalf("expected 1 deduped path, got %v", got[0].paths)
	}
}

func TestDebouncer_CloseDropsPending(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)

	d.Observe(DebounceKey{Event: EventInboxChanged, Consumer: "c1"}, 1*time.Second, "inbox/x.md")
	d.Close()

	// Even after the original window elapses, no flush should fire.
	time.Sleep(50 * time.Millisecond)
	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("expected no flush after Close, got %+v", got)
	}

	// Observe after Close is a no-op.
	d.Observe(DebounceKey{Event: EventInboxChanged, Consumer: "c2"}, 20*time.Millisecond, "y")
	time.Sleep(60 * time.Millisecond)
	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("expected no flush from post-Close Observe, got %+v", got)
	}
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
