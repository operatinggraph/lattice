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
			{OperationType: "InspectPremises", Scope: "any"},
			{OperationType: "SupersedeClause", Scope: "any"},
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

func seedIdentity(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.identity." + id
	seedVertex(t, ctx, conn, key, "identity", map[string]any{})
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

// TestCreateClause_ConditionedFee_WritesConditionedOnLink — CreateClause with
// a conditionedOnKey writes the conditionedOn link (clause→that vertex) and
// terms.conditioned=true.
func TestCreateClause_ConditionedFee_WritesConditionedOnLink(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "condfee")

	leaseKey := seedLease(t, ctx, conn, "BBLEASECNDFEEHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctcondfee01", leaseKey)
	petKey := seedIdentity(t, ctx, conn, "BBPETCNDFEEHJKMNPQRS") // any live vertex qualifies

	reqID := testutil.GenReqID("createclausecondfee1")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
			`","prose":"Tenant agrees to a $50 monthly pet fee.","amountCents":5000,"conditionedOnKey":"` + petKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey, petKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	clauseKey := "vtx.clause." + nanoIDFromRequestID(reqID)
	clauseID := clauseKey[len("vtx.clause."):]
	petID := petKey[len("vtx.identity."):]

	termsDoc := readDoc(t, ctx, conn, clauseKey+".terms")
	termsData, _ := termsDoc["data"].(map[string]any)
	if got, _ := termsData["conditioned"].(bool); !got {
		t.Fatalf("terms.conditioned = %v, want true", termsData["conditioned"])
	}

	condLnk := "lnk.clause." + clauseID + ".conditionedOn.identity." + petID
	if !keyExists(t, ctx, conn, condLnk) {
		t.Fatalf("conditionedOn link must exist: %s", condLnk)
	}
}

// TestCreateClause_ConditionedFee_UnknownConditionVertex rejects a
// conditionedOnKey naming a vertex that doesn't exist.
func TestCreateClause_ConditionedFee_UnknownConditionVertex(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "condfeeunknown")

	leaseKey := seedLease(t, ctx, conn, "BBLEASECNDUNKHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctcondunk01", leaseKey)
	absentPetKey := "vtx.identity.BBABSENTPETHJKMNPQRS"

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createclausecondunk1"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
			`","prose":"x","amountCents":5000,"conditionedOnKey":"` + absentPetKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey, absentPetKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateClause_JudgmentClause_WritesInspectorLinkNoCharge — CreateClause
// with kind=judgment writes the requiresInspectionBy link, no chargesTo link,
// and terms carries no amountCents.
func TestCreateClause_JudgmentClause_WritesInspectorLinkNoCharge(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "judgment")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEJUDGMENTHJKMN")
	inspKey := seedIdentity(t, ctx, conn, "BBAGENTJUDGMENTHJKMN")

	reqID := testutil.GenReqID("createclausejudgment1")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","kind":"judgment",` +
			`"prose":"Landlord will inspect before move-in.","inspectorKey":"` + inspKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, inspKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	clauseKey := "vtx.clause." + nanoIDFromRequestID(reqID)
	clauseID := clauseKey[len("vtx.clause."):]
	inspID := inspKey[len("vtx.identity."):]

	termsDoc := readDoc(t, ctx, conn, clauseKey+".terms")
	termsData, _ := termsDoc["data"].(map[string]any)
	if got, _ := termsData["kind"].(string); got != "judgment" {
		t.Fatalf("terms.kind = %q, want judgment", got)
	}
	if _, ok := termsData["amountCents"]; ok {
		t.Fatalf("a judgment clause must carry no amountCents, got %v", termsData["amountCents"])
	}

	inspLnk := "lnk.clause." + clauseID + ".requiresInspectionBy.identity." + inspID
	if !keyExists(t, ctx, conn, inspLnk) {
		t.Fatalf("requiresInspectionBy link must exist: %s", inspLnk)
	}

	// No chargesTo link exists for ANY account under a judgment clause — spot
	// check there's no chargesTo link namespace collision with the inspector.
	badChargesLnk := "lnk.clause." + clauseID + ".chargesTo.account." + inspID
	if keyExists(t, ctx, conn, badChargesLnk) {
		t.Fatalf("a judgment clause must not write a chargesTo link")
	}
}

