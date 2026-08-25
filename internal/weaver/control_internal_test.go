package weaver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// controlHarness is an Engine wired to an embedded NATS server with the
// weaver-targets and weaver-state buckets provisioned, so ListTargets /
// Disable / Enable / Revoke can be exercised against a real
// substrate.ConsumerSupervisor (AC #6).
type controlHarness struct {
	engine *Engine
	conn   *substrate.Conn
	ops    *nats.Subscription
}

func newControlHarness(t *testing.T, ctx context.Context) *controlHarness {
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

	engine := NewEngine(conn, Config{
		ActorKey: "vtx.identity.WeaverServiceActor1abc",
		Instance: "control-" + testNanoID(t),
		Logger:   discardLogger(),
	})
	return &controlHarness{engine: engine, conn: conn, ops: ops}
}

// rowMessage builds a §10.2 row substrate.Message for h.engine.handleRow,
// mirroring handlerHarness.rowMessage in evaluator_internal_test.go.
func (h *controlHarness) rowMessage(t *testing.T, targetID, entityID string, row map[string]any, sequence, numDelivered uint64) substrate.Message {
	t.Helper()
	body, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	return substrate.Message{
		Subject:      h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Body:         body,
		Sequence:     sequence,
		NumDelivered: numDelivered,
	}
}

func (h *controlHarness) nextOp(t *testing.T) map[string]any {
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

func (h *controlHarness) requireNoOp(t *testing.T) {
	t.Helper()
	if msg, err := h.ops.NextMsg(500 * time.Millisecond); err == nil {
		t.Fatalf("expected no op on ops.system, got: %s", string(msg.Data))
	}
}

// seedTarget registers a target in the in-memory registry (no CDC, no
// reconcileConsumers loop).
func (h *controlHarness) seedTarget(target *Target) {
	h.engine.source.mu.Lock()
	h.engine.source.targets[target.TargetID] = target
	h.engine.source.targetOwner[target.TargetID] = "vtx.meta." + testNanoIDStatic(target.TargetID)
	h.engine.source.ownerTargetID["vtx.meta."+testNanoIDStatic(target.TargetID)] = target.TargetID
	h.engine.source.mu.Unlock()
}

// addConsumer adds the target's lane-1 consumer to the supervisor directly
// (bypassing reconcileConsumers/Start), so Disable/Enable/Revoke have a real
// managed consumer to Pause/Resume/Remove.
func (h *controlHarness) addConsumer(t *testing.T, ctx context.Context, targetID string) {
	t.Helper()
	if err := h.engine.supervisor.Add(ctx, h.engine.targetSpec(targetID)); err != nil {
		t.Fatalf("supervisor.Add(%s): %v", targetID, err)
	}
}

// testNanoIDStatic derives a deterministic pseudo-NanoID-shaped string from s
// for use as a synthetic owner vertex id in tests (does not need to be a real
// NanoID — only ownerVertexID's map lookup is exercised).
func testNanoIDStatic(s string) string {
	out := s
	for len(out) < 20 {
		out += "x"
	}
	return out[:20]
}

func (h *controlHarness) consumerState(name string) (string, bool) {
	snap := h.engine.states.Snapshot()
	state, ok := snap[name]
	return state, ok
}

// TestListTargets_ActiveByDefault verifies ListTargets reports a freshly
// registered target as "active" with its lensRef and sorted gaps (AC #1, #6).
func TestListTargets_ActiveByDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{
		TargetID: "t1",
		LensRef:  "lens-1",
		Gaps: map[string]GapAction{
			"missing_b": {Action: actionDirectOp, Operation: "FixB"},
			"missing_a": {Action: actionDirectOp, Operation: "FixA"},
		},
	})

	out, err := h.engine.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("ListTargets returned %d entries, want 1", len(out))
	}
	got := out[0]
	if got.TargetID != "t1" || got.LensRef != "lens-1" {
		t.Fatalf("ListTargets[0] = %+v, want TargetID=t1 LensRef=lens-1", got)
	}
	if got.State != targetStateActive {
		t.Fatalf("ListTargets[0].State = %q, want %q", got.State, targetStateActive)
	}
	if len(got.Gaps) != 2 || got.Gaps[0] != "missing_a" || got.Gaps[1] != "missing_b" {
		t.Fatalf("ListTargets[0].Gaps = %v, want sorted [missing_a missing_b]", got.Gaps)
	}
}

