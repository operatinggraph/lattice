// wellness-ledger integration tests through the real install + Processor
// pipeline. External test package (wellnessledger_test) so they exercise the
// public Lattice surface: seed the kernel, install rbac + identity + hygiene +
// wellness-domain + wellness-ledger through the Processor, then submit the
// ops and assert the committed Core-KV shape + the emitted events.
package wellnessledger_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strconv"
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
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
	wellnessdomain "github.com/operatinggraph/lattice/packages/wellness-domain"
	wellnessledger "github.com/operatinggraph/lattice/packages/wellness-ledger"
)

const (
	ledgerActorID  = "WLLEDGERACTRHJKMNPQR"
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
			{OperationType: "CreateStudio", Scope: "any"},
			{OperationType: "CreateSession", Scope: "any"},
			{OperationType: "CreateBooking", Scope: "any"},
			{OperationType: "WellnessCreateAccount", Scope: "any"},
			{OperationType: "WellnessDebitAccount", Scope: "any"},
			{OperationType: "WellnessCreditAccount", Scope: "any"},
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
	// wlConsumerRoleID stands in for identity-domain's real `consumer` role
	// NanoID — these tests don't install identity-domain (the lease-signing
	// lsConsumerRoleID idiom, mirrored by clinic-ledger's ledConsumerRoleID).
	const wlConsumerRoleID = "WLEDConsumerRoZeHJKM"
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": wlConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
	if _, err := inst.Install(ctx, orchestrationbase.Package); err != nil {
		t.Fatalf("install orchestration-base: %v", err)
	}
	if _, err := inst.Install(ctx, servicedomain.Package); err != nil {
		t.Fatalf("install service-domain: %v", err)
	}
	if _, err := inst.Install(ctx, leasesigning.Package); err != nil {
		t.Fatalf("install lease-signing: %v", err)
	}
	if _, err := inst.Install(ctx, wellnessdomain.Package); err != nil {
		t.Fatalf("install wellness-domain: %v", err)
	}
	if _, err := inst.Install(ctx, wellnessledger.Package); err != nil {
		t.Fatalf("install wellness-ledger: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, ledgerCapDoc())
	// The operator grant is only half the claim — the workplace-confinement
	// guard reads the holdsRole LINK to decide whether its caller is root, not
	// the cap doc's Roles (mirrors wellness-domain's own fixture).
	testutil.SeedHoldsRole(t, ctx, conn, ledgerActorKey, bootstrap.RoleOperatorKey)
	return ctx, conn
}

func newLedgerPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "wl-" + durable,
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

// seedIdentity writes a raw vtx.identity vertex directly (these tests don't
// install identity-domain, mirroring wellness-domain's own integration_test.go
// idiom) — the ledger's CreateAccount only needs a live class=identity vertex.
func seedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.identity." + id
	doc := map[string]any{"class": "identity", "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed identity %s: %v", key, err)
	}
	return key
}

// createAccount submits CreateAccount{identityKey} and returns the account key
// — the account's own independently-minted NanoID (never derived from the
// identity's own).
func createAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, identityKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.wellnessaccount." + nanoIDFromRequestID(reqID)
}

// TestCreateAccount_MintsAccountHeldForIdentity (test 1). CreateAccount mints
// vtx.wellnessaccount.<freshId> (root {} — D5, an id independent of the
// identity's own) + the identity's .wellnessLedgerAccount guard aspect + the
// heldFor link; a second call for the same identity that declares the guard
// aspect in reads conflicts on it (AccountAlreadyExists).
func TestCreateAccount_MintsAccountHeldForIdentity(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "create")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT1AB")
	identityID := identityKey[len("vtx.identity."):]
	guardKey := identityKey + ".wellnessLedgerAccount"

	if keyExists(t, ctx, conn, guardKey) {
		t.Fatalf("guard aspect must not exist before CreateAccount")
	}

	acctKey := createAccount(t, ctx, conn, cp, cons, "createacct0000001", identityKey)
	acctID := acctKey[len("vtx.wellnessaccount."):]
	if acctID == identityID {
		t.Fatalf("account id must NOT equal the identity's own id (independently minted), got %q for both", acctID)
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

	heldForLnk := "lnk.wellnessaccount." + acctID + ".heldFor.identity." + identityID
	if !keyExists(t, ctx, conn, heldForLnk) {
		t.Fatalf("heldFor link must exist: %s", heldForLnk)
	}

	// A second CreateAccount for the SAME identity, declaring the now-existing
	// guard aspect in reads, conflicts on it (AccountAlreadyExists — the
	// create-only write is the guard).
	dup := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacct0000002"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:05:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey, guardKey}},
	}
	testutil.PublishOp(t, conn, dup)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateAccount_UnknownIdentity rejects an account opened against a
