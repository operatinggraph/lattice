package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// sweepHarness is an Engine wired to an embedded NATS server with its registry
// seeded directly, so sweeper passes can be driven synchronously against
// constructed weaver-state marks and weaver-targets rows (no tickers, no
// CDC consumers).
type sweepHarness struct {
	engine *Engine
	conn   *substrate.Conn
	ops    *nats.Subscription
}

func newSweepHarness(t *testing.T, ctx context.Context, opts ...func(*Config)) *sweepHarness {
	t.Helper()
	srvOpts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(srvOpts)
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats connect: %v", err)
	}
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("substrate wrap: %v", err)
	}
	js := conn.JetStream()
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "weaver-state", LimitMarkerTTL: time.Second}); err != nil {
		t.Fatalf("create weaver-state: %v", err)
	}
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "weaver-targets"}); err != nil {
		t.Fatalf("create weaver-targets: %v", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-operations", Subjects: []string{"ops.>"},
	}); err != nil {
		t.Fatalf("create ops stream: %v", err)
	}
	ops, err := nc.SubscribeSync("ops.system")
	if err != nil {
		t.Fatalf("subscribe ops: %v", err)
	}
	t.Cleanup(func() { _ = ops.Unsubscribe() })

	cfg := Config{
		ActorKey: "vtx.identity.WeaverServiceActor1abc",
		Instance: "sweep-" + testNanoID(t),
		Logger:   discardLogger(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	engine := NewEngine(conn, cfg)
	return &sweepHarness{engine: engine, conn: conn, ops: ops}
}

func (h *sweepHarness) seedTarget(target *Target) {
	h.engine.source.mu.Lock()
	h.engine.source.targets[target.TargetID] = target
	h.engine.source.mu.Unlock()
}

func (h *sweepHarness) putRow(t *testing.T, ctx context.Context, targetID, entityID string, row map[string]any) {
	t.Helper()
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	if _, err := h.conn.KVPut(ctx, "weaver-targets", targetID+"."+entityID, body); err != nil {
		t.Fatalf("put row: %v", err)
	}
}

// putMark writes a constructed §10.3 mark value directly (no TTL — the shape a
// lease-less mark has when its writer died before arming the lease, or a
// manually-aged episode) and returns its revision.
func (h *sweepHarness) putMark(t *testing.T, ctx context.Context, key string, rec mark) uint64 {
	t.Helper()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal mark: %v", err)
	}
	rev, err := h.conn.KVCreate(ctx, "weaver-state", key, body)
	if err != nil {
		t.Fatalf("create mark %q: %v", key, err)
	}
	return rev
}

func (h *sweepHarness) pass(ctx context.Context) { h.engine.sweep.pass(ctx) }

// agePastWarmup rewinds the sweeper's start anchor so the orphan legs'
// warm-up window reads as elapsed.
func (h *sweepHarness) agePastWarmup() {
	h.engine.sweep.startedAt = time.Now().Add(-2 * h.engine.sweep.warmup)
}

func (h *sweepHarness) markExists(t *testing.T, ctx context.Context, key string) bool {
	t.Helper()
	_, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("mark read %q: %v", key, err)
	}
	return err == nil
}

func (h *sweepHarness) nextOp(t *testing.T) map[string]any {
	t.Helper()
	msg, err := h.ops.NextMsg(5 * time.Second)
	if err != nil {
		t.Fatalf("expected an op on ops.system: %v", err)
	}
	var op map[string]any
	if err := json.Unmarshal(msg.Data, &op); err != nil {
		t.Fatalf("unmarshal op: %v", err)
	}
	return op
}

func (h *sweepHarness) requireNoOp(t *testing.T) {
	t.Helper()
	if msg, err := h.ops.NextMsg(500 * time.Millisecond); err == nil {
		t.Fatalf("expected no op on ops.system, got: %s", string(msg.Data))
	}
}

func pastLease() string   { return substrate.FormatTimestamp(time.Now().Add(-time.Minute)) }
func futureLease() string { return substrate.FormatTimestamp(time.Now().Add(time.Hour)) }

func fixtureMark(targetID, entityID, col, action, lease string) mark {
	return mark{
		TargetID:  targetID,
		EntityKey: "vtx.leaseApp." + entityID,
		Gap:       col,
		Action:    action,
		// ClaimedAt is aged well past the default MarkLease (30m): a real mark
		// stamps ClaimedAt and LeaseExpiresAt = ClaimedAt + lease together, so an
		// expired-lease mark always has elapsed-since-ClaimedAt > the lease. The
		// userTask reclaim-backoff guard keys off that gap (first reclaim fires at
		// lease-expiry); a too-recent fixture ClaimedAt would falsely read as
		// "dispatched moments ago" and suppress the very reclaim under test.
		ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
		LeaseExpiresAt: lease,
		HeldBy:         "dead-instance",
	}
}

// TestSweep_LevelClear proves the sweep leg of §10.3 level-reconciled clearing
// (F6's prompt half and F7's row-tombstone variant): a mark whose column is no
// longer true — or whose row is gone — is deleted promptly with NO lease wait
// and no dispatch, while an unparseable row never clears a mark (unreadable
// evidence).
func TestSweep_LevelClear(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureClear"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})

	// (1) Column flipped false: cleared despite a live lease.
	closedEntity := testNanoID(t)
	closedKey := markKey(targetID, closedEntity, "missing_x")
	h.putMark(t, ctx, closedKey, fixtureMark(targetID, closedEntity, "missing_x", "directOp", futureLease()))
	h.putRow(t, ctx, targetID, closedEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + closedEntity, "violating": false, "missing_x": false,
	})

	// (2) Row gone entirely (entity tombstoned): cleared.
	goneEntity := testNanoID(t)
	goneKey := markKey(targetID, goneEntity, "missing_x")
	h.putMark(t, ctx, goneKey, fixtureMark(targetID, goneEntity, "missing_x", "directOp", futureLease()))

	// (3) Column absent from the current row (the Lens re-projected without
	// it): a mark may only stand for a currently-true column — cleared.
	absentEntity := testNanoID(t)
	absentKey := markKey(targetID, absentEntity, "missing_x")
	h.putMark(t, ctx, absentKey, fixtureMark(targetID, absentEntity, "missing_x", "directOp", futureLease()))
	h.putRow(t, ctx, targetID, absentEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + absentEntity, "violating": false,
	})

	// (4) Row unparseable: the mark must survive (never delete on unreadable
	// evidence).
	badRowEntity := testNanoID(t)
	badRowKey := markKey(targetID, badRowEntity, "missing_x")
	h.putMark(t, ctx, badRowKey, fixtureMark(targetID, badRowEntity, "missing_x", "directOp", futureLease()))
	if _, err := h.conn.KVPut(ctx, "weaver-targets", targetID+"."+badRowEntity, []byte("{not json")); err != nil {
		t.Fatalf("put bad row: %v", err)
	}

	h.pass(ctx)

	if h.markExists(t, ctx, closedKey) {
		t.Fatalf("closed-gap mark must be cleared by the sweep")
	}
	if h.markExists(t, ctx, goneKey) {
		t.Fatalf("row-gone mark must be cleared by the sweep")
	}
	if h.markExists(t, ctx, absentKey) {
		t.Fatalf("a mark at a column absent from the current row must be cleared")
	}
	if !h.markExists(t, ctx, badRowKey) {
		t.Fatalf("a mark must survive an unparseable row")
	}
	h.requireNoOp(t)
}

