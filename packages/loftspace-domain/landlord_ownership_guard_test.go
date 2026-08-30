// The ownership ops bind their actor. AssignUnitOwner / RemoveUnitOwner are what
// CONFERS unit management, so enforce_manages DEFAULT-DENIES: the operator role
// is the only exemption, and every other actor — whatever auth path carried it —
// must already hold a live `manages` link to the payload unit. These tests drive
// each path the platform can authorize on: platform scope=self, an ephemeral task
// grant, a plain scope=any grant held by a non-operator (the shape the service
// path and an empty-target task grant both present), and the operator itself.
package loftspacedomain_test

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
	loftspacedomain "github.com/operatinggraph/lattice/packages/loftspace-domain"
)

const (
	loOwnerID  = "LSownerActorHJKMNPQR"
	loOwnerKey = "vtx.identity." + loOwnerID
	loOwnerCap = "cap.identity." + loOwnerID

	loPeerID  = "LSpeerActorHJKMNPQRS"
	loPeerKey = "vtx.identity." + loPeerID

	loAnyID  = "LSanyActorHJKMNPQRST"
	loAnyKey = "vtx.identity." + loAnyID

	loTaskID  = "LStaskActorHJKMNPQRS"
	loTaskKey = "vtx.identity." + loTaskID
	loTaskCap = "cap.ephemeral.identity." + loTaskID
)

// loOwnerCapDoc is a plain signed-in landlord: `consumer` plus scope=SELF grants
// on the two ownership ops and nothing else, so step 3 denies unless
// authContext.target == actor — the path enforce_manages binds.
func loOwnerCapDoc() *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		Key:                    loOwnerCap,
		Actor:                  loOwnerKey,
		Version:                "1.0",
		ProjectedAt:            "2026-07-26T00:00:00Z",
		ProjectedFromRevisions: map[string]uint64{loOwnerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "AssignUnitOwner", Scope: "self"},
			{OperationType: "RemoveUnitOwner", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	}
}

// loTaskCapDoc holds AssignUnitOwner through an ephemeral TASK grant naming a
// unit. The task branch reads the disjoint cap.ephemeral.identity.<id> key
// (Contract #10 §10.7), not the actor's cap doc. The grant's target is the unit,
// not the actor — the shape a copy of SetListingStatus's require_manages probe
// would have returned on, since its exemption compares the validated target to
// the actor and this one is a resource.
func loTaskCapDoc(unitKey string) *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		Key:                    loTaskCap,
		Actor:                  loTaskKey,
		Version:                "1.0",
		ProjectedAt:            "2026-07-26T00:00:00Z",
		ProjectedFromRevisions: map[string]uint64{loTaskKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions:    []processor.PlatformPermission{},
		ServiceAccess:          []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{
			{
				Source:        "task",
				TaskKey:       "vtx.task." + loTaskID,
				OperationType: "AssignUnitOwner",
				Target:        unitKey,
				ExpiresAt:     time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			},
		},
		Roles: []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	}
}

// loOwnershipAs submits an ownership op on a VALIDATED path and returns the reply
// so a test can see which check answered. Both manages links are declared in
// optionalReads: the payload pair's (the op's own idempotency read) and the
// ACTOR's (the probe's), which differ whenever the actor confers on someone else.
func loOwnershipAs(t *testing.T, ctx context.Context, conn *substrate.Conn,
	cp *processor.CommitPath, cons jetstream.Consumer,
	label, opType, landlordKey, unitKey, actorKey, target, task string) (processor.MessageOutcome, *processor.OperationReply) {
	t.Helper()
	_, actorID, _ := substrate.ParseVertexKey(actorKey)
	_, unitID, _ := substrate.ParseVertexKey(unitKey)
	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID(label),
		Lane:          processor.LaneDefault,
		OperationType: opType,
		Actor:         actorKey,
		SubmittedAt:   time.Now().UTC().Format(time.RFC3339),
		Class:         "loftspaceOwnership",
		Payload:       json.RawMessage(`{"landlord":"` + landlordKey + `","unit":"` + unitKey + `"}`),
		ContextHint: &processor.ContextHint{
			Reads: []string{landlordKey, unitKey},
			OptionalReads: []string{
				manageLinkKey(landlordKey, unitKey),
				"lnk.identity." + actorID + ".manages.unit." + unitID,
			},
			Enumerations: loOwnershipEnumerations(opType, actorKey),
		},
		AuthContext: &processor.AuthContext{Target: target, Task: task},
	}
	return testutil.SubmitAndAwaitReply(t, ctx, conn, cp, cons, env)
}

