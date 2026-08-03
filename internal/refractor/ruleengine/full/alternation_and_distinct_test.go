package full

// Two narrow correctness pins on the openCypher path: a relationship
// alternation is refused rather than silently truncated to its first type, and
// RETURN DISTINCT keys rows on the injective rendering rather than on
// json.Marshal, which erases a node-valued column.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// TestParse_RelationshipTypeAlternationRejected pins that `-[:a|b]->` is a
// parse error. A RelPattern carries ONE Type and the executor traverses that
// one, so accepting an alternation and keeping only names[0] executed a
// different query than the author wrote — and, since the referenced-relation
// set is derived from the same field, under-declared which link events the lens
// reacts to. pkgmgr's anchor-walk parser already refuses alternation
// (anchorwalk.go's rel); this is the general Cypher path agreeing.
func TestParse_RelationshipTypeAlternationRejected(t *testing.T) {
	eng := New()

	for _, spec := range []string{
		`MATCH (u:unit)-[:manages|occupies]->(i:identity) RETURN u.key AS key`,
		`MATCH (u:unit)<-[:manages|occupies]-(i:identity) RETURN u.key AS key`,
		`MATCH (u:unit)-[r:manages|occupies|holds]->(i:identity) RETURN u.key AS key`,
	} {
		_, err := eng.Parse(spec)
		require.Error(t, err, "an alternation must not parse: %s", spec)
		require.Contains(t, err.Error(), "at most ONE type")
	}

	// The single-type and untyped forms are untouched — this rejects only what
	// the engine cannot execute, and an untyped hop is executable (it is merely
	// non-exhaustive for narrowing).
	for _, spec := range []string{
		`MATCH (u:unit)-[:manages]->(i:identity) RETURN u.key AS key`,
		`MATCH (u:unit)-[]->(i:identity) RETURN u.key AS key`,
		`MATCH (u:unit)-[r]->(i:identity) RETURN u.key AS key`,
	} {
		_, err := eng.Parse(spec)
		require.NoError(t, err, "a one-type or untyped hop must still parse: %s", spec)
	}
}

// TestExec_DistinctKeepsRowsDifferingOnlyByNode pins that DISTINCT does not
// collapse two rows whose only difference is a node-valued column.
//
// A node column holds a *nodeRef, whose fields are all unexported: json.Marshal
// renders EVERY one of them as `{}`, so the old key made two different nodes
// indistinguishable and dropped one row. normalizeForKey renders a node as its
// key. The test asserts the outcome AND, below, the property that made the old
// key wrong — so a future edit back to marshalling fails here rather than
// silently losing rows.
func TestExec_DistinctKeepsRowsDifferingOnlyByNode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})
	putVertex(t, reg, coreKV, "bob", "identity", map[string]any{"name": "bob"})

	results := parseExec(t,
		`MATCH (i:identity) RETURN DISTINCT i AS who`,
		ruleengine.EventContext{},
		adjKV, coreKV,
	)
	require.Len(t, results, 2, "two different nodes are two distinct rows")

	// The underlying property: the two rows are indistinguishable to
	// json.Marshal and distinct to normalizeForKey.
	rowA := binding{"who": &nodeRef{key: vtxKey(reg, "alice")}}
	rowB := binding{"who": &nodeRef{key: vtxKey(reg, "bob")}}

	ja, err := json.Marshal(rowA)
	require.NoError(t, err)
	jb, err := json.Marshal(rowB)
	require.NoError(t, err)
	require.Equal(t, string(ja), string(jb),
		"a nodeRef has no exported fields, so marshalling cannot tell two nodes apart")

	require.NotEqual(t,
		normalizeForKey(map[string]any(rowA)),
		normalizeForKey(map[string]any(rowB)),
		"the injective rendering must keep two nodes distinct")
}
