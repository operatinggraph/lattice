package capabilityauthor

import (
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// DDLs returns the package's DDL meta-vertex declarations.
//
// The `capabilityproposal` vertex-type DDL owns the capture pair + the human
// verdict op + the apply-flip for one authoring episode
// (ai-authored-capabilities-design.md §3.1, §3.3, §3.5); the
// `capabilityauthorclaim` vertex-type DDL owns the escalation-dispatch
// instanceOp (§3.4). Together they carry Fire 1 (capture + dispatch), Fire 2
// (review + apply + a CLI review-and-apply affordance, lens + grant kinds),
// Fire 3 (weaverTarget + loomPattern kinds in the materializer), and Fire 4
// (the Starlark-bearing vertexTypeDDL/opMeta kinds, gated on the
// verified-pure Starlark sandbox dry-run + the sensitive-ref-mac-provenance
// §7 condition-2 lint — see the design doc's §3.2/§8 checkpoint).
//
//   - RequestCapabilityAuthoring mints the proposal vertex write-ahead of the
//     reasoning call (mirrors augur's CreateAugurReasoningClaim), recording the
//     requester + intent. No artifact yet — the escalation dispatch (below) is
//     what triggers the reasoning call that fills it in.
//   - CreateAuthoringClaim (capabilityauthorclaim DDL) is the externalTask
//     instanceOp the capabilityAuthor Loom pattern submits: it mints a small
//     correlation-claim vertex vtx.capabilityauthorclaim.<handle> keyed by
//     Loom's opaque instanceKey (never the proposal's own id — the two are
//     independent by construction, Contract #10 §10.3/§10.5), records a
//     .target aspect pointing back at the real proposal vertex, writes a
//     create-only .claim aspect onto the PROPOSAL vertex itself (closing the
//     capabilityAuthorPending lens's missing_authoring gap immediately), and
//     emits the external.capabilityAuthor event.
//   - RecordAuthoringDispatch (capabilityAuthorClaimDispatch DDL) is the
//     externalTask dispatchOp the bridge submits when its capabilityAuthor
//     adapter returns Pending (the reasoning call was submitted but has not
//     resolved yet) — mirrors lease-signing's RecordServiceDispatch
//     byte-for-byte in role. Read-free: it reconstructs
//     vtx.capabilityauthorclaim.<externalRef> from the bare handle
//     CreateAuthoringClaim already minted and writes a create-only .dispatch
//     aspect {vendorRef, adapter, replyOp, submittedAt, nextPollAt, deadline}
//     — the PENDING marker. It writes no .artifact/.review and emits no
//     completion signal (the reasoning call is not done); the
//     capabilityAuthorPending lens reads only .claim/.artifact
//     (lenses.go:111-117), so the marker cannot perturb that gap predicate.
//   - RecordCapabilityProposal is the bridge replyOp: payload {externalRef,
//     status, result} — the standard generic reply shape
//     internal/bridge/dispatch.go's terminal-outcome leg always submits
//     (mirrors augur's RecordProposal exactly). externalRef is the Loom
//     instanceKey CreateAuthoringClaim minted; the op resolves the real
//     proposal vertex by reading the claim's .target aspect (a single
//     known-key read, no scan) — never by treating externalRef itself as the
//     proposal id. On status=completed
//     it decodes a single `result` JSON blob for kind/content/target/rationale/
//     confidence/validation*/provenance*; the `validation` sub-object is the
//     ALREADY-COMPUTED §5 deterministic-validation verdict (in the full design,
//     computed by the bridge via pkgmgr.ValidateCapabilityArtifact before it
//     submits this op — the same "compute client-side, submit a trusted
//     verdict" split F-004's own install path uses). The script does NOT
//     re-run cypher parsing or validateAll itself (a Starlark DDL script has no
//     parser/registry access) — it trusts the decoded verdict from this op's
//     privileged submitter (the bridge). A decode failure (empty/non-JSON/
//     non-object result) or status=failed (a terminal adapter failure — a
//     modeled refusal or another definitive cause; the real adapter's own
//     Detail names which one) NEVER fail()s the op — the bridge has already
//     Ack'd the external event, so a reject would
//     wedge the episode with no record; both store the proposal review.state=
//     invalid instead (auditable, never dispatchable). Either way this op only
//     ever produces a `pending`-or-`invalid` PROPOSAL, never a write to any
//     other vertex. Note what the kernel's step-8 protected-key guard does and
//     does not cover at APPLY time: it refuses an update/tombstone of a root
//     carrying data.protected, and SKIPS creates — so it is not a check on a
//     proposed grant's SCOPE, which installs new permission vertices. The
//     independent re-check of a submitted verdict is the APPROVE-time
//     re-validation (cmd/loupe's freshCapabilityVerdict, cmd/lattice/
//     capability's freshApprovalVerdict), which re-runs
//     pkgmgr.ValidateCapabilityArtifact server-side and, for kind=grant, reads
//     the requester's LIVE held permissions.
//   - SubmitCapabilityProposal is the HUMAN authoring lane
//     (weaver-target-studio-design.md §6.4) — the single-op counterpart to that
//     whole three-op capture pair. An operator who composed the artifact
//     themselves has no reasoning call to dispatch, so there is no claim to
//     correlate and no write-ahead request to require: one op mints the
//     proposal vertex and every aspect the pair would have written, taking
//     {proposalId, kind, content, target, rationale, validation, intent?} as
//     ordinary top-level fields. The requester is still op.actor, the same §5
//     boundary decides pending vs invalid (minus the confidence range — a human
//     produces no model score, so .confidence.score carries the -1.0 absent
//     sentinel), and .provenance.source is stamped 'operator' where the AI lane
//     stamps 'ai'. It reads nothing at all: a duplicate proposalId is refused by
//     the commit batch's CreateOnly conditioning, not by a read-before-create
//     probe. Unlike RecordCapabilityProposal it DOES fail() a malformed payload
//     — there is no bridge Ack to protect, so the operator is told
//     synchronously and nothing is written.
//   - ReviewCapabilityProposal is the human verdict op (design §3.3): a
//     capability-authorized operator flips a PENDING proposal to approved or
//     rejected, addressed directly by its proposalId (no claim indirection).
//     A reject needs no re-check; an approve re-runs the §5 boundary against
//     the LIVE catalog via a fresh validation verdict the TRUSTED caller
//     attaches to the payload (same compute-client-side split as
//     RecordCapabilityProposal) — missing or non-"valid" fail-closes to
//     invalid. Only a pending proposal is reviewable.
//
// Architectural rules (binding — same known-key discipline as augur /
// orchestration-base / lease-signing):
//   - Every op reads ONLY by known key (kv.Read of the proposal's own aspects);
//     SubmitCapabilityProposal reads nothing at all. No prefix scans, no
//     adjacency lookups, no lens reads.
//   - No-orphan invariant (FR29/P4): RecordCapabilityProposal REQUIRES the
//     proposal's .request aspect to be live (the RequestCapabilityAuthoring op
//     must have committed write-ahead) and rejects (structured ScriptError) if
//     absent — the model can never fabricate a proposal with no request.
//   - The requester identity comes from op.actor (the TRUSTED submitting
//     actor), never a payload field — the same don't-trust-the-payload-for-
//     identity discipline augur's ReviewProposal reviewer uses.
//   - The proposal is idempotent on redelivery: both ops write their aspects
//     create-only, so a redelivered op conflicts (Contract #4 tracker collapse
//     backstop) rather than double-recording. The same create-only conditioning
//     is what stops a SubmitCapabilityProposal from overwriting a live proposal
//     whose id it reuses.
//
// Proposal shape (Contract #1 key shapes; D5 — minimal root, business data in
// aspects; design §3.1):
//
//	vtx.capabilityproposal.<id>   root data = {}
//	  .request     { requesterId, intent, contextRef }            RequestCapabilityAuthoring | SubmitCapabilityProposal
//	  .claim       { claimedAt, claimKey }                        CreateAuthoringClaim (write-ahead marker)
//	  .artifact    { kind, content }                              Record | SubmitCapabilityProposal
//	  .target      { mode, packageName, baseVersion, newVersion } Record | SubmitCapabilityProposal
//	  .rationale   { text }                                       Record | SubmitCapabilityProposal
//	  .confidence  { score }                                      Record | SubmitCapabilityProposal (-1.0 = no model score)
//	  .validation  { state, report, deltaPreview, checkedAt }     Record | SubmitCapabilityProposal
//	  .provenance  { source, model, promptHash, catalogHash, reasonedAt } Record ('ai') | SubmitCapabilityProposal ('operator')
//	  .review      { state, invalidReason, reviewedAt, appliedAt, appliedByOp }  RecordCapabilityProposal + ReviewCapabilityProposal (appliedAt/appliedByOp remain empty until the apply increment)
//	lnk.capabilityproposal.<id>.requestedBy.<type>.<requesterId>  proposal requestedBy requester
//	lnk.capabilityproposal.<id>.reviewedBy.<type>.<reviewerId>    proposal reviewedBy reviewer (ReviewCapabilityProposal)
//
//	vtx.capabilityauthorclaim.<handle>   root data = {}   CreateAuthoringClaim
//	  .target   { proposalKey }   the back-pointer to the real vtx.capabilityproposal.<id>
//	  .dispatch { vendorRef, adapter, replyOp, submittedAt, nextPollAt, deadline }   RecordAuthoringDispatch (pending marker; async only, closes no gap)
//
// review.state ∈ {pending, invalid, approved, rejected} (applied/superseded
// arrive with the apply increment). Absence of .artifact/.review = the
// request is recorded but not yet authored (mirrors augur's claim-in-flight —
// absence of .proposed/.review).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		capabilityProposalDDL(),
		capabilityAuthorClaimDDL(),
		capabilityAuthorClaimDispatchDDL(),
		capabilityAuthorDispatchMarkerDDL(),
	}
}

func capabilityProposalDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "capabilityproposal",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"RequestCapabilityAuthoring", "SubmitCapabilityProposal", "RecordCapabilityProposal", "ReviewCapabilityProposal", "MarkCapabilityProposalApplied"},
		Description: "AI-authored capability proposal DDL — Increment 1 (design §3.1/§3.3): the capture " +
			"pair for one authoring episode. Vertex shape: vtx.capabilityproposal.<id>, class=capabilityproposal, " +
			"root data = {} (D5); business data in aspects: .request {requesterId, intent, contextRef} (the " +
			"RequestCapabilityAuthoring instanceOp's write-ahead context), .artifact {kind, content}, " +
			".target {mode, packageName, baseVersion, newVersion}, .rationale {text}, .confidence {score}, " +
			".validation {state, report, deltaPreview, checkedAt}, .provenance {source, model, promptHash, " +
			"catalogHash, reasonedAt} (RecordCapabilityProposal's model-derived data + the §5 deterministic-validation " +
			"verdict). `source` declares which lane authored the proposal — 'ai' for the bridge-recorded reasoning " +
			"path, 'operator' for a human's direct SubmitCapabilityProposal — so a review queue badges origin from a " +
			"declared field rather than inferring it from the presence of model-shaped provenance. " +
			"Relationship: requestedBy (proposal→requester identity), a LINK. " +
			"RequestCapabilityAuthoring mints the proposal vertex write-ahead with the .request aspect + the " +
			"requestedBy link (no-orphan by construction — the requester is op.actor, the trusted submitting " +
			"actor). RecordCapabilityProposal is the bridge replyOp (payload {externalRef, status, result}, " +
			"mirroring augur's RecordProposal): externalRef is the Loom instanceKey CreateAuthoringClaim minted " +
			"(never the proposal's own id); the op resolves the real proposal vertex via the claim's .target " +
			"aspect (vtx.capabilityauthorclaim.<externalRef>.target); requires a live .request aspect on the " +
			"resolved proposal (the instanceOp must commit first); on " +
			"status=completed it decodes a single result JSON blob {kind, content, target:{mode, packageName, " +
			"baseVersion, newVersion}, rationale, confidence, validation:{state, report, deltaPreview}, " +
			"provenance:{model, promptHash, catalogHash, reasonedAt}} — the ALREADY-COMPUTED §5 verdict travels " +
			"as validation.state/report. Writes review.state=pending only when kind is an enabled artifact kind " +
			"(increment 1: lens only), validation.state is 'valid', and confidence is a real 0..1 score; " +
			"otherwise (including status=failed — a terminal adapter failure, honestly labeled from the " +
			"adapter's own Detail: a modeled refusal or another definitive cause such as a timeout, an " +
			"unusable idempotencyKey, an empty intent, or a vendor call failing outright — and an " +
			"empty/non-JSON/non-object completed result) review.state=invalid with an auditable invalidReason " +
			"— the proposal is ALWAYS recorded (auditability, never fail()ed post-Ack), never applicable " +
			"when invalid. The script does " +
			"not itself run the openCypher parser or validateAll (no parser/registry access from Starlark) — it " +
			"trusts the decoded validation verdict from this op's privileged submitter (the bridge in the full " +
			"design), computed by pkgmgr.ValidateCapabilityArtifact before submission; the kernel's F-004 " +
			"apply-time step-8 guard refuses an update/tombstone of a protected root but skips creates, so it " +
			"is not itself a scope check on a proposed grant; the independent re-check is the approve-time " +
			"re-validation, which re-runs the materializer server-side against the requester's live held " +
			"permissions. SubmitCapabilityProposal is the human-authored counterpart to that whole capture pair: " +
			"a capability-gated operator submits an artifact they composed themselves (the Weaver Target Studio's " +
			"canvas, weaver-target-studio-design.md §6.4) directly, in ONE op, with no claim indirection — there is " +
			"no reasoning call to dispatch, so no RequestCapabilityAuthoring write-ahead and no " +
			"vtx.capabilityauthorclaim handle stand between the author and the proposal. It mints the proposal " +
			"vertex plus every aspect the capture pair would have written, taking {proposalId, kind, content, " +
			"target{mode,packageName,baseVersion,newVersion}, rationale, validation{state,report,deltaPreview}, " +
			"intent?} as ordinary top-level payload fields (never a JSON result blob — there is no adapter Detail " +
			"to unwrap here). The submitter is the TRUSTED actor (op.actor, never a payload field): it becomes " +
			".request.requesterId and the requestedBy link target, so the no-orphan invariant the capture pair " +
			"establishes holds identically. The SAME §5 record-time boundary decides the verdict — review.state = " +
			"pending only when kind is an enabled artifact kind AND the caller-supplied validation.state is " +
			"'valid' (the operator's client runs pkgmgr.ValidateCapabilityArtifact before submitting, exactly as " +
			"the bridge and ReviewCapabilityProposal's approve leg both do); otherwise the proposal is recorded " +
			"review.state=invalid with an auditable invalidReason, never dropped. The confidence-range check does " +
			"NOT apply — a human author produces no model confidence — so .confidence.score is the -1.0 " +
			"absent-sentinel a decode-failed record already writes, and .provenance carries source='operator' " +
			"with the four model fields empty. Unlike RecordCapabilityProposal, a malformed payload here DOES " +
			"fail() the op: that op must never fail post-Ack because the bridge has already acknowledged the " +
			"external event, whereas an operator submitting directly gets the rejection synchronously and has " +
			"written nothing. A repeated proposalId cannot overwrite a live proposal — every mutation is a " +
			"create, and the commit batch's CreateOnly conditioning rejects the whole op (Contract #3 §3.2), the " +
			"same duplicate-id posture RequestCapabilityAuthoring already has. Review/approve/apply are " +
			"unchanged: an operator-authored proposal enters the identical queue and passes the identical human " +
			"gate, so the studio can never bypass review. ReviewCapabilityProposal is the human verdict op (design §3.3, mirrors augur's " +
			"ReviewProposal): a capability-authorized operator flips a PENDING proposal to approved or " +
			"rejected, addressed directly by its own proposalId (no claim indirection — unlike " +
			"RecordCapabilityProposal's Loom-minted opaque handle, a human reviewer already names the real " +
			"proposal). A reject is always permitted with no re-check. An approve re-runs the §5 boundary " +
			"against the LIVE catalog/registry (record-time and approve-time can drift): the script has no " +
			"parser/registry access, so the TRUSTED caller supplies a FRESH validation verdict in the payload " +
			"(the same compute-client-side-submit-a-trusted-verdict split RecordCapabilityProposal's own " +
			"verdict rides); a missing or non-'valid' fresh verdict fail-closes the approve to invalid. Only " +
			"a pending proposal is reviewable — any other state rejects (InvalidReviewTransition); a " +
			"redelivered review is collapsed earlier by the Contract #4 requestId tracker. The reviewer is " +
			"the TRUSTED submitting actor (op.actor, never a payload field) and the stamp is the envelope's " +
			"authoritative submit time. MarkCapabilityProposalApplied is the apply-flip op that closes the " +
			"loop (design §3.5): submitted by the operator AFTER they have separately applied the proposal's " +
			"materialized artifact through the existing, UNMODIFIED F-004 InstallPackage/UpgradePackage op " +
			"(pkgmgr.CapabilityApplyPlanForProposal builds the Definition; pkgmgr.Installer.Apply submits the " +
			"real install/upgrade — a SEPARATE Processor commit, not this one). Only an APPROVED proposal may " +
			"be marked applied (InvalidApplyTransition otherwise — fail-closed, no double-apply/replay-onto-a-" +
			"different-state); packageKey is never trusted blind — the script requires a LIVE installed " +
			"package (its .manifest aspect, written only by a real F-004 install/upgrade) whose recorded name " +
			"matches THIS proposal's own .target.packageName (PackageMismatch/UnknownPackage otherwise), so a " +
			"caller cannot mark a proposal applied against a nonexistent, tombstoned, or unrelated package. On " +
			"success it flips review.state approved→applied, stamps appliedAt (op.submittedAt) + appliedByOp " +
			"(the caller-supplied audit pointer to the install/upgrade that ran), and creates the appliedAs " +
			"link from the proposal to the verified vtx.package.<id> vertex. A crash between the real " +
			"install/upgrade committing and this op committing leaves a harmless, recoverable inconsistency " +
			"(the package is live but the proposal still reads approved) — not a safety gap, since the §5 " +
			"validation + human-approval invariant already held before either op ran; a redelivered " +
			"MarkCapabilityProposalApplied is collapsed by the Contract #4 requestId tracker like any other op.",
		Script: capabilityProposalDDLScript,
		InputSchema: `{"type":"object","description":"RequestCapabilityAuthoring{proposalId,intent,contextRef?} | SubmitCapabilityProposal{proposalId,kind,content,target,rationale,validation,intent?} | RecordCapabilityProposal — the bridge replyOp {externalRef,status,result} — | ReviewCapabilityProposal{proposalId,verdict,validation?}. The artifact/target/rationale/confidence/validation/provenance fields on a RecordCapabilityProposal are decoded from a single JSON result blob, never top-level payload fields; on a SubmitCapabilityProposal the equivalents ARE top-level fields (a human author has no adapter Detail to unwrap).","properties":` +
			`{"proposalId":{"type":"string","description":"RequestCapabilityAuthoring, SubmitCapabilityProposal or ReviewCapabilityProposal — bare NanoID (no dots/wildcards/whitespace) naming vtx.capabilityproposal.<proposalId>. Caller-supplied."},` +
			`"intent":{"type":"string","description":"RequestCapabilityAuthoring — the plain-language capability request, e.g. 'a lens listing active providers by specialty'. SubmitCapabilityProposal — optional; the queue's row label, defaulting to the rationale text when omitted."},` +
			`"kind":{"type":"string","description":"SubmitCapabilityProposal only — the artifact kind: 'lens', 'grant', 'weaverTarget', 'loomPattern', 'vertexTypeDDL' or 'opMeta'. Any other value records the proposal review.state=invalid (auditable, never a hard reject)."},` +
			`"content":{"type":"string","description":"SubmitCapabilityProposal only — the artifact body the operator composed, the same JSON string shape RecordCapabilityProposal's result.content carries for that kind."},` +
			`"target":{"type":"object","description":"SubmitCapabilityProposal only — where the artifact lands: {mode, packageName?, baseVersion?, newVersion?}, the same shape RecordCapabilityProposal decodes from its result blob. mode is REQUIRED and non-empty: unlike the bridge replyOp (which can never fail post-Ack and so records an empty target invalid), this op rejects the submission outright."},` +
			`"rationale":{"type":"string","description":"SubmitCapabilityProposal only — the operator's own justification for the artifact, stored verbatim on .rationale.text (the human counterpart to the model's rationale)."},` +
			`"contextRef":{"type":"string","description":"RequestCapabilityAuthoring only — an optional pointer to bounded context the reasoning call hydrates (opaque to this DDL)."},` +
			`"externalRef":{"type":"string","description":"RecordCapabilityProposal only — the bare Loom instanceKey handle CreateAuthoringClaim minted; the op resolves the real proposal vertex via vtx.capabilityauthorclaim.<externalRef>.target (never treated as the proposal's own id)."},` +
			`"status":{"type":"string","description":"RecordCapabilityProposal only — the adapter's terminal outcome: completed (the model proposed an artifact) or failed (a terminal adapter failure — stored invalid, never dispatchable; NOT only a modeled refusal — the adapter's own Detail names the real cause, e.g. a timeout, an unusable idempotencyKey, an empty intent, or a vendor call failing outright)."},` +
			`"result":{"type":"string","description":"RecordCapabilityProposal only — on status=completed, the model's structured-output proposal as a JSON string {kind, content, target:{mode,packageName,baseVersion?,newVersion?}, rationale, confidence, validation:{state,report?,deltaPreview?}, provenance:{model?,promptHash?,catalogHash?,reasonedAt?}} — the opaque adapter Detail; required. On status=failed, the adapter's own plain-text failure Detail (e.g. 'capabilityAuthor: the model declined to propose (refusal: <category>)' for a genuine refusal, or an unrelated honest cause otherwise) carried verbatim as BOTH .rationale.text and review.invalidReason — never a hardcoded refusal claim. validation.state is the ALREADY-COMPUTED §5 verdict (pkgmgr.ValidateCapabilityArtifact, run by the trusted caller before submission — the script does not itself re-run the parser/validateAll). kind enables 'lens', 'grant', 'weaverTarget', 'loomPattern', 'vertexTypeDDL', or 'opMeta'; any other value, or a validation.state other than 'valid', or an out-of-range confidence, or an undecodable result stores the proposal review.state=invalid (auditable, never a hard reject)."},` +
			`"verdict":{"type":"string","description":"ReviewCapabilityProposal only — the operator's verdict on a pending proposal: 'approve' (re-validated against the §5 boundary via the fresh validation payload field, fail-closing to invalid if it no longer validates) or 'reject'. The reviewer is the trusted submitting actor (op.actor) and the stamp is the envelope submit time; neither is a payload field."},` +
			`"validation":{"type":"object","description":"ReviewCapabilityProposal (approve verdict only) and SubmitCapabilityProposal — the §5 verdict {state,report?,deltaPreview?} the trusted caller computed by running pkgmgr.ValidateCapabilityArtifact against the CURRENT catalog/registry immediately before submitting (the script has no parser/registry access of its own). state must be exactly 'valid': on an approve a missing/stale verdict fail-closes to invalid, and on a submit the proposal is recorded review.state=invalid. Ignored on a reject verdict."},` +
			`"packageKey":{"type":"string","description":"MarkCapabilityProposalApplied only — the vtx.package.<id> the caller's separate F-004 Installer.Apply produced (pkgmgr.ApplyResult.PackageKey). Verified live (a .manifest aspect must exist) and its recorded name must match this proposal's own target.packageName before it is recorded as the appliedAs link target."},` +
			`"installRequestId":{"type":"string","description":"MarkCapabilityProposalApplied only — a caller-supplied audit pointer to the install/upgrade op that applied the artifact (opaque to this DDL). Stored verbatim as appliedByOp."}},` +
			`"required":["proposalId"]}`,
		OutputSchema: `{"type":"object","properties":{"primaryKey":{"type":"string","description":"vtx.capabilityproposal.<id> of the created/updated/reviewed/applied proposal. The recorded review.state (pending|invalid|approved|rejected|applied) is read from the proposal's .review aspect, not the op response."}}}`,
		FieldDescription: map[string]string{
			"proposalId":       "Bare NanoID naming the proposal vertex (RequestCapabilityAuthoring, SubmitCapabilityProposal, ReviewCapabilityProposal, MarkCapabilityProposalApplied).",
			"intent":           "The plain-language capability request (RequestCapabilityAuthoring); the optional queue row label defaulting to the rationale (SubmitCapabilityProposal).",
			"kind":             "The artifact kind the operator composed: lens, grant, weaverTarget, loomPattern, vertexTypeDDL or opMeta (SubmitCapabilityProposal).",
			"content":          "The artifact body the operator composed, same shape as the model's result.content for that kind (SubmitCapabilityProposal).",
			"target":           "Where the artifact lands: {mode, packageName?, baseVersion?, newVersion?}; mode is required and non-empty (SubmitCapabilityProposal).",
			"rationale":        "The operator's own justification for the artifact (SubmitCapabilityProposal).",
			"externalRef":      "The bare Loom instanceKey handle CreateAuthoringClaim minted; resolved to the real proposal vertex via the claim's .target aspect (RecordCapabilityProposal).",
			"status":           "The bridge's terminal outcome: completed or failed (RecordCapabilityProposal).",
			"result":           "The model's proposal as a JSON string, decoded for kind/content/target/rationale/confidence/validation*/provenance* (RecordCapabilityProposal).",
			"verdict":          "The operator's verdict on a pending proposal: approve or reject (ReviewCapabilityProposal).",
			"validation":       "The §5 verdict {state,report?} computed by the caller before submission; must be 'valid' for an approve to succeed (ReviewCapabilityProposal) or for a submitted proposal to enter pending (SubmitCapabilityProposal).",
			"packageKey":       "The vtx.package.<id> the caller's separate F-004 apply produced; verified live + name-matched against target.packageName before being recorded as the appliedAs link target (MarkCapabilityProposalApplied).",
			"installRequestId": "A caller-supplied audit pointer to the install/upgrade op that applied the artifact; stored verbatim as appliedByOp (MarkCapabilityProposalApplied).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "RequestCapabilityAuthoring — an operator requests a new lens",
				Payload: map[string]any{
					"proposalId": "capPropOneHJKMNPQRST",
					"intent":     "a lens that lists active providers by specialty",
				},
				ExpectedOutcome: "Mints vtx.capabilityproposal.capPropOneHJKMNPQRST with root {} (D5), the .request " +
					"aspect {requesterId: op.actor, intent, contextRef}, and the requestedBy link. No .artifact/.review yet.",
			},
			{
				Name: "SubmitCapabilityProposal — an operator submits a lens they composed in the studio",
				Payload: map[string]any{
					"proposalId": "capPropStudioHJKMNPQ",
					"kind":       "lens",
					"content":    `{"canonicalName":"activeProvidersBySpecialty","adapter":"nats-kv","bucket":"active-providers","spec":"MATCH (p:provider) RETURN p.key AS key"}`,
					"target":     map[string]any{"mode": "newPackage"},
					"rationale":  "the on-call roster needs a specialty cut no installed lens projects",
					"validation": map[string]any{"state": "valid"},
				},
				ExpectedOutcome: "Mints vtx.capabilityproposal.capPropStudioHJKMNPQ with every aspect the capture pair writes, " +
					"in one op and with no claim indirection: .request {requesterId: op.actor, intent: the rationale}, the requestedBy " +
					"link, .artifact/.target/.rationale, .confidence.score = -1.0 (no model confidence), .validation, " +
					".provenance {source: 'operator'}, and review.state = pending. An unenabled kind or a non-'valid' " +
					"validation records review.state = invalid instead; a proposalId already in use rejects the whole op.",
			},
			{
				Name: "RecordCapabilityProposal — a valid lens artifact (already §5-validated by the caller)",
				Payload: map[string]any{
					"externalRef": "<claimHandle>",
					"status":      "completed",
					"result": `{"kind":"lens","content":"{\"canonicalName\":\"activeProvidersBySpecialty\",\"adapter\":\"nats-kv\",\"bucket\":\"active-providers\",\"spec\":\"MATCH (p:provider) RETURN p.key AS key\"}",` +
						`"target":{"mode":"newPackage"},"rationale":"no existing lens surfaces this projection","confidence":0.86,"validation":{"state":"valid"}}`,
				},
				ExpectedOutcome: "Resolves the real proposal via vtx.capabilityauthorclaim.<claimHandle>.target; review.state = pending (dispatchable in a later increment's apply op); the .artifact/.target/.rationale/.confidence/.validation/.provenance aspects are recorded.",
			},
			{
				Name: "RecordCapabilityProposal — a modeled refusal",
				Payload: map[string]any{
					"externalRef": "<claimHandle>",
					"status":      "failed",
					"result":      "capabilityAuthor: the model declined to propose (refusal: policy)",
				},
				ExpectedOutcome: "review.state = invalid; invalidReason AND .rationale.text both carry the result verbatim ('capabilityAuthor: the model declined to propose (refusal: policy)') — the op never substitutes a hardcoded refusal claim of its own.",
			},
			{
				Name: "RecordCapabilityProposal — a terminal adapter failure that is NOT a refusal",
				Payload: map[string]any{
					"externalRef": "<claimHandle>",
					"status":      "failed",
					"result":      "capabilityAuthor: both model calls failed (deadline exceeded; deadline exceeded)",
				},
				ExpectedOutcome: "review.state = invalid; invalidReason AND .rationale.text both carry the result verbatim. The reviewer sees the real cause (both vendor calls timed out) — the op never claims 'declined (refusal)' for a cause that was never a refusal.",
			},
			{
				Name: "ReviewCapabilityProposal — a human operator approves a pending proposal",
				Payload: map[string]any{
					"proposalId": "capPropOneHJKMNPQRST",
					"verdict":    "approve",
					"validation": map[string]any{"state": "valid"},
				},
				ExpectedOutcome: "review.state: pending → approved; reviewedBy link recorded (reviewer = op.actor, verdict = approve). " +
					"An approve with a stale/missing/non-'valid' fresh validation verdict fail-closes to invalid instead.",
			},
			{
				Name: "ReviewCapabilityProposal — a human operator rejects a pending proposal",
				Payload: map[string]any{
					"proposalId": "capPropOneHJKMNPQRST",
					"verdict":    "reject",
				},
				ExpectedOutcome: "review.state: pending → rejected; no re-validation performed (a reject is always permitted).",
			},
			{
				Name: "MarkCapabilityProposalApplied — the operator closes the loop after a real F-004 apply",
				Payload: map[string]any{
					"proposalId":       "capPropOneHJKMNPQRST",
					"packageKey":       "vtx.package.aiLensPkgHJKMNPQRST",
					"installRequestId": "install:activeProvidersBySpecialty@0.1.0",
				},
				ExpectedOutcome: "review.state: approved → applied; appliedAt + appliedByOp stamped on .review; an appliedAs " +
					"link from the proposal to vtx.package.aiLensPkgHJKMNPQRST is created. Rejected (InvalidApplyTransition) " +
					"if the proposal is not currently approved.",
			},
		},
	}
}

