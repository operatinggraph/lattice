package grantchange_test

// The personal plane's standing healer reports a VERDICT, not a progress stamp
// (personal-lens-derivation-licence-design.md §4.4c conjunct 3, §13 B3).
//
// Every case here is one the first draft of that conjunct read as healthy. The
// distinction the file exists to hold: the cursor and the cycle stamp advance on
// a pass in which every reprojection failed, because the per-lens failure path
// logs, raises that lens's health fault and continues — so a consumer reading
// progress reads healthy through the exact condition the healer exists to
// detect. What the licence rests on is what the pass ACHIEVED.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestPersonalSweep_VerdictIsWhatThePassAchieved is the positive vector plus its
// three refusals, all read off the SAME accessor the licence reads.
func TestPersonalSweep_VerdictIsWhatThePassAchieved(t *testing.T) {
	ctx := context.Background()

	t.Run("a clean pass over a readable population with one live instance", func(t *testing.T) {
		_, keys := sweepActors(3)
		lens := &fakePersonal{}
		r := grantchange.New()
		r.RegisterPersonal("lens-1", lens)
		s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, &fakeLister{keys: []string{"health.refractor.rfx-a1b2c3"}})
		s.SetBounds(10, 0)

		s.Sweep(ctx)
		v := s.Verdict()
		assert.False(t, v.CompletedAt.IsZero())
		assert.Equal(t, 3, v.Attempted)
		assert.Zero(t, v.Failed)
		assert.True(t, v.PopulationReadable)
		assert.Equal(t, 1, v.InstanceCount)
		assert.True(t, v.InstanceCountReadable)
		assert.Equal(t, pipeline.PersonalHealerVerdictClean, v.Summary())
	})

	t.Run("a pass in which every reprojection failed is NOT clean", func(t *testing.T) {
		// Drive it the way the live case does — a Capability-KV read fault
		// surfacing through the D1 gate — rather than by writing a count, so the
		// verdict is produced by the reprojection path the drain also runs.
		_, keys := sweepActors(3)
		lens := &fakePersonal{failWith: errors.New("capability-kv read fault through the D1 gate")}
		r := grantchange.New()
		r.RegisterPersonal("lens-1", lens)
		s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, &fakeLister{keys: []string{"health.refractor.rfx-a1b2c3"}})
		s.SetBounds(10, 0)

		s.Sweep(ctx)
		v := s.Verdict()
		assert.Equal(t, 3, v.Attempted)
		assert.Equal(t, 3, v.Failed,
			"the failure count is what turns a pass into a verdict; the progress stamp advances regardless")
		assert.Equal(t, pipeline.PersonalHealerVerdictFailed, v.Summary())
		assert.False(t, v.CompletedAt.IsZero(),
			"the pass DID complete — what it did not do is repair, and the two facts are separate")
	})

	t.Run("a lens deregistered mid-walk is skipped, not counted as a failure", func(t *testing.T) {
		_, keys := sweepActors(2)
		r := grantchange.New()
		r.RegisterPersonal("lens-1", &fakePersonal{})
		s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, &fakeLister{keys: []string{"health.refractor.rfx-a1b2c3"}})
		s.SetBounds(10, 0)
		r.DeregisterPersonal("lens-1")
		// hasPersonal is false now, so the pass returns before recording. The
		// standing zero verdict refuses, which is the fail-closed answer for a
		// plane with no registered personal lens.
		s.Sweep(ctx)
		assert.True(t, s.Verdict().CompletedAt.IsZero())
	})

	t.Run("an unreadable population is not a clean pass", func(t *testing.T) {
		lens := &fakePersonal{}
		r := grantchange.New()
		r.RegisterPersonal("lens-1", lens)
		lister := &fakeLister{}
		lister.setErr(errors.New("core kv unreachable"))
		s := grantchange.NewPersonalSweeper(r, lister, &fakeLister{keys: []string{"health.refractor.rfx-a1b2c3"}})

		s.Sweep(ctx)
		v := s.Verdict()
		assert.False(t, v.PopulationReadable)
		assert.Equal(t, pipeline.PersonalHealerVerdictPopulationUnreadable, v.Summary())
	})
}

