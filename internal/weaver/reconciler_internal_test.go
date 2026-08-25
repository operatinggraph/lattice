package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
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
	srv := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, srv.ClientURL())
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

// reexpireMark rewrites an already-reclaimed mark's ClaimedAt (pushed back,
// fixtureMark's own "-2h" convention) and LeaseExpiresAt (past) — simulating
// that time has passed since the mark's last reclaim, so the NEXT sweep pass
// treats it as an expired lease again regardless of any configured reclaim
// backoff. Drives a mark through several successive reclaims in one test
// without a real wall-clock wait.
func (h *sweepHarness) reexpireMark(t *testing.T, ctx context.Context, key string) {
	t.Helper()
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("read mark %q to re-expire: %v", key, err)
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal mark %q to re-expire: %v", key, err)
	}
	rec.ClaimedAt = substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour))
	rec.LeaseExpiresAt = pastLease()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal re-expired mark %q: %v", key, err)
	}
	if _, err := h.conn.KVPut(ctx, "weaver-state", key, body); err != nil {
		t.Fatalf("put re-expired mark %q: %v", key, err)
	}
}

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
	h.engine.sweep.deleteCorrupt(ctx, badValKey, staleRev, corruptShapeMark, "stale-revision probe")
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

// seedInflightMismatchFixture wires the shared shape the three reclaim-
// classification proofs below need: a triggerLoom gap over patternRef, a
// violating row that DECLARES inflight_<g> reading false (which, without the
// classifier, would read as "the external call concluded"), and an
// expired-lease mark carrying claimID. It deliberately does not index the
// pattern — each caller seeds (or withholds) the spec that is its subject.
func seedInflightMismatchFixture(t *testing.T, ctx context.Context, h *sweepHarness,
	targetID, gap, patternRef, claimID string, extraRow map[string]any) (entityID, key string) {
	t.Helper()
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{gap: {Action: actionTriggerLoom, Pattern: patternRef, Subject: "row.entityKey"}},
	})
	entityID = testNanoID(t)
	key = markKey(targetID, entityID, gap)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
		inflightColumnPrefix + strings.TrimPrefix(gap, gapColumnPrefix): false,
	}
	for k, v := range extraRow {
		row[k] = v
	}
	h.putRow(t, ctx, targetID, entityID, row)
	m := fixtureMark(targetID, entityID, gap, "triggerLoom", pastLease())
	m.ClaimID = claimID
	h.putMark(t, ctx, key, m)
	return entityID, key
}

// TestSweep_InflightMarkerPreservesClaimIdForUserTaskGap proves the classifier
// over a REAL parking pattern: the gap's pattern is indexed from a spec whose
// steps include a userTask, so triggering it concludes on a person. A lens
// declaring its inflight_<g> companion is contract-legal suppression, NOT an
// authoring bug — the two human-paced lease-signing gaps do exactly this — so it
// must NOT be trusted as proof the gap is a concluded EXTERNAL gap. Without the
// cross-check this mark is misclassified confirmedConcluded=true and reclaimed
// with a FRESH claimId (§10.3's external-gap behavior), collapsing the "retry"
// onto a new, unrelated Loom instance and violating §10.3's claimId-verbatim rule
// for a human userTask gap. The reclaim must instead preserve the mark's claimId
// exactly like TestReclaim_StableUserTaskIdentity, and must raise NO Health issue:
// a suppression-only declaration is the contract working as written.
func TestSweep_InflightMarkerPreservesClaimIdForUserTaskGap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureInflightUserTask"
	const gap = "missing_onboarding"
	const claimID = "Lk2Pn6mQrtwzKbcXvP3T"
	seedPatternSpec(t, h.engine.source, "onboardFlow", stepKindSystemOp, stepKindUserTask)
	entityID, _ := seedInflightMismatchFixture(t, ctx, h, targetID, gap, "onboardFlow", claimID, nil)

	h.pass(ctx)
	h.nextOp(t)

	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, gap)
	if err != nil || !found {
		t.Fatalf("re-armed mark missing: err=%v found=%v", err, found)
	}
	if rec.ClaimID != claimID {
		t.Fatalf("a suppression-only inflight_<g> must not mint a fresh claimId for a userTask gap: got %q want preserved %q",
			rec.ClaimID, claimID)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "InflightActionMismatch") {
		t.Fatalf("a suppression-only inflight_<g> declaration must raise no Health issue")
	}
}

// TestSweep_InflightMarkerIgnoredForUnindexedPattern is the fail-safe leg of the
// same classifier, and the one a restarted Weaver actually walks: the sweep
// reclaims marks left in weaver-state while the meta.loomPattern registry is
// still replaying, so the gap's pattern resolves to no indexed spec. The
// classification is unavailable, not negative, and the engine takes the
// human-safe side — claimId preserved verbatim, exactly as if the pattern
// parked. It also raises no Health issue: staleMark never alerts on a
// suppression-only inflight_<g> declaration, so this fail-safe leg — which every
// restarted Weaver walks while the registry replays — cannot report unhealthy.
func TestSweep_InflightMarkerIgnoredForUnindexedPattern(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureInflightUnindexed"
	const gap = "missing_onboarding"
	const claimID = "Qw4Er5Ty6Uh7Pn8As9Df"
	// patternMeta resolves (so planGap can dispatch) but no SPEC was ever
	// indexed — the registry mid-replay, and the only way to reach the branch.
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["ghostFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()
	entityID, _ := seedInflightMismatchFixture(t, ctx, h, targetID, gap, "ghostFlow", claimID, nil)

	h.pass(ctx)
	h.nextOp(t)

	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, gap)
	if err != nil || !found {
		t.Fatalf("re-armed mark missing: err=%v found=%v", err, found)
	}
	if rec.ClaimID != claimID {
		t.Fatalf("an unindexed pattern must fail SAFE (claimId preserved): got %q want %q", rec.ClaimID, claimID)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "InflightActionMismatch") {
		t.Fatal("a suppression-only inflight_<g> on an unindexed pattern must raise no Health issue")
	}
}

