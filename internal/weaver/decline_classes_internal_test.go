package weaver

import (
	"context"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// The decline-class tests (design weaver-decline-retry-substrate-native-design.md
// §3.2). One row of that table is one decline exit of handleRow, and the table's
// classifying rule is WHERE THE FIX CAN COME FROM:
//
//   - a CONFIG error is fixed by a registry/package/template edit, which produces
//     no new delivery of the row — so the redelivery loop is the only automatic
//     uptake path and the row declines on the LONG floor, standing pending until
//     the config is fixed;
//   - a DATA error is fixed by a re-projection, which delivers on its own — so a
//     Nak buys no retry value and the row Acks with a standing Health issue as its
//     audibility.
//
// Each test asserts BOTH halves of the changed rows — the Decision and the
// severity — because a raise site owns both and a revert of either alone must be
// visible.

// rawRowMessage builds a delivery whose body is exactly the given bytes, for the
// row-3 case that h.rowMessage cannot express (it marshals a map, so it can only
// ever produce valid JSON).
func (h *handlerHarness) rawRowMessage(targetID, entityID string, body []byte, sequence uint64) substrate.Message {
	return substrate.Message{
		Subject:      h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Body:         body,
		Sequence:     sequence,
		NumDelivered: 1,
	}
}

// seedGapWithoutPlaybookTarget registers a target whose row opens a gap column
// the playbook names no entry for — §3.2 row 8's config dead-end — and returns
// the entity and the violating row that opens it.
func seedGapWithoutPlaybookTarget(t *testing.T, h *handlerHarness, targetID string) (string, map[string]any) {
	t.Helper()
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_known": {Action: actionDirectOp, Operation: "SendReminder"},
		},
	})
	entityID := testNanoID(t)
	return entityID, map[string]any{
		"entityKey":       "vtx.leaseApp." + entityID,
		"violating":       true,
		"missing_unknown": true,
	}
}

// TestHandleRow_GapWithoutPlaybookAcksWithAStandingWarning is §3.2 row 8, and it
// is the row the table deliberately holds OUT of the long-Nak config class.
//
// The fix-path rule that puts a config error on the long floor assumes a
// playbook edit is coming. For this exit it need not be: a package may project a
// `missing_*` column with NO gaps entry on purpose, ORing it into `violating` so
// the row stays violating without dispatching anything (packages/lease-signing
// does exactly that for missing_decision and missing_manager, and says so in the
// lens). Such a column's "fix" is a human decision, or nothing at all. A long Nak
// would park those rows for the whole human-latency window — forever, for the
// ones nothing ever closes — each holding a MaxAckPending slot and re-running the
// entire clearClosedMarks preamble every floor for a configuration that is
// already correct. That is the same argument §4.2 makes for leaving the
// unregistered-target exit at Ack.
//
// So the audibility is the standing issue, and both halves of the raise are
// asserted: the Ack, and the `warning` severity that keeps a package-authoring
// dead-end from pinning the whole component unhealthy.
func TestHandleRow_GapWithoutPlaybookAcksWithAStandingWarning(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureNoEntry8"
	entityID, row := seedGapWithoutPlaybookTarget(t, h, targetID)

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1))
	if dec != substrate.Ack {
		t.Fatalf("a column the package projects with no gaps entry has no fix that a redelivery can "+
			"take up — a long Nak parks it forever holding a pending slot — so the row must Ack "+
			"with its issue standing, got %v", dec)
	}
	h.requireNoOp(t)
	is, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_unknown"))
	if !ok {
		t.Fatalf("the config dead-end must stand as a target-scoped issue — the Ack makes the issue "+
			"the ONLY surface this fault has, issues = %+v", h.engine.issues.snapshot())
	}
	if is.Code != "GapWithoutPlaybook" {
		t.Fatalf("issue code = %q, want GapWithoutPlaybook", is.Code)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want warning: a package-authoring gap self-heals on the edit and "+
			"must degrade Weaver, never pin the whole component unhealthy", is.Severity)
	}

	// The row redelivers (a re-projection, or any later delivery): the fact is
	// re-derived and stands unmoved, still Acking.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 2, 1)); dec != substrate.Ack {
		t.Fatalf("every delivery of an orphaned column takes the same exit, got %v", dec)
	}
	again, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_unknown"))
	if !ok || again.Since != is.Since {
		t.Fatalf("the standing fact must keep its arrival stamp across deliveries: since %q -> %q "+
			"(standing=%v)", is.Since, again.Since, ok)
	}
}

