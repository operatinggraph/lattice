package clinicledger

import "github.com/asolgan/lattice/internal/pkgmgr"

// LedgerHistoryBucket is the NATS-KV read model the ledgerHistory lens projects
// into. It is the **P5 query surface** for "what charges/payments has this
// patient had": the billing-history FE reads THIS projected bucket (one entry
// per transaction, keyed by the transaction key), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const LedgerHistoryBucket = "clinic-ledger-history"

// Lenses returns the package's Lens declarations: clinicLedgerHistory, one row
// per posted transaction, flattening the .entry aspect + the account/patient it
// posted to into a query-optimized read-model row. The FE derives a running
// balance client-side by summing amountCents (positive for debit, negative for
// credit) over rows for a given patientKey/accountKey — the ledger itself
// never stores a mutable running total (append-only, no read-modify-write).
// Prefixed like the package's DDLs (ddls.go): a Lens canonicalName is global
// across every installed package, and loftspace-ledger already owns the bare
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
