package weaver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- shared fixtures --------------------------------------------------------

// goalLegGap is the gap column every goal-leg vector in this file drives.
const goalLegGap = "missing_g"

// goalLegSpec is the authored two-leg goal target these vectors run against:
// legA has no precondition and asserts aDone; legB requires aDone and asserts
// bDone; the goal is both. It is the smallest shape with a real leg BOUNDARY —
// the fact an escalation standing over the gap must not erase. legAAction
// selects the leg's dispatch contract type (a human assignTask, or a directOp),
// and escalate opts the target into the exhausted→Augur redirect.
//
// Authored as a SPEC and registered through the registry source rather than
// injected as a *Target, because augurEscalation resolves the target's own
// meta-vertex key from the source and an injected target has none.
func goalLegSpec(targetID, legAAction string, escalate bool) map[string]any {
	legA := map[string]any{
		"ref": "legA", "action": legAAction, "operation": "DoA",
		"effects": []any{map[string]any{"present": "subject.data.aDone"}},
	}
	if legAAction == actionAssignTask {
		legA["assignee"] = "row.applicant"
		legA["target"] = "row.entityKey"
	}
	spec := map[string]any{
		"targetId": targetID,
		"lensRef":  "lensFixture",
		"mode":     targetModePlanned,
		"gaps": map[string]any{
			goalLegGap: map[string]any{
				"goal": map[string]any{"allOf": []any{
					map[string]any{"present": "subject.data.aDone"},
					map[string]any{"present": "subject.data.bDone"},
				}},
				"actions": []any{legA, map[string]any{
					"ref": "legB", "action": actionDirectOp, "operation": "DoB",
					"pre":     map[string]any{"present": "subject.data.aDone"},
					"effects": []any{map[string]any{"present": "subject.data.bDone"}},
				}},
			},
		},
	}
	if escalate {
		spec["augur"] = map[string]any{"escalate": []any{"exhausted"}}
	}
	return spec
}

// registerSpec loads one authored target spec into a registry source the way a
// meta.weaverTarget CDC delivery does.
func registerSpec(t *testing.T, s *targetSource, spec map[string]any) {
	t.Helper()
	id := testNanoID(t)
	s.handle(vertexEvent(t, id, weaverTargetClass))
	s.handle(specEvent(t, id, spec))
	targetID, _ := spec["targetId"].(string)
	if _, ok := s.target(targetID); !ok {
		t.Fatalf("target %q did not register", targetID)
	}
}

// goalLegRow is the §10.2 row the goal-leg vectors start from. budget 0 means
// the row declares NO maxretries_<g> at all — the uncapped half of every
// vector's fixture pair, where the engine's own default is directOp-only and so
// the gap must never exhaust.
func goalLegRow(entityID string, budget int, extra map[string]any) map[string]any {
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		goalLegGap:  true,
		"applicant": "vtx.identity." + entityID,
	}
	if budget > 0 {
		row[maxretriesColumnPrefix+strings.TrimPrefix(goalLegGap, gapColumnPrefix)] = budget
	}
	for k, v := range extra {
		row[k] = v
	}
	return row
}

// escalationMark is the mark an Augur escalation leaves at the gap's own key:
// its action is the reasoning op's dispatch CLASS, and the leg it displaced
// rides escalatedFrom. escalatedFrom "" is the old-shape mark, written before
// the field existed.
func escalationMark(targetID, entityID, escalatedFrom, lease string) mark {
	return mark{
		TargetID:       targetID,
		EntityKey:      "vtx.leaseApp." + entityID,
		Gap:            goalLegGap,
		Action:         actionDirectOp,
		EscalatedFrom:  escalatedFrom,
		ClaimID:        testNanoIDStatic("escalationClaim0"),
		ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
		LeaseExpiresAt: lease,
	}
}

// putStateValue writes a weaver-state value directly (overwriting), so a vector
// can start from a durable state a real run reaches only after hours.
func putStateValue(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, value any) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal weaver-state value for %q: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, "weaver-state", key, body); err != nil {
		t.Fatalf("put weaver-state %q: %v", key, err)
	}
}

// readCount reads a gap's count document straight from the bucket.
func readCount(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID, entityID, gapColumn string) dispatchCount {
	t.Helper()
	entry, err := conn.KVGet(ctx, "weaver-state", countKey(targetID, entityID, gapColumn))
	if err != nil {
		t.Fatalf("read dispatch-count %s.%s.%s: %v", targetID, entityID, gapColumn, err)
	}
	var doc dispatchCount
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal dispatch-count: %v", err)
	}
	return doc
}

// --- Increment 1: the budget books attempts ---------------------------------

// TestSweep_CollapseOnlyReclaim_AdvancesReclaimsNotCount is the fix for the
// filed harm's first clause: a budget of six spent with zero attempts mounted.
//
// A reclaim of a collapse-only episode — an assignTask whose claimId is
// preserved verbatim, so the re-dispatch lands on the task already open — mounts
// nothing. It re-arms the episode, which is what the backoff is paced on, and it
// spends none of a budget that bounds ATTEMPTS. Five of them therefore leave the
// count exactly where the one real dispatch left it, and the gap is nowhere near
// its cap.
//
// Run capped and uncapped: the cap changes what exhaustion would mean, and must
// change nothing about what the reclaim books.
func TestSweep_CollapseOnlyReclaim_AdvancesReclaimsNotCount(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 3}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			// A negligible base isolates the BOOKING from the pacing: every
			// reclaim below fires, so five of them really do reach the count.
			h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Millisecond })
			h.agePastWarmup()

			const targetID = "fixCollapseBooks"
			const gap = "missing_signature"
			h.seedTarget(&Target{
				TargetID: targetID,
				Gaps: map[string]GapAction{gap: {
					Action: actionAssignTask, Operation: "SignLease",
					Assignee: "row.applicant", Target: "row.entityKey",
				}},
			})
			h.engine.source.mu.Lock()
			h.engine.source.opMetaByType["SignLease"] = "vtx.meta." + testNanoID(t)
			h.engine.source.mu.Unlock()

			entityID := testNanoID(t)
			// The one real attempt: the fresh episode that created the task.
			if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, gap, actionAssignTask, true, false, false); err != nil {
				t.Fatalf("seed the first attempt: %v", err)
			}
			key := markKey(targetID, entityID, gap)
			h.putMark(t, ctx, key, fixtureMark(targetID, entityID, gap, actionAssignTask, pastLease()))
			row := map[string]any{
				"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
				"applicant": "vtx.identity." + entityID,
			}
			if tc.budget > 0 {
				row["maxretries_signature"] = tc.budget
			}
			h.putRow(t, ctx, targetID, entityID, row)

			const reclaims = 5
			for i := 0; i < reclaims; i++ {
				h.pass(ctx)
				h.nextOp(t) // the re-dispatch, which the consumer collapses
				h.reexpireMark(t, ctx, key)
			}

			doc := readCount(t, ctx, h.conn, targetID, entityID, gap)
			if doc.Count != 1 {
				t.Fatalf("retry budget = %d, want 1: %d collapse-only reclaims mounted no attempt between them",
					doc.Count, reclaims)
			}
			if doc.Reclaims != reclaims {
				t.Fatalf("re-arm tally = %d, want %d: every reclaim re-armed the episode, and that is what paces the next",
					doc.Reclaims, reclaims)
			}
			if _, exhausted, _, _, _ := h.engine.gapSuppressed(ctx, targetID, entityID, row, gap, actionAssignTask); exhausted {
				t.Fatalf("the gap must not read exhausted after %d re-claims that spent nothing (budget %d)",
					reclaims, tc.budget)
			}
			if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
				t.Fatal("a budget nothing spent must raise no exhaustion")
			}
		})
	}
}

// TestSweep_ExternalReclaim_IsAnAttempt is the positive vector the negative
// above needs: the same seam, an EXTERNAL gap, and the reclaim books BOTH. A
// re-call of a vendor is a genuinely new attempt (§10.3 "re-call a dead vendor /
// mint a fresh service instance"), so it spends the budget that bounds attempts
// — and it is still a re-arm, so it lengthens the next wait. A rule that stopped
// booking attempts for everything would be indistinguishable from no budget.
func TestSweep_ExternalReclaim_IsAnAttempt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 5}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixExternalBooks"
			const gap = "missing_x"
			h.seedTarget(&Target{
				TargetID: targetID,
				Gaps:     map[string]GapAction{gap: {Action: actionTriggerLoom, Pattern: "vendorFlow", Subject: "row.entityKey"}},
			})
			// A pattern whose every step runs to an outcome with no human in it:
			// what makes externalDispatchGap classify the reclaim as a fresh call.
			seedPatternSpec(t, h.engine.source, "vendorFlow", stepKindExternalTask)

			entityID := testNanoID(t)
			key := markKey(targetID, entityID, gap)
			h.putMark(t, ctx, key, fixtureMark(targetID, entityID, gap, actionTriggerLoom, pastLease()))
			row := map[string]any{
				"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
				"inflight_x": false,
			}
			if tc.budget > 0 {
				row["maxretries_x"] = tc.budget
			}
			h.putRow(t, ctx, targetID, entityID, row)

			h.pass(ctx)
			h.nextOp(t)

			doc := readCount(t, ctx, h.conn, targetID, entityID, gap)
			if doc.Count != 1 {
				t.Fatalf("retry budget = %d, want 1: an external reclaim re-calls the vendor and spends the budget", doc.Count)
			}
			if doc.Reclaims != 1 {
				t.Fatalf("re-arm tally = %d, want 1: it is still a re-arm of the same open episode", doc.Reclaims)
			}
		})
	}
}