// TestSweep_ExternalTaskOnlyPatternReclaimsWithFreshClaimId is the headline
// behavior of the classifier, and the reason lease-signing's post-timeout
// bgcheck retry works: the SAME fixture as the two proofs above, except the
// pattern's indexed spec is externalTask-only. That gap concludes on a vendor
// outcome, not a person, so with inflight_<g> false the reclaim is a genuinely
// fresh attempt — §10.3's "re-call a dead vendor / mint a fresh service
// instance" — and must mint a FRESH claimId, since deriveStableInstanceID is
// claimId-seeded and reusing the old one would collapse the retry onto the
// already-terminal Loom instance. The row carries maxretries_<g> as §10.3
// requires of any gap declaring inflight_<g>, so the bounded-retry cap, not the
// backoff timer, is what paces it.
func TestSweep_ExternalTaskOnlyPatternReclaimsWithFreshClaimId(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureExternalGapFresh"
	const gap = "missing_bgcheck"
	const claimID = "Zx1Cv2Bn3Mk4Kj5Hg6Fd"
	seedPatternSpec(t, h.engine.source, "bgcheckFlow", stepKindExternalTask)
	entityID, _ := seedInflightMismatchFixture(t, ctx, h, targetID, gap, "bgcheckFlow", claimID,
		map[string]any{"maxretries_bgcheck": 3})

	h.pass(ctx)
	op := h.nextOp(t)

	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, gap)
	if err != nil || !found {
		t.Fatalf("re-armed mark missing: err=%v found=%v", err, found)
	}
	if rec.ClaimID == claimID {
		t.Fatalf("an externalTask-only pattern's reclaim is a FRESH attempt and must mint a new claimId, kept %q", claimID)
	}
	if rec.ClaimID == "" {
		t.Fatal("the reclaim must mint a claimId, not blank it")
	}
	if hasIssueCode(h.engine.issues.snapshot(), "InflightActionMismatch") {
		t.Fatal("an externalTask-only pattern is a legitimate external gap — no mismatch issue")
	}
	// The dispatched op carries the fresh identity: a claimId-seeded instanceId
	// distinct from the one the terminal attempt used.
	payload, _ := op["payload"].(map[string]any)
	if payload == nil {
		t.Fatalf("dispatched op has no payload: %v", op)
	}
	if got, want := payload["instanceId"], deriveStableInstanceID(targetID, entityID, gap, claimID); got == want {
		t.Fatalf("instanceId %v collapses onto the terminal attempt's — the retry would be a no-op", got)
	}
	if got, want := payload["instanceId"], deriveStableInstanceID(targetID, entityID, gap, rec.ClaimID); got != want {
		t.Fatalf("instanceId = %v, want it seeded from the re-armed mark's fresh claimId (%v)", got, want)
	}
}