// TestSweep_ReclaimExpired proves the lease-expiry reclaim (F5's lost publish,
// F6's coalesced close→reopen shadow, the mid-flight-kill recovery): an
// expired mark at a still-true column is replaced IN PLACE and re-dispatched
// as a FRESH episode — new mark revision, new requestId, fresh lease and
// re-armed per-key TTL, this instance as holder — and the sweepReclaims
// counter records it.
func TestSweep_ReclaimExpired(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureReclaim"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	oldRev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	op := h.nextOp(t)
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("the reclaim must leave the mark standing, got %v", err)
	}
	if entry.Revision == oldRev {
		t.Fatalf("the reclaimed mark must carry a fresh episode revision (old revision %d)", oldRev)
	}
	// The in-place replace re-arms the per-key TTL: the new entry carries the
	// wire Nats-TTL header at markTTLBackstopFactor × MarkLease.
	stream, err := h.conn.JetStream().Stream(ctx, "KV_weaver-state")
	if err != nil {
		t.Fatalf("open weaver-state stream: %v", err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, "$KV.weaver-state."+key)
	if err != nil {
		t.Fatalf("read raw reclaimed mark message: %v", err)
	}
	wantTTL := (markTTLBackstopFactor * h.engine.cfg.MarkLease).String()
	if got := raw.Header.Get("Nats-TTL"); got != wantTTL {
		t.Fatalf("reclaimed mark Nats-TTL header = %q, want %q (the replace must re-arm the TTL)", got, wantTTL)
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal reclaimed mark: %v", err)
	}
	if rec.HeldBy != h.engine.cfg.Instance {
		t.Fatalf("reclaimed mark heldBy = %q, want this instance %q", rec.HeldBy, h.engine.cfg.Instance)
	}
	if leaseExp, err := time.Parse(time.RFC3339Nano, rec.LeaseExpiresAt); err != nil || !leaseExp.After(time.Now()) {
		t.Fatalf("reclaimed mark must carry a fresh live lease, got %q (err=%v)", rec.LeaseExpiresAt, err)
	}
	deadRequestID := deriveEpisodeRequestID(targetID, entityID, "missing_x", oldRev)
	wantRequestID := deriveEpisodeRequestID(targetID, entityID, "missing_x", entry.Revision)
	if op["requestId"] == deadRequestID {
		t.Fatalf("the reclaim must mint a NEW episode, not re-fire the dead one (%s)", deadRequestID)
	}
	if op["requestId"] != wantRequestID {
		t.Fatalf("reclaim requestId = %v, want the fresh episode %v", op["requestId"], wantRequestID)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 1 {
		t.Fatalf("sweepReclaims = %d, want 1", reclaims)
	}
	h.requireNoOp(t)
}

// TestSweep_LegacyMarkReclaimed proves a lease-less mark (the pre-lease value
// shape: no leaseExpiresAt, no TTL) reads as expired — reclaimed on the first
// sweep, never immortal.
func TestSweep_LegacyMarkReclaimed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureLegacy"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	legacy := fixtureMark(targetID, entityID, "missing_x", "directOp", "")
	legacy.HeldBy = ""
	oldRev := h.putMark(t, ctx, key, legacy)
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	h.nextOp(t)
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision == oldRev {
		t.Fatalf("legacy mark must be reclaimed into a fresh episode (err=%v)", err)
	}
}

// TestSweep_LeaseUnexpired proves a live lease is respected: the episode is in
// flight, the sweep leaves the mark and dispatches nothing.
func TestSweep_LeaseUnexpired(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureLive"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", futureLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != rev {
		t.Fatalf("a live-lease mark must be untouched (err=%v)", err)
	}
	h.requireNoOp(t)
}

