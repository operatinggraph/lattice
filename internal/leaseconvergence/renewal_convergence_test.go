//go:build leaseshortwindow

// Package leaseconvergence_test — the R2 renewal proof (design
// loftspace-lease-renewal-goal-authored-target-design.md §9/§10): the
// goal-authored renewalComplete target (Target B, mode:planned) converges TWO
// tenants through DIFFERENT action chains driven by the SAME catalog + goal —
// one with a guarantor and a bgcheck gone stale by the time it signs (needs
// refreshBgcheck + verifyGuarantor + setTerms + signRenewal), one with no
// guarantor and a bgcheck that stays fresh (needs only setTerms +
// signRenewal) — plus a CancelRenewal decline path that parks terminally and
// does not let the leaseExpiry target (Target A) reopen that cycle.
//
// It reuses newHarness/seedApplicant's stack wiring but seeds its OWN
// applications with an availableFrom far enough in the past that
// renewalOpensAt is already <= $now the instant DecideLeaseApplication
// approves — so Target A's missing_renewalCycle is true immediately, with no
// wall-clock wait for the real (60-day) renewal horizon. The short
// `leaseshortwindow` renewalWindow (1h) and bgcheckFreshnessWindow (25s,
// lease-signing/freshness_window_short.go) are both compiled in under this
// same build tag.
package leaseconvergence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/bridge"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
)

// withExtraLenses activates additional actor-aggregate lenses (beyond
// leaseApplicationComplete) off newHarness's ONE shared CoreKVSource —
// harnessConfig.extraLenses' doc explains why a second, independently-started
// CoreKVSource is unsafe (it would race leaseApplicationComplete's already-
// running consumer for the shared lensSourceDurableName durable and typically
// miss the historical replay). This test needs leaseExpiry + renewalComplete
// live too, since Weaver only ever sees a target's violations via its OWN
// lens's weaver-targets rows.
func withExtraLenses(names ...string) harnessOpt {
	return func(hc *harnessConfig) {
		hc.extraLenses = append(hc.extraLenses, names...)
	}
}

// seedRenewableApplication mints a fresh applicant + a fresh unit whose
// listing's availableFrom is far in the past — so once approved, the
// leaseapp's .tenancy carries a renewalOpensAt already <= $now, and the
// leaseExpiry target dispatches OpenRenewal immediately (no wait for the real
// horizon). termMonths=1 clears the short-window renewalWindow's 1-month
// floor (renewalWindowHours=1 under -tags leaseshortwindow — see
// packages/lease-signing/renewal_window_short.go). Returns the leaseapp +
// applicant keys and the unit key (the caller assigns ownership separately —
// Target A's anchor requires >= 1 manages-landlord, design §4.2).
func (h *harness) seedRenewableApplication(label string) (appKey, applicantKey, unitKey string) {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("renewal-applicant-claim-" + label + "-" + mustNanoID(h.t)))
	idReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Renewal Tenant " + label,
		"email":        "tenant-" + label + "@loftspace.example",
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
		"unit": unitKey, "line1": "1 Renewal Way " + label, "city": "Springfield", "region": "OR", "postal": "97477",
	}, &processor.ContextHint{Reads: []string{unitKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, addrReply.Status, "SetUnitAddress(%s): %+v", label, addrReply.Error)

	// availableFrom in the distant past + a 1-month term: leaseEnd is also in
	// the distant past, so renewalOpensAt (leaseEnd - renewalWindow) is too —
	// missing_renewalCycle reads true the instant .tenancy is stamped.
	listingReply := h.submitOp("SetListing", "loftspaceListing", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"unit": unitKey, "rentAmount": 2000, "rentCurrency": "USD", "bedrooms": 1,
		"availableFrom": "2020-01-01T00:00:00Z", "leaseTermMonths": 1, "status": "available",
	}, &processor.ContextHint{Reads: []string{unitKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, listingReply.Status, "SetListing(%s): %+v", label, listingReply.Error)

	appReply := h.submitOp("CreateLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"applicant": applicantKey, "unit": unitKey,
	}, &processor.ContextHint{
		Reads: []string{applicantKey, unitKey},
		OptionalReads: []string{
			"lnk.identity." + applicantKey[len("vtx.identity."):] + ".appliedToUnit.unit." + unitKey[len("vtx.unit."):],
		},
	})
	require.Equalf(h.t, processor.ReplyStatusAccepted, appReply.Status, "CreateLeaseApplication(%s): %+v", label, appReply.Error)
	appKey = appReply.PrimaryKey
	return appKey, applicantKey, unitKey
}

// assignLandlord mints a fresh landlord identity and AssignUnitOwner's them
// onto unitKey — Target A's anchor requires >= 1 manages-landlord (design
// §4.2: an ownerless unit never opens a cycle).
func (h *harness) assignLandlord(unitKey string) (landlordKey string) {
	h.t.Helper()
	claimSum := sha256.Sum256([]byte("renewal-landlord-claim-" + mustNanoID(h.t)))
	idReply := h.submitOp("CreateUnclaimedIdentity", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"name":         "Renewal Landlord " + mustNanoID(h.t),
		"email":        "landlord-" + mustNanoID(h.t) + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(claimSum[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, idReply.Status, "CreateUnclaimedIdentity(landlord): %+v", idReply.Error)
	landlordKey = idReply.PrimaryKey

	ownerReply := h.submitOp("AssignUnitOwner", "loftspaceOwnership", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"landlord": landlordKey, "unit": unitKey,
	}, &processor.ContextHint{Reads: []string{landlordKey, unitKey},
		Enumerations: testutil.DeclaredEnumerations("AssignUnitOwner", bootstrap.BootstrapIdentityKey, loftspacedomain.OpMetas())})
	require.Equalf(h.t, processor.ReplyStatusAccepted, ownerReply.Status, "AssignUnitOwner: %+v", ownerReply.Error)
	return landlordKey
}

// approveWithTenancy drives PII+sign+approve for a renewable application
// (mirrors driveApplicantSteps + decideLandlord, but against a caller-supplied
// unit rather than h.lastUnitKey, since this test seeds several units).
func (h *harness) approveWithTenancy(appKey, applicantKey, unitKey string) {
	h.t.Helper()
	piiReply := h.submitOp("RecordIdentityPII", "identity", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"identityKey": applicantKey, "ssn": "123456789", "dob": "1990-01-01",
	}, &processor.ContextHint{Reads: []string{applicantKey},
		Enumerations: testutil.DeclaredEnumerations("RecordIdentityPII", bootstrap.BootstrapIdentityKey, identitydomain.OpMetas())})
	require.Equalf(h.t, processor.ReplyStatusAccepted, piiReply.Status, "RecordIdentityPII: %+v", piiReply.Error)

	signReply := h.submitOp("SignLease", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey,
	}, &processor.ContextHint{Reads: []string{appKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, signReply.Status, "SignLease: %+v", signReply.Error)

	decideReply := h.submitOp("DecideLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "decision": "approved", "unit": unitKey,
	}, &processor.ContextHint{Reads: []string{appKey, unitKey},
		Enumerations: testutil.DeclaredEnumerations("DecideLeaseApplication", bootstrap.BootstrapIdentityKey, leasesigning.OpMetas())})
	require.Equalf(h.t, processor.ReplyStatusAccepted, decideReply.Status, "DecideLeaseApplication(approved): %+v", decideReply.Error)

	// Settle: an approve ALSO opens the pre-existing leaseApplicationComplete
	// target's missing_listingLeased gap (directOp SetListingStatus over this
	// SAME unit, targets.go). Waiting for that flip to fully settle here — before
	// this test's renewal ops touch the unit-adjacent leaseapp again — avoids
	// two concurrent actor-aggregate re-evaluations of the SAME leaseapp racing
	// against the unit's OWN concurrently-changing .listing aspect (an existing,
	// narrow full-engine re-execution consistency window, out of R2's scope).
	appID := appKey[len("vtx.leaseapp."):]
	require.Eventuallyf(h.t, func() bool {
		row := h.readRow(appID)
		return row != nil && !rowBool(row, "missing_listingLeased")
	}, 30*time.Second, 150*time.Millisecond, "the approve's own listing-leased flip must settle before the renewal chain proceeds")
}

