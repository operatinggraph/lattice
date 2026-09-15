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
			{OperationType: "SetMenuItemAvailability", Scope: "any"},
			{OperationType: "SetMenuItemLocation", Scope: "any"},
			{OperationType: "UpdateMenuItem", Scope: "any"},
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
			OptionalReads: []string{leaseAppKey + ".cafeOpenTab", leaseAppKey + ".decision", leaseAppKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: leaseAppKey, Relation: "heldFor", Direction: "in"},
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
			OptionalReads: []string{leaseKey + ".cafeOpenTab", leaseKey + ".decision", leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// openTabEnv is the OpenTab envelope openTabExpect submits, exposed so a test
// can assert the refusal text rather than only the outcome.
func openTabEnv(label, leaseAppKey, submittedAt string) *processor.OperationEnvelope {
	return &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         domainActorKey,
		SubmittedAt:   submittedAt,
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseAppKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseAppKey},
			OptionalReads: []string{leaseAppKey + ".cafeOpenTab", leaseAppKey + ".decision", leaseAppKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: leaseAppKey, Relation: "heldFor", Direction: "in"},
			},
		},
	}
}

// seedTenancy stamps the lease-signing .tenancy aspect DecideLeaseApplication
// writes on approval — the term OpenTab's TenancyEnded guard reads.
func seedTenancy(t *testing.T, ctx context.Context, conn *substrate.Conn, leaseKey, leaseStart, leaseEnd string) {
	t.Helper()
	seedAspect(t, ctx, conn, leaseKey, "tenancy", "tenancy", map[string]any{
		"leaseStart": leaseStart, "leaseEnd": leaseEnd, "renewalOpensAt": leaseEnd,
	})
}

// TestOpenTab_RejectsEndedTenancy proves a house tab closes to a lease the
// moment its term ends: the rent clause already stops billing at leaseEnd,
// and a moved-out resident must not keep charging the same ledger. The
// boundary is inclusive (a submit AT leaseEnd is refused), the instant before
// it is accepted, and a lease with no .tenancy at all — one approved before
// lease-signing minted terms — still opens, since it has no term to have
// ended.
func TestOpenTab_RejectsEndedTenancy(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "endedtenancy")

	ended := seedLease(t, ctx, conn, "BBCAFEDMNTNCYENDEDHJ")
	seedTenancy(t, ctx, conn, ended, "2025-07-01T00:00:00Z", "2026-07-01T00:00:00Z")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		openTabEnv("cdopentenancyended01", ended, "2026-07-07T12:00:00Z"))
	if outcome != processor.OutcomeRejected {
		t.Fatalf("OpenTab past leaseEnd: outcome = %q, want rejected", outcome)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "TenancyEnded: this lease's tenancy ended on 2026-07-01") {
		t.Fatalf("OpenTab past leaseEnd rejected with %+v, want TenancyEnded naming the end date", reply.Error)
	}

	// The boundary itself: a submit at exactly leaseEnd is past the term.
	atEnd := seedLease(t, ctx, conn, "BBCAFEDMNTNCYATENDHJ")
	seedTenancy(t, ctx, conn, atEnd, "2025-07-07T12:00:00Z", "2026-07-07T12:00:00Z")
	outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		openTabEnv("cdopentenancyatend01", atEnd, "2026-07-07T12:00:00Z"))
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "TenancyEnded") {
		t.Fatalf("OpenTab at leaseEnd: outcome = %q error = %+v, want a TenancyEnded rejection", outcome, reply.Error)
	}

	// The positive vector the guard must not swallow: one second before the
	// term ends the tab opens.
	live := seedLease(t, ctx, conn, "BBCAFEDMNTNCYLYVEHJK")
	seedTenancy(t, ctx, conn, live, "2025-07-07T12:00:00Z", "2026-07-07T12:00:01Z")
	if outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		openTabEnv("cdopentenancylive001", live, "2026-07-07T12:00:00Z")); outcome != processor.OutcomeAccepted {
		t.Fatalf("OpenTab one second before leaseEnd: outcome = %q error = %+v, want accepted", outcome, reply.Error)
	}

	// And the term-less lease (seedLease carries .decision only) — every
	// other fixture in this file, unchanged by the guard.
	if outcome, reply = testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons,
		openTabEnv("cdopentenancynone001", seedLease(t, ctx, conn, "BBCAFEDMNTNCYNQNEHJK"), "2026-07-07T12:00:00Z")); outcome != processor.OutcomeAccepted {
		t.Fatalf("OpenTab with no .tenancy: outcome = %q error = %+v, want accepted", outcome, reply.Error)
	}
}

