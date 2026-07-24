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
	"regexp"
	"slices"
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
	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
	cafeledger "github.com/operatinggraph/lattice/packages/cafe-ledger"
	leasesigning "github.com/operatinggraph/lattice/packages/lease-signing"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	servicedomain "github.com/operatinggraph/lattice/packages/service-domain"
)

const (
	domainActorID  = "BBCAFEDMANACTHJKMNPQ"
	domainActorKey = "vtx.identity." + domainActorID
	domainCapKey   = "cap.identity." + domainActorID

	domainConsumerRoleID = "BBConsumerRoZeCafeDo"

	// domainConsumerID stands in for identity-domain's real `consumer` role
	// grant flow (mirrors wellness-domain's domainConsumerID) — the
	// self-service caller's own identity, distinct from the operator actor
	// above.
	domainConsumerID  = "BBCAFEDMANCQNSHJKMNP"
	domainConsumerKey = "vtx.identity." + domainConsumerID
	domainConsumerCap = "cap.identity." + domainConsumerID
)

// domainConsumerCapDoc grants the consumer role's scope=self OpenTab /
// Settle permissions — the real-actor-write-auth-e2e self-service caller,
// mirrors wellness-domain's domainConsumerCapDoc.
func domainConsumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    domainConsumerCap,
		Actor:                  domainConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{domainConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "OpenTab", Scope: "self"},
			{OperationType: "Charge", Scope: "self"},
			{OperationType: "Settle", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

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
			{OperationType: "VoidCharge", Scope: "any"},
			{OperationType: "Settle", Scope: "any"},
			{OperationType: "CreateMenuItem", Scope: "any"},
			{OperationType: "RetireMenuItem", Scope: "any"},
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
	inst.RoleIDs = map[string]string{"operator": bootstrap.RoleOperatorID, "consumer": domainConsumerRoleID, "frontOfHouse": pkgmgr.RoleID("identity-domain", "frontOfHouse"), "backOfHouse": pkgmgr.RoleID("identity-domain", "backOfHouse"), "provider": pkgmgr.RoleID("identity-domain", "provider")}
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
	// The operator grant is only half the claim — the workplace-confinement
	// guard reads the holdsRole LINK to decide whether its caller is root.
	testutil.SeedHoldsRole(t, ctx, conn, domainActorKey, bootstrap.RoleOperatorKey)
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

// tombstoneLink soft-deletes a link the way an unwiring op does — the document
// stays in Core KV with isDeleted:true. This is the case a `kv.Read(k) == None`
// ownership guard silently passes, because a tombstone hydrates as a DOCUMENT,
// not None; the self-guard must read it as absent.
func tombstoneLink(t *testing.T, ctx context.Context, conn *substrate.Conn, key, source, target, class, localName string) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": true,
		"sourceVertex": source, "targetVertex": target,
		"localName": localName, "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("tombstone link %s: %v", key, err)
	}
}

// seedLeaseWithApplicant seeds a leaseapp vertex + its applicationFor link
// to applicantID — the residency check OpenTab/Settle's self-scope guard
// reads (mirrors wellness-domain's seedLease(..., applicantID, ...)).
func seedLeaseWithApplicant(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseID, applicantID string) string {
	t.Helper()
	key := "vtx.leaseapp." + leaseID
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	lnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + applicantID
	seedLink(t, ctx, conn, lnk, key, "vtx.identity."+applicantID, "applicationFor", "applicationFor")
	return key
}

// openTab submits OpenTab{leaseAppKey}, declaring the per-lease
// cafeOpenTab guard in OptionalReads (Contract #2 §2.5 class-(d) — the
// guard legitimately may or may not exist yet), and returns the tab key.
// The caller drives the expected outcome (a lease with an already-open tab
// must reject).
func openTabExpect(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey string, outcome processor.MessageOutcome) string {
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseAppKey},
			OptionalReads: []string{leaseAppKey + ".cafeOpenTab"},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, outcome)
	return "vtx.tab." + nanoIDFromRequestID(reqID)
}

