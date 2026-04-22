package dispatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// newTestConfig returns a minimal valid Config pointing at serverURL. It is
// intentionally aggressive on timings so tests run fast.
func newTestConfig(serverURL, consumerName, secretEnv string) *Config {
	cfg := &Config{
		Events: map[EventType]EventConfig{
			EventInboxChanged: {Prompt: "inbox-manager"},
		},
		Wiki: WikiConfig{
			BaseURL: "https://wiki.example.com",
			MCPURL:  "https://wiki.example.com/mcp",
		},
		Consumers: []Consumer{
			{
				Name:      consumerName,
				URL:       serverURL,
				Events:    []EventType{EventInboxChanged},
				SecretEnv: secretEnv,
				// Per-consumer retries set by individual tests.
			},
		},
	}
	cfg.applyDefaults()
	// Tests use tight timings so asserts don't block CI.
	cfg.Defaults.Retries.InitialBackoff = duration{D: 5 * time.Millisecond}
	cfg.Defaults.Retries.MaxBackoff = duration{D: 20 * time.Millisecond}
	cfg.Defaults.Retries.MaxAttempts = 3
	cfg.Defaults.Timeout = duration{D: 2 * time.Second}
	cfg.Defaults.CircuitBreaker.Threshold = 3
	cfg.Defaults.CircuitBreaker.Cooldown = duration{D: 100 * time.Millisecond}
	return cfg
}

// counterValue returns the total across all labelled variants of a counter.
func counterValue(t *testing.T, c prometheus.Collector) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)
	var sum float64
	for m := range ch {
		var dto dto.Metric
		if err := m.Write(&dto); err != nil {
			t.Fatalf("metric write: %v", err)
		}
		if dto.Counter != nil {
			sum += dto.Counter.GetValue()
		}
	}
	return sum
}

// labelledCounterValue sums the counter only across metrics carrying
// label=value.
func labelledCounterValue(t *testing.T, c prometheus.Collector, label, value string) float64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 64)
	c.Collect(ch)
	close(ch)
	var sum float64
	for m := range ch {
		var dm dto.Metric
		if err := m.Write(&dm); err != nil {
			t.Fatalf("metric write: %v", err)
		}
		matched := false
		for _, lp := range dm.Label {
			if lp.GetName() == label && lp.GetValue() == value {
				matched = true
				break
			}
		}
		if matched && dm.Counter != nil {
			sum += dm.Counter.GetValue()
		}
	}
	return sum
}

// newTestEnvelope returns a valid envelope for a test dispatch.
func newTestEnvelope() Envelope {
	return Envelope{
		DeliveryID: "did-test",
		Event:      EventInboxChanged,
		Timestamp:  time.Unix(0, 0).UTC(),
		Source:     SourceAPI,
		Paths:      []string{"inbox/one.md"},
		Prompt: PromptRef{
			Name: "inbox-manager",
			URL:  "https://wiki.example.com/meta/prompts/inbox-manager.md",
		},
		Wiki: WikiLocators{
			BaseURL: "https://wiki.example.com",
			MCPURL:  "https://wiki.example.com/mcp",
		},
	}
}

// syncBuffer is a goroutine-safe io.Writer backed by a bytes.Buffer. The
// dispatcher logs from worker goroutines concurrently with test assertions,
// so an unsynchronized bytes.Buffer trips the race detector.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// silentLogger returns a slog.Logger writing to buf, suitable for inspection
// in tests without printing to stderr.
func silentLogger(buf *syncBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestHTTPDispatcher_HappyPath(t *testing.T) {
	const secret = "topsecret"
	t.Setenv("WIKI_TEST_HMAC", secret)

	var (
		mu         sync.Mutex
		gotBody    []byte
		gotHeaders http.Header
		hits       int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		gotHeaders = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "primary", "WIKI_TEST_HMAC")
	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	env := newTestEnvelope()
	if err := d.Dispatch(context.Background(), cfg.Consumers[0], env); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	waitUntil(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) == 1 })

	mu.Lock()
	defer mu.Unlock()
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type: got %q", gotHeaders.Get("Content-Type"))
	}
	if gotHeaders.Get("X-Wiki-Event") != string(env.Event) {
		t.Errorf("X-Wiki-Event: got %q", gotHeaders.Get("X-Wiki-Event"))
	}
	if gotHeaders.Get("X-Wiki-Delivery-Id") != env.DeliveryID {
		t.Errorf("X-Wiki-Delivery-Id: got %q", gotHeaders.Get("X-Wiki-Delivery-Id"))
	}
	ts := gotHeaders.Get("X-Wiki-Timestamp")
	if ts == "" {
		t.Error("X-Wiki-Timestamp missing")
	}
	sig := gotHeaders.Get("X-Wiki-Signature")
	if !strings.HasPrefix(sig, "sha256=") {
		t.Errorf("X-Wiki-Signature: got %q", sig)
	}
	// Verify signature matches what the consumer would compute.
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(ts))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != want {
		t.Errorf("signature mismatch: got %q want %q", sig, want)
	}

	// Body round-trips as Envelope JSON.
	var parsed Envelope
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if parsed.DeliveryID != env.DeliveryID {
		t.Errorf("DeliveryID: got %q", parsed.DeliveryID)
	}

	if got := labelledCounterValue(t, d.dispatchTotal, "outcome", outcomeSuccess); got != 1 {
		t.Errorf("success counter: got %v want 1", got)
	}
}