// seedCafeAccount seeds a cafe-ledger account held for a lease the way
// CreateAccount mints it: the vtx.cafeaccount vertex plus the heldFor link
// (cafeaccount → leaseapp). Returns the account key.
func seedCafeAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, acctID, leaseKey string) string {
	t.Helper()
	acctKey := "vtx.cafeaccount." + acctID
	leaseID := strings.TrimPrefix(leaseKey, "vtx.leaseapp.")
	seedVertex(t, ctx, conn, acctKey, "cafeaccount", map[string]any{})
	seedLink(t, ctx, conn, "lnk.cafeaccount."+acctID+".heldFor.leaseapp."+leaseID,
		acctKey, leaseKey, "heldFor", "heldFor")
	return acctKey
}

// seedRentAccount seeds the loftspace rent ledger's account held for the same
// lease — a second heldFor in-link on the leaseapp whose source is a
// vtx.account, never a café account. Returns the account key.
func seedRentAccount(t *testing.T, ctx context.Context, conn *substrate.Conn, acctID, leaseKey string) string {
	t.Helper()
	acctKey := "vtx.account." + acctID
	leaseID := strings.TrimPrefix(leaseKey, "vtx.leaseapp.")
	seedVertex(t, ctx, conn, acctKey, "account", map[string]any{})
	seedLink(t, ctx, conn, "lnk.account."+acctID+".heldFor.leaseapp."+leaseID,
		acctKey, leaseKey, "heldFor", "heldFor")
	return acctKey
}

// remindedArrears is the .arrears data cafe-ledger's EvaluateCafeArrears
// writes once a reminder has gone out in the current episode — the shape
// OpenTab's credit hold refuses on.
func remindedArrears() map[string]any {
	return map[string]any{
		"dueAt": "2026-06-20T12:00:00Z", "remindedFor": "2026-06-20T12:00:00Z",
		"sentAt": "2026-06-21T03:00:00Z", "evaluatedAt": "2026-07-06T03:00:00Z",
	}
}

// openTabRejectedWith submits OpenTab for the lease and asserts a rejection
// whose message carries want, and that neither the tab the request would
// have minted nor the lease's cafeOpenTab guard was written.
func openTabRejectedWith(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseKey, want string) {
	t.Helper()
	env := openTabEnv(label, leaseKey, "2026-07-07T12:00:00Z")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("%s: outcome = %q error = %+v, want rejected", label, outcome, reply.Error)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, want) {
		t.Fatalf("%s: rejected with %+v, want a message carrying %q", label, reply.Error, want)
	}
	if tabKey := "vtx.tab." + nanoIDFromRequestID(env.RequestID); keyExists(t, ctx, conn, tabKey) {
		t.Fatalf("%s: a refused OpenTab must mint no tab, found %s", label, tabKey)
	}
	if keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("%s: a refused OpenTab must not claim the lease's cafeOpenTab guard", label)
	}
}

// openTabAcceptedFor submits OpenTab for the lease, asserts acceptance, and
// that the tab the request minted and the lease's cafeOpenTab guard both
// exist — the positive mirror of openTabRejectedWith.
func openTabAcceptedFor(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, leaseKey, why string) {
	t.Helper()
	env := openTabEnv(label, leaseKey, "2026-07-07T12:00:00Z")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("OpenTab %s: outcome = %q error = %+v, want accepted", why, outcome, reply.Error)
	}
	if tabKey := "vtx.tab." + nanoIDFromRequestID(env.RequestID); !keyExists(t, ctx, conn, tabKey) {
		t.Fatalf("OpenTab %s: accepted but minted no tab at %s", why, tabKey)
	}
	if !keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("OpenTab %s: accepted but claimed no cafeOpenTab guard on the lease", why)
	}
}

