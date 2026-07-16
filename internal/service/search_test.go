package service

import (
	"testing"

	"github.com/jedwards1230/my-wiki/internal/search"
)

// fakeSearcher is a minimal search.Searcher stub for exercising
// SearchService's engine-selection logic without a real vault/index.
type fakeSearcher struct {
	name    string
	results []search.Result
}

func (f *fakeSearcher) Name() string { return f.name }

func (f *fakeSearcher) Search(_ string, _ int) ([]search.Result, error) {
	return f.results, nil
}

// TestSearchService_DefaultEngine verifies that Search with engine=""
// resolves to the first registered engine — the ordering serve.go relies on
// to make the index engine the default (index registered first, substring
// second) while falling back to substring-only when the index build fails.
func TestSearchService_DefaultEngine(t *testing.T) {
	cases := []struct {
		name    string
		engines []search.Searcher
		want    string
	}{
		{
			name: "index first is default",
			engines: []search.Searcher{
				&fakeSearcher{name: "index"},
				&fakeSearcher{name: "substring"},
			},
			want: "index",
		},
		{
			name: "substring-only fallback when index build failed",
			engines: []search.Searcher{
				&fakeSearcher{name: "substring"},
			},
			want: "substring",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewSearchService(tc.engines...)

			resp, err := svc.Search("query", 10, "")
			if err != nil {
				t.Fatalf("Search() error = %v", err)
			}
			if len(resp.Engines) != 1 || resp.Engines[0] != tc.want {
				t.Errorf("Search(engine=\"\") used engines %v, want [%q]", resp.Engines, tc.want)
			}

			// First registered engine also drives Engines() ordering, which
			// downstream callers (e.g. the "all" merge) rely on.
			if got := svc.Engines()[0]; got != tc.want {
				t.Errorf("Engines()[0] = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSearchService_ExplicitEngineOverridesDefault confirms an explicit
// engine name is honored regardless of registration order.
func TestSearchService_ExplicitEngineOverridesDefault(t *testing.T) {
	svc := NewSearchService(
		&fakeSearcher{name: "index"},
		&fakeSearcher{name: "substring"},
	)

	resp, err := svc.Search("query", 10, "substring")
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(resp.Engines) != 1 || resp.Engines[0] != "substring" {
		t.Errorf("Search(engine=\"substring\") used engines %v, want [substring]", resp.Engines)
	}
}

// TestSearchService_UnknownEngineErrors confirms an unregistered engine name
// fails clearly rather than silently falling back to the default.
func TestSearchService_UnknownEngineErrors(t *testing.T) {
	svc := NewSearchService(&fakeSearcher{name: "index"})

	if _, err := svc.Search("query", 10, "bogus"); err == nil {
		t.Error("Search(engine=\"bogus\") expected error, got nil")
	}
}
