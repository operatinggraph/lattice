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
			{OperationType: "OpenTab", Scope: "any"},
			{OperationType: "Charge", Scope: "any"},
			{OperationType: "VoidCharge", Scope: "any"},
			{OperationType: "Settle", Scope: "any"},
			{OperationType: "SettleStaleTab", Scope: "any"},
			{OperationType: "BackfillTabStaleAt", Scope: "any"},
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
	inst := testutil.NewInstaller(conn, bootstrap.BootstrapIdentityKey)
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

// seedLease seeds a leaseapp already carrying an approved lease-signing
// .decision aspect — OpenTab now rejects LeaseNotApproved otherwise, and
// every fixture here stands in for an ordinary resident already cleared to
// open a tab. seedUnapprovedLease is the one negative fixture that omits it.
func seedLease(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.leaseapp." + id
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	seedAspect(t, ctx, conn, key, "decision", "decision", map[string]any{"value": "approved", "decidedAt": "2026-07-01T12:00:00Z"})
	return key
}

// seedUnapprovedLease seeds a leaseapp with NO lease-signing .decision aspect
// — the ordinary not-yet-decided state OpenTab must reject.
func seedUnapprovedLease(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
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

// seedLocation seeds a location the way location-domain actually mints one:
// the key TYPE segment is the location level (unit|building|property) and the
// CLASS is that same key type (location-domain/ddls.go, CreateLocation).
// Seeding a `vtx.location.<id>` would test a key shape production never
// produces — and would hide that servedAt's link key carries the level, not
// the class.
func seedLocation(t *testing.T, ctx context.Context, conn *substrate.Conn, id string) string {
	t.Helper()
	key := "vtx.unit." + id
	seedVertex(t, ctx, conn, key, "unit", map[string]any{})
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

// seedAspect writes an aspect document directly, the shape ddls.go's
// make_aspect builds — used to construct a vertex's state in one write
// rather than through the op that would normally produce it (a legacy
// pre-existing shape, e.g., a tab predating a since-added link write).
func seedAspect(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexKey, localName, class string, data map[string]any) {
	t.Helper()
	doc := map[string]any{
		"class": class, "isDeleted": false,
		"vertexKey": vertexKey, "localName": localName, "data": data,
	}
	b, _ := json.Marshal(doc)
	key := vertexKey + "." + localName
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, key, b); err != nil {
		t.Fatalf("seed aspect %s: %v", key, err)
	}
}

// chargedToOptionalRead returns Settle's class-(d) dedup read for its own
// chargedTo backfill (ddls.go): every Settle submission must declare whether
// the tab already carries the link, or the script cannot tell "declared
// absent" from "undeclared" and a tab that already has one fails CreateOnly
// when Settle tries to write it again.
func chargedToOptionalRead(tabKey, leaseKey string) string {
	tabID := strings.TrimPrefix(tabKey, "vtx.tab.")
	leaseID := strings.TrimPrefix(leaseKey, "vtx.leaseapp.")
	return "lnk.tab." + tabID + ".chargedTo.leaseapp." + leaseID
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

// seedLeaseWithApplicant seeds a leaseapp vertex (already lease-signing
// .decision-approved, see seedLease) + its applicationFor link to
// applicantID — the residency check OpenTab/Settle's self-scope guard reads
// (mirrors wellness-domain's seedLease(..., applicantID, ...)).
func seedLeaseWithApplicant(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseID, applicantID string) string {
	t.Helper()
	key := "vtx.leaseapp." + leaseID
	seedVertex(t, ctx, conn, key, "leaseapp", map[string]any{})
	seedAspect(t, ctx, conn, key, "decision", "decision", map[string]any{"value": "approved", "decidedAt": "2026-07-01T12:00:00Z"})
	lnk := "lnk.leaseapp." + leaseID + ".applicationFor.identity." + applicantID
	seedLink(t, ctx, conn, lnk, key, "vtx.identity."+applicantID, "applicationFor", "applicationFor")
	return key
}

// seedAppliesToUnit wires a leaseapp's appliesToUnit link to a unit location
// the way lease-signing actually mints one — leaseapp_unit (ddls.go) resolves
// a tab's own building from this link, never from a payload field, so a
// self-order Charge's locality bound has nothing to check without it.
func seedAppliesToUnit(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseKey, unitKey string) {
	t.Helper()
	leaseID := strings.TrimPrefix(leaseKey, "vtx.leaseapp.")
	unitID := strings.TrimPrefix(unitKey, "vtx.unit.")
	seedLink(t, ctx, conn, "lnk.leaseapp."+leaseID+".appliesToUnit.unit."+unitID,
		leaseKey, unitKey, "appliesToUnit", "appliesToUnit")
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
			OptionalReads: []string{leaseAppKey + ".cafeOpenTab", leaseAppKey + ".decision"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
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
	if got, _ := statusData["itemsMemo"].(string); got != "" {
		t.Fatalf("status.itemsMemo = %q, want empty on a fresh tab", got)
	}
	if got, _ := statusData["leaseAppKey"].(string); got != leaseKey {
		t.Fatalf("status.leaseAppKey = %q, want %q", got, leaseKey)
	}

	for _, rel := range []string{"chargedTo", "openFor"} {
		lnk := "lnk.tab." + tabID + "." + rel + ".leaseapp." + leaseID
		if !keyExists(t, ctx, conn, lnk) {
			t.Fatalf("%s link must exist after OpenTab: %s", rel, lnk)
		}
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

// TestOpenTab_RejectsUnapprovedLease proves a lease with no landlord
// decision yet — the ordinary state before DecideLeaseApplication runs —
// cannot open a house tab: live, an unapproved lease posted a real charge
// before this guard existed (verticals.md).
func TestOpenTab_RejectsUnapprovedLease(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "unapprovedlease")

	leaseKey := seedUnapprovedLease(t, ctx, conn, "BBCAFEDMNUNAPPRVDLHJ")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdopenunapproved0001"),
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", leaseKey + ".decision"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
			ContextHint: &processor.ContextHint{
				Reads: []string{tabKey, tabKey + ".status"},
				Enumerations: []processor.EnumerationHint{
					{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				},
			},
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
	if got, want := statusData["itemsMemo"].(string), "Off-menu charge, Off-menu charge"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (default off-menu line per Charge, comma-joined)", got, want)
	}
	lines, _ := statusData["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("status.lines has %d entries, want 2 (one per Charge)", len(lines))
	}
	first, _ := lines[0].(map[string]any)
	if got, want := first["id"].(string), "line-1"; got != want {
		t.Fatalf("lines[0].id = %q, want %q", got, want)
	}
	if got, want := first["description"].(string), "Off-menu charge"; got != want {
		t.Fatalf("lines[0].description = %q, want %q", got, want)
	}
	if got, want := first["amountCents"].(float64), float64(450); got != want {
		t.Fatalf("lines[0].amountCents = %v, want %v", got, want)
	}
	if got := first["voided"].(bool); got {
		t.Fatalf("lines[0].voided = %v, want false (never voided)", got)
	}
	if got, want := first["orderedBy"].(string), domainActorKey; got != want {
		t.Fatalf("lines[0].orderedBy = %q, want %q (the Charge's own op.actor)", got, want)
	}
	second, _ := lines[1].(map[string]any)
	if got, want := second["id"].(string), "line-2"; got != want {
		t.Fatalf("lines[1].id = %q, want %q", got, want)
	}
	if got, want := second["amountCents"].(float64), float64(300); got != want {
		t.Fatalf("lines[1].amountCents = %v, want %v", got, want)
	}
	if got, want := second["orderedBy"].(string), domainActorKey; got != want {
		t.Fatalf("lines[1].orderedBy = %q, want %q (the Charge's own op.actor)", got, want)
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
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
	if got, want := statusData["itemsMemo"].(string), "Off-menu charge, Void correction"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (a real void appends a correction line)", got, want)
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
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 0 {
		t.Fatalf("status.totalCents = %v, want 0 (clamped, not negative)", got)
	}
	if got, want := statusData["itemsMemo"].(string), "Off-menu charge, Void correction"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (a clamped void still actually reduced the total)", got, want)
	}
}

// TestVoidCharge_ByLineId_DerivesAmountAndMarksVoided proves the itemized
// void path: the caller names only lineId (never amountCents), the void
// amount is derived from the line itself (the same "derive, don't trust"
// posture Charge's own menuItemKey branch uses), and the target line is
// marked voided:true in place rather than removed — a second, un-targeted
// line is untouched.
func TestVoidCharge_ByLineId_DerivesAmountAndMarksVoided(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidline")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVLNDLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvln00000001", leaseKey)

	charge := func(reqLabel string, amountCents int) {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(reqLabel),
			Lane:          processor.LaneDefault,
			OperationType: "Charge",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-22T12:05:00Z",
			Class:         "tab",
			Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":` + strconv.Itoa(amountCents) + `}`),
			ContextHint: &processor.ContextHint{
				Reads: []string{tabKey, tabKey + ".status"},
				Enumerations: []processor.EnumerationHint{
					{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				},
			},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	}
	charge("cdvlnchargeone000001", 450)
	charge("cdvlnchargetwo000001", 300)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidlineone0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:06:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-1"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, want := statusData["totalCents"].(float64), float64(300); got != want {
		t.Fatalf("status.totalCents = %v, want %v (750-450, amount derived from line-1, not caller-supplied)", got, want)
	}
	if got, want := statusData["itemsMemo"].(string), "Off-menu charge, Off-menu charge, Void correction"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q", got, want)
	}
	lines, _ := statusData["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("status.lines has %d entries, want 2 (voided in place, not removed)", len(lines))
	}
	voided, _ := lines[0].(map[string]any)
	if got := voided["voided"].(bool); !got {
		t.Fatalf("lines[0].voided = %v, want true", got)
	}
	if got, want := voided["orderedBy"].(string), domainActorKey; got != want {
		t.Fatalf("lines[0].orderedBy = %q, want %q (VoidCharge rewrites voided:true in place but must not drop who ordered it)", got, want)
	}
	untouched, _ := lines[1].(map[string]any)
	if got := untouched["voided"].(bool); got {
		t.Fatalf("lines[1].voided = %v, want false (only line-1 was targeted)", got)
	}
	if got, want := untouched["orderedBy"].(string), domainActorKey; got != want {
		t.Fatalf("lines[1].orderedBy = %q, want %q", got, want)
	}
}

// TestVoidCharge_ByLineId_RejectsUnknownLine proves a lineId naming no live
// entry on the tab (never charged, or already voided) is rejected rather
// than silently voiding nothing or falling back to the legacy amount path.
func TestVoidCharge_ByLineId_RejectsUnknownLine(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidlineunknown")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVLUKLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvlu00000001", leaseKey)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvlucharge0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":450}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidlineunk0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:06:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-9"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestVoidCharge_LegacyAmountOnly_LeavesLinesUntouched proves the
// no-lineId form (a correction predating itemized lines, or an off-menu
// adjustment with no line to reference) still works exactly as before and
// never writes to .status.lines.
func TestVoidCharge_LegacyAmountOnly_LeavesLinesUntouched(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidlegacy")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVLGYLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvlg00000001", leaseKey)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvlgcharge0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":850}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidlegacy00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:06:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":350}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, want := statusData["totalCents"].(float64), float64(500); got != want {
		t.Fatalf("status.totalCents = %v, want %v (850-350)", got, want)
	}
	lines, _ := statusData["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("status.lines has %d entries, want 1 (the original Charge only — legacy void touches no line)", len(lines))
	}
	line, _ := lines[0].(map[string]any)
	if got := line["voided"].(bool); got {
		t.Fatalf("lines[0].voided = %v, want false (a legacy amount-only void never marks a line)", got)
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{chargedToOptionalRead(tabKey, leaseKey)},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision"},
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
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{chargedToOptionalRead(tabKey, leaseKey)},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
	if got, want := statusData["itemsMemo"].(string), "Off-menu charge"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (carried over frozen from the Charge, same as totalCents)", got, want)
	}
	if _, ok := statusData["settledAt"]; !ok {
		t.Fatalf("status.settledAt must be stamped on settle")
	}

	// The two lease links part ways here. `openFor` is retracted, which is what
	// drops the tab out of every resident's edgeEntityTabs read grant (that walk
	// traverses this hop and cannot see the .status aspect). `chargedTo` must
	// survive: cafeTabSettlement anchors on it, and the posting it drives is
	// owed only NOW that the tab is settled — retracting both would delete the
	// convergence row and silently leave the house tab unposted.
	tabID := tabKey[len("vtx.tab."):]
	leaseID := leaseKey[len("vtx.leaseapp."):]
	if keyExists(t, ctx, conn, "lnk.tab."+tabID+".openFor.leaseapp."+leaseID) {
		t.Fatalf("Settle must tombstone the openFor link")
	}
	if !keyExists(t, ctx, conn, "lnk.tab."+tabID+".chargedTo.leaseapp."+leaseID) {
		t.Fatalf("Settle must LEAVE chargedTo alive — cafeTabSettlement anchors on it")
	}
}

