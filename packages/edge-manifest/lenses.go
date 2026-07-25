package edgemanifest

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Lenses returns the package's fourteen Personal-Lens declarations
// (edge-showcase-app-design.md §3.2; the manifest.ent entity lenses per
// facet-entity-browse-design.md; the staff siblings edgeCatalogRoles +
// edgeTasksQueued + edgeStaffWorkOrders per facet-staff-worlds-design.md
// §3.3; the provider-hat siblings edgeProviderSchedule + edgeProviderQueue +
// edgeInstructorSessions per persona-worlds-design.md Fire W0) — the repo's
// first `nats-subject` / Personal Lens package.
//
// Thirteen of the fourteen are NON-SELF-ANCHORED: each keys its rows on a
// vertex other than the recipient identity (a service template, an op meta, a
// task, an instance, a session, a provider, a booking, a work order, an
// appointment). Refractor's D1 gate (internal/refractor/projection/personal.go
// → capabilityread.IsReadable) drops such a row unless the actor's unioned
// `cap-read.<domain>.<actor>` slices list the anchor's bare NanoID — silently,
// fail-closed, by design (Contract #6 §6.14 Path B; NOT the Postgres
// actor_read_grants table a `GrantTable:true` lens feeds, which is Path A / RLS
// for Protected reads and irrelevant here — this package has no Postgres lens).
//
// So each of those thirteen declares its actor→anchor reachability ONCE, as a
// `Walk`, and pkgmgr compiles BOTH artifacts from it: the lens's own OPTIONAL
// MATCH prefix, and the read-grant producer that grants the anchors. `Spec`
// therefore carries the presentation TAIL only. The three producers
// (edgeManifestReadGrants / …Staff… / …Provider…) are generated, one per
// declared ReadGrantDomain — they are not written here, and must not be.
//
// Three domains rather than one: §6.14 unions every cap-read slice into the
// actor's effective readable set, so a reachability path not every actor has
// (staff role-standing grants, provider-hat bindings) lives in its own slice
// and its branches never join the base producer's cross-branch fan-out. An
// identity with no such binding simply gets an empty slice, deleted by the
// generated producer's EmptyBehavior + realness filter.
//
// Every Personal-Lens cypher below is Personal:true (Refractor's cross-vertex
// fan-out re-executes the cypher once per reachable identity, binding
// $actorKey to that identity's own key — personal-secure-lens-design.md §3.3),
// delivers over the shared `lattice.sync.user.<actor>` subject
// (SubjectPrefix/Stream), and keys its rows under the reserved `manifest.`
// namespace via IntoKey's dot-join (edge/store.go's ApplyUpsert/ApplyDelete
// carry a matching exemption for this prefix, since a `manifest.*` key is a
// projection-row key, not a Contract #1 key).
//
// $actorKey is NOT the "current identity" for actor-aggregate lenses (those
// bind $actorKey to whichever vertex was mutated); for a Personal:true lens
// it is always the enumerated recipient identity's own key.
//
// v1 scope-downs (named, not silent — each is a reasonable narrowing the
// engine or the data model makes convenient to defer, not a correctness gap in
// what IS built): edgeIdentity's anchors carry the location's
// `.presentation.data.name` (class-2 display source, display-name-convention-
// design.md N1) plus its container's name, so a named world renders a human
// label instead of a bare NanoID; the location TYPE segment is still not
// synthesized into the row (the engine has no vertex-type-from-key function
// outside nanoIdFromKey, and no string concatenation to build one), so the
// renderer derives type from the key client-side; edgeCatalog covers the
// service-permitsOperation reachability path and edgeTasks the direct
// `assignedTo` one, with their role-derived counterparts split into the sibling
// lenses edgeCatalogRoles + edgeTasksQueued rather than folded in (this engine
// has no UNION, so a second independent path in one cypher cross-products it).
// Still deferred: the open-task-forOperation catalog path — a task's own bound
// op already rides inline on its edgeTasks row, so that gap is "browse all my
// ops," never "complete my task."
func Lenses() []pkgmgr.LensSpec {
	return []pkgmgr.LensSpec{
		{
			CanonicalName: "edgeIdentity",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns"},
			Spec:          edgeIdentitySpec,
		},
		{
			CanonicalName: "edgeServices",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainBase,
				AnchorType:  "service",
				AnchorVar:   "tpl",
				Chain:       []string{chainResidence, chainAvailableTemplates},
			},
			Spec: edgeServicesTail,
		},
		{
			CanonicalName: "edgeCatalog",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainBase,
				AnchorType:  "meta",
				AnchorVar:   "op",
				Chain: []string{
					chainResidence,
					chainAvailableTemplates,
					"(tpl)-[:permitsOperation]->(op:meta)",
				},
			},
			Spec: edgeCatalogTail,
		},
		{
			CanonicalName: "edgeTasks",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainBase,
				AnchorType:  "task",
				AnchorVar:   "task",
				Chain:       []string{"(identity)<-[:assignedTo]-(task:task)"},
			},
			Spec: edgeTasksTail,
		},
		{
			CanonicalName: "edgeInstances",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainBase,
				AnchorType:  "service",
				AnchorVar:   "inst",
				Chain:       []string{"(identity)<-[:providedTo]-(inst:service)"},
			},
			Spec: edgeInstancesTail,
		},
		{
			CanonicalName: "edgeEntitySessions",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainBase,
				AnchorType:  "session",
				AnchorVar:   "sess",
				Chain: []string{
					chainResidence,
					"(container)<-[:locatedAt]-(studio:studio)",
					"(studio)<-[:atStudio]-(sess:session)",
				},
			},
			Spec: edgeEntitySessionsTail,
		},
		{
			CanonicalName: "edgeEntityProviders",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainBase,
				AnchorType:  "provider",
				AnchorVar:   "prov",
				Chain: []string{
					chainResidence,
					"(container)<-[:practicesAt]-(prov:provider)",
				},
			},
			Spec: edgeEntityProvidersTail,
		},
		{
			CanonicalName: "edgeEntityBookings",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainBase,
				AnchorType:  "booking",
				AnchorVar:   "bk",
				Chain:       []string{"(identity)<-[:bookedBy]-(bk:booking)"},
			},
			Spec: edgeEntityBookingsTail,
		},
		{
			CanonicalName: "edgeCatalogRoles",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainStaff,
				AnchorType:  "meta",
				AnchorVar:   "op",
				Chain: []string{
					chainHeldRoles,
					"(role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:meta)",
				},
			},
			Spec: edgeCatalogRolesTail,
		},
		{
			CanonicalName: "edgeTasksQueued",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainStaff,
				AnchorType:  "task",
				AnchorVar:   "task",
				Chain: []string{
					chainHeldRoles,
					"(role)<-[:queuedFor]-(task:task)",
				},
			},
			Spec: edgeTasksQueuedTail,
		},
		{
			CanonicalName: "edgeStaffWorkOrders",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainStaff,
				AnchorType:  "workorder",
				AnchorVar:   "wo",
				Chain: []string{
					"(identity)-[:worksAt]->(work)",
					"(work)<-[:containedIn*0..]-(place)<-[:locatedAt]-(wo:workorder)",
				},
			},
			Spec: edgeStaffWorkOrdersTail,
		},
		{
			CanonicalName: "edgeProviderSchedule",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainProvider,
				AnchorType:  "appointment",
				AnchorVar:   "appt",
				Chain: []string{
					"(identity)<-[:identifiedBy]-(pr:provider)<-[:withProvider]-(appt:appointment)",
				},
			},
			Spec: edgeProviderScheduleTail,
		},
		{
			CanonicalName: "edgeProviderQueue",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainProvider,
				AnchorType:  "service",
				AnchorVar:   "inst",
				Chain: []string{
					"(identity)<-[:identifiedBy]-(sp:serviceprovider)<-[:providedBy]-(tpl:service)<-[:instanceOf]-(inst:service)",
				},
			},
			Spec: edgeProviderQueueTail,
		},
		{
			CanonicalName: "edgeInstructorSessions",
			Class:         "meta.lens",
			Adapter:       "nats-subject",
			SubjectPrefix: manifestSubjectPrefix,
			Stream:        manifestStream,
			Personal:      true,
			Engine:        "full",
			IntoKey:       []string{"__actor", "ns", "entityId"},
			Walk: &pkgmgr.AnchorWalk{
				GrantDomain: domainProvider,
				AnchorType:  "session",
				AnchorVar:   "sess",
				Chain: []string{
					"(identity)<-[:identifiedBy]-(instr:instructor)<-[:ledBy]-(sess:session)",
				},
			},
			Spec: edgeInstructorSessionsTail,
		},
	}
}

