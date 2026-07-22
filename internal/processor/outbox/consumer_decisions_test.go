package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// quietLogger returns the warn-level stderr logger these tests hand to the
// consumer, matching the level the harness uses elsewhere in the package.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// decisionName renders a substrate.Decision so a wrong verdict fails with the
// named decision, not a bare integer.
func decisionName(d substrate.Decision) string {
	switch d {
	case substrate.Ack:
		return "Ack"
	case substrate.Nak:
		return "Nak"
	case substrate.Term:
		return "Term"
	case substrate.NakWithDelay:
		return "NakWithDelay"
	}
	return fmt.Sprintf("Decision(%d)", int(d))
}

// mustPanic asserts fn panics with exactly the wanted value, so a guard test
// cannot pass on some unrelated panic (e.g. a nil dereference further in).
func mustPanic(t *testing.T, want string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic %q, got no panic", want)
		}
		if s, ok := r.(string); !ok || s != want {
			t.Fatalf("panic = %v (%T), want %q", r, r, want)
		}
	}()
	fn()
}

// connectBare starts a fresh embedded server + conn with NO streams or buckets
// provisioned, so a test can provision exactly the half it needs and force the
// other half's substrate calls to fail.
func connectBare(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	url := startEmbeddedNATS(t)
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "outbox-decisions-test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(conn.Close)
	return ctx, conn
}

// provisionCoreKV creates only the Core KV bucket (no core-events stream).
func provisionCoreKV(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	if _, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: testBucket}); err != nil {
		t.Fatalf("create core-kv: %v", err)
	}
}

// provisionCoreEvents creates only the core-events stream (no Core KV bucket).
func provisionCoreEvents(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	if _, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:               "core-events",
		Subjects:           []string{"events.>"},
		Retention:          jetstream.LimitsPolicy,
		MaxAge:             7 * 24 * time.Hour,
		AllowAtomicPublish: true,
	}); err != nil {
		t.Fatalf("provision core-events: %v", err)
	}
}

// outboxMsg wraps an aspect body in the Message shape RunDurableConsumer
// delivers for that aspect's KV entry ($KV.<bucket>.<key> subject).
func outboxMsg(requestID string, body []byte) substrate.Message {
	return substrate.Message{
		Subject: "$KV." + testBucket + "." + processor.OutboxAspectKey(requestID),
		Body:    body,
	}
}

// marshaledAspect builds + marshals an outbox aspect exactly as persistOutbox
// stores it, for handing to handle directly.
func marshaledAspect(t *testing.T, requestID string, events processor.EventList) []byte {
	t.Helper()
	aspect := processor.NewOutboxAspect(requestID, "vtx.identity.actor", processor.TrackerKey(requestID),
		substrate.FormatTimestamp(time.Now()), events)
	b, err := aspect.Marshal()
	if err != nil {
		t.Fatalf("marshal aspect: %v", err)
	}
	return b
}

// TestNew_Validation covers New's guard panics and its nil-logger default +
// the JetStream config it derives from the bucket name (stream, filter,
// subject prefix) — the wiring Run hands to RunDurableConsumer.
func TestNew_Validation(t *testing.T) {
	t.Parallel()

	t.Run("nil conn panics", func(t *testing.T) {
		mustPanic(t, "outbox: New requires Conn", func() {
			New(nil, testBucket, quietLogger())
		})
	})

	t.Run("empty bucket panics", func(t *testing.T) {
		mustPanic(t, "outbox: New requires coreKVBucket", func() {
			New(&substrate.Conn{}, "", quietLogger())
		})
	})

	t.Run("nil logger defaults and config is derived from the bucket", func(t *testing.T) {
		c := New(&substrate.Conn{}, testBucket, nil)
		if c.logger == nil {
			t.Fatal("nil logger must default, got nil on the consumer")
		}
		if c.streamName != "KV_"+testBucket {
			t.Errorf("streamName = %q, want %q", c.streamName, "KV_"+testBucket)
		}
		if c.filterSubj != "$KV."+testBucket+".vtx.op.*.events" {
			t.Errorf("filterSubj = %q, want %q", c.filterSubj, "$KV."+testBucket+".vtx.op.*.events")
		}
		if c.subjectPrefx != "$KV."+testBucket+"." {
			t.Errorf("subjectPrefx = %q, want %q", c.subjectPrefx, "$KV."+testBucket+".")
		}
		if c.bucket != testBucket {
			t.Errorf("bucket = %q, want %q", c.bucket, testBucket)
		}
		if c.publisher == nil {
			t.Fatal("publisher not constructed")
		}
		if c.publisher.Logger != c.logger {
			t.Error("publisher must receive the defaulted logger, got a different one")
		}
	})
}

