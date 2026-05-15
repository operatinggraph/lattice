package subjects

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRules(t *testing.T) {
	tests := []struct {
		team, ruleID, want string
	}{
		{"agreement-team", "agreement-summary", "materializer.rules.agreement-team.agreement-summary"},
		{"facilities", "occupancy-view", "materializer.rules.facilities.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Rules(tt.team, tt.ruleID))
	}
}

func TestRules_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { Rules("", "rule") })
	assert.Panics(t, func() { Rules("team", "") })
	assert.Panics(t, func() { Rules("team.a", "rule") })
	assert.Panics(t, func() { Rules("team", "rule*") })
	assert.Panics(t, func() { Rules("team", "rule>") })
	assert.Panics(t, func() { Rules("team a", "rule") })
}

func TestHealth(t *testing.T) {
	tests := []struct {
		ruleID, want string
	}{
		{"agreement-summary", "materializer.health.agreement-summary"},
		{"occupancy-view", "materializer.health.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Health(tt.ruleID))
	}
}

func TestDLQ(t *testing.T) {
	tests := []struct {
		team, ruleID, want string
	}{
		{"agreement-team", "agreement-summary", "materializer.dlq.agreement-team.agreement-summary"},
		{"facilities", "occupancy-view", "materializer.dlq.facilities.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, DLQ(tt.team, tt.ruleID))
	}
}

func TestDLQ_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { DLQ("", "rule") })
	assert.Panics(t, func() { DLQ("team", "") })
	assert.Panics(t, func() { DLQ("team.a", "rule") })
}

func TestHealth_InvalidInputPanics(t *testing.T) {
	assert.Panics(t, func() { Health("") })
	assert.Panics(t, func() { Health("lens.id") })
	assert.Panics(t, func() { Health("rule*") })
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
		ruleID, want string
	}{
		{"agreement-summary", "materializer.metrics.agreement-summary"},
		{"occupancy-view", "materializer.metrics.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Metrics(tt.ruleID))
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
		ruleID, want string
	}{
		{"agreement-summary", "materializer.audit.agreement-summary"},
		{"occupancy-view", "materializer.audit.occupancy-view"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, Audit(tt.ruleID))
	}
}

func TestControl(t *testing.T) {
	assert.Equal(t, "materializer.control", Control())
}

func TestCoreKVStream(t *testing.T) {
	assert.Equal(t, "KV_core", CoreKVStream("core"))
	assert.Equal(t, "KV_my-bucket", CoreKVStream("my-bucket"))
}

func TestCoreKVFilter(t *testing.T) {
	assert.Equal(t, "$KV.core.>", CoreKVFilter("core"))
	assert.Equal(t, "$KV.my-bucket.>", CoreKVFilter("my-bucket"))
}
