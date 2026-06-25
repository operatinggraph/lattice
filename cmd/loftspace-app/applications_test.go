package main

import "testing"

func TestComputeApplications_FiltersPrefixAndApplicant(t *testing.T) {
	entries := map[string]string{
		// alice — all gaps open, anchored to a unit. maxretries_<g> is the lens's
		// CONSTANT integer cap (3) — typing it bool would drop this row on decode.
		"leaseApplicationComplete.app1": `{"entityKey":"vtx.leaseapp.app1","applicant":"vtx.identity.alice","violating":true,"missing_onboarding":true,"missing_bgcheck":true,"missing_payment":true,"missing_signature":true,"inflight_bgcheck":false,"inflight_payment":false,"maxretries_bgcheck":3,"maxretries_payment":3,"unitKey":"vtx.unit.u1","unitAddress":"1 Market St","unitRent":2400}`,
		// alice — a second, fully converged application
		"leaseApplicationComplete.app2": `{"entityKey":"vtx.leaseapp.app2","applicant":"vtx.identity.alice","violating":false,"missing_onboarding":false,"missing_bgcheck":false,"missing_payment":false,"missing_signature":false,"unitKey":"vtx.unit.u2","unitRent":1800}`,
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
