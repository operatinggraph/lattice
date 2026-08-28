package weaver

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// --- Fixtures ----------------------------------------------------------------

// seedOwnedTarget seeds a Target directly AND records its owning meta vertex,
// so augurEscalation (which resolves the full vtx.meta.<id> key) works for a
// target the test constructs by hand rather than through the registry's
// validating load path.
func seedOwnedTarget(t *testing.T, h *handlerHarness, target *Target) {
	t.Helper()
	h.seedTarget(target)
	h.engine.source.mu.Lock()
	h.engine.source.targetOwner[target.TargetID] = testNanoID(t)
	h.engine.source.mu.Unlock()
}

// noPlaybookRow is a violating row whose one open gap column the playbook does
// not name — the §3.2 row-8 decline (GapWithoutPlaybook).
func noPlaybookRow(entityID string) map[string]any {
	return map[string]any{
		"entityKey": "vtx.leaseapp." + entityID,
		"violating": true,
		"missing_a": true,
	}
}

// --- T3: the four changed decline classes ------------------------------------

// TestHandleRow_GapWithoutPlaybookRidesLongFloor is §3.2 row 8. A violating
// column the playbook does not name is a CONFIG error: the only fix is a package
// re-author, which projects no new row, so an Ack would strand the row violating
// for the life of the durable. The row is held on the long redelivery floor
// instead, and the standing issue is a `warning` — the fault degrades Weaver,
// it does not stop it fulfilling its responsibility for every other target
// (Contract #5 §5.2).
func TestHandleRow_GapWithoutPlaybookRidesLongFloor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureLongNoPlaybook"
	h.seedTarget(&Target{TargetID: targetID})
	entityID := testNanoID(t)

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, noPlaybookRow(entityID), 1, 1))
	if dec != substrate.NakWithLongDelay {
		t.Fatalf("a gap with no playbook entry must ride the long floor, got %v", dec)
	}
	h.requireNoOp(t)
	is, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_a"))
	if !ok {
		t.Fatalf("expected a standing GapWithoutPlaybook, issues = %+v", h.engine.issues.snapshot())
	}
	if is.Code != "GapWithoutPlaybook" {
		t.Fatalf("code = %q, want GapWithoutPlaybook", is.Code)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want \"warning\": the Nak loop re-raises this fact for as long as it holds, "+
			"and an `error` over a standing fact pins the whole component unhealthy while it dispatches "+
			"normally for every other target", is.Severity)
	}
}

// TestPlanGap_PlaybookConfigErrorRidesLongFloor is §3.2 row 11: an action the
// deployment cannot dispatch. Same class and same severity argument as row 8 —
// only a package re-author fixes it, and that produces no new row delivery.
func TestPlanGap_PlaybookConfigErrorRidesLongFloor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureLongConfigError"
	h.seedTarget(&Target{
		TargetID: targetID,
		// An action the plan builder cannot name: the errConfig arm.
		Gaps: map[string]GapAction{"missing_a": {Action: "notAnAction"}},
	})
	entityID := testNanoID(t)

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, noPlaybookRow(entityID), 1, 1))
	if dec != substrate.NakWithLongDelay {
		t.Fatalf("an un-dispatchable action must ride the long floor, got %v", dec)
	}
	h.requireNoOp(t)
	is, ok := issueAt(h.engine.issues, issueKeyGapConfig(targetID, "missing_a"))
	if !ok {
		t.Fatalf("expected a standing PlaybookConfigError, issues = %+v", h.engine.issues.snapshot())
	}
	if is.Code != "PlaybookConfigError" {
		t.Fatalf("code = %q, want PlaybookConfigError", is.Code)
	}
	if is.Severity != "warning" {
		t.Fatalf("severity = %q, want \"warning\": a standing `error` maps to unhealthy over the whole "+
			"issue set, for one target's authoring fault", is.Severity)
	}
}

