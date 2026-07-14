package processor

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// TestAckerImpl_NilMsg unit-tests the defensive guard in NewAcker.
func TestAckerImpl_NilMsg(t *testing.T) {
	a := NewAcker(nil, testLogger())
	if err := a.Ack(context.Background()); err == nil {
		t.Fatalf("expected error on nil msg")
	}
}

// TestAckerImpl_E2E confirms an AckerImpl wrapping a real
// jetstream.Msg flushes the ack to the consumer. We publish a message,
// pull it via Fetch, wrap it in NewAcker, call Ack, then verify the
// consumer's AckFloor advanced (NumAckPending → 0).
func TestAckerImpl_E2E(t *testing.T) {
	t.Parallel()
	ctx, conn, _, _, _ := setupTestPipeline(t)
	js := conn.JetStream()

	// Provision an isolated stream so we don't interfere with the test
	// pipeline's core-operations consumer.
	streamName := "acker-test"
	subject := "acker.test.sub"
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     streamName,
		Subjects: []string{subject},
	}); err != nil {
		t.Fatalf("create stream: %v", err)
	}

	if _, err := js.Publish(ctx, subject, []byte("payload")); err != nil {
		t.Fatalf("publish: %v", err)
	}

	cons, err := js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:   "acker-test-cons",
		AckPolicy: jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateConsumer: %v", err)
	}
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var msg jetstream.Msg
	for m := range batch.Messages() {
		msg = m
		break
	}
	if msg == nil {
		t.Fatalf("no message fetched")
	}

	a := NewAcker(msg, testLogger())
	if err := a.Ack(ctx); err != nil {
		t.Fatalf("Ack: %v", err)
	}

	// Flush pushes the ack (published asynchronously by Msg.Ack) out of the
	// client promptly; awaitAckSettled observes the server apply it to
	// consumer state.
	if err := conn.NATS().Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	awaitAckSettled(t, ctx, cons)
}
