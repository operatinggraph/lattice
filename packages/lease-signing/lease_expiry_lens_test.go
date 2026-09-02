package leasesigning

// Rule-engine proof of the leaseExpiry convergence cypher — the leaseapp-anchored
// renewal-cycle horizon.
//
// What decides "the cycle horizon arrived" is a recorded FACT, not a clock: the
// instant the @at this lens armed actually fired, recorded on the leaseapp under
// this target's own byTarget key. The read is carried through the same
// aggregating WITH as renewalOpensAt itself, so the RETURN compares two carried
// scalars and the cypher references no clock parameter at all. No $now is
// supplied to any vector below — passing one would let a clock-reading regression
// pass unnoticed.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// projectLeaseExpiry runs leaseExpirySpec anchored on the named leaseapp.
func (f *lensFixture) projectLeaseExpiry(t *testing.T, appName string) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(leaseExpirySpec)
	require.NoError(t, err, "leaseExpiry cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey": "vtx.leaseapp." + f.ids[appName],
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "exactly one row per leaseapp anchor")
	return out[0].Values
}

// seedSignedTenancy seeds a decided-approved, signed leaseapp with a .tenancy
// carrying the given renewalOpensAt, on an owned unit — the shape leaseExpiry
// requires before it will open a cycle at all.
func seedSignedTenancy(t *testing.T, f *lensFixture, appName, renewalOpensAt string) {
	t.Helper()
	f.vtx(t, appName, "leaseapp")
	f.vtx(t, appName+"_tenant", "identity")
	f.vtx(t, appName+"_unit", "unit")
	f.vtx(t, appName+"_landlord", "identity")
	f.aspect(t, appName, "tenancy", "tenancy", map[string]any{
		"leaseEnd": "2027-01-01T00:00:00Z", "renewalOpensAt": renewalOpensAt})
	f.aspect(t, appName, "decision", "decision", map[string]any{"value": "approved"})
	f.aspect(t, appName, "signature", "signature", map[string]any{"signedAt": "2026-01-01T00:00:00Z"})
	f.edge(t, "applicationFor", appName, appName+"_tenant")
	f.edge(t, "appliesToUnit", appName, appName+"_unit")
	f.edge(t, "manages", appName+"_landlord", appName+"_unit")
}

// recordLeaseappLapse writes the freshnessExpiry marker MarkExpired commits onto
// a leaseapp when a target's @at fires: the instant the timer fired for, recorded
// under that target's own key in byTarget, with expiredAt carrying the
// entity-wide maximum.
func recordLeaseappLapse(t *testing.T, f *lensFixture, appName string, byTarget map[string]string) {
	t.Helper()
	entries := map[string]any{}
	maxAt := ""
	for target, at := range byTarget {
		entries[target] = at
		if at > maxAt {
			maxAt = at
		}
	}
	f.aspect(t, appName, "freshnessExpiry", "freshnessExpiry", map[string]any{
		"expiredAt": maxAt,
		"byTarget":  entries,
	})
}

// TestLeaseExpiry_ArmsWhileNoLapseRecorded is the arming vector: a signed,
// approved tenancy no timer has fired on projects its renewalOpensAt as
// freshUntil — the scalar Weaver schedules the @at from — and opens no cycle yet.
// The horizon is deliberately in the PAST relative to any wall clock the suite
// runs at, so a clock-reading form would open the cycle here, which is what makes
// the vector discriminating.
func TestLeaseExpiry_ArmsWhileNoLapseRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2020-06-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, renewalOpensAt, v["freshUntil"], "no recorded lapse → the horizon is still armed, whatever the wall clock says")
	_, isString := v["freshUntil"].(string)
	require.True(t, isString, "freshUntil must be a scalar string so scheduleFreshness can parse it as RFC3339")
	require.Equal(t, false, v["missing_renewalCycle"], "nothing has fired yet, so the cycle does not open until the marker lands")
	require.Equal(t, false, v["violating"])
}

// TestLeaseExpiry_PastHorizonProjectedVerbatim is the
// PAST-DEADLINE-AT-FIRST-PROJECTION vector, and the one place a "null when the
// deadline is already past" guard would be tempting — the shipped cypher carried
// exactly that guard. A tenancy whose renewal horizon arrived while no target was
// watching carries no marker, so the only path to recording the lapse is this row
// projecting the past instant, Weaver publishing the overdue @at, and NATS
// releasing it immediately. Nulling it arms nothing and the cycle never opens.
func TestLeaseExpiry_PastHorizonProjectedVerbatim(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2020-06-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, renewalOpensAt, v["freshUntil"],
		"an already-past renewalOpensAt with no recorded lapse projects VERBATIM — the overdue @at is the only path to recording it")

	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": renewalOpensAt})
	v = f.projectLeaseExpiry(t, "app")
	require.Equal(t, true, v["missing_renewalCycle"], "the recorded lapse opens the cycle")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"], "and the horizon disarms — one @at fire per cycle, not one per delivery")
}

