package dispatch

import (
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/my-wiki/internal/notify"
)

type flushCapture struct {
	mu     sync.Mutex
	events []flushEvent
}

type flushEvent struct {
	key   DebounceKey
	batch DebounceBatch
}

func (f *flushCapture) record(key DebounceKey, batch DebounceBatch) {
	// Defensive copy: the debouncer sorts in place; downstream mutation by
	// the caller (tests below do not, but production might) should not
	// affect what we captured.
	cp := make([]notify.PathChange, len(batch.Changes))
	copy(cp, batch.Changes)
	batch.Changes = cp
	f.mu.Lock()
	f.events = append(f.events, flushEvent{key: key, batch: batch})
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

// pathList extracts the Path field from a Changes slice for assertions.
func pathList(changes []notify.PathChange) []string {
	out := make([]string, len(changes))
	for i, c := range changes {
		out[i] = c.Path
	}
	return out
}

func change(path string, action notify.ChangeKind) notify.PathChange {
	return notify.PathChange{Path: path, Action: action}
}

func TestDebouncer_SingleObserveFlushesAfterWindow(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 40 * time.Millisecond
	d.Observe(key, window, change("inbox/a.md", notify.ChangeModified))

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
	if !equalStrings(pathList(got[0].batch.Changes), []string{"inbox/a.md"}) {
		t.Fatalf("wrong paths: %v", pathList(got[0].batch.Changes))
	}
	if got[0].batch.Changes[0].Action != notify.ChangeModified {
		t.Fatalf("wrong action: %v", got[0].batch.Changes[0].Action)
	}
	if got[0].batch.Count != 1 {
		t.Fatalf("want count=1, got %d", got[0].batch.Count)
	}
	if got[0].batch.Window != window {
		t.Fatalf("want window=%v, got %v", window, got[0].batch.Window)
	}
}

func TestDebouncer_RapidObservesCoalesce(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 80 * time.Millisecond

	d.Observe(key, window, change("inbox/a.md", notify.ChangeCreated))
	time.Sleep(20 * time.Millisecond)
	d.Observe(key, window, change("inbox/b.md", notify.ChangeModified))
	time.Sleep(20 * time.Millisecond)
	d.Observe(key, window, change("inbox/c.md", notify.ChangeDeleted))

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d: %+v", len(got), got)
	}
	want := []string{"inbox/a.md", "inbox/b.md", "inbox/c.md"}
	if !equalStrings(pathList(got[0].batch.Changes), want) {
		t.Fatalf("expected %v, got %v", want, pathList(got[0].batch.Changes))
	}
	if got[0].batch.Count != 3 {
		t.Fatalf("want count=3, got %d", got[0].batch.Count)
	}
}

func TestDebouncer_MultipleKeysIndependent(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	k1 := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	k2 := DebounceKey{Event: EventInboxChanged, Consumer: "c2"}

	window := 30 * time.Millisecond
	d.Observe(k1, window, change("a", notify.ChangeModified))
	d.Observe(k2, window, change("b", notify.ChangeModified))

	waitUntil(t, window*4, func() bool { return cap.count() >= 2 })

	got := cap.snapshot()
	if len(got) != 2 {
		t.Fatalf("expected 2 flushes, got %d: %+v", len(got), got)
	}
	seen := map[string][]string{}
	for _, ev := range got {
		seen[ev.key.Consumer] = pathList(ev.batch.Changes)
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
	d.Observe(key, window, change("inbox/same.md", notify.ChangeCreated))
	d.Observe(key, window, change("inbox/same.md", notify.ChangeModified))
	d.Observe(key, window, change("inbox/same.md", notify.ChangeModified))

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d", len(got))
	}
	if len(got[0].batch.Changes) != 1 {
		t.Fatalf("expected 1 deduped path, got %v", pathList(got[0].batch.Changes))
	}
	// Count reflects all three Observe calls even after dedup.
	if got[0].batch.Count != 3 {
		t.Fatalf("want count=3 (observations), got %d", got[0].batch.Count)
	}
}

