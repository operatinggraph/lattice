package weaver

import (
	"context"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// surfaceTarget is the one-column `surface` playbook every test below drives,
// under a caller-chosen issue identity so the re-author vector can restate it.
func surfaceTarget(targetID, col, code, severity string) *Target {
	return &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionSurface, IssueCode: code, IssueSeverity: severity}},
	}
}

// TestHandleRow_SurfaceMembershipSurvivesAnUnreadableColumn holds the boundary
// between "this column closed" and "this pass could not read it".
//
// A row can project its gap column as a PRESENT non-bool — the string "true", a
// number, an object — and that value states nothing the level reconcile may act
// on. It is a per-row data fault, repaired by the next projection. The candidate
// walk nevertheless reaches the not-open branch for such a column, which is
// exactly why the membership retirement sits behind the same `readable` guard the
// target-scoped config latch does: a workload count shrunk on an unreadable read
// would fall by one per broken row, and climb back the moment each projected as a
// bool again, so the number an operator reads would flap at the rate a broken
// lens re-projects — while the rows themselves never stopped holding the work.
//
// The `since` is asserted alongside the count because a clear-and-re-raise would
// restore the count and still destroy the age of a backlog that never emptied.
func TestHandleRow_SurfaceMembershipSurvivesAnUnreadableColumn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureSurfaceUnreadable"
	const col = "missing_claim"
	h.seedTarget(surfaceTarget(targetID, col, "UnroutedTasks", "warning"))
	entityID := testNanoID(t)
	key := issueKeyGapOpen(targetID, col)

	open := map[string]any{"entityKey": "vtx.task." + entityID, "violating": true, col: true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
		t.Fatalf("surface dispatch: decision = %v, want Ack", dec)
	}
	entry, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("setup: expected the entry at %s, issues = %+v", key, h.engine.issues.snapshot())
	}
	arrival := entry.Since

	// The next projection carries the column as the STRING "true": present, and
	// not the §10.2 bool. Nothing here says the row's work is done.
	unreadable := map[string]any{"entityKey": "vtx.task." + entityID, "violating": true, col: "true"}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, unreadable, 2, 1)); dec != substrate.Ack {
		t.Fatalf("unreadable-column delivery: decision = %v, want Ack", dec)
	}
	if n := h.engine.surface.count(targetID, col); n != 1 {
		t.Fatalf("membership = %d, want 1: a column that cannot be read is not a column that closed", n)
	}
	entry, ok = issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the entry must stand — the row still holds the work; issues = %+v",
			h.engine.issues.snapshot())
	}
	if want := "target " + targetID + ": 1 row has column " + col + " true"; entry.Message != want {
		t.Fatalf("entry message = %q, want %q", entry.Message, want)
	}
	if entry.Since != arrival {
		t.Fatalf("the entry's since moved %q -> %q across an unreadable projection: the backlog never "+
			"emptied, so the fact never re-arose", arrival, entry.Since)
	}
	// The unreadable value is still reported, in the family that owns it.
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, col)); !ok {
		t.Fatalf("a present non-bool gap column is a per-row data fault and must be raised; issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestHandleRow_SurfaceMembershipSurvivesAnUnreadableViolating holds the same
// boundary at the widest removal leg there is.
//
// The non-violating leg drops the entity from EVERY column set of the target at
// once — it acts on the lens's verdict about the whole row, not about one column
// — so it is the leg with the most to lose from a value that states nothing. A
// present non-bool `violating` (the string "true", say) yields the conservative
// false every other consumer of the read wants, and taking that false here would
// sweep the row out of the backlog, delete the entry's `since` when it was the
// last member, and put it back the moment the column projected as a bool again.
// The gap column reading TRUE throughout is what isolates the leg: the candidate
// walk takes its open branch and touches nothing.
func TestHandleRow_SurfaceMembershipSurvivesAnUnreadableViolating(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureSurfaceBadFlag"
	const col = "missing_claim"
	h.seedTarget(surfaceTarget(targetID, col, "UnroutedTasks", "warning"))
	entityID := testNanoID(t)
	key := issueKeyGapOpen(targetID, col)

	open := map[string]any{"entityKey": "vtx.task." + entityID, "violating": true, col: true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
		t.Fatalf("surface dispatch: decision = %v, want Ack", dec)
	}
	entry, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("setup: expected the entry at %s, issues = %+v", key, h.engine.issues.snapshot())
	}
	arrival := entry.Since

	// The next projection carries `violating` as the STRING "true", with the gap
	// column still an honest bool true. The lens has stated nothing about whether
	// this row still holds work.
	unreadable := map[string]any{"entityKey": "vtx.task." + entityID, "violating": "true", col: true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, unreadable, 2, 1)); dec != substrate.Ack {
		t.Fatalf("unreadable-violating delivery: decision = %v, want Ack", dec)
	}
	if n := h.engine.surface.count(targetID, col); n != 1 {
		t.Fatalf("membership = %d, want 1: a row that cannot state whether it is violating has not said "+
			"its work is done, and this leg sweeps every column set of the target", n)
	}
	entry, ok = issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the entry must stand — the row still holds the work; issues = %+v",
			h.engine.issues.snapshot())
	}
	if want := "target " + targetID + ": 1 row has column " + col + " true"; entry.Message != want {
		t.Fatalf("entry message = %q, want %q", entry.Message, want)
	}
	if entry.Since != arrival {
		t.Fatalf("the entry's since moved %q -> %q across an unreadable `violating`: the backlog never "+
			"emptied, so the fact never re-arose", arrival, entry.Since)
	}
	// The unreadable value is still reported, in the family that owns it, exactly
	// once for the column it is on.
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "violating")); !ok {
		t.Fatalf("a present non-bool `violating` is a per-row data fault and must be raised; issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestHandleRow_NonViolatingRowLeavesTheOpenRowSet pins the removal leg that no
// per-column walk can supply.
//
// `violating` is the lens's own verdict that a row holds open work, and handleRow
// returns at the L1 gate for a row that reads false — before the dispatch loop
// that would re-add a membership. The candidate walk cannot cover it either: a
// gap column may still read TRUE beside `violating: false`, so the walk takes its
// open branch and skips the retirement entirely. Without a leg at the verdict
// itself, a membership recorded while the row was violating stands for the
// process's lifetime, counting a row the lens says holds nothing.
//
// The re-arm at the end is what separates this from a leak in the other
// direction: the row is not exiled from the set, it simply is not in it while the
// lens says there is nothing to do.
func TestHandleRow_NonViolatingRowLeavesTheOpenRowSet(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureSurfaceNonViolating"
	const col = "missing_claim"
	h.seedTarget(surfaceTarget(targetID, col, "UnroutedTasks", "warning"))
	held, leaving := testNanoID(t), testNanoID(t)
	key := issueKeyGapOpen(targetID, col)
	open := func(entityID string) map[string]any {
		return map[string]any{"entityKey": "vtx.task." + entityID, "violating": true, col: true}
	}
	for i, entityID := range []string{held, leaving} {
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open(entityID), uint64(i+1), 1)); dec != substrate.Ack {
			t.Fatalf("surface dispatch: decision = %v, want Ack", dec)
		}
	}
	entry, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("setup: expected the entry at %s, issues = %+v", key, h.engine.issues.snapshot())
	}
	arrival := entry.Since

	// The lens re-projects one row as NOT violating while its gap column still
	// reads true — the exact shape the candidate walk cannot act on.
	settled := map[string]any{"entityKey": "vtx.task." + leaving, "violating": false, col: true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, leaving, settled, 3, 1)); dec != substrate.Ack {
		t.Fatalf("non-violating delivery: decision = %v, want Ack", dec)
	}
	if n := h.engine.surface.count(targetID, col); n != 1 {
		t.Fatalf("membership = %d, want 1: a row the lens calls non-violating holds no open work", n)
	}
	entry, ok = issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("one row leaving must not retire a column another row still holds; issues = %+v",
			h.engine.issues.snapshot())
	}
	if want := "target " + targetID + ": 1 row has column " + col + " true"; entry.Message != want {
		t.Fatalf("entry message = %q, want %q", entry.Message, want)
	}
	if entry.Since != arrival {
		t.Fatalf("the entry's since moved %q -> %q while the backlog stayed non-empty", arrival, entry.Since)
	}

	// The other row settles too: the set empties and the entry retires.
	stillHeld := map[string]any{"entityKey": "vtx.task." + held, "violating": false, col: true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, held, stillHeld, 4, 1)); dec != substrate.Ack {
		t.Fatalf("non-violating delivery: decision = %v, want Ack", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("the entry must retire when the last row leaves the set, still standing as %+v", is)
	}

	// And a row that starts violating again rejoins on that delivery's own
	// dispatch — the removal is a verdict about now, not an exile.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, leaving, open(leaving), 5, 1)); dec != substrate.Ack {
		t.Fatalf("re-violating delivery: decision = %v, want Ack", dec)
	}
	if n := h.engine.surface.count(targetID, col); n != 1 {
		t.Fatalf("membership = %d, want 1 after the row starts violating again", n)
	}
	h.requireNoOp(t)
}