// TestDisable_PausesConsumerAndMarksDisabled verifies Disable (a) returns no
// error for a registered target, (b) pauses the lane-1 consumer
// (consumerStateCache reflects "pausedManual"), (c) writes the __control
// marker (markStore.isDisabled true), and (d) ListTargets now reports
// "disabled" (AC #2, #3, #6).
func TestDisable_PausesConsumerAndMarksDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{TargetID: "t1", LensRef: "lens-1", Gaps: map[string]GapAction{}})
	h.addConsumer(t, ctx, "t1")

	if err := h.engine.Disable(ctx, "t1"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	state, ok := h.consumerState(laneConsumerPrefix + "t1")
	if !ok || state != "pausedManual" {
		t.Fatalf("consumerStateCache[%s] = (%q, %v), want (pausedManual, true)", laneConsumerPrefix+"t1", state, ok)
	}

	disabled, err := h.engine.marks.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if !disabled {
		t.Fatalf("isDisabled(t1) = false after Disable, want true")
	}

	if !h.engine.isTargetDisabled("t1") {
		t.Fatalf("isTargetDisabled(t1) = false after Disable, want true (in-memory set)")
	}

	out, err := h.engine.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if out[0].State != targetStateDisabled {
		t.Fatalf("ListTargets[0].State = %q after Disable, want %q", out[0].State, targetStateDisabled)
	}
}

// TestDisable_NotRegistered verifies Disable returns an error mentioning the
// targetID for an unregistered target, and does not write a __control marker.
func TestDisable_NotRegistered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	err := h.engine.Disable(ctx, "ghost")
	if err == nil {
		t.Fatalf("Disable(ghost) = nil error, want error")
	}

	disabled, dErr := h.engine.marks.isDisabled(ctx, "ghost")
	if dErr != nil {
		t.Fatalf("isDisabled: %v", dErr)
	}
	if disabled {
		t.Fatalf("isDisabled(ghost) = true after failed Disable, want false")
	}
}

// TestEnable_ReversesDisable verifies Enable resumes the lane-1 consumer
// (consumerStateCache reflects "running"), clears the __control marker, and
// ListTargets reports "active" again (AC #2, #3, #6, #7).
func TestEnable_ReversesDisable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{TargetID: "t1", LensRef: "lens-1", Gaps: map[string]GapAction{}})
	h.addConsumer(t, ctx, "t1")

	if err := h.engine.Disable(ctx, "t1"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if err := h.engine.Enable(ctx, "t1"); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	state, ok := h.consumerState(laneConsumerPrefix + "t1")
	if !ok || state != "running" {
		t.Fatalf("consumerStateCache[%s] = (%q, %v), want (running, true)", laneConsumerPrefix+"t1", state, ok)
	}

	disabled, err := h.engine.marks.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if disabled {
		t.Fatalf("isDisabled(t1) = true after Enable, want false")
	}

	if h.engine.isTargetDisabled("t1") {
		t.Fatalf("isTargetDisabled(t1) = true after Enable, want false (in-memory set)")
	}

	out, err := h.engine.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if out[0].State != targetStateActive {
		t.Fatalf("ListTargets[0].State = %q after Enable, want %q", out[0].State, targetStateActive)
	}
}

// TestEnable_NotRegistered verifies Enable returns an error mentioning the
// targetID for an unregistered target.
func TestEnable_NotRegistered(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	if err := h.engine.Enable(ctx, "ghost"); err == nil {
		t.Fatalf("Enable(ghost) = nil error, want error")
	}
}

