package grantchange_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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
	details     []string
	failWith    error
	// issueErr makes the Health write itself fail, which is how the overflow
	// counter's "never clear what was not reported" rule is exercised.
	issueErr error
}

func (f *fakePersonal) ReprojectPersonalActor(ctx context.Context, identityID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reprojected = append(f.reprojected, identityID)
	return f.failWith
}

func (f *fakePersonal) RecordGrantReprojectIssue(ctx context.Context, kind, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issueErr != nil {
		return f.issueErr
	}
	f.issues = append(f.issues, kind)
	f.details = append(f.details, detail)
	return nil
}

func (f *fakePersonal) reportedDetails() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.details...)
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

// TestDrain_OverflowIsReportedDuringASustainedStorm covers the scenario the
// bound exists for, and the one a report fired only on entry to Drain would go
// silent through: signals keep being dropped WHILE the drain is running, and
// the drain does not return until the storm ends.
//
// A report only at the top of the pass would produce exactly one Health issue
// for the whole storm — an operator watching a mass grant change would see the
// first drop and nothing after it, which is indistinguishable from a storm that
// stopped.
func TestDrain_OverflowIsReportedDuringASustainedStorm(t *testing.T) {
	r := grantchange.New()
	// A bound of 1 makes the storm deterministic: each reprojection refills the
	// single slot and its second enqueue is refused.
	r.SetBounds(1, 0)
	witness := &fakePersonal{}

	const stormLength = 130 // > 2 x the in-loop report interval
	fired := 0
	storm := &reenqueueingPersonal{onReproject: func(string) {
		if fired >= stormLength {
			return
		}
		r.GrantChanged(substrate.VertexKey("identity", stormActor(fired*2)))
		r.GrantChanged(substrate.VertexKey("identity", stormActor(fired*2+1)))
		fired++
	}}
	r.RegisterPersonal("a-storm", storm)
	r.RegisterPersonal("b-witness", witness)

	r.GrantChanged(substrate.VertexKey("identity", stormActor(9000)))
	r.Drain(context.Background())

	raised := witness.raised()
	require.Greater(t, len(raised), 1,
		"a sustained overflow must be reported repeatedly while the drain runs, not once on entry")
	for _, kind := range raised {
		assert.Equal(t, "overflow", kind)
	}
	// The count is cumulative and rising, which is what tells an operator the
	// storm is ongoing rather than a single past event.
	details := witness.reportedDetails()
	require.NotEmpty(t, details)
	assert.Contains(t, details[len(details)-1], "cumulative")
	assert.NotEqual(t, details[0], details[len(details)-1],
		"successive reports must carry a growing count, not repeat one number")
}

// stormActor builds a distinct valid 20-char NanoID for index i. The digits are
// encoded in the platform alphabet rather than base 10, which excludes "0".
func stormActor(i int) string {
	alphabet := substrate.Alphabet
	out := []byte("StormActorAAAAAAAAAA")
	n := i
	for pos := len(out) - 1; pos >= 10; pos-- {
		out[pos] = alphabet[n%len(alphabet)]
		n /= len(alphabet)
	}
	return string(out)
}

// TestReportDropped_KeepsTheCountWhenTheHealthWriteFails pins the compounding
// half of the same finding: a delta cleared before the write lands is a count
// gone forever, and the next pass then reports nothing — a silent overflow.
func TestReportDropped_KeepsTheCountWhenTheHealthWriteFails(t *testing.T) {
	r := grantchange.New()
	r.SetBounds(1, 0)
	failing := &fakePersonal{issueErr: errors.New("health kv unavailable")}
	r.RegisterPersonal("lens-1", failing)

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.GrantChanged(substrate.VertexKey("identity", actorB)) // dropped at the bound

	r.Drain(context.Background())
	require.Empty(t, failing.raised(), "the Health write failed, so nothing was reported")

	// The write recovers; the count must still be there to report.
	failing.mu.Lock()
	failing.issueErr = nil
	failing.mu.Unlock()

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.Drain(context.Background())

	assert.Equal(t, []string{"overflow"}, failing.raised(),
		"a drop the Health write lost must still be reported on the next pass, not cleared into silence")
}

