package substrate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Schedule-publish header names (Contract #10 §10.4, ADR-51). Publishing to a
// stream provisioned with AllowMsgSchedules and these headers set arms a
// NATS-native scheduled message; the scheduler republishes the payload back
// into the same stream at the target subject when the schedule fires.
//
// Header strings are version-matched to the pinned NATS server (2.14):
// JSSchedulePattern / JSScheduleTarget / JSScheduleTTL / JSScheduler /
// JSScheduleNext in nats-server/v2@v2.14.0 server/stream.go.
const (
	// ScheduleHeader carries the schedule spec — the one-shot absolute form
	// "@at <RFC3339>" or the recurring form "@every <duration>".
	ScheduleHeader = "Nats-Schedule"
	// ScheduleTargetHeader carries the republish target subject. The target
	// MUST lie within the scheduling stream's own subject space and differ from
	// the schedule subject — the server rejects an out-of-stream or
	// self-targeting target at publish time.
	ScheduleTargetHeader = "Nats-Schedule-Target"
	// ScheduleTTLHeader optionally bounds how long a fired occurrence stays
	// valid (an occurrence older than the TTL is discarded, not delivered).
	// Requires the stream's AllowMsgTTL. Opt-in: ScheduleEvery does not set it;
	// it is declared for a consumer that wants stale-occurrence discard.
	ScheduleTTLHeader = "Nats-Schedule-TTL"
	// SchedulerHeader is set BY THE SERVER on each fired occurrence, carrying the
	// schedule subject that produced it. Read off a fired message to recover the
	// originating schedule; never set by a publisher arming a schedule.
	SchedulerHeader = "Nats-Scheduler"
	// ScheduleNextHeader is set BY THE SERVER on each fired occurrence, carrying
	// the next occurrence instant (RFC3339). Read off a fired message. (A
	// publisher may set it only to "purge" to cancel a schedule — CancelSchedule
	// purges the subject instead.)
	ScheduleNextHeader = "Nats-Schedule-Next"
)

// minScheduleInterval is the floor ScheduleEvery enforces on a recurring
// interval. It mirrors the NATS server's own rule (@every requires
// dur.Seconds() >= 1, scheduler.go) — a sub-second schedule would hot-fire —
// surfaced here as an explicit Go error rather than a silent server-side skip.
const minScheduleInterval = time.Second

// Publish sends a single message to subject through JetStream and waits for the
// server's store ack (ctx-bounds the round trip). It is the fire-and-forget
// primitive for command submission where no reply is awaited — the durable
// record of intent lives in the caller's own store (e.g. Loom's loom-state
// outbox), and the outcome is observed off-stream (a committed event, or a
// timeout). header is optional.
//
// This is deliberately a thin wrapper over the JetStream publish so callers
// (e.g. internal/loom) can submit ops without importing nats.go / jetstream
// directly.
func (c *Conn) Publish(ctx context.Context, subject string, data []byte, header map[string]string) error {
	if subject == "" {
		return fmt.Errorf("substrate: Publish: subject required")
	}
	msg := &nats.Msg{Subject: subject, Data: data}
	if len(header) > 0 {
		msg.Header = nats.Header{}
		for k, v := range header {
			msg.Header.Set(k, v)
		}
	}
	if _, err := c.js.PublishMsg(ctx, msg); err != nil {
		return fmt.Errorf("substrate: publish %q: %w", subject, err)
	}
	return nil
}

// PublishCore sends a single message to subject over core NATS — no JetStream
// stream, no store ack. It is the fire-and-forget primitive for ephemeral
// fan-out (e.g. observability metrics) where no durable record is wanted and the
// subject is not stream-backed; use Publish (JetStream) for durable command
// submission. ctx is honoured for cancellation only — a core publish is a local
// buffer enqueue with no server round trip.
func (c *Conn) PublishCore(ctx context.Context, subject string, data []byte) error {
	if subject == "" {
		return fmt.Errorf("substrate: PublishCore: subject required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.nc.Publish(subject, data); err != nil {
		return fmt.Errorf("substrate: publish-core %q: %w", subject, err)
	}
	return nil
}

// ScheduleEvery arms a recurring NATS message schedule on subject: every
// interval the server republishes payload to target. subject is the schedule's
// stable identity; target is where each fired occurrence is delivered and MUST
// lie within the scheduling stream's subject space and differ from subject (the
// server rejects an out-of-stream or self-targeting target at publish time).
//
// Re-publishing the same subject REPLACES the prior schedule: the server rolls a
// schedule message up on its subject (it auto-adds Nats-Rollup: sub), so arming
// is idempotent — a restart or a second instance converges to a single live
// schedule. interval must be a whole, >= 1s duration (the server's own @every
// floor). Requires a stream provisioned with AllowMsgSchedules (core-schedules).
// Stop a schedule with CancelSchedule.
//
// This is a thin wrapper over Publish with the §10.4 schedule headers — no new
// transport, so a consumer never imports nats.go/jetstream to drive a schedule.
func (c *Conn) ScheduleEvery(ctx context.Context, subject, target string, interval time.Duration, payload []byte) error {
	if subject == "" {
		return fmt.Errorf("substrate: ScheduleEvery: subject required")
	}
	if target == "" {
		return fmt.Errorf("substrate: ScheduleEvery: target required")
	}
	if target == subject {
		return fmt.Errorf("substrate: ScheduleEvery: target must differ from subject %q", subject)
	}
	if interval < minScheduleInterval {
		return fmt.Errorf("substrate: ScheduleEvery: interval %s below the %s floor", interval, minScheduleInterval)
	}
	header := map[string]string{
		ScheduleHeader:       "@every " + interval.String(),
		ScheduleTargetHeader: target,
	}
	return c.Publish(ctx, subject, payload, header)
}

// CancelSchedule stops a schedule by purging its schedule subject from the
// backing stream — after it returns the server no longer re-fires that schedule.
// It purges ONLY the schedule subject, never the fired-occurrence target subject
// (already-delivered occurrences are untouched). Idempotent: cancelling a subject
// with no live schedule — already cancelled, never armed, or no stream binds it —
// is a no-op returning nil.
func (c *Conn) CancelSchedule(ctx context.Context, subject string) error {
	if subject == "" {
		return fmt.Errorf("substrate: CancelSchedule: subject required")
	}
	streamName, err := c.js.StreamNameBySubject(ctx, subject)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			// No stream binds the subject → nothing to cancel.
			return nil
		}
		return fmt.Errorf("substrate: CancelSchedule %q: resolve stream: %w", subject, err)
	}
	stream, err := c.js.Stream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("substrate: CancelSchedule %q: open stream %q: %w", subject, streamName, err)
	}
	if err := stream.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
		return fmt.Errorf("substrate: CancelSchedule %q: purge: %w", subject, err)
	}
	return nil
}

// DeriveScheduleOccurrenceRequestID is the deterministic requestId for one fired
// occurrence of a recurring schedule, the @every analog of the @at fired-timer
// derivation (Contract #10 §10.4): it is keyed on the schedule subject + the
// occurrence instant (truncated to whole seconds, the granularity @every rounds
// to), so an at-least-once REDELIVERY of the same stored occurrence derives the
// same id and collapses on the Contract #4 vtx.op.<requestId> tracker, while a
// DISTINCT occurrence (a later tick) is a genuinely new op.
//
// A schedule consumer that drives an op passes the fired message's store
// timestamp (msg.Metadata().Timestamp) as occurrence. A consumer whose fire
// drives no op (a pure level-reconcile handler, e.g. the reconciler sweep) needs
// no requestId — its idempotency is the handler's own property — and need not
// call this.
func DeriveScheduleOccurrenceRequestID(scheduleSubject string, occurrence time.Time) string {
	occ := occurrence.UTC().Truncate(time.Second).Format(time.RFC3339)
	return DeriveNanoID("schedule-occurrence:", scheduleSubject+"\x00"+occ)
}
