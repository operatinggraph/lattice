package substrate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// durableReconnect is the delay before reopening the message iterator after a
// transient error in RunDurableConsumer.
const durableReconnect = 5 * time.Second

// Decision is the caller's verdict on a delivered message, returned from a
// HandlerFunc. It determines the JetStream acknowledgement applied after the
// handler returns: confirmed-processed (Ack), retry-later (Nak), or
// permanently-undeliverable (Term).
type Decision int

const (
	// Ack marks the message processed and advances the durable ack floor.
	Ack Decision = iota
	// Nak signals a transient failure; JetStream redelivers (at-least-once
	// preserved).
	Nak
	// Term drops a poison message; JetStream never redelivers it
	// (event-loss-accepting — log loudly before returning it).
	Term
)

// Message is the minimal view of a delivered JetStream message handed to a
// HandlerFunc. Routing/identity is read from Body (read-from-body discipline),
// not from Subject; Subject is provided only for mechanical key recovery (e.g.
// stripping a "$KV.<bucket>." prefix to recover a Core KV key) and diagnostics.
type Message struct {
	Subject  string
	Body     []byte
	Sequence uint64 // backing-stream sequence (diagnostics / position reasoning)
}

// HandlerFunc processes one message and returns the ack Decision. It MUST be
// idempotent: at-least-once delivery means the same message can arrive again
// after a Nak or a crash-before-ack.
type HandlerFunc func(ctx context.Context, msg Message) Decision

// DurableConsumerConfig binds a durable consumer to a stream + filter subject.
type DurableConsumerConfig struct {
	// Stream is the JetStream stream name (e.g. "KV_core-kv").
	Stream string
	// FilterSubject restricts delivery to matching subjects (e.g.
	// "$KV.core-kv.vtx.op.*.events").
	FilterSubject string
	// Durable is the durable consumer name. Re-running with the same name
	// resumes from the last-acked sequence.
	Durable string
	// MaxDeliver bounds redelivery on Nak. A value <= 0 omits the bound,
	// leaving JetStream's default (unlimited redelivery).
	MaxDeliver int
	// Logger is the diagnostics sink. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// RunDurableConsumer creates (idempotently) the durable consumer described by
// cfg and drives it, invoking handler for each delivered message and applying
// the returned Decision, until ctx is cancelled. It blocks until ctx is done.
//
// The consumer uses DeliverAllPolicy + AckExplicitPolicy: delivery starts at
// the beginning of the durable's history and every message is acknowledged by
// the handler's Decision. Empty-body messages are delivered to the handler
// (the primitive is policy-free about body content); the handler decides what
// they mean.
//
// Re-running with the same cfg.Durable resumes from the last-acked sequence:
// the consumer is NOT deleted on shutdown — its persisted position is the point
// of "durable". Operators who need to retire a durable must delete it
// explicitly.
func (c *Conn) RunDurableConsumer(ctx context.Context, cfg DurableConsumerConfig, handler HandlerFunc) error {
	if cfg.Stream == "" {
		return fmt.Errorf("substrate: RunDurableConsumer: Stream required")
	}
	if cfg.Durable == "" {
		return fmt.Errorf("substrate: RunDurableConsumer: Durable required")
	}
	if handler == nil {
		return fmt.Errorf("substrate: RunDurableConsumer: handler required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	consCfg := jetstream.ConsumerConfig{
		Durable:       cfg.Durable,
		FilterSubject: cfg.FilterSubject,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	}
	if cfg.MaxDeliver > 0 {
		consCfg.MaxDeliver = cfg.MaxDeliver
	}

	cons, err := c.js.CreateOrUpdateConsumer(ctx, cfg.Stream, consCfg)
	if err != nil {
		return fmt.Errorf("substrate: RunDurableConsumer: create consumer %q on %q: %w",
			cfg.Durable, cfg.Stream, err)
	}

	c.runDurableLoop(ctx, cons, cfg.Durable, logger, handler)
	return nil
}

// runDurableLoop reopens the message iterator on transient errors until ctx is
// done.
func (c *Conn) runDurableLoop(
	ctx context.Context,
	cons jetstream.Consumer,
	durable string,
	logger *slog.Logger,
	handler HandlerFunc,
) {
	for {
		if ctx.Err() != nil {
			return
		}
		mc, err := cons.Messages()
		if err != nil {
			logger.Error("substrate: durable consumer: open messages iterator",
				"durable", durable, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(durableReconnect):
			}
			continue
		}
		c.drainDurable(ctx, mc, durable, logger, handler)
	}
}

// drainDurable reads messages until ctx is cancelled or the iterator returns an
// error. A watcher stops the iterator on ctx.Done so the blocking Next()
// unblocks promptly for a clean shutdown.
func (c *Conn) drainDurable(
	ctx context.Context,
	mc jetstream.MessagesContext,
	durable string,
	logger *slog.Logger,
	handler HandlerFunc,
) {
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			mc.Stop()
		case <-stopped:
		}
	}()
	defer func() {
		close(stopped)
		mc.Stop()
	}()

	for {
		msg, err := mc.Next()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Error("substrate: durable consumer: receive error, will reconnect",
				"durable", durable, "error", err)
			return
		}
		applyDecision(handler(ctx, newMessage(msg)), msg, durable, logger)
	}
}

// newMessage builds the caller-facing Message view from a raw JetStream
// message. Sequence is the backing-stream sequence when metadata is available.
func newMessage(msg jetstream.Msg) Message {
	m := Message{
		Subject: msg.Subject(),
		Body:    msg.Data(),
	}
	if meta, err := msg.Metadata(); err == nil {
		m.Sequence = meta.Sequence.Stream
	}
	return m
}

// applyDecision applies the handler's Decision to the underlying JetStream
// message. A failed Ack is logged, not retried (a redelivery re-runs the
// handler, which must be idempotent).
func applyDecision(d Decision, msg jetstream.Msg, durable string, logger *slog.Logger) {
	switch d {
	case Nak:
		if err := msg.Nak(); err != nil {
			logger.Error("substrate: durable consumer: nak failed", "durable", durable, "error", err)
		}
	case Term:
		if err := msg.Term(); err != nil {
			logger.Error("substrate: durable consumer: term failed", "durable", durable, "error", err)
		}
	default:
		if err := msg.Ack(); err != nil {
			logger.Error("substrate: durable consumer: ack failed", "durable", durable, "error", err)
		}
	}
}
