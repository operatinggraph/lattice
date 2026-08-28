package weaver

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
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

// TestHandleRow_GapWithoutPlaybookIsLogPaced is the log-volume side of the same
// change. Making this raise STANDING makes it re-derive once per long floor per
// stuck row, for as long as the playbook is wrong — so a seam that logs every
// call at Error would write one ERROR line per stuck row per floor, forever, for
// a condition this severity is `warning`. It belongs on the same paced seam its
// two siblings at this key already use.
func TestHandleRow_GapWithoutPlaybookIsLogPaced(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixturePacedNoPlaybook"
	h.seedTarget(&Target{TargetID: targetID})
	entityID := testNanoID(t)

	logs := &logCapture{}
	h.engine.logger = slog.New(logs)
	for i, seq := range []uint64{1, 2, 3} {
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, noPlaybookRow(entityID), seq, uint64(i+1))); dec != substrate.NakWithLongDelay {
			t.Fatalf("delivery %d = %v, want the long floor", i+1, dec)
		}
	}
	levels := logs.levelsContaining("the playbook defines no gaps entry")
	if len(levels) != 3 {
		t.Fatalf("every re-derivation must still emit a record (never dropped, only lowered), got %v", levels)
	}
	if levels[0] != slog.LevelWarn {
		t.Fatalf("the arrival must be loud at the raise's own severity, got %v — an Error record for a "+
			"`warning` fact contradicts the demotion in the same breath", levels[0])
	}
	for i, lvl := range levels[1:] {
		if lvl != slog.LevelDebug {
			t.Fatalf("re-derivation %d = %v, want Debug: one loud record per pace interval per key, or a "+
				"stuck target writes one line per row per floor forever", i+2, lvl)
		}
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

	// The invariant is the READER WHITELIST, not the name. Refractor's output
	// descriptor accepts any non-blank bodyColumn, so a lens may legally project
	// a column called `__body`; what makes the key un-collidable is that a column
	// segment only reaches the `data:` family by being PASSED to a reader, and
	// every call site passes either an engine constant or a gap column — and a
	// gap column is either one of the row's own `missing_*` keys or a playbook
	// gaps key, which validateTarget rejects unless it matches the same
	// convention. So a lens-authored name reaches this family only when it starts
	// with `missing_`.
	if strings.HasPrefix(rowBodyColumn, gapColumnPrefix) {
		t.Fatalf("rowBodyColumn = %q is %s-shaped, which IS the one lens-authored shape that reaches this "+
			"issue family — pick a name outside it", rowBodyColumn, gapColumnPrefix)
	}
	for _, engineColumn := range []string{
		"violating", "entityKey", freshUntilColumn, admissionPriorityColumn,
		inflightColumnPrefix, maxretriesColumnPrefix,
	} {
		if rowBodyColumn == engineColumn || strings.HasPrefix(rowBodyColumn, engineColumn) {
			t.Fatalf("rowBodyColumn = %q collides with the engine column %q, which a reader already passes "+
				"into this family", rowBodyColumn, engineColumn)
		}
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

// TestHandleRow_ConfigErrorGapDoesNotRePublishASiblingsLiveEpisode is the
// interaction the long floor introduces, and the one it must not pay for. A row
// carries two gaps: A dispatched and its episode is in flight; B has no playbook
// entry, so the ROW is Nak'd every long floor and comes back forever.
//
// Every one of those redeliveries re-enters dispatchGap for A as well. A also
// suppresses nothing — it declares no inflight_ companion, and the re-fire path
// never bumps the dispatch count, so no retry cap is ever reached — so without
// the early anti-storm drop A's op would be re-published once per floor for as
// long as B's playbook stayed wrong. Those collapse on the Contract #4 tracker
// only inside its 24h TTL; a playbook fault outlives that, and then each one is
// a genuine second execution.
func TestHandleRow_ConfigErrorGapDoesNotRePublishASiblingsLiveEpisode(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	h := newHandlerHarness(t, ctx)

	const targetID = "fixtureMixedRow"
	// missing_a dispatches (a bare directOp — no inflight_/maxretries_
	// companions, exactly the clinicSiteBackfill shape); missing_b has no
	// playbook entry at all.
	//
	// The target also declares an admission budget, because the drop's placement
	// decides a second cost: a token is drawn inside planGap, so a marked gap
	// planned on every redelivery would burn one per cycle too.
	h.seedTarget(&Target{
		TargetID:  targetID,
		Admission: &AdmissionPolicy{GlobalRate: 100},
		Gaps:      map[string]GapAction{"missing_a": {Action: actionDirectOp, Operation: "FixA"}},
	})
	entityID := testNanoID(t)
	row := map[string]any{
		"entityKey": "vtx.leaseapp." + entityID,
		"violating": true,
		"missing_a": true,
		"missing_b": true,
	}

	// First delivery: A dispatches, B declines, so the ROW rides the long floor.
	if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, 1)); dec != substrate.NakWithLongDelay {
		t.Fatalf("the config-error gap must hold the row on the long floor, got %v", dec)
	}
	first := h.nextOp(t)
	if first["operationType"] != "FixA" {
		t.Fatalf("expected the dispatchable gap to fire, got %v", first["operationType"])
	}
	if _, _, inFlight, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a"); err != nil || !inFlight {
		t.Fatalf("setup: the dispatched gap must hold a mark (err=%v inFlight=%v)", err, inFlight)
	}
	admittedAfterDispatch, _ := h.engine.admission.metrics()

	// The floor expires and the row comes back — twice, as it will forever until
	// the playbook is fixed. A's episode is still in flight, so nothing is
	// re-published for it; B still rides the long floor.
	for i, numDelivered := range []uint64{2, 3} {
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, row, 1, numDelivered)); dec != substrate.NakWithLongDelay {
			t.Fatalf("redelivery %d must still ride the long floor, got %v", i+1, dec)
		}
		h.requireNoOp(t)
	}
	if _, _, inFlight, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a"); err != nil || !inFlight {
		t.Fatalf("the drop must leave the live mark alone (err=%v inFlight=%v)", err, inFlight)
	}
	if admitted, _ := h.engine.admission.metrics(); admitted != admittedAfterDispatch {
		t.Fatalf("admission tokens drawn during the redeliveries: %d -> %d. A marked gap must not re-enter "+
			"planGap at all, or a stuck row costs its target one token per gap per floor forever",
			admittedAfterDispatch, admitted)
	}
}

