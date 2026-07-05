package orchestrationbase

import "github.com/asolgan/lattice/internal/pkgmgr"

// Lenses returns the package's Lens declarations. The single
// `capabilityEphemeral` lens projects FR56 ephemeral task grants to the
// disjoint key `cap.ephemeral.<actor-suffix>` (Contract #6 §6.6 Phase-2
// amendment / Contract #10 §10.7).
//
// It projects per actor to the DISJOINT key `cap.ephemeral.<actor-suffix>`
// in the shared, primordial `capability-kv` bucket — the same
// disjoint-prefix contribution pattern Contract #6 §6.1 endorses and
// `capabilityRoleIndex` (`cap.role-by-operation.*`) already proves. The
// dotted key + the `cap.ephemeral.` prefix are produced from the lens's
// §6.13 Output descriptor (anchorType identity, outputKeyPattern
// `cap.ephemeral.{actorSuffix}`), not by the cypher.
//
// DEFAULT HARD delete: Bucket has no deleteMode override. When an actor's
// last grant expires / their task goes away the cypher's OPTIONAL task
// matches yield only degenerate (null-taskKey) collect entries; the
// envelope wrapper drops those and, finding zero real grants, signals a
// delete → the key is hard-deleted → absent → step-3 denies with
// AuthContextMismatch (absence = denial, Contract #6 §6.8).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "capabilityEphemeral",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "capability-kv",
			Engine:         "full",
			Spec:           capabilityEphemeralSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "cap.ephemeral.{actorSuffix}",
				BodyColumns:      []string{"ephemeralGrants"},
				EmptyBehavior:    "delete",
				RealnessFilter:   "taskKey",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  "myTasks",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         MyTasksBucket,
			Engine:         "full",
			Spec:           myTasksSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "my-tasks.{actorSuffix}",
				BodyColumns:      []string{"openTasks"},
				EmptyBehavior:    "delete",
				RealnessFilter:   "taskKey",
				Freshness:        "auto",
				ActorField:       "assignee",
			},
		},
		{
			CanonicalName:  "unroutedTasks",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           unroutedTasksSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "task",
				OutputKeyPattern: "unroutedTasks.{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_claim", "entityKey", "queuedRole", "expiresAt", "freshUntil"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
		{
			CanonicalName: "loomFlowHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        "orchestration-history",
			IntoKey:       []string{"instance_id"},
			Source:        loomFlowHistorySource,
		},
	}
}

// loomFlowHistorySource is the Chronicler's durable Loom-flow history read
// model (orchestration-history-read-model-design.md §2.2/§2.6, Fire 2 — the
// first consumer of the Fire 1 `eventStream` lens-source primitive): an
// event-sourced projection of `events.loom.>` (the loomLifecycle DDL's
// patternStarted/Completed/Failed events, loom_lifecycle.go) into one row
// per Loom instance. There is no Core-KV vertex to MATCH (P1) — the event
// payload is the only data, so this lens carries no cypher (Spec left empty
// on the LensSpec above).
//
// Each lifecycle event only ever carries a subset of the row's columns (a
// patternCompleted/Failed event has no patternRef/subjectKey); the
// eventlens runtime merges each projected partial onto the previously
// stored row (carry-forward), so status/timestamps accumulate correctly
// across a flow's Started→Completed|Failed lifecycle. last_event_seq is the
// JetStream stream sequence of the projecting delivery — the monotonic
// ordering token that makes an out-of-order replay converge (design §2.4):
// a replayed lower-seq patternStarted cannot clobber an already-applied
// higher-seq terminal event.
var loomFlowHistorySource = &pkgmgr.SourceConfig{
	Kind:     "eventStream",
	Subjects: []string{"events.loom.>"},
	Project: &pkgmgr.EventProjection{
		Key: "payload.instanceId",
		Columns: map[string]pkgmgr.ColumnMapping{
			"instance_id": {Path: "payload.instanceId"},
			"pattern_ref": {Path: "payload.patternRef"},
			"subject_key": {Path: "payload.subjectKey"},
			"status": {
				From: "eventType",
				Map: map[string]string{
					"loom.patternStarted":   "running",
					"loom.patternCompleted": "complete",
					"loom.patternFailed":    "failed",
				},
			},
			"failure_reason": {Path: "payload.reason"},
			"started_at": {
				When:  []string{"loom.patternStarted"},
				Value: "timestamp",
			},
			"ended_at": {
				When:  []string{"loom.patternCompleted", "loom.patternFailed"},
				Value: "timestamp",
			},
			"last_event_seq": {Path: "message.sequence"},
		},
	},
}

