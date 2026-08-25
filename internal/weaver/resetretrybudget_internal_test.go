package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestResetDispatchCount_ZeroesInPlaceAndKeepsTheKey is the central assertion
// of the whole verb: the reset REWRITES the count to zero and the key SURVIVES.
//
// That is not a stylistic preference over a delete. The reconciler sweep routes
// weaver-state by key shape, so the count leg — the only leg that can re-arm a
// markless gap — is reached only for a key ending countKeySuffix. An exhausted
// gap has long since lost its mark, so a reset that removed the count would
// leave NO key in the bucket naming the gap: nothing would enumerate it and
// nothing would dispatch it, and the operator's un-park would produce a quieter
// version of the silent park it exists to cure.
//
// The re-armed TTL is asserted on the wire for the same reason it is asserted
// for a counted key: a reset entry must carry exactly the backstop a counted one
// does, so a reset nobody acts on is still collected instead of living forever.
func TestResetDispatchCount_ZeroesInPlaceAndKeepsTheKey(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx) // lease = time.Minute
	const targetID, gap = "t1", "missing_x"
	entityID := testNanoID(t)

	for i := 0; i < 3; i++ {
		if _, err := m.incrementDispatchCount(ctx, targetID, entityID, gap); err != nil {
			t.Fatalf("seed dispatch-count #%d: %v", i, err)
		}
	}

	cleared, found, err := m.resetDispatchCount(ctx, targetID, entityID, gap)
	if err != nil {
		t.Fatalf("resetDispatchCount: %v", err)
	}
	if !found {
		t.Fatal("resetDispatchCount reported no count for a gap that had one")
	}
	if cleared != 3 {
		t.Fatalf("resetDispatchCount cleared = %d, want 3 (the spent budget it displaced)", cleared)
	}

	key := countKey(targetID, entityID, gap)
	entry, err := m.conn.KVGet(ctx, m.bucket, key)
	if err != nil {
		t.Fatalf("the count key must SURVIVE the reset — the sweep reaches this gap only through it: %v", err)
	}
	var dc dispatchCount
	if err := json.Unmarshal(entry.Value, &dc); err != nil {
		t.Fatalf("unmarshal reset count: %v", err)
	}
	if dc.Count != 0 {
		t.Fatalf("count after the reset = %d, want 0", dc.Count)
	}
	if n, err := m.getDispatchCount(ctx, targetID, entityID, gap); err != nil || n != 0 {
		t.Fatalf("getDispatchCount after the reset = (%d, %v), want (0, nil)", n, err)
	}

	stream, err := m.conn.JetStream().Stream(ctx, "KV_weaver-state")
	if err != nil {
		t.Fatalf("open weaver-state stream: %v", err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, "$KV.weaver-state."+key)
	if err != nil {
		t.Fatalf("read raw reset count message: %v", err)
	}
	wantTTL := (dispatchCountTTLBackstopFactor * time.Minute).String()
	if got := raw.Header.Get("Nats-TTL"); got != wantTTL {
		t.Fatalf("reset count Nats-TTL header = %q, want %q — a reset key must keep the standard backstop", got, wantTTL)
	}
}

// TestResetDispatchCount_AlreadyZeroIsStillASuccess pins the idempotent rerun:
// re-issuing the verb against an already-reset gap reports zero cleared and no
// error, exactly as a rerun of resetConfidence reports zero windows.
func TestResetDispatchCount_AlreadyZeroIsStillASuccess(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap = "t1", "missing_x"
	entityID := testNanoID(t)
	if _, err := m.incrementDispatchCount(ctx, targetID, entityID, gap); err != nil {
		t.Fatalf("seed dispatch-count: %v", err)
	}

	if cleared, found, err := m.resetDispatchCount(ctx, targetID, entityID, gap); err != nil || !found || cleared != 1 {
		t.Fatalf("first reset = (%d, %v, %v), want (1, true, nil)", cleared, found, err)
	}
	cleared, found, err := m.resetDispatchCount(ctx, targetID, entityID, gap)
	if err != nil {
		t.Fatalf("second reset: %v", err)
	}
	if !found {
		t.Fatal("second reset reported no count — the key must still exist after the first")
	}
	if cleared != 0 {
		t.Fatalf("second reset cleared = %d, want 0 (nothing left to clear)", cleared)
	}
}

