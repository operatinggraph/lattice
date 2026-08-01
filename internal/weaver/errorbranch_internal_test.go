package weaver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestFireEpisode_StaleReclaim_DispatchesFreshEpisode drives the lane-1
// found&&stale branch (evaluator.go fireEpisode) end-to-end through
// handleRow: an EXTERNAL gap (a declared inflight_<g> companion, currently
// false — the prior call concluded) whose mark's lease has expired is
// reclaimed in place with a fresh claimId rather than suppressed or
// CAS-created anew. This branch was entirely untested (0% line coverage).
func TestFireEpisode_StaleReclaim_DispatchesFreshEpisode(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureStaleReclaim"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	staleRev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", actionDirectOp, pastLease()))

	row := map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"violating":  true,
		"missing_x":  true,
		"inflight_x": false,
	}
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	msg := substrate.Message{
		Subject:      h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Body:         body,
		Sequence:     7,
		NumDelivered: 1,
	}
	if dec := h.engine.handleRow(ctx, msg); dec != substrate.Ack {
		t.Fatalf("stale-mark reclaim dispatch must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("reclaim must fire a fresh FixX episode, got %v", op["operationType"])
	}

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("mark read after reclaim: %v", err)
	}
	if entry.Revision == staleRev {
		t.Fatalf("reclaim must replace the mark in place with a fresh revision")
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal reclaimed mark: %v", err)
	}
	if rec.ClaimID == "" {
		t.Fatalf("reclaimed mark must carry a freshly-minted claimId")
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != 1 {
		t.Fatalf("reclaim must bump the dispatch-count budget: got %d (err=%v), want 1", got, err)
	}
}

// TestFireEpisode_StaleReclaim_ConflictSkipsDispatch proves fireEpisode's
// stale-mark reclaim is itself OCC-safe: if the mark changed since the
// caller's read (a fresh episode already won), the in-place replace
// conflicts and the caller Acks without a second, duplicate dispatch.
func TestFireEpisode_StaleReclaim_ConflictSkipsDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureStaLeConfLict"
	target := &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	}
	h.seedTarget(target)
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	expired := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, pastLease())
	staleRev := h.putMark(t, ctx, key, expired)

	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "inflight_x": false,
	}
	pl, _, dec := h.engine.planGap(ctx, target, targetID, entityID, "missing_x", target.Gaps["missing_x"], row, 1, "")
	if pl == nil {
		t.Fatalf("planGap must produce a plan, got dec=%v", dec)
	}

	// A fresh episode replaces the mark after this (simulated) read.
	fresh := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, futureLease())
	freshBody, err := json.Marshal(fresh)
	if err != nil {
		t.Fatalf("marshal fresh mark: %v", err)
	}
	freshRev, err := h.conn.KVUpdate(ctx, "weaver-state", key, freshBody, staleRev)
	if err != nil {
		t.Fatalf("replace with fresh mark: %v", err)
	}

	got := h.engine.fireEpisode(ctx, targetID, entityID, "vtx.leaseApp."+entityID, "missing_x", actionDirectOp,
		pl, false, &expired, staleRev, true, true)
	if got != substrate.Ack {
		t.Fatalf("a conflicted stale-reclaim must Ack (the winner already dispatched), got %v", got)
	}
	h.requireNoOp(t)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != freshRev {
		t.Fatalf("the fresh episode's mark must stay intact (err=%v)", err)
	}
}

// TestFireEpisode_StaleReclaim_ReplaceFailureNaksWithDelay proves a hard
// mark-store failure (not a revision conflict) during the in-place reclaim
// naks with delay rather than silently dropping the episode or crashing.
func TestFireEpisode_StaleReclaim_ReplaceFailureNaksWithDelay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.WeaverStateBucket = "phantom-state" })

	const targetID = "fixtureStaleReplaceFail"
	target := &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	}
	h.seedTarget(target)
	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "inflight_x": false,
	}
	pl, _, dec := h.engine.planGap(ctx, target, targetID, entityID, "missing_x", target.Gaps["missing_x"], row, 1, "")
	if pl == nil {
		t.Fatalf("planGap must produce a plan, got dec=%v", dec)
	}
	expired := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, pastLease())

	got := h.engine.fireEpisode(ctx, targetID, entityID, "vtx.leaseApp."+entityID, "missing_x", actionDirectOp,
		pl, false, &expired, 1, true, true)
	if got != substrate.NakWithDelay {
		t.Fatalf("a mark-store failure during reclaim must nak with delay, got %v", got)
	}
	h.requireNoOp(t)
}

