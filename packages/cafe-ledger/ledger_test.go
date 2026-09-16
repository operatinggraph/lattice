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
			{OperationType: "PayoutCafeCredit", Scope: "any"},
			// Deliberately the SAME grant Weaver holds for EvaluateCafeArrears
			// (arrearsWeaverCapDoc below). That is what makes the forged-send
			// vector attributable: the refusal can only come from the script's
			// own actor guard, never from a missing or narrower grant.
			{OperationType: "EvaluateCafeArrears", Scope: "any"},
			// The bridge's service actor is operator-equivalent, and this stands
			// in for it: the replyOp is granted to operator/Scope:"any" by the
			// package (permissions.go), so step 3 authorizes any operator that
			// submits it. Everything that constrains WHAT such a submission can
			// touch lives in the script's own validation of externalRef.
			{OperationType: "RecordCafeArrearsReminderNotification", Scope: "any"},
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
	testutil.SeedCapDoc(t, ctx, conn, arrearsWeaverCapDoc())
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

// debitHint is the contextHint a DebitAccount submission carries, mirroring
// cafe-domain's cafeTabSettlement missing_charge dispatch (targets.go): the
// account (plus any extra vertex the payload names, e.g. tabRef) and the
// account's absence-tolerant .balance aspect. No postedTo walk — a charge never
// backfills a legacy account's balance, only a payment does.
//
// .balance is declared, not incidental: the declaration is what auto-conditions
// the update post_entry emits for it on the revision it hydrated at
// (Contract #3 §3.2). The script's own derive_reads(op) declares the same key
// whatever a submitter sends, so these fixtures mirror the dispatchers rather
// than supply the guarantee.
func debitHint(acctKey string, extraReads ...string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         append([]string{acctKey}, extraReads...),
		OptionalReads: []string{acctKey + ".balance"},
	}
}

// staffCreditHint is the contextHint a scope=any CreditCafeAccount submission
// carries — debitHint's reads plus the holdsRole walk workplace_exempt's
// operator short-circuit runs and the bounded postedTo walk that backfills
// .balance on a legacy account's first payment, exactly as the descriptor
// declares them (opmetas.go).
func staffCreditHint(actorKey, acctKey string) *processor.ContextHint {
	h := debitHint(acctKey)
	h.Enumerations = []processor.EnumerationHint{
		{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
		{Hub: acctKey, Relation: "postedTo", Direction: "in"},
	}
	return h
}

// selfCreditHint is the contextHint a resident's scope=self CreditCafeAccount
// submission carries. No holdsRole walk: workplace_exempt short-circuits on
// op.authTargetValidated, so the operator probe never runs on this path. The
// legacy-backfill postedTo walk stays — a resident's payment reaches it on the
// same terms a staffer's does.
func selfCreditHint(acctKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{acctKey},
		OptionalReads: []string{acctKey + ".balance"},
		Enumerations: []processor.EnumerationHint{
			{Hub: acctKey, Relation: "postedTo", Direction: "in"},
		},
	}
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
		ContextHint:   debitHint(acctKey),
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
		ContextHint:   staffCreditHint(ledgerActorKey, acctKey),
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
		ContextHint:   debitHint("vtx.cafeaccount.BBABSENTACCTHJKMNPQR"),
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
		ContextHint:   debitHint(acctKey, tabKey),
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
		ContextHint:   debitHint(acctKey, "vtx.tab.BBABSENTTABHJKMNPQR"),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// --- CreditCafeAccount tabRef: the settlement playbook's counter payment ----

// seedSettledTab seeds a cafe-domain tab in the exact .status shape a staff
// Settle{paidCents} leaves behind (ddls.go, cafe-domain): settled, the total
// frozen, leaseAppKey denormalized, and paidAtSettleCents recorded when
// paidCents > 0. value overrides the status for the not-settled vector.
func seedSettledTab(t *testing.T, ctx context.Context, conn *substrate.Conn, id, leaseKey string,
	totalCents, paidCents int, value string) string {
	t.Helper()
	tabKey := "vtx.tab." + id
	seedVertex(t, ctx, conn, tabKey, "tab", map[string]any{})
	status := map[string]any{"value": value, "totalCents": totalCents, "leaseAppKey": leaseKey,
		"openedAt": "2026-07-07T12:00:00Z", "settledAt": "2026-07-07T13:00:00Z"}
	if paidCents > 0 {
		status["paidAtSettleCents"] = paidCents
		status["paidAtSettleBy"] = ledgerActorKey
	}
	seedAspect(t, ctx, conn, tabKey, "status", "tabStatus", status)
	return tabKey
}

// postTabDebit posts the playbook's charge for a settled tab: DebitAccount
// with tabRef, the settles link the counter payment's dedup walk reads.
func postTabDebit(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, tabKey string, amountCents int) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-07T13:01:00Z",
		Class:         "cafetransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
			strconv.Itoa(amountCents) + `,"memo":"Settled tab","tabRef":"` + tabKey + `"}`),
		ContextHint: debitHint(acctKey, tabKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// counterPaymentEnv is the CreditCafeAccount the cafeTabSettlement playbook's
// missing_payment gap dispatches (cafe-domain targets.go), declared exactly as
// that gap declares it: the account and the tab + its .status in Reads, the
// account's .balance/.arrears absence-tolerant, and the two walks
// require_counter_payment runs — the account's heldFor lease and the tab's
// inbound settles entries — beside the staff hint's own holdsRole and
// postedTo walks.
func counterPaymentEnv(label, actorKey, acctKey, tabKey string, amountCents int, reason string) (*processor.OperationEnvelope, string) {
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-07T13:05:00Z",
		Class:         "cafetransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) +
			`,"memo":"Paid at the counter","reason":"` + reason + `","tabRef":"` + tabKey + `"}`),
		ContextHint: staffCreditHint(actorKey, acctKey),
	}
	env.ContextHint.Reads = append(env.ContextHint.Reads, tabKey, tabKey+".status")
	env.ContextHint.Enumerations = append(env.ContextHint.Enumerations,
		processor.EnumerationHint{Hub: acctKey, Relation: "heldFor", Direction: "out"},
		processor.EnumerationHint{Hub: tabKey, Relation: "settles", Direction: "in"},
	)
	return env, "vtx.cafetransaction." + nanoIDFromRequestID(reqID)
}

// TestCreditCafeAccount_CounterPayment_WritesSettlesLink is the credit half of
// the tab back-link (cafe-domain's cafeTabSettlement missing_payment
// consumer): a CreditCafeAccount carrying a live tabRef writes the SAME
// settles audit link (transaction→tab) a charge does, alongside the normal
// postedTo link, and its .entry.type is "credit" — the type is what the lens
// discriminates on, so a payment never satisfies missing_charge's count of
// settling debits. The link key is asserted as the exact string the
// cafe-domain lens matches on.
func TestCreditCafeAccount_CounterPayment_WritesSettlesLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "credittabref")

	leaseKey := seedLease(t, ctx, conn, "BBCAFELEASEPAYTABHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreateacctpaytab", leaseKey)
	tabKey := seedSettledTab(t, ctx, conn, "BBCAFEPAYSFRHJKMNPQR", leaseKey, 950, 950, "settled")
	postTabDebit(t, ctx, conn, cp, cons, "cafecredittabrefdeb", acctKey, tabKey, 950)

	creditEnv, txKey := counterPaymentEnv("cafecredittabref0001", ledgerActorKey, acctKey, tabKey, 950, "payment")
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	txID := txKey[len("vtx.cafetransaction."):]
	tabID := tabKey[len("vtx.tab."):]
	settlesLnk := "lnk.cafetransaction." + txID + ".settles.tab." + tabID
	if !keyExists(t, ctx, conn, settlesLnk) {
		t.Fatalf("settles link must exist on the counter payment: %s", settlesLnk)
	}
	entryDoc := readDoc(t, ctx, conn, txKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "credit" {
		t.Fatalf("the counter payment posts entry.type = %q, want credit — the lens tells it from the charge by this", got)
	}
	if got := entryReason(t, ctx, conn, txKey); got != "payment" {
		t.Fatalf("the counter payment records reason = %q, want payment", got)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the charge and its counter payment = %v, want 0", got)
	}
}

// TestCreditCafeAccount_CounterPayment_ExemptFromBalanceCap is the exemption's
// positive vector, and what it is measured against. The account is already
// 1000 in credit (a refund of a paid charge); a settled tab paid 1500 at the
// counter has its charge posted (balance 500) — and then a resident-style
// self-payment shape is not needed to show the race: the counter payment of
// 1500 runs past the 500 owed, which the ordinary cap would refuse
// PaymentExceedsBalance for cash the desk already holds. Tab-tied, it posts:
// the balance lands at −1000 and cashCents rises by exactly the 1500 taken,
// so the credit the account now holds is covered by cash paid in and a later
// payout of it stays within the cash floor.
func TestCreditCafeAccount_CounterPayment_ExemptFromBalanceCap(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "counterpayexempt")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECPEXMPTLEASEHJ")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "cafecpexmpt", leaseKey, 1000)
	cashBefore := cashCents(t, ctx, conn, acctKey)
	tabKey := seedSettledTab(t, ctx, conn, "BBCAFECPEXMPTTABHJKM", leaseKey, 1500, 1500, "settled")
	postTabDebit(t, ctx, conn, cp, cons, "cafecpexmptdebit0001", acctKey, tabKey, 1500)
	if got := balanceCents(t, ctx, conn, acctKey); got != 500 {
		t.Fatalf("balance after the charge = %v, want 500", got)
	}

	// The same amount WITHOUT tabRef is what the cap refuses — the control
	// that shows the exemption, not a loosened cap, is what admits the credit.
	over, _ := creditEnvFor("cafecpexmptplain0001", ledgerActorKey, acctKey, 1500)
	assertRejectedBecause(t, ctx, conn, cp, cons, over,
		"PaymentExceedsBalance: a payment of $15.00 exceeds the outstanding balance of $5.00")

	env, _ := counterPaymentEnv("cafecpexmptcredit001", ledgerActorKey, acctKey, tabKey, 1500, "payment")
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -1000 {
		t.Fatalf("balance after the counter payment = %v, want -1000 (further into credit, the cash is real)", got)
	}
	if got := cashCents(t, ctx, conn, acctKey); got != cashBefore+1500 {
		t.Fatalf("cashCents after the counter payment = %v, want %v (+1500 — the credit is cash paid in)", got, cashBefore+1500)
	}
}

// TestCreditCafeAccount_CounterPayment_Refusals pins every conjunct the
// exemption leans on, each against a fresh tab so no refusal is explained by
// an earlier one: a write-off may not name a tab; the amount must equal the
// recorded counter payment (above AND below — a partial is not a counter
// payment); the tab must be settled; the tab must record a counter payment
// at all. Every refusal leaves the balance untouched.
func TestCreditCafeAccount_CounterPayment_Refusals(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "counterpayrefusals")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECPREFSLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecprefsacct000001", leaseKey)
	paid := seedSettledTab(t, ctx, conn, "BBCAFECPREFSPDTABHJK", leaseKey, 1200, 1200, "settled")
	open := seedSettledTab(t, ctx, conn, "BBCAFECPREFSQPENHJKM", leaseKey, 1200, 1200, "open")
	unpaid := seedSettledTab(t, ctx, conn, "BBCAFECPREFSUNPDHJKM", leaseKey, 1200, 0, "settled")
	postTabDebit(t, ctx, conn, cp, cons, "cafecprefsdebitpaid1", acctKey, paid, 1200)
	postTabDebit(t, ctx, conn, cp, cons, "cafecprefsdebitunpd1", acctKey, unpaid, 1200)

	for _, tc := range []struct {
		label, tab, reason string
		amount             int
		want               string
	}{
		{"cafecprefswaiver0001", paid, "waiver", 1200, "InvalidArgument: tabRef: a write-off pays for no tab"},
		{"cafecprefsover000001", paid, "payment", 1300, "CounterPaymentMismatch: a counter payment of $13.00 does not match the $12.00 recorded on the tab"},
		{"cafecprefsunder00001", paid, "payment", 1100, "CounterPaymentMismatch: a counter payment of $11.00 does not match the $12.00 recorded on the tab"},
		{"cafecprefsopen000001", open, "payment", 1200, "TabNotSettled: tab " + open + " is not settled"},
		{"cafecprefsnopay00001", unpaid, "payment", 1200, "NoCounterPayment: tab " + unpaid + " records no counter payment"},
	} {
		env, _ := counterPaymentEnv(tc.label, ledgerActorKey, acctKey, tc.tab, tc.amount, tc.reason)
		assertRejectedBecause(t, ctx, conn, cp, cons, env, tc.want)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 2400 {
		t.Fatalf("balance after five refused counter payments = %v, want the untouched 2400", got)
	}
}

// TestCreditCafeAccount_CounterPayment_PostedOnce is the dedup: a Weaver
// redelivery or a duplicate submit of the same counter payment must not
// credit cash taken once twice. The first posts (positive first); the second,
// byte-identical in everything but its request id, is refused off the tab's
// own settles entries, and the balance shows the cash once.
func TestCreditCafeAccount_CounterPayment_PostedOnce(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "counterpayonce")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECPQNCELEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecponceacct000001", leaseKey)
	tabKey := seedSettledTab(t, ctx, conn, "BBCAFECPQNCETABHJKMN", leaseKey, 900, 900, "settled")
	postTabDebit(t, ctx, conn, cp, cons, "cafecponcedebit00001", acctKey, tabKey, 900)

	first, _ := counterPaymentEnv("cafecponcecredit0001", ledgerActorKey, acctKey, tabKey, 900, "payment")
	testutil.PublishOp(t, conn, first)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	second, _ := counterPaymentEnv("cafecponcecredit0002", ledgerActorKey, acctKey, tabKey, 900, "payment")
	assertRejectedBecause(t, ctx, conn, cp, cons, second,
		"CounterPaymentAlreadyPosted: tab "+tabKey+" already has its counter payment posted")
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after a duplicate counter payment = %v, want 0 (credited once)", got)
	}
}

// TestCreditCafeAccount_UndeclaredSubmitterTabRef_PostsPayment is the
// derive_reads guarantee for the counter payment's own read: the tab's
// .status is what require_counter_payment measures the amount against, and a
// submitter that declares nothing beyond the account must still have it
// hydrated — the script's own class-(g) derivation appends the tab and its
// .status for every well-formed tabRef. The walks stay declared: only a read
// can be derived server-side.
func TestCreditCafeAccount_UndeclaredSubmitterTabRef_PostsPayment(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "counterpayundecl")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECPUNDCLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecpundclacct00001", leaseKey)
	tabKey := seedSettledTab(t, ctx, conn, "BBCAFECPUNDCTABHJKMN", leaseKey, 700, 700, "settled")
	postTabDebit(t, ctx, conn, cp, cons, "cafecpundcldebit0001", acctKey, tabKey, 700)

	env, _ := counterPaymentEnv("cafecpundclcredit001", ledgerActorKey, acctKey, tabKey, 700, "payment")
	// The account alone in Reads — no tab, no .status, no .balance.
	env.ContextHint.Reads = []string{acctKey}
	env.ContextHint.OptionalReads = nil
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after an undeclared counter payment = %v, want 0", got)
	}
}

// TestCreditCafeAccount_ConsumerSelfScope_TabRefDenied: tabRef on a credit is
// staff-only. The resident OWNS this account — the same payment without the
// field is accepted first — so the refusal is the field alone. A settles link
// on a resident's own payment would read to cafe-domain's cafeTabSettlement
// lens as the desk's counter payment already posted, closing missing_payment
// before the cash the desk recorded ever reached the ledger.
func TestCreditCafeAccount_ConsumerSelfScope_TabRefDenied(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselftabref")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFESELFTABLEASEHJ", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "creditselftabacct001", leaseKey)
	postDebit(t, ctx, conn, cp, cons, "creditselftabdebit01", acctKey, 1850, "Settled tab")
	tabKey := "vtx.tab.BBCAFESELFTABHJKMNPQ"
	seedVertex(t, ctx, conn, tabKey, "tab", map[string]any{})

	selfEnv := func(label, payload string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "CreditCafeAccount",
			Actor:         ledgerSelfConsumerKey,
			SubmittedAt:   "2026-07-08T09:00:00Z",
			Class:         "cafetransaction",
			Payload:       json.RawMessage(payload),
			ContextHint:   selfCreditHint(acctKey),
			AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
		}
	}

	testutil.PublishOp(t, conn, selfEnv("creditselftabpay0001", `{"accountKey":"`+acctKey+`","amountCents":100}`))
	if outcome := testutil.DriveOne(t, ctx, cp, cons, ""); outcome != processor.OutcomeAccepted {
		t.Fatalf("the resident's own bare self-payment outcome = %v, want Accepted — the refusal below is the field alone", outcome)
	}

	withTab := selfEnv("creditselftabpay0002", `{"accountKey":"`+acctKey+`","amountCents":100,"tabRef":"`+tabKey+`"}`)
	withTab.ContextHint.Reads = append(withTab.ContextHint.Reads, tabKey)
	assertRejectedBecause(t, ctx, conn, cp, cons, withTab,
		"AuthDenied: only staff record a tab's counter payment")
	if got := balanceCents(t, ctx, conn, acctKey); got != 1750 {
		t.Fatalf("balance after the refused self-payment = %v, want the untouched 1750", got)
	}
}

