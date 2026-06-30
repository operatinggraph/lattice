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
		PermittedCommands: []string{"CreateAugurReasoningClaim", "RecordProposal", "ReviewProposal"},
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
			"reply via the create-only .review aspect atop the bridge's deterministic reply requestId. " +
			"ReviewProposal is the human verdict op (payload {externalRef, verdict ∈ approve|reject}): an operator " +
			"flips a pending proposal to approved | rejected — the reviewer is the trusted submitting actor (op.actor) " +
			"and the stamp is op.submittedAt; approve re-runs the §5 boundary against the stored proposal and " +
			"fail-closes to invalid if it no longer validates. Only a pending proposal is reviewable.",
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
			"verdict":     "ReviewProposal only — the operator's verdict on a pending proposal: 'approve' (re-validated against the §5 boundary, fail-closing to invalid if it no longer validates) or 'reject'. The reviewer is the trusted submitting actor (op.actor) and the stamp is the envelope submit time; neither is a payload field.",
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
			{
				Name: "ReviewProposal — a human operator approves a pending proposal (the verdict op)",
				Payload: map[string]any{
					"externalRef": "augurEpisodeHJKMNPQRST",
					"verdict":     "approve",
				},
				ExpectedOutcome: "The proposal must be review.state=pending (only a pending proposal is reviewable; any other " +
					"state rejects InvalidReviewTransition). The reviewer is the TRUSTED submitting actor (op.actor, " +
					"capability-authorized — never a payload field) and the stamp is op.submittedAt (the sandbox has no clock; a " +
					"replay re-derives the identical timestamp). On approve the §5 record-time boundary is re-run against the " +
					"STORED proposal (action vocabulary, confidence, scope vs the trusted claim entity) — fail-closing to invalid " +
					"if it no longer validates. The .review aspect flips pending → approved (OCC-guarded on its revision), a " +
					"reviewedBy link to the actor is created, and augur.proposalReviewed is emitted. verdict=reject flips to " +
					"rejected with no re-validation.",
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
    # The bare instance handle Weaver mints for the escalation episode (Option F —
    # a directOp, no Loom): type-free, must carry no key delimiters so
    # "vtx.augurproposal." + handle is a single well-formed key.
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

def is_vtx_key(v):
    # True if v is a string shaped vtx.<type>.<id> (a vertex key a proposal would
    # ACT ON). Operation-type names, timestamps, booleans, and free text are not
    # vertex keys and are not scope-checked.
    if type(v) != type(""):
        return False
    parts = v.split(".")
    return len(parts) >= 3 and parts[0] == "vtx" and parts[1] != "" and parts[2] != ""

# collect_param_strings flattens a proposal's params to the string values it
# carries, WITHOUT recursion (this Starlark dialect forbids recursion / while) —
# one pass over the top level plus the immediate children of any nested dict/list.
# A model action's params are a flat string->string map (assignTask / directOp /
# triggerLoom); a value nested deeper than that cannot be exhaustively scope-checked
# in a non-recursive pass, so it sets too_deep and the proposal is conservatively
# stored invalid (a false-positive over-reject is the contained failure — design
# §5 — vs. a false-negative that would let a foreign reference through). Returns
# (strings, too_deep).
def collect_param_strings(params):
    out = []
    too_deep = False
    for k in params:
        v = params[k]
        t = type(v)
        if t == type(""):
            out.append(v)
        elif t == type({}):
            for k2 in v:
                v2 = v[k2]
                if type(v2) == type(""):
                    out.append(v2)
                elif type(v2) == type({}) or type(v2) == type([]):
                    too_deep = True
        elif t == type([]):
            for item in v:
                if type(item) == type(""):
                    out.append(item)
                elif type(item) == type({}) or type(item) == type([]):
                    too_deep = True
    return out, too_deep

# scope_verdict enforces the §5 scope containment as DEFAULT-DENY: EVERY
# vtx-shaped value the proposal carries — under ANY param name, not a fixed
# allow-list of names — MUST equal the TRUSTED escalated candidate, and the
# proposal MUST reference that candidate at least once (a scope-less action has no
# bounded target and cannot be made dispatchable). The candidate identity comes
# from the claim vertex, never the model's reply. Returns (ok, reason): ok True =>
# in scope; ok False => reason is the invalid explanation. Default-deny is what
# makes the check sound against future ops introducing new entity params — an
# unrecognized entity-naming param can no longer smuggle a foreign vertex through.
def scope_verdict(params, entity_key, entity_id):
    strings, too_deep = collect_param_strings(params)
    if too_deep:
        return False, "proposal params nested deeper than the flat action-param model can scope-check"
    foreign = ""
    references = False
    for s in strings:
        sv = s.strip()
        if sv == entity_key or sv == entity_id:
            references = True
        elif is_vtx_key(sv):
            foreign = sv
            break
    if foreign != "":
        return False, "scope escape: param value '" + foreign + "' references an entity other than the escalated candidate " + entity_key
    if not references:
        return False, "proposal does not scope to the escalated candidate " + entity_key + " (no param references it)"
    return True, ""

# revalidate_for_approval re-runs the §5 record-time deterministic boundary
# against the STORED proposal at approval time (design §3.2: "re-runs the
# deterministic validator on approve"). It re-reads the proposal's .proposed /
# .confidence aspects + the TRUSTED claim .gap (entity identity comes from HERE,
# never any reply) and re-checks the DDL-determinable half — action vocabulary,
# confidence range, scope containment. (The live-catalog drift re-check is the
# dispatch-time leg, Weaver-side; this leg defends against a proposal that no
# longer validates against the static boundary.) Known-key reads only. Returns
# (ok, reason); ok False => the approval fail-closes to invalid.
def revalidate_for_approval(proposal_key):
    proposed_doc = kv.Read(proposal_key + ".proposed")
    if not alive(proposed_doc) or proposed_doc.data == None:
        return False, "proposal has no recorded .proposed aspect"
    pdata = proposed_doc.data
    action = ""
    if "action" in pdata and type(pdata["action"]) == type(""):
        action = pdata["action"]
    params = {}
    if "params" in pdata and type(pdata["params"]) == type({}):
        params = pdata["params"]
    if action not in ALLOWED_ACTIONS:
        return False, "action not in allowed escalation vocabulary (triggerLoom|assignTask|directOp): " + action

    conf_doc = kv.Read(proposal_key + ".confidence")
    score = -1.0
    if alive(conf_doc) and conf_doc.data != None and "score" in conf_doc.data:
        sv = conf_doc.data["score"]
        if type(sv) == type(0) or type(sv) == type(0.0):
            score = sv
    if score < 0.0 or score > 1.0:
        return False, "confidence out of range [0,1]: " + str(score)

    gap_doc = kv.Read(proposal_key + ".gap")
    if not alive(gap_doc) or gap_doc.data == None or "entityId" not in gap_doc.data:
        return False, "claim .gap missing entityId"
    entity_key = gap_doc.data["entityId"]
    _, entity_id = parts_of(entity_key, "claim.gap.entityId", "")
    return scope_verdict(params, entity_key, entity_id)

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
            # Decode with a None default (never raise): an empty / non-JSON /
            # non-object result on a completed reply is a definitive verdict (an
            # adapter wiring fault or a malformed model output), NOT a crash. Store
            # it invalid (auditable, never dispatchable) so the episode ALWAYS lands
            # a .review — never fail() the op here: the bridge has already Ack'd the
            # external event, so a reject would wedge the episode with no record.
            # Mirrors the refusal branch's store-invalid posture.
            result_str = optional_string_attr(p, "result")
            proposal = None
            if len(result_str.strip()) > 0:
                proposal = json.decode(result_str, None)
            if type(proposal) != type({}):
                review_state = "invalid"
                invalid_reason = "completed reply carried no decodable JSON-object reasoning result"
                rationale = result_str
            else:
                action = proposal_string(proposal, "action")
                params = proposal_dict(proposal, "params")
                rationale = proposal_string(proposal, "rationale")
                confidence = proposal_number(proposal, "confidence")
                model = proposal_string(proposal, "model")
                prompt_hash = proposal_string(proposal, "promptHash")
                catalog_hash = proposal_string(proposal, "catalogHash")
                reasoned_at = proposal_string(proposal, "reasonedAt")

                # --- §5 record-time deterministic validation (the safety core) ---
                # The proposal is ALWAYS stored (auditability); the verdict decides
                # only pending (dispatchable) vs invalid (never dispatchable). The
                # scope check is DEFAULT-DENY against the TRUSTED entity_key from the
                # claim — never the model's reply.
                if action not in ALLOWED_ACTIONS:
                    review_state = "invalid"
                    invalid_reason = "action not in allowed escalation vocabulary (triggerLoom|assignTask|directOp): " + action
                elif confidence < 0.0 or confidence > 1.0:
                    review_state = "invalid"
                    invalid_reason = "confidence out of range [0,1]: " + str(confidence)
                else:
                    in_scope, scope_reason = scope_verdict(params, entity_key, entity_id)
                    if not in_scope:
                        review_state = "invalid"
                        invalid_reason = scope_reason
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

    if ot == "ReviewProposal":
        # The human verdict (design §3.2): an operator flips a PENDING proposal to
        # approved | rejected. The reviewer is the TRUSTED submitting actor
        # (op.actor — capability-authorized, never a payload field — the same
        # don't-trust-the-payload-for-identity discipline as RecordProposal's entity
        # split); the stamp is the envelope's authoritative submit time
        # (op.submittedAt; the sandbox has no clock, so a replay re-derives the
        # identical timestamp). Only a pending proposal is reviewable — any other
        # state rejects (terminal-state guard: an invalid / already-reviewed /
        # dispatched proposal cannot be re-reviewed; a redelivered op is collapsed
        # earlier by the Contract #4 requestId tracker).
        handle = required_bare_handle(p, "externalRef")
        proposal_key = "vtx.augurproposal." + handle
        verdict = required_string(p, "verdict")
        if verdict != "approve" and verdict != "reject":
            fail("InvalidArgument: verdict: must be one of approve, reject; got " + verdict)

        review_doc = kv.Read(proposal_key + ".review")
        if not alive(review_doc):
            fail("UnknownAugurProposal: no recorded proposal for " + proposal_key + " (RecordProposal must commit a verdict before review)")
        rd = review_doc.data
        cur_state = ""
        if rd != None and "state" in rd:
            cur_state = rd["state"]
        if cur_state != "pending":
            fail("InvalidReviewTransition: proposal " + proposal_key + " is '" + cur_state + "', only a pending proposal is reviewable")

        reviewer = op.actor
        if not is_vtx_key(reviewer):
            fail("InvalidState: op.actor is not a vertex key: " + reviewer)
        reviewer_type, reviewer_id = parts_of(reviewer, "actor", "")
        reviewed_at = op.submittedAt

        new_state = "approved"
        invalid_reason = ""
        if verdict == "reject":
            # A reject is always permitted — no re-validation; the operator declines
            # the proposal regardless of whether it would still dispatch.
            new_state = "rejected"
        else:
            # Re-run the §5 boundary against the STORED proposal (design §3.2). A
            # re-validation failure fail-closes to invalid: the operator reviewed,
            # but the verdict is invalid, never approved / dispatchable.
            in_scope, reason = revalidate_for_approval(proposal_key)
            if not in_scope:
                new_state = "invalid"
                invalid_reason = "re-validation at approval failed: " + reason

        # Flip the .review aspect (unconditioned update, preserving the aspect's
        # full shape — D5; the reply leg carries no ContextHint.Reads, so the
        # single-review guarantee rides the create-only reviewedBy link + the
        # pending-only guard above, not a step-8 revision CAS). The reviewedBy link
        # records WHO reviewed (create-only — a redelivery conflicts on it; a second
        # genuine review by the same reviewer is already blocked by the pending-only
        # guard once the first review lands).
        reviewedby_lnk = "lnk.augurproposal." + handle + ".reviewedBy." + reviewer_type + "." + reviewer_id
        mutations = [
            {"op": "update", "key": proposal_key + ".review",
             "document": {"class": "augur.review", "isDeleted": False,
                          "vertexKey": proposal_key, "localName": "review",
                          "data": {"state": new_state, "invalidReason": invalid_reason,
                                   "reviewedAt": reviewed_at, "dispatchedAt": ""}}},
            make_link(reviewedby_lnk, proposal_key, reviewer, "reviewedBy", "reviewedBy",
                      {"reviewedAt": reviewed_at, "verdict": verdict}),
        ]
        events = [
            {"class": "augur.proposalReviewed",
             "data": {"proposalKey": proposal_key, "reviewState": new_state,
                      "reviewer": reviewer, "verdict": verdict}},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": proposal_key}}

    fail("augurproposal DDL: unknown operationType: " + ot)
`
