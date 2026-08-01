package pipeline

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// perEntryRetryAdapter wraps a real guarded multi-entry target adapter,
// failing the FIRST upsert call with a transient error and delegating every
// call after that to the embedded adapter. Embeds the CONCRETE
// *adapter.NatsKVAdapter (not the adapter.Adapter interface) so GetRow /
// ListKeysPrefix / Guarded promote automatically. Upsert AND UpsertWithOutcome
// are both overridden — one Go-embedding pitfall this fixture must avoid: they
// are two separate promoted methods, so overriding only Upsert would leave
// UpsertWithOutcome (adapter.OutcomeUpserter) promoted straight through to the
// embedded adapter, silently skipping the injected failure for any caller —
// writeResults included — that prefers UpsertWithOutcome when available.
// Records every upsert call's (keys, seq) for inspection.
type perEntryRetryAdapter struct {
	*adapter.NatsKVAdapter
	mu         sync.Mutex
	failedOnce bool
	upserts    []recordedWrite
}

func (a *perEntryRetryAdapter) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	_, err := a.upsertRecording(ctx, keys, row, seq)
	return err
}

func (a *perEntryRetryAdapter) UpsertWithOutcome(ctx context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	return a.upsertRecording(ctx, keys, row, seq)
}

func (a *perEntryRetryAdapter) upsertRecording(ctx context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	a.mu.Lock()
	a.upserts = append(a.upserts, recordedWrite{keys: keys, row: row, seq: seq})
	first := !a.failedOnce
	a.failedOnce = true
	a.mu.Unlock()
	if first {
		return adapter.UpsertOutcome{}, errors.New("injected: transient upsert failure")
	}
	return a.NatsKVAdapter.UpsertWithOutcome(ctx, keys, row, seq)
}

// TestWriteResults_PerEntryLens_TransientFailure_ReevaluatesActorNotRawReplay
// is the §4.3 security proof: a perEntry lens's transient write failure must
// not be retried by replaying the captured (keys, row, seq) — that raw
// replay could resurrect a since-revoked anchor through the absent-key
// Create door (no watermark exists yet at a key that never landed). Instead
// the retry must re-evaluate the whole actor via Reproject, which stamps the
// pipeline's own forward-progress sequence, not the originally-captured
// message sequence. Distinguishing those two sequence values is what proves
// the mechanism actually swapped, not just that "some" retry happened.
func TestWriteResults_PerEntryLens_TransientFailure_ReevaluatesActorNotRawReplay(t *testing.T) {
	ctx := context.Background()
	coreKV, adjKV, _ := newCollisionKVs(t)
	const identityID = "Tcc3RetryActorHhhhhh"
	actorKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, actorKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return []Envelope{{
			Keys: map[string]any{"key": "child.a1"},
			Row:  map[string]any{"key": "child.a1", "id": "a1"},
		}}, nil
	}

	failing := &perEntryRetryAdapter{NatsKVAdapter: newMultiEntryTargetAdapter(t)}
	rq := failure.NewRetryQueue()

	p := &Pipeline{
		ruleID:           "rule-multi-retry",
		coreKV:           coreKV,
		adjKV:            adjKV,
		adpt:             failing,
		actorDeleteKey:   func(string) string { return "child" },
		actorEnumerator:  NewActorEnumerator(adjKV, coreKV, "identity"),
		engineKind:       ruleengine.EngineFull,
		fullEngine:       eng,
		fullCR:           cr,
		multiEnvelopeFn:  entryFn,
		retryQueue:       rq,
		retryMaxAttempts: 3,
		retryBaseBackoff: time.Millisecond,
	}
	const reprojectSeq = 999 // the pipeline's own forward-progress token
	p.recordAppliedSeq(reprojectSeq)

	const originalMsgSeq = 42 // the seq a raw replay would have carried
	msg := substrate.Message{Sequence: originalMsgSeq}
	results := []ruleengine.EvalResult{{
		Keys: map[string]any{"key": "child.a1"},
		Row:  map[string]any{"key": "child.a1", "id": "a1"},
	}}

	decision, err := p.writeResults(ctx, msg, actorKey, results, nil)
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision, "a transient write failure disposes the message; the retry queue owns the eventual write")
	require.Equal(t, 1, rq.Len(), "the transient failure must enqueue exactly one retry entry")
	require.Len(t, failing.upserts, 1, "the initial failing attempt must be the only synchronous write")
	require.Equal(t, uint64(originalMsgSeq), failing.upserts[0].seq)

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go rq.Run(rctx)

	deadline := time.Now().Add(3 * time.Second)
	for rq.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, 0, rq.Len(), "the retry must eventually succeed and drain from the queue")

	require.Len(t, failing.upserts, 2, "exactly one retry attempt after the initial failure")
	require.Equal(t, uint64(reprojectSeq), failing.upserts[1].seq,
		"the retry must carry the pipeline's forward-progress seq (Reproject), not the original msg.Sequence — proving it re-evaluated the actor rather than replaying the captured raw write")
}

