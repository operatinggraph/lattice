package cafedomain_test

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// Workplace write confinement — facet-staff-worlds-design.md §3.5 / F4.
//
// A staff actor holds its vertical grants at scope=any, exactly as `operator`
// does; nothing in the capability plane distinguishes the two, because scope is
// only `any` or `self` (Contract #6) and a standing grant sets no authContext.
// Confinement is therefore enforced in the op script: a caller that cannot
// prove it is root may write only inside the location it worksAt.
//
// The topology every vector below builds:
//
//	vtx.building.<A>                      vtx.building.<B>
//	      ^ containedIn                         ^ containedIn
//	vtx.unit.<A>                          vtx.unit.<B>
//	      ^ appliesToUnit                       ^ appliesToUnit
//	vtx.leaseapp.<A>                      vtx.leaseapp.<B>
//
// The staff identity worksAt building A only.
const (
	wcStaffID  = "BBCAFEWCSTAFFHJKMNPQ"
	wcStaffKey = "vtx.identity." + wcStaffID
	wcStaffCap = "cap.identity." + wcStaffID

	wcBuildingAID = "BBCAFEWCBLDGAHJKMNPQ"
	wcBuildingBID = "BBCAFEWCBLDGBHJKMNPQ"
	wcUnitAID     = "BBCAFEWCUNTAHJKMNPQR"
	wcUnitBID     = "BBCAFEWCUNTBHJKMNPQR"
	wcLeaseAID    = "BBCAFEWCLEASEAHJKMNP"
	wcLeaseBID    = "BBCAFEWCLEASEBHJKMNP"

	wcBuildingAKey = "vtx.building." + wcBuildingAID
	wcBuildingBKey = "vtx.building." + wcBuildingBID
)

// wcStaffCapDoc grants the same scope=any tab surface the operator cap doc
// grants. That is the point: the capability plane cannot tell staff from root,
// so if confinement holds, it holds entirely inside the script.
func wcStaffCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    wcStaffCap,
		Actor:                  wcStaffKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{wcStaffKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "OpenTab", Scope: "any"},
			{OperationType: "Charge", Scope: "any"},
			{OperationType: "Settle", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

func wcWorksAtLink() string {
	return "lnk.identity." + wcStaffID + ".worksAt.building." + wcBuildingAID
}

// seedWorkplaceTopology builds the two-building world above and returns the two
// lease keys (A, B). The staff identity is wired worksAt building A only, and
// holds NO operator holdsRole link — it cannot prove root.
func seedWorkplaceTopology(t *testing.T, ctx context.Context, conn *substrate.Conn) (string, string) {
	t.Helper()
	seedIdentity(t, ctx, conn, wcStaffID)
	seedVertex(t, ctx, conn, wcBuildingAKey, "building", map[string]any{})
	seedVertex(t, ctx, conn, wcBuildingBKey, "building", map[string]any{})

	mk := func(unitID, leaseID, buildingKey string) string {
		unitKey := "vtx.unit." + unitID
		seedVertex(t, ctx, conn, unitKey, "unit", map[string]any{})
		testutil.SeedLink(t, ctx, conn,
			"lnk.unit."+unitID+".containedIn.building."+buildingKey[len("vtx.building."):],
			"containedIn", unitKey, buildingKey)

		leaseKey := "vtx.leaseapp." + leaseID
		seedVertex(t, ctx, conn, leaseKey, "leaseapp", map[string]any{})
		// Approved (see seedLease's own comment) — these vectors probe
		// workplace confinement, not lease approval.
		seedAspect(t, ctx, conn, leaseKey, "decision", "decision", map[string]any{"value": "approved", "decidedAt": "2026-07-01T12:00:00Z"})
		testutil.SeedLink(t, ctx, conn,
			"lnk.leaseapp."+leaseID+".appliesToUnit.unit."+unitID,
			"appliesToUnit", leaseKey, unitKey)
		return leaseKey
	}
	leaseA := mk(wcUnitAID, wcLeaseAID, wcBuildingAKey)
	leaseB := mk(wcUnitBID, wcLeaseBID, wcBuildingBKey)

	testutil.SeedLink(t, ctx, conn, wcWorksAtLink(), "worksAt", wcStaffKey, wcBuildingAKey)
	return leaseA, leaseB
}

// tombstoneWorksAt soft-deletes the worksAt link the way UnwireWorksAt does —
// the document stays in Core KV with isDeleted:true. This is the case a
// `kv.Read(k) == None` guard silently passes, because a tombstone hydrates as a
// DOCUMENT, not None.
func tombstoneWorksAt(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	doc := map[string]any{
		"class": "worksAt", "isDeleted": true,
		"sourceVertex": wcStaffKey, "targetVertex": wcBuildingAKey,
		"localName": "worksAt", "data": map[string]any{},
	}
	b, _ := json.Marshal(doc)
	if _, err := conn.KVPut(ctx, testutil.HarnessCoreBucket, wcWorksAtLink(), b); err != nil {
		t.Fatalf("tombstone worksAt: %v", err)
	}
}

// submitOpenTabAs submits OpenTab{leaseAppKey} as an arbitrary actor on the
// standing path (no authContext), declaring exactly what a staff caller would.
func submitOpenTabAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, leaseKey, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "OpenTab",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-20T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"leaseAppKey":"` + leaseKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{leaseKey},
			OptionalReads: []string{leaseKey + ".cafeOpenTab", leaseKey + ".decision", leaseKey + ".tenancy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
				{Hub: leaseKey, Relation: "heldFor", Direction: "in"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestWorkplace_OperatorUnconfined proves the guard leaves root alone: the
// operator actor holds no worksAt link at all and still writes at both
// buildings. A worksAt-derived exemption would produce this same result — the
// Unwired vector below is what tells the two designs apart.
func TestWorkplace_OperatorUnconfined(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	cp, cons := newDomainPipeline(t, ctx, conn, "wcoperator")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcopa000000000000001", leaseA, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator OpenTab at building A = %v, want Accepted (root stays unconfined)", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcopb000000000000002", leaseB, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator OpenTab at building B = %v, want Accepted (root stays unconfined)", got)
	}
}

// TestWorkplace_StaffConfinedToWorkplace is the F4 guarantee: one staff actor,
// one scope=any grant, accepted at the building it worksAt and rejected at the
// one it does not.
func TestWorkplace_StaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcstaff")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcsta00000000000001", leaseA, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff OpenTab at its OWN workplace = %v, want Accepted", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcstb00000000000002", leaseB, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff OpenTab at ANOTHER building = %v, want Rejected — this is the multi-org gate", got)
	}
}

