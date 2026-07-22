package adapter

import "context"

// Adapter is the common write interface implemented by all target store adapters.
//
// keys holds the composite key fields and values (from EvalResult.Keys).
// row holds all projected non-key column values (from EvalResult.Row).
// projectionSeq is the JetStream stream sequence of the triggering CDC message
// (EvalResult.ProjectionSeq); a guarded adapter uses it as a monotonic ordering
// token to reject a lower-seq replay. Unguarded adapters ignore it.
type Adapter interface {
	Upsert(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) error
	Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error
	// Probe performs a lightweight liveness check against the target store.
	// Returns nil if the store is reachable and the target bucket/table exists;
	// returns an error (classified by failure.Classify) otherwise.
	// Used by the pipeline's infrastructure-pause probe loop (FR17).
	Probe(ctx context.Context) error
	Close() error
}

// Truncater is an optional interface for adapters that support clearing all
// rows/entries from the target store. Adapters may implement this to support
// the truncate-before-rebuild operation (FR29).
// Truncate is called by pipeline.Pipeline.Rebuild when truncate=true is
// requested via the "rebuild" control operation.
type Truncater interface {
	Truncate(ctx context.Context) error
}

// KeyLister is an optional interface for adapters that support enumerating
// every key currently live in the target, each rendered as the same
// field-name-to-value map shape Upsert/Delete accept as `keys`. Implemented
// by adapters backing a DiffRetraction-enabled lens (the neighbor-driven /
// multi-row filter-retraction gap Fire 2's anchor-self presence check
// structurally cannot reach — a composite key with a column bound to a
// non-anchor variable, e.g. a `manages`-walked landlord_id): the pipeline
// diffs this list against a fresh re-projection's key set to derive Deletes
// for rows no single CDC event names directly.
type KeyLister interface {
	ListKeys(ctx context.Context) ([]map[string]any, error)
}

// RowReader is an optional interface for adapters that support reading back
// one row by its composite key. Implemented by NatsKVAdapter for the
// Chronicler's event→row runtime (internal/chronicler): a single lifecycle
// event only ever carries a SUBSET of a row's columns (e.g. a
// loom.patternCompleted event carries no patternRef/subjectKey), so the
// runtime reads the previously stored row and merges the event's partial
// projection onto it before writing — carrying forward columns this event
// didn't touch. Returns (nil, false, nil) when the row does not exist yet.
type RowReader interface {
	GetRow(ctx context.Context, keys map[string]any) (row map[string]any, ok bool, err error)
}

// HydrationMarkerPublisher is an optional interface for adapters that support
// publishing a terminal "hydrationComplete" marker after a cold bulk
// projection (personal-secure-lens-design.md §3.5, Fire PL.4). Implemented by
// NatsSubjectAdapter: the marker carries the high-water revision the device's
// Sync Manager reverts to incremental delivery from. Called by
// pipeline.Pipeline.Hydrate once every row of the bulk projection has been
// published through Upsert/Delete.
type HydrationMarkerPublisher interface {
	PublishHydrationComplete(ctx context.Context, actorID string, revision uint64) error
}

// KeySetPublisher is an optional interface for adapters that support
// publishing a "keyset" frame — the complete, authoritative set of keys a
// lens currently projects for one actor, as of one revision
// (personal-lens-retraction-design.md §3.1, R1). Implemented by
// NatsSubjectAdapter. keys carries the same field-name-to-value key maps
// Upsert accepts, one per row this lens currently projects for actorID
// (empty/nil when the actor's evaluation surfaced no surviving row — the
// last-row-retraction case a keyset frame exists to signal). The Edge
// client diffs its per-lens mirror against the frame and prunes whatever
// dropped out; the adapter derives each key's on-wire string itself, the
// same derivation Upsert/Delete use.
type KeySetPublisher interface {
	PublishKeySet(ctx context.Context, actorID string, keys []map[string]any, revision uint64) error
}
