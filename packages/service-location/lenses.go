package servicelocation

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Lenses returns the package's Lens declarations.
//
// service-location owns the residence-based service-access projection — the
// LOCATION grant scheme, one of the three disjoint capability sources that
// union into an actor's authorization (Contract #6 §6.1 / §6.10 item 4). Core
// retains the Capability KV bucket + the step-3 dispatcher; service-location
// declares, as install-time data, where its grants project (cap.svc.<actor>)
// and re-points the service auth path's key derivation to it.
//
//   - capabilityServiceAccess (actor-aggregate): for every actor, projects
//     cap.svc.<actor-suffix> carrying that actor's serviceAccess[] — the
//     services reachable from the actor's residence→containment chain that are
//     availableAt a location on that chain (with unavailableAt exclusions), and
//     the operations each such service permits. The disjoint cap.svc.* key
//     space (Contract #6 §6.1) keeps the location grant off the core
//     cap.<actor> / cap.roles.<actor> keys; the service path reads it via the
//     re-pointed serviceKeyFromActor derivation (one key per path).
//
//   - staffReadGrants (GrantTable, Path A): for every front-desk staff actor,
//     one actor_read_grants row per building they work at — the workplace READ
//     grant behind RLS on the Protected worklist tables. The location spine's
//     read-path sibling to capabilityServiceAccess's write-path grant, and it
//     lives here because it derives from worksAt, the link this package owns and
//     whose removal must revoke it.
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName:  "capabilityServiceAccess",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "capability-kv",
			Engine:         "full",
			Spec:           capabilityServiceAccessSpec,
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "cap.svc.{actorSuffix}",
				BodyColumns:      []string{"serviceAccess"},
				EmptyBehavior:    "delete",
				Freshness:        "auto",
			},
		},
		{
			// staffReadGrants — the cap-read.staff Path A producer
			// (facet-staff-worlds-design.md §3.5), mirroring the
			// clinicPatientReadGrants package-producer shape but anchoring on the
			// WORKPLACE rather than on the actor itself.
			//
			// The anchor is the BUILDING's NanoID, and that is the whole security
			// argument: anchors are global tokens, so granting a front-desk actor
			// each resident's NanoID would open those residents' rows in every
			// Protected table, credentials included. A building token instead
			// opens exactly the rows a lens chose to declare workplace-readable.
			//
			// The grant exists only while BOTH links do — holdsRole frontOfHouse
			// and worksAt. Retraction therefore cannot ride an anchor tombstone
			// the way the clinic producers' does: an unwire tombstones the LINK,
			// not the staff identity or the building, so no vertex tombstone ever
			// names this row. The row-set simply shrinks, which is exactly the
			// shape DiffRetraction exists for — here the diff IS the revocation
			// transport, scoped to this lens's own grant_source (GrantSource,
			// below) so it cannot touch another producer's grants.
			CanonicalName:  "staffReadGrants",
			Class:          "meta.lens",
			Adapter:        "postgres",
			GrantTable:     true,
			GrantSource:    "cap-read.staff",
			DiffRetraction: true,
			Engine:         "full",
			Spec:           staffReadGrantsSpec,
		},
	}
}

