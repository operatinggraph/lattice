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

// futureTrigger is a class this build does not know — the shape a rolling
// upgrade produces, where a newer instance wrote the mark. It releases nothing
// and routes nowhere, which is what makes it the one class that still reaches
// the two seams that re-arm a mark in place.
const futureTrigger = "aTriggerThisBuildDoesNotKnow"

// TestMark_EscalationSurvivesEveryReplace walks the two seams that rewrite a
// mark whole rather than minting one, and the rule they share: the pair that
// says what an episode IS — the leg it stands over and the class it was
// escalated on — is carried forward while the re-arm is still that dispatch, and
// dropped when it is not.
//
// Carried, because a re-arm is the same episode with a fresh lease, and a mark
// that stops declaring its class reads at every router as an ordinary leg mark
// whose action happens not to resolve — the case the field exists to tell it
// apart from. Dropped when the re-arm resolves a DIFFERENT action, because that
// is a different dispatch: an escalation whose policy was withdrawn, re-armed as
// the gap's own remediation, would otherwise keep declaring an escalation that
// no longer exists, and a policy re-added later would route it as one.
func TestMark_EscalationSurvivesEveryReplace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("fireEpisodeStaleReArm", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			fireWith  string
			wantClass string
		}{
			{"sameDispatchCarriesTheClass", actionDirectOp, escalateExhausted},
			{"aDifferentDispatchDropsIt", "FixXDirectly", ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
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

				// The external gap's expired episode — the one shape fireEpisode
				// re-arms in place — carrying the class an earlier escalation gave it.
				expired := fixtureMark(targetID, entityID, "missing_x", actionDirectOp, pastLease())
				expired.Escalation = escalateExhausted
				expired.EscalatedFrom = "legA"
				staleRev := h.putMark(t, ctx, key, expired)

				row := map[string]any{
					"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_x": true, "inflight_x": false,
				}
				pl, _, _, _, dec := h.engine.planGap(ctx, target, targetID, entityID, "missing_x", target.Gaps["missing_x"], row, 1, "")
				if pl == nil {
					t.Fatalf("setup: planGap must produce a plan, got dec=%v", dec)
				}
				if got, _ := h.engine.fireEpisode(ctx, targetID, entityID, "vtx.leaseApp."+entityID, "missing_x",
					tc.fireWith, pl, &expired, staleRev, true, true, "", true, ""); got != substrate.Ack {
					t.Fatalf("stale re-arm decision = %v, want Ack", got)
				}
				drainOps(t, h.ops, 1)

				rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_x")
				if err != nil || !found {
					t.Fatalf("read the re-armed mark: found=%v err=%v", found, err)
				}
				if rec.Escalation != tc.wantClass {
					t.Fatalf("escalation on the re-armed mark = %q, want %q (re-armed as %q over a mark pinning %q)",
						rec.Escalation, tc.wantClass, tc.fireWith, expired.Action)
				}
				if tc.wantClass == "" && rec.EscalatedFrom != "" {
					t.Fatalf("escalatedFrom = %q, want it dropped with the class: a different dispatch stands "+
						"over nothing this mark recorded", rec.EscalatedFrom)
				}
			})
		}
	})

	t.Run("sweepReclaimReplace", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			gapAction string
			wantClass string
		}{
			{"sameDispatchCarriesTheClass", actionAssignTask, futureTrigger},
			{"aDifferentDispatchDropsIt", actionDirectOp, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				// A negligible backoff base so a collapse-only reclaim fires on
				// the first pass rather than pacing this vector away.
				h := newSweepHarness(t, ctx, func(c *Config) { c.ReclaimBackoffBase = time.Millisecond })
				h.agePastWarmup()

				const targetID = "fixReclaimClass"
				const gap = "missing_signature"
				h.seedTarget(&Target{
					TargetID: targetID,
					Gaps: map[string]GapAction{gap: {
						Action: tc.gapAction, Operation: "SignLease",
						Assignee: "row.applicant", Target: "row.entityKey",
					}},
				})
				h.engine.source.mu.Lock()
				h.engine.source.opMetaByType["SignLease"] = "vtx.meta." + testNanoID(t)
				h.engine.source.mu.Unlock()
				entityID := testNanoID(t)
				key := markKey(targetID, entityID, gap)

				// The one shape that still reaches this re-arm holding a class: a
				// mark whose class no route in this build knows, so nothing
				// releases it and nothing escalates it. The vectors differ in
				// whether the playbook still declares the action the mark pins.
				expired := fixtureMark(targetID, entityID, gap, actionAssignTask, pastLease())
				expired.Escalation = futureTrigger
				h.putMark(t, ctx, key, expired)
				h.putRow(t, ctx, targetID, entityID, map[string]any{
					"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
					"applicant": "vtx.identity." + entityID,
				})

				h.pass(ctx)
				h.nextOp(t)

				rec, _ := h.readMark(t, ctx, key)
				if rec.Escalation != tc.wantClass {
					t.Fatalf("escalation on the reclaimed mark = %q, want %q: the reclaim rewrites the whole "+
						"value, so what it does not thread through it deletes — and what it re-armed as a "+
						"different dispatch it must delete", rec.Escalation, tc.wantClass)
				}
			})
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

// --- Increment 2: the release ----------------------------------------------

