package dispatch

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"go.yaml.in/yaml/v2"
)

// Default values applied when corresponding config fields are unset. Exposed
// as vars (not consts) so tests can reference them.
var (
	defaultTimeout                 = 10 * time.Second
	defaultRetriesMaxAttempts      = 5
	defaultRetriesInitial          = 2 * time.Second
	defaultRetriesMaxBackoff       = 2 * time.Minute
	defaultInboxChangedWindow      = 90 * time.Second
	defaultCircuitBreakerThreshold = 5
	defaultCircuitBreakerCooldown  = 60 * time.Second
)

// duration wraps time.Duration so yaml.v2 can parse values like "15s" or
// "2m". It stores the parsed duration in D for use by the rest of the
// package after Config defaulting runs.
type duration struct {
	D time.Duration
}

// UnmarshalYAML implements yaml.Unmarshaler. It accepts a string like "90s"
// or a numeric value interpreted as nanoseconds (matching stdlib json).
func (d *duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var s string
	if err := unmarshal(&s); err == nil {
		if s == "" {
			d.D = 0
			return nil
		}
		parsed, perr := time.ParseDuration(s)
		if perr != nil {
			return fmt.Errorf("invalid duration %q: %w", s, perr)
		}
		d.D = parsed
		return nil
	}
	// Fall back to integer nanoseconds.
	var n int64
	if err := unmarshal(&n); err != nil {
		return fmt.Errorf("duration must be a string or integer: %w", err)
	}
	d.D = time.Duration(n)
	return nil
}

// Config is the top-level dispatcher configuration, loaded from YAML.
//
// A zero value is not valid; use LoadConfig to obtain a populated instance.
type Config struct {
	Defaults         Defaults                  `yaml:"defaults"`
	Events           map[EventType]EventConfig `yaml:"events"`
	ReconcileOnStart bool                      `yaml:"reconcile_on_start"`
	Consumers        []Consumer                `yaml:"consumers"`
	Wiki             WikiConfig                `yaml:"wiki"`
}

// WikiConfig identifies the wiki instance so outbound envelopes can populate
// consumer-facing locators.
type WikiConfig struct {
	BaseURL string `yaml:"base_url"`
	MCPURL  string `yaml:"mcp_url"`
}

// Defaults holds values applied to every consumer unless overridden.
type Defaults struct {
	Timeout        duration               `yaml:"timeout"`
	Retries        Retries                `yaml:"retries"`
	CircuitBreaker CircuitBreaker         `yaml:"circuit_breaker"`
	Debounce       map[EventType]duration `yaml:"debounce"`
}

// Retries configures HTTP retry behavior.
type Retries struct {
	MaxAttempts    int      `yaml:"max_attempts"`
	InitialBackoff duration `yaml:"initial_backoff"`
	MaxBackoff     duration `yaml:"max_backoff"`
}

// CircuitBreaker configures per-consumer failure tripping. Threshold is the
// number of consecutive failures that trips the breaker open; Cooldown is the
// duration to remain open before allowing a half-open probe.
type CircuitBreaker struct {
	Threshold int      `yaml:"threshold"`
	Cooldown  duration `yaml:"cooldown"`
}

// EventConfig binds an event type to the Claude prompt a consumer should
// load when it receives the event.
type EventConfig struct {
	Prompt string `yaml:"prompt"`
}

// PathFilters filter inbound paths by prefix. Include filters out paths that
// do not match any prefix; exclude removes paths that do. In v1 these are
// simple prefix matches — no glob syntax.
type PathFilters struct {
	Include []string `yaml:"include"`
	Exclude []string `yaml:"exclude"`
}

// Consumer is one webhook receiver. The HTTP dispatcher consumes
// SecretEnv/BearerTokenEnv at send time.
//
// SkipAllDeletes, when true, suppresses dispatches to this consumer when
// every filtered change in a debounced batch has action=deleted. Useful
// for consumers whose agent cleanup (deleting classified inbox files)
// would otherwise self-trigger an empty-inbox follow-up run on the next
// debounce window. Batches with any created/modified paths still
// dispatch unchanged. Reconcile dispatches are unaffected since
// reconcile never emits deletions.
type Consumer struct {
	Name           string         `yaml:"name"`
	URL            string         `yaml:"url"`
	Events         []EventType    `yaml:"events"`
	PathFilters    PathFilters    `yaml:"path_filters"`
	SecretEnv      string         `yaml:"secret_env"`
	BearerTokenEnv string         `yaml:"bearer_token_env"`
	Timeout        duration       `yaml:"timeout"`
	Retries        Retries        `yaml:"retries"`
	CircuitBreaker CircuitBreaker `yaml:"circuit_breaker"`
	SkipAllDeletes bool           `yaml:"skip_all_deletes"`
}

