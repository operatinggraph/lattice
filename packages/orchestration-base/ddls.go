package orchestrationbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// DDLs returns the package's DDL meta-vertex declarations.
//
// Single DDL `task` (vertex-type class) handles the CreateTask operation.
//
// Architectural rules (binding — same known-key discipline as rbac-domain /
// identity-domain):
//
//   - The script reads ONLY by known key, or via the one sanctioned bounded
//     enumeration (kv.Links, Contract #2 §2.5.1 — ClaimTask's queuedFor
//     lookup, bounded to a single task's at-most-one link). No prefix
//     scans, no adjacency lookups, no lens-output reads. CreateTask
//     validates its link endpoints (assignee identity and/or queue role,
//     forOperation op-meta, scopedTo target) by reading each by its known
//     key — exactly the keys the caller lists in ContextHint.Reads.
//   - No-orphan invariant (FR29 / P4): CreateTask REQUIRES a resolved
//     assignee-or-queue endpoint and rejects (structured ScriptError,
//     RoutingFailed) if neither resolves to a live vertex — a task pointing
//     at a non-existent identity/role is never committed. forOperation +
//     scopedTo endpoints are validated likewise.
//   - Fire 2 (Contract #10 §10.1): CreateTask's routing additionally gates
//     on the assignee's `availability` aspect via kv.Read — the aspect may
//     legitimately never have been set, so it is NOT a ContextHint.Reads
//     key (a miss there is a fatal HydrationMiss); callers declare it in
//     ContextHint.OptionalReads instead (Contract #2 §2.5 class (d): the
//     read resolves at the step-4 snapshot, absent → None). Absent aspect
//     == available, byte-compatible with pre-Fire-2 callers; an undeclared
//     caller still works via the lazy on-demand fallback. A given+alive but
//     unavailable assignee falls back to a given+alive queue.
//
// Task shape (Contract #10 §10.1 — scalars + links only, NO aspects):
//
//	vtx.task.<id>   root data = { status, expiresAt }   status ∈ {open, complete, cancelled}
//	lnk.task.<id>.assignedTo.identity.<assigneeId>   # direct push assignment (who performs it)
//	lnk.task.<id>.queuedFor.role.<roleId>            # FR28 role-queue pull assignment
//	lnk.task.<id>.forOperation.meta.<opId>           # the op this task grants
//	lnk.task.<id>.scopedTo.<type>.<targetId>         # the grant's target (often ≠ assignee)
//	vtx.identity.<id>.availability   data = { available: bool }   # Fire 2 routing input (SetAvailability)
//
// Exactly ONE of assignedTo/queuedFor is present on an open task (FR28,
// Contract #10 §10.1). All links: task = the later-arriving SOURCE, the
// other vertex pre-exists = the TARGET (Contract #1 §1.1). The operationType
// the task grants is LINK-sourced via forOperation->op by the
// capabilityEphemeral lens — it is NOT a task.data.grantedOperationType
// field (the corrected anti-pattern, Contract #10 §10.1).
//
// Caller's ContextHint.Reads MUST include, for CreateTask:
//   - assignee and/or queue (vtx.identity.<id> / vtx.role.<id>)
//   - forOperation (vtx.meta.<opId>)
//   - scopedTo   (vtx.<type>.<targetId>)
//
// ClaimTask needs only `taskKey` declared: the queuedFor link is resolved
// via the sanctioned bounded kv.Links enumeration (Contract #2 §2.5.1, an
// open task carries at most one), and the holdsRole / speculative
// assignedTo checks are on-demand kv.Read (may legitimately be absent).
//
// SetAvailability needs only `identity` declared (the aspect it writes is
// not itself a read). CreateTask's absence-tolerant kv.Read keys — the task
// dedup key and the assignee's `.availability` aspect — belong in
// ContextHint.OptionalReads (§2.5 class (d)), never in Reads: both may
// legitimately not exist, and a declared-but-absent Reads key faults. The
// engine dispatchers declare them (Loom userTaskOptionalReads, Weaver's
// assignTask plan); an undeclared caller falls back to lazy on-demand reads.
func DDLs() []pkgmgr.DDLSpec {
	return []pkgmgr.DDLSpec{
		taskDDL(),
		LoomLifecycleDDL(),
		MarkExpiredDDL(),
		FreshnessExpiryAspectDDL(),
	}
}

