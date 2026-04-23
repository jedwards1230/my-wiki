package notify

// ChangeKind describes what happened to a filesystem path. The values are
// the strings we expose on the wire (Envelope.Changes[*].Action) as well as
// the tokens the debouncer merges with last-action-wins semantics.
type ChangeKind string

const (
	// ChangeCreated is emitted for a newly-created path. When an unseen
	// path appears, this is the first action we record.
	ChangeCreated ChangeKind = "created"

	// ChangeModified is emitted when an existing path is written to. Also
	// used for the reconcile-on-start scan — those paths already exist at
	// server boot, and "modified" is the closest non-terminal action.
	ChangeModified ChangeKind = "modified"

	// ChangeDeleted is emitted when a path is removed or renamed away.
	// fsnotify's Rename event reports the source path of a rename; the
	// destination path (if any) is reported separately as Create.
	ChangeDeleted ChangeKind = "deleted"
)

// PathChange is one path-plus-action pair. It travels from the watcher
// through the debouncer into the outbound webhook envelope.
type PathChange struct {
	Path   string     `json:"path"`
	Action ChangeKind `json:"action"`
}
