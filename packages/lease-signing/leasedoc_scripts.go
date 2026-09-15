package leasesigning

import (
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// leaseDocInstanceDDLScript is the docGen externalTask instanceOp. It mirrors
// leaseServiceInstanceDDLScript's claim-minting mechanics (vtx.service.<handle>,
// envelope class + instanceOf type authority, providedTo convergence link,
// external.<adapter> event off the op's transactional outbox) with two
// deliberate differences:
//
//   - the subject is the LEASEAPP (the document is about the application), and
//     it must be SIGNED: the .signature aspect is read on demand (kv.Read §2.5 —
//     the pattern fires on signing, but a raced or manual trigger against an
//     unsigned application must fail with no claim and no dispatch).
//   - the emitted params carry the RESOLVED document fields (§10.5's
//     linked-vertex read: kv.Links walks the applicationFor / appliesToUnit /
//     manages links; kv.Read loads the unit's .address/.listing and the
//     application's own .terms), so the vendor receives real field values and
//     never touches the graph or a lens. Absent optional fields are omitted
//     from doc{}; the vendor's renderer degrades exactly as the display path
//     does (an unnamed applicant renders by bare key, an unmanaged unit names
//     no landlord). The landlord's .name is deliberately NOT read here — only
//     the landlord's bare key resolves: a link-discovered sensitive aspect
//     has no contextHint.egressReads declaration path (Loom cannot
//     pre-declare a key this DDL only resolves at execute time via
//     live_link_target / live_link_source), so a plaintext read here would
//     be structurally rejected as sensitive-plaintext-into-external-event.
//
// The TENANT's name is different: SignLease snapshots it onto the SUBJECT
// itself (the leaseapp's own SENSITIVE .tenantName aspect, custodied on the
// executedLeaseRecord retention class — scripts.go's SignLease branch), so
// it IS a subject-own aspect Loom's inferExternalTaskReads can pre-declare
// under contextHint.egressReads, exactly like backgroundCheck's subject.name/
// subject.dob templates. The leaseDocument pattern's Params therefore
// templates "tenantName": "subject.tenantName.data.value" at the TOP LEVEL
// (never into doc{} — the unwrap substitutes markers at the top level of
// params only, and the docGen adapter reads tenantName from the unwrapped
// map, retention-class-egress-envelope-design.md §3.5(a)); this DDL resolves
// it via orchestration-base's resolve_subject_params, dropping the template
// first when the application carries no .tenantName snapshot (absent —
// tolerated by the descriptor floor — rather than a failed dispatch).
const leaseDocInstanceDDLScript = `
` + orchestrationbase.ResolveSubjectParamsHelper + `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

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
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx.<type>.<NanoID>; got " + key)
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

def family_of(p):
    # params.family is opaque pass-through from the Loom step; this DDL owns
    # exactly one family (docGen), so anything else is rejected. A nested
    # payload object is exposed to Starlark as a dict (not a struct).
    if not hasattr(p, "params") or p.params == None:
        fail("InvalidArgument: params.family: required (docGen)")
    params = p.params
    if type(params) != type({}) or "family" not in params:
        fail("InvalidArgument: params.family: required (docGen)")
    fam = params["family"]
    if fam == None or type(fam) != type("") or fam.strip() != "docGen":
        fail("InvalidArgument: params.family: must be docGen")
    return "docGen"

LIVE_LINK_PAGE_LIMIT = 8
MAX_LIVE_LINK_PAGES = 4

def live_link_target(hub_key, relation):
    # The subject's single outbound <relation> link target, enumerated via the
    # sanctioned bounded kv.Links (Contract #2 §2.5.1). A leaseapp carries
    # exactly one LIVE applicationFor / appliesToUnit link, but
    # ReassignLeaseUnit repoints appliesToUnit (tombstone old, create new) —
    # a page can hold the tombstone before the live link — so this pages
    # until it finds one; absent -> None (the doc omits what the graph
    # lacks).
    cursor = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=applicationFor|appliesToUnit epoch=none
        # (read-only field resolution: the document snapshots then-current
        # state; a concurrent link change lands in the next generation)
        page, cursor = kv.Links(hub_key, relation, "out", cursor, LIVE_LINK_PAGE_LIMIT)
        for l in page:
            if not l.isDeleted:
                return l.targetVertex
        if cursor == None:
            break
    return None

def live_link_source(hub_key, relation):
    # The counterpart of live_link_target for links where the subject is the
    # TARGET: a unit's inbound "manages" links (loftspace-domain/ownership.go
    # — source = landlord identity, target = unit; a flat co-management set
    # with no primary, and RemoveUnitOwner tombstones a manages link rather
    # than deleting it, so a page can hold a tombstone before the live
    # source). The first live entry
    # resolves a representative landlord party; absent -> None (an unmanaged
    # unit names no landlord). Unlike the tenant's display name, the resolved
    # value here is a bare identity KEY — not a sensitive aspect — so no
    # egress-declaration path is needed (contrast the tenantName comment
    # below).
    cursor = None
    for _page in range(MAX_LIVE_LINK_PAGES):
        # read-posture: (e) relation=manages epoch=none
        page, cursor = kv.Links(hub_key, relation, "in", cursor, LIVE_LINK_PAGE_LIMIT)
        for l in page:
            if not l.isDeleted:
                return l.sourceVertex
        if cursor == None:
            break
    return None

def aspect_data(key):
    # kv.Read a known aspect key (§2.5 on-demand, absence-tolerant). None when
    # absent / tombstoned — the document omits the corresponding fields.
    # read-posture: (c) deliberately live field resolution: the document
    # renders the subject's then-current display state (no OCC intent — a
    # concurrent aspect change simply lands in the next generation)
    doc = kv.Read(key)
    if doc == None or doc.isDeleted:
        return None
    return doc.data

def put_string(d, key, data, field):
    # Copy a non-blank string field into the doc; absent/blank/non-string is
    # omitted (the vendor's renderer skips what is not present).
    if data == None:
        return
    v = data.get(field)
    if v != None and type(v) == type("") and len(v.strip()) > 0:
        d[key] = v.strip()

def put_number(d, key, data, field):
    # Copy a numeric field into the doc; absent/non-numeric is omitted.
    if data == None:
        return
    v = data.get(field)
    if v != None and (type(v) == type(0) or type(v) == type(0.0)):
        d[key] = v

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "CreateLeaseDocInstance":
        # actor-guard: (primordial) restricted to Loom's relay actor, see
        # declared-read-scope-authorization-design.md §12. The grant behind this
        # op is operator/Scope:"any", which admits every operator-role holder —
        # far wider than the one engine that dispatches the docGen pattern. The
        # branch assembles document fields off the payload-named subjectKey and
        # forwards them to an external adapter, so a wider submitter set is an
        # arbitrary application's lease terms reaching a vendor.
        if op.actor != primordialActor["loom"]:
            fail("AuthDenied: CreateLeaseDocInstance is restricted to Loom's relay actor; got " + op.actor)

        handle = required_bare_handle(p, "instanceKey")
        subject_key = required_string(p, "subjectKey")
        adapter = required_string(p, "adapter")
        reply_op = required_string(p, "replyOp")
        fam = family_of(p)
        _, subject_id = parts_of(subject_key, "subjectKey", "leaseapp")

        # No-orphan invariant: the subject application MUST be alive. Loom
        # declares the subject root in ContextHint.Reads, so it is hydrated.
        if not vertex_alive(state, subject_key):
            fail("UnknownLeaseApplication: " + subject_key)

        # The signature gate: an executed-lease document exists only for a
        # SIGNED application. Read on demand (kv.Read) — absent or tombstoned
        # fails the op with NO claim and NO dispatch.
        sig = aspect_data(subject_key + ".signature")
        signed_at = None
        if sig != None:
            sa = sig.get("signedAt")
            if sa != None and type(sa) == type("") and len(sa.strip()) > 0:
                signed_at = sa.strip()
        if signed_at == None:
            fail("NotSigned: application " + subject_key + " carries no .signature; an executed lease document cannot be generated before signing")

        # Prepend the package-chosen claim-vertex type; the replyOp re-prepends
        # the SAME type — a matched pair.
        inst_key = "vtx.service." + handle

        # The type/subtype discriminator lives on the vertex ENVELOPE class (P7)
        # — service.docGen.instance. That fine-grained class misses the exact
        # class→DDL lookup, so the step-6 write-gate resolver walks the
        # instanceOf link to this DDL's meta-vertex (the type authority).
        inst_class = "service." + fam + ".instance"
        meta_key = ddl["leaseDocInstance"].metaKey
        _, meta_id = parts_of(meta_key, "typeAuthority", "meta")
        instance_of_lnk = "lnk.service." + handle + ".instanceOf.meta." + meta_id

        # providedTo: the claim (later-arriving) is the source, the pre-existing
        # LEASEAPP is the target (Contract #1 §1.1) — the document is about the
        # application. The convergence lens fans out across this link.
        provided_to_lnk = "lnk.service." + handle + ".providedTo.leaseapp." + subject_id

        mutations = [
            make_vtx(inst_key, inst_class, {}),
            make_link(instance_of_lnk, inst_key, meta_key, "instanceOf", "instanceOf", {}),
            make_link(provided_to_lnk, inst_key, subject_key, "providedTo", "providedTo", {}),
        ]

        # Assemble the document fields Processor-side (Contract #10 §10.5: a
        # field on a LINKED vertex is reached by the instanceOp DDL's own §2.5
        # kv.Read / §2.5.1 kv.Links). Every field is optional except signedAt
        # (gated above): the vendor's renderer emits only present fields.
        doc = {"signedAt": signed_at}

        applicant = live_link_target(subject_key, "applicationFor")
        if applicant != None:
            doc["applicant"] = applicant
            # The applicant identity's own .name is NOT read here: it is
            # discovered at execute time from the applicationFor link, and
            # Loom cannot pre-declare a link-discovered key in
            # contextHint.egressReads, so a plaintext kv.Read/aspect_data of
            # it would be rejected by the commit-path emission guard (no
            # sensitive plaintext may reach an external.* event). The TENANT
            # name the vendor actually renders comes from a different path
            # below (params.tenantName, the leaseapp's OWN .tenantName
            # snapshot) — this doc["applicant"] bare key is what a nameless
            # render still falls back to when that snapshot is absent.

        unit = live_link_target(subject_key, "appliesToUnit")
        if unit != None:
            doc["unitKey"] = unit
            landlord = live_link_source(unit, "manages")
            if landlord != None:
                doc["landlordKey"] = landlord
            addr = aspect_data(unit + ".address")
            put_string(doc, "unitAddress", addr, "line1")
            put_string(doc, "unitCity", addr, "city")
            put_string(doc, "unitRegion", addr, "region")
            listing = aspect_data(unit + ".listing")
            put_number(doc, "unitRent", listing, "rentAmount")
            put_string(doc, "unitCurrency", listing, "rentCurrency")
            put_number(doc, "unitBedrooms", listing, "bedrooms")
            put_number(doc, "unitBathrooms", listing, "bathrooms")
            put_number(doc, "unitLeaseTermMonths", listing, "leaseTermMonths")
            put_string(doc, "unitAvailableFrom", listing, "availableFrom")

        terms = aspect_data(subject_key + ".terms")
        put_string(doc, "termsMoveInDate", terms, "moveInDate")
        put_number(doc, "termsLeaseTermMonths", terms, "leaseTermMonths")
        put_number(doc, "termsRequestedRent", terms, "requestedRent")

        # tenantName resolves through orchestration-base's shared helper,
        # exactly like leaseServiceInstanceDDLScript resolves backgroundCheck's
        # name/dob: the leaseDocument pattern templates it as
        # "subject.tenantName.data.value" at the TOP LEVEL of Params, Loom's
        # inferExternalTaskReads declares subject_key + ".tenantName" under
        # contextHint.egressReads, and step 4 hydrates it as a $sensitiveRef
        # marker the docGen adapter opens at the bridge's egress boundary —
        # never plaintext here. A signed application carrying no .tenantName
        # snapshot must not fail the dispatch (SignLease already degraded to
        # no snapshot for exactly this case), so the template is dropped
        # BEFORE resolution when the aspect key reads absent. A TOMBSTONED
        # .tenantName is never something this branch has to handle: hydrating
        # a deleted sensitive aspect under egressReads is refused at step 4
        # (sensitive_decrypt.go, "read deleted sensitive aspect") before the
        # op's script ever runs, so a tombstoned snapshot is a refused
        # dispatch, by design, not a case this script degrades. The drop is
        # keyed on the value actually being the template — a literal
        # "tenantName" param (never emitted by the shipped pattern, but not
        # this script's business to assume) is passed through unresolved
        # rather than probed or dropped.
        raw_params = dict(p.params) if hasattr(p, "params") and p.params != None else {}
        tenant_name_template = raw_params.get("tenantName")
        if type(tenant_name_template) == type("") and tenant_name_template.startswith("subject."):
            # read-posture: (f) declared in contextHint.egressReads by Loom's
            # inferExternalTaskReads (internal/loom/externaltask_params.go);
            # its ABSENCE is known-absent via the op-meta's descriptor floor
            # (OptionalReads), so a signed application with no snapshot still
            # renders.
            tenant_name_node = kv.Read(subject_key + ".tenantName")
            if tenant_name_node == None:
                raw_params.pop("tenantName")
        params = resolve_subject_params(raw_params, subject_key)
        # family_of already validated + trimmed fam; re-pin it post-resolve so
        # a caller's untrimmed family value can never diverge from the
        # validated one (mirrors leaseServiceInstanceDDLScript).
        params["family"] = fam
        params["leaseAppKey"] = subject_key
        params["doc"] = doc

        # Emit the external.<adapter> event off this op's transactional outbox.
        # The bare handle is the opaque correlation token (instanceKey ==
        # externalRef == idempotencyKey). dispatchOp is the shared pending-marker
        # op the bridge posts if its adapter returns Pending; params carry the
        # resolved doc fields plus the (possibly $sensitiveRef-marked)
        # top-level tenantName to the vendor.
        event_data = {
            "instanceKey":    handle,
            "adapter":        adapter,
            "replyOp":        reply_op,
            "dispatchOp":     "RecordServiceDispatch",
            "externalRef":    handle,
            "idempotencyKey": handle,
            "params":         params,
        }
        events = [{"class": "external." + adapter, "data": event_data}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": inst_key}}

    fail("leaseDocInstance DDL: unknown operationType: " + ot)
`

// leaseDocReplyDDLScript is the docGen externalTask replyOp the bridge submits.
// It mirrors leaseServiceReplyDDLScript's read-free mechanics (reconstruct
// vtx.service.<handle>, create-only .outcome, the orchestration completion +
// provenance events) with two deliberate differences: on a completed reply the
// bridge's free-form result string IS structured — the JSON document-pointer
// object the vendor produced — and its fields are parsed (json.decode) onto the
// .outcome aspect, because the convergence lens and the §10.8 AttachObject
// playbook read them from there (reference metadata for bytes already in the
// object store, never PII); and there is no validUntil (a produced document
// does not expire — no freshness lane).
//
// The bridge submits with no ContextHint.Reads, so the op reads NOTHING from
// state; the once-only guarantee is the create-only .outcome write itself (a
// redelivered reply conflicts on the existing key), atop the bridge's
// deterministic deriveReplyRequestID collapse on the Contract #4 tracker.
const leaseDocReplyDDLScript = `
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

# The terminal outcome values RecordLeaseDocOutcome admits. completed = the
# vendor rendered the document and stored its bytes; failed = a definitive
# render rejection (missing required inputs). The bridge supplies it verbatim
# from the adapter's Result.Status — required, with no default.
OUTCOME_STATUSES = ["completed", "failed"]

def required_status(p):
    st = required_string(p, "status")
    if st not in OUTCOME_STATUSES:
        fail("InvalidArgument: status: must be one of completed, failed; got " + st)
    return st

def pointer_string(ptr, name):
    v = ptr.get(name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: result." + name + ": required non-empty string on a completed docGen reply")
    return v.strip()

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "RecordLeaseDocOutcome":
        handle = required_bare_handle(p, "externalRef")
        # Reconstruct the claim-vertex key from the bare handle: the instanceOp
        # chose 'service' as the type, so the replyOp re-prepends the same type.
        inst_key = "vtx.service." + handle

        status = required_status(p)
        completed_at = time.rfc3339_utc(op.submittedAt)
        result = optional_string(p, "result")

        outcome_data = {"status": status, "completedAt": completed_at}
        if status == "completed":
            # A completed reply's result is the vendor's document-pointer object,
            # JSON-encoded into the bridge's free-form Detail string. Parse it
            # (json.decode with a tolerant default so a malformed string is a
            # structured rejection, not an opaque decode error) and copy the
            # pointer set onto the aspect — the lens/playbook read it there.
            if result == None:
                fail("InvalidArgument: result: required on a completed docGen reply (the JSON document-pointer object)")
            ptr = json.decode(result, None)
            if ptr == None or type(ptr) != type({}):
                fail("InvalidArgument: result: must be the JSON document-pointer object {digest,size,contentType,storeName,filename}")
            outcome_data["digest"] = pointer_string(ptr, "digest")
            outcome_data["contentType"] = pointer_string(ptr, "contentType")
            outcome_data["storeName"] = pointer_string(ptr, "storeName")
            outcome_data["filename"] = pointer_string(ptr, "filename")
            size = ptr.get("size")
            if size == None or type(size) != type(0) or size < 0:
                fail("InvalidArgument: result.size: required non-negative integer on a completed docGen reply")
            outcome_data["size"] = size

        # Write the .outcome aspect as a create-only mutation. This create-only
        # IS the once-only guarantee: a redelivered reply conflicts on the
        # existing key and the batch is rejected (FR58 at the DDL layer, atop
        # the bridge's deterministic requestId collapse). The instance root,
        # already {data:{}}, is not touched (D5). No validUntil — a produced
        # document does not expire.
        mutations = [
            make_aspect(inst_key, "outcome", "leaseDocOutcome", outcome_data),
        ]

        # Emit the completion signal Loom correlates on (the BARE handle as
        # externalRef — Loom parks on token.<handle>) PLUS a provenance event
        # carrying the raw result string for the audit join.
        provenance = {"serviceKey": inst_key, "status": status, "completedAt": completed_at}
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

    fail("leaseDocReply DDL: unknown operationType: " + ot)
`
