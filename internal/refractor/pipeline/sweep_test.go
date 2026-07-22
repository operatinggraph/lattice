package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// sweepBuildKey / sweepAnchorFromKey mirror the capabilityRoles descriptor's
// key shape (cap.roles.<type>.<id>), the production auth-plane lens the sweep
// runs against. The pair must round-trip: the sweep compares a computed key
// set against a listed one, so a one-sided rendering would report every actor
// as divergent.
func sweepBuildKey(actorKey string) string {
	return "cap.roles." + strings.TrimPrefix(actorKey, "vtx.")
}

func sweepAnchorFromKey(targetKey string) (string, bool) {
	rest, ok := strings.CutPrefix(targetKey, "cap.roles.")
	if !ok {
		return "", false
	}
	actorKey := "vtx." + rest
	vtxType, _, parsed := substrate.ParseVertexKey(actorKey)
	if !parsed || vtxType != "identity" {
		return "", false
	}
	return actorKey, true
}

// listingAdapter is a recordingAdapter that can also enumerate the target's
// live keys, so a test can pose both prefilter directions.
type listingAdapter struct {
	recordingAdapter
	keys []string
}

func (a *listingAdapter) ListKeys(context.Context) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(a.keys))
	for _, k := range a.keys {
		out = append(out, map[string]any{"key": k})
	}
	return out, nil
}

const (
	sweepActorA = "vtx.identity.Tswp1AaaaaaaaaaaaaaZ"
	sweepActorB = "vtx.identity.Tswp2BbbbbbbbbbbbbbZ"
	sweepActorC = "vtx.identity.Tswp3CcccccccccccccZ"
)

// newSweepPipeline builds an actor-aggregate pipeline with a sweep plan
// installed and a real (empty) Core KV, so the missing-actor branch resolves
// against a genuine ErrKeyNotFound.
func newSweepPipeline(t *testing.T, adpt *listingAdapter, batch int) *Pipeline {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	p := &Pipeline{
		ruleID:          "sweep-rule",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      &full.Engine{},
		fullCR:          &full.CompiledRule{},
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
		adpt:            adpt,
	}
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	p.SetActorDeleteKey(sweepBuildKey)
	p.SetSweepPlan(SweepPlan{
		AnchorType:    "identity",
		BuildKey:      sweepBuildKey,
		AnchorFromKey: sweepAnchorFromKey,
		Interval:      time.Hour, // ticks are driven explicitly by the tests
		Batch:         batch,
	})
	return p
}

// writeAnchor seeds a Core KV identity vertex; deleted writes the tombstoned
// form (a live NATS-KV key carrying isDeleted).
func writeAnchor(t *testing.T, p *Pipeline, actorKey string, deleted bool) {
	t.Helper()
	body := map[string]any{"key": actorKey, "class": "identity", "data": map[string]any{}}
	if deleted {
		body["isDeleted"] = true
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = p.coreKV.Put(context.Background(), actorKey, data)
	require.NoError(t, err)
}

func TestRunSweep_NoPlanInstalled_ReturnsImmediately(t *testing.T) {
	// A plain, personal, convergence, or operation-aggregate lens never
	// receives a plan, which is what excludes it structurally. Starting the
	// goroutine unconditionally beside Run must therefore be free.
	p := newDeleteKeyPipeline(t, nil)
	require.Nil(t, p.Sweeper())
	done := make(chan struct{})
	go func() { p.RunSweep(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunSweep did not return for a pipeline with no sweep plan")
	}
}

func TestSweepCandidates_AnchorWithNoTargetKeyIsDivergent(t *testing.T) {
	adpt := &listingAdapter{keys: []string{sweepBuildKey(sweepActorA)}}
	p := newSweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)
	writeAnchor(t, p, sweepActorB, false)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{sweepActorA, sweepActorB}, anchors)

	got := p.Sweeper().candidates(context.Background(), anchors, targets)
	// B is the definite divergence (the observed first-projection loss) and
	// must be picked first, ahead of the round-robin walk.
	require.Equal(t, sweepActorB, got[0])
}

