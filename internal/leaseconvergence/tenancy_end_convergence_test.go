//go:build leaseshortwindow

// Package leaseconvergence_test — the R2 tenancy-end proof (design
// loftspace-lease-term-and-tenancy-end-design.md §2.2): a signed,
// landlord-approved lease whose term has already ended converges through the
// REAL Weaver temporal lane — an already-overdue @at, published verbatim and
// released at once (the leaseExpiry idiom) — to a recorded .tenancy.endedAt
// and a relisted unit, and the relist survives a second tenant leasing the
// same unit (the otherLiveTenancyCount conjunct that keeps a terminal row
// terminal).
package leaseconvergence_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
)

// seedTenancyEndApplication mints a landlord-owned unit and a lease
// application whose declared .terms places the WHOLE term in the past
// (moveInDate two years back, a 12-month leaseTermMonths) — so the FIRST
// approve derives a .tenancy whose leaseEnd already precedes $now, and the
// tenancyEnd target's row projects an already-lapsed freshUntil the instant
// it is activated (the leaseExpiry idiom: an overdue @at is published
// verbatim and NATS releases it at once).
func (h *harness) seedTenancyEndApplication(label string, requestedRent float64) (appKey, appID, applicantKey, unitKey, moveInDate string) {
	h.t.Helper()
	// Truncated to whole seconds so it is byte-identical to time.rfc3339_utc's
	// canonical re-emission (whole seconds, "Z" suffix) and can be compared to
	// the recorded .tenancy.leaseStart directly.
	moveInDate = time.Now().UTC().AddDate(-2, 0, 0).Truncate(time.Second).Format(time.RFC3339)
	appKey, appID, applicantKey, unitKey = h.seedTenancyApplicationFrom(label, requestedRent, moveInDate)
	return appKey, appID, applicantKey, unitKey, moveInDate
}

