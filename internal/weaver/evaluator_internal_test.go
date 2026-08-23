package weaver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
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
	srv := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, srv.ClientURL())
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

// issueAt returns the active issue held at an exact issueCache key. Keyed
// lookup, not code lookup: the per-entity issue classes raise the SAME code for
// every subject, so only the key tells one subject's issue from another's.
func issueAt(c *issueCache, key string) (healthIssue, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	is, ok := c.issues[key]
	return is, ok
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
		// A row's own template data is that row's fact: keyed per entity, so a
		// sibling row resolving cleanly cannot retire it.
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "missing_onboarding")); !ok {
			t.Fatalf("the TemplateDataError must be keyed to the row that carries it, issues = %+v",
				h.engine.issues.snapshot())
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
// safe default). Every case passes a non-"directOp" action (actionAssignTask)
// so this proves the GENERAL mechanism independent of defaultDirectOpRetryBudget
// (whose own fallback is TestGapSuppressed_DirectOpDefaultBudget); the cap term
// firing on a non-zero count is covered by TestGapSuppressed_BudgetCap.
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
			got, gotExhausted, gotDefault := h.engine.gapSuppressed(ctx, "t1", entityID, tc.row, tc.col, actionAssignTask)
			if got != tc.want || gotExhausted != tc.wantExhausted {
				t.Fatalf("gapSuppressed(%v, %q) = (%v, %v), want (%v, %v)", tc.row, tc.col, got, gotExhausted, tc.want, tc.wantExhausted)
			}
			if gotDefault {
				t.Fatalf("a non-directOp action must never report budgetIsDefault=true")
			}
		})
	}
}

// inflightMismatchMessage returns the InflightActionMismatch issue message open
// for targetID, or "" when none is. Each caller uses its own targetID, so the
// shared issue cache cannot leak one case's alert into another's assertion.
func inflightMismatchMessage(e *Engine, targetID string) string {
	for _, issue := range e.issues.snapshot() {
		if issue.Code == "InflightActionMismatch" && strings.Contains(issue.Message, "target "+targetID+":") {
			return issue.Message
		}
	}
	return ""
}

// TestStaleMark_ExternalDispatchClassifier unit-tests the Contract #10 §10.3
// external-gap classifier behind staleMark: a declared inflight_<g> is trusted
// only for a dispatch that concludes on an EXTERNAL call's outcome, and that is
// decided by the dispatch's real shape, not by its action name. directOp
// qualifies outright. A triggerLoom qualifies iff EVERY indexed step is a kind
// known not to park — the externalTask-only shape lease-signing's
// backgroundCheck/collectPayment gaps use, whose post-timeout retry depends on
// reading external here.
//
// The not-external paths are surfaced differently, and the assertion is on the
// operator-visible MESSAGE rather than merely on an alert existing: a permanent
// mismatch (a parking pattern, a step kind this build does not recognise, or an
// action that never calls out) raises an `error` Health issue naming which,
// while an unindexed pattern is transient replay lag and must raise none at all.
func TestStaleMark_ExternalDispatchClassifier(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	// Real pattern specs indexed through indexPattern in the CDC aspect envelope
	// (seedPatternSpec), so the classification comes from the registry's own
	// step-kind probe over the real wire shape.
	seedPatternSpec(t, h.engine.source, "bgcheckFlow", stepKindExternalTask)
	seedPatternSpec(t, h.engine.source, "onboardFlow", stepKindSystemOp, stepKindUserTask)
	seedPatternSpec(t, h.engine.source, "futureFlow", "agentTask")

	const col = "missing_x"
	concluded := map[string]any{col: true, "inflight_x": false}
	inFlight := map[string]any{col: true, "inflight_x": true}

	cases := []struct {
		name string
		ga   GapAction
		row  map[string]any
		// wantStale is staleMark's verdict; wantAlertSubstr is the substring the
		// InflightActionMismatch message must contain ("" = no alert at all).
		wantStale       bool
		wantAlertSubstr string
	}{
		{"directOp, call concluded", GapAction{Action: actionDirectOp, Operation: "SetStatus"}, concluded, true, ""},
		{"proposedOp, call concluded", GapAction{Action: actionProposedOp}, concluded, true, ""},
		{"triggerLoom over an externalTask-only pattern", GapAction{Action: actionTriggerLoom, Pattern: "bgcheckFlow"}, concluded, true, ""},
		{"triggerLoom over an externalTask-only pattern, still in flight", GapAction{Action: actionTriggerLoom, Pattern: "bgcheckFlow"}, inFlight, false, ""},
		{"triggerLoom over a parking pattern", GapAction{Action: actionTriggerLoom, Pattern: "onboardFlow"}, concluded, false, `pattern "onboardFlow" is not externalTask-only`},
		{"triggerLoom over an unrecognised step kind", GapAction{Action: actionTriggerLoom, Pattern: "futureFlow"}, concluded, false, `pattern "futureFlow" is not externalTask-only`},
		{"triggerLoom over an unindexed pattern is transient, not an alert", GapAction{Action: actionTriggerLoom, Pattern: "neverInstalled"}, concluded, false, ""},
		{"assignTask never dispatches externally", GapAction{Action: actionAssignTask, Operation: "SignLease"}, concluded, false, `action "assignTask" never makes an external call`},
		{"no inflight_<g> declared", GapAction{Action: actionDirectOp}, map[string]any{col: true}, false, ""},
	}
	entityID := testNanoID(t)
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targetID := fmt.Sprintf("fixtureClassifier%d", i)
			if got := h.engine.staleMark(targetID, entityID, tc.row, col, tc.ga); got != tc.wantStale {
				t.Fatalf("staleMark(%+v, %v) = %v, want %v", tc.ga, tc.row, got, tc.wantStale)
			}
			msg := inflightMismatchMessage(h.engine, targetID)
			switch {
			case tc.wantAlertSubstr == "" && msg != "":
				t.Fatalf("no InflightActionMismatch alert expected, got %q", msg)
			case tc.wantAlertSubstr != "" && !strings.Contains(msg, tc.wantAlertSubstr):
				t.Fatalf("InflightActionMismatch message = %q, want it to contain %q", msg, tc.wantAlertSubstr)
			}
		})
	}
}