func TestSweepCandidates_TombstonedAnchorIsNotADivergence(t *testing.T) {
	// A tombstoned anchor legitimately has no target key. Counting it as a
	// definite divergence would refill the batch every tick forever and starve
	// the deep verify.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 1)
	writeAnchor(t, p, sweepActorA, true)  // tombstoned, no row — correct
	writeAnchor(t, p, sweepActorB, false) // live, no row — the real divergence

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	got := p.Sweeper().candidates(context.Background(), anchors, targets)
	require.Equal(t, []string{sweepActorB}, got,
		"the batch must go to the live unprojected anchor, not the tombstoned one")
}

func TestSweepCandidates_OrphanTargetKeyIsDivergent(t *testing.T) {
	// The over-grant direction: a row survives an anchor that is gone from
	// Core KV entirely. Nothing will ever re-drive it — its last event was the
	// one that was lost.
	orphan := sweepBuildKey(sweepActorC)
	adpt := &listingAdapter{keys: []string{orphan}}
	p := newSweepPipeline(t, adpt, 10)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.Empty(t, anchors)

	got := p.Sweeper().candidates(context.Background(), anchors, targets)
	require.Equal(t, []string{sweepActorC}, got)
}

func TestSweepCandidates_ForeignKeysInASharedBucketAreNotClaimed(t *testing.T) {
	// capability-kv is shared by every auth-plane lens. Claiming a sibling's
	// key would have this lens retract rows it does not own.
	adpt := &listingAdapter{keys: []string{
		"cap.identity.Tswp3CcccccccccccccZ",         // the primary lens's key
		"cap.role-by-operation.lattice.role.assign", // the operation-aggregate index
		"cap.roles.service.Tswp3CcccccccccccccZ",    // right prefix, wrong anchor type
		"cap.roles.identity.Tswp3CcccccccccccccZ.x", // right prefix, not a vertex key
	}}
	p := newSweepPipeline(t, adpt, 10)

	anchors, targets, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.Empty(t, p.Sweeper().candidates(context.Background(), anchors, targets))
}

func TestSweepCandidates_CursorWalksAndResumes(t *testing.T) {
	// The deep verify is bounded and round-robin: each tick continues where
	// the last left off, so a large cell re-verifies fully over many ticks
	// instead of re-checking the same head every minute.
	all := []string{sweepActorA, sweepActorB, sweepActorC}
	keys := make([]string, 0, len(all))
	for _, a := range all {
		keys = append(keys, sweepBuildKey(a))
	}
	adpt := &listingAdapter{keys: keys}
	p := newSweepPipeline(t, adpt, 1)
	for _, a := range all {
		writeAnchor(t, p, a, false)
	}
	sw := p.Sweeper()

	anchors, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Len(t, anchors, 3)

	seen := make([]string, 0, 3)
	for range all {
		got := sw.candidates(context.Background(), anchors, targets)
		require.Len(t, got, 1)
		seen = append(seen, got[0])
	}
	require.ElementsMatch(t, all, seen, "three bounded ticks must cover every anchor exactly once")

	// A fourth tick wraps rather than stalling at the end of the list.
	got := sw.candidates(context.Background(), anchors, targets)
	require.Equal(t, anchors[0], got[0])
}

func TestSweepCandidates_DeepVerifyKeepsAReservedSliceOfEveryBatch(t *testing.T) {
	// A prefilter candidate that recurs indefinitely — a heal that keeps
	// erroring, a soft-delete key still listed after retraction — must not be
	// able to refill the whole batch every tick. If it could, the round-robin
	// walk would stop advancing and the only detector for a stale-but-present
	// row would be silently disabled.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	// Twenty live anchors, none projected: every one is a definite divergence,
	// which is exactly the case that could crowd the walk out.
	// The Contract #1 NanoID alphabet excludes I, l, O and 0, so the varying
	// segment is drawn from a fixed safe run rather than generated arithmetically.
	const idChars = "abcdefghijkmnpqrstu"
	anchors := make([]string, 0, len(idChars))
	for i := range idChars {
		actor := "vtx.identity.Tstv" + string(idChars[i]) + "aaaaaaaaaaaaaaa"
		writeAnchor(t, p, actor, false)
		anchors = append(anchors, actor)
	}
	sw := p.Sweeper()
	surveyed, targets, err := sw.survey(context.Background())
	require.NoError(t, err)
	require.Len(t, surveyed, len(anchors))

	got := sw.candidates(context.Background(), surveyed, targets)
	require.LessOrEqual(t, len(got), 10)
	require.NotEmpty(t, sw.Status().Cursor,
		"the deep verify must still reach its reserved slots and advance the cursor")

	first := sw.Status().Cursor
	sw.candidates(context.Background(), surveyed, targets)
	require.NotEqual(t, first, sw.Status().Cursor,
		"the cursor must keep advancing tick over tick under a saturated prefilter")
}

