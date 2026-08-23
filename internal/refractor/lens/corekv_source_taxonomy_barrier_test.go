package lens

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// These tests exercise the replay/reconnect barrier behind the taxonomy
// resolver's `armed` flag (dynamic-type-taxonomy-design.md §4.2, §14 Fire C
// item 5) against a REAL JetStream durable, because the property under test
// is entirely about that durable's delivery state: whether the snapshots the
// source has emitted so far describe the whole taxonomy or a prefix of it.
// Feeding handle() directly (as corekv_source_taxonomy_test.go does for the
// accumulation logic) cannot express the question at all — there is no replay
// to be partway through.
//
// The wiring under test is the production one: source → taxonomy callback →
// InstallSnapshot, source → liveness callback → SetArmed, exactly as
// cmd/refractor/main.go wires it.

// taxonomyBarrierRig is one source wired to one resolver over a live NATS
// connection, plus the observation history the assertions are made against.
type taxonomyBarrierRig struct {
	conn     *substrate.Conn
	kv       jetstream.KeyValue
	src      *CoreKVSource
	resolver *taxonomy.Resolver

	mu sync.Mutex
	// snapshotObservations records, for every taxonomy snapshot the source
	// emitted, what the resolver answered for the abstract label IMMEDIATELY
	// after that snapshot was installed — i.e. what a `*` lens activating at
	// that instant would have been told. Captured inside the callback, on the
	// source's dispatch goroutine, which is the only place the mid-replay
	// state is observable at all.
	snapshotObservations []barrierObservation
	// livenessEdges records the Armed half's true/false sequence.
	livenessEdges []bool
	// sweeps counts Changed invocations, and sweepGoroutines records which
	// goroutine each ran on — the observation that proves the sweep is
	// single-producer, which every ordering argument downstream of it
	// (rederiveEntry's publish-then-baseline, the registry's
	// check-then-insert) is built on.
	sweeps          int
	sweepGoroutines map[string]struct{}

	// onObservation, when set before Start, runs at the end of every snapshot
	// callback — still ON the source's dispatch goroutine, and still inside
	// handle(). A test that blocks in here is holding the dispatch goroutine
	// exactly where the ordering guarantee has to hold.
	onObservation func(barrierObservation)
}

type barrierObservation struct {
	entries int
	status  taxonomy.Status
	leaves  int
}

func (r *taxonomyBarrierRig) observations() []barrierObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]barrierObservation(nil), r.snapshotObservations...)
}

func (r *taxonomyBarrierRig) edges() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.livenessEdges...)
}

// sweepProducers returns how many distinct goroutines have run a
// re-derivation sweep (from either the snapshot path or the liveness path).
func (r *taxonomyBarrierRig) sweepProducers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sweepGoroutines)
}

// livenessSweeps returns how many times the Changed half has run.
func (r *taxonomyBarrierRig) livenessSweeps() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sweeps
}

// expandLocation asks the resolver what a `*` lens on the abstract label
// would be told right now.
func (r *taxonomyBarrierRig) expandLocation() (int, taxonomy.Status) {
	exp, _, status, _ := r.resolver.Expand(map[string]struct{}{"location": {}})
	return len(exp["location"]), status
}

// newTaxonomyBarrierRig builds the rig against the NATS URL given, creating
// the core-kv bucket. The source is NOT started — callers seed the corpus
// first when they want it to arrive as boot-replay history.
func newTaxonomyBarrierRig(t *testing.T, ctx context.Context, url string) *taxonomyBarrierRig {
	t.Helper()
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{
		URL: url,
		// Reconnect forever, promptly: the reconnect test blocks the
		// connection for as long as it takes to OBSERVE the disarm, and a
		// bounded reconnect budget would turn that observation window into a
		// permanently closed connection.
		MaxReconnects: -1,
		ReconnectWait: 25 * time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	kv, err := conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-kv"})
	require.NoError(t, err)

	rig := &taxonomyBarrierRig{
		conn:            conn,
		kv:              kv,
		src:             NewCoreKVSource(conn, "core-kv", "barrier", discardTestLogger()),
		resolver:        taxonomy.New(),
		sweepGoroutines: map[string]struct{}{},
	}
	// The production wiring, verbatim (cmd/refractor/main.go): the snapshot
	// installs the graph and says nothing about currency; the liveness edge
	// is the only thing that arms.
	rig.src.SetTaxonomyCallback(func(snap []taxonomy.TypeSnapshot) {
		rig.resolver.InstallSnapshot(snap)
		leaves, status := rig.expandLocation()
		obs := barrierObservation{entries: len(snap), status: status, leaves: leaves}
		rig.mu.Lock()
		rig.snapshotObservations = append(rig.snapshotObservations, obs)
		// The snapshot path re-derives too (main.go calls rl.taxonomyChanged
		// here as well), so it is the OTHER producer the confinement claim is
		// about — recorded in the same set as the liveness sweeps.
		rig.sweepGoroutines[currentGoroutineID()] = struct{}{}
		hook := rig.onObservation
		rig.mu.Unlock()
		if hook != nil {
			hook(obs)
		}
	})
	rig.src.SetTaxonomyLivenessCallbacks(TaxonomyLiveness{
		Armed: func(armed bool) {
			rig.resolver.SetArmed(armed)
			rig.mu.Lock()
			rig.livenessEdges = append(rig.livenessEdges, armed)
			rig.mu.Unlock()
		},
		Changed: func() {
			rig.mu.Lock()
			rig.sweeps++
			rig.sweepGoroutines[currentGoroutineID()] = struct{}{}
			rig.mu.Unlock()
		},
	})
	return rig
}

// currentGoroutineID extracts the running goroutine's id from its own stack
// header. It exists for one assertion the barrier's correctness turns on and
// that no other observation can make: that EVERY re-derivation sweep runs on
// one goroutine. Confinement is not a property of the callback's contents, so
// counting sweeps or ordering them proves nothing — only their identity does.
func currentGoroutineID() string {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// "goroutine 42 [running]:" — the id is the second field.
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return "unknown"
	}
	return fields[1]
}

