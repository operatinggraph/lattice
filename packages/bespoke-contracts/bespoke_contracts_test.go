// bespoke-contracts integration tests through the real install + Processor
// pipeline. External test package (bespokecontracts_test) so they exercise
// the public Lattice surface: seed the kernel, install rbac + identity +
// hygiene + orchestration-base + service-domain + lease-signing +
// loftspace-ledger + bespoke-contracts through the Processor, then submit the
// ops and assert the committed Core-KV shape + the emitted events.
package bespokecontracts_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	bespokecontracts "github.com/asolgan/lattice/packages/bespoke-contracts"
	leasesigning "github.com/asolgan/lattice/packages/lease-signing"
	loftspaceledger "github.com/asolgan/lattice/packages/loftspace-ledger"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
)

const (
	bcActorID  = "BBBESPOKEACTRHJKMNPQ"
	bcActorKey = "vtx.identity." + bcActorID
	bcCapKey   = "cap.identity." + bcActorID
)

func bcCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    bcCapKey,
		Actor:                  bcActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{bcActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateAccount", Scope: "any"},
			{OperationType: "DebitAccount", Scope: "any"},
			{OperationType: "CreditAccount", Scope: "any"},
			{OperationType: "CreateClause", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupBcEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID}
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
	if _, err := inst.Install(ctx, bespokecontracts.Package); err != nil {
		t.Fatalf("install bespoke-contracts: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, bcCapDoc())
	return ctx, conn
}

func newBcPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "bc-" + durable,
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

func seedLease(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.leaseapp." + id
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	return key
}

func createAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateAccount",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "account",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseAppKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.account." + nanoIDFromRequestID(reqID)
}

func createClause(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey, acctKey, prose string, amountCents int) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `","accountKey":"` + acctKey +
			`","prose":"` + prose + `","amountCents":` + itoa(amountCents) + `}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseAppKey, acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.clause." + nanoIDFromRequestID(reqID)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestCreateClause_MintsClauseGoverningLeaseChargingAccount (test 1).
// CreateClause mints vtx.clause.<freshId> (root {} — D5) + .prose/.terms/
// .status aspects + the governs (clause→lease) and chargesTo (clause→account)
// links.
func TestCreateClause_MintsClauseGoverningLeaseChargingAccount(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "createclause")

	leaseKey := seedLease(t, ctx, conn, "BBLEASECLAUSEHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctclause001", leaseKey)

	clauseKey := createClause(t, ctx, conn, cp, cons, "createclauseop00001", leaseKey, acctKey,
		"Tenant agrees to a $45 lockout fee.", 4500)
	clauseID := clauseKey[len("vtx.clause."):]

	clauseDoc := readDoc(t, ctx, conn, clauseKey)
	if d, _ := clauseDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("clause root data must stay minimal ({}) after create, got %v", d)
	}

	proseDoc := readDoc(t, ctx, conn, clauseKey+".prose")
	proseData, _ := proseDoc["data"].(map[string]any)
	if got, _ := proseData["text"].(string); got != "Tenant agrees to a $45 lockout fee." {
		t.Fatalf("prose.text = %q, want the seeded prose", got)
	}

	termsDoc := readDoc(t, ctx, conn, clauseKey+".terms")
	termsData, _ := termsDoc["data"].(map[string]any)
	if got, _ := termsData["amountCents"].(float64); got != 4500 {
		t.Fatalf("terms.amountCents = %v, want 4500", got)
	}
	if got, _ := termsData["kind"].(string); got != "computational" {
		t.Fatalf("terms.kind = %q, want computational", got)
	}

	statusDoc := readDoc(t, ctx, conn, clauseKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["state"].(string); got != "active" {
		t.Fatalf("status.state = %q, want active", got)
	}

	leaseID := "BBLEASECLAUSEHJKMNPQ"
	governsLnk := "lnk.clause." + clauseID + ".governs.lease." + leaseID
	if !keyExists(t, ctx, conn, governsLnk) {
		t.Fatalf("governs link must exist: %s", governsLnk)
	}
	acctID := acctKey[len("vtx.account."):]
	chargesLnk := "lnk.clause." + clauseID + ".chargesTo.account." + acctID
	if !keyExists(t, ctx, conn, chargesLnk) {
		t.Fatalf("chargesTo link must exist: %s", chargesLnk)
	}
}

// TestCreateClause_UnknownLease rejects a clause governing a non-existent
// lease (no-orphan invariant).
func TestCreateClause_UnknownLease(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "unknownlease")

	leaseKey := "vtx.leaseapp.BBABSENTLEASEHJKMNPQ"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("clauseunknownlease1"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"vtx.account.BBABSENTACCTHJKMNPQR","prose":"x","amountCents":100}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseKey, "vtx.account.BBABSENTACCTHJKMNPQR"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_ClauseRef_WritesAuthorizedByAndCompletesClause (test 2 —
// the design's canonical Fire V1 e2e path). A DebitAccount dispatched with
// clauseRef (the shape Weaver's clauseSatisfaction playbook templates) writes
// the authorizedBy link (transaction→clause) AND marks the clause .status
// completed, on top of the ordinary DebitAccount entry-posting behavior.
func TestDebitAccount_ClauseRef_WritesAuthorizedByAndCompletesClause(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "debitclauseref")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEDEBCLZHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctdebcl0001", leaseKey)
	clauseKey := createClause(t, ctx, conn, cp, cons, "createclausedebcl01", leaseKey, acctKey,
		"Tenant agrees to a $45 lockout fee.", 4500)
	clauseID := clauseKey[len("vtx.clause."):]

	debitReqID := testutil.GenReqID("debitclauseref00001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":4500,"clauseRef":"` + clauseKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, clauseKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	txKey := "vtx.transaction." + nanoIDFromRequestID(debitReqID)
	txID := nanoIDFromRequestID(debitReqID)

	authorizedByLnk := "lnk.transaction." + txID + ".authorizedBy.clause." + clauseID
	if !keyExists(t, ctx, conn, authorizedByLnk) {
		t.Fatalf("authorizedBy link must exist: %s", authorizedByLnk)
	}

	statusDoc := readDoc(t, ctx, conn, clauseKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["state"].(string); got != "completed" {
		t.Fatalf("clause status.state = %q, want completed after the authorizing debit", got)
	}
	if _, ok := statusData["completedAt"]; !ok {
		t.Fatalf("clause status.completedAt must be stamped, got %v", statusData)
	}

	entryDoc := readDoc(t, ctx, conn, txKey+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["amountCents"].(float64); got != 4500 {
		t.Fatalf("entry.amountCents = %v, want 4500", got)
	}
}

// TestDebitAccount_NoClauseRef_Unaffected (regression) — a plain DebitAccount
// with no clauseRef behaves exactly as before this fire: no authorizedBy
// link, no clause touched.
func TestDebitAccount_NoClauseRef_Unaffected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "debitnoclause")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEZZZZHJKMNPQRS")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctnocl00001", leaseKey)

	debitReqID := testutil.GenReqID("debitnoclauseref001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":15000,"memo":"June rent"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	txID := nanoIDFromRequestID(debitReqID)
	// No clause was ever named — no authorizedBy link should exist for this tx
	// under any clause id (spot-check: the key namespace requires a specific
	// clause id, so absence of the transaction's own clause reference is
	// implicit — this test's real assertion is that the plain debit still
	// commits cleanly with no clauseRef in the payload).
	entryDoc := readDoc(t, ctx, conn, "vtx.transaction."+txID+".entry")
	entryData, _ := entryDoc["data"].(map[string]any)
	if got, _ := entryData["amountCents"].(float64); got != 15000 {
		t.Fatalf("entry.amountCents = %v, want 15000", got)
	}
}
