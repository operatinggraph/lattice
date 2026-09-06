package weaver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// The two `unplannable` doors into the Augur reasoning tier — a gap column with
// no playbook entry, and a goal gap from whose current row state no plan derives
// — and the episode seam all three doors share.
//
// Every vector runs capped and uncapped where a maxretries_<g> is available to
// supply, because the cap decides which read hands the seam its pacing document:
// a capped gap's suppression verdict rests on the budget, so the gate reads the
// count and passes it on; an uncapped gap's never does, so the dispatch leg must
// read it itself or the door escalates with nothing to pace on.

// noEntryTarget registers a target whose playbook names missing_a only and whose
// augur policy escalates `unplannable` — the no-playbook-entry door's fixture.
func noEntryTarget(t *testing.T, h *handlerHarness, targetID string) {
	t.Helper()
	spec := targetSpecFixture(targetID)
	spec["augur"] = map[string]any{"escalate": []any{escalateUnplannable}}
	registerSpec(t, h.engine.source, spec)
}

// noEntryRow is a violating row holding a column the playbook does not name.
func noEntryRow(entityID string, budget int) map[string]any {
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, noEntryGap: true,
	}
	if budget > 0 {
		row["maxretries_unknown"] = budget
	}
	return row
}

// noEntryGap is the column no playbook entry names.
const noEntryGap = "missing_unknown"

// TestEscalateGap_NoEntryDoorBooksNothing is the no-playbook-entry door taking
// the episode model the exhausted door already had.
//
// Ridden down the ordinary dispatch path — which is where a substituted
// GapAction put it — the escalation was booked as an ATTEMPT of a gap the
// playbook does not even name: a tally nothing bounds (the cap term is consulted
// for a literal directOp action, and this column's action is the zero value) and
// an `__effect` window slot per fire, of which the gap's own close credits at
// most one. So it books neither, and what it writes instead is the escalation's
// own pacing document — the only handle a re-fire of a dead reasoning claim has.
//
// The mark records the class, so no later reader has to infer "this is an
// escalation" from an action that fails to resolve — which is also what a
// removed catalog ref looks like.
func TestEscalateGap_NoEntryDoorBooksNothing(t *testing.T) {
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
			h := newHandlerHarness(t, ctx)

			const targetID = "fixDoorTwoBooks"
			noEntryTarget(t, h, targetID)
			entityID := testNanoID(t)
			row := noEntryRow(entityID, tc.budget)

			// The alert a pass before the policy existed raised, and the stamp
			// that says whether the clear below really ran: a re-raise at the
			// same key would leave membership true and `since` untouched.
			h.engine.issues.set(issueKeyGapConfig(targetID, noEntryGap), "warning", "GapWithoutPlaybook", "raised before the policy")
			seeded, _ := issueAt(h.engine.issues, issueKeyGapConfig(targetID, noEntryGap))

			// Metadata-less delivery first: the expectedRevision guard is above
			// the escalation, so nothing is dispatched and nothing is written.
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 0, 1)); dec != substrate.NakWithDelay {
				t.Fatalf("a delivery with no JetStream metadata must defer, got %v", dec)
			}
			h.requireNoOp(t)
			if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, noEntryGap); err != nil || found {
				t.Fatalf("the metadata defer must mint no mark (found=%v err=%v)", found, err)
			}

			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 9, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op: a dead-end the policy covers is escalated, "+
					"not alerted", op["operationType"])
			}

			doc := readCount(t, ctx, h.conn, targetID, entityID, noEntryGap)
			if doc.Count != 0 || doc.Leg != "" {
				t.Fatalf("count document = %+v, want no attempt and no leg charged to it: an escalation is a "+
					"dispatch ABOUT the gap, and this gap has no chain to charge at all", doc)
			}
			if doc.Reclaims != 1 || doc.EscalatedAt == "" {
				t.Fatalf("count document = %+v, want {reclaims 1, escalatedAt now}: the pacing pair is the whole "+
					"reason the document exists for this door", doc)
			}
			if _, _, present, err := readEffectStats(ctx, h.engine.marks, targetID, noEntryGap, actionDirectOp); err != nil || present {
				t.Fatalf("a confidence window opened at the escalation's dispatch class (present=%v err=%v) — "+
					"its slots are keyed on a catalog ref, and this column has no catalog", present, err)
			}

			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, noEntryGap)
			if err != nil || !found {
				t.Fatalf("the escalation must own the gap's mark (found=%v err=%v)", found, err)
			}
			if rec.Action != actionDirectOp || rec.Escalation != escalateUnplannable {
				t.Fatalf("mark = %+v, want the reasoning dispatch class declaring the %q trigger", rec, escalateUnplannable)
			}
			if rec.EscalatedFrom != "" {
				t.Fatalf("escalatedFrom = %q, want empty: a column with no playbook entry has no legs to displace",
					rec.EscalatedFrom)
			}

			if is, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, noEntryGap)); ok {
				t.Fatalf("the policy covers the dead-end, so GapWithoutPlaybook must retire; still standing as "+
					"%+v (seeded at %s)", is, seeded.Since)
			}
			is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, noEntryGap))
			if !ok || is.Code != codeGapEscalatedToAugur || is.Severity != "warning" {
				t.Fatalf("issue at the gap's entity key = %+v (present=%v), want a warning %s", is, ok, codeGapEscalatedToAugur)
			}
			if want := "has no playbook entry"; !strings.Contains(is.Message, want) {
				t.Fatalf("escalation record = %q, want it naming the dead end (%q): the key says which gap, "+
					"only the message says which door", is.Message, want)
			}
			h.requireNoOp(t)
		})
	}
}