// seedTaxonomyLeaves writes one abstract `location` plus n concrete leaves
// under it. Each leaf costs three keys (root, canonicalName, subtypeOf link),
// so n=6 makes a 20-key replay — long enough that the source is provably
// partway through it when the first snapshots are emitted.
func seedTaxonomyLeaves(t *testing.T, ctx context.Context, kv jetstream.KeyValue, parentID string, leafIDs []string) {
	t.Helper()
	put := func(key string, body []byte) {
		t.Helper()
		_, err := kv.Put(ctx, key, body)
		require.NoError(t, err)
	}
	put("vtx.meta."+parentID, vertexTypeBody(t, true))
	put("vtx.meta."+parentID+".canonicalName", canonicalNameBody(t, "location"))
	for i, leafID := range leafIDs {
		put("vtx.meta."+leafID, vertexTypeBody(t, false))
		put("vtx.meta."+leafID+".canonicalName", canonicalNameBody(t, fmt.Sprintf("unit%d", i)))
		put("lnk.meta."+leafID+".subtypeOf.meta."+parentID, subtypeOfLinkBody(t))
	}
}

// shrinkArmPollInterval makes the liveness watcher probe fast enough for a
// test's patience, restoring the production value afterwards. Only the
// LATENCY of arming moves; nothing about what the probe has to establish
// before it arms does.
func shrinkArmPollInterval(t *testing.T) {
	t.Helper()
	prev := taxonomyArmPollInterval
	taxonomyArmPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { taxonomyArmPollInterval = prev })
}

func barrierNanoIDs(t *testing.T, n int) []string {
	t.Helper()
	out := make([]string, n)
	for i := range out {
		out[i] = mustNanoID(t)
	}
	return out
}

// TestCoreKVSource_TaxonomyBarrier_NoSnapshotDuringBootReplayIsAnswveredArmed
// is the core of §14 Fire C item 5's first shape. The whole taxonomy is
// written BEFORE the source starts, so every event the source sees is boot
// replay, and the snapshots it emits partway through describe a strict prefix
// of the graph — `location` resolving to two leaves when it has six.
//
// A `*` lens activating in that window would publish a narrowed consumer
// filter and a client gate over that prefix, and the four leaves it has not
// heard of yet get silently acked-and-dropped for the life of the pipeline —
// §6.5's one unacceptable state. So NO snapshot emitted during the replay may
// be answered StatusArmed, however complete it happens to look from the
// inside: mid-replay, a leaf whose meta has not been delivered yet is
// indistinguishable from one that does not exist.
//
// The assertion is made inside the snapshot callback because that is the only
// place the mid-replay state is observable — by the time the test goroutine
// could look, the replay is over and the resolver has (correctly) armed.
func TestCoreKVSource_TaxonomyBarrier_NoSnapshotDuringBootReplayIsAnsweredArmed(t *testing.T) {
	shrinkArmPollInterval(t)
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rig := newTaxonomyBarrierRig(t, ctx, s.ClientURL())
	parentID := mustNanoID(t)
	leafIDs := barrierNanoIDs(t, 6)
	seedTaxonomyLeaves(t, ctx, rig.kv, parentID, leafIDs)

	// Hold the dispatch goroutine inside the callback for the LAST replayed
	// event — the one that completes the closure — and prove the resolver
	// cannot arm underneath it. This is the ordering half of the barrier, and
	// it is a separate proposition from "the drain test is applied at all":
	// by the time the final event is being handled, the durable HAS drained
	// (every message acked, none pending), so a caught-up verdict applied on
	// the probing goroutine would arm right here, over a snapshot the
	// dispatch goroutine has not finished installing. Routing the verdict
	// through consume's own select is what makes that unreachable — consume
	// cannot receive while it is inside handle().
	var holdOnce sync.Once
	held := make(chan struct{})
	release := make(chan struct{})
	rig.onObservation = func(o barrierObservation) {
		if o.leaves < len(leafIDs) {
			return
		}
		holdOnce.Do(func() {
			close(held)
			<-release
		})
	}

	require.NoError(t, rig.src.Start(ctx))

	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("the replay never reached the snapshot that completes the closure")
	}
	require.Never(t, func() bool {
		_, status := rig.expandLocation()
		return status == taxonomy.StatusArmed
	}, 500*time.Millisecond, 10*time.Millisecond,
		"the resolver armed while the dispatch goroutine was still inside the snapshot callback — "+
			"a verdict applied off that goroutine races whatever handle() has not finished folding in")
	close(release)

	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		return status == taxonomy.StatusArmed && leaves == len(leafIDs)
	}, 20*time.Second, 10*time.Millisecond,
		"the source must eventually arm on the complete taxonomy — a barrier that never lifts is not fail-closed, it is broken")

	obs := rig.observations()
	require.NotEmpty(t, obs, "the replay must have emitted snapshots")
	require.Less(t, obs[0].leaves, len(leafIDs),
		"precondition: the first snapshot must be a genuine PREFIX of the taxonomy, or this test proves nothing")

	for i, o := range obs {
		require.NotEqual(t, taxonomy.StatusArmed, o.status,
			"snapshot %d of %d (entries=%d, leaves=%d) was answered StatusArmed during the boot replay: "+
				"a `*` lens activating here narrows on a downward closure whose remaining leaves have not been read yet",
			i, len(obs), o.entries, o.leaves)
	}
}

