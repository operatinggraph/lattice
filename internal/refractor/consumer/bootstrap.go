package consumer

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	adjConsumerName = "refractor-adjacency"
	// bootstrapReadyPoll is how often pollProgress checks the durable's pending
	// count BEFORE Ready, to detect the empty-stream "already caught up" case.
	bootstrapReadyPoll = 100 * time.Millisecond
	// bootstrapProgressPollFactor slows the same poll down once Ready has been
	// signalled. Before Ready the poll is a startup gate and every tick costs a
	// lens its start; after it, the poll is a background reconciliation of the
	// applied-sequence cursor with the durable's real state, which nothing waits
	// on — so it runs at a fraction of the rate for the life of the process.
	bootstrapProgressPollFactor = 5
)

// Bootstrapper drives the dedicated adjacency consumer on the Core KV stream
// until its pending message count reaches zero, then closes the Ready channel.
// It continues running thereafter to keep the adjacency index current (ADR-7, ADR-8).
// The index serves the cypher executor's Neighbors reads (adjacency.Neighbors);
// a pipeline that reacts to link events pre-applies the link to the index
// itself before evaluating, so its re-execute always reads a consistent edge
// set without depending on this bootstrapper's own build for the same edge.
type Bootstrapper struct {
	conn         *substrate.Conn
	streamName   string
	filterSubj   string
	bucket       string
	subjectPrefx string // "$KV.<bucket>." — strip this from msg.Subject to recover the Core KV key
	adjKV        *substrate.KV
	ready        chan struct{}
	once         sync.Once
	// seqMu guards the two fields below, which are read and written together:
	// the cursor they answer is a function of both, and a lock-free pair would
	// let a reader see a retirement without the owed entry that caps it.
	seqMu sync.Mutex
	// maxRetired is the highest Core KV stream sequence this process has seen
	// RETIRED — applied to the adjacency index, or discarded as unappliable.
	maxRetired uint64
	// owed holds the sequences this process has Nak'd and not since retired:
	// messages the index still has to apply. They are what turns a maximum
	// into a FLOOR. A Nak'd message is redelivered out of order, so the
	// sequences above it keep retiring while it is outstanding, and a cursor
	// reported as their maximum would claim the index reflects an edge it is
	// still missing — blind to precisely the lag AppliedSeq exists to expose.
	owed map[uint64]struct{}
}

// NewBootstrapper creates a Bootstrapper that reads from coreKVBucket via the
// dedicated adjacency durable consumer and writes edge index entries to adjKV.
func NewBootstrapper(conn *substrate.Conn, coreKVBucket string, adjKV *substrate.KV) *Bootstrapper {
	return &Bootstrapper{
		conn:         conn,
		streamName:   subjects.CoreKVStream(coreKVBucket),
		filterSubj:   subjects.CoreKVFilter(coreKVBucket),
		bucket:       coreKVBucket,
		subjectPrefx: "$KV." + coreKVBucket + ".",
		adjKV:        adjKV,
		ready:        make(chan struct{}),
		owed:         map[uint64]struct{}{},
	}
}

// Ready returns a channel that is closed once the adjacency consumer has
// processed all messages pending at startup (lag = 0, ADR-8).
func (b *Bootstrapper) Ready() <-chan struct{} {
	return b.ready
}

// Run drives the durable adjacency consumer via the substrate runtime,
// processing Core KV messages through adjacency.Build(), and closes Ready() when
// lag reaches zero. It blocks until ctx is cancelled.
//
// Ready is signalled by two complementary paths: the message handler closes it
// when it processes a delivery whose in-message pending count is zero (the
// non-empty-stream case), and the progress poll closes it when the durable
// reports zero pending without ever delivering a message (the empty-stream
// case). That poll then keeps running for the life of the process, at a slower
// tick, reconciling the applied-sequence cursor with the durable's real state
// (pollProgress).
func (b *Bootstrapper) Run(ctx context.Context) error {
	go b.pollProgress(ctx)
	return b.conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:        b.streamName,
		FilterSubject: b.filterSubj,
		Durable:       adjConsumerName,
	}, b.handle)
}