// TestSweep_WarmUpGuardAndOrphanTarget proves F8 with the registry warm-up
// guard: while the warm-up window (a registry-replay-readiness proxy) is
// open, BOTH orphan legs — target not installed AND playbook lacking the gap
// column — leave their expired marks standing on every pass, while the
// expired-lease reclaim of an installed target runs ungated; once the window
// elapses both orphans are deleted without dispatch.
func TestSweep_WarmUpGuardAndOrphanTarget(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.SweepOrphanWarmup = time.Hour })

	// Orphan leg 1: no target installed.
	const goneTarget = "fixtureGone"
	goneEntity := testNanoID(t)
	goneKey := markKey(goneTarget, goneEntity, "missing_x")
	h.putMark(t, ctx, goneKey, fixtureMark(goneTarget, goneEntity, "missing_x", "triggerLoom", pastLease()))
	h.putRow(t, ctx, goneTarget, goneEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + goneEntity, "violating": true, "missing_x": true,
	})

	// Orphan leg 2: target installed but its playbook no longer names the gap.
	const droppedTarget = "fixtureDropGap"
	h.seedTarget(&Target{
		TargetID: droppedTarget,
		Gaps:     map[string]GapAction{"missing_other": {Action: actionDirectOp, Operation: "FixOther"}},
	})
	droppedEntity := testNanoID(t)
	droppedKey := markKey(droppedTarget, droppedEntity, "missing_x")
	h.putMark(t, ctx, droppedKey, fixtureMark(droppedTarget, droppedEntity, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, droppedTarget, droppedEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + droppedEntity, "violating": true, "missing_x": true,
	})

	// Ungated control: an installed target's expired mark reclaims during
	// warm-up.
	const liveTarget = "fixtureLiveReclaim"
	h.seedTarget(&Target{
		TargetID: liveTarget,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	liveEntity := testNanoID(t)
	liveKey := markKey(liveTarget, liveEntity, "missing_x")
	h.putMark(t, ctx, liveKey, fixtureMark(liveTarget, liveEntity, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, liveTarget, liveEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + liveEntity, "violating": true, "missing_x": true,
	})

	h.pass(ctx)
	h.pass(ctx)
	if !h.markExists(t, ctx, goneKey) {
		t.Fatalf("inside the warm-up window every pass must skip the target-uninstalled orphan leg")
	}
	if !h.markExists(t, ctx, droppedKey) {
		t.Fatalf("inside the warm-up window every pass must skip the orphan-column leg")
	}
	h.nextOp(t)
	h.requireNoOp(t)
	if reclaims, _, orphans, _, _ := h.engine.sweep.metrics(); reclaims != 1 || orphans != 0 {
		t.Fatalf("during warm-up: sweepReclaims = %d, sweepOrphansDeleted = %d; want 1, 0", reclaims, orphans)
	}

	h.agePastWarmup()
	h.pass(ctx)
	if h.markExists(t, ctx, goneKey) {
		t.Fatalf("after the warm-up window a removed target's mark must be deleted")
	}
	if h.markExists(t, ctx, droppedKey) {
		t.Fatalf("after the warm-up window an orphan-column mark must be deleted")
	}
	h.requireNoOp(t)
	if _, _, orphans, _, _ := h.engine.sweep.metrics(); orphans != 2 {
		t.Fatalf("sweepOrphansDeleted = %d, want 2", orphans)
	}
}

// TestSweep_OrphanColumn proves F7's playbook-drop half: once the warm-up
// window has elapsed, a still-true column the CURRENT playbook no longer
// names is an orphan — deleted without dispatch — and a spec that later
// re-adds the column dispatches fresh, unshadowed.
func TestSweep_OrphanColumn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureDropped"
	// The playbook no longer names missing_x (only missing_other).
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_other": {Action: actionDirectOp, Operation: "FixOther"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	}
	h.putRow(t, ctx, targetID, entityID, row)

	h.pass(ctx)
	if h.markExists(t, ctx, key) {
		t.Fatalf("a mark at a column absent from the current playbook must be deleted")
	}
	h.requireNoOp(t)
	if _, _, orphans, _, _ := h.engine.sweep.metrics(); orphans != 1 {
		t.Fatalf("sweepOrphansDeleted = %d, want 1", orphans)
	}

	// The spec re-adds the column: a fresh delivery dispatches, unshadowed.
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	body, _ := json.Marshal(row)
	dec := h.engine.handleRow(ctx, substrate.Message{
		Subject:      h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Body:         body,
		Sequence:     9,
		NumDelivered: 1,
	})
	if dec != substrate.Ack {
		t.Fatalf("re-added column must dispatch, got %v", dec)
	}
	h.nextOp(t)
}

// TestSweep_CorruptMark proves disposition (a): an unparseable mark value and
// a malformed mark key both alert (CorruptMark Health issue) and are deleted —
// weaver-state is weaver-private, so garbage left in place lives forever. The
// alert follows the delete (a skipped stale-revision delete must not claim a
// deletion), and the issue is retired by the next pass that no longer lists
// the key.
func TestSweep_CorruptMark(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	// Bad value at a well-formed key.
	entityID := testNanoID(t)
	badValKey := markKey("fixtureCorrupt", entityID, "missing_x")
	staleRev, err := h.conn.KVCreate(ctx, "weaver-state", badValKey, []byte("{not json"))
	if err != nil {
		t.Fatalf("create corrupt-value mark: %v", err)
	}
	// Malformed key (no NanoID entity segment).
	badKey := "fixtureCorrupt.notananoid.missing_x"
	if _, err := h.conn.KVCreate(ctx, "weaver-state", badKey, []byte(`{}`)); err != nil {
		t.Fatalf("create corrupt-key mark: %v", err)
	}

	// A stale-revision delete is skipped — and must not raise the "deleted"
	// alert for a deletion that did not happen.
	if _, err := h.conn.KVPut(ctx, "weaver-state", badValKey, []byte("{still not json")); err != nil {
		t.Fatalf("bump corrupt mark revision: %v", err)
	}
	h.engine.sweep.deleteCorrupt(ctx, badValKey, staleRev, "stale-revision probe")
	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("a skipped corrupt delete must not alert a deletion")
	}
	if !h.markExists(t, ctx, badValKey) {
		t.Fatalf("a stale-revision corrupt delete must be skipped")
	}

	h.pass(ctx)

	if h.markExists(t, ctx, badValKey) || h.markExists(t, ctx, badKey) {
		t.Fatalf("corrupt marks must be deleted")
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("a deleted corrupt mark must surface a CorruptMark Health issue")
	}
	if _, _, _, corrupt, _ := h.engine.sweep.metrics(); corrupt != 2 {
		t.Fatalf("sweepCorrupt = %d, want 2", corrupt)
	}
	h.requireNoOp(t)

	// The next pass no longer lists the keys: the issues are retired, so a
	// one-off corrupt entry does not degrade the heartbeat forever.
	h.pass(ctx)
	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("the CorruptMark issue must be retired once the key stays gone")
	}
}

// TestSweep_PlanFailureLeavesMark proves the plan-before-delete ordering: a
// reclaim whose plan fails (unresolved pattern reference) leaves the expired
// mark in place for the next sweep — deleting first would orphan the gap until
// the next row delivery — and surfaces the failure to Health.
func TestSweep_PlanFailureLeavesMark(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixturePlanFail"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_x": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"},
		},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "triggerLoom", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != rev {
		t.Fatalf("a failed plan must leave the expired mark in place (err=%v)", err)
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "UnresolvedReference") {
		t.Fatalf("a failed reclaim plan must surface to Health")
	}
	h.requireNoOp(t)

	// The pattern is installed later: the next sweep reclaims.
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["ghostFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()
	h.pass(ctx)
	h.nextOp(t)
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 1 {
		t.Fatalf("sweepReclaims = %d, want 1", reclaims)
	}
}

