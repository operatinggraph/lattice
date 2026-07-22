// Renewal-op integration tests through the real install + Processor pipeline
// (design loftspace-lease-renewal-goal-authored-target-design.md §10 test
// strategy — op DDL tests). External test package, mirroring
// lease_signing_test.go's shape and helpers.
package leasesigning_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// approveAndSignLeaseApp brings a fresh application through
// CreateLeaseApplication -> SignLease -> DecideLeaseApplication(approved),
// stamping .tenancy on the first approve. Returns the leaseapp key, its
// applicant, and its unit key.
func approveAndSignLeaseApp(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, applicantSeed string) (appKey, applicantKey, unitKey string) {
	t.Helper()
	applicantKey = seedApplicant(t, ctx, conn, applicantSeed)
	appKey = createApplication(t, ctx, conn, cp, cons, applicantKey)
	unitKey = unitKeyFor(applicantKey)
	signLease(t, ctx, conn, cp, cons, "renewalSign"+applicantSeed[len(applicantSeed)-4:], appKey, "2026-06-26T09:00:00Z")
	decide(t, ctx, conn, cp, cons, "renewalAppr"+applicantSeed[len(applicantSeed)-4:], appKey, "approved", unitKey, "2026-06-26T10:00:00Z", processor.OutcomeAccepted)
	return appKey, applicantKey, unitKey
}

// findRenewalKeyFor scans Core KV for the renewal vertex whose renews link
// points at leaseAppKey, returning its full vtx.renewal.<id> key ("" if none).
func findRenewalKeyFor(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseAppKey string) string {
	t.Helper()
	appID := leaseAppKey[len("vtx.leaseapp."):]
	keys, err := conn.KVListKeys(ctx, testutil.HarnessCoreBucket)
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	for _, k := range keys {
		t1, renewalID, name, t2, dstID, ok := substrate.ParseLinkKey(k)
		if !ok || t1 != "renewal" || name != "renews" || t2 != "leaseapp" || dstID != appID {
			continue
		}
		return "vtx.renewal." + renewalID
	}
	return ""
}

// renewsLinkKey / applicationForLinkKey reconstruct the renewal-cycle
// validation links renewal_scripts.go verifies (VerifyGuarantor/SignRenewal),
// mirroring lease_signing_test.go's guardLinkKey idiom.
func renewsLinkKey(renewalKey, leaseAppKey string) string {
	_, renewalID, _ := substrate.ParseVertexKey(renewalKey)
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	return "lnk.renewal." + renewalID + ".renews.leaseapp." + appID
}
func applicationForLinkKey(leaseAppKey, applicantKey string) string {
	_, appID, _ := substrate.ParseVertexKey(leaseAppKey)
	_, applicantID, _ := substrate.ParseVertexKey(applicantKey)
	return "lnk.leaseapp." + appID + ".applicationFor.identity." + applicantID
}

