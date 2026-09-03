package pipeline

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

const (
	shredActorA = "vtx.identity.Hj4kPmRtw9nbCxz5vQ2y"
	shredActorB = "vtx.identity.Kx3TmZpq7RvwNsY2Hc9L"
	shredAnchor = "ZwqPmRtw9nbCxz5vQ2yH"
)

// recordingShredSink is a GrantChangeSink that remembers every actor it was
// told about, so a shred test can prove an announcement HAPPENED rather than
// merely that the delete returned nil.
type recordingShredSink struct {
	mu     sync.Mutex
	actors []string
}

func (s *recordingShredSink) GrantChanged(actorKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actors = append(s.actors, actorKey)
}

func (s *recordingShredSink) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.actors...)
}

// shredKeyPrefix is the per-entry key shape these tests project into:
// cap-read.<actor vertex key>.<anchor NanoID>, the same bracketing a real
// perEntry producer's descriptor renders.
const shredKeyPrefix = "cap-read."

// shredAnchorFromKey is the descriptor inverse the guarded arm announces
// through — the same role OutputDescriptor.AnchorFromKey plays in production.
// It strips the literal prefix and the trailing entry token, and reports false
// for anything this pattern did not produce.
func shredAnchorFromKey(targetKey string) (string, bool) {
	rest, ok := strings.CutPrefix(targetKey, shredKeyPrefix)
	if !ok {
		return "", false
	}
	idx := strings.LastIndexByte(rest, '.')
	if idx < 0 {
		return "", false
	}
	return rest[:idx], true
}

// newShredPipeline builds a perEntry pipeline over one target adapter with the
// grant-change edge wired, so a shred's announcements are observable.
func newShredPipeline(t *testing.T, adpt adapter.Adapter, sink GrantChangeSink) *Pipeline {
	t.Helper()
	// These fixtures ARE a cap-read producer — every key below is a D1 grant
	// key — so they carry the namespace licence projection.InstallActorAggregate
	// grants such a lens. Without it the adapter's own producer-closure guard
	// refuses the write before the shred can retract anything, which is the
	// right answer for a lens that is not a producer and the wrong fixture for
	// one that is.
	if nkv, ok := adpt.(*adapter.NatsKVAdapter); ok {
		nkv.SetReadGrantWriter(true)
	}
	p := &Pipeline{
		ruleID:          "test-rule-shred",
		adpt:            adpt,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorDeleteKey:  func(actor string) string { return shredKeyPrefix + actor },
		multiEnvelopeFn: fanOutEntryFn,
	}
	if sink != nil {
		p.SetGrantChangeSink(sink, shredAnchorFromKey)
	}
	return p
}

func shredChildKey(actor, anchor string) map[string]any {
	return map[string]any{"key": shredKeyPrefix + actor + "." + anchor}
}

// TestDeleteAllForActor_AnnouncesOncePerRevokedKey is the perEntry arm of the
// key-shred announcement (personal-lens-derivation-licence-design.md §4.1).
//
// The shred loop retracts an identity's whole cap-read.* child set out of band,
// through a path that never reaches the per-key guard's caller. Without an
// announcement it would withdraw live grants and tell nobody, and a consumer of
// the read-grant projection that hears only about CDC-path flips keeps honouring
// every one of them until its standing healer next runs.
//
// The zero-announcement half is as load-bearing as the count: a signal per
// already-tombstoned key would drive a pointless reprojection of every actor on
// every redelivered shred event, and redelivery is the ordinary case (the
// listener Acks without retrying, so a whole-event redelivery re-attempts every
// target).
func TestDeleteAllForActor_AnnouncesOncePerRevokedKey(t *testing.T) {
	ctx := context.Background()
	sink := &recordingShredSink{}
	adpt := newMultiEntryTargetAdapter(t)
	p := newShredPipeline(t, adpt, sink)

	for _, anchor := range []string{shredAnchor, "Nb7RvwKx3TmZpq2Hc9Ls"} {
		keys := shredChildKey(shredActorA, anchor)
		require.NoError(t, adpt.Upsert(ctx, keys, map[string]any{"key": keys["key"]}, 1))
	}
	// A sibling actor's key under the same target, to prove the announcement
	// set follows the keys the shred actually touched.
	sibling := shredChildKey(shredActorB, shredAnchor)
	require.NoError(t, adpt.Upsert(ctx, sibling, map[string]any{"key": sibling["key"]}, 1))

	require.NoError(t, p.DeleteAllForActor(ctx, shredActorA, math.MaxInt64))
	require.Equal(t, []string{shredActorA, shredActorA}, sink.seen(),
		"each live child key the shred revoked must announce, and only actor A's")

	// A re-shred's keys are declined by the watermark, and a DECLINED guarded
	// retraction is not evidence that nothing was revoked: the guard returns
	// before reading the stored body, so the row it left behind is unverified.
	// The per-key path stays correctly silent — it has no transition to report
	// — and the actor-level fallback closes the silence once, which is the
	// fail-safe direction at the cost of one coalesced reprojection.
	require.NoError(t, p.DeleteAllForActor(ctx, shredActorA, math.MaxInt64))
	require.Equal(t, []string{shredActorA, shredActorA, shredActorA}, sink.seen(),
		"a re-shred whose keys the watermark declined announces once for the actor, not once per key and not zero times")
}