// TestLeaseExpiry_OpensOnceTheLapseIsRecorded is the gap vector at a horizon in
// the far FUTURE, so only the recorded lapse can open the cycle.
func TestLeaseExpiry_OpensOnceTheLapseIsRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2099-01-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": renewalOpensAt})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, true, v["missing_renewalCycle"], "a recorded lapse at renewalOpensAt opens the cycle")
	require.Equal(t, true, v["violating"])
	require.Nil(t, v["freshUntil"])
}

// TestLeaseExpiry_ReTenantedPastTheRecordedLapse is the RE-ARM vector, and the
// whole argument for comparing the marker against the deadline rather than
// testing its presence. A leaseapp re-tenanted for a new term carries a later
// renewalOpensAt that OUTRUNS the recorded instant; nothing clears the marker —
// MarkExpired never tombstones it — so a presence test would leave the cycle
// permanently open and re-dispatch OpenRenewal forever.
func TestLeaseExpiry_ReTenantedPastTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const newHorizon = "2099-06-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", newHorizon)
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": "2026-06-01T00:00:00Z"})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, false, v["missing_renewalCycle"], "a lapse the current horizon has outrun is not a lapse of THIS horizon")
	require.Equal(t, newHorizon, v["freshUntil"], "and the @at re-arms with no clearing write")
}

// TestLeaseExpiry_HorizonMovedEarlierThanTheRecordedLapse is the
// DEADLINE-MOVED-EARLIER row of the state table, asserted deliberately so a later
// reader does not "fix" it: a tenancy re-stamped with an earlier horizon, below
// an instant this target already fired at, opens its cycle at once. Correct — a
// timer did fire at or after the new horizon.
func TestLeaseExpiry_HorizonMovedEarlierThanTheRecordedLapse(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedSignedTenancy(t, f, "app", "2026-01-01T00:00:00Z")
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": "2026-06-01T00:00:00Z"})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, true, v["missing_renewalCycle"], "the recorded fire is after the new horizon, so it IS a lapse of it")
	require.Nil(t, v["freshUntil"])
}

// TestLeaseExpiry_BoundaryMarkerEqualsHorizon pins which side of the `>=` the
// equal instant falls on: the timer fires AT renewalOpensAt and records that
// instant, so equality is the ordinary lapse — the common case, since the armed
// deadline and the fire instant are the same value.
func TestLeaseExpiry_BoundaryMarkerEqualsHorizon(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2099-01-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": renewalOpensAt})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, true, v["missing_renewalCycle"], "marker == renewalOpensAt is a lapse (>= boundary)")
}

// TestLeaseExpiry_SiblingTargetLapseDoesNotOpenTheCycle is the isolation vector.
// A leaseapp is also the anchor of leaseApplicationComplete and applicantOnboarding,
// and every target that fires on it shares ONE marker aspect, so reading the
// aspect's presence — or its entity-wide expiredAt maximum — would open a renewal
// cycle off an unrelated fire.
func TestLeaseExpiry_SiblingTargetLapseDoesNotOpenTheCycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2099-01-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseApplicationComplete": "2100-01-01T00:00:00Z"})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, false, v["missing_renewalCycle"], "another target's recorded fire is not this target's lapse")
	require.Equal(t, renewalOpensAt, v["freshUntil"], "and it does not disarm this target's timer either")
}

// TestLeaseExpiry_MarkerWithNoByTargetMapReadsUnlapsed pins the shape a marker
// written before byTarget existed carries. `expiredAt` alone says which entity
// last lapsed, never which target, so a lens that read it would answer for a
// sibling's fire. The four-hop read resolves to nil and compareAny answers false:
// unlapsed, and the horizon stays armed.
func TestLeaseExpiry_MarkerWithNoByTargetMapReadsUnlapsed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2099-01-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)
	f.aspect(t, "app", "freshnessExpiry", "freshnessExpiry", map[string]any{"expiredAt": "2100-01-01T00:00:00Z"})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, false, v["missing_renewalCycle"], "a marker with no byTarget map names no target and lapses nothing here")
	require.Equal(t, renewalOpensAt, v["freshUntil"])
}

