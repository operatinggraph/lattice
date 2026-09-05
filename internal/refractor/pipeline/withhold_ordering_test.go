package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The ordering pins (T3) run the interleavings §5's rows 7, 7r and 7s describe
// on the REAL seam, not a model of it: a guarded NatsKVAdapter over an embedded
// NATS bucket, an evaluation whose entry set comes from a real adjacency index,
// and the two callers ordered by channels rather than by luck. Withholding's
// safety argument is a claim about what the stored watermark carries, and that
// claim lives ACROSS the pipeline/adapter boundary — green on each side alone
// proves nothing about it.

const (
	orderingActor = "vtx.identity.TorderAaaaaaaaaaaaaa"
	orderingRole  = "vtx.role.TorderRoeAaaaaaaaaaa"
	orderingEntry = "child.TorderRoeAaaaaaaaaaa"
)

// orderingEntryFn turns the walk's collected role keys into one entry each —
// the perEntry shape, with the entry set genuinely derived from the adjacency
// index rather than handed to the envelope by the fixture.
func orderingEntryFn(row, _, _ map[string]any) ([]Envelope, error) {
	ids, _ := row["ids"].([]any)
	out := make([]Envelope, 0, len(ids))
	for _, raw := range ids {
		roleKey, _ := raw.(string)
		_, roleID, ok := substrate.ParseVertexKey(roleKey)
		if !ok {
			continue
		}
		key := "child." + roleID
		out = append(out, Envelope{
			Keys: map[string]any{"key": key},
			Row:  map[string]any{"key": key, "role": roleKey},
		})
	}
	return out, nil
}

// newOrderingFixture builds the perEntry pipeline the ordering pins run
// against: a real Core KV holding the actor and the role it may hold, a real
// adjacency bucket the walk reads its edges from, and a guarded, soft-delete
// NATS-KV target. The edge is deliberately NOT built — each case decides when
// the index learns about it, which is the whole subject of these tests.
func newOrderingFixture(t *testing.T, ruleID string) (*Pipeline, *recordingEntryAdapter, *substrate.KV) {
	t.Helper()
	kvs := newTestKVs(t, "CORE", "ADJ", "TARGET")
	coreKV, adjKV, targetKV := kvs[0], kvs[1], kvs[2]
	writeCollisionVertex(t, coreKV, orderingActor, "identity", map[string]any{})
	writeCollisionVertex(t, coreKV, orderingRole, "role", map[string]any{})

	inner, err := adapter.New(targetKV, []string{"key"}, adapter.DeleteModeSoft)
	require.NoError(t, err)
	inner.SetGuarded(true)
	adpt := &recordingEntryAdapter{NatsKVAdapter: inner}

	eng := full.New()
	cr, err := eng.Parse(
		`MATCH (i:identity {key: $actorKey})-[:holdsRole]->(r:role) ` +
			`RETURN i.key AS actorKey, collect(DISTINCT r.key) AS ids`)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"actorKey"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	p := &Pipeline{
		ruleID:          ruleID,
		coreKV:          coreKV,
		adjKV:           adjKV,
		adpt:            adpt,
		actorDeleteKey:  func(string) string { return "child" },
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "identity"),
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          fullCR,
		multiEnvelopeFn: orderingEntryFn,
	}
	// The index is current with the lens unless a case says otherwise — the
	// retraction arm's own precondition, which these pins are not about.
	p.SetAdjacencyAppliedFn(func() uint64 { return 1 << 40 })
	return p, adpt, targetKV
}

// grantTheRole makes the actor's role edge visible to the walk.
func grantTheRole(t *testing.T, p *Pipeline) {
	t.Helper()
	_, actorID, ok := substrate.ParseVertexKey(orderingActor)
	require.True(t, ok)
	_, roleID, ok := substrate.ParseVertexKey(orderingRole)
	require.True(t, ok)
	buildCollisionEdge(t, p.adjKV, "holdsRole", "identity", actorID, "role", roleID)
}

// runCDCEvent drives one costed evaluation and its write loop — the production
// CDC path — at the given stream sequence, and reports how many entries it
// withheld.
func runCDCEvent(t *testing.T, p *Pipeline, seq uint64) (results []ruleengine.EvalResult) {
	t.Helper()
	ctx := context.Background()
	results, err := p.executeFullForActor(ctx, p.ruleState(), orderingActor,
		map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}, "")
	require.NoError(t, err)
	decision, err := p.writeResults(ctx, p.ruleState(), substrate.Message{Sequence: seq}, orderingActor, results, nil, ScopeAll())
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, decision)
	return results
}