// TestDispatchGap_PlannableAgainReleasesTheEscalation is the no-derivable-plan
// door's release. A goal gap whose catalog reached nothing from the row it was
// escalated on is not a permanent park: the row moves, a plan derives again, and
// the standing escalation is what would otherwise keep the gap from acting —
// the mark drops every delivery at the anti-storm arm, and the reclaim re-pins
// the escalation forever.
//
// The release is ONE write: the mark, revision-conditioned. The pacing document
// is deliberately kept — it is the count leg's only handle on a quiet row — and
// the fresh chain does not inherit it: its CAS-create books count 0→1 with the
// escalation's re-arm history and last-fire instant restarted.
func TestDispatchGap_PlannableAgainReleasesTheEscalation(t *testing.T) {
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

			const targetID = "fixPlannableAgain"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, goalLegGap)

			// The escalation's own state: a LIVE mark over no leg (nothing ever
			// dispatched for this gap), and the pacing document its fire created.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Reclaims: 3, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
			})
			putStateValue(t, ctx, h.conn, key,
				escalationMark(targetID, entityID, "", escalateUnplannable, futureLease()))
			h.engine.issues.set(issueKeyGapEntity(targetID, entityID, goalLegGap), "warning",
				codeGapEscalatedToAugur, "raised when the escalation fired")
			seeded, _ := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap))

			// The row from which legA's precondition now holds: a plan derives.
			row := goalLegRow(entityID, tc.budget, map[string]any{"ready": true})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 11, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}
			if op := h.nextOp(t); op["operationType"] != "DoA" {
				t.Fatalf("operationType = %v, want DoA: the released gap dispatches its OWN leg, as a genuinely "+
					"fresh episode", op["operationType"])
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found {
				t.Fatalf("read the fresh mark: found=%v err=%v", found, err)
			}
			if rec.Action != "legA" || rec.Escalation != "" || rec.EscalatedFrom != "" {
				t.Fatalf("mark = %+v, want legA's own episode: the escalation is over, so nothing on the mark "+
					"may still declare one", rec)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 1 || doc.Leg != "legA" {
				t.Fatalf("count document = %+v, want the fresh chain's first attempt charged to legA", doc)
			}
			if doc.Reclaims != 0 || doc.EscalatedAt != "" {
				t.Fatalf("count document = %+v, want the escalation's pacing restarted: the re-arm history and "+
					"last fire belong to the episode that ended, and inherited they would make this chain's "+
					"first reclaim wait it out", doc)
			}
			if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap)); ok && is.Since == seeded.Since {
				t.Fatalf("the escalation record standing since %s must retire at the release; still standing as "+
					"%+v", seeded.Since, is)
			}
			h.requireNoOp(t)
		})
	}
}

// TestDispatchGap_EntryAddedReleasesTheEscalation is the no-playbook-entry
// door's release: the re-author that adds the entry is the release, and the gap
// takes the disposition that entry declares.
//
// A `surface` re-author is the same release down the other leg, and the vector
// says which: lane 1's surface arm answers above the mark read — one KV read per
// delivery is what a column of many open rows must not pay — so the sweep, which
// already holds the mark, is what releases it there.
func TestDispatchGap_EntryAddedReleasesTheEscalation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		entry  map[string]any
		budget int
		wantOp string
	}{
		{"entryAdded", map[string]any{"action": "directOp", "operation": "FixUnknown"}, 3, "FixUnknown"},
		{"entryAddedUncapped", map[string]any{"action": "directOp", "operation": "FixUnknown"}, 0, "FixUnknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixEntryAdded"
			vtx := testNanoID(t)
			spec := targetSpecFixture(targetID)
			spec["augur"] = map[string]any{"escalate": []any{escalateUnplannable}}
			h.engine.source.handle(vertexEvent(t, vtx, weaverTargetClass))
			h.engine.source.handle(specEvent(t, vtx, spec))
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, noEntryGap)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, noEntryGap), dispatchCount{
				Reclaims: 2, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
			})
			putStateValue(t, ctx, h.conn, key, mark{
				TargetID: targetID, EntityKey: "vtx.leaseApp." + entityID, Gap: noEntryGap,
				Action: actionDirectOp, Escalation: escalateUnplannable,
				ClaimID:        testNanoIDStatic("escalationClaim0"),
				ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
				LeaseExpiresAt: futureLease(),
			})
			h.engine.issues.set(issueKeyGapEntity(targetID, entityID, noEntryGap), "warning",
				codeGapEscalatedToAugur, "raised when the escalation fired")
			seeded, _ := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, noEntryGap))

			// The re-author: the playbook now names the column.
			gaps, _ := spec["gaps"].(map[string]any)
			gaps[noEntryGap] = tc.entry
			h.engine.source.handle(specEvent(t, vtx, spec))

			row := noEntryRow(entityID, tc.budget)
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 12, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}
			if tc.wantOp == "" {
				h.requireNoOp(t)
			} else if op := h.nextOp(t); op["operationType"] != tc.wantOp {
				t.Fatalf("operationType = %v, want %q: the released gap runs the disposition its new entry "+
					"declares", op["operationType"], tc.wantOp)
			}
			if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, noEntryGap)); ok && is.Since == seeded.Since {
				t.Fatalf("the escalation record standing since %s must retire at the release; still standing as "+
					"%+v", seeded.Since, is)
			}
			if doc := readCount(t, ctx, h.conn, targetID, entityID, noEntryGap); doc.EscalatedAt != "" {
				t.Fatalf("count document = %+v, want the escalation's pacing restarted by the fresh chain's "+
					"first attempt", doc)
			}
			h.requireNoOp(t)
		})
	}
}

