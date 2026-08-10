package substrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
)

// TestTerminalSubscriptionError_ClassifiesStallsApartFromDeath pins the
// decision the reopen loop turns on, and it is a decision with a sharp cost on
// both sides. Closing the channel is how a caller learns its feed is over —
// internal/refractor/lens latches its taxonomy resolver permanently unarmed on
// it — so classifying a STALL as death retires a working subscription and
// leaves the caller degraded until the process restarts. Classifying real
// death as a stall is the opposite: an endless reopen against a consumer that
// is not coming back, with the caller never told.
//
// jetstream.ErrNoHeartbeat is the case that makes this worth a test rather
// than a default: nats.go raises it whenever it sees no traffic for two
// heartbeat intervals, and parseMessagesOpts arms that reporting by DEFAULT
// (ReportMissingHeartbeats), so on an idle stream it is a routine event, not a
// fault at all.
func TestTerminalSubscriptionError_ClassifiesStallsApartFromDeath(t *testing.T) {
	t.Parallel()

	terminal := map[string]error{
		"consumer deleted":   jetstream.ErrConsumerDeleted,
		"consumer not found": jetstream.ErrConsumerNotFound,
		"bad request":        jetstream.ErrBadRequest,
		"wrapped deletion":   fmt.Errorf("pull request failed: %w", jetstream.ErrConsumerDeleted),
	}
	for name, err := range terminal {
		require.True(t, terminalSubscriptionError(err),
			"%s must close the channel — no amount of asking again brings the consumer back", name)
	}

	transient := map[string]error{
		"missed heartbeat":     jetstream.ErrNoHeartbeat,
		"leadership change":    jetstream.ErrConsumerLeadershipChanged,
		"context deadline":     context.DeadlineExceeded,
		"unclassified":         errors.New("some transport hiccup"),
		"wrapped no-heartbeat": fmt.Errorf("iterator: %w", jetstream.ErrNoHeartbeat),
	}
	for name, err := range transient {
		require.False(t, terminalSubscriptionError(err),
			"%s must re-open against the same durable — treating it as death retires a subscription that "+
				"had nothing wrong with it, and the caller stays degraded until a restart", name)
	}
}

// TestSubscriptionIsGone_AsksTheServerNotTheErrorText pins the decision the
// error classifier cannot make on its own, and this test exists because the
// integration test below only catches it SOMETIMES.
//
// A consumer deleted under a live iterator frequently surfaces as
// jetstream.ErrNoHeartbeat rather than anything naming a consumer: the server
// stops answering, and the client's heartbeat monitor is what notices. Which
// error arrives is a race between the heartbeat timer and an in-flight pull
// request — so a classifier reading only the error text reopens forever
// against a consumer that no longer exists, and only under some timings.
// Asking the server whether the consumer exists is not subject to that race.
//
// Both directions are asserted from the identical error, because the whole
// point is that the error carries no information here.
func TestSubscriptionIsGone_AsksTheServerNotTheErrorText(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := Connect(ctx, ConnectOpts{URL: s.ClientURL()})
	require.NoError(t, err)
	defer conn.Close()

	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "gone-kv"})
	require.NoError(t, err)

	cons, err := conn.JetStream().CreateOrUpdateConsumer(ctx, "KV_gone-kv", jetstream.ConsumerConfig{
		Durable:       "gone-durable",
		FilterSubject: "$KV.gone-kv.vtx.meta.>",
	})
	require.NoError(t, err)

	require.False(t, conn.subscriptionIsGone(ctx, cons, jetstream.ErrNoHeartbeat),
		"a stall on a consumer that still exists must re-open — retiring it here loses a subscription that "+
			"had nothing wrong with it")

	require.NoError(t, conn.DeleteDurable(ctx, "gone-kv", "gone-durable"))

	require.True(t, conn.subscriptionIsGone(ctx, cons, jetstream.ErrNoHeartbeat),
		"the SAME error over a consumer that no longer exists must be terminal — the error text cannot tell "+
			"these apart, so the server has to be asked")
}