// TestCoreKVSource_TaxonomyBarrier_ArmsOnceTheReplayIsDrained is the other
// half: fail-closed must be a WINDOW, not a posture. Once the durable reports
// nothing pending and nothing delivered-but-unacked — and the dispatch
// goroutine has processed every one of those events, which is what routing
// the verdict through its own select buys — the snapshot genuinely is the
// whole taxonomy and the resolver arms with the complete closure.
//
// Pinning the closure SIZE at the moment of arming, not merely the status, is
// what makes this test able to fail: arming early would still reach
// StatusArmed, just over a prefix.
func TestCoreKVSource_TaxonomyBarrier_ArmsOnceTheReplayIsDrained(t *testing.T) {
	shrinkArmPollInterval(t)
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rig := newTaxonomyBarrierRig(t, ctx, s.ClientURL())
	parentID := mustNanoID(t)
	leafIDs := barrierNanoIDs(t, 6)
	seedTaxonomyLeaves(t, ctx, rig.kv, parentID, leafIDs)

	require.NoError(t, rig.src.Start(ctx))

	var armedLeaves int
	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		if status != taxonomy.StatusArmed {
			return false
		}
		armedLeaves = leaves
		return true
	}, 20*time.Second, 10*time.Millisecond, "the resolver never armed after the replay drained")

	require.Equal(t, len(leafIDs), armedLeaves,
		"the FIRST armed answer must already carry the whole closure — an armed answer over a prefix is the defect, "+
			"and it reaches StatusArmed just the same")
	require.Equal(t, []bool{true}, rig.edges(),
		"one arming edge, no flapping: each edge costs a re-derivation sweep across every live `*` lens")

	// A live edit after arming keeps the resolver armed — the barrier gates
	// the boot replay, it does not re-gate every subsequent event.
	extraLeaf := mustNanoID(t)
	_, err := rig.kv.Put(ctx, "vtx.meta."+extraLeaf, vertexTypeBody(t, false))
	require.NoError(t, err)
	_, err = rig.kv.Put(ctx, "vtx.meta."+extraLeaf+".canonicalName", canonicalNameBody(t, "penthouse"))
	require.NoError(t, err)
	_, err = rig.kv.Put(ctx, "lnk.meta."+extraLeaf+".subtypeOf.meta."+parentID, subtypeOfLinkBody(t))
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		return status == taxonomy.StatusArmed && leaves == len(leafIDs)+1
	}, 20*time.Second, 10*time.Millisecond,
		"a live taxonomy edit must land on an already-armed resolver without disarming it")
	require.Equal(t, []bool{true}, rig.edges(), "a snapshot is not a liveness edge")
}

