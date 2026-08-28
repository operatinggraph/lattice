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

// laneConsumerInfo reads the live lane-1 durable's server-side state.
func laneConsumerInfo(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) *jetstream.ConsumerInfo {
	t.Helper()
	cons, err := conn.JetStream().Consumer(ctx, "KV_"+weaverTargetsBucket, durable)
	require.NoError(t, err)
	info, err := cons.Info(ctx)
	require.NoError(t, err)
	return info
}

// waitConsumerState polls the lane-1 durable until cond holds, failing with the
// last observed state rather than a bare timeout.
func waitConsumerState(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string,
	timeout time.Duration, what string, cond func(*jetstream.ConsumerInfo) bool) *jetstream.ConsumerInfo {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last *jetstream.ConsumerInfo
	for time.Now().Before(deadline) {
		last = laneConsumerInfo(t, ctx, conn, durable)
		if cond(last) {
			return last
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("durable %q never reached %s; last state: numAckPending=%d numRedelivered=%d deliveredStream=%d",
		durable, what, last.NumAckPending, last.NumRedelivered, last.Delivered.Stream)
	return nil
}

// declineTarget installs a weaverTarget whose playbook does NOT name gapColumn,
// so a violating row carrying that column takes the GapWithoutPlaybook decline.
func declineTarget(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexID, targetID string, gaps map[string]any) {
	t.Helper()
	spec := map[string]any{"targetId": targetID, "lensRef": mustNanoID(t)}
	if gaps != nil {
		spec["gaps"] = gaps
	} else {
		// A playbook that names a DIFFERENT column: the target is valid, and the
		// row's own column still has no entry.
		spec["gaps"] = map[string]any{
			"missing_other": map[string]any{"action": "surface"},
		}
	}
	installWeaverTarget(t, ctx, conn, vertexID, spec)
}

// TestWeaverE2E_DeclinedRowStaysPendingAndIsSupersededByItsOverwrite is T4: the
// substrate keystone the whole decline loop rests on, pinned end to end against
// a real server.
//
//   - A config-error decline is Nak'd, and a delayed Nak keeps the message in the
//     consumer's pending set — the slot is held continuously, which is what makes
//     the row owed rather than dropped.
//   - Overwriting the row's KV key removes the pending revision from the stream,
//     and the server Terms it EAGERLY: the slot is freed rather than accumulating
//     a stale revision beside the fresh one.
//   - The fresh revision delivers normally and, once the playbook covers the gap,
//     dispatches — with no rebuild of the durable.
//
// The eager Term holds only while `weaver-targets` keeps KV history 1 (per-subject
// compaction on every overwrite). That pin is asserted here, Weaver-owned: at
// history >= 2 an overwrite no longer compacts, a Nak'd stale revision keeps
// redelivering beside the fresh one, and this design's premise fails.
func TestWeaverE2E_DeclinedRowStaysPendingAndIsSupersededByItsOverwrite(t *testing.T) {
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

	kv, err := conn.JetStream().KeyValue(ctx, weaverTargetsBucket)
	require.NoError(t, err)
	status, err := kv.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), status.History(),
		"weaver-targets must keep KV history 1: the decline loop's correctness rests on an overwrite "+
			"compacting the previous revision away and eagerly Term'ing its pending delivery. At history >= 2 "+
			"a Nak'd stale revision keeps redelivering beside the fresh one — this assertion reddening is the "+
			"trigger to revive the shelved row-sweep fallback (weaver-sweep-declared-work-enumeration-design.md)")

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "fixtureDeclinePending"
	targetVtx := mustNanoID(t)
	declineTarget(t, ctx, conn, targetVtx, targetID, nil)

	// The long floor stays at its 5m production default: the row must still be
	// pending when the overwrite arrives, not redelivered out from under it.
	engine := newEngine(conn, "e2e-decline-pending-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)

	entityID := mustNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	row := map[string]any{
		"entityKey":          entityKey,
		"violating":          true,
		"missing_onboarding": true,
	}
	putRow(t, ctx, conn, targetID, entityID, row)

	declined := waitConsumerState(t, ctx, conn, durable, 20*time.Second,
		"a Nak'd-pending declined row", func(i *jetstream.ConsumerInfo) bool {
			return i.NumAckPending == 1
		})
	requireNoOp(t, ops, time.Second)
	firstDelivered := declined.Delivered.Stream

	// The overwrite: a new revision of the SAME key, still declining. The old
	// pending revision is compacted out of the stream and Term'd eagerly, so the
	// pending set holds the fresh revision and nothing else.
	putRow(t, ctx, conn, targetID, entityID, row)
	superseded := waitConsumerState(t, ctx, conn, durable, 20*time.Second,
		"delivery of the overwriting revision", func(i *jetstream.ConsumerInfo) bool {
			return i.Delivered.Stream > firstDelivered && i.NumAckPending > 0
		})
	require.Equal(t, 1, superseded.NumAckPending,
		"the overwritten revision must be Term'd when per-subject compaction removes it — a pending set "+
			"of 2 means a stale revision is still owed beside the fresh one")

	// Once the playbook covers the gap, the fresh revision dispatches through the
	// unchanged ladder and the slot is released.
	declineTarget(t, ctx, conn, targetVtx, targetID, map[string]any{
		"missing_onboarding": map[string]any{
			"action": "triggerLoom", "pattern": "onboarding", "subject": "row.entityKey",
		},
	})
	putRow(t, ctx, conn, targetID, entityID, row)

	op := nextOp(t, ops, 20*time.Second)
	require.Equal(t, "StartLoomPattern", op.OperationType)
	waitConsumerState(t, ctx, conn, durable, 20*time.Second,
		"an empty pending set after the dispatch", func(i *jetstream.ConsumerInfo) bool {
			return i.NumAckPending == 0
		})
}

// TestWeaverE2E_DeclinedRowTakesTheFixWithinOneFloor is T5: the claim that makes
// the Nak loop sufficient on its own. A row declined for a config error is
// re-evaluated against the CURRENT playbook on every redelivery, so adding the
// missing gaps entry is picked up automatically within one long floor — with no
// durable rebuild and no re-projection of the row.
//
// The floor is shortened for the test but stays well clear of the transient
// (5 s) one, and the elapsed-time assertion is what proves the row rode the LONG
// floor: a row Nak'd on the transient floor would have dispatched at ~5 s.
func TestWeaverE2E_DeclinedRowTakesTheFixWithinOneFloor(t *testing.T) {
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

	targetID := "fixtureDecUptake"
	targetVtx := mustNanoID(t)
	declineTarget(t, ctx, conn, targetVtx, targetID, nil)

	const longFloor = 15 * time.Second
	engine := newEngine(conn, "e2e-decline-uptake-"+mustNanoID(t), func(c *weaver.Config) {
		c.LongRedeliveryDelay = longFloor
	})
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)
	created := laneConsumerInfo(t, ctx, conn, durable).Created

	entityID := mustNanoID(t)
	row := map[string]any{
		"entityKey":          "vtx.leaseApp." + entityID,
		"violating":          true,
		"missing_onboarding": true,
	}
	putRow(t, ctx, conn, targetID, entityID, row)
	projectedAt := time.Now()

	waitConsumerState(t, ctx, conn, durable, 20*time.Second,
		"a Nak'd-pending declined row", func(i *jetstream.ConsumerInfo) bool {
			return i.NumAckPending == 1
		})
	requireNoOp(t, ops, time.Second)
	rowKey := targetID + "." + entityID
	declinedRev, err := conn.KVGet(ctx, weaverTargetsBucket, rowKey)
	require.NoError(t, err)

	// The fix: the package adds the gaps entry. Nothing re-projects the row, and
	// nothing rebuilds the durable.
	declineTarget(t, ctx, conn, targetVtx, targetID, map[string]any{
		"missing_onboarding": map[string]any{
			"action": "triggerLoom", "pattern": "onboarding", "subject": "row.entityKey",
		},
	})

	op := nextOp(t, ops, 2*longFloor)
	elapsed := time.Since(projectedAt)
	require.Equal(t, "StartLoomPattern", op.OperationType)
	require.Greater(t, elapsed, longFloor-6*time.Second,
		"the dispatch arrived %v after the row was projected — too early for the %v long floor, so the "+
			"decline was Nak'd on the transient floor (or not Nak'd at all)", elapsed, longFloor)

	after, err := conn.KVGet(ctx, weaverTargetsBucket, rowKey)
	require.NoError(t, err)
	require.Equal(t, declinedRev.Revision, after.Revision,
		"the fix must be taken up with NO re-projection: the row's revision moved, so the redelivery is "+
			"not what carried the fix")
	require.Equal(t, created, laneConsumerInfo(t, ctx, conn, durable).Created,
		"the fix must be taken up with NO rebuild: the lane-1 durable was delete-and-recreated")
}
