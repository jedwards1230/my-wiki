package notify

// Sink receives vault path mutations. Implementations decide how to react
// (debounce, fanout, dispatch, etc.).
type Sink interface {
	MarkDirty(path string)
}