// ReadGrantDomains declares the three cap-read slices this package owns. pkgmgr
// generates one actorAggregate producer lens per entry, in this order, appended
// after the declared lenses — which is the order manifest.yaml lists them in.
func ReadGrantDomains() []pkgmgr.ReadGrantDomainSpec {
	return []pkgmgr.ReadGrantDomainSpec{
		{Name: domainStaff},
		{Name: domainBase},
		{Name: domainProvider},
	}
}

const (
	// The cap-read domains, one generated producer each:
	// cap-read.<domain>.<actorSuffix>.
	domainBase     = "edgeManifest"
	domainStaff    = "edgeManifestStaff"
	domainProvider = "edgeManifestProvider"

	// manifestSubjectPrefix + manifestStream are the shared Personal Lens
	// transport every edge-manifest lens rides — the same SYNC stream +
	// lattice.sync.user.<actor> subject prefix Fire 0 provisioned
	// (edge-showcase-app-design.md §3.1: "delivered over the shipped SYNC
	// plane").
	manifestSubjectPrefix = "lattice.sync.user"
	manifestStream        = "SYNC"
)

// Chain clauses several walks share. Prefix factoring in the generated producer
// keys on TEXTUAL identity of leading clauses, so sharing a named constant is
// what keeps four resident walks binding the residence chain once instead of
// four times — a fan-out difference, not a correctness one.
const (
	// chainResidence is the resident reachability spine: residesIn to a
	// location, then an unbounded (possibly zero-hop) containedIn walk up the
	// location hierarchy.
	chainResidence = "(identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)"

	// chainAvailableTemplates reaches service templates back off each
	// container. availableAt's source is required-live-template
	// (service-location/ddls.go), so every `tpl` matched here IS a template —
	// no class filter needed.
	chainAvailableTemplates = "(container)<-[:availableAt]-(tpl:service)"

	// chainHeldRoles is the staff spine: the roles the actor holds, which both
	// the role-standing-grant catalog path and the role queue hang off.
	chainHeldRoles = "(identity)-[:holdsRole]->(role:role)"
)