// setRenewalTerms submits SetRenewalTerms{renewalKey, rentAmount, termMonths}.
// optionalReads carries the (d) .renewalSignature TermsLocked check.
func setRenewalTerms(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, renewalKey string, rentAmount, termMonths float64, want processor.MessageOutcome) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"renewalKey": renewalKey, "rentAmount": rentAmount, "termMonths": termMonths})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetRenewalTerms",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(payload),
		ContextHint:   &processor.ContextHint{Reads: []string{renewalKey}, OptionalReads: []string{renewalKey + ".renewalSignature"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// verifyGuarantor submits VerifyGuarantor{renewalKey, leaseApp, applicant, method?}.
// reads carries the (a) renews/applicationFor validation links; optionalReads
// carries the (d) leaseApp.profile no-guarantor-on-file check.
func verifyGuarantor(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, renewalKey, leaseAppKey, applicantKey, method string, want processor.MessageOutcome) {
	t.Helper()
	payload := map[string]any{"renewalKey": renewalKey, "leaseApp": leaseAppKey, "applicant": applicantKey}
	if method != "" {
		payload["method"] = method
	}
	b, _ := json.Marshal(payload)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "VerifyGuarantor",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(b),
		ContextHint: &processor.ContextHint{
			Reads:         []string{renewalKey, renewsLinkKey(renewalKey, leaseAppKey), applicationForLinkKey(leaseAppKey, applicantKey)},
			OptionalReads: []string{leaseAppKey + ".profile"},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// signRenewal submits SignRenewal{renewalKey, leaseApp, applicant}. reads
// carries the (a) renews/applicationFor validation links + leaseApp.tenancy
// (a renewable application always has one); optionalReads carries the (d)
// .terms / leaseApp.profile / .guarantorVerification ordering-state reads.
func signRenewal(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, renewalKey, leaseAppKey, applicantKey string, want processor.MessageOutcome) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"renewalKey": renewalKey, "leaseApp": leaseAppKey, "applicant": applicantKey})
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SignRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(payload),
		ContextHint: &processor.ContextHint{
			Reads: []string{
				renewalKey,
				renewsLinkKey(renewalKey, leaseAppKey),
				applicationForLinkKey(leaseAppKey, applicantKey),
				leaseAppKey + ".tenancy",
			},
			OptionalReads: []string{renewalKey + ".terms", leaseAppKey + ".profile", renewalKey + ".guarantorVerification"},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// cancelRenewal submits CancelRenewal{renewalKey, reason?}. optionalReads
// carries the (d) .renewalSignature TermsLocked check.
func cancelRenewal(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, renewalKey, reason string, want processor.MessageOutcome) {
	t.Helper()
	payload := map[string]any{"renewalKey": renewalKey}
	if reason != "" {
		payload["reason"] = reason
	}
	b, _ := json.Marshal(payload)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CancelRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(b),
		ContextHint:   &processor.ContextHint{Reads: []string{renewalKey}, OptionalReads: []string{renewalKey + ".renewalSignature"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// TestDecideLeaseApplication_StampsTenancyOnFirstApproveOnly proves the
// create-only .tenancy stamp: first approve derives leaseStart/leaseEnd/
// renewalOpensAt from the unit's .listing; a re-approve never re-derives it
// (idempotent re-submit does not require unit and does not overwrite).
func TestDecideLeaseApplication_StampsTenancyOnFirstApproveOnly(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "tenancystamp")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBtenstamp1ntHJKMNPQ")

	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["leaseStart"].(string); got != "2026-08-01T00:00:00Z" {
		t.Fatalf("tenancy.leaseStart = %q, want 2026-08-01T00:00:00Z", got)
	}
	if got, _ := tdata["leaseEnd"].(string); got != "2027-08-01T00:00:00Z" {
		t.Fatalf("tenancy.leaseEnd = %q, want 2027-08-01T00:00:00Z (leaseStart + 12 months)", got)
	}
	if got, _ := tdata["renewalOpensAt"].(string); got != "2027-06-02T00:00:00Z" {
		t.Fatalf("tenancy.renewalOpensAt = %q, want 2027-06-02T00:00:00Z (leaseEnd - 1440h)", got)
	}

	// Re-approve (idempotent decision re-submit) must NOT re-derive .tenancy —
	// simulate a manual extension (as SignRenewal would do) and prove a
	// re-approve leaves it untouched.
	extended := map[string]any{"class": "tenancy", "isDeleted": false, "vertexKey": appKey, "localName": "tenancy",
		"data": map[string]any{"leaseStart": "2026-08-01T00:00:00Z", "leaseEnd": "2028-08-01T00:00:00Z", "renewalOpensAt": "2028-06-02T00:00:00Z"}}
	eb, _ := json.Marshal(extended)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, appKey+".tenancy", eb); err != nil {
		t.Fatalf("simulate extended tenancy: %v", err)
	}
	decide(t, ctx, conn, cp, cons, "tenstampReap1", appKey, "approved", unitKeyFor("vtx.identity.BBtenstamp1ntHJKMNPQ"), "2026-06-26T11:00:00Z", processor.OutcomeAccepted)
	tdoc = readDoc(t, ctx, conn, appKey+".tenancy")
	tdata, _ = tdoc["data"].(map[string]any)
	if got, _ := tdata["leaseEnd"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("re-approve truncated the extended tenancy: leaseEnd = %q, want preserved 2028-08-01T00:00:00Z", got)
	}
}

// TestOpenRenewal_CreatesVertexAndLink_IdempotentOnDuplicate proves OpenRenewal
// mints the deterministic vtx.renewal.<id> + the renews link, and a duplicate
// fire for the same leaseapp+cycle collides (CreateOnly) rather than minting a
// second cycle vertex.
func TestOpenRenewal_CreatesVertexAndLink_IdempotentOnDuplicate(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "openrenewal")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBopenren1ntHJKMNPQR")

	env1 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("openRen0001"),
		Lane:          processor.LaneDefault,
		OperationType: "OpenRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(`{"leaseApp":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy"}},
	}
	testutil.PublishOp(t, conn, env1)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	renewalKey := findRenewalKeyFor(t, ctx, conn, appKey)
	if renewalKey == "" {
		t.Fatal("OpenRenewal did not mint a discoverable renews link")
	}
	rdoc := readDoc(t, ctx, conn, renewalKey)
	rdata, _ := rdoc["data"].(map[string]any)
	if got, _ := rdata["status"].(string); got != "open" {
		t.Fatalf("renewal status = %q, want open", got)
	}
	if _, ok := rdata["cycleEnd"].(string); !ok {
		t.Fatalf("renewal root missing cycleEnd: %v", rdata)
	}

	// A duplicate fire with a DIFFERENT requestId for the same (leaseapp, cycle)
	// collides on the deterministic vertex create (CreateOnly) and is rejected
	// — the same vertex, never a second cycle.
	env2 := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("openRen0002"),
		Lane:          processor.LaneDefault,
		OperationType: "OpenRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(`{"leaseApp":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy"}},
	}
	testutil.PublishOp(t, conn, env2)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	renewalKey2 := findRenewalKeyFor(t, ctx, conn, appKey)
	if renewalKey2 != renewalKey {
		t.Fatalf("duplicate OpenRenewal must not mint a second cycle vertex: got %q, want %q", renewalKey2, renewalKey)
	}
}

