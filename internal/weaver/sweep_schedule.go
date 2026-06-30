package weaver

import (
	"context"
	"fmt"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// The reconciler sweep cadence is a durable NATS `@every` schedule rather than an
// in-process ticker: one schedule message fires once per interval into
// core-schedules, a single fixed durable picks each occurrence up, and the
// cadence survives restart, is operator-visible, and fires exactly once across
// replicas. This is the §10.4 temporal lane's recurring half — the @at one-shot
// leg's cron-killing twin (Contract #10 §10.4, brainstorm #47).
const (
	// sweepConsumerName is the fixed durable that runs one reconcile pass per
	// fired @every occurrence.
	sweepConsumerName = "weaver-sweep"
	// sweepScheduleSubject carries the recurring schedule message; re-publishing
	// it replaces the prior schedule (per-subject rollup). It is a singleton
	// platform sweep, so it carries no entity token.
	sweepScheduleSubject = "schedule.weaver.sweep"
	// sweepFiredSubject is the republish target the server fires each occurrence
	// to; it lies within schedule.> per §10.4 and outside the temporal lane's
	// schedule.weaver.timer.fired.> filter, so the two consumers never overlap.
	sweepFiredSubject = "schedule.weaver.sweep.fired"
	// minSweepScheduleInterval is the floor the recurring arm clamps to: NATS
	// message-schedules cannot fire sub-second (substrate.ScheduleEvery enforces
	// the same 1s floor). The production SweepInterval (1 min) is far above it; a
	// sub-second interval is only ever set by aggressive tests, where 1s is still
	// fast enough — and the @every cadence simply cannot go below this floor.
	minSweepScheduleInterval = time.Second
)

// sweepSpec describes the recurring-sweep fire consumer: a fixed durable on
// core-schedules filtered to the @every sweep's fired subject. MaxAckPending is
// 1 so one replica never self-overlaps a pass — preserving the in-process
// ticker's single-goroutine serialization under the durable (concurrent passes
// across replicas remain OCC-safe, the standing §10.3 invariant).
func (e *Engine) sweepSpec() substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          sweepConsumerName,
		Stream:        e.cfg.CoreSchedulesStream,
		FilterSubject: sweepFiredSubject,
		DeliverPolicy: substrate.DeliverAll,
		MaxAckPending: 1,
		Handler:       supervisedHandler(e.handleSweepFired),
		Health:        newConsumerHealthSink(e.conn, e.cfg.HealthKVBucket, e.cfg.Instance, sweepConsumerName, e.states),
		Logger:        e.logger,
	}
}

// handleSweepFired runs one reconciler pass per fired @every occurrence. A pass
// is a level-reconcile over current weaver-state marks, so a redelivered
// occurrence is one harmless extra pass — the handler Acks unconditionally
// (idempotent by construction; no per-occurrence dedup needed, the §10.4
// requestId rule is moot for a handler that drives no op directly).
func (e *Engine) handleSweepFired(ctx context.Context, _ substrate.Message) substrate.Decision {
	e.sweep.pass(ctx)
	return substrate.Ack
}

// armSweepSchedule publishes the recurring @every sweep schedule. It is armed on
// every engine start and is idempotent — a restart, an interval change, or a
// second replica all converge to one schedule (per-subject rollup). A sub-second
// SweepInterval is clamped up to the @every 1s floor (the warm pass still runs at
// start, so a cold start is not delayed; only the recurring cadence is bounded).
func (e *Engine) armSweepSchedule(ctx context.Context) error {
	interval := e.cfg.SweepInterval
	if interval < minSweepScheduleInterval {
		e.logger.Warn("weaver: SweepInterval below the @every 1s floor; arming the recurring sweep at the floor",
			"sweepInterval", interval, "floor", minSweepScheduleInterval)
		interval = minSweepScheduleInterval
	}
	if err := e.conn.ScheduleEvery(ctx, sweepScheduleSubject, sweepFiredSubject, interval, nil); err != nil {
		return fmt.Errorf("arm recurring sweep schedule: %w", err)
	}
	return nil
}
