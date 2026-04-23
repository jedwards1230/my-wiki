package dispatch

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jedwards1230/home-wiki/internal/notify"
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
	// Copy + sort the paths slice defensively for deterministic assertion.
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

func (c *captureDispatcher) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.runs)
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
	r.RecordInboxFSChange("inbox/y.md", notify.ChangeModified)
	r.RecordReconcile([]string{"inbox/z.md"})
}

func TestEventRouter_APIMutationSuppressesFSNotify(t *testing.T) {
	cap := &captureDispatcher{}
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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
	r.RecordInboxFSChange("inbox/foo.md", notify.ChangeModified)

	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })

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
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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
	r.RecordInboxFSChange("inbox/bar.md", notify.ChangeModified) // different path, should be recorded

	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })

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

	window := 30 * time.Millisecond
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
	cfg.Defaults.Debounce[EventInboxChanged] = duration{D: window}
	cfg.Defaults.Debounce[EventType("other")] = duration{D: window}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	r := NewEventRouter(cfg, cap, quietLogger())
	defer func() { _ = r.Close() }()

	r.RecordInboxFSChange("inbox/x.md", notify.ChangeModified)

	// Wait long enough that any (incorrect) dispatch to other-listener
	// would have surfaced, then assert only inbox-listener received.
	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })
	// Sleep briefly to give any spurious extra dispatch a chance to appear.
	time.Sleep(window * 2)

	runs := cap.snapshot()
	if len(runs) != 1 {
		t.Fatalf("expected 1 dispatch, got %d: %+v", len(runs), runs)
	}
	if runs[0].Consumer.Name != "inbox-listener" {
		t.Fatalf("expected inbox-listener, got %s", runs[0].Consumer.Name)
	}
}

func TestEventRouter_IncludeFilterDropsConsumer(t *testing.T) {
	cap := &captureDispatcher{}
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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

	r.RecordInboxFSChange("inbox/not-important.md", notify.ChangeModified)

	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })
	// Ensure no second dispatch sneaks in.
	time.Sleep(window * 2)

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
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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

	r.RecordInboxFSChange("inbox/drafts/wip.md", notify.ChangeModified)
	// Give it a bounded chance to (incorrectly) dispatch.
	waitStable(t, window*3, window*5, func() bool { return cap.count() == 0 })

	r.RecordInboxFSChange("inbox/ok.md", notify.ChangeModified)
	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })

	got := cap.snapshot()
	if len(got) != 1 {
		t.Fatalf("expected 1 dispatch after non-excluded path, got %d", len(got))
	}
}

func TestEventRouter_EnvelopeContainsConfiguredFields(t *testing.T) {
	cap := &captureDispatcher{}
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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
	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })

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
	// Envelope v2: schema version, per-path action, coalesce info.
	if env.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version: got %q want %q", env.SchemaVersion, SchemaVersion)
	}
	if len(env.Changes) != 1 {
		t.Fatalf("expected 1 change, got %d: %+v", len(env.Changes), env.Changes)
	}
	if env.Changes[0].Path != "inbox/new.md" {
		t.Errorf("change path: got %q", env.Changes[0].Path)
	}
	if env.Changes[0].Action != notify.ChangeCreated {
		t.Errorf("change action: got %v want ChangeCreated (mutation kind %q)", env.Changes[0].Action, "create")
	}
	// Paths is the v1 back-compat view; must mirror Changes[].Path.
	if len(env.Paths) != 1 || env.Paths[0] != "inbox/new.md" {
		t.Errorf("paths back-compat: got %v", env.Paths)
	}
	if env.Coalesced == nil {
		t.Fatal("coalesced must be populated for a debounced dispatch")
	}
	if env.Coalesced.Count < 1 {
		t.Errorf("coalesced.count: got %d, want >=1", env.Coalesced.Count)
	}
	if env.Coalesced.WindowSeconds <= 0 {
		t.Errorf("coalesced.window_seconds: got %f", env.Coalesced.WindowSeconds)
	}
	if env.Coalesced.EarliestAt.IsZero() {
		t.Error("coalesced.earliest_at must be set")
	}
}

