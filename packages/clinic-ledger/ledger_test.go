// clinic-ledger integration tests through the real install + Processor
// pipeline. External test package (clinicledger_test) so they exercise the
// public Lattice surface: seed the kernel, install rbac + identity + hygiene +
// clinic-domain + clinic-ledger through the Processor, then submit the ops
// and assert the committed Core-KV shape + the emitted events.
package clinicledger_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	clinicdomain "github.com/operatinggraph/lattice/packages/clinic-domain"
	clinicledger "github.com/operatinggraph/lattice/packages/clinic-ledger"
)

const (
	ledgerActorID  = "CLLEDGERACTRHJKMNPQR"
	ledgerActorKey = "vtx.identity." + ledgerActorID
	ledgerCapKey   = "cap.identity." + ledgerActorID
)

func ledgerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ledgerCapKey,
		Actor:                  ledgerActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ledgerActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreatePatient", Scope: "any"},
			{OperationType: "CreateProvider", Scope: "any"},
			{OperationType: "CreateAppointment", Scope: "any"},
			{OperationType: "SetAppointmentStatus", Scope: "any"},
			{OperationType: "ClinicCreateAccount", Scope: "any"},
			{OperationType: "ClinicDebitAccount", Scope: "any"},
			{OperationType: "ClinicCreditAccount", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupLedgerEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	// ledConsumerRoleID stands in for identity-domain's real `consumer` role
	// NanoID: clinic-domain's CreateAppointment scope=self grant (GrantsTo:
	// "consumer") needs a role id registered directly, since these tests don't
	// install identity-domain (the lease-signing lsConsumerRoleID idiom).
	const ledConsumerRoleID = "LEDConsumerRoZeHJKMN"
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": ledConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
	if _, err := inst.Install(ctx, clinicdomain.Package); err != nil {
		t.Fatalf("install clinic-domain: %v", err)
	}
	if _, err := inst.Install(ctx, clinicledger.Package); err != nil {
		t.Fatalf("install clinic-ledger: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, ledgerCapDoc())
	// CreateAppointment's workplace-confinement guard reads the holdsRole LINK to
	// decide whether its caller is root (actor_holds_operator), not the cap doc's
	// Roles — so the operator actor needs the link, exactly as clinic-domain's own
	// fixture seeds it. In production the cap doc's operator role is projected FROM
	// this link, so seeding it makes the fixture realistic rather than adding anything.
	testutil.SeedHoldsRole(t, ctx, conn, ledgerActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

func newLedgerPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "cll-" + durable,
	})
}

func nanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
}

func readDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		t.Fatalf("KVGet %s: %v", key, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		t.Fatalf("unmarshal %s: %v", key, err)
	}
	return doc
}

func keyExists(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) bool {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, key)
	if err != nil {
		return false
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return false
	}
	if del, _ := doc["isDeleted"].(bool); del {
		return false
	}
	return true
}

// createPatient submits CreatePatient and returns the patient's full key.
func createPatient(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, fullName string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreatePatient",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "patient",
		Payload:       json.RawMessage(`{"fullName":"` + fullName + `"}`),
		ContextHint: &processor.ContextHint{
			Enumerations: testutil.DeclaredEnumerations("CreatePatient", ledgerActorKey, clinicdomain.OpMetas())},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.patient." + nanoIDFromRequestID(reqID)
}

// createAccount submits ClinicCreateAccount{patientKey} and returns the account key
// — the account's own independently-minted NanoID, matching the deterministic
// nanoid.new() seed the test harness uses for the transaction DDL (never
// derived from the patient's own id).
func createAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, patientKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.clinicaccount." + nanoIDFromRequestID(reqID)
}

// TestClinicCreateAccount_MintsAccountHeldForPatient (test 1). ClinicCreateAccount mints
// vtx.clinicaccount.<freshId> (root {} — D5, an id independent of the
// patient's own) + the patient's .ledgerAccount guard aspect + the heldFor
// link; a second call for the same patient that declares the guard aspect in
// reads conflicts on it (AccountAlreadyExists).
func TestClinicCreateAccount_MintsAccountHeldForPatient(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "create")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0000000000001", "Alice Rivera")
	patientID := patientKey[len("vtx.patient."):]
	guardKey := patientKey + ".ledgerAccount"

	if keyExists(t, ctx, conn, guardKey) {
		t.Fatalf("guard aspect must not exist before ClinicCreateAccount")
	}

	acctKey := createAccount(t, ctx, conn, cp, cons, "createacct0000001", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]
	if acctID == patientID {
		t.Fatalf("account id must NOT equal the patient's own id (independently minted), got %q for both", acctID)
	}

	acctDoc := readDoc(t, ctx, conn, acctKey)
	if d, _ := acctDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("account root data must stay minimal ({}) after create, got %v", d)
	}

	guardDoc := readDoc(t, ctx, conn, guardKey)
	guardData, _ := guardDoc["data"].(map[string]any)
	if got, _ := guardData["accountKey"].(string); got != acctKey {
		t.Fatalf("guard aspect accountKey = %q, want %q", got, acctKey)
	}

	heldForLnk := "lnk.clinicaccount." + acctID + ".heldFor.patient." + patientID
	if !keyExists(t, ctx, conn, heldForLnk) {
		t.Fatalf("heldFor link must exist: %s", heldForLnk)
	}

	// A second ClinicCreateAccount for the SAME patient, declaring the now-existing
	// guard aspect in reads, conflicts on it (AccountAlreadyExists — the
	// create-only write is the guard).
	dup := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacct0000002"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:05:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey, guardKey}},
	}
	testutil.PublishOp(t, conn, dup)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestClinicCreateAccount_UnknownPatient rejects an account opened against a
// non-existent patient (no-orphan invariant).
func TestClinicCreateAccount_UnknownPatient(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownpatient")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacctunknown01"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"vtx.patient.CLABSENTPATNTHJKMNPQ"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.patient.CLABSENTPATNTHJKMNPQ"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitCreditAccount_PostEntries (test 2). ClinicDebitAccount/ClinicCreditAccount
// each mint a fresh transaction vertex (root {} — D5) + a .entry aspect + the
// postedTo link to the account; the account root is never touched (append-only
// ledger — the account's own .balance aspect, not its root, is what moves).
func TestDebitCreditAccount_PostEntries(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "postentries")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatpost00000000001", "Bob Nguyen")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctpost00001", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]

	debitReqID := testutil.GenReqID("debitcopay0000000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Office visit copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	debitTxKey := "vtx.clinictransaction." + nanoIDFromRequestID(debitReqID)
	entryDoc := readDoc(t, ctx, conn, debitTxKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "debit" {
		t.Fatalf("entry.type = %q, want debit", got)
	}
	if got, _ := entryData["amountCents"].(float64); got != 2500 {
		t.Fatalf("entry.amountCents = %v, want 2500", got)
	}
	if got, _ := entryData["memo"].(string); got != "Office visit copay" {
		t.Fatalf("entry.memo = %q, want %q", got, "Office visit copay")
	}
	if got, _ := entryData["billedTo"].(string); got != "self" {
		t.Fatalf("entry.billedTo = %q, want default \"self\" when omitted", got)
	}
	if _, present := entryData["expectedReimbursementCents"]; present {
		t.Fatalf("entry.expectedReimbursementCents must be absent for a self-pay debit, got %v", entryData["expectedReimbursementCents"])
	}

	txDoc := readDoc(t, ctx, conn, debitTxKey)
	if d, _ := txDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("transaction root data must stay minimal ({}) after post, got %v", d)
	}

	postedToLnk := "lnk.clinictransaction." + nanoIDFromRequestID(debitReqID) + ".postedTo.clinicaccount." + acctID
	if !keyExists(t, ctx, conn, postedToLnk) {
		t.Fatalf("postedTo link must exist: %s", postedToLnk)
	}

	// The account root is never mutated by a debit — append-only ledger.
	acctDoc := readDoc(t, ctx, conn, acctKey)
	if d, _ := acctDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("account root data must stay minimal ({}) after a debit — the ledger is append-only, got %v", d)
	}

	// CreditAccount — a payment received.
	creditReqID := testutil.GenReqID("creditpay0000000001")
	creditEnv := &processor.OperationEnvelope{
		RequestID:     creditReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-05T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Insurance payment - claim #4471"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	creditTxKey := "vtx.clinictransaction." + nanoIDFromRequestID(creditReqID)
	creditEntryDoc := readDoc(t, ctx, conn, creditTxKey+".entry")
	creditEntryData, _ := creditEntryDoc["data"].(map[string]any)
	if got, _ := creditEntryData["type"].(string); got != "credit" {
		t.Fatalf("entry.type = %q, want credit", got)
	}
	if _, present := creditEntryData["billedTo"]; present {
		t.Fatalf("entry.billedTo must be absent on a credit (payment) entry, got %v", creditEntryData["billedTo"])
	}
}

