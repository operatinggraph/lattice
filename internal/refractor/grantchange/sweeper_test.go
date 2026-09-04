package grantchange_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// fakeLister is a scripted identity population, plus a call counter — which is
// the whole point of it: the sweep's contract is that it lists once per CYCLE,
// not once per tick, and nothing about the swept order would reveal a
// re-listing on its own.
type fakeLister struct {
	mu         sync.Mutex
	keys       []string
	calls      int
	lastFilter string
	lastLimit  int
	err        error
}

func (f *fakeLister) ListKeysFilter(ctx context.Context, filter, cursor string, limit int) ([]string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastFilter = filter
	f.lastLimit = limit
	if f.err != nil {
		return nil, "", f.err
	}
	return append([]string(nil), f.keys...), "", nil
}

func (f *fakeLister) setKeys(keys []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append([]string(nil), keys...)
}

func (f *fakeLister) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sweepActorAlphabet is the NanoID alphabet minus the characters it never
// contains, so a generated id is a key ParseVertexKey actually accepts.
const sweepActorAlphabet = "abcdefghijkmnopqrstuvwxyz"

// sweepActor builds the i-th test identity: a valid 20-character NanoID whose
// lexical order matches i, so the expected round-robin order is readable from
// the index rather than from a sort in the assertion.
func sweepActor(i int) string {
	return "SweepActor" + strings.Repeat("A", 9) + string(sweepActorAlphabet[i])
}

func sweepActors(n int) (ids []string, keys []string) {
	for i := 0; i < n; i++ {
		id := sweepActor(i)
		ids = append(ids, id)
		keys = append(keys, substrate.VertexKey("identity", id))
	}
	return ids, keys
}

// newSweptFixture wires a reprojector with one registered lens and a sweeper
// over a scripted population.
func newSweptFixture(t *testing.T, n, batch int) (*grantchange.PersonalSweeper, *fakeLister, *fakePersonal, []string) {
	t.Helper()
	ids, keys := sweepActors(n)
	lister := &fakeLister{keys: keys}
	r := grantchange.New()
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)
	s := grantchange.NewPersonalSweeper(r, lister, nil)
	s.SetBounds(batch, 0)
	return s, lister, lens, ids
}

func TestPersonalSweep_WalksTheWholePopulationInBoundedBatches(t *testing.T) {
	s, lister, lens, ids := newSweptFixture(t, 5, 2)
	ctx := context.Background()

	s.Sweep(ctx)
	assert.Equal(t, ids[0:2], lens.seen(), "one tick sweeps at most the batch")
	assert.Equal(t, ids[1], s.Cursor(), "the cursor names the last identity swept")
	assert.True(t, s.CycleCompletedAt().IsZero(), "a cycle that has not reached the end has completed nothing")

	s.Sweep(ctx)
	assert.Equal(t, ids[0:4], lens.seen(), "the next tick resumes just past the cursor")
	assert.True(t, s.CycleCompletedAt().IsZero())

	// The tick that reaches the end: a short final batch, and the wrap.
	s.Sweep(ctx)
	assert.Equal(t, ids, lens.seen(), "the whole population is covered in one cycle, each identity once")
	assert.False(t, s.CycleCompletedAt().IsZero(), "reaching the end of the population closes a cycle")
	assert.Empty(t, s.Cursor(), "a closed cycle resets the walk to the top")

	// One listing for the whole cycle — the enumeration is unpaged, so a
	// per-tick re-list would cost the entire population every minute.
	assert.Equal(t, 1, lister.callCount(), "the population is listed once per CYCLE, not once per tick")

	s.Sweep(ctx)
	assert.Equal(t, append(append([]string(nil), ids...), ids[0:2]...), lens.seen(),
		"the next cycle starts again from the top")
	assert.Equal(t, 2, lister.callCount(), "and re-lists exactly once, at the wrap")
}