// seedTenancyApplicationFrom mints a landlord-owned unit and a lease
// application whose declared .terms start at moveInDate with a 12-month
// leaseTermMonths — the caller picks where the term sits relative to $now.
// The listing's own rentAmount is deliberately set apart from requestedRent
// so a tenancy.rentAmount that matches requestedRent proves R1's terms-first
// derivation rather than an accidental fallback to the listing.
func (h *harness) seedTenancyApplicationFrom(label string, requestedRent float64, moveInDate string) (appKey, appID, applicantKey, unitKey string) {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("tenancyend-applicant-claim-" + label + "-" + mustNanoID(h.t)))
	idReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Tenancy End Tenant " + label,
		"email":        "tenancyend-" + label + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(claimSum[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, idReply.Status, "CreateUnclaimedIdentity(%s): %+v", label, idReply.Error)
	applicantKey = idReply.PrimaryKey

	unitReply := h.submitOp("CreateLocation", "unit", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"locationType": "unit",
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, unitReply.Status, "CreateLocation(%s): %+v", label, unitReply.Error)
	unitKey = unitReply.PrimaryKey

	addrReply := h.submitOp("SetUnitAddress", "loftspaceListing", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"unit": unitKey, "line1": "1 Tenancy End Way", "city": "Springfield", "region": "OR", "postal": "97477",
	}, &processor.ContextHint{Reads: []string{unitKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, addrReply.Status, "SetUnitAddress(%s): %+v", label, addrReply.Error)

	// The listing's OWN rentAmount is requestedRent-200 — different from the
	// .terms requestedRent below — so the recorded tenancy.rentAmount can only
	// match requestedRent if the derivation actually read .terms first.
	listingReply := h.submitOp("SetListing", "loftspaceListing", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"unit": unitKey, "rentAmount": requestedRent - 200, "rentCurrency": "USD", "bedrooms": 1,
		"availableFrom": "2020-01-01T00:00:00Z", "leaseTermMonths": 12, "status": "available",
	}, &processor.ContextHint{Reads: []string{unitKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, listingReply.Status, "SetListing(%s): %+v", label, listingReply.Error)

	// A real landlord must manage the unit before DecideLeaseApplication's own
	// require_manages guard admits a decision (seedApplicant's precedent).
	landlordClaim := sha256.Sum256([]byte("tenancyend-landlord-claim-" + label + "-" + mustNanoID(h.t)))
	landlordReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Tenancy End Landlord " + label,
		"email":        "tenancyend-landlord-" + label + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(landlordClaim[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, landlordReply.Status, "CreateUnclaimedIdentity(landlord %s): %+v", label, landlordReply.Error)
	landlordKey := landlordReply.PrimaryKey
	ownerReply := h.submitOp("AssignUnitOwner", "loftspaceOwnership", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"landlord": landlordKey, "unit": unitKey,
	}, &processor.ContextHint{Reads: []string{landlordKey, unitKey},
		Enumerations: testutil.DeclaredEnumerations("AssignUnitOwner", bootstrap.BootstrapIdentityKey, loftspacedomain.OpMetas())})
	require.Equalf(h.t, processor.ReplyStatusAccepted, ownerReply.Status, "AssignUnitOwner(%s): %+v", label, ownerReply.Error)

	appReply := h.submitOp("CreateLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"applicant": applicantKey, "unit": unitKey,
		"moveInDate": moveInDate, "leaseTermMonths": 12, "requestedRent": requestedRent,
	}, &processor.ContextHint{
		Reads: []string{applicantKey, unitKey},
		OptionalReads: []string{
			"lnk.identity." + applicantKey[len("vtx.identity."):] + ".appliedToUnit.unit." + unitKey[len("vtx.unit."):],
			unitKey + ".listing",
		},
	})
	require.Equalf(h.t, processor.ReplyStatusAccepted, appReply.Status, "CreateLeaseApplication(%s): %+v", label, appReply.Error)
	appKey = appReply.PrimaryKey
	appID = appKey[len("vtx.leaseapp."):]
	return appKey, appID, applicantKey, unitKey
}

// applyForUnit mints a SECOND applicant + application against an
// ALREADY-owned unit (the first application's landlord manages link covers
// it too) with a term that starts now and runs a full year — safely fresh,
// so leg 4 proves a genuine re-lease rather than tripping the same
// overdue-@at path a second time.
func (h *harness) applyForUnit(label, unitKey string, requestedRent float64) (appKey, appID, applicantKey string) {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("tenancyend-applicant2-claim-" + label + "-" + mustNanoID(h.t)))
	idReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Tenancy End Tenant " + label,
		"email":        "tenancyend2-" + label + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(claimSum[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, idReply.Status, "CreateUnclaimedIdentity(%s): %+v", label, idReply.Error)
	applicantKey = idReply.PrimaryKey

	moveInDate := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	appReply := h.submitOp("CreateLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"applicant": applicantKey, "unit": unitKey,
		"moveInDate": moveInDate, "leaseTermMonths": 12, "requestedRent": requestedRent,
	}, &processor.ContextHint{
		Reads: []string{applicantKey, unitKey},
		OptionalReads: []string{
			"lnk.identity." + applicantKey[len("vtx.identity."):] + ".appliedToUnit.unit." + unitKey[len("vtx.unit."):],
			unitKey + ".listing",
		},
	})
	require.Equalf(h.t, processor.ReplyStatusAccepted, appReply.Status, "CreateLeaseApplication(%s): %+v", label, appReply.Error)
	appKey = appReply.PrimaryKey
	appID = appKey[len("vtx.leaseapp."):]
	return appKey, appID, applicantKey
}

// approveAndDrain drives the applicant to fully qualified, submits the
// landlord's approve against unitKey, and drains the WHOLE row to
// violating=false — unlike approveWithTenancy's settle-wait (which polls only
// missing_listingLeased and can observe it false BEFORE the applicant gaps
// ever open, returning prematurely), drainUntilConverged cannot pass until
// every gap — onboarding, bgcheck, payment, signature, the listing flip, the
// executed-lease document chain — has actually closed, so a caller sees the
// unit genuinely leased when this returns.
func (h *harness) approveAndDrain(appKey, applicantKey, unitKey string) {
	h.t.Helper()
	h.driveApplicantSteps(appKey, applicantKey)
	appID := appKey[len("vtx.leaseapp."):]
	decideReply := h.submitOp("DecideLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "decision": "approved", "unit": unitKey,
	}, &processor.ContextHint{Reads: []string{appKey, unitKey},
		Enumerations: testutil.DeclaredEnumerations("DecideLeaseApplication", bootstrap.BootstrapIdentityKey, leasesigning.OpMetas())})
	require.Equalf(h.t, processor.ReplyStatusAccepted, decideReply.Status, "DecideLeaseApplication(approved): %+v", decideReply.Error)
	h.drainUntilConverged(appID, 45*time.Second)
}