// TestPersonalSweep_CountsLiveRefractorInstances covers conjunct 5's input, on
// the filter the heartbeater itself writes.
func TestPersonalSweep_CountsLiveRefractorInstances(t *testing.T) {
	ctx := context.Background()
	_, keys := sweepActors(2)

	newSweeper := func(healthKV grantchange.HealthKVLister) (*grantchange.PersonalSweeper, *fakePersonal) {
		lens := &fakePersonal{}
		r := grantchange.New()
		r.RegisterPersonal("lens-1", lens)
		s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, healthKV)
		s.SetBounds(10, 0)
		return s, lens
	}

	t.Run("one heartbeat is one instance, read off the filter the heartbeater writes", func(t *testing.T) {
		hk := &fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}}
		s, _ := newSweeper(hk)
		s.Sweep(ctx)
		assert.Equal(t, 1, s.Verdict().InstanceCount)
		assert.True(t, s.Verdict().InstanceCountReadable)
		assert.Equal(t, health.InstanceKeyFilter, hk.lastFilter,
			"the filter must be the one the heartbeater's own key prefix produces, not a respelling")
	})

	t.Run("two heartbeats refuse — the grant-change edge reaches one process", func(t *testing.T) {
		s, _ := newSweeper(&fakeLister{keys: []string{
			health.InstanceKeyPrefix + "rfx-a1b2c3",
			health.InstanceKeyPrefix + "rfx-d4e5f6",
		}})
		s.Sweep(ctx)
		assert.Equal(t, 2, s.Verdict().InstanceCount)
		assert.Equal(t, pipeline.PersonalHealerVerdictMultipleInstances, s.Verdict().Summary())
	})

	t.Run("a CRASHED instance's unexpired heartbeat over-counts, and that is CORRECT", func(t *testing.T) {
		// The asymmetry that makes the count a backstop rather than the primary
		// defence, pinned so a later freshness filter has to argue with a test.
		// Over-counting refuses and pessimises: safe. UNDER-counting — a newly
		// started instance that has not yet written its first heartbeat — fails
		// OPEN, which is the hazard, and no filter over this listing closes it.
		s, _ := newSweeper(&fakeLister{keys: []string{
			health.InstanceKeyPrefix + "rfx-live00",
			health.InstanceKeyPrefix + "rfx-crashed", // TTL not yet elapsed
		}})
		s.Sweep(ctx)
		assert.Equal(t, 2, s.Verdict().InstanceCount,
			"a stale entry counts; trading this pessimisation away removes the safe direction, not the hazard")
	})

	t.Run("an EMPTY listing fails closed — zero is self-refuting", func(t *testing.T) {
		// A live Refractor performing this census and finding no Refractor has
		// contradicted itself: zero can only mean the census is broken (bucket
		// purged or re-provisioned under a running process, heartbeat writes
		// failing while listings succeed, a permission change, key-shape drift).
		// The direction is what matters — two instances whose heartbeats are not
		// landing read exactly this on BOTH of them, and a zero treated as
		// readable would license the narrowing on both while the grant-change
		// edge reaches neither.
		s, _ := newSweeper(&fakeLister{})
		s.Sweep(ctx)
		assert.False(t, s.Verdict().InstanceCountReadable,
			"an empty instance census is UNREADABLE, never an empty deployment")
		assert.Zero(t, s.Verdict().InstanceCount)
		assert.Equal(t, pipeline.PersonalHealerVerdictInstancesUnreadable, s.Verdict().Summary())
	})

	t.Run("an unreadable listing fails CLOSED", func(t *testing.T) {
		hk := &fakeLister{}
		hk.setErr(errors.New("health kv unreachable"))
		s, _ := newSweeper(hk)
		s.Sweep(ctx)
		assert.False(t, s.Verdict().InstanceCountReadable)
		assert.Equal(t, pipeline.PersonalHealerVerdictInstancesUnreadable, s.Verdict().Summary())
	})

	t.Run("no health handle at all fails CLOSED", func(t *testing.T) {
		s, _ := newSweeper(nil)
		s.Sweep(ctx)
		assert.False(t, s.Verdict().InstanceCountReadable)
		assert.Equal(t, pipeline.PersonalHealerVerdictInstancesUnreadable, s.Verdict().Summary())
	})

	t.Run("the census is read ONCE per pass, never per actor", func(t *testing.T) {
		hk := &fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}}
		s, lens := newSweeper(hk)
		s.Sweep(ctx)
		require.Len(t, lens.seen(), 2, "the pass really did walk both identities")
		assert.Equal(t, 1, hk.callCount(),
			"the deployment's cardinality is a property of the deployment; asking it per actor would put a KV listing on the path this narrowing shortens")
	})
}