// capabilityServiceAccessSpec walks the actor's residence→containment chain to
// the services availableAt a reachable location, and projects the per-service
// serviceAccess[] entry (Contract #6 §6.5 / §6.10). Anchored on the bound
// identity so reprojection traverses adjacency from the actor on any
// residesIn / containedIn / availableAt / unavailableAt / permitsOperation CDC
// event. The OPTIONAL MATCH yields a single degenerate (all-null) collect entry
// for an actor that reaches no service; the envelope wrapper's
// emptyBehavior:delete drops the key when no real grant remains (Contract #6
// §6.8 absence = denial).
//
// Directions match the as-built model (Contract #1 §1.1):
//
//   - residesIn is identity→location, so (identity)-[:residesIn]->(loc0).
//   - containedIn is child→parent, so (loc0)-[:containedIn*0..]->(loc) walks
//     residence→ancestors; *0.. includes the direct (depth-0) residence
//     (Contract #6 §6.10 item 2, transitive availability).
//   - availableAt / unavailableAt are service→location, so the service is the
//     INBOUND side: (loc)<-[:availableAt]-(svc). NOT inverted.
//
// Two guards make the projection sound:
//
//   - TEMPLATE guard (§6.10 / §6.5): `NOT (svc)-[:instanceOf]->(svcTpl:service)`
//     admits service TEMPLATES and excludes service INSTANCES (and any claim
//     vertex). The template/instance discriminator is the vertex ENVELOPE class
//     (P7 — service.<x>.template / service.<x>.instance; no `.class` shadow
//     aspect). Both a template and an instance now carry an outgoing instanceOf
//     link (the P7 type-authority chain: a template → the service DDL meta, an
//     instance → its template), so bare instanceOf-absence no longer
//     discriminates. The `:service` label on the target restores it: an
//     instance's instanceOf points at a `vtx.service.*` template (matches
//     `:service` by key-type → excluded), while a template's instanceOf points
//     at a `vtx.meta.*` DDL vertex (NOT `:service` → admitted). Defense-in-depth:
//     the WireAvailableAt op already restricts the availableAt source to
//     templates (its envelope class ends in `.template`).
//
//   - MULTI-LEVEL EXCLUSION (§6.10 item 1), PER RESIDENCE CHAIN: the exclusion
//     existential walks up from the bound loc0 — the SAME residence that granted
//     this row — through a FRESH exLoc, suppressing the service iff an
//     unavailableAt for the bound svc sits anywhere on THAT residence's
//     containment chain. Anchoring on loc0 (rather than re-seeding from identity
//     across the actor's whole residence set) keeps the exclusion per-chain: a
//     service unavailableAt one residence is still granted through a different,
//     unexcluded residence. exLoc is fresh, so the walk is not pinned to the
//     granting ancestor loc. A laundry availableAt a building but unavailableAt
//     the actor's penthouse is excluded for the penthouse chain.
//
// `svc` carries the `:service` label (matched by the `vtx.service.*` key-type)
// as a self-contained guard — only service vertices project, even if a
// non-service vertex were ever wired an availableAt edge. `allowedOperations` is
// the pattern-comprehension over permitsOperation → op-meta, keeping only ops
// that carry an operationType. The entry carries no serviceClass: the residence
// scheme has no use for it; the rich class discriminator is now the vertex
// envelope class (`svc.class` = service.<x>.template / .instance), which a
// structural denial can read directly off the root.
const capabilityServiceAccessSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(loc0)-[:containedIn*0..]->(loc)<-[:availableAt]-(svc:service)
WHERE NOT (svc)-[:instanceOf]->(svcTpl:service)
  AND NOT (loc0)-[:containedIn*0..]->(exLoc)<-[:unavailableAt]-(svc)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    service: svc.key,
    resolvedVia: [loc.key],
    allowedOperations: [(svc)-[:permitsOperation]->(op) WHERE op.data.operationType <> null | {operationType: op.data.operationType}]
  }) AS serviceAccess
`

// staffReadGrantsSpec projects one grant row per (front-desk staff, workplace
// building) pair. Both MATCHes are REQUIRED, so the row exists only while the
// actor both holds frontOfHouse and works somewhere: drop either link and the
// pair stops being derived, and the target-diff revokes the grant.
//
// Deliberately UNANCHORED — no {key: $actorKey} anywhere, unlike this package's
// capabilityServiceAccess above. DiffRetraction compares the target's full live
// key set against this query's full result set, which is exact only when the
// result set is already the complete current truth; an $actorKey-scoped variant
// would retract every OTHER staff actor's grants on its first event. Refractor
// enforces this at activation (ValidateUnanchoredForDiffRetraction), so the
// property cannot be lost by a later edit.
//
// The `building` label matches the vtx.building.* KEY TYPE, not the class (every
// location vertex is class=location — location-domain/ddls.go): a worksAt wired
// to a unit or a property therefore grants nothing, which is the intended
// granularity. The role is matched by canonicalName, the same way
// rbac-domain's capabilityRoleIndex reads a role's name.
const staffReadGrantsSpec = `MATCH (staff:identity)-[:holdsRole]->(r:role)
MATCH (staff)-[:worksAt]->(b:building)
WHERE r.canonicalName.data.value = 'frontOfHouse'
RETURN
  nanoIdFromKey(staff.key)    AS actor_id,
  nanoIdFromKey(b.key)        AS anchor_id,
  'cap-read.staff'            AS grant_source
`
