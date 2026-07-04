// Package capabilityauthor is the AI-authored-capabilities data + safety
// foundation (ai-authored-capabilities-design.md) — Increment 1 of Fire 1.
//
// A Lattice-aware agent turns a capability REQUEST ("a lens listing active
// providers by specialty") into a proposed package artifact, deterministically
// validated, and applied only after a human approves — lifting the Augur
// pattern (AI proposes → validate → human gate → Processor writes) from
// arranging existing ops to authoring new package capabilities.
//
// This increment declares:
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
//     .artifact    { kind, content }
//     .target      { mode, packageName, baseVersion, newVersion }
//     .rationale   { text }
//     .confidence  { score }
//     .validation  { state, report, deltaPreview, checkedAt }
//     .provenance  { model, promptHash, catalogHash, reasonedAt }
//     .review      { state, invalidReason, reviewedAt, appliedAt, appliedByOp }
//     lnk.capabilityproposal.<id>.requestedBy.<type>.<requesterId>
//
//   - The Go-side deterministic materializer (internal/pkgmgr,
//     ValidateCapabilityArtifact) — the §5 record-time validation boundary for
//     the "lens" kind: parses the proposed cypher with the real openCypher
//     parser and runs the artifact through the same validateAll the human
//     package-authoring path uses (reused, not duplicated).
//
//   - Permissions granting RequestCapabilityAuthoring + RecordCapabilityProposal
//     to `operator` (the human requester / the trusted bridge-equivalent
//     submitter — the same operator-equivalent idiom augur's capture pair uses).
//
// Deliberately NOT in this increment (the fire's remaining checkpoints, see
// the design doc): the `capabilityAuthor` bridge adapter + Loom pattern that
// auto-dispatches a request to the reasoning model; the `capability-proposals`
// review lens + `capability-author-context` catalog lens; ReviewCapabilityProposal
// + the F-004 apply path; the `grant`/`weaverTarget`/`loomPattern`/Starlark kinds.
//
// Install via the InstallPackage kernel op. See docs/components/_packages.md
// and _bmad-output/implementation-artifacts/ai-authored-capabilities-design.md.
package capabilityauthor

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "capability-author",
	Version:     "0.1.0",
	Description: "AI-authored capabilities — Increment 1 of Fire 1: the capabilityproposal vertex type + the RequestCapabilityAuthoring/RecordCapabilityProposal capture pair (§5 record-time deterministic-validation boundary for the lens kind). The escalation dispatch, review/apply ops, and catalog/review lenses land in later increments.",
	Depends:     []string{"orchestration-base"},
	DDLs:        DDLs(),
	Permissions: Permissions(),
}