// TestStaleMark_MismatchAlertSelfHeals proves the alert tracks a LIVE condition
// rather than latching: a triggerLoom gap over a parking pattern raises the
// InflightActionMismatch issue, and re-authoring that patternId with an
// externalTask-only spec — the package fix the message asks for — both clears
// the issue on the very next staleMark and flips the gap to external. The
// classifier's table test cannot see this, since every case there uses a fresh
// targetID; without it, deleting staleMark's e.issues.clear goes unnoticed.
func TestStaleMark_MismatchAlertSelfHeals(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureSelfHeal"
	const col = "missing_x"
	entityID := testNanoID(t)
	row := map[string]any{col: true, "inflight_x": false}
	ga := GapAction{Action: actionTriggerLoom, Pattern: "mixedFlow"}

	seedPatternSpec(t, h.engine.source, "mixedFlow", stepKindExternalTask, stepKindUserTask)
	if stale := h.engine.staleMark(targetID, entityID, row, col, ga); stale {
		t.Fatal("a parking pattern must not read as a concluded external gap")
	}
	if msg := inflightMismatchMessage(h.engine, targetID); msg == "" {
		t.Fatal("a misauthored inflight_<g> over a parking pattern must raise InflightActionMismatch")
	}

	// The package fix: the same patternId re-authored with the userTask removed.
	seedPatternSpec(t, h.engine.source, "mixedFlow", stepKindExternalTask)
	if stale := h.engine.staleMark(targetID, entityID, row, col, ga); !stale {
		t.Fatal("after the fix the gap must read as a concluded EXTERNAL gap")
	}
	if msg := inflightMismatchMessage(h.engine, targetID); msg != "" {
		t.Fatalf("the mismatch issue must clear once the gap classifies clean, still open: %q", msg)
	}
}

// TestGapSuppressed_BudgetCap unit-tests the §E mechanism-B budget term: with
// inflight false and a row cap of maxretries_x, the gate suppresses iff the
// weaver-state dispatch-count for (target, entity, gap) has REACHED the cap. The
// count is seeded via the real markStore (incrementDispatchCount), and a gap-close
// reset (deleteDispatchCount) drops it back below the cap → dispatchable again
// (the reset that B exists for, at the gate level). Uses a "directOp" action
// throughout to also prove the row's OWN declared cap always wins over
// defaultDirectOpRetryBudget when present — budgetIsDefault stays false at
// every step (the fallback-only case is TestGapSuppressed_DirectOpDefaultBudget).
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
	if suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionDirectOp); suppressed || exhausted || isDefault {
		t.Fatalf("a zero dispatch-count under the cap must not suppress (got suppressed=%v exhausted=%v isDefault=%v)", suppressed, exhausted, isDefault)
	}
	// Drive the count to cap-1: still under → not suppressed.
	for i := 0; i < 2; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("increment dispatch-count: %v", err)
		}
	}
	if suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionDirectOp); suppressed || exhausted || isDefault {
		t.Fatalf("a dispatch-count of cap-1 must not suppress (one more attempt allowed) (got suppressed=%v exhausted=%v isDefault=%v)", suppressed, exhausted, isDefault)
	}
	// One more → count == cap: suppressed AND exhausted (the escalation-eligible
	// reason, distinct from inflight) — via the row's OWN declared cap, not the
	// engine default.
	if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("increment dispatch-count: %v", err)
	}
	if suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionDirectOp); !suppressed || !exhausted || isDefault {
		t.Fatalf("a dispatch-count at the cap must suppress AND report exhausted=true via the DECLARED cap (got suppressed=%v exhausted=%v isDefault=%v)", suppressed, exhausted, isDefault)
	}
	// The gap-close reset deletes the count → dispatchable again (fresh budget).
	if err := h.engine.marks.deleteDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
		t.Fatalf("delete dispatch-count: %v", err)
	}
	if suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionDirectOp); suppressed || exhausted || isDefault {
		t.Fatalf("after the gap-close reset the budget must be fresh → not suppressed (got suppressed=%v exhausted=%v isDefault=%v)", suppressed, exhausted, isDefault)
	}
}

