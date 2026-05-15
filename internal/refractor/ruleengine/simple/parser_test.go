package simple

// No testing.Short() skip — all parser tests are infrastructure-free (ADR-6).

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── AC#1: valid MATCH/RETURN ─────────────────────────────────────────────────

func TestParse_ValidMatchReturn(t *testing.T) {
	q, err := Parse("MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name")
	require.NoError(t, err)
	require.NotNil(t, q)

	require.Len(t, q.Matches, 1)
	assert.False(t, q.Matches[0].Optional)
	require.Len(t, q.Matches[0].Patterns, 1)

	pat := q.Matches[0].Patterns[0]
	require.Len(t, pat.Nodes, 2)
	assert.Equal(t, "a", pat.Nodes[0].Variable)
	assert.Equal(t, "agreement", pat.Nodes[0].Label)
	assert.Equal(t, "i", pat.Nodes[1].Variable)
	assert.Equal(t, "identity", pat.Nodes[1].Label)

	require.Len(t, pat.Edges, 1)
	assert.Equal(t, "HAS_PARTY", pat.Edges[0].Type)
	assert.Equal(t, Outbound, pat.Edges[0].Direction)

	require.Len(t, q.Return.Items, 2)
	assert.Equal(t, "a.id", q.Return.Items[0].Expression)
	assert.Equal(t, "agreement_id", q.Return.Items[0].Alias)
	assert.Equal(t, "i.name", q.Return.Items[1].Expression)
	assert.Equal(t, "party_name", q.Return.Items[1].Alias)
}

// ─── AC#2: OPTIONAL MATCH ────────────────────────────────────────────────────

func TestParse_OptionalMatch(t *testing.T) {
	q, err := Parse(`
		MATCH (a:agreement)
		OPTIONAL MATCH (a)-[:HAS_CONTACT]->(c:contact)
		RETURN a.id AS agreement_id, c.email AS contact_email
	`)
	require.NoError(t, err)
	require.Len(t, q.Matches, 2)

	assert.False(t, q.Matches[0].Optional, "first MATCH must not be optional")
	assert.True(t, q.Matches[1].Optional, "second clause must be OPTIONAL MATCH")
}

func TestParse_OptionalMatchDistinctNodeType(t *testing.T) {
	q, err := Parse("MATCH (a:room) OPTIONAL MATCH (a)-[:HAS_OCCUPANT]->(p:person) RETURN a.id AS room_id, p.name AS occupant_name")
	require.NoError(t, err)

	require.Len(t, q.Matches, 2)
	assert.True(t, q.Matches[1].Optional)
	assert.Equal(t, "HAS_OCCUPANT", q.Matches[1].Patterns[0].Edges[0].Type)
}

// ─── AC#3: missing RETURN clause ─────────────────────────────────────────────

func TestParse_MissingReturn(t *testing.T) {
	_, err := Parse("MATCH (a:agreement)-[:HAS_PARTY]->(i:identity)")
	require.Error(t, err)
	assert.True(t,
		strings.Contains(strings.ToUpper(err.Error()), "RETURN"),
		"error should mention RETURN, got: %v", err)
}

func TestParse_MatchFollowedByUnexpectedToken(t *testing.T) {
	_, err := Parse("MATCH (a:X) WHERE a.id = 1")
	require.Error(t, err)
}

// ─── AC#4: malformed syntax ───────────────────────────────────────────────────

func TestParse_UnmatchedParen(t *testing.T) {
	_, err := Parse("MATCH (a:agreement-[:HAS_PARTY]->(i:identity) RETURN a.id AS id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line", "error should include position info")
}

func TestParse_MissingRelType(t *testing.T) {
	_, err := Parse("MATCH (a:X)-[]->(b:Y) RETURN a.id AS id")
	require.Error(t, err)
}

func TestParse_EmptyQuery(t *testing.T) {
	_, err := Parse("")
	require.Error(t, err)

	_, err = Parse("   \n  \t  ")
	require.Error(t, err)
}

// ─── AC#5: circular relationship detection ────────────────────────────────────

func TestParse_CircularRelationship(t *testing.T) {
	_, err := Parse("MATCH (a:X)-[:R]->(a) RETURN a.id AS id")
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "circular") || strings.Contains(err.Error(), "cycle"),
		"expected circular/cycle error, got: %v", err)
}

