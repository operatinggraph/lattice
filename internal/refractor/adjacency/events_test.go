package adjacency_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
)

func TestEventsForLink_DirectionalPair(t *testing.T) {
	evts := adjacency.EventsForLink("lnk.identity.srcNode.holdsRole.role.dstNode",
		"identity", "srcNode", "holdsRole", "role", "dstNode", false)

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
		"identity", "a", "supervises", "identity", "b", true)

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
		"identity", "self", "supervises", "identity", "self", false)

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
