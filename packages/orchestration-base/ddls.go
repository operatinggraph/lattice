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
			PermittedCommands: []string{"CreateTask", "ReAssignTask", "CompleteTask", "CancelTask"},
			Description: "Orchestration task DDL. Vertex shape: vtx.task.<NanoID>, class=task, " +
				"root data = scalars only {status (open|complete|cancelled), expiresAt}; NO aspects " +
				"(the UI renders from the bound op's self-describing DDL via the forOperation link). " +
				"Relationships are LINKS: assignedTo (task→identity: who performs it), forOperation " +
				"(task→op-meta: the operation this task grants), scopedTo (task→target: the grant's " +
				"target). All links: task is the later-arriving source, the other vertex is the " +
				"pre-existing target (Contract #1 §1.1). CreateTask requires + validates the assignee " +
				"identity (no-orphan invariant, FR29/P4). Lifecycle ops: ReAssignTask validates the " +
				"new assignee + re-points the assignedTo link atomically (old tombstoned, new created); " +
				"CompleteTask (open→complete) and CancelTask (open→cancelled) transition the root " +
				"data.status. All lifecycle ops require the task to be open, assert the task root " +
				"revision (OCC, Contract #2 §2.6), and reject any other source state (§10.6).",
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

    if ot == "CreateTask":
        assignee = required_string(p, "assignee")
        for_op = required_string(p, "forOperation")
        scoped_to = required_string(p, "scopedTo")
        # Validate + normalize expiresAt to canonical UTC whole-second RFC3339
        # (the same form the Refractor now param uses), so the lens lexical
        # expiresAt > now compare is sound regardless of the caller offset /
        # fractional-second formatting. Malformed input is rejected with a
        # structured ScriptError.
        expires_at = time.rfc3339_utc(required_string(p, "expiresAt"))

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

    if ot == "ReAssignTask":
        task_key = required_string(p, "taskKey")
        new_assignee = required_string(p, "newAssignee")
        _, task_id = parts_of(task_key, "taskKey", "task")
        _, new_assignee_id = parts_of(new_assignee, "newAssignee", "identity")

        # The task root must exist, be alive, and be open. A reassign of a
        # complete/cancelled task is rejected (the link only flips while the
        # task is live).
        if not vertex_alive(state, task_key):
            fail("UnknownTask: " + task_key)
        task_status = root_status(state, task_key)
        if task_status != "open":
            fail("TaskNotOpen: cannot re-assign a " + task_status + " task: " + task_key)

        # No-orphan invariant (FR29 / P4): the NEW assignee identity MUST be
        # alive, or no link flip is committed.
        if not vertex_alive(state, new_assignee):
            fail("UnknownAssignee: " + new_assignee)

        # The caller names the OLD assignedTo link in ContextHint.Reads so the
        # script can re-point it. Locate the single assignedTo link for this
        # task among the hydrated reads.
        old_link = find_assigned_link(state, task_id)
        if old_link == None:
            fail("UnknownAssignedLink: no assignedTo link for " + task_key + " in reads")
        # The old assignee is the link key's final id segment
        # (lnk.task.<taskId>.assignedTo.identity.<oldId>); the script reads it
        # from the key shape, not from a link-endpoint field (the hydrated
        # link struct exposes no targetVertex).
        old_link_parts = split_key(old_link)
        old_target = "vtx.identity." + old_link_parts[len(old_link_parts) - 1]
        if old_target == new_assignee:
            fail("NoOpReassign: task already assigned to " + new_assignee)

        new_link = "lnk.task." + task_id + ".assignedTo.identity." + new_assignee_id

        # OCC on the task root: assert its read revision so a concurrent
        # transition cannot clobber. The root vertex is NOT re-created; the
        # link flip is the effect. The root takes an OCC-guarded touch so the
        # reassign serialises against complete/cancel on the same root.
        mutations = [
            {"op": "update", "key": task_key,
             "expectedRevision": state[task_key].revision,
             "document": {"class": "task", "isDeleted": False,
                          "data": {"status": "open",
                                   "expiresAt": root_expires_at(state, task_key)}}},
            {"op": "tombstone", "key": old_link},
            make_link(new_link, task_key, new_assignee, "assignedTo", "assignedTo", {}),
        ]
        events = [{"class": "TaskReAssigned",
                   "data": {"taskKey": task_key, "oldAssignee": old_target,
                            "newAssignee": new_assignee}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": task_key}}

    if ot == "CompleteTask":
        return transition_task(state, p, "complete", "TaskCompleted")

    if ot == "CancelTask":
        return transition_task(state, p, "cancelled", "TaskCancelled")

    fail("task DDL: unknown operationType: " + ot)

def root_status(state, key):
    doc = state[key]
    if not hasattr(doc, "data") or doc.data == None:
        return ""
    if "status" not in doc.data:
        return ""
    return doc.data["status"]

def root_expires_at(state, key):
    doc = state[key]
    if not hasattr(doc, "data") or doc.data == None:
        return ""
    if "expiresAt" not in doc.data:
        return ""
    return doc.data["expiresAt"]

def find_assigned_link(state, task_id):
    want_prefix = "lnk.task." + task_id + ".assignedTo.identity."
    for k in state:
        if k.startswith(want_prefix):
            doc = state[k]
            if hasattr(doc, "isDeleted") and doc.isDeleted:
                continue
            return k
    return None

def transition_task(state, p, target_status, event_class):
    # CompleteTask (open -> complete) and CancelTask (open -> cancelled) share
    # this validated-transition body. Only an OPEN task transitions; any other
    # source state is rejected with a structured ScriptError (the named AC
    # invariant: cannot complete a cancelled task, and its symmetric guard).
    task_key = required_string(p, "taskKey")
    parts_of(task_key, "taskKey", "task")
    if not vertex_alive(state, task_key):
        fail("UnknownTask: " + task_key)
    status = root_status(state, task_key)
    if status == target_status:
        fail("TaskAlreadyInState: task " + task_key + " is already " + target_status)
    if status != "open":
        fail("InvalidTransition: cannot move task " + task_key + " from " + status + " to " + target_status)
    mutations = [
        {"op": "update", "key": task_key,
         "expectedRevision": state[task_key].revision,
         "document": {"class": "task", "isDeleted": False,
                      "data": {"status": target_status,
                               "expiresAt": root_expires_at(state, task_key)}}},
    ]
    events = [{"class": event_class, "data": {"taskKey": task_key}}]
    return {"mutations": mutations, "events": events,
            "response": {"primaryKey": task_key}}
`