// TestCreditCafeAccount_UnknownTabRefRejected rejects a tabRef naming a tab
// that is not alive on the credit leg (no-orphan invariant on the settles
// link, mirroring the debit's UnknownTab). The tab is TOMBSTONED rather than
// absent: an absent declared read is refused by hydration before the script
// runs (the debit's own vector), whereas a tombstone hydrates and it is the
// script's vertex_alive check that refuses it — the refusal this pins.
func TestCreditCafeAccount_UnknownTabRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditunknowntab")

	leaseKey := seedLease(t, ctx, conn, "BBCAFELEASEBADPAYHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecreateacctbadpay", leaseKey)
	postDebit(t, ctx, conn, cp, cons, "cafecreditbadtabdeb", acctKey, 950, "Settled tab")
	tabKey := "vtx.tab.BBGNEPAYTABHJKMNPQRS"
	tomb, _ := json.Marshal(map[string]any{"class": "tab", "isDeleted": true, "data": map[string]any{}})
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, tabKey, tomb); err != nil {
		t.Fatalf("seed tombstoned tab: %v", err)
	}

	env, _ := creditEnvWithPayload("cafecreditbadtabref1", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":950,"tabRef":"`+tabKey+`"}`)
	env.ContextHint.Reads = append(env.ContextHint.Reads, tabKey)
	assertRejectedBecause(t, ctx, conn, cp, cons, env, "UnknownTab: "+tabKey)
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
		ContextHint:   debitHint(acctKey),
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
		ContextHint: debitHint(acctKey),
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
			Reads:         []string{acctKey, reversesRef, reversesRef + ".entry"},
			OptionalReads: []string{acctKey + ".balance", acctKey + ".arrears"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: reversesRef, Relation: "postedTo", Direction: "out"},
				{Hub: acctKey, Relation: "postedTo", Direction: "in"},
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
// may win.
//
// The loser does not simply conflict out. The account carries a .balance aspect
// and both refunds update it, an update the Processor auto-conditions on the
// revision it hydrated at, so the losing commit re-hydrates and RE-EXECUTES the
// whole operation — and reversed_charge then refuses the retry on the fresh
// refundedCents it re-reads (the charge is already fully given back). Which of
// the two mechanisms turns the loser away is not fixed and does not matter; the
// outcome is: one refund posted, the charge's tally equal to that one refund,
// never both.
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
				Reads:         []string{acctKey, chargeKey, chargeKey + ".entry"},
				OptionalReads: []string{acctKey + ".balance"},
				Enumerations: []processor.EnumerationHint{
					{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
					{Hub: acctKey, Relation: "postedTo", Direction: "in"},
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
// tabRef sent to RefundCafeCharge is refused rather than ignored. The field
// belongs to DebitAccount and CreditCafeAccount (both write settles) — a
// refund settles no tab — and a caller sending one means "give back the charge
// that settled this tab". Silently dropping it commits a credit with no
// settles link and no relation to the tab at all, which is the same
// disagreement between what was asked for and what the ledger records.
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
			Reads:         []string{acctKey, chargeKey, chargeKey + ".entry", tabKey},
			OptionalReads: []string{acctKey + ".balance"},
			Enumerations: []processor.EnumerationHint{
				{Hub: ledgerActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: chargeKey, Relation: "postedTo", Direction: "out"},
			},
		},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"InvalidArgument: tabRef: only valid on DebitAccount or CreditCafeAccount, not RefundCafeCharge")
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
		ContextHint:   debitHint(acctKey),
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
		ContextHint:   selfCreditHint(acctKey),
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-service CreditCafeAccount outcome = %v, want Accepted", outcome)
	}
}

// TestCreditCafeAccount_SelfLegCapped proves the self-credit amount is bounded
// by what the account actually owes — a resident cannot self-forgive debt by
// naming an amount larger than any front-desk-recorded charge. Nothing on this
// platform verifies a self-submitted payment actually happened, so the amount
// itself is the attack surface, not just which account it targets.
//
// It is a test in its own right because the cap does NOT live inside the
// resident branch: post_entry's authContextTarget branch proves ownership and
// nothing else, and the cap is applied afterwards to the payment leg whoever
// submitted it. A staff-only cap test would leave that relocation unwitnessed
// on the leg it was written for. The refusal is read from the reply, not the
// outcome: every denial in this script collapses to "rejected", and the
// ownership proof one line above produces one too.
func TestCreditCafeAccount_SelfLegCapped(t *testing.T) {
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
		ContextHint:   debitHint(acctKey),
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
		ContextHint:   selfCreditHint(acctKey),
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"PaymentExceedsBalance: a payment of $18500.00 exceeds the outstanding balance of $18.50")
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
		ContextHint:   selfCreditHint(acctKey),
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"NoBalanceToPay: this account has no outstanding balance to pay")
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
		ContextHint:   selfCreditHint(acctKey),
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"AuthDenied: a resident may only pay down their own lease's account")
}

// TestCreditCafeAccount_StrangersAccountDeniedBeforeAnyReplay pins the ORDER of
// post_entry's checks, which is a security property and not a stylistic one.
// The balance read and the bounded history replay behind it are the most
// expensive work the script does — hundreds of Core KV round trips on a legacy
// account — and anyone holding the ordinary scope=self payment grant can name
// any account key they like. If that work ran before the ownership proof, a
// resident could spend the whole replay budget against a stranger's account,
// repeatedly, and be denied only afterwards.
//
// The stranger's account is deliberately over-budget, which is what makes the
// ordering visible rather than merely believed: run the replay first and the
// refusal is the budget's ("could not backfill…"); prove ownership first and it
// is this one. A same-shaped account under the budget would refuse identically
// either way and prove nothing.
func TestCreditCafeAccount_StrangersAccountDeniedBeforeAnyReplay(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditselfamplify")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	otherApplicantID := "BBCAFEAMPLAPPLCTHJKM"
	seedIdentity(t, ctx, conn, otherApplicantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEAMPLLEASEHJKMN", otherApplicantID)
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEAMPLACCTHJKMNP", leaseKey)
	for i := 0; i < 501; i++ {
		seedLegacyEntry(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("creditamplifypay0001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerSelfConsumerKey,
		SubmittedAt:   "2026-07-08T09:05:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":100}`),
		ContextHint:   selfCreditHint(acctKey),
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"AuthDenied: a resident may only pay down their own lease's account")
}

// TestAccountBalance_WrongClassRefused: the script is the sole writer of a
// .balance aspect and writes exactly cafeAccountBalance, so a document of any
// other class under that key is a fault, not a number to measure a payment cap
// against. Reading balanceCents off whatever happened to be there would let an
// unrelated aspect's field decide how much a resident may pay.
func TestAccountBalance_WrongClassRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancewrongclass")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALWRCLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafebalwrongclsacct1", leaseKey)
	postDebit(t, ctx, conn, cp, cons, "cafebalwrongclsdebit", acctKey, 1000, "Settled tab")
	seedAspect(t, ctx, conn, acctKey, "balance", "transactionEntry",
		map[string]any{"balanceCents": 900000})

	env, _ := creditEnvFor("cafebalwrongclspay01", ledgerActorKey, acctKey, 5000)
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"InvalidState: this account's balance aspect is not a cafeAccountBalance")
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
		ContextHint:   debitHint(acctKey),
		AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-scoped DebitAccount outcome = %v, want Rejected (no matching grant)", outcome)
	}
}

// --- The account's maintained .balance running total ----------------------

// balanceCents reads back the account's own .balance aspect — the O(1) running
// total post_entry keeps in lockstep with every posted entry, and the quantity
// the payment cap is measured against.
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

// creditEnvFor builds one staff-voice CreditCafeAccount of amountCents against
// acctKey, declaring exactly what the descriptor declares (staffCreditHint).
func creditEnvFor(label, actorKey, acctKey string, amountCents int) (*processor.OperationEnvelope, string) {
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-20T09:00:00Z",
		Class:         "cafetransaction",
		Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
			strconv.Itoa(amountCents) + `,"memo":"House tab payment"}`),
		ContextHint: staffCreditHint(actorKey, acctKey),
	}
	return env, "vtx.cafetransaction." + nanoIDFromRequestID(reqID)
}

// creditAmount submits one CreditCafeAccount of amountCents against acctKey as
// the given actor and asserts the outcome.
func creditAmount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actorKey, acctKey string, amountCents int, want processor.MessageOutcome) string {
	t.Helper()
	env, txKey := creditEnvFor(label, actorKey, acctKey, amountCents)
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return txKey
}

// assertRejectedBecause drives env and asserts it was rejected FOR THE STATED
// REASON. MessageOutcome collapses every refusal into "rejected", so an
// outcome-only assertion on a payment cap passes just as well against a guard
// that denied the actor, the account or the payload — which is the whole
// question when the cap has just been moved out of one branch and made to bind
// the operation.
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

// TestCreditCafeAccount_StaffOverBalanceRefused is the staff half of the amount
// cap. A front-desk payment is keyed by hand from a card terminal or a till, and
// no payment rail on this platform witnesses the number that gets typed — so an
// uncapped staff credit posts whatever was mis-keyed, taking the account
// negative and off the arrears grid with no op that undoes it. The exact-balance
// payment runs alongside it: the cap is `>`, not `>=`, and a rejection-only test
// would pass just as well against a guard that refused every payment.
func TestCreditCafeAccount_StaffOverBalanceRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditstaffoverbal")

	leaseKey := seedLease(t, ctx, conn, "BBCAFESTFQVRLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafestaffoveracct001", leaseKey)
	postDebit(t, ctx, conn, cp, cons, "cafestaffoverdebit01", acctKey, 1425, "Settled tab")

	// $14.25 owed; the staffer keys $50.00. The refusal names both amounts as
	// money, because it is toasted verbatim at the counter.
	overEnv, _ := creditEnvFor("cafestaffoverpay0001", ledgerActorKey, acctKey, 5000)
	assertRejectedBecause(t, ctx, conn, cp, cons, overEnv,
		"PaymentExceedsBalance: a payment of $50.00 exceeds the outstanding balance of $14.25")
	if got := balanceCents(t, ctx, conn, acctKey); got != 1425 {
		t.Fatalf("balance after the refused payment = %v, want the untouched 1425", got)
	}

	// Exactly what is owed is fine.
	creditAmount(t, ctx, conn, cp, cons, "cafestaffexactpay001",
		ledgerActorKey, acctKey, 1425, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying in full = %v, want 0", got)
	}
}

// TestCreditCafeAccount_StaffNoBalanceRefused: a staff payment against an
// account that owes nothing has nothing to pay down, and posting one would put
// the resident into a credit balance the arrears grid stops showing. The
// accepted payment after a charge is posted proves the refusal is the empty
// balance and not the actor or the account.
func TestCreditCafeAccount_StaffNoBalanceRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "creditstaffnobal")

	leaseKey := seedLease(t, ctx, conn, "BBCAFESTFNQBALEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafestaffnobalacct01", leaseKey)

	noBalanceEnv, _ := creditEnvFor("cafestaffnobalpay001", ledgerActorKey, acctKey, 100)
	assertRejectedBecause(t, ctx, conn, cp, cons, noBalanceEnv,
		"NoBalanceToPay: this account has no outstanding balance to pay")

	postDebit(t, ctx, conn, cp, cons, "cafestaffnobaldebit1", acctKey, 100, "Settled tab")
	creditAmount(t, ctx, conn, cp, cons, "cafestaffnobalpay002",
		ledgerActorKey, acctKey, 100, processor.OutcomeAccepted)
}

// TestAccountBalance_AccumulatesAcrossEntries pins the cache itself rather than
// the cap that reads it: CreateAccount mints .balance at zero, and each of the
// three posting ops moves it by its own signed amount. A refund is in the
// sequence deliberately — it takes the third branch of post_entry and is the
// one entry the cap never examines, so a refund that forgot to maintain the
// total would leave every later payment measured against a stale number while
// every existing refund test stayed green.
func TestAccountBalance_AccumulatesAcrossEntries(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balanceaccum")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALACCLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafebalaccumacct0001", leaseKey)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("a freshly opened account's balance = %v, want 0", got)
	}

	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafebalaccumdebit001", acctKey, 4000, "Settled tab")
	if got := balanceCents(t, ctx, conn, acctKey); got != 4000 {
		t.Fatalf("balance after a 4000 charge = %v, want 4000", got)
	}

	creditAmount(t, ctx, conn, cp, cons, "cafebalaccumpay00001",
		ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 3000 {
		t.Fatalf("balance after a 1000 payment = %v, want 3000", got)
	}

	refundAs(t, ctx, conn, cp, cons, "cafebalaccumrefund01",
		ledgerActorKey, acctKey, chargeKey, 500, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 2500 {
		t.Fatalf("balance after a 500 refund = %v, want 2500 — a refund is a posted credit and moves the total like any other", got)
	}
}

// seedAspect writes one aspect document directly, in the shape the platform
// stores (vertexKey/localName carried alongside class/isDeleted/data) — the
// legacy-account fixture below needs entries and topology that exist WITHOUT a
// .balance aspect, which no op can produce any more.
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vtxKey, localName, class string, data map[string]any) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vtxKey, "localName": localName, "data": data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, vtxKey+"."+localName, b); err != nil {
		t.Fatalf("seed aspect %s.%s: %v", vtxKey, localName, err)
	}
}

// seedTombstonedAspect writes one aspect document as a TOMBSTONE — present,
// isDeleted true, exactly what kv.Read hands a script for a soft-deleted key
// (step4_hydrate routes only ErrKeyNotFound to knownAbsent). No café op
// tombstones a .balance, so this shape has to be planted; the write path must
// still handle it, because a create against a tombstone is refused outright
// (Contract #3 §3.3) and an account stuck that way would take no more entries.
func seedTombstonedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vtxKey, localName, class string, data map[string]any) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": true,
		"vertexKey": vtxKey, "localName": localName, "data": data,
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, vtxKey+"."+localName, b); err != nil {
		t.Fatalf("seed tombstoned aspect %s.%s: %v", vtxKey, localName, err)
	}
}

// seedLegacyEntry seeds one already-posted transaction on acctKey — the vertex,
// its .entry aspect and the postedTo link back — with no effect on any .balance
// aspect, exactly as an entry posted before that aspect existed sits today.
func seedLegacyEntry(t *testing.T, ctx context.Context, conn *substrate.Conn,
	acctKey, txID, entryType string, amountCents int) {
	t.Helper()
	txKey := "vtx.cafetransaction." + txID
	acctID := acctKey[len("vtx.cafeaccount."):]
	seedVertex(t, ctx, conn, txKey, "cafetransaction", map[string]any{})
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", map[string]any{
		"type": entryType, "amountCents": amountCents, "postedAt": "2026-06-01T08:00:00Z",
	})
	seedLink(t, ctx, conn,
		"lnk.cafetransaction."+txID+".postedTo.cafeaccount."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
}

// seedLegacyAccount seeds a café account in the shape one minted under
// cafe-ledger < 0.4.0 sits in today: the vertex and its heldFor lease, and NO
// .balance aspect — a shape no op produces.
func seedLegacyAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, acctID, leaseKey string) string {
	t.Helper()
	acctKey := "vtx.cafeaccount." + acctID
	leaseID := leaseKey[len("vtx.leaseapp."):]
	seedVertex(t, ctx, conn, acctKey, "cafeaccount", map[string]any{})
	seedLink(t, ctx, conn,
		"lnk.cafeaccount."+acctID+".heldFor.leaseapp."+leaseID,
		acctKey, leaseKey, "heldFor", "heldFor")
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("the fixture must start with NO .balance aspect — that is the legacy shape under test")
	}
	return acctKey
}

// TestAccountBalance_LegacyAccountSelfHealsOnFirstPayment covers the accounts
// that already exist. One minted under cafe-ledger < 0.4.0 carries no such
// aspect, which is why every dispatcher declares the key optionalReads
// rather than reads — a required read would HydrationMiss-reject every entry
// against it. A PAYMENT against such an account replays its postedTo history
// once, bounded, to get the number its own cap needs, and mints the total from
// that; every touch afterwards is O(1). The seeded history is a debit AND a
// credit so a replay that summed only charges (or got the sign wrong) lands on a
// different number than one that nets them.
func TestAccountBalance_LegacyAccountSelfHealsOnFirstPayment(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancelegacy")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALLEGLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEBALLEGACCTHJKM", leaseKey)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEBALLEGTXAHJKMN", "debit", 3000)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEBALLEGTXBHJKMN", "credit", 500)

	// The cap already measures against the replayed number, before any
	// .balance exists: 2500 is owed (3000 charged − 500 paid), so 2501 is not
	// payable — and a refused payment commits nothing, so the account is still
	// legacy afterwards.
	creditAmount(t, ctx, conn, cp, cons, "cafebalegacyover0001",
		ledgerActorKey, acctKey, 2501, processor.OutcomeRejected)
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("a REFUSED payment seeded .balance — nothing about a rejected op may commit")
	}

	// The first accepted payment mints the aspect from the replayed total.
	creditAmount(t, ctx, conn, cp, cons, "cafebalegacypay00001",
		ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 1500 {
		t.Fatalf("backfilled balance = %v, want 1500 (3000 charged − 500 paid − this 1000 payment)", got)
	}

	// And every touch after that is the O(1) path off the aspect itself.
	creditAmount(t, ctx, conn, cp, cons, "cafebalegacyexact001",
		ledgerActorKey, acctKey, 1500, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying the backfilled total = %v, want 0", got)
	}
}

// TestAccountBalance_LegacyAccountDebitDoesNotSeed pins which leg pays for the
// replay, and it is only the payment. A charge (and a refund) against a legacy
// account posts normally and writes NO .balance: seeding the cache from that one
// entry would record a total that never counted the history behind it, and every
// later payment would be capped against that wrong number. The account stays
// legacy until a payment first touches it — and that payment's replay sums the
// whole history, the charges posted meanwhile included.
//
// The wedge this avoids is the reason: the Weaver's cafeTabSettlement
// missing_charge dispatch is the ONLY writer of a settlement charge, it runs
// unattended, and an account whose history outgrew the replay budget would
// refuse every one of those dispatches forever with no repair path.
func TestAccountBalance_LegacyAccountDebitDoesNotSeed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancelegacydebit")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALLGDLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEBALLGDACCTHJKM", leaseKey)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEBALLGDTXAHJKMN", "debit", 3000)

	postDebit(t, ctx, conn, cp, cons, "cafebalgdebit000001", acctKey, 1000, "Settled tab")
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("a charge against a legacy account seeded .balance — only a payment may, and only from the whole history")
	}

	// The later payment's replay counts that charge: 4000 is owed, not 3000.
	creditAmount(t, ctx, conn, cp, cons, "cafebalgdover000001",
		ledgerActorKey, acctKey, 4001, processor.OutcomeRejected)
	creditAmount(t, ctx, conn, cp, cons, "cafebalgdpay0000001",
		ledgerActorKey, acctKey, 4000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying off a legacy account = %v, want 0 (3000 seeded + 1000 charged − 4000 paid)", got)
	}
}

// TestAccountBalance_LegacyFirstTouchRace is the two-payment race on the FIRST
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
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancelegacyrace")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALLGRLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEBALLGRACCTHJKM", leaseKey)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEBALLGRTXAHJKMN", "debit", 3000)

	envA, _ := creditEnvFor("cafebalgracefirst001", ledgerActorKey, acctKey, 1500)
	envB, _ := creditEnvFor("cafebalgracesecond01", ledgerActorKey, acctKey, 1500)
	testutil.PublishOp(t, conn, envA)
	testutil.PublishOp(t, conn, envB)

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	for i, o := range outcomes {
		if o != processor.OutcomeAccepted {
			t.Fatalf("outcome[%d] = %v, want accepted: the loser of a first-touch race re-hydrates against the winner's minted .balance and re-runs, never conflicts out", i, o)
		}
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after two concurrent 1500 payments on a 3000 legacy tab = %v, want 0", got)
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
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancetombstone")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALTMBLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEBALTMBACCTHJKM", leaseKey)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEBALTMBTXAHJKMN", "debit", 3000)
	seedTombstonedAspect(t, ctx, conn, acctKey, "balance", "cafeAccountBalance",
		map[string]any{"balanceCents": 0})

	creditAmount(t, ctx, conn, cp, cons, "cafebaltombpay000001",
		ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)

	doc := readDoc(t, ctx, conn, acctKey+".balance")
	if deleted, _ := doc["isDeleted"].(bool); deleted {
		t.Fatalf("%s.balance is still tombstoned after an accepted payment", acctKey)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 2000 {
		t.Fatalf("revived balance = %v, want 2000 (3000 replayed − 1000 paid) — the tombstoned document's own stale zero must not be read", got)
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

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALUNDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafebalundeclacct001", leaseKey)

	undeclaredDebit := func(label string, amountCents int) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "DebitAccount",
			Actor:         ledgerActorKey,
			SubmittedAt:   "2026-07-22T08:00:00Z",
			Class:         "cafetransaction",
			Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
				strconv.Itoa(amountCents) + `,"memo":"Settled tab"}`),
			// The account alone. No optionalReads, no .balance — the shape a
			// client that never read the descriptor sends.
			ContextHint: &processor.ContextHint{Reads: []string{acctKey}},
		}
	}
	testutil.PublishOp(t, conn, undeclaredDebit("cafebalundecfirst001", 700))
	testutil.PublishOp(t, conn, undeclaredDebit("cafebalundecsecond01", 300))

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

// budgetTxID encodes i as a valid 20-char NanoID (Contract #1's alphabet — no
// I/O/l/0), so the budget fixture below can plant several hundred distinct
// transactions without hand-writing an id per entry.
func budgetTxID(i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	n := len(safe)
	return "BBCAFEBUDGETTXAHJ" + string([]byte{safe[i/(n*n)%n], safe[(i/n)%n], safe[i%n]})
}

// TestCreditCafeAccount_BackfillBudgetExhausted is the fail-closed end of the
// replay. The budget is 10 pages of 50, so an account carrying more than 500
// postedTo entries cannot be summed in one operation — and the script refuses
// the payment rather than seeding .balance from the partial sum it did reach,
// which would silently under-state the debt for the life of the account.
//
// The refusal names no key: it is toasted verbatim at whoever tried to pay.
func TestCreditCafeAccount_BackfillBudgetExhausted(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancebudget")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALBUDLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEBALBUDACCTHJKM", leaseKey)
	for i := 0; i < 501; i++ {
		seedLegacyEntry(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100)
	}

	env, _ := creditEnvFor("cafebalbudgetpay0001", ledgerActorKey, acctKey, 100)
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"AuthDenied: could not backfill this account's balance (too much transaction history for one op)")
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("the refused payment seeded .balance from a partial replay — the whole point is that it must not")
	}
}

// TestDeriveReads_BalanceKey pins the class-(g) derivation as TEXT, because its
// effect is otherwise invisible on the happy path: declared or not, the script
// reads .balance through kv.Read and an undeclared read falls through to a live
// Core KV GET that returns the same number. Only the concurrent test above shows
// the difference behaviourally, and only this shows the derivation still covers
// all four ops rather than the one that test happens to drive.
func TestDeriveReads_BalanceKey(t *testing.T) {
	var script string
	for _, d := range cafeledger.DDLs() {
		if d.CanonicalName == "cafetransaction" {
			script = d.Script
		}
	}
	if script == "" {
		t.Fatal("no `cafetransaction` DDL script found")
	}
	deriveIdx := strings.Index(script, "def derive_reads(op):")
	executeIdx := strings.Index(script, "\ndef execute(state, op):")
	if deriveIdx < 0 || executeIdx <= deriveIdx {
		t.Fatalf("cannot locate derive_reads in the cafetransaction script (derive=%d execute=%d)", deriveIdx, executeIdx)
	}
	derive := script[deriveIdx:executeIdx]
	for _, want := range []string{"DebitAccount", "CreditCafeAccount", "RefundCafeCharge", "PayoutCafeCredit"} {
		if !strings.Contains(derive, want) {
			t.Fatalf("derive_reads does not mention %q — that op's .balance update would be unconditioned whenever its submitter omits the declaration", want)
		}
	}
	if !strings.Contains(derive, `keys = [acct_key, acct_key + ".balance", acct_key + ".arrears"]`) {
		t.Fatalf("derive_reads no longer returns the account root, its .balance AND its .arrears under optionalReads:\n%s", derive)
	}
	if !strings.Contains(derive, `keys.append(reverses_key)`) || !strings.Contains(derive, `keys.append(reverses_key + ".entry")`) {
		t.Fatalf("derive_reads no longer derives reversesRef and its .entry for RefundCafeCharge:\n%s", derive)
	}
	if !strings.Contains(derive, `return {"optionalReads": keys}`) {
		t.Fatalf("derive_reads no longer returns keys under optionalReads:\n%s", derive)
	}
	// optionalReads, never reads: a legacy account carries no .balance, and a
	// required read's absence is a HydrationMiss on the very branch the replay
	// exists for.
	if strings.Contains(derive, `"reads"`) {
		t.Fatalf("derive_reads returns a hard `reads` entry — every legacy account would HydrationMiss:\n%s", derive)
	}
}

// TestRefundCafeCharge_NotBalanceCapped is the boundary of the payment cap. A
// refund is a credit too, and capping it at the outstanding balance would make
// the one case a refund exists for — a charge the resident has ALREADY paid,
// wrongly posted — the one case that could not be given back. Its ceiling is
// the reversed charge's own un-refunded remainder, so the balance legitimately
// goes negative: the café owes money out.
func TestRefundCafeCharge_NotBalanceCapped(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundnotcapped")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEREFNCPLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferefnotcapacct001", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferefnotcapdebit01", acctKey, 900, "Settled tab")
	creditAmount(t, ctx, conn, cp, cons, "caferefnotcappay0001",
		ledgerActorKey, acctKey, 900, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying the charge in full = %v, want 0", got)
	}

	// A plain payment on this account is now refused — nothing is owed. The
	// refund of the same size is not, and that contrast is the whole test.
	creditAmount(t, ctx, conn, cp, cons, "caferefnotcappay0002",
		ledgerActorKey, acctKey, 900, processor.OutcomeRejected)
	refundAs(t, ctx, conn, cp, cons, "caferefnotcaprefund1",
		ledgerActorKey, acctKey, chargeKey, 900, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -900 {
		t.Fatalf("balance after refunding a paid-in-full charge = %v, want -900 (the café owes it back)", got)
	}
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
// loser would be rejected outright — a charge the café made and never billed.
//
// Serial driving cannot show this: DriveOne finishes the first entry before the
// second hydrates, so the second reads the already-updated revision and never
// races at all.
func TestAccountBalance_ConcurrentEntriesBothPost(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "balancerace")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEBALRACELEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafebalraceacct00001", leaseKey)

	debitEnv := func(label string, amountCents int) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "DebitAccount",
			Actor:         ledgerActorKey,
			SubmittedAt:   "2026-07-21T08:00:00Z",
			Class:         "cafetransaction",
			Payload: json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` +
				strconv.Itoa(amountCents) + `,"memo":"Settled tab"}`),
			ContextHint: debitHint(acctKey),
		}
	}
	testutil.PublishOp(t, conn, debitEnv("cafebalracefirst0001", 700))
	testutil.PublishOp(t, conn, debitEnv("cafebalracesecond001", 300))

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	for i, o := range outcomes {
		if o != processor.OutcomeAccepted {
			t.Fatalf("outcome[%d] = %v, want accepted: a lost race on the account's own .balance must re-hydrate and retry, not reject a charge the café made", i, o)
		}
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 1000 {
		t.Fatalf("balance after two concurrent charges = %v, want 1000 — neither may be dropped", got)
	}
}