// openTab submits OpenTab{leaseAppKey} expecting acceptance and returns the
// tab key.
func openTab(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseAppKey string) string {
	t.Helper()
	return openTabExpect(t, ctx, conn, cp, cons, label, leaseAppKey, processor.OutcomeAccepted)
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

func TestVoidCharge_SubtractsFromTotalCents(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidsub")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVSUBLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvoi00000001", leaseKey)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdchargevoid000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":850}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidchgone00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:06:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":350}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 500 {
		t.Fatalf("status.totalCents = %v, want 500 (850-350)", got)
	}
	if got, _ := statusData["value"].(string); got != "open" {
		t.Fatalf("status.value = %q, want open (voiding does not close the tab)", got)
	}
}

// TestVoidCharge_ClampsAtZero proves an over-void — subtracting more than the
// tab's current running total — corrects cleanly to 0 rather than rejecting
// or going negative (verticals.md — "decrement not below 0").
func TestVoidCharge_ClampsAtZero(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidclamp")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNCLAMPLEASEH")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabclm00000001", leaseKey)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdchargeclamp0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":300}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidclampbig000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:06:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":9000}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 0 {
		t.Fatalf("status.totalCents = %v, want 0 (clamped, not negative)", got)
	}
}

func TestVoidCharge_RejectsNonPositiveAmount(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidbadamt")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVBADLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvba00000001", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidbadamt000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":0}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestVoidCharge_RejectsAfterSettle proves a settled tab's total is frozen —
// once dispatched to the ledger, it cannot be corrected via VoidCharge.
func TestVoidCharge_RejectsAfterSettle(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidaftersettle")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVASLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvas00000001", leaseKey)

	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettlevas000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, settleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidvas000000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T13:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":500}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestVoidCharge_RejectsForConsumer proves the fraud-vector gate: a resident
// (consumer, scope=self on OpenTab/Charge/Settle only) has no VoidCharge
// grant at all — a self-order mis-tap can only be corrected by staff.
func TestVoidCharge_RejectsForConsumer(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "voidconsumer")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNVCNLEASEHJK", domainConsumerID)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNVCNLEASEHJK.applicationFor.identity." + domainConsumerID

	openReqID := testutil.GenReqID("cdopentabvcn00000001")
	openEnv := &processor.OperationEnvelope{
		RequestID:     openReqID,
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-22T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, openEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	tabKey := "vtx.tab." + nanoIDFromRequestID(openReqID)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidvcn0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":100}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
		AuthContext:   &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, voidEnv)
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

// TestOpenTab_RejectsSecondConcurrentTab proves the fix for the no-guard
// bug: a lease with an already-open tab must reject a second OpenTab
// (verticals.md — "Café tab: no guard against a 2nd concurrent open tab per
// lease").
func TestOpenTab_RejectsSecondConcurrentTab(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabguard")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNGRDLEASEHJK")
	firstTabKey := openTab(t, ctx, conn, cp, cons, "cdopentabgrd00000001", leaseKey)

	secondTabKey := openTabExpect(t, ctx, conn, cp, cons, "cdopentabgrd00000002", leaseKey, processor.OutcomeRejected)

	guardDoc := readDoc(t, ctx, conn, leaseKey+".cafeOpenTab")
	guardData, _ := guardDoc["data"].(map[string]any)
	if got, _ := guardData["tabKey"].(string); got != firstTabKey {
		t.Fatalf("guard tabKey = %q, want %q (first tab, unaffected by rejected second)", got, firstTabKey)
	}
	if keyExists(t, ctx, conn, secondTabKey) {
		t.Fatalf("rejected second OpenTab must not have minted a tab: %s", secondTabKey)
	}
}