// TestOpenTab_RefusesCreditHold proves a lease whose café account has been
// reminded of a balance it still owes opens no new tab: cafe-ledger's
// .arrears.sentAt is present exactly while a reminder has gone out in the
// current arrears episode, and that — not dueAt alone — is the hold. The
// account is reached by the lease's heldFor in-links filtered to the café
// account, so the rent ledger's vtx.account link beside it neither hides nor
// stands in for it. The positive vectors come first: an overdue-but-unreminded
// episode and an owes-nothing evaluation both open.
func TestOpenTab_RefusesCreditHold(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "credithold")

	// Overdue, not yet reminded: dueAt without sentAt is not a hold.
	overdue := seedLease(t, ctx, conn, "BBCAFEDMNHQLDNQRMNDA")
	overdueAcct := seedCafeAccount(t, ctx, conn, "BBCAFEDMNHQLDNQRMACC", overdue)
	seedAspect(t, ctx, conn, overdueAcct, "arrears", "cafeAccountArrears", map[string]any{
		"dueAt": "2026-06-20T12:00:00Z", "evaluatedAt": "2026-07-06T03:00:00Z",
	})
	openTabAcceptedFor(t, ctx, conn, cp, cons, "cdholdoverdueonly001", overdue, "on an overdue-but-unreminded account")

	// Owes nothing: the episode ended and {evaluatedAt} alone remains.
	square := seedLease(t, ctx, conn, "BBCAFEDMNHQLDSQUAREA")
	squareAcct := seedCafeAccount(t, ctx, conn, "BBCAFEDMNHQLDSQUAREB", square)
	seedAspect(t, ctx, conn, squareAcct, "arrears", "cafeAccountArrears", map[string]any{
		"evaluatedAt": "2026-07-06T03:00:00Z",
	})
	openTabAcceptedFor(t, ctx, conn, cp, cons, "cdholdsquare00000001", square, "on a square account")

	// Reminded and still owing: the hold.
	held := seedLease(t, ctx, conn, "BBCAFEDMNHQLDLEASEHJ")
	heldAcct := seedCafeAccount(t, ctx, conn, "BBCAFEDMNHQLDACCTHJK", held)
	seedAspect(t, ctx, conn, heldAcct, "arrears", "cafeAccountArrears", remindedArrears())
	openTabRejectedWith(t, ctx, conn, cp, cons, "cdholdreminded000001", held,
		"CreditHold: this lease's café account owes a balance a reminder went out for on 2026-06-21")

	// The rent ledger's account beside the café one: the type filter must
	// still find the café account and hold.
	both := seedLease(t, ctx, conn, "BBCAFEDMNHQLDBQTHLSE")
	seedRentAccount(t, ctx, conn, "BBCAFEDMNHQLDBQTHRNT", both)
	bothCafe := seedCafeAccount(t, ctx, conn, "BBCAFEDMNHQLDBQTHCAF", both)
	seedAspect(t, ctx, conn, bothCafe, "arrears", "cafeAccountArrears", remindedArrears())
	openTabRejectedWith(t, ctx, conn, cp, cons, "cdholdbothledgers001", both, "CreditHold")

	// Only the rent ledger's account, no café account at all: nothing to
	// hold on, whatever the rent account's own aspects say.
	rentOnly := seedLease(t, ctx, conn, "BBCAFEDMNHQLDRNTQNLY")
	rentAcct := seedRentAccount(t, ctx, conn, "BBCAFEDMNHQLDRNTQNLA", rentOnly)
	seedAspect(t, ctx, conn, rentAcct, "arrears", "cafeAccountArrears", remindedArrears())
	openTabAcceptedFor(t, ctx, conn, cp, cons, "cdholdrentonly000001", rentOnly, "with only a rent account held for the lease")

	// An .arrears document of the wrong class is a fault, never state to
	// decide a hold on.
	wrong := seedLease(t, ctx, conn, "BBCAFEDMNHQLDWRNGCLS")
	wrongAcct := seedCafeAccount(t, ctx, conn, "BBCAFEDMNHQLDWRNGACC", wrong)
	seedAspect(t, ctx, conn, wrongAcct, "arrears", "somethingElse", map[string]any{"evaluatedAt": "2026-07-06T03:00:00Z"})
	openTabRejectedWith(t, ctx, conn, cp, cons, "cdholdwrongclass0001", wrong, "InvalidState")
}

// TestOpenTab_RefusesCreditHold_SelfLeg proves the hold binds the resident's
// own self-service OpenTab exactly as it binds the desk's: the debt is the
// lease's, and the applicant satisfying the applicationFor probe changes
// nothing about it.
func TestOpenTab_RefusesCreditHold_SelfLeg(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "creditholdself")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNHQLDSELFLSE", domainConsumerID)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNHQLDSELFLSE.applicationFor.identity." + domainConsumerID
	acctKey := seedCafeAccount(t, ctx, conn, "BBCAFEDMNHQLDSELFACC", leaseKey)
	seedAspect(t, ctx, conn, acctKey, "arrears", "cafeAccountArrears", remindedArrears())

	reqID := testutil.GenReqID("cdholdself0000000001")
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
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision", leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
			},
		},
		AuthContext: &processor.AuthContext{Target: domainConsumerKey},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected || reply.Error == nil || !strings.Contains(reply.Error.Message, "CreditHold") {
		t.Fatalf("self-service OpenTab on a held account: outcome = %q error = %+v, want a CreditHold rejection", outcome, reply.Error)
	}
	if keyExists(t, ctx, conn, "vtx.tab."+nanoIDFromRequestID(reqID)) || keyExists(t, ctx, conn, leaseKey+".cafeOpenTab") {
		t.Fatalf("a held self-service OpenTab must mint no tab and claim no guard")
	}
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

