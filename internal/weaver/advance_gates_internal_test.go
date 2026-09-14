package weaver

import (
	"context"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- the advance's own "cannot act" gates -----------------------------------
//
// A release is a RETIRE and stands above every gate; the ADVANCE that follows
// one is a dispatch and stands below them. advanceReleasedLeg holds both gates
// itself, so these vectors drive the release seams that sit ABOVE them — the
// sweep's mark leg, whose two release arms precede the reclaim's own violating
// and suppression gates — and pin what the advance does from there.
//
// Every vector is a PAIR by the column it turns on, because the negative alone
// cannot tell a withheld advance from a release that never fired: the positive
// half is what proves the seam still reaches the dispatch at all.

// TestReclaim_GoalLegAdvanceHonoursTheCannotActGates: the goal shape
// (releaseCompletedLeg's arm). An expired mark pinning legA over a row where
// legA's effect holds is a leg BOUNDARY — the release stands whatever else the
// row says — but the next leg's op is a real dispatch at a real entity, so it
// fires only on a row lane 1 would itself have dispatched: violating, and with
// no call in flight.
func TestReclaim_GoalLegAdvanceHonoursTheCannotActGates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		violating bool
		inflight  any
		wantAdv   bool
	}{
		{"violatingAndFree", true, false, true},
		{"notViolating", false, false, false},
		{"callInFlight", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixAdvanceGatesGoal"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, false))
			entityID := testNanoID(t)

			key := markKey(targetID, entityID, goalLegGap)
			h.putMark(t, ctx, key, fixtureMark(targetID, entityID, goalLegGap, "legA", pastLease()))
			// aDone: legA's declared effect holds, which is the boundary. The gap
			// itself stays open (bDone is still missing), so the mark leg reaches
			// the reclaim rather than the level clear.
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, 0, map[string]any{
				"aDone": true, "violating": tc.violating, "inflight_g": tc.inflight,
			}))

			h.pass(ctx)

			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil {
				t.Fatalf("read the gap's mark back: %v", err)
			}
			if !tc.wantAdv {
				// What the ADVANCE would have created, not what the release
				// removed: the fresh mark pinning the next leg, and the next
				// leg's op.
				h.requireNoOp(t)
				if found {
					t.Fatalf("mark = %+v, want none: the release cleared legA's and the withheld advance mints no leg", rec)
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: a violating row with no call in flight advances to the next leg",
					op["operationType"])
			}
			if !found || rec.Action != "legB" {
				t.Fatalf("advanced mark = %+v (found=%v), want a fresh mark pinning legB", rec, found)
			}
			h.requireNoOp(t)
		})
	}
}

// TestReclaim_ProposalLegAdvanceHonoursTheCannotActGates: the Augur PLAN shape
// (releaseAdvancedProposalLeg's arm), which the same gates must answer the same
// way. A gap's meaning differs by shape — here the leg lives on the row's own
// dispatchLeg counter rather than in a catalog — so the goal vector above does
// not speak for it.
func TestReclaim_ProposalLegAdvanceHonoursTheCannotActGates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		handle    string
		violating bool
		inflight  any
		wantAdv   bool
	}{
		{"violatingAndFree", "BBadvGateAHJKMNPQRST", true, false, true},
		{"notViolating", "BBadvGateBHJKMNPQRST", false, false, false},
		{"callInFlight", "BBadvGateCHJKMNPQRST", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)

			const targetID = "augurDispatch"
			h.seedTarget(augurPlanTarget(targetID))

			key := markKey(targetID, tc.handle, "missing_dispatch")
			rec := fixtureMark(targetID, tc.handle, "missing_dispatch", actionProposedOp, pastLease())
			rec.EntityKey = "vtx.augurproposal." + tc.handle
			h.putMark(t, ctx, key, rec)
			// The mark stands over leg 0 while the row's counter reads 1: the flip
			// already recorded that leg, so this is the plan's leg boundary.
			row := twoLegRow(tc.handle, 1)
			row["violating"] = tc.violating
			row["inflight_dispatch"] = tc.inflight
			h.putRow(t, ctx, targetID, tc.handle, row)

			h.pass(ctx)

			fresh, _, found, err := h.engine.marks.get(ctx, targetID, tc.handle, "missing_dispatch")
			if err != nil {
				t.Fatalf("read the gap's mark back: %v", err)
			}
			if !tc.wantAdv {
				h.requireNoOp(t)
				if found {
					t.Fatalf("mark = %+v, want none: the release cleared leg 0's and the withheld advance mints no leg", fresh)
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != "RecordLeaseDecision" {
				t.Fatalf("operationType = %v, want the second leg's remediation", op["operationType"])
			}
			if flip := h.nextOp(t); flip["operationType"] != opRecordProposalDispatch {
				t.Fatalf("operationType = %v, want the advanced leg's flip", flip["operationType"])
			}
			if !found || fresh.ProposalLeg != 1 {
				t.Fatalf("advanced mark = %+v (found=%v), want a fresh mark recording leg 1", fresh, found)
			}
			h.requireNoOp(t)
		})
	}
}

// TestAdvanceReleasedLeg_GatesAreANoOpAtTheSeamsThatAlreadyPassedThem is the
// other half of moving the gates into the advance: the two release seams that
// reach it from BELOW their own violating and suppression gates must be
// unchanged by the re-ask.
//
// Each subtest is chosen so a gate asked the wrong way would withhold:
//
//   - the count leg's escalation-release arm reaches the advance having already
//     cleared the suppression gate over the REAL count, and the re-ask says the
//     same thing at zero;
//   - escalateExhaustedGap reaches it from the exhausted verdict itself, where
//     the real count has by definition REACHED the cap. Asked with that count
//     the budget term would read the advance as suppressed and the chain would
//     stall on a leg that has finished; asked at zero — the document the release
//     just deleted — it is the inflight term alone that decides, and no call is
//     in flight.
func TestAdvanceReleasedLeg_GatesAreANoOpAtTheSeamsThatAlreadyPassedThem(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
		count  dispatchCount
	}{
		{"countLegEscalationRelease", 0, dispatchCount{Count: 2, Leg: "legA",
			EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour))}},
		{"exhaustedGapEscalation", 2, dispatchCount{Count: 2, Leg: "legA"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixAdvanceGatesNoOp"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)

			// A markless gap: the count leg is the only leg that visits it, and
			// the document it carries is the whole evidence of the chain.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), tc.count)
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, map[string]any{"aDone": true}))

			h.pass(ctx)

			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: this seam cleared its own gates before releasing, so the "+
					"advance must still dispatch the next leg", op["operationType"])
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.Action != "legB" {
				t.Fatalf("advanced mark = %+v (found=%v err=%v), want a fresh mark pinning legB", rec, found, err)
			}
			h.requireNoOp(t)
		})
	}
}
