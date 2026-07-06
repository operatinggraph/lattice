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
//   - The script reads ONLY by known key, with one sanctioned exception:
//     TombstoneRole enumerates the role's inbound `queuedFor` links via
//     the bounded `kv.Links` primitive (Contract #2 §2.5.1) to enforce
//     Contract #10 §10.1's no-orphan tombstone guard — a role holding a
//     live queued task is rejected (`RoleHasOpenTasks`), not silently
//     orphaned. This is the same enumeration idiom clinic-domain's
//     assert_no_overlap uses; it is not a keyspace scan (bounded to the
//     role's own degree, per §2.5.1).
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
			CanonicalName: "rbac",
			Class:         "meta.ddl.vertexType",
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
				"the link's canonical name. TombstoneRole rejects (RoleHasOpenTasks) a role " +
				"that still holds a live queuedFor task (Contract #10 §10.1 no-orphan " +
				"tombstone guard), enumerated via the bounded kv.Links primitive.",
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
				`{"primaryKey":{"type":"string","description":"The principal Core KV key the operation wrote: the role/permission vertex key for create/update/tombstone ops, or the holdsRole/grantedBy link key for assign/grant ops. Absent on idempotent no-op replays (nothing committed)."}}}`,
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
						"writes .canonicalName=consumer and .description aspects. Returns primaryKey (the role key).",
				},
				{
					Name:    "AssignRole — assign consumer role to an identity",
					Payload: map[string]any{"actorKey": "vtx.identity.<actorNanoID>", "roleKey": "vtx.role.<roleNanoID>"},
					ExpectedOutcome: "Writes lnk.identity.<actorNanoID>.holdsRole.role.<roleNanoID> with class=holdsRole. " +
						"Returns primaryKey (the link key). Idempotent: a replay where the link already exists alive " +
						"commits nothing and omits primaryKey.",
				},
				{
					Name:            "GrantPermission — grant CreateRole permission to operator role",
					Payload:         map[string]any{"permKey": "vtx.permission.<permNanoID>", "roleKey": "vtx.role.<operatorNanoID>"},
					ExpectedOutcome: "Writes lnk.permission.<permNanoID>.grantedBy.role.<operatorNanoID> with class=grantedBy. Returns primaryKey (the link key).",
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

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
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

ROLE_TASK_PAGE_LIMIT = 256
MAX_ROLE_TASK_PAGES = 64

def role_has_open_tasks(role_key):
    # Contract #10 §10.1 no-orphan tombstone guard: a role holding a live
    # queuedFor task is rejected, not silently orphaned (operator
    # reassigns/cancels the task first). Enumerated via the sanctioned
    # bounded kv.Links (Contract #2 §2.5.1), direction "in" -- the role is
    # the queuedFor link's TARGET (task is source, per Contract #1 §1.1).
    # A live LINK alone does not mean an open task: CompleteTask/CancelTask
    # never tombstone the assignedTo/queuedFor link (orchestration-base
    # leaves it live post-transition), so each candidate's source task
    # vertex is read and only a still-"open" task blocks.
    cursor = None
    for _page in range(MAX_ROLE_TASK_PAGES):
        # read-posture: (e) relation=queuedFor epoch=none (read-only guard:
        # a task queued concurrently with the tombstone slips past — accepted;
        # Weaver detect+recover is the orphan-task enforcer)
        links, cursor = kv.Links(role_key, "queuedFor", "in", cursor, ROLE_TASK_PAGE_LIMIT)
        for lk in links:
            if lk.isDeleted:
                continue
            # read-posture: (e) per-candidate follow-up read off the
            # enumeration above (data-derived key)
            task = kv.Read(lk.sourceVertex)
            if task == None or task.isDeleted:
                continue
            if task.data.get("status") == "open":
                fail("RoleHasOpenTasks: " + role_key + " still has open task " + lk.sourceVertex + " queued; reassign or cancel it first")
        if cursor == None:
            return
    fail("RoleTaskFanoutTooLarge: " + role_key + " has too many queuedFor links to enumerate at tombstone time; reassign/cancel enough to bring it under the page cap first")

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
        events = [{"class": "rbac.roleCreated", "data": {"roleKey": role_key, "name": name}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": role_key}}

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
        events = [{"class": "rbac.roleUpdated", "data": {"roleKey": role_key}}]
        # UpdateRole mutates only the .description aspect (not the vertex).
        # primaryKey names the principal entity (the role); the Processor accepts
        # it as the 3-segment root of the committed aspect.
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": role_key}}

    if ot == "TombstoneRole":
        role_key = required_string(p, "roleKey")
        _ = role_id_from_key(role_key)
        if not vertex_alive(state, role_key):
            fail("UnknownRole: " + role_key)
        role_has_open_tasks(role_key)
        mutations = [make_tombstone(role_key)]
        events = [{"class": "rbac.roleTombstoned", "data": {"roleKey": role_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": role_key}}

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
        events = [{"class": "rbac.permissionCreated",
                   "data": {"permissionKey": perm_key, "operationType": opt}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": perm_key}}

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
        events = [{"class": "rbac.permissionUpdated", "data": {"permissionKey": perm_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": perm_key}}

    if ot == "TombstonePermission":
        perm_key = required_string(p, "permKey")
        _ = perm_id_from_key(perm_key)
        if not vertex_alive(state, perm_key):
            fail("UnknownPermission: " + perm_key)
        mutations = [make_tombstone(perm_key)]
        events = [{"class": "rbac.permissionTombstoned", "data": {"permissionKey": perm_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": perm_key}}

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
        # Idempotent assign: if link exists alive, return ok no-op. No
        # mutations means no committed key, so no primaryKey is returned
        # (the link key is deterministic and already known to the caller).
        existing = state[lnk_key] if lnk_key in state else None
        if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
            return {"mutations": [], "events": []}
        mutations = [make_link(lnk_key, actor_key, role_key, "holdsRole", "holdsRole", {})]
        events = [{"class": "rbac.roleAssigned",
                   "data": {"actorKey": actor_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": lnk_key}}

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
        events = [{"class": "rbac.roleRevoked",
                   "data": {"actorKey": actor_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": lnk_key}}

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
        # Idempotent grant: if link exists alive, return ok no-op. No
        # mutations means no committed key, so no primaryKey is returned.
        existing = state[lnk_key] if lnk_key in state else None
        if existing != None and not (hasattr(existing, "isDeleted") and existing.isDeleted):
            return {"mutations": [], "events": []}
        mutations = [make_link(lnk_key, perm_key, role_key, "grantedBy", "grantedBy", {})]
        events = [{"class": "rbac.permissionGranted",
                   "data": {"permissionKey": perm_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": lnk_key}}

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
        events = [{"class": "rbac.permissionRevoked",
                   "data": {"permissionKey": perm_key, "roleKey": role_key, "linkKey": lnk_key}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": lnk_key}}

    fail("rbac DDL: unknown operationType: " + ot)
`