// TestVoidCharge_RejectsAmountOnly proves the amount-only form is retired:
// VoidCharge without lineId is rejected outright, and the tab's .status
// (totalCents, lines, itemsMemo) is left exactly as it was.
func TestVoidCharge_RejectsAmountOnly(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidamountonly")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVAQLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvao00000001", leaseKey)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdchargevao0000000001"),
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

	beforeDoc := readDoc(t, ctx, conn, tabKey+".status")
	beforeData, _ := beforeDoc["data"].(map[string]any)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvoidvao0000000001"),
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
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)

	afterDoc := readDoc(t, ctx, conn, tabKey+".status")
	afterData, _ := afterDoc["data"].(map[string]any)
	if got, want := afterData["totalCents"].(float64), beforeData["totalCents"].(float64); got != want {
		t.Fatalf("status.totalCents after rejected amount-only void = %v, want unchanged %v", got, want)
	}
	if got, want := afterData["itemsMemo"].(string), beforeData["itemsMemo"].(string); got != want {
		t.Fatalf("status.itemsMemo after rejected amount-only void = %q, want unchanged %q", got, want)
	}
	beforeLines, _ := beforeData["lines"].([]any)
	afterLines, _ := afterData["lines"].([]any)
	if len(afterLines) != len(beforeLines) {
		t.Fatalf("status.lines has %d entries after rejected amount-only void, want unchanged %d", len(afterLines), len(beforeLines))
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
	if got, want := statusData["itemsMemo"].(string), "Off-menu charge"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (line-1 voided drops out of the join, line-2 remains)", got, want)
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
		// The workplace confinement walk runs before the line lookup, so a
		// refused unknown line still declares it.
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, voidEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestVoidCharge_ByLineId_RejectsDoubleVoid proves a line voids once: the
// second VoidCharge naming an already-voided line is refused
// UnknownChargeLine (void_line_by_id matches live lines only), and the tab's
// totalCents and lines are exactly what the first void left.
func TestVoidCharge_ByLineId_RejectsDoubleVoid(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidlinetwice")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVLTWLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvltw0000001", leaseKey)

	charge := func(label string, amountCents int) {
		t.Helper()
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
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
	charge("cdvltwcharge00000001", 450)
	charge("cdvltwcharge00000002", 350)

	void := func(label string, outcome processor.MessageOutcome) {
		t.Helper()
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(label),
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
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, outcome)
	}
	void("cdvltwvoid0000000001", processor.OutcomeAccepted)
	after := readDoc(t, ctx, conn, tabKey+".status")
	afterData, _ := after["data"].(map[string]any)
	if got, _ := afterData["totalCents"].(float64); got != 350 {
		t.Fatalf("totalCents after the first void = %v, want 350", got)
	}
	afterLines, _ := json.Marshal(afterData["lines"])

	void("cdvltwvoid0000000002", processor.OutcomeRejected)
	again := readDoc(t, ctx, conn, tabKey+".status")
	againData, _ := again["data"].(map[string]any)
	if got, _ := againData["totalCents"].(float64); got != 350 {
		t.Fatalf("totalCents after the refused second void = %v, want 350 unchanged", got)
	}
	if againLines, _ := json.Marshal(againData["lines"]); string(againLines) != string(afterLines) {
		t.Fatalf("lines changed across a refused second void:\n before %s\n after  %s", afterLines, againLines)
	}
}

// TestChargeVoidSettleItemsMemo_ProjectsLiveNonVoidedLines proves the Unit A
// fix end to end: itemsMemo is a projection of .status.lines at every write,
// not an append-only accumulator, so a voided line drops out of the frozen
// settlement memo instead of staying on it with a trailing "Void correction"
// line appended after it.
func TestChargeVoidSettleItemsMemo_ProjectsLiveNonVoidedLines(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargevoidsettlememo")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNCVSMLEASEHJ")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabcvsm0000001", leaseKey)

	charge := func(reqLabel, description string) {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(reqLabel),
			Lane:          processor.LaneDefault,
			OperationType: "Charge",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-22T12:05:00Z",
			Class:         "tab",
			Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":400,"description":"` + description + `"}`),
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
	charge("cdcvsmchargeone00001", "Croissant")
	charge("cdcvsmchargetwo00001", "Latte")
	charge("cdcvsmchargethre0001", "Latte")

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdcvsmvoidline000001"),
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

	settleEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdcvsmsettle0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
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
	if got, want := statusData["itemsMemo"].(string), "Latte, Latte"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (Croissant voided drops out of the join, no trailing Void correction)", got, want)
	}
}