// TestSettle_BackfillsChargedToWhenMissing proves Settle heals a tab that
// predates the chargedTo write entirely — seeded directly rather than
// through OpenTab, the exact shape a historical write-path gap leaves
// behind: an open tab with only the transient openFor hop wired. Without the
// backfill, such a tab has no lens row to find its tabKey through
// (cafeTabSettlement's required chargedTo match) and its lease's
// .cafeOpenTab guard is claimed forever — permanently unsettleable, and the
// next OpenTab for that lease rejects OpenTabAlreadyExists with no way out.
func TestSettle_BackfillsChargedToWhenMissing(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "backfillchargedto")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNBKFLLEASEHJ")
	tabKey := "vtx.tab.BBCAFEDMNBKFLTABHJKM"
	tabID := tabKey[len("vtx.tab."):]
	leaseID := leaseKey[len("vtx.leaseapp."):]

	seedVertex(t, ctx, conn, tabKey, "tab", map[string]any{})
	seedAspect(t, ctx, conn, tabKey, "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 650.0, "itemsMemo": "", "openedAt": "2026-07-20T10:00:00Z", "leaseAppKey": leaseKey,
	})
	seedLink(t, ctx, conn, "lnk.tab."+tabID+".openFor.leaseapp."+leaseID, tabKey, leaseKey, "openFor", "openFor")
	seedAspect(t, ctx, conn, leaseKey, "cafeOpenTab", "cafeOpenTabGuard", map[string]any{"tabKey": tabKey})

	chargedToKey := "lnk.tab." + tabID + ".chargedTo.leaseapp." + leaseID
	if keyExists(t, ctx, conn, chargedToKey) {
		t.Fatalf("test setup: chargedTo must start absent to model the historical gap")
	}

	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettlebkfl00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-31T09:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{chargedToKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, settleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	if !keyExists(t, ctx, conn, chargedToKey) {
		t.Fatalf("Settle must backfill the missing chargedTo link")
	}
	if keyExists(t, ctx, conn, "lnk.tab."+tabID+".openFor.leaseapp."+leaseID) {
		t.Fatalf("Settle must still tombstone openFor")
	}
	if keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("Settle must still release the lease's open-tab guard, unblocking the next OpenTab")
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{chargedToOptionalRead(tabKey, leaseKey)},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{chargedToOptionalRead(tabKey, leaseKey)},
		},
	}
	testutil.PublishOp(t, conn, settleTwice)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestOpenTab_WritesStaleAtTwentyFourHoursAhead pins the auto-settle
