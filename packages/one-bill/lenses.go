package onebill

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// HistoryBucket is the NATS-KV read model both lenses below project into — the
// **P5 query surface** for "every charge/payment across every ledger this
// lease holds," the Café Inc 3 "one-bill" payoff (café charges land on the
// same lease statement as rent). The Refractor auto-creates the bucket on
// lens load.
const HistoryBucket = "one-bill-history"

// Lenses returns the package's Lens declarations. The cypher engine does not
// support UNION (internal/refractor/ruleengine/full/visitor.go rejects any
// query with a oC_Union at parse time), and a Lens spec produces one RETURN
// shape (docs/contracts/06-capability-kv.md §"Lens" — multi-output patterns
// are additional Lenses, not Lens-internal complexity), so "unioning"
// loftspace-ledger's transactions with cafe-ledger's is two independently
// declared Lenses projecting into the SAME bucket — exactly the
// rbac-domain/service-location precedent of cap.roles.* and cap.svc.*
// sharing capability-kv. No collision is possible here even without extra
// key-namespacing: each lens's per-row key is the transaction's own vtx key
// (t.key), and vtx.transaction.<id> (rent) and vtx.cafetransaction.<id>
// (café) are already disjoint by vertex-type prefix.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "oneBillRentEntries",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        HistoryBucket,
			Engine:        "full",
			Spec:          rentEntriesSpec,
		},
		{
			CanonicalName: "oneBillCafeEntries",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        HistoryBucket,
			Engine:        "full",
			Spec:          cafeEntriesSpec,
		},
	}
}

// rentEntriesSpec re-projects loftspace-ledger's posted transactions (no
// compile-time dependency needed — the cypher matches vertex class labels at
// read time, same as loftspace-ledger's own OPTIONAL MATCH into
// semantic-contracts' :clause) tagged source:"rent", into the shared one-bill
// bucket. Every MATCH is REQUIRED: a row projects only for a transaction
// genuinely posted to a live account held for a live lease.
const rentEntriesSpec = `MATCH (t:transaction)
MATCH (t)-[:postedTo]->(a:account)
MATCH (a)-[:heldFor]->(l:leaseapp)
RETURN
  t.key AS key,
  t.key AS transactionKey,
  a.key AS accountKey,
  l.key AS leaseAppKey,
  t.entry.data.type AS type,
  t.entry.data.amountCents AS amountCents,
  t.entry.data.memo AS memo,
  t.entry.data.postedAt AS postedAt,
  'rent' AS source`

// cafeEntriesSpec re-projects cafe-ledger's posted transactions tagged
// source:"cafe", into the shared one-bill bucket. Mirrors rentEntriesSpec
// exactly, anchored on cafe-ledger's own vertex/link classes.
const cafeEntriesSpec = `MATCH (t:cafetransaction)
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
  t.entry.data.postedAt AS postedAt,
  'cafe' AS source`