// TestHandleRow_ConfigErrorDeclinesOnTheLongFloor covers §3.2's two long-floor
// config-class rows — 10 (TemplateDataError) and 11 (PlaybookConfigError). Each
// must decline on the long floor so the package edit that fixes it is picked up
// with no rebuild and no re-projection, and each must raise its fact at
// `warning`: the condition self-heals on that edit and Weaver goes on dispatching
// every other target while it stands, which Contract #5 §5.2 separates from
// `unhealthy` ("cannot fulfil its primary responsibility").
func TestHandleRow_ConfigErrorDeclinesOnTheLongFloor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("row 10: the gap's template resolves null against the row", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureTemplate10"
		entityID, row := seedTemplateFaultTarget(t, h, targetID)

		dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1))
		if dec != substrate.NakWithLongDelay {
			t.Fatalf("a template fault has a fix path — a template edit — that delivers nothing, "+
				"so it declines on the long floor, got %v", dec)
		}
		h.requireNoOp(t)
		is, ok := issueAt(h.engine.issues, issueKeyTemplateEntity(targetID, entityID, templateFaultGap))
		if !ok {
			t.Fatalf("the template fault must stand at its own per-row key, issues = %+v",
				h.engine.issues.snapshot())
		}
		if is.Code != "TemplateDataError" || is.Severity != "warning" {
			t.Fatalf("issue = (%q, %q), want (TemplateDataError, warning)", is.Code, is.Severity)
		}
	})

	t.Run("row 11: the playbook's action is un-dispatchable", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureConfig11"
		// A candidates-only gap on a non-planned target: buildPlan has no case
		// for the empty action, which is planGap's errConfig arm.
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps: map[string]GapAction{
				"missing_z": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "SendReminder"}}},
			},
		})
		entityID := testNanoID(t)
		row := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_z": true}

		dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1))
		if dec != substrate.NakWithLongDelay {
			t.Fatalf("an un-dispatchable action is a config error: it must decline on the long floor, got %v", dec)
		}
		h.requireNoOp(t)
		is, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_z"))
		if !ok {
			t.Fatalf("the config fault must stand as a target-scoped issue, issues = %+v",
				h.engine.issues.snapshot())
		}
		if is.Code != "PlaybookConfigError" {
			t.Fatalf("issue code = %q, want PlaybookConfigError", is.Code)
		}
		if is.Severity != "warning" {
			t.Fatalf("severity = %q, want warning: an un-dispatchable action self-heals on the package "+
				"edit and must not pin the whole component unhealthy", is.Severity)
		}
	})
}