// findRenewalKey scans Core KV for the `renews` link off appKey and returns
// the renewal vertex key it finds, or "" within the deadline. Mirrors
// serviceOutcomes' ParseLinkKey scan idiom.
func (h *harness) findRenewalKey(appID string, deadline time.Duration) string {
	h.t.Helper()
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
		if err == nil {
			for _, k := range keys {
				t1, id1, name, t2, id2, ok := substrate.ParseLinkKey(k)
				if !ok || t1 != "renewal" || name != "renews" || t2 != "leaseapp" || id2 != appID {
					continue
				}
				return "vtx.renewal." + id1
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return ""
}

// weaverTargetRow reads a row off an arbitrary weaver-targets target (unlike
// h.readRow, which is hardcoded to leaseApplicationComplete).
func (h *harness) weaverTargetRow(targetID, entityID string) map[string]any {
	h.t.Helper()
	entry, err := h.convKV.Get(h.ctx, targetID+"."+entityID)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var row map[string]any
	if json.Unmarshal(entry.Value, &row) != nil {
		return nil
	}
	return row
}

// awaitRenewalComplete polls the renewalComplete row for renewalID until
// violating flips false (the goal is met) within the deadline.
func (h *harness) awaitRenewalComplete(renewalID string, deadline time.Duration) map[string]any {
	h.t.Helper()
	cut := time.Now().Add(deadline)
	for time.Now().Before(cut) {
		row := h.weaverTargetRow("renewalComplete", renewalID)
		if row != nil && !rowBool(row, "violating") {
			return row
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.t.Fatalf("renewal %s did not converge (violating never flipped false) within %s; last row=%v",
		renewalID, deadline, h.weaverTargetRow("renewalComplete", renewalID))
	return nil
}

// TestRenewalConvergence_TwoTenantsDivergeThenDeclinePath is the R2 capstone:
//
//  1. Tenant WITH a guarantor whose ORIGINAL onboarding bgcheck is allowed to
//     go stale (the test waits past bgcheckFreshnessWindow before letting the
//     renewal chain run) converges through refreshBgcheck + verifyGuarantor +
//     setTerms + signRenewal — all FOUR catalog legs.
//  2. Tenant with NO guarantor whose bgcheck stays fresh (the renewal chain
//     runs immediately after approval, inside the freshness window) converges
//     through only setTerms + signRenewal — TWO legs, proving the anyOf
//     vacuous-satisfaction + already-true-atom-needs-no-action planner
//     properties for a REAL per-target-authored catalog end to end.
//  3. A landlord CancelRenewal on a freshly-opened THIRD cycle parks
//     terminally (status=cancelled, non-violating) and Target A's
//     leaseExpiry does NOT reopen that same cycle (cycleRenewalCount counts
//     the cancelled renewal).
func TestRenewalConvergence_TwoTenantsDivergeThenDeclinePath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	// backgroundCheckFreshness rides along because it is what RECORDS a bgcheck's
	// lapse: renewalComplete's bgcheckValidUntil reads that recorded fact, so
	// without this target's timer arming on the instance no check ever goes stale
	// and refreshBgcheck never reaches tenant A's plan.
	h := newHarness(t, withExtraLenses("leaseExpiry", "renewalComplete", "backgroundCheckFreshness"))

	// --- Tenant A: guarantor, bgcheck allowed to go stale ---
	appKeyA, applicantA, unitA := h.seedRenewableApplication("A")
	landlordA := h.assignLandlord(unitA)
	h.approveWithTenancy(appKeyA, applicantA, unitA)

	// hasGuarantor=true so the goal's anyOf disjunct requires a real
	// verification (verifyGuarantor becomes pre-eligible, per the catalog).
	profileReply := h.submitOp("SetApplicantProfile", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKeyA, "unit": unitA, "annualIncome": 60000, "employmentStatus": "employed",
		"hasGuarantor": true, "guarantorName": "Pat Guarantor", "guarantorRelationship": "parent", "guarantorAnnualIncome": 90000,
	}, &processor.ContextHint{Reads: []string{appKeyA}})
	require.Equalf(t, processor.ReplyStatusAccepted, profileReply.Status, "SetApplicantProfile(A): %+v", profileReply.Error)

	appIDA := appKeyA[len("vtx.leaseapp."):]
	applicantIDA := applicantA[len("vtx.identity."):]
	// The ORIGINAL onboarding check, named before any re-dispatch can mint a
	// second one — it is THIS instance whose lapse the wait below is about. It is
	// minted by the triggerLoom -> Loom -> externalTask -> bridge chain, which
	// nothing before this point waits on (the preceding barrier is the shorter
	// missing_listingLeased directOp leg), so the lookup polls rather than taking
	// one scan of Core KV.
	var originalBgcheckA string
	require.Eventuallyf(t, func() bool {
		originalBgcheckA = h.bgcheckHandle(applicantIDA)
		return originalBgcheckA != ""
	}, 30*time.Second, 200*time.Millisecond,
		"tenant A's onboarding bgcheck instance must exist before the renewal legs run")
	renewalKeyA := h.findRenewalKey(appIDA, 30*time.Second)
	require.NotEmpty(t, renewalKeyA, "Target A must open a renewal cycle for tenant A (renewalOpensAt is already past)")
	renewalIDA := renewalKeyA[len("vtx.renewal."):]

	// Snapshot Target A's leaseExpiry row BEFORE signing — the current cycle is
	// still open, so missing_renewalCycle should already read false (a renewal
	// exists for this cycleEnd) and freshUntil still reflects the ORIGINAL
	// (pre-extension) renewalOpensAt. This is the baseline §4.4 close-cascade
	// assertion below compares against.
	preSignRowA := h.weaverTargetRow("leaseExpiry", appIDA)
	require.NotNilf(t, preSignRowA, "the leaseExpiry row for tenant A's leaseapp must exist once the cycle has opened")
	preSignFreshUntilA, _ := preSignRowA["freshUntil"].(string)

	// Let tenant A's ORIGINAL onboarding bgcheck (25s window) lapse before doing
	// anything else on the renewal. The lapse is a RECORDED fact now, not a clock
	// reading, so sleeping past the window proves nothing: wait for that
	// instance's own backgroundCheckFreshness row to go null, which happens only
	// once its @at fired and MarkExpired committed the lapse onto it.
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("backgroundCheckFreshness", originalBgcheckA)
		return row != nil && row["freshUntil"] == nil
	}, 90*time.Second, 300*time.Millisecond,
		"tenant A's ORIGINAL onboarding bgcheck must be RECORDED lapsed before the renewal legs run")

	// Drive the landlord legs directly (the ephemeral task-grant UI path is out of
	// scope for this platform-mechanics proof; the operator-model direct-op path
	// exercises the SAME write-guards SignRenewal's pre mirrors).
	//
	// termMonthsA is picked large enough that the EXTENDED leaseEnd (hence the
	// recomputed renewalOpensAt) lands safely past $now — the seed leaseEnd is
	// pinned at 2020-02-01 (seedRenewableApplication's distant-past anchor, so
	// the ORIGINAL cycle opens immediately), but the §4.4 re-arm proof below
	// needs the NEW renewalOpensAt to be a concrete future freshUntil rather
	// than null-when-past, so a fixed termMonths=1 (which only reaches
	// 2020-03-01) would not do. 12*8 months comfortably clears "now" with
	// years of margin against calendar drift.
	termsReplyA := h.submitOp("SetRenewalTerms", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKeyA, "rentAmount": 2100, "termMonths": 12 * 8,
	}, &processor.ContextHint{Reads: []string{renewalKeyA}})
	require.Equalf(t, processor.ReplyStatusAccepted, termsReplyA.Status, "SetRenewalTerms(A): %+v", termsReplyA.Error)

	// Settle SetRenewalTerms' own reprojection before firing VerifyGuarantor —
	// two aspect writes on the SAME renewal vertex in quick succession can each
	// trigger their own aspect-fan-out re-evaluation of that one actor; waiting
	// here avoids two concurrent re-evaluations of renewalComplete's cypher
	// racing each other (the same narrow full-engine re-execution consistency
	// window worked around in approveWithTenancy, out of R2's scope).
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalIDA)
		return row != nil && row["termsSetAt"] != nil
	}, 15*time.Second, 150*time.Millisecond, "SetRenewalTerms(A) must settle before VerifyGuarantor(A) fires")

	// renewalComplete arms NO freshness timer. The window it reports on belongs to
	// a background-check instance reached across providedTo, and that instance
	// carries its own target; a timer here could only mark the RENEWAL, which is
	// neither where the deadline lapses nor where any reader looks. The wait above
	// proves the row has reprojected at least twice under the current descriptor,
	// so an armed schedule would already exist if the lens still projected one —
	// and the window below spans the VerifyGuarantor reprojection too.
	require.Neverf(t, func() bool {
		return h.scheduleArmed("schedule.weaver.timer.renewalComplete." + renewalIDA)
	}, 3*time.Second, 200*time.Millisecond,
		"no @at may be armed on the renewal itself (renewalComplete projects no freshUntil); renewal=%s", renewalIDA)

	verifyReplyA := h.submitOp("VerifyGuarantor", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKeyA, "leaseApp": appKeyA, "applicant": applicantA, "method": "phone",
	}, &processor.ContextHint{Reads: []string{renewalKeyA}})
	require.Equalf(t, processor.ReplyStatusAccepted, verifyReplyA.Status, "VerifyGuarantor(A): %+v", verifyReplyA.Error)

	// Settle VerifyGuarantor's own reprojection before the eventual SignRenewal
	// polling loop begins (same rationale).
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalIDA)
		return row != nil && row["guarantorVerifiedAt"] != nil
	}, 15*time.Second, 150*time.Millisecond, "VerifyGuarantor(A) must settle before the SignRenewal polling loop begins")

	// Proof that refreshBgcheck actually fired for tenant A: the applicant now
	// carries TWO bgcheck outcomes — the original onboarding one (now stale,
	// having lapsed during the 30s sleep above) PLUS the renewal chain's own
	// refresh — rather than asserting the transient stale state directly
	// (Weaver's planner may refresh it before any poll observes the null
	// window, since the refresh is dispatched autonomously the moment the
	// goal search finds bgcheckValidUntil unmet, not on this test's schedule).
	require.Eventuallyf(t, func() bool {
		return h.countBgcheckOutcomes(applicantIDA) >= 2
	}, 30*time.Second, 300*time.Millisecond,
		"tenant A must show a SECOND bgcheck outcome (the renewal chain's refreshBgcheck leg, beyond onboarding's original)")

	// SignRenewal is rejected until the goal's remainder holds (bgcheck fresh
	// again + guarantor verified + terms set) — the write-path mirror of the
	// planner's terminal-leg pre. Poll it (the refresh leg is Weaver-dispatched,
	// not caller-driven) rather than asserting a single premature attempt.
	require.Eventuallyf(t, func() bool {
		reply := h.submitOp("SignRenewal", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
			"renewalKey": renewalKeyA, "leaseApp": appKeyA, "applicant": applicantA,
		}, &processor.ContextHint{Reads: []string{renewalKeyA}})
		return reply.Status == processor.ReplyStatusAccepted
	}, 60*time.Second, 500*time.Millisecond, "SignRenewal(A) must eventually succeed once the refreshed bgcheck is fresh")

	rowA := h.awaitRenewalComplete(renewalIDA, 10*time.Second)
	require.Truef(t, rowA["signedAt"] != nil, "tenant A's renewal row must carry signedAt; row=%v", rowA)
	require.Equal(t, landlordA, rowA["landlord"], "the row's landlord column is the min-key manages pick")

	// The leaseapp's .tenancy extended (leaseEnd advanced past the original term).
	tenancyA := h.aspectData(appKeyA, "tenancy")
	require.NotNil(t, tenancyA, "the leaseapp must still carry .tenancy after SignRenewal")
	require.NotEqual(t, "2020-02-01T00:00:00Z", tenancyA["leaseEnd"], "SignRenewal must extend leaseEnd past the original 1-month term")
	newRenewalOpensAtA, _ := tenancyA["renewalOpensAt"].(string)
	require.NotEmptyf(t, newRenewalOpensAtA, "the extended tenancy must carry a recomputed renewalOpensAt; tenancy=%v", tenancyA)

	// §4.4 close-cascade proof: Target A (leaseExpiry) reprojects off the
	// EXTENDED .tenancy — missing_renewalCycle reads false (this cycle is
	// satisfied: the just-signed renewal's cycleEnd matches the OLD leaseEnd)
	// AND freshUntil re-arms forward to the NEW renewalOpensAt the extension
	// just derived, so the sweep is armed for the NEXT cycle rather than
	// stuck re-evaluating a stale horizon.
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("leaseExpiry", appIDA)
		if row == nil {
			return false
		}
		fu, _ := row["freshUntil"].(string)
		return !rowBool(row, "missing_renewalCycle") && fu == newRenewalOpensAtA
	}, 15*time.Second, 200*time.Millisecond,
		"Target A must re-arm for the next cycle after SignRenewal(A): missing_renewalCycle=false and freshUntil advanced to the new renewalOpensAt %q (was %q pre-sign)",
		newRenewalOpensAtA, preSignFreshUntilA)
	postSignRowA := h.weaverTargetRow("leaseExpiry", appIDA)
	require.NotEqualf(t, preSignFreshUntilA, postSignRowA["freshUntil"],
		"freshUntil must have moved FORWARD off the pre-sign value once .tenancy extended; row=%v", postSignRowA)

	// --- Tenant B: no guarantor, bgcheck stays fresh (short chain) ---
	appKeyB, applicantB, unitB := h.seedRenewableApplication("B")
	landlordB := h.assignLandlord(unitB)
	h.approveWithTenancy(appKeyB, applicantB, unitB)

	// A guarantor-less profile submission (hasGuarantor omitted): renewalComplete
	// projects hasGuarantor as (app.applicationSignals.data.hasGuarantor = True),
	// so this genuinely projects a real false (renewal_lenses.go) and the goal's
	// anyOf disjunct is satisfied NON-vacuously — verifyGuarantor never becomes
	// pre-eligible, so the planner must never dispatch it. (SignRenewal/
	// VerifyGuarantor now fail closed — ApplicationSignalsMissing — when
	// .applicationSignals is absent entirely, so a profile submission, even a
	// guarantor-less one, is required before the chain can complete at all.)
	profileReplyB := h.submitOp("SetApplicantProfile", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKeyB, "unit": unitB, "annualIncome": 60000, "employmentStatus": "employed",
	}, &processor.ContextHint{Reads: []string{appKeyB}})
	require.Equalf(t, processor.ReplyStatusAccepted, profileReplyB.Status, "SetApplicantProfile(B): %+v", profileReplyB.Error)

	appIDB := appKeyB[len("vtx.leaseapp."):]
	renewalKeyB := h.findRenewalKey(appIDB, 30*time.Second)
	require.NotEmpty(t, renewalKeyB, "Target A must open a renewal cycle for tenant B")
	renewalIDB := renewalKeyB[len("vtx.renewal."):]

	// Drive immediately (inside the bgcheck freshness window) — the ORIGINAL
	// onboarding bgcheck stays fresh through to signing, so refreshBgcheck must
	// never fire for this tenant.
	termsReplyB := h.submitOp("SetRenewalTerms", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKeyB, "rentAmount": 2050, "termMonths": 1,
	}, &processor.ContextHint{Reads: []string{renewalKeyB}})
	require.Equalf(t, processor.ReplyStatusAccepted, termsReplyB.Status, "SetRenewalTerms(B): %+v", termsReplyB.Error)

	// Settle before SignRenewal(B) — see the tenant-A settle-wait rationale above.
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalIDB)
		return row != nil && row["termsSetAt"] != nil
	}, 15*time.Second, 150*time.Millisecond, "SetRenewalTerms(B) must settle before SignRenewal(B) fires")

	signReplyB := h.submitOp("SignRenewal", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKeyB, "leaseApp": appKeyB, "applicant": applicantB,
	}, &processor.ContextHint{Reads: []string{renewalKeyB}})
	require.Equalf(t, processor.ReplyStatusAccepted, signReplyB.Status, "SignRenewal(B): %+v", signReplyB.Error)

	rowB := h.awaitRenewalComplete(renewalIDB, 10*time.Second)
	require.Truef(t, rowB["signedAt"] != nil, "tenant B's renewal row must carry signedAt; row=%v", rowB)
	require.Nilf(t, rowB["guarantorVerifiedAt"], "tenant B never had a guarantor to verify; row=%v", rowB)
	require.Equal(t, landlordB, rowB["landlord"])

	// --- Decline path: a THIRD tenant's cycle is cancelled, not signed, and
	// Target A does not reopen it. ---
	appKeyC, applicantC, unitC := h.seedRenewableApplication("C")
	h.assignLandlord(unitC)
	h.approveWithTenancy(appKeyC, applicantC, unitC)
	appIDC := appKeyC[len("vtx.leaseapp."):]
	renewalKeyC := h.findRenewalKey(appIDC, 30*time.Second)
	require.NotEmpty(t, renewalKeyC, "Target A must open a renewal cycle for tenant C")
	renewalIDC := renewalKeyC[len("vtx.renewal."):]

	cancelReplyC := h.submitOp("CancelRenewal", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKeyC, "reason": "Selling the property.",
	}, &processor.ContextHint{Reads: []string{renewalKeyC}})
	require.Equalf(t, processor.ReplyStatusAccepted, cancelReplyC.Status, "CancelRenewal(C): %+v", cancelReplyC.Error)

	// The renewalComplete row settles non-violating (status=cancelled is a
	// terminal disposition — open=false gates the gap off).
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalIDC)
		return row != nil && !rowBool(row, "open") && !rowBool(row, "violating")
	}, 15*time.Second, 200*time.Millisecond, "a cancelled renewal must settle non-violating (terminal)")
	rowC := h.weaverTargetRow("renewalComplete", renewalIDC)
	require.Nilf(t, rowC["signedAt"], "a cancelled renewal must never carry a signature; row=%v", rowC)

	// Hold: Target A must NOT reopen a SECOND renewal for the SAME cycle (a
	// cancelled renewal still counts as this cycle's disposition, design §4.4).
	cut := time.Now().Add(5 * time.Second)
	for time.Now().Before(cut) {
		row := h.weaverTargetRow("leaseExpiry", appIDC)
		require.NotNilf(t, row, "the leaseExpiry row for the cancelled-cycle leaseapp must still exist")
		require.Falsef(t, rowBool(row, "missing_renewalCycle"),
			"a cancelled cycle must not be reopened by the sweep; row=%v", row)
		time.Sleep(250 * time.Millisecond)
	}
	// No SECOND renewal vertex was ever minted for this leaseapp.
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	renewalCount := 0
	for _, k := range keys {
		t1, _, name, t2, id2, ok := substrate.ParseLinkKey(k)
		if ok && t1 == "renewal" && name == "renews" && t2 == "leaseapp" && id2 == appIDC {
			renewalCount++
		}
	}
	require.Equal(t, 1, renewalCount, "exactly one renewal cycle must exist for the cancelled leaseapp — no reopen")
}