// pollProgress is the bootstrapper's standing observation of the durable, and
// it runs for the life of the process rather than only until Ready.
//
// BEFORE READY it covers the empty-stream case: RunDurableConsumer never
// invokes the handler when there is nothing to deliver, so Ready would never
// fire from the handler. It closes Ready once the durable is fully caught up —
// both NumPending and NumAckPending zero (ConsumerCaughtUp), which is immediate
// for an empty stream. The ack-aware check is essential: NumPending alone drops
// the instant a backlog is prefetched into the client buffer, so signalling on
// NumPending==0 would fire Ready while the handler is still building the
// adjacency index for that prefetched batch — closing the gate on a partial
// index. Requiring NumAckPending==0 means it never fires mid-drain; on a
// non-empty stream the handler's msg.NumPending==0 path (delivery-accurate,
// raised after the last edge is built) signals first.
//
// AFTER READY, at a fifth of the rate, it keeps doing the same reconciliation
// for the reason a one-shot raise cannot: the cursor this poll maintains is
// otherwise only ever moved forward by deliveries, and two states leave it
// wrong with no delivery to fix them.
//
//   - An OWED sequence the durable will never redeliver — a consumer deleted
//     and recreated, a stream purged under a live process — would pin the floor
//     below it forever, so every reader that consults the cursor refuses
//     forever. A drained durable owes nothing BY DEFINITION, which makes a
//     caught-up observation the one moment the owed set can be cleared
//     wholesale. That is also what bounds the map: it cannot grow without a
//     drained observation eventually emptying it.
//   - A process that reaches Ready through the HANDLER never ran the head
//     raise at all, and one whose first head read failed reached Ready with the
//     cursor wherever the handler left it. Both are corrected by the next
//     drained observation.
//
// A head read that FAILS skips the tick entirely — it never falls through to
// the drained branch, because retiring to a head nobody read is retiring to
// zero, and clearing the owed set on the strength of a failed observation would
// free debts the index still owes. Ready is not held hostage to that failure
// either: the handler path signals it independently, and a later successful
// tick signals it here.
func (b *Bootstrapper) pollProgress(ctx context.Context) {
	ticker := time.NewTicker(bootstrapReadyPoll)
	defer ticker.Stop()
	slowed := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		// Slow the tick the first time round after Ready — by either path, so
		// this covers the handler's signal as well as this loop's own.
		if !slowed && b.isReady() {
			ticker.Reset(bootstrapReadyPoll * bootstrapProgressPollFactor)
			slowed = true
		}

		// The HEAD IS READ FIRST, and the drained check second. A message
		// committed between the two reads is above the head this pass would
		// raise to, so the worst it costs is a cursor one message stale —
		// refusing more, never less. Reading the head after the check would
		// invert that: the head would then include a commit the consumer had
		// not been asked about, and the cursor would claim the index reflects
		// an edge it has never seen. That inversion is worst at startup, which
		// is the one moment this poll is load-bearing for correctness rather
		// than for reconciliation.
		last, serr := b.conn.StreamLastSequence(ctx, b.streamName)
		if serr != nil {
			if ctx.Err() == nil {
				slog.Warn("adjacency bootstrap: could not read the stream head — the applied-sequence cursor stays where it is",
					"stream", b.streamName, "err", serr)
			}
			continue
		}
		caughtUp, err := b.conn.ConsumerCaughtUp(ctx, b.streamName, adjConsumerName)
		if err != nil {
			// The durable may not be created yet (RunDurableConsumer creates
			// it as it starts) — keep polling.
			continue
		}
		if !caughtUp {
			continue
		}

		// A drained consumer has retired everything the stream held at the read
		// above and owes nothing at all. Both facts are applied together: the
		// owed set is cleared (releasing any debt no redelivery will ever
		// retire) and the cursor is raised to that head. Raised BEFORE Ready is
		// signalled so a reader released by Ready sees the cursor.
		b.reconcileToDrainedHead(last)
		b.signalReady()
	}
}

// handle processes one delivered Core KV message and returns its disposition.
// It signals Ready when the in-message pending count is zero — delivery-based
// "zero lag" (ADR-8) — after disposition, so Ready reflects that the last
// message pending at startup has been delivered.
func (b *Bootstrapper) handle(ctx context.Context, msg substrate.Message) substrate.Decision {
	decision := b.processMsg(ctx, msg)
	// Ack and Term both RETIRE a message: the index will never do more with it
	// than it has now. An Ack applied it; a Term discarded it, and every Term
	// shape here is a message no consumer of this index could ever apply (an
	// undecodable body, a key the KV client refuses), so waiting for it would
	// be waiting forever. A Nak leaves the message OWED — redelivered later,
	// its edge still missing — so it caps the cursor until it retires.
	switch decision {
	case substrate.Ack, substrate.Term:
		b.retire(msg.Sequence)
	default:
		b.markOwed(msg.Sequence)
	}
	if msg.NumPending == 0 {
		b.signalReady()
	}
	return decision
}

