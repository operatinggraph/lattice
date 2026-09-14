package leasesigning

// Rule-engine proof that the two SIBLING lenses on the leaseapp anchor read an
// ended tenancy as terminal (design loftspace-lease-term-and-tenancy-end-design.md
// §2.2, the consumer census): leaseApplicationComplete closes its applicant
// gaps and the listing flip, leaseExpiry opens no renewal cycle. Without these
// conjuncts the relist tenancyEnd drives would be undone by a re-lease, and a
// former tenant's stale bgcheck would re-dispatch a vendor check.

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRenewalComplete_OpenRenewalOnEndedTenancyProjectsNothingOpen: an
// operator can EndTenancy under an OPEN renewal (the op does not walk
// renewals), so the cycle's row must read the ended term as terminal —
// open false, missing_renewalComplete false, violating false — rather than
// keep a signRenewal leg the op refuses TenancyEnded and re-dispatch bgchecks
// on a former tenant.
func TestRenewalComplete_OpenRenewalOnEndedTenancyProjectsNothingOpen(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.seedOpenRenewal(t, "rn", "app", "tenant", "unit1", "larry")

	rows := f.projectRenewalComplete(t, "rn")
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0].Values["open"], "the control: a live tenancy's open cycle is open")
	require.Equal(t, true, rows[0].Values["missing_renewalComplete"])
	require.Equal(t, true, rows[0].Values["violating"])

	f.aspect(t, "app", "tenancy", "tenancy", map[string]any{
		"leaseEnd": "2027-01-01T00:00:00Z", "renewalOpensAt": "2026-11-02T00:00:00Z", "endedAt": "2027-01-01T00:00:00Z"})
	rows = f.projectRenewalComplete(t, "rn")
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["open"], "an ended term's open cycle is not open")
	require.Equal(t, false, rows[0].Values["missing_renewalComplete"])
	require.Equal(t, false, rows[0].Values["violating"])
}

// endedApprovedLeaseFixture is approvedAppFixture (qualified, signed, the
// executed lease attached) approved by the landlord on a unit that tenancyEnd
// has already RELISTED — .tenancy carries endedAt and the listing reads
// 'available' again. This is exactly the shape that, without the ended-tenancy
// conjunct, re-opens missing_listingLeased and re-leases the unit to the
// tenant who has just left.
func endedApprovedLeaseFixture(t *testing.T) *lensFixture {
	t.Helper()
	f := approvedAppFixture(t)
	f.landlordDecision(t, "app", "approved")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "available"})
	f.edge(t, "appliesToUnit", "app", "unit1")
	f.vtx(t, "larry", "identity")
	f.edge(t, "manages", "larry", "unit1")
	f.aspect(t, "app", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z",
		"renewalOpensAt": "2027-05-02T00:00:00Z", "endedAt": "2027-07-01T00:00:00Z"})
	return f
}

// TestLeaseApplicationComplete_EndedTenancyIsTerminal: an approved, signed,
// fully-qualified application whose term has ended, on a unit relisted as
// available, is terminal-not-violating — missing_listingLeased false (the
// relisted unit is NOT re-leased to the ended tenant), every applicant gap
// false, missing_decision false, violating false — and projects tenancyEndedAt
// so a reader can tell this terminal shape from a decline.
func TestLeaseApplicationComplete_EndedTenancyIsTerminal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := endedApprovedLeaseFixture(t)

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "2027-07-01T00:00:00Z", v["tenancyEndedAt"], "the ended fact projects as a column")
	require.Equal(t, "available", v["unitStatus"], "the fixture poses the relisted unit")
	require.Equal(t, true, v["landlordApproved"])
	require.Equal(t, true, v["applicantApproved"], "the applicant qualified — readiness is a fact about the person, not the term")
	require.Equal(t, false, v["missing_listingLeased"], "an ended tenancy never re-leases its relisted unit")
	require.Equal(t, false, v["missing_onboarding"])
	require.Equal(t, false, v["missing_bgcheck"])
	require.Equal(t, false, v["missing_payment"])
	require.Equal(t, false, v["missing_signature"])
	require.Equal(t, false, v["missing_decision"], "an ended tenancy is approved by construction")
	require.Equal(t, false, v["missing_manager"], "the unit is managed")
	require.Equal(t, false, v["violating"], "terminal — no work remains, Weaver stops reconciling")

	// The control: the same row with the term still live is the ordinary
	// approved-on-an-available-unit shape, which DOES open the listing flip.
	f.aspect(t, "app", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z", "renewalOpensAt": "2027-05-02T00:00:00Z"})
	rows = f.project(t, "app")
	require.Len(t, rows, 1)
	require.Nil(t, rows[0].Values["tenancyEndedAt"])
	require.Equal(t, true, rows[0].Values["missing_listingLeased"], "a live tenancy on an available unit still leases it")
	require.Equal(t, true, rows[0].Values["violating"])
}