// parkTheFirstPrefixListing holds the first evaluation that reaches the prefix
// diff — after its engine walk, before any write it derives — and hands back
// the two channels that order the interleaving. Only the FIRST caller parks, so
// the pass the test runs while the other is held proceeds normally.
func parkTheFirstPrefixListing(adpt *recordingEntryAdapter) (reached <-chan struct{}, release chan<- struct{}) {
	reachedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	// A CAS rather than a sync.Once: Once makes every later caller WAIT for
	// the first Do to return, so the pass this test runs while the first is
	// parked would queue behind it and the interleaving could never happen.
	var parked atomic.Bool
	adpt.mu.Lock()
	adpt.onListKeysPrefix = func() {
		if parked.CompareAndSwap(false, true) {
			close(reachedCh)
			<-releaseCh
		}
	}
	adpt.mu.Unlock()
	return reachedCh, releaseCh
}

func entryIsLive(t *testing.T, adpt *recordingEntryAdapter) bool {
	t.Helper()
	_, live, err := adpt.GetRow(context.Background(), map[string]any{"key": orderingEntry})
	require.NoError(t, err)
	return live
}

// TestOrdering_AStaleReprojectTombstoneIsDeclinedByTheGuard is T3(a) — §5 row
// 7, the ordering axis, and the pin the whole design rests on.
//
// The interleaving, in production order: a Reproject captures its token BEFORE
// the entry is created, its walk reads the actor holding no role (the edge is
// not in the index yet), and it is then held at the prefix diff. While it is
// held, a CDC event at a HIGHER sequence creates the entry — over a tombstone,
// so the create is §5 row 4, the presence change that must never be withheld.
// The Reproject then resumes, finds the entry it never evaluated, and issues a
// tombstone carrying its own older token.
//
// The guard declines it, because the stored watermark is the sequence of the
// write that last changed the entry's PRESENCE, and that write is newer. The
// entry stays live.
//
// MUTATION (revert-prove this one): make the presence change withholdable —
// mark §5 row 4, a tombstoned stored entry, as Unchanged — and the CDC create
// never lands, so the stale tombstone meets a watermark below its own token and
// the entry ends the interleaving retracted. This test reds.
func TestOrdering_AStaleReprojectTombstoneIsDeclinedByTheGuard(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newOrderingFixture(t, "ordering-row7")

	// The entry has lived and been retracted before: the CDC create below is a
	// resurrection over a tombstone, which is the presence change §5 row 4
	// requires to be written.
	require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
		map[string]any{"key": orderingEntry, "role": orderingRole}, 1))
	require.NoError(t, adpt.NatsKVAdapter.Delete(ctx, map[string]any{"key": orderingEntry}, 2))

	// The reconciliation's token: captured now, before the create.
	p.recordAppliedSeq(3)

	reached, release := parkTheFirstPrefixListing(adpt)
	type reprojection struct {
		res Reprojection
		err error
	}
	done := make(chan reprojection, 1)
	go func() {
		res, err := p.Reproject(ctx, orderingActor)
		done <- reprojection{res, err}
	}()

	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the reconciliation never reached the prefix diff")
	}

	// The role is granted and the CDC event lands the create at a sequence
	// above the reconciliation's token.
	grantTheRole(t, p)
	p.recordAppliedSeq(5)
	results := runCDCEvent(t, p, 5)
	require.Len(t, results, 1)
	require.False(t, results[0].Unchanged, "a tombstoned stored entry is a presence change and is always written")
	require.True(t, entryIsLive(t, adpt), "the CDC create landed")

	close(release)
	got := <-done
	require.NoError(t, got.err)

	require.True(t, entryIsLive(t, adpt),
		"a reconciliation tombstone carrying a token below the stored watermark must be declined")
	require.Equal(t, VerdictBlocked, got.res.Verdict)
	require.Equal(t, BlockedRetraction, got.res.BlockedClass,
		"the guard declined it against a fresher stored watermark")
	require.False(t, got.res.Deleted)
}

