package adjacency

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPrefetchConstants_MatchTheServerCapsAndTheMarkPairing pins the batched
// read's request size absolutely, because both halves of it are external facts
// rather than tuning knobs.
//
// prefetchSubjectCap is the substrate multi-get's atomic fast-path cap on
// MATCHED SUBJECTS: a request over it leaves that path for a consumer drain.
// prefetchNodesPerRequest is that cap HALVED, because a node contributes two
// subjects — its document and its overflow mark — and readNodeState's
// correctness argument requires the pair to come from one instant. A request
// sized past the half could split a node's pair across two instants, letting a
// reader see the document the latch has just emptied without the mark that
// explains it.
//
// Shrinking either constant turns a batch back into many small requests while
// every read-count assertion still passes, since those count the per-node reads
// a batch REMOVED. The request count is the quantity that notices, and it is
// asserted alongside the read counts in the engine's own prefetch tests.
func TestPrefetchConstants_MatchTheServerCapsAndTheMarkPairing(t *testing.T) {
	require.Equal(t, 1024, prefetchSubjectCap,
		"the multi-get's atomic fast path admits 1,024 matched subjects")
	require.Equal(t, prefetchSubjectCap/2, prefetchNodesPerRequest,
		"a node is a document AND a mark, and the pair must not be split across requests")
	require.Equal(t, 512, prefetchNodesPerRequest)
	require.Equal(t, 8, prefetchNodeFloor,
		"the floor bounds the split-on-failure descent; below it a failure is not about size")
	require.Less(t, prefetchNodeFloor, prefetchNodesPerRequest)
}
