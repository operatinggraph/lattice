package weaver_test

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/weaver"
)

// The decline-retry e2e suite (design weaver-decline-retry-substrate-native-design.md
// §3.2/§3.3/§6). A config-error decline is Nak'd on a long redelivery floor, so
// the row stands as an owed, pending message that re-evaluates against CURRENT
// config until the config is fixed. These tests drive that through a live
// embedded server, because the two claims it rests on — the pending set and
// per-subject compaction — are the substrate's behaviour, not the handler's.

// declineFloors configures a lane-1 engine with redelivery floors short enough
// to run a decline loop inside a test. Both floors are configured: the substrate
// clamps the long floor UP to the effective short one, so lowering only the long
// floor would leave the loop pinned at the 5s short default.
func declineFloors(short, long time.Duration) func(*weaver.Config) {
	return func(c *weaver.Config) {
		c.RedeliveryDelay = short
		c.LongRedeliveryDelay = long
	}
}

// laneConsumerInfo reads the live JetStream state of one target's lane-1 durable.
func laneConsumerInfo(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID string) *jetstream.ConsumerInfo {
	t.Helper()
	cons, err := conn.JetStream().Consumer(ctx, "KV_"+weaverTargetsBucket, "weaver-target-"+targetID)
	require.NoError(t, err)
	info, err := cons.Info(ctx)
	require.NoError(t, err)
	return info
}

// waitAckPending polls one target's lane-1 durable until its un-acked in-flight
// count reaches want.
func waitAckPending(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID string, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		got = laneConsumerInfo(t, ctx, conn, targetID).NumAckPending
		if got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("lane-1 durable for %q: num_ack_pending = %d, want %d — %s", targetID, got, want, why)
}

// installDeclineTarget installs a target whose single gap resolves its Loom
// subject through the row column named by subjectRef. A row that does not carry
// that column takes §3.2 row 10's TemplateDataError dead-end: the plan cannot be
// built, and one of its fix paths is a template edit that produces no new
// delivery, so the row declines on the LONG floor and stands pending.
//
// Row 10, not row 8's orphaned column: an orphaned column Acks (its fix may
// never come, so a Nak would park it forever), which is a different test.
//
// It returns the meta-vertex id, so a later spec re-install can correct the
// template on the SAME vertex.
func installDeclineTarget(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID, patternRef, subjectRef string) string {
	t.Helper()
	vtx := mustNanoID(t)
	installWeaverTarget(t, ctx, conn, vtx, map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_known": map[string]any{
				"action": "triggerLoom", "pattern": patternRef, "subject": subjectRef,
			},
		},
	})
	return vtx
}

// TestWeaverE2E_DeclinedRowStaysPendingUntilTheRowIsSuperseded pins the two
// substrate facts the standing decline loop rests on (design §2 V1, V3, V7).
//
//   - A config-error decline is Nak'd, so the message stays in the consumer's
//     PENDING set — the substrate's only "owed" tracking — and re-delivers on the
//     long floor for as long as the fault holds. An Ack here would retire the
//     row's only claim on the handler's attention, and nothing else enumerates it.
//   - Overwriting the row's KV key frees that slot eagerly: weaver-targets keeps
//     history 1, so the new revision compacts the old message out of the backing
//     stream and JetStream terminates its pending delivery rather than waiting
//     out the floor. The fresh revision then delivers and dispatches on its own.
//
// The history-1 pin those two facts rest on is asserted where it can actually
// red against Lattice's own provisioning —
// internal/bootstrap/weaver_targets_bucket_test.go's TestWeaverTargetsBucket_Provisioned,
// which reads the bucket the seeder created and carries the revive-the-fallback
// failure message. Asserting it here as well would pin nothing: this test's own
// provision() creates the bucket a few lines earlier, so the value read back
// would be the jetstream client's default, not the platform's decision.
func TestWeaverE2E_DeclinedRowStaysPendingUntilTheRowIsSuperseded(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "declinePending"
	installDeclineTarget(t, ctx, conn, targetID, "onboarding", "row.applicant")

	engine := newEngine(conn, "e2e-decline-"+mustNanoID(t), declineFloors(200*time.Millisecond, 500*time.Millisecond))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)

	// §6: lane 1 declares its own in-flight window, because a declined row now
	// holds a pending slot for as long as the config stays broken.
	require.Equal(t, 2000, laneConsumerInfo(t, ctx, conn, targetID).Config.MaxAckPending,
		"lane-1 must declare its in-flight window explicitly rather than inherit the server default")

	entityID := mustNanoID(t)
	applicant := "vtx.identity." + mustNanoID(t)
	// The row opens the gap but carries no `applicant` column, so the gap's
	// subject template resolves null and no episode can be built.
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":     "vtx.leaseApp." + entityID,
		"violating":     true,
		"missing_known": true,
	})

	requireNoOp(t, ops, 2*time.Second)
	waitAckPending(t, ctx, conn, targetID, 1,
		"a config-error decline must stand as an owed, pending message, not be acked away")

	// The lens re-projects the row WITH the column the template reads. The new
	// revision compacts the declined one out, so the pending slot is released
	// without waiting out the floor, and the fresh row dispatches.
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":     "vtx.leaseApp." + entityID,
		"violating":     true,
		"missing_known": true,
		"applicant":     applicant,
	})

	op := nextOp(t, ops, 20*time.Second)
	require.Equal(t, "StartLoomPattern", op.OperationType)
	require.Equal(t, applicant, op.Payload["subjectKey"])
	waitAckPending(t, ctx, conn, targetID, 0,
		"the superseding revision must free the declined row's pending slot and then be acked itself")
}