// TestSweep_UncappedExternalGapIsStillPaced covers the defense-in-depth term for
// Contract #10 §10.3's "a gap declaring inflight_<g> MUST declare
// maxretries_<g>" rule. An external gap's reclaim is deliberately not
// collapse-only, and gapSuppressed refuses to substitute the engine's default
// budget for a row that declares inflight_<g> — so a lens that declares the
// marker without a usable cap would otherwise get a fresh vendor call every
// mark-lease expiry, unbounded AND unpaced, forever. The reclaim must fall back
// to the backoff timer: mark untouched, no op, and the hold counted on
// sweepReclaimsSuppressed. Identical to
// TestSweep_ExternalTaskOnlyPatternReclaimsWithFreshClaimId except the cap.
func TestSweep_UncappedExternalGapIsStillPaced(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A 1h base makes "dispatched moments ago" deterministically inside the
	// backoff window regardless of test-host speed.
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Hour })

	const targetID = "fixtureUncappedExternal"
	const gap = "missing_bgcheck"
	const claimID = "Pm2Nk9Xj8Uh7Yg6Tf5Rd"
	seedPatternSpec(t, h.engine.source, "bgcheckFlow", stepKindExternalTask)
	// No maxretries_bgcheck on the row — the contract violation this guards.
	entityID, key := seedInflightMismatchFixture(t, ctx, h, targetID, gap, "bgcheckFlow", claimID, nil)
	// fixtureMark ages ClaimedAt by 2h; bring it back to "just dispatched" so the
	// 1h backoff window is live.
	rec, _, _, err := h.engine.marks.get(ctx, targetID, entityID, gap)
	if err != nil {
		t.Fatalf("read seeded mark: %v", err)
	}
	rec.ClaimedAt = substrate.FormatTimestamp(time.Now())
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal mark: %v", err)
	}
	if _, err := h.conn.KVPut(ctx, "weaver-state", key, body); err != nil {
		t.Fatalf("refresh ClaimedAt: %v", err)
	}
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("read back mark: %v", err)
	}

	h.pass(ctx)

	h.requireNoOp(t)
	after, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil || after.Revision != entry.Revision {
		t.Fatalf("a paced reclaim must leave the mark untouched (rev %d → %v, err=%v)", entry.Revision, after.Revision, err)
	}
	if _, suppressed, _, _, _ := h.engine.sweep.metrics(); suppressed != 1 {
		t.Fatalf("reclaimsSuppressed = %d, want 1 — an uncapped external gap must be paced, never left unbounded and unpaced", suppressed)
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

	const targetID = "fixtureControLMarker"
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

// TestSweep_DisabledTargetExpiredMarkNotReclaimed proves the freeze gate
// sweepMark consults ahead of reclaim (isTargetDisabled, reconciler.go): a
// disabled target's expired-lease mark over a STILL-VIOLATING gap is left
// standing — no reclaim, no op fires, the sweepReclaims metric stays at 0 —
// while a DIFFERENT mark on the very same disabled target whose gap has
// already closed still clears via the unconditional level-reconciled pass
// (disabling suppresses only re-dispatch, never the bookkeeping clears —
// docs/components/weaver.md "Dispatch-skip marker and in-memory cache").
func TestSweep_DisabledTargetExpiredMarkNotReclaimed(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureDisabledReclaim"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	// Mirrors Engine.Disable's in-memory half directly (the harness never runs
	// Start/seedDisabledTargets) — isTargetDisabled reads only this set.
	h.engine.disabled.set(targetID, true)

	// (1) Still violating, expired lease: must NOT be reclaimed while disabled.
	openEntity := testNanoID(t)
	openKey := markKey(targetID, openEntity, "missing_x")
	openRev := h.putMark(t, ctx, openKey, fixtureMark(targetID, openEntity, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, openEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + openEntity, "violating": true, "missing_x": true,
	})

	// (2) Gap already closed, expired lease: must still clear (bookkeeping
	// runs regardless of the freeze — only re-dispatch is gated).
	closedEntity := testNanoID(t)
	closedKey := markKey(targetID, closedEntity, "missing_x")
	h.putMark(t, ctx, closedKey, fixtureMark(targetID, closedEntity, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, closedEntity, map[string]any{
		"entityKey": "vtx.leaseApp." + closedEntity, "violating": false, "missing_x": false,
	})

	h.pass(ctx)

	entry, err := h.conn.KVGet(ctx, "weaver-state", openKey)
	if err != nil || entry.Revision != openRev {
		t.Fatalf("a disabled target's expired-lease mark over a still-open gap must be left untouched (err=%v)", err)
	}
	if h.markExists(t, ctx, closedKey) {
		t.Fatalf("a disabled target's gap-closed mark must still clear (cleanup, not new dispatch)")
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 0 {
		t.Fatalf("sweepReclaims = %d, want 0 (the target is disabled)", reclaims)
	}
	h.requireNoOp(t)

	// Enabling the target resumes reclaim for whatever is still violating —
	// nothing about the mark is lost across the freeze.
	h.engine.disabled.set(targetID, false)
	h.pass(ctx)
	h.nextOp(t)
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 1 {
		t.Fatalf("sweepReclaims = %d, want 1 (re-enabling must resume the deferred reclaim)", reclaims)
	}
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

	const targetID = "fixtureinfLightSweep"
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

// TestSweep_DirectOpDefaultBudgetEscalates proves the default retry bound
// end-to-end: a "directOp" gap whose row declares NEITHER
// maxretries_<g> NOR inflight_<g> — orphanedTaskGrants/ReleaseOrphanedBooking's
// exact shape — reclaim-dispatches up to defaultDirectOpRetryBudget times
// (standing in for "boot + reclaims": each loop iteration is a genuine fresh
// dispatch that bumps the chain's dispatch-count, mirroring
// TestSweep_ReclaimIncrementsBudget) and then, instead of a 4th
// reclaim-dispatch, the sweep raises the standing GapBudgetExhausted Health
// issue — naming the engine default so an operator can tell it apart from a
// declared cap — and stops dispatching.
func TestSweep_DirectOpDefaultBudgetEscalates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureDirectOpDefault"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	// No maxretries_x, no inflight_x declared.
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	for i := 0; i < defaultDirectOpRetryBudget; i++ {
		h.pass(ctx)
		h.nextOp(t)
		h.reexpireMark(t, ctx, key)
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != defaultDirectOpRetryBudget {
		t.Fatalf("dispatch-count after %d reclaims = %d (err=%v), want %d", defaultDirectOpRetryBudget, got, err, defaultDirectOpRetryBudget)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
		t.Fatalf("must not exhaust before the engine default %d is reached", defaultDirectOpRetryBudget)
	}

	// The next pass: the engine-default budget is spent. Escalate; do not
	// reclaim-dispatch a 4th time.
	h.pass(ctx)
	h.requireNoOp(t)

	issues := h.engine.issues.snapshot()
	if sev := issueSeverity(issues, "GapBudgetExhausted"); sev != "warning" {
		t.Fatalf("expected a GapBudgetExhausted warning issue, got severity %q (issues: %+v)", sev, issues)
	}
	var msg string
	for _, iss := range issues {
		if iss.Code == "GapBudgetExhausted" {
			msg = iss.Message
		}
	}
	if !strings.Contains(msg, "engine's default retry budget") {
		t.Fatalf("the GapBudgetExhausted message must name the engine default so an operator can tell it from a declared cap, got %q", msg)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != defaultDirectOpRetryBudget {
		t.Fatalf("sweepReclaims = %d, want %d (no 4th reclaim)", reclaims, defaultDirectOpRetryBudget)
	}
}

// TestSweep_DeclaredMaxRetriesKeepsOwnBudget proves the row's own
// maxretries_<g> always wins over defaultDirectOpRetryBudget when present,
// even when the declared cap is LARGER than the engine default: a directOp
// gap declaring maxretries_x above defaultDirectOpRetryBudget must still
// reclaim PAST the engine default's own threshold — proving the default never
// leaks in behind a declared cap — and only escalates once its OWN declared
// cap is reached.
func TestSweep_DeclaredMaxRetriesKeepsOwnBudget(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureDeclaredCapBeatsDefault"
	const cap = defaultDirectOpRetryBudget + 2 // strictly ABOVE the engine default
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
		"maxretries_x": cap,
	})

	// Drive every reclaim through the DECLARED cap, including past the engine
	// default's own (smaller) threshold: each of these must still reclaim —
	// proving the declared column governs, never silently overridden.
	for i := 0; i < cap; i++ {
		h.pass(ctx)
		h.nextOp(t)
		if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
			t.Fatalf("must not exhaust before the DECLARED cap %d (reclaim %d)", cap, i+1)
		}
		h.reexpireMark(t, ctx, key)
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != cap {
		t.Fatalf("dispatch-count = %d (err=%v), want the declared cap %d", got, err, cap)
	}

	// The NEXT pass: the declared cap is spent — escalate, no reclaim.
	h.pass(ctx)
	h.requireNoOp(t)
	if sev := issueSeverity(h.engine.issues.snapshot(), "GapBudgetExhausted"); sev != "warning" {
		t.Fatalf("expected GapBudgetExhausted once the DECLARED cap %d is reached, got severity %q", cap, sev)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != cap {
		t.Fatalf("sweepReclaims = %d, want exactly the declared cap %d (no extra reclaim)", reclaims, cap)
	}
}

// TestSweep_UserTaskReclaimNeverCappedByEngineDefault proves
// defaultDirectOpRetryBudget is consulted ONLY for a "directOp" gap: a
// triggerLoom gap declaring NEITHER maxretries_<g> NOR inflight_<g> — the
// same "declares nothing" row shape as an uncapped directOp target — keeps
// reclaiming PAST the engine default's own threshold with no
// GapBudgetExhausted escalation. assignTask/triggerLoom rely solely on the
// Option-D backoff-only pacing (reconciler.go's collapseOnlyReclaim) for
// their repeat-reclaim hygiene, never a hard retry cap.
func TestSweep_UserTaskReclaimNeverCappedByEngineDefault(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	// A negligible backoff base: this test isolates the CAP behavior, not the
	// backoff pacing (already proven by reclaim_backoff_internal_test.go) —
	// reexpireMark pushes ClaimedAt back 2h every iteration regardless, but a
	// tiny base keeps the (irrelevant here) backoff interval far below that.
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Millisecond })
	h.agePastWarmup()

	const targetID = "fixtureUserTaskUncapped"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"}},
	})
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta["ghostFlow"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "triggerLoom", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	const attempts = defaultDirectOpRetryBudget + 2
	for i := 0; i < attempts; i++ {
		h.pass(ctx)
		h.nextOp(t)
		h.reexpireMark(t, ctx, key)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
		t.Fatalf("a triggerLoom gap must never be capped by defaultDirectOpRetryBudget")
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != attempts {
		t.Fatalf("dispatch-count = %d (err=%v), want %d (every reclaim above must have fired, uncapped)", got, err, attempts)
	}
	if reclaims, _, _, _, _ := h.engine.sweep.metrics(); reclaims != attempts {
		t.Fatalf("sweepReclaims = %d, want %d", reclaims, attempts)
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

	const targetID = "fixtureRecLaimBudget"
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

	const targetID = "fixtureRecLaimEffect"
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

	const targetID = "fixtureorphanNoCLose"
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

// TestSweep_GapClosedMarkRetiresStandingIssue proves the sweep's
// level-reconciled gap-close (the row is gone from weaver-targets — deleted,
// or never projected) retires the gap's standing issue along with the mark:
// for a row with no further CDC deliveries the sweep is the only leg that
// will ever observe the close, so without this the issue stands until a
// process restart.
func TestSweep_GapClosedMarkRetiresStandingIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureIssueRetire"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", pastLease()))
	h.engine.issues.set(issueKeyGapEntity(targetID, entityID, "missing_x"), "warning", "GapBudgetExhausted",
		"target "+targetID+" entity "+entityID+": row column missing_x has exhausted the engine's default retry budget")
	// No weaver-targets row seeded: the row-gone branch is the close.

	h.pass(ctx)
	h.requireNoOp(t)

	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil || found {
		t.Fatalf("gap-closed mark must be deleted (found=%v err=%v)", found, err)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
		t.Fatal("a gap-closed mark delete must retire the gap's standing issue")
	}
}

// countExists reports whether a `…__count` retry-budget dispatch-count key is
// present in weaver-state. Presence only — see countValue where the test's
// claim is about the budget's magnitude.
func (h *sweepHarness) countExists(t *testing.T, ctx context.Context, targetID, entityID, gapColumn string) bool {
	t.Helper()
	_, err := h.conn.KVGet(ctx, "weaver-state", countKey(targetID, entityID, gapColumn))
	if err != nil && !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("count read %q: %v", countKey(targetID, entityID, gapColumn), err)
	}
	return err == nil
}

