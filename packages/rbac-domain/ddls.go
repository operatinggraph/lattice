package rbacdomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `rbac` handles all 10 operations:
//
//	CreateRole, UpdateRole, TombstoneRole
//	CreatePermission, UpdatePermission, TombstonePermission
//	AssignRole, RevokeRole
//	GrantPermission, RevokePermission
//
// Architectural rules (binding — same as identity-hygiene):
//
//   - The script reads ONLY by known key. No prefix scans, no
//     adjacency lookups, no lens-output reads.
//   - For AssignRole / GrantPermission and their inverses, the *link*
//     key is deterministic from the input keys; we read it by known key
//     to verify pre-existence (idempotent) or absence (so we don't
//     create twice). No "list all grants for role" enumeration.
//   - Canonical-name uniqueness for new role/permission vertices is NOT
//     enforced here — that's an upstream concern. The script assigns a
//     fresh NanoID and writes; duplicate-name gates are the caller's
//     responsibility.
//
// Caller's ContextHint.Reads MUST include:
//   - CreateRole / CreatePermission: nothing (script generates the new NanoID)
//   - UpdateRole / UpdatePermission: the existing vertex key
//   - TombstoneRole / TombstonePermission: the existing vertex key
//   - AssignRole / RevokeRole: actorKey, roleKey, and the deterministic
//     holdsRole link key (computed from actorKey + roleKey by the caller
//     — see comments below for the shape)
//   - GrantPermission / RevokePermission: permKey, roleKey, and the
//     deterministic grantedBy link key
//
// Link key shapes:
//
//	lnk.<actorType>.<actorId>.holdsRole.role.<roleId>
//	lnk.permission.<permId>.grantedBy.role.<roleId>
//
// The DDL .description aspect documents direction for FR19 self-description.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName:     "rbac",
			Class:             "meta.ddl.vertexType",
			PermittedCommands: []string{
				"CreateRole", "UpdateRole", "TombstoneRole",
				"CreatePermission", "UpdatePermission", "TombstonePermission",
				"AssignRole", "RevokeRole",
				"GrantPermission", "RevokePermission",
			},
			Description: "RBAC DDL. Manages role + permission vertices and the holdsRole + " +
				"grantedBy links. holdsRole link direction: actor -> role (actor is the " +
				"later-added vertex in typical graph growth). grantedBy link direction: " +
				"permission -> role (permission is the later-added vertex; reads as " +
				"'permission granted by role'). The operation verbs GrantPermission / " +
				"RevokePermission follow operator-action semantics and are distinct from " +
				"the link's canonical name.",
			Script: rbacDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"name":{"type":"string","description":"Role canonical name (CreateRole)."},` +
				`"description":{"type":"string","description":"Human-readable role or permission description."},` +
				`"roleKey":{"type":"string","description":"vtx.role.<NanoID> — target role (UpdateRole/TombstoneRole/AssignRole/RevokeRole/GrantPermission/RevokePermission)."},` +
				`"operationType":{"type":"string","description":"The operationType this permission gates (CreatePermission/UpdatePermission)."},` +
				`"scope":{"type":"string","enum":["any","self"],"description":"Permission scope (CreatePermission/UpdatePermission). Defaults to any."},` +
				`"note":{"type":"string","description":"Optional human-readable note for the permission vertex."},` +
				`"permKey":{"type":"string","description":"vtx.permission.<NanoID> — target permission (UpdatePermission/TombstonePermission/GrantPermission/RevokePermission)."},` +
				`"actorKey":{"type":"string","description":"vtx.<type>.<NanoID> — the actor to assign/revoke a role for (AssignRole/RevokeRole)."}}}`,
			OutputSchema: `{"type":"object","properties":` +
				`{"roleKey":{"type":"string","description":"vtx.role.<NanoID> of the created/updated/tombstoned role."},` +
				`"permissionKey":{"type":"string","description":"vtx.permission.<NanoID> of the created/updated/tombstoned permission."},` +
				`"linkKey":{"type":"string","description":"Link key written for holdsRole or grantedBy operations."},` +
				`"alreadyAssigned":{"type":"boolean","description":"True if AssignRole was a no-op (link already existed alive)."},` +
				`"alreadyGranted":{"type":"boolean","description":"True if GrantPermission was a no-op (link already existed alive)."}}}`,
			FieldDescription: map[string]string{
				"name":          "Canonical name for the new role. Used in holdsRole link shape and audit queries.",
				"description":   "Optional plain-language description of the role's purpose or permission's semantics.",
				"roleKey":       "Full vtx.role.<NanoID> key of an existing role vertex.",
				"operationType": "The operationType string this permission authorizes (e.g. CreateRole, MergeIdentity).",
				"scope":         "Permission scope: 'any' allows the operation on any target; 'self' restricts to the actor's own resources.",
				"note":          "Optional note stored in the permission vertex's data field.",
				"permKey":       "Full vtx.permission.<NanoID> key of an existing permission vertex.",
				"actorKey":      "Full vtx.<type>.<NanoID> key of the actor receiving or losing a role assignment.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name:    "CreateRole — operator creates a consumer role",
					Payload: map[string]any{"name": "consumer", "description": "Default customer-facing role with read-only access."},
					ExpectedOutcome: "Creates vtx.role.<NanoID> with class=role, " +
						"writes .canonicalName=consumer and .description aspects. Returns roleKey.",
				},
				{
					Name:    "AssignRole — assign consumer role to an identity",
					Payload: map[string]any{"actorKey": "vtx.identity.<actorNanoID>", "roleKey": "vtx.role.<roleNanoID>"},
					ExpectedOutcome: "Writes lnk.identity.<actorNanoID>.holdsRole.role.<roleNanoID> with class=holdsRole. " +
						"Returns linkKey. Idempotent: alreadyAssigned=true if link existed.",
				},
				{
					Name:    "GrantPermission — grant CreateRole permission to operator role",
					Payload: map[string]any{"permKey": "vtx.permission.<permNanoID>", "roleKey": "vtx.role.<operatorNanoID>"},
					ExpectedOutcome: "Writes lnk.permission.<permNanoID>.grantedBy.role.<operatorNanoID> with class=grantedBy. Returns linkKey.",
				},
			},
		},
	}
}

// rbacDDLScript handles all 10 RBAC operations. ~280 LOC target.
//
// Command parameters (op.payload) by op type:
//   - CreateRole          { name, description? }
//   - UpdateRole          { roleKey, description }
//   - TombstoneRole       { roleKey }
//   - CreatePermission    { operationType, scope, note? }
//   - UpdatePermission    { permKey, operationType, scope }
//   - TombstonePermission { permKey }
//   - AssignRole          { actorKey, roleKey }
//   - RevokeRole          { actorKey, roleKey }
//   - GrantPermission     { permKey, roleKey }
//   - RevokePermission    { permKey, roleKey }
const rbacDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_aspect(key, vkey, local_name, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "vertexKey": vkey, "localName": local_name,
                         "isDeleted": False, "data": data}}

def make_update_aspect(key, vkey, local_name, cls, data):
    return {"op": "update", "key": key,
            "document": {"class": cls, "vertexKey": vkey, "localName": local_name,
                         "isDeleted": False, "data": data}}

def make_link(key, younger, older, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "youngerVertex": younger, "olderVertex": older,
                         "localName": local_name, "data": data}}

def make_tombstone(key):
    return {"op": "tombstone", "key": key,
            "document": {"isDeleted": True, "data": {}}}

def split_key(k):
    return k.split(".")

def role_id_from_key(role_key):
    parts = split_key(role_key)
    if len(parts) < 3 or parts[0] != "vtx" or parts[1] != "role":
        fail("InvalidArgument: roleKey: required vtx.role.<NanoID>")
    return parts[2]

def perm_id_from_key(perm_key):
    parts = split_key(perm_key)
    if len(parts) < 3 or parts[0] != "vtx" or parts[1] != "permission":
        fail("InvalidArgument: permKey: required vtx.permission.<NanoID>")
    return parts[2]

def actor_parts(actor_key):
    parts = split_key(actor_key)
    if len(parts) < 3 or parts[0] != "vtx":
        fail("InvalidArgument: actorKey: required vtx.<type>.<NanoID>")
    return parts[1], parts[2]

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

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

    if ot == "CreateRole":
        name = required_string(p, "name")
        desc = ""
        if hasattr(p, "description") and p.description != None and type(p.description) == type(""):
            desc = p.description
        role_id = nanoid.new()
        role_key = "vtx.role." + role_id
        mutations = [
            make_vtx(role_key, "role", {}),
            make_aspect(role_key + ".canonicalName", role_key, "canonicalName", "canonicalName",
                {"value": name}),
            make_aspect(role_key + ".description", role_key, "description", "description",
                {"text": desc}),
        ]
        events = [{"class": "RoleCreated", "data": {"roleKey": role_key, "name": name}}]
        return {"mutations": mutations, "events": events,
                "response": {"roleKey": role_key}}

    if ot == "UpdateRole":
        role_key = required_string(p, "roleKey")
        _ = role_id_from_key(role_key)
        if not vertex_alive(state, role_key):
            fail("UnknownRole: " + role_key)
        desc = ""
        if hasattr(p, "description") and p.description != None and type(p.description) == type(""):
            desc = p.description
        mutations = [
            make_update_aspect(role_key + ".description", role_key, "description", "description",
                {"text": desc}),
        ]
        events = [{"class": "RoleUpdated", "data": {"roleKey": role_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"roleKey": role_key}}

    if ot == "TombstoneRole":
        role_key = required_string(p, "roleKey")
        _ = role_id_from_key(role_key)
        if not vertex_alive(state, role_key):
            fail("UnknownRole: " + role_key)
        mutations = [make_tombstone(role_key)]
        events = [{"class": "RoleTombstoned", "data": {"roleKey": role_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"roleKey": role_key}}

    if ot == "CreatePermission":
        opt = required_string(p, "operationType")
        scope = "any"
        if hasattr(p, "scope") and p.scope != None and type(p.scope) == type("") and len(p.scope) > 0:
            scope = p.scope
        note = ""
        if hasattr(p, "note") and p.note != None and type(p.note) == type(""):
            note = p.note
        perm_id = nanoid.new()
        perm_key = "vtx.permission." + perm_id
        data = {"operationType": opt, "scope": scope}
        if note != "":
            data["note"] = note
        mutations = [make_vtx(perm_key, "permission", data)]
        events = [{"class": "PermissionCreated",
                   "data": {"permissionKey": perm_key, "operationType": opt}}]
        return {"mutations": mutations, "events": events,
                "response": {"permissionKey": perm_key}}

    if ot == "UpdatePermission":
        perm_key = required_string(p, "permKey")
        _ = perm_id_from_key(perm_key)
        if not vertex_alive(state, perm_key):
            fail("UnknownPermission: " + perm_key)
        opt = required_string(p, "operationType")
        scope = "any"
        if hasattr(p, "scope") and p.scope != None and type(p.scope) == type("") and len(p.scope) > 0:
            scope = p.scope
        mutations = [
            {"op": "update", "key": perm_key,
             "document": {"class": "permission", "isDeleted": False,
                          "data": {"operationType": opt, "scope": scope}}},
        ]
        events = [{"class": "PermissionUpdated", "data": {"permissionKey": perm_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"permissionKey": perm_key}}

    if ot == "TombstonePermission":
        perm_key = required_string(p, "permKey")
        _ = perm_id_from_key(perm_key)
        if not vertex_alive(state, perm_key):
            fail("UnknownPermission: " + perm_key)
        mutations = [make_tombstone(perm_key)]
        events = [{"class": "PermissionTombstoned", "data": {"permissionKey": perm_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"permissionKey": perm_key}}

    if ot == "AssignRole":
        actor_key = required_string(p, "actorKey")
        role_key = required_string(p, "roleKey")
        actor_type, actor_id = actor_parts(actor_key)
        role_id = role_id_from_key(role_key)
        if not vertex_alive(state, actor_key):
            fail("UnknownActor: " + actor_key)
        if not vertex_alive(state, role_key):
            fail("UnknownRole: " + role_key)
        lnk_key = "lnk." + actor_type + "." + actor_id + ".holdsRole.role." + role_id
        # Idempotent assign: if link exists alive, return ok no-op.
        existing = state[lnk_key] if lnk_key in state else None
        if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
            return {"mutations": [], "events": [],
                    "response": {"linkKey": lnk_key, "alreadyAssigned": True}}
        mutations = [make_link(lnk_key, actor_key, role_key, "holdsRole", "holdsRole", {})]
        events = [{"class": "RoleAssigned",
                   "data": {"actorKey": actor_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"linkKey": lnk_key}}

    if ot == "RevokeRole":
        actor_key = required_string(p, "actorKey")
        role_key = required_string(p, "roleKey")
        actor_type, actor_id = actor_parts(actor_key)
        role_id = role_id_from_key(role_key)
        lnk_key = "lnk." + actor_type + "." + actor_id + ".holdsRole.role." + role_id
        existing = state[lnk_key] if lnk_key in state else None
        if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
            fail("UnknownLink: " + lnk_key)
        mutations = [make_tombstone(lnk_key)]
        events = [{"class": "RoleRevoked",
                   "data": {"actorKey": actor_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"linkKey": lnk_key}}

    if ot == "GrantPermission":
        perm_key = required_string(p, "permKey")
        role_key = required_string(p, "roleKey")
        perm_id = perm_id_from_key(perm_key)
        role_id = role_id_from_key(role_key)
        if not vertex_alive(state, perm_key):
            fail("UnknownPermission: " + perm_key)
        if not vertex_alive(state, role_key):
            fail("UnknownRole: " + role_key)
        lnk_key = "lnk.permission." + perm_id + ".grantedBy.role." + role_id
        existing = state[lnk_key] if lnk_key in state else None
        if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
            return {"mutations": [], "events": [],
                    "response": {"linkKey": lnk_key, "alreadyGranted": True}}
        mutations = [make_link(lnk_key, perm_key, role_key, "grantedBy", "grantedBy", {})]
        events = [{"class": "PermissionGranted",
                   "data": {"permissionKey": perm_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"linkKey": lnk_key}}

    if ot == "RevokePermission":
        perm_key = required_string(p, "permKey")
        role_key = required_string(p, "roleKey")
        perm_id = perm_id_from_key(perm_key)
        role_id = role_id_from_key(role_key)
        lnk_key = "lnk.permission." + perm_id + ".grantedBy.role." + role_id
        existing = state[lnk_key] if lnk_key in state else None
        if existing == None or (hasattr(existing, "isDeleted") and existing.isDeleted):
            fail("UnknownLink: " + lnk_key)
        mutations = [make_tombstone(lnk_key)]
        events = [{"class": "PermissionRevoked",
                   "data": {"permissionKey": perm_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"linkKey": lnk_key}}

    fail("rbac DDL: unknown operationType: " + ot)
`