// TestHandleRow_UnreadableBodyRaisesAndRetires covers §3.2 row 3. An unreadable
// body is a DATA error: only a re-projection can fix it and that re-projection
// delivers on its own, so the row Acks — and the delta over a bare log line is
// the standing Health entry, raised and retired by the one read that decides the
// fact.
//
// The clear is asserted on the STAMP, not on membership. `since` is only ever
// minted for a key that carries none, so a re-raise carrying a NEW stamp is the
// only observable that distinguishes "the entry was retired and re-arose" from
// "the entry stood the whole time" — a membership assertion greens either way.
func TestHandleRow_UnreadableBodyRaisesAndRetires(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureBadBody"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "SendReminder"}},
	})
	entityID := testNanoID(t)
	key := issueKeyDataEntity(targetID, entityID, rowBodyColumn)

	if dec := h.engine.handleRow(ctx, h.rawRowMessage(targetID, entityID, []byte("{not json"), 1)); dec != substrate.Ack {
		t.Fatalf("an unreadable body is a data error — its fix is a re-projection, which delivers "+
			"on its own — so the row must Ack, got %v", dec)
	}
	first, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("an unreadable body must raise a standing RowDataError at its own synthetic column, "+
			"issues = %+v", h.engine.issues.snapshot())
	}
	if first.Code != "RowDataError" || first.Severity != "warning" {
		t.Fatalf("issue = (%q, %q), want (RowDataError, warning)", first.Code, first.Severity)
	}

	// The same unreadable body again: one continuing fault, dated from its arrival.
	if dec := h.engine.handleRow(ctx, h.rawRowMessage(targetID, entityID, []byte("{not json"), 2)); dec != substrate.Ack {
		t.Fatalf("the redelivery must Ack too, got %v", dec)
	}
	second, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the standing fault must survive a redelivery of the same unreadable body")
	}
	if second.Since != first.Since {
		t.Fatalf("since moved %q -> %q across a delivery of the SAME unrepaired row", first.Since, second.Since)
	}

	// A body that parses IS the repair: the same read that raised the fact
	// retires it.
	repaired := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": false}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, repaired, 3, 1)); dec != substrate.Ack {
		t.Fatalf("a repaired non-violating row must Ack, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("a body that parses must retire the unreadable-body fault, still standing: %+v", is)
	}

	// The retirement is real, not merely a gap in the assertions: the fault
	// re-arriving carries a FRESH stamp, which only a genuinely retired key can.
	if dec := h.engine.handleRow(ctx, h.rawRowMessage(targetID, entityID, []byte("{not json"), 4)); dec != substrate.Ack {
		t.Fatalf("the re-broken row must Ack, got %v", dec)
	}
	third, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("the fault must re-raise once the body breaks again")
	}
	if third.Since == first.Since {
		t.Fatalf("since = %q on both sides of a repair: the clear never ran, so the re-raise inherited "+
			"the original arrival stamp and a fresh fault reads as weeks old", third.Since)
	}
}

// TestHandleRow_DataErrorsAckWithTheirIssue pins §3.2's fix-path rule at its
// boundary: the rows the table deliberately LEAVES at Ack. Each of these is a
// data error whose only fix is a re-projection — which delivers on its own — so
// holding a pending slot for it buys nothing, and the standing issue is the
// audibility.
func TestHandleRow_DataErrorsAckWithTheirIssue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("a gap column carrying a non-bool", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureNonBoolCol"
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "SendReminder"}},
		})
		entityID := testNanoID(t)
		row := map[string]any{
			"entityKey": "vtx.leaseApp." + entityID,
			"violating": true,
			"missing_x": "yes",
		}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
			t.Fatalf("a non-bool column is a data error: only a re-projection fixes it, and that "+
				"delivers — the row must Ack, got %v", dec)
		}
		h.requireNoOp(t)
		is, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "missing_x"))
		if !ok {
			t.Fatalf("the non-bool read must stand as a per-row RowDataError, issues = %+v",
				h.engine.issues.snapshot())
		}
		if is.Code != "RowDataError" || is.Severity != "warning" {
			t.Fatalf("issue = (%q, %q), want (RowDataError, warning)", is.Code, is.Severity)
		}
	})

	t.Run("a violating row with no entityKey echo", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureNoEcho"
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps:     map[string]GapAction{"missing_x": {Action: actionDirectOp, Operation: "SendReminder"}},
		})
		entityID := testNanoID(t)
		row := map[string]any{"violating": true, "missing_x": true}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
			t.Fatalf("a missing entityKey echo is a data error: the row must Ack, got %v", dec)
		}
		h.requireNoOp(t)
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "entityKey")); !ok {
			t.Fatalf("the missing echo must stand as a per-row RowDataError, issues = %+v",
				h.engine.issues.snapshot())
		}
	})

	t.Run("a disabled target's row acks whatever its gaps say", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureDisabled"
		entityID, row := seedTemplateFaultTarget(t, h, targetID)
		h.engine.disabled.set(targetID, true)

		// The identical row on an ENABLED target declines on the long floor
		// (row 10), so the Ack below is the freeze deciding, not an inert row.
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
			t.Fatalf("a disabled target's rows must Ack at the dispatch skip regardless of what their "+
				"gaps would have decided — a Nak loop buys nothing during a freeze, got %v", dec)
		}
		h.requireNoOp(t)
		if _, ok := issueAt(h.engine.issues, issueKeyTemplateEntity(targetID, entityID, templateFaultGap)); ok {
			t.Fatalf("a disabled target's row never reaches the dispatch leg, so it raises no plan fault")
		}
	})
}