// TestFireEpisode_MarkCreateFailureNaksWithDelay proves a hard mark-store
// failure on the lane-1 CAS-create (fresh dispatch, no pre-existing mark)
// naks with delay rather than dispatching blind.
func TestFireEpisode_MarkCreateFailureNaksWithDelay(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.WeaverStateBucket = "phantom-state" })

	const targetID = "fixtureCreateFail"
	target := &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	}
	h.seedTarget(target)
	entityID := testNanoID(t)
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true}
	pl, _, dec := h.engine.planGap(ctx, target, targetID, entityID, "missing_x", target.Gaps["missing_x"], row, 1, "")
	if pl == nil {
		t.Fatalf("planGap must produce a plan, got dec=%v", dec)
	}

	got := h.engine.fireEpisode(ctx, targetID, entityID, "vtx.leaseApp."+entityID, "missing_x", actionDirectOp,
		pl, false, nil, 0, false, false)
	if got != substrate.NakWithDelay {
		t.Fatalf("a mark-create failure must nak with delay, got %v", got)
	}
	h.requireNoOp(t)
}

// TestFireEpisode_MarkCreateLostAcksWithoutDispatch proves the CAS-create
// race: if a concurrent evaluation already created the mark (this
// evaluation's own read genuinely raced, so it computed found=false against
// a now-stale snapshot), the create loses and this side Acks without a
// duplicate dispatch — the winner already fired.
func TestFireEpisode_MarkCreateLostAcksWithoutDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureCreateLost"
	target := &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	}
	h.seedTarget(target)
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	winner := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, futureLease())
	h.putMark(t, ctx, key, winner)

	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true}
	pl, _, dec := h.engine.planGap(ctx, target, targetID, entityID, "missing_x", target.Gaps["missing_x"], row, 1, "")
	if pl == nil {
		t.Fatalf("planGap must produce a plan, got dec=%v", dec)
	}

	got := h.engine.fireEpisode(ctx, targetID, entityID, "vtx.leaseApp."+entityID, "missing_x", actionDirectOp,
		pl, false, nil, 0, false, false)
	if got != substrate.Ack {
		t.Fatalf("a lost CAS-create race must Ack (the winner already dispatched), got %v", got)
	}
	h.requireNoOp(t)
}

// TestBumpDispatchCount_LogsWarnOnFailure proves a corrupt dispatch-count key
// (unmarshal failure) is logged and swallowed, never propagated — the budget
// is a bound, not a dispatch gate.
func TestBumpDispatchCount_LogsWarnOnFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureBumpCountFaiL"
	entityID := testNanoID(t)
	key := countKey(targetID, entityID, "missing_x")
	if _, err := h.conn.KVCreate(ctx, "weaver-state", key, []byte("{not json")); err != nil {
		t.Fatalf("seed corrupt count key: %v", err)
	}

	h.engine.bumpDispatchCount(ctx, targetID, entityID, "missing_x")

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("read count key after failed bump: %v", err)
	}
	if string(entry.Value) != "{not json" {
		t.Fatalf("a failed increment must not modify the corrupt count key, got %q", entry.Value)
	}
}

// TestBumpEffectDispatch_LogsWarnOnFailure proves a corrupt `__effect` window
// key (unmarshal failure) is logged and swallowed, never propagated — the
// confidence window is Fire 5's ranking input, not a dispatch gate.
func TestBumpEffectDispatch_LogsWarnOnFailure(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureBumpEffectFail"
	key := effectKey(targetID, "missing_x", actionDirectOp)
	if _, err := h.conn.KVCreate(ctx, "weaver-state", key, []byte("{not json")); err != nil {
		t.Fatalf("seed corrupt effect key: %v", err)
	}

	h.engine.bumpEffectDispatch(ctx, targetID, "missing_x", actionDirectOp)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("read effect key after failed bump: %v", err)
	}
	if string(entry.Value) != "{not json" {
		t.Fatalf("a failed dispatch record must not modify the corrupt effect key, got %q", entry.Value)
	}
}