// TestHandleRow_UnparseableBodyRaisesAndClears is §3.2 row 3. The row Acks (only
// a re-projection can fix it, and that revision supersedes this one and delivers
// on its own), but the fact is no longer lost to a log line: it stands at the
// row's synthetic body column until a later revision of the SAME row parses.
//
// The key is per-ENTITY, so two broken rows are two independent facts and
// repairing one does not retire the other.
func TestHandleRow_UnparseableBodyRaisesAndClears(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureBadBody"
	h.seedTarget(&Target{TargetID: targetID, Gaps: map[string]GapAction{"missing_a": {Action: actionSurface}}})
	first, second := testNanoID(t), testNanoID(t)

	broken := func(entityID string) substrate.Message {
		return substrate.Message{
			Subject:      h.engine.rowSubjectPrefix + targetID + "." + entityID,
			Body:         []byte("{not json"),
			Sequence:     1,
			NumDelivered: 1,
		}
	}
	for _, entityID := range []string{first, second} {
		if dec := h.engine.handleRow(ctx, broken(entityID)); dec != substrate.Ack {
			t.Fatalf("an unparseable body is a data error and must Ack, got %v", dec)
		}
	}
	for _, entityID := range []string{first, second} {
		is, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, rowBodyColumn))
		if !ok {
			t.Fatalf("an unparseable body must raise a standing RowDataError for entity %s, issues = %+v",
				entityID, h.engine.issues.snapshot())
		}
		if is.Code != "RowDataError" || is.Severity != "warning" {
			t.Fatalf("issue = %+v, want a warning RowDataError", is)
		}
	}

	// The repair: a later revision of the FIRST row parses. Its own entry
	// retires; the sibling's must not.
	repaired := map[string]any{"entityKey": "vtx.leaseapp." + first, "violating": false}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, first, repaired, 2, 1)); dec != substrate.Ack {
		t.Fatalf("a parseable non-violating row must Ack, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, first, rowBodyColumn)); ok {
		t.Fatalf("the successful parse must retire the body error, still standing as %+v", is)
	}
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, second, rowBodyColumn)); !ok {
		t.Fatalf("one row's repair must not retire a sibling row's body error, issues = %+v",
			h.engine.issues.snapshot())
	}
}

// TestHandleRow_UnparseableBodyKeyCannotCollideWithAProjectedColumn is the latch
// guard on the synthetic column. boolColumn raises into the SAME `data:` family
// for whatever column its caller passes, so a lens projecting a column literally
// named "body" would share one latch with the parse error — and this test's
// second half is what that sharing would break: the parse-success clear would
// falsely retire the projected column's own standing RowDataError.
func TestHandleRow_UnparseableBodyKeyCannotCollideWithAProjectedColumn(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	if !strings.HasPrefix(rowBodyColumn, "__") {
		t.Fatalf("rowBodyColumn = %q: the synthetic column must carry the reserved `__` prefix, "+
			"which is the only thing keeping it out of the projected column namespace", rowBodyColumn)
	}
	if strings.ContainsAny(substrate.Alphabet, "_") {
		t.Fatalf("substrate.Alphabet contains '_' — the reserved `__` segment may now collide with a " +
			"NanoID-derived entityId; escalate as a structural finding")
	}

	const targetID = "fixtureBodyColumnCollision"
	h.seedTarget(&Target{TargetID: targetID})
	entityID := testNanoID(t)

	// A row that DOES project a column called "body", malformed as a bool so
	// boolColumn latches it at data:<target>.<entity>.body.
	h.engine.boolColumn(targetID, entityID, map[string]any{"body": "not a bool"}, "body")
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "body")); !ok {
		t.Fatalf("setup: the projected column's own RowDataError must stand, issues = %+v",
			h.engine.issues.snapshot())
	}

	// The same row's body then fails to parse, and then parses again. Neither
	// the raise nor its clear may touch the projected column's latch.
	broken := substrate.Message{
		Subject: h.engine.rowSubjectPrefix + targetID + "." + entityID,
		Body:    []byte("{not json"), Sequence: 1, NumDelivered: 1,
	}
	if dec := h.engine.handleRow(ctx, broken); dec != substrate.Ack {
		t.Fatalf("an unparseable body must Ack, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "body")); !ok || is.Message == "" {
		t.Fatalf("the parse error must not overwrite a projected `body` column's own latch, issues = %+v",
			h.engine.issues.snapshot())
	}
	parseable := map[string]any{"entityKey": "vtx.leaseapp." + entityID, "violating": false, "body": "not a bool"}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, parseable, 2, 1)); dec != substrate.Ack {
		t.Fatalf("a parseable non-violating row must Ack, got %v", dec)
	}
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "body")); !ok {
		t.Fatalf("the parse-success clear retired the PROJECTED column's RowDataError — the two facts "+
			"share a latch, issues = %+v", h.engine.issues.snapshot())
	}
	if is, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, rowBodyColumn)); ok {
		t.Fatalf("the parse-success clear must retire the synthetic entry, still standing as %+v", is)
	}
}

