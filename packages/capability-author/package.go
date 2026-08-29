// Package capabilityauthor is the AI-authored-capabilities data + safety
// foundation (ai-authored-capabilities-design.md) — capture + escalation
// dispatch (Fire 1), review + apply (Fire 2), the weaverTarget/loomPattern
// artifact kinds (Fire 3), and the Starlark-bearing vertexTypeDDL/opMeta
// artifact kinds (Fire 4).
//
// A Lattice-aware agent turns a capability REQUEST ("a lens listing active
// providers by specialty") into a proposed package artifact, deterministically
// validated, and applied only after a human approves — lifting the Augur
// pattern (AI proposes → validate → human gate → Processor writes) from
// arranging existing ops to authoring new package capabilities.
//
// This package declares:
//
//   - The `capabilityproposal` DDL — the proposal vertex type + the capture
//     pair for one authoring episode:
//
//   - RequestCapabilityAuthoring mints the proposal vertex write-ahead with
//     the requester + intent (no artifact yet).
//
//   - RecordCapabilityProposal carries a proposed artifact + its
//     ALREADY-COMPUTED §5 deterministic-validation verdict (in the full
//     design, computed by the bridge via pkgmgr.ValidateCapabilityArtifact
//     before submission) and stores review.state = pending | invalid.
//
//   - RecordAuthoringDispatch is the externalTask dispatchOp the bridge
//     submits when its capabilityAuthor adapter returns Pending instead of
//     resolving synchronously (natural-language-weaver-targets-design.md
//     §3.2) — a create-only .dispatch pending marker on the claim vertex,
//     mirroring lease-signing's RecordServiceDispatch. CreateAuthoringClaim's
//     emitted external.capabilityAuthor event names it as the dispatchOp.
//
//     Proposal shape (D5 — minimal root, business data in aspects):
//
//     vtx.capabilityproposal.<id>   root data = {}
//     .request     { requesterId, intent, contextRef }
//     .claim       { claimedAt, claimKey }
//     .artifact    { kind, content }
//     .target      { mode, packageName, baseVersion, newVersion }
//     .rationale   { text }
//     .confidence  { score }
//     .validation  { state, report, deltaPreview, checkedAt }
//     .provenance  { model, promptHash, catalogHash, reasonedAt }
//     .review      { state, invalidReason, reviewedAt, appliedAt, appliedByOp }
//     lnk.capabilityproposal.<id>.requestedBy.<type>.<requesterId>
//
//   - The `capabilityauthorclaim` DDL + the `capabilityAuthor` Loom pattern —
//     the escalation dispatch (design §3.4): a `capabilityAuthorPending`
//     weaver-target lens self-anchored on `capabilityproposal` triggers
//     `triggerLoom(capabilityAuthor)` while a proposal's `.claim` aspect is
//     absent; the pattern's sole externalTask step submits CreateAuthoringClaim
//     (mints the correlation-claim vertex + writes the `.claim` aspect,
//     closing the lens gap) and parks for the bridge's RecordCapabilityProposal.
//
//   - The Go-side deterministic materializer (internal/pkgmgr,
//     ValidateCapabilityArtifact) — the §5 record-time validation boundary for
//     the "lens" kind (parses the proposed cypher with the real openCypher
//     parser and runs the artifact through the same validateAll the human
//     package-authoring path uses, reused not duplicated), the "grant" kind
//     (full Contract #6 permission-identity validation plus the scope check:
//     the artifact's operationType+scope must be a subset of what the
//     requesting operator already holds — the property that makes it safe to
//     let an AI author authority-widening artifacts at all), the
//     "weaverTarget"/"loomPattern" kinds (the same validateWeaverTargets/
//     validateLoomPatterns a hand-authored package's §10.8/§10.5 declarations
//     run through; a weaverTarget artifact may not carry an `augur` escalation
//     block — out of scope for an AI to configure its own reasoning-escalation
//     policy), and the "vertexTypeDDL"/"opMeta" kinds (Fire 4 — a
//     verified-pure internal/starlarksandbox.Validate dry-run of a
//     vertexTypeDDL's Script, plus the sensitive-ref-mac-provenance-design.md
//     §7 condition-2 lint: no artifact of any kind may spell the literal
//     "$sensitiveRef", and an opMeta's declared Dispatch.Reads may never name
//     a sensitive-classed aspect — an AI-authored capability that needs PII
//     egress routes to human authoring instead).
//
//   - A `lattice capability list`/`review` CLI review-and-apply affordance
//     (cmd/lattice/capability): lists proposals from the capabilityProposals
//     Lens and submits ReviewCapabilityProposal, re-running the §5 boundary
//     fresh on approve.
//
//   - Permissions granting RequestCapabilityAuthoring + CreateAuthoringClaim +
//     RecordCapabilityProposal + ReviewCapabilityProposal to `operator` (the
//     human requester / Loom's relay actor / the trusted bridge-equivalent
//     submitter / the human reviewer — the same operator-equivalent idiom
//     augur's + lease-signing's capture pairs use).
//
//   - Three P5 read-model lenses (the operator/reasoning-model query surface,
//     lattice-architecture.md P5): `capabilityProposals` (flat, one row per
//     proposal — the review surface Loupe renders), `capabilityAuthorContext`
//     (a flat scan of every installed `vtx.meta.*` DDL/lens/target/pattern, the
//     same installed-DDL self-description catalog `cmd/loupe/ops.go`'s
//     buildOpGroups computes by scanning Core KV directly — this lens is the
//     non-Loupe equivalent so the bridge/reasoning adapter never needs Core KV
//     access), and `capabilityAuthorPackages` (a flat scan of every installed
//     package's manifest — name, version, description, depends, declaredKeys —
//     sharing the context
//     bucket on a disjoint `vtx.package.*` key space, the surface that answers
//     "which package declared this meta key, at what version", the reverse
//     index `cmd/loupe/lens.go`'s buildLensPackageIndex computes from its own
//     Core KV scan).
//
//   - ReviewCapabilityProposal (design §3.3) — the human verdict op: a
//     capability-authorized operator flips a PENDING proposal to approved or
//     rejected, addressed directly by its own proposalId. An approve re-runs
//     the §5 boundary against the LIVE catalog (the TRUSTED caller attaches a
//     fresh validation verdict; a missing or non-"valid" one fail-closes to
//     invalid); a reject needs no re-check.
//
//   - The F-004 apply path + the `applied` flip (design §3.5, closes the
//     loop): pkgmgr.CapabilityApplyPlanForProposal reads an APPROVED
//     proposal's stored artifact + target and materializes the SAME
//     Definition §5 already validated; the operator submits it through the
//     existing, UNMODIFIED F-004 InstallPackage/UpgradePackage op (a
//     separate Processor commit — this package does not special-case those
//     ops); MarkCapabilityProposalApplied then records the applied-flip
//     (review.state approved→applied, appliedAt/appliedByOp, the appliedAs
//     link to the resulting vtx.package.<id> vertex). Only an approved
//     proposal may be marked applied (fail-closed, no double-apply).
//
//   - The `capabilityAuthor` bridge adapter (`bridge.NewCapabilityAuthor`):
//     reasons over this package's capabilityAuthorContext catalog through the
//     model-runner fleet, which holds the vendor credential. `cmd/bridge`
//     registers it only when BRIDGE_CAPABILITY_AUTHOR=real; the ordinary
//     unset default leaves an authoring request on the adapter-missing path
//     (ack + a health issue). `FakeCapabilityAuthor` is a deterministic
//     in-memory double exercised by internal/bridge's own tests, registered
//     by no binary.
//
//   - Operator review surfaces for the queue above: the `lattice-pkg` CLI's
//     review-and-apply affordance, and Loupe's AI review console
//     (`GET /api/review/capability(/<id>)` plus its approve / apply /
//     mark-applied endpoints, over the capabilityProposals read model).
//
// Install via the InstallPackage kernel op. See docs/components/_packages.md
// and _bmad-output/implementation-artifacts/ai-authored-capabilities-design.md.
package capabilityauthor

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "capability-author",
	Version:       "0.13.0",
	Description:   "AI-authored capabilities — Fire 1 capture + escalation dispatch + P5 read models, Fire 2 review + apply + a CLI review-and-apply affordance, Fire 3 weaverTarget/loomPattern artifact kinds, Fire 4 Starlark-bearing vertexTypeDDL/opMeta artifact kinds, and the async DispatchOp + catalog widening for the real model-backed adapter (natural-language-weaver-targets-design.md): the capabilityproposal + capabilityauthorclaim vertex types, the RequestCapabilityAuthoring/CreateAuthoringClaim/RecordCapabilityProposal/RecordAuthoringDispatch/ReviewCapabilityProposal/MarkCapabilityProposalApplied/RecordCapabilityInstallReceipt ops (§5 record-time + approve-time deterministic-validation boundary for the lens/grant/weaverTarget/loomPattern/vertexTypeDDL/opMeta kinds, plus the F-004-apply-then-mark-applied loop closer and the create-only .install receipt binding a proposal to the ONE install/upgrade commit that produced its package), the capabilityAuthorPending weaver-target lens, the capabilityAuthor Loom pattern, and the capabilityProposals/capabilityAuthorContext/capabilityAuthorPackages review + catalog + manifest lenses (the catalog one also projecting the full `.spec` aspect body so a reasoning model sees existing lens/weaverTarget bodies, not just self-description; the manifest one projecting each installed package's name/version/description/depends/declaredKeys so a Core-KV-denied reader can resolve which package owns a meta key and what an in-place upgrade of it would blank). SubmitCapabilityProposal opens a second, human authoring lane into the same review queue — an operator submits an artifact they composed themselves in one op, with no authoring-claim indirection, and a declared provenance.source ('ai' | 'operator') tells the two apart. RecordAuthoringDispatch is the externalTask dispatchOp CreateAuthoringClaim's emitted event now names, so the bridge's async Pending/poll path is dispatchable for this adapter.",
	Depends:       []string{"orchestration-base"},
	DDLs:          DDLs(),
	Permissions:   Permissions(),
	OpMetas:       OpMetas(),
	WeaverTargets: WeaverTargets(),
	LoomPatterns:  LoomPatterns(),
	Lenses:        Lenses(),
}