// TestDebouncer_LastActionWins verifies the debouncer keeps the most recent
// action per path when a bucket sees the same path with different actions.
// Reflects the file's terminal state at flush time.
func TestDebouncer_LastActionWins(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 40 * time.Millisecond

	// Created → Modified → Deleted within one window: terminal state is
	// Deleted (the file is gone by flush time).
	d.Observe(key, window, change("inbox/p.md", notify.ChangeCreated))
	d.Observe(key, window, change("inbox/p.md", notify.ChangeModified))
	d.Observe(key, window, change("inbox/p.md", notify.ChangeDeleted))

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })
	got := cap.snapshot()[0]
	if len(got.batch.Changes) != 1 {
		t.Fatalf("want 1 change, got %v", got.batch.Changes)
	}
	if got.batch.Changes[0].Action != notify.ChangeDeleted {
		t.Fatalf("want Deleted (last-action-wins), got %v", got.batch.Changes[0].Action)
	}
}

func TestDebouncer_FlushedPathsAreSorted(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 40 * time.Millisecond

	// Observe in reverse-sorted order; expect flush to sort ascending.
	d.Observe(key, window, change("inbox/z.md", notify.ChangeModified))
	d.Observe(key, window, change("inbox/m.md", notify.ChangeModified))
	d.Observe(key, window, change("inbox/a.md", notify.ChangeModified))

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 flush, got %d", len(got))
	}
	want := []string{"inbox/a.md", "inbox/m.md", "inbox/z.md"}
	if !equalStrings(pathList(got[0].batch.Changes), want) {
		t.Fatalf("expected sorted %v, got %v", want, pathList(got[0].batch.Changes))
	}
}

// TestDebouncer_EarliestAtTracksFirstObserve verifies the bucket's
// EarliestAt timestamp points at the first Observe that opened it, not
// subsequent observations.
func TestDebouncer_EarliestAtTracksFirstObserve(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	// Inject a deterministic clock so we can assert exact timestamps.
	fixed := time.Date(2026, 4, 22, 12, 0, 0, 0, time.UTC)
	calls := 0
	d.now = func() time.Time {
		calls++
		return fixed.Add(time.Duration(calls) * time.Second)
	}

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	window := 30 * time.Millisecond

	d.Observe(key, window, change("inbox/a.md", notify.ChangeCreated))
	// d.now only fires when opening a new bucket, so the earliest_at on
	// this bucket is fixed.Add(1s). Subsequent Observe calls on the same
	// bucket do not bump it.
	d.Observe(key, window, change("inbox/b.md", notify.ChangeModified))

	waitUntil(t, window*4, func() bool { return cap.count() >= 1 })
	got := cap.snapshot()[0]
	wantEarliest := fixed.Add(1 * time.Second)
	if !got.batch.EarliestAt.Equal(wantEarliest) {
		t.Fatalf("want earliest=%v, got %v", wantEarliest, got.batch.EarliestAt)
	}
}

// TestDebouncer_WindowFollowsLatestObserve asserts the bucket's Window
// field reflects the window of the most recent Observe call (same value
// the timer is re-armed with) rather than the first call's window. This
// matters on config reload scenarios where a bucket outlives a window
// change.
func TestDebouncer_WindowFollowsLatestObserve(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)
	defer d.Close()

	key := DebounceKey{Event: EventInboxChanged, Consumer: "c1"}
	initial := 200 * time.Millisecond
	shorter := 40 * time.Millisecond

	d.Observe(key, initial, change("inbox/a.md", notify.ChangeCreated))
	// Second Observe arrives quickly with a tighter window; the timer
	// must re-arm at `shorter` and the reported batch window must match.
	time.Sleep(5 * time.Millisecond)
	d.Observe(key, shorter, change("inbox/b.md", notify.ChangeModified))

	waitUntil(t, shorter*4, func() bool { return cap.count() >= 1 })
	got := cap.snapshot()[0]
	if got.batch.Window != shorter {
		t.Fatalf("want window=%v (latest Observe), got %v", shorter, got.batch.Window)
	}
}

func TestDebouncer_CloseDropsPending(t *testing.T) {
	var cap flushCapture
	d := NewDebouncer(cap.record)

	window := 40 * time.Millisecond
	d.Observe(DebounceKey{Event: EventInboxChanged, Consumer: "c1"}, window, change("inbox/x.md", notify.ChangeModified))
	d.Close()

	// Even after a generous wait, no flush should fire.
	waitStable(t, window*3, window*5, func() bool { return cap.count() == 0 })

	// Observe after Close is a no-op.
	d.Observe(DebounceKey{Event: EventInboxChanged, Consumer: "c2"}, window, change("y", notify.ChangeModified))
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