// TestGapSuppressed_DirectOpDefaultBudget unit-tests the cap-fallback at the
// gapSuppressed level: a "directOp" gap whose row declares NEITHER
// maxretries_<g> NOR inflight_<g> falls back to defaultDirectOpRetryBudget
// instead of reading "no cap" forever (Option D's ratified directOp-never-
// backs-off reclaim assumed every directOp declares a §10.3 bound; this is the
// engine's safety net for a target — orchestration-base's orphanedTaskGrants,
// wellness-domain's ReleaseOrphanedBooking — that declares neither column).
// The fallback is narrowly scoped: a non-"directOp" action never gets it
// regardless of count, and a "directOp" gap that DOES declare inflight_<g>
// (even absent maxretries_<g>) is left uncapped too — that lens has already
// opted into the §10.3 external-gap contract, whose pacing is its own call.
func TestGapSuppressed_DirectOpDefaultBudget(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	t.Run("neither column declared: caps at the engine default", func(t *testing.T) {
		const targetID = "tDirectOpDefault"
		entityID := testNanoID(t)
		row := map[string]any{"missing_x": true}

		for i := 0; i < defaultDirectOpRetryBudget-1; i++ {
			if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
				t.Fatalf("increment dispatch-count: %v", err)
			}
			if suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionDirectOp); suppressed || exhausted || isDefault {
				t.Fatalf("dispatch-count %d must stay under the engine default %d (got suppressed=%v exhausted=%v isDefault=%v)",
					i+1, defaultDirectOpRetryBudget, suppressed, exhausted, isDefault)
			}
		}
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
			t.Fatalf("increment dispatch-count: %v", err)
		}
		suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionDirectOp)
		if !suppressed || !exhausted || !isDefault {
			t.Fatalf("dispatch-count at the engine default %d must suppress, report exhausted, AND flag budgetIsDefault (got suppressed=%v exhausted=%v isDefault=%v)",
				defaultDirectOpRetryBudget, suppressed, exhausted, isDefault)
		}
	})

	t.Run("declares inflight_<g> but no maxretries: default never applies", func(t *testing.T) {
		const targetID = "tDirectOpInflightOnly"
		entityID := testNanoID(t)
		row := map[string]any{"missing_x": true, "inflight_x": false}

		for i := 0; i < defaultDirectOpRetryBudget+2; i++ {
			if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
				t.Fatalf("increment dispatch-count: %v", err)
			}
		}
		if suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionDirectOp); suppressed || exhausted || isDefault {
			t.Fatalf("a directOp gap declaring inflight_<g> (even absent maxretries_<g>) must never fall back to the engine default, got suppressed=%v exhausted=%v isDefault=%v", suppressed, exhausted, isDefault)
		}
	})

	t.Run("non-directOp action: no engine default regardless of count", func(t *testing.T) {
		const targetID = "tAssignTaskUncapped"
		entityID := testNanoID(t)
		row := map[string]any{"missing_x": true}

		for i := 0; i < defaultDirectOpRetryBudget+2; i++ {
			if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x"); err != nil {
				t.Fatalf("increment dispatch-count: %v", err)
			}
		}
		if suppressed, exhausted, isDefault := h.engine.gapSuppressed(ctx, targetID, entityID, row, "missing_x", actionAssignTask); suppressed || exhausted || isDefault {
			t.Fatalf("assignTask must never be capped by the directOp engine default, got suppressed=%v exhausted=%v isDefault=%v", suppressed, exhausted, isDefault)
		}
	})
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