// TestPersonalSweep_RunSweepsImmediately pins §4.4c's third bullet — no lens
// waits a whole interval on the relation-blind enumerator after a restart —
// AGAINST THE SEQUENCE cmd/refractor ACTUALLY RUNS.
//
// THE ORDER IS THE TEST. cmd/refractor starts the loop before the lens source
// has activated anything, and Sweep returns without recording a verdict while
// the registry is empty — so a fixture that registers a lens and only then
// starts Run hands the immediate pass a registry to walk that production never
// gives it, and goes green over a mechanism that records nothing where it runs.
// Run first, registration second, and an interval of an hour so that no tick can
// be what produced the answer.
//
// The lens's own registration instant is the bar, not merely the pass's
// existence: conjunct 3 refuses a verdict from a pass that began before this
// lens registered (anchor_derivation_personal.go), so a prompt pass that
// started first licenses nothing.
func TestPersonalSweep_RunSweepsImmediately(t *testing.T) {
	_, keys := sweepActors(2)
	r := grantchange.New()
	s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, &fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
	// An interval far longer than this test will ever wait: whatever the run
	// loop produces cannot have come from a tick.
	s.SetBounds(10, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	// The first lens is the BARRIER, not the subject: once a verdict exists the
	// run loop has provably finished its immediate pass and is sitting in the
	// select, so nothing after this point can be attributed to that pass.
	r.RegisterPersonal("lens-1", &fakePersonal{})
	require.Eventually(t, func() bool { return !s.Verdict().CompletedAt.IsZero() }, 5*time.Second, time.Millisecond,
		"a registration must earn a pass without waiting for a tick — the run loop started over an EMPTY registry, so the immediate pass recorded nothing and only the registration's nudge can produce a verdict")
	assert.Equal(t, 2, s.Verdict().Attempted)

	// Now the subject, in registerPersonalHealer's own order: the registry
	// insert, then the licence's RegisteredAt stamp, then the nudge. The stamp
	// sits AFTER the insert, which is why the registry's own nudge is not enough
	// on its own and cmd/refractor fires a second one here.
	r.RegisterPersonal("lens-2", &fakePersonal{})
	registeredAt := time.Now()
	s.Nudge()

	require.Eventually(t, func() bool { return s.Verdict().StartedAt.After(registeredAt) }, 5*time.Second, time.Millisecond,
		"a lens registering into a running loop must see a pass BEGUN after its own registration instant; without the nudge the next one is a tick away and conjunct 3 refuses until then")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestPersonalSweep_RegistrationDuringAPassEarnsItsOwnPass is the race the
// single-slot nudge exists to answer.
//
// A pass drains the slot BEFORE it runs, so a registration landing while that
// pass is in flight refills it and the loop's next iteration runs one more pass
// — whose StartedAt is later than that registration. Drain the slot after the
// pass instead and the two coalesce into a pass that began before the lens
// existed, which conjunct 3 correctly refuses, leaving the lens on the
// enumerator until a tick.
//
// Driven off the health fan-out rather than a sleep: SetPersonalSweepProgress
// runs inside the pass, after its verdict is stamped, which is exactly the
// window in question.
func TestPersonalSweep_RegistrationDuringAPassEarnsItsOwnPass(t *testing.T) {
	_, keys := sweepActors(2)
	r := grantchange.New()
	s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, &fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
	s.SetBounds(10, time.Hour)

	registered := make(chan time.Time, 1)
	var once sync.Once
	first := &fakePersonal{}
	first.onProgress = func() {
		once.Do(func() {
			// Mid-pass: the running pass's StartedAt is already fixed and is
			// earlier than this instant, so only a LATER pass can cover it.
			r.RegisterPersonal("lens-2", &fakePersonal{})
			registered <- time.Now()
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.Run(ctx)
		close(done)
	}()

	r.RegisterPersonal("lens-1", first)

	var registeredAt time.Time
	select {
	case registeredAt = <-registered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first lens's pass never reached its health fan-out")
	}

	require.Eventually(t, func() bool { return s.Verdict().StartedAt.After(registeredAt) }, 5*time.Second, time.Millisecond,
		"the mid-pass registration must be covered by a FOLLOWING pass; a nudge dropped because one was already being served leaves it for the tick")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestPersonalSweep_VerdictReachesEveryLensHealthEntry pins the operator half:
// fifteen personal lenses dropping back onto the enumerator at once is an
// availability cliff, and the reason has to be readable from any one of them.
func TestPersonalSweep_VerdictReachesEveryLensHealthEntry(t *testing.T) {
	ctx := context.Background()
	_, keys := sweepActors(2)
	a, b := &fakePersonal{}, &fakePersonal{failWith: errors.New("capability-kv read fault")}
	r := grantchange.New()
	r.RegisterPersonal("lens-a", a)
	r.RegisterPersonal("lens-b", b)
	s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, &fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
	s.SetBounds(10, 0)

	s.Sweep(ctx)
	for name, lens := range map[string]*fakePersonal{"lens-a": a, "lens-b": b} {
		p := lens.reportedProgress()
		require.Lenf(t, p, 1, "%s must receive the pass's report", name)
		assert.Equalf(t, pipeline.PersonalHealerVerdictFailed, p[0].verdict,
			"%s must carry the SHARED healer's verdict, including the lens whose own reprojections succeeded — the plane's licence is refused for all of them", name)
	}
}

// TestPersonalSweep_InstanceCensusExcludesNestedLensKeys runs the census against
// a REAL Health KV, because the property under test belongs to the subject
// filter rather than to this package.
//
// The Health bucket carries per-lens keys nested under an instance
// (health.refractor.<instance>.lens.<name>) alongside the instance heartbeats
// themselves. health.InstanceKeyFilter's `*` matches exactly one token, so a
// nested key is not an instance — and it must not be, because one Refractor
// running fifteen personal lenses would otherwise census as sixteen instances
// and refuse its own licence forever, which is a self-inflicted availability
// cliff dressed as a safety property.
//
// A scripted lister cannot show this: it would have to reimplement NATS subject
// matching, and a test of my own reimplementation proves nothing about the
// server's. So this one goes through substrate.KV against an embedded server.
func TestPersonalSweep_InstanceCensusExcludesNestedLensKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	ctx := context.Background()
	_, nc := natsfixture.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "HEALTHCENSUS"})
	require.NoError(t, err)
	healthKV, err := conn.OpenKV(ctx, "HEALTHCENSUS")
	require.NoError(t, err)

	for _, k := range []string{
		health.InstanceKeyPrefix + "rfx-a1b2c3",
		health.InstanceKeyPrefix + "rfx-a1b2c3.lens.edgeCatalog",
		health.InstanceKeyPrefix + "rfx-a1b2c3.lens.edgeTasks",
		"health.weaver.wvr-000001",
	} {
		_, err := healthKV.Put(ctx, k, []byte(`{}`))
		require.NoError(t, err)
	}
	// The precondition, stated against the store rather than assumed: the bucket
	// really does hold the nested keys the filter has to exclude.
	all, err := healthKV.ListKeys(ctx)
	require.NoError(t, err)
	require.Len(t, all, 4)

	_, keys := sweepActors(2)
	r := grantchange.New()
	r.RegisterPersonal("lens-1", &fakePersonal{})
	s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys}, healthKV)
	s.SetBounds(10, 0)
	s.Sweep(ctx)

	require.True(t, s.Verdict().InstanceCountReadable)
	require.Equal(t, 1, s.Verdict().InstanceCount,
		"the single-token wildcard must count the instance heartbeat alone — not its per-lens sub-keys, and not another component's")
}

