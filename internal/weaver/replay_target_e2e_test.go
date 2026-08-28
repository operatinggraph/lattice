package weaver_test

import (
	"context"
	"net"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/weaver"
)

// onboardingPlaybook is the dispatchable gaps entry these tests switch a target
// TO — a triggerLoom of the "onboarding" pattern anchored on the row's
// entityKey. Its predecessor in each test is a `surface` entry, whose whole
// contract is to raise a Health issue and dispatch nothing.
func onboardingPlaybook() map[string]any {
	return map[string]any{
		"missing_onboarding": map[string]any{
			"action": "triggerLoom", "pattern": "onboarding", "subject": "row.entityKey",
		},
	}
}

// surfacePlaybook is the gaps entry that ACKS a violating row: `surface` raises
// a Health issue while the column is true and dispatches nothing, so the row is
// acknowledged with its violation unremediated and nothing owes it a
// redelivery. That is the shape of every row this design's replay verb exists
// to reach — a decline the standing Nak loop cannot reach because the row was
// already acked.
func surfacePlaybook() map[string]any {
	return map[string]any{
		"missing_onboarding": map[string]any{"action": "surface"},
	}
}

// violatingRow is the §10.2 row body these tests project: violating, with the
// onboarding gap column open.
func violatingRow(entityID string) map[string]any {
	return map[string]any{
		"entityKey":          "vtx.leaseApp." + entityID,
		"violating":          true,
		"missing_onboarding": true,
	}
}

