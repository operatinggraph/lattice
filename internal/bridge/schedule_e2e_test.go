package bridge_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bridge"
)

// The poll/timeout lane proof. A bridge engine on embedded NATS consumes a Pending
// external event, posts the dispatchOp (the fixture Processor writes the .dispatch
// marker — the lens/Weaver read-model, not read by the bridge), and arms the poll +
// timeout @at schedules on core-schedules, carrying the routing in the schedule
// payload. The fired consumer then drives resolution: a poll resolves → the replyOp
// posts (the .outcome lands); a poll that stays Pending re-arms; a call that never
// resolves times out → a failed .outcome lands. The bridge stays type-agnostic — it
// judges resolution by the reply op-tracker and routes from the payload, never
// synthesizing the typed claim key; the fixture's claim type (service) lives only in
// the package ops.

// serviceHandle mints a bare NanoID for the claim the package's dispatchOp/replyOp
// reconstruct as vtx.service.<handle> (the bridge itself never reconstructs it).
func serviceHandle(t *testing.T) string { return mustNanoID(t) }

// TestBridgeSchedule_PollResolves_PostsReplyOp drives the full poll→resolve cycle:
// a Pending event arms the poll schedule (at a near-now instant, so it fires
// immediately); the poll resolves (FakeAsyncCheck(0)); the bridge posts the
// replyOp under the deterministic reply requestId; the .outcome lands and the
// reply carries the adapter's terminal status. Exactly one reply.
func TestBridgeSchedule_PollResolves_PostsReplyOp(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	conn, fp := newServiceHarness(t, ctx)
	async := bridge.NewFakeAsyncCheck(0) // the first poll resolves
	// PollInterval short so the poll fires promptly; CallDeadline far enough out
	// that the timeout never races the resolve in this test.
	startBridgeWithAdapter(t, ctx, conn, fixtureAsyncName, async, func(c *bridge.Config) {
		c.CoreSchedulesStream = schedulesStream
		c.PollInterval = 1 * time.Second
		c.CallDeadline = 1 * time.Hour
	})

	handle := serviceHandle(t)
	replyReqID := bridge.DeriveReplyRequestID(handle)
	dispatchReqID := bridge.DeriveDispatchRequestID(handle)

	publishAsyncExternalEvent(t, ctx, conn, fixtureAsyncName, handle, fixtureReplyOp, fixtureDispatchOp, nil)

	// The pending marker lands first (the dispatch op committed).
	require.Eventually(t, func() bool { return fp.dispatchMutations() == 1 },
		15*time.Second, 60*time.Millisecond, "the Pending outcome must commit the dispatch marker")
	require.Equal(t, 1, async.SideEffects(handle), "exactly one submit for the pending call")
	if op, _ := fp.sawOp(dispatchReqID); op != fixtureDispatchOp {
		t.Fatalf("dispatch op = %q, want %q", op, fixtureDispatchOp)
	}

	// The armed poll fires; the poll resolves; the bridge posts the replyOp → the
	// .outcome lands (the fixture Processor counts a result mutation).
	require.Eventually(t, func() bool { return fp.mutations() == 1 },
		25*time.Second, 100*time.Millisecond, "the resolved poll must post the replyOp (the .outcome must land)")

	// It is the replyOp, under the deterministic reply requestId, carrying the
	// adapter's terminal status (completed).
	gotOp, seen := fp.sawOp(replyReqID)
	require.GreaterOrEqual(t, seen, 1, "the replyOp must be posted under the deterministic reply requestId")
	require.Equal(t, fixtureReplyOp, gotOp, "the poll-resolved path posts the replyOp")
	require.Equal(t, string(bridge.OutcomeCompleted), fp.sawStatus(replyReqID),
		"the replyOp must carry the adapter's terminal status verbatim")
	_, gotRef := fp.sawReply(replyReqID)
	require.Equal(t, handle, gotRef, "payload.externalRef must echo the bare handle")

	// Exactly one reply — no duplicate from a redelivered firing.
	require.Never(t, func() bool { return fp.mutations() > 1 },
		1500*time.Millisecond, 100*time.Millisecond, "the resolved poll must post exactly one replyOp")
}