// ─── AC#6: pure function ─────────────────────────────────────────────────────

func TestParse_PureFunction(t *testing.T) {
	// Same input called multiple times must produce identical results.
	input := "MATCH (a:agreement) RETURN a.id AS id"
	q1, err1 := Parse(input)
	q2, err2 := Parse(input)
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, q1, q2, "Parse must be deterministic")
}

// ─── AC#7: infrastructure-free (all tests above are by definition) ────────────

// ─── Additional correctness tests ────────────────────────────────────────────

func TestParse_MultipleMatchClauses(t *testing.T) {
	q, err := Parse(`
		MATCH (a:agreement)
		MATCH (a)-[:HAS_PARTY]->(i:identity)
		RETURN a.id AS agreement_id, i.name AS party_name
	`)
	require.NoError(t, err)
	assert.Len(t, q.Matches, 2)
	assert.False(t, q.Matches[0].Optional)
	assert.False(t, q.Matches[1].Optional)
}

func TestParse_InboundRelationship(t *testing.T) {
	q, err := Parse("MATCH (a:X)<-[:HAS_MEMBER]-(b:Y) RETURN a.id AS a_id")
	require.NoError(t, err)
	require.Len(t, q.Matches[0].Patterns[0].Edges, 1)
	assert.Equal(t, Inbound, q.Matches[0].Patterns[0].Edges[0].Direction)
}

func TestParse_UndirectedRelationship(t *testing.T) {
	q, err := Parse("MATCH (a:X)-[:LINKED]-(b:Y) RETURN a.id AS a_id")
	require.NoError(t, err)
	assert.Equal(t, Both, q.Matches[0].Patterns[0].Edges[0].Direction)
}

func TestParse_MultipleReturnItems(t *testing.T) {
	q, err := Parse("MATCH (a:x) RETURN a.f1 AS col1, a.f2 AS col2, a.f3 AS col3")
	require.NoError(t, err)
	require.Len(t, q.Return.Items, 3)
	assert.Equal(t, "col1", q.Return.Items[0].Alias)
	assert.Equal(t, "col2", q.Return.Items[1].Alias)
	assert.Equal(t, "col3", q.Return.Items[2].Alias)
}

func TestParse_CaseInsensitiveKeywords(t *testing.T) {
	tests := []string{
		"match (a:X) return a.id as my_id",
		"Match (a:X) Return a.id As my_id",
		"MATCH (a:X) RETURN a.id AS my_id",
	}
	for _, tc := range tests {
		q, err := Parse(tc)
		require.NoError(t, err, "query: %q", tc)
		require.NotNil(t, q)
	}
}

func TestParse_NodeWithoutLabel(t *testing.T) {
	q, err := Parse("MATCH (a)-[:R]->(b) RETURN a.id AS id")
	require.NoError(t, err)
	assert.Equal(t, "a", q.Matches[0].Patterns[0].Nodes[0].Variable)
	assert.Empty(t, q.Matches[0].Patterns[0].Nodes[0].Label)
}

func TestParse_RelationshipVariableName(t *testing.T) {
	q, err := Parse("MATCH (a:X)-[r:KNOWS]->(b:Y) RETURN a.id AS id")
	require.NoError(t, err)
	assert.Equal(t, "r", q.Matches[0].Patterns[0].Edges[0].Variable)
}

func TestParse_MultiNodeChain(t *testing.T) {
	// Three nodes in a single pattern: (a)-[:R1]->(b)-[:R2]->(c)
	q, err := Parse("MATCH (a:X)-[:R1]->(b:Y)-[:R2]->(c:Z) RETURN a.id AS id")
	require.NoError(t, err)
	pat := q.Matches[0].Patterns[0]
	assert.Len(t, pat.Nodes, 3)
	assert.Len(t, pat.Edges, 2)
}

func TestParse_CircularRelationship_IntermediateNode(t *testing.T) {
	// Cycle where repeated variable is not at Nodes[0]: (x)-[:R]->(a)-[:S]->(a)
	_, err := Parse("MATCH (x:W)-[:R]->(a:X)-[:S]->(a) RETURN x.id AS id")
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "circular") || strings.Contains(err.Error(), "cycle"),
		"expected cycle error, got: %v", err)
}
