package orchestrationbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `task` (vertex-type class) handles the CreateTask operation.
//
// Architectural rules (binding — same known-key discipline as rbac-domain /
// identity-domain):
//
//   - The script reads ONLY by known key. No prefix scans, no adjacency
//     lookups, no lens-output reads. CreateTask validates its link
//     endpoints (assignee identity, forOperation op-meta, scopedTo target)
//     by reading each by its known key — exactly the keys the caller lists
//     in ContextHint.Reads.
//   - No-orphan invariant (FR29 / P4): CreateTask REQUIRES an `assignee`
//     and rejects (structured ScriptError) if the assignee identity is
//     absent/invalid — a task pointing at a non-existent identity is never
//     committed. forOperation + scopedTo endpoints are validated likewise.
//
// Task shape (Contract #10 §10.1 — scalars + links only, NO aspects):
//
//	vtx.task.<id>   root data = { status, expiresAt }   status ∈ {open, complete, cancelled}
//	lnk.task.<id>.assignedTo.identity.<assigneeId>   # who performs it
//	lnk.task.<id>.forOperation.meta.<opId>           # the op this task grants
//	lnk.task.<id>.scopedTo.<type>.<targetId>         # the grant's target (often ≠ assignee)
//
// All three links: task = the later-arriving SOURCE, the other vertex
// pre-exists = the TARGET (Contract #1 §1.1). The operationType the task
// grants is LINK-sourced via forOperation→op by the capabilityEphemeral
// lens — it is NOT a task.data.grantedOperationType field (the corrected
// anti-pattern, Contract #10 §10.1).
//
// Caller's ContextHint.Reads MUST include, for CreateTask:
//   - assignee   (vtx.identity.<id>)
//   - forOperation (vtx.meta.<opId>)
//   - scopedTo   (vtx.<type>.<targetId>)
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		{
			CanonicalName:     "task",
			Class:             "meta.ddl.vertexType",
			PermittedCommands: []string{"CreateTask"},
			Description: "Orchestration task DDL. Vertex shape: vtx.task.<NanoID>, class=task, " +
				"root data = scalars only {status (open|complete|cancelled), expiresAt}; NO aspects " +
				"(the UI renders from the bound op's self-describing DDL via the forOperation link). " +
				"Relationships are LINKS: assignedTo (task→identity: who performs it), forOperation " +
				"(task→op-meta: the operation this task grants), scopedTo (task→target: the grant's " +
				"target). All links: task is the later-arriving source, the other vertex is the " +
				"pre-existing target (Contract #1 §1.1). CreateTask requires + validates the assignee " +
				"identity (no-orphan invariant, FR29/P4).",
			Script: taskDDLScript,
			InputSchema: `{"type":"object","properties":` +
				`{"assignee":{"type":"string","description":"vtx.identity.<NanoID> — the identity that will perform the task (required; validated)."},` +
				`"forOperation":{"type":"string","description":"vtx.meta.<NanoID> — the operation meta-vertex this task grants the assignee permission to perform."},` +
				`"scopedTo":{"type":"string","description":"vtx.<type>.<NanoID> — the specific target the granted operation is scoped to (often ≠ the assignee)."},` +
				`"expiresAt":{"type":"string","description":"RFC3339 expiry timestamp; the grant is valid only while expiresAt > now."}},` +
				`"required":["assignee","forOperation","scopedTo","expiresAt"]}`,
			OutputSchema: `{"type":"object","properties":` +
				`{"primaryKey":{"type":"string","description":"vtx.task.<NanoID> of the created task (the operation's principal key)."}}}`,
			FieldDescription: map[string]string{
				"assignee":     "Full vtx.identity.<NanoID> key of the identity that will perform this task. Required; CreateTask rejects if absent/invalid.",
				"forOperation": "Full vtx.meta.<NanoID> key of the operation meta-vertex the task grants. The capabilityEphemeral lens link-sources the granted operationType from this op.",
				"scopedTo":     "Full vtx.<type>.<NanoID> key of the specific entity the granted operation is scoped to (e.g. the lease application to approve).",
				"expiresAt":    "RFC3339 timestamp after which the task no longer grants. Stored as a scalar on the task root data.",
			},
			Examples: []pkgmgr.ExampleSpec{
				{
					Name: "CreateTask — assign a lease-approval task to a manager",
					Payload: map[string]any{
						"assignee":     "vtx.identity.<managerNanoID>",
						"forOperation": "vtx.meta.<approveLeaseApplicationOpNanoID>",
						"scopedTo":     "vtx.leaseApp.<applicantNanoID>",
						"expiresAt":    "2026-06-04T14:00:00Z",
					},
					ExpectedOutcome: "Validates the assignee identity, forOperation op-meta, and scopedTo target all exist. " +
						"Atomically commits vtx.task.<NanoID> (status=open, expiresAt) + the assignedTo/forOperation/scopedTo " +
						"links in one batch. Returns primaryKey (the task key). Rejects with ScriptError if any endpoint is absent.",
				},
			},
		},
	}
}