// TestBackoffInterval_ReadsReclaims pins WHICH tally the reclaim backoff is
// keyed on, at the two sites that read it — the pacing decision and the
// mark-TTL widening that has to outlast it.
//
// The vector is chosen so the two answers disagree: an episode with five
// ATTEMPTS behind it and no re-arms at all. Keyed on the re-arm tally (0) the
// wait is the base and this reclaim is due; keyed on the attempt count (5) it
// would be base × 2^4 and the sweep would sit on its hands for sixteen hours.
// Swap the exponent's input back to Count and this reds on both halves.
func TestBackoffInterval_ReadsReclaims(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Hour })
	h.agePastWarmup()

	const targetID = "fixBackoffTally"
	const gap = "missing_x"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{gap: {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"}},
	})
	seedPatternSpec(t, h.engine.source, "ghostFlow", stepKindUserTask)

	entityID := testNanoID(t)
	// Five attempts, zero re-arms — the disagreement the vector rests on.
	for i := 0; i < 5; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, gap, actionTriggerLoom, true, false, false); err != nil {
			t.Fatalf("seed attempts: %v", err)
		}
	}
	key := markKey(targetID, entityID, gap)
	m := fixtureMark(targetID, entityID, gap, actionTriggerLoom, pastLease())
	m.ClaimedAt = substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)) // past the base, far short of base × 2^4
	h.putMark(t, ctx, key, m)
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
	})

	h.pass(ctx)

	h.nextOp(t) // the reclaim is due on the re-arm tally, and fired
	if reclaims, suppressed, _, _, _, _ := h.engine.sweep.metrics(); reclaims != 1 || suppressed != 0 {
		t.Fatalf("metrics: reclaims=%d suppressed=%d, want 1, 0 — a wait keyed on the ATTEMPT count would have "+
			"suppressed this reclaim for sixteen hours", reclaims, suppressed)
	}

	// The widening reads the same tally: the re-armed mark must outlast the NEXT
	// wait, which is one re-arm's worth (this pass's), not six attempts' worth.
	stream, err := h.conn.JetStream().Stream(ctx, "KV_weaver-state")
	if err != nil {
		t.Fatalf("open weaver-state stream: %v", err)
	}
	raw, err := stream.GetLastMsgForSubject(ctx, "$KV.weaver-state."+key)
	if err != nil {
		t.Fatalf("read the reclaimed mark's raw message: %v", err)
	}
	wantTTL := (h.engine.sweep.backoffInterval(1) + 2*h.engine.sweep.interval).String()
	if got := raw.Header.Get("Nats-TTL"); got != wantTTL {
		t.Fatalf("reclaimed mark Nats-TTL = %q, want %q (sized on the re-arm tally the pacing uses)", got, wantTTL)
	}
}

// TestIncrementDispatchCount_LegChangeRestartsCount pins the per-leg budget
// semantics inside the booking itself: attempts are charged to a LEG, and a
// chain that has moved to a different leg starts that leg's bookkeeping fresh.
// The clean boundary deletes the whole document, so this is the belt-and-braces
// for a boundary nothing witnessed — and it is what keeps a never-released chain
// honest rather than silently carrying the previous leg's spend.
//
// The whole leg-scoped record restarts, not just the tally the gate reads. The
// re-arm history and the last escalation instant describe the episode the
// PREVIOUS leg ran; carried across, the new leg's very first reclaim would wait
// the old leg's exponential — hours of silence for a chain that has just moved.
func TestIncrementDispatchCount_LegChangeRestartsCount(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)

	const targetID, gap = "t1", "missing_x"
	entityID := "entityAAAAAAAAAAAAAA"

	for i := 0; i < 5; i++ {
		if _, err := m.incrementDispatchCount(ctx, targetID, entityID, gap, "legA", true, false, true); err != nil {
			t.Fatalf("attempt on legA #%d: %v", i, err)
		}
	}
	// A re-arm of the same leg, so the tally carried across the change is real.
	if _, err := m.incrementDispatchCount(ctx, targetID, entityID, gap, "legA", false, true, true); err != nil {
		t.Fatalf("re-arm on legA: %v", err)
	}
	// An escalation instant on the document too: it is leg-scoped in the same
	// way, and a new leg must not be paced against the old leg's last fire.
	seeded, _, err := m.getDispatchCount(ctx, targetID, entityID, gap)
	if err != nil {
		t.Fatalf("read the seeded document: %v", err)
	}
	if _, err := m.bookEscalation(ctx, targetID, entityID, gap, time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("seed the escalation instant: %v", err)
	}
	before, _, err := m.getDispatchCount(ctx, targetID, entityID, gap)
	if err != nil || before.Count != 5 || before.Leg != "legA" || before.Reclaims != 2 || before.EscalatedAt == "" {
		t.Fatalf("seeded document = %+v (err=%v, after %+v), want count 5 leg legA reclaims 2 and an escalation instant",
			before, err, seeded)
	}

	after, err := m.incrementDispatchCount(ctx, targetID, entityID, gap, "legB", true, false, true)
	if err != nil {
		t.Fatalf("attempt on legB: %v", err)
	}
	if after.Count != 1 {
		t.Fatalf("count after the leg change = %d, want 1: legA's five attempts bound legA, not legB", after.Count)
	}
	if after.Leg != "legB" {
		t.Fatalf("leg after the change = %q, want legB", after.Leg)
	}
	if after.Reclaims != 0 {
		t.Fatalf("re-arm tally = %d, want 0: legA's re-arms paced legA's episode, and legB's first reclaim is "+
			"its own first", after.Reclaims)
	}
	if after.EscalatedAt != "" {
		t.Fatalf("escalation instant = %q, want it cleared: it records when legA's chain was handed to the "+
			"reasoning tier, and legB has not been", after.EscalatedAt)
	}
}

// TestIncrementDispatchCount_CandidatesPickChangeKeepsCount is the boundary of
// the restart above: it is a LEG rule, and only a goal gap has legs.
//
// Every other shape re-decides its dispatch per EPISODE from inputs that move
// under it — a candidates gap ranks over the `__effect` close-rate windows,
// which a concurrent close rewrites — so a different ref on the next attempt is
// the ordinary case, not a boundary. Restarting there, two candidates
// alternating would hold the count at 1 forever: the exhaustion gate would never
// see the cap reached, the escalation would never fire, and the gap would sit in
// exactly the silent park §10.8 forbids. So the pick is recorded and the budget
// keeps counting.
//
// The goal row is the same document and the same call one flag apart, so the
// pair states what the flag decides and nothing else.
func TestIncrementDispatchCount_CandidatesPickChangeKeepsCount(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		legScoped bool
		wantCount int
	}{
		{"candidatesGapKeepsCounting", false, 3},
		{"goalGapRestartsAtTheBoundary", true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			m := newStateTestStore(t, ctx)

			const targetID, gap = "t1", "missing_x"
			entityID := "entityAAAAAAAAAAAAAA"

			// Two attempts already charged, against a pick that is not the one
			// the next episode makes.
			putStateValue(t, ctx, m.conn, countKey(targetID, entityID, gap),
				dispatchCount{Count: 2, Leg: actionDirectOp})

			after, err := m.incrementDispatchCount(ctx, targetID, entityID, gap, actionAssignTask, true, false, tc.legScoped)
			if err != nil {
				t.Fatalf("attempt on the new pick: %v", err)
			}
			if after.Count != tc.wantCount {
				t.Fatalf("count = %d, want %d", after.Count, tc.wantCount)
			}
			if after.Leg != actionAssignTask {
				t.Fatalf("leg = %q, want %q: the document always records the pick the attempts are charged to",
					after.Leg, actionAssignTask)
			}
		})
	}
}

// TestResetRetryBudget_KeepsReclaims pins what an operator's un-park does and
// does not say. It re-arms the budget — that is the whole verb. It says nothing
// about how many times the sweep has re-armed the episode, which leg the chain
// is on, or when the escalation last fired, and zeroing those would restart the
// pacing of an episode that may still be open: a task sitting on a human's
// inbox would go back to being re-dispatched every half hour.
func TestResetRetryBudget_KeepsReclaims(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixResetKeeps"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
	})
	entityID := testNanoID(t)
	for i := 0; i < 2; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x", actionDirectOp, true, true, false); err != nil {
			t.Fatalf("seed attempt+re-arm: %v", err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, "missing_x", actionDirectOp, false, true, false); err != nil {
			t.Fatalf("seed re-arm: %v", err)
		}
	}
	putStateValue(t, ctx, h.conn, countKey(targetID, entityID, "missing_x"), dispatchCount{
		Count: 2, Reclaims: 5, Leg: actionDirectOp, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Hour)),
	})

	previous, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, "missing_x")
	if err != nil || previous != 2 {
		t.Fatalf("reset = (%d, %v), want (2, nil)", previous, err)
	}

	doc := readCount(t, ctx, h.conn, targetID, entityID, "missing_x")
	if doc.Count != 0 {
		t.Fatalf("count after the reset = %d, want 0", doc.Count)
	}
	if doc.Reclaims != 5 {
		t.Fatalf("re-arm tally after the reset = %d, want 5: the un-park re-arms the budget, not the pacing", doc.Reclaims)
	}
	if doc.Leg != actionDirectOp || doc.EscalatedAt == "" {
		t.Fatalf("document after the reset = %+v, want the leg and the escalation instant carried forward", doc)
	}
}

// --- Increment 2: the escalation preserves the leg pin ----------------------