// capabilityProposalDDLScript handles the matched pair
// RequestCapabilityAuthoring (mints the proposal write-ahead) +
// RecordCapabilityProposal (records a proposed artifact + its
// already-computed §5 verdict), the single-op human counterpart
// SubmitCapabilityProposal, and the review/apply verdict ops. Known-key reads
// only; SubmitCapabilityProposal reads nothing at all.
const capabilityProposalDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def optional_string_attr(p, name):
    if not hasattr(p, name):
        return ""
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return ""
    return v

def required_number(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        fail("InvalidArgument: " + name + ": required number")
    return v

def required_bare_id(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def alive(doc):
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def proposal_string(d, name):
    if name not in d:
        return ""
    v = d[name]
    if v == None or type(v) != type(""):
        return ""
    return v

# proposal_package_name reads target.packageName with the same trim
# required_string applies to an authored identifier, so a proposal's declared
# install target folds to the exact spelling Installer.findInstalledPackage
# and IsPackageInstalled compare against. Every OTHER proposal_string field —
# target.mode, validation.state chief among them — is a fail-closed gate
# tested against a literal expected value (e.g. "valid", "newPackage") and
# must keep rejecting a value that carries stray whitespace; only the
# packageName identifier gets this treatment.
def proposal_package_name(d):
    return proposal_string(d, "packageName").strip()

def proposal_dict(d, name):
    if name not in d:
        return {}
    v = d[name]
    if v == None or type(v) != type({}):
        return {}
    return v

def proposal_number(d, name):
    if name not in d:
        return -1.0
    v = d[name]
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        return -1.0
    return v

ENABLED_KINDS = ["lens", "grant", "weaverTarget", "loomPattern", "vertexTypeDDL", "opMeta"]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RequestCapabilityAuthoring":
        proposal_id = required_bare_id(p, "proposalId")
        intent = required_string(p, "intent")
        context_ref = optional_string_attr(p, "contextRef")

        requester = op.actor
        requester_type, requester_id = parts_of(requester, "actor", "")

        proposal_key = "vtx.capabilityproposal." + proposal_id
        requestedby_lnk = "lnk.capabilityproposal." + proposal_id + ".requestedBy." + requester_type + "." + requester_id

        mutations = [
            make_vtx(proposal_key, "capabilityproposal", {}),
            make_aspect(proposal_key, "request", "capabilityAuthor.request",
                        {"requesterId": requester, "intent": intent, "contextRef": context_ref}),
            make_link(requestedby_lnk, proposal_key, requester, "requestedBy", "requestedBy", {}),
        ]
        events = [
            {"class": "capabilityAuthor.requested",
             "data": {"proposalKey": proposal_key, "requesterId": requester, "intent": intent}},
        ]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": proposal_key}}

    if ot == "SubmitCapabilityProposal":
        # The human-authored counterpart to the RequestCapabilityAuthoring /
        # RecordCapabilityProposal capture pair (weaver-target-studio-design.md
        # §6.4): a capability-gated operator submits an artifact they composed
        # themselves, in ONE op. There is no reasoning call to dispatch, so
        # there is no claim handle to resolve and no write-ahead request to
        # require — this op mints the proposal vertex and every aspect the pair
        # would have written between them. It performs NO kv.Read of any kind:
        # a proposalId already in use is refused by the commit batch's
        # CreateOnly conditioning on the vertex create (Contract #3 §3.2), not
        # by a read-before-create probe, so there is nothing to declare in
        # contextHint.reads/optionalReads.
        proposal_id = required_bare_id(p, "proposalId")
        kind = required_string(p, "kind")
        content = required_string(p, "content")
        rationale = required_string(p, "rationale")

        target = {}
        if hasattr(p, "target") and p.target != None and type(p.target) == type({}):
            target = p.target
        # Where the artifact lands is not optional. RecordCapabilityProposal
        # tolerates an empty target because it must never fail() post-Ack — it
        # records the proposal invalid instead. This op CAN reject, so it does:
        # an empty target.mode would otherwise mint a proposal that only fails
        # later, at review or apply, with nothing naming the omission.
        target_mode = proposal_string(target, "mode")
        if len(target_mode.strip()) == 0:
            fail("InvalidArgument: target.mode: required non-empty string (where the artifact lands)")
        validation = {}
        if hasattr(p, "validation") and p.validation != None and type(p.validation) == type({}):
            validation = p.validation

        # The review queue labels every row from .request.intent regardless of
        # which lane authored it. An operator's rationale IS their statement of
        # intent, so it stands in when no explicit intent is supplied.
        intent = optional_string_attr(p, "intent").strip()
        if intent == "":
            intent = rationale

        author = op.actor
        author_type, author_id = parts_of(author, "actor", "")
        proposal_key = "vtx.capabilityproposal." + proposal_id
        requestedby_lnk = "lnk.capabilityproposal." + proposal_id + ".requestedBy." + author_type + "." + author_id

        validation_state = proposal_string(validation, "state")
        validation_report = proposal_string(validation, "report")

        # The SAME §5 boundary RecordCapabilityProposal applies, minus the
        # confidence range: a human author produces no model confidence, so
        # .confidence.score carries the -1.0 absent-sentinel that op's
        # decode-failure path already writes rather than a fabricated score.
        # Unlike that op, a malformed payload here fail()s outright (the
        # required_* checks above) — there is no bridge Ack to protect, and the
        # operator learns synchronously with nothing written.
        review_state = "pending"
        invalid_reason = ""
        if kind not in ENABLED_KINDS:
            review_state = "invalid"
            invalid_reason = "artifact kind not enabled in this increment: " + kind
        elif validation_state != "valid":
            review_state = "invalid"
            if validation_report != "":
                invalid_reason = "materializer validation failed: " + validation_report
            else:
                invalid_reason = "materializer validation failed: no valid verdict supplied"

        mutations = [
            make_vtx(proposal_key, "capabilityproposal", {}),
            make_aspect(proposal_key, "request", "capabilityAuthor.request",
                        {"requesterId": author, "intent": intent, "contextRef": ""}),
            make_link(requestedby_lnk, proposal_key, author, "requestedBy", "requestedBy", {}),
            make_aspect(proposal_key, "artifact", "capabilityAuthor.artifact",
                        {"kind": kind, "content": content}),
            make_aspect(proposal_key, "target", "capabilityAuthor.target",
                        {"mode": target_mode,
                         "packageName": proposal_package_name(target),
                         "baseVersion": proposal_string(target, "baseVersion"),
                         "newVersion": proposal_string(target, "newVersion")}),
            make_aspect(proposal_key, "rationale", "capabilityAuthor.rationale", {"text": rationale}),
            make_aspect(proposal_key, "confidence", "capabilityAuthor.confidence", {"score": -1.0}),
            make_aspect(proposal_key, "validation", "capabilityAuthor.validation",
                        {"state": validation_state, "report": validation_report,
                         "deltaPreview": proposal_string(validation, "deltaPreview"),
                         "checkedAt": op.submittedAt}),
            make_aspect(proposal_key, "provenance", "capabilityAuthor.provenance",
                        {"source": "operator", "model": "", "promptHash": "",
                         "catalogHash": "", "reasonedAt": ""}),
            make_aspect(proposal_key, "review", "capabilityAuthor.review",
                        {"state": review_state, "invalidReason": invalid_reason,
                         "reviewedAt": "", "appliedAt": "", "appliedByOp": ""}),
        ]
        events = [
            {"class": "capabilityAuthor.proposalRecorded",
             "data": {"proposalKey": proposal_key, "kind": kind,
                      "reviewState": review_state, "source": "operator"}},
        ]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": proposal_key}}

    if ot == "RecordCapabilityProposal":
        # The bridge replyOp: payload {externalRef, status, result} (the standard
        # generic reply shape internal/bridge/dispatch.go's terminal-outcome leg
        # always submits — mirrors augur's RecordProposal exactly). externalRef is
        # the Loom instanceKey CreateAuthoringClaim minted — an opaque handle
        # independent of the proposal's own id (Contract #10 §10.3/§10.5) — so the
        # real proposal vertex is resolved via the claim's .target aspect (a
        # single known-key read), never by treating externalRef as the proposal id.
        claim_handle = required_bare_id(p, "externalRef")
        # read-posture: (a) declared in contextHint.reads by the bridge replyOp
        # dispatcher (internal/bridge/dispatch.go replyOpReads, derived from
        # externalRef alone)
        claim_target_doc = kv.Read("vtx.capabilityauthorclaim." + claim_handle + ".target")
        if not alive(claim_target_doc):
            fail("UnknownCapabilityAuthorClaim: no live claim for externalRef " + claim_handle)
        proposal_key = claim_target_doc.data.get("proposalKey")
        if proposal_key == None or type(proposal_key) != type(""):
            fail("CorruptCapabilityAuthorClaim: " + claim_handle + " has no proposalKey")

        # read-posture: (a) UNDECLARABLE — chained (script-read-posture-design
        # §13 hard case 1). proposal_key is resolved from the :386 claim read's
        # result; the bridge replyOp dispatcher knows only externalRef, so this
        # key cannot be declared in contextHint.reads ahead of the script run.
        # Sanctioned live known-key read; absence is still a wiring fault.
        request_doc = kv.Read(proposal_key + ".request")
        if not alive(request_doc):
            fail("UnknownCapabilityProposal: no live .request aspect for " + proposal_key + " (RequestCapabilityAuthoring must commit write-ahead)")

        status = required_string(p, "status")

        review_state = "pending"
        invalid_reason = ""
        kind = ""
        content = ""
        target_mode = ""
        target_package_name = ""
        target_base_version = ""
        target_new_version = ""
        rationale = ""
        confidence = -1.0
        validation_state = ""
        validation_report = ""
        validation_delta_preview = ""
        model = ""
        prompt_hash = ""
        catalog_hash = ""
        reasoned_at = ""

        if status == "failed":
            # A terminal OutcomeFailed is a definitive verdict, NOT a crash: store
            # the proposal invalid (auditable, never dispatchable). "failed" is NOT
            # only a modeled refusal: the real capabilityAuthor adapter
            # (internal/bridge/capability_author.go) also terminates this way on a
            # 24h timeout / cold-episode-no-prompt restart, an unusable
            # idempotencyKey, an empty intent, an invalid-ack from the model
            # runner, and both model calls failing — each with its OWN honest
            # prose in Detail, never the word "refusal" unless refusalDetail
            # actually fired on a genuine vendor policy refusal. dispatch.go:249
            # carries Result.Detail verbatim as this op's 'result' field, so
            # threading it through here — never a hardcoded refusal claim — is
            # what tells the review queue the truth (a down runner must never
            # read to an operator as "the model refused you").
            review_state = "invalid"
            detail = optional_string_attr(p, "result").strip()
            if detail == "":
                detail = "adapter reported a failed status with no detail"
            invalid_reason = detail
            rationale = detail
        elif status == "completed":
            # Decode with a None default (never raise): an empty / non-JSON /
            # non-object result on a completed reply is a definitive verdict, NOT
            # a crash — never fail() the op here (the bridge has already Ack'd
            # the external event; a reject would wedge the episode with no
            # record). Mirrors augur's RecordProposal decode exactly.
            result_str = optional_string_attr(p, "result")
            proposal = None
            if len(result_str.strip()) > 0:
                proposal = json.decode(result_str, None)
            if type(proposal) != type({}):
                review_state = "invalid"
                invalid_reason = "completed reply carried no decodable JSON-object reasoning result"
                rationale = result_str
            else:
                kind = proposal_string(proposal, "kind")
                content = proposal_string(proposal, "content")
                target = proposal_dict(proposal, "target")
                target_mode = proposal_string(target, "mode")
                target_package_name = proposal_package_name(target)
                target_base_version = proposal_string(target, "baseVersion")
                target_new_version = proposal_string(target, "newVersion")
                rationale = proposal_string(proposal, "rationale")
                confidence = proposal_number(proposal, "confidence")
                validation = proposal_dict(proposal, "validation")
                validation_state = proposal_string(validation, "state")
                validation_report = proposal_string(validation, "report")
                validation_delta_preview = proposal_string(validation, "deltaPreview")
                provenance = proposal_dict(proposal, "provenance")
                model = proposal_string(provenance, "model")
                prompt_hash = proposal_string(provenance, "promptHash")
                catalog_hash = proposal_string(provenance, "catalogHash")
                reasoned_at = proposal_string(provenance, "reasonedAt")
        else:
            review_state = "invalid"
            invalid_reason = "unrecognized bridge status: " + status

        # --- §5 record-time deterministic-validation boundary (the safety core) ---
        # The proposal is ALWAYS stored (auditability); the verdict decides only
        # pending (dispatchable in a later increment) vs invalid (never
        # dispatchable). kind enablement + confidence range are checked HERE
        # (cheap, self-contained); the heavier artifact-shape checks (cypher
        # parse, validateAll) were already run by the trusted caller via
        # pkgmgr.ValidateCapabilityArtifact and arrive as validation.state on the
        # decoded result. Skipped when the decode itself already failed above —
        # that verdict already stands.
        if review_state == "pending":
            if kind not in ENABLED_KINDS:
                review_state = "invalid"
                invalid_reason = "artifact kind not enabled in this increment: " + kind
            elif confidence < 0.0 or confidence > 1.0:
                review_state = "invalid"
                invalid_reason = "confidence out of range [0,1]: " + str(confidence)
            elif validation_state != "valid":
                review_state = "invalid"
                invalid_reason = "materializer validation failed: " + validation_report

        mutations = [
            make_aspect(proposal_key, "artifact", "capabilityAuthor.artifact",
                        {"kind": kind, "content": content}),
            make_aspect(proposal_key, "target", "capabilityAuthor.target",
                        {"mode": target_mode, "packageName": target_package_name,
                         "baseVersion": target_base_version, "newVersion": target_new_version}),
            make_aspect(proposal_key, "rationale", "capabilityAuthor.rationale", {"text": rationale}),
            make_aspect(proposal_key, "confidence", "capabilityAuthor.confidence", {"score": confidence}),
            make_aspect(proposal_key, "validation", "capabilityAuthor.validation",
                        {"state": validation_state, "report": validation_report,
                         "deltaPreview": validation_delta_preview, "checkedAt": op.submittedAt}),
            make_aspect(proposal_key, "provenance", "capabilityAuthor.provenance",
                        {"source": "ai", "model": model, "promptHash": prompt_hash,
                         "catalogHash": catalog_hash, "reasonedAt": reasoned_at}),
            make_aspect(proposal_key, "review", "capabilityAuthor.review",
                        {"state": review_state, "invalidReason": invalid_reason,
                         "reviewedAt": "", "appliedAt": "", "appliedByOp": ""}),
        ]
        events = [
            {"class": "capabilityAuthor.proposalRecorded",
             "data": {"proposalKey": proposal_key, "kind": kind, "reviewState": review_state}},
        ]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": proposal_key}}

    if ot == "ReviewCapabilityProposal":
        # The human verdict (design §3.3, mirrors augur's ReviewProposal
        # exactly): an operator flips a PENDING proposal to approved | rejected.
        # Addressed directly by the proposal's own proposalId — unlike
        # RecordCapabilityProposal, there is no claim indirection to resolve
        # (a human reviewer already names the real proposal they are
        # reviewing). The reviewer is the TRUSTED submitting actor (op.actor,
        # capability-authorized, never a payload field); the stamp is the
        # envelope's authoritative submit time (op.submittedAt). Only a
        # pending proposal is reviewable — any other state rejects (terminal-
        # state guard: invalid / already-reviewed cannot be re-reviewed; a
        # redelivered op is collapsed earlier by the Contract #4 requestId
        # tracker).
        proposal_id = required_bare_id(p, "proposalId")
        proposal_key = "vtx.capabilityproposal." + proposal_id
        verdict = required_string(p, "verdict")
        if verdict != "approve" and verdict != "reject":
            fail("InvalidArgument: verdict: must be one of approve, reject; got " + verdict)

        # read-posture: (a) declared in contextHint.reads by the cmd/lattice
        # "capability review" CLI dispatcher
        review_doc = kv.Read(proposal_key + ".review")
        if not alive(review_doc):
            fail("UnknownCapabilityProposal: no recorded review for " + proposal_key + " (RecordCapabilityProposal must commit a verdict before review)")
        rd = review_doc.data
        cur_state = ""
        if rd != None and "state" in rd:
            cur_state = rd["state"]
        if cur_state != "pending":
            fail("InvalidReviewTransition: proposal " + proposal_key + " is '" + cur_state + "', only a pending proposal is reviewable")

        reviewer = op.actor
        reviewer_type, reviewer_id = parts_of(reviewer, "actor", "")
        reviewed_at = op.submittedAt

        new_state = "approved"
        invalid_reason = ""
        if verdict == "reject":
            # A reject is always permitted — no re-validation; the operator
            # declines the proposal regardless of whether it would still validate.
            new_state = "rejected"
        else:
            # Re-run the §5 boundary against the LIVE catalog/registry (design
            # §5 point 3 — record-time and approve-time can drift). This script
            # has no parser/registry access, so — exactly like
            # RecordCapabilityProposal's own verdict — the TRUSTED caller (the
            # operator's Loupe/CLI submission path, which reruns
            # pkgmgr.ValidateCapabilityArtifact against the CURRENT catalog
            # immediately before submitting this op) supplies the fresh verdict
            # in the payload. Missing/non-"valid" fresh validation on an
            # approve fail-closes to invalid — an approve is never trusted blind.
            # Type-checked exactly like RecordCapabilityProposal's own nested-
            # object fields (proposal_dict): a non-object validation payload
            # (a string/list/number) falls back to {} rather than reaching
            # proposal_string with a non-dict, which fails closed to invalid
            # via the empty-dict "state" lookup below instead of risking a
            # Starlark runtime error on a malformed caller payload.
            validation = {}
            if hasattr(p, "validation") and p.validation != None and type(p.validation) == type({}):
                validation = p.validation
            fresh_state = proposal_string(validation, "state")
            fresh_report = proposal_string(validation, "report")
            if fresh_state != "valid":
                new_state = "invalid"
                if fresh_report != "":
                    invalid_reason = "re-validation at approval failed: " + fresh_report
                else:
                    invalid_reason = "re-validation at approval failed: no valid verdict supplied"

        # Unconditioned update preserving the aspect's full shape (D5; the
        # pending-only guard above is the single-transition guarantee, same
        # posture as augur's ReviewProposal flip). appliedAt/appliedByOp stay
        # empty here — only MarkCapabilityProposalApplied (below) ever sets them.
        reviewedby_lnk = "lnk.capabilityproposal." + proposal_id + ".reviewedBy." + reviewer_type + "." + reviewer_id
        mutations = [
            {"op": "update", "key": proposal_key + ".review",
             "document": {"class": "capabilityAuthor.review", "isDeleted": False,
                          "vertexKey": proposal_key, "localName": "review",
                          "data": {"state": new_state, "invalidReason": invalid_reason,
                                   "reviewedAt": reviewed_at, "appliedAt": "", "appliedByOp": ""}}},
            make_link(reviewedby_lnk, proposal_key, reviewer, "reviewedBy", "reviewedBy",
                      {"reviewedAt": reviewed_at, "verdict": verdict}),
        ]
        events = [
            {"class": "capabilityAuthor.proposalReviewed",
             "data": {"proposalKey": proposal_key, "reviewState": new_state,
                      "reviewer": reviewer, "verdict": verdict}},
        ]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": proposal_key}}

    if ot == "MarkCapabilityProposalApplied":
        # The apply-flip (design §3.5): the operator submits this AFTER
        # separately applying the proposal's materialized artifact through the
        # existing, UNMODIFIED F-004 InstallPackage/UpgradePackage op (a
        # SEPARATE Processor commit — pkgmgr.CapabilityApplyPlanForProposal +
        # pkgmgr.Installer.Apply). This op only records that it happened: only
        # an APPROVED proposal may be marked applied (fail-closed — no
        # double-apply, no marking a pending/rejected/invalid proposal
        # applied); a redelivered op is collapsed by the Contract #4 requestId
        # tracker before this script even runs a second time.
        proposal_id = required_bare_id(p, "proposalId")
        proposal_key = "vtx.capabilityproposal." + proposal_id
        package_key = required_string(p, "packageKey")
        install_request_id = required_string(p, "installRequestId")

        package_type, package_id = parts_of(package_key, "packageKey", "")
        if package_type != "package":
            fail("InvalidArgument: packageKey: required vtx.package.<NanoID>; got " + package_key)

        # read-posture: (a) declared in contextHint.reads by the cmd/lattice-pkg
        # submitMarkApplied dispatcher
        review_doc = kv.Read(proposal_key + ".review")
        if not alive(review_doc):
            fail("UnknownCapabilityProposal: no recorded review for " + proposal_key)
        rd = review_doc.data
        cur_state = ""
        reviewed_at = ""
        if rd != None:
            if "state" in rd:
                cur_state = rd["state"]
            if "reviewedAt" in rd:
                reviewed_at = rd["reviewedAt"]
        if cur_state != "approved":
            fail("InvalidApplyTransition: proposal " + proposal_key + " is '" + cur_state + "', only an approved proposal may be applied")

        # packageKey is caller-supplied — never trust it blind. Bind it to a
        # LIVE installed package (its .manifest aspect, written only by a real
        # F-004 install/upgrade) AND cross-check that package's own recorded
        # name against THIS proposal's own .target.packageName — closing the
        # gap a shape-only check (parts_of above) would leave open: a
        # syntactically valid but nonexistent/tombstoned/unrelated packageKey
        # could otherwise mark this proposal applied with no server-side proof
        # it names the package the proposal actually targeted.
        # read-posture: (a) declared in contextHint.reads by the cmd/lattice-pkg
        # submitMarkApplied dispatcher (see the .review note above)
        target_doc = kv.Read(proposal_key + ".target")
        if not alive(target_doc):
            fail("UnknownCapabilityProposal: no recorded target for " + proposal_key)
        td = target_doc.data
        target_package_name = ""
        if td != None and "packageName" in td:
            target_package_name = td["packageName"]

        # read-posture: (a) declared in contextHint.reads by the cmd/lattice-pkg
        # submitMarkApplied dispatcher (see the .review note above)
        manifest_doc = kv.Read(package_key + ".manifest")
        if not alive(manifest_doc):
            fail("UnknownPackage: " + package_key + " is not a live installed package")
        md = manifest_doc.data
        manifest_package_name = ""
        if md != None and "name" in md:
            manifest_package_name = md["name"]

        if manifest_package_name == "" or manifest_package_name != target_package_name:
            fail("PackageMismatch: " + package_key + " (installed name '" + manifest_package_name + "') does not match proposal " + proposal_key + "'s target.packageName '" + target_package_name + "'")

        applied_at = op.submittedAt
        appliedas_lnk = "lnk.capabilityproposal." + proposal_id + ".appliedAs." + package_type + "." + package_id

        mutations = [
            {"op": "update", "key": proposal_key + ".review",
             "document": {"class": "capabilityAuthor.review", "isDeleted": False,
                          "vertexKey": proposal_key, "localName": "review",
                          "data": {"state": "applied", "invalidReason": "",
                                   "reviewedAt": reviewed_at, "appliedAt": applied_at, "appliedByOp": install_request_id}}},
            make_link(appliedas_lnk, proposal_key, package_key, "appliedAs", "appliedAs",
                      {"appliedAt": applied_at, "installRequestId": install_request_id}),
        ]
        events = [
            {"class": "capabilityAuthor.proposalApplied",
             "data": {"proposalKey": proposal_key, "packageKey": package_key, "installRequestId": install_request_id}},
        ]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": proposal_key}}

    fail("capabilityproposal DDL: unknown operationType: " + ot)
`

// capabilityAuthorClaimDDL declares the CreateAuthoringClaim instanceOp
// (design §3.4) — the externalTask instanceOp
// the capabilityAuthor Loom pattern submits. The claim vertex's envelope
// class (capabilityauthorclaim) matches this DDL's own CanonicalName exactly,
// so the step-6 write-gate resolves it by direct class match (Contract #1
// §1.5 step-3 path) — no instanceOf link needed, mirroring how
// capabilityproposal's own root resolves (unlike lease-signing's
// service.<family>.instance, which needs the instanceOf indirection because
// its fine-grained class never matches a DDL name).
func capabilityAuthorClaimDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "capabilityauthorclaim",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAuthoringClaim"},
		Description: "ExternalTask instanceOp DDL (Contract #10 §10.5) for the AI-authored-capabilities " +
			"escalation dispatch. The op the capabilityAuthor Loom pattern submits: payload {instanceKey (the " +
			"bare handle Loom minted), subjectKey (the vtx.capabilityproposal.<id> the pattern runs over), " +
			"adapter, replyOp, params:{requesterId, intent, contextRef} (subject-templated tokens Loom declared " +
			"the read-set for; resolved here via orchestration-base's resolve_subject_params against the " +
			"subject's own .request aspect)}. Mints vtx.capabilityauthorclaim.<handle> (root data {} — D5) with " +
			"a .target aspect {proposalKey} pointing back at the subject — the indirection that lets " +
			"RecordCapabilityProposal resolve the real proposal from the Loom-minted (and therefore " +
			"proposal-id-independent) externalRef. Also writes a create-only .claim aspect onto the SUBJECT " +
			"proposal vertex itself (no new vertex for that half — the subject already exists), which closes " +
			"the capabilityAuthorPending lens's missing_authoring gap immediately. Emits the " +
			"external.capabilityAuthor event via its own transactional outbox, its body carrying " +
			"dispatchOp: RecordAuthoringDispatch — the op the bridge posts back if its capabilityAuthor adapter " +
			"returns Pending (internal/bridge/dispatch.go's Pending path requires one; see " +
			"capabilityAuthorClaimDispatchDDL).",
		Script: capabilityAuthorClaimDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"instanceKey":{"type":"string","description":"The BARE instance handle Loom minted (no dots / key segments / wildcards); the op prepends vtx.capabilityauthorclaim. Required."},` +
			`"subjectKey":{"type":"string","description":"vtx.capabilityproposal.<id> the pattern runs over (the pattern's own subject) — required, shape-validated (exactly vtx.capabilityproposal.<NanoID>) and validated alive."},` +
			`"adapter":{"type":"string","description":"The external adapter name (capabilityAuthor), carried into the external.<adapter> event. Required."},` +
			`"replyOp":{"type":"string","description":"The result-op the bridge posts back (RecordCapabilityProposal), carried into the external event. Required."},` +
			`"params":{"type":"object","description":"Subject-templated adapter params (subject.request.data.{requesterId,intent,contextRef}), resolved against the subject's own .request aspect before emission."}},` +
			`"required":["instanceKey","subjectKey","adapter","replyOp"]}`,
		OutputSchema: `{"type":"object","properties":{"primaryKey":{"type":"string","description":"vtx.capabilityproposal.<id> — the subject the claim was recorded against (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"instanceKey": "The bare instance handle Loom minted for this externalTask. Echoed back as the reply op's externalRef and the bridge's adapter dedup key. Required.",
			"subjectKey":  "Full vtx.capabilityproposal.<id> key of the proposal the escalation is for. Shape-validated (rejects any other vertex type) and validated alive; the .claim aspect is written here. Required.",
			"adapter":     "The registered bridge adapter name (capabilityAuthor). Required.",
			"replyOp":     "The result-op type the bridge posts back (RecordCapabilityProposal). Required.",
			"params":      "Subject-templated params resolved against the subject's own .request aspect before the external event is emitted.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateAuthoringClaim — dispatch the reasoning call for a pending request",
				Payload: map[string]any{
					"instanceKey": "<bareHandle>",
					"subjectKey":  "vtx.capabilityproposal.capPropOneHJKMNPQRST",
					"adapter":     "capabilityAuthor",
					"replyOp":     "RecordCapabilityProposal",
					"params": map[string]any{
						"requesterId": "subject.request.data.requesterId",
						"intent":      "subject.request.data.intent",
						"contextRef":  "subject.request.data.contextRef",
					},
				},
				ExpectedOutcome: "Validates the proposal is alive. Atomically commits vtx.capabilityauthorclaim.<handle> " +
					"(root {} — D5) + its .target aspect {proposalKey: subjectKey}, and a create-only .claim aspect on " +
					"the proposal itself. Emits the external.capabilityAuthor event (body {instanceKey, adapter, replyOp, " +
					"dispatchOp: RecordAuthoringDispatch, params (resolved), externalRef, idempotencyKey}) off the op's " +
					"outbox. Returns primaryKey (the proposal key). Rejects with ScriptError if the proposal is absent " +
					"or the handle is malformed.",
			},
		},
	}
}

// capabilityAuthorClaimDDLScript handles CreateAuthoringClaim. Known-key
// reads only (kv.Read of the subject root + its .request aspect via
// resolve_subject_params).
const capabilityAuthorClaimDDLScript = `
` + orchestrationbase.ResolveSubjectParamsHelper + `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def required_vertex_key(p, name, want_type):
    v = required_string(p, name)
    parts = v.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID> (exactly 3 segments); got " + v)
    return v

def alive(doc):
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateAuthoringClaim":
        # actor-guard: (primordial) restricted to Loom's relay actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the escalation. The
        # branch resolves the payload-named subjectKey's own .request aspect
        # into params it forwards to an external adapter, so a wider submitter
        # set is an arbitrary proposal's contents reaching a vendor.
        if op.actor != primordialActor["loom"]:
            fail("AuthDenied: CreateAuthoringClaim is restricted to Loom's relay actor; got " + op.actor)

        handle = required_bare_handle(p, "instanceKey")
        subject_key = required_vertex_key(p, "subjectKey", "capabilityproposal")
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")

        # read-posture: (a) declared in contextHint.reads by Loom's
        # inferExternalTaskReads (internal/loom/externaltask_params.go) —
        # every externalTask step's subjectKey is declared unconditionally
        subject_doc = kv.Read(subject_key)
        if not alive(subject_doc):
            fail("UnknownCapabilityProposal: " + subject_key)

        claim_key = "vtx.capabilityauthorclaim." + handle

        params = {}
        if hasattr(p, "params") and p.params != None:
            params = p.params
        resolved_params = resolve_subject_params(params, subject_key)

        mutations = [
            make_vtx(claim_key, "capabilityauthorclaim", {}),
            make_aspect(claim_key, "target", "capabilityAuthorClaim.target", {"proposalKey": subject_key}),
            make_aspect(subject_key, "claim", "capabilityAuthor.claim", {"claimedAt": op.submittedAt, "claimKey": claim_key}),
        ]
        event_data = {
            "instanceKey":    handle,
            "adapter":        adapter,
            "replyOp":        reply_op,
            "dispatchOp":     "RecordAuthoringDispatch",
            "externalRef":    handle,
            "idempotencyKey": handle,
            "params":         resolved_params,
        }
        events = [{"class": "external." + adapter, "data": event_data}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": subject_key}}

    fail("capabilityauthorclaim DDL: unknown operationType: " + ot)
`

// aspectDeclarationOnlyScript is the Starlark for the aspect-type DDLs. The
// aspect is written by the capabilityAuthorClaimDispatch vertexType DDL's op
// script; this aspect-type DDL is a step-6 write gate only, never an op
// handler — it fails closed if dispatched (mirrors packages/lease-signing's
// aspectDeclarationOnlyScript).
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("aspect-type DDL: not an operation handler: " + op.operationType)
`

// capabilityAuthorClaimDispatchDDL declares RecordAuthoringDispatch — the
// externalTask dispatchOp the bridge submits when its capabilityAuthor
// adapter returns Pending (internal/bridge/dispatch.go:293-298 requires one
// on every Pending outcome). Mirrors packages/lease-signing's
// leaseServiceDispatchDDL byte-for-byte in role, adapted to the claim vertex:
// this op reconstructs vtx.capabilityauthorclaim.<externalRef> from the bare
// handle CreateAuthoringClaim already minted for it (the SAME vertex, never a
// new one) and writes a create-only .dispatch aspect, the pending marker.
//
// This DDL's CanonicalName is deliberately NOT "capabilityAuthorDispatch" —
// that name is already the package's escalation-dispatch WEAVER TARGET
// (targets.go), and a meta canonicalName is a single flat namespace the
// installer's ErrCanonicalNameCollision enforces kernel-wide (the Processor's
// DDL cache serves one meta-vertex per canonicalName and drops the other).
// "capabilityAuthorClaimDispatch" names what this DDL actually does — record
// a dispatch marker on the CLAIM vertex — with no such collision.
func capabilityAuthorClaimDispatchDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "capabilityAuthorClaimDispatch",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"RecordAuthoringDispatch"},
		Description: "ExternalTask dispatchOp DDL (Contract #10 §10.5/§10.6) for the AI-authored-capabilities " +
			"escalation dispatch — mirrors lease-signing's RecordServiceDispatch byte-for-byte in role. The op the " +
			"bridge submits when its capabilityAuthor adapter returns Pending (the reasoning call was submitted " +
			"but has not resolved yet): payload {externalRef (the bare handle Loom minted for CreateAuthoringClaim), " +
			"vendorRef (the vendor's opaque pending reference — the poll/webhook key), adapter (which adapter to " +
			"Poll), replyOp (the result-op to post on resolve/timeout), nextPollAt + deadline (the bridge's " +
			"schedule instants)}. The bridge submits it with no ContextHint.Reads, so the op reads NOTHING from " +
			"state: it reconstructs the claim vertex key vtx.capabilityauthorclaim.<externalRef> from the bare " +
			"handle and writes a create-only .dispatch aspect {vendorRef, adapter, replyOp, submittedAt " +
			"(canonical-UTC of op.submittedAt), nextPollAt, deadline} — the PENDING MARKER. It writes NO change to " +
			"the proposal's own .artifact/.review aspects and emits NO completion signal: the reasoning call is " +
			"NOT done, so capabilityAuthorPending's missing_authoring stays exactly as CreateAuthoringClaim's " +
			".claim write already left it (closed) — that lens reads only .claim/.artifact (lenses.go:111-117), so " +
			"the .dispatch aspect cannot perturb the gap predicate either way. It emits " +
			"capabilityAuthor.dispatchRecorded (provenance, NOT a completion signal). The marker is recorded " +
			"once: the .dispatch aspect is create-only, so a redelivered Pending conflicts and is rejected (atop " +
			"the bridge's deterministic dispatch-op requestId collapse).",
		Script: capabilityAuthorClaimDispatchDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"externalRef":{"type":"string","description":"The BARE instance handle the bridge echoes (no dots / key segments); the op reconstructs vtx.capabilityauthorclaim.<externalRef>. Required."},` +
			`"vendorRef":{"type":"string","description":"The vendor's opaque pending reference (the poll/webhook key) the bridge got back from its adapter on a Pending outcome. Recorded on the .dispatch marker. Required."},` +
			`"adapter":{"type":"string","description":"The adapter name to re-call on a poll, recorded on the .dispatch marker for the lens / Weaver read-model (the fired handler reads the adapter from the schedule payload). Required."},` +
			`"replyOp":{"type":"string","description":"The result-op type the fired handler posts when the poll resolves or the call times out (RecordCapabilityProposal). Required."},` +
			`"nextPollAt":{"type":"string","description":"RFC3339 instant the next poll is due (the bridge armed schedule.bridge.poll at this instant). Normalized to canonical UTC on the marker. Required."},` +
			`"deadline":{"type":"string","description":"RFC3339 instant the call gives up (the bridge armed schedule.bridge.timeout at this instant); the marker records the same instant for the lens / Weaver read-model. Normalized to canonical UTC. Required."}},` +
			`"required":["externalRef","vendorRef","adapter","replyOp","nextPollAt","deadline"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.capabilityauthorclaim.<handle> of the claim vertex the pending marker was recorded on (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare instance handle the bridge echoes back (the same handle CreateAuthoringClaim received as instanceKey). The op reconstructs vtx.capabilityauthorclaim.<externalRef> and writes the create-only .dispatch marker on it. Required.",
			"vendorRef":   "The vendor's opaque pending reference (the poll/webhook key) the bridge received from its adapter when the external call returned Pending. Written to the .dispatch aspect; a later poll resolution carries it back. Required.",
			"adapter":     "The adapter name to re-call on a poll, recorded on the .dispatch marker for the lens / Weaver read-model (the fired handler reads the adapter from the schedule payload, not the marker). Required.",
			"replyOp":     "The result-op type (RecordCapabilityProposal) the fired handler posts when the poll resolves or the call times out. Required.",
			"nextPollAt":  "RFC3339 instant the next poll is due — the instant the bridge armed schedule.bridge.poll.<handle> at. Normalized to canonical UTC on the marker. Required.",
			"deadline":    "RFC3339 instant the call gives up — the instant the bridge armed schedule.bridge.timeout.<handle> at. The marker records the same instant for the lens / Weaver read-model. Normalized to canonical UTC. Required.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "RecordAuthoringDispatch — record a pending reasoning call",
				Payload: map[string]any{
					"externalRef": "<bareHandle>",
					"vendorRef":   "model-runner-ref-abc123",
					"adapter":     "capabilityAuthor",
					"replyOp":     "RecordCapabilityProposal",
					"nextPollAt":  "2026-08-21T10:00:30Z",
					"deadline":    "2026-08-21T10:05:00Z",
				},
				ExpectedOutcome: "Reads no state (the bridge submits no Reads). Reconstructs vtx.capabilityauthorclaim.<handle> " +
					"from the bare handle. Writes the .dispatch aspect {vendorRef, adapter, replyOp, submittedAt: " +
					"canonical-UTC(op.submittedAt), nextPollAt, deadline} as a create-only mutation (the claim root, " +
					"already {}, is untouched — D5). Writes NO change to the proposal's .artifact/.review and emits NO " +
					"completion signal (the reasoning call is not done). Emits capabilityAuthor.dispatchRecorded " +
					"(provenance). Returns primaryKey. Rejects a second dispatch for the same handle (the create-only " +
					".dispatch once-only guard).",
			},
		},
	}
}

