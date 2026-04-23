package dispatch

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jedwards1230/home-wiki/internal/notify"
)

// MutationEvent is the router's local shape for a vault mutation. We do not
// import internal/service here to keep the dispatch package standalone; the
// adapter in serve.go (Phase 4) maps service.MutationEvent → dispatch.MutationEvent.
type MutationEvent struct {
	Kind string
	Path string
	From string // only set for a move
}

// apiDedupeWindow is the window in which a filesystem event for a path is
// dropped if the same path was recorded by RecordMutation. The API feed
// always fires first; the watcher sees the same edit moments later. 2s is
// plenty for fsnotify to deliver and tight enough that sequential edits
// aren't over-suppressed.
const apiDedupeWindow = 2 * time.Second

// EventRouter merges API, fsnotify, and reconcile feeds into per-(event,
// consumer) debounced dispatches. It owns a Debouncer and a recently-seen
// path cache. It does not issue network calls; delivery is delegated to
// the Dispatcher supplied at construction time.
type EventRouter struct {
	cfg        *Config
	dispatcher Dispatcher
	logger     *slog.Logger

	debouncer *Debouncer

	mu        sync.Mutex
	recentAPI map[string]time.Time
	closed    bool
}

// NewEventRouter constructs a router. cfg may be nil, in which case every
// Record* method is a no-op — the caller can wire the router unconditionally
// and let the missing config disable it. logger defaults to slog.Default.
// Panics if dispatcher is nil when cfg is non-nil.
func NewEventRouter(cfg *Config, dispatcher Dispatcher, logger *slog.Logger) *EventRouter {
	if logger == nil {
		logger = slog.Default()
	}
	r := &EventRouter{
		cfg:        cfg,
		dispatcher: dispatcher,
		logger:     logger,
		recentAPI:  make(map[string]time.Time),
	}
	if cfg != nil {
		if dispatcher == nil {
			panic("dispatch: dispatcher must not be nil when cfg is set")
		}
		r.debouncer = NewDebouncer(r.onDebounceFlush)
	}
	return r
}

// RecordMutation ingests a mutation produced by the API or MCP surface. In
// v1 the only configured event is EventInboxChanged, so a mutation whose
// path matches an inbox consumer filter contributes to that event's bucket.
// The method also remembers the path in the API dedupe cache so a
// follow-up fsnotify event on the same path is suppressed.
//
// A move mutation is split into two observations: ChangeDeleted for
// evt.From (the source is gone) and ChangeCreated for evt.Path (a new
// file appeared at the destination). Both paths are added to the dedupe
// cache so the fsnotify events fsnotify produces for a rename — Rename on
// the source and Create on the destination — are absorbed and do not
// duplicate the dispatch.
//
// Extending to more event types later: add another observe call (or a
// routing table that maps mutation Kind → event types) here.
func (r *EventRouter) RecordMutation(evt MutationEvent) {
	if r.cfg == nil || r.debouncer == nil {
		return
	}
	path := strings.TrimSpace(evt.Path)
	if path == "" {
		return
	}
	if r.isClosed() {
		return
	}

	r.rememberAPI(path)

	from := strings.TrimSpace(evt.From)
	if from != "" {
		// Move semantics — dedupe the source path (so a Rename fsnotify
		// event on the old location is absorbed) and emit an explicit
		// delete for it.
		r.rememberAPI(from)
		r.observe(EventInboxChanged, notify.PathChange{Path: from, Action: notify.ChangeDeleted})
		r.observe(EventInboxChanged, notify.PathChange{Path: path, Action: notify.ChangeCreated})
		return
	}

	r.observe(EventInboxChanged, notify.PathChange{Path: path, Action: mutationKindToAction(evt.Kind)})
}

// RecordInboxFSChange ingests a path reported by the fsnotify watcher with
// the action the watcher derived from the event op. It is dropped when the
// same path was seen via the API within the dedupe window.
func (r *EventRouter) RecordInboxFSChange(path string, action notify.ChangeKind) {
	if r.cfg == nil || r.debouncer == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if r.isClosed() {
		return
	}
	if r.seenRecently(path) {
		r.logger.Debug("dispatch.fsnotify.dedupe", slog.String("path", path))
		return
	}
	r.observe(EventInboxChanged, notify.PathChange{Path: path, Action: action})
}

// mutationKindToAction maps a service-layer mutation kind string to a
// notify.ChangeKind for non-move mutations. Moves are handled separately
// in RecordMutation because they produce two observations. Unknown kinds
// fall back to ChangeModified — the router prefers "something happened"
// over dropping an event on a new mutation kind.
func mutationKindToAction(kind string) notify.ChangeKind {
	switch strings.ToLower(kind) {
	case "create":
		return notify.ChangeCreated
	case "delete":
		return notify.ChangeDeleted
	default:
		return notify.ChangeModified
	}
}

