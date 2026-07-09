//go:build leaseshortwindow

package leaseconvergence_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/processor"
)

// gapColumns are the §10.2 gap bools the steady-state assertion pins false: the
// four applicant gaps plus the executed-lease document chain (the docGen
// externalTask on signing + Weaver's AttachObject anchor — platform-driven, no
// harness involvement).
var gapColumns = []string{"missing_onboarding", "missing_bgcheck", "missing_payment", "missing_signature", "missing_leaseDoc", "missing_leaseDocAttach"}

// driveApplicantSteps drives the two applicant-facing ops the userTask / assignTask
// remediations represent: RecordIdentityPII (closes missing_onboarding) and
// SignLease (closes missing_signature). The bgcheck/payment externalTasks complete
// through the LIVE bridge with no harness involvement. These are submitted as plain
// ops (the applicant "filling in PII and signing") — the real DDLs, driven directly,
// exactly the facts the gaps key on.
func (h *harness) driveApplicantSteps(appKey, applicantKey string) {
	h.t.Helper()
	piiReply := h.submitOp("RecordIdentityPII", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"identityKey": applicantKey,
		"ssn":         "123456789",
		"dob":         "1990-01-01",
	}, &processor.ContextHint{Reads: []string{applicantKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, piiReply.Status, "RecordIdentityPII: %+v", piiReply.Error)

	signReply := h.submitOp("SignLease", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey,
	}, &processor.ContextHint{Reads: []string{appKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, signReply.Status, "SignLease: %+v", signReply.Error)
}

// decideLandlord submits the landlord's DecideLeaseApplication decision (the human
// gate the listing-flip waits behind). decision is approved | declined. A qualified
// application does NOT lease its unit until the landlord approves — convergence
// (violating=false) requires this step after applicant readiness. On an approve, it
// also passes the harness's seeded unit (h.lastUnitKey) + lists it in Reads: the
// FIRST approve for an application create-only-stamps the .tenancy aspect (the
// renewal chain's tenancy-term fact) from the unit's .listing economics, so the op
// needs the unit key + its .listing hydrated exactly like SetApplicantProfile does.
func (h *harness) decideLandlord(appKey, decision string) {
	h.t.Helper()
	payload := map[string]any{
		"leaseAppKey": appKey,
		"decision":    decision,
	}
	reads := []string{appKey}
	if decision == "approved" {
		payload["unit"] = h.lastUnitKey
		reads = append(reads, h.lastUnitKey)
	}
	reply := h.submitOp("DecideLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, payload,
		&processor.ContextHint{Reads: reads})
	require.Equalf(h.t, processor.ReplyStatusAccepted, reply.Status, "DecideLeaseApplication(%s): %+v", decision, reply.Error)
}

// drainUntilConverged polls the convergence row until violating flips false within
// the deadline, dumping the last row + Health issues on timeout (the loud-failure
// diagnostic). Returns once converged.
func (h *harness) drainUntilConverged(appID string, deadline time.Duration) {
	h.t.Helper()
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		row := h.readRow(appID)
		if row != nil && !rowBool(row, "violating") {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	h.dumpDiagnostics(appID)
	h.t.Fatalf("lease application %s did not converge (violating never flipped false) within %s", appID, deadline)
}

// assertSteadyState holds for a settle window and asserts violating stays false and
// every missing_* gap stays false across repeated reads (no oscillation).
func (h *harness) assertSteadyState(appID string, hold time.Duration) {
	h.t.Helper()
	cut := time.Now().Add(hold)
	for time.Now().Before(cut) {
		row := h.readRow(appID)
		require.NotNil(h.t, row, "the converged row must remain present (never tombstoned)")
		require.Falsef(h.t, rowBool(row, "violating"), "violating must stay false at steady state; row=%v", row)
		for _, col := range gapColumns {
			require.Falsef(h.t, rowBool(row, col), "%s must stay false at steady state; row=%v", col, row)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// TestLeaseConvergence_DrainThenAssert_SteadyState is the AC #1 capstone: the full
// boot (install chain + Processor + Refractor + Loom + Weaver + live bridge), one
// CreateLeaseApplication with all four gaps open, drives the applicant PII + sign
// steps, drains until violating flips false, then holds and asserts it STAYS false
// (steady state — no oscillation, no gap re-opens) with every missing_* false.
//
// This single test proves the whole vertical composes end-to-end through the live
// bridge: Weaver dispatched all four remediations, Loom ran the two externalTasks +
// the onboarding step, the live bridge completed the two external calls and posted
// the replyOps, the SignLease/RecordIdentityPII ops closed their gaps, and Refractor
// reprojected to a stable converged row.
func TestLeaseConvergence_DrainThenAssert_SteadyState(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()

	// First confirm the row projects violating (all gaps open) — the starting state.
	require.Eventually(t, func() bool {
		row := h.readRow(appID)
		return row != nil && rowBool(row, "violating")
	}, 30*time.Second, 150*time.Millisecond, "the fresh application must project a violating row")

	// Increment 2: the convergence lens walked the appliesToUnit link to the leased
	// unit and projected its informational columns (key / address / rent) — the
	// cross-package proof that loftspace-domain's aspects on location-domain's unit
	// reach the row "applying to lease Unit X at $Y/mo".
	require.Eventually(t, func() bool {
		row := h.readRow(appID)
		return row != nil && row["unitKey"] == h.lastUnitKey
	}, 30*time.Second, 150*time.Millisecond, "the row must project the leased unit's key from the appliesToUnit walk")
	unitRow := h.readRow(appID)
	require.Equal(t, h.lastUnitAddress, unitRow["unitAddress"], "unitAddress projected from the unit's .address aspect")
	require.NotNil(t, unitRow["unitRent"], "unitRent projected from the unit's .listing aspect")

	// Prove the dispatch path actually ran through the real Processor BEFORE
	// the direct ops close the gaps. Weaver's missing_signature assignTask submits
	// CreateTask scopedTo the application; Loom's onboarding userTask submits
	// CreateTask scopedTo the applicant. Both are engine-dispatched CreateTask ops
	// whose committed task vertices prove the dispatch envelopes hydrate +
	// commit through the real Processor with no shim.
	applicantID := applicantKey[len("vtx.identity."):]
	require.NotEmpty(t, h.awaitDispatchedTask(appID, 45*time.Second),
		"Weaver's assignTask must commit a CreateTask scopedTo the application (the dispatch path)")
	require.NotEmpty(t, h.awaitDispatchedTask(applicantID, 45*time.Second),
		"Loom's onboarding userTask must commit a CreateTask scopedTo the applicant (the dispatch path)")

	// The applicant records PII and signs; bgcheck/payment complete via the bridge.
	h.driveApplicantSteps(appKey, applicantKey)

	// The landlord approves the now-qualified application — the human gate the
	// listing-flip waits behind (nothing auto-leases on applicant-readiness alone).
	h.decideLandlord(appKey, "approved")

	// Drain: violating flips false once all four gaps close AND the landlord-approved
	// unit leases (the bridge round-trips the bgcheck + payment, the two ops close
	// onboarding + signature, and the directOp flips the listing to leased).
	//
	// The drain cap (15s) + the settle hold (5s) below is the ceiling the
	// `leaseshortwindow` freshness window (25s) must clear so the bgcheck does not
	// lapse mid-assertion; converge runs in ~1s in-process, so 15s is far above the
	// real drain while keeping that ceiling under the window with a 5s margin.
	h.drainUntilConverged(appID, 15*time.Second)

	// Assert steady: it stays converged (no oscillation).
	h.assertSteadyState(appID, 5*time.Second)

	// The live bridge actually dispatched the two external calls (not direct writes):
	// exactly one charge each on the Fake adapters, and exactly two .outcome aspects.
	require.Equal(t, 2, h.countOutcomeAspects(applicantID),
		"exactly two service outcomes (one bgcheck + one payment) recorded via the bridge")
}

// TestLeaseConvergence_ListingLeasedOnApproval proves the listing-status-on-approval
// convergence end-to-end through the live stack: once the application is approved
// (all four applicant gaps closed), the lens opens missing_listingLeased, Weaver
// dispatches directOp(SetListingStatus status=leased) over the appliesToUnit unit,
// the Processor flips the unit's .listing.status, the leaseapp reprojects (the unit
// is an appliesToUnit neighbor — the same adjacency reprojection that closes
// missing_onboarding on a .ssn write), missing_listingLeased closes, and the row
// converges. A status-only flip that PRESERVES the economics.
//
// Because missing_listingLeased is folded into violating, the existing
// drain-until-converged tests already require this flip; this test asserts it
// EXPLICITLY (the unit's listing actually became leased, economics intact).
func TestLeaseConvergence_ListingLeasedOnApproval(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()

	// Starting state: the unit is listed available (it shows in Browse&Apply).
	require.Equal(t, "available", h.unitListingStatus(h.lastUnitKey), "the unit starts available")

	h.driveApplicantSteps(appKey, applicantKey)

	// The landlord approves — the gate the listing-flip waits behind.
	h.decideLandlord(appKey, "approved")

	// Convergence REQUIRES the listing flip (violating includes missing_listingLeased),
	// so draining to violating=false already proves the directOp fired + reprojected.
	h.drainUntilConverged(appID, 30*time.Second)

	// Explicit: the unit's listing is now leased, with the economics preserved verbatim
	// by the status-only rewrite (rentAmount / rentCurrency untouched).
	require.Equal(t, "leased", h.unitListingStatus(h.lastUnitKey),
		"the approved application's unit must be marked leased by the convergence directOp")
	listing := h.aspectData(h.lastUnitKey, "listing")
	require.NotNil(t, listing, "the .listing aspect must still exist after the status flip")
	require.EqualValues(t, h.lastUnitRent, listing["rentAmount"], "the status-only flip preserved rentAmount")
	require.Equal(t, "USD", listing["rentCurrency"], "the status-only flip preserved rentCurrency")

	// The converged row reflects it: unit leased, gap closed, applicant approved.
	row := h.readRow(appID)
	require.NotNil(t, row)
	require.Equal(t, "leased", row["unitStatus"], "the row reprojected the leased status")
	require.Falsef(t, rowBool(row, "missing_listingLeased"), "the listing gap must be closed; row=%v", row)
	require.Truef(t, rowBool(row, "applicantApproved"), "the applicant must read approved; row=%v", row)

	// Steady: it stays leased + converged (the idempotent directOp does not thrash).
	h.assertSteadyState(appID, 3*time.Second)
}

// TestLeaseConvergence_NoLeaseUntilLandlordApproves is the crux of Increment 2: a
// QUALIFIED application (all four applicant gaps closed) does NOT lease its unit
// until the landlord approves. The lens holds it in the missing_decision state —
// violating (work not done) but dispatching NO listing flip — so the unit stays
// 'available'. Only after DecideLeaseApplication(approved) does the listing flip and
// the row converge. This closes the auto-lease race the old applicantApproved-gated
// flip had.
func TestLeaseConvergence_NoLeaseUntilLandlordApproves(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()

	require.Equal(t, "available", h.unitListingStatus(h.lastUnitKey), "the unit starts available")

	// Drive the applicant to fully qualified (PII + sign; bgcheck/payment via the bridge).
	h.driveApplicantSteps(appKey, applicantKey)

	// The row becomes qualified-awaiting-decision: applicantApproved true,
	// missing_decision true, NO listing flip. Assert it reaches this state and HOLDS
	// — the unit must NOT lease without a landlord decision.
	require.Eventually(t, func() bool {
		row := h.readRow(appID)
		return row != nil && rowBool(row, "applicantApproved") && rowBool(row, "missing_decision")
	}, 30*time.Second, 150*time.Millisecond, "the qualified application must reach the awaiting-landlord-decision state")

	// Hold: the unit stays available, the row stays missing_decision + violating, and
	// missing_listingLeased never opens (nothing dispatches a flip without approval).
	cut := time.Now().Add(3 * time.Second)
	for time.Now().Before(cut) {
		require.Equal(t, "available", h.unitListingStatus(h.lastUnitKey), "the unit must NOT lease before the landlord approves")
		row := h.readRow(appID)
		require.NotNil(t, row)
		require.Truef(t, rowBool(row, "missing_decision"), "stays awaiting decision; row=%v", row)
		require.Falsef(t, rowBool(row, "missing_listingLeased"), "no listing flip dispatched without approval; row=%v", row)
		require.Truef(t, rowBool(row, "violating"), "qualified-awaiting-decision is still violating; row=%v", row)
		time.Sleep(150 * time.Millisecond)
	}

	// The landlord approves → the listing flips and the row converges.
	h.decideLandlord(appKey, "approved")
	h.drainUntilConverged(appID, 30*time.Second)
	require.Equal(t, "leased", h.unitListingStatus(h.lastUnitKey), "the landlord-approved unit leases")
	row := h.readRow(appID)
	require.Truef(t, rowBool(row, "landlordApproved"), "the row reflects the landlord approval; row=%v", row)
	require.Falsef(t, rowBool(row, "missing_decision"), "the decision closed the awaiting-decision gap; row=%v", row)
}

// TestLeaseConvergence_LandlordDeclineIsTerminal: a landlord DECLINE of a qualified
// application is terminal — the unit never leases, the row reads declined, and
// violating settles to false (no work remains; Weaver stops reconciling). The
// decline is a definitive disposition, not a transient gap.
func TestLeaseConvergence_LandlordDeclineIsTerminal(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()

	require.Equal(t, "available", h.unitListingStatus(h.lastUnitKey), "the unit starts available")
	h.driveApplicantSteps(appKey, applicantKey)

	// Wait until qualified-awaiting-decision, then DECLINE.
	require.Eventually(t, func() bool {
		row := h.readRow(appID)
		return row != nil && rowBool(row, "applicantApproved") && rowBool(row, "missing_decision")
	}, 30*time.Second, 150*time.Millisecond, "the application must reach the awaiting-decision state")
	h.decideLandlord(appKey, "declined")

	// A declined application is terminal-not-violating: declined true, the unit stays
	// available (never leased), and violating settles false (no work remains).
	require.Eventually(t, func() bool {
		row := h.readRow(appID)
		return row != nil && rowBool(row, "declined") && !rowBool(row, "violating")
	}, 30*time.Second, 150*time.Millisecond, "a landlord-declined application must read declined + non-violating (terminal)")

	// Hold: the unit must NOT lease, and the terminal state is stable.
	cut := time.Now().Add(3 * time.Second)
	for time.Now().Before(cut) {
		require.Equal(t, "available", h.unitListingStatus(h.lastUnitKey), "a declined application must NEVER lease its unit")
		row := h.readRow(appID)
		require.NotNil(t, row)
		require.Truef(t, rowBool(row, "declined"), "stays declined; row=%v", row)
		require.Truef(t, rowBool(row, "landlordDeclined"), "the decline is the landlord's; row=%v", row)
		require.Falsef(t, rowBool(row, "missing_decision"), "a recorded decision closes the awaiting-decision gap; row=%v", row)
		require.Falsef(t, rowBool(row, "missing_listingLeased"), "declined ≠ approved → no listing flip; row=%v", row)
		require.Falsef(t, rowBool(row, "violating"), "terminal — no work remains; row=%v", row)
		time.Sleep(150 * time.Millisecond)
	}
}

// TestLeaseConvergence_D5_OutcomeInAspect_RootMinimal is AC #3, gate-asserted (not
// review-asserted): after the bridge round-trip, the service instance's external
// outcome lives in the .outcome ASPECT, and the service + leaseapp vertex ROOT data
// stays minimal ({}). A regression that fattens root data fails this gate.
func TestLeaseConvergence_D5_OutcomeInAspect_RootMinimal(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()
	h.driveApplicantSteps(appKey, applicantKey)
	h.decideLandlord(appKey, "approved")
	h.drainUntilConverged(appID, 30*time.Second)

	applicantID := applicantKey[len("vtx.identity."):]
	svcKeys := h.serviceOutcomes(applicantID)
	require.Len(t, svcKeys, 2, "two service instances (bgcheck + payment) providedTo the applicant")

	for _, svcKey := range svcKeys {
		// (a) the outcome lives in the .outcome aspect.
		outcome := h.aspectData(svcKey, "outcome")
		require.NotNilf(t, outcome, "%s.outcome aspect must carry the external outcome", svcKey)
		require.Equal(t, "completed", outcome["status"], "outcome aspect status")
		require.NotEmpty(t, outcome["completedAt"], "outcome aspect completedAt")
		require.NotEmpty(t, outcome["validUntil"], "outcome aspect validUntil")

		// (b) the service vertex ROOT data is minimal ({}) — the outcome is NOT on root.
		root, ok := h.vertexRootData(svcKey)
		require.Truef(t, ok, "%s vertex must exist", svcKey)
		require.Emptyf(t, root, "%s root data must be minimal (D5), got %v", svcKey, root)
	}

	// (c) the leaseapp root data is {} — the signature is in the .signature aspect.
	appRoot, ok := h.vertexRootData(appKey)
	require.True(t, ok, "leaseapp vertex must exist")
	require.Emptyf(t, appRoot, "leaseapp root data must be minimal (D5), got %v", appRoot)
	require.NotNil(t, h.aspectData(appKey, "signature"), "the signature must live in the .signature aspect")
}

// TestLeaseConvergence_FR58_RetriedExternalCall_AtMostOnce is AC #2 (first clause),
// end-to-end through the live bridge: after convergence, REPUBLISH one of the
// external.<adapter> events (the same instanceKey → same deriveReplyRequestID →
// Contract #4 tracker collapse / create-only .outcome conflict). Assert NO second
// external effect lands — exactly one charge on the adapter and exactly one
// .outcome aspect for that service throughout.
func TestLeaseConvergence_FR58_RetriedExternalCall_AtMostOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()
	h.driveApplicantSteps(appKey, applicantKey)
	h.decideLandlord(appKey, "approved")
	h.drainUntilConverged(appID, 30*time.Second)

	applicantID := applicantKey[len("vtx.identity."):]
	before := h.countOutcomeAspects(applicantID)
	require.Equal(t, 2, before, "two outcomes recorded before the retry")

	// Re-drive the bgcheck external event (same instanceKey → same reply requestId).
	bgHandle := h.bgcheckHandle(applicantID)
	require.NotEmpty(t, bgHandle, "must find the bgcheck service handle")
	chargesBefore := h.bgFake.SideEffects(bgHandle)
	skippedBefore := h.bridgeSkipped()
	h.republishExternalEvent("backgroundCheck", bgHandle)

	// POSITIVE CONTROL: the republished event must be OBSERVED + deduped by the
	// bridge, not silently dropped as garbage — otherwise the require.Never below
	// passes vacuously. The bridge's skip-on-redelivery probe sees the replyOp's
	// deterministic requestId already landed and short-circuits, incrementing
	// metrics.skipped in its Contract #5 heartbeat. A malformed event would hit the
	// event:* Ack path and NOT bump skipped — so this Eventually fails if the
	// redelivery never reached the bridge or was rejected at parse.
	require.Eventually(t, func() bool {
		return h.bridgeSkipped() > skippedBefore
	}, 10*time.Second, 150*time.Millisecond,
		"the bridge must OBSERVE + dedup the redelivered event (metrics.skipped must increment) — else the at-most-once assertion is vacuous")

	// No second external effect: the charge count and the outcome-aspect count both
	// stay put (the deterministic requestId collapses the replyOp; the create-only
	// .outcome rejects the redelivery; the adapter dedups on idempotencyKey).
	require.Never(t, func() bool {
		return h.bgFake.SideEffects(bgHandle) > chargesBefore || h.countOutcomeAspects(applicantID) > before
	}, 5*time.Second, 150*time.Millisecond,
		"a redelivered external event must not double-act (no second charge, no second outcome)")

	require.Equal(t, chargesBefore, h.bgFake.SideEffects(bgHandle), "exactly one charge for the bgcheck under retry")
	require.Equal(t, 2, h.countOutcomeAspects(applicantID), "still exactly two outcomes after the retry")

	// The application stays converged through the retry.
	h.assertSteadyState(appID, 2*time.Second)
}

// TestLeaseConvergence_BgcheckFreshness_EagerReopen is AC #2 (second clause),
// asserting the FULL eager chain end-to-end (the MarkExpired DDL re-touches the
// entity so the stale-freshness gap re-opens eagerly): after convergence the lens projects the
// bgcheck's validUntil as the scalar freshUntil; Weaver's temporal lane arms the
// per-target-per-entity @at schedule at that instant; the short window lapses;
// the NATS scheduler republishes to the fired subject; Weaver converts the
// firing into a MarkExpired op; the generic freshnessMarker DDL writes the marker
// aspect on the application (an UNCONDITIONED update); Refractor reprojects the
// row with a fresh $now; missing_bgcheck re-opens; Weaver re-dispatches the
// bgcheck externalTask; the live bridge re-completes it with exactly ONE new
// external call; the row re-converges.
//
// It then runs a SECOND freshness cycle (C2): lapse → re-open → re-converge,
// again with exactly ONE new external call — proving the eager re-open is not a
// one-shot (the unconditioned marker write overwrites cleanly on the second
// lapse, where a create-based marker would conflict and silently stop reprojecting).
//
// Runs ONLY under -tags leaseshortwindow (the gate), where the freshness window
// is short enough to watch two lapses in bounded wall-clock.
func TestLeaseConvergence_BgcheckFreshness_EagerReopen(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	if !shortFreshnessWindow {
		t.Skip("eager-freshness leg requires -tags leaseshortwindow (a window short enough to watch a lapse)")
	}
	h := newHarness(t)
	appKey, appID, applicantKey := h.seedApplicant()
	applicantID := applicantKey[len("vtx.identity."):]

	// Persistently count MarkExpired ops for this app across BOTH cycles, from
	// before the first converge. A per-cycle non-durable subscription would miss a
	// firing published between two cycle assertions; one standing subscription
	// never does.
	marks := h.startMarkExpiredCounter(appKey)

	h.driveApplicantSteps(appKey, applicantKey)
	h.decideLandlord(appKey, "approved")
	h.drainUntilConverged(appID, 30*time.Second)

	// The converged row carries freshUntil (the bgcheck's validUntil) — the column
	// Weaver's temporal lane schedules the @at from. Distinguishes eager from lazy:
	// without this scalar there is nothing to arm a timer on.
	row := h.readRow(appID)
	require.NotNil(t, row)
	freshUntil, _ := row["freshUntil"].(string)
	require.NotEmpty(t, freshUntil, "the converged row must carry a freshUntil scalar (the eager-arm input)")

	// One bgcheck call so far (the initial converge).
	require.Equal(t, 1, h.countBgcheckOutcomes(applicantID), "exactly one bgcheck outcome after the initial converge")
	require.Equal(t, 1, h.totalBgcheckSideEffects(applicantID), "exactly one bgcheck external call after the initial converge")

	// Two full freshness cycles, each: lapse → MarkExpired → re-open → re-dispatch
	// → re-converge, each adding EXACTLY ONE new bgcheck call. The second cycle is
	// the C2 assertion: the eager re-open is NOT a one-shot (the unconditioned
	// marker write overwrites cleanly on the second lapse, where a create-based
	// marker would conflict and silently stop reprojecting).
	h.assertEagerReopenCycle(appKey, appID, applicantID, 1)
	h.assertEagerReopenCycle(appKey, appID, applicantID, 2)

	// At least one MarkExpired per cycle drove the re-opens (the @at → MarkExpired
	// dispatch path actually fired — not an incidental CDC touch). Counted on the
	// standing subscription, so neither firing was missed.
	require.GreaterOrEqualf(t, marks.seen(), 2, "expected at least one MarkExpired per freshness cycle (got %d)", marks.seen())
}

// assertEagerReopenCycle drives one eager freshness cycle and asserts it produces
// EXACTLY ONE new bgcheck external call, CAUSALLY attributed to a committed
// MarkExpired. cycle is the 1-based cycle number; the bgcheck-call count before
// this cycle equals the cycle number (1 from the initial converge + (cycle-1)
// prior re-opens), so after it the count is cycle+1.
//
// Two witnesses run together, and the test FAILS if MarkExpired→reproject is
// broken (e.g. a class-inference regression for MarkExpired, or the marker write
// being rejected):
//
//   - CAUSAL (H2/H3): the freshnessExpiry MARKER aspect that MarkExpired commits
//     onto the application must ADVANCE this cycle — its KV revision strictly
//     increases (every committed marker write bumps it) AND its data.expiredAt
//     advances to this cycle's later fireAt. A LAZY re-open (an incidental CDC
//     touch re-running the cypher) never submits MarkExpired, so it could NOT move
//     the marker — confounding the old count-only witness. This proves THIS
//     cycle's MarkExpired actually COMMITTED (not merely submitted to ops.system).
//   - the bgcheck-call COUNT then increments by exactly +1: a new external call
//     can only happen if the marker-triggered reprojection re-opened the gap and
//     Weaver re-dispatched the externalTask exactly once (no storm — FR58). The
//     count is durable KV state, never missed.
func (h *harness) assertEagerReopenCycle(appKey, appID, applicantID string, cycle int) {
	h.t.Helper()
	prior := cycle // bgcheck calls before this cycle == the cycle number

	// Snapshot the committed marker BEFORE this cycle's lapse. Cycle 1 starts with
	// no marker ("", 0); later cycles carry the prior fire's marker.
	beforeExpiredAt, beforeRev := h.freshnessMarker(appKey)

	// Weaver re-arms the @at from the (re-)projected freshUntil each converge.
	schedSubject := "schedule.weaver.timer.leaseApplicationComplete." + appID
	require.Eventuallyf(h.t, func() bool {
		return h.scheduleArmed(schedSubject)
	}, 30*time.Second, 200*time.Millisecond, "cycle %d: Weaver must arm the @at freshness schedule", cycle)

	// The window lapses → fired subject → Weaver submits MarkExpired → the generic
	// freshnessMarker DDL COMMITS the marker aspect (an unconditioned update,
	// bumping its revision + writing this fire's later expiredAt) → Refractor
	// reprojects with a fresh $now → the lapsed validUntil makes missing_bgcheck
	// re-open → Weaver re-dispatches the bgcheck externalTask → the live bridge
	// re-completes it. The @at fires one freshness window after the prior converge,
	// so the wait budget is the window plus a generous margin (the freshness window
	// is bgcheckFreshnessWindow under -tags leaseshortwindow; the budget must absorb
	// the full window and CI scheduling variance under the load of the engines +
	// bridge re-running the externalTask).
	//
	// CAUSAL gate FIRST: wait for the committed marker to ADVANCE (revision bump +
	// later expiredAt). This is the proof a NEW MarkExpired committed this cycle —
	// if MarkExpired→commit is broken, this Eventually times out and the test fails
	// here, before the (potentially lazy) bgcheck count is consulted.
	require.Eventuallyf(h.t, func() bool {
		gotExpiredAt, gotRev := h.freshnessMarker(appKey)
		return gotRev > beforeRev && gotExpiredAt != "" && gotExpiredAt > beforeExpiredAt
	}, 240*time.Second, 500*time.Millisecond,
		"cycle %d: the freshnessExpiry marker must advance (a NEW MarkExpired must COMMIT this cycle — revision bump + later expiredAt); before rev=%d expiredAt=%q",
		cycle, beforeRev, beforeExpiredAt)

	// Then the bgcheck re-dispatch + re-completion the committed marker triggered.
	require.Eventuallyf(h.t, func() bool {
		return h.countBgcheckOutcomes(applicantID) >= prior+1
	}, 240*time.Second, 500*time.Millisecond,
		"cycle %d: a new bgcheck must be dispatched + completed after the marker advance (the eager re-open → re-converge)", cycle)

	// Exactly +1 (no storm): one re-open → one re-dispatch → one external call.
	require.Equalf(h.t, prior+1, h.countBgcheckOutcomes(applicantID),
		"cycle %d: exactly one NEW bgcheck outcome (re-dispatch minted exactly one fresh instance)", cycle)
	require.Equalf(h.t, prior+1, h.totalBgcheckSideEffects(applicantID),
		"cycle %d: exactly one NEW bgcheck external call this cycle (no double-dispatch)", cycle)

	// And it settles back to converged (no residual violation after the re-converge).
	h.drainUntilConverged(appID, 30*time.Second)
}
