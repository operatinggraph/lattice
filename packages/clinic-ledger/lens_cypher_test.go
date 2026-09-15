package clinicledger

// Rule-engine proof of the clinicNoShowSettlement convergence lens, driven
// through the `full` engine (engine:"full") against an embedded NATS
// Core/Adjacency KV — the same harness cafe-domain / semantic-contracts /
// lease-signing use.
//
// The lens bills the fee's PRESENCE on the current .status, whoever wrote it,
// and reverses when the current status carries none:
//
//   - SCHEDULED: a scheduled appointment (no fee) never violates.
//   - NOSHOW_NO_FEE: a noShow appointment with no noShowFeeCents
//     (MarkPastDueNoShow's fee-less auto no-show) never violates.
//   - NOSHOW_NO_ACCOUNT: noShow, carries a fee, the patient has no
//     clinic-ledger account yet — missing_account true (Weaver opens one via
//     ClinicCreateAccount).
//   - NOSHOW_ACCOUNT_NO_CHARGE: noShow, carries a fee, account exists, no
//     clinictransaction settles this appointment yet — missing_charge true,
//     memo 'No-show fee'.
//   - LATE_CANCEL_ACCOUNT_NO_CHARGE: cancelled by the patient inside the
//     late-cancel window (lateCancel + the fee), account exists, no charge
//     yet — missing_charge true, memo 'Late-cancellation fee'.
//   - CANCELLED_NO_FEE: a fee-less cancel (staff, or the patient before the
//     window) never violates.
//   - NOSHOW_CHARGED: noShow, carries a fee, account exists, a
//     clinictransaction settles this appointment — converged.
//   - CORRECTED_NO_REVERSAL: a CorrectAppointmentStatus correction moved a
//     charged no-show to completed (the correction writes no fee), no credit
//     reverses the charge yet — missing_reversal true.
//   - LATE_CANCEL_WAIVED_NO_REVERSAL: a charged late cancel corrected to a
//     fee-less cancelled (the waiver), no credit yet — missing_reversal true.
//   - CORRECTED_WITH_REVERSAL: same, but a credit already reverses the
//     settling transaction — converged.
//   - NEVER_CHARGED_NO_REVERSAL: an appointment that never owed (no settles
//     link ever existed) never violates missing_reversal regardless of its
//     current status.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type clFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newClFixture(t *testing.T) *clFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &clFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *clFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

func (f *clFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[ownerName]] + "." + f.ids[ownerName]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *clFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// projectAt runs the anchored clinicNoShowSettlement spec for one appointment.
func (f *clFixture) projectAt(t *testing.T, apptName string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(noShowSettlementSpec)
	require.NoError(t, err, "clinicNoShowSettlement cypher must parse on the full engine")
	apptKey := "vtx.appointment." + f.ids[apptName]
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    apptKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkAppointment seeds one appointment forPatient a fresh patient, with the
// given status and (optional, nil to omit) noShowFeeCents.
func (f *clFixture) mkAppointment(t *testing.T, name string, status string, feeCents any) {
	t.Helper()
	f.vtx(t, name, "appointment")
	statusData := map[string]any{"value": status}
	if feeCents != nil {
		statusData["noShowFeeCents"] = feeCents
	}
	f.aspect(t, name, "status", "appointmentStatus", statusData)
	f.vtx(t, name+"_patient", "patient")
	f.edge(t, "forPatient", name, name+"_patient")
}

func TestClinicNoShowSettlement_ScheduledNotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "schedappt", "scheduled", nil)

	v := f.projectAt(t, "schedappt")[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["schedappt"], v["entityKey"])
	require.Equal(t, "scheduled", v["status"])
	require.Equal(t, false, v["missing_charge"], "not a noShow — never violates")
	require.Equal(t, false, v["violating"])
}

func TestClinicNoShowSettlement_NoShowNoFee_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "nofeeappt", "noShow", nil)

	v := f.projectAt(t, "nofeeappt")[0].Values
	require.Equal(t, "noShow", v["status"])
	require.Nil(t, v["feeCents"], "no noShowFeeCents set")
	require.Equal(t, false, v["missing_charge"], "no fee to charge — never violates")
	require.Equal(t, false, v["violating"])
}

