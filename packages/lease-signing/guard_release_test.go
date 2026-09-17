// The per-(applicant, unit) guard link is freed by every TERMINAL application
// state (design docs/reviews/loftspace-unit-turnover-2026-09-17.md decision
// 4): DecideLeaseApplication's first decline, RecordApplicationLoss's
// recording arm and EndTenancy's ending arm each tombstone
// lnk.identity.<a>.appliedToUnit.unit.<u>, so a subsequent
// CreateLeaseApplication by the same applicant on the same unit is ADMITTED
// (the tombstone revives); the idempotent no-op arms leave the guard alone.
// External test package, mirroring lease_signing_test.go's helpers.
package leasesigning_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// TestGuardRelease_DeclineFreesThePairAndReapplyIsAdmitted: a first decline
// tombstones the guard (present, isDeleted) and the applicant's re-apply on
// the same unit commits to a fresh application, reviving it. A same-value
// re-decline of the OLD application after that re-apply leaves the NEW
// application's revived guard alive.
func TestGuardRelease_DeclineFreesThePairAndReapplyIsAdmitted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-decline")

	applicantKey := seedApplicant(t, ctx, conn, "GRdecjineappHJKMNPQR")
	first := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)
	gk := guardLinkKey(applicantKey, unitKey)
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be alive after the first apply")
	}

	decide(t, ctx, conn, cp, cons, "grDecline01", first, "declined", unitKey, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, gk) {
		t.Fatalf("a decline must free the (applicant, unit) guard")
	}
	if d, _ := readDoc(t, ctx, conn, gk)["isDeleted"].(bool); !d {
		t.Fatalf("the freed guard must be a tombstone (isDeleted=true), not absent — re-apply revives it")
	}

	second := applyToUnit(t, ctx, conn, cp, cons, "grReapply01", applicantKey, unitKey, processor.OutcomeAccepted)
	if second == "" || second == first {
		t.Fatalf("re-apply after a decline should commit to a new key; got %q (first=%q)", second, first)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be revived (alive) after the re-apply")
	}

	// The old application's idempotent re-decline must not tombstone the new
	// application's guard.
	decide(t, ctx, conn, cp, cons, "grDecline02", first, "declined", unitKey, "2026-06-27T10:00:00Z", processor.OutcomeAccepted)
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("a same-value re-decline of the OLD application tombstoned the NEW application's guard")
	}
}

// TestGuardRelease_ApproveKeepsThePair: an approve is the executed lease, not a
// terminal release — the guard stays alive and a re-apply is still refused
// DuplicateApplication until the tenancy ends.
func TestGuardRelease_ApproveKeepsThePair(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-approve")

	_, applicantKey, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "GRapproveappHJKMNPQR")
	gk := guardLinkKey(applicantKey, unitKey)
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("an approve must keep the (applicant, unit) guard alive")
	}
	if got := applyToUnit(t, ctx, conn, cp, cons, "grReapplyAp", applicantKey, unitKey, processor.OutcomeRejected); got != "" {
		t.Fatalf("a re-apply under an approved application must be refused, got %q", got)
	}
}

// TestGuardRelease_LossFreesThePairAndReapplyIsAdmitted: recording the loss
// tombstones the guard and the losing applicant may apply for the unit again;
// the idempotent no-op re-dispatch after the re-apply leaves the revived
// guard alive.
func TestGuardRelease_LossFreesThePairAndReapplyIsAdmitted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-loss")

	first, applicantKey, unitKey := ralLosingApplication(t, ctx, conn, cp, cons, "GRjossappHJKMNPQRSTU")
	gk := guardLinkKey(applicantKey, unitKey)
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be alive before the loss")
	}

	env := ralEnvelope("grLoss0001", lsActorKey, first, "2026-09-14T13:42:00Z")
	if outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env); outcome != processor.OutcomeAccepted {
		t.Fatalf("RecordApplicationLoss: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if keyExists(t, ctx, conn, gk) {
		t.Fatalf("a recorded loss must free the (applicant, unit) guard")
	}
	if d, _ := readDoc(t, ctx, conn, gk)["isDeleted"].(bool); !d {
		t.Fatalf("the freed guard must be a tombstone, not absent")
	}

	second := applyToUnit(t, ctx, conn, cp, cons, "grReapplyLs", applicantKey, unitKey, processor.OutcomeAccepted)
	if second == "" || second == first {
		t.Fatalf("re-apply after a loss should commit to a new key; got %q (first=%q)", second, first)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be revived (alive) after the re-apply")
	}

	// The at-least-once re-dispatch on the lost application is the no-op arm:
	// it must not touch the new application's guard.
	again := ralEnvelope("grLoss0002", lsActorKey, first, "2026-09-15T13:42:00Z")
	if outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, again); outcome != processor.OutcomeAccepted {
		t.Fatalf("RecordApplicationLoss re-dispatch: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("the no-op re-dispatch of a recorded loss tombstoned the NEW application's guard")
	}
}

