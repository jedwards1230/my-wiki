package search

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestIndexBuildAndSearch(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("kubernetes", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected results for 'kubernetes'")
	}

	if results[0].Engine != "index" {
		t.Errorf("expected engine=index, got %q", results[0].Engine)
	}
}

func TestIndexSearchMultiToken(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("alpha kubernetes", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected results for multi-token query")
	}

	// Alpha Project should rank highest since it has both tokens
	if results[0].Path != "project/alpha.md" {
		t.Errorf("expected Alpha to rank first, got %s", results[0].Path)
	}
}

func TestIndexSearchNoResults(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("zzzznonexistent", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestIndexSearchBeforeBuild(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	// Search before Build should return nil, not panic
	results, err := idx.Search("test", 10)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("expected nil results before build, got %d", len(results))
	}
}

func TestIndexRebuildNoDuplicates(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	first, _ := idx.Search("wiki", 20)

	// Rebuild
	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	second, _ := idx.Search("wiki", 20)

	if len(first) != len(second) {
		t.Errorf("rebuild changed result count: %d → %d", len(first), len(second))
	}
}

func TestIndexSearchLimit(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("wiki", 1)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) > 1 {
		t.Errorf("expected at most 1 result with limit=1, got %d", len(results))
	}
}

func TestIndexTitleWeightedHigher(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("alpha", 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) == 0 {
		t.Fatal("expected results for 'alpha'")
	}

	// Alpha Project has "Alpha" in the title — should rank highest
	if results[0].Path != "project/alpha.md" {
		t.Errorf("expected title match to rank first, got %s", results[0].Path)
	}
}

func TestIndexConcurrentReadDuringRebuild(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	// Concurrent reads and writes should not panic
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = idx.Search("kubernetes", 5)
		}()
		go func() {
			defer wg.Done()
			_ = idx.Build()
		}()
	}
	wg.Wait()
}

func TestIndexAutoRebuild(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	idx.StartAutoRebuild(ctx, 50*time.Millisecond)

	// Wait for at least one rebuild
	time.Sleep(100 * time.Millisecond)

	results, err := idx.Search("wiki", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("expected auto-rebuild to populate index")
	}

	// Cancel should stop the goroutine
	cancel()
	time.Sleep(100 * time.Millisecond)
}

func TestIndexExcludesActivityLogs(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if err := idx.Build(); err != nil {
		t.Fatal(err)
	}

	results, err := idx.Search("create", 20)
	if err != nil {
		t.Fatal(err)
	}

	for _, r := range results {
		if r.Path == "meta/activity/2026-04-06.md" {
			t.Error("activity log files should be excluded from index")
		}
	}
}

func TestIndexName(t *testing.T) {
	v := setupTestVault(t)
	idx := NewIndexSearcher(v)

	if idx.Name() != "index" {
		t.Errorf("expected name 'index', got %q", idx.Name())
	}
}
