// ReassignLeaseUnit op tests through the real install + Processor pipeline.
// External test package, mirroring lease_signing_test.go's shape and helpers
// (setupLeaseEnv, newLeasePipeline, seedApplicant, seedUnit, applyToUnit,
// tombstoneUnit, guardLinkKey, keyExists, readDoc, readRevision,
// findEmittedEvent).
package leasesigning_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// appliesToUnitLinkKey reconstructs the deterministic
// lnk.leaseapp.<id>.appliesToUnit.unit.<id> link key from the full leaseapp +
// unit vertex keys — the key ReassignLeaseUnit tombstones (old target) and
// creates-or-revives (new target).
func appliesToUnitLinkKey(leaseAppKey, unitKey string) string {
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	return "lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID
}

// reassignLeaseUnit submits ReassignLeaseUnit{leaseAppKey, newUnitKey} (class
// leaseapp). Reads carries the two (a) required keys; optionalReads carries
// the (d) keys the op-meta declares — the new appliesToUnit link, and the
// .decision / .tenancy that decide whether the application is terminal;
// Enumerations carries
// the two (e) bounded kv.Links walks the script performs (appliesToUnit +
// applicationFor off the leaseapp) — the same declarations a real op-catalog
// dispatch resolves off OpMetas().
func reassignLeaseUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, newUnitKey string, want processor.MessageOutcome) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ReassignLeaseUnit",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","newUnitKey":"` + newUnitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseAppKey, newUnitKey},
			OptionalReads: []string{appliesToUnitLinkKey(leaseAppKey, newUnitKey), leaseAppKey + ".decision", leaseAppKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: leaseAppKey, Relation: "appliesToUnit", Direction: "out"},
				{Hub: leaseAppKey, Relation: "applicationFor", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return reqID
}

// reassignLeaseUnitReason is reassignLeaseUnit for the rejection-message
// assertions: submits + awaits the reply, requires Rejected, and returns the
// script's own failure text past the per-op requestId wrapper (the
// withdrawReason idiom, lease_signing_test.go).
func reassignLeaseUnitReason(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, newUnitKey string) string {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ReassignLeaseUnit",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "leaseapp",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","newUnitKey":"` + newUnitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseAppKey, newUnitKey},
			OptionalReads: []string{appliesToUnitLinkKey(leaseAppKey, newUnitKey), leaseAppKey + ".decision", leaseAppKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: leaseAppKey, Relation: "appliesToUnit", Direction: "out"},
				{Hub: leaseAppKey, Relation: "applicationFor", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %v, want Rejected", label, outcome)
	}
	if reply.Error == nil {
		t.Fatalf("%s: rejected reply carries no error", label)
	}
	msg := reply.Error.Message
	marker := "fail: "
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("%s: rejection carries no script failure: %s", label, msg)
	}
	return msg[i+len(marker):]
}

// TestReassignLeaseUnit_DeadUnitRepoint is the repair case the fire exists
// for: a lease's unit is tombstoned out from under it (TombstoneLocation does
// not cascade), and the operator re-points appliesToUnit at a live unit. The
// old link tombstones, the new one lands live, the (applicant, newUnit) guard
// goes live, the VACATED (applicant, oldUnit) guard tombstones, and the event
// names both units.
func TestReassignLeaseUnit_DeadUnitRepoint(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-dead")

	applicant := seedApplicant(t, ctx, conn, "BBRLUAPPAHJKMNPQRSTU")
	unitA := seedUnit(t, ctx, conn, "BBRLUAAAAHJKMNPQRSTU")
	unitB := seedUnit(t, ctx, conn, "BBRLUBBBBHJKMNPQRSTU")

	app := applyToUnit(t, ctx, conn, cp, cons, "rluDeadApply1", applicant, unitA, processor.OutcomeAccepted)
	if app == "" {
		t.Fatalf("application should commit")
	}
	oldLinkKey := appliesToUnitLinkKey(app, unitA)
	newLinkKey := appliesToUnitLinkKey(app, unitB)
	guardA := guardLinkKey(applicant, unitA)
	guardB := guardLinkKey(applicant, unitB)
	if !keyExists(t, ctx, conn, guardA) {
		t.Fatalf("guard(applicant, A) should be alive right after CreateLeaseApplication (proving the positive before the tombstone assertion below)")
	}

	tombstoneUnit(t, ctx, conn, unitA)

	reqID := reassignLeaseUnit(t, ctx, conn, cp, cons, "rluDeadReassign1", app, unitB, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, oldLinkKey) {
		t.Fatalf("old appliesToUnit link should be tombstoned after reassignment")
	}
	if d, _ := readDoc(t, ctx, conn, oldLinkKey)["isDeleted"].(bool); !d {
		t.Fatalf("old appliesToUnit link should be a tombstone (isDeleted=true), not absent")
	}
	if !keyExists(t, ctx, conn, newLinkKey) {
		t.Fatalf("new appliesToUnit link should be alive after reassignment")
	}
	if !keyExists(t, ctx, conn, guardB) {
		t.Fatalf("(applicant, newUnit) guard link should be alive after reassignment")
	}
	if keyExists(t, ctx, conn, guardA) {
		t.Fatalf("the VACATED (applicant, oldUnit) guard should be tombstoned after reassignment")
	}
	if d, _ := readDoc(t, ctx, conn, guardA)["isDeleted"].(bool); !d {
		t.Fatalf("the vacated guard should be a tombstone (isDeleted=true), not absent")
	}

	ev := findEmittedEvent(t, ctx, conn, reqID, "leaseapp.unitReassigned")
	if ev["leaseAppKey"] != app || ev["oldUnitKey"] != unitA || ev["newUnitKey"] != unitB {
		t.Fatalf("leaseapp.unitReassigned event = %+v, want leaseAppKey=%s oldUnitKey=%s newUnitKey=%s", ev, app, unitA, unitB)
	}
}

