package substrate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
)

// provisionPipelineStream creates a plain JetStream stream over `pipeline.>`,
// the subject space the pipelined-publish tests below publish onto. No atomic
// publish, no TTL: a PublishPipeline is an ordinary publisher and must need
// nothing of the stream that Publish would not.
func provisionPipelineStream(ctx context.Context, t *testing.T, c *Conn) jetstream.Stream {
	t.Helper()
	s, err := c.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "pipelined",
		Subjects:  []string{"pipeline.>"},
		Retention: jetstream.LimitsPolicy,
		MaxAge:    time.Hour,
	})
	if err != nil {
		t.Fatalf("create pipelined stream: %v", err)
	}
	return s
}

// drainPipelineStream consumes up to want messages off the stream and returns
// their subjects and stream sequences in delivery order.
func drainPipelineStream(ctx context.Context, t *testing.T, c *Conn, durable string, want int) (subjects []string, seqs []uint64) {
	t.Helper()
	cons, err := c.JetStream().CreateOrUpdateConsumer(ctx, "pipelined", jetstream.ConsumerConfig{
		Durable:        durable,
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{"pipeline.>"},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateConsumer: %v", err)
	}
	batch, err := cons.Fetch(want, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for m := range batch.Messages() {
		md, mderr := m.Metadata()
		if mderr != nil {
			t.Fatalf("Metadata: %v", mderr)
		}
		subjects = append(subjects, m.Subject())
		seqs = append(seqs, md.Sequence.Stream)
		_ = m.Ack()
	}
	return subjects, seqs
}

// TestPublishPipeline_OrderPreserved is the property every caller of a pipeline
// depends on: pipelining bounds when acks are awaited, not the order messages
// land in. The stream sequences must be strictly increasing in the order Add
// was called, exactly as a series of synchronous Publish calls produces.
func TestPublishPipeline_OrderPreserved(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	const n = 40
	pl := c.NewPublishPipeline(0)
	var want []string
	for i := 0; i < n; i++ {
		subj := fmt.Sprintf("pipeline.row.%02d", i)
		want = append(want, subj)
		if err := pl.Add(ctx, subj, []byte(fmt.Sprintf(`{"i":%d}`, i)), nil); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got, seqs := drainPipelineStream(ctx, t, c, "order", n)
	if len(got) != n {
		t.Fatalf("consumed %d messages, want %d", len(got), n)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("message %d subject = %q, want %q (send order not preserved)", i, got[i], want[i])
		}
		if i > 0 && seqs[i] <= seqs[i-1] {
			t.Fatalf("message %d sequence %d not greater than message %d's %d", i, seqs[i], i-1, seqs[i-1])
		}
	}
}

// TestPublishPipeline_HeaderForwardedAndDefaultWindow pins that Add builds the
// message the same way Publish does, and that window <= 0 takes the package
// default rather than an unbounded or zero-length window (a zero window would
// await every message inside Add, which is the synchronous publish path wearing
// a pipeline's name).
func TestPublishPipeline_HeaderForwardedAndDefaultWindow(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	pl := c.NewPublishPipeline(0)
	if pl.window != DefaultPublishPipelineWindow {
		t.Fatalf("window = %d, want the %d default", pl.window, DefaultPublishPipelineWindow)
	}
	if err := pl.Add(ctx, "pipeline.hdr", []byte(`{"x":1}`), map[string]string{"X-Lattice-RequestId": "req-1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	cons, err := c.JetStream().CreateOrUpdateConsumer(ctx, "pipelined", jetstream.ConsumerConfig{
		Durable:        "hdr",
		AckPolicy:      jetstream.AckExplicitPolicy,
		FilterSubjects: []string{"pipeline.>"},
	})
	if err != nil {
		t.Fatalf("CreateOrUpdateConsumer: %v", err)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got := msg.Headers().Get("X-Lattice-RequestId"); got != "req-1" {
		t.Fatalf("X-Lattice-RequestId = %q, want req-1", got)
	}
	_ = msg.Ack()
}

// TestPublishPipeline_FlushReturnsFirstError publishes two good messages, one to a
// subject no stream binds, then two more good ones. Flush must name the failed
// subject, and the good messages ahead of it must still have landed — the
// partial-application property that separates a publish PIPELINE from
// Conn.PublishBatch's atomic BATCH, and the reason a failed flush has to be
// disposed of as a redelivery rather than as "nothing happened".
func TestPublishPipeline_FlushReturnsFirstError(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	pl := c.NewPublishPipeline(0)
	for _, subj := range []string{"pipeline.a", "pipeline.b", "unbound.subject", "pipeline.c", "pipeline.d"} {
		if err := pl.Add(ctx, subj, []byte(`{}`), nil); err != nil {
			t.Fatalf("Add(%s): %v", subj, err)
		}
	}
	err := pl.Flush(ctx)
	if err == nil {
		t.Fatalf("Flush must report the publish to a subject no stream binds")
	}
	if !errors.Is(err, jetstream.ErrNoStreamResponse) {
		t.Fatalf("Flush error = %v, want it to wrap ErrNoStreamResponse", err)
	}
	if want := `"unbound.subject"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("Flush error %q must name the failing subject %s", err, want)
	}

	// Flush left the pipeline empty and reusable, and the four good publishes
	// landed either side of the failure.
	if n := len(pl.pending); n != 0 {
		t.Fatalf("pending = %d after Flush, want 0", n)
	}
	got, _ := drainPipelineStream(ctx, t, c, "firsterr", 4)
	want := []string{"pipeline.a", "pipeline.b", "pipeline.c", "pipeline.d"}
	if len(got) != len(want) {
		t.Fatalf("landed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("landed %v, want %v", got, want)
		}
	}
}

// TestPublishPipeline_WindowBoundsInFlight drives 20 publishes through a window of
// 4 and asserts the pipeline never holds more than 4 futures — the bound that
// keeps a wide actor's write loop far below the JetStream client's shared
// outstanding-async-publish ceiling. It also asserts the window actually FILLS
// (a bound of 4 that never exceeded 1 would pass the inequality while proving
// nothing), and that every message still landed.
func TestPublishPipeline_WindowBoundsInFlight(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	const (
		window = 4
		n      = 20
	)
	pl := c.NewPublishPipeline(window)
	maxPending := 0
	for i := 0; i < n; i++ {
		if err := pl.Add(ctx, fmt.Sprintf("pipeline.w.%02d", i), []byte(`{}`), nil); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
		if got := len(pl.pending); got > window {
			t.Fatalf("after Add(%d) the pipeline held %d futures, above the window of %d", i, got, window)
		} else if got > maxPending {
			maxPending = got
		}
		// The client's own outstanding-ack count can only be lower than the
		// pipeline's pending list (resolved futures stay in the list until
		// awaited), so this is an upper bound that cannot false-fail.
		if got := c.JetStream().PublishAsyncPending(); got > window {
			t.Fatalf("after Add(%d) the client held %d unacknowledged publishes, above the window of %d", i, got, window)
		}
	}
	if maxPending != window {
		t.Fatalf("the pipeline never filled its window: max pending %d, window %d", maxPending, window)
	}
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got, _ := drainPipelineStream(ctx, t, c, "window", n)
	if len(got) != n {
		t.Fatalf("landed %d messages, want %d", len(got), n)
	}
}

// TestPublishPipeline_ReusableAfterFlush pins that one pipeline serves a
// caller's successive write loops: Flush empties it, and a second round of
// publishes through the same pipeline lands normally.
func TestPublishPipeline_ReusableAfterFlush(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	pl := c.NewPublishPipeline(0)
	for _, subj := range []string{"pipeline.r1", "pipeline.r2"} {
		if err := pl.Add(ctx, subj, []byte(`{}`), nil); err != nil {
			t.Fatalf("Add(%s): %v", subj, err)
		}
	}
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("first Flush: %v", err)
	}
	if n := len(pl.pending); n != 0 {
		t.Fatalf("pending = %d after Flush, want 0", n)
	}
	// A second Flush with nothing outstanding is a clean no-op.
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("empty Flush: %v", err)
	}
	for _, subj := range []string{"pipeline.r3", "pipeline.r4"} {
		if err := pl.Add(ctx, subj, []byte(`{}`), nil); err != nil {
			t.Fatalf("Add(%s): %v", subj, err)
		}
	}
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	got, _ := drainPipelineStream(ctx, t, c, "reuse", 4)
	want := []string{"pipeline.r1", "pipeline.r2", "pipeline.r3", "pipeline.r4"}
	if len(got) != len(want) {
		t.Fatalf("landed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("landed %v, want %v", got, want)
		}
	}
}

// TestPublishPipeline_CtxCancelUnblocks covers both cancellation seams: a Flush
// whose ctx is already done reports the cancellation instead of a success it
// can no longer stand behind, and a Flush blocked on a future that has not
// resolved returns rather than hanging. The second half publishes to a subject
// no stream binds, whose future stays unresolved across the client's
// no-responder retry window.
func TestPublishPipeline_CtxCancelUnblocks(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	cancelled, cancel := context.WithCancel(ctx)
	pl := c.NewPublishPipeline(0)
	if err := pl.Add(ctx, "pipeline.cancel", []byte(`{}`), nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cancel()
	err := pl.Flush(cancelled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Flush on a cancelled ctx = %v, want context.Canceled", err)
	}
	if n := len(pl.pending); n != 0 {
		t.Fatalf("pending = %d after a cancelled Flush, want 0 — the pipeline must not hold futures it will never await", n)
	}

	// A cancel arriving while Flush is blocked on an unresolved future must
	// also return. The test's own deadline is the hang detector.
	blocking, cancelBlocking := context.WithCancel(ctx)
	pl2 := c.NewPublishPipeline(0)
	if err := pl2.Add(ctx, "unbound.cancel", []byte(`{}`), nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- pl2.Flush(blocking) }()
	cancelBlocking()
	select {
	case ferr := <-done:
		if ferr == nil {
			t.Fatalf("Flush of a publish no stream accepts must report an error")
		}
	case <-ctx.Done():
		t.Fatalf("Flush did not return after its ctx was cancelled")
	}
}

// TestPublishPipeline_AddNeverSurfacesAnotherMessagesFailure is the invariant
// that keeps a caller's per-message error handling honest: with a window of 1
// and a first publish no stream accepts, the SECOND Add is the call that awaits
// the failed ack — and it must still return nil, because the failure belongs to
// message one. Handing it back would let the caller charge it to message two,
// dispose of message two, and then find a clean Flush (the failed future having
// already been consumed) and report success for a batch that lost a row.
func TestPublishPipeline_AddNeverSurfacesAnotherMessagesFailure(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	pl := c.NewPublishPipeline(1)
	if err := pl.Add(ctx, "unbound.first", []byte(`{}`), nil); err != nil {
		t.Fatalf("Add(first): %v", err)
	}
	// Window full: this Add awaits the first message's ack, which fails.
	if err := pl.Add(ctx, "pipeline.second", []byte(`{}`), nil); err != nil {
		t.Fatalf("Add(second) returned %v — a window await's failure belongs to the EARLIER message and must not be charged to this one", err)
	}
	if err := pl.Add(ctx, "pipeline.third", []byte(`{}`), nil); err != nil {
		t.Fatalf("Add(third): %v", err)
	}

	err := pl.Flush(ctx)
	if err == nil {
		t.Fatalf("Flush must report the failure Add recorded")
	}
	if !errors.Is(err, jetstream.ErrNoStreamResponse) {
		t.Fatalf("Flush error = %v, want it to wrap ErrNoStreamResponse", err)
	}
	if want := `"unbound.first"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("Flush error %q must name the message that actually failed, %s", err, want)
	}
	// Recorded errors are reported once and cleared with everything else.
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("second Flush = %v, want nil — a recorded error is reported once", err)
	}
	got, _ := drainPipelineStream(ctx, t, c, "recorded", 2)
	if len(got) != 2 {
		t.Fatalf("landed %v, want the two publishes the recorded failure did not stop", got)
	}
}

// TestPublishPipeline_RecordedErrorLosesToNothing pins the ordering between the
// two error sources: a failure Add recorded happened before any ack Flush
// awaits, so it is the one reported. Reporting the later one would name a
// consequence and hide the cause.
func TestPublishPipeline_RecordedErrorLosesToNothing(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	pl := c.NewPublishPipeline(1)
	for _, subj := range []string{"unbound.early", "unbound.late", "pipeline.ok"} {
		if err := pl.Add(ctx, subj, []byte(`{}`), nil); err != nil {
			t.Fatalf("Add(%s): %v", subj, err)
		}
	}
	err := pl.Flush(ctx)
	if err == nil {
		t.Fatalf("Flush must report a failure")
	}
	if want := `"unbound.early"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("Flush error %q must name the EARLIEST failure, %s", err, want)
	}
}

// TestPublishPipeline_PendingTracksOutstandingAcks pins the accessor callers use
// to order work after a pipeline's writes: it counts what has been issued and
// not awaited, and a Flush drives it to zero.
func TestPublishPipeline_PendingTracksOutstandingAcks(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	provisionPipelineStream(ctx, t, c)

	pl := c.NewPublishPipeline(0)
	if got := pl.Pending(); got != 0 {
		t.Fatalf("Pending() = %d on a fresh pipeline, want 0", got)
	}
	for i := 0; i < 5; i++ {
		if err := pl.Add(ctx, fmt.Sprintf("pipeline.p%d", i), []byte(`{}`), nil); err != nil {
			t.Fatalf("Add(%d): %v", i, err)
		}
	}
	if got := pl.Pending(); got != 5 {
		t.Fatalf("Pending() = %d after 5 un-awaited publishes, want 5", got)
	}
	if err := pl.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := pl.Pending(); got != 0 {
		t.Fatalf("Pending() = %d after Flush, want 0", got)
	}
}

// TestPublishPipeline_UnackedPublishTimesOutAtFlush pins the bound that keeps a
// flush from hanging forever. A stream configured NoAck accepts the publish and
// never acknowledges it — the shape a stream leader lost between accept and ack
// produces — so without an async ack timeout the future never resolves and
// Flush blocks for the life of the process, holding its slot in the
// connection's pending budget. The connection sets one, so the failure arrives
// as ErrAsyncPublishTimeout.
//
// It costs the timeout once, deliberately: the bound is the thing under test,
// and a shorter test-only knob would pin a value production does not run.
func TestPublishPipeline_UnackedPublishTimesOutAtFlush(t *testing.T) {
	t.Parallel()
	c, ctx := newTestConn(t)
	if _, err := c.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "noack",
		Subjects:  []string{"noack.>"},
		Retention: jetstream.LimitsPolicy,
		NoAck:     true,
	}); err != nil {
		t.Fatalf("create noack stream: %v", err)
	}

	deadline, cancel := context.WithTimeout(context.Background(), publishAsyncTimeout+15*time.Second)
	defer cancel()

	pl := c.NewPublishPipeline(0)
	if err := pl.Add(deadline, "noack.row", []byte(`{}`), nil); err != nil {
		t.Fatalf("Add: %v", err)
	}
	start := time.Now()
	err := pl.Flush(deadline)
	if err == nil {
		t.Fatalf("Flush of a publish that is never acked must fail, not return clean")
	}
	if !errors.Is(err, jetstream.ErrAsyncPublishTimeout) {
		t.Fatalf("Flush error = %v, want it to wrap ErrAsyncPublishTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > publishAsyncTimeout+10*time.Second {
		t.Fatalf("Flush took %s, well past the %s bound — the timeout is not the thing that released it", elapsed, publishAsyncTimeout)
	}
	if got := pl.Pending(); got != 0 {
		t.Fatalf("Pending() = %d after a timed-out Flush, want 0 — a resolved future must not keep its slot", got)
	}
}

// TestPublishAsyncPosture_AppliedByBothConstructors pins that the async-publish
// bounds reach every substrate connection, not just the one Connect builds.
// Wrap is how the whole test corpus and several components obtain a Conn, and a
// Wrap that skipped the options would leave those processes with unbounded acks
// and nats.go's smaller pending ceiling — invisible until a flush hung.
//
// Both halves assert it the only way it is observable: a publish onto a stream
// that accepts and never acknowledges must resolve as ErrAsyncPublishTimeout.
// The ctx deadline sits well past the bound, so a connection built without the
// option fails here with a ctx error rather than passing.
func TestPublishAsyncPosture_AppliedByBothConstructors(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*publishAsyncTimeout+30*time.Second)
	defer cancel()

	connected, err := Connect(ctx, ConnectOpts{URL: url, Name: "posture-connect"})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer connected.Close()
	if _, err := connected.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      "posture-noack",
		Subjects:  []string{"posture.>"},
		Retention: jetstream.LimitsPolicy,
		NoAck:     true,
	}); err != nil {
		t.Fatalf("create posture-noack stream: %v", err)
	}

	wrapped, err := Wrap(natsfixture.Connect(t, url))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}

	// One 5s bound per constructor, run together rather than in series.
	var wg sync.WaitGroup
	for _, tc := range []struct {
		name    string
		conn    *Conn
		subject string
	}{
		{"Connect", connected, "posture.connect"},
		{"Wrap", wrapped, "posture.wrap"},
	} {
		if got := tc.conn.PublishAsyncPending(); got != 0 {
			t.Fatalf("%s: PublishAsyncPending() = %d on a fresh connection, want 0", tc.name, got)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			pl := tc.conn.NewPublishPipeline(0)
			if err := pl.Add(ctx, tc.subject, []byte(`{}`), nil); err != nil {
				t.Errorf("%s: Add: %v", tc.name, err)
				return
			}
			ferr := pl.Flush(ctx)
			if !errors.Is(ferr, jetstream.ErrAsyncPublishTimeout) {
				t.Errorf("%s: Flush = %v, want ErrAsyncPublishTimeout — this constructor is not applying the async-publish bounds", tc.name, ferr)
			}
			if got := pl.Pending(); got != 0 {
				t.Errorf("%s: Pending() = %d after a timed-out Flush, want 0", tc.name, got)
			}
		}()
	}
	wg.Wait()

	if PublishAsyncMaxPending < DefaultPublishPipelineWindow {
		t.Fatalf("the connection's pending ceiling (%d) is below one pipeline's window (%d)", PublishAsyncMaxPending, DefaultPublishPipelineWindow)
	}
}
