package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
)

const (
	adjConsumerName    = "refractor-adjacency"
	bootstrapReconnect = 5 * time.Second
)

// Bootstrapper drives the dedicated adjacency consumer on the Core KV stream
// until its pending message count reaches zero, then closes the Ready channel.
// It continues running thereafter to keep the adjacency index current (ADR-7, ADR-8).
// After adjacency is updated for a node, rule pipelines are notified via their
// adjacency KV watch (ADR-16) — no writes to Core KV are required.
type Bootstrapper struct {
	js           jetstream.JetStream
	streamName   string
	filterSubj   string
	bucket       string
	subjectPrefx string // "$KV.<bucket>." — strip this from msg.Subject() to recover the Core KV key
	adjKV        jetstream.KeyValue
	ready        chan struct{}
	once         sync.Once
}

// NewBootstrapper creates a Bootstrapper that reads from coreKVBucket via the
// dedicated adjacency durable consumer and writes edge index entries to adjKV.
func NewBootstrapper(js jetstream.JetStream, coreKVBucket string, adjKV jetstream.KeyValue) *Bootstrapper {
	return &Bootstrapper{
		js:           js,
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

// Run creates the durable adjacency consumer (idempotent), processes Core KV
// messages through adjacency.Build(), and closes Ready() when lag reaches zero.
// It continues consuming until ctx is cancelled. Run blocks until ctx is done.
func (b *Bootstrapper) Run(ctx context.Context) error {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, b.streamName, jetstream.ConsumerConfig{
		Durable:       adjConsumerName,
		FilterSubject: b.filterSubj,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return fmt.Errorf("adjacency bootstrap: create consumer: %w", err)
	}

	// Signal ready immediately if the stream is already empty. This is the only
	// place we use cons.Info() — before any acks are in flight, so the count is
	// authoritative with no async-ack race.
	info, err := cons.Info(ctx)
	if err != nil {
		slog.Warn("adjacency bootstrap: initial lag check failed", "err", err)
	} else if info.NumPending == 0 {
		b.signalReady()
	}

	b.loop(ctx, cons)
	return nil
}

// loop reopens the message iterator on transient errors until ctx is done.
func (b *Bootstrapper) loop(ctx context.Context, cons jetstream.Consumer) {
	for {
		if ctx.Err() != nil {
			return
		}
		mc, err := cons.Messages()
		if err != nil {
			slog.Error("adjacency bootstrap: open messages iterator", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(bootstrapReconnect):
			}
			continue
		}
		b.drain(ctx, mc)
	}
}

// drain reads messages from mc until ctx is cancelled or mc returns an error.
// Lag detection uses msg.Metadata().NumPending — the authoritative pending count
// NATS embeds in each message at delivery time — rather than a separate
// cons.Info() round-trip, which suffers from async-ack races on slow networks.
func (b *Bootstrapper) drain(ctx context.Context, mc jetstream.MessagesContext) {
	defer mc.Stop()
	for {
		msg, err := mc.Next()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("adjacency bootstrap: receive error, will reconnect", "err", err)
			return
		}

		// Capture the in-message pending count before any disposition.
		// NumPending == 0 means this was the last message in the stream at
		// delivery time; signalling ready after any disposition is correct
		// because ADR-8 defines "zero lag" as delivery-based, not success-based.
		meta, metaErr := msg.Metadata()

		b.processMsg(ctx, msg)

		if metaErr == nil && meta.NumPending == 0 {
			b.signalReady()
		}
	}
}

// processMsg applies the appropriate disposition to a single Core KV message.
// Link envelopes (key shape `lnk.<srcType>.<srcId>.<linkName>.<dstType>.<dstId>`)
// are detected by key shape and bridged to TWO adjacency.CoreKVEvents — one
// outbound from src, one inbound from dst. All other messages go through the
// legacy CoreKVEvent path keyed on `nodeId`.
func (b *Bootstrapper) processMsg(ctx context.Context, msg jetstream.Msg) {
	// NATS KV tombstone entries (DEL/PURGE operations) have empty bodies — ack and skip.
	if len(msg.Data()) == 0 {
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("adjacency bootstrap: ack tombstone", "err", ackErr)
		}
		return
	}

	// Recover the Core KV key from the JetStream subject ($KV.<bucket>.<key>).
	key := strings.TrimPrefix(msg.Subject(), b.subjectPrefx)

	// Branch on Contract #1 §1.5 key shape. Link envelopes feed the bridge;
	// everything else falls through to the legacy `CoreKVEvent` path.
	if substrate.ClassifyKey(key) == substrate.KindLink {
		b.processLinkEnvelope(ctx, msg, key)
		return
	}

	var evt adjacency.CoreKVEvent
	if jsonErr := json.Unmarshal(msg.Data(), &evt); jsonErr != nil {
		slog.Error("adjacency bootstrap: unmarshal event", "err", jsonErr, "subject", msg.Subject())
		if termErr := msg.Term(); termErr != nil {
			slog.Error("adjacency bootstrap: term failed", "err", termErr)
		}
		return
	}

	// Skip non-edge entries (node-only records carry no NodeID for the adjacency index).
	if evt.NodeID == "" {
		if ackErr := msg.Ack(); ackErr != nil {
			slog.Error("adjacency bootstrap: ack non-edge entry", "err", ackErr)
		}
		return
	}

	// Validate NodeID against the NATS-safe token pattern before passing to
	// adjacency.Build, which calls subjects.AdjKey and panics on invalid chars.
	// A single bad message must not crash the bootstrapper goroutine.
	if strings.ContainsAny(evt.NodeID, ".*> \t\n\r") {
		slog.Error("adjacency bootstrap: nodeId contains NATS-reserved characters — discarding",
			"nodeId", evt.NodeID, "subject", msg.Subject())
		if termErr := msg.Term(); termErr != nil {
			slog.Error("adjacency bootstrap: term failed", "err", termErr)
		}
		return
	}

	if buildErr := adjacency.Build(ctx, b.adjKV, evt); buildErr != nil {
		slog.Error("adjacency bootstrap: build", "err", buildErr, "subject", msg.Subject())
		if nakErr := msg.Nak(); nakErr != nil {
			slog.Error("adjacency bootstrap: nak failed", "err", nakErr)
		}
		return
	}

	// Rule pipelines are notified of the adjacency update via their adjKV watch
	// (ADR-16) — no write to Core KV is required here.

	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("adjacency bootstrap: ack failed", "err", ackErr)
	}
}

