package cafedomain

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TabSettlementTarget is the §10.8 TargetID == the cafeTabSettlement lens's
// OutputKeyPattern prefix — the §10.2↔§10.8 binding Weaver reads.
const TabSettlementTarget = "cafeTabSettlement"

// StaleTabSettlementTarget is the §10.8 TargetID == the
// cafeStaleTabSettlement lens's OutputKeyPattern prefix — the §10.2↔§10.8
// binding Weaver reads (targets.go).
const StaleTabSettlementTarget = "cafeStaleTabSettlement"

// LeaseWorkplacesBucket is the NATS-KV read model the cafeLeaseWorkplaces
// lens projects into — one row per lease, carrying the set of locations that
// COVER it, plus the lease's own tenancy end (leaseEnd, and its recorded
// early end endedAt when a resident has given notice or the term has been
// closed out). It is the P5 query surface for the one question every café
// staff read has to answer before returning a row: "does this caller's
// workplace reach this lease" — and, via leaseEnd/endedAt, the surface
// cmd/cafe-app's own resident-readable /api/residents joins by leaseAppKey
// to give the resident's self-service Open Tab the same TenancyEnded
// courtesy the staff picker already has. The Refractor auto-creates the
// bucket on lens load.
const LeaseWorkplacesBucket = "cafe-lease-workplaces"

// MenuCatalogBucket is the NATS-KV read model the menuCatalog lens projects
// into — the P5 query surface for "what can a resident self-order": an
// application reads THIS projected bucket (one entry per live menuItem,
// keyed by the item's key), never Core KV (lattice-architecture.md P5 —
// lenses are the only application query surface).
const MenuCatalogBucket = "cafe-menu-catalog"

// HousePoliciesBucket is the NATS-KV read model the cafeHousePolicies lens
// projects into — one row per location carrying a .cafePolicy aspect
// (SetCafePolicy, ddls.go), keyed by the location's own key. It is the P5
// surface for the house's self-service tab limit: cmd/cafe-app composes a
// lease's effective limit as the MINIMUM over the lease's coveringLocations
// (cafeLeaseWorkplaces) that appear here — the same tightest-policy rule
// OpenTab / Charge apply on the resident-self leg — and the desk's Manage
// Menu panel reads and sets its own workplace's row.
const HousePoliciesBucket = "cafe-house-policies"

