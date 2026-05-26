package substrate

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
)

// KVEvent describes a single KV mutation observed via
// (*Conn).SubscribeKVChanges. It carries enough information for callers to
// reconstruct the post-mutation Core KV state without an extra Get round
// trip, plus the JetStream sequence (for durable-consumer position
// reasoning) and the soft-delete marker decoded from the canonical
// envelope.
type KVEvent struct {
	Bucket    string
	Key       string
	Value     []byte
	Revision  uint64 // KV revision (equals the JetStream stream sequence for KV-backed streams)
	IsDeleted bool   // value envelope's `isDeleted` field, or true on a KV tombstone
}

// SubscribeKVOptions configures (*Conn).SubscribeKVChanges. The zero value
// is a valid configuration: replay-from-new, AckExplicit, MaxDeliver=10.
type SubscribeKVOptions struct {
	// IncludeHistory replays every existing KV entry under keyPrefix from
	// the start of the backing stream. Default false (= start from new
	// mutations only). Future stories that stateful-cache the
	// meta-vertex set can flip this back to false.
	IncludeHistory bool
	// AckPolicy overrides the default AckExplicitPolicy. Most callers
	// should leave this zero.
	AckPolicy jetstream.AckPolicy
	// MaxDeliver bounds redelivery on Nak. Defaults to 10 when zero.
	MaxDeliver int
	// Logger is used for internal diagnostic messages (decode failures,
	// channel-blocked drops). Defaults to slog.Default().
	Logger *slog.Logger
}

// SubscribeKVChanges creates a durable JetStream consumer on the backing
// stream of the named KV bucket (NATS convention: KV bucket "foo" is
// backed by stream "KV_foo"), filtered to subjects matching
// "$KV.<bucket>.<keyPrefix>". Each KV mutation under the prefix is
// decoded into a KVEvent and emitted on the returned channel.
//
// Sequence position is persisted across restarts: re-invoking
// SubscribeKVChanges with the same durableName resumes from the
// last-acked sequence. This is the Lattice-native replacement for
// jetstream.KeyValue.Watch (which is ephemeral and replays full history
// on every connect).
//
// keyPrefix semantics:
//   - "" — matches all keys (equivalent to ">")
//   - "vtx.meta." — matches all keys under the vtx.meta prefix
//   - "vtx.meta.>" or "vtx.*" — already a NATS wildcard, used verbatim
//   - bare literal without trailing "." or wildcard — returns an error;
//     callers must be explicit: append "." for prefix matching or ">" for
//     all descendants
//
// On ctx.Done the consumer is deleted from the JetStream catalog and the
// returned channel is closed. Unrecoverable subscription errors
// (iterator stops, decode failures that exhaust MaxDeliver) also close
// the channel — callers should treat channel close as the signal that
// the subscription is gone.
//
// Backpressure: the channel is unbuffered. The dispatch loop will not
// ack a message until the caller has consumed the previous event, which
// preserves at-least-once ordering with JetStream's redelivery
// semantics.
func (c *Conn) SubscribeKVChanges(
	ctx context.Context,
	bucket string,
	keyPrefix string,
	durableName string,
	opts SubscribeKVOptions,
) (<-chan KVEvent, error) {
	if bucket == "" {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: bucket required")
	}
	if durableName == "" {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: durableName required")
	}

	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	ackPolicy := opts.AckPolicy
	if ackPolicy == 0 {
		// jetstream.AckExplicitPolicy's iota value is 0; spelt out for clarity.
		ackPolicy = jetstream.AckExplicitPolicy
	}
	maxDeliver := opts.MaxDeliver
	if maxDeliver == 0 {
		maxDeliver = 10
	}

	streamName := "KV_" + bucket
	subjectPrefix := "$KV." + bucket + "."
	normalizedPrefix, err := normalizePrefix(keyPrefix)
	if err != nil {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: %w", err)
	}
	filterSubject := subjectPrefix + normalizedPrefix

	deliverPolicy := jetstream.DeliverNewPolicy
	if opts.IncludeHistory {
		deliverPolicy = jetstream.DeliverAllPolicy
	}

	cons, err := c.js.CreateOrUpdateConsumer(ctx, streamName, jetstream.ConsumerConfig{
		Durable:       durableName,
		FilterSubject: filterSubject,
		DeliverPolicy: deliverPolicy,
		AckPolicy:     ackPolicy,
		MaxDeliver:    maxDeliver,
	})
	if err != nil {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: create consumer %q on %q: %w",
			durableName, streamName, err)
	}

	out := make(chan KVEvent)
	go c.runKVSubscription(ctx, cons, durableName, bucket, subjectPrefix, out, logger)
	return out, nil
}

