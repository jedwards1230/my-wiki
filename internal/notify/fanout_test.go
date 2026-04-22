package notify

import (
	"fmt"
	"sync"
	"testing"
)

// recordingSink is a test helper Sink that records every path it receives.
type recordingSink struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingSink) MarkDirty(path string) {
	r.mu.Lock()
	r.paths = append(r.paths, path)
	r.mu.Unlock()
}

func (r *recordingSink) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.paths))
	copy(out, r.paths)
	return out
}

func (r *recordingSink) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

func TestFanoutSinkZeroSinks(t *testing.T) {
	// Both constructor forms should be no-op safe.
	f := NewFanoutSink()
	f.MarkDirty("some/path")

	// Zero-value FanoutSink must also be safe — no panic, no behavior.
	var zero FanoutSink
	zero.MarkDirty("other/path")
}

func TestFanoutSinkSingleSink(t *testing.T) {
	r := &recordingSink{}
	f := NewFanoutSink(r)

	f.MarkDirty("a.md")

	got := r.snapshot()
	if len(got) != 1 || got[0] != "a.md" {
		t.Fatalf("expected [a.md], got %v", got)
	}
}

func TestFanoutSinkMultipleSinksPreservesOrder(t *testing.T) {
	// Use a shared recorder that logs which sink received the path in order,
	// so we can verify fanout iterates sinks in registration order.
	var mu sync.Mutex
	var order []string

	makeSink := func(name string) Sink {
		return sinkFunc(func(path string) {
			mu.Lock()
			order = append(order, name+":"+path)
			mu.Unlock()
		})
	}

	f := NewFanoutSink(makeSink("first"), makeSink("second"), makeSink("third"))
	f.MarkDirty("x.md")

	mu.Lock()
	defer mu.Unlock()
	want := []string{"first:x.md", "second:x.md", "third:x.md"}
	if len(order) != len(want) {
		t.Fatalf("expected %d deliveries, got %d: %v", len(want), len(order), order)
	}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("delivery %d: want %q, got %q (full: %v)", i, w, order[i], order)
		}
	}
}

func TestFanoutSinkConcurrent(t *testing.T) {
	const (
		numSinks      = 4
		numGoroutines = 16
		callsPerG     = 50
	)

	sinks := make([]*recordingSink, numSinks)
	forwardees := make([]Sink, numSinks)
	for i := range sinks {
		sinks[i] = &recordingSink{}
		forwardees[i] = sinks[i]
	}
	f := NewFanoutSink(forwardees...)

	var wg sync.WaitGroup
	wg.Add(numGoroutines)
	for g := 0; g < numGoroutines; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < callsPerG; i++ {
				f.MarkDirty(fmt.Sprintf("g%d/%d.md", g, i))
			}
		}(g)
	}
	wg.Wait()

	wantTotal := numGoroutines * callsPerG
	for i, r := range sinks {
		if got := r.len(); got != wantTotal {
			t.Errorf("sink[%d]: want %d calls, got %d", i, wantTotal, got)
		}
	}
}

func TestFanoutSinkAddRegistersForFuturesOnly(t *testing.T) {
	first := &recordingSink{}
	f := NewFanoutSink(first)

	f.MarkDirty("before.md")

	second := &recordingSink{}
	f.Add(second)

	f.MarkDirty("after.md")

	firstGot := first.snapshot()
	if len(firstGot) != 2 || firstGot[0] != "before.md" || firstGot[1] != "after.md" {
		t.Fatalf("first sink: want [before.md after.md], got %v", firstGot)
	}

	secondGot := second.snapshot()
	if len(secondGot) != 1 || secondGot[0] != "after.md" {
		t.Fatalf("second sink: want [after.md], got %v", secondGot)
	}
}

func TestFanoutSinkSkipsNilSinks(t *testing.T) {
	s1 := &recordingSink{}
	s2 := &recordingSink{}

	// NewFanoutSink: nil sinks interspersed with real ones are filtered out.
	f := NewFanoutSink(s1, nil, s2)

	// Add(nil) is a no-op; must not panic and must not grow the sink slice.
	f.Add(nil)

	f.MarkDirty("x.md")

	if got := s1.snapshot(); len(got) != 1 || got[0] != "x.md" {
		t.Fatalf("s1: want [x.md], got %v", got)
	}
	if got := s2.snapshot(); len(got) != 1 || got[0] != "x.md" {
		t.Fatalf("s2: want [x.md], got %v", got)
	}
}

// sinkFunc adapts a plain function into a Sink for tests.
type sinkFunc func(path string)

func (f sinkFunc) MarkDirty(path string) { f(path) }