// TestVoidCharge_ByLineId_TotalEqualsLiveLines proves the core invariant a
// lineId-only VoidCharge is meant to hold: on a tab charged for a menu item
// and an off-menu item, voiding one by lineId leaves totalCents equal to the
// sum of the non-voided lines' own amountCents, and itemsMemo naming only
// the surviving line.
func TestVoidCharge_ByLineId_TotalEqualsLiveLines(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidlinetotal")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVLTLEASEHJK")
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNVLTUNTPHJKM")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvlt00000001", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdvltmenu0000000001", "Latte", 450, unitKey)

	menuChargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvltchargeone000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, menuChargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	offMenuChargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvltchargetwo000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:06:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":300,"description":"Late fee"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, offMenuChargeEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	voidEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdvltvoidline000001"),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:07:00Z",
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
	lines, _ := statusData["lines"].([]any)
	var liveTotal float64
	for _, l := range lines {
		line, _ := l.(map[string]any)
		if voided, _ := line["voided"].(bool); !voided {
			amt, _ := line["amountCents"].(float64)
			liveTotal += amt
		}
	}
	if got, want := statusData["totalCents"].(float64), liveTotal; got != want {
		t.Fatalf("status.totalCents = %v, want %v (== the sum of non-voided lines' amountCents)", got, want)
	}
	if got, want := statusData["totalCents"].(float64), float64(300); got != want {
		t.Fatalf("status.totalCents = %v, want %v (450+300 minus the voided 450 line)", got, want)
	}
	if got, want := statusData["itemsMemo"].(string), "Late fee"; got != want {
		t.Fatalf("status.itemsMemo = %q, want %q (only the live line named, the voided Latte dropped out)", got, want)
	}
}

// TestVoidCharge_RejectsAfterSettle proves a settled tab's total is frozen —
// once dispatched to the ledger, it cannot be corrected via VoidCharge.
func TestVoidCharge_RejectsAfterSettle(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "voidaftersettle")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNVASLEASEHJK")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdopentabvas00000001", leaseKey)

	chargeEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdchargevas0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":500}`),
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
		RequestID:     testutil.GenReqID("cdsettlevas000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-22T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
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
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-1"}`),
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
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision", leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
			},
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
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-1"}`),
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
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
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
		t.Fatalf("status.itemsMemo = %q, want %q (projected from the live lines and frozen, same as totalCents)", got, want)
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
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
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
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
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
			Reads: []string{tabKey, tabKey + ".status"},
		},
	}
	testutil.PublishOp(t, conn, settleTwice)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestSettle_NoDeclaredChargedToRead_StillBackfillsIdempotently proves
// Settle's chargedTo confirmation no longer depends on a caller-declared
// OptionalRead keyed by the caller's own lease. A staff Settle has no
// `{me.leaseapp}` of its own to declare that key by (the caller settles a
// RESIDENT's tab, not their own), so a real staff dispatch's ContextHint
// carries no chargedTo OptionalRead at all — omitting it here reproduces
// that shape. Before the live kv.Links confirmation this shipped with,
// the declared-state check saw the already-live chargedTo link (written
// unconditionally by OpenTab) as absent and tried to recreate it, failing
// every retry with RevisionConflict.
func TestSettle_NoDeclaredChargedToRead_StillBackfillsIdempotently(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "staffsettlenodeclare")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSTFSETLEASE")
	tabKey := openTab(t, ctx, conn, cp, cons, "cdstaffsettab0000001", leaseKey)

	settle := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdstaffsettle0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Settle",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-07T13:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, settle)
	outcome := testutil.DriveOne(t, ctx, cp, cons, "")
	if outcome != processor.OutcomeAccepted {
		t.Fatalf("staff Settle with no declared chargedTo optionalRead outcome = %v, want Accepted", outcome)
	}

	statusDoc := readDoc(t, ctx, conn, tabKey+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, want := statusData["value"].(string), "settled"; got != want {
		t.Fatalf("status.value = %q, want %q", got, want)
	}
	chargedToDoc := readDoc(t, ctx, conn, chargedToOptionalRead(tabKey, leaseKey))
	if isDeleted, _ := chargedToDoc["isDeleted"].(bool); isDeleted {
		t.Fatalf("chargedTo link is tombstoned after Settle, want alive")
	}
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
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
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
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
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
			Reads: []string{firstTabKey, firstTabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: firstTabKey, Relation: "chargedTo", Direction: "out"},
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
			OptionalReads: []string{leaseKey + ".cafeOpenTab", applicationForLnk, leaseKey + ".decision", leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
			},
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
			Enumerations: []processor.EnumerationHint{
				{Hub: tabKey, Relation: "chargedTo", Direction: "out"},
			},
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

// TestSetMenuItemLocation_MovesServedAtLink proves SetMenuItemLocation tombstones
// the item's CURRENT servedAt link and creates the new one, and that the op's
// response names the NEW link key as primaryKey rather than the item vertex
// (ddls.go's SetMenuItemLocation branch: this op never mutates the item vertex
// or an aspect rooted on it, only its servedAt link — so the Processor's
// write-footprint reply constraint would reject Accepted here if primaryKey
// named anything else). There is no direct accessor for the response payload
// in this harness, so Accepted is itself the proof (the objects-base
// TestObject_ReattachAlreadyAlive_IsAcceptedNoOp precedent).
func TestSetMenuItemLocation_MovesServedAtLink(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "setmenuitemloc")

	locA := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJF")
	locB := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJG")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdsetmenuitemlocsu01", "Croissant", 350, locA)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdsetmenuitemloc0001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetMenuItemLocation",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:05:30Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","newLocation":"` + locB + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, locB},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	itemID := strings.TrimPrefix(itemKey, "vtx.menuitem.")
	oldLnk := "lnk.menuitem." + itemID + ".servedAt.unit." + strings.TrimPrefix(locA, "vtx.unit.")
	newLnk := "lnk.menuitem." + itemID + ".servedAt.unit." + strings.TrimPrefix(locB, "vtx.unit.")

	oldDoc := readDoc(t, ctx, conn, oldLnk)
	if del, _ := oldDoc["isDeleted"].(bool); !del {
		t.Fatalf("old servedAt link must be tombstoned: %s", oldLnk)
	}
	if !keyExists(t, ctx, conn, newLnk) {
		t.Fatalf("new servedAt link must exist live: %s", newLnk)
	}
	newDoc := readDoc(t, ctx, conn, newLnk)
	if got, _ := newDoc["sourceVertex"].(string); got != itemKey {
		t.Fatalf("new servedAt sourceVertex = %q, want the item %q", got, itemKey)
	}
	if got, _ := newDoc["targetVertex"].(string); got != locB {
		t.Fatalf("new servedAt targetVertex = %q, want the new location %q", got, locB)
	}
}