// processLinkEnvelope translates one Contract #1 link envelope into two
// directional adjacency.CoreKVEvents (outbound from src, inbound from dst)
// and feeds them to adjacency.Build. The link key is its own EdgeID
// (Contract #1 link keys are globally unique).
func (b *Bootstrapper) processLinkEnvelope(ctx context.Context, msg jetstream.Msg, key string) {
	srcType, srcID, linkName, dstType, dstID, ok := substrate.ParseLinkKey(key)
	if !ok {
		// Defensive — ClassifyKey already gated on this; never reachable.
		slog.Error("adjacency bootstrap: link bridge: ParseLinkKey failed after ClassifyKey pass", "key", key)
		if termErr := msg.Term(); termErr != nil {
			slog.Error("adjacency bootstrap: term failed", "err", termErr)
		}
		return
	}

	// Pull the `isDeleted` field out of the value envelope. We only need
	// that one field; an inline struct keeps the bridge cheap and decoupled
	// from the full LinkEnvelope shape.
	var meta struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if jsonErr := json.Unmarshal(msg.Data(), &meta); jsonErr != nil {
		slog.Error("adjacency bootstrap: link bridge: unmarshal envelope", "err", jsonErr, "key", key)
		if termErr := msg.Term(); termErr != nil {
			slog.Error("adjacency bootstrap: term failed", "err", termErr)
		}
		return
	}

	// Emit both directional events. The link key serves as a unique EdgeID
	// per Decision #1 (Contract #1 link keys are globally unique).
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
			if nakErr := msg.Nak(); nakErr != nil {
				slog.Error("adjacency bootstrap: nak failed", "err", nakErr)
			}
			return
		}
	}

	if ackErr := msg.Ack(); ackErr != nil {
		slog.Error("adjacency bootstrap: link bridge: ack failed", "err", ackErr)
	}
}

func (b *Bootstrapper) signalReady() {
	b.once.Do(func() {
		close(b.ready)
		slog.Info("adjacency bootstrap: complete, rule consumers may start")
	})
}
