package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	edgestore "github.com/operatinggraph/lattice/internal/edge/store"
	"github.com/operatinggraph/lattice/internal/edge/transport"
	"github.com/operatinggraph/lattice/internal/refractor/control/controlwire"
)

// The first-paint gate's tests drive Manager.hydrate and Manager.handle
// directly against a fake control plane and a real Local VAL Store: the gate is
// client-local paint sequencing, and every input it reacts to — a hydrate
// response, a delivered sequence, a timer — is reachable without a broker.

// gateCallbackWait bounds how long a test waits for OnHydrationComplete. It is
// a failure ceiling, not a timing assumption: every release under test is
// either synchronous with a handle()/hydrate() call or driven by a deadline set
// in tens of milliseconds.
const gateCallbackWait = 10 * time.Second

// gateLogs records every record the Manager emits, so a test can assert that
// releasing on a stall or refusing a diverged position was actually REPORTED
// rather than done silently.
type gateLogs struct {
	mu      stdsync.Mutex
	records []slog.Record
}

func (h *gateLogs) Enabled(context.Context, slog.Level) bool { return true }

func (h *gateLogs) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *gateLogs) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *gateLogs) WithGroup(string) slog.Handler      { return h }

// sawAtLeast reports whether any record at or above level mentions substr.
func (h *gateLogs) sawAtLeast(level slog.Level, substr string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, r := range h.records {
		if r.Level >= level && strings.Contains(r.Message, substr) {
			return true
		}
	}
	return false
}

// gateState copies the gate under gateMu, so a white-box assertion never races
// the arming goroutine or the deadline timer.
func gateState(m *Manager) hydrateGate {
	m.gateMu.Lock()
	defer m.gateMu.Unlock()
	return m.gate
}

// gateConfig is what a harness's fake control plane answers personal.hydrate
// with, plus the deadline the Manager runs its gate on.
type gateConfig struct {
	revision uint64
	startSeq uint64
	endSeq   uint64
	// deadline defaults to a minute — long enough that a test not about the
	// deadline never meets it.
	deadline time.Duration
	// store defaults to a fresh Local VAL Store.
	store edgestore.Store
	// gapped is what personal.syncgap answers, for a harness driving Run.
	gapped bool
}

// gateHarness is one Manager wired to a control plane whose personal.hydrate
// names a chosen (revision, syncStartSeq, syncEndSeq), with first paint
// reported on a channel.
type gateHarness struct {
	t      *testing.T
	mgr    *Manager
	st     edgestore.Store
	tr     *fakeControlTransport
	logs   *gateLogs
	fired  chan uint64
	cfg    gateConfig
	nextID int
}

func newGateHarness(t *testing.T, cfg gateConfig) *gateHarness {
	t.Helper()
	st := cfg.store
	if st == nil {
		st = openTestStore(t)
	}
	deadline := cfg.deadline
	if deadline == 0 {
		deadline = time.Minute
	}
	h := &gateHarness{t: t, st: st, logs: &gateLogs{}, fired: make(chan uint64, 8), cfg: cfg}
	h.tr = &fakeControlTransport{reply: func(op string, _ controlwire.ControlRequest) (controlwire.ControlResponse, error) {
		switch op {
		case "register":
			return controlwire.ControlResponse{PersonalRegister: &controlwire.PersonalRegisterResult{Registered: true}}, nil
		case "hydrate":
			return controlwire.ControlResponse{PersonalHydrate: &controlwire.PersonalHydrateResult{
				Hydrated: true, Revision: cfg.revision, SyncStartSeq: cfg.startSeq, SyncEndSeq: cfg.endSeq,
			}}, nil
		case "syncgap":
			return controlwire.ControlResponse{PersonalSyncGap: &controlwire.PersonalSyncGapResult{Gapped: cfg.gapped}}, nil
		default:
			return controlwire.ControlResponse{Error: "unexpected op " + op}, nil
		}
	}}
	mgr, err := New(h.tr, st, Config{
		IdentityID: "identityA", DeviceID: "deviceX",
		Logger:              slog.New(h.logs),
		HydrateGateDeadline: deadline,
		OnHydrationComplete: func(revision uint64) { h.fired <- revision },
	})
	require.NoError(t, err)
	h.mgr = mgr
	// A gate armed by a test that never releases it leaves a live timer behind,
	// which would fire into this test's callback after it finished.
	t.Cleanup(mgr.disarmHydrateGate)
	return h
}

// armCold arms the gate the way ensureFresh does — cold boot, gap, or an
// operator-requested hydration, with the consumer not yet started.
func (h *gateHarness) armCold(ctx context.Context) {
	h.t.Helper()
	_, err := h.mgr.hydrate(ctx, armCold)
	require.NoError(h.t, err)
}

// armLive arms the gate the way Rehydrate does, against a consumer that is
// already attached.
func (h *gateHarness) armLive(ctx context.Context) {
	h.t.Helper()
	require.NoError(h.t, h.mgr.Rehydrate(ctx))
}

func (h *gateHarness) body(env deltaEnvelope) []byte {
	h.t.Helper()
	b, err := json.Marshal(env)
	require.NoError(h.t, err)
	return b
}

// deliver hands one delta to the Manager exactly as the durable consumer would.
func (h *gateHarness) deliver(ctx context.Context, seq uint64, env deltaEnvelope) transport.Decision {
	h.t.Helper()
	return h.mgr.handle(ctx, transport.Delta{Sequence: seq, Body: h.body(env)})
}

// row delivers one ordinary upsert at seq, under a key unique to this harness.
func (h *gateHarness) row(ctx context.Context, seq uint64, lens string) transport.Decision {
	h.t.Helper()
	h.nextID++
	key := fmt.Sprintf("manifest.task.firstpaintrow%07d", h.nextID)
	return h.deliver(ctx, seq, deltaEnvelope{
		Op: "upsert", Key: key, Lens: lens, Revision: seq, Data: json.RawMessage(`{"row":true}`),
	})
}

