package leasesigning

// The submitProfile leg of the renewalComplete catalog, pinned end to end: the
// lens row a never-submitted profile projects, the planner's search over the
// REAL catalog from that row, and the stale-task arm that retires the task
// when the tenant submits the profile by the apply-flow route instead.
//
// The defect this guards against: SignRenewal fails closed on a leaseapp with
// no .applicationSignals (ApplicationSignalsMissing), while the lens coalesces
// that same absence to hasGuarantor=false — which alone satisfies the goal's
// anyOf — so a catalog without the signalsSubmittedAt conjunct planned
// signRenewal first and assigned the tenant a task the op refused.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/guardgrammar"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/weaver/planner"
)

const profileSubmittedAt = "2026-08-23T12:51:29Z"

// renewalCompleteGap returns the renewalComplete target's one gap.
func renewalCompleteGap(t *testing.T) pkgmgr.GapActionSpec {
	t.Helper()
	for _, wt := range RenewalTargets() {
		if wt.TargetID == "renewalComplete" {
			ga, ok := wt.Gaps["missing_renewalComplete"]
			require.True(t, ok, "renewalComplete target has no missing_renewalComplete gap")
			return ga
		}
	}
	t.Fatal("renewalComplete weaverTarget not declared")
	return pkgmgr.GapActionSpec{}
}

// planFromRow runs the planner over the gap's declared catalog from the
// projected lens row, mapping columns onto planner paths exactly as Weaver
// does (root by default; the gap's goalColumns at their aspect-qualified
// path), and returns the plan's action refs in order.
func planFromRow(t *testing.T, ga pkgmgr.GapActionSpec, row map[string]any) []string {
	t.Helper()
	goal, err := guardgrammar.Parse(ga.Goal)
	require.NoError(t, err)
	aspectCols := map[string]guardgrammar.Path{}
	for col, p := range ga.GoalColumns {
		path, err := guardgrammar.ParsePath(p)
		require.NoError(t, err, "goalColumns[%s]", col)
		aspectCols[col] = path
	}
	state := make(planner.State, len(row))
	for col, v := range row {
		if p, ok := aspectCols[col]; ok {
			state[p] = v
			continue
		}
		state[guardgrammar.Path{Field: col}] = v
	}
	catalog := make([]planner.Action, 0, len(ga.Actions))
	for _, a := range ga.Actions {
		act := planner.Action{Ref: a.Ref, Cost: a.Cost}
		if len(a.Pre) > 0 {
			act.Precondition, err = guardgrammar.Parse(a.Pre)
			require.NoError(t, err, "action %s pre", a.Ref)
		}
		for _, e := range a.Effects {
			g, err := guardgrammar.Parse(e)
			require.NoError(t, err, "action %s effect", a.Ref)
			act.Effects = append(act.Effects, g)
		}
		catalog = append(catalog, act)
	}
	plan, err := planner.Synthesize(goal, state, catalog, len(catalog)+2)
	require.NoError(t, err, "the catalog must reach the goal from this row")
	refs := make([]string, len(plan.Steps))
	for i, s := range plan.Steps {
		refs[i] = s.ActionRef
	}
	return refs
}

// seedTermsSetRenewal seeds the live shape the defect was found in: an open
// renewal whose tenant holds a current background check and whose landlord has
// set terms, with no profile ever submitted on the renewed application.
func seedTermsSetRenewal(t *testing.T, f *lensFixture) {
	t.Helper()
	seedRenewalWithBgcheck(t, f, "2026-10-03T19:46:50Z")
	f.aspect(t, "rn", "terms", "terms", map[string]any{"setAt": "2026-09-05T22:57:14Z", "rentAmount": 2200, "termMonths": 12})
}

func TestRenewalComplete_NoProfile_ProjectsNullSignalsAndTheLeaseApp(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedTermsSetRenewal(t, f)

	rows := f.projectRenewalComplete(t, "rn")
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["signalsSubmittedAt"], "no .applicationSignals -> the stamp is null, never a false")
	require.Equal(t, false, v["hasGuarantor"], "the coalesced flag still reads a real false (the goal's anyOf needs it)")
	require.Equal(t, "vtx.leaseapp."+f.ids["app"], v["leaseApp"], "the submitProfile leg targets the application, not the renewal")
	require.Equal(t, true, v["missing_renewalComplete"])
}