// TestNewEventPublisher_Validation covers the publisher constructor's guard
// panic and its defaults — including the documented invariant that the
// backoff schedule is at least MaxRetries long.
func TestNewEventPublisher_Validation(t *testing.T) {
	t.Parallel()

	t.Run("nil conn panics", func(t *testing.T) {
		mustPanic(t, "outbox: NewEventPublisher requires Conn", func() {
			NewEventPublisher(nil, quietLogger())
		})
	})

	t.Run("nil logger defaults with retry config intact", func(t *testing.T) {
		p := NewEventPublisher(&substrate.Conn{}, nil)
		if p.Logger == nil {
			t.Fatal("nil logger must default, got nil")
		}
		if p.MaxRetries <= 0 {
			t.Errorf("MaxRetries = %d, want > 0", p.MaxRetries)
		}
		if len(p.BackoffSchedule) < p.MaxRetries {
			t.Errorf("BackoffSchedule has %d entries, want >= MaxRetries (%d)",
				len(p.BackoffSchedule), p.MaxRetries)
		}
		if p.Timeout <= 0 {
			t.Errorf("Timeout = %v, want > 0", p.Timeout)
		}
		if p.Clock == nil {
			t.Error("Clock must default, got nil")
		}
	})
}

// TestPublicationError_MessageAndUnwrap pins the typed failure's rendering and
// its unwrap chain (errors.Is must reach the underlying substrate error).
func TestPublicationError_MessageAndUnwrap(t *testing.T) {
	t.Parallel()
	inner := errors.New("stream missing")
	pe := &PublicationError{
		EventClass: "identity.created",
		Subject:    "events.identity.created",
		Attempts:   3,
		LastErr:    inner,
	}
	msg := pe.Error()
	for _, want := range []string{"identity.created", "events.identity.created", "attempts=3", "stream missing"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, missing %q", msg, want)
		}
	}
	if !errors.Is(pe, inner) {
		t.Error("errors.Is(pe, inner) = false, want true via Unwrap")
	}
}

// TestHandle_EmptyBodyAcked: a tombstone/PURGE/TTL marker (empty body) is
// acked and skipped. The consumer holds a zero-value Conn, so the skip must
// decide BEFORE any substrate call — a regression that reaches for the conn
// panics and fails the test loudly.
func TestHandle_EmptyBodyAcked(t *testing.T) {
	t.Parallel()
	c := New(&substrate.Conn{}, testBucket, quietLogger())
	for _, body := range [][]byte{nil, {}} {
		dec := c.handle(context.Background(), outboxMsg("Gj4kPmRtw9nbCxz5vQ2y", body))
		if dec != substrate.Ack {
			t.Fatalf("empty body (len %d): got %s, want Ack", len(body), decisionName(dec))
		}
	}
}

