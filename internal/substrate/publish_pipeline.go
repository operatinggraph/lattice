package substrate

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// DefaultPublishPipelineWindow is how many publishes a PublishPipeline keeps in
// flight when the caller names no window of its own.
//
// The budget it is sized against is PER CONNECTION, not per pipeline:
// PublishAsyncMaxPending caps the unacknowledged async publishes a whole
// JetStream context will hold, and a process runs one substrate.Conn. The
// arithmetic that has to hold is therefore
//
//	concurrent pipelines × window <= PublishAsyncMaxPending
//
// and the pipelines are counted across every lens, because each personal lens's
// write step opens two — one for its rows and one for its audit entries — while
// a device hydrate or a grant-change reprojection opens another for the same
// lens. At 8,192 pending and a window of 128 that funds 64 simultaneously
// draining pipelines, i.e. every personal lens in the corpus running its write
// step and its audit together with room to spare
// (TestPersonalLensPipelineBudget_FitsTheConnectionsAsyncCeiling pins the count
// against the real corpus). Crossing the ceiling is not a slow path: the
// publisher stalls 200ms and then fails with ErrTooManyStalledMsgs.
//
// 128 still collects essentially all of the payoff — the store-ack round trip
// is the whole cost this type removes, and a window of 128 removes all but
// 1/128th of it — so raising it buys almost nothing and spends headroom.
const DefaultPublishPipelineWindow = 128

// pendingPublish is one issued-but-unacknowledged publish: the future to await
// and the subject to name in an error, since a resolved future reports the
// failure but not where it was going.
type pendingPublish struct {
	subject string
	future  jetstream.PubAckFuture
}

// PublishPipeline pipelines JetStream publishes for one caller's write loop:
// Add sends a message without waiting for its store ack, and Flush awaits the
// acks of everything added since the last Flush. A loop publishing N messages
// pays one round trip plus the server's store time rather than N round trips.
//
// It carries NO atomicity, which is the whole of what separates it from
// Conn.PublishBatch — an atomic batch (Nats-Batch-Id) where every message lands
// or none does, on one stream. A pipeline governs only WHEN acks are awaited,
// never whether a message can land on its own: a Flush error means at least one
// message did not land, and the others sent through the same pipeline may well
// have. A caller disposes of a failed Flush exactly as it disposes of a failed
// mid-loop Publish — redeliver the whole unit, whose repeated writes must be
// idempotent.
//
// Ordering is the connection's send order: Add hands each message to the same
// connection in call order, so the stream assigns sequences in that order —
// with one exception worth knowing. The client answers a no-responders reply
// (no stream bound the subject at that instant) by RE-SENDING the message from
// a timer goroutine ~250ms later, so a message that hits that window lands
// after the rest of the pipeline rather than in its own place. Harmless for the
// personal wire — the rows carry distinct keys and one shared revision, and
// Flush awaits the resend before the keyset frame — but a caller that needs
// strict inter-message ordering under a stream that can vanish and return must
// not assume it.
//
// A PublishPipeline is not safe for concurrent use. Concurrent callers
// publishing through the same Conn each open their own; the futures are
// per-message, so two pipelines on one connection do not interfere.
type PublishPipeline struct {
	c       *Conn
	window  int
	pending []pendingPublish
	// recorded is the first wire failure this pipeline has seen but not yet
	// reported: a publish the client refused, or the ack of an earlier message
	// that a full window forced Add to await. Flush reports it and clears it.
	// It exists so Add can never hand a caller an error belonging to a
	// DIFFERENT message than the one it was called for — see Add's doc.
	recorded error
}

// NewPublishPipeline opens a publish pipeline on this connection. window is how
// many publishes may be in flight before Add awaits the oldest; window <= 0
// takes DefaultPublishPipelineWindow. A pipeline holds no server-side state —
// dropping one without flushing abandons its outstanding acks (the messages
// themselves may still land), so a caller that wants to know its writes are
// durable must Flush.
func (c *Conn) NewPublishPipeline(window int) *PublishPipeline {
	if window <= 0 {
		window = DefaultPublishPipelineWindow
	}
	return &PublishPipeline{c: c, window: window}
}