// startOpCounter is startMarkExpiredCounter generalized to any operationType
// Weaver dispatches under its "system" lane — the exactly-once-dispatch
// witness for EndTenancy, mirroring the bgcheck "no storm" idiom (assertEagerReopenCycle).
// match filters by payload so two applications on the same unit are counted
// separately.
func (h *harness) startOpCounter(operationType string, match func(payload map[string]any) bool) *markExpiredCounter {
	h.t.Helper()
	var n int64
	c := &markExpiredCounter{count: &n}
	sub, err := h.conn.NATS().Subscribe("ops.system", func(msg *nats.Msg) {
		var env struct {
			OperationType string         `json:"operationType"`
			Payload       map[string]any `json:"payload"`
		}
		if json.Unmarshal(msg.Data, &env) != nil {
			return
		}
		if env.OperationType == operationType && match(env.Payload) {
			atomic.AddInt64(c.count, 1)
		}
	})
	require.NoError(h.t, err)
	c.sub = sub
	h.t.Cleanup(func() { _ = sub.Unsubscribe() })
	return c
}

// TestLeaseConvergence_EndedTenancyRelistsTheUnit is the R2 tenancy-end
// capstone, proving the whole loop through the REAL Weaver:
//
//  1. R1 end-to-end: a signed, landlord-approved application derives its
//     .tenancy from its OWN .terms (moveInDate/leaseTermMonths/requestedRent),
//     not the listing — .tenancy.leaseStart == moveInDate, rentAmount ==
//     requestedRent, and the unit leases.
//  2. R2's overdue path: the seeded term already ended, so once the
//     tenancyEnd target activates its row projects an already-lapsed
//     freshUntil, Weaver's temporal lane publishes the overdue @at, NATS
//     releases it at once, MarkExpired records the lapse on the leaseapp
//     itself, missing_tenancyEnded opens, EndTenancy records
//     .tenancy.endedAt == leaseEnd, missing_relist opens, and
//     SetListingStatus relists the unit as available — economics (the
//     listing's own rentAmount) untouched.
//  3. Steady state: leaseApplicationComplete's row reads
//     tenancyEndedAt/missing_listingLeased/violating all consistent with a
//     terminal, non-violating tenancy, and the tenancyEnd row itself reads
//     missing_relist/violating false with freshUntil null (nothing left to
//     watch) — held across a settle window with no oscillation. EndTenancy
//     was accepted exactly once (counted by operationType off ops.system —
//     the harness's Weaver dispatch lane).
//  4. A second applicant leases the SAME unit; the first (ended) row's
//     missing_relist must never re-open (otherLiveTenancyCount holds it) —
//     the load-bearing conjunct that keeps a terminal row terminal.
func TestLeaseConvergence_EndedTenancyRelistsTheUnit(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}

	// tenancyEnd starts OFF: with it live from boot, its overdue @at can fire
	// WHILE leg 1's own applicant gaps are still closing (both paths are
	// event-driven off the same CDC stream, so their relative speed is not a
	// guarantee), racing leg 1's "the unit leases" proof against leg 2's "the
	// unit relists" — exactly the deploy-window shape
	// TestLeaseConvergence_BgcheckFreshness_LapsedBeforeTheTargetExisted
	// exists to drive on purpose. Activating it only after leg 1 converges
	// (activateActorAggregateLensNow, that test's own late-activation path)
	// gives leg 1 a clean read and still proves the SAME overdue-@at
	// mechanism for leg 2: the target arrives at a graph whose leaseEnd has
	// already passed, exactly like a freshly deployed Refractor would.
	h := newHarness(t)

	const requestedRentA = 2200.0
	appKeyA, appIDA, applicantA, unitKey, moveInDateA := h.seedTenancyEndApplication("A", requestedRentA)

	// --- leg 1: R1 proven end-to-end (tenancyEnd is not yet watching) ---
	h.approveAndDrain(appKeyA, applicantA, unitKey)

	require.Equal(t, "leased", h.unitListingStatus(unitKey), "the approved application's unit must be marked leased")
	tenancyA := h.aspectData(appKeyA, "tenancy")
	require.NotNil(t, tenancyA, ".tenancy must exist after the first approve")
	require.Equal(t, moveInDateA, tenancyA["leaseStart"], "leaseStart must equal the applicant's own moveInDate (R1: derived from .terms)")
	require.EqualValues(t, requestedRentA, tenancyA["rentAmount"], "rentAmount must equal the applicant's requestedRent, not the listing's own rentAmount")
	leaseEndA, _ := tenancyA["leaseEnd"].(string)
	require.NotEmpty(t, leaseEndA, "leaseEnd must be recorded")
	require.Lessf(t, leaseEndA, time.Now().UTC().Format(time.RFC3339), "the seeded term must already be over; leaseEnd=%s", leaseEndA)

	// --- leg 2: activate tenancyEnd on a graph whose leaseEnd has already
	// passed — the overdue @at fires at once; EndTenancy then the relist ---
	endTenancyCount := h.startOpCounter("EndTenancy", func(p map[string]any) bool {
		return p["leaseAppKey"] == appKeyA
	})
	marks := h.startMarkExpiredCounter(leasesigning.TenancyEndTarget)
	h.activateActorAggregateLensNow(h.ctx, leasesigning.TenancyEndTarget)

	require.Eventuallyf(t, func() bool {
		tenancy := h.aspectData(appKeyA, "tenancy")
		return tenancy != nil && tenancy["endedAt"] != nil
	}, 45*time.Second, 200*time.Millisecond, "EndTenancy must record .tenancy.endedAt once the overdue @at fires")

	tenancyEnded := h.aspectData(appKeyA, "tenancy")
	require.Equal(t, leaseEndA, tenancyEnded["endedAt"], "endedAt must equal the recorded leaseEnd, not the instant EndTenancy happened to run")

	require.Eventuallyf(t, func() bool {
		return h.unitListingStatus(unitKey) == "available"
	}, 45*time.Second, 200*time.Millisecond, "the ended tenancy's unit must relist as available")

	listingAfterEnd := h.aspectData(unitKey, "listing")
	require.NotNil(t, listingAfterEnd, "the .listing aspect must still exist after the status flip")
	require.EqualValues(t, requestedRentA-200, listingAfterEnd["rentAmount"], "the relist is a status-only flip; the listing's own rentAmount is preserved")

	require.GreaterOrEqualf(t, marks.seen(), 1, "at least one MarkExpired must have recorded the overdue lapse for tenancyEnd")

	// missing_residenceUnwired is a SEPARATE directOp (UnwireResidesIn) and
	// reprojection from the relist's SetListingStatus flip above — the two
	// close on their own schedules, so the steady-state loop below must not
	// assume the residence gap has already closed just because the unit has
	// already relisted.
	require.Eventuallyf(t, func() bool {
		row := h.readRow(appIDA)
		tRow := h.weaverTargetRow(leasesigning.TenancyEndTarget, appIDA)
		return row != nil && !rowBool(row, "violating") && tRow != nil && !rowBool(tRow, "violating")
	}, 45*time.Second, 200*time.Millisecond,
		"both the leaseApplicationComplete and tenancyEnd rows must converge (violating=false) before steady state")

	// --- leg 3: steady state, no oscillation ---
	cut := time.Now().Add(5 * time.Second)
	for time.Now().Before(cut) {
		row := h.readRow(appIDA)
		require.NotNil(t, row, "the leaseApplicationComplete row must remain present")
		require.Falsef(t, rowBool(row, "missing_listingLeased"), "missing_listingLeased must stay false; row=%v", row)
		require.Falsef(t, rowBool(row, "violating"), "violating must stay false; row=%v", row)
		tea, _ := row["tenancyEndedAt"].(string)
		require.NotEmptyf(t, tea, "tenancyEndedAt must be recorded on the leaseApplicationComplete row; row=%v", row)

		tRow := h.weaverTargetRow(leasesigning.TenancyEndTarget, appIDA)
		require.NotNil(t, tRow, "the tenancyEnd row must remain present")
		require.Falsef(t, rowBool(tRow, "missing_relist"), "missing_relist must stay false; tenancyEnd row=%v", tRow)
		require.Falsef(t, rowBool(tRow, "violating"), "violating must stay false; tenancyEnd row=%v", tRow)
		require.Nilf(t, tRow["freshUntil"], "freshUntil must be null once ended (nothing left to watch); tenancyEnd row=%v", tRow)

		require.Equal(t, "available", h.unitListingStatus(unitKey), "the unit must stay available at steady state")
		time.Sleep(150 * time.Millisecond)
	}

	require.Equalf(t, 1, endTenancyCount.seen(),
		"EndTenancy must be accepted exactly once (idempotent no-op thereafter; no re-dispatch storm)")

	// --- leg 4: a second tenant re-leases the same unit; the first (ended)
	// row's missing_relist must never re-open ---
	appKeyB, _, applicantB := h.applyForUnit("B", unitKey, 2500)
	h.approveAndDrain(appKeyB, applicantB, unitKey)

	require.Equal(t, "leased", h.unitListingStatus(unitKey), "the second application leases the unit again")

	holdCut := time.Now().Add(5 * time.Second)
	for time.Now().Before(holdCut) {
		tRow := h.weaverTargetRow(leasesigning.TenancyEndTarget, appIDA)
		require.NotNil(t, tRow, "the first (ended) tenancyEnd row must remain present")
		require.Falsef(t, rowBool(tRow, "missing_relist"),
			"the first (ended) row's missing_relist must never re-open once another tenant holds the unit (otherLiveTenancyCount); tenancyEnd row=%v", tRow)
		require.Equal(t, "leased", h.unitListingStatus(unitKey), "the re-leased unit must not be flipped back to available")
		time.Sleep(150 * time.Millisecond)
	}
}