// TestCoreKVSource_TaxonomyBarrier_ConnectionLossDisarmsAndTheDrainedFeedReArms
// is §14 Fire C item 5's second shape. A dead subscription is not the only
// way a source stops seeing the taxonomy: for the length of a NATS
// disconnect it sees nothing at all, and a resolver still answering
// StatusArmed through that window has a lens narrowing against a taxonomy
// nobody is maintaining. The durable loses no events — it resumes from its
// ack floor — which is exactly what makes the failure silent: everything
// reconciles afterwards, and the only casualty is the window in between.
//
// The connection is cut through a proxy that then REFUSES reconnects, so the
// disarm is observed deterministically rather than raced against a fast
// reconnect. Restoring the proxy proves the other half: the source re-arms
// only after the re-established feed drains again, and the taxonomy edit
// written during the outage is in the closure when it does.
func TestCoreKVSource_TaxonomyBarrier_ConnectionLossDisarmsAndTheDrainedFeedReArms(t *testing.T) {
	shrinkArmPollInterval(t)
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	proxy := newCuttableProxy(t, s.Addr().String())
	rig := newTaxonomyBarrierRig(t, ctx, proxy.url)
	parentID := mustNanoID(t)
	leafIDs := barrierNanoIDs(t, 3)
	seedTaxonomyLeaves(t, ctx, rig.kv, parentID, leafIDs)

	require.NoError(t, rig.src.Start(ctx))
	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		return status == taxonomy.StatusArmed && leaves == len(leafIDs)
	}, 20*time.Second, 10*time.Millisecond, "precondition: the source arms on the seeded taxonomy")

	proxy.cut()
	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		// The snapshot survives — the closure is still there to answer from.
		// Only the CURRENCY claim is withdrawn, which is the difference
		// between StatusStale (run broad) and StatusUnknown (refuse).
		return status == taxonomy.StatusStale && leaves == len(leafIDs)
	}, 20*time.Second, 10*time.Millisecond,
		"a connection loss must withdraw the armed claim: the source sees no taxonomy edits at all while disconnected")
	require.Equal(t, []bool{true, false}, rig.edges())

	proxy.restore()
	require.Eventually(t, rig.conn.Connected, 20*time.Second, 10*time.Millisecond,
		"the proxy is accepting again — nats.go must reconnect")

	// The edit lands on the re-established connection; the durable delivers it
	// from its ack floor.
	extraLeaf := mustNanoID(t)
	putAfterReconnect := func(key string, body []byte) {
		require.Eventually(t, func() bool {
			_, err := rig.kv.Put(ctx, key, body)
			return err == nil
		}, 20*time.Second, 20*time.Millisecond, "write %q after reconnect", key)
	}
	putAfterReconnect("vtx.meta."+extraLeaf, vertexTypeBody(t, false))
	putAfterReconnect("vtx.meta."+extraLeaf+".canonicalName", canonicalNameBody(t, "cellar"))
	putAfterReconnect("lnk.meta."+extraLeaf+".subtypeOf.meta."+parentID, subtypeOfLinkBody(t))

	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		return status == taxonomy.StatusArmed && leaves == len(leafIDs)+1
	}, 30*time.Second, 10*time.Millisecond,
		"the re-established feed must re-arm once drained, with the edit written during the outage in the closure")
	require.Equal(t, []bool{true, false, true}, rig.edges(),
		"exactly one edge per real transition — a reconnect that re-armed on the reconnect EDGE would show up as an "+
			"extra true before the feed had re-delivered anything")

	// The confinement claim, asserted over the run that exercised every
	// producer: snapshots during replay, an arm, a connection-loss disarm, and
	// a re-arm. Each of those re-derives the live lens corpus in production,
	// and every ordering argument downstream assumes exactly one goroutine
	// does it — rederiveEntry publishes its gate outside entry.taxMu and
	// records its baseline inside it, so two producers commit baselines in the
	// opposite order from their publishes and leave a narrow gate recorded as
	// armed; and activateIfNotRegistered drops its lock between the existence
	// check and the insert, so two producers register two pipelines for one
	// lens. Neither is detectable after the fact, which is why the goroutine
	// identity is asserted directly rather than the symptoms.
	// Every transition owes a sweep, and the DISARM's is the one that can go
	// missing without anything else looking wrong: it is decided on another
	// goroutine and handed over, so a hand-over that silently drops leaves the
	// flag correctly false while every live `*` lens keeps serving through the
	// narrow client gate it took while armed — acking-and-dropping against a
	// taxonomy nobody is maintaining, which is the whole state that must stay
	// unreachable. Counting sweeps against EDGES is what catches that; a lower
	// bound would be satisfied by the two arms alone.
	require.Eventually(t, func() bool { return rig.livenessSweeps() == len(rig.edges()) },
		20*time.Second, 10*time.Millisecond,
		"every liveness transition must produce exactly one re-derivation sweep — got %d sweeps for %v",
		rig.livenessSweeps(), rig.edges())
	require.Equal(t, 1, rig.sweepProducers(),
		"every re-derivation sweep must run on ONE goroutine — a disarm sweeping from substrate's "+
			"connection-state goroutine is a second writer to the reloader, which its check-then-register "+
			"and publish-then-baseline sequences are not written to survive")
}

