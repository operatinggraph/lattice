package loftspacedomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// LoftspaceListingsBucket is the NATS-KV read model the availableListings lens
// projects into. It is the **P5 query surface** for "what units can I lease":
// an application reads THIS projected bucket (one entry per listed unit, keyed by
// the unit key), never Core KV (lattice-architecture.md P5 — lenses are the only
// application query surface). The Refractor auto-creates the bucket on lens load.
const LoftspaceListingsBucket = "loftspace-listings"

// LoftspaceIdentitiesBucket is the NATS-KV read model the applicantRoster lens
// projects into. It is the **P5 query surface** for "who can I act as": the
// applicant FE reads THIS projected bucket (one entry per named identity, keyed
// by the identity key) to render a human-readable identity picker, never Core KV.
// The Refractor auto-creates the bucket on lens load.
//
// The identity `name` is a sensitive aspect; projecting it is consistent with the
// trusted single-identity tool model (no read-path auth yet, Phase-3+) and with
// identity-hygiene already projecting names into its duplicate-candidates lens.
const LoftspaceIdentitiesBucket = "loftspace-identities"

// Lenses returns the package's Lens declarations: `availableListings` (the
// listed-unit projection), `applicantRoster` (the unprotected NATS-KV roster
// the trusted-tool console reads server-side to resolve a name for display —
// unit_applications.go, lease_document.go), and `applicantRosterRead` (the
// protected Postgres roster D1.5's identity picker reads as an authenticated
// actor). It projects one row per LISTED unit — a location unit carrying a
// `.listing` aspect — flattening the listing economics + street address into a
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
			CanonicalName: "applicantRoster",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        LoftspaceIdentitiesBucket,
			Engine:        "full",
			Spec:          applicantRosterSpec,
		},
		{
			// applicantRosterRead — the protected Postgres read model for the
			// applicant-identity picker (D1.5). cmd/loftspace-app's handleIdentities
			// used to list the unprotected applicantRoster NATS-KV bucket and serve
			// every named identity's full name to ANY caller with no authentication
			// at all — a system-wide membership-disclosure leak (which applicants and
			// landlords exist, by full name). handleStaffIdentities replaces that
			// vector, reading THIS table as a JWT-authenticated actor, mirroring
			// clinic-domain's clinicPatientsRead exactly.
			//
			// Like clinicPatientsRead there is no per-identity self-anchor to carve
			// out — "the whole roster" has no single-row owner, so every row
			// projects an EMPTY authz_anchors set: only an actor holding the reserved
			// WildcardAnchor grant (D1 design §3.4 M5,
			// internal/refractor/adapter.WildcardAnchor) ever matches a row. The
			// picker still works before any applicant has selected who they are —
			// the app mints its own fixed-subject staff token (s.adminActor, the same
			// root-equivalent identity the app already connects to NATS as), so the
			// client never needs a prior login to bootstrap identity selection.
			//
			// NAME + STATE ONLY, the same columns the unprotected applicantRoster
			// projects — no additional PII.
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

// applicantRosterSpec projects one row per NAMED identity — the human-readable
// roster the applicant FE renders so a person picks themselves by name instead of
// a raw vtx.identity.<id> key. The WHERE keeps only identities carrying a `.name`
// aspect (the `<> null` aspect-presence idiom availableListings uses), so service
// / unnamed actors are excluded and the picker stays a list of real people. The
// per-row key is the identity key (the IntoKey default), so the read model is
// keyed by vtx.identity.<id>; `identityKey` repeats it in the body. `name` and
// `state` are read by the documented node.<aspect>.data.<field> form.
const applicantRosterSpec = `MATCH (i:identity)
WHERE i.name.data.value <> null
RETURN
  i.key AS key,
  i.key AS identityKey,
  i.name.data.value AS name,
  i.state.data.value AS state`

// applicantRosterReadSpec is the PROTECTED Postgres twin of applicantRosterSpec
// (D1.5): same WHERE guard (only named identities project), plus the
// nanoIdFromKey(i.key) IntoKey column and an EMPTY authz_anchors set — the
// roster has no per-row owner, so only the reserved WildcardAnchor grant ever
// matches (mirrors clinic-domain's clinicPatientsReadSpec).
const applicantRosterReadSpec = `MATCH (i:identity)
WHERE i.name.data.value <> null
RETURN
  nanoIdFromKey(i.key)  AS identity_id,
  i.key                 AS entity_key,
  i.key                 AS identity_key,
  i.name.data.value     AS name,
  i.state.data.value    AS state,
  []                    AS authz_anchors
`