// countValue reads the gap's current dispatch-count. A test asserting that a
// leg did not SPEND the budget must compare this, not countExists: a dispatch
// bumps the count and leaves the key exactly where it was, so presence alone
// cannot tell an untouched budget from a consumed one.
func (h *sweepHarness) countValue(t *testing.T, ctx context.Context, targetID, entityID, gapColumn string) int {
	t.Helper()
	n, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, gapColumn)
	if err != nil {
		t.Fatalf("dispatch-count read: %v", err)
	}
	return n
}

// seedCount drives the gap's dispatch-count to n through the real increment
// path, so the seeded budget carries the same value shape and TTL a chain of n
// genuine dispatches leaves behind.
func (h *sweepHarness) seedCount(t *testing.T, ctx context.Context, targetID, entityID, gapColumn string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, gapColumn); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
}

// deleteRow removes an entity's weaver-targets row — the entity tombstoned, its
// Lens re-projected without it, or a lens rebuild mid-purge.
func (h *sweepHarness) deleteRow(t *testing.T, ctx context.Context, targetID, entityID string) {
	t.Helper()
	if err := h.conn.KVDelete(ctx, "weaver-targets", targetID+"."+entityID); err != nil {
		t.Fatalf("delete row: %v", err)
	}
}

// seedIssue plants a standing per-entity gap issue, so a test whose claim is
// about a CLEAR has something to retire that it did not have to raise first.
func (h *sweepHarness) seedIssue(targetID, entityID, gapColumn string) {
	h.engine.issues.set(issueKeyGapEntity(targetID, entityID, gapColumn), "warning", "GapBudgetExhausted",
		"target "+targetID+" entity "+entityID+": row column "+gapColumn+" has exhausted its declared retry budget")
}

// restart replaces the harness's engine with a FRESH one over the SAME NATS
// connection and the same weaver-state/weaver-targets buckets: a new
// issueCache (empty — the cache is process-local, health.go), a new sweeper,
// and an empty registry. That is the shape a Weaver process has moments after a
// restart, so anything held only in the old process's memory is gone while
// every durable key survives. The caller re-seeds the registry, standing in for
// the meta.weaverTarget replay.
func (h *sweepHarness) restart(t *testing.T) {
	t.Helper()
	h.engine = NewEngine(h.conn, h.engine.cfg)
}

// logCapture records every log record the engine emits, AT EVERY LEVEL. Level
// is deliberately not part of the filter: e.alert logs a fact's arrival at
// Error and a re-raise of the same standing fact at Debug, so a counter that
// looked only at Error would silently stop counting the very repeats a
// "how many times did this happen" assertion exists to catch.
type logCapture struct {
	mu   sync.Mutex
	recs []capturedRecord
}

type capturedRecord struct {
	level   slog.Level
	message string
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.recs = append(c.recs, capturedRecord{level: r.Level, message: r.Message})
	c.mu.Unlock()
	return nil
}

func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(string) slog.Handler      { return c }

// countContaining reports how many captured records mention sub — used with an
// entityId to attribute records to one subject.
func (c *logCapture) countContaining(sub string) int {
	return len(c.levelsContaining(sub))
}

// levelsContaining returns the level of every captured record mentioning sub,
// in emission order.
func (c *logCapture) levelsContaining(sub string) []slog.Level {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []slog.Level
	for _, r := range c.recs {
		if strings.Contains(r.message, sub) {
			out = append(out, r.level)
		}
	}
	return out
}

// exhaustedRow is the §10.2 row shape every count-leg vector starts from: a
// violating entity whose gap is open, whose retry cap is declared, and which
// declares no in-flight remediation. Callers mutate the one column their vector
// is about, so a negative vector differs from its positive control in exactly
// one field.
func exhaustedRow(entityID, gapColumn string, budget int) map[string]any {
	g := strings.TrimPrefix(gapColumn, gapColumnPrefix)
	return map[string]any{
		"entityKey":                "vtx.leaseApp." + entityID,
		"violating":                true,
		gapColumn:                  true,
		inflightColumnPrefix + g:   false,
		maxretriesColumnPrefix + g: budget,
	}
}

