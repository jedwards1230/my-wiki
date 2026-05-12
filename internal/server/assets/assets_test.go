package assets

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandlerBlocksDirectoryListing ensures requests for directory paths
// return 404 rather than an auto-generated index. Operators must not be
// able to enumerate the asset tree by visiting /_/static/vendor/.
func TestHandlerBlocksDirectoryListing(t *testing.T) {
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	cases := []string{
		"/",
		"/vendor",
		"/vendor/",
		"/fonts/",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			t.Cleanup(func() { _ = resp.Body.Close() })
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET %s: got status %d, want 404", path, resp.StatusCode)
			}
		})
	}
}

// TestHandlerServesEmbeddedFile is a sanity check that real files in the
// bundle are still reachable through the wrapped FS — making sure the
// directory block didn't accidentally swallow file responses.
func TestHandlerServesEmbeddedFile(t *testing.T) {
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/MANIFEST.txt")
	if err != nil {
		t.Fatalf("GET /MANIFEST.txt: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /MANIFEST.txt: got status %d, want 200", resp.StatusCode)
	}
}
