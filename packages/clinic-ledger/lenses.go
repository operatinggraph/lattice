package clinicledger

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// LedgerHistoryBucket is the NATS-KV read model the ledgerHistory lens projects
// into. It is the **P5 query surface** for "what charges/payments has this
// patient had": the billing-history FE reads THIS projected bucket (one entry
// per transaction, keyed by the transaction key), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const LedgerHistoryBucket = "clinic-ledger-history"

// PatientAccountsBucket is the NATS-KV read model the clinicPatientAccounts
// lens projects into — one row per PATIENT (whether or not a ledger account
// has been opened yet), carrying the account's key when one exists. Since the
// account carries its own independently-minted NanoID (never derived from the
// patient's), the FE cannot compute an account key by string manipulation the
// way it once could — this lens is the P5 query surface for "does this
// patient have a ledger account, and what is its key."
const PatientAccountsBucket = "clinic-patient-accounts"

// NoShowSettlementTarget is the §10.8 TargetID == the clinicNoShowSettlement
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const NoShowSettlementTarget = "clinicNoShowSettlement"

// ArrearsRemindersTarget is the §10.8 TargetID == the clinicArrearsReminders
// lens's OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads, and
// the key the freshnessExpiry marker records this target's own fired timer
// under.
const ArrearsRemindersTarget = "clinicArrearsReminders"

// arrearsOp is the Weaver-dispatched arrears evaluation (accountDDLScript).
const arrearsOp = "EvaluateClinicArrears"

// Lenses returns the package's Lens declarations: clinicLedgerHistory (one row
// per posted transaction, flattening the .entry aspect + the account/patient
// it posted to into a query-optimized read-model row — the FE derives a
// running balance client-side by summing amountCents, positive for debit,
// negative for credit, over rows for a given patientKey/accountKey; the
// ledger itself never stores a mutable running total), clinicPatientAccounts
// (the patient -> account key lookup, since the account key is no longer
// derivable, plus the account's arrears due date and reminder timestamp for
// the patient's statement and the desk), clinicNoShowSettlement (the
// missing_account/missing_charge/missing_reversal convergence lens
// targets.go's WeaverTargets dispatches
// ClinicCreateAccount/DebitAccount/CreditAccount over), and
// clinicArrearsReminders (the one-row-per-account arrears convergence lens
// whose missing_evaluation gap dispatches EvaluateClinicArrears). Prefixed
// like the package's DDLs (ddls.go): a Lens canonicalName is global across
// every installed package, and loftspace-ledger already owns the bare
// `ledgerHistory` name.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "clinicLedgerHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LedgerHistoryBucket,
			Engine:        "full",
			Spec:          ledgerHistorySpec,
		},
		{
			CanonicalName: "clinicPatientAccounts",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        PatientAccountsBucket,
			Engine:        "full",
			Spec:          patientAccountsSpec,
		},
		{
			CanonicalName:  NoShowSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           noShowSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "appointment",
				OutputKeyPattern: NoShowSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_account", "missing_charge", "missing_reversal", "entityKey", "appointmentKey", "patientKey", "accountKey", "feeCents", "status", "memo", "chargeTxKey", "chargedAmountCents"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  ArrearsRemindersTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           arrearsRemindersSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "clinicaccount",
				OutputKeyPattern: ArrearsRemindersTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_evaluation", "entityKey", "freshUntil", "patientKey", "dueAt", "remindedFor", "reminderSentAt", "stale", "historyTooLong", "evaluatedAt"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
			},
		},
	}
}

