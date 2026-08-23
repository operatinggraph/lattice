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
// while the proposal has NEITHER a .claim aspect (CreateAuthoringClaim hasn't
// run) NOR a .artifact aspect (nobody has authored one) — null-safe `= null`
// presence tests (the full engine's documented null-test form; never IS NULL,
// per packages/lease-signing's lenses.go note). Once CreateAuthoringClaim
// writes the create-only .claim aspect, the SAME row reprojects
// missing_authoring=false, closing the gap — no negative/filter-retraction
// primitive needed (a single-row column overwrite, mirroring
// augurDispatchPending's approved→dispatched flip).
//
// The .artifact half of that conjunction is what keeps the HUMAN authoring
// lane out of the AI lane's dispatch. A SubmitCapabilityProposal proposal is
// born with .request and .artifact but deliberately no .claim — on a
// claim-only predicate it is indistinguishable from "reasoning not yet
// dispatched", so this target would trigger the capabilityAuthor Loom pattern
// against it: an unrequested reasoning call carrying the operator's own
// rationale as the prompt, whose RecordCapabilityProposal reply would then
// fail forever (its create-only aspect writes collide with the ones the
// submit already committed). A proposal that already carries an artifact has
// no authoring gap by definition, whoever wrote it.
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
// capabilityAuthorPackages is a FLAT scan of every installed package's
// manifest, projected into the SAME bucket as capabilityAuthorContext. Two
// lenses, one bucket, disjoint key spaces (`vtx.package.*` here,
// `vtx.meta.*` there) — the packages/one-bill posture, where four plain
// lenses share one-bill-history and collision-freedom rests on the vertex-type
// key segment alone. Both are plain projections: no ProjectionKind, no Output
// descriptor and no DiffRetraction, which is what keeps them unguarded and
// so never bucket-wide truncated (internal/refractor/projection/driver.go's
// RequiresGuard, and Pipeline.RebuildTruncateIsScoped, which refuses an
// unscoped rebuild in a shared bucket).
//
// It exists because package OWNERSHIP of a meta key is discoverable ONLY by
// scanning `vtx.package.*.manifest.declaredKeys` — no `declaredBy` link or
// aspect exists on a meta — and the bridge is denied Core KV, so it cannot
// scan. cmd/loupe computes the same reverse index off its own Core KV scan
// (P5's sole inspector exception); this lens is the non-Loupe equivalent, the
// surface the capabilityAuthor adapter reads to answer "which package owns the
// target I am being asked to edit, at what version, and what else does that
// package declare".
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
		{
			CanonicalName: "capabilityAuthorPackages",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        CapabilityAuthorContextBucket,
			Engine:        "full",
			Spec:          capabilityAuthorPackagesSpec,
		},
	}
}

const capabilityAuthorPendingSpec = `
MATCH (p:capabilityproposal {key: $actorKey})
RETURN
  p.key AS entityKey,
  ((p.claim.data.claimedAt = null) AND (p.artifact.data.kind = null)) AS missing_authoring,
  ((p.claim.data.claimedAt = null) AND (p.artifact.data.kind = null)) AS violating
`

// capabilityProposalsSpec projects one row per capabilityproposal vertex,
// keyed by the proposal's own key (the IntoKey default). Every aspect the
// capture pair (RequestCapabilityAuthoring / RecordCapabilityProposal) or the
// single-op human path (SubmitCapabilityProposal) can write is surfaced so an
// operator sees the full episode lifecycle — a request with no artifact yet
// (reasoning in flight) projects cleanly with null artifact/review columns,
// the same claim-in-flight shape augurProposals documents.
//
// `source` is what a review queue badges origin from: 'ai' for the
// bridge-recorded lane, 'operator' for a directly-submitted human artifact. It
// projects null for a proposal recorded before the field existed and for one
// whose reasoning is still in flight — in both cases no artifact has been
// authored yet or the only author available was the AI.
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
  p.provenance.data.source AS source,
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
// description populates for any meta carrying one (a DDL, lens, weaverTarget,
// or the weaverTarget's own authored `.description` aspect); spec is the
// FULL aspect-body projection (in-repo precedent: identity-domain's
// `u.credentialBinding.data AS binding`) and populates only for
// meta.lens/meta.weaverTarget/meta.loomPattern rows — the `.spec` aspect
// internal/pkgmgr/build.go writes for those three kinds (existing lens
// bodies: what cypher looks like, which columns exist; existing weaverTarget
// bodies: style + collision avoidance), never for a DDL. The remaining five
// self-description columns (the DDL self-description aspects
// internal/aiagent's cold-start traversal also reads) populate only for
// meta.ddl.vertexType/meta.ddl.eventType rows and project null otherwise —
// the same shape buildOpGroups already handles by skipping any meta with an
// empty permittedCommands.
const capabilityAuthorContextSpec = `MATCH (m:meta)
RETURN
  m.key AS key,
  m.class AS class,
  m.canonicalName.data.value AS canonicalName,
  m.description.data.text AS description,
  m.spec.data AS spec,
  m.permittedCommands.data.commands AS permittedCommands,
  m.inputSchema.data.schema AS inputSchema,
  m.outputSchema.data.schema AS outputSchema,
  m.fieldDescription.data.fieldDescriptions AS fieldDescriptions,
  m.examples.data.examples AS examples
`

// capabilityAuthorPackagesSpec projects one row per installed package vertex,
// keyed by the package's own key. The manifest fields a reader needs to resolve
// ownership are read straight off the `.manifest` aspect
// internal/pkgmgr/build.go writes at install: `name` and `version` identify the
// package an upgrade must name and the version it must be authored against, and
// `declaredKeys` is the full list of Core KV keys that install wrote — the only
// place a meta's owning package is recorded. A package vertex whose manifest
// aspect is absent (an install caught mid-batch) projects null columns rather
// than dropping out, so a reader sees the package exists and simply cannot
// claim any key.
//
// `description` and `depends` ride along for the opposite reason: not to resolve
// an upgrade but to REFUSE one. build.go rewrites the whole manifest aspect from
// the submitted Definition, and a Definition materialized from a capability
// proposal carries neither field — so a reader proposing an in-place upgrade has
// to see them to know it would blank them.
const capabilityAuthorPackagesSpec = `MATCH (p:package)
RETURN
  p.key AS key,
  p.manifest.data.name AS name,
  p.manifest.data.version AS version,
  p.manifest.data.description AS description,
  p.manifest.data.depends AS depends,
  p.manifest.data.declaredKeys AS declaredKeys
`