// RecordReconcile dispatches a synthetic event covering the supplied paths
// with SourceReconcile. It bypasses the debouncer so startup scans fire
// immediately. Called by serve.go at startup when cfg.ReconcileOnStart and
// the inbox has pending files — the path scan itself is the caller's job.
func (r *EventRouter) RecordReconcile(paths []string) {
	if r.cfg == nil || r.debouncer == nil {
		return
	}
	if len(paths) == 0 {
		return
	}
	if r.isClosed() {
		return
	}

	ctx := context.Background()
	ts := time.Now().UTC()

	// Phase 2 only dispatches EventInboxChanged; when future events arrive
	// we can add a parameter or a per-event scan.
	for _, consumer := range r.consumersFor(EventInboxChanged) {
		filtered := filterPaths(paths, consumer.PathFilters)
		if len(filtered) == 0 {
			continue
		}
		// Reconcile paths already exist at server boot — they were not
		// "just created", so ChangeModified is the closest honest action.
		changes := make([]notify.PathChange, len(filtered))
		for i, p := range filtered {
			changes[i] = notify.PathChange{Path: p, Action: notify.ChangeModified}
		}
		env := r.buildEnvelope(EventInboxChanged, SourceReconcile, changes, nil, ts)
		if err := r.dispatcher.Dispatch(ctx, consumer, env); err != nil {
			r.logger.Error("dispatch.reconcile.error",
				slog.String("consumer", consumer.Name),
				slog.String("event", string(EventInboxChanged)),
				slog.String("delivery_id", env.DeliveryID),
				slog.Any("error", err),
			)
		}
	}
}

// Close stops pending timers and prevents further dispatches. Phase 2 policy
// is to drop in-flight work; Phase 3 may choose to flush synchronously.
// Subsequent Record* calls become no-ops.
func (r *EventRouter) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.mu.Unlock()

	if r.debouncer != nil {
		r.debouncer.Close()
	}
	return nil
}

// isClosed reports whether Close has been called. Exposed as a helper so
// each Record* entry point can short-circuit under the same lock.
func (r *EventRouter) isClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

// observe fans a single-path change for event out to every consumer whose
// subscription and path filter accept it.
func (r *EventRouter) observe(event EventType, change notify.PathChange) {
	if _, ok := r.cfg.Events[event]; !ok {
		return
	}
	window := r.cfg.DebounceWindow(event)
	if window <= 0 {
		// Event without a positive debounce window: skip to avoid
		// producing a zero-window timer that fires immediately.
		return
	}
	for _, consumer := range r.consumersFor(event) {
		if !acceptPath(change.Path, consumer.PathFilters) {
			continue
		}
		r.debouncer.Observe(DebounceKey{Event: event, Consumer: consumer.Name}, window, change)
	}
}

// onDebounceFlush is the Debouncer callback. It rebuilds the consumer view
// from config and dispatches one envelope.
func (r *EventRouter) onDebounceFlush(key DebounceKey, batch DebounceBatch) {
	consumer, ok := r.consumerByName(key.Consumer)
	if !ok {
		// Config changed during the window; nothing sensible to do.
		r.logger.Warn("dispatch.flush.unknown_consumer", slog.String("consumer", key.Consumer))
		return
	}
	// Re-apply path filters at flush time in case the config tightened while
	// the bucket was open. Cheap and defensive.
	filtered := filterChanges(batch.Changes, consumer.PathFilters)
	if len(filtered) == 0 {
		return
	}

	// Suppress self-triggered cleanup cascades: if the consumer opted in
	// and every filtered change is a deletion, skip. The common cause is
	// an agent that just classified+deleted files; the follow-up delete
	// event produces no new work for it. Mixed batches (any created or
	// modified path) still dispatch unchanged.
	if consumer.SkipAllDeletes && allDeleted(filtered) {
		r.logger.Debug("dispatch.flush.skipped_all_deletes",
			slog.String("consumer", consumer.Name),
			slog.String("event", string(key.Event)),
			slog.Int("paths", len(filtered)),
		)
		return
	}

	coalesced := &CoalesceInfo{
		Count:         batch.Count,
		WindowSeconds: batch.Window.Seconds(),
		EarliestAt:    batch.EarliestAt.UTC(),
	}
	env := r.buildEnvelope(key.Event, SourceFsnotify, filtered, coalesced, time.Now().UTC())
	// SourceFsnotify is a reasonable default for debounced flushes; API and
	// fsnotify events coalesce into the same bucket, so no single source
	// label is strictly correct. Phase 3 may track the source set per entry.

	if err := r.dispatcher.Dispatch(context.Background(), consumer, env); err != nil {
		r.logger.Error("dispatch.flush.error",
			slog.String("consumer", consumer.Name),
			slog.String("event", string(key.Event)),
			slog.String("delivery_id", env.DeliveryID),
			slog.Any("error", err),
		)
	}
}