// TestWriteResults_PerEntryLens_TransientFailure_FanOut_ReprojectsEveryEnumeratedActor
// proves the fan-out call shape: when enumeratedActors is populated (a
// link/aspect event affecting several actors), a transient failure
// reprojects every enumerated actor, not just the entity key the CDC event
// named.
func TestWriteResults_PerEntryLens_TransientFailure_FanOut_ReprojectsEveryEnumeratedActor(t *testing.T) {
	ctx := context.Background()
	coreKV, adjKV, _ := newCollisionKVs(t)
	const actorAID = "Tcc3FanoutActorAaaaa"
	const actorBID = "Tcc3FanoutActorBbbbb"
	actorA := "vtx.identity." + actorAID
	actorB := "vtx.identity." + actorBID
	writeCollisionVertex(t, coreKV, actorA, "identity", map[string]any{})
	writeCollisionVertex(t, coreKV, actorB, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	// Unlike the single-actor test above, this entryFn must derive its key
	// from params["actorKey"] (executeFullForActor passes it through) so
	// each of the two enumerated actors' reprojections land at their own
	// distinct key rather than colliding on one shared "child.a1".
	entryFn := func(_, _, params map[string]any) ([]Envelope, error) {
		actor, _ := params["actorKey"].(string)
		k := "child." + actor + ".a1"
		return []Envelope{{
			Keys: map[string]any{"key": k},
			Row:  map[string]any{"key": k, "id": "a1"},
		}}, nil
	}

	failing := &perEntryRetryAdapter{NatsKVAdapter: newMultiEntryTargetAdapter(t)}
	rq := failure.NewRetryQueue()

	p := &Pipeline{
		ruleID:           "rule-multi-retry-fanout",
		coreKV:           coreKV,
		adjKV:            adjKV,
		adpt:             failing,
		actorDeleteKey:   func(actor string) string { return "child." + actor },
		actorEnumerator:  NewActorEnumerator(adjKV, coreKV, "identity"),
		engineKind:       ruleengine.EngineFull,
		fullEngine:       eng,
		fullCR:           cr,
		multiEnvelopeFn:  entryFn,
		retryQueue:       rq,
		retryMaxAttempts: 3,
		retryBaseBackoff: time.Millisecond,
	}
	p.recordAppliedSeq(555)

	msg := substrate.Message{Sequence: 7}
	// One failing result for the fan-out batch; enumeratedActors names both
	// actors touched by the triggering link/aspect event.
	results := []ruleengine.EvalResult{{
		Keys: map[string]any{"key": "child." + actorA + ".a1"},
		Row:  map[string]any{"key": "child." + actorA + ".a1", "id": "a1"},
	}}

	decision, err := p.writeResults(ctx, msg, "vtx.residence.someLink", results, []string{actorA, actorB})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)
	require.Equal(t, 2, rq.Len(), "one reproject-retry entry per enumerated actor")

	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go rq.Run(rctx)

	deadline := time.Now().Add(3 * time.Second)
	for rq.Len() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, 0, rq.Len())

	reader := failing.NatsKVAdapter
	_, liveA, err := reader.GetRow(ctx, map[string]any{"key": "child." + actorA + ".a1"})
	require.NoError(t, err)
	require.True(t, liveA, "actor A's entry must be projected by the reproject retry")
	_, liveB, err := reader.GetRow(ctx, map[string]any{"key": "child." + actorB + ".a1"})
	require.NoError(t, err)
	require.True(t, liveB, "actor B must also be reprojected — it was named in enumeratedActors even though its own write never failed")
}