// TestEscalateGap_NoPlanDoorBooksNothing is the same for the goal gap from whose
// current state no chain of its own catalog actions reaches the goal. It books
// nothing either — and it is reached with NO count document at all, which is why
// the seam's booking creates one: without it the door has nothing to pace a
// re-fire on, and every subsequent derivation would fire a fresh reasoning
// claim the Processor rejects create-only.
func TestEscalateGap_NoPlanDoorBooksNothing(t *testing.T) {
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

			const targetID = "fixDoorThreeBooks"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			entityID := testNanoID(t)
			row := goalLegRow(entityID, tc.budget, nil)

			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 9, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op", op["operationType"])
			}

			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 0 || doc.Leg != "" || doc.Reclaims != 1 || doc.EscalatedAt == "" {
				t.Fatalf("count document = %+v, want {count 0, no leg, reclaims 1, escalatedAt now}: a gap that "+
					"has never had a derivable plan has mounted no attempt, and the document exists only to pace "+
					"the re-fire", doc)
			}
			if _, _, present, err := readEffectStats(ctx, h.engine.marks, targetID, goalLegGap, actionDirectOp); err != nil || present {
				t.Fatalf("a confidence window opened at the escalation's dispatch class (present=%v err=%v)", present, err)
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.Escalation != escalateUnplannable {
				t.Fatalf("mark = %+v (found=%v err=%v), want it declaring the %q trigger", rec, found, err, escalateUnplannable)
			}
			is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap))
			if !ok || is.Code != codeGapEscalatedToAugur {
				t.Fatalf("issue at the gap's entity key = %+v (present=%v), want %s", is, ok, codeGapEscalatedToAugur)
			}
			if want := "has no derivable plan"; !strings.Contains(is.Message, want) {
				t.Fatalf("escalation record = %q, want it naming the dead end (%q)", is.Message, want)
			}
			h.requireNoOp(t)
		})
	}
}

