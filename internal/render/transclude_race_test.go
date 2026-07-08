package render

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jedwards1230/my-wiki/internal/memfs"
	"github.com/jedwards1230/my-wiki/internal/vault"
)

var anchorOpenRE = regexp.MustCompile(`<a[ >]`)

// TestTranscludeConcurrentSharedNode is a regression test for the transclusion
// enter/exit race. The AST cache is shared across the Builder's concurrent
// pass-2 renders (errgroup, SetLimit(GOMAXPROCS)). When many pages transclude
// the SAME target — and that target contains a wikilink — every render walks
// the SAME *wikilink.Node pointer. A process-global "did we open an <a>?" map
// keyed by that pointer let concurrent enter/exit interleave and drop a
// closing </a>, corrupting the HTML. Scoping that bookkeeping per
// transcludeRenderer instance (one per page render) fixes it.
//
// The test builds a real Builder over a multi-page vault so the errgroup path
// and shared cache are exercised, then asserts every rendered host page has
// balanced <a>/</a> tags. It is meaningful under `go test -race`: if the
// per-instance map were ever shared across goroutines, the race detector would
// flag the concurrent map access directly.
func TestTranscludeConcurrentSharedNode(t *testing.T) {
	dir := t.TempDir()

	// The transclusion target contains a resolvable wikilink, so rendering it
	// emits an <a>…</a> pair whose enter/exit bookkeeping is under test.
	write := func(rel, body string) {
		t.Helper()
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write("linked.md", "---\ntitle: Linked\n---\n\nDestination page.\n")
	write("shared.md", "---\ntitle: Shared\n---\n\nShared body links to [[linked|the linked page]].\n")

	// Many host pages all transclude the same target so several concurrent
	// renders hit the shared node pointer at once.
	const hostCount = 24
	hostSlugs := make([]string, 0, hostCount)
	for i := 0; i < hostCount; i++ {
		slug := fmt.Sprintf("host%02d", i)
		hostSlugs = append(hostSlugs, slug)
		write(slug+".md", fmt.Sprintf("---\ntitle: Host %d\n---\n\nBefore. ![[shared]] After.\n", i))
	}

	b := NewBuilder(BuilderConfig{
		Vault:     vault.New(dir),
		SiteTitle: "Race Test",
		BaseURL:   "https://wiki.test",
	})
	snap, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	fs := memfs.New()
	fs.Store(snap)

	for _, slug := range hostSlugs {
		key := slug + "/index.html"
		f, err := fs.Open(key)
		if err != nil {
			t.Fatalf("missing %s: %v", key, err)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		html := string(data)

		// Sanity: the transclusion actually expanded and the shared wikilink
		// rendered as an anchor — otherwise the test isn't exercising the path.
		if !strings.Contains(html, `class="transclude"`) {
			t.Fatalf("%s: transclude did not expand:\n%s", slug, html)
		}

		opens := len(anchorOpenRE.FindAllString(html, -1))
		closes := strings.Count(html, "</a>")
		if opens != closes {
			t.Errorf("%s: unbalanced anchor tags — %d opening <a>, %d closing </a> (dropped tag = corrupted HTML)", slug, opens, closes)
		}
	}
}
