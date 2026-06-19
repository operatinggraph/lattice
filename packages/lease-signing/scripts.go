package leasesigning

import "fmt"

// leaseAppDDLScript handles the leaseapp lifecycle ops CreateLeaseApplication
// and SignLease. Known-key reads only (validates every link/aspect endpoint by
// the keys the caller lists in ContextHint.Reads). Root data stays {} on every
// op (D5): the applicant is a link, the signature is an aspect.
const leaseAppDDLScript = `
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

def bare_nanoid_or_mint(p, name):
    if not hasattr(p, name):
        return nanoid.new()
    v = getattr(p, name)
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": must be a non-empty id string")
    v = v.strip()
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
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLeaseApplication":
        applicant = required_string(p, "applicant")
        _, applicant_id = parts_of(applicant, "applicant", "identity")

        # No-orphan invariant (FR29 / P4): the applicant identity MUST be alive.
        # An application pointing at a non-existent applicant is never committed.
        if not vertex_alive(state, applicant):
            fail("UnknownApplicant: " + applicant)

        # leaseAppId is a caller-supplied write-ahead seam (mirrors
        # service-domain's instanceId). Absent → mint internally. CreateOnly
        # semantics make a crash-retry with the same id collapse on the
        # Contract #4 tracker.
        app_id = bare_nanoid_or_mint(p, "leaseAppId")
        app_key = "vtx.leaseapp." + app_id

        # applicationFor: the leaseapp (later-arriving) is the source, the
        # pre-existing identity is the target (Contract #1 §1.1). Reads as
        # "this application is for this applicant."
        app_for_lnk = "lnk.leaseapp." + app_id + ".applicationFor.identity." + applicant_id

        # Root data minimal (D5): {} on root. The applicant is a link; the
        # status/gaps are lens-computed, never stored.
        mutations = [
            make_vtx(app_key, "leaseapp", {}),
            make_link(app_for_lnk, app_key, applicant, "applicationFor", "applicationFor", {}),
        ]
        events = [{"class": "leaseapp.applicationCreated",
                   "data": {"leaseAppKey": app_key, "applicant": applicant}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    if ot == "SignLease":
        app_key = required_string(p, "leaseAppKey")
        parts_of(app_key, "leaseAppKey", "leaseapp")

        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # Sign once: the .signature aspect is written CreateOnly, so a second
        # SignLease with a different requestId conflicts and is rejected. When
        # the caller lists the now-existing .signature key in ContextHint.Reads,
        # the state is hydrated and this explicit check fires first, upgrading
        # the rejection to a structured AlreadySigned ScriptError.
        sig_key = app_key + ".signature"
        if vertex_alive(state, sig_key):
            fail("AlreadySigned: " + app_key)

        # The signature is a fact in an aspect (D5); the application root stays
        # {}. signedAt is the op's own timestamp, normalized to canonical UTC so
        # a downstream lexical compare is sound.
        signed_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_aspect(app_key, "signature", "signature", {"signedAt": signed_at}),
        ]
        events = [{"class": "leaseapp.leaseSigned",
                   "data": {"leaseAppKey": app_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": app_key}}

    fail("leaseapp DDL: unknown operationType: " + ot)
`