// TestDispatchGap_EscalationOverALeg_ReleasesOnlyAtItsBoundary is the ordering
// between the two release rules, and the hazard it exists for: the leg an
// escalation displaced may have an OPEN artifact — a task on a human's queue —
// and "the gap is plannable now" is no evidence at all about that task. Only the
// leg's own declared effects holding says the artifact concluded, so a plannable
// row must not release, and the boundary must.
func TestDispatchGap_EscalationOverALeg_ReleasesOnlyAtItsBoundary(t *testing.T) {
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

			const targetID = "fixEscOverALeg"
			// legA is the human task the escalation displaced; the escalation
			// stands over it, recorded on the count document alone.
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID, actionAssignTask))
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, goalLegGap)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Count: 2, Leg: "legA", Reclaims: 1,
				EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
			})
			putStateValue(t, ctx, h.conn, key,
				escalationMark(targetID, entityID, "", escalateUnplannable, futureLease()))

			// A row a plan derives from — and legA's effects do NOT hold, so its
			// task may still be open.
			plannable := goalLegRow(entityID, tc.budget, map[string]any{"ready": true})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, plannable, 13, 1)); dec != substrate.Ack {
				t.Fatalf("decision = %v, want Ack", dec)
			}
			h.requireNoOp(t)
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.Escalation != escalateUnplannable {
				t.Fatalf("mark = %+v (found=%v err=%v), want the escalation still standing: a plannable row says "+
					"nothing about whether the displaced leg's task is still open", rec, found, err)
			}
			if doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap); doc.Count != 2 || doc.Leg != "legA" {
				t.Fatalf("count document = %+v, want {2 legA} untouched", doc)
			}

			// The boundary: legA's declared effect holds, so its artifact
			// concluded and the chain advances to legB.
			advanced := goalLegRow(entityID, tc.budget, map[string]any{"ready": true, "aDone": true})
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, advanced, 14, 1)); dec != substrate.Ack {
				t.Fatalf("boundary delivery: decision = %v, want Ack", dec)
			}
			if op := h.nextOp(t); op["operationType"] != "DoB" {
				t.Fatalf("operationType = %v, want DoB: the boundary is the release an escalation over a leg has",
					op["operationType"])
			}
			h.requireNoOp(t)
		})
	}
}

// TestDispatchGap_UnParkedExhaustedEscalationReleases is the class release for
// the trigger this design did not invent. An operator's resetBudget (or a lens
// raising maxretries_<g>) makes an exhausted gap dispatchable again while its
// escalation mark still stands — and the suppression gate then stops routing it
// to the exhausted door, so the general path is where it arrives. Routing only
// the `unplannable` class would strand it there: its pin resolves to nothing,
// which is a config error, and it would sit on a false PlaybookConfigError with
// no release and no re-fire.
//
// The old-shape variant is the migration row: a mark with no class declares
// nothing to route on, so lane 1 sees an ordinary in-flight episode and takes
// the anti-storm drop — the gap stays held until the mark's own TTL, and the
// sweep's config-error disposition for such a pin is pinned where the sweep is
// (TestSweep_ReclaimOfEscalationPreservesDisplacedLeg).
func TestDispatchGap_UnParkedExhaustedEscalationReleases(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name     string
		declares string
	}{{"declaredClass", escalateExhausted}, {"oldShapeMark", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newHandlerHarness(t, ctx)

			const targetID = "fixUnParkedEsc"
			registerSpec(t, h.engine.source, goalLegSpec(targetID, actionDirectOp, true))
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, goalLegGap)

			// The un-park: the budget re-armed to 0 under a standing escalation,
			// with no leg on the document (nothing was ever charged to one).
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Reclaims: 4, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
			})
			putStateValue(t, ctx, h.conn, key,
				escalationMark(targetID, entityID, "", tc.declares, futureLease()))
			h.engine.issues.set(issueKeyGapEntity(targetID, entityID, goalLegGap), "warning",
				codeGapEscalatedToAugur, "raised when the escalation fired")
			seeded, _ := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap))

			row := goalLegRow(entityID, 3, nil)
			if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 15, 1)); dec != substrate.Ack &&
				dec != substrate.NakWithLongDelay {
				t.Fatalf("decision = %v, want Ack or the config floor", dec)
			}

			if tc.declares == "" {
				h.requireNoOp(t)
				rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
				if err != nil || !found || rec.Action != actionDirectOp {
					t.Fatalf("mark = %+v (found=%v err=%v), want the old-shape mark left standing: nothing "+
						"declares a class to release on, so it holds the gap until its own TTL", rec, found, err)
				}
				if doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap); doc.Count != 0 || doc.Reclaims != 4 {
					t.Fatalf("count document = %+v, want the un-parked budget untouched: nothing dispatched", doc)
				}
				if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap)); !ok ||
					is.Since != seeded.Since {
					t.Fatalf("issue at the gap's entity key = %+v (present=%v), want the escalation record still "+
						"standing since %s: nothing released it", is, ok, seeded.Since)
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != "DoA" {
				t.Fatalf("operationType = %v, want DoA: the un-parked gap runs its own chain again",
					op["operationType"])
			}
			rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, goalLegGap)
			if err != nil || !found || rec.Action != "legA" || rec.Escalation != "" {
				t.Fatalf("mark = %+v (found=%v err=%v), want legA's own fresh episode", rec, found, err)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 1 || doc.Leg != "legA" || doc.Reclaims != 0 || doc.EscalatedAt != "" {
				t.Fatalf("count document = %+v, want a fresh chain charged to legA with the escalation's pacing "+
					"restarted", doc)
			}
			if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap)); ok && is.Since == seeded.Since {
				t.Fatalf("the escalation record standing since %s must retire at the release; still standing as "+
					"%+v", seeded.Since, is)
			}
			h.requireNoOp(t)
		})
	}
}

