package leasesigning

// Rule-engine proof of the tenancyEnd convergence cypher — the leaseapp-anchored
// term-end horizon and the relist that follows it.
//
// Like leaseExpiry's, what decides "the term ended" is a recorded FACT, not a
// clock: the instant the @at this lens armed actually fired, recorded on the
// leaseapp under this target's own byTarget key, compared against the stored
// leaseEnd. No $now is supplied to any vector below — passing one would let a
// clock-reading regression pass unnoticed.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectTenancyEnd runs tenancyEndSpec anchored on the named leaseapp.
func (f *lensFixture) projectTenancyEnd(t *testing.T, appName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(tenancyEndSpec)
	require.NoError(t, err, "tenancyEnd cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.leaseapp." + f.ids[appName],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per leaseapp anchor")
	return out[0].Values
}

// teLeaseEnd is the term end every vector below stamps; seedSignedTenancy
// (lease_expiry_lens_test.go) writes it as the .tenancy leaseEnd.
const teLeaseEnd = "2027-01-01T00:00:00Z"

// seedLeasedTenancy seeds a signed, approved tenancy on a unit whose listing
// reads the given status — the shape the listing-flip already drove the unit
// to before the term could end.
func seedLeasedTenancy(t *testing.T, f *lensFixture, appName, unitStatus string) {
	t.Helper()
	seedSignedTenancy(t, f, appName, "2026-11-02T00:00:00Z")
	f.aspect(t, appName+"_unit", "listing", "listing", map[string]any{"rentAmount": 2400, "status": unitStatus})
}

// endTenancy rewrites the .tenancy the way EndTenancy commits it: every field
// preserved, endedAt = leaseEnd.
func endTenancy(t *testing.T, f *lensFixture, appName string) {
	t.Helper()
	f.aspect(t, appName, "tenancy", "tenancy", map[string]any{
		"leaseEnd": teLeaseEnd, "renewalOpensAt": "2026-11-02T00:00:00Z", "endedAt": teLeaseEnd})
}

// TestTenancyEnd_ArmsWhileNoLapseRecorded is the arming vector: a live,
// signed, approved tenancy no timer has fired on projects its leaseEnd as
// freshUntil — the scalar Weaver schedules the @at from — and opens nothing.
// The end is deliberately in the PAST relative to any wall clock the suite runs
// at, so a clock-reading form would end the term here, which is what makes the
// vector discriminating.
func TestTenancyEnd_ArmsWhileNoLapseRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, teLeaseEnd, v["freshUntil"], "no recorded lapse → the end is still armed, whatever the wall clock says")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, false, v["missing_tenancyEnded"], "nothing has fired yet, so the term does not end until the marker lands")
	require.Equal(t, false, v["missing_relist"])
	require.Equal(t, false, v["violating"])
	require.Equal(t, "vtx.leaseapp."+f.ids["app"], v["entityKey"])
	require.Equal(t, "vtx.unit."+f.ids["app_unit"], v["unitKey"])
}

// TestTenancyEnd_LapseAtEndOpensTheEnd is the gap vector: a recorded lapse at
// (or after) leaseEnd with no renewal opens missing_tenancyEnded and disarms
// the timer — one @at fire per term, not one per delivery.
func TestTenancyEnd_LapseAtEndOpensTheEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, true, v["missing_tenancyEnded"], "marker == leaseEnd is a lapse (>= boundary): the term ended")
	require.Equal(t, false, v["missing_relist"], "nothing recorded the end yet, so there is nothing to relist on")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "and the horizon disarms")
}

// TestTenancyEnd_OpenRenewalHoldsTheTerm: an OPEN renewal for THIS cycle keeps
// the term alive past its recorded lapse — the parties are mid-negotiation, and
// a signed renewal would extend leaseEnd rather than end it.
func TestTenancyEnd_OpenRenewalHoldsTheTerm(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})
	f.vtx(t, "rn", "renewal")
	f.setRootData(t, "rn", map[string]any{"status": "open", "cycleEnd": teLeaseEnd})
	f.edge(t, "renews", "rn", "app")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_tenancyEnded"], "an open renewal for this term holds it")
	require.Equal(t, false, v["violating"])
}

