package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Canonical name of the ownership DDL. It owns the landlord→unit ownership link
// (the residence/management relationship the cap-read.residence grant lens
// projects so a landlord reads only their own units' applications — D1.3).
const loftspaceOwnershipDDL = "loftspaceOwnership"

// loftspaceOwnershipVertexDDL declares the loftspaceOwnership vertexType DDL —
// the op handler for AssignUnitOwner + RemoveUnitOwner.
//
// It models a landlord's management of a leasable unit as a LINK, never a key in
// an aspect (Contract #1): the later-arriving fact is the link, and — mirroring
// lease-signing's per-(applicant, unit) appliedToUnit guard where both endpoints
// pre-exist — the ACTOR identity is the source, the unit the target:
//
//	lnk.identity.<landlordID>.manages.unit.<unitID>   (class "manages")
//
// Reads as "this landlord manages this unit." Source = the identity, target =
// the unit, so the residence GrantTable lens (D1.3 Increment 2) anchors on the
// link and projects (actor = link source identity, anchor = link target unit) —
// the actor's owned-unit grant with no per-landlord lens.
//
// This package owns NO vertex type (the identity is identity-domain's, the unit
// location-domain's). loftspaceOwnership only contributes the management link on
// top of them — the same cross-package contribution pattern the listing/address
// aspects use. The link carries class "manages"; like every lease-signing link
// it needs no link-type DDL (a link mutation resolves to the step-6 permissive
// default — the authorizing op is the gate).
//
// A management relationship is a plain pair-uniqueness fact: at most ONE live
// link per (landlord, unit), but a unit may have MANY landlords and a landlord
// MANY units. The deterministic per-pair key IS the uniqueness constraint, so
// AssignUnitOwner needs no list — it reads the one key on demand and
// creates / revives / no-ops. RemoveUnitOwner is the reversible complement
// (tombstone the link) so an ownership transfer or correction never requires a
// tombstone-and-recreate of the unit.
func loftspaceOwnershipVertexDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     loftspaceOwnershipDDL,
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"AssignUnitOwner", "RemoveUnitOwner"},
		Description: "LoftSpace unit-ownership DDL. Owns AssignUnitOwner + RemoveUnitOwner, which write / tombstone the " +
			"landlord→unit management LINK lnk.identity.<landlordID>.manages.unit.<unitID> (class \"manages\", reads as " +
			"\"landlord manages unit\"; source = the identity, target = the unit). This is the ownership relationship the " +
			"cap-read.residence grant lens projects so a landlord reads only their own units' lease applications (D1.3). " +
			"AssignUnitOwner validates the landlord is an alive vtx.identity and the unit an alive vtx.unit of " +
			"class=location (both listed in ContextHint.Reads), then reads the deterministic per-pair link key ON DEMAND " +
			"(kv.Read) and creates it (absent), revives it via CAS (tombstoned by a prior RemoveUnitOwner), or no-ops " +
			"(already live — idempotent at-least-once). RemoveUnitOwner tombstones the same link (idempotent: absent / " +
			"already-tombstoned → clean no-op), the reversible complement so an ownership transfer needs no " +
			"tombstone-and-recreate. The link needs no link-type DDL (it resolves to the step-6 permissive default). " +
			"This package introduces no vertex type; it contributes the management link on top of identity-domain's " +
			"identity and location-domain's unit. Both ops DEFAULT-DENY on their actor: enforce_manages exempts the " +
			"operator role alone (resolved from the graph, not from a validated-target bit), and requires every other " +
			"actor to already hold a live manages link to the payload unit — so management is only ever conferred by " +
			"someone who already holds it and can never be bootstrapped onto an unheld unit. Off the operator role " +
			"RemoveUnitOwner additionally requires payload.landlord == op.actor, so a co-manager cannot unseat a peer.",
		Script: loftspaceOwnershipDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"landlord":{"type":"string","description":"vtx.identity.<NanoID> of the landlord / property-manager identity (required; validated alive). Listed in ContextHint.Reads."},` +
			`"unit":{"type":"string","description":"vtx.unit.<NanoID> of an existing location unit (required; validated alive + class=location). Listed in ContextHint.Reads."}},` +
			`"required":["landlord","unit"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"The management link key written (lnk.identity.<landlordID>.manages.unit.<unitID>) on create / revive / tombstone; omitted on an idempotent no-op."}}}`,
		FieldDescription: map[string]string{
			"landlord": "Full vtx.identity.<NanoID> key of the landlord / property-manager. AssignUnitOwner validates it is alive and uses it as the management link's source; RemoveUnitOwner reconstructs the link key from it. The caller MUST list this key in ContextHint.Reads (AssignUnitOwner).",
			"unit":     "Full vtx.unit.<NanoID> key of the location-domain unit being managed. AssignUnitOwner validates it is alive + class=location and uses it as the management link's target. The caller MUST list this key in ContextHint.Reads (AssignUnitOwner).",
		},
		Examples: []pkgmgr.ExampleSpec{
			{
				Name: "AssignUnitOwner — record that a landlord manages a unit",
				Payload: map[string]any{
					"landlord": "vtx.identity.<landlordNanoID>",
					"unit":     "vtx.unit.<unitNanoID>",
				},
				ExpectedOutcome: "Validates the landlord is an alive identity and the unit an alive class=location unit, then " +
					"reads lnk.identity.<landlordNanoID>.manages.unit.<unitNanoID> on demand and creates it (class \"manages\"). " +
					"Returns primaryKey (the link). Re-running is idempotent: already-live → clean no-op (empty response); a link " +
					"a prior RemoveUnitOwner tombstoned is revived (CAS-guarded). Rejects a non-identity landlord, a non-unit / " +
					"non-location / dead target.",
			},
			{
				Name: "RemoveUnitOwner — revoke a landlord's management of a unit",
				Payload: map[string]any{
					"landlord": "vtx.identity.<landlordNanoID>",
					"unit":     "vtx.unit.<unitNanoID>",
				},
				ExpectedOutcome: "Reconstructs the management link key, reads it on demand, and tombstones it (frees the pair). " +
					"Idempotent: an absent / already-tombstoned link → clean no-op (empty response). Does not require the unit " +
					"to be alive (an ownership revoke on a retired unit is valid).",
			},
		},
	}
}

// loftspaceOwnershipDDLScript handles AssignUnitOwner + RemoveUnitOwner. The
// landlord + unit are validated by the keys the caller lists in
// ContextHint.Reads (AssignUnitOwner); the per-pair management link is read ON
// DEMAND (kv.Read) — it may not exist yet, so a declared read would
// HydrationMiss on first touch, deferred past hydration (the appliedToUnit-guard
// idiom). No prefix scans.
const loftspaceOwnershipDDLScript = `
def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def make_link_revive_occ(key, source, target, cls, local_name, expected_revision):
    # Revive a soft-deleted management link (isDeleted=True -> False), CAS-guarded
    # on its tombstone revision. A blind make_link (op:create) would COLLIDE with
    # the existing tombstone key, so a re-assign after a RemoveUnitOwner must
    # revive, not create. The CAS serializes two concurrent re-assigns: both
    # snapshot the same revision, both update, the second RevisionConflicts (fail
    # closed, never a silent duplicate).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}},
            "expectedRevision": expected_revision}