// TestHandleRow_ExhaustedLegWhoseEffectsHoldReleasesAndAdvances is the payoff,
// on lane 1: a gap whose budget is spent and which has been handed to the
// reasoning tier still keeps the plan leg it was on, so the moment that leg's
// declared effects hold in the row the pin releases and the NEXT leg dispatches
// — the §10.8 promise the escalation was silently destroying.
//
// The route matters as much as the outcome: an exhausted gap's delivery is read
// by the suppression gate and routed to the escalation, which is the only lane-1
// leg it reaches — dispatchGap, the other caller of the release, is never
// reached for it. So the boundary has to be tested inside the escalation, above
// its live-mark Ack, or a delivery of the changed row does nothing at all.
//
// The uncapped half proves the vector is about the LEG boundary and not about
// the cap: with no maxretries_<g> the gap is never exhausted, so lane 1 releases
// through dispatchGap instead — same leg, same advance, a different route.
func TestHandleRow_ExhaustedLegWhoseEffectsHoldReleasesAndAdvances(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 2}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixLaneOneRelease"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)

			// The state the filed harm leaves behind: a spent budget charged to
			// legA, and the escalation's own mark over the leg it displaced.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			putStateValue(t, ctx, h.conn, markKey(targetID, entityID, goalLegGap),
				escalationMark(targetID, entityID, "legA", futureLease()))
			// legA's own pending confidence slot, opened when it dispatched: the
			// release is what credits its close, and a window that was never
			// opened would let that assertion pass over a slot nothing wrote.
			if err := h.engine.marks.recordEffectDispatch(ctx, targetID, goalLegGap, "legA"); err != nil {
				t.Fatalf("seed legA's pending confidence slot: %v", err)
			}
			issueKey := issueKeyGapEntity(targetID, entityID, goalLegGap)
			h.engine.issues.set(issueKey, "warning", codeGapEscalatedToAugur, "escalated on an earlier pass")

			// legA's op has committed: the re-projected row carries aDone, while
			// the gap itself stays open (bDone still missing).
			row := goalLegRow(entityID, tc.budget, map[string]any{"aDone": true})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 11, 1)); dec != substrate.Ack {
				t.Fatalf("the leg-boundary delivery must Ack, got %v", dec)
			}

			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: the completed leg must release and advance", op["operationType"])
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found {
				t.Fatalf("the advance must leave a fresh mark (found=%v err=%v)", found, err)
			}
			if rec.Action != "legB" || rec.EscalatedFrom != "" {
				t.Fatalf("advanced mark = %+v, want it pinning legB with no displaced leg — the escalation is over", rec)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 1 || doc.Leg != "legB" {
				t.Fatalf("count document = %+v, want a fresh budget charged to legB (the release deleted legA's)", doc)
			}
			stats, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, "legA")
			if err != nil || !ok || len(stats.Window) != 1 || !stats.Window[0] {
				t.Fatalf("legA's confidence window = %+v (present=%v err=%v), want one CLOSED slot — "+
					"the release is the leg's close credit", stats, ok, err)
			}
			if _, standing := issueAt(h.engine.issues, issueKey); standing {
				t.Fatalf("the escalation latch must retire with the budget it described (issues: %+v)",
					h.engine.issues.snapshot())
			}
			h.requireNoOp(t)
		})
	}
}

// TestSweep_CountLegReleasesEscalatedLegFromCountLeg is the same boundary
// reached from the leg that has NO mark to read a leg off: a row that has gone
// quiet, whose only durable trace is its retry budget. The leg the attempts were
// charged to lives on the count document precisely so this leg can test the
// boundary too — otherwise a gap whose escalation mark had TTL'd away would sit
// open forever with its next leg fully satisfiable.
//
// The uncapped half is the fixture rule's own point and not a duplicate: with no
// maxretries_<g> the gap never exhausts (the engine's own default budget is
// directOp-only), so this leg reaches no suppression verdict at all and must
// leave the chain to its ladder rather than dispatching from a budget nothing
// spent.
func TestSweep_CountLegReleasesEscalatedLegFromCountLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name        string
		budget      int
		wantRelease bool
	}{{"capped", 2, true}, {"uncapped", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixCountLegRelease"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, map[string]any{"aDone": true}))
			if h.markExists(t, ctx, markKey(targetID, entityID, goalLegGap)) {
				t.Fatal("setup: this vector requires a markless gap")
			}

			h.pass(ctx)

			if !tc.wantRelease {
				h.requireNoOp(t)
				if doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap); doc.Count != 2 || doc.Leg != "legA" {
					t.Fatalf("count document = %+v, want it untouched: an uncapped gap never exhausts, so this "+
						"leg has no verdict to act on", doc)
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: the count leg must release the completed leg and advance",
					op["operationType"])
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.Action != "legB" {
				t.Fatalf("the advance must mint a mark pinning legB (rec=%+v found=%v err=%v)", rec, found, err)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 1 || doc.Leg != "legB" {
				t.Fatalf("count document = %+v, want a fresh budget charged to legB", doc)
			}
			h.requireNoOp(t)
		})
	}
}

// TestSweep_ReclaimReleasesEscalatedLeg is the boundary reached from the sweep's
// mark leg, over the escalation's own mark. The leg is recovered from
// escalatedFrom alone — the count document records none here — so this vector
// is what proves the field is threaded and read, not merely written. Drop the
// propagation and the sweep re-escalates a gap whose next leg is ready.
func TestSweep_ReclaimReleasesEscalatedLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 2}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixReclaimRelease"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)

			// No leg on the count document: escalatedFrom is the only carrier.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{Count: 2})
			putStateValue(t, ctx, h.conn, markKey(targetID, entityID, goalLegGap),
				escalationMark(targetID, entityID, "legA", pastLease()))
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, map[string]any{"aDone": true}))

			h.pass(ctx)

			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB — a stale escalation over a COMPLETED leg is a boundary, "+
					"not an episode to re-escalate", op["operationType"])
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.Action != "legB" {
				t.Fatalf("the advance must mint a mark pinning legB (rec=%+v found=%v err=%v)", rec, found, err)
			}
			h.requireNoOp(t)
		})
	}
}

// TestEscalateExhaustedGap_EffectsNotHolding_StillEscalates is the regression
// guard on the release: a leg whose effects do NOT hold releases nothing, the
// escalation stands, and the budget is left exactly where it was. Without this
// the release would be indistinguishable from "re-plan whenever the row
// changes", which is the unbounded loop the level test exists to avoid — a
// bgcheck lapsing changes the row without any leg making progress.
//
// The uncapped half reaches the same "nothing moves" by a different route, and
// that is the job the fixture rule gives it: with no maxretries_<g> the gap
// never exhausts, so the delivery goes to the ordinary dispatch leg instead —
// where the standing escalation's mark takes the anti-storm drop, and an
// unfinished leg still releases nothing.
func TestEscalateExhaustedGap_EffectsNotHolding_StillEscalates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name          string
		budget        int
		wantEscalated bool
	}{{"capped", 2, true}, {"uncapped", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixNoRelease"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			putStateValue(t, ctx, h.conn, markKey(targetID, entityID, goalLegGap),
				escalationMark(targetID, entityID, "legA", pastLease()))

			// aDone absent: legA has not finished.
			row := goalLegRow(entityID, tc.budget, nil)
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 12, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}

			if tc.wantEscalated {
				if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
					t.Fatalf("operationType = %v, want the reasoning op: a leg that has not finished is still escalated",
						op["operationType"])
				}
			} else {
				h.requireNoOp(t)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 2 || doc.Leg != "legA" {
				t.Fatalf("count document = %+v, want count 2 charged to legA — nothing released, nothing booked", doc)
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.EscalatedFrom != "legA" {
				t.Fatalf("the escalation must keep carrying the displaced leg (rec=%+v found=%v err=%v)", rec, found, err)
			}
			if hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted") {
				t.Fatalf("this target escalates its exhausted gaps, so the un-escalated alert must never stand: %+v",
					h.engine.issues.snapshot())
			}
			h.requireNoOp(t)
		})
	}
}

// TestEscalateExhaustedGap_OldShapeMark_DoesNotRelease is the migration row: an
// escalation mark written before escalatedFrom existed, over a count document
// written before the leg was recorded. Nothing anywhere names the leg, so
// nothing can test its boundary and the gap keeps escalating — the honest
// answer, and the reason the one live instance of this shape is re-armed by
// hand rather than expected to heal itself.
//
// The uncapped half says the same thing from the ordinary dispatch leg, which is
// where a gap with no maxretries_<g> goes: it too finds no leg named anywhere,
// releases nothing, and leaves the standing escalation alone.
func TestEscalateExhaustedGap_OldShapeMark_DoesNotRelease(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name          string
		budget        int
		wantEscalated bool
	}{{"capped", 2, true}, {"uncapped", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixOldShapeMark"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)

			// The old shape, both halves: no escalatedFrom, no leg.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{Count: 2})
			putStateValue(t, ctx, h.conn, markKey(targetID, entityID, goalLegGap),
				escalationMark(targetID, entityID, "", pastLease()))

			// The row DOES satisfy legA — the release would fire if anything named it.
			row := goalLegRow(entityID, tc.budget, map[string]any{"aDone": true})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 13, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}

			if tc.wantEscalated {
				if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
					t.Fatalf("operationType = %v, want the reasoning op: an unnamed leg has no boundary to test",
						op["operationType"])
				}
			} else {
				h.requireNoOp(t)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 2 {
				t.Fatalf("count = %d, want 2: nothing was released and an escalation books nothing", doc.Count)
			}
			if rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap); err != nil || !found || rec.Action != actionDirectOp {
				t.Fatalf("the escalation must still own the gap (rec=%+v found=%v err=%v)", rec, found, err)
			}
			h.requireNoOp(t)
		})
	}
}