// rowKeyed delivers an upsert at seq under an explicit key, for the gated store
// that must fail one of them.
func (h *gateHarness) rowKeyed(ctx context.Context, seq uint64, key string) transport.Decision {
	h.t.Helper()
	return h.deliver(ctx, seq, deltaEnvelope{
		Op: "upsert", Key: key, Lens: "rule-1", Revision: seq, Data: json.RawMessage(`{"row":true}`),
	})
}

func (h *gateHarness) keyset(ctx context.Context, seq uint64, lens string) transport.Decision {
	h.t.Helper()
	return h.deliver(ctx, seq, deltaEnvelope{Op: "keyset", Lens: lens, Revision: seq})
}

func (h *gateHarness) marker(ctx context.Context, seq, revision uint64) transport.Decision {
	h.t.Helper()
	return h.deliver(ctx, seq, deltaEnvelope{Op: "hydrationComplete", Revision: revision})
}

// awaitFired blocks until first paint is announced and returns the revision it
// was announced with.
func (h *gateHarness) awaitFired() uint64 {
	h.t.Helper()
	select {
	case revision := <-h.fired:
		return revision
	case <-time.After(gateCallbackWait):
		h.t.Fatal("OnHydrationComplete never fired")
		return 0
	}
}

// requireQuiet asserts first paint has NOT been announced. Every release path
// under test is synchronous with the call that precedes this one, so an empty
// channel here is a decision already taken, not a race.
func (h *gateHarness) requireQuiet(reason string) {
	h.t.Helper()
	select {
	case revision := <-h.fired:
		h.t.Fatalf("OnHydrationComplete fired with revision %d: %s", revision, reason)
	default:
	}
}

// TestManager_FirstPaintGate_ReleasesWhenTheFloorReachesTheBurstEnd is §7's
// test 1: two lenses burst, each ending in its own terminal marker, and first
// paint waits for the delivery floor to reach the end position the whole cycle
// sits at or below — not for whichever lens marked itself done first.
func TestManager_FirstPaintGate_ReleasesWhenTheFloorReachesTheBurstEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h := newGateHarness(t, gateConfig{revision: 42, startSeq: 0, endSeq: 8})
	h.armCold(ctx)

	// Lens A's whole segment, marker included.
	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	require.Equal(t, transport.Ack, h.row(ctx, 2, "lensA"))
	require.Equal(t, transport.Ack, h.keyset(ctx, 3, "lensA"))
	require.Equal(t, transport.Ack, h.marker(ctx, 4, 42))
	h.requireQuiet("lens A's marker is not the end of the cycle — lens B has not been delivered at all")

	require.Equal(t, transport.Ack, h.row(ctx, 5, "lensB"))
	require.Equal(t, transport.Ack, h.row(ctx, 6, "lensB"))
	require.Equal(t, transport.Ack, h.keyset(ctx, 7, "lensB"))
	h.requireQuiet("the floor has not reached the burst's end position yet")

	require.Equal(t, transport.Ack, h.marker(ctx, 8, 42))
	assert.Equal(t, uint64(42), h.awaitFired(), "first paint must be announced with the response's revision")

	// A live delta behind the burst must not announce first paint a second time.
	require.Equal(t, transport.Ack, h.row(ctx, 9, "lensA"))
	h.requireQuiet("the gate is disarmed — it fires exactly once")

	cursor, ok, err := h.st.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, uint64(9), cursor, "every marker still advances the cursor like any other delta")
}

// TestManager_FirstPaintGate_PartialWorldCannotRelease is §7's test 2, the
// refuted marker-shaped gate's green criterion expressed positionally: the
// higher-revision lens's terminal marker arrives while an EARLIER sequence is
// still unresolved, and must not release a world that is missing a frame.
//
// It is the mutation vector for the two ways the mechanism could be wrong:
// releasing on the highest delivered sequence rather than the contiguous floor
// (the hole is invisible to a high-water mark), or comparing the floor to the
// end position the wrong way round (any advance would release).
func TestManager_FirstPaintGate_PartialWorldCannotRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	heldKey := "manifest.task.partialworldheldrow"
	gated := newGatedStore(openTestStore(t), heldKey)
	h := newGateHarness(t, gateConfig{revision: 42, endSeq: 6, store: gated})
	h.armCold(ctx)

	// One frame resolves, so the floor moves — a gate that released on any
	// advance would fire here.
	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	h.requireQuiet("an advanced floor short of the end position releases nothing")

	// Lens A's second row cannot apply and is asked for again: the floor is
	// pinned below it for as long as it is outstanding.
	require.Equal(t, transport.Nak, h.rowKeyed(ctx, 2, heldKey))

	// Lens B — the higher-revision lens — finishes its whole segment, marker
	// and all, while lens A's row 2 is still missing.
	require.Equal(t, transport.Ack, h.row(ctx, 3, "lensB"))
	require.Equal(t, transport.Ack, h.marker(ctx, 4, 999))
	h.requireQuiet("a marker from the lens holding the highest revision must not paint a world missing an earlier frame")

	require.Equal(t, transport.Ack, h.row(ctx, 5, "lensC"))
	require.Equal(t, transport.Ack, h.row(ctx, 6, "lensC"))
	h.requireQuiet("delivering the end position is not reaching it — sequence 2 is still unresolved")

	cursor, ok, err := h.st.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(1), cursor, "the floor must be pinned one below the outstanding sequence")

	// The redelivery lands and the world is whole.
	gated.open()
	require.Equal(t, transport.Ack, h.rowKeyed(ctx, 2, heldKey))
	assert.Equal(t, uint64(42), h.awaitFired(), "resolving the hole completes the world and releases first paint")
	h.requireQuiet("exactly one announcement")
}