func TestRenewalComplete_ProfileSubmitted_ProjectsTheStamp(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	seedTermsSetRenewal(t, f)
	f.aspect(t, "app", "applicationSignals", "applicationSignals", map[string]any{"hasGuarantor": false, "submittedAt": profileSubmittedAt})

	v := f.projectRenewalComplete(t, "rn")[0].Values
	require.Equal(t, profileSubmittedAt, v["signalsSubmittedAt"])
	require.Equal(t, false, v["hasGuarantor"])
}

// TestRenewalComplete_PlanWalksSubmitProfileBeforeSigning is the fix, proven on
// the real lens row and the real catalog: with no profile the cheapest path to
// the goal submits the profile first; with one it signs straight away; with a
// guarantor declared it verifies first. Dropping the signalsSubmittedAt
// conjunct from signRenewal's pre makes the first case plan [signRenewal].
func TestRenewalComplete_PlanWalksSubmitProfileBeforeSigning(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ga := renewalCompleteGap(t)

	t.Run("no profile submitted", func(t *testing.T) {
		f := newLensFixture(t)
		seedTermsSetRenewal(t, f)
		row := f.projectRenewalComplete(t, "rn")[0].Values
		require.Equal(t, []string{"submitProfile", "signRenewal"}, planFromRow(t, ga, row))
	})
	t.Run("profile submitted, no guarantor", func(t *testing.T) {
		f := newLensFixture(t)
		seedTermsSetRenewal(t, f)
		f.aspect(t, "app", "applicationSignals", "applicationSignals", map[string]any{"hasGuarantor": false, "submittedAt": profileSubmittedAt})
		row := f.projectRenewalComplete(t, "rn")[0].Values
		require.Equal(t, []string{"signRenewal"}, planFromRow(t, ga, row))
	})
	t.Run("fresh cycle: terms first, then the profile", func(t *testing.T) {
		// Equal-cost legs order by ref, so setTerms precedes submitProfile —
		// the tenant's own application-card route is what lets them submit
		// ahead of the chain; the FE's hint is written against this order.
		f := newLensFixture(t)
		seedRenewalWithBgcheck(t, f, "2026-10-03T19:46:50Z")
		row := f.projectRenewalComplete(t, "rn")[0].Values
		require.Equal(t, []string{"setTerms", "submitProfile", "signRenewal"}, planFromRow(t, ga, row))
	})
	t.Run("profile submitted with a guarantor", func(t *testing.T) {
		f := newLensFixture(t)
		seedTermsSetRenewal(t, f)
		f.aspect(t, "app", "applicationSignals", "applicationSignals", map[string]any{"hasGuarantor": true, "submittedAt": profileSubmittedAt})
		row := f.projectRenewalComplete(t, "rn")[0].Values
		require.Equal(t, []string{"verifyGuarantor", "signRenewal"}, planFromRow(t, ga, row))
	})
}

// TestRenewalComplete_SubmitProfileLegResolvesFromTheRow pins the leg's row
// templates against the lens's own body columns: an assignTask whose assignee
// or target names a column the row does not carry is a dispatch refusal on
// every row (strategist.go's "references row.<col>, which is null/absent").
func TestRenewalComplete_SubmitProfileLegResolvesFromTheRow(t *testing.T) {
	ga := renewalCompleteGap(t)
	var lens *pkgmgr.LensSpec
	for i := range RenewalLenses() {
		if RenewalLenses()[i].CanonicalName == "renewalComplete" {
			l := RenewalLenses()[i]
			lens = &l
		}
	}
	require.NotNil(t, lens)
	body := map[string]bool{}
	for _, c := range lens.Output.BodyColumns {
		body[c] = true
	}
	var leg *pkgmgr.ActionCatalogEntrySpec
	for i := range ga.Actions {
		if ga.Actions[i].Ref == "submitProfile" {
			leg = &ga.Actions[i]
		}
	}
	require.NotNil(t, leg, "the catalog declares a submitProfile leg")
	require.Equal(t, "assignTask", leg.Action)
	require.Equal(t, "SetApplicantProfile", leg.Operation)
	for _, tmpl := range []string{leg.Assignee, leg.Target} {
		col, ok := cutRowTemplate(tmpl)
		require.True(t, ok, "%q is a row.<column> template", tmpl)
		require.True(t, body[col], "row column %q is a declared body column of renewalComplete", col)
	}
	require.True(t, body["signalsSubmittedAt"], "the leg's effect path is a declared body column")
}