func TestClinicNoShowSettlement_NoShowNoAccount_MissingAccount(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "noacctappt", "noShow", 2500.0)

	v := f.projectAt(t, "noacctappt")[0].Values
	require.Nil(t, v["accountKey"], "patient has no clinic-ledger account yet")
	require.Equal(t, true, v["missing_account"], "no account yet — Weaver opens one via ClinicCreateAccount")
	require.Equal(t, false, v["missing_charge"], "cannot charge before the account exists")
	require.Equal(t, true, v["violating"])
}

func TestClinicNoShowSettlement_NoShowWithAccountNoCharge_MissingCharge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "unchargedappt", "noShow", 2500.0)
	f.aspect(t, "unchargedappt_patient", "ledgerAccount", "clinicLedgerAccountGuard", map[string]any{"accountKey": "vtx.clinicaccount.BBFAKEACCTHJKMNPQRST"})
	f.vtx(t, "unchargedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "unchargedappt_acct", "unchargedappt_patient")

	v := f.projectAt(t, "unchargedappt")[0].Values
	require.Equal(t, "vtx.clinicaccount."+f.ids["unchargedappt_acct"], v["accountKey"])
	require.Equal(t, 2500.0, v["feeCents"])
	require.Equal(t, true, v["missing_charge"], "no clinictransaction settles this appointment yet — violating")
	require.Equal(t, "No-show fee", v["memo"], "the charge names what was billed")
	require.Equal(t, true, v["violating"])
}

// TestClinicNoShowSettlement_LateCancelWithAccountNoCharge_MissingCharge
// proves the lens bills the fee's presence, not the noShow status: a
// patient's own cancel inside the late-cancel window lands as
// {cancelled, lateCancel, noShowFeeCents} and is charged exactly as a
// staff-set no-show is, under its own memo.
func TestClinicNoShowSettlement_LateCancelWithAccountNoCharge_MissingCharge(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.vtx(t, "latecancelappt", "appointment")
	f.aspect(t, "latecancelappt", "status", "appointmentStatus", map[string]any{
		"value": "cancelled", "lateCancel": true, "noShowFeeCents": 2500.0,
	})
	f.vtx(t, "latecancelappt_patient", "patient")
	f.edge(t, "forPatient", "latecancelappt", "latecancelappt_patient")
	f.vtx(t, "latecancelappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "latecancelappt_acct", "latecancelappt_patient")

	v := f.projectAt(t, "latecancelappt")[0].Values
	require.Equal(t, "cancelled", v["status"])
	require.Equal(t, 2500.0, v["feeCents"], "the late cancel's fee, as the script wrote it")
	require.Equal(t, false, v["missing_account"], "the account exists")
	require.Equal(t, true, v["missing_charge"], "a late cancel owes its fee — no charge posted yet")
	require.Equal(t, false, v["missing_reversal"], "nothing charged, nothing to reverse")
	require.Equal(t, "Late-cancellation fee", v["memo"], "billed under its own name, not as a no-show")
	require.Equal(t, true, v["violating"])
}

// TestClinicNoShowSettlement_CancelledNoFee_NotViolating pins the staff
// cancel and the patient's own early cancel: a cancelled status carrying no
// fee owes nothing.
func TestClinicNoShowSettlement_CancelledNoFee_NotViolating(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "freecancelappt", "cancelled", nil)
	f.vtx(t, "freecancelappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "freecancelappt_acct", "freecancelappt_patient")

	v := f.projectAt(t, "freecancelappt")[0].Values
	require.Equal(t, "cancelled", v["status"])
	require.Nil(t, v["feeCents"])
	require.Equal(t, false, v["missing_account"])
	require.Equal(t, false, v["missing_charge"], "no fee on the status — nothing owed")
	require.Equal(t, false, v["missing_reversal"])
	require.Equal(t, false, v["violating"])
}

func TestClinicNoShowSettlement_NoShowCharged_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "chargedappt", "noShow", 2500.0)
	f.vtx(t, "chargedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "chargedappt_acct", "chargedappt_patient")
	f.vtx(t, "chargedappt_tx", "clinictransaction")
	f.edge(t, "settles", "chargedappt_tx", "chargedappt")

	v := f.projectAt(t, "chargedappt")[0].Values
	require.Equal(t, false, v["missing_charge"], "a clinictransaction settles this appointment — converged")
	require.Equal(t, false, v["missing_reversal"], "still noShow — nothing to reverse yet")
	require.Equal(t, false, v["violating"])
}

