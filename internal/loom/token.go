package loom

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/asolgan/lattice/internal/substrate"
)

// deriveRequestID returns a deterministic 20-char NanoID (over the canonical
// Lattice alphabet, Contract #1) for the step at cursor within instance. It is
// the step's write-ahead pendingToken AND the op's requestId — the single
// token that makes systemOp submission idempotent: a re-attempt after a crash
// reuses the same requestId and collapses on the Contract #4 vtx.op.<requestId>
// tracker (Crash-safety invariant 1; exactly-once).
//
// A systemOp token is a bare NanoID (the canonical Lattice alphabet contains no
// dot), so it can never carry the "vtx.task." prefix isUserTaskToken keys on —
// the systemOp / userTask token namespaces are disjoint by construction.
func deriveRequestID(instanceID string, cursor int) string {
	return deriveID("", instanceID, cursor)
}

// deriveTaskID returns a deterministic 20-char NanoID for the task a userTask
// step at cursor creates within instance. It is the task NanoID Loom supplies to
// CreateTask (the verbatim taskId seam) AND, via vtx.task.<id>, the userTask's
// write-ahead token and the orchestration.taskCompleted payload.taskKey completion-correlation
// handle (Contract #10 §10.6). It is namespaced disjoint from deriveRequestID so
// the CreateTask op's own requestId (the submission idempotency handle) and the
// task id (the completion-correlation handle) never collide.
func deriveTaskID(instanceID string, cursor int) string {
	return deriveID("task:", instanceID, cursor)
}

// deriveInstanceID returns a deterministic 20-char NanoID for the externalTask
// step at cursor within instance: the bare instance handle Loom mints
// write-ahead, parks on as token.<handle>, AND passes to instanceOp as the
// caller-supplied id (the DDL prepends its package-chosen type to form the
// vtx.<type>.<handle> claim-vertex key — the engine never names the type). A
// crash-retry re-mints the same handle, so the re-submitted instanceOp collapses
// on the Contract #4 vtx.op.<opRequestId> tracker (no duplicate claim vertex);
// the bridge echoes the handle back as payload.externalRef, the same value Loom
// parked on. It is namespaced disjoint from deriveRequestID ("") and
// deriveTaskID ("task:") so the same (instanceId, cursor) yields three distinct
// ids — the instanceOp's own submission requestId (deriveRequestID) and the
// instance handle never collide. The handle is a bare NanoID (the canonical
// Lattice alphabet has no dot), so it can never carry the "vtx.task." prefix
// isUserTaskToken keys on — the externalTask token is namespace-disjoint from
// the userTask token AND the systemOp requestId.
func deriveInstanceID(instanceID string, cursor int) string {
	return deriveID("instance:", instanceID, cursor)
}

// deriveID is the shared deterministic NanoID derivation. The namespace prefix
// keeps disjoint derivations (op requestId vs task id vs instance handle) from
// colliding for the same (instanceId, cursor).
func deriveID(namespace, instanceID string, cursor int) string {
	var seed [8]byte
	binary.BigEndian.PutUint64(seed[:], uint64(cursor))
	sum := sha256.Sum256(append([]byte(namespace+instanceID+":"), seed[:]...))
	id := make([]byte, substrate.NanoIDLength)
	// Expand the 32-byte digest across the id by re-hashing as needed.
	digest := sum[:]
	di := 0
	for i := 0; i < substrate.NanoIDLength; i++ {
		if di >= len(digest) {
			next := sha256.Sum256(digest)
			digest = next[:]
			di = 0
		}
		id[i] = substrate.Alphabet[int(digest[di])%len(substrate.Alphabet)]
		di++
	}
	return string(id)
}