// TestSweep_CountLegEscalatesExhaustedGapWithNoMark is the core of the
// count-anchored sweep leg. An exhausted gap stops refreshing its mark (the
// exhausted branch deliberately never reclaims), so once that mark's TTL
// expires the only durable trace of the suppression is the `…__count` retry
// budget — which outlives the mark by two orders of magnitude and keeps the gap
// from dispatching the whole time. The count leg is what re-derives Contract
// #10 §10.8's loud stop from it: one pass over a violating, still-open,
// budget-spent, markless gap raises GapBudgetExhausted on the
// per-(target, entity, gap) latch, dispatches nothing, and leaves the budget at
// exactly the value it found (the suppression is unchanged — only its silence
// is fixed).
func TestSweep_CountLegEscalatesExhaustedGapWithNoMark(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegRaise"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))
	if h.markExists(t, ctx, markKey(targetID, entityID, "missing_x")) {
		t.Fatal("setup: this vector requires a markless gap")
	}

	h.pass(ctx)

	issue, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_x"))
	if !ok {
		t.Fatalf("a markless exhausted gap must still raise its §10.8 loud stop (issues: %+v)",
			h.engine.issues.snapshot())
	}
	if issue.Code != "GapBudgetExhausted" || issue.Severity != "warning" {
		t.Fatalf("issue = %q/%q, want GapBudgetExhausted/warning", issue.Code, issue.Severity)
	}
	if got := h.countValue(t, ctx, targetID, entityID, "missing_x"); got != budget {
		t.Fatalf("dispatch-count = %d, want %d: raising the alert must not spend the budget it reports on", got, budget)
	}
	if h.markExists(t, ctx, markKey(targetID, entityID, "missing_x")) {
		t.Fatal("an un-escalated exhausted gap must not be dispatched by the count leg")
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegReRaisesExhaustedGapAfterRestart pins the durability the
// leg exists for. The GapBudgetExhausted issue lives in the process-local
// issueCache; the suppression that causes it lives in weaver-state. A fresh
// engine over the same buckets — empty cache, empty registry, every durable key
// intact — must re-derive the issue from the count alone, so the loud stop
// stands for as long as the park it explains.
func TestSweep_CountLegReRaisesExhaustedGapAfterRestart(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegRestart"
	const budget = 2
	target := &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	}
	h.seedTarget(target)
	entityID := testNanoID(t)
	issueKey := issueKeyGapEntity(targetID, entityID, "missing_x")
	h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))

	h.pass(ctx)
	if _, ok := issueAt(h.engine.issues, issueKey); !ok {
		t.Fatal("setup: the first pass must raise the standing issue")
	}

	h.restart(t)
	h.agePastWarmup()
	h.seedTarget(target)
	if _, ok := issueAt(h.engine.issues, issueKey); ok {
		t.Fatal("setup: a restarted engine must start with an empty issueCache")
	}

	h.pass(ctx)

	issue, ok := issueAt(h.engine.issues, issueKey)
	if !ok {
		t.Fatalf("the loud stop must be re-derivable from the durable count after a restart (issues: %+v)",
			h.engine.issues.snapshot())
	}
	if issue.Code != "GapBudgetExhausted" {
		t.Fatalf("re-raised issue code = %q, want GapBudgetExhausted", issue.Code)
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegEscalatesToAugurWithNoMark covers the count leg's DISPATCH
// branch: where the target's augur block escalates "exhausted", the leg does
// not merely alert — it fires a real CreateAugurReasoningClaim episode for a
// gap that has no mark and no fresh CDC delivery, and retires the standing
// warning in favour of that escalation. The fresh episode is a genuine
// dispatch, so it bumps the chain's count exactly once; anything more would
// mean the leg double-counted an attempt it made once.
func TestSweep_CountLegEscalatesToAugurWithNoMark(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegAugur"
	const budget = 2
	id := testNanoID(t)
	spec := targetSpecFixture(targetID) // gaps.missing_a -> directOp FixA
	spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
	h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
	h.engine.source.handle(specEvent(t, id, spec))

	entityID := testNanoID(t)
	h.seedCount(t, ctx, targetID, entityID, "missing_a", budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_a", budget))

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != defaultAugurOp {
		t.Fatalf("operationType = %v, want %q (the escalation the count leg dispatched)",
			op["operationType"], defaultAugurOp)
	}
	if !h.markExists(t, ctx, markKey(targetID, entityID, "missing_a")) {
		t.Fatal("the escalation episode must hold the gap's mark (its own anti-storm claim)")
	}
	if got, want := h.countValue(t, ctx, targetID, entityID, "missing_a"), budget+1; got != want {
		t.Fatalf("dispatch-count = %d, want %d: the escalation is ONE fresh dispatch, counted once", got, want)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
		t.Fatal("an escalated gap must not also hold the un-escalated standing warning")
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegDefersToTheMarkLeg pins WHERE the mark-listed guard sits:
// below the level reconcile (which must run for every key, gated by nothing)
// and above every arm that escalates. Two entities, one pass, one target:
//
//   - held: a LIVE mark. The mark leg leaves an in-flight episode alone and
//     never escalates it, so if the count leg escalated over it the gap would be
//     escalated while its remediation is still running — the guard's whole
//     point. Nothing may be raised for this entity at all, which is an
//     assertion no logging or severity choice can hollow out.
//   - stale: an expired mark. Both legs reach the same exhausted gap, and the
//     escalation site must be reached exactly ONCE — counted over records at
//     every level, since a repeat raise logs at Debug rather than Error.
func TestSweep_CountLegDefersToTheMarkLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	logs := &logCapture{}
	h := newSweepHarness(t, ctx, func(cfg *Config) { cfg.Logger = slog.New(logs) })
	h.agePastWarmup()

	const targetID = "fixtureCountLegDefers"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	held, stale := testNanoID(t), testNanoID(t)
	h.putMark(t, ctx, markKey(targetID, held, "missing_x"),
		fixtureMark(targetID, held, "missing_x", "directOp", futureLease()))
	h.putMark(t, ctx, markKey(targetID, stale, "missing_x"),
		fixtureMark(targetID, stale, "missing_x", "directOp", pastLease()))
	for _, entityID := range []string{held, stale} {
		h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
		h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))
	}

	h.pass(ctx)

	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, held, "missing_x")); ok {
		t.Fatal("a gap whose episode is still in flight must not be escalated by the count leg")
	}
	if got := logs.countContaining("entity " + held); got != 0 {
		t.Fatalf("records naming the in-flight entity = %d, want 0", got)
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, stale, "missing_x")); !ok {
		t.Fatal("setup: the expired-mark vector must escalate once, via the mark leg")
	}
	if got := logs.countContaining("entity " + stale); got != 1 {
		t.Fatalf("escalations of one gap in one pass = %d, want exactly 1 (the mark leg's)", got)
	}
}

