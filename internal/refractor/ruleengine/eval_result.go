package ruleengine

// NodeEntry describes one Core KV node entry received from a rule consumer.
// It is engine-neutral: both the simple and full engines' callers (the
// pipeline) construct and consume it.
type NodeEntry struct {
	CoreKVKey  string         // full Core KV key, e.g. "node:agreement:abc123"
	NodeLabel  string         // label of this node, e.g. "agreement"
	IsDeleted  bool           // true when the "isDeleted" JSON field is true
	Properties map[string]any // all JSON fields from the payload (including "isDeleted")
}

// EvalResult is the evaluation output for one anchor entity. It is the
// pipeline's write-loop carrier — engine-neutral, populated by whichever
// engine (simple or full) evaluated the entry.
type EvalResult struct {
	Delete bool           // true = issue a hard delete to the adapter
	Keys   map[string]any // key column values (always populated)
	Row    map[string]any // all projected column values; nil when Delete is true
	// ProjectionSeq is the JetStream stream sequence of the CDC message that
	// triggered this evaluation. It is the monotonic ordering token a guarded
	// adapter uses to reject a lower-seq replay. Zero means unguarded/unknown
	// (no triggering stream message, e.g. a reconciliation Reproject on a
	// pipeline that has not yet applied any event).
	ProjectionSeq uint64
	// FailClosed marks a result whose write must never be silently skipped
	// while its batch siblings still land (cap-read-per-anchor-grant-keys-
	// design.md §4.2's deny-closed ordering — a retraction tombstone that is
	// supposed to land before a sibling upsert, e.g. a perEntry lens's dropped-
	// anchor/legacy-parent-document tombstones). The pipeline write loop
	// aborts the whole batch (full redelivery) on ANY failure of a
	// FailClosed result, regardless of failure.Category — a category that
	// would otherwise let the loop continue (CatTerminal's DLQ-and-continue,
	// CatTransient's per-actor-retry-and-continue) would let a still-live
	// stale grant this write meant to retire survive alongside a sibling's
	// successful fresh write in the very same pass.
	FailClosed bool
	// Provenance is ProjectionResult.Provenance carried onto the write loop:
	// the vertex keys this row's evaluation read. A personal pipeline's CDC
	// write loop publishes a non-delete result iff the event's vertex set
	// meets it (PublishScope.Admits under ScopeVertices). Nil on a Delete, on
	// a result an engine did not record provenance for, and on a
	// pipeline-manufactured result (a tombstone, a zero-row retraction).
	Provenance []string
	// Unchanged marks a result the target already holds verbatim: the stored
	// body equals this one, so the write loop skips it — nothing written,
	// audited, counted or marked on the freshness clock. It is the pipeline's
	// verdict, not an engine's: only the perEntry prefix diff
	// (Pipeline.multiEntryRetractions) sets it, and only after reading the
	// stored bodies back and comparing them.
	//
	// The ZERO VALUE WRITES, which is the direction every path that forgets
	// this field must fail in: an unset Unchanged reproduces the
	// write-everything behaviour exactly, while a wrongly set one withholds a
	// row that needed to land.
	//
	// Never set on a Delete (a retraction is always written — a tombstone the
	// target "already holds" is precisely the one the prefix diff skipped
	// before it ever became a result) and never on a FailClosed result, whose
	// whole purpose is that its write cannot be silently passed over.
	Unchanged bool
}