// --- the goal gap's external leg (weaver-goal-leg-external-class-design.md §9) ---
//
// renewalComplete is the corpus's one goal-mode target, and its catalog is
// MIXED: refreshBgcheck is a triggerLoom over the externalTask-only
// backgroundCheck pattern, while verifyGuarantor / setTerms / signRenewal are
// human assignTasks. The two tests below are the ephemeral-stack halves of that
// design's Increment 3 — the reclaim payoff on the external leg, and the
// leg-scoping of the inflight_renewalComplete companion that makes the payoff
// safe over a mixed catalog. The rule-engine halves live beside the cypher
// (packages/lease-signing/bgcheck_freshness_lens_test.go).

// renewalLegMarkLease is the Weaver mark lease both tests below run at, and it
// is what paces every reclaim they observe: a leg's expired episode is only
// reconsidered once its lease runs out.
//
// It carries the same FLOOR asyncMarkLease documents, for the same reason.
// inflight_renewalComplete is presence-based on the instance's .dispatch aspect,
// which the BRIDGE writes only once its adapter has accepted the call, so
// between the dispatch op committing and .dispatch landing the row reads
// not-in-flight over a call that already exists. A lease whose whole span fits
// inside that window would be reclaimed as concluded and mint a genuinely second
// call — correct per §10.3, but not what these tests are measuring. 5s against
// the tens of milliseconds a local bridge turnaround takes is ~100×;
// production's default is ~36000×.
const renewalLegMarkLease = 5 * time.Second