func TestHTTPDispatcher_RetryThenSucceed(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "retrier", "WIKI_TEST_HMAC")
	cfg.Defaults.Retries.MaxAttempts = 5
	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	waitUntil(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) >= 3 })

	if got := labelledCounterValue(t, d.dispatchTotal, "outcome", outcomeSuccess); got != 1 {
		t.Errorf("success counter: got %v want 1", got)
	}
	if got := counterValue(t, d.retryTotal); got < 2 {
		t.Errorf("retry_total: got %v want >=2", got)
	}
}

func TestHTTPDispatcher_AllRetriesFail(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "doomed", "WIKI_TEST_HMAC")
	cfg.Defaults.Retries.MaxAttempts = 3
	// Threshold high enough that retry exhaustion doesn't trip the breaker.
	cfg.Defaults.CircuitBreaker.Threshold = 10

	var buf syncBuffer
	d, err := NewHTTPDispatcher(cfg, silentLogger(&buf), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// After retry exhaustion the terminal outcome is dropped (and exactly
	// one). Intermediate failures are tracked in retry_total, not
	// dispatch_total — asserting success=0 confirms nothing slipped
	// through.
	waitUntil(t, 3*time.Second, func() bool {
		return labelledCounterValue(t, d.dispatchTotal, "outcome", outcomeDropped) == 1
	})
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("server hits: got %d want 3", got)
	}
	if got := labelledCounterValue(t, d.dispatchTotal, "outcome", outcomeSuccess); got != 0 {
		t.Errorf("success counter: got %v want 0", got)
	}
	// 3 attempts → 2 retries.
	if got := counterValue(t, d.retryTotal); got != 2 {
		t.Errorf("retry_total: got %v want 2", got)
	}
	logged := buf.String()
	if !strings.Contains(logged, "dispatch.dropped") {
		t.Errorf("expected dispatch.dropped in logs: %s", logged)
	}
	if !strings.Contains(logged, "dispatch.attempt.failed") {
		t.Errorf("expected dispatch.attempt.failed in logs: %s", logged)
	}
}