// TestCoreKVSource_TaxonomyBarrier_VerdictStraddlingAConnectionLossIsDiscarded
// pins the epoch, which the other tests cannot reach: they never produce a
// caught-up verdict that is IN FLIGHT across a connection loss, so deleting
// the epoch comparison leaves them all green.
//
// The window is real and the barrier depends on closing it. A probe reads the
// consumer's counters, and the verdict travels to the dispatch goroutine
// before it is applied; a connection lost inside that gap means the verdict
// describes a feed that no longer exists, and applying it re-arms a resolver
// that is blind from the instant it goes live. Here the dispatch goroutine is
// held inside a snapshot callback (so a verdict is provably parked on armCh),
// the connection is severed, and the verdict is then released into a source
// that must refuse it.
func TestCoreKVSource_TaxonomyBarrier_VerdictStraddlingAConnectionLossIsDiscarded(t *testing.T) {
	shrinkArmPollInterval(t)
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	proxy := newCuttableProxy(t, s.Addr().String())
	rig := newTaxonomyBarrierRig(t, ctx, proxy.url)
	parentID := mustNanoID(t)
	leafIDs := barrierNanoIDs(t, 3)
	seedTaxonomyLeaves(t, ctx, rig.kv, parentID, leafIDs)

	var holdOnce sync.Once
	held := make(chan struct{})
	release := make(chan struct{})
	rig.onObservation = func(o barrierObservation) {
		if o.leaves < len(leafIDs) {
			return
		}
		holdOnce.Do(func() {
			close(held)
			<-release
		})
	}

	require.NoError(t, rig.src.Start(ctx))
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("the replay never reached the snapshot that completes the closure")
	}

	// The POSITIVE control, and this test is worthless without it. The
	// durable drains while the dispatch goroutine sits in the callback, so
	// the watcher's probe answers caught-up and parks its verdict on armCh —
	// but only once it has actually run. Cutting before that happens leaves
	// the watcher skipping on `!Connected()` forever, no verdict is ever
	// produced, and the "did not arm" assertion below passes for a reason
	// that has nothing to do with the epoch.
	require.Eventually(t, func() bool {
		return rig.src.TaxonomyLivenessStatus().DrainedVerdicts > 0
	}, 20*time.Second, 5*time.Millisecond,
		"precondition: a caught-up verdict must exist and be parked on armCh BEFORE the connection is cut")

	proxy.cut()
	require.Eventually(t, func() bool { return !rig.conn.Connected() }, 20*time.Second, 10*time.Millisecond,
		"the severed connection must be observed as down before the parked verdict is released")
	close(release)

	require.Never(t, func() bool {
		_, status := rig.expandLocation()
		return status == taxonomy.StatusArmed
	}, 2*time.Second, 20*time.Millisecond,
		"a caught-up verdict computed BEFORE a connection loss must not arm the resolver after it — "+
			"the feed it measured is gone, and everything written during the outage is unseen")
	require.Empty(t, rig.edges(), "no arming edge should ever have been reported")
}

