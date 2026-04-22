package dispatch

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// defaultQueueDepth is the bounded per-consumer channel size. A full queue
// drops the event with a dropped{reason=queue_full} metric rather than
// backpressuring upstream producers.
const defaultQueueDepth = 100

// Outcome label values for wiki_webhook_dispatch_total.
const (
	outcomeSuccess     = "success"
	outcomeFailure     = "failure"
	outcomeDropped     = "dropped"
	outcomeCircuitOpen = "circuit_open"
)

// HTTPDispatcher delivers envelopes over HTTP with HMAC signing, exponential
// backoff retries, and a per-consumer circuit breaker. Each consumer gets a
// dedicated worker goroutine and bounded queue so a slow consumer cannot
// backpressure others.
//
// It satisfies Dispatcher. Create once at startup and call Close during
// shutdown to drain and stop workers.
type HTTPDispatcher struct {
	cfg    *Config
	logger *slog.Logger
	client *http.Client

	// now is injected to make retry/circuit-breaker tests deterministic.
	now func() time.Time

	// workers keyed by consumer name. Populated in NewHTTPDispatcher; never
	// mutated after.
	workers map[string]*consumerWorker

	// metrics
	dispatchTotal    *prometheus.CounterVec
	dispatchDuration *prometheus.HistogramVec
	retryTotal       *prometheus.CounterVec
	queueDepth       *prometheus.GaugeVec

	closeOnce sync.Once
}

// consumerWorker owns the per-consumer queue, HMAC secret cache, and circuit
// breaker state. One goroutine pulls from ch and attempts delivery.
type consumerWorker struct {
	consumer Consumer
	dispatch *HTTPDispatcher

	ch   chan workItem
	done chan struct{}

	// secret cache: read lazily on first send. Protected by mu.
	mu            sync.Mutex
	cachedSecret  string
	consecFails   int
	breakerOpenAt time.Time // zero when closed; nonzero while open
}

type workItem struct {
	ctx context.Context
	env Envelope
}

// NewHTTPDispatcher constructs an HTTPDispatcher ready to deliver envelopes.
// cfg must be non-nil and already validated (LoadConfig output). logger
// defaults to slog.Default if nil. registerer may be nil in which case
// metrics are registered on prometheus.DefaultRegisterer — tests should pass
// a fresh prometheus.NewRegistry() to avoid collisions.
func NewHTTPDispatcher(cfg *Config, logger *slog.Logger, registerer prometheus.Registerer) (*HTTPDispatcher, error) {
	if cfg == nil {
		return nil, errors.New("dispatch: cfg must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}

	d := &HTTPDispatcher{
		cfg:    cfg,
		logger: logger,
		client: &http.Client{
			// Per-request timeout is enforced via request context rather than
			// client.Timeout so retries can each have their own deadline.
			Transport: http.DefaultTransport,
		},
		now:     time.Now,
		workers: make(map[string]*consumerWorker, len(cfg.Consumers)),
	}

	d.dispatchTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wiki_webhook_dispatch_total",
		Help: "Total webhook dispatch attempts by event, consumer, and outcome.",
	}, []string{"event", "consumer", "outcome"})

	d.dispatchDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "wiki_webhook_dispatch_duration_seconds",
		Help:    "Wall-clock duration of a single dispatch attempt in seconds.",
		Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
	}, []string{"event", "consumer"})

	d.retryTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "wiki_webhook_retry_total",
		Help: "Total webhook retry attempts by consumer.",
	}, []string{"consumer"})

	d.queueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "wiki_webhook_queue_depth",
		Help: "Current depth of the per-consumer delivery queue.",
	}, []string{"consumer"})

	for _, c := range []prometheus.Collector{d.dispatchTotal, d.dispatchDuration, d.retryTotal, d.queueDepth} {
		if err := registerer.Register(c); err != nil {
			return nil, fmt.Errorf("register metric: %w", err)
		}
	}

	for _, consumer := range cfg.Consumers {
		// Warn at startup when the HMAC secret env var is empty. The worker
		// still attempts to sign at send time (in case an operator rotates
		// the secret out-of-band by editing env + restart), but deliveries
		// will fail until the env is populated.
		if v := os.Getenv(consumer.SecretEnv); v == "" {
			logger.Warn("dispatch.secret.empty",
				slog.String("consumer", consumer.Name),
				slog.String("secret_env", consumer.SecretEnv),
			)
		}
		if consumer.BearerTokenEnv != "" {
			if v := os.Getenv(consumer.BearerTokenEnv); v == "" {
				logger.Warn("dispatch.bearer.empty",
					slog.String("consumer", consumer.Name),
					slog.String("bearer_token_env", consumer.BearerTokenEnv),
				)
			}
		}

		w := &consumerWorker{
			consumer: consumer,
			dispatch: d,
			ch:       make(chan workItem, defaultQueueDepth),
			done:     make(chan struct{}),
		}
		d.workers[consumer.Name] = w
		go w.run()
	}

	return d, nil
}