// TestDrain_HoldsUntilTheLensRegistryIsComplete is the fix for the one drop
// path that had no observability at all.
//
// A signal is consumed exactly once. Personal lenses register one at a time as
// their rules activate, while the producers are already emitting, so an actor
// dirtied before the last lens registers would be reprojected against a short
// registry and the frames the missing lenses owed it would be gone silently.
func TestDrain_HoldsUntilTheLensRegistryIsComplete(t *testing.T) {
	r := grantchange.New()
	early, late := &fakePersonal{}, &fakePersonal{}
	r.RegisterPersonal("early", early)

	complete := false
	r.SetRegistryReady(func(context.Context) error {
		if complete {
			return nil
		}
		return errors.New("1 lens(es) are in Core KV but not yet registered")
	})

	// A producer signals while the registry is still loading.
	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.Drain(context.Background())

	assert.Empty(t, early.seen(), "the drain must not consume a signal against a partial registry")
	assert.Equal(t, 1, r.QueueDepth(), "the signal is HELD, not dropped")

	// The last lens registers and the registry reports complete.
	r.RegisterPersonal("late", late)
	complete = true
	r.Drain(context.Background())

	assert.Equal(t, []string{actorA}, early.seen())
	assert.Equal(t, []string{actorA}, late.seen(),
		"the lens that registered late must still receive the actor dirtied before it did")
	assert.Zero(t, r.QueueDepth())
}

// TestDrain_RegistryHoldIsNotPermanent pins the bound on that wait. A lens that
// fails activation outright never registers, and prevention must not become a
// permanent stall that holds the whole edge closed forever.
func TestDrain_RegistryHoldIsNotPermanent(t *testing.T) {
	r := grantchange.New()
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)
	r.SetRegistryReady(func(context.Context) error {
		return errors.New("1 lens(es) are in Core KV but not yet registered")
	})
	r.GrantChanged(substrate.VertexKey("identity", actorA))

	r.Drain(context.Background())
	require.Empty(t, lens.seen(), "held while inside the bound")

	// Shrink the bound so the hold this pass started is already past it.
	r.SetRegistryHoldMax(time.Nanosecond)
	r.Drain(context.Background())

	assert.Equal(t, []string{actorA}, lens.seen(),
		"past the bound the drain proceeds rather than holding the edge closed forever")
}

// TestDrain_ReadinessIsSkippedWhileNothingIsQueued pins the cost bound: the
// check is a Core KV enumeration and the drain ticks every second, so an idle
// boot must not spend one per tick to say it has nothing to lose.
func TestDrain_ReadinessIsSkippedWhileNothingIsQueued(t *testing.T) {
	r := grantchange.New()
	r.RegisterPersonal("lens-1", &fakePersonal{})
	checks := 0
	r.SetRegistryReady(func(context.Context) error {
		checks++
		return nil
	})

	for i := 0; i < 5; i++ {
		r.Drain(context.Background())
	}

	assert.Zero(t, checks, "an empty queue has no signal to lose, so it must not pay for the check")
}

// TestReprojectActor_SkipsALensDeregisteredMidWalk closes the race between the
// registry snapshot and the serial walk over it.
//
// Walking one actor across every personal lens is a window seconds wide under
// load. A lens deleted inside it has had its run context cancelled and its
// Health entry removed — so calling it fails, and the failure path's
// read-modify-PUT would RE-CREATE the entry the deleter just deleted, leaving
// an orphan "degraded lens" row with no lens behind it.
func TestReprojectActor_SkipsALensDeregisteredMidWalk(t *testing.T) {
	r := grantchange.New()
	doomed := &fakePersonal{failWith: errors.New("pipeline torn down")}
	// The first lens deregisters the second from inside its own reprojection,
	// which is the mid-walk window made deterministic.
	first := &reenqueueingPersonal{onReproject: func(string) { r.DeregisterPersonal("b-doomed") }}
	// The walk is sorted by rule ID, so "a-first" is visited before "b-doomed"
	// deterministically — no reliance on map order.
	r.RegisterPersonal("a-first", first)
	r.RegisterPersonal("b-doomed", doomed)

	r.GrantChanged(substrate.VertexKey("identity", actorA))
	r.Drain(context.Background())

	assert.Empty(t, doomed.seen(), "a lens deregistered mid-walk must not be reprojected")
	assert.Empty(t, doomed.raised(),
		"and must not have a Health entry re-created for it — the deleter just removed that entry")
}