// TestReproject_PerEntryLens_NoLongerRefusedAsNotActorAggregate proves §4.3's
// gate widening: a perEntry lens (multiEnvelopeFn installed, envelopeFn nil)
// is actor-aggregate too and must not be refused the way a plain lens is.
func TestReproject_PerEntryLens_NoLongerRefusedAsNotActorAggregate(t *testing.T) {
	ctx := context.Background()
	coreKV, adjKV, _ := newCollisionKVs(t)
	const identityID = "Tcc3ReprojActorJjjjj"
	actorKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, actorKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return []Envelope{{
			Keys: map[string]any{"key": "child.a1"},
			Row:  map[string]any{"key": "child.a1", "id": "a1"},
		}}, nil
	}
	p := &Pipeline{
		ruleID:          "rule-multi-reproject",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            newMultiEntryTargetAdapter(t),
		actorDeleteKey:  func(string) string { return "child" },
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}
	p.recordAppliedSeq(111)

	res, err := p.Reproject(ctx, actorKey)
	require.NoError(t, err)
	require.True(t, res.Wrote)
	require.Equal(t, actorKey, res.Actor)
}

// partialFailAdapter wraps a real guarded multi-entry target adapter,
// failing every upsert call whose key equals alwaysFailKey and delegating
// every other call to the embedded adapter. Upsert AND UpsertWithOutcome are
// both overridden for the same reason perEntryRetryAdapter overrides both:
// they promote independently from the embedded *adapter.NatsKVAdapter, so a
// caller preferring UpsertWithOutcome would otherwise bypass this fixture's
// fault injection entirely.
type partialFailAdapter struct {
	*adapter.NatsKVAdapter
	alwaysFailKey string
	mu            sync.Mutex
	upserts       []recordedWrite
}

func (a *partialFailAdapter) Upsert(ctx context.Context, keys, row map[string]any, seq uint64) error {
	_, err := a.upsertRecording(ctx, keys, row, seq)
	return err
}

func (a *partialFailAdapter) UpsertWithOutcome(ctx context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	return a.upsertRecording(ctx, keys, row, seq)
}

func (a *partialFailAdapter) upsertRecording(ctx context.Context, keys, row map[string]any, seq uint64) (adapter.UpsertOutcome, error) {
	a.mu.Lock()
	a.upserts = append(a.upserts, recordedWrite{keys: keys, row: row, seq: seq})
	a.mu.Unlock()
	if k, _ := keys["key"].(string); k == a.alwaysFailKey {
		return adapter.UpsertOutcome{}, errors.New("injected: deterministic upsert failure")
	}
	return a.NatsKVAdapter.UpsertWithOutcome(ctx, keys, row, seq)
}