// TestOpenRenewal_NoTenancy_Rejected proves OpenRenewal rejects a leaseapp
// with no .tenancy aspect (an approved-but-never-decided-via-DecideLease
// leaseapp, or a leaseapp approved before this shipped — no backfill).
func TestOpenRenewal_NoTenancy_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "openrennotenancy")

	applicantKey := seedApplicant(t, ctx, conn, "BBnotenancy1tHJKMNPQ")
	appKey := createApplication(t, ctx, conn, cp, cons, applicantKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("openRenNoTen"),
		Lane:          processor.LaneDefault,
		OperationType: "OpenRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(`{"leaseApp":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestSetRenewalTerms_ValidatesAndLocksAfterSignature proves: rentAmount must
// be positive; termMonths must meet the renewal-window floor; terms are
// writable while open+unsigned; and TermsLocked once signed.
func TestSetRenewalTerms_ValidatesAndLocksAfterSignature(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "setterms")

	appKey, applicantKey, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBsetterms1tHJKMNPQR")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	// Non-positive rentAmount rejected.
	setRenewalTerms(t, ctx, conn, cp, cons, "termsBadRent", renewalKey, -100, 12, processor.OutcomeRejected)
	// Too-short termMonths rejected (60-day window floors at 2 months: ceil(1440/730)=2).
	setRenewalTerms(t, ctx, conn, cp, cons, "termsTooShrt", renewalKey, 2500, 1, processor.OutcomeRejected)

	// Valid terms accepted.
	setRenewalTerms(t, ctx, conn, cp, cons, "termsValid01", renewalKey, 2500, 12, processor.OutcomeAccepted)
	tdoc := readDoc(t, ctx, conn, renewalKey+".terms")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["rentAmount"].(float64); got != 2500 {
		t.Fatalf("terms.rentAmount = %v, want 2500", got)
	}

	// Sign the renewal (no guarantor on this applicant), then prove TermsLocked.
	_ = applicantKey
	signRenewal(t, ctx, conn, cp, cons, "termsLockSig", renewalKey, appKey, applicantKey, processor.OutcomeAccepted)
	setRenewalTerms(t, ctx, conn, cp, cons, "termsAfterSig", renewalKey, 2600, 12, processor.OutcomeRejected)
}

