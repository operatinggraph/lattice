package wellnessledger_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// Front-desk unconfined grant for CreateAccount (verticals.md — "CreateAccount
// can't be called from any of the 4 ledger apps' browsers", and "A wellness
// class's ledger account never opens", blocked-on this exact gap).
// DebitAccount/CreditAccount stay operator-only — every wellness charge is a
// Weaver-target auto-charge, no front-desk caller reaches them. A
// wellnessaccount is anchored on a member identity, which carries no
// building, so there is nothing left to workplace-confine — mirrors
// clinic-ledger's identical CreateAccount fix and clinic-domain's
// TestFrontDesk_RegisterPatientUnconfined.
const (
	ledFDActorID  = "WLLEDFDACTRHJKMNPQRS"
	ledFDActorKey = "vtx.identity." + ledFDActorID
	ledFDCapKey   = "cap.identity." + ledFDActorID

	ledNoGrantActorID  = "WLLEDNGRANTHJKMNPQRS"
	ledNoGrantActorKey = "vtx.identity." + ledNoGrantActorID

	// ledConsumerID stands in for identity-domain's real `consumer` role
	// grant flow (mirrors wellness-domain's domainConsumerID) — a real
	// member's own identity, self-opening their OWN ledger account.
	ledConsumerID  = "WLLEDCQNSUMERHJKMNPQ"
	ledConsumerKey = "vtx.identity." + ledConsumerID
	ledConsumerCap = "cap.identity." + ledConsumerID
)

// ledConsumerCapDoc grants the consumer role's scope=self WellnessCreateAccount
// permission — the real-actor-write-auth-e2e self-service caller, mirrors
// wellness-domain's domainConsumerCapDoc.
func ledConsumerCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ledConsumerCap,
		Actor:                  ledConsumerKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ledConsumerKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "WellnessCreateAccount", Scope: "self"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role.consumer"},
	}
}

func ledFDCapDoc() *processor.CapabilityDoc {
	now := time.Now().UTC()
	return &processor.CapabilityDoc{
		Key:                    ledFDCapKey,
		Actor:                  ledFDActorKey,
		Version:                "1.0",
		ProjectedAt:            now.Format(time.RFC3339Nano),
		ProjectedFromRevisions: map[string]uint64{ledFDActorKey: 1},
		Lanes:                  []string{"default"},
		PlatformPermissions: []processor.PlatformPermission{
			{OperationType: "WellnessCreateAccount", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// TestFrontDesk_CreateAccountUnconfined is the Inc guarantee: a front-desk
// actor holding only the CreateAccount grant (no operator role) can open a
// member's ledger account — the call wellness-app's My Classes balance panel
// needs before it can show anything but "no charges yet".
func TestFrontDesk_CreateAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledger")

	identityKey := seedIdentity(t, ctx, conn, "WLFD23456789ABCDT1AB")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdledgeracct0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk CreateAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_CreateAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_CreateAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdenied")

	identityKey := seedIdentity(t, ctx, conn, "WLFD23456789ABCDT2AB")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdlnograntacct00001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + identityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{identityKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("CreateAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}

// TestConsumer_CreateAccountSelfScopeAllowed proves a real member, holding
// only the consumer scope=self grant, can open THEIR OWN ledger account —
// most bookings are self-service (wellness-domain's CreateBooking self-scope
// precedent), so the account has to open at the same moment without a
// front-desk actor in the loop.
func TestConsumer_CreateAccountSelfScopeAllowed(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfledgerok")

	seedIdentity(t, ctx, conn, ledConsumerID)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("selfledgeracct00001"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledConsumerKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + ledConsumerKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{ledConsumerKey}},
		AuthContext:   &processor.AuthContext{Target: ledConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("self-service WellnessCreateAccount outcome = %v, want Accepted", got)
	}
}

// TestConsumer_CreateAccountSelfScopeRejectedForOther proves the Starlark
// guard closes the gap step 3 leaves open: step 3's scope=self only checks
// authContext.target == actor, never looks at payload.identityKey. A
// consumer satisfying that check but naming a DIFFERENT identity must be
// rejected — self-service never lets one member open another's account.
func TestConsumer_CreateAccountSelfScopeRejectedForOther(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledConsumerCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "selfledgerother")

	seedIdentity(t, ctx, conn, ledConsumerID)
	otherIdentityKey := seedIdentity(t, ctx, conn, "WLLEDCQNSOTHERHJKMNP")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("selfledgeracct00002"),
		Lane:          processor.LaneDefault,
		OperationType: "WellnessCreateAccount",
		Actor:         ledConsumerKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "wellnessaccount",
		Payload:       json.RawMessage(`{"identityKey":"` + otherIdentityKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{otherIdentityKey}},
		AuthContext:   &processor.AuthContext{Target: ledConsumerKey},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("self-service WellnessCreateAccount for another identity outcome = %v, want Rejected (AuthDenied)", got)
	}
}