// TestReassignLeaseUnit_LiveUnitMove proves the repair op doubles as an
// ordinary move: the old unit stays alive, its (applicant, oldUnit) guard is
// freed (the pair is vacated), and the new pair's guard goes live.
func TestReassignLeaseUnit_LiveUnitMove(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-live")

	applicant := seedApplicant(t, ctx, conn, "BBRLUMVAAHJKMNPQRSTU")
	unitA := seedUnit(t, ctx, conn, "BBRLUMVUAAHJKMNPQRST")
	unitB := seedUnit(t, ctx, conn, "BBRLUMVUBBHJKMNPQRST")

	app := applyToUnit(t, ctx, conn, cp, cons, "rluLiveApply1", applicant, unitA, processor.OutcomeAccepted)
	guardA := guardLinkKey(applicant, unitA)
	guardB := guardLinkKey(applicant, unitB)
	if !keyExists(t, ctx, conn, guardA) {
		t.Fatalf("guard(applicant, A) should be alive right after CreateLeaseApplication")
	}

	reassignLeaseUnit(t, ctx, conn, cp, cons, "rluLiveReassign1", app, unitB, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, appliesToUnitLinkKey(app, unitA)) {
		t.Fatalf("old appliesToUnit link should be tombstoned after an ordinary move")
	}
	if !keyExists(t, ctx, conn, appliesToUnitLinkKey(app, unitB)) {
		t.Fatalf("new appliesToUnit link should be alive after an ordinary move")
	}
	if keyExists(t, ctx, conn, guardA) {
		t.Fatalf("the VACATED (applicant, oldUnit) guard should be tombstoned — the guard is per LIVE pair, and this lease no longer applies to A")
	}
	if !keyExists(t, ctx, conn, guardB) {
		t.Fatalf("the NEW pair's guard link should be alive")
	}
	if unitA == unitB {
		t.Fatalf("test setup bug: unitA and unitB must differ")
	}
}

// TestReassignLeaseUnit_ReturnToOriginalUnit_RevivesLinkAndGuard: freeing the
// vacated pair's guard on the way out means the way back is clean. Move
// A->B->A: accepted both times. A's appliesToUnit link key is deterministic
// per (leaseapp, unit) — the second move REVIVES it (same key, live again,
// never a duplicate). guard(applicant, A) is freed by the first move and
// revived by the second; guard(applicant, B) — now the vacated pair — ends
// tombstoned.
func TestReassignLeaseUnit_ReturnToOriginalUnit_RevivesLinkAndGuard(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-return-revive")

	applicant := seedApplicant(t, ctx, conn, "BBRLURTNAAAHJKMNPQRS")
	unitA := seedUnit(t, ctx, conn, "BBRLURTNUAAHJKMNPQRS")
	unitB := seedUnit(t, ctx, conn, "BBRLURTNUBBHJKMNPQRS")

	app := applyToUnit(t, ctx, conn, cp, cons, "rluRTApply1", applicant, unitA, processor.OutcomeAccepted)
	linkA := appliesToUnitLinkKey(app, unitA)
	guardA := guardLinkKey(applicant, unitA)
	guardB := guardLinkKey(applicant, unitB)
	if !keyExists(t, ctx, conn, guardA) {
		t.Fatalf("guard(applicant, A) should be alive right after CreateLeaseApplication")
	}
	revA := readRevision(t, ctx, conn, linkA)

	reassignLeaseUnit(t, ctx, conn, cp, cons, "rluRTToB1", app, unitB, processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, linkA) {
		t.Fatalf("A's appliesToUnit link should be tombstoned after the move to B")
	}
	if keyExists(t, ctx, conn, guardA) {
		t.Fatalf("guard(applicant, A) is now the vacated pair and should be tombstoned")
	}
	if !keyExists(t, ctx, conn, guardB) {
		t.Fatalf("guard(applicant, B) should be alive after the move to B")
	}

	reassignLeaseUnit(t, ctx, conn, cp, cons, "rluRTBackA1", app, unitA, processor.OutcomeAccepted)

	if !keyExists(t, ctx, conn, linkA) {
		t.Fatalf("A's appliesToUnit link should be REVIVED (alive again) after moving back")
	}
	if got := readRevision(t, ctx, conn, linkA); got <= revA {
		t.Fatalf("the revived link's revision must have advanced past the original tombstone: got %d, want > %d", got, revA)
	}
	if !keyExists(t, ctx, conn, guardA) {
		t.Fatalf("guard(applicant, A) should be REVIVED (alive again) after moving back")
	}
	if keyExists(t, ctx, conn, guardB) {
		t.Fatalf("guard(applicant, B) is now the vacated pair and should be tombstoned")
	}
}