// TestHandleRow_AggregatesDeclinesByShortestRetry pins the per-gap aggregation
// (§3.2: `Nak > NakWithDelay > NakWithLongDelay > Ack`). One message carries the
// whole row, so the row's gaps must agree on one disposition, and the aggregate
// is the SHORTEST retry any gap asked for — a gap wanting prompt re-evaluation
// must not wait out another gap's config-error floor.
//
// Both switches feeding the accumulators have a `default:` arm, and no
// exhaustiveness linter is enabled, so a Decision with no case there is silently
// downgraded to Ack. These cases are what make that visible.
func TestHandleRow_AggregatesDeclinesByShortestRetry(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("a config-error gap alone yields the long floor", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureAggLong"
		entityID, row := seedTemplateFaultTarget(t, h, targetID)
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.NakWithLongDelay {
			t.Fatalf("the row's only decline is a config-class error, so the row's disposition is the "+
				"long floor — an Ack here means the dispatch switch has no case for it, got %v", dec)
		}
	})

	t.Run("a transient gap alongside a config-error gap wins", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureAggMixed"
		// missing_ref names an uninstalled pattern (the transient class);
		// templateFaultGap's subject template resolves null against this row
		// (the config class, row 10).
		h.seedPattern("onboardFlow", testNanoID(t))
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps: map[string]GapAction{
				"missing_ref":    {Action: actionTriggerLoom, Pattern: "neverInstalled", Subject: "row.entityKey"},
				templateFaultGap: {Action: actionTriggerLoom, Pattern: "onboardFlow", Subject: "row.applicant"},
			},
		})
		entityID := testNanoID(t)
		row := map[string]any{
			"entityKey":      "vtx.leaseApp." + entityID,
			"violating":      true,
			"missing_ref":    true,
			templateFaultGap: true,
		}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.NakWithDelay {
			t.Fatalf("a transient decline must not be paced at another gap's config floor, got %v", dec)
		}
		// Both facts stand: the aggregation picks one disposition, never one fault.
		if _, ok := issueAt(h.engine.issues, issueKeyTemplateEntity(targetID, entityID, templateFaultGap)); !ok {
			t.Fatalf("the template fault must still be raised, issues = %+v", h.engine.issues.snapshot())
		}
		if _, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_ref")); !ok {
			t.Fatalf("the unresolved reference must still be raised, issues = %+v", h.engine.issues.snapshot())
		}
	})

	t.Run("an exhausted gap's escalation decline reaches the aggregate", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureAggExh"
		const gap = "missing_x"
		// The exhausted-escalation switch is the second accumulator feed. Its
		// escalated dispatch is planned through the same planGap, so a config
		// fault in the augur block reaches the same long floor. The target is
		// seeded past the registry, which is what lets an `augur.op` that is not
		// a literal operationType exist at all — the arm's job is that the
		// decline planGap returns is carried to the row's disposition rather
		// than falling through `default:` to Ack.
		h.seedTarget(&Target{
			TargetID: targetID,
			Gaps:     map[string]GapAction{gap: {Action: actionDirectOp, Operation: "SendReminder"}},
			Augur:    &AugurPolicy{Escalate: []string{escalateExhausted}, Op: "row.notALiteralOp"},
		})
		h.engine.source.mu.Lock()
		h.engine.source.targetOwner[targetID] = testNanoID(t)
		h.engine.source.mu.Unlock()

		entityID := testNanoID(t)
		if _, err := h.engine.marks.incrementDispatchCount(ctx, targetID, entityID, gap); err != nil {
			t.Fatalf("seed dispatch count: %v", err)
		}
		row := map[string]any{
			"entityKey":    "vtx.leaseApp." + entityID,
			"violating":    true,
			gap:            true,
			"maxretries_x": 1,
			"inflight_x":   false,
		}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.NakWithLongDelay {
			t.Fatalf("the escalation's config fault must carry through the exhausted-gap switch to the "+
				"row's disposition, got %v", dec)
		}
		h.requireNoOp(t)
	})
}