// TestGuardRelease_EndTenancyFreesThePairAndReapplyIsAdmitted: ending the
// tenancy tombstones the guard the approve kept, and the former tenant may
// apply for the unit again (the residence design's re-approval-on-the-same-unit
// case); the already-ended no-op arm leaves the revived guard alone.
func TestGuardRelease_EndTenancyFreesThePairAndReapplyIsAdmitted(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-endtenancy")

	first, applicantKey, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "GRendtenappHJKMNPQRS")
	etStageRenewedTenancy(t, ctx, conn, first)
	gk := guardLinkKey(applicantKey, unitKey)
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be alive under the approved tenancy")
	}

	env := etEnvelope("grEndTen001", lsActorKey, first, "2028-09-15T13:42:00Z", etDeclaredHint(first))
	if outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env); outcome != processor.OutcomeAccepted {
		t.Fatalf("EndTenancy: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if got, _ := readDoc(t, ctx, conn, first+".tenancy")["data"].(map[string]any)["endedAt"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("tenancy.endedAt = %q, want 2028-08-01T00:00:00Z", got)
	}
	if keyExists(t, ctx, conn, gk) {
		t.Fatalf("an ended tenancy must free the (applicant, unit) guard")
	}
	if d, _ := readDoc(t, ctx, conn, gk)["isDeleted"].(bool); !d {
		t.Fatalf("the freed guard must be a tombstone, not absent")
	}

	second := applyToUnit(t, ctx, conn, cp, cons, "grReapplyEt", applicantKey, unitKey, processor.OutcomeAccepted)
	if second == "" || second == first {
		t.Fatalf("re-apply after the tenancy ended should commit to a new key; got %q (first=%q)", second, first)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be revived (alive) after the re-apply")
	}

	// The already-ended no-op arm must not touch the new application's guard.
	again := etEnvelope("grEndTen002", lsActorKey, first, "2028-10-15T13:42:00Z", etDeclaredHint(first))
	if outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, again); outcome != processor.OutcomeAccepted {
		t.Fatalf("EndTenancy re-dispatch: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("the already-ended no-op re-dispatch tombstoned the NEW application's guard")
	}
}

// TestGuardRelease_EndTenancyWithTombstonedUnitStillRecordsTheEnd: a unit that
// no longer resolves (tombstoned out from under the lease) means there is no
// pair left to free — the end is still recorded, never refused.
func TestGuardRelease_EndTenancyWithTombstonedUnitStillRecordsTheEnd(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-endtenancy-deadunit")

	first, applicantKey, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "GRendtdeadapHJKMNPQR")
	etStageRenewedTenancy(t, ctx, conn, first)
	tombstoneUnit(t, ctx, conn, unitKey)
	gk := guardLinkKey(applicantKey, unitKey)

	env := etEnvelope("grEndTenDead", lsActorKey, first, "2028-09-15T13:42:00Z", etDeclaredHint(first))
	if outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env); outcome != processor.OutcomeAccepted {
		t.Fatalf("EndTenancy on a tombstoned unit: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	if got, _ := readDoc(t, ctx, conn, first+".tenancy")["data"].(map[string]any)["endedAt"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("tenancy.endedAt = %q, want the end recorded regardless of the unit", got)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("with no live unit there is no pair to free — the guard is left as it was")
	}
}