// TestResetDispatchCount_AbsentKeyIsSuccessAndMintsNothing pins both halves of
// the absence posture: a gap with no count has no suppression to lift, so the
// verb succeeds reporting zero rather than erroring — and it does NOT create the
// key on the way, so an operator naming a gap that was never parked (or fat-
// fingering an entity that exists) cannot seed weaver-state with budget keys.
func TestResetDispatchCount_AbsentKeyIsSuccessAndMintsNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap = "t1", "missing_x"
	entityID := testNanoID(t)

	cleared, found, err := m.resetDispatchCount(ctx, targetID, entityID, gap)
	if err != nil {
		t.Fatalf("resetDispatchCount on an absent key: %v", err)
	}
	if found {
		t.Fatal("resetDispatchCount reported a count for a gap that has none")
	}
	if cleared != 0 {
		t.Fatalf("cleared = %d, want 0", cleared)
	}
	if _, err := m.conn.KVGet(ctx, m.bucket, countKey(targetID, entityID, gap)); !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("the reset minted a count key for a gap that had none (KVGet err = %v)", err)
	}
}

// TestResetDispatchCount_RevisionConflictLosesToAConcurrentDispatch pins the
// CAS posture the verb rests on: the write is conditioned on the revision read
// in the same call, so a dispatch landing between that read and the write moved
// the budget itself and wins — the operator's zero never clobbers a genuine
// attempt, and a rerun is the remedy.
//
// substrate.Conn is a concrete type with no injection seam, so the interleave
// cannot be driven from inside resetDispatchCount deterministically (a
// timing-based attempt would be exactly the fixed-sleep synchronisation the
// house rules forbid). This drives the same KVUpdateWithTTL call the method
// makes, with the same stale-revision argument a racing dispatch produces, and
// asserts all three halves of the documented posture: the write is refused as
// ErrRevisionConflict, the racing dispatch's fresh count survives intact, and
// the rerun the operator is told to make then succeeds against the new revision.
func TestResetDispatchCount_RevisionConflictLosesToAConcurrentDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap = "t1", "missing_x"
	entityID := testNanoID(t)
	key := countKey(targetID, entityID, gap)

	if _, err := m.incrementDispatchCount(ctx, targetID, entityID, gap); err != nil {
		t.Fatalf("seed dispatch-count: %v", err)
	}
	entry, err := m.conn.KVGet(ctx, m.bucket, key)
	if err != nil {
		t.Fatalf("KVGet: %v", err)
	}
	staleRevision := entry.Revision

	// The racing dispatch: a second attempt bumps the revision the reset read.
	if _, err := m.incrementDispatchCount(ctx, targetID, entityID, gap); err != nil {
		t.Fatalf("racing incrementDispatchCount: %v", err)
	}

	body, err := json.Marshal(dispatchCount{Count: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	_, err = m.conn.KVUpdateWithTTL(ctx, m.bucket, key, body, staleRevision, dispatchCountTTLBackstopFactor*m.lease)
	if !errors.Is(err, substrate.ErrRevisionConflict) {
		t.Fatalf("zeroing at a stale revision = %v, want ErrRevisionConflict", err)
	}
	if n, err := m.getDispatchCount(ctx, targetID, entityID, gap); err != nil || n != 2 {
		t.Fatalf("count after the refused reset = (%d, %v), want (2, nil) — the racing dispatch must survive", n, err)
	}

	cleared, found, err := m.resetDispatchCount(ctx, targetID, entityID, gap)
	if err != nil || !found || cleared != 2 {
		t.Fatalf("rerun after the conflict = (%d, %v, %v), want (2, true, nil)", cleared, found, err)
	}
}

// TestResetRetryBudget_NotRegistered pins the registration gate: an
// unregistered target errors rather than quietly reporting a zero reset,
// mirroring Disable/Enable/ResetConfidence. A count whose target is genuinely
// gone is the sweep's business, not an operator's — so a typo'd target must
// fail loudly.
func TestResetRetryBudget_NotRegistered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	cleared, found, err := h.engine.ResetRetryBudget(ctx, "ghost", testNanoID(t), "missing_x")
	if err == nil {
		t.Fatalf("ResetRetryBudget(ghost) = (%d, %v, nil), want an error", cleared, found)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %v, want it to name the unregistered target", err)
	}
}

