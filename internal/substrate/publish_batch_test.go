package substrate

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// provisionEventsStream creates a JetStream stream that accepts the
// `events.>` subjects the Processor's outbox consumer publishes onto. We enable
// AllowAtomicPublish to match the production `core-events` provisioning
// (see internal/bootstrap/primordial.go).
func provisionEventsStream(ctx context.Context, t *testing.T, c *Conn) {
	t.Helper()
	js := c.JetStream()
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:               "core-events",
		Subjects:           []string{"events.>"},
		Retention:          jetstream.LimitsPolicy,
		MaxAge:             7 * 24 * time.Hour,
		AllowAtomicPublish: true,
	})
	if err != nil {
		t.Fatalf("create core-events stream: %v", err)
	}
}

// TestPublishBatch_HappyPath publishes three events in one batch and
// asserts all three land on the stream in publish order.
func TestPublishBatch_HappyPath(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionEventsStream(ctx, t, c)

	ops := []PublishOp{
		{Subject: "events.identity.created", Data: []byte(`{"i":1}`)},
		{Subject: "events.identity.updated", Data: []byte(`{"i":2}`)},
		{Subject: "events.identity.linked", Data: []byte(`{"i":3}`)},
	}
	ack, err := c.PublishBatch(ctx, ops)
	if err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
	if ack.Count != 3 {
		t.Fatalf("ack count = %d, want 3", ack.Count)
	}
	if ack.Stream != "core-events" {
		t.Fatalf("ack stream = %q, want core-events", ack.Stream)
	}

	// Verify all three messages landed by consuming via a pull consumer.
	js := c.JetStream()
	cons, err := js.CreateOrUpdateConsumer(ctx, "core-events", jetstream.ConsumerConfig{
		Durable:        "test-consumer",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{"events.>"},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateConsumer: %v", err)
	}
	msgs, err := cons.Fetch(3, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var got []string
	for m := range msgs.Messages() {
		got = append(got, m.Subject())
		_ = m.Ack()
	}
	if len(got) != 3 {
		t.Fatalf("consumed %d messages, want 3 (got=%v)", len(got), got)
	}
	want := []string{"events.identity.created", "events.identity.updated", "events.identity.linked"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("msg %d subject = %q, want %q", i, got[i], w)
		}
	}
}

// TestPublishBatch_EmptyRejected confirms the helper rejects empty op lists.
func TestPublishBatch_EmptyRejected(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	if _, err := c.PublishBatch(ctx, nil); err == nil {
		t.Fatalf("PublishBatch(nil) should error")
	}
}

// TestPublishBatch_HeaderForwarded confirms custom headers reach the wire.
func TestPublishBatch_HeaderForwarded(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionEventsStream(ctx, t, c)

	ops := []PublishOp{
		{
			Subject: "events.identity.created",
			Data:    []byte(`{"x":1}`),
			Header:  map[string]string{"X-Lattice-RequestId": "test-request-id"},
		},
	}
	if _, err := c.PublishBatch(ctx, ops); err != nil {
		t.Fatalf("PublishBatch: %v", err)
	}
	js := c.JetStream()
	cons, err := js.CreateOrUpdateConsumer(ctx, "core-events", jetstream.ConsumerConfig{
		Durable:        "hdr-consumer",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{"events.>"},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateConsumer: %v", err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for m := range batch.Messages() {
		if got := m.Headers().Get("X-Lattice-RequestId"); got != "test-request-id" {
			t.Fatalf("X-Lattice-RequestId = %q, want test-request-id", got)
		}
		_ = m.Ack()
	}
}