// TestHandleRow_SurfaceEntryCarriesAReauthoredIssueIdentity is the wire-level
// half of surfaceStats.add's identity rule.
//
// issueCode and issueSeverity are package config, re-authorable at any install,
// and they are recorded on the member set at the ADD because no removal leg holds
// a *Target to read them from. That makes an add the ONLY path by which a
// re-author reaches Health KV — and a steady backlog produces no membership
// changes at all, so an add gated on membership alone would keep publishing the
// retired identity for as long as the backlog sat still. The count and the
// `since` are asserted with it: restating the identity is not a new fact, and
// must not disturb either.
func TestHandleRow_SurfaceEntryCarriesAReauthoredIssueIdentity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureSurfaceReauthor"
	const col = "missing_claim"
	h.seedTarget(surfaceTarget(targetID, col, "UnroutedTasks", "warning"))
	entityID := testNanoID(t)
	key := issueKeyGapOpen(targetID, col)
	open := map[string]any{"entityKey": "vtx.task." + entityID, "violating": true, col: true}

	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
		t.Fatalf("surface dispatch: decision = %v, want Ack", dec)
	}
	entry, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("setup: expected the entry at %s, issues = %+v", key, h.engine.issues.snapshot())
	}
	arrival := entry.Since

	// The package is re-authored: same column, new code and severity. The
	// backlog does not move — the same one row is still open.
	h.seedTarget(surfaceTarget(targetID, col, "UnclaimedTasks", "error"))
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 2, 1)); dec != substrate.Ack {
		t.Fatalf("redelivery after the re-author: decision = %v, want Ack", dec)
	}
	entry, ok = issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the entry must still stand; issues = %+v", h.engine.issues.snapshot())
	}
	if entry.Code != "UnclaimedTasks" || entry.Severity != "error" {
		t.Fatalf("entry = %+v, want the re-authored UnclaimedTasks/error: a steady backlog produces no "+
			"membership change, so the add is the only path a re-author has to the wire", entry)
	}
	if want := "target " + targetID + ": 1 row has column " + col + " true"; entry.Message != want {
		t.Fatalf("entry message = %q, want %q — restating the identity must not move the count",
			entry.Message, want)
	}
	if entry.Since != arrival {
		t.Fatalf("the entry's since moved %q -> %q across a re-author: the backlog never emptied, so the "+
			"fact never re-arose", arrival, entry.Since)
	}
}

