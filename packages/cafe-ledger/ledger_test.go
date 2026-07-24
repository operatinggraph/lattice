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
	otherLedgerAcctKey := "vtx.account.BBOTHERLEDGERACCTHJK"
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

// TestDebitCreditAccount_PostEntries (test 2). DebitAccount/CreditAccount
// each mint a fresh transaction vertex (root {} — D5) + a .entry aspect +
// the postedTo link to the account; the account root is never touched
// (append-only ledger, no balance stored).
func TestDebitCreditAccount_PostEntries(t *testing.T) {
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

	// CreditAccount — a house-tab payment received.
	creditReqID := testutil.GenReqID("cafecreditpay0000001")
	creditEnv := &processor.OperationEnvelope{
		RequestID:     creditReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-05T09:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1850,"memo":"House tab payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
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
// TestDebitCreditAccount_PostEntries already covers (no tabRef at all).
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
