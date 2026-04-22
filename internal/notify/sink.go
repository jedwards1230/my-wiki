package notify

// Sink receives vault path mutations. Implementations decide how to react
// (debounce, fanout, dispatch, etc.).
//
// MarkDirty may be called concurrently from multiple goroutines;
// implementations must be safe for concurrent use.
//
// The meaning of path is set by the producer. VaultWatcher forwards
// absolute filesystem paths from fsnotify events; other producers may
// use vault-relative paths. Implementations that dedupe or route on
// path values must be consistent with the producers feeding them.
//
// Implementations should return quickly. MarkDirty is called from
// latency-sensitive contexts (such as the fsnotify event goroutine);
// any blocking work must be deferred to another goroutine.
type Sink interface {
	MarkDirty(path string)
}