// TestSweep_CountLegRowGoneKeepsTheOpenRowMembership is the guard on the one
// retirement the sweep's row-gone leg must NOT make.
//
// That leg reaches a (target, entity, gap) from a stranded `…__count` key whose
// row is absent from weaver-targets, and its own standing decision is that
// absence is not evidence — the row may be mid-rebuild, which is why it retires
// the gap's FAULTS (each states a fact about a row that is not there) and leaves
// the retry bound itself to the TTL. A `surface` membership is neither: it says
// the target has a unit of open business work, and a row that is momentarily
// unreadable from KV has not completed it. Retiring it here would under-report
// the backlog for the whole rebuild window, on a per-row basis, with the entry
// still standing at a smaller count and nothing to say why.
//
// The entity that is genuinely gone is retired by lane-1's tombstone leg, which
// sweeps every column set of the target.
func TestSweep_CountLegRowGoneKeepsTheOpenRowMembership(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureSurfaceRowGone"
	const col = "missing_claim"
	h.seedTarget(surfaceTarget(targetID, col, "UnroutedTasks", "warning"))
	entityID := testNanoID(t)
	key := issueKeyGapOpen(targetID, col)

	// The membership as lane 1 would have recorded it, through the production
	// reflector so the Health entry exists exactly as it would on the wire.
	h.engine.surface.add(targetID, col, entityID, "UnroutedTasks", "warning", h.engine.surfaceReflector(targetID))
	entry, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("setup: expected the entry at %s, issues = %+v", key, h.engine.issues.snapshot())
	}
	arrival := entry.Since

	// A dispatch-count stranded at the same column by an earlier package version
	// (a `surface` gap mints none of its own), and no row in weaver-targets: the
	// sweep's count leg reads row-gone.
	h.seedCount(t, ctx, targetID, entityID, col, 2)
	h.pass(ctx)

	if n := h.engine.surface.count(targetID, col); n != 1 {
		t.Fatalf("membership = %d, want 1: a row absent from KV has not finished its work, and this leg "+
			"read no column at all", n)
	}
	entry, ok = issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the entry must stand; issues = %+v", h.engine.issues.snapshot())
	}
	if want := "target " + targetID + ": 1 row has column " + col + " true"; entry.Message != want {
		t.Fatalf("entry message = %q, want %q", entry.Message, want)
	}
	if entry.Since != arrival {
		t.Fatalf("the entry's since moved %q -> %q across a sweep that observed nothing", arrival, entry.Since)
	}
}

