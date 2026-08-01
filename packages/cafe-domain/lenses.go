package cafedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// TabSettlementTarget is the §10.8 TargetID == the cafeTabSettlement lens's
// OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const TabSettlementTarget = "cafeTabSettlement"

// LeaseWorkplacesBucket is the NATS-KV read model the cafeLeaseWorkplaces
// lens projects into — one row per lease, carrying the set of locations that
// COVER it. It is the P5 query surface for the one question every café staff
// read has to answer before returning a row: "does this caller's workplace
// reach this lease." The Refractor auto-creates the bucket on lens load.
const LeaseWorkplacesBucket = "cafe-lease-workplaces"

// MenuCatalogBucket is the NATS-KV read model the menuCatalog lens projects
// into — the P5 query surface for "what can a resident self-order": an
// application reads THIS projected bucket (one entry per live menuItem,
// keyed by the item's key), never Core KV (lattice-architecture.md P5 —
// lenses are the only application query surface).
const MenuCatalogBucket = "cafe-menu-catalog"

// Lenses returns the package's Lens declarations: the `cafeTabSettlement`
// actorAggregate convergence lens (§10.2) anchored on tab, the plain
// `menuCatalog` projection (mirrors loftspace-domain's availableListings)
// listing every live menuItem for the Resident view's self-order picker, and
// `cafeLeaseWorkplaces`, the read-side half of workplace confinement.
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
				BodyColumns:      []string{"violating", "missing_account", "missing_charge", "entityKey", "tabKey", "leaseAppKey", "accountKey", "totalCents", "itemsMemo", "status", "openedAt", "settledAt"},
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
		{
			CanonicalName: "cafeLeaseWorkplaces",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LeaseWorkplacesBucket,
			Engine:        "full",
			Spec:          leaseWorkplacesSpec,
		},
		{
			// cafeIdentitiesRead — the protected Postgres identity-name lens
			// closing the cross-vertical "Signed in as <NanoID>" gap
			// (verticals.md): cafe-app has no roster of named identities to
			// resolve the signed-in actor's own name against, so it falls back
			// to printing the raw key. NAME ONLY, mirroring
			// loftspace-domain's applicantRosterRead SECURE LENS (Contract #3
			// §3.10) — the identity `name` is a sensitive aspect, so Core KV
			// holds only its ciphertext envelope, and the cypher RETURNs the
			// envelope whole for Refractor to decrypt at projection time.
			//
			// SELF-ANCHORED, unlike applicantRosterRead's empty/wildcard-only
			// set: each row's authz_anchors carries the identity's OWN bare
			// NanoID, so the platform's base cap-read self-grant
			// (CapabilityReadGrantsLensDefinition, every actor's
			// actor_id==anchor_id=='s own key) lets a signed-in resident or
			// staffer read their OWN row with no extra grant declaration —
			// the landlordUnitsRead idiom (loftspace-domain/lenses.go), not
			// clinic's indirect two-lens patientIdentityReadGrants (there is
			// no business vertex between the row and the login identity to
			// route through — the anchor IS the identity). Each row ALSO
			// carries every workplace building that covers the identity's own
			// lease (applicationFor -> appliesToUnit -> containedIn*0..7,
			// mirroring cafeLeaseWorkplaces' own walk and clinicPatientsRead's
			// practicesAt fan-out), so a worksAt-anchored front-desk actor
			// (service-location's cap-read.staff grant) can resolve the name
			// of every resident whose lease their workplace covers, not only
			// themselves. A staffer holding the reserved WildcardAnchor grant
			// still reads every row.
			CanonicalName: "cafeIdentitiesRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_cafe_identities",
			Engine:        "full",
			Spec:          cafeIdentitiesReadSpec,
			Protected:     true,
			IntoKey:       []string{"identity_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "identity_key", Type: "text"},
				{Name: "name", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "name", IdentityKeyColumn: "identity_key", Field: "value"},
			},
		},
	}
}

