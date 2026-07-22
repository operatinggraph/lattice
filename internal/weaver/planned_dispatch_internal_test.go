package weaver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedEnginePattern registers a pattern ref → meta.loomPattern vertex key
// directly on an Engine's registry, for either handlerHarness or
// sweepHarness (both expose .engine) — mirrors handlerHarness.seedPattern
// without duplicating a per-harness method.
func seedEnginePattern(e *Engine, ref, vertexID string) {
	e.source.mu.Lock()
	defer e.source.mu.Unlock()
	e.source.patternMeta[ref] = "vtx.meta." + vertexID
}

// --- Unit-level: resolvePlannedAction (design weaver-planner-mandate-design.md §3.3) ---

// TestResolvePlannedAction_NonCandidateShapesUnchanged proves the resolver is
// a no-op — returns ga verbatim — for every gap shape Fire 5 must leave
// byte-identical: an explicit Action (the operator override always wins,
// §10.8 precedence), and a non-planned target mode (absent or shadow) even
// when the gap declares Candidates.
func TestResolvePlannedAction_NonCandidateShapesUnchanged(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	ctx := context.Background()

	explicit := GapAction{Action: actionDirectOp, Operation: "X",
		Candidates: []GapCandidate{{Action: actionAssignTask}}}
	planned := &Target{TargetID: "t1", Mode: targetModePlanned}
	got, _, perr := e.resolvePlannedAction(ctx, planned, "t1", "e1", "missing_a", explicit, map[string]any{}, "")
	if perr != nil {
		t.Fatalf("explicit Action must never error, got %v", perr)
	}
	if got.Action != actionDirectOp || got.Operation != "X" {
		t.Fatalf("explicit Action must pass through unchanged, got %+v", got)
	}

	candidatesOnly := GapAction{Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "Y"}}}
	for _, mode := range []string{"", targetModeShadow} {
		target := &Target{TargetID: "t1", Mode: mode}
		got, _, perr := e.resolvePlannedAction(ctx, target, "t1", "e1", "missing_a", candidatesOnly, map[string]any{}, "")
		if perr != nil {
			t.Fatalf("mode %q must never error on a candidates-only gap (byte-identical no-op), got %v", mode, perr)
		}
		if got.Action != "" {
			t.Fatalf("mode %q must leave Action empty (buildPlan's pre-Fire-5 config-error path), got %q", mode, got.Action)
		}
	}
}

// TestResolvePlannedAction_FreshEpisodeRanks proves a genuinely fresh episode
// (pinnedAction == "", no mark yet) ranks the candidates and materializes the
// winner into a dispatchable GapAction carrying that candidate's own fields.
func TestResolvePlannedAction_FreshEpisodeRanks(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := GapAction{Candidates: []GapCandidate{
		{Action: actionTriggerLoom, Pattern: "expensiveFlow", Subject: "row.entityKey", Cost: 5},
		{Action: actionDirectOp, Operation: "Cheap", Cost: 0},
	}}
	got, _, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_a", ga, map[string]any{}, "")
	if perr != nil {
		t.Fatalf("expected a pick, got error %v", perr)
	}
	if got.Action != actionDirectOp || got.Operation != "Cheap" {
		t.Fatalf("expected the lower-cost candidate to win, got %+v", got)
	}
}

// TestResolvePlannedAction_PinnedEpisodeReusesChoice is the pre-build-gate's
// core assertion (design §2): an episode that already pinned a candidate
// reuses that EXACT choice even when a fresh rank would now prefer the other
// one (here, by cost) — replanning must only ever happen at a fresh episode.
func TestResolvePlannedAction_PinnedEpisodeReusesChoice(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := GapAction{Candidates: []GapCandidate{
		{Action: actionTriggerLoom, Pattern: "expensiveFlow", Subject: "row.entityKey", Cost: 5},
		{Action: actionDirectOp, Operation: "Cheap", Cost: 0},
	}}
	// Pinned to the costlier candidate — as if a prior fresh episode picked it
	// before Cheap existed, or before its cost/close-rate made it preferable.
	got, _, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_a", ga, map[string]any{}, actionTriggerLoom)
	if perr != nil {
		t.Fatalf("a pinned choice must resolve without error, got %v", perr)
	}
	if got.Action != actionTriggerLoom || got.Pattern != "expensiveFlow" {
		t.Fatalf("expected the PINNED candidate reused verbatim, got %+v", got)
	}
}

// TestResolvePlannedAction_PinVanished proves a pin whose candidate the
// playbook no longer declares (edited out since the episode was dispatched)
// is a CONFIG error — alerted, never silently reinterpreted as a fresh pick.
func TestResolvePlannedAction_PinVanished(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := GapAction{Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "Cheap", Cost: 0}}}
	_, _, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_a", ga, map[string]any{}, "aRemovedCandidate")
	if perr == nil || perr.kind != errConfig {
		t.Fatalf("expected an errConfig for a vanished pin, got %v", perr)
	}
}

// TestResolvePlannedAction_NoEligibleCandidate proves a fresh episode with
// every candidate's precondition false surfaces as a bounded per-row data
// condition (errData), not a systemic config error.
func TestResolvePlannedAction_NoEligibleCandidate(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := GapAction{Candidates: []GapCandidate{
		{Action: actionDirectOp, Operation: "X", preGuard: parseGuard(t, `{"present":"subject.data.nope"}`)},
	}}
	_, _, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_a", ga, map[string]any{}, "")
	if perr == nil || perr.kind != errData {
		t.Fatalf("expected an errData when no candidate is eligible, got %v", perr)
	}
}