// TestManager_FirstPaintGate_RehydrateArmsAfterTheBurstAndReleasesAtOnce is
// §7's test 3. On the live path the burst is often fully applied before the RPC
// response returns — the defect that made the refuted, edge-triggered gate race
// — and a LEVEL cannot be consumed before it is armed: the arm-time check reads
// the floor and releases immediately.
func TestManager_FirstPaintGate_RehydrateArmsAfterTheBurstAndReleasesAtOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h := newGateHarness(t, gateConfig{revision: 77, endSeq: 5})

	// The whole burst is delivered and applied before anything is armed.
	for seq := uint64(1); seq <= 5; seq++ {
		require.Equal(t, transport.Ack, h.row(ctx, seq, "lensA"))
	}
	h.requireQuiet("no gate is armed and no marker was delivered")

	h.armLive(ctx)
	assert.Equal(t, uint64(77), h.awaitFired(), "an already-applied burst must release the gate at arming, not hang")

	state := gateState(h.mgr)
	assert.False(t, state.armed, "the arm-time release must disarm the gate")
	assert.Nil(t, state.deadline, "the arm-time release must cancel the deadline it just started")

	require.Equal(t, transport.Ack, h.row(ctx, 6, "lensA"))
	h.requireQuiet("no double fire behind an arm-time release")
}

// TestManager_FirstPaintGate_SecondDeviceTrafficCannotRelease is §7's test 4.
// The SYNC subject is per-ACTOR, so a second device signed in as the same
// identity publishes onto this device's feed. Foreign messages below the end
// position only add to what must resolve first; foreign messages at or above it
// are irrelevant to the comparison; and a foreign terminal marker — the shape
// a revision-only gate would accept — releases nothing at all.
func TestManager_FirstPaintGate_SecondDeviceTrafficCannotRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	heldKey := "manifest.task.seconddeviceheldrow"
	gated := newGatedStore(openTestStore(t), heldKey)
	h := newGateHarness(t, gateConfig{revision: 42, endSeq: 6, store: gated})
	h.armCold(ctx)

	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	require.Equal(t, transport.Ack, h.row(ctx, 2, "lensA"))

	// The other device's completed cycle, interleaved below this cycle's end
	// position and carrying a revision far above this cycle's.
	require.Equal(t, transport.Ack, h.marker(ctx, 3, 5000))
	h.requireQuiet("another device's terminal marker must never release this device's armed gate")

	// This device's own frame goes outstanding.
	require.Equal(t, transport.Nak, h.rowKeyed(ctx, 4, heldKey))
	require.Equal(t, transport.Ack, h.row(ctx, 5, "lensB"))

	// Foreign traffic AT the end position, while an own frame is still missing.
	require.Equal(t, transport.Ack, h.row(ctx, 6, "foreignLens"))
	h.requireQuiet("foreign traffic at the end position cannot stand in for an unresolved own frame")

	gated.open()
	require.Equal(t, transport.Ack, h.rowKeyed(ctx, 4, heldKey))
	assert.Equal(t, uint64(42), h.awaitFired(), "the gate releases once this device's own sequences resolve")
	h.requireQuiet("exactly one announcement")
}

// TestManager_FirstPaintGate_EvictedSequencesDoNotWedge is §7's test 5.
// Retention (MaxMsgsPerSubject) can evict messages below the end position
// before this node ever attaches. A never-delivered sequence is never held, so
// the floor jumps it on the next resolved delivery and the gate still reaches
// its end position — the gate cannot be wedged open by a message that no longer
// exists.
func TestManager_FirstPaintGate_EvictedSequencesDoNotWedge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h := newGateHarness(t, gateConfig{revision: 42, endSeq: 10})
	h.armCold(ctx)

	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	require.Equal(t, transport.Ack, h.row(ctx, 2, "lensA"))
	h.requireQuiet("still short of the end position")

	// Sequences 3-8 were evicted and are never delivered.
	require.Equal(t, transport.Ack, h.row(ctx, 9, "lensB"))
	h.requireQuiet("the floor jumped the evicted span but has not reached the end position")

	// The end sequence is an ordinary row, so only the floor reaching it can
	// account for the release: a marker carrying the cycle's revision would let
	// the fallback rule take the credit for what the position mechanism is
	// being tested for.
	require.Equal(t, transport.Ack, h.row(ctx, 10, "lensB"))
	assert.Equal(t, uint64(42), h.awaitFired(), "a gap of never-delivered sequences must not wedge the gate")
}

// TestManager_FirstPaintGate_NakHoldsPaintUntilRedelivery is §7's test 6: a
// single Nak'd burst message holds the floor, and therefore first paint, until
// its redelivery resolves — paint follows APPLIED state, not delivered state.
func TestManager_FirstPaintGate_NakHoldsPaintUntilRedelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	heldKey := "manifest.task.nakholdspaintrow"
	gated := newGatedStore(openTestStore(t), heldKey)
	h := newGateHarness(t, gateConfig{revision: 42, endSeq: 3, store: gated})
	h.armCold(ctx)

	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	require.Equal(t, transport.Nak, h.rowKeyed(ctx, 2, heldKey))
	require.Equal(t, transport.Ack, h.row(ctx, 3, "lensA"))
	h.requireQuiet("a Nak'd burst message holds the floor below the end position")

	gated.open()
	require.Equal(t, transport.Ack, h.rowKeyed(ctx, 2, heldKey))
	assert.Equal(t, uint64(42), h.awaitFired(), "the redelivery resolves the hole and releases first paint")
}

