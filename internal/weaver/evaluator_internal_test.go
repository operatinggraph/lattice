package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
)

// handlerHarness is an Engine wired to an embedded NATS server with its
// registry seeded directly, so handleRow can be driven with constructed
// substrate.Message values (controlled Sequence/NumDelivered — the metadata
// branches a live consumer cannot script).
type handlerHarness struct {
	engine *Engine
	conn   *substrate.Conn
	ops    *nats.Subscription
}

func newHandlerHarness(t *testing.T, ctx context.Context) *handlerHarness {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	srv := natstest.RunServer(opts)
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
	// LimitMarkerTTL mirrors bootstrap provisioning: weaver-state marks carry a
	// per-key TTL, which the server only honours on a TTL-capable bucket.
	if _, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "weaver-state", LimitMarkerTTL: time.Second}); err != nil {
		t.Fatalf("create weaver-state: %v", err)
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
		Instance: "unit-" + testNanoID(t),
		Logger:   discardLogger(),
	})
	return &handlerHarness{engine: engine, conn: conn, ops: ops}
}

func (h *handlerHarness) seedTarget(target *Target) {
	h.engine.source.mu.Lock()
	h.engine.source.targets[target.TargetID] = target
	h.engine.source.mu.Unlock()
}

func (h *handlerHarness) seedPattern(ref, vertexID string) {
	h.engine.source.mu.Lock()
	h.engine.source.patternMeta[ref] = "vtx.meta." + vertexID
	h.engine.source.mu.Unlock()
}

func (h *handlerHarness) rowMessage(t *testing.T, targetID, entityID string, row map[string]any, sequence, numDelivered uint64) substrate.Message {
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

func (h *handlerHarness) nextOp(t *testing.T) map[string]any {
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

func (h *handlerHarness) requireNoOp(t *testing.T) {
	t.Helper()
	if msg, err := h.ops.NextMsg(500 * time.Millisecond); err == nil {
		t.Fatalf("expected no op on ops.system, got: %s", string(msg.Data))
	}
}

// TestHandleRow_NumDeliveredBranches walks the in-flight-mark decision point:
// a FRESH delivery (NumDelivered 1) with an existing mark anti-storm drops; a
// REDELIVERY (NumDelivered > 1) with an existing mark re-publishes the SAME
// episode requestId; missing metadata (NumDelivered/Sequence 0) takes the
// conservative side — never the drop, never an expectedRevision of 0.
func TestHandleRow_NumDeliveredBranches(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureRetry"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		"missing_x": true,
	}

	// Fresh delivery, no mark: dispatches (creates the mark + fires).
	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
	if dec != substrate.Ack {
		t.Fatalf("initial dispatch must Ack, got %v", dec)
	}
	first := h.nextOp(t)
	_, markRev, inFlight, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x")
	if err != nil || !inFlight {
		t.Fatalf("mark must exist after dispatch (err=%v, inFlight=%v)", err, inFlight)
	}
	wantRequestID := deriveEpisodeRequestID(targetID, entityID, "missing_x", markRev)
	if first["requestId"] != wantRequestID {
		t.Fatalf("dispatch requestId = %v, want %v", first["requestId"], wantRequestID)
	}

	// Fresh delivery (NumDelivered 1) + existing mark: the anti-storm drop.
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 6, 1))
	if dec != substrate.Ack {
		t.Fatalf("anti-storm drop must Ack, got %v", dec)
	}
	h.requireNoOp(t)

	// Redelivery (NumDelivered 2) + existing mark: re-fires the SAME episode
	// requestId (idempotent at the Contract #4 tracker).
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 2))
	if dec != substrate.Ack {
		t.Fatalf("redelivery re-fire must Ack, got %v", dec)
	}
	refire := h.nextOp(t)
	if refire["requestId"] != wantRequestID {
		t.Fatalf("re-fire requestId = %v, want the same episode %v", refire["requestId"], wantRequestID)
	}

	// Metadata unavailable (Sequence 0, NumDelivered 0): defer on a delayed
	// redelivery — no anti-storm drop, no expectedRevision 0 published.
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 0, 0))
	if dec != substrate.NakWithDelay {
		t.Fatalf("metadata-less delivery must NakWithDelay, got %v", dec)
	}
	h.requireNoOp(t)

	// NumDelivered 0 with usable Sequence: not classified as fresh — the
	// possible-redelivery re-fires the same episode (the safe side).
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 7, 0))
	if dec != substrate.Ack {
		t.Fatalf("NumDelivered-0 re-fire must Ack, got %v", dec)
	}
	refire = h.nextOp(t)
	if refire["requestId"] != wantRequestID {
		t.Fatalf("NumDelivered-0 re-fire requestId = %v, want %v", refire["requestId"], wantRequestID)
	}
}