// deadline OpenTab derives (ddls.go's time.rfc3339_add(openedAt, "24h")) —
// cafeStaleTabSettlement (lenses.go) arms its one-shot @at against exactly
// this value.
func TestOpenTab_WritesStaleAtTwentyFourHoursAhead(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "opentabstaleat")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSTLLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabstale000001", leaseKey)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, want := statusData["openedAt"].(string), "2026-07-07T12:00:00Z"; got != want {
		t.Fatalf("status.openedAt = %q, want %q", got, want)
	}
	if got, want := statusData["staleAt"].(string), "2026-07-08T12:00:00Z"; got != want {
		t.Fatalf("status.staleAt = %q, want %q (openedAt + 24h)", got, want)
	}
}

// TestSettleStaleTab_ClosesTabAndBackfillsChargedTo mirrors
// TestSettle_ClosesTabFreezesTotal / TestSettle_BackfillsChargedToWhenMissing
// but through the auto-settle op — proving SettleStaleTab needs no declared
// OptionalReads for chargedTo (unlike Settle) because it confirms the link
// via a bounded LIVE kv.Links read instead (ddls.go), the mechanical reason
// this op exists as a dedicated operationType rather than a directOp against
// Settle itself (a Weaver GapActionSpec's Reads cannot template a link key).
func TestSettleStaleTab_ClosesTabAndBackfillsChargedTo(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "staletabsettle")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNAUTLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabauto0000001", leaseKey)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdchargeauto00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":900}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, chargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	staleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettlestale0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "SettleStaleTab",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T12:00:01Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, staleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["value"].(string); got != "settled" {
		t.Fatalf("status.value = %q, want settled", got)
	}
	if got, _ := statusData["totalCents"].(float64); got != 900 {
		t.Fatalf("status.totalCents = %v, want 900 (frozen)", got)
	}

	tabID := tabKey[len("vtx.tab."):]
	leaseID := leaseKey[len("vtx.leaseapp."):]
	if keyExists(t, ctx, conn, "lnk.tab."+tabID+".openFor.leaseapp."+leaseID) {
		t.Fatalf("SettleStaleTab must tombstone openFor, same as Settle")
	}
	if !keyExists(t, ctx, conn, "lnk.tab."+tabID+".chargedTo.leaseapp."+leaseID) {
		t.Fatalf("SettleStaleTab must LEAVE chargedTo alive — cafeTabSettlement anchors on it")
	}
	if keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("SettleStaleTab must release the lease's open-tab guard, unblocking the next OpenTab")
	}
}