// unroutedTasksSpec is FR29's convergence target (Contract #10 §10.1 "unrouted
// tasks surface; never silently dropped"): an open task still queued to a
// role — never claimed — whose grant has lapsed (`expiresAt` passed) without
// anyone claiming it. The required `-[:queuedFor]->` match (not OPTIONAL) is
// the scoping gate: a direct-assigned task never matches at all, so it never
// gets a weaver-targets row; once ClaimTask swaps queuedFor→assignedTo (or
// CancelTask/CompleteTask closes the task), the match stops firing on the
// next reprojection and the envelope's EmptyBehavior:"delete" removes the row
// — the same "match fails → delete" idiom capabilityEphemeral/augurDispatch
// already use, so closing needs no explicit negative branch.
//
// There is no queuedAt timestamp to measure elapsed queue time against — the
// task DDL's root data is scalars-only {status, expiresAt} by design (no
// aspects; D5), and Starlark DDL scripts are pure (no wall-clock access), so
// nothing could stamp one. expiresAt is the grant's own clock and already
// answers the FR29 question in the form that matters operationally: "will
// this lapse unrouted?" — so the staleness threshold IS expiresAt itself
// (not an arbitrary elapsed-since-creation window), reusing existing state
// rather than adding a new one. freshUntil re-arms Weaver's @at one-shot
// timer for exactly that instant (RFC3339 UTC string comparison against
// $now, the same lexical-compare idiom capabilityEphemeral/myTasks use); it
// goes null once violating (the row's own CDC delivery drives re-evaluation
// from there, like every other target).
const unroutedTasksSpec = `
MATCH (t:task {key: $actorKey})-[:queuedFor]->(role:role)
WHERE t.data.status = 'open'
RETURN
  t.key AS actorKey,
  t.key AS entityKey,
  nanoIdFromKey(t.key) AS entityId,
  role.key AS queuedRole,
  t.data.expiresAt AS expiresAt,
  ($now > t.data.expiresAt) AS missing_claim,
  ($now > t.data.expiresAt) AS violating,
  (CASE WHEN ($now > t.data.expiresAt) THEN null ELSE t.data.expiresAt END) AS freshUntil
`

// MyTasksBucket is the package-owned output bucket for the my-tasks lens.
// Provisioned at package-install time (NOT primordial), mirroring
// duplicate-candidates. Each entry is keyed my-tasks.identity.<NanoID> and
// carries that identity's OPEN tasks (§10.1). DEFAULT HARD delete (Story
// 1.5.12 — no deleteMode override): when an identity's last open task closes
// or moves away, the envelope signals a delete and the key is hard-deleted →
// the identity drops out of my-tasks.
const MyTasksBucket = "my-tasks"

// myTasksSpec is the link-sourced per-identity OPEN-task cypher. Anchored on
// the bound identity (not the unbound task label) so reprojection traverses
// adjacency from the actor, mirroring capabilityEphemeral.
//
// Per identity it walks (identity)<-[:assignedTo]-(task) where the task is
// OPEN, plus (FR28) (identity)-[:holdsRole]->(role)<-[:queuedFor]-(task) for
// any task queued to a role the identity holds, and for each open task
// LINK-sources the projected fields (Contract #10 §10.1 — task
// relationships are links, not fields):
//
//	forOperation ← (task)-[:forOperation]->(op),  op.key
//	operationName        ← op.data.operationType        (the op's name)
//	operationDescription ← op.description.data.value    (the op's instructions)
//	scopedTo     ← (task)-[:scopedTo]->(t),        t.key
//	expiresAt    ← task.data.expiresAt (scalar on the task root)
//	status       ← task.data.status (always 'open' here; the WHERE filters it)
//
// operationName reads the op meta-vertex's root `operationType`, the field every
// op DDL carries (Contract #10 §10.1: "UI finds the bound op by walking
// forOperation to the operation meta-vertex"). A dispatched userTask's
// forOperation points at the operation's DDL meta-vertex, whose name lives on
// the root as `data.operationType` — NOT as a `.canonicalName` aspect (those
// exist only on a handful of primordial metas), so an aspect-hop projects null
// for every package op. operationDescription stays a best-effort aspect-hop:
// the op's optional human instructions, null when the package authored none.
// Both are null-safe (resolveProperty returns nil when the op or field is
// absent), keeping the row self-describing so a task-inbox renders a label
// without a second read.
//
// The non-optional identity anchor means a live identity always yields exactly
// one row whose `openTasks` collect may contain a degenerate {taskKey:null}
// artifact when the identity has no open task; the envelope wrapper drops those
// and, finding zero real open tasks, signals a delete so absence is genuine
// (the 7.1 FIX-1 absence mechanism — ErrSkipProjection would leave the key).
const myTasksSpec = `
MATCH (identity:identity {key: $actorKey})

OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open'
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)

// --- role-queue fan-out (FR28): any task queued to a role this identity
// holds shows up in the inbox too, so a role-holder can see (and ClaimTask)
// it. queuedRole names the governing role; null for a direct assignment.
// On ClaimTask the queuedFor link tombstones and this branch stops matching
// for every non-claimant -- the grant narrows through ordinary reprojection.
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(qtask:task)
  WHERE qtask.data.status = 'open'
OPTIONAL MATCH (qtask)-[:forOperation]->(qop)
OPTIONAL MATCH (qtask)-[:scopedTo]->(qtgt)

RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    taskKey: task.key,
    assignee: identity.key,
    forOperation: op.key,
    operationName: op.data.operationType,
    operationDescription: op.description.data.value,
    scopedTo: tgt.key,
    expiresAt: task.data.expiresAt,
    queuedRole: null
  }) + collect(DISTINCT {
    taskKey: qtask.key,
    assignee: null,
    forOperation: qop.key,
    operationName: qop.data.operationType,
    operationDescription: qop.description.data.value,
    scopedTo: qtgt.key,
    expiresAt: qtask.data.expiresAt,
    queuedRole: role.key
  }) AS openTasks
`