// TestCoreKVSource_TaxonomyBarrier_VerdictAppliedBeforeTheDisarmCallbackIsDiscarded
// pins the ORDERING the test above can only race for, and it is the ordering
// the whole epoch comparison silently assumed: that by the time a verdict is
// applied, a connection loss preceding it has already bumped the epoch.
//
// It has not, and nothing makes it. nats.go's processOpErr flips the
// connection's own status under the connection lock and then spawns
// doReconnect, which QUEUES the disconnect callback onto the single serial
// async-callback goroutine every callback in the process shares; substrate's
// fan-out — and so connectionLost, and so the epoch bump — runs whenever that
// queue reaches it. Meanwhile the verdict's own hand-over is unbounded in the
// other direction: it is parked on armCh until the dispatch goroutine leaves
// handle(), which can be a whole lens activation. Two unbounded delays with
// no ordering between them is not a race that is unlikely to lose, it is a
// coin flip, and the losing side arms a resolver over a feed that is gone.
//
// Held deterministically rather than raced: a listener registered BEFORE the
// source's own occupies the head of substrate's fan-out (registration order,
// one goroutine at a time), so blocking in it holds connectionLost — and the
// epoch — exactly where the assumption sits, with the connection provably
// already down.
func TestCoreKVSource_TaxonomyBarrier_VerdictAppliedBeforeTheDisarmCallbackIsDiscarded(t *testing.T) {
	shrinkArmPollInterval(t)
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	proxy := newCuttableProxy(t, s.Addr().String())
	rig := newTaxonomyBarrierRig(t, ctx, proxy.url)
	parentID := mustNanoID(t)
	leafIDs := barrierNanoIDs(t, 3)
	seedTaxonomyLeaves(t, ctx, rig.kv, parentID, leafIDs)

	// Registered before Start, so it sits ahead of the source's own listener
	// in substrate's fan-out. Released unconditionally at teardown: the
	// fan-out goroutine is nats.go's, and leaving it parked would wedge the
	// connection's Close.
	gateEntered := make(chan struct{})
	gateRelease := make(chan struct{})
	var gateOnce, releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(func() { close(gateRelease) }) }
	t.Cleanup(releaseGate)
	rig.conn.OnConnectionStateChange(func(connected bool) {
		if connected {
			return
		}
		gateOnce.Do(func() {
			close(gateEntered)
			<-gateRelease
		})
	})

	var holdOnce sync.Once
	held := make(chan struct{})
	release := make(chan struct{})
	rig.onObservation = func(o barrierObservation) {
		if o.leaves < len(leafIDs) {
			return
		}
		holdOnce.Do(func() {
			close(held)
			<-release
		})
	}

	require.NoError(t, rig.src.Start(ctx))
	select {
	case <-held:
	case <-ctx.Done():
		t.Fatal("the replay never reached the snapshot that completes the closure")
	}

	// Same positive control as the test above: a verdict must exist and be
	// parked on armCh before the connection is cut, or the refusal below has
	// nothing to refuse.
	require.Eventually(t, func() bool {
		return rig.src.TaxonomyLivenessStatus().DrainedVerdicts > 0
	}, 20*time.Second, 5*time.Millisecond,
		"precondition: a caught-up verdict must exist and be parked on armCh BEFORE the connection is cut")

	proxy.cut()
	select {
	case <-gateEntered:
	case <-ctx.Done():
		t.Fatal("the connection-state fan-out never reported the loss")
	}
	// The window is now pinned open from both ends: the connection is down
	// and readable as down, and connectionLost is parked one listener behind
	// this one, so the epoch still holds the value the parked verdict was
	// measured under.
	require.False(t, rig.conn.Connected(),
		"precondition: the connection must read as down while the disarm callback is still held")
	rig.src.taxLiveMu.Lock()
	epochWhileHeld := rig.src.taxonomyEpoch
	rig.src.taxLiveMu.Unlock()
	require.Zero(t, epochWhileHeld,
		"precondition: the epoch must NOT have moved yet — that lag is the window under test, and a test "+
			"that lets it close first proves only what the epoch comparison already proved")

	close(release)
	require.Never(t, func() bool {
		_, status := rig.expandLocation()
		return status == taxonomy.StatusArmed
	}, 2*time.Second, 20*time.Millisecond,
		"a verdict measured on a connection that is now down must be refused on the connection's own evidence — "+
			"waiting to be TOLD the connection dropped arms the resolver in the meantime")
	require.Empty(t, rig.edges(), "no arming edge should ever have been reported")

	// And the refusal must not have wedged anything: once the disarm lands and
	// the connection is genuinely back, the ordinary drain probe re-arms.
	releaseGate()
	proxy.restore()
	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		return status == taxonomy.StatusArmed && leaves == len(leafIDs)
	}, 30*time.Second, 10*time.Millisecond,
		"a discarded verdict must cost only latency — the re-established feed re-arms through the same probe")
	require.Equal(t, []bool{true}, rig.edges(),
		"exactly one arming edge, the one earned after the reconnect drained")
}

