package cafeledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// LedgerHistoryBucket is the NATS-KV read model the cafeLedgerHistory lens
// projects into. It is the **P5 query surface** for "what café charges/
// payments has this lease had": the house-tab-history FE reads THIS
// projected bucket (one entry per transaction, keyed by the transaction
// key), never Core KV (lattice-architecture.md P5 — lenses are the only
// application query surface). The Refractor auto-creates the bucket on lens
// load.
const LedgerHistoryBucket = "cafe-ledger-history"

// LeaseAccountsBucket is the NATS-KV read model the cafeLeaseAccounts lens
// projects into — one row per LEASE (whether or not a café account has been
// opened yet), carrying the account's key when one exists. Since the
// account carries its own independently-minted NanoID (never derived from
// the lease's), the FE cannot compute an account key by string manipulation
// — this lens is the P5 query surface for "does this lease have a café
// account, and what is its key."
const LeaseAccountsBucket = "cafe-lease-accounts"

// Lenses returns the package's Lens declarations: cafeLedgerHistory (one row
// per posted transaction, flattening the .entry aspect + the account/lease
// it posted to into a query-optimized read-model row — the FE derives a
// running balance client-side by summing amountCents, positive for debit,
// negative for credit, over rows for a given leaseAppKey/accountKey; the
// ledger itself never stores a mutable running total) and
// cafeLeaseAccounts (the lease -> account key lookup, since the account key
// is no longer derivable). Prefixed like the package's DDLs (ddls.go): a
// Lens canonicalName is global across every installed package, and
// loftspace-ledger already owns the bare `ledgerHistory` / `leaseAccounts`
// names.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "cafeLedgerHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LedgerHistoryBucket,
			Engine:        "full",
			Spec:          ledgerHistorySpec,
		},
		{
			CanonicalName: "cafeLeaseAccounts",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LeaseAccountsBucket,
			Engine:        "full",
			Spec:          leaseAccountsSpec,
		},
	}
}

// ledgerHistorySpec projects one row per transaction, walking postedTo to
// the account and heldFor to the leaseapp so the FE can filter/group by
// leaseAppKey with no extra hop. Every MATCH is REQUIRED (not OPTIONAL): a
// transaction projects a row only when it is genuinely posted to a live
// account held for a live lease (the normal shape every
// DebitAccount/CreditAccount commit produces). The per-row key is the
// transaction key (the IntoKey default), so the read model is keyed by
// vtx.cafetransaction.<id>; transactionKey repeats it in the body for the
// reader.
const ledgerHistorySpec = `MATCH (t:cafetransaction)
MATCH (t)-[:postedTo]->(a:cafeaccount)
MATCH (a)-[:heldFor]->(l:leaseapp)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  l.key AS leaseAppKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt`

// leaseAccountsSpec projects one row per lease — the anchor is the lease
// (not the account), so a lease with no café account yet still gets a row
// (accountKey null), which is exactly the "has this lease opened a café
// account" query the FE needs before its first-ever charge or payment.
// OPTIONAL MATCH: the heldFor hop legitimately has no match for a lease that
// has never had a café charge/payment.
const leaseAccountsSpec = `MATCH (l:leaseapp)
OPTIONAL MATCH (l)<-[:heldFor]-(a:cafeaccount)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  a.key AS accountKey`