// TestWorkplace_SettleWithCounterPaymentStaffConfinedToWorkplace: the
// counter-payment leg of Settle is a STAFF act (a frontOfHouse standing
// grant, never the operator, so actor_holds_operator does not short-circuit
// the walk) and is confined like every other tab write — a staffer records
// cash taken at settle only for a tab at a building they worksAt. Positive
// sibling first: at their OWN building the fields land, stamped with the
// staffer as paidAtSettleBy; at ANOTHER building the same call is refused
// and the tab stays open.
func TestWorkplace_SettleWithCounterPaymentStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcsettlepaid")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	tabA := openTab(t, ctx, conn, cp, cons, "wcstlpaidtaba0000001", leaseA)
	staffCharge(t, ctx, conn, cp, cons, "wcstlpaidchga0000001", tabA, 900, "2026-07-20T12:05:00Z")
	tabB := openTab(t, ctx, conn, cp, cons, "wcstlpaidtabb0000001", leaseB)
	staffCharge(t, ctx, conn, cp, cons, "wcstlpaidchgb0000001", tabB, 900, "2026-07-20T12:05:00Z")

	testutil.PublishOp(t, conn, staffSettleEnv("wcstlpaidsettlea0001", tabA, wcStaffKey,
		`{"tabKey":"`+tabA+`","paidCents":900}`, "2026-07-20T13:00:00Z"))
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("staff Settle{paidCents} at its OWN workplace = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}
	statusA, _ := readDoc(t, ctx, conn, tabA+".status")["data"].(map[string]any)
	if got, _ := statusA["paidAtSettleCents"].(float64); got != 900 {
		t.Fatalf("tabA status.paidAtSettleCents = %v, want 900", statusA["paidAtSettleCents"])
	}
	if got, _ := statusA["paidAtSettleBy"].(string); got != wcStaffKey {
		t.Fatalf("tabA status.paidAtSettleBy = %q, want the frontOfHouse staffer %q", got, wcStaffKey)
	}

	settleRejectedBecause(t, ctx, conn, cp, cons,
		staffSettleEnv("wcstlpaidsettleb0001", tabB, wcStaffKey,
			`{"tabKey":"`+tabB+`","paidCents":900}`, "2026-07-20T13:00:00Z"),
		tabB, "AuthDenied")
	statusB, _ := readDoc(t, ctx, conn, tabB+".status")["data"].(map[string]any)
	if _, has := statusB["paidAtSettleCents"]; has {
		t.Fatalf("tabB carries paidAtSettleCents %v — a denied Settle must write nothing", statusB["paidAtSettleCents"])
	}
}