// TestUpdateMenuItem_RewritesNameAndPrice proves UpdateMenuItem rewrites the
// item's .price aspect in one OCC'd upsert — a rename and a reprice are the
// same act on the same aspect, so both fields land in a single call.
func TestUpdateMenuItem_RewritesNameAndPrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "updatemenuitem")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJC")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdupdatemenuitemsu01", "Croissant", 350, locKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdupdatemenuitem0001"),
		Lane:          processor.LaneDefault,
		OperationType: "UpdateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:06:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","name":"Almond croissant","priceCents":425}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	priceDoc := readDoc(t, ctx, conn, itemKey+".price")
	priceData, _ := priceDoc["data"].(map[string]any)
	if got, _ := priceData["name"].(string); got != "Almond croissant" {
		t.Fatalf("price.name = %q, want Almond croissant", got)
	}
	if got, _ := priceData["priceCents"].(float64); got != 425 {
		t.Fatalf("price.priceCents = %v, want 425", got)
	}
}

// TestUpdateMenuItem_RejectsNonPositivePrice supplies a LIVE item so the only
// thing wrong with the submission is the price — otherwise the rejection
// would prove nothing about the price check (mirrors
// TestCreateMenuItem_RejectsNonPositivePrice's own posture).
func TestUpdateMenuItem_RejectsNonPositivePrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "updatemenuitembad")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNMENULCTNHJD")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdupdatemenuitembd01", "Latte", 450, locKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdupdatemenuitembad1"),
		Lane:          processor.LaneDefault,
		OperationType: "UpdateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:07:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","name":"Latte","priceCents":0}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
}