// --- arrears (Weaver-dispatched evaluation + the episode state post_entry keeps) ---

// arrearsWeaverCapDoc grants Weaver's primordial dispatch actor
// EvaluateCafeArrears. Read through a func, not a package var: bootstrap's
// primordial globals are populated by SetupPackageTestEnv's EnsurePrimordials,
// well after package var initialization.
func arrearsWeaverCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + bootstrap.WeaverIdentityID,
		Actor:                  bootstrap.WeaverIdentityKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bootstrap.WeaverIdentityKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "EvaluateCafeArrears", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

// arrearsHint is the contextHint the cafeArrearsReminders playbook dispatches
// with (targets.go): the account root, its absence-tolerant .arrears aspect,
// and the bounded postedTo replay. The per-transaction .entry reads that replay
// discovers are NOT declared — their keys are data-derived, the class-(e) split.
func arrearsHint(acctKey string) *processor.ContextHint {
	return &processor.ContextHint{
		Reads:         []string{acctKey},
		OptionalReads: []string{acctKey + ".arrears"},
		Enumerations: []processor.EnumerationHint{
			{Hub: acctKey, Relation: "postedTo", Direction: "in"},
			{Hub: acctKey, Relation: "heldFor", Direction: "out"},
		},
	}
}

// evaluateArrears drives one EvaluateCafeArrears as `actor` at `submittedAt`,
// asserts the outcome, and returns the reply (for a refusal's message) and the
// request id (for the outbox the notification rides on). Class is LEFT EMPTY,
// exactly as Weaver's actuator dispatches a directOp — it relies on the
// Processor's operationType→class reverse index, which resolves to the
// cafeaccount vertexType handler.
func evaluateArrears(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actor, acctKey, submittedAt string,
	want processor.MessageOutcome) (*processor.OperationReply, string) {
	t.Helper()
	reqID := testutil.GenReqID(label)
	payload := `{"accountKey":"` + acctKey + `"}`
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateCafeArrears",
		Actor:         actor,
		SubmittedAt:   submittedAt,
		Payload:       json.RawMessage(payload),
		ContextHint:   arrearsHint(acctKey),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply, reqID
}

// arrearsData reads the account's .arrears aspect back. Returns nil when the
// aspect is absent — which is a real state (a never-charged account carries
// none), not an error.
func arrearsData(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) map[string]any {
	t.Helper()
	if !keyExists(t, ctx, conn, acctKey+".arrears") {
		return nil
	}
	doc := readDoc(t, ctx, conn, acctKey+".arrears")
	if cls, _ := doc["class"].(string); cls != "cafeAccountArrears" {
		t.Fatalf("%s.arrears class = %q, want cafeAccountArrears", acctKey, cls)
	}
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.arrears carries no data", acctKey)
	}
	return data
}

// arrearsNotification returns the external.notification event this request's
// own transactional outbox carries, or nil when it emitted none. "Nil" is the
// assertion half the once-per-episode guarantee rests on.
func arrearsNotification(t *testing.T, ctx context.Context, conn *substrate.Conn, requestID string) map[string]any {
	t.Helper()
	entry, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(requestID))
	if err != nil {
		t.Fatalf("read outbox aspect for %s: %v", requestID, err)
	}
	ob, err := processor.ParseOutboxAspect(entry.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect for %s: %v", requestID, err)
	}
	for _, e := range ob.Data.Events {
		if e.EventType == "external.notification" {
			return e.Payload
		}
	}
	return nil
}

// debitAt posts a charge at an explicit instant — the arrears vectors turn on
// WHEN a charge posted, which postDebit's fixed submittedAt cannot express.
func debitAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"Settled tab"}`),
		ContextHint:   debitHint(acctKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.cafetransaction." + nanoIDFromRequestID(reqID)
}

// creditAt is debitAt's payment counterpart.
func creditAt(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, acctKey, submittedAt string, amountCents int) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CreditCafeAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   submittedAt,
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"House tab payment"}`),
		ContextHint:   staffCreditHint(ledgerActorKey, acctKey),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestArrears_FirstChargeOpensAnEpisode (a). A charge posted to an account that
// owes nothing IS the FIFO head, so post_entry can name the due date without
// replaying anything: this charge's own postedAt plus the package's net term.
func TestArrears_FirstChargeOpensAnEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsopen")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRPENLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearropenacct00001", leaseKey)
	if arrearsData(t, ctx, conn, acctKey) != nil {
		t.Fatal("CreateAccount must mint NO .arrears — a new account owes nothing, and its missing evaluatedAt is what opens the gap once")
	}

	debitAt(t, ctx, conn, cp, cons, "cafearropendebit0001", acctKey, "2026-08-01T09:00:00Z", 1425)

	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("a charge against an account that owed nothing must open an arrears episode")
	}
	// 2026-08-01 + ArrearsGraceDays, the same arithmetic the resident's own
	// statement runs — the constant is the package's, not two literals.
	wantDue := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q (the charge's own postedAt + the net term)", got, wantDue)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-01T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the charge's postedAt", got)
	}
	if _, ok := data["stale"]; ok {
		t.Fatal("a freshly opened episode is not stale")
	}
}

// TestArrears_SecondChargeLeavesTheHead (b). The head is the OLDEST open
// charge, and a second charge queues behind it — re-stamping dueAt here would
// push a weeks-old debt's due date back to the day of the newest coffee.
func TestArrears_SecondChargeLeavesTheHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssecond")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRSNDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrsndacct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrsnddebit00001", acctKey, "2026-08-01T09:00:00Z", 1425)
	first := arrearsData(t, ctx, conn, acctKey)

	debitAt(t, ctx, conn, cp, cons, "cafearrsnddebit00002", acctKey, "2026-08-20T09:00:00Z", 500)

	second := arrearsData(t, ctx, conn, acctKey)
	if second["dueAt"] != first["dueAt"] {
		t.Fatalf("dueAt moved from %v to %v on a second charge — the head is the OLDEST open charge", first["dueAt"], second["dueAt"])
	}
	if second["evaluatedAt"] != first["evaluatedAt"] {
		t.Fatalf("a charge that changes nothing must write nothing; evaluatedAt moved from %v to %v", first["evaluatedAt"], second["evaluatedAt"])
	}
}

// TestArrears_PartialPaymentMarksStale (c). A partial payment can move the FIFO
// head to a later charge with a later due date, which no single entry can
// compute — so post_entry marks the recorded state stale rather than guessing,
// and the convergence lens reads stale as an open gap.
func TestArrears_PartialPaymentMarksStale(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsstale")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRSTLLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrstlacct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrstldebit00001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "cafearrstldebit00002", acctKey, "2026-08-20T09:00:00Z", 500)
	before := arrearsData(t, ctx, conn, acctKey)

	creditAt(t, ctx, conn, cp, cons, "cafearrstlpay000001", acctKey, "2026-08-21T09:00:00Z", 1000)

	after := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("a partial payment must mark the recorded arrears state stale, got %+v", after)
	}
	if after["dueAt"] != before["dueAt"] {
		t.Fatalf("the stale mark is an ADDITION, not a rewrite: dueAt moved from %v to %v", before["dueAt"], after["dueAt"])
	}
}

// TestArrears_PaymentToZeroEndsTheEpisode (d). Paying the tab off rewrites
// .arrears to {evaluatedAt} alone: no dueAt, so no timer stays armed, and
// nothing of the finished episode survives to make the NEXT charge look already
// reminded.
func TestArrears_PaymentToZeroEndsTheEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscleared")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRCLRLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrclracct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrclrdebit00001", acctKey, "2026-08-01T09:00:00Z", 1425)
	creditAt(t, ctx, conn, cp, cons, "cafearrclrpay000001", acctKey, "2026-08-05T09:00:00Z", 1425)

	data := arrearsData(t, ctx, conn, acctKey)
	if _, ok := data["dueAt"]; ok {
		t.Fatalf("a paid-off account must carry no dueAt, got %+v", data)
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("a paid-off account is not stale — there is nothing to recompute: %+v", data)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-05T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the payment's postedAt", got)
	}
}

// TestArrears_DebitOnACreditBalanceOpensNoEpisode (d2). "The account owed
// nothing before this charge" is B ≤ 0, but that is not the same question as
// "does it owe anything after it". A refund can take an account into CREDIT, and
// a charge that only eats into that credit leaves the resident still owed money
// by the café — under the FIFO the surplus prepays the charge outright, so there
// is no open debit and no head. An episode minted there arms a timer that
// reminds a resident about money they do not owe, which is the one thing the
// green bar says must never happen. The SECOND charge, the one that finally
// takes the balance positive, is the one that opens the episode — and its own
// postedAt is the term the resident is held to.
func TestArrears_DebitOnACreditBalanceOpensNoEpisode(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscredit")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRCRDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrcrdacct000001", leaseKey)

	// Charge, pay it off, then refund the charge: B = -1000.
	chargeKey := debitAt(t, ctx, conn, cp, cons, "cafearrcrddebit00001", acctKey, "2026-08-01T09:00:00Z", 1000)
	creditAt(t, ctx, conn, cp, cons, "cafearrcrdpay000001", acctKey, "2026-08-05T09:00:00Z", 1000)
	refundAs(t, ctx, conn, cp, cons, "cafearrcrdrefund0001",
		ledgerActorKey, acctKey, chargeKey, 1000, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -1000 {
		t.Fatalf("fixture precondition: balanceCents = %v, want -1000 (the account is in credit)", got)
	}

	// B = -1000 ≤ 0 AND B′ = -500 ≤ 0: still in credit, so no episode.
	debitAt(t, ctx, conn, cp, cons, "cafearrcrddebit00002", acctKey, "2026-08-20T09:00:00Z", 500)
	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("fixture precondition: the payment that cleared the tab must have written {evaluatedAt}")
	}
	if _, ok := data["dueAt"]; ok {
		t.Fatalf("a charge that leaves the account IN CREDIT must open no episode — the resident owes nothing: %+v", data)
	}

	// B = -500 ≤ 0 AND B′ = +100 > 0: NOW the episode opens, on this charge.
	debitAt(t, ctx, conn, cp, cons, "cafearrcrddebit00003", acctKey, "2026-08-21T09:00:00Z", 600)
	wantDue := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
	data = arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the charge that actually took the account into arrears starts the term", got, wantDue)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-21T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want that charge's postedAt", got)
	}
}