// loOwnershipEnumerations names the actor-role confinement walk both ownership
// ops run (ownership.go's actor_holds_operator), from the channel each one has.
//
// The RemoveUnitOwner arm is keyed on the OP, never on "the resolve came back
// empty". Keyed on emptiness, deleting AssignUnitOwner's own
// Dispatch.Enumerations would silently fall through to the hardcoded hint and
// leave every submission green with its baseline row already retired — the
// exact failure DeclaredEnumerations exists to make impossible. Only the op
// that genuinely has no OpMetaSpec to resolve from (opmetas.go states why: no
// cmd/*-app renders it) declares through its hand-built envelope, which is
// itself a sanctioned declaring channel.
func loOwnershipEnumerations(opType, actorKey string) []processor.EnumerationHint {
	if opType == "RemoveUnitOwner" {
		return []processor.EnumerationHint{{Hub: actorKey, Relation: "holdsRole", Direction: "out"}}
	}
	return testutil.DeclaredEnumerations(opType, actorKey, loftspacedomain.OpMetas())
}

// loSeedLandlord seeds a signed-in identity holding `consumer` plus its cap doc.
func loSeedLandlord(t *testing.T, ctx context.Context, conn *substrate.Conn, key string, doc *processor.CapabilityDoc) {
	t.Helper()
	testutil.SeedCapDoc(t, ctx, conn, doc)
	lsSeedVertex(t, ctx, conn, key, "identity", false)
	testutil.SeedHoldsRole(t, ctx, conn, key,
		"vtx.role."+pkgmgr.RoleID("identity-domain", "consumer"))
}

// TestLandlord_AssignConfinedToManagedUnit: the landlord confers management of
// the unit they already manage and is denied on the one they do not — and the
// denial is the ownership probe rather than a downstream check.
func TestLandlord_AssignConfinedToManagedUnit(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "loassign")
	loSeedLandlord(t, ctx, conn, loOwnerKey, loOwnerCapDoc())
	lsSeedVertex(t, ctx, conn, loPeerKey, "identity", false)

	mine := llCreateUnit(t, ctx, conn, cp, cons, "loUnitMine")
	theirs := llCreateUnit(t, ctx, conn, cp, cons, "loUnitTheirs")
	assignUnitOwner(t, ctx, conn, cp, cons, "loSeed1", loOwnerKey, mine,
		lsStaffActorKey, processor.OutcomeAccepted)

	// Positive sibling first: a Rejected below is then the manages probe talking,
	// not a broken scope=self path.
	if got, reply := loOwnershipAs(t, ctx, conn, cp, cons, "loAssign1", "AssignUnitOwner",
		loPeerKey, mine, loOwnerKey, loOwnerKey, ""); got != processor.OutcomeAccepted {
		t.Fatalf("landlord confers management of the unit they MANAGE = %v (%+v), want Accepted "+
			"(the positive sibling — if this fails the negative proves nothing)", got, reply.Error)
	}
	if doc := lsReadDoc(t, ctx, conn, manageLinkKey(loPeerKey, mine)); doc["class"] != "manages" {
		t.Fatalf("the delegation did not commit a manages link: %+v", doc)
	}

	got, reply := loOwnershipAs(t, ctx, conn, cp, cons, "loAssign2", "AssignUnitOwner",
		loPeerKey, theirs, loOwnerKey, loOwnerKey, "")
	if got != processor.OutcomeRejected {
		t.Fatalf("landlord confers management of a unit they do NOT manage = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the unmanaged assign rejected with %+v, want AuthDenied — the ownership probe must "+
			"answer before the alive checks, or this op reports whether a unit exists", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, manageLinkKey(loPeerKey, theirs)); err == nil {
		t.Errorf("the denied assign committed a management link on %s", theirs)
	}
}

