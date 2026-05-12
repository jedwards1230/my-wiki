package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testServer(ready bool) *Server {
	publicFS := fstest.MapFS{
		"index.html": {Data: []byte("<html>home</html>")},
		"404.html":   {Data: []byte("<html>not found</html>")},
	}
	vaultFS := fstest.MapFS{
		"notes/hello.md": {Data: []byte("# Hello")},
	}
	s := New(Config{}, publicFS, vaultFS, nil)
	if ready {
		s.SetReady()
	}
	return s
}

func TestNotReadyReturns503(t *testing.T) {
	s := testServer(false)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when not ready, got %d", resp.StatusCode)
	}
}

func TestHealthzAlwaysReturns200(t *testing.T) {
	s := testServer(false)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	// healthz is the liveness probe — always returns 200 so K8s doesn't kill the pod
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on healthz even when not ready, got %d", resp.StatusCode)
	}
}

func TestReadyzReturns503WhenNotReady(t *testing.T) {
	s := testServer(false)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on readyz when not ready, got %d", resp.StatusCode)
	}
}

func TestReadyServesContent(t *testing.T) {
	s := testServer(true)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 when ready, got %d", resp.StatusCode)
	}
}

func TestMetricsBypassesReadiness(t *testing.T) {
	s := testServer(false)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on /metrics even when not ready, got %d", resp.StatusCode)
	}
}

func TestReadyzReturns200WhenReady(t *testing.T) {
	s := testServer(true)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on readyz when ready, got %d", resp.StatusCode)
	}
}

func TestStaticAssetsMount(t *testing.T) {
	s := testServer(true)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	// wiki.css is always embedded; verify the route is wired and serves
	// the right MIME type. We don't assert on body to keep the test
	// resilient to css edits.
	resp, err := http.Get(ts.URL + "/_/static/wiki.css")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from /_/static/wiki.css, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct == "" {
		t.Errorf("expected non-empty content-type on wiki.css, got empty")
	}
}

// fixedFragment is a deterministic FragmentRenderer used by the HX-Request
// branch test.
type fixedFragment struct{ body []byte }

func (f *fixedFragment) RenderFragment(_ string) ([]byte, bool) {
	return f.body, true
}

func TestFragmentShim_HXRequest(t *testing.T) {
	publicFS := fstest.MapFS{
		"index.html": {Data: []byte("<html>full page</html>")},
	}
	vaultFS := fstest.MapFS{}
	cfg := Config{FragmentRenderer: &fixedFragment{body: []byte(`<article>fragment</article>`)}}
	s := New(cfg, publicFS, vaultFS, nil)
	s.SetReady()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("HX-Request", "true")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Vary should include HX-Request among (possibly multiple) values
	// like "Accept-Encoding, HX-Request" added by gzip + fragment shim.
	foundVary := false
	for _, v := range resp.Header.Values("Vary") {
		if strings.Contains(v, "HX-Request") {
			foundVary = true
			break
		}
	}
	if !foundVary {
		t.Errorf("expected Vary to include HX-Request, got %v", resp.Header.Values("Vary"))
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if string(buf[:n]) != "<article>fragment</article>" {
		t.Errorf("got body %q, want fragment", string(buf[:n]))
	}
}