// TestSweep_DeleteEffect_ConflictSkipsDelete proves the sweep's `__effect`
// orphan-delete is OCC-safe: a fresh dispatch/close racing the sweep between
// its read and this delete leaves the fresh state intact (re-evaluated next
// pass) instead of destroying it.
func TestSweep_DeleteEffect_ConflictSkipsDelete(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureEffectDeleteConflict"
	key := effectKey(targetID, "missing_x", actionDirectOp)
	staleRev, err := h.conn.KVCreate(ctx, "weaver-state", key, mustMarshalEffectStats(t, effectStats{Window: []bool{false}}))
	if err != nil {
		t.Fatalf("seed effect key: %v", err)
	}

	fresh := mustMarshalEffectStats(t, effectStats{Window: []bool{false, true}})
	freshRev, err := h.conn.KVUpdate(ctx, "weaver-state", key, fresh, staleRev)
	if err != nil {
		t.Fatalf("replace with fresh effect stats: %v", err)
	}

	if h.engine.sweep.deleteEffect(ctx, key, staleRev, sweepReasonOrphanColumn) {
		t.Fatalf("a conflicted delete must be skipped, not succeed")
	}
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != freshRev {
		t.Fatalf("the fresh effect stats must stay intact (err=%v)", err)
	}
}

// TestSweep_DeleteEffect_HardFailureLogsWarn proves a hard KV failure (not a
// revision conflict) deleting an `__effect` key is logged and swallowed —
// deleteEffect reports false either way, and the caller re-evaluates next pass.
func TestSweep_DeleteEffect_HardFailureLogsWarn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.WeaverStateBucket = "phantom-state" })

	const targetID = "fixtureEffectDeleteHardFail"
	key := effectKey(targetID, "missing_x", actionDirectOp)

	if h.engine.sweep.deleteEffect(ctx, key, 1, sweepReasonOrphanColumn) {
		t.Fatalf("a hard KV failure must not report success")
	}
}

// TestReconcileConsumers_AddFailureRaisesIssue proves a failed lane-1
// consumer Add (the target's desired stream unresolvable) raises a
// ConsumerReconcileError issue and leaves e.targets unpopulated, rather than
// silently pretending the consumer is running.
func TestReconcileConsumers_AddFailureRaisesIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newSweepHarness(t, ctx, func(c *Config) { c.WeaverTargetsBucket = "phantom-targets" })
	h.engine.ctx = ctx

	const targetID = "fixtureAddFail"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})

	h.engine.reconcileConsumers()

	if _, ok := h.engine.targets[targetID]; ok {
		t.Fatalf("a failed Add must not populate e.targets")
	}
	if issues := h.engine.issues.snapshot(); !hasIssueCode(issues, "ConsumerReconcileError") {
		t.Fatalf("a failed Add must raise a ConsumerReconcileError issue, got %+v", issues)
	}
}

// TestReconcileConsumers_RemoveFailureRaisesIssue proves a failed lane-1
// consumer Remove (the durable's stream gone out from under it) raises a
// ConsumerReconcileError issue and leaves e.targets untouched (the durable
// may leak until the next reconcile, per the doc comment), rather than
// silently forgetting the target was ever running.
func TestReconcileConsumers_RemoveFailureRaisesIssue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newSweepHarness(t, ctx)
	h.engine.ctx = ctx

	const targetID = "fixtureRemoveFail"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	h.engine.reconcileConsumers()
	if _, ok := h.engine.targets[targetID]; !ok {
		t.Fatalf("setup: Add must succeed and populate e.targets")
	}

	// Unregister the target so the next pass wants it removed, then pull the
	// stream backing the durable out from under the supervisor — Remove's
	// DeleteConsumer call now fails with something other than
	// ErrConsumerNotFound (the tolerated, idempotent case).
	h.engine.source.mu.Lock()
	delete(h.engine.source.targets, targetID)
	h.engine.source.mu.Unlock()
	if err := h.conn.JetStream().DeleteStream(ctx, "KV_weaver-targets"); err != nil {
		t.Fatalf("delete backing stream: %v", err)
	}

	h.engine.reconcileConsumers()

	if _, ok := h.engine.targets[targetID]; !ok {
		t.Fatalf("a failed Remove must leave e.targets[%s] in place (durable presumed still running)", targetID)
	}
	if issues := h.engine.issues.snapshot(); !hasIssueCode(issues, "ConsumerReconcileError") {
		t.Fatalf("a failed Remove must raise a ConsumerReconcileError issue, got %+v", issues)
	}
}
