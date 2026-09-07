package adjacency_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
)

func TestEventsForLink_DirectionalPair(t *testing.T) {
	evts := adjacency.EventsForLink("lnk.identity.srcNode.holdsRole.role.dstNode",
		"identity", "srcNode", "holdsRole", "role", "dstNode", false, 0)

	require.Len(t, evts, 2)
	out, in := evts[0], evts[1]

	assert.Equal(t, "lnk.identity.srcNode.holdsRole.role.dstNode", out.CoreKvKey)
	assert.Equal(t, "lnk.identity.srcNode.holdsRole.role.dstNode", out.EdgeID)
	assert.Equal(t, "holdsRole", out.Name)
	assert.Equal(t, "outbound", out.Direction)
	assert.Equal(t, "srcNode", out.NodeID)
	assert.Equal(t, "dstNode", out.OtherNodeID)
	assert.Equal(t, "role", out.OtherType)
	assert.False(t, out.IsDeleted)

	assert.Equal(t, "lnk.identity.srcNode.holdsRole.role.dstNode", in.CoreKvKey)
	assert.Equal(t, "lnk.identity.srcNode.holdsRole.role.dstNode", in.EdgeID)
	assert.Equal(t, "holdsRole", in.Name)
	assert.Equal(t, "inbound", in.Direction)
	assert.Equal(t, "dstNode", in.NodeID)
	assert.Equal(t, "srcNode", in.OtherNodeID)
	assert.Equal(t, "identity", in.OtherType)
	assert.False(t, in.IsDeleted)
}

func TestEventsForLink_PropagatesIsDeleted(t *testing.T) {
	evts := adjacency.EventsForLink("lnk.identity.a.supervises.identity.b",
		"identity", "a", "supervises", "identity", "b", true, 0)

	require.Len(t, evts, 2)
	assert.True(t, evts[0].IsDeleted)
	assert.True(t, evts[1].IsDeleted)
}

// TestEventsForLink_SelfLinkYieldsBothDirections pins the case that motivates
// keeping direction as its own dimension rather than deriving it from
// whether the two endpoints differ: a self-link (both endpoints the same
// node) still has to appear once as that node's outbound edge and once as
// its inbound edge, not collapse into one entry or cancel out.
func TestEventsForLink_SelfLinkYieldsBothDirections(t *testing.T) {
	evts := adjacency.EventsForLink("lnk.identity.self.supervises.identity.self",
		"identity", "self", "supervises", "identity", "self", false, 0)

	require.Len(t, evts, 2)
	out, in := evts[0], evts[1]

	assert.Equal(t, "outbound", out.Direction)
	assert.Equal(t, "self", out.NodeID)
	assert.Equal(t, "self", out.OtherNodeID)
	assert.Equal(t, "identity", out.OtherType)

	assert.Equal(t, "inbound", in.Direction)
	assert.Equal(t, "self", in.NodeID)
	assert.Equal(t, "self", in.OtherNodeID)
	assert.Equal(t, "identity", in.OtherType)
}

// TestEventsForLink_StampsTheSequenceOnBothDirections pins that the ordering
// stamp reaches both halves of an edge. The two endpoints index the same link
// under the same EdgeID and each arbitrates it against the floor its own node
// document holds, so a stamp that reached only one arm would let one endpoint
// keep a version of the link the other had already refused — the two ends of
// one edge disagreeing about whether it exists.
func TestEventsForLink_StampsTheSequenceOnBothDirections(t *testing.T) {
	evts := adjacency.EventsForLink("lnk.identity.srcNode.holdsRole.role.dstNode",
		"identity", "srcNode", "holdsRole", "role", "dstNode", false, 4207)

	require.Len(t, evts, 2)
	assert.Equal(t, uint64(4207), evts[0].Seq, "the outbound half carries the message's sequence")
	assert.Equal(t, uint64(4207), evts[1].Seq, "and so does the inbound half")
}

// TestCoreKVEvent_SequenceIsNotWireCarried pins the `json:"-"` on Seq, which is
// a correctness tag rather than a cosmetic one. The legacy event path unmarshals
// a CoreKVEvent straight out of a Core KV message BODY; were the field wire
// visible, that body could name its own ordering floor and promote itself over
// the events it must lose to. The sequence is transport-derived, stamped by the
// consumer after the unmarshal, and a body claiming one is ignored.
func TestCoreKVEvent_SequenceIsNotWireCarried(t *testing.T) {
	encoded, err := json.Marshal(adjacency.CoreKVEvent{
		CoreKvKey: "lnk.identity.a.holdsRole.role.b", EdgeID: "lnk.identity.a.holdsRole.role.b",
		NodeID: "a", OtherNodeID: "b", Seq: 4207,
	})
	require.NoError(t, err)

	var onTheWire map[string]any
	require.NoError(t, json.Unmarshal(encoded, &onTheWire))
	assert.NotContains(t, onTheWire, "seq", "the sequence never leaves the process as payload")
	assert.NotContains(t, onTheWire, "Seq")

	var decoded adjacency.CoreKVEvent
	require.NoError(t, json.Unmarshal([]byte(
		`{"coreKvKey":"k","edgeId":"k","nodeId":"a","otherNodeId":"b","seq":999999,"Seq":999999}`), &decoded))
	assert.Zero(t, decoded.Seq, "a message body cannot choose the floor its own event is measured against")
}