// TestClinicNoShowSettlement_CorrectedAwayNoReversal_MissingReversal proves
// the reversal gap: a CorrectAppointmentStatus correction moved the
// appointment to completed — the correction's own write carries no fee — a
// clinictransaction already settled it, and no credit yet reverses that
// transaction — missing_reversal true, and chargeTxKey/chargedAmountCents
// carry the settling transaction's own key/amount for Weaver to dispatch
// ClinicCreditAccount{reversesRef: chargeTxKey, amountCents: chargedAmountCents}.
func TestClinicNoShowSettlement_CorrectedAwayNoReversal_MissingReversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "correctedappt", "completed", nil)
	f.vtx(t, "correctedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "correctedappt_acct", "correctedappt_patient")
	f.vtx(t, "correctedappt_tx", "clinictransaction")
	f.edge(t, "settles", "correctedappt_tx", "correctedappt")
	f.aspect(t, "correctedappt_tx", "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 2500.0, "memo": "No-show fee", "postedAt": "2026-08-06T00:00:00Z",
	})

	v := f.projectAt(t, "correctedappt")[0].Values
	require.Equal(t, "completed", v["status"], "corrected off noShow")
	require.Equal(t, false, v["missing_charge"], "the original charge posted fine — not this gap")
	require.Equal(t, true, v["missing_reversal"], "corrected away with a live settled charge and no reversal yet")
	require.Equal(t, "vtx.clinictransaction."+f.ids["correctedappt_tx"], v["chargeTxKey"])
	require.Equal(t, 2500.0, v["chargedAmountCents"])
	require.Equal(t, true, v["violating"])
}

// TestClinicNoShowSettlement_LateCancelWaivedNoReversal_MissingReversal is
// the waiver: a charged late cancel (its status carried the fee when the
// charge posted) corrected to a fee-less cancelled — the current status
// still reads cancelled, but carries no fee — opens missing_reversal.
func TestClinicNoShowSettlement_LateCancelWaivedNoReversal_MissingReversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "waivedappt", "cancelled", nil)
	f.vtx(t, "waivedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "waivedappt_acct", "waivedappt_patient")
	f.vtx(t, "waivedappt_tx", "clinictransaction")
	f.edge(t, "settles", "waivedappt_tx", "waivedappt")
	f.aspect(t, "waivedappt_tx", "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 2500.0, "memo": "Late-cancellation fee", "postedAt": "2026-08-06T00:00:00Z",
	})

	v := f.projectAt(t, "waivedappt")[0].Values
	require.Equal(t, "cancelled", v["status"])
	require.Nil(t, v["feeCents"], "the waiver correction wrote no fee")
	require.Equal(t, false, v["missing_charge"])
	require.Equal(t, true, v["missing_reversal"], "a charged appointment whose status no longer carries a fee is owed its reversal")
	require.Equal(t, "vtx.clinictransaction."+f.ids["waivedappt_tx"], v["chargeTxKey"])
	require.Equal(t, 2500.0, v["chargedAmountCents"])
	require.Equal(t, true, v["violating"])
}

// TestClinicNoShowSettlement_NoShowNoFeeCharged_MissingReversal pins the
// lens law itself rather than a reachable path: no writer today produces a
// fee-less noShow over a charged appointment (SetAppointmentStatus and
// CorrectAppointmentStatus always write a fee onto noShow; MarkPastDueNoShow
// never overwrites a terminal row). It is the one shape where billing the
// fee's presence and billing the noShow status disagree — a status-keyed
// reversal clause would read this row as converged, so the pin fails if the
// clause ever regresses to `status <> 'noShow'`.
func TestClinicNoShowSettlement_NoShowNoFeeCharged_MissingReversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "nofeechargedappt", "noShow", nil)
	f.vtx(t, "nofeechargedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "nofeechargedappt_acct", "nofeechargedappt_patient")
	f.vtx(t, "nofeechargedappt_tx", "clinictransaction")
	f.edge(t, "settles", "nofeechargedappt_tx", "nofeechargedappt")
	f.aspect(t, "nofeechargedappt_tx", "entry", "transactionEntry", map[string]any{
		"type": "debit", "amountCents": 2500.0, "memo": "No-show fee", "postedAt": "2026-08-06T00:00:00Z",
	})

	v := f.projectAt(t, "nofeechargedappt")[0].Values
	require.Equal(t, "noShow", v["status"])
	require.Nil(t, v["feeCents"])
	require.Equal(t, false, v["missing_charge"])
	require.Equal(t, true, v["missing_reversal"], "the fee's absence, not the status, is what owes a reversal")
	require.Equal(t, true, v["violating"])
}

