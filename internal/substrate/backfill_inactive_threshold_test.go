package substrate

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate/keys"
)

// quietBackfillLogger discards the sweep's per-consumer diagnostics: these
// vectors assert on the consumer configs the server ends up holding, not on
// what was logged.
func quietBackfillLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

const backfillTestStream = "BACKFILLSYNC"
const backfillTestSubject = "backfillsync.actor"
const backfillTestThreshold = 25 * time.Hour
const backfillTestMaxAckPending = 1000

// newBackfillStream provisions a plain (non-KV) stream with NO consumer
// limits and fills it with n messages — the pre-policy world every consumer
// this helper exists for was created in.
func newBackfillStream(ctx context.Context, t *testing.T, c *Conn, n int) {
	t.Helper()
	if err := c.EnsureStream(ctx, StreamSpec{
		Name:     backfillTestStream,
		Subjects: []string{"backfillsync.>"},
	}); err != nil {
		t.Fatalf("EnsureStream: %v", err)
	}
	for i := 0; i < n; i++ {
		if _, err := c.js.Publish(ctx, backfillTestSubject, []byte{byte('a' + i)}); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}
}

// applyBackfillStreamLimit adopts the consumer-lifetime policy on an existing
// stream, the way ensureSyncStream does on a running deployment. The
// MaxAckPending ceiling rides along because adopting either limit re-validates
// every existing consumer against both, against a zero limit read as an
// allowance of zero (nats-server 2.14 server/stream.go:2433-2434).
func applyBackfillStreamLimit(ctx context.Context, t *testing.T, c *Conn) {
	t.Helper()
	if err := c.EnsureStream(ctx, StreamSpec{
		Name:                      backfillTestStream,
		Subjects:                  []string{"backfillsync.>"},
		ConsumerInactiveThreshold: backfillTestThreshold,
		ConsumerMaxAckPending:     backfillTestMaxAckPending,
	}); err != nil {
		t.Fatalf("EnsureStream with limit: %v", err)
	}
}