// TestArrears_EvaluateSendsOnceThenNothing (e) is the green bar: a resident past
// the net term is reminded ONCE per episode. The first evaluation stamps
// remindedFor + sentAt and emits the external.notification the bridge turns into
// a real message; a re-dispatch recomputes the same head, finds remindedFor
// already equal, and emits nothing at all.
func TestArrears_EvaluateSendsOnceThenNothing(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearssend")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRSNDLEASEKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrsendacct00001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrsenddebit0001", acctKey, "2026-08-01T09:00:00Z", 1425)
	wantDue := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "cafearrsendeval00001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q (the FIFO head recomputed from the account's own history)", got, wantDue)
	}
	if got, _ := data["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q — this is what closes the gap for the episode", got, wantDue)
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt", got)
	}

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("an overdue head must emit external.notification — the send is the whole point of the target")
	}
	wantRef := acctKey + ":" + wantDue
	if got, _ := notif["externalRef"].(string); got != wantRef {
		t.Fatalf("externalRef = %q, want %q (the episode key the adapter dedups on)", got, wantRef)
	}
	if got, _ := notif["idempotencyKey"].(string); got != wantRef {
		t.Fatalf("idempotencyKey = %q, want %q", got, wantRef)
	}
	if got, _ := notif["adapter"].(string); got != "notification" {
		t.Fatalf("adapter = %q, want notification", got)
	}
	if got, _ := notif["replyOp"].(string); got != "RecordCafeArrearsReminderNotification" {
		t.Fatalf("replyOp = %q, want RecordCafeArrearsReminderNotification", got)
	}
	params, _ := notif["params"].(map[string]any)
	if params == nil {
		t.Fatalf("the notification carries no params: %+v", notif)
	}
	if got, _ := params["accountKey"].(string); got != acctKey {
		t.Fatalf("params.accountKey = %q, want %q", got, acctKey)
	}
	if got, _ := params["leaseAppKey"].(string); got != leaseKey {
		t.Fatalf("params.leaseAppKey = %q, want %q (resolved live off the account's own heldFor link, not the payload)", got, leaseKey)
	}
	if got, _ := params["reminderType"].(string); got != "cafeArrears" {
		t.Fatalf("params.reminderType = %q, want cafeArrears", got)
	}
	if got, _ := params["balanceCents"].(float64); got != 1425 {
		t.Fatalf("params.balanceCents = %v, want 1425", got)
	}

	// The re-dispatch. Same account, same history, a later instant: the head is
	// unchanged, remindedFor already names it, and NOTHING goes out.
	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrsendeval00002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-23T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("a re-dispatched evaluation must send NOTHING for an episode already reminded for, got %+v", notif)
	}
	again := arrearsData(t, ctx, conn, acctKey)
	if got, _ := again["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt was re-stamped to %q — the original send record must be carried forward, not rewritten", got)
	}
	if got, _ := again["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q carried forward", got, wantDue)
	}
}

// TestArrears_EvaluateRearmsOnACoveredHead (f). A payment covered the charge the
// recorded due date came from, so the FIFO head is now a LATER charge with a
// later due date. The evaluation recomputes it, clears stale, carries no send
// record (none was made), and the recomputed date re-arms the timer — with no
// notification, because nothing is overdue yet.
func TestArrears_EvaluateRearmsOnACoveredHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrearm")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRRRMLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrrearmacct0001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrrearmdebit001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "cafearrrearmdebit002", acctKey, "2026-08-20T09:00:00Z", 500)
	creditAt(t, ctx, conn, cp, cons, "cafearrrearmpay00001", acctKey, "2026-08-21T09:00:00Z", 1000)
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: the partial payment must have marked the state stale")
	}

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "cafearrrearmeval0001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	// The Aug 1 charge was fully paid off, so the head is the Aug 20 one.
	wantDue := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the FIFO head moved to the surviving charge", got, wantDue)
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("the evaluation IS the recomputation stale asked for, so it must clear it: %+v", data)
	}
	if _, ok := data["remindedFor"]; ok {
		t.Fatalf("nothing has been reminded for this episode: %+v", data)
	}
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a head that is not yet due must send nothing, got %+v", notif)
	}
}

// TestArrears_EvaluateNetsARefundAgainstItsCharge proves arrears_head's
// netting pre-pass matches the statement's own rule (cmd/cafe-app/ledger.go
// deriveStatement), not plain FIFO: a refund that names a charge
// (reversesRef) retires that charge directly, wherever the refund itself
// happens to sort chronologically — refundEnv fixes SubmittedAt at
// 2026-07-11, AHEAD of both charges below (a fixture quirk, not something
// this test chose for its shape), so the refund's own postedAt predates
// even the charge it names. Under plain FIFO that would make it an
// ordinary early credit: nothing open yet to apply to, so it would carry
// forward as surplus and prepay whichever debit posts next (A, the
// older one) — leaving B open as the head by coincidence of timing, not
// because the refund named it. The netting pre-pass reads reversesRef
// independent of row order, so the SAME refund instead retires B
// specifically, and it is A — untouched by the reversal — that is left
// open as the head. The dueAt asserted below is the netting rule's
// answer; no FIFO application of these rows, timing coincidence included,
// produces it.
func TestArrears_EvaluateNetsARefundAgainstItsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnetref")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRNETLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrnetacct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrnetdebita0001", acctKey, "2026-08-01T09:00:00Z", 1000)
	chargeB := debitAt(t, ctx, conn, cp, cons, "cafearrnetdebitb0001", acctKey, "2026-08-20T09:00:00Z", 1000)
	refundAs(t, ctx, conn, cp, cons, "cafearrnetrefund0001",
		ledgerActorKey, acctKey, chargeB, 1000, "", processor.OutcomeAccepted)

	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: the refund must have marked the state stale")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearrnetevalXX0001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	// The refund names B directly and retires it regardless of the refund's
	// own (earlier) postedAt, leaving A — untouched — as the head. A plain
	// FIFO reading of these rows would have the refund arrive before A even
	// posts, so it would carry forward as surplus and prepay A instead,
	// leaving B as the head: the opposite of what netting produces here.
	wantDue := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — the reversal retires B directly, leaving A (Aug 1) as the head", got, wantDue)
	}
}

// TestArrears_OneNotificationPerEpisodeNotPerHead (f2) is the once-per-episode
// guarantee at its only hard case. An EPISODE is the stretch from the charge
// that takes the account from square to owing until the balance comes back to
// zero; the FIFO HEAD moves within one episode every time a partial payment
// retires the oldest charge. A resident who pays SOMETHING off is doing the
// right thing, and if the send were keyed on the head — on remindedFor naming
// this dueAt — every part-payment would hand them a second nag for the same
// continuous debt, because the charge the head moves to is usually past its own
// term too. The send is keyed on sentAt's ABSENCE instead, which is a fact about
// the episode. remindedFor is still written every time, because that is what
// closes the convergence gap; the two are deliberately different questions.
func TestArrears_OneNotificationPerEpisodeNotPerHead(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsepisode")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARREPSLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrepsacct000001", leaseKey)

	debitAt(t, ctx, conn, cp, cons, "cafearrepsdebit00001", acctKey, "2026-08-01T09:00:00Z", 1000)
	debitAt(t, ctx, conn, cp, cons, "cafearrepsdebit00002", acctKey, "2026-08-10T09:00:00Z", 1000)
	dueC1 := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
	dueC2 := time.Date(2026, 8, 10, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)

	// The first charge falls due and the reminder goes out.
	_, reqID1 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrepseval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-17T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID1) == nil {
		t.Fatal("the first overdue head in an episode must send")
	}
	sent := arrearsData(t, ctx, conn, acctKey)
	if got, _ := sent["remindedFor"].(string); got != dueC1 {
		t.Fatalf("remindedFor = %q, want %q", got, dueC1)
	}
	if got, _ := sent["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt", got)
	}

	// A PARTIAL payment retires the first charge exactly. The head moves to the
	// second, whose own term has also passed — and nothing may go out for it.
	creditAt(t, ctx, conn, cp, cons, "cafearrepspay000001", acctKey, "2026-08-18T09:00:00Z", 1000)
	if stale, _ := arrearsData(t, ctx, conn, acctKey)["stale"].(bool); !stale {
		t.Fatal("fixture precondition: a partial payment marks the state stale")
	}

	_, reqID2 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrepseval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-27T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID2); notif != nil {
		t.Fatalf("the head moved WITHIN one episode — a resident paying their tab down must not be nagged twice: %+v", notif)
	}
	moved := arrearsData(t, ctx, conn, acctKey)
	if got, _ := moved["dueAt"].(string); got != dueC2 {
		t.Fatalf("dueAt = %q, want %q (the head moved to the surviving charge)", got, dueC2)
	}
	if got, _ := moved["remindedFor"].(string); got != dueC2 {
		t.Fatalf("remindedFor = %q, want %q — remindedFor tracks the HEAD, and leaving it on the retired charge holds the convergence gap open forever", got, dueC2)
	}
	if got, _ := moved["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the episode's send record is carried, not re-stamped", got)
	}
	if _, ok := moved["stale"]; ok {
		t.Fatalf("the evaluation IS the recomputation stale asked for: %+v", moved)
	}

	// Paying the tab off ENDS the episode: the send record goes with it.
	creditAt(t, ctx, conn, cp, cons, "cafearrepspay000002", acctKey, "2026-08-28T09:00:00Z", 1000)
	cleared := arrearsData(t, ctx, conn, acctKey)
	if _, ok := cleared["sentAt"]; ok {
		t.Fatalf("a paid-off account keeps no send record — the NEXT episode must be able to send: %+v", cleared)
	}
	if _, ok := cleared["dueAt"]; ok {
		t.Fatalf("a paid-off account carries no dueAt: %+v", cleared)
	}

	// A NEW tab, a new episode, and the reminder goes out again.
	debitAt(t, ctx, conn, cp, cons, "cafearrepsdebit00003", acctKey, "2026-08-29T09:00:00Z", 500)
	if _, ok := arrearsData(t, ctx, conn, acctKey)["sentAt"]; ok {
		t.Fatal("a fresh episode starts with no send record")
	}
	_, reqID3 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrepseval000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-09-14T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, reqID3) == nil {
		t.Fatal("a NEW episode past its term must send — one per episode is not one per account")
	}
}

// TestArrears_ForgedSendRefused (g) is the actor guard. ledgerActorKey holds the
// operator role AND the identical Scope:"any" EvaluateCafeArrears grant, so step
// 3 authorizes it; only `op.actor != primordialActor["weaver"]` stops it from
// having the platform tell an arbitrary resident they owe money. Nothing is
// written either — a marker minted on a forged send would close the gap and
// suppress the real reminder.
func TestArrears_ForgedSendRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsforged")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRFGDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrfgdacct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrfgddebit00001", acctKey, "2026-08-01T09:00:00Z", 1425)
	before := arrearsData(t, ctx, conn, acctKey)

	reply, _ := evaluateArrears(t, ctx, conn, cp, cons, "cafearrfgdeval000001",
		ledgerActorKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied") {
		t.Fatalf("want an AuthDenied rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "Weaver's dispatch actor") {
		t.Fatalf("the denial must name the actor guard, got %q", reply.Error.Message)
	}
	after := arrearsData(t, ctx, conn, acctKey)
	if _, ok := after["sentAt"]; ok {
		t.Fatalf("a refused evaluation must record no send: %+v", after)
	}
	if after["evaluatedAt"] != before["evaluatedAt"] {
		t.Fatalf("a refused evaluation must write nothing at all; evaluatedAt moved from %v to %v", before["evaluatedAt"], after["evaluatedAt"])
	}
}

// TestArrears_LeaseResolvedFromAccountState (g2a). leaseAppKey never travels
// through the payload — a Weaver Params reference to a null lens column
// (an account with no live heldFor lease) would otherwise make the
// strategist refuse to dispatch the row at all (internal/weaver/strategist.go),
// silently starving the very accounts most worth aging. The op resolves the
// lease itself, live, off the account's own heldFor out-link, so it decides
// this with no payload field to trust or forge. TestArrears_EvaluateSendsOnceThenNothing
// already asserts the resolved key lands in the notification's params; this is
// that guarantee's own dedicated positive vector, verifying it comes from
// state rather than incidentally matching a payload value that no longer
// exists.
func TestArrears_LeaseResolvedFromAccountState(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsleasestate")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRLSSLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrlssacct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrlssdebit00001", acctKey, "2026-08-01T09:00:00Z", 1425)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "cafearrlsseval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-due account must send")
	}
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["leaseAppKey"].(string); got != leaseKey {
		t.Fatalf("params.leaseAppKey = %q, want %q — resolved live off the account's own heldFor link", got, leaseKey)
	}
}

// TestArrears_NoHeldForLeaseStillEvaluates (g2b) is the other half: an account
// that carries no live heldFor lease at all — never bound to one, or bound to
// one that has since gone dead — is still evaluated, because the arrears fact
// is about the ACCOUNT, not the lease it happens to be held for. Before the
// lease moved off Params this shape was UNREACHABLE: Weaver's strategist
// refuses to dispatch any row whose Params reference a null column, so this
// account's gap simply never opened. A past-due evaluation on it still sends
// — with no leaseAppKey in the notification's params, because none resolves.
func TestArrears_NoHeldForLeaseStillEvaluates(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnolease")

	acctKey := "vtx.cafeaccount.BBCAFEARRNLSACCTHJKM"
	seedVertex(t, ctx, conn, acctKey, "cafeaccount", map[string]any{})
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEARRNLSTXNAHJKM", "debit", 1425)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "cafearrnolseval00001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)

	wantDue := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
	data := arrearsData(t, ctx, conn, acctKey)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q — a leaseless account ages from its own history same as any other", got, wantDue)
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the evaluation's own submittedAt — a leaseless account still gets its one reminder", got)
	}

	notif := arrearsNotification(t, ctx, conn, reqID)
	if notif == nil {
		t.Fatal("a past-due account with no lease must still send")
	}
	params, _ := notif["params"].(map[string]any)
	if _, ok := params["leaseAppKey"]; ok {
		t.Fatalf("no lease resolved, so params must carry no leaseAppKey: %+v", params)
	}
}

// TestArrears_LegacyAccountOnlyEverMarksStale (h). An account minted under
// cafe-ledger < 0.4.0 carries no .balance, so a posted entry has no before/after
// balance and cannot tell an episode opening from an episode continuing. It may
// only mark EXISTING state stale — never mint arrears state off a number it does
// not have, which would record a due date computed from one entry over a history
// it never counted.
func TestArrears_LegacyAccountOnlyEverMarksStale(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearslegacy")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRLGCLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEARRLGCACCTHJKM", leaseKey)

	// No .arrears yet: a charge against a legacy account mints nothing. Such an
	// account is already opening the never-evaluated gap on its missing
	// evaluatedAt, so there is nothing to record and nothing to lose.
	debitAt(t, ctx, conn, cp, cons, "cafearrlgcdebit00001", acctKey, "2026-08-01T09:00:00Z", 1425)
	if data := arrearsData(t, ctx, conn, acctKey); data != nil {
		t.Fatalf("a legacy account has no balance to reason from, so an entry must mint NO arrears state: %+v", data)
	}

	// Now give it arrears state, as the Weaver-dispatched evaluation would.
	seedAspect(t, ctx, conn, acctKey, "arrears", "cafeAccountArrears", map[string]any{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-22T09:00:00Z",
		"evaluatedAt": "2026-08-22T09:00:00Z",
	})
	debitAt(t, ctx, conn, cp, cons, "cafearrlgcdebit00002", acctKey, "2026-08-25T09:00:00Z", 500)

	data := arrearsData(t, ctx, conn, acctKey)
	if stale, _ := data["stale"].(bool); !stale {
		t.Fatalf("an entry against a legacy account carrying arrears state must mark it stale: %+v", data)
	}
	if got, _ := data["sentAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("sentAt = %q — the send record must be CARRIED, not dropped, or the resident is reminded twice for one debt", got)
	}
	if got, _ := data["remindedFor"].(string); got != "2026-08-16T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the recorded episode carried forward", got)
	}
}

// TestArrears_HistoryPastTheBudgetDegrades (k) is the exhaustion path, and the
// claim is that it is a DEGRADE and not a stop. The replay is paged — one
// kv.Links page of ARREARS_PAGE_LIMIT entries per dispatch, the running
// aggregate checkpointed on the account between pages — and capped at
// ARREARS_MAX_PAGES pages, so an account can genuinely outrun it, and the op
// then cannot name a head. A refusal there would be permanent and SILENT: the
// only thing that re-drives this op is the convergence gap the account's own
// row opens, and a rejected op never closes it, so Weaver would re-dispatch the
// same doomed evaluation on every window — no reminder, no error anyone reads,
// forever.
//
// So the op records the exhaustion instead, on the dispatch AFTER the last
// in-budget page. It is ACCEPTED, it sends nothing, it leaves everything already
// recorded untouched (a reminder already sent stays recorded as sent; a due
// date already armed is not erased by an evaluation that could not read the
// history), it records the budget it exhausted, and it drops stale and the
// checkpoint — which the lens pin TestCafeArrears_HistoryTooLongGoesQuiet
// turns into silence. Every page before it carried the episode record and sent
// nothing too. The second half is the way back out: the next posted entry
// drops the flag and its budget and re-marks the state stale, which re-opens
// the gap for exactly one more attempt.
func TestArrears_HistoryPastTheBudgetDegrades(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbudget")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRBGTLEASEHJK")
	// A LEGACY account (no .balance): that is what makes the second half's
	// DEBIT a stale-marking write. On an account with a balance cache a further
	// charge against an already-owing tab writes nothing at all, so a payment
	// would be the re-arming entry there.
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEARRBGTACCTHJKM", leaseKey)

	// One more posted entry than the budget, so the last in-budget page ends
	// with a live cursor and the budget genuinely runs out.
	overBudget := cafeledger.ArrearsPageLimit*cafeledger.ArrearsMaxPages + 1
	for i := 0; i < overBudget; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	// The state a previous, in-budget evaluation left: an episode reminded for.
	seedAspect(t, ctx, conn, acctKey, "arrears", "cafeAccountArrears", map[string]any{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
		"evaluatedAt": "2026-08-17T09:00:00Z",
		"stale":       true,
	})
	carried := map[string]string{
		"dueAt":       "2026-08-16T09:00:00Z",
		"remindedFor": "2026-08-16T09:00:00Z",
		"sentAt":      "2026-08-17T09:00:00Z",
	}

	// Drive one dispatch per page until the degrade lands; every page before
	// it is a checkpoint that carries the record and sends nothing.
	dispatches := 0
	var data map[string]any
	for dispatches < cafeledger.ArrearsMaxPages+2 {
		dispatches++
		_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "cafearrbgteval"+strconv.Itoa(100000+dispatches),
			bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
		if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
			t.Fatalf("dispatch %d: an evaluation that has not read the whole history must send nothing — it does not know the head: %+v", dispatches, notif)
		}
		data = arrearsData(t, ctx, conn, acctKey)
		for field, want := range carried {
			if got, _ := data[field].(string); got != want {
				t.Fatalf("dispatch %d: %s = %q, want %q carried untouched — a page that read nothing whole must erase nothing", dispatches, field, got, want)
			}
		}
		if flag, _ := data["historyTooLong"].(bool); flag {
			break
		}
		replay, _ := data["replay"].(map[string]any)
		if replay == nil {
			t.Fatalf("dispatch %d: neither a checkpoint nor the degrade: %+v", dispatches, data)
		}
		if got, _ := replay["pages"].(float64); int(got) != dispatches {
			t.Fatalf("dispatch %d: replay.pages = %v, want %d — one page per dispatch", dispatches, replay["pages"], dispatches)
		}
		if got, _ := data["evaluatedAt"].(string); got != "2026-08-17T09:00:00Z" {
			t.Fatalf("dispatch %d: evaluatedAt = %q — a page is not an evaluation, so the last completed one's stamp stands", dispatches, got)
		}
	}
	if dispatches != cafeledger.ArrearsMaxPages+1 {
		t.Fatalf("the degrade landed on dispatch %d, want %d — every in-budget page is consumed first, then one more dispatch records the exhaustion", dispatches, cafeledger.ArrearsMaxPages+1)
	}
	if got, _ := data["historyBudget"].(float64); int(got) != cafeledger.ArrearsPageLimit*cafeledger.ArrearsMaxPages {
		t.Fatalf("historyBudget = %v, want %d — the budget the flag was recorded under, so a later larger one can tell it apart", data["historyBudget"], cafeledger.ArrearsPageLimit*cafeledger.ArrearsMaxPages)
	}
	if _, ok := data["replay"]; ok {
		t.Fatalf("the degrade ends the replay, so the checkpoint must not survive it — a surviving phase would hold a gap open under the flag: %+v", data)
	}
	if _, ok := data["stale"]; ok {
		t.Fatalf("stale asks for a recomputation this op has just attempted; re-asking re-opens the gap the degrade closes: %+v", data)
	}
	if got, _ := data["evaluatedAt"].(string); got != "2026-08-22T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the degrading evaluation's own submittedAt", got)
	}

	// The way back out: one more posted entry, one more attempt.
	debitAt(t, ctx, conn, cp, cons, "cafearrbgtdebit00001", acctKey, "2026-08-25T09:00:00Z", 500)
	after := arrearsData(t, ctx, conn, acctKey)
	if _, ok := after["historyTooLong"]; ok {
		t.Fatalf("a posted entry must drop the flag, or the row stays quiet for the life of the account: %+v", after)
	}
	if _, ok := after["historyBudget"]; ok {
		t.Fatalf("and the budget recorded beside it — a budget with no flag records nothing: %+v", after)
	}
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("and re-mark the state stale, which is what re-opens the gap for that one attempt: %+v", after)
	}
	if got, _ := after["sentAt"].(string); got != "2026-08-17T09:00:00Z" {
		t.Fatalf("sentAt = %q — the send record is still carried across the re-arming entry", got)
	}
}

