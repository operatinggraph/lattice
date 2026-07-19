package edgemanifest

import "github.com/asolgan/lattice/internal/pkgmgr"

// Lenses returns the package's five Personal-Lens declarations
// (edge-showcase-app-design.md §3.2) — the first real `nats-subject` /
// Personal Lens package in the repo (the adapter plumbing shipped latent in
// Fire 0; this is its first production consumer) — plus ONE read-grant
// PRODUCER lens, edgeManifestReadGrants (Fire 2 fix, added building
// cmd/facet): every non-self-anchored Personal Lens row (edgeServices/
// edgeCatalog/edgeTasks/edgeInstances each anchor on a vertex OTHER than the
// recipient identity — a service template, op meta, task, or instance) is
// silently dropped by Refractor's D1 `readableAnchors` fail-closed gate
// (internal/refractor/projection/personal.go's personalEnvelopeFn calls
// capabilityread.IsReadable, which reads the NATS-KV "capability" bucket's
// per-actor `cap-read.<domain>.<actor>` documents — Contract #6 §6.14 Path
// B — NOT the Postgres actor_read_grants table a `GrantTable:true` lens
// feeds (Path A, RLS enforcement for Protected postgres reads; irrelevant
// here since edge-manifest has no Postgres/Protected lens at all). This
// gate is orthogonal to — and never derived from — the manifest lenses' own
// cypher reachability (internal/bootstrap/lenses.go's
// CapabilityReadLensDefinition doc: "each package ships its own
// cap-read.<domain>... lens for the relationships it owns"). Fire 1 shipped
// the five manifest lenses without this read-grant half, which is why only
// edgeIdentity's self-anchored manifest.me ever reached a live tenant.
// edgeManifestReadGrants is edge-manifest's own
// `ProjectionKind:"actorAggregate"` + nats-kv `Output` descriptor lens —
// the SAME declarative shape internal/bootstrap/lenses.go's
// CapabilityReadLensDefinition uses at the kernel level, just the first
// PACKAGE to use it (every existing package cap-read producer —
// console-operator, clinic-domain — is the OTHER kind, a Postgres
// GrantTable feeding Path A; this is Path B, and edge-manifest is Path B's
// first package producer). One combined `cap-read.edgeManifest.<actor>`
// slice covers all four non-self anchor kinds (service/op/task/instance) —
// no need for four separate lenses, since the actor's effective readable
// set is already a union over every slice class, and one slice may itself
// list many anchors.
//
// Every Personal-Lens cypher below is Personal:true (Refractor's
// cross-vertex fan-out re-executes the cypher once per reachable identity,
// binding $actorKey to that identity's own key — personal-secure-lens-
// design.md §3.3), delivers over the shared `lattice.sync.user.<actor>`
// subject (SubjectPrefix/Stream), and keys its rows under the reserved
// `manifest.` namespace via IntoKey's dot-join (edge/store.go's
// ApplyUpsert/ApplyDelete carry a matching exemption for this prefix, since
// a `manifest.*` key is a projection-row key, not a Contract #1 key).
//
// $actorKey is NOT the "current identity" for actor-aggregate lenses (those
// bind $actorKey to whichever vertex was mutated); for a Personal:true lens
// it is always the enumerated recipient identity's own key — every cypher
// below anchors `MATCH (identity:identity {key: $actorKey})` on exactly
// that basis.
//
// v1 scope-downs (named, not silent — each is a reasonable narrowing the
// engine or the data model makes convenient to defer, not a correctness
// gap in what IS built): edgeIdentity's anchors carry the location's
// `.presentation.data.name` (class-2 display source, display-name-convention-
// design.md N1) plus its container's name, so a named world renders a
// human label instead of a bare NanoID; the location TYPE segment is still
// not synthesized into the row (the engine has no vertex-type-from-key
// function outside nanoIdFromKey, and no string concatenation to build one),
// so the renderer derives type from the key client-side; edgeCatalog covers only the
// service-permitsOperation reachability path (role-standing-grant and
// open-task-forOperation paths are deferred — a task's own bound op
// already rides inline on its edgeTasks row, so the gap is "browse all my
// ops," not "complete my task"); edgeTasks covers only direct `assignedTo`
// tasks (FR28 role-queued tasks are deferred, mirroring the same
// multi-path-dedup engine limits as edgeCatalog — no UNION, no list
// comprehension to filter degenerate branches apart).
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
			Spec:          edgeServicesSpec,
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
			Spec:          edgeCatalogSpec,
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
			Spec:          edgeTasksSpec,
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
			Spec:          edgeInstancesSpec,
		},
		{
			CanonicalName:  "edgeManifestReadGrants",
			Class:          "meta.lens",
			Adapter:        "nats-kv",
			Bucket:         "capability-kv",
			Engine:         "full",
			ProjectionKind: "actorAggregate",
			Output: &pkgmgr.OutputDescriptorSpec{
				AnchorType:       "identity",
				OutputKeyPattern: "cap-read.edgeManifest.{actorSuffix}",
				BodyColumns:      []string{"readableAnchors"},
				EmptyBehavior:    "delete",
				Freshness:        "auto",
				Lanes:            []string{"default"},
			},
			Spec: edgeManifestReadGrantsSpec,
		},
	}
}