// TestSweep_ReclaimOfUnplannableEscalationReResolvesFresh is the class release
// and the class re-fire on the leg that visits a gap through its MARK.
//
// Released, the reclaim dispatches nothing at all. A released gap is a markless
// open gap, and the two seams that already own that state carry the
// collapse-only refusal this leg would otherwise have to re-implement — a
// delivery, and the count leg's arm (n) for a quiet row — so re-dispatching here
// would be a second implementation of a rule whose whole point is that it is
// applied once, before any fresh claimId is minted.
//
// Not released, nothing about the gap's own episode happens either: no `mark
// reclaimed` Warn, no re-arm booked against a chain the escalation is not a
// member of, and the paced seam decides whether the reasoning op re-fires at
// all. The escalation mark never reaches the collapse-only classification: it is
// routed above it, which is what keeps a classifier that reads a resolved
// dispatch SHAPE from being handed a dispatch class no catalog resolves.
func TestSweep_ReclaimOfUnplannableEscalationReResolvesFresh(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		budget    int
		plannable bool
	}{
		{"plannableAgain", 5, true},
		{"plannableAgainUncapped", 0, true},
		{"stillUnplannable", 5, false},
		{"stillUnplannableUncapped", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixReclaimRelease"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, goalLegGap)

			// An escalation over NO leg: nothing was ever dispatched for this
			// gap, so the document holds only the episode's pacing — paced out,
			// so a re-fire that is owed is due rather than waiting.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Reclaims: 1, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
			})
			putStateValue(t, ctx, h.conn, key,
				escalationMark(targetID, entityID, "", escalateUnplannable, pastLease()))
			h.engine.issues.set(issueKeyGapEntity(targetID, entityID, goalLegGap), "warning",
				codeGapEscalatedToAugur, "raised when the escalation fired")
			seeded, _ := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap))

			extra := map[string]any{}
			if tc.plannable {
				extra["ready"] = true
			}
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, extra))

			h.pass(ctx)

			if tc.plannable {
				h.requireNoOp(t)
				if h.markExists(t, ctx, key) {
					t.Fatal("a gap that resolves again must have its escalation released — the mark is what " +
						"stands between it and its own remediation")
				}
				if !h.countExists(t, ctx, targetID, entityID, goalLegGap) {
					t.Fatal("the release must keep the pacing document: it is the count leg's only handle on a " +
						"quiet row, and lane 1 holds no revision to condition a delete on")
				}
				if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap)); ok && is.Since == seeded.Since {
					t.Fatalf("the escalation record standing since %s must retire at the release; still standing "+
						"as %+v", seeded.Since, is)
				}
				// The next pass's arm (n) is what dispatches a released gap that
				// has gone quiet — the reclaim deliberately did not.
				h.pass(ctx)
				if op := h.nextOp(t); op["operationType"] != "DoA" {
					t.Fatalf("operationType = %v, want DoA: the markless open gap is arm (n)'s to fire, with its "+
						"own collapse-only refusal applied", op["operationType"])
				}
				doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
				if doc.Count != 1 || doc.Reclaims != 0 || doc.EscalatedAt != "" {
					t.Fatalf("count document = %+v, want the fresh chain's first attempt with the escalation's "+
						"pacing restarted", doc)
				}
				h.requireNoOp(t)
				return
			}

			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op re-fired paced by the seam", op["operationType"])
			}
			rec, _ := h.readMark(t, ctx, key)
			if rec.Escalation != escalateUnplannable {
				t.Fatalf("re-fired mark = %+v, want the escalation still declaring its class", rec)
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 0 {
				t.Fatalf("count document = %+v, want no attempt booked: an escalation is not a member of the "+
					"chain it is about", doc)
			}
			if doc.Reclaims != 2 {
				t.Fatalf("re-arm tally = %d, want 2: the seam's own re-fire booking, not the reclaim ladder's",
					doc.Reclaims)
			}
			h.requireNoOp(t)
		})
	}
}

// TestSweep_OrphanArmSparesAStandingNoEntryEscalation pins the one mark the
// orphan-column arm must not delete. Its rule — a mark whose column the playbook
// no longer names is stranded bookkeeping — is exactly inverted for the no-entry
// door, whose whole premise is that no entry exists: deleting that mark leaves
// the gap markless, and the next delivery mints a fresh (rejected) reasoning
// claim, which is the unpaced re-fire per delivery the episode model removes.
//
// The policy is what tells the two apart, and the second vector is what makes
// the guard mean something: with the escalation policy gone the mark is stranded
// again and the arm deletes it exactly as it always has.
func TestSweep_OrphanArmSparesAStandingNoEntryEscalation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		escalates bool
	}{{"policyStands", true}, {"policyRemoved", false}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixOrphanSpare"
			spec := targetSpecFixture(targetID)
			if tc.escalates {
				spec["augur"] = map[string]any{"escalate": []any{escalateUnplannable}}
			}
			registerSpec(t, h.engine.source, spec)
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, noEntryGap)

			// Paced out, so a spared episode's re-fire is due on this pass: the
			// positive half must show the mark surviving AND the seam acting.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, noEntryGap), dispatchCount{
				Reclaims: 1, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
			})
			putStateValue(t, ctx, h.conn, key, mark{
				TargetID: targetID, EntityKey: "vtx.leaseApp." + entityID, Gap: noEntryGap,
				Action: actionDirectOp, Escalation: escalateUnplannable,
				ClaimID:        testNanoIDStatic("escalationClaim0"),
				ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
				LeaseExpiresAt: pastLease(),
			})
			h.putRow(t, ctx, targetID, entityID, noEntryRow(entityID, 0))

			h.pass(ctx)

			if !tc.escalates {
				h.requireNoOp(t)
				if h.markExists(t, ctx, key) {
					t.Fatal("with no policy escalating it, an escalation mark on a column the playbook does not " +
						"name is stranded state and the orphan arm deletes it")
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op: the spared mark reached the class branch, "+
					"which is what re-fires the episode", op["operationType"])
			}
			rec, _ := h.readMark(t, ctx, key)
			if rec.Escalation != escalateUnplannable {
				t.Fatalf("mark = %+v, want the standing episode's, re-fired and still declaring its class", rec)
			}
		})
	}
}