// TestUpdateMenuItem_UnknownItemRejected covers an absent menuItemKey — the
// declared .price read never hydrates, so the script's vertex_alive check
// fails closed with UnknownMenuItem rather than reading it live.
func TestUpdateMenuItem_UnknownItemRejected(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "updatemenuitemunk")

	itemKey := "vtx.menuitem.BBCAFEDMNMENUGHSTHJC"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdupdatemenuitemunk1"),
		Lane:          processor.LaneDefault,
		OperationType: "UpdateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-18T12:08:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","name":"Latte","priceCents":450}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeRejected)
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
		var enumerations []processor.EnumerationHint
		for _, e := range openDispatch.Enumerations {
			enumerations = append(enumerations, processor.EnumerationHint{
				Hub: substituteDispatch(e.Hub, domainConsumerKey, vars), Relation: e.Relation, Direction: e.Direction,
			})
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
		// And the credit-hold walk: the lease's heldFor in-links, resolved
		// from the payload the descriptor itself filled.
		wantWalk := processor.EnumerationHint{Hub: leaseKey, Relation: "heldFor", Direction: "in"}
		if !slices.Contains(enumerations, wantWalk) {
			t.Fatalf("OpenTab enumerations %v must declare the credit-hold walk %+v", enumerations, wantWalk)
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
			ContextHint:   &processor.ContextHint{Reads: reads, OptionalReads: optional, Enumerations: enumerations},
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
	var settleEnumerations []processor.EnumerationHint
	for _, e := range settleDispatch.Enumerations {
		settleEnumerations = append(settleEnumerations, processor.EnumerationHint{
			Hub: substituteDispatch(e.Hub, domainConsumerKey, vars), Relation: e.Relation, Direction: e.Direction,
		})
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
		ContextHint:   &processor.ContextHint{Reads: settleReads, OptionalReads: settleOptional, Enumerations: settleEnumerations},
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

func submitSetMenuItemAvailability(t *testing.T, ctx context.Context, conn *substrate.Conn, cp *processor.CommitPath, cons jetstream.Consumer, label, itemKey string, available bool) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetMenuItemAvailability",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-30T12:20:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","available":` + strconv.FormatBool(available) + `}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestSetMenuItemAvailability_FlipsAndPreservesPrice proves the toggle
// rewrites only available: name and priceCents ride through unchanged, off
// then on.
func TestSetMenuItemAvailability_FlipsAndPreservesPrice(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "setavailflip")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNAVLFLPTNLCH")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdavailflipmenu00001", "Latte", 450, locKey)

	if outcome := submitSetMenuItemAvailability(t, ctx, conn, cp, cons, "cdavailflipoff000001", itemKey, false); outcome != processor.OutcomeAccepted {
		t.Fatalf("SetMenuItemAvailability(false) outcome = %v, want Accepted", outcome)
	}
	priceDoc := readDoc(t, ctx, conn, itemKey+".price")
	priceData, _ := priceDoc["data"].(map[string]any)
	if got, ok := priceData["available"].(bool); !ok || got != false {
		t.Fatalf("price.available = %v, want false", priceData["available"])
	}
	if got, _ := priceData["name"].(string); got != "Latte" {
		t.Fatalf("price.name = %q, want Latte (unchanged by the toggle)", got)
	}
	if got, _ := priceData["priceCents"].(float64); got != 450 {
		t.Fatalf("price.priceCents = %v, want 450 (unchanged by the toggle)", got)
	}

	if outcome := submitSetMenuItemAvailability(t, ctx, conn, cp, cons, "cdavailflipon0000001", itemKey, true); outcome != processor.OutcomeAccepted {
		t.Fatalf("SetMenuItemAvailability(true) outcome = %v, want Accepted", outcome)
	}
	priceDoc = readDoc(t, ctx, conn, itemKey+".price")
	priceData, _ = priceDoc["data"].(map[string]any)
	if got, ok := priceData["available"].(bool); !ok || got != true {
		t.Fatalf("price.available = %v, want true", priceData["available"])
	}
}

// TestSetMenuItemAvailability_RefusesNonBool proves the require_bool guard:
// a non-boolean available is rejected rather than coerced.
func TestSetMenuItemAvailability_RefusesNonBool(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "setavailbadtype")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNAVLBADTLCTN")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdavailbadtmenu00001", "Latte", 450, locKey)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdavailbadt0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "SetMenuItemAvailability",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-30T12:21:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","available":"nope"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("SetMenuItemAvailability with a string available: outcome = %v, want Rejected", outcome)
	}
	if reply == nil || reply.Error == nil || !strings.Contains(reply.Error.Message, "InvalidArgument: available: required boolean") {
		t.Fatalf("reply = %+v, want the require_bool refusal naming the field", reply)
	}
}

// TestUpdateMenuItem_PreservesAvailability proves the carry-through:
// a reprice of a sold-out item leaves it sold out — UpdateMenuItem rewrites
// the SAME .price aspect SetMenuItemAvailability does, so a naive rewrite
// would silently clear the flag.
func TestUpdateMenuItem_PreservesAvailability(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "updatemenupreserve")

	locKey := seedLocation(t, ctx, conn, "BBCAFEDMNUPDPRSVLCTN")
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdupdprsvmenu000001", "Latte", 450, locKey)
	if outcome := submitSetMenuItemAvailability(t, ctx, conn, cp, cons, "cdupdprsvoff0000001", itemKey, false); outcome != processor.OutcomeAccepted {
		t.Fatalf("SetMenuItemAvailability(false) outcome = %v, want Accepted", outcome)
	}

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("cdupdprsvreprice0001"),
		Lane:          processor.LaneDefault,
		OperationType: "UpdateMenuItem",
		Actor:         domainActorKey,
		SubmittedAt:   "2026-07-30T12:22:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","name":"Latte","priceCents":475}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)

	priceDoc := readDoc(t, ctx, conn, itemKey+".price")
	priceData, _ := priceDoc["data"].(map[string]any)
	if got, ok := priceData["available"].(bool); !ok || got != false {
		t.Fatalf("price.available = %v, want false (a reprice must not clear the sold-out flag)", priceData["available"])
	}
	if got, _ := priceData["priceCents"].(float64); got != 475 {
		t.Fatalf("price.priceCents = %v, want 475", got)
	}
}

