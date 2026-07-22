package weaver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- Unit-level: resolveGoalAction (Fire 6, R1 — design
// loftspace-lease-renewal-goal-authored-target-design.md §4.3/§9) ---

// goalGapFixture builds a validated, install-cached goal-mode gap: a 2-leg
// chain (legA has no precondition and asserts aDone; legB requires aDone and
// asserts bDone) toward goal allOf(aDone, bDone) — the smallest shape that
// exercises real goal-regression search (legB is only reachable AFTER legA).
func goalGapFixture(t *testing.T) GapAction {
	t.Helper()
	target := &Target{
		TargetID: "fixtureGoalUnit",
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {
				Goal: json.RawMessage(`{"allOf":[{"present":"subject.data.aDone"},{"present":"subject.data.bDone"}]}`),
				Actions: []ActionCatalogEntry{
					{Ref: "legA", Action: actionDirectOp, Operation: "DoA",
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.aDone"}`)}},
					{Ref: "legB", Action: actionDirectOp, Operation: "DoB",
						Pre:     json.RawMessage(`{"present":"subject.data.aDone"}`),
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.bDone"}`)}},
				},
			},
		},
	}
	if err := validateTarget(target); err != nil {
		t.Fatalf("validateTarget: %v", err)
	}
	return target.Gaps["missing_x"]
}

// TestResolveGoalAction_FreshEpisodeSynthesizesFirstLeg proves a genuinely
// fresh episode (pinnedAction=="") synthesizes the cheapest plan reaching the
// goal from the CURRENT row and dispatches its Steps[0] — here, legA must
// fire first because legB's own precondition (aDone) does not hold yet.
func TestResolveGoalAction_FreshEpisodeSynthesizesFirstLeg(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := goalGapFixture(t)
	got, ref, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_x", ga, map[string]any{}, "")
	if perr != nil {
		t.Fatalf("expected a synthesized plan, got error %v", perr)
	}
	if ref != "legA" || got.Operation != "DoA" {
		t.Fatalf("expected the first reachable leg (legA/DoA), got ref=%q %+v", ref, got)
	}
}

// TestResolveGoalAction_PinnedEpisodeReusesLeg proves an in-flight episode
// reuses its EXACT pinned leg — even one a fresh synthesis could not derive
// yet (legB's own precondition is unmet at this row) — never re-planning
// mid-episode (mirrors the candidates branch's pin discipline, design §2).
func TestResolveGoalAction_PinnedEpisodeReusesLeg(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := goalGapFixture(t)
	got, ref, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_x", ga, map[string]any{}, "legB")
	if perr != nil {
		t.Fatalf("a pinned leg must resolve without error, got %v", perr)
	}
	if ref != "legB" || got.Operation != "DoB" {
		t.Fatalf("expected the PINNED leg reused verbatim, got ref=%q %+v", ref, got)
	}
}

// TestResolveGoalAction_PinVanished_FlagsUnplannable proves a pin whose ref
// the catalog no longer names surfaces as an unplannable-flagged error —
// planGap retries this through the SAME augur.escalate("unplannable") policy
// a fresh Synthesize dead-end uses, because it is indistinguishable from a
// redelivered episode previously escalated to the augur (resolveGoalAction's
// doc).
func TestResolveGoalAction_PinVanished_FlagsUnplannable(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := goalGapFixture(t)
	_, _, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_x", ga, map[string]any{}, "aGhostLeg")
	if perr == nil || !perr.unplannable {
		t.Fatalf("expected an unplannable-flagged error for a pin the catalog no longer names, got %v", perr)
	}
}

// TestResolveGoalAction_NoPlanDerivable_FlagsUnplannable proves a goal no
// catalog action's effects can ever satisfy surfaces as an unplannable errData
// (never a hot loop, never a silent skip) — the "ErrNoPlan → unplannable"
// contract clause.
func TestResolveGoalAction_NoPlanDerivable_FlagsUnplannable(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	unreachable := &Target{
		TargetID: "fixtureUnreachable",
		Gaps: map[string]GapAction{
			"missing_y": {
				Goal: json.RawMessage(`{"present":"subject.data.neverAsserted"}`),
				Actions: []ActionCatalogEntry{
					{Ref: "legA", Action: actionDirectOp, Operation: "DoA",
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.aDone"}`)}},
				},
			},
		},
	}
	if err := validateTarget(unreachable); err != nil {
		t.Fatalf("validateTarget: %v", err)
	}
	_, _, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_y", unreachable.Gaps["missing_y"], map[string]any{}, "")
	if perr == nil || !perr.unplannable || perr.kind != errData {
		t.Fatalf("expected an unplannable errData when no plan reaches the goal, got %v", perr)
	}
}

// TestResolveGoalAction_GoalAlreadyMetIsALensMismatch proves the goal-already-
// met-but-gap-still-open case surfaces as a (non-unplannable) errData — a
// lens/goal authoring mismatch, never a silent no-dispatch while the gap
// stays open forever.
func TestResolveGoalAction_GoalAlreadyMetIsALensMismatch(t *testing.T) {
	t.Parallel()
	e := shadowTestEngine(t)
	target := &Target{TargetID: "t1", Mode: targetModePlanned}
	ga := goalGapFixture(t)
	row := map[string]any{"aDone": true, "bDone": true}
	_, _, perr := e.resolvePlannedAction(context.Background(), target, "t1", "e1", "missing_x", ga, row, "")
	if perr == nil || perr.unplannable || perr.kind != errData {
		t.Fatalf("expected a plain errData (lens/goal mismatch) when the goal already holds, got %v", perr)
	}
}

// --- Engine-level: real dispatch + leg advancement through handleRow ---

// TestGoalMode_FreshDispatchThenLegReleaseAdvances is the R1 engine-wiring
// proof end to end: a fresh delivery synthesizes and dispatches the first
// leg unaided, pinning the mark on the leg's OWN Ref (not its dispatch
// contract type); once a later row delivery shows that leg's declared effect
// holding, the SAME call releases the completed leg (deletes its mark,
// mints a fresh one) and dispatches the next leg — level-triggered advance,
// no re-rank, no waiting for lease expiry.
func TestGoalMode_FreshDispatchThenLegReleaseAdvances(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureGoalDispatch"
	target := &Target{
		TargetID: targetID,
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {
				Goal: json.RawMessage(`{"allOf":[{"present":"subject.data.aDone"},{"present":"subject.data.bDone"}]}`),
				Actions: []ActionCatalogEntry{
					{Ref: "legA", Action: actionDirectOp, Operation: "DoA",
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.aDone"}`)}},
					{Ref: "legB", Action: actionDirectOp, Operation: "DoB",
						Pre:     json.RawMessage(`{"present":"subject.data.aDone"}`),
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.bDone"}`)}},
				},
			},
		},
	}
	if err := validateTarget(target); err != nil {
		t.Fatalf("validateTarget: %v", err)
	}
	h.seedTarget(target)

	entityID := testNanoID(t)
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true}

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
	if dec != substrate.Ack {
		t.Fatalf("fresh goal dispatch must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != "DoA" {
		t.Fatalf("expected the first synthesized leg (DoA) dispatched unaided, got %v", op["operationType"])
	}
	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x")
	if err != nil || !found {
		t.Fatalf("expected a mark pinning the first leg (err=%v found=%v)", err, found)
	}
	if rec.Action != "legA" {
		t.Fatalf("mark must pin the leg's own Ref (not its dispatch contract type %q), got %q", actionDirectOp, rec.Action)
	}

	// legA's op has committed: the re-projected row now carries aDone=true,
	// while the gap itself stays open (bDone still missing).
	row2 := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "aDone": true}
	dec = h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row2, 6, 1))
	if dec != substrate.Ack {
		t.Fatalf("leg-release-then-advance dispatch must Ack, got %v", dec)
	}
	op2 := h.nextOp(t)
	if op2["operationType"] != "DoB" {
		t.Fatalf("expected the completed leg's release to advance to legB (DoB), got %v", op2["operationType"])
	}
	rec2, _, found2, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x")
	if err != nil || !found2 {
		t.Fatalf("expected a FRESH mark pinning the second leg (err=%v found=%v)", err, found2)
	}
	if rec2.Action != "legB" {
		t.Fatalf("advanced mark must pin the new leg's Ref, got %q", rec2.Action)
	}
	if rec2.ClaimID == rec.ClaimID {
		t.Fatalf("a leg boundary must mint a FRESH claimId (design §5), got the same %q", rec.ClaimID)
	}
	h.requireNoOp(t)
}

// TestGoalMode_UnplannableEscalatesToAugur proves a goal no catalog action
// can ever reach redirects to the target's augur.escalate("unplannable")
// policy exactly like the pre-existing "no playbook entry" dead-end, instead
// of alerting forever (Contract #10 §10.8: "its meaning extends to 'no
// playbook entry AND no derivable plan'").
func TestGoalMode_UnplannableEscalatesToAugur(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureGoalAugur"
	spec := targetSpecFixture(targetID)
	spec["mode"] = targetModePlanned
	spec["augur"] = map[string]any{"escalate": []any{"unplannable"}}
	spec["gaps"] = map[string]any{
		"missing_x": map[string]any{
			// A goal no catalog action's effects touch — Synthesize can never
			// reach it, however many legs it tries.
			"goal": map[string]any{"present": "subject.data.neverAsserted"},
			"actions": []any{
				map[string]any{
					"ref": "legA", "action": actionDirectOp, "operation": "DoA",
					"effects": []any{map[string]any{"present": "subject.data.aDone"}},
				},
			},
		},
	}
	id := testNanoID(t)
	h.engine.source.handle(vertexEvent(t, id, weaverTargetClass))
	h.engine.source.handle(specEvent(t, id, spec))
	if _, ok := h.engine.source.target(targetID); !ok {
		t.Fatalf("target %q not registered", targetID)
	}

	entityID := testNanoID(t)
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true}
	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 5, 1))
	if dec != substrate.Ack {
		t.Fatalf("escalated dispatch must Ack, got %v", dec)
	}
	op := h.nextOp(t)
	if op["operationType"] != defaultAugurOp {
		t.Fatalf("expected the unplannable goal to escalate to the augur reasoning op %q, got %v", defaultAugurOp, op["operationType"])
	}
	issues := h.engine.issues.snapshot()
	if hasIssueCode(issues, "PlaybookConfigError") || hasIssueCode(issues, "TemplateDataError") {
		t.Fatalf("an escalated dispatch must not ALSO alert a config/data error, got %+v", issues)
	}
}

