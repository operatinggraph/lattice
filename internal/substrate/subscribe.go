package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

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
	// MaxDeliver bounds redelivery on Nak. Defaults to 10 when zero. A
	// NEGATIVE value is passed through as JetStream's unlimited redelivery,
	// which is the right posture for a subscription whose events carry state
	// with no other write path: a message Termed by an exhausted delivery
	// budget is silently gone, and the derived state built from the stream is
	// wrong from then on with nothing left to repair it.
	MaxDeliver int
	// AckWait is how long JetStream waits for an ack before redelivering. Zero
	// leaves JetStream's 30s default, which is right for a caller that drains
	// the returned channel promptly and wrong for one that does real work per
	// event: the dispatch side is strictly serial, so a consumer spending
	// longer than AckWait on one event has it redelivered WHILE STILL BEING
	// HANDLED, burning a delivery against MaxDeliver every time.
	//
	// Setting it is not sufficient on its own — see MaxPrefetch. Mirrors
	// DurableConsumerConfig.AckWait (consumer.go) deliberately: same shape,
	// same hazard, same answer.
	AckWait time.Duration
	// MaxPrefetch caps how many messages the client pulls ahead of the
	// caller's consumption. Zero leaves nats.go's DefaultMaxMessages (500),
	// which AckWait alone cannot rescue: the ack clock starts when a message
	// is DELIVERED INTO THE CLIENT BUFFER, not when the caller receives it, so
	// on a DeliverAll replay a queued message can exhaust any AckWait before
	// anyone has looked at it. A caller doing real work per event should set
	// 1, so the ack clock starts when the work does.
	MaxPrefetch int
	// OnReopen, when set, is called each time the messages iterator is
	// re-opened after a transient stall — each time, that is, that this
	// subscription had a gap it could not see through. Nil (the default)
	// means nobody is watching and a reopen stays silent, exactly as it is
	// for every caller that does not set it.
	//
	// It exists because "the channel never closed" and "I have missed
	// nothing" are different claims. No message is LOST across a reopen: the
	// ack floor is the position, and everything undelivered comes back. But
	// between the stall and that redelivery the caller is blind, so a caller
	// that derives a FRESHNESS claim from this stream — "what I hold is
	// current" — must withdraw it for the gap, where a caller deriving only
	// eventual state can ignore the reopen entirely. That asymmetry is why
	// this is opt-in rather than a channel-close.
	//
	// Invoked on the subscription's own goroutine, before the reopen, holding
	// nothing. It must be prompt and must not block: this subscription cannot
	// re-open, and so cannot deliver anything, until it returns.
	OnReopen func()
	// Logger is used for internal diagnostic messages (decode failures,
	// channel-blocked drops). Defaults to slog.Default().
	Logger *slog.Logger
}