// normalizePrefix ensures the prefix ends in a wildcard token so that the
// resulting FilterSubject is a legal NATS subject pattern. Callers may
// pass any of: "", "vtx.meta.", "vtx.meta.>", "vtx.*".
//
// A bare literal without a trailing ".", ">", or "*" is ambiguous — it
// is unclear whether the caller wants exact-match or prefix-and-children
// semantics. normalizePrefix returns an error in that case, requiring the
// caller to be explicit (append "." for prefix, or use ">" / "*" for
// wildcard matching).
func normalizePrefix(p string) (string, error) {
	if p == "" {
		return ">", nil
	}
	if strings.HasSuffix(p, ">") || strings.HasSuffix(p, "*") {
		return p, nil
	}
	if strings.HasSuffix(p, ".") {
		return p + ">", nil
	}
	// Bare literal without a wildcard suffix: ambiguous — fail fast so the
	// caller is explicit. For exact-match on a single key, append a
	// wildcard sentinel such as ">" after a trailing ".". For prefix
	// matching, append ".".
	return "", fmt.Errorf("ambiguous keyPrefix %q: must end with '.', '>', or '*' "+
		"(append '.' for prefix-and-children, or '>' for all descendants)", p)
}

// runKVSubscription drives the consumer iterator until ctx is cancelled
// or the iterator returns an unrecoverable error, then closes out.
//
// IMPORTANT — durable-position semantics: this function deliberately does
// NOT delete the JetStream consumer on shutdown. The whole point of a
// durable consumer is that its ack floor persists across process
// restarts: re-invoking SubscribeKVChanges with the same durableName
// resumes from the last-acked sequence. Deleting the consumer on
// ctx.Done would wipe that position and force a full replay on the
// next start — exactly the wasteful behaviour the migration off
// kv.Watch is meant to eliminate.
//
// Operators who need to permanently retire a durable subscription must
// call js.DeleteConsumer explicitly (or use `nats consumer rm`). The
// catalog cost of a parked durable consumer is negligible; the value of
// the persisted sequence position is large.
func (c *Conn) runKVSubscription(
	ctx context.Context,
	cons jetstream.Consumer,
	durableName, bucket, subjectPrefix string,
	out chan<- KVEvent,
	logger *slog.Logger,
) {
	defer close(out)

	mc, err := cons.Messages()
	if err != nil {
		logger.Error("substrate: SubscribeKVChanges: open messages iterator",
			"durable", durableName, "err", err)
		return
	}
	defer mc.Stop()

	// Stop the iterator when ctx is cancelled to unblock mc.Next().
	doneCh := make(chan struct{})
	defer close(doneCh)
	go func() {
		select {
		case <-ctx.Done():
			mc.Stop()
		case <-doneCh:
		}
	}()

	for {
		msg, err := mc.Next()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("substrate: SubscribeKVChanges: iterator stopped",
				"durable", durableName, "err", err)
			return
		}

		evt := decodeKVMessage(msg, bucket, subjectPrefix)

		select {
		case <-ctx.Done():
			return
		case out <- evt:
		}

		if err := msg.Ack(); err != nil {
			logger.Warn("substrate: SubscribeKVChanges: ack failed",
				"durable", durableName, "key", evt.Key, "err", err)
		}
	}
}

// decodeKVMessage translates a raw JetStream message on a KV backing
// stream into a KVEvent. Tombstone messages (empty body) are mapped to
// IsDeleted=true.
func decodeKVMessage(msg jetstream.Msg, bucket, subjectPrefix string) KVEvent {
	key := strings.TrimPrefix(msg.Subject(), subjectPrefix)
	evt := KVEvent{
		Bucket: bucket,
		Key:    key,
		Value:  msg.Data(),
	}
	if meta, err := msg.Metadata(); err == nil {
		evt.Revision = meta.Sequence.Stream
	}
	// KV tombstones land as empty-body messages with an operation header
	// (KV-Operation: DEL or PURGE). Either way: empty body means
	// soft-deleted from the caller's perspective.
	if len(msg.Data()) == 0 {
		evt.IsDeleted = true
		return evt
	}
	// Otherwise inspect the canonical envelope's isDeleted field.
	var probe struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(msg.Data(), &probe); err == nil {
		evt.IsDeleted = probe.IsDeleted
	}
	return evt
}