// edgeIdentitySpec projects the single `manifest.me` row: who the actor is,
// their standing roles, and their residence anchor(s) (edge-showcase-app-
// design.md §3.2). It is the one SELF-anchored lens — `anchor` is the
// identity's own key, covered by the platform base cap-read self-grant, so it
// declares no Walk and carries its whole cypher. Anchored non-optionally on the
// identity itself so exactly one row is always produced (Personal:true
// re-executes per recipient, so "the identity" is always this identity);
// roles/anchors collect via OPTIONAL MATCH the same way myTasks/
// capabilityEphemeral do — a degenerate {key:null,...} entry when the identity
// holds no role / has no residence is expected and, per the design's own
// renderer-obligations note (§3.2, inherited from the my-tasks corpus), dropped
// client-side. `anchor` is required by projection.personalEnvelopeFn: every
// Personal Lens row must alias a Contract #1 vertex key to `anchor` or the row
// is silently declined as a hollow/degenerate delegation row.
//
// The name is projected twice on purpose (display-name-convention-design.md
// §3 N3). `name` is a sensitive aspect, so the Processor seals it at rest
// (step 6.5) and `identity.name.data.value` resolves to null on any stack
// with a Vault — the plaintext genuinely cannot reach a broadcast KV row,
// and this lens declares no SecureColumns because putting identity PII in
// one is exactly what the crypto-shredding design rejected. `sealedName`
// therefore carries the { ct, nonce, keyId } envelope itself, which the edge
// engine decrypts in memory for its own identity (internal/edge/vault's
// SelfName). `displayName` still resolves directly on a stack whose
// sensitive aspects were never sealed (an in-process harness with no Vault),
// so both paths land on the same field the renderer reads.
//
// `selfAnchors` is the typed self-anchor set an op meta's
// dispatch.contextParams addresses as `{me.<type>}` (OpDispatchSpec's
// ContextParams vocabulary): each entry is {type, key}, where `type` is a
// literal stamped per walk rather than parsed out of the key — the engine
// has no vertex-type-from-key function, and the type is a declaration of
// what the walk means, not a derivation from what it returned. One
// OPTIONAL MATCH per anchor type; adding a type is one more walk plus one
// more collect entry. Five types ship: `leaseapp` (a resident's own lease
// application), `workplace` (a staff actor's worksAt location — the
// anchor a standing staff op like ReportIssue fills its `location` from, so
// the form asks only for what is genuinely typed), and the three
// provider-archetype bindings (persona-worlds-design.md Fire W0) —
// `provider` (clinic), `instructor` (wellness), `serviceprovider`
// (service-domain) — each an inbound `identifiedBy` walk exactly mirroring
// the leaseapp shape (the provider entity is the later-arriving source of
// `identifiedBy`, so the walk into the pre-existing identity runs
// backwards). Contract #1 direction: `worksAt` runs forwards off
// the identity and is matched once for the `anchors` grouping already. A degenerate {key:null} entry when
// the identity holds none of these bindings is the same expected shape
// roles/anchors carry and is dropped client-side.
//
// `anchors` is the same bindings seen from the OTHER side: not "which vertex
// does `{me.<type>}` resolve to" but "which world am I in, and by what
// relation" — so every entry is `relation`-stamped and carries a display
// name. The three `identifiedBy` bindings appear in both sets for that
// reason, and it is not a duplication to collapse: `selfAnchors` is
// dispatch plumbing keyed by type, `anchors` is the provenance the renderer
// groups hats by (persona-worlds-design.md §3.4 — home / work / services).
// The provider entities carry their display name on `.profile`, under the
// field each domain declared for it (`fullName` for a clinic provider,
// `displayName` for an instructor and a service provider); a binding whose
// profile is absent resolves to a null name and degrades to the renderer's
// typed floor rather than a bare NanoID.
const edgeIdentitySpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:residesIn]->(loc)
OPTIONAL MATCH (loc)-[:containedIn]->(container)
OPTIONAL MATCH (identity)-[:worksAt]->(work)
OPTIONAL MATCH (work)-[:containedIn]->(workContainer)
OPTIONAL MATCH (identity)<-[:applicationFor]-(leaseapp:leaseapp)
OPTIONAL MATCH (identity)<-[:identifiedBy]-(prov:provider)
OPTIONAL MATCH (identity)<-[:identifiedBy]-(instr:instructor)
OPTIONAL MATCH (identity)<-[:identifiedBy]-(sp:serviceprovider)
RETURN
  identity.key AS anchor,
  "manifest.me" AS ns,
  identity.key AS identityKey,
  identity.name.data.value AS displayName,
  identity.name.data AS sealedName,
  (identity.state.data.value = "claimed") AS claimed,
  collect(DISTINCT {key: role.key, name: role.canonicalName.data.value}) AS roles,
  collect(DISTINCT {key: loc.key, name: loc.presentation.data.name, container: container.key, containerName: container.presentation.data.name, relation: 'residesIn'}) +
  collect(DISTINCT {key: work.key, name: work.presentation.data.name, container: workContainer.key, containerName: workContainer.presentation.data.name, relation: 'worksAt'}) +
  collect(DISTINCT {key: prov.key, name: prov.profile.data.fullName, type: 'provider', relation: 'identifiedBy'}) +
  collect(DISTINCT {key: instr.key, name: instr.profile.data.displayName, type: 'instructor', relation: 'identifiedBy'}) +
  collect(DISTINCT {key: sp.key, name: sp.profile.data.displayName, type: 'serviceprovider', relation: 'identifiedBy'}) AS anchors,
  collect(DISTINCT {type: 'leaseapp', key: leaseapp.key}) +
  collect(DISTINCT {type: 'workplace', key: work.key}) +
  collect(DISTINCT {type: 'provider', key: prov.key}) +
  collect(DISTINCT {type: 'instructor', key: instr.key}) +
  collect(DISTINCT {type: 'serviceprovider', key: sp.key}) AS selfAnchors
