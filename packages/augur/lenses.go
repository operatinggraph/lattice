package augur

import "github.com/asolgan/lattice/internal/pkgmgr"

// AugurProposalsBucket is the NATS-KV read model the augurProposals lens projects
// into — the **P5 query surface** for "what reasoning proposals exist and what is
// their review verdict". Loupe (the operator inspector) reads THIS projected
// bucket (one row per proposal, keyed by the proposal key) to render the
// human-in-the-loop review surface — list pending proposals, show the model's
// proposed action + rationale + confidence, and (Fire 2) approve / reject — never
// Core KV (lattice-architecture.md P5 — lenses are the only application query
// surface). The Refractor auto-creates the bucket on lens load.
const AugurProposalsBucket = "augur-proposals"

// Lenses returns the package's two lenses.
//
// augurProposals is a FLAT projection (no aggregation / WITH, no link walks) —
// one row per augurproposal vertex, the same clean shape clinic-domain's
// clinicAppointments / clinicPatients use. Every display column is read off the
// proposal's own aspects by the documented node.<aspect>.data.<field> form; the
// candidate + target keys come from the TRUSTED .gap aspect (the instanceOp-
// minted escalation context), so no forCandidate / forTarget walk is needed for
// display and the row stays strictly one-per-proposal.
//
// The lens surfaces the WHOLE proposal lifecycle, not just completed verdicts: a
// claim minted by CreateAugurReasoningClaim (reasoning still in flight) projects
// its .gap context with a null reviewState, and once RecordProposal lands the
// model-derived columns + the pending|invalid verdict fill in. Loupe renders "in
// flight" for a null state and the verdict otherwise.
//
// Read-model only (the trusted-tool posture): NOT protected. The proposal
// vertices it projects are Weaver/bridge-authored orchestration state; this is
// the operator's window onto them, read like any other P5 read-model bucket.
//
// augurDispatchPending IS a weaver-target convergence lens (design Fire 2b
// §3.1) — see its doc comment below.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "augurProposals",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        AugurProposalsBucket,
			Engine:        "full",
			Spec:          augurProposalsSpec,
		},
		{
			CanonicalName:  "augurDispatchPending",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           augurDispatchPendingSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "augurproposal",
				OutputKeyPattern: "augurDispatch.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_dispatch", "entityKey", "proposedAction", "proposedParams", "candidateKey", "targetMetaKey", "originGap"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

// augurDispatchPendingSpec is the augurDispatch convergence target's
// actor-aggregate lens (design Fire 2b §3.1) — the pickup transport that closes
// the Augur loop's last hop. It anchors on ONE proposal per reprojection
// ($actorKey — the same single-anchor, no-hop shape internal/bootstrap's
// capabilityRead uses) and, via the Output descriptor's KeyColumn +
// OutputKeyPattern (`augurDispatch.{actorSuffix}`, actorSuffix = the anchor's
// bare NanoID), lands the row in the shared primordial `weaver-targets` bucket
// under the `augurDispatch.` prefix — so Weaver's existing lane-1
// watch/mark/lease/sweep picks up an approved proposal like any other
// convergence gap. Zero new pickup path; the composed key needs no string
// concatenation in cypher (the full engine's function set has none) because
// BuildKey composes it in Go from the anchor key alone.
//
// `violating = (reviewState = "approved")` is the ONLY dispatching state and is
// default-deny: a pending / rejected / invalid / dispatched / superseded
// proposal — or a claim still in flight (null reviewState) — projects
// violating=false (no dispatch). The row is 1:1 with the proposal vertex, so
// the approved→dispatched flip is a single-row column overwrite (`violating`
// retracts via the ordinary §10.2 upsert) — no negative/filter-retraction
// primitive needed (design §3.1, §4).
//
// candidateKey / targetMetaKey come from the TRUSTED .gap aspect (the
// instanceOp-minted escalation context) — never from the model's .proposed
// reply — so the dispatch-time §5 scope re-check (internal/weaver, buildPlan's
// actionProposedOp case) has a trusted anchor. proposedAction/proposedParams
// project the model's remediation verbatim (the same JSON-map-column shape the
// augurProposals lens already uses for non-scalar columns). '=' is the full
// engine's equality test (not '=='); a null reviewState compares false, never
// erroring (equalsAny's nil-safe rule).
const augurDispatchPendingSpec = `
MATCH (pr:augurproposal {key: $actorKey})
RETURN
  pr.key AS actorKey,
  pr.key AS entityKey,
  pr.proposed.data.action AS proposedAction,
  pr.proposed.data.params AS proposedParams,
  pr.gap.data.entityId AS candidateKey,
  pr.gap.data.targetId AS targetMetaKey,
  pr.gap.data.gapColumn AS originGap,
  (pr.review.data.state = "approved") AS missing_dispatch,
  (pr.review.data.state = "approved") AS violating
`

// augurProposalsSpec projects one row per augurproposal vertex. Flat (no-WITH, no
// OPTIONAL walk) like clinicPatients: the per-row key is `key` (the proposal key,
// the IntoKey default), so the read model is keyed by vtx.augurproposal.<handle>;
// proposalKey repeats it in the body for client-side reference.
//
//   - .gap {targetId, entityId, gapColumn, trigger} — the TRUSTED escalation
//     context the CreateAugurReasoningClaim instanceOp minted write-ahead. entityId
//     / targetId are full keys (vtx.leaseapp.<id> / vtx.meta.<id>), so a reader
//     derives the candidate type + target from them without a link walk.
//   - .proposed {action, params} — the model's remediation. proposedParams is a
//     non-scalar (map) projected verbatim, stored as JSON (the same shape
//     clinicProviders uses for the timeOff / hours arrays); the reviewer reads it
//     to see exactly what would be dispatched on approval.
//   - .rationale.text / .confidence.score / .provenance.{model, reasonedAt} — the
//     reasoning audit: why the model proposed this, its self-reported 0..1
//     confidence, and the provenance the operator weighs the proposal against.
//   - .review {state, invalidReason, reviewedAt, dispatchedAt} — the verdict.
//     reviewState is null while the claim's reasoning is in flight, then
//     pending|invalid once RecordProposal records the §5-validated verdict
//     (invalidReason carries the auditable reason on an invalid). reviewedAt /
//     dispatchedAt are the Fire-2 approve / dispatch stamps (null until then).
//
// All aspect reads are null-safe by key-shape: a not-yet-written aspect projects
// null (the same null-safe discipline clinicAppointments applies to .reminder /
// .encounter), so a claim-in-flight row projects cleanly with null model columns.
const augurProposalsSpec = `MATCH (pr:augurproposal)
RETURN
  pr.key AS key,
  pr.key AS proposalKey,
  pr.gap.data.targetId AS targetId,
  pr.gap.data.entityId AS entityId,
  pr.gap.data.gapColumn AS gapColumn,
  pr.gap.data.trigger AS trigger,
  pr.proposed.data.action AS proposedAction,
  pr.proposed.data.params AS proposedParams,
  pr.rationale.data.text AS rationale,
  pr.confidence.data.score AS confidence,
  pr.provenance.data.model AS model,
  pr.provenance.data.reasonedAt AS reasonedAt,
  pr.review.data.state AS reviewState,
  pr.review.data.invalidReason AS invalidReason,
  pr.review.data.reviewedAt AS reviewedAt,
  pr.review.data.dispatchedAt AS dispatchedAt`
