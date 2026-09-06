// cafe-ledger integration tests through the real install + Processor
// pipeline. External test package (cafeledger_test) so they exercise the
// public Lattice surface: seed the kernel, install rbac + identity + hygiene
// + orchestration-base + service-domain + lease-signing + cafe-ledger
// through the Processor, then submit the ops and assert the committed
// Core-KV shape + the emitted events.
package cafeledger_test

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
	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
)

const (
	ledgerActorID  = "BBCAFELEDGERACTHJKMN"
	ledgerActorKey = "vtx.identity." + ledgerActorID
	ledgerCapKey   = "cap.identity." + ledgerActorID

	// ledgerConsumerRoleID stands in for identity-domain's real `consumer`
	// role NanoID: this package's tests don't install identity-domain (only
	// rbac + hygiene via SetupPackageTestEnv), so lease-signing's
	// CreateLeaseApplication scope=self grant (GrantsTo: "consumer") needs a
	// role id registered directly.
	ledgerConsumerRoleID = "BBConsumerRoZeCafeMN"
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
			{OperationType: "CreateAccount", Scope: "any"},
			{OperationType: "DebitAccount", Scope: "any"},
			{OperationType: "CreditCafeAccount", Scope: "any"},
			{OperationType: "RefundCafeCharge", Scope: "any"},
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
	if _, err := inst.Install(ctx, cafeledger.Package); err != nil {
		t.Fatalf("install cafe-ledger: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, ledgerCapDoc())
	// CreditCafeAccount's workplace guard asks the GRAPH whether its caller is
	// root, so the cap doc's Roles claim is not enough on its own — without the
	// link this actor reads as an unprivileged caller with no worksAt anywhere
	// (testutil.SeedHoldsRole's doc comment).
	testutil.SeedHoldsRole(t, ctx, conn, ledgerActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

func newLedgerPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "cl-" + durable,
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

// seedLease seeds a live leaseapp vertex to hold a café account for.
func seedLease(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.leaseapp." + id
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	return key
}

// createAccount submits CreateAccount{leaseAppKey} and returns the account
// key — the account's own independently-minted NanoID, matching the
// deterministic nanoid.new() seed the test harness uses for the transaction
// DDL (never derived from the lease's own id).
func createAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "cafeaccount",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseAppKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.cafeaccount." + nanoIDFromRequestID(reqID)
}

// TestCreateAccount_MintsAccountHeldForLease (test 1). CreateAccount mints
// vtx.cafeaccount.<freshId> (root {} — D5, an id independent of the lease's
// own) + the leaseapp's .cafeLedgerAccount guard aspect + the heldFor link;
// a second call for the same lease that declares the guard aspect in reads
// conflicts on it (AccountAlreadyExists).
func TestCreateAccount_MintsAccountHeldForLease(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "create")

	leaseKey := seedLease(t, ctx, conn, "BBCAFELEASEHJKMNPQRS")
	leaseID := "BBCAFELEASEHJKMNPQRS"
	guardKey := leaseKey + ".cafeLedgerAccount"

	if keyExists(t, ctx, conn, guardKey) {
		t.Fatalf("guard aspect must not exist before CreateAccount")
	}

	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreateacct000001", leaseKey)
	acctID := acctKey[len("vtx.cafeaccount."):]
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

	heldForLnk := "lnk.cafeaccount." + acctID + ".heldFor.leaseapp." + leaseID
	if !keyExists(t, ctx, conn, heldForLnk) {
		t.Fatalf("heldFor link must exist: %s", heldForLnk)
	}

	// A second CreateAccount for the SAME lease, declaring the now-existing
	// guard aspect in reads, conflicts on it (AccountAlreadyExists — the
	// create-only write is the guard).
	dup := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafecreateacct000002"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:05:00Z",
		Class:         "cafeaccount",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseKey, guardKey}},
	}
	testutil.PublishOp(t, conn, dup)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateAccount_GuardDoesNotCollideWithLoftspaceLedger — the design