// TestWeaverE2E_ConfigFixIsTakenUpByTheDeclineLoop is §3.2's fix-uptake claim,
// pinned: a row declined for a config-class error re-evaluates against the
// CURRENT registry on its next redelivery, so correcting the gap's template
// dispatches the already-declined row automatically — with no durable rebuild
// and no re-projection of the row.
//
// Both negatives are asserted, not assumed. The lane-1 durable's creation
// timestamp must be unchanged (a rebuild is a delete-then-create, which mints a
// new one), and the row's KV revision must be unchanged (a re-projection is a
// new revision). Without those, a test that merely saw the op fire could not
// tell the redelivery loop from either of the mechanisms this design removed.
func TestWeaverE2E_ConfigFixIsTakenUpByTheDeclineLoop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "declineFixUp"
	targetVtx := installDeclineTarget(t, ctx, conn, targetID, "onboarding", "row.applicant")

	const longFloor = time.Second
	engine := newEngine(conn, "e2e-fixup-"+mustNanoID(t), declineFloors(200*time.Millisecond, longFloor))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)

	entityID := mustNanoID(t)
	// No `applicant` column: the gap's subject template resolves null, so the
	// plan cannot be built and the row declines on the long floor.
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":     "vtx.leaseApp." + entityID,
		"violating":     true,
		"missing_known": true,
	})

	requireNoOp(t, ops, 2*time.Second)
	waitAckPending(t, ctx, conn, targetID, 1, "the declined row must stand pending while the template is unbuildable")

	before := laneConsumerInfo(t, ctx, conn, targetID)
	rowKey := targetID + "." + entityID
	rowBefore, err := conn.KVGet(ctx, weaverTargetsBucket, rowKey)
	require.NoError(t, err)

	// The package is re-authored: the SAME target vertex now reads a column the
	// row actually carries. Nothing touches the row.
	fixedAt := time.Now()
	installWeaverTarget(t, ctx, conn, targetVtx, map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_known": map[string]any{
				"action": "triggerLoom", "pattern": "onboarding", "subject": "row.entityKey",
			},
		},
	})

	op := nextOp(t, ops, 20*time.Second)
	elapsed := time.Since(fixedAt)
	require.Equal(t, "StartLoomPattern", op.OperationType)
	require.Equal(t, "vtx.leaseApp."+entityID, op.Payload["subjectKey"])
	require.Less(t, elapsed, 10*longFloor,
		"the fix must be taken up on the decline loop's own cadence (one long floor), not eventually")

	after := laneConsumerInfo(t, ctx, conn, targetID)
	require.Equal(t, before.Created, after.Created,
		"the fix must be taken up with NO durable rebuild — a rebuild is a delete-then-create, "+
			"which mints a fresh creation timestamp")

	rowAfter, err := conn.KVGet(ctx, weaverTargetsBucket, rowKey)
	require.NoError(t, err)
	require.Equal(t, rowBefore.Revision, rowAfter.Revision,
		"the fix must be taken up with NO re-projection — the redelivered message is the row that "+
			"was already declined, re-evaluated against the current registry")
}