// TestDebitAccount_InsuranceBilling (test 2b). A debit billed to insurance
// stores billedTo + expectedReimbursementCents on the .entry aspect; a
// same-account debit with no billedTo defaults to self, proving the two
// coexist without cross-contamination.
func TestDebitAccount_InsuranceBilling(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "insurancebilling")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatins00000000001", "Dana Osei")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctins00001", patientKey)

	debitReqID := testutil.GenReqID("debitinsurance000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":15000,"memo":"Specialist visit",` +
			`"billedTo":"insurance","expectedReimbursementCents":12000}`),
		ContextHint: &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	debitTxKey := "vtx.clinictransaction." + nanoIDFromRequestID(debitReqID)
	entryDoc := readDoc(t, ctx, conn, debitTxKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["billedTo"].(string); got != "insurance" {
		t.Fatalf("entry.billedTo = %q, want insurance", got)
	}
	if got, _ := entryData["expectedReimbursementCents"].(float64); got != 12000 {
		t.Fatalf("entry.expectedReimbursementCents = %v, want 12000", got)
	}
}

// TestDebitAccount_PayerDimensionValidation (test 2c). Rejects every
// malformed shape of the billedTo/expectedReimbursementCents dimension: an
// unrecognized billedTo value, insurance billing with no reimbursement
// figure, a self-pay debit that supplies one anyway, a reimbursement that
// exceeds the charge, and either field on a credit (a payment has
// nothing to bill).
func TestDebitAccount_PayerDimensionValidation(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payervalidation")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatpv0000000000001", "Eli Farrow")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctpv000001", patientKey)

	cases := []struct {
		name    string
		opType  string
		payload string
	}{
		{"unrecognized billedTo value", "ClinicDebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"medicare"}`},
		{"insurance with no reimbursement figure", "ClinicDebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"insurance"}`},
		{"self-pay debit supplying a reimbursement anyway", "ClinicDebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"self","expectedReimbursementCents":500}`},
		{"reimbursement exceeds the charge", "ClinicDebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"insurance","expectedReimbursementCents":1500}`},
		{"non-positive reimbursement", "ClinicDebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"insurance","expectedReimbursementCents":0}`},
		{"billedTo on a credit (payment)", "ClinicCreditAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"self"}`},
		{"expectedReimbursementCents on a credit (payment)", "ClinicCreditAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"expectedReimbursementCents":500}`},
	}
	for i, c := range cases {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("payerval" + string(rune('0'+i)) + "00000001"),
			Lane:          processor.LaneDefault,
			OperationType: c.opType,
			Actor:         ledgerActorKey,
			SubmittedAt:   "2026-07-01T13:00:00Z",
			Class:         "clinictransaction",
			Payload:       json.RawMessage(c.payload),
			ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	}
}

// TestCreditAccount_Waiver (front-desk/operator, scope=any) proves a credit
// carrying reason:"waiver" is accepted, stores reason on the .entry aspect
// (instead of the default "payment"), and reduces the derived balance
// exactly like a plain payment would — a waiver forgives debt rather than
// recording cash collected, but the ledger's arithmetic treats the two
// identically; only the projected reason tells them apart.
func TestCreditAccount_Waiver(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditwaiver")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatwaiv0000000001", "Farah Nassar")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctwaiv00001", patientKey)

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("waiverdebit000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"No-show fee"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	waiverReqID := testutil.GenReqID("waivercredit00000001")
	waiverEnv := &processor.OperationEnvelope{
		RequestID:     waiverReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T14:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Waived — patient hardship","reason":"waiver"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, waiverEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	waiverTxKey := "vtx.clinictransaction." + nanoIDFromRequestID(waiverReqID)
	entryDoc := readDoc(t, ctx, conn, waiverTxKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "credit" {
		t.Fatalf("entry.type = %q, want credit", got)
	}
	if got, _ := entryData["reason"].(string); got != "waiver" {
		t.Fatalf("entry.reason = %q, want waiver", got)
	}
}

// TestCreditAccount_ReasonValidation proves reason is a bounded credit-only
// dimension, mirroring TestDebitAccount_PayerDimensionValidation's billedTo
// coverage: an unrecognized value is rejected, a plain payment omitting
// reason still defaults it (proven separately by TestDebitCreditAccount_
// PostEntries' absence check pre-dating this field — here we only need the
// negative cases), and reason on a debit is rejected (a charge has nothing
// to waive).
func TestCreditAccount_ReasonValidation(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "reasonvalidation")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatrv00000000001", "Gio Petrova")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctrv000001", patientKey)

	cases := []struct {
		name    string
		opType  string
		payload string
	}{
		{"unrecognized reason value", "ClinicCreditAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"reason":"refund"}`},
		{"reason on a debit (charge)", "ClinicDebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"reason":"waiver"}`},
	}
	for i, c := range cases {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID("reasonval" + string(rune('0'+i)) + "00000001"),
			Lane:          processor.LaneDefault,
			OperationType: c.opType,
			Actor:         ledgerActorKey,
			SubmittedAt:   "2026-07-01T13:00:00Z",
			Class:         "clinictransaction",
			Payload:       json.RawMessage(c.payload),
			ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
	}
}

// TestDebitAccount_UnknownAccount rejects a debit against a non-existent
// account.
func TestDebitAccount_UnknownAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownacct")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitunknownacct001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"vtx.clinicaccount.CLABSENTACCTHJKMNPQR","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.clinicaccount.CLABSENTACCTHJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_NonPositiveAmountRejected rejects amountCents <= 0
// (InvalidArgument).
func TestDebitAccount_NonPositiveAmountRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "badamount")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatbad0000000001", "Cara Diallo")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctbad000001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitbadamount00001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_RejectsFractionalCents rejects an amountCents that is not
// a whole number — every schema in this package (the DDL, the .entry field
// description, the .balance aspect) says integer cents, and a fractional
// amount would post an entry the clinicLedgerHistory balance sums into a
// non-representable total.
func TestDebitAccount_RejectsFractionalCents(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fracamount")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatfrac0000000001", "Iris Kwan")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctfrac00001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitfracamount0001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":10.5}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, env, "required whole cents")
}

// TestDebitAccount_RejectsFractionalReimbursement rejects an
// expectedReimbursementCents that is not a whole number, for the same reason
// as TestDebitAccount_RejectsFractionalCents above.
func TestDebitAccount_RejectsFractionalReimbursement(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fracreimb")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatfracr000000001", "Jonas Ward")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctfracr0001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitfracreimb00001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey +
			`","amountCents":1000,"billedTo":"insurance","expectedReimbursementCents":12.5}`),
		ContextHint: &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, env, "required whole cents")
}

// createProvider submits CreateProvider and returns the provider's full key.
func createProvider(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, fullName, specialty string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateProvider",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "provider",
		Payload:       json.RawMessage(`{"fullName":"` + fullName + `","specialty":"` + specialty + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.provider." + nanoIDFromRequestID(reqID)
}

// createAppointment submits CreateAppointment and returns the appointment's
// full key.
func createAppointment(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, patientKey, providerKey, startsAt, endsAt string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAppointment",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-25T12:00:00Z",
		Class:         "appointment",
		Payload: json.RawMessage(`{"patient":"` + patientKey + `","provider":"` + providerKey +
			`","startsAt":"` + startsAt + `","endsAt":"` + endsAt + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{patientKey, providerKey},
			Enumerations: testutil.DeclaredEnumerations("CreateAppointment", ledgerActorKey, clinicdomain.OpMetas())},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.appointment." + nanoIDFromRequestID(reqID)
}

// TestDebitAccount_AppointmentRefWritesSettlesLink (test 3). A DebitAccount
// carrying appointmentRef writes the settles audit link (transaction→
// appointment) the clinicNoShowSettlement lens reads; a plain DebitAccount
// with no appointmentRef writes no such link (byte-for-byte the existing
// self-pay shape).
func TestDebitAccount_AppointmentRefWritesSettlesLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "apptref")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatapref00000001", "Farah Al-Amin")
	providerKey := createProvider(t, ctx, conn, cp, cons, "mkprovapref0000001", "Dr. Kim", "family-medicine")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctapref0001", patientKey)
	apptKey := createAppointment(t, ctx, conn, cp, cons, "mkapptapref0000001", patientKey, providerKey, "2026-06-25T15:00:00Z", "2026-06-25T15:30:00Z")
	apptID := apptKey[len("vtx.appointment."):]

	debitReqID := testutil.GenReqID("debitapref0000000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"appointmentRef":"` + apptKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, apptKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	settlesLnk := "lnk.clinictransaction." + nanoIDFromRequestID(debitReqID) + ".settles.appointment." + apptID
	if !keyExists(t, ctx, conn, settlesLnk) {
		t.Fatalf("settles link must exist: %s", settlesLnk)
	}

	// A plain DebitAccount (no appointmentRef) writes no settles link at all.
	plainReqID := testutil.GenReqID("debitapref0000000002")
	plainEnv := &processor.OperationEnvelope{
		RequestID:     plainReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:05:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, plainEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	plainSettlesLnk := "lnk.clinictransaction." + nanoIDFromRequestID(plainReqID) + ".settles.appointment." + apptID
	if keyExists(t, ctx, conn, plainSettlesLnk) {
		t.Fatalf("a plain DebitAccount with no appointmentRef must write no settles link, found %s", plainSettlesLnk)
	}
}

// TestDebitAccount_UnknownAppointmentRefRejected rejects a DebitAccount whose
// appointmentRef names a non-existent appointment (UnknownAppointment).
func TestDebitAccount_UnknownAppointmentRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownapptref")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatuar00000000001", "Grant Okafor")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctuar000001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debituar0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"appointmentRef":"vtx.appointment.CLABSENTAPPTHJKMNPQR"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, "vtx.appointment.CLABSENTAPPTHJKMNPQR", acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreditAccount_ReversesRefWritesReversesLink (mirrors
// TestDebitAccount_AppointmentRefWritesSettlesLink). A ClinicCreditAccount
// carrying reversesRef writes the reverses audit link (credit tx -> the
// reversed debit tx) clinicNoShowSettlement's missing_reversal gap reads; a
// plain ClinicCreditAccount with no reversesRef writes no such link.
func TestCreditAccount_ReversesRefWritesReversesLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "reversesref")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatrevref00000001", "Priya Nandakumar")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctrevref001", patientKey)

	debitReqID := testutil.GenReqID("debitrevref00000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	debitTxKey := "vtx.clinictransaction." + nanoIDFromRequestID(debitReqID)

	creditReqID := testutil.GenReqID("creditrevref0000001")
	creditEnv := &processor.OperationEnvelope{
		RequestID:     creditReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:05:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"reason":"waiver","reversesRef":"` + debitTxKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, debitTxKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reversesLnk := "lnk.clinictransaction." + nanoIDFromRequestID(creditReqID) + ".reverses.clinictransaction." + nanoIDFromRequestID(debitReqID)
	if !keyExists(t, ctx, conn, reversesLnk) {
		t.Fatalf("reverses link must exist: %s", reversesLnk)
	}

	// A plain ClinicCreditAccount (no reversesRef) writes no reverses link.
	plainReqID := testutil.GenReqID("creditrevref0000002")
	plainEnv := &processor.OperationEnvelope{
		RequestID:     plainReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:10:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, plainEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	plainReversesLnk := "lnk.clinictransaction." + nanoIDFromRequestID(plainReqID) + ".reverses.clinictransaction." + nanoIDFromRequestID(debitReqID)
	if keyExists(t, ctx, conn, plainReversesLnk) {
		t.Fatalf("a plain ClinicCreditAccount with no reversesRef must write no reverses link, found %s", plainReversesLnk)
	}
}

// TestCreditAccount_UnknownReversesRefRejected rejects a ClinicCreditAccount
// whose reversesRef names a non-existent transaction (UnknownTransaction).
func TestCreditAccount_UnknownReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownrevref")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpaturr00000000001", "Owen Delacroix")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctuur000001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("credituur0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"reversesRef":"vtx.clinictransaction.CLABSENTTXNHJKMNPQRS"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, "vtx.clinictransaction.CLABSENTTXNHJKMNPQRS", acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_ReversesRefRejected rejects reversesRef on a
// ClinicDebitAccount — reversal is a credit-only concept, mirroring
// billedTo/expectedReimbursementCents being debit-only.
func TestDebitAccount_ReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "debitrevrefrej")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatdrr00000000001", "Selin Aydemir")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctdrr000001", patientKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitdrr0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"reversesRef":"vtx.clinictransaction.CLABSENTTXNHJKMNPQRS"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// ledgerSelfConsumerID/Key/Cap + ledgerSelfConsumerCapDoc grant the platform
// permission ClinicCreditAccount's scope=self branch checks — mirrors
// loftspace-ledger's ledgerSelfConsumerCapDoc.
const (
	ledgerSelfConsumerID  = "CLLEDGERSELFCQNSHJKM"
	ledgerSelfConsumerKey = "vtx.identity." + ledgerSelfConsumerID
	ledgerSelfConsumerCap = "cap.identity." + ledgerSelfConsumerID
)

func ledgerSelfConsumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ledgerSelfConsumerCap,
		Actor:                  ledgerSelfConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ledgerSelfConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "ClinicCreditAccount", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

func seedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.identity." + id
	seedVertex(t, ctx, conn, key, "identity", map[string]any{})
	return key
}

func seedVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, key, class string, data map[string]any) {
	t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	doc := map[string]any{"class": class, "isDeleted": false, "data": data}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed vertex %s: %v", key, err)
	}
}

func seedLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key, source, target, class, localName string) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"sourceVertex": source, "targetVertex": target,
		"localName": localName, "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed link %s: %v", key, err)
	}
}

// seedPatientWithIdentity seeds a live patient vertex plus its identifiedBy
// link to identityID — the ownership chain ClinicCreditAccount's self-scope
// branch walks (via the account's own heldFor link) to the patient, then
// this link to the caller's identity.
func seedPatientWithIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, patientID, identityID string) string {
	t.Helper()
	key := "vtx.patient." + patientID
	seedVertex(t, ctx, conn, key, "patient", map[string]any{})
	lnk := "lnk.patient." + patientID + ".identifiedBy.identity." + identityID
	seedLink(t, ctx, conn, lnk, key, "vtx.identity."+identityID, "identifiedBy", "identifiedBy")
	return key
}

// TestCreditAccount_ConsumerSelfScope_Allowed proves a real patient can
// credit (pay down) THEIR OWN account: the account's heldFor patient
// resolves (via identifiedBy) to the caller's own authContext.target
// identity.
func TestCreditAccount_ConsumerSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfok")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEDGERSELFPATNTHJK", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditselfacctsetup1", patientKey)

	// A front-desk-recorded charge establishes the $25 owed the self-credit
	// below pays down — the balance cap (scripts.go) has nothing to verify
	// against on a freshly-opened, never-charged account.
	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditselfdebit000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Office visit copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID := testutil.GenReqID("creditselfpay0000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-service ClinicCreditAccount outcome = %v, want Accepted", outcome)
	}
}