// TestSettleStaleTab_NoOpsIfAlreadySettled proves the race guard: a staff
// Settle that beat the Weaver dispatch must not make SettleStaleTab reject
// (which would burn a retry-budget slot for nothing) — it no-ops cleanly,
// mirroring clinic-domain's MarkPastDueNoShow defensive re-check.
func TestSettleStaleTab_NoOpsIfAlreadySettled(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "staletabraced")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNRACLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabrace0000001", leaseKey)

	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettleraced0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T11:59:59Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{chargedToOptionalRead(tabKey, leaseKey)},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, settleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	staleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsettlestalerc00001"),
		Lane:          processor.LaneDefault,
		OperationType: "SettleStaleTab",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-08T12:00:01Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, staleEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["value"].(string); got != "settled" {
		t.Fatalf("status.value = %q, want settled", got)
	}
	if got, want := statusData["settledAt"].(string), "2026-07-08T11:59:59Z"; got != want {
		t.Fatalf("status.settledAt = %q, want %q (the staff Settle's own timestamp, untouched by the no-op)", got, want)
	}
}

// TestBackfillTabStaleAt_ComputesFromOpenedAt covers the real defect: a tab
// opened before staleAt shipped (af451062) carries a .status with no staleAt
// key at all — seeded directly here, since OpenTab itself always writes one
// now. cafeStaleTabSettlement's missing_staleat gap (lenses.go) is what
// dispatches this in production; the op itself just needs to compute the
// SAME openedAt + 24h OpenTab would have written and backfill it.
func TestBackfillTabStaleAt_ComputesFromOpenedAt(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "backfillstaleat")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNBKFLEASEHJK")
	tabKey := "vtx.tab.CDLEGACYTABKHJMNPQRS"
	seedVertex(t, ctx, conn, tabKey, "tab", map[string]any{})
	seedAspect(t, ctx, conn, tabKey, "status", "tabStatus", map[string]any{
		"value": "open", "totalCents": 500.0, "itemsMemo": "", "lines": []any{},
		"openedAt": "2026-07-07T12:00:00Z", "leaseAppKey": leaseKey,
	})

	backfillEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdbackfillstale00001"),
		Lane:          processor.LaneDefault,
		OperationType: "BackfillTabStaleAt",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-08-05T00:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, backfillEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["value"].(string); got != "open" {
		t.Fatalf("status.value = %q, want open (backfill must not close the tab)", got)
	}
	if got, want := statusData["staleAt"].(string), "2026-07-08T12:00:00Z"; got != want {
		t.Fatalf("status.staleAt = %q, want %q (openedAt + 24h)", got, want)
	}
	if got, want := statusData["totalCents"].(float64), 500.0; got != want {
		t.Fatalf("status.totalCents = %v, want %v (carried forward unchanged)", got, want)
	}
}