// TestCoreKVSource_TaxonomyBarrier_ConsumerConfigKeepsTheDrainTestSound pins
// the three consumer settings the barrier's soundness rests on, on the live
// consumer rather than in the call that asks for them.
//
// ConsumerCaughtUp answers "has everything been handled" by reading two
// counters, and that is only equivalent to the question while every message is
// in exactly one of them. A message whose delivery budget is exhausted is in
// NEITHER: nats-server drops it from the pending set having already counted
// the delivery, so the probe reports a drained feed over a taxonomy missing
// whatever that message carried — a type, or a subtypeOf edge — with no way to
// tell it from a type that never existed.
//
// Unlimited MaxDeliver removes the budget. MaxPrefetch 1 and a long AckWait
// stop a redelivery being manufactured by this consumer's own serial
// dispatch: the ack clock starts when a message is delivered into the client
// buffer, and handle() can spend a lens activation's worth of time on the
// message ahead of it. Each is a config-dependent premise of a correctness
// claim, so each is asserted rather than assumed.
func TestCoreKVSource_TaxonomyBarrier_ConsumerConfigKeepsTheDrainTestSound(t *testing.T) {
	shrinkArmPollInterval(t)
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rig := newTaxonomyBarrierRig(t, ctx, s.ClientURL())
	parentID := mustNanoID(t)
	leafIDs := barrierNanoIDs(t, 1)
	seedTaxonomyLeaves(t, ctx, rig.kv, parentID, leafIDs)
	require.NoError(t, rig.src.Start(ctx))
	require.Eventually(t, func() bool {
		_, status := rig.expandLocation()
		return status == taxonomy.StatusArmed
	}, 20*time.Second, 10*time.Millisecond, "precondition: the source is running against its durable")

	// The reopen hook is asserted here rather than at the live consumer,
	// because it is not a consumer setting at all — it is the wiring that
	// tells this source a stalled iterator happened. Nothing else can catch
	// its absence: without it the subscription behaves identically in every
	// observable way, and the only difference is that the resolver stays
	// armed across a window it saw nothing in.
	require.NotNil(t, rig.src.subscribeOptions().OnReopen,
		"the subscription must announce a reopen — a stall leaves the channel open, the connection up, and "+
			"no other trace that this source went blind")

	stream, err := rig.conn.JetStream().Stream(ctx, "KV_core-kv")
	require.NoError(t, err)
	var checked int
	names := stream.ConsumerNames(ctx)
	for name := range names.Name() {
		if !strings.HasPrefix(name, "refractor-lens-source") {
			continue
		}
		cons, cErr := rig.conn.JetStream().Consumer(ctx, "KV_core-kv", name)
		require.NoError(t, cErr)
		info, iErr := cons.Info(ctx)
		require.NoError(t, iErr)
		require.Equal(t, -1, info.Config.MaxDeliver,
			"the taxonomy consumer must redeliver without bound — a Termed taxonomy event is state loss with no "+
				"other write path, and it leaves the drain probe reporting a currency the snapshot does not have")
		require.Equal(t, taxonomyEventAckWait, info.Config.AckWait,
			"the ack clock must outlast the slowest thing handle() does per event")
		checked++
	}
	require.NoError(t, names.Err())
	require.Equal(t, 1, checked, "exactly one lens-source durable should exist for this boot")
}

// TestCoreKVSource_TaxonomyBarrier_ASubscriptionReopenDisarms closes the last
// window of this class, and it is the quietest one. A stalled messages
// iterator is re-opened against the same durable: the channel never closes,
// the NATS connection never drops, and nothing is LOST — everything
// undelivered comes back from the ack floor. All of which makes it look like a
// non-event. What it actually is, is a stretch of time in which this source
// read no taxonomy at all, and a resolver answering StatusArmed across it is
// telling a `*` lens that its narrowed client gate — which acks-and-drops
// everything outside the resolved set — is safe against a taxonomy nobody was
// reading.
//
// So a reopen is treated exactly like a connection loss: disarm, bump the
// epoch (so a drain verdict computed before the gap cannot arm after it), owe
// a sweep. Re-arming goes back through the ordinary drain probe, which is why
// nothing new has to be proven about recovery.
//
// DRIVEN AS A UNIT, deliberately, with the reason recorded rather than left
// for a reader to wonder about: a real transient stall cannot be produced in
// test time. Measured on this fixture, 2s, 5s and 10s connection outages each
// produce ZERO iterator errors — nats.go absorbs them internally — and the
// heartbeat path that does surface one needs ~30s of silence at the default
// interval. substrate's TestRunKVSubscription_ReopenIsAnnouncedToTheCaller
// proves the other half (that a stall reaches this hook) with an injected
// stall; the seam between the two is Start's OnReopen field, a struct literal.
func TestCoreKVSource_TaxonomyBarrier_ASubscriptionReopenDisarms(t *testing.T) {
	shrinkArmPollInterval(t)
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	rig := newTaxonomyBarrierRig(t, ctx, s.ClientURL())
	parentID := mustNanoID(t)
	leafIDs := barrierNanoIDs(t, 3)
	seedTaxonomyLeaves(t, ctx, rig.kv, parentID, leafIDs)

	require.NoError(t, rig.src.Start(ctx))
	require.Eventually(t, func() bool {
		leaves, status := rig.expandLocation()
		return status == taxonomy.StatusArmed && leaves == len(leafIDs)
	}, 20*time.Second, 10*time.Millisecond, "precondition: the source arms on the seeded taxonomy")

	rig.src.taxLiveMu.Lock()
	epochBefore := rig.src.taxonomyEpoch
	rig.src.taxLiveMu.Unlock()

	rig.src.subscriptionReopened()

	_, status := rig.expandLocation()
	require.Equal(t, taxonomy.StatusStale, status,
		"the armed claim must be withdrawn immediately — the flag flip is the fail-closed half and does not "+
			"wait on the dispatch goroutine")
	rig.src.taxLiveMu.Lock()
	epochAfter := rig.src.taxonomyEpoch
	rig.src.taxLiveMu.Unlock()
	require.Greater(t, epochAfter, epochBefore,
		"the epoch must move, or a drain verdict computed before the gap arms the resolver after it")
	require.Equal(t, []bool{true, false}, rig.edges())

	// The sweep is owed to the dispatch goroutine, and the sweep is what
	// widens every live `*` lens back off the narrow gate it took while armed.
	require.Eventually(t, func() bool { return rig.livenessSweeps() == len(rig.edges()) },
		20*time.Second, 10*time.Millisecond,
		"the reopen's re-derivation sweep must run — the flag alone leaves every live `*` lens still serving "+
			"through the narrow client gate it published while armed")

	// Recovery is the ordinary path: the drain probe re-arms once the feed is
	// provably current again, with no special case for having stalled.
	require.Eventually(t, func() bool {
		leaves, st := rig.expandLocation()
		return st == taxonomy.StatusArmed && leaves == len(leafIDs)
	}, 20*time.Second, 10*time.Millisecond,
		"a re-opened subscription must re-arm through the same drain probe as boot and reconnect")
	require.Equal(t, []bool{true, false, true}, rig.edges())
	require.Equal(t, 1, rig.sweepProducers(),
		"and every sweep along the way must stay on the one goroutine")
}