// renewalLegOpts paces Weaver so a mark's lease actually expires and the
// reconciler sweep actually ticks inside a test, without touching the bridge's
// own horizons. bgcheckAsync selects the adapter: nil keeps the production-
// faithful synchronous FakeBackgroundCheck (every call concludes inline, so no
// .dispatch marker is ever written and nothing is ever in flight); a non-nil
// FakeAsyncCheck that never resolves keeps every call WITHHELD — accepted by the
// vendor, .dispatch written, no outcome — which is the only shape that makes
// inflight_renewalComplete readable as true.
//
// The bridge's CallDeadline is pushed past the harness context's own ceiling so
// the give-up timeout never fires: these tests decide when a withheld call
// concludes (by submitting the replyOp themselves), rather than racing a
// wall-clock horizon.
func renewalLegOpts(bgcheckAsync *bridge.FakeAsyncCheck) []harnessOpt {
	return []harnessOpt{func(hc *harnessConfig) {
		hc.bgcheckAsync = bgcheckAsync
		hc.weaverMarkLease = renewalLegMarkLease
		hc.weaverSweepInterval = 500 * time.Millisecond
		hc.weaverSweepWarmup = 500 * time.Millisecond
		hc.bridgePollInterval = 2 * time.Second
		hc.bridgeCallDeadline = 10 * time.Minute
	}}
}