// TestLandlord_RemoveIsSelfOnly: on a validated path a landlord may drop their
// OWN management of a unit they manage, but not a peer co-manager's. `manages` is
// a flat set with no primary, so a symmetric revoke would let whoever was
// delegated management last remove everyone who came before.
func TestLandlord_RemoveIsSelfOnly(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "loremove")
	loSeedLandlord(t, ctx, conn, loOwnerKey, loOwnerCapDoc())
	lsSeedVertex(t, ctx, conn, loPeerKey, "identity", false)

	unit := llCreateUnit(t, ctx, conn, cp, cons, "loUnitShared")
	assignUnitOwner(t, ctx, conn, cp, cons, "loSeed2", loOwnerKey, unit,
		lsStaffActorKey, processor.OutcomeAccepted)
	assignUnitOwner(t, ctx, conn, cp, cons, "loSeed3", loPeerKey, unit,
		lsStaffActorKey, processor.OutcomeAccepted)

	// The peer's link survives an attempt by a co-manager to revoke it.
	got, reply := loOwnershipAs(t, ctx, conn, cp, cons, "loRemove1", "RemoveUnitOwner",
		loPeerKey, unit, loOwnerKey, loOwnerKey, "")
	if got != processor.OutcomeRejected {
		t.Fatalf("co-manager revokes a PEER's management = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the peer revoke rejected with %+v, want AuthDenied", reply.Error)
	}
	if doc := lsReadDoc(t, ctx, conn, manageLinkKey(loPeerKey, unit)); doc["isDeleted"] == true {
		t.Fatalf("the denied revoke tombstoned the peer's management link")
	}

	// The same actor dropping their OWN management is accepted — the positive
	// vector that proves the rejection above was the self-only rule and not a
	// scope=self path that never matched.
	if got, reply := loOwnershipAs(t, ctx, conn, cp, cons, "loRemove2", "RemoveUnitOwner",
		loOwnerKey, unit, loOwnerKey, loOwnerKey, ""); got != processor.OutcomeAccepted {
		t.Fatalf("landlord drops their OWN management = %v (%+v), want Accepted", got, reply.Error)
	}
	if doc := lsReadDoc(t, ctx, conn, manageLinkKey(loOwnerKey, unit)); doc["isDeleted"] != true {
		t.Fatalf("the self revoke did not tombstone the landlord's own link: %+v", doc)
	}
}

// TestLandlord_TaskGrantIsBoundToo: an ephemeral task grant naming the unit is
// the second path on which authTargetValidated is true, and its target is the
// UNIT rather than the actor. The probe must bind it: a task grant confers the
// right to run the op, never the management the op hands out.
func TestLandlord_TaskGrantIsBoundToo(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "lotask")
	lsSeedVertex(t, ctx, conn, loPeerKey, "identity", false)

	unit := llCreateUnit(t, ctx, conn, cp, cons, "loUnitTask")
	loSeedLandlord(t, ctx, conn, loTaskKey, loTaskCapDoc(unit))

	got, reply := loOwnershipAs(t, ctx, conn, cp, cons, "loTask1", "AssignUnitOwner",
		loPeerKey, unit, loTaskKey, unit, "vtx.task."+loTaskID)
	if got != processor.OutcomeRejected {
		t.Fatalf("task-grant holder confers management of a unit it does not manage = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the task-grant assign rejected with %+v, want AuthDenied — a probe whose exemption "+
			"compares the validated target to the actor would have let this through", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, manageLinkKey(loPeerKey, unit)); err == nil {
		t.Error("the denied task-grant assign committed a management link")
	}
}