func cutRowTemplate(s string) (string, bool) {
	const prefix = "row."
	if len(s) <= len(prefix) || s[:len(prefix)] != prefix {
		return "", false
	}
	return s[len(prefix):], true
}

func TestStaleUserTasks_SetApplicantProfile_NotYetSubmitted_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.seedTask(t, "proftask", "setprofile", "SetApplicantProfile", "app", "open")

	v := f.projectStaleAt(t, "proftask", staleNow)[0].Values
	require.Equal(t, false, v["missing_cancellation"], "no .applicationSignals yet — the task is still the live remedy")
	require.Equal(t, false, v["violating"])
}

func TestStaleUserTasks_SetApplicantProfile_AlreadySubmitted_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "app", "leaseapp")
	f.aspect(t, "app", "applicationSignals", "applicationSignals", map[string]any{"hasGuarantor": false, "submittedAt": profileSubmittedAt})
	f.seedTask(t, "proftask", "setprofile", "SetApplicantProfile", "app", "open")

	v := f.projectStaleAt(t, "proftask", staleNow)[0].Values
	require.Equal(t, true, v["missing_cancellation"], "profile submitted from the apply-flow form under the tenant's own grant — this task is obsolete")
	require.Equal(t, true, v["violating"])
}

func TestStaleUserTasks_SignRenewal_AlreadySigned_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "renewal1", "renewal")
	f.aspect(t, "renewal1", "renewalSignature", "renewalSignature", map[string]any{"signedAt": "2026-09-10T00:00:00Z"})
	f.seedTask(t, "signtask", "signrenewal", "SignRenewal", "renewal1", "open")

	v := f.projectStaleAt(t, "signtask", staleNow)[0].Values
	require.Equal(t, true, v["missing_cancellation"], "the cycle is signed — a second signing task minted by a re-opened episode is obsolete")
	require.Equal(t, true, v["violating"])
}

func TestStaleUserTasks_SignRenewal_NotYetSigned_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "renewal1", "renewal")
	f.aspect(t, "renewal1", "terms", "terms", map[string]any{"setAt": "2026-08-01T00:00:00Z", "rentAmount": 2200})
	f.seedTask(t, "signtask", "signrenewal", "SignRenewal", "renewal1", "open")

	v := f.projectStaleAt(t, "signtask", staleNow)[0].Values
	require.Equal(t, false, v["missing_cancellation"], "terms set but unsigned — the signing task is still the live remedy")
	require.Equal(t, false, v["violating"])
}

func TestStaleUserTasks_VerifyGuarantor_AlreadyVerified_Violating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "renewal1", "renewal")
	f.aspect(t, "renewal1", "guarantorVerification", "guarantorVerification", map[string]any{"verifiedAt": "2026-09-10T00:00:00Z", "method": "manual"})
	f.seedTask(t, "vtask", "verifyguarantor", "VerifyGuarantor", "renewal1", "open")

	v := f.projectStaleAt(t, "vtask", staleNow)[0].Values
	require.Equal(t, true, v["missing_cancellation"])
	require.Equal(t, true, v["violating"])
}

func TestStaleUserTasks_VerifyGuarantor_NotYetVerified_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLensFixture(t)
	f.vtx(t, "renewal1", "renewal")
	f.seedTask(t, "vtask", "verifyguarantor", "VerifyGuarantor", "renewal1", "open")

	v := f.projectStaleAt(t, "vtask", staleNow)[0].Values
	require.Equal(t, false, v["missing_cancellation"])
	require.Equal(t, false, v["violating"])
}
