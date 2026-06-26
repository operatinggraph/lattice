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

def make_aspect_upsert(vtx_key, local_name, cls, data):
    return {"op": "update", "key": vtx_key + "." + local_name,
            "document": {"class": cls, "isDeleted": False,
                         "vertexKey": vtx_key, "localName": local_name, "data": data}}

def make_aspect_upsert_occ(vtx_key, local_name, cls, data, expected_revision):
    # Like make_aspect_upsert but carries an explicit expectedRevision so the
    # commit applies an OCC condition (an update with no expectedRevision commits
    # UNCONDITIONED — step8_commit.go). The per-unit application-index
    # serialization point: two concurrent applications for one unit snapshot the
    # index at the same revision, both rewrite it, and the second commit
    # RevisionConflicts (fail closed, never a silent duplicate).
    m = make_aspect_upsert(vtx_key, local_name, cls, data)
    m["expectedRevision"] = expected_revision
    return m

def make_vtx_tombstone(key, cls):
    # Soft-delete a vertex (isDeleted=True). UNCONDITIONED — a concurrent withdraw
    # tombstones to the same state (idempotent), and nothing else writes the
    # leaseapp ROOT (SignLease writes the .signature aspect, a different key). The
    # convergence lens anchors on the leaseapp and filters isDeleted, so the row
    # deletes (EmptyBehavior). Root data stays {} (D5).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True, "data": {}}}

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

