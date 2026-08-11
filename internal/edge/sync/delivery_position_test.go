package sync

import (
	"context"
	"encoding/json"
	"fmt"
	stdsync "sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	edgestore "github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/edge/transport"
	"github.com/operatinggraph/lattice/internal/edge/transport/natstransport"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// The acceptance bar for the delivery-position initiative
// (edge-cold-signin-delivery-position-design.md §6, Fire 2), in the units the
// defect was measured in: a `W`-key world behind `L` personal lenses, sitting
// behind a ledger of stale frames the node has no use for.
const (
	// acceptanceWorldKeys is `W` — the authorized world whose cold sign-in was
	// measured at 2,049 delivered frames (§1.1).
	acceptanceWorldKeys = 14
	// acceptanceLenses is `L` — edge-manifest's personal-lens count, which
	// sets how many keyset and hydrationComplete frames one burst carries.
	acceptanceLenses = 15
	// acceptanceStaleFrames is the retained ledger deposited on the actor's
	// subject before the node ever attaches. Every one of them describes a
	// world the hydration burst supersedes.
	acceptanceStaleFrames = 200
	// acceptanceFrameBar is `W + 2L + 5`: one burst plus headroom. A cold
	// sign-in must land under it.
	acceptanceFrameBar = acceptanceWorldKeys + 2*acceptanceLenses + 5
	// acceptanceBurstFrames is what one hydration burst actually publishes:
	// a row per world key, a keyset frame per lens, and a terminal marker per
	// lens.
	acceptanceBurstFrames = acceptanceWorldKeys + 2*acceptanceLenses
	// hydrateRevision is the projection revision the fake hydrator reports and
	// stamps on its terminal markers, so the boot gate's armed target is met.
	hydrateRevision = 1000
)

// countingTransport is the live NATS transport with two facts recorded on the
// way past: how many frames the node was actually delivered, and what start
// position it asked its feed for. Those are the two quantities the delivery-
// position invariant is stated in, and neither is observable from the store.
type countingTransport struct {
	*natstransport.Conn

	mu      stdsync.Mutex
	frames  int
	lastCfg transport.ConsumerConfig
}

func (c *countingTransport) RunDurableConsumer(ctx context.Context, cfg transport.ConsumerConfig, h transport.Handler) error {
	c.mu.Lock()
	c.lastCfg = cfg
	c.mu.Unlock()
	return c.Conn.RunDurableConsumer(ctx, cfg, func(ctx context.Context, d transport.Delta) transport.Decision {
		c.mu.Lock()
		c.frames++
		c.mu.Unlock()
		return h(ctx, d)
	})
}

func (c *countingTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.frames
}

// resetCount clears both recorded facts. Clearing lastCfg matters as much as
// the counter: a later read must observe the NEXT attach's start position, not
// the previous one's, or an assertion about repositioning could pass by
// coincidence.
func (c *countingTransport) resetCount() {
	c.mu.Lock()
	c.frames = 0
	c.lastCfg = transport.ConsumerConfig{}
	c.mu.Unlock()
}

func (c *countingTransport) startSeq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCfg.StartSeq
}

// newVertexKeys mints n distinct Contract #1 vertex keys.
func newVertexKeys(t *testing.T, typeName string, n int) []string {
	t.Helper()
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, err := substrate.NewNanoID()
		require.NoError(t, err)
		keys = append(keys, substrate.VertexKey(typeName, id))
	}
	return keys
}

// publishEnvelope writes one delta envelope to an actor's subject. It reports
// failures with Errorf rather than a require, because the hydration burst is
// published from the control service's goroutine, where FailNow is illegal.
func publishEnvelope(t *testing.T, ctx context.Context, conn *substrate.Conn, identityID string, env deltaEnvelope) {
	body, err := json.Marshal(env)
	if err != nil {
		t.Errorf("marshal delta envelope: %v", err)
		return
	}
	if err := conn.Publish(ctx, subjects.PersonalSync(defaultSubjectPrefix, identityID), body, nil); err != nil {
		t.Errorf("publish delta envelope: %v", err)
	}
}

// deliveryPositionScenario is one actor whose subject already holds a stale
// ledger, plus a control plane whose hydrator publishes the current world.
type deliveryPositionScenario struct {
	conn      *substrate.Conn
	tr        *countingTransport
	st        edgestore.Store
	identity  string
	device    string
	durable   string
	worldKeys []string
	staleKeys []string
}