// TestSetRenewalTerms_FractionalTermMonths_Rejected proves a fractional
// termMonths (e.g. 2.5) is rejected (InvalidTermMonths) rather than silently
// truncated by SignRenewal's later add_months call — require_number alone
// allows int-or-float, so the whole-number check must be explicit.
func TestSetRenewalTerms_FractionalTermMonths_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "termsfrac")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBtermsfrac1tHJKMNPQ")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	setRenewalTerms(t, ctx, conn, cp, cons, "termsFrac01", renewalKey, 2500, 2.5, processor.OutcomeRejected)
	if keyExists(t, ctx, conn, renewalKey+".terms") {
		t.Fatalf("a rejected fractional-termMonths SetRenewalTerms must not write .terms")
	}

	// A whole-number term at the same floor is accepted.
	setRenewalTerms(t, ctx, conn, cp, cons, "termsFrac02", renewalKey, 2500, 2, processor.OutcomeAccepted)
}

// openRenewalHelper submits OpenRenewal and returns the discovered renewal key.
func openRenewalHelper(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, appKey string) string {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("openRenFor" + appKey[len(appKey)-4:]),
		Lane:          processor.LaneDefault,
		OperationType: "OpenRenewal",
		Actor:         lsActorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "renewal",
		Payload:       json.RawMessage(`{"leaseApp":"` + appKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{appKey, appKey + ".tenancy"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	renewalKey := findRenewalKeyFor(t, ctx, conn, appKey)
	if renewalKey == "" {
		t.Fatalf("OpenRenewal for %s did not mint a discoverable renews link", appKey)
	}
	return renewalKey
}

// TestVerifyGuarantor_NoGuarantorToVerify_Rejected proves VerifyGuarantor
// rejects when the applicant's profile has no guarantor (or no profile at
// all).
func TestVerifyGuarantor_NoGuarantorToVerify_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "verifynog")

	appKey, applicantKey, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBnoguar1ntHJKMNPQRS")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	verifyGuarantor(t, ctx, conn, cp, cons, "verifyNoGuar1", renewalKey, appKey, applicantKey, "", processor.OutcomeRejected)
	if keyExists(t, ctx, conn, renewalKey+".guarantorVerification") {
		t.Fatalf("a rejected VerifyGuarantor must not write .guarantorVerification")
	}
}

// TestVerifyGuarantor_WithGuarantor_WritesVerification proves VerifyGuarantor
// succeeds and writes .guarantorVerification when the applicant's profile
// declares a guarantor, and that the leaseApp/applicant payload fields are
// link-verified (a mismatched applicant is rejected).
func TestVerifyGuarantor_WithGuarantor_WritesVerification(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "verifywithg")

	appKey, applicantKey, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBwithguar1tHJKMNPQR")
	setProfile(t, ctx, conn, cp, cons, "verifyProfSet", appKey, unitKey, map[string]any{
		"annualIncome": 40000, "employmentStatus": "employed", "hasGuarantor": true,
		"guarantorName": "Pat Guarantor", "guarantorAnnualIncome": 120000,
	}, processor.OutcomeAccepted)
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	// A mismatched applicant (not this leaseapp's applicant) is rejected —
	// the link-verification precedent.
	otherApplicant := seedApplicant(t, ctx, conn, "BBwrongapp1cHJKMNPQR")
	verifyGuarantor(t, ctx, conn, cp, cons, "verifyWrongApp", renewalKey, appKey, otherApplicant, "", processor.OutcomeRejected)

	verifyGuarantor(t, ctx, conn, cp, cons, "verifyRight01", renewalKey, appKey, applicantKey, "updated pay stub", processor.OutcomeAccepted)
	gdoc := readDoc(t, ctx, conn, renewalKey+".guarantorVerification")
	gdata, _ := gdoc["data"].(map[string]any)
	if got, _ := gdata["method"].(string); got != "updated pay stub" {
		t.Fatalf("guarantorVerification.method = %q, want %q", got, "updated pay stub")
	}
	if _, ok := gdata["verifiedAt"].(string); !ok {
		t.Fatalf("guarantorVerification missing verifiedAt: %v", gdata)
	}
}

