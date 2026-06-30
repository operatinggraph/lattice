package substrate

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// provisionSchedulesStream mirrors the bootstrap's core-schedules provisioning
// (Contract #10 §10.4): AllowMsgSchedules enables @at/@every and the server
// auto-adds Nats-Rollup: sub so a schedule subject holds at most one live
// schedule. It deliberately OMITS MaxMsgsPerSubject (which bootstrap adds as an
// extra storage bound) so the replace-on-republish assertion below proves the
// server rollup, not a storage cap.
func provisionSchedulesStream(ctx context.Context, t *testing.T, c *Conn) {
	t.Helper()
	_, err := c.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              "core-schedules",
		Subjects:          []string{"schedule.>"},
		Retention:         jetstream.LimitsPolicy,
		Storage:           jetstream.FileStorage,
		AllowMsgSchedules: true,
	})
	if err != nil {
		t.Fatalf("provision core-schedules: %v", err)
	}
}

// firedConsumer is a durable pull consumer on core-schedules filtered to the
// fired-occurrence subject — fired occurrences are JetStream-stored republishes,
// so they are read off the stream, not as core subscriptions.
func firedConsumer(ctx context.Context, t *testing.T, c *Conn, durable, firedSubject string) jetstream.Consumer {
	t.Helper()
	cons, err := c.JetStream().CreateOrUpdateConsumer(ctx, "core-schedules", jetstream.ConsumerConfig{
		Durable:        durable,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{firedSubject},
	})
	if err != nil {
		t.Fatalf("create fired consumer: %v", err)
	}
	return cons
}

// drainFired acks every currently-stored occurrence the consumer can see,
// returning once a fetch comes back empty.
func drainFired(t *testing.T, cons jetstream.Consumer) {
	t.Helper()
	for {
		msgs, err := cons.Fetch(50, jetstream.FetchMaxWait(time.Second))
		if err != nil {
			return
		}
		n := 0
		for m := range msgs.Messages() {
			n++
			_ = m.Ack()
		}
		if n == 0 {
			return
		}
	}
}

// TestScheduleEvery_Validation pins the fail-fast guards: an empty subject or
// target, a target equal to the subject (the server would reject it), and a
// sub-second / zero interval (below the server's @every floor) all error BEFORE
// any publish.
func TestScheduleEvery_Validation(t *testing.T) {
	c, ctx := newTestConn(t)
	cases := []struct {
		name, subject, target string
		interval              time.Duration
	}{
		{"empty subject", "", "schedule.x.fired", time.Second},
		{"empty target", "schedule.x", "", time.Second},
		{"target equals subject", "schedule.x", "schedule.x", time.Second},
		{"sub-second interval", "schedule.x", "schedule.x.fired", 500 * time.Millisecond},
		{"zero interval", "schedule.x", "schedule.x.fired", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.ScheduleEvery(ctx, tc.subject, tc.target, tc.interval, []byte("x")); err == nil {
				t.Fatalf("ScheduleEvery(%q,%q,%s) = nil, want a validation error", tc.subject, tc.target, tc.interval)
			}
		})
	}
}

// TestScheduleEvery_FiresRepeatedlyAndReplaces is the live proof against the
// embedded NATS 2.14 scheduler: an armed @every schedule fires repeatedly, each
// fired occurrence is stamped with the originating schedule subject, and
// re-arming the same subject REPLACES the prior schedule (the server rolls a
// schedule message up on its subject) rather than accumulating a second one.
func TestScheduleEvery_FiresRepeatedlyAndReplaces(t *testing.T) {
	c, ctx := newTestConn(t)
	provisionSchedulesStream(ctx, t, c)

	const subject = "schedule.test.tick"
	const target = "schedule.test.tick.fired"

	cons := firedConsumer(ctx, t, c, "test-tick-fired", target)

	if err := c.ScheduleEvery(ctx, subject, target, time.Second, []byte(`{"tick":true}`)); err != nil {
		t.Fatalf("ScheduleEvery: %v", err)
	}

	// At least two occurrences proves "recurring", within a generous window
	// (the first fire can be up to ~2s out; @every rounds to whole seconds).
	got := 0
	var first jetstream.Msg
	deadline := time.Now().Add(20 * time.Second)
	for got < 2 && time.Now().Before(deadline) {
		msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			continue
		}
		for m := range msgs.Messages() {
			got++
			if first == nil {
				first = m
			}
			_ = m.Ack()
		}
	}
	if got < 2 {
		t.Fatalf("recurring schedule fired %d times in the window, want >= 2", got)
	}
	if s := first.Headers().Get(SchedulerHeader); s != subject {
		t.Errorf("fired occurrence %s header = %q, want the schedule subject %q", SchedulerHeader, s, subject)
	}

	// Re-arm the SAME subject: the server's per-subject rollup keeps exactly one
	// schedule message on the subject (replace, not append).
	if err := c.ScheduleEvery(ctx, subject, target, time.Second, []byte(`{"tick":2}`)); err != nil {
		t.Fatalf("re-arm ScheduleEvery: %v", err)
	}
	stream, err := c.JetStream().Stream(ctx, "core-schedules")
	if err != nil {
		t.Fatalf("get stream: %v", err)
	}
	si, err := stream.Info(ctx, jetstream.WithSubjectFilter(subject))
	if err != nil {
		t.Fatalf("stream info: %v", err)
	}
	if n := si.State.Subjects[subject]; n != 1 {
		t.Fatalf("schedule subject %q holds %d messages after re-arm, want 1 (rollup replace)", subject, n)
	}
}