// TestEscalateGap_UnplannableRefireIsPaced pins the cadence the `unplannable`
// doors inherit from the exhausted one. The re-fire arm exists for one case — a
// reasoning claim that was never minted, which nothing else re-derives — and
// past the first commit every re-fire is an op the Processor rejects
// create-only, so it waits the same exponential every other re-arm of an open
// episode waits.
//
// The test is level, not event: the wait is measured against the document's last
// fire, so it holds whether the escalation's mark is stale or gone entirely —
// which is the normal state between re-fires, the mark's TTL being shorter than
// every backoff step past the second.
func TestEscalateGap_UnplannableRefireIsPaced(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name     string
		withMark bool
	}{{"staleMarkStanding", true}, {"markGone", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixRefirePace2"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			target := mustTarget(t, h.engine.source, targetID)
			entityID := testNanoID(t)
			entityKey := "vtx.leaseApp." + entityID
			key := markKey(targetID, entityID, goalLegGap)
			row := goalLegRow(entityID, 0, nil)

			esc, ok := augurEscalation(h.engine.source, target, escalateUnplannable, targetID, entityID, entityKey, goalLegGap)
			if !ok {
				t.Fatal("setup: the fixture target must escalate unplannable")
			}

			// Two re-arms behind it: the wait is base × 2 = one hour at the
			// defaults, and the document is the escalation-only shape the door
			// writes.
			seedDoc := func(age time.Duration) {
				putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
					Reclaims: 2, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(age)),
				})
			}
			read := func() (dispatchCount, uint64) {
				doc, rev, err := h.engine.marks.getDispatchCount(ctx, targetID, entityID, goalLegGap)
				if err != nil {
					t.Fatalf("read the pacing document: %v", err)
				}
				return doc, rev
			}
			seedMark := func() uint64 {
				if !tc.withMark {
					return 0
				}
				putStateValue(t, ctx, h.conn, key, escalationMark(targetID, entityID, "", escalateUnplannable, pastLease()))
				entry, err := h.conn.KVGet(ctx, "weaver-state", key)
				if err != nil {
					t.Fatalf("read the seeded mark: %v", err)
				}
				return entry.Revision
			}

			// Inside the window: nothing fires, nothing is booked, and the stale
			// mark is left exactly where it stands — its own TTL is the backstop.
			seedDoc(-10 * time.Minute)
			markRev := seedMark()
			doc, countRev := read()
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil {
				t.Fatalf("read the seeded mark: %v", err)
			}
			if dec := h.engine.escalateGap(ctx, target, targetID, entityID, entityKey, goalLegGap, esc,
				escalateUnplannable, row, 9, rec, markRev, found, doc, countRev); dec != substrate.Ack {
				t.Fatalf("a paced pass = %v, want Ack: the wait is not a fault", dec)
			}
			h.requireNoOp(t)
			if paced, _ := read(); paced.Reclaims != 2 || paced.EscalatedAt != doc.EscalatedAt {
				t.Fatalf("pacing document = %+v, want it untouched at %+v: a pass that fired nothing books "+
					"nothing", paced, doc)
			}
			if tc.withMark {
				if entry, err := h.conn.KVGet(ctx, "weaver-state", key); err != nil || entry.Revision != markRev {
					t.Fatalf("the stale mark must stand while the wait runs (rev %v want %d, err=%v)", entry.Revision, markRev, err)
				}
			}

			// Past the window: one op, the tally lengthens the next wait, the
			// instant advances, and the stale mark is cleared under the revision
			// it was read at before the fresh episode's CAS-create.
			seedDoc(-61 * time.Minute)
			doc, countRev = read()
			rec, markRev, found, err = h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil {
				t.Fatalf("re-read the mark: %v", err)
			}
			if dec := h.engine.escalateGap(ctx, target, targetID, entityID, entityKey, goalLegGap, esc,
				escalateUnplannable, row, 10, rec, markRev, found, doc, countRev); dec != substrate.Ack {
				t.Fatalf("the due re-fire = %v, want Ack", dec)
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op once the window has elapsed", op["operationType"])
			}
			fired, _ := read()
			if fired.Reclaims != 3 {
				t.Fatalf("re-arm tally = %d, want 3: a re-fire is a re-arm and must lengthen the next wait", fired.Reclaims)
			}
			if fired.Count != 0 {
				t.Fatalf("retry budget = %d, want 0: a re-fire books no attempt either", fired.Count)
			}
			at, perr := time.Parse(time.RFC3339Nano, fired.EscalatedAt)
			if perr != nil || time.Since(at) > time.Minute {
				t.Fatalf("escalatedAt = %q (parse err=%v), want the instant this re-fire dispatched at", fired.EscalatedAt, perr)
			}
			after, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || after.Escalation != escalateUnplannable {
				t.Fatalf("mark after the re-fire = %+v (found=%v err=%v), want the fresh episode's, declaring its "+
					"class", after, found, err)
			}
			if tc.withMark && after.ClaimID == rec.ClaimID {
				t.Fatalf("the stale mark must be cleared and a fresh episode minted, got the same claimId %q", after.ClaimID)
			}
			h.requireNoOp(t)
		})
	}
}