// TestRevoke_RemovesDurableMarksAndStaysDisabled verifies Revoke (a) removes
// the lane-1 durable (consumerStateCache no longer has an entry for it),
// (b) deletes every weaver-state key with prefix "t1." — including any
// in-flight marks AND the __control marker, (c) is NOT an error, and
// (d) re-writes the __control marker afterward so the in-memory disabled-set
// still reports t1 as disabled (AC #4, strict superset of Disable).
func TestRevoke_RemovesDurableMarksAndStaysDisabled(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{
		TargetID: "t1",
		LensRef:  "lens-1",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	h.addConsumer(t, ctx, "t1")

	// Seed an in-flight mark under t1.
	entityID := testNanoID(t)
	if _, _, _, err := h.engine.marks.create(ctx, "t1", entityID, "missing_x", "vtx.leaseApp."+entityID, "directOp"); err != nil {
		t.Fatalf("create mark: %v", err)
	}

	if err := h.engine.Revoke(ctx, "t1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if _, ok := h.consumerState(laneConsumerPrefix + "t1"); ok {
		t.Fatalf("consumerStateCache still has an entry for %s after Revoke", laneConsumerPrefix+"t1")
	}

	// The in-flight mark is gone.
	if _, _, found, err := h.engine.marks.get(ctx, "t1", entityID, "missing_x"); err != nil {
		t.Fatalf("get mark: %v", err)
	} else if found {
		t.Fatalf("in-flight mark still present after Revoke")
	}

	// The __control marker is re-written: revoked target stays disabled.
	disabled, err := h.engine.marks.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if !disabled {
		t.Fatalf("isDisabled(t1) = false after Revoke, want true (Revoke is a strict superset of Disable)")
	}
	if !h.engine.isTargetDisabled("t1") {
		t.Fatalf("isTargetDisabled(t1) = false after Revoke, want true (in-memory set)")
	}

	// ListTargets still shows t1 (registry unchanged) but disabled.
	out, err := h.engine.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(out) != 1 || out[0].State != targetStateDisabled {
		t.Fatalf("ListTargets after Revoke = %+v, want 1 entry with State=%q", out, targetStateDisabled)
	}
}

// TestRevoke_RetiresEveryPerEntityIssueFamily pins the teardown of the issue
// families whose keys carry a segment below the target — gap (per entity), gap-
// config (per gap column), data (per entity) and inflight-mismatch (per gap
// column). A revoked target has no rows left to close and no marks left to
// reclaim, so nothing on the live path will ever retire these: without the
// prefix clear, one entry per (entity, column) stands for a target that no
// longer exists until the process restarts. Another target's identically-shaped
// entries must survive, which is what makes this a prefix clear rather than a
// flush.
func TestRevoke_RetiresEveryPerEntityIssueFamily(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{
		TargetID: "t1",
		LensRef:  "lens-1",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	h.addConsumer(t, ctx, "t1")

	entityA, entityB := testNanoID(t), testNanoID(t)
	revoked := []string{
		issueKeyGapEntity("t1", entityA, "missing_x"),
		issueKeyGapEntity("t1", entityB, "missing_x"),
		issueKeyGapConfig("t1", "missing_x"),
		issueKeyDataEntity("t1", entityA, "missing_x"),
		issueKeyDataEntity("t1", entityB, freshUntilColumn),
		issueKeyDataEntity("t1", entityB, "entityKey"),
	}
	for _, key := range revoked {
		h.engine.issues.set(key, "warning", "Fixture", key)
	}
	// A second target whose keys share t1's leading characters: the trailing
	// separator in each family prefix is what keeps "t1." from matching "t10.".
	survivors := []string{
		issueKeyGapEntity("t10", entityA, "missing_x"),
		issueKeyGapConfig("t10", "missing_x"),
		issueKeyDataEntity("t10", entityA, "missing_x"),
	}
	for _, key := range survivors {
		h.engine.issues.set(key, "warning", "Fixture", key)
	}

	if err := h.engine.Revoke(ctx, "t1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	for _, key := range revoked {
		if _, ok := issueAt(h.engine.issues, key); ok {
			t.Fatalf("Revoke left %q standing for a target that no longer exists; issues = %+v",
				key, h.engine.issues.snapshot())
		}
	}
	for _, key := range survivors {
		if _, ok := issueAt(h.engine.issues, key); !ok {
			t.Fatalf("Revoke(t1) retired %q, which belongs to another target", key)
		}
	}
}

// TestRevoke_NotRegistered_NoError verifies Revoke on a never-registered
// target is NOT an error (idempotent, mirrors ConsumerSupervisor.Remove's
// no-op-if-unmanaged posture) and still writes the __control marker.
func TestRevoke_NotRegistered_NoError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	if err := h.engine.Revoke(ctx, "ghost"); err != nil {
		t.Fatalf("Revoke(ghost) = %v, want nil (idempotent)", err)
	}

	disabled, err := h.engine.marks.isDisabled(ctx, "ghost")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if !disabled {
		t.Fatalf("isDisabled(ghost) = false after Revoke, want true")
	}
}

// TestSeedDisabledTargets_RestoresInMemorySet verifies seedDisabledTargets
// scans weaver-state for `<targetId>.__control` markers and populates the
// in-memory disabled-set (AC #6 — durable truth survives restart).
func TestSeedDisabledTargets_RestoresInMemorySet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	if err := h.engine.marks.setDisabled(ctx, "t1", true); err != nil {
		t.Fatalf("setDisabled: %v", err)
	}

	// Fresh in-memory set (simulates restart): not yet seeded.
	if h.engine.isTargetDisabled("t1") {
		t.Fatalf("isTargetDisabled(t1) = true before seedDisabledTargets, want false")
	}

	if err := h.engine.seedDisabledTargets(ctx); err != nil {
		t.Fatalf("seedDisabledTargets: %v", err)
	}

	if !h.engine.isTargetDisabled("t1") {
		t.Fatalf("isTargetDisabled(t1) = false after seedDisabledTargets, want true")
	}
}

