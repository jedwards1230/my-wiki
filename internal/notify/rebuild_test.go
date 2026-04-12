package notify

import (
	"sort"
	"sync"
	"testing"
	"time"
)

func TestSingleMutationFlushesAfterDebounce(t *testing.T) {
	var mu sync.Mutex
	var flushed []string

	n := New(50*time.Millisecond, func(paths []string) {
		mu.Lock()
		flushed = append(flushed, paths...)
		mu.Unlock()
	})
	defer n.Close()

	n.MarkDirty("/vault/test.md")

	// Should not have flushed yet
	mu.Lock()
	if len(flushed) != 0 {
		t.Fatal("flushed too early")
	}
	mu.Unlock()

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 1 || flushed[0] != "/vault/test.md" {
		t.Fatalf("expected [/vault/test.md], got %v", flushed)
	}
}

func TestRapidMutationsBatchIntoOneFlush(t *testing.T) {
	var mu sync.Mutex
	flushCount := 0
	var flushedPaths []string

	n := New(100*time.Millisecond, func(paths []string) {
		mu.Lock()
		flushCount++
		flushedPaths = append(flushedPaths, paths...)
		mu.Unlock()
	})
	defer n.Close()

	// Rapid mutations within debounce window
	n.MarkDirty("/vault/a.md")
	time.Sleep(20 * time.Millisecond)
	n.MarkDirty("/vault/b.md")
	time.Sleep(20 * time.Millisecond)
	n.MarkDirty("/vault/c.md")

	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	if flushCount != 1 {
		t.Fatalf("expected 1 flush, got %d", flushCount)
	}

	sort.Strings(flushedPaths)
	expected := []string{"/vault/a.md", "/vault/b.md", "/vault/c.md"}
	if len(flushedPaths) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, flushedPaths)
	}
	for i := range expected {
		if flushedPaths[i] != expected[i] {
			t.Fatalf("expected %v, got %v", expected, flushedPaths)
		}
	}
}

func TestDuplicatePathDeduplication(t *testing.T) {
	var mu sync.Mutex
	var flushed []string

	n := New(50*time.Millisecond, func(paths []string) {
		mu.Lock()
		flushed = append(flushed, paths...)
		mu.Unlock()
	})
	defer n.Close()

	n.MarkDirty("/vault/same.md")
	n.MarkDirty("/vault/same.md")
	n.MarkDirty("/vault/same.md")

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(flushed) != 1 {
		t.Fatalf("expected 1 path (deduplicated), got %v", flushed)
	}
}

func TestCloseFlushesRemaining(t *testing.T) {
	var flushed []string

	n := New(10*time.Second, func(paths []string) {
		flushed = append(flushed, paths...)
	})

	n.MarkDirty("/vault/pending.md")

	// Close should flush immediately without waiting for timer
	n.Close()

	if len(flushed) != 1 || flushed[0] != "/vault/pending.md" {
		t.Fatalf("expected [/vault/pending.md], got %v", flushed)
	}
}

func TestNoFlushWhenNoDirtyPaths(t *testing.T) {
	flushCount := 0

	n := New(50*time.Millisecond, func(_ []string) {
		flushCount++
	})

	n.Close()

	if flushCount != 0 {
		t.Fatalf("expected 0 flushes, got %d", flushCount)
	}
}