// leaseServiceInstanceDDLScript is the externalTask instanceOp. It mints the
// claim vertex vtx.service.<handle> (the same shape 14.1's service instance
// uses, reusing its .outcome aspect shape downstream), records the family
// discriminator + the providedTo link, and emits the external.<adapter> event
// off its own transactional outbox. Template-less (no instanceOf): the lens
// hops providedTo, not instanceOf.
const leaseServiceInstanceDDLScript = `
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

def required_bare_handle(p, name):
    # The bare instance handle Loom minted: type-free, must carry no key
    # delimiters so "vtx.service." + handle is a single well-formed vertex key.
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
    if want_type != "" and parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[1], parts[2]

def vertex_alive(state, key):
    if key not in state:
        return False
    doc = state[key]
    if doc == None:
        return False
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

SERVICE_FAMILIES = ["backgroundCheck", "payment"]

def family_of(p):
    # The family is opaque pass-through from the Loom step's params.family.
    # A nested payload object is exposed to Starlark as a dict (not a struct),
    # so it is read by key, not by attribute.
    if not hasattr(p, "params") or p.params == None:
        fail("InvalidArgument: params.family: required (backgroundCheck|payment)")
    params = p.params
    if type(params) != type({}) or "family" not in params:
        fail("InvalidArgument: params.family: required (backgroundCheck|payment)")
    fam = params["family"]
    if fam == None or type(fam) != type("") or len(fam.strip()) == 0:
        fail("InvalidArgument: params.family: required non-empty string")
    fam = fam.strip()
    if fam not in SERVICE_FAMILIES:
        fail("InvalidArgument: params.family: must be one of backgroundCheck, payment; got " + fam)
    return fam

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLeaseServiceInstance":
        handle = required_bare_handle(p, "instanceKey")
        subject_key = required_string(p, "subjectKey")
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")
        fam = family_of(p)
        _, subject_id = parts_of(subject_key, "subjectKey", "identity")

        # No-orphan invariant (FR29 / P4): the applicant identity MUST be alive.
        if not vertex_alive(state, subject_key):
            fail("UnknownApplicant: " + subject_key)

        # Prepend the package-chosen claim-vertex type. The engine never names a
        # type; the replyOp re-prepends the SAME type — a matched pair.
        inst_key = "vtx.service." + handle
        class_value_str = "service." + fam + ".instance"

        # providedTo: the service instance (later-arriving) is the source, the
        # pre-existing identity is the target (Contract #1 §1.1). This is the
        # convergence link the lens fans out across to read the outcome aspect.
        provided_to_lnk = "lnk.service." + handle + ".providedTo.identity." + subject_id

        # Root data minimal (D5): {} on root. The vertex KEY type is 'service'
        # (vtx.service.<handle>) so the lens anchors via the key segment and reads
        # the 14.1 .outcome aspect shape, but the vertex CLASS is the
        # package-owned 'leaseServiceInstance' — NOT 'service', whose DDL
        # (service-domain) restricts its class to that DDL's permittedCommands.
        # The .class aspect still mirrors 14.1's instance discriminator
        # (service.<family>.instance); the .family aspect is the lens's
        # bgcheck/payment discriminator (read as a distinct aspect because the
        # vertex envelope 'class' field shadows the .class aspect on the
        # projection read path). NO outcome aspect yet — absence =
        # not-yet-complete. Template-less.
        mutations = [
            make_vtx(inst_key, "leaseServiceInstance", {}),
            make_aspect(inst_key, "class", "class", {"value": class_value_str}),
            make_aspect(inst_key, "family", "family", {"value": fam}),
            make_link(provided_to_lnk, inst_key, subject_key, "providedTo", "providedTo", {}),
        ]

        # Emit the external.<adapter> event off this op's transactional outbox.
        # The body shape matches the bridge's externalEvent reader: the bare
        # handle is the opaque correlation token (instanceKey == externalRef ==
        # idempotencyKey by construction).
        event_data = {
            "instanceKey":    handle,
            "adapter":        adapter,
            "replyOp":        reply_op,
            "externalRef":    handle,
            "idempotencyKey": handle,
            "params":         {"family": fam},
        }
        events = [{"class": "external." + adapter, "data": event_data}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceInstance DDL: unknown operationType: " + ot)
`