// TestOpenTab_AllowsReopenAfterSettle proves the guard is released (not a
// one-time-forever guard like cafe-ledger's account guard): once the first
// tab is settled, the same lease can open a new one.
func TestOpenTab_AllowsReopenAfterSettle(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabreopen")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNRPNLEASEHJK")
	firstTabKey := openTab(t, ctx, conn, cp, cons, "cdopentabrpn00000001", leaseKey)

	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettlerpn000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + firstTabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{firstTabKey, firstTabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, settleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("guard must be tombstoned once its tab is settled")
	}

	secondTabKey := openTab(t, ctx, conn, cp, cons, "cdopentabrpn00000002", leaseKey)
	if secondTabKey == firstTabKey {
		t.Fatalf("second tab must be a distinct vertex")
	}

	guardDoc := readDoc(t, ctx, conn, leaseKey+".cafeOpenTab")
	guardData, _ := guardDoc["data"].(map[string]any)
	if got, _ := guardData["tabKey"].(string); got != secondTabKey {
		t.Fatalf("guard tabKey = %q, want %q (revived for the second tab)", got, secondTabKey)
	}
}

// TestOpenTab_ConsumerSelfScope_Allowed proves a real resident, holding only
// the consumer scope=self grant, can open a house tab for THEIR OWN lease:
// payload.leaseAppKey names a lease identified-by their own identity and
// authContext.target matches it.
func TestOpenTab_ConsumerSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabselfok")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNSLFQKLEASEH", domainConsumerID)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNSLFQKLEASEH.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfopentab0000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-service OpenTab outcome = %v, want Accepted", outcome)
	}
}

// TestOpenTab_ConsumerSelfScope_RejectedForOthersLease proves the Starlark
// guard closes the gap step 3 leaves open: step 3's scope=self only checks
// authContext.target == actor, never looks at payload.leaseAppKey. A
// consumer satisfying that check but naming a lease identified-by a
// DIFFERENT identity must be rejected — self-service never lets one
// resident open a tab against another's lease.
func TestOpenTab_ConsumerSelfScope_RejectedForOthersLease(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabselfother")

	seedIdentity(t, ctx, conn, domainConsumerID)
	otherApplicantID := "BBCAFEDMQTHERAPPHJKM"
	seedIdentity(t, ctx, conn, otherApplicantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNSLFQTHLEASE", otherApplicantID)
	// The consumer declares the applicationFor link for THEIR OWN identity —
	// which does not exist for this lease (it belongs to otherApplicantID) —
	// so the declared read simply comes back absent, failing closed.
	wrongApplicationForLnk := "lnk.leaseapp.BBCAFEDMNSLFQTHLEASE.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfopentab0000002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", wrongApplicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-service OpenTab for another's lease outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestOpenTab_ConsumerSelfScope_TombstonedApplicationForDenied pins the
// tombstone-blind self-guard: the applicationFor link that once bound this
// resident to the lease is soft-deleted (isDeleted:true), so kv.Read returns
// the tombstone DOCUMENT rather than None. A `== None`-only probe reads a
// moved-out resident's stale link as present and lets them open a tab; the
// guard must treat a tombstone as absent and deny — the same distinction F4's
// worksAt guard draws.
func TestOpenTab_ConsumerSelfScope_TombstonedApplicationForDenied(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabselftomb")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNSLFQTMLEASE", domainConsumerID)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNSLFQTMLEASE.applicationFor.identity." + domainConsumerID
	// The bond existed and was unwired: soft-delete it in place.
	tombstoneLink(t, ctx, conn, applicationForLnk, leaseKey, domainConsumerKey, "applicationFor", "applicationFor")

	reqID := testutil.GenReqID("cdselfopentab0000003")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-service OpenTab with a tombstoned applicationFor outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestSettle_ConsumerSelfScope_Allowed proves a real resident can settle
// THEIR OWN open tab: the tab's leaseAppKey resolves (via applicationFor) to
// the caller's own authContext.target identity.
func TestSettle_ConsumerSelfScope_Allowed(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "settleselfok")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNSTLQKLEASEH", domainConsumerID)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfsettlesetup0001", leaseKey)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNSTLQKLEASEH.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfsettletab000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-service Settle outcome = %v, want Accepted", outcome)
	}
}

