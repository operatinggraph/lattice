// Package healthwire holds the health KV value schema — the Entry a Reporter
// writes and a control response embeds. It depends on nothing but the standard
// library.
//
// It exists because internal/refractor/health bundles the Reporter and its
// pollers beside the schema, so importing an Entry links a NATS client through
// internal/substrate. A control response carries an Entry, and an Edge node
// decodes control responses (edge-browser-node-design.md §3.2) without ever
// reporting health. internal/refractor/health re-exports every name here, so
// platform call sites read as health vocabulary and do not import this package
// directly.
package healthwire

// PauseReason values used in health KV entries.
const (
	PauseReasonInfra      = "infra"
	PauseReasonStructural = "structural"
	PauseReasonManual     = "manual"
)

// Status values used in health KV entries. The vocabulary is closed, so a
// reader branches on these rather than on a literal: every branch that exists
// treats an unrecognized status as the benign default, which makes a mistyped
// literal silent.
const (
	StatusActive     = "active"
	StatusPaused     = "paused"
	StatusRebuilding = "rebuilding"
)

// FilterMode values used in health KV entries — which Core KV consumer filter
// a lens's own derivation chose. The vocabulary is closed and every entry that
// has derived a filter carries exactly one of these.
const (
	FilterModeNarrowedRelation = "narrowed-relation"
	FilterModeNarrowedLabel    = "narrowed-label"
	FilterModeBroad            = "broad"
)

// FilterBroadReason values used in health KV entries — why a lens took the
// broad filter. The vocabulary is closed and TOTAL over the states that reach
// the broad filter, so a broad entry never carries an empty reason and no
// reader needs a default branch:
//
//   - not-eligible — the lens can never narrow as it stands: it is not on the
//     full engine at all, or (actor-aware) one of the §4.2 conjuncts its
//     INSTALLATION supplies is missing (pattern-closure, a sweep plan, the
//     anchor type absent from the label set, a declared secure holder type
//     absent from it). A property of the lens's shape, not of the data.
//   - non-exhaustive — the compiled rule CAN bind a type no label names (an
//     unlabeled node, a variable-length hop, a name re-seeded after its
//     labelling clause went out of scope), or a `*` label resolved to zero
//     concrete types. The cypher is what has to change.
//   - label-cap — the derivation was exhaustive but resolved to more labels
//     than the narrowed filter carries. A footprint REGRESSION rather than an
//     authoring mistake: another package's install can push a one-label lens
//     over the cap.
//   - taxonomy-unarmed — every referenced label resolved, but the resolver's
//     answer is not guaranteed CURRENT: a snapshot is loaded and the
//     invalidation consumer is not live, so the lens is deliberately
//     correct-but-slower until it arms. Transient by construction, and the
//     ONLY reason in this vocabulary that clears without anyone editing
//     anything — which is exactly why the state below must not share it.
//   - taxonomy-unresolvable — the taxonomy could not answer AT ALL: a
//     subtypeOf cycle, an over-depth chain, an ambiguous canonicalName, a
//     vanished abstract type, or no snapshot ever loaded. Waiting does not fix
//     this one; a package does. An operator handed taxonomy-unarmed here would
//     wait forever for an arming that changes nothing.
//   - registration-failed — the lens DID derive a narrowed filter and
//     JetStream refused to register it, so it fell back to the broad one. The
//     only reason decided after the derivation, and the only one that also
//     raises the entry's errorCount/lastError fault signal.
//
// When more than one holds, the reported reason is the one that survives
// fixing the others (pipeline's narrowingBlockRank): non-exhaustive outranks
// taxonomy-unresolvable outranks taxonomy-unarmed.
const (
	FilterBroadReasonNone                 = ""
	FilterBroadReasonNotEligible          = "not-eligible"
	FilterBroadReasonNonExhaustive        = "non-exhaustive"
	FilterBroadReasonLabelCap             = "label-cap"
	FilterBroadReasonTaxonomyUnarmed      = "taxonomy-unarmed"
	FilterBroadReasonTaxonomyUnresolvable = "taxonomy-unresolvable"
	FilterBroadReasonRegistrationFailed   = "registration-failed"
)