// SubscribeKVChanges creates a durable JetStream consumer on the backing
// stream of the named KV bucket (NATS convention: KV bucket "foo" is
// backed by stream "KV_foo"), filtered to subjects matching
// "$KV.<bucket>.<keyPrefix>" for each entry of keyPrefixes. Each KV mutation
// under any of the prefixes is decoded into a KVEvent and emitted on the
// returned channel.
//
// Sequence position is persisted across restarts: re-invoking
// SubscribeKVChanges with the same durableName resumes from the
// last-acked sequence. This is the Lattice-native replacement for
// jetstream.KeyValue.Watch (which is ephemeral and replays full history
// on every connect).
//
// keyPrefixes must be non-empty. Each entry follows the same semantics:
//   - "" — matches all keys (equivalent to ">")
//   - "vtx.meta." — matches all keys under the vtx.meta prefix
//   - "vtx.meta.>" or "vtx.*" — already a NATS wildcard, used verbatim
//   - bare literal without trailing "." or wildcard — returns an error;
//     callers must be explicit: append "." for prefix matching or ">" for
//     all descendants
//
// A single-element slice produces a ConsumerConfig using the singular
// FilterSubject field, never the plural FilterSubjects — nats-server treats
// the two as different, mutually exclusive configs (CreateOrUpdateConsumer's
// response even echoes them back differently), so a one-prefix subscription's
// consumer config carries exactly the shape a single prefix needs, no more.
// FilterSubjects (plural) is used only when len(keyPrefixes) > 1.
//
// On ctx.Done the returned channel is closed, but the durable consumer is
// PRESERVED in the JetStream catalog — its acked position is the point of
// durability, so re-invoking with the same durableName resumes from it (the
// runKVSubscription loop only closes the channel; it never deletes the durable).
// Deleting the durable is an explicit, separate call (DeleteDurable /
// PruneStaleDurables / DeleteStreamConsumer).
//
// The channel also closes when the subscription is TERMINALLY gone — the
// server-side consumer deleted or not found, or a malformed request
// (terminalSubscriptionError). Callers may therefore treat channel close as
// "this feed is over" and act on it. A merely stalled iterator does NOT close
// it: the iterator is re-opened against the same durable, which resumes from
// its ack floor, so an idle stream's missed heartbeat costs a reopen rather
// than the subscription.
//
// Backpressure: the channel is unbuffered. The dispatch loop will not
// ack a message until the caller has consumed the previous event, which
// preserves at-least-once ordering with JetStream's redelivery
// semantics.
func (c *Conn) SubscribeKVChanges(
	ctx context.Context,
	bucket string,
	keyPrefixes []string,
	durableName string,
	opts SubscribeKVOptions,
) (<-chan KVEvent, error) {
	if bucket == "" {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: bucket required")
	}
	if durableName == "" {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: durableName required")
	}
	if len(keyPrefixes) == 0 {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: at least one keyPrefix required")
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
	normalizedPrefixes := make([]string, len(keyPrefixes))
	for i, kp := range keyPrefixes {
		np, err := normalizePrefix(kp)
		if err != nil {
			return nil, fmt.Errorf("substrate: SubscribeKVChanges: %w", err)
		}
		normalizedPrefixes[i] = np
	}

	deliverPolicy := jetstream.DeliverNewPolicy
	if opts.IncludeHistory {
		deliverPolicy = jetstream.DeliverAllPolicy
	}

	cfg := jetstream.ConsumerConfig{
		Durable:       durableName,
		DeliverPolicy: deliverPolicy,
		AckPolicy:     ackPolicy,
		MaxDeliver:    maxDeliver,
		AckWait:       opts.AckWait,
	}
	if len(normalizedPrefixes) == 1 {
		cfg.FilterSubject = subjectPrefix + normalizedPrefixes[0]
	} else {
		filterSubjects := make([]string, len(normalizedPrefixes))
		for i, np := range normalizedPrefixes {
			filterSubjects[i] = subjectPrefix + np
		}
		cfg.FilterSubjects = filterSubjects
	}

	cons, err := c.js.CreateOrUpdateConsumer(ctx, streamName, cfg)
	if err != nil {
		return nil, fmt.Errorf("substrate: SubscribeKVChanges: create consumer %q on %q: %w",
			durableName, streamName, err)
	}

	out := make(chan KVEvent)
	go c.runKVSubscription(ctx, cons, durableName, bucket, subjectPrefix, opts.MaxPrefetch, opts.OnReopen, out, logger)
	return out, nil
}

// PruneStaleDurableAge is the recency threshold below which a candidate
// durable is treated as a live sibling's, not a crashed boot's leftover, and
// skipped by PruneStaleDurables. A live sibling's consumer is continuously
// created/delivered-to well inside this window; a crashed boot's durable
// stops updating the instant its process died, so it only ever lingers one
// threshold longer than before — negligible catalog cost (design
// refractor-lens-registry-restart-integrity-design.md §4 Fire A step 2).
// A var, not a const, and exported: PruneStaleDurables' callers (Loom,
// Weaver, Chronicler, Refractor) each need their own tests to shrink it
// rather than waiting out the real window — production code never
// reassigns it.
var PruneStaleDurableAge = 10 * time.Minute

// PruneStaleDurables deletes every durable JetStream consumer on the named
// KV bucket's backing stream (KV_<bucket>) whose name starts with namePrefix,
// except keep, AND whose ConsumerInfo shows no creation or delivery activity
// within PruneStaleDurableAge. It is intended for the per-boot-durable
// pattern used by SubscribeKVChanges callers that derive a fresh durable
// name on every process start (e.g. "<prefix>-<instance>-<nonce>"): each
// boot prunes durables left behind by prior, no-longer-running instances
// before creating its own. The age guard is what makes this safe under
// concurrent instances of the same prefix — without it, instance A's boot
// would delete instance B's still-live durable mid-run, and B's watch dies
// silently (the exact failure class this pattern exists to prevent,
// inflicted by a sibling instead of a stale ack floor).
//
// A consumer-not-found error from a concurrent deletion (another instance
// pruning the same stale name) is not an error. A ConsumerInfo lookup
// failure is treated as "recently active" (skip, don't prune) — fail-closed
// against wrongly deleting a live sibling on a transient info-fetch error.
// Any other deletion error is logged and otherwise ignored — pruning is
// best-effort cleanup, never a reason to fail startup.
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
		if c.durableRecentlyActive(ctx, streamName, name, logger) {
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

// durableRecentlyActive reports whether the named consumer was created or
// last delivered a message within PruneStaleDurableAge. An info-fetch error
// (including consumer-not-found — already gone, nothing to prune) is treated
// as "recently active" so the caller skips it rather than risking a wrongful
// delete.
func (c *Conn) durableRecentlyActive(ctx context.Context, streamName, name string, logger *slog.Logger) bool {
	cons, err := c.js.Consumer(ctx, streamName, name)
	if err != nil {
		logger.Warn("substrate: PruneStaleDurables: consumer lookup failed, skipping",
			"stream", streamName, "durable", name, "err", err)
		return true
	}
	info, err := cons.Info(ctx)
	if err != nil {
		logger.Warn("substrate: PruneStaleDurables: consumer info failed, skipping",
			"stream", streamName, "durable", name, "err", err)
		return true
	}
	now := time.Now()
	if now.Sub(info.Created) < PruneStaleDurableAge {
		return true
	}
	if info.Delivered.Last != nil && now.Sub(*info.Delivered.Last) < PruneStaleDurableAge {
		return true
	}
	return false
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

// DeleteOrphanDurables deletes every durable JetStream consumer on the named
// KV bucket's backing stream (KV_<bucket>) that orphaned reports true for,
// and returns the names it deleted, sorted.
//
// It is the reconciliation counterpart to PruneStaleDurables: that one asks
// "is this durable of my per-boot family and idle?", this one hands the whole
// question to the caller, because the owner of a durable family is the only
// thing that can say whether a given name still has a live owner. The caller's
// predicate therefore carries the entire safety burden — it must answer false
// for any name it does not positively recognize as its own AND orphaned, since
// a true answer deletes a server-side consumer along with its ack floor.
//
// Deletion is best-effort per name: a consumer-not-found error (a concurrent
// deletion won the race) is success, any other delete error is logged and the
// pass continues, so one undeletable consumer never aborts the reconciliation.
// A failure to LIST, by contrast, is returned — an empty or partial listing
// must never read as "nothing to do".
func (c *Conn) DeleteOrphanDurables(ctx context.Context, bucket string, orphaned func(name string) bool, logger *slog.Logger) ([]string, error) {
	if logger == nil {
		logger = slog.Default()
	}
	streamName := "KV_" + bucket
	stream, err := c.js.Stream(ctx, streamName)
	if err != nil {
		return nil, fmt.Errorf("substrate: DeleteOrphanDurables: get stream %q: %w", streamName, err)
	}

	// Drain the listing fully before consulting the predicate: orphaned may do
	// its own I/O to reach a verdict, and running it inside the range would
	// hold the lister's paging subscription open across every one of those
	// round trips.
	var names []string
	lister := stream.ConsumerNames(ctx)
	for name := range lister.Name() {
		names = append(names, name)
	}
	if err := lister.Err(); err != nil {
		return nil, fmt.Errorf("substrate: DeleteOrphanDurables: list consumers on %q: %w", streamName, err)
	}

	var candidates []string
	for _, name := range names {
		if orphaned(name) {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)

	var deleted []string
	for _, name := range candidates {
		if err := c.js.DeleteConsumer(ctx, streamName, name); err != nil {
			if errors.Is(err, jetstream.ErrConsumerNotFound) {
				continue
			}
			logger.Warn("substrate: DeleteOrphanDurables: delete orphan durable failed",
				"stream", streamName, "durable", name, "err", err)
			continue
		}
		logger.Info("substrate: deleted orphan durable", "stream", streamName, "durable", name)
		deleted = append(deleted, name)
	}
	return deleted, nil
}

// DeleteStreamConsumer removes a single named durable consumer from an event
// stream (not a KV bucket — use DeleteDurable for those). It is the seam for a
// one-time durable migration: retiring a superseded consumer whose un-acked
// messages then redeliver to the replacement durables. Idempotent — a
// consumer-not-found error is treated as success (already gone / never existed),
// so it is safe to call unconditionally on every startup.
func (c *Conn) DeleteStreamConsumer(ctx context.Context, stream, durableName string) error {
	if err := c.js.DeleteConsumer(ctx, stream, durableName); err != nil {
		if errors.Is(err, jetstream.ErrConsumerNotFound) {
			return nil
		}
		return fmt.Errorf("substrate: DeleteStreamConsumer: delete %q on %q: %w", durableName, stream, err)
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

// subscriptionReopenBackoff is the pause before re-opening the messages
// iterator after a TRANSIENT failure, so a server that is refusing pull
// requests is not hammered. Sized well under any caller's own liveness
// window: the reopen is the repair, and a slow repair is a blind window for
// whatever the caller derives from this stream.
const subscriptionReopenBackoff = 250 * time.Millisecond

// consumerExistsProbeTimeout bounds the "is my consumer still there" question
// asked before a reopen. Short: it runs on a path that is already failing, and
// an unanswered probe is treated as "still there" (keep reopening), which is
// the recoverable direction.
const consumerExistsProbeTimeout = 5 * time.Second

// terminalSubscriptionError reports whether an iterator error ITSELF says the
// subscription is gone rather than momentarily unhappy.
//
// It is a fast path, not the authority — see subscriptionIsGone, which exists
// because this classification is not decidable from the error alone. Only
// errors that name the consumer's absence, or a malformed request, qualify:
// neither is repaired by asking again. Everything else — most importantly
// jetstream.ErrNoHeartbeat, which nats.go raises whenever it sees no traffic
// for two heartbeat intervals and which parseMessagesOpts arms by DEFAULT
// (ReportMissingHeartbeats) — is a stall on its face, and treating a stall as
// death is how an idle stream retires a subscription that had nothing wrong
// with it.
func terminalSubscriptionError(err error) bool {
	return errors.Is(err, jetstream.ErrConsumerDeleted) ||
		errors.Is(err, jetstream.ErrConsumerNotFound) ||
		errors.Is(err, jetstream.ErrBadRequest)
}

// subscriptionIsGone decides whether to stop reopening, and it asks the SERVER
// rather than trusting the error text.
//
// The error alone is not enough, and the gap is not hypothetical: a consumer
// deleted out from under a live iterator very often surfaces as
// jetstream.ErrNoHeartbeat, because the server simply stops answering and the
// client's heartbeat monitor is what notices. That error is indistinguishable
// from an idle stream's routine stall — so a classifier reading only the error
// reopens forever against a consumer that no longer exists, and the caller,
// whose whole contract is "channel close means the feed is over", is never
// told. (internal/refractor/lens is that caller: channel-close is what latches
// its taxonomy resolver permanently unarmed.)
//
// So the fast path is the error, and the authority is a ConsumerInfo lookup: a
// consumer-not-found answer is proof of death, and every other outcome —
// including a probe that cannot complete because the connection is down —
// resolves to "keep trying", the direction that costs a retry rather than a
// subscription.
func (c *Conn) subscriptionIsGone(ctx context.Context, cons jetstream.Consumer, err error) bool {
	if terminalSubscriptionError(err) {
		return true
	}
	probeCtx, cancel := context.WithTimeout(ctx, consumerExistsProbeTimeout)
	defer cancel()
	_, infoErr := cons.Info(probeCtx)
	return errors.Is(infoErr, jetstream.ErrConsumerNotFound)
}

// runKVSubscription drives the consumer iterator until ctx is cancelled
// or the subscription is terminally gone, then closes out. A transient
// iterator failure re-opens the iterator against the same durable instead,
// which resumes from its ack floor — mirroring runDurableConsumer's own
// reopen loop (consumer.go), for the same reason: the position is the
// durable's, not the iterator's, so re-opening costs nothing and losing the
// subscription over a heartbeat gap costs the caller its whole feed.
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
	maxPrefetch int,
	onReopen func(),
	out chan<- KVEvent,
	logger *slog.Logger,
) {
	defer close(out)

	var msgOpts []jetstream.PullMessagesOpt
	if maxPrefetch > 0 {
		msgOpts = append(msgOpts, jetstream.PullMaxMessages(maxPrefetch))
	}

	for {
		if ctx.Err() != nil {
			return
		}
		stopped, err := c.pumpKVSubscription(ctx, cons, durableName, bucket, subjectPrefix, msgOpts, out, logger)
		if stopped {
			return
		}
		// A closed connection ends the loop, and it is checked BEFORE the
		// is-it-gone probe because that probe cannot answer over a closed
		// connection and resolves an unanswerable question to "keep trying" —
		// the right default for a stall, and an infinite one here. There is
		// nothing to re-open against and never will be: nats.go reports
		// IsClosed only after a deliberate Close or after the reconnect budget
		// is exhausted, neither of which a later attempt repairs. Without this
		// the loop spins at the backoff interval for as long as the caller's
		// ctx outlives its connection — which for every test that closes a
		// fixture connection before cancelling its context is the rest of the
		// process, burning CPU that starves whatever runs alongside it.
		if c.nc.IsClosed() {
			logger.Debug("substrate: SubscribeKVChanges: connection closed, closing the event channel",
				"durable", durableName, "err", err)
			return
		}
		if c.subscriptionIsGone(ctx, cons, err) {
			logger.Error("substrate: SubscribeKVChanges: subscription is gone, closing the event channel",
				"durable", durableName, "err", err)
			return
		}
		logger.Warn("substrate: SubscribeKVChanges: iterator stalled, re-opening against the same durable",
			"durable", durableName, "err", err)
		// Announced BEFORE the backoff and the reopen, not after: the gap has
		// already happened by the time we get here, and a caller whose
		// freshness claim is now false should be told while it is false rather
		// than once the feed is healthy again. Everything undelivered still
		// redelivers from the ack floor — what is being reported is the blind
		// window, not a loss.
		if onReopen != nil {
			onReopen()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(subscriptionReopenBackoff):
		}
	}
}

// pumpKVSubscription opens one messages iterator and drives it until it
// fails or ctx is cancelled. stopped is true when the caller must give up
// entirely (ctx done); otherwise err is why this iterator ended and the
// caller decides whether that is terminal.
//
// The iterator is a per-attempt resource — opened, deferred-stopped, and
// discarded here — which is what keeps the ctx-cancellation watcher below
// bound to the iterator it can actually stop. A single long-lived watcher
// over a variable holding "the current iterator" would be reading that
// variable without synchronisation across reopens.
func (c *Conn) pumpKVSubscription(
	ctx context.Context,
	cons jetstream.Consumer,
	durableName, bucket, subjectPrefix string,
	msgOpts []jetstream.PullMessagesOpt,
	out chan<- KVEvent,
	logger *slog.Logger,
) (stopped bool, err error) {
	mc, err := cons.Messages(msgOpts...)
	if err != nil {
		return false, fmt.Errorf("open messages iterator: %w", err)
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
		msg, nextErr := mc.Next()
		if nextErr != nil {
			if ctx.Err() != nil {
				return true, nil
			}
			return false, nextErr
		}

		evt := decodeKVMessage(msg, bucket, subjectPrefix)

		select {
		case <-ctx.Done():
			return true, nil
		case out <- evt:
		}

		if ackErr := msg.Ack(); ackErr != nil {
			logger.Warn("substrate: SubscribeKVChanges: ack failed",
				"durable", durableName, "key", evt.Key, "err", ackErr)
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
