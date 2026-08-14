package pipeline

// The plain-lens divergence audit (lens-projection-divergence-audit-design.md
// §4.3-§4.5): a background recompute-and-compare that gives a plain lens its
// first per-row correctness verdict and NEVER writes to the target.
//
// Every divergence test below asserts the target's revisions are unchanged
// afterwards, because "detect, don't fix" is the whole design and a revision is
// the only proof of it that no rewrite of any value can satisfy.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	auditUnitA = "AudtunitAAAAAAAAAAAA"
	auditUnitB = "AudtunitBBBBBBBBBBBB"
)

// newAuditKVs stands up the four buckets an audit test needs: the graph, its
// adjacency, a target that cannot collide with vtx.* input keys, and the health
// entry the cursor persists onto.
func newAuditKVs(t *testing.T) (coreKV, adjKV, targetKV, healthKV *substrate.KV) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	_, nc := natsfixture.Server(t)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	for _, bucket := range []string{"CORE", "ADJ", "TARGET", "HEALTH"} {
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		require.NoError(t, err)
	}
	coreKV, err = conn.OpenKV(ctx, "CORE")
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "ADJ")
	require.NoError(t, err)
	targetKV, err = conn.OpenKV(ctx, "TARGET")
	require.NoError(t, err)
	healthKV, err = conn.OpenKV(ctx, "HEALTH")
	require.NoError(t, err)
	return coreKV, adjKV, targetKV, healthKV
}

// auditFixture is one audited plain lens plus the handles a test needs to reach
// behind its back — into the graph it reads and the target it must never write.
type auditFixture struct {
	p        *Pipeline
	coreKV   *substrate.KV
	adjKV    *substrate.KV
	targetKV *substrate.KV
	healthKV *substrate.KV
	reporter *health.Reporter
}

