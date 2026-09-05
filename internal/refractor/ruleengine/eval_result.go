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
	// UnchangedAt carries the verdict "the target already holds this body
	// verbatim, so the write loop skips it" — nothing written, audited,
	// counted or marked as a projection write. It is the pipeline's verdict,
	// not an engine's: only the perEntry prefix diff
	// (Pipeline.multiEntryRetractions) sets it, and only after reading the
	// stored bodies back and comparing them.
	//
	// It is the ADAPTER GENERATION the comparison ran against, not a boolean,
	// because the verdict is a statement about ONE target instance. Evaluation
	// and writing read the adapter separately, so a HotReloadInto between them
	// swaps the store underneath: every entry that matched on the old target
	// would be skipped on the new one, which holds nothing — an under-grant
	// standing until the rebuild replay. A consumer honours the verdict only
	// while this equals the pipeline's current adapter generation, so a swap
	// invalidates every verdict in flight by construction rather than by
	// anybody remembering to.
	//
	// The ZERO VALUE WRITES, which is the direction every path that forgets
	// this field must fail in: an unset UnchangedAt reproduces the
	// write-everything behaviour exactly (generations start at 1, so zero is
	// never a live one), while a wrongly set one withholds a row that needed
	// to land.
	//
	// Never set on a Delete (a retraction is always written — a tombstone the
	// target "already holds" is precisely the one the prefix diff skipped
	// before it ever became a result) and never on a FailClosed result, whose
	// whole purpose is that its write cannot be silently passed over.
	UnchangedAt uint64
	// AbsenceFromEdgeIndex marks a retraction whose "the actor no longer holds
	// this anchor" verdict was concluded from the executor's EDGE view — the
	// refractor-adjacency index, a separately cursored consumer that can be
	// arbitrarily far behind the lens and can therefore report an edge absent
	// that Core KV already holds.
	//
	// Only the perEntry prefix diff against a real evaluation sets it
	// (Pipeline.multiEntryRetractions): the dropped-anchor tombstones it
	// derives by subtracting the walk's fresh entry set from the stored one.
	// Everything else is FALSE and means it: a legacy-parent tombstone (a
	// presence fact read live from the target), a missing-actor retraction
	// (the actor vertex read live from Core KV by fetchVertexProps), a doc-mode
	// actor-aggregate delete, and every engine-produced result.
	//
	// The zero value therefore says "this absence was not concluded from the
	// index", which is the answer for every writer that does not consult one.
	// A reader gating on it — Reproject's index-behind refusal — acts only
	// where the index really is the evidence.
	AbsenceFromEdgeIndex bool
}