// TestReassignLeaseUnit_VacatedPairAcceptsNewApplication proves the guard
// freed by a move is genuinely free, not just left inert: after A->B, the
// SAME applicant can open a BRAND NEW application on A via ordinary
// CreateLeaseApplication (a real re-apply, not a reassignment).
func TestReassignLeaseUnit_VacatedPairAcceptsNewApplication(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-vacate-reapply")

	applicant := seedApplicant(t, ctx, conn, "BBRLUVCTAAAHJKMNPQRS")
	unitA := seedUnit(t, ctx, conn, "BBRLUVCTUAAHJKMNPQRS")
	unitB := seedUnit(t, ctx, conn, "BBRLUVCTUBBHJKMNPQRS")

	first := applyToUnit(t, ctx, conn, cp, cons, "rluVCApply1", applicant, unitA, processor.OutcomeAccepted)
	reassignLeaseUnit(t, ctx, conn, cp, cons, "rluVCToB1", first, unitB, processor.OutcomeAccepted)

	second := applyToUnit(t, ctx, conn, cp, cons, "rluVCApply2", applicant, unitA, processor.OutcomeAccepted)
	if second == "" || second == first {
		t.Fatalf("a fresh application by the same applicant on the vacated unit A should commit to a new key; got %q (first=%q)", second, first)
	}
	if !keyExists(t, ctx, conn, guardLinkKey(applicant, unitA)) {
		t.Fatalf("guard(applicant, A) should be alive again after the new application")
	}
}

// TestReassignLeaseUnit_DuplicateApplication_Rejected: the applicant already
// holds a live application on the target unit (a SEPARATE leaseapp), so
// re-pointing THIS application at that unit is rejected — the operator
// withdraws one first. Nothing commits: the original link + guard are
// unchanged.
func TestReassignLeaseUnit_DuplicateApplication_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-dup")

	applicant := seedApplicant(t, ctx, conn, "BBRLUDUPAHJKMNPQRSTU")
	unitA := seedUnit(t, ctx, conn, "BBRLUDUPUAHJKMNPQRST")
	unitB := seedUnit(t, ctx, conn, "BBRLUDUPUBHJKMNPQRST")

	appA := applyToUnit(t, ctx, conn, cp, cons, "rluDupApplyA1", applicant, unitA, processor.OutcomeAccepted)
	// A SECOND, separate application by the SAME applicant directly on unitB —
	// this is the live guard(applicant, unitB) that blocks the reassignment.
	appB := applyToUnit(t, ctx, conn, cp, cons, "rluDupApplyB1", applicant, unitB, processor.OutcomeAccepted)
	if appA == appB {
		t.Fatalf("test setup bug: appA and appB must differ")
	}

	oldLinkKey := appliesToUnitLinkKey(appA, unitA)
	oldRev := readRevision(t, ctx, conn, oldLinkKey)

	reason := reassignLeaseUnitReason(t, ctx, conn, cp, cons, "rluDupReassign1", appA, unitB)
	if !strings.Contains(reason, "DuplicateApplication") {
		t.Fatalf("want DuplicateApplication, got %q", reason)
	}

	// Rejected atomically: appA's original link is untouched (same revision,
	// still live) — nothing partially committed.
	if !keyExists(t, ctx, conn, oldLinkKey) {
		t.Fatalf("a rejected reassignment must not tombstone the original appliesToUnit link")
	}
	if got := readRevision(t, ctx, conn, oldLinkKey); got != oldRev {
		t.Fatalf("a rejected reassignment must not touch the original link's revision: got %d, want %d", got, oldRev)
	}
}