// TestReclaim_StableUserTaskIdentity is the §10.3 anti-duplication proof: a
// triggerLoom gap reclaimed across TWO mark-lease expiries re-dispatches
// StartLoomPattern with the SAME claimId-derived Loom instanceId both times — so
// Loom collapses the second on the existing instance and no duplicate userTask is
// spawned. (The defect was markRevision-derived ids that differed per reclaim.)
// The preserved claimId is the load-bearing invariant; the instanceId is what
// Loom dedups on.
func TestReclaim_StableUserTaskIdentity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureLoomDup"
	const gap = "missing_onboarding"
	const claimID = "Lk2Pn6mQrtwzKbcXvP3T" // the preserved per-open-episode token
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{gap: {Action: actionTriggerLoom, Pattern: "onboardFlow", Subject: "row.entityKey"}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["onboardFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, gap)
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
	})

	wantInstance := deriveStableInstanceID(targetID, entityID, gap, claimID)

	// First reclaim: an expired mark carrying claimID.
	m := fixtureMark(targetID, entityID, gap, "triggerLoom", pastLease())
	m.ClaimID = claimID
	h.putMark(t, ctx, key, m)
	h.pass(ctx)
	op1 := h.nextOp(t)
	got1 := op1["payload"].(map[string]any)["instanceId"]
	if got1 != wantInstance {
		t.Fatalf("reclaim 1 instanceId = %v, want the claimId-derived stable id %q", got1, wantInstance)
	}

	// The reclaim PRESERVED the claimId on the re-armed mark.
	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, gap)
	if err != nil || !found {
		t.Fatalf("re-armed mark missing: err=%v found=%v", err, found)
	}
	if rec.ClaimID != claimID {
		t.Fatalf("reclaim must preserve claimId: got %q want %q", rec.ClaimID, claimID)
	}

	// Age the re-armed mark again (unconditional overwrite — the key now exists)
	// preserving the same claimId, and reclaim a SECOND time: same instanceId.
	m2 := fixtureMark(targetID, entityID, gap, "triggerLoom", pastLease())
	m2.ClaimID = claimID
	m2Body, err := json.Marshal(m2)
	if err != nil {
		t.Fatalf("marshal aged mark: %v", err)
	}
	if _, err := h.conn.KVPut(ctx, "weaver-state", key, m2Body); err != nil {
		t.Fatalf("age re-armed mark: %v", err)
	}
	h.pass(ctx)
	op2 := h.nextOp(t)
	got2 := op2["payload"].(map[string]any)["instanceId"]
	if got2 != got1 {
		t.Fatalf("reclaim 2 instanceId = %v, want it STABLE across reclaims (= %v)", got2, got1)
	}
}

// TestSweep_InflightActionMismatchIgnoredForUserTaskGap proves the
// staleMark/ga.Action cross-check: a triggerLoom gap has no external-call
// outcome, so a lens mistakenly declaring its inflight_<g> companion column
// (a package authoring bug) must NOT be trusted as proof the gap is a
// concluded EXTERNAL gap. Before the cross-check, this mark would have been
// misclassified confirmedConcluded=true and reclaimed with a FRESH claimId
// (§10.3's external-gap behavior), collapsing the "retry" onto a new,
// unrelated Loom instance and violating §10.3's claimId-verbatim rule for a
// human userTask gap. The reclaim must instead preserve the mark's claimId
// exactly like TestReclaim_StableUserTaskIdentity, and the mismatch must
// surface as a Health issue rather than fail silently (FR29).
func TestSweep_InflightActionMismatchIgnoredForUserTaskGap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureInflightMismatch"
	const gap = "missing_onboarding"
	const claimID = "Lk2Pn6mQrtwzKbcXvP3T"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{gap: {Action: actionTriggerLoom, Pattern: "onboardFlow", Subject: "row.entityKey"}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["onboardFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, gap)
	// A misauthored lens: inflight_<g> declared on a triggerLoom gap, reading
	// false (which, absent the cross-check, reads as "call concluded").
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true, "inflight_onboarding": false,
	})

	m := fixtureMark(targetID, entityID, gap, "triggerLoom", pastLease())
	m.ClaimID = claimID
	h.putMark(t, ctx, key, m)
	h.pass(ctx)
	h.nextOp(t)

	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, gap)
	if err != nil || !found {
		t.Fatalf("re-armed mark missing: err=%v found=%v", err, found)
	}
	if rec.ClaimID != claimID {
		t.Fatalf("a mismatched inflight_<g> must not mint a fresh claimId for a userTask gap: got %q want preserved %q",
			rec.ClaimID, claimID)
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "InflightActionMismatch") {
		t.Fatalf("expected an InflightActionMismatch Health issue for the misdeclared column")
	}
}

// TestReclaim_StableTaskId_AssignTask is the assignTask analogue of the proof
// above: a SignLease assignTask gap reclaimed across two mark-lease expiries
// re-dispatches CreateTask with the SAME claimId-derived taskId both times, so
// the CreateTask kv.Read branch collapses the second on the existing task.
func TestReclaim_StableTaskId_AssignTask(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureSignDup"
	const gap = "missing_signature"
	const claimID = "Zz9Yx8Wv7Ut6Sr5Qp4N"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{gap: {
			Action: actionAssignTask, Operation: "SignLease", Assignee: "row.applicant", Target: "row.entityKey",
		}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.opMetaByType["SignLease"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, gap)
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseapp." + entityID, "violating": true, gap: true,
		"applicant": "vtx.identity." + testNanoID(t),
	})
	wantTask := deriveStableTaskID(targetID, entityID, gap, claimID)

	m := fixtureMark(targetID, entityID, gap, "assignTask", pastLease())
	m.ClaimID = claimID
	h.putMark(t, ctx, key, m)
	h.pass(ctx)
	op1 := h.nextOp(t)
	got1 := op1["payload"].(map[string]any)["taskId"]
	if got1 != wantTask {
		t.Fatalf("reclaim 1 taskId = %v, want the claimId-derived stable id %q", got1, wantTask)
	}

	// Age the re-armed mark (preserving claimId) and reclaim again: same taskId.
	m2 := fixtureMark(targetID, entityID, gap, "assignTask", pastLease())
	m2.ClaimID = claimID
	m2Body, err := json.Marshal(m2)
	if err != nil {
		t.Fatalf("marshal aged mark: %v", err)
	}
	if _, err := h.conn.KVPut(ctx, "weaver-state", key, m2Body); err != nil {
		t.Fatalf("age re-armed mark: %v", err)
	}
	h.pass(ctx)
	op2 := h.nextOp(t)
	if got2 := op2["payload"].(map[string]any)["taskId"]; got2 != got1 {
		t.Fatalf("reclaim 2 taskId = %v, want it STABLE across reclaims (= %v)", got2, got1)
	}
}