// newDeliveryPositionScenario builds the shared setup both vectors run on. Only
// wireStartSeq differs between them: with the seam, personal.hydrate names the
// position its burst was taken from; without it, the response carries zero and
// the node has no position to assert — which is the pre-position behaviour, and
// the reachability proof the "few frames" assertion needs.
func newDeliveryPositionScenario(t *testing.T, ctx context.Context, wireStartSeq bool) *deliveryPositionScenario {
	t.Helper()
	conn := newSyncTestConn(t, ctx)
	interestKV := openInterestKV(t, ctx, conn)

	s := &deliveryPositionScenario{
		conn:      conn,
		st:        openTestStore(t),
		identity:  "identityA",
		device:    "deviceX",
		worldKeys: newVertexKeys(t, "lease", acceptanceWorldKeys),
		staleKeys: newVertexKeys(t, "lease", acceptanceStaleFrames),
	}
	s.durable = DurableName(s.identity, s.device)
	s.tr = &countingTransport{Conn: natstransport.New(conn)}

	// The ledger: every stale frame names a key the current world does not
	// contain, so its arrival is observable in the mirror. Unattributed, so
	// the hydrate's dead-lens prune cannot be what removes them.
	for i, key := range s.staleKeys {
		publishEnvelope(t, ctx, conn, s.identity, deltaEnvelope{
			Op: "upsert", Key: key, Revision: uint64(i + 1), ProjectionSeq: uint64(i + 1),
			Data: json.RawMessage(`{"stale":true}`),
		})
	}

	h := &fakeHydrator{conn: conn, revision: hydrateRevision}
	h.publish = func(ctx context.Context, conn *substrate.Conn, identityID string) {
		for i, key := range s.worldKeys {
			publishEnvelope(t, ctx, conn, identityID, deltaEnvelope{
				Op: "upsert", Key: key, Lens: "rule-1", Revision: uint64(i + 1), ProjectionSeq: uint64(i + 1),
				Data: json.RawMessage(`{"hydrated":true}`),
			})
		}
		// One authoritative keyset frame per lens. The hydrating lens's frame
		// carries the whole world; the rest are the other lenses reporting an
		// empty frame for this actor.
		publishEnvelope(t, ctx, conn, identityID, deltaEnvelope{
			Op: "keyset", Lens: "rule-1", Revision: hydrateRevision, Keys: s.worldKeys,
		})
		for i := 1; i < acceptanceLenses; i++ {
			publishEnvelope(t, ctx, conn, identityID, deltaEnvelope{
				Op: "keyset", Lens: fmt.Sprintf("lens-%d", i), Revision: hydrateRevision,
			})
		}
		// A terminal marker per lens — the shape personal.hydrate's fan-out
		// actually produces, one pipeline.Hydrate at a time.
		for i := 0; i < acceptanceLenses; i++ {
			publishEnvelope(t, ctx, conn, identityID, deltaEnvelope{
				Op: "hydrationComplete", Revision: hydrateRevision,
			})
		}
	}
	if wireStartSeq {
		startControlService(t, ctx, conn, h, interestKV)
	} else {
		startControlServiceWithoutStartSeq(t, ctx, conn, h, interestKV)
	}
	return s
}

// runManager starts a Manager over the scenario's store and counting transport
// and returns a stop func that cancels it and waits for a clean return.
func (s *deliveryPositionScenario) runManager(t *testing.T, ctx context.Context) func() {
	t.Helper()
	mgr, err := New(s.tr, s.st, Config{
		IdentityID: s.identity, DeviceID: s.device, Logger: testutil.TestLogger(),
	})
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- mgr.Run(runCtx) }()
	return func() {
		cancel()
		select {
		case runErr := <-done:
			require.NoError(t, runErr)
		case <-time.After(10 * time.Second):
			t.Fatal("Manager.Run did not return after context cancellation")
		}
	}
}

// awaitWorld blocks until every hydrated world key is present in the mirror.
func (s *deliveryPositionScenario) awaitWorld(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, key := range s.worldKeys {
			entry, ok, err := s.st.Get(key)
			if err != nil || !ok || entry.Deleted {
				return false
			}
		}
		return true
	}, 30*time.Second, 50*time.Millisecond, "the hydrated world must reach the mirror")
}

