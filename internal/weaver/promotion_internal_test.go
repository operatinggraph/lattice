package weaver

import (
	"context"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// Promotion emission: Weaver's own recommendation that a goal leg which closed
// EVERY episode of its confidence window be promoted to the gap's declared
// playbook entry.
//
// The vectors drive the release credit site (releaseCompletedLeg's markless
// branch — the same recordEffectClose every close path runs through) and read
// what reached ops.system, because the emission's whole contract is "exactly one
// op per (target, gap, actionRef), ever": the boundary is not a state the engine
// keeps but an op it does or does not publish.

// seedEffectWindow writes the (target, gap, actionRef) confidence window
// directly: closed slots first, then pending ones (recordEffectClose flips the
// OLDEST pending, so the pending tail is what the next close consumes).
func seedEffectWindow(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID, gapColumn, actionRef string, closed, pending int) {
	t.Helper()
	window := make([]bool, 0, closed+pending)
	for i := 0; i < closed; i++ {
		window = append(window, true)
	}
	for i := 0; i < pending; i++ {
		window = append(window, false)
	}
	putStateValue(t, ctx, conn, effectKey(targetID, gapColumn, actionRef), effectStats{Window: window})
}

// creditOneClose drives ONE close credit for legA through the leg-release path —
// the markless branch, which conditions on the count document's revision exactly
// as the production sites do. Returns whether the release was taken.
func creditOneClose(t *testing.T, ctx context.Context, h *handlerHarness, targetID, entityID string, ga GapAction) bool {
	t.Helper()
	putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{Count: 1, Leg: "legA"})
	_, countRev, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, goalLegGap)
	if err != nil {
		t.Fatalf("read the seeded count's revision: %v", err)
	}
	row := goalLegRow(entityID, 0, map[string]any{"aDone": true})
	return h.engine.releaseCompletedLeg(ctx, targetID, entityID, goalLegGap, ga, "legA", row, 0, countRev)
}

// promotionHarness registers the two-leg planned goal target and returns its
// GapAction.
func promotionHarness(t *testing.T, ctx context.Context, targetID string, planned bool) (*handlerHarness, GapAction) {
	t.Helper()
	h := newHandlerHarness(t, ctx)
	spec := goalLegSpec(targetID, actionDirectOp, false)
	if !planned {
		delete(spec, "mode")
	}
	registerSpec(t, h.engine.source, spec)
	target, ok := h.engine.source.target(targetID)
	if !ok {
		t.Fatal("setup: the target must be registered")
	}
	return h, target.Gaps[goalLegGap]
}

// TestPromotion_FullAllClosedWindowEmitsExactlyOnce: the close that completes an
// all-closed window emits ONE RecordPromotionProposal under the deterministic
// per-triple requestId; every later close emits nothing, because the window that
// earned the recommendation stays earned.
func TestPromotion_FullAllClosedWindowEmitsExactlyOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const targetID = "fixPromoEmit"
	h, ga := promotionHarness(t, ctx, targetID, true)
	entityID := testNanoID(t)

	seedEffectWindow(t, ctx, h.conn, targetID, goalLegGap, "legA", effectWindowSize-1, 1)
	if !creditOneClose(t, ctx, h, targetID, entityID, ga) {
		t.Fatal("the leg release must be taken; it is the credit this emission hangs off")
	}

	op := h.nextOp(t)
	if op["operationType"] != opRecordPromotionProposal {
		t.Fatalf("op = %v, want %s", op["operationType"], opRecordPromotionProposal)
	}
	if op["requestId"] != derivePromotionRequestID(targetID, goalLegGap, "legA") {
		t.Fatalf("requestId = %v, want the per-triple deterministic id", op["requestId"])
	}
	payload, _ := op["payload"].(map[string]any)
	if got, _ := payload["actionRef"].(string); got != "legA" {
		t.Fatalf("payload actionRef = %v, want legA", payload["actionRef"])
	}
	if got, _ := payload["gapColumn"].(string); got != goalLegGap {
		t.Fatalf("payload gapColumn = %v, want %s", payload["gapColumn"], goalLegGap)
	}
	if got, _ := payload["handle"].(string); got != derivePromotionHandle(targetID, goalLegGap, "legA") {
		t.Fatalf("payload handle = %v, want the per-triple deterministic handle", payload["handle"])
	}
	if got, _ := payload["window"].(float64); int(got) != effectWindowSize {
		t.Fatalf("payload window = %v, want %d", payload["window"], effectWindowSize)
	}
	if got, _ := payload["closed"].(float64); int(got) != effectWindowSize {
		t.Fatalf("payload closed = %v, want %d", payload["closed"], effectWindowSize)
	}
	metaKey, _ := h.engine.source.targetMetaKey(targetID)
	auth, _ := op["authContext"].(map[string]any)
	if got, _ := auth["target"].(string); got != metaKey {
		t.Fatalf("authContext.target = %v, want the target's meta vertex %q", auth["target"], metaKey)
	}
	hint, _ := op["contextHint"].(map[string]any)
	reads, _ := hint["reads"].([]any)
	if len(reads) != 1 || reads[0] != metaKey {
		t.Fatalf("contextHint.reads = %v, want just the target meta vertex", reads)
	}

	// The 21st close: the window is already all-closed, so the emission rule is
	// satisfied again and only the latch stops it.
	if creditOneClose(t, ctx, h, targetID, entityID, ga) {
		// A release either way; what matters is that nothing was published.
		_ = true
	}
	h.requireNoOp(t)
}

