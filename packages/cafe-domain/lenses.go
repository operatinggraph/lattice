package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// TabSettlementTarget is the §10.8 TargetID == the cafeTabSettlement lens's
// OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const TabSettlementTarget = "cafeTabSettlement"

// MenuCatalogBucket is the NATS-KV read model the menuCatalog lens projects
// into — the P5 query surface for "what can a resident self-order": an
// application reads THIS projected bucket (one entry per live menuItem,
// keyed by the item's key), never Core KV (lattice-architecture.md P5 —
// lenses are the only application query surface).
const MenuCatalogBucket = "cafe-menu-catalog"

// Lenses returns the package's Lens declarations: the `cafeTabSettlement`
// actorAggregate convergence lens (§10.2) anchored on tab, and the plain
// `menuCatalog` projection (mirrors loftspace-domain's availableListings)
// listing every live menuItem for the Resident view's self-order picker.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  TabSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           tabSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "tab",
				OutputKeyPattern: TabSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_account", "missing_charge", "entityKey", "tabKey", "leaseAppKey", "accountKey", "totalCents", "status", "openedAt", "settledAt"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName: "menuCatalog",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        MenuCatalogBucket,
			Engine:        "full",
			Spec:          menuCatalogSpec,
		},
	}
}

// menuCatalogSpec projects one row per live menuItem — a tombstoned item
// simply drops out of the MATCH, so RetireMenuItem needs no explicit filter
// here (mirrors loftspace-domain's availableListingsSpec). The per-row key
// column is `key` (the item key, the IntoKey default), so the read model is
// keyed by vtx.menuitem.<id>; `menuItemKey` repeats it in the body for the
// reader.
const menuCatalogSpec = `MATCH (m:menuitem)
RETURN
  m.key AS key,
  m.key AS menuItemKey,
  m.price.data.name AS name,
  m.price.data.priceCents AS priceCents`

// tabSettlementSpec is the one-row-per-tab convergence cypher: a settled tab
// with a positive total needs its charge posted onto the resident's
// cafe-ledger account, in two independent gap columns, never both live at
// once for a given cause (missing_account clears the moment cafe-ledger's
// CreateAccount writes the leaseapp's .cafeLedgerAccount guard aspect,
// exposing missing_charge instead):
//
//   - `missing_account` — the tab is settled, owes money, and the leaseapp
//     has no café-ledger account yet (l.cafeLedgerAccount.data.accountKey is
//     null). Weaver dispatches CreateAccount{leaseAppKey} (cafe-ledger,
//     targets.go) — "opening one via CreateAccount on first use"
//     (cafe-ledger-design.md's Inc 2 note).
//   - `missing_charge` — the tab is settled, owes money, the account exists,
//     and no cafetransaction `settles` this tab yet (count(tx.key) collapses
//     the fan to a single existence check — the objectLiveness/clauseSatisfaction
//     idiom). Weaver dispatches DebitAccount{accountKey, amountCents, tabRef}
//     (cafe-ledger, targets.go) — the tabRef extension writes the settles
//     audit link this OPTIONAL MATCH walks, so once posted the gap converges
//     and stays converged (a tab is settled exactly once — Settle rejects a
//     second call with TabNotOpen — so there is no re-open path to guard,
//     unlike semantic-contracts' recurring-clause freshness lane).
//
// A tab with totalCents=0 (opened and settled with nothing charged) never
// violates either gap — no house-tab posting is needed for a zero-amount
// visit.
const tabSettlementSpec = `MATCH (t:tab {key: $actorKey})
MATCH (t)-[:openFor]->(l:leaseapp)
OPTIONAL MATCH (t)<-[:settles]-(tx:cafetransaction)
WITH
  t.key AS entityKey,
  t.status.data.value AS status,
  t.status.data.totalCents AS totalCents,
  t.status.data.openedAt AS openedAt,
  t.status.data.settledAt AS settledAt,
  l.key AS leaseAppKey,
  l.cafeLedgerAccount.data.accountKey AS accountKey,
  count(tx.key) AS txCount
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS tabKey,
  leaseAppKey,
  accountKey,
  totalCents,
  status,
  openedAt,
  settledAt,
  ((status = 'settled') AND (totalCents > 0) AND (accountKey = null)) AS missing_account,
  ((status = 'settled') AND (totalCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_charge,
  (
    ((status = 'settled') AND (totalCents > 0) AND (accountKey = null))
    OR ((status = 'settled') AND (totalCents > 0) AND (accountKey <> null) AND (txCount = 0))
  ) AS violating
`