// replayTxID encodes (prefix, i) as a valid 20-char NanoID whose first
// character is `lead`, so a fixture can place an entry on a chosen page: the
// postedTo enumeration pages the SORTED link keys, and the transaction id is
// the first varying segment of lnk.cafetransaction.<id>.postedTo…, so ids
// beginning 'A' precede every id beginning 'z'. i is encoded in the last three
// characters over an alphabet that sorts in index order for i < 48.
func replayTxID(lead byte, i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	n := len(safe)
	return string(lead) + "BBREPLAYTXAHJKMN" + string([]byte{safe[i/(n*n)%n], safe[(i/n)%n], safe[i%n]})
}

// seedReplayAccount seeds a legacy account (no .balance — so a later posted
// debit is a stale-marking write) and returns it.
func seedReplayAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseID, acctID string) string {
	t.Helper()
	leaseKey := seedLease(t, ctx, conn, leaseID)
	return seedLegacyAccount(t, ctx, conn, acctID, leaseKey)
}

// arrearsReplay reads the checkpoint off the account's .arrears, or nil when
// none is recorded.
func arrearsReplay(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	replay, _ := data["replay"].(map[string]any)
	return replay
}

// TestArrears_TwoPageHistoryCompletesInTwoDispatches is the resumable replay's
// green bar: a history one entry longer than a page is evaluated EXACTLY across
// two dispatches. The first consumes one page and records a checkpoint — phase
// a, one page, a live cursor, the running aggregate — and nothing else: no
// notification, no evaluatedAt (this account has never been evaluated, and a
// page is not an evaluation). The second exhausts the enumeration, drops the
// checkpoint, computes the FIFO head from the aggregate and, the head being
// overdue, sends once. The head is the one a single-execution replay over the
// same entries names: twenty-five charges a day apart and six payments of one
// charge each, interleaved across the two pages, pay off the six oldest
// charges wherever the payments sat in the walk, so the head is the seventh
// charge.
func TestArrears_TwoPageHistoryCompletesInTwoDispatches(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearstwopage")

	acctKey := seedReplayAccount(t, ctx, conn, "BBCAFEARRTW2LEASEHJK", "BBCAFEARRTW2ACCTHJKM")

	// 31 entries in id (= page) order: charges at i = 0..24, payments at the
	// six slots 3, 9, 15, 21, 27, 30 — the last of them on the second page.
	const pageLimit = cafeledger.ArrearsPageLimit
	total := pageLimit + 1
	paymentSlots := map[int]bool{3: true, 9: true, 15: true, 21: true, 27: true, 30: true}
	day := 0
	wantHead := ""
	for i := 0; i < total; i++ {
		if paymentSlots[i] {
			seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "credit", 100, "2026-05-01T09:00:00Z", "")
			continue
		}
		postedAt := time.Date(2026, 5, 1+day, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
		if day == 6 {
			wantHead = postedAt
		}
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, postedAt, "")
		day++
	}
	if day != 25 || wantHead == "" {
		t.Fatalf("fixture: %d charges seeded, want 25 with the seventh as the head", day)
	}

	// Dispatch 1: one page, a checkpoint, nothing else.
	_, req1 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrtwoeval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, req1); notif != nil {
		t.Fatalf("a page is not an evaluation — nothing may be sent before the head is known: %+v", notif)
	}
	first := arrearsData(t, ctx, conn, acctKey)
	replay := arrearsReplay(t, first)
	if replay == nil {
		t.Fatalf("a history longer than one page must leave a checkpoint: %+v", first)
	}
	if got, _ := replay["phase"].(string); got != cafeledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q — the first page opens missing_replay_a", got, cafeledger.ArrearsPhaseA)
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1", replay["pages"])
	}
	if got, _ := replay["cursor"].(string); got == "" {
		t.Fatalf("replay.cursor must be the live cursor the next page resumes from: %+v", replay)
	}
	if _, ok := first["evaluatedAt"]; ok {
		t.Fatalf("this account has never been evaluated and a page is not an evaluation, so no evaluatedAt may be stamped: %+v", first)
	}
	if _, ok := first["dueAt"]; ok {
		t.Fatalf("no head is known mid-replay: %+v", first)
	}
	debits, _ := replay["debits"].(map[string]any)
	if len(debits) != pageLimit-5 {
		t.Fatalf("replay.debits carries %d charges after the first page, want %d (the page's 30 entries less its five payments)", len(debits), pageLimit-5)
	}
	if got, _ := replay["creditCents"].(float64); got != 500 {
		t.Fatalf("replay.creditCents = %v after the first page, want 500 (five payments of 100)", replay["creditCents"])
	}

	// Dispatch 2: the enumeration is exhausted, the head is computed and sent for.
	_, req2 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrtwoeval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-23T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("the finalize page writes no checkpoint: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != dueFor(t, wantHead) {
		t.Fatalf("dueAt = %q, want %q — six payments retire the six oldest charges whatever page they sat on, so the seventh charge is the head", got, dueFor(t, wantHead))
	}
	if got, _ := final["evaluatedAt"].(string); got != "2026-08-23T09:00:00Z" {
		t.Fatalf("evaluatedAt = %q, want the finalizing dispatch's own submittedAt", got)
	}
	if got, _ := final["sentAt"].(string); got != "2026-08-23T09:00:00Z" {
		t.Fatalf("sentAt = %q, want the finalizing dispatch's own submittedAt — the head is overdue and nothing has gone out", got)
	}
	notif := arrearsNotification(t, ctx, conn, req2)
	if notif == nil {
		t.Fatal("the finalize page must send for an overdue head exactly as a one-page evaluation does")
	}
	params, _ := notif["params"].(map[string]any)
	if got, _ := params["balanceCents"].(float64); got != 1900 {
		t.Fatalf("params.balanceCents = %v, want 1900 (25 × 100 − 6 × 100) — the aggregate's balance is the account's", got)
	}

	// A re-dispatch over the finished history: one page again, and it fits, so
	// it finalizes in place and sends nothing more.
	_, req3 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrtwoeval000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-24T09:00:00Z", processor.OutcomeAccepted)
	if arrearsNotification(t, ctx, conn, req3) != nil {
		t.Fatal("the episode is already reminded for")
	}
	if got, _ := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))["phase"].(string); got != cafeledger.ArrearsPhaseA {
		t.Fatalf("a fresh evaluation over a two-page history starts again at phase %q, got %q", cafeledger.ArrearsPhaseA, got)
	}
}

// TestArrears_RefundOnAnEarlierPageNetsItsCharge proves the aggregate's
// netting is exact ACROSS pages — the reason the checkpoint keys its debits and
// reversals by transaction id. The refunded charge sits on page 1 and the
// credit that reverses it on page 2 (ids chosen to sort on opposite sides of
// the page boundary), so no single execution sees both. The oldest charge A is
// older than the refunded charge C: plain FIFO would spend the credit on A and
// name the first small charge as the head; the netting retires C specifically
// and leaves A — the charge the resident actually still owes — as the head,
// the same rule the statement runs.
func TestArrears_RefundOnAnEarlierPageNetsItsCharge(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsxpage")

	acctKey := seedReplayAccount(t, ctx, conn, "BBCAFEARRXPGLEASEHJK", "BBCAFEARRXPGACCTHJKM")

	// Page 1 (30 entries, ids A…, B…, C…): charge A, 28 small charges B, the
	// charge C the refund names. Page 2 (1 entry, id z…): the refund.
	chargeA := replayTxID('A', 0)
	chargeC := replayTxID('C', 0)
	seedEntryAt(t, ctx, conn, acctKey, chargeA, "debit", 1000, "2026-05-01T12:00:00Z", "")
	for i := 0; i < cafeledger.ArrearsPageLimit-2; i++ {
		postedAt := time.Date(2026, 6, 1+i, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, postedAt, "")
	}
	seedEntryAt(t, ctx, conn, acctKey, chargeC, "debit", 1000, "2026-07-01T12:00:00Z", "")
	seedEntryAt(t, ctx, conn, acctKey, replayTxID('z', 0), "credit", 1000, "2026-07-02T12:00:00Z", chargeC)

	_, req1 := evaluateArrears(t, ctx, conn, cp, cons, "cafearrxpgeval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	first := arrearsData(t, ctx, conn, acctKey)
	replay := arrearsReplay(t, first)
	if replay == nil {
		t.Fatalf("fixture: 31 entries must leave a checkpoint after page 1: %+v", first)
	}
	if reversed, _ := replay["reversed"].(map[string]any); len(reversed) != 0 {
		t.Fatalf("fixture: the refund is on page 2, so page 1 records no reversal: %+v", reversed)
	}
	if debits, _ := replay["debits"].(map[string]any); debits[chargeC] == nil || debits[chargeA] == nil {
		t.Fatalf("fixture: both A and C are on page 1: %v", debits)
	}
	if arrearsNotification(t, ctx, conn, req1) != nil {
		t.Fatal("nothing is sent mid-replay")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearrxpgeval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-23T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("two pages, two dispatches: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != dueFor(t, "2026-05-01T12:00:00Z") {
		t.Fatalf("dueAt = %q, want %q — the refund on page 2 retires charge C on page 1, leaving A as the head; plain FIFO would have spent it on A", got, dueFor(t, "2026-05-01T12:00:00Z"))
	}
}

// TestArrears_PostedEntryMidReplayRestartsIt pins the checkpoint's reset
// boundary. A posted entry changes the set the cursor pages over, so a
// checkpoint taken before it no longer describes a prefix of the history:
// post_entry drops it on every branch and marks the state stale, the phase gap
// closes, missing_evaluation re-opens, and the next evaluation starts again at
// page 1 — never resumes a cursor over a set that has moved under it.
func TestArrears_PostedEntryMidReplayRestartsIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsrestart")

	acctKey := seedReplayAccount(t, ctx, conn, "BBCAFEARRRSTLEASEHJK", "BBCAFEARRRSTACCTHJKM")
	for i := 0; i <= cafeledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearrrsteval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if arrearsReplay(t, arrearsData(t, ctx, conn, acctKey)) == nil {
		t.Fatal("fixture: the first dispatch must leave a checkpoint")
	}

	// A charge against the legacy account: the stale-marking branch.
	debitAt(t, ctx, conn, cp, cons, "cafearrrstdebit00001", acctKey, "2026-08-25T09:00:00Z", 500)
	after := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, after) != nil {
		t.Fatalf("a posted entry must drop the checkpoint — the set under its cursor has changed: %+v", after)
	}
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("and mark the state stale, which is what re-opens the evaluation gap: %+v", after)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearrrsteval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-26T09:00:00Z", processor.OutcomeAccepted)
	replay := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if replay == nil {
		t.Fatal("32 entries are two pages, so the restarted evaluation checkpoints again")
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1 — the evaluation restarts at page 1, it does not resume a cursor over a moved set", replay["pages"])
	}
	if got, _ := replay["phase"].(string); got != cafeledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q on a fresh page 1", got, cafeledger.ArrearsPhaseA)
	}
}

// TestArrears_ChargeOnAnOwingAccountMidReplayRestartsIt is the reset boundary
// on the one post_entry shape that otherwise writes NOTHING: a charge against
// an account with a balance cache that already owes (the charge queues behind
// the open head, so no dueAt moves). With no replay in progress that shape
// stays a no-write. With one, the new transaction changes the set the
// checkpoint's cursor pages over — its key may sort at or below the cursor and
// be skipped — so the finalize page would name a head and a balance over a
// history missing an entry. post_entry therefore carries the state, drops the
// checkpoint and marks it stale exactly when a checkpoint is present, and the
// next evaluation restarts at page 1.
func TestArrears_ChargeOnAnOwingAccountMidReplayRestartsIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsowingrestart")

	// An account WITH a .balance: the opening charge posts through the op and
	// leaves the balance owing; thirty more entries are seeded around it.
	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRWNGLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrowracct000001", leaseKey)
	debitAt(t, ctx, conn, cp, cons, "cafearrowrdebit00001", acctKey, "2026-05-01T09:00:00Z", 2500)
	for i := 0; i < cafeledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, replayTxID('B', i), "debit", 100, "2026-05-02T12:00:00Z", "")
	}
	before := arrearsData(t, ctx, conn, acctKey)
	if before == nil || before["dueAt"] == nil {
		t.Fatalf("fixture: the opening charge must have opened an episode: %+v", before)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearrowreval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-05-03T09:00:00Z", processor.OutcomeAccepted)
	mid := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, mid) == nil {
		t.Fatalf("fixture: 31 entries must leave a checkpoint after page 1: %+v", mid)
	}

	// The charge that, on a non-replaying owing account, writes nothing.
	debitAt(t, ctx, conn, cp, cons, "cafearrowrdebit00002", acctKey, "2026-05-04T09:00:00Z", 500)
	after := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, after) != nil {
		t.Fatalf("a charge mid-replay must drop the checkpoint even where it would otherwise write nothing — the set under the cursor has changed: %+v", after)
	}
	if stale, _ := after["stale"].(bool); !stale {
		t.Fatalf("and mark the state stale, which re-opens the evaluation gap: %+v", after)
	}
	if after["dueAt"] != before["dueAt"] {
		t.Fatalf("the drop is a carry, not a rewrite: dueAt moved from %v to %v", before["dueAt"], after["dueAt"])
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearrowreval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-05-05T09:00:00Z", processor.OutcomeAccepted)
	replay := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if replay == nil {
		t.Fatal("32 entries are two pages, so the restarted evaluation checkpoints again")
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1 — the evaluation restarts at page 1", replay["pages"])
	}

	// The control: the same charge shape with NO replay in progress stays a
	// no-write. Finish the replay first, then charge again.
	evaluateArrears(t, ctx, conn, cp, cons, "cafearrowreval000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-05-06T09:00:00Z", processor.OutcomeAccepted)
	settled := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, settled) != nil {
		t.Fatalf("fixture: the second page finalizes: %+v", settled)
	}
	debitAt(t, ctx, conn, cp, cons, "cafearrowrdebit00003", acctKey, "2026-05-07T09:00:00Z", 500)
	control := arrearsData(t, ctx, conn, acctKey)
	if _, ok := control["stale"]; ok {
		t.Fatalf("with no checkpoint to drop, a charge that queues behind the head writes nothing: %+v", control)
	}
	if control["evaluatedAt"] != settled["evaluatedAt"] {
		t.Fatalf("with no checkpoint to drop, a charge that queues behind the head writes nothing; evaluatedAt moved from %v to %v", settled["evaluatedAt"], control["evaluatedAt"])
	}
}

// TestArrears_MalformedCheckpointRestartsAtPageOne pins the op's side of the
// hardening the lens pin TestCafeArrears_MalformedCheckpointReopensEvaluation
// covers: a recorded checkpoint the op cannot resume — an unknown phase here —
// is treated as absent, so the evaluation the re-opened gap dispatches starts
// at page 1 over a fresh aggregate and records a well-formed checkpoint in its
// place, rather than folding onto a corrupt one or refusing.
func TestArrears_MalformedCheckpointRestartsAtPageOne(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsbadcheckpoint")

	acctKey := seedReplayAccount(t, ctx, conn, "BBCAFEARRBADLEASEHJK", "BBCAFEARRBADACCTHJKM")
	for i := 0; i <= cafeledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}
	seedAspect(t, ctx, conn, acctKey, "arrears", "cafeAccountArrears", map[string]any{
		"evaluatedAt": "2026-05-17T09:00:00Z",
		"stale":       true,
		"replay": map[string]any{
			"phase":       "x",
			"cursor":      "lnk.cafetransaction." + budgetTxID(5) + ".postedTo.cafeaccount.BBCAFEARRBADACCTHJKM",
			"pages":       7,
			"debits":      map[string]any{budgetTxID(0): map[string]any{"postedAt": "2026-01-01T00:00:00Z", "amountCents": 999999}},
			"reversed":    map[string]any{},
			"creditCents": 0,
		},
	})

	evaluateArrears(t, ctx, conn, cp, cons, "cafearrbadeval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	replay := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if replay == nil {
		t.Fatal("31 entries are two pages, so the restarted evaluation checkpoints")
	}
	if got, _ := replay["pages"].(float64); got != 1 {
		t.Fatalf("replay.pages = %v, want 1 — an unresumable checkpoint is treated as absent, not continued from", replay["pages"])
	}
	if got, _ := replay["phase"].(string); got != cafeledger.ArrearsPhaseA {
		t.Fatalf("replay.phase = %q, want %q on a fresh page 1", got, cafeledger.ArrearsPhaseA)
	}
	debits, _ := replay["debits"].(map[string]any)
	if len(debits) != cafeledger.ArrearsPageLimit {
		t.Fatalf("replay.debits carries %d charges, want %d — a fresh aggregate over page 1, not the corrupt one folded onto", len(debits), cafeledger.ArrearsPageLimit)
	}
	if d, _ := debits[budgetTxID(0)].(map[string]any); d == nil || d["amountCents"].(float64) != 100 {
		t.Fatalf("the corrupt aggregate's entry must be replaced by the page's own reading: %v", debits[budgetTxID(0)])
	}
}

