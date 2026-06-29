package augur

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// A single `augurproposal` vertex-type DDL owns the matched pair that drives one
// Augur reasoning episode against the bridge's standard `{externalRef, status,
// result}` reply contract. The reasoning call is single-step, so Weaver
// dispatches it directly (Option F — no Loom wrapper): CreateAugurReasoningClaim
// is submitted as a Weaver directOp, and the bridge replyOp records the verdict.
//
//   - CreateAugurReasoningClaim (the instanceOp, Weaver's directOp) mints the
//     claim vertex vtx.augurproposal.<handle> write-ahead of the reasoning call,
//     recording the TRUSTED gap context (.gap aspect) + the forCandidate /
//     forTarget links, and emits external.<adapter> off its own transactional
//     outbox.
//   - RecordProposal (the replyOp the bridge posts on a terminal outcome) reads
//     the trusted gap context back from that claim vertex, decodes the model's
//     structured proposal from the opaque `result` string, applies the design §5
//     record-time deterministic-validation boundary, writes the proposal's
//     model-derived aspects (.proposed / .rationale / .confidence / .provenance /
//     .review) create-only, and emits augur.proposalRecorded for the review lens.
//     There is no Loom instance to unpark, so no externalTaskCompleted is emitted.
//
// The load-bearing safety split (design §5): the entity/target IDENTITY the
// proposal acts on is read from the instanceOp-minted claim vertex — NEVER from
// the model's reply. The model supplies only {action, params, confidence,
// rationale, provenance}; it can propose arranging an action but can never name
// the entity it acts on. A redelivered reply collapses on the bridge's
// deterministic reply requestId and, as the final backstop, conflicts on the
// create-only .review aspect (FR58 at the DDL layer).
//
// Architectural rules (binding — same known-key discipline as orchestration-base
// / lease-signing):
//
//   - Both ops read ONLY by known key. The instanceOp validates its two link
//     endpoints by kv.Read of each (Contract #2 §2.5 lazy known-key read,
//     read-path-independent of how the op is dispatched). The replyOp reads the
//     claim's .gap aspect by kv.Read — the bridge posts the reply with no
//     ContextHint.Reads, so the op hydrates nothing from `state`. No prefix scans,
//     no adjacency lookups, no lens reads.
//   - No-orphan invariant (FR29 / P4): CreateAugurReasoningClaim REQUIRES the
//     candidate and the weaver target to be alive (the forCandidate / forTarget
//     links need live targets) and rejects (structured ScriptError) if either is
//     absent. RecordProposal REQUIRES the claim vertex to be live (its .gap
//     aspect) and rejects if absent — the instanceOp must have committed first.
//   - The deterministic-validation boundary (design §5, record-time leg) is the
//     safety core: the proposal is stored review.state=pending (dispatchable) only
//     when the proposed action is in the allowed escalation vocabulary, the
//     confidence is a real 0..1 score, and the proposal does not escape the
//     escalated candidate's scope. Any failure — and a modeled refusal
//     (status=failed) — stores the proposal review.state=invalid with an auditable
//     invalidReason, never pending, never dispatchable. The proposal vertex is
//     ALWAYS recorded (auditability); the verdict decides only pending vs invalid.
//
// Proposal shape (Contract #1 key shapes; D5 — minimal root, business data in
// aspects):
//
//	vtx.augurproposal.<handle>   root data = {}            (handle = the escalation episode's instanceKey)
//	  .gap         { targetId, entityId, gapColumn, trigger }   # instanceOp — TRUSTED escalation context
//	  .proposed    { action, params }                           # replyOp — the model's remediation
//	  .rationale   { text }                                     # replyOp — the model's reasoning (audit)
//	  .confidence  { score }                                    # replyOp — 0..1 self-reported
//	  .provenance  { model, promptHash, catalogHash, reasonedAt }  # replyOp
//	  .review      { state, invalidReason, reviewedAt, dispatchedAt }  # replyOp — verdict
//	lnk.augurproposal.<handle>.forCandidate.<type>.<entityId>   # proposal forCandidate candidate
//	lnk.augurproposal.<handle>.forTarget.meta.<weaverTargetId>  # proposal forTarget target
//
// Both links: augurproposal = the later-arriving SOURCE, the other vertex
// pre-exists = the TARGET (Contract #1 §1.1). The names pass the sentence test
// ("proposal forCandidate candidate", "proposal forTarget target"). Absence of
// .review / .proposed on a claim vertex = the reasoning call is still in flight
// (mirrors lease-signing's outcome-absence = not-yet-complete).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		augurproposalDDL(),
	}
}

func augurproposalDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "augurproposal",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateAugurReasoningClaim", "RecordProposal"},
		Description: "Augur proposal DDL — the externalTask matched pair for one reasoning episode. " +
			"Vertex shape: vtx.augurproposal.<handle>, class=augurproposal, root data = {} (D5); business " +
			"data in aspects: .gap {targetId, entityId, gapColumn, trigger} (the instanceOp's TRUSTED " +
			"escalation context), .proposed {action, params}, .rationale {text}, .confidence {score}, " +
			".provenance {model, promptHash, catalogHash, reasonedAt}, .review {state, invalidReason, " +
			"reviewedAt, dispatchedAt} (the replyOp's model-derived data + verdict). Relationships are LINKS: " +
			"forCandidate (proposal→candidate: the escalated entity), forTarget (proposal→weaverTarget meta: " +
			"the target whose gap was stuck). Both links: proposal is the later-arriving source (Contract #1 " +
			"§1.1). CreateAugurReasoningClaim is the reasoning instanceOp Weaver submits as a directOp: it mints " +
			"the claim vertex write-ahead with the .gap aspect + links (no-orphan, FR29/P4) and emits external.<adapter>. " +
			"RecordProposal is the bridge replyOp (payload {externalRef, status, result}): it reads the " +
			"trusted gap context back from the claim, decodes the model proposal from the opaque result " +
			"string, and applies the design §5 record-time deterministic-validation boundary — a proposal is " +
			"stored review.state=pending (dispatchable) only when its action is in the allowed escalation " +
			"vocabulary (triggerLoom|assignTask|directOp), its confidence is a real 0..1 score, and it does " +
			"not escape the escalated candidate's scope; otherwise (and on a model refusal, status=failed) it " +
			"is stored review.state=invalid with an auditable invalidReason. The proposal is always stored. " +
			"The model NEVER supplies the entity it acts on (read from the claim); idempotent on a redelivered " +
			"reply via the create-only .review aspect atop the bridge's deterministic reply requestId.",
		Script: augurproposalDDLScript,
		InputSchema: `{"type":"object","description":"RecordProposal — the bridge replyOp. The bridge posts {externalRef, status, result}; gap context is reconstructed from the claim vertex, never this payload.","properties":` +
			`{"externalRef":{"type":"string","description":"The bare instanceKey handle of the reasoning episode; the claim vertex is vtx.augurproposal.<externalRef>."},` +
			`"status":{"type":"string","description":"The adapter's terminal outcome: completed (the model proposed) or failed (a modeled refusal — stored invalid, never dispatchable)."},` +
			`"result":{"type":"string","description":"The model's structured-output proposal as a JSON string {action, params, confidence, rationale, model, promptHash, catalogHash, reasonedAt} — the opaque adapter Detail. Required when status=completed; carried as the rationale on a refusal."}},` +
			`"required":["externalRef","status"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.augurproposal.<handle> of the recorded proposal. The recorded review.state (pending | invalid) is read from the proposal's .review aspect, not the op response."}}}`,
		FieldDescription: map[string]string{
			"externalRef": "The bare instanceKey handle Weaver minted for the reasoning episode (no dots / key segments / whitespace); the claim vertex key is vtx.augurproposal.<externalRef>. RecordProposal rejects if no live claim vertex exists for it (the CreateAugurReasoningClaim instanceOp must commit write-ahead).",
			"status":      "The adapter's terminal outcome verbatim: completed (the model returned a structured proposal in result) or failed (a modeled refusal — the proposal is stored invalid with the refusal as its rationale, never dispatchable). Any other value rejects the op.",
			"result":      "The model's structured-output proposal as a JSON string {action, params, confidence, rationale, model, promptHash, catalogHash, reasonedAt}. The §5 validator decodes it and validates action ∈ {triggerLoom, assignTask, directOp}, confidence ∈ [0,1], and no scope escape (a params entity-key other than the escalated candidate, read from the trusted claim). Required when status=completed.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "CreateAugurReasoningClaim — mint the claim vertex write-ahead of the reasoning call (Weaver's directOp)",
				Payload: map[string]any{
					"instanceKey": "augurEpisodeHJKMNPQRST",
					"adapter":     "augur",
					"replyOp":     "RecordProposal",
					"targetId":    "vtx.meta.<weaverTargetNanoID>",
					"entityId":    "vtx.leaseapp.<applicantNanoID>",
					"gapColumn":   "missing_approval",
					"trigger":     "unplannable",
				},
				ExpectedOutcome: "Validates the weaver target + candidate are alive (no-orphan). Mints vtx.augurproposal." +
					"augurEpisodeHJKMNPQRST with root {} (D5), the .gap aspect carrying the TRUSTED escalation context, and " +
					"the forCandidate / forTarget links (proposal is the source). No .review / .proposed yet (the reasoning " +
					"call is in flight). Emits external.augur off the transactional outbox for the bridge to pick up.",
			},
			{
				Name: "RecordProposal — a valid assignTask proposal for an unplannable approval gap (the bridge replyOp)",
				Payload: map[string]any{
					"externalRef": "augurEpisodeHJKMNPQRST",
					"status":      "completed",
					"result":      `{"action":"assignTask","params":{"scopedTo":"vtx.leaseapp.<applicantNanoID>","forOperation":"ApproveLeaseApplication"},"rationale":"No playbook entry; the closest catalog action is a human approval task scoped to the applicant.","confidence":0.82,"model":"claude-opus-4-8","reasonedAt":"2026-06-29T15:00:00Z"}`,
				},
				ExpectedOutcome: "Reads the trusted entity (vtx.leaseapp.<applicantNanoID>) from the claim's .gap aspect, " +
					"decodes the proposal from result. The action is in the allowed vocabulary, confidence is in [0,1], and the " +
					"proposed scopedTo matches the escalated candidate, so the proposal is stored review.state=pending " +
					"(dispatchable) + its model-derived aspects. Emits augur.proposalRecorded for the review lens (no Loom to unpark).",
			},
			{
				Name: "RecordProposal — a scope-escaping proposal is stored invalid (auditable, never dispatchable)",
				Payload: map[string]any{
					"externalRef": "augurEpisodeHJKMNPQRST",
					"status":      "completed",
					"result":      `{"action":"directOp","params":{"scopedTo":"vtx.leaseapp.<aDifferentApplicantNanoID>"},"confidence":0.95}`,
				},
				ExpectedOutcome: "The proposed scopedTo names a DIFFERENT entity than the escalated candidate (read from the " +
					"trusted claim, not the reply), so the §5 scope-escape check fails: the proposal is still stored " +
					"(auditability) but with review.state=invalid + an invalidReason, never pending, never dispatchable.",
			},
		},
	}
}