// TestSweep_CountLegLevelReconcilesAboveTheGates pins the arm ORDER against
// sweepMark's: the level reconcile runs for every count key, gated by nothing,
// because retiring a satisfied gap's bookkeeping is cleanup and never new
// dispatch. Both vectors have a PRESENT row whose gap column reads false — the
// positive evidence that the chain the budget bounded has ended — and both are
// behind a gate that stops the acting arms:
//
//   - a target frozen by the operator `__control` marker. If the freeze gated
//     the reconcile, the spent budget and its standing issue would survive the
//     whole freeze, and the moment the operator re-enabled the target a
//     re-opened gap would be suppressed against a budget spent by a chain that
//     already closed.
//   - a target absent from the registry (a replay that has not landed). Same
//     stranding, with no operator action to notice it.
func TestSweep_CountLegLevelReconcilesAboveTheGates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const frozen = "fixtureCountLegFrozenClose"
	const unknown = "fixtureCountLegUnknownClose"
	h.seedTarget(&Target{
		TargetID: frozen,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	if err := h.engine.marks.setDisabled(ctx, frozen, true); err != nil {
		t.Fatalf("setDisabled: %v", err)
	}
	if err := h.engine.seedDisabledTargets(ctx); err != nil {
		t.Fatalf("seedDisabledTargets: %v", err)
	}
	closedRow := func(entityID string) map[string]any {
		row := exhaustedRow(entityID, "missing_x", 2)
		row["violating"], row["missing_x"] = false, false
		return row
	}
	frozenEntity, unknownEntity := testNanoID(t), testNanoID(t)
	for targetID, entityID := range map[string]string{frozen: frozenEntity, unknown: unknownEntity} {
		h.seedCount(t, ctx, targetID, entityID, "missing_x", 2)
		h.putRow(t, ctx, targetID, entityID, closedRow(entityID))
		h.seedIssue(targetID, entityID, "missing_x")
	}

	h.pass(ctx)

	for targetID, entityID := range map[string]string{frozen: frozenEntity, unknown: unknownEntity} {
		if h.countExists(t, ctx, targetID, entityID, "missing_x") {
			t.Fatalf("%s: a closed gap's budget must be reset whatever gate stands over the target", targetID)
		}
		if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_x")); ok {
			t.Fatalf("%s: the close must retire the standing issue whatever gate stands over the target", targetID)
		}
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegOnlyEscalatesForARegisteredTarget pins the registry gate on
// the ACTING arms. "Not installed" reads identically for a target that was
// removed and one whose meta.weaverTarget replay has not landed yet, and an
// escalation is a dispatch — so a gap the engine cannot even describe must not
// be escalated on the strength of a key left in a bucket. The positive vector
// runs first, in the same pass on an identical row and an identical budget, so
// the negative pins the REGISTRATION and not an inert leg.
func TestSweep_CountLegOnlyEscalatesForARegisteredTarget(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const registered = "fixtureCountLegRegistered"
	const unregistered = "fixtureCountLegUnknown"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: registered,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	live, ghost := testNanoID(t), testNanoID(t)
	for targetID, entityID := range map[string]string{registered: live, unregistered: ghost} {
		h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
		h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))
	}

	h.pass(ctx)

	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(registered, live, "missing_x")); !ok {
		t.Fatalf("a registered target's exhausted gap must escalate (issues: %+v)", h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(unregistered, ghost, "missing_x")); ok {
		t.Fatal("an unregistered target must not be escalated: replay lag reads exactly like removal")
	}
	if got := h.countValue(t, ctx, unregistered, ghost, "missing_x"); got != budget {
		t.Fatalf("unregistered target's dispatch-count = %d, want %d (the bound itself is never destroyed here)", got, budget)
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegSkipsDisabledTarget proves the freeze gate reaches the
// count leg's acting arms: an escalation is a dispatch (on an augur target it
// fires a real op), and an operator freeze stops new dispatch — the gate
// sweepMark's reclaim and lane-1's Ack-skip already honour. The enabled target
// in the same pass carries the identical row, gap and spent budget, so the
// negative proves the FREEZE rather than a vector that could never escalate.
func TestSweep_CountLegSkipsDisabledTarget(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const frozen = "fixtureCountLegDisabled"
	const active = "fixtureCountLegEnabled"
	const budget = 2
	for _, targetID := range []string{frozen, active} {
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
		})
	}
	if err := h.engine.marks.setDisabled(ctx, frozen, true); err != nil {
		t.Fatalf("setDisabled: %v", err)
	}
	// The durable `__control` marker is the authority; the in-memory set the
	// gate reads is rebuilt from it exactly as a restart rebuilds it.
	if err := h.engine.seedDisabledTargets(ctx); err != nil {
		t.Fatalf("seedDisabledTargets: %v", err)
	}
	frozenEntity, activeEntity := testNanoID(t), testNanoID(t)
	for targetID, entityID := range map[string]string{frozen: frozenEntity, active: activeEntity} {
		h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
		h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))
	}

	h.pass(ctx)

	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(active, activeEntity, "missing_x")); !ok {
		t.Fatalf("the same vector on an ENABLED target must escalate (issues: %+v)", h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(frozen, frozenEntity, "missing_x")); ok {
		t.Fatal("a frozen target must not escalate: escalation is dispatch")
	}
	if got := h.countValue(t, ctx, frozen, frozenEntity, "missing_x"); got != budget {
		t.Fatalf("frozen target's dispatch-count = %d, want %d (a freeze touches nothing)", got, budget)
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegNeverEscalatesAnOrphanColumn pins the orphan-column arm. A
// column the playbook no longer names is a gap Weaver does not manage: there is
// no remediation to bound, so the budget bounds nothing and the standing issue
// describes nothing an operator can act on — and arm 7 can never retire it,
// because an orphaned column stays TRUE in the row. Escalating instead would
// not even terminate on an augur-escalating target: the escalation re-creates
// the gap's mark and re-arms its count, the mark leg's own orphan arm deletes
// that mark, and the next pass escalates again — a real CreateAugurReasoningClaim
// every sweep interval, forever. So: retire the issue, dispatch nothing, and
// leave the budget for its TTL (a partially replayed target carries an
// intermediate definition that may not yet name the gap).
func TestSweep_CountLegNeverEscalatesAnOrphanColumn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegOrphanColumn"
	const budget = 2
	id := testNanoID(t)
	spec := targetSpecFixture(targetID) // declares gaps.missing_a ONLY
	spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
	h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
	h.engine.source.handle(specEvent(t, id, spec))

	entityID := testNanoID(t)
	// missing_dropped is a live, violating, budget-spent gap column that the
	// playbook does not name — the shape a package re-author leaves behind.
	h.seedCount(t, ctx, targetID, entityID, "missing_dropped", budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_dropped", budget))
	h.seedIssue(targetID, entityID, "missing_dropped")

	h.pass(ctx)
	h.pass(ctx)

	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_dropped")); ok {
		t.Fatal("an orphan column's standing issue must be retired: nothing manages that gap any more")
	}
	if h.markExists(t, ctx, markKey(targetID, entityID, "missing_dropped")) {
		t.Fatal("an orphan column must never be dispatched — that is the non-terminating escalation loop")
	}
	if got := h.countValue(t, ctx, targetID, entityID, "missing_dropped"); got != budget {
		t.Fatalf("dispatch-count = %d, want %d (an intermediate replay definition is not evidence)", got, budget)
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegKeepsTheBudgetWhenTheRowIsGone pins absence-is-not-evidence
// on the one key that IS the retry bound. A Refractor lens rebuild purges a
// target's rows and re-replays them; inside that window a registered, enabled
// target reads row-gone for every entity it has. Deleting the budgets there
// would re-arm exactly the storm defaultDirectOpRetryBudget exists to prevent,
// and would buy only prompt collection of a key the 128h TTL already collects.
// The standing ISSUE is retired — it states a fact about a row that is not
// there — and the next pass re-raises it if the row returns still exhausted.
func TestSweep_CountLegKeepsTheBudgetWhenTheRowIsGone(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegRowGone"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	issueKey := issueKeyGapEntity(targetID, entityID, "missing_x")
	h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))

	h.pass(ctx)
	if _, ok := issueAt(h.engine.issues, issueKey); !ok {
		t.Fatal("setup: the first pass must raise the standing issue")
	}

	// The row vanishes (a tombstone, or a lens rebuild mid-purge).
	h.deleteRow(t, ctx, targetID, entityID)
	h.pass(ctx)

	if got := h.countValue(t, ctx, targetID, entityID, "missing_x"); got != budget {
		t.Fatalf("dispatch-count = %d, want %d: a missing row is not evidence, and this key is the retry bound", got, budget)
	}
	if _, ok := issueAt(h.engine.issues, issueKey); ok {
		t.Fatal("the issue names a row that is not there and must be retired")
	}

	// The row comes back still exhausted: the loud stop returns with it.
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))
	h.pass(ctx)
	if _, ok := issueAt(h.engine.issues, issueKey); !ok {
		t.Fatal("a returning row with a spent budget must re-raise the loud stop")
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegClearsBudgetAndIssueWhenTheGapCloses is the counterpart to
// the row-gone vector, and the contrast is the whole rule: a PRESENT row whose
// gap column reads false is positive evidence that the chain this budget
// bounded has ended, so here the count IS deleted — otherwise it stands for its
// whole TTL and suppresses the next chain the moment the gap re-opens, while
// the standing issue keeps naming a gap that closed. The delete is visible on
// the sweep's orphan metric, like every other key the sweep removes.
func TestSweep_CountLegClearsBudgetAndIssueWhenTheGapCloses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegGapClosed"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	issueKey := issueKeyGapEntity(targetID, entityID, "missing_x")
	h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))

	h.pass(ctx)
	if _, ok := issueAt(h.engine.issues, issueKey); !ok {
		t.Fatal("setup: the first pass must raise the standing issue")
	}

	closed := exhaustedRow(entityID, "missing_x", budget)
	closed["violating"], closed["missing_x"] = false, false
	h.putRow(t, ctx, targetID, entityID, closed)
	h.pass(ctx)

	if h.countExists(t, ctx, targetID, entityID, "missing_x") {
		t.Fatal("a closed gap's retry budget must be reset by the count leg")
	}
	if _, ok := issueAt(h.engine.issues, issueKey); ok {
		t.Fatal("the raise must be retired by the same read that observes the close")
	}
	if _, _, orphans, _, _ := h.engine.sweep.metrics(); orphans != 1 {
		t.Fatalf("sweepOrphansDeleted = %d, want 1: a budget the sweep deletes must be visible on the heartbeat", orphans)
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegLeavesBudgetOnUnparseableRow pins never-act-on-unreadable-
// evidence for the budget. Both entities carry a directOp gap with no declared
// cap and a count already at defaultDirectOpRetryBudget, so a readable row
// escalates on the engine default — which is exactly what the control entity
// does in the same pass. The vector under test differs only in that its row
// body is not JSON: the leg must neither escalate on it nor reset the budget.
func TestSweep_CountLegLeavesBudgetOnUnparseableRow(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegBadRow"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	readable, garbled := testNanoID(t), testNanoID(t)
	for _, entityID := range []string{readable, garbled} {
		h.seedCount(t, ctx, targetID, entityID, "missing_x", defaultDirectOpRetryBudget)
	}
	// No maxretries_x on either row: the engine default is the cap in play.
	h.putRow(t, ctx, targetID, readable, map[string]any{
		"entityKey": "vtx.leaseApp." + readable, "violating": true, "missing_x": true,
	})
	if _, err := h.conn.KVPut(ctx, "weaver-targets", targetID+"."+garbled, []byte("{not json")); err != nil {
		t.Fatalf("put bad row: %v", err)
	}

	h.pass(ctx)

	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, readable, "missing_x")); !ok {
		t.Fatalf("the same vector with a READABLE row must escalate on the engine default (issues: %+v)",
			h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, garbled, "missing_x")); ok {
		t.Fatal("an unparseable row must never drive an escalation")
	}
	if got := h.countValue(t, ctx, targetID, garbled, "missing_x"); got != defaultDirectOpRetryBudget {
		t.Fatalf("dispatch-count = %d, want %d: an unparseable row must not touch the budget",
			got, defaultDirectOpRetryBudget)
	}
	h.requireNoOp(t)
}