// TestHandleRow_UnresolvedReference proves an unresolvable playbook reference
// never hot-loops and never sits silent: the gap defers on NakWithDelay with
// an UnresolvedReference Health issue, no mark is claimed, and a later-
// installed pattern recovers on redelivery (issue cleared, episode fired).
func TestHandleRow_UnresolvedReference(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureGhost"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_y": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"},
		},
	})
	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		"missing_y": true,
	}

	// The pattern is not installed: defer with delay + surface to Health.
	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
	if dec != substrate.NakWithDelay {
		t.Fatalf("unresolved pattern ref must NakWithDelay, got %v", dec)
	}
	h.requireNoOp(t)
	if !hasIssueCode(h.engine.issues.snapshot(), "UnresolvedReference") {
		t.Fatalf("an unresolved reference must surface an UnresolvedReference Health issue")
	}
	if _, _, inFlight, err := h.engine.marks.get(ctx, targetID, entityID, "missing_y"); err != nil || inFlight {
		t.Fatalf("no mark may be claimed while the reference is unresolved (err=%v, inFlight=%v)", err, inFlight)
	}

	// The pattern is installed later: the redelivery resolves, fires, and
	// clears the issue.
	patternVtx := testNanoID(t)
	h.seedPattern("ghostFlow", patternVtx)
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 2))
	if dec != substrate.Ack {
		t.Fatalf("resolved redelivery must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "StartLoomPattern" {
		t.Fatalf("expected StartLoomPattern, got %v", op["operationType"])
	}
	if hasIssueCode(h.engine.issues.snapshot(), "UnresolvedReference") {
		t.Fatalf("the UnresolvedReference issue must clear once the reference resolves")
	}
}

// issueSeverity returns the severity of the issue with the given code, or "" if
// no such issue is active.
func issueSeverity(issues []healthIssue, code string) string {
	for _, i := range issues {
		if i.Code == code {
			return i.Severity
		}
	}
	return ""
}

// TestHandleRow_MalformedAnchorDegradesNotErrors pins the Contract #5 §5.2
// severity of a single malformed/incomplete anchor row: a per-row DATA error
// (a template reference that resolves null, or a violating row missing its
// entityKey echo) is surfaced as a `warning` (degraded) and the row is skipped
// (Ack, no op) — it must NOT raise an `error` (unhealthy) and pin the whole
// Weaver component red while every other row still remediates.
func TestHandleRow_MalformedAnchorDegradesNotErrors(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("template reference resolves null", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureNullSubject"
		// The pattern IS installed, so the only failure is the null subject —
		// isolating the TemplateDataError path from UnresolvedReference.
		h.seedPattern("onboardFlow", testNanoID(t))
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps: map[string]GapAction{
				"missing_onboarding": {Action: actionTriggerLoom, Pattern: "onboardFlow", Subject: "row.applicant"},
			},
		})
		entityID := testNanoID(t)
		row := map[string]any{
			"entityKey":          "vtx.leaseapp." + entityID,
			"violating":          true,
			"missing_onboarding": true,
			// no "applicant" column — the malformed/bare anchor case
		}

		dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
		if dec != substrate.Ack {
			t.Fatalf("a malformed-row data error must Ack (skip), got %v", dec)
		}
		h.requireNoOp(t)
		if sev := issueSeverity(h.engine.issues.snapshot(), "TemplateDataError"); sev != "warning" {
			t.Fatalf("a null template reference must surface a `warning` (degraded) TemplateDataError, got %q", sev)
		}
	})

	t.Run("violating row missing entityKey", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureNoEntityKey"
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps: map[string]GapAction{
				"missing_y": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"},
			},
		})
		entityID := testNanoID(t)
		row := map[string]any{
			"violating": true,
			"missing_y": true,
			// no "entityKey" — the candidate is unidentifiable
		}

		dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
		if dec != substrate.Ack {
			t.Fatalf("a row missing entityKey must Ack (skip), got %v", dec)
		}
		h.requireNoOp(t)
		if sev := issueSeverity(h.engine.issues.snapshot(), "RowDataError"); sev != "warning" {
			t.Fatalf("a violating row missing entityKey must surface a `warning` (degraded) RowDataError, got %q", sev)
		}
	})
}