// TestEscalateExhaustedGap_NoAugurPolicy_StillReleases pins the ORDER: the
// release is a retirement and sits above every cannot-act guard, so a target
// with no augur block — which would otherwise only raise a standing warning and
// Ack — advances at the leg boundary just the same. A leg finishing is a fact
// about the gap's own chain; it has nothing to do with whether the target opted
// into the reasoning tier.
//
// The uncapped half reaches the identical outcome down the OTHER lane-1 leg —
// no exhaustion, so the delivery goes to the ordinary dispatch path and releases
// there. Two routes, one boundary: which is the point of putting the release
// inside a function both reach rather than at one caller.
func TestEscalateExhaustedGap_NoAugurPolicy_StillReleases(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 2}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixNoAugurRelease"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			putStateValue(t, ctx, h.conn, markKey(targetID, entityID, goalLegGap),
				mark{
					TargetID: targetID, EntityKey: "vtx.leaseApp." + entityID, Gap: goalLegGap,
					Action: "legA", ClaimID: testNanoIDStatic("legAClaim00000000"),
					ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
					LeaseExpiresAt: futureLease(),
				})
			issueKey := issueKeyGapEntity(targetID, entityID, goalLegGap)
			h.engine.issues.set(issueKey, "warning", "GapBudgetExhausted", "spent on an earlier pass")

			row := goalLegRow(entityID, tc.budget, map[string]any{"aDone": true})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 14, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}

			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: the boundary is honoured whether or not the target escalates",
					op["operationType"])
			}
			if _, standing := issueAt(h.engine.issues, issueKey); standing {
				t.Fatalf("the spent-budget latch must retire with the budget (issues: %+v)", h.engine.issues.snapshot())
			}
			h.requireNoOp(t)
		})
	}
}

// --- Increment 3: the escalation is booked nowhere and paced like a re-arm ---

// TestEscalateExhaustedGap_BooksNeitherCountNorEffect pins what the filed harm's
// third clause measured: 309 dispatches against a budget of 6, and a confidence
// window filled with twenty pending slots no close could ever answer.
//
// The escalation is a dispatch ABOUT the gap's spent chain, not a member of it.
// Its "action" is a dispatch class, never an entry in the gap's catalog, so
// booking it into the retry budget spends a budget it exists only because of,
// and booking it into an `__effect` window opens a slot keyed on a ref nothing
// can close. It books neither.
// The uncapped half is the positive control the negative needs: the same gap
// with no maxretries_<g> never exhausts, so it dispatches its own leg instead —
// which DOES book, on both tallies. A rule that booked nothing for anything
// would be indistinguishable from the one under test.
func TestEscalateExhaustedGap_BooksNeitherCountNorEffect(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name          string
		budget        int
		wantEscalated bool
	}{{"capped", 2, true}, {"uncapped", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixBooksNothing"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})

			row := goalLegRow(entityID, tc.budget, nil)
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 15, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}
			if !tc.wantEscalated {
				if op := h.nextOp(t); op["operationType"] != "DoA" {
					t.Fatalf("operationType = %v, want DoA: an uncapped gap never exhausts, so it dispatches its "+
						"own leg", op["operationType"])
				}
				booked := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
				if booked.Count != 3 || booked.Leg != "legA" {
					t.Fatalf("count document = %+v, want {3 legA}: a real leg dispatch DOES spend the budget", booked)
				}
				stats, _, present, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, "legA")
				if err != nil || !present || len(stats.Window) != 1 {
					t.Fatalf("legA's confidence window = %+v (present=%v err=%v), want the one slot its dispatch "+
						"booked", stats, present, err)
				}
				h.requireNoOp(t)
				return
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("setup: the escalation must actually fire, got %v — a negative over a dispatch that never "+
					"happened proves nothing", op["operationType"])
			}

			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 2 {
				t.Fatalf("retry budget = %d, want 2: the escalation must not spend the budget whose exhaustion caused it",
					doc.Count)
			}
			if doc.Leg != "legA" {
				t.Fatalf("leg = %q, want legA: an escalation does not re-charge the chain to its own dispatch class", doc.Leg)
			}
			if _, _, present, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, actionDirectOp); err != nil || present {
				t.Fatalf("a confidence window opened at the escalation's dispatch class (present=%v err=%v) — "+
					"its slots are keyed on a catalog ref, and nothing can ever close this one", present, err)
			}
			h.requireNoOp(t)
		})
	}
}

// TestEscalateExhaustedGap_RefireIsPacedByReclaims pins the re-fire cadence.
// The arm exists for one case — a reasoning episode whose claim was never
// minted, which nothing else re-derives — and past the first commit every
// re-fire is an op the Processor rejects create-only. So it waits the same
// exponential every other re-arm of an open episode waits, level-tested against
// the last fire rather than against the mark it may have lost.
// The uncapped half is where the re-fire arm is unreachable at all — an
// uncapped gap never exhausts, so its delivery goes to the ordinary dispatch leg
// and the standing escalation's mark takes the anti-storm drop there. Nothing
// fires and nothing is booked on either delivery, which is the pacing question
// dissolving rather than being answered differently.
func TestEscalateExhaustedGap_RefireIsPacedByReclaims(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name       string
		budget     int
		wantRefire bool
	}{{"capped", 2, true}, {"uncapped", 0, false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixRefirePaced"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)
			markK := markKey(targetID, entityID, goalLegGap)

			// Two re-arms behind it: the wait is base × 2 = one hour at the defaults.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Count: 2, Leg: "legA", Reclaims: 2,
				EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-10 * time.Minute)),
			})
			putStateValue(t, ctx, h.conn, markK, escalationMark(targetID, entityID, "legA", pastLease()))
			before, err := h.conn.KVGet(ctx, "weaver-state", markK)
			if err != nil {
				t.Fatalf("read the stale mark: %v", err)
			}

			row := goalLegRow(entityID, tc.budget, nil)
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 16, 1)); dec != substrate.Ack {
				t.Fatalf("paced delivery: decision = %v, want Ack", dec)
			}
			h.requireNoOp(t)
			if after, err := h.conn.KVGet(ctx, "weaver-state", markK); err != nil || after.Revision != before.Revision {
				t.Fatalf("a paced re-fire must leave the stale mark alone — its own TTL is the backstop "+
					"(rev %d, want %d, err=%v)", after.Revision, before.Revision, err)
			}

			// Past the window: the re-fire is due, and it books its own instant and
			// lengthens the next wait.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Count: 2, Leg: "legA", Reclaims: 2,
				EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-61 * time.Minute)),
			})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 17, 1)); dec != substrate.Ack {
				t.Fatalf("due delivery: decision = %v, want Ack", dec)
			}
			if !tc.wantRefire {
				h.requireNoOp(t)
				doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
				if doc.Reclaims != 2 || doc.Count != 2 {
					t.Fatalf("count document = %+v, want {count 2, leg legA, reclaims 2} untouched: an uncapped "+
						"gap never reaches the re-fire arm at all", doc)
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op once the window has elapsed", op["operationType"])
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Reclaims != 3 {
				t.Fatalf("re-arm tally = %d, want 3: a re-fire is a re-arm and must lengthen the next wait", doc.Reclaims)
			}
			if doc.Count != 2 {
				t.Fatalf("retry budget = %d, want 2: pacing a re-fire must not book one", doc.Count)
			}
			firedAt, perr := time.Parse(time.RFC3339Nano, doc.EscalatedAt)
			if perr != nil || time.Since(firedAt) > time.Minute {
				t.Fatalf("escalatedAt = %q (parse err=%v), want the instant this re-fire dispatched at", doc.EscalatedAt, perr)
			}
			h.requireNoOp(t)
		})
	}
}

// --- Increment 4: the verb and the arm resolve a leg the same way -----------

// TestResetRetryBudget_GoalGap_DirectOpLegAccepted pins the verb's acceptance of
// a plan-time-resolved gap. The old refusal reasoned about the ARM ("the sweep's
// re-arm never runs a plan to find out what it would fire"), which was a claim
// about the arm's implementation rather than about the gap: resolving the leg
// costs a bounded regression over the catalog, no admission token and no issue
// clear. Both now resolve it, so the verb accepts exactly what the arm fires —
// and the re-armed budget is followed by the arm actually firing it.
//
// The cap is not part of the question either way — the verb answers from the
// gap's CLASS, and the arm fires from a re-armed count — so the uncapped half
// must reach the identical acceptance and the identical dispatch.
func TestResetRetryBudget_GoalGap_DirectOpLegAccepted(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		rowBudget int
	}{{"capped", 2}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixResetGoalDirect"
			const spent = 2
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
			entityID := testNanoID(t)
			h.seedCount(t, ctx, targetID, entityID, goalLegGap, spent)
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.rowBudget, nil))

			previous, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, goalLegGap)
			if err != nil || previous != spent {
				t.Fatalf("reset of a goal gap whose leg dispatches a directOp = (%d, %v), want (%d, nil)",
					previous, err, spent)
			}

			// The acceptance, executed: the arm resolves the same leg and fires it.
			h.pass(ctx)
			if op := h.nextOp(t); op["operationType"] != "DoA" {
				t.Fatalf("operationType = %v, want DoA — the verb's acceptance means nothing unless the arm dispatches",
					op["operationType"])
			}
			h.requireNoOp(t)
		})
	}
}

