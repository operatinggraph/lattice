package grantchange_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakePersonal records every reprojection asked of it and can fail on demand,
// so the drain's per-actor error posture is observable without a NATS-backed
// pipeline.
type fakePersonal struct {
	mu          sync.Mutex
	reprojected []string
	issues      []string
	failWith    error
}

func (f *fakePersonal) ReprojectPersonalActor(ctx context.Context, identityID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reprojected = append(f.reprojected, identityID)
	return f.failWith
}

func (f *fakePersonal) RecordGrantReprojectIssue(ctx context.Context, kind, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues = append(f.issues, kind)
}

func (f *fakePersonal) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.reprojected...)
}

func (f *fakePersonal) raised() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.issues...)
}

const (
	actorA = "Hj4kPmRtw9nbCxz5vQ2y"
	actorB = "Kx3TmZpq7RvwNsY2Hc9L"
)

func TestGrantChanged_EnqueuesAndCoalesces(t *testing.T) {
	r := grantchange.New()
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)

	// Three transitions for one actor between drains — the coalescing case a
	// mass grant change produces per actor.
	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.GrantChanged(substrate.VertexKey("identity", actorB))

	assert.Equal(t, 2, r.QueueDepth(), "N transitions for one actor cost one queue slot, not N")

	r.Drain(context.Background())

	assert.ElementsMatch(t, []string{actorA, actorB}, lens.seen())
	assert.Zero(t, r.QueueDepth(), "a completed drain leaves nothing owed")
}

func TestGrantChanged_RejectsNonIdentityAndMalformedKeys(t *testing.T) {
	r := grantchange.New()

	// A read-grant producer anchored on something else has no personal lens
	// keyed off it; a bare NanoID is not a Contract #1 vertex key at all.
	r.GrantChanged(substrate.VertexKey("role", actorA))
	r.GrantChanged(actorA)
	r.GrantChanged("")
	r.GrantChanged("cap-read.identity." + actorA)

	assert.Zero(t, r.QueueDepth(), "only a well-formed identity vertex key names a personal reprojection")
}

// TestDrain_EveryRegisteredLensIsReDriven pins the fan-out decision: a grant
// transition names one anchor, but the reprojection covers the whole actor on
// every personal lens. Routing by the entry's anchorType would cut this 15x and
// is refused — that field is audit-only, and a wrong or absent value would
// route a retraction to no lens at all, which is a silent over-grant.
func TestDrain_EveryRegisteredLensIsReDriven(t *testing.T) {
	r := grantchange.New()
	lenses := map[string]*fakePersonal{"a": {}, "b": {}, "c": {}}
	for id, l := range lenses {
		r.RegisterPersonal(id, l)
	}

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.Drain(context.Background())

	for id, l := range lenses {
		assert.Equal(t, []string{actorA}, l.seen(), "lens %s", id)
	}
}

func TestDeregisterPersonal_StopsReDriving(t *testing.T) {
	r := grantchange.New()
	live, gone := &fakePersonal{}, &fakePersonal{}
	r.RegisterPersonal("live", live)
	r.RegisterPersonal("gone", gone)
	r.DeregisterPersonal("gone")

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.Drain(context.Background())

	assert.Equal(t, []string{actorA}, live.seen())
	assert.Empty(t, gone.seen(), "a deleted lens must stop being re-driven")
}