func TestHTTPDispatcher_HMACRotation(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "first")

	var (
		mu   sync.Mutex
		sigs []string
		tsv  []string
		bods [][]byte
		hits int32
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		sigs = append(sigs, r.Header.Get("X-Wiki-Signature"))
		tsv = append(tsv, r.Header.Get("X-Wiki-Timestamp"))
		bods = append(bods, b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "rot", "WIKI_TEST_HMAC")
	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
		t.Fatalf("Dispatch 1: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) == 1 })

	// Rotate secret and dispatch again.
	t.Setenv("WIKI_TEST_HMAC", "second")
	if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
		t.Fatalf("Dispatch 2: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) == 2 })

	mu.Lock()
	defer mu.Unlock()
	verify := func(secret string, i int) {
		t.Helper()
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(tsv[i]))
		_, _ = mac.Write([]byte("."))
		_, _ = mac.Write(bods[i])
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if sigs[i] != want {
			t.Errorf("attempt %d: signature mismatch\n  got  %q\n  want %q", i, sigs[i], want)
		}
	}
	verify("first", 0)
	verify("second", 1)
}

func TestHTTPDispatcher_CircuitBreaker(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "breaker", "WIKI_TEST_HMAC")
	cfg.Defaults.Retries.MaxAttempts = 1 // one attempt per deliver → one failure
	cfg.Defaults.CircuitBreaker.Threshold = 2
	cfg.Defaults.CircuitBreaker.Cooldown = duration{D: 150 * time.Millisecond}

	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	// Two failing dispatches should trip the breaker.
	for i := 0; i < 2; i++ {
		if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
			t.Fatalf("Dispatch %d: %v", i, err)
		}
	}
	// Each deliver exhausts its single attempt → terminal outcome=dropped.
	// Two deliveries plus a breaker threshold of 2 trips the circuit.
	waitUntil(t, 3*time.Second, func() bool {
		return labelledCounterValue(t, d.dispatchTotal, "outcome", outcomeDropped) == 2
	})
	hitsAfterFail := atomic.LoadInt32(&hits)
	if hitsAfterFail != 2 {
		t.Fatalf("server hits after two fails: got %d want 2", hitsAfterFail)
	}

	// Additional dispatches while open should not reach the server.
	for i := 0; i < 3; i++ {
		if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
			t.Fatalf("Dispatch (open): %v", err)
		}
	}
	// Give the pipeline a moment to settle; no hits should land.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != hitsAfterFail {
		t.Errorf("server hit during open breaker: got %d want %d", got, hitsAfterFail)
	}
	if got := labelledCounterValue(t, d.dispatchTotal, "outcome", outcomeCircuitOpen); got < 1 {
		t.Errorf("circuit_open counter: got %v want >=1", got)
	}

	// After cooldown, next attempt should be allowed through.
	time.Sleep(200 * time.Millisecond)
	if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
		t.Fatalf("Dispatch (post-cooldown): %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) > hitsAfterFail })
}

func TestHTTPDispatcher_BearerToken(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	tests := []struct {
		name      string
		tokenEnv  string
		tokenVal  string
		wantAuth  string
		setNoSend bool
	}{
		{
			name:     "present",
			tokenEnv: "WIKI_TEST_BEARER",
			tokenVal: "abc.123",
			wantAuth: "Bearer abc.123",
		},
		{
			name:     "empty env means no header",
			tokenEnv: "WIKI_TEST_BEARER",
			tokenVal: "",
			wantAuth: "",
		},
		{
			name:     "unset means no header",
			tokenEnv: "",
			wantAuth: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				auth string
				hits int32
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&hits, 1)
				mu.Lock()
				auth = r.Header.Get("Authorization")
				mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			if tc.tokenEnv != "" {
				t.Setenv(tc.tokenEnv, tc.tokenVal)
			}
			cfg := newTestConfig(server.URL, "bearer-"+tc.name, "WIKI_TEST_HMAC")
			cfg.Consumers[0].BearerTokenEnv = tc.tokenEnv

			d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
			if err != nil {
				t.Fatalf("NewHTTPDispatcher: %v", err)
			}
			defer func() { _ = d.Close(context.Background()) }()

			if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			waitUntil(t, 2*time.Second, func() bool { return atomic.LoadInt32(&hits) == 1 })

			mu.Lock()
			got := auth
			mu.Unlock()
			if got != tc.wantAuth {
				t.Errorf("Authorization: got %q want %q", got, tc.wantAuth)
			}
		})
	}
}

func TestHTTPDispatcher_SanitizedURLInLogs(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	// Build consumer URL with userinfo and query so we can assert on log redaction.
	consumerURL := strings.Replace(server.URL, "http://", "http://user:supersecret@", 1) + "/?token=tkn"

	cfg := newTestConfig(consumerURL, "redact", "WIKI_TEST_HMAC")
	cfg.Defaults.Retries.MaxAttempts = 1

	var buf syncBuffer
	d, err := NewHTTPDispatcher(cfg, silentLogger(&buf), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	waitUntil(t, 2*time.Second, func() bool {
		return labelledCounterValue(t, d.dispatchTotal, "outcome", outcomeDropped) == 1
	})

	logged := buf.String()
	for _, forbidden := range []string{"supersecret", "user:supersecret", "token=tkn"} {
		if strings.Contains(logged, forbidden) {
			t.Errorf("log leaks %q: %s", forbidden, logged)
		}
	}
	if !strings.Contains(logged, SanitizeURL(consumerURL)) {
		t.Errorf("log missing sanitized URL %q: %s", SanitizeURL(consumerURL), logged)
	}
}

func TestHTTPDispatcher_CloseDrainsInFlight(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	// Server responds but blocks briefly so Close has to wait.
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "close", "WIKI_TEST_HMAC")
	cfg.Defaults.Retries.MaxAttempts = 1

	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}

	if err := d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Close is idempotent.
	if err := d.Close(context.Background()); err != nil {
		t.Errorf("Close (second call): %v", err)
	}

	// Post-close Dispatch returns errDispatcherClosed cleanly (no panic).
	err = d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope())
	if !errors.Is(err, errDispatcherClosed) {
		t.Errorf("post-close Dispatch: got err=%v, want errDispatcherClosed", err)
	}

	// No dispatches should land after close.
	pre := atomic.LoadInt32(&hits)
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&hits); got != pre {
		t.Errorf("hits after close: got %d want %d", got, pre)
	}
}

