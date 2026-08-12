package sync

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/edge/transport"
	"github.com/operatinggraph/lattice/internal/refractor/control/controlwire"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// gatedTransport reproduces the browser shell's shape: BOTH the readiness step
// and the feed-opening call park on the same leadership signal, because the
// shell awaits leadership in `awaitLeadership` and again in `startConsumer`.
// That is what makes the ordering observable — a Manager that resolves its
// position before awaiting readiness still ends up waiting, just with a stale
// answer already in hand.
type gatedTransport struct {
	*fakeControlTransport
	ready chan struct{}
	// firstEntry records which of the two the Manager reached first.
	firstEntry chan string
	cfg        transport.ConsumerConfig
	started    chan struct{}
	err        error
}

func newGatedTransport(reply func(op string, body controlwire.ControlRequest) (controlwire.ControlResponse, error)) *gatedTransport {
	return &gatedTransport{
		fakeControlTransport: &fakeControlTransport{reply: reply},
		ready:                make(chan struct{}),
		firstEntry:           make(chan string, 1),
		started:              make(chan struct{}),
	}
}

func (g *gatedTransport) enter(who string) {
	select {
	case g.firstEntry <- who:
	default:
	}
}

func (g *gatedTransport) AwaitAttachReady(ctx context.Context) error {
	g.enter("readiness")
	if g.err != nil {
		return g.err
	}
	select {
	case <-g.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *gatedTransport) RunDurableConsumer(ctx context.Context, cfg transport.ConsumerConfig, _ transport.Handler) error {
	g.cfg = cfg
	g.enter("consumer")
	close(g.started)
	select {
	case <-g.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// TestManager_Run_ResolvesThePositionOnlyOnceReadyToAttach pins the ordering
// the browser host depends on: nothing about the position — the stored cursor,
// its retention-gap check, the delivery floor — is read before the transport
// says this host may attach.
//
// The world moves while the gate is shut, exactly as it does for a follower tab
// parked on the Web Lock behind a leader that may live for days. Only a Manager
// that resolves AFTER the wait names the position that is current then; one that
// resolved at boot names the stale one, and a stale StartSeq that has fallen
// below the stream's retained beginning is clamped UP — a silent skip, with the
// gap check that would have caught it already spent.
func TestManager_Run_ResolvesThePositionOnlyOnceReadyToAttach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tr := newGatedTransport(happyControlReply(false))
	st := openTestStore(t)
	require.NoError(t, st.SetCursor(7))
	mgr, err := New(tr, st, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- mgr.Run(ctx) }()

	var first string
	select {
	case first = <-tr.firstEntry:
	case <-ctx.Done():
		t.Fatal("Run reached neither the readiness gate nor the consumer")
	}
	require.Equal(t, "readiness", first,
		"readiness must be awaited before any position work — reaching the consumer first means the cursor, the gap check and the floor were all resolved at boot")

	// The shared store moves under the waiting host: the leader tab is
	// consuming on the same durable and writing the same per-identity cursor.
	require.NoError(t, st.SetCursor(41))
	close(tr.ready)

	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-ctx.Done():
		t.Fatal("Run never returned after the gate opened")
	}

	// The exact sequence, not just its contents: everything the control plane
	// is asked happens after the wait, the gap check runs exactly once, and
	// the warm path's Interest Set refresh follows it — still before the
	// attach, which is the ordering that refresh exists for.
	require.Equal(t, []string{"syncgap", "register"}, tr.requests,
		"after the wait: one gap check, then the warm resume's registration refresh, and nothing else")
	require.Equal(t, uint64(42), tr.cfg.StartSeq,
		"the position must come from the cursor as it stands at attach time, not at boot")
	require.Equal(t, uint64(41), floorPersisted(&mgr.floor),
		"the delivery floor must be seeded from the same post-wait cursor, or release can write a cursor BELOW what the leader already stored")
}

// A readiness gate that fails is a Run that never attaches: the host is not
// entitled to the durable, so resolving a position and opening a feed anyway is
// the split-consumer hazard the gate exists for.
func TestManager_Run_ReadinessFailureAttachesNothing(t *testing.T) {
	tr := newGatedTransport(happyControlReply(false))
	tr.err = errors.New("leader election failed")
	close(tr.ready)
	st := openTestStore(t)
	require.NoError(t, st.SetCursor(7))
	mgr, err := New(tr, st, Config{IdentityID: "identityA", DeviceID: "deviceX", Logger: testutil.TestLogger()})
	require.NoError(t, err)

	runErr := mgr.Run(context.Background())
	require.Error(t, runErr)
	require.Contains(t, runErr.Error(), "await attach readiness")
	require.Empty(t, tr.requests, "a host that may not attach must not ask the control plane anything")
	select {
	case <-tr.started:
		t.Fatal("the durable consumer must not start behind a failed readiness gate")
	default:
	}
}

// A transport that implements no readiness step is ready now — the trusted Go
// hosts' NATS transport, which is entitled to attach the moment it dials.
func TestManager_Run_UngatedTransportAttachesImmediately(t *testing.T) {
	var events []string
	tr := &establishedTransport{
		fakeControlTransport: &fakeControlTransport{reply: happyControlReply(false)},
		events:               &events,
	}
	mgr := newEstablishedManager(t, tr, 7, true, &events)

	require.NoError(t, mgr.Run(context.Background()))
	require.Equal(t, []string{"established", "consumer"}, events)
}

// floorPersisted reads the floor's seeded cursor under its own lock.
func floorPersisted(f *deliveryFloor) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.persisted
}