// TestHandleRow_UnparseableBodyEntryEndsByEveryLeg walks the OTHER routes by
// which the body fact ends. The successful parse is the only live-path
// retirement, so a row whose last projected body was unparseable must still be
// retired when the ENTITY is tombstoned or when the TARGET leaves — both of
// which reach it only because the entry sits inside the `data:` prefix family.
func TestHandleRow_UnparseableBodyEntryEndsByEveryLeg(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	broken := func(h *handlerHarness, targetID, entityID string) substrate.Message {
		return substrate.Message{
			Subject: h.engine.rowSubjectPrefix + targetID + "." + entityID,
			Body:    []byte("{not json"), Sequence: 1, NumDelivered: 1,
		}
	}

	t.Run("the entity's deletion tombstone", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureBodyTombstone"
		h.seedTarget(&Target{TargetID: targetID})
		entityID := testNanoID(t)
		if dec := h.engine.handleRow(ctx, broken(h, targetID, entityID)); dec != substrate.Ack {
			t.Fatalf("setup: an unparseable body must Ack, got %v", dec)
		}
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, rowBodyColumn)); !ok {
			t.Fatalf("setup: the body error must stand, issues = %+v", h.engine.issues.snapshot())
		}
		tombstone := substrate.Message{
			Subject:  h.engine.rowSubjectPrefix + targetID + "." + entityID,
			Sequence: 2, NumDelivered: 1,
		}
		if dec := h.engine.handleRow(ctx, tombstone); dec != substrate.Ack {
			t.Fatalf("a tombstone must Ack, got %v", dec)
		}
		if is, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, rowBodyColumn)); ok {
			t.Fatalf("a deleted entity's body error describes a row that no longer exists and must retire, "+
				"still standing as %+v", is)
		}
	})

	t.Run("the target's teardown prefixes", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureBodyTeardown"
		h.seedTarget(&Target{TargetID: targetID})
		entityID := testNanoID(t)
		if dec := h.engine.handleRow(ctx, broken(h, targetID, entityID)); dec != substrate.Ack {
			t.Fatalf("setup: an unparseable body must Ack, got %v", dec)
		}
		// The exact prefix set Revoke and the reconcileConsumers removal walk.
		for _, prefix := range issueKeyTargetPrefixes(targetID) {
			h.engine.issues.clearPrefix(prefix)
		}
		if is, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, rowBodyColumn)); ok {
			t.Fatalf("a target teardown must carry the body error away with the rest of its `data:` family, "+
				"still standing as %+v", is)
		}
	})
}

// TestHandleRow_FixPathRuleBoundary pins the classes §3.2 deliberately leaves at
// Ack, which is what keeps the long floor to the config classes. A DATA error's
// fix necessarily arrives as a re-projection, and the fresh revision supersedes
// any pending state and delivers on its own — so Nak'ing one would buy no retry
// value and hold a pending slot for nothing. A DISABLED target owes nothing at
// all while it is frozen.
func TestHandleRow_FixPathRuleBoundary(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Run("a non-bool violating column", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureBoundaryNonBool"
		h.seedTarget(&Target{TargetID: targetID, Gaps: map[string]GapAction{"missing_a": {Action: actionSurface}}})
		entityID := testNanoID(t)
		row := map[string]any{"entityKey": "vtx.leaseapp." + entityID, "violating": "yes", "missing_a": true}

		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
			t.Fatalf("a row-data error must Ack — its fix is a re-projection, which delivers on its own; got %v", dec)
		}
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "violating")); !ok {
			t.Fatalf("the data error must still be raised, issues = %+v", h.engine.issues.snapshot())
		}
	})

	t.Run("a violating row with no entityKey echo", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureBoundaryNoEcho"
		h.seedTarget(&Target{TargetID: targetID, Gaps: map[string]GapAction{"missing_a": {Action: actionSurface}}})
		entityID := testNanoID(t)
		row := map[string]any{"violating": true, "missing_a": true}

		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.Ack {
			t.Fatalf("a missing entityKey echo must Ack, got %v", dec)
		}
		if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, entityID, "entityKey")); !ok {
			t.Fatalf("the anchor data error must still be raised, issues = %+v", h.engine.issues.snapshot())
		}
	})

	t.Run("a disabled target's rows, whatever their class", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureBoundaryDisabled"
		h.seedTarget(&Target{TargetID: targetID})
		h.engine.disabled.set(targetID, true)
		entityID := testNanoID(t)

		// The same row that rides the long floor when the target is enabled.
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, noPlaybookRow(entityID), 1, 1)); dec != substrate.Ack {
			t.Fatalf("a frozen target's rows must Ack regardless of class — a Nak loop buys nothing "+
				"during a freeze, and Resume redelivers the already-pending set anyway; got %v", dec)
		}
	})
}

