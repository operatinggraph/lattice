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
	// (no triggering stream message, e.g. the adjacency-watch path).
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
}
