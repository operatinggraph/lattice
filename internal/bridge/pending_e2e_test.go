package bridge_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bridge"
)

// TestBridge_PendingOutcome_PostsDispatchOpNotReply drives the async path: the
// FakeAsyncCheck returns a Pending Dispatch, so the bridge posts the dispatchOp
// (RecordWidgetDispatch) carrying the vendorRef — and posts NO replyOp and writes
// NO .outcome (the Loom token stays parked). The bridge ACKs (a Pending is a
// successful Execute). The dispatch op lands under the deterministic dispatch
// requestId. The instanceKey is a NON-service token (invariant a).
func TestBridge_PendingOutcome_PostsDispatchOpNotReply(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, fp := newHarness(t, ctx)
	async := bridge.NewFakeAsyncCheck(1)
	startBridgeWithAdapter(t, ctx, conn, fixtureAsyncName, async, nil)

	instanceKey := nonServiceHandle(t)
	dispatchReqID := bridge.DeriveDispatchRequestID(instanceKey)
	replyReqID := bridge.DeriveReplyRequestID(instanceKey)

	publishAsyncExternalEvent(t, ctx, conn, fixtureAsyncName, instanceKey, fixtureReplyOp, fixtureDispatchOp, nil)

	// The dispatch op commits exactly one pending-marker mutation.
	require.Eventually(t, func() bool { return fp.dispatchMutations() == 1 },
		15*time.Second, 60*time.Millisecond, "a Pending outcome must commit exactly one dispatch-marker mutation")

	// It is the dispatchOp, posted under the deterministic dispatch requestId,
	// carrying the adapter's vendorRef.
	gotOp, seen := fp.sawOp(dispatchReqID)
	require.GreaterOrEqual(t, seen, 1, "the dispatch op must be posted under the deterministic dispatch requestId")
	require.Equal(t, fixtureDispatchOp, gotOp, "the Pending path posts the dispatchOp, not the replyOp")
	require.NotEmpty(t, fp.sawVendorRef(dispatchReqID), "the dispatch op payload must carry the vendorRef")

	// The submit happened exactly once.
	require.Equal(t, 1, async.SideEffects(instanceKey), "exactly one submit for the Pending call")

	// And crucially: NO replyOp / NO .outcome. The terminal reply requestId was
	// never posted, and no result mutation landed.
	require.Never(t, func() bool {
		_, replySeen := fp.sawOp(replyReqID)
		return replySeen > 0 || fp.mutations() > 0
	}, 2*time.Second, 100*time.Millisecond,
		"a Pending outcome must post NO replyOp and write NO .outcome (the token stays parked)")
}

// TestBridge_PendingRedelivery_NoSecondDispatchOp publishes the SAME Pending
// event twice. The deterministic dispatch requestId collapses the second
// dispatch op on the Contract #4 tracker, and the idempotent adapter returns the
// same vendor Ref with no new side-effect — so exactly one submit and exactly one
// dispatch-marker mutation, no matter the redelivery. Run with the skip-probe ON
// and OFF (the skip probes the REPLY tracker, which a Pending call never writes,
// so it must NOT short-circuit the Pending path).
func TestBridge_PendingRedelivery_NoSecondDispatchOp(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, skip := range []bool{true, false} {
		skip := skip
		name := "skipProbeOn"
		if !skip {
			name = "skipProbeOff"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			conn, fp := newHarness(t, ctx)
			async := bridge.NewFakeAsyncCheck(1)
			startBridgeWithAdapter(t, ctx, conn, fixtureAsyncName, async, func(c *bridge.Config) { c.SkipOnRedelivery = &skip })

			instanceKey := nonServiceHandle(t)

			// First delivery: one submit, one dispatch-marker mutation.
			publishAsyncExternalEvent(t, ctx, conn, fixtureAsyncName, instanceKey, fixtureReplyOp, fixtureDispatchOp, nil)
			require.Eventually(t, func() bool { return fp.dispatchMutations() == 1 },
				15*time.Second, 60*time.Millisecond, "first delivery must commit one dispatch-marker mutation")

			// Redelivery: publish the SAME event again.
			publishAsyncExternalEvent(t, ctx, conn, fixtureAsyncName, instanceKey, fixtureReplyOp, fixtureDispatchOp, nil)

			require.Never(t, func() bool { return async.SideEffects(instanceKey) > 1 || fp.dispatchMutations() > 1 },
				3*time.Second, 100*time.Millisecond,
				"redelivery must not produce a second submit or a second dispatch-marker mutation")

			require.Equal(t, 1, async.SideEffects(instanceKey), "exactly one submit under redelivery")
			require.Equal(t, 1, fp.dispatchMutations(), "exactly one dispatch-marker mutation under redelivery")
		})
	}
}

// TestBridge_PendingWithNoDispatchOp_AckAndHealth: a Pending outcome for an
// externalTask that carries NO dispatchOp is a config error (a sync-only task
// wired to an async adapter). The bridge ACKs it (no redelivery loop) AND raises a
// Health issue, posts NO dispatch op and NO replyOp — mirroring the
// unregistered-adapter handling (errConfig, never a hot Nak loop).
func TestBridge_PendingWithNoDispatchOp_AckAndHealth(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, fp := newHarness(t, ctx)
	async := bridge.NewFakeAsyncCheck(1)
	startBridgeWithAdapter(t, ctx, conn, fixtureAsyncName, async, nil)

	instanceKey := nonServiceHandle(t)
	// Use the plain publisher (no dispatchOp field) — the externalTask is sync-only.
	publishExternalEvent(t, ctx, conn, fixtureAsyncName, instanceKey, fixtureReplyOp, nil)

	require.True(t, waitHealthIssue(t, ctx, conn, "BridgeDispatchOpMissing"),
		"a Pending outcome with no dispatchOp must raise a Health issue (errConfig, never a silent skip)")

	// Nothing is posted: no dispatch-marker mutation, no replyOp/.outcome.
	require.Never(t, func() bool { return fp.dispatchMutations() > 0 || fp.mutations() > 0 },
		3*time.Second, 100*time.Millisecond, "a misconfigured Pending dispatches nothing")
}