// TestHandleRow_RetryPrecedenceAcrossGaps pins the tail's collapse of the three
// accumulators. A message carries ONE decision for the whole row, so the
// shortest floor any gap asked for wins: a gap needing the transient retry must
// not wait out another gap's config-error floor.
func TestHandleRow_RetryPrecedenceAcrossGaps(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixturePrecedence"
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			// An unresolvable pattern: the transient (5 s) class.
			"missing_transient": {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"},
			// An un-dispatchable action: the config (long) class.
			"missing_config": {Action: "notAnAction"},
		},
	})
	entityID := testNanoID(t)
	base := map[string]any{"entityKey": "vtx.leaseapp." + entityID, "violating": true}

	longOnly := map[string]any{"entityKey": base["entityKey"], "violating": true, "missing_config": true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, longOnly, 1, 1)); dec != substrate.NakWithLongDelay {
		t.Fatalf("a row whose only declining gap is a config error must take the long floor, got %v", dec)
	}

	both := map[string]any{
		"entityKey": base["entityKey"], "violating": true,
		"missing_config": true, "missing_transient": true,
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, both, 2, 1)); dec != substrate.NakWithDelay {
		t.Fatalf("NakWithDelay outranks NakWithLongDelay — a transient gap must not wait out a config "+
			"gap's floor; got %v", dec)
	}
	h.requireNoOp(t)
}

// TestHandleRow_ExhaustedBranchCarriesTheLongFloor exercises the OTHER
// aggregation switch. The exhausted-gap escalation is reached from the
// suppression arm, above the dispatch arm, and it has its own switch over the
// returned Decision — a value that falls through its `default` is silently
// downgraded to Ack, with nothing to redeliver the row.
func TestHandleRow_ExhaustedBranchCarriesTheLongFloor(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureExhaustedLong"
	const col = "missing_a"
	// The target escalates "exhausted" to the AI tier, but its augur op is not a
	// literal operationType — so the escalation's own plan build is a config
	// error, and the escalation arm returns the long floor.
	seedOwnedTarget(t, h, &Target{
		TargetID: targetID,
		Gaps:     map[string]GapAction{col: {Action: actionDirectOp, Operation: "SendReminder"}},
		Augur:    &AugurPolicy{Escalate: []string{escalateExhausted}, Op: "row.notALiteral"},
	})
	entityID := testNanoID(t)
	// One declared attempt, already spent: the suppression arm's exhausted term.
	h.engine.bumpDispatchCount(ctx, targetID, entityID, col)
	row := map[string]any{
		"entityKey": "vtx.leaseapp." + entityID, "violating": true,
		col: true, "maxretries_a": 1,
	}

	dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1))
	if dec != substrate.NakWithLongDelay {
		t.Fatalf("the exhausted-escalation arm must carry its plan's long floor out of handleRow, got %v", dec)
	}
	h.requireNoOp(t)
}

// --- T8: the gapConfig latch self-heals --------------------------------------