// capabilityEphemeralSpec is the link-sourced ephemeral-grant cypher.
//
// Per actor it walks (identity)<-[:assignedTo]-(task) (direct + a
// reportsTo 2-hop where a manager inherits the tasks of the reports that
// report to it — downward delegation), and for each task LINK-sources
// the grant fields (Contract #10 §10.1 — task relationships are links, not
// fields):
//
//	operationType ← (task)-[:forOperation]->(op),   op.data.operationType
//	target        ← (task)-[:scopedTo]->(t),        t.key
//	expiresAt     ← task.data.expiresAt (scalar on the task root)
//
// This replaces the bootstrap cypher's old field reads
// (task.data.grantedOperationType / task.data.targetKey — the corrected
// anti-pattern). The grant *field shape* {source, taskKey, operationType,
// target, expiresAt} is unchanged (Contract #6 §6.6). Only live grants are
// projected (`task.data.expiresAt > $now`).
//
// The RETURN produces `actorKey` (so the envelope wrapper can derive the
// `cap.ephemeral.<actor>` key) and the `ephemeralGrants` array. Anchored on
// the bound identity (not the unbound task label) so reprojection traverses
// adjacency from the actor instead of scanning every task.
const capabilityEphemeralSpec = `
MATCH (identity:identity {key: $actorKey})

// --- direct assignments ---
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.expiresAt > $now
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)

// --- manager delegation: reportsTo 2-hop ---
// identity is the manager; each report reportsTo identity, so identity
// inherits the tasks assigned to its reports (downward delegation).
OPTIONAL MATCH (identity)<-[:reportsTo]-(report:identity)<-[:assignedTo]-(task2:task)
  WHERE task2.data.expiresAt > $now
OPTIONAL MATCH (task2)-[:forOperation]->(op2)
OPTIONAL MATCH (task2)-[:scopedTo]->(tgt2)

// --- role-queue fan-out (FR28) ---
// while a task is queued, every holder of the queued role is granted the
// underlying operation -- the role-queue's "anyone on the team can pick it
// up" semantics, expressed through the same ephemeralGrants array the
// direct path uses. On ClaimTask the queuedFor link tombstones, this branch
// stops matching for every non-claimant, and the claimant picks the grant
// up via the direct assignedTo branch above -- the grant narrows through
// ordinary reprojection, no bespoke revocation.
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(task3:task)
  WHERE task3.data.expiresAt > $now
OPTIONAL MATCH (task3)-[:forOperation]->(op3)
OPTIONAL MATCH (task3)-[:scopedTo]->(tgt3)

RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    source: "task",
    taskKey: task.key,
    operationType: op.data.operationType,
    target: tgt.key,
    expiresAt: task.data.expiresAt
  }) + collect(DISTINCT {
    source: "task",
    taskKey: task2.key,
    operationType: op2.data.operationType,
    target: tgt2.key,
    expiresAt: task2.data.expiresAt
  }) + collect(DISTINCT {
    source: "task",
    taskKey: task3.key,
    operationType: op3.data.operationType,
    target: tgt3.key,
    expiresAt: task3.data.expiresAt
  }) AS ephemeralGrants
`