// EffectiveTimeout returns the per-consumer timeout if set, else the
// defaults.
func (c Consumer) EffectiveTimeout(defaults Defaults) time.Duration {
	if c.Timeout.D > 0 {
		return c.Timeout.D
	}
	return defaults.Timeout.D
}

// EffectiveRetries returns per-consumer retry settings if any field is set,
// otherwise the defaults. Mixed overrides fall back to defaults per-field.
func (c Consumer) EffectiveRetries(defaults Defaults) Retries {
	r := c.Retries
	if r.MaxAttempts == 0 {
		r.MaxAttempts = defaults.Retries.MaxAttempts
	}
	if r.InitialBackoff.D == 0 {
		r.InitialBackoff = defaults.Retries.InitialBackoff
	}
	if r.MaxBackoff.D == 0 {
		r.MaxBackoff = defaults.Retries.MaxBackoff
	}
	return r
}

// EffectiveCircuitBreaker returns the per-consumer circuit breaker config if
// set, otherwise the defaults. Threshold and Cooldown are resolved
// independently so partial per-consumer overrides are allowed.
func (c Consumer) EffectiveCircuitBreaker(defaults Defaults) CircuitBreaker {
	cb := c.CircuitBreaker
	if cb.Threshold == 0 {
		cb.Threshold = defaults.CircuitBreaker.Threshold
	}
	if cb.Cooldown.D == 0 {
		cb.Cooldown = defaults.CircuitBreaker.Cooldown
	}
	return cb
}

// LoadConfig reads path, parses YAML, applies defaults, and validates.
// An empty path or a missing file returns (nil, nil) so the caller can treat
// "no config" as "dispatcher disabled".
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read dispatch config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.UnmarshalStrict(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse dispatch config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid dispatch config %s: %w", path, err)
	}
	return &cfg, nil
}

// applyDefaults fills in unset values. Idempotent.
func (c *Config) applyDefaults() {
	if c.Defaults.Timeout.D == 0 {
		c.Defaults.Timeout.D = defaultTimeout
	}
	if c.Defaults.Retries.MaxAttempts == 0 {
		c.Defaults.Retries.MaxAttempts = defaultRetriesMaxAttempts
	}
	if c.Defaults.Retries.InitialBackoff.D == 0 {
		c.Defaults.Retries.InitialBackoff.D = defaultRetriesInitial
	}
	if c.Defaults.Retries.MaxBackoff.D == 0 {
		c.Defaults.Retries.MaxBackoff.D = defaultRetriesMaxBackoff
	}
	if c.Defaults.CircuitBreaker.Threshold == 0 {
		c.Defaults.CircuitBreaker.Threshold = defaultCircuitBreakerThreshold
	}
	if c.Defaults.CircuitBreaker.Cooldown.D == 0 {
		c.Defaults.CircuitBreaker.Cooldown.D = defaultCircuitBreakerCooldown
	}
	if c.Defaults.Debounce == nil {
		c.Defaults.Debounce = make(map[EventType]duration)
	}
	if _, ok := c.Defaults.Debounce[EventInboxChanged]; !ok {
		c.Defaults.Debounce[EventInboxChanged] = duration{D: defaultInboxChangedWindow}
	}
}

// DebounceWindow returns the configured debounce window for event, defaulting
// to the event's default (or 0 if no default is known) when unset.
func (c *Config) DebounceWindow(event EventType) time.Duration {
	if c == nil {
		return 0
	}
	if d, ok := c.Defaults.Debounce[event]; ok && d.D > 0 {
		return d.D
	}
	return 0
}

