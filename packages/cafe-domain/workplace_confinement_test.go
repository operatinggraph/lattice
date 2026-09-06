package cafedomain_test

import (
	"context"
	"encoding/json"
	"strings"
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
			OptionalReads: []string{leaseKey + ".cafeOpenTab", leaseKey + ".decision"},
			Enumerations: []processor.EnumerationHint{
				{Hub: actorKey, Relation: "holdsRole", Direction: "out"},
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
		Payload:       json.RawMessage(`{"tabKey":"` + tabKey + `","amountCents":100}`),
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
		processor.PlatformPermission{OperationType: "UpdateMenuItem", Scope: "any"})
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
