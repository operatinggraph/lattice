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
//     policy in this increment), and the "vertexTypeDDL"/"opMeta" kinds (Fire
//     4 — a verified-pure internal/starlarksandbox.Validate dry-run of a
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
//   - Two P5 read-model lenses (the operator/reasoning-model query surface,
//     lattice-architecture.md P5): `capabilityProposals` (flat, one row per
//     proposal — the review surface Loupe renders) and
//     `capabilityAuthorContext` (a flat scan of every installed
//     `vtx.meta.*` DDL/lens/target/pattern, the same installed-DDL
//     self-description catalog `cmd/loupe/ops.go`'s buildOpGroups computes by
//     scanning Core KV directly — this lens is the non-Loupe equivalent so
//     the bridge/reasoning adapter never needs Core KV access).
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
// Deliberately NOT yet built (the fire's remaining checkpoints, see the design
// doc): the real claude-opus-4-8-backed `capabilityAuthor` bridge adapter (only
// the deterministic `FakeCapabilityAuthor` ships — the same posture Augur's own
// adapter is still in); a Loupe UI affordance (the CLI one has shipped).
//
// Install via the InstallPackage kernel op. See docs/components/_packages.md
// and _bmad-output/implementation-artifacts/ai-authored-capabilities-design.md.
package capabilityauthor

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "capability-author",
	Version:       "0.7.0",
	Description:   "AI-authored capabilities — Fire 1 capture + escalation dispatch + P5 read models, Fire 2 review + apply + a CLI review-and-apply affordance, Fire 3 weaverTarget/loomPattern artifact kinds, and Fire 4 Starlark-bearing vertexTypeDDL/opMeta artifact kinds: the capabilityproposal + capabilityauthorclaim vertex types, the RequestCapabilityAuthoring/CreateAuthoringClaim/RecordCapabilityProposal/ReviewCapabilityProposal/MarkCapabilityProposalApplied ops (§5 record-time + approve-time deterministic-validation boundary for the lens/grant/weaverTarget/loomPattern/vertexTypeDDL/opMeta kinds, plus the F-004-apply-then-mark-applied loop closer), the capabilityAuthorPending weaver-target lens, the capabilityAuthor Loom pattern, and the capabilityProposals/capabilityAuthorContext review + catalog lenses. A Loupe UI affordance lands in a later increment.",
	Depends:       []string{"orchestration-base"},
	DDLs:          DDLs(),
	Permissions:   Permissions(),
	OpMetas:       OpMetas(),
	WeaverTargets: WeaverTargets(),
	LoomPatterns:  LoomPatterns(),
	Lenses:        Lenses(),
}