// TestDeleteAllForActor_UnguardedAdapterAnnouncesOncePerActor pins §4.1's
// fallback arm, and the reason the discriminator is NOT the OutcomeDeleter
// interface.
//
// An adapter that does not derive liveness reports TransitionNone for every key
// it retracts. Routing the shred through DeleteWithOutcome on the strength of
// the interface alone would therefore announce NOTHING while reading, at the
// call site, exactly like a closed hole. Announcing once per actor instead is
// coarser than per key and strictly safe: the reprojection it triggers is per
// actor anyway.
func TestDeleteAllForActor_UnguardedAdapterAnnouncesOncePerActor(t *testing.T) {
	ctx := context.Background()
	sink := &recordingShredSink{}
	adpt := newMultiEntryTargetAdapter(t)
	adpt.SetGuarded(false)
	require.False(t, adpt.DerivesGrantTransition(),
		"the fixture must actually be in the non-deriving posture this test is about")
	p := newShredPipeline(t, adpt, sink)

	for _, anchor := range []string{shredAnchor, "Nb7RvwKx3TmZpq2Hc9Ls"} {
		keys := shredChildKey(shredActorA, anchor)
		require.NoError(t, adpt.Upsert(ctx, keys, map[string]any{"key": keys["key"]}, 1))
	}

	require.NoError(t, p.DeleteAllForActor(ctx, shredActorA, math.MaxInt64))
	require.Equal(t, []string{shredActorA}, sink.seen(),
		"an adapter that cannot report a per-key transition must still announce the actor exactly once, not zero times")

	// A REDELIVERED shred announces again, and that is the accepted trade
	// rather than an oversight. This adapter cannot tell a live key from a
	// tombstone without a read-before-delete it does not perform, so the only
	// alternatives are re-announcing or never announcing; the cost is one
	// reprojection per redelivery, coalesced per identity by the reprojector's
	// own dirty set. Pinned here so the asymmetry with the guarded arm — which
	// HAS the liveness and stays exactly silent — is a decision on the record.
	require.NoError(t, p.DeleteAllForActor(ctx, shredActorA, math.MaxInt64))
	require.Equal(t, []string{shredActorA, shredActorA}, sink.seen(),
		"the non-deriving arm re-announces on a redelivered shred: coarser than the guarded arm, bounded, and never silent about a real revocation")
}

// TestDeleteAllForActor_NoSinkIsSilentAndClean pins the fail-slow default: a
// lens carrying no grant-change edge — every lens that is not a read-grant
// producer — shreds its keys and emits no signal at all.
func TestDeleteAllForActor_NoSinkIsSilentAndClean(t *testing.T) {
	ctx := context.Background()
	adpt := newMultiEntryTargetAdapter(t)
	p := newShredPipeline(t, adpt, nil)

	keys := shredChildKey(shredActorA, shredAnchor)
	require.NoError(t, adpt.Upsert(ctx, keys, map[string]any{"key": keys["key"]}, 1))
	require.NoError(t, p.DeleteAllForActor(ctx, shredActorA, math.MaxInt64))

	_, live, err := adpt.GetRow(ctx, keys)
	require.NoError(t, err)
	require.False(t, live, "the shred itself must still retract the key")
}