// TestArrears_RedeliveredPageAdvancesNeverRepeats pins the replay-under-
// redelivery rule: a dispatch that finds a checkpoint consumes the NEXT page,
// so two dispatches after page 1 advance the checkpoint 1 → 2 (never 1 → 1),
// and the phase alternates a → b — the flip that closes the gap that
// dispatched the page and opens its sibling. Three pages of history so the
// second dispatch is still mid-replay; the third finalizes.
func TestArrears_RedeliveredPageAdvancesNeverRepeats(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsadvance")

	acctKey := seedReplayAccount(t, ctx, conn, "BBCAFEARRADVLEASEHJK", "BBCAFEARRADVACCTHJKM")
	for i := 0; i < 2*cafeledger.ArrearsPageLimit+1; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearradveval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	first := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if first == nil {
		t.Fatal("fixture: page 1 must checkpoint")
	}
	if pages, _ := first["pages"].(float64); pages != 1 {
		t.Fatalf("after one dispatch replay.pages = %v, want 1", first["pages"])
	}
	if phase, _ := first["phase"].(string); phase != cafeledger.ArrearsPhaseA {
		t.Fatalf("after one dispatch replay.phase = %q, want %q", phase, cafeledger.ArrearsPhaseA)
	}
	firstCursor, _ := first["cursor"].(string)

	evaluateArrears(t, ctx, conn, cp, cons, "cafearradveval000002",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	second := arrearsReplay(t, arrearsData(t, ctx, conn, acctKey))
	if second == nil {
		t.Fatal("61 entries are three pages, so the second dispatch is still mid-replay")
	}
	if pages, _ := second["pages"].(float64); pages != 2 {
		t.Fatalf("after two dispatches replay.pages = %v, want 2 — a redelivered dispatch advances, it never repeats the page", second["pages"])
	}
	if phase, _ := second["phase"].(string); phase != cafeledger.ArrearsPhaseB {
		t.Fatalf("after two dispatches replay.phase = %q, want %q — the flip is what closes missing_replay_a and opens missing_replay_b", phase, cafeledger.ArrearsPhaseB)
	}
	if cursor, _ := second["cursor"].(string); cursor == "" || cursor <= firstCursor {
		t.Fatalf("the cursor must advance past page 1's (%q), got %q", firstCursor, cursor)
	}
	if debits, _ := second["debits"].(map[string]any); len(debits) != 2*cafeledger.ArrearsPageLimit {
		t.Fatalf("replay.debits carries %d charges after two pages, want %d", len(debits), 2*cafeledger.ArrearsPageLimit)
	}

	evaluateArrears(t, ctx, conn, cp, cons, "cafearradveval000003",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	final := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, final) != nil {
		t.Fatalf("the third page exhausts the enumeration and finalizes: %+v", final)
	}
	if got, _ := final["dueAt"].(string); got != dueFor(t, "2026-05-01T12:00:00Z") {
		t.Fatalf("dueAt = %q, want %q over all 61 charges", got, dueFor(t, "2026-05-01T12:00:00Z"))
	}
}

// TestArrears_MidReplayCarriesTheEpisodeRecord pins what a page WRITES: the
// checkpoint, and every other recorded field verbatim. A page has evaluated
// nothing, so the episode's due date, the head it was reminded for, the send
// record and the stale mark all still describe the account exactly as the last
// completed evaluation or posted entry left them — dropping the send record
// here would send twice for one debt once the finalize page found the head
// overdue — and evaluatedAt keeps naming that last completed evaluation.
// Nothing is sent.
func TestArrears_MidReplayCarriesTheEpisodeRecord(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearscarry")

	acctKey := seedReplayAccount(t, ctx, conn, "BBCAFEARRCRYLEASEHJK", "BBCAFEARRCRYACCTHJKM")
	for i := 0; i <= cafeledger.ArrearsPageLimit; i++ {
		seedEntryAt(t, ctx, conn, acctKey, budgetTxID(i), "debit", 100, "2026-05-01T12:00:00Z", "")
	}
	seeded := map[string]any{
		"dueAt":       "2026-05-16T12:00:00Z",
		"remindedFor": "2026-05-16T12:00:00Z",
		"sentAt":      "2026-05-17T09:00:00Z",
		"evaluatedAt": "2026-05-17T09:00:00Z",
		"stale":       true,
	}
	seedAspect(t, ctx, conn, acctKey, "arrears", "cafeAccountArrears", seeded)

	_, reqID := evaluateArrears(t, ctx, conn, cp, cons, "cafearrcryeval000001",
		bootstrap.WeaverIdentityKey, acctKey, "2026-08-22T09:00:00Z", processor.OutcomeAccepted)
	if notif := arrearsNotification(t, ctx, conn, reqID); notif != nil {
		t.Fatalf("a page sends nothing: %+v", notif)
	}
	data := arrearsData(t, ctx, conn, acctKey)
	if arrearsReplay(t, data) == nil {
		t.Fatalf("a checkpoint is the one thing the page adds: %+v", data)
	}
	for field, want := range seeded {
		if got := data[field]; got != want {
			t.Fatalf("%s = %v, want %v carried verbatim — a page evaluates nothing and may rewrite nothing", field, got, want)
		}
	}
	if len(data) != len(seeded)+1 {
		t.Fatalf("the page writes the seeded fields plus replay and nothing else, got %+v", data)
	}
}

// recordArrearsNotification submits one RecordCafeArrearsReminderNotification
// exactly as the bridge does: no Class (the Processor's operationType→class
// reverse index resolves the handler) and NO ContextHint at all — the generic
// dispatch path declares nothing, which is why the op reads nothing and why
// externalRef is the only thing that decides which vertex it writes to.
func recordArrearsNotification(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, externalRef, status string, want processor.MessageOutcome) *processor.OperationReply {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RecordCafeArrearsReminderNotification",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-22T09:00:05Z",
		Payload:       json.RawMessage(`{"externalRef":"` + externalRef + `","status":"` + status + `","result":"notification sent"}`),
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != want {
		t.Fatalf("%s: outcome = %v, want %v (reply: %+v)", label, outcome, want, reply.Error)
	}
	return reply
}

// TestArrearsNotification_ForgedExternalRefRefused (l). externalRef arrives from
// OUTSIDE the platform — the adapter echoes it back through the bridge — and it
// is the op's only say over which vertex the outcome aspect is hung on. Splitting
// it and trusting the left half means any 3-segment vtx key names a target: an
// externalRef of "vtx.identity.<NanoID>:<dueAt>" would write a
// cafeAccountArrearsNotification onto a RESIDENT'S IDENTITY, a vertex this
// package has no business touching at all. The type check is what stops it.
//
// The accepted vector runs first, so the refusal below is attributable to the
// type and not to a guard that denies every reply — the two submissions differ
// in exactly one segment.
func TestArrearsNotification_ForgedExternalRefRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsnotif")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRNTFLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrntfacct000001", leaseKey)

	recordArrearsNotification(t, ctx, conn, cp, cons, "cafearrntfok00000001",
		acctKey+":2026-08-16T09:00:00Z", "completed", processor.OutcomeAccepted)
	outcome := arrearsNotificationOutcome(t, ctx, conn, acctKey)
	if got, _ := outcome["status"].(string); got != "completed" {
		t.Fatalf("status = %q, want completed", got)
	}
	if got, _ := outcome["remindedFor"].(string); got != "2026-08-16T09:00:00Z" {
		t.Fatalf("remindedFor = %q, want the dueAt half of the externalRef", got)
	}

	// The same submission with the account key's TYPE segment swapped.
	victim := "vtx.identity." + ledgerActorID
	reply := recordArrearsNotification(t, ctx, conn, cp, cons, "cafearrntfforged0001",
		victim+":2026-08-16T09:00:00Z", "completed", processor.OutcomeRejected)
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument") {
		t.Fatalf("want an InvalidArgument rejection, got %+v", reply.Error)
	}
	if !strings.Contains(reply.Error.Message, "externalRef") {
		t.Fatalf("the refusal must name the field it refused, got %q", reply.Error.Message)
	}
	if keyExists(t, ctx, conn, victim+".arrearsNotification") {
		t.Fatalf("a forged externalRef wrote an aspect onto %s — the op must touch nothing but a cafeaccount", victim)
	}
}

// arrearsNotificationOutcome reads the audit aspect the replyOp writes.
func arrearsNotificationOutcome(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, acctKey+".arrearsNotification")
	if cls, _ := doc["class"].(string); cls != "cafeAccountArrearsNotification" {
		t.Fatalf("%s.arrearsNotification class = %q, want cafeAccountArrearsNotification", acctKey, cls)
	}
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.arrearsNotification carries no data", acctKey)
	}
	return data
}

// arrearsFIFOVector is one shared statement-aging vector: a history, and the due
// date the FIFO must age it to. The five below are COPIED FROM
// cmd/cafe-app/ledger_test.go's own deriveStatement vectors — the same entries,
// the same hand-derived expectations — which is what makes
// TestArrears_FIFOMatchesTheStatement a claim about AGREEMENT rather than about
// this implementation agreeing with itself. The FE's tests pin the display side
// against these; this pins the op's side.
type arrearsFIFOVector struct {
	name    string
	acctID  string
	leaseID string
	entries []struct {
		kind     string
		postedAt string
		cents    int
	}
	wantDue string
}

func arrearsFIFOVectors() []arrearsFIFOVector {
	type e = struct {
		kind     string
		postedAt string
		cents    int
	}
	return []arrearsFIFOVector{
		{
			name: "credits age off the oldest debit first", acctID: "BBCAFEAGEACCTAHJKMNP", leaseID: "BBCAFEAGELEASEAHJKMN",
			entries: []e{
				{"debit", "2026-08-01T00:00:00Z", 1000},
				{"debit", "2026-08-20T00:00:00Z", 500},
				{"credit", "2026-08-21T00:00:00Z", 1000},
			},
			wantDue: "2026-09-04T00:00:00Z",
		},
		{
			name: "a prepaid credit carries forward", acctID: "BBCAFEAGEACCTBHJKMNP", leaseID: "BBCAFEAGELEASEBHJKMN",
			entries: []e{
				{"credit", "2026-08-01T00:00:00Z", 1000},
				{"debit", "2026-08-02T00:00:00Z", 1000},
				{"debit", "2026-08-28T23:50:00Z", 1425},
			},
			wantDue: "2026-09-12T23:50:00Z",
		},
		{
			name: "an overpayment prepays later charges", acctID: "BBCAFEAGEACCTCHJKMNP", leaseID: "BBCAFEAGELEASECHJKMN",
			entries: []e{
				{"debit", "2026-08-01T00:00:00Z", 1425},
				{"credit", "2026-08-02T00:00:00Z", 5000},
				{"debit", "2026-08-03T00:00:00Z", 3000},
				{"debit", "2026-08-28T00:00:00Z", 2000},
			},
			wantDue: "2026-09-12T00:00:00Z",
		},
		{
			name: "a partial prepay and a later credit compose", acctID: "BBCAFEAGEACCTDHJKMNP", leaseID: "BBCAFEAGELEASEDHJKMN",
			entries: []e{
				{"credit", "2026-08-01T00:00:00Z", 500},
				{"debit", "2026-08-02T00:00:00Z", 1000},
				{"debit", "2026-08-20T00:00:00Z", 700},
				{"credit", "2026-08-21T00:00:00Z", 500},
			},
			wantDue: "2026-09-04T00:00:00Z",
		},
		{
			// A credit SMALLER than the head debit's remainder: it retires part
			// of that charge and the head does not move. Every other vector
			// clears the head outright or overshoots it, so this is the only one
			// that exercises the partial-retirement arm — and the one that would
			// pass if that arm popped the debit anyway, since the wrong answer
			// there (Sep 4, aged from the Aug 20 charge) is a date the other
			// vectors already produce legitimately.
			name: "a credit smaller than the head's remainder leaves the head", acctID: "BBCAFEAGEACCTEHJKMNP", leaseID: "BBCAFEAGELEASEEHJKMN",
			entries: []e{
				{"debit", "2026-08-01T00:00:00Z", 1000},
				{"debit", "2026-08-20T00:00:00Z", 700},
				{"credit", "2026-08-21T00:00:00Z", 400},
			},
			wantDue: "2026-08-16T00:00:00Z",
		},
	}
}

// fifoTxID encodes (vector, entry) as a valid 20-char NanoID so each vector's
// entries carry distinct keys without hand-writing an id per line.
func fifoTxID(v, i int) string {
	const safe = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"
	return "BBCAFEAGETXAHJKMNP" + string([]byte{safe[v%len(safe)], safe[i%len(safe)]})
}

// TestArrears_FIFOMatchesTheStatement (i) is the green bar's other half: no
// account is ever reminded for a balance it does not owe, because the op's aging
// and the resident's own statement agree. Each vector's entries are SEEDED
// (rather than posted through the ops) so the exact postedAt values the FE's
// vectors specify survive — including the credits-before-any-debit shapes a
// live CreditCafeAccount would refuse, which are precisely the surplus
// carry-forward cases the two implementations have to agree about.
func TestArrears_FIFOMatchesTheStatement(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsfifo")

	for vi, vec := range arrearsFIFOVectors() {
		leaseKey := seedLease(t, ctx, conn, vec.leaseID)
		acctKey := seedLegacyAccount(t, ctx, conn, vec.acctID, leaseKey)
		for ei, ent := range vec.entries {
			seedEntryAt(t, ctx, conn, acctKey, fifoTxID(vi, ei), ent.kind, ent.cents, ent.postedAt, "")
		}

		evaluateArrears(t, ctx, conn, cp, cons, "cafearrfifoeval"+strconv.Itoa(100+vi),
			bootstrap.WeaverIdentityKey, acctKey, "2026-08-29T00:00:00Z", processor.OutcomeAccepted)

		got, _ := arrearsData(t, ctx, conn, acctKey)["dueAt"].(string)
		if got != vec.wantDue {
			t.Errorf("%s: dueAt = %q, want %q — the op's FIFO must age a history exactly as the resident's statement does", vec.name, got, vec.wantDue)
		}
	}
}