// TestDrain_PerActorErrorDoesNotAbortTheRemainingLenses is T10 of
// personal-lens-grant-change-trigger-design.md §10.
//
// Three things are asserted together because each alone would be a plausible
// wrong implementation: the failure does not abort the actor's other lenses
// (aborting would let one lens's Capability KV hiccup silence fourteen others),
// it raises the lens's Health fault (without which the handoff to the standing
// healer is silent), and it does NOT re-enqueue (a persistent fault would
// otherwise spin the drain forever and starve every other actor).
func TestDrain_PerActorErrorDoesNotAbortTheRemainingLenses(t *testing.T) {
	r := grantchange.New()
	failing := &fakePersonal{failWith: errors.New("capability-kv read fault through the D1 gate")}
	healthy1, healthy2 := &fakePersonal{}, &fakePersonal{}
	r.RegisterPersonal("failing", failing)
	r.RegisterPersonal("healthy-1", healthy1)
	r.RegisterPersonal("healthy-2", healthy2)

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.Drain(context.Background())

	assert.Equal(t, []string{actorA}, healthy1.seen(), "a sibling lens's failure must not skip this one")
	assert.Equal(t, []string{actorA}, healthy2.seen())
	assert.Equal(t, []string{"reproject"}, failing.raised(),
		"the failing lens must raise its own Health fault, so the handoff to the sweep is visible")
	assert.Empty(t, healthy1.raised(), "a lens that succeeded raises nothing")
	assert.Zero(t, r.QueueDepth(),
		"a failed actor must NOT be re-enqueued — a persistent fault would spin the drain and starve every other actor")
}

// TestGrantChanged_OverflowDropsTheNewEntryAndIsLoud pins the bound's whole
// contract. Silent degradation is the failure this refuses: overflow means the
// standing healer is the only thing converging an unknown set of actors, which
// an operator has to be able to see.
func TestGrantChanged_OverflowDropsTheNewEntryAndIsLoud(t *testing.T) {
	r := grantchange.New()
	r.SetBounds(2, 0)
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.GrantChanged(substrate.VertexKey("identity", actorB))
	require.Equal(t, 2, r.QueueDepth())

	// At the bound. The NEW entry is refused; the two already owed a
	// reprojection keep it.
	r.GrantChanged(substrate.VertexKey("identity", "Zwq9PmRtw3nbCxz5vQ2y"))
	assert.Equal(t, 2, r.QueueDepth(), "overflow drops the new entry, never one already queued")

	// An actor ALREADY in the set is still coalesced rather than counted as a
	// drop — it has a reprojection owed to it, so nothing was lost.
	r.GrantChanged(substrate.VertexKey("identity", actorA))

	r.Drain(context.Background())

	assert.ElementsMatch(t, []string{actorA, actorB}, lens.seen())
	assert.Equal(t, []string{"overflow"}, lens.raised(),
		"an overflow must raise a Health fault — the drop is otherwise invisible")

	// Edge-triggered: a second drain with no further drops reports nothing.
	r.Drain(context.Background())
	assert.Equal(t, []string{"overflow"}, lens.raised(),
		"the overflow report is per-tick-with-drops, not per-tick")
}

// TestDrain_TransitionsLandingMidDrainAreNotSwallowed pins why the drain takes
// actors one at a time rather than snapshotting the set: a grant that flips
// again while its own reprojection is running must be re-driven, not folded
// into the pass that already read the pre-flip state.
func TestDrain_TransitionsLandingMidDrainAreNotSwallowed(t *testing.T) {
	r := grantchange.New()
	var reentered bool
	lens := &reenqueueingPersonal{onReproject: func(actorID string) {
		if !reentered {
			reentered = true
			r.GrantChanged(substrate.VertexKey("identity", actorID))
		}
	}}
	r.RegisterPersonal("lens-1", lens)

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.Drain(context.Background())

	assert.Equal(t, []string{actorA, actorA}, lens.seen(),
		"a transition landing mid-drain must earn its own reprojection")
}

// reenqueueingPersonal runs a hook inside the reprojection, which is how a
// mid-drain transition is simulated deterministically — no sleeps, no racing
// goroutine.
type reenqueueingPersonal struct {
	fakePersonal
	onReproject func(actorID string)
}

func (f *reenqueueingPersonal) ReprojectPersonalActor(ctx context.Context, identityID string) error {
	if f.onReproject != nil {
		f.onReproject(identityID)
	}
	return f.fakePersonal.ReprojectPersonalActor(ctx, identityID)
}

func TestDrain_StopsOnContextCancel(t *testing.T) {
	r := grantchange.New()
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)
	r.GrantChanged(substrate.VertexKey("identity", actorA))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r.Drain(ctx)

	assert.Empty(t, lens.seen(), "a cancelled drain abandons the in-flight set rather than racing shutdown")
}
