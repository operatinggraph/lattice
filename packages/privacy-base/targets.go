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
//     (lenses.go's identityErasureResidueSpec doc, "FIVE FAN-OUT ARMS").
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
//     attestation only if every one is clear.
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
// read through an UNDECLARED kv.Read: GapActionSpec carries no OptionalReads
// field (only Reads), so a Weaver directOp cannot pre-declare an
// absence-tolerant read the way a Loom systemOp step can. That is not a gap
// in this playbook: internal/processor/starlark_kv.go's kv.Read documents an
// undeclared read as tolerating absence exactly like a declared optionalRead
// does — it costs one live-read-budget unit and a round trip instead of a
// step-4 prefetch, nothing more — so the three DDL doc comments' "declared in
// contextHint.optionalReads by every dispatcher" overstates what THIS
// dispatcher does; the marker and its siblings still resolve correctly, just
// lazily.
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
// No gap declares a retry cap. A maxretries_<g> row column looked like the
// mechanism for capping a stuck sweep's re-dispatch, but the realized
// re-dispatch rate is roughly one attempt per 30-minute mark lease
// (internal/weaver/reconciler.go, evaluator.go's fireEpisode anti-storm drop
// on a live-leased mark) — at that pacing a cap sized to the sweep's own
// reachable ceiling is reached only after weeks, making it inert for the two
// sweep gaps, while a small cap sized to be reachable (the seal gap) turns a
// stuck-but-recoverable episode into a permanently parked one:
// escalateExhaustedGap alerts and returns without touching the mark
// (evaluator.go), so the mark TTL-expires, the reconciler stops re-arming it,
// and gapSuppressed keeps suppressing on the dispatch-count key forever. The
// obligation increment 5's residual 1 named (a bound on stuck re-dispatch) is
// real; maxretries_<g> is not a mechanism that discharges it at this
// re-dispatch cadence, so this playbook does not declare one.
//
// That residual's other option — routing the loud stop to a surface gap — is
// not available either, and for a reason that outlives the pacing: a surface
// gap fires on a missing_<g> column, and no column the lens can compute is
// true exactly when a sweep is stuck. A hard-failing sweep commits nothing, so
// it leaves every residue count at the value it already had; "stuck" is
// "this count stopped decreasing", which is a statement about history, and the
// row carries none. Expressing it needs either a per-entity attempt record or
// an elapsed-time predicate against the shred stamp, and neither exists on
// this row today. Until one does, a stuck sweep is visible as a residue count
// that stays put, not as a signal that announces itself.
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