func TestPersonalSweep_ABatchThatCoversTheWholePopulationClosesACycleInOneTick(t *testing.T) {
	s, _, lens, ids := newSweptFixture(t, 3, 10)

	s.Sweep(context.Background())
	assert.Equal(t, ids, lens.seen())
	assert.False(t, s.CycleCompletedAt().IsZero())
}

func TestPersonalSweep_ListsOnlyIdentityRootsAndSaysSoInTheFilter(t *testing.T) {
	ids, keys := sweepActors(2)
	// Everything the filter can let through that is not an identity root: an
	// aspect of one of them, a link, a different vertex type, and a malformed
	// key. The filter is a COST mechanism; the parse is what decides.
	keys = append(keys,
		substrate.VertexKey("identity", ids[0])+".profile",
		substrate.LinkKey("identity", ids[0], "holds", "lease", sweepActor(5)),
		substrate.VertexKey("lease", sweepActor(6)),
		"vtx.identity.not-a-nanoid",
		"",
	)
	lister := &fakeLister{keys: keys}
	r := grantchange.New()
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)
	s := grantchange.NewPersonalSweeper(r, lister, nil)
	s.SetBounds(10, 0)

	s.Sweep(context.Background())

	assert.Equal(t, ids, lens.seen(), "only well-formed identity roots are swept, and as bare NanoIDs")
	assert.Equal(t, substrate.VertexPrefix+".identity.*", lister.lastFilter,
		"the listing is scoped to the identity population server-side")
	assert.Equal(t, 0, lister.lastLimit,
		"limit 0 is the whole population in one page — a page boundary would hand the walk a partial census that reads like a complete one")
}

func TestPersonalSweep_PublishesItsPositionToEveryRegisteredLens(t *testing.T) {
	ids, keys := sweepActors(3)
	lister := &fakeLister{keys: keys}
	r := grantchange.New()
	first, second := &fakePersonal{}, &fakePersonal{}
	r.RegisterPersonal("lens-1", first)
	r.RegisterPersonal("lens-2", second)
	// Two actors owed a reprojection by the fast path, so the depth the sweep
	// publishes is a number and not a constant zero.
	r.GrantChanged(substrate.VertexKey("identity", actorA), "")
	r.GrantChanged(substrate.VertexKey("identity", actorB), "")

	s := grantchange.NewPersonalSweeper(r, lister, nil)
	s.SetBounds(2, 0)
	ctx := context.Background()

	s.Sweep(ctx)
	for _, lens := range []*fakePersonal{first, second} {
		progress := lens.reportedProgress()
		require.Len(t, progress, 1, "every registered personal lens hears about the shared sweep")
		assert.Equal(t, ids[1], progress[0].cursor)
		assert.True(t, progress[0].cycleCompletedAt.IsZero(),
			"an intermediate tick stamps no cycle — the reporter's zero-time rule then leaves the stored claim alone")
		assert.Equal(t, uint64(2), progress[0].queueDepth,
			"the drain's backlog rides along: this is the only place that gauge reaches an operator")
	}

	s.Sweep(ctx)
	progress := first.reportedProgress()
	require.Len(t, progress, 2)
	assert.Equal(t, ids[2], progress[1].cursor)
	assert.False(t, progress[1].cycleCompletedAt.IsZero(), "the tick that closes a cycle stamps it")
}

