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
			"identity and location-domain's unit.",
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
// HydrationMiss (the appliedToUnit-guard idiom). No prefix scans.
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

def vertex_parts(key, name, want_type):
    # Parse key as vtx.<want_type>.<NanoID>: exactly 3 segments, the type segment
    # fixed. A non-3-segment key (aspect/link) or wrong type is rejected.
    parts = key.split(".")
    if len(parts) != 3 or parts[0] != "vtx" or parts[1] != want_type:
        fail("InvalidArgument: " + name + ": required vtx." + want_type + ".<NanoID> (exactly 3 segments); got " + key)
    if parts[2] == "":
        fail("InvalidArgument: " + name + ": empty id segment; required vtx." + want_type + ".<NanoID>; got " + key)
    return parts[2]

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
        landlord_id = vertex_parts(landlord, "landlord", "identity")
        unit = required_string(p, "unit")
        unit_id = vertex_parts(unit, "unit", "unit")

        # No-orphan invariant (FR29 / P4): both endpoints MUST be alive. The
        # landlord is alive-checked (so the caller lists it in Reads); the unit is
        # alive + class=location.
        if not vertex_alive(state, landlord):
            fail("UnknownLandlord: " + landlord + " is absent or tombstoned")
        require_live_unit(state, unit)

        # Deterministic per-(landlord, unit) management link. Read it ON DEMAND
        # (kv.Read) — it may not exist yet, so it's a declared optionalReads,
        # never a required reads (which would HydrationMiss on a fresh pair).
        # read-posture: (d) declared optionalReads at AssignUnitOwner dispatch
        # (create/revive idempotency branch).
        link_key = "lnk.identity." + landlord_id + ".manages.unit." + unit_id
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
        landlord_id = vertex_parts(landlord, "landlord", "identity")
        unit = required_string(p, "unit")
        unit_id = vertex_parts(unit, "unit", "unit")

        # read-posture: (d) declared optionalReads at RemoveUnitOwner dispatch
        # (revoke idempotency branch).
        link_key = "lnk.identity." + landlord_id + ".manages.unit." + unit_id
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