`

// edgeServicesTail presents one `manifest.svc.<tplId>` row per service template
// the residence walk reaches. `<> null` is this engine's null test (its grammar
// accepts, but silently mis-evaluates, `IS NOT NULL` — full/visitor.go; do not
// "correct" this to IS NOT NULL).
const edgeServicesTail = `
OPTIONAL MATCH (tpl)-[:providedBy]->(provider)
WITH tpl, provider, container
WHERE tpl.key <> null
RETURN
  tpl.key AS anchor,
  "manifest.svc" AS ns,
  nanoIdFromKey(tpl.key) AS entityId,
  tpl.key AS serviceKey,
  tpl.presentation.data.name AS name,
  tpl.presentation.data.description AS description,
  tpl.presentation.data.icon AS icon,
  tpl.presentation.data.category AS category,
  provider.key AS providerKey,
  container.key AS resolvedVia
`

// edgeCatalogTail presents one `manifest.op.<opMetaId>` row per op meta the
// walk reaches through a service template (§3.3's descriptor vocabulary, read
// back off the op meta's optional aspects — an op meta that never adopted the
// vocabulary still projects a row, just with those fields null, per §3.3 "ops
// without descriptors still render, degraded").
//
// viaServices answers "which service(s) offer this op" without a WITH/collect
// grouping stage — it reuses the pattern-comprehension-in-RETURN form
// service-location/lenses.go's `allowedOperations` already proves parses under
// this engine, just walked in the reverse direction from `op`. This is
// presentation only (design §4.5: the manifest affects visibility, never
// permission), so a global (not actor-scoped) permitsOperation fan-in is an
// acceptable v1 narrowing, same class as the other named scope-downs above.
const edgeCatalogTail = `
WITH op
WHERE op.key <> null
RETURN
  op.key AS anchor,
  "manifest.op" AS ns,
  nanoIdFromKey(op.key) AS entityId,
  op.key AS opMetaKey,
  op.data.operationType AS operationType,
  op.presentation.data.title AS title,
  op.presentation.data.shortLabel AS shortLabel,
  op.presentation.data.description AS description,
  op.presentation.data.icon AS icon,
  op.presentation.data.tone AS tone,
  op.presentation.data.submitLabel AS submitLabel,
  op.presentation.data.group AS group,
  op.inputSchema.data.schema AS inputSchema,
  op.fieldDescriptions.data.fieldDescriptions AS fieldDescriptions,
  op.dispatch.data.class AS dispatchClass,
  op.dispatch.data.authContext AS dispatchAuthContext,
  op.dispatch.data.targetField AS dispatchTargetField,
  op.dispatch.data.targetType AS dispatchTargetType,
  op.dispatch.data.contextParams AS dispatchContextParams,
  op.dispatch.data.reads AS dispatchReads,
  op.dispatch.data.optionalReads AS dispatchOptionalReads,
  op.sensitive.data.value AS sensitive,
  [(op)<-[:permitsOperation]-(svc:service) | svc.key] AS viaServices