// A pass that swept nobody still owes a VERDICT — what it publishes and what it
// must NOT publish are different fields, and the distinction is the point.
// Neither of the two cases below may write a cursor or a cycle stamp: those are
// coverage claims, and a walk that reached no batch has covered nothing. Both
// must write the verdict, because the personal derivation licence reads it and
// the entry an operator opens has to say why fifteen lenses stopped narrowing.
func TestPersonalSweep_AnEmptyPopulationPublishesNoCoverageClaimAndKeepsLooking(t *testing.T) {
	lister := &fakeLister{}
	r := grantchange.New()
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)
	s := grantchange.NewPersonalSweeper(r, lister, nil)
	ctx := context.Background()

	s.Sweep(ctx)
	s.Sweep(ctx)
	assert.Empty(t, lens.seen())
	for i, p := range lens.reportedProgress() {
		assert.Empty(t, p.cursor,
			"pass %d: a cursor written over a population nobody swept would claim coverage the sweep does not have", i)
		assert.True(t, p.cycleCompletedAt.IsZero(),
			"pass %d: a cycle stamp over nobody is the same false claim", i)
	}
	// An EMPTY population is a CLEAN pass, not a failed one: a cell with no
	// identities has no personal rows for anything to leave stale, and refusing
	// the licence there would put every personal lens on the enumerator to heal
	// nothing. The instance census is what refuses here, because this fixture
	// threads no health lister — the fail-closed answer for a deployment whose
	// cardinality nothing can read.
	require.NotEmpty(t, lens.reportedProgress())
	assert.Equal(t, pipeline.PersonalHealerVerdictInstancesUnreadable, lens.reportedProgress()[0].verdict)
	assert.True(t, s.Verdict().PopulationReadable,
		"an empty population is readable; it is the LISTING that must be able to fail separately")
	assert.Zero(t, s.Verdict().Failed)

	// The cache is invalidated by a cycle WRAPPING, and a walk over nothing
	// never wraps — so an empty answer must not be cached, or a cell that boots
	// before its first identity exists stays unswept for the life of the process.
	assert.Equal(t, 2, lister.callCount())

	ids, keys := sweepActors(2)
	lister.setKeys(keys)
	s.Sweep(ctx)
	assert.Equal(t, ids, lens.seen(), "the identities that appear later are picked up")
}

func TestPersonalSweep_AFailedListingSkipsTheCycleAndRetries(t *testing.T) {
	s, lister, lens, ids := newSweptFixture(t, 2, 10)
	lister.setErr(errors.New("core kv unreachable"))
	ctx := context.Background()

	s.Sweep(ctx)
	assert.Empty(t, lens.seen())
	require.Len(t, lens.reportedProgress(), 1)
	assert.Empty(t, lens.reportedProgress()[0].cursor, "a cycle nobody could enumerate publishes no coverage claim")
	assert.True(t, lens.reportedProgress()[0].cycleCompletedAt.IsZero())
	// The verdict is the whole point of the distinction: a healer that cannot
	// see its own population is not covering it, and the licence must read that
	// rather than inherit the previous pass's clean answer.
	assert.Equal(t, pipeline.PersonalHealerVerdictPopulationUnreadable, lens.reportedProgress()[0].verdict)
	assert.False(t, s.Verdict().PopulationReadable)

	lister.setErr(nil)
	s.Sweep(ctx)
	assert.Equal(t, ids, lens.seen(), "the next tick retries the listing")
}

func TestPersonalSweep_AnIdentityAddedMidCycleIsSweptOnTheNextOne(t *testing.T) {
	s, lister, lens, ids := newSweptFixture(t, 4, 2)
	ctx := context.Background()

	s.Sweep(ctx)
	// A fifth identity appears while the cycle is in flight. It is deliberately
	// NOT picked up mid-cycle: the cached census is what makes the cursor mean
	// something, and the fast path covers a brand-new identity's grants anyway.
	extraID := sweepActor(4)
	lister.setKeys(append(append([]string(nil), keysOf(ids)...), substrate.VertexKey("identity", extraID)))

	s.Sweep(ctx)
	assert.Equal(t, ids, lens.seen(), "the cycle in flight walks the census it started with")
	assert.False(t, s.CycleCompletedAt().IsZero())

	s.Sweep(ctx)
	assert.Equal(t, append(append([]string(nil), ids...), ids[0], ids[1]), lens.seen())
	s.Sweep(ctx)
	s.Sweep(ctx)
	assert.Contains(t, lens.seen(), extraID, "the next cycle's re-listing picks it up")
}