// TestTenancyEnd_CancelledRenewalDoesNotHoldTheTerm is the other half of the
// renewal rule: a landlord's recorded decline is not a negotiation in flight,
// so the term ends on its date.
func TestTenancyEnd_CancelledRenewalDoesNotHoldTheTerm(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})
	f.vtx(t, "rn", "renewal")
	f.setRootData(t, "rn", map[string]any{"status": "cancelled", "cycleEnd": teLeaseEnd})
	f.edge(t, "renews", "rn", "app")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, true, v["missing_tenancyEnded"], "a cancelled renewal holds nothing")
	require.Equal(t, true, v["violating"])
}

// TestTenancyEnd_OpenRenewalForAnotherCycleDoesNotHold pins the cycleEnd
// conjunct: an open renewal whose cycleEnd is some OTHER term's end is not a
// negotiation on this one.
func TestTenancyEnd_OpenRenewalForAnotherCycleDoesNotHold(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})
	f.vtx(t, "rn", "renewal")
	f.setRootData(t, "rn", map[string]any{"status": "open", "cycleEnd": "2026-01-01T00:00:00Z"})
	f.edge(t, "renews", "rn", "app")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, true, v["missing_tenancyEnded"], "a renewal on a different cycle does not hold this term")
}

// TestTenancyEnd_EndedTenancyRelistsItsLeasedUnit: once endedAt is recorded
// and the unit still reads leased with nobody else holding it, missing_relist
// opens and missing_tenancyEnded stays closed (the end is already recorded, so
// a still-present lapse marker re-opens nothing).
func TestTenancyEnd_EndedTenancyRelistsItsLeasedUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})
	endTenancy(t, f, "app")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_tenancyEnded"], "the end is recorded; the marker re-opens nothing")
	require.Equal(t, true, v["missing_relist"], "ended + unit still leased + no other tenant → relist")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "an ended term has nothing left to wait for")
	require.Equal(t, teLeaseEnd, v["endedAt"])
	require.Equal(t, "leased", v["unitStatus"])
}