// TestPlanGap_AdmissionDeferralDoesNotDowngradeAConfigError pins the second half
// of the admission gate's placement. A denied token is the 5 s transient class,
// and the gate now sits BELOW the build — so a row whose plan cannot build at
// all never reaches it, and keeps its own (long) class instead of collapsing
// onto a 5 s loop on any target that declares an admission budget.
func TestPlanGap_AdmissionDeferralDoesNotDowngradeAConfigError(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// A budget with no capacity at all: every admit() call denies.
	starved := &AdmissionPolicy{GlobalRate: 0.000001}

	t.Run("an un-dispatchable action keeps the long floor", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureStarvedConfig"
		h.seedTarget(&Target{
			TargetID:  targetID,
			Admission: starved,
			Gaps:      map[string]GapAction{"missing_a": {Action: "notAnAction"}},
		})
		entityID := testNanoID(t)
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, noPlaybookRow(entityID), 1, 1)); dec != substrate.NakWithLongDelay {
			t.Fatalf("a config error on an admission-paced target must keep the long floor, got %v — "+
				"a 5 s deferral here is the hot loop the design says cannot happen", dec)
		}
		h.requireNoOp(t)
	})

	t.Run("a buildable plan still defers on the transient floor", func(t *testing.T) {
		h := newHandlerHarness(t, ctx)
		const targetID = "fixtureStarvedBuildable"
		h.seedTarget(&Target{
			TargetID:  targetID,
			Admission: starved,
			Gaps:      map[string]GapAction{"missing_a": {Action: actionDirectOp, Operation: "FixA"}},
		})
		entityID := testNanoID(t)
		if dec := h.engine.handleRow(ctx, h.rowMessage(t, targetID, entityID, noPlaybookRow(entityID), 1, 1)); dec != substrate.NakWithDelay {
			t.Fatalf("an admission deferral is ordinary pacing and stays the transient class, got %v", dec)
		}
		h.requireNoOp(t)
		// No mark: the gate still prevents the dispatch, it only moved below the
		// build.
		if _, _, found, err := h.engine.marks.get(ctx, targetID, entityID, "missing_a"); err != nil || found {
			t.Fatalf("a deferred gap must claim no mark (err=%v found=%v)", err, found)
		}
	})
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

	// Clearing back under the cap readmits — but does NOT retire the overflow
	// entry. A refused `data:` fact has no other record and no way back: those
	// exits Ack, and lane 1 is DeliverLastPerSubject on a stable durable, so the
	// row is never delivered again until it re-projects. Retiring the count the
	// moment one tracked row repaired would delete the only surviving evidence
	// that the others are broken.
	c.clear(issueKeyDataEntity(targetA, entity(0), "violating"))
	c.set(issueKeyDataEntity(targetA, entity(9999), "violating"), "warning", "RowDataError", "readmitted")
	if _, ok := issueAt(c, issueKeyDataEntity(targetA, entity(9999), "violating")); !ok {
		t.Fatalf("a freed slot must be readmitted")
	}
	if _, ok := issueAt(c, issueKeyRowIssuesCapped(targetA)); !ok {
		t.Fatalf("dropping under the cap must NOT retire the overflow entry — the raises it stands for "+
			"are not re-derivable, so it is their only record; issues = %d", len(c.issues))
	}

	// It retires only when the target's per-row set drains to nothing.
	for i := 0; i < rowIssueCapPerTarget+25; i++ {
		c.clear(issueKeyDataEntity(targetA, entity(i), "violating"))
	}
	c.clear(issueKeyDataEntity(targetA, entity(9999), "violating"))
	if _, ok := issueAt(c, issueKeyRowIssuesCapped(targetA)); ok {
		t.Fatalf("with no per-row entry left for the target, the overflow entry must retire")
	}
	if _, tracked := c.rowIssues[targetA]; tracked {
		t.Fatalf("a drained target must leave no slot accounting behind, rowIssues=%v", c.rowIssues)
	}
	if len(c.refused) != 0 || len(c.refusedWorst) != 0 {
		t.Fatalf("a drained target must leave no refusal accounting behind, refused=%v worst=%v",
			c.refused, c.refusedWorst)
	}
}