// newAuditFixture builds a plain, full-engine, NATS-KV-targeted lens — the shape
// the audit enrols — with a health reporter wired so the cursor genuinely
// round-trips through Health KV rather than through an in-memory shortcut.
//
// wrap, when non-nil, replaces the adapter the pipeline is built with, so a test
// can pose a target whose read-back fails.
func newAuditFixture(t *testing.T, spec string, wrap func(adapter.Adapter) adapter.Adapter) *auditFixture {
	t.Helper()
	coreKV, adjKV, targetKV, healthKV := newAuditKVs(t)

	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	var adpt adapter.Adapter
	adpt, err = adapter.New(targetKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	if wrap != nil {
		adpt = wrap(adpt)
	}
	reporter := health.New(healthKV, "audit-rule")
	p, err := New("audit-rule", "nats_kv", "CORE", adjKV, coreKV, adpt, reporter)
	require.NoError(t, err)
	require.NoError(t, p.UseFullEngine(eng, cr))
	return &auditFixture{p: p, coreKV: coreKV, adjKV: adjKV, targetKV: targetKV, healthKV: healthKV, reporter: reporter}
}

// installAudit arms the auditor through the production enrolment path, then
// narrows its batch so a test can drive cycles by hand. Going through
// InstallAudit rather than SetAuditPlan alone is deliberate: it makes every
// divergence test below depend on the enrolment actually granting a plan.
func (f *auditFixture) installAudit(t *testing.T, batch int) *Auditor {
	t.Helper()
	enrolled, refusal := f.p.InstallAudit(AuditOptions{})
	require.True(t, enrolled, "the fixture lens must enrol; refusal: %s", refusal)
	require.Equal(t, "unit", f.p.Auditor().AnchorLabel())
	f.p.SetAuditPlan(AuditPlan{AnchorLabel: "unit", Batch: batch, Interval: time.Hour})
	return f.p.Auditor()
}

// project seeds one unit anchor with a listing aspect and drives its vertex
// event, so the target row is written by the lens's OWN write path — the audit
// must compare against what the pipeline really produces, never against a row a
// test hand-assembled to match.
func (f *auditFixture) project(t *testing.T, id, name string, seq uint64) string {
	t.Helper()
	key := "vtx.unit." + id
	body := seedVertexBody(t, f.coreKV, key, "unit", map[string]any{"name": name})
	putBody(t, f.coreKV, key+".listing", aspectBody(key, "listing", map[string]any{"status": "active"}, false))
	handleVertexEvent(t, f.p, key, body, seq)
	return key
}

// revisions snapshots the target's revision for each key, so a test can prove
// the audit wrote NOTHING. A revision moves on an identical-value rewrite, which
// is exactly why it is the assertion rather than the row content.
func (f *auditFixture) revisions(t *testing.T, keys ...string) map[string]uint64 {
	t.Helper()
	out := make(map[string]uint64, len(keys))
	for _, k := range keys {
		entry, err := f.targetKV.Get(context.Background(), k)
		if err != nil {
			out[k] = 0
			continue
		}
		out[k] = entry.Revision
	}
	return out
}

// corruptStoredRow rewrites a projected row behind the pipeline's back — the
// hand-corruption the `stale` class exists to catch.
func corruptStoredRow(t *testing.T, targetKV *substrate.KV, key string) {
	t.Helper()
	ctx := context.Background()
	entry, err := targetKV.Get(ctx, key)
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	row["name"] = "corrupted by something that is not this lens"
	raw, err := json.Marshal(row)
	require.NoError(t, err)
	_, err = targetKV.Put(ctx, key, raw)
	require.NoError(t, err)
}

// TestAudit_CleanCorpusIsCleanTwice is the determinism pin §2 rests on: a plain
// lens's projected row is byte-stable for an unchanged graph, so a recompute of
// an unchanged anchor must compare equal — every pass, not just the first.
//
// It is also the positive vector for every divergence test below. Without it a
// green "divergent" result could equally come from a comparison that reports
// divergence on everything.
func TestAudit_CleanCorpusIsCleanTwice(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()

	keyA := f.project(t, auditUnitA, "Loft A", 1)
	keyB := f.project(t, auditUnitB, "Loft B", 2)
	before := f.revisions(t, keyA, keyB)

	for pass := 1; pass <= 2; pass++ {
		a.pass(ctx)
		st := a.Status()
		require.Equal(t, 2, st.Audited, "pass %d", pass)
		require.Zero(t, st.DivergentTotal, "pass %d: an unchanged graph must recompute to the stored row", pass)
		require.Empty(t, st.Divergent, "a class that never fires must read as ABSENT, not as zero")
		require.Zero(t, st.Unverified, "pass %d", pass)
		require.Equal(t, AuditCoverageBasisKeyType, st.CoverageBasis)
		require.Equal(t, 2, st.ListingSize)
		require.False(t, st.CycleCompletedAt.IsZero(), "a batch wider than the corpus completes a cycle every pass")
		require.Empty(t, st.Cursor, "a completed cycle resets the cursor")
	}
	require.Equal(t, before, f.revisions(t, keyA, keyB), "the audit must not write to the target")
}

// TestAudit_DetectsEveryDivergenceClassWithoutWriting walks the three classes
// plus the tombstoned-anchor case, and asserts after each that the target is
// exactly as wrong as it was. That last assertion is the one that pins "detect,
// don't fix" — §8.1 rejected repair on an unguarded, shared plain target, and a
// mechanism that quietly grew one back would pass every other assertion here.
func TestAudit_DetectsEveryDivergenceClassWithoutWriting(t *testing.T) {
	ctx := context.Background()

	t.Run("stale: the stored row's content no longer matches", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)
		keyA := f.project(t, auditUnitA, "Loft A", 1)
		f.project(t, auditUnitB, "Loft B", 2)

		corruptStoredRow(t, f.targetKV, keyA)
		before := f.revisions(t, keyA)

		a.pass(ctx)
		st := a.Status()
		require.Equal(t, map[string]int{AuditClassStale: 1}, st.Divergent)
		require.Equal(t, 1, st.DivergentTotal)
		require.Equal(t, 2, st.Audited, "a divergent anchor is still an audited one")
		require.Zero(t, st.Unverified)

		require.Equal(t, before, f.revisions(t, keyA), "the audit must not repair what it finds")
		require.Equal(t, "corrupted by something that is not this lens",
			targetRow(t, f.targetKV, keyA)["name"], "the row must still be wrong afterwards")
	})

	t.Run("missing: the row the recomputation produces is not there", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)
		keyA := f.project(t, auditUnitA, "Loft A", 1)
		require.NoError(t, f.targetKV.Delete(ctx, keyA))

		a.pass(ctx)
		st := a.Status()
		require.Equal(t, map[string]int{AuditClassMissing: 1}, st.Divergent)
		require.Equal(t, 1, st.DivergentTotal)

		_, err := f.targetKV.Get(ctx, keyA)
		require.Error(t, err, "the audit must not re-create the row it found missing")
	})

	t.Run("retained: the anchor stopped matching and its row stayed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)
		keyA := f.project(t, auditUnitA, "Loft A", 1)

		// Tombstone the aspect the filtering WHERE reads, WITHOUT driving the
		// CDC event that would retract the row — the lost-retraction shape.
		putBody(t, f.coreKV, keyA+".listing", aspectBody(keyA, "listing", map[string]any{"status": "active"}, true))
		before := f.revisions(t, keyA)

		a.pass(ctx)
		st := a.Status()
		require.Equal(t, map[string]int{AuditClassRetained: 1}, st.Divergent)
		require.Equal(t, 1, st.DivergentTotal)
		require.Equal(t, 1, st.Audited)

		require.Equal(t, before, f.revisions(t, keyA), "the audit must not retract what it finds retained")
	})

	t.Run("retained: the anchor was tombstoned and its row stayed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)
		keyA := f.project(t, auditUnitA, "Loft A", 1)

		// Soft-tombstone the anchor vertex itself, again with no CDC event: the
		// row key is still derivable read-free from the stored body, which is
		// what lets the audit ask whether the lost Delete's row is still there.
		putBody(t, f.coreKV, keyA, map[string]any{
			"key": keyA, "class": "unit", "isDeleted": true, "name": "Loft A",
			"createdAt": "2026-08-01T10:00:00Z", "lastModifiedAt": "2026-08-01T10:00:00Z",
			"data": map[string]any{},
		})
		before := f.revisions(t, keyA)

		a.pass(ctx)
		st := a.Status()
		require.Equal(t, map[string]int{AuditClassRetained: 1}, st.Divergent)
		require.Equal(t, 1, st.Audited)
		require.Zero(t, st.Unverified, "a tombstone whose key IS derivable is a verdict, not an unknown")

		require.Equal(t, before, f.revisions(t, keyA), "the audit must not retract a tombstoned anchor's row")
	})
}

