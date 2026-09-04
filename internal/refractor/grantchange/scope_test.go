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
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// scopeAnchorX/Y/Z are the anchors a grant change names. Valid NanoIDs, since
// the scope's own match parses the row's anchor key.
const (
	scopeAnchorX = "Zwq9PmRtw3nbCxz5vQ2y"
	scopeAnchorY = "Ywq9PmRtw3nbCxz5vQ2x"
	scopeAnchorZ = "Nb7RvwKx3TmZpq2Hc9Ls"
)

// scopeAnchorN generates n distinct valid NanoIDs, for the bound vectors.
func scopeAnchorN(n int) []string {
	const alphabet = "abcdefghijkmnopqrstuvwxyz"
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, "GrantAnchorAAAAAA"+
			string(alphabet[i/len(alphabet)])+string(alphabet[i%len(alphabet)])+"z")
	}
	return ids
}

// admits reports whether scope publishes the row anchored at anchorID — the
// scope's own decision, asked exactly as the write loop asks it.
func admits(scope pipeline.PublishScope, anchorID string) bool {
	return scope.Admits(ruleengineRow(substrate.VertexKey("lease", anchorID)))
}

// TestEnqueue_MergesTheScopeOfCoalescedSignals is T5's merge-law table, driven
// through the public edges rather than the law's own unit test: what matters
// here is that the two edges hand the right scope in and that the queue folds
// them by the law, not that the law holds (pipeline's own table pins that).
func TestEnqueue_MergesTheScopeOfCoalescedSignals(t *testing.T) {
	actorKey := substrate.VertexKey("identity", actorA)

	cases := []struct {
		name string
		// signal each apply one edge to the reprojector, in order.
		signal   func(r *grantchange.Reprojector)
		wantKind pipeline.ScopeKind
		admits   []string
		declines []string
	}{
		{
			name:     "one grant change scopes to its own anchor",
			signal:   func(r *grantchange.Reprojector) { r.GrantChanged(actorKey, scopeAnchorX) },
			wantKind: pipeline.ScopeKindAnchors,
			admits:   []string{scopeAnchorX},
			declines: []string{scopeAnchorY},
		},
		{
			name:     "a grant change with no entry token publishes the whole actor",
			signal:   func(r *grantchange.Reprojector) { r.GrantChanged(actorKey, "") },
			wantKind: pipeline.ScopeKindAll,
			admits:   []string{scopeAnchorX, scopeAnchorY},
		},
		{
			name: "two anchors coalesce into their union",
			signal: func(r *grantchange.Reprojector) {
				r.GrantChanged(actorKey, scopeAnchorX)
				r.GrantChanged(actorKey, scopeAnchorY)
			},
			wantKind: pipeline.ScopeKindAnchors,
			admits:   []string{scopeAnchorX, scopeAnchorY},
			declines: []string{scopeAnchorZ},
		},
		{
			name: "a whole-actor signal widens a scoped one",
			signal: func(r *grantchange.Reprojector) {
				r.GrantChanged(actorKey, scopeAnchorX)
				r.GrantChanged(actorKey, "")
			},
			wantKind: pipeline.ScopeKindAll,
			admits:   []string{scopeAnchorY},
		},
		{
			name: "and a scoped signal cannot narrow a whole-actor one",
			signal: func(r *grantchange.Reprojector) {
				r.GrantChanged(actorKey, "")
				r.GrantChanged(actorKey, scopeAnchorX)
			},
			wantKind: pipeline.ScopeKindAll,
			admits:   []string{scopeAnchorY},
		},
		{
			name: "an interest change widens to the whole actor",
			signal: func(r *grantchange.Reprojector) {
				r.GrantChanged(actorKey, scopeAnchorX)
				r.InterestChanged(actorA)
			},
			wantKind: pipeline.ScopeKindAll,
			admits:   []string{scopeAnchorY},
		},
		{
			name: "an interest change alone publishes the whole actor",
			signal: func(r *grantchange.Reprojector) {
				r.InterestChanged(actorA)
			},
			wantKind: pipeline.ScopeKindAll,
			admits:   []string{scopeAnchorX},
		},
		{
			name: "a union AT the bound stays scoped",
			signal: func(r *grantchange.Reprojector) {
				for _, id := range scopeAnchorN(pipeline.MaxScopedAnchors - 1) {
					r.GrantChanged(actorKey, id)
				}
				r.GrantChanged(actorKey, scopeAnchorX)
			},
			wantKind: pipeline.ScopeKindAnchors,
			admits:   []string{scopeAnchorX},
			declines: []string{scopeAnchorY},
		},
		{
			name: "a union PAST the bound widens to the whole actor",
			signal: func(r *grantchange.Reprojector) {
				for _, id := range scopeAnchorN(pipeline.MaxScopedAnchors) {
					r.GrantChanged(actorKey, id)
				}
				r.GrantChanged(actorKey, scopeAnchorX)
			},
			wantKind: pipeline.ScopeKindAll,
			admits:   []string{scopeAnchorY},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := grantchange.New()
			lens := &fakePersonal{}
			r.RegisterPersonal("lens-1", lens)

			tc.signal(r)
			require.Equal(t, 1, r.QueueDepth(), "every signal above is for one actor, so it is one reprojection")
			r.Drain(context.Background())

			scopes := lens.scopesSeen()
			require.Len(t, scopes, 1)
			require.Equal(t, tc.wantKind, scopes[0].Kind(), "scope was %s", scopes[0])
			for _, id := range tc.admits {
				assert.True(t, admits(scopes[0], id), "the reprojection must publish the row anchored at %s", id)
			}
			for _, id := range tc.declines {
				assert.False(t, admits(scopes[0], id), "the reprojection must withhold the row anchored at %s", id)
			}
		})
	}
}