// TestGapSuppressed_Companions unit-tests the dispatch-suppression gate's
// inflight (row) term and its budget term over the row's maxretries_<g> with a
// dispatch-count of zero: a gap is suppressed iff inflight_<g> reads true, while
// an absent/non-bool inflight, an absent/non-positive maxretries, and a column
// without the missing_ prefix all read NOT-suppressed (dispatch proceeds — the
// safe default). The cap term firing on a non-zero count is covered by
// TestGapSuppressed_BudgetCap.
func TestGapSuppressed_Companions(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)
	entityID := testNanoID(t)

	cases := []struct {
		name          string
		row           map[string]any
		col           string
		want          bool
		wantExhausted bool
	}{
		{"no companions", map[string]any{"missing_x": true}, "missing_x", false, false},
		{"inflight true", map[string]any{"missing_x": true, "inflight_x": true}, "missing_x", true, false},
		{"inflight false, zero count under cap", map[string]any{"missing_x": true, "inflight_x": false, "maxretries_x": 3}, "missing_x", false, false},
		{"non-bool inflight reads false", map[string]any{"missing_x": true, "inflight_x": "yes", "maxretries_x": 3}, "missing_x", false, false},
		{"non-positive cap never suppresses", map[string]any{"missing_x": true, "maxretries_x": 0}, "missing_x", false, false},
		{"non-gap column never suppressed", map[string]any{"inflight_x": true}, "violating", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, gotExhausted := h.engine.gapSuppressed(ctx, "t1", entityID, tc.row, tc.col)
			if got != tc.want || gotExhausted != tc.wantExhausted {
				t.Fatalf("gapSuppressed(%v, %q) = (%v, %v), want (%v, %v)", tc.row, tc.col, got, gotExhausted, tc.want, tc.wantExhausted)
			}
		})
	}
}

// TestGapSuppressed_BudgetCap unit-tests the §E mechanism-B budget term: with
// inflight false and a row cap of maxretries_x, the gate suppresses iff the
// weaver-state dispatch-count for (target, entity, gap) has REACHED the cap. The
// count is seeded via the real markStore (incrementDispatchCount), and a gap-close
// reset (deleteDispatchCount) drops it back below the cap → dispatchable again
// (the reset that B exists for, at the gate level).
func TestGapSuppressed_BudgetCap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "tCap"
	entityID := testNanoID(t)
	row := map[string]any{"missing_x": true, "maxretries_x": 3}

	// Zero count: under cap → not suppressed.
	if suppressed, exhausted := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x"); suppressed || exhausted {
		t.Fatalf("a zero dispatch-count under the cap must not suppress (got suppressed=%v exhausted=%v)", suppressed, exhausted)
	}
	// Drive the count to cap-1: still under → not suppressed.
	for i := 0; i < 2; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("increment dispatch-count: %v", err)
		}
	}
	if suppressed, exhausted := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x"); suppressed || exhausted {
		t.Fatalf("a dispatch-count of cap-1 must not suppress (one more attempt allowed) (got suppressed=%v exhausted=%v)", suppressed, exhausted)
	}
	// One more → count == cap: suppressed AND exhausted (the escalation-eligible
	// reason, distinct from inflight).
	if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("increment dispatch-count: %v", err)
	}
	if suppressed, exhausted := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x"); !suppressed || !exhausted {
		t.Fatalf("a dispatch-count at the cap must suppress AND report exhausted=true (budget spent) (got suppressed=%v exhausted=%v)", suppressed, exhausted)
	}
	// The gap-close reset deletes the count → dispatchable again (fresh budget).
	if err := h.engine.marks.deleteDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("delete dispatch-count: %v", err)
	}
	if suppressed, exhausted := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x"); suppressed || exhausted {
		t.Fatalf("after the gap-close reset the budget must be fresh → not suppressed (got suppressed=%v exhausted=%v)", suppressed, exhausted)
	}
}