// TestManager_FirstPaintGate_DeadlineFiresOnceAndAReleaseCancelsIt is §7's test
// 7. A first-paint gate is state with a lifetime, and the failure to design for
// is the gate that never opens: with nothing delivered at all, the deadline
// releases it anyway, loudly. A gate released by its own condition cancels that
// timer, so there is no second announcement waiting to happen.
func TestManager_FirstPaintGate_DeadlineFiresOnceAndAReleaseCancelsIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("nothing delivered releases on the deadline, with a Warn", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 5, deadline: 40 * time.Millisecond})
		h.armCold(ctx)

		assert.Equal(t, uint64(42), h.awaitFired(), "a gate that is never satisfied must still open")
		assert.True(t, h.logs.sawAtLeast(slog.LevelWarn, "no delivery progress"),
			"a release with nothing delivered must be reported, not silent")

		state := gateState(h.mgr)
		assert.False(t, state.armed)
		assert.Nil(t, state.deadline, "the expired timer must be dropped, not left to fire again")
	})

	t.Run("a release cancels the deadline", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 2, deadline: time.Minute})
		h.armCold(ctx)
		armed := gateState(h.mgr)
		require.True(t, armed.armed)
		require.NotNil(t, armed.deadline, "the deadline is armed unconditionally, before anything is delivered")

		require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
		require.Equal(t, transport.Ack, h.row(ctx, 2, "lensA"))
		assert.Equal(t, uint64(42), h.awaitFired())

		assert.Nil(t, gateState(h.mgr).deadline, "the released gate must hold no timer")
		assert.False(t, armed.deadline.Stop(), "the timer the release cancelled must already be stopped")
	})
}

// TestManager_FirstPaintGate_FallbackKeepsTheScalarRuleAndArmsTheDeadline is
// §7's test 8. A control plane that can name no end position (an older one, a
// nil seam, a failed read) answers SyncEndSeq == 0, and the gate falls back to
// the scalar rule — first marker at or above the cycle's revision — under the
// same deadline, which the scalar rule on its own has no equivalent of.
func TestManager_FirstPaintGate_FallbackKeepsTheScalarRuleAndArmsTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("the scalar revision rule is preserved", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 0})
		h.armCold(ctx)

		state := gateState(h.mgr)
		require.True(t, state.armed)
		require.Zero(t, state.endSeq, "an absent position must select the fallback explicitly")
		require.NotNil(t, state.deadline, "a gate no marker may ever satisfy must still be bounded")

		require.Equal(t, transport.Ack, h.marker(ctx, 1, 10))
		h.requireQuiet("a marker replayed from before this cycle must not release the gate")

		require.Equal(t, transport.Ack, h.marker(ctx, 2, 42))
		assert.Equal(t, uint64(42), h.awaitFired(), "the marker at the armed target releases the gate")

		// With no gate armed, any marker fires — a foreign device's completed
		// cycle re-marking an already-painted host. Deliberately unchanged.
		require.Equal(t, transport.Ack, h.marker(ctx, 3, 50))
		assert.Equal(t, uint64(50), h.awaitFired(), "the steady-state tail behaviour is untouched by this gate")
	})

	t.Run("a fallback gate no marker ever satisfies is still bounded", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 0, deadline: 40 * time.Millisecond})
		h.armCold(ctx)
		require.Equal(t, transport.Ack, h.marker(ctx, 1, 10))
		assert.Equal(t, uint64(42), h.awaitFired(), "the fallback's deadline must release a gate no marker satisfies")
	})

	t.Run("a floor advance never releases a fallback gate", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 0})
		h.armCold(ctx)
		for seq := uint64(1); seq <= 5; seq++ {
			require.Equal(t, transport.Ack, h.row(ctx, seq, "lensA"))
		}
		h.requireQuiet("with no end position to compare against, only a marker or the deadline may release")
	})
}