// TestEscalateGap_PublishFailureWithdrawsTheObligation runs the exhausted door's
// republish-withdrawal pin through the seam for the two `unplannable` doors.
//
// `fire` records an obligation at the gap's key whenever a publish fails, which
// is right for an ordinary episode: the redelivery the failure asked for
// re-publishes the SAME episode. An escalation's retry is the seam's own paced
// re-fire instead, so the entry has no reader — and it does not merely idle: it
// burns one of the target's 256 slots, and the moment the gap can dispatch again
// dispatchGap's live-mark arm falls through on `owes` and re-publishes an
// episode nothing asked for.
func TestEscalateGap_PublishFailureWithdrawsTheObligation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name  string
		gap   string
		setup func(t *testing.T, h *handlerHarness, targetID, entityID string) map[string]any
	}{
		{"noEntryDoor", noEntryGap, func(t *testing.T, h *handlerHarness, targetID, entityID string) map[string]any {
			noEntryTarget(t, h, targetID)
			return noEntryRow(entityID, 0)
		}},
		{"noPlanDoor", goalLegGap, func(t *testing.T, h *handlerHarness, targetID, entityID string) map[string]any {
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			return goalLegRow(entityID, 0, nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixDoorNoOwe"
			entityID := testNanoID(t)
			row := tc.setup(t, h, targetID, entityID)

			if err := h.conn.JetStream().DeleteStream(ctx, "core-operations"); err != nil {
				t.Fatalf("delete ops stream: %v", err)
			}
			pubCtx, pubCancel := context.WithTimeout(ctx, 2*time.Second)
			defer pubCancel()
			if dec := h.engine.handleRow(pubCtx, h.rowMessage(t, targetID, entityID, row, 9, 1)); dec == substrate.Ack {
				t.Fatalf("a publish failure must not Ack the row — the redelivery it asks for is the retry")
			}
			if h.engine.republish.owes(targetID, entityID, tc.gap) {
				t.Fatal("an escalation's failed publish must leave no republish obligation: nothing reads it " +
					"while the escalation stands, and it re-publishes against the escalation's own mark once " +
					"the gap can dispatch again")
			}
		})
	}
}

