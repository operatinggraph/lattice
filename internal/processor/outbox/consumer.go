// Package outbox implements the durable transactional-event-outbox consumer.
//
// The Processor persists each operation's faithful EventList as a sibling
// aspect (`vtx.op.<requestId>.events`) inside the step-8 atomic batch, so the
// events are durable iff the commit succeeds. This consumer reads those aspects
// from the Core KV stream and publishes the real events to `core-events`,
// acking only after a confirmed publish — then tombstones the aspect. A crash
// between commit and publish is recovered by redelivery from the durable
// offset; events are at-least-once and never reconstructed.
package outbox

import (
	"context"
	"log/slog"
	"strings"

	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
)

// ConsumerName is the durable consumer name on the Core KV stream.
const ConsumerName = "processor-outbox"

// Consumer drives the durable outbox consumer on the Core KV stream. It
// filters for outbox aspects (`$KV.<bucket>.vtx.op.*.events`), publishes the
// persisted EventList to `core-events`, tombstones the aspect, then acks.
type Consumer struct {
	conn         *substrate.Conn
	streamName   string
	filterSubj   string
	bucket       string
	subjectPrefx string // "$KV.<bucket>." — strip from msg.Subject to recover the Core KV key
	publisher    *EventPublisherImpl
	logger       *slog.Logger
}

// New constructs the outbox Consumer for the given Core KV bucket.
func New(conn *substrate.Conn, coreKVBucket string, logger *slog.Logger) *Consumer {
	if conn == nil {
		panic("outbox: New requires Conn")
	}
	if coreKVBucket == "" {
		panic("outbox: New requires coreKVBucket")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Consumer{
		conn:         conn,
		streamName:   "KV_" + coreKVBucket,
		filterSubj:   "$KV." + coreKVBucket + ".vtx.op.*.events",
		bucket:       coreKVBucket,
		subjectPrefx: "$KV." + coreKVBucket + ".",
		publisher:    NewEventPublisher(conn, logger),
		logger:       logger,
	}
}

// Run creates the durable consumer (idempotent) and processes outbox aspects
// until ctx is cancelled. Run blocks until ctx is done.
func (c *Consumer) Run(ctx context.Context) error {
	return c.conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:        c.streamName,
		FilterSubject: c.filterSubj,
		Durable:       ConsumerName,
		Logger:        c.logger,
	}, c.handle)
}

// handle processes a single outbox-aspect delivery: empty/tombstone/PURGE
// bodies are acked and skipped; otherwise the persisted EventList is published
// to `core-events`, the aspect is tombstoned, and the message is acked. Nak on
// publish failure so JetStream redelivers (events stay at-least-once). Term on
// an unparseable aspect (poison; event-loss risk).
func (c *Consumer) handle(ctx context.Context, msg substrate.Message) substrate.Decision {
	// KV tombstone / PURGE / TTL-expiry markers have empty bodies — ack + skip.
	// This also covers our own post-publish tombstone on a full seq-0 replay.
	if len(msg.Body) == 0 {
		return substrate.Ack
	}

	// Recover the Core KV key from the JetStream subject ($KV.<bucket>.<key>).
	key := strings.TrimPrefix(msg.Subject, c.subjectPrefx)

	aspect, err := processor.ParseOutboxAspect(msg.Body)
	if err != nil {
		// An unparseable outbox record is structurally invalid and an
		// event-loss risk; term it (poison message) and log loudly.
		c.logger.Error("outbox: unparseable aspect — terminating (event-loss risk)",
			"key", key, "error", err)
		return substrate.Term
	}

	// A tombstoned aspect (isDeleted) carries no events to publish — ack + skip.
	if aspect.IsDeleted || len(aspect.Data.Events) == 0 {
		return substrate.Ack
	}

	// Publish the faithful EventList. The publisher batches all events for the
	// operation into one ordered PublishBatch, preserving intra-op order.
	env := &processor.OperationEnvelope{RequestID: aspect.Data.RequestID}
	if err := c.publisher.Publish(ctx, env, aspect.Data.Events); err != nil {
		c.logger.Warn("outbox: publish failed; nak for redelivery",
			"key", key, "requestId", aspect.Data.RequestID, "error", err)
		return substrate.Nak
	}

	// Tombstone the aspect (cleanup + replay-safety) before acking. A failure
	// here is tolerated — the events were published, and a redelivery would at
	// most re-publish once (consumers are idempotent).
	if delErr := c.conn.KVDelete(ctx, c.bucket, key); delErr != nil {
		c.logger.Warn("outbox: tombstone failed (events already published)",
			"key", key, "requestId", aspect.Data.RequestID, "error", delErr)
	}

	return substrate.Ack
}