// --- Reconciler-level: pin reuse and leg release under sweep ---

// TestReclaim_ReusesPinnedGoalLeg_NotFreshRePlan is the goal-branch analogue
// of TestReclaim_ReusesPinnedCandidate_NotFreshRank: an expired mark pinning
// the costlier leg must be re-fired VERBATIM on reclaim, never re-derived —
// a fresh synthesis from this row would always prefer the cheaper leg.
func TestReclaim_ReusesPinnedGoalLeg_NotFreshRePlan(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureReclaimGoal"
	target := &Target{
		TargetID: targetID,
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {
				Goal: json.RawMessage(`{"present":"subject.data.done"}`),
				Actions: []ActionCatalogEntry{
					{Ref: "cheap", Action: actionDirectOp, Operation: "Cheap", Cost: 0,
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)}},
					{Ref: "pinned", Action: actionDirectOp, Operation: "Pinned", Cost: 10,
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.done"}`)}},
				},
			},
		},
	}
	if err := validateTarget(target); err != nil {
		t.Fatalf("validateTarget: %v", err)
	}
	h.seedTarget(target)

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "pinned", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
	})

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != "Pinned" {
		t.Fatalf("expected the PINNED leg (Pinned) reclaimed, got %v — a fresh synth would have wrongly picked Cheap", op["operationType"])
	}
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("the reclaim must leave the mark standing: %v", err)
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal reclaimed mark: %v", err)
	}
	if rec.Action != "pinned" {
		t.Fatalf("reclaimed mark must keep the SAME pinned leg ref, got %q", rec.Action)
	}
	h.requireNoOp(t)
}