// TestClinicNoShowSettlement_CorrectedAwayWithReversal_Converged proves the
// gap closes, and stays closed, once a credit reverses the settling
// transaction — the same existence-idempotency missing_charge already
// relies on for the original charge.
func TestClinicNoShowSettlement_CorrectedAwayWithReversal_Converged(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "reversedappt", "cancelled", nil)
	f.vtx(t, "reversedappt_acct", "clinicaccount")
	f.edge(t, "heldFor", "reversedappt_acct", "reversedappt_patient")
	f.vtx(t, "reversedappt_tx", "clinictransaction")
	f.edge(t, "settles", "reversedappt_tx", "reversedappt")
	f.vtx(t, "reversedappt_credit", "clinictransaction")
	f.edge(t, "reverses", "reversedappt_credit", "reversedappt_tx")

	v := f.projectAt(t, "reversedappt")[0].Values
	require.Equal(t, false, v["missing_reversal"], "a credit already reverses the settling transaction")
	require.Equal(t, false, v["violating"])
}

// TestClinicNoShowSettlement_ScheduledNeverChargedNoReversal proves an
// appointment that never owed (no settling transaction at all) never
// violates missing_reversal regardless of its current status — the gap
// requires a live settles link, not merely a fee-less status.
func TestClinicNoShowSettlement_ScheduledNeverChargedNoReversal(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkAppointment(t, "neverchargedappt", "cancelled", nil)

	v := f.projectAt(t, "neverchargedappt")[0].Values
	require.Equal(t, false, v["missing_reversal"], "no settles link ever existed — nothing to reverse")
	require.Equal(t, false, v["violating"])
}

// project runs the unanchored clinicLedgerHistory spec — the engine
// enumerates its own roots and no actorKey is supplied, mirroring
// cafe-ledger/lens_cypher_test.go's `project` helper.
func (f *clFixture) project(t *testing.T, specName, spec string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "%s cypher must parse on the full engine", specName)
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// mkPostedTransaction seeds the shape a committed DebitAccount/CreditAccount
// produces: a clinictransaction posted to a clinicaccount held for a patient.
func (f *clFixture) mkPostedTransaction(t *testing.T, prefix string, amountCents float64, memo string) {
	t.Helper()
	f.vtx(t, prefix+"_patient", "patient")
	f.vtx(t, prefix+"_acct", "clinicaccount")
	f.vtx(t, prefix+"_tx", "clinictransaction")
	f.edge(t, "heldFor", prefix+"_acct", prefix+"_patient")
	f.edge(t, "postedTo", prefix+"_tx", prefix+"_acct")
	f.aspect(t, prefix+"_tx", "entry", "transactionEntry", map[string]any{
		"type":        "debit",
		"amountCents": amountCents,
		"memo":        memo,
		"postedAt":    "2026-08-06T00:00:00Z",
	})
}

func TestClinicLedgerHistory_SettlesAppointment_ProjectsVisit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkPostedTransaction(t, "noshow", 2500, "No-show fee")
	f.vtx(t, "noshow_appt", "appointment")
	f.aspect(t, "noshow_appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-08-05T09:00:00Z"})
	f.edge(t, "settles", "noshow_tx", "noshow_appt")

	rows := f.project(t, "clinicLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["noshow_appt"], v["appointmentKey"],
		"the settles link ties this charge to the visit that caused it")
	require.Equal(t, "2026-08-05T09:00:00Z", v["visitStartsAt"])
	require.Equal(t, true, v["settlesFee"], "a settles line IS the visit's fee")
	require.Nil(t, v["reversesKey"], "a debit reverses nothing")
}