// TestAudit_SourceMutationBehindTheLensReadsDivergent is the second half of the
// determinism pin (§7): the comparison must discriminate, not merely agree. A
// source aspect changed with no CDC event is precisely the incidental-recompute
// gap the D1/D2 narrowing widened, and the class the audit exists to see.
func TestAudit_SourceMutationBehindTheLensReadsDivergent(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()

	keyA := f.project(t, auditUnitA, "Loft A", 1)
	a.pass(ctx)
	require.Zero(t, a.Status().DivergentTotal)

	putBody(t, f.coreKV, keyA+".listing", aspectBody(keyA, "listing", map[string]any{"status": "delisted"}, false))
	before := f.revisions(t, keyA)

	a.pass(ctx)
	st := a.Status()
	require.Equal(t, map[string]int{AuditClassStale: 1}, st.Divergent)
	require.Equal(t, before, f.revisions(t, keyA), "the audit must not refresh the row it found stale")
}

// failingRowReader is a target that cannot read a row back this pass. It
// overrides GetRow on the concrete type rather than relying on the embedded
// adapter's promotion, which would hand the audit the real read straight
// through (the embedding trap adapter.OutcomeUpserter's doc records).
type failingRowReader struct {
	adapter.Adapter
	err error
}

func (f failingRowReader) GetRow(context.Context, map[string]any) (map[string]any, bool, error) {
	return nil, false, f.err
}

// TestAudit_UnverifiedIsNeitherCleanNorDivergent pins the third outcome. An
// anchor the audit could not check must be counted apart from both — folding it
// into "clean" is the collapse toward health this whole design exists to end,
// and folding it into "divergent" would alarm on a fault in the OBSERVATION.
func TestAudit_UnverifiedIsNeitherCleanNorDivergent(t *testing.T) {
	ctx := context.Background()

	t.Run("a target that cannot be read back", func(t *testing.T) {
		// Only GetRow fails: the write path is the real adapter's, so the corpus
		// below is genuinely converged and any non-clean verdict comes from the
		// read fault alone rather than from a lens that never projected.
		f := newAuditFixture(t, seedUnitsSpec, func(a adapter.Adapter) adapter.Adapter {
			return failingRowReader{Adapter: a, err: context.DeadlineExceeded}
		})
		f.project(t, auditUnitA, "Loft A", 1)
		a := f.installAudit(t, 10)

		a.pass(ctx)
		st := a.Status()
		require.Equal(t, 1, st.Unverified)
		require.Zero(t, st.Audited, "an unverified anchor is not an audited one")
		require.Zero(t, st.DivergentTotal, "an unverified anchor is not a divergent one")
		require.Empty(t, st.Divergent)
		require.Contains(t, st.LastUnverified, "target row read failed")
	})

	t.Run("an anchor body that cannot be parsed", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)
		keyA := f.project(t, auditUnitA, "Loft A", 1)
		_, err := f.coreKV.Put(ctx, keyA, []byte("{this is not a vertex body"))
		require.NoError(t, err)

		a.pass(ctx)
		st := a.Status()
		require.Equal(t, 1, st.Unverified)
		require.Zero(t, st.Audited)
		require.Zero(t, st.DivergentTotal)
	})
}