// wcSubmitVoidCharge submits VoidCharge as an arbitrary actor. forgedTarget,
// when non-empty, becomes authContext.target with no task — the shape any
// scope=any holder can put on the wire, since the Gateway forwards target
// verbatim and step 3 never inspects it on the scope=any path.
func wcSubmitVoidCharge(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, tabKey, actorKey, forgedTarget string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "VoidCharge",
		Actor:         actorKey,
		SubmittedAt:   "2026-07-20T12:30:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","lineId":"line-1"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabKey, tabKey + ".status"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	if forgedTarget != "" {
		env.AuthContext = &processor.AuthContext{Target: forgedTarget}
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestWorkplace_ForgedAuthContextTargetStaysConfined: the exemption that lets a
// self-service resident past confinement must key on a target the PLATFORM
// validated, not on the caller having set one — a scope=any staff member can
// put any target on the wire.
//
// The vector runs on VoidCharge deliberately. OpenTab/Charge/Settle each pair
// the workplace guard with an ownership proof that independently denies a
// caller who names a lease they do not hold, so a forged target there is
// stopped by the second guard whatever the first one does. VoidCharge is
// staff-only by design (no scope=self grant exists for it) and so carries no
// ownership proof — the workplace guard is the ONLY thing confining it, which
// makes it the op where exempting on target presence is actually exploitable.
func TestWorkplace_ForgedAuthContextTargetStaysConfined(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	doc := wcStaffCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "VoidCharge", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)
	cp, cons := newDomainPipeline(t, ctx, conn, "wcforge")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	// Two tabs the operator opens and charges — one at each building.
	tabA := openTab(t, ctx, conn, cp, cons, "wcfgtaba0000000001", leaseA)
	tabB := openTab(t, ctx, conn, cp, cons, "wcfgtabb0000000001", leaseB)
	for _, tc := range []struct{ label, tab string }{
		{"wcfgchga000000000001", tabA}, {"wcfgchgb000000000001", tabB},
	} {
		env := &processor.OperationEnvelope{
			RequestID:     testutil.GenReqID(tc.label),
			Lane:          processor.LaneDefault,
			OperationType: "Charge",
			Actor:         domainActorKey,
			SubmittedAt:   "2026-07-20T12:10:00Z",
			Class:         "tab",
			Payload:       json.RawMessage(`{"tabKey":"` + tc.tab + `","amountCents":800}`),
			ContextHint: &processor.ContextHint{
				Reads: []string{tc.tab, tc.tab + ".status"},
				Enumerations: []processor.EnumerationHint{
					{Hub: domainActorKey, Relation: "holdsRole", Direction: "out"},
				},
			},
		}
		testutil.PublishOp(t, conn, env)
		testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	}

	// Positive sibling first: the staff member voids at their OWN building, so
	// a Rejected below is the confinement guard talking and not a broken path.
	if got := wcSubmitVoidCharge(t, ctx, conn, cp, cons, "wcfgva00000000000001",
		tabA, wcStaffKey, ""); got != processor.OutcomeAccepted {
		t.Fatalf("staff VoidCharge at its OWN workplace = %v, want Accepted "+
			"(the positive sibling — if this fails the negatives prove nothing)", got)
	}
	// Unforged control: without a target they are confined to their building.
	if got := wcSubmitVoidCharge(t, ctx, conn, cp, cons, "wcfgvb00000000000002",
		tabB, wcStaffKey, ""); got != processor.OutcomeRejected {
		t.Fatalf("staff VoidCharge at ANOTHER building = %v, want Rejected", got)
	}
	// THE FORGERY: the same denied call, plus a fabricated target.
	if got := wcSubmitVoidCharge(t, ctx, conn, cp, cons, "wcfgvc00000000000003",
		tabB, wcStaffKey, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff VoidCharge at ANOTHER building with a FORGED authContext.target = %v, want Rejected — "+
			"target presence must not exempt a scope=any caller from workplace confinement", got)
	}
	// A target naming an unrelated vertex is no better than one naming self.
	if got := wcSubmitVoidCharge(t, ctx, conn, cp, cons, "wcfgvd00000000000004",
		tabB, wcStaffKey, leaseB); got != processor.OutcomeRejected {
		t.Fatalf("staff VoidCharge at ANOTHER building with a forged lease target = %v, want Rejected", got)
	}
	// Denied before any mutation: the void must not have moved tabB's total.
	statusDoc := readDoc(t, ctx, conn, tabB+".status")
	statusData, _ := statusDoc["data"].(map[string]any)
	if got, _ := statusData["totalCents"].(float64); got != 800 {
		t.Errorf("tabB totalCents = %v, want 800 — a denied VoidCharge must write nothing", got)
	}
}

// TestWorkplace_UnwiredStaffDeniedNotWidened pins the design decision, and is
// the vector a `kv.Read(link) == None` guard fails.
//
// UnwireWorksAt tombstones rather than deletes, and a tombstone hydrates as a
// document — so `== None` reads it as "no workplace". Under a worksAt-derived
// exemption that means UNCONFINED: unwiring a staff member's workplace would
// widen their write surface from one building to every building. The exemption
// is role-derived precisely so this actor — who can no longer prove a workplace
// and cannot prove root either — is denied everywhere.
func TestWorkplace_UnwiredStaffDeniedNotWidened(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcunwired")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	tombstoneWorksAt(t, ctx, conn)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcuwa00000000000001", leaseA, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("unwired staff at its FORMER workplace = %v, want Rejected", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcuwb00000000000002", leaseB, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("unwired staff at another building = %v, want Rejected — an unwire must NEVER widen the write surface", got)
	}
}

// TestWorkplace_UnlocatableTargetIsOperatorOnly pins the fail-closed default: a
// lease whose unit is wired into no building resolves to no location, cannot be
// confined, and is therefore root-only. Falling open here would make "remove
// the containedIn link" a confinement bypass.
func TestWorkplace_UnlocatableTargetIsOperatorOnly(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcorphan")
	seedWorkplaceTopology(t, ctx, conn)

	orphanLease := seedLeaseWithApplicant(t, ctx, conn, "BBCAFEWCQRPHANLEASEH", wcStaffID)

	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcora00000000000001", orphanLease, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff OpenTab on an unlocatable lease = %v, want Rejected (fail closed)", got)
	}
	if got := submitOpenTabAs(t, ctx, conn, cp, cons, "wcorb00000000000002", orphanLease, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator OpenTab on an unlocatable lease = %v, want Accepted", got)
	}
}

