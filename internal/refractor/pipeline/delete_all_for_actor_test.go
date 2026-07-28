package pipeline

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
)

// TestDeleteAllForActor_DeletesEveryChildKey proves the shred-site mechanism
// (cap-read-per-anchor-grant-keys-design.md §4.2 point (d)): every live key
// under actorKey's perEntry prefix is enumerated and deleted, against a real
// guarded embedded-NATS adapter — the same fixture increments 3-4 use for
// multiEntryRetractions.
func TestDeleteAllForActor_DeletesEveryChildKey(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "child.a2"}, map[string]any{"key": "child.a2", "id": "a2"}, 1))

	err := p.DeleteAllForActor(ctx, deleteKeyActor, math.MaxInt64)
	require.NoError(t, err)

	reader := p.adpt.(interface {
		GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error)
	})
	_, live1, err := reader.GetRow(ctx, map[string]any{"key": "child.a1"})
	require.NoError(t, err)
	require.False(t, live1, "child.a1 must be tombstoned")
	_, live2, err := reader.GetRow(ctx, map[string]any{"key": "child.a2"})
	require.NoError(t, err)
	require.False(t, live2, "child.a2 must be tombstoned")
}

// TestDeleteAllForActor_NoChildren_NoOp proves an actor with no per-anchor
// keys (e.g. never granted anything) shreds cleanly with no error.
func TestDeleteAllForActor_NoChildren_NoOp(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)

	require.NoError(t, p.DeleteAllForActor(ctx, deleteKeyActor, math.MaxInt64))
}

// TestDeleteAllForActor_AlreadyTombstoned_Idempotent proves re-shredding an
// actor whose keys are already tombstoned (e.g. a redelivered keyShredded
// event) is harmless — Delete is idempotent per key.
func TestDeleteAllForActor_AlreadyTombstoned_Idempotent(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))
	require.NoError(t, p.DeleteAllForActor(ctx, deleteKeyActor, math.MaxInt64))

	require.NoError(t, p.DeleteAllForActor(ctx, deleteKeyActor, math.MaxInt64), "re-shredding must not error")
}

// TestDeleteAllForActor_AdapterNotPrefixKeyLister_ErrorsClosed proves the
// same fail-closed refusal multiEntryRetractions already enforces: a target
// that cannot enumerate keys by prefix cannot shred a perEntry actor's
// children, and must say so rather than silently no-op (which would leave
// per-anchor grants live post-shred — a privacy leak). multiEnvelopeFn is set
// so this exercises the adapter-capability refusal specifically, not the
// perEntry-lens-kind refusal (covered by its own test below).
func TestDeleteAllForActor_AdapterNotPrefixKeyLister_ErrorsClosed(t *testing.T) {
	p := &Pipeline{adpt: fakeBareAdapter{}, actorDeleteKey: func(string) string { return "child" }, multiEnvelopeFn: fanOutEntryFn}
	err := p.DeleteAllForActor(context.Background(), "vtx.identity.x", math.MaxInt64)
	require.Error(t, err)
	require.ErrorContains(t, err, "cannot enumerate keys by prefix")
}

// TestDeleteAllForActor_NotPerEntryLens_RefusesClosed proves a lens with no
// MultiEnvelopeFn installed (a doc-mode lens, or a PerEntry-vs-lens-kind
// operator misconfiguration) refuses loudly instead of listing zero keys
// under a prefix that was never how its row is keyed and reporting a
// falsely-clean shred.
func TestDeleteAllForActor_NotPerEntryLens_RefusesClosed(t *testing.T) {
	p := &Pipeline{ruleID: "doc-mode-rule", adpt: newMultiEntryTargetAdapter(t), actorDeleteKey: func(string) string { return "cap.actor" }}
	err := p.DeleteAllForActor(context.Background(), "vtx.identity.x", math.MaxInt64)
	require.Error(t, err)
	require.ErrorContains(t, err, "not a perEntry lens")
}