// TestSignRenewal_GatesOnTermsAndGuarantor_ThenExtendsTenancy proves the
// terminal-leg write guard: NotReadyToSign with no .terms; GuarantorNotVerified
// when hasGuarantor=true and unverified; and on success, .renewalSignature +
// status=complete + the leaseapp's .tenancy extension (leaseEnd +=
// termMonths, renewalOpensAt recomputed).
func TestSignRenewal_GatesOnTermsAndGuarantor_ThenExtendsTenancy(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "signrenewal")

	appKey, applicantKey, unitKey := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBsignrenew1HJKMNPQR")
	setProfile(t, ctx, conn, cp, cons, "signrenProfSet", appKey, unitKey, map[string]any{
		"annualIncome": 40000, "employmentStatus": "employed", "hasGuarantor": true,
		"guarantorName": "Pat Guarantor", "guarantorAnnualIncome": 120000,
	}, processor.OutcomeAccepted)
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	// No .terms yet -> NotReadyToSign.
	signRenewal(t, ctx, conn, cp, cons, "signNoTerms01", renewalKey, appKey, applicantKey, processor.OutcomeRejected)

	setRenewalTerms(t, ctx, conn, cp, cons, "signTermsSet", renewalKey, 2500, 12, processor.OutcomeAccepted)

	// Terms set but guarantor (hasGuarantor=true) unverified -> GuarantorNotVerified.
	signRenewal(t, ctx, conn, cp, cons, "signNoGuar01", renewalKey, appKey, applicantKey, processor.OutcomeRejected)
	if keyExists(t, ctx, conn, renewalKey+".renewalSignature") {
		t.Fatalf("a rejected SignRenewal must not write .renewalSignature")
	}

	verifyGuarantor(t, ctx, conn, cp, cons, "signGuarVer1", renewalKey, appKey, applicantKey, "", processor.OutcomeAccepted)

	// Now SignRenewal succeeds.
	signRenewal(t, ctx, conn, cp, cons, "signSuccess1", renewalKey, appKey, applicantKey, processor.OutcomeAccepted)
	sdoc := readDoc(t, ctx, conn, renewalKey+".renewalSignature")
	if sdata, _ := sdoc["data"].(map[string]any); sdata["signedAt"] == nil {
		t.Fatalf("SignRenewal did not write .renewalSignature.signedAt")
	}
	rdoc := readDoc(t, ctx, conn, renewalKey)
	rdata, _ := rdoc["data"].(map[string]any)
	if got, _ := rdata["status"].(string); got != "complete" {
		t.Fatalf("renewal status after sign = %q, want complete", got)
	}

	// .tenancy extended: leaseEnd 2027-08-01 + 12 months = 2028-08-01;
	// renewalOpensAt recomputed as leaseEnd - 1440h.
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["leaseEnd"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("extended tenancy.leaseEnd = %q, want 2028-08-01T00:00:00Z", got)
	}
	if got, _ := tdata["renewalOpensAt"].(string); got != "2028-06-02T00:00:00Z" {
		t.Fatalf("extended tenancy.renewalOpensAt = %q, want 2028-06-02T00:00:00Z", got)
	}
	if got, _ := tdata["leaseStart"].(string); got != "2026-08-01T00:00:00Z" {
		t.Fatalf("extended tenancy.leaseStart = %q, want preserved 2026-08-01T00:00:00Z", got)
	}

	// A second, fresh SignRenewal attempt against the now-complete renewal is
	// rejected (RenewalNotOpen) rather than re-reading and re-extending the
	// ALREADY-extended .tenancy.leaseEnd a second time (the double-extension
	// bug) — assert the exact leaseEnd value is unchanged from the first
	// extension, not just "not double the original".
	signRenewal(t, ctx, conn, cp, cons, "signDoubleExt", renewalKey, appKey, applicantKey, processor.OutcomeRejected)
	tdoc2 := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata2, _ := tdoc2["data"].(map[string]any)
	if got, _ := tdata2["leaseEnd"].(string); got != "2028-08-01T00:00:00Z" {
		t.Fatalf("a rejected re-SignRenewal must leave tenancy.leaseEnd unchanged at the first extension's value; got %q, want 2028-08-01T00:00:00Z", got)
	}
	if got, _ := tdata2["renewalOpensAt"].(string); got != "2028-06-02T00:00:00Z" {
		t.Fatalf("a rejected re-SignRenewal must leave tenancy.renewalOpensAt unchanged; got %q, want 2028-06-02T00:00:00Z", got)
	}
}

