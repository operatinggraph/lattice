package maintenancedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Canonical names. One vertexType DDL owns both op scripts (an op is admitted
// by EXACTLY ONE vertexType DDL — the operationType→script index drops an op
// claimed by two). The aspect-type DDLs are step-6 write gates only, mirroring
// clinic-domain / wellness-domain's split.
const (
	workOrderVertexDDL = "workOrder"

	workOrderReportAspectDDL     = "workOrderReport"
	workOrderResolutionAspectDDL = "workOrderResolution"
)

// DDLs returns the package's three DDL meta-vertex declarations.
//
// Architectural rules (binding — the known-key discipline of clinic-domain /
// wellness-domain): the scripts read by known key, plus the ONE sanctioned
// bounded enumeration class F4's workplace guard already established
// (Contract #2 §2.5.1, read-posture (e): an identity's holdsRole links and a
// location's containedIn parents, each annotated at its call site).
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		workOrderVertexTypeDDL(),
		workOrderReportAspectTypeDDL(),
		workOrderResolutionAspectTypeDDL(),
	}
}

func workOrderVertexTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     workOrderVertexDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"ReportIssue", "ResolveWorkOrder"},
		Description: "Maintenance work-order DDL. Vertex shape: vtx.workorder.<NanoID>, class=workorder, root " +
			"data = {} (minimal, D5 — the content lives in the .report / .resolution aspects). ReportIssue " +
			"mints the work order + the .report aspect {summary, priority, reportedAt (canonical-UTC of " +
			"op.submittedAt), reportedBy (op.actor)} + the workorder locatedAt location LINK " +
			"(lnk.workorder.<id>.locatedAt.<locType>.<locId>; source = the later-arriving work order, target = " +
			"the pre-existing location, Contract #1 §1.1). It does NOT mint a task: a work order becomes queued " +
			"WORK only when someone submits orchestration-base's CreateTask(queue: <role>, forOperation: " +
			"<ResolveWorkOrder's op-meta>, scopedTo: <the work order>), which owns the FR28 exactly-one-of " +
			"assignedTo/queuedFor invariant. ResolveWorkOrder writes the .resolution aspect {notes, resolvedAt, " +
			"resolvedBy} — it is the op that queued task GRANTS, performed by the claimant under " +
			"authContext.task, and the Processor's §10.6 auto-complete closes the task on the same commit (so " +
			"there is no separate completion op). .resolution is the read-before-write terminal marker: a " +
			"re-submit carrying IDENTICAL notes is an idempotent no-op (an offline device's drain retry must " +
			"not fail the tech's work), a re-submit carrying DIFFERENT notes rejects AlreadyResolved (a " +
			"resolution never silently flips). Both ops carry F4's canonical workplace write-confinement guard: " +
			"a standing-path caller must worksAt a location covering the work order's place, root (the " +
			"primordial operator role, resolved from the graph) is exempt, and a task-path caller is bound " +
			"instead by the task's own scopedTo grant.",
		Script: workOrderDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"summary":{"type":"string","description":"What is wrong (ReportIssue; required)."},` +
			`"priority":{"type":"string","enum":["low","normal","urgent"],"description":"How urgent the issue is (ReportIssue; optional, default normal)."},` +
			`"location":{"type":"string","description":"vtx.<locType>.<NanoID> of the place the issue is at (ReportIssue; required, validated alive + class=location)."},` +
			`"workOrderId":{"type":"string","description":"Optional bare NanoID for the new work-order vertex (ReportIssue); absent → minted."},` +
			`"workOrderKey":{"type":"string","description":"vtx.workorder.<NanoID> being resolved (ResolveWorkOrder; required, validated alive)."},` +
			`"notes":{"type":"string","description":"What was done to resolve it (ResolveWorkOrder; required)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.workorder.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"summary":      "One line describing the issue, e.g. \"Boiler in the basement is cycling\" (ReportIssue; required). Shown as the work order's label everywhere — keep it free of resident PII, since it rides the SYNC plane to staff devices (D3).",
			"priority":     "low | normal | urgent (ReportIssue; optional, default normal).",
			"location":     "Full vtx.<locType>.<NanoID> key of the location-domain place the issue is at (a unit, a building). Validated alive + class=location; written as the workorder locatedAt location link. MUST be listed in ContextHint.Reads.",
			"workOrderId":  "Optional bare NanoID (no dots / key segments) for the new work-order vertex. Absent → minted with nanoid.new().",
			"workOrderKey": "Full vtx.workorder.<NanoID> key of the work order being resolved (ResolveWorkOrder). Auto-filled by a task-driven client from the task's scopedTo target, not typed.",
			"notes":        "What was actually done (ResolveWorkOrder; required). TERMINAL: the same notes re-submit harmlessly — which is what makes an offline drain retry safe — but different notes are rejected, so a resolution can never silently flip.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "ReportIssue — raise a work order at a unit",
				Payload: map[string]any{"summary": "Kitchen tap is dripping", "priority": "normal", "location": "vtx.unit.<NanoID>"},
				ExpectedOutcome: "Validates the location is alive + class=location and that the caller worksAt a location covering it " +
					"(root exempt). Mints vtx.workorder.<NanoID> (class=workorder, root {}) + the .report aspect + " +
					"lnk.workorder.<id>.locatedAt.unit.<NanoID>. Returns primaryKey (the work-order key).",
			},
			{
				Name:    "ResolveWorkOrder — close it out",
				Payload: map[string]any{"workOrderKey": "vtx.workorder.<NanoID>", "notes": "Replaced the washer."},
				ExpectedOutcome: "Validates the work order is alive and unresolved, then writes the .resolution aspect " +
					"{notes, resolvedAt, resolvedBy}. Submitted under authContext.task by the claimant of the task " +
					"scopedTo this work order, the §10.6 auto-complete closes that task on the same commit. " +
					"Re-submitting the identical notes is an accepted no-op; different notes reject AlreadyResolved.",
			},
		},
	}
}

func workOrderReportAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     workOrderReportAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"ReportIssue"},
		Description: "Work-order report aspect. Stored as vtx.workorder.<NanoID>.report (class workOrderReport) = " +
			"{summary, priority, reportedAt, reportedBy}. Non-sensitive by construction — maintenance work is " +
			"unit/equipment-scoped, and a summary must carry no resident PII because these rows ride the SYNC " +
			"plane to staff devices where D3 forbids plaintext PII. Written by ReportIssue (whose workOrder " +
			"vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. Declaration-only: " +
			"no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"summary":{"type":"string"},"priority":{"type":"string"},"reportedAt":{"type":"string"},"reportedBy":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"summary":    "One line describing the issue.",
			"priority":   "low | normal | urgent.",
			"reportedAt": "RFC3339 instant the issue was reported (canonical UTC of op.submittedAt).",
			"reportedBy": "vtx.identity.<NanoID> of the reporting actor (op.actor — the trusted submitter, never a payload field).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "work-order report aspect",
				Payload:         map[string]any{"summary": "Kitchen tap is dripping", "priority": "normal", "reportedAt": "2026-07-21T09:00:00Z", "reportedBy": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.workorder.<NanoID>.report; written by ReportIssue.",
			},
		},
	}
}

func workOrderResolutionAspectTypeDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     workOrderResolutionAspectDDL,
		Class:             "meta.ddl.aspectType",
		PermittedCommands: []string{"ResolveWorkOrder"},
		Description: "Work-order resolution aspect. Stored as vtx.workorder.<NanoID>.resolution (class " +
			"workOrderResolution) = {notes, resolvedAt, resolvedBy}. Its PRESENCE is the work order's terminal " +
			"state (root data stays {} — D5), the read-before-write marker ResolveWorkOrder consults as an " +
			"OptionalRead, mirroring lease-signing's .decision. Written by ResolveWorkOrder (whose workOrder " +
			"vertexType DDL owns the script); this aspect-type DDL is the step-6 write gate. Declaration-only: " +
			"no op handler.",
		Script: aspectDeclarationOnlyScript,
		InputSchema: `{"type":"object","properties":` +
			`{"notes":{"type":"string"},"resolvedAt":{"type":"string"},"resolvedBy":{"type":"string"}}}`,
		OutputSchema: `{"type":"object"}`,
		FieldDescription: map[string]string{
			"notes":      "What was done to resolve the issue.",
			"resolvedAt": "RFC3339 instant the work order was resolved (canonical UTC of op.submittedAt).",
			"resolvedBy": "vtx.identity.<NanoID> of the resolving actor (op.actor — the trusted submitter, never a payload field).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:            "work-order resolution aspect",
				Payload:         map[string]any{"notes": "Replaced the washer.", "resolvedAt": "2026-07-21T11:30:00Z", "resolvedBy": "vtx.identity.<NanoID>"},
				ExpectedOutcome: "Stored as vtx.workorder.<NanoID>.resolution; written by ResolveWorkOrder.",
			},
		},
	}
}

// aspectDeclarationOnlyScript is the step-6 write-gate stub every aspect-type
// DDL in this package carries: the aspect is written by the vertexType DDL's
// own script, so this one is never dispatched.
const aspectDeclarationOnlyScript = `
def execute(state, op):
    fail("UnknownOperation: declaration-only aspect DDL admits no operations")
`