// TestHandleRow_ExhaustedEscalationRetiresThisEntitysIssue pins the clear that
// pairs with the GapBudgetExhausted raise: once an augur policy takes the
// exhausted gap, the standing issue raised for THIS subject (before the policy
// existed) retires — at the entity key its raise used, so the escalation of one
// subject's gap never retires another's. Observed on its own: the live
// escalation mark returns before any plan is built, so the fresh-plan clear
// (which retires both scopes) never runs here.
func TestHandleRow_ExhaustedEscalationRetiresThisEntitysIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureExhaustedAugurRetire"
	id := testNanoID(t)
	spec := targetSpecFixture(targetID) // declares gaps.missing_a -> directOp FixA
	spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
	h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
	h.engine.source.handle(specEvent(t, id, spec))

	entityID := testNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	const budget = 2
	for i := 0; i < budget; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_a"); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	if _, _, _, err := h.engine.marks.create(ctx, targetID, entityID, "missing_a", entityKey, actionDirectOp); err != nil {
		t.Fatalf("seed live escalation mark: %v", err)
	}
	// Raised on an earlier pass, before the package added the augur policy.
	h.engine.issues.set(issueKeyGapEntity(targetID, entityID, "missing_a"), "warning", "GapBudgetExhausted",
		"target "+targetID+" entity "+entityID+": row column missing_a has exhausted its declared retry budget")

	row := map[string]any{
		"entityKey": entityKey, "violating": true, "missing_a": true,
		"inflight_a": false, "maxretries_a": budget,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 7, 1)); dec != substrate.Ack {
		t.Fatalf("decision = %v, want Ack", dec)
	}
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_a")); ok {
		t.Fatalf("the escalation covers this subject's exhausted gap; its issue must retire, issues = %+v",
			h.engine.issues.snapshot())
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

// TestHandleRow_RetractionTombstoneRetiresExhaustedIssue proves a standing
// GapBudgetExhausted issue clears when the gap closes by RETRACTION: the
// lens tombstones the row (guarded delete — the body is {isDeleted:true}
// with no gap columns at all), lane-1 delivers it, and clearClosedMarks
// retires the issue alongside the mark — a dispatched (non-surface) gap,
// where the issue would otherwise stand until a process restart.
func TestHandleRow_RetractionTombstoneRetiresExhaustedIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureExhaustRetire"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	h.engine.issues.set(issueKeyGapEntity(targetID, entityID, "missing_x"), "warning", "GapBudgetExhausted",
		"target "+targetID+" entity "+entityID+": row column missing_x has exhausted the engine's default retry budget")

	tombstone := map[string]any{"isDeleted": true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, tombstone, 1, 1)); dec != substrate.Ack {
		t.Fatalf("tombstone delivery must Ack, got %v", dec)
	}
	if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
		t.Fatal("a retraction tombstone closes the gap; its standing GapBudgetExhausted issue must retire with it")
	}
}

// TestHandleRow_RowDataIssueRetiresOnACleanRead is the bound on the per-entity
// data family. Its entries are keyed per (entity, column), and most of the
// columns the readers surface — violating, inflight_<g>, maxretries_<g>,
// admissionPriority — have no gap-close or plan-success path that would ever
// retire them. Without a retirement at the read itself, a lens projecting one
// of them with the wrong type across N rows would leave N entries standing for
// the process's lifetime, long after the lens was fixed: the heartbeat's
// listing cap bounds the DOCUMENT, never the cache behind it.
//
// The read is the retirement: the next projection carrying a usable value, or
// dropping the column, clears that row's entry.
func TestHandleRow_RowDataIssueRetiresOnACleanRead(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureDataRetire"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	repaired, dropped, deleted := testNanoID(t), testNanoID(t), testNanoID(t)
	all := []string{repaired, dropped, deleted}

	// A lens projecting `violating` as a string: one entry per row, and no gap
	// close or successful plan will ever retire any of them.
	for i, entityID := range all {
		dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": "true",
		}, uint64(i+1), 1))
		if dec != substrate.Ack {
			t.Fatalf("a non-bool violating column must Ack, got %v", dec)
		}
	}
	h.requireNoOp(t)
	for _, entityID := range all {
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "violating")); !ok {
			t.Fatalf("row %s must carry its own RowDataError; issues = %+v", entityID, h.engine.issues.snapshot())
		}
	}

	// Retirement 1: the lens is fixed and re-projects a real bool.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, repaired, map[string]any{
		"entityKey": "vtx.leaseApp." + repaired, "violating": false,
	}, 4, 1)); dec != substrate.Ack {
		t.Fatalf("the repaired row must Ack, got %v", dec)
	}
	// Retirement 2: the column is dropped from the row (the retraction shape —
	// a tombstone body carries no columns at all, and an absent column must
	// clear, never raise).
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, dropped, map[string]any{
		"isDeleted": true,
	}, 5, 1)); dec != substrate.Ack {
		t.Fatalf("the retraction tombstone must Ack, got %v", dec)
	}
	// Retirement 3: the entity is deleted outright (empty body). No value is
	// projected at all, so no reader runs — the level-reconciled pass retires
	// the row's whole data family instead.
	if dec := h.engine.handleRow(ctx, substrate.Message{
		Subject:      h.engine.rowSubjectPrefix + targetID + "." + deleted,
		Sequence:     6,
		NumDelivered: 1,
	}); dec != substrate.Ack {
		t.Fatalf("the entity-deletion tombstone must Ack, got %v", dec)
	}

	for _, entityID := range all {
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "violating")); ok {
			t.Fatalf("row %s read clean; its data error must not be retained, issues = %+v",
				entityID, h.engine.issues.snapshot())
		}
	}
	if n := len(h.engine.issues.snapshot()); n != 0 {
		t.Fatalf("every row read clean; the cache must retain nothing, got %+v", h.engine.issues.snapshot())
	}
}