// TestClearClosedMarks_ConfigLatchSelfHeals is §3.5. The `gapConfig:` latch is
// TARGET-scoped but retired per ENTITY, so one entity closing its column retires
// a fact another entity's still-open row is still causing. That over-eager clear
// is accepted precisely because the Nak loop makes it self-healing: the still-open
// row's next delivery re-raises within one floor. Once EVERY entity has closed,
// the retirement is final and no delivery re-raises it.
func TestClearClosedMarks_ConfigLatchSelfHeals(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureSelfHeaLatch"
	const col = "missing_a"
	h.seedTarget(&Target{TargetID: targetID})
	a, b := testNanoID(t), testNanoID(t)
	key := issueKeyGapConfig(targetID, col)

	for _, entityID := range []string{a, b} {
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, noPlaybookRow(entityID), 1, 1)); dec != substrate.NakWithLongDelay {
			t.Fatalf("setup: entity %s must ride the long floor, got %v", entityID, dec)
		}
	}
	if _, ok := issueAt(h.engine.issues, key); !ok {
		t.Fatalf("setup: the target-scoped config latch must stand, issues = %+v", h.engine.issues.snapshot())
	}

	closed := func(entityID string) map[string]any {
		return map[string]any{"entityKey": "vtx.leaseapp." + entityID, "violating": false, "missing_a": false}
	}

	// A closes: the target-scoped latch retires, even though B still holds the
	// column open.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, a, closed(a), 2, 1)); dec != substrate.Ack {
		t.Fatalf("a closing row must Ack, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("the per-entity close retires the target-scoped latch, still standing as %+v", is)
	}

	// B's next redelivery — which the long floor guarantees — re-raises it.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, b, noPlaybookRow(b), 3, 2)); dec != substrate.NakWithLongDelay {
		t.Fatalf("B's redelivery must still ride the long floor, got %v", dec)
	}
	if _, ok := issueAt(h.engine.issues, key); !ok {
		t.Fatalf("a still-open row's redelivery must re-raise the latch — that re-raise is what makes the "+
			"over-eager clear self-healing; issues = %+v", h.engine.issues.snapshot())
	}

	// B closes too: the retirement is final, and a further delivery of either
	// closed row does not bring it back.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, b, closed(b), 4, 1)); dec != substrate.Ack {
		t.Fatalf("B's closing row must Ack, got %v", dec)
	}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, a, closed(a), 5, 1)); dec != substrate.Ack {
		t.Fatalf("A's re-delivered closed row must Ack, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("with every entity closed nothing re-raises the latch; it must stay retired, got %+v", is)
	}
}

// TestClearClosedMarks_UnreadableColumnDoesNotClearTheConfigLatch is the §3.5
// narrowing. A column carrying a PRESENT non-bool value is not evidence the gap
// closed — it is evidence the row cannot be read — so it must not retire a fact
// scoped to the whole target. Without the narrowing a single broken row clears
// the latch at its own projection rate, re-stamping the `since` of a config
// fault every other row is still raising.
//
// The column is one the playbook NAMES, because that is the shape where the
// distinction bites: such a column is a mark candidate on every delivery, so a
// broken row reaches the clear on each of them, and a row that drops the column
// entirely reaches it as a genuine retraction.
func TestClearClosedMarks_UnreadableColumnDoesNotClearTheConfigLatch(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureLatchNarrowing"
	const col = "missing_a"
	// An unresolvable pattern: a target-scoped config fault at the gapConfig
	// latch, raised by every delivery of an open row.
	h.seedTarget(&Target{
		TargetID: targetID,
		Gaps: map[string]GapAction{
			col: {Action: actionTriggerLoom, Pattern: "ghostFlow", Subject: "row.entityKey"},
		},
	})
	a, b := testNanoID(t), testNanoID(t)
	key := issueKeyGapConfig(targetID, col)

	open := map[string]any{"entityKey": "vtx.leaseapp." + a, "violating": true, col: true}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, a, open, 1, 1)); dec != substrate.NakWithDelay {
		t.Fatalf("setup: an unresolved reference is the transient class, got %v", dec)
	}
	if _, ok := issueAt(h.engine.issues, key); !ok {
		t.Fatalf("setup: the config latch must stand, issues = %+v", h.engine.issues.snapshot())
	}

	// B projects the column as a non-bool: unreadable, not closed.
	unreadable := map[string]any{"entityKey": "vtx.leaseapp." + b, "violating": true, col: "maybe"}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, b, unreadable, 2, 1)); dec != substrate.Ack {
		t.Fatalf("a row-data error must Ack, got %v", dec)
	}
	if _, ok := issueAt(h.engine.issues, key); !ok {
		t.Fatalf("an unreadable column is not evidence of closure and must not retire the target-scoped "+
			"latch A's still-open row is causing; issues = %+v", h.engine.issues.snapshot())
	}
	// The narrowing is scoped to that one clear: B's own per-row data error is
	// raised exactly as before.
	if _, ok := issueAt(h.engine.issues, issueKeyDataEntity(targetID, b, col)); !ok {
		t.Fatalf("the unreadable column must still raise its own per-row data error, issues = %+v",
			h.engine.issues.snapshot())
	}

	// An ABSENT column is the §10.2 retraction shape and DOES still retire it —
	// the narrowing must not swallow the one live retirement this leg owns.
	retracted := map[string]any{"entityKey": "vtx.leaseapp." + b, "violating": false}
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, b, retracted, 3, 1)); dec != substrate.Ack {
		t.Fatalf("a retracting row must Ack, got %v", dec)
	}
	if is, ok := issueAt(h.engine.issues, key); ok {
		t.Fatalf("a row that stopped reporting the column closed it; the config latch must still retire "+
			"on that, got %+v", is)
	}
}

