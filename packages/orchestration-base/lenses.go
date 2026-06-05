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
// dotted key + the `cap.ephemeral.` prefix are produced by the
// capabilityenv.NewEphemeralWrapper envelope, not by the cypher.
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
			CanonicalName: "capabilityEphemeral",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        "capability-kv",
			Engine:        "full",
			Spec:          capabilityEphemeralSpec,
		},
		{
			CanonicalName: "myTasks",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        MyTasksBucket,
			Engine:        "full",
			Spec:          myTasksSpec,
		},
	}
}

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
// OPEN, and for each open task LINK-sources the projected fields (Contract
// #10 §10.1 — task relationships are links, not fields):
//
//	forOperation ← (task)-[:forOperation]->(op),  op.key
//	scopedTo     ← (task)-[:scopedTo]->(t),        t.key
//	expiresAt    ← task.data.expiresAt (scalar on the task root)
//	status       ← task.data.status (always 'open' here; the WHERE filters it)
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

RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    taskKey: task.key,
    assignee: identity.key,
    forOperation: op.key,
    scopedTo: tgt.key,
    expiresAt: task.data.expiresAt
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
  }) AS ephemeralGrants
`