// TestSubscribeKVChanges_ClosesWhenTheConsumerIsDeleted is the other half of
// the reopen loop, and the half a reopen loop is most likely to break: having
// taught the subscription to survive a failure, it must still recognise the
// one it cannot survive. A durable deleted out from under the iterator is
// gone — retrying only produces the same error forever, silently, while the
// caller waits on a channel that will never close and never deliver.
//
// internal/refractor/lens is the caller this matters to: channel-close is what
// latches its taxonomy resolver permanently unarmed, which is the fail-closed
// posture for a source that has stopped reading the taxonomy. A reopen loop
// that swallowed this would leave that source armed forever over a snapshot
// nothing is maintaining.
func TestSubscribeKVChanges_ClosesWhenTheConsumerIsDeleted(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := Connect(ctx, ConnectOpts{URL: s.ClientURL()})
	require.NoError(t, err)
	defer conn.Close()

	kv, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "deleted-kv"})
	require.NoError(t, err)

	const durable = "deleted-durable"
	events, err := conn.SubscribeKVChanges(ctx, "deleted-kv", []string{"vtx.meta."}, durable,
		SubscribeKVOptions{IncludeHistory: true, MaxPrefetch: 1})
	require.NoError(t, err)

	_, err = kv.Put(ctx, "vtx.meta.AvakeBefareDeleteAA", []byte(`{"class":"meta.lens"}`))
	require.NoError(t, err)
	select {
	case evt, ok := <-events:
		require.True(t, ok, "precondition: the subscription is delivering")
		require.Equal(t, "vtx.meta.AvakeBefareDeleteAA", evt.Key)
	case <-ctx.Done():
		t.Fatal("no event before the deletion")
	}

	require.NoError(t, conn.DeleteDurable(ctx, "deleted-kv", durable))

	for {
		select {
		case _, ok := <-events:
			if !ok {
				return // closed, as it must be
			}
		case <-ctx.Done():
			t.Fatal("the channel never closed after its consumer was deleted — the caller waits forever on a " +
				"subscription that cannot be repaired, with no signal that it is over")
		}
	}
}

// stallingConsumer is a jetstream.Consumer whose messages iterator fails with
// a transient error a fixed number of times and then blocks. It exists because
// a real transient stall cannot be produced inside a test's patience: nats.go
// absorbs connection outages internally (measured on this fixture — 2s, 5s and
// 10s outages each produced zero iterator errors), and the heartbeat path that
// does surface one needs ~30s of silence at the default interval. Deleting the
// consumer to force an error takes the TERMINAL branch instead, by
// construction.
//
// So the stall is injected rather than provoked. What is under test is the
// reopen DECISION and its announcement — everything downstream of an iterator
// error — which is exactly the part a stub can drive honestly.
type stallingConsumer struct {
	jetstream.Consumer // never called: only the two methods below are used

	mu       sync.Mutex
	opens    int
	failures int
	blocked  chan struct{}
}

func (c *stallingConsumer) Messages(_ ...jetstream.PullMessagesOpt) (jetstream.MessagesContext, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opens++
	return &stallingMessages{fail: c.opens <= c.failures, blocked: c.blocked}, nil
}

// Info answers "the consumer still exists", which is what makes
// subscriptionIsGone classify the injected error as a stall rather than death.
func (c *stallingConsumer) Info(_ context.Context) (*jetstream.ConsumerInfo, error) {
	return &jetstream.ConsumerInfo{Name: "stalling"}, nil
}

func (c *stallingConsumer) openCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.opens
}

type stallingMessages struct {
	fail    bool
	blocked chan struct{}
	stopped chan struct{}
	once    sync.Once
}

func (m *stallingMessages) Next(_ ...jetstream.NextOpt) (jetstream.Msg, error) {
	if m.fail {
		return nil, jetstream.ErrNoHeartbeat
	}
	m.once.Do(func() {
		m.stopped = make(chan struct{})
		close(m.blocked)
	})
	<-m.stopped
	return nil, jetstream.ErrMsgIteratorClosed
}

func (m *stallingMessages) Stop() {
	if m.stopped == nil {
		return
	}
	select {
	case <-m.stopped:
	default:
		close(m.stopped)
	}
}

func (m *stallingMessages) Drain() { m.Stop() }