// TestOrdering_AStaleEntryLosesToAFreshReprojectTombstone is T3(b) — §5 row 7r,
// the residual this design accepts and states.
//
// Here the stored entry is STALE: its watermark is old because a retraction the
// fan-out lost never re-stamped it. A CDC event withholds (the body matches),
// so nothing moves the watermark, and a reconciliation whose token is newer
// then lands its tombstone. The entry really is retracted for a while — an
// under-grant, the deny direction — and the sweep's next pass, running against
// a repaired index, rewrites it.
//
// The pin is the trade itself: both faces asserted, so the residual is a number
// rather than a surprise.
func TestOrdering_AStaleEntryLosesToAFreshReprojectTombstone(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newOrderingFixture(t, "ordering-row7r")

	// The entry is live at an OLD watermark, holding exactly the body a fresh
	// evaluation produces — so a CDC event withholds and the watermark stays
	// where it is.
	require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
		map[string]any{"key": orderingEntry, "role": orderingRole}, 1))

	p.recordAppliedSeq(4)
	reached, release := parkTheFirstPrefixListing(adpt)
	done := make(chan Reprojection, 1)
	go func() {
		res, err := p.Reproject(ctx, orderingActor)
		require.NoError(t, err)
		done <- res
	}()

	select {
	case <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("the reconciliation never reached the prefix diff")
	}

	grantTheRole(t, p)
	results := runCDCEvent(t, p, 5)
	require.Len(t, results, 1)
	require.True(t, results[0].Unchanged, "the stored body already equals the fresh one, so the write is withheld")
	require.True(t, entryIsLive(t, adpt))

	close(release)
	res := <-done
	require.True(t, res.Deleted, "the reconciliation's token outranks the stale stored watermark")
	require.False(t, entryIsLive(t, adpt),
		"§5 row 7r: a stale entry is retracted by a fresher reconciliation — the design's stated residual")

	// The other face: the very next reconciliation, now walking an index that
	// holds the edge, puts the entry back.
	p.recordAppliedSeq(9)
	healed, err := p.Reproject(ctx, orderingActor)
	require.NoError(t, err)
	require.True(t, healed.Wrote)
	require.True(t, entryIsLive(t, adpt), "the sweep's next pass heals it")
}

// TestOrdering_AWronglyRemovedEdgeRetractsAndTheNextEventHeals is T3(c) — §5
// row 7s's S-wrong half, which this design inherits rather than introduces.
//
// The adjacency index has lost an edge Core KV still holds (a redelivered older
// tombstone of a reused EdgeID — the filed index bug), and its cursor is at the
// head, so the index-behind refusal has nothing to catch: the view is not late,
// it is wrong. The reconciliation reads the actor holding no role and its
// tombstone lands.
//
// That is today's behaviour, not this design's: a CDC event evaluating under
// the same wrong index writes the same tombstone. The heal is the same one it
// has always been — the next event on the actor, once the index is repaired.
func TestOrdering_AWronglyRemovedEdgeRetractsAndTheNextEventHeals(t *testing.T) {
	ctx := context.Background()
	p, adpt, _ := newOrderingFixture(t, "ordering-row7s")

	require.NoError(t, adpt.NatsKVAdapter.Upsert(ctx, map[string]any{"key": orderingEntry},
		map[string]any{"key": orderingEntry, "role": orderingRole}, 1))
	require.True(t, entryIsLive(t, adpt))

	// The index is caught up to the head — the cursor cannot distinguish a
	// wrong view from a right one, which is exactly why S-wrong is filed as
	// its own defect rather than absorbed here.
	p.recordAppliedSeq(4)

	res, err := p.Reproject(ctx, orderingActor)
	require.NoError(t, err)
	require.True(t, res.Deleted)
	require.False(t, entryIsLive(t, adpt),
		"a wrongly-removed edge retracts a live grant — the pre-existing index defect, unchanged by withholding")

	// The index is repaired and the next event on the actor rewrites the entry.
	grantTheRole(t, p)
	results := runCDCEvent(t, p, 7)
	require.Len(t, results, 1)
	require.False(t, results[0].Unchanged, "a tombstoned stored entry is written, not withheld")
	require.True(t, entryIsLive(t, adpt), "the next CDC event on the actor heals it")
}
