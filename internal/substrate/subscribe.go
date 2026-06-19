package substrate

import (
	"context"
	"encoding/json"
	"errors"
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

// PruneStaleDurables deletes every durable JetStream consumer on the named
// KV bucket's backing stream (KV_<bucket>) whose name starts with namePrefix,
// except keep. It is intended for the per-boot-durable pattern used by
// SubscribeKVChanges callers that derive a fresh durable name on every
// process start (e.g. "<prefix>-<instance>"): each boot prunes durables left
// behind by prior, no-longer-running instances before creating its own.
//
// A consumer-not-found error from a concurrent deletion (another instance
// pruning the same stale name) is not an error. Any other deletion error is
// logged and otherwise ignored — pruning is best-effort cleanup, never a
// reason to fail startup.
func (c *Conn) PruneStaleDurables(ctx context.Context, bucket, namePrefix, keep string, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	streamName := "KV_" + bucket
	stream, err := c.js.Stream(ctx, streamName)
	if err != nil {
		return fmt.Errorf("substrate: PruneStaleDurables: get stream %q: %w", streamName, err)
	}
	lister := stream.ConsumerNames(ctx)
	for name := range lister.Name() {
		if name == keep || !strings.HasPrefix(name, namePrefix) {
			continue
		}
		if err := c.js.DeleteConsumer(ctx, streamName, name); err != nil {
			if errors.Is(err, jetstream.ErrConsumerNotFound) {
				continue
			}
			logger.Warn("substrate: PruneStaleDurables: delete stale durable failed",
				"stream", streamName, "durable", name, "err", err)
		} else {
			logger.Info("substrate: pruned stale durable", "stream", streamName, "durable", name)
		}
	}
	if err := lister.Err(); err != nil {
		return fmt.Errorf("substrate: PruneStaleDurables: list consumers on %q: %w", streamName, err)
	}
	return nil
}

// DeleteDurable removes a single named durable JetStream consumer from the
// named KV bucket's backing stream (KV_<bucket>). It is intended for clean
// shutdown of a per-boot durable created by SubscribeKVChanges: the caller
// deletes its own durable so it does not linger as a stale entry for the next
// boot's PruneStaleDurables to clean up.
//
// A consumer-not-found error is not an error (already gone).
func (c *Conn) DeleteDurable(ctx context.Context, bucket, durableName string) error {
	streamName := "KV_" + bucket
	if err := c.js.DeleteConsumer(ctx, streamName, durableName); err != nil {
		if errors.Is(err, jetstream.ErrConsumerNotFound) {
			return nil
		}
		return fmt.Errorf("substrate: DeleteDurable: delete %q on %q: %w", durableName, streamName, err)
	}
	return nil
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

// WatchKVUpdates returns a channel of KVEvents for every key mutation in bucket
// that occurs AFTER the call (updates-only — no history replay), driven by an
// ephemeral JetStream KV watcher. It is the substrate-typed equivalent of
// jetstream.KeyValue.WatchAll(UpdatesOnly()): nothing is persisted, so on a
// reconnect the caller resumes from "now", not from a durable position. Use
// SubscribeKVChanges instead when a durable, restart-resumable position is
// required.
//
// The channel is closed when ctx is cancelled or the underlying watcher stops
// (e.g. a transient NATS disconnect). The caller treats a closed channel as the
// signal to reconnect by calling WatchKVUpdates again with a live ctx. The
// watcher is stopped when the goroutine exits, so a closed channel leaks
// nothing.
func (c *Conn) WatchKVUpdates(ctx context.Context, bucket string) (<-chan KVEvent, error) {
	kv, err := c.bucket(ctx, bucket)
	if err != nil {
		return nil, err
	}
	watcher, err := kv.WatchAll(ctx, jetstream.UpdatesOnly())
	if err != nil {
		return nil, fmt.Errorf("substrate: WatchKVUpdates %s: %w", bucket, err)
	}
	out := make(chan KVEvent)
	go func() {
		defer close(out)
		defer func() { _ = watcher.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-watcher.Updates():
				if !ok {
					return
				}
				if entry == nil {
					// End-of-initial-replay sentinel. With UpdatesOnly there is no
					// replay, but jetstream still emits one nil to mark "caught up".
					continue
				}
				select {
				case <-ctx.Done():
					return
				case out <- kvEventFromUpdate(entry, bucket):
				}
			}
		}
	}()
	return out, nil
}

// kvEventFromUpdate translates a KV watcher entry into a KVEvent, mirroring
// decodeKVMessage's tombstone handling: a Delete/Purge operation (or an
// isDeleted envelope on a live Put) maps to IsDeleted=true.
func kvEventFromUpdate(entry jetstream.KeyValueEntry, bucket string) KVEvent {
	evt := KVEvent{
		Bucket:   bucket,
		Key:      entry.Key(),
		Value:    entry.Value(),
		Revision: entry.Revision(),
	}
	if entry.Operation() != jetstream.KeyValuePut {
		evt.IsDeleted = true
		return evt
	}
	var probe struct {
		IsDeleted bool `json:"isDeleted"`
	}
	if err := json.Unmarshal(entry.Value(), &probe); err == nil {
		evt.IsDeleted = probe.IsDeleted
	}
	return evt
}