// doc's key wrinkle: café anchors to the SAME leaseapp loftspace-ledger
// already anchors to, so an existing (simulated) loftspace-ledger
// `.ledgerAccount` guard aspect on that leaseapp must not block, alias, or
// be overwritten by cafe-ledger's own `.cafeLedgerAccount` guard — the two
// local names are vertical-prefixed distinctly for exactly this reason.
func TestCreateAccount_GuardDoesNotCollideWithLoftspaceLedger(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "guardcollision")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDUALGUARDHJKMN")
	otherLedgerAcctKey := "vtx.account.BBoTHERLEDGERACCTHJK"
	seedVertex(t, ctx, conn, otherLedgerAcctKey, "account", map[string]any{})
	// Simulate loftspace-ledger's own guard aspect already present on this
	// same leaseapp, at its bare (non-prefixed) local name.
	loftspaceGuardKey := leaseKey + ".ledgerAccount"
	seedVertex(t, ctx, conn, loftspaceGuardKey, "ledgerAccountGuard", map[string]any{"accountKey": otherLedgerAcctKey})

	acctKey := createAccount(t, ctx, conn, cp, cons, "cafedualguard000001", leaseKey)

	cafeGuardKey := leaseKey + ".cafeLedgerAccount"
	if !keyExists(t, ctx, conn, cafeGuardKey) {
		t.Fatalf("cafeLedgerAccount guard aspect must exist: %s", cafeGuardKey)
	}
	cafeGuardDoc := readDoc(t, ctx, conn, cafeGuardKey)
	if got, _ := cafeGuardDoc["class"].(string); got != "cafeLedgerAccountGuard" {
		t.Fatalf("cafeLedgerAccount guard class = %q, want cafeLedgerAccountGuard", got)
	}
	cafeGuardData, _ := cafeGuardDoc["data"].(map[string]any)
	if got, _ := cafeGuardData["accountKey"].(string); got != acctKey {
		t.Fatalf("cafeLedgerAccount guard accountKey = %q, want %q", got, acctKey)
	}

	// loftspace-ledger's pre-existing guard aspect must be untouched.
	loftspaceGuardDoc := readDoc(t, ctx, conn, loftspaceGuardKey)
	loftspaceGuardData, _ := loftspaceGuardDoc["data"].(map[string]any)
	if got, _ := loftspaceGuardData["accountKey"].(string); got != otherLedgerAcctKey {
		t.Fatalf("pre-existing ledgerAccount guard accountKey = %q, want %q (must be undisturbed)", got, otherLedgerAcctKey)
	}
}