// TestHandle_ValidAspect_PublishesTombstonesAcks is the positive sibling for
// the decision tests below: through the same direct-handle seam, a valid
// persisted aspect publishes the faithful event, tombstones the aspect, and
// acks. This proves the negative vectors diverge for their own reasons, not
// because the seam never publishes.
func TestHandle_ValidAspect_PublishesTombstonesAcks(t *testing.T) {
	t.Parallel()
	ctx, conn := setup(t)
	rid := "Jj4kPmRtw9nbCxz5vQ2y"
	ev := processor.Event{
		EventID:   "Kj4kPmRtw9nbCxz5vQ2y",
		RequestID: rid,
		EventType: "identity.created",
		TargetKey: "vtx.identity.Mj4kPmRtw9nbCxz5vQ2y",
		Payload:   map[string]interface{}{"name": "Andrew"},
		Timestamp: substrate.FormatTimestamp(time.Now()),
	}
	body := marshaledAspect(t, rid, processor.EventList{ev})
	if _, err := conn.KVPut(ctx, testBucket, processor.OutboxAspectKey(rid), body); err != nil {
		t.Fatalf("put aspect: %v", err)
	}

	c := New(conn, testBucket, quietLogger())
	if dec := c.handle(ctx, outboxMsg(rid, body)); dec != substrate.Ack {
		t.Fatalf("valid aspect: got %s, want Ack", decisionName(dec))
	}

	got := drainEvents(t, ctx, conn, "handle-valid-events")
	if len(got) != 1 {
		t.Fatalf("got %d events on core-events, want 1", len(got))
	}
	wantJSON, _ := json.Marshal(ev)
	if string(got[0]) != string(wantJSON) {
		t.Fatalf("event not byte-identical:\n got=%s\nwant=%s", got[0], wantJSON)
	}
	if _, err := conn.KVGet(ctx, testBucket, processor.OutboxAspectKey(rid)); !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("aspect should be tombstoned after publish, got err=%v", err)
	}
}

// TestHandle_PoisonAspect_Termed: a structurally invalid aspect body is
// terminated — specifically Term, because a Nak would redeliver the same
// unparseable record forever and an Ack would drop it without the loud log —
// and nothing reaches core-events.
func TestHandle_PoisonAspect_Termed(t *testing.T) {
	t.Parallel()
	ctx, conn := setup(t)
	c := New(conn, testBucket, quietLogger())

	dec := c.handle(ctx, outboxMsg("Nj4kPmRtw9nbCxz5vQ2y", []byte(`{"key": truncated-not-json`)))
	if dec != substrate.Term {
		t.Fatalf("poison aspect: got %s, want Term (Nak would hot-loop redelivery; Ack would silently drop)", decisionName(dec))
	}
	if got := drainEvents(t, ctx, conn, "handle-poison-events"); len(got) != 0 {
		t.Fatalf("poison aspect published %d events, want 0", len(got))
	}
}

// TestHandle_DeletedOrEventlessAspectAcked: a tombstoned aspect (isDeleted,
// even with events still in the body) and an eventless aspect are acked
// without publishing anything.
func TestHandle_DeletedOrEventlessAspectAcked(t *testing.T) {
	t.Parallel()
	ctx, conn := setup(t)
	c := New(conn, testBucket, quietLogger())

	ridDeleted := "Pj4kPmRtw9nbCxz5vQ2y"
	aspect := processor.NewOutboxAspect(ridDeleted, "vtx.identity.actor", processor.TrackerKey(ridDeleted),
		substrate.FormatTimestamp(time.Now()), processor.EventList{{
			EventID: "Qj4kPmRtw9nbCxz5vQ2y", RequestID: ridDeleted, EventType: "identity.created",
			Payload: map[string]interface{}{}, Timestamp: substrate.FormatTimestamp(time.Now()),
		}})
	aspect.IsDeleted = true
	deletedBody, err := aspect.Marshal()
	if err != nil {
		t.Fatalf("marshal deleted aspect: %v", err)
	}
	if dec := c.handle(ctx, outboxMsg(ridDeleted, deletedBody)); dec != substrate.Ack {
		t.Fatalf("isDeleted aspect: got %s, want Ack", decisionName(dec))
	}

	ridEventless := "Rj4kPmRtw9nbCxz5vQ2y"
	if dec := c.handle(ctx, outboxMsg(ridEventless, marshaledAspect(t, ridEventless, processor.EventList{}))); dec != substrate.Ack {
		t.Fatalf("eventless aspect: got %s, want Ack", decisionName(dec))
	}

	if got := drainEvents(t, ctx, conn, "handle-skip-events"); len(got) != 0 {
		t.Fatalf("skip vectors published %d events, want 0", len(got))
	}
}