// TestHandleRow_SurfaceGapIssueIsPerEntity proves a `surface` gap's Health
// issue states a fact about ONE ROW: two subjects violating the same
// (target, gap) concurrently raise two independent issues, and the one whose
// gap closes first retires ONLY its own. A target-scoped key made the two
// subjects share a single latch, so whichever half landed first cleared the
// issue standing for the subject still stuck — the wrong per-subject answer,
// and silently so.
func TestHandleRow_SurfaceGapIssueIsPerEntity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "unroutedTasks"
	const col = "missing_claim"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			col: {Action: actionSurface, IssueCode: "UnroutedTasks", IssueSeverity: "warning"},
		},
	})
	stuck, cleared := testNanoID(t), testNanoID(t)
	open := func(entityID string) map[string]any {
		return map[string]any{"entityKey": "vtx.task." + entityID, "violating": true, col: true}
	}
	for i, entityID := range []string{stuck, cleared} {
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open(entityID), uint64(i+1), 1)); dec != substrate.Ack {
			t.Fatalf("surface gap dispatch must Ack, got %v", dec)
		}
	}
	h.requireNoOp(t)
	if n := len(h.engine.issues.snapshot()); n != 2 {
		t.Fatalf("two subjects violating the same gap must raise two issues, got %d: %+v", n, h.engine.issues.snapshot())
	}

	// The second subject's halves land: its gap closes.
	closed := map[string]any{"entityKey": "vtx.task." + cleared, "violating": false, col: false}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, cleared, closed, 3, 1)); dec != substrate.Ack {
		t.Fatalf("gap-close row must Ack, got %v", dec)
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, cleared, col)); ok {
		t.Fatal("the closed subject's own issue must retire")
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, stuck, col)); !ok {
		t.Fatalf("the STILL-STUCK subject's issue must survive another subject's close; issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestHandleRow_ExhaustedGapIssueIsPerEntity proves the same for the other
// per-row fact Weaver raises at a gap: the retry budget is counted per
// (target, entity, gap) in weaver-state, so the GapBudgetExhausted issue that
// reports it is keyed the same way — one subject exhausting its budget and the
// other closing its gap are independent events.
func TestHandleRow_ExhaustedGapIssueIsPerEntity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureExhaustPerEntity"
	const col = "missing_x"
	const budget = 2
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "FixX"}},
	})
	stuck, cleared := testNanoID(t), testNanoID(t)
	for _, entityID := range []string{stuck, cleared} {
		for i := 0; i < budget; i++ {
			if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, col); err != nil {
				t.Fatalf("seed dispatch-count: %v", err)
			}
		}
		row := map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": true, col: true,
			"inflight_x": false, "maxretries_x": budget,
		}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 9, 1)); dec != substrate.Ack {
			t.Fatalf("exhausted gap must Ack, got %v", dec)
		}
	}
	h.requireNoOp(t)
	for _, entityID := range []string{stuck, cleared} {
		if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, col)); !ok {
			t.Fatalf("entity %s must carry its own GapBudgetExhausted issue; issues = %+v",
				entityID, h.engine.issues.snapshot())
		}
	}

	closed := map[string]any{"entityKey": "vtx.leaseApp." + cleared, "violating": false, col: false}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, cleared, closed, 10, 1)); dec != substrate.Ack {
		t.Fatalf("gap-close row must Ack, got %v", dec)
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, cleared, col)); ok {
		t.Fatal("the closed subject's own GapBudgetExhausted must retire")
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, stuck, col)); !ok {
		t.Fatalf("the still-exhausted subject's issue must survive another subject's close; issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestClearClosedMarks_RetiresBothIssueScopes is the leak guard on the split.
// A config fact (GapWithoutPlaybook here) is target-scoped, and every OTHER
// clear site for it requires the config to have been FIXED — so a column that
// simply stops being reported would strand it forever if clearClosedMarks
// retired only the entity-scoped key. Both scopes retire on the close.
func TestClearClosedMarks_RetiresBothIssueScopes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureBothScopes"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_claim": {Action: actionSurface, IssueCode: "UnroutedTasks", IssueSeverity: "warning"},
		},
	})
	entityID := testNanoID(t)
	// missing_orphan has no gaps entry at all: the config dead-end.
	open := map[string]any{
		"entityKey": "vtx.task." + entityID, "violating": true,
		"missing_claim": true, "missing_orphan": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.Ack {
		t.Fatalf("dispatch must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_orphan")); !ok {
		t.Fatalf("expected a target-scoped GapWithoutPlaybook issue, issues = %+v", h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_claim")); !ok {
		t.Fatalf("expected an entity-scoped surface issue, issues = %+v", h.engine.issues.snapshot())
	}

	closed := map[string]any{
		"entityKey": "vtx.task." + entityID, "violating": false,
		"missing_claim": false, "missing_orphan": false,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, closed, 2, 1)); dec != substrate.Ack {
		t.Fatalf("gap-close row must Ack, got %v", dec)
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_orphan")); ok {
		t.Fatal("a column that stopped being reported must retire its target-scoped config issue too")
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, "missing_claim")); ok {
		t.Fatal("the entity-scoped surface issue must retire on the close")
	}
}