// TestSweep_DeleteRevisionRace proves every sweep delete is conditioned on the
// revision read this pass: a fresh episode CAS-created between the sweep's
// read and its delete wins the race — the delete is skipped and the fresh mark
// stays intact.
func TestSweep_DeleteRevisionRace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureRace"
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	staleRev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))

	// A fresh episode replaces the mark after the sweep's (simulated) read.
	if err := h.conn.KVDelete(ctx, "weaver-state", key); err != nil {
		t.Fatalf("delete stale mark: %v", err)
	}
	fresh := fixtureMark(targetID, entityID, "missing_x", "directOp", futureLease())
	body, _ := json.Marshal(fresh)
	freshRev, err := h.conn.KVCreate(ctx, "weaver-state", key, body)
	if err != nil {
		t.Fatalf("create fresh mark: %v", err)
	}

	if h.engine.sweep.deleteMark(ctx, key, staleRev, "directOp", sweepReasonTargetRemoved,
		targetID, entityID, "missing_x") {
		t.Fatalf("a stale-revision delete must be skipped, not succeed")
	}
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != freshRev {
		t.Fatalf("the fresh episode's mark must stay intact (err=%v)", err)
	}
}

// TestSweep_ReclaimConflictSkips proves the reclaim's atomicity: the in-place
// replace is conditioned on the revision read this pass, so a mark that
// changed under the sweep (a fresh episode won the race) is skipped — no op,
// no counter — and the key is never absent at any point (the crash window of
// a delete-then-recreate reclaim does not exist).
func TestSweep_ReclaimConflictSkips(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureConflict"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	expired := fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease())
	staleRev := h.putMark(t, ctx, key, expired)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	}
	h.putRow(t, ctx, targetID, entityID, row)

	// A fresh episode replaces the mark after the sweep's (simulated) read.
	fresh := fixtureMark(targetID, entityID, "missing_x", "directOp", futureLease())
	body, _ := json.Marshal(fresh)
	freshRev, err := h.conn.KVUpdate(ctx, "weaver-state", key, body, staleRev)
	if err != nil {
		t.Fatalf("replace with fresh mark: %v", err)
	}

	h.engine.sweep.reclaim(ctx, key, staleRev, &expired, targetID, entityID, "missing_x", row, 7)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != freshRev {
		t.Fatalf("the fresh episode's mark must stay intact and present (err=%v)", err)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 0 {
		t.Fatalf("sweepReclaims = %d, want 0 (a conflicted reclaim is a skip)", reclaims)
	}
	h.requireNoOp(t)
}

// TestSweep_NonViolatingRowNotReclaimed proves the reclaim mirrors lane-1's
// L1 gate: an expired mark whose row carries an open missing_* column but
// violating=false is left alone — no dispatch (lane-1 would never fire it)
// and no delete (level clearing or the next CDC delivery owns the mark; the
// TTL backstop bounds a stale one).
func TestSweep_NonViolatingRowNotReclaimed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureNotViolating"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": false, "missing_x": true,
	})

	h.pass(ctx)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != rev {
		t.Fatalf("a non-violating row's expired mark must be left untouched (err=%v)", err)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 0 {
		t.Fatalf("sweepReclaims = %d, want 0", reclaims)
	}
	h.requireNoOp(t)
}

// TestSweep_MissingEntityKeyMarks proves a violating row with no entityKey
// echo routes its expired mark through the corrupt leg — alert + delete —
// instead of re-alerting forever over an unreclaimable mark, and the issue
// key is per-mark, so two bad entities under one target alert independently.
func TestSweep_MissingEntityKeyMarks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureNoEntityKey"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	keys := make([]string, 0, 2)
	for range 2 {
		entityID := testNanoID(t)
		key := markKey(targetID, entityID, "missing_x")
		h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
		h.putRow(t, ctx, targetID, entityID, map[string]any{
			"violating": true, "missing_x": true,
		})
		keys = append(keys, key)
	}

	h.pass(ctx)

	for _, key := range keys {
		if h.markExists(t, ctx, key) {
			t.Fatalf("an entityKey-less violating row's expired mark must be deleted (%s)", key)
		}
	}
	count := 0
	for _, issue := range h.engine.issues.snapshot() {
		if issue.Code == "CorruptMark" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("CorruptMark issues = %d, want 2 (one per entity, no key collision)", count)
	}
	if _, _, _, corrupt, _ := h.engine.sweep.metrics(); corrupt != 2 {
		t.Fatalf("sweepCorrupt = %d, want 2", corrupt)
	}
	h.requireNoOp(t)

	// Retired once the keys stay gone.
	h.pass(ctx)
	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("the CorruptMark issues must be retired once the keys stay gone")
	}
}

// TestSweep_ControlMarkerSurvives proves the reserved-key guard (AC #3
// reserved-key safety): a `<targetId>.__control` dispatch-skip marker is not
// a §10.3 mark (it has no <entityId>.<gapColumn> tail, so splitMarkKey would
// reject it as corrupt) — the sweep must skip it entirely, never enumerating
// it as corrupt and never deleting it, across both warm-up states.
func TestSweep_ControlMarkerSurvives(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureControlMarker"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	if err := h.engine.marks.setDisabled(ctx, targetID, true); err != nil {
		t.Fatalf("setDisabled: %v", err)
	}

	h.pass(ctx)
	h.pass(ctx)

	disabled, err := h.engine.marks.isDisabled(ctx, targetID)
	if err != nil {
		t.Fatalf("isDisabled after sweep: %v", err)
	}
	if !disabled {
		t.Fatalf("the __control marker must survive sweep passes, got disabled=false")
	}
	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("the __control marker must never be enumerated as a CorruptMark")
	}
	if _, _, _, corrupt, _ := h.engine.sweep.metrics(); corrupt != 0 {
		t.Fatalf("sweepCorrupt = %d, want 0 (the __control marker is not a mark)", corrupt)
	}
	h.requireNoOp(t)
}