// weaverStateDoc reads one weaver-state document (a mark, or a `__count`),
// returning nil when the key is absent or unreadable. Weaver-state is the
// engine's own dispatch ledger — the only place a gap's LEG and its per-leg
// attempt tally are observable — and neither is projected into any lens row.
func (h *harness) weaverStateDoc(key string) map[string]any {
	entry, err := h.conn.KVGet(h.ctx, bootstrap.WeaverStateBucket, key)
	if err != nil || entry == nil || len(entry.Value) == 0 {
		return nil
	}
	var doc map[string]any
	if json.Unmarshal(entry.Value, &doc) != nil {
		return nil
	}
	return doc
}

// gapMarkClaimID returns the per-open-episode claimId on a gap's in-flight mark,
// or "" when no mark stands. Every Loom instance id a triggerLoom dispatch
// supplies is claimId-seeded, so a reclaim that PRESERVES this value re-dispatches
// onto the same (already terminal) instance as a no-op, while one that MINTS A
// FRESH value produces a genuinely new instance — the §10.3 difference the
// external class buys, observed at its source.
func (h *harness) gapMarkClaimID(targetID, entityID, gapColumn string) string {
	doc := h.weaverStateDoc(targetID + "." + entityID + "." + gapColumn)
	if doc == nil {
		return ""
	}
	id, _ := doc["claimId"].(string)
	return id
}

// gapDispatchCount reads a gap's dispatch-count document: the attempts booked
// against the chain (count) and the catalog Ref they are charged to (leg). For a
// GOAL gap the tally is leg-scoped — a leg change restarts it — so `count` is
// the attempt tally of the named leg alone, which is what makes "the
// refreshBgcheck leg reads 2" a statement about the external leg rather than
// about the renewal's whole chain. present=false means no dispatch has been
// booked for this gap yet.
func (h *harness) gapDispatchCount(targetID, entityID, gapColumn string) (count int, leg string, present bool) {
	doc := h.weaverStateDoc(targetID + "." + entityID + "." + gapColumn + ".__count")
	if doc == nil {
		return 0, "", false
	}
	n, _ := doc["count"].(float64)
	leg, _ = doc["leg"].(string)
	return int(n), leg, true
}

// taskScopedTo returns the bare id of a task vertex scopedTo the given entity id,
// or "" when none exists. It is awaitDispatchedTask's single-scan sibling: a hold
// that asserts NO task was dispatched needs one scan per tick, not a helper that
// polls for its own deadline.
func (h *harness) taskScopedTo(scopedToID string) string {
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	if errors.Is(err, context.Canceled) || substrate.IsConnectionError(err) {
		return ""
	}
	if err != nil {
		return ""
	}
	for _, k := range keys {
		t1, taskID, name, _, dstID, ok := substrate.ParseLinkKey(k)
		if ok && t1 == "task" && name == "scopedTo" && dstID == scopedToID {
			return taskID
		}
	}
	return ""
}

// taskAssignedTo returns the bare identity id a task is assignedTo, or "" when
// the link is absent. The renewal catalog assigns its three human legs to two
// different people — setTerms and verifyGuarantor to the landlord, signRenewal to
// the tenant — so the assignee identifies WHICH leg a dispatched task belongs to
// without resolving the task's forOperation meta-vertex.
func (h *harness) taskAssignedTo(taskID string) string {
	keys, err := h.conn.KVListKeys(h.ctx, bootstrap.CoreKVBucket)
	if err != nil {
		return ""
	}
	for _, k := range keys {
		t1, srcID, name, _, dstID, ok := substrate.ParseLinkKey(k)
		if ok && t1 == "task" && srcID == taskID && name == "assignedTo" {
			return dstID
		}
	}
	return ""
}

