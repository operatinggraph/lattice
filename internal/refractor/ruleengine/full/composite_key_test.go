// Full-engine multi-column projection key (the GrantTable producer fix, D1.3).
//
// applyReturn historically put only the first RETURN item in the projection
// key map, so a composite-key lens (a GrantTable lens keyed on
// actor_id/anchor_id/grant_source) handed the adapter a one-column key and the
// GrantWriterAdapter errored "anchor_id absent" on every row — the live
// capabilityReadGrants lens never populated actor_read_grants. These tests pin
// the threaded-KeyColumns build (every key column present), the byte-identical
// single-key behavior, the legacy first-item fallback when un-threaded, and the
// fail-closed activation validation.
package full_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// withKeyColumns parses body, type-asserts the *full.CompiledRule, and sets its
// KeyColumns — exactly what cmd/refractor does at activation from Rule.Into.Key.
func withKeyColumns(t *testing.T, eng *full.Engine, body string, cols []string) ruleengine.CompiledRule {
	t.Helper()
	cr, err := eng.Parse(body)
	require.NoError(t, err)
	fcr, ok := cr.(*full.CompiledRule)
	require.True(t, ok, "expected *full.CompiledRule")
	fcr.KeyColumns = cols
	return cr
}

// TestExecuteWith_CompositeKey_BuildsAllColumns is the producer fix: with the
// three grant key columns threaded, every projection row's Key map carries all
// three (matching Values) — the shape GrantWriterAdapter.Upsert requires.
func TestExecuteWith_CompositeKey_BuildsAllColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := contractStartKVs(t)
	aliceKey := contractPutVertex(t, coreKV, "identity", "alice", map[string]any{"name": "alice"})

	eng := full.New()
	cr := withKeyColumns(t, eng, bootstrap.CapabilityReadGrantsLensDefinition().CypherRule,
		[]string{"actor_id", "anchor_id", "grant_source"})

	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{}}, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1)

	id := nanoFromVertexKey(t, aliceKey)
	row := out[0]
	// Every key column present in the KEY map (not just Values) — the gap.
	require.Equal(t, id, row.Key["actor_id"], "actor_id in the key map")
	require.Equal(t, id, row.Key["anchor_id"], "anchor_id in the key map (the column that was absent)")
	require.Equal(t, "cap-read", row.Key["grant_source"], "grant_source in the key map")
	require.Len(t, row.Key, 3, "the key map carries exactly the three composite columns")
	// Key mirrors Values for every key column.
	for _, col := range []string{"actor_id", "anchor_id", "grant_source"} {
		require.Equal(t, row.Values[col], row.Key[col], "key column %q mirrors its value", col)
	}
}

// TestExecuteWith_SingleKeyColumn_Unchanged: a single-key lens projects the same
// one-column key whether KeyColumns is threaded ([id]) or left unset (first-item
// fallback) — byte-identical, so every shipped single-key lens is unaffected.
func TestExecuteWith_SingleKeyColumn_Unchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := contractStartKVs(t)
	aliceKey := contractPutVertex(t, coreKV, "identity", "alice", map[string]any{"name": "alice"})

	const body = `MATCH (p:identity) RETURN p.key AS id, p.name AS name`
	eng := full.New()

	run := func(cr ruleengine.CompiledRule) ruleengine.ProjectionResult {
		out, err := eng.ExecuteWith(context.Background(), cr,
			ruleengine.EventContext{Parameters: map[string]any{}}, adjKV, coreKV)
		require.NoError(t, err)
		require.Len(t, out, 1)
		return out[0]
	}

	threaded := run(withKeyColumns(t, eng, body, []string{"id"}))
	unthreaded := run(withKeyColumns(t, eng, body, nil))

	require.Equal(t, map[string]any{"id": aliceKey}, threaded.Key, "threaded single-key map")
	require.Equal(t, threaded.Key, unthreaded.Key, "fallback produces the identical single-column key")
}

// TestExecuteWith_NoKeyColumns_FirstItemFallback pins the legacy behavior (and
// documents the bug): with KeyColumns unset, a composite-key cypher yields a
// one-column key (just the first RETURN item) — which the GrantWriterAdapter
// rejects. The fix is to thread the key columns; this guards the fallback path
// used by directly-constructed (test) rules.
func TestExecuteWith_NoKeyColumns_FirstItemFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := contractStartKVs(t)
	contractPutVertex(t, coreKV, "identity", "alice", map[string]any{"name": "alice"})

	eng := full.New()
	cr := withKeyColumns(t, eng, bootstrap.CapabilityReadGrantsLensDefinition().CypherRule, nil)

	out, err := eng.ExecuteWith(context.Background(), cr,
		ruleengine.EventContext{Parameters: map[string]any{}}, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Key, 1, "legacy fallback keys on the first RETURN item only")
	require.Contains(t, out[0].Key, "actor_id", "the first RETURN item is actor_id")
	require.NotContains(t, out[0].Key, "anchor_id", "the composite columns are absent (the bug the fix closes)")
}

func TestValidateKeyColumns(t *testing.T) {
	eng := full.New()
	parse := func(body string, cols []string) *full.CompiledRule {
		cr, err := eng.Parse(body)
		require.NoError(t, err)
		fcr := cr.(*full.CompiledRule)
		fcr.KeyColumns = cols
		return fcr
	}

	t.Run("all key columns are RETURN aliases", func(t *testing.T) {
		cr := parse(bootstrap.CapabilityReadGrantsLensDefinition().CypherRule,
			[]string{"actor_id", "anchor_id", "grant_source"})
		require.NoError(t, cr.ValidateKeyColumns())
	})

	t.Run("a key column absent from RETURN fails closed", func(t *testing.T) {
		cr := parse(bootstrap.CapabilityReadGrantsLensDefinition().CypherRule,
			[]string{"actor_id", "anchor_id", "missing_col"})
		err := cr.ValidateKeyColumns()
		require.Error(t, err)
		require.Contains(t, err.Error(), "missing_col")
	})

	t.Run("no key columns declared validates trivially", func(t *testing.T) {
		cr := parse(`MATCH (p:identity) RETURN p.key AS id`, nil)
		require.NoError(t, cr.ValidateKeyColumns())
	})

	t.Run("auto-aliased key column is recognized", func(t *testing.T) {
		// p.key auto-aliases to "key"; declaring it as the key must validate.
		cr := parse(`MATCH (p:identity) RETURN p.key`, []string{"key"})
		require.NoError(t, cr.ValidateKeyColumns())
	})

	t.Run("envelope-lens synthesized key is not a RETURN alias", func(t *testing.T) {
		// An actor-aggregate lens RETURNs actorKey + a body column and has its
		// projection key ("key", e.g. cap.<actor>) synthesized by the envelope at
		// write time — so Into.Key=["key"] is NOT a RETURN alias. Threading +
		// validating it would wrongly fail activation: this is exactly why
		// cmd/refractor gates envelope lenses out of KeyColumns threading. The
		// engine never sees KeyColumns for them; pinned here so the trap can't
		// reappear if the gate is removed.
		cr := parse(`MATCH (p:identity {key: $actorKey}) RETURN p.key AS actorKey`,
			[]string{"key"})
		err := cr.ValidateKeyColumns()
		require.Error(t, err)
		require.Contains(t, err.Error(), "key")
	})
}