// buildEnvelope constructs the outbound payload for a single consumer.
// Paths is populated as a back-compat view over changes so v1 consumers
// that only read envelope.paths continue to work unchanged.
func (r *EventRouter) buildEnvelope(event EventType, source Source, changes []notify.PathChange, coalesced *CoalesceInfo, ts time.Time) Envelope {
	promptName := ""
	if ec, ok := r.cfg.Events[event]; ok {
		promptName = ec.Prompt
	}
	promptURL := ""
	if promptName != "" {
		if base := strings.TrimSpace(r.cfg.Wiki.BaseURL); base != "" {
			promptURL = strings.TrimRight(base, "/") + "/meta/prompts/" + promptName + ".md"
		} else {
			r.logger.Warn("dispatch.envelope.no_base_url",
				slog.String("prompt", promptName),
				slog.String("event", string(event)),
			)
		}
	}
	paths := make([]string, len(changes))
	for i, c := range changes {
		paths[i] = c.Path
	}
	return Envelope{
		SchemaVersion: SchemaVersion,
		DeliveryID:    NewDeliveryID(),
		Event:         event,
		Timestamp:     ts,
		Source:        source,
		Paths:         paths,
		Changes:       changes,
		Coalesced:     coalesced,
		Prompt: PromptRef{
			Name: promptName,
			URL:  promptURL,
		},
		Wiki: WikiLocators{
			BaseURL: r.cfg.Wiki.BaseURL,
			MCPURL:  r.cfg.Wiki.MCPURL,
		},
	}
}

// consumersFor returns the subset of configured consumers that subscribe to
// event. Order matches config order.
func (r *EventRouter) consumersFor(event EventType) []Consumer {
	out := make([]Consumer, 0, len(r.cfg.Consumers))
	for _, c := range r.cfg.Consumers {
		for _, e := range c.Events {
			if e == event {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// consumerByName returns the consumer with the given name, or false if it
// is not in the config.
func (r *EventRouter) consumerByName(name string) (Consumer, bool) {
	for _, c := range r.cfg.Consumers {
		if c.Name == name {
			return c, true
		}
	}
	return Consumer{}, false
}

// rememberAPI stamps path with the current time for dedupe purposes and
// trims stale entries opportunistically.
func (r *EventRouter) rememberAPI(path string) {
	now := time.Now()
	r.mu.Lock()
	r.recentAPI[path] = now
	// Opportunistic trim: bound the map so a long-running server does not
	// leak paths. O(n) but called infrequently (on each mutation) so fine.
	for p, t := range r.recentAPI {
		if now.Sub(t) > apiDedupeWindow {
			delete(r.recentAPI, p)
		}
	}
	r.mu.Unlock()
}

// seenRecently reports whether path was recorded by the API within
// apiDedupeWindow. The lookup also prunes stale entries.
func (r *EventRouter) seenRecently(path string) bool {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.recentAPI[path]
	if !ok {
		return false
	}
	if now.Sub(t) > apiDedupeWindow {
		delete(r.recentAPI, path)
		return false
	}
	return true
}

// acceptPath runs the prefix-match filters for a single path. Include is
// permissive when empty; exclude always applies.
func acceptPath(path string, filters PathFilters) bool {
	if len(filters.Include) > 0 {
		matched := false
		for _, p := range filters.Include {
			if strings.HasPrefix(path, p) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	for _, p := range filters.Exclude {
		if strings.HasPrefix(path, p) {
			return false
		}
	}
	return true
}

// filterPaths returns the subset of paths that pass filters, preserving order.
func filterPaths(paths []string, filters PathFilters) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if acceptPath(p, filters) {
			out = append(out, p)
		}
	}
	return out
}

// filterChanges returns the subset of changes whose paths pass filters,
// preserving order. Used at flush time to re-apply filters after a config
// tightening.
func filterChanges(changes []notify.PathChange, filters PathFilters) []notify.PathChange {
	out := make([]notify.PathChange, 0, len(changes))
	for _, c := range changes {
		if acceptPath(c.Path, filters) {
			out = append(out, c)
		}
	}
	return out
}

// allDeleted reports whether every change in the slice has action=deleted.
// An empty slice returns false — callers already short-circuit on zero
// length before reaching this check, and treating "no changes" as "all
// deleted" would give the wrong suppression semantics.
func allDeleted(changes []notify.PathChange) bool {
	if len(changes) == 0 {
		return false
	}
	for _, c := range changes {
		if c.Action != notify.ChangeDeleted {
			return false
		}
	}
	return true
}