// TestPersonalSweep_VerdictBracketsThePass pins the two clocks the licence's
// per-lens conjunct needs.
//
// StartedAt is stamped BEFORE any work and CompletedAt after, so a lens that
// registers mid-pass can tell that pass did not cover it. Without StartedAt a
// lens joining an already-swept plane inherits a clean verdict from a pass that
// never drove it, and the licence narrows it on evidence about other lenses.
func TestPersonalSweep_VerdictBracketsThePass(t *testing.T) {
	ctx := context.Background()
	_, keys := sweepActors(2)
	r := grantchange.New()
	r.RegisterPersonal("lens-1", &fakePersonal{})
	s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys},
		&fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
	s.SetBounds(10, 0)

	before := time.Now()
	s.Sweep(ctx)
	v := s.Verdict()
	require.False(t, v.StartedAt.IsZero(), "a completed pass must say when it began")
	require.False(t, v.StartedAt.Before(before))
	require.False(t, v.CompletedAt.Before(v.StartedAt), "a pass cannot finish before it starts")

	// The failure paths owe the same bracket: an unreadable population is still
	// a pass that ran, and a lens registering after it must still not inherit it.
	lister := &fakeLister{}
	lister.setErr(errors.New("core kv unreachable"))
	s2 := grantchange.NewPersonalSweeper(r, lister,
		&fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
	s2.Sweep(ctx)
	require.False(t, s2.Verdict().StartedAt.IsZero(),
		"the unreadable-population verdict must carry its bracket too, or a lens registering after it reads the zero time and never earns a pass")
}