// wcMenuCapDoc grants the same scope=any tab surface as wcStaffCapDoc plus
// CreateMenuItem/RetireMenuItem — the menu-catalog counterpart of the tab
// grants, proving the capability plane cannot tell staff from root here
// either.
func wcMenuCapDoc() *processor.CapabilityDoc {
	doc := wcStaffCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "CreateMenuItem", Scope: "any"},
		processor.PlatformPermission{OperationType: "RetireMenuItem", Scope: "any"},
		processor.PlatformPermission{OperationType: "SetMenuItemAvailability", Scope: "any"},
		processor.PlatformPermission{OperationType: "SetMenuItemLocation", Scope: "any"},
		processor.PlatformPermission{OperationType: "UpdateMenuItem", Scope: "any"},
		processor.PlatformPermission{OperationType: "SetCafePolicy", Scope: "any"})
	return doc
}

// wcSubmitCreateMenuItem submits CreateMenuItem{name, priceCents, locationKey}
// as an arbitrary actor on the standing path, declaring exactly what a staff
// caller would.
func wcSubmitCreateMenuItem(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, locationKey, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CreateMenuItem",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-05T12:00:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"name":"Latte","priceCents":450,"locationKey":"` + locationKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{locationKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// wcSubmitCreateMenuItemWithReason is wcSubmitCreateMenuItem's sibling for a
// REJECTION vector: it returns the script's own failure message so a test can
// name the guard it means to exercise. A bare outcome check cannot tell the
// location guard from the workplace-confinement guard beside it.
func wcSubmitCreateMenuItemWithReason(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, locationKey, actorKey string) (processor.MessageOutcome, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "CreateMenuItem",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-05T12:00:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"name":"Latte","priceCents":450,"locationKey":"` + locationKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{locationKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// wcSubmitRetireMenuItem submits RetireMenuItem{menuItemKey} as an arbitrary
// actor on the standing path, declaring exactly what a staff caller would.
func wcSubmitRetireMenuItem(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, itemKey, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "RetireMenuItem",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-05T12:05:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// wcSubmitUpdateMenuItem submits UpdateMenuItem{menuItemKey, name, priceCents}
// as an arbitrary actor on the standing path, declaring exactly what a staff
// caller would.
func wcSubmitUpdateMenuItem(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, itemKey, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "UpdateMenuItem",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-05T12:06:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","name":"Latte","priceCents":475}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// wcSubmitSetMenuItemAvailability submits SetMenuItemAvailability{menuItemKey,
// available} as an arbitrary actor on the standing path, declaring exactly
// what a staff caller would.
func wcSubmitSetMenuItemAvailability(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, itemKey, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetMenuItemAvailability",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-05T12:07:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","available":false}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, itemKey + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestWorkplace_MenuItemStaffConfinedToWorkplace is the menu-catalog
// counterpart of TestWorkplace_StaffConfinedToWorkplace: a staff actor may
// add a catalog item at the building it worksAt and is denied at another —
// the write the café's Manage Menu panel needs, currently AuthDenied for
// every front-of-house staffer regardless of building.
func TestWorkplace_MenuItemStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcmenu")
	seedWorkplaceTopology(t, ctx, conn)

	if got := wcSubmitCreateMenuItem(t, ctx, conn, cp, cons, "wcmia00000000000001", wcBuildingAKey, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff CreateMenuItem at its OWN workplace = %v, want Accepted", got)
	}
	if got := wcSubmitCreateMenuItem(t, ctx, conn, cp, cons, "wcmib00000000000002", wcBuildingBKey, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff CreateMenuItem at ANOTHER building = %v, want Rejected", got)
	}
}

// TestWorkplace_MenuItemRejectsNonLocation pins the migrated location guard on
// CreateMenuItem's `locationKey`: the servedAt target must be keyed with an
// admitted location type segment. The guard reads the KEY, never the root
// class alone.
//
// Both calls are made as the OPERATOR, which is exempt from workplace
// confinement, so the difference between them is the location guard alone.
// Verified by mutation: disabling the guard turns the negative green, which a
// staff-actor version of this test did NOT — the confinement guard was
// refusing it instead. The positive vector is seedWorkplaceTopology's
// building: a real per-type-classed building.
func TestWorkplace_MenuItemRejectsNonLocation(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcmenunonloc")
	seedWorkplaceTopology(t, ctx, conn)

	if got := wcSubmitCreateMenuItem(t, ctx, conn, cp, cons, "wcmlp00000000000001", wcBuildingAKey, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator CreateMenuItem at a real building = %v, want Accepted", got)
	}
	got, why := wcSubmitCreateMenuItemWithReason(t, ctx, conn, cp, cons, "wcmln00000000000002", wcStaffKey, domainActorKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("CreateMenuItem servedAt a non-location target = %v, want Rejected", got)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the location guard's own NotALocation", why)
	}
}

// TestWorkplace_MenuItemAcceptsPerTypeClass pins the class arm's positive
// case: a location vertex's class is its own key type (CreateLocation writes
// make_vtx(loc_key, lt, {})), which is the only shape a live location vertex
// carries. Both calls are made as the OPERATOR, which is exempt from
// workplace confinement, so the only thing separating them is the location
// guard.
func TestWorkplace_MenuItemAcceptsPerTypeClass(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcmenupertype")
	seedWorkplaceTopology(t, ctx, conn)

	// class == the key's own type segment: what CreateLocation mints now.
	perType := "vtx.building.BBCAFEPERTYPEBLDGAHJ"
	seedVertex(t, ctx, conn, perType, "building", map[string]any{})
	if got := wcSubmitCreateMenuItem(t, ctx, conn, cp, cons, "wcmpt00000000000001", perType, domainActorKey); got != processor.OutcomeAccepted {
		t.Fatalf("operator CreateMenuItem at a per-type-classed building = %v, want Accepted", got)
	}

	// The KEY arm's own discriminator: an admitted CLASS on a key type that is
	// not a location at all. Every other negative vector here fails both arms
	// at once and so cannot tell the key guard from the class-only guard that
	// preceded it.
	impostor := "vtx.tab.BBCAFEMPSTRTABHJKMNP"
	seedVertex(t, ctx, conn, impostor, "building", map[string]any{})
	got, why := wcSubmitCreateMenuItemWithReason(t, ctx, conn, cp, cons, "wcmpt00000000000002", impostor, domainActorKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("CreateMenuItem servedAt a non-location target = %v, want Rejected", got)
	}
	if !strings.Contains(why, "NotALocation") {
		t.Errorf("refused with %q, want the location guard's own NotALocation", why)
	}
}

// TestWorkplace_RetireMenuItemStaffConfinedToWorkplace proves RetireMenuItem
// resolves its confining location from the item's OWN servedAt link (never a
// payload field, which RetireMenuItem carries none of) — a staff member may
// retire an item served at their own building and is denied for one served
// elsewhere.
func TestWorkplace_RetireMenuItemStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcretiremenu")
	seedWorkplaceTopology(t, ctx, conn)

	itemA := createMenuItem(t, ctx, conn, cp, cons, "wcrmiseeda00000000001", "Latte", 450, wcBuildingAKey)
	itemB := createMenuItem(t, ctx, conn, cp, cons, "wcrmiseedb00000000001", "Latte", 450, wcBuildingBKey)

	if got := wcSubmitRetireMenuItem(t, ctx, conn, cp, cons, "wcrma00000000000001", itemA, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff RetireMenuItem served at its OWN workplace = %v, want Accepted", got)
	}
	if got := wcSubmitRetireMenuItem(t, ctx, conn, cp, cons, "wcrmb00000000000002", itemB, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff RetireMenuItem served at ANOTHER building = %v, want Rejected", got)
	}
}

// TestWorkplace_UpdateMenuItemStaffConfinedToWorkplace proves UpdateMenuItem
// resolves its confining location from the item's OWN servedAt link (never a
// payload field, which UpdateMenuItem carries none of) — a staff member may
// edit an item served at their own building and is denied for one served
// elsewhere. Mirrors TestWorkplace_RetireMenuItemStaffConfinedToWorkplace.
func TestWorkplace_UpdateMenuItemStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcupdatemenu")
	seedWorkplaceTopology(t, ctx, conn)

	itemA := createMenuItem(t, ctx, conn, cp, cons, "wcumiseeda00000000001", "Latte", 450, wcBuildingAKey)
	itemB := createMenuItem(t, ctx, conn, cp, cons, "wcumiseedb00000000001", "Latte", 450, wcBuildingBKey)

	if got := wcSubmitUpdateMenuItem(t, ctx, conn, cp, cons, "wcuma00000000000001", itemA, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff UpdateMenuItem served at its OWN workplace = %v, want Accepted", got)
	}
	if got := wcSubmitUpdateMenuItem(t, ctx, conn, cp, cons, "wcumb00000000000002", itemB, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff UpdateMenuItem served at ANOTHER building = %v, want Rejected", got)
	}
}

// TestWorkplace_SetMenuItemAvailabilityStaffConfinedToWorkplace proves
// SetMenuItemAvailability resolves its confining location from the item's
// OWN servedAt link (never a payload field, which SetMenuItemAvailability
// carries none of) — a staff member may toggle an item served at their own
// building and is denied for one served elsewhere. Mirrors
// TestWorkplace_UpdateMenuItemStaffConfinedToWorkplace: an op tested only as
// the operator has never run this guard.
func TestWorkplace_SetMenuItemAvailabilityStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcsetavail")
	seedWorkplaceTopology(t, ctx, conn)

	itemA := createMenuItem(t, ctx, conn, cp, cons, "wcsaiseeda00000000001", "Latte", 450, wcBuildingAKey)
	itemB := createMenuItem(t, ctx, conn, cp, cons, "wcsaiseedb00000000001", "Latte", 450, wcBuildingBKey)

	if got := wcSubmitSetMenuItemAvailability(t, ctx, conn, cp, cons, "wcsaa00000000000001", itemA, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff SetMenuItemAvailability served at its OWN workplace = %v, want Accepted", got)
	}
	if got := wcSubmitSetMenuItemAvailability(t, ctx, conn, cp, cons, "wcsab00000000000002", itemB, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff SetMenuItemAvailability served at ANOTHER building = %v, want Rejected", got)
	}
}