const (
	// manifestSubjectPrefix + manifestStream are the shared Personal Lens
	// transport every edge-manifest lens rides — the same SYNC stream +
	// lattice.sync.user.<actor> subject prefix Fire 0 provisioned
	// (edge-showcase-app-design.md §3.1: "delivered over the shipped SYNC
	// plane").
	manifestSubjectPrefix = "lattice.sync.user"
	manifestStream        = "SYNC"
)

// edgeIdentitySpec projects the single `manifest.me` row: who the actor is,
// their standing roles, and their residence anchor(s) (edge-showcase-app-
// design.md §3.2). Anchored non-optionally on the identity itself so
// exactly one row is always produced (Personal:true re-executes per
// recipient, so "the identity" is always this identity); roles/anchors
// collect via OPTIONAL MATCH the same way myTasks/capabilityEphemeral do —
// a degenerate {key:null,...} entry when the identity holds no role / has
// no residence is expected and, per the design's own renderer-obligations
// note (§3.2, inherited from the my-tasks corpus), dropped client-side.
// `anchor` (the identity's own key — this row is never degenerate, so
// self-anchoring is correct, unlike a fan-out-reached neighbor) is
// required by projection.personalEnvelopeFn: every Personal Lens row must
// alias a Contract #1 vertex key to `anchor` or the row is silently
// declined as a hollow/degenerate delegation row (personal.go's own doc:
// "a personal lens's cypher must therefore always alias its neighbor's key
// to anchor").
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
// more collect entry. Contract #1 direction: the leaseapp is the
// later-arriving source of `applicationFor`, so the walk into the
// pre-existing identity runs backwards. A degenerate {key:null} entry when
// the identity has no lease is the same expected shape roles/anchors carry
// and is dropped client-side.
const edgeIdentitySpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:residesIn]->(loc)
OPTIONAL MATCH (loc)-[:containedIn]->(container)
OPTIONAL MATCH (identity)<-[:applicationFor]-(leaseapp:leaseapp)
RETURN
  identity.key AS anchor,
  "manifest.me" AS ns,
  identity.key AS identityKey,
  identity.name.data.value AS displayName,
  identity.name.data AS sealedName,
  (identity.state.data.value = "claimed") AS claimed,
  collect(DISTINCT {key: role.key, name: role.canonicalName.data.value}) AS roles,
  collect(DISTINCT {key: loc.key, name: loc.presentation.data.name, container: container.key, containerName: container.presentation.data.name}) AS anchors,
  collect(DISTINCT {type: 'leaseapp', key: leaseapp.key}) AS selfAnchors
`

// edgeServicesSpec projects one `manifest.svc.<tplId>` row per service
// template reachable via the actor's residence chain: residesIn to a
// location, an unbounded (possibly zero-hop) containedIn walk up the
// location hierarchy, then availableAt reached backwards from each
// container (availableAt's source is required-live-template,
// service-location/ddls.go, so every `tpl` matched here IS a template —
// no class filter needed). `<> null` is this engine's null test (its
// grammar accepts, but silently mis-evaluates, `IS NOT NULL` — full/
// visitor.go; do not "correct" this to IS NOT NULL).
const edgeServicesSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
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

// edgeCatalogSpec projects one `manifest.op.<opMetaId>` row per op meta
// reachable via a service template the actor can reach (§3.3's descriptor
// vocabulary, read back off the op meta's optional aspects — an op meta
// that never adopted the vocabulary still projects a row, just with those
// fields null, per §3.3 "ops without descriptors still render, degraded").
// v1 scope: the service-permitsOperation path only (see the package doc
// comment above for the two deferred paths).
//
// viaServices (added Fire 2, facet-app-ux.md §3.3 Service detail) answers
// "which service(s) offer this op" without a WITH/collect grouping stage —
// it reuses the pattern-comprehension-in-RETURN form service-location/
// lenses.go's `allowedOperations` already proves parses under this engine,
// just walked in the reverse direction from `op`. This is presentation
// only (design §4.5: the manifest affects visibility, never permission),
// so a global (not actor-scoped) permitsOperation fan-in is an acceptable
// v1 narrowing, same class as the other named scope-downs above.
const edgeCatalogSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
OPTIONAL MATCH (tpl)-[:permitsOperation]->(op:meta)
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
  op.sensitive.data.value AS sensitive,
  [(op)<-[:permitsOperation]-(svc:service) | svc.key] AS viaServices
`

