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

// PrefixKeyLister is an optional interface for adapters that can enumerate only
// the keys under a given prefix, in the same map shape KeyLister returns.
//
// A target is shared: one NATS-KV bucket (weaver-targets, capability-kv) holds
// the rows of every lens pointed at it. An unscoped listing therefore hands a
// caller its siblings' keys, and every caller that acts on what it lists has to
// re-derive ownership afterwards — which the convergence sweep does exactly
// (OutputDescriptor.AnchorFromKey), at the cost of streaming the whole bucket
// once per lens per tick. Scoping the listing to the lens's own key prefix
// moves that cost to the substrate, where a JetStream subject filter answers it.
//
// The prefix must end on a "." segment boundary: the NATS filter it becomes is
// prefix + ">". It scopes, it does not prove ownership — cap. is a legitimate
// prefix of cap.roles. — so a caller whose correctness depends on ownership
// keeps its exact test.
type PrefixKeyLister interface {
	ListKeysPrefix(ctx context.Context, prefix string) ([]map[string]any, error)
}

// SeqGuarded is an optional interface reporting whether an adapter's writes are
// ordered by the projectionSeq token (Contract #6 §6.2). It exists for the one
// caller that must decide BEFORE writing — reconciliation, which reports back
// whether it healed anything. NatsKVAdapter's guard drops a token-less write
// outright and returns nil, so without this a reconciler cannot tell that
// silence apart from a write that landed.
//
// An adapter that does not implement it, or reports false, ignores
// projectionSeq: its writes are last-writer-wins and always land.
type SeqGuarded interface {
	Guarded() bool
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

// UpsertOutcome reports what one OutcomeUpserter.UpsertWithOutcome call
// actually did to the target store.
type UpsertOutcome struct {
	// Wrote is true when the call performed a real write (a Put, or — for a
	// guarded adapter — the guardedWrite CAS path, which always reports true
	// regardless of its own internal watermark no-op branches). False only
	// when an unguarded write was skipped because the marshaled row was
	// already byte-identical to what's currently stored.
	Wrote bool
	// DeclinedByWatermark is true when a guarded write was dropped because the
	// stored projectionSeq was equal to or higher than this call's token
	// (Contract #6 §6.2). It is orthogonal to Wrote, which keeps reporting
	// true on that branch: advancing — or deliberately holding — the watermark
	// is the guarded path's job on every call, and writeResults' audit skip
	// reads Wrote to decide whether a new audit fact exists.
	//
	// It exists for the caller that must know the difference: a reconciler
	// holding read-back evidence that the row diverges learns here that its
	// repair provably did not land, instead of booking the guard's silent nil
	// as a heal and reporting a frozen row as converged on every pass.
	DeclinedByWatermark bool
}

// OutcomeUpserter is an optional interface for adapters whose Upsert can
// report, as part of the same call, whether the write actually landed or was
// skipped as a content-identical no-op. Implemented by NatsKVAdapter: its
// unguarded path reads the current value back before writing and skips the
// Put when nothing changed (natskv.go's upsert) — an unanchored lens rewrites
// its full row set on every trigger, and most of those rewrites touch no
// actual row content, so the skip saves a Put plus that Put's CDC fan-out
// (the target bucket's watchers re-notifying) whenever it fires.
//
// The pipeline's write-audit step (writeResults) type-asserts for this and
// skips WriteAudit when Wrote is false: an unchanged row is not a new audit
// fact. An adapter that does not implement it — or a Delete, which this
// interface deliberately does not cover — is always treated as having
// written, the historical behavior every caller already gets from plain
// Upsert.
//
// This is a sibling method rather than a stateful "last write" flag read back
// off the adapter after the fact: NatsKVAdapter is shared between the
// pipeline's main consumer goroutine and the RetryQueue's own dedicated
// goroutine (failure.RetryQueue.Run runs on exactly one goroutine, separate
// from the consumer's), and either can call Upsert on the same adapter
// concurrently for a different key. A flag set inside one call and read back
// by its caller afterward could be clobbered by the other goroutine's call in
// between, misattributing the outcome to the wrong write. Returning the
// outcome from the call itself has no such window.
//
// Go-embedding trap for a test double: a type that embeds *NatsKVAdapter to
// promote its other methods (GetRow, ListKeysPrefix, Guarded, …) and
// overrides only Upsert to inject test behavior will still have
// UpsertWithOutcome promoted straight through to the embedded adapter — it is
// a separate method, so overriding one does not touch the other. A caller
// that prefers UpsertWithOutcome (writeResults does) would then silently
// bypass the override. A test double that overrides Upsert must override
// UpsertWithOutcome the same way (see pipeline's perEntryRetryAdapter /
// partialFailAdapter for the pattern: both route through one shared
// unexported method).
type OutcomeUpserter interface {
	UpsertWithOutcome(ctx context.Context, keys map[string]any, row map[string]any, projectionSeq uint64) (UpsertOutcome, error)
}

// DeleteOutcome reports what one OutcomeDeleter.DeleteWithOutcome call actually
// did to the target store.
type DeleteOutcome struct {
	// Wrote is true when the call performed a real retraction — a hard delete
	// of a live key, a soft tombstone Put, or a guarded tombstone that
	// committed. Every way of retracting nothing reports false: a guarded
	// delete the watermark declined, a guarded delete dropped for want of an
	// ordering token, and a hard delete of an already-absent key.
	//
	// This is deliberately STRICTER than UpsertOutcome.Wrote, which stays true
	// through the guarded path's own no-op branches because advancing the
	// watermark is that path's job on every call and writeResults' audit skip
	// reads it. A retraction has no such second job: if the row is still there,
	// nothing happened. The two therefore must not be wired to a shared
	// consumer without deciding which rule that consumer wants — use
	// DeclinedByWatermark, which means the same thing on both.
	Wrote bool
	// DeclinedByWatermark is true when a guarded retraction was dropped
	// because the stored projectionSeq was equal to or higher than this call's
	// token. This is the over-grant direction: a revocation that silently does
	// not land leaves the pre-edit permission set live, which is the state
	// Contract #6 §6.2's guard exists to keep subordinate — not to conceal.
	DeclinedByWatermark bool
}

// OutcomeDeleter is the retraction sibling of OutcomeUpserter, for adapters
// whose Delete can report whether the retraction actually landed. Implemented
// by NatsKVAdapter.
//
// It exists because reporting only the upsert direction leaves the more
// dangerous half silent. A guarded Delete runs the same CAS as a guarded
// Upsert, so a revocation at a tied-or-lower watermark is dropped and returns
// nil — and a reconciler reading only the error would book Deleted and Wrote
// for a row that still grants what the edit meant to revoke.
//
// An adapter that does not implement it is treated as having written, the
// historical behavior every caller already gets from plain Delete.
//
// The same Go-embedding trap OutcomeUpserter documents applies verbatim: a
// test double that embeds *NatsKVAdapter and overrides only Delete still
// promotes DeleteWithOutcome straight through to the embedded adapter, so a
// caller preferring the outcome form would bypass the override. Route both
// through one shared unexported method.
type OutcomeDeleter interface {
	DeleteWithOutcome(ctx context.Context, keys map[string]any, projectionSeq uint64) (DeleteOutcome, error)
}
