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
			{OperationType: "CreateAccount", Scope: "any"},
			{OperationType: "DebitAccount", Scope: "any"},
			{OperationType: "CreditAccount", Scope: "any"},
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
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
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
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.patient." + nanoIDFromRequestID(reqID)
}

// createAccount submits CreateAccount{patientKey} and returns the account key
// — the account's own independently-minted NanoID, matching the deterministic
// nanoid.new() seed the test harness uses for the transaction DDL (never
// derived from the patient's own id).
func createAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, patientKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAccount",
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

// TestCreateAccount_MintsAccountHeldForPatient (test 1). CreateAccount mints
// vtx.clinicaccount.<freshId> (root {} — D5, an id independent of the
// patient's own) + the patient's .ledgerAccount guard aspect + the heldFor
// link; a second call for the same patient that declares the guard aspect in
// reads conflicts on it (AccountAlreadyExists).
func TestCreateAccount_MintsAccountHeldForPatient(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "create")

	patientKey := createPatient(t, ctx, conn, cp, cons, "mkpat0000000000001", "Alice Rivera")
	patientID := patientKey[len("vtx.patient."):]
	guardKey := patientKey + ".ledgerAccount"

	if keyExists(t, ctx, conn, guardKey) {
		t.Fatalf("guard aspect must not exist before CreateAccount")
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

	// A second CreateAccount for the SAME patient, declaring the now-existing
	// guard aspect in reads, conflicts on it (AccountAlreadyExists — the
	// create-only write is the guard).
	dup := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacct0000002"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:05:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey, guardKey}},
	}
	testutil.PublishOp(t, conn, dup)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateAccount_UnknownPatient rejects an account opened against a
// non-existent patient (no-orphan invariant).
func TestCreateAccount_UnknownPatient(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownpatient")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacctunknown01"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"vtx.patient.CLABSENTPATNTHJKMNPQ"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.patient.CLABSENTPATNTHJKMNPQ"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitCreditAccount_PostEntries (test 2). DebitAccount/CreditAccount each
// mint a fresh transaction vertex (root {} — D5) + a .entry aspect + the
// postedTo link to the account; the account root is never touched (append-only
// ledger, no balance stored).
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
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Office visit copay"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
		OperationType: "CreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-05T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Insurance payment - claim #4471"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":15000,"memo":"Specialist visit",` +
			`"billedTo":"insurance","expectedReimbursementCents":12000}`),
		ContextHint: &processor.ContextHint{Reads: []string{acctKey}},
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
// exceeds the charge, and either field on a CreditAccount (a payment has
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
		{"unrecognized billedTo value", "DebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"medicare"}`},
		{"insurance with no reimbursement figure", "DebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"insurance"}`},
		{"self-pay debit supplying a reimbursement anyway", "DebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"self","expectedReimbursementCents":500}`},
		{"reimbursement exceeds the charge", "DebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"insurance","expectedReimbursementCents":1500}`},
		{"non-positive reimbursement", "DebitAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"insurance","expectedReimbursementCents":0}`},
		{"billedTo on a credit (payment)", "CreditAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"billedTo":"self"}`},
		{"expectedReimbursementCents on a credit (payment)", "CreditAccount", `{"accountKey":"` + acctKey + `","amountCents":1000,"expectedReimbursementCents":500}`},
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
			ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
		OperationType: "DebitAccount",
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
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
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
		ContextHint: &processor.ContextHint{Reads: []string{patientKey, providerKey}},
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
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"appointmentRef":"` + apptKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, apptKey}},
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
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:05:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "clinictransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"appointmentRef":"vtx.appointment.CLABSENTAPPTHJKMNPQR"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, "vtx.appointment.CLABSENTAPPTHJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
