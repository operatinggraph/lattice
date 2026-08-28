package weaver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// TestReplayTarget_RefusesAnUnmanagedConsumer covers the one permanent decline
// the operator verbs cannot construct between them: a target that is registered
// and enabled, whose lane-1 consumer this instance does not manage. In
// production that is a target whose consumer Add failed (it carries a standing
// ConsumerReconcileError) — a revoke lands on the DISABLED refusal instead,
// because Revoke is a superset of Disable.
//
// The engine here is never started, so its supervisor manages nothing while the
// seeded target is registered and enabled: exactly that shape. The verb must
// refuse it under its OWN wording — reporting a successful replay of a durable
// that does not exist would leave the operator's diagnostic standing over a fact
// nothing re-derived, and surfacing the supervisor's bare "not managed" would
// name no remedy.
func TestReplayTarget_RefusesAnUnmanagedConsumer(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureReplayUnmanaged"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_a": {Action: actionDirectOp, Operation: "FixA"}},
	})

	_, err := h.engine.ReplayTarget(ctx, targetID)
	if err == nil {
		t.Fatalf("replay of a target with no managed lane-1 consumer must refuse, not report success")
	}
	for _, want := range []string{"not managed by this instance", laneConsumerPrefix + targetID, "`enable` re-adds"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q must name %q — the durable that is missing and the verb that restores it", err, want)
		}
	}
}

// escalatingTarget registers a target whose missing_a gap dispatches directOp
// FixA and whose augur block escalates an exhausted budget to the reasoning
// tier, and spends that gap's budget for entityID so the next delivery reaches
// escalateExhaustedGap.
func escalatingTarget(t *testing.T, ctx context.Context, h *handlerHarness, targetID, entityID string, budget int) {
	t.Helper()
	if _, registered := h.engine.source.target(targetID); !registered {
		id := testNanoID(t)
		spec := targetSpecFixture(targetID) // declares gaps.missing_a -> directOp FixA
		spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
		h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
		h.engine.source.handle(specEvent(t, id, spec))
	}
	for i := 0; i < budget; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_a"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
}

// expireMark rewrites the gap's mark so its lease reads as expired — the state a
// replay finds for any episode that was dispatched more than one lease ago, and
// the state escalateExhaustedGap's re-fire arm acts on.
func expireMark(t *testing.T, ctx context.Context, h *handlerHarness, targetID, entityID, col string) {
	t.Helper()
	key := markKey(targetID, entityID, col)
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("read mark %q: %v", key, err)
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal mark %q: %v", key, err)
	}
	rec.ClaimedAt = substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour))
	rec.LeaseExpiresAt = pastLease()
	body, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal mark %q: %v", key, err)
	}
	if _, err := h.conn.KVPut(ctx, "weaver-state", key, body); err != nil {
		t.Fatalf("put expired mark %q: %v", key, err)
	}
}

// TestEscalateExhaustedGap_StandingEscalationIsNotRePaid is T11: a replay must
// not re-pay Augur for a question already asked.
//
// One ReplayTarget invocation re-delivers every row of a target, and for a
// violating row whose mark has aged past its lease the exhausted-gap arm would
// clear that mark and fire a FRESH reasoning episode — a real per-call cost, paid
// again on every replay, every decline-floor redelivery and every sweep pass, for
// a gap whose escalation already stands.
//
// The level check at the raise site stops that. This test walks all three states
// it has to tell apart: a first escalation (fires), a re-derivation with the fact
// standing and the mark stale (does not fire, and does not tear the mark down
// either), and the fact having been retired by the gap closing (fires again).
func TestEscalateExhaustedGap_StandingEscalationIsNotRePaid(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureEscalateOnce"
	const budget = 2
	entityID := testNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	escalatingTarget(t, ctx, h, targetID, entityID, budget)

	row := map[string]any{
		"entityKey": entityKey, "violating": true, "missing_a": true,
		"inflight_a": false, "maxretries_a": budget,
	}
	issueKey := issueKeyGapEntity(targetID, entityID, "missing_a")

	// The first escalation: no fact stands, so the episode fires and the fact is
	// recorded.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 7, 1)); dec != substrate.Ack {
		t.Fatalf("first delivery: decision = %v, want Ack", dec)
	}
	if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
		t.Fatalf("first delivery must dispatch the reasoning episode, got %v", op["operationType"])
	}
	if !h.engine.issues.standingAs(issueKey, "warning", codeGapEscalatedToAugur) {
		t.Fatalf("a dispatched escalation must record its standing fact at %s, issues = %+v",
			issueKey, h.engine.issues.snapshot())
	}

	// The replay: the mark this escalation left has aged past its lease, which is
	// the exact input the re-fire arm acts on. The standing fact must stop it.
	expireMark(t, ctx, h, targetID, entityID, "missing_a")
	_, revBefore, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a")
	if err != nil || !found {
		t.Fatalf("the expired mark must still be there (found=%v err=%v)", found, err)
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 8, 1)); dec != substrate.Ack {
		t.Fatalf("replayed delivery: decision = %v, want Ack", dec)
	}
	h.requireNoOp(t)
	_, revAfter, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a")
	if err != nil || !found || revAfter != revBefore {
		t.Fatalf("the suppression must return before the mark teardown (found=%v rev=%v want=%v err=%v)",
			found, revAfter, revBefore, err)
	}

	// The fact is not a permanent gag: it retires exactly where the fact ends —
	// the gap closing, the row leaving, a plan building for the gap again — and a
	// fresh exhaustion after that escalates afresh.
	h.engine.retireClosedGapIssues(targetID, entityID, "missing_a")
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 9, 1)); dec != substrate.Ack {
		t.Fatalf("post-retirement delivery: decision = %v, want Ack", dec)
	}
	if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
		t.Fatalf("a retired fact must let the next exhaustion escalate again, got %v", op["operationType"])
	}
}

