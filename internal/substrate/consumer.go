package substrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// durableReconnect is the delay before reopening the message iterator after a
// transient error in RunDurableConsumer.
const durableReconnect = 5 * time.Second

const (
	// drainBufferedIdle bounds how long the reconnect-path buffer drain waits
	// for the next already-delivered message. A drained iterator pulls nothing
	// more, so a buffer that goes quiet for this long is an empty one.
	drainBufferedIdle = 2 * time.Second
	// drainBufferedBudget caps the whole reconnect-path buffer drain, so a
	// wedged link delays the reconnect by a bounded amount rather than parking
	// the consumer indefinitely.
	drainBufferedBudget = 30 * time.Second
	// drainBufferedMaxErrs is how many errors the drain rides out back-to-back
	// before giving up on the buffer. Each one may be masking a message that was
	// ready to be handed back, so a few are worth absorbing; a run of them with
	// no message in between is a link that will not deliver, and spinning
	// through the whole budget against it buys nothing.
	drainBufferedMaxErrs = 8
)

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
	// NakWithDelay signals a transient failure that must NOT hot-loop:
	// JetStream redelivers, but no sooner than a fixed redelivery floor. The
	// floor is configuration, not data — it is carried on the consumer's
	// config (DurableConsumerConfig.RedeliveryDelay or a ConsumerSpec), never
	// on the Decision. Use this instead of Nak when immediate redelivery would
	// spin the handler against a still-failing dependency.
	NakWithDelay
	// NakWithLongDelay is identical in semantics to NakWithDelay — JetStream
	// redelivers, no sooner than a floor carried on the consumer's config
	// (DurableConsumerConfig.LongRedeliveryDelay or a ConsumerSpec), never on
	// the Decision — but applied with the consumer's LONG redelivery floor
	// instead of the short one. Use this for a decline whose fix cannot arrive
	// as a new delivery at all: a configuration, registry, or template change
	// that the handler has no way to observe except by being asked again, so
	// the redelivery IS the only automatic uptake path. NakWithDelay's short
	// floor is sized for a dependency that heals on its own; this one is sized
	// for a re-poll cadence against state that only a human or a separate
	// change ever touches.
	NakWithLongDelay
)

// DefaultRedeliveryDelay is the floor applied to a NakWithDelay decision when
// the consumer config leaves RedeliveryDelay at its zero value. A handler that
// returns NakWithDelay has expressed "do not hot-loop"; degrading to immediate
// redelivery would silently reintroduce the spin, so a missing floor falls back
// to this package default rather than plain Nak. Same order of magnitude as
// durableReconnect.
const DefaultRedeliveryDelay = 5 * time.Second

// DefaultLongRedeliveryDelay is the floor applied to a NakWithLongDelay
// decision when the consumer config leaves LongRedeliveryDelay at its zero
// value. It prices a pure re-poll cadence, not a retry: the declined row it
// applies to has no fix that can arrive as a fresh delivery, so this is
// simply how often the row is asked again on the chance its config has
// changed. Five minutes trades that uptake latency against the standing cost
// of a stuck population being redelivered on a floor tick.
const DefaultLongRedeliveryDelay = 5 * time.Minute