func TestSweepCandidates_CursorSurvivesAnchorRemoval(t *testing.T) {
	// The cursor is a key, not an index: the anchor it names can disappear
	// between ticks, and the walk must resume at the next key rather than
	// restart or stall.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 1)
	sw := p.Sweeper()
	sw.mu.Lock()
	sw.status.Cursor = sweepActorB
	sw.mu.Unlock()

	anchors := []string{sweepActorA, sweepActorC} // B has been deleted
	got := sw.candidates(context.Background(), anchors, map[string]struct{}{})
	require.Equal(t, []string{sweepActorC}, got)
}

func TestSweepPass_SuppressedWhileRebuilding(t *testing.T) {
	// A rebuild is a superset of the sweep (truncate + full rescan); running
	// both at once would have reconciliation writes race a replay.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)
	p.rebuildInFlight.Store(true)

	p.Sweeper().pass(context.Background())
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)
	require.Zero(t, p.Sweeper().Status().Reconciled)
}

func TestSweepPass_ConvergedWorldWritesNothing(t *testing.T) {
	// The zero-write steady state: with no anchors and no target keys there is
	// nothing to reconcile, and a sweep must cost reads only.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)

	p.Sweeper().pass(context.Background())
	require.Empty(t, adpt.upserts)
	require.Empty(t, adpt.deletes)

	st := p.Sweeper().Status()
	require.Zero(t, st.Reconciled)
	require.Zero(t, st.DivergentStreak)
}

func TestSweepPass_HealsAnOrphanRowAndCountsIt(t *testing.T) {
	// End of the over-grant direction: the row is present, its anchor is gone,
	// so reconciliation retracts it and the heal is counted loudly.
	orphan := sweepBuildKey(sweepActorC)
	adpt := &listingAdapter{keys: []string{orphan}}
	adpt.present = true
	adpt.stored = map[string]any{"key": orphan}
	p := newSweepPipeline(t, adpt, 10)
	p.recordAppliedSeq(910)

	p.Sweeper().pass(context.Background())

	require.Len(t, adpt.deletes, 1)
	require.Equal(t, uint64(910), adpt.deletes[0].seq,
		"a reconciliation write carries the captured last-applied sequence, never MaxInt64")
	st := p.Sweeper().Status()
	require.Equal(t, uint64(1), st.Reconciled)
	require.Equal(t, 1, st.DivergentStreak)
}

func TestSweepRecord_StreakEscalatesAndClears(t *testing.T) {
	// The escalation input for CapabilityCoverageDivergence: one divergent
	// pass is a repaired incident, two in a row means events are still being
	// lost, and a clean pass clears the alert.
	p := newSweepPipeline(t, &listingAdapter{}, 10)
	sw := p.Sweeper()
	ctx := context.Background()

	sw.record(ctx, 2)
	require.Equal(t, 1, sw.Status().DivergentStreak)
	require.Equal(t, uint64(2), sw.Status().Reconciled)

	sw.record(ctx, 1)
	require.Equal(t, 2, sw.Status().DivergentStreak)
	require.Equal(t, uint64(3), sw.Status().Reconciled)

	sw.record(ctx, 0)
	require.Zero(t, sw.Status().DivergentStreak)
	require.Equal(t, uint64(3), sw.Status().Reconciled,
		"the cumulative heal count is not reset by a clean pass")
}

func TestSweepSurvey_SkipsAspectKeysUnderTheAnchorPrefix(t *testing.T) {
	// The anchor listing is by key prefix, which also matches every aspect of
	// every anchor. Only the three-segment vertex root is an anchor.
	adpt := &listingAdapter{}
	p := newSweepPipeline(t, adpt, 10)
	writeAnchor(t, p, sweepActorA, false)
	_, err := p.coreKV.Put(context.Background(), sweepActorA+".demographics", []byte(`{"key":"x"}`))
	require.NoError(t, err)

	anchors, _, err := p.Sweeper().survey(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{sweepActorA}, anchors)
}