// TestRunKVSubscription_ReopenIsAnnouncedToTheCaller pins the hook a freshness
// claim is withdrawn on. A stall costs no MESSAGES — the durable resumes from
// its ack floor and everything undelivered comes back — which is exactly what
// makes it dangerous to a caller deriving CURRENCY: the channel stays open,
// the connection never dropped, and without this announcement nothing anywhere
// records that the caller was blind for a while.
//
// internal/refractor/lens is that caller. Its taxonomy resolver answers
// StatusArmed, which licenses a `*` lens to publish a narrowed client gate
// that acks-and-drops; straight through an unannounced stall, that gate is
// being applied against a taxonomy nobody was reading.
//
// The stall is injected (see stallingConsumer) because it cannot be provoked
// in test time: this is a unit-level proof of the decision and the hook, not
// an end-to-end reproduction of a heartbeat gap.
func TestRunKVSubscription_ReopenIsAnnouncedToTheCaller(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Connect(ctx, ConnectOpts{URL: s.ClientURL()})
	require.NoError(t, err)
	defer conn.Close()

	cons := &stallingConsumer{failures: 2, blocked: make(chan struct{})}
	reopens := make(chan struct{}, 8)
	out := make(chan KVEvent)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn.runKVSubscription(ctx, cons, "stall-durable", "stall-kv", "$KV.stall-kv.",
			1, func() { reopens <- struct{}{} }, out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	for i := 1; i <= 2; i++ {
		select {
		case <-reopens:
		case <-ctx.Done():
			t.Fatalf("stall %d was never announced — a caller deriving freshness from this stream has no way to know it went blind", i)
		}
	}

	select {
	case <-cons.blocked: // the third open succeeded and is waiting for messages
	case <-ctx.Done():
		t.Fatal("the subscription never re-opened successfully")
	}
	require.Equal(t, 3, cons.openCount(), "two announced stalls, then a healthy iterator")

	select {
	case <-closed:
		t.Fatal("a transient stall must not close the channel — that signal means the feed is over")
	default:
	}

	cancel()
	select {
	case <-closed:
	case <-time.After(10 * time.Second):
		t.Fatal("the subscription did not stop on ctx cancellation")
	}
}

// TestRunKVSubscription_NilOnReopenChangesNothing pins the default. Every
// other SubscribeKVChanges caller in the tree leaves this unset, and unset has
// to be exactly the behaviour they already had: a stall that re-opens
// silently, no callback, no channel close.
func TestRunKVSubscription_NilOnReopenChangesNothing(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := Connect(ctx, ConnectOpts{URL: s.ClientURL()})
	require.NoError(t, err)
	defer conn.Close()

	cons := &stallingConsumer{failures: 1, blocked: make(chan struct{})}
	out := make(chan KVEvent)
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn.runKVSubscription(ctx, cons, "stall-durable", "stall-kv", "$KV.stall-kv.",
			1, nil, out, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	select {
	case <-cons.blocked:
	case <-ctx.Done():
		t.Fatal("the subscription never re-opened with a nil OnReopen")
	}
	select {
	case <-closed:
		t.Fatal("a nil hook must not change the reopen behaviour")
	default:
	}
	cancel()
	<-closed
}

// TestSubscribeKVChanges_SurvivesAConnectionOutage asserts the property the
// reopen loop exists to protect, from the caller's side: a connection that
// drops and comes back must leave the subscription usable, not closed. The
// durable's position is the ack floor, not the iterator's, so a repaired
// connection has everything it needs to carry on — and a caller watching for
// channel-close (which it is told to treat as "this feed is over") must not be
// handed that signal by a blip.
func TestSubscribeKVChanges_SurvivesAConnectionOutage(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	proxy := newSeveringProxy(t, s.Addr().String())
	conn, err := Connect(ctx, ConnectOpts{
		URL:           proxy.url,
		MaxReconnects: -1,
		ReconnectWait: 25 * time.Millisecond,
	})
	require.NoError(t, err)
	defer conn.Close()

	kv, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "outage-kv"})
	require.NoError(t, err)

	events, err := conn.SubscribeKVChanges(ctx, "outage-kv", []string{"vtx.meta."}, "outage-durable",
		SubscribeKVOptions{IncludeHistory: true, MaxPrefetch: 1, AckWait: 30 * time.Second})
	require.NoError(t, err)

	_, err = kv.Put(ctx, "vtx.meta.BefarePutageAAAAAAAA", []byte(`{"class":"meta.lens"}`))
	require.NoError(t, err)
	select {
	case evt, ok := <-events:
		require.True(t, ok, "precondition: the subscription delivers before the outage")
		require.Equal(t, "vtx.meta.BefarePutageAAAAAAAA", evt.Key)
	case <-ctx.Done():
		t.Fatal("no event before the outage")
	}

	proxy.sever()
	require.Eventually(t, func() bool { return !conn.Connected() }, 20*time.Second, 10*time.Millisecond)
	proxy.restore()
	require.Eventually(t, conn.Connected, 20*time.Second, 10*time.Millisecond)

	require.Eventually(t, func() bool {
		_, putErr := kv.Put(ctx, "vtx.meta.AfterPutageAAAAAAAAA", []byte(`{"class":"meta.lens"}`))
		return putErr == nil
	}, 20*time.Second, 20*time.Millisecond)

	select {
	case evt, ok := <-events:
		require.True(t, ok, "the channel must NOT have closed — an outage is not the end of a durable subscription")
		require.Equal(t, "vtx.meta.AfterPutageAAAAAAAAA", evt.Key)
	case <-ctx.Done():
		t.Fatal("the subscription stopped delivering after the connection came back")
	}
}