// Message is the minimal view of a delivered JetStream message handed to a
// HandlerFunc. Routing/identity is read from Body (read-from-body discipline),
// not from Subject; Subject is provided only for mechanical key recovery (e.g.
// stripping a "$KV.<bucket>." prefix to recover a Core KV key) and diagnostics.
type Message struct {
	Subject  string
	Body     []byte
	Sequence uint64 // backing-stream sequence (diagnostics / position reasoning)
	// NumDelivered is the JetStream delivery count for this message (1 on first
	// delivery, incrementing on each redelivery). Zero when metadata is
	// unavailable. Provided so a supervised handler can reason about redelivery
	// without reaching for a jetstream.Msg.
	NumDelivered uint64
	// NumPending is the number of messages still pending behind this one at
	// delivery time (the authoritative count NATS embeds in each message's
	// metadata). Zero when metadata is unavailable, or when this was the last
	// message pending at delivery. Provided so a handler can detect drain
	// ("lag == 0") without a separate consumer-info round-trip.
	NumPending uint64
	// ReplySubject is the NATS reply subject of the delivered message
	// (msg.Reply()), for request-reply consumers that publish a reply to the
	// caller's inbox. Empty for fire-and-forget event/CDC messages. The first
	// request-reply consumer of the supervisor (the Processor commit path) reads
	// it to answer the submitting client; event consumers (Loom/Weaver/Refractor)
	// leave it unused.
	ReplySubject string
	// Header returns the value of a delivered-message header by key, or "" if the
	// header is absent or the message carried none. Provided so a request-reply
	// handler can honor an in-band reply inbox (the Lattice-Reply-Inbox header,
	// which JetStream pull consumers require because they rewrite Reply() to the
	// stream ACK subject) without reaching for a jetstream.Msg. Nil on a Message
	// the caller constructed directly without a header source.
	Header func(key string) string
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
	// StartSeq is the stream sequence delivery begins at, for a caller that
	// holds its own record of what it has already processed and wants the
	// consumer positioned there rather than at the stream's retained
	// beginning. A value > 0 selects DeliverByStartSequencePolicy with
	// OptStartSeq; the zero value selects DeliverAllPolicy, so a caller that
	// has no position to assert gets the whole retained history.
	//
	// The position is honored only when the durable is CREATED. JetStream
	// refuses to update DeliverPolicy or OptStartSeq on an existing consumer
	// (nats-server 2.14 server/consumer.go:2435,:2438) — in BOTH directions,
	// so an existing positioned durable cannot be re-created unpositioned
	// either. A caller that changes the position it asks for, including back
	// to zero, has to delete the durable first: see DeleteStreamConsumer, and
	// note that deleting a durable someone else is reading is destructive to
	// that reader.
	//
	// An out-of-range position never fails the create, but the two directions
	// are not equally benign. A sequence past the last retained message is
	// respected and the consumer waits. A sequence BELOW the first retained
	// message is silently clamped UP to it — the SKIP direction: the caller
	// named a start the stream no longer holds and gets no signal that the
	// span between is missing. A caller whose correctness depends on that span
	// must detect the shortfall itself before asking (the Edge does, via the
	// personal.syncgap control RPC).
	StartSeq uint64
	// MaxDeliver bounds redelivery on Nak. A value <= 0 omits the bound,
	// leaving JetStream's default (unlimited redelivery).
	MaxDeliver int
	// RedeliveryDelay is the floor applied when a handler returns NakWithDelay.
	// A zero value falls back to DefaultRedeliveryDelay. It has no effect on
	// plain Nak (immediate redelivery) decisions.
	RedeliveryDelay time.Duration
	// LongRedeliveryDelay is the floor applied when a handler returns
	// NakWithLongDelay. A zero value falls back to DefaultLongRedeliveryDelay;
	// a configured value below DefaultRedeliveryDelay is raised to it (the
	// long floor can never be shorter than the short one). It has no effect on
	// Nak or NakWithDelay decisions.
	LongRedeliveryDelay time.Duration
	// AckWait is how long JetStream waits for a Decision before treating the
	// message as un-acked and redelivering it. A zero value leaves JetStream's
	// 30s default, which is right for a handler that returns promptly and wrong
	// for one that blocks on real work: the loop below is strictly serial, so a
	// handler that runs longer than AckWait is redelivered WHILE STILL RUNNING,
	// and each redelivery re-does the whole handler. Any consumer whose handler
	// can legitimately exceed 30s must set this above its own upper bound.
	//
	// Setting it is NOT sufficient on its own — see MaxPrefetch. The clock runs
	// from delivery, and delivery means "into the client's prefetch buffer", not
	// "into the handler".
	AckWait time.Duration
	// MaxPrefetch caps how many messages the client pulls ahead of the handler.
	// A zero value leaves nats.go's DefaultMaxMessages (500), which is right for
	// a fast handler and wrong for a slow one for a reason AckWait alone cannot
	// fix: AckWait is measured from when a message is DELIVERED INTO THE BUFFER,
	// and this loop is strictly serial, so message N sits in the buffer through
	// every preceding message's handler. On a DeliverAll replay a queued message
	// can exhaust any AckWait before its handler is even entered, and each
	// redelivery re-runs the whole handler and burns a NumDelivered any
	// delivery-budget logic is counting. A slow-handler consumer should set 1,
	// so the ack clock starts when the work does.
	MaxPrefetch int
	// Logger is the diagnostics sink. Defaults to slog.Default() when nil.
	Logger *slog.Logger
}