// TestArrears_UndeclaredSubmitterStillHydratesArrears (j) is the guarantee the
// account-side and transaction-side derive_reads both exist for. This envelope
// declares the account root and NOTHING else — the shape a client that never
// read the descriptor sends. .arrears must still be hydrated, because a bare
// update is auto-conditioned only on a key the operation DECLARED (Contract #3
// §3.2), and an undeclared read would be LIVE and its write unconditioned.
//
// The read-drift guard armed on every CapabilityPipeline is the mechanism-level
// assertion: a live, undeclared read of vtx.cafeaccount.<id>.arrears reds this
// test deterministically. The state assertions below are the outcome-level
// residual.
func TestArrears_UndeclaredSubmitterStillHydratesArrears(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "arrearsundeclared")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEARRUNDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafearrundacct000001", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafearrunddebit00001"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-08-01T09:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1425,"memo":"Settled tab"}`),
		// The account alone. No optionalReads, no .balance, no .arrears.
		ContextHint: &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	data := arrearsData(t, ctx, conn, acctKey)
	if data == nil {
		t.Fatal("the episode must open even when the submitter declared nothing about .arrears")
	}
	wantDue := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC).
		AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
	if got, _ := data["dueAt"].(string); got != wantDue {
		t.Fatalf("dueAt = %q, want %q", got, wantDue)
	}

	// And the same for the Weaver-dispatched evaluation, whose own derive_reads
	// declares the key for a dispatcher that omitted it.
	evalEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafearrundeval000001"),
		Lane:          processor.LaneDefault,
		OperationType: "EvaluateCafeArrears",
		Actor:         bootstrap.WeaverIdentityKey,
		SubmittedAt:   "2026-08-22T09:00:00Z",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{acctKey},
			// Both walks stay declared — only a read can be derived server-side.
			Enumerations: []processor.EnumerationHint{
				{Hub: acctKey, Relation: "postedTo", Direction: "in"},
				{Hub: acctKey, Relation: "heldFor", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, evalEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got, _ := arrearsData(t, ctx, conn, acctKey)["remindedFor"].(string); got != wantDue {
		t.Fatalf("remindedFor = %q, want %q — the evaluation must see and rewrite the hydrated aspect", got, wantDue)
	}
}

// seedEntryAt seeds one posted transaction at an EXPLICIT postedAt —
// seedLegacyEntry's counterpart for the aging vectors, which turn on when each
// entry posted rather than only on its sign. A non-empty reversesTxID also
// seeds the reverses link a refund naming that charge writes.
func seedEntryAt(t *testing.T, ctx context.Context, conn *substrate.Conn,
	acctKey, txID, entryType string, amountCents int, postedAt string, reversesTxID string) {
	t.Helper()
	txKey := "vtx.cafetransaction." + txID
	acctID := acctKey[len("vtx.cafeaccount."):]
	seedVertex(t, ctx, conn, txKey, "cafetransaction", map[string]any{})
	seedAspect(t, ctx, conn, txKey, "entry", "transactionEntry", map[string]any{
		"type": entryType, "amountCents": amountCents, "postedAt": postedAt,
	})
	seedLink(t, ctx, conn,
		"lnk.cafetransaction."+txID+".postedTo.cafeaccount."+acctID,
		txKey, acctKey, "postedTo", "postedTo")
	if reversesTxID != "" {
		seedLink(t, ctx, conn,
			"lnk.cafetransaction."+txID+".reverses.cafetransaction."+reversesTxID,
			txKey, "vtx.cafetransaction."+reversesTxID, "reverses", "reverses")
	}
}

// dueFor is a charge's postedAt plus the package's net term — the same
// arithmetic the resident's own statement runs, from the package's constant
// rather than a second literal.
func dueFor(t *testing.T, postedAt string) string {
	t.Helper()
	at, err := time.Parse(time.RFC3339, postedAt)
	if err != nil {
		t.Fatalf("parse %q: %v", postedAt, err)
	}
	return at.AddDate(0, 0, cafeledger.ArrearsGraceDays).Format(time.RFC3339)
}

// --- .entry.reason + PayoutCafeCredit ---------------------------------------

// entryReason reads the reason a posted entry carries — "" when it carries
// none, which is the classification a plain charge records.
func entryReason(t *testing.T, ctx context.Context, conn *substrate.Conn, txKey string) string {
	t.Helper()
	doc := readDoc(t, ctx, conn, txKey+".entry")
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.entry carries no data", txKey)
	}
	reason, _ := data["reason"].(string)
	return reason
}

// creditEnvWithPayload is creditEnvFor with the payload spelled out, for the
// vectors that carry a reason.
func creditEnvWithPayload(label, actorKey, acctKey, payload string) (*processor.OperationEnvelope, string) {
	env, txKey := creditEnvFor(label, actorKey, acctKey, 0)
	env.Payload = json.RawMessage(payload)
	return env, txKey
}

// payoutEnv builds one PayoutCafeCredit submission and the transaction key it
// will mint, declaring exactly the reads and enumerations the descriptor
// promises (opmetas.go): the account, its absence-tolerant .balance and
// .arrears, the operator-probe holdsRole walk and the legacy-backfill postedTo
// walk. authContextTarget is the raw client-supplied hint the script refuses
// outright.
func payoutEnv(label, actorKey, acctKey, payload string, authContextTarget string) (*processor.OperationEnvelope, string) {
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "PayoutCafeCredit",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-21T09:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(payload),
		ContextHint: &processor.ContextHint{
			Reads:         []string{acctKey},
			OptionalReads: []string{acctKey + ".balance", acctKey + ".arrears"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: acctKey, Relation: "postedTo", Direction: "in"},
			},
		},
	}
	if authContextTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: authContextTarget}
	}
	return env, "vtx.cafetransaction." + nanoIDFromRequestID(reqID)
}

func payoutPayload(acctKey string, amountCents int) string {
	return `{"accountKey":"` + acctKey + `","amountCents":` + strconv.Itoa(amountCents) + `,"memo":"Paid from till"}`
}

// payoutAs submits one PayoutCafeCredit of amountCents as the given actor,
// drives it, asserts the outcome and returns the payout's transaction key.
func payoutAs(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, label, actorKey, acctKey string, amountCents int, want processor.MessageOutcome) string {
	t.Helper()
	env, txKey := payoutEnv(label, actorKey, acctKey, payoutPayload(acctKey, amountCents), "")
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, want)
	return txKey
}

// seedCreditAccount opens an account for leaseKey and drives it into CREDIT
// by the only route the ledger offers: a charge, paid in full, then refunded —
// the shape a resident who paid for a wrong item and got the charge given back
// is left in. Returns the account key, its .balance at -creditCents.
func seedCreditAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath,
	cons jetstream.Consumer, prefix, leaseKey string, creditCents int) string {
	t.Helper()
	acctKey := createAccount(t, ctx, conn, cp, cons, prefix+"acct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, prefix+"debit", acctKey, creditCents, "Settled tab")
	creditAmount(t, ctx, conn, cp, cons, prefix+"pay", ledgerActorKey, acctKey, creditCents, processor.OutcomeAccepted)
	refundAs(t, ctx, conn, cp, cons, prefix+"refund", ledgerActorKey, acctKey, chargeKey, creditCents, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -float64(creditCents) {
		t.Fatalf("fixture balance = %v, want %d (in credit)", got, -creditCents)
	}
	return acctKey
}

// TestEntryReason_PaymentDefaultsAndWaiverRecorded pins the two credit
// reasons a caller may choose. A payment that names no reason records
// "payment" — the default is written, not left absent, so a statement never
// has to infer it — and a staff write-off records "waiver". Both are ordinary
// credits: the balance moves identically, and the reason is the only thing
// that tells a statement one was cash and the other forgiven.
func TestEntryReason_PaymentDefaultsAndWaiverRecorded(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "reasonwaiver")

	leaseKey := seedLease(t, ctx, conn, "BBCAFERSNWVRLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafereasonwaiveracct", leaseKey)
	postDebit(t, ctx, conn, cp, cons, "cafereasonwaiverdebit", acctKey, 1800, "Settled tab")

	payKey := creditAmount(t, ctx, conn, cp, cons, "cafereasonwaiverpay",
		ledgerActorKey, acctKey, 300, processor.OutcomeAccepted)
	if got := entryReason(t, ctx, conn, payKey); got != "payment" {
		t.Fatalf("a payment naming no reason records reason = %q, want payment", got)
	}

	env, waiverKey := creditEnvWithPayload("cafereasonwaiverwoff", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":1500,"reason":"waiver","memo":"Lease never approved"}`)
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := entryReason(t, ctx, conn, waiverKey); got != "waiver" {
		t.Fatalf("a write-off records reason = %q, want waiver", got)
	}
	entryDoc := readDoc(t, ctx, conn, waiverKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "credit" {
		t.Fatalf("a write-off posts entry.type = %q, want credit — every balance consumer sums it unchanged", got)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying 300 and writing off 1500 of 1800 = %v, want 0", got)
	}

	// A reason outside the two is refused, not defaulted.
	bad, _ := creditEnvWithPayload("cafereasonwaiverbad", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":100,"reason":"gift"}`)
	assertRejectedBecause(t, ctx, conn, cp, cons, bad,
		`InvalidArgument: reason: must be "payment" or "waiver", got gift`)
}

// TestEntryReason_SelfScopedWaiverRefused: forgiving a debt is the café's
// call, never the debtor's. The resident OWNS this account — the same submit
// with reason "payment" is accepted — so the refusal is the reason and nothing
// else, and it lands before the ownership walk.
func TestEntryReason_SelfScopedWaiverRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledgerSelfConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "reasonselfwaiver")

	seedIdentity(t, ctx, conn, ledgerSelfConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFERSNSLFLEASEHJK", ledgerSelfConsumerID)
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafereasonselfacct", leaseKey)
	postDebit(t, ctx, conn, cp, cons, "cafereasonselfdebit", acctKey, 1850, "Settled tab")

	selfEnv := func(label, reason string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
			Lane:          processor.LaneDefault,
			OperationType: "CreditCafeAccount",
			Actor:         ledgerSelfConsumerKey,
			SubmittedAt:   "2026-07-08T09:00:00Z",
			Class:         "cafetransaction",
			Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":100,"reason":"` + reason + `"}`),
			ContextHint:   selfCreditHint(acctKey),
			AuthContext:   &processor.AuthContext{Target: ledgerSelfConsumerKey},
		}
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, selfEnv("cafereasonselfwaiver", "waiver"),
		"AuthDenied: a resident may only pay down their own account, not write it off")
	if got := balanceCents(t, ctx, conn, acctKey); got != 1850 {
		t.Fatalf("balance after the refused write-off = %v, want the untouched 1850", got)
	}
	testutil.PublishOp(t, conn, selfEnv("cafereasonselfpaymnt", "payment"))
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
}

// TestEntryReason_WaiverOverBalanceRefused: a write-off is a credit that is not
// a refund, so the outstanding-balance cap binds it exactly as it binds a
// payment — forgiving more than is owed would put the resident in credit the
// café then owes back. Only the refusal's wording changes, because it is
// toasted verbatim at the staffer who clicked Write off.
func TestEntryReason_WaiverOverBalanceRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "reasonwaivercap")

	leaseKey := seedLease(t, ctx, conn, "BBCAFERSNCAPLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafereasoncapacct", leaseKey)
	postDebit(t, ctx, conn, cp, cons, "cafereasoncapdebit", acctKey, 1425, "Settled tab")

	over, _ := creditEnvWithPayload("cafereasoncapover", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":5000,"reason":"waiver"}`)
	assertRejectedBecause(t, ctx, conn, cp, cons, over,
		"WriteOffExceedsBalance: a write-off of $50.00 exceeds the outstanding balance of $14.25")
	if got := balanceCents(t, ctx, conn, acctKey); got != 1425 {
		t.Fatalf("balance after the refused write-off = %v, want the untouched 1425", got)
	}

	// Exactly what is owed is fine — the cap is `>`, not `>=`.
	exact, _ := creditEnvWithPayload("cafereasoncapexact", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":1425,"reason":"waiver"}`)
	testutil.PublishOp(t, conn, exact)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after writing off the whole tab = %v, want 0", got)
	}
}

// TestEntryReason_RefundWritesItsOwn: a refund's reason is the op's to write,
// not the caller's — "refund" lands on the entry alongside the reverses link,
// so a statement can badge the line without walking the hop.
func TestEntryReason_RefundWritesItsOwn(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "reasonrefund")

	leaseKey := seedLease(t, ctx, conn, "BBCAFERSNRFDLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafereasonrefundacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafereasonrefunddebit", acctKey, 900, "Settled tab")
	if got := entryReason(t, ctx, conn, chargeKey); got != "" {
		t.Fatalf("a charge records reason = %q, want none — absence on a debit IS the classification", got)
	}

	refundKey := refundAs(t, ctx, conn, cp, cons, "cafereasonrefundrfd",
		ledgerActorKey, acctKey, chargeKey, 900, "", processor.OutcomeAccepted)
	if got := entryReason(t, ctx, conn, refundKey); got != "refund" {
		t.Fatalf("a refund records reason = %q, want refund", got)
	}
}

// TestEntryReason_PayloadReasonRefusedOffCreditCafeAccount is the
// cross-refusal: a reason sent to any op but CreditCafeAccount is refused
// rather than ignored, the reversesRef / tabRef idiom. A caller that sends
// reason "waiver" to a refund means a write-off, and silently posting the
// refund would record a different fact than the one asked for. The positive
// vector for each op runs first so the refusal is the field alone.
func TestEntryReason_PayloadReasonRefusedOffCreditCafeAccount(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "reasoncrossrefuse")

	leaseKey := seedLease(t, ctx, conn, "BBCAFERSNXRFLEASEHJK")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "cafereasonxrf", leaseKey, 900)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafereasonxrfchargeb", acctKey, 300, "Settled tab")

	// DebitAccount.
	debit := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cafereasonxrfdebitrsn"),
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-10T08:00:00Z",
		Class:         "cafetransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":100,"reason":"payment"}`),
		ContextHint:   debitHint(acctKey),
	}
	assertRejectedBecause(t, ctx, conn, cp, cons, debit,
		"InvalidArgument: reason: only valid on CreditCafeAccount, not DebitAccount")

	// RefundCafeCharge — the charge is refundable (100 of 300 given back first).
	refundAs(t, ctx, conn, cp, cons, "cafereasonxrfrefundok",
		ledgerActorKey, acctKey, chargeKey, 100, "", processor.OutcomeAccepted)
	refund, _ := refundEnv("cafereasonxrfrefundrsn", ledgerActorKey, acctKey, chargeKey, 100, "")
	refund.Payload = json.RawMessage(`{"accountKey":"` + acctKey + `","reversesRef":"` + chargeKey +
		`","amountCents":100,"reason":"waiver"}`)
	assertRejectedBecause(t, ctx, conn, cp, cons, refund,
		"InvalidArgument: reason: only valid on CreditCafeAccount, not RefundCafeCharge")

	// PayoutCafeCredit — the account holds credit (900 − 300 + 100 = 700).
	payoutAs(t, ctx, conn, cp, cons, "cafereasonxrfpayoutok", ledgerActorKey, acctKey, 100, processor.OutcomeAccepted)
	payout, _ := payoutEnv("cafereasonxrfpayoutrsn", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":100,"reason":"payout"}`, "")
	assertRejectedBecause(t, ctx, conn, cp, cons, payout,
		"InvalidArgument: reason: only valid on CreditCafeAccount, not PayoutCafeCredit")
}

// TestPayoutCafeCredit_SettlesCreditInCash is the positive vector every
// refusal below is measured against. An account left in credit by a refund of
// a paid charge is paid out in full: the commit carries an ordinary DEBIT
// (transaction + .entry{debit, reason: payout} + postedTo), .balance returns
// to zero, account.paidOut is emitted — and the account's live .arrears
// episode state is left byte-identical, because the balance a payout leaves
// is at most zero and the debit branch opens an episode only above it.
func TestPayoutCafeCredit_SettlesCreditInCash(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutok")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYQTLEASEHJKM")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "cafepayoutok", leaseKey, 3575)

	// The fixture's charge opened an episode and its payment ended it, so the
	// account carries a live {evaluatedAt} — the shape a payout must not touch.
	arrearsBefore, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, acctKey+".arrears")
	if err != nil {
		t.Fatalf("read .arrears before the payout: %v", err)
	}
	if data := arrearsData(t, ctx, conn, acctKey); data == nil || data["evaluatedAt"] == nil {
		t.Fatalf("fixture must leave a live .arrears {evaluatedAt} for the payout to leave alone, got %v", data)
	}

	env, payoutKey := payoutEnv("cafepayoutokpayout", ledgerActorKey, acctKey, payoutPayload(acctKey, 3575), "")
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	entryDoc := readDoc(t, ctx, conn, payoutKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "debit" {
		t.Fatalf("payout entry.type = %q, want debit — cash leaving is what the resident's credit bought", got)
	}
	if got, _ := entryData["amountCents"].(float64); got != 3575 {
		t.Fatalf("payout entry.amountCents = %v, want 3575", got)
	}
	if got, _ := entryData["reason"].(string); got != "payout" {
		t.Fatalf("payout entry.reason = %q, want payout", got)
	}
	if got, _ := entryData["memo"].(string); got != "Paid from till" {
		t.Fatalf("payout entry.memo = %q, want %q", got, "Paid from till")
	}
	acctID := acctKey[len("vtx.cafeaccount."):]
	payoutID := payoutKey[len("vtx.cafetransaction."):]
	if !keyExists(t, ctx, conn, "lnk.cafetransaction."+payoutID+".postedTo.cafeaccount."+acctID) {
		t.Fatalf("a payout must post to the account like any other entry")
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying the credit out in full = %v, want 0", got)
	}

	arrearsAfter, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, acctKey+".arrears")
	if err != nil {
		t.Fatalf("read .arrears after the payout: %v", err)
	}
	if string(arrearsAfter.Value) != string(arrearsBefore.Value) || arrearsAfter.Revision != arrearsBefore.Revision {
		t.Fatalf("a payout rewrote .arrears (rev %d → %d):\n before %s\n after  %s",
			arrearsBefore.Revision, arrearsAfter.Revision, arrearsBefore.Value, arrearsAfter.Value)
	}

	// The event class is the payout's own, not a charge's.
	outbox, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, processor.OutboxAspectKey(env.RequestID))
	if err != nil {
		t.Fatalf("read outbox aspect: %v", err)
	}
	ob, err := processor.ParseOutboxAspect(outbox.Value)
	if err != nil {
		t.Fatalf("parse outbox aspect: %v", err)
	}
	var classes []string
	for _, e := range ob.Data.Events {
		classes = append(classes, e.EventType)
	}
	if len(classes) != 1 || classes[0] != "account.paidOut" {
		t.Fatalf("payout emitted %v, want exactly [account.paidOut]", classes)
	}
}

// TestPayoutCafeCredit_NoCreditToPayOut: an account that owes, or is square,
// holds nothing to hand back — paying it out would be lending it money. Both
// shapes are pinned, at a positive balance and at exactly zero, because the
// guard is `>= 0` and a test at one of them passes against `> 0` too.
func TestPayoutCafeCredit_NoCreditToPayOut(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutnocredit")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYNQCRLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafepayoutnocracct", leaseKey)

	// Square: a fresh account at zero.
	zero, _ := payoutEnv("cafepayoutnocrzero", ledgerActorKey, acctKey, payoutPayload(acctKey, 100), "")
	assertRejectedBecause(t, ctx, conn, cp, cons, zero,
		"NoCreditToPayOut: this account holds no credit to pay out")

	// Owing: a charge outstanding.
	postDebit(t, ctx, conn, cp, cons, "cafepayoutnocrdebit", acctKey, 900, "Settled tab")
	owing, _ := payoutEnv("cafepayoutnocrowing", ledgerActorKey, acctKey, payoutPayload(acctKey, 100), "")
	assertRejectedBecause(t, ctx, conn, cp, cons, owing,
		"NoCreditToPayOut: this account holds no credit to pay out")
	if got := balanceCents(t, ctx, conn, acctKey); got != 900 {
		t.Fatalf("balance after two refused payouts = %v, want the untouched 900", got)
	}
}

// TestPayoutCafeCredit_PayoutExceedsCredit: a payout is capped at the credit
// the account holds, so Σdebit−Σcredit returns to at most zero and never past
// it — paying out more would post a debit for coffee nobody drank. The exact
// amount runs after the refusal: the cap is `>`, not `>=`, and the refusal
// spells both amounts as money because it is toasted verbatim at the desk.
func TestPayoutCafeCredit_PayoutExceedsCredit(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutexceeds")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYXCDLEASEHJK")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "cafepayoutxcd", leaseKey, 1425)

	over, _ := payoutEnv("cafepayoutxcdover", ledgerActorKey, acctKey, payoutPayload(acctKey, 5000), "")
	assertRejectedBecause(t, ctx, conn, cp, cons, over,
		"PayoutExceedsCredit: a payout of $50.00 exceeds the credit balance of $14.25")
	if got := balanceCents(t, ctx, conn, acctKey); got != -1425 {
		t.Fatalf("balance after the refused payout = %v, want the untouched -1425", got)
	}

	// A partial payout is fine, and the remainder exactly.
	payoutAs(t, ctx, conn, cp, cons, "cafepayoutxcdpart", ledgerActorKey, acctKey, 425, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -1000 {
		t.Fatalf("balance after a partial payout of 425 = %v, want -1000", got)
	}
	payoutAs(t, ctx, conn, cp, cons, "cafepayoutxcdrest", ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying out the rest = %v, want 0", got)
	}
}

// TestPayoutCafeCredit_SelfScopedSubmitRefused: a payout is a till act and is
// never self-scoped. The operator's own payout of the same credit proves the
// vector is otherwise well-formed, and the refusal is read from the reply — a
// step-3 denial would mean the script was never asked.
func TestPayoutCafeCredit_SelfScopedSubmitRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutselfscoped")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYSLFLEASEHJK")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "cafepayoutslf", leaseKey, 900)

	payoutAs(t, ctx, conn, cp, cons, "cafepayoutslfoperator", ledgerActorKey, acctKey, 100, processor.OutcomeAccepted)
	targeted, _ := payoutEnv("cafepayoutslftargeted", ledgerActorKey, acctKey, payoutPayload(acctKey, 100), ledgerActorKey)
	assertRejectedBecause(t, ctx, conn, cp, cons, targeted,
		"AuthDenied: PayoutCafeCredit is a front-desk act, never self-scoped")
	if got := balanceCents(t, ctx, conn, acctKey); got != -800 {
		t.Fatalf("balance after the refused payout = %v, want the untouched -800", got)
	}
}