// TestSeedDisabledTargets_ListKeysErrorPropagates verifies a KVListKeys
// failure (e.g. the weaver-state bucket isn't provisioned yet) surfaces as an
// error rather than being swallowed into an empty disabled-set — Engine.Start
// wraps and aborts on it (engine.go), so a substrate outage at boot must fail
// closed, never silently start with every target's disable-state unknown.
func TestSeedDisabledTargets_ListKeysErrorPropagates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	srv := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, srv.ClientURL())
	t.Cleanup(nc.Close)
	conn, err := substrate.Wrap(nc)
	if err != nil {
		t.Fatalf("substrate wrap: %v", err)
	}
	// Deliberately do NOT provision the weaver-state bucket.
	engine := NewEngine(conn, Config{
		ActorKey: "vtx.identity.WeaverServiceActor1abc",
		Instance: "seed-err-" + testNanoID(t),
		Logger:   discardLogger(),
	})

	if err := engine.seedDisabledTargets(ctx); err == nil {
		t.Fatalf("seedDisabledTargets against an unprovisioned bucket = nil error, want error")
	}
}

// TestDisable_UnmanagedConsumer_StillMarksControlState verifies Disable/Enable
// degrade safely when the target's lane-1 consumer isn't (yet) registered
// with the supervisor: Pause/Resume are silent no-ops on an unmanaged name
// (substrate.ConsumerSupervisor.Pause/Resume's bool return, discarded here),
// but the durable `__control` marker and in-memory disabled-set — the actual
// remediation-skip authority handleRow reads — are still set/cleared exactly
// as when a real consumer is paused/resumed.
func TestDisable_UnmanagedConsumer_StillMarksControlState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{TargetID: "t1", LensRef: "lens-1", Gaps: map[string]GapAction{}})
	// Deliberately skip h.addConsumer: the supervisor has no managed consumer
	// for t1, so Disable/Enable's Pause/Resume calls are silent no-ops.

	if err := h.engine.Disable(ctx, "t1"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	disabled, err := h.engine.marks.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if !disabled {
		t.Fatalf("isDisabled(t1) = false after Disable with no managed consumer, want true")
	}
	if !h.engine.isTargetDisabled("t1") {
		t.Fatalf("isTargetDisabled(t1) = false after Disable with no managed consumer, want true")
	}

	if err := h.engine.Enable(ctx, "t1"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	disabled, err = h.engine.marks.isDisabled(ctx, "t1")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if disabled {
		t.Fatalf("isDisabled(t1) = true after Enable with no managed consumer, want false")
	}
	if h.engine.isTargetDisabled("t1") {
		t.Fatalf("isTargetDisabled(t1) = true after Enable with no managed consumer, want false")
	}
}

// TestFreezeOscillatingPair_DisableFailureStillAlerts verifies a Disable
// failure for one leg of an oscillating pair (e.g. the target was removed
// between its last dispatch and the freeze) is logged, not fatal: the other
// leg is still disabled and the oscillation alert still names the pair.
func TestFreezeOscillatingPair_DisableFailureStillAlerts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.seedTarget(&Target{TargetID: "targetA", LensRef: "lens-a", Gaps: map[string]GapAction{}})
	h.addConsumer(t, ctx, "targetA")
	// targetB is deliberately NOT registered — Disable("targetB") errors.

	h.engine.freezeOscillatingPair(ctx, "targetA", "targetB", guardgrammar.Path{Field: "status"})

	disabled, err := h.engine.marks.isDisabled(ctx, "targetA")
	if err != nil {
		t.Fatalf("isDisabled: %v", err)
	}
	if !disabled {
		t.Fatalf("isDisabled(targetA) = false after freeze, want true (the registered leg must still be disabled despite the other leg's Disable failing)")
	}

	issues := h.engine.issues.snapshot()
	if !hasIssueCode(issues, "TargetOscillation") {
		t.Fatalf("expected a TargetOscillation issue naming the pair even though targetB's Disable failed, got %+v", issues)
	}
}

