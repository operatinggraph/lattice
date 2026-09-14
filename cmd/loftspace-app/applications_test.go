package main

import "testing"

func TestComputeApplications_FiltersPrefixAndApplicant(t *testing.T) {
	entries := map[string]string{
		// alice — all gaps open, anchored to a unit. maxretries_<g> is the lens's
		// CONSTANT integer cap (3) — typing it bool would drop this row on decode.
		"leaseApplicationComplete.app1": `{"entityKey":"vtx.leaseapp.app1","applicant":"vtx.identity.alice","violating":true,"missing_onboarding":true,"missing_bgcheck":true,"missing_payment":true,"missing_signature":true,"inflight_bgcheck":false,"inflight_payment":false,"maxretries_bgcheck":3,"maxretries_payment":3,"unitKey":"vtx.unit.u1","unitAddress":"1 Market St","unitRent":2400}`,
		// alice — a second, fully converged application (approved + unit leased)
		"leaseApplicationComplete.app2": `{"entityKey":"vtx.leaseapp.app2","applicant":"vtx.identity.alice","violating":false,"applicantApproved":true,"missing_onboarding":false,"missing_bgcheck":false,"missing_payment":false,"missing_signature":false,"unitKey":"vtx.unit.u2","unitRent":1800,"unitStatus":"leased"}`,
		// bob — a different applicant
		"leaseApplicationComplete.app3": `{"entityKey":"vtx.leaseapp.app3","applicant":"vtx.identity.bob","violating":true,"missing_bgcheck":true,"inflight_bgcheck":true}`,
		// a non-convergence read-model row sharing the bucket — must be ignored
		"someOtherLens.xyz": `{"entityKey":"vtx.leaseapp.zzz","applicant":"vtx.identity.alice"}`,
		// a tombstoned / empty convergence entry — skipped (no entityKey)
		"leaseApplicationComplete.app4": `{}`,
	}
	get := fakeKV(entries)

	alice := computeApplications(keysOf(entries), get, "vtx.identity.alice")
	if len(alice) != 2 {
		t.Fatalf("alice: want 2 applications, got %d (%+v)", len(alice), alice)
	}
	// stable sort by entityKey → app1 then app2
	if alice[0].EntityKey != "vtx.leaseapp.app1" || alice[1].EntityKey != "vtx.leaseapp.app2" {
		t.Errorf("sort by entityKey: got %q, %q", alice[0].EntityKey, alice[1].EntityKey)
	}
	if !alice[0].Violating || !alice[0].MissingOnboarding {
		t.Errorf("app1 gaps: want violating+missing_onboarding, got %+v", alice[0])
	}
	if alice[0].UnitRent == nil || *alice[0].UnitRent != 2400 || alice[0].UnitAddress != "1 Market St" {
		t.Errorf("app1 unit columns: want rent 2400 / addr set, got %+v", alice[0])
	}
	// the integer retry-budget cap must decode (the row-drop regression guard)
	if alice[0].MaxretriesBgcheck != 3 {
		t.Errorf("app1 maxretries_bgcheck: want 3 (integer cap), got %d", alice[0].MaxretriesBgcheck)
	}
	if alice[1].Violating {
		t.Errorf("app2 should be converged (violating=false), got %+v", alice[1])
	}
	if !alice[1].ApplicantApproved || alice[1].UnitStatus != "leased" {
		t.Errorf("app2: want applicantApproved + unitStatus=leased, got %+v", alice[1])
	}

	bob := computeApplications(keysOf(entries), get, "vtx.identity.bob")
	if len(bob) != 1 || bob[0].EntityKey != "vtx.leaseapp.app3" {
		t.Fatalf("bob: want only app3, got %+v", bob)
	}
	if !bob[0].InflightBgcheck {
		t.Errorf("app3: want inflight_bgcheck true, got %+v", bob[0])
	}

	// no applicant filter → every convergence row (the non-lens + empty rows stay out)
	all := computeApplications(keysOf(entries), get, "")
	if len(all) != 3 {
		t.Fatalf("unfiltered: want 3 convergence rows, got %d (%+v)", len(all), all)
	}

	// an applicant with no applications → empty, not nil-panic
	if none := computeApplications(keysOf(entries), get, "vtx.identity.nobody"); len(none) != 0 {
		t.Errorf("unknown applicant: want 0 rows, got %d", len(none))
	}
}

// TestComputeApplications_LandlordApprovedDuringListingFlip pins the banner data
// the FE relies on during the brief window after the landlord approves but before
// the unit is marked leased: the row is STILL violating (the listing-leased gap is
// open) with landlordApproved=true and unitStatus not yet leased. The FE keys its
// "complete" banner off landlordApproved && unitStatus==='leased', so this row must
// surface landlordApproved=true and unitStatus available independently of violating.
func TestComputeApplications_LandlordApprovedDuringListingFlip(t *testing.T) {
	entries := map[string]string{
		"leaseApplicationComplete.app9": `{"entityKey":"vtx.leaseapp.app9","applicant":"vtx.identity.alice","violating":true,"applicantApproved":true,"landlordDecision":"approved","landlordApproved":true,"missing_decision":false,"missing_onboarding":false,"missing_bgcheck":false,"missing_payment":false,"missing_signature":false,"missing_listingLeased":true,"unitKey":"vtx.unit.u9","unitStatus":"available"}`,
	}
	got := computeApplications(keysOf(entries), fakeKV(entries), "vtx.identity.alice")
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	if !got[0].Violating {
		t.Errorf("the listing-flip window is still violating, got %+v", got[0])
	}
	if !got[0].LandlordApproved || got[0].LandlordDecision != "approved" {
		t.Errorf("landlordApproved/landlordDecision must round-trip true/approved, got %+v", got[0])
	}
	if got[0].UnitStatus != "available" {
		t.Errorf("unitStatus should be available (not yet leased), got %q", got[0].UnitStatus)
	}
}

