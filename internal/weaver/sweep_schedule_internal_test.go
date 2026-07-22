package weaver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestSweepSchedule_ArmPublishesRecurringSchedule proves the cron-kill arm: the
// engine publishes a single @every recurring schedule at schedule.weaver.sweep
// targeting schedule.weaver.sweep.fired, and re-arming is idempotent (per-subject
// rollup keeps exactly one live schedule) — so a restart / interval change /
// second replica all converge to one schedule.
func TestSweepSchedule_ArmPublishesRecurringSchedule(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)
	provisionSchedules(t, ctx, h.conn)

	if err := h.engine.armSweepSchedule(ctx); err != nil {
		t.Fatalf("armSweepSchedule: %v", err)
	}

	msg := scheduleMsg(t, ctx, h.conn, sweepScheduleSubject)
	if msg == nil {
		t.Fatal("arm must publish a schedule message at " + sweepScheduleSubject)
	}
	if got := msg.Header.Get(substrate.ScheduleHeader); !strings.HasPrefix(got, "@every ") {
		t.Fatalf("schedule header = %q, want an @every recurring spec", got)
	}
	if got := msg.Header.Get(substrate.ScheduleTargetHeader); got != sweepFiredSubject {
		t.Fatalf("schedule target = %q, want %q", got, sweepFiredSubject)
	}

	// Re-arm: the rollup keeps one live schedule at the same subject (no second
	// schedule, no divergent target).
	firstSeq := msg.Sequence
	if err := h.engine.armSweepSchedule(ctx); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	again := scheduleMsg(t, ctx, h.conn, sweepScheduleSubject)
	if again == nil {
		t.Fatal("re-arm must leave one live schedule at " + sweepScheduleSubject)
	}
	if again.Sequence <= firstSeq {
		t.Fatalf("re-arm must replace the schedule (new seq %d > %d)", again.Sequence, firstSeq)
	}
	if got := again.Header.Get(substrate.ScheduleTargetHeader); got != sweepFiredSubject {
		t.Fatalf("re-armed target = %q, want %q", got, sweepFiredSubject)
	}
}

// TestHandleSweepFired_RunsReconcilePass proves a fired @every occurrence drives
// exactly one reconcile pass: an expired-lease mark is reclaimed (op emitted,
// metrics incremented) and the handler Acks — the durable schedule replaces the
// in-process ticker with no change to the sweep logic.
func TestHandleSweepFired_RunsReconcilePass(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureFiredSweep"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	dec := h.engine.handleSweepFired(ctx, substrate.Message{
		Subject: sweepFiredSubject, Sequence: 1, NumDelivered: 1,
	})
	if dec != substrate.Ack {
		t.Fatalf("handleSweepFired must Ack a fired occurrence, got %v", dec)
	}

	if op := h.nextOp(t); op["requestId"] == nil {
		t.Fatal("the fired sweep must emit a reclaim op for the expired mark")
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 1 {
		t.Fatalf("sweepReclaims = %d, want 1 (the fired occurrence ran one pass)", reclaims)
	}
}

// TestSweepSpec_Shape pins the durable config the cron-kill relies on: the fixed
// weaver-sweep durable binds core-schedules, filters only the sweep's fired
// subject, and caps MaxAckPending at 1 so one replica never self-overlaps a pass.
func TestSweepSpec_Shape(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	spec := h.engine.sweepSpec()
	if spec.Name != sweepConsumerName {
		t.Fatalf("sweep durable name = %q, want %q", spec.Name, sweepConsumerName)
	}
	if spec.Stream != h.engine.cfg.CoreSchedulesStream {
		t.Fatalf("sweep durable stream = %q, want %q", spec.Stream, h.engine.cfg.CoreSchedulesStream)
	}
	if spec.FilterSubject != sweepFiredSubject {
		t.Fatalf("sweep durable filter = %q, want only %q", spec.FilterSubject, sweepFiredSubject)
	}
	if spec.MaxAckPending != 1 {
		t.Fatalf("sweep durable MaxAckPending = %d, want 1 (no per-replica self-overlap)", spec.MaxAckPending)
	}
	if spec.Handler == nil {
		t.Fatal("sweep durable must set a handler")
	}
}