// TestHandle_PublishFailureNaksAndRetainsAspect: when the publish to
// core-events fails (stream absent), the decision is specifically Nak — Term
// would drop committed events (event loss), Ack would confirm an unpublished
// record — and the aspect stays in Core KV so redelivery can re-publish from
// the durable record.
func TestHandle_PublishFailureNaksAndRetainsAspect(t *testing.T) {
	t.Parallel()
	ctx, conn := connectBare(t)
	provisionCoreKV(t, ctx, conn) // core-events intentionally NOT provisioned

	rid := "Sj4kPmRtw9nbCxz5vQ2y"
	body := marshaledAspect(t, rid, processor.EventList{{
		EventID: "Tj4kPmRtw9nbCxz5vQ2y", RequestID: rid, EventType: "identity.created",
		Payload: map[string]interface{}{"k": "v"}, Timestamp: substrate.FormatTimestamp(time.Now()),
	}})
	if _, err := conn.KVPut(ctx, testBucket, processor.OutboxAspectKey(rid), body); err != nil {
		t.Fatalf("put aspect: %v", err)
	}

	c := New(conn, testBucket, quietLogger())
	c.publisher.BackoffSchedule = []time.Duration{0, 0, 0}
	c.publisher.MaxRetries = 2

	if dec := c.handle(ctx, outboxMsg(rid, body)); dec != substrate.Nak {
		t.Fatalf("publish failure: got %s, want Nak (Term would lose committed events; Ack would confirm an unpublished record)", decisionName(dec))
	}
	if _, err := conn.KVGet(ctx, testBucket, processor.OutboxAspectKey(rid)); err != nil {
		t.Fatalf("aspect must remain for redelivery after a failed publish, got err=%v", err)
	}
}

// TestHandle_TombstoneFailureToleratedStillAcks: after a successful publish, a
// failing tombstone delete (bucket absent) is tolerated — the decision stays
// Ack, because a Nak would re-publish already-delivered events on every
// redelivery — and the event did land on core-events.
func TestHandle_TombstoneFailureToleratedStillAcks(t *testing.T) {
	t.Parallel()
	ctx, conn := connectBare(t)
	provisionCoreEvents(t, ctx, conn) // Core KV bucket intentionally NOT provisioned

	rid := "Uj4kPmRtw9nbCxz5vQ2y"
	ev := processor.Event{
		EventID: "Vj4kPmRtw9nbCxz5vQ2y", RequestID: rid, EventType: "identity.created",
		Payload: map[string]interface{}{"k": "v"}, Timestamp: substrate.FormatTimestamp(time.Now()),
	}
	c := New(conn, testBucket, quietLogger())

	if dec := c.handle(ctx, outboxMsg(rid, marshaledAspect(t, rid, processor.EventList{ev}))); dec != substrate.Ack {
		t.Fatalf("tombstone failure after successful publish: got %s, want Ack (Nak would re-publish delivered events)", decisionName(dec))
	}
	got := drainEvents(t, ctx, conn, "handle-tombfail-events")
	if len(got) != 1 {
		t.Fatalf("got %d events on core-events, want 1 (publish succeeded before the failed tombstone)", len(got))
	}
	wantJSON, _ := json.Marshal(ev)
	if string(got[0]) != string(wantJSON) {
		t.Fatalf("event not byte-identical:\n got=%s\nwant=%s", got[0], wantJSON)
	}
}