// Dispatch enqueues env for delivery to consumer. It returns nil on enqueue
// success or when the circuit breaker short-circuits the call; a blocked
// queue is reported as a dropped outcome (not an error) because the router
// should not halt on a single slow consumer. The only returned error is
// "unknown consumer", which indicates a config mismatch.
func (d *HTTPDispatcher) Dispatch(ctx context.Context, consumer Consumer, envelope Envelope) error {
	w, ok := d.workers[consumer.Name]
	if !ok {
		return fmt.Errorf("dispatch: unknown consumer %q", consumer.Name)
	}

	// Short-circuit under an open breaker so we don't fill the queue with
	// events that will immediately be rejected.
	if w.breakerOpen() {
		d.dispatchTotal.WithLabelValues(string(envelope.Event), consumer.Name, outcomeCircuitOpen).Inc()
		d.logger.Warn("dispatch.circuit_open",
			slog.String("consumer", consumer.Name),
			slog.String("consumer_url", SanitizeURL(consumer.URL)),
			slog.String("event", string(envelope.Event)),
			slog.String("delivery_id", envelope.DeliveryID),
		)
		return nil
	}

	select {
	case w.ch <- workItem{ctx: ctx, env: envelope}:
		d.queueDepth.WithLabelValues(consumer.Name).Set(float64(len(w.ch)))
		return nil
	default:
		d.dispatchTotal.WithLabelValues(string(envelope.Event), consumer.Name, outcomeDropped).Inc()
		d.logger.Warn("dispatch.queue_full",
			slog.String("consumer", consumer.Name),
			slog.String("event", string(envelope.Event)),
			slog.String("delivery_id", envelope.DeliveryID),
		)
		return nil
	}
}

// Close stops every consumer worker and waits for in-flight deliveries to
// drain under the supplied context's deadline. Returns a non-nil error
// describing any undrained work when the deadline elapses. Subsequent calls
// are no-ops.
func (d *HTTPDispatcher) Close(ctx context.Context) error {
	var err error
	d.closeOnce.Do(func() {
		// Close every input channel; workers will return once their queues
		// drain.
		for _, w := range d.workers {
			close(w.ch)
		}

		// Wait for each worker to signal done, bounded by ctx.
		pending := make([]string, 0, len(d.workers))
		for name, w := range d.workers {
			select {
			case <-w.done:
			case <-ctx.Done():
				pending = append(pending, fmt.Sprintf("%s(%d queued)", name, len(w.ch)))
			}
		}
		if len(pending) > 0 {
			err = fmt.Errorf("dispatch: close deadline reached, pending: %v", pending)
		}
	})
	return err
}

// run is the per-consumer worker loop. It drains ch sequentially so the
// circuit-breaker and retry logic see a linear attempt order for one
// consumer. Parallelism across consumers is achieved by independent workers.
func (w *consumerWorker) run() {
	defer close(w.done)
	for item := range w.ch {
		w.dispatch.queueDepth.WithLabelValues(w.consumer.Name).Set(float64(len(w.ch)))
		w.deliver(item.ctx, item.env)
	}
	// Zero the gauge on shutdown so Prometheus doesn't retain stale depth.
	w.dispatch.queueDepth.WithLabelValues(w.consumer.Name).Set(0)
}