// --- §3.6: the per-target bound on the per-row issue families -----------------

// TestIssueCache_PerRowFamiliesAreBoundedPerTarget pins §3.6's map-level cap.
// The heartbeat DOCUMENT was already bounded; what was not is the in-memory map
// and the per-heartbeat sort over it, both of which grow with the LENS — one
// entry per malformed row — so a systemically-broken large target would grow
// them without limit.
func TestIssueCache_PerRowFamiliesAreBoundedPerTarget(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetA, targetB = "targetA", "targetB"

	entity := func(i int) string { return "e" + strconv.Itoa(i) }
	for i := 0; i < rowIssueCapPerTarget+25; i++ {
		c.set(issueKeyDataEntity(targetA, entity(i), "violating"), "warning", "RowDataError", "bad")
	}
	if got := len(c.issues); got != rowIssueCapPerTarget+1 {
		t.Fatalf("cache holds %d entries, want the cap %d plus one overflow entry", got, rowIssueCapPerTarget+1)
	}
	overflow, ok := issueAt(c, issueKeyRowIssuesCapped(targetA))
	if !ok {
		t.Fatalf("past the cap the refusals must be surfaced as one entry per target, issues = %d", len(c.issues))
	}
	if overflow.Code != rowIssuesCappedCode {
		t.Fatalf("overflow code = %q, want %q", overflow.Code, rowIssuesCappedCode)
	}
	if !strings.Contains(overflow.Message, "25 further raises") {
		t.Fatalf("the overflow entry must count the refusals it stands for, got %q", overflow.Message)
	}

	// The cap is per TARGET: a second target is unaffected by the first's flood.
	c.set(issueKeyDataEntity(targetB, entity(0), "violating"), "warning", "RowDataError", "bad")
	if _, ok := issueAt(c, issueKeyDataEntity(targetB, entity(0), "violating")); !ok {
		t.Fatalf("one target reaching its cap must not refuse another target's first per-row issue")
	}

	// Refreshing an ALREADY-tracked key is never refused: the cap bounds how many
	// distinct facts are tracked, never how current a tracked one is.
	c.set(issueKeyDataEntity(targetA, entity(0), "violating"), "warning", "RowDataError", "refreshed")
	if is, _ := issueAt(c, issueKeyDataEntity(targetA, entity(0), "violating")); is.Message != "refreshed" {
		t.Fatalf("a tracked key's refresh was refused, got %q", is.Message)
	}

	// Clearing back under the cap readmits, and retires the overflow entry: the
	// untracked facts are level-driven and re-raise on their next delivery, so
	// there is no backlog for a carried counter to describe.
	c.clear(issueKeyDataEntity(targetA, entity(0), "violating"))
	if _, ok := issueAt(c, issueKeyRowIssuesCapped(targetA)); ok {
		t.Fatalf("under the cap again, the overflow entry must retire")
	}
	c.set(issueKeyDataEntity(targetA, entity(9999), "violating"), "warning", "RowDataError", "readmitted")
	if _, ok := issueAt(c, issueKeyDataEntity(targetA, entity(9999), "violating")); !ok {
		t.Fatalf("a freed slot must be readmitted")
	}
}

