package dispatch

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"
)

// captureDispatcher records every Dispatch call for assertion. Safe for
// concurrent use: the router calls Dispatch from timer goroutines.
type captureDispatcher struct {
	mu   sync.Mutex
	runs []captureRun
	err  error
}

type captureRun struct {
	Consumer Consumer
	Envelope Envelope
}

func (c *captureDispatcher) Dispatch(_ context.Context, consumer Consumer, envelope Envelope) error {
	// Stable ordering for assertion.
	paths := append([]string{}, envelope.Paths...)
	sort.Strings(paths)
	envelope.Paths = paths

	c.mu.Lock()
	c.runs = append(c.runs, captureRun{Consumer: consumer, Envelope: envelope})
	err := c.err
	c.mu.Unlock()
	return err
}

func (c *captureDispatcher) snapshot() []captureRun {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]captureRun, len(c.runs))
	copy(out, c.runs)
	return out
}

// fastConfig returns a minimal Config with a very short debounce window,
// suitable for tests that need to observe flushes quickly.
func fastConfig(window time.Duration, consumers []Consumer) *Config {
	cfg := &Config{
		Events: map[EventType]EventConfig{
			EventInboxChanged: {Prompt: "inbox-manager"},
		},
		Consumers: consumers,
		Wiki: WikiConfig{
			BaseURL: "https://wiki.example.com",
			MCPURL:  "https://wiki.example.com/mcp",
		},
	}
	cfg.applyDefaults()
	cfg.Defaults.Debounce[EventInboxChanged] = duration{D: window}
	if err := cfg.Validate(); err != nil {
		panic(err)
	}
	return cfg
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEventRouter_NilConfigNoOp(t *testing.T) {
	r := NewEventRouter(nil, nil, quietLogger())
	defer func() { _ = r.Close() }()

	// Must not panic or do anything observable.
	r.RecordMutation(MutationEvent{Kind: "edit", Path: "inbox/x.md"})
	r.RecordInboxFSChange("inbox/y.md")
	r.RecordReconcile([]string{"inbox/z.md"})
}

func TestEventRouter_APIMutationSuppressesFSNotify(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(30*time.Millisecond, []Consumer{
		{
			Name:        "n8n",
			URL:         "https://n8n.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	// API records first.
	r.RecordMutation(MutationEvent{Kind: "edit", Path: "inbox/foo.md"})
	// FS sees the same path immediately afterwards — should be absorbed.
	r.RecordInboxFSChange("inbox/foo.md")

	// Wait for the debounce to flush.
	time.Sleep(120 * time.Millisecond)

	runs := cap.snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected 1 dispatch, got %d: %+v", len(runs), runs)
	}
	if len(runs[0].Envelope.Paths) != 1 || runs[0].Envelope.Paths[0] != "inbox/foo.md" {
		t.Fatalf("expected single path inbox/foo.md, got %v", runs[0].Envelope.Paths)
	}
}

func TestEventRouter_DifferentPathNotSuppressed(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(30*time.Millisecond, []Consumer{
		{
			Name:        "n8n",
			URL:         "https://n8n.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordMutation(MutationEvent{Kind: "edit", Path: "inbox/foo.md"})
	r.RecordInboxFSChange("inbox/bar.md") // different path, should be recorded

	time.Sleep(120 * time.Millisecond)

	runs := cap.snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(runs))
	}
	want := []string{"inbox/bar.md", "inbox/foo.md"}
	if !equalStrings(runs[0].Envelope.Paths, want) {
		t.Fatalf("expected %v, got %v", want, runs[0].Envelope.Paths)
	}
}

func TestEventRouter_ConsumerEventFilterSkipsNonMatching(t *testing.T) {
	cap := &captureDispatcher{}

	// Add a second event type to the config so we can register a consumer
	// that only watches that other event. Validation expects the prompt.
	cfg := &Config{
		Events: map[EventType]EventConfig{
			EventInboxChanged:  {Prompt: "inbox-manager"},
			EventType("other"): {Prompt: "other-prompt"},
		},
		Consumers: []Consumer{
			{
				Name:        "inbox-listener",
				URL:         "https://inbox.example.com/hook",
				Events:      []EventType{EventInboxChanged},
				SecretEnv:   "S",
				PathFilters: PathFilters{Include: []string{"inbox/"}},
			},
			{
				Name:        "other-listener",
				URL:         "https://other.example.com/hook",
				Events:      []EventType{EventType("other")},
				SecretEnv:   "S",
				PathFilters: PathFilters{Include: []string{"inbox/"}},
			},
		},
		Wiki: WikiConfig{
			BaseURL: "https://wiki.example.com",
			MCPURL:  "https://wiki.example.com/mcp",
		},
	}
	cfg.applyDefaults()
	cfg.Defaults.Debounce[EventInboxChanged] = duration{D: 30 * time.Millisecond}
	cfg.Defaults.Debounce[EventType("other")] = duration{D: 30 * time.Millisecond}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordInboxFSChange("inbox/x.md")
	time.Sleep(120 * time.Millisecond)

	runs := cap.snapshot()
	// Both consumers subscribe to events with matching path filters, but
	// RecordInboxFSChange is event-specific — only inbox.changed should fire.
	// Since the "other" event is dispatched via observe() as well (observe
	// fans to all configured events), we want to confirm only the
	// inbox-listener receives because its event list matches the observed event.
	//
	// The router iterates over all events; both events are configured. The
	// consumer filter is "events this consumer subscribes to", so
	// other-listener should be dispatched only when the observation is for
	// the "other" event. RecordInboxFSChange observes as inbox.changed →
	// inbox-listener dispatches, other-listener is skipped.
	if len(runs) != 1 {
		t.Fatalf("expected 1 dispatch, got %d: %+v", len(runs), runs)
	}
	if runs[0].Consumer.Name != "inbox-listener" {
		t.Fatalf("expected inbox-listener, got %s", runs[0].Consumer.Name)
	}
}

func TestEventRouter_IncludeFilterDropsConsumer(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(30*time.Millisecond, []Consumer{
		{
			Name:        "pickier",
			URL:         "https://picky.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/important/"}},
		},
		{
			Name:        "any-inbox",
			URL:         "https://any.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordInboxFSChange("inbox/not-important.md")

	time.Sleep(120 * time.Millisecond)

	runs := cap.snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(runs))
	}
	if runs[0].Consumer.Name != "any-inbox" {
		t.Fatalf("expected any-inbox, got %s", runs[0].Consumer.Name)
	}
}

func TestEventRouter_ExcludeFilter(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(30*time.Millisecond, []Consumer{
		{
			Name:      "x",
			URL:       "https://x.example.com/hook",
			Events:    []EventType{EventInboxChanged},
			SecretEnv: "S",
			PathFilters: PathFilters{
				Include: []string{"inbox/"},
				Exclude: []string{"inbox/drafts/"},
			},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordInboxFSChange("inbox/drafts/wip.md")
	time.Sleep(120 * time.Millisecond)
	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("expected 0 dispatches (excluded), got %d", len(got))
	}

	r.RecordInboxFSChange("inbox/ok.md")
	time.Sleep(120 * time.Millisecond)
	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 dispatch after non-excluded path, got %d", len(got))
	}
}

func TestEventRouter_EnvelopeContainsConfiguredFields(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(30*time.Millisecond, []Consumer{
		{
			Name:        "n8n",
			URL:         "https://n8n.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordMutation(MutationEvent{Kind: "create", Path: "inbox/new.md"})
	time.Sleep(120 * time.Millisecond)

	runs := cap.snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(runs))
	}
	env := runs[0].Envelope
	if env.Event != EventInboxChanged {
		t.Errorf("event: got %q", env.Event)
	}
	if env.Prompt.Name != "inbox-manager" {
		t.Errorf("prompt name: got %q", env.Prompt.Name)
	}
	wantURL := "https://wiki.example.com/meta/prompts/inbox-manager.md"
	if env.Prompt.URL != wantURL {
		t.Errorf("prompt url: got %q want %q", env.Prompt.URL, wantURL)
	}
	if env.Wiki.BaseURL != "https://wiki.example.com" {
		t.Errorf("wiki base_url: got %q", env.Wiki.BaseURL)
	}
	if env.Wiki.MCPURL != "https://wiki.example.com/mcp" {
		t.Errorf("wiki mcp_url: got %q", env.Wiki.MCPURL)
	}
	if env.DeliveryID == "" {
		t.Error("delivery id must be set")
	}
	if env.Timestamp.IsZero() {
		t.Error("timestamp must be set")
	}
}

func TestEventRouter_Reconcile_DispatchesImmediately(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(1*time.Second, []Consumer{
		{
			Name:        "n8n",
			URL:         "https://n8n.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordReconcile([]string{"inbox/stale-1.md", "inbox/stale-2.md", "outside/skip.md"})

	runs := cap.snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(runs))
	}
	if runs[0].Envelope.Source != SourceReconcile {
		t.Errorf("source: got %q", runs[0].Envelope.Source)
	}
	want := []string{"inbox/stale-1.md", "inbox/stale-2.md"}
	if !equalStrings(runs[0].Envelope.Paths, want) {
		t.Fatalf("paths: got %v want %v", runs[0].Envelope.Paths, want)
	}
}

func TestEventRouter_Close_DropsPending(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(500*time.Millisecond, []Consumer{
		{
			Name:        "n8n",
			URL:         "https://n8n.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())

	r.RecordInboxFSChange("inbox/x.md")
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Wait beyond the debounce window; nothing should have flushed.
	time.Sleep(600 * time.Millisecond)
	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("expected no dispatches after Close, got %+v", got)
	}
}

func TestEventRouter_MultipleConsumersSameEvent(t *testing.T) {
	cap := &captureDispatcher{}
	cfg := fastConfig(30*time.Millisecond, []Consumer{
		{
			Name:        "c1",
			URL:         "https://c1.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
		{
			Name:        "c2",
			URL:         "https://c2.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordInboxFSChange("inbox/x.md")
	time.Sleep(120 * time.Millisecond)

	runs := cap.snapshot()
	if len(runs) != 2 {
		t.Fatalf("expected 2 dispatches (fanout), got %d", len(runs))
	}
	seen := map[string]bool{}
	for _, run := range runs {
		seen[run.Consumer.Name] = true
	}
	if !seen["c1"] || !seen["c2"] {
		t.Fatalf("expected both consumers dispatched, got %v", seen)
	}
}

func TestAcceptPath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		filters PathFilters
		want    bool
	}{
		{"no filters", "inbox/a.md", PathFilters{}, true},
		{"include match", "inbox/a.md", PathFilters{Include: []string{"inbox/"}}, true},
		{"include miss", "elsewhere/a.md", PathFilters{Include: []string{"inbox/"}}, false},
		{"exclude match", "inbox/drafts/x.md", PathFilters{Include: []string{"inbox/"}, Exclude: []string{"inbox/drafts/"}}, false},
		{"exclude only, not matching", "a.md", PathFilters{Exclude: []string{"b/"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := acceptPath(tc.path, tc.filters); got != tc.want {
				t.Fatalf("acceptPath(%q): got %v want %v", tc.path, got, tc.want)
			}
		})
	}
}