// TestLeaseConvergence_ApprovalWiresResidenceThenTenancyEndUnwiresIt is the
// residence-spine e2e proof (loftspace-residence-spine-2026-09-16.md decisions
// 2+3), through the REAL Weaver: an approved, signed lease wires the
// applicant's residesIn link to the unit the instant the term exists
// (missing_residence → directOp WireResidesIn), and the ended term releases
// it (missing_residenceUnwired → directOp UnwireResidesIn) — the mirror of
// the relist proof above, on the same overdue-@at mechanism.
func TestLeaseConvergence_ApprovalWiresResidenceThenTenancyEndUnwiresIt(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	h := newHarness(t)

	const requestedRent = 2150.0
	appKey, appID, applicantKey, unitKey, _ := h.seedTenancyEndApplication("Residence", requestedRent)
	residesInKey := "lnk.identity." + applicantKey[len("vtx.identity."):] + ".residesIn.unit." + unitKey[len("vtx.unit."):]

	require.False(t, h.linkLive(residesInKey), "no residence before the term is even approved")

	// --- approval wires the residence link ---
	h.approveAndDrain(appKey, applicantKey, unitKey)

	require.True(t, h.linkLive(residesInKey), "an approved, live term must wire the applicant's residesIn link to the unit")
	row := h.readRow(appID)
	require.NotNil(t, row, "the leaseApplicationComplete row must remain present")
	require.Falsef(t, rowBool(row, "missing_residence"), "missing_residence must close once WireResidesIn lands; row=%v", row)
	require.Falsef(t, rowBool(row, "violating"), "violating must stay false once every gap — including the new residence one — has closed; row=%v", row)

	// --- the seeded term is already over: activating tenancyEnd fires the
	// overdue @at at once, ends the term, and must unwire the residence link ---
	h.activateActorAggregateLensNow(h.ctx, leasesigning.TenancyEndTarget)

	require.Eventuallyf(t, func() bool {
		tenancy := h.aspectData(appKey, "tenancy")
		return tenancy != nil && tenancy["endedAt"] != nil
	}, 45*time.Second, 200*time.Millisecond, "EndTenancy must record .tenancy.endedAt once the overdue @at fires")

	require.Eventuallyf(t, func() bool {
		return !h.linkLive(residesInKey)
	}, 45*time.Second, 200*time.Millisecond, "the ended term's residesIn link must be tombstoned by UnwireResidesIn")

	require.Eventuallyf(t, func() bool {
		tRow := h.weaverTargetRow(leasesigning.TenancyEndTarget, appID)
		return tRow != nil && !rowBool(tRow, "missing_residenceUnwired") && !rowBool(tRow, "violating")
	}, 45*time.Second, 200*time.Millisecond, "missing_residenceUnwired must close (and stay converged) once UnwireResidesIn lands")

	// --- steady state: the tombstoned link never revives on its own, and the
	// gap never re-opens on redelivery ---
	cut := time.Now().Add(5 * time.Second)
	for time.Now().Before(cut) {
		require.Falsef(t, h.linkLive(residesInKey), "the residence link must stay tombstoned at steady state")
		tRow := h.weaverTargetRow(leasesigning.TenancyEndTarget, appID)
		require.NotNil(t, tRow, "the tenancyEnd row must remain present")
		require.Falsef(t, rowBool(tRow, "missing_residenceUnwired"), "missing_residenceUnwired must not re-open; tenancyEnd row=%v", tRow)
		time.Sleep(150 * time.Millisecond)
	}
}