// workOrderDDLScript handles ReportIssue + ResolveWorkOrder.
//
// The workplace-confinement half (WORKPLACE_* constants, actor_holds_operator,
// worksAt_covers, workplace_exempt, require_workplace) is byte-identical to the
// guard F4 shipped in cafe-domain / clinic-domain / lease-signing /
// wellness-domain; see facet-staff-worlds-design.md §6 F4 for why each of its
// three properties is the opposite of the simpler form that looks right.
const workOrderDDLScript = `
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

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None:
        return None
    if not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_typed(state, key, name, want_class):
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != want_class:
        fail("WrongClass: " + name + ": " + key + " has class " + str(cls) + ", required " + want_class)

# --- workplace write confinement (facet-staff-worlds-design.md §3.5) ---------
#
# A staff actor may write only inside the location it worksAt. Three properties
# make this sound; each is a trap a simpler form falls into.
#
# 1. The exemption is ROLE-derived, never worksAt-derived. Exempting "an actor
#    with no worksAt link" would be perverse: UnwireWorksAt would WIDEN a staff
#    member's write surface from one building to everywhere. The exemption is
#    holding the primordial 'operator' role -- the same walk the kernel projects
#    its own root grant from (internal/bootstrap/lenses.go: MATCH (identity)
#    -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator'), so
#    an actor that is genuinely root necessarily has it. Everyone else is
#    confined, and an actor holding no roles at all is confined to nothing.
#
# 2. A tombstoned link is ABSENT. kv.Read returns the tombstone DOCUMENT rather
#    than None (step4_hydrate routes only ErrKeyNotFound to knownAbsent), and
#    UnwireWorksAt tombstones rather than deletes, so the '== None' form the
#    cafe/clinic self-guards use would let a moved-on staff member keep writing.
#
# 3. The location is resolved from the TARGET's own topology, never from a
#    payload field -- a caller cannot forge which building it is writing at.
WORKPLACE_ROLE_PAGE_LIMIT = 50
WORKPLACE_PARENT_PAGE_LIMIT = 20
WORKPLACE_MAX_DEPTH = 8

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
    # roles, so this is never a keyspace scan. A role granted concurrently with
    # this write is not a race worth closing: it can only widen authority, and
    # the confined branch is the safe one.
    page, _ = kv.Links(actor_key, "holdsRole", "out", None, WORKPLACE_ROLE_PAGE_LIMIT)
    for lk in page:
        if lk.isDeleted:
            continue
        # read-posture: (e) per-candidate follow-up read off the enumeration
        # above (data-derived key -- the role is unknown until it resolves).
        cn = kv.Read(lk.targetVertex + ".canonicalName")
        if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
            return True
    return False

def worksAt_covers(actor_id, location_key):
    # Walks the location's containedIn chain upward, testing the actor's
    # deterministic worksAt link at each level. The location itself is tested
    # first, so a staff member wired to an exact unit matches too; one wired to
    # the building matches everything containedIn it.
    cur = location_key
    for _ in range(WORKPLACE_MAX_DEPTH):
        if cur == None:
            return False
        parts = cur.split(".")
        if len(parts) != 3:
            return False
        # read-posture: (e) per-candidate follow-up read off the containedIn
        # enumeration below (data-derived key -- the ancestor chain is not
        # knowable client-side, so it cannot be pre-declared).
        lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
        if lnk != None and not lnk.isDeleted:
            return True
        # read-posture: (e) relation=containedIn epoch=none -- a location has at
        # most a few parents; containment is provisioned topology, not written
        # concurrently with this op.
        page, _ = kv.Links(cur, "containedIn", "out", None, WORKPLACE_PARENT_PAGE_LIMIT)
        nxt = None
        for lk in page:
            if not lk.isDeleted:
                nxt = lk.targetVertex
        cur = nxt
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authContextTarget != "" or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # submit with no authContext (scope=any never sets one). A scope=self caller
    # is bound instead by its own op's ownership probe (the applicationFor /
    # identifiedBy indirection): a resident legitimately holds no worksAt link,
    # and confining them by a rule written for staff would deny every
    # self-service write. The two guards are complementary, not alternatives --
    # each binds the path the other cannot see.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
    if op.authContextTarget != "":
        return
    if actor_holds_operator(op.actor):
        return
    _, actor_id = parts_of(op.actor, "actor", "identity")
    for loc in location_keys:
        if loc != None and worksAt_covers(actor_id, loc):
            return
    fail("AuthDenied: " + op.actor + " does not worksAt any location covering " +
         str(location_keys) + "; " + what)

def workorder_location(work_order_key):
    # A work order's location is its own locatedAt target -- written by
    # ReportIssue, never a payload field on the resolve path.
    # read-posture: (e) relation=locatedAt epoch=none -- a work order carries
    # exactly one locatedAt link (required at ReportIssue), so this is never a
    # keyspace scan.
    page, _ = kv.Links(work_order_key, "locatedAt", "out")
    loc = None
    for lk in page:
        if not lk.isDeleted:
            loc = lk.targetVertex
    return loc

PRIORITIES = ["low", "normal", "urgent"]

def priority_of(p):
    if not hasattr(p, "priority"):
        return "normal"
    v = getattr(p, "priority")
    if v == None:
        return "normal"
    if type(v) != type(""):
        fail("InvalidArgument: priority: must be one of " + str(PRIORITIES))
    v = v.strip()
    if len(v) == 0:
        return "normal"
    if v not in PRIORITIES:
        fail("InvalidArgument: priority: must be one of " + str(PRIORITIES) + "; got " + v)
    return v

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "ReportIssue":
        summary = required_string(p, "summary")
        priority = priority_of(p)
        loc = required_string(p, "location")
        ltype, lid = parts_of(loc, "location", "")
        require_live_typed(state, loc, "location", "location")

        # Confinement on a CREATE differs from F4's four resolve-the-target
        # sites in exactly one way, and the difference is safe: there is no
        # target topology yet, so the REPORTED location is itself the subject.
        # Property 3 above guards against a caller naming a location that is
        # not where the target actually is -- claiming authority over something
        # elsewhere. Here the named location BECOMES the work order's truth (it
        # is written as the locatedAt link in this same batch), so naming a
        # place the caller does not worksAt-cover only DENIES the write; it
        # cannot reach anything the caller was not already entitled to.
        if not workplace_exempt():
            require_workplace([loc], "ReportIssue at " + loc)

        wid = bare_nanoid_or_mint(p, "workOrderId")
        wkey = "vtx.workorder." + wid
        reported_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_vtx(wkey, "workorder", {}),
            make_aspect(wkey, "report", "workOrderReport",
                        {"summary": summary, "priority": priority,
                         "reportedAt": reported_at, "reportedBy": op.actor}),
            make_link("lnk.workorder." + wid + ".locatedAt." + ltype + "." + lid,
                      wkey, loc, "locatedAt", "locatedAt", {}),
        ]
        events = [{"class": "maintenance.issueReported",
                   "data": {"workOrderKey": wkey, "location": loc, "priority": priority}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": wkey}}

    if ot == "ResolveWorkOrder":
        wkey = required_string(p, "workOrderKey")
        _, wid = parts_of(wkey, "workOrderKey", "workorder")
        notes = required_string(p, "notes")
        if not vertex_alive(state, wkey):
            fail("UnknownWorkOrder: " + wkey)

        # Read-before-write terminal, mirroring lease-signing's .decision. The
        # aspect is an OptionalRead: it is legitimately absent on the first
        # resolve, which is the overwhelmingly common case.
        rkey = wkey + ".resolution"
        # read-posture: (d) declared in ContextHint.OptionalReads by every
        # dispatcher of ResolveWorkOrder (its op-meta's dispatch.optionalReads
        # carries "{payload.workOrderKey}.resolution", so a descriptor-driven
        # client declares it from the vertex alone).
        existing = kv.Read(rkey)
        if existing != None and not existing.isDeleted:
            prior = existing.data.get("notes")
            if prior == notes:
                # Idempotent no-op -- NOT politeness. An offline device queues
                # this op, drains on reconnect, and a drain that retries under a
                # fresh requestId slips past the Contract #4 tracker; failing it
                # would lose the tech's work at exactly the moment the offline
                # beat is supposed to pay off (facet-staff-worlds-design.md §6
                # F5). Differing notes still reject below, so a resolution can
                # never silently flip.
                #
                # No response field at all, mirroring ClaimTask's own idempotent
                # re-claim branch: the reply constraint requires a named
                # primaryKey to lie within the write footprint, and a no-op has
                # none. The client already holds the key it submitted.
                return {"mutations": [], "events": []}
            fail("AlreadyResolved: " + wkey + " was resolved with different notes; " +
                 "a resolution is terminal and cannot be rewritten")

        if not workplace_exempt():
            loc = workorder_location(wkey)
            locs = []
            if loc != None:
                locs = [loc]
            require_workplace(locs, "ResolveWorkOrder on " + wkey)

        resolved_at = time.rfc3339_utc(op.submittedAt)
        mutations = [
            make_aspect(wkey, "resolution", "workOrderResolution",
                        {"notes": notes, "resolvedAt": resolved_at, "resolvedBy": op.actor}),
        ]
        events = [{"class": "maintenance.workOrderResolved",
                   "data": {"workOrderKey": wkey, "resolvedBy": op.actor}}]
        return {"mutations": mutations, "events": events, "response": {"primaryKey": wkey}}

    fail("UnknownOperation: " + ot)
`