// capabilityAuthorClaimDispatchDDLScript handles RecordAuthoringDispatch.
// Read-free: the bridge submits with no ContextHint.Reads (mirrors
// packages/lease-signing's leaseServiceDispatchDDLScript, scripts.go:1453-1534
// — every helper below is that script's, ported byte-for-byte with the
// vertex-type prefix/event names adapted to this package).
const capabilityAuthorClaimDispatchDDLScript = `
def make_aspect(vtx_key, local_name, cls, data):
    return {"op": "create", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def required_instant(p, name):
    # An RFC3339 instant the bridge computed (nextPollAt / deadline), normalized
    # to canonical UTC so the marker compares lexically with the schedule headers.
    v = required_string(p, name)
    return time.rfc3339_utc(v)

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordAuthoringDispatch":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle — the SAME
        # vertex CreateAuthoringClaim minted for this handle. No state read
        # needed: the reconstruction and the bare-handle validation are both
        # pure functions of the payload.
        claim_key = "vtx.capabilityauthorclaim." + handle

        # The vendor's opaque pending reference (the poll/webhook key the bridge
        # got back from its adapter). Required — a Pending with no ref is meaningless.
        vendor_ref = required_string(p, "vendorRef")

        # The routing recorded for the lens / Weaver read-model: which adapter to
        # Poll on a poll firing, and which replyOp to post when the poll resolves or
        # the call times out. The fired handler reads these from the schedule
        # payload, not the marker; both are required here for the read-model record.
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")

        # The bridge-supplied schedule instants: when the next poll is due and when
        # the call gives up. Recorded for the lens / Weaver read-model; the timeout
        # itself fires from the armed schedule, not this marker.
        next_poll_at = required_instant(p, "nextPollAt")
        deadline = required_instant(p, "deadline")

        # submittedAt is the op's own timestamp, normalized to canonical UTC. The
        # bridge supplies no timestamp; this is the dispatch instant.
        submitted_at = time.rfc3339_utc(op.submittedAt)

        # Write the .dispatch aspect {vendorRef, adapter, replyOp, submittedAt,
        # nextPollAt, deadline} as a create-only mutation. This create-only IS the
        # once-only guarantee: a redelivered Pending conflicts on the existing key
        # and the batch is rejected (atop the bridge's deterministic dispatch
        # requestId collapse). NO change to the proposal's .artifact/.review and NO
        # completion signal is emitted — the reasoning call is not done. The claim
        # root, already {}, is untouched (D5).
        mutations = [
            make_aspect(claim_key, "dispatch", "capabilityAuthorDispatchMarker",
                        {"vendorRef": vendor_ref, "adapter": adapter, "replyOp": reply_op,
                         "submittedAt": submitted_at, "nextPollAt": next_poll_at, "deadline": deadline}),
        ]

        # A provenance event marks the submit for the audit join (NOT a completion
        # signal — the escalation is NOT done).
        events = [
            {"class": "capabilityAuthor.dispatchRecorded",
             "data": {"claimKey": claim_key, "vendorRef": vendor_ref, "submittedAt": submitted_at}},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": claim_key}}

    fail("capabilityAuthorClaimDispatch DDL: unknown operationType: " + ot)
`