// TestCreateClause_JudgmentClause_UnknownInspector rejects an inspectorKey
// naming an identity that doesn't exist.
func TestCreateClause_JudgmentClause_UnknownInspector(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "judgmentunknown")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEJUDGUNKHJKMNP")
	absentInspKey := "vtx.identity.BBABSENTAGNTHJKMNPQR"

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("createclausejudgunk1"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","kind":"judgment",` +
			`"prose":"x","inspectorKey":"` + absentInspKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, absentInspKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestInspectPremises_WritesInspectionAspect (the design's Fire V2 judgment
// e2e path). InspectPremises writes the .inspection aspect on the clause the
// §10.8 playbook's missing_inspection gap templates.
func TestInspectPremises_WritesInspectionAspect(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "inspectpremises")

	leaseKey := seedLease(t, ctx, conn, "BBLEASECHECKHJKMNPQR")
	inspKey := seedIdentity(t, ctx, conn, "BBAGENTCHECKHJKMNPQR")

	clauseReqID := testutil.GenReqID("createclauseinspect1")
	clauseEnv := &processor.OperationEnvelope{
		RequestID:     clauseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","kind":"judgment",` +
			`"prose":"Landlord will inspect before move-in.","inspectorKey":"` + inspKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, inspKey}},
	}
	testutil.PublishOp(t, conn, clauseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	clauseKey := "vtx.clause." + nanoIDFromRequestID(clauseReqID)

	inspectEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("inspectpremises00001"),
		Lane:          processor.LaneDefault,
		OperationType: "InspectPremises",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{clauseKey}},
	}
	testutil.PublishOp(t, conn, inspectEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	inspDoc := readDoc(t, ctx, conn, clauseKey+".inspection")
	inspData, _ := inspDoc["data"].(map[string]any)
	if got, _ := inspData["completed"].(bool); !got {
		t.Fatalf("inspection.completed = %v, want true", inspData["completed"])
	}
	if _, ok := inspData["completedAt"]; !ok {
		t.Fatalf("inspection.completedAt must be stamped, got %v", inspData)
	}
}

// TestInspectPremises_AlreadyInspected_Rejected — a second InspectPremises
// against the same clause is rejected (CreateOnly once-only write, mirrors
// SignLease's AlreadySigned check).
func TestInspectPremises_AlreadyInspected_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "inspecttwice")

	leaseKey := seedLease(t, ctx, conn, "BBLEASECHECKTWCEHJKM")
	inspKey := seedIdentity(t, ctx, conn, "BBAGENTCHECKTWCEHJKM")

	clauseReqID := testutil.GenReqID("createclauseinstwic1")
	clauseEnv := &processor.OperationEnvelope{
		RequestID:     clauseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","kind":"judgment",` +
			`"prose":"x","inspectorKey":"` + inspKey + `"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, inspKey}},
	}
	testutil.PublishOp(t, conn, clauseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	clauseKey := "vtx.clause." + nanoIDFromRequestID(clauseReqID)

	firstInspectEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("inspecttwice0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "InspectPremises",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{clauseKey}},
	}
	testutil.PublishOp(t, conn, firstInspectEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	secondInspectEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("inspecttwice0000002"),
		Lane:          processor.LaneDefault,
		OperationType: "InspectPremises",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T14:00:00Z",
		Class:         "clause",
		Payload:       json.RawMessage(`{"clauseKey":"` + clauseKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{clauseKey, clauseKey + ".inspection"}},
	}
	testutil.PublishOp(t, conn, secondInspectEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
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

// TestCreateClause_Prorated_ComputesExactAmountCents (Fire V3, the design's
// §7/§8 money-precision golden test): a proration clause's amountCents =
// (rateCents*daysOccupied)/periodDays must land on EXACT integer cents via
// Starlark bignum integer floor division, including at the exact-divides
// boundary where naive float64 division risks an off-by-one (e.g.
// 9000/30=300.0 could round to 299.999... and floor wrong).
func TestCreateClause_Prorated_ComputesExactAmountCents(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "prorated")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEPRATEDHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctprorated1", leaseKey)

	cases := []struct {
		name                                                 string
		rateCents, periodDays, daysOccupied, wantAmountCents int
	}{
		{"fractional", 5000, 30, 17, 2833},          // (5000*17)/30 = 2833.33.. -> 2833
		{"exact-full-period", 9000, 30, 30, 9000},   // daysOccupied==periodDays: must be exact, not 8999
		{"exact-divides-cleanly", 6000, 3, 1, 2000}, // (6000*1)/3 = 2000.0 exactly
	}
	for i, tc := range cases {
		reqID := testutil.GenReqID("prorated" + itoa(i) + "00000")
		env := &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "CreateClause",
			Actor:         bcActorKey,
			SubmittedAt:   "2026-07-02T12:00:00Z",
			Class:         "clause",
			Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
				`","prose":"Prorated amenity fee.","rateCents":` + itoa(tc.rateCents) +
				`,"periodDays":` + itoa(tc.periodDays) + `,"daysOccupied":` + itoa(tc.daysOccupied) + `}`),
			ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

		clauseKey := "vtx.clause." + nanoIDFromRequestID(reqID)
		termsDoc := readDoc(t, ctx, conn, clauseKey+".terms")
		termsData, _ := termsDoc["data"].(map[string]any)
		if got, _ := termsData["amountCents"].(float64); got != float64(tc.wantAmountCents) {
			t.Fatalf("%s: terms.amountCents = %v, want %d", tc.name, termsData["amountCents"], tc.wantAmountCents)
		}
		if got, _ := termsData["basis"].(string); got != "daysOccupied" {
			t.Fatalf("%s: terms.basis = %q, want daysOccupied", tc.name, got)
		}
		if got, _ := termsData["period"].(string); got != "oneTime" {
			t.Fatalf("%s: terms.period = %q, want oneTime (proration is one-time only)", tc.name, got)
		}
	}
}