// AppliedSeq reports the CONTIGUOUS Core KV stream sequence the adjacency
// index has been brought up to: every message at or below it has been retired,
// and nothing is promised above it. 0 means nothing has been retired, or the
// oldest owed message is the first one.
//
// It is a FLOOR, not a maximum, and the difference is the whole point. A Nak'd
// message is redelivered later while the sequences above it keep retiring, so
// the maximum would sit at the head while the index is missing the edge that
// message carries — reporting caught-up during exactly the lag a reader
// consults this to detect. The floor stops at the oldest thing still owed.
//
// WHOSE work it describes: the DURABLE's. The consumer is shared and its
// progress is durable, so a process that starts against an already-drained
// consumer legitimately reports work a previous process did — that is the
// index's state, which is what a reader is asking about, not this process's
// biography. It under-states on a second concurrent instance (this process
// sees only its own deliveries), and under-stating refuses more, never less.
//
// Lifetime: per process, never persisted, 0 at start until the first retirement
// or the first caught-up poll. A reader that cannot act on an unknown cursor
// must treat 0 as a refusal, since it is also the never-measured reading.
func (b *Bootstrapper) AppliedSeq() uint64 {
	b.seqMu.Lock()
	defer b.seqMu.Unlock()
	floor := b.maxRetired
	for seq := range b.owed {
		if seq == 0 {
			continue
		}
		if seq-1 < floor {
			floor = seq - 1
		}
	}
	return floor
}

// isReady reports whether Ready has been signalled, without blocking.
func (b *Bootstrapper) isReady() bool {
	select {
	case <-b.ready:
		return true
	default:
		return false
	}
}

// reconcileToDrainedHead applies the two facts a drained observation carries at
// once: the durable owes NOTHING (so every outstanding debt is released), and
// it has retired everything the stream held at head.
//
// Clearing the set is the only thing that can free a debt no redelivery will
// ever retire — a sequence Nak'd by a process whose consumer was then deleted
// and recreated, or whose stream was purged — which would otherwise pin the
// floor below it for the life of the process and make every reader refuse
// forever. It is also what bounds the map: nothing else removes an entry that
// is never redelivered.
//
// The two are applied under one hold of seqMu so no reader can see the cleared
// set without the head that justifies it, or the head without the clear.
func (b *Bootstrapper) reconcileToDrainedHead(head uint64) {
	b.seqMu.Lock()
	clear(b.owed)
	if head > b.maxRetired {
		b.maxRetired = head
	}
	b.seqMu.Unlock()
}

// retire records that seq will never be delivered again — applied, or
// discarded as unappliable — raising the maximum and clearing seq from the
// owed set if a redelivery is what retired it.
func (b *Bootstrapper) retire(seq uint64) {
	b.seqMu.Lock()
	if seq > b.maxRetired {
		b.maxRetired = seq
	}
	delete(b.owed, seq)
	b.seqMu.Unlock()
}

// markOwed records that seq was Nak'd and is still to be applied, so the floor
// stops below it until a redelivery retires it. Idempotent: a message Nak'd
// again on redelivery is the same debt, not a second one.
func (b *Bootstrapper) markOwed(seq uint64) {
	b.seqMu.Lock()
	if b.owed == nil {
		// The zero-value Bootstrapper is usable: nothing else here needs the
		// constructor, so the map is built on first debt rather than made a
		// precondition of calling handle.
		b.owed = map[uint64]struct{}{}
	}
	b.owed[seq] = struct{}{}
	b.seqMu.Unlock()
}

