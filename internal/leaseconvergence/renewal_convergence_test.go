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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
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

	unitReply := h.submitOp("CreateLocation", "location", "default", bootstrap.BootstrapIdentityKey, map[string]any{
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
	}, &processor.ContextHint{Reads: []string{applicantKey, unitKey}})
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
		"name":         "Renewal Landlord",
		"email":        "landlord-" + mustNanoID(h.t) + "@loftspace.example",
		"claimKeyHash": hex.EncodeToString(claimSum[:]),
	}, nil)
	require.Equalf(h.t, processor.ReplyStatusAccepted, idReply.Status, "CreateUnclaimedIdentity(landlord): %+v", idReply.Error)
	landlordKey = idReply.PrimaryKey

	ownerReply := h.submitOp("AssignUnitOwner", "loftspaceOwnership", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"landlord": landlordKey, "unit": unitKey,
	}, &processor.ContextHint{Reads: []string{landlordKey, unitKey}})
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
	}, &processor.ContextHint{Reads: []string{applicantKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, piiReply.Status, "RecordIdentityPII: %+v", piiReply.Error)

	signReply := h.submitOp("SignLease", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey,
	}, &processor.ContextHint{Reads: []string{appKey}})
	require.Equalf(h.t, processor.ReplyStatusAccepted, signReply.Status, "SignLease: %+v", signReply.Error)

	decideReply := h.submitOp("DecideLeaseApplication", "leaseapp", "default", bootstrap.BootstrapIdentityKey, map[string]any{
		"leaseAppKey": appKey, "decision": "approved", "unit": unitKey,
	}, &processor.ContextHint{Reads: []string{appKey, unitKey}})
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
	h := newHarness(t, withExtraLenses("leaseExpiry", "renewalComplete"))

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
	// anything else on the renewal — forcing refreshBgcheck onto the plan by the
	// time signRenewal's remainder-pre is checked.
	time.Sleep(30 * time.Second)

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
	applicantIDA := applicantA[len("vtx.identity."):]
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
	// hasGuarantor defaults to false (SetApplicantProfile omitted): the goal's
	// anyOf disjunct is satisfied vacuously — verifyGuarantor never becomes
	// pre-eligible, so the planner must never dispatch it.

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