// awaitSettled blocks until the node's consumer has nothing left pending and
// nothing delivered-but-unacked, so a frame count read after it is final
// rather than a snapshot taken mid-burst.
func (s *deliveryPositionScenario) awaitSettled(t *testing.T, ctx context.Context) {
	t.Helper()
	require.Eventually(t, func() bool {
		caughtUp, err := s.conn.ConsumerCaughtUp(ctx, DefaultStream, s.durable)
		return err == nil && caughtUp
	}, 30*time.Second, 50*time.Millisecond, "the node's consumer must drain what it was given")
}

// staleKeysPresent counts how many of the superseded ledger's keys reached the
// mirror.
func (s *deliveryPositionScenario) staleKeysPresent(t *testing.T) int {
	t.Helper()
	present := 0
	for _, key := range s.staleKeys {
		_, ok, err := s.st.Get(key)
		require.NoError(t, err)
		if ok {
			present++
		}
	}
	return present
}

// TestManager_ColdStart_DeliversTheWorldNotTheLedger is the acceptance bar for
// the whole initiative (edge-cold-signin-delivery-position-design.md §6, Fire
// 2): a hydrate repositions the feed, so nothing published before the
// hydration snapshot is ever delivered.
//
// Both vectors run on ONE setup — the same 200-frame ledger, the same 14-key
// world, the same 15 lenses — differing only in whether the control plane can
// name the position its burst was taken from. That pairing is the point. "The
// node received under 49 frames" pins nothing on its own; it is worth something
// only because the identical harness, with no position to assert, delivers all
// 200 stale frames and lands every one of them in the mirror.
func TestManager_ColdStart_DeliversTheWorldNotTheLedger(t *testing.T) {
	t.Run("with no position to assert the whole ledger arrives", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s := newDeliveryPositionScenario(t, ctx, false)

		stop := s.runManager(t, ctx)
		defer stop()

		s.awaitWorld(t)
		s.awaitSettled(t, ctx)

		assert.Zero(t, s.tr.startSeq(), "a control plane that names no position must leave StartSeq unset")
		assert.GreaterOrEqual(t, s.tr.count(), acceptanceStaleFrames+acceptanceBurstFrames,
			"a node with no start position must be handed the subject's whole retained history")
		assert.Equal(t, acceptanceStaleFrames, s.staleKeysPresent(t),
			"every stale frame's key must land — this is the amplification the fix removes")
		t.Logf("no start position: %d frames delivered, %d stale keys in the mirror",
			s.tr.count(), s.staleKeysPresent(t))
	})

	t.Run("positioned at the hydration point the ledger is never read", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		s := newDeliveryPositionScenario(t, ctx, true)

		stop := s.runManager(t, ctx)
		defer stop()

		s.awaitWorld(t)
		s.awaitSettled(t, ctx)

		frames := s.tr.count()
		t.Logf("positioned at %d: %d frames delivered (bar is %d), %d stale keys in the mirror",
			s.tr.startSeq(), frames, acceptanceFrameBar, s.staleKeysPresent(t))

		assert.Greater(t, s.tr.startSeq(), uint64(acceptanceStaleFrames),
			"the start position must sit past the whole stale ledger")
		assert.Less(t, frames, acceptanceFrameBar,
			"a cold sign-in must receive one burst, not the ledger behind it")
		assert.Zero(t, s.staleKeysPresent(t),
			"the mirror must hold exactly the hydrated world — no key from a superseded frame")

		for _, key := range s.worldKeys {
			entry, ok, err := s.st.Get(key)
			require.NoError(t, err)
			require.True(t, ok)
			require.False(t, entry.Deleted)
			require.JSONEq(t, `{"hydrated":true}`, string(entry.Data))
		}
	})
}

// TestManager_WarmRestart_ResumesAtCursorPlusOne covers the other half of the
// resume table: once the cursor is the single resume authority, a restart must
// ask for exactly the position after the last message the node finished with —
// and "finished with" has to include a Term'd poison frame, or a permanently
// undeliverable message would be redelivered on every boot forever.
func TestManager_WarmRestart_ResumesAtCursorPlusOne(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := newDeliveryPositionScenario(t, ctx, true)

	stop := s.runManager(t, ctx)
	s.awaitWorld(t)

	// A frame that will never parse, published behind the burst the node just
	// applied. handle() Terms it — and must still record it as a position the
	// node has passed.
	require.NoError(t, s.conn.Publish(ctx, subjects.PersonalSync(defaultSubjectPrefix, s.identity), []byte("not json"), nil))
	poisonSeq := s.streamLastSeq(t, ctx)

	require.Eventually(t, func() bool {
		cursor, ok, err := s.st.Cursor()
		return err == nil && ok && cursor >= poisonSeq
	}, 30*time.Second, 50*time.Millisecond,
		"a Term'd frame is a position the node has passed — the cursor must advance past it")
	stop()

	cursor, ok, err := s.st.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, poisonSeq, cursor, "the poison frame must be the last position the node recorded")

	s.tr.resetCount()
	stopAgain := s.runManager(t, ctx)
	defer stopAgain()

	require.Eventually(t, func() bool {
		return s.tr.startSeq() == cursor+1
	}, 30*time.Second, 50*time.Millisecond, "a warm restart must resume at cursor+1")

	// Nothing has been published since the poison frame, so a correctly
	// positioned consumer has nothing to deliver. Wait for it to say so rather
	// than for a clock.
	s.awaitSettled(t, ctx)
	assert.Zero(t, s.tr.count(), "a Term'd poison frame must not be redelivered after a restart")
}

