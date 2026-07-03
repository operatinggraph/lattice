package subjects

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDLQ(t *testing.T) {
	tests := []struct {
		lensID, want string
	}{
		{"agreement-summary", "lattice.refractor.dlq.agreement-summary"},
		{"occupancy-view", "lattice.refractor.dlq.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, DLQ(tt.lensID))
	}
}

func TestDLQ_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { DLQ("") })
	assert.Panics(t, func() { DLQ("lens.id") })
}

func TestMetrics_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { Metrics("") })
	assert.Panics(t, func() { Metrics("lens.id") })
}

func TestAudit_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { Audit("") })
	assert.Panics(t, func() { Audit("rule>") })
}

func TestMetrics(t *testing.T) {
	tests := []struct {
		lensID, want string
	}{
		{"agreement-summary", "lattice.refractor.metrics.agreement-summary"},
		{"occupancy-view", "lattice.refractor.metrics.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Metrics(tt.lensID))
	}
}

func TestAdjKey(t *testing.T) {
	tests := []struct {
		nodeID, want string
	}{
		{"nodeA", "adj.nodeA"},
		{"agreement-123", "adj.agreement-123"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, AdjKey(tt.nodeID))
	}
}

func TestAdjKey_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { AdjKey("") })
	assert.Panics(t, func() { AdjKey("node.id") })
	assert.Panics(t, func() { AdjKey("node*") })
	assert.Panics(t, func() { AdjKey("node>") })
	assert.Panics(t, func() { AdjKey("node id") })
}

func TestAudit(t *testing.T) {
	tests := []struct {
		lensID, want string
	}{
		{"agreement-summary", "lattice.refractor.audit.agreement-summary"},
		{"occupancy-view", "lattice.refractor.audit.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Audit(tt.lensID))
	}
}

func TestPersonalSync(t *testing.T) {
	tests := []struct {
		prefix, actorID, want string
	}{
		{"lattice.sync.user", "Op4Nb2mPq6rTwzKxVyP7", "lattice.sync.user.Op4Nb2mPq6rTwzKxVyP7"},
		{"lattice.sync.user", "identityA", "lattice.sync.user.identityA"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, PersonalSync(tt.prefix, tt.actorID))
	}
}

func TestPersonalSync_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { PersonalSync("", "identityA") })
	assert.Panics(t, func() { PersonalSync("lattice.sync.user", "") })
	assert.Panics(t, func() { PersonalSync("lattice.sync.user", "actor.id") })
	assert.Panics(t, func() { PersonalSync("lattice.sync.user", "actor*") })
}

func TestCoreKVStream(t *testing.T) {
	assert.Equal(t, "KV_core", CoreKVStream("core"))
	assert.Equal(t, "KV_my-bucket", CoreKVStream("my-bucket"))
}

func TestCoreKVFilter(t *testing.T) {
	assert.Equal(t, "$KV.core.>", CoreKVFilter("core"))
	assert.Equal(t, "$KV.my-bucket.>", CoreKVFilter("my-bucket"))
}