// TestSweep_CountLegRefiresAMarklessEscalation is the quiet row's half of the
// episode model. A standing escalation is normally MARKLESS between re-fires —
// the mark's TTL is shorter than every backoff step past the second — and a row
// that has stopped being delivered has no other leg that visits it. Without this
// route a reasoning claim that was never minted is never re-derived for exactly
// the rows that most need it.
//
// Three shapes, one route: a document over no leg whose gap is still
// unplannable re-fires paced; a document over a LEG is tested for that leg's
// boundary first and re-fires paced when it has not been reached — which is why
// the branch sits ABOVE arm (n)'s `Count == 0` test, since a document over a leg
// carries that leg's attempts; and a leg-less document whose gap resolves again
// falls through to arm (n), which fires the plan with its own refusals applied.
func TestSweep_CountLegRefiresAMarklessEscalation(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name      string
		budget    int
		doc       dispatchCount
		plannable bool
		wantOp    string
	}{
		{"legLessStillUnplannable", 5, dispatchCount{Reclaims: 1}, false, defaultAugurOp},
		{"legLessStillUnplannableUncapped", 0, dispatchCount{Reclaims: 1}, false, defaultAugurOp},
		{"overALegNotAtItsBoundary", 5, dispatchCount{Count: 3, Leg: "legA", Reclaims: 1}, false, defaultAugurOp},
		{"legLessPlannableAgain", 5, dispatchCount{Reclaims: 1}, true, "DoA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixCountLegEsc"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			entityID := testNanoID(t)

			doc := tc.doc
			doc.EscalatedAt = substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour))
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), doc)
			extra := map[string]any{}
			if tc.plannable {
				extra["ready"] = true
			}
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, extra))
			h.engine.issues.set(issueKeyGapEntity(targetID, entityID, goalLegGap), "warning",
				codeGapEscalatedToAugur, "raised when the escalation fired")
			seeded, _ := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap))

			h.pass(ctx)

			if op := h.nextOp(t); op["operationType"] != tc.wantOp {
				t.Fatalf("operationType = %v, want %q", op["operationType"], tc.wantOp)
			}
			after := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if tc.wantOp == defaultAugurOp {
				if after.Count != tc.doc.Count || after.Leg != tc.doc.Leg {
					t.Fatalf("count document = %+v, want the chain's own record untouched at %+v: the re-fire "+
						"books nothing", after, tc.doc)
				}
				if after.Reclaims != tc.doc.Reclaims+1 {
					t.Fatalf("re-arm tally = %d, want %d: the seam books its own fire so the next wait lengthens",
						after.Reclaims, tc.doc.Reclaims+1)
				}
			} else {
				if after.Count != 1 || after.Leg != "legA" || after.Reclaims != 0 || after.EscalatedAt != "" {
					t.Fatalf("count document = %+v, want the fresh chain's first attempt with the escalation's "+
						"pacing restarted", after)
				}
				if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, goalLegGap)); ok && is.Since == seeded.Since {
					t.Fatalf("the escalation record standing since %s must retire when the gap resolves again; "+
						"still standing as %+v", seeded.Since, is)
				}
			}
			h.requireNoOp(t)
		})
	}
}

// TestSweep_CountLegDoorTwoRouteHonoursTheGates pins where the no-entry door's
// count-leg route sits. Its arm answers ABOVE the leg's violating and
// suppression gates — a column the playbook does not name has nothing to bound,
// so the arm returns before them — which means the route must restate both
// inline or it would act on rows lane 1 never dispatches for: a row that is not
// violating, and a gap whose remediation is already in flight.
func TestSweep_CountLegDoorTwoRouteHonoursTheGates(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		row    map[string]any
		wantOp bool
	}{
		{"violatingAndFree", map[string]any{}, true},
		{"notViolating", map[string]any{"violating": false}, false},
		{"remediationInFlight", map[string]any{"inflight_unknown": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixCountLegDoor2"
			spec := targetSpecFixture(targetID)
			spec["augur"] = map[string]any{"escalate": []any{escalateUnplannable}}
			registerSpec(t, h.engine.source, spec)
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, noEntryGap), dispatchCount{
				Reclaims: 1, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
			})
			row := noEntryRow(entityID, 0)
			for k, v := range tc.row {
				row[k] = v
			}
			h.putRow(t, ctx, targetID, entityID, row)

			h.pass(ctx)

			if !tc.wantOp {
				h.requireNoOp(t)
				if doc := readCount(t, ctx, h.conn, targetID, entityID, noEntryGap); doc.Reclaims != 1 {
					t.Fatalf("count document = %+v, want it untouched: a pass that fired nothing books nothing", doc)
				}
				return
			}
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op re-fired for a quiet row", op["operationType"])
			}
			if doc := readCount(t, ctx, h.conn, targetID, entityID, noEntryGap); doc.Reclaims != 2 || doc.Count != 0 {
				t.Fatalf("count document = %+v, want {count 0, reclaims 2}", doc)
			}
			h.requireNoOp(t)
		})
	}
}

// TestSweep_CountLegNeverUnParksAnUnplannableZero is the hazard the pacing
// document's own creation rule names: a count key that exists and reads 0 is the
// one state arm (n) treats as an operator's un-park, and an `unplannable`
// escalation now writes exactly that shape. The two are told apart before the
// zero is ever read — the escalation's document is routed by its `escalatedAt`
// stamp, above arm (n) — so a gap that resolves nothing never gets a markless
// episode fired at it.
//
// The operator verb is the same rule from the other side, and the pairing is the
// standing one: the verb must refuse exactly what the arm permanently declines.
func TestSweep_CountLegNeverUnParksAnUnplannableZero(t *testing.T) {
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

			const targetID = "fixNoUnPark"
			registerSpec(t, h.engine.source, goalLegBlockedSpec(targetID))
			entityID := testNanoID(t)

			// Inside its backoff window: the escalation is paced, so this pass
			// must fire NOTHING — and the zero it leaves behind must not read as
			// an un-park to arm (n) either.
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Reclaims: 2, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
			})
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, nil))

			h.pass(ctx)

			h.requireNoOp(t)
			if doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap); doc.Count != 0 || doc.Reclaims != 2 {
				t.Fatalf("count document = %+v, want it untouched: nothing fired, so nothing books", doc)
			}

			// The verb refuses the same gap the arm declines, and says why.
			_, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, goalLegGap)
			if err == nil {
				t.Fatal("resetBudget must refuse a gap whose plan resolves no action for this row — re-arming a " +
					"budget the arm will never act on promises a dispatch that cannot happen")
			}
			// The no-entry door's gap is refused by the same verb, one arm earlier.
			if _, err := h.engine.ResetRetryBudget(ctx, targetID, entityID, noEntryGap); err == nil {
				t.Fatal("resetBudget must refuse a column the playbook does not name")
			}
		})
	}
}