def optional_string(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or type(v) != type(""):
        return None
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def require_number(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        fail("InvalidArgument: " + name + ": required number")
    return v

def optional_number(p, name):
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None or (type(v) != type(0) and type(v) != type(0.0)):
        return None
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

        # The application MUST name the unit it applies to (§7 Q2): a unit-less
        # application can never exist, so there is no unactuatable missing_unit
        # gap to wedge Weaver — the convergence lens reads the unit's listing /
        # address as informational columns ("what am I applying to lease"). The
        # unit is a location-domain vtx.unit.<NanoID>, alive-checked here (so the
        # caller must list it in ContextHint.Reads).
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        if not vertex_alive(state, unit):
            fail("UnknownUnit: " + unit)

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

        # appliesToUnit: the leaseapp (later-arriving) is the source, the
        # pre-existing unit is the target (Contract #1 §1.1). Reads as
        # "this application applies to this unit." The convergence lens walks it.
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id

        # Per-unit live-application guard (Capability-KV §06 — the operation's own
        # Starlark logic; no platform scan, no frozen contract). The unit carries a
        # .leaseApplications index aspect listing its live applications as
        # {leaseApp, applicant} entries. Read it ON DEMAND (kv.Read, §2.5) — NOT a
        # declared contextHint.reads key: the unit is a location-domain vertex and
        # the index does not exist until the FIRST application, so a declared read
        # would HydrationMiss on a never-applied unit. Absent → unconstrained (the
        # index is created on the first application). For each existing entry,
        # kv.Read the application: a tombstoned / absent leaseapp is pruned (does
        # not block); a still-live application by the SAME applicant is a
        # DuplicateApplication — a unit accepts many DIFFERENT applicants (normal
        # leasing: the landlord chooses) but not the same applicant twice. The index
        # is rewritten OCC-guarded on its read revision (present) or CreateOnly
        # (absent), so two concurrent applications for one unit fail closed (the
        # second conflicts), never a silent duplicate.
        idx_key = unit + ".leaseApplications"
        idx = kv.Read(idx_key)
        idx_present = idx != None and not idx.isDeleted
        kept = []
        if idx_present:
            apps_val = idx.data.get("applications")
            if apps_val != None and type(apps_val) == type([]):
                for entry in apps_val:
                    if type(entry) != type({}):
                        continue
                    cand_key = entry.get("leaseApp")
                    cand_applicant = entry.get("applicant")
                    if cand_key == None:
                        continue
                    cand = kv.Read(cand_key)
                    if cand == None or cand.isDeleted:
                        continue
                    # Still live: keep it in the rebuilt index, and block a repeat
                    # by the same applicant.
                    kept.append({"leaseApp": cand_key, "applicant": cand_applicant})
                    if cand_applicant == applicant:
                        fail("DuplicateApplication: applicant " + applicant + " already has a live application " + cand_key + " for unit " + unit)
        kept.append({"leaseApp": app_key, "applicant": applicant})
        if idx_present:
            index_mut = make_aspect_upsert_occ(unit, "leaseApplications", "unitLeaseApplications", {"applications": kept}, idx.revision)
        else:
            index_mut = make_aspect(unit, "leaseApplications", "unitLeaseApplications", {"applications": kept})

        # Root data minimal (D5): {} on root. The applicant + unit are links; the
        # status/gaps are lens-computed, never stored.
        mutations = [
            make_vtx(app_key, "leaseapp", {}),
            make_link(app_for_lnk, app_key, applicant, "applicationFor", "applicationFor", {}),
            make_link(applies_to_lnk, app_key, unit, "appliesToUnit", "appliesToUnit", {}),
            # The rebuilt per-unit application index (pruned + appended), OCC-guarded
            # on the snapshot revision — see the duplicate-application guard above.
            index_mut,
        ]

        # .terms (D3): the applicant's requested lease terms — additive
        # application detail for the applicant FE / operator (the convergence lens
        # does NOT read it). Written only when moveInDate is supplied, so a bare
        # applicant+unit application stays valid; moveInDate present ⇒
        # leaseTermMonths required (a half-specified terms block is rejected);
        # requestedRent is optional.
        move_in = optional_string(p, "moveInDate")
        if move_in != None:
            terms_data = {"moveInDate": move_in, "leaseTermMonths": require_number(p, "leaseTermMonths")}
            req_rent = optional_number(p, "requestedRent")
            if req_rent != None:
                terms_data["requestedRent"] = req_rent
            mutations.append(make_aspect(app_key, "terms", "terms", terms_data))

        events = [{"class": "leaseapp.applicationCreated",
                   "data": {"leaseAppKey": app_key, "applicant": applicant, "unit": unit}}]
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

    if ot == "WithdrawLeaseApplication":
        # Withdraw / cancel an application: soft-delete the leaseapp so it drops
        # from My Applications (the convergence lens anchors on it + filters
        # isDeleted → EmptyBehavior delete), and prune its entry from the unit's
        # live-application index so it stops counting against the per-applicant
        # duplicate guard. The complement to CreateLeaseApplication's guard — an
        # applicant who applied to the wrong unit can back out + re-apply.
        app_key = required_string(p, "leaseAppKey")
        _, app_id = parts_of(app_key, "leaseAppKey", "leaseapp")
        if not vertex_alive(state, app_key):
            fail("UnknownLeaseApplication: " + app_key)

        # The unit the application applies to (the FE carries it on the row). Verify
        # it is genuinely THIS application's unit via the deterministic appliesToUnit
        # link (kv.Read) — mirroring clinic's withProvider check — so a wrong /
        # fabricated unit can't be used to prune a different unit's index.
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")
        applies_to_lnk = "lnk.leaseapp." + app_id + ".appliesToUnit.unit." + unit_id
        link = kv.Read(applies_to_lnk)
        if link == None or link.isDeleted:
            fail("UnitMismatch: " + unit + " is not the unit application " + app_key + " applies to")

        # Tombstone the application. The applicationFor / appliesToUnit links are
        # left in place (non-cascading tombstone, the clinic-domain precedent) — they
        # dangle off a tombstoned anchor every reader filters.
        mutations = [make_vtx_tombstone(app_key, "leaseapp")]

        # Prune this application's entry from the unit's index. The duplicate guard
        # ALSO self-prunes tombstoned entries on the next apply, but pruning here
        # keeps the index clean immediately (it also feeds a future by-unit landlord
        # lens). OCC-guarded on the snapshot revision so a concurrent
        # CreateLeaseApplication for the same unit fails closed. Absent index →
        # nothing to prune.
        idx_key = unit + ".leaseApplications"
        idx = kv.Read(idx_key)
        if idx != None and not idx.isDeleted:
            kept = []
            apps_val = idx.data.get("applications")
            if apps_val != None and type(apps_val) == type([]):
                for entry in apps_val:
                    if type(entry) != type({}):
                        continue
                    cand_key = entry.get("leaseApp")
                    if cand_key == None or cand_key == app_key:
                        continue
                    kept.append({"leaseApp": cand_key, "applicant": entry.get("applicant")})
            mutations.append(make_aspect_upsert_occ(unit, "leaseApplications", "unitLeaseApplications", {"applications": kept}, idx.revision))

        events = [{"class": "leaseapp.applicationWithdrawn",
                   "data": {"leaseAppKey": app_key, "unit": unit}}]
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
        # idempotencyKey by construction). dispatchOp is the package-local op the
        # bridge posts if its adapter returns Pending (it records the .dispatch
        # marker); it is the matched pair of replyOp, which the bridge posts on a
        # terminal outcome.
        event_data = {
            "instanceKey":    handle,
            "adapter":        adapter,
            "replyOp":        reply_op,
            "dispatchOp":     "RecordServiceDispatch",
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
// The bridge posts {externalRef, status, result}; this op reconstructs the claim
// vertex key, takes the adapter's terminal status (completed | failed), derives
// completedAt + validUntil, writes the .outcome aspect, and emits
// orchestration.externalTaskCompleted{externalRef} — the completion signal Loom
// correlates on. Without that event the externalTask never completes.
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

# The terminal outcome values RecordLeaseServiceOutcome admits (mirrors
# service-domain). completed = the external call succeeded with a satisfying
# result; failed = a definitive business rejection (a declined charge, a failed
# background check). The bridge supplies it verbatim from the adapter's
# Result.Status — it is required, with no default.
OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

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

        # The terminal status is the adapter's verdict, supplied verbatim by the
        # bridge (completed | failed) and required — an adapter error is
        # Nak+retry (never a reply), so every reply carries a definitive business
        # outcome. completedAt is the op's own timestamp (the bridge supplies
        # none), normalized to canonical UTC for a sound lexical compare.
        status = required_status(p)
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

// leaseServiceDispatchDDLScript is the externalTask dispatchOp the bridge submits
// when its adapter returns Pending (the external call was submitted but has not
// resolved yet). The bridge posts {externalRef, vendorRef, adapter, replyOp,
// nextPollAt, deadline}; this op reconstructs the claim vertex key from the bare
// handle and writes a create-only .dispatch aspect
// {vendorRef, adapter, replyOp, submittedAt, nextPollAt, deadline} on it — the
// pending marker. The bridge's poll/timeout schedules carry the routing (adapter /
// replyOp / vendorRef) on their payload, so the fired handler reads it from there —
// NOT from this marker; the marker records the same routing for the lens / Weaver
// read-model (pending-suppression, a later increment). It does NOT write the create-only .outcome
// aspect and does NOT emit orchestration.externalTaskCompleted: the externalTask
// is NOT done, so Loom's token stays parked. The .dispatch and .outcome aspects
// are deliberately separate (.outcome is the FR58 once-only terminal guard;
// "pending" is a distinct state the lens/Weaver can read without colliding with
// it).
//
// Like the replyOp the bridge submits this with no ContextHint.Reads, so the op
// reads NOTHING from state: the reconstructed vtx.service.<handle> vertex is
// unhydrated on the live path. The once-only guarantee is the create-only
// .dispatch write itself — a redelivered Pending conflicts on the existing
// .dispatch key and the batch is rejected (atop the bridge's deterministic
// deriveDispatchRequestID, which already collapses most redeliveries at the
// Contract #4 tracker). submittedAt is the op's own timestamp, normalized to
// canonical UTC; nextPollAt and deadline are the bridge-supplied schedule
// instants, normalized to canonical UTC for a sound lexical compare (no clock
// read — read-free, deterministic).
const leaseServiceDispatchDDLScript = `
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

    if ot == "RecordServiceDispatch":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle (the matched-pair
        # type the instanceOp chose). The bare-handle validation needs no state read.
        inst_key = "vtx.service." + handle

        # The vendor's opaque pending reference (the poll/webhook key the bridge
        # got back from the adapter). Required — a Pending with no ref is meaningless.
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
        # requestId collapse). NO .outcome is written and NO
        # orchestration.externalTaskCompleted is emitted — the task is not done,
        # the token stays parked. The instance root, already {}, is untouched (D5).
        mutations = [
            make_aspect(inst_key, "dispatch", "dispatch",
                        {"vendorRef": vendor_ref, "adapter": adapter, "replyOp": reply_op,
                         "submittedAt": submitted_at, "nextPollAt": next_poll_at, "deadline": deadline}),
        ]

        # A provenance event marks the submit for the audit join (NOT a completion
        # signal — Loom must NOT close the token on a dispatch).
        events = [
            {"class": "service.dispatchRecorded",
             "data": {"serviceKey": inst_key, "vendorRef": vendor_ref, "submittedAt": submitted_at}},
        ]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseServiceDispatch DDL: unknown operationType: " + ot)
`