// TestCreateClause_Prorated_RejectsMonthlyPeriod — proration
// (rateCents/periodDays/daysOccupied) combined with period=monthly is
// rejected; the design scopes proration to one-time only (§10 Fire V3).
func TestCreateClause_Prorated_RejectsMonthlyPeriod(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "proratedmonthly")

	leaseKey := seedLease(t, ctx, conn, "BBLEASEPRATEMTHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctproratemo1", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("proratedmonthly00001"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
			`","prose":"x","rateCents":5000,"periodDays":30,"daysOccupied":17,"period":"monthly"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestDebitAccount_RecurringClause_ReArmsChargeValidUntil (Fire V3, the
// design's canonical recurring e2e path). A DebitAccount dispatched with
// clauseRef + period="monthly" (the shape Weaver's clauseSatisfaction
// playbook templates for a period=monthly clause) writes the authorizedBy
// link AND re-arms .status.chargeValidUntil — state stays active, never
// completed.
func TestDebitAccount_RecurringClause_ReArmsChargeValidUntil(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "recurdebit")

	leaseKey := seedLease(t, ctx, conn, "BBLEASERECURDEBHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctrecurdeb01", leaseKey)

	clauseReqID := testutil.GenReqID("createclauserecurdb1")
	clauseEnv := &processor.OperationEnvelope{
		RequestID:     clauseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
			`","prose":"Monthly smart-home fee.","amountCents":1500,"period":"monthly"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, clauseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	clauseKey := "vtx.clause." + nanoIDFromRequestID(clauseReqID)
	clauseID := clauseKey[len("vtx.clause."):]

	debitReqID := testutil.GenReqID("debitrecurring00001")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"clauseRef":"` + clauseKey + `","period":"monthly"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, clauseKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	txID := nanoIDFromRequestID(debitReqID)
	authorizedByLnk := "lnk.transaction." + txID + ".authorizedBy.clause." + clauseID
	if !keyExists(t, ctx, conn, authorizedByLnk) {
		t.Fatalf("authorizedBy link must exist: %s", authorizedByLnk)
	}

	statusDoc := readDoc(t, ctx, conn, clauseKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["state"].(string); got != "active" {
		t.Fatalf("recurring clause status.state = %q, want active (never completed)", got)
	}
	if _, ok := statusData["completedAt"]; ok {
		t.Fatalf("recurring clause must never carry completedAt, got %v", statusData)
	}
	cvu, ok := statusData["chargeValidUntil"].(string)
	if !ok || cvu == "" {
		t.Fatalf("recurring clause status.chargeValidUntil must be stamped, got %v", statusData)
	}
	debitAt, err := time.Parse(time.RFC3339, "2026-07-02T13:00:00Z")
	if err != nil {
		t.Fatalf("parse debit time: %v", err)
	}
	gotValidUntil, err := time.Parse(time.RFC3339, cvu)
	if err != nil {
		t.Fatalf("chargeValidUntil %q is not RFC3339: %v", cvu, err)
	}
	if !gotValidUntil.After(debitAt) {
		t.Fatalf("chargeValidUntil %s must be after the debit instant %s", cvu, debitAt)
	}
}

// TestDebitAccount_RecurringClause_MismatchedPeriodOmitted_StillReArms —
// defense-in-depth regression (adversarial review finding, Fire V3): a
// clauseRef debit against a GENUINELY period=monthly clause, submitted with
// NO period param (the shape a hand-submitted, non-Weaver DebitAccount could
// send since this op has no read of the clause's own .terms.data.period to
// cross-check the caller's claim), must still stamp chargeValidUntil. If it
// didn't, the clauseSatisfaction lens's monthly gate (which reads only
// chargeValidUntil, never .status.state) would find chargeValidUntil
// permanently null and treat the clause as permanently violating — an
// uncontrolled repeated-charge loop. state="completed" is expected here too
// (the caller's period was absent, so the completing branch fires) but is
// harmless: the monthly gate never reads `state`.
func TestDebitAccount_RecurringClause_MismatchedPeriodOmitted_StillReArms(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "recurmismatch")

	leaseKey := seedLease(t, ctx, conn, "BBLEASERECURMSTHJKMN")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctrecurmis01", leaseKey)

	clauseReqID := testutil.GenReqID("createclauserecurms1")
	clauseEnv := &processor.OperationEnvelope{
		RequestID:     clauseReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T12:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"leaseAppKey":"` + leaseKey + `","accountKey":"` + acctKey +
			`","prose":"Monthly smart-home fee.","amountCents":1500,"period":"monthly"}`),
		ContextHint: &processor.ContextHint{Reads: []string{leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, clauseEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	clauseKey := "vtx.clause." + nanoIDFromRequestID(clauseReqID)

	// Deliberately NO "period" field — the mismatched/omitted-caller shape.
	debitReqID := testutil.GenReqID("debitrecurmismatch1")
	debitEnv := &processor.OperationEnvelope{
		RequestID:     debitReqID,
		Lane:          processor.LaneDefault,
		OperationType: "DebitAccount",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T13:00:00Z",
		Class:         "transaction",
		Payload:       json.RawMessage(`{"accountKey":"` + acctKey + `","amountCents":1500,"clauseRef":"` + clauseKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{acctKey, clauseKey}},
	}
	testutil.PublishOp(t, conn, debitEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, clauseKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	cvu, ok := statusData["chargeValidUntil"].(string)
	if !ok || cvu == "" {
		t.Fatalf("chargeValidUntil must be stamped even when the caller omitted period, got %v", statusData)
	}
	if _, err := time.Parse(time.RFC3339, cvu); err != nil {
		t.Fatalf("chargeValidUntil %q is not RFC3339: %v", cvu, err)
	}
}

// TestSupersedeClause_TombstonesOldWritesAmendsLinkMintsNew (Fire V4, the
// design's canonical self-amendment e2e path). SupersedeClause mints a
// replacement clause exactly like CreateClause, tombstones the amended
// clause's root (the anchor-tombstone retraction signal), marks its .status
// superseded, and writes the amends link (new clause→old clause).
func TestSupersedeClause_TombstonesOldWritesAmendsLinkMintsNew(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "supersede")

	leaseKey := seedLease(t, ctx, conn, "BBLEASESUPERSDHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctsupersede1", leaseKey)
	oldClauseKey := createClause(t, ctx, conn, cp, cons, "createclausesupersd1", leaseKey, acctKey,
		"Tenant agrees to a $45 lockout fee.", 4500)
	oldClauseID := oldClauseKey[len("vtx.clause."):]

	supersedeReqID := testutil.GenReqID("supersedeclause0001")
	supersedeEnv := &processor.OperationEnvelope{
		RequestID:     supersedeReqID,
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T14:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + oldClauseKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"Tenant agrees to a $55 lockout fee (amended).","amountCents":5500}`),
		ContextHint: &processor.ContextHint{Reads: []string{oldClauseKey, leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, supersedeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	newClauseKey := "vtx.clause." + nanoIDFromRequestID(supersedeReqID)
	newClauseID := newClauseKey[len("vtx.clause."):]

	oldDoc := readDoc(t, ctx, conn, oldClauseKey)
	if del, _ := oldDoc["isDeleted"].(bool); !del {
		t.Fatalf("old clause root must be tombstoned (isDeleted=true), got %v", oldDoc)
	}

	oldStatusDoc := readDoc(t, ctx, conn, oldClauseKey+".status")
	oldStatusData, _ := oldStatusDoc["data"].(map[string]any)
	if got, _ := oldStatusData["state"].(string); got != "superseded" {
		t.Fatalf("old clause status.state = %q, want superseded", got)
	}
	if got, _ := oldStatusData["supersededBy"].(string); got != newClauseKey {
		t.Fatalf("old clause status.supersededBy = %q, want %q", got, newClauseKey)
	}
	if _, ok := oldStatusData["supersededAt"]; !ok {
		t.Fatalf("old clause status.supersededAt must be stamped, got %v", oldStatusData)
	}

	amendsLnk := "lnk.clause." + newClauseID + ".amends.clause." + oldClauseID
	if !keyExists(t, ctx, conn, amendsLnk) {
		t.Fatalf("amends link must exist: %s", amendsLnk)
	}

	newTermsDoc := readDoc(t, ctx, conn, newClauseKey+".terms")
	newTermsData, _ := newTermsDoc["data"].(map[string]any)
	if got, _ := newTermsData["amountCents"].(float64); got != 5500 {
		t.Fatalf("new clause terms.amountCents = %v, want 5500", got)
	}

	newStatusDoc := readDoc(t, ctx, conn, newClauseKey+".status")
	newStatusData, _ := newStatusDoc["data"].(map[string]any)
	if got, _ := newStatusData["state"].(string); got != "active" {
		t.Fatalf("new clause status.state = %q, want active", got)
	}

	newGovernsLnk := "lnk.clause." + newClauseID + ".governs.lease." + "BBLEASESUPERSDHJKMNP"
	if !keyExists(t, ctx, conn, newGovernsLnk) {
		t.Fatalf("new clause's own governs link must exist: %s", newGovernsLnk)
	}
}

// TestSupersedeClause_UnknownOldClause_Rejected — SupersedeClause naming a
// clauseKey that isn't alive (never existed, or already superseded) is
// rejected, so a clause can only be amended once at a time.
func TestSupersedeClause_UnknownOldClause_Rejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "supersedeunknown")

	leaseKey := seedLease(t, ctx, conn, "BBLEASESUPUNKHJKMNPQ")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctsupunk001", leaseKey)
	absentClauseKey := "vtx.clause.BBABSENTCLAUSEHJKMNP"

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("supersedeunknown0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T14:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + absentClauseKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"x","amountCents":100}`),
		ContextHint: &processor.ContextHint{Reads: []string{absentClauseKey, leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestSupersedeClause_DoubleSupersede_SecondRejected — a second
// SupersedeClause naming the same already-superseded clause is rejected: the
// first supersede's tombstone makes the old clause not-alive, so a clause can
// only be amended once (the amendment chain grows forward through the amends
// link, never branches).
func TestSupersedeClause_DoubleSupersede_SecondRejected(t *testing.T) {
	ctx, conn := setupBcEnv(t)
	cp, cons := newBcPipeline(t, ctx, conn, "supersedetwice")

	leaseKey := seedLease(t, ctx, conn, "BBLEASESUPTWCEHJKMNP")
	acctKey := createAccount(t, ctx, conn, cp, cons, "createacctsuptwice1", leaseKey)
	oldClauseKey := createClause(t, ctx, conn, cp, cons, "createclausesuptwc1", leaseKey, acctKey,
		"Tenant agrees to a $45 lockout fee.", 4500)

	firstEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("supersedetwice00001"),
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T14:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + oldClauseKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"$55 amended.","amountCents":5500}`),
		ContextHint: &processor.ContextHint{Reads: []string{oldClauseKey, leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, firstEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	secondEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("supersedetwice00002"),
		Lane:          processor.LaneDefault,
		OperationType: "SupersedeClause",
		Actor:         bcActorKey,
		SubmittedAt:   "2026-07-02T15:00:00Z",
		Class:         "clause",
		Payload: json.RawMessage(`{"clauseKey":"` + oldClauseKey + `","leaseAppKey":"` + leaseKey +
			`","accountKey":"` + acctKey + `","prose":"$60 amended again.","amountCents":6000}`),
		ContextHint: &processor.ContextHint{Reads: []string{oldClauseKey, leaseKey, acctKey}},
	}
	testutil.PublishOp(t, conn, secondEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