// TestDelete_DocModeShredAnnounces is the OTHER arm of the same hole, and the
// one a census matching a single receiver spelling rather than the call itself
// cannot see.
//
// keyshredded routes a doc-mode NullifyTarget through Control.NullifyRow →
// Pipeline.Delete, a sibling of the perEntry path above and just as silent. It
// is exercised on both adapter postures for the same reason DeleteAllForActor
// is: the guarded arm announces the key the adapter itself rendered, and the
// non-deriving arm announces the actor the shred call already holds.
func TestDelete_DocModeShredAnnounces(t *testing.T) {
	t.Run("a guarded adapter announces the revoked key's own actor", func(t *testing.T) {
		ctx := context.Background()
		sink := &recordingShredSink{}
		adpt := newMultiEntryTargetAdapter(t)
		p := newShredPipeline(t, adpt, sink)

		keys := shredChildKey(shredActorA, shredAnchor)
		require.NoError(t, adpt.Upsert(ctx, keys, map[string]any{"key": keys["key"]}, 1))

		require.NoError(t, p.Delete(ctx, keys, shredActorA, math.MaxInt64))
		require.Equal(t, []string{shredActorA}, sink.seen())

		// The second call is declined by the watermark, which is NOT the same
		// fact as "the row was already a tombstone": the guard returns before
		// reading the body, so nothing here has verified what the key holds.
		// The fallback announces the actor once rather than assuming.
		require.NoError(t, p.Delete(ctx, keys, shredActorA, math.MaxInt64))
		require.Equal(t, []string{shredActorA, shredActorA}, sink.seen(),
			"a watermark-declined retraction is unverified, so it announces the actor rather than staying silent")
	})

	t.Run("a non-deriving adapter announces the actor the shred names", func(t *testing.T) {
		ctx := context.Background()
		sink := &recordingShredSink{}
		adpt := newMultiEntryTargetAdapter(t)
		adpt.SetGuarded(false)
		p := newShredPipeline(t, adpt, sink)

		keys := shredChildKey(shredActorA, shredAnchor)
		require.NoError(t, adpt.Upsert(ctx, keys, map[string]any{"key": keys["key"]}, 1))

		require.NoError(t, p.Delete(ctx, keys, shredActorA, math.MaxInt64))
		require.Equal(t, []string{shredActorA}, sink.seen(),
			"the fallback must name the actor the shred call holds — there is no written key to invert")
	})
}

// TestDeleteAllForActor_GuardedArmFallsBackWhenNoKeySignalled closes the gap
// between "the per-key arm ran" and "a signal was emitted".
//
// notifyGrantChange declines for two reasons that are NOT "this key was already
// tombstoned": a sequence-less guarded write leaves the liveness unclassified
// (TransitionUnknown), and a key the lens's own inverse does not claim emits
// nothing at all. For a CDC write both are correctly fail-slow — there is no
// actor in hand. A shred HAS the actor, so leaving the revocation silent there
// is a choice, and the wrong one: it is the over-grant direction.
//
// The inverse is broken here rather than the transition, because that is the
// arm a real deployment reaches: IsReadGrantProducer probes the round trip
// before wiring the sink, so a mismatch means the key does not belong to this
// lens's pattern — exactly the fail-closed case the inversion exists for.
func TestDeleteAllForActor_GuardedArmFallsBackWhenNoKeySignalled(t *testing.T) {
	ctx := context.Background()
	sink := &recordingShredSink{}
	adpt := newMultiEntryTargetAdapter(t)
	adpt.SetReadGrantWriter(true)
	p := &Pipeline{
		ruleID:          "test-rule-shred-noinvert",
		adpt:            adpt,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorDeleteKey:  func(actor string) string { return shredKeyPrefix + actor },
		multiEnvelopeFn: fanOutEntryFn,
	}
	// An inverse that claims nothing: every per-key announcement declines.
	p.SetGrantChangeSink(sink, func(string) (string, bool) { return "", false })

	for _, anchor := range []string{shredAnchor, "Nb7RvwKx3TmZpq2Hc9Ls"} {
		keys := shredChildKey(shredActorA, anchor)
		require.NoError(t, adpt.Upsert(ctx, keys, map[string]any{"key": keys["key"]}, 1))
	}

	require.NoError(t, p.DeleteAllForActor(ctx, shredActorA, math.MaxInt64))
	require.Equal(t, []string{shredActorA}, sink.seen(),
		"a shred whose per-key announcements all declined must still name the actor it holds — once, not per key")
}

// TestDelete_DocModeFallsBackWhenNoKeySignalled is the doc-mode sibling of the
// case above: the same silence, on the arm reached through Control.NullifyRow.
func TestDelete_DocModeFallsBackWhenNoKeySignalled(t *testing.T) {
	ctx := context.Background()
	sink := &recordingShredSink{}
	adpt := newMultiEntryTargetAdapter(t)
	adpt.SetReadGrantWriter(true)
	p := &Pipeline{
		ruleID:          "test-rule-shred-docmode-noinvert",
		adpt:            adpt,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorDeleteKey:  func(actor string) string { return shredKeyPrefix + actor },
		multiEnvelopeFn: fanOutEntryFn,
	}
	p.SetGrantChangeSink(sink, func(string) (string, bool) { return "", false })

	keys := shredChildKey(shredActorA, shredAnchor)
	require.NoError(t, adpt.Upsert(ctx, keys, map[string]any{"key": keys["key"]}, 1))

	require.NoError(t, p.Delete(ctx, keys, shredActorA, math.MaxInt64))
	require.Equal(t, []string{shredActorA}, sink.seen(),
		"the doc-mode arm holds the actor too, so a declined per-key announcement must not end in silence")
}
