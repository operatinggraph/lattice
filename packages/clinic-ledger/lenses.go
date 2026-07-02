package clinicledger

import "github.com/asolgan/lattice/internal/pkgmgr"

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

// Lenses returns the package's Lens declarations: clinicLedgerHistory (one row
// per posted transaction, flattening the .entry aspect + the account/patient
// it posted to into a query-optimized read-model row — the FE derives a
// running balance client-side by summing amountCents, positive for debit,
// negative for credit, over rows for a given patientKey/accountKey; the
// ledger itself never stores a mutable running total) and
// clinicPatientAccounts (the patient -> account key lookup, since the account
// key is no longer derivable). Prefixed like the package's DDLs (ddls.go): a
// Lens canonicalName is global across every installed package, and
// loftspace-ledger already owns the bare `ledgerHistory` name.
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
	}
}

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
  t.entry.data.postedAt AS postedAt`

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