// TestIssueCache_CapCountsTheTwoPerRowFamiliesAndNothingElse pins WHICH keys the
// budget covers. Only the two families keyed per (target, entity, column) grow
// with the lens; a target-scoped fact — a config latch, the overflow entry
// itself — must never consume a per-row slot, or a flood of row errors would
// start refusing the very entries an operator needs in front of them.
func TestIssueCache_CapCountsTheTwoPerRowFamiliesAndNothingElse(t *testing.T) {
	t.Parallel()
	const targetID = "targetShapes"
	entityID := "eNTTYnanoidSEGMENTx2"

	perRow := []string{
		issueKeyDataEntity(targetID, entityID, "violating"),
		issueKeyDataEntity(targetID, entityID, rowBodyColumn),
		issueKeyTemplateEntity(targetID, entityID, "missing_a"),
	}
	for _, key := range perRow {
		got, ok := rowIssueTarget(key)
		if !ok || got != targetID {
			t.Fatalf("rowIssueTarget(%q) = (%q, %v), want (%q, true)", key, got, ok, targetID)
		}
	}
	notPerRow := []string{
		issueKeyGapConfig(targetID, "missing_a"),
		issueKeyGapEntity(targetID, entityID, "missing_a"),
		issueKeyRowIssuesCapped(targetID),
		issueKeyConsumer(targetID),
		issueKeyEffect(targetID, "missing_a", actionDirectOp),
	}
	for _, key := range notPerRow {
		if got, ok := rowIssueTarget(key); ok {
			t.Fatalf("rowIssueTarget(%q) = (%q, true), want not-a-per-row-key", key, got)
		}
	}
}

// TestIssueCache_CapLeavesTheDocumentBoundHonest is the dossier's own guard: the
// map-level cap must not make boundIssues' total lie. Every entry the cache
// holds — the overflow entry included, which declares its own count — reaches
// aggregateStatus and the listing cut exactly as before.
func TestIssueCache_CapLeavesTheDocumentBoundHonest(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "targetHonest"
	for i := 0; i < rowIssueCapPerTarget+3; i++ {
		c.set(issueKeyDataEntity(targetID, "e"+strconv.Itoa(i), "violating"), "warning", "RowDataError", "bad")
	}
	// One error-severity fact, which must survive both bounds.
	c.set(issueKeyGapConfig(targetID, "missing_a"), "error", "ConsumerReconcileError", "loud")

	snap := c.snapshot()
	if got, want := len(snap), rowIssueCapPerTarget+2; got != want {
		t.Fatalf("snapshot has %d entries, want %d (the cap, the overflow entry, the error)", got, want)
	}
	if aggregateStatus("running", snap) != "unhealthy" {
		t.Fatalf("the status is computed over every entry the cache holds; an error must still escalate it")
	}
	bounded := boundIssues(snap, maxHeartbeatIssues)
	if len(bounded) != maxHeartbeatIssues+1 {
		t.Fatalf("bounded listing has %d entries, want %d", len(bounded), maxHeartbeatIssues+1)
	}
	truncation := bounded[len(bounded)-1]
	if truncation.Code != issuesTruncatedCode {
		t.Fatalf("last entry = %q, want the truncation entry", truncation.Code)
	}
	if !strings.Contains(truncation.Message, strconv.Itoa(len(snap))+" open in total") {
		t.Fatalf("the truncation entry must state the true count of what the cache holds, got %q",
			truncation.Message)
	}
	if bounded[0].Severity != "error" {
		t.Fatalf("severity-first selection must survive the cap, got %q", bounded[0].Severity)
	}
}