// TestCreditAccount_ConsumerSelfScope_RejectedOverBalance proves the
// self-credit amount is bounded by what the account actually owes — a
// patient cannot self-forgive debt by naming an amount larger than any
// front-desk-recorded charge (scripts.go recomputes the balance from the
// account's own postedTo history; nothing on this platform verifies a
// self-submitted payment actually happened, so the amount itself is the
// attack surface, not just which account it targets).
func TestCreditAccount_ConsumerSelfScope_RejectedOverBalance(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfoverbal")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEDGERSELFQVRPATNT", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditoverbalsetup01", patientKey)

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditoverbaldebit01"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Office visit copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// $25 owed; self-credit claims $25,000 — must be rejected even though
	// ownership checks out.
	reqID := testutil.GenReqID("creditoverbalpay0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("over-balance self-credit outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestAccountBalance_AccumulatesAcrossEntries proves the .balance aspect
// (scripts.go's O(1) replacement for the old full-postedTo-history replay)
// tracks the SAME net total a hand-summed history would, across a sequence
// of debits and a staff credit — not just a single entry. Two debits
// ($50 + $30 = $80 owed) then a staff payment ($20) should leave $60 owed;
// the .balance aspect is read directly (proving the maintained value, not
// just an outcome that could pass with a wrong-but-net-zero bug), and the
// self-credit boundary ($60 accepted, $61 rejected) proves post_entry's
// authorization check reads that same maintained value.
func TestAccountBalance_AccumulatesAcrossEntries(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "balanceaccum")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEDGERBALACCMPATNT", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "balaccumacctsetup001", patientKey)

	debit1Env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("balaccumdebit0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-09T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":5000,"memo":"Visit one"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debit1Env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	debit2Env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("balaccumdebit0000002"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-09T08:10:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":3000,"memo":"Visit two"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debit2Env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	staffCreditEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("balaccumcredit000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-09T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2000,"memo":"Partial payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, staffCreditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// $50 + $30 - $20 = $60 owed. Read the aspect directly.
	balanceDoc := readDoc(t, ctx, conn, acctKey+".balance")
	balanceData, _ := balanceDoc["data"].(map[string]any)
	if got, _ := balanceData["balanceCents"].(float64); got != 6000 {
		t.Fatalf(".balance aspect balanceCents = %v, want 6000 (5000+3000-2000)", got)
	}

	// Self-credit for exactly $60 succeeds.
	exactReqID := testutil.GenReqID("balaccumselfpay00001")
	exactEnv := &processor.OperationEnvelope{
		RequestID:     exactReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-09T09:05:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":6000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, exactEnv)
	if outcome := testutil.DriveOne(t, ctx, cp, cons, ""); outcome != processor.OutcomeAccepted {
		t.Fatalf("self-credit for the exact $60 owed outcome = %v, want Accepted", outcome)
	}

	// Balance is now zero — a further self-credit of even $1 is rejected.
	overReqID := testutil.GenReqID("balaccumselfpay00002")
	overEnv := &processor.OperationEnvelope{
		RequestID:     overReqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-09T09:10:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":100}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, overEnv)
	if outcome := testutil.DriveOne(t, ctx, cp, cons, ""); outcome != processor.OutcomeRejected {
		t.Fatalf("self-credit against a zeroed balance outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// seedLegacyTransaction writes a clinictransaction straight to Core KV —
// vertex, .entry aspect, postedTo link — mirroring exactly what
// ClinicDebitAccount/ClinicCreditAccount's post_entry mints, EXCEPT it never
// touches the account's .balance aspect. This is what a real transaction
// posted before the .balance DDL revision shipped looks like today: a
// vertex_alive account, a live postedTo history, no .balance aspect anywhere.
func seedLegacyTransaction(t *testing.T, ctx context.Context, conn *substrate.Conn, txID, acctKey, acctID, entryType string, amountCents int) {
	t.Helper()
	txKey := "vtx.clinictransaction." + txID
	putDoc(t, ctx, conn, txKey, map[string]any{"class": "clinictransaction", "isDeleted": false, "data": map[string]any{}})
	putDoc(t, ctx, conn, txKey+".entry", map[string]any{
		"class": "transactionEntry", "isDeleted": false, "vertexKey": txKey, "localName": "entry",
		"data": map[string]any{"type": entryType, "amountCents": amountCents, "postedAt": "2026-05-01T12:00:00Z"},
	})
	postedToLnk := "lnk.clinictransaction." + txID + ".postedTo.clinicaccount." + acctID
	putDoc(t, ctx, conn, postedToLnk, map[string]any{
		"class": "postedTo", "isDeleted": false, "sourceVertex": txKey, "targetVertex": acctKey, "localName": "postedTo", "data": map[string]any{},
	})
}

func putDoc(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, doc map[string]any) {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal %s: %v", key, err)
	}
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// TestCreditAccount_ConsumerSelfScope_RejectedNoBalance proves a self-credit
// against a freshly-opened account (never charged, nothing owed) is rejected
// — there is nothing to pay down.
func TestCreditAccount_ConsumerSelfScope_RejectedNoBalance(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfnobal")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEDGERSELFNQBAPATN", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditnobalsetup0001", patientKey)

	reqID := testutil.GenReqID("creditnobalpay000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":100}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-credit on a never-charged account outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestCreditAccount_ConsumerSelfScope_RejectedForOthersAccount proves a
// consumer satisfying step 3 (authContext.target == actor) but naming an
// account whose patient is NOT their own is rejected — self-service never
// lets one patient pay down another's balance.
func TestCreditAccount_ConsumerSelfScope_RejectedForOthersAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfother")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	otherPatientID := "CLLEDGERQTHERPATNTHJ"
	seedIdentity(t, ctx, conn, otherPatientID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEDGERQTHERPATNTQR", otherPatientID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditotheracctsetup", patientKey)

	reqID := testutil.GenReqID("creditselfpay0000002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:05:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-service ClinicCreditAccount on another's account outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestCreditAccount_ConsumerSelfScope_DebitStaysStaffOnly proves the
// self-scope grant does not leak to ClinicDebitAccount: even a caller who
// legitimately owns the patient record cannot self-charge it (permissions.go
// grants no scope=self ClinicDebitAccount, and post_entry's own branch fails
// closed for a debit regardless).
func TestCreditAccount_ConsumerSelfScope_DebitStaysStaffOnly(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "debitselfdenied")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEDGERSELFDEBTPATN", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "debitselfacctsetup01", patientKey)

	reqID := testutil.GenReqID("debitselfpay00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:10:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":500}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-scoped ClinicDebitAccount outcome = %v, want Rejected (no matching grant)", outcome)
	}
}

// TestCreditAccount_ConsumerSelfScope_RejectedWaiver proves a patient cannot
// forgive their own debt: a self-scoped credit (ownership + amount both
// otherwise valid) carrying reason:"waiver" is rejected — post_entry's own
// authContextTarget branch fails closed on it before the ownership/amount
// proofs even run, since a patient may only pay down a balance, never waive
// it (that decision is the operator/front-desk's, scope=any).
func TestCreditAccount_ConsumerSelfScope_RejectedWaiver(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfwaiver")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEDGERSELFWAVRPATN", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditselfwaivsetup1", patientKey)

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditselfwaivdebit1"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Office visit copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID := testutil.GenReqID("creditselfwaivpay001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"reason":"waiver"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, acctKey + ".balance"}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-scoped waiver outcome = %v, want Rejected (AuthDenied) — a patient may pay down a balance, never waive it", outcome)
	}
}

// --- The account's maintained .balance running total ----------------------

// balanceCents reads back the account's own .balance aspect — the O(1) running
// total post_entry keeps in lockstep with every posted entry, and the quantity
// the self-pay cap is measured against.
func balanceCents(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) float64 {
	t.Helper()
	doc := readDoc(t, ctx, conn, acctKey+".balance")
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.balance carries no data", acctKey)
	}
	got, ok := data["balanceCents"].(float64)
	if !ok {
		t.Fatalf("%s.balance carries no balanceCents, got %v", acctKey, data)
	}
	return got
}

// staffDebitHint is the contextHint a ClinicDebitAccount submission carries,
// mirroring opmetas.go's dispatch: the account plus its absence-tolerant
// .balance aspect. No postedTo walk — a charge never backfills a legacy
// account's balance, only a self-pay does.
//
// .balance is declared, not incidental: the declaration is what auto-conditions
// the update post_entry emits for it on the revision it hydrated at
// (Contract #3 §3.2). The script's own derive_reads(op) declares the same key
// whatever a submitter sends, so these fixtures mirror the dispatchers rather
// than supply the guarantee.
func staffDebitHint(acctKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{acctKey},
		OptionalReads: []string{acctKey + ".balance"},
	}
}

// selfPayHint is the contextHint a patient's scope=self ClinicCreditAccount
// submission carries, exactly as opmetas.go declares it — the account, its
// absence-tolerant .balance, and the bounded postedTo walk that backfills
// .balance on a legacy account's first self-pay.
func selfPayHint(acctKey string) *processor.ContextHint {
	h := staffDebitHint(acctKey)
	h.Enumerations = []processor.EnumerationHint{
		{Hub: acctKey, Relation: "postedTo", Direction: "in"},
	}
	return h
}

// staffDebitEnv builds one operator-voice ClinicDebitAccount of amountCents
// against acctKey.
func staffDebitEnv(label, acctKey string, amountCents int) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-20T08:00:00Z",
		Class:         "clinictransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
			strconv.Itoa(amountCents) + `,"memo":"Office visit copay"}`),
		ContextHint: staffDebitHint(acctKey),
	}
}

// staffDebit submits one ClinicDebitAccount and asserts the outcome.
func staffDebit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey string, amountCents int, want processor.MessageOutcome) {
	t.Helper()
	testutil.PublishOp(t, conn, staffDebitEnv(label, acctKey, amountCents))
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// selfPayEnv builds one scope=self (patient) ClinicCreditAccount of amountCents
// against acctKey — the one leg the amount cap binds.
func selfPayEnv(label, acctKey string, amountCents int) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-20T09:00:00Z",
		Class:         "clinictransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
			strconv.Itoa(amountCents) + `}`),
		ContextHint: selfPayHint(acctKey),
		AuthContext: &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
}