def make_link_tombstone(key, source, target, cls, local_name):
    # Soft-delete the management link (isDeleted=True). UNCONDITIONED — a remove is
    # the authority that the ownership is gone; a concurrent remove tombstones to
    # the same state (idempotent).
    return {"op": "update", "key": key,
            "document": {"class": cls, "isDeleted": True,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": {}}}

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

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

ROLE_PAGE_LIMIT = 50
MAX_ROLE_PAGES = 4

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

def enforce_manages(unit_id, what):
    # The ownership probe for the two ops that CONFER management, and the reason
    # either could ever be granted outside the operator role. It DEFAULT-DENIES:
    # every actor but an operator must already hold a live manages link to the
    # unit, so management is only ever handed out by someone who already holds it
    # and can never be bootstrapped onto a unit the actor does not hold. Returns
    # whether it actually bound, so a caller can layer a further rule on the same
    # non-operator population without walking the roles twice.
    #
    # The exemption is the operator ROLE, and deliberately NOT the absence of a
    # platform-validated authContext target. Exempting on that bit would read as
    # "only the operator gets here", which is false twice over
    # (internal/processor/operation_context.go): the service path authorizes
    # without ever inspecting a target, and a task grant whose scopedTo vertex
    # was tombstoned projects an empty target that matches an empty authContext
    # and authorizes with the bit still unset. Either would walk straight through
    # an exemption written that way. Conferring management is the one authority
    # that must never be reachable without already holding it, so the only escape
    # is the role the platform itself seeds.
    if actor_holds_operator(op.actor):
        return False
    _, actor_id = parts_of(op.actor, "actor", "identity")
    # read-posture: (d) declared optionalReads at AssignUnitOwner /
    # RemoveUnitOwner dispatch -- a DIFFERENT key from the payload pair's link
    # whenever the actor is conferring on someone else, so a dispatcher declares
    # both. optionalReads, not reads: absence IS the denial this probe exists to
    # produce, so hydrating it as required would turn every unauthorized call
    # into a HydrationMiss before the guard could answer.
    lnk = kv.Read("lnk.identity." + actor_id + ".manages.unit." + unit_id)
    if lnk == None or lnk.isDeleted:
        # The unit is deliberately NOT named: a caller who manages nothing must
        # not learn from the denial whether that unit exists.
        fail("AuthDenied: " + op.actor + " does not manage the unit this write is for; " + what)
    return True

def vertex_alive(state, key):
    if key not in state or state[key] == None:
        return False
    doc = state[key]
    if hasattr(doc, "isDeleted") and doc.isDeleted:
        return False
    return True

def class_of(state, key):
    if key not in state:
        return None
    doc = state[key]
    if doc == None or not hasattr(doc, "class"):
        return None
    return getattr(doc, "class")

def require_live_unit(state, key):
    # The unit MUST be alive AND class=location (location-domain's unit). A dead or
    # non-location vertex never receives a management link.
    if not vertex_alive(state, key):
        fail("UnknownUnit: unit: " + key + " is absent or tombstoned")
    cls = class_of(state, key)
    if cls != "location":
        fail("NotAUnit: unit: " + key + " has class " + str(cls) + ", required location")

def execute(state, op):
    ot = op.operationType
    p = op.payload

    if ot == "AssignUnitOwner":
        landlord = required_string(p, "landlord")
        _, landlord_id = parts_of(landlord, "landlord", "identity")
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")

        # The probe answers before the alive checks, or a caller who manages
        # nothing could use this op to learn whether a unit exists.
        enforce_manages(unit_id, "cannot confer management of " + unit)

        # No-orphan invariant (FR29 / P4): both endpoints MUST be alive. The
        # landlord is alive-checked (so the caller lists it in Reads); the unit is
        # alive + class=location.
        if not vertex_alive(state, landlord):
            fail("UnknownLandlord: " + landlord + " is absent or tombstoned")
        require_live_unit(state, unit)

        # Deterministic per-(landlord, unit) management link. Read it ON DEMAND
        # (kv.Read) — it may not exist yet, so it's a declared optionalReads,
        # never a required reads (which would HydrationMiss on a fresh pair).
        link_key = "lnk.identity." + landlord_id + ".manages.unit." + unit_id
        # read-posture: (d) declared optionalReads at AssignUnitOwner dispatch
        # (create/revive idempotency branch).
        existing = kv.Read(link_key)
        if existing != None and not existing.isDeleted:
            # Already managed (idempotent at-least-once re-dispatch): emit nothing.
            # An empty response omits primaryKey — the reply-constraint requires a
            # non-empty primaryKey to be a committed mutation key, and a no-op
            # commits none.
            return {"mutations": [], "events": [], "response": {}}
        if existing != None:
            # Tombstoned by a prior RemoveUnitOwner -> revive via CAS (a blind
            # create would collide with the tombstone key).
            link_mut = make_link_revive_occ(link_key, landlord, unit, "manages", "manages", existing.revision)
        else:
            link_mut = make_link(link_key, landlord, unit, "manages", "manages", {})

        events = [{"class": "loftspace.unitOwnerAssigned",
                   "data": {"landlord": landlord, "unit": unit}}]
        return {"mutations": [link_mut], "events": events,
                "response": {"primaryKey": link_key}}

    if ot == "RemoveUnitOwner":
        landlord = required_string(p, "landlord")
        _, landlord_id = parts_of(landlord, "landlord", "identity")
        unit = required_string(p, "unit")
        _, unit_id = parts_of(unit, "unit", "unit")

        # A revoker must already hold the unit, and off the operator role may
        # drop only their OWN management: manages is a flat set with no primary,
        # so a symmetric revoke would let whoever was delegated management last
        # remove everyone who came before.
        if enforce_manages(unit_id, "cannot revoke management of " + unit) and landlord != op.actor:
            fail("AuthDenied: " + op.actor + " may revoke only its own management of this unit")

        link_key = "lnk.identity." + landlord_id + ".manages.unit." + unit_id
        # read-posture: (d) declared optionalReads at RemoveUnitOwner dispatch
        # (revoke idempotency branch).
        existing = kv.Read(link_key)
        if existing == None or existing.isDeleted:
            # Nothing to revoke (idempotent): clean no-op, empty response.
            return {"mutations": [], "events": [], "response": {}}

        link_mut = make_link_tombstone(link_key, landlord, unit, "manages", "manages")
        events = [{"class": "loftspace.unitOwnerRemoved",
                   "data": {"landlord": landlord, "unit": unit}}]
        return {"mutations": [link_mut], "events": events,
                "response": {"primaryKey": link_key}}

    fail("loftspaceOwnership DDL: unknown operationType: " + ot)
`