// TestWeaverE2E_ReplayTargetReachesAnAckedDecline is T6: the verb's motivating
// case, end to end.
//
// A violating row whose playbook entry is `surface` is ACKED with its violation
// standing. Fixing the playbook afterwards changes nothing for that row: the
// registry update rebuilds no durable (the lane-1 spec fingerprint is
// name-derived), the row does not re-project, and an acked message is owed no
// redelivery by anything — so the row stays violating and undispatched
// indefinitely. That is exactly the population the standing decline loop cannot
// reach, and the assertion below that NO op arrives after the fix is what pins
// it as unreachable rather than merely slow.
//
// ReplayTarget is the repair: it recreates the target's lane-1 durable under a
// STABLE name, so DeliverLastPerSubject re-delivers the target's current row set
// through the unchanged ladder and the still-violating row dispatches.
//
// The test also pins the two things a replay must NOT do: it must refuse an
// unregistered target loudly rather than reporting a successful zero-row replay,
// and a replayed row whose episode is already in flight must take the anti-storm
// Ack rather than dispatching a duplicate.
func TestWeaverE2E_ReplayTargetReachesAnAckedDecline(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "fixtureReplayAcked"
	targetVtx := mustNanoID(t)
	declineTarget(t, ctx, conn, targetVtx, targetID, surfacePlaybook())

	engine := newEngine(conn, "e2e-replay-acked-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)
	createdBefore := laneConsumerInfo(t, ctx, conn, durable).Created

	entityID := mustNanoID(t)
	putRow(t, ctx, conn, targetID, entityID, violatingRow(entityID))

	// The surface decline: acked, pending set empty, nothing dispatched.
	waitConsumerState(t, ctx, conn, durable, 20*time.Second,
		"an empty pending set after the surface Ack", func(i *jetstream.ConsumerInfo) bool {
			return i.NumAckPending == 0 && i.Delivered.Consumer >= 1
		})
	requireNoOp(t, ops, time.Second)

	// The fix arrives. Nothing re-projects the row and nothing rebuilds the
	// durable, so the acked row is not reconsidered — the fix reaches every
	// FUTURE row of this target and none of the acked ones.
	declineTarget(t, ctx, conn, targetVtx, targetID, onboardingPlaybook())
	requireNoOp(t, ops, 3*time.Second)
	require.Equal(t, createdBefore, laneConsumerInfo(t, ctx, conn, durable).Created,
		"a playbook fix must not rebuild the lane-1 durable on its own — if it does, the acked-decline "+
			"population heals itself and this verb's motivating case no longer exists")

	// The refusal that must never be reported as a success: a target this engine
	// does not know cannot be replayed, and saying otherwise would leave the
	// operator's diagnostic standing over a fact nothing re-derived.
	_, err = engine.ReplayTarget(ctx, "noSuchTarget")
	require.ErrorContains(t, err, "not registered")

	// The replay itself.
	queued, err := engine.ReplayTarget(ctx, targetID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, queued, 1, "the recreated durable must have the target's row queued to deliver")

	op := nextOp(t, ops, 30*time.Second)
	require.Equal(t, "StartLoomPattern", op.OperationType)

	after := laneConsumerInfo(t, ctx, conn, durable)
	require.True(t, after.Created.After(createdBefore),
		"the replay must delete-and-recreate the durable: a DeliverPolicy is fixed at first create, so an "+
			"update could never return the consumer to the head of every subject")
	require.Equal(t, durable, after.Name,
		"the recreated durable must keep its name: it is per-target and keys the per-consumer health sink, "+
			"so a nonce would churn those keys and need a prune pass of its own")

	// A second replay re-delivers the same row while its episode is in flight.
	// The mark is live, so the anti-storm arm Acks it — a replay must never
	// duplicate an episode it re-presents.
	waitMark(t, ctx, conn, targetID+"."+entityID+".missing_onboarding")
	_, err = engine.ReplayTarget(ctx, targetID)
	require.NoError(t, err)
	requireNoOp(t, ops, 5*time.Second)
}

// TestWeaverE2E_ReplayTargetRefusesWhatTheLadderPermanentlyDeclines pins the
// verb's refusal split. The verb's whole effect is what the lane-1 ladder does
// with the rows it re-presents, so it must refuse exactly the shapes that ladder
// PERMANENTLY declines: accepting one recreates a durable, re-delivers every
// row, reports success — and changes nothing an operator can observe.
//
// One vector per permanent decline, each asserted to name its OWN reason (they
// are three different problems with three different fixes, so one shared wording
// would misdirect), plus a control that replays.
func TestWeaverE2E_ReplayTargetRefusesWhatTheLadderPermanentlyDeclines(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "fixtureReplayRefuse"
	targetVtx := mustNanoID(t)
	declineTarget(t, ctx, conn, targetVtx, targetID, onboardingPlaybook())

	engine := newEngine(conn, "e2e-replay-refuse-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)

	// Control: a registered, enabled, managed target replays.
	_, err = engine.ReplayTarget(ctx, targetID)
	require.NoError(t, err, "the control vector must replay — a refusal test that refuses everything proves nothing")

	// Permanent decline 1: an unregistered target. handleRow returns at the
	// registry miss for every row of one, so the replay would deliver a set
	// nothing evaluates.
	_, err = engine.ReplayTarget(ctx, "ghostTarget")
	require.ErrorContains(t, err, "not registered")

	// Permanent decline 2: a DISABLED target. Its pump is paused and every row
	// it delivers Acks at the dispatch-skip without remediating, and only an
	// operator Enable lifts either — so the refusal names enable as the order to
	// do things in, rather than reporting a replay that could not run.
	require.NoError(t, engine.Disable(ctx, targetID))
	_, err = engine.ReplayTarget(ctx, targetID)
	require.ErrorContains(t, err, "is disabled")
	require.ErrorContains(t, err, "enable it first")
	require.NoError(t, engine.Enable(ctx, targetID))
	_, err = engine.ReplayTarget(ctx, targetID)
	require.NoError(t, err, "enable must restore the verb — the disabled refusal is a state, not a verdict on the target")

	// A revoked target refuses too, and it refuses as DISABLED: Revoke is a
	// strict superset of Disable, so the state an operator has to lift first is
	// the disable, and `enable` is what re-adds the durable Revoke removed. The
	// third permanent decline — a registered, enabled target with no managed
	// lane-1 consumer — is not constructible from the operator verbs for exactly
	// that reason, and is covered where it IS constructible
	// (TestReplayTarget_RefusesAnUnmanagedConsumer).
	require.NoError(t, engine.Revoke(ctx, targetID))
	waitConsumerGone(t, ctx, conn, durable)
	_, err = engine.ReplayTarget(ctx, targetID)
	require.ErrorContains(t, err, "is disabled")
}

// TestWeaverE2E_EnableAfterFreezeRedeliversNakdRows is T7: `Enable` stays plain
// Resume, and that is now enough for the whole declined population.
//
// A config-error decline holds its message in the consumer's pending set on the
// long floor. Disabling the target pauses the pump before the iterator, so the
// freeze delivers nothing and acks nothing — the pending slot is held for the
// whole freeze. Enable resumes the same durable, and JetStream redelivers every
// Nak'd-pending row on its own timestamps, so remediation resumes for every row
// still violating with NO durable rebuild.
//
// The no-rebuild half is the load-bearing one: it is why Enable does not need to
// become a replay, and why the replay verb is reserved for the rows an Ack
// stranded.
func TestWeaverE2E_EnableAfterFreezeRedeliversNakdRows(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "fixtureFreezeResume"
	targetVtx := mustNanoID(t)
	declineTarget(t, ctx, conn, targetVtx, targetID, nil)

	const longFloor = 6 * time.Second
	engine := newEngine(conn, "e2e-freeze-resume-"+mustNanoID(t), func(c *weaver.Config) {
		c.LongRedeliveryDelay = longFloor
	})
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)
	created := laneConsumerInfo(t, ctx, conn, durable).Created

	entityID := mustNanoID(t)
	putRow(t, ctx, conn, targetID, entityID, violatingRow(entityID))

	waitConsumerState(t, ctx, conn, durable, 30*time.Second,
		"a Nak'd-pending declined row", func(i *jetstream.ConsumerInfo) bool {
			return i.NumAckPending == 1
		})

	// The freeze. The row keeps its pending slot for the whole of it.
	require.NoError(t, engine.Disable(ctx, targetID))

	// The fix lands DURING the freeze, and the wait that proves the freeze held
	// also carries past several redelivery floors — so an op arriving after the
	// Enable below can only have come from the resumed pump, never from a
	// redelivery the freeze failed to hold.
	declineTarget(t, ctx, conn, targetVtx, targetID, onboardingPlaybook())
	requireNoOp(t, ops, 3*longFloor)
	require.Equal(t, 1, int(laneConsumerInfo(t, ctx, conn, durable).NumAckPending),
		"the declined row must still hold its pending slot through the freeze")

	require.NoError(t, engine.Enable(ctx, targetID))

	op := nextOp(t, ops, 3*longFloor)
	require.Equal(t, "StartLoomPattern", op.OperationType)
	require.Equal(t, created, laneConsumerInfo(t, ctx, conn, durable).Created,
		"Enable must stay plain Resume: the Nak'd-pending row redelivers on its own timestamps, so a "+
			"durable rebuild here would be unearned cost on every enable")
}

// TestWeaverE2E_ServerRestartKeepsANakdRowOwed is T10, and it pins the substrate
// property the replay verb's SCOPE rests on.
//
// The design this verb ships under (weaver-decline-retry-substrate-native-design
// §3.3, ledger row V16) reasoned that a NATS restart strands a quiet target's
// Nak'd-pending set: the redelivery timer `o.ptmr` is not persisted, and
// `setLeader → checkPending` bails when it is nil, so a consumer with no traffic
// of its own would never re-arm it. Run against the pinned nats-server 2.14.0
// that is NOT what happens, and this test is the pin: `setLeader` calls
// `readStoredState → applyState`, which arms the timer whenever restored pending
// is non-empty (`consumer.go`'s "Setup tracking timer if we have restored
// pending"), and `checkPending` then re-derives each pending message's remaining
// delay from the `p.Timestamp` the delayed Nak persisted. The row redelivers at
// its ORIGINAL floor, across the restart, with no traffic and no operator action.
//
// This test reddening means that property regressed — a quiet target's declined
// rows would then be owed by nothing until some row under its prefix projects,
// and `ReplayTarget` (asserted below to recover the row regardless) becomes the
// only repair, to be run per affected target.
func TestWeaverE2E_ServerRestartKeepsANakdRowOwed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// The server is started from amended fixture options rather than the plain
	// helper because this test restarts it: the replacement must bind the SAME
	// port (so the client reconnects) and open the SAME JetStream store (so the
	// durable and its pending state come back).
	opts := natsfixture.Options(t)
	srv := natsfixture.StartServerWith(t, opts)
	port := srv.Addr().(*net.TCPAddr).Port
	nc := natsfixture.Connect(t, srv.ClientURL())
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "fixtureRestartOwed"
	targetVtx := mustNanoID(t)
	declineTarget(t, ctx, conn, targetVtx, targetID, nil)

	// The floor is long enough that the restart completes well inside it, so a
	// redelivery observed afterwards is the RESTORED timer firing on schedule and
	// not one that was already due when the server came back.
	const longFloor = 25 * time.Second
	engine := newEngine(conn, "e2e-restart-owed-"+mustNanoID(t), func(c *weaver.Config) {
		c.LongRedeliveryDelay = longFloor
	})
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)

	entityID := mustNanoID(t)
	putRow(t, ctx, conn, targetID, entityID, violatingRow(entityID))

	declined := waitConsumerState(t, ctx, conn, durable, 30*time.Second,
		"a Nak'd-pending declined row", func(i *jetstream.ConsumerInfo) bool {
			return i.NumAckPending == 1
		})
	nakedAt := time.Now()
	deliveredBefore := declined.Delivered.Consumer

	// The restart, under a target with no other traffic: nothing else projects
	// under its prefix, so no fresh delivery can re-arm the timer on the
	// consumer's behalf.
	srv.Shutdown()
	srv.WaitForShutdown()
	restartOpts := natsfixture.Options(t)
	restartOpts.Port = port
	restartOpts.StoreDir = opts.StoreDir
	natsfixture.StartServerWith(t, restartOpts)
	waitReconnected(t, nc, 30*time.Second)

	// The redelivery arrives on its own, at the floor it was Nak'd onto.
	redelivered := waitConsumerState(t, ctx, conn, durable, 3*longFloor,
		"a redelivery of the Nak'd row after the restart", func(i *jetstream.ConsumerInfo) bool {
			return i.Delivered.Consumer > deliveredBefore
		})
	require.Greater(t, time.Since(nakedAt), longFloor/2,
		"the redelivery arrived %v after the Nak, far inside the %v floor — the restart did not restore the "+
			"delay, it discarded it", time.Since(nakedAt), longFloor)
	require.Equal(t, 1, int(redelivered.NumAckPending),
		"the row must still be owed after the restart: one pending slot, held continuously")

	// And the operator repair works regardless of whether the substrate owed the
	// row: a replay re-presents the target's current rows through the same ladder,
	// so fixing the playbook and replaying dispatches without waiting on any
	// server-side timer at all.
	declineTarget(t, ctx, conn, targetVtx, targetID, onboardingPlaybook())
	_, err = engine.ReplayTarget(ctx, targetID)
	require.NoError(t, err)
	op := nextOp(t, ops, 30*time.Second)
	require.Equal(t, "StartLoomPattern", op.OperationType)
}

// waitReconnected polls until nc has re-established its connection to a
// restarted server, failing with the connection's own status rather than a bare
// timeout. Polling the status is the deterministic wait for a reconnect whose
// timing belongs to nats.go's backoff, not to this test.
func waitReconnected(t *testing.T, nc *nats.Conn, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if nc.IsConnected() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("client never reconnected to the restarted server within %v; status = %v", timeout, nc.Status())
}