// TestIssueCache_RefusedErrorSeverityReachesTheStatus is the severity leak the
// cap would otherwise open. aggregateStatus only ever sees c.issues, so a raise
// the cap REFUSES is invisible to it — and an `error` refused that way would let
// a component that cannot fulfil its responsibility report `degraded`. The two
// per-row families are warning-dominated today, but nothing constrains them:
// temporal.go's timer-payload marshal failure already raises `error` at a
// per-ENTITY data key.
func TestIssueCache_RefusedErrorSeverityReachesTheStatus(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "targetRefusedError"
	for i := 0; i < rowIssueCapPerTarget; i++ {
		c.set(issueKeyDataEntity(targetID, "e"+strconv.Itoa(i), "violating"), "warning", "RowDataError", "bad")
	}
	// Past the cap, a warning first: the overflow entry stands at its severity.
	c.set(issueKeyDataEntity(targetID, "eWarn", "violating"), "warning", "RowDataError", "bad")
	if is, _ := issueAt(c, issueKeyRowIssuesCapped(targetID)); is.Severity != "warning" {
		t.Fatalf("overflow severity = %q, want warning while only warnings were refused", is.Severity)
	}
	if got := aggregateStatus("running", c.snapshot()); got != "degraded" {
		t.Fatalf("status = %q, want degraded while only warnings are open", got)
	}

	// Now an ERROR is refused. It never enters the map, so the overflow entry is
	// the only thing that can carry it to the status.
	c.set(issueKeyDataEntity(targetID, "eErr", "violating"), "error", "RowDataError", "unmarshalable timer payload")
	if _, ok := issueAt(c, issueKeyDataEntity(targetID, "eErr", "violating")); ok {
		t.Fatalf("the cap must still refuse the insertion — admitting errors would unbound the map")
	}
	if is, _ := issueAt(c, issueKeyRowIssuesCapped(targetID)); is.Severity != "error" {
		t.Fatalf("overflow severity = %q, want error: a refused error must not be downgraded", is.Severity)
	}
	if got := aggregateStatus("running", c.snapshot()); got != "unhealthy" {
		t.Fatalf("status = %q, want unhealthy: a refused error must still escalate the document", got)
	}

	// The worst severity is sticky for as long as the overflow entry stands — a
	// later warning refusal must not walk it back down.
	c.set(issueKeyDataEntity(targetID, "eWarn2", "violating"), "warning", "RowDataError", "bad")
	if is, _ := issueAt(c, issueKeyRowIssuesCapped(targetID)); is.Severity != "error" {
		t.Fatalf("overflow severity = %q, want error: a later warning refusal must not downgrade it", is.Severity)
	}
}