// RunDurableConsumer creates (idempotently) the durable consumer described by
// cfg and drives it, invoking handler for each delivered message and applying
// the returned Decision, until ctx is cancelled. It blocks until ctx is done.
//
// The consumer uses AckExplicitPolicy — every message is acknowledged by the
// handler's Decision — and takes its delivery start from cfg.StartSeq:
// DeliverAllPolicy (the beginning of the durable's history) when it is zero,
// DeliverByStartSequencePolicy at that sequence otherwise. Empty-body messages
// are delivered to the handler (the primitive is policy-free about body
// content); the handler decides what they mean.
//
// Re-running with the same cfg.Durable resumes from the last-acked sequence:
// the consumer is NOT deleted on shutdown — its persisted position is the point
// of "durable". Operators who need to retire a durable must delete it
// explicitly. That persisted position is also why cfg.StartSeq applies only to
// a durable this call creates: against an existing one the server rejects a
// changed delivery position outright, so a caller re-positioning a durable it
// owns must delete it before calling.
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
	if cfg.StartSeq > 0 {
		consCfg.DeliverPolicy = jetstream.DeliverByStartSequencePolicy
		consCfg.OptStartSeq = cfg.StartSeq
	}
	if cfg.MaxDeliver > 0 {
		consCfg.MaxDeliver = cfg.MaxDeliver
	}
	if cfg.AckWait > 0 {
		consCfg.AckWait = cfg.AckWait
	}

	cons, err := c.js.CreateOrUpdateConsumer(ctx, cfg.Stream, consCfg)
	if err != nil {
		return fmt.Errorf("substrate: RunDurableConsumer: create consumer %q on %q: %w",
			cfg.Durable, cfg.Stream, err)
	}

	c.runDurableLoop(ctx, cons, cfg.Durable, cfg.RedeliveryDelay, cfg.LongRedeliveryDelay, cfg.MaxPrefetch, logger, handler)
	return nil
}

// runDurableLoop reopens the message iterator on transient errors until ctx is
// done.
func (c *Conn) runDurableLoop(
	ctx context.Context,
	cons jetstream.Consumer,
	durable string,
	redeliveryDelay time.Duration,
	longRedeliveryDelay time.Duration,
	maxPrefetch int,
	logger *slog.Logger,
	handler HandlerFunc,
) {
	for {
		if ctx.Err() != nil {
			return
		}
		var opts []jetstream.PullMessagesOpt
		if maxPrefetch > 0 {
			opts = append(opts, jetstream.PullMaxMessages(maxPrefetch))
		}
		mc, err := cons.Messages(opts...)
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
		c.drainDurable(ctx, mc, durable, redeliveryDelay, longRedeliveryDelay, logger, handler)
	}
}

// drainDurable reads messages until ctx is cancelled or the iterator returns an
// error. A watcher stops the iterator on ctx.Done so the blocking Next()
// unblocks promptly for a clean shutdown.
//
// The two exits owe the client's prefetch buffer different things. Stop()
// discards everything sitting in it without a Decision (nats.go v1.52.0
// jetstream/pull.go:768 sets `closed` but not `draining`, and Next() at :620
// refuses to hand a buffered message back in that state). That is right on ctx
// cancellation — the loop is going away, so nothing downstream can record
// progress past the discarded messages. It is wrong on the reconnect path,
// where runDurableLoop reopens the iterator on the SAME consumer and keeps
// handling higher sequences: the server counts a buffered message as delivered
// and holds it ack-pending until AckWait, so a handler that tracks which
// delivered sequences are still unresolved never learns about it and is free to
// record progress over the top of it. The reconnect path therefore hands the
// buffer to the handler first.
func (c *Conn) drainDurable(
	ctx context.Context,
	mc jetstream.MessagesContext,
	durable string,
	redeliveryDelay time.Duration,
	longRedeliveryDelay time.Duration,
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
			c.drainBuffered(ctx, mc, durable, redeliveryDelay, longRedeliveryDelay, logger, handler)
			return
		}
		applyDecision(handler(ctx, newMessage(msg)), msg, durable, redeliveryDelay, longRedeliveryDelay, logger)
	}
}

