package clinicledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

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

// Lenses returns the package's Lens declarations: clinicLedgerHistory (one row
// per posted transaction, flattening the .entry aspect + the account/patient
// it posted to into a query-optimized read-model row — the FE derives a
// running balance client-side by summing amountCents, positive for debit,
// negative for credit, over rows for a given patientKey/accountKey; the
// ledger itself never stores a mutable running total), clinicPatientAccounts
// (the patient -> account key lookup, since the account key is no longer
// derivable), and clinicNoShowSettlement (the missing_account/missing_charge/
// missing_reversal convergence lens targets.go's WeaverTargets dispatches
// ClinicCreateAccount/DebitAccount/CreditAccount over). Prefixed like the
// package's DDLs (ddls.go): a Lens canonicalName is global across every
// installed package, and loftspace-ledger already owns the bare
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
// The settles hop is OPTIONAL (unlike the two above) because most
// transactions — copays, payments — never settle an appointment; only a
// clinicNoShowSettlement-dispatched debit does (targets.go's
// appointmentRef param, written as the settles link). Surfacing
// appointmentKey/visitStartsAt here is what lets a reader tie an otherwise
// identical "No-show fee" line to the specific visit that caused it — the
// link already existed for noShowSettlementSpec's own convergence check
// (:105-ish above); this just also projects it into the history a patient
// or front-desk actually reads.
const ledgerHistorySpec = `MATCH (t:clinictransaction)
MATCH (t)-[:postedTo]->(a:clinicaccount)
MATCH (a)-[:heldFor]->(pt:patient)
OPTIONAL MATCH (t)-[:settles]->(appt:appointment)
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
  appt.key AS appointmentKey,
  appt.schedule.data.startsAt AS visitStartsAt`

// patientAccountsSpec projects one row per patient — the anchor is the
// patient (not the account), so a patient with no ledger account yet still
// gets a row (accountKey null), which is exactly the "has this patient
// opened an account" query the FE needs before its first-ever charge or
// payment. OPTIONAL MATCH: the heldFor hop legitimately has no match for a
// patient who has never had a charge/payment.
const patientAccountsSpec = `MATCH (pt:patient)
OPTIONAL MATCH (pt)<-[:heldFor]-(a:clinicaccount)
RETURN
  pt.key AS key,
  pt.key AS patientKey,
  a.key AS accountKey`
