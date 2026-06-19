package search

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/vault"
)

func TestIndexSearcherStats(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "page.md"), []byte("---\ntitle: Page\n---\nhello world content"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewIndexSearcher(vault.New(dir))

	// Before Build: zero docs, never built.
	if st := s.Stats(); st.DocCount != 0 || !st.LastBuilt.IsZero() {
		t.Fatalf("pre-build stats = %+v, want zero", st)
	}

	if err := s.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	st := s.Stats()
	if st.DocCount != 1 {
		t.Fatalf("DocCount = %d, want 1", st.DocCount)
	}
	if st.LastBuilt.IsZero() {
		t.Fatal("LastBuilt should be set after Build")
	}
}