// TestManager_FirstPaintGate_ArmingReplacesTheArmedCycle is §7's test 9. A
// reconnect→gap→hydrate, or a Rehydrate racing a boot, leaves two cycles in
// flight; only the newest may fire, and the superseded one's timer must not
// outlive it.
func TestManager_FirstPaintGate_ArmingReplacesTheArmedCycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("the superseded cycle is dropped, timer and all", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 10, endSeq: 5})
		h.armCold(ctx)
		first := gateState(h.mgr)
		require.NotNil(t, first.deadline)

		// A newer cycle: the control plane now names a later end position and a
		// higher revision.
		h.cfg.revision, h.cfg.endSeq = 20, 8
		h.tr.reply = func(op string, _ controlwire.ControlRequest) (controlwire.ControlResponse, error) {
			switch op {
			case "register":
				return controlwire.ControlResponse{PersonalRegister: &controlwire.PersonalRegisterResult{Registered: true}}, nil
			case "hydrate":
				return controlwire.ControlResponse{PersonalHydrate: &controlwire.PersonalHydrateResult{
					Hydrated: true, Revision: 20, SyncEndSeq: 8,
				}}, nil
			default:
				return controlwire.ControlResponse{Error: "unexpected op " + op}, nil
			}
		}
		h.armCold(ctx)

		second := gateState(h.mgr)
		assert.Greater(t, second.generation, first.generation, "each cycle is stamped with its own generation")
		assert.Equal(t, uint64(8), second.endSeq)
		assert.False(t, first.deadline.Stop(), "the superseded cycle's timer must have been stopped by the replacement")

		for seq := uint64(1); seq <= 5; seq++ {
			require.Equal(t, transport.Ack, h.row(ctx, seq, "lensA"))
		}
		h.requireQuiet("the superseded cycle's end position must release nothing")

		for seq := uint64(6); seq <= 8; seq++ {
			require.Equal(t, transport.Ack, h.row(ctx, seq, "lensA"))
		}
		assert.Equal(t, uint64(20), h.awaitFired(), "only the newest cycle fires, and with its own revision")
	})

	t.Run("a floor advance racing a replacement never fires the superseded cycle", func(t *testing.T) {
		// What this proves: whichever of the two coarse orderings wins, the
		// outcome is one of the two legal ones — the old cycle released before
		// the replacement (leaving the new gate armed and intact), or the
		// replacement won and the old cycle stayed silent. Never a double
		// announcement, never the wrong revision, and never a superseded cycle
		// firing while the new gate has been disarmed under it.
		//
		// What makes that safe in the floor path is STRUCTURAL, not a
		// generation re-check: hydrateGateFloorAdvanced evaluates readiness,
		// disarms and decides to fire inside ONE gateMu critical section, so
		// there is no window for a replacement to land between the parts. The
		// generation check earns its keep on the deadline path, where the timer
		// callback carries the generation it was armed under and can arrive
		// arbitrarily late; that check is pinned in the stall-detector test.
		// This race is therefore a smoke test over the whole path, not a
		// barrier that would catch a two-step floor release on its own.
		const (
			oldRevision = uint64(10)
			newRevision = uint64(20)
		)
		st := openTestStore(t)
		for i := 0; i < 64; i++ {
			fired := make(chan uint64, 4)
			mgr, err := New(&fakeControlTransport{}, st, Config{
				IdentityID: "identityA", DeviceID: "deviceX", Logger: slog.New(&gateLogs{}),
				HydrateGateDeadline: time.Minute,
				OnHydrationComplete: func(revision uint64) { fired <- revision },
			})
			require.NoError(t, err)
			mgr.armHydrateGate(5, oldRevision, armCold)

			var wg stdsync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				if fire, revision := mgr.hydrateGateFloorAdvanced(5); fire {
					fired <- revision
				}
			}()
			go func() {
				defer wg.Done()
				mgr.armHydrateGate(100, newRevision, armCold)
			}()
			wg.Wait()
			after := gateState(mgr)
			mgr.disarmHydrateGate()

			close(fired)
			var announced []uint64
			for revision := range fired {
				announced = append(announced, revision)
			}
			require.LessOrEqual(t, len(announced), 1, "a cycle announces first paint at most once")
			if len(announced) == 1 {
				require.Equal(t, oldRevision, announced[0],
					"the replacement's end position is out of reach, so only the superseded cycle could have released")
				require.True(t, after.armed,
					"a superseded cycle that fired must have done so BEFORE the replacement, leaving the new gate armed")
				require.Equal(t, newRevision, after.revision)
				require.Equal(t, uint64(100), after.endSeq)
			}
		}
	})
}

// gateRunTransport answers control RPCs like fakeControlTransport and then
// parks its durable consumer until the context is cancelled — enough for Run to
// reach, and pass, the point where a cold-path gate's position is checked.
type gateRunTransport struct {
	fakeControlTransport
	started chan struct{}
	// fail, when closed, ends the consumer with consumerErr while the run
	// context is still live — a transient delivery failure rather than a
	// shutdown, which are the two Run exits the gate must treat differently.
	fail        chan struct{}
	consumerErr error
}

func (g *gateRunTransport) RunDurableConsumer(ctx context.Context, _ transport.ConsumerConfig, _ transport.Handler) error {
	close(g.started)
	select {
	case <-ctx.Done():
		return nil
	case <-g.fail:
		return g.consumerErr
	}
}

// gateRunHarness drives a Manager through the real Run — the only path on which
// a cold-path gate's end position is compared against the cursor the attach
// resumes from, and the only one that can retire a gate at shutdown.
type gateRunHarness struct {
	t     *testing.T
	mgr   *Manager
	tr    *gateRunTransport
	logs  *gateLogs
	fired chan uint64
	st    edgestore.Store
}

// newGateRunHarness seeds the store with cursor (zero leaves the node cold) and
// answers personal.hydrate from cfg. The syncgap answer is not-gapped with an
// operator's hydration request pending, so a warm cursor still takes the
// hydrate path — the combination that reaches a fresh burst behind a stale
// cursor without any gap check standing in the way.
func newGateRunHarness(t *testing.T, cursor uint64, cfg gateConfig) *gateRunHarness {
	t.Helper()
	st := openTestStore(t)
	if cursor > 0 {
		require.NoError(t, st.SetCursor(cursor))
	}
	deadline := cfg.deadline
	if deadline == 0 {
		deadline = time.Minute
	}
	h := &gateRunHarness{t: t, st: st, logs: &gateLogs{}, fired: make(chan uint64, 8)}
	h.tr = &gateRunTransport{started: make(chan struct{}), fail: make(chan struct{})}
	h.tr.reply = func(op string, _ controlwire.ControlRequest) (controlwire.ControlResponse, error) {
		switch op {
		case "register":
			return controlwire.ControlResponse{PersonalRegister: &controlwire.PersonalRegisterResult{Registered: true}}, nil
		case "hydrate":
			return controlwire.ControlResponse{PersonalHydrate: &controlwire.PersonalHydrateResult{
				Hydrated: true, Revision: cfg.revision, SyncStartSeq: cfg.startSeq, SyncEndSeq: cfg.endSeq,
			}}, nil
		case "syncgap":
			return controlwire.ControlResponse{PersonalSyncGap: &controlwire.PersonalSyncGapResult{
				Gapped: false, HydrationRequested: true,
			}}, nil
		default:
			return controlwire.ControlResponse{Error: "unexpected op " + op}, nil
		}
	}
	mgr, err := New(h.tr, st, Config{
		IdentityID: "identityA", DeviceID: "deviceX", Logger: slog.New(h.logs),
		HydrateGateDeadline: deadline,
		OnHydrationComplete: func(revision uint64) { h.fired <- revision },
	})
	require.NoError(t, err)
	h.mgr = mgr
	t.Cleanup(mgr.disarmHydrateGate)
	return h
}