// TestReclaim_ReleasesCompletedLegInsteadOfReclaiming proves the sweep's own
// row re-read catches a completed leg independent of CDC delivery timing: an
// expired mark whose pinned leg's effect ALREADY holds in the row is
// RELEASED, never reclaimed/re-fired as a stuck episode — and because the
// sweep enumerates MARKS, not rows (a released, markless gap would otherwise
// be invisible until an unrelated future row write), the SAME pass
// immediately dispatches the next leg as a genuinely fresh episode under a
// brand-new mark, rather than leaving the gap markless.
func TestReclaim_ReleasesCompletedLegInsteadOfReclaiming(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureReclaimGoalRelease"
	target := &Target{
		TargetID: targetID,
		Mode:     targetModePlanned,
		Gaps: map[string]GapAction{
			"missing_x": {
				Goal: json.RawMessage(`{"allOf":[{"present":"subject.data.aDone"},{"present":"subject.data.bDone"}]}`),
				Actions: []ActionCatalogEntry{
					{Ref: "legA", Action: actionDirectOp, Operation: "DoA",
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.aDone"}`)}},
					{Ref: "legB", Action: actionDirectOp, Operation: "DoB",
						Pre:     json.RawMessage(`{"present":"subject.data.aDone"}`),
						Effects: []json.RawMessage{json.RawMessage(`{"present":"subject.data.bDone"}`)}},
				},
			},
		},
	}
	if err := validateTarget(target); err != nil {
		t.Fatalf("validateTarget: %v", err)
	}
	h.seedTarget(target)

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "legA", pastLease()))
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "aDone": true,
	})

	h.pass(ctx)

	op := h.nextOp(t)
	if op["operationType"] != "DoB" {
		t.Fatalf("expected the released leg to advance to legB (DoB) in the SAME sweep pass, got %v", op["operationType"])
	}
	entry, err := h.conn.KVGet(ctx, "weaver-state", key)
	if err != nil {
		t.Fatalf("expected a FRESH mark for the advanced leg, got: %v", err)
	}
	var rec mark
	if err := json.Unmarshal(entry.Value, &rec); err != nil {
		t.Fatalf("unmarshal advanced mark: %v", err)
	}
	if rec.Action != "legB" {
		t.Fatalf("the fresh mark must pin the NEW leg's ref, got %q", rec.Action)
	}
	h.requireNoOp(t)
}