// selfPay submits one self-scoped ClinicCreditAccount and asserts the outcome.
func selfPay(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey string, amountCents int, want processor.MessageOutcome) {
	t.Helper()
	testutil.PublishOp(t, conn, selfPayEnv(label, acctKey, amountCents))
	testutil.DriveOne(t, ctx, cp, cons, want)
}

// assertRejectedBecause drives env and asserts it was rejected FOR THE STATED
// REASON. MessageOutcome collapses every refusal into "rejected", so an
// outcome-only assertion on a payment cap passes just as well against a guard
// that denied the actor, the account or the payload — which is the whole
// question when the order the guards run in is what is under test.
func assertRejectedBecause(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, env *processor.OperationEnvelope, wantMessage string) {
	t.Helper()
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, wantMessage) {
		t.Fatalf("rejected with %+v, want a refusal containing %q", reply.Error, wantMessage)
	}
}

// driveConcurrently fetches n pending operations and runs them through the
// commit path SIMULTANEOUSLY, returning their outcomes. It exists because
// DriveOne is strictly serial: a second entry driven after the first always
// re-reads the .balance the first wrote, so the two never share a revision and
// the compare-and-set that conditions the update is never exercised. Only
// overlapping hydrations put two entries on the same revision of the account —
// the race a live front desk and a patient portal actually produce.
func driveConcurrently(t *testing.T, ctx context.Context, cp *processor.CommitPath,
	cons jetstream.Consumer, n int) []processor.MessageOutcome {
	t.Helper()
	batch, err := cons.Fetch(n, jetstream.FetchMaxWait(10*time.Second))
	if err != nil {
		t.Fatalf("Fetch(%d): %v", n, err)
	}
	var msgs []jetstream.Msg
	for m := range batch.Messages() {
		msgs = append(msgs, m)
	}
	if err := batch.Error(); err != nil {
		t.Fatalf("Fetch batch error: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("fetched %d messages, want %d", len(msgs), n)
	}

	outcomes := make([]processor.MessageOutcome, n)
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i, m := range msgs {
		wg.Add(1)
		go func(i int, m jetstream.Msg) {
			defer wg.Done()
			<-release
			outcomes[i] = cp.HandleMessage(ctx, m)
		}(i, m)
	}
	close(release)
	wg.Wait()
	return outcomes
}

// seedAspect writes one aspect document directly, in the shape the platform
// stores (vertexKey/localName carried alongside class/isDeleted/data).
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vtxKey, localName, class string, data map[string]any) {
	t.Helper()
	putDoc(t, ctx, conn, vtxKey+"."+localName, map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vtxKey, "localName": localName, "data": data,
	})
}