// TestResetRetryBudget_GoalGap_AssignTaskLegRefused is the same resolver
// answering the other way. A goal gap whose current leg assigns a human task is
// collapse-only: the artifact may still be open, a markless re-arm would mint a
// second one, and the arm therefore declines it permanently — so the verb
// refuses rather than writing a zero nothing will act on, and its reason names
// the resolved action and where it came from.
//
// The refusal is a property of the leg's dispatch shape, so the uncapped half
// must reach it identically: a cap decides when a gap is parked, never whether
// re-arming it would duplicate a human's task.
func TestResetRetryBudget_GoalGap_AssignTaskLegRefused(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		rowBudget int
	}{{"capped", 2}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixResetGoalTask"
			const spent = 2
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionAssignTask, false))
			entityID := testNanoID(t)
			h.seedCount(t, ctx, targetID, entityID, goalLegGap, spent)
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.rowBudget, nil))

			_, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, goalLegGap)
			if err == nil {
				t.Fatal("a goal gap whose leg assigns a human task must be refused: the arm would never act on the reset")
			}
			if !strings.Contains(err.Error(), actionAssignTask) || !strings.Contains(err.Error(), "plan resolves to") {
				t.Fatalf("error = %q, want it to name the RESOLVED action and that the plan resolved it", err)
			}
			if got := h.countValue(t, ctx, targetID, entityID, goalLegGap); got != spent {
				t.Fatalf("refused reset left the budget at %d, want %d (nothing written)", got, spent)
			}

			// The refusal's reason, executed: at the budget an accepted reset would have
			// written, the arm still declines this gap.
			h.seedReArmedCount(t, ctx, targetID, entityID, goalLegGap)
			h.pass(ctx)
			h.requireNoOp(t)
			if h.markExists(t, ctx, markKey(targetID, entityID, goalLegGap)) {
				t.Fatal("a re-armed collapse-only leg must stay markless — which is why the verb refuses it")
			}
		})
	}
}

// --- The reclaim's classification: shape, never the recorded name ------------

// seedGoalLegEpisode puts a goal-mode gap into the durable state one real
// dispatch of its first leg leaves behind: an expired mark pinning the leg's own
// catalog Ref, a retry budget of one attempt charged to that leg, and a single
// pending slot in the leg's `__effect` window. It returns the mark key and the
// claimId the episode's artifact is seeded from.
func seedGoalLegEpisode(t *testing.T, ctx context.Context, h *sweepHarness, targetID, entityID string) (string, string) {
	t.Helper()
	if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, goalLegGap, "legA", true, false, true); err != nil {
		t.Fatalf("seed the leg's one real attempt: %v", err)
	}
	if _, err := h.conn.KVCreate(ctx, "weaver-state", effectKey(targetID, goalLegGap, "legA"),
		mustMarshalEffectStats(t, effectStats{Window: []bool{false}})); err != nil {
		t.Fatalf("seed the leg's effect window: %v", err)
	}
	key := markKey(targetID, entityID, goalLegGap)
	rec := fixtureMark(targetID, entityID, goalLegGap, "legA", pastLease())
	rec.ClaimID = testNanoIDStatic("goalLegClaim")
	h.putMark(t, ctx, key, rec)
	return key, rec.ClaimID
}

// TestSweep_GoalLegReclaim_IsCollapseOnlyByDispatchShape is the shape rule, on
// the seam that decides a reclaim's whole disposition.
//
// A planned-mode mark records the LEG's own catalog Ref — "legA", "setTerms" —
// which is not a dispatch contract type at all. Classify that string directly
// and every goal leg reads as not-collapse-only, whatever it actually
// dispatches: the reclaim is unpaced (a re-fire every sweep interval), it books
// an attempt against the retry budget, and it appends a pending slot to the
// `__effect` window. A human task nobody has opened then spends a budget of six
// in minutes and lands on the reasoning tier — the filed harm, in the one gap
// shape whose mark records a catalog ref rather than a dispatch class.
//
// The leg here dispatches assignTask, so the re-dispatch collapses onto the task
// already open (its claimId is preserved verbatim) and mounts nothing. Five
// reclaims must therefore leave the budget where the one real dispatch left it,
// leave the window one slot long, and keep the same claimId; and a sixth inside
// its backoff window must not fire at all.
//
// Run capped and uncapped: the cap changes what exhaustion would mean and must
// change nothing about how the reclaim is classified.
func TestSweep_GoalLegReclaim_IsCollapseOnlyByDispatchShape(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 3}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			// A negligible base isolates the BOOKING half: every reclaim below
			// fires, so nothing is hidden behind the pacing. The pacing half sets
			// its own base at the end.
			h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Millisecond })
			h.agePastWarmup()

			const targetID = "fixGoalLegShape"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionAssignTask, false))
			// assignTask resolves its forOperation link through the op's
			// meta-vertex, so the leg's operation has to be a loaded one.
			h.engine.source.mu.Lock()
			h.engine.source.opMetaByType["DoA"] = "vtx.meta." + testNanoID(t)
			h.engine.source.mu.Unlock()

			entityID := testNanoID(t)
			key, claimID := seedGoalLegEpisode(t, ctx, h, targetID, entityID)
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, nil))

			const reclaims = 5
			for i := 0; i < reclaims; i++ {
				h.pass(ctx)
				if op := h.nextOp(t); op["operationType"] != "CreateTask" {
					t.Fatalf("reclaim %d dispatched %v, want CreateTask — the reclaim re-arms the leg it pinned",
						i+1, op["operationType"])
				}
				h.reexpireMark(t, ctx, key)
			}

			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 1 {
				t.Fatalf("retry budget = %d, want 1: a goal leg that assigns a human task is collapse-only, so "+
					"%d reclaims of it mounted no attempt between them", doc.Count, reclaims)
			}
			if doc.Reclaims != reclaims {
				t.Fatalf("re-arm tally = %d, want %d: every reclaim re-armed the episode, and that is what paces the next",
					doc.Reclaims, reclaims)
			}
			if doc.Leg != "legA" {
				t.Fatalf("count document leg = %q, want legA: the budget stays charged to the leg it was spent on", doc.Leg)
			}
			row := goalLegRow(entityID, tc.budget, nil)
			if _, exhausted, _, _, _ := h.engine.gapSuppressed(ctx, targetID, entityID, row, goalLegGap, ""); exhausted {
				t.Fatalf("the gap must not read exhausted after %d reclaims that spent nothing (budget %d)",
					reclaims, tc.budget)
			}
			stats, _, found, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, "legA")
			if err != nil || !found {
				t.Fatalf("read the leg's effect window: found=%v err=%v", found, err)
			}
			if len(stats.Window) != 1 {
				t.Fatalf("effect window = %v, want the one slot the real dispatch booked: a collapsed re-dispatch "+
					"appends a pending slot no close can ever answer", stats.Window)
			}
			rec, _ := h.readMark(t, ctx, key)
			if rec.ClaimID != claimID {
				t.Fatalf("claimId = %q, want %q preserved verbatim — it is what makes the re-dispatch collapse "+
					"onto the open task instead of minting a second one", rec.ClaimID, claimID)
			}
			if rec.Action != "legA" {
				t.Fatalf("mark action = %q, want legA: a reclaim re-pins the same leg, never re-plans", rec.Action)
			}
			if got, suppressed, _, _, _, _ := h.engine.sweep.metrics(); got != reclaims || suppressed != 0 {
				t.Fatalf("metrics: reclaims=%d suppressed=%d, want %d, 0", got, suppressed, reclaims)
			}

			// The pacing half, on the same episode: the one predicate that just
			// spared the budget also paces the re-fire. Widen the backoff and
			// hand the sweep an episode dispatched moments ago — a collapse-only
			// reclaim inside its window does nothing at all.
			h.engine.sweep.backoffBase = time.Hour
			rec.ClaimedAt = substrate.FormatTimestamp(time.Now())
			rec.LeaseExpiresAt = pastLease()
			putStateValue(t, ctx, h.conn, key, rec)

			h.pass(ctx)

			h.requireNoOp(t)
			if got, suppressed, _, _, _, _ := h.engine.sweep.metrics(); got != reclaims || suppressed != 1 {
				t.Fatalf("metrics: reclaims=%d suppressed=%d, want %d, 1 — a goal leg's re-fire is paced exactly "+
					"like a static one's", got, suppressed, reclaims)
			}
		})
	}
}

// TestSweep_GoalLegReclaim_DirectOpLegIsAnAttempt is the control the rule above
// needs: the same goal target, the same mark shape, the same leg Ref — and a leg
// that dispatches a directOp instead. Nothing collapses that re-submission onto
// an existing artifact, so every reclaim of it IS an attempt: it advances the
// budget the exhaustion gate reads and books a slot in the leg's window.
//
// Without this vector a rule that classified every goal leg as collapse-only
// would be indistinguishable from the one under test, and a goal chain's retry
// budget would bound nothing at all.
//
// Run capped and uncapped. The cap is chosen wide enough that the reclaims never
// reach the gate, so both halves must book identically: what a reclaim BOOKS is
// decided by the dispatch's shape, and a gap with no cap has exactly as many
// attempts against it — it simply has nothing bounding them.
func TestSweep_GoalLegReclaim_DirectOpLegIsAnAttempt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 20}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixGoalLegAttempt"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
			entityID := testNanoID(t)
			key, _ := seedGoalLegEpisode(t, ctx, h, targetID, entityID)
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, nil))

			const reclaims = 3
			for i := 0; i < reclaims; i++ {
				h.pass(ctx)
				if op := h.nextOp(t); op["operationType"] != "DoA" {
					t.Fatalf("reclaim %d dispatched %v, want DoA", i+1, op["operationType"])
				}
				h.reexpireMark(t, ctx, key)
			}

			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 1+reclaims {
				t.Fatalf("retry budget = %d, want %d: a re-submitted op collapses onto nothing, so each reclaim of it "+
					"is a genuinely new attempt", doc.Count, 1+reclaims)
			}
			if doc.Reclaims != reclaims {
				t.Fatalf("re-arm tally = %d, want %d", doc.Reclaims, reclaims)
			}
			stats, _, found, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, "legA")
			if err != nil || !found {
				t.Fatalf("read the leg's effect window: found=%v err=%v", found, err)
			}
			if len(stats.Window) != 1+reclaims {
				t.Fatalf("effect window = %v, want %d slots: every attempt is booked, and each is answerable by a close",
					stats.Window, 1+reclaims)
			}
			if got, suppressed, _, _, _, _ := h.engine.sweep.metrics(); got != reclaims || suppressed != 0 {
				t.Fatalf("metrics: reclaims=%d suppressed=%d, want %d, 0 — a directOp leg is never paced", got, suppressed, reclaims)
			}
		})
	}
}

