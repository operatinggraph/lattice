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
// DEFAULT HARD delete: Bucket has no deleteMode override, so
// when an actor's last grant expires / their task goes away the lens
// reprojects to no row → the key is hard-deleted → absent → step-3 denies
// with AuthContextMismatch (absence = denial, Contract #6 §6.8).
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
	}
}

// capabilityEphemeralSpec is the link-sourced ephemeral-grant cypher.
//
// Per actor it walks (identity)<-[:assignedTo]-(task) (direct + a
// reportsTo 2-hop for manager delegation), and for each task LINK-sources
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
OPTIONAL MATCH (identity)-[:reportsTo]->(report:identity)<-[:assignedTo]-(task2:task)
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