// TestEnqueue_MergingIntoAnExistingEntryIsNotANewEntry pins the bound and the
// drop accounting across the value change.
//
// The dirty set's bound stands between a mass grant change and unbounded
// memory, and its drop count is the only evidence an operator gets that signals
// were refused. A merge is not a new entry — the actor was already owed a
// reprojection — so it must neither be weighed against the bound nor counted as
// a drop, whatever it does to the scope.
func TestEnqueue_MergingIntoAnExistingEntryIsNotANewEntry(t *testing.T) {
	r := grantchange.New()
	r.SetBounds(1, 0)
	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)
	actorKey := substrate.VertexKey("identity", actorA)

	// The set is full after the first signal. Every one after it for the SAME
	// actor merges; the one for a different actor is the drop.
	r.GrantChanged(actorKey, scopeAnchorX)
	for _, id := range scopeAnchorN(20) {
		r.GrantChanged(actorKey, id)
	}
	assert.Equal(t, 1, r.QueueDepth(), "merges do not grow the set")

	r.GrantChanged(substrate.VertexKey("identity", actorB), scopeAnchorY)
	r.Drain(context.Background())

	assert.Equal(t, []string{actorA}, lens.seen(), "the second actor was refused at the bound")
	require.Equal(t, []string{"overflow"}, lens.raised(),
		"the refused actor is reported, so the count below is a real reading and not a dead counter")
	details := lens.reportedDetails()
	require.Len(t, details, 1)
	assert.Contains(t, details[0], "dropped 1 signal(s) cumulative",
		"exactly ONE signal was refused — the 20 merges into a waiting entry are not drops and must not be counted as any")
}

// TestSweep_PublishesFramesOnlyBetweenContentCycles is T5's healer arm.
//
// The first cycle after construction is a CONTENT cycle — a process that just
// started republishes rows once — and the cycles between content cycles publish
// the authoritative frame alone. Both halves are asserted through the scope the
// reprojection actually received, which is the effect, not through the latch's
// own field.
func TestSweep_PublishesFramesOnlyBetweenContentCycles(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
	s, _, lens, ids := newSweptFixture(t, 4, 2)
	s.SetClock(clock.Now)
	ctx := context.Background()

	// Cycle 1, both ticks: the first cycle after boot carries content.
	s.Sweep(ctx)
	s.Sweep(ctx)
	require.Equal(t, ids, lens.seen(), "the first cycle covers the population")
	for i, scope := range lens.scopesSeen() {
		assert.Equal(t, pipeline.ScopeKindAll, scope.Kind(),
			"pass %d of the first cycle must republish rows: nothing has been sent since boot", i)
	}

	// Cycle 2, a minute later: the content latch is fresh, so this cycle is
	// frames-only.
	clock.advance(time.Minute)
	s.Sweep(ctx)
	s.Sweep(ctx)
	framesOnly := lens.scopesSeen()[len(ids):]
	require.Len(t, framesOnly, len(ids))
	for i, scope := range framesOnly {
		assert.Equal(t, pipeline.ScopeKindNone, scope.Kind(),
			"reprojection %d of the second cycle must publish the frame alone", i)
	}

	// A cycle that starts after the interval has elapsed carries content again.
	clock.advance(grantchange.PersonalContentHealInterval)
	s.Sweep(ctx)
	next := lens.scopesSeen()[2*len(ids):]
	require.NotEmpty(t, next)
	assert.Equal(t, pipeline.ScopeKindAll, next[0].Kind(),
		"a day after the last content cycle the next cycle carries content again")
}