// withheldBgchecks returns the applicant's background-check instances that the
// vendor ACCEPTED and has not answered — a .dispatch marker with a vendorRef, no
// .outcome. It is exactly the population the lens's bgInflight fan counts, read
// back off Core KV so a test can say which instance is holding the column up.
func (h *harness) withheldBgchecks(applicantID string) (handles []string) {
	for _, svcKey := range h.serviceOutcomes(applicantID) {
		if !h.isBgcheckInstance(svcKey) {
			continue
		}
		dispatch := h.aspectData(svcKey, "dispatch")
		if dispatch == nil || dispatch["vendorRef"] == nil {
			continue
		}
		if h.aspectData(svcKey, "outcome") == nil {
			handles = append(handles, svcKey[len("vtx.service."):])
		}
	}
	return handles
}

// concludeBgcheck posts the terminal outcome the bridge would post, through the
// real replyOp: a definitive vendor verdict on one instance handle. `failed` is a
// business rejection — a check that CONCLUDED without success — which is the
// state the goal leg's reclaim is allowed to retry; it is deliberately not a
// withheld reply, which is the lost-after-accept state no presence-based column
// can separate.
func (h *harness) concludeBgcheck(handle, status string) {
	h.t.Helper()
	reply := h.submitOp("RecordLeaseServiceOutcome", "leaseServiceReply", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"externalRef": handle, "status": status, "result": "renewal-leg e2e: " + status,
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, reply.Status,
		"RecordLeaseServiceOutcome(%s, %s): %+v", handle, status, reply.Error)
}

