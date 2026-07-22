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
// derivable), and clinicNoShowSettlement (the missing_charge convergence lens
// targets.go's WeaverTargets dispatches DebitAccount over). Prefixed like the
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
				BodyColumns:      []string{"violating", "missing_charge", "entityKey", "appointmentKey", "patientKey", "accountKey", "feeCents", "status"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
	}
}

// noShowSettlementSpec is the one-row-per-appointment convergence cypher: a
// noShow appointment carrying a positive noShowFeeCents needs its charge
// posted onto the patient's clinic-ledger account, once. missing_charge only
// (no missing_account gap — see targets.go's doc comment): the appointment's
// patient must already have a clinicaccount, matching clinic's existing
// billing assumption.
//
//   - `missing_charge` — the appointment is a noShow, carries a fee, the
//     patient has a ledger account, and no clinictransaction `settles` this
//     appointment yet (count(tx.key) collapses the fan to a single existence
//     check — the objectLiveness/clauseSatisfaction idiom, same as
//     cafe-domain's cafeTabSettlement). Weaver dispatches
//     DebitAccount{accountKey, amountCents, appointmentRef} (targets.go) —
//     the appointmentRef extension writes the settles audit link this
//     OPTIONAL MATCH walks, so once posted the gap converges and stays
//     converged (noShow is a terminal status — SetAppointmentStatus rejects
//     transitioning away from it — so there is no re-open path to guard).
//
// An appointment whose patient has no clinicaccount yet never violates
// (accountKey null); one with no noShowFeeCents (a noShow set before this
// lens existed) never violates either — both are non-goals for v1, not a
// gap this lens is meant to converge.
const noShowSettlementSpec = `MATCH (appt:appointment {key: $actorKey})
MATCH (appt)-[:forPatient]->(pt:patient)
OPTIONAL MATCH (pt)<-[:heldFor]-(a:clinicaccount)
OPTIONAL MATCH (appt)<-[:settles]-(tx:clinictransaction)
WITH
  appt.key AS entityKey,
  appt.status.data.value AS status,
  appt.status.data.noShowFeeCents AS feeCents,
  pt.key AS patientKey,
  a.key AS accountKey,
  count(tx.key) AS txCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS appointmentKey,
  patientKey,
  accountKey,
  feeCents,
  status,
  ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_charge,
  ((status = 'noShow') AND (feeCents <> null) AND (feeCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS violating
`

// ledgerHistorySpec projects one row per transaction, walking postedTo to the
// account and heldFor to the patient so the FE can filter/group by patientKey
// with no extra hop. Every MATCH is REQUIRED (not OPTIONAL): a transaction
// projects a row only when it is genuinely posted to a live account held for a
// live patient (the normal shape every DebitAccount/CreditAccount commit
// produces). The per-row key is the transaction key (the IntoKey default), so
// the read model is keyed by vtx.clinictransaction.<id>; transactionKey
// repeats it in the body for the reader.
const ledgerHistorySpec = `MATCH (t:clinictransaction)
MATCH (t)-[:postedTo]->(a:clinicaccount)
MATCH (a)-[:heldFor]->(pt:patient)
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
  t.entry.data.expectedReimbursementCents AS expectedReimbursementCents`

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