// TestHandleRow_InflightSuppressesDispatch proves skip site 1 (the lane-1 dispatch
// loop): a violating row whose gap carries inflight_<g>=true is NOT dispatched —
// no op fires and no in-flight mark is created — while the gap stays violating.
// A row whose companion clears (the call resolved or timed out) then dispatches
// normally.
func TestHandleRow_InflightSuppressesDispatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureInflight"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)

	suppressed := map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"violating":  true,
		"missing_x":  true,
		"inflight_x": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, suppressed, 1, 1)); dec != substrate.Ack {
		t.Fatalf("a suppressed-gap row must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil || found {
		t.Fatalf("no mark may be created while inflight_x suppresses dispatch (err=%v, found=%v)", err, found)
	}

	// The in-flight companion clears (call resolved/timed-out): dispatch resumes.
	resumed := map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"violating":  true,
		"missing_x":  true,
		"inflight_x": false,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, resumed, 2, 1)); dec != substrate.Ack {
		t.Fatalf("a non-suppressed row must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("dispatch must resume once inflight_x clears; got op %v", op["operationType"])
	}
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil || !found {
		t.Fatalf("a mark must be created once dispatch resumes (err=%v, found=%v)", err, found)
	}
}

// TestHandleRow_BudgetIncrementsThenSuppresses proves the §E mechanism-B budget
// end-to-end through lane-1: each dispatch increments the weaver-state
// dispatch-count, and once the count reaches the row's maxretries_<g> the gap is
// no longer auto-dispatched (no op, no NEW mark) — the "stop and escalate"
// terminal — while the gap stays violating. The mark is cleared between attempts
// (as the sweep would after a lease lapse) so each fresh delivery re-dispatches
// and re-increments, the way a real retry chain advances the count.
func TestHandleRow_BudgetIncrementsThenSuppresses(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureBudget"
	const cap = 3
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	// inflight is false throughout; the cap rides the row as maxretries_x.
	row := map[string]any{
		"entityKey":    "vtx.leaseApp." + entityID,
		"violating":    true,
		"missing_x":    true,
		"inflight_x":   false,
		"maxretries_x": cap,
	}

	// cap dispatches, each preceded by clearing the prior mark (the sweep/level
	// clear after a lapse) so the next delivery is a fresh CAS-create + increment.
	for i := 1; i <= cap; i++ {
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, uint64(i), 1)); dec != substrate.Ack {
			t.Fatalf("attempt %d must Ack, got %v", i, dec)
		}
		h.nextOp(t) // the dispatch op fired
		if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != i {
			t.Fatalf("dispatch-count after attempt %d = %d (err=%v), want %d", i, got, err, i)
		}
		// Clear the mark so the next delivery dispatches afresh (the count
		// PERSISTS across this clear — it is chain-scoped, not mark-bound).
		if err := h.engine.marks.delete(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("clear mark between attempts: %v", err)
		}
	}

	// The budget is now spent (count == cap): the next delivery suppresses — no op,
	// no new mark — but the gap stays violating.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, uint64(cap+1), 1)); dec != substrate.Ack {
		t.Fatalf("an exhausted-budget row must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil || found {
		t.Fatalf("no mark may be created once the budget is spent (err=%v, found=%v)", err, found)
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != cap {
		t.Fatalf("a suppressed delivery must not increment the count: got %d (err=%v), want %d", got, err, cap)
	}
}