// TestHandleRow_DisabledSkipsDispatchButClearsMarks proves AC #12c's
// disable-during-in-flight scenario at the handleRow dispatch-skip guard
// (AC #2/#7): once Disable has set the in-memory disabled-set, a violating
// row for a NEW entity creates no mark and runs no remediation, while a row
// whose gap closes for an EXISTING in-flight mark still clears that mark — the
// level-reconciled mark-clearing leg (clearClosedMarks) runs unconditionally
// before the disabled-skip guard, which now gates ONLY the remediation loop
// (mark-create + Strategist/Actuator dispatch), not the bookkeeping legs.
func TestHandleRow_DisabledSkipsDispatchButClearsMarks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	const targetID = "t1"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	h.addConsumer(t, ctx, targetID)

	// Establish an in-flight mark for entityA while active.
	entityA := testNanoID(t)
	rowA := map[string]any{
		"entityKey": "vtx.leaseApp." + entityA,
		"violating": true,
		"missing_x": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityA, rowA, 1, 1)); dec != substrate.Ack {
		t.Fatalf("initial dispatch must Ack, got %v", dec)
	}
	h.nextOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityA, "missing_x"); err != nil || !found {
		t.Fatalf("mark for entityA must exist before Disable (err=%v, found=%v)", err, found)
	}

	if err := h.engine.Disable(ctx, targetID); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// A NEW violating entity must not create a mark or fire an op while disabled.
	entityB := testNanoID(t)
	rowB := map[string]any{
		"entityKey": "vtx.leaseApp." + entityB,
		"violating": true,
		"missing_x": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityB, rowB, 2, 1)); dec != substrate.Ack {
		t.Fatalf("disabled dispatch-skip must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityB, "missing_x"); err != nil {
		t.Fatalf("get mark for entityB: %v", err)
	} else if found {
		t.Fatalf("mark for entityB must not be created while target is disabled")
	}

	// entityA's gap closes while the target is disabled: its pre-existing
	// mark still clears (mark-clearing is additive, not gated by the
	// disabled-skip guard).
	rowAClosed := map[string]any{
		"entityKey": "vtx.leaseApp." + entityA,
		"violating": false,
		"missing_x": false,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityA, rowAClosed, 3, 1)); dec != substrate.Ack {
		t.Fatalf("mark-clearing while disabled must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityA, "missing_x"); err != nil {
		t.Fatalf("get mark for entityA after clear: %v", err)
	} else if found {
		t.Fatalf("mark for entityA must clear while target is disabled (clearClosedMarks runs unconditionally)")
	}

	out, err := h.engine.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if out[0].State != targetStateDisabled {
		t.Fatalf("ListTargets[0].State = %q, want %q", out[0].State, targetStateDisabled)
	}
}

// TestHandleRow_EnableResumesDispatch proves AC #12c's enable-resumes
// scenario: after Disable suppresses dispatch for a violating row, Enable
// clears the in-memory disabled-set and the SAME row delivered again
// dispatches normally (creates a mark, fires an op).
func TestHandleRow_EnableResumesDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	const targetID = "t1"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	h.addConsumer(t, ctx, targetID)

	if err := h.engine.Disable(ctx, targetID); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		"missing_x": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
		t.Fatalf("disabled dispatch-skip must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("get mark: %v", err)
	} else if found {
		t.Fatalf("mark must not exist while target is disabled")
	}

	if err := h.engine.Enable(ctx, targetID); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// The next row delivery resumes normal dispatch.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 2, 1)); dec != substrate.Ack {
		t.Fatalf("post-enable dispatch must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("post-enable op = %v, want operationType FixX", op)
	}
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil || !found {
		t.Fatalf("mark must exist after post-enable dispatch (err=%v, found=%v)", err, found)
	}

	out, err := h.engine.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if out[0].State != targetStateActive {
		t.Fatalf("ListTargets[0].State = %q after Enable, want %q", out[0].State, targetStateActive)
	}
}

// TestHandleRow_RevokeRemovesDurableAndConsumerGone proves AC #12c's
// revoke-clears-state scenario: Revoke removes the lane-1 durable consumer
// (consumerStateCache no longer has an entry for it) and deletes the
// in-flight mark, but the registry (targetSource) still reports the target —
// ListTargets continues to list it as "disabled" (AC #4's documented bound:
// Revoke does not unregister the target).
func TestHandleRow_RevokeRemovesDurableAndConsumerGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)

	const targetID = "t1"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	h.addConsumer(t, ctx, targetID)

	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		"missing_x": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
		t.Fatalf("initial dispatch must Ack, got %v", dec)
	}
	h.nextOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil || !found {
		t.Fatalf("mark must exist before Revoke (err=%v, found=%v)", err, found)
	}

	if err := h.engine.Revoke(ctx, targetID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	if _, ok := h.consumerState(laneConsumerPrefix + targetID); ok {
		t.Fatalf("consumerStateCache still has an entry for %s after Revoke", laneConsumerPrefix+targetID)
	}
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("get mark after Revoke: %v", err)
	} else if found {
		t.Fatalf("mark must be deleted by Revoke")
	}

	out, err := h.engine.ListTargets(ctx)
	if err != nil {
		t.Fatalf("ListTargets: %v", err)
	}
	if len(out) != 1 || out[0].TargetID != targetID {
		t.Fatalf("ListTargets after Revoke = %+v, want 1 entry for %q (still registered per AC #4's bound)", out, targetID)
	}
	if out[0].State != targetStateDisabled {
		t.Fatalf("ListTargets[0].State = %q after Revoke, want %q", out[0].State, targetStateDisabled)
	}
}