// seedTombstonedAspect writes one aspect document as a TOMBSTONE — present,
// isDeleted true, exactly what kv.Read hands a script for a soft-deleted key
// (step4_hydrate routes only ErrKeyNotFound to knownAbsent). No clinic op
// tombstones a .balance, so this shape has to be planted; the write path must
// still handle it, because a create against a tombstone is refused outright
// (Contract #3 §3.3) and an account stuck that way would take no more entries.
func seedTombstonedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vtxKey, localName, class string, data map[string]any) {
	t.Helper()
	putDoc(t, ctx, conn, vtxKey+"."+localName, map[string]any{
		"class": class, "isDeleted": true,
		"vertexKey": vtxKey, "localName": localName, "data": data,
	})
}

// seedLegacyAccount seeds a clinic account in the shape one minted under
// clinic-ledger < 0.3.0 sits in today: the vertex, the patient's own
// .ledgerAccount guard and the heldFor link, and NO .balance aspect — a shape
// no op produces any more.
func seedLegacyAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, acctID, patientKey string) string {
	t.Helper()
	acctKey := "vtx.clinicaccount." + acctID
	patientID := patientKey[len("vtx.patient."):]
	seedVertex(t, ctx, conn, acctKey, "clinicaccount", map[string]any{})
	seedAspect(t, ctx, conn, patientKey, "ledgerAccount", "clinicLedgerAccountGuard",
		map[string]any{"accountKey": acctKey})
	seedLink(t, ctx, conn,
		"lnk.clinicaccount."+acctID+".heldFor.patient."+patientID,
		acctKey, patientKey, "heldFor", "heldFor")
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("the fixture must start with NO .balance aspect — that is the legacy shape under test")
	}
	return acctKey
}

// budgetTxID encodes i as a valid 20-char NanoID (Contract #1's alphabet — no
// I/O/l/0), so the budget fixtures below can plant several hundred distinct
// transactions without hand-writing an id per entry.
func budgetTxID(i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	n := len(safe)
	return "CLBUDGETTXAHJKMNP" + string([]byte{safe[i/(n*n)%n], safe[(i/n)%n], safe[i%n]})
}

// TestAccountBalance_ConcurrentEntriesBothPost is why the .balance update
// carries NO expectedRevision of its own. Two charges against one account,
// hydrated together and committed together, both read the same revision of
// .balance — one of them necessarily loses the compare-and-set. Because the
// condition was DEFAULTED by the Processor (a declared read, Contract #3 §3.2)
// rather than asserted by the script, that loser is re-hydrated, re-executed
// against the winner's total and re-committed, so both entries post and the
// total is their sum. An explicit expectedRevision on the same update would be
// read as a caller's compensating assertion, excluded from that retry, and the
// loser would be rejected outright — a charge the clinic made and never billed.
//
// Serial driving cannot show this: DriveOne finishes the first entry before the
// second hydrates, so the second reads the already-updated revision and never
// races at all.
func TestAccountBalance_ConcurrentEntriesBothPost(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancerace")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatbalrace0000001", "Rosa Iberra")
	acctKey := createAccount(t, ctx, conn, cp, cons, "balraceacctsetup0001", patientKey)

	testutil.PublishOp(t, conn, staffDebitEnv("balracefirst00000001", acctKey, 700))
	testutil.PublishOp(t, conn, staffDebitEnv("balracesecond0000001", acctKey, 300))

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	for i, o := range outcomes {
		if o != processor.OutcomeAccepted {
			t.Fatalf("outcome[%d] = %v, want accepted: a lost race on the account's own .balance must re-hydrate and retry, not reject a charge the clinic made", i, o)
		}
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 1000 {
		t.Fatalf("balance after two concurrent charges = %v, want 1000 — neither may be dropped", got)
	}
}

// TestAccountBalance_UndeclaredSubmitterStillConditioned is the guarantee
// derive_reads exists for. contextHint is submitter-supplied and nothing
// enforces it, and a bare update is auto-conditioned only on a key the operation
// DECLARED (Contract #3 §3.2) — so a submitter that simply omits
// `<accountKey>.balance` would, without the script's own class-(g) derivation,
// get a live read and an unconditioned update, and K concurrent entries would
// each write their own total over the others.
//
// These two envelopes declare nothing about .balance at all. Both must still
// post, and the total must be their SUM: a lost update lands on one of the two
// amounts instead.
//
// Two assertions carry it, and they fail at different depths. Deleting the
// derivation makes the read LIVE and undeclared, which the read-drift guard
// (armed on every CapabilityPipeline) reports deterministically — that is the
// mechanism-level proof. Whether the lost update itself then materialises
// depends on how far the two live reads actually overlap, so the sum below is
// the outcome-level residual, not the primary signal.
func TestAccountBalance_UndeclaredSubmitterStillConditioned(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balanceundeclared")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatbalundecl00001", "Tomas Halvorsen")
	acctKey := createAccount(t, ctx, conn, cp, cons, "balundeclacctsetup01", patientKey)

	undeclaredDebit := func(label string, amountCents int) *processor.OperationEnvelope {
		env := staffDebitEnv(label, acctKey, amountCents)
		// The account alone. No optionalReads, no .balance — the shape a
		// client that never read the descriptor sends.
		env.ContextHint = &processor.ContextHint{Reads: []string{acctKey}}
		return env
	}
	testutil.PublishOp(t, conn, undeclaredDebit("balundecfirst0000001", 700))
	testutil.PublishOp(t, conn, undeclaredDebit("balundecsecond000001", 300))

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	for i, o := range outcomes {
		if o != processor.OutcomeAccepted {
			t.Fatalf("outcome[%d] = %v, want accepted", i, o)
		}
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 1000 {
		t.Fatalf("balance after two concurrent undeclared charges = %v, want 1000 — a submitter that declares nothing must not be able to turn the OCC condition off", got)
	}
}

// TestDeriveReads_BalanceKey pins the class-(g) derivation as TEXT, because its
// effect is otherwise invisible on the happy path: declared or not, the script
// reads .balance through kv.Read and an undeclared read falls through to a live
// Core KV GET that returns the same number. Only the concurrent test above shows
// the difference behaviourally, and only this shows the derivation still covers
// both ops rather than the one that test happens to drive.
func TestDeriveReads_BalanceKey(t *testing.T) {
	var script string
	for _, d := range clinicledger.DDLs() {
		if d.CanonicalName == "clinictransaction" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("no `clinictransaction` DDL script found")
	}
	deriveIdx := strings.Index(script, "def derive_reads(op):")
	executeIdx := strings.Index(script, "\ndef execute(state, op):")
	if deriveIdx < 0 || executeIdx <= deriveIdx {
		t.Fatalf("cannot locate derive_reads in the clinictransaction script (derive=%d execute=%d)", deriveIdx, executeIdx)
	}
	derive := script[deriveIdx:executeIdx]
	for _, want := range []string{"ClinicDebitAccount", "ClinicCreditAccount"} {
		if !strings.Contains(derive, want) {
			t.Fatalf("derive_reads does not mention %q — that op's .balance update would be unconditioned whenever its submitter omits the declaration", want)
		}
	}
	if !strings.Contains(derive, `{"optionalReads": [acct_key + ".balance"]}`) {
		t.Fatalf("derive_reads no longer returns the account's .balance under optionalReads:\n%s", derive)
	}
	// optionalReads, never reads: a legacy account carries no .balance, and a
	// required read's absence is a HydrationMiss on the very branch the replay
	// exists for.
	if strings.Contains(derive, `"reads"`) {
		t.Fatalf("derive_reads returns a hard `reads` entry — every legacy account would HydrationMiss:\n%s", derive)
	}
}

