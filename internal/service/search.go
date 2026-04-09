package service

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jedwards1230/home-wiki/internal/search"
)

// SearchService orchestrates search across one or more Searcher backends.
type SearchService struct {
	engines []search.Searcher
	byName  map[string]search.Searcher
}

// NewSearchService creates a SearchService with the given backends.
// The first engine is the default when no engine is specified.
// Panics if two engines share the same Name().
func NewSearchService(engines ...search.Searcher) *SearchService {
	byName := make(map[string]search.Searcher, len(engines))
	for _, e := range engines {
		if _, exists := byName[e.Name()]; exists {
			panic(fmt.Sprintf("duplicate search engine name: %q", e.Name()))
		}
		byName[e.Name()] = e
	}
	return &SearchService{
		engines: engines,
		byName:  byName,
	}
}

// Engines returns the names of all registered search backends.
func (s *SearchService) Engines() []string {
	names := make([]string, len(s.engines))
	for i, e := range s.engines {
		names[i] = e.Name()
	}
	return names
}

// Search runs a query against the specified engine(s).
//   - engine == "": uses the first (default) engine
//   - engine == "all": runs all engines in parallel
//   - engine == "<name>": runs that specific engine
func (s *SearchService) Search(query string, limit int, engine string) (*SearchResponse, error) {
	if engine == "" && len(s.engines) > 0 {
		engine = s.engines[0].Name()
	}

	if engine == "all" {
		return s.searchAll(query, limit)
	}

	eng, ok := s.byName[engine]
	if !ok {
		return nil, fmt.Errorf("unknown search engine %q (available: %s)",
			engine, strings.Join(s.Engines(), ", "))
	}

	start := time.Now()
	results, err := eng.Search(query, limit)
	elapsed := time.Since(start)

	if err != nil {
		return nil, fmt.Errorf("engine %s failed: %w", eng.Name(), err)
	}

	return &SearchResponse{
		Results:   toServiceResults(results),
		Engines:   []string{eng.Name()},
		ElapsedMs: map[string]float64{eng.Name(): float64(elapsed.Microseconds()) / 1000.0},
	}, nil
}

// searchAll runs all engines in parallel and merges results.
func (s *SearchService) searchAll(query string, limit int) (*SearchResponse, error) {
	type engineResult struct {
		name    string
		results []search.Result
		elapsed time.Duration
		err     error
	}

	ch := make(chan engineResult, len(s.engines))
	var wg sync.WaitGroup

	for _, eng := range s.engines {
		wg.Add(1)
		go func(e search.Searcher) {
			defer wg.Done()
			start := time.Now()
			results, err := e.Search(query, limit)
			ch <- engineResult{
				name:    e.Name(),
				results: results,
				elapsed: time.Since(start),
				err:     err,
			}
		}(eng)
	}

	wg.Wait()
	close(ch)

	resp := &SearchResponse{
		Engines:   s.Engines(),
		ElapsedMs: make(map[string]float64, len(s.engines)),
	}

	for er := range ch {
		if er.err != nil {
			return nil, fmt.Errorf("engine %s failed: %w", er.name, er.err)
		}
		resp.Results = append(resp.Results, toServiceResults(er.results)...)
		resp.ElapsedMs[er.name] = float64(er.elapsed.Microseconds()) / 1000.0
	}

	return resp, nil
}

func toServiceResults(results []search.Result) []SearchResult {
	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			Path:    r.Path,
			Title:   r.Title,
			Score:   r.Score,
			Snippet: r.Snippet,
			Match:   r.Match,
			Engine:  r.Engine,
		}
	}
	return out
}
