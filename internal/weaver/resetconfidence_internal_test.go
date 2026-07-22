package weaver

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedFullWindow fills a (target, gap, action) confidence window to capacity
// with zero closes — the shape flagEffectMismatches raises on, and the exact
// fossil the drain exists to clear.
func seedFullWindow(t *testing.T, ctx context.Context, m *markStore, targetID, gap, action string) {
	t.Helper()
	for i := 0; i < effectWindowSize; i++ {
		if err := m.recordEffectDispatch(ctx, targetID, gap, action); err != nil {
			t.Fatalf("recordEffectDispatch %s/%s/%s #%d: %v", targetID, gap, action, i, err)
		}
	}
}

// stateKeys lists every live weaver-state key, so a test can assert on the
// whole bucket rather than only the keys it thought to name.
func stateKeys(t *testing.T, ctx context.Context, h *controlHarness) map[string]struct{} {
	t.Helper()
	keys, err := h.conn.KVListKeys(ctx, h.engine.cfg.WeaverStateBucket)
	if err != nil {
		t.Fatalf("list weaver-state keys: %v", err)
	}
	out := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

// TestResetConfidence_DeletesOnlyThisTargetsEffectWindows is the load-bearing
// blast-radius test: a reset drains every `__effect` window under the named
// target and touches nothing else — not that target's in-flight marks, its
// `…__count` retry budget, or its `__control` disabled marker, and not another
// target's windows. Those three key families plus the sibling target are the
// exact things a prefix-delete (Revoke) would take with it, which is why this
// verb exists instead of reusing revoke.
func TestResetConfidence_DeletesOnlyThisTargetsEffectWindows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{
		TargetID: "t1",
		LensRef:  "lens-1",
		Gaps: map[string]GapAction{
			"missing_x": {Action: actionDirectOp, Operation: "FixX"},
			"missing_y": {Action: actionDirectOp, Operation: "FixY"},
		},
	})
	h.seedTarget(&Target{
		TargetID: "t2",
		LensRef:  "lens-2",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	m := h.engine.marks

	// Two windows under t1 (two gap columns), one under the sibling t2.
	seedFullWindow(t, ctx, m, "t1", "missing_x", actionDirectOp)
	seedFullWindow(t, ctx, m, "t1", "missing_y", actionDirectOp)
	seedFullWindow(t, ctx, m, "t2", "missing_x", actionDirectOp)

	// Everything the reset must NOT touch, all under t1's own prefix.
	entityID := testNanoID(t)
	if _, _, _, err := m.create(ctx, "t1", entityID, "missing_x",
		"vtx.leaseApp."+entityID, actionDirectOp); err != nil {
		t.Fatalf("create mark: %v", err)
	}
	if _, err := m.incrementDispatchCount(ctx, "t1", entityID, "missing_x"); err != nil {
		t.Fatalf("incrementDispatchCount: %v", err)
	}
	if err := m.setDisabled(ctx, "t1", true); err != nil {
		t.Fatalf("setDisabled: %v", err)
	}

	deleted, err := h.engine.ResetConfidence(ctx, "t1")
	if err != nil {
		t.Fatalf("ResetConfidence: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("ResetConfidence deleted = %d, want 2 (t1's two gap-column windows)", deleted)
	}

	keys := stateKeys(t, ctx, h)
	for key := range keys {
		if strings.HasPrefix(key, "t1"+effectKeyMarker) {
			t.Errorf("t1 effect window %q survived the reset", key)
		}
	}
	if _, ok := keys[effectKey("t2", "missing_x", actionDirectOp)]; !ok {
		t.Error("sibling target t2's window was deleted — the reset is not target-scoped")
	}
	if _, _, found, err := m.get(ctx, "t1", entityID, "missing_x"); err != nil {
		t.Fatalf("get mark: %v", err)
	} else if !found {
		t.Error("t1's in-flight mark was deleted — a reset must never clear dispatch state")
	}
	if n, err := m.getDispatchCount(ctx, "t1", entityID, "missing_x"); err != nil {
		t.Fatalf("getDispatchCount: %v", err)
	} else if n != 1 {
		t.Errorf("t1's dispatch count = %d, want 1 (retry budget is live state, not a fossil)", n)
	}
	if disabled, err := m.isDisabled(ctx, "t1"); err != nil {
		t.Fatalf("isDisabled: %v", err)
	} else if !disabled {
		t.Error("t1's __control marker was cleared — a reset must not change enable/disable state")
	}
}

// TestResetConfidence_IdempotentRerunDeletesNothing pins the rerun posture the
// verb's partial-failure story rests on (§11): re-issuing the command after a
// successful drain is a harmless no-op reporting zero, not an error.
func TestResetConfidence_IdempotentRerunDeletesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{
		TargetID: "t1",
		LensRef:  "lens-1",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	seedFullWindow(t, ctx, h.engine.marks, "t1", "missing_x", actionDirectOp)

	if deleted, err := h.engine.ResetConfidence(ctx, "t1"); err != nil || deleted != 1 {
		t.Fatalf("first ResetConfidence = (%d, %v), want (1, nil)", deleted, err)
	}
	deleted, err := h.engine.ResetConfidence(ctx, "t1")
	if err != nil {
		t.Fatalf("second ResetConfidence: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("second ResetConfidence deleted = %d, want 0 (idempotent rerun)", deleted)
	}
}

// TestResetConfidence_NotRegistered pins the registration gate: an unregistered
// target errors rather than silently sweeping keys, mirroring Disable/Enable.
// A window whose target is genuinely gone is sweepEffect's orphan leg, not an
// operator's — so a typo'd target ID must fail loudly instead of reporting a
// successful zero-window drain.
func TestResetConfidence_NotRegistered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	deleted, err := h.engine.ResetConfidence(ctx, "ghost")
	if err == nil {
		t.Fatalf("ResetConfidence(ghost) = (%d, nil), want an error (target not registered)", deleted)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %v, want it to name the unregistered target", err)
	}
}

// TestResetConfidence_PrefixBoundaryIsTheReservedMarker pins that the scan
// keys off `<targetId>.__effect.` and not a bare `<targetId>` prefix: target
// "t1" must never drain target "t10"'s windows. Contract #1 target IDs are
// single dot-free tokens, so the reserved marker is what makes the boundary
// exact.
func TestResetConfidence_PrefixBoundaryIsTheReservedMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	for _, id := range []string{"t1", "t10"} {
		h.seedTarget(&Target{
			TargetID: id,
			LensRef:  "lens-" + id,
			Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
		})
		seedFullWindow(t, ctx, h.engine.marks, id, "missing_x", actionDirectOp)
	}

	deleted, err := h.engine.ResetConfidence(ctx, "t1")
	if err != nil {
		t.Fatalf("ResetConfidence: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("ResetConfidence(t1) deleted = %d, want 1 — t10's window must not match t1's prefix", deleted)
	}
	if _, ok := stateKeys(t, ctx, h)[effectKey("t10", "missing_x", actionDirectOp)]; !ok {
		t.Fatal("t10's window was drained by a reset of t1 — the prefix boundary is wrong")
	}
}

// TestResetConfidence_RevisionConditionedDeleteSkipsARacingBooking pins the
// CAS posture the verb relies on: the delete is conditioned on the revision
// read this pass, so a dispatch or close that lands between the read and the
// delete wins and survives as honest new history rather than being clobbered
// blind.
//
// substrate.Conn is a concrete type with no injection seam, so the race cannot
// be driven from inside ResetConfidence's loop deterministically (a timing-
// based attempt would be exactly the fixed-sleep synchronisation the house
// rules forbid). This drives the same KVDeleteRevision call the loop makes,
// with the same stale-revision argument a racing booking produces, and asserts
// both halves of the branch: the conflict is reported as ErrRevisionConflict
// (which the loop skips rather than treating as fatal), and the window's fresh
// content survives intact.
func TestResetConfidence_RevisionConditionedDeleteSkipsARacingBooking(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	m := h.engine.marks
	const targetID, gap = "t1", "missing_x"
	key := effectKey(targetID, gap, actionDirectOp)

	if err := m.recordEffectDispatch(ctx, targetID, gap, actionDirectOp); err != nil {
		t.Fatalf("recordEffectDispatch: %v", err)
	}
	entry, err := h.conn.KVGet(ctx, h.engine.cfg.WeaverStateBucket, key)
	if err != nil {
		t.Fatalf("KVGet: %v", err)
	}
	staleRevision := entry.Revision

	// The racing booking: a second dispatch bumps the revision the reset read.
	if err := m.recordEffectDispatch(ctx, targetID, gap, actionDirectOp); err != nil {
		t.Fatalf("racing recordEffectDispatch: %v", err)
	}

	err = h.conn.KVDeleteRevision(ctx, h.engine.cfg.WeaverStateBucket, key, staleRevision)
	if !errors.Is(err, substrate.ErrRevisionConflict) {
		t.Fatalf("KVDeleteRevision at a stale revision = %v, want ErrRevisionConflict", err)
	}
	if _, ok := stateKeys(t, ctx, h)[key]; !ok {
		t.Fatal("the window was deleted despite the revision conflict — a racing booking would be lost")
	}
}

// TestResetConfidence_ClearsAStandingEffectMismatch is the convergence proof
// the whole verb exists for: a fossil window raises a standing
// LensEffectMismatch that no close will ever clear, and after the reset the
// next heartbeat scan lists nothing for the target — the issue and the
// effectMismatches metric both go to zero through the existing reconciliation
// loop, with no new clearing path.
func TestResetConfidence_ClearsAStandingEffectMismatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{
		TargetID: "t1",
		LensRef:  "lens-1",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	m := h.engine.marks
	const targetID, gap = "t1", "missing_x"
	seedFullWindow(t, ctx, m, targetID, gap, actionDirectOp)

	hb := &heartbeater{
		marks:                 m,
		issues:                newIssueCache(),
		effectMismatchAlerted: make(map[string]struct{}),
		logger:                slog.New(slog.NewTextHandler(discardWriter{}, nil)),
	}
	metrics := map[string]any{}
	hb.flagEffectMismatches(ctx, metrics)
	if got := metrics["effectMismatches"]; got != 1 {
		t.Fatalf("effectMismatches before the reset = %v, want 1 (the fossil window)", got)
	}
	wantKey := issueKeyEffect(targetID, gap, actionDirectOp)
	if _, ok := hb.effectMismatchAlerted[wantKey]; !ok {
		t.Fatalf("LensEffectMismatch not raised for %q before the reset", wantKey)
	}

	if _, err := h.engine.ResetConfidence(ctx, targetID); err != nil {
		t.Fatalf("ResetConfidence: %v", err)
	}

	metricsAfter := map[string]any{}
	hb.flagEffectMismatches(ctx, metricsAfter)
	if got := metricsAfter["effectMismatches"]; got != 0 {
		t.Fatalf("effectMismatches after the reset = %v, want 0", got)
	}
	if _, ok := hb.effectMismatchAlerted[wantKey]; ok {
		t.Fatalf("effectMismatchAlerted still tracks %q after the reset", wantKey)
	}
	if _, ok := effectIssue(hb.issues.snapshot()); ok {
		t.Fatalf("LensEffectMismatch issue not cleared after the reset; snapshot=%+v", hb.issues.snapshot())
	}
}