// TestColumnReaders_SurfaceDataErrorsPerEntity pins the entity each §10.2
// column reader names when it surfaces a malformed value — including through
// the three helpers that read a row without otherwise needing the entity
// (staleMark, openGapColumns, hasUsableRetryCap). Each raise must land at the
// reading row's own key: a helper that dropped the entity on the way through
// would key every row's data error identically, which is the collision these
// keys exist to prevent, and no end-to-end test distinguishes the reader that
// raised from the one that did not.
func TestColumnReaders_SurfaceDataErrorsPerEntity(t *testing.T) {
	t.Parallel()
	e := &Engine{issues: newIssueCache(), logger: discardLogger()}
	const targetID = "fixtureReaders"
	first, second := testNanoID(t), testNanoID(t)

	// boolColumn, read directly and again through openGapColumns.
	e.boolColumn(targetID, first, map[string]any{"violating": "yes"}, "violating")
	e.openGapColumns(targetID, second, map[string]any{"missing_x": "yes"})
	// intColumn, read directly and again through hasUsableRetryCap.
	e.intColumn(targetID, first, map[string]any{admissionPriorityColumn: "high"}, admissionPriorityColumn)
	e.hasUsableRetryCap(targetID, second, map[string]any{"maxretries_x": "three"}, "missing_x")
	// boolColumn through staleMark's inflight_<g> read.
	e.staleMark(targetID, first, map[string]any{"missing_x": true, "inflight_x": "maybe"},
		"missing_x", GapAction{Action: actionDirectOp, Operation: "FixX"})

	for _, want := range []struct {
		entityID, col string
	}{
		{first, "violating"},
		{second, "missing_x"},
		{first, admissionPriorityColumn},
		{second, "maxretries_x"},
		{first, "inflight_x"},
	} {
		if _, ok := issueAt(e.issues, issueKeyDataEntity(targetID, want.entityID, want.col)); !ok {
			t.Fatalf("no RowDataError at (%s, %s); issues = %+v", want.entityID, want.col, e.issues.snapshot())
		}
	}
	if n := len(e.issues.snapshot()); n != 5 {
		t.Fatalf("five malformed reads across two rows must raise five issues, got %d: %+v",
			n, e.issues.snapshot())
	}

	// Each reader retires its own row's entry on a value that parses — through
	// the same three helpers, so a helper that surfaced a fault but could not
	// retire it is caught here too.
	e.boolColumn(targetID, first, map[string]any{"violating": true}, "violating")
	e.openGapColumns(targetID, second, map[string]any{"missing_x": true})
	e.intColumn(targetID, first, map[string]any{admissionPriorityColumn: 7}, admissionPriorityColumn)
	e.hasUsableRetryCap(targetID, second, map[string]any{"maxretries_x": 3}, "missing_x")
	e.staleMark(targetID, first, map[string]any{"missing_x": true, "inflight_x": false},
		"missing_x", GapAction{Action: actionDirectOp, Operation: "FixX"})
	if n := len(e.issues.snapshot()); n != 0 {
		t.Fatalf("every column re-read clean; the cache must retain nothing, got %+v", e.issues.snapshot())
	}

	// An ABSENT column is the retraction shape: it retires too, for both
	// readers, and must never raise a fault of its own.
	e.boolColumn(targetID, first, map[string]any{"violating": "yes"}, "violating")
	e.intColumn(targetID, first, map[string]any{admissionPriorityColumn: "high"}, admissionPriorityColumn)
	if n := len(e.issues.snapshot()); n != 2 {
		t.Fatalf("two malformed reads must raise two issues, got %+v", e.issues.snapshot())
	}
	e.boolColumn(targetID, first, map[string]any{}, "violating")
	e.intColumn(targetID, first, map[string]any{}, admissionPriorityColumn)
	if n := len(e.issues.snapshot()); n != 0 {
		t.Fatalf("an absent column must retire, never raise; got %+v", e.issues.snapshot())
	}
}