// augurproposalDDLScript handles the matched pair CreateAugurReasoningClaim
// (the instanceOp Weaver submits as a directOp) + RecordProposal (the bridge
// replyOp). Known-key reads only (kv.Read of the link endpoints / the claim's
// .gap aspect — the bridge posts the reply with no ContextHint.Reads). The §5
// record-time deterministic-validation boundary decides pending vs invalid on the
// reply; the proposal is always stored (auditability). No-orphan by construction.
const augurproposalDDLScript = `
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

def required_bare_handle(p, name):
    # The bare instance handle Loom minted: type-free, must carry no key
    # delimiters so "vtx.augurproposal." + handle is a single well-formed key.
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def proposal_string(d, name):
    # A field of the decoded model proposal; absent / wrong-typed => "".
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
    # confidence: absent / non-numeric => -1.0 (out of range => invalid verdict).
    if name not in d:
        return -1.0
    v = d[name]
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        return -1.0
    return v

def parts_of(key, name, want_type):
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID> (exactly 3 segments); got " + key)
    if parts[1] == "":
        fail("InvalidArgument: " + name + ": empty type segment; required vtx.<type>.<NanoID>; got " + key)
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def alive(doc):
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

# The allowed escalation action vocabulary (design §5). A proposal naming any
# other action is stored invalid — the model gains NO new authority, only the
# ability to PROPOSE arranging the actions Weaver already has.
ALLOWED_ACTIONS = ["triggerLoom", "assignTask", "directOp"]

# The param keys that name an entity. A proposed action whose entity-naming param
# references a candidate OTHER than the escalated one is a scope escape (design
# §5): the model cannot propose acting on a different entity than the gap it was
# asked to reason about. The escalated candidate is read from the TRUSTED claim
# vertex, never the model's reply. The check compares against both the full vertex
# key and the bare NanoID, since a param may carry either form.
ENTITY_PARAM_KEYS = ["scopedTo", "subject", "subjectKey", "entity", "entityKey", "candidate"]

def scope_escape(params, entity_key, entity_id):
    # Returns the offending param name (non-empty) on a scope escape, else "".
    for k in ENTITY_PARAM_KEYS:
        if k not in params:
            continue
        v = params[k]
        if v == None or type(v) != type("") or len(v.strip()) == 0:
            continue
        v = v.strip()
        if v != entity_key and v != entity_id:
            return k
    return ""

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateAugurReasoningClaim":
        # The reasoning instanceOp: mint the claim vertex write-ahead of the
        # reasoning call, recording the TRUSTED escalation context. Dispatched by
        # Weaver as a directOp (Option F) — every param arrives FLAT at the
        # top-level payload (Weaver's directOp resolves a flat params map from the
        # lens row); there is no nested params object.
        handle = required_bare_handle(p, "instanceKey")
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")
        target_key = required_string(p, "targetId")
        entity_key = required_string(p, "entityId")
        gap_column = required_string(p, "gapColumn")
        trigger = required_string(p, "trigger")

        parts_of(target_key, "targetId", "meta")
        entity_type, entity_id = parts_of(entity_key, "entityId", "")
        target_id = target_key.split(".")[2]

        # No-orphan (FR29 / P4): both link endpoints MUST be alive. Weaver's
        # directOp routes the candidate / target keys into the op's
        # ContextHint.Reads, but the alive checks use kv.Read (Contract #2 §2.5
        # known-key lazy read) — read-path-independent, matched to the bridge reply
        # leg's no-Reads posture.
        if not alive(kv.Read(target_key)):
            fail("UnknownTarget: " + target_key)
        if not alive(kv.Read(entity_key)):
            fail("UnknownCandidate: " + entity_key)

        proposal_key = "vtx.augurproposal." + handle
        forcand_lnk = "lnk.augurproposal." + handle + ".forCandidate." + entity_type + "." + entity_id
        fortarget_lnk = "lnk.augurproposal." + handle + ".forTarget.meta." + target_id

        # Mint the claim vertex write-ahead with the TRUSTED gap context. The .gap
        # aspect is the model-independent record of what was escalated; the replyOp
        # reads entity/target identity from HERE, never from the model's reply (the
        # load-bearing safety split, design §5). No .review / .proposed yet =
        # reasoning in flight (mirrors lease-signing's outcome-absence). A redelivered
        # instanceOp conflicts create-only on the root vertex and is rejected.
        mutations = [
            make_vtx(proposal_key, "augurproposal", {}),
            make_aspect(proposal_key, "gap", "augur.gap",
                        {"targetId": target_key, "entityId": entity_key,
                         "gapColumn": gap_column, "trigger": trigger}),
            make_link(forcand_lnk, proposal_key, entity_key, "forCandidate", "forCandidate", {}),
            make_link(fortarget_lnk, proposal_key, target_key, "forTarget", "forTarget", {}),
        ]
        # Emit external.<adapter> off this op's transactional outbox — the bridge's
        # externalEvent shape. The bare handle is the correlation token (instanceKey
        # == externalRef == idempotencyKey). params carries the gap context so the
        # adapter (and FakeAugur) can scope its proposal to the escalated candidate.
        event_data = {
            "instanceKey":    handle,
            "adapter":        adapter,
            "replyOp":        reply_op,
            "externalRef":    handle,
            "idempotencyKey": handle,
            "params":         {"entityId": entity_key, "targetId": target_key,
                               "gapColumn": gap_column, "trigger": trigger},
        }
        events = [{"class": "external." + adapter, "data": event_data}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": proposal_key}}

    if ot == "RecordProposal":
        # The bridge replyOp: payload {externalRef, status, result}. Reconstruct the
        # claim-vertex key from the bare handle; the bridge submits this with no
        # ContextHint.Reads, so every read is a kv.Read of a known key.
        handle = required_bare_handle(p, "externalRef")
        proposal_key = "vtx.augurproposal." + handle

        # Reconstruct the TRUSTED gap context from the claim vertex the instanceOp
        # minted write-ahead (design §5 safety split: entity identity comes from
        # HERE, never the model's reply). The claim MUST be live — its absence is a
        # wiring fault (the instanceOp must commit first).
        gap_doc = kv.Read(proposal_key + ".gap")
        if not alive(gap_doc):
            fail("UnknownAugurClaim: no live claim vertex for " + proposal_key + " (the CreateAugurReasoningClaim instanceOp must commit write-ahead of the reply)")
        gd = gap_doc.data
        if "entityId" not in gd:
            fail("InvalidState: claim .gap missing entityId for " + proposal_key)
        entity_key = gd["entityId"]
        _, entity_id = parts_of(entity_key, "claim.gap.entityId", "")

        status = required_string(p, "status")

        review_state = "pending"
        invalid_reason = ""
        action = ""
        params = {}
        rationale = ""
        confidence = 0.0
        model = ""
        prompt_hash = ""
        catalog_hash = ""
        reasoned_at = ""

        if status == "failed":
            # A modeled refusal (stop_reason "refusal") is a definitive verdict, NOT
            # a crash: store the proposal invalid (auditable, never dispatchable) and
            # ride the adapter's detail on the rationale for the reviewer.
            review_state = "invalid"
            invalid_reason = "model declined to propose (refusal)"
            rationale = optional_string_attr(p, "result")
        elif status == "completed":
            result_str = required_string(p, "result")
            proposal = json.decode(result_str)
            if type(proposal) != type({}):
                fail("InvalidArgument: result: reasoning result is not a JSON object")
            action = proposal_string(proposal, "action")
            params = proposal_dict(proposal, "params")
            rationale = proposal_string(proposal, "rationale")
            confidence = proposal_number(proposal, "confidence")
            model = proposal_string(proposal, "model")
            prompt_hash = proposal_string(proposal, "promptHash")
            catalog_hash = proposal_string(proposal, "catalogHash")
            reasoned_at = proposal_string(proposal, "reasonedAt")

            # --- §5 record-time deterministic validation (the safety core) ---
            # The proposal is ALWAYS stored (auditability); the verdict decides only
            # pending (dispatchable) vs invalid (never dispatchable). The scope check
            # compares the model's params against the TRUSTED entity_key from the claim.
            if action not in ALLOWED_ACTIONS:
                review_state = "invalid"
                invalid_reason = "action not in allowed escalation vocabulary (triggerLoom|assignTask|directOp): " + action
            elif confidence < 0.0 or confidence > 1.0:
                review_state = "invalid"
                invalid_reason = "confidence out of range [0,1]: " + str(confidence)
            else:
                offending = scope_escape(params, entity_key, entity_id)
                if offending != "":
                    review_state = "invalid"
                    invalid_reason = "scope escape: proposed param '" + offending + "' references an entity other than the escalated candidate " + entity_key
        else:
            fail("InvalidArgument: status: must be one of completed, failed; got " + status)

        # Write the model-derived aspects create-only — the once-only guarantee (a
        # redelivered reply conflicts on .review and the batch is rejected, atop the
        # bridge's deterministic reply requestId collapse). The .gap aspect + links
        # the instanceOp committed are left untouched (D5).
        mutations = [
            make_aspect(proposal_key, "proposed", "augur.proposed",
                        {"action": action, "params": params}),
            make_aspect(proposal_key, "rationale", "augur.rationale", {"text": rationale}),
            make_aspect(proposal_key, "confidence", "augur.confidence", {"score": confidence}),
            make_aspect(proposal_key, "provenance", "augur.provenance",
                        {"model": model, "promptHash": prompt_hash,
                         "catalogHash": catalog_hash, "reasonedAt": reasoned_at}),
            make_aspect(proposal_key, "review", "augur.review",
                        {"state": review_state, "invalidReason": invalid_reason,
                         "reviewedAt": "", "dispatchedAt": ""}),
        ]
        # Augur dispatches as a Weaver directOp (Option F) — there is NO Loom
        # instance to unpark, so no orchestration.externalTaskCompleted is emitted.
        # The proposal record IS the terminal effect; the bridge already correlated
        # its reply on instanceKey == <handle>. The proposalRecorded event is the
        # audit/observability signal (the review lens consumes it), emitted on every
        # verdict — pending, invalid, or refusal.
        events = [
            {"class": "augur.proposalRecorded",
             "data": {"proposalKey": proposal_key, "entityId": entity_key,
                      "action": action, "reviewState": review_state}},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": proposal_key}}

    fail("augurproposal DDL: unknown operationType: " + ot)
`