// TestAccountBalance_LegacyAccountSelfHealsOnFirstTouch covers the accounts that
// already exist. One minted under clinic-ledger < 0.3.0 carries no .balance
// aspect, which is why every dispatcher declares the key optionalReads rather
// than reads — a required read would HydrationMiss-reject every entry against
// it. A SELF-PAY against such an account replays its postedTo history once,
// bounded, to get the number its own cap needs, and mints the total from that;
// every touch afterwards is O(1). The seeded history is a debit AND a credit so
// a replay that summed only charges (or got the sign wrong) lands on a different
// number than one that nets them.
func TestAccountBalance_LegacyAccountSelfHealsOnFirstTouch(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "legacybalance")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLLEGACYPATNTHJKMNPQ", ledgerSelfConsumerID)
	acctKey := seedLegacyAccount(t, ctx, conn, "CLLEGACYACCTHJKMNPQR", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]

	// Pre-existing history: a $40 charge, a $10 payment — $30 owed, computed
	// nowhere but this replay until the first self-pay below.
	seedLegacyTransaction(t, ctx, conn, "CLLEGACYTXNAHJKMNPQR", acctKey, acctID, "debit", 4000)
	seedLegacyTransaction(t, ctx, conn, "CLLEGACYTXNBHJKMNPQR", acctKey, acctID, "credit", 1000)

	// The cap already measures against the replayed number, before any .balance
	// exists: $30 is owed, so $30.01 is not payable — and a refused payment
	// commits nothing, so the account is still legacy afterwards.
	selfPay(t, ctx, conn, cp, cons, "legacyoverpay0000001", acctKey, 3001, processor.OutcomeRejected)
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("a REFUSED self-pay seeded .balance — nothing about a rejected op may commit")
	}

	// The first accepted self-pay mints the aspect from the replayed total.
	selfPay(t, ctx, conn, cp, cons, "legacyselfpay0000001", acctKey, 1000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 2000 {
		t.Fatalf("backfilled balance = %v, want 2000 (4000 charged − 1000 paid − this 1000 self-pay)", got)
	}

	// And every touch after that is the O(1) path off the aspect itself.
	selfPay(t, ctx, conn, cp, cons, "legacyselfpay0000002", acctKey, 2000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying the backfilled total = %v, want 0", got)
	}
}

// TestAccountBalance_LegacyAccountDebitDoesNotSeed pins which leg pays for the
// replay, and it is only the self-pay. A charge (and a staff credit or waiver)
// against a legacy account posts normally and writes NO .balance: seeding the
// cache from that one entry would record a total that never counted the history
// behind it, and every later self-pay would be capped against that wrong number.
// The account stays legacy until a self-pay first touches it — and that
// self-pay's replay sums the whole history, the charges posted meanwhile
// included.
//
// The wedge this avoids is the reason: clinicNoShowSettlement's missing_charge
// dispatch is the ONLY writer of a no-show fee, it runs unattended, and an
// account whose history outgrew the replay budget would refuse every one of
// those dispatches forever with no repair path.
func TestAccountBalance_LegacyAccountDebitDoesNotSeed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "legacydebitnoseed")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLBALLGDPATNTHJKMNPQ", ledgerSelfConsumerID)
	acctKey := seedLegacyAccount(t, ctx, conn, "CLBALLGDACCTHJKMNPQR", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]
	seedLegacyTransaction(t, ctx, conn, "CLBALLGDTXAHJKMNPQRS", acctKey, acctID, "debit", 3000)

	staffDebit(t, ctx, conn, cp, cons, "balgddebit0000000001", acctKey, 1000, processor.OutcomeAccepted)
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("a charge against a legacy account seeded .balance — only a self-pay may, and only from the whole history")
	}

	// The later self-pay's replay counts that charge: 4000 is owed, not 3000.
	selfPay(t, ctx, conn, cp, cons, "balgdoverpay00000001", acctKey, 4001, processor.OutcomeRejected)
	selfPay(t, ctx, conn, cp, cons, "balgdselfpay00000001", acctKey, 4000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying off a legacy account = %v, want 0 (3000 seeded + 1000 charged − 4000 paid)", got)
	}
}

// TestAccountBalance_LegacyFirstTouchRace is the two-self-pay race on the FIRST
// touch of a legacy account, where neither submission has a .balance revision to
// be conditioned on — both see the key absent and both emit a create. The create
// carries that declared absence as its assertion, so the loser is not rejected:
// commit_path.go re-probes it (materializedAbsentKeys), re-hydrates, and
// re-executes against the winner's freshly minted total.
//
// The two halves are sized so that a correct retry accepts BOTH (1500 + 1500
// against 3000 owed, the second measured against the winner's 1500). That is
// what makes the assertion sharp: a hard RevisionConflict, or a create that
// clobbered the winner, shows up as a rejection or a wrong total rather than
// hiding behind an outcome the cap could also have produced.
func TestAccountBalance_LegacyFirstTouchRace(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "legacyfirsttouchrace")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLBALLGRPATNTHJKMNPQ", ledgerSelfConsumerID)
	acctKey := seedLegacyAccount(t, ctx, conn, "CLBALLGRACCTHJKMNPQR", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]
	seedLegacyTransaction(t, ctx, conn, "CLBALLGRTXAHJKMNPQRS", acctKey, acctID, "debit", 3000)

	testutil.PublishOp(t, conn, selfPayEnv("balgracefirst0000001", acctKey, 1500))
	testutil.PublishOp(t, conn, selfPayEnv("balgracesecond000001", acctKey, 1500))

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	for i, o := range outcomes {
		if o != processor.OutcomeAccepted {
			t.Fatalf("outcome[%d] = %v, want accepted: the loser of a first-touch race re-hydrates against the winner's minted .balance and re-runs, never conflicts out", i, o)
		}
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after two concurrent 1500 self-pays on a 3000 legacy balance = %v, want 0", got)
	}
}