// TestConfigClamps proves the withDefaults invariants that keep the sweep's
// reclaim leg reachable: SweepInterval is clamped to MarkLease (an expired
// mark must be observed before its 2×lease TTL deletes it unseen) and
// SweepOrphanWarmup is clamped up to SweepInterval (a warm-up shorter than
// one tick gates nothing), defaulting to 5m.
func TestConfigClamps(t *testing.T) {
	t.Parallel()
	cfg := Config{
		MarkLease:         5 * time.Second,
		SweepInterval:     time.Minute,
		SweepOrphanWarmup: time.Millisecond,
		Logger:            discardLogger(),
	}
	cfg.withDefaults()
	if cfg.SweepInterval != 5*time.Second {
		t.Fatalf("SweepInterval = %v, want the MarkLease clamp 5s", cfg.SweepInterval)
	}
	if cfg.SweepOrphanWarmup != 5*time.Second {
		t.Fatalf("SweepOrphanWarmup = %v, want the SweepInterval clamp 5s", cfg.SweepOrphanWarmup)
	}

	def := Config{Logger: discardLogger()}
	def.withDefaults()
	if def.SweepInterval != defaultSweepInterval {
		t.Fatalf("default SweepInterval = %v, want %v", def.SweepInterval, defaultSweepInterval)
	}
	if def.SweepOrphanWarmup != defaultSweepOrphanWarmup {
		t.Fatalf("default SweepOrphanWarmup = %v, want %v", def.SweepOrphanWarmup, defaultSweepOrphanWarmup)
	}
}

// TestMarkCreate_TTLBackstop proves the dispatch-path create arms the NATS
// per-key TTL at markTTLBackstopFactor × MarkLease (the wire Nats-TTL header)
// and mirrors the lease in leaseExpiresAt — the "dead reconciler" guarantee is
// this header plus the substrate-level expiry test.
func TestMarkCreate_TTLBackstop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	lease := 2 * time.Second
	h := newSweepHarness(t, ctx, func(c *Config) { c.MarkLease = lease })

	const targetID = "fixtureTTL"
	entityID := testNanoID(t)
	before := time.Now()
	_, _, exists, err := h.engine.marks.create(ctx, targetID, entityID, "missing_x",
		"vtx.leaseApp."+entityID, "directOp")
	if err != nil || exists {
		t.Fatalf("mark create: err=%v exists=%v", err, exists)
	}

	key := markKey(targetID, entityID, "missing_x")
	stream, err := h.conn.JetStream().Stream(ctx, "KV_weaver-state")
	if err != nil {
		t.Fatalf("open weaver-state stream: %v", err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, "$KV.weaver-state."+key)
	if err != nil {
		t.Fatalf("read raw mark message: %v", err)
	}
	wantTTL := (markTTLBackstopFactor * lease).String()
	if got := raw.Header.Get("Nats-TTL"); got != wantTTL {
		t.Fatalf("mark Nats-TTL header = %q, want %q (markTTLBackstopFactor × MarkLease)", got, wantTTL)
	}

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("read mark: %v", err)
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal mark: %v", err)
	}
	leaseExp, err := time.Parse(time.RFC3339Nano, rec.LeaseExpiresAt)
	if err != nil {
		t.Fatalf("leaseExpiresAt %q: %v", rec.LeaseExpiresAt, err)
	}
	if leaseExp.Before(before.Add(lease)) || leaseExp.After(time.Now().Add(lease)) {
		t.Fatalf("leaseExpiresAt %v must mirror claimedAt + MarkLease", leaseExp)
	}
	if rec.HeldBy != h.engine.cfg.Instance {
		t.Fatalf("heldBy = %q, want %q", rec.HeldBy, h.engine.cfg.Instance)
	}
	// The mark CAS-create now mints the per-open-episode claimId (§10.3): it must
	// be a valid NanoID, the stable seed the userTask identity derives from.
	if !substrate.IsValidNanoID(rec.ClaimID) {
		t.Fatalf("claimId must be a minted NanoID on a written mark, got %q", rec.ClaimID)
	}
}

// TestSweep_InflightGapNotReclaimed proves SKIP SITE 2 — the load-bearing one.
// The mark-lease expiry → sweep reclaim is the actual re-dispatch path for a
// long-pending external call; the lane-1 skip alone does NOT stop it. An expired
// mark over a violating row whose gap carries inflight_<g>=true must be LEFT
// untouched, with NO re-dispatch op — exactly as the in-flight call requires.
func TestSweep_InflightGapNotReclaimed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureInflightSweep"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "inflight_x": true,
	})

	h.pass(ctx)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != rev {
		t.Fatalf("an in-flight gap's expired mark must be left untouched by the sweep (err=%v)", err)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 0 {
		t.Fatalf("sweepReclaims = %d, want 0 (in-flight suppression)", reclaims)
	}
	h.requireNoOp(t)
}

// TestSweep_ExhaustedBudgetGapNotReclaimed proves skip site 2 also fires on the §E
// mechanism-B budget term: a violating row whose weaver-state dispatch-count has
// reached the row's maxretries_<g> is never re-dispatched by the sweep — the mark
// is left and no op fires (the terminal is "stop and escalate," the gap stays
// violating).
func TestSweep_ExhaustedBudgetGapNotReclaimed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureExhaustedSweep"
	const cap = 3
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	// Seed the dispatch-count to the cap: the budget is spent.
	for i := 0; i < cap; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
		"inflight_x": false, "maxretries_x": cap,
	})

	h.pass(ctx)

	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || entry.Revision != rev {
		t.Fatalf("an exhausted-budget gap's expired mark must be left untouched by the sweep (err=%v)", err)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 0 {
		t.Fatalf("sweepReclaims = %d, want 0 (budget-cap suppression)", reclaims)
	}
	h.requireNoOp(t)
}