// TestSignRenewal_NoGuarantor_SignsWithoutVerification proves the anyOf
// vacuous-satisfaction path: an applicant with hasGuarantor=false never needs
// VerifyGuarantor at all.
func TestSignRenewal_NoGuarantor_SignsWithoutVerification(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "signnoguar")

	appKey, applicantKey, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBsignnoguar1HJKMNPQ")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)
	setRenewalTerms(t, ctx, conn, cp, cons, "signnogTerms", renewalKey, 2500, 12, processor.OutcomeAccepted)

	signRenewal(t, ctx, conn, cp, cons, "signnogSucc1", renewalKey, appKey, applicantKey, processor.OutcomeAccepted)
	rdoc := readDoc(t, ctx, conn, renewalKey)
	rdata, _ := rdoc["data"].(map[string]any)
	if got, _ := rdata["status"].(string); got != "complete" {
		t.Fatalf("renewal status = %q, want complete", got)
	}
}

// TestSignRenewal_RejectsWhenNotOpen_NoDoubleExtension proves SignRenewal's
// status guard in isolation: signing a no-guarantor renewal once succeeds and
// extends .tenancy.leaseEnd; signing the SAME (now-complete) renewal again is
// rejected (RenewalNotOpen) and leaves .tenancy.leaseEnd at the exact value
// the first sign produced — not extended a second time.
func TestSignRenewal_RejectsWhenNotOpen_NoDoubleExtension(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "signnotopen")

	appKey, applicantKey, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBsignnotopen1HJKMNP")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)
	setRenewalTerms(t, ctx, conn, cp, cons, "signnotopTerms", renewalKey, 2500, 12, processor.OutcomeAccepted)

	signRenewal(t, ctx, conn, cp, cons, "signnotopSucc1", renewalKey, appKey, applicantKey, processor.OutcomeAccepted)
	tdoc := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata, _ := tdoc["data"].(map[string]any)
	firstLeaseEnd, _ := tdata["leaseEnd"].(string)
	if firstLeaseEnd != "2028-08-01T00:00:00Z" {
		t.Fatalf("first SignRenewal extended tenancy.leaseEnd = %q, want 2028-08-01T00:00:00Z (2027-08-01 original + 12 months)", firstLeaseEnd)
	}

	// Re-submit against the now-complete renewal: rejected, and leaseEnd is
	// the EXACT first-extension value — not extended a second time (which
	// would land on 2029-08-01).
	signRenewal(t, ctx, conn, cp, cons, "signnotopAgain", renewalKey, appKey, applicantKey, processor.OutcomeRejected)
	tdoc2 := readDoc(t, ctx, conn, appKey+".tenancy")
	tdata2, _ := tdoc2["data"].(map[string]any)
	if got, _ := tdata2["leaseEnd"].(string); got != firstLeaseEnd {
		t.Fatalf("a rejected re-SignRenewal on a non-open renewal must not double-extend; tenancy.leaseEnd = %q, want unchanged %q", got, firstLeaseEnd)
	}
}