// TestSettle_ConsumerSelfScope_RejectedForOthersTab proves a consumer
// satisfying step 3 (authContext.target == actor) but naming a tab whose
// lease is NOT their own is rejected — self-service never lets one resident
// settle another's tab.
func TestSettle_ConsumerSelfScope_RejectedForOthersTab(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "settleselfother")

	seedIdentity(t, ctx, conn, domainConsumerID)
	otherApplicantID := "BBCAFEDMQTHERTABHJKM"
	seedIdentity(t, ctx, conn, otherApplicantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNSTLQTHLEASE", otherApplicantID)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfsettleoth0000001", leaseKey)
	wrongApplicationForLnk := "lnk.leaseapp.BBCAFEDMNSTLQTHLEASE.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfsettletab000002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{wrongApplicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-service Settle of another's tab outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// createMenuItem submits CreateMenuItem{name, priceCents} expecting
// acceptance and returns the new item's key.
func createMenuItem(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, name string, priceCents int) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:00:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"name":"` + name + `","priceCents":` + strconv.Itoa(priceCents) + `}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.menuitem." + nanoIDFromRequestID(reqID)
}

func TestCreateMenuItem_MintsItemAndPriceAspect(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createmenuitem")

	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdcreatemenuitem0001", "Latte", 450)

	itemDoc := readDoc(t, ctx, conn, itemKey)
	if d, _ := itemDoc["data"].(map[string]any); len(d) != 0 {
		t.Fatalf("menuItem root data must stay minimal ({}) after CreateMenuItem, got %v", d)
	}
	priceDoc := readDoc(t, ctx, conn, itemKey+".price")
	priceData, _ := priceDoc["data"].(map[string]any)
	if got, _ := priceData["name"].(string); got != "Latte" {
		t.Fatalf("price.name = %q, want Latte", got)
	}
	if got, _ := priceData["priceCents"].(float64); got != 450 {
		t.Fatalf("price.priceCents = %v, want 450", got)
	}
}