// TestIssueCache_PacedMapIsBoundedPerTarget closes §3.6's other half. The pace
// clock is a second per-key map, and prunePaced — which drops entries unraised
// for two staleness windows — cannot bound it once a per-row fault is re-derived
// on a redelivery floor SHORTER than that window, which is exactly what a stuck
// TemplateDataError now is. Without its own budget the map would be sized by the
// consumer's MaxAckPending rather than by the cap the design advertises.
func TestIssueCache_PacedMapIsBoundedPerTarget(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "targetPacedBound"
	now := time.Now()
	for i := 0; i < rowIssueCapPerTarget+40; i++ {
		c.pacedRaise(issueKeyTemplateEntity(targetID, "e"+strconv.Itoa(i), "missing_a"),
			"warning", "TemplateDataError", now)
	}
	if got := len(c.paced); got != rowIssueCapPerTarget {
		t.Fatalf("pace map holds %d entries, want the cap %d", got, rowIssueCapPerTarget)
	}
	// A refused key is reported not-loud: an untracked key has no clock to tell
	// arrival from continuation, and the safe side for a map deliberately not
	// grown is the quiet one (the record is lowered to Debug, never dropped).
	loud, _ := c.pacedRaise(issueKeyTemplateEntity(targetID, "eOverflow", "missing_a"),
		"warning", "TemplateDataError", now)
	if loud {
		t.Fatalf("a refused pace key must not report loud — every raise would then log at Warn forever")
	}
	// A target-scoped key is never counted against the per-row budget, so the
	// config latch keeps pacing even under a per-row flood.
	if loud, _ := c.pacedRaise(issueKeyGapConfig(targetID, "missing_a"), "warning", "PlaybookConfigError", now); !loud {
		t.Fatalf("a target-scoped paced key must not be refused by the per-ROW budget")
	}
	// The prune returns slots, so a target whose faults age out can pace again.
	c.prunePaced(now.Add(3 * logPaceInterval))
	if len(c.paced) != 0 || len(c.rowPaced) != 0 {
		t.Fatalf("prunePaced must return every slot it drops, paced=%d rowPaced=%v", len(c.paced), c.rowPaced)
	}
	if loud, _ := c.pacedRaise(issueKeyTemplateEntity(targetID, "eOverflow", "missing_a"),
		"warning", "TemplateDataError", now.Add(3*logPaceInterval)); !loud {
		t.Fatalf("a freed pace slot must be readmitted")
	}
}

// --- The per-entity issue-family census, executable ---------------------------

// censusEntityID and its companions are the canonical triple every issue-key
// constructor is probed with below. The entity segment is NanoID-shaped and
// dot-free so the keys built from it have the real thing's segment structure.
const (
	censusTargetID  = "censusTarget"
	censusEntityID  = "eNTTYnanoidSEGMENTx2"
	censusGapColumn = "missing_census"
)

// issueKeyProbes builds one key per issue-key constructor from that triple, so
// each family can be classified by the key it actually produces rather than by
// what anyone remembered to write down. Every entry passes the argument the
// constructor's own callers pass — issueKeySweep gets a real mark key, because a
// mark key is what its one caller has in hand.
//
// The AST walk in the census below asserts this table names EVERY issueKey*
// constructor in the package. That is what makes the census self-extending: a new
// family cannot be classified by omission, because omitting it fails the walk
// with the constructor's own file:line.
var issueKeyProbes = map[string]func() string{
	"issueKeyGapEntity":       func() string { return issueKeyGapEntity(censusTargetID, censusEntityID, censusGapColumn) },
	"issueKeyDataEntity":      func() string { return issueKeyDataEntity(censusTargetID, censusEntityID, censusGapColumn) },
	"issueKeyTemplateEntity":  func() string { return issueKeyTemplateEntity(censusTargetID, censusEntityID, censusGapColumn) },
	"issueKeyGapConfig":       func() string { return issueKeyGapConfig(censusTargetID, censusGapColumn) },
	"issueKeyRowIssuesCapped": func() string { return issueKeyRowIssuesCapped(censusTargetID) },
	"issueKeyEffect":          func() string { return issueKeyEffect(censusTargetID, censusGapColumn, actionDirectOp) },
	"issueKeyConsumer":        func() string { return issueKeyConsumer(censusTargetID) },
	"issueKeyTimer":           func() string { return issueKeyTimer(censusTargetID) },
	"issueKeyPendingSpec":     func() string { return issueKeyPendingSpec(censusTargetID) },
	"issueKeySweep":           func() string { return issueKeySweep(markKey(censusTargetID, censusEntityID, censusGapColumn)) },
	"issueKeyOscillation": func() string {
		return issueKeyOscillation(censusTargetID, censusTargetID+"B", guardgrammar.Path{Field: "f"})
	},
}