// TestEscalateExhaustedGap_SuppressionIsPerSubject pins the boundary the
// suppression must not cross: the standing fact is one ROW's, keyed per (target,
// entity, gap), so another entity's first escalation of the same column is a
// different question and still reaches the model. A suppression keyed any wider
// would silently answer one subject's escalation with another's.
func TestEscalateExhaustedGap_SuppressionIsPerSubject(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureEscalatePerSubject"
	const budget = 2
	first := testNanoID(t)
	second := testNanoID(t)
	escalatingTarget(t, ctx, h, targetID, first, budget)
	escalatingTarget(t, ctx, h, targetID, second, budget)

	rowFor := func(entityID string) map[string]any {
		return map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_a": true,
			"inflight_a": false, "maxretries_a": budget,
		}
	}

	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, first, rowFor(first), 7, 1)); dec != substrate.Ack {
		t.Fatalf("first subject: decision = %v, want Ack", dec)
	}
	if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
		t.Fatalf("first subject must escalate, got %v", op["operationType"])
	}

	// The second subject's gap has never escalated: its own latch is empty, so it
	// escalates even though the first subject's stands at the same column.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, second, rowFor(second), 8, 1)); dec != substrate.Ack {
		t.Fatalf("second subject: decision = %v, want Ack", dec)
	}
	if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
		t.Fatalf("a second subject's FIRST escalation must not be swallowed by the first subject's standing fact, got %v",
			op["operationType"])
	}
}

// TestEscalateExhaustedGap_UnfiredEscalationRecordsNothing pins the guard that
// keeps the suppression honest: the standing fact is written only after an
// episode ACTUALLY fired. A publish failure Naks and asks for the row back, and
// recording the escalation on the way out would suppress that very retry against
// an episode that never ran — the gap would sit exhausted, un-escalated, and
// permanently unable to escalate.
//
// The failure is a real one (the ops stream is deleted, so the publish gets no
// stream acknowledgement) rather than a stubbed disposition, because what is
// under test is what `fire`'s error does to this function's bookkeeping.
func TestEscalateExhaustedGap_UnfiredEscalationRecordsNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureEscalateUnfired"
	const budget = 2
	entityID := testNanoID(t)
	escalatingTarget(t, ctx, h, targetID, entityID, budget)

	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_a": true,
		"inflight_a": false, "maxretries_a": budget,
	}
	issueKey := issueKeyGapEntity(targetID, entityID, "missing_a")

	js := h.conn.JetStream()
	if err := js.DeleteStream(ctx, "core-operations"); err != nil {
		t.Fatalf("delete ops stream: %v", err)
	}
	// The publish has no responder with the stream gone, so it needs a deadline
	// of its own or it would block until this test's context expired.
	pubCtx, pubCancel := context.WithTimeout(ctx, 2*time.Second)
	defer pubCancel()
	if dec := h.engine.handleRow(pubCtx, h.rowMessage(t, targetID, entityID, row, 7, 1)); dec == substrate.Ack {
		t.Fatalf("a publish failure must not Ack the row — the redelivery it asks for is the retry")
	}
	// The op reaches the wire; what the deleted stream loses is the
	// acknowledgement, so the engine cannot know whether it landed. Drain it so
	// the assertion below is about the RETRY's publish, not this one's.
	h.nextOp(t)
	if h.engine.issues.standingAs(issueKey, "warning", codeGapEscalatedToAugur) {
		t.Fatalf("an escalation that never published must record nothing at %s, or its own retry is suppressed", issueKey)
	}

	// The retry the failure asked for. The failed attempt left a mark holding the
	// gap; the lease expiring is what re-opens it, which is the state the re-fire
	// arm acts on — and with no standing fact in the way it escalates and records
	// one.
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: "core-operations", Subjects: []string{"ops.>"},
	}); err != nil {
		t.Fatalf("restore ops stream: %v", err)
	}
	expireMark(t, ctx, h, targetID, entityID, "missing_a")
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 8, 1)); dec != substrate.Ack {
		t.Fatalf("retry delivery: decision = %v, want Ack", dec)
	}
	if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
		t.Fatalf("the retry must escalate, got %v", op["operationType"])
	}
	if !h.engine.issues.standingAs(issueKey, "warning", codeGapEscalatedToAugur) {
		t.Fatalf("the retry that DID fire must record the fact, issues = %+v", h.engine.issues.snapshot())
	}
}