// edgeTasksSpec projects one `manifest.task.<taskId>` row per task directly
// assignedTo the actor and still open (Contract #10 §10.1 link-sourced
// shape, mirrored from orchestration-base's myTasksSpec). v1 scope: direct
// assignedTo only — FR28 role-queued tasks are deferred (see the package
// doc comment above).
//
// scopedName projects the display name of the task's scoped target's subject
// (class-4 relational label, display-name-convention-design.md §2): a
// SignLease task scopedTo a leaseapp carries the applied-for unit's
// `.presentation` name, so the renderer composes "Unit 1 lease" instead of a
// bare NanoID. Mirrors the `templateName` idiom on edgeInstances — rides
// inline on the already-readable task row, no separate read-grant. Null when
// the target has no `appliesToUnit` subject (non-leaseapp scopes fall to the
// renderer's typed floor).
const edgeTasksSpec = `
MATCH (identity:identity {key: $actorKey})<-[:assignedTo]-(task:task)
WHERE task.data.status = "open"
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (tgt)-[:appliesToUnit]->(scopedUnit:unit)
RETURN
  task.key AS anchor,
  "manifest.task" AS ns,
  nanoIdFromKey(task.key) AS entityId,
  task.key AS taskKey,
  identity.key AS assignee,
  op.key AS forOperationKey,
  op.data.operationType AS operationType,
  tgt.key AS scopedTo,
  scopedUnit.presentation.data.name AS scopedName,
  task.data.expiresAt AS expiresAt
`

// edgeInstancesSpec projects one `manifest.inst.<instId>` row per service
// instance providedTo the actor — "my orders" (§3.2). status derives from
// the instance's optional `.outcome` aspect: absent ⇒ "open" (no external
// result recorded yet), present ⇒ the outcome's own status
// (completed|failed). The generic CASE form (WHEN <cond> THEN <result>)
// is this engine's only supported CASE shape (full/visitor.go
// visitCaseExpression) — the simple `CASE <expr> WHEN <value>` form is
// rejected.
const edgeInstancesSpec = `
MATCH (identity:identity {key: $actorKey})<-[:providedTo]-(inst:service)
OPTIONAL MATCH (inst)-[:instanceOf]->(tpl:service)
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

// edgeManifestReadGrantsSpec is edge-manifest's single combined read-grant
// producer for all four non-self-anchored manifest lenses (edgeServices,
// edgeCatalog, edgeTasks, edgeInstances) — an actorAggregate lens mirroring
// internal/bootstrap/lenses.go's CapabilityReadLensDefinition shape exactly
// (one row per actor, a readableAnchors[] array), just walking THIS
// package's reachability chains instead of the trivial self-anchor.
//
// Each anchor kind's OPTIONAL MATCH is independent of the others (no shared
// WITH-scoping), so a tenant reachable to N services and M tasks produces
// an N×M intermediate row fan-out before collect(DISTINCT ...) re-dedupes
// each column — correct (DISTINCT drops the cross-product duplicates), not
// maximally efficient, but the same multi-branch collect()+concat shape
// packages/orchestration-base's ephemeralGrants lens already proves parses
// and executes under this engine (no UNION, no list comprehension across
// independent branches — string "+" is the only concatenation this engine
// has, and it works on two collect() results the same way it works on two
// strings).
const edgeManifestReadGrantsSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
OPTIONAL MATCH (tpl)-[:permitsOperation]->(op:meta)
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(tpl.key), via: ['availableAt']}) +
  collect(DISTINCT {anchorType: 'meta', anchorId: nanoIdFromKey(op.key), via: ['permitsOperation']}) +
  collect(DISTINCT {anchorType: 'task', anchorId: nanoIdFromKey(task.key), via: ['assignedTo']}) +
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(inst.key), via: ['providedTo']})
  AS readableAnchors
`