// Entry is the full health KV value schema. All field names are camelCase per
// architecture convention. The KV key is the ruleID; the KV bucket is
// configured via config.HealthKVBucket.
//
// PauseReason and LastError are *string so they marshal as JSON null when
// inactive, satisfying the FR21 requirement for null (not empty string) in
// active entries.
type Entry struct {
	RuleID         string  `json:"ruleId"`
	Status         string  `json:"status"`         // "active" | "paused" | "rebuilding"
	PauseReason    *string `json:"pauseReason"`    // null when active; "infra", "structural", or "manual" when paused
	ActiveSequence uint64  `json:"activeSequence"` // NATS sequence of the active rule version
	ConsumerLag    uint64  `json:"consumerLag"`    // current consumer lag; updated by Story 4.2
	ErrorCount     uint64  `json:"errorCount"`     // cumulative RecordError calls (DLQ writes, hot-reload refusals, consumer-registration fallbacks, ...); preserved across restarts
	LastError      *string `json:"lastError"`      // null when no error; non-nil with latest error message
	LastUpdated    string  `json:"lastUpdated"`    // RFC3339 UTC
	// RuleEngine is the engine name that successfully parsed this rule's match
	// body (Story 3.1a). Cached via SetRuleEngine and re-emitted on every
	// status transition. Empty string when not yet set (forward-compat).
	RuleEngine string `json:"ruleEngine,omitempty"`
	// LastProjectedAt is the wall-clock of the last successful target write
	// (lens-projection-liveness-design.md §3.2) — RFC3339 UTC; "" until the
	// lens's first projection. A freshness signal, never an alert input on its
	// own (a quiet, no-match lens naturally has an old value).
	LastProjectedAt string `json:"lastProjectedAt,omitempty"`
	// ProjectionLag is the operator-facing alias of ConsumerLag (same NumPending
	// value, named for what it means to an operator: events behind).
	ProjectionLag uint64 `json:"projectionLag"`
	// LagProgressAt is when ConsumerLag was last observed to decrease (stamped
	// at first observation too) — RFC3339 UTC; "" before the lens's first lag
	// poll. A newly-activated consumer on a bucket-wide filter can carry a
	// large ConsumerLag purely from skipping types it does not match (cold
	// bring-up replay debt) — that backlog is real but harmless as long as it
	// keeps falling. This is the "still actively draining" clock a reader
	// checks before treating ConsumerLag as staleness: only a backlog that has
	// stopped falling for a while is a genuine signal.
	LagProgressAt string `json:"lagProgressAt,omitempty"`
	// AckPending is how many messages the lens's consumer has been delivered but
	// not yet acked. ConsumerLag/ProjectionLag cannot see this: a consumer that
	// has been handed everything and cannot finish it reports NumPending 0 and is
	// indistinguishable from one that is genuinely drained. Read it together with
	// AckFloorProgressAt — nonzero here with a stale clock there is a consumer
	// that is no longer retiring the work it owes.
	AckPending uint64 `json:"ackPending"`
	// AckFloorProgressAt is when the consumer's ack floor was last observed to
	// advance (stamped at the first poll too) — RFC3339 UTC; "" before the lens's
	// first poll. It is the forward-progress clock for DELIVERED work, the
	// counterpart to LagProgressAt's clock for the undelivered backlog. A floor
	// that has stopped advancing while AckPending is nonzero is the signal, and
	// the clock's age is how long it has been stuck.
	AckFloorProgressAt string `json:"ackFloorProgressAt,omitempty"`
	// SweepCursor is the auth-plane convergence sweep's round-robin position —
	// the last anchor vertex key its deep pass verified
	// (capability-projection-reconciliation-design.md §3.2). It lives on the
	// existing health entry rather than in new state, so a restart resumes the
	// walk instead of restarting it. "" for every lens that does not sweep.
	SweepCursor string `json:"sweepCursor,omitempty"`
	// SweepReconciled is the cumulative number of divergent projections the
	// sweep has healed for this lens. Healing is deliberately loud: a nonzero
	// rate is itself the signal to go find the delivery gap it is papering over.
	SweepReconciled uint64 `json:"sweepReconciled,omitempty"`
	// EvalDriftRetries is the cumulative number of inline re-executions an
	// auth-plane evaluation's footprint validation has triggered — a
	// mid-evaluation write moved a key the evaluation read
	// (refractor-evaluation-consistency-design.md §4.6). Zero for every lens
	// outside the actorAggregate ∧ auth-plane scope, and expected to be rare
	// even in scope: drift is ms-scale by construction.
	EvalDriftRetries uint64 `json:"evalDriftRetries,omitempty"`
	// EvalDriftRequeues is the cumulative number of evaluations whose read
	// surface still diverged after the inline re-execution and were requeued
	// as a typed transient failure (failure.ErrEvalDrift) rather than landing
	// a possibly-torn row. A nonzero rate under sustained load is the signal
	// that sizes the still-undesigned per-row footprint validation for the
	// unanchored grant-table scans.
	EvalDriftRequeues uint64 `json:"evalDriftRequeues,omitempty"`
	// SecureRedactions is the cumulative number of secure-column values this
	// lens projected as null because it could not RESOLVE them — a malformed
	// envelope, a holder type the column never declared, a missing or
	// unparseable piiKey, or a failed authenticated decrypt
	// (retention-class-key-custody-design.md §6.2, fork F2). A legitimate shred
	// is NOT counted: erasure projecting null is the mechanism working. So any
	// nonzero value is a defect between a package's custody declaration and its
	// ciphertext, and it is privacy-critical precisely because the redaction is
	// SILENT at the read model — the row still renders, carrying a null where
	// plaintext belongs, indistinguishable from an erased record. This counter
	// is the only thing that tells those two apart.
	SecureRedactions uint64 `json:"secureRedactions,omitempty"`
	// FilterMode is which Core KV consumer filter this lens's own derivation
	// chose — one of the FilterMode constants above. It reports a decision the
	// lens already makes; it never changes one, and the set of subjects a lens
	// filters on is identical whether or not this field is written.
	//
	// ABSENT means the lens has NEVER DERIVED a consumer filter — an entry
	// written by a Refractor that predates this field, or a lens that has not
	// reached its filter derivation yet. It does NOT mean "broad": a lens on
	// the broad filter says so, with a reason.
	//
	// It describes the SERVER-side delivery footprint only. A narrowed mode
	// does not mean the lens matches fewer rows, and a broad one is never
	// incorrect — the narrowed filter is strictly an optimization over it.
	FilterMode string `json:"filterMode,omitempty"`
	// FilterLabelCount is how many vertex-type labels the narrowed filter
	// actually carries, and 0 whenever FilterMode is "broad". It is the number
	// of labels the filter was BUILT from, not the number the cypher mentions:
	// a `*` label contributes its resolved concrete types instead of itself, so
	// this can move without the cypher changing. Read it beside
	// filterBroadReason "label-cap", whose threshold it is measured against.
	FilterLabelCount int `json:"filterLabelCount,omitempty"`
	// FilterBroadReason is why this lens took the broad filter — one of the
	// FilterBroadReason constants above — and "" (absent) whenever FilterMode
	// is narrowed. Absent WITH an absent filterMode carries no claim at all;
	// absent with a narrowed filterMode means there is nothing to explain.
	//
	// It names the cause the derivation actually acted on, not every condition
	// that happened to hold: a lens can be both non-exhaustive and over the
	// label cap, and the reason is the one that decided the outcome.
	FilterBroadReason string `json:"filterBroadReason,omitempty"`
}