// noShowSettlementSpec is the one-row-per-appointment convergence cypher: an
// appointment whose CURRENT .status carries a positive noShowFeeCents owes
// that fee, and it is posted onto the patient's clinic-ledger account once;
// an appointment whose current .status carries none but which a
// clinictransaction already settles is owed a reversal, once. The lens bills
// the fee's PRESENCE, whoever wrote it — SetAppointmentStatus(noShow), a
// patient's own late cancel (status cancelled with lateCancel + the fee), or
// a CorrectAppointmentStatus correction onto noShow — and reverses when the
// current status carries none: a correction to completed / cancelled, which
// is the waiver. MarkPastDueNoShow's deliberately fee-less noShow never
// owes. Three independent gaps, the first two mirroring cafe-domain's
// cafeTabSettlement (lenses.go there):
//
//   - `missing_account` — the appointment owes a fee and the patient has no
//     clinicaccount yet (accountKey null). Weaver dispatches
//     ClinicCreateAccount{patientKey} (targets.go), opening the account lazily
//     on the first charge rather than requiring it pre-exist.
//   - `missing_charge` — the appointment owes a fee, the patient has a ledger
//     account, and no clinictransaction `settles` this appointment yet
//     (count(tx.key) collapses the fan to a single existence check — the
//     objectLiveness/clauseSatisfaction idiom). Weaver dispatches
//     DebitAccount{accountKey, amountCents, appointmentRef, memo} (targets.go)
//     — the appointmentRef extension writes the settles audit link this
//     OPTIONAL MATCH walks, so once posted the gap converges and stays
//     converged. The memo names what was billed: 'Late-cancellation fee'
//     when the status is cancelled, 'No-show fee' otherwise.
//   - `missing_reversal` — a clinictransaction settles this appointment
//     (txCount = 1) but the appointment's CURRENT status carries no fee (a
//     correction moved it to completed / cancelled — the only way a fee-less
//     status and a live settles link coexist, since the charge is minted only
//     while the status carries the fee), and no credit yet `reverses` that
//     transaction (reversalCount = 0). Weaver dispatches
//     ClinicCreditAccount{accountKey, amountCents: chargedAmountCents,
//     reason: "waiver", reversesRef: chargeTxKey} (targets.go) — the
//     reversesRef extension writes the reverses audit link this OPTIONAL
//     MATCH walks, so once posted the gap converges and stays converged,
//     the same existence-idempotency missing_charge already relies on.
//     chargeTxKey/chargedAmountCents use max() rather than collect()+index
//     (unsupported by this engine) to pull the single settling transaction's
//     key/amount out of the aggregate — safe because missing_charge's own
//     txCount=0 gate never lets more than one live settles link exist. A
//     same-value re-set with a different fee stands at the posted amount:
//     amount drift is not a gap.
//
// Once missing_account converges (ClinicCreateAccount writes the patient's
// .ledgerAccount guard aspect), the next lens tick reads the now-real
// accountKey and missing_charge takes over — the same lazy account-open
// relay cafeTabSettlement uses.
const noShowSettlementSpec = `MATCH (appt:appointment {key: $actorKey})
MATCH (appt)-[:forPatient]->(pt:patient)
OPTIONAL MATCH (pt)<-[:heldFor]-(a:clinicaccount)
OPTIONAL MATCH (appt)<-[:settles]-(tx:clinictransaction)
OPTIONAL MATCH (tx)<-[:reverses]-(credit:clinictransaction)
WITH
  appt.key AS entityKey,
  appt.status.data.value AS status,
  appt.status.data.noShowFeeCents AS feeCents,
  pt.key AS patientKey,
  a.key AS accountKey,
  count(DISTINCT tx.key) AS txCount,
  max(tx.key) AS chargeTxKey,
  max(tx.entry.data.amountCents) AS chargedAmountCents,
  count(DISTINCT credit.key) AS reversalCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS appointmentKey,
  patientKey,
  accountKey,
  feeCents,
  status,
  chargeTxKey,
  chargedAmountCents,
  (CASE WHEN status = 'cancelled' THEN 'Late-cancellation fee' ELSE 'No-show fee' END) AS memo,
  ((feeCents <> null) AND (feeCents > 0) AND (accountKey = null)) AS missing_account,
  ((feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_charge,
  (((feeCents = null) OR (feeCents <= 0)) AND (txCount = 1) AND (reversalCount = 0)) AS missing_reversal,
  (
    ((feeCents <> null) AND (feeCents > 0) AND (accountKey = null))
    OR ((feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0))
    OR (((feeCents = null) OR (feeCents <= 0)) AND (txCount = 1) AND (reversalCount = 0))
  ) AS violating
`