func TestPersonalSweep_AFailedHealthWriteDoesNotStopTheWalk(t *testing.T) {
	ids, keys := sweepActors(2)
	lister := &fakeLister{keys: keys}
	r := grantchange.New()
	mute, heard := &fakePersonal{progressErr: errors.New("health kv unreachable")}, &fakePersonal{}
	r.RegisterPersonal("a-mute", mute)
	r.RegisterPersonal("b-heard", heard)
	s := grantchange.NewPersonalSweeper(r, lister, nil)
	s.SetBounds(10, 0)

	s.Sweep(context.Background())

	assert.Equal(t, ids, mute.seen(), "the reprojection is the work; the health write is the observability")
	assert.Equal(t, ids, heard.seen())
	assert.Len(t, heard.reportedProgress(), 1, "one lens's unwritable entry does not silence the others")
}

// A lens deleted while the sweep is fanning its progress out must not be
// written to. SetPersonalSweepProgress is a read-modify-PUT: on a missing entry
// the read synthesizes a zero one and the write puts it back, so writing to a
// deleted lens RE-CREATES the Health entry the deleter just removed — an orphan
// row reading "active" for a lens that no longer exists, with no TTL behind it
// to reap it. Worse than the drain's guarded case, because this write runs on
// every tick unconditionally rather than on an error branch.
func TestPersonalSweep_ALensDeletedMidFanOutGetsNoProgressWrite(t *testing.T) {
	ids, keys := sweepActors(2)
	lister := &fakeLister{keys: keys}
	r := grantchange.New()
	doomed := &fakePersonal{}
	// The fan-out is sorted by rule ID, so "a-survivor" is written before
	// "b-doomed" deterministically — and it deletes the doomed lens from inside
	// its own progress write, which IS the window.
	survivor := &fakePersonal{onProgress: func() { r.DeregisterPersonal("b-doomed") }}
	r.RegisterPersonal("a-survivor", survivor)
	r.RegisterPersonal("b-doomed", doomed)
	s := grantchange.NewPersonalSweeper(r, lister, nil)
	s.SetBounds(10, 0)

	s.Sweep(context.Background())

	assert.Equal(t, ids, doomed.seen(), "the deletion lands after the batch, so the reprojections themselves stand")
	assert.Empty(t, doomed.reportedProgress(),
		"a lens deregistered mid-fan-out must not have its Health entry re-created by the progress write")
	assert.Len(t, survivor.reportedProgress(), 1, "and the lenses that are still live are still reported on")
}

func TestPersonalSweep_SetBoundsLeavesTheDefaultsForZeroOrNegative(t *testing.T) {
	s, _, lens, ids := newSweptFixture(t, grantchange.DefaultPersonalSweepBatch+2, 0)
	s.SetBounds(-1, -time.Second)

	s.Sweep(context.Background())
	assert.Equal(t, ids[:grantchange.DefaultPersonalSweepBatch], lens.seen(),
		"a zero or negative override leaves the shipped batch in place")
}

func TestPersonalSweep_RunTicksUntilTheContextIsCancelled(t *testing.T) {
	s, _, lens, ids := newSweptFixture(t, 3, 1)
	s.SetBounds(1, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()

	require.Eventually(t, func() bool { return len(lens.seen()) >= len(ids) },
		5*time.Second, 5*time.Millisecond, "the ticker must drive the walk without anyone calling Sweep")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func keysOf(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, substrate.VertexKey("identity", id))
	}
	return out
}

func TestPersonalSweep_WithNoRegisteredLensItSpendsNothing(t *testing.T) {
	_, keys := sweepActors(3)
	lister := &fakeLister{keys: keys}
	r := grantchange.New()
	s := grantchange.NewPersonalSweeper(r, lister, nil)
	s.SetBounds(10, 0)
	ctx := context.Background()

	s.Sweep(ctx)
	s.Sweep(ctx)
	assert.Zero(t, lister.callCount(),
		"with no personal lens registered every reprojection is a no-op, so the whole-population listing is bought for nothing")

	// And a lens registering later simply resumes the walk — the check is per
	// tick, not latched, because THIS is the case the sweep exists for.
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)
	s.Sweep(ctx)
	assert.Len(t, lens.seen(), 3)
	assert.Equal(t, 1, lister.callCount())
}