// --- The escalation substituted at PLAN time is dispatched as one ------------

// goalLegBlockedSpec is goalLegSpec with legA gated on a `ready` column no
// vector's row carries, and the target opted into the `unplannable` redirect.
// From that row no entry in the catalog is applicable, so the gap's own bounded
// regression dead-ends and planGap substitutes an Augur escalation for the plan
// it could not build — the escalation that reaches a caller wearing nothing but
// a dispatch class.
//
// Writing legA's own effect (aDone) makes the same catalog plannable again,
// because legB's precondition is exactly that: one fixture drives both the
// escalation and the leg boundary that ends it.
func goalLegBlockedSpec(targetID string) map[string]any {
	spec := goalLegSpec(targetID, actionDirectOp, false)
	spec["augur"] = map[string]any{"escalate": []any{escalateUnplannable}}
	gaps, _ := spec["gaps"].(map[string]any)
	gap, _ := gaps[goalLegGap].(map[string]any)
	actions, _ := gap["actions"].([]any)
	legA, _ := actions[0].(map[string]any)
	legA["pre"] = map[string]any{"present": "subject.data.ready"}
	return spec
}

// TestPlanGap_UnplannableEscalation_PreservesLegAndBooksNothing is the second
// route an escalation reaches a dispatch seam by, and the one that wears no
// label: planGap substitutes it for a plan it could not build, and hands the
// caller an actionRef ("directOp") indistinguishable from an ordinary leg.
//
// Dispatched as an ordinary episode it would charge the chain to that dispatch
// class, restart the budget under it and mint a mark with no displaced leg —
// after which legOf answers "directOp", the boundary can never be tested, and
// the gap alternates between its leg and the escalation with its exhaustion gate
// postponed on every hop. So the fire books neither tally nor window, and the
// mark carries the leg forward, exactly as the exhaustion escalation's does.
//
// The uncapped half is the fixture rule earning its keep rather than repeating
// the capped one, because the two reach the same answer down different reads. A
// capped gap's suppression verdict rests on the budget, so the gate reads the
// count document and hands it on. An uncapped gap's never does, so the gate
// reads nothing — and with no mark either, the pin would have no source at all
// unless the dispatch seam reads the document itself for exactly this shape. The
// leg is named either way, and both halves release at the boundary.
func TestPlanGap_UnplannableEscalation_PreservesLegAndBooksNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 5}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixPlanEscLeg"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			entityID := testNanoID(t)

			// A chain two attempts into legA, well inside a cap of five: this
			// vector is about the escalation's BOOKING and its pin, not about
			// exhaustion, so the gap must reach dispatchGap and not the
			// exhausted route.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			if err := h.engine.marks.recordEffectDispatch(ctx, targetID, goalLegGap, "legA"); err != nil {
				t.Fatalf("seed legA's pending confidence slot: %v", err)
			}

			row := goalLegRow(entityID, tc.budget, nil)
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 21, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("setup: operationType = %v, want the reasoning op — a negative over a dispatch that "+
					"never happened proves nothing", op["operationType"])
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 2 || doc.Leg != "legA" {
				t.Fatalf("count document = %+v, want {2 legA} untouched: an escalation is a dispatch ABOUT the "+
					"chain, and re-charging it to a dispatch class restarts the very budget it exists over", doc)
			}
			if _, _, present, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, actionDirectOp); err != nil || present {
				t.Fatalf("a confidence window opened at the escalation's dispatch class (present=%v err=%v) — "+
					"its slots are keyed on a catalog ref, and nothing can ever close this one", present, err)
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.Action != actionDirectOp {
				t.Fatalf("the escalation must own the gap's mark (rec=%+v found=%v err=%v)", rec, found, err)
			}
			if rec.EscalatedFrom != "legA" {
				t.Fatalf("escalatedFrom = %q, want %q — the leg the escalation stands over is recoverable from "+
					"nowhere else once it owns the mark", rec.EscalatedFrom, "legA")
			}
			h.requireNoOp(t)

			// legA's own effect lands: the boundary the pin exists to keep
			// testable.
			advanced := goalLegRow(entityID, tc.budget, map[string]any{"aDone": true})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, advanced, 22, 1)); dec != substrate.Ack {
				t.Fatalf("leg-boundary delivery: decision = %v, want Ack", dec)
			}
			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: the escalation kept the pin, so the finished leg releases "+
					"and the chain advances", op["operationType"])
			}
			next, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || next.Action != "legB" || next.EscalatedFrom != "" {
				t.Fatalf("advanced mark = %+v (found=%v err=%v), want it pinning legB with no displaced leg — "+
					"the escalation is over", next, found, err)
			}
			after := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if after.Count != 1 || after.Leg != "legB" {
				t.Fatalf("count document = %+v, want a fresh budget charged to legB", after)
			}
			stats, _, ok, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, "legA")
			if err != nil || !ok || len(stats.Window) != 1 || !stats.Window[0] {
				t.Fatalf("legA's confidence window = %+v (present=%v err=%v), want one CLOSED slot", stats, ok, err)
			}
			h.requireNoOp(t)
		})
	}
}

// TestSweep_ReclaimOfEscalationPreservesDisplacedLeg is the same rule on the
// seam that rewrites the mark whole rather than minting one. A reclaim replaces
// every field, so the displaced leg survives only by being threaded back
// through — and the reclaim of an escalation whose pin is its own dispatch class
// re-derives that escalation through planGap, which means it also decides the
// booking.
//
// Both halves run the same way: the reclaim reads the count document for its own
// pacing whether or not a cap exists, so the leg is nameable either way.
func TestSweep_ReclaimOfEscalationPreservesDisplacedLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
	}{{"capped", 5}, {"uncapped", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixReclaimEscLeg"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, goalLegGap)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			putStateValue(t, ctx, h.conn, key, escalationMark(targetID, entityID, "legA", pastLease()))
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, nil))

			h.pass(ctx)

			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("setup: operationType = %v, want the reasoning op re-derived by the reclaim", op["operationType"])
			}
			rec, _ := h.readMark(t, ctx, key)
			if rec.Action != actionDirectOp || rec.EscalatedFrom != "legA" {
				t.Fatalf("re-armed mark = %+v, want the escalation still standing over legA", rec)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 2 || doc.Leg != "legA" {
				t.Fatalf("count document = %+v, want {2 legA} untouched: a re-derived escalation books no more "+
					"than the first one did", doc)
			}
			if _, _, present, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, actionDirectOp); err != nil || present {
				t.Fatalf("the reclaim opened a confidence window at the escalation's dispatch class "+
					"(present=%v err=%v) — nothing can ever close it", present, err)
			}

			// The boundary, tested off the re-armed mark alone.
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, map[string]any{"aDone": true}))
			h.reexpireMark(t, ctx, key)
			h.pass(ctx)

			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: the re-armed escalation carried the leg, so the finished "+
					"leg releases and the chain advances", op["operationType"])
			}
			h.requireNoOp(t)
		})
	}
}

// --- The markless release's own concurrency control -------------------------

// TestFireEpisode_StaleReArm_TakesTheEscalationsLegFromItsCaller pins which
// source the mark-rewriting re-arm takes its displaced leg from, on the one seam
// that has two candidates for it.
//
// A re-arm replaces every field of the mark, so the displaced leg survives only
// by being written back — and there are two answers to write. For an ordinary
// episode it is the value already on the mark: the re-arm changed the lease, not
// what the episode stands over. For an ESCALATION it is the caller's, because
// the plan that produced this dispatch is what decided the leg, and a mark that
// records none (an old-shape escalation, or the gap's own leg mark being
// displaced now) would otherwise take the pin dark for as long as the escalation
// stood — legOf would answer nothing and the boundary could never be tested.
//
// The ordinary row is the control: the same call one flag apart must still keep
// the mark's own value, so the rule is "the escalation's leg comes from its
// caller", not "the caller always wins".
func TestFireEpisode_StaleReArm_TakesTheEscalationsLegFromItsCaller(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name       string
		escalation bool
		onTheMark  string
		fromCaller string
		want       string
	}{
		{"escalationTakesTheCallers", true, "", "legA", "legA"},
		{"ordinaryReArmKeepsTheMarks", false, "legA", "", "legA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)

			const targetID = "fixStaleReArmLeg"
			target := &Target{
				TargetID: targetID,
				Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
			}
			h.seedTarget(target)
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, "missing_x")

			// The external gap's expired episode: inflight_x false and a lapsed
			// lease are what make the mark stale, which is the arm that rewrites
			// it in place instead of minting a fresh one.
			expired := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, pastLease())
			expired.EscalatedFrom = tc.onTheMark
			staleRev := h.putMark(t, ctx, key, expired)

			row := map[string]any{
				"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "inflight_x": false,
			}
			pl, _, _, dec := h.engine.planGap(ctx, target, targetID, entityID, "missing_x", target.Gaps["missing_x"], row, 1, "")
			if pl == nil {
				t.Fatalf("setup: planGap must produce a plan, got dec=%v", dec)
			}

			got, _ := h.engine.fireEpisode(ctx, targetID, entityID, "vtx.leaseApp."+entityID, "missing_x", actionDirectOp,
				pl, &expired, staleRev, true, true, tc.escalation, true, tc.fromCaller)
			if got != substrate.Ack {
				t.Fatalf("stale re-arm decision = %v, want Ack", got)
			}
			drainOps(t, h.ops, 1)

			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x")
			if err != nil || !found {
				t.Fatalf("read the re-armed mark: found=%v err=%v", found, err)
			}
			if rec.EscalatedFrom != tc.want {
				t.Fatalf("escalatedFrom on the re-armed mark = %q, want %q", rec.EscalatedFrom, tc.want)
			}
		})
	}
}