// perEntityBudgetExceptions names each constructor that mints a PER-ENTITY key
// the budget deliberately does not cover, with the reason. It is not an escape
// hatch: the census asserts every entry is genuinely per-entity (so a
// target-scoped family cannot be parked here to silence it) AND genuinely
// uncovered (so an entry left behind after its family IS brought under the
// budget fails rather than rotting).
var perEntityBudgetExceptions = map[string]string{
	"issueKeySweep": "CorruptMark, raised per unparseable mark. Its key embeds the entity because a mark key does, " +
		"but its cardinality is driven by data corruption rather than by the subject count, so it does not grow " +
		"with a healthy lens the way the gap/data/template families do. Bringing it under the budget would also " +
		"need a `sweep:` prefix in issueKeyTargetPrefixes first — the target teardown does not walk it today, so " +
		"a revoked target's entries would hold slots nothing releases.",
}

// TestCensusPerEntityIssueFamiliesAreBudgeted is the executable census behind
// §3.6's bound, and it exists because the same class of miss has now happened
// three times: a per-entity issue family ships, the budget's predicate is not
// extended to it, and nothing fails.
//
// The predicate (rowIssueTarget) decides which keys count against a target's
// per-row cap, and the facts that must count are exactly the ones whose
// cardinality grows with the SUBJECT count — one entry per (entity, column) — so
// that a systemically-broken lens cannot grow the in-memory map and the
// per-heartbeat sort over it without limit.
//
// The classification is DERIVED, twice over, which is the whole point. Which
// constructors exist comes from parsing the package, not from a list here — so a
// family added tomorrow is caught by its own absence from the probe table rather
// than by nobody noticing. And whether each one is per-entity comes from the key
// it actually builds (does it carry the entity segment?), not from its name or
// its parameter names — so a constructor that takes an entity-ish string under
// another name is classified by what it emits.
//
// A failure here names the constructor and its file:line, because the next author
// needs to know which family to act on, not that a count moved.
func TestCensusPerEntityIssueFamiliesAreBudgeted(t *testing.T) {
	t.Parallel()

	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list internal/weaver sources: %v", err)
	}
	fset := token.NewFileSet()
	found := map[string]string{} // constructor name -> file:line
	scanned := 0
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		scanned++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "issueKey") {
				continue
			}
			// A constructor mints ONE key and returns it. The prefix helpers
			// (issueKeyTargetPrefixes) return a slice and are teardown inputs, not
			// keys, so the return type is what separates them — a future prefix
			// helper is excluded for the same structural reason rather than by
			// name.
			if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
				continue
			}
			if ident, isIdent := fn.Type.Results.List[0].Type.(*ast.Ident); !isIdent || ident.Name != "string" {
				continue
			}
			found[fn.Name.Name] = path + ":" + strconv.Itoa(fset.Position(fn.Pos()).Line)
		}
	}
	if scanned == 0 {
		t.Fatalf("the scan found no non-test sources to read — this census would pass vacuously")
	}
	if len(found) == 0 {
		t.Fatalf("the scan found no issueKey* constructors in %d source file(s) — the walk is broken, not the "+
			"package; this census would pass vacuously", scanned)
	}

	// Completeness, both directions: the probe table is the census's only way to
	// build a key for a family, so a constructor missing from it is unclassified
	// (not "fine"), and a probe for a constructor that no longer exists is a
	// stale entry that would keep asserting about nothing.
	for name, where := range found {
		if _, probed := issueKeyProbes[name]; !probed {
			t.Errorf("%s (%s) is an issue-key constructor with no probe in issueKeyProbes, so this census "+
				"cannot classify it. Add one that calls it the way its own callers do. If the key it builds "+
				"carries the entity segment, rowIssueTarget must match it too — a per-entity family outside "+
				"the budget grows the issue map and the per-heartbeat sort with the subject count", name, where)
		}
	}
	for name := range issueKeyProbes {
		if _, exists := found[name]; !exists {
			t.Errorf("issueKeyProbes names %q, which the package no longer declares — a stale probe asserts "+
				"about a family that is gone", name)
		}
	}

	// The classification itself.
	for name, where := range found {
		probe, ok := issueKeyProbes[name]
		if !ok {
			continue // already reported above
		}
		key := probe()
		perEntity := strings.Contains(key, censusEntityID)
		_, budgeted := rowIssueTarget(key)
		reason, excepted := perEntityBudgetExceptions[name]

		switch {
		case perEntity && !budgeted && !excepted:
			t.Errorf("%s (%s) builds the per-entity key %q, but rowIssueTarget does not count it against the "+
				"target's per-row budget. One entry per (entity, column) grows with the subject count, so an "+
				"uncovered family is unbounded in the issues map and in snapshot()'s per-heartbeat sort. "+
				"Add its prefix to rowIssueTarget — the release legs (clear, clearPrefix, the target teardown) "+
				"are already shape-driven — or declare it in perEntityBudgetExceptions with the reason",
				name, where, key)
		case !perEntity && budgeted:
			t.Errorf("%s (%s) builds the target-scoped key %q, but rowIssueTarget counts it against the per-row "+
				"budget. A flood of row faults would then start refusing the very entries that explain them",
				name, where, key)
		case excepted && !perEntity:
			t.Errorf("%s (%s) is declared in perEntityBudgetExceptions but builds the target-scoped key %q — "+
				"the exception is for a per-entity family the budget deliberately skips, not a parking space "+
				"for anything that fails the check. Reason on file: %s", name, where, key, reason)
		case excepted && budgeted:
			t.Errorf("%s (%s) is declared in perEntityBudgetExceptions but rowIssueTarget now covers %q. The "+
				"exception is stale — delete it, or the next reader believes a bounded family is unbounded. "+
				"Reason on file: %s", name, where, key, reason)
		}

		// Counting a family and RELEASING it are two obligations, and a family
		// that gains the first without the second turns the cap into a one-way
		// ratchet: a revoked target's entries would hold slots nothing ever gives
		// back, and the target would eventually refuse every raise. The per-key
		// legs (clear, clearPrefix) are shape-driven and need nothing, but the
		// TARGET teardown walks an explicit prefix list, so a budgeted family
		// must appear in it.
		if !budgeted {
			continue
		}
		reachable := false
		for _, prefix := range issueKeyTargetPrefixes(censusTargetID) {
			if strings.HasPrefix(key, prefix) {
				reachable = true
				break
			}
		}
		if !reachable {
			t.Errorf("%s (%s) builds %q, which rowIssueTarget counts against the per-row budget, but no prefix "+
				"in issueKeyTargetPrefixes matches it — a revoke or a registry removal would drop the target's "+
				"entries from nothing and leave their budget slots held forever. Add the family's prefix there "+
				"in the same change that adds it to rowIssueTarget", name, where, key)
		}
	}
}