// TestSweep_CountLegDeletesACorruptBudgetBody proves the count leg deletes a
// retry budget whose BODY no reader can parse, says so loudly, and retires the
// gap's standing issue with it (the leg reaches that gap only through the key
// it is removing, so a latch left behind would never be revisited). This is not
// cosmetic parity with the mark and effect legs: every read of a garbled count
// errors, gapSuppressed's read-failure posture is deliberately the dispatchable
// side, and incrementDispatchCount's read-modify-write fails identically — so
// the budget can never accumulate and a directOp gap retries with no cap at
// all. The delete re-arms from 0 (what the garbled body already yields) and
// leaves a key that can count again — and once it does, the CorruptMark issue
// is retired, because a count key's name comes back by construction and the
// listing-based retirement never fires for one.
func TestSweep_CountLegDeletesACorruptBudgetBody(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegCorrupt"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	if _, err := h.conn.KVPut(ctx, "weaver-state", countKey(targetID, entityID, "missing_x"), []byte("{not json")); err != nil {
		t.Fatalf("put corrupt count: %v", err)
	}
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", 2))
	h.seedIssue(targetID, entityID, "missing_x")

	h.pass(ctx)

	if h.countExists(t, ctx, targetID, entityID, "missing_x") {
		t.Fatal("a retry budget whose body cannot be parsed must be deleted: it can never accumulate")
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("the delete must be loud (issues: %+v)", h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_x")); ok {
		t.Fatal("deleting the key the leg navigates by must retire that gap's standing issue with it")
	}
	if _, _, _, corrupt, _ := h.engine.sweep.metrics(); corrupt != 1 {
		t.Fatalf("sweepCorrupt = %d, want 1", corrupt)
	}

	// The next dispatch recreates the same key with a countable body: the
	// CorruptMark issue is about the value that was deleted, not the name.
	h.seedCount(t, ctx, targetID, entityID, "missing_x", 1)
	h.pass(ctx)
	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatal("a key that reads and parses cleanly must retire its CorruptMark issue")
	}
}

