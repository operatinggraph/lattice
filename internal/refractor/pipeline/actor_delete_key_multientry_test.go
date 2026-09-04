package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// newMultiEntryDeleteKeyPipeline mirrors newDeleteKeyPipeline (actor_delete_key_test.go)
// but installs a perEntry (multiEnvelopeFn) lens over a real guarded adapter, so the
// actor-disappearance paths' prefix-diff retraction (§4.2) runs against the substrate.
func newMultiEntryDeleteKeyPipeline(t *testing.T) *Pipeline {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	adpt := newMultiEntryTargetAdapter(t)
	return &Pipeline{
		ruleID:          "test-rule-multi",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            adpt,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
		actorDeleteKey:  func(string) string { return "child" },
		multiEnvelopeFn: fanOutEntryFn,
	}
}

// tombstoneKeys extracts the "key" field of every Delete result, asserting
// every result is in fact a Delete (no stray upsert survives an
// actor-disappearance retraction).
func tombstoneKeys(t *testing.T, results []ruleengine.EvalResult) []string {
	t.Helper()
	keys := make([]string, 0, len(results))
	for _, r := range results {
		require.True(t, r.Delete, "actor-disappearance retraction must emit only Deletes")
		k, _ := r.Keys["key"].(string)
		keys = append(keys, k)
	}
	return keys
}

func TestActorTombstone_MultiEnvelopeFn_TombstonesAllChildren(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "child.a2"}, map[string]any{"key": "child.a2", "id": "a2"}, 1))

	results, enumerated, _, err := p.evaluateForEntry(ctx, p.ruleState(), ruleengine.NodeEntry{
		CoreKVKey: deleteKeyActor,
		NodeLabel: "identity",
		IsDeleted: true,
	})
	require.NoError(t, err)
	require.Nil(t, enumerated)
	require.ElementsMatch(t, []string{"child.a1", "child.a2"}, tombstoneKeys(t, results))
}

func TestActorTombstone_MultiEnvelopeFn_NoChildren_NoResults(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)

	results, enumerated, _, err := p.evaluateForEntry(ctx, p.ruleState(), ruleengine.NodeEntry{
		CoreKVKey: deleteKeyActor,
		NodeLabel: "identity",
		IsDeleted: true,
	})
	require.NoError(t, err)
	require.Nil(t, enumerated)
	require.Empty(t, results)
}

func TestReprojectActors_MissingActor_MultiEnvelopeFn_TombstonesAllChildren(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "child.a2"}, map[string]any{"key": "child.a2", "id": "a2"}, 1))

	// deleteKeyActor is absent from Core KV (newDeleteKeyKV seeds an empty
	// CORE bucket), so this exercises the missing-actor branch inside
	// reprojectActors directly (fan-out re-evaluation; sweep Reproject does
	// not yet reach a perEntry lens — see the evaluate.go comment).
	results, err := p.reprojectActors(ctx, p.ruleState(), []string{deleteKeyActor})
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"child.a1", "child.a2"}, tombstoneKeys(t, results))
}

func TestReprojectActors_MissingActor_MultiEnvelopeFn_NoChildren_NoResults(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)

	results, err := p.reprojectActors(ctx, p.ruleState(), []string{deleteKeyActor})
	require.NoError(t, err)
	require.Empty(t, results)
}
