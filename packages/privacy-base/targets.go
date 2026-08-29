package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// WeaverTargets returns the package's one meta.weaverTarget: the convergence
// loop over identityErasureResidue (lenses.go), the row that turns an
// erasureRequested marker into a completed erasure (erasure-orchestration-
// design.md §7.2). TargetID is ErasureCompleteTarget — the lens's
// OutputKeyPattern prefix, which is the only binding the Weaver resolves a
// target through (registry.go's Target doc); LensRef is the lens's own
// CanonicalName, deliberately different (lenses.go's ErasureCompleteTarget
// comment explains why that is allowed).
//
// Five gaps, three shapes:
//
//   - missing_credentialResidue → directOp UnbindIdentityCredentials
//     (identity-domain), missing_dedupResidue → directOp
//     PurgeIdentityDedupFootprint (this package). Both sweep one bounded page
//     of their respective residue per commit and re-open until the lens's
//     boundIn/boundOut/index/duplicateOut/duplicateIn counts reach zero
//     (lenses.go's identityErasureResidueSpec doc, "FIVE FAN-OUT ARMS"). Each
//     drains one ARM per commit, which is what sizes their retry caps — see the
//     cap block below.
//   - missing_vaultDestruction, missing_projectionNullify → surface, IssueSeverity
//     "warning". Both are the two ASYNC halves of ShredIdentityKey's
//     crypto-shred: the privacy-worker's Vault.ShredKey destruction and the
//     Refractor's keyshredded-listener projection nullification
//     (shredStatusSpec's own doc above). Neither has a directOp remediation
//     because neither has one: the Vault call and the Refractor's
//     NullifyTarget sweep are each driven by exactly one actor already, off
//     the privacy.keyShredded event, and a second driver racing that actor
//     would not make either finish faster — it would just be a second writer
//     of the same completion. What an operator needs here is visibility into a
//     stuck async half, which surface's Health-KV issue is; a
//     Weaver-dispatched remediation would be manufacturing a rescuer for a job
//     that already has one. "warning", not "error": the row exists from the
//     instant erasureRequested is written, and BOTH async halves are always
//     outstanding at that instant — every ordinary erasure passes through this
//     state for the whole in-flight window, which is the routine case, not a
//     failure to fulfil the responsibility (Contract #5 §5.2's line for
//     "error"). An "error" issue holds aggregateStatus at "unhealthy" for the
//     whole Weaver component (internal/weaver/health.go) for as long as ANY
//     erasure is in flight, which would be always, in any deployment that ever
//     processes one. orchestration-base's unroutedTasks — the only other
//     shipped surface gap — is "warning" for the same reason.
//   - missing_erasureSeal → directOp SealIdentityForErasureComplete (this
//     package), the terminal gap: it opens only once the other four are
//     closed (the lens orders it last), re-verifies all five residue arms and
//     both async halves inside its own commit, and writes the completion
//     attestation only if every one is clear. One commit, no paging — so its
//     cap is a judgement rather than a derivation (the cap block below).
//
// No gap sets Class. Each of the three dispatched operationTypes is admitted
// by exactly one installed vertexType DDL today (targets_test.go's
// TestWeaverTargets_DispatchedOpsAreUnambiguous pins it) — Class exists only
// to disambiguate the Processor's operationType→class reverse index when an
// operationType is admitted by two or more vertexType DDLs
// (internal/processor/ddl_cache.go), and pinning it where nothing is
// ambiguous would be a second place to keep in sync with no dispatch benefit.
// The day a second vertexType DDL claims one of these operationTypes, the
// dispatch fails closed and loudly (MissingClass) rather than guessing, which
// is why the non-ambiguity is pinned by a test instead of worked around here.
//
// No gap sets Params["expectedRevision"] — the engine injects it (the OCC
// revision-condition on every directOp dispatch); a package supplying it
// would collide with engine state and install rejects it outright.
//
// Reads: []string{"row.entityKey"} on all three directOp gaps. entityKey is
// the lens row's own i.key — the subject identity's own vertex key — and it
// is the ONE key each op's DDL requires in ContextHint.Reads (fail-closed:
// UnbindIdentityCredentialsDDL, PurgeIdentityDedupFootprintDDL and
// SealIdentityForErasureCompleteDDL's FieldDescription comments, and each
// script's vertex_alive(state, subject_key) call, which reads the hydrated
// snapshot rather than issuing a live GET). Every other key each script reads
// — the erasureRequested marker, piiKey, erasure, mergedInto, .state — is
// read through an UNDECLARED kv.Read. GapActionSpec does carry an
// OptionalReads field, so a Weaver directOp CAN pre-declare an
// absence-tolerant read the way a Loom systemOp step does; these three gaps
// deliberately do not. Declaring a key is not bookkeeping — the Processor
// serves it from the step-4 snapshot instead of a live read, it stops costing
// a live-read-budget unit, and a create off its observed absence becomes a
// CreateOnly assertion whose conflict the commit path absorbs on retry — so
// converting these three is a change to how the ERASURE path commits, and it
// owns the review that goes with that rather than riding in as a comment fix.
// Until then the reads resolve correctly, just lazily, and the three DDL doc
// comments' "declared in contextHint.optionalReads by every dispatcher"
// overstates what THIS dispatcher does.
//
// Enumerations on all three directOp gaps declare the class-(e) kv.Links walks
// those ops run (Contract #2 §2.5), each the set its own script annotates
// `# read-posture: (e)`. The two sweeps drain one arm per commit and declare
// the union of their arms: `boundTo` inbound + outbound for
// UnbindIdentityCredentials, `indexes` inbound plus `duplicateOf` outbound +
// inbound for PurgeIdentityDedupFootprint. The seal declares all five, because
// it re-walks every arm inside its own commit before it will attest — the
// union of both sweeps, off the same hub. For the two sweeps the identityErasure
// pattern's steps 3 and 4 declare the identical set, so the op carries the same
// walks whichever dispatcher submits it; the seal has only this dispatcher, the
// pattern completing without it.
//
// The hub of every walk is `row.entityKey`, the erased identity itself,
// resolved from the violation row exactly like the Reads entry above it. The
// declaration is metadata: each walk stays a bounded paged live kv.Links call
// inside the script, and the Processor validates the shape and otherwise
// ignores it. What it buys is that a reader of the playbook — and of the
// envelope — sees which relations these ops traverse without reading the
// Starlark.
//
// EACH OF THE THREE directOp GAPS IS RETRY-CAPPED, and the cap is what makes a
// stuck erasure stop loudly. A directOp gap is external-class outright
// (internal/weaver/evaluator.go's externalDispatchGap), so Contract #10 §10.3
// leaves the declared maxretries_<g> column as its only bound: the lens
// projects one per gap (lenses.go's identityErasureResidueSpec; the values and
// their derivation live in retry_budget.go), the Weaver counts dispatches per
// (target, entity, gap) in weaver-state, and reaching the cap raises the §10.8
// GapBudgetExhausted standing issue instead of re-dispatching forever.
//
// The two sweep gaps are capped at the summed reach of every arm their op
// sweeps (maxCredentialResidueRetries, maxDedupResidueRetries), so a cap binds
// only once every arm has run out of reach; the terminal seal, which does not
// page, matches the widest sibling (maxErasureSealRetries) so no gap on this
// target parks while another could still legitimately be converging. Neither
// gap needs a cap to TERMINATE — a draining sweep strictly decreases its count
// and §6 keeps the set closed — so a cap is purely the bound on a sweep that
// has stopped making progress.
//
// EXHAUSTION IS A PARK, AND THAT COST IS ACCEPTED HERE, NOT DENIED. It is
// self-sealing: escalateExhaustedGap alerts and returns without touching the
// mark, so the mark TTL-expires, the reconciler stops re-arming it, and because
// nothing dispatches after that, the residue never shrinks, the gap never
// closes, and the dispatch-count that would be deleted on close never is. The
// budget is what the caps above buy that against: sized to the ops' own reach,
// reaching one means the sweep is definitively not draining — the state in
// which continuing to re-dispatch was never going to erase anyone, and the
// alternative to a park is a stuck erasure grinding on forever with §10.8's
// escalation structurally unreachable and no operator ever told. An operator
// recovers a park by hand: clearing the weaver-state key
// <targetId>.<entityId>.<gapColumn>.__count restarts the budget, and is the
// right move once the cause (an unreachable-residue ceiling, a Vault or
// projection lag, a failing seal) is actually fixed.
//
// A budget spans the gap's whole open episode, not one cycle. A re-shred that
// lands while the seal gap is still open and unattested only changes the value
// the seal diffs against, so the column stays true throughout and the second
// cycle inherits the first's spent budget; a residue class that CLOSES and
// later re-opens starts a fresh one, because closing is what deletes the count.
//
// The loud stop is an alert on top of a signal that is already there, never a
// replacement for it: the erasure's incompleteness stays independently visible
// in the lens's residue counts and `violating`, and in the two surface gaps,
// whatever the budget does. That matters because no column the lens can compute
// is true exactly when a sweep is STUCK — a hard-failing sweep commits nothing,
// so it leaves every residue count at the value it already had, and "this count
// stopped decreasing" is a statement about history the row carries none of. The
// dispatch-count the budget bounds is the one place that history is kept, which
// is why the cap, and not a sixth surface gap, is what announces a stall.
//
// No gap declares inflight_<g>. That column suppresses a dispatch while a
// remediation is genuinely in flight, and each of these three ops is a single
// synchronous commit with no such window — declaring it would suppress nothing
// while opting the gap out of the budget above (evaluator.go's gapSuppressed
// declines the engine default for any row that declares the marker).
func WeaverTargets() []pkgmgr.WeaverTargetSpec {
	return []pkgmgr.WeaverTargetSpec{
		{
			TargetID: ErasureCompleteTarget,
			Description: "An identity that asked to be erased ends with no credentials or duplicate-index links " +
				"left, its key destroyed and its projections cleared, sealed by a completion attestation. " +
				"Leftover traces are swept; a stalled step is raised for an operator.",
			LensRef: "identityErasureResidue",
			Gaps: map[string]pkgmgr.GapActionSpec{
				"missing_credentialResidue": {
					Action:    "directOp",
					Operation: "UnbindIdentityCredentials",
					Params:    map[string]string{"subjectKey": "row.entityKey"},
					Reads:     []string{"row.entityKey"},
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"},
						{Hub: "row.entityKey", Relation: "boundTo", Direction: "out"},
					},
				},
				"missing_dedupResidue": {
					Action:    "directOp",
					Operation: "PurgeIdentityDedupFootprint",
					Params:    map[string]string{"subjectKey": "row.entityKey"},
					Reads:     []string{"row.entityKey"},
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.entityKey", Relation: "indexes", Direction: "in"},
						{Hub: "row.entityKey", Relation: "duplicateOf", Direction: "out"},
						{Hub: "row.entityKey", Relation: "duplicateOf", Direction: "in"},
					},
				},
				"missing_vaultDestruction": {
					Action:        "surface",
					IssueCode:     "ErasureVaultKeyNotDestroyed",
					IssueSeverity: "warning",
				},
				"missing_projectionNullify": {
					Action:        "surface",
					IssueCode:     "ErasureProjectionsNotNullified",
					IssueSeverity: "warning",
				},
				"missing_erasureSeal": {
					Action:    "directOp",
					Operation: "SealIdentityForErasureComplete",
					Params:    map[string]string{"subjectKey": "row.entityKey"},
					Reads:     []string{"row.entityKey"},
					// The seal re-walks all five residue arms inside its own
					// commit before it will attest, so it enumerates the union
					// of what the two sweeps do — the same five, off the same
					// hub.
					Enumerations: []pkgmgr.EnumerationSpec{
						{Hub: "row.entityKey", Relation: "boundTo", Direction: "in"},
						{Hub: "row.entityKey", Relation: "boundTo", Direction: "out"},
						{Hub: "row.entityKey", Relation: "indexes", Direction: "in"},
						{Hub: "row.entityKey", Relation: "duplicateOf", Direction: "out"},
						{Hub: "row.entityKey", Relation: "duplicateOf", Direction: "in"},
					},
				},
			},
		},
	}
}