// Lenses returns the package's Lens declarations: the `cafeTabSettlement`
// actorAggregate convergence lens (§10.2) anchored on tab, the
// `cafeStaleTabSettlement` sibling convergence lens (also anchored on tab)
// that auto-chases a tab nobody ever settles, the plain `menuCatalog`
// projection (mirrors loftspace-domain's availableListings) listing every
// live menuItem for the Resident view's self-order picker, and
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
				BodyColumns:      []string{"violating", "missing_account", "missing_charge", "missing_payment", "entityKey", "tabKey", "leaseAppKey", "accountKey", "totalCents", "paidAtSettleCents", "itemsMemo", "lines", "status", "openedAt", "settledAt"},
				EmptyBehavior:    "delete",
				KeyColumn:        "entityId",
				Freshness:        "auto",
			},
		},
		{
			CanonicalName:  StaleTabSettlementTarget,
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "weaver-targets",
			Engine:         "full",
			Spec:           staleTabSettlementSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "tab",
				OutputKeyPattern: StaleTabSettlementTarget + ".{actorSuffix}",
				BodyColumns:      []string{"violating", "missing_settle", "missing_staleat", "entityKey", "tabKey", "status", "openedAt", "staleAt", "freshUntil", "maxretries_settle"},
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
			CanonicalName: "cafeHousePolicies",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        HousePoliciesBucket,
			Engine:        "full",
			Spec:          housePoliciesSpec,
		},
		{
			CanonicalName: "cafeLeaseWorkplaces",

			Class:   "meta.lens",
			Adapter: "nats-kv",
			Bucket:  LeaseWorkplacesBucket,
			Engine:  "full",
			Spec:    leaseWorkplacesSpec,
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
				{Column: "name", HolderTypes: []string{"identity"}, Field: "value"},
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
// by wellness-domain's own coveringLocations) keeps the row one-per-lease — a
// plain (non-comprehension) MATCH/OPTIONAL MATCH on a multi-parent unit's
// containedIn chain would fan the lease into several rows instead, one per
// ancestor. The single-hop `OPTIONAL MATCH (l)-[:appliesToUnit]->(u)` added
// below is exempt from that: appliesToUnit is 0..1 (lease-signing/lenses.go),
// so it binds at most one `u` per lease and adds no row of its own — only the
// `missingLocation` column.
//
// The location nodes carry no label because the chain must admit a
// location at ANY level — a building, a floor, a unit — and a bare node is the
// simplest way to say so, the same reason edge-manifest's workplace chains
// leave them bare. (`:location*`, the abstract label with the taxonomy sigil,
// would say it precisely, but it makes the walk depend on the taxonomy
// resolver being armed for no gain here.) The labelled `(l:leaseapp)` head is
// what keeps the comprehension anchored rather than seeding the whole
// keyspace.
//
// A lease whose unit is unwired — or which has no appliesToUnit at all —
// projects an EMPTY set, which the boundary reads as "no workplace covers
// this" and denies. That is the same answer require_workplace gives an empty
// location_keys list, and it is why the column must be projected for every
// lease rather than only for the wired ones: an absent row and an empty set
// have to deny alike.
//
// `leaseEnd` and `endedAt` carry the lease's own tenancy end off the SAME
// `.tenancy` aspect OpenTab's TenancyEnded guard reads (ddls.go) and
// front-desk's frontDeskLeaseDetails projects for staff
// (packages/front-desk/lenses.go, same source aspect, same shape — no
// coalesce, a lease approved before terms were minted projects null rather
// than dropping the row): endedAt is the recorded FACT the tenancy ended —
// set early on a resident's own notice, months before leaseEnd, or later
// when the term simply runs out — while leaseEnd stays the term's nominal
// end for a lease with no endedAt recorded yet. This is the resident-readable
// half of that fact: cmd/cafe-app's /api/residents (residents.go) joins this
// bucket onto its own roster by leaseAppKey so the resident's own
// self-service Open Tab can hide itself past the recorded tenancy end the
// same way fillLeaseSelect's tenancyEnded check already disables the option
// on the staff POS/front-desk picker, with no extra protected lens (the
// front-desk lens stays staff-only, gated at the HTTP handler).
//
// `missingLocation` (mirrors menuCatalogSpec's own flag below) distinguishes
// WHY the set is empty: `u.key = null` means the appliesToUnit target is gone
// (tombstoned out from under a still-live lease — the 2026-08-23 duplicate-
// listing reap did this to 11 café leases before the reap script's own
// live-tenancy guard existed, `9a3a7807`) or was never wired at all, either
// way a data gap rather than a genuine "no workplace" answer. The `(l:leaseapp)`
// head is lease-signing's shared type, not café-scoped — a tombstoned-unit gap
// on a LoftSpace/clinic/wellness lease projects missingLocation too, and reads
// visible to café front desk the identical way; that is intentional, not a
// leak, since a café tenant IS a loftspace tenant in the same building and
// staffCoveredLeases already crosses that boundary for the wired case (a
// staffer's workplace covers a lease's containedIn ancestors regardless of
// which vertical minted it). staffCoveredLeases still denies a missingLocation
// row on the per-workplace path — a data gap proves no MORE workplace access
// than an empty set did — but readauth.go's staffCoveredLeases additionally
// returns it in a SEPARATE unattributable set that every front-desk staffer
// sees regardless of workplace: no specific one can be blamed for the gap, so
// every workplace gets to see and escalate it, same as an operator would.
// This is a READ-side accommodation only; require_workplace's own write-side
// walk is untouched and independently refuses a Charge/Settle against the
// same dead unit, so broadening this set never grants a collection
// capability, only visibility.
const leaseWorkplacesSpec = `MATCH (l:leaseapp)
OPTIONAL MATCH (l)-[:appliesToUnit]->(u)
RETURN
  l.key AS key,
  l.key AS leaseAppKey,
  (u.key = null) AS missingLocation,
  l.tenancy.data.leaseEnd AS leaseEnd,
  l.tenancy.data.endedAt AS endedAt,
  [(l)-[:appliesToUnit]->(wu)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

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
//
// `coveringLocations` is the item's OWN ancestor chain — its servedAt
// location plus every containedIn ancestor, the exact leaseWorkplacesSpec
// shape (below) anchored on the item instead of a lease's unit. It is what
// lets the front-desk Manage Menu grid (cmd/cafe-app/menu.go's handleMenu,
// no leaseAppKey in view) confine itself to a staffer's own workplace the
// same way staffCoveredLeases confines /api/leases: intersect the caller's
// worksAt keys against this column rather than an exact-match on servedAt,
// so a staffer wired to the BUILDING still sees an item served at a UNIT
// inside it. The comprehension yields an empty list when `loc` never
// matched (no servedAt link) — an unlinked item is covered by nobody,
// mirroring leaseWorkplacesSpec's own empty-covering denial.
//
// `missingLocation` names the "outlived its place" gap explicitly, mirroring
// wellness-domain's wellnessSessionsSpec missingStudio: TombstoneLocation
// (location-domain) doesn't cascade onto a menu item's servedAt link, and an
// item minted with no locationKey is impossible today but not provable so
// from this lens — both land here as `loc.key = null`, which is also true
// the instant a rewire drops SetMenuItemLocation's old link and before it
// claims the new one, same as the OPTIONAL MATCH above. verticals.md "a menu
// item outlives the place that served it, with no flag and no way back" —
// SetMenuItemLocation (ddls.go) is the way back.
//
// `available` coalesces a missing .price.available field to true (the
// coalesce precedent below, tabSettlementSpec's own `l` column) — a live
// item nobody has toggled carries no field, and it projects available
// rather than an undecided null the picker would have to special-case.
const menuCatalogSpec = `MATCH (m:menuitem)
OPTIONAL MATCH (m)-[:servedAt]->(loc)
RETURN
  m.key AS key,
  m.key AS menuItemKey,
  m.price.data.name AS name,
  m.price.data.priceCents AS priceCents,
  coalesce(m.price.data.available, true) AS available,
  loc.key AS servedAt,
  (loc.key = null) AS missingLocation,
  [(m)-[:servedAt]->(sloc)-[:containedIn*0..7]->(c) | c.key] AS coveringLocations`

// housePoliciesSpec projects one row per location that records a café house
// policy — the .cafePolicy aspect SetCafePolicy writes (ddls.go). The head
// is `(loc:location*)`, the abstract label with the taxonomy sigil, so the
// row set admits a policy at any location level (unit / building / property,
// and any leaf a later package declares) without this lens naming the
// levels — the same reason service-location's capabilityServiceAccess uses
// it. The WHERE keeps only locations carrying the aspect: a location with no
// policy projects no row (absent = no limit recorded, the op's own reading),
// and a tombstoned aspect drops its row the way a tombstoned vertex drops out
// of the MATCH. `name` is the location's own .presentation display name
// (location-domain's SetLocationPresentation), null when never set — a
// label for the desk's panel, never a key.
const housePoliciesSpec = `MATCH (loc:location*)
WHERE loc.cafePolicy.class = 'cafeHousePolicy'
RETURN
  loc.key AS key,
  loc.key AS locationKey,
  loc.cafePolicy.data.tabLimitCents AS tabLimitCents,
  loc.presentation.data.name AS name`

// tabSettlementSpec is the one-row-per-tab convergence cypher: a settled tab
// with a positive total needs its charge posted onto the resident's
// cafe-ledger account — and, when the desk took cash at settle, that payment
// posted after it — in three gap columns, never two live at once for a given
// tab (missing_account clears the moment cafe-ledger's CreateAccount writes
// the leaseapp's .cafeLedgerAccount guard aspect, exposing missing_charge
// instead; missing_charge clears the moment the charge's settles link lands,
// exposing missing_payment instead):
//
//   - `missing_account` — the tab is settled, owes money, and the leaseapp
//     has no café-ledger account yet (l.cafeLedgerAccount.data.accountKey is
//     null). Weaver dispatches CreateAccount{leaseAppKey} (cafe-ledger,
//     targets.go) — "opening one via CreateAccount on first use"
//     (cafe-ledger-design.md's Inc 2 note).
//   - `missing_charge` — the tab is settled, owes money, the account exists,
//     and no DEBIT cafetransaction `settles` this tab yet. Weaver dispatches
//     DebitAccount{accountKey, amountCents, memo, tabRef} (cafe-ledger,
//     targets.go) — the tabRef extension writes the settles audit link this
//     OPTIONAL MATCH walks, so once posted the gap converges and stays
//     converged (a tab is settled exactly once — Settle rejects a second call
//     with TabNotOpen — so there is no re-open path to guard, unlike
//     semantic-contracts' recurring-clause freshness lane).
//   - `missing_payment` — the tab is settled, owes money, the account exists,
//     the charge IS posted (txCount > 0), the tab records cash taken at the
//     counter (`paidAtSettleCents > 0` — a staff Settle{paidCents}, ddls.go;
//     absent on every other settle, and the full engine's compareAny reads a
//     null operand as false, so no null guard is needed), and no CREDIT
//     cafetransaction `settles` this tab yet. Weaver dispatches
//     CreditCafeAccount{accountKey, amountCents: paidAtSettleCents, memo,
//     reason: payment, tabRef} (cafe-ledger, targets.go) and the credit
//     writes the same settles link. The `txCount > 0` conjunct is
//     missing_charge's own closing predicate, verbatim, and it is what
//     ORDERS the two postings: cafe-ledger's payment cap reads the account's
//     live .balance, so a credit dispatched before the debit posts is refused
//     PaymentExceedsBalance for cash the resident already handed over; opened
//     only once the debit exists, the payment always lands inside the balance
//     that debit opened. It also guarantees missing_payment and
//     missing_charge are never both live.
//
// ONE settles hop serves both gaps. The charge and the counter payment carry
// the same relation (cafe-ledger writes settles on a debit and a credit
// alike), and the two counts discriminate by the entry's own type with a
// `count(CASE WHEN … THEN tx.key ELSE null END)` conditional count (the
// CASE-guarded count idiom of edge-manifest's seatsHeld and lease-signing's
// readiness counts): a CASE with no truthy WHEN yields null and count()
// skips nulls (ruleengine/full expr_eval.go, aggregate.go), so an unbound tx
// (no settling entry at all) and an entry of the other type both contribute
// nothing. That is a new dependency both gaps' closing predicates now carry:
// they read the settling transaction's `.entry.data.type`, so a settles link
// whose transaction has no readable .entry counts as NEITHER — missing_charge
// would stay open past a posted charge and missing_payment past a posted
// payment, and the Weaver would re-dispatch until the retry budget parks it.
// Unreachable today: cafe-ledger's post_entry commits the .entry aspect and
// the settles link in the SAME atomic batch (scripts.go, the mutations list
// — make_aspect(tx_key, "entry", …) beside make_link(settles_lnk, …)), so
// one never exists without the other; no cafe-ledger DDL marks the entry
// aspect Sensitive (pkgmgr.DDLSpec.Sensitive — cafeLedgerHistory already
// projects t.entry.data.type on the same plane), so this lens reads it as
// freely as the link; and nothing tombstones an entry (the ledger is
// append-only). The count is deliberately
// NOT DISTINCT: the other optional hops
// (cl, ol) bind at most one row each, so no tx is ever multiplied, and a
// non-DISTINCT count keeps this stage a multiplicity-sensitive aggregator —
// the engine evaluates the three sibling branches as one product, the
// shipped verdict (branch_decomposition_corpus_census_pins_test.go); a
// DISTINCT count would move the lens into the branch-decomposing population,
// which needs its own equivalence differential in ruleengine/full. A second
// relation name would have cost the lens its relation-narrowed consumer
// filter (3 labels × (1 + 2·4 relations) = 27 > the 24-subject budget,
// internal/refractor/subjects); one relation keeps it at 3 × 7 = 21.
//
// `itemsMemo` is a plain scalar pass-through of `t.status.data.itemsMemo`
// (ddls.go's tab DDL builds it, comma-joined, as Charge/VoidCharge run) —
// the row column targets.go's missing_charge Params templates straight into
// DebitAccount's memo field, and cmd/cafe-app renders the same column on the
// open-tab card, so what a resident sees ordering and what lands on the
// house-tab ledger entry are the identical string.
//
// `lines` is likewise a plain array pass-through of `t.status.data.lines`
// (the itemized {id, description, amountCents, voided, orderedBy, orderedAt,
// servedAt?, servedBy?} entries Charge/VoidCharge/MarkLineServed maintain —
// orderedBy = the Charge's own op.actor, servedAt absent while a self-order
// is still to make),
// mirroring clinic-domain's `.hours.data.windows` pass-through (lenses.go) —
// Cypher needs no `collect()` to project a field that is already an array on
// the aspect. cmd/cafe-app renders it as the itemized receipt breakdown,
// falling back to itemsMemo for a tab whose .status predates the field
// (lines absent or []).
//
// A tab with totalCents=0 (opened and settled with nothing charged) never
// violates either gap — no house-tab posting is needed for a zero-amount
// visit.
//
// The lease hop prefers `chargedTo`, the tab's PERMANENT link, over the
// `openFor` one Settle retracts (ddls.go): both money gaps only ever open on
// a SETTLED tab, and settlement is exactly when openFor is gone, so a SETTLED
// row anchors on chargedTo alone — that's what lets the row survive past
// settlement rather than vanishing (EmptyBehavior "delete") right when a
// posting is owed, and it's why a settled tab that somehow still lacks
// chargedTo projects NO row rather than one with a null leaseAppKey Weaver
// could dispatch against blind (TestCafeTabSettlement_OpenForAloneDoesNotAnchor
// pins this: openFor must never paper over a missing permanent link once a
// tab is settled). openFor is admitted ONLY while the tab is still OPEN, and
// only as a fallback when chargedTo is absent (Settle backfills it
// unconditionally the moment the tab closes — ddls.go) — this is what makes a
// tab that predates the chargedTo write discoverable and settleable at all,
// instead of stranding it invisible to every reader forever. A tab carrying
// both resolves to chargedTo (coalesce's first argument), so a normally
// opened tab's row is identical whichever branch a reader imagines.
const tabSettlementSpec = `MATCH (t:tab {key: $actorKey})
OPTIONAL MATCH (t)-[:chargedTo]->(cl:leaseapp)
OPTIONAL MATCH (t)-[:openFor]->(ol:leaseapp)
OPTIONAL MATCH (t)<-[:settles]-(tx:cafetransaction)
WITH
  t.key AS entityKey,
  t.status.data.value AS status,
  t.status.data.totalCents AS totalCents,
  t.status.data.paidAtSettleCents AS paidAtSettleCents,
  t.status.data.itemsMemo AS itemsMemo,
  t.status.data.lines AS lines,
  t.status.data.openedAt AS openedAt,
  t.status.data.settledAt AS settledAt,
  (CASE WHEN t.status.data.value = 'open' THEN coalesce(cl, ol) ELSE cl END) AS l,
  count(CASE WHEN tx.entry.data.type = 'debit' THEN tx.key ELSE null END) AS txCount,
  count(CASE WHEN tx.entry.data.type = 'credit' THEN tx.key ELSE null END) AS payCount
WHERE l.key <> null
WITH
  entityKey, status, totalCents, paidAtSettleCents, itemsMemo, lines, openedAt, settledAt, txCount, payCount,
  l.key AS leaseAppKey,
  l.cafeLedgerAccount.data.accountKey AS accountKey
RETURN
  entityKey AS actorKey,
  entityKey,
  entityKey AS tabKey,
  leaseAppKey,
  accountKey,
  totalCents,
  paidAtSettleCents,
  itemsMemo,
  lines,
  status,
  openedAt,
  settledAt,
  ((status = 'settled') AND (totalCents > 0) AND (accountKey = null)) AS missing_account,
  ((status = 'settled') AND (totalCents > 0) AND (accountKey <> null) AND (txCount = 0)) AS missing_charge,
  ((status = 'settled') AND (totalCents > 0) AND (accountKey <> null) AND (txCount > 0) AND (paidAtSettleCents > 0) AND (payCount = 0)) AS missing_payment,
  (
    ((status = 'settled') AND (totalCents > 0) AND (accountKey = null))
    OR ((status = 'settled') AND (totalCents > 0) AND (accountKey <> null) AND (txCount = 0))
    OR ((status = 'settled') AND (totalCents > 0) AND (accountKey <> null) AND (txCount > 0) AND (paidAtSettleCents > 0) AND (payCount = 0))
  ) AS violating
`

// staleTabSettlementSpec is the auto-settle convergence lens: an OPEN tab
// nobody ever settles sits unbilled indefinitely (verticals.md — Riley's ran
// 2026-07-28 → 07-31 with nobody nudged), the café-domain analog of
// clinic-reminders' pastDueAppointments. Unlike an appointment (whose own
// .schedule.endsAt already IS the staleness threshold), a tab carries no
// pre-existing business timestamp to bind to, so staleAt is PRECOMPUTED at
// OpenTab write time (time.rfc3339_add(openedAt, "24h"), ddls.go — the cypher
// engine has no date-arithmetic builtin, clinic-reminders/visitseries.go's
// own finding) and carried forward unchanged by Charge/VoidCharge.
//
// What decides "stale" is a recorded FACT, not a clock: this lens is itself
// the convergence target the @at it arms fires against, so it reads its own
// entry under `byTarget.<StaleTabSettlementTarget>` in the freshnessExpiry
// marker (MarkExpired, orchestration-base/mark_expired.go) — the
// unroutedTasksSpec / staleAssignedTasksSpec idiom
// (orchestration-base/lenses.go), a target reading the key the
// timer that fired against it was told to write.
//
// freshUntil arms a one-shot @at at staleAt while the tab is still open and
// no lapse of THIS deadline has landed yet; once the marker lands,
// missing_settle opens — the violating row itself drives dispatch from
// there, the pastDueAppointments idiom, not a repeated timer wake-up. An
// already-past staleAt with no marker yet still projects freshUntil =
// staleAt verbatim, arming an overdue @at rather than leaving the tab
// unarmed forever; a marker whose recorded instant falls short of a later
// staleAt (the deadline moved out past a prior fire) reads unlapsed and
// re-arms with no clearing write. `status = 'open'` is the only
// terminal-state check needed: unlike an appointment's three-way status, a
// tab is only ever open or settled, and Settle/SettleStaleTab both flip it
// the same way, so a legitimate staff Settle at any point (even racing the
// @at fire) permanently converges the gate — SettleStaleTab's own defensive
// re-check (ddls.go) guards the same race a heartbeat later.
//
// missing_staleat is the second, independent gap: an OPEN tab whose
// .status carries NO staleAt at all (every tab opened before this feature
// shipped) leaves the marker comparison and every ordering test against
// staleAt resolving false or null, so missing_settle alone can never see
// it — such a tab would be invisible to the whole convergence, forever.
// missing_staleat dispatches BackfillTabStaleAt (ddls.go), which computes
// the same openedAt + 24h OpenTab would have written; the NEXT convergence
// cycle then re-evaluates missing_settle against the now-present value like
// any other tab. (freshUntil's CASE takes its THEN branch for this row too
// — NOT (marker >= null) is NOT false — but the column still comes out
// null, because THEN t.status.data.staleAt is itself null.)
//
// maxretries_settle bakes the retry cap (maxSettleRetries, retry_budget.go)
// into the row as a constant, the wellness-domain/lenses.go precedent
// (orphanedBookingSettlementSpec) — built once at package init via
// fmt.Sprintf, splicing the target id (StaleTabSettlementTarget above — the
// §10.8 TargetID the byTarget read compares against) and the retry cap; no
// literal '%' in the cypher body.
var staleTabSettlementSpec = fmt.Sprintf(`MATCH (t:tab {key: $actorKey})
RETURN
  t.key AS actorKey,
  t.key AS entityKey,
  t.key AS tabKey,
  t.status.data.value AS status,
  t.status.data.openedAt AS openedAt,
  t.status.data.staleAt AS staleAt,
  CASE WHEN (t.status.data.value = 'open') AND NOT (t.freshnessExpiry.data.byTarget.%[1]s >= t.status.data.staleAt) THEN t.status.data.staleAt ELSE null END AS freshUntil,
  ((t.status.data.value = 'open') AND (t.freshnessExpiry.data.byTarget.%[1]s >= t.status.data.staleAt)) AS missing_settle,
  ((t.status.data.value = 'open') AND (t.status.data.staleAt = null)) AS missing_staleat,
  (
    ((t.status.data.value = 'open') AND (t.freshnessExpiry.data.byTarget.%[1]s >= t.status.data.staleAt))
    OR ((t.status.data.value = 'open') AND (t.status.data.staleAt = null))
  ) AS violating,
  %[2]d AS maxretries_settle
`, StaleTabSettlementTarget, maxSettleRetries)

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