// TestTenancyEnd_ReLeasedUnitIsNeverFlippedBack is the load-bearing vector:
// another approved application on the same unit holds a live tenancy, so this
// ended row must NOT relist the unit out from under the new tenant — however
// often it is re-delivered.
func TestTenancyEnd_ReLeasedUnitIsNeverFlippedBack(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	endTenancy(t, f, "app")
	f.vtx(t, "newapp", "leaseapp")
	f.aspect(t, "newapp", "decision", "decision", map[string]any{"value": "approved"})
	f.aspect(t, "newapp", "signature", "signature", map[string]any{"signedAt": "2027-01-05T00:00:00Z"})
	f.aspect(t, "newapp", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2027-02-01T00:00:00Z", "leaseEnd": "2028-02-01T00:00:00Z", "renewalOpensAt": "2027-12-03T00:00:00Z"})
	f.edge(t, "appliesToUnit", "newapp", "app_unit")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_relist"], "the unit is somebody else's now — never flipped back")
	require.Equal(t, false, v["violating"])

	// The new tenant's own row is a live tenancy: armed on its own end, nothing open.
	nv := f.projectTenancyEnd(t, "newapp")
	require.Equal(t, "2028-02-01T00:00:00Z", nv["freshUntil"])
	require.Equal(t, false, nv["missing_tenancyEnded"])
	require.Equal(t, false, nv["missing_relist"])
}

// TestTenancyEnd_OtherEndedOrUndecidedApplicationsDoNotHoldTheRelist pins what
// does NOT count as another live tenancy — a rival that was never approved and
// an approved rival whose own term has ended — and what DOES: an approved
// rival carrying no .tenancy at all. That last one is the pin that keeps the
// two targets from fighting: its own missing_listingLeased claims an
// 'available' unit (it conjoins only tenancyEndedAt = null), so this row must
// read it as the unit's holder rather than relist under it.
func TestTenancyEnd_OtherEndedOrUndecidedApplicationsDoNotHoldTheRelist(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	endTenancy(t, f, "app")
	f.vtx(t, "rival", "leaseapp")
	f.edge(t, "appliesToUnit", "rival", "app_unit")
	f.vtx(t, "prior", "leaseapp")
	f.aspect(t, "prior", "decision", "decision", map[string]any{"value": "approved"})
	f.aspect(t, "prior", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2025-01-01T00:00:00Z", "leaseEnd": "2026-01-01T00:00:00Z", "endedAt": "2026-01-01T00:00:00Z"})
	f.edge(t, "appliesToUnit", "prior", "app_unit")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, true, v["missing_relist"], "an undecided rival and an ended prior term hold nothing")

	f.vtx(t, "legacy", "leaseapp")
	f.aspect(t, "legacy", "decision", "decision", map[string]any{"value": "approved"})
	f.edge(t, "appliesToUnit", "legacy", "app_unit")
	v = f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_relist"], "an approved application with no .tenancy would lease the unit the moment it read available — it holds the relist")
	require.Equal(t, false, v["violating"])
}

// TestTenancyEnd_AlreadyAvailableUnitOpensNothing: an ended term whose unit is
// already relisted is terminal — no work remains.
func TestTenancyEnd_AlreadyAvailableUnitOpensNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "available")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})
	endTenancy(t, f, "app")

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_tenancyEnded"])
	require.Equal(t, false, v["missing_relist"], "already available → nothing to relist")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestTenancyEnd_TombstonedUnitNeverRelists: the appliesToUnit walk misses (a
// unit tombstoned out from under the lease — the ReassignLeaseUnit repair case),
// so unitKey projects null and the relist gap — whose params bind row.unitKey —
// stays closed rather than dispatching a refusal.
func TestTenancyEnd_TombstonedUnitNeverRelists(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	endTenancy(t, f, "app")
	f.tombstoneEdge(t, "appliesToUnit", "app", "app_unit")

	v := f.projectTenancyEnd(t, "app")
	require.Nil(t, v["unitKey"], "the walk misses → null unitKey")
	require.Equal(t, false, v["missing_relist"], "a null unitKey must never open the relist gap")
	require.Equal(t, false, v["violating"])
}

// TestTenancyEnd_ExtendedTermReArmsPastTheRecordedLapse is the RE-ARM vector
// (SignRenewal's shape): a leaseEnd extended past the instant this target
// already fired at is a NEW horizon — nothing clears the marker, so a presence
// test would end the renewed term at once. The comparison re-arms it with no
// clearing write, and opens nothing.
func TestTenancyEnd_ExtendedTermReArmsPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})
	const newEnd = "2028-01-01T00:00:00Z"
	f.aspect(t, "app", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-01-01T00:00:00Z", "leaseEnd": newEnd, "renewalOpensAt": "2027-11-02T00:00:00Z",
		"termStart": teLeaseEnd, "rentAmount": 2500})

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_tenancyEnded"], "a lapse the current end has outrun is not a lapse of THIS term")
	require.Equal(t, newEnd, v["freshUntil"], "and the @at re-arms on the new end with no clearing write")
	require.Equal(t, false, v["violating"])
}

// TestTenancyEnd_SiblingTargetLapseDoesNotEndTheTerm is the isolation vector:
// a leaseapp is also the anchor of leaseExpiry and leaseApplicationComplete, and
// every target that fires on it shares ONE marker aspect, so reading the
// aspect's presence — or its entity-wide expiredAt maximum — would end a term
// off the renewal-cycle fire.
func TestTenancyEnd_SiblingTargetLapseDoesNotEndTheTerm(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{LeaseExpiryTarget: "2099-01-01T00:00:00Z"})

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_tenancyEnded"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, teLeaseEnd, v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestTenancyEnd_UnsignedOrUndecidedNeverEnds keeps the non-clock conjuncts
// honest: a tenancy on an application that is not both approved and signed is
// not a lease, so a recorded lapse at its end records nothing.
func TestTenancyEnd_UnsignedOrUndecidedNeverEnds(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedLeasedTenancy(t, f, "app", "leased")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: teLeaseEnd})
	f.aspect(t, "app", "decision", "decision", map[string]any{"value": "declined"})

	v := f.projectTenancyEnd(t, "app")
	require.Equal(t, false, v["missing_tenancyEnded"], "not approved → no lease → never ends")
}

