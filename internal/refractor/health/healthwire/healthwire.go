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
//   - install-incomplete — the filter was derived before the lens's install
//     stages finished: its cypher DECLARES an actor anchor while its enumerator
//     is not installed yet, so the actor-aware conjuncts could not be evaluated
//     at all. The one reason that reports a HOST bug (a `cmd/refractor` install
//     stage ordered after the derivation) rather than a property of the lens,
//     and the only one whose broad filter is a refusal to answer rather than an
//     answer of "cannot narrow".
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
	FilterBroadReasonInstallIncomplete    = "install-incomplete"
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
	// PeakBindingRows is the largest binding set any of this lens's recent
	// evaluations materialized at one time — the high-water mark, over a
	// rolling window of the most recent evaluations, of the same per-stage row
	// count the full engine's binding-set cap refuses on. It is a COST gauge,
	// the counterpart to ProjectionLag's throughput one: lag says the lens is
	// behind, this says how expensive one evaluation is.
	//
	// Read it against the cap (REFRACTOR_MAX_BINDINGS, 1,000,000 by default).
	// A refused evaluation's peak is included, so a lens that has just been
	// refused reports the row count that refused it instead of leaving an
	// operator to reconstruct it. A lens sitting within an order of magnitude
	// of the cap is the signal that its query materializes a product it does
	// not need.
	//
	// The window is rolling and per-process: it holds only samples this
	// Refractor recorded, and it is never written while empty, so a restart
	// leaves the last real observation standing rather than blanking it to
	// zero. ABSENT means no evaluation has ever reported one for this lens —
	// a lens that has not evaluated, or an entry written by a Refractor that
	// predates the field. A recorded peak of 0 (an evaluation whose first
	// pattern matched nothing) is also absent from the wire by omitempty; the
	// distinction does not matter to a reader, since neither is a cost.
	PeakBindingRows uint64 `json:"peakBindingRows,omitempty"`
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
	// AuditCursor is the plain-lens divergence audit's round-robin position —
	// the last anchor vertex key its pass examined
	// (lens-projection-divergence-audit-design.md §4.3). Like SweepCursor it
	// lives on the existing health entry rather than in new state, so a redeploy
	// resumes the walk; without it a cell that restarts more often than a cycle
	// completes would re-audit the head forever and never reach the tail, while
	// publishing a verdict that reads clean. "" for every lens that is not
	// audited.
	AuditCursor string `json:"auditCursor,omitempty"`
	// AuditCycleCompletedAt is when the audit last reached the END of its anchor
	// listing — RFC3339 UTC, "" before the first completed cycle. It is what a
	// clean verdict is worth: one pass covers at most one batch, so
	// `divergentRows: 0` says nothing about the whole lens until a cycle has
	// closed over it. Restored at startup beside the cursor, so a redeploy does
	// not silently retract a coverage claim the lens has already earned.
	AuditCycleCompletedAt string `json:"auditCycleCompletedAt,omitempty"`
	// PersonalSweepCursor is the personal convergence sweep's round-robin
	// position — the last identity it re-drove
	// (personal-lens-grant-change-trigger-design.md §4.3). Unlike SweepCursor it
	// is written by ONE process-level walk shared by every personal lens, and
	// fanned out to each of their entries, because the question it answers ("do
	// this lens's rows have a standing healer, and where has it got to") is
	// per-lens even though the mechanism is not. It is not restored at startup:
	// a restart re-starts the cycle from the top of the population, which is the
	// safe direction. "" for every lens the sweep does not drive.
	PersonalSweepCursor string `json:"personalSweepCursor,omitempty"`
	// PersonalSweepCycleCompletedAt is when that sweep last reached the END of
	// the identity population — RFC3339 UTC, "" before the first completed
	// cycle. It is what a healthy-looking cursor is worth: a tick covers at most
	// one batch of identities, so a moving cursor says the backstop is alive
	// while only a closed cycle says it has covered the plane.
	PersonalSweepCycleCompletedAt string `json:"personalSweepCycleCompletedAt,omitempty"`
	// PersonalSweepQueueDepth is how many actors the grant-change drain still
	// owes a reprojection at the moment the sweep published — the fast path's
	// backlog gauge, carried on the sweep's write because the sweep is the only
	// thing that reports on a schedule. A depth that keeps climbing is a mass
	// grant change outpacing the drain, which is the shape that ends in the
	// coalescing set overflowing (and that overflow raises its own fault).
	PersonalSweepQueueDepth uint64 `json:"personalSweepQueueDepth,omitempty"`
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
	// StructuralAutoRecoveredAt is when this lens last cleared a STRUCTURAL
	// pause without an operator — its own probe re-verified the condition the
	// pause was raised on and the consumer resumed. RFC3339 UTC; "" for a lens
	// that has never self-healed, which is the whole corpus until one does.
	//
	// A timestamp rather than a flag because the recovery is an EVENT and the
	// entry it lands on reads `active` like any other: only the age of this
	// stamp separates "healed a moment ago" from "healed last week", and a
	// reader that wants to alert on the former needs the difference.
	StructuralAutoRecoveredAt string `json:"structuralAutoRecoveredAt,omitempty"`
	// StructuralAutoRecoveredCause is the diagnosis the pause was carrying when
	// it cleared — the lastError an operator would have read while the lens was
	// dark, kept after the pause itself is gone. It is what decides whether
	// anything is still owed: the pause's own backlog replays on resume, so a
	// cause an operator fixed in the schema costs nothing, while one cleared by
	// re-provisioning or restoring the target left every earlier row
	// unreplayable and owes a full rebuild. Nothing else survives the recovery
	// to tell those apart.
	StructuralAutoRecoveredCause string `json:"structuralAutoRecoveredCause,omitempty"`
	// StructuralAutoRecoveryAttempts is which self-heal attempt lifted the
	// pause, counting from 1. It is the lens's distance from the consumer's
	// relapse latch, which stops probing altogether once a run of self-heals has
	// each failed to hold — so a recovery reported at 1 healed cleanly, and one
	// reported near the limit is a lens flapping, whose next relapse hands
	// control back to a human.
	StructuralAutoRecoveryAttempts int `json:"structuralAutoRecoveryAttempts,omitempty"`
}