`

// edgeTasksTail presents one `manifest.task.<taskId>` row per task directly
// assignedTo the actor and still open (Contract #10 §10.1 link-sourced shape,
// mirrored from orchestration-base's myTasksSpec). The open-status filter is a
// PRESENTATION narrowing, not a reachability one — the walk grants the task
// anchor regardless of status, which is the correct asymmetry (grant ⊇
// projection).
//
// scopedName projects the display name of the task's scoped target
// (class-4 relational label, display-name-convention-design.md §2), from
// whichever of two subjects the target actually has: a SignLease task
// scopedTo a leaseapp carries the applied-for unit's `.presentation` name
// ("Unit 1 lease"), and a maintenance task scopedTo a work order carries
// that order's own `.report` summary ("Boiler in the basement is cycling").
// Both ride inline on the already-readable task row, the `templateName`
// idiom on edgeInstances — no separate read-grant. The work-order summary is
// safe to carry here precisely because maintenance work is unit/equipment-
// scoped and its summary is declared PII-free (D3 forbids plaintext identity
// PII on the SYNC plane, which is why NO name arrives this way). Null when
// the target is neither, and the renderer falls to its typed floor.
const edgeTasksTail = `
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (tgt)-[:appliesToUnit]->(scopedUnit:unit)
WITH task, identity, op, tgt, scopedUnit
WHERE task.data.status = "open"
RETURN
  task.key AS anchor,
  "manifest.task" AS ns,
  nanoIdFromKey(task.key) AS entityId,
  task.key AS taskKey,
  identity.key AS assignee,
  op.key AS forOperationKey,
  op.data.operationType AS operationType,
  tgt.key AS scopedTo,
  (CASE WHEN scopedUnit.presentation.data.name <> null THEN scopedUnit.presentation.data.name
        ELSE tgt.report.data.summary END) AS scopedName,
  task.data.expiresAt AS expiresAt
`

// edgeInstancesTail presents one `manifest.inst.<instId>` row per service
// instance providedTo the actor — "my orders" (§3.2). status derives from
// the instance's optional `.outcome` aspect: absent ⇒ "open" (no external
// result recorded yet), present ⇒ the outcome's own status
// (completed|failed). The generic CASE form (WHEN <cond> THEN <result>)
// is this engine's only supported CASE shape (full/visitor.go
// visitCaseExpression) — the simple `CASE <expr> WHEN <value>` form is
// rejected.
const edgeInstancesTail = `
OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
WITH inst, tpl
WHERE inst.key <> null
RETURN
  inst.key AS anchor,
  "manifest.inst" AS ns,
  nanoIdFromKey(inst.key) AS entityId,
  inst.key AS instanceKey,
  tpl.key AS templateKey,
  tpl.presentation.data.name AS templateName,
  tpl.presentation.data.icon AS templateIcon,
  (CASE WHEN inst.outcome.data.status <> null THEN inst.outcome.data.status ELSE "open" END) AS status,
  inst.outcome.data.status AS outcome,
  inst.outcome.data.completedAt AS completedAt