// TestSweep_ExhaustedBudgetGapEscalatesToAugur proves Fire 9's second
// suppression site (weaver-exhausted-escalation-and-model): the sweep is the
// ONLY dispatch leg that still visits a row once its owning entity stops
// producing fresh CDC deliveries, so it — not lane-1 — must actually close the
// §10.8 "never a silent park" promise for a gap that has gone quiet. A target
// escalating "exhausted" gets a fresh CreateAugurReasoningClaim episode fired
// by the sweep; the exhausted gap's OWN (already-expired) mark is left
// untouched (never reclaimed in place, never re-armed with the escalation
// action) — the escalation is a genuinely separate episode.
func TestSweep_ExhaustedBudgetGapEscalatesToAugur(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureExhaustedSweepAugur"
	id := testNanoID(t)
	spec := targetSpecFixture(targetID) // declares gaps.missing_a -> directOp FixA
	spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
	h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
	h.engine.source.handle(specEvent(t, id, spec))

	const cap = 3
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_a")
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_a", "directOp", pastLease()))
	for i := 0; i < cap; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_a"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_a": true,
		"inflight_a": false, "maxretries_a": cap,
	})

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != defaultAugurOp {
		t.Fatalf("operationType = %v, want %q (the escalation, not the exhausted FixA action)", op["operationType"], defaultAugurOp)
	}
	// The exhausted gap's OWN (now-stale) mark is cleared and replaced by a
	// FRESH one for the escalation episode — a genuinely new revision, never
	// the original rev, and NOT the sweep's ordinary reclaim-in-place metric
	// (this is a fresh CAS-create, not a reclaim of the original mark).
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("the escalation must leave a fresh mark at the gap's key: %v", err)
	}
	if entry.Revision == rev {
		t.Fatalf("the escalation must not reuse the exhausted gap's original mark revision")
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 0 {
		t.Fatalf("sweepReclaims = %d, want 0 (the escalation is not a reclaim of the original mark)", reclaims)
	}
}

// TestSweep_ReclaimIncrementsBudget proves a sweep reclaim (a fresh dispatch on a
// re-armed mark) advances the chain's dispatch-count — so a multi-attempt chain
// driven by the sweeper (not just CDC touches) accrues toward the cap. A reclaim
// of a count-0 gap whose row cap is above 1 reclaims AND bumps the count to 1; a
// second reclaim would take it to 2, etc.
func TestSweep_ReclaimIncrementsBudget(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureReclaimBudget"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
		"inflight_x": false, "maxretries_x": 5,
	})

	h.pass(ctx)
	h.nextOp(t) // the reclaim re-dispatched

	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != 1 {
		t.Fatalf("a reclaim must increment the dispatch-count: got %d (err=%v), want 1", got, err)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 1 {
		t.Fatalf("sweepReclaims = %d, want 1", reclaims)
	}
}

// TestSweep_ReclaimRecordsEffectDispatch proves the sweep-reclaim half of the
// §10.3 `__effect` confidence window (design §3.2, Fire 2): a reclaim IS a
// fresh dispatch, so it must advance the (target, gap, actionRef) window
// exactly like the lane-1 CAS-create path — the same seam bumpDispatchCount
// uses.
func TestSweep_ReclaimRecordsEffectDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureReclaimEffect"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
		"inflight_x": false, "maxretries_x": 5,
	})

	h.pass(ctx)
	h.nextOp(t) // the reclaim re-dispatched

	stats, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, "missing_x", actionDirectOp)
	if err != nil || !ok {
		t.Fatalf("read effect stats after reclaim: err=%v ok=%v", err, ok)
	}
	if len(stats.Window) != 1 || stats.Window[0] {
		t.Fatalf("window after one reclaim dispatch = %v, want [false] (pending)", stats.Window)
	}
}

// TestSweep_CollapseOnlyReclaimBooksNoEffectDispatch is the counterpart to the
// proof above: a reclaim that can only COLLAPSE (assignTask — the consumer
// re-lands on the same claimId-derived task) mounts no new attempt, so it must
// NOT book a pending `__effect` episode. Booking one would append a slot no
// close can ever answer — a human userTask held open across enough reclaims
// would fill its whole window and trip a LensEffectMismatch describing nothing.
// The retry-budget dispatch-count is asserted to still advance: it bounds
// reclaim effort, which a repeat reclaim genuinely spends.
func TestSweep_CollapseOnlyReclaimBooksNoEffectDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCollapseEffect"
	const gap = "missing_signature"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{gap: {
			Action: actionAssignTask, Operation: "SignLease", Assignee: "row.applicant", Target: "row.entityKey",
		}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.opMetaByType["SignLease"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	h.putMark(t, ctx, markKey(targetID, entityID, gap),
		fixtureMark(targetID, entityID, gap, actionAssignTask, pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
		"applicant": "vtx.identity." + testNanoID(t),
	})

	h.pass(ctx)
	h.nextOp(t) // the reclaim re-dispatched (and will collapse at the consumer)

	if _, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, gap, actionAssignTask); err != nil || ok {
		t.Fatalf("a collapse-only reclaim must book no effect episode: present=%v (err=%v)", ok, err)
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, gap); err != nil || got != 1 {
		t.Fatalf("the retry-budget count must still advance: got %d (err=%v), want 1", got, err)
	}
}

// TestSweep_GapClosedCreditsEffectClose proves the close side of the §10.3
// `__effect` window has a sweep leg at all: when the sweep — not lane-1 — is
// the leg that observes a gap close, the close must be credited to the window.
// For a row that has gone quiet the sweep is the ONLY leg that will observe it,
// so crediting lane-1 alone biased every window toward zero closes.
func TestSweep_GapClosedCreditsEffectClose(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureSweepClose"
	const gap = "missing_x"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{gap: {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	h.putMark(t, ctx, markKey(targetID, entityID, gap),
		fixtureMark(targetID, entityID, gap, actionDirectOp, futureLease()))
	// The dispatch this close answers.
	if _, err := h.conn.KVCreate(ctx, "weaver-state", effectKey(targetID, gap, actionDirectOp),
		mustMarshalEffectStats(t, effectStats{Window: []bool{false}})); err != nil {
		t.Fatalf("seed effect key: %v", err)
	}
	// The gap has closed: the sweep clears the mark on the level reconcile.
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": false, gap: false,
	})

	h.pass(ctx)

	stats, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, gap, actionDirectOp)
	if err != nil || !ok {
		t.Fatalf("read effect stats after a sweep-won close: err=%v ok=%v", err, ok)
	}
	if len(stats.Window) != 1 || !stats.Window[0] {
		t.Fatalf("window after a sweep-won close = %v, want [true] (closed)", stats.Window)
	}
}