// TestRevokeEnable_ReAddsConsumerViaReconcile drives a real Revoke → reconcile
// → Enable through e.targets / reconcileConsumers (NOT the addConsumer harness
// bypass): Revoke removes the lane-1 durable AND drops e.targets[targetID], so
// the next reconcileConsumers re-Adds an (inert) consumer for the
// still-registered target; that consumer Ack-skips remediation while the
// `__control` marker stands; Enable then clears the marker, re-runs reconcile,
// and remediation pumps live again. Proves the BH-1 fix: a revoked→enabled
// target is restored rather than dead-until-restart.
func TestRevokeEnable_ReAddsConsumerViaReconcile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	// reconcileConsumers needs e.ctx set (Start is not run in the harness).
	h.engine.ctx = ctx

	const targetID = "t1"
	h.seedTarget(&Target{
		TargetID: targetID,
		LensRef:  "lens-1",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})

	// First reconcile Adds the consumer and populates e.targets (the applied
	// fingerprint the re-add path keys on — exactly what addConsumer skips).
	h.engine.reconcileConsumers()
	if _, ok := h.engine.targets[targetID]; !ok {
		t.Fatalf("e.targets[%s] not populated after reconcile", targetID)
	}

	// Revoke removes the durable AND drops e.targets[targetID].
	if err := h.engine.Revoke(ctx, targetID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := h.engine.targets[targetID]; ok {
		t.Fatalf("e.targets[%s] must be dropped by Revoke so reconcile re-adds", targetID)
	}

	// A reconcile pass (as a registry event would trigger) re-Adds the
	// consumer; it is inert (the `__control` marker stands) so a violating row
	// Ack-skips with no op.
	h.engine.reconcileConsumers()
	if _, ok := h.engine.targets[targetID]; !ok {
		t.Fatalf("reconcile after Revoke must re-Add the consumer (e.targets re-populated)")
	}
	entityID := testNanoID(t)
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
		t.Fatalf("inert re-added consumer must Ack-skip, got %v", dec)
	}
	h.requireNoOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("get mark while disabled: %v", err)
	} else if found {
		t.Fatalf("no mark must be created while the re-added consumer is inert")
	}

	// Enable clears the marker and re-runs reconcile — the consumer pumps live.
	if err := h.engine.Enable(ctx, targetID); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if h.engine.isTargetDisabled(targetID) {
		t.Fatalf("isTargetDisabled(t1) = true after Enable, want false")
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 2, 1)); dec != substrate.Ack {
		t.Fatalf("post-enable dispatch must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("post-enable op = %v, want operationType FixX", op)
	}
}