// TestSignRenewal_LeaseAppMismatch_Rejected proves the link-verified
// cross-vertex write: a payload leaseApp that is NOT this renewal's renews
// target is rejected, so a tampered payload cannot extend an arbitrary
// leaseapp's .tenancy.
func TestSignRenewal_LeaseAppMismatch_Rejected(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "signmismatch")

	appKey, applicantKey, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBsignmis1tHJKMNPQRS")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)
	setRenewalTerms(t, ctx, conn, cp, cons, "signmisTerms", renewalKey, 2500, 12, processor.OutcomeAccepted)

	otherAppKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBsignmisQth1HJKMNPQ")

	signRenewal(t, ctx, conn, cp, cons, "signMismatch1", renewalKey, otherAppKey, applicantKey, processor.OutcomeRejected)
	// The victim leaseapp's tenancy must be untouched.
	tdoc := readDoc(t, ctx, conn, otherAppKey+".tenancy")
	tdata, _ := tdoc["data"].(map[string]any)
	if got, _ := tdata["leaseEnd"].(string); got != "2027-08-01T00:00:00Z" {
		t.Fatalf("a rejected mismatched SignRenewal must not touch the OTHER leaseapp's tenancy; leaseEnd = %q", got)
	}
}

// TestCancelRenewal_TerminalAfterSignature_OtherwiseCancels proves
// CancelRenewal flips status=cancelled (+ optional reason) while open, and
// rejects once signed (a signed cycle cannot be cancelled).
func TestCancelRenewal_TerminalAfterSignature_OtherwiseCancels(t *testing.T) {
	t.Parallel()
	ctx, conn := setupLeaseEnv(t)
	cp, cons := newLeasePipeline(t, ctx, conn, "cancelrenewal")

	appKey, _, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBcanceLren1HJKMNPQR")
	renewalKey := openRenewalHelper(t, ctx, conn, cp, cons, appKey)

	cancelRenewal(t, ctx, conn, cp, cons, "cancelReason1", renewalKey, "Selling the property.", processor.OutcomeAccepted)
	rdoc := readDoc(t, ctx, conn, renewalKey)
	rdata, _ := rdoc["data"].(map[string]any)
	if got, _ := rdata["status"].(string); got != "cancelled" {
		t.Fatalf("renewal status = %q, want cancelled", got)
	}
	if got, _ := rdata["reason"].(string); got != "Selling the property." {
		t.Fatalf("renewal reason = %q, want preserved", got)
	}

	// A signed cycle cannot be cancelled.
	appKey2, applicantKey2, _ := approveAndSignLeaseApp(t, ctx, conn, cp, cons, "BBcanceLsg2HJKMNPQTW")
	renewalKey2 := openRenewalHelper(t, ctx, conn, cp, cons, appKey2)
	setRenewalTerms(t, ctx, conn, cp, cons, "cancelSigTerms", renewalKey2, 2500, 12, processor.OutcomeAccepted)
	signRenewal(t, ctx, conn, cp, cons, "cancelSigSign", renewalKey2, appKey2, applicantKey2, processor.OutcomeAccepted)
	cancelRenewal(t, ctx, conn, cp, cons, "cancelSigTry1", renewalKey2, "", processor.OutcomeRejected)
	rdoc2 := readDoc(t, ctx, conn, renewalKey2)
	rdata2, _ := rdoc2["data"].(map[string]any)
	if got, _ := rdata2["status"].(string); got != "complete" {
		t.Fatalf("a rejected CancelRenewal on a signed cycle must leave status unchanged; got %q", got)
	}
}
