package pipeline

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// Both link arms of this pipeline write the adjacency index themselves before
// re-reading it, and each such write is arbitrated against the ordering floor
// that index keeps per edge. The number that arbitration uses is the delivering
// message's backing-stream sequence, threaded from substrate.Message through
// the arm into adjacency.EventsForLink.
//
// A threaded value asserted only as "non-zero" is not asserted: a site that
// stamped a constant, or the wrong message's position, would leave every
// ordering test in the adjacency package green while the guard did nothing
// here. So both pins below drive a whole message through the real dispatch
// (Pipeline.handle) and assert the persisted EdgeEntry.Seq equals the sequence
// that message carried, exactly.

// storedEdgeSeq returns the persisted sequence of one node's entry for edgeID.
func storedEdgeSeq(t *testing.T, adjKV *substrate.KV, nodeID, edgeID string) uint64 {
	t.Helper()
	entry, err := adjKV.Get(context.Background(), subjects.AdjKey(nodeID))
	require.NoError(t, err, "the arm must have written an adjacency document for %q", nodeID)

	var doc adjacency.AdjValue
	require.NoError(t, json.Unmarshal(entry.Value, &doc))
	for _, e := range doc.Edges {
		if e.EdgeID == edgeID {
			return e.Seq
		}
	}
	t.Fatalf("node %q holds no entry for edge %q", nodeID, edgeID)
	return 0
}

// TestLinkArmsStampTheDeliveringMessagesSequence covers both arms that write
// the index — the actor-aware fan-out's pre-apply and plain-link
// reprojection — because each threads the sequence through its own call chain
// and a stamp lost on one would be invisible from the other.
func TestLinkArmsStampTheDeliveringMessagesSequence(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()

	// A sequence that is nothing else in the fixture: not a revision, not a
	// count, not 1 — so a stamp of the wrong quantity cannot coincide with it.
	const deliveredAt = 4207

	event := func(p *Pipeline, key string) substrate.Message {
		return substrate.Message{
			Subject:  "$KV." + p.coreKVBucket + "." + key,
			Sequence: deliveredAt,
			Body:     []byte(`{"key":"` + key + `","isDeleted":false,"lastModifiedAt":"2026-08-14T00:00:00Z"}`),
		}
	}

	t.Run("the actor-aware link fan-out", func(t *testing.T) {
		p, _ := newScopeProducerFixture(t)
		linkKey := substrate.LinkKey("identity", personalActorA, "holds", "lease", scopedLeaseIDs[0])

		decision, err := p.handle(ctx, event(p, linkKey))
		require.NoError(t, err)
		require.Equal(t, substrate.Ack, decision)

		assert.Equal(t, uint64(deliveredAt), storedEdgeSeq(t, p.adjKV, personalActorA, linkKey),
			"the pre-apply's outbound arm carries the delivering message's own sequence")
		assert.Equal(t, uint64(deliveredAt), storedEdgeSeq(t, p.adjKV, scopedLeaseIDs[0], linkKey),
			"and so does its inbound arm")
	})

	t.Run("plain link reprojection", func(t *testing.T) {
		p, _ := newPlainScopeVectorPipeline(t)
		linkKey := substrate.LinkKey("unit", scopedLeaseIDs[0], "inBuilding", "building", scopedLeaseIDs[1])

		decision, err := p.handle(ctx, event(p, linkKey))
		require.NoError(t, err)
		require.Equal(t, substrate.Ack, decision)

		assert.Equal(t, uint64(deliveredAt), storedEdgeSeq(t, p.adjKV, scopedLeaseIDs[0], linkKey))
		assert.Equal(t, uint64(deliveredAt), storedEdgeSeq(t, p.adjKV, scopedLeaseIDs[1], linkKey))
	})
}
