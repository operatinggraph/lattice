package loftspacedomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// LoftspaceListingsBucket is the NATS-KV read model the availableListings lens
// projects into. It is the **P5 query surface** for "what units can I lease":
// an application reads THIS projected bucket (one entry per listed unit, keyed by
// the unit key), never Core KV (lattice-architecture.md P5 — lenses are the only
// application query surface). The Refractor auto-creates the bucket on lens load.
const LoftspaceListingsBucket = "loftspace-listings"

// Lenses returns the package's Lens declarations: `availableListings` (the
// listed-unit projection) and `applicantRosterRead` (the protected Postgres
// identity roster — the ONLY roster surface: D1.5's picker reads it as an
// authenticated actor, and cmd/loftspace-app's server-side name resolution
// (unit_applications.go, lease_document.go) reads it as the SIGNED-IN
// caller, RLS-scoped by the lens's own authz_anchors; there is no
// unprotected NATS-KV roster, because the identity `name`
// is a sensitive aspect and a Secure Lens may only decrypt into an
// RLS-protected Postgres model, Contract #3 §3.10). availableListings
// projects one row per LISTED unit — a location unit carrying a `.listing`
// aspect — flattening the listing economics + street address into a
// query-optimized read-model row. The lens does NOT filter on status (it
// carries the status column), so a reader can show available units by default
// and still surface pending / leased on request; the per-row key is the unit
// key (the CreateLeaseApplication target the applicant FE submits).
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "availableListings",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LoftspaceListingsBucket,
			Engine:        "full",
			Spec:          availableListingsSpec,
		},
		{
			// applicantRosterRead — the protected Postgres read model for the
			// applicant-identity picker (D1.5) and for the app's server-side
			// name resolution. Reading it requires an authenticated actor:
			// every row's authz_anchors carries the identity's OWN bare NanoID
			// (a signed-in actor always resolves their own name) PLUS the bare
			// NanoID of every landlord managing a unit the identity has a live
			// lease application against, PLUS every building covering that unit
			// (applicationFor -> appliesToUnit -> manages / containedIn*1..,
			// mirroring cafe-domain's cafeIdentitiesRead self+fan-out and
			// lease-signing's landlordLeaseApplicationsRead anchor shape) — so
			// the SAME managing landlord and worksAt-anchored staffer whose
			// cap-read grant already resolves /api/landlord/applications also
			// resolves the roster. The reserved WildcardAnchor grant (D1 design
			// §3.4 M5, internal/refractor/adapter.WildcardAnchor) still matches
			// every row on top of these. The picker still works before any
			// applicant has selected who they are: the app mints its own
			// fixed-subject staff token (s.adminActor, the same root-equivalent
			// identity the app already connects to NATS as), so the client never
			// needs a prior login to bootstrap identity selection.
			//
			// SECURE LENS (Contract #3 §3.10, Vault Phase B): the identity
			// `name` is a sensitive aspect, so Core KV holds only its
			// ciphertext envelope. The cypher RETURNs the envelope whole
			// (i.name.data) and Refractor decrypts it under the owning
			// identity's DEK at projection time — plaintext exists only in
			// this RLS-protected table. A shredded identity's name projects
			// NULL (right-to-erasure at the projection surface).
			//
			// NAME + STATE ONLY — no additional PII.
			CanonicalName: "applicantRosterRead",
			Class:         "meta.lens",
			Adapter:       "postgres",
			Table:         "read_loftspace_identities",
			Engine:        "full",
			Spec:          applicantRosterReadSpec,
			Protected:     true,
			IntoKey:       []string{"identity_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "entity_key", Type: "text"},
				{Name: "identity_key", Type: "text"},
				{Name: "name", Type: "text"},
				{Name: "state", Type: "text"},
			},
			SecureColumns: []pkgmgr.SecureColumn{
				{Column: "name", HolderTypes: []string{"identity"}, Field: "value"},
			},
		},
		{
			// landlordUnitsRead — the protected Postgres read model for
			// portfolio-pulse occupancy (mixed-use-composition-design.md Inc 2):
			// one row per unit the landlord manages, independent of any lease
			// application — a vacant, never-applied-to unit still projects a
			// row (unlike lease-signing's landlordLeaseApplicationsRead, which
			// requires a leaseapp to exist at all, so an unlisted/un-applied-to
			// unit is invisible to it). MATCH REQUIRES the `manages` link, so a
			// row exists only for a unit that has a manager; a co-managed unit
			// fans out to one row per landlord (composite unit_id/landlord_id
			// key, mirroring landlordLeaseApplicationsRead's app_id/landlord_id
			// shape). unit_status is the raw `.listing.data.status`
			// (available/pending/leased/withdrawn) or null when never listed —
			// the occupancy handler treats null as its own "not yet listed"
			// bucket, not an error.
			//
			// DIFF RETRACTION: like landlordLeaseApplicationsRead, this walks
			// `manages` structurally rather than an anchor-key equality, so a
			// RemoveUnitOwner unassign needs Refractor's target-diff retraction
			// path, not anchor-self retraction.
			CanonicalName:  "landlordUnitsRead",
			Class:          "meta.lens",
			Adapter:        "postgres",
			Table:          "read_landlord_units",
			Engine:         "full",
			Spec:           landlordUnitsReadSpec,
			Protected:      true,
			DiffRetraction: true,
			IntoKey:        []string{"unit_id", "landlord_id"},
			Columns: []pkgmgr.PostgresColumn{
				{Name: "unit_key", Type: "text"},
				{Name: "landlord_key", Type: "text"},
				{Name: "unit_address", Type: "text"},
				{Name: "unit_city", Type: "text"},
				{Name: "unit_region", Type: "text"},
				{Name: "unit_status", Type: "text"},
				{Name: "unit_rent", Type: "double precision"},
				{Name: "unit_currency", Type: "text"},
			},
		},
	}
}