// TestPersonalSweep_TheTickerFollowsSetBounds pins m9's fix: every verdict
// advertises tickInterval() as the cadence the licence measures staleness
// against, so a bound changed after Run started must move the loop as well.
// The drift's direction is a licence staying granted through a cadence the
// sweeper is not actually running at.
func TestPersonalSweep_TheTickerFollowsSetBounds(t *testing.T) {
	_, keys := sweepActors(2)
	lens := &fakePersonal{}
	r := grantchange.New()
	r.RegisterPersonal("lens-1", lens)
	s := grantchange.NewPersonalSweeper(r, &fakeLister{keys: keys},
		&fakeLister{keys: []string{health.InstanceKeyPrefix + "rfx-a1b2c3"}})
	// Start on an interval no test will ever wait out, so any second pass can
	// only come from the ticker having been reset.
	s.SetBounds(10, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	require.Eventually(t, func() bool { return !s.Verdict().CompletedAt.IsZero() },
		5*time.Second, time.Millisecond, "Run's immediate first pass")
	first := s.Verdict().CompletedAt

	// Retune to a cadence this test can observe. The FIRST tick still fires on
	// the old hour-long interval, so the reset has to happen after that tick —
	// which is exactly where it is, and why this waits rather than asserting
	// immediately.
	s.SetBounds(10, time.Millisecond)
	require.Never(t, func() bool { return s.Verdict().CompletedAt.After(first) },
		200*time.Millisecond, 20*time.Millisecond,
		"the first tick is still owed to the interval Run captured; the reset lands after it, not before")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	// The property the pair gives, checkable without waiting out an hour: the
	// interval every verdict advertises is the LIVE bound — stamped at read time,
	// not carried from the pass that produced the verdict — so the licence's
	// staleness window and the loop's cadence are one number even when the bound
	// moved after the last pass. The unsafe direction is a cadence SHORTENED
	// after a pass: a verdict still advertising the old, longer interval would
	// hold the licence open past the window the sweeper is actually keeping.
	require.Equal(t, time.Millisecond, s.Verdict().Interval,
		"the advertised cadence must be the live bound, or the licence measures staleness against a clock nothing keeps")
}