// taskDDLScript handles CreateTask. Known-key reads only (validates the
// three link endpoints by the keys the caller listed in ContextHint.Reads).
// No-orphan by construction: the assignee identity (and the other two
// endpoints) must be alive or the op is rejected.
const taskDDLScript = `
def make_vtx(key, cls, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False, "data": data}}

def make_link(key, source, target, cls, local_name, data):
    return {"op": "create", "key": key,
            "document": {"class": cls, "isDeleted": False,
                         "sourceVertex": source, "targetVertex": target,
                         "localName": local_name, "data": data}}

def split_key(k):
    return k.split(".")

def required_string(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: " + name + ": required non-empty string")
    return v.strip()

def parts_of(key, name, want_type):
    parts = split_key(key)
    if len(parts) < 3 or parts[0] != "vtx":
        fail("InvalidArgument: " + name + ": required vtx.<type>.<NanoID>; got " + key)
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

    if ot == "CreateTask":
        assignee = required_string(p, "assignee")
        for_op = required_string(p, "forOperation")
        scoped_to = required_string(p, "scopedTo")
        expires_at = required_string(p, "expiresAt")

        # Validate endpoint key shapes.
        _, assignee_id = parts_of(assignee, "assignee", "identity")
        _, op_id = parts_of(for_op, "forOperation", "meta")
        scoped_type, scoped_id = parts_of(scoped_to, "scopedTo", "")

        # No-orphan invariant (FR29 / P4): the assignee identity MUST exist
        # and be alive. A task pointing at a non-existent identity is never
        # committed.
        if not vertex_alive(state, assignee):
            fail("UnknownAssignee: " + assignee)
        # Validate the other two link endpoints (match the package idiom:
        # rbac-domain validates both endpoints of every link it writes).
        if not vertex_alive(state, for_op):
            fail("UnknownOperation: " + for_op)
        if not vertex_alive(state, scoped_to):
            fail("UnknownTarget: " + scoped_to)

        task_id = nanoid.new()
        task_key = "vtx.task." + task_id

        assigned_lnk = "lnk.task." + task_id + ".assignedTo.identity." + assignee_id
        forop_lnk = "lnk.task." + task_id + ".forOperation.meta." + op_id
        scoped_lnk = "lnk.task." + task_id + ".scopedTo." + scoped_type + "." + scoped_id

        mutations = [
            make_vtx(task_key, "task", {"status": "open", "expiresAt": expires_at}),
            make_link(assigned_lnk, task_key, assignee, "assignedTo", "assignedTo", {}),
            make_link(forop_lnk, task_key, for_op, "forOperation", "forOperation", {}),
            make_link(scoped_lnk, task_key, scoped_to, "scopedTo", "scopedTo", {}),
        ]
        events = [{"class": "TaskCreated",
                   "data": {"taskKey": task_key, "assignee": assignee,
                            "forOperation": for_op, "scopedTo": scoped_to,
                            "expiresAt": expires_at}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": task_key}}

    fail("task DDL: unknown operationType: " + ot)
`