`

// edgeEntitySessionsTail presents one `manifest.ent.<sessionId>` row per
// wellness class session the residence walk reaches — the browse rows that give
// a declared `dispatch.targetType: "session"` something to resolve against
// (facet-entity-browse-design.md §3 F2). The walk reaches them through
// wellness-domain's authZ-free `studio locatedAt location` link off the same
// containment spine edgeServices uses (NEVER availableAt — that edge is
// service-access authZ, §3 F1). `entityType` is a literal stamped per walk,
// exactly as edgeIdentity's selfAnchors stamps its type — the engine has no
// vertex-type-from-key function, and the type is a declaration of what the walk
// means. One lens per entity kind: the engine has no UNION, and a row-per-entity
// cypher carrying two unrelated kinds would cross-product. The schedule instant
// projects as `startsAt`, not the design's `when` — WHEN is a CASE keyword in
// this engine's lexer and an alias by that name fails to parse.
const edgeEntitySessionsTail = `
WITH sess, studio
WHERE sess.key <> null
RETURN
  sess.key AS anchor,
  "manifest.ent" AS ns,
  nanoIdFromKey(sess.key) AS entityId,
  sess.key AS entityKey,
  "session" AS entityType,
  sess.schedule.data.name AS title,
  studio.profile.data.name AS subtitle,
  sess.schedule.data.startsAt AS startsAt
`

// edgeEntityProvidersTail presents one `manifest.ent.<providerId>` row per
// clinic provider the residence walk reaches — the browse rows for
// `dispatch.targetType: "provider"` (CreateAppointment). The provider's own
// `practicesAt` link (clinic-domain site.go) already lands on a location-domain
// building, so the walk needs no new DDL anywhere. Same row shape as
// edgeEntitySessions minus `startsAt` (a provider is not a scheduled thing);
// the renderer treats the shape generically by entityType.
const edgeEntityProvidersTail = `
WITH prov
WHERE prov.key <> null
RETURN
  prov.key AS anchor,
  "manifest.ent" AS ns,
  nanoIdFromKey(prov.key) AS entityId,
  prov.key AS entityKey,
  "provider" AS entityType,
  prov.profile.data.fullName AS title,
  prov.profile.data.specialty AS subtitle
`

// edgeEntityBookingsTail presents one `manifest.ent.<bookingId>` row per
// booking the actor themself made — the browse rows for
// `dispatch.targetType: "booking"` (CancelBooking).
//
// The walk is the actor's OWN `bookedBy` link, NOT the residence spine the
// session/provider lenses use. Locality is the wrong predicate for a booking in
// both directions: it would surface co-residents' bookings (a booking is
// nobody's business but the booker's) and it would drop the actor's own booking
// at a studio outside their building. That makes this the first manifest.ent
// lens whose row set is inherently private rather than merely locality-scoped.
//
// `sessionKey` rides along because CancelBooking needs the booking's session
// in its payload (the seat-cell key it tombstones is rebuilt from it) and the
// renderer fills that from the viewed row via `{entity.<column>}`. A
// cancelled booking tombstones its own vertex, so the row self-clears with no
// status filter.
const edgeEntityBookingsTail = `
OPTIONAL MATCH (bk)-[:forSession]->(sess:session)
OPTIONAL MATCH (sess)-[:atStudio]->(studio:studio)
WITH bk, sess, studio
WHERE bk.key <> null
RETURN
  bk.key AS anchor,
  "manifest.ent" AS ns,
  nanoIdFromKey(bk.key) AS entityId,
  bk.key AS entityKey,
  "booking" AS entityType,
  sess.schedule.data.name AS title,
  studio.profile.data.name AS subtitle,
  sess.schedule.data.startsAt AS startsAt,
  sess.key AS sessionKey
`

// edgeCatalogRolesTail presents one `manifest.op.<opMetaId>` row per op meta the
// actor reaches through a ROLE they hold, rather than through a service
// template (staff-worlds F2). The walk's last hop is the install-time edge
// pkgmgr mints beside `grantedBy` (internal/pkgmgr/build.go): without it the
// walk dead-ends at perm.data.operationType, a STRING this engine cannot join
// to a vertex.
//
// A sibling lens rather than more branches on edgeCatalog, for the same reason
// the entity lenses are siblings: this engine has no UNION, so folding a second
// independent reachability path into one cypher cross-products it. Same `ns`
// and same RETURN shape as edgeCatalog means the renderer needs to know nothing
// about which path a row arrived by, and an op reachable BOTH ways projects the
// identical row under the identical key — an LWW-idempotent overlap, noted
// rather than feared.
//
// This is the lens that makes the staff catalog honest: it derives visibility
// from the grant topology the Processor actually authorizes against, so the
// catalog cannot drift into offering ops step 3 will deny. It also closes the
// named "browse all my ops" gap for ordinary residents, whose consumer-role
// grants project through exactly the same walk.
const edgeCatalogRolesTail = `
WITH op, role
WHERE op.key <> null
RETURN
  op.key AS anchor,
  "manifest.op" AS ns,
  nanoIdFromKey(op.key) AS entityId,
  op.key AS opMetaKey,
  op.data.operationType AS operationType,
  op.presentation.data.title AS title,
  op.presentation.data.shortLabel AS shortLabel,
  op.presentation.data.description AS description,
  op.presentation.data.icon AS icon,
  op.presentation.data.tone AS tone,
  op.presentation.data.submitLabel AS submitLabel,
  op.presentation.data.group AS group,
  op.inputSchema.data.schema AS inputSchema,
  op.fieldDescriptions.data.fieldDescriptions AS fieldDescriptions,
  op.dispatch.data.class AS dispatchClass,
  op.dispatch.data.authContext AS dispatchAuthContext,
  op.dispatch.data.targetField AS dispatchTargetField,
  op.dispatch.data.targetType AS dispatchTargetType,
  op.dispatch.data.contextParams AS dispatchContextParams,
  op.dispatch.data.reads AS dispatchReads,
  op.dispatch.data.optionalReads AS dispatchOptionalReads,
  op.sensitive.data.value AS sensitive,
  role.key AS viaRole,
  role.canonicalName.data.value AS viaRoleName