// leaseServiceReplyDDLScript is the externalTask replyOp the bridge submits.
// The bridge posts only {externalRef, result}; this op reconstructs the claim
// vertex key, derives status + completedAt + validUntil, writes the .outcome
// aspect, and emits orchestration.externalTaskCompleted{externalRef} — the
// completion signal Loom correlates on. Without that event the externalTask
// never completes.
//
// The bridge submits this op with no ContextHint.Reads (internal/bridge's
// actuator builds an envelope with no Reads field), so the op reads NOTHING
// from state: the reconstructed vtx.service.<handle> vertex, its .class aspect,
// and its root revision are all unhydrated on the live path. The once-only
// guarantee is therefore the create-only .outcome write itself — a redelivered
// reply conflicts on the existing .outcome key and the batch is rejected (the
// bridge's deterministic deriveReplyRequestID already collapses most
// redeliveries at the Contract #4 tracker). The instance root, already minted
// {data:{}} by the instanceOp, is left untouched (D5).
//
// validUntil is pure arithmetic on the op's own completedAt
// (time.rfc3339_add), so the op stays read-free.
var leaseServiceReplyDDLScript = fmt.Sprintf(`
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

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    return v

def required_bare_handle(p, name):
    v = required_string(p, name)
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: " + name + ": must carry no dots / key segments, wildcards, or whitespace; got " + v)
    return v

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordLeaseServiceOutcome":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle: the instanceOp
        # chose 'service' as the type, so the replyOp re-prepends the same type.
        # The bare-handle format validation needs no state read.
        inst_key = "vtx.service." + handle

        # The bridge supplies only a free-form result string. It is NOT written
        # to the projection-plane .outcome aspect (it can carry PII / payment
        # data in production and the lens reads only status / completedAt /
        # validUntil); it rides the service.outcomeRecorded provenance event body
        # instead.
        result = optional_string(p, "result")

        # Derive status + completedAt. The bridge only posts a reply on adapter
        # success (an adapter error is Nak+retry, never a reply), so status is
        # completed on every reply; a failed outcome has no Phase-2 producer on
        # the bridge path (the deliberate demo simplification — the Phase-3
        # plug-in point). completedAt is the op's own timestamp (the bridge
        # supplies none), normalized to canonical UTC for a sound lexical compare.
        status = "completed"
        completed_at = time.rfc3339_utc(op.submittedAt)

        # Stamp validUntil = completedAt + the freshness window. This op is
        # read-free and cannot tell bgcheck from payment, so it stamps validUntil
        # on EVERY outcome (family-agnostic). The lens applies the freshness
        # policy to bgcheck only — it counts a completed bgcheck toward
        # convergence solely while validUntil > $now, re-opening the gap once it
        # lapses; payment ignores validUntil (ever-completed). So validUntil on a
        # payment outcome is harmless and unused: the freshness rule lives in the
        # cypher (Contract #10 §10.2). The add is pure arithmetic on completed_at
        # — no clock read, so the op stays read-free and deterministic.
        valid_until = time.rfc3339_add(completed_at, %q)

        # Write the .outcome aspect {status, completedAt, validUntil} as a
        # create-only mutation. This create-only IS the once-only guarantee: a
        # redelivered reply conflicts on the existing key and the batch is
        # rejected (FR58 at the DDL layer, atop the bridge's deterministic
        # requestId collapse). The instance root, already {data:{}}, is not
        # touched (D5).
        mutations = [
            make_aspect(inst_key, "outcome", "outcome", {"status": status, "completedAt": completed_at, "validUntil": valid_until}),
        ]

        # Emit the completion signal Loom correlates on (the BARE handle as
        # externalRef — Loom parks on token.<handle>) PLUS a provenance event
        # (which carries the free-form result, kept off the aspect). The
        # completion event is load-bearing: without it the externalTask never
        # completes (the creation-deadline disarmed on instanceOp commit; the
        # bridge reply carried no completion signal).
        provenance = {"serviceKey": inst_key, "status": status, "completedAt": completed_at, "validUntil": valid_until}
        if result != None:
            provenance["result"] = result
        events = [
            {"class": "orchestration.externalTaskCompleted",
             "data": {"externalRef": handle}},
            {"class": "service.outcomeRecorded",
             "data": provenance},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceReply DDL: unknown operationType: " + ot)
`, bgcheckFreshnessWindow)