// Validate enforces structural and semantic rules. applyDefaults should run
// first; Validate only surfaces user-visible errors.
func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config is nil")
	}
	// Event map + prompt names. Reject leading/trailing whitespace loudly
	// rather than silently normalizing so typos in configs fail on load
	// instead of manifesting as broken prompt URLs or mis-routed events.
	for evt, ec := range c.Events {
		raw := string(evt)
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			return errors.New("events: empty event key is not allowed")
		}
		if raw != trimmed {
			return fmt.Errorf("events[%q]: event key must not have leading or trailing whitespace", raw)
		}
		promptTrimmed := strings.TrimSpace(ec.Prompt)
		if promptTrimmed == "" {
			return fmt.Errorf("events[%s]: prompt must be non-empty", evt)
		}
		if ec.Prompt != promptTrimmed {
			return fmt.Errorf("events[%s]: prompt must not have leading or trailing whitespace", evt)
		}
	}

	// Duration sanity.
	if c.Defaults.Timeout.D < 0 {
		return errors.New("defaults.timeout must be non-negative")
	}
	if c.Defaults.Retries.InitialBackoff.D < 0 {
		return errors.New("defaults.retries.initial_backoff must be non-negative")
	}
	if c.Defaults.Retries.MaxBackoff.D < 0 {
		return errors.New("defaults.retries.max_backoff must be non-negative")
	}
	if c.Defaults.Retries.MaxAttempts < 0 {
		return errors.New("defaults.retries.max_attempts must be non-negative")
	}
	if c.Defaults.CircuitBreaker.Threshold <= 0 {
		return errors.New("defaults.circuit_breaker.threshold must be positive")
	}
	if c.Defaults.CircuitBreaker.Cooldown.D < 0 {
		return errors.New("defaults.circuit_breaker.cooldown must be non-negative")
	}
	for evt, d := range c.Defaults.Debounce {
		if d.D < 0 {
			return fmt.Errorf("defaults.debounce[%s]: duration must be non-negative", evt)
		}
	}

	// Consumers.
	seenNames := make(map[string]struct{}, len(c.Consumers))
	anyEnvelopes := false
	for i, cons := range c.Consumers {
		if strings.TrimSpace(cons.Name) == "" {
			return fmt.Errorf("consumers[%d]: name must be non-empty", i)
		}
		if _, dup := seenNames[cons.Name]; dup {
			return fmt.Errorf("consumers[%d]: duplicate consumer name %q", i, cons.Name)
		}
		seenNames[cons.Name] = struct{}{}

		if strings.TrimSpace(cons.URL) == "" {
			return fmt.Errorf("consumers[%s]: url must be non-empty", cons.Name)
		}
		u, err := url.Parse(cons.URL)
		if err != nil {
			return fmt.Errorf("consumers[%s]: invalid url %q: %w", cons.Name, cons.URL, err)
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("consumers[%s]: url scheme must be http or https, got %q", cons.Name, u.Scheme)
		}
		if u.Host == "" {
			return fmt.Errorf("consumers[%s]: url must include a host", cons.Name)
		}

		if len(cons.Events) == 0 {
			return fmt.Errorf("consumers[%s]: events list must be non-empty", cons.Name)
		}
		seenEvents := make(map[EventType]struct{}, len(cons.Events))
		for _, evt := range cons.Events {
			if strings.TrimSpace(string(evt)) == "" {
				return fmt.Errorf("consumers[%s]: empty event entry not allowed", cons.Name)
			}
			if _, dup := seenEvents[evt]; dup {
				return fmt.Errorf("consumers[%s]: duplicate event %q", cons.Name, evt)
			}
			seenEvents[evt] = struct{}{}
			if _, ok := c.Events[evt]; !ok {
				return fmt.Errorf("consumers[%s]: references unknown event %q", cons.Name, evt)
			}
			anyEnvelopes = true
		}

		if strings.TrimSpace(cons.SecretEnv) == "" {
			return fmt.Errorf("consumers[%s]: secret_env must be non-empty (HMAC required)", cons.Name)
		}

		// Path filter entries are prefix-matched verbatim; an entry like
		// "inbox/ " would validate but never match. Reject whitespace to
		// prevent silently inert configs.
		for j, p := range cons.PathFilters.Include {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				return fmt.Errorf("consumers[%s]: path_filters.include[%d] must be non-empty", cons.Name, j)
			}
			if trimmed != p {
				return fmt.Errorf("consumers[%s]: path_filters.include[%d] must not have leading or trailing whitespace", cons.Name, j)
			}
		}
		for j, p := range cons.PathFilters.Exclude {
			trimmed := strings.TrimSpace(p)
			if trimmed == "" {
				return fmt.Errorf("consumers[%s]: path_filters.exclude[%d] must be non-empty", cons.Name, j)
			}
			if trimmed != p {
				return fmt.Errorf("consumers[%s]: path_filters.exclude[%d] must not have leading or trailing whitespace", cons.Name, j)
			}
		}

		if cons.Timeout.D < 0 {
			return fmt.Errorf("consumers[%s]: timeout must be non-negative", cons.Name)
		}
		if cons.Retries.MaxAttempts < 0 {
			return fmt.Errorf("consumers[%s]: retries.max_attempts must be non-negative", cons.Name)
		}
		if cons.Retries.InitialBackoff.D < 0 {
			return fmt.Errorf("consumers[%s]: retries.initial_backoff must be non-negative", cons.Name)
		}
		if cons.Retries.MaxBackoff.D < 0 {
			return fmt.Errorf("consumers[%s]: retries.max_backoff must be non-negative", cons.Name)
		}
		if cons.CircuitBreaker.Threshold < 0 {
			return fmt.Errorf("consumers[%s]: circuit_breaker.threshold must be non-negative", cons.Name)
		}
		if cons.CircuitBreaker.Cooldown.D < 0 {
			return fmt.Errorf("consumers[%s]: circuit_breaker.cooldown must be non-negative", cons.Name)
		}
	}

	if anyEnvelopes {
		if strings.TrimSpace(c.Wiki.BaseURL) == "" {
			return errors.New("wiki.base_url must be set when any consumer is configured")
		}
		if strings.TrimSpace(c.Wiki.MCPURL) == "" {
			return errors.New("wiki.mcp_url must be set when any consumer is configured")
		}
	}

	return nil
}
