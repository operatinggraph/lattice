package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// fanOutEntryFn is a minimal MultiEnvelopeFn: it splits row["ids"] (a []any
// of strings) into one Envelope per entry, keyed "child.<id>". It stands in
// for projection.OutputDescriptor.EntryEnvelopeFn without importing the
// projection package (which would form an import cycle with pipeline).
func fanOutEntryFn(row, _, _ map[string]any) ([]Envelope, error) {
	ids, _ := row["ids"].([]any)
	entries := make([]Envelope, 0, len(ids))
	for _, raw := range ids {
		id, _ := raw.(string)
		if id == "" {
			return nil, errors.New("fanOutEntryFn: empty id")
		}
		entries = append(entries, Envelope{
			Keys: map[string]any{"key": "child." + id},
			Row:  map[string]any{"key": "child." + id, "id": id},
		})
	}
	return entries, nil
}

func TestSetMultiEnvelopeFn_MutuallyExclusiveWithEnvelopeFn(t *testing.T) {
	p := &Pipeline{}

	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	require.NotNil(t, p.envelopeFn)

	p.SetMultiEnvelopeFn(fanOutEntryFn)
	require.NotNil(t, p.multiEnvelopeFn)
	require.Nil(t, p.envelopeFn, "installing a MultiEnvelopeFn must clear any installed EnvelopeFn")

	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	require.NotNil(t, p.envelopeFn)
	require.Nil(t, p.multiEnvelopeFn, "installing an EnvelopeFn must clear any installed MultiEnvelopeFn")
}

// singleRowEngine parses a cypher that returns exactly one row per actor
// carrying a fixed "ids" list — enough to drive executeFullForActor's
// multiEnvelopeFn dispatch without a live graph.
func singleRowEngine(t *testing.T) (*full.Engine, ruleengine.CompiledRule) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(`MATCH (identity:identity {key: $actorKey}) RETURN identity.key AS actorKey`)
	require.NoError(t, err)
	return eng, cr
}

func TestExecuteFullForActor_MultiEnvelopeFn_EmitsPerEntryResults(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityDddddddd"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(row, keys, params map[string]any) ([]Envelope, error) {
		return fanOutEntryFn(map[string]any{"ids": []any{"a1", "a2", "a3"}}, keys, params)
	}

	p := &Pipeline{
		ruleID:          "rule-multi",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Len(t, results, 3)
	got := map[string]bool{}
	for _, r := range results {
		require.False(t, r.Delete)
		got[r.Keys["key"].(string)] = true
	}
	require.True(t, got["child.a1"])
	require.True(t, got["child.a2"])
	require.True(t, got["child.a3"])
}

func TestExecuteFullForActor_MultiEnvelopeFn_ZeroEntries_NoResultsNoError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityEeeeeeee"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return nil, nil
	}

	p := &Pipeline{
		ruleID:          "rule-multi-empty",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Empty(t, results, "zero real entries must write nothing and delete nothing — retraction is a later increment")
}

func TestExecuteFullForActor_MultiEnvelopeFn_SkipProjection_DropsRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityFfffffff"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return nil, ErrSkipProjection
	}

	p := &Pipeline{
		ruleID:          "rule-multi-skip",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	results, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.NoError(t, err)
	require.Empty(t, results)
}

func TestExecuteFullForActor_MultiEnvelopeFn_Error_FailsActorClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityGggggggg"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	wantErr := errors.New("boom: malformed key token")
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return nil, wantErr
	}

	p := &Pipeline{
		ruleID:          "rule-multi-err",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}

	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}
	_, err := p.executeFullForActor(ctx, identityKey, nodeProps)
	require.Error(t, err)
	require.ErrorContains(t, err, "boom: malformed key token")
}