// TestReproject_PartialFailure_AttemptsAllAndJoinsErrors proves the §4.3 fix
// this fire's adversarial review surfaced: a perEntry actor's reproject must
// attempt every result even after one fails, not abort at the first error.
// Without this, a deterministically-failing sibling anchor would
// permanently block a transiently-failing one from ever healing — the
// retry unit is "the actor", so one bad anchor must not poison the rest.
func TestReproject_PartialFailure_AttemptsAllAndJoinsErrors(t *testing.T) {
	ctx := context.Background()
	coreKV, adjKV, _ := newCollisionKVs(t)
	const identityID = "Tcc3TwoEntryActorPpp"
	actorKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, actorKey, "identity", map[string]any{})

	eng, cr := singleRowEngine(t)
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return []Envelope{
			{Keys: map[string]any{"key": "child.a1"}, Row: map[string]any{"key": "child.a1", "id": "a1"}},
			{Keys: map[string]any{"key": "child.a2"}, Row: map[string]any{"key": "child.a2", "id": "a2"}},
		}, nil
	}

	adpt := &partialFailAdapter{NatsKVAdapter: newMultiEntryTargetAdapter(t), alwaysFailKey: "child.a1"}
	p := &Pipeline{
		ruleID:          "rule-partial-fail",
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            adpt,
		actorDeleteKey:  func(string) string { return "child" },
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          cr,
		multiEnvelopeFn: entryFn,
	}
	p.recordAppliedSeq(321)

	_, err := p.Reproject(ctx, actorKey)
	require.Error(t, err)
	require.ErrorContains(t, err, "child.a1")

	reader := adpt.NatsKVAdapter
	_, live2, gerr := reader.GetRow(ctx, map[string]any{"key": "child.a2"})
	require.NoError(t, gerr)
	require.True(t, live2, "child.a2 must be written even though child.a1's write failed in the same reproject call")
	_, live1, gerr := reader.GetRow(ctx, map[string]any{"key": "child.a1"})
	require.NoError(t, gerr)
	require.False(t, live1, "child.a1 never succeeds")

	// A second attempt (the retry queue's next backoff cycle) still fails
	// on a1 but must not re-write or disturb the already-converged a2.
	upsertsBefore := len(adpt.upserts)
	_, err = p.Reproject(ctx, actorKey)
	require.Error(t, err)
	require.ErrorContains(t, err, "child.a1")
	require.Len(t, adpt.upserts, upsertsBefore+1, "a2 is already converged and must not be rewritten on the next attempt")
}

// TestWriteResults_PerEntryLens_TransientFailure_NoActorEnumerator_RefusesClosed
// proves the structural refusal this fire's adversarial review added: a
// perEntry lens (multiEnvelopeFn set) with no paired ActorEnumerator must
// never fall back to guessing that the triggering `key` is an actor key.
// InstallActorAggregate always pairs the two, so this is defense-in-depth —
// but the alternative (silently reprojecting the wrong entity, or a
// non-actor key that evaluates to zero rows and reads as a clean heal) is
// worse than the raw-replay bug this whole mechanism replaces.
func TestWriteResults_PerEntryLens_TransientFailure_NoActorEnumerator_RefusesClosed(t *testing.T) {
	ctx := context.Background()
	entryFn := func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
		return []Envelope{{
			Keys: map[string]any{"key": "child.a1"},
			Row:  map[string]any{"key": "child.a1", "id": "a1"},
		}}, nil
	}
	rq := failure.NewRetryQueue()
	p := &Pipeline{
		ruleID:           "rule-no-enumerator",
		adpt:             &recordingAdapter{writeErr: errors.New("injected: transient upsert failure")},
		actorDeleteKey:   func(string) string { return "child" },
		multiEnvelopeFn:  entryFn,
		retryQueue:       rq,
		retryMaxAttempts: 3,
		retryBaseBackoff: time.Millisecond,
	}
	// actorEnumerator deliberately left nil.

	msg := substrate.Message{Sequence: 1}
	results := []ruleengine.EvalResult{{
		Keys: map[string]any{"key": "child.a1"},
		Row:  map[string]any{"key": "child.a1", "id": "a1"},
	}}

	decision, err := p.writeResults(ctx, msg, "vtx.residence.someLink", results, nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "ActorEnumerator")
	require.Equal(t, substrate.Nak, decision, "a missing ActorEnumerator pairing is a structural defect, not a transient one")
	require.Zero(t, rq.Len(), "nothing must be enqueued when the actor cannot be trusted")
}