// TestLeaseExpiry_NoTenancyNeverOpens keeps the non-clock conjuncts honest: a
// leaseapp with no .tenancy aspect has no horizon at all, so it never opens a
// cycle however the marker reads. compareAny answers false when either operand is
// nil, which is the same answer the clock form gave.
func TestLeaseExpiry_NoTenancyNeverOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "tenant", "identity")
	f.vtx(t, "unit1", "unit")
	f.vtx(t, "larry", "identity")
	f.aspect(t, "app", "decision", "decision", map[string]any{"value": "approved"})
	f.aspect(t, "app", "signature", "signature", map[string]any{"signedAt": "2026-01-01T00:00:00Z"})
	f.edge(t, "applicationFor", "app", "tenant")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.edge(t, "manages", "larry", "unit1")
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": "2100-01-01T00:00:00Z"})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, false, v["missing_renewalCycle"], "no tenancy → no horizon → never opens, marker or not")
	require.Nil(t, v["freshUntil"])
}

// TestLeaseExpiry_UnsignedNeverOpens is the other non-clock conjunct: an approved
// but unsigned application has no lease to renew, so a recorded lapse at its
// horizon must not open a cycle.
func TestLeaseExpiry_UnsignedNeverOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2099-01-01T00:00:00Z"
	f.vtx(t, "app", "leaseapp")
	f.vtx(t, "tenant", "identity")
	f.vtx(t, "unit1", "unit")
	f.vtx(t, "larry", "identity")
	f.aspect(t, "app", "tenancy", "tenancy", map[string]any{
		"leaseEnd": "2027-01-01T00:00:00Z", "renewalOpensAt": renewalOpensAt})
	f.aspect(t, "app", "decision", "decision", map[string]any{"value": "approved"})
	f.edge(t, "applicationFor", "app", "tenant")
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.edge(t, "manages", "larry", "unit1")
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": renewalOpensAt})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, false, v["missing_renewalCycle"], "no signature → no lease → never opens, even with the lapse recorded")
}

// TestLeaseExpiry_ExistingCycleKeepsItClosed is the last non-clock conjunct: a
// live renewal already covering THIS tenancy's leaseEnd keeps the cycle closed
// even with the horizon lapsed — the §4.4 no-reopen rule, and the reason the
// recorded lapse alone is not sufficient to violate.
func TestLeaseExpiry_ExistingCycleKeepsItClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2099-01-01T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)
	recordLeaseappLapse(t, f, "app", map[string]string{"leaseExpiry": renewalOpensAt})
	f.vtx(t, "rn", "renewal")
	f.setRootData(t, "rn", map[string]any{"status": "open", "cycleEnd": "2027-01-01T00:00:00Z"})
	f.edge(t, "renews", "rn", "app")

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, false, v["missing_renewalCycle"], "a live renewal for this leaseEnd already covers the cycle")
	require.Equal(t, false, v["violating"])
}

// TestLeaseExpiry_ReferencesNoClockParameter is the structural half of the
// conversion, asserted on the compiled cypher rather than on any one row: a lens
// that returns $now projects a clock reading the sweep's deep verify cannot
// compare, which is the divergence this conversion removes.
func TestLeaseExpiry_ReferencesNoClockParameter(t *testing.T) {
	eng := full.New()
	cr, err := eng.Parse(leaseExpirySpec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull, "leaseExpiry must compile to the full engine")
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		require.Truef(t, exhaustive, "the query shape must be provably free of $%s", param)
		require.Falsef(t, referenced,
			"leaseExpiry must reference no $%s — expiry is a recorded fact, not a clock reading", param)
	}
}

// TestLeaseExpiry_ReadsItsOwnTargetsMarkerEntry binds the two halves that can
// silently drift apart: the §10.8 TargetID Weaver fires a timer under, and the
// byTarget key the lens compares against its horizon. A rename of one without the
// other leaves the lens reading an entry nothing ever writes — a cycle that can
// never open, with every row still projecting and every seeded-marker test still
// passing.
func TestLeaseExpiry_ReadsItsOwnTargetsMarkerEntry(t *testing.T) {
	var targetID string
	for _, tgt := range WeaverTargets() {
		if tgt.LensRef == "leaseExpiry" {
			targetID = tgt.TargetID
		}
	}
	require.NotEmpty(t, targetID, "the leaseExpiry target must be declared")
	require.Contains(t, leaseExpirySpec, "byTarget."+targetID,
		"leaseExpiry must read the marker under its own target id — the timer that fires writes that entry and no other")
}
