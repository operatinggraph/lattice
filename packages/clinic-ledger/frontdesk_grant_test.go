package clinicledger_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// Front-desk unconfined grant for ClinicCreateAccount (verticals.md —
// "CreateAccount can't be called from any of the 4 ledger apps' browsers").
// DebitAccount/CreditAccount stay operator-only — a billing act submitted by
// the trusted-tool app, not the browser. A clinicaccount is anchored on a
// patient, which carries no building, so there is nothing left to
// workplace-confine — mirrors wellness-ledger's identical
// WellnessCreateAccount fix and clinic-domain's
// TestFrontDesk_RegisterPatientUnconfined.
const (
	ledFDActorID  = "CLLEDFDACTRHJKMNPQRS"
	ledFDActorKey = "vtx.identity." + ledFDActorID
	ledFDCapKey   = "cap.identity." + ledFDActorID

	ledNoGrantActorID  = "CLLEDNGRANTHJKMNPQRS"
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
			{OperationType: "ClinicCreateAccount", Scope: "any"},
			{OperationType: "CreatePatient", Scope: "any"},
		},
		ServiceAccess:   []processor.ServiceAccessEntry{},
		EphemeralGrants: []processor.EphemeralGrant{},
		Roles:           []string{"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")},
	}
}

// TestFrontDesk_ClinicCreateAccountUnconfined is the Inc guarantee: a
// front-desk actor holding only the ClinicCreateAccount grant (no operator
// role) can open a patient's ledger account — the call clinic-app's billing
// view needs before it can show anything but "no account yet".
func TestFrontDesk_ClinicCreateAccountUnconfined(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	testutil.SeedCapDoc(t, ctx, conn, ledFDCapDoc())
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledger")

	patientReqID := testutil.GenReqID("fdledgerpatient00001")
	patientEnv := &processor.OperationEnvelope{
		RequestID:     patientReqID,
		Lane:          processor.LaneDefault,
		OperationType: "CreatePatient",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "patient",
		Payload:       json.RawMessage(`{"fullName":"Front Desk Test Patient"}`),
	}
	testutil.PublishOp(t, conn, patientEnv)
	testutil.DriveOne(t, ctx, cp, cons, processor.OutcomeAccepted)
	patientKey := "vtx.patient." + nanoIDFromRequestID(patientReqID)

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdledgeracct0000001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreateAccount",
		Actor:         ledFDActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeAccepted {
		t.Fatalf("front-desk ClinicCreateAccount = %v, want Accepted (unconfined grant)", got)
	}
}

// TestFrontDesk_ClinicCreateAccountDeniedWithoutIt proves the grant is what
// changed: an actor with no platform permission at all is still denied.
func TestFrontDesk_ClinicCreateAccountDeniedWithoutIt(t *testing.T) {
	ctx, conn := setupLedgerEnv(t)
	cp, cons := newLedgerPipeline(t, ctx, conn, "fdledgerdenied")

	patientKey := createPatient(t, ctx, conn, cp, cons, "fdlnograntpatient001", "Denied Patient")

	env := &processor.OperationEnvelope{
		RequestID:     testutil.GenReqID("fdlnograntacct00001"),
		Lane:          processor.LaneDefault,
		OperationType: "ClinicCreateAccount",
		Actor:         ledNoGrantActorKey,
		SubmittedAt:   "2026-07-01T12:00:00Z",
		Class:         "clinicaccount",
		Payload:       json.RawMessage(`{"patientKey":"` + patientKey + `"}`),
		ContextHint:   &processor.ContextHint{Reads: []string{patientKey}},
	}
	testutil.PublishOp(t, conn, env)
	if got := testutil.DriveOne(t, ctx, cp, cons, ""); got != processor.OutcomeRejected {
		t.Fatalf("ClinicCreateAccount from an actor with no ledger grant = %v, want Rejected", got)
	}
}