// TestSweep_OrphanDeleteCreditsNoEffectClose guards the gate above from being
// widened to every sweep delete: targetRemoved/orphanColumn mean the gap went
// AWAY rather than closed, so neither may be credited as an observed close.
func TestSweep_OrphanDeleteCreditsNoEffectClose(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureOrphanNoClose"
	const gap = "missing_x"
	// The target is installed but its playbook no longer names the gap column:
	// the mark is an orphanColumn delete, not a close.
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_other": {Action: actionDirectOp, Operation: "FixOther"}},
	})
	entityID := testNanoID(t)
	h.putMark(t, ctx, markKey(targetID, entityID, gap),
		fixtureMark(targetID, entityID, gap, actionDirectOp, pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
	})
	if _, err := h.conn.KVCreate(ctx, "weaver-state", effectKey(targetID, gap, actionDirectOp),
		mustMarshalEffectStats(t, effectStats{Window: []bool{false}})); err != nil {
		t.Fatalf("seed effect key: %v", err)
	}

	h.pass(ctx)

	// The window's own orphan-GC leg may delete it outright; what must never
	// happen is its pending slot being flipped to closed.
	if stats, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, gap, actionDirectOp); err == nil && ok {
		if len(stats.Window) != 1 || stats.Window[0] {
			t.Fatalf("an orphan-column delete must not credit a close: window = %v, want [false]", stats.Window)
		}
	}
}

// TestSweep_EffectOrphanColumn proves the `__effect` sweep-GC leg's
// orphan-column half (mirrors TestSweep_OrphanColumn for the confidence
// window instead of a mark): once the warm-up window has elapsed, a
// confidence window whose gap column the CURRENT playbook no longer names is
// deleted.
func TestSweep_EffectOrphanColumn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureEffectDroppedColumn"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_other": {Action: actionDirectOp, Operation: "FixOther"}},
	})
	key := effectKey(targetID, "missing_x", "directOp")
	if _, err := h.conn.KVCreate(ctx, "weaver-state", key, mustMarshalEffectStats(t, effectStats{Window: []bool{false}})); err != nil {
		t.Fatalf("seed effect key: %v", err)
	}

	h.pass(ctx)
	if _, err := h.conn.KVGet(ctx, "weaver-state", key); err == nil {
		t.Fatalf("an effect window at a column absent from the current playbook must be deleted")
	}
	if _, _, orphans, _, _ := h.engine.sweep.metrics(); orphans != 1 {
		t.Fatalf("sweepOrphansDeleted = %d, want 1", orphans)
	}
}

// TestSweep_EffectTargetRemoved proves the `__effect` sweep-GC leg's
// target-removed half, warm-up gated exactly like the mark orphan legs: an
// uninstalled target's confidence window survives during warm-up and is
// deleted once the window elapses.
func TestSweep_EffectTargetRemoved(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.SweepOrphanWarmup = time.Hour })

	const targetID = "fixtureEffectGoneTarget"
	key := effectKey(targetID, "missing_x", "directOp")
	if _, err := h.conn.KVCreate(ctx, "weaver-state", key, mustMarshalEffectStats(t, effectStats{Window: []bool{false, true}})); err != nil {
		t.Fatalf("seed effect key: %v", err)
	}

	h.pass(ctx)
	if _, err := h.conn.KVGet(ctx, "weaver-state", key); err != nil {
		t.Fatalf("during warm-up an uninstalled target's effect window must survive: %v", err)
	}

	h.agePastWarmup()
	h.pass(ctx)
	if _, err := h.conn.KVGet(ctx, "weaver-state", key); err == nil {
		t.Fatalf("after the warm-up window an uninstalled target's effect window must be deleted")
	}
	if _, _, orphans, _, _ := h.engine.sweep.metrics(); orphans != 1 {
		t.Fatalf("sweepOrphansDeleted = %d, want 1", orphans)
	}
}

// TestSweep_EffectKeyLiveTargetSurvives proves the converse of the two orphan
// tests above: a live (installed target, declared gap column) confidence
// window is never touched by the sweep — the reserved-marker guard routes it
// to sweepEffect (not sweepMark's corrupt-key path), and sweepEffect only
// deletes on an orphan verdict.
func TestSweep_EffectKeyLiveTargetSurvives(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureEffectLive"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	key := effectKey(targetID, "missing_x", actionDirectOp)
	if _, err := h.conn.KVCreate(ctx, "weaver-state", key, mustMarshalEffectStats(t, effectStats{Window: []bool{false}})); err != nil {
		t.Fatalf("seed effect key: %v", err)
	}

	h.pass(ctx)
	h.pass(ctx)
	if _, err := h.conn.KVGet(ctx, "weaver-state", key); err != nil {
		t.Fatalf("a live target/column's effect window must survive the sweep: %v", err)
	}
	if _, _, orphans, corrupt, _ := h.engine.sweep.metrics(); orphans != 0 || corrupt != 0 {
		t.Fatalf("sweepOrphansDeleted = %d, sweepCorrupt = %d; want 0, 0", orphans, corrupt)
	}
}

func mustMarshalEffectStats(t *testing.T, stats effectStats) []byte {
	t.Helper()
	body, err := json.Marshal(stats)
	if err != nil {
		t.Fatalf("marshal effect stats: %v", err)
	}
	return body
}

// TestSweep_CountKeySurvives proves the reserved count-key guard: a
// `…__count` dispatch-count is not a §10.3 mark (it has a 4th segment, so
// splitMarkKey would reject it as corrupt) — the sweep must skip it entirely,
// never enumerating it as corrupt and never deleting it, across both warm-up
// states.
func TestSweep_CountKeySurvives(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountKey"
	entityID := testNanoID(t)
	if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("seed dispatch-count: %v", err)
	}

	h.pass(ctx)
	h.pass(ctx)

	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != 1 {
		t.Fatalf("the dispatch-count must survive sweep passes: got %d (err=%v), want 1", got, err)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("a dispatch-count must never be enumerated as a CorruptMark")
	}
	if _, _, _, corrupt, _ := h.engine.sweep.metrics(); corrupt != 0 {
		t.Fatalf("sweepCorrupt = %d, want 0 (the __count key is not a mark)", corrupt)
	}
	h.requireNoOp(t)
}