// TestHandleRow_ExhaustedGapEscalatesToAugur proves the Fire 9 wiring at
// lane-1's suppression site (weaver-exhausted-escalation-and-model): a gap
// whose retry budget is spent (maxretries_<g> reached) on a target that
// escalates "exhausted" fires a CreateAugurReasoningClaim op — NOT its normal,
// now-exhausted action — through the standard dispatch path, and never raises
// GapBudgetExhausted (escalation is a live remediation avenue, not a park).
func TestHandleRow_ExhaustedGapEscalatesToAugur(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureExhaustedAugur"
	id := testNanoID(t)
	spec := targetSpecFixture(targetID) // declares gaps.missing_a -> directOp FixA
	spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
	h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
	h.engine.source.handle(specEvent(t, id, spec))

	entityID := testNanoID(t)
	const cap = 2
	for i := 0; i < cap; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_a"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_a": true,
		"inflight_a": false, "maxretries_a": cap,
	}

	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 7, 1)); dec != substrate.Ack {
		t.Fatalf("decision = %v, want Ack", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != defaultAugurOp {
		t.Fatalf("operationType = %v, want %q (the escalation, not the exhausted FixA action)", op["operationType"], defaultAugurOp)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
		t.Fatalf("escalating must never raise GapBudgetExhausted, issues = %+v", h.engine.issues.snapshot())
	}
}

// TestHandleRow_LiveEscalationMarkNotTornDownAndRefired proves the fix for a
// real bug caught in review: the LENS never learns an escalation is running
// (inflight_<g> is a lens-authored companion of the gap's NORMAL action, and
// an Augur escalation is a different action class entirely — the row keeps
// reporting inflight_a=false and missing_a=true for as long as the escalated
// gap stays open), so gapSuppressed keeps reporting exhausted=true on EVERY
// subsequent delivery of this still-violating row. Without a leaseLive check,
// escalateExhaustedGap would tear down and re-fire a brand-new escalation
// episode on every single redelivery — a self-inflicted storm. A LIVE mark
// (the escalation this function already fired, still within its lease) must
// be left alone, exactly like the ordinary inflight case.
func TestHandleRow_LiveEscalationMarkNotTornDownAndRefired(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureExhaustedAugurLive"
	id := testNanoID(t)
	spec := targetSpecFixture(targetID) // declares gaps.missing_a -> directOp FixA
	spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
	h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
	h.engine.source.handle(specEvent(t, id, spec))

	entityID := testNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	const cap = 2
	for i := 0; i < cap; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_a"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	// Simulate an escalation episode this function already fired: a LIVE
	// mark (fresh lease) at the exact key the escalation dispatches under.
	liveRev, _, _, err := h.engine.marks.create(ctx, targetID, entityID, "missing_a", entityKey, actionDirectOp)
	if err != nil {
		t.Fatalf("seed live escalation mark: %v", err)
	}
	row := map[string]any{
		"entityKey": entityKey, "violating": true, "missing_a": true,
		"inflight_a": false, "maxretries_a": cap,
	}

	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 7, 1)); dec != substrate.Ack {
		t.Fatalf("decision = %v, want Ack", dec)
	}
	h.requireNoOp(t)
	if _, markRev, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a"); err != nil || !found || markRev != liveRev {
		t.Fatalf("the live escalation mark must survive untouched (found=%v rev=%v want=%v err=%v)", found, markRev, liveRev, err)
	}
}