func backfillConsumerInfo(ctx context.Context, t *testing.T, c *Conn, durable string) *jetstream.ConsumerInfo {
	t.Helper()
	cons, err := c.js.Consumer(ctx, backfillTestStream, durable)
	if err != nil {
		t.Fatalf("consumer %q: %v", durable, err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info %q: %v", durable, err)
	}
	return info
}

// TestBackfillConsumerInactiveThreshold_PreservesDeliveryPositionAndAckFloor
// is the load-bearing case. A consumer created before the stream declared its
// limit keeps InactiveThreshold=0 forever — a stream limits change only
// validates that no existing consumer EXCEEDS the new value (nats-server 2.14
// server/stream.go:2433-2441), and zero exceeds nothing. The backfill has to
// move it off zero, and it has to do so without disturbing where the consumer
// reads from or how far it has acked: the write-back reuses the read-back
// config precisely because DeliverPolicy and OptStartSeq are not updatable
// (server/consumer.go:2435, :2438), so a rebuilt config would be refused on
// exactly the consumers that started somewhere other than the defaults.
func TestBackfillConsumerInactiveThreshold_PreservesDeliveryPositionAndAckFloor(t *testing.T) {
	c, ctx := newTestConn(t)
	newBackfillStream(ctx, t, c, 6)

	const durable = "edge-sync-AbCdEfGhJkMnPqRsTuVw-ZwVuTsRqPnMkJhGfEdCb"
	cons, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: backfillTestSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:   3,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	// Ack two messages so the consumer carries a non-zero ack floor — the
	// thing a delete-then-create would silently discard. DoubleAck (not Ack)
	// so the ack is confirmed by the server before the "before" snapshot
	// below reads AckFloor: Ack() returns as soon as the ack is written to
	// the client's outbound buffer, racing the server's floor update under
	// load and flaking this test's equality assertion.
	batch, err := cons.Fetch(2)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	acked := 0
	for msg := range batch.Messages() {
		if err := msg.DoubleAck(ctx); err != nil {
			t.Fatalf("ack: %v", err)
		}
		acked++
	}
	if err := batch.Error(); err != nil {
		t.Fatalf("fetch batch: %v", err)
	}
	if acked != 2 {
		t.Fatalf("expected to ack 2 messages, acked %d", acked)
	}

	before := backfillConsumerInfo(ctx, t, c, durable)
	if before.Config.InactiveThreshold != 0 {
		t.Fatalf("precondition: consumer created before the limit must sit at 0, got %v", before.Config.InactiveThreshold)
	}
	if before.AckFloor.Stream == 0 {
		t.Fatal("precondition: the consumer must carry a non-zero ack floor")
	}

	applyBackfillStreamLimit(ctx, t, c)

	// The limit alone does not reach it — this is the whole reason the
	// backfill exists, so assert it rather than assuming it.
	stillZero := backfillConsumerInfo(ctx, t, c, durable)
	if stillZero.Config.InactiveThreshold != 0 {
		t.Fatalf("a stream limit must NOT retro-apply, but the consumer now reads %v", stillZero.Config.InactiveThreshold)
	}

	updated, err := c.BackfillConsumerInactiveThreshold(ctx, backfillTestStream, backfillTestThreshold, quietBackfillLogger())
	if err != nil {
		t.Fatalf("BackfillConsumerInactiveThreshold: %v", err)
	}
	if len(updated) != 1 || updated[0] != durable {
		t.Fatalf("expected the pre-policy consumer to be updated, got %v", updated)
	}

	after := backfillConsumerInfo(ctx, t, c, durable)
	if after.Config.InactiveThreshold != backfillTestThreshold {
		t.Fatalf("InactiveThreshold = %v, want %v", after.Config.InactiveThreshold, backfillTestThreshold)
	}
	if after.Config.DeliverPolicy != jetstream.DeliverByStartSequencePolicy {
		t.Fatalf("DeliverPolicy = %v, want DeliverByStartSequencePolicy", after.Config.DeliverPolicy)
	}
	if after.Config.OptStartSeq != 3 {
		t.Fatalf("OptStartSeq = %d, want 3", after.Config.OptStartSeq)
	}
	if after.AckFloor.Stream != before.AckFloor.Stream {
		t.Fatalf("AckFloor.Stream = %d, want %d — the ack floor did not survive the write-back",
			after.AckFloor.Stream, before.AckFloor.Stream)
	}
	if after.Delivered.Stream != before.Delivered.Stream {
		t.Fatalf("Delivered.Stream = %d, want %d", after.Delivered.Stream, before.Delivered.Stream)
	}
}

// TestBackfillConsumerInactiveThreshold_SecondPassIsANoOp pins the property
// that licenses running this at every boot with no ticker: once a consumer
// carries a threshold it is no longer a candidate, so a repeat pass reports
// nothing and writes nothing.
func TestBackfillConsumerInactiveThreshold_SecondPassIsANoOp(t *testing.T) {
	c, ctx := newTestConn(t)
	newBackfillStream(ctx, t, c, 3)

	const durable = "edge-sync-AbCdEfGhJkMnPqRsTuVw-ZwVuTsRqPnMkJhGfEdCb"
	if _, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: backfillTestSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	}); err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	applyBackfillStreamLimit(ctx, t, c)

	first, err := c.BackfillConsumerInactiveThreshold(ctx, backfillTestStream, backfillTestThreshold, quietBackfillLogger())
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first pass = %v, want the one pre-policy consumer", first)
	}

	second, err := c.BackfillConsumerInactiveThreshold(ctx, backfillTestStream, backfillTestThreshold, quietBackfillLogger())
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("second pass = %v, want nothing", second)
	}
}

// TestBackfillConsumerInactiveThreshold_LeavesAnExplicitThresholdAlone: a
// host that declares its own shorter lifetime (the browser shell's 30
// minutes) has already answered the question this sweep asks, and must not be
// widened to the stream's ceiling.
func TestBackfillConsumerInactiveThreshold_LeavesAnExplicitThresholdAlone(t *testing.T) {
	c, ctx := newTestConn(t)
	newBackfillStream(ctx, t, c, 3)
	applyBackfillStreamLimit(ctx, t, c)

	const declared = "edge-sync-AbCdEfGhJkMnPqRsTuVw-BrowserShe11Device1x"
	const inherited = "edge-sync-AbCdEfGhJkMnPqRsTuVw-ZwVuTsRqPnMkJhGfEdCb"
	if _, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
		Durable:           declared,
		FilterSubject:     backfillTestSubject,
		AckPolicy:         jetstream.AckExplicitPolicy,
		InactiveThreshold: 30 * time.Minute,
	}); err != nil {
		t.Fatalf("create declared consumer: %v", err)
	}
	// A consumer created AFTER the limit with no threshold of its own already
	// inherits the stream's, so it is not a candidate either.
	if _, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
		Durable:       inherited,
		FilterSubject: backfillTestSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	}); err != nil {
		t.Fatalf("create inheriting consumer: %v", err)
	}

	updated, err := c.BackfillConsumerInactiveThreshold(ctx, backfillTestStream, backfillTestThreshold, quietBackfillLogger())
	if err != nil {
		t.Fatalf("BackfillConsumerInactiveThreshold: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("updated = %v, want nothing — every consumer already carries a threshold", updated)
	}
	if got := backfillConsumerInfo(ctx, t, c, declared).Config.InactiveThreshold; got != 30*time.Minute {
		t.Fatalf("declared consumer's threshold = %v, want 30m", got)
	}
	if got := backfillConsumerInfo(ctx, t, c, inherited).Config.InactiveThreshold; got != backfillTestThreshold {
		t.Fatalf("inheriting consumer's threshold = %v, want %v", got, backfillTestThreshold)
	}
}