// TestPromotion_WindowWithAnOpenEpisodeNeverEmits: the evidence is "this action
// has never once failed to close, across a whole window" — one still-pending
// episode withdraws it, even at a full window.
func TestPromotion_WindowWithAnOpenEpisodeNeverEmits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const targetID = "fixPromoOpen"
	h, ga := promotionHarness(t, ctx, targetID, true)
	entityID := testNanoID(t)

	// A FULL window with two pending slots: one close leaves one open.
	seedEffectWindow(t, ctx, h.conn, targetID, goalLegGap, "legA", effectWindowSize-2, 2)
	if !creditOneClose(t, ctx, h, targetID, entityID, ga) {
		t.Fatal("the leg release must be taken")
	}
	h.requireNoOp(t)
}

// TestPromotion_ShortWindowNeverEmits: a window that has not yet reached its
// full size is not enough evidence, however clean it is — K−1 all-closed
// episodes emit nothing.
func TestPromotion_ShortWindowNeverEmits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const targetID = "fixPromoShort"
	h, ga := promotionHarness(t, ctx, targetID, true)
	entityID := testNanoID(t)

	seedEffectWindow(t, ctx, h.conn, targetID, goalLegGap, "legA", effectWindowSize-2, 1)
	if !creditOneClose(t, ctx, h, targetID, entityID, ga) {
		t.Fatal("the leg release must be taken")
	}
	h.requireNoOp(t)
}

// TestPromotion_ResetConfidenceDoesNotReopenTheEmission: `resetConfidence`
// deletes the target's windows, so a fresh all-closed window can be rebuilt —
// and it still emits nothing, because the recommendation for this triple was
// already made. Two independent things say so: the in-memory latch, and the
// deterministic requestId + create-only handle, which collapse a duplicate that
// slips past it.
func TestPromotion_ResetConfidenceDoesNotReopenTheEmission(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const targetID = "fixPromoReset"
	h, ga := promotionHarness(t, ctx, targetID, true)
	entityID := testNanoID(t)

	seedEffectWindow(t, ctx, h.conn, targetID, goalLegGap, "legA", effectWindowSize-1, 1)
	if !creditOneClose(t, ctx, h, targetID, entityID, ga) {
		t.Fatal("the leg release must be taken")
	}
	if op := h.nextOp(t); op["operationType"] != opRecordPromotionProposal {
		t.Fatalf("first op = %v, want the promotion", op["operationType"])
	}

	if _, err := h.engine.marks.deleteEffectWindows(ctx, targetID); err != nil {
		t.Fatalf("resetConfidence's window delete: %v", err)
	}
	seedEffectWindow(t, ctx, h.conn, targetID, goalLegGap, "legA", effectWindowSize-1, 1)
	if !creditOneClose(t, ctx, h, targetID, entityID, ga) {
		t.Fatal("the second leg release must be taken")
	}
	h.requireNoOp(t)
}

// TestPromotion_ModeAbsentTargetNeverEmits: only a planned-mode target DERIVES
// its legs, so only it has a derived leg to recommend declaring. A target
// running its authored playbook is already declaring what it wants.
func TestPromotion_ModeAbsentTargetNeverEmits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const targetID = "fixPromoUnplan"
	h, ga := promotionHarness(t, ctx, targetID, false)
	entityID := testNanoID(t)

	seedEffectWindow(t, ctx, h.conn, targetID, goalLegGap, "legA", effectWindowSize-1, 1)
	if !creditOneClose(t, ctx, h, targetID, entityID, ga) {
		t.Fatal("the leg release must be taken")
	}
	h.requireNoOp(t)
}

// TestPromotion_SweepObservedCloseEmits: the close that completes the window is
// observed by the SWEEP, not by lane 1 — the row went quiet the moment its gap
// closed, so no further delivery is coming and the sweep's gapClosed mark-clear
// is the only leg that will ever credit this episode. The recommendation the
// close earned must be proposed from there too, or it waits on a delivery that
// never arrives.
func TestPromotion_SweepObservedCloseEmits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixPromoSweep"
	registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
	entityID := testNanoID(t)

	seedEffectWindow(t, ctx, h.conn, targetID, goalLegGap, "legA", effectWindowSize-1, 1)
	// A live-lease mark over legA, and a row whose gap column now reads FALSE:
	// the gap closed, and this is the close lane 1 never saw.
	h.putMark(t, ctx, markKey(targetID, entityID, goalLegGap),
		fixtureMark(targetID, entityID, goalLegGap, "legA", futureLease()))
	h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, 0, map[string]any{goalLegGap: false}))

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != opRecordPromotionProposal {
		t.Fatalf("op = %v, want %s — the sweep is the only leg that observes a quiet row's close",
			op["operationType"], opRecordPromotionProposal)
	}
	if op["requestId"] != derivePromotionRequestID(targetID, goalLegGap, "legA") {
		t.Fatalf("requestId = %v, want the per-triple deterministic id", op["requestId"])
	}
	if h.markExists(t, ctx, markKey(targetID, entityID, goalLegGap)) {
		t.Fatal("precondition: the sweep's gapClosed leg must have cleared the mark")
	}
	h.requireNoOp(t)
}
