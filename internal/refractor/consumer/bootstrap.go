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
	// bootstrapReadyPoll is how often pollReady checks the durable's pending
	// count to detect the empty-stream "already caught up" case.
	bootstrapReadyPoll = 100 * time.Millisecond
)

// Bootstrapper drives the dedicated adjacency consumer on the Core KV stream
// until its pending message count reaches zero, then closes the Ready channel.
// It continues running thereafter to keep the adjacency index current (ADR-7, ADR-8).
// After adjacency is updated for a node, rule pipelines are notified via their
// adjacency KV watch (ADR-16) — no writes to Core KV are required.
type Bootstrapper struct {
	conn         *substrate.Conn
	streamName   string
	filterSubj   string
	bucket       string
	subjectPrefx string // "$KV.<bucket>." — strip this from msg.Subject to recover the Core KV key
	adjKV        *substrate.KV
	ready        chan struct{}
	once         sync.Once
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
// non-empty-stream case), and a startup poll closes it when the durable reports
// zero pending without ever delivering a message (the empty-stream case).
func (b *Bootstrapper) Run(ctx context.Context) error {
	go b.pollReady(ctx)
	return b.conn.RunDurableConsumer(ctx, substrate.DurableConsumerConfig{
		Stream:        b.streamName,
		FilterSubject: b.filterSubj,
		Durable:       adjConsumerName,
	}, b.handle)
}

// pollReady covers the empty-stream case: RunDurableConsumer never invokes the
// handler when there is nothing to deliver, so Ready would never fire from the
// handler. The poll closes Ready once the durable is fully caught up — both
// NumPending and NumAckPending zero (ConsumerCaughtUp), which is immediate for an
// empty stream. The ack-aware check is essential: NumPending alone drops the
// instant a backlog is prefetched into the client buffer, so signalling on
// NumPending==0 would fire Ready while the handler is still building the
// adjacency index for that prefetched batch — closing the gate on a partial
// index. Requiring NumAckPending==0 means pollReady never fires mid-drain; on a
// non-empty stream the handler's msg.NumPending==0 path (delivery-accurate,
// raised after the last edge is built) signals first. pollReady exits once Ready
// is signalled (by either path) or ctx is done.
func (b *Bootstrapper) pollReady(ctx context.Context) {
	ticker := time.NewTicker(bootstrapReadyPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.ready:
			return
		case <-ticker.C:
			caughtUp, err := b.conn.ConsumerCaughtUp(ctx, b.streamName, adjConsumerName)
			if err != nil {
				// The durable may not be created yet (RunDurableConsumer creates
				// it as it starts) — keep polling.
				continue
			}
			if caughtUp {
				b.signalReady()
				return
			}
		}
	}
}

// handle processes one delivered Core KV message and returns its disposition.
// It signals Ready when the in-message pending count is zero — delivery-based
// "zero lag" (ADR-8) — after disposition, so Ready reflects that the last
// message pending at startup has been delivered.
func (b *Bootstrapper) handle(ctx context.Context, msg substrate.Message) substrate.Decision {
	decision := b.processMsg(ctx, msg)
	if msg.NumPending == 0 {
		b.signalReady()
	}
	return decision
}

// processMsg classifies one Core KV message and returns its disposition. Link
// envelopes (key shape `lnk.<srcType>.<srcId>.<linkName>.<dstType>.<dstId>`) are
// detected by key shape and bridged to TWO adjacency.CoreKVEvents — one outbound
// from src, one inbound from dst. All other messages go through the legacy
// CoreKVEvent path keyed on `nodeId`.
func (b *Bootstrapper) processMsg(ctx context.Context, msg substrate.Message) substrate.Decision {
	// NATS KV tombstone entries (DEL/PURGE operations) have empty bodies — ack and skip.
	if len(msg.Body) == 0 {
		return substrate.Ack
	}

	// Recover the Core KV key from the JetStream subject ($KV.<bucket>.<key>).
	key := strings.TrimPrefix(msg.Subject, b.subjectPrefx)

	// Branch on Contract #1 §1.5 key shape. Link envelopes feed the bridge;
	// everything else falls through to the legacy `CoreKVEvent` path.
	if substrate.ClassifyKey(key) == substrate.KindLink {
		return b.processLinkEnvelope(ctx, msg, key)
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
		slog.Error("adjacency bootstrap: build", "err", buildErr, "subject", msg.Subject)
		return substrate.Nak
	}

	// Rule pipelines are notified of the adjacency update via their adjKV watch
	// (ADR-16) — no write to Core KV is required here.
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

	// Pull the `isDeleted` field out of the value envelope. We only need that one
	// field; an inline struct keeps the bridge cheap and decoupled from the full
	// LinkEnvelope shape.
	var meta struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if jsonErr := json.Unmarshal(msg.Body, &meta); jsonErr != nil {
		slog.Error("adjacency bootstrap: link bridge: unmarshal envelope", "err", jsonErr, "key", key)
		return substrate.Term
	}

	// Emit both directional events. The link key serves as a unique EdgeID per
	// Decision #1 (Contract #1 link keys are globally unique).
	outbound := adjacency.CoreKVEvent{
		CoreKvKey:   key,
		EdgeID:      key,
		Name:        linkName,
		Direction:   "outbound",
		NodeID:      srcID,
		OtherNodeID: dstID,
		OtherType:   dstType,
		IsDeleted:   meta.IsDeleted,
	}
	inbound := adjacency.CoreKVEvent{
		CoreKvKey:   key,
		EdgeID:      key,
		Name:        linkName,
		Direction:   "inbound",
		NodeID:      dstID,
		OtherNodeID: srcID,
		OtherType:   srcType,
		IsDeleted:   meta.IsDeleted,
	}

	for _, evt := range []adjacency.CoreKVEvent{outbound, inbound} {
		if buildErr := adjacency.Build(ctx, b.adjKV, evt); buildErr != nil {
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