// TestMark_EscalationSurvivesEveryReplace walks the two seams that rewrite a
// mark whole rather than minting one. Every field is rewritten, so the class an
// episode declares survives only by being threaded back through — and a mark
// that stops declaring it reads, at every router, as an ordinary leg mark whose
// action happens not to resolve, which is precisely the case the field exists to
// tell it apart from.
func TestMark_EscalationSurvivesEveryReplace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("fireEpisodeStaleReArm", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		h := newSweepHarness(t, ctx)

		const targetID = "fixReArmClass"
		target := &Target{
			TargetID: targetID,
			Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
		}
		h.seedTarget(target)
		entityID := testNanoID(t)
		key := markKey(targetID, entityID, "missing_x")

		// The external gap's expired episode — the one shape fireEpisode re-arms
		// in place — carrying the class an earlier escalation gave it.
		expired := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, pastLease())
		expired.Escalation = escalateExhausted
		staleRev := h.putMark(t, ctx, key, expired)

		row := map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "inflight_x": false,
		}
		pl, _, _, _, dec := h.engine.planGap(ctx, target, targetID, entityID, "missing_x", target.Gaps["missing_x"], row, 1, "")
		if pl == nil {
			t.Fatalf("setup: planGap must produce a plan, got dec=%v", dec)
		}
		if got, _ := h.engine.fireEpisode(ctx, targetID, entityID, "vtx.leaseApp."+entityID, "missing_x",
			actionDirectOp, pl, &expired, staleRev, true, true, "", true, ""); got != substrate.Ack {
			t.Fatalf("stale re-arm decision = %v, want Ack", got)
		}
		drainOps(t, h.ops, 1)

		rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x")
		if err != nil || !found {
			t.Fatalf("read the re-armed mark: found=%v err=%v", found, err)
		}
		if rec.Escalation != escalateExhausted {
			t.Fatalf("escalation on the re-armed mark = %q, want %q: a re-arm is the SAME episode, and it does "+
				"not stop being an escalation by having its lease refreshed", rec.Escalation, escalateExhausted)
		}
	})

	t.Run("sweepReclaimReplace", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		h := newSweepHarness(t, ctx)
		h.agePastWarmup()

		const targetID = "fixReclaimClass"
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "FixX"}},
		})
		entityID := testNanoID(t)
		key := markKey(targetID, entityID, "missing_x")

		expired := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, pastLease())
		expired.Escalation = escalateExhausted
		h.putMark(t, ctx, key, expired)
		h.putRow(t, ctx, targetID, entityID, map[string]any{
			"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true,
		})

		h.pass(ctx)
		h.nextOp(t)

		rec, _ := h.readMark(t, ctx, key)
		if rec.Escalation != escalateExhausted {
			t.Fatalf("escalation on the reclaimed mark = %q, want %q: the reclaim rewrites the whole value, so "+
				"a field it does not thread through is a field it deletes", rec.Escalation, escalateExhausted)
		}
	})
}

// TestBookEscalation_CreatesTheDocumentForUnplannableOnly pins the one write
// that differs by door.
//
// The `unplannable` doors are reached with no count document by construction —
// they book nothing, and a gap with no plan has mounted no attempt — so refusing
// to create one would leave them with nothing to pace on and every re-fire
// unpaced forever. The `exhausted` door keeps the refusal: absence is impossible
// past a spent budget, and a document created at count 0 would read to the
// sweep's count leg as an operator's un-park.
func TestBookEscalation_CreatesTheDocumentForUnplannableOnly(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx := context.Background()
	m := newStateTestStore(t, ctx)
	const targetID, gap = "t1", "missing_x"
	now := time.Now()

	t.Run("absentDocument", func(t *testing.T) {
		for _, tc := range []struct {
			trigger    string
			wantBooked bool
		}{{escalateUnplannable, true}, {escalateExhausted, false}} {
			t.Run(tc.trigger, func(t *testing.T) {
				entityID := testNanoID(t)
				booked, err := m.bookEscalation(ctx, targetID, entityID, gap, tc.trigger, now)
				if err != nil || booked != tc.wantBooked {
					t.Fatalf("bookEscalation(%q) over an absent document = (%v, %v), want (%v, nil)",
						tc.trigger, booked, err, tc.wantBooked)
				}
				doc, rev, err := m.getDispatchCount(ctx, targetID, entityID, gap)
				if err != nil {
					t.Fatalf("read the document: %v", err)
				}
				if !tc.wantBooked {
					if rev != 0 {
						t.Fatalf("document = %+v at revision %d, want none written: a zero created for the "+
							"exhausted door reads as an operator's un-park", doc, rev)
					}
					return
				}
				if doc.Count != 0 || doc.Leg != "" || doc.Reclaims != 1 || doc.EscalatedAt == "" {
					t.Fatalf("created document = %+v, want {count 0, no leg, reclaims 1, escalatedAt}", doc)
				}
			})
		}
	})

	t.Run("presentDocumentIsUpdatedForEitherTrigger", func(t *testing.T) {
		for _, trigger := range []string{escalateUnplannable, escalateExhausted} {
			t.Run(trigger, func(t *testing.T) {
				entityID := testNanoID(t)
				putStateValue(t, ctx, m.conn, countKey(targetID, entityID, gap),
					dispatchCount{Count: 2, Leg: "legA", Reclaims: 4})
				booked, err := m.bookEscalation(ctx, targetID, entityID, gap, trigger, now)
				if err != nil || !booked {
					t.Fatalf("bookEscalation(%q) over a live document = (%v, %v), want (true, nil)", trigger, booked, err)
				}
				doc, _, err := m.getDispatchCount(ctx, targetID, entityID, gap)
				if err != nil {
					t.Fatalf("read the document: %v", err)
				}
				if doc.Count != 2 || doc.Leg != "legA" {
					t.Fatalf("document = %+v, want the chain's own record untouched: recording a fire is not an "+
						"attempt of the gap's remediation", doc)
				}
				if doc.Reclaims != 5 || doc.EscalatedAt == "" {
					t.Fatalf("document = %+v, want reclaims 5 and this fire's instant", doc)
				}
			})
		}
	})
}