// TestSweep_SurfaceReAuthorReleasesTheEscalation is the no-entry door's other
// release: a re-author that makes the column `surface` answers the dead end just
// as an action entry does — the playbook now says this column is reported, not
// remediated — and the escalation must end with it, or a record saying the gap
// is on the reasoning tier stands over a column nobody is asking anyone to fix.
//
// The sweep is the leg that releases it, and the vector is why: lane 1's surface
// arm answers above the mark read, so a delivery never sees the mark at all,
// while the sweep enumerates marks and already holds it.
func TestSweep_SurfaceReAuthorReleasesTheEscalation(t *testing.T) {
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
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixSurfaceRelease"
			spec := targetSpecFixture(targetID)
			spec["augur"] = map[string]any{"escalate": []any{escalateUnplannable}}
			gaps, _ := spec["gaps"].(map[string]any)
			gaps[noEntryGap] = map[string]any{"action": actionSurface, "issueCode": "UnroutedTasks"}
			registerSpec(t, h.engine.source, spec)
			entityID := testNanoID(t)
			key := markKey(targetID, entityID, noEntryGap)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, noEntryGap), dispatchCount{
				Reclaims: 1, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
			})
			putStateValue(t, ctx, h.conn, key, mark{
				TargetID: targetID, EntityKey: "vtx.leaseApp." + entityID, Gap: noEntryGap,
				Action: actionDirectOp, Escalation: escalateUnplannable,
				ClaimID:        testNanoIDStatic("escalationClaim0"),
				ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
				LeaseExpiresAt: pastLease(),
			})
			h.engine.issues.set(issueKeyGapEntity(targetID, entityID, noEntryGap), "warning",
				codeGapEscalatedToAugur, "raised when the escalation fired")
			seeded, _ := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, noEntryGap))
			h.putRow(t, ctx, targetID, entityID, noEntryRow(entityID, tc.budget))

			h.pass(ctx)

			h.requireNoOp(t)
			if h.markExists(t, ctx, key) {
				t.Fatal("a column the playbook now only surfaces has no episode to keep on the reasoning tier; " +
					"its escalation mark must be released")
			}
			if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, noEntryGap)); ok && is.Since == seeded.Since {
				t.Fatalf("the escalation record standing since %s must retire with the mark; still standing as "+
					"%+v", seeded.Since, is)
			}
		})
	}
}

// TestSweep_CountLegUnParkOverALegStillDispatches is the shape the escalation
// route must not swallow: an operator's un-park of a gap that was escalated
// while it stood on a leg.
//
// resetBudget zeroes the attempts and keeps the rest — the leg, the re-arm tally
// and the escalation instant — so the document it leaves is stamped
// `escalatedAt` AND names a leg, which is the escalation-over-a-leg shape one
// field short. The field is the attempt count, and it is decisive: zero attempts
// against a named leg is a state only the verb writes, and the un-park's whole
// point is that arm (n) dispatches it for a row no delivery visits any more.
//
// Both policies run, because the discriminator must be structural: the target
// that escalates only `exhausted` is the live shape the verb is used on, and the
// one that also escalates `unplannable` is where a route reading the stamp alone
// would intercept the un-park.
func TestSweep_CountLegUnParkOverALegStillDispatches(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name   string
		budget int
		spec   func(string) map[string]any
	}{
		{"exhaustedPolicy", 5, func(id string) map[string]any { return goalLegSpec(id, actionDirectOp, true) }},
		{"exhaustedPolicyUncapped", 0, func(id string) map[string]any { return goalLegSpec(id, actionDirectOp, true) }},
		{"unplannablePolicy", 5, func(id string) map[string]any { return goalLegBlockedSpec(id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx)
			h.agePastWarmup()

			const targetID = "fixUnParkOverLeg"
			registerSpec(t, h.engine.source, tc.spec(targetID))
			entityID := testNanoID(t)

			stamp := substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour))
			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, goalLegGap), dispatchCount{
				Count: 0, Leg: "legA", Reclaims: 2, EscalatedAt: stamp,
			})
			// `ready` keeps legA applicable for the blocked fixture, so both
			// policies reach the same re-armed dispatch.
			h.putRow(t, ctx, targetID, entityID, goalLegRow(entityID, tc.budget, map[string]any{"ready": true}))

			h.pass(ctx)

			if op := h.nextOp(t); op["operationType"] != "DoA" {
				t.Fatalf("operationType = %v, want DoA: an un-parked gap is arm (n)'s to dispatch, and a stamp "+
					"left by the escalation that preceded the un-park does not make it an episode again",
					op["operationType"])
			}
			doc := readCount(t, ctx, h.conn, targetID, entityID, goalLegGap)
			if doc.Count != 1 || doc.Leg != "legA" {
				t.Fatalf("count document = %+v, want the re-armed leg's first attempt", doc)
			}
			if doc.Reclaims != 2 || doc.EscalatedAt != stamp {
				t.Fatalf("count document = %+v, want the re-arm tally and escalation instant carried forward: "+
					"the verb re-arms the budget, not the pacing of an episode that may still be open", doc)
			}
			h.requireNoOp(t)
		})
	}
}

// nonGoalEscalationTarget is the live `exhausted` consumer's shape: a target
// with no goal anywhere, whose gap dispatches one action and whose count
// document therefore records that action as its `leg` — bookDispatch stores the
// pick the attempts are charged to, for every shape that has no legs at all.
func nonGoalEscalationTarget(targetID, gap string, ga GapAction) *Target {
	return &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{gap: ga},
		Augur:    &AugurPolicy{Escalate: []string{escalateExhausted}},
	}
}

