package loftspaceledger

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// LedgerHistoryBucket is the NATS-KV read model the ledgerHistory lens projects
// into. It is the **P5 query surface** for "what charges/payments has this lease
// had": the payment-history FE reads THIS projected bucket (one entry per
// transaction, keyed by the transaction key), never Core KV
// (lattice-architecture.md P5 — lenses are the only application query surface).
// The Refractor auto-creates the bucket on lens load.
const LedgerHistoryBucket = "loftspace-ledger-history"

// LeaseAccountsBucket is the NATS-KV read model the leaseAccounts lens
// projects into — one row per LEASE (whether or not a ledger account has
// been opened yet), carrying the account's key when one exists. Since the
// account carries its own independently-minted NanoID (never derived from
// the lease's own), the FE cannot compute an account key by string
// manipulation the way it once could — this lens is the P5 query surface for
// "does this lease have a ledger account, and what is its key."
const LeaseAccountsBucket = "loftspace-lease-accounts"

// Lenses returns the package's Lens declarations: ledgerHistory (one row per
// posted transaction, flattening the .entry aspect + the account/lease it
// posted to into a query-optimized read-model row — the FE derives a running
// balance client-side by summing amountCents, positive for debit, negative
// for credit, over rows for a given leaseAppKey/accountKey; the ledger
// itself never stores a mutable running total) and leaseAccounts (the lease
// -> account key lookup, since the account key is no longer derivable).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "ledgerHistory",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LedgerHistoryBucket,
			Engine:        "full",
			Spec:          ledgerHistorySpec,
		},
		{
			CanonicalName: "leaseAccounts",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LeaseAccountsBucket,
			Engine:        "full",
			Spec:          leaseAccountsSpec,
		},
	}
}

// ledgerHistorySpec projects one row per transaction, walking postedTo to the
// account and heldFor to the lease so the FE can filter/group by leaseAppKey
// with no extra hop. Every MATCH is REQUIRED (not OPTIONAL): a transaction
// projects a row only when it is genuinely posted to a live account held for a
// live lease (the normal shape every DebitAccount/CreditAccount commit
// produces). The per-row key is the transaction key (the IntoKey default), so
// the read model is keyed by vtx.transaction.<id>; transactionKey repeats it in
// the body for the reader.
//
// The trailing OPTIONAL MATCH walks authorizedBy to a semantic-contracts clause
// (Fire V4 "why was I charged this?") — OPTIONAL because a plain human-
// submitted DebitAccount/CreditAccount carries no clauseRef, and this lens
// projects a row for every transaction regardless. No compile-time dependency
// on semantic-contracts: the cypher matches a vertex by class label at read
// time, same as any other package's lens matching a cross-package link.
const ledgerHistorySpec = `MATCH (t:transaction)
MATCH (t)-[:postedTo]->(a:account)
MATCH (a)-[:heldFor]->(l:leaseapp)
OPTIONAL MATCH (t)-[:authorizedBy]->(c:clause)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  l.key AS leaseAppKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  c.key AS clauseKey,
  c.prose.data.text AS clauseProse`

// leaseAccountsSpec projects one row per lease — the anchor is the leaseapp
// (not the account), so a lease with no ledger account yet still gets a row
// (accountKey null), which is exactly the "has this lease opened an
// account" query the FE needs before its first-ever charge or payment.
// OPTIONAL MATCH: the heldFor hop legitimately has no match for a lease that
// has never had a charge/payment.
const leaseAccountsSpec = `MATCH (l:leaseapp)
OPTIONAL MATCH (l)<-[:heldFor]-(a:account)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  a.key AS accountKey`