// TestCreateAccount_UnknownLease rejects an account opened against a
// non-existent lease (no-orphan invariant).
func TestCreateAccount_UnknownLease(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownlease")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafecreateunknown01"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "cafeaccount",
		Payload:       json.RawMessage(`{"leaseAppKey":"vtx.leaseapp.BBABSENTLEASEHJKMNPQ"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.leaseapp.BBABSENTLEASEHJKMNPQ"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitCreditCafeAccount_PostEntries (test 2). DebitAccount/CreditCafeAccount
// each mint a fresh transaction vertex (root {} — D5) + a .entry aspect +
// the postedTo link to the account; the account root is never touched
// (append-only ledger, no balance stored).
func TestDebitCreditCafeAccount_PostEntries(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "postentries")

	leaseKey := seedLease(t, ctx, conn, "BBCAFELEASEPSTHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreateacctpost01", leaseKey)

	debitReqID := testutil.GenReqID("cafedebittab00000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850,"memo":"Settled tab - table 4"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	debitTxKey := "vtx.cafetransaction." + nanoIDFromRequestID(debitReqID)
	entryDoc := readDoc(t, ctx, conn, debitTxKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "debit" {
		t.Fatalf("entry.type = %q, want debit", got)
	}
	if got, _ := entryData["amountCents"].(float64); got != 1850 {
		t.Fatalf("entry.amountCents = %v, want 1850", got)
	}
	if got, _ := entryData["memo"].(string); got != "Settled tab - table 4" {
		t.Fatalf("entry.memo = %q, want %q", got, "Settled tab - table 4")
	}

	txDoc := readDoc(t, ctx, conn, debitTxKey)
	if d, _ := txDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("transaction root data must stay minimal ({}) after post, got %v", d)
	}

	acctID := acctKey[len("vtx.cafeaccount."):]
	postedToLnk := "lnk.cafetransaction." + nanoIDFromRequestID(debitReqID) + ".postedTo.cafeaccount." + acctID
	if !keyExists(t, ctx, conn, postedToLnk) {
		t.Fatalf("postedTo link must exist: %s", postedToLnk)
	}

	// The account root is never mutated by a debit — append-only ledger.
	acctDoc := readDoc(t, ctx, conn, acctKey)
	if d, _ := acctDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("account root data must stay minimal ({}) after a debit — the ledger is append-only, got %v", d)
	}

	// CreditCafeAccount — a house-tab payment received.
	creditReqID := testutil.GenReqID("cafecreditpay0000001")
	creditEnv := &processor.OperationEnvelope{
		RequestID:     creditReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-05T09:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850,"memo":"House tab payment"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{acctKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	creditTxKey := "vtx.cafetransaction." + nanoIDFromRequestID(creditReqID)
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
		RequestID:     testutil.GenReqID("cafedebitunknown001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"vtx.cafeaccount.BBABSENTACCTHJKMNPQR","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.cafeaccount.BBABSENTACCTHJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_TabRefWritesSettlesLink (cafe-domain Settle consumer,
// mirroring semantic-contracts' clauseRef test): a DebitAccount carrying a
// live tabRef writes the settles audit link (transaction→tab) alongside the
// normal postedTo link, on top of the byte-for-byte-unaffected plain path
// TestDebitCreditCafeAccount_PostEntries already covers (no tabRef at all).
func TestDebitAccount_TabRefWritesSettlesLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "tabref")

	leaseKey := seedLease(t, ctx, conn, "BBCAFELEASETABHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreateaccttab001", leaseKey)
	tabKey := "vtx.tab.BBCAFETABREFHJKMNPQR"
	seedVertex(t, ctx, conn, tabKey, "tab", map[string]any{})

	debitReqID := testutil.GenReqID("cafedebittabref00001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":950,"tabRef":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, tabKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	tabID := tabKey[len("vtx.tab."):]
	settlesLnk := "lnk.cafetransaction." + nanoIDFromRequestID(debitReqID) + ".settles.tab." + tabID
	if !keyExists(t, ctx, conn, settlesLnk) {
		t.Fatalf("settles link must exist: %s", settlesLnk)
	}
}

// TestDebitAccount_UnknownTabRefRejected rejects a tabRef naming an absent
// tab (no-orphan invariant on the settles link, mirroring UnknownAccount).
func TestDebitAccount_UnknownTabRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknowntabref")

	leaseKey := seedLease(t, ctx, conn, "BBCAFELEASEBADTABHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreateacctbadtb1", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafedebitbadtabref01"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":950,"tabRef":"vtx.tab.BBABSENTTABHJKMNPQR"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, "vtx.tab.BBABSENTTABHJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_NonPositiveAmountRejected rejects amountCents <= 0
// (InvalidArgument).
func TestDebitAccount_NonPositiveAmountRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "badamount")

	leaseKey := seedLease(t, ctx, conn, "BBCAFELEASEBADHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreateacctbad001", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafedebitbadamount1"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// --- RefundCafeCharge (front-desk correction of a posted charge) -----------

// postDebit posts a charge to acctKey as the operator and returns its
// transaction key — the charge every refund vector below reverses. tabRef is
// optional; supplied, it writes the settles link a real playbook-posted café
// charge carries.
func postDebit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey string, amountCents int, memo string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-10T08:00:00Z",
		Class:         "cafetransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
			strconv.Itoa(amountCents) + `,"memo":"` + memo + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.cafetransaction." + nanoIDFromRequestID(reqID)
}

// refundEnv builds one RefundCafeCharge submission and the transaction key it
// will mint (deterministic from the request id), declaring exactly the reads
// and enumerations the descriptor promises. authContextTarget is the raw
// client-supplied hint — the script refuses its presence outright, which one
// vector below exercises.
func refundEnv(label, actorKey, acctKey, reversesRef string, amountCents int,
	authContextTarget string) (*processor.OperationEnvelope, string) {
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "RefundCafeCharge",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-11T09:00:00Z",
		Class:         "cafetransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","reversesRef":"` + reversesRef +
			`","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"Wrong item charged"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{acctKey, reversesRef, reversesRef + ".entry"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: reversesRef, Relation: "postedTo", Direction: "out"},
			},
		},
	}
	if authContextTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: authContextTarget}
	}
	return env, "vtx.cafetransaction." + nanoIDFromRequestID(reqID)
}

// refundAs submits one RefundCafeCharge, drives it, asserts the outcome and
// returns the refund's own transaction key.
func refundAs(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actorKey, acctKey, reversesRef string, amountCents int,
	authContextTarget string, want processor.MessageOutcome) string {
	t.Helper()
	env, txKey := refundEnv(label, actorKey, acctKey, reversesRef, amountCents, authContextTarget)
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return txKey
}

// chargeTally reads a charge's own .entry aspect back — the refund ceiling
// lives there, on the charge, not on the account.
func chargeTally(t *testing.T, ctx context.Context, conn *substrate.Conn, chargeKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, chargeKey+".entry")
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.entry carries no data", chargeKey)
	}
	return data
}

// TestRefundCafeCharge_PostsCreditAndReversesLink is the positive vector every
// refusal below is measured against: a posted charge is refunded in full, and
// the commit carries the ordinary credit shape (transaction + .entry{credit} +
// postedTo) PLUS the reverses link back to the charge. The link is the refund's
// whole identity — the entry itself is indistinguishable from a payment, which
// is exactly why the correction has to be recorded as a relationship.
func TestRefundCafeCharge_PostsCreditAndReversesLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundok")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFUNDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefundacct000001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefunddebit00001", acctKey, 900, "Settled tab")

	refundKey := refundAs(t, ctx, conn, cp, cons, "caferefundpost000001",
		ledgerActorKey, acctKey, chargeKey, 900, "", processor.OutcomeAccepted)

	entryDoc := readDoc(t, ctx, conn, refundKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "credit" {
		t.Fatalf("refund entry.type = %q, want credit (a refund posts an ordinary credit)", got)
	}
	if got, _ := entryData["amountCents"].(float64); got != 900 {
		t.Fatalf("refund entry.amountCents = %v, want 900", got)
	}

	acctID := acctKey[len("vtx.cafeaccount."):]
	refundID := refundKey[len("vtx.cafetransaction."):]
	chargeID := chargeKey[len("vtx.cafetransaction."):]
	if !keyExists(t, ctx, conn, "lnk.cafetransaction."+refundID+".postedTo.cafeaccount."+acctID) {
		t.Fatalf("refund must post to the account like any other entry")
	}
	reversesLnk := "lnk.cafetransaction." + refundID + ".reverses.cafetransaction." + chargeID
	if !keyExists(t, ctx, conn, reversesLnk) {
		t.Fatalf("reverses link must exist: %s", reversesLnk)
	}
	lnkDoc := readDoc(t, ctx, conn, reversesLnk)
	if got, _ := lnkDoc["sourceVertex"].(string); got != refundKey {
		t.Fatalf("reverses sourceVertex = %q, want the refund %q (the later-arriving vertex is the source)", got, refundKey)
	}
	if got, _ := lnkDoc["targetVertex"].(string); got != chargeKey {
		t.Fatalf("reverses targetVertex = %q, want the charge %q", got, chargeKey)
	}
}

// TestRefundCafeCharge_NonDebitRefRejected: a refund may only reverse a CHARGE.
// Reversing a credit would let one refund reverse another — each credit
// compounding the last into money the café never took.
func TestRefundCafeCharge_NonDebitRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundnondebit")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFNQNDEBTLEAS")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefndbtacct00001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefndbtdebit0001", acctKey, 900, "Settled tab")

	// The refund that succeeds is the positive vector; the SECOND refund, aimed
	// at that first refund's own credit entry, is the one that must fail.
	refundKey := refundAs(t, ctx, conn, cp, cons, "caferefndbtrefund001",
		ledgerActorKey, acctKey, chargeKey, 400, "", processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, "caferefndbtoncredit1",
		ledgerActorKey, acctKey, refundKey, 100, "", processor.OutcomeRejected)
}

// TestRefundCafeCharge_OtherAccountsChargeRejected: the reversed charge must be
// postedTo the same account being credited. Without that hop a staffer could
// aim a refund on one resident's account at another resident's charge and
// credit money against a debit that was never theirs.
func TestRefundCafeCharge_OtherAccountsChargeRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundotheracct")

	leaseA := seedLease(t, ctx, conn, "BBCAFEREFQTHRLEASEAA")
	leaseB := seedLease(t, ctx, conn, "BBCAFEREFQTHRLEASEBB")
	acctA := createAccount(t, ctx, conn, cp, cons, "caferefotheracctaaa1", leaseA)
	acctB := createAccount(t, ctx, conn, cp, cons, "caferefotheracctbbb1", leaseB)
	chargeB := postDebit(t, ctx, conn, cp, cons, "caferefotherdebitbb1", acctB, 900, "Settled tab")

	// Positive vector first: the same charge refunds fine against its OWN
	// account, so the rejection below is the account mismatch and not a guard
	// that denies every refund.
	refundAs(t, ctx, conn, cp, cons, "caferefotherownacct1",
		ledgerActorKey, acctB, chargeB, 100, "", processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, "caferefothercrossacc",
		ledgerActorKey, acctA, chargeB, 100, "", processor.OutcomeRejected)
}

// TestRefundCafeCharge_OverRefundRejected: a single refund may not exceed the
// charge it reverses.
func TestRefundCafeCharge_OverRefundRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundover")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFQVERLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefoveracct00001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefoverdebit0001", acctKey, 900, "Settled tab")

	refundAs(t, ctx, conn, cp, cons, "caferefoverexceeds01",
		ledgerActorKey, acctKey, chargeKey, 901, "", processor.OutcomeRejected)
	// Exactly the charge is fine — the cap is `>`, not `>=`.
	refundAs(t, ctx, conn, cp, cons, "caferefoverexact0001",
		ledgerActorKey, acctKey, chargeKey, 900, "", processor.OutcomeAccepted)
}

// TestRefundCafeCharge_CumulativeOverRefundRejected is the vector a per-refund
// cap alone would pass: two partial refunds, each individually well under the
// charge, whose SUM runs past it. The ceiling is the charge minus everything
// already given back, so the second one has to fail — and the third, sized to
// the true remainder, has to succeed, or the test would pass just as well
// against a guard that denied every second refund.
func TestRefundCafeCharge_CumulativeOverRefundRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundcumulative")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFCUMLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefcumacct000001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefcumdebit00001", acctKey, 1000, "Settled tab")

	refundAs(t, ctx, conn, cp, cons, "caferefcumfirst00001",
		ledgerActorKey, acctKey, chargeKey, 600, "", processor.OutcomeAccepted)
	// 600 + 500 = 1100 > 1000, though 500 alone is under the charge.
	refundAs(t, ctx, conn, cp, cons, "caferefcumsecond0001",
		ledgerActorKey, acctKey, chargeKey, 500, "", processor.OutcomeRejected)
	// The true remainder still refunds.
	refundAs(t, ctx, conn, cp, cons, "caferefcumthird00001",
		ledgerActorKey, acctKey, chargeKey, 400, "", processor.OutcomeAccepted)
}

// TestRefundCafeCharge_SelfScopedSubmitRejected: a refund is never self-scoped.
// The resident who owes the charge does not get to decide it was wrong — and
// were the submit to fall through to post_entry's resident branch, they would
// be minting credits against their own charges, capped only by what they owe.
func TestRefundCafeCharge_SelfScopedSubmitRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundselfscoped")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEREFSELFLEASEHJ", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefselfacct00001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefselfdebit0001", acctKey, 900, "Settled tab")

	// The operator's own refund of the same charge proves the vector is
	// otherwise well-formed, so the rejection below is the target and nothing
	// else about the submission.
	refundAs(t, ctx, conn, cp, cons, "caferefselfoperator1",
		ledgerActorKey, acctKey, chargeKey, 100, "", processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, "caferefselftargeted1",
		ledgerActorKey, acctKey, chargeKey, 100, ledgerActorKey, processor.OutcomeRejected)
}

// TestRefundCafeCharge_TallyLandsOnTheChargeItReverses reads back what a refund
// actually writes to the charge: a refundedCents total, and nothing else
// disturbed. That total IS the ceiling every later refund is measured against,
// so the shape matters twice over — a tally that netted itself off the charge's
// own amountCents would halve what the statement says the resident was charged,
// and one that dropped the memo or postedAt would rewrite a posted line of a
// permanent ledger to record a correction about it.
func TestRefundCafeCharge_TallyLandsOnTheChargeItReverses(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundtally")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFTALLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafereftallacct00001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafereftalldebit0001", acctKey, 1000, "Settled tab")

	before := chargeTally(t, ctx, conn, chargeKey)
	if _, ok := before["refundedCents"]; ok {
		t.Fatalf("an unrefunded charge carries no tally yet, got %v", before)
	}

	refundAs(t, ctx, conn, cp, cons, "cafereftallfirst0001",
		ledgerActorKey, acctKey, chargeKey, 600, "", processor.OutcomeAccepted)

	after := chargeTally(t, ctx, conn, chargeKey)
	if got, _ := after["refundedCents"].(float64); got != 600 {
		t.Fatalf("charge refundedCents = %v, want 600", got)
	}
	if got, _ := after["amountCents"].(float64); got != 1000 {
		t.Fatalf("charge amountCents = %v, want the charge's own 1000 — the tally records what was given back, it does not net it off", got)
	}
	if got, _ := after["type"].(string); got != "debit" {
		t.Fatalf("charge type = %q, want debit", got)
	}
	if got, _ := after["memo"].(string); got != "Settled tab" {
		t.Fatalf("charge memo = %q, want it carried across the tally upsert", got)
	}
	if got, _ := after["postedAt"].(string); got != "2026-07-10T08:00:00Z" {
		t.Fatalf("charge postedAt = %q, want it carried across the tally upsert", got)
	}

	// A second partial refund accumulates onto the same tally rather than
	// replacing it — the arithmetic the cumulative cap depends on.
	refundAs(t, ctx, conn, cp, cons, "cafereftallsecond001",
		ledgerActorKey, acctKey, chargeKey, 400, "", processor.OutcomeAccepted)
	full := chargeTally(t, ctx, conn, chargeKey)
	if got, _ := full["refundedCents"].(float64); got != 1000 {
		t.Fatalf("charge refundedCents after two partial refunds = %v, want 1000", got)
	}
}

// driveConcurrently fetches n pending operations and runs them through the
// commit path SIMULTANEOUSLY, returning their outcomes. It exists because
// DriveOne is strictly serial: a second refund driven after the first always
// re-reads the tally the first wrote, so the arithmetic cap alone refuses it
// and the compare-and-set that pins the tally is never exercised. Only
// overlapping hydrations put two refunds on the same revision of the charge —
// the race a live front desk with two terminals actually produces.
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

// TestRefundCafeCharge_ConcurrentRefundsCannotJointlyExceedTheCharge is the
// race the ceiling exists to survive: two full refunds of the same charge,
// each legal on its own, hydrated together and committed together. Exactly one
// may win. Which one, and by which refusal, is not fixed — the loser is turned
// away either by the compare-and-set on the tally it read (if the hydrations
// overlapped) or by the tally itself (if they did not) — but the outcome is:
// one refund posted, the charge's tally equal to that one refund, never both.
func TestRefundCafeCharge_ConcurrentRefundsCannotJointlyExceedTheCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundrace")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFRACELEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefraceacct00001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefracedebit0001", acctKey, 1000, "Settled tab")

	envA, refundA := refundEnv("caferefracefirst0001", ledgerActorKey, acctKey, chargeKey, 1000, "")
	envB, refundB := refundEnv("caferefracesecond001", ledgerActorKey, acctKey, chargeKey, 1000, "")
	testutil.PublishOp(t, conn, envA)
	testutil.PublishOp(t, conn, envB)

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	accepted := 0
	for _, o := range outcomes {
		if o == processor.OutcomeAccepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("outcomes = %v, want exactly one Accepted: two full refunds of one charge may never both post", outcomes)
	}

	tally := chargeTally(t, ctx, conn, chargeKey)
	if got, _ := tally["refundedCents"].(float64); got != 1000 {
		t.Fatalf("charge refundedCents = %v, want exactly one refund's 1000 (2000 would mean both committed against the same read)", got)
	}

	posted := 0
	for _, k := range []string{refundA, refundB} {
		if keyExists(t, ctx, conn, k) {
			posted++
		}
	}
	if posted != 1 {
		t.Fatalf("%d refund transactions exist, want 1 — the loser must leave no entry behind", posted)
	}
}

// TestRefundCafeCharge_MalformedReversesRefRejected: reversesRef must name a
// cafetransaction, and the TYPE segment is what says so. The vector points it
// at a live vertex of another type, so what refuses is the key's shape and not
// its absence — a refund allowed to anchor on an arbitrary vertex would write
// a reverses link into a class pair no consumer of the lens can read.
func TestRefundCafeCharge_MalformedReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundbadreftype")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFBADREFLEASE")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefbadrefacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefbadrefdebit", acctKey, 900, "Settled tab")

	// The well-formed ref refunds, so the rejection below is the type segment
	// and nothing else about this account or this actor.
	refundAs(t, ctx, conn, cp, cons, "caferefbadrefpositiv",
		ledgerActorKey, acctKey, chargeKey, 100, "", processor.OutcomeAccepted)

	tabKey := "vtx.tab.BBCAFEREFBADTABHJKMN"
	seedVertex(t, ctx, conn, tabKey, "tab", nil)
	refundAs(t, ctx, conn, cp, cons, "caferefbadrefwrongty",
		ledgerActorKey, acctKey, tabKey, 100, "", processor.OutcomeRejected)
}

// TestRefundCafeCharge_AbsentReversesRefRejected: a refund against a charge
// that does not exist has nothing to correct. The ref is declared in reads
// exactly as the front desk's descriptor declares it, so the refusal is the
// platform's own dependence check on a declared read the script consults —
// which is why the script does not invent an "unknown transaction" of its own
// for this shape.
func TestRefundCafeCharge_AbsentReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundabsentref")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFGQNELEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefabsentacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefabsentdebit", acctKey, 900, "Settled tab")

	refundAs(t, ctx, conn, cp, cons, "caferefabsentpositiv",
		ledgerActorKey, acctKey, chargeKey, 100, "", processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, "caferefabsentmissing",
		ledgerActorKey, acctKey, "vtx.cafetransaction.BBCAFEREFABSENTTXHJK", 100, "",
		processor.OutcomeRejected)
}

// TestCreditCafeAccount_ReversesRefRejected: reversesRef belongs to
// RefundCafeCharge, and every other entry refuses it rather than ignoring it.
// A caller that sends one to CreditCafeAccount means to record a correction;
// dropping the field silently would post a plain payment, leaving the
// statement saying the resident handed money over at the counter.
func TestCreditCafeAccount_ReversesRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditrevref")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECREDREVLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreditrevacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafecreditrevdebit", acctKey, 900, "Settled tab")

	creditEnv := func(label, payload string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "CreditCafeAccount",
			Actor:         ledgerActorKey,
			SubmittedAt:   "2026-07-12T09:00:00Z",
			Class:         "cafetransaction",
			Payload:       json.RawMessage(payload),
			ContextHint: &processor.ContextHint{
				Reads: []string{acctKey, chargeKey, chargeKey + ".entry"},
				Enumerations: []processor.EnumerationHint{
					{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
				},
			},
		}
	}

	// The same payment without the field is accepted, so the rejection below
	// is the field and not the amount, the account or the actor.
	testutil.PublishOp(t, conn, creditEnv("cafecreditrevrefokay",
		`{"accountKey":"`+acctKey+`","amountCents":100}`))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	testutil.PublishOp(t, conn, creditEnv("cafecreditrevrefbad",
		`{"accountKey":"`+acctKey+`","amountCents":100,"reversesRef":"`+chargeKey+`"}`))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestRefundCafeCharge_TabRefRejected is the mirror of the refusal above: a
// tabRef sent to RefundCafeCharge is refused rather than ignored. The field is
// DebitAccount's — a refund settles no tab — and a caller sending one means
// "give back the charge that settled this tab". Silently dropping it commits a
// credit with no settles link and no relation to the tab at all, which is the
// same disagreement between what was asked for and what the ledger records.
func TestRefundCafeCharge_TabRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundtabref")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFTABREFLEASE")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafereftabrefacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafereftabrefdebit", acctKey, 900, "Settled tab")
	tabKey := "vtx.tab.BBCAFEREFTABHJKMNPQR"
	seedVertex(t, ctx, conn, tabKey, "tab", nil)

	refundAs(t, ctx, conn, cp, cons, "cafereftabrefpositiv",
		ledgerActorKey, acctKey, chargeKey, 100, "", processor.OutcomeAccepted)

	// The live tab is what makes this the FIELD's refusal: an absent one would
	// reject on the declared read alone, whichever op it was sent to.
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafereftabrefrefused"),
		Lane:          processor.LaneDefault,
		OperationType: "RefundCafeCharge",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-13T09:00:00Z",
		Class:         "cafetransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","reversesRef":"` + chargeKey +
			`","amountCents":100,"tabRef":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{acctKey, chargeKey, chargeKey + ".entry", tabKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: chargeKey, Relation: "postedTo", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

const (
	ledgerTaskActorID  = "BBCAFETASKACTQRHJKMN"
	ledgerTaskActorKey = "vtx.identity." + ledgerTaskActorID
	ledgerTaskActorCap = "cap.ephemeral.identity." + ledgerTaskActorID
	ledgerTaskKey      = "vtx.task.BBCAFEREFTASKHJKMNPQ"
)

// ledgerTaskCapDoc holds RefundCafeCharge through an ephemeral TASK grant. The
// task branch reads the disjoint cap.ephemeral.identity.<id> key (Contract #10
// §10.7), and a grant matched there is the OTHER path that sets
// op.authTargetValidated — the bit workplace_exempt discharges confinement on.
func ledgerTaskCapDoc(target string) *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		Key:                    ledgerTaskActorCap,
		Actor:                  ledgerTaskActorKey,
		Version:                "1.0",
		ProjectedAt:            "2026-07-14T00:00:00Z",
		ProjectedFromRevisions: map[string]uint64{ledgerTaskActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{
			{
				Source:        "task",
				TaskKey:       ledgerTaskKey,
				OperationType: "RefundCafeCharge",
				Target:        target,
				ExpiresAt:     time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			},
		},
		Roles: []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// TestRefundCafeCharge_ValidatedTargetSubmitRejected drives the refusal from
// the OTHER side of op.authTargetValidated: a task's ephemeral grant, matched
// on the disjoint capability key, authorizes without any standing role and
// exempts its holder from the workplace walk. A refund reaching post_entry that
// way would arrive both unconfined and on the resident-credit branch, so the
// task path has to stop at the same refusal a self-scoped submit does.
func TestRefundCafeCharge_ValidatedTargetSubmitRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundtaskgrant")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFTASKLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafereftaskacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafereftaskdebit", acctKey, 900, "Settled tab")

	seedIdentity(t, ctx, conn, ledgerTaskActorID)
	testutil.SeedCapDoc(t, ctx, conn, ledgerTaskCapDoc(acctKey))

	// The operator's own standing refund of the same charge proves the vector
	// is otherwise well-formed.
	refundAs(t, ctx, conn, cp, cons, "cafereftaskpositive",
		ledgerActorKey, acctKey, chargeKey, 100, "", processor.OutcomeAccepted)

	env, _ := refundEnv("cafereftaskgrantpath", ledgerTaskActorKey, acctKey, chargeKey, 100, acctKey)
	env.AuthContext.Task = ledgerTaskKey
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("outcome = %v, want rejected: a task-authorized refund is exempt from the workplace walk", outcome)
	}
	// The reason matters: a step-3 denial would mean the grant never matched
	// and the script was never asked, which proves nothing about the guard.
	if reply == nil || reply.Error == nil {
		t.Fatalf("rejected reply carries no error: %+v", reply)
	}
	if !strings.Contains(reply.Error.Message, "front-desk act") {
		t.Fatalf("rejection reason = %q, want the script's own refusal — a step-3 denial would mean the ephemeral grant never matched and the guard was never reached", reply.Error.Message)
	}
}

// --- CreditCafeAccount consumer scope=self (resident self-pay) -------------

const (
	ledgerSelfConsumerID  = "BBCAFESELFCQNSMRHJKN"
	ledgerSelfConsumerKey = "vtx.identity." + ledgerSelfConsumerID
	ledgerSelfConsumerCap = "cap.identity." + ledgerSelfConsumerID
)

// ledgerSelfConsumerCapDoc grants the platform permission CreditCafeAccount's
// scope=self branch checks — mirrors loftspace-ledger's ledgerSelfConsumerCapDoc.
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
			{OperationType: "CreditCafeAccount", Scope: "self"},
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
// link to applicantID — the ownership chain CreditCafeAccount's self-scope
// branch walks (via the account's own heldFor link) to the lease, then this
// link to the caller's identity.
func seedLeaseWithApplicant(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseID, applicantID string) string {
	t.Helper()
	key := "vtx.leaseapp." + leaseID
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	lnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + applicantID
	seedLink(t, ctx, conn, lnk, key, "vtx.identity."+applicantID, "applicationFor", "applicationFor")
	return key
}

// TestCreditCafeAccount_ConsumerSelfScope_Allowed proves a real resident can
// credit (pay down) THEIR OWN account: the account's heldFor lease resolves
// (via applicationFor) to the caller's own authContext.target identity.
func TestCreditCafeAccount_ConsumerSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfok")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFESELFLEASEHJKMN", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditselfacctsetup1", leaseKey)

	// A front-desk-recorded charge establishes the $18.50 owed the self-credit
	// below pays down — the balance cap (scripts.go) has nothing to verify
	// against on a freshly-opened, never-charged account.
	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditselfdebit000001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850,"memo":"Settled tab"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	reqID := testutil.GenReqID("creditselfpay0000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-service CreditCafeAccount outcome = %v, want Accepted", outcome)
	}
}

// TestCreditCafeAccount_ConsumerSelfScope_RejectedOverBalance proves the
// self-credit amount is bounded by what the account actually owes — a
// resident cannot self-forgive debt by naming an amount larger than any
// front-desk-recorded charge (scripts.go recomputes the balance from the
// account's own postedTo history; nothing on this platform verifies a
// self-submitted payment actually happened, so the amount itself is the
// attack surface, not just which account it targets).
func TestCreditCafeAccount_ConsumerSelfScope_RejectedOverBalance(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfoverbal")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFESELFQVRLEASEHJ", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditoverbalsetup01", leaseKey)

	debitEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditoverbaldebit01"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-08T08:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850,"memo":"Settled tab"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	// $18.50 owed; self-credit claims $18,500 — must be rejected even though
	// ownership checks out.
	reqID := testutil.GenReqID("creditoverbalpay0001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("over-balance self-credit outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestCreditCafeAccount_ConsumerSelfScope_RejectedNoBalance proves a
// self-credit against a freshly-opened account (never charged, nothing owed)
// is rejected — there is nothing to pay down.
func TestCreditCafeAccount_ConsumerSelfScope_RejectedNoBalance(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfnobal")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFESELFNQBALEASEH", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditnobalsetup0001", leaseKey)

	reqID := testutil.GenReqID("creditnobalpay000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:00:00Z",
		Class:         "cafetransaction",
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

// TestCreditCafeAccount_ConsumerSelfScope_RejectedForOthersAccount proves a
// consumer satisfying step 3 (authContext.target == actor) but naming an
// account whose lease is NOT their own is rejected — self-service never lets
// one resident pay down another's balance.
func TestCreditCafeAccount_ConsumerSelfScope_RejectedForOthersAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfother")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	otherApplicantID := "BBCAFEQTHERAPPLCTHJK"
	seedIdentity(t, ctx, conn, otherApplicantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEQTHERLEASEHJKM", otherApplicantID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditotheracctsetup", leaseKey)

	reqID := testutil.GenReqID("creditselfpay0000002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:05:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-service CreditCafeAccount on another's account outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestCreditCafeAccount_ConsumerSelfScope_DebitStaysStaffOnly proves the
// self-scope grant does not leak to DebitAccount: even a caller who
// legitimately owns the lease cannot self-charge it (permissions.go grants no
// scope=self DebitAccount, and post_entry's own branch fails closed for a
// debit regardless).
func TestCreditCafeAccount_ConsumerSelfScope_DebitStaysStaffOnly(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "debitselfdenied")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFESELFDEBTLEASEH", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "debitselfacctsetup01", leaseKey)

	reqID := testutil.GenReqID("debitselfpay00000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:10:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":500}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-scoped DebitAccount outcome = %v, want Rejected (no matching grant)", outcome)
	}
}