// TestBridgeSchedule_StillPending_ReArms: a poll that returns Pending re-arms the
// poll schedule (the self-rescheduling @at chain), so the vendor is polled more
// than once. FakeAsyncCheck(2) stays Pending for the first two polls; we assert
// the adapter is polled at least twice (the chain advanced) before it resolves on
// the third.
func TestBridgeSchedule_StillPending_ReArms(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, fp := newServiceHarness(t, ctx)
	async := bridge.NewFakeAsyncCheck(2) // Pending, Pending, then Resolved
	startBridgeWithAdapter(t, ctx, conn, fixtureAsyncName, async, func(c *bridge.Config) {
		c.CoreSchedulesStream = schedulesStream
		c.PollInterval = 1 * time.Second
		c.CallDeadline = 1 * time.Hour
	})

	handle := serviceHandle(t)
	replyReqID := bridge.DeriveReplyRequestID(handle)

	publishAsyncExternalEvent(t, ctx, conn, fixtureAsyncName, handle, fixtureReplyOp, fixtureDispatchOp, nil)

	require.Eventually(t, func() bool { return fp.dispatchMutations() == 1 },
		15*time.Second, 60*time.Millisecond, "the Pending outcome must commit the dispatch marker")

	// The poll chain advances: at least two polls happen (each Pending poll
	// re-arms), and the call eventually resolves on the third → the .outcome lands.
	require.Eventually(t, func() bool { return async.Polls(handle) >= 2 },
		30*time.Second, 100*time.Millisecond, "a Pending poll must re-arm the chain (the vendor is polled again)")
	require.Eventually(t, func() bool { return fp.mutations() == 1 },
		30*time.Second, 100*time.Millisecond, "the chain must resolve and post the replyOp once it clears")
	if op, _ := fp.sawOp(replyReqID); op != fixtureReplyOp {
		t.Fatalf("resolved reply op = %q, want %q", op, fixtureReplyOp)
	}
	require.Equal(t, 1, async.SideEffects(handle), "still exactly one submit across the whole poll chain")
}

// TestBridgeSchedule_Timeout_PostsFailedOutcome: a call that stays Pending forever
// trips the timeout schedule, which posts a terminal failed replyOp → a failed
// .outcome lands, exactly one reply. CallDeadline is short and PollInterval is
// long, so the timeout fires before any poll resolves.
func TestBridgeSchedule_Timeout_PostsFailedOutcome(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	conn, fp := newServiceHarness(t, ctx)
	// Never resolves: every poll returns Pending. A long PollInterval keeps the
	// poll from racing the short-deadline timeout.
	async := bridge.NewFakeAsyncCheck(1 << 30)
	startBridgeWithAdapter(t, ctx, conn, fixtureAsyncName, async, func(c *bridge.Config) {
		c.CoreSchedulesStream = schedulesStream
		c.PollInterval = 1 * time.Hour
		c.CallDeadline = 2 * time.Second
	})

	handle := serviceHandle(t)
	replyReqID := bridge.DeriveReplyRequestID(handle)

	publishAsyncExternalEvent(t, ctx, conn, fixtureAsyncName, handle, fixtureReplyOp, fixtureDispatchOp, nil)

	require.Eventually(t, func() bool { return fp.dispatchMutations() == 1 },
		15*time.Second, 60*time.Millisecond, "the Pending outcome must commit the dispatch marker")

	// The timeout fires → a failed replyOp posts → the failed .outcome lands.
	require.Eventually(t, func() bool { return fp.mutations() == 1 },
		20*time.Second, 100*time.Millisecond, "the timeout must post a terminal replyOp (the failed .outcome must land)")
	gotOp, _ := fp.sawOp(replyReqID)
	require.Equal(t, fixtureReplyOp, gotOp, "the timeout posts the replyOp")
	require.Equal(t, string(bridge.OutcomeFailed), fp.sawStatus(replyReqID),
		"a timed-out call posts status=failed (the reused failed verdict)")

	// Exactly one reply — the timeout never double-posts, and no poll ever
	// resolved.
	require.Never(t, func() bool { return fp.mutations() > 1 },
		2*time.Second, 100*time.Millisecond, "a timed-out call posts exactly one terminal reply")
}