// TestTenancyEnd_NoTenancyProjectsNothingOpen: a leaseapp with no .tenancy has
// no end at all — no freshUntil to arm, nothing to end, nothing to relist.
func TestTenancyEnd_NoTenancyProjectsNothingOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"status": "leased"})
	f.aspect(t, "app", "decision", "decision", map[string]any{"value": "approved"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-01-01T00:00:00Z"})
	f.edge(t, "appliesToUnit", "app", "unit1")
	recordLeaseappLapse(t, f, "app", map[string]string{TenancyEndTarget: "2100-01-01T00:00:00Z"})

	v := f.projectTenancyEnd(t, "app")
	require.Nil(t, v["freshUntil"])
	require.Equal(t, false, v["missing_tenancyEnded"])
	require.Equal(t, false, v["missing_relist"])
	require.Equal(t, false, v["violating"])
}

// TestTenancyEnd_ReferencesNoClockParameter is the structural half, asserted on
// the compiled cypher rather than on any one row: a lens that returns $now
// projects a clock reading the sweep's deep verify cannot compare.
func TestTenancyEnd_ReferencesNoClockParameter(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(tenancyEndSpec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull, "tenancyEnd must compile to the full engine")
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		require.Truef(t, exhaustive, "the query shape must be provably free of $%s", param)
		require.Falsef(t, referenced,
			"tenancyEnd must reference no $%s — a term's end is a recorded fact, not a clock reading", param)
	}
}

// TestTenancyEnd_ReadsItsOwnTargetsMarkerEntry binds the two halves that can
// silently drift apart: the §10.8 TargetID Weaver fires a timer under (and
// MarkExpired — orchestration-base's freshnessExpiry writer — records the lapse
// under, keyed by the firing target's id), and the byTarget key the lens
// compares against its end. A rename of one without the other leaves the lens
// reading an entry nothing ever writes — a term that can never end, with every
// row still projecting and every seeded-marker test still passing. The
// freshUntil/lapse fragment is pinned on the SHIPPED spec string so the marker's
// reader (this lens) and its writer (the target's timer) stay one fragment.
func TestTenancyEnd_ReadsItsOwnTargetsMarkerEntry(t *testing.T) {
	var target string
	var lensRef string
	for _, tgt := range WeaverTargets() {
		if tgt.TargetID == TenancyEndTarget {
			target = tgt.TargetID
			lensRef = tgt.LensRef
		}
	}
	require.NotEmpty(t, target, "the tenancyEnd target must be declared")
	require.Equal(t, TenancyEndTarget, lensRef, "TargetID == LensRef == the output-key prefix")
	var outputPrefix string
	for _, l := range Lenses() {
		if l.CanonicalName == TenancyEndTarget {
			outputPrefix = l.Output.OutputKeyPattern
		}
	}
	require.Equal(t, TenancyEndTarget+".{actorSuffix}", outputPrefix, "the row prefix IS the target id (the §10.2↔§10.8 binding)")
	require.Contains(t, tenancyEndSpec, "app.freshnessExpiry.data.byTarget."+target+" AS lapsedAt",
		"tenancyEnd must read the marker under its own target id — the timer that fires writes that entry and no other")
	require.Contains(t, tenancyEndSpec, "CASE WHEN (endedAt <> null) OR (lapsedAt >= leaseEnd) THEN null ELSE leaseEnd END AS freshUntil",
		"freshUntil is leaseEnd until the recorded lapse reaches it or the end is recorded — the fragment the timer and the reader share")
	require.True(t, strings.Contains(tenancyEndSpec, "(lapsedAt >= leaseEnd) AND (openRenewalCount = 0)"),
		"the end gap requires the recorded lapse AND no open renewal")
}
