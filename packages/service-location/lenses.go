package servicelocation

import "github.com/asolgan/lattice/internal/pkgmgr"

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