func taskDDL() pkgmgr.DDLSpec {
	return pkgmgr.DDLSpec{
		CanonicalName:     "task",
		Class:             "meta.ddl.vertexType",
		PermittedCommands: []string{"CreateTask", "ReAssignTask", "CompleteTask", "CancelTask", "ClaimTask", "SetAvailability"},
		Description: "Orchestration task DDL. Vertex shape: vtx.task.<NanoID>, class=task, " +
			"root data = scalars only {status (open|complete|cancelled), expiresAt}; NO aspects " +
			"(the UI renders from the bound op's self-describing DDL via the forOperation link). " +
			"Relationships are LINKS: queuedFor (task->role: FR28 role-queue pull assignment; any " +
			"holder of the role may ClaimTask it; exactly one of assignedTo/queuedFor is present on " +
			"an open task), assignedTo (task→identity: who performs it), forOperation " +
			"(task→op-meta: the operation this task grants), scopedTo (task→target: the grant's " +
			"target). All links: task is the later-arriving source, the other vertex is the " +
			"pre-existing target (Contract #1 §1.1). CreateTask requires an assignee identity " +
			"and/or a queue role: assignee given+alive+available wins (assignedTo, byte-compatible " +
			"with pre-Fire-2 behavior); if the assignee is given+alive but unavailable, or absent, " +
			"a given+alive queue queues (queuedFor); else rejects RoutingFailed (no-orphan " +
			"invariant, FR29/P4). SetAvailability(identity, available) writes the identity's " +
			"availability aspect (Fire 2) that CreateTask's routing reads; an absent aspect " +
			"means available. ClaimTask lets any holder of a queued " +
			"task's role atomically swap queuedFor->assignedTo(claimant) -- the claimant is the " +
			"trusted submitting actor (op.actor), never a payload field; a non-role-holder rejects " +
			"NotAuthorizedToClaim, a re-claim by the same actor is an idempotent no-op, a claim of " +
			"an already-claimed task rejects TaskAlreadyClaimed. ReAssignTask validates the " +
			"new assignee + re-points the assignedTo link atomically (old tombstoned, new created); " +
			"CompleteTask (open->complete) and CancelTask (open->cancelled) transition the root " +
			"data.status. All lifecycle ops require the task to be open, assert the task root " +
			"revision (OCC, Contract #2 §2.6), and reject any other source state (§10.6).",
		Script: taskDDLScript,
		InputSchema: `{"type":"object","properties":` +
			`{"assignee":{"type":"string","description":"vtx.identity.<NanoID> — the identity that will perform the task. Required unless queue is given; a given+alive assignee always wins over queue."},` +
			`"queue":{"type":"string","description":"vtx.role.<NanoID> — the role-queue fallback target (FR28); any holder may later ClaimTask it. Required unless assignee is given."},` +
			`"forOperation":{"type":"string","description":"vtx.meta.<NanoID> — the operation meta-vertex this task grants the assignee permission to perform."},` +
			`"scopedTo":{"type":"string","description":"vtx.<type>.<NanoID> — the specific target the granted operation is scoped to (often ≠ the assignee)."},` +
			`"expiresAt":{"type":"string","description":"RFC3339 expiry timestamp; the grant is valid only while expiresAt > now."},` +
			`"taskId":{"type":"string","description":"Optional bare NanoID for the task vertex; supplied by a caller that must know the task key before commit (e.g. Loom's write-ahead). Absent → minted internally."},` +
			`"identity":{"type":"string","description":"vtx.identity.<NanoID> -- the identity whose availability is being set (SetAvailability)."},` +
			`"available":{"type":"boolean","description":"Whether the identity is available to receive direct task assignments (SetAvailability); read by CreateTask's routing."}},` +
			`"required":["forOperation","scopedTo","expiresAt"]}`,
		OutputSchema: `{"type":"object","properties":` +
			`{"primaryKey":{"type":"string","description":"vtx.task.<NanoID> of the created task (the operation's principal key)."}}}`,
		FieldDescription: map[string]string{
			"assignee":     "Full vtx.identity.<NanoID> key of the identity that will perform this task. A given+alive assignee always wins over queue; CreateTask rejects RoutingFailed if neither assignee nor queue resolves.",
			"queue":        "Full vtx.role.<NanoID> key of the role-queue fallback target (FR28). Used only when assignee is absent or CreateTask rejects if the role is absent/invalid too. Any holder of the role may later ClaimTask it.",
			"forOperation": "Full vtx.meta.<NanoID> key of the operation meta-vertex the task grants. The capabilityEphemeral lens link-sources the granted operationType from this op.",
			"scopedTo":     "Full vtx.<type>.<NanoID> key of the specific entity the granted operation is scoped to (e.g. the lease application to approve).",
			"expiresAt":    "RFC3339 timestamp after which the task no longer grants. Stored as a scalar on the task root data.",
			"taskId":       "Optional bare NanoID (no dots / key segments) for the created task vertex (vtx.task.<taskId>). Supplied by a caller that must know the task key before the op commits, e.g. Loom write-aheading its token.<taskKey> pointer. Absent → minted with nanoid.new(). A crash-retry with the same id collapses on the Contract #4 tracker.",
			"identity":     "Full vtx.identity.<NanoID> key of the identity whose availability is being set (SetAvailability). Must be alive; declared in ContextHint.Reads.",
			"available":    "Whether the identity should be treated as available for direct task assignment (SetAvailability). CreateTask's routing reads this identity's availability aspect: unavailable falls back to a given queue instead of assignedTo.",
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
			{
				Name: "SetAvailability — mark a manager unavailable so CreateTask falls back to a queue",
				Payload: map[string]any{
					"identity":  "vtx.identity.<managerNanoID>",
					"available": false,
				},
				ExpectedOutcome: "Validates the identity is alive, then upserts vtx.identity.<managerNanoID>.availability " +
					"(data.available=false). A subsequent CreateTask naming this identity as assignee falls back to " +
					"its queue (if given) instead of assigning directly.",
			},
		},
	}
}

