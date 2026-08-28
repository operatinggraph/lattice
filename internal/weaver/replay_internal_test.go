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

// TestEscalateExhaustedGap_LiveEscalationIsNotRePaidAndADeadOneIsRetried is
// T11, and it pins BOTH halves of the bound — because they trade off directly
// and a fix for either one alone silently destroys the other.
//
// The cost T11 names is real: every derivation of the same exhaustion reaches
// this function — a decline-floor redelivery, a sweep pass, one row of an
// operator's ReplayTarget — and a re-fire mints a fresh reasoning episode at a
// real per-call price. What bounds it is the escalation mark's own LEASE, read
// here: while the mark is live the episode is in flight and the derivation costs
// one mark read and nothing else.
//
// The other half is why that bound is the lease and not a standing Health fact.
// A reasoning episode can DIE — its claim never converges, the bridge drops it —
// and nothing else in the engine re-derives it, so the expired-lease arm is that
// episode's only recovery. A suppression keyed on "an escalation was recorded
// for this gap" would be permanent, would sit above this read, and would retire
// that recovery from every leg at once: the gap would stay open and violating
// forever behind a warning.
//
// So: live mark ⇒ no second dispatch. Expired mark ⇒ re-escalate.
func TestEscalateExhaustedGap_LiveEscalationIsNotRePaidAndADeadOneIsRetried(t *testing.T) {
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

	// The first escalation: nothing is in flight, so the episode fires and the
	// standing record is written.
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

	// Half one — the replay, while that episode is genuinely in flight. This is
	// the delivery a ReplayTarget of the whole target produces for this row, and
	// it must cost no model call and leave the mark exactly as it found it.
	_, revBefore, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a")
	if err != nil || !found {
		t.Fatalf("the escalation's mark must be there (found=%v err=%v)", found, err)
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 8, 1)); dec != substrate.Ack {
		t.Fatalf("replayed delivery: decision = %v, want Ack", dec)
	}
	h.requireNoOp(t)
	_, revAfter, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a")
	if err != nil || !found || revAfter != revBefore {
		t.Fatalf("a live escalation's mark must survive the replay untouched (found=%v rev=%v want=%v err=%v)",
			found, revAfter, revBefore, err)
	}

	// Half two — the same delivery once that episode is presumed dead. The lease
	// has expired, so the gap is owed a fresh escalation and gets one; the
	// standing record must not have turned into a gag.
	expireMark(t, ctx, h, targetID, entityID, "missing_a")
	if !h.engine.issues.standingAs(issueKey, "warning", codeGapEscalatedToAugur) {
		t.Fatalf("the record must still stand — this half is about it NOT suppressing the retry")
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 9, 1)); dec != substrate.Ack {
		t.Fatalf("lease-expired delivery: decision = %v, want Ack", dec)
	}
	if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
		t.Fatalf("a dead escalation episode must be re-fired — it has no other recovery, got %v",
			op["operationType"])
	}
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a"); err != nil || !found {
		t.Fatalf("the re-fired escalation must leave a fresh live mark, which is what paces the next "+
			"re-fire to one per lease (found=%v err=%v)", found, err)
	}

	// And the record retires where the fact ends, so a later exhaustion of a
	// re-opened gap is reported afresh rather than inheriting this one's age.
	h.engine.retireClosedGapIssues(targetID, entityID, "missing_a")
	if h.engine.issues.standingAs(issueKey, "warning", codeGapEscalatedToAugur) {
		t.Fatalf("the gap closing must retire the record")
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

// TestEscalateExhaustedGap_PublishFailureRecordsNoRepublishObligation pins the
// withdrawal of a republish entry the escalation path would otherwise strand.
//
// `fire` records an obligation at the gap's key whenever a publish fails, which
// is right for an ordinary episode: dispatchGap reads that set and re-publishes
// the SAME episode on the redelivery the failure asked for. An exhausted gap
// never reaches dispatchGap — handleRow routes it here and continues — so the
// entry has no reader, and it does not merely idle: it burns one of the target's
// 256 slots, and the moment the gap becomes dispatchable again (an operator
// `resetBudget`, a lens raising maxretries_<g>) dispatchGap's live-mark arm falls
// through on `owes` and re-publishes an episode against the escalation's own
// mark. Past the Contract #4 tracker's horizon that is a second real dispatch.
//
// The escalation's retry is the lease-expiry re-fire, not the republish set, so
// nothing is lost by withdrawing the entry — and this test is what keeps a
// future edit from re-adding it by making `fire` generic again.
func TestEscalateExhaustedGap_PublishFailureRecordsNoRepublishObligation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureEscalateNoRepublish"
	const budget = 2
	entityID := testNanoID(t)
	escalatingTarget(t, ctx, h, targetID, entityID, budget)

	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_a": true,
		"inflight_a": false, "maxretries_a": budget,
	}

	if err := h.conn.JetStream().DeleteStream(ctx, "core-operations"); err != nil {
		t.Fatalf("delete ops stream: %v", err)
	}
	pubCtx, pubCancel := context.WithTimeout(ctx, 2*time.Second)
	defer pubCancel()
	if dec := h.engine.handleRow(pubCtx, h.rowMessage(t, targetID, entityID, row, 7, 1)); dec == substrate.Ack {
		t.Fatalf("a publish failure must not Ack the row")
	}
	if h.engine.republish.owes(targetID, entityID, "missing_a") {
		t.Fatalf("an escalation's failed publish must leave no republish obligation: nothing reads it while " +
			"the gap is exhausted, and it re-publishes against the escalation's own mark once the gap is " +
			"dispatchable again")
	}
}