// TestLandlord_OperatorPathUnbound: the standing scope=any operator path carries
// no authContext, so authTargetValidated is false and the probe returns before
// reading anything. The trusted admin tool and seed-showcase submit there and
// must keep working with no management link of their own.
func TestLandlord_OperatorPathUnbound(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "looperator")

	lsSeedVertex(t, ctx, conn, loPeerKey, "identity", false)
	unit := llCreateUnit(t, ctx, conn, cp, cons, "loUnitOperator")

	assignUnitOwner(t, ctx, conn, cp, cons, "loOp1", loPeerKey, unit,
		lsStaffActorKey, processor.OutcomeAccepted)
	if doc := lsReadDoc(t, ctx, conn, manageLinkKey(loPeerKey, unit)); doc["class"] != "manages" {
		t.Fatalf("the operator first-assign did not commit: %+v", doc)
	}
	removeUnitOwner(t, ctx, conn, cp, cons, "loOp2", loPeerKey, unit,
		lsStaffActorKey, processor.OutcomeAccepted)
	if doc := lsReadDoc(t, ctx, conn, manageLinkKey(loPeerKey, unit)); doc["isDeleted"] != true {
		t.Fatalf("the operator revoke did not tombstone the link: %+v", doc)
	}
}

// loAnyCapDoc holds AssignUnitOwner at scope=ANY without the operator role — the
// shape BOTH escapes the probe's exemption has to survive. A service-path
// authorization never inspects a target, and a task grant whose scopedTo vertex
// was tombstoned projects an empty target that matches an empty authContext; in
// each case the platform authorizes with its validated-target bit unset, exactly
// as this actor does. If the exemption keyed on that bit rather than on the
// operator role, this caller would confer management of a unit it never held.
func loAnyCapDoc() *processor.CapabilityDoc {
	return &processor.CapabilityDoc{
		Key:                    "cap.identity." + loAnyID,
		Actor:                  loAnyKey,
		Version:                "1.0",
		ProjectedAt:            "2026-07-26T00:00:00Z",
		ProjectedFromRevisions: map[string]uint64{loAnyKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "AssignUnitOwner", Scope: "any"},
			{OperationType: "RemoveUnitOwner", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "consumer")},
	}
}

// TestLandlord_ScopeAnyNonOperatorIsBound: a scope=any grant held by an identity
// that is NOT the operator confers no management. This is the vector that fails
// if the probe's exemption is ever rewritten as "no validated target" — that
// caller reaches the script with the bit unset and full scope=any authorization.
func TestLandlord_ScopeAnyNonOperatorIsBound(t *testing.T) {
	ctx, conn := setupLoftspaceEnv(t)
	cp, cons := newLoftspacePipeline(t, ctx, conn, "loany")
	loSeedLandlord(t, ctx, conn, loAnyKey, loAnyCapDoc())
	lsSeedVertex(t, ctx, conn, loPeerKey, "identity", false)

	unit := llCreateUnit(t, ctx, conn, cp, cons, "loUnitAny")

	got, reply := loOwnershipAs(t, ctx, conn, cp, cons, "loAny1", "AssignUnitOwner",
		loPeerKey, unit, loAnyKey, "", "")
	if got != processor.OutcomeRejected {
		t.Fatalf("scope=any non-operator confers management of a unit it does not manage = %v, want Rejected", got)
	}
	if reply.Error == nil || !strings.Contains(reply.Error.Message, "AuthDenied:") {
		t.Fatalf("the scope=any assign rejected with %+v, want AuthDenied — the probe must bind every "+
			"actor but the operator, not merely the ones carrying a validated target", reply.Error)
	}
	if _, err := conn.KVGet(ctx, testutil.HarnessCoreBucket, manageLinkKey(loPeerKey, unit)); err == nil {
		t.Error("the denied scope=any assign committed a management link")
	}

	// The same actor, once it holds the unit, is allowed — the positive sibling
	// that proves the rejection was the probe and not the scope=any path failing
	// to match at all.
	assignUnitOwner(t, ctx, conn, cp, cons, "loAny2", loAnyKey, unit,
		lsStaffActorKey, processor.OutcomeAccepted)
	if got, reply := loOwnershipAs(t, ctx, conn, cp, cons, "loAny3", "AssignUnitOwner",
		loPeerKey, unit, loAnyKey, "", ""); got != processor.OutcomeAccepted {
		t.Fatalf("scope=any actor that DOES manage the unit = %v (%+v), want Accepted", got, reply.Error)
	}
}