// TestResetRetryBudget_RefusesAMalformedScopeWithoutTouchingKV pins the
// fail-closed whitelist on the two values that concatenate into a KV key. Each
// vector plants a REAL count key at the shape the malformed argument would
// build, then asserts the verb refuses AND that the planted value is untouched
// — so the refusal happens before the bucket, not after a read that decided the
// key looked odd.
//
// The rule is a whitelist (a NanoID entityId, a missing_<gap> single token), not
// a list of forbidden characters: an enumerated-legal rule stays closed as the
// key vocabulary grows, while an enumerated-illegal one silently admits every
// shape nobody thought of.
func TestResetRetryBudget_RefusesAMalformedScopeWithoutTouchingKV(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	const targetID = "t1"
	h.seedTarget(&Target{
		TargetID: targetID,
		LensRef:  "lens-1",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	goodEntity := testNanoID(t)

	for _, tc := range []struct {
		name      string
		entityID  string
		gapColumn string
		wantIn    string
	}{
		{"entityId is not a NanoID", "short", "missing_x", "entityId"},
		{"entityId carries an excluded character", strings.Repeat("O", 20), "missing_x", "entityId"},
		{"entityId is a reserved marker", "__control", "missing_x", "entityId"},
		{"gapColumn without the missing_ prefix", goodEntity, "notagap", "gapColumn"},
		{"gapColumn is a reserved marker", goodEntity, "__control", "gapColumn"},
		{"gapColumn smuggles the count suffix", goodEntity, "missing_x.__count", "gapColumn"},
		{"gapColumn is bare prefix plus nothing", goodEntity, "missing_", "gapColumn"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Plant the exact key the unvalidated argument would have reached.
			key := countKey(targetID, tc.entityID, tc.gapColumn)
			body, err := json.Marshal(dispatchCount{Count: 7})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if _, err := h.conn.KVPut(ctx, h.engine.cfg.WeaverStateBucket, key, body); err != nil {
				t.Skipf("KV refused the planted key %q itself: %v", key, err)
			}

			cleared, found, err := h.engine.ResetRetryBudget(ctx, targetID, tc.entityID, tc.gapColumn)
			if err == nil {
				t.Fatalf("ResetRetryBudget = (%d, %v, nil), want a refusal", cleared, found)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("error = %v, want it to name %s", err, tc.wantIn)
			}
			entry, gErr := h.conn.KVGet(ctx, h.engine.cfg.WeaverStateBucket, key)
			if gErr != nil {
				t.Fatalf("planted key read: %v", gErr)
			}
			var dc dispatchCount
			if uErr := json.Unmarshal(entry.Value, &dc); uErr != nil {
				t.Fatalf("unmarshal planted key: %v", uErr)
			}
			if dc.Count != 7 {
				t.Fatalf("planted count = %d, want 7 — the refusal must happen before KV is written", dc.Count)
			}
		})
	}
}

// TestValidateGapScope_AcceptsOnlyTheShapesAKeyIsBuiltFrom is the whitelist
// stated directly, so the rule is pinned independently of any caller: exactly a
// canonical NanoID and a missing_<gap> single token pass.
func TestValidateGapScope_AcceptsOnlyTheShapesAKeyIsBuiltFrom(t *testing.T) {
	t.Parallel()
	good := testNanoID(t)
	if err := ValidateGapScope(good, "missing_x"); err != nil {
		t.Fatalf("ValidateGapScope(%q, missing_x) = %v, want nil", good, err)
	}
	if err := ValidateGapScope(good, "missing_bg-check_2"); err != nil {
		t.Fatalf("ValidateGapScope with a hyphenated gap = %v, want nil", err)
	}
	for _, tc := range []struct{ name, entityID, gapColumn string }{
		{"empty entity", "", "missing_x"},
		{"short entity", good[:19], "missing_x"},
		{"long entity", good + "A", "missing_x"},
		{"entity with a dot", good[:19] + ".", "missing_x"},
		{"entity with an excluded char", good[:19] + "0", "missing_x"},
		{"empty gap", good, ""},
		{"gap without the prefix", good, "violating"},
		{"gap with a dot", good, "missing_x.y"},
		{"gap with a space", good, "missing_x y"},
		{"gap with a wildcard", good, "missing_*"},
	} {
		if err := ValidateGapScope(tc.entityID, tc.gapColumn); err == nil {
			t.Errorf("ValidateGapScope must refuse %s (%q, %q)", tc.name, tc.entityID, tc.gapColumn)
		}
	}
}

// TestResetRetryBudget_UnParksAnExhaustedGapThroughTheNextSweepPass is the
// end-to-end proof the verb and the re-arm arm exist for, and the reason they
// land together: neither is worth anything alone.
//
// The gap starts in the state the item was filed about — budget spent, mark long
// gone, row quiet, GapBudgetExhausted standing and nothing in the system able to
// retire it. The operator resets the budget, which dispatches nothing and clears
// nothing. The NEXT ordinary sweep pass is what un-parks it: the zeroed count is
// still enumerated, reads as un-suppressed, and the re-arm arm retires the
// standing stop and fires the episode.
//
// The surviving key is what makes that possible, and the test asserts it
// explicitly between the two halves — with the count deleted instead, the pass
// would find no key naming this gap and the operator's reset would produce
// exactly the silent park the verb exists to cure.
func TestResetRetryBudget_UnParksAnExhaustedGapThroughTheNextSweepPass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureResetBudgetUnpark"
	const budget = 2
	const gap = "missing_x"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{gap: {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	issueKey := issueKeyGapEntity(targetID, entityID, gap)

	// The park: the whole budget spent, the row still violating, no mark left.
	h.seedCount(t, ctx, targetID, entityID, gap, budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, gap, budget))
	if h.markExists(t, ctx, markKey(targetID, entityID, gap)) {
		t.Fatal("setup: this vector requires a markless gap")
	}

	h.pass(ctx)
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKey); !ok {
		t.Fatalf("setup: the exhausted gap must be parked with a standing stop (issues: %+v)",
			h.engine.issues.snapshot())
	}

	cleared, found, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, gap)
	if err != nil {
		t.Fatalf("ResetRetryBudget: %v", err)
	}
	if !found || cleared != budget {
		t.Fatalf("ResetRetryBudget = (%d, %v), want (%d, true)", cleared, found, budget)
	}

	// The verb itself is inert on the world: it neither dispatches nor clears.
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKey); !ok {
		t.Fatal("the verb must not retire the standing stop — the level reconcile owns that clear")
	}
	if !h.countExists(t, ctx, targetID, entityID, gap) {
		t.Fatal("the count key must survive the reset, or the sweep can never reach this gap again")
	}
	if got := h.countValue(t, ctx, targetID, entityID, gap); got != 0 {
		t.Fatalf("count after the reset = %d, want 0", got)
	}

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("operationType = %v, want FixX — the pass after a reset must re-arm the gap", op["operationType"])
	}
	if !h.markExists(t, ctx, markKey(targetID, entityID, gap)) {
		t.Fatal("the re-armed episode must hold the gap's mark — that mark is the arm's only storm bound")
	}
	if got := h.countValue(t, ctx, targetID, entityID, gap); got != 1 {
		t.Fatalf("count after the re-arm = %d, want 1 — the reset restarts the chain, it does not remove it", got)
	}
	if _, ok := issueAt(h.engine.issues, issueKey); ok {
		t.Fatalf("a gap that just dispatched cannot still be parked on a spent budget (issues: %+v)",
			h.engine.issues.snapshot())
	}
	h.requireNoOp(t)
}