// start runs the Manager until its durable consumer is attached — past the
// point where the cold-path gate's position is checked — and returns a stop
// func that cancels the run and requires a clean return.
func (h *gateRunHarness) start(ctx context.Context) func() {
	h.t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- h.mgr.Run(runCtx) }()
	select {
	case <-h.tr.started:
	case runErr := <-done:
		h.t.Fatalf("Run returned before its durable consumer attached: %v", runErr)
	case <-time.After(gateCallbackWait):
		h.t.Fatal("Run never reached its durable consumer")
	}
	return func() {
		cancel()
		select {
		case runErr := <-done:
			require.NoError(h.t, runErr)
		case <-time.After(gateCallbackWait):
			h.t.Fatal("Manager.Run did not return after context cancellation")
		}
	}
}

func (h *gateRunHarness) requireQuiet(reason string) {
	h.t.Helper()
	select {
	case revision := <-h.fired:
		h.t.Fatalf("OnHydrationComplete fired with revision %d: %s", revision, reason)
	default:
	}
}

func (h *gateRunHarness) awaitFired() uint64 {
	h.t.Helper()
	select {
	case revision := <-h.fired:
		return revision
	case <-time.After(gateCallbackWait):
		h.t.Fatal("OnHydrationComplete never fired")
		return 0
	}
}

// TestManager_FirstPaintGate_ColdArmAboveTheEndPositionIsAnAnomaly is §7's test
// 9a. On a cold path the floor derives from a PREVIOUS session's persisted
// cursor, and cursor <= syncStartSeq <= endSeq holds while the stream keeps
// growing — so a cursor above the fresh end position is not a release, it is
// evidence the sequence spaces diverged (stream recreation, DR restore, world
// wipe; reachable because personal.syncgap validates nothing and the
// operator's hydration-request bit forces a hydrate anyway). The gate must
// refuse the comparison rather than paint an empty store. The same inequality
// on the live path is exactly the case that releases.
func TestManager_FirstPaintGate_ColdArmAboveTheEndPositionIsAnAnomaly(t *testing.T) {
	t.Run("the cold path degrades to the fallback and paints nothing", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// The stale cursor: numerically huge, and not a gap, because
		// personal.syncgap tests cursor < firstSeq.
		h := newGateRunHarness(t, 500, gateConfig{revision: 42, startSeq: 3, endSeq: 5})
		stop := h.start(ctx)
		defer stop()

		state := gateState(h.mgr)
		require.True(t, state.armed, "the gate stays armed — the anomaly is not a release")
		assert.Zero(t, state.endSeq, "a diverged position space must degrade the gate to its marker rule")
		assert.True(t, h.logs.sawAtLeast(slog.LevelError, "sequence spaces have diverged"),
			"refusing the comparison must be reported at Error")
		h.requireQuiet("an empty store must not be painted")

		// The degraded gate is a fallback gate, not a dead one: this cycle's
		// marker still releases it.
		body, err := json.Marshal(deltaEnvelope{Op: "hydrationComplete", Revision: 42})
		require.NoError(t, err)
		require.Equal(t, transport.Ack, h.mgr.handle(ctx, transport.Delta{Sequence: 6, Body: body}))
		assert.Equal(t, uint64(42), h.awaitFired(), "the fallback's marker rule must still open the gate")
	})

	t.Run("a cursor exactly at the end position degrades too", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// The floor is seeded at the cursor and only ever released past what is
		// already persisted, so a gate whose end position IS the cursor can
		// never be satisfied by its own burst — every message of it sits at or
		// below that number.
		h := newGateRunHarness(t, 5, gateConfig{revision: 42, startSeq: 5, endSeq: 5})
		stop := h.start(ctx)
		defer stop()

		state := gateState(h.mgr)
		require.True(t, state.armed)
		assert.Zero(t, state.endSeq, "an end position at the resume cursor releases nothing — it must degrade, not wait out the window")
		h.requireQuiet("degrading is not releasing")
	})

	t.Run("an end position above the cursor keeps its position gate", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h := newGateRunHarness(t, 2, gateConfig{revision: 42, startSeq: 3, endSeq: 5})
		stop := h.start(ctx)
		defer stop()

		state := gateState(h.mgr)
		require.True(t, state.armed)
		assert.Equal(t, uint64(5), state.endSeq, "an ordinary cold attach must keep gating on the burst's end position")
		assert.False(t, h.logs.sawAtLeast(slog.LevelError, "sequence spaces have diverged"),
			"a healthy cold attach must not be reported as an anomaly")
	})

	t.Run("a cold re-attach never releases on the previous attach's floor", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// cmd/facet re-Runs the SAME Manager in a restart loop, and Run arms via
		// ensureFresh BEFORE it reseeds the floor — so from the second attach on,
		// the floor at cold-arm time still holds the previous attach's applied
		// position. Reading it as "the burst is already applied" would paint a
		// world this cycle has not delivered a byte of.
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 5})
		h.mgr.floor.reset(500)

		h.armCold(ctx)
		h.requireQuiet("a cold arm must never release against a floor from another attach")
		state := gateState(h.mgr)
		assert.True(t, state.armed, "the cold gate stays armed and waits for its own delivery")
		assert.Equal(t, uint64(5), state.endSeq)
	})

	t.Run("the same inequality on the live path releases at once", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 5})
		// A live consumer's floor is this session's own applied state.
		h.mgr.floor.reset(500)

		h.armLive(ctx)
		assert.Equal(t, uint64(42), h.awaitFired(),
			"on the live path a floor at or above the end position means the burst is already applied")
	})
}