// TestClinicLedgerHistory_ForVisit_ProjectsVisitNotFee: a charge posted with
// visitRef reaches the history through the forVisit hop — the same
// appointmentKey/visitStartsAt pair a settles line projects, coalesced from
// the other relation — and settlesFee false says the line is FOR the visit,
// not its fee.
func TestClinicLedgerHistory_ForVisit_ProjectsVisitNotFee(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkPostedTransaction(t, "visitcopay", 2500, "Office visit copay")
	f.vtx(t, "visitcopay_appt", "appointment")
	f.aspect(t, "visitcopay_appt", "schedule", "appointmentSchedule", map[string]any{"startsAt": "2026-08-07T10:00:00Z"})
	f.edge(t, "forVisit", "visitcopay_tx", "visitcopay_appt")

	rows := f.project(t, "clinicLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, "vtx.appointment."+f.ids["visitcopay_appt"], v["appointmentKey"],
		"the forVisit link names the visit the copay is for")
	require.Equal(t, "2026-08-07T10:00:00Z", v["visitStartsAt"])
	require.Equal(t, false, v["settlesFee"], "a forVisit line is for the visit, never its fee")
	require.Nil(t, v["reversesKey"])
}

// TestClinicLedgerHistory_Reverses_ProjectsReversesKey: a credit that
// reverses a charge (a clinicNoShowSettlement reversal, or a desk waiver that
// named the charge it forgives) projects the charge's key as reversesKey —
// the column a statement retires the named debit on before its FIFO.
func TestClinicLedgerHistory_Reverses_ProjectsReversesKey(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkPostedTransaction(t, "rev", 2500, "No-show fee")
	f.vtx(t, "rev_credit", "clinictransaction")
	f.edge(t, "postedTo", "rev_credit", "rev_acct")
	f.edge(t, "reverses", "rev_credit", "rev_tx")
	f.aspect(t, "rev_credit", "entry", "transactionEntry", map[string]any{
		"type": "credit", "amountCents": 2500.0, "memo": "Fee reversal (corrected)", "postedAt": "2026-08-08T00:00:00Z", "reason": "waiver",
	})

	rows := f.project(t, "clinicLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 2)
	byKey := map[string]map[string]any{}
	for _, r := range rows {
		byKey[r.Values["key"].(string)] = r.Values
	}
	credit := byKey["vtx.clinictransaction."+f.ids["rev_credit"]]
	require.NotNil(t, credit)
	require.Equal(t, "vtx.clinictransaction."+f.ids["rev_tx"], credit["reversesKey"],
		"the reverses link names the charge this credit gives back")
	require.Nil(t, credit["appointmentKey"])
	require.Equal(t, false, credit["settlesFee"], "a line naming no visit is nobody's fee")
	debit := byKey["vtx.clinictransaction."+f.ids["rev_tx"]]
	require.NotNil(t, debit)
	require.Nil(t, debit["reversesKey"], "the reversed charge itself reverses nothing — the hop is outbound only")
}

func TestClinicLedgerHistory_NoSettlesLink_ProjectsNullVisit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.mkPostedTransaction(t, "copay", 15000, "Copay")

	rows := f.project(t, "clinicLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Nil(t, v["appointmentKey"], "a copay settles no appointment — OPTIONAL MATCH leaves it null")
	require.Nil(t, v["visitStartsAt"])
	require.Equal(t, false, v["settlesFee"], "the engine answers (null <> null) with false, so settlesFee is a plain boolean on every line")
	require.Nil(t, v["reversesKey"])
}

// TestClinicLedgerHistory_ProjectsWaiverReason proves the lens surfaces a
// credit entry's reason column — the field a reader needs to tell a waived
// charge apart from cash actually collected, since both reduce the derived
// balance identically.
func TestClinicLedgerHistory_ProjectsWaiverReason(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newClFixture(t)
	f.vtx(t, "waiv_patient", "patient")
	f.vtx(t, "waiv_acct", "clinicaccount")
	f.vtx(t, "waiv_tx", "clinictransaction")
	f.edge(t, "heldFor", "waiv_acct", "waiv_patient")
	f.edge(t, "postedTo", "waiv_tx", "waiv_acct")
	f.aspect(t, "waiv_tx", "entry", "transactionEntry", map[string]any{
		"type":        "credit",
		"amountCents": 2500.0,
		"memo":        "Waived — patient hardship",
		"postedAt":    "2026-08-06T00:00:00Z",
		"reason":      "waiver",
	})

	rows := f.project(t, "clinicLedgerHistory", ledgerHistorySpec)
	require.Len(t, rows, 1)
	require.Equal(t, "waiver", rows[0].Values["reason"])
}