// TestClearClosedMarks_TombstoneRetiresAnOrphanColumnsGapIssue pins the `gap:`
// prefix clear on the entity-deletion branch, and the vector is the one the
// per-key walk provably cannot reach.
//
// `gap:` entries are raised at whatever openGapColumns enumerated — every true
// missing_* column, WHETHER OR NOT the playbook names it. The tombstone branch's
// candidate walk is markCandidateColumns(target, nil), which for an empty body is
// just the playbook's own keys. So an entry raised at a column the playbook has
// since dropped is retired by no per-key clear once the entity is gone: the row
// that would re-derive it no longer exists, and the walk no longer names it. The
// prefix clear is its only retirement, and without it the entry stands for the
// process's lifetime holding one of the target's per-row budget slots.
func TestClearClosedMarks_TombstoneRetiresAnOrphanColumnsGapIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureTombstoneOrphanGap"
	entityID := testNanoID(t)
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_a": {Action: actionSurface, IssueCode: "UnroutedTasks"}},
	})

	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_a": true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 7, 1)); dec != substrate.Ack {
		t.Fatalf("surface delivery: decision = %v, want Ack", dec)
	}
	issueKey := issueKeyGapEntity(targetID, entityID, "missing_a")
	if !h.engine.issues.standingAs(issueKey, "warning", "UnroutedTasks") {
		t.Fatalf("a surface gap standing open must raise at %s, issues = %+v", issueKey, h.engine.issues.snapshot())
	}

	// The package re-author drops the column. The entry stays raised — nothing
	// about a playbook edit is evidence that THIS row's gap closed — but it is
	// now at a column the candidate walk will not name for an empty body.
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_b": {Action: actionDirectOp, Operation: "FixB"}},
	})

	// The entity is deleted: an empty body is the §10.2 tombstone.
	tombstone := substrate.Message{
		Subject:  h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Sequence: 8,
	}
	if dec := h.engine.handleRow(ctx, tombstone); dec != substrate.Ack {
		t.Fatalf("tombstone delivery: decision = %v, want Ack", dec)
	}
	if h.engine.issues.standingAs(issueKey, "warning", "UnroutedTasks") {
		t.Fatalf("a deleted entity must retire its gap: entries; %s survives an entity that no longer exists "+
			"and nothing can ever reach it again", issueKey)
	}
}