`

// edgeTasksQueuedTail presents one `manifest.task.<taskId>` row per OPEN task
// queued to a role the actor holds (FR28). The walk
// (identity)-[:holdsRole]->(role)<-[:queuedFor]-(task) is the one
// orchestration-base's my-tasks aggregate already runs verbatim; this re-emits
// it per-row over the personal SYNC transport a Facet device mirrors.
//
// queuedRole names the governing role, which is what distinguishes these rows
// from edgeTasks' directly-assigned ones: a queued task is CLAIMABLE, not yet
// owned. The renderer's claim affordance submits the shipped ClaimTask, whose
// atomic queuedFor→assignedTo swap then stops this branch matching for every
// non-claimant and materializes the edgeTasks row for the winner — so the whole
// claim beat is existing machinery, reached over a new projection.
const edgeTasksQueuedTail = `
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (tgt)-[:appliesToUnit]->(scopedUnit:unit)
WITH task, op, tgt, scopedUnit, role
WHERE task.data.status = "open"
RETURN
  task.key AS anchor,
  "manifest.task" AS ns,
  nanoIdFromKey(task.key) AS entityId,
  task.key AS taskKey,
  op.key AS forOperationKey,
  op.data.operationType AS operationType,
  tgt.key AS scopedTo,
  (CASE WHEN scopedUnit.presentation.data.name <> null THEN scopedUnit.presentation.data.name
        ELSE tgt.report.data.summary END) AS scopedName,
  task.data.expiresAt AS expiresAt,
  role.key AS queuedRole,
  role.canonicalName.data.value AS queuedRoleName
`

// edgeStaffWorkOrdersTail presents one `manifest.work.<workOrderId>` row per
// maintenance work order at a place the actor worksAt — FORK-S1 A's per-row
// domain worklist, the half of a staff world that is MIRROR rather than
// server-pane.
//
// Why this is a different lens from edgeTasksQueued rather than a column on
// it: a task row exists only because somebody queued a task. A work order at
// your building with no task on it — unqueued, claimed by a colleague, or
// already resolved — is domain state your world should still show, and it is
// what §3.6 means by "what work exists at my workplace" as opposed to "what
// has been handed to me." The two lenses answer different questions and a
// device offline needs both.
//
// The walk runs DOWN from the workplace: `(work)<-[:containedIn*0..]-(place)`
// enumerates the workplace itself (0 hops) and everything contained in it
// transitively, then `(place)<-[:locatedAt]-(wo:workorder)` takes the orders
// at each. Every other variable-length walk in this package runs UP (a
// resident's residence to its containers), because that is the direction a
// resident's reachability has; a staff actor's is the mirror image — you work
// at the building, the work is in the units. The engine handles it with the
// same code either way (executor.traverseRel filters each hop through
// directionMatches, and Adjacency records every edge under both endpoints).
//
// D3 holds without effort here, and that is the whole reason FORK-S1 A is
// safe to build: a work order carries a summary, a priority and a place —
// unit/equipment-scoped facts. No identity, no name, nothing sealed. The one
// field that could carry resident PII is the free-text summary, which is why
// maintenance-domain's own field description tells the reporter to keep it
// out; nothing on this row can leak a name because no name is joined into it.
//
// `status` derives from the `.resolution` aspect's presence, the same
// read-before-write terminal marker ResolveWorkOrder consults — so a resolve
// that drains after a device reconnects flips this row to "resolved" on the
// mirror without any second write to model it.
const edgeStaffWorkOrdersTail = `
WITH wo, place, work
WHERE wo.key <> null
RETURN
  wo.key AS anchor,
  "manifest.work" AS ns,
  nanoIdFromKey(wo.key) AS entityId,
  wo.key AS workOrderKey,
  wo.report.data.summary AS summary,
  wo.report.data.priority AS priority,
  wo.report.data.reportedAt AS reportedAt,
  place.key AS placeKey,
  place.presentation.data.name AS placeName,
  work.key AS workplaceKey,
  (CASE WHEN wo.resolution.data.resolvedAt <> null THEN "resolved" ELSE "open" END) AS status,
  wo.resolution.data.resolvedAt AS resolvedAt,
  wo.resolution.data.notes AS resolutionNotes
