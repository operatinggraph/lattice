// loftspace-ledger integration tests through the real install + Processor
// pipeline. External test package (loftspaceledger_test) so they exercise the
// public Lattice surface: seed the kernel, install rbac + identity + hygiene +
// orchestration-base + service-domain + lease-signing + loftspace-ledger
// through the Processor, then submit the ops and assert the committed Core-KV
// shape + the emitted events.
package loftspaceledger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	loftspaceledger "github.com/operatinggraph/lattice/packages/loftspace-ledger"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
)

const (
	ledgerActorID  = "BBLEDGERACTRHJKMNPQR"
	ledgerActorKey = "vtx.identity." + ledgerActorID
	ledgerCapKey   = "cap.identity." + ledgerActorID

	// ledgerConsumerRoleID stands in for identity-domain's real `consumer`
	// role NanoID: this package's tests don't install identity-domain (only
	// rbac + hygiene via SetupPackageTestEnv), so lease-signing's
	// CreateLeaseApplication scope=self grant (GrantsTo: "consumer") needs a
	// role id registered directly.
	ledgerConsumerRoleID = "BBConsumerRoZe2JKMNP"
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
			{OperationType: "CreateLeaseApplication", Scope: "any"},
			{OperationType: "LoftspaceCreateAccount", Scope: "any"},
			{OperationType: "DebitAccount", Scope: "any"},
			{OperationType: "LoftspaceRecordCharge", Scope: "any"},
			{OperationType: "CreditAccount", Scope: "any"},
			{OperationType: "ReturnDeposit", Scope: "any"},
			// Deliberately the SAME grant Weaver holds for EvaluateLoftspaceArrears
			// (arrearsWeaverCapDoc, arrears_test.go). That is what makes the
			// forged-send vector attributable: the refusal can only come from the
			// script's own actor guard, never from a missing or narrower grant.
			{OperationType: "EvaluateLoftspaceArrears", Scope: "any"},
			// The bridge's service actor is operator-equivalent, and this stands
			// in for it: the replyOp is granted to operator/Scope:"any" by the
			// package (notifications.go), so step 3 authorizes any operator that
			// submits it. Everything that constrains WHAT such a submission can
			// touch lives in the script's own validation of externalRef.
			{OperationType: "RecordLoftspaceArrearsReminderNotification", Scope: "any"},
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
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": ledgerConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("install orchestration-base: %v", err)
	}
	if _, err := inst.Install(ctx, servicedomain.Package); err != nil {
		t.Fatalf("install service-domain: %v", err)
	}
	if _, err := inst.Install(ctx, leasesigning.Package); err != nil {
		t.Fatalf("install lease-signing: %v", err)
	}
	if _, err := inst.Install(ctx, loftspaceledger.Package); err != nil {
		t.Fatalf("install loftspace-ledger: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, ledgerCapDoc())
	testutil.SeedCapDoc(t, ctx, conn, arrearsWeaverCapDoc())
	// LoftspaceCreateAccount's workplace guard asks the GRAPH whether its
	// caller is root, so the cap doc's Roles claim is not enough on its own —
	// without the link this actor reads as an unprivileged caller with no
	// worksAt anywhere (testutil.SeedHoldsRole's doc comment).
	testutil.SeedHoldsRole(t, ctx, conn, ledgerActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

func newLedgerPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "ll-" + durable,
	})
}

func nanoIDFromRequestID(requestID string) string {
	seed := processor.SeedFromRequestID(requestID)
	pcg := rand.NewPCG(seed[0], seed[1])
	return processor.DeterministicNanoID(pcg, substrate.NanoIDLength)
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

// seedLease seeds a live leaseapp vertex to hold an account for.
func seedLease(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.leaseapp." + id
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	return key
}

// createAccount submits LoftspaceCreateAccount{leaseAppKey} and returns the account key
// — the account's own independently-minted NanoID, matching the deterministic
// nanoid.new() seed the test harness uses for the transaction DDL (never
// derived from the lease's own id).
func createAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "LoftspaceCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "account",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint:   &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("LoftspaceCreateAccount", ledgerActorKey, loftspaceledger.OpMetas()), Reads: []string{leaseAppKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.account." + nanoIDFromRequestID(reqID)
}