// TestAudit_CursorWalksAndCycleCompletes pins the coverage machinery: a corpus
// three batches wide takes exactly three passes to close a cycle, and the cursor
// resets when it does. Without this the audit could publish `divergentTotal: 0`
// forever off the same ten anchors while never reaching the rest.
func TestAudit_CursorWalksAndCycleCompletes(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	const batch = 2
	a := f.installAudit(t, batch)
	ctx := context.Background()

	ids := []string{
		"AudtturnAAAAAAAAAAA1", "AudtturnAAAAAAAAAAA2", "AudtturnAAAAAAAAAAA3",
		"AudtturnAAAAAAAAAAA4", "AudtturnAAAAAAAAAAA5", "AudtturnAAAAAAAAAAA6",
	}
	for i, id := range ids {
		f.project(t, id, "Loft "+id, uint64(i+1))
	}

	// Two partial passes: each covers a batch, neither may claim a cycle.
	for pass := 1; pass <= 2; pass++ {
		a.pass(ctx)
		st := a.Status()
		require.Equal(t, batch, st.Audited, "pass %d", pass)
		require.Equal(t, len(ids), st.ListingSize, "the published listing size is the whole anchor type, not the page")
		require.NotEmpty(t, st.Cursor, "pass %d must leave the walk mid-cycle", pass)
		require.True(t, st.CycleCompletedAt.IsZero(), "pass %d has not covered the lens", pass)
	}

	a.pass(ctx)
	st := a.Status()
	require.Equal(t, batch, st.Audited)
	require.Empty(t, st.Cursor, "the third pass reaches the end and resets")
	require.False(t, st.CycleCompletedAt.IsZero(), "only a completed walk earns the coverage claim")
}

// TestAudit_RestartResumesAtThePersistedCursor is the reason the cursor lives on
// the health entry at all. A cell that redeploys more often than a cycle
// completes would otherwise re-walk the head forever — auditing the same first
// batch, never reaching the tail, and publishing a clean verdict the whole time.
func TestAudit_RestartResumesAtThePersistedCursor(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 2)
	ctx := context.Background()

	ids := []string{
		"AudtresumAAAAAAAAAA1", "AudtresumAAAAAAAAAA2", "AudtresumAAAAAAAAAA3",
		"AudtresumAAAAAAAAAA4",
	}
	for i, id := range ids {
		f.project(t, id, "Loft "+id, uint64(i+1))
	}

	a.pass(ctx)
	mid := a.Status().Cursor
	require.NotEmpty(t, mid)

	// The persisted half: the cursor reached Health KV, not just memory.
	entry, err := f.reporter.GetStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, mid, entry.AuditCursor)

	// A restart: a brand-new auditor over the same pipeline, restoring rather
	// than walking from the head.
	f.p.SetAuditPlan(AuditPlan{AnchorLabel: "unit", Batch: 2, Interval: time.Hour})
	restarted := f.p.Auditor()
	require.Empty(t, restarted.Status().Cursor, "a fresh auditor starts at the head...")
	restarted.restore(ctx)
	require.Equal(t, mid, restarted.Status().Cursor, "...and restore is what moves it back to the walk")

	restarted.pass(ctx)
	st := restarted.Status()
	require.Equal(t, 2, st.Audited)
	require.Empty(t, st.Cursor, "resuming from the persisted cursor reaches the end of a 4-anchor corpus")
	require.False(t, st.CycleCompletedAt.IsZero())
}

// TestAudit_EnrolmentIsRecheckedEveryPass pins §9.1 finding 3: the conjuncts are
// read off MUTABLE pipeline fields, so a plan cached at install would keep
// auditing under a shape the lens no longer has. The auditor must self-suppress
// with the reason instead.
func TestAudit_EnrolmentIsRecheckedEveryPass(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()
	f.project(t, auditUnitA, "Loft A", 1)

	a.pass(ctx)
	require.Equal(t, 1, a.Status().Audited)
	require.Empty(t, a.Status().Suppression)

	// Diff retraction arrives on the live pipeline. Its semantics — the target's
	// full live key set against a full re-execute — are exactly what a
	// single-anchor evaluation would misread, so the next pass must hold.
	require.NoError(t, f.p.SetDiffRetraction(true))

	a.pass(ctx)
	st := a.Status()
	require.Contains(t, st.Suppression, "target-diff retraction")
	require.Contains(t, st.Suppression, "enrolment no longer holds")
	require.False(t, st.SuppressionAt.IsZero(), "the reason needs a clock or a wedged audit reads as a suppressed one")
	require.Equal(t, 1, st.Audited, "a suppressed tick must not republish a verdict as if it re-derived one")
}