// deliver runs the retry loop for a single envelope. It updates metrics and
// the circuit breaker based on the outcome of each attempt.
func (w *consumerWorker) deliver(parentCtx context.Context, env Envelope) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	// Re-check the breaker at delivery time (an earlier item in this
	// worker's queue may have tripped it while this one was in flight).
	if w.breakerOpen() {
		w.dispatch.dispatchTotal.WithLabelValues(string(env.Event), w.consumer.Name, outcomeCircuitOpen).Inc()
		return
	}

	retries := w.consumer.EffectiveRetries(w.dispatch.cfg.Defaults)
	timeout := w.consumer.EffectiveTimeout(w.dispatch.cfg.Defaults)
	maxAttempts := retries.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			w.dispatch.retryTotal.WithLabelValues(w.consumer.Name).Inc()
			backoff := computeBackoff(attempt-1, retries.InitialBackoff.D, retries.MaxBackoff.D)
			if backoff > 0 {
				select {
				case <-parentCtx.Done():
					w.recordFailure(env, parentCtx.Err())
					return
				case <-time.After(backoff):
				}
			}
		}

		err := w.attempt(parentCtx, env, timeout)
		if err == nil {
			w.recordSuccess(env)
			return
		}
		lastErr = err
		if !isRetriable(err) {
			break
		}
	}

	w.recordFailure(env, lastErr)
}

// attempt performs one HTTP POST with HMAC signing. The timeout governs the
// single request; retries are orchestrated by deliver.
func (w *consumerWorker) attempt(parentCtx context.Context, env Envelope, timeout time.Duration) error {
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	secret := w.resolveSecret()
	if secret == "" {
		// Treat as a non-retriable failure: without a secret, the consumer
		// cannot validate the signature, so retrying won't help.
		return &nonRetriableError{msg: "hmac secret is empty; refusing to send unsigned envelope"}
	}

	ts := strconv.FormatInt(w.dispatch.now().Unix(), 10)
	sig := signBody(secret, ts, body)

	ctx := parentCtx
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(parentCtx, timeout)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.consumer.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Wiki-Event", string(env.Event))
	req.Header.Set("X-Wiki-Delivery-Id", env.DeliveryID)
	req.Header.Set("X-Wiki-Timestamp", ts)
	req.Header.Set("X-Wiki-Signature", "sha256="+sig)
	if w.consumer.BearerTokenEnv != "" {
		if token := os.Getenv(w.consumer.BearerTokenEnv); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}

	start := w.dispatch.now()
	resp, err := w.dispatch.client.Do(req)
	w.dispatch.dispatchDuration.WithLabelValues(string(env.Event), w.consumer.Name).
		Observe(w.dispatch.now().Sub(start).Seconds())

	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return &httpStatusError{code: resp.StatusCode}
}

// resolveSecret returns the consumer's HMAC secret, caching the env lookup
// across sends so a hot dispatch loop does not syscall per-attempt. A
// rotation (restart with new env) is picked up on the next call when the
// cached value is empty or differs — specifically, an empty cache always
// re-reads so operators can rotate a previously-empty secret without
// restarting.
func (w *consumerWorker) resolveSecret() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cachedSecret != "" {
		// Re-check env in case operator rotated. Cheap Getenv keeps rotation
		// latency low without paying per-request overhead when unchanged.
		if v := os.Getenv(w.consumer.SecretEnv); v != "" && v != w.cachedSecret {
			w.cachedSecret = v
		}
		return w.cachedSecret
	}
	w.cachedSecret = os.Getenv(w.consumer.SecretEnv)
	return w.cachedSecret
}

// recordSuccess increments success metrics and resets breaker state.
func (w *consumerWorker) recordSuccess(env Envelope) {
	w.dispatch.dispatchTotal.WithLabelValues(string(env.Event), w.consumer.Name, outcomeSuccess).Inc()
	w.mu.Lock()
	w.consecFails = 0
	w.breakerOpenAt = time.Time{}
	w.mu.Unlock()
	w.dispatch.logger.Debug("dispatch.success",
		slog.String("consumer", w.consumer.Name),
		slog.String("consumer_url", SanitizeURL(w.consumer.URL)),
		slog.String("event", string(env.Event)),
		slog.String("delivery_id", env.DeliveryID),
	)
}