// TestHandleRow_RowDataIssueIsPerEntity proves a malformed COLUMN VALUE is a
// fact about the one row carrying it, exactly like the gap facts. Two rows on
// the same target project a non-bool gap column; one is re-projected clean and
// dispatches, which retires its own data issue — the row still carrying the bad
// value stays surfaced. A target-scoped key made the two share one latch, so
// the first repair silenced the surface for a row that was still broken.
func TestHandleRow_RowDataIssueIsPerEntity(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureRowDataPerEntity"
	const col = "missing_x"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "FixX"}},
	})
	broken, repaired := testNanoID(t), testNanoID(t)
	for i, entityID := range []string{broken, repaired} {
		// A string where §10.2 requires a bool: read as not actionable, surfaced.
		dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": true, col: "yes",
		}, uint64(i+1), 1))
		if dec != substrate.Ack {
			t.Fatalf("a non-bool gap column must Ack, got %v", dec)
		}
	}
	h.requireNoOp(t)
	for _, entityID := range []string{broken, repaired} {
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, col)); !ok {
			t.Fatalf("row %s must carry its own RowDataError; issues = %+v", entityID, h.engine.issues.snapshot())
		}
	}

	// The lens re-projects the second row with a real bool: it dispatches, and
	// the plan that fires retires that row's data issue.
	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, repaired, map[string]any{
		"entityKey": "vtx.leaseApp." + repaired, "violating": true, col: true,
	}, 3, 1))
	if dec != substrate.Ack {
		t.Fatalf("the repaired row must dispatch and Ack, got %v", dec)
	}
	h.nextOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, repaired, col)); ok {
		t.Fatal("the repaired row's own data issue must retire")
	}
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, broken, col)); !ok {
		t.Fatalf("the STILL-BROKEN row's issue must survive another row's repair; issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestHandleRow_ConfigIssuesAreTargetScoped is the other half of the scope
// rule. An unresolvable reference and an un-dispatchable action are facts about
// the PLAYBOOK: identical for every row of the target, fixable only by a
// package re-author. Two subjects hitting the same one raise ONE issue, not one
// per subject — segmenting these by entity would mint N duplicate config alerts
// the moment N rows violated.
func TestHandleRow_ConfigIssuesAreTargetScoped(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureConfigScope"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			// An uninstalled pattern (transient) and a candidates-only gap on a
			// non-planned target (an unknown action "") — the two config raises.
			"missing_y": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"},
			"missing_z": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "SendReminder"}}},
		},
	})
	first, second := testNanoID(t), testNanoID(t)
	for i, entityID := range []string{first, second} {
		row := map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": true,
			"missing_y": true, "missing_z": true,
		}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, uint64(i+1), 1)); dec != substrate.NakWithDelay {
			t.Fatalf("an unresolved reference must NakWithDelay, got %v", dec)
		}
	}
	h.requireNoOp(t)

	if _, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_y")); !ok {
		t.Fatalf("UnresolvedReference must be raised target-scoped, issues = %+v", h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_z")); !ok {
		t.Fatalf("PlaybookConfigError must be raised target-scoped, issues = %+v", h.engine.issues.snapshot())
	}
	if n := len(h.engine.issues.snapshot()); n != 2 {
		t.Fatalf("two subjects hitting two config faults must raise 2 issues, got %d: %+v",
			n, h.engine.issues.snapshot())
	}
}

