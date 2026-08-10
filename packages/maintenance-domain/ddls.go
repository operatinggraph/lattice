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
			`"location":{"type":"string","description":"vtx.<locType>.<NanoID> of the place the issue is at (ReportIssue; required, validated alive + an admitted location type segment)."},` +
			`"workOrderId":{"type":"string","description":"Optional bare NanoID for the new work-order vertex (ReportIssue); absent → minted."},` +
			`"workOrderKey":{"type":"string","description":"vtx.workorder.<NanoID> being resolved (ResolveWorkOrder; required, validated alive)."},` +
			`"notes":{"type":"string","description":"What was done to resolve it (ResolveWorkOrder; required)."}},` +
			`"required":[]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.workorder.<NanoID> the operation wrote."}}}`,
		FieldDescription: map[string]string{
			"summary":      "One line describing the issue, e.g. \"Boiler in the basement is cycling\" (ReportIssue; required). Shown as the work order's label everywhere — keep it free of resident PII, since it rides the SYNC plane to staff devices (D3).",
			"priority":     "low | normal | urgent (ReportIssue; optional, default normal).",
			"location":     "Full vtx.<locType>.<NanoID> key of the location-domain place the issue is at (a unit, a building). Validated alive + an admitted location type segment; written as the workorder locatedAt location link. MUST be listed in ContextHint.Reads.",
			"workOrderId":  "Optional bare NanoID (no dots / key segments) for the new work-order vertex. Absent → minted with nanoid.new().",
			"workOrderKey": "Full vtx.workorder.<NanoID> key of the work order being resolved (ResolveWorkOrder). Auto-filled by a task-driven client from the task's scopedTo target, not typed.",
			"notes":        "What was actually done (ResolveWorkOrder; required). TERMINAL: the same notes re-submit harmlessly — which is what makes an offline drain retry safe — but different notes are rejected, so a resolution can never silently flip.",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name:    "ReportIssue — raise a work order at a unit",
				Payload: map[string]any{"summary": "Kitchen tap is dripping", "priority": "normal", "location": "vtx.unit.<NanoID>"},
				ExpectedOutcome: "Validates the location is alive + an admitted location type segment and that the caller worksAt a location covering it " +
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

# The concrete location levels an issue may be reported at (Contract #6 §6.9).
# location-domain owns the vertices; this script references them by KEY TYPE —
# a location vertex's class IS its own key type, so no single class value names
# the family.
LOCATION_TYPES = ["unit", "building", "property"]

# The class a location vertex minted before the taxonomy landed carries: one
# shared discriminator across all three levels. Nothing rewrites those
# documents, so both class shapes are live at once and this guard admits
# either.
LEGACY_LOCATION_CLASS = "location"

# The full set of classes a live location vertex may carry: its own key type
# (the class every newly-minted location gets) or the shared legacy
# discriminator.
LOCATION_CLASSES = LOCATION_TYPES + [LEGACY_LOCATION_CLASS]

def require_live_location(state, key, name):
    # Alive, keyed vtx.<locationType>.<NanoID> at an admitted location level,
    # AND carrying a location class.
    # BOTH the key and the class are checked, and each catches what the other
    # cannot. The KEY's type segment is the type authority — it is what a lens
    # label resolves against, and it is the only thing that can say "any
    # location" across the three levels, since a location's class is its own
    # key type. The CLASS is what proves location-domain minted the vertex: a
    # foreign package writing vtx.unit.<id> with a class of its own passes the
    # key check and must still be refused.
    if not vertex_alive(state, key):
        fail("UnknownEndpoint: " + name + ": " + key + " is absent or tombstoned")
    lt, _ = parts_of(key, name, "")
    if lt not in LOCATION_TYPES:
        fail("NotALocation: " + name + ": " + key + " has type segment " + str(lt) + ", required one of unit, building, property")
    cls = class_of(state, key)
    if cls not in LOCATION_CLASSES:
        fail("NotALocation: " + name + ": " + key + " has class " + str(cls) + ", required its own location type or " + LEGACY_LOCATION_CLASS)

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
ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4
WORKPLACE_PARENT_PAGE_LIMIT = 20
MAX_PARENT_PAGES = 4
WORKPLACE_MAX_DEPTH = 8
WORKPLACE_MAX_NODES = 64

def actor_holds_operator(actor_key):
    # Resolved from the GRAPH, not from a compile-time constant: the primordial
    # role ids are loaded at runtime (bootstrap.LoadPrimordialNanoIDs) while a
    # package's Definition -- and so its script text -- is built at package-init,
    # so no substitution can see the operator id. The walk mirrors the kernel's
    # own root-grant lens exactly (internal/bootstrap/lenses.go: MATCH (identity)
    # -[:holdsRole]->(role) WHERE role.canonicalName.data.value = 'operator').
    #
    # Paginated: a role beyond page 1 must not read as "not held" -- the walk
    # follows the cursor up to MAX_ROLE_PAGES pages before giving up, and
    # giving up still denies (fail-closed).
    cursor = None
    for _page in range(MAX_ROLE_PAGES):
        # read-posture: (e) relation=holdsRole epoch=none -- an identity holds few
        # roles, so this is never a keyspace scan. A role granted concurrently with
        # this write is not a race worth closing: it can only widen authority, and
        # the confined branch is the safe one.
        page, cursor = kv.Links(actor_key, "holdsRole", "out", cursor, ROLE_PAGE_LIMIT)
        for lk in page:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the enumeration
            # above (data-derived key -- the role is unknown until it resolves).
            cn = kv.Read(lk.targetVertex + ".canonicalName")
            if cn != None and not cn.isDeleted and cn.data.get("value") == "operator":
                return True
        if cursor == None:
            return False
    return False

def worksAt_covers(actor_id, location_key):
    # Answers "does this actor worksAt this location, or any LIVE location that
    # contains it?" -- a BREADTH-first walk up the containedIn topology, testing
    # the actor's deterministic worksAt link at every node. The location itself
    # is tested first, so a staff member wired to an exact unit matches too; one
    # wired to any containing building matches everything containedIn it.
    #
    # A tombstoned link OR VERTEX is absent. kv.Read returns the tombstone
    # document rather than None (step4_hydrate routes only ErrKeyNotFound to
    # knownAbsent), and UnwireWorksAt / TombstoneLocation tombstone rather than
    # delete, so isDeleted is tested explicitly in three places: the worksAt
    # link, each containedIn link, and every location VERTEX the walk stands on.
    # The vertex test is what stops a DECOMMISSIONED location from still
    # conferring authority -- TombstoneLocation does not cascade to containedIn
    # links (location-domain), so those links stay live and only the vertex's own
    # isDeleted marks it gone, while the read side stops dead there (the full
    # engine's fetchNode yields nothing for a soft-deleted node). Transiting one
    # would grant a write the reader would never show.
    #
    # It is tested on EVERY node, the caller-supplied one included, not just on
    # ancestors: a guard where a dead ancestor confers nothing but a dead
    # starting location confers everything would be exactly the kind of
    # inconsistency the next reader copies wrongly.
    #
    # EVERY parent is followed, not one per level: containment is a DAG. A walk
    # that kept a single parent would deny a staffer wired to whichever branch it
    # happened to discard, while a read-side lens projecting a covering set
    # unions every branch of [:containedIn*0..7] (cafe-domain's and
    # wellness-domain's coveringLocations are the two that do).
    #
    # Bounded three ways so an op-time guard cannot fan out: WORKPLACE_MAX_DEPTH
    # levels (0..7, the read side's hop range), WORKPLACE_PARENT_PAGE_LIMIT
    # parents per node, and WORKPLACE_MAX_NODES distinct nodes overall, a node
    # never being enqueued twice. Exhausting a bound falls through to the final
    # 'return False' -- a DENIAL, never an escape. The node budget is the one
    # bound the read side does not share (its walk caps hops, not nodes), so a
    # containment tree wide enough to exhaust it denies a write the reader would
    # show; it is set far above any real topology, and it fails closed.
    if location_key == None:
        return False
    frontier = [location_key]
    seen = [location_key]
    for _ in range(WORKPLACE_MAX_DEPTH):
        if len(frontier) == 0:
            return False
        parents = []
        for cur in frontier:
            parts = cur.split(".")
            if len(parts) != 3:
                # Not walkable. Stops its OWN branch rather than aborting the
                # walk, so one malformed ancestor cannot deny a sibling branch
                # that would have matched. A malformed location_key still
                # denies: nothing else is queued, so the frontier empties.
                continue
            # read-posture: (e) per-candidate follow-up read off the containedIn
            # enumeration below -- the location VERTEX, so a tombstoned one
            # neither confers a match nor is walked through.
            node = kv.Read(cur)
            if node == None or node.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the same
            # enumeration (data-derived key -- the ancestor chain is not
            # knowable client-side, so it cannot be pre-declared).
            lnk = kv.Read("lnk.identity." + actor_id + ".worksAt." + parts[1] + "." + parts[2])
            if lnk != None and not lnk.isDeleted:
                return True
            # Paginated: a parent beyond page 1 must not read as "no more
            # parents" -- the walk follows the cursor up to MAX_PARENT_PAGES
            # pages before moving on, same as actor_holds_operator's role walk.
            cursor = None
            for _page in range(MAX_PARENT_PAGES):
                # read-posture: (e) relation=containedIn epoch=none -- a location has
                # at most a few parents; containment is provisioned topology, not
                # written concurrently with this op.
                page, cursor = kv.Links(cur, "containedIn", "out", cursor, WORKPLACE_PARENT_PAGE_LIMIT)
                for lk in page:
                    if lk.isDeleted:
                        continue
                    nxt = lk.targetVertex
                    if nxt in seen:
                        continue
                    if len(seen) >= WORKPLACE_MAX_NODES:
                        continue
                    # Charged to the budget at ENQUEUE, so the node count bounds the
                    # walk's reads exactly rather than to within a page, and an
                    # ancestor reachable from several branches is visited once.
                    seen.append(nxt)
                    parents.append(nxt)
                if cursor == None:
                    break
        frontier = parents
    return False

def workplace_exempt():
    # The cheap half of require_workplace, callable BEFORE a domain resolver
    # runs. Starlark evaluates arguments eagerly, so
    # require_workplace(resolve(x), ...) would walk the target's topology even
    # for root -- wasted reads, and worse, a malformed key anywhere in that walk
    # raises where the op previously succeeded. Call sites therefore gate on
    # this; require_workplace re-checks it anyway, so a site that forgets the
    # gate is still CORRECT, only slower.
    return op.authTargetValidated or actor_holds_operator(op.actor)

def require_workplace(location_keys, what):
    # Binds the STANDING path only -- operator and staff role grants, which
    # authorize via scope=any and so carry no target the platform has checked.
    # A scope=self or task caller is bound instead by its own op's ownership
    # probe (the applicationFor / identifiedBy indirection, or an explicit
    # bind of the validated target to the resource): a resident legitimately
    # holds no worksAt link, and confining them by a rule written for staff
    # would deny every self-service write. The two guards are complementary,
    # not alternatives -- each binds the path the other cannot see.
    #
    # The exemption keys on authTargetValidated, NOT on authContextTarget being
    # non-empty: the raw target is a client-supplied hint that any scope=any
    # holder can set, so exempting on its presence would let any staff member
    # opt out of confinement.
    if op.authTargetValidated:
        return
    enforce_workplace(location_keys, what)

def enforce_workplace(location_keys, what):
    # require_workplace minus the validated-target exemption, for a
    # resource-scoped op that has already checked for itself that the validated
    # target names the resource being acted on. Past that check the caller is an
    # ordinary staff member and must clear the worksAt walk like any other.
    #
    # location_keys is a LIST of candidate locations, and covering ANY ONE of
    # them authorizes the write: a target can legitimately sit at several places
    # at once (a provider practises at two buildings), and staff at either one
    # are equally entitled to it. An empty list -- a target whose location
    # cannot be resolved at all -- is a DENIAL for anyone but an operator, so an
    # unwired topology fails closed rather than falling open.
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
        require_live_location(state, loc, "location")

        # Confinement on a CREATE differs from F4's four resolve-the-target
        # sites in exactly one way, and the difference is safe: there is no
        # target topology yet, so the REPORTED location is itself the subject.
        # Property 3 above guards against a caller naming a location that is
        # not where the target actually is -- claiming authority over something
        # elsewhere. Here the named location BECOMES the work order's truth (it
        # is written as the locatedAt link in this same batch), so naming a
        # place the caller does not worksAt-cover only DENIES the write; it
        # cannot reach anything the caller was not already entitled to.
        # workplace-exempt: (no-validated-path) ReportIssue is granted scope=any
        # to operator + both staff roles only (permissions.go) and no task mints
        # it today. It DOES carry an op-meta, so a CreateTask forOperation it
        # would make a validated target reachable -- add a resource bind here
        # before minting any such task.
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

        # Authorization runs ahead of the terminal branch below, the ordering
        # clinic's SetAppointmentStatus gives its own terminal-status read
        # (its identity binding precedes that read for the same reason; its
        # payload-matching probes are a separate matter). That branch answers a
        # resolved work order differently from an open one -- an idempotent
        # accept versus AlreadyResolved -- so a caller who reached it before
        # being authorized would hold a read oracle over a building they do not
        # work at: resolved-vs-open, and the resolution notes themselves,
        # recoverable by probing which submission the branch accepts. Guarding
        # first means a caller who may not resolve the work order cannot read
        # its resolution state either. The liveness check above still answers
        # ahead of the guard, so key EXISTENCE remains distinguishable from
        # denial -- the same shape clinic's SetAppointmentStatus carries.
        #
        # A validated target only exempts a claimant when it names THIS work
        # order. The task grant is scopedTo one work order (authContext.target),
        # while the work order actually resolved comes from payload.workOrderKey
        # -- two independent client fields. Without the bind, a tech holding a
        # legitimate grant for one work order could resolve a different one at a
        # building they do not work at. Past the bind the caller is ordinary
        # staff, so enforce_workplace (not require_workplace, whose own
        # validated-target exemption would re-open exactly this hole) runs the
        # worksAt walk.
        # authcontext-target: (resource-bind) the VALIDATED target must name
        # the work order this op resolves.
        resource_bound = op.authTargetValidated and op.authContextTarget == wkey
        if not (resource_bound or actor_holds_operator(op.actor)):
            loc = workorder_location(wkey)
            locs = []
            if loc != None:
                locs = [loc]
            enforce_workplace(locs, "ResolveWorkOrder on " + wkey)

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