// TestAudit_SuppressedWhilePausedOrRebuilding mirrors the sweep's own posture: a
// rebuild is a superset of the audit, and an operator pause is intent the audit
// must not quietly speak over.
func TestAudit_SuppressedWhilePausedOrRebuilding(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()
	f.project(t, auditUnitA, "Loft A", 1)

	require.NoError(t, f.reporter.SetPaused(ctx, "operator", "held for investigation"))
	a.pass(ctx)
	require.Contains(t, a.Status().Suppression, "lens status is paused")
	require.Zero(t, a.Status().Audited, "a suppressed tick verifies nothing")
	require.True(t, a.Status().LastPassAt.IsZero(),
		"a suppressed tick must leave the liveness clock ageing — that ageing is the only thing that "+
			"distinguishes an audit held indefinitely from one that is clean and quiet")

	require.NoError(t, f.reporter.SetActive(ctx))
	a.pass(ctx)
	require.Empty(t, a.Status().Suppression)
	require.Equal(t, 1, a.Status().Audited)
}

// TestAudit_UnderCoverageIsPublishedNotAssumedAway pins §9.1 finding 2. The
// executor admits a vertex whose BODY class equals the pattern label, but the
// audit enumerates by KEY TYPE — so such an anchor is never audited. The failure
// is under-coverage, never a wrong verdict, and the published coverage basis is
// what keeps "audited clean" readable as the bounded claim it is.
func TestAudit_UnderCoverageIsPublishedNotAssumedAway(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()

	f.project(t, auditUnitA, "Loft A", 1)

	// A body-bound anchor: class "unit", key type "dwelling". The lens's matcher
	// admits it; the anchor listing cannot see it.
	bodyBound := "vtx.dwelling.Audtbodybound1111111"
	putBody(t, f.coreKV, bodyBound, map[string]any{
		"key": bodyBound, "class": "unit", "isDeleted": false, "name": "Body-bound",
		"createdAt": "2026-08-01T10:00:00Z", "lastModifiedAt": "2026-08-01T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, f.coreKV, bodyBound+".listing",
		aspectBody(bodyBound, "listing", map[string]any{"status": "active"}, false))

	a.pass(ctx)
	st := a.Status()
	require.Equal(t, 1, st.Audited, "the body-bound anchor is not enumerated, so it is not audited")
	require.Equal(t, 1, st.ListingSize, "and it is not counted in the listing either")
	require.Equal(t, AuditCoverageBasisKeyType, st.CoverageBasis,
		"the bound must be PUBLISHED — a clean verdict over an unenumerable anchor is an over-claim otherwise")
}

// seedRoomsSpec is seedUnitsSpec's twin over a DIFFERENT anchor type — the
// shape a MATCH hot-reload can turn the fixture's lens into.
const seedRoomsSpec = `
MATCH (r:room)
WHERE r.listing.data.status <> null
RETURN r.key AS key, r.name AS name, r.listing.data.status AS status
`

// TestAudit_AnchorLabelChangeIsAdoptedNotSuppressedForever pins the recovery a
// frozen install-time plan cannot make. InstallAudit runs ONCE, at activation;
// nothing re-invokes it. So an auditor that merely compared each pass against
// its installed label would self-suppress from the first hot-reload onward,
// publish a stale reason forever, and eventually raise an LensAuditStalled that
// only a process restart could clear.
func TestAudit_AnchorLabelChangeIsAdoptedNotSuppressedForever(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()

	f.project(t, auditUnitA, "Loft A", 1)
	a.pass(ctx)
	require.Equal(t, 1, a.Status().Audited)
	require.Equal(t, "unit", a.AnchorLabel())

	// A MATCH hot-reload moves the lens's anchor to another type.
	eng := full.New()
	cr, err := eng.Parse(seedRoomsSpec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, f.p.UseFullEngine(eng, cr))

	roomKey := "vtx.room.AudtroomAAAAAAAAAAAA"
	putBody(t, f.coreKV, roomKey, map[string]any{
		"key": roomKey, "class": "room", "isDeleted": false, "name": "Room One",
		"createdAt": "2026-08-13T10:00:00Z", "lastModifiedAt": "2026-08-13T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, f.coreKV, roomKey+".listing",
		aspectBody(roomKey, "listing", map[string]any{"status": "active"}, false))

	a.pass(ctx)
	st := a.Status()
	require.Equal(t, "room", a.AnchorLabel(), "the auditor must adopt the lens's new anchor, not freeze against the old one")
	require.Empty(t, st.Suppression, "adopting a moved anchor is not a suppression — a suppression here never clears")
	require.Equal(t, 1, st.Audited, "and it must resume auditing under the new shape without a restart")
	require.Equal(t, 1, st.ListingSize)
}

// TestAudit_RuleSwapMidPassDiscardsTheVerdict mirrors the sweep's own
// ErrRuleSuperseded disposition. A pass takes real time, and a rule swap landing
// inside it makes every comparison in hand a comparison against a rule no longer
// in force. The sweep withholds its write; the audit, having none, withholds the
// VERDICT — publishing one would bank a finding derived from a retired rule.
func TestAudit_RuleSwapMidPassDiscardsTheVerdict(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()

	keyA := f.project(t, auditUnitA, "Loft A", 1)
	corruptStoredRow(t, f.targetKV, keyA)

	// The swap lands while the pass is in flight. Driving it from a hook inside
	// the target read is what makes the ordering deterministic — no sleep, no
	// racing goroutine.
	swapped := false
	f.p.adapterMu.Lock()
	f.p.adpt = hookedRowReader{Adapter: f.p.adpt, before: func() {
		if swapped {
			return
		}
		swapped = true
		eng := full.New()
		cr, perr := eng.Parse(seedUnitsSpec)
		require.NoError(t, perr)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.True(t, isFull)
		fullCR.KeyColumns = []string{"key"}
		require.NoError(t, f.p.UseFullEngine(eng, cr))
	}}
	f.p.adapterMu.Unlock()

	a.pass(ctx)
	st := a.Status()
	require.True(t, swapped, "the fixture must actually have swapped the rule mid-pass")
	require.Equal(t, "rule swapped mid-pass", st.Suppression)
	require.Zero(t, st.DivergentTotal, "a verdict derived from a retired rule must not be banked")
	require.True(t, st.LastPassAt.IsZero(), "and the pass must not claim the freshness of one that concluded")
}

// hookedRowReader runs before each read-back, so a test can land a concurrent
// mutation at an exact point INSIDE a pass without a sleep or a second
// goroutine. It overrides GetRow on the concrete type rather than relying on
// promotion through the embedded adapter.
type hookedRowReader struct {
	adapter.Adapter
	before func()
}

func (h hookedRowReader) GetRow(ctx context.Context, keys map[string]any) (map[string]any, bool, error) {
	h.before()
	reader, ok := h.Adapter.(adapter.RowReader)
	if !ok {
		return nil, false, nil
	}
	return reader.GetRow(ctx, keys)
}

// TestAudit_PauseStartingMidPassDiscardsTheVerdict is the other mid-pass
// condition, and the one with a concrete false finding attached: a rebuild or a
// pause starting mid-batch leaves the remaining anchors compared against a
// target being truncated underneath them, which reads as a run of `missing`
// divergences that were never divergent.
func TestAudit_PauseStartingMidPassDiscardsTheVerdict(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()

	keyA := f.project(t, auditUnitA, "Loft A", 1)
	require.NoError(t, f.targetKV.Delete(ctx, keyA))

	paused := false
	f.p.adapterMu.Lock()
	f.p.adpt = hookedRowReader{Adapter: f.p.adpt, before: func() {
		if paused {
			return
		}
		paused = true
		require.NoError(t, f.reporter.SetPaused(ctx, "operator", "held mid-pass"))
	}}
	f.p.adapterMu.Unlock()

	a.pass(ctx)
	st := a.Status()
	require.True(t, paused)
	require.Contains(t, st.Suppression, "started mid-pass")
	require.Contains(t, st.Suppression, "lens status is paused")
	require.Zero(t, st.DivergentTotal,
		"a row read while the target was being frozen underneath the pass is not a proven divergence")
}

// TestAudit_EmptyAnchorTypeEarnsNoCycleCompletion pins the honesty of the
// coverage stamp. A pass that reaches the end of an EMPTY listing has verified
// nothing, and stamping it would publish `divergentTotal: 0` beside a fresh
// completion timestamp — which reads as "the whole lens was audited and is
// clean". A lens with no enumerable anchors must stay visibly unsubstantiated.
func TestAudit_EmptyAnchorTypeEarnsNoCycleCompletion(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	a := f.installAudit(t, 10)
	ctx := context.Background()

	a.pass(ctx)
	st := a.Status()
	require.Zero(t, st.Audited)
	require.Zero(t, st.ListingSize)
	require.True(t, st.CycleCompletedAt.IsZero(),
		"a tick that compared nothing must not read as a completed cycle")
	require.Zero(t, st.CycleAudited)

	// And the positive vector: one real anchor, and the stamp is earned.
	f.project(t, auditUnitA, "Loft A", 1)
	a.pass(ctx)
	st = a.Status()
	require.False(t, st.CycleCompletedAt.IsZero())
	require.Equal(t, 1, st.CycleAudited, "the stamp travels with what the cycle actually compared")
}

// TestAudit_APassThatVerifiedNothingLeavesTheClockAgeing is the liveness clock's
// half of the same honesty the cycle stamp above keeps. A pass that reached the
// end of its loop having compared no anchor reached no verdict, and there are two
// ways to get there: an empty page, and a page on which EVERY anchor went
// unverified — which one target-read outage produces in a single pass.
//
// The second is the one with teeth. Nothing about the tick loop looks wrong: it
// runs, it enumerates, it publishes `divergentTotal: 0`. Stamping the clock on
// top of that would make a blind audit read exactly like a clean one, and
// plainDerivationLicence reads this very field (through Auditor.Stale) to decide
// whether a plain lens's narrowing write licence still holds — so the stamp would
// keep the licence live for the whole outage, which is the fail-open the
// staleness conjunct was added to close.
func TestAudit_APassThatVerifiedNothingLeavesTheClockAgeing(t *testing.T) {
	ctx := context.Background()

	t.Run("every anchor unverified", func(t *testing.T) {
		// Only GetRow fails, so the corpus below is genuinely converged: the
		// pass's verdict is nothing but the read fault.
		f := newAuditFixture(t, seedUnitsSpec, func(a adapter.Adapter) adapter.Adapter {
			return failingRowReader{Adapter: a, err: context.DeadlineExceeded}
		})
		f.project(t, auditUnitA, "Loft A", 1)
		a := f.installAudit(t, 10)

		a.pass(ctx)
		st := a.Status()
		require.Equal(t, 1, st.Unverified, "the pass really did try, and really did fail to conclude")
		require.Zero(t, st.Audited)
		require.Empty(t, st.Suppression, "nothing suppressed this tick — it ran, and that is the whole hazard")
		require.True(t, st.LastPassAt.IsZero(),
			"a pass that compared no anchor must leave the liveness clock ageing — that ageing is the only thing "+
				"that distinguishes an audit gone blind from one that is clean and quiet")
	})

	t.Run("an empty page", func(t *testing.T) {
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)

		a.pass(ctx)
		require.Zero(t, a.Status().Audited)
		require.True(t, a.Status().LastPassAt.IsZero(),
			"a lens with no enumerable anchor has proven nothing, so its clock has nothing to stamp")
	})

	t.Run("the healthy path still advances it", func(t *testing.T) {
		// The positive vector, and the regression the guard must not cause: a pass
		// that compared something stamps, and a LATER one re-stamps rather than
		// latching on the first.
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)
		f.project(t, auditUnitA, "Loft A", 1)

		a.pass(ctx)
		first := a.Status().LastPassAt
		require.Equal(t, 1, a.Status().Audited)
		require.False(t, first.IsZero(), "a pass that audited an anchor reached a verdict")

		a.pass(ctx)
		require.True(t, a.Status().LastPassAt.After(first),
			"and each subsequent verdict re-stamps, or a running audit would age into staleness")
	})

	t.Run("an outage stops the clock a running lens was relying on", func(t *testing.T) {
		// The consequence stated end to end: the clock advances while the target
		// answers, and stops the moment the target stops answering — WITHOUT the
		// auditor reporting anything else wrong. That is what lets Stale, and so
		// the narrowing licence, see an outage the counters cannot show.
		f := newAuditFixture(t, seedUnitsSpec, nil)
		a := f.installAudit(t, 10)
		f.project(t, auditUnitA, "Loft A", 1)

		a.pass(ctx)
		stamped := a.Status().LastPassAt
		require.False(t, stamped.IsZero())

		require.NoError(t, f.p.HotReloadInto(failingRowReader{Adapter: f.p.currentAdapter(), err: context.DeadlineExceeded}))
		a.pass(ctx)
		st := a.Status()
		require.Equal(t, 1, st.Unverified)
		require.Zero(t, st.DivergentTotal, "the blind pass publishes a clean-looking number, exactly as before")
		require.Equal(t, stamped, st.LastPassAt,
			"but the clock behind it does not move, so the licence reading it can age out of the narrowing")
	})
}