// TestLeaseApplicationComplete_StaleBgcheckOnEndedTenancyOpensNothing: the
// winning applicant's approved-escape-hatch keeps missing_bgcheck reopenable
// after their unit leases — correct while they live there, wrong once they
// have left. A lapsed check on an ended tenancy must not re-dispatch a vendor
// check on a former tenant.
func TestLeaseApplicationComplete_StaleBgcheckOnEndedTenancyOpensNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := endedApprovedLeaseFixture(t)
	// The unit still reads leased here (the relist has not landed yet) — the
	// approved escape hatch's own shape — and the bgcheck has lapsed.
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})
	recordBgcheckLapse(t, f, "bg1", farFutureValidUntil)

	rows := f.project(t, "app")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, false, v["applicantApproved"], "the check has lapsed — readiness is genuinely gone")
	require.Equal(t, false, v["missing_bgcheck"], "but a former tenant is never re-checked")
	require.Equal(t, false, v["violating"])

	// The control: the same lapsed check on a LIVE tenancy re-opens the gap
	// through the approved escape hatch, as the eager-reopen cycle needs.
	f.aspect(t, "app", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z", "renewalOpensAt": "2027-05-02T00:00:00Z"})
	rows = f.project(t, "app")
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0].Values["missing_bgcheck"], "a live tenant's lapsed check re-opens the gap")
	require.Equal(t, true, rows[0].Values["violating"])
}

// TestLeaseExpiry_EndedTenancyNeverOpensACycle: a lease whose term ended with
// no renewal ever opened — the recorded lapse at renewalOpensAt still stands,
// no renewal covers the cycle — must not have one opened for it now, and the
// horizon must not re-arm on a term that is over.
func TestLeaseExpiry_EndedTenancyNeverOpensACycle(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const renewalOpensAt = "2026-11-02T00:00:00Z"
	seedSignedTenancy(t, f, "app", renewalOpensAt)
	recordLeaseappLapse(t, f, "app", map[string]string{LeaseExpiryTarget: renewalOpensAt})

	v := f.projectLeaseExpiry(t, "app")
	require.Equal(t, true, v["missing_renewalCycle"], "the control: a live tenancy past its lapsed horizon with no renewal opens a cycle")

	f.aspect(t, "app", "tenancy", "tenancy", map[string]any{
		"leaseEnd": "2027-01-01T00:00:00Z", "renewalOpensAt": renewalOpensAt, "endedAt": "2027-01-01T00:00:00Z"})
	v = f.projectLeaseExpiry(t, "app")
	require.Equal(t, false, v["missing_renewalCycle"], "an ended tenancy opens no cycle, renewal or not")
	require.Equal(t, false, v["violating"])
	require.Nil(t, v["freshUntil"], "and arms no horizon")

	// Ended with the horizon never lapsed: the timer is disarmed too.
	f.aspect(t, "app", "freshnessExpiry", "freshnessExpiry", map[string]any{"expiredAt": "", "byTarget": map[string]any{}})
	v = f.projectLeaseExpiry(t, "app")
	require.Nil(t, v["freshUntil"], "an ended term's unlapsed horizon is not armed either")
}

// TestApplicantOnboarding_EndedTenancyStopsAsking: an approved, signed
// applicant who never recorded an ssn is asked for it while the term is live
// (leaseApplicationComplete's approved escape hatch keeps the application
// counted after its unit leases) and stops being asked once the term ends —
// the same terminal reading the per-application target gives the ended row.
func TestApplicantOnboarding_EndedTenancyStopsAsking(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	const now = "2026-06-18T00:00:00Z"
	f.vtx(t, "alice", "identity")
	f.vtx(t, "app1", "leaseapp")
	f.vtx(t, "unit1", "unit")
	f.aspect(t, "unit1", "listing", "listing", map[string]any{"rentAmount": 2400, "status": "leased"})
	f.aspect(t, "app1", "decision", "decision", map[string]any{"value": "approved"})
	f.aspect(t, "app1", "signature", "signature", map[string]any{"signedAt": "2026-06-10T00:00:00Z"})
	f.aspect(t, "app1", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z", "renewalOpensAt": "2027-05-02T00:00:00Z"})
	f.edge(t, "applicationFor", "app1", "alice")
	f.edge(t, "appliesToUnit", "app1", "unit1")

	rows := f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1)
	require.Equal(t, true, rows[0].Values["missing_onboarding"], "the control: a live approved tenancy with no ssn still asks")

	f.aspect(t, "app1", "tenancy", "tenancy", map[string]any{
		"leaseStart": "2026-07-01T00:00:00Z", "leaseEnd": "2027-07-01T00:00:00Z", "renewalOpensAt": "2027-05-02T00:00:00Z",
		"endedAt": "2027-07-01T00:00:00Z"})
	rows = f.projectApplicantOnboarding(t, "alice", now)
	require.Len(t, rows, 1)
	require.Equal(t, false, rows[0].Values["missing_onboarding"], "an ended term's applicant is not asked for PII")
	require.Equal(t, false, rows[0].Values["violating"])
}
