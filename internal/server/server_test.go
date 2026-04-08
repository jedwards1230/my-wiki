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