// TestSweep_ContentCycleMeasuresAgainstTheProjectedCycleEnd pins WHERE the
// content latch measures to: the END of the cycle it is deciding, not its start.
//
// Rows are republished by a whole CYCLE, so a latch asking "has the interval
// already elapsed?" bought a heal every cycleLength × ceil(interval /
// cycleLength) — 46 h for a 23 h cycle. Deciding on where the NEXT cycle would
// close makes the heal at least once per interval and at most once per cycle.
//
// The passes are driven by hand rather than by the ticker, so the wall clock is
// compressed; what the rule reads is the cycle length PROJECTED from the
// configured population, batch and interval.
func TestSweep_ContentCycleMeasuresAgainstTheProjectedCycleEnd(t *testing.T) {
	t.Run("a cycle shorter than the interval heals within it, not at twice it", func(t *testing.T) {
		// 23 identities, one per pass, an hour apart: a 23 h cycle. Under a
		// start-measured latch the cycle beginning at 23 h reads "not yet a
		// day" and goes frames-only, putting the next content pass at 46 h.
		const population, batch, interval = 23, 1, time.Hour
		clock := &testClock{now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
		s, _, lens, ids := newSweptFixture(t, population, batch)
		s.SetBounds(batch, interval)
		s.SetClock(clock.Now)
		ctx := context.Background()

		for range ids {
			s.Sweep(ctx)
			clock.advance(interval)
		}
		first := lens.scopesSeen()
		require.Len(t, first, population, "the boot content cycle covers the population one pass at a time")
		for i, scope := range first {
			require.Equal(t, pipeline.ScopeKindAll, scope.Kind(), "pass %d belongs to the boot content cycle", i)
		}

		// The cycle starting here begins 23 h after the last heal and would not
		// close until 46 h, which is past the window the interval promises.
		s.Sweep(ctx)

		scopes := lens.scopesSeen()
		require.Len(t, scopes, population+1)
		assert.Equal(t, pipeline.ScopeKindAll, scopes[population].Kind(),
			"the next cycle would close 46 h after the last heal, so this one must carry content")
	})

	t.Run("a cycle at least as long as the interval carries content every time", func(t *testing.T) {
		// Two identities one at a time on a day-long tick: a 48 h cycle, longer
		// than the 24 h window. No cycle can be skipped without missing the
		// window, so every cycle is a content cycle and the frames-only saving
		// is nil — exactly what PersonalContentHealInterval's doc promises for
		// such a deployment.
		const population, batch, interval, cycles = 2, 1, 24 * time.Hour, 3
		clock := &testClock{now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
		s, _, lens, ids := newSweptFixture(t, population, batch)
		s.SetBounds(batch, interval)
		s.SetClock(clock.Now)

		for range cycles {
			for range ids {
				s.Sweep(context.Background())
				// A minute, not the interval: the wall clock a hand-driven test
				// keeps is not the cadence, and the projected cycle length is
				// what the latch reads. Under a start-measured latch these
				// cycles would all read "healed a minute ago" and go
				// frames-only, never republishing a row again.
				clock.advance(time.Minute)
			}
		}

		scopes := lens.scopesSeen()
		require.Len(t, scopes, cycles*population)
		for i, scope := range scopes {
			assert.Equal(t, pipeline.ScopeKindAll, scope.Kind(),
				"pass %d: a cycle longer than the interval can never be skipped", i)
		}
	})
}

// TestSweep_TheCycleKindIsLatchedAtTheCycleStart pins that the decision is per
// CYCLE, not per pass: a content cycle whose latch was stamped at its first
// tick must not read as "recently healed" on its own later ticks and drop the
// rest of the population back to frames.
func TestSweep_TheCycleKindIsLatchedAtTheCycleStart(t *testing.T) {
	clock := &testClock{now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
	// Four identities in batches of one, so the content cycle spans four passes
	// and the latch is a day stale for none of them.
	s, _, lens, ids := newSweptFixture(t, 4, 1)
	s.SetClock(clock.Now)

	for range ids {
		clock.advance(time.Minute)
		s.Sweep(context.Background())
	}

	scopes := lens.scopesSeen()
	require.Len(t, scopes, len(ids))
	for i, scope := range scopes {
		assert.Equal(t, pipeline.ScopeKindAll, scope.Kind(),
			"pass %d belongs to the content cycle that started at pass 0", i)
	}
}

// TestSweep_TheVerdictIsIdenticalAcrossScopes pins the reason a frames-only
// pass is sound: nothing reads what a pass published. The verdict counts
// reprojections attempted and reprojections that failed, and neither is a
// statement about rows.
func TestSweep_TheVerdictIsIdenticalAcrossScopes(t *testing.T) {
	run := func(t *testing.T, contentCycle bool) pipeline.PersonalHealerVerdict {
		t.Helper()
		clock := &testClock{now: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)}
		ids, keys := sweepActors(3)
		lister := &fakeLister{keys: keys}
		r := grantchange.New()
		// One healthy lens and one that fails every reprojection, so the
		// verdict carries a non-zero Failed as well as a non-zero Attempted.
		r.RegisterPersonal("lens-ok", &fakePersonal{})
		failing := &fakePersonal{failWith: errFakeReproject}
		r.RegisterPersonal("lens-bad", failing)
		s := grantchange.NewPersonalSweeper(r, lister, nil)
		s.SetBounds(len(ids), 0)
		s.SetClock(clock.Now)

		if !contentCycle {
			// Burn the boot content cycle, then start a fresh cycle inside the
			// interval — the frames-only case.
			s.Sweep(context.Background())
			clock.advance(time.Minute)
		}
		s.Sweep(context.Background())

		scopes := failing.scopesSeen()
		require.NotEmpty(t, scopes)
		last := scopes[len(scopes)-1]
		if contentCycle {
			require.Equal(t, pipeline.ScopeKindAll, last.Kind(), "fixture must actually be on a content cycle")
		} else {
			require.Equal(t, pipeline.ScopeKindNone, last.Kind(), "fixture must actually be on a frames-only cycle")
		}
		return s.Verdict()
	}

	content := run(t, true)
	frames := run(t, false)

	assert.Equal(t, 3, content.Attempted)
	assert.Equal(t, 3, content.Failed, "one of the two lenses fails for every actor")
	assert.Equal(t, content.Attempted, frames.Attempted,
		"a frames-only pass attempts exactly what a content pass attempts")
	assert.Equal(t, content.Failed, frames.Failed,
		"and fails exactly as often — the verdict is about reprojections, never about rows")
}

// TestSweep_ScopeSurvivesTheProductionStartupOrder runs the sweeper the way
// cmd/refractor does: the loop starts BEFORE any personal lens registers, and
// the registration is what kicks the first real pass.
//
// A fixture that registered first would prove nothing about that order — the
// pass a registration nudges is the one every deployment's first content cycle
// actually is. The barrier is the EFFECT (a reprojection with a scope), never
// the loop's internal state.
func TestSweep_ScopeSurvivesTheProductionStartupOrder(t *testing.T) {
	ids, keys := sweepActors(2)
	lister := &fakeLister{keys: keys}
	r := grantchange.New()
	s := grantchange.NewPersonalSweeper(r, lister, nil)
	s.SetBounds(len(ids), time.Hour) // the ticker must never fire; the nudge is what drives this

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	lens := &fakePersonal{}
	r.RegisterPersonal("lens-1", lens)

	// Barriered on the EFFECT — reprojections that actually happened — because
	// the registration can land either side of Run's own immediate pass, and a
	// barrier on an exact count would be asserting which side it landed on. The
	// FIRST cycle is what this pins whichever pass ran it.
	require.Eventually(t, func() bool { return len(lens.seen()) >= len(ids) }, 10*time.Second, 5*time.Millisecond,
		"the pass a registration nudges must cover the population")

	assert.Equal(t, ids, lens.seen()[:len(ids)], "the first cycle walks the whole population in order")
	for i, scope := range lens.scopesSeen()[:len(ids)] {
		assert.Equal(t, pipeline.ScopeKindAll, scope.Kind(),
			"reprojection %d: the first cycle after boot carries content", i)
	}
}

// testClock is a hand-advanced clock, so a day-wide latch is crossed without
// waiting one out and without a sleep anywhere.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// ruleengineRow builds the row shape a scope's anchor match reads.
func ruleengineRow(anchorKey string) ruleengine.EvalResult {
	return ruleengine.EvalResult{Row: map[string]any{"anchor": anchorKey}}
}

var errFakeReproject = errors.New("injected: reprojection failed")

// TestScopeAnchorN_GeneratesRealNanoIDs keeps the generated bound vectors
// honest — a generated anchor the parser rejects would be declined for the
// wrong reason, and a duplicate would never reach the bound at all.
func TestScopeAnchorN_GeneratesRealNanoIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, id := range scopeAnchorN(pipeline.MaxScopedAnchors + 1) {
		require.True(t, substrate.IsValidNanoID(id), "generated anchor %q is not a NanoID", id)
		require.False(t, seen[id], "generated anchors must be distinct, got %q twice", id)
		seen[id] = true
	}
	require.Equal(t, pipeline.MaxScopedAnchors+1, len(seen))
}