// TestAccountBalance_TombstonedBalanceRevivesByUpdate is the shape a create
// cannot serve. Contract #3 §3.3 refuses a create against a tombstone, so the
// absence a legacy account presents and the absence a TOMBSTONED .balance
// presents are not the same absence: the first is minted, the second is revived
// by the update verb, auto-conditioned on the tombstone's own hydrated revision.
// Collapsing the two would reject every entry against such an account.
func TestAccountBalance_TombstonedBalanceRevivesByUpdate(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancetombstone")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLBALTMBPATNTHJKMNPQ", ledgerSelfConsumerID)
	acctKey := seedLegacyAccount(t, ctx, conn, "CLBALTMBACCTHJKMNPQR", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]
	seedLegacyTransaction(t, ctx, conn, "CLBALTMBTXAHJKMNPQRS", acctKey, acctID, "debit", 3000)
	seedTombstonedAspect(t, ctx, conn, acctKey, "balance", "clinicAccountBalance",
		map[string]any{"balanceCents": 0})

	selfPay(t, ctx, conn, cp, cons, "baltombselfpay000001", acctKey, 1000, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, acctKey+".balance")
	if deleted, _ := doc["isDeleted"].(bool); deleted {
		t.Fatalf("%s.balance is still tombstoned after an accepted self-pay", acctKey)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 2000 {
		t.Fatalf("revived balance = %v, want 2000 (3000 replayed − 1000 paid) — the tombstoned document's own stale zero must not be read", got)
	}
}

// TestAccountBalance_WrongClassRefused: the script is the sole writer of a
// .balance aspect and writes exactly clinicAccountBalance, so a document of any
// other class under that key is a fault, not a number to move or to measure a
// self-pay cap against. Reading balanceCents off whatever happened to be there
// would let an unrelated aspect's field decide how much a patient may pay.
func TestAccountBalance_WrongClassRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancewrongclass")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatbalwrcls000001", "Ada Mwangi")
	acctKey := createAccount(t, ctx, conn, cp, cons, "balwrongclsacct00001", patientKey)
	seedAspect(t, ctx, conn, acctKey, "balance", "transactionEntry",
		map[string]any{"balanceCents": 900000})

	assertRejectedBecause(t, ctx, conn, cp, cons,
		staffDebitEnv("balwrongclsdebit0001", acctKey, 5000),
		"InvalidState: this account's balance aspect is not a clinicAccountBalance")
}

// TestCreditAccount_StrangersAccountDeniedBeforeAnyReplay is the amplification
// primitive the ordering closes. The scope=self grant is held by every patient,
// and the accountKey is payload-supplied — so if the legacy backfill ran before
// the ownership proof, any patient could name any account, make the server walk
// that account's whole transaction history repeatedly, and be denied only
// afterwards.
//
// The stranger's account is deliberately over-budget, which is what makes the
// ordering visible rather than merely believed: run the replay first and the
// refusal is the budget's ("could not backfill…"); prove ownership first and it
// is this one. A same-shaped account under the budget would refuse identically
// either way and prove nothing.
func TestCreditAccount_StrangersAccountDeniedBeforeAnyReplay(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfamplify")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	strangerID := "CLAMPLSTRANGERHJKMNP"
	seedIdentity(t, ctx, conn, strangerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLAMPLQTHERPATNTHJKM", strangerID)
	acctKey := seedLegacyAccount(t, ctx, conn, "CLAMPLACCTHJKMNPQRST", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]
	for i := 0; i < 501; i++ {
		seedLegacyTransaction(t, ctx, conn, budgetTxID(i), acctKey, acctID, "debit", 100)
	}

	assertRejectedBecause(t, ctx, conn, cp, cons,
		selfPayEnv("amplifyselfpay000001", acctKey, 100),
		"AuthDenied: a patient may only pay down their own account")
}

// TestCreditAccount_BackfillBudgetExhausted is the fail-closed end of the
// replay. The budget is 10 pages of 50, so an account carrying more than 500
// postedTo entries cannot be summed in one operation — and the script refuses
// the payment rather than seeding .balance from the partial sum it did reach,
// which would silently under-state the debt for the life of the account.
//
// The refusal names no key: it is toasted verbatim at whoever tried to pay.
func TestCreditAccount_BackfillBudgetExhausted(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancebudget")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLBALBUDPATNTHJKMNPQ", ledgerSelfConsumerID)
	acctKey := seedLegacyAccount(t, ctx, conn, "CLBALBUDACCTHJKMNPQR", patientKey)
	acctID := acctKey[len("vtx.clinicaccount."):]
	for i := 0; i < 501; i++ {
		seedLegacyTransaction(t, ctx, conn, budgetTxID(i), acctKey, acctID, "debit", 100)
	}

	assertRejectedBecause(t, ctx, conn, cp, cons,
		selfPayEnv("balbudgetselfpay0001", acctKey, 100),
		"AuthDenied: could not backfill this account's balance (too much transaction history for one op)")
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("the refused self-pay seeded .balance from a partial replay — the whole point is that it must not")
	}
}

// TestCreditAccount_SelfPayCapNamesDollars pins the refusal TEXT the patient is
// shown. MessageOutcome collapses every refusal into "rejected", so the two
// existing over-balance tests pass just as well against a guard that denied the
// actor or the account; and the number in the text is what the patient acts on,
// so "exceeds 2500" — a raw cent count, or a vtx key — is the wrong thing to
// toast at them.
func TestCreditAccount_SelfPayCapNamesDollars(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfcaptext")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	patientKey := seedPatientWithIdentity(t, ctx, conn, "CLBALWRCPATNTHJKMNPQ", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "captextacctsetup0001", patientKey)

	// Nothing owed yet: the empty-balance refusal, which names no account.
	assertRejectedBecause(t, ctx, conn, cp, cons,
		selfPayEnv("captextnobalpay00001", acctKey, 100),
		"AuthDenied: this account has no outstanding balance to pay")

	staffDebit(t, ctx, conn, cp, cons, "captextdebit00000001", acctKey, 1425, processor.OutcomeAccepted)

	// $14.25 owed; the patient types $50.00. Both amounts are spelled as money.
	assertRejectedBecause(t, ctx, conn, cp, cons,
		selfPayEnv("captextoverpay000001", acctKey, 5000),
		"AuthDenied: a payment of $50.00 exceeds the outstanding balance of $14.25")
	if got := balanceCents(t, ctx, conn, acctKey); got != 1425 {
		t.Fatalf("balance after the refused self-pay = %v, want the untouched 1425", got)
	}

	// Exactly what is owed is fine — the cap is `>`, not `>=`, and a
	// rejection-only test would pass against a guard that refused every payment.
	selfPay(t, ctx, conn, cp, cons, "captextexactpay00001", acctKey, 1425, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying in full = %v, want 0", got)
	}
}

// TestCreditAccount_StaffLegStaysUncapped is the boundary of the cap. A
// front-desk credit records a decision the clinic made — cash taken at the
// counter, or a charge waived — and clinicNoShowSettlement's missing_reversal
// dispatch gives back a charge that may already have been paid. Capping either
// at what is currently owed would make the one case a reversal exists for the
// one case that could not be posted, so the staff leg stays uncapped and the
// balance legitimately goes negative.
func TestCreditAccount_StaffLegStaysUncapped(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditstaffuncapped")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpatstaffuncap0001", "Yusuf Bektas")
	acctKey := createAccount(t, ctx, conn, cp, cons, "staffuncapacctsetup1", patientKey)
	staffDebit(t, ctx, conn, cp, cons, "staffuncapdebit00001", acctKey, 900, processor.OutcomeAccepted)

	waiverEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("staffuncapwaiver0001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-20T10:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"reason":"waiver"}`),
		ContextHint:   staffDebitHint(acctKey),
	}
	testutil.PublishOp(t, conn, waiverEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if got := balanceCents(t, ctx, conn, acctKey); got != -1600 {
		t.Fatalf("balance after waiving 2500 against a 900 charge = %v, want -1600 — the staff leg is not capped by what is owed", got)
	}
}