// leaseWorkplacesSpec projects one row per lease carrying coveringLocations —
// every location that COVERS the lease: its applied-to unit plus each of that
// unit's `containedIn` ancestors. It is the read-side mirror of this package's
// own write-side walk (facet-staff-worlds-design.md §9): `require_workplace`
// resolves a tab's location through `leaseapp_unit` and then walks upward from
// it with `worksAt_covers` (ddls.go), testing the actor's worksAt link at each
// level; this materializes that same chain per lease, so a staff read boundary
// gets the identical answer from a set intersection and needs no Core-KV read
// (P5). The two definitions belong in one package for exactly that reason —
// they are one rule, and a reader must be able to see both.
//
// The zero-hop lower bound is load-bearing: the depth-0 entry is the unit
// itself, so a staffer wired to the exact unit matches, not only one wired to
// the building above it. The upper bound is WORKPLACE_MAX_DEPTH - 1, not
// WORKPLACE_MAX_DEPTH, because the two sides count differently and the goal is
// that neither reaches a depth the other refuses: the Starlark walk runs
// `range(WORKPLACE_MAX_DEPTH)` testing depths 0..7, while `*0..N` here admits
// depths 0..N inclusive (the executor matches the zero-hop node and THEN runs
// hops 1..N). `*0..8` would therefore admit a staffer nine levels up whose
// writes require_workplace refuses.
//
// The list-comprehension form (lease-signing's authz_anchors idiom, mirrored
// by wellness-domain's own coveringLocations) keeps the row one-per-lease — an
// OPTIONAL MATCH on a multi-parent unit would fan the lease into several rows
// instead. The location nodes carry no label because a location is any vertex
// of class `location` whatever its type segment — a building, a floor, a unit
// — the same reason edge-manifest's workplace chains leave them bare; the
// labelled `(l:leaseapp)` head is what keeps the comprehension anchored rather
// than seeding the whole keyspace.
//
// A lease whose unit is unwired — or which has no appliesToUnit at all —
// projects an EMPTY set, which the boundary reads as "no workplace covers
// this" and denies. That is the same answer require_workplace gives an empty
// location_keys list, and it is why the column must be projected for every
// lease rather than only for the wired ones: an absent row and an empty set
// have to deny alike.
const leaseWorkplacesSpec = `MATCH (l:leaseapp)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  [(l)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

// menuCatalogSpec projects one row per live menuItem — a tombstoned item
// simply drops out of the MATCH, so RetireMenuItem needs no explicit filter
// here (mirrors loftspace-domain's availableListingsSpec). The per-row key
// column is `key` (the item key, the IntoKey default), so the read model is
// keyed by vtx.menuitem.<id>; `menuItemKey` repeats it in the body for the
// reader. `servedAt` carries the item's own serving-location key (OPTIONAL
// MATCH, so an item minted with no servedAt link still projects a row rather
// than dropping out) — the exact key `location_covers` (ddls.go) resolves via
// `menu_item_served_at` when a self-order Charge is bound, so a picker that
// filters on this column offers only what that same Charge would accept.
const menuCatalogSpec = `MATCH (m:menuitem)
OPTIONAL MATCH (m)-[:servedAt]->(loc)
RETURN
  m.key AS key,
  m.key AS menuItemKey,
  m.price.data.name AS name,
  m.price.data.priceCents AS priceCents,
  loc.key AS servedAt`

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
//     idiom). Weaver dispatches DebitAccount{accountKey, amountCents, memo, tabRef}
//     (cafe-ledger, targets.go) — the tabRef extension writes the settles
//     audit link this OPTIONAL MATCH walks, so once posted the gap converges
//     and stays converged (a tab is settled exactly once — Settle rejects a
//     second call with TabNotOpen — so there is no re-open path to guard,
//     unlike semantic-contracts' recurring-clause freshness lane).
//
// `itemsMemo` is a plain scalar pass-through of `t.status.data.itemsMemo`
// (ddls.go's tab DDL builds it, comma-joined, as Charge/VoidCharge run) —
// the row column targets.go's missing_charge Params templates straight into
// DebitAccount's memo field, and cmd/cafe-app renders the same column on the
// open-tab card, so what a resident sees ordering and what lands on the
// house-tab ledger entry are the identical string.
//
// A tab with totalCents=0 (opened and settled with nothing charged) never
// violates either gap — no house-tab posting is needed for a zero-amount
// visit.
//
// The lease hop is `chargedTo`, the tab's PERMANENT link, not the `openFor`
// one Settle retracts (ddls.go). Both gaps only ever open on a SETTLED tab, so
// a required match on a hop that disappears at settlement would project no row
// exactly when one is owed — and with EmptyBehavior "delete" the target row
// would vanish and Weaver would dispatch nothing. Which link this walks is
// therefore a money question, not a naming one.
const tabSettlementSpec = `MATCH (t:tab {key: $actorKey})
MATCH (t)-[:chargedTo]->(l:leaseapp)
OPTIONAL MATCH (t)<-[:settles]-(tx:cafetransaction)
WITH
  t.key AS entityKey,
  t.status.data.value AS status,
  t.status.data.totalCents AS totalCents,
  t.status.data.itemsMemo AS itemsMemo,
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
  itemsMemo,
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

// cafeIdentitiesReadSpec projects one row per NAMED identity — the roster
// cafe-app resolves the signed-in actor's own name against. The WHERE keeps
// only identities carrying a `.name` aspect via ciphertext presence
// (`i.name.data.ct <> null` — there is no plaintext `value` field at rest),
// mirroring loftspace-domain's applicantRosterReadSpec. authz_anchors carries
// the identity's OWN bare NanoID (see the Lenses() declaration above for why
// that self-anchor is the right shape here) PLUS the workplace fan-out below.
//
// The fan-out is a pattern COMPREHENSION anchored on `i` (clinicPatientsRead's
// own idiom, one hop further out: identity -> leaseapp -> unit -> building),
// not a separate MATCH: a MATCH binding the leaseapp/unit hops in the query
// body would fan an identity with multiple leases into one row per lease,
// colliding on the single-valued identity_id IntoKey. `applicationFor` runs
// leaseapp -> identity (Contract #1 §1.1: the later-arriving leaseapp is the
// source), so the walk reads `(i)<-[:applicationFor]-(l:leaseapp)`; the
// `*0..7` containedIn bound is the exact depth cafeLeaseWorkplacesSpec (this
// package) and worksAt_covers (ddls.go) both reach, so a front-desk actor's
// cap-read.staff grant (service-location's staffReadGrants, anchored on the
// building it worksAt) resolves the name of any resident whose lease that
// building covers — the roster gap the read-side lease-workplace lens didn't
// close, since /api/identities is a separate Postgres model, not the
// cafeLeaseWorkplaces NATS-KV bucket.
const cafeIdentitiesReadSpec = `MATCH (i:identity)
WHERE i.name.data.ct <> null
RETURN
  nanoIdFromKey(i.key)   AS identity_id,
  i.key                  AS identity_key,
  i.name.data            AS name,
  [nanoIdFromKey(i.key)] + [(i)<-[:applicationFor]-(l:leaseapp)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | nanoIdFromKey(c.key)]
                         AS authz_anchors
`
