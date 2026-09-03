package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// scopeSpec is the cypher the scope tests evaluate: one row per task the actor
// holds, each carrying its own task key, so a fixture can drive N rows through
// ONE actor evaluation. The envelope below keys each row by its own task, so
// the multi-row output-key collision guard is not what these tests measure.
const scopeSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
RETURN
  identity.key AS actorKey,
  task.key AS taskRef
`

// scopeTestParamKey is the params entry the scope publishes and the envelope
// reads back. The dotted spelling mirrors the real personal-lens scope keys:
// no cypher `$name` can be spelled this way, so a scope entry can never be
// mistaken for a declared parameter.
const scopeTestParamKey = "pipeline.test.scope"

// perTaskEnvelopeFn keys each row by its own taskRef, so N matched tasks
// produce N distinct output keys. It asserts, per row, that the scope entry
// reached it and that the evaluation's own parameters are still intact
// alongside it.
func perTaskEnvelopeFn(t *testing.T, wantScope string) EnvelopeFn {
	t.Helper()
	return func(row, _, params map[string]any) (map[string]any, map[string]any, error) {
		got, ok := params[scopeTestParamKey].(string)
		if !ok {
			return nil, nil, fmt.Errorf("row %v: the scope entry did not reach the envelope: %v", row["taskRef"], params)
		}
		if got != wantScope {
			return nil, nil, fmt.Errorf("row %v: scope entry %q, want %q", row["taskRef"], got, wantScope)
		}
		if actorKey, _ := params["actorKey"].(string); actorKey == "" {
			return nil, nil, errors.New("the evaluation's own $actorKey must survive the scope merge")
		}
		taskRef, _ := row["taskRef"].(string)
		if taskRef == "" {
			return nil, nil, ErrSkipProjection
		}
		return map[string]any{"key": taskRef, "actor": params["actorKey"]}, map[string]any{"key": taskRef}, nil
	}
}

// scopeNanoID pads a readable stem into a valid 20-character Contract #1
// NanoID — the alphabet excludes I, l, O and 0, so the stems here carry none of them.
func scopeNanoID(stem string) string {
	const width = 20
	if len(stem) > width {
		return stem[:width]
	}
	return stem + strings.Repeat("a", width-len(stem))
}

// newScopePipeline builds a live full-engine pipeline over a real graph:
// one identity with taskCount assigned tasks.
func newScopePipeline(t *testing.T, identityStem string, taskCount int) (*Pipeline, string) {
	t.Helper()
	coreKV, adjKV, _ := newCollisionKVs(t)
	identityID := scopeNanoID(identityStem)
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{"name": "scope"})
	for i := 0; i < taskCount; i++ {
		taskID := scopeNanoID(fmt.Sprintf("ScopeTask%d", i+1))
		writeCollisionVertex(t, coreKV, "vtx.task."+taskID, "task", map[string]any{"status": "open"})
		buildCollisionEdge(t, adjKV, "assignedTo", "task", taskID, "identity", identityID)
	}

	eng := full.New()
	cr, err := eng.Parse(scopeSpec)
	require.NoError(t, err)

	return &Pipeline{
		ruleID:     "rule-envelope-scope",
		coreKV:     coreKV,
		adjKV:      adjKV,
		engineKind: ruleengine.EngineFull,
		fullEngine: eng,
		fullCR:     cr,
	}, identityKey
}

// TestExecuteFullForActor_EnvelopeScope_OncePerEvaluation is the whole point
// of the hook: whatever the actor's row count, the scope is computed exactly
// ONCE per evaluation and every row's envelope answers from it. An actor the
// engine returns no rows for pays nothing.
func TestExecuteFullForActor_EnvelopeScope_OncePerEvaluation(t *testing.T) {
	const taskCount = 4
	p, identityKey := newScopePipeline(t, "ScopeActor1", taskCount)

	calls := 0
	p.SetEnvelopeFn(perTaskEnvelopeFn(t, "scoped-value"))
	p.SetEnvelopeScope(func(ctx context.Context, params map[string]any) (map[string]any, error) {
		calls++
		require.NotNil(t, ctx, "the scope must receive the evaluation's own ctx")
		require.Equal(t, identityKey, params["actorKey"], "the scope reads the evaluation's parameters")
		return map[string]any{scopeTestParamKey: "scoped-value"}, nil
	})

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(context.Background(), p.ruleState(), identityKey, nodeProps, "")
	require.NoError(t, err)
	require.Len(t, results, taskCount, "every matched task must project one row")
	require.Equal(t, 1, calls, "%d rows must cost exactly one scope call", taskCount)
}

// TestExecuteFullForActor_EnvelopeScope_NoRowsNoCall pins the other half of
// the once-per-evaluation contract: the scope is a read, and an actor with
// nothing to gate must not pay for one.
func TestExecuteFullForActor_EnvelopeScope_NoRowsNoCall(t *testing.T) {
	p, _ := newScopePipeline(t, "ScopeActor2", 0)

	calls := 0
	p.SetEnvelopeFn(perTaskEnvelopeFn(t, "scoped-value"))
	p.SetEnvelopeScope(func(context.Context, map[string]any) (map[string]any, error) {
		calls++
		return map[string]any{scopeTestParamKey: "scoped-value"}, nil
	})

	// An actor the graph holds no vertex for produces no binding at all, so
	// the engine returns zero rows.
	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(context.Background(), p.ruleState(), "vtx.identity."+scopeNanoID("ScopeAbsentActor"), nodeProps, "")
	require.NoError(t, err)
	require.Empty(t, results)
	require.Zero(t, calls, "an evaluation with no rows must not compute a scope")
}

// TestExecuteFullForActor_EnvelopeScope_ErrorFailsTheEvaluation pins the
// fail-closed posture: a scope that cannot be computed is a decision input
// that could not be READ, so the evaluation fails and no row is produced —
// never a silent fall-through to an unscoped envelope.
func TestExecuteFullForActor_EnvelopeScope_ErrorFailsTheEvaluation(t *testing.T) {
	p, identityKey := newScopePipeline(t, "ScopeActor3", 3)

	envelopeCalls := 0
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		envelopeCalls++
		return row, keys, nil
	})
	p.SetEnvelopeScope(func(context.Context, map[string]any) (map[string]any, error) {
		return nil, errors.New("grant store unreachable")
	})

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(context.Background(), p.ruleState(), identityKey, nodeProps, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "envelope scope")
	require.Contains(t, err.Error(), "grant store unreachable")
	require.Nil(t, results, "a failed scope must produce no results at all")
	require.Zero(t, envelopeCalls, "no row may be enveloped once the scope has failed")
}

// TestExecuteFullForActor_EnvelopeScope_LeavesEngineParamsAlone pins the
// params-copy contract: the entries a scope returns reach the envelope and
// nothing else. The engine evaluated against the original map, so a scope
// entry landing in it would be a value a cypher could bind by name.
//
// The discriminating part is the RETAINED map. Nothing reads the engine's
// parameters after the scope runs — executeBranches is already done — so
// asserting only on what the envelope received cannot tell a copy from an
// in-place merge: both leave the envelope holding the scope entries. The scope
// therefore keeps a reference to the map it was handed, and the assertions are
// that the envelope's map is a DIFFERENT object and that the retained one
// never gained the scope's key. An in-place merge fails both.
func TestExecuteFullForActor_EnvelopeScope_LeavesEngineParamsAlone(t *testing.T) {
	p, identityKey := newScopePipeline(t, "ScopeActor4", 2)

	var seen []map[string]any
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		seen = append(seen, params)
		taskRef, _ := row["taskRef"].(string)
		return map[string]any{"key": taskRef}, map[string]any{"key": taskRef}, nil
	})
	var handedToScope map[string]any
	p.SetEnvelopeScope(func(_ context.Context, params map[string]any) (map[string]any, error) {
		require.NotContains(t, params, scopeTestParamKey, "the scope must be handed the evaluation's own parameters, unscoped")
		handedToScope = params
		return map[string]any{scopeTestParamKey: "scoped-value"}, nil
	})

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	_, err := p.executeFullForActor(context.Background(), p.ruleState(), identityKey, nodeProps, "")
	require.NoError(t, err)
	require.Len(t, seen, 2)
	require.NotNil(t, handedToScope, "the scope must have run, or the assertions below are vacuous")

	require.NotContains(t, handedToScope, scopeTestParamKey,
		"the map the ENGINE evaluated against must never gain a scope entry — a cypher could bind it by name")
	for _, params := range seen {
		require.Equal(t, "scoped-value", params[scopeTestParamKey])
		require.Equal(t, identityKey, params["actorKey"])
		require.NotEqual(t, fmt.Sprintf("%p", handedToScope), fmt.Sprintf("%p", params),
			"the envelope must see a COPY, never the engine's own parameters map")
	}
	require.Equal(t, fmt.Sprintf("%p", seen[0]), fmt.Sprintf("%p", seen[1]),
		"every row of one evaluation shares the one params copy")
}

// TestExecuteFullForActor_NoEnvelopeScope_LeavesRowsUnscoped pins that an
// unscoped pipeline is untouched: with no scope installed the envelope sees
// exactly the evaluation's own parameters.
func TestExecuteFullForActor_NoEnvelopeScope_LeavesRowsUnscoped(t *testing.T) {
	p, identityKey := newScopePipeline(t, "ScopeActor5", 2)

	rows := 0
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		rows++
		require.NotContains(t, params, scopeTestParamKey)
		taskRef, _ := row["taskRef"].(string)
		return map[string]any{"key": taskRef}, map[string]any{"key": taskRef}, nil
	})
	require.False(t, p.HasEnvelopeScope())

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	_, err := p.executeFullForActor(context.Background(), p.ruleState(), identityKey, nodeProps, "")
	require.NoError(t, err)
	require.Equal(t, 2, rows)
}

func TestSetEnvelopeScope_NilClears(t *testing.T) {
	p := &Pipeline{}
	require.False(t, p.HasEnvelopeScope())

	p.SetEnvelopeScope(func(context.Context, map[string]any) (map[string]any, error) { return nil, nil })
	require.True(t, p.HasEnvelopeScope())

	p.SetEnvelopeScope(nil)
	require.False(t, p.HasEnvelopeScope())
}

// TestSetEnvelope_ClearsTheScope pins that a scope belongs to the envelope
// that installed it: replacing (or clearing) either envelope drops it. A scope
// surviving an envelope swap would hand the NEW envelope decision inputs
// computed for the old one — on a security-plane envelope reading params by
// name, a set of admissions belonging to something else.
func TestSetEnvelope_ClearsTheScope(t *testing.T) {
	scope := func(context.Context, map[string]any) (map[string]any, error) { return nil, nil }
	envelope := func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	}

	t.Run("installing an EnvelopeFn clears it", func(t *testing.T) {
		p := &Pipeline{}
		p.SetEnvelopeFn(envelope)
		p.SetEnvelopeScope(scope)
		require.True(t, p.HasEnvelopeScope())

		p.SetEnvelopeFn(envelope)
		require.False(t, p.HasEnvelopeScope())
	})

	t.Run("clearing the EnvelopeFn clears it", func(t *testing.T) {
		p := &Pipeline{}
		p.SetEnvelopeFn(envelope)
		p.SetEnvelopeScope(scope)
		require.True(t, p.HasEnvelopeScope())

		p.SetEnvelopeFn(nil)
		require.False(t, p.HasEnvelopeScope())
	})

	t.Run("installing a MultiEnvelopeFn clears it", func(t *testing.T) {
		p := &Pipeline{}
		p.SetEnvelopeFn(envelope)
		p.SetEnvelopeScope(scope)
		require.True(t, p.HasEnvelopeScope())

		p.SetMultiEnvelopeFn(fanOutEntryFn)
		require.False(t, p.HasEnvelopeScope())
	})

	t.Run("clearing the MultiEnvelopeFn clears it", func(t *testing.T) {
		p := &Pipeline{}
		p.SetMultiEnvelopeFn(fanOutEntryFn)
		p.SetEnvelopeScope(scope)
		require.True(t, p.HasEnvelopeScope())

		p.SetMultiEnvelopeFn(nil)
		require.False(t, p.HasEnvelopeScope())
	})
}