// TestDispatchGap_NonGoalEscalationReleasesOnARaisedCap is the release for the
// only targets that escalate today: gaps with no goal, no catalog and no legs.
//
// Their count document still records a `leg` — it is where bookDispatch keeps
// the action the attempts are charged to — so a release ordered on "does this
// escalation stand over a leg?" must ask for a PLAN leg, which only a goal gap
// has. Asking the count document instead reads a triggerLoom or assignTask gap
// as standing over a boundary releaseCompletedLeg can never test (it answers
// false for a gap with no goal at its first line), and the escalation would then
// stand for the life of the gap: no release, and a re-fired reasoning claim the
// Processor rejects create-only for as long as the row keeps arriving.
//
// The cap being raised is what makes the gap dispatchable again, and the
// exhausted class's release is exactly that fact: an escalation reaches this
// path at all only past the suppression gate that would have routed a still
// exhausted gap to its own door.
func TestDispatchGap_NonGoalEscalationReleasesOnARaisedCap(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixNonGoalRelease"
	const gap = "missing_bgcheck"
	h.seedPattern("bgcheckFlow", testNanoID(t))
	h.seedTarget(nonGoalEscalationTarget(targetID, gap, GapAction{
		Action: actionTriggerLoom, Pattern: "bgcheckFlow", Subject: "row.entityKey",
	}))
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, gap)

	// The state a spent budget and its escalation leave: three attempts charged
	// to the gap's own action, the escalation's stamp, and its mark.
	putStateValue(t, ctx, h.conn, countKey(targetID, entityID, gap), dispatchCount{
		Count: 3, Leg: actionTriggerLoom, Reclaims: 2,
		EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
	})
	putStateValue(t, ctx, h.conn, key, mark{
		TargetID: targetID, EntityKey: "vtx.leaseApp." + entityID, Gap: gap,
		Action: actionDirectOp, Escalation: escalateExhausted,
		ClaimID:        testNanoIDStatic("escalationClaim0"),
		ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
		LeaseExpiresAt: futureLease(),
	})
	h.engine.issues.set(issueKeyGapEntity(targetID, entityID, gap), "warning",
		codeGapEscalatedToAugur, "raised when the escalation fired")
	seeded, _ := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, gap))

	// The lens raises the cap: the gap has attempts again.
	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
		"inflight_bgcheck": false, "maxretries_bgcheck": 5,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 21, 1)); dec != substrate.Ack {
		t.Fatalf("decision = %v, want Ack", dec)
	}
	if op := h.nextOp(t); op["operationType"] != "StartLoomPattern" {
		t.Fatalf("operationType = %v, want the gap's own remediation: the released gap dispatches what its "+
			"playbook entry declares", op["operationType"])
	}
	rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, gap)
	if err != nil || !found || rec.Escalation != "" || rec.Action != actionTriggerLoom {
		t.Fatalf("mark = %+v (found=%v err=%v), want the gap's own fresh episode with no class on it",
			rec, found, err)
	}
	if doc := readCount(t, ctx, h.conn, targetID, entityID, gap); doc.Count != 4 || doc.Leg != actionTriggerLoom {
		t.Fatalf("count document = %+v, want the fresh attempt charged to the gap's own action", doc)
	}
	if is, ok := issueAt(h.engine.issues, issueKeyGapEntity(targetID, entityID, gap)); ok && is.Since == seeded.Since {
		t.Fatalf("the escalation record standing since %s must retire at the release; still standing as %+v",
			seeded.Since, is)
	}
	h.requireNoOp(t)
}

// TestDispatchGap_NonGoalEscalationHoldsWhileTheRemediationIsInFlight is the
// duplicate guard the release above leans on. A collapse-only gap whose released
// episode would mint a fresh claimId — and so a second task — is not held back by
// the escalation at all: it is held by the lens's inflight_<g> companion and the
// suppression gate, exactly as it is today once an escalation mark TTLs.
func TestDispatchGap_NonGoalEscalationHoldsWhileTheRemediationIsInFlight(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixNonGoalHeld"
	const gap = "missing_signature"
	h.seedTarget(nonGoalEscalationTarget(targetID, gap, GapAction{
		Action: actionAssignTask, Operation: "SignLease", Assignee: "row.applicant", Target: "row.entityKey",
	}))
	h.engine.source.mu.Lock()
	h.engine.source.opMetaByType["SignLease"] = "vtx.meta." + testNanoID(t)
	h.engine.source.mu.Unlock()
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, gap)

	putStateValue(t, ctx, h.conn, countKey(targetID, entityID, gap), dispatchCount{
		Count: 3, Leg: actionAssignTask,
		EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-time.Minute)),
	})
	putStateValue(t, ctx, h.conn, key, mark{
		TargetID: targetID, EntityKey: "vtx.leaseApp." + entityID, Gap: gap,
		Action: actionDirectOp, Escalation: escalateExhausted,
		ClaimID:        testNanoIDStatic("escalationClaim0"),
		ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
		LeaseExpiresAt: pastLease(),
	})

	row := map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
		"applicant": "vtx.identity." + entityID, "inflight_signature": true, "maxretries_signature": 5,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 22, 1)); dec != substrate.Ack {
		t.Fatalf("decision = %v, want Ack", dec)
	}
	h.requireNoOp(t)
	if rec, _, found, err := h.engine.marks.get(ctx, targetID, entityID, gap); err != nil || !found ||
		rec.Escalation != escalateExhausted {
		t.Fatalf("mark = %+v (found=%v err=%v), want it untouched: a suppressed gap never reaches the release, "+
			"which is what keeps a still-open task from being duplicated", rec, found, err)
	}
	if doc := readCount(t, ctx, h.conn, targetID, entityID, gap); doc.Count != 3 {
		t.Fatalf("count document = %+v, want it untouched", doc)
	}
}