// TestIssueCache_GapFamilyIsBoundedAndReleased walks the `gap:` family through
// the budget end to end, because being counted is only half of belonging to it:
// an entry that takes a slot and never gives it back turns the cap into a
// one-way ratchet that eventually refuses everything for the target.
//
// The vectors are the two retirement legs the family actually uses — the
// per-gap clear a closing gap runs (retireClosedGapIssues) and the per-target
// prefix teardown a revoke runs — plus the refusal itself.
func TestIssueCache_GapFamilyIsBoundedAndReleased(t *testing.T) {
	t.Parallel()
	c := newIssueCache()
	const targetID = "targetGapBudget"

	for i := 0; i < rowIssueCapPerTarget; i++ {
		c.set(issueKeyGapEntity(targetID, "A"+strconv.Itoa(i), "missing_a"), "warning", "UnroutedTasks", "open")
	}
	if got := c.rowIssues[targetID]; got != rowIssueCapPerTarget {
		t.Fatalf("tracked per-row entries = %d, want the full cap %d", got, rowIssueCapPerTarget)
	}
	// Past the cap the raise is refused and folded into the one overflow entry,
	// exactly as a data/template raise is.
	c.set(issueKeyGapEntity(targetID, "Aoverflow", "missing_a"), "warning", "UnroutedTasks", "open")
	if _, tracked := c.issues[issueKeyGapEntity(targetID, "Aoverflow", "missing_a")]; tracked {
		t.Fatalf("a gap: raise past the cap must be refused, not tracked")
	}
	if _, ok := c.issues[issueKeyRowIssuesCapped(targetID)]; !ok {
		t.Fatalf("a refused gap: raise must maintain the target's overflow entry")
	}

	// A closing gap returns its slot, so a target whose rows repair can track
	// fresh facts again.
	c.clear(issueKeyGapEntity(targetID, "A0", "missing_a"))
	if got := c.rowIssues[targetID]; got != rowIssueCapPerTarget-1 {
		t.Fatalf("a gap: clear must return its slot, tracked = %d", got)
	}
	c.set(issueKeyGapEntity(targetID, "Areadmit", "missing_a"), "warning", "UnroutedTasks", "open")
	if _, tracked := c.issues[issueKeyGapEntity(targetID, "Areadmit", "missing_a")]; !tracked {
		t.Fatalf("a freed slot must be readmitted")
	}

	// The target's teardown returns every slot and retires the overflow entry
	// with them — the entry sits in the data: family, so the prefix walk that
	// carries it is issueKeyTargetPrefixes', not this family's own.
	for _, prefix := range issueKeyTargetPrefixes(targetID) {
		c.clearPrefix(prefix)
	}
	if got, ok := c.rowIssues[targetID]; ok || got != 0 {
		t.Fatalf("a target teardown must drop the whole per-row budget, tracked = %d (present=%v)", got, ok)
	}
	if _, ok := c.issues[issueKeyRowIssuesCapped(targetID)]; ok {
		t.Fatalf("a target teardown must retire the overflow entry with the entries it counted")
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
	// One error-severity fact, TRACKED and inside the CAPPED family — the vector
	// an error placed in an uncapped family would not exercise at all. Raised
	// first so it holds a slot; the flood behind it then fills the rest and
	// overflows.
	//
	// The entity segments deliberately sort BEFORE the overflow key's reserved
	// `__capped` segment (an uppercase letter precedes '_'), which is the shape a
	// real NanoID entity id takes — so key order alone would bury the marker
	// behind all five hundred of them.
	c.set(issueKeyDataEntity(targetID, "A1", freshUntilColumn), "error", "RowDataError", "loud")
	for i := 1; i < rowIssueCapPerTarget+3; i++ {
		c.set(issueKeyDataEntity(targetID, "A"+strconv.Itoa(i), "violating"), "warning", "RowDataError", "bad")
	}

	snap := c.snapshot()
	if got, want := len(snap), rowIssueCapPerTarget+1; got != want {
		t.Fatalf("snapshot has %d entries, want %d (the cap's worth of tracked entries, the error among "+
			"them, plus one overflow entry)", got, want)
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
	// The overflow entry must survive the DOCUMENT cut too. It is a warning in
	// the same family as the hundreds of warnings that caused it, so severity
	// alone would sort it among them in key order and truncate away the one entry
	// saying "N further rows are broken and are not tracked".
	if !hasIssueCode(bounded, rowIssuesCappedCode) {
		t.Fatalf("the cache-overflow marker must be pinned into the listed set; listed codes = %v",
			listedCodes(bounded))
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

	// One entity's tombstone clear frees exactly its own slot — and leaves the
	// overflow entry standing, because the raises it stands for are still lost.
	c.clearPrefix(issuePrefixData + targetID + ".e0.")
	if _, ok := issueAt(c, issueKeyRowIssuesCapped(targetID)); !ok {
		t.Fatalf("one freed slot must not retire the overflow entry")
	}
	c.set(issueKeyDataEntity(targetID, "eFresh", "violating"), "warning", "RowDataError", "bad")
	if _, ok := issueAt(c, issueKeyDataEntity(targetID, "eFresh", "violating")); !ok {
		t.Fatalf("a tombstone must return its entity's slot to the budget")
	}

	// The target's teardown frees the rest.
	for _, prefix := range issueKeyTargetPrefixes(targetID) {
		c.clearPrefix(prefix)
	}
	if len(c.issues) != 0 {
		t.Fatalf("a target teardown must leave nothing standing, got %d entries", len(c.issues))
	}
	if len(c.rowIssues) != 0 || len(c.refused) != 0 || len(c.refusedWorst) != 0 {
		t.Fatalf("a target teardown must leave no slot accounting behind, rowIssues=%v refused=%v worst=%v",
			c.rowIssues, c.refused, c.refusedWorst)
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

// TestLaneMaxAckPending_StaysUnderTheServersDeferThreshold pins the cap against
// the server behaviour that chose it.
//
// nats-server walks the WHOLE pending map on every redelivery-timer fire, at any
// size; the `len(o.pending) > 1024` test inside that walk gates only an early
// BAIL — past it, any inbound ack/nak/+WPI makes checkPending abandon the scan,
// throw away the expiries it had already collected, and reschedule 100 ms out. A
// consumer that is both large and continuously acking can keep re-entering that
// regime. Under the server's own 1000 default the branch is unreachable, because
// getNextMsg stalls new deliveries once the pending set reaches MaxAckPending, so
// raising this cap past 1024 would make lane 1 the only consumer in the
// deployment that can get there.
func TestLaneMaxAckPending_StaysUnderTheServersDeferThreshold(t *testing.T) {
	t.Parallel()
	// The literal is the server's, not ours: consumer.go's `check := len(o.pending) > 1024`.
	const serverPendingWalkDeferThreshold = 1024
	if laneMaxAckPending > serverPendingWalkDeferThreshold {
		t.Fatalf("laneMaxAckPending = %d exceeds the server's %d pending-walk defer threshold: the pending "+
			"set could then exceed it, and lane 1 would be the only consumer able to enter the "+
			"abandon-and-retry redelivery regime — in a band where ConsumerSaturated is still silent",
			laneMaxAckPending, serverPendingWalkDeferThreshold)
	}
	if laneMaxAckPending <= 0 {
		t.Fatalf("laneMaxAckPending = %d: a non-positive value is not passed to the durable at all, "+
			"silently restoring the server's 1000 default", laneMaxAckPending)
	}
}

// TestHeartbeat_SaturatedLaneConsumerRaisesAnError is the wedged-target signal.
// One mis-authored gap column parks every violating row of its target in the
// pending set; at the cap, getNextMsg stops handing out NEW deliveries for that
// target entirely, so entities that appear from then on are never evaluated.
// Nothing else in the platform observes that — the per-consumer health sink
// reports the durable as running throughout.
func TestHeartbeat_SaturatedLaneConsumerRaisesAnError(t *testing.T) {
	t.Parallel()
	const targetID = "fixtureSaturated"
	name := laneConsumerPrefix + targetID
	states := map[string]string{name: "running", "weaver-sweep": "running"}

	newHB := func(pending uint64) *heartbeater {
		h := &heartbeater{
			issues:                 newIssueCache(),
			logger:                 discardLogger(),
			consumerPausedSince:    map[string]string{},
			consumerSaturatedSince: map[string]string{},
			ackStats:               stubAckStats{pending: pending},
		}
		return h
	}

	// Below the cap: no issue, but the gradient is exposed as a metric so an
	// operator can see a target filling up before it wedges.
	metrics := map[string]any{}
	if got := newHB(laneMaxAckPending-1).saturatedIssues(context.Background(), states, time.Now(), metrics); len(got) != 0 {
		t.Fatalf("below the cap the consumer is not wedged; want no issue, got %+v", got)
	}
	pending, ok := metrics["laneAckPending"].(map[string]uint64)
	if !ok || pending[targetID] != uint64(laneMaxAckPending-1) {
		t.Fatalf("the raw ack-pending count must be exposed as a metric, got %+v", metrics["laneAckPending"])
	}

	// At the cap: an error-severity, target-naming issue.
	hb := newHB(laneMaxAckPending)
	issues := hb.saturatedIssues(context.Background(), states, time.Now(), map[string]any{})
	if len(issues) != 1 {
		t.Fatalf("a saturated lane-1 durable must raise exactly one issue, got %+v", issues)
	}
	if issues[0].Code != "ConsumerSaturated" {
		t.Fatalf("code = %q, want ConsumerSaturated", issues[0].Code)
	}
	if issues[0].Severity != "error" {
		t.Fatalf("severity = %q, want error: at the cap this target's deliveries have STOPPED — that is "+
			"not the self-healing degradation the config-error classes describe", issues[0].Severity)
	}
	if !strings.Contains(issues[0].Message, targetID) {
		t.Fatalf("the issue must name its target, got %q", issues[0].Message)
	}
	if aggregateStatus("healthy", issues) != "unhealthy" {
		t.Fatalf("a wedged target must escalate the document status")
	}

	// It is built inline from live state, so it needs no teardown leg: a target
	// that leaves simply stops appearing, and the since map drains with it.
	if got := hb.saturatedIssues(context.Background(), map[string]string{}, time.Now(), map[string]any{}); len(got) != 0 {
		t.Fatalf("a consumer no longer present must stop being reported, got %+v", got)
	}
	if len(hb.consumerSaturatedSince) != 0 {
		t.Fatalf("the since map must drain with the consumer, got %v", hb.consumerSaturatedSince)
	}

	// A paused consumer is reported by pausedIssues, not twice.
	if got := newHB(laneMaxAckPending).saturatedIssues(context.Background(),
		map[string]string{name: "pausedStructural"}, time.Now(), map[string]any{}); len(got) != 0 {
		t.Fatalf("a paused consumer is not a saturated one; got %+v", got)
	}
	// A non-lane-1 durable is never polled at all.
	if got := newHB(laneMaxAckPending).saturatedIssues(context.Background(),
		map[string]string{"weaver-temporal": "running"}, time.Now(), map[string]any{}); len(got) != 0 {
		t.Fatalf("only lane-1 durables carry the decline loop's pending set; got %+v", got)
	}
}

// stubAckStats reports a fixed ack-pending count for every consumer.
type stubAckStats struct{ pending uint64 }

func (s stubAckStats) AckStatsForConsumer(context.Context, string) (substrate.AckStats, error) {
	return substrate.AckStats{AckPending: s.pending}, nil
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
