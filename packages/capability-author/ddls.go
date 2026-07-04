package capabilityauthor

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// A single `capabilityproposal` vertex-type DDL owns the two-op capture pair
// that records one authoring episode (ai-authored-capabilities-design.md §3.1,
// §3.3). This is Increment 1 of Fire 1 — the COMPLETE lens-kind loop's capture
// half (propose → deterministically validate → record); the escalation
// dispatch (triggerLoom → externalTask → the capabilityAuthor bridge adapter),
// the review/apply ops, and the catalog/review lenses are the fire's remaining
// increments (see the design doc's checkpoint).
//
//   - RequestCapabilityAuthoring mints the proposal vertex write-ahead of the
//     reasoning call (mirrors augur's CreateAugurReasoningClaim), recording the
//     requester + intent. No artifact yet — the reasoning call (in the
//     remaining increments) is what fills it in.
//   - RecordCapabilityProposal is the bridge replyOp: payload {externalRef,
//     status, result} — the standard generic reply shape
//     internal/bridge/dispatch.go's terminal-outcome leg always submits
//     (mirrors augur's RecordProposal exactly). The proposal id is the bare
//     handle in externalRef, never a separate payload field. On status=completed
//     it decodes a single `result` JSON blob for kind/content/target/rationale/
//     confidence/validation*/provenance*; the `validation` sub-object is the
//     ALREADY-COMPUTED §5 deterministic-validation verdict (in the full design,
//     computed by the bridge via pkgmgr.ValidateCapabilityArtifact before it
//     submits this op — the same "compute client-side, submit a trusted
//     verdict" split F-004's own install path uses). The script does NOT
//     re-run cypher parsing or validateAll itself (a Starlark DDL script has no
//     parser/registry access) — it trusts the decoded verdict from this op's
//     privileged submitter (the bridge). A decode failure (empty/non-JSON/
//     non-object result) or status=failed (a modeled refusal) NEVER fail()s the
//     op — the bridge has already Ack'd the external event, so a reject would
//     wedge the episode with no record; both store the proposal review.state=
//     invalid instead (auditable, never dispatchable). The KERNEL step-8
//     protected-key guard at APPLY time (a later increment's F-004 op
//     submission) remains the authoritative, independent backstop regardless
//     (design §5 point 4) — this op only ever produces a `pending`-or-`invalid`
//     PROPOSAL, never a write to any other vertex.
//
// Architectural rules (binding — same known-key discipline as augur /
// orchestration-base / lease-signing):
//   - Both ops read ONLY by known key (kv.Read of the proposal's own aspects).
//     No prefix scans, no adjacency lookups, no lens reads.
//   - No-orphan invariant (FR29/P4): RecordCapabilityProposal REQUIRES the
//     proposal's .request aspect to be live (the RequestCapabilityAuthoring op
//     must have committed write-ahead) and rejects (structured ScriptError) if
//     absent — the model can never fabricate a proposal with no request.
//   - The requester identity comes from op.actor (the TRUSTED submitting
//     actor), never a payload field — the same don't-trust-the-payload-for-
//     identity discipline augur's ReviewProposal reviewer uses.
//   - The proposal is idempotent on redelivery: both ops write their aspects
//     create-only, so a redelivered op conflicts (Contract #4 tracker collapse
//     backstop) rather than double-recording.
//
// Proposal shape (Contract #1 key shapes; D5 — minimal root, business data in
// aspects; design §3.1):
//
//	vtx.capabilityproposal.<id>   root data = {}
//	  .request     { requesterId, intent, contextRef }            RequestCapabilityAuthoring
//	  .artifact    { kind, content }                              RecordCapabilityProposal
//	  .target      { mode, packageName, baseVersion, newVersion } RecordCapabilityProposal
//	  .rationale   { text }                                       RecordCapabilityProposal
//	  .confidence  { score }                                      RecordCapabilityProposal
//	  .validation  { state, report, deltaPreview, checkedAt }     RecordCapabilityProposal
//	  .provenance  { model, promptHash, catalogHash, reasonedAt } RecordCapabilityProposal
//	  .review      { state, reviewedAt, appliedAt, appliedByOp }  RecordCapabilityProposal (state only, this increment)
//	lnk.capabilityproposal.<id>.requestedBy.<type>.<requesterId>  proposal requestedBy requester
//
// review.state ∈ {pending, invalid} in this increment (approved/rejected/
// applied/superseded arrive with the review + apply ops). Absence of
// .artifact/.review = the request is recorded but not yet authored (mirrors
// augur's claim-in-flight — absence of .proposed/.review).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		capabilityProposalDDL(),
	}
}

func capabilityProposalDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "capabilityproposal",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"RequestCapabilityAuthoring", "RecordCapabilityProposal"},
		Description: "AI-authored capability proposal DDL — Increment 1 (design §3.1/§3.3): the capture " +
			"pair for one authoring episode. Vertex shape: vtx.capabilityproposal.<id>, class=capabilityproposal, " +
			"root data = {} (D5); business data in aspects: .request {requesterId, intent, contextRef} (the " +
			"RequestCapabilityAuthoring instanceOp's write-ahead context), .artifact {kind, content}, " +
			".target {mode, packageName, baseVersion, newVersion}, .rationale {text}, .confidence {score}, " +
			".validation {state, report, deltaPreview, checkedAt}, .provenance {model, promptHash, catalogHash, " +
			"reasonedAt} (RecordCapabilityProposal's model-derived data + the §5 deterministic-validation " +
			"verdict). Relationship: requestedBy (proposal→requester identity), a LINK. " +
			"RequestCapabilityAuthoring mints the proposal vertex write-ahead with the .request aspect + the " +
			"requestedBy link (no-orphan by construction — the requester is op.actor, the trusted submitting " +
			"actor). RecordCapabilityProposal is the bridge replyOp (payload {externalRef, status, result}, " +
			"mirroring augur's RecordProposal): the proposal id is the bare handle in externalRef, never a " +
			"payload field; requires a live .request aspect (the instanceOp must commit first); on " +
			"status=completed it decodes a single result JSON blob {kind, content, target:{mode, packageName, " +
			"baseVersion, newVersion}, rationale, confidence, validation:{state, report, deltaPreview}, " +
			"provenance:{model, promptHash, catalogHash, reasonedAt}} — the ALREADY-COMPUTED §5 verdict travels " +
			"as validation.state/report. Writes review.state=pending only when kind is an enabled artifact kind " +
			"(increment 1: lens only), validation.state is 'valid', and confidence is a real 0..1 score; " +
			"otherwise (including status=failed — a modeled refusal — and an empty/non-JSON/non-object " +
			"completed result) review.state=invalid with an auditable invalidReason — the proposal is ALWAYS " +
			"recorded (auditability, never fail()ed post-Ack), never applicable when invalid. The script does " +
			"not itself run the openCypher parser or validateAll (no parser/registry access from Starlark) — it " +
			"trusts the decoded validation verdict from this op's privileged submitter (the bridge in the full " +
			"design), computed by pkgmgr.ValidateCapabilityArtifact before submission; the kernel's F-004 " +
			"apply-time step-8 guard (a later increment) remains the authoritative, independent backstop " +
			"regardless.",
		Script: capabilityProposalDDLScript,
		InputSchema: `{"type":"object","description":"RequestCapabilityAuthoring{proposalId,intent,contextRef?} | RecordCapabilityProposal — the bridge replyOp. The bridge posts {externalRef,status,result}; the artifact/target/rationale/confidence/validation/provenance fields are decoded from a single JSON result blob, never top-level payload fields.","properties":` +
			`{"proposalId":{"type":"string","description":"RequestCapabilityAuthoring only — bare NanoID (no dots/wildcards/whitespace) naming vtx.capabilityproposal.<proposalId>. Caller-supplied (the auto-minted-by-Loom form lands with the escalation-dispatch increment)."},` +
			`"intent":{"type":"string","description":"RequestCapabilityAuthoring only — the plain-language capability request, e.g. 'a lens listing active providers by specialty'."},` +
			`"contextRef":{"type":"string","description":"RequestCapabilityAuthoring only — an optional pointer to bounded context the reasoning call hydrates (opaque to this DDL)."},` +
			`"externalRef":{"type":"string","description":"RecordCapabilityProposal only — the bare instanceKey handle of the authoring episode; the proposal vertex is vtx.capabilityproposal.<externalRef> (must match the proposalId a RequestCapabilityAuthoring already minted)."},` +
			`"status":{"type":"string","description":"RecordCapabilityProposal only — the adapter's terminal outcome: completed (the model proposed an artifact) or failed (a modeled refusal — stored invalid, never dispatchable)."},` +
			`"result":{"type":"string","description":"RecordCapabilityProposal only — the model's structured-output proposal as a JSON string {kind, content, target:{mode,packageName,baseVersion?,newVersion?}, rationale, confidence, validation:{state,report?,deltaPreview?}, provenance:{model?,promptHash?,catalogHash?,reasonedAt?}} — the opaque adapter Detail. Required when status=completed; carried verbatim as the rationale on a refusal. validation.state is the ALREADY-COMPUTED §5 verdict (pkgmgr.ValidateCapabilityArtifact, run by the trusted caller before submission — the script does not itself re-run the parser/validateAll). kind enables only 'lens' in this increment; any other value, or a validation.state other than 'valid', or an out-of-range confidence, or an undecodable result stores the proposal review.state=invalid (auditable, never a hard reject)."}},` +
			`"required":["proposalId"]}`,
		OutputSchema: `{"type":"object","properties":{"primaryKey":{"type":"string","description":"vtx.capabilityproposal.<id> of the created/updated proposal. The recorded review.state (pending|invalid) is read from the proposal's .review aspect, not the op response."}}}`,
		FieldDescription: map[string]string{
			"proposalId":  "Bare NanoID naming the proposal vertex (RequestCapabilityAuthoring).",
			"intent":      "The plain-language capability request (RequestCapabilityAuthoring).",
			"externalRef": "The bare instanceKey handle naming the proposal vertex; must match a RequestCapabilityAuthoring's proposalId (RecordCapabilityProposal).",
			"status":      "The bridge's terminal outcome: completed or failed (RecordCapabilityProposal).",
			"result":      "The model's proposal as a JSON string, decoded for kind/content/target/rationale/confidence/validation*/provenance* (RecordCapabilityProposal).",
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
				Name: "RecordCapabilityProposal — a valid lens artifact (already §5-validated by the caller)",
				Payload: map[string]any{
					"externalRef": "capPropOneHJKMNPQRST",
					"status":      "completed",
					"result": `{"kind":"lens","content":"{\"canonicalName\":\"activeProvidersBySpecialty\",\"adapter\":\"nats-kv\",\"bucket\":\"active-providers\",\"spec\":\"MATCH (p:provider) RETURN p.key AS key\"}",` +
						`"target":{"mode":"newPackage"},"rationale":"no existing lens surfaces this projection","confidence":0.86,"validation":{"state":"valid"}}`,
				},
				ExpectedOutcome: "review.state = pending (dispatchable in a later increment's apply op); the .artifact/.target/.rationale/.confidence/.validation/.provenance aspects are recorded.",
			},
			{
				Name: "RecordCapabilityProposal — a modeled refusal",
				Payload: map[string]any{
					"externalRef": "capPropOneHJKMNPQRST",
					"status":      "failed",
					"result":      "the requested projection would expose PHI without a masking clause",
				},
				ExpectedOutcome: "review.state = invalid, invalidReason = 'model declined to propose (refusal)', .rationale.text carries the result verbatim.",
			},
		},
	}
}