// seedCompletedBgcheck mints one background-check instance for the subject and
// records a COMPLETED outcome on it, through the two real ops the platform uses
// (CreateLeaseServiceInstance as Loom's relay actor, then the replyOp). The
// outcome's validUntil is derived from the op's own submittedAt plus the
// package's freshness window, so the check reads CURRENT to every reader for that
// window.
//
// The fixture mints it rather than waiting for the platform to, because the state
// the scoping test needs — a completed check standing BESIDE a withheld one — is
// one the running chain never produces on its own: the suppression under test is
// exactly what keeps a second check from being dispatched while the first is in
// flight.
func (h *harness) seedCompletedBgcheck(subjectKey string) string {
	h.t.Helper()
	handle := mustNanoID(h.t)
	create := h.submitOp("CreateLeaseServiceInstance", "leaseServiceInstance", "default", bootstrap.LoomIdentityKey, map[string]any{
		"instanceKey": handle,
		"subjectKey":  subjectKey,
		"adapter":     "backgroundCheck",
		"replyOp":     "RecordLeaseServiceOutcome",
		"params":      map[string]any{"family": "backgroundCheck"},
	}, &processor.ContextHint{Reads: []string{subjectKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, create.Status, "CreateLeaseServiceInstance(seed): %+v", create.Error)
	h.concludeBgcheck(handle, "completed")
	return handle
}

// TestRenewalConvergence_ExternalLegReclaimsAfterAFailedCheck is the payoff half
// of the goal-leg external-class design (§1.2 rows 1-2, §9 Inc 3): the renewal's
// refreshBgcheck leg is a triggerLoom over an externalTask-only pattern, so when
// its check CONCLUDES WITHOUT SUCCESS the expired episode must be reclaimed with a
// FRESH claimId — a genuinely new vendor call — rather than collapsing back onto
// the terminal instance forever.
//
// The vector is a `failed` reply, not a withheld one, and that choice is
// load-bearing: a withheld reply is the lost-after-accept state (§1.2 row 3),
// which a presence-based in-flight column cannot separate from a healthy call and
// which this design explicitly does not fix. Using it as the payoff would assert
// something untrue.
//
// The renewal's tenant reaches this state the way a real one does — an onboarding
// check that CLEARED and then LAPSED — after which every later check is declined,
// so no completed check ever stands again and bgcheckValidUntil is null for the
// rest of the run.
//
// Two things are asserted, and they are the two halves of the same mechanism: the
// mark's claimId CHANGES across the reclaim (the mint the external class grants),
// and the count document's refreshBgcheck leg reaches 2 (the reclaim booked a real
// ATTEMPT, not a collapse-only re-arm). Before the engine classified a goal gap by
// its resolved leg, both stood still forever: staleMark read the playbook entry,
// whose Action is empty for every goal gap, and answered "never makes an external
// call".
func TestRenewalConvergence_ExternalLegReclaimsAfterAFailedCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	// backgroundCheckFreshness rides along because it is what RECORDS a lapse:
	// freshness is a recorded fact, not a clock reading, so without this target's
	// timer arming on the instance the onboarding check never goes stale and the
	// renewal's bgcheck atom is never unmet. leaseExpiry / renewalComplete are
	// deliberately NOT activated at boot — see the late activation below.
	h := newHarness(t, append(renewalLegOpts(nil), withExtraLenses("backgroundCheckFreshness"))...)

	appKey, applicant, unit := h.seedRenewableApplication("R")
	h.assignLandlord(unit)
	h.approveWithTenancy(appKey, applicant, unit)
	appID := appKey[len("vtx.leaseapp."):]
	applicantID := applicant[len("vtx.identity."):]

	// A guarantor-less profile: renewalComplete projects hasGuarantor as
	// (…hasGuarantor = True), so this is a REAL false and the goal's anyOf
	// disjunct is satisfied without any verification — verifyGuarantor never
	// becomes pre-eligible, which keeps the catalog's actionable set small enough
	// to reason about below.
	profile := h.submitOp("SetApplicantProfile", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "unit": unit, "annualIncome": 60000, "employmentStatus": "employed",
	}, &processor.ContextHint{Reads: []string{appKey}})
	require.Equalf(t, processor.ReplyStatusAccepted, profile.Status, "SetApplicantProfile(R): %+v", profile.Error)

	// The onboarding check clears (the synchronous adapter is still in its
	// default clearing mode), so the tenant genuinely holds a CURRENT check
	// before anything about the renewal is in play.
	var onboardingBgcheck string
	require.Eventuallyf(t, func() bool {
		onboardingBgcheck = h.bgcheckHandle(applicantID)
		if onboardingBgcheck == "" {
			return false
		}
		outcome := h.aspectData("vtx.service."+onboardingBgcheck, "outcome")
		return outcome != nil && outcome["status"] == "completed"
	}, 60*time.Second, 250*time.Millisecond, "the onboarding background check must clear before it can lapse")

	// Arm the decline BEFORE the lapse re-opens the gap: from here every check the
	// platform dispatches concludes `failed`, so no completed check ever stands
	// again and bgcheckValidUntil stays null for the rest of the run. Armed after
	// the clearing outcome above is observed and well before the lapse (a whole
	// freshness window away), so which mode each dispatch sees is decided, not raced.
	h.bgFake.SetDeclineAll(true)

	// The lapse is a RECORDED fact: wait for the instance's own
	// backgroundCheckFreshness row to go null, which happens only once its @at
	// fired and MarkExpired committed the lapse onto it. Sleeping past the window
	// would prove nothing.
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("backgroundCheckFreshness", onboardingBgcheck)
		return row != nil && row["freshUntil"] == nil
	}, 90*time.Second, 300*time.Millisecond,
		"the onboarding check must be RECORDED lapsed — the state that leaves the renewal's bgcheck atom unmet")

	// The lapse re-opens the STATIC target's own missing_bgcheck gap over the same
	// providedTo fan (lenses.go: a leased AND approved application re-opens on the
	// lapse), and that gap dispatches its own declined checks for this same tenant.
	// It is bounded by its own maxretries_bgcheck, so it spends that budget and
	// stops — and only once it has is the tenant's instance population attributable
	// to the renewal leg alone. This is the §3.5 static-target overlap, waited out
	// rather than assumed away.
	staticCap := 0
	require.Eventuallyf(t, func() bool {
		row := h.readRow(appID)
		if row == nil {
			return false
		}
		declared, ok := row["maxretries_bgcheck"].(float64)
		if !ok || declared <= 0 {
			return false
		}
		staticCap = int(declared)
		// The static gap is not a goal gap, so its count document records the
		// dispatch ACTION rather than a catalog Ref — only the tally is read here.
		count, _, present := h.gapDispatchCount("leaseApplicationComplete", appID, "missing_bgcheck")
		return present && count >= staticCap
	}, 90*time.Second, 250*time.Millisecond,
		"the static bgcheck gap must spend its whole retry budget on declined checks before the renewal leg is measured")

	// …and stay spent: no further static dispatch across a full mark-lease expiry
	// plus several sweep ticks, which is what makes the baseline below a fixed
	// point rather than a sample of a still-moving population.
	baseline := h.countBgcheckInstances(applicantID)
	require.Neverf(t, func() bool {
		return h.countBgcheckInstances(applicantID) != baseline
	}, 2*renewalLegMarkLease, 300*time.Millisecond,
		"the exhausted static gap must dispatch nothing more; baseline=%d cap=%d", baseline, staticCap)

	// Only NOW does the renewal chain come into being. Activating its two lenses
	// late is what keeps the whole static-exhaustion phase above free of renewal
	// dispatches — a lens Weaver cannot see projects no rows, so no renewal target
	// competes for the same tenant's checks while the budget is being spent.
	h.activateActorAggregateLensNow(h.ctx, "leaseExpiry")
	h.activateActorAggregateLensNow(h.ctx, "renewalComplete")

	renewalKey := h.findRenewalKey(appID, 60*time.Second)
	require.NotEmpty(t, renewalKey, "leaseExpiry must open a renewal cycle once its lens is live")
	renewalID := renewalKey[len("vtx.renewal."):]

	// Set the terms directly so refreshBgcheck is the catalog's ONE actionable
	// leg: with terms set, no guarantor claimed and bgcheckValidUntil null,
	// signRenewal's pre is unmet and verifyGuarantor is not pre-eligible, so every
	// dispatch the gap makes from here is the external one. (The gap's count is
	// leg-scoped, so whichever leg Weaver picked before this lands restarts the
	// tally at the boundary rather than polluting it.)
	terms := h.submitOp("SetRenewalTerms", "renewal", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"renewalKey": renewalKey, "rentAmount": 2100, "termMonths": 12,
	}, &processor.ContextHint{Reads: []string{renewalKey}})
	require.Equalf(t, processor.ReplyStatusAccepted, terms.Status, "SetRenewalTerms(R): %+v", terms.Error)
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalID)
		return row != nil && row["termsSetAt"] != nil
	}, 30*time.Second, 200*time.Millisecond, "SetRenewalTerms must settle before the external leg is the only one left")

	// The reclaim payoff. Every claimId the mark carries is collected as the poll
	// runs, because the reclaim REPLACES the mark in place: sampling the value once
	// before and once after would be a race, while accumulating every distinct
	// value the episode ever showed is not. A collapse-only reclaim preserves the
	// claimId, so the set stays at one member however long the poll runs.
	claimIDs := map[string]bool{}
	var legCount int
	require.Eventuallyf(t, func() bool {
		if id := h.gapMarkClaimID("renewalComplete", renewalID, "missing_renewalComplete"); id != "" {
			claimIDs[id] = true
		}
		count, leg, present := h.gapDispatchCount("renewalComplete", renewalID, "missing_renewalComplete")
		if !present || leg != "refreshBgcheck" {
			return false
		}
		legCount = count
		return count >= 2
	}, 120*time.Second, 200*time.Millisecond,
		"the refreshBgcheck leg must book a SECOND attempt after its failed check's lease expires — "+
			"a collapse-only reclaim books no attempt at all and the tally stands at 1 forever")

	require.Equal(t, 2, legCount,
		"the count document tallies attempts on the refreshBgcheck leg alone (a goal gap's tally is leg-scoped)")
	require.GreaterOrEqualf(t, len(claimIDs), 2,
		"the reclaim must MINT a fresh claimId, not preserve the concluded episode's — every Loom instance id is "+
			"claimId-seeded, so a preserved claimId re-dispatches onto the terminal instance as a no-op; saw %v", claimIDs)

	// The observable consequence of that fresh mint: a genuinely SECOND Loom
	// instance, and so a second background-check claim vertex providedTo the
	// tenant, beyond everything the static gap left behind.
	//
	// The ledger LEADS the artifact: the reclaim books its attempt as it re-arms
	// the mark, while the instance it dispatches only exists once triggerLoom →
	// Loom → the externalTask's CreateLeaseServiceInstance has committed. So this
	// is a wait, not a read — an immediate assertion here catches the window in
	// between and reads one instance short.
	require.Eventuallyf(t, func() bool {
		return h.countBgcheckInstances(applicantID) >= baseline+2
	}, 60*time.Second, 200*time.Millisecond,
		"the refreshBgcheck leg must have minted TWO background-check instances (the first attempt and the reclaim's "+
			"fresh one) beyond the %d the static gap left; a preserved claimId collapses the second onto the first",
		baseline)

	// Each renewal-leg attempt concluded with a definitive vendor rejection —
	// the state §1.2 row 1 names, and the whole reason the retry is legitimate.
	require.Eventuallyf(t, func() bool {
		return h.failedBgcheckInstances(applicantID) >= baseline+1
	}, 60*time.Second, 200*time.Millisecond,
		"the renewal leg's own attempts must carry terminal failed outcomes — a check that concluded without success, "+
			"never a withheld reply")

	// The gap is still open: a retried external leg is a bounded retry toward the
	// goal, not a silent close.
	row := h.weaverTargetRow("renewalComplete", renewalID)
	require.NotNil(t, row)
	require.Nil(t, row["bgcheckValidUntil"], "no check ever cleared again, so the goal's bgcheck atom stays unmet")
	require.Equal(t, true, rowBool(row, "missing_renewalComplete"), "the renewal still has work to do")
}

