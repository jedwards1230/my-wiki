package service

// MutationKind describes the type of page mutation.
type MutationKind string

const (
	MutationCreate MutationKind = "create"
	MutationEdit   MutationKind = "edit"
	MutationDelete MutationKind = "delete"
	MutationMove   MutationKind = "move"
)

// MutationEvent is passed to the OnMutation callback after a successful page mutation.
type MutationEvent struct {
	Kind MutationKind
	Path string // relative path within vault (with .md extension)
	From string // only set for MutationMove
}