// TestHandleRow_ExhaustedGapWithoutAugurRaisesHealthIssue proves the §10.8
// "never a silent park" promise when no augur policy escalates "exhausted":
// no op fires, and a standing GapBudgetExhausted issue is raised — the
// visible signal this design replaces the bare, invisible `continue` with.
func TestHandleRow_ExhaustedGapWithoutAugurRaisesHealthIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureExhaustedNoAugur"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	const cap = 2
	for i := 0; i < cap; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
		"inflight_x": false, "maxretries_x": cap,
	}

	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 9, 1)); dec != substrate.Ack {
		t.Fatalf("decision = %v, want Ack", dec)
	}
	h.requireNoOp(t)
	if !hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
		t.Fatalf("expected a standing GapBudgetExhausted issue, issues = %+v", h.engine.issues.snapshot())
	}
	if sev := issueSeverity(h.engine.issues.snapshot(), "GapBudgetExhausted"); sev != "warning" {
		t.Fatalf("GapBudgetExhausted severity = %q, want warning", sev)
	}
}

// TestHandleRow_BudgetResetsOnGapClose is the escape-hatch / reset-on-success
// proof (the headline of mechanism B): drive a chain to the cap (no further
// dispatch), then a delivery whose gap is CLOSED (missing_x=false — a completed
// check) → clearClosedMarks deletes the dispatch-count → a subsequent REOPEN of
// the gap is dispatchable again from a fresh budget.
func TestHandleRow_BudgetResetsOnGapClose(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureBudgetReset"
	const cap = 3
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)

	// Seed the count straight to the cap (the gate's view of a spent chain).
	for i := 0; i < cap; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	open := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true,
		"missing_x": true, "inflight_x": false, "maxretries_x": cap,
	}
	// At the cap with the gap open: suppressed (no dispatch).
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
		t.Fatalf("at-cap open row must Ack, got %v", dec)
	}
	h.requireNoOp(t)

	// The check completes → the gap CLOSES: clearClosedMarks deletes the count.
	closed := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": false,
		"missing_x": false, "inflight_x": false, "maxretries_x": cap,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, closed, 2, 1)); dec != substrate.Ack {
		t.Fatalf("gap-close row must Ack, got %v", dec)
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != 0 {
		t.Fatalf("the gap-close must reset the dispatch-count: got %d (err=%v), want 0", got, err)
	}

	// The gap REOPENS (a later renewal): the budget is fresh, so it dispatches.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 3, 1)); dec != substrate.Ack {
		t.Fatalf("reopened row must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "FixX" {
		t.Fatalf("a reopened gap on a fresh budget must dispatch; got op %v", op["operationType"])
	}
	if got, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil || got != 1 {
		t.Fatalf("the fresh-budget redispatch must restart the count at 1: got %d (err=%v)", got, err)
	}
}

// TestHandleRow_EffectDispatchAndClose proves the lane-1 half of the §10.3
// `__effect` confidence window (weaver-planner-mandate design §3.2, Fire 2):
// a fresh CAS-create-won dispatch appends a pending entry keyed by the gap's
// playbook action, and the level-reconciled close path (clearClosedMarks)
// flips it — read from the mark's Action BEFORE the mark is deleted, so the
// close lands against the SAME actionRef the dispatch recorded.
func TestHandleRow_EffectDispatchAndClose(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureEffect"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	open := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
		t.Fatalf("fresh dispatch must Ack, got %v", dec)
	}
	h.nextOp(t) // the dispatch op fired

	stats, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, "missing_x", actionDirectOp)
	if err != nil || !ok {
		t.Fatalf("read effect stats after dispatch: err=%v ok=%v", err, ok)
	}
	if len(stats.Window) != 1 || stats.Window[0] {
		t.Fatalf("window after one fresh dispatch = %v, want [false] (pending)", stats.Window)
	}

	closed := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": false, "missing_x": false,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, closed, 2, 1)); dec != substrate.Ack {
		t.Fatalf("gap-close row must Ack, got %v", dec)
	}
	stats, _, ok, err = readEffectStats(ctx, h.engine.marks, targetID, "missing_x", actionDirectOp)
	if err != nil || !ok {
		t.Fatalf("read effect stats after close: err=%v ok=%v", err, ok)
	}
	if len(stats.Window) != 1 || !stats.Window[0] {
		t.Fatalf("window after close = %v, want [true]", stats.Window)
	}
}