// TestHTTPDispatcher_NoPanicOnConcurrentCloseAndDispatch is the regression
// guard for the send-on-closed-channel panic that the original shutdown
// path allowed. The previous implementation closed each worker's input
// channel in Close while concurrent Dispatch calls could still be
// mid-send — a classic Go race. The current implementation never closes
// the input channel (workers exit via closeCh) and Dispatch bails out via
// an atomic flag + select-on-closeCh.
//
// Run with -race to catch data races; run with -count=10 to shake out
// timing-dependent panics.
func TestHTTPDispatcher_NoPanicOnConcurrentCloseAndDispatch(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := newTestConfig(server.URL, "racer", "WIKI_TEST_HMAC")
	cfg.Defaults.Retries.MaxAttempts = 1

	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}

	const n = 50
	started := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-started
			// Errors are fine — errDispatcherClosed is expected for calls
			// that land after Close. The assertion is simply that nothing
			// panics.
			_ = d.Dispatch(context.Background(), cfg.Consumers[0], newTestEnvelope())
		}()
	}

	// Release all goroutines at once, then race Close against them.
	close(started)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = d.Close(ctx)
	}()

	wg.Wait()
	// Idempotent close after goroutines finish; must not panic either.
	_ = d.Close(context.Background())
}

func TestHTTPDispatcher_UnknownConsumer(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")

	cfg := newTestConfig("http://unused.example.com/x", "known", "WIKI_TEST_HMAC")
	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	unknown := Consumer{Name: "not-configured", URL: "http://x/y"}
	if err := d.Dispatch(context.Background(), unknown, newTestEnvelope()); err == nil {
		t.Fatal("expected error for unknown consumer, got nil")
	}
}

func TestComputeBackoff_FullJitterBounded(t *testing.T) {
	t.Setenv("WIKI_TEST_HMAC", "s")
	// Need a dispatcher to host the per-instance PRNG; the config contents
	// don't matter beyond passing validation.
	cfg := newTestConfig("http://unused.example.com/x", "backoff", "WIKI_TEST_HMAC")
	d, err := NewHTTPDispatcher(cfg, silentLogger(&syncBuffer{}), prometheus.NewRegistry())
	if err != nil {
		t.Fatalf("NewHTTPDispatcher: %v", err)
	}
	defer func() { _ = d.Close(context.Background()) }()

	initial := 100 * time.Millisecond
	max := 1 * time.Second
	// attempt 0 → base = initial; attempt 3 → base = 800ms; attempt 10 → capped to max.
	for attempt := 0; attempt < 12; attempt++ {
		for i := 0; i < 20; i++ {
			got := d.computeBackoff(attempt, initial, max)
			if got < 0 || got > max {
				t.Errorf("attempt=%d: backoff %v out of [0,%v]", attempt, got, max)
			}
		}
	}
}

func TestIsRetriable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"500", &httpStatusError{code: 500}, true},
		{"502", &httpStatusError{code: 502}, true},
		{"408", &httpStatusError{code: 408}, true},
		{"429", &httpStatusError{code: 429}, true},
		{"400", &httpStatusError{code: 400}, false},
		{"401", &httpStatusError{code: 401}, false},
		{"403", &httpStatusError{code: 403}, false},
		{"404", &httpStatusError{code: 404}, false},
		{"non-retriable", &nonRetriableError{msg: "x"}, false},
		{"context cancel", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetriable(tc.err); got != tc.want {
				t.Errorf("isRetriable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
