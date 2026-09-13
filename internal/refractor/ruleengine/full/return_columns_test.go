package full

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// ReturnColumns must answer with the exact same names, in the same order, that
// the RETURN clause's item-naming rule (itemAliasAt) produces: an alias wins,
// a bare variable falls back to its own name, a property access to its key,
// and anything else to the positional _col<i> auto-alias.
func TestReturnColumns_Alias(t *testing.T) {
	cr := mustCompile(t, "MATCH (i:identity) RETURN i.name AS displayName")
	cols, ok := cr.ReturnColumns()
	require.True(t, ok)
	require.Equal(t, []string{"displayName"}, cols)
}

func TestReturnColumns_BareVariableRef(t *testing.T) {
	cr := mustCompile(t, "MATCH (i:identity) RETURN i")
	cols, ok := cr.ReturnColumns()
	require.True(t, ok)
	require.Equal(t, []string{"i"}, cols)
}

func TestReturnColumns_PropertyAccess(t *testing.T) {
	cr := mustCompile(t, "MATCH (i:identity) RETURN i.name")
	cols, ok := cr.ReturnColumns()
	require.True(t, ok)
	require.Equal(t, []string{"name"}, cols)
}

func TestReturnColumns_LiteralOrFunctionAutoAliasesPositionally(t *testing.T) {
	cr := mustCompile(t, "MATCH (i:identity) RETURN i.name AS name, 1 + 1")
	cols, ok := cr.ReturnColumns()
	require.True(t, ok)
	require.Equal(t, []string{"name", "_col1"}, cols)
}

// A WITH clause upstream of RETURN must not leak its own aliases into
// ReturnColumns: only the RETURN clause's own item names win.
func TestReturnColumns_WithThenReturn_ReturnClauseWins(t *testing.T) {
	cr := mustCompile(t, "MATCH (i:identity) WITH i.name AS withAlias RETURN withAlias AS finalName")
	cols, ok := cr.ReturnColumns()
	require.True(t, ok)
	require.Equal(t, []string{"finalName"}, cols)
}

func TestReturnColumns_NoReturnClause(t *testing.T) {
	// A directly-constructed CompiledRule with no Query at all, and a parsed
	// rule with no RETURN, must both report ok=false.
	var cr *CompiledRule
	cols, ok := cr.ReturnColumns()
	require.False(t, ok)
	require.Nil(t, cols)
}

func mustCompile(t *testing.T, body string) *CompiledRule {
	t.Helper()
	compiled, err := New().Parse(body)
	require.NoError(t, err)
	cr, ok := compiled.(*CompiledRule)
	require.True(t, ok)
	return cr
}

// SpecLabels.Columns must be exactly ReturnColumns()'s first return for the
// same body — one parse, one derivation, two facts of it.
func TestSpecLabels_ColumnsMatchesReturnColumns(t *testing.T) {
	body := "MATCH (i:identity) RETURN i.name AS name, i.key, 1 + 1"
	facts, err := SpecLabels(body)
	require.NoError(t, err)

	cr := mustCompile(t, body)
	want, ok := cr.ReturnColumns()
	require.True(t, ok)
	require.Equal(t, want, facts.Columns)
}

// TestExec_ReturnColumnsPinnedAgainstExecutedRowKeys is the one-derivation
// proof: it drives a query with an aliased item, a bare VariableRef, a
// PropertyAccess and a positionally-auto-aliased literal expression through
// the SAME engine's Execute path, and asserts the resulting row's keys are
// exactly ReturnColumns() — the executor's itemAliasAt and
// (*CompiledRule).ReturnColumns() must never disagree, because they are the
// same function.
func TestExec_ReturnColumnsPinnedAgainstExecutedRowKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"name": "alice"})

	body := `MATCH (i:identity {key: $k}) RETURN i.name AS displayName, i, i.name, 1 + 1`

	eng := New()
	compiled, err := eng.Parse(body)
	require.NoError(t, err)
	cr, ok := compiled.(*CompiledRule)
	require.True(t, ok)

	wantCols, ok := cr.ReturnColumns()
	require.True(t, ok)
	require.Equal(t, []string{"displayName", "i", "name", "_col3"}, wantCols)

	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{"k": vtxKey(reg, "alice")}},
		adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1)

	gotKeys := make(map[string]struct{}, len(out[0].Values))
	for k := range out[0].Values {
		gotKeys[k] = struct{}{}
	}
	for _, col := range wantCols {
		_, present := gotKeys[col]
		require.True(t, present, "row keys %v must contain ReturnColumns() column %q", gotKeys, col)
	}
}