// TestManager_ColdStart_ReplacesPreexistingDeliverAllDurable is the upgrade
// case. Every durable in flight today was created with DeliverAllPolicy, and a
// server refuses to move an existing consumer's delivery position — so a node
// that names one must recreate the durable rather than fail against the old
// one.
func TestManager_ColdStart_ReplacesPreexistingDeliverAllDurable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := newDeliveryPositionScenario(t, ctx, true)

	_, err := s.conn.JetStream().CreateOrUpdateConsumer(ctx, DefaultStream, jetstream.ConsumerConfig{
		Durable:       s.durable,
		FilterSubject: subjects.PersonalSync(defaultSubjectPrefix, s.identity),
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err, "the pre-existing durable is the starting state, not part of the assertion")

	stop := s.runManager(t, ctx)
	defer stop()

	s.awaitWorld(t)
	s.awaitSettled(t, ctx)

	cons, err := s.conn.JetStream().Consumer(ctx, DefaultStream, s.durable)
	require.NoError(t, err)
	info, err := cons.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, jetstream.DeliverByStartSequencePolicy, info.Config.DeliverPolicy,
		"the DeliverAll durable must be replaced, not attached to")
	assert.Equal(t, s.tr.startSeq(), info.Config.OptStartSeq)
	assert.Less(t, s.tr.count(), acceptanceFrameBar,
		"the replacement durable must start at the hydration point, not the ledger's beginning")
	assert.Zero(t, s.staleKeysPresent(t))
}

// streamLastSeq reads the SYNC stream's current last sequence — the sequence of
// the message published most recently.
func (s *deliveryPositionScenario) streamLastSeq(t *testing.T, ctx context.Context) uint64 {
	t.Helper()
	st, err := s.conn.JetStream().Stream(ctx, DefaultStream)
	require.NoError(t, err)
	return st.CachedInfo().State.LastSeq
}

// TestManager_Restart_RedeliversAFrameTheCursorNeverPassed is the end-to-end
// consequence of the contiguous floor, over a real consumer and a real
// restart. One live frame cannot apply; the frame behind it, already in the
// client's prefetch buffer, applies fine. The cursor must stay below the hole
// — and because the next attach deletes the durable, the cursor is the ONLY
// thing that can bring the held frame back.
func TestManager_Restart_RedeliversAFrameTheCursorNeverPassed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := newDeliveryPositionScenario(t, ctx, true)

	heldKeys := newVertexKeys(t, "lease", 2)
	heldKey, followerKey := heldKeys[0], heldKeys[1]
	gated := newGatedStore(s.st, heldKey)
	s.st = gated

	stop := s.runManager(t, ctx)
	s.awaitWorld(t)
	s.awaitSettled(t, ctx)

	burstCursor, ok, err := s.st.Cursor()
	require.NoError(t, err)
	require.True(t, ok)

	// Two live frames behind the burst. The first cannot apply while the gate
	// is closed and is Nak'd for redelivery; the second applies normally.
	publishEnvelope(t, ctx, s.conn, s.identity, deltaEnvelope{
		Op: "upsert", Key: heldKey, Lens: "rule-1", Revision: hydrateRevision + 1,
		Data: json.RawMessage(`{"held":true}`),
	})
	heldSeq := s.streamLastSeq(t, ctx)
	require.Equal(t, burstCursor+1, heldSeq, "the held frame must be the next sequence after the burst")
	publishEnvelope(t, ctx, s.conn, s.identity, deltaEnvelope{
		Op: "upsert", Key: followerKey, Lens: "rule-1", Revision: hydrateRevision + 2,
		Data: json.RawMessage(`{"follower":true}`),
	})

	require.Eventually(t, func() bool {
		_, ok, err := s.st.Get(followerKey)
		return err == nil && ok
	}, 30*time.Second, 50*time.Millisecond, "the frame behind the hole must apply while the hole is open")

	cursor, ok, err := s.st.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, heldSeq-1, cursor,
		"the cursor must stop at the contiguous floor — a higher frame applying must not carry it past the outstanding one")

	stop()

	_, ok, err = s.st.Get(heldKey)
	require.NoError(t, err)
	require.False(t, ok, "the held frame must not have applied while the gate was closed")

	// Restart with the hole resolvable. The durable is deleted and recreated
	// from the cursor, so only a cursor that stayed below the hole can bring
	// the held frame back.
	gated.open()
	s.tr.resetCount()
	stopAgain := s.runManager(t, ctx)
	defer stopAgain()

	require.Eventually(t, func() bool {
		entry, ok, err := s.st.Get(heldKey)
		return err == nil && ok && !entry.Deleted
	}, 30*time.Second, 50*time.Millisecond,
		"a restart must redeliver the frame the cursor never passed")
	assert.Equal(t, heldSeq, s.tr.startSeq(), "the restart must resume exactly at the held sequence")
}