// TestRenewalConvergence_InflightIsLegScopedAcrossTheStaticCheck is the scoping
// half (§3.5, the design's BLOCKING adversarial finding). Weaver answers a gap's
// suppression gate on inflight_<g> BEFORE it binds a leg, so over renewalComplete's
// mixed catalog the bare in-flight fact would park the landlord's and the tenant's
// human tasks behind ANY background check running for the same tenant — including
// the static leaseApplicationComplete target's own check, which fans through the
// identical providedTo hop. Conjoining the in-flight fact with the external leg's
// unmet effect (bgcheckValidUntil = null) makes the column read true only while the
// check is what the chain is still waiting on.
//
// The two states are asserted in sequence on ONE renewal:
//
//  1. a WITHHELD check (the vendor accepted it, .dispatch written, no outcome) and
//     no completed check ⇒ the column reads true and the gap dispatches nothing —
//     no task for the landlord, no second check.
//  2. a completed, unlapsed check standing BESIDE that same still-withheld one ⇒
//     the column flips FALSE and the human leg dispatches. This is the
//     discriminating vector: the bare in-flight fact is unchanged between (1) and
//     (2) — a check is in flight throughout — so only the conjunct can move.
//
// The withheld check is the STATIC target's, which is the overlap §3.5 is about;
// the completed one is seeded, because the suppression under test is precisely
// what stops the running chain from producing a second check while the first is in
// flight.
func TestRenewalConvergence_InflightIsLegScopedAcrossTheStaticCheck(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping the all-engines lease convergence e2e in -short mode")
	}
	// An adapter that accepts every call and never resolves it: the only shape
	// that yields a durable .dispatch-without-outcome, which is what the lens's
	// bgInflight fan counts. backgroundCheckFreshness is deliberately NOT
	// activated — freshness is a recorded fact, so with no target to arm the @at
	// the seeded completed check below stays UNLAPSED for the whole test.
	h := newHarness(t, renewalLegOpts(bridge.NewFakeAsyncCheck(1_000_000))...)

	appKey, applicant, unit := h.seedRenewableApplication("S")
	landlord := h.assignLandlord(unit)
	h.approveWithTenancy(appKey, applicant, unit)
	appID := appKey[len("vtx.leaseapp."):]
	applicantID := applicant[len("vtx.identity."):]
	landlordID := landlord[len("vtx.identity."):]

	// Guarantor-less, so verifyGuarantor is never pre-eligible: the only human leg
	// the catalog can offer for an unset-terms renewal is setTerms.
	profile := h.submitOp("SetApplicantProfile", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "unit": unit, "annualIncome": 60000, "employmentStatus": "employed",
	}, &processor.ContextHint{Reads: []string{appKey}})
	require.Equalf(t, processor.ReplyStatusAccepted, profile.Status, "SetApplicantProfile(S): %+v", profile.Error)

	// The static target's own onboarding check goes out and is never answered.
	var withheld string
	require.Eventuallyf(t, func() bool {
		handles := h.withheldBgchecks(applicantID)
		if len(handles) != 1 {
			return false
		}
		withheld = handles[0]
		return rowBool(h.readRow(appID), "inflight_bgcheck")
	}, 60*time.Second, 200*time.Millisecond,
		"the static bgcheck gap's own check must be accepted and left WITHHELD before the renewal chain exists")

	// The renewal opens with that check already in flight. Activating the two
	// renewal lenses only now is what makes the ORDER deterministic: a lens
	// activated at boot would race the bridge's .dispatch write, and a renewal row
	// projected in that window would read not-in-flight over a call that already
	// exists and dispatch before the state under test is reached.
	h.activateActorAggregateLensNow(h.ctx, "leaseExpiry")
	h.activateActorAggregateLensNow(h.ctx, "renewalComplete")

	renewalKey := h.findRenewalKey(appID, 60*time.Second)
	require.NotEmpty(t, renewalKey, "leaseExpiry must open a renewal cycle once its lens is live")
	renewalID := renewalKey[len("vtx.renewal."):]

	// State (1): in flight, nothing completed.
	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalID)
		return row != nil && row["bgcheckValidUntil"] == nil && rowBool(row, "inflight_renewalComplete")
	}, 60*time.Second, 200*time.Millisecond,
		"with a check in flight and none completed, inflight_renewalComplete must read TRUE — the external leg is "+
			"what the chain is waiting on")

	// …and the gap dispatches NOTHING while it does: no landlord task, no second
	// check. The hold spans two full mark-lease expiries so the reconciler sweep's
	// own dispatch leg is exercised, not just lane 1's.
	require.Neverf(t, func() bool {
		return h.taskScopedTo(renewalID) != "" || len(h.withheldBgchecks(applicantID)) != 1 ||
			h.countBgcheckInstances(applicantID) != 1
	}, 2*renewalLegMarkLease, 300*time.Millisecond,
		"a suppressed goal gap dispatches nothing at all — not through lane 1 and not through the sweep")

	// State (2): a completed, unlapsed check seeded BESIDE the still-withheld one.
	// Nothing about the in-flight fact changes here — the same check is still in
	// flight, and the assertion below re-checks that — so the column can only move
	// because of the conjunct.
	seeded := h.seedCompletedBgcheck(applicant)
	require.NotEqual(t, withheld, seeded, "the seeded completed check is a SECOND instance, not the withheld one")

	require.Eventuallyf(t, func() bool {
		row := h.weaverTargetRow("renewalComplete", renewalID)
		return row != nil && row["bgcheckValidUntil"] != nil && !rowBool(row, "inflight_renewalComplete")
	}, 60*time.Second, 200*time.Millisecond,
		"a completed, unlapsed check meets the external leg's effect, so the leg-scoped column must read FALSE even "+
			"though a check is still in flight — the bare in-flight fact would still read true here")

	require.Containsf(t, h.withheldBgchecks(applicantID), withheld,
		"the original check must STILL be withheld at the moment the column reads false — that is what makes this "+
			"vector discriminating rather than a check simply concluding")

	// …and the human leg the false column releases actually dispatches: a task
	// scopedTo the renewal, assigned to the LANDLORD, which over this catalog can
	// only be setTerms (verifyGuarantor is not pre-eligible for a guarantor-less
	// tenant, and signRenewal is the tenant's).
	var taskID string
	require.Eventuallyf(t, func() bool {
		taskID = h.taskScopedTo(renewalID)
		return taskID != ""
	}, 60*time.Second, 250*time.Millisecond,
		"once the column reads false the goal gap must dispatch its human leg — the park the bare column would cause")
	require.Equal(t, landlordID, h.taskAssignedTo(taskID),
		"the released leg is setTerms: the landlord's task, not the tenant's")
}