// TestEventRouter_MoveMutationEmitsDeleteAndCreate verifies that a move
// mutation (evt.From set) is split into two observations — delete on the
// source and create on the destination — and that the follow-up fsnotify
// events for both paths are absorbed by the dedupe cache.
func TestEventRouter_MoveMutationEmitsDeleteAndCreate(t *testing.T) {
	cap := &captureDispatcher{}
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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

	// Move inbox/old.md -> inbox/new.md via the API; the fsnotify watcher
	// would later report Rename on old and Create on new, both of which
	// must be dedupe-absorbed so we end with exactly two changes.
	r.RecordMutation(MutationEvent{Kind: "move", Path: "inbox/new.md", From: "inbox/old.md"})
	r.RecordInboxFSChange("inbox/old.md", notify.ChangeDeleted) // absorbed by dedupe
	r.RecordInboxFSChange("inbox/new.md", notify.ChangeCreated) // absorbed by dedupe

	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })
	env := cap.snapshot()[0].Envelope

	if len(env.Changes) != 2 {
		t.Fatalf("expected 2 changes (source delete + dest create), got %d: %+v", len(env.Changes), env.Changes)
	}
	// Changes sort ascending by path: new.md before old.md.
	byPath := map[string]notify.ChangeKind{}
	for _, c := range env.Changes {
		byPath[c.Path] = c.Action
	}
	if byPath["inbox/old.md"] != notify.ChangeDeleted {
		t.Errorf("source action: got %v want ChangeDeleted", byPath["inbox/old.md"])
	}
	if byPath["inbox/new.md"] != notify.ChangeCreated {
		t.Errorf("destination action: got %v want ChangeCreated", byPath["inbox/new.md"])
	}
}

func TestEventRouter_EnvelopePathsMirrorChangesForBackCompat(t *testing.T) {
	cap := &captureDispatcher{}
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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

	r.RecordInboxFSChange("inbox/b.md", notify.ChangeModified)
	r.RecordInboxFSChange("inbox/a.md", notify.ChangeCreated)
	r.RecordInboxFSChange("inbox/c.md", notify.ChangeDeleted)
	waitUntil(t, window*5, func() bool { return cap.count() >= 1 })

	env := cap.snapshot()[0].Envelope
	// Changes are sorted ascending by path; Paths must be the exact
	// per-element projection.
	if len(env.Changes) != len(env.Paths) {
		t.Fatalf("len mismatch: Changes=%d Paths=%d", len(env.Changes), len(env.Paths))
	}
	for i := range env.Changes {
		if env.Changes[i].Path != env.Paths[i] {
			t.Errorf("index %d: Changes[].Path=%q Paths[]=%q", i, env.Changes[i].Path, env.Paths[i])
		}
	}
	// Sorted by path: a, b, c; actions preserved as observed.
	wantActions := []notify.ChangeKind{notify.ChangeCreated, notify.ChangeModified, notify.ChangeDeleted}
	for i, want := range wantActions {
		if env.Changes[i].Action != want {
			t.Errorf("index %d: action=%v want %v", i, env.Changes[i].Action, want)
		}
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

	// Reconcile is synchronous — no need to wait.
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
	window := 50 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
		{
			Name:        "n8n",
			URL:         "https://n8n.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())

	r.RecordInboxFSChange("inbox/x.md", notify.ChangeModified)
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Bounded wait: nothing must flush even after the window elapses.
	waitStable(t, window*3, window*6, func() bool { return cap.count() == 0 })
}

func TestEventRouter_Close_BlocksSubsequentRecordCalls(t *testing.T) {
	cap := &captureDispatcher{}
	window := 40 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
		{
			Name:        "n8n",
			URL:         "https://n8n.example.com/hook",
			Events:      []EventType{EventInboxChanged},
			SecretEnv:   "S",
			PathFilters: PathFilters{Include: []string{"inbox/"}},
		},
	})
	r := NewEventRouter(cfg, cap, quietLogger())
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Every Record* entry point must be a no-op after Close.
	r.RecordMutation(MutationEvent{Kind: "edit", Path: "inbox/a.md"})
	r.RecordInboxFSChange("inbox/b.md", notify.ChangeModified)
	r.RecordReconcile([]string{"inbox/c.md"})

	waitStable(t, window*3, window*5, func() bool { return cap.count() == 0 })

	if got := cap.snapshot(); len(got) != 0 {
		t.Fatalf("expected no dispatches after Close, got %+v", got)
	}
}

func TestEventRouter_MultipleConsumersSameEvent(t *testing.T) {
	cap := &captureDispatcher{}
	window := 30 * time.Millisecond
	cfg := fastConfig(window, []Consumer{
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

	r.RecordInboxFSChange("inbox/x.md", notify.ChangeModified)
	waitUntil(t, window*5, func() bool { return cap.count() >= 2 })

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