// Add publishes one message into the pipeline without awaiting its store ack,
// building the message exactly as Publish does (header is optional). When the
// pipeline already has window publishes in flight it first awaits the OLDEST of
// them, so in-flight work stays bounded.
//
// FLUSH IS THE ONLY ERROR SEAM. Add returns an error solely for caller misuse —
// an empty subject, which is a programming fault and not a wire outcome. Every
// wire failure it encounters is recorded on the pipeline and reported by Flush:
// the ack it awaited belongs to an EARLIER message, and the publish it issues
// has not been acked yet, so neither failure is attributable to the message Add
// was called for. Handing one back would let a caller charge another row's
// failure to this row — dispose of it per-row, retry the wrong row, and then
// ack the message with the real casualty never stored, because the failed
// future has already been consumed and the later Flush comes back clean.
//
// A recorded failure does not stop the loop: Add still publishes the message it
// was called for, and the caller finds out at its Flush, whose disposition
// (redeliver the whole unit) is the same one an early abort would have reached.
func (pl *PublishPipeline) Add(ctx context.Context, subject string, data []byte, header map[string]string) error {
	if subject == "" {
		return fmt.Errorf("substrate: PublishPipeline.Add: subject required")
	}
	if len(pl.pending) >= pl.window {
		if err := pl.awaitOldest(ctx); err != nil {
			// Recorded, and the loop carries on: whether this message is
			// published cannot depend on an earlier one's fate, or Add's
			// behaviour would turn on hidden state and a caller could not read
			// its own write loop. The recorded failure redelivers the whole
			// unit anyway, so an extra idempotent write costs nothing.
			pl.record(err)
		}
	}
	msg := &nats.Msg{Subject: subject, Data: data}
	if len(header) > 0 {
		msg.Header = nats.Header{}
		for k, v := range header {
			msg.Header.Set(k, v)
		}
	}
	future, err := pl.c.js.PublishMsgAsync(msg)
	if err != nil {
		pl.record(fmt.Errorf("substrate: publish %q: %w", subject, err))
		return nil
	}
	pl.pending = append(pl.pending, pendingPublish{subject: subject, future: future})
	return nil
}

// record keeps the FIRST wire failure the pipeline has seen. First rather than
// last: it is the one whose message is furthest from being stored, and a caller
// reading one error wants the earliest thing that went wrong, not the last
// consequence of it.
func (pl *PublishPipeline) record(err error) {
	if pl.recorded == nil {
		pl.recorded = err
	}
}

// Pending reports how many publishes this pipeline has issued and not yet
// awaited. Zero after a Flush; it is what makes "these writes are stored"
// observable to a caller that has to order something after them.
func (pl *PublishPipeline) Pending() int {
	return len(pl.pending)
}

// Flush awaits every publish still outstanding, in the order it was added, and
// returns the FIRST error (wrapped with its subject) — anything Add recorded
// earlier first, then the earliest of the acks it awaits itself. It awaits the
// rest even after one fails, so no future is left dangling, and it leaves the
// pipeline empty and ready to be used again.
//
// A nil return means every message added since the last Flush is stored. A
// non-nil one means at least one is not; which of the others landed is not
// reported, because no disposition depends on it (see the type's doc).
func (pl *PublishPipeline) Flush(ctx context.Context) error {
	firstErr := pl.recorded
	pl.recorded = nil
	for _, p := range pl.pending {
		if err := pl.await(ctx, p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	pl.pending = pl.pending[:0]
	return firstErr
}

// awaitOldest pops the longest-outstanding publish and awaits it. The entry is
// removed before the await so a failed one is never awaited twice.
func (pl *PublishPipeline) awaitOldest(ctx context.Context) error {
	p := pl.pending[0]
	pl.pending = pl.pending[1:]
	return pl.await(ctx, p)
}

// await blocks until p resolves or ctx ends.
//
// ctx is checked before the future is examined, so a caller that has already
// given up gets ctx's error rather than a success it can no longer stand
// behind — an ack that arrived after cancellation says the message is stored,
// but the caller is no longer in a position to act on that, and reporting the
// cancellation makes it redeliver (idempotently) instead of proceeding.
func (pl *PublishPipeline) await(ctx context.Context, p pendingPublish) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("substrate: publish %q: %w", p.subject, err)
	}
	select {
	case <-p.future.Ok():
		return nil
	case err := <-p.future.Err():
		return fmt.Errorf("substrate: publish %q: %w", p.subject, err)
	case <-ctx.Done():
		return fmt.Errorf("substrate: publish %q: %w", p.subject, ctx.Err())
	}
}