// TestReconcileRemove_DeletesControlMarker proves the ECH-1 fix: when a
// disabled target leaves the registry (genuine uninstall), the
// reconcileConsumers removal branch deletes its `<targetId>.__control` marker
// and prunes the in-memory disabled-set — so a re-install of the same targetId
// does not silently come up disabled and no orphan marker leaks in
// weaver-state.
func TestReconcileRemove_DeletesControlMarker(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := newControlHarness(t, ctx)
	h.engine.ctx = ctx

	const targetID = "t1"
	h.seedTarget(&Target{
		TargetID: targetID,
		LensRef:  "lens-1",
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	h.engine.reconcileConsumers()

	if err := h.engine.Disable(ctx, targetID); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if disabled, err := h.engine.marks.isDisabled(ctx, targetID); err != nil || !disabled {
		t.Fatalf("marker must be present after Disable (err=%v, disabled=%v)", err, disabled)
	}

	// Uninstall: drop the target from the registry, then reconcile (the removal
	// branch fires because the id is no longer desired).
	h.engine.source.mu.Lock()
	delete(h.engine.source.targets, targetID)
	h.engine.source.mu.Unlock()
	h.engine.reconcileConsumers()

	if disabled, err := h.engine.marks.isDisabled(ctx, targetID); err != nil {
		t.Fatalf("isDisabled after uninstall: %v", err)
	} else if disabled {
		t.Fatalf("`__control` marker must be deleted on genuine uninstall, still present")
	}
	if h.engine.isTargetDisabled(targetID) {
		t.Fatalf("in-memory disabled-set must be pruned on genuine uninstall")
	}
	keys, err := h.conn.KVListKeys(ctx, "weaver-state")
	if err != nil {
		t.Fatalf("list weaver-state: %v", err)
	}
	for _, k := range keys {
		if k == controlKey(targetID) {
			t.Fatalf("orphan `__control` marker %q leaked after uninstall", k)
		}
	}
}

// TestResetRetryBudget_UnParksAGapEndToEnd proves the verb un-parks a gap: the
// whole path from an operator's decision to a remediation on the wire.
//
// The setup is a genuinely parked gap: budget spent, no mark (an exhausted gap
// stops refreshing one), and a row nothing will re-deliver. The sweep's count
// leg re-derives the standing GapBudgetExhausted from the count on every pass
// and dispatches nothing, forever, which is correct until an operator says
// otherwise. The verb is that operator saying otherwise — and one pass later
// the gap must actually be trying again: an op on the wire and a mark holding
// the fresh episode.
//
// This is also what pins the verb's one load-bearing implementation choice.
// Writing the count to 0 leaves the key the count leg enumerates; DELETING it
// would leave a gap with no count, no mark and no delivery coming — invisible
// to every leg, un-suppressed and still un-dispatched. The op below is the
// difference between the two.
func TestResetRetryBudget_UnParksAGapEndToEnd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureResetBudgetE2E"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	h.seedCount(t, ctx, targetID, entityID, "missing_x", budget)
	h.putRow(t, ctx, targetID, entityID, exhaustedRow(entityID, "missing_x", budget))

	// The park: the sweep raises the loud stop and dispatches nothing.
	h.pass(ctx)
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_x")); !ok {
		t.Fatal("setup: the gap must be parked before it can be un-parked")
	}

	previous, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, "missing_x")
	if err != nil {
		t.Fatalf("ResetRetryBudget: %v", err)
	}
	if previous != budget {
		t.Fatalf("previous count = %d, want %d (what the park had actually spent)", previous, budget)
	}
	// The verb states intent and stops: no dispatch of its own, and the issue
	// is still standing because nothing has disproved it yet.
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_x")); !ok {
		t.Fatal("the verb must not clear the issue itself — the dispatch that disproves it does")
	}
	if !h.countExists(t, ctx, targetID, entityID, "missing_x") {
		t.Fatal("the count key IS the anchor the sweep enumerates: writing 0 must keep it")
	}
	if got := h.countValue(t, ctx, targetID, entityID, "missing_x"); got != 0 {
		t.Fatalf("dispatch-count after the reset = %d, want 0", got)
	}

	// One pass later the gap is trying again.
	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("operationType = %v, want FixX (the playbook's remediation, dispatched again)", op["operationType"])
	}
	if !h.markExists(t, ctx, markKey(targetID, entityID, "missing_x")) {
		t.Fatal("the un-parked gap must hold a fresh episode's mark")
	}
	if got := h.countValue(t, ctx, targetID, entityID, "missing_x"); got != 1 {
		t.Fatalf("dispatch-count = %d, want 1: the re-armed chain's first attempt", got)
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_x")); ok {
		t.Fatal("a gap that just dispatched is not parked: the standing issue must be retired")
	}
	if _, _, _, _, reArms, _ := h.engine.sweep.metrics(); reArms != 1 {
		t.Fatalf("sweepReArms = %d, want 1 (the un-park is visible on the heartbeat)", reArms)
	}
}

