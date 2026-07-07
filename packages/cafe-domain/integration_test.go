// cafe-domain integration tests through the real install + Processor
// pipeline. External test package (cafedomain_test) so they exercise the
// public Lattice surface: seed the kernel, install rbac + identity + hygiene
// + orchestration-base + service-domain + lease-signing + cafe-ledger +
// cafe-domain through the Processor, then submit the ops and assert the
// committed Core-KV shape + the emitted events.
package cafedomain_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/pkgmgr"
	"github.com/asolgan/lattice/internal/processor"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/testutil"
	cafedomain "github.com/asolgan/lattice/packages/cafe-domain"
	cafeledger "github.com/asolgan/lattice/packages/cafe-ledger"
	leasesigning "github.com/asolgan/lattice/packages/lease-signing"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
	servicedomain "github.com/asolgan/lattice/packages/service-domain"
)

const (
	domainActorID  = "BBCAFEDMANACTHJKMNPQ"
	domainActorKey = "vtx.identity." + domainActorID
	domainCapKey   = "cap.identity." + domainActorID

	domainConsumerRoleID = "BBConsumerRoZeCafeDo"
)

func domainCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    domainCapKey,
		Actor:                  domainActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{domainActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "CreateLeaseApplication", Scope: "any"},
			{OperationType: "CreateAccount", Scope: "any"},
			{OperationType: "DebitAccount", Scope: "any"},
			{OperationType: "CreditAccount", Scope: "any"},
			{OperationType: "OpenTab", Scope: "any"},
			{OperationType: "Charge", Scope: "any"},
			{OperationType: "Settle", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{bootstrap.RoleOperatorKey},
	}
}

func setupDomainEnv(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	ctx, conn := testutil.SetupPackageTestEnv(t) // rbac + identity + hygiene
	stop := testutil.RunMetaInstallPipeline(t, ctx, conn)
	defer stop()
	inst := pkgmgr.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": domainConsumerRoleID}
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
	if _, err := inst.Install(ctx, cafedomain.Package); err != nil {
		t.Fatalf("install cafe-domain: %v", err)
	}
	testutil.SeedCapDoc(t, ctx, conn, domainCapDoc())
	return ctx, conn
}

func newDomainPipeline(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) (*processor.CommitPath, jetstream.Consumer) {
	t.Helper()
	return testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:  durable,
		Instance: "cd-" + durable,
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

// openTab submits OpenTab{leaseAppKey} and returns the tab key.
func openTab(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{leaseAppKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.tab." + nanoIDFromRequestID(reqID)
}

func TestOpenTab_MintsTabOpenForLease(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "opentab")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNLEASEHJKMNP")
	leaseID := "BBCAFEDMNLEASEHJKMNP"

	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentab000000001", leaseKey)
	tabID := tabKey[len("vtx.tab."):]

	tabDoc := readDoc(t, ctx, conn, tabKey)
	if d, _ := tabDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("tab root data must stay minimal ({}) after OpenTab, got %v", d)
	}

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["value"].(string); got != "open" {
		t.Fatalf("status.value = %q, want open", got)
	}
	if got, _ := statusData["totalCents"].(float64); got != 0 {
		t.Fatalf("status.totalCents = %v, want 0", got)
	}
	if got, _ := statusData["leaseAppKey"].(string); got != leaseKey {
		t.Fatalf("status.leaseAppKey = %q, want %q", got, leaseKey)
	}

	openForLnk := "lnk.tab." + tabID + ".openFor.leaseapp." + leaseID
	if !keyExists(t, ctx, conn, openForLnk) {
		t.Fatalf("openFor link must exist: %s", openForLnk)
	}
}

func TestOpenTab_UnknownLease(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "unknownlease")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdopenunknown0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"vtx.leaseapp.BBABSENTLEASEHJKMNPQ"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{"vtx.leaseapp.BBABSENTLEASEHJKMNPQ"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestCharge_AccumulatesTotalCents(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeaccum")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNCHGLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabchg00000001", leaseKey)

	charge := func(reqLabel string, amountCents int) {
		reqID := testutil.GenReqID(reqLabel)
		env := &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "Charge",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-07T12:05:00Z",
			Class:         "tab",
			Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":` + strconv.Itoa(amountCents) + `}`),
			ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	}
	charge("cdchargeone00000001", 450)
	charge("cdchargetwo00000001", 300)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 750 {
		t.Fatalf("status.totalCents = %v, want 750 (450+300)", got)
	}
	if got, _ := statusData["value"].(string); got != "open" {
		t.Fatalf("status.value = %q, want open (still charging)", got)
	}
}

func TestCharge_RejectsNonPositiveAmount(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargebad")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNBADLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabbad00000001", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdchargebadamt000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestSettle_ClosesTabFreezesTotal(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "settle")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSETLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabset00000001", leaseKey)

	chargeReqID := testutil.GenReqID("cdchargesettle000001")
	chargeEnv := &processor.OperationEnvelope{
		RequestID:     chargeReqID,
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":1200}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettletab000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, settleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["value"].(string); got != "settled" {
		t.Fatalf("status.value = %q, want settled", got)
	}
	if got, _ := statusData["totalCents"].(float64); got != 1200 {
		t.Fatalf("status.totalCents = %v, want 1200 (frozen)", got)
	}
	if _, ok := statusData["settledAt"]; !ok {
		t.Fatalf("status.settledAt must be stamped on settle")
	}
}

func TestSettle_RejectsDoubleSettle(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "doublesettle")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNDBLLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabdbl00000001", leaseKey)

	settleOnce := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettledbl000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, settleOnce)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	settleTwice := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettledbl000000002"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, settleTwice)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestCharge_RejectsAfterSettle(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeaftersettle")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNCASLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabcas00000001", leaseKey)

	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettlecas000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, settleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdchargecas000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":500}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}