// TestReleaseCompletedLeg_MarklessWithAMarkPresent_DoesNothing pins what stands
// in for a revision condition on the leg that has none.
//
// The markless caller reaches a gap through its budget and holds no mark
// revision, so its three writes — the close credit, the budget delete, the latch
// clear — are conditioned on the mark's ABSENCE instead, re-read at the last
// moment the release can still be abandoned. A gap with no mark is exactly the
// gap a lane-1 delivery may CAS-create a fresh episode for at any instant, and
// without the re-read this leg would delete that new episode's budget and credit
// the old leg's close against it.
//
// The positive half is what makes the negative mean anything: the identical call
// over a genuinely markless gap releases and writes all three.
func TestReleaseCompletedLeg_MarklessWithAMarkPresent_DoesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name        string
		markPresent bool
		wantRelease bool
	}{{"markAppearedInTheWindow", true, false}, {"genuinelyMarkless", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixMarklessRelease"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
			target, ok := h.engine.source.target(targetID)
			if !ok {
				t.Fatal("setup: the target must be registered")
			}
			ga := target.Gaps[goalLegGap]
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			if err := h.engine.marks.recordEffectDispatch(ctx, targetID, goalLegGap, "legA"); err != nil {
				t.Fatalf("seed legA's pending confidence slot: %v", err)
			}
			issueKey := issueKeyGapEntity(targetID, entityID, goalLegGap)
			h.engine.issues.set(issueKey, "warning", "GapBudgetExhausted", "spent on an earlier pass")
			if tc.markPresent {
				// The fresh episode a lane-1 delivery CAS-created between this
				// caller's budget read and its writes.
				putStateValue(t, ctx, h.conn, markKey(targetID, entityID, goalLegGap),
					fixtureMark(targetID, entityID, goalLegGap, "legA", futureLease()))
			}

			// The markless branch conditions its writes on the count document's
			// revision, so the caller reads it exactly as the three production
			// sites do — from the same read that decided the gap was exhausted.
			_, countRev, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, goalLegGap)
			if err != nil {
				t.Fatalf("read the seeded count's revision: %v", err)
			}
			row := goalLegRow(entityID, 2, map[string]any{"aDone": true})
			released := h.engine.releaseCompletedLeg(ctx, targetID, entityID, goalLegGap, ga, "legA", row, 0, countRev)
			if released != tc.wantRelease {
				t.Fatalf("markless release = %v, want %v", released, tc.wantRelease)
			}

			// getDispatchCount, not readCount: a clean release DELETES the
			// document, and absence reads as the zero one.
			doc, _, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, goalLegGap)
			if err != nil {
				t.Fatalf("read the count document back: %v", err)
			}
			stats, _, statsFound, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, "legA")
			if err != nil || !statsFound {
				t.Fatalf("read legA's confidence window: found=%v err=%v", statsFound, err)
			}
			_, standing := issueAt(h.engine.issues, issueKey)
			if !tc.wantRelease {
				if doc.Count != 2 || doc.Leg != "legA" {
					t.Fatalf("count document = %+v, want it untouched: the budget now belongs to the episode "+
						"holding the mark, not to this caller's leg", doc)
				}
				if len(stats.Window) != 1 || stats.Window[0] {
					t.Fatalf("confidence window = %+v, want its one slot still pending: a close credited here "+
						"would answer the new episode's dispatch", stats)
				}
				if !standing {
					t.Fatalf("the latch must stand: nothing was released (issues: %+v)", h.engine.issues.snapshot())
				}
				return
			}
			if doc.Count != 0 || doc.Leg != "" {
				t.Fatalf("count document = %+v, want the whole budget gone at a clean leg boundary", doc)
			}
			if len(stats.Window) != 1 || !stats.Window[0] {
				t.Fatalf("confidence window = %+v, want its slot CLOSED — the release is the leg's close credit", stats)
			}
			if standing {
				t.Fatalf("the latch must retire with the budget it described (issues: %+v)", h.engine.issues.snapshot())
			}
		})
	}
}

// --- One resolution decides both the refusal and the dispatch ---------------

// seedCandidateWindows writes the two candidates' `__effect` confidence
// windows, which is what rankCandidates ranks on: a window of closed slots
// scores 1, one of pending slots scores 0.
func seedCandidateWindows(t *testing.T, ctx context.Context, h *sweepHarness, targetID, gap, winner, loser string) {
	t.Helper()
	if _, err := h.conn.KVCreate(ctx, "weaver-state", effectKey(targetID, gap, winner),
		mustMarshalEffectStats(t, effectStats{Window: []bool{true, true}})); err != nil {
		t.Fatalf("seed %q's confidence window: %v", winner, err)
	}
	if _, err := h.conn.KVCreate(ctx, "weaver-state", effectKey(targetID, gap, loser),
		mustMarshalEffectStats(t, effectStats{Window: []bool{false, false}})); err != nil {
		t.Fatalf("seed %q's confidence window: %v", loser, err)
	}
}

// TestReleaseCompletedLeg_TwoMarklessReleases_OnlyOneCredits pins the mutual
// exclusion a markless release has, and where it comes from.
//
// Two derivations genuinely race for the same boundary — a lane-1 delivery and
// the sweep's count leg both reach a gap with no mark, read the same count
// document and see the same effects holding — and the release is not idempotent:
// recordEffectClose flips ONE pending slot per call, so a second credit answers a
// dispatch that never closed and inflates the leg's confidence. The mark's
// absence cannot serialize them, being a state both read identically. The count
// DOCUMENT can: its revision-conditioned delete has exactly one winner, and the
// winner is the caller entitled to run the rest.
//
// Two pending slots, because one would hide a double credit behind
// recordEffectClose finding nothing left to flip.
func TestReleaseCompletedLeg_TwoMarklessReleases_OnlyOneCredits(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixMarklessRace"
	registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
	target, ok := h.engine.source.target(targetID)
	if !ok {
		t.Fatal("setup: the target must be registered")
	}
	ga := target.Gaps[goalLegGap]
	entityID := testNanoID(t)

	putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
		dispatchCount{Count: 2, Leg: "legA"})
	for i := 0; i < 2; i++ {
		if err := h.engine.marks.recordEffectDispatch(ctx, targetID, goalLegGap, "legA"); err != nil {
			t.Fatalf("seed legA's pending confidence slot #%d: %v", i, err)
		}
	}
	issueKey := issueKeyGapEntity(targetID, entityID, goalLegGap)
	h.engine.issues.set(issueKey, "warning", "GapBudgetExhausted", "spent on an earlier pass")

	// The one read both derivations made before either wrote — the racing pair
	// is exactly two callers holding the same revision.
	_, countRev, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, goalLegGap)
	if err != nil || countRev == 0 {
		t.Fatalf("read the seeded count's revision: rev=%d err=%v", countRev, err)
	}

	row := goalLegRow(entityID, 2, map[string]any{"aDone": true})
	if released := h.engine.releaseCompletedLeg(ctx, targetID, entityID, goalLegGap, ga, "legA", row, 0, countRev); !released {
		t.Fatal("the first markless release must win and credit the leg")
	}
	if released := h.engine.releaseCompletedLeg(ctx, targetID, entityID, goalLegGap, ga, "legA", row, 0, countRev); released {
		t.Fatal("the second markless release over the same revision must lose: the boundary is already credited")
	}

	stats, _, statsFound, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, "legA")
	if err != nil || !statsFound {
		t.Fatalf("read legA's confidence window: found=%v err=%v", statsFound, err)
	}
	closed := 0
	for _, slot := range stats.Window {
		if slot {
			closed++
		}
	}
	if len(stats.Window) != 2 || closed != 1 {
		t.Fatalf("confidence window = %+v, want exactly one of its two slots closed: one leg finished once", stats)
	}
	doc, rev, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, goalLegGap)
	if err != nil || rev != 0 || doc != (dispatchCount{}) {
		t.Fatalf("count document = %+v at revision %d (err=%v), want it gone: the winner deleted it and the "+
			"loser wrote nothing", doc, rev, err)
	}
	if _, standing := issueAt(h.engine.issues, issueKey); standing {
		t.Fatalf("the gap's latch must be cleared by the release (issues: %+v)", h.engine.issues.snapshot())
	}
}