`

// edgeProviderScheduleTail presents one `manifest.ent.<appointmentId>` row per
// clinic appointment the actor's OWN bound provider is withProvider of — the
// provider-hat schedule (persona-worlds-design.md Fire W0). The walk is the
// actor's own inbound `identifiedBy` binding to a clinic provider, NOT the
// residence spine the browse lenses use: a provider's schedule is private to
// that provider, never locality-scoped.
//
// The namespace is deliberately `manifest.ent` with `entityType: "appointment"`
// (not a bespoke `manifest.sched` namespace): the renderer only knows the
// seven shipped namespaces, and entityType must equal the entityKey's own
// vtx-type segment for op-attach + payload-resolve to work, exactly like
// every other manifest.ent row.
//
// No patient name is projected (D3 — no plaintext identity PII on the SYNC
// plane): `title` carries the visit reason (an appointment's own .schedule
// data, not a person's), `subtitle` the status.
const edgeProviderScheduleTail = `
WITH appt, pr
WHERE appt.key <> null
RETURN
  appt.key AS anchor,
  "manifest.ent" AS ns,
  nanoIdFromKey(appt.key) AS entityId,
  appt.key AS entityKey,
  "appointment" AS entityType,
  appt.schedule.data.reason AS title,
  appt.status.data.value AS subtitle,
  appt.schedule.data.startsAt AS startsAt,
  appt.schedule.data.endsAt AS endsAt,
  pr.key AS providerKey
`

// edgeProviderQueueTail presents one `manifest.ent.<instanceId>` row per
// service instance whose template is providedBy the actor's OWN bound
// serviceprovider — the provider-hat work queue (persona-worlds-design.md
// Fire W0): "what runs do I need to complete". The walk goes through the
// actor's own inbound `identifiedBy` binding to a serviceprovider, then that
// serviceprovider's providedBy templates, then each template's instances
// (instanceOf, NOT providedTo — the instance→template direction, mirroring
// edgeInstances' own walk). No startsAt: a service instance is always-current
// work, not a scheduled thing (unlike a session/appointment).
//
// `subtitle` mirrors edgeInstancesTail's status CASE idiom exactly: absent
// `.outcome` reads "open" (not yet recorded), present reads the outcome's
// own status.
const edgeProviderQueueTail = `
WITH inst, tpl, sp
WHERE inst.key <> null
RETURN
  inst.key AS anchor,
  "manifest.ent" AS ns,
  nanoIdFromKey(inst.key) AS entityId,
  inst.key AS entityKey,
  "service" AS entityType,
  tpl.presentation.data.name AS title,
  (CASE WHEN inst.outcome.data.status <> null THEN inst.outcome.data.status ELSE "open" END) AS subtitle,
  tpl.key AS templateKey,
  sp.key AS serviceproviderKey
`

// edgeInstructorSessionsTail presents one `manifest.ent.<sessionId>` row per
// wellness session the actor's OWN bound instructor leads — the provider-hat
// "my classes to teach" queue (persona-worlds-design.md Fire W0). The walk goes
// through the actor's own inbound `identifiedBy` binding to an instructor, then
// that instructor's `ledBy`-inverse sessions, NOT the residence spine
// edgeEntitySessions uses: an instructor teaches wherever they're assigned, not
// only where they happen to live.
//
// The RETURN is byte-identical to edgeEntitySessionsTail's (same aliases,
// same title/subtitle/startsAt expressions, same entityType "session") on
// purpose: a resident who is ALSO the instructor of a session reachable by
// BOTH walks projects the identical row under the identical key — an
// LWW-idempotent overlap, the same pattern edgeCatalogRolesTail already
// proves for a dual-reachable op meta.
const edgeInstructorSessionsTail = `
OPTIONAL MATCH (sess)-[:atStudio]->(studio:studio)
WITH sess, studio
WHERE sess.key <> null
RETURN
  sess.key AS anchor,
  "manifest.ent" AS ns,
  nanoIdFromKey(sess.key) AS entityId,
  sess.key AS entityKey,
  "session" AS entityType,
  sess.schedule.data.name AS title,
  studio.profile.data.name AS subtitle,
  sess.schedule.data.startsAt AS startsAt
`