// TestCharge_RefusesItemUnavailable_SelfOrder proves a self-order Charge
// naming a sold-out item is refused ItemUnavailable, and accepted again once
// the item is put back on the menu — both the negative and the positive
// vector, so the guard is proven to actually gate rather than always deny.
func TestCharge_RefusesItemUnavailable_SelfOrder(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "chargeselfunavail")

	seedIdentity(t, ctx, conn, domainConsumerID)
	leaseKey := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEDMNCHGUAVLEASE", domainConsumerID)
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNCHGUAVUNPTH")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdselfunavlsetup0001", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdselfunavlmenu00001", "Latte", 450, unitKey)
	applicationForLnk := "lnk.leaseapp.BBCAFEDMNCHGUAVLEASE.applicationFor.identity." + domainConsumerID

	if outcome := submitSetMenuItemAvailability(t, ctx, conn, cp, cons, "cdselfunavloff000001", itemKey, false); outcome != processor.OutcomeAccepted {
		t.Fatalf("SetMenuItemAvailability(false) outcome = %v, want Accepted", outcome)
	}

	chargeEnv := func(reqID string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(reqID),
			Lane:          processor.LaneDefault,
			OperationType: "Charge",
			Actor:         domainConsumerKey,
			SubmittedAt:   "2026-07-30T12:23:00Z",
			Class:         "tab",
			Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
			ContextHint: &processor.ContextHint{
				Reads:         []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
				OptionalReads: []string{applicationForLnk},
			},
			AuthContext: &processor.AuthContext{Target: domainConsumerKey},
		}
	}

	env := chargeEnv("cdselfunavlchg000001")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("self-order Charge against a sold-out item outcome = %v, want Rejected", outcome)
	}
	if reply == nil || reply.Error == nil || !strings.Contains(reply.Error.Message, "ItemUnavailable") {
		t.Fatalf("reply = %+v, want an ItemUnavailable rejection", reply)
	}

	if outcome := submitSetMenuItemAvailability(t, ctx, conn, cp, cons, "cdselfunavlon0000001", itemKey, true); outcome != processor.OutcomeAccepted {
		t.Fatalf("SetMenuItemAvailability(true) outcome = %v, want Accepted", outcome)
	}
	env2 := chargeEnv("cdselfunavlchg000002")
	testutil.PublishOp(t, conn, env2)
	if outcome := testutil.DriveOne(t, ctx, cp, cons, ""); outcome != processor.OutcomeAccepted {
		t.Fatalf("self-order Charge against a re-available item outcome = %v, want Accepted", outcome)
	}
}

// TestCharge_RefusesItemUnavailable_StaffCatalogPick mirrors
// TestCharge_RefusesItemUnavailable_SelfOrder on the staff POS leg: a sold-out
// item is sold out whoever rings it up.
func TestCharge_RefusesItemUnavailable_StaffCatalogPick(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "chargestaffunavail")

	leaseKey := seedLease(t, ctx, conn, "BBCAFEDMNSTFUAVLEASE")
	unitKey := seedLocation(t, ctx, conn, "BBCAFEDMNSTFUAVUNPTH")
	seedAppliesToUnit(t, ctx, conn, leaseKey, unitKey)
	tabKey := openTab(t, ctx, conn, cp, cons, "cdstaffunavlsetup01", leaseKey)
	itemKey := createMenuItem(t, ctx, conn, cp, cons, "cdstaffunavlmenu001", "Latte", 450, unitKey)

	if outcome := submitSetMenuItemAvailability(t, ctx, conn, cp, cons, "cdstaffunavloff00001", itemKey, false); outcome != processor.OutcomeAccepted {
		t.Fatalf("SetMenuItemAvailability(false) outcome = %v, want Accepted", outcome)
	}

	chargeEnv := func(reqID string) *processor.OperationEnvelope {
		return &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(reqID),
			Lane:          processor.LaneDefault,
			OperationType: "Charge",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-30T12:24:00Z",
			Class:         "tab",
			Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","menuItemKey":"` + itemKey + `"}`),
			ContextHint: &processor.ContextHint{
				Reads: []string{tabKey, tabKey + ".status", itemKey, itemKey + ".price"},
				Enumerations: []processor.EnumerationHint{
					{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				},
			},
		}
	}

	env := chargeEnv("cdstaffunavlchg00001")
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	if outcome != processor.OutcomeRejected {
		t.Fatalf("staff Charge against a sold-out item outcome = %v, want Rejected", outcome)
	}
	if reply == nil || reply.Error == nil || !strings.Contains(reply.Error.Message, "ItemUnavailable") {
		t.Fatalf("reply = %+v, want an ItemUnavailable rejection", reply)
	}

	if outcome := submitSetMenuItemAvailability(t, ctx, conn, cp, cons, "cdstaffunavlon00001", itemKey, true); outcome != processor.OutcomeAccepted {
		t.Fatalf("SetMenuItemAvailability(true) outcome = %v, want Accepted", outcome)
	}
	env2 := chargeEnv("cdstaffunavlchg00002")
	testutil.PublishOp(t, conn, env2)
	if outcome := testutil.DriveOne(t, ctx, cp, cons, ""); outcome != processor.OutcomeAccepted {
		t.Fatalf("staff Charge against a re-available item outcome = %v, want Accepted", outcome)
	}
}