// seedUndispatchableGapTarget registers a target whose single gap column IS
// named by the playbook but resolves to no dispatchable action — planGap's
// errConfig arm, raising PlaybookConfigError at the target-scoped
// issueKeyGapConfig. The column being playbook-declared is what puts it in
// markCandidateColumns even when a row stops reporting it, which is the read the
// §3.5 narrowing turns on.
func seedUndispatchableGapTarget(t *testing.T, h *handlerHarness, targetID string) {
	t.Helper()
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			"missing_z": {Candidates: []GapCandidate{{Action: actionDirectOp, Operation: "SendReminder"}}},
		},
	})
}

// TestClearClosedMarks_ConfigLatchNarrowedToWellFormedReads pins §3.5's
// narrowing of the target-scoped `gapConfig:` clear, and the two reads it has to
// tell apart. boolColumn answers false for three different reads, and only ONE
// of them is not evidence that the gap closed:
//
//	column absent/nil       | false | closure — a row that stopped reporting the column closed it
//	column present, `false` | false | closure
//	column present, non-bool| false | NOT closure — the read says nothing about the fact
//
// The clear is therefore gated on the read being WELL-FORMED, never on the value
// having literally been a bool: gating on the latter would stop retiring the
// latch for a column that DISAPPEARS from the projection, and this site is the
// only clear such a column can reach (nothing else walks the candidate set), so
// the issue would stand for the process's life.
func TestClearClosedMarks_ConfigLatchNarrowedToWellFormedReads(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("a non-bool column read does not clear the latch", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureNarrowBad"
		seedUndispatchableGapTarget(t, h, targetID)
		entityID := testNanoID(t)
		key := issueKeyGapConfig(targetID, "missing_z")

		open := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_z": true}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.NakWithLongDelay {
			t.Fatalf("setup: the config fault must decline on the long floor, got %v", dec)
		}
		raised, ok := issueAt(h.engine.issues, key)
		if !ok {
			t.Fatalf("setup: the config latch must stand, issues = %+v", h.engine.issues.snapshot())
		}

		// The same row re-projected with the column carrying a non-bool. The
		// gap does not re-open (a non-bool reads as not actionable), so nothing
		// re-raises in this pass — a latch found standing afterwards was never
		// cleared, not cleared-and-re-raised.
		broken := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_z": "yes"}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, broken, 2, 1)); dec != substrate.Ack {
			t.Fatalf("a non-bool gap column is a data error: the row Acks, got %v", dec)
		}
		still, ok := issueAt(h.engine.issues, key)
		if !ok {
			t.Fatalf("a column read that is not the §10.2 bool says nothing about whether the gap "+
				"closed, so it must not retire the TARGET-scoped config latch — a repeatedly "+
				"re-projecting broken row would otherwise clear it at its projection rate; "+
				"issues = %+v", h.engine.issues.snapshot())
		}
		if still.Since != raised.Since {
			t.Fatalf("since moved %q -> %q: the latch was retired and re-raised behind the assertion",
				raised.Since, still.Since)
		}
		// The positive vector for the read itself: the non-bool DID raise its own
		// per-row data error, so the pass genuinely reached the column.
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "missing_z")); !ok {
			t.Fatalf("setup: the non-bool read must raise its own RowDataError, issues = %+v",
				h.engine.issues.snapshot())
		}
	})

	t.Run("a column that disappears from the projection clears the latch", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureNarrowGone"
		seedUndispatchableGapTarget(t, h, targetID)
		entityID := testNanoID(t)
		key := issueKeyGapConfig(targetID, "missing_z")

		open := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, "missing_z": true}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, open, 1, 1)); dec != substrate.NakWithLongDelay {
			t.Fatalf("setup: the config fault must decline on the long floor, got %v", dec)
		}
		if _, ok := issueAt(h.engine.issues, key); !ok {
			t.Fatalf("setup: the config latch must stand, issues = %+v", h.engine.issues.snapshot())
		}

		// The lens stops projecting the column at all. This site is the ONLY
		// clear that observes a column leaving, so if it declines to act the
		// latch stands forever.
		gone := map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": false}
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, gone, 2, 1)); dec != substrate.Ack {
			t.Fatalf("a non-violating row Acks, got %v", dec)
		}
		if is, ok := issueAt(h.engine.issues, key); ok {
			t.Fatalf("a row that stopped reporting the column closed it, and this is the only clear "+
				"that can observe a column leaving — the latch must be retired, still standing: %+v", is)
		}
	})
}