// TestAudit_CycleTotalsSpanTheWholeWalk is the other half of the same property:
// a cycle's finding is accumulated across every pass of that cycle, so a
// divergence found in the first batch is still readable when the last one
// closes. Keyed on the per-pass count alone, a divergence on a corpus wider than
// one batch would flap in and out of the issue list once per rotation.
func TestAudit_CycleTotalsSpanTheWholeWalk(t *testing.T) {
	f := newAuditFixture(t, seedUnitsSpec, nil)
	const batch = 2
	a := f.installAudit(t, batch)
	ctx := context.Background()

	ids := []string{
		"AudtspanAAAAAAAAAAA1", "AudtspanAAAAAAAAAAA2", "AudtspanAAAAAAAAAAA3",
		"AudtspanAAAAAAAAAAA4",
	}
	var keys []string
	for i, id := range ids {
		keys = append(keys, f.project(t, id, "Loft "+id, uint64(i+1)))
	}
	corruptStoredRow(t, f.targetKV, keys[0])

	a.pass(ctx)
	require.Equal(t, 1, a.Status().DivergentTotal, "the first batch finds it")

	a.pass(ctx)
	st := a.Status()
	require.Zero(t, st.DivergentTotal, "the second batch re-examines different anchors, so the PASS count is zero")
	require.Equal(t, 1, st.CycleDivergentTotal,
		"but the cycle's finding stands until a new cycle re-derives it — a pass must never clear a verdict it did not re-derive")
	require.Equal(t, 4, st.CycleAudited)
}