// TestPayoutCafeCredit_TabRefAndReversesRefRefused: the two fields other entry
// ops own are refused on a payout rather than dropped — a caller sending
// tabRef means "charge this tab" and reversesRef means "refund that charge",
// and neither is cash leaving the till.
func TestPayoutCafeCredit_TabRefAndReversesRefRefused(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutfields")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYFLDLEASEHJK")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "cafepayoutfld", leaseKey, 900)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafepayoutfldcharge", acctKey, 100, "Settled tab")
	tabKey := "vtx.tab.BBCAFEPAYTABHJKMNPQR"
	seedVertex(t, ctx, conn, tabKey, "tab", nil)

	withTab, _ := payoutEnv("cafepayoutfldtab", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":100,"tabRef":"`+tabKey+`"}`, "")
	withTab.ContextHint.Reads = append(withTab.ContextHint.Reads, tabKey)
	assertRejectedBecause(t, ctx, conn, cp, cons, withTab,
		"InvalidArgument: tabRef: only valid on DebitAccount or CreditCafeAccount, not PayoutCafeCredit")

	withReverses, _ := payoutEnv("cafepayoutfldrev", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":100,"reversesRef":"`+chargeKey+`"}`, "")
	withReverses.ContextHint.Reads = append(withReverses.ContextHint.Reads, chargeKey, chargeKey+".entry")
	assertRejectedBecause(t, ctx, conn, cp, cons, withReverses,
		"InvalidArgument: reversesRef: only valid on RefundCafeCharge, not PayoutCafeCredit")
}

// TestPayoutCafeCredit_LegacyAccountBackfillsOnPayout mirrors the payment's
// self-heal for the payout leg: an account minted under cafe-ledger < 0.4.0
// carries no .balance, and a payout's cap needs the number just as a payment's
// does, so it replays the postedTo history once and mints the total. The
// seeded history nets to a credit (charge 3000, paid 3000, refunded 3000 as a
// plain credit, then a further refund of 500), so a replay that summed only
// one sign would land on the wrong side of zero and refuse.
func TestPayoutCafeCredit_LegacyAccountBackfillsOnPayout(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutlegacy")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYLEGLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEPAYLEGACCTHJKM", leaseKey)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEPAYLEGTXAHJKMN", "debit", 3000)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEPAYLEGTXBHJKMN", "credit", 3000)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFEPAYLEGTXCHJKMN", "credit", 500)

	// The cap already measures against the replayed number before any
	// .balance exists: 500 of credit, so 501 is not payable — and a refused
	// payout commits nothing, so the account is still legacy afterwards.
	over, _ := payoutEnv("cafepayoutlegover", ledgerActorKey, acctKey, payoutPayload(acctKey, 501), "")
	assertRejectedBecause(t, ctx, conn, cp, cons, over,
		"PayoutExceedsCredit: a payout of $5.01 exceeds the credit balance of $5.00")
	if keyExists(t, ctx, conn, acctKey+".balance") {
		t.Fatalf("a REFUSED payout seeded .balance — nothing about a rejected op may commit")
	}

	// The first accepted payout mints the aspect from the replayed total.
	payoutAs(t, ctx, conn, cp, cons, "cafepayoutlegpart", ledgerActorKey, acctKey, 200, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -300 {
		t.Fatalf("backfilled balance = %v, want -300 (3000 charged − 3500 credited + this 200 payout)", got)
	}

	// And every touch after that is the O(1) path off the aspect itself.
	payoutAs(t, ctx, conn, cp, cons, "cafepayoutlegrest", ledgerActorKey, acctKey, 300, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after paying out the backfilled credit = %v, want 0", got)
	}
}

// TestPayoutCafeCredit_UndeclaredSubmitterStillConditioned is the payout's
// half of the OCC guarantee: the .balance a payout's cap reads is hydrated —
// and its update revision-conditioned — because the script's own derive_reads
// declares it, never because the submitter did. Two payouts of the whole
// credit race with the account alone declared; under a hydrated, conditioned
// read the loser re-hydrates, re-executes against the winner's zero and is
// refused, so the credit is paid out exactly once. An unconditioned pair would
// each pass the cap against the same −900 and both post, paying out 1800
// against 900 of credit.
func TestPayoutCafeCredit_UndeclaredSubmitterStillConditioned(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutundeclared")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYUNDLEASEHJK")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "cafepayoutund", leaseKey, 900)

	undeclaredPayout := func(label string) *processor.OperationEnvelope {
		env, _ := payoutEnv(label, ledgerActorKey, acctKey, payoutPayload(acctKey, 900), "")
		// The account alone, plus the two walks (only a read can be derived
		// server-side). No optionalReads, no .balance, no .arrears — the shape
		// a client that never read the descriptor sends.
		env.ContextHint.OptionalReads = nil
		return env
	}
	testutil.PublishOp(t, conn, undeclaredPayout("cafepayoutundfirst"))
	testutil.PublishOp(t, conn, undeclaredPayout("cafepayoutundsecond"))

	outcomes := driveConcurrently(t, ctx, cp, cons, 2)
	accepted := 0
	for _, o := range outcomes {
		if o == processor.OutcomeAccepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("outcomes = %v, want exactly one accepted — a submitter that declares nothing must not be able to turn the OCC condition off", outcomes)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after two racing undeclared payouts = %v, want 0 (the credit paid out once, never twice)", got)
	}
}

// --- the cash invariant: credit never exceeds cash paid in -------------------

// balanceFields reads the account's .balance data back whole, so a test can
// assert cashCents is PRESENT with a value, or ABSENT — two different facts.
func balanceFields(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) map[string]any {
	t.Helper()
	doc := readDoc(t, ctx, conn, acctKey+".balance")
	data, _ := doc["data"].(map[string]any)
	if data == nil {
		t.Fatalf("%s.balance carries no data", acctKey)
	}
	return data
}

func cashCents(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string) float64 {
	t.Helper()
	data := balanceFields(t, ctx, conn, acctKey)
	got, ok := data["cashCents"].(float64)
	if !ok {
		t.Fatalf("%s.balance carries no cashCents, got %v", acctKey, data)
	}
	return got
}

// TestCreateAccount_MintsBothBalanceFields pins the aspect's shape at birth:
// {balanceCents: 0, cashCents: 0}. A document minted without cashCents would
// read as pre-field on every later leg and pay a replay it never needed.
func TestCreateAccount_MintsBothBalanceFields(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "createcash")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECASHMNTLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecashmintacct", leaseKey)
	data := balanceFields(t, ctx, conn, acctKey)
	if got, _ := data["balanceCents"].(float64); got != 0 {
		t.Fatalf("balanceCents at birth = %v, want 0", got)
	}
	if got, ok := data["cashCents"].(float64); !ok || got != 0 {
		t.Fatalf("cashCents at birth = %v (present=%v), want 0", got, ok)
	}
}

// TestRefundCafeCharge_PayoutCannotBeRefunded closes the cash loop at its
// second turn. A payout is a debit, and one carrying no refundedCents tally,
// so a refund that could name it would hand the paid-out credit straight back:
// charge, pay, refund, pay out, refund the payout, pay out again — without end,
// every entry legitimate-looking. The refund of the CHARGE that ran first
// proves the vector is well-formed; the refund of the payout is refused on the
// reason it carries, and the balance is untouched.
func TestRefundCafeCharge_PayoutCannotBeRefunded(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundpayout")

	leaseKey := seedLease(t, ctx, conn, "BBCAFERFPQTLEASEHJKM")
	acctKey := seedCreditAccount(t, ctx, conn, cp, cons, "caferfpqt", leaseKey, 1000)
	payoutKey := payoutAs(t, ctx, conn, cp, cons, "caferfpqtpayout", ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the payout = %v, want 0", got)
	}

	env, _ := refundEnv("caferfpqtrefundpayout", ledgerActorKey, acctKey, payoutKey, 1000, "")
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"InvalidArgument: reversesRef: only a posted charge (a debit with no reason) can be refunded; "+payoutKey+" is a payout")
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the refused refund-of-a-payout = %v, want the untouched 0", got)
	}
	if got := entryReason(t, ctx, conn, payoutKey); got != "payout" {
		t.Fatalf("the payout's own entry was disturbed: reason = %q", got)
	}
}

// TestRefundCafeCharge_WrittenOffChargeCannotBeRefunded is the sequence the
// invariant exists for, end to end: a charge the café wrote off is refunded —
// still offered, since a waiver leaves the charge's refundedCents untouched —
// and the account would land $18 in credit having paid nothing, credit the
// desk then hands over in cash. RefundExceedsPaid stops it at the mint, and
// the balance stays at zero.
func TestRefundCafeCharge_WrittenOffChargeCannotBeRefunded(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundwaived")

	leaseKey := seedLease(t, ctx, conn, "BBCAFERFWVDLEASEHJKM")
	acctKey := createAccount(t, ctx, conn, cp, cons, "caferfwvdacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "caferfwvddebit", acctKey, 1800, "Settled tab")
	waive, _ := creditEnvWithPayload("caferfwvdwaive", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":1800,"reason":"waiver","memo":"Lease never approved"}`)
	testutil.PublishOp(t, conn, waive)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the write-off = %v, want 0", got)
	}
	if got := cashCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("cash after the write-off = %v, want 0 — forgiven debt is not cash paid in", got)
	}

	env, _ := refundEnv("caferfwvdrefund", ledgerActorKey, acctKey, chargeKey, 1800, "")
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"RefundExceedsPaid: a refund of $18.00 would leave this account $18.00 in credit, more than the $0.00 it has paid in")
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the refused refund = %v, want the untouched 0", got)
	}

	// And with nothing in credit there is nothing to pay out either.
	none, _ := payoutEnv("caferfwvdpayout", ledgerActorKey, acctKey, payoutPayload(acctKey, 1800), "")
	assertRejectedBecause(t, ctx, conn, cp, cons, none,
		"NoCreditToPayOut: this account holds no credit to pay out")
}

// TestCashInvariant_PaidChargeRefundedAndPaidOut is the legitimate loop the
// invariant must leave open: the resident paid $10 in cash, the charge is
// given back, the desk hands the $10 back. cash tracks it — 10 in, 10 out —
// and both numbers return to zero.
func TestCashInvariant_PaidChargeRefundedAndPaidOut(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "cashloopok")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECASHQKLEASEHJK")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecashqkacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafecashqkdebit", acctKey, 1000, "Settled tab")
	if got := cashCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("cash after a charge = %v, want 0 — a charge is not cash", got)
	}
	creditAmount(t, ctx, conn, cp, cons, "cafecashqkpay", ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)
	if got := cashCents(t, ctx, conn, acctKey); got != 1000 {
		t.Fatalf("cash after paying $10 = %v, want 1000", got)
	}
	refundAs(t, ctx, conn, cp, cons, "cafecashqkrefund", ledgerActorKey, acctKey, chargeKey, 1000, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -1000 {
		t.Fatalf("balance after refunding the paid charge = %v, want -1000", got)
	}
	if got := cashCents(t, ctx, conn, acctKey); got != 1000 {
		t.Fatalf("cash after the refund = %v, want the untouched 1000 — a refund mints credit, it moves no cash", got)
	}
	payoutAs(t, ctx, conn, cp, cons, "cafecashqkpayout", ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the payout = %v, want 0", got)
	}
	if got := cashCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("cash after the payout = %v, want 0 — the $10 went back out", got)
	}
}

// TestCashInvariant_PartiallyPaidChargeRefundBoundedByCash: $18 charged, $10
// paid, the remaining $8 written off. Refunding the whole charge would leave
// $18 of credit against $10 of cash and is refused; refunding what was actually
// paid lands exactly on the cash floor and passes. The cap is `>`, not `>=`.
func TestCashInvariant_PartiallyPaidChargeRefundBoundedByCash(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "cashpartial")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECASHPRTLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecashprtacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafecashprtdebit", acctKey, 1800, "Settled tab")
	creditAmount(t, ctx, conn, cp, cons, "cafecashprtpay", ledgerActorKey, acctKey, 1000, processor.OutcomeAccepted)
	waive, _ := creditEnvWithPayload("cafecashprtwaive", ledgerActorKey, acctKey,
		`{"accountKey":"`+acctKey+`","amountCents":800,"reason":"waiver"}`)
	testutil.PublishOp(t, conn, waive)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	if got := cashCents(t, ctx, conn, acctKey); got != 1000 {
		t.Fatalf("cash after $10 paid and $8 waived = %v, want 1000", got)
	}

	whole, _ := refundEnv("cafecashprtrefundall", ledgerActorKey, acctKey, chargeKey, 1800, "")
	assertRejectedBecause(t, ctx, conn, cp, cons, whole,
		"RefundExceedsPaid: a refund of $18.00 would leave this account $18.00 in credit, more than the $10.00 it has paid in")
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the refused refund = %v, want the untouched 0", got)
	}

	refundAs(t, ctx, conn, cp, cons, "cafecashprtrefundpd", ledgerActorKey, acctKey, chargeKey, 1000, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -1000 {
		t.Fatalf("balance after refunding what was paid = %v, want -1000", got)
	}
}

// TestCashInvariant_UnpaidChargeRefundPasses: a refund of a charge nobody paid
// nets the balance to zero and moves no cash — it must pass, or a wrongly
// posted charge could never be corrected before the resident paid it.
func TestCashInvariant_UnpaidChargeRefundPasses(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "cashunpaid")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECASHNPDLEASEHJ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "cafecashnpdacct", leaseKey)
	chargeKey := postDebit(t, ctx, conn, cp, cons, "cafecashnpddebit", acctKey, 1800, "Settled tab")
	refundAs(t, ctx, conn, cp, cons, "cafecashnpdrefund", ledgerActorKey, acctKey, chargeKey, 1800, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after refunding an unpaid charge = %v, want 0", got)
	}
	if got := cashCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("cash = %v, want 0", got)
	}
}

// seedPreFieldBalance plants a live .balance document in the shape one written
// before cashCents existed sits in today: balanceCents alone. No op produces
// it any more, so it has to be planted — and its absent field is the case the
// script must read as "not yet computed", never as zero.
func seedPreFieldBalance(t *testing.T, ctx context.Context, conn *substrate.Conn, acctKey string, balance int) {
	t.Helper()
	seedAspect(t, ctx, conn, acctKey, "balance", "cafeAccountBalance", map[string]any{"balanceCents": balance})
}

// seedLegacyRefund seeds an already-posted credit that reverses chargeTxID —
// a refund written before entries carried a reason, distinguishable from a
// payment ONLY by the reverses link it carries.
func seedLegacyRefund(t *testing.T, ctx context.Context, conn *substrate.Conn,
	acctKey, txID, chargeTxID string, amountCents int) {
	t.Helper()
	seedLegacyEntry(t, ctx, conn, acctKey, txID, "credit", amountCents)
	seedLink(t, ctx, conn,
		"lnk.cafetransaction."+txID+".reverses.cafetransaction."+chargeTxID,
		"vtx.cafetransaction."+txID, "vtx.cafetransaction."+chargeTxID, "reverses", "reverses")
}

// TestCashInvariant_PreFieldBalanceDocComputesCashOnGuardedLegs covers the documents
// that already exist: a live .balance carrying balanceCents alone. Its history
// holds one old payment (a credit with no reason and no reverses link) and one
// old refund (a credit with no reason and a reverses link) — the two shapes
// only the link tells apart, and a replay that counted both as cash would
// write 5000 where 3000 is the truth. A charge against the document leaves
// the field absent; the refund that needs the number computes it, is bounded
// by it, and writes it, and every touch afterwards is O(1) off the aspect.
func TestCashInvariant_PreFieldBalanceDocComputesCashOnGuardedLegs(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "cashprefield")

	leaseKey := seedLease(t, ctx, conn, "BBCAFECASHPFLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFECASHPFACCTHJKM", leaseKey)
	const chargeA = "BBCAFECASHPFTXAHJKMN"
	const chargeB = "BBCAFECASHPFTXBHJKMN"
	seedLegacyEntry(t, ctx, conn, acctKey, chargeA, "debit", 3000)
	seedLegacyEntry(t, ctx, conn, acctKey, "BBCAFECASHPFTXPHJKMN", "credit", 3000) // the old payment
	seedLegacyEntry(t, ctx, conn, acctKey, chargeB, "debit", 2000)
	seedLegacyRefund(t, ctx, conn, acctKey, "BBCAFECASHPFTXRHJKMN", chargeB, 2000) // the old refund
	seedPreFieldBalance(t, ctx, conn, acctKey, 0)

	// A charge reads no number: it moves balanceCents and leaves cashCents
	// absent rather than seed it from a view it never computed.
	postDebit(t, ctx, conn, cp, cons, "cafecashpfdebit", acctKey, 500, "Settled tab")
	data := balanceFields(t, ctx, conn, acctKey)
	if got, _ := data["balanceCents"].(float64); got != 500 {
		t.Fatalf("balanceCents after the charge = %v, want 500", got)
	}
	if _, present := data["cashCents"]; present {
		t.Fatalf("a charge against a pre-field document wrote cashCents: %v — only a leg that computed it may", data)
	}

	// The refund needs the number: cash is the old payment alone (3000), so a
	// refund of charge A leaving $25 in credit passes, and the field is written.
	refundAs(t, ctx, conn, cp, cons, "cafecashpfrefund", ledgerActorKey, acctKey,
		"vtx.cafetransaction."+chargeA, 3000, "", processor.OutcomeAccepted)
	if got := balanceCents(t, ctx, conn, acctKey); got != -2500 {
		t.Fatalf("balance after the refund = %v, want -2500", got)
	}
	if got := cashCents(t, ctx, conn, acctKey); got != 3000 {
		t.Fatalf("cashCents computed from the pre-field history = %v, want 3000 (the old payment alone — the old refund carries a reverses link and is not cash)", got)
	}

	// From here the aspect carries both numbers and a payout is O(1) off it,
	// bounded by the cash: $30 in, $25 of credit, so $25 goes back out.
	payoutAs(t, ctx, conn, cp, cons, "cafecashpfpayout", ledgerActorKey, acctKey, 2500, processor.OutcomeAccepted)
	if got := cashCents(t, ctx, conn, acctKey); got != 500 {
		t.Fatalf("cash after paying $25 back out = %v, want 500", got)
	}
	if got := balanceCents(t, ctx, conn, acctKey); got != 0 {
		t.Fatalf("balance after the payout = %v, want 0", got)
	}
}

// TestPayoutCafeCredit_PayoutExceedsCash is the defence-in-depth conjunct. It
// cannot be reached through the ops — the refund invariant never mints credit
// past the cash — so the vector plants the inconsistent document directly:
// $10 of credit against $0 of cash. The payout is refused at the till.
func TestPayoutCafeCredit_PayoutExceedsCash(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "payoutcash")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEPAYCSHLEASEHJK")
	acctKey := seedLegacyAccount(t, ctx, conn, "BBCAFEPAYCSHACCTHJKM", leaseKey)
	seedAspect(t, ctx, conn, acctKey, "balance", "cafeAccountBalance",
		map[string]any{"balanceCents": -1000, "cashCents": 0})

	env, _ := payoutEnv("cafepaycshpayout", ledgerActorKey, acctKey, payoutPayload(acctKey, 500), "")
	assertRejectedBecause(t, ctx, conn, cp, cons, env,
		"PayoutExceedsCash: a payout of $5.00 exceeds the $0.00 this account has paid in")
	if got := balanceCents(t, ctx, conn, acctKey); got != -1000 {
		t.Fatalf("balance after the refused payout = %v, want the untouched -1000", got)
	}
}