// TestCoreKVSource_TaxonomyBarrier_DeadSubscriptionCanNeverArm pins the latch
// the drain test alone would invert. A dead subscription leaves this boot's
// durable sitting in the JetStream catalog with nothing consuming it, so it
// reports nothing pending and nothing unacked — the drained test passes
// PERFECTLY, forever, on a source that has stopped reading the taxonomy
// altogether. Without the latch the liveness watcher would read that as proof
// of currency and arm a resolver whose snapshot is frozen at the moment its
// consumer died.
//
// Driven through the state machine directly rather than by killing a real
// subscription: the proposition is about what a caught-up verdict is allowed
// to do AFTER the death, and hand-delivering the verdict is the only way to
// exercise that without waiting on JetStream's own failure timing.
//
// The connection is real, and has to be, even though nothing is subscribed on
// it. A hand-delivered verdict is validated against the connection it claims
// to have been measured on, so a source holding no connection refuses every
// verdict for that reason alone — and this test would then pass with the
// death latch deleted, proving nothing about it.
func TestCoreKVSource_TaxonomyBarrier_DeadSubscriptionCanNeverArm(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: s.ClientURL()})
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	src := NewCoreKVSource(conn, "core-kv", "dead-latch", discardTestLogger())
	var edges []bool
	src.SetTaxonomyLivenessCallbacks(TaxonomyLiveness{
		Armed:   func(armed bool) { edges = append(edges, armed) },
		Changed: func() {},
	})

	src.taxonomyConsumerDead()

	src.taxLiveMu.Lock()
	epoch := src.taxonomyEpoch
	src.taxLiveMu.Unlock()
	connGeneration, connected := src.connectionGeneration()
	require.True(t, connected,
		"precondition: the verdict must be hand-delivered on a live connection, or the death latch is not what refuses it")
	src.armTaxonomy(taxonomyVerdict{epoch: epoch, connGeneration: connGeneration})

	require.Empty(t, edges,
		"a caught-up verdict after the subscription died must never arm — the durable reads drained precisely "+
			"because nobody is consuming it")
	src.taxLiveMu.Lock()
	defer src.taxLiveMu.Unlock()
	require.False(t, src.taxonomyLive)
}

// cuttableProxy is a TCP proxy that can sever every live connection and then
// refuse new ones until restored — a connection loss whose LENGTH the test
// controls, which is what makes the disarm observable instead of raced
// against nats.go's own reconnect. It mirrors the proxy helpers in
// internal/substrate/conn_timeout_test.go (stallProxy/resetProxy), which
// stand in for the same class of host-level fault.
type cuttableProxy struct {
	url string

	mu      sync.Mutex
	live    []net.Conn
	blocked bool
}

func newCuttableProxy(t *testing.T, backend string) *cuttableProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	p := &cuttableProxy{url: "nats://" + ln.Addr().String()}
	go func() {
		for {
			cc, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			blocked := p.blocked
			p.mu.Unlock()
			if blocked {
				_ = cc.Close()
				continue
			}
			sc, err := net.Dial("tcp", backend)
			if err != nil {
				_ = cc.Close()
				continue
			}
			p.mu.Lock()
			p.live = append(p.live, cc, sc)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(sc, cc); _ = sc.Close() }()
			go func() { _, _ = io.Copy(cc, sc); _ = cc.Close() }()
		}
	}()
	return p
}

// cut severs every live connection and refuses new ones until restore. Both
// halves are needed: severing alone would be repaired by nats.go's reconnect
// within milliseconds, and the window under test would close before it could
// be observed.
func (p *cuttableProxy) cut() {
	p.mu.Lock()
	live := p.live
	p.live = nil
	p.blocked = true
	p.mu.Unlock()
	for _, c := range live {
		_ = c.Close()
	}
}

func (p *cuttableProxy) restore() {
	p.mu.Lock()
	p.blocked = false
	p.mu.Unlock()
}
