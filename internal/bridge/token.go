package bridge

import (
	"crypto/sha256"

	"github.com/asolgan/lattice/internal/substrate"
)

// replyRequestNamespace prefixes the hash input so a bridge result-op requestId
// can never collide with a Loom-derived id (Loom namespaces its derivations
// "", "task:", "instance:") for the same opaque value.
const replyRequestNamespace = "bridge:reply:"

// dispatchRequestNamespace prefixes the hash input for a bridge dispatch-op
// requestId. A distinct namespace from replyRequestNamespace keeps the
// pending-marker op id disjoint from the terminal reply-op id for the SAME opaque
// instanceKey — both are derived for one external call, but they are two distinct
// ops on two distinct Contract #4 trackers (the dispatch may land while the reply
// never does), so they must never collide.
const dispatchRequestNamespace = "bridge:dispatch:"

// deriveReplyRequestID returns the deterministic result-op requestId for an
// external call, derived solely from the opaque instanceKey. A redelivered
// external.* event therefore yields the SAME requestId, so the re-submitted
// replyOp collapses on the Contract #4 vtx.op.<requestId> tracker — exactly one
// result mutation (the pinned FR58 invariant, Contract #10 §10.3). The
// instanceKey is treated as an opaque token: its type segment, if any, is never
// parsed. The derivation is pure (no stored map), so a fresh replica or a
// restart computes the identical id from the same instanceKey, which is what
// makes redelivery-after-crash collapse correctly without shared state.
//
// The output is a bare NanoID over the canonical Lattice alphabet (Contract #1),
// so it is a valid dot-free op requestId.
func deriveReplyRequestID(instanceKey string) string {
	return deriveID(replyRequestNamespace, instanceKey)
}

// deriveDispatchRequestID returns the deterministic dispatch-op requestId for an
// external call that came back Pending, derived solely from the opaque
// instanceKey. A redelivered external.* event therefore yields the SAME requestId,
// so the re-submitted dispatch op collapses on the Contract #4
// vtx.op.<requestId> tracker — exactly one create-only .dispatch marker no matter
// how many times the Pending event is redelivered. Like deriveReplyRequestID the
// instanceKey is opaque (its type segment, if any, is never parsed) and the
// derivation is pure, so a restart computes the identical id and redelivery-after-
// crash still collapses. The output is a bare NanoID (a valid dot-free op
// requestId).
func deriveDispatchRequestID(instanceKey string) string {
	return deriveID(dispatchRequestNamespace, instanceKey)
}

// deriveID is the shared deterministic NanoID derivation: a stable hash over
// namespace+input expanded across the canonical alphabet. The namespace prefix
// keeps disjoint derivations from colliding for the same input.
func deriveID(namespace, input string) string {
	sum := sha256.Sum256([]byte(namespace + input))
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