// drainBuffered hands every message already delivered into the client's
// prefetch buffer to handler before the iterator is abandoned, so no
// server-delivered message leaves the client without a Decision.
//
// Drain() is what makes the buffer readable: it marks the subscription draining
// as well as closed, so Next() keeps returning buffered messages and reports
// ErrMsgIteratorClosed only once the buffer is empty and the message channel has
// closed (nats.go v1.52.0 jetstream/pull.go:786, :620, :646). It is also why the
// caller's deferred Stop() is a harmless no-op afterwards: both guard on the
// same closed CompareAndSwap(0, 1).
//
// Three bounds keep the drain from outliving the reconnect it precedes, because
// this runs precisely when the link is unhealthy. A Decision's ack is a
// fire-and-forget publish that reports an error rather than blocking on a closed,
// draining or reconnect-buffer-full connection (nats.go v1.52.0 nats.go:4448,
// :4453, :4468), but a half-open socket can still stall one write up to the
// connection's FlusherTimeout. So each Next waits at most drainBufferedIdle, a
// run of drainBufferedMaxErrs errors with no message between them ends the
// drain, and the whole thing is capped at drainBufferedBudget.
func (c *Conn) drainBuffered(
	ctx context.Context,
	mc jetstream.MessagesContext,
	durable string,
	redeliveryDelay time.Duration,
	longRedeliveryDelay time.Duration,
	logger *slog.Logger,
	handler HandlerFunc,
) {
	mc.Drain()
	deadline := time.Now().Add(drainBufferedBudget)
	drained, consecutiveErrs := 0, 0
	report := func(reason string) {
		if drained > 0 {
			logger.Info("substrate: durable consumer: drained the prefetch buffer before reconnecting",
				"durable", durable, "messages", drained, "reason", reason)
		}
	}
	for {
		if ctx.Err() != nil {
			report("shutdown")
			return
		}
		wait := time.Until(deadline)
		if wait <= 0 {
			logger.Warn("substrate: durable consumer: prefetch-buffer drain budget exhausted",
				"durable", durable, "messages", drained)
			return
		}
		wait = min(wait, drainBufferedIdle)
		msg, err := mc.Next(jetstream.NextMaxWait(wait))
		if err != nil {
			switch {
			case errors.Is(err, jetstream.ErrMsgIteratorClosed):
				report("buffer empty")
			case errors.Is(err, nats.ErrTimeout):
				report("buffer idle")
			default:
				// Next selects between the message channel and the error
				// channel, so an error can win the race with a message that is
				// ready to be handed back. Keep draining while a few of those
				// are all that arrives; the idle wait above is what decides the
				// buffer is empty.
				consecutiveErrs++
				if consecutiveErrs < drainBufferedMaxErrs {
					continue
				}
				logger.Warn("substrate: durable consumer: prefetch-buffer drain abandoned after repeated errors",
					"durable", durable, "messages", drained, "error", err)
			}
			return
		}
		consecutiveErrs = 0
		drained++
		applyDecision(handler(ctx, newMessage(msg)), msg, durable, redeliveryDelay, longRedeliveryDelay, logger)
	}
}