// TestDeleteAllForActor_ScopedToActorPrefix_SiblingActorSurvives proves the
// actorKey argument actually binds the listed prefix — deleting for one
// actor must never reach a sibling actor's keys under the same lens target.
// actorDeleteKey here derives the prefix from its argument (unlike the
// shared newMultiEntryDeleteKeyPipeline fixture's constant "child" stub),
// so this is the test that actually proves the argument→prefix binding.
func TestDeleteAllForActor_ScopedToActorPrefix_SiblingActorSurvives(t *testing.T) {
	ctx := context.Background()
	const actorA = "vtx.identity.AaaaaaaaaaaaaaaaaaaA"
	const actorB = "vtx.identity.BbbbbbbbbbbbbbbbbbbB"
	p := &Pipeline{
		ruleID:          "test-rule-scoped",
		adpt:            newMultiEntryTargetAdapter(t),
		actorDeleteKey:  func(actor string) string { return "cap." + actor },
		multiEnvelopeFn: fanOutEntryFn,
	}
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "cap." + actorA + ".anchor1"}, map[string]any{"key": "cap." + actorA + ".anchor1"}, 1))
	require.NoError(t, p.adpt.Upsert(ctx, map[string]any{"key": "cap." + actorB + ".anchor1"}, map[string]any{"key": "cap." + actorB + ".anchor1"}, 1))

	require.NoError(t, p.DeleteAllForActor(ctx, actorA, math.MaxInt64))

	reader := p.adpt.(interface {
		GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error)
	})
	_, liveA, err := reader.GetRow(ctx, map[string]any{"key": "cap." + actorA + ".anchor1"})
	require.NoError(t, err)
	require.False(t, liveA, "actor A's own key must be deleted")
	_, liveB, err := reader.GetRow(ctx, map[string]any{"key": "cap." + actorB + ".anchor1"})
	require.NoError(t, err)
	require.True(t, liveB, "actor B's key must survive — DeleteAllForActor must never delete outside actorKey's own prefix")
}

// failOnKeyAdapter wraps a real adapter, injecting a delete failure for one
// specific key while recording every Delete call it receives.
type failOnKeyAdapter struct {
	adapter.Adapter
	lister      adapter.PrefixKeyLister
	failKey     string
	deleteCalls []string
}

func (f *failOnKeyAdapter) ListKeysPrefix(ctx context.Context, prefix string) ([]map[string]any, error) {
	return f.lister.ListKeysPrefix(ctx, prefix)
}

func (f *failOnKeyAdapter) Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error {
	k, _ := keys["key"].(string)
	f.deleteCalls = append(f.deleteCalls, k)
	if k == f.failKey {
		return errors.New("injected: delete failed for " + k)
	}
	return f.Adapter.Delete(ctx, keys, projectionSeq)
}

// TestDeleteAllForActor_PartialFailure_AttemptsAllAndJoinsErrors proves a
// transient failure on one child key never abandons the rest of the set:
// this path is never retried by its caller (keyshredded's privacy-critical
// tier Acks, it does not Nak), so partial progress here is strictly better
// than aborting on the first failure.
func TestDeleteAllForActor_PartialFailure_AttemptsAllAndJoinsErrors(t *testing.T) {
	ctx := context.Background()
	p := newMultiEntryDeleteKeyPipeline(t)
	real := p.adpt
	require.NoError(t, real.Upsert(ctx, map[string]any{"key": "child.a1"}, map[string]any{"key": "child.a1", "id": "a1"}, 1))
	require.NoError(t, real.Upsert(ctx, map[string]any{"key": "child.a2"}, map[string]any{"key": "child.a2", "id": "a2"}, 1))
	require.NoError(t, real.Upsert(ctx, map[string]any{"key": "child.a3"}, map[string]any{"key": "child.a3", "id": "a3"}, 1))

	lister, ok := real.(adapter.PrefixKeyLister)
	require.True(t, ok)
	failing := &failOnKeyAdapter{Adapter: real, lister: lister, failKey: "child.a2"}
	p.adpt = failing

	err := p.DeleteAllForActor(ctx, deleteKeyActor, math.MaxInt64)
	require.Error(t, err)
	require.ErrorContains(t, err, "deleted 2/3")
	require.ElementsMatch(t, []string{"child.a1", "child.a2", "child.a3"}, failing.deleteCalls,
		"every key must be attempted despite a mid-set failure")

	reader := real.(interface {
		GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error)
	})
	_, live1, err := reader.GetRow(ctx, map[string]any{"key": "child.a1"})
	require.NoError(t, err)
	require.False(t, live1, "child.a1 must still be deleted despite a later key's failure")
	_, live3, err := reader.GetRow(ctx, map[string]any{"key": "child.a3"})
	require.NoError(t, err)
	require.False(t, live3, "child.a3 must still be deleted despite an earlier key's failure")
	_, live2, err := reader.GetRow(ctx, map[string]any{"key": "child.a2"})
	require.NoError(t, err)
	require.True(t, live2, "child.a2 itself must remain live since its own delete failed")
}