// TestIssueCache_PrefixClearFreesPerRowSlots pins the cap's teardown legs. The
// per-row families are retired wholesale by prefix — an entity's deletion
// tombstone, a target's Revoke or registry removal — and each of those must give
// the slots back, or a target that has churned through enough entities would
// refuse every new row issue forever.
func TestIssueCache_PrefixClearFreesPerRowSlots(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "targetPrefixFree"
	for i := 0; i < rowIssueCapPerTarget+1; i++ {
		c.set(issueKeyDataEntity(targetID, "e"+strconv.Itoa(i), "violating"), "warning", "RowDataError", "bad")
	}
	if _, ok := issueAt(c, issueKeyRowIssuesCapped(targetID)); !ok {
		t.Fatalf("setup: the overflow entry must stand")
	}

	// One entity's tombstone clear frees exactly its own slot.
	c.clearPrefix(issuePrefixData + targetID + ".e0.")
	if _, ok := issueAt(c, issueKeyRowIssuesCapped(targetID)); ok {
		t.Fatalf("one freed slot puts the target back under the cap; the overflow entry must retire")
	}

	// The target's teardown frees the rest.
	for _, prefix := range issueKeyTargetPrefixes(targetID) {
		c.clearPrefix(prefix)
	}
	if len(c.issues) != 0 {
		t.Fatalf("a target teardown must leave nothing standing, got %d entries", len(c.issues))
	}
	if len(c.rowIssues) != 0 || len(c.refused) != 0 {
		t.Fatalf("a target teardown must leave no slot accounting behind, rowIssues=%v refused=%v",
			c.rowIssues, c.refused)
	}
	for i := 0; i < rowIssueCapPerTarget; i++ {
		c.set(issueKeyDataEntity(targetID, "f"+strconv.Itoa(i), "violating"), "warning", "RowDataError", "bad")
	}
	if got := len(c.issues); got != rowIssueCapPerTarget {
		t.Fatalf("after a teardown the target's whole budget must be available again, tracked %d", got)
	}
}

// --- §6: the lane-1 consumer envelope ----------------------------------------

// TestTargetSpec_DeclinePendingEnvelope pins the two envelope fields the decline
// loop depends on. A Nak'd row holds its MaxAckPending slot continuously, so the
// cap is no longer "what is momentarily in flight" but "how large a stuck
// population a target may carry before NEW entities stall" — and the long floor
// is what paces the whole stuck set's redelivery.
func TestTargetSpec_DeclinePendingEnvelope(t *testing.T) {
	t.Parallel()
	e := NewEngine(nil, Config{ActorKey: "vtx.identity.WeaverServiceActor1abc", Logger: discardLogger()})
	spec := e.targetSpec("someTarget")
	if spec.MaxAckPending != laneMaxAckPending {
		t.Fatalf("MaxAckPending = %d, want %d: left unset, the durable takes the server's 1000 default and "+
			"a stuck population stalls new entities far earlier than intended",
			spec.MaxAckPending, laneMaxAckPending)
	}
	if spec.LongRedeliveryDelay != substrate.DefaultLongRedeliveryDelay {
		t.Fatalf("LongRedeliveryDelay = %v, want the %v default: left unset, every config-error decline "+
			"would fall back to the substrate default rather than the engine's configured floor",
			spec.LongRedeliveryDelay, substrate.DefaultLongRedeliveryDelay)
	}
}

// TestConfig_LongRedeliveryDelayDefaultsAndClamps pins the knob's end-to-end
// path, mirroring the sweep intervals': zero takes the package default, and a
// value that would invert the two floors is warned about and clamped rather than
// silently honoured.
func TestConfig_LongRedeliveryDelayDefaultsAndClamps(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		configured time.Duration
		want       time.Duration
	}{
		{"zero takes the package default", 0, substrate.DefaultLongRedeliveryDelay},
		{"negative takes the package default", -time.Second, substrate.DefaultLongRedeliveryDelay},
		{"below the transient floor clamps up to it", substrate.DefaultRedeliveryDelay / 2, substrate.DefaultRedeliveryDelay},
		{"above the transient floor is honoured", 42 * time.Second, 42 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Logger: discardLogger(), LongRedeliveryDelay: tc.configured}
			cfg.withDefaults()
			if cfg.LongRedeliveryDelay != tc.want {
				t.Fatalf("LongRedeliveryDelay = %v, want %v", cfg.LongRedeliveryDelay, tc.want)
			}
		})
	}
}