// processMsg classifies one Core KV message and returns its disposition. Link
// envelopes (key shape `lnk.<srcType>.<srcId>.<linkName>.<dstType>.<dstId>`) are
// detected by key shape and bridged to TWO adjacency.CoreKVEvents — one outbound
// from src, one inbound from dst. All other messages go through the legacy
// CoreKVEvent path keyed on `nodeId`.
func (b *Bootstrapper) processMsg(ctx context.Context, msg substrate.Message) substrate.Decision {
	// Recover the Core KV key from the JetStream subject ($KV.<bucket>.<key>).
	key := strings.TrimPrefix(msg.Subject, b.subjectPrefx)

	// Branch on Contract #1 §1.5 key shape BEFORE the empty-body check below:
	// a hard-deleted link key (NATS KV-Delete/Purge) arrives as an empty
	// body — the same wire shape as every other KV tombstone — and unlike a
	// non-link tombstone it carries a retraction the adjacency index must
	// apply. Link envelopes feed the bridge, which classifies its own empty
	// body into that retraction; everything else falls through to the
	// legacy `CoreKVEvent` path below.
	if substrate.ClassifyKey(key) == substrate.KindLink {
		return b.processLinkEnvelope(ctx, msg, key)
	}

	// NATS KV tombstone entries (DEL/PURGE operations) have empty bodies —
	// ack and skip. A link key never reaches this line (branched above).
	if len(msg.Body) == 0 {
		return substrate.Ack
	}

	var evt adjacency.CoreKVEvent
	if jsonErr := json.Unmarshal(msg.Body, &evt); jsonErr != nil {
		slog.Error("adjacency bootstrap: unmarshal event", "err", jsonErr, "subject", msg.Subject)
		return substrate.Term
	}

	// Skip non-edge entries (node-only records carry no NodeID for the adjacency index).
	if evt.NodeID == "" {
		return substrate.Ack
	}

	// Validate NodeID against the NATS-safe token pattern before passing to
	// adjacency.Build, which calls subjects.AdjKey and panics on invalid chars.
	// A single bad message must not crash the bootstrapper goroutine.
	if strings.ContainsAny(evt.NodeID, ".*> \t\n\r") {
		slog.Error("adjacency bootstrap: nodeId contains NATS-reserved characters — discarding",
			"nodeId", evt.NodeID, "subject", msg.Subject)
		return substrate.Term
	}

	if buildErr := adjacency.Build(ctx, b.adjKV, evt); buildErr != nil {
		if substrate.IsInvalidKeyError(buildErr) {
			slog.Error("adjacency bootstrap: build: unwritable key — discarding",
				"err", buildErr, "subject", msg.Subject)
			return substrate.Term
		}
		slog.Error("adjacency bootstrap: build", "err", buildErr, "subject", msg.Subject)
		return substrate.Nak
	}

	// The cypher executor reads this index directly (adjacency.Neighbors) — no
	// write to Core KV is required here.
	return substrate.Ack
}

// processLinkEnvelope translates one Contract #1 link envelope into two
// directional adjacency.CoreKVEvents (outbound from src, inbound from dst) and
// feeds them to adjacency.Build. The link key is its own EdgeID (Contract #1
// link keys are globally unique).
func (b *Bootstrapper) processLinkEnvelope(ctx context.Context, msg substrate.Message, key string) substrate.Decision {
	srcType, srcID, linkName, dstType, dstID, ok := substrate.ParseLinkKey(key)
	if !ok {
		// Defensive — ClassifyKey already gated on this; never reachable.
		slog.Error("adjacency bootstrap: link bridge: ParseLinkKey failed after ClassifyKey pass", "key", key)
		return substrate.Term
	}

	// A hard-deleted link key (NATS KV-Delete/Purge) is an empty body — the
	// wire shape processMsg branched here on. It is a SECOND retraction
	// transport alongside the JSON envelope's own `isDeleted` field (the
	// soft tombstone): either shape removes the edge from both endpoints'
	// adjacency documents the same way, so an empty body maps straight to
	// isDeleted without attempting to unmarshal a body that plainly isn't
	// JSON.
	isDeleted := len(msg.Body) == 0
	if !isDeleted {
		// Pull the `isDeleted` field out of the value envelope. We only need
		// that one field; an inline struct keeps the bridge cheap and
		// decoupled from the full LinkEnvelope shape.
		var meta struct {
			IsDeleted bool `json:"isDeleted"`
		}
		if jsonErr := json.Unmarshal(msg.Body, &meta); jsonErr != nil {
			slog.Error("adjacency bootstrap: link bridge: unmarshal envelope", "err", jsonErr, "key", key)
			return substrate.Term
		}
		isDeleted = meta.IsDeleted
	}

	// Emit both directional events. The link key serves as a unique EdgeID per
	// Decision #1 (Contract #1 link keys are globally unique).
	for _, evt := range adjacency.EventsForLink(key, srcType, srcID, linkName, dstType, dstID, isDeleted) {
		if buildErr := adjacency.Build(ctx, b.adjKV, evt); buildErr != nil {
			if substrate.IsInvalidKeyError(buildErr) {
				slog.Error("adjacency bootstrap: link bridge: build: unwritable key — discarding",
					"err", buildErr, "key", key, "nodeId", evt.NodeID, "direction", evt.Direction)
				return substrate.Term
			}
			slog.Error("adjacency bootstrap: link bridge: build",
				"err", buildErr, "key", key, "nodeId", evt.NodeID, "direction", evt.Direction)
			return substrate.Nak
		}
	}

	return substrate.Ack
}

func (b *Bootstrapper) signalReady() {
	b.once.Do(func() {
		close(b.ready)
		slog.Info("adjacency bootstrap: complete, rule consumers may start")
	})
}