// TestClearClosedMarks_ConcurrentCloseCreditsEffectOnce is the regression for
// the lane-1/sweep `__effect` double-credit: two paths clearing the SAME closed
// gap each used to record a close, because the credit was gated on the mark
// being FOUND at read time, not on winning its delete. Both reading found=true
// before either deleted, both credited — inflating the confidence window's
// close count and masking a real LensEffectMismatch. Revision-conditioning the
// delete makes exactly one concurrent path win, so exactly one close is
// credited. The window is made two-deep (a fresh dispatch plus one reclaim
// re-fire) so a double-credit is observable: one close must flip exactly one
// pending slot, never both.
func TestClearClosedMarks_ConcurrentCloseCreditsEffectOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	// A fresh targetID per iteration keeps each (target, gap, action) `__effect`
	// window independent — the window key carries no entityID, so reusing a
	// target would let iterations accumulate into one window.
	for i := 0; i < 20; i++ {
		targetID := fmt.Sprintf("fixtureRace%d", i)
		target := &Target{
			TargetID: targetID,
			Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
		}
		h.seedTarget(target)
		entityID := testNanoID(t)
		open := map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
		}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
			t.Fatalf("iter %d: fresh dispatch must Ack, got %v", i, dec)
		}
		h.nextOp(t) // drain the dispatch op

		// A second dispatch of the still-open gap (as the sweep's reclaim re-fires
		// an expired mark, the mark surviving) makes the window two-deep.
		if err := h.engine.marks.recordEffectDispatch(ctx, targetID, "missing_x", actionDirectOp); err != nil {
			t.Fatalf("iter %d: second dispatch record: %v", i, err)
		}

		closed := map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": false, "missing_x": false,
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		for g := 0; g < 2; g++ {
			go func() {
				defer wg.Done()
				<-start
				h.engine.clearClosedMarks(ctx, target, targetID, entityID, closed)
			}()
		}
		close(start) // release both at once to maximise overlap
		wg.Wait()

		stats, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, "missing_x", actionDirectOp)
		if err != nil || !ok {
			t.Fatalf("iter %d: read effect stats: err=%v ok=%v", i, err, ok)
		}
		closedCount := 0
		for _, w := range stats.Window {
			if w {
				closedCount++
			}
		}
		if closedCount != 1 {
			t.Fatalf("iter %d: window %v credited %d closes, want exactly 1 (double-credit regression)",
				i, stats.Window, closedCount)
		}
	}
}

// TestHandleRow_SurfaceGap proves FR29's "surface, never dispatch" gap
// (actionSurface): a violating row raises the named Health issue at the
// declared severity and dispatches NO op and creates NO mark; when the row
// stops naming the gap, the issue clears via clearClosedMarks — with no mark
// ever having existed to clean up.
func TestHandleRow_SurfaceGap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "unroutedTasks"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_claim": {Action: actionSurface, IssueCode: "UnroutedTasks", IssueSeverity: "warning"},
		},
	})
	entityID := testNanoID(t)
	open := map[string]any{
		"entityKey": "vtx.task." + entityID, "violating": true, "missing_claim": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
		t.Fatalf("surface gap dispatch must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	issues := h.engine.issues.snapshot()
	if !hasIssueCode(issues, "UnroutedTasks") {
		t.Fatalf("expected an UnroutedTasks issue, got %v", issues)
	}
	if sev := issueSeverity(issues, "UnroutedTasks"); sev != "warning" {
		t.Fatalf("UnroutedTasks severity = %q, want warning", sev)
	}
	if _, _, inFlight, err := h.engine.marks.get(ctx, targetID, entityID, "missing_claim"); err != nil || inFlight {
		t.Fatalf("surface gap must never create a mark (err=%v, inFlight=%v)", err, inFlight)
	}

	closed := map[string]any{
		"entityKey": "vtx.task." + entityID, "violating": false, "missing_claim": false,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, closed, 2, 1)); dec != substrate.Ack {
		t.Fatalf("gap-close row must Ack, got %v", dec)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "UnroutedTasks") {
		t.Fatal("expected UnroutedTasks issue to clear once the gap closes")
	}
}