// TestBackfillConsumerInactiveThreshold_EnumerationFailureTouchesNothing: an
// enumeration that did not complete must surface as an error, never as an
// empty pass that quietly reports success. Both ways the enumeration can fail
// — the stream is unreadable, or the context is already done — are checked,
// and the live consumer's own config is asserted unchanged.
func TestBackfillConsumerInactiveThreshold_EnumerationFailureTouchesNothing(t *testing.T) {
	c, ctx := newTestConn(t)
	newBackfillStream(ctx, t, c, 3)

	const durable = "edge-sync-AbCdEfGhJkMnPqRsTuVw-ZwVuTsRqPnMkJhGfEdCb"
	if _, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: backfillTestSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	}); err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	applyBackfillStreamLimit(ctx, t, c)

	updated, err := c.BackfillConsumerInactiveThreshold(ctx, "NO_SUCH_STREAM", backfillTestThreshold, quietBackfillLogger())
	if err == nil {
		t.Fatal("an unreadable stream must return an error, not an empty success")
	}
	if updated != nil {
		t.Fatalf("updated = %v, want nil", updated)
	}

	dead, cancel := context.WithCancel(ctx)
	cancel()
	updated, err = c.BackfillConsumerInactiveThreshold(dead, backfillTestStream, backfillTestThreshold, quietBackfillLogger())
	if err == nil {
		t.Fatal("a cancelled enumeration must return an error, not an empty success")
	}
	if updated != nil {
		t.Fatalf("updated = %v, want nil", updated)
	}

	if got := backfillConsumerInfo(ctx, t, c, durable).Config.InactiveThreshold; got != 0 {
		t.Fatalf("consumer threshold = %v, want 0 — a failed enumeration must touch nothing", got)
	}
}

// TestBackfillFixtureIdsAreRealNanoIDs: the durable names above are spliced
// from a platform-minted identity and device id, so a fixture using ids the
// NanoID alphabet excludes would be proving something about a name that can
// never occur.
func TestBackfillFixtureIdsAreRealNanoIDs(t *testing.T) {
	for _, id := range []string{
		"AbCdEfGhJkMnPqRsTuVw", "ZwVuTsRqPnMkJhGfEdCb", "BrowserShe11Device1x",
	} {
		if !keys.IsValidNanoID(id) {
			t.Fatalf("fixture id %q is not a valid 20-char NanoID", id)
		}
	}
}

// TestBackfillConsumerInactiveThreshold_ReturnsEveryNameSorted pins two
// things the sweep's contract asserts and nothing else exercises: that the
// enumeration is drained COMPLETELY (every pre-policy consumer is reached in
// one pass, not just the first), and that the returned names come back
// sorted, which is what makes the result comparable across passes and across
// processes.
func TestBackfillConsumerInactiveThreshold_ReturnsEveryNameSorted(t *testing.T) {
	c, ctx := newTestConn(t)
	newBackfillStream(ctx, t, c, 2)

	// Created in an order that is neither sorted nor reverse-sorted, so a
	// missing sort cannot pass by luck.
	names := []string{
		"edge-sync-AbCdEfGhJkMnPqRsTuVw-MdevWWWWWWWWWWWWWWWW",
		"edge-sync-AbCdEfGhJkMnPqRsTuVw-AdevWWWWWWWWWWWWWWWW",
		"edge-sync-AbCdEfGhJkMnPqRsTuVw-ZdevWWWWWWWWWWWWWWWW",
		"edge-sync-AbCdEfGhJkMnPqRsTuVw-BdevWWWWWWWWWWWWWWWW",
	}
	for _, n := range names {
		if _, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
			Durable:       n,
			FilterSubject: backfillTestSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}); err != nil {
			t.Fatalf("create %q: %v", n, err)
		}
	}
	applyBackfillStreamLimit(ctx, t, c)

	updated, err := c.BackfillConsumerInactiveThreshold(ctx, backfillTestStream, backfillTestThreshold, quietBackfillLogger())
	if err != nil {
		t.Fatalf("BackfillConsumerInactiveThreshold: %v", err)
	}
	want := append([]string(nil), names...)
	sort.Strings(want)
	if !slices.Equal(updated, want) {
		t.Fatalf("updated = %v, want every pre-policy consumer, sorted: %v", updated, want)
	}
	for _, n := range names {
		if got := backfillConsumerInfo(ctx, t, c, n).Config.InactiveThreshold; got != backfillTestThreshold {
			t.Fatalf("consumer %q threshold = %v, want %v — the drain did not reach every name", n, got, backfillTestThreshold)
		}
	}
}