// TestLoftspaceCreateAccount_MintsAccountHeldForLease (test 1). LoftspaceCreateAccount mints
// vtx.account.<freshId> (root {} — D5, an id independent of the lease's own)
// + the leaseapp's .ledgerAccount guard aspect + the heldFor link; a second
// call for the same lease that declares the guard aspect in reads conflicts
// on it (AccountAlreadyExists).
func TestLoftspaceCreateAccount_MintsAccountHeldForLease(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "create")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEACCTHJKMNPQRS")
	leaseID := "BBLEASEACCTHJKMNPQRS"
	guardKey := leaseKey + ".ledgerAccount"

	if keyExists(t, ctx, conn, guardKey) {
		t.Fatalf("guard aspect must not exist before LoftspaceCreateAccount")
	}

	acctKey := createAccount(t, ctx, conn, cp, cons, "createacct0000001", leaseKey)
	acctID := acctKey[len("vtx.account."):]
	if acctID == leaseID {
		t.Fatalf("account id must NOT equal the lease's own id (independently minted), got %q for both", acctID)
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

	heldForLnk := "lnk.account." + acctID + ".heldFor.leaseapp." + leaseID
	if !keyExists(t, ctx, conn, heldForLnk) {
		t.Fatalf("heldFor link must exist: %s", heldForLnk)
	}

	// A second LoftspaceCreateAccount for the SAME lease, declaring the now-existing
	// guard aspect in reads, conflicts on it (AccountAlreadyExists — the
	// create-only write is the guard).
	dup := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacct0000002"),
		Lane:          processor.LaneDefault,
		OperationType: "LoftspaceCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:05:00Z",
		Class:         "account",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint:   &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("LoftspaceCreateAccount", ledgerActorKey, loftspaceledger.OpMetas()), Reads: []string{leaseKey, guardKey}},
	}
	testutil.PublishOp(t, conn, dup)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestLoftspaceCreateAccount_UnknownLease rejects an account opened against a