// wcSubmitSetMenuItemLocation submits SetMenuItemLocation{menuItemKey,
// newLocation} as an arbitrary actor on the standing path, declaring exactly
// what a staff caller would — itemKey and newLocation are both declared
// Reads (ddls.go's SetMenuItemLocation branch: item_key for vertex_alive,
// newLocation for require_live_location, mirroring
// TestSetMenuItemLocation_MovesServedAtLink's own envelope).
func wcSubmitSetMenuItemLocation(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, itemKey, newLocation, actorKey string) processor.MessageOutcome {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetMenuItemLocation",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-05T12:08:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"menuItemKey":"` + itemKey + `","newLocation":"` + newLocation + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{itemKey, newLocation},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	return testutil.DriveOne(t, ctx, cp, cons, "")
}

// TestWorkplace_SetMenuItemLocationStaffConfinedToWorkplace proves
// SetMenuItemLocation resolves its confining location from the NEW target
// alone (permissions.go: "confined to the NEW workplace — the item's own
// served-at link may already be dead, this op's repair case"), never the
// item's current servedAt — a staff member may relocate an item TO their own
// building and is denied relocating one TO another. Mirrors
// TestWorkplace_SetMenuItemAvailabilityStaffConfinedToWorkplace: an op
// tested only as the operator has never run this guard.
func TestWorkplace_SetMenuItemLocationStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcsetmenuloc")
	seedWorkplaceTopology(t, ctx, conn)

	// Both items start served at building B — irrelevant to this op's own
	// confinement, which binds the NEW location the payload names, not
	// wherever the item happens to be served today; started elsewhere so the
	// move actually creates a new servedAt link rather than colliding with
	// an already-live one at the SAME location.
	itemA := createMenuItem(t, ctx, conn, cp, cons, "wcsmiseeda00000000001", "Latte", 450, wcBuildingBKey)
	itemB := createMenuItem(t, ctx, conn, cp, cons, "wcsmiseedb00000000001", "Latte", 450, wcBuildingAKey)

	if got := wcSubmitSetMenuItemLocation(t, ctx, conn, cp, cons, "wcsma00000000000001", itemA, wcBuildingAKey, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff SetMenuItemLocation TO its OWN workplace = %v, want Accepted", got)
	}
	if got := wcSubmitSetMenuItemLocation(t, ctx, conn, cp, cons, "wcsmb00000000000002", itemB, wcBuildingBKey, wcStaffKey); got != processor.OutcomeRejected {
		t.Fatalf("staff SetMenuItemLocation TO ANOTHER building = %v, want Rejected", got)
	}
}

// wcCapturedReads is a per-test ScriptReadObserver that keeps the LAST
// ScriptReadRecord seen for each request id — a retried operation re-enters
// step 4/5 on the same request id, so the last record is the one for the
// execution that actually committed — and wakes any waiter through a channel
// rather than a sleep, since the pipeline drives an execution synchronously on
// the test goroutine and the record can already be there by the time the
// waiter looks. Mirrors clinic-domain's capturedScriptReads
// (withprovider_listing_test.go), filtered on Charge instead of
// SetAppointmentStatus.
type wcCapturedReads struct {
	mu      sync.Mutex
	records map[string]processor.ScriptReadRecord
	notify  chan struct{}
}

func newWCCapturedReads() *wcCapturedReads {
	return &wcCapturedReads{
		records: make(map[string]processor.ScriptReadRecord),
		notify:  make(chan struct{}, 1),
	}
}

func (c *wcCapturedReads) ObserveScriptReads(_ context.Context, env *processor.OperationEnvelope, record processor.ScriptReadRecord) {
	if env.OperationType != "Charge" {
		return
	}
	c.mu.Lock()
	c.records[env.RequestID] = record
	c.mu.Unlock()
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// waitFor blocks until a record for requestID has been observed, polling on
// the notify channel rather than a fixed sleep, bounded by a ceiling that
// only trips on a real defect.
func (c *wcCapturedReads) waitFor(t *testing.T, requestID string) processor.ScriptReadRecord {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		c.mu.Lock()
		rec, ok := c.records[requestID]
		c.mu.Unlock()
		if ok {
			return rec
		}
		select {
		case <-c.notify:
		case <-deadline:
			t.Fatalf("timed out waiting for a ScriptReadRecord for requestId %s", requestID)
		}
	}
}

// TestWorkplace_ChargeCatalogItemListsAppliesToUnitOnce proves Charge's two
// leaseapp_unit resolutions on one tab's lease — the staff-confinement site
// and the catalog-locality site, both fed the SAME
// existing.data.get("leaseAppKey") — share one appliesToUnit listing per
// execution, via the per-execution memo leaseapp_unit threads, rather than
// issuing the identical listing twice.
//
// Enumerations (ScriptReadRecord.Enumerations) cannot see the difference: it
// is a SET keyed {hub, relation, direction}, and both leaseapp_unit calls
// enumerate the exact same lease key, so a doubled appliesToUnit listing
// collapses to the same single set member a memoized lookup produces either
// way — asserting len==1 on it would pass whether or not the memo exists.
// ListCalls, the per-execution counter, is the only observable that can tell
// one listing from two (authority-walk-wall-unit-cost-design.md §4.1).
func TestWorkplace_ChargeCatalogItemListsAppliesToUnitOnce(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcStaffCapDoc())
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	reads := newWCCapturedReads()
	cp, cons := testutil.CapabilityPipeline(t, ctx, conn, testutil.PipelineConfig{
		Durable:                  "wcchargeonce",
		Instance:                 "cd-wcchargeonce",
		ExtraScriptReadObservers: []processor.ScriptReadObserver{reads},
	})

	tabA := openTab(t, ctx, conn, cp, cons, "wccoseedtaba00000001", leaseA)
	tabB := openTab(t, ctx, conn, cp, cons, "wccoseedtabb00000001", leaseB)
	itemA := createMenuItem(t, ctx, conn, cp, cons, "wccoseedmenua0000001", "Latte", 450, wcBuildingAKey)
	itemB := createMenuItem(t, ctx, conn, cp, cons, "wccoseedmenub0000001", "Latte", 450, wcBuildingBKey)

	// Positive vector: staff Charge, catalog item, at the staff member's OWN
	// workplace. Its own leaseAppKey feeds leaseapp_unit at both sites.
	const label = "wccochargea0000000001"
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         wcStaffKey,
		SubmittedAt:   "2026-09-13T12:00:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabA + `","menuItemKey":"` + itemA + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabA, tabA + ".status", itemA, itemA + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: wcStaffKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("staff Charge with menuItemKey at its OWN workplace = %v, want Accepted "+
			"(the positive sibling — if this fails the negative below proves nothing)", got)
	}

	rec := reads.waitFor(t, testutil.GenReqID(label))

	appliesToUnitMembers := 0
	for _, e := range rec.Enumerations {
		if e.Relation == "appliesToUnit" {
			appliesToUnitMembers++
		}
	}
	if appliesToUnitMembers != 1 {
		t.Fatalf("Charge enumerations = %v, want exactly ONE appliesToUnit member "+
			"(a set keyed {hub,relation,direction} cannot see a doubled identical listing)", rec.Enumerations)
	}

	// Five listings make up this execution: the actor_holds_operator holdsRole
	// walk (staff is not operator, one page); the FIRST leaseapp_unit's
	// appliesToUnit listing (staff-confinement site, memo miss); the
	// worksAt_covers containedIn listing off the tab's unit (one page, matches
	// on the unit's own parent building); the menu_item_served_at servedAt
	// listing; and the location_covers containedIn listing off the same unit
	// (one page, matches on that same parent building). The SECOND
	// leaseapp_unit call (catalog-locality site) hits the memo and issues no
	// listing at all — without the memo this would be 6.
	const wantListCalls = 5
	if rec.ListCalls != wantListCalls {
		t.Fatalf("Charge ListCalls = %d over enumerations %v, want %d: "+
			"actor_holds_operator holdsRole + leaseapp_unit appliesToUnit (1st, memo miss) + "+
			"worksAt_covers containedIn + menu_item_served_at servedAt + location_covers containedIn "+
			"(leaseapp_unit's 2nd call must hit the memo and cost nothing)",
			rec.ListCalls, rec.Enumerations, wantListCalls)
	}

	// Negative sibling: the same staff actor and shape, at building B — proves
	// confinement still denies on the memoized path, so the Accepted result
	// above is the workplace guard actually running rather than some
	// unconditional pass.
	negEnv := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("wccochargeb0000000001"),
		Lane:          processor.LaneDefault,
		OperationType: "Charge",
		Actor:         wcStaffKey,
		SubmittedAt:   "2026-09-13T12:05:00Z",
		Class:         "tab",
		Payload:       json.RawMessage(`{"tabKey":"` + tabB + `","menuItemKey":"` + itemB + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{tabB, tabB + ".status", itemB, itemB + ".price"},
			Enumerations: []processor.EnumerationHint{
				{Hub: wcStaffKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	testutil.PublishOp(t, conn, negEnv)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("staff Charge with menuItemKey at ANOTHER building = %v, want Rejected", got)
	}
}

// TestWorkplace_MarkLineServedStaffConfinedToWorkplace: MarkLineServed is
// staff-only with no ownership proof, so — exactly as for VoidCharge — the
// workplace walk is the ONLY thing confining it. A front-of-house staffer hands
// over a self-order at their OWN building and is refused at another, the
// refusal writing nothing.
func TestWorkplace_MarkLineServedStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	doc := wcStaffCapDoc()
	doc.PlatformPermissions = append(doc.PlatformPermissions,
		processor.PlatformPermission{OperationType: "MarkLineServed", Scope: "any"})
	testutil.SeedCapDoc(t, ctx, conn, doc)
	testutil.SeedCapDoc(t, ctx, conn, domainConsumerCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcserve")
	leaseA, leaseB := seedWorkplaceTopology(t, ctx, conn)

	// One resident holds both leases (a test topology), so both tabs carry a
	// self-ordered, unserved line-1.
	seedIdentity(t, ctx, conn, domainConsumerID)
	tabs := map[string]string{}
	for _, tc := range []struct{ leaseID, leaseKey, unitID, label string }{
		{wcLeaseAID, leaseA, wcUnitAID, "wcsrva"}, {wcLeaseBID, leaseB, wcUnitBID, "wcsrvb"},
	} {
		appFor := "lnk.leaseapp." + tc.leaseID + ".applicationFor.identity." + domainConsumerID
		testutil.SeedLink(t, ctx, conn, appFor, "applicationFor", tc.leaseKey, domainConsumerKey)
		tab := openTab(t, ctx, conn, cp, cons, tc.label+"tab00000000001", tc.leaseKey)
		item := createMenuItem(t, ctx, conn, cp, cons, tc.label+"itm00000000001", "Latte", 450, "vtx.unit."+tc.unitID)
		selfOrder(t, ctx, conn, cp, cons, tc.label+"ord00000000001", tab, item, appFor, "2026-07-20T12:10:00Z")
		tabs[tc.leaseKey] = tab
	}
	tabA, tabB := tabs[leaseA], tabs[leaseB]

	// Positive sibling first: at their OWN building the staffer's serve lands.
	testutil.PublishOp(t, conn, markLineServedEnv("wcsrvaserve000000001", tabA, "line-1", wcStaffKey, "2026-07-20T12:30:00Z"))
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("staff MarkLineServed at its OWN workplace = %v, want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got)
	}
	_, linesA := tabLines(t, ctx, conn, tabA)
	if got, want := linesA[0]["servedBy"], wcStaffKey; got != want {
		t.Fatalf("tabA lines[0].servedBy = %v, want %q", got, want)
	}

	// At ANOTHER building the same call is refused before the line lookup.
	testutil.PublishOp(t, conn, markLineServedEnv("wcsrvbserve000000001", tabB, "line-1", wcStaffKey, "2026-07-20T12:31:00Z"))
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("staff MarkLineServed at ANOTHER building = %v, want Rejected", got)
	}
	// A fabricated authContext.target — self, then the tab's own lease — must
	// not exempt a scope=any caller: the exemption keys on a target the
	// PLATFORM validated, and no task validates one for this op.
	for _, tc := range []struct{ label, target string }{
		{"wcsrvbforgeself00001", wcStaffKey}, {"wcsrvbforgelease0001", leaseB},
	} {
		env := markLineServedEnv(tc.label, tabB, "line-1", wcStaffKey, "2026-07-20T12:32:00Z")
		env.AuthContext = &processor.AuthContext{Target: tc.target}
		testutil.PublishOp(t, conn, env)
		if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
			t.Fatalf("staff MarkLineServed at ANOTHER building with a FORGED authContext.target %s = %v, want Rejected", tc.target, got)
		}
	}
	_, linesB := tabLines(t, ctx, conn, tabB)
	if _, has := linesB[0]["servedAt"]; has {
		t.Fatalf("tabB lines[0] carries servedAt %v — a denied MarkLineServed must write nothing", linesB[0]["servedAt"])
	}
}

