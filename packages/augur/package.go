// Package augur is the Augur Capability Package — the data + safety foundation
// for Weaver's AI-assisted reasoning tier (the L3 evaluator).
//
// The Augur turns a Weaver convergence gap the package playbook cannot plan
// (an unplannable / retry-exhausted gap) into an AI-reasoned, human-reviewable
// PROPOSAL: Weaver dispatches CreateAugurReasoningClaim as a directOp (Option F
// — no Loom wrapper) that mints the claim vertex and emits external.augur off
// its transactional outbox; the bridge's `augur` adapter calls the model (Weaver
// never calls the model directly), which proposes a remediation within the
// installed action catalog, and the bridge's replyOp records a `vtx.augurproposal`
// vertex pending human approval. The AI proposes; the human decides; the Processor
// stays the sole writer (P2).
//
// This package declares:
//
//   - The `augurproposal` DDL — the proposal vertex type + the matched op pair
//     that drives one reasoning episode against the bridge's standard
//     {externalRef, status, result} reply contract:
//
//       - CreateAugurReasoningClaim (Weaver's directOp) mints the claim vertex
//         write-ahead with the TRUSTED gap context + the links, and emits
//         external.augur off its transactional outbox for the bridge to pick up.
//       - RecordProposal (the bridge replyOp) reads that trusted context back,
//         decodes the model's structured proposal from the opaque result, and
//         records the verdict.
//
//     Proposal shape (D5 — minimal root, business data in aspects; Contract #1
//     key shapes; handle = the escalation episode's instanceKey):
//
//	vtx.augurproposal.<handle>   root data = {}
//	  .gap         { targetId, entityId, gapColumn, trigger }   instanceOp — TRUSTED, what was stuck
//	  .proposed    { action, params }                           replyOp — the remediation
//	  .rationale   { text }                                     replyOp — the reasoning (audit)
//	  .confidence  { score }                                    replyOp — 0..1 self-reported
//	  .provenance  { model, promptHash, catalogHash, reasonedAt }  replyOp
//	  .review      { state, invalidReason, reviewedAt, dispatchedAt }  replyOp — verdict
//	               state ∈ {pending, approved, rejected, dispatched, invalid, superseded}
//	lnk.augurproposal.<handle>.forCandidate.<type>.<entityId>   proposal forCandidate candidate
//	lnk.augurproposal.<handle>.forTarget.meta.<weaverTargetId>  proposal forTarget target
//
//     Both links: the proposal is the later-arriving SOURCE, the candidate and
//     the weaver target pre-exist = the TARGETs (Contract #1 §1.1); the names
//     pass the sentence test.
//
//   - RecordProposal carries the deterministic-validation safety boundary
//     (design §5, record-time leg): the entity/target identity is read from the
//     instanceOp-minted claim — NEVER the model's reply (the load-bearing safety
//     split). A proposal is stored `pending` ONLY when its proposed action is in
//     the allowed escalation vocabulary, its confidence is a real 0..1 score, and
//     it does not escape the escalated candidate's scope. A proposal that fails
//     any of these — and a modeled refusal (status=failed) — is stored `invalid`
//     with an auditable reason, never `pending`, never dispatchable. The AI never
//     produces a side effect that was not deterministically validated, and can
//     never name the entity it acts on.
//
//   - The `augurProposals` read-model lens — the P5 query surface (nats-kv
//     bucket `augur-proposals`) Loupe reads to render the human-in-the-loop
//     review surface: one flat row per proposal carrying the trusted gap
//     context, the model's proposed action + rationale + confidence, and the
//     review verdict. Read-model only (trusted-tool posture) — not protected,
//     not a weaver-target convergence lens.
//
//   - ReviewProposal — the human verdict op (design §3.2): an operator flips a
//     pending proposal to `approved` | `rejected`. The reviewer is the trusted
//     submitting actor (op.actor) and the stamp is the envelope's submit time;
//     approve re-runs the §5 boundary against the stored proposal and fail-closes
//     to `invalid` if it no longer validates. Only a pending proposal is
//     reviewable; the verdict + reviewer are recorded on the .review aspect + a
//     reviewedBy link. (The approved-proposal dispatch pickup is Fire 2b,
//     Weaver-side.)
//
//   - Permissions granting CreateAugurReasoningClaim + RecordProposal +
//     ReviewProposal to `operator` (Weaver — the directOp dispatcher — the bridge
//     service actor, and the human reviewer are all operator-equivalent via
//     holdsRole → operator).
//
// Install via the InstallPackage kernel op. See docs/components/_packages.md and
// _bmad-output/implementation-artifacts/augur-design.md.
package augur

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "augur",
	Version:     "0.2.0",
	Description: "Augur (Weaver L3 reasoning tier) data + safety foundation: the augurproposal vertex type + the CreateAugurReasoningClaim / RecordProposal capture pair (record-time deterministic-validation boundary) + the ReviewProposal human-verdict op (re-validated on approve).",
	Depends:     []string{"orchestration-base"},
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