// TestGuardRelease_WithdrawOfDeclinedAppLeavesTheNewApplicationsGuard pins
// the guard's integrity across the revive: the guard key is deterministic per
// pair, so after a decline freed it and a re-apply revived it for the NEW
// application, withdrawing the OLD declined application (accepted, its vertex
// tombstoned) must leave that alive guard alone — a third apply on the pair
// is still refused DuplicateApplication.
func TestGuardRelease_WithdrawOfDeclinedAppLeavesTheNewApplicationsGuard(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-withdraw-declined")

	applicantKey := seedApplicant(t, ctx, conn, "GRwdrdecappHJKMNPQRS")
	first := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)
	gk := guardLinkKey(applicantKey, unitKey)

	decide(t, ctx, conn, cp, cons, "grWdDecline", first, "declined", unitKey, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, gk) {
		t.Fatalf("the decline must free the guard first")
	}
	second := applyToUnit(t, ctx, conn, cp, cons, "grWdReapply", applicantKey, unitKey, processor.OutcomeAccepted)
	if second == "" || !keyExists(t, ctx, conn, gk) {
		t.Fatalf("the re-apply must revive the guard for the new application")
	}

	// Withdrawing the OLD declined application drops it from My Applications
	// but never touches the pair's guard — that guard is the new application's.
	withdraw(t, ctx, conn, cp, cons, "grWdOld0001", first, unitKey, applicantKey, processor.OutcomeAccepted)
	if d, _ := readDoc(t, ctx, conn, first)["isDeleted"].(bool); !d {
		t.Fatalf("the withdrawn declined application must be tombstoned")
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("withdrawing a DECLINED application tombstoned the NEW application's guard")
	}
	if got := applyToUnit(t, ctx, conn, cp, cons, "grWdThird01", applicantKey, unitKey, processor.OutcomeRejected); got != "" {
		t.Fatalf("a third application on the pair must be refused DuplicateApplication while the second is live, got %q", got)
	}
	if !keyExists(t, ctx, conn, second) {
		t.Fatalf("the new application must stay alive")
	}
}

// TestGuardRelease_WithdrawOfUndecidedAppFreesThePair is the positive vector
// the rule above is scoped against: an UNDECIDED application's withdraw still
// frees the pair (the tombstone), and the applicant's re-apply is admitted.
func TestGuardRelease_WithdrawOfUndecidedAppFreesThePair(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-withdraw-undecided")

	applicantKey := seedApplicant(t, ctx, conn, "GRwdrundappHJKMNPQRS")
	first := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey := unitKeyFor(applicantKey)
	gk := guardLinkKey(applicantKey, unitKey)

	withdraw(t, ctx, conn, cp, cons, "grWdUnd0001", first, unitKey, applicantKey, processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, gk) {
		t.Fatalf("withdrawing an undecided application must free the guard")
	}
	if d, _ := readDoc(t, ctx, conn, gk)["isDeleted"].(bool); !d {
		t.Fatalf("the freed guard must be a tombstone, not absent")
	}
	if second := applyToUnit(t, ctx, conn, cp, cons, "grWdUndRe01", applicantKey, unitKey, processor.OutcomeAccepted); second == "" || second == first {
		t.Fatalf("re-apply after withdrawing an undecided application should commit to a new key; got %q", second)
	}
	if !keyExists(t, ctx, conn, gk) {
		t.Fatalf("guard link should be revived after the re-apply")
	}
}