// TestReleaseCompletedLeg_RevisionConflict_LeavesMarkStanding proves
// releaseCompletedLeg's revision-conditioned mark clear (evaluator.go) skips
// the release — returning false, not erroring — when the mark changed
// underneath the caller's read (a concurrent path already released or is
// otherwise handling this same episode), mirroring every other sweep-path
// mark mutation's conflict-is-not-fatal posture.
func TestReleaseCompletedLeg_RevisionConflict_LeavesMarkStanding(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixtureReleaseConflict"
	ga := goalGapFixture(t)

	entityID := testNanoID(t)
	key := markKey(targetID, entityID, "missing_x")
	rev := h.putMark(t, ctx, key, fixtureMark(targetID, entityID, "missing_x", "legA", futureLease()))

	// legA's effect (aDone) already holds in the row — a real release would
	// fire here — but pass a revision one past the mark's actual current
	// revision, as if a concurrent path had already advanced it.
	row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "aDone": true}
	if released := h.engine.releaseCompletedLeg(ctx, targetID, entityID, "missing_x", ga, "legA", row, rev+1); released {
		t.Fatalf("releaseCompletedLeg with a stale/mismatched revision = true, want false (conflict)")
	}

	if !h.markExists(t, ctx, key) {
		t.Fatalf("a revision-conflicted release must leave the mark standing, got deleted")
	}
}
