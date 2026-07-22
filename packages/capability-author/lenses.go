package capabilityauthor

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// CapabilityProposalsBucket is the NATS-KV read model capabilityProposals
// projects into — the P5 query surface for "what capability-authoring
// episodes exist and what is their review verdict". Loupe (the inspector)
// reads this bucket to render the human-in-the-loop review surface; the
// Refractor auto-creates the bucket on lens load (mirrors packages/augur's
// AugurProposalsBucket).
const CapabilityProposalsBucket = "capability-proposals"

// CapabilityAuthorContextBucket is the NATS-KV read model
// capabilityAuthorContext projects into — the platform's installed-DDL
// self-description catalog, the same surface the reasoning model needs to
// know what ops/lenses/targets/patterns already exist before authoring a new
// one (design §2, "the action/artifact catalog the model authors within").
const CapabilityAuthorContextBucket = "capability-author-context"

// Lenses returns the package's Lens declarations.
//
// capabilityAuthorPending is the escalation-dispatch weaver-target
// convergence lens (Contract #10 §10.2) — SELF-ANCHORED, not
// neighbor-projected: it anchors on ONE capabilityproposal vertex per
// reprojection ($actorKey), the same single-anchor no-hop shape
// packages/augur's augurDispatchPending lens uses. missing_authoring is true
// while the proposal's OWN .claim aspect is absent (CreateAuthoringClaim
// hasn't run yet) — a null-safe `= null` presence test (the full engine's
// documented null-test form; never IS NULL, per packages/lease-signing's
// lenses.go note). Once CreateAuthoringClaim writes the create-only .claim
// aspect, the SAME row reprojects missing_authoring=false, closing the gap —
// no negative/filter-retraction primitive needed (a single-row column
// overwrite, mirroring augurDispatchPending's approved→dispatched flip).
//
// capabilityProposals is the FLAT operator review lens (design §3.5, the
// Fire-1 checkpoint's remaining P5 read model) — one row per
// capabilityproposal vertex, mirroring packages/augur's augurProposals
// exactly: no aggregation/WITH/link walk, every column a null-safe
// node.<aspect>.data.<field> read off the proposal's own aspects (a claim
// still in flight, or a request not yet authored, projects cleanly with null
// downstream columns). Read-model only, NOT protected (the same
// trusted-tool posture augurProposals documents) — this is the operator's
// window onto Weaver/bridge-authored orchestration state, not
// business/PII data.
//
// capabilityAuthorContext is a FLAT, platform-wide scan of every
// `vtx.meta.<NanoID>` vertex (label `meta` — the key TYPE segment every
// DDL/lens/weaverTarget/loomPattern meta-vertex shares regardless of its own
// `class`; the engine's nodeMatches resolves labels off the key, not the
// class field) — the same installed-DDL self-description surface
// cmd/loupe/ops.go's buildOpGroups computes by scanning Core KV directly
// (Loupe is P5's sole exception; this lens is the non-Loupe equivalent so
// the capabilityAuthor bridge adapter can read it like any other P5
// read-model, never scanning Core KV itself). Unfiltered by class — a
// non-DDL meta (a lens/weaverTarget/loomPattern) simply projects null
// self-description columns (canonicalName + class always populate; the
// consuming reader distinguishes rows by `class`, exactly as buildOpGroups
// already does client-side after its own scan). The full engine has no
// STARTS WITH/string-prefix operator, so a class-discriminating WHERE isn't
// attempted here — that filtering stays the reader's job, unchanged from
// today's Loupe posture.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "capabilityAuthorPending",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           capabilityAuthorPendingSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "capabilityproposal",
				OutputKeyPattern: "capabilityAuthorDispatch.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_authoring", "entityKey"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		{
			CanonicalName: "capabilityProposals",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        CapabilityProposalsBucket,
			Engine:        "full",
			Spec:          capabilityProposalsSpec,
		},
		{
			CanonicalName: "capabilityAuthorContext",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        CapabilityAuthorContextBucket,
			Engine:        "full",
			Spec:          capabilityAuthorContextSpec,
		},
	}
}

const capabilityAuthorPendingSpec = `
MATCH (p:capabilityproposal {key: $actorKey})
RETURN
  p.key AS entityKey,
  (p.claim.data.claimedAt = null) AS missing_authoring,
  (p.claim.data.claimedAt = null) AS violating
`

// capabilityProposalsSpec projects one row per capabilityproposal vertex,
// keyed by the proposal's own key (the IntoKey default). Every aspect the
// capture pair (RequestCapabilityAuthoring / RecordCapabilityProposal) can
// write is surfaced so an operator sees the full episode lifecycle — a
// request with no artifact yet (reasoning in flight) projects cleanly with
// null artifact/review columns, the same claim-in-flight shape
// augurProposals documents.
const capabilityProposalsSpec = `MATCH (p:capabilityproposal)
RETURN
  p.key AS key,
  p.key AS proposalKey,
  p.request.data.requesterId AS requesterId,
  p.request.data.intent AS intent,
  p.request.data.contextRef AS contextRef,
  p.claim.data.claimedAt AS claimedAt,
  p.artifact.data.kind AS kind,
  p.artifact.data.content AS content,
  p.target.data.mode AS targetMode,
  p.target.data.packageName AS targetPackageName,
  p.target.data.baseVersion AS targetBaseVersion,
  p.target.data.newVersion AS targetNewVersion,
  p.rationale.data.text AS rationale,
  p.confidence.data.score AS confidence,
  p.validation.data.state AS validationState,
  p.validation.data.report AS validationReport,
  p.validation.data.deltaPreview AS validationDeltaPreview,
  p.validation.data.checkedAt AS validationCheckedAt,
  p.provenance.data.model AS model,
  p.provenance.data.promptHash AS promptHash,
  p.provenance.data.catalogHash AS catalogHash,
  p.provenance.data.reasonedAt AS reasonedAt,
  p.review.data.state AS reviewState,
  p.review.data.invalidReason AS reviewInvalidReason,
  p.review.data.reviewedAt AS reviewedAt,
  p.review.data.appliedAt AS appliedAt,
  p.review.data.appliedByOp AS appliedByOp
`

// capabilityAuthorContextSpec projects one row per installed meta-vertex,
// keyed by the meta's own key. canonicalName + class populate for every row;
// the remaining five self-description columns (the DDL self-description
// aspects internal/aiagent's cold-start traversal also reads) populate only
// for meta.ddl.vertexType/meta.ddl.eventType rows and project null
// otherwise — the same shape buildOpGroups already handles by skipping any
// meta with an empty permittedCommands.
const capabilityAuthorContextSpec = `MATCH (m:meta)
RETURN
  m.key AS key,
  m.class AS class,
  m.canonicalName.data.value AS canonicalName,
  m.description.data.text AS description,
  m.permittedCommands.data.commands AS permittedCommands,
  m.inputSchema.data.schema AS inputSchema,
  m.outputSchema.data.schema AS outputSchema,
  m.fieldDescription.data.fieldDescriptions AS fieldDescriptions,
  m.examples.data.examples AS examples
`