// taskDDLScript handles CreateTask/ClaimTask/ReAssignTask/CompleteTask/
// CancelTask. CreateTask is known-key reads only (validates its link
// endpoints by the keys the caller listed in ContextHint.Reads); no-orphan
// by construction: the resolved assignee-or-queue endpoint (and the other
// two endpoints) must be alive or the op is rejected. ClaimTask additionally
// uses the one sanctioned bounded enumeration (kv.Links) plus on-demand
// kv.Read for checks that may legitimately find nothing.
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

def required_bool(p, name):
    if not hasattr(p, name):
        fail("InvalidArgument: " + name + ": required")
    v = getattr(p, name)
    if v == None or type(v) != type(True):
        fail("InvalidArgument: " + name + ": required bool")
    return v

def optional_string(p, name):
    # Same shape validation as required_string, but an absent/empty value is
    # not an error -- it returns None so the caller branches on presence
    # (FR28's assignee-or-queue routing, §10.1).
    if not hasattr(p, name):
        return None
    v = getattr(p, name)
    if v == None:
        return None
    if type(v) != type(""):
        fail("InvalidArgument: " + name + ": must be a string")
    v = v.strip()
    if len(v) == 0:
        return None
    return v

def bare_nanoid_or_mint(p):
    # Returns the caller-supplied taskId when present (validated as a bare
    # NanoID), else a freshly minted one. A bare NanoID carries no dots and no
    # key-prefix segments, so "vtx.task." + id is a single well-formed 3-segment
    # vertex key.
    if not hasattr(p, "taskId"):
        return nanoid.new()
    v = getattr(p, "taskId")
    if v == None:
        return nanoid.new()
    if type(v) != type("") or len(v.strip()) == 0:
        fail("InvalidArgument: taskId: must be a non-empty bare NanoID string")
    v = v.strip()
    for bad in [".", "*", ">", " ", "\t", "\n"]:
        if bad in v:
            fail("InvalidArgument: taskId: must be a bare NanoID (no dots / key segments, wildcards, or whitespace); got " + v)
    return v

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
        assignee = optional_string(p, "assignee")
        queue = optional_string(p, "queue")
        for_op = required_string(p, "forOperation")
        scoped_to = required_string(p, "scopedTo")
        # Validate + normalize expiresAt to canonical UTC whole-second RFC3339
        # (the same form the Refractor now param uses), so the lens lexical
        # expiresAt > now compare is sound regardless of the caller offset /
        # fractional-second formatting. Malformed input is rejected with a
        # structured ScriptError.
        expires_at = time.rfc3339_utc(required_string(p, "expiresAt"))

        # Validate the two link endpoints common to every CreateTask (match
        # the package idiom: rbac-domain validates both endpoints of every
        # link it writes).
        _, op_id = parts_of(for_op, "forOperation", "meta")
        scoped_type, scoped_id = parts_of(scoped_to, "scopedTo", "")
        if not vertex_alive(state, for_op):
            fail("UnknownOperation: " + for_op)
        if not vertex_alive(state, scoped_to):
            fail("UnknownTarget: " + scoped_to)

        # Routing (FR28+Fire2, Contract #10 §10.1): an assignee given+alive+
        # available wins (assignedTo, byte-identical to pre-Fire-2 behavior
        # when no availability aspect was ever set). If the assignee is
        # given+alive but marked UNavailable, fall back to a given+alive
        # queue (queuedFor) instead. An unknown/dead assignee still fails
        # immediately (UnknownAssignee) -- only "unavailable", never
        # "invalid", triggers the queue fallback. Neither endpoint resolving
        # rejects RoutingFailed -- no silent drop. No-orphan invariant
        # (FR29 / P4) either way: the resolved endpoint MUST exist and be
        # alive, or no task is committed.
        assignee_id = None
        role_id = None
        routed_via_assignee = False
        if assignee != None:
            _, assignee_id = parts_of(assignee, "assignee", "identity")
            if not vertex_alive(state, assignee):
                fail("UnknownAssignee: " + assignee)
            # Fire 2 availability gate: a known-key kv.Read whose absence is
            # a legitimate branch -- declared in ContextHint.OptionalReads by
            # the engine dispatchers (Contract #2 §2.5: present → hydrated,
            # absent → snapshot None; never in Reads, where a miss faults),
            # lazy on-demand for callers that don't declare it. Absent aspect
            # == available, so a caller that never calls SetAvailability sees
            # byte-identical Fire-1 routing.
            # read-posture: (d) declared in contextHint.optionalReads by the
            # engine dispatchers (Loom userTaskOptionalReads, Weaver assignTask)
            availability_doc = kv.Read(assignee + ".availability")
            is_available = True
            if availability_doc != None and not availability_doc.isDeleted:
                is_available = availability_doc.data.get("available", True)
            if is_available:
                routed_via_assignee = True

        if not routed_via_assignee:
            assignee_id = None
            if queue != None:
                _, role_id = parts_of(queue, "queue", "role")
                if not vertex_alive(state, queue):
                    fail("UnknownQueue: " + queue)
            elif assignee != None:
                fail("RoutingFailed: assignee " + assignee + " is unavailable and no queue was given")
            else:
                fail("RoutingFailed: CreateTask requires an assignee or a queue")

        # taskId is a caller-supplied write-ahead seam (Contract #10 §10.6): a
        # caller that must know the task key before the op commits (e.g. Loom
        # write-aheading its token.<taskKey> pointer) supplies a bare NanoID and
        # the minted task uses it verbatim. Absent → mint internally. A supplied
        # id must be a bare NanoID (no dots / key segments) so task_key stays a
        # well-formed vtx.task.<id>. CreateOnly semantics make a crash-retry with
        # the same id collapse on the Contract #4 tracker — no duplicate task.
        task_id = bare_nanoid_or_mint(p)
        task_key = "vtx.task." + task_id

        # Idempotency (Contract #10 §10.3 / §2.5): a re-dispatched CreateTask
        # supplies the SAME stable taskId (Weaver's claimId-seeded id across
        # reclaims; Loom's write-ahead id across crash-retries), so the task key
        # is stable. This kv.Read is the SOLE cross-reclaim dedup for the
        # assignTask path: Weaver's op requestId is EPISODE-scoped (changes per
        # reclaim), so the Contract #4 vtx.op.<requestId> tracker collapses only a
        # same-episode re-fire, NOT a mark-lease reclaim — the durable guard is
        # this read at the task key. kv.Read — with the key declared in
        # contextHint.optionalReads by the engine dispatchers (Contract #2 §2.5:
        # the key may legitimately not exist yet, so it can never sit in reads,
        # where a miss faults HydrationMiss; declared-optional it resolves at the
        # step-4 snapshot — absent → None, with a lost create race absorbed by the
        # Processor's CreateOnly-backstop retry). Undeclared callers still get the
        # lazy on-demand read. A present, ALIVE
        # task here means a duplicate dispatch: return empty mutations AND empty
        # events — a coherent no-op (the CreateOnly mutation below still guards the
        # same-commit concurrent race). Branch on isDeleted too: kv.Read yields
        # None for a hard-tombstoned key but a present doc with isDeleted=true
        # for a logically-deleted one — either way the gap still needs its task,
        # so absent OR deleted falls through to create; only a live task
        # suppresses. Self-heal actually COMPLETES only for absent/hard-tombstone
        # (key gone → CreateOnly commits): on a logically-deleted key step 8's
        # unconditional CreateOnly can never commit onto the still-present doc —
        # each attempt Terms as an honest RevisionConflict and a reclaim
        # re-dispatch reproduces it (bounded, operator-visible, no hot loop).
        # The §10.3 "logical delete ⇒ create" self-heal claim holds only for
        # hard tombstones — known truth-drift, follow-up recorded in the
        # script-read-posture design §12 checkpoint.
        # read-posture: (d) declared in contextHint.optionalReads by the
        # engine dispatchers (see the dedup note above)
        existing = kv.Read(task_key)
        if existing != None and not existing.isDeleted:
            return {"mutations": [], "events": []}

        forop_lnk = "lnk.task." + task_id + ".forOperation.meta." + op_id
        scoped_lnk = "lnk.task." + task_id + ".scopedTo." + scoped_type + "." + scoped_id

        mutations = [
            make_vtx(task_key, "task", {"status": "open", "expiresAt": expires_at}),
            make_link(forop_lnk, task_key, for_op, "forOperation", "forOperation", {}),
            make_link(scoped_lnk, task_key, scoped_to, "scopedTo", "scopedTo", {}),
        ]
        event_data = {"taskKey": task_key, "assignee": None, "queue": None,
                      "forOperation": for_op, "scopedTo": scoped_to,
                      "expiresAt": expires_at}
        if assignee_id != None:
            assigned_lnk = "lnk.task." + task_id + ".assignedTo.identity." + assignee_id
            mutations.append(make_link(assigned_lnk, task_key, assignee, "assignedTo", "assignedTo", {}))
            event_data["assignee"] = assignee
        else:
            queued_lnk = "lnk.task." + task_id + ".queuedFor.role." + role_id
            mutations.append(make_link(queued_lnk, task_key, queue, "queuedFor", "queuedFor", {}))
            event_data["queue"] = queue

        events = [{"class": "orchestration.taskCreated", "data": event_data}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": task_key}}

    if ot == "ClaimTask":
        # FR28: any holder of a queued task's role may claim it, atomically
        # swapping queuedFor -> assignedTo(claimant). The claimant is the
        # TRUSTED submitting actor (op.actor), never a payload field -- the
        # same don't-trust-the-payload-for-identity discipline
        # ReviewProposal uses (augur/ddls.go).
        task_key = required_string(p, "taskKey")
        _, task_id = parts_of(task_key, "taskKey", "task")

        if not vertex_alive(state, task_key):
            fail("UnknownTask: " + task_key)
        task_status = root_status(state, task_key)
        if task_status != "open":
            fail("TaskNotOpen: cannot claim a " + task_status + " task: " + task_key)

        claimant = op.actor
        _, claimant_id = parts_of(claimant, "actor", "identity")
        assigned_lnk = "lnk.task." + task_id + ".assignedTo.identity." + claimant_id

        # Resolve the task's current queuedFor link via the sanctioned
        # bounded op-time enumeration (Contract #2 §2.5.1) -- an open task
        # carries AT MOST one outgoing queuedFor link (the §10.1 "exactly
        # one assignment link" invariant), so this is never a keyspace scan,
        # and it is NOT a declared contextHint.reads key: the caller cannot
        # know the role in advance, and the link may legitimately already be
        # gone (claimed by someone else, or never queued).
        # read-posture: (e) relation=queuedFor epoch=task root (the claim's
        # own OCC-asserted update below — every queuedFor mutator commits
        # through the task root, so concurrent claimers serialise on it)
        queued_page, _ = kv.Links(task_key, "queuedFor", "out")
        queued_link = None
        for lk in queued_page:
            if not lk.isDeleted:
                queued_link = lk
        if queued_link == None:
            # Not currently queued: either already claimed, or never queued.
            # A re-claim by the SAME actor (their own assignedTo already
            # committed) is an idempotent no-op; anyone else gets
            # TaskAlreadyClaimed. kv.Read tolerates absence (on-demand, not a
            # declared read -- the CreateTask idempotency-check idiom).
            assigned_doc = kv.Read(assigned_lnk)
            if assigned_doc != None and not assigned_doc.isDeleted:
                return {"mutations": [], "events": []}
            fail("TaskAlreadyClaimed: " + task_key)

        role_key = queued_link.targetVertex
        _, role_id = parts_of(role_key, "role", "role")

        # The claimant must hold the queued role: a single known-key
        # on-demand read (both the claimant and the role are now known).
        holds_role_lnk = "lnk.identity." + claimant_id + ".holdsRole.role." + role_id
        holds_doc = kv.Read(holds_role_lnk)
        if holds_doc == None or holds_doc.isDeleted:
            fail("NotAuthorizedToClaim: " + claimant + " does not hold role " + role_key)

        mutations = [
            {"op": "update", "key": task_key,
             "expectedRevision": state[task_key].revision,
             "document": {"class": "task", "isDeleted": False,
                          "data": {"status": "open",
                                   "expiresAt": root_expires_at(state, task_key)}}},
            {"op": "tombstone", "key": queued_link.key},
            make_link(assigned_lnk, task_key, claimant, "assignedTo", "assignedTo", {}),
        ]
        events = [{"class": "orchestration.taskClaimed",
                   "data": {"taskKey": task_key, "claimant": claimant, "role": role_key}}]
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
        events = [{"class": "orchestration.taskReAssigned",
                   "data": {"taskKey": task_key, "oldAssignee": old_target,
                            "newAssignee": new_assignee}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": task_key}}

    if ot == "CompleteTask":
        return transition_task(state, p, "complete", "orchestration.taskCompleted")

    if ot == "CancelTask":
        return transition_task(state, p, "cancelled", "orchestration.taskCancelled")

    if ot == "SetAvailability":
        # Fire 2 (Contract #10 §10.1): writes the routing input CreateTask
        # reads. identity MUST be declared in ContextHint.Reads (known-key,
        # same discipline as every other endpoint this DDL validates).
        identity = required_string(p, "identity")
        parts_of(identity, "identity", "identity")
        if not vertex_alive(state, identity):
            fail("UnknownIdentity: " + identity)
        available = required_bool(p, "available")

        # Unconditioned update (create-if-absent / overwrite-if-present, the
        # loftspace-domain make_aspect_upsert idiom): a caller flipping
        # availability back and forth overwrites in place rather than
        # conflicting on a stale expectedRevision.
        aspect_key = identity + ".availability"
        mutations = [
            {"op": "update", "key": aspect_key,
             "document": {"class": "availability", "isDeleted": False,
                          "vertexKey": identity, "localName": "availability",
                          "data": {"available": available}}},
        ]
        events = [{"class": "orchestration.availabilitySet",
                   "data": {"identity": identity, "available": available}}]
        return {"mutations": mutations, "events": events,
                "response": {"primaryKey": identity}}

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