func TestCreateMenuItem_RejectsNonPositivePrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createmenuitembad")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdcreatemenuitembad1"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:00:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"name":"Free Sample","priceCents":0}`),
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

func TestRetireMenuItem_Tombstones(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "retiremenuitem")

	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdretiremenuitemsu01", "Croissant", 350)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdretiremenuitem0001"),
		Lane:          processor.LaneDefault,
		OperationType: "RetireMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:05:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{itemKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if keyExists(t, ctx, conn, itemKey) {
		t.Fatalf("RetireMenuItem must tombstone the item: %s", itemKey)
	}
}

// TestCharge_SelfOrder_DerivesAmountFromMenuItem proves a resident's
// self-service Charge binds against the menuItem catalog: amountCents comes
// from the referenced item's own .price.priceCents (450), never from any
// caller-supplied amountCents (the payload carries none here).
func TestCharge_SelfOrder_DerivesAmountFromMenuItem(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeselfok")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNCHGQKLEASEH", domainConsumerID)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfchargesetup001", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdselfchargemenu0001", "Latte", 450)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNCHGQKLEASEH.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfchargetab000001")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-18T12:10:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
			OptionalReads: []string{applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("self-order Charge outcome = %v, want Accepted", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 450 {
		t.Fatalf("status.totalCents = %v, want 450 (derived from the menu item's price)", got)
	}
}

// TestCharge_SelfOrder_RejectedForOthersTab proves a consumer satisfying
// step 3 (authContext.target == actor) but naming a tab whose lease is NOT
// their own is rejected — self-order never lets one resident charge
// another's tab.
func TestCharge_SelfOrder_RejectedForOthersTab(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeselfother")

	seedIdentity(t, ctx, conn, domainConsumerID)
	otherApplicantID := "BBCAFEDMQTHERCHGHJKM"
	seedIdentity(t, ctx, conn, otherApplicantID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNCHGQTHLEASE", otherApplicantID)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfchargeoth00001", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdselfchargeothmenu1", "Latte", 450)
	wrongApplicationForLnk := "lnk.leaseapp.BBCAFEDMNCHGQTHLEASE.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfchargetab000002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-18T12:10:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
			OptionalReads: []string{wrongApplicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-order Charge of another's tab outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestCharge_SelfOrder_UnknownMenuItemRejected proves a self-service Charge
// naming an absent menuItemKey is rejected, not silently zero-priced.
func TestCharge_SelfOrder_UnknownMenuItemRejected(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeselfunknownitem")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNCHGUNKLEASE", domainConsumerID)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfchargeunksetup1", leaseKey)
	absentItemKey := "vtx.menuitem.BBABSENTMENUITEMHJKM"
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNCHGUNKLEASE.applicationFor.identity." + domainConsumerID

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdselfchargeunk00001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-18T12:10:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + absentItemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status", absentItemKey, absentItemKey + ".price"},
			OptionalReads: []string{applicationForLnk},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-order Charge against an unknown menu item outcome = %v, want Rejected", outcome)
	}
}

// substituteDispatch mirrors the descriptor client's template substitution
// (cmd/facet/web/app.js substituteTemplate) so the tests below can build an
// envelope from the SHIPPED OpMetas() declarations rather than a hand-written
// read list. That is the whole point: a hand-written list proves the script
// works, but only the declarations prove a descriptor-driven client can reach
// it — the gap that made café OpenTab un-drivable from Facet.
func substituteDispatch(tmpl, actorKey string, payload map[string]string) string {
	return regexp.MustCompile(`\{([^}]+)\}`).ReplaceAllStringFunc(tmpl, func(m string) string {
		expr := m[1 : len(m)-1]
		bareID := false
		if strings.HasSuffix(expr, ":id") {
			bareID, expr = true, strings.TrimSuffix(expr, ":id")
		}
		var v string
		switch {
		case expr == "actor":
			v = actorKey
		case strings.HasPrefix(expr, "payload."):
			v = payload[strings.TrimPrefix(expr, "payload.")]
		case strings.HasPrefix(expr, "me."):
			v = payload["me."+strings.TrimPrefix(expr, "me.")]
		}
		if bareID {
			if parts := strings.Split(v, "."); len(parts) >= 3 {
				return parts[2]
			}
			return ""
		}
		return v
	})
}

// dispatchFor returns the shipped op-meta dispatch spec for an operationType.
func dispatchFor(t *testing.T, opType string) *pkgmgr.OpDispatchSpec {
	t.Helper()
	for _, m := range cafedomain.OpMetas() {
		if m.OperationType == opType {
			if m.Dispatch == nil {
				t.Fatalf("%s op-meta declares no dispatch spec", opType)
			}
			return m.Dispatch
		}
	}
	t.Fatalf("no op-meta declared for %s", opType)
	return nil
}

// TestDescriptorDrivenSelfService_OpenSettleReopen is the end-to-end proof that
// cafe-domain's op-metas declare ENOUGH for a descriptor-driven client to run
// the whole self-service tab cycle — open, settle, and open again — with no
// hand-written read list anywhere. Every ContextHint key below is substituted
// from the shipped Dispatch.Reads/Dispatch.OptionalReads templates.
//
// The reopen leg is the one that actually needed the optional half: Settle
// tombstones the lease's .cafeOpenTab guard in place, so the second OpenTab
// finds it PRESENT-but-dead and must OCC-revive it. A client that could not
// declare that key would leave the guard unhydrated, drop the script to its
// create-only branch, and collide with the live tombstone.
func TestDescriptorDrivenSelfService_OpenSettleReopen(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "descriptorcycle")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseID := "BBCAFEDMNDSCRPTRLESE"
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, leaseID, domainConsumerID)

	openDispatch := dispatchFor(t, "OpenTab")
	settleDispatch := dispatchFor(t, "Settle")

	// openOnce builds an OpenTab envelope purely from the declared templates.
	openOnce := func(label string) string {
		// contextParams first — {me.leaseapp} is what fills leaseAppKey, so the
		// visitor is never asked for it (and dispatch.reads then resolves
		// {payload.leaseAppKey} against it).
		vars := map[string]string{"me.leaseapp": leaseKey}
		payload := map[string]string{}
		for field, tmpl := range openDispatch.ContextParams {
			payload[field] = substituteDispatch(tmpl, domainConsumerKey, vars)
		}
		if payload["leaseAppKey"] != leaseKey {
			t.Fatalf("contextParams filled leaseAppKey = %q, want %q", payload["leaseAppKey"], leaseKey)
		}
		for k, v := range payload {
			vars[k] = v
		}

		var reads, optional []string
		for _, r := range openDispatch.Reads {
			reads = append(reads, substituteDispatch(r, domainConsumerKey, vars))
		}
		for _, r := range openDispatch.OptionalReads {
			optional = append(optional, substituteDispatch(r, domainConsumerKey, vars))
		}
		// The declarations must cover both halves of the script's needs.
		wantGuard := leaseKey + ".cafeOpenTab"
		wantLink := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + domainConsumerID
		if !slices.Contains(optional, wantGuard) {
			t.Fatalf("OpenTab optionalReads %v must declare the per-lease guard %q", optional, wantGuard)
		}
		if !slices.Contains(optional, wantLink) {
			t.Fatalf("OpenTab optionalReads %v must declare the ownership link %q", optional, wantLink)
		}

		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		reqID := testutil.GenReqID(label)
		testutil.PublishOp(t, conn, &processor.OperationEnvelope{
			RequestID:     reqID,
			Lane:          processor.LaneDefault,
			OperationType: "OpenTab",
			Actor:         domainConsumerKey,
			SubmittedAt:   "2026-07-07T12:00:00Z",
			Class:         openDispatch.Class,
			Payload:       body,
			ContextHint:   &processor.ContextHint{Reads: reads, OptionalReads: optional},
			AuthContext:   &processor.AuthContext{Target: domainConsumerKey},
		})
		if outcome := testutil.DriveOne(t, ctx, cp, cons, ""); outcome != processor.OutcomeAccepted {
			t.Fatalf("descriptor-driven OpenTab (%s) outcome = %v, want Accepted", label, outcome)
		}
		return "vtx.tab." + nanoIDFromRequestID(reqID)
	}

	firstTab := openOnce("cddesc0penfirst00001")

	// Settle, again built only from Settle's own declarations. targetField is
	// what a client fills from the tab it just opened.
	vars := map[string]string{"me.leaseapp": leaseKey, settleDispatch.TargetField: firstTab}
	var settleReads, settleOptional []string
	for _, r := range settleDispatch.Reads {
		settleReads = append(settleReads, substituteDispatch(r, domainConsumerKey, vars))
	}
	for _, r := range settleDispatch.OptionalReads {
		settleOptional = append(settleOptional, substituteDispatch(r, domainConsumerKey, vars))
	}
	// require_open_status needs the tab's .status aspect — a declaration the
	// targetField fallback alone never produces.
	if !slices.Contains(settleReads, firstTab+".status") {
		t.Fatalf("Settle reads %v must declare the tab's .status aspect", settleReads)
	}
	testutil.PublishOp(t, conn, &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cddescsettle00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         settleDispatch.Class,
		Payload:       json.RawMessage(`{"` + settleDispatch.TargetField + `":"` + firstTab + `"}`),
		ContextHint:   &processor.ContextHint{Reads: settleReads, OptionalReads: settleOptional},
		AuthContext:   &processor.AuthContext{Target: domainConsumerKey},
	})
	if outcome := testutil.DriveOne(t, ctx, cp, cons, ""); outcome != processor.OutcomeAccepted {
		t.Fatalf("descriptor-driven Settle outcome = %v, want Accepted", outcome)
	}

	// The guard is now a live tombstone, not an absent key — the exact state
	// the create-only branch cannot write over.
	if keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("guard must be tombstoned once its tab is settled")
	}

	secondTab := openOnce("cddesc0pensecnd00001")
	if secondTab == firstTab {
		t.Fatalf("reopened tab must be a distinct vertex")
	}
	guardDoc := readDoc(t, ctx, conn, leaseKey+".cafeOpenTab")
	guardData, _ := guardDoc["data"].(map[string]any)
	if got, _ := guardData["tabKey"].(string); got != secondTab {
		t.Fatalf("guard tabKey = %q, want %q (revived for the reopened tab)", got, secondTab)
	}
}