// TestNatsTransport_UnpositionedAttachAfterAPositionedDurable pins the other
// direction of the server's immutable-position rule. A durable created at a
// named position cannot be re-created unpositioned either, so an attach that
// resolves to zero — an older control plane, a failed stream read, a cursor of
// zero — must still succeed. It does because the transport deletes the durable
// on EVERY attach, not only when it names a position; a conditional delete
// would leave the node unable to attach at all.
func TestNatsTransport_UnpositionedAttachAfterAPositionedDurable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn := newSyncTestConn(t, ctx)

	identity := "identityB"
	cfg := transport.ConsumerConfig{
		Stream:        DefaultStream,
		Durable:       DurableName(identity, "deviceX"),
		FilterSubject: subjects.PersonalSync(defaultSubjectPrefix, identity),
		Logger:        testutil.TestLogger(),
	}
	keys := newVertexKeys(t, "lease", 5)
	for i, key := range keys {
		publishEnvelope(t, ctx, conn, identity, deltaEnvelope{
			Op: "upsert", Key: key, Revision: uint64(i + 1), Data: json.RawMessage(`{"v":1}`),
		})
	}
	lastSeq := func() uint64 {
		st, err := conn.JetStream().Stream(ctx, DefaultStream)
		require.NoError(t, err)
		return st.CachedInfo().State.LastSeq
	}()
	firstSeq := lastSeq - uint64(len(keys)) + 1

	tr := natstransport.New(conn)

	positioned := cfg
	positioned.StartSeq = lastSeq
	got := attachAndCollect(t, ctx, tr, positioned, 1)
	require.Equal(t, []uint64{lastSeq}, got, "the positioned attach must start where it was told")

	unpositioned := cfg
	got = attachAndCollect(t, ctx, tr, unpositioned, len(keys))
	require.Equal(t, firstSeq, got[0],
		"an attach asserting no position must succeed against an already-positioned durable, and take the whole retained subject")
	require.Len(t, got, len(keys))
}

// attachAndCollect runs one durable attach until it has delivered want deltas,
// then cancels it and requires a clean return — so a create the server rejected
// surfaces as a test failure rather than as silence.
func attachAndCollect(t *testing.T, ctx context.Context, tr transport.DeltaSource, cfg transport.ConsumerConfig, want int) []uint64 {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	seen := make(chan uint64, 64)
	done := make(chan error, 1)
	go func() {
		done <- tr.RunDurableConsumer(runCtx, cfg, func(_ context.Context, d transport.Delta) transport.Decision {
			seen <- d.Sequence
			return transport.Ack
		})
	}()

	var got []uint64
	deadline := time.After(30 * time.Second)
	for len(got) < want {
		select {
		case seq := <-seen:
			got = append(got, seq)
		case runErr := <-done:
			require.NoError(t, runErr, "the attach must not fail")
			t.Fatalf("attach returned after %d/%d deltas", len(got), want)
		case <-deadline:
			t.Fatalf("attach timed out after %d/%d deltas: %v", len(got), want, got)
		}
	}
	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(10 * time.Second):
		t.Fatal("attach did not return after context cancellation")
	}
	return got
}