// TestBookDispatch_RestartsOverAnEscalationOnlyDocument pins what a fresh chain
// inherits from the escalation that preceded it: nothing.
//
// An escalation-only document — no attempt ever charged, no leg named, an
// escalation instant stamped — records one episode's pacing and nothing else, so
// the first attempt booked over it starts its own history. Carried forward, the
// fresh chain's very first reclaim would wait out the dead episode's exponential
// (hours of silence for a chain that has just started).
//
// The operator's un-park is the other document that reads zero, and it is told
// apart by the leg it still names: it keeps inheriting, which is the ratified
// stance for a chain that was merely re-armed.
func TestBookDispatch_RestartsOverAnEscalationOnlyDocument(t *testing.T) {
	t.Parallel()
	stamp := substrate.FormatTimestamp(time.Now().Add(-time.Hour))
	for _, tc := range []struct {
		name      string
		doc       dispatchCount
		actionRef string
		legScoped bool
		want      dispatchCount
	}{
		{
			name:      "escalationOnlyDocumentRestarts",
			doc:       dispatchCount{Reclaims: 3, EscalatedAt: stamp},
			actionRef: "legA", legScoped: true,
			want: dispatchCount{Count: 1, Leg: "legA"},
		},
		{
			name:      "escalationOnlyDocumentRestartsForADirectOpChainToo",
			doc:       dispatchCount{Reclaims: 3, EscalatedAt: stamp},
			actionRef: actionDirectOp,
			want:      dispatchCount{Count: 1},
		},
		{
			name:      "unParkedDocumentKeepsInheriting",
			doc:       dispatchCount{Leg: "legA", Reclaims: 3, EscalatedAt: stamp},
			actionRef: "legA", legScoped: true,
			want: dispatchCount{Count: 1, Leg: "legA", Reclaims: 3, EscalatedAt: stamp},
		},
		{
			name:      "aDocumentThatNeverEscalatedIsUntouchedByTheRule",
			doc:       dispatchCount{},
			actionRef: "legA", legScoped: true,
			want: dispatchCount{Count: 1, Leg: "legA"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := bookDispatch(tc.doc, tc.actionRef, true, false, tc.legScoped); got != tc.want {
				t.Fatalf("bookDispatch(%+v, %q) = %+v, want %+v", tc.doc, tc.actionRef, got, tc.want)
			}
		})
	}
}