// wcSubmitSetCafePolicy submits SetCafePolicy{locationKey, tabLimitCents} as
// an arbitrary actor on the standing path, declaring exactly what a staff
// caller would — the location in Reads, its .cafePolicy as the class-(d)
// OptionalRead, the holdsRole enumeration the confinement walk needs.
func wcSubmitSetCafePolicy(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer, label, locationKey string, tabLimitCents int, actorKey string) (processor.MessageOutcome, string) {
	t.Helper()
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: "SetCafePolicy",
		Actor:         actorKey,
		SubmittedAt:   "2026-08-05T12:09:00Z",
		Class:         "menuitem",
		Payload:       json.RawMessage(`{"locationKey":"` + locationKey + `","tabLimitCents":` + strconv.Itoa(tabLimitCents) + `}`),
		ContextHint: &processor.ContextHint{
			Reads:         []string{locationKey},
			OptionalReads: []string{locationKey + ".cafePolicy"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
			},
		},
	}
	outcome, reply := testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
	msg := ""
	if reply != nil && reply.Error != nil {
		msg = reply.Error.Message
	}
	return outcome, msg
}

// TestWorkplace_SetCafePolicyStaffConfinedToWorkplace proves SetCafePolicy
// confines a front-of-house staffer to a location their workplace covers —
// the payload's own locationKey, CreateMenuItem's confinement — and that the
// refusal is the confinement, not the location guard. An op tested only as
// the operator has never run this guard.
func TestWorkplace_SetCafePolicyStaffConfinedToWorkplace(t *testing.T) {
	ctx, conn := setupDomainEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, wcMenuCapDoc())
	cp, cons := newDomainPipeline(t, ctx, conn, "wcsetpolicy")
	seedWorkplaceTopology(t, ctx, conn)

	if got, msg := wcSubmitSetCafePolicy(t, ctx, conn, cp, cons, "wcscpa00000000000001", wcBuildingAKey, 5000, wcStaffKey); got != processor.OutcomeAccepted {
		t.Fatalf("staff SetCafePolicy at its OWN workplace = %v (%s), want Accepted", got, msg)
	}
	got, msg := wcSubmitSetCafePolicy(t, ctx, conn, cp, cons, "wcscpb00000000000002", wcBuildingBKey, 5000, wcStaffKey)
	if got != processor.OutcomeRejected {
		t.Fatalf("staff SetCafePolicy at ANOTHER building = %v, want Rejected", got)
	}
	if !strings.Contains(msg, "AuthDenied") {
		t.Fatalf("staff SetCafePolicy at ANOTHER building rejected with %q, want the workplace confinement's AuthDenied", msg)
	}
	if keyExists(t, ctx, conn, wcBuildingBKey+".cafePolicy") {
		t.Fatalf("building B carries a .cafePolicy after a refused staff write")
	}
}