// TestBackfillTabStaleAt_NoOpsIfAlreadyPresent proves the idempotency guard:
// a redelivery, or a race with a second dispatch, must not clobber a
// staleAt the tab already carries (which could silently push the deadline
// forward if it just overwrote unconditionally).
func TestBackfillTabStaleAt_NoOpsIfAlreadyPresent(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "backfillstalenoop")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNRDYLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabnoop0000001", leaseKey)

	backfillEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdbackfillnoop00001"),
		Lane:          processor.LaneDefault,
		OperationType: "BackfillTabStaleAt",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-08-05T00:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{tabKey, tabKey + ".status"}},
	}
	testutil.PublishOp(t, conn, backfillEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, want := statusData["staleAt"].(string), "2026-07-08T12:00:00Z"; got != want {
		t.Fatalf("status.staleAt = %q, want %q (OpenTab's own value, untouched by the no-op)", got, want)
	}
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{tabKey, tabKey + ".status"},
			OptionalReads: []string{chargedToOptionalRead(tabKey, leaseKey)},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
		ContextHint: &processor.ContextHint{
			Reads:         []string{firstTabKey, firstTabKey + ".status"},
			OptionalReads: []string{chargedToOptionalRead(firstTabKey, leaseKey)},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision"},
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
			OptionalReads: []string{applicationForLnk, chargedToOptionalRead(tabKey, leaseKey)},
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
			OptionalReads: []string{wrongApplicationForLnk, chargedToOptionalRead(tabKey, leaseKey)},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-service Settle of another's tab outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// createMenuItem submits CreateMenuItem{name, priceCents, locationKey}
// expecting acceptance and returns the new item's key. locationKey is a
// declared read (Contract #2 §2.5) — the script's liveness check reads the
// hydrated location document, so an undeclared key would fail closed.
func createMenuItem(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, name string, priceCents int, locationKey string) string {
	t.Helper()
	reqID := testutil.GenReqID(label)
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:00:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"name":"` + name + `","priceCents":` + strconv.Itoa(priceCents) + `,"locationKey":"` + locationKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{locationKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	return "vtx.menuitem." + nanoIDFromRequestID(reqID)
}

func TestCreateMenuItem_MintsItemAndPriceAspect(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createmenuitem")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJA")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdcreatemenuitem0001", "Latte", 450, locKey)

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

	// The servedAt link is the item's only reachability — without it no walk
	// can offer the item to anyone, so its absence is a silent feature loss
	// rather than a visible failure.
	servedAtLnk := "lnk.menuitem." + strings.TrimPrefix(itemKey, "vtx.menuitem.") +
		".servedAt.unit." + strings.TrimPrefix(locKey, "vtx.unit.")
	if !keyExists(t, ctx, conn, servedAtLnk) {
		t.Fatalf("servedAt link must exist: %s", servedAtLnk)
	}
	lnkDoc := readDoc(t, ctx, conn, servedAtLnk)
	if got, _ := lnkDoc["sourceVertex"].(string); got != itemKey {
		t.Fatalf("servedAt sourceVertex = %q, want the item %q (Contract #1 §1.1: the later-arriving vertex is the source)", got, itemKey)
	}
	if got, _ := lnkDoc["targetVertex"].(string); got != locKey {
		t.Fatalf("servedAt targetVertex = %q, want the location %q", got, locKey)
	}
}

// TestCreateMenuItem_RejectsNonPositivePrice supplies a LIVE location so the
// only thing wrong with the submission is the price — otherwise the rejection
// would prove nothing about the price check.
func TestCreateMenuItem_RejectsNonPositivePrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "createmenuitembad")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJE")
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdcreatemenuitembad1"),
		Lane:          processor.LaneDefault,
		OperationType: "CreateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:00:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"name":"Free Sample","priceCents":0,"locationKey":"` + locKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{locKey}},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestCreateMenuItem_RejectsUnservedLocation covers the two ways the anchor can
// be wrong: a key naming nothing live, and a key naming a live vertex of the
// wrong class. Both must fail closed — an item minted against neither is an
// item no browse walk can reach, which is exactly the state this field exists
// to make impossible.
func TestCreateMenuItem_RejectsUnservedLocation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		label string
		seed  func(t *testing.T, ctx context.Context, conn *substrate.Conn) string
	}{
		{
			name:  "absent location",
			label: "cdcreatemenuitemnoloc",
			seed: func(t *testing.T, ctx context.Context, conn *substrate.Conn) string {
				return "vtx.unit.BBCAFEDMNMENUGHSTHJA"
			},
		},
		{
			name:  "live vertex of the wrong class",
			label: "cdcreatemenuitemwrongc",
			seed: func(t *testing.T, ctx context.Context, conn *substrate.Conn) string {
				return seedLease(t, ctx, conn, "BBCAFEDMNMENUWRNGCLHJ")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, conn := setupDomainEnv(t)
			cp, cons := newDomainPipeline(t, ctx, conn, "createmenuitem"+tc.label)

			locKey := tc.seed(t, ctx, conn)
			env := &processor.OperationEnvelope{
				RequestID:     testutil.GenReqID(tc.label[:20]),
				Lane:          processor.LaneDefault,
				OperationType: "CreateMenuItem",
				Actor:         domainActorKey,
				SubmittedAt:   "2026-07-18T12:00:00Z",
				Class:         "menuitem",
				Payload:       json.RawMessage(`{"name":"Latte","priceCents":450,"locationKey":"` + locKey + `"}`),
				ContextHint:   &processor.ContextHint{Reads: []string{locKey}},
			}
			testutil.PublishOp(t, conn, env)
			testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
		})
	}
}

func TestRetireMenuItem_Tombstones(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "retiremenuitem")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJB")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdretiremenuitemsu01", "Croissant", 350, locKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdretiremenuitem0001"),
		Lane:          processor.LaneDefault,
		OperationType: "RetireMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:05:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
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
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNCHGQKUNPTHJ")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfchargesetup001", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdselfchargemenu0001", "Latte", 450, unitKey)
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
	if got, want := statusData["itemsMemo"].(string), "Latte"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (the menu item's own name)", got, want)
	}
	lines, _ := statusData["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("status.lines has %d entries, want 1", len(lines))
	}
	line, _ := lines[0].(map[string]any)
	if got, want := line["orderedBy"].(string), domainConsumerKey; got != want {
		t.Fatalf("lines[0].orderedBy = %q, want %q (the RESIDENT's own identity on a self-order, not staff)", got, want)
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
	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJD")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdselfchargeothmenu1", "Latte", 450, locKey)
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
	absentItemKey := "vtx.menuitem.BBABSENTMENUiTEMHJKM"
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

// TestCharge_SelfOrder_RejectedForMenuItemAtAnotherBuilding proves a consumer
// satisfying every existing check (own tab, own applicationFor) is still
// rejected when the referenced menu item is served at a location unrelated to
// the tab's own building — the write confinement servedAt never had before.
func TestCharge_SelfOrder_RejectedForMenuItemAtAnotherBuilding(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeselfotherbldg")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNCHGXBLEASEH", domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNCHGXBUNPTHJ")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfchargeoth10001", leaseKey)

	otherLocKey := seedLocation(t, ctx, conn, "BBCAFEDMNCHGXBBLDGHJ")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdselfchargeothmenu2", "Latte", 450, otherLocKey)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNCHGXBLEASEH.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfchargeoth10002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-27T12:10:00Z",
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
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-order Charge for a menu item at another building outcome = %v, want Rejected (AuthDenied)", outcome)
	}
}

// TestCharge_SelfOrder_AcceptsMenuItemServedAtCoveringBuilding proves the
// locality bound is ancestor-inclusive, not exact-match-only: a menu item
// served at the BUILDING that contains the tab's own unit (a building-level
// café, not one scoped per-unit) is still chargeable — mirrors
// worksAt_covers' "wired to any containing building matches everything
// containedIn it".
func TestCharge_SelfOrder_AcceptsMenuItemServedAtCoveringBuilding(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeselfcovering")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNCHGCVLEASEH", domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNCHGCVUNPTHJ")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	buildingKey := "vtx.building.BBCAFEDMNCHGCVBLDGHJ"
	seedVertex(t, ctx, conn, buildingKey, "building", map[string]any{})
	seedLink(t, ctx, conn, "lnk.unit.BBCAFEDMNCHGCVUNPTHJ.containedIn.building.BBCAFEDMNCHGCVBLDGHJ",
		unitKey, buildingKey, "containedIn", "containedIn")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfchargecov10001", leaseKey)

	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdselfchargecovmenu2", "Latte", 450, buildingKey)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNCHGCVLEASEH.applicationFor.identity." + domainConsumerID

	reqID := testutil.GenReqID("cdselfchargecov10002")
	env := &processor.OperationEnvelope{
		RequestID:     reqID,
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainConsumerKey,
		SubmittedAt:   "2026-07-27T12:10:00Z",
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
		t.Fatalf("self-order Charge for a menu item served at a covering building outcome = %v, want Accepted", outcome)
	}
}

// TestCharge_Staff_CatalogItemDerivesAmount proves a staff (non-self) Charge
// can also bind against the menuItem catalog when the caller supplies
// menuItemKey — the same "derive, don't trust" amount source the self-order
// path already used, now available to a staff POS Charge too.
func TestCharge_Staff_CatalogItemDerivesAmount(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargestaffcatalog")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSTFCATLEASE")
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNSTFCATUNPTH")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdstaffcatsetup00001", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdstaffcatmenu000001", "Latte", 450, unitKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdstaffcattab0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-30T12:10:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("staff Charge with menuItemKey outcome = %v, want Accepted", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 450 {
		t.Fatalf("status.totalCents = %v, want 450 (derived from the menu item's price)", got)
	}
	if got, want := statusData["itemsMemo"].(string), "Latte"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (the menu item's own name)", got, want)
	}
	lines, _ := statusData["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("status.lines has %d entries, want 1", len(lines))
	}
	line, _ := lines[0].(map[string]any)
	if got, want := line["orderedBy"].(string), domainActorKey; got != want {
		t.Fatalf("lines[0].orderedBy = %q, want %q (the STAFFER's own identity on a POS ring-up, not the resident)", got, want)
	}
}

// TestCharge_Staff_HandKeyedAmountStillAccepted pins that a staff Charge with
// no menuItemKey still hand-keys amountCents — the off-menu path (a charge
// the catalog does not cover) is not removed by adding the catalog binding.
func TestCharge_Staff_HandKeyedAmountStillAccepted(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargestaffoffmenu")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSTFHKMLEASE")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdstaffoffsetup00001", leaseKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdstaffofftab0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-30T12:10:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":999,"description":"Lost key fob"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("staff off-menu Charge outcome = %v, want Accepted", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 999 {
		t.Fatalf("status.totalCents = %v, want 999 (hand-keyed off-menu amount)", got)
	}
	if got, want := statusData["itemsMemo"].(string), "Lost key fob"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (the caller-supplied off-menu description)", got, want)
	}
}

// TestCharge_Staff_RejectedForMenuItemAtAnotherBuilding proves the staff
// catalog-charge path is location-bound the same way self-order already is:
// a menu item served at a building unrelated to the tab's own is rejected
// even though nothing else about the call is wrong.
func TestCharge_Staff_RejectedForMenuItemAtAnotherBuilding(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargestaffotherbldg")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSTFXBLEASEH")
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNSTFXBUNPTHJ")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdstaffxbsetup000001", leaseKey)

	otherLocKey := seedLocation(t, ctx, conn, "BBCAFEDMNSTFXBBLDGHJ")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdstaffxbmenu0000001", "Latte", 450, otherLocKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdstaffxbtab00000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-30T12:10:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeRejected {
		t.Fatalf("staff Charge for a menu item at another building outcome = %v, want Rejected (AuthDenied)", outcome)
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
