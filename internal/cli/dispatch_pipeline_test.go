package cli

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/my-wiki/internal/dispatch"
	"github.com/jedwards1230/my-wiki/internal/notify"
	"github.com/jedwards1230/my-wiki/internal/service"
	"github.com/prometheus/client_golang/prometheus"
)

const validWebhooksYAML = `
defaults:
  debounce:
    inbox.changed: 30ms
events:
  inbox.changed:
    prompt: inbox-manager
consumers:
  - name: test
    url: https://example.com/hook
    events: [inbox.changed]
    path_filters:
      include: ["inbox/"]
    secret_env: WIKI_TEST_HMAC
wiki:
  base_url: https://wiki.example.com
  mcp_url: https://wiki.example.com/mcp
`

// discardLogger returns a slog.Logger that swallows log output — tests
// should not clutter CI with dispatcher chatter. Earlier revision used
// os.NewFile(0, os.DevNull), which wraps stdin (fd 0); any write would
// have gone there. io.Discard is the correct black hole.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// captureDispatcher implements dispatch.Dispatcher by recording every
// delivered envelope. Used to assert pipeline wiring produces the expected
// events without making real HTTP calls.
type captureDispatcher struct {
	mu        sync.Mutex
	envelopes []dispatch.Envelope
}

func (c *captureDispatcher) Dispatch(_ context.Context, _ dispatch.Consumer, env dispatch.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.envelopes = append(c.envelopes, env)
	return nil
}

func (c *captureDispatcher) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.envelopes)
}

func (c *captureDispatcher) snapshot() []dispatch.Envelope {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]dispatch.Envelope, len(c.envelopes))
	copy(out, c.envelopes)
	return out
}

func TestBuildDispatchPipeline_Disabled(t *testing.T) {
	t.Setenv("WIKI_WEBHOOKS_CONFIG", "")
	p, err := buildDispatchPipeline(t.TempDir(), discardLogger(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil pipeline when disabled, got %+v", p)
	}
}

func TestBuildDispatchPipeline_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	t.Setenv("WIKI_WEBHOOKS_CONFIG", path)
	if _, err := buildDispatchPipeline(t.TempDir(), discardLogger(), prometheus.NewRegistry()); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestBuildDispatchPipeline_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "webhooks.yaml")
	if err := os.WriteFile(path, []byte(validWebhooksYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WIKI_TEST_HMAC", "some-secret")
	t.Setenv("WIKI_WEBHOOKS_CONFIG", path)

	p, err := buildDispatchPipeline(t.TempDir(), discardLogger(), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if p.router == nil || p.dispatcher == nil || p.sink == nil || p.closer == nil {
		t.Errorf("pipeline missing fields: %+v", p)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := p.closer(ctx); err != nil {
		t.Errorf("close: %v", err)
	}
}

func TestToVaultRelative(t *testing.T) {
	vault := t.TempDir()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"inbox md", filepath.Join(vault, "inbox", "foo.md"), "inbox/foo.md"},
		{"nested", filepath.Join(vault, "inbox", "a", "b.md"), "inbox/a/b.md"},
		{"non-inbox md", filepath.Join(vault, "meta", "log.md"), "meta/log.md"},
		{"outside vault", filepath.Join("/var/tmp/other-xyz", "file.md"), ""},
		{"relative passthrough", "inbox/foo.md", "inbox/foo.md"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := toVaultRelative(vault, tc.in)
			got = filepath.ToSlash(got)
			if got != tc.want {
				t.Errorf("toVaultRelative(%q) = %q want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestPipelineSink_ForwardsOnlyInbox(t *testing.T) {
	// Build a real router with a capture dispatcher to observe what the
	// sink actually routes. Short debounce so the test isn't slow.
	path := filepath.Join(t.TempDir(), "webhooks.yaml")
	if err := os.WriteFile(path, []byte(validWebhooksYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := dispatch.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cap := &captureDispatcher{}
	router := dispatch.NewEventRouter(cfg, cap, discardLogger())
	defer func() { _ = router.Close() }()

	vaultDir := t.TempDir()
	sink := newPipelineSink(vaultDir, router)
	// Non-inbox paths are dropped by the sink before the router sees them.
	sink.MarkDirty(filepath.Join(vaultDir, "meta", "log.md"), notify.ChangeModified)
	sink.MarkDirty(filepath.Join(vaultDir, "project", "alpha.md"), notify.ChangeModified)
	// Inbox paths should route.
	sink.MarkDirty(filepath.Join(vaultDir, "inbox", "new.md"), notify.ChangeModified)

	// The debounce window is 30ms; wait up to 500ms for a single envelope.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cap.count() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cap.count() != 1 {
		t.Fatalf("expected exactly 1 envelope, got %d", cap.count())
	}
	env := cap.snapshot()[0]
	if len(env.Paths) != 1 || env.Paths[0] != "inbox/new.md" {
		t.Errorf("envelope paths: %v", env.Paths)
	}
}

func TestMutationAdapter_CallsBaseAndRouter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "webhooks.yaml")
	if err := os.WriteFile(path, []byte(validWebhooksYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := dispatch.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cap := &captureDispatcher{}
	router := dispatch.NewEventRouter(cfg, cap, discardLogger())
	defer func() { _ = router.Close() }()

	var baseCalls int
	base := func(evt service.MutationEvent) {
		baseCalls++
		if evt.Path != "inbox/new.md" {
			t.Errorf("base got path %q", evt.Path)
		}
	}

	cb := mutationAdapter(router, base)
	cb(service.MutationEvent{Kind: service.MutationCreate, Path: "inbox/new.md"})

	if baseCalls != 1 {
		t.Errorf("base called %d times, want 1", baseCalls)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cap.count() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if cap.count() != 1 {
		t.Fatalf("router envelope count: %d", cap.count())
	}
	env := cap.snapshot()[0]
	if env.Event != dispatch.EventInboxChanged {
		t.Errorf("event type: %q", env.Event)
	}
}

func TestMutationAdapter_NilBaseIsOK(t *testing.T) {
	cb := mutationAdapter(nil, nil)
	// Should not panic even with everything nil.
	cb(service.MutationEvent{Kind: service.MutationCreate, Path: "inbox/x.md"})
}

func TestScanInboxForReconcile(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vaultDir, "inbox", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"inbox/a.md", "inbox/b.md", "inbox/nested/c.md"} {
		if err := os.WriteFile(filepath.Join(vaultDir, filepath.FromSlash(p)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Non-md file should be ignored.
	if err := os.WriteFile(filepath.Join(vaultDir, "inbox", "skip.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	paths := scanInboxForReconcile(vaultDir, discardLogger())
	if len(paths) != 3 {
		t.Fatalf("expected 3 paths, got %d: %v", len(paths), paths)
	}
	// All paths must be vault-relative with forward slashes and begin with
	// "inbox/".
	for _, p := range paths {
		if filepath.IsAbs(p) {
			t.Errorf("path is absolute: %s", p)
		}
		if len(p) < 6 || p[:6] != "inbox/" {
			t.Errorf("path missing inbox/ prefix: %s", p)
		}
	}
}

func TestScanInboxForReconcile_NoInbox(t *testing.T) {
	paths := scanInboxForReconcile(t.TempDir(), discardLogger())
	if len(paths) != 0 {
		t.Errorf("expected 0 paths for missing inbox, got %v", paths)
	}
}