// TestCancelSchedule proves CancelSchedule stops the re-firing (purges the
// schedule subject) and is idempotent on a subject with no live schedule —
// both the no-stream branch (a subject bound to no stream) and the
// purge-no-messages branch (an already-cancelled subject).
func TestCancelSchedule(t *testing.T) {
	c, ctx := newTestConn(t)
	provisionSchedulesStream(ctx, t, c)

	const subject = "schedule.test.cancel"
	const target = "schedule.test.cancel.fired"

	cons := firedConsumer(ctx, t, c, "test-cancel-fired", target)

	if err := c.ScheduleEvery(ctx, subject, target, time.Second, []byte(`{}`)); err != nil {
		t.Fatalf("ScheduleEvery: %v", err)
	}
	// Confirm it is live before cancelling.
	live := false
	deadline := time.Now().Add(15 * time.Second)
	for !live && time.Now().Before(deadline) {
		msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
		if err != nil {
			continue
		}
		for m := range msgs.Messages() {
			live = true
			_ = m.Ack()
		}
	}
	if !live {
		t.Fatalf("schedule never fired before cancel")
	}

	if err := c.CancelSchedule(ctx, subject); err != nil {
		t.Fatalf("CancelSchedule: %v", err)
	}
	// Let any occurrence generated just before the purge settle, then drain the
	// backlog so the silence assertion below only sees NEW fires.
	time.Sleep(2 * time.Second)
	drainFired(t, cons)

	// After cancel + drain, no NEW occurrence may arrive for a multi-interval window.
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(4*time.Second))
	if err == nil {
		for m := range msgs.Messages() {
			_ = m.Ack()
			t.Fatalf("schedule fired after cancel: %s", string(m.Data()))
		}
	}

	// Idempotent — no-stream branch: a subject bound to no stream.
	if err := c.CancelSchedule(ctx, "unbound.subject.xyz"); err != nil {
		t.Fatalf("CancelSchedule(unbound) = %v, want nil (idempotent)", err)
	}
	// Idempotent — purge-no-messages branch: an already-cancelled in-stream subject.
	if err := c.CancelSchedule(ctx, subject); err != nil {
		t.Fatalf("CancelSchedule(already-cancelled) = %v, want nil (idempotent)", err)
	}
}

// TestDeriveScheduleOccurrenceRequestID pins the per-occurrence dedup contract:
// deterministic, second-granular (sub-second jitter within one occurrence
// collapses), distinct per occurrence and per schedule subject, and a valid
// 20-char NanoID requestId.
func TestDeriveScheduleOccurrenceRequestID(t *testing.T) {
	const subj = "schedule.weaver.sweep"
	t0 := time.Date(2026, 6, 30, 12, 0, 0, 0, time.UTC)

	id := DeriveScheduleOccurrenceRequestID(subj, t0)

	if id2 := DeriveScheduleOccurrenceRequestID(subj, t0); id2 != id {
		t.Fatalf("non-deterministic: %q vs %q", id, id2)
	}
	// Sub-second jitter within the same occurrence second → same id (the @every
	// server rounds occurrences to whole seconds; a redelivery must collapse).
	if idJitter := DeriveScheduleOccurrenceRequestID(subj, t0.Add(900*time.Millisecond)); idJitter != id {
		t.Fatalf("same-second occurrence derived a different id: %q vs %q", idJitter, id)
	}
	// A different timezone representation of the SAME instant → same id (UTC-normalized).
	if idTZ := DeriveScheduleOccurrenceRequestID(subj, t0.In(time.FixedZone("X", 3600))); idTZ != id {
		t.Fatalf("same instant in another zone derived a different id: %q vs %q", idTZ, id)
	}
	// A distinct occurrence (next second) → distinct id.
	if idNext := DeriveScheduleOccurrenceRequestID(subj, t0.Add(time.Second)); idNext == id {
		t.Fatalf("distinct occurrence collapsed to the same id: %q", idNext)
	}
	// A different schedule subject → distinct id.
	if idOther := DeriveScheduleOccurrenceRequestID("schedule.other.sweep", t0); idOther == id {
		t.Fatalf("different subject collapsed to the same id: %q", idOther)
	}
	if len(id) != NanoIDLength {
		t.Fatalf("requestId %q len = %d, want %d", id, len(id), NanoIDLength)
	}
}