// TestResetRetryBudget_RefusesWhatItCannotHonour pins every way the verb
// declines, because each one is a case where writing would be worse than
// refusing. The vectors that matter most are the two that would CREATE a key:
// a gap with no budget at all, and the same with no row either. A count key
// exists only where a chain has actually dispatched, so inventing one would
// hand the sweep's re-arm arm a gap nobody chose — on a mistyped entityId, an
// arbitrary entity's gap would start dispatching.
func TestResetRetryBudget_RefusesWhatItCannotHonour(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixtureResetBudgetRefuse"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	budgeted := testNanoID(t)
	unbudgeted := testNanoID(t)
	rowless := testNanoID(t)
	h.seedCount(t, ctx, targetID, budgeted, "missing_x", 2)
	h.putRow(t, ctx, targetID, budgeted, exhaustedRow(budgeted, "missing_x", 2))
	// unbudgeted has a row but no chain has ever dispatched it; rowless has
	// neither.
	h.putRow(t, ctx, targetID, unbudgeted, exhaustedRow(unbudgeted, "missing_x", 2))

	for _, tc := range []struct {
		name                       string
		targetID, entityID, gapCol string
		wantErrContains            string
	}{
		{"no budget at this gap", targetID, unbudgeted, "missing_x", "no retry budget"},
		{"no budget and no row", targetID, rowless, "missing_x", "no retry budget"},
		{"unregistered target", "ghostTarget", budgeted, "missing_x", "not registered"},
		{"dotted targetId", "a.b", budgeted, "missing_x", "single token"},
		{"entityId is not a NanoID", targetID, "not-a-nanoid", "missing_x", "NanoID"},
		{"gapColumn is not a missing_ column", targetID, budgeted, "violating", "missing_"},
		{"dotted gapColumn", targetID, budgeted, "missing_a.b", "missing_"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.engine.ResetRetryBudget(ctx, tc.targetID, tc.entityID, tc.gapCol)
			if err == nil {
				t.Fatalf("ResetRetryBudget(%q,%q,%q) must refuse", tc.targetID, tc.entityID, tc.gapCol)
			}
			if !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.wantErrContains)
			}
		})
	}

	// Nothing was written anywhere: no invented anchors.
	keys, err := h.conn.KVListKeys(ctx, "weaver-state")
	if err != nil {
		t.Fatalf("list weaver-state: %v", err)
	}
	for _, k := range keys {
		if strings.HasSuffix(k, countKeySuffix) && k != countKey(targetID, budgeted, "missing_x") {
			t.Fatalf("a refused reset created a budget key: %q", k)
		}
	}
	// And the one real budget is untouched by every refusal above.
	if got := h.countValue(t, ctx, targetID, budgeted, "missing_x"); got != 2 {
		t.Fatalf("the surviving budget = %d, want 2 (a refusal writes nothing)", got)
	}

	// A rowless gap that DOES carry a budget resets fine — the verb re-arms the
	// budget; whether the gap is dispatchable is the sweep's judgement, and its
	// row-gone arm leaves that count alone rather than acting on absence.
	h.seedCount(t, ctx, targetID, rowless, "missing_x", 3)
	if previous, err := h.engine.ResetRetryBudget(ctx, targetID, rowless, "missing_x"); err != nil || previous != 3 {
		t.Fatalf("ResetRetryBudget on a rowless budget = (%d, %v), want (3, nil)", previous, err)
	}
	h.pass(ctx)
	h.requireNoOp(t)
	if got := h.countValue(t, ctx, targetID, rowless, "missing_x"); got != 0 {
		t.Fatalf("rowless budget = %d, want 0: the reset holds, and the sweep never dispatched it", got)
	}
}

// TestResetRetryBudget_RefusesAConcurrentBump pins the revision condition on
// the reset — the only thing standing between the verb and a chain that is
// already moving again.
//
// The verb reads the budget the operator is deciding about, then writes 0
// conditioned on that read. If a dispatch lands in between, the value they
// decided on is gone: writing anyway would force a chain that has just spent an
// attempt back to a full budget nobody asked for, and the sweep would then keep
// re-arming it. The interleave is staged the way the count leg's OCC race is —
// by handing the write the revision a caller read BEFORE the bump, which is
// exactly the state a losing writer holds.
func TestResetRetryBudget_RefusesAConcurrentBump(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureResetBudgetRace"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	h.seedCount(t, ctx, targetID, entityID, "missing_x", 2)

	// What the verb read: the budget as the operator saw it.
	previous, revision, found, err := h.engine.marks.dispatchCountEntry(ctx, targetID, entityID, "missing_x")
	if err != nil || !found || previous != 2 {
		t.Fatalf("read = (%d, rev %d, found=%v, %v), want (2, _, true, nil)", previous, revision, found, err)
	}
	// What landed in between: one more dispatch on the same chain.
	h.engine.bumpDispatchCount(ctx, targetID, entityID, "missing_x")

	conflict, err := h.engine.marks.resetDispatchCount(ctx, targetID, entityID, "missing_x", revision)
	if err != nil {
		t.Fatalf("resetDispatchCount: %v", err)
	}
	if !conflict {
		t.Fatal("a reset conditioned on a superseded revision must report the conflict, not silently win")
	}
	if got := h.countValue(t, ctx, targetID, entityID, "missing_x"); got != 3 {
		t.Fatalf("dispatch-count = %d, want 3: the dispatch that raced the reset must survive it", got)
	}

	// Re-reading is the remedy the error tells the operator to apply, and it
	// resets the value that is actually there now.
	if previous, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, "missing_x"); err != nil || previous != 3 {
		t.Fatalf("re-run after the conflict = (%d, %v), want (3, nil)", previous, err)
	}
	if got := h.countValue(t, ctx, targetID, entityID, "missing_x"); got != 0 {
		t.Fatalf("dispatch-count after the re-run = %d, want 0", got)
	}
}