// TestResetDispatchCount_GarbledBodyIsReportedNotOverwritten pins the one
// failure the verb reports rather than "fixes". A count whose body no reader can
// parse never suppressed the gap in the first place (getDispatchCount errors and
// gapSuppressed reads its safe, dispatchable side), so there is no park to
// release — and the sweep's own corrupt-body arm deletes the key within a pass,
// re-arming from 0 anyway. Overwriting it would destroy the evidence of a
// distinct fault while reporting a budget the operator never actually had;
// naming it tells them something true at no cost.
func TestResetDispatchCount_GarbledBodyIsReportedNotOverwritten(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap = "t1", "missing_x"
	entityID := testNanoID(t)
	key := countKey(targetID, entityID, gap)
	if _, err := m.conn.KVPut(ctx, m.bucket, key, []byte("{not json")); err != nil {
		t.Fatalf("plant a garbled count: %v", err)
	}

	cleared, found, err := m.resetDispatchCount(ctx, targetID, entityID, gap)
	if err == nil {
		t.Fatalf("resetDispatchCount over a garbled body = (%d, %v, nil), want an error naming the garble", cleared, found)
	}
	if !strings.Contains(err.Error(), "unmarshal dispatch-count") {
		t.Fatalf("error = %v, want it to name the unparseable count", err)
	}
	entry, gErr := m.conn.KVGet(ctx, m.bucket, key)
	if gErr != nil {
		t.Fatalf("planted key read: %v", gErr)
	}
	if string(entry.Value) != "{not json" {
		t.Fatalf("planted value = %q, want it untouched — the reset must not overwrite evidence of a distinct fault", entry.Value)
	}
}