// availableListingsSpec projects one row per listed unit. The WHERE keeps only
// units whose `.listing` aspect exists (status non-null) — a unit with no
// listing is not leasable and is excluded. Aspect fields are read by the
// documented `node.<aspect>.data.<field>` form (executor.go), the same access
// lease-signing's convergence lens uses against these exact `.listing` /
// `.address` aspects. The per-row key column is `key` (the unit key, the
// IntoKey default), so the read model is keyed by `vtx.unit.<id>`; `unitKey`
// repeats it in the body for the reader. Address columns are null when a unit
// has no `.address` aspect (the reader treats them as absent).
const availableListingsSpec = `MATCH (u:unit)
WHERE u.listing.data.status <> null
RETURN
  u.key AS key,
  u.key AS unitKey,
  u.listing.data.status AS status,
  u.listing.data.rentAmount AS rentAmount,
  u.listing.data.rentCurrency AS rentCurrency,
  u.listing.data.bedrooms AS bedrooms,
  u.listing.data.bathrooms AS bathrooms,
  u.listing.data.sqft AS sqft,
  u.listing.data.availableFrom AS availableFrom,
  u.listing.data.leaseTermMonths AS leaseTermMonths,
  u.address.data.line1 AS addrLine1,
  u.address.data.line2 AS addrLine2,
  u.address.data.city AS addrCity,
  u.address.data.region AS addrRegion,
  u.address.data.postal AS addrPostal`

// applicantRosterReadSpec projects one row per NAMED identity — the roster a
// person picks themselves from by name instead of a raw vtx.identity.<id> key.
// `name` is a sensitive aspect, so its Core-KV `data` is a ciphertext envelope
// ({ct, nonce, keyId}): the WHERE keeps only identities carrying a `.name`
// aspect via ciphertext presence (`i.name.data.ct <> null` — there is no
// plaintext `value` field at rest), so service / unnamed actors are excluded
// and the picker stays a list of real people. The RETURN carries the envelope
// whole (`i.name.data AS name`) for the Secure-Lens decryptor, which projects
// the decrypted object's `value` field per the SecureColumns declaration,
// opening it under the holder the ciphertext itself names. authz_anchors
// carries the identity's OWN bare NanoID (a signed-in actor always resolves
// its own name) PLUS the bare NanoID of every landlord managing a unit the
// identity has a live lease application against, PLUS every building
// covering that unit — a walk anchored on `i` via PATTERN COMPREHENSION (not
// a separate MATCH: a bound leaseapp/unit hop in the query body would fan an
// identity with multiple applications into one row per application,
// colliding on the single-valued identity_id IntoKey), mirroring
// cafe-domain's cafeIdentitiesRead self+fan-out idiom and reusing
// lease-signing's landlordLeaseApplicationsRead anchor shape
// (`[landlordKey] + [containedIn*1.. building tokens]`) one hop further in
// (identity -> leaseapp -> unit, not leaseapp -> unit). The reserved
// WildcardAnchor grant still matches every row on top of these (mirrors
// clinic-domain's clinicPatientsReadSpec). An identity with no leaseapp at
// all (e.g. a staffer with no lease of their own) still projects — the
// variable-length walk finds no match and the fan-out is simply empty, the
// self-anchor survives on its own.
const applicantRosterReadSpec = `MATCH (i:identity)
WHERE i.name.data.ct <> null
RETURN
  nanoIdFromKey(i.key)  AS identity_id,
  i.key                 AS entity_key,
  i.key                 AS identity_key,
  i.name.data           AS name,
  i.state.data.value    AS state,
  [nanoIdFromKey(i.key)] + [(i)<-[:applicationFor]-(la:leaseapp)-[:appliesToUnit]->(ua:unit)<-[:manages]-(landlord:identity) | nanoIdFromKey(landlord.key)] + [(i)<-[:applicationFor]-(lb:leaseapp)-[:appliesToUnit]->(ub:unit)-[:containedIn*1..]->(b:building) | nanoIdFromKey(b.key)]
                        AS authz_anchors
`

// landlordUnitsReadSpec projects one row per (unit, managing landlord) pair —
// see the Lenses() declaration above for the shape rationale. authz_anchors
// carries the managing landlord's bare NanoID PLUS every building covering
// the unit, the same `[landlordKey] + [containedIn*1.. building tokens]`
// shape landlordLeaseApplicationsRead already anchors on — a front-desk
// staffer's worksAt-building grant resolves both models identically, so a
// portfolio card composed from the two never goes half-populated.
const landlordUnitsReadSpec = `MATCH (u:unit)<-[:manages]-(landlord:identity)
RETURN
  nanoIdFromKey(u.key)          AS unit_id,
  nanoIdFromKey(landlord.key)   AS landlord_id,
  u.key                         AS unit_key,
  landlord.key                  AS landlord_key,
  u.address.data.line1          AS unit_address,
  u.address.data.city           AS unit_city,
  u.address.data.region         AS unit_region,
  u.listing.data.status         AS unit_status,
  u.listing.data.rentAmount     AS unit_rent,
  u.listing.data.rentCurrency   AS unit_currency,
  [nanoIdFromKey(landlord.key)] + [(u)-[:containedIn*1..]->(b:building) | nanoIdFromKey(b.key)] AS authz_anchors
`
