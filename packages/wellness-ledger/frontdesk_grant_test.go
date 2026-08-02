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
)

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