// TestAuditorStale walks the verdict clock Auditor.Stale reads. It needs no
// corpus and no substrate: the whole mechanism is LastPassAt against the
// auditor's own cadence, and `now` is a parameter precisely so the window can be
// crossed by naming a time rather than by sleeping through one.
func TestAuditorStale(t *testing.T) {
	const interval = 15 * time.Minute
	window := auditorStaleCycles * interval
	base := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)

	// The auditor a lens carries when it is being audited: a real cadence and a
	// verdict clock a pass has stamped.
	enrolled := func(lastPass time.Time) *Auditor {
		a := newAuditor(nil, AuditPlan{AnchorLabel: "unit", Interval: interval}, false)
		a.status.LastPassAt = lastPass
		return a
	}

	t.Run("no cadence at all is stale — a refused auditor is not a standing re-test", func(t *testing.T) {
		// A refused lens holds no interval. The heartbeat reads that zero as "no
		// cadence to be late against" and stays quiet; a WRITE licence must read
		// it the other way, because an auditor with no clock re-tests nothing.
		a := newRefusedAuditor(nil, "it projects onto the auth plane")
		require.Zero(t, a.Interval())
		stale, elapsed := a.Stale(base)
		require.True(t, stale)
		require.Zero(t, elapsed, "with no cadence there is no window to measure an age against")
	})

	t.Run("a verdict clock that has never been stamped is stale", func(t *testing.T) {
		a := enrolled(time.Time{})
		require.True(t, a.Status().LastPassAt.IsZero())
		stale, elapsed := a.Stale(base)
		require.True(t, stale, "not yet proven is not licensed")
		require.Positive(t, elapsed)
	})

	t.Run("a recent verdict is not stale", func(t *testing.T) {
		a := enrolled(base.Add(-interval))
		stale, elapsed := a.Stale(base)
		require.False(t, stale)
		require.Equal(t, interval, elapsed)
	})

	t.Run("exactly at the window is not yet stale", func(t *testing.T) {
		// The comparison is strictly greater-than, so the last instant of the
		// window still counts as a running audit. Pinned rather than left to
		// taste: the licence's own boundary case cannot assert it, because there
		// `now` is the wall clock.
		a := enrolled(base.Add(-window))
		stale, elapsed := a.Stale(base)
		require.False(t, stale)
		require.Equal(t, window, elapsed)
	})

	t.Run("one nanosecond past the window is stale", func(t *testing.T) {
		a := enrolled(base.Add(-window - time.Nanosecond))
		stale, elapsed := a.Stale(base)
		require.True(t, stale)
		require.Equal(t, window+time.Nanosecond, elapsed)
	})

	t.Run("a long-wedged audit is stale and reports how long", func(t *testing.T) {
		a := enrolled(base.Add(-72 * time.Hour))
		stale, elapsed := a.Stale(base)
		require.True(t, stale)
		require.Equal(t, 72*time.Hour, elapsed, "the elapsed is what the refusal names, so it must be the real age")
	})

	t.Run("a clock ahead of now is not stale", func(t *testing.T) {
		// A negative elapsed (a clock step, or a restored cursor stamped by
		// another instance) reads as fresh rather than as an enormous age, which
		// is what a bare subtraction of an unsigned width would produce.
		a := enrolled(base.Add(time.Hour))
		stale, elapsed := a.Stale(base)
		require.False(t, stale)
		require.Negative(t, elapsed)
	})
}
