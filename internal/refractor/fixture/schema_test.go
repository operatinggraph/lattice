package fixture_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/fixture"
)

// fixtureDir returns the absolute path to testdata/fixtures/ relative to this test file.
func fixtureDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "../../testdata/fixtures")
}

func TestLoad_BasicUpsert(t *testing.T) {
	fix, err := fixture.Load(filepath.Join(fixtureDir(), "basic_upsert.yaml"))
	require.NoError(t, err)

	assert.Equal(t, "basic-upsert", fix.Rule.ID)
	assert.Equal(t, "test-team", fix.Rule.Team)
	assert.NotEmpty(t, fix.Rule.Match)
	assert.Equal(t, "nats_kv", fix.Rule.Into.Target)
	assert.Equal(t, []string{"agreement_id"}, []string(fix.Rule.Into.Key))

	require.Len(t, fix.Inputs, 1)
	assert.Equal(t, "node_agreement_abc123", fix.Inputs[0].Key)
	assert.Equal(t, "abc", fix.Inputs[0].Payload["id"])
	assert.Equal(t, "Acme", fix.Inputs[0].Payload["partyName"])
	assert.Equal(t, false, fix.Inputs[0].Payload["isDeleted"])

	require.Len(t, fix.Expect.NatsKV, 1)
	assert.Equal(t, "abc", fix.Expect.NatsKV[0].Key)
	assert.False(t, fix.Expect.NatsKV[0].Deleted)
	assert.Equal(t, "abc", fix.Expect.NatsKV[0].Value["agreement_id"])
	assert.Equal(t, "Acme", fix.Expect.NatsKV[0].Value["party_name"])
}

func TestLoad_MissingRuleID(t *testing.T) {
	content := `
rule:
  team: test-team
  match: "MATCH (a:agreement) RETURN a.id AS agreement_id"
  into:
    target: nats_kv
    bucket: b
    key: agreement_id
inputs: []
expect:
  nats_kv: []
`
	path := writeTempFixture(t, content)
	_, err := fixture.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lens.id")
}

func TestLoad_MissingIntoKey(t *testing.T) {
	content := `
rule:
  id: test
  team: test-team
  match: "MATCH (a:agreement) RETURN a.id AS agreement_id"
  into:
    target: nats_kv
    bucket: b
inputs: []
expect:
  nats_kv: []
`
	path := writeTempFixture(t, content)
	_, err := fixture.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "into.key")
}

func TestLoad_MissingMatch(t *testing.T) {
	content := `
rule:
  id: test
  team: test-team
  into:
    target: nats_kv
    bucket: b
    key: agreement_id
inputs: []
expect:
  nats_kv: []
`
	path := writeTempFixture(t, content)
	_, err := fixture.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lens.match")
}

func TestLoad_CompositeKey(t *testing.T) {
	content := `
description: "composite key fixture"
rule:
  id: composite-test
  team: test-team
  match: "MATCH (a:agreement) RETURN a.teamId AS team_id, a.id AS agreement_id"
  into:
    target: nats_kv
    bucket: b
    key: [team_id, agreement_id]
inputs: []
expect:
  nats_kv: []
`
	path := writeTempFixture(t, content)
	fix, err := fixture.Load(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"team_id", "agreement_id"}, []string(fix.Rule.Into.Key))
}

func TestLoad_WithAdjacency(t *testing.T) {
	content := `
description: "adjacency seed fixture"
rule:
  id: adj-test
  team: test-team
  match: "MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name"
  into:
    target: nats_kv
    bucket: b
    key: agreement_id
inputs:
  - key: "node_agreement_abc123"
    payload:
      id: "abc"
      isDeleted: false
adjacency:
  - node_id: "node_agreement_abc123"
    edges:
      - coreKvKey: "node_edge_rel1"
        edgeId: "rel1"
        name: "HAS_PARTY"
        direction: "outbound"
        otherNodeId: "node_identity_xyz789"
expect:
  nats_kv: []
`
	path := writeTempFixture(t, content)
	fix, err := fixture.Load(path)
	require.NoError(t, err)
	require.Len(t, fix.Adjacency, 1)
	assert.Equal(t, "node_agreement_abc123", fix.Adjacency[0].NodeID)
	require.Len(t, fix.Adjacency[0].Edges, 1)
	assert.Equal(t, "HAS_PARTY", fix.Adjacency[0].Edges[0].Name)
	assert.Equal(t, "outbound", fix.Adjacency[0].Edges[0].Direction)
	assert.Equal(t, "node_identity_xyz789", fix.Adjacency[0].Edges[0].OtherNodeID)
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := fixture.Load("/does/not/exist.yaml")
	require.Error(t, err)
}

func TestLoad_DeletedExpectEntry(t *testing.T) {
	content := `
description: "fixture with deleted expect entry"
rule:
  id: del-test
  team: test-team
  match: "MATCH (a:agreement) RETURN a.id AS agreement_id"
  into:
    target: nats_kv
    bucket: b
    key: agreement_id
inputs: []
expect:
  nats_kv:
    - key: "gone-key"
      deleted: true
`
	path := writeTempFixture(t, content)
	fix, err := fixture.Load(path)
	require.NoError(t, err)
	require.Len(t, fix.Expect.NatsKV, 1)
	assert.Equal(t, "gone-key", fix.Expect.NatsKV[0].Key)
	assert.True(t, fix.Expect.NatsKV[0].Deleted)
	assert.Nil(t, fix.Expect.NatsKV[0].Value)
}

// writeTempFixture writes content to a temp file and returns the path.
func writeTempFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