// capabilityProposalDDLScript handles the matched pair
// RequestCapabilityAuthoring (mints the proposal write-ahead) +
// RecordCapabilityProposal (records a proposed artifact + its
// already-computed §5 verdict). Known-key reads only.
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

def parts_of(key, name):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
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

ENABLED_KINDS = ["lens"]

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RequestCapabilityAuthoring":
        proposal_id = required_bare_id(p, "proposalId")
        intent = required_string(p, "intent")
        context_ref = optional_string_attr(p, "contextRef")

        requester = op.actor
        requester_type, requester_id = parts_of(requester, "actor")

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

    if ot == "RecordCapabilityProposal":
        # The bridge replyOp: payload {externalRef, status, result} (the standard
        # generic reply shape internal/bridge/dispatch.go's terminal-outcome leg
        # always submits — mirrors augur's RecordProposal exactly). The proposal
        # id is the bare handle in externalRef, never a separate payload field.
        proposal_id = required_bare_id(p, "externalRef")
        proposal_key = "vtx.capabilityproposal." + proposal_id

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
            # A modeled refusal is a definitive verdict, NOT a crash: store the
            # proposal invalid (auditable, never dispatchable) and ride the
            # adapter's detail on the rationale for the reviewer (mirrors augur's
            # RecordProposal refusal branch exactly).
            review_state = "invalid"
            invalid_reason = "model declined to propose (refusal)"
            rationale = optional_string_attr(p, "result")
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
                target_package_name = proposal_string(target, "packageName")
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
                        {"model": model, "promptHash": prompt_hash,
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

    fail("capabilityproposal DDL: unknown operationType: " + ot)
`