// ledgerHistorySpec projects one row per transaction, walking postedTo to the
// account and heldFor to the patient so the FE can filter/group by patientKey
// with no extra hop. Every MATCH is REQUIRED (not OPTIONAL): a transaction
// projects a row only when it is genuinely posted to a live account held for a
// live patient (the normal shape every DebitAccount/CreditAccount commit
// produces). The per-row key is the transaction key (the IntoKey default), so
// the read model is keyed by vtx.clinictransaction.<id>; transactionKey
// repeats it in the body for the reader.
//
// The three hops below the required pair are OPTIONAL because most
// transactions carry none of them:
//   - settles (appt): the line IS the fee this appointment's status carries —
//     a clinicNoShowSettlement-dispatched debit (targets.go's appointmentRef
//     param). The link exists for noShowSettlementSpec's own convergence
//     check above; projecting it here is what ties an otherwise identical
//     "No-show fee" line to the visit that caused it.
//   - forVisit (v): the line is FOR this visit — a desk copay or procedure
//     charge posted with visitRef. No convergence lens reads it.
//   - reverses (rt): a credit that gives back one named charge — a
//     clinicNoShowSettlement reversal, or a desk waiver that names the charge
//     it forgives. reversesKey is the column a statement ages on (the same
//     name café's and wellness's histories project).
//
// A line has at most one visit, so the two visit hops coalesce into one
// appointmentKey/visitStartsAt pair and settlesFee says which relation
// supplied it: true when the line is the visit's fee, false otherwise (the
// engine answers `null <> null` with false, so a line naming no visit at all
// reads false too, never null).
const ledgerHistorySpec = `MATCH (t:clinictransaction)
MATCH (t)-[:postedTo]->(a:clinicaccount)
MATCH (a)-[:heldFor]->(pt:patient)
OPTIONAL MATCH (t)-[:settles]->(appt:appointment)
OPTIONAL MATCH (t)-[:forVisit]->(v:appointment)
OPTIONAL MATCH (t)-[:reverses]->(rt:clinictransaction)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  pt.key AS patientKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  t.entry.data.billedTo AS billedTo,
  t.entry.data.expectedReimbursementCents AS expectedReimbursementCents,
  t.entry.data.reason AS reason,
  coalesce(appt.key, v.key) AS appointmentKey,
  coalesce(appt.schedule.data.startsAt, v.schedule.data.startsAt) AS visitStartsAt,
  (appt.key <> null) AS settlesFee,
  rt.key AS reversesKey`

// patientAccountsSpec projects one row per patient — the anchor is the
// patient (not the account), so a patient with no ledger account yet still
// gets a row (accountKey null), which is exactly the "has this patient
// opened an account" query the FE needs before its first-ever charge or
// payment. OPTIONAL MATCH: the heldFor hop legitimately has no match for a
// patient who has never had a charge/payment.
//
// The three arrears columns come off the account's own .arrears aspect and are
// INFORMATIONAL — this lens drives no convergence. They are here because the
// desk's arrears grid, the appointment card and the patient's statement all
// need to say WHEN a reminder went out, and this is already the per-patient
// row both app handlers read; the alternative was a second bucket keyed by
// account for three scalars. They are null for a patient with no account, and
// for an account nothing has yet aged.
const patientAccountsSpec = `MATCH (pt:patient)
OPTIONAL MATCH (pt)<-[:heldFor]-(a:clinicaccount)
RETURN
  pt.key AS key,
  pt.key AS patientKey,
  a.key AS accountKey,
  a.arrears.data.dueAt AS arrearsDueAt,
  a.arrears.data.remindedFor AS arrearsRemindedFor,
  a.arrears.data.sentAt AS arrearsReminderSentAt`

