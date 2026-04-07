package server

import (
	"net/http"
	"net/http/httptest"
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
	s := New(Config{}, publicFS, vaultFS)
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when not ready, got %d", resp.StatusCode)
	}
}

func TestHealthzBypassesReadiness(t *testing.T) {
	s := testServer(false)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// healthz should return 200 even when not ready, so K8s can see the pod is alive
	// But it should return 503 so K8s readiness probe keeps traffic away until content is built
	// Decision: healthz returns 503 when not ready (aligns K8s readiness with content readiness)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on healthz when not ready, got %d", resp.StatusCode)
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
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 when ready, got %d", resp.StatusCode)
	}
}

func TestReadyHealthz(t *testing.T) {
	s := testServer(true)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on healthz when ready, got %d", resp.StatusCode)
	}
}