// TestClearClosedMarks_ConfigLatchSelfHealsAcrossEntities is §3.5's flap, pinned
// end to end. The `gapConfig:` latch is TARGET-scoped while the clear that
// retires it fires per ENTITY, so one entity's column closing retires a fact
// another entity's row is still producing — and the design's answer is that the
// still-open row re-raises it within one redelivery floor rather than the clear
// being narrowed away (which would strand the latch of a column that simply
// stops being reported).
//
// Every step is asserted on the STAMP as well as on membership, and the two
// surfaces say different things here. The latch's own membership flaps — it is
// retired and re-raised — while the stamp does NOT move, because
// PlaybookConfigError raises through alertPaced, whose arrival memory survives
// the clear on purpose: a clear this key sees is routinely another entity's
// close rather than a repair, so a stamp minted from the latch would report a
// week-old config fault as seconds old on essentially every pass.
func TestClearClosedMarks_ConfigLatchSelfHealsAcrossEntities(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureLatchFlap"
	const gap = "missing_z"
	seedUndispatchableGapTarget(t, h, targetID)
	key := issueKeyGapConfig(targetID, gap)
	entityA, entityB := testNanoID(t), testNanoID(t)
	openRow := func(entityID string) map[string]any {
		return map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": true, gap: true}
	}
	closedRow := func(entityID string) map[string]any {
		return map[string]any{"entityKey": "vtx.leaseApp." + entityID, "violating": false, gap: false}
	}

	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityA, openRow(entityA), 1, 1)); dec != substrate.NakWithLongDelay {
		t.Fatalf("A's open gap must decline on the long floor, got %v", dec)
	}
	arrival, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("A's open gap must raise the target-scoped latch, issues = %+v", h.engine.issues.snapshot())
	}

	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityB, openRow(entityB), 2, 1)); dec != substrate.NakWithLongDelay {
		t.Fatalf("B's open gap must decline on the long floor too, got %v", dec)
	}
	if second, ok := issueAt(h.engine.issues, key); !ok || second.Since != arrival.Since {
		t.Fatalf("a second entity raising the SAME target-scoped fact is a continuation, not an "+
			"arrival: since %q -> %q (standing=%v)", arrival.Since, second.Since, ok)
	}

	// A's gap closes. The clear is target-scoped, so it retires a fact B's row
	// is still producing — the flap the design accepts.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityA, closedRow(entityA), 3, 1)); dec != substrate.Ack {
		t.Fatalf("A's closed row Acks, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("A's column closing must retire the target-scoped latch, still standing: %+v", is)
	}

	// B's next delivery — which the long floor bounds at one floor away — puts
	// the fact straight back, dated from its ORIGINAL arrival: the pace memory
	// the stamp comes from is untouched by a clear.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityB, openRow(entityB), 4, 1)); dec != substrate.NakWithLongDelay {
		t.Fatalf("B's still-open gap must decline on the long floor, got %v", dec)
	}
	reraised, ok := issueAt(h.engine.issues, key)
	if !ok {
		t.Fatalf("B's still-open gap must re-raise the latch within one floor, issues = %+v",
			h.engine.issues.snapshot())
	}
	if reraised.Since != arrival.Since {
		t.Fatalf("since moved %q -> %q across the flap: A's close is not evidence the config fault "+
			"ended, so the re-raise must carry the ORIGINAL arrival — a stamp re-minted here would "+
			"reset roughly once a pass and report a standing fault as newborn",
			arrival.Since, reraised.Since)
	}

	// B closes too: the last row producing the fact is gone, so the retirement
	// is final and no further delivery re-raises it.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityB, closedRow(entityB), 5, 1)); dec != substrate.Ack {
		t.Fatalf("B's closed row Acks, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("the last open row closing must retire the latch for good, still standing: %+v", is)
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityA, closedRow(entityA), 6, 1)); dec != substrate.Ack {
		t.Fatalf("A's closed row Acks, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("nothing may re-raise a retired latch once every row has closed the column: %+v", is)
	}
}