// TestSweep_CountLegLeavesACorruptBodyAloneBehindTheGates pins WHERE the
// corrupt-body delete sits: below the registry and freeze gates, because it
// destroys durable state and those gates exist to stop exactly that during
// replay lag or under an operator freeze. The realistic trigger is a rolling
// upgrade writing a body an older build cannot parse — shredding every budget
// of a target the registry has not replayed yet would be the worst possible
// response to it. The corrupt-KEY delete is different and stays ungated: an
// unsplittable key names no target, so no gate can decide it.
func TestSweep_CountLegLeavesACorruptBodyAloneBehindTheGates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const unregistered = "fixtureCountLegCorruptUnknown"
	const frozen = "fixtureCountLegCorruptFrozen"
	h.seedTarget(&Target{
		TargetID: frozen,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	if err := h.engine.marks.setDisabled(ctx, frozen, true); err != nil {
		t.Fatalf("setDisabled: %v", err)
	}
	if err := h.engine.seedDisabledTargets(ctx); err != nil {
		t.Fatalf("seedDisabledTargets: %v", err)
	}
	ghost, frozenEntity := testNanoID(t), testNanoID(t)
	for targetID, entityID := range map[string]string{unregistered: ghost, frozen: frozenEntity} {
		if _, err := h.conn.KVPut(ctx, "weaver-state", countKey(targetID, entityID, "missing_x"), []byte("{not json")); err != nil {
			t.Fatalf("put corrupt count: %v", err)
		}
		h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", 2))
	}
	// A key no split can attribute to any target: nothing gates this one.
	unattributable := "fixtureCountLegCorruptKey.__count"
	if _, err := h.conn.KVCreate(ctx, "weaver-state", unattributable, []byte(`{"count":1}`)); err != nil {
		t.Fatalf("create corrupt-key count: %v", err)
	}

	h.pass(ctx)

	if !h.countExists(t, ctx, unregistered, ghost, "missing_x") {
		t.Fatal("an unregistered target's budget must survive an unparseable body: replay lag is not corruption")
	}
	if !h.countExists(t, ctx, frozen, frozenEntity, "missing_x") {
		t.Fatal("a frozen target's budget must survive an unparseable body: a freeze forbids destroying its state")
	}
	if h.markExists(t, ctx, unattributable) {
		t.Fatal("a count key that names no target can be gated by nothing and must be deleted")
	}
	if _, _, _, corrupt, _ := h.engine.sweep.metrics(); corrupt != 1 {
		t.Fatalf("sweepCorrupt = %d, want 1 (the unattributable key only)", corrupt)
	}
}

// TestSweep_CountLegLeavesBudgetOnRowWithNoEntityKey pins the
// incomplete-evidence gate. escalateExhaustedGap feeds entityKey to planGap so
// the escalation can name its candidate, and lane 1 declines to dispatch a
// violating row that carries no §10.2 entityKey echo at all — the mark leg
// reaches that call only past the reclaim's own non-empty guard, and this leg
// has no mark to have been guarded. The control entity carries the identical
// row WITH the echo, in the same pass, so the negative pins the echo.
func TestSweep_CountLegLeavesBudgetOnRowWithNoEntityKey(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCountLegNoEntityKey"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	echoed, silent := testNanoID(t), testNanoID(t)
	for _, entityID := range []string{echoed, silent} {
		h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
	}
	h.putRow(t, ctx, targetID, echoed, exhaustedRow(echoed, "missing_x", budget))
	silentRow := exhaustedRow(silent, "missing_x", budget)
	delete(silentRow, "entityKey")
	h.putRow(t, ctx, targetID, silent, silentRow)

	h.pass(ctx)

	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, echoed, "missing_x")); !ok {
		t.Fatalf("the same vector WITH an entityKey must escalate (issues: %+v)", h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, silent, "missing_x")); ok {
		t.Fatal("a violating row with no entityKey echo must not drive an escalation the plan cannot name")
	}
	h.requireNoOp(t)
}

// TestSweep_CorruptMarkIssueRetiresWhenTheMarkKeyComesBack pins the retirement
// route pass's listing check cannot reach. A mark key's NAME recurs by
// construction: the gap stays open, so the next episode CAS-creates a mark at
// the very key the corrupt one was deleted from, and from then on every listing
// contains it. The CorruptMark issue is about the VALUE that was deleted, so a
// key that reads and parses cleanly must retire it — otherwise one garbled
// value pins an `error`-severity issue, and with it the component's whole
// health status, for the life of the process while the gap remediates normally.
func TestSweep_CorruptMarkIssueRetiresWhenTheMarkKeyComesBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCorruptMarkReturns"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	if _, err := h.conn.KVCreate(ctx, "weaver-state", key, []byte("{not json")); err != nil {
		t.Fatalf("create corrupt-value mark: %v", err)
	}
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)
	if !hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("setup: the corrupt mark must alert (issues: %+v)", h.engine.issues.snapshot())
	}
	if h.markExists(t, ctx, key) {
		t.Fatal("setup: the corrupt mark must be deleted")
	}

	// The gap is still open, so the next episode claims the same key with a
	// well-formed §10.3 mark — the recurrence a listing check can never see.
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "directOp", futureLease()))

	h.pass(ctx)

	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("a mark key that reads and parses cleanly must retire its CorruptMark issue (issues: %+v)",
			h.engine.issues.snapshot())
	}
	if !h.markExists(t, ctx, key) {
		t.Fatal("the fresh episode's mark must survive the pass that retires the issue")
	}
}

// TestSweep_CorruptMarkIssueRetiresWhenTheEffectKeyComesBack is the same
// retirement for the `__effect` family, which recurs the same way: the window
// is keyed per (target, gapColumn, actionRef), so the next dispatch of that
// same pair writes a fresh window at the deleted key's name and every later
// listing contains it. Without the leg's own retirement the CorruptMark issue
// would outlive the value it describes by the life of the process.
func TestSweep_CorruptMarkIssueRetiresWhenTheEffectKeyComesBack(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureCorruptEffectReturns"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	key := effectKey(targetID, "missing_x", "directOp")
	if _, err := h.conn.KVCreate(ctx, "weaver-state", key, []byte("{not json")); err != nil {
		t.Fatalf("create corrupt-value effect window: %v", err)
	}

	h.pass(ctx)
	if !hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("setup: the corrupt window must alert (issues: %+v)", h.engine.issues.snapshot())
	}
	if h.markExists(t, ctx, key) {
		t.Fatal("setup: the corrupt window must be deleted")
	}

	// The next dispatch of the same (target, gap, action) books a fresh window
	// at the same key.
	if err := h.engine.marks.recordEffectDispatch(ctx, targetID, "missing_x", "directOp"); err != nil {
		t.Fatalf("record effect dispatch: %v", err)
	}

	h.pass(ctx)

	if hasIssueCode(h.engine.issues.snapshot(), "CorruptMark") {
		t.Fatalf("an effect key that reads and parses cleanly must retire its CorruptMark issue (issues: %+v)",
			h.engine.issues.snapshot())
	}
	if !h.markExists(t, ctx, key) {
		t.Fatal("a live target/column's rebuilt window must survive the pass that retires the issue")
	}
}