// TestHandleRow_FreshPlanRetiresStaleExhaustedIssue proves the successful-plan
// clear retires the ENTITY-scoped budget issue too, not just the target-scoped
// config ones. A raised maxretries_<g> makes a standing GapBudgetExhausted
// false without the gap ever closing, so a plan built and about to fire is the
// only site that observes it — the gap-close clears never run.
func TestHandleRow_FreshPlanRetiresStaleExhaustedIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureBudgetRaised"
	const col = "missing_x"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	for i := 0; i < 2; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, col); err != nil {
			t.Fatalf("seed dispatch-count: %v", err)
		}
	}
	spent := map[string]any{
		"entityKey": entityKey, "violating": true, col: true,
		"inflight_x": false, "maxretries_x": 2,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, spent, 1, 1)); dec != substrate.Ack {
		t.Fatalf("exhausted gap must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, col)); !ok {
		t.Fatalf("expected a standing GapBudgetExhausted, issues = %+v", h.engine.issues.snapshot())
	}

	// The lens raises the cap: the budget is no longer spent, the gap is still
	// open, and the next delivery dispatches.
	raised := map[string]any{
		"entityKey": entityKey, "violating": true, col: true,
		"inflight_x": false, "maxretries_x": 5,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, raised, 2, 1)); dec != substrate.Ack {
		t.Fatalf("the re-budgeted gap must dispatch and Ack, got %v", dec)
	}
	h.nextOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, col)); ok {
		t.Fatalf("a fresh dispatch disproves GapBudgetExhausted; it must not stand, issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestDispatchGap_AugurPolicyRetiresGapWithoutPlaybook proves the config latch's
// other clear site keeps its scope: an alert raised before the augur policy was
// added retires once the policy covers the dead-end (the raise at the no-
// playbook branch and this clear are one latch, and both are target-scoped),
// while this subject's own entity-scoped issue is untouched — a policy addition
// says nothing about any one row.
//
// The target declares an admission rate the escalated dispatch cannot draw a
// token from, so the deferral stops the pass before a plan is built: this
// clear is observed on its own, not shadowed by the fresh-plan clear.
func TestDispatchGap_AugurPolicyRetiresGapWithoutPlaybook(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureNoPlaybookAugur"
	const col = "missing_unknown"
	vtx := testNanoID(t)
	spec := targetSpecFixture(targetID) // declares gaps.missing_a only
	spec["admission"] = map[string]any{"globalRate": 0.5}
	h.engine.source.handle(vertexEvent(t, vtx, weaverTargetClass))
	h.engine.source.handle(specEvent(t, vtx, spec))

	entityID := testNanoID(t)
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, col: true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
		t.Fatalf("a gap with no playbook entry must Ack, got %v", dec)
	}
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, col)); !ok {
		t.Fatalf("expected a target-scoped GapWithoutPlaybook, issues = %+v", h.engine.issues.snapshot())
	}
	h.engine.issues.set(issueKeyGapEntity(targetID, entityID, col), "warning", "GapBudgetExhausted", "this subject's own fact")

	// The package adds the augur policy: the dead-end is covered from here on.
	spec["augur"] = map[string]any{"escalate": []any{"unplannable"}}
	h.engine.source.handle(specEvent(t, vtx, spec))
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 2, 1)); dec != substrate.NakWithDelay {
		t.Fatalf("the escalated dead-end must defer on admission, got %v", dec)
	}
	h.requireNoOp(t)
	if _, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, col)); ok {
		t.Fatalf("an augur policy covering the gap must retire its GapWithoutPlaybook alert, issues = %+v",
			h.engine.issues.snapshot())
	}
	if _, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, col)); !ok {
		t.Fatalf("a policy addition must not retire this subject's own issue, issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestPlanGap_UnplannableEscalationRetiresConfigIssue pins the scope of the
// third config clear: an augur escalation that resolves an unplannable plan
// says the playbook dead-end is covered, so it retires the TARGET-scoped issue
// and leaves this subject's own untouched. The declared admission rate holds
// the pass at the deferral, so the fresh-plan clear (which retires both scopes)
// never runs and this clear is observed on its own.
func TestPlanGap_UnplannableEscalationRetiresConfigIssue(t *testing.T) {
	t.Parallel()
	const targetID = "fixtureUnplannableEscalate"
	const col = "missing_x"
	s, _ := registerAugurTarget(t, targetID, map[string]any{"escalate": []any{"unplannable"}})
	e := &Engine{
		source:      s,
		issues:      newIssueCache(),
		admission:   newAdmissionScheduler(),
		contraction: newContractionStats(),
		logger:      discardLogger(),
	}
	target := &Target{
		TargetID:  targetID,
		Mode:      targetModePlanned,
		Augur:     mustTarget(t, s, targetID).Augur,
		Admission: &AdmissionPolicy{GlobalRate: 0.5},
	}
	entityID := testNanoID(t)
	e.issues.set(issueKeyGapConfig(targetID, col), "error", "PlaybookConfigError", "the pin has vanished")
	e.issues.set(issueKeyGapEntity(targetID, entityID, col), "warning", "GapBudgetExhausted", "budget spent")

	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID}
	// A pin the catalog no longer names is the unplannable-flagged error.
	if pl, _, dec := e.planGap(context.Background(), target, targetID, entityID, col,
		goalGapFixture(t), row, 42, "aGhostLeg"); pl != nil || dec != substrate.NakWithDelay {
		t.Fatalf("the escalated gap must escalate then defer on admission, got plan=%v decision %v", pl != nil, dec)
	}
	if _, ok := issueAt(e.issues, issueKeyGapConfig(targetID, col)); ok {
		t.Fatalf("the augur escalation covers the dead-end; its config issue must retire, issues = %+v",
			e.issues.snapshot())
	}
	if _, ok := issueAt(e.issues, issueKeyGapEntity(targetID, entityID, col)); !ok {
		t.Fatalf("the escalation says nothing about this subject's budget; its issue must stand, issues = %+v",
			e.issues.snapshot())
	}
}