// TestGuardRelease_ReassignOfTerminalAppTouchesNoGuard pins ReassignLeaseUnit's
// terminal rule: App1 on (A, U) is declined (its pair freed), A re-applies as
// App2 (the (A, U) guard revived), then an operator moves App1 to live unit
// V. App1 holds no pair, so the move re-points its appliesToUnit link only:
// App2's guard on (A, U) stays alive (a third apply on U is still refused),
// and no guard is minted on (A, V) — a fresh apply by A on V is admitted.
func TestGuardRelease_ReassignOfTerminalAppTouchesNoGuard(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-reassign-terminal")

	applicantKey := seedApplicant(t, ctx, conn, "GRreasgtrmapHJKMNPQR")
	app1 := createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitU := unitKeyFor(applicantKey)
	unitV := seedUnit(t, ctx, conn, "GRreasgtrmuvHJKMNPQR")
	guardU := guardLinkKey(applicantKey, unitU)
	guardV := guardLinkKey(applicantKey, unitV)

	decide(t, ctx, conn, cp, cons, "grRsDecline", app1, "declined", unitU, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)
	app2 := applyToUnit(t, ctx, conn, cp, cons, "grRsReapply", applicantKey, unitU, processor.OutcomeAccepted)
	if app2 == "" || !keyExists(t, ctx, conn, guardU) {
		t.Fatalf("the re-apply must revive the (A, U) guard for App2")
	}

	reassignLeaseUnit(t, ctx, conn, cp, cons, "grRsMove001", app1, unitV, processor.OutcomeAccepted)
	if !keyExists(t, ctx, conn, appliesToUnitLinkKey(app1, unitV)) || keyExists(t, ctx, conn, appliesToUnitLinkKey(app1, unitU)) {
		t.Fatalf("the declined application must be re-pointed at V")
	}
	if !keyExists(t, ctx, conn, guardU) {
		t.Fatalf("moving the declined App1 tombstoned App2's live guard on (A, U)")
	}
	if keyExists(t, ctx, conn, guardV) {
		t.Fatalf("moving the declined App1 minted a guard on (A, V) that nothing would ever free")
	}
	if got := applyToUnit(t, ctx, conn, cp, cons, "grRsThirdU1", applicantKey, unitU, processor.OutcomeRejected); got != "" {
		t.Fatalf("a third application on (A, U) must still be refused while App2 is live, got %q", got)
	}
	if got := applyToUnit(t, ctx, conn, cp, cons, "grRsFreshV1", applicantKey, unitV, processor.OutcomeAccepted); got == "" {
		t.Fatalf("a fresh application on (A, V) must be admitted — the terminal move minted no guard there")
	}
}

// TestGuardRelease_ReassignOfEndedTenancyTouchesNoGuard is the tenancy-ended
// arm of the same rule: an approved application whose term has ended holds no
// pair either, so its move re-points only.
func TestGuardRelease_ReassignOfEndedTenancyTouchesNoGuard(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "guard-reassign-ended")

	app1, applicantKey, unitU := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "GRreasgendapHJKMNPQR")
	etStageRenewedTenancy(t, ctx, conn, app1)
	unitV := seedUnit(t, ctx, conn, "GRreasgenduvHJKMNPQR")
	end := etEnvelope("grRsEndTen1", lsActorKey, app1, "2028-09-15T13:42:00Z", etDeclaredHint(app1))
	if outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, end); outcome != processor.OutcomeAccepted {
		t.Fatalf("EndTenancy: outcome = %v, want Accepted (reply %+v)", outcome, reply)
	}
	app2 := applyToUnit(t, ctx, conn, cp, cons, "grRsEndReap", applicantKey, unitU, processor.OutcomeAccepted)
	if app2 == "" || !keyExists(t, ctx, conn, guardLinkKey(applicantKey, unitU)) {
		t.Fatalf("the re-apply after the end must revive the (A, U) guard for App2")
	}

	reassignLeaseUnit(t, ctx, conn, cp, cons, "grRsEndMove", app1, unitV, processor.OutcomeAccepted)
	if !keyExists(t, ctx, conn, guardLinkKey(applicantKey, unitU)) {
		t.Fatalf("moving the ended App1 tombstoned App2's live guard on (A, U)")
	}
	if keyExists(t, ctx, conn, guardLinkKey(applicantKey, unitV)) {
		t.Fatalf("moving the ended App1 minted a guard on (A, V)")
	}
}