// non-existent lease (no-orphan invariant).
func TestLoftspaceCreateAccount_UnknownLease(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownlease")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacctunknown01"),
		Lane:          processor.LaneDefault,
		OperationType: "LoftspaceCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "account",
		Payload:       json.RawMessage(`{"leaseAppKey":"vtx.leaseapp.BBABSENTLEASEHJKMNPQ"}`),
		ContextHint:   &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("LoftspaceCreateAccount", ledgerActorKey, loftspaceledger.OpMetas()), Reads: []string{"vtx.leaseapp.BBABSENTLEASEHJKMNPQ"}},
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

	leaseKey := seedLease(t, ctx, conn, "BBLEASEPSTXHJKMNPQRS")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctpost00001", leaseKey)

	debitReqID := testutil.GenReqID("debitrent0000000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":150000,"memo":"June rent"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	debitTxKey := "vtx.transaction." + nanoIDFromRequestID(debitReqID)
	entryDoc := readDoc(t, ctx, conn, debitTxKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "debit" {
		t.Fatalf("entry.type = %q, want debit", got)
	}
	if got, _ := entryData["amountCents"].(float64); got != 150000 {
		t.Fatalf("entry.amountCents = %v, want 150000", got)
	}
	if got, _ := entryData["memo"].(string); got != "June rent" {
		t.Fatalf("entry.memo = %q, want %q", got, "June rent")
	}

	txDoc := readDoc(t, ctx, conn, debitTxKey)
	if d, _ := txDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("transaction root data must stay minimal ({}) after post, got %v", d)
	}

	acctID := acctKey[len("vtx.account."):]
	postedToLnk := "lnk.transaction." + nanoIDFromRequestID(debitReqID) + ".postedTo.account." + acctID
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
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":150000,"memo":"Rent payment - check #1042"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	creditTxKey := "vtx.transaction." + nanoIDFromRequestID(creditReqID)
	creditEntryDoc := readDoc(t, ctx, conn, creditTxKey+".entry")
	creditEntryData, _ := creditEntryDoc["data"].(map[string]any)
	if got, _ := creditEntryData["type"].(string); got != "credit" {
		t.Fatalf("entry.type = %q, want credit", got)
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
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"vtx.account.BBABSENTACCTHJKMNPQR","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.account.BBABSENTACCTHJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_NonPositiveAmountRejected rejects amountCents <= 0
// (InvalidArgument).
func TestDebitAccount_NonPositiveAmountRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "badamount")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEBADXHJKMNPQRS")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctbad000001", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitbadamount00001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// --- CreditAccount consumer scope=self (resident self-pay) -----------------

const (
	ledgerSelfConsumerID  = "BBLEDGERSELFCQNSHJKM"
	ledgerSelfConsumerKey = "vtx.identity." + ledgerSelfConsumerID
	ledgerSelfConsumerCap = "cap.identity." + ledgerSelfConsumerID
)

// ledgerSelfConsumerCapDoc is the resident: both consumer scope=self grants
// the package declares (permissions.go), so step 3 admits a resident
// LoftspaceRecordCharge and the SCRIPT's own refusal is what the
// resident-charge test proves — mirrors cafe-domain's domainConsumerCapDoc.
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
			{OperationType: "CreditAccount", Scope: "self"},
			{OperationType: "LoftspaceRecordCharge", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// --- LoftspaceRecordCharge / CreditAccount consumer scope=self (landlord) ---

const (
	ledgerLandlordID  = "BBLEDGERLANDLQRDHJKM"
	ledgerLandlordKey = "vtx.identity." + ledgerLandlordID
	ledgerLandlordCap = "cap.identity." + ledgerLandlordID
)

// ledgerLandlordCapDoc is the plain signed-in landlord: `consumer` plus the
// two scope=self grants the package declares (LoftspaceRecordCharge,
// CreditAccount — never DebitAccount, which has no self grant) and no
// operator role, so step 3 denies unless authContext.target == actor — the
// path the manages proof binds.
func ledgerLandlordCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ledgerLandlordCap,
		Actor:                  ledgerLandlordKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ledgerLandlordKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "LoftspaceRecordCharge", Scope: "self"},
			{OperationType: "CreditAccount", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

// seedUnitForLease seeds a live unit vertex and the lease's appliesToUnit
// link to it — the hop the landlord proof walks (lease_unit) before it reads
// the caller's manages link.
func seedUnitForLease(t *testing.T, ctx context.Context, conn *substrate.Conn, unitID, leaseKey string) string {
	t.Helper()
	unitKey := "vtx.unit." + unitID
	seedVertex(t, ctx, conn, unitKey, "unit", map[string]any{})
	leaseID := strings.TrimPrefix(leaseKey, "vtx.leaseapp.")
	seedLink(t, ctx, conn, "lnk.leaseapp."+leaseID+".appliesToUnit.unit."+unitID, leaseKey, unitKey, "appliesToUnit", "appliesToUnit")
	return unitKey
}

// seedManages seeds the landlord's management link to a unit.
func seedManages(t *testing.T, ctx context.Context, conn *substrate.Conn, landlordID, unitID string) {
	t.Helper()
	seedLink(t, ctx, conn, "lnk.identity."+landlordID+".manages.unit."+unitID, "vtx.identity."+landlordID, "vtx.unit."+unitID, "manages", "manages")
}

// submitSelfEntry submits a transaction op as actor acting as
// themselves (authContext.target == actor — the only shape a scope=self grant
// authorizes at all) and returns the reply so a test can see which check
// answered.
func submitSelfEntry(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, opType, actorKey, acctKey string, amountCents int) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         actorKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + fmt.Sprint(amountCents) + `}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext:   &processor.AuthContext{Target: actorKey},
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// TestRecordChargeCreditAccount_LandlordSelfScope_Allowed proves a landlord
// holding no operator role records a charge AND a payment on the account of a lease
// whose unit they manage: the account's heldFor lease is somebody else's
// (applicationFor names a different identity, so the resident proof fails
// first), its appliesToUnit unit carries the landlord's manages link, and the
// payment is LARGER than what is owed — the landlord leg carries no balance
// cap (the landlord is the creditor).
func TestRecordChargeCreditAccount_LandlordSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "landlordselfok")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBLEDGERLLTENANTHJKM"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERLLQKLEASEHJK", tenantID)
	unitID := "BBLEDGERLLQKUNJTHJKM"
	seedUnitForLease(t, ctx, conn, unitID, leaseKey)
	seedManages(t, ctx, conn, ledgerLandlordID, unitID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "landlordokacctsetup1", leaseKey)

	got, reply := submitSelfEntry(t, ctx, conn, cp, cons, "landlordokdebit00001", "LoftspaceRecordCharge", ledgerLandlordKey, acctKey, 150000)
	if got != processor.OutcomeAccepted {
		t.Fatalf("landlord LoftspaceRecordCharge on a managed unit's lease = %v (%+v), want Accepted", got, reply.Error)
	}
	got, reply = submitSelfEntry(t, ctx, conn, cp, cons, "landlordokcredit0001", "CreditAccount", ledgerLandlordKey, acctKey, 200000)
	if got != processor.OutcomeAccepted {
		t.Fatalf("landlord CreditAccount above the balance on a managed unit's lease = %v (%+v), want Accepted (the landlord leg is uncapped)", got, reply.Error)
	}
}

// TestRecordCharge_LandlordSelfScope_RejectedUnmanagedUnit proves the manages
// proof confines the landlord to their own units: the lease sits on a unit
// the landlord does NOT manage (they manage a different one), so the charge
// is refused AuthDenied — and the refusal never names the unit the account's
// lease sits on.
func TestRecordCharge_LandlordSelfScope_RejectedUnmanagedUnit(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "landlordselfother")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBLEDGERLLTENANTQHJK"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERLLQTHERLEASE", tenantID)
	theirsID := "BBLEDGERLLQTHERUNJTH"
	theirs := seedUnitForLease(t, ctx, conn, theirsID, leaseKey)
	mineID := "BBLEDGERLLMJNEUNJTHJ"
	seedVertex(t, ctx, conn, "vtx.unit."+mineID, "unit", map[string]any{})
	seedManages(t, ctx, conn, ledgerLandlordID, mineID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "landlordotheracctset", leaseKey)

	got, reply := submitSelfEntry(t, ctx, conn, cp, cons, "landlordotherdebit01", "LoftspaceRecordCharge", ledgerLandlordKey, acctKey, 150000)
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord LoftspaceRecordCharge on an UNMANAGED unit's lease = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the unmanaged charge rejected with %+v, want AuthDenied — the manages proof must answer", reply.Error)
	}
	if strings.Contains(reply.Error.Message, theirs) || strings.Contains(reply.Error.Message, theirsID) {
		t.Fatalf("the denial names the unit the lease sits on (%q) — a denial must not be a lookup for a resource the caller does not manage", reply.Error.Message)
	}
}

// TestDebitAccount_NoSelfGrant proves the clause-authorized op did not inherit
// the landlord path: the same landlord (manages link, LoftspaceRecordCharge:self
// in the cap doc, no DebitAccount:self) submitting DebitAccount with a target
// is refused at step 3 — DebitAccount is the orchestrated charge, operator
// and Weaver only.
func TestDebitAccount_NoSelfGrant(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "landlorddebitnogrant")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBLEDGERLLTENANTNGHJ"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERLLNGLEASEHJK", tenantID)
	unitID := "BBLEDGERLLNGUNJTHJKM"
	seedUnitForLease(t, ctx, conn, unitID, leaseKey)
	seedManages(t, ctx, conn, ledgerLandlordID, unitID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "landlordngacctsetup1", leaseKey)

	got, reply := submitSelfEntry(t, ctx, conn, cp, cons, "landlordngdebit00001", "DebitAccount", ledgerLandlordKey, acctKey, 150000)
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord DebitAccount with a self target = %v, want Rejected (no scope=self grant exists for DebitAccount)", got)
	}
	if reply.Error == nil || reply.Error.Code != "AuthDenied" || strings.Contains(reply.Error.Message, "ScriptError") {
		t.Fatalf("landlord DebitAccount rejected with %+v, want step 3's AuthDenied (no matching platformPermission), never the script", reply.Error)
	}
}

// TestRecordChargeCreditAccount_DualHolder_IsResident proves resident-first
// ordering: an identity that BOTH holds the lease (applicationFor) AND manages
// its unit is a resident -- the charge is refused by the resident branch and
// the over-balance credit hits the resident cap, never the uncapped landlord
// leg.
func TestRecordChargeCreditAccount_DualHolder_IsResident(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "landlorddualholder")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERLLDUALLEASEH", ledgerLandlordID)
	unitID := "BBLEDGERLLDUALUNJTHJ"
	seedUnitForLease(t, ctx, conn, unitID, leaseKey)
	seedManages(t, ctx, conn, ledgerLandlordID, unitID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "landlorddualacctset1", leaseKey)

	got, reply := submitSelfEntry(t, ctx, conn, cp, cons, "landlorddualcharge01", "LoftspaceRecordCharge", ledgerLandlordKey, acctKey, 150000)
	if got != processor.OutcomeRejected {
		t.Fatalf("dual holder's LoftspaceRecordCharge = %v, want Rejected (a landlord who tenants their own unit is a resident)", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "a resident may only credit") {
		t.Fatalf("dual holder's charge rejected with %+v, want the resident branch's refusal", reply.Error)
	}

	// $1,500 owed via the operator; the dual holder's $2,000 self-credit must
	// hit the resident cap, never the landlord leg's uncapped credit.
	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("landlorddualdebit001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":150000,"memo":"July rent"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	got, reply = submitSelfEntry(t, ctx, conn, cp, cons, "landlorddualcredit01", "CreditAccount", ledgerLandlordKey, acctKey, 200000)
	if got != processor.OutcomeRejected {
		t.Fatalf("dual holder's over-balance CreditAccount = %v, want Rejected (the resident cap applies)", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "PaymentExceedsBalance") {
		t.Fatalf("dual holder's over-balance credit rejected with %+v, want PaymentExceedsBalance from the resident branch", reply.Error)
	}
}

// --- LoftspaceCreateAccount consumer scope=self (landlord) ------------------

// ledgerLandlordOpenCapDoc is the landlord opening a lease's ledger account on
// its first-ever charge: `consumer` plus LoftspaceCreateAccount:self and no
// operator role.
func ledgerLandlordOpenCapDoc() *processor.CapabilityDoc {
	doc := ledgerLandlordCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "LoftspaceCreateAccount", Scope: "self"})
	return doc
}

// openAccountAsLandlord submits LoftspaceCreateAccount as the landlord acting
// as themselves and returns the reply so a test can see which check answered.
func openAccountAsLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseKey string) (processor.MessageOutcome, *processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "LoftspaceCreateAccount",
		Actor:         ledgerLandlordKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "account",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint:   &processor.ContextHint{Enumerations: testutil.DeclaredEnumerations("LoftspaceCreateAccount", ledgerLandlordKey, loftspaceledger.OpMetas()), Reads: []string{leaseKey}},
		AuthContext:   &processor.AuthContext{Target: ledgerLandlordKey},
	}
	got, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	return got, reply, "vtx.account." + nanoIDFromRequestID(reqID)
}

// TestLoftspaceCreateAccount_LandlordSelfScope_Allowed proves a landlord
// holding no operator role and no worksAt link opens the ledger account of a
// lease whose unit they manage, and the account is heldFor that lease.
func TestLoftspaceCreateAccount_LandlordSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordOpenCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "landlordopenok")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBLEDGERLLTENANTQPHJ"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERLLQPENLEASEH", tenantID)
	unitID := "BBLEDGERLLQPENUNJTHJ"
	seedUnitForLease(t, ctx, conn, unitID, leaseKey)
	seedManages(t, ctx, conn, ledgerLandlordID, unitID)

	got, reply, acctKey := openAccountAsLandlord(t, ctx, conn, cp, cons, "landlordopenacct0001", leaseKey)
	if got != processor.OutcomeAccepted {
		t.Fatalf("landlord LoftspaceCreateAccount on a managed unit's lease = %v (%+v), want Accepted", got, reply.Error)
	}
	if !keyExists(t, ctx, conn, acctKey) {
		t.Fatalf("account %s not minted", acctKey)
	}
	heldFor := "lnk.account." + strings.TrimPrefix(acctKey, "vtx.account.") + ".heldFor.leaseapp." + strings.TrimPrefix(leaseKey, "vtx.leaseapp.")
	if !keyExists(t, ctx, conn, heldFor) {
		t.Fatalf("heldFor link %s not written", heldFor)
	}
}

// TestLoftspaceCreateAccount_LandlordSelfScope_RejectedUnmanagedUnit proves
// the manages probe confines the landlord: the lease sits on a unit they do
// NOT manage, so the open is refused AuthDenied without naming the unit, and
// no account is minted.
func TestLoftspaceCreateAccount_LandlordSelfScope_RejectedUnmanagedUnit(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerLandlordOpenCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "landlordopenother")

	seedIdentity(t, ctx, conn, ledgerLandlordID)
	tenantID := "BBLEDGERLLTENANTQQHJ"
	seedIdentity(t, ctx, conn, tenantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERLLQQTHERLEAS", tenantID)
	theirsID := "BBLEDGERLLQQTHERUNJT"
	theirs := seedUnitForLease(t, ctx, conn, theirsID, leaseKey)
	mineID := "BBLEDGERLLQQMJNEUNJT"
	seedVertex(t, ctx, conn, "vtx.unit."+mineID, "unit", map[string]any{})
	seedManages(t, ctx, conn, ledgerLandlordID, mineID)

	got, reply, acctKey := openAccountAsLandlord(t, ctx, conn, cp, cons, "landlordopenacct0002", leaseKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord LoftspaceCreateAccount on an UNMANAGED unit's lease = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") || !strings.Contains(reply.Error.Message, "does not manage the unit") {
		t.Fatalf("the unmanaged open rejected with %+v, want require_manages's AuthDenied", reply.Error)
	}
	if strings.Contains(reply.Error.Message, theirs) || strings.Contains(reply.Error.Message, theirsID) {
		t.Fatalf("the denial names the unit the lease sits on (%q)", reply.Error.Message)
	}
	if keyExists(t, ctx, conn, acctKey) {
		t.Fatalf("the denied open minted %s", acctKey)
	}
}

func seedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.identity." + id
	seedVertex(t, ctx, conn, key, "identity", map[string]any{})
	return key
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

// seedLeaseWithApplicant seeds a live leaseapp vertex plus its applicationFor
// link to applicantID — the ownership chain CreditAccount's self-scope branch
// walks (via the account's own heldFor link) to the lease, then this link to
// the caller's identity.
func seedLeaseWithApplicant(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseID, applicantID string) string {
	t.Helper()
	key := "vtx.leaseapp." + leaseID
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	lnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + applicantID
	seedLink(t, ctx, conn, lnk, key, "vtx.identity."+applicantID, "applicationFor", "applicationFor")
	return key
}

// TestCreditAccount_ConsumerSelfScope_Allowed proves a real resident can
// credit (pay down) THEIR OWN account: the account's heldFor lease resolves
// (via applicationFor) to the caller's own authContext.target identity.
func TestCreditAccount_ConsumerSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfok")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERSELFLEASEHJK", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditselfacctsetup1", leaseKey)

	// A landlord-recorded charge establishes the $1,500 owed the self-credit
	// below pays down — the balance cap (scripts.go) has nothing to verify
	// against on a freshly-opened, never-charged account.
	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditselfdebit000001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":150000,"memo":"July rent"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID := testutil.GenReqID("creditselfpay0000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":150000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-service CreditAccount outcome = %v, want Accepted", outcome)
	}
}

// TestCreditAccount_ConsumerSelfScope_RejectedOverBalance proves the
// self-credit amount is bounded by what the account actually owes — a
// resident cannot self-forgive debt by naming an amount larger than any
// landlord-recorded charge (scripts.go recomputes the balance from the
// account's own postedTo history; nothing on this platform verifies a
// self-submitted payment actually happened, so the amount itself is the
// attack surface, not just which account it targets).
func TestCreditAccount_ConsumerSelfScope_RejectedOverBalance(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfoverbal")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERSELFQVRLEASE", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditoverbalsetup01", leaseKey)

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditoverbaldebit01"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":50000,"memo":"July rent"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// $500 owed; self-credit claims $500,000 — must be rejected even though
	// ownership checks out.
	reqID := testutil.GenReqID("creditoverbalpay0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":50000000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("over-balance self-credit outcome = %v, want Rejected (AuthDenied)", outcome)
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
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERSELFNQBALEAS", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditnobalsetup0001", leaseKey)

	reqID := testutil.GenReqID("creditnobalpay000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":100}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
// account whose lease is NOT their own is rejected — self-service never lets
// one resident pay down another's balance.
func TestCreditAccount_ConsumerSelfScope_RejectedForOthersAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfother")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	otherApplicantID := "BBLEDGERQTHERLEASHJK"
	seedIdentity(t, ctx, conn, otherApplicantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERQTHERLEASEHJ", otherApplicantID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditotheracctsetup", leaseKey)

	// Not the applicant, so the resident proof fails and the vector exits
	// through the landlord branch -- which finds no manages link either.
	got, reply := submitSelfEntry(t, ctx, conn, cp, cons, "creditselfpay0000002", "CreditAccount", ledgerSelfConsumerKey, acctKey, 150000)
	if got != processor.OutcomeRejected {
		t.Fatalf("self-service CreditAccount on another's account outcome = %v, want Rejected (AuthDenied)", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") || !strings.Contains(reply.Error.Message, "neither holds nor manages") {
		t.Fatalf("another's account rejected with %+v, want the landlord branch's AuthDenied (\"neither holds nor manages\")", reply.Error)
	}
}

// TestLoftspaceRecordCharge_ResidentSelfScope_Rejected proves a RESIDENT may
// never charge their own lease: the cap doc carries LoftspaceRecordCharge:self,
// so step 3 admits the op and the SCRIPT's resident branch is what refuses it
// (AuthDenied). The lease sits on a unit nobody manages, so the landlord
// proof cannot answer for it either — the resident proof answers first
// regardless.
func TestLoftspaceRecordCharge_ResidentSelfScope_Rejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "debitselfdenied")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBLEDGERSELFDEBJTLEA", ledgerSelfConsumerID)
	seedUnitForLease(t, ctx, conn, "BBLEDGERSELFDEBJTUNJ", leaseKey)
	acctKey := createAccount(t, ctx, conn, cp, cons, "debitselfacctsetup01", leaseKey)

	got, reply := submitSelfEntry(t, ctx, conn, cp, cons, "debitselfpay00000001", "LoftspaceRecordCharge", ledgerSelfConsumerKey, acctKey, 50000)
	if got != processor.OutcomeRejected {
		t.Fatalf("resident self-scoped LoftspaceRecordCharge outcome = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") || !strings.Contains(reply.Error.Message, "a resident may only credit") {
		t.Fatalf("the resident charge rejected with %+v, want the RESIDENT branch's AuthDenied (\"a resident may only credit\"), not the landlord branch's", reply.Error)
	}
}