// TestManager_FirstPaintGate_DeadlineIsAStallDetector is §7's test 9b. The
// window measures a STALL, not elapsed time: floor progress across a window
// re-arms it (a large world, or a browser tab whose timers and IndexedDB writes
// are background-throttled, can legitimately exceed any fixed total bound),
// only a full window with zero progress releases, and a delivery that carries
// the floor past the end position releases outright whatever the timer is
// doing.
func TestManager_FirstPaintGate_DeadlineIsAStallDetector(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("progress re-arms the window, a window without it releases", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 100})
		h.armCold(ctx)
		generation := gateState(h.mgr).generation

		// A superseded cycle's expiry does nothing.
		h.mgr.hydrateGateDeadlineExpired(generation - 1)
		require.True(t, gateState(h.mgr).armed)
		h.requireQuiet("an expiry stamped with another cycle's generation must be ignored")

		// A window that saw the floor move keeps waiting.
		require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
		require.True(t, gateState(h.mgr).progressed, "a floor advance is progress")
		h.mgr.hydrateGateDeadlineExpired(generation)
		state := gateState(h.mgr)
		assert.True(t, state.armed, "a window that saw progress must re-arm, not release")
		assert.False(t, state.progressed, "the re-armed window starts measuring again")
		assert.NotNil(t, state.deadline)
		h.requireQuiet("the burst is slow, not stalled")

		// The next window sees nothing at all.
		h.mgr.hydrateGateDeadlineExpired(generation)
		assert.Equal(t, uint64(42), h.awaitFired(), "a full window with zero progress must release the gate")
		assert.True(t, h.logs.sawAtLeast(slog.LevelWarn, "no delivery progress"))
		assert.False(t, gateState(h.mgr).armed)

		h.mgr.hydrateGateDeadlineExpired(generation)
		h.requireQuiet("a disarmed gate cannot be released twice")
	})

	t.Run("a delivery past the end position releases outright", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 10, deadline: time.Minute})
		h.armCold(ctx)
		// One delivery above the end position — a foreign device's live delta —
		// carries the floor past it, and the gate releases without waiting for
		// any window.
		require.Equal(t, transport.Ack, h.row(ctx, 11, "foreignLens"))
		assert.Equal(t, uint64(42), h.awaitFired(),
			"unrelated traffic cannot extend the gate without also satisfying it")
	})
}

// TestManager_FirstPaintGate_FallbackDeadlineIsATotalBound pins the one place
// the stall detector must NOT re-arm. A fallback gate's release condition is a
// marker, so a delivered delta is no evidence it is approaching — and the SYNC
// subject is per-ACTOR, so ordinary live traffic (this device's own writes, a
// second device's) is always available to reset a window. Counting it as
// progress would leave the degraded gate open forever, swallowing every later
// marker below its target for the life of the process: the gate that never
// opens, on exactly the path the position-absent fail-soft selects.
func TestManager_FirstPaintGate_FallbackDeadlineIsATotalBound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	t.Run("delivery never re-arms a fallback window", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 0})
		h.armCold(ctx)
		generation := gateState(h.mgr).generation

		for seq := uint64(1); seq <= 5; seq++ {
			require.Equal(t, transport.Ack, h.row(ctx, seq, "lensA"))
			require.False(t, gateState(h.mgr).progressed,
				"a floor advance says nothing about a gate waiting for a marker")
		}

		h.mgr.hydrateGateDeadlineExpired(generation)
		assert.Equal(t, uint64(42), h.awaitFired(),
			"the first window is the fallback gate's bound, however much traffic crossed it")
		assert.True(t, h.logs.sawAtLeast(slog.LevelWarn, "no delivery progress"))
	})

	t.Run("a busy subject still opens the gate within one window", func(t *testing.T) {
		h := newGateHarness(t, gateConfig{revision: 42, endSeq: 0, deadline: 40 * time.Millisecond})
		h.armCold(ctx)
		// Traffic that keeps arriving while the window runs. The real timer is
		// what releases; the deliveries are here to be ignored by it.
		for seq := uint64(1); seq <= 20; seq++ {
			require.Equal(t, transport.Ack, h.row(ctx, seq, "lensA"))
		}
		assert.Equal(t, uint64(42), h.awaitFired(), "a fallback gate must open even on a busy subject")
	})
}

// TestManager_FirstPaintGate_ProgressIsResolutionNotTheFloorMoving is the other
// half of the same distinction. A hydrate whose START position fail-softed to
// zero while its END position read succeeded leaves the consumer replaying the
// whole retained subject from its first sequence — every one of those messages
// below an already-higher persisted cursor, so the floor advances by nothing at
// all for as long as the replay takes. That is delivery, not a stall, and the
// deadline must not paint over it.
func TestManager_FirstPaintGate_ProgressIsResolutionNotTheFloorMoving(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	h := newGateHarness(t, gateConfig{revision: 42, endSeq: 200})
	// A warm cursor well above where the replay begins.
	h.mgr.floor.reset(100)
	h.armCold(ctx)
	generation := gateState(h.mgr).generation

	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	require.Equal(t, transport.Ack, h.row(ctx, 2, "lensA"))
	require.True(t, gateState(h.mgr).progressed,
		"a message this attach had never passed is progress even when the persisted floor cannot move")

	h.mgr.hydrateGateDeadlineExpired(generation)
	assert.True(t, gateState(h.mgr).armed, "a replaying burst must re-arm the window, not be painted over")
	h.requireQuiet("the burst is being delivered, however little the floor moves")

	// A duplicate of a sequence already resolved is not progress: it is exactly
	// the traffic a bound must not be resettable by.
	require.Equal(t, transport.Ack, h.mgr.handle(ctx, transport.Delta{
		Sequence: 2, Body: h.body(deltaEnvelope{Op: "upsert", Key: "manifest.task.duplicatereplayrow", Revision: 2, Data: json.RawMessage(`{"row":true}`)}),
	}))
	assert.False(t, gateState(h.mgr).progressed, "a redelivery of an already-resolved sequence resolves nothing new")

	h.mgr.hydrateGateDeadlineExpired(generation)
	assert.Equal(t, uint64(42), h.awaitFired(), "a window that resolved nothing new is a stall")
}