// TestSweep_NonGoalEscalationReleaseDispatchesNothing is the reclaim's half of
// the same release: the mark goes, and this leg fires nothing for the markless
// open gap it leaves — a delivery and arm (n) own that state, and each carries
// the collapse-only refusal the reclaim would otherwise have to re-implement.
func TestSweep_NonGoalEscalationReleaseDispatchesNothing(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixNonGoalSweep"
	const gap = "missing_signature"
	h.seedTarget(nonGoalEscalationTarget(targetID, gap, GapAction{
		Action: actionAssignTask, Operation: "SignLease", Assignee: "row.applicant", Target: "row.entityKey",
	}))
	entityID := testNanoID(t)
	key := markKey(targetID, entityID, gap)

	putStateValue(t, ctx, h.conn, countKey(targetID, entityID, gap), dispatchCount{
		Count: 3, Leg: actionAssignTask,
		EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
	})
	h.putMark(t, ctx, key, mark{
		TargetID: targetID, EntityKey: "vtx.leaseApp." + entityID, Gap: gap,
		Action: actionDirectOp, Escalation: escalateExhausted,
		ClaimID:        testNanoIDStatic("escalationClaim0"),
		ClaimedAt:      substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
		LeaseExpiresAt: pastLease(),
	})
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
		"applicant": "vtx.identity." + entityID, "maxretries_signature": 5,
	})

	h.pass(ctx)

	h.requireNoOp(t)
	if h.markExists(t, ctx, key) {
		t.Fatal("a gap with attempts again must have its escalation released; the mark is what holds it")
	}
	if !h.countExists(t, ctx, targetID, entityID, gap) {
		t.Fatal("the release keeps the pacing document — it is the count leg's only handle on a quiet row")
	}
}

// TestSweep_CountLegEscalationRoutesAreWarmUpGated puts both new count-leg
// routes where arm (n) already is: below the registry warm-up. A start-up pass,
// or the first pass after an outage, reads definitions that are still replaying
// — an intermediate revision can carry the augur block while its gaps entry has
// not landed yet, which is the no-entry door's whole trigger — so firing there
// would escalate a column the target does name, for every affected row at once.
func TestSweep_CountLegEscalationRoutesAreWarmUpGated(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, tc := range []struct {
		name string
		gap  string
		spec func(string) map[string]any
	}{
		{"noEntryDoor", noEntryGap, func(id string) map[string]any {
			spec := targetSpecFixture(id)
			spec["augur"] = map[string]any{"escalate": []any{escalateUnplannable}}
			return spec
		}},
		{"noPlanDoor", goalLegGap, func(id string) map[string]any { return goalLegBlockedSpec(id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			h := newSweepHarness(t, ctx) // deliberately NOT aged past warm-up

			const targetID = "fixColdCountLeg"
			registerSpec(t, h.engine.source, tc.spec(targetID))
			entityID := testNanoID(t)

			putStateValue(t, ctx, h.conn, countKey(targetID, entityID, tc.gap), dispatchCount{
				Reclaims: 1, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
			})
			row := goalLegRow(entityID, 0, nil)
			if tc.gap == noEntryGap {
				row = noEntryRow(entityID, 0)
			}
			h.putRow(t, ctx, targetID, entityID, row)

			h.pass(ctx)

			h.requireNoOp(t)
			if doc := readCount(t, ctx, h.conn, targetID, entityID, tc.gap); doc.Reclaims != 1 {
				t.Fatalf("count document = %+v, want it untouched by a warming pass", doc)
			}

			// Warmed up, the same state fires: the gate is what defers it, not
			// the state itself.
			h.agePastWarmup()
			h.pass(ctx)
			if op := h.nextOp(t); op["operationType"] != defaultAugurOp {
				t.Fatalf("operationType = %v, want the reasoning op once the registry is warm", op["operationType"])
			}
		})
	}
}

// TestSweep_CountLegReleaseRetiresOnlyTheEscalationRecord pins the scope of the
// release's clear. Both facts a parked gap can stand at this latch — a spent
// budget, and that budget spent and handed to the reasoning tier — are raised by
// legs that know nothing about each other, so a release that cleared the key
// blind would wipe a GapBudgetExhausted its own leg never derived and flap it on
// every pass.
//
// The vector holds arm (n) back (a collapse-only gap declines the re-arm), which
// is what makes the clear observable on its own: the dispatch that would
// otherwise retire both is never made.
func TestSweep_CountLegReleaseRetiresOnlyTheEscalationRecord(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newSweepHarness(t, ctx)
	h.agePastWarmup()

	const targetID = "fixReleaseClear"
	const gap = "missing_signature"
	spec := map[string]any{
		"targetId": targetID, "lensRef": "lensFixture",
		"gaps": map[string]any{gap: map[string]any{
			"action": actionAssignTask, "operation": "SignLease",
			"assignee": "row.applicant", "target": "row.entityKey",
		}},
		"augur": map[string]any{"escalate": []any{escalateUnplannable}},
	}
	registerSpec(t, h.engine.source, spec)
	entityID := testNanoID(t)

	putStateValue(t, ctx, h.conn, countKey(targetID, entityID, gap), dispatchCount{
		Reclaims: 1, EscalatedAt: substrate.FormatTimestamp(time.Now().Add(-2 * time.Hour)),
	})
	h.putRow(t, ctx, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true,
		"applicant": "vtx.identity." + entityID,
	})
	budgetKey := issueKeyGapEntity(targetID, entityID, gap)
	h.engine.issues.set(budgetKey, "warning", "GapBudgetExhausted", "raised by the suppression leg")
	seeded, _ := issueAt(h.engine.issues, budgetKey)

	h.pass(ctx)

	h.requireNoOp(t)
	is, ok := issueAt(h.engine.issues, budgetKey)
	if !ok || is.Code != "GapBudgetExhausted" || is.Since != seeded.Since {
		t.Fatalf("issue at the gap's entity key = %+v (present=%v), want the spent-budget record standing "+
			"untouched since %s: the release retires the ESCALATION record, and nothing else raised here is "+
			"its to retire", is, ok, seeded.Since)
	}
}