// --- Engine-level: real dispatch through handleRow (design §8 Fire 5 acceptance) ---

// TestPlannedMode_FreshDispatchPicksRankedCandidate proves the FIRST engine
// wiring end of Fire 5: a mode:"planned" target's candidates-only gap
// dispatches the ranked winner — unaided, no human, no Augur — and pins that
// choice on the mark's Action.
func TestPlannedMode_FreshDispatchPicksRankedCandidate(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixturePlanned"
	h.seedTarget(&Target{
		TargetID: targetID,
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {Candidates: []GapCandidate{
				{Action: actionTriggerLoom, Pattern: "unseeded", Subject: "row.entityKey", Cost: 5},
				{Action: actionDirectOp, Operation: "SendReminder", Cost: 0},
			}},
		},
	})
	entityID := testNanoID(t)
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true}

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
	if dec != substrate.Ack {
		t.Fatalf("fresh planned dispatch must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "SendReminder" {
		t.Fatalf("expected the lower-cost candidate (SendReminder) dispatched unaided, got %v", op["operationType"])
	}
	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x")
	if err != nil || !found {
		t.Fatalf("expected a mark pinning the pick (err=%v found=%v)", err, found)
	}
	if rec.Action != actionDirectOp {
		t.Fatalf("mark must pin the chosen candidate's Action, got %q", rec.Action)
	}
}

// TestPlannedMode_AbsentModeByteIdentical proves a candidates-only gap on a
// target whose Mode is NOT "planned" (absent or shadow) hits the exact
// pre-Fire-5 config-error path — dispatch never routes through candidate
// selection for these modes, so every target installed before this fire is
// untouched.
func TestPlannedMode_AbsentModeByteIdentical(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureAbsentMode"
	h.seedTarget(&Target{
		TargetID: targetID,
		// Mode absent (zero value).
		Gaps: map[string]GapAction{
			"missing_x": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "SendReminder"}}},
		},
	})
	entityID := testNanoID(t)
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true}

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
	if dec != substrate.Ack {
		t.Fatalf("a mode-absent candidates-only gap must Ack (skip), got %v", dec)
	}
	h.requireNoOp(t)
	if !hasIssueCode(h.engine.issues.snapshot(), "PlaybookConfigError") {
		t.Fatalf("expected the pre-Fire-5 PlaybookConfigError (unknown action \"\") to still fire")
	}
	if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x"); err != nil || found {
		t.Fatalf("no mark may be claimed on a config error (err=%v found=%v)", err, found)
	}
}

// --- Reconciler-level: episode stability under reclaim (the design's self-imposed pre-build gate) ---

// TestReclaim_ReusesPinnedCandidate_NotFreshRank is the pre-build gate's core
// proof: an episode whose mark already pins one candidate must have that
// EXACT choice re-dispatched on reclaim, even though a fresh rank over the
// current candidate list would prefer the cheaper one — a reclaim never
// replans mid-episode (design §2).
func TestReclaim_ReusesPinnedCandidate_NotFreshRank(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	seedEnginePattern(h.engine, "emailFlow", testNanoID(t))

	const targetID = "fixtureReclaimPlanned"
	h.seedTarget(&Target{
		TargetID: targetID,
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {Candidates: []GapCandidate{
				// A fresh rank always prefers this one (lower cost) — but it must
				// NOT be what fires below; the mark below pins the OTHER candidate.
				{Action: actionDirectOp, Operation: "ChargeCard", Cost: 0},
				{Action: actionTriggerLoom, Pattern: "emailFlow", Subject: "row.entityKey", Cost: 10},
			}},
		},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", actionTriggerLoom, pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != "StartLoomPattern" {
		t.Fatalf("expected the PINNED candidate (triggerLoom) reclaimed, got %v — a fresh rank would have wrongly picked ChargeCard", op["operationType"])
	}
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("the reclaim must leave the mark standing: %v", err)
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal reclaimed mark: %v", err)
	}
	if rec.Action != actionTriggerLoom {
		t.Fatalf("reclaimed mark must keep the SAME pinned Action, got %q", rec.Action)
	}
	h.requireNoOp(t)
}

// TestReclaim_PinVanishedLeavesMarkForNextSweep proves a reclaim whose pinned
// candidate the playbook no longer declares is a config error handled exactly
// like any other planGap failure: alerted, the expired mark left standing
// (never deleted, never silently re-ranked) for the next sweep.
func TestReclaim_PinVanishedLeavesMarkForNextSweep(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureReclaimPinGone"
	h.seedTarget(&Target{
		TargetID: targetID,
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "ChargeCard", Cost: 0}}},
		},
	})
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	// Pinned to a candidate ref the current playbook no longer declares.
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", actionAssignTask, pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	h.requireNoOp(t)
	if !h.markExists(t, ctx, key) {
		t.Fatalf("a plan failure must leave the expired mark standing for the next sweep")
	}
	if !hasIssueCode(h.engine.issues.snapshot(), "PlaybookConfigError") {
		t.Fatalf("expected a PlaybookConfigError for the vanished pin")
	}
}
