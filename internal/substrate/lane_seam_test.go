package substrate

import (
	"context"
	"errors"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// TestSupervisor_MaxAckPending threads the spec's MaxAckPending into the created
// JetStream consumer (1 ⇒ server-side serialization for the Processor meta lane,
// Contract #2 §3.7) and leaves it at the JetStream default when unset.
func TestSupervisor_MaxAckPending(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := "ops-maxack"
	if err := c.EnsureStream(ctx, StreamSpec{Name: stream, Subjects: []string{"ops.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	sup := NewConsumerSupervisor(c)
	t.Cleanup(sup.Stop)

	noop := func(context.Context, Message) (Decision, error) { return Ack, nil }
	if err := sup.Add(ctx, ConsumerSpec{
		Name: "serial-1", Stream: stream, FilterSubject: "ops.meta",
		MaxAckPending: 1, Handler: noop,
	}); err != nil {
		t.Fatalf("Add serial: %v", err)
	}
	if err := sup.Add(ctx, ConsumerSpec{
		Name: "parallel-default", Stream: stream, FilterSubject: "ops.default",
		Handler: noop,
	}); err != nil {
		t.Fatalf("Add parallel: %v", err)
	}

	if got := consumerInfoByName(ctx, t, c, stream, "serial-1").Config.MaxAckPending; got != 1 {
		t.Fatalf("serial consumer MaxAckPending = %d, want 1", got)
	}
	// JetStream's default MaxAckPending is a positive number (not 1); the exact
	// value is server-versioned, so assert only that the unset lane is NOT pinned
	// to 1 (i.e. left for parallelism per deployment sizing).
	if got := consumerInfoByName(ctx, t, c, stream, "parallel-default").Config.MaxAckPending; got == 1 {
		t.Fatalf("parallel consumer MaxAckPending = 1, want the JetStream default (unpinned)")
	}
}

// TestConn_DeleteStreamConsumer removes a named durable from an event stream and
// is idempotent: a not-found consumer (already gone, or never created) is treated
// as success — so the Processor's one-time processor-main retirement is safe to
// run on every startup.
func TestConn_DeleteStreamConsumer(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	stream := "ops-delete"
	if err := c.EnsureStream(ctx, StreamSpec{Name: stream, Subjects: []string{"ops.>"}}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}

	// Create a durable to delete.
	if _, err := c.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable: "legacy-main", FilterSubject: "ops.default", AckPolicy: jetstream.AckExplicitPolicy,
	}); err != nil {
		t.Fatalf("create legacy consumer: %v", err)
	}

	// First delete removes it.
	if err := c.DeleteStreamConsumer(ctx, stream, "legacy-main"); err != nil {
		t.Fatalf("DeleteStreamConsumer: %v", err)
	}
	if _, err := c.js.Consumer(ctx, stream, "legacy-main"); !errors.Is(err, jetstream.ErrConsumerNotFound) {
		t.Fatalf("consumer still present after delete: err=%v", err)
	}

	// Second delete is a no-op (idempotent).
	if err := c.DeleteStreamConsumer(ctx, stream, "legacy-main"); err != nil {
		t.Fatalf("idempotent re-delete: %v", err)
	}

	// Deleting a never-existing durable on an existing stream is also a no-op.
	if err := c.DeleteStreamConsumer(ctx, stream, "never-existed"); err != nil {
		t.Fatalf("delete absent durable: %v", err)
	}
}