// TestSweep_CountLegReArm_ActsOnTheCandidateItClassified pins that the arm's
// collapse-only refusal and its dispatch are taken over the SAME candidate.
//
// The arm must classify before it may plan — planning draws an admission token
// and clears the gap's standing issues on the strength of a dispatch that may
// not happen — so it resolves the leg first and then plans. A candidates gap
// ranks on the `__effect` confidence windows, which are live: two independent
// resolutions in one pass can answer differently, and the gap would then
// dispatch a candidate the refusal was never applied to. The plan is therefore
// PINNED to the ref the classification was taken over.
//
// Both directions are here because either alone is satisfiable by a rule that
// always answers the same way: when the collapse-only candidate ranks first
// nothing may fire at all, and when the dispatchable one does, what fires must
// be that one.
func TestSweep_CountLegReArm_ActsOnTheCandidateItClassified(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name        string
		winner      string
		loser       string
		wantOp      string
		wantRefusal bool
	}{
		{"collapseOnlyCandidateRanksFirst", actionAssignTask, actionDirectOp, "", true},
		{"dispatchableCandidateRanksFirst", actionDirectOp, actionAssignTask, "DoX", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixArmCandidate"
			const gap = "missing_c"
			h.seedTarget(&Target{
				TargetID: targetID,
				Mode:     targetModePlanned,
				Gaps: map[string]GapAction{gap: {Candidates: []GapCandidate{
					{Action: actionDirectOp, Operation: "DoX"},
					{Action: actionAssignTask, Operation: "SignX", Assignee: "row.applicant", Target: "row.entityKey"},
				}}},
			})
			h.engine.source.mu.Lock()
			h.engine.source.opMetaByType["SignX"] = "vtx.meta." + testNanoID(t)
			h.engine.source.mu.Unlock()
			seedCandidateWindows(t, ctx, h, targetID, gap, tc.winner, tc.loser)

			entityID := testNanoID(t)
			// The one state the arm fires on: a budget an operator re-armed to
			// zero, and no mark.
			h.seedReArmedCount(t, ctx, targetID, entityID, gap)
			h.putRow(t, ctx, targetID, entityID, map[string]any{
				"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
				"applicant": "vtx.identity." + entityID, "maxretries_c": 5,
			})

			h.pass(ctx)

			if tc.wantRefusal {
				h.requireNoOp(t)
				if h.markExists(t, ctx, markKey(targetID, entityID, gap)) {
					t.Fatal("the ranked candidate assigns a human task, whose artifact may still be open: the " +
						"arm must leave the gap markless rather than mint a second one")
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != tc.wantOp {
				t.Fatalf("operationType = %v, want %q — the arm must dispatch the candidate its refusal was "+
					"taken over, not a second rank's pick", op["operationType"], tc.wantOp)
			}
			rec, _ := h.readMark(t, ctx, markKey(targetID, entityID, gap))
			if rec.Action != tc.winner {
				t.Fatalf("mark action = %q, want %q: the mark records the ref the plan was pinned to", rec.Action, tc.winner)
			}
			h.requireNoOp(t)
		})
	}
}

// --- The un-park verb's declines, one vector each ---------------------------

// TestReArmDeclines_RowUnreadable_IsTransient is the verb's one TRANSIENT
// refusal. Every other decline is a property of the gap's class and never lifts;
// this one is a KV read that failed, and handing the resolver a row it never got
// would produce "no derivable plan" — a permanent-sounding verdict about the
// playbook, from a fault that is gone on the next attempt. It refuses under its
// own reason, and the reason says to re-run.
func TestReArmDeclines_RowUnreadable_IsTransient(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)

	const targetID = "fixRowUnreadable"
	const budget = 2
	registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
	entityID := testNanoID(t)
	h.seedCount(t, ctx, targetID, entityID, goalLegGap, budget)
	if _, err := h.conn.KVPut(ctx, h.engine.cfg.WeaverTargetsBucket, targetID+"."+entityID,
		[]byte(`{"entityKey": `)); err != nil {
		t.Fatalf("put an unparseable row body: %v", err)
	}

	_, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, goalLegGap)
	if err == nil {
		t.Fatal("a row the verb could not read must be refused: nothing can say what the arm would fire")
	}
	if !strings.Contains(err.Error(), "could not be read") || !strings.Contains(err.Error(), "re-run") {
		t.Fatalf("error = %q, want it to name the read failure and the remedy, not a verdict about the gap", err)
	}
	if got := h.countValue(t, ctx, targetID, entityID, goalLegGap); got != budget {
		t.Fatalf("refused reset left the budget at %d, want %d (nothing written)", got, budget)
	}
}

// TestReArmDeclines_PlanResolvesNoAction is the decline the resolver itself
// produces: a goal gap whose catalog dead-ends against this row. The arm would
// fire nothing for it on any pass, so a zero written here would be a false
// success — a re-armed budget nothing will ever act on, over a gap still parked
// and still holding the standing issue that says so.
func TestReArmDeclines_PlanResolvesNoAction(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixNoPlanDecline"
	const budget = 2
	// The blocked spec WITHOUT its augur block: nothing substitutes for the
	// plan, so the resolver's dead-end is the answer the verb sees.
	spec := goalLegBlockedSpec(targetID)
	delete(spec, "augur")
	registerSpec(t, h.engine.source, spec)
	entityID := testNanoID(t)
	h.seedCount(t, ctx, targetID, entityID, goalLegGap, budget)
	h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, budget, nil))

	_, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, goalLegGap)
	if err == nil {
		t.Fatal("a gap whose plan resolves nothing for this row must be refused: the arm would fire nothing")
	}
	if !strings.Contains(err.Error(), "resolves no action for this row") {
		t.Fatalf("error = %q, want it to say the plan resolves no action for this row", err)
	}
	if got := h.countValue(t, ctx, targetID, entityID, goalLegGap); got != budget {
		t.Fatalf("refused reset left the budget at %d, want %d (nothing written)", got, budget)
	}

	// The refusal's reason, executed: at the budget an accepted reset would have
	// written, the arm still dispatches nothing.
	h.seedReArmedCount(t, ctx, targetID, entityID, goalLegGap)
	h.pass(ctx)
	h.requireNoOp(t)
	if h.markExists(t, ctx, markKey(targetID, entityID, goalLegGap)) {
		t.Fatal("a gap whose plan dead-ends must stay markless — which is why the verb refuses it")
	}
}

// --- What a booking may create, and what a fire may record ------------------

// TestIncrementDispatchCount_ReArmWithNoDocument_CreatesNothing pins the one
// value the count key may never hold by accident.
//
// A count key that EXISTS and reads 0 is the sweep's count leg's whole evidence
// for "an operator re-armed this gap" — the one state that arm fires a fresh
// markless episode on — and it holds only because a booking creates at 1 and
// never writes below it. A re-arm of an episode with no document at all would
// break that: it would persist {count:0} and hand the arm a dispatch nobody
// asked for. There is nothing to re-arm there anyway, so it writes nothing.
//
// The positive control is the same call stating that it mounts an attempt: that
// one does create the document, at 1.
func TestIncrementDispatchCount_ReArmWithNoDocument_CreatesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)

	const targetID, gap = "t1", "missing_x"
	entityID := "entityBBBBBBBBBBBBBB"

	doc, err := m.incrementDispatchCount(ctx, targetID, entityID, gap, actionAssignTask, false, true, false)
	if err != nil {
		t.Fatalf("re-arm with no document: %v", err)
	}
	if doc != (dispatchCount{}) {
		t.Fatalf("re-arm with no document returned %+v, want the zero document — nothing was booked", doc)
	}
	if _, err := m.conn.KVGet(ctx, m.bucket, countKey(targetID, entityID, gap)); !errors.Is(err, substrate.ErrKeyNotFound) {
		t.Fatalf("the count key exists after a re-arm of nothing (err=%v) — a key reading 0 is the count leg's "+
			"evidence of an operator's un-park, and this one would fabricate it", err)
	}

	created, err := m.incrementDispatchCount(ctx, targetID, entityID, gap, actionAssignTask, true, false, false)
	if err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if created.Count != 1 || created.Leg != actionAssignTask {
		t.Fatalf("first attempt booked %+v, want {count 1, leg assignTask}: a dispatch that mounts something "+
			"is exactly what creates the document", created)
	}
}

// TestEscalateExhaustedGap_UnreadableMark_AlertsAndRecordsNoFire covers the two
// things that must still hold when the gap's mark cannot be read at all.
//
// The mark is read above the augur-policy check, because the leg boundary is
// tested there — but a read failure is a KV blip, not a verdict. Naking on it
// would take the loud stop OFF THE AIR for as long as the blip lasted, and the
// un-escalated GapBudgetExhausted is the only record saying a row has stopped
// remediating. So the pass continues without the mark: the boundary is simply
// untestable, and the escalation's own CAS-create is left to discover whether a
// mark is there after all — which, when it loses, means another instance owns
// the dispatch, and this pass records nothing about it.
//
// The uncapped half is the fixture rule's answer for an arm only reachable past
// exhaustion: with no maxretries_<g> the gap never exhausts, so the delivery
// never reaches this function at all, and the unreadable mark is the ordinary
// dispatch leg's problem instead.
func TestEscalateExhaustedGap_UnreadableMark_AlertsAndRecordsNoFire(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name     string
		budget   int
		escalate bool
		wantDec  substrate.Decision
	}{
		{"cappedNoAugurPolicy", 2, false, substrate.Ack},
		{"cappedWithAugurPolicy", 2, true, substrate.Ack},
		{"uncapped", 0, true, substrate.NakWithDelay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixUnreadableMark"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, tc.escalate))
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap),
				dispatchCount{Count: 2, Leg: "legA"})
			// A mark body nothing can parse: the read fails, and the key is
			// still there for the escalation's CAS-create to lose against.
			if _, err := h.conn.KVPut(ctx, "weaver-state", markKey(targetID, entityID, goalLegGap),
				[]byte(`{"targetId": `)); err != nil {
				t.Fatalf("put an unparseable mark body: %v", err)
			}

			row := goalLegRow(entityID, tc.budget, nil)
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 18, 1)); dec != tc.wantDec {
				t.Fatalf("decision = %v, want %v", dec, tc.wantDec)
			}
			h.requireNoOp(t)

			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 2 || doc.Leg != "legA" {
				t.Fatalf("count document = %+v, want {2 legA} untouched", doc)
			}
			if doc.EscalatedAt != "" || doc.Reclaims != 0 {
				t.Fatalf("count document = %+v, want no fire recorded: the escalation's CAS-create LOST, so the "+
					"dispatch — and the pacing stamp for it — belong to whoever won", doc)
			}
			standing := hasIssueCode(h.engine.issues.snapshot(), "GapBudgetExhausted")
			wantStanding := tc.budget > 0 && !tc.escalate
			if standing != wantStanding {
				t.Fatalf("GapBudgetExhausted standing = %v, want %v: a gap that has stopped remediating is said "+
					"out loud whether or not its mark could be read (issues: %+v)",
					standing, wantStanding, h.engine.issues.snapshot())
			}
		})
	}
}