// TestManager_FirstPaintGate_ShutdownRetiresTheGateButAFailureKeepsIt covers
// §3.5's two Run boundaries, which pull in opposite directions. Shutdown must
// drop the gate and its timer, or a stray expiry fires OnHydrationComplete into
// a torn-down engine. A Run that returns for any OTHER reason must keep it: the
// deadline is first paint's only liveness backstop, and the browser host gets
// ONE Run per page with no restart loop, so a consumer failure mid-burst that
// also cancelled the gate would hang first paint for the life of the page.
func TestManager_FirstPaintGate_ShutdownRetiresTheGateButAFailureKeepsIt(t *testing.T) {
	t.Run("a cancelled context retires the gate", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h := newGateRunHarness(t, 0, gateConfig{revision: 42, startSeq: 3, endSeq: 5})
		stop := h.start(ctx)
		require.True(t, gateState(h.mgr).armed, "the attach armed a gate")

		stop()

		state := gateState(h.mgr)
		assert.False(t, state.armed, "shutdown must retire the gate")
		assert.Nil(t, state.deadline, "shutdown must leave no timer to fire into a torn-down host")
		h.requireQuiet("a retired gate announces nothing")
	})

	t.Run("a transient consumer failure keeps the gate armed", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// The deadline is armed at attach, before this test observes the gate,
		// and its only job here is to fire AFTER the consumer failure below.
		// It is sized so a loaded runner cannot let it elapse between the
		// attach and the armed assertion (a 40 ms deadline did, on CI), while
		// staying well inside awaitFired's budget.
		h := newGateRunHarness(t, 0, gateConfig{revision: 42, startSeq: 3, endSeq: 5, deadline: 2 * time.Second})
		h.tr.consumerErr = errors.New("edge/sync: simulated consumer failure")

		runCtx, runCancel := context.WithCancel(ctx)
		defer runCancel()
		done := make(chan error, 1)
		go func() { done <- h.mgr.Run(runCtx) }()
		select {
		case <-h.tr.started:
		case <-time.After(gateCallbackWait):
			t.Fatal("Run never reached its durable consumer")
		}
		require.True(t, gateState(h.mgr).armed)

		// The consumer fails while the burst is still arriving. The run context
		// is never cancelled — this is a transient failure, not a shutdown.
		close(h.tr.fail)
		select {
		case runErr := <-done:
			require.Error(t, runErr, "the consumer's terminal error must reach the caller")
		case <-time.After(gateCallbackWait):
			t.Fatal("Manager.Run did not return")
		}

		assert.Equal(t, uint64(42), h.awaitFired(),
			"a host with no restart loop must still get its first paint from the deadline")
	})
}

// TestManager_FirstPaintGate_ACursorWriteFailureCannotPaint pins the rule that
// the gate is consulted after the cursor write and on no other branch. A
// delivery that reaches the end position but whose position could not be
// recorded has not been passed — it is going to be asked for again — so
// announcing first paint on it would paint a world the store cannot resume.
func TestManager_FirstPaintGate_ACursorWriteFailureCannotPaint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	gated := newGatedStore(openTestStore(t), "")
	h := newGateHarness(t, gateConfig{revision: 42, endSeq: 3, store: gated})
	h.armCold(ctx)

	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	require.Equal(t, transport.Ack, h.row(ctx, 2, "lensA"))
	h.requireQuiet("short of the end position")

	gated.setCursorFails(true)
	require.Equal(t, transport.Nak, h.row(ctx, 3, "lensA"),
		"an applied delta whose position could not be recorded must be asked for again")
	h.requireQuiet("a floor the store never accepted must not paint")

	gated.setCursorFails(false)
	require.Equal(t, transport.Ack, h.row(ctx, 3, "lensA"))
	assert.Equal(t, uint64(42), h.awaitFired(), "the retry records the position and releases the gate")
}

// TestManager_FirstPaintGate_ArmTimeCheckReadsThePersistedFloor pins which
// number the arm-time check compares. `highest` is the highest sequence
// DELIVERED; the floor that has reached the store is what paint may follow. A
// burst with a hole in it has a highest at its end position and a floor far
// below, and the difference is a world missing a frame.
func TestManager_FirstPaintGate_ArmTimeCheckReadsThePersistedFloor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	heldKey := "manifest.task.armtimeheldrow"
	gated := newGatedStore(openTestStore(t), heldKey)
	h := newGateHarness(t, gateConfig{revision: 42, endSeq: 5, store: gated})

	require.Equal(t, transport.Ack, h.row(ctx, 1, "lensA"))
	require.Equal(t, transport.Nak, h.rowKeyed(ctx, 2, heldKey))
	for seq := uint64(3); seq <= 5; seq++ {
		require.Equal(t, transport.Ack, h.row(ctx, seq, "lensA"))
	}
	// Delivered up to the end position, applied only up to sequence 1.
	cursor, ok, err := h.st.Cursor()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint64(1), cursor)

	h.armLive(ctx)
	h.requireQuiet("the arm-time check must read the floor that reached the store, not the highest sequence delivered")
	require.True(t, gateState(h.mgr).armed)

	gated.open()
	require.Equal(t, transport.Ack, h.rowKeyed(ctx, 2, heldKey))
	assert.Equal(t, uint64(42), h.awaitFired(), "resolving the hole releases the gate")
}