// non-existent identity (no-orphan invariant).
func TestCreateAccount_UnknownIdentity(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownidentity")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createacctunknown01"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"vtx.identity.WLABS23456789ABTDHJK"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.identity.WLABS23456789ABTDHJK"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitCreditAccount_PostEntries (test 2). WellnessDebitAccount/WellnessCreditAccount each
// mint a fresh transaction vertex (root {} — D5) + a .entry aspect + the
// postedTo link to the account; the account root is never touched (append-only
// ledger, no balance stored).
func TestDebitCreditAccount_PostEntries(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "postentries")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT2AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctpost00001", identityKey)
	acctID := acctKey[len("vtx.wellnessaccount."):]

	debitReqID := testutil.GenReqID("debitfee0000000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"No-show fee"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	debitTxKey := "vtx.wellnesstransaction." + nanoIDFromRequestID(debitReqID)
	entryDoc := readDoc(t, ctx, conn, debitTxKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["type"].(string); got != "debit" {
		t.Fatalf("entry.type = %q, want debit", got)
	}
	if got, _ := entryData["amountCents"].(float64); got != 2500 {
		t.Fatalf("entry.amountCents = %v, want 2500", got)
	}
	if got, _ := entryData["memo"].(string); got != "No-show fee" {
		t.Fatalf("entry.memo = %q, want %q", got, "No-show fee")
	}

	txDoc := readDoc(t, ctx, conn, debitTxKey)
	if d, _ := txDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("transaction root data must stay minimal ({}) after post, got %v", d)
	}

	postedToLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(debitReqID) + ".postedTo.wellnessaccount." + acctID
	if !keyExists(t, ctx, conn, postedToLnk) {
		t.Fatalf("postedTo link must exist: %s", postedToLnk)
	}

	// The account root is never mutated by a debit — append-only ledger.
	acctDoc := readDoc(t, ctx, conn, acctKey)
	if d, _ := acctDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("account root data must stay minimal ({}) after a debit — the ledger is append-only, got %v", d)
	}

	// WellnessCreditAccount — a payment received.
	creditReqID := testutil.GenReqID("creditpay0000000001")
	creditEnv := &processor.OperationEnvelope{
		RequestID:     creditReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-05T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"memo":"Front-desk payment"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	creditTxKey := "vtx.wellnesstransaction." + nanoIDFromRequestID(creditReqID)
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
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"vtx.wellnessaccount.WLABSENTACCTHJKMNPQR","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.wellnessaccount.WLABSENTACCTHJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_NonPositiveAmountRejected rejects amountCents <= 0
// (InvalidArgument).
func TestDebitAccount_NonPositiveAmountRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "badamount")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT3AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctbad000001", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitbadamount00001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T13:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// createStudio submits CreateStudio and returns the studio's full key.
func createStudio(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, name string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateStudio",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "studio",
		Payload:       json.RawMessage(`{"name":"` + name + `"}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.studio." + nanoIDFromRequestID(reqID)
}

// createSession submits CreateSession and returns the session's full key.
func createSession(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, studioKey, startsAt, endsAt string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateSession",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-25T12:00:00Z",
		Class:         "session",
		Payload: json.RawMessage(`{"studio":"` + studioKey + `","name":"Vinyasa Flow","startsAt":"` + startsAt +
			`","endsAt":"` + endsAt + `","capacity":10}`),
		ContextHint: &processor.ContextHint{Reads: []string{studioKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.session." + nanoIDFromRequestID(reqID)
}

// createBooking submits CreateBooking and returns the booking's full key. The
// session's own .schedule is a required read (prepare_booking_common's
// SessionStarted check, wellness-domain opmetas.go); the seat-claim cells up
// to the session's capacity, the booker's own double-book guard, and the
// booker's slot cells over the session's window are all optionalReads
// (claim_first_free_seat / bookerSlotClaim, ddls.go).
func createBooking(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, sessionKey, bookerKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	_, bookerID, _ := substrate.ParseVertexKey(bookerKey)
	optionalReads := []string{sessionKey + ".bkr" + bookerID}
	for n := 1; n <= 10; n++ {
		optionalReads = append(optionalReads, sessionKey+".seat"+strconv.Itoa(n))
	}
	sched := readDoc(t, ctx, conn, sessionKey+".schedule")
	schedData, _ := sched["data"].(map[string]any)
	startsAt, _ := schedData["startsAt"].(string)
	endsAt, _ := schedData["endsAt"].(string)
	if start, err := time.Parse(time.RFC3339, startsAt); err == nil {
		if end, err := time.Parse(time.RFC3339, endsAt); err == nil {
			for cur := start; cur.Before(end); cur = cur.Add(15 * time.Minute) {
				cc := strings.ToLower(strings.NewReplacer("-", "", ":", "").Replace(cur.UTC().Format(time.RFC3339)))
				optionalReads = append(optionalReads, bookerKey+".slot"+cc)
			}
		}
	}
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateBooking",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-25T13:00:00Z",
		Class:         "booking",
		Payload:       json.RawMessage(`{"session":"` + sessionKey + `","booker":"` + bookerKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{sessionKey, sessionKey + ".schedule", bookerKey},
			OptionalReads: optionalReads,
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.booking." + nanoIDFromRequestID(reqID)
}

// TestDebitAccount_BookingRefWritesSettlesLink (test 3). A WellnessDebitAccount
// carrying bookingRef writes the settles audit link (transaction→booking) the
// wellnessNoShowSettlement lens reads; a plain WellnessDebitAccount with no
// bookingRef writes no such link (byte-for-byte the existing plain-charge
// shape).
func TestDebitAccount_BookingRefWritesSettlesLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "bookingref")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT4AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctbkr0001", identityKey)
	studioKey := createStudio(t, ctx, conn, cp, cons, "mkstudiobkr0000001", "Sunrise Yoga Room")
	sessionKey := createSession(t, ctx, conn, cp, cons, "mksessbkr0000001", studioKey, "2026-06-25T15:00:00Z", "2026-06-25T15:30:00Z")
	bookingKey := createBooking(t, ctx, conn, cp, cons, "mkbkgbkr0000001", sessionKey, identityKey)
	bookingID := bookingKey[len("vtx.booking."):]

	debitReqID := testutil.GenReqID("debitbkr0000000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"bookingRef":"` + bookingKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, bookingKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	settlesLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(debitReqID) + ".settles.booking." + bookingID
	if !keyExists(t, ctx, conn, settlesLnk) {
		t.Fatalf("settles link must exist: %s", settlesLnk)
	}

	// A plain WellnessDebitAccount (no bookingRef) writes no settles link at all.
	plainReqID := testutil.GenReqID("debitbkr0000000002")
	plainEnv := &processor.OperationEnvelope{
		RequestID:     plainReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:05:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, plainEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	plainSettlesLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(plainReqID) + ".settles.booking." + bookingID
	if keyExists(t, ctx, conn, plainSettlesLnk) {
		t.Fatalf("a plain WellnessDebitAccount with no bookingRef must write no settles link, found %s", plainSettlesLnk)
	}
}

// TestDebitAccount_UnknownBookingRefRejected rejects a WellnessDebitAccount whose
// bookingRef names a non-existent booking (UnknownBooking).
func TestDebitAccount_UnknownBookingRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownbookingref")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT5AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctubr0001", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitubr0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":2500,"bookingRef":"vtx.booking.WLABSENTBKGHJKMNPQRS"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, "vtx.booking.WLABSENTBKGHJKMNPQRS"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_PriceBookingRefWritesSettlesClassPriceLink (test 5). A
// WellnessDebitAccount carrying priceBookingRef writes the settlesClassPrice audit
// link (transaction→booking, a relation DISTINCT from settles) the
// wellnessClassPriceSettlement lens reads; a plain WellnessDebitAccount with no
// priceBookingRef writes no such link. priceBookingRef and bookingRef are
// independent — supplying priceBookingRef alone writes ONLY settlesClassPrice,
// never settles (the byte-for-byte regression the EXISTING bookingRef shape
// above must never see).
func TestDebitAccount_PriceBookingRefWritesSettlesClassPriceLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "pricebookingref")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT6AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctpbr0001", identityKey)
	studioKey := createStudio(t, ctx, conn, cp, cons, "mkstudiopbr0000001", "Sunrise Yoga Room")
	sessionKey := createSession(t, ctx, conn, cp, cons, "mksesspbr0000001", studioKey, "2026-06-25T15:00:00Z", "2026-06-25T15:30:00Z")
	bookingKey := createBooking(t, ctx, conn, cp, cons, "mkbkgpbr0000001", sessionKey, identityKey)
	bookingID := bookingKey[len("vtx.booking."):]

	debitReqID := testutil.GenReqID("debitpbr0000000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"priceBookingRef":"` + bookingKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, bookingKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	settlesClassPriceLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(debitReqID) + ".settlesClassPrice.booking." + bookingID
	if !keyExists(t, ctx, conn, settlesClassPriceLnk) {
		t.Fatalf("settlesClassPrice link must exist: %s", settlesClassPriceLnk)
	}
	settlesLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(debitReqID) + ".settles.booking." + bookingID
	if keyExists(t, ctx, conn, settlesLnk) {
		t.Fatalf("a WellnessDebitAccount carrying ONLY priceBookingRef must write no settles link, found %s", settlesLnk)
	}

	// A plain WellnessDebitAccount (no priceBookingRef) writes no settlesClassPrice link at all.
	plainReqID := testutil.GenReqID("debitpbr0000000002")
	plainEnv := &processor.OperationEnvelope{
		RequestID:     plainReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:05:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, plainEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	plainSettlesClassPriceLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(plainReqID) + ".settlesClassPrice.booking." + bookingID
	if keyExists(t, ctx, conn, plainSettlesClassPriceLnk) {
		t.Fatalf("a plain WellnessDebitAccount with no priceBookingRef must write no settlesClassPrice link, found %s", plainSettlesClassPriceLnk)
	}
}

// TestDebitAccount_BookingRefAndPriceBookingRefBothWritten proves the two ref
// params are independent: a single WellnessDebitAccount carrying BOTH bookingRef and
// priceBookingRef writes BOTH the settles and settlesClassPrice links (no
// mutual exclusion) — the no-show fee and the class-price charge on the same
// booking are separate settlement facts.
func TestDebitAccount_BookingRefAndPriceBookingRefBothWritten(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "bothrefs")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT7AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctboth0001", identityKey)
	studioKey := createStudio(t, ctx, conn, cp, cons, "mkstudioboth0000001", "Sunrise Yoga Room")
	sessionKey := createSession(t, ctx, conn, cp, cons, "mksessboth0000001", studioKey, "2026-06-25T15:00:00Z", "2026-06-25T15:30:00Z")
	bookingKey := createBooking(t, ctx, conn, cp, cons, "mkbkgboth0000001", sessionKey, identityKey)
	bookingID := bookingKey[len("vtx.booking."):]

	debitReqID := testutil.GenReqID("debitboth0000000001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":4000,"bookingRef":"` + bookingKey + `","priceBookingRef":"` + bookingKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, bookingKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	settlesLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(debitReqID) + ".settles.booking." + bookingID
	if !keyExists(t, ctx, conn, settlesLnk) {
		t.Fatalf("settles link must exist: %s", settlesLnk)
	}
	settlesClassPriceLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(debitReqID) + ".settlesClassPrice.booking." + bookingID
	if !keyExists(t, ctx, conn, settlesClassPriceLnk) {
		t.Fatalf("settlesClassPrice link must exist: %s", settlesClassPriceLnk)
	}
}

// TestDebitAccount_UnknownPriceBookingRefRejected rejects a WellnessDebitAccount whose
// priceBookingRef names a non-existent booking (UnknownBooking).
func TestDebitAccount_UnknownPriceBookingRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownpricebookingref")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT8AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctupbr0001", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("debitupbr0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessDebitAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"priceBookingRef":"vtx.booking.WLABSENTBKG2HJKMNPQR"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, "vtx.booking.WLABSENTBKG2HJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// seedRefund directly seeds a bare wellnessrefund vertex (root {}) — the
// mirror of seedIdentity, standing in for wellness-domain's CancelBooking
// (the only real minter) so refundRef validation can be exercised without
// standing up a full booking/cancellation flow.
func seedRefund(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.wellnessrefund." + id
	doc := map[string]any{"class": "wellnessrefund", "isDeleted": false, "data": map[string]any{}}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed refund %s: %v", key, err)
	}
	return key
}

// TestCreditAccount_RefundRefWritesSettlesRefundLink proves WellnessCreditAccount
// carrying refundRef writes the settlesRefund audit link, and that a plain
// WellnessCreditAccount (no refundRef) writes no such link — the
// WellnessCreditAccount mirror of TestDebitAccount_BookingRefWritesSettlesLink.
func TestCreditAccount_RefundRefWritesSettlesRefundLink(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "refundref")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT5AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctrfr0001", identityKey)
	refundKey := seedRefund(t, ctx, conn, "WLREFUNDMKR1HJKMNPQR")
	refundID := refundKey[len("vtx.wellnessrefund."):]

	creditReqID := testutil.GenReqID("creditrfr0000000001")
	creditEnv := &processor.OperationEnvelope{
		RequestID:     creditReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"refundRef":"` + refundKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, refundKey}},
	}
	testutil.PublishOp(t, conn, creditEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	settlesRefundLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(creditReqID) + ".settlesRefund.wellnessrefund." + refundID
	if !keyExists(t, ctx, conn, settlesRefundLnk) {
		t.Fatalf("settlesRefund link must exist: %s", settlesRefundLnk)
	}

	// A plain WellnessCreditAccount (no refundRef) writes no settlesRefund link at all.
	plainReqID := testutil.GenReqID("creditrfr0000000002")
	plainEnv := &processor.OperationEnvelope{
		RequestID:     plainReqID,
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:05:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, plainEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	plainSettlesRefundLnk := "lnk.wellnesstransaction." + nanoIDFromRequestID(plainReqID) + ".settlesRefund.wellnessrefund." + refundID
	if keyExists(t, ctx, conn, plainSettlesRefundLnk) {
		t.Fatalf("a plain WellnessCreditAccount with no refundRef must write no settlesRefund link, found %s", plainSettlesRefundLnk)
	}
}

// TestCreditAccount_UnknownRefundRefRejected rejects a WellnessCreditAccount whose
// refundRef names a non-existent wellnessrefund marker (UnknownRefund) — the
// WellnessCreditAccount mirror of TestDebitAccount_UnknownPriceBookingRefRejected.
func TestCreditAccount_UnknownRefundRefRejected(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "unknownrefundref")

	identityKey := seedIdentity(t, ctx, conn, "WLMK23456789ABCDT6AB")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctuqr0001", identityKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("credituqr0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreditAccount",
		Actor:         ledgerActorKey,
		SubmittedAt:   "2026-06-26T09:00:00Z",
		Class:         "wellnesstransaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"refundRef":"vtx.wellnessrefund.WLABSENTRFD2HJKMNPQR"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, "vtx.wellnessrefund.WLABSENTRFD2HJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