// TestDrainConsumerNames_PagingFailureIsAnError pins the branch the two
// enumeration-failure vectors above structurally cannot reach: both of those
// fail at js.Stream, which round-trips (nats.go v1.52.0
// jetstream/jetstream.go:812) and so consumes any dead context before paging
// begins. Driving the drain with an already-resolved stream handle is the
// only way to make the lister itself fail, and a swallowed lister error is
// exactly the shape that turns a short listing into a confident "nothing to
// do".
func TestDrainConsumerNames_PagingFailureIsAnError(t *testing.T) {
	c, ctx := newTestConn(t)
	newBackfillStream(ctx, t, c, 1)
	if _, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
		Durable:       "edge-sync-AbCdEfGhJkMnPqRsTuVw-ZwVuTsRqPnMkJhGfEdCb",
		FilterSubject: backfillTestSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	}); err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	stream, err := c.js.Stream(ctx, backfillTestStream)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if names, err := drainConsumerNames(ctx, stream); err != nil || len(names) != 1 {
		t.Fatalf("precondition: drain = %v, %v; want the one consumer and no error", names, err)
	}

	dead, cancel := context.WithCancel(ctx)
	cancel()
	names, err := drainConsumerNames(dead, stream)
	if err == nil {
		t.Fatal("a paging failure must be returned, not reported as a complete empty listing")
	}
	if names != nil {
		t.Fatalf("names = %v, want nil on a failed enumeration", names)
	}
}

// backfillReasonRecorder captures the sweep's per-consumer diagnostics. A
// pass that skipped a consumer and one that aborted at it produce the same
// empty result; only the reasons tell them apart.
type backfillReasonRecorder struct {
	mu   sync.Mutex
	msgs []string
}

func (h *backfillReasonRecorder) Enabled(context.Context, slog.Level) bool { return true }
func (h *backfillReasonRecorder) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *backfillReasonRecorder) WithGroup(string) slog.Handler            { return h }
func (h *backfillReasonRecorder) Handle(_ context.Context, rec slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, rec.Message)
	return nil
}
func (h *backfillReasonRecorder) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, m := range h.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

// TestBackfillConsumerInactiveThreshold_AnUpdateFailureDoesNotAbortThePass:
// one consumer that cannot be written must cost only itself. Driven with a
// threshold above the stream's own ceiling, which the server refuses per
// consumer (nats-server v2.14.0 server/consumer.go:843-844) — the realistic
// shape being a caller whose constant drifted above the stream's.
//
// The reason COUNT is the assertion that matters: an aborting pass and a
// continuing pass both return an empty slice, and only "one warning per
// consumer" shows the loop kept going.
func TestBackfillConsumerInactiveThreshold_AnUpdateFailureDoesNotAbortThePass(t *testing.T) {
	c, ctx := newTestConn(t)
	newBackfillStream(ctx, t, c, 2)

	names := []string{
		"edge-sync-AbCdEfGhJkMnPqRsTuVw-AdevWWWWWWWWWWWWWWWW",
		"edge-sync-AbCdEfGhJkMnPqRsTuVw-BdevWWWWWWWWWWWWWWWW",
		"edge-sync-AbCdEfGhJkMnPqRsTuVw-CdevWWWWWWWWWWWWWWWW",
	}
	for _, n := range names {
		if _, err := c.js.CreateOrUpdateConsumer(ctx, backfillTestStream, jetstream.ConsumerConfig{
			Durable:       n,
			FilterSubject: backfillTestSubject,
			AckPolicy:     jetstream.AckExplicitPolicy,
		}); err != nil {
			t.Fatalf("create %q: %v", n, err)
		}
	}
	applyBackfillStreamLimit(ctx, t, c)

	reasons := &backfillReasonRecorder{}
	updated, err := c.BackfillConsumerInactiveThreshold(ctx, backfillTestStream,
		backfillTestThreshold+time.Hour, slog.New(reasons))
	if err != nil {
		t.Fatalf("a per-consumer update failure must not fail the call: %v", err)
	}
	if len(updated) != 0 {
		t.Fatalf("updated = %v, want nothing — every write-back was refused", updated)
	}
	if got := reasons.count("substrate: BackfillConsumerInactiveThreshold: update failed"); got != len(names) {
		t.Fatalf("update-failure warnings = %d, want one per consumer (%d) — the pass aborted instead of continuing",
			got, len(names))
	}
	for _, n := range names {
		if got := backfillConsumerInfo(ctx, t, c, n).Config.InactiveThreshold; got != 0 {
			t.Fatalf("consumer %q threshold = %v, want 0 — a refused write-back must change nothing", n, got)
		}
	}
}