// recordFailure increments failure + dropped metrics (retries exhausted),
// bumps the consecutive failure counter, and may open the breaker.
func (w *consumerWorker) recordFailure(env Envelope, err error) {
	w.dispatch.dispatchTotal.WithLabelValues(string(env.Event), w.consumer.Name, outcomeFailure).Inc()
	w.dispatch.dispatchTotal.WithLabelValues(string(env.Event), w.consumer.Name, outcomeDropped).Inc()
	w.dispatch.logger.Warn("dispatch.failure",
		slog.String("consumer", w.consumer.Name),
		slog.String("consumer_url", SanitizeURL(w.consumer.URL)),
		slog.String("event", string(env.Event)),
		slog.String("delivery_id", env.DeliveryID),
		slog.Any("error", err),
	)

	cb := w.consumer.EffectiveCircuitBreaker(w.dispatch.cfg.Defaults)
	w.mu.Lock()
	w.consecFails++
	if cb.Threshold > 0 && w.consecFails >= cb.Threshold && w.breakerOpenAt.IsZero() {
		w.breakerOpenAt = w.dispatch.now()
		w.dispatch.logger.Warn("dispatch.circuit.opened",
			slog.String("consumer", w.consumer.Name),
			slog.Int("consecutive_failures", w.consecFails),
			slog.Duration("cooldown", cb.Cooldown.D),
		)
	}
	w.mu.Unlock()
}

// breakerOpen reports whether the consumer's circuit is currently open. When
// the cooldown window has elapsed it returns false without clearing
// consecFails — the next attempt runs in half-open mode, and either clears
// state on success (recordSuccess) or re-opens the breaker on failure
// (recordFailure). Callers guard against concurrent mutations via the same
// mu used by recordSuccess/recordFailure.
func (w *consumerWorker) breakerOpen() bool {
	cb := w.consumer.EffectiveCircuitBreaker(w.dispatch.cfg.Defaults)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.breakerOpenAt.IsZero() {
		return false
	}
	if cb.Cooldown.D <= 0 {
		// No cooldown configured: a tripped breaker stays open forever until
		// a manual intervention. Defensive: validation requires threshold>0
		// and rejects negative cooldowns, but defaults may be zeroed in
		// tests.
		return true
	}
	if w.dispatch.now().Sub(w.breakerOpenAt) >= cb.Cooldown.D {
		// Enter half-open: clear the open timestamp so the next attempt is
		// allowed through. Leave consecFails intact so the next failure
		// re-opens the breaker immediately.
		w.breakerOpenAt = time.Time{}
		return false
	}
	return true
}

// computeBackoff returns initial * 2^attempt, capped at max, with full
// jitter applied. attempt=0 corresponds to the first retry delay.
func computeBackoff(attempt int, initial, max time.Duration) time.Duration {
	if initial <= 0 {
		return 0
	}
	// Guard against overflow for large attempt counts.
	d := initial
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
		if d <= 0 { // overflow
			d = max
			break
		}
	}
	if max > 0 && d > max {
		d = max
	}
	// Full jitter: uniformly random in [0, d).
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(d)))
}

// signBody returns the hex-encoded HMAC-SHA256 of "<timestamp>.<body>" with
// the given secret key.
func signBody(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// isRetriable reports whether err should cause another attempt. Timeouts,
// network errors, and 5xx/408/429 responses are retriable; explicit
// non-retriable errors and context cancellation are not.
func isRetriable(err error) bool {
	if err == nil {
		return false
	}
	// Caller-marked permanent errors (e.g. "empty secret").
	var nre *nonRetriableError
	if errors.As(err, &nre) {
		return false
	}
	// Context done is terminal; deliver() re-checks the parent context on
	// each iteration so there is no point retrying.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var hse *httpStatusError
	if errors.As(err, &hse) {
		if hse.code >= 500 || hse.code == http.StatusRequestTimeout || hse.code == http.StatusTooManyRequests {
			return true
		}
		return false
	}
	// Network/timeout errors expose net.Error.
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	// Fall back to retry — unknown errors from http.Client.Do are usually
	// transient (connection reset, EOF, etc.).
	return true
}

// httpStatusError carries a non-2xx response code so the retry policy can
// differentiate between retriable and permanent failures.
type httpStatusError struct {
	code int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http status %d", e.code)
}

// nonRetriableError marks permanent failures that should bypass the retry
// loop.
type nonRetriableError struct {
	msg string
}

func (e *nonRetriableError) Error() string {
	return e.msg
}
