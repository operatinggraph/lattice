package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// helper: parse a query string and require success.
func mustParse(t *testing.T, query string) *Query {
	t.Helper()
	q, err := Parse(query)
	require.NoError(t, err)
	return q
}

// TestCompile_ColumnMappings verifies Expression, Variable, Property, and Alias are extracted correctly.
func TestCompile_ColumnMappings(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)

	require.Len(t, plan.Columns, 2)

	col0 := plan.Columns[0]
	assert.Equal(t, "agreement_id", col0.Alias)
	assert.Equal(t, "a.id", col0.Expression)
	assert.Equal(t, "a", col0.Variable)
	assert.Equal(t, "id", col0.Property)

	col1 := plan.Columns[1]
	assert.Equal(t, "party_name", col1.Alias)
	assert.Equal(t, "i.name", col1.Expression)
	assert.Equal(t, "i", col1.Variable)
	assert.Equal(t, "name", col1.Property)
}

// TestCompile_TraversalSteps verifies steps are built from MATCH pattern in order.
func TestCompile_TraversalSteps(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)

	require.Len(t, plan.Steps, 1)
	step := plan.Steps[0]
	assert.Equal(t, "a", step.FromVariable)
	assert.Equal(t, "agreement", step.FromLabel)
	assert.Equal(t, "HAS_PARTY", step.EdgeType)
	assert.Equal(t, Outbound, step.Direction)
	assert.Equal(t, "i", step.ToVariable)
	assert.Equal(t, "identity", step.ToLabel)
	assert.False(t, step.Optional)
}

// TestCompile_OptionalMatchNullable verifies variables from OPTIONAL MATCH get Nullable: true.
func TestCompile_OptionalMatchNullable(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) OPTIONAL MATCH (a)-[:HAS_CONTACT]->(c:contact) RETURN a.id AS agreement_id, i.name AS party_name, c.email AS contact_email`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)

	require.Len(t, plan.Columns, 3)
	assert.False(t, plan.Columns[0].Nullable, "agreement_id (from required MATCH) should not be nullable")
	assert.False(t, plan.Columns[1].Nullable, "party_name (from required MATCH) should not be nullable")
	assert.True(t, plan.Columns[2].Nullable, "contact_email (from OPTIONAL MATCH only) should be nullable")
}

// TestCompile_RequiredMatchNotNullable verifies variables from required MATCH get Nullable: false.
func TestCompile_RequiredMatchNotNullable(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)

	for _, col := range plan.Columns {
		assert.False(t, col.Nullable, "all columns from required MATCH should be non-nullable")
	}
}

// TestCompile_IsKeyMarked verifies the into.key alias is flagged as IsKey: true.
func TestCompile_IsKeyMarked(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)

	require.Len(t, plan.Columns, 2)
	assert.True(t, plan.Columns[0].IsKey, "agreement_id should be a key column")
	assert.False(t, plan.Columns[1].IsKey, "party_name should not be a key column")
}

// TestCompile_CompositeKey verifies composite into.key marks multiple columns as IsKey: true.
func TestCompile_CompositeKey(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id, i.name AS party_name`)
	plan, err := Compile(q, []string{"agreement_id", "party_name"})
	require.NoError(t, err)

	require.Len(t, plan.Columns, 2)
	assert.True(t, plan.Columns[0].IsKey, "agreement_id should be a key column")
	assert.True(t, plan.Columns[1].IsKey, "party_name should be a key column")
}

// TestCompile_EmptyAlias verifies an error is returned when a ReturnItem has an empty alias.
func TestCompile_EmptyAlias(t *testing.T) {
	// Construct a hand-crafted Query with an empty alias to bypass the parser's AS enforcement.
	q := &Query{
		Matches: []MatchClause{
			{Patterns: []Pattern{{Nodes: []NodePattern{{Variable: "a", Label: "agreement"}}}}},
		},
		Return: ReturnClause{Items: []ReturnItem{
			{Expression: "a.id", Alias: ""},
		}},
	}
	_, err := Compile(q, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "alias")
}

// TestCompile_MissingKeyField verifies an error is returned when a keyField is not a RETURN alias.
func TestCompile_MissingKeyField(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id`)
	_, err := Compile(q, []string{"nonexistent_col"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nonexistent_col")
}

// TestCompile_AnchorLabel verifies AnchorLabel and AnchorVariable are set from the first node of the first required MATCH.
func TestCompile_AnchorLabel(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)
	assert.Equal(t, "agreement", plan.AnchorLabel)
	assert.Equal(t, "a", plan.AnchorVariable)
}

// TestCompile_MultiHopSteps verifies two TraversalSteps are produced for a 3-node, 2-edge MATCH.
func TestCompile_MultiHopSteps(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity)-[:HAS_CONTACT]->(c:contact) RETURN a.id AS agreement_id, i.name AS party_name, c.email AS contact_email`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)

	require.Len(t, plan.Steps, 2)

	assert.Equal(t, "a", plan.Steps[0].FromVariable)
	assert.Equal(t, "agreement", plan.Steps[0].FromLabel)
	assert.Equal(t, "HAS_PARTY", plan.Steps[0].EdgeType)
	assert.Equal(t, "i", plan.Steps[0].ToVariable)
	assert.Equal(t, "identity", plan.Steps[0].ToLabel)

	assert.Equal(t, "i", plan.Steps[1].FromVariable)
	assert.Equal(t, "identity", plan.Steps[1].FromLabel)
	assert.Equal(t, "HAS_CONTACT", plan.Steps[1].EdgeType)
	assert.Equal(t, "c", plan.Steps[1].ToVariable)
	assert.Equal(t, "contact", plan.Steps[1].ToLabel)
}

// TestCompile_OptionalStepMarked verifies OPTIONAL MATCH steps are flagged as Optional: true.
func TestCompile_OptionalStepMarked(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) OPTIONAL MATCH (a)-[:HAS_CONTACT]->(c:contact) RETURN a.id AS agreement_id, c.email AS contact_email`)
	plan, err := Compile(q, []string{"agreement_id"})
	require.NoError(t, err)

	require.Len(t, plan.Steps, 2)
	assert.False(t, plan.Steps[0].Optional, "required MATCH step should not be optional")
	assert.True(t, plan.Steps[1].Optional, "OPTIONAL MATCH step should be optional")
}

// TestCompile_AllOptionalMatch verifies that a query with only OPTIONAL MATCH clauses is rejected.
func TestCompile_AllOptionalMatch(t *testing.T) {
	q := &Query{
		Matches: []MatchClause{
			{
				Optional: true,
				Patterns: []Pattern{{
					Nodes: []NodePattern{{Variable: "a", Label: "agreement"}, {Variable: "i", Label: "identity"}},
					Edges: []EdgePattern{{Type: "HAS_PARTY", Direction: Outbound}},
				}},
			},
		},
		Return: ReturnClause{Items: []ReturnItem{
			{Expression: "a.id", Alias: "agreement_id"},
		}},
	}
	_, err := Compile(q, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "required MATCH")
}

// TestCompile_EmptyKeyFields verifies nil/empty keyFields is allowed (no key column required).
func TestCompile_EmptyKeyFields(t *testing.T) {
	q := mustParse(t, `MATCH (a:agreement)-[:HAS_PARTY]->(i:identity) RETURN a.id AS agreement_id`)
	plan, err := Compile(q, nil)
	require.NoError(t, err)
	assert.False(t, plan.Columns[0].IsKey)
}