// TestReassignLeaseUnit_NoOp_UnchangedRevision: reassigning an application to
// the unit it ALREADY applies to is accepted as a no-op — zero mutations, the
// link's revision is untouched.
func TestReassignLeaseUnit_NoOp_UnchangedRevision(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-noop")

	applicant := seedApplicant(t, ctx, conn, "BBRLUNPPAHJKMNPQRSTU")
	unit := seedUnit(t, ctx, conn, "BBRLUNPPUAHJKMNPQRST")

	app := applyToUnit(t, ctx, conn, cp, cons, "rluNoopApply1", applicant, unit, processor.OutcomeAccepted)
	linkKey := appliesToUnitLinkKey(app, unit)
	rev := readRevision(t, ctx, conn, linkKey)

	reassignLeaseUnit(t, ctx, conn, cp, cons, "rluNoopReassign1", app, unit, processor.OutcomeAccepted)

	if got := readRevision(t, ctx, conn, linkKey); got != rev {
		t.Fatalf("a no-op reassignment (already on this unit) must not touch the link's revision: got %d, want %d", got, rev)
	}
	if !keyExists(t, ctx, conn, linkKey) {
		t.Fatalf("the link should still be alive after a no-op reassignment")
	}
}

// TestReassignLeaseUnit_UnknownLeaseApplication_Rejected: a leaseAppKey naming
// a genuinely non-existent application is rejected. leaseAppKey is a REQUIRED
// read, so a key that never existed at all fails closed at hydration
// (HydrationMiss) before the script's own UnknownLeaseApplication check ever
// runs — the same shape TestBackfillLeaseTerms_UnknownApplication_Rejected
// asserts (outcome only, not the in-script reason text, for the identical
// reason).
func TestReassignLeaseUnit_UnknownLeaseApplication_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-unknown-app")

	unit := seedUnit(t, ctx, conn, "BBRLUUKNAUAHJKMNPQRS")
	reassignLeaseUnit(t, ctx, conn, cp, cons, "rluUnknownApp1", "vtx.leaseapp.ZZNSCHAPPXHJKMNPQRST", unit, processor.OutcomeRejected)
}

// TestReassignLeaseUnit_UnknownUnit_Rejected: a newUnitKey naming a genuinely
// non-existent unit is rejected (HydrationMiss, the same required-read shape
// as the unknown-application case above), and the original link is untouched.
func TestReassignLeaseUnit_UnknownUnit_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-unknown-unit")

	applicant := seedApplicant(t, ctx, conn, "BBRLUUKNUAHJKMNPQRST")
	unit := seedUnit(t, ctx, conn, "BBRLUUKNUUAHJKMNPQRS")
	app := applyToUnit(t, ctx, conn, cp, cons, "rluUnknownUnitApply1", applicant, unit, processor.OutcomeAccepted)

	reassignLeaseUnit(t, ctx, conn, cp, cons, "rluUnknownUnit1", app, "vtx.unit.ZZNSCHUNTXHJKMNPQRST", processor.OutcomeRejected)
	if !keyExists(t, ctx, conn, appliesToUnitLinkKey(app, unit)) {
		t.Fatalf("a rejected reassignment must not tombstone the original appliesToUnit link")
	}
}

// TestReassignLeaseUnit_TombstonedUnit_Rejected: a newUnitKey that EXISTS but
// is tombstoned exercises the script's own UnknownUnit check directly (unlike
// the genuinely-absent case above, a tombstoned unit satisfies the required
// read and reaches vertex_alive inside the script).
func TestReassignLeaseUnit_TombstonedUnit_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "reassign-tombstoned-unit")

	applicant := seedApplicant(t, ctx, conn, "BBRLUTMBDAHJKMNPQRST")
	unit := seedUnit(t, ctx, conn, "BBRLUTMBDUAHJKMNPQRS")
	deadUnit := seedUnit(t, ctx, conn, "BBRLUTMBDUBHJKMNPQRS")
	app := applyToUnit(t, ctx, conn, cp, cons, "rluTombApply1", applicant, unit, processor.OutcomeAccepted)
	tombstoneUnit(t, ctx, conn, deadUnit)

	reason := reassignLeaseUnitReason(t, ctx, conn, cp, cons, "rluTombReassign1", app, deadUnit)
	if !strings.Contains(reason, "UnknownUnit") {
		t.Fatalf("want UnknownUnit, got %q", reason)
	}
	if !keyExists(t, ctx, conn, appliesToUnitLinkKey(app, unit)) {
		t.Fatalf("a rejected reassignment must not tombstone the original appliesToUnit link")
	}
}

// TestReassignLeaseUnit_TombstonesWithExpectedRevision (the revision-pinned
// tombstone text-pin) lives in reassign_lease_unit_internal_test.go, package
// leasesigning — it reads the unexported leaseAppDDLScript source, which this
// external test package cannot see.