// arrearsRemindersSpec is the one-row-per-account arrears convergence cypher —
// the cafeArrearsReminders mechanism (cafe-ledger/lenses.go) applied to a
// patient's clinic account: freshUntil arms Weaver's @at temporal timer
// (internal/weaver/temporal.go) at a deadline, the fired timer's MarkExpired
// records that lapse under THIS target's own byTarget key on the account, and
// the recorded lapse — not a clock — is what opens the gap.
//
// The lifecycle of one arrears episode:
//
//   - A charge posted to an account that owed nothing stamps
//     .arrears.dueAt = that charge's postedAt + the ledger's net term
//     (post_entry, scripts.go — a RECORDED time fact, written by the op). While
//     no timer has fired at that deadline the row projects freshUntil = dueAt →
//     Weaver arms an @at there. missing_evaluation is false.
//   - At dueAt the @at fires → MarkExpired's freshnessExpiry marker on this
//     account records the fired instant under this target's key AND re-projects
//     the row → the recorded lapse now reaches dueAt → missing_evaluation flips
//     true and freshUntil goes null (a one-shot wake-up, not re-armed).
//   - Weaver dispatches directOp(EvaluateClinicArrears) — driven by the
//     violating row, not by a timer. The op recomputes the FIFO head over the
//     account's own history and stamps .arrears.remindedFor = the due date it
//     reminded for, alongside the notification it fires → re-projection →
//     remindedFor = dueAt → missing_evaluation false, freshUntil null.
//     Converged, and no second reminder for this episode however many times
//     the row is re-evaluated.
//   - A partial payment can move the FIFO head to a LATER charge, which no
//     single entry can compute; post_entry marks .arrears.stale instead, which
//     opens the gap directly (no timer involved) so the evaluation recomputes.
//     Its rewrite drops stale, and the recomputed dueAt — later than the one the
//     lapse was recorded at — re-arms freshUntil with no clearing write at all.
//   - A payment that clears the balance rewrites .arrears to {evaluatedAt}
//     alone: no dueAt, so no timer and no gap. The NEXT charge opens a fresh
//     episode whose dueAt is necessarily later than any instant already
//     recorded (its own postedAt is later than the last episode's, and both add
//     the same term), so the permanent marker never poisons it.
//   - An account nothing has ever evaluated projects evaluatedAt = null and is
//     violating from its first projection — which is exactly how the accounts
//     standing at install get their first evaluation, one op each, then quiet.
//   - An account whose transaction history outran the op's replay budget carries
//     historyTooLong, and it suppresses BOTH the gap and the timer. That pairing
//     is the point: the op cannot compute a head for such an account, so a gap
//     that stayed open would have Weaver re-dispatch the same doomed evaluation
//     on every window with nothing sent and nothing said, and a timer armed at a
//     dueAt no evaluation could confirm would fire against a head nobody knows.
//     Quiet, but VISIBLE — the row stays in the weaver-targets bucket carrying
//     the flag, which is the operator's signal. The next posted entry drops the
//     flag (post_entry's carry) and sets stale, buying exactly one more attempt.
//
// missing_evaluation's third arm carries no `dueAt <> null` conjunct. It would be
// dead: the arm's own byTarget >= dueAt comparison is already false on a null
// dueAt (a null operand makes the range test false, never true), so nothing
// reaches that arm without a recorded due date. freshUntil KEEPS its null test —
// there the comparison it guards is negated, and NOT(false) is true.
//
// The lens reads NO clock. Both operands of every comparison are stored graph
// data, so the row is a pure function of the subgraph and two projections at
// different wall-clock instants over the same graph agree.
//
// One row per anchor: heldFor is 0..1 (ClinicCreateAccount writes exactly one,
// guarded create-only by the patient's .ledgerAccount aspect), so the OPTIONAL
// walk cannot fan out — and it is OPTIONAL, so an account with no patient still
// projects a row, with a null patientKey. patientKey, dueAt, remindedFor,
// reminderSentAt, stale, historyTooLong and evaluatedAt are INFORMATIONAL
// columns (operator observability in the weaver-targets read model; the
// playbook never routes patientKey as a param — the op resolves the patient
// itself); only entityKey + freshUntil + the two bools are load-bearing for
// Weaver's dispatch and temporal lanes.
//
// Built with fmt.Sprintf so the target id comes from the constant the
// WeaverTargetSpec uses, which puts this Spec out of lint-lens-anchors' static
// reach; its advisory asks for a hand check for a narrowing range bound inside
// a NEGATED pattern, and there is none — the cypher has no negated relationship
// pattern at all, only scalar NOT comparisons.
var arrearsRemindersSpec = fmt.Sprintf(`MATCH (a:clinicaccount {key: $actorKey})
OPTIONAL MATCH (a)-[:heldFor]->(pt:patient)
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  pt.key AS patientKey,
  a.arrears.data.dueAt AS dueAt,
  a.arrears.data.remindedFor AS remindedFor,
  a.arrears.data.sentAt AS reminderSentAt,
  a.arrears.data.stale AS stale,
  a.arrears.data.historyTooLong AS historyTooLong,
  a.arrears.data.evaluatedAt AS evaluatedAt,
  CASE WHEN (a.arrears.data.dueAt <> null) AND (a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND NOT (a.arrears.data.stale = true) AND NOT (a.arrears.data.historyTooLong = true) AND NOT (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt) THEN a.arrears.data.dueAt ELSE null END AS freshUntil,
  (
    NOT (a.arrears.data.historyTooLong = true)
    AND (
      (a.arrears.data.evaluatedAt = null)
      OR (a.arrears.data.stale = true)
      OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
    )
  ) AS missing_evaluation,
  (
    NOT (a.arrears.data.historyTooLong = true)
    AND (
      (a.arrears.data.evaluatedAt = null)
      OR (a.arrears.data.stale = true)
      OR ((a.arrears.data.remindedFor <> a.arrears.data.dueAt) AND (a.freshnessExpiry.data.byTarget.%[1]s >= a.arrears.data.dueAt))
    )
  ) AS violating`, ArrearsRemindersTarget)