// TestSweep_MarkLegRowGoneKeepsTheOpenRowMembership is the row-gone guard on the
// sweep's other leg.
//
// A `surface` gap mints no mark of its own, so a mark at such a column is
// stranded state — a column an earlier package version remediated. The sweep
// reaches it, finds no row in weaver-targets, and clears the mark on the
// standing "no column of an absent row can be true" reconcile. That reconcile is
// about the MARK. It reads no column, so it witnesses nothing about the row's
// open workload, and a row absent for a rebuild window has not completed its
// business work. Deciding the membership from the delete's REASON rather than
// from a read is what merges this leg with the one that did read: both arrive
// carrying `gapClosed`.
func TestSweep_MarkLegRowGoneKeepsTheOpenRowMembership(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureSurfaceMarkGone"
	const col = "missing_claim"
	h.seedTarget(surfaceTarget(targetID, col, "UnroutedTasks", "warning"))
	entityID := testNanoID(t)
	key := issueKeyGapOpen(targetID, col)

	h.engine.surface.add(targetID, col, entityID, "UnroutedTasks", "warning", h.engine.surfaceReflector(targetID))
	entry, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("setup: expected the entry at %s, issues = %+v", key, h.engine.issues.snapshot())
	}
	arrival := entry.Since

	mk := markKey(targetID, entityID, col)
	h.putMark(t, ctx, mk, fixtureMark(targetID, entityID, col, "directOp", pastLease()))
	h.pass(ctx)

	if h.markExists(t, ctx, mk) {
		t.Fatalf("setup: the stranded mark must be cleared by the row-gone reconcile, so this test " +
			"exercises the leg it means to")
	}
	if n := h.engine.surface.count(targetID, col); n != 1 {
		t.Fatalf("membership = %d, want 1: a row absent from KV has not finished its work, and this leg "+
			"read no column at all", n)
	}
	entry, ok = issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the entry must stand; issues = %+v", h.engine.issues.snapshot())
	}
	if want := "target " + targetID + ": 1 row has column " + col + " true"; entry.Message != want {
		t.Fatalf("entry message = %q, want %q", entry.Message, want)
	}
	if entry.Since != arrival {
		t.Fatalf("the entry's since moved %q -> %q across a sweep that observed nothing", arrival, entry.Since)
	}
}

// TestSweep_UnreadableColumnKeepsTheOpenRowMembership carries the unreadable-value
// boundary onto both sweep legs at once.
//
// Each reaches its gap from a different stranded key — a mark, and a
// dispatch-count — and each decides "closed" from the same column read. The
// conservative false that read hands back on a present non-bool is what clears
// the stranded key, which is right: a key nothing can read a live column for is
// no use to anyone. It is not evidence about the row's open WORKLOAD, and the two
// legs must not diverge from lane 1 on that, since the sweep is the only leg that
// runs at all for a row that has gone quiet.
//
// One pass carries both vectors: the count leg defers to the mark leg whenever
// the same gap holds both, so the two entities keep them apart.
func TestSweep_UnreadableColumnKeepsTheOpenRowMembership(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureSurfBadCol"
	const col = "missing_claim"
	h.seedTarget(surfaceTarget(targetID, col, "UnroutedTasks", "warning"))
	marked, counted := testNanoID(t), testNanoID(t)
	key := issueKeyGapOpen(targetID, col)

	for _, entityID := range []string{marked, counted} {
		h.engine.surface.add(targetID, col, entityID, "UnroutedTasks", "warning", h.engine.surfaceReflector(targetID))
		// Both rows are PRESENT and project the column as the string "true".
		h.putRow(t, ctx, targetID, entityID, map[string]any{
			"entityKey": "vtx.task." + entityID, "violating": true, col: "true",
		})
	}
	entry, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("setup: expected the entry at %s, issues = %+v", key, h.engine.issues.snapshot())
	}
	arrival := entry.Since

	mk := markKey(targetID, marked, col)
	h.putMark(t, ctx, mk, fixtureMark(targetID, marked, col, "directOp", pastLease()))
	h.seedCount(t, ctx, targetID, counted, col, 2)
	h.pass(ctx)

	// Both legs took their close branch — the stranded keys are gone — so the
	// membership assertion below is about the retirement they did NOT make.
	if h.markExists(t, ctx, mk) {
		t.Fatalf("setup: the mark leg must have taken its close branch")
	}
	if h.countExists(t, ctx, targetID, counted, col) {
		t.Fatalf("setup: the count leg must have taken its close branch")
	}
	if n := h.engine.surface.count(targetID, col); n != 2 {
		t.Fatalf("membership = %d, want 2: a column neither leg could READ is not a column that closed", n)
	}
	entry, ok = issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the entry must stand; issues = %+v", h.engine.issues.snapshot())
	}
	if want := "target " + targetID + ": 2 rows have column " + col + " true"; entry.Message != want {
		t.Fatalf("entry message = %q, want %q", entry.Message, want)
	}
	if entry.Since != arrival {
		t.Fatalf("the entry's since moved %q -> %q across a sweep that read no usable column",
			arrival, entry.Since)
	}
}