// ConsumerCaughtUp reports whether the named durable consumer has fully drained
// its backlog: nothing pending delivery (NumPending) AND nothing delivered but
// not yet acked (NumAckPending). The two-field check is load-bearing — NumPending
// drops as soon as a message is delivered into the client's prefetch buffer, well
// before the handler has processed it, so a NumPending==0-only check would report
// "caught up" while a prefetched batch is still being worked. Requiring
// NumAckPending==0 too means the consumer is caught up only once every delivered
// message has been processed and acked. It is the standalone analogue of
// ConsumerSupervisor.PendingForConsumer for a durable created via
// RunDurableConsumer (which the supervisor does not manage). Returns an error if
// the consumer does not exist yet or its info cannot be read.
func (c *Conn) ConsumerCaughtUp(ctx context.Context, stream, durable string) (bool, error) {
	cons, err := c.js.Consumer(ctx, stream, durable)
	if err != nil {
		return false, fmt.Errorf("substrate: ConsumerCaughtUp: consumer %q on %q: %w", durable, stream, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		return false, fmt.Errorf("substrate: ConsumerCaughtUp: info %q: %w", durable, err)
	}
	return info.NumPending == 0 && info.NumAckPending == 0, nil
}

// StreamLastSequence reports the highest sequence the named stream currently
// holds (State.LastSeq), or 0 for a stream that has never taken a message.
//
// It is the companion of ConsumerCaughtUp for a caller that needs a NUMBER
// rather than a boolean: a consumer that has drained its backlog has applied
// everything up to this sequence, so a reader comparing its own progress
// against the stream's head reads the two together — caught up, and caught up
// TO WHAT. Reading it alone says nothing about any consumer.
//
// BOUND: exactly ONE round trip. js.Stream is itself a stream-info request, so
// the state it already fetched is read back with CachedInfo rather than asked
// for a second time — an Info call here would double the cost for a value the
// handle is holding. With no deadline on ctx the client bounds that one request
// by its own default API timeout; there is no retry and no paging, so the cost
// does not grow with what the stream holds.
func (c *Conn) StreamLastSequence(ctx context.Context, stream string) (uint64, error) {
	str, err := c.js.Stream(ctx, stream)
	if err != nil {
		return 0, fmt.Errorf("substrate: StreamLastSequence: stream %q: %w", stream, err)
	}
	return str.CachedInfo().State.LastSeq, nil
}

// newMessage builds the caller-facing Message view from a raw JetStream
// message. Sequence is the backing-stream sequence when metadata is available.
func newMessage(msg jetstream.Msg) Message {
	hdr := msg.Headers()
	m := Message{
		Subject:      msg.Subject(),
		Body:         msg.Data(),
		ReplySubject: msg.Reply(),
		Header: func(key string) string {
			if hdr == nil {
				return ""
			}
			return hdr.Get(key)
		},
	}
	if meta, err := msg.Metadata(); err == nil {
		m.Sequence = meta.Sequence.Stream
		m.NumDelivered = meta.NumDelivered
		m.NumPending = meta.NumPending
	}
	return m
}

// applyDecision applies the handler's Decision to the underlying JetStream
// message. A failed Ack is logged, not retried (a redelivery re-runs the
// handler, which must be idempotent). redeliveryDelay is the floor applied to a
// NakWithDelay decision; a zero value falls back to DefaultRedeliveryDelay.
// longRedeliveryDelay is the floor applied to a NakWithLongDelay decision; a
// zero value falls back to DefaultLongRedeliveryDelay, and a configured value
// below DefaultRedeliveryDelay is raised to it — the long floor can never be
// shorter than the short one.
func applyDecision(d Decision, msg jetstream.Msg, durable string, redeliveryDelay, longRedeliveryDelay time.Duration, logger *slog.Logger) {
	switch d {
	case Nak:
		if err := msg.Nak(); err != nil {
			logger.Error("substrate: durable consumer: nak failed", "durable", durable, "error", err)
		}
	case NakWithDelay:
		delay := redeliveryDelay
		if delay <= 0 {
			delay = DefaultRedeliveryDelay
		}
		if err := msg.NakWithDelay(delay); err != nil {
			logger.Error("substrate: durable consumer: nak-with-delay failed", "durable", durable, "error", err)
		}
	case NakWithLongDelay:
		delay := longRedeliveryDelay
		if delay <= 0 {
			delay = DefaultLongRedeliveryDelay
		}
		if delay < DefaultRedeliveryDelay {
			delay = DefaultRedeliveryDelay
		}
		if err := msg.NakWithDelay(delay); err != nil {
			logger.Error("substrate: durable consumer: nak-with-long-delay failed", "durable", durable, "error", err)
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