// capabilityAuthorDispatchMarkerDDL declares the .dispatch aspect (class
// capabilityAuthorDispatchMarker) — the step-6 write gate for
// RecordAuthoringDispatch. Necessary for the SAME reason
// leaseServiceDispatchAspectDDL is necessary in lease-signing, even though
// this package's root-vertex resolution otherwise needs no instanceOf
// indirection (capabilityAuthorClaimDDL's own doc comment): without an exact
// class match, a .dispatch write would fall back to resolving by the
// vertex's TYPE segment ("capabilityauthorclaim") to capabilityAuthorClaimDDL
// — which permits only CreateAuthoringClaim — and fail closed. The vertexType
// DDL capabilityAuthorClaimDispatch (the op script) and this aspect class
// share the name capabilityAuthorDispatchMarker; aspectType DDLs are excluded
// from the operationType->class reverse index, so there is no script-selection
// ambiguity. Declaration-only.
func capabilityAuthorDispatchMarkerDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "capabilityAuthorDispatchMarker",
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"RecordAuthoringDispatch"},
		Description: "AI-authored-capabilities claim pending-dispatch aspect. Stored as " +
			"vtx.capabilityauthorclaim.<handle>.dispatch (class capabilityAuthorDispatchMarker) = {vendorRef, " +
			"adapter, replyOp, submittedAt, nextPollAt, deadline}. The async PENDING marker (the capabilityAuthor " +
			"adapter returned Pending). Written ONLY by RecordAuthoringDispatch (whose " +
			"capabilityAuthorClaimDispatch vertexType DDL owns the script); this aspect-type DDL is the step-6 " +
			"write gate (exact class match). Declaration-only: no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"vendorRef":{"type":"string"},"adapter":{"type":"string"},"replyOp":{"type":"string"},"submittedAt":{"type":"string"},"nextPollAt":{"type":"string"},"deadline":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"vendorRef":   "The vendor's opaque pending reference.",
			"adapter":     "The adapter to re-call on a poll.",
			"replyOp":     "The result-op posted on resolve/timeout.",
			"submittedAt": "RFC3339 instant the pending marker was recorded (canonical UTC).",
			"nextPollAt":  "RFC3339 instant the next poll is due.",
			"deadline":    "RFC3339 instant the call gives up.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "capability-author claim dispatch (pending) aspect",
				Payload: map[string]any{
					"vendorRef": "model-runner-ref-abc123", "adapter": "capabilityAuthor", "replyOp": "RecordCapabilityProposal",
				},
				ExpectedOutcome: "Stored as vtx.capabilityauthorclaim.<handle>.dispatch; written by RecordAuthoringDispatch.",
			},
		},
	}
}