// TestComputeApplications_LandlordDecisionColumnsRoundTrip is the FIX-1 regression
// guard: the four landlord-decision columns the FE banner + Withdraw guard read
// (landlordDecision / landlordApproved / landlordDeclined / missing_decision) MUST
// survive the handler's decode→re-serialize round-trip. Before they were added to
// applicationRow the handler silently dropped them, so the FE saw them undefined and
// every app read "In review." Three rows exercise the three decision states.
func TestComputeApplications_LandlordDecisionColumnsRoundTrip(t *testing.T) {
	entries := map[string]string{
		// qualified, awaiting the landlord decision (the Approve/Decline state).
		"leaseApplicationComplete.aw": `{"entityKey":"vtx.leaseapp.aw","applicant":"vtx.identity.alice","violating":true,"applicantApproved":true,"missing_decision":true,"unitKey":"vtx.unit.uaw","unitStatus":"available"}`,
		// landlord-approved and leased — the terminal done state.
		"leaseApplicationComplete.ok": `{"entityKey":"vtx.leaseapp.ok","applicant":"vtx.identity.alice","violating":false,"applicantApproved":true,"landlordDecision":"approved","landlordApproved":true,"missing_decision":false,"unitKey":"vtx.unit.uok","unitStatus":"leased"}`,
		// landlord-declined — terminal rejection.
		"leaseApplicationComplete.no": `{"entityKey":"vtx.leaseapp.no","applicant":"vtx.identity.alice","violating":false,"applicantApproved":true,"landlordDecision":"declined","landlordDeclined":true,"declined":true,"missing_decision":false,"unitKey":"vtx.unit.uno","unitStatus":"available"}`,
	}
	got := computeApplications(keysOf(entries), fakeKV(entries), "vtx.identity.alice")
	if len(got) != 3 {
		t.Fatalf("want 3 rows, got %d (%+v)", len(got), got)
	}
	byKey := map[string]applicationRow{}
	for _, r := range got {
		byKey[r.EntityKey] = r
	}

	aw := byKey["vtx.leaseapp.aw"]
	if !aw.MissingDecision || aw.LandlordApproved || aw.LandlordDeclined || aw.LandlordDecision != "" {
		t.Errorf("awaiting-decision row: want missing_decision true + no landlord decision, got %+v", aw)
	}
	ok := byKey["vtx.leaseapp.ok"]
	if !ok.LandlordApproved || ok.LandlordDecision != "approved" || ok.MissingDecision {
		t.Errorf("approved row: want landlordApproved + landlordDecision=approved + missing_decision false, got %+v", ok)
	}
	no := byKey["vtx.leaseapp.no"]
	if !no.LandlordDeclined || no.LandlordDecision != "declined" || !no.Declined {
		t.Errorf("declined row: want landlordDeclined + landlordDecision=declined + declined, got %+v", no)
	}
}

// TestComputeApplications_TenancyEndedAtRoundTrip pins the tenancyEndedAt
// decode: it must survive the handler's decode→re-serialize round-trip (the
// landlord by-unit Relist gate reads it off the SAME applicationRow, via
// groupByUnit — see TestGroupByUnit_CarriesTenancyEndedAt), and stay empty on
// a row that never recorded a tenancy rather than a misleading zero value.
func TestComputeApplications_TenancyEndedAtRoundTrip(t *testing.T) {
	entries := map[string]string{
		"leaseApplicationComplete.ended": `{"entityKey":"vtx.leaseapp.ended","applicant":"vtx.identity.alice","landlordApproved":true,"tenancyEndedAt":"2026-09-15T00:00:00Z"}`,
		"leaseApplicationComplete.live":  `{"entityKey":"vtx.leaseapp.live","applicant":"vtx.identity.alice","landlordApproved":true}`,
	}
	got := computeApplications(keysOf(entries), fakeKV(entries), "vtx.identity.alice")
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
	byKey := map[string]applicationRow{}
	for _, r := range got {
		byKey[r.EntityKey] = r
	}
	if got := byKey["vtx.leaseapp.ended"].TenancyEndedAt; got != "2026-09-15T00:00:00Z" {
		t.Errorf("ended row tenancyEndedAt = %q, want 2026-09-15T00:00:00Z", got)
	}
	if got := byKey["vtx.leaseapp.live"].TenancyEndedAt; got != "" {
		t.Errorf("live row (no tenancy end recorded) tenancyEndedAt = %q, want empty", got)
	}
}

func TestComputeApplications_SkipsUndecodable(t *testing.T) {
	entries := map[string]string{
		"leaseApplicationComplete.app1": `not json`,
		"leaseApplicationComplete.app2": `{"entityKey":"vtx.leaseapp.app2","applicant":"vtx.identity.alice","violating":true}`,
	}
	got := computeApplications(keysOf(entries), fakeKV(entries), "")
	if len(got) != 1 || got[0].EntityKey != "vtx.leaseapp.app2" {
		t.Fatalf("want only the decodable row, got %+v", got)
	}
}
