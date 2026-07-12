package edgemanifest

import "github.com/asolgan/lattice/internal/pkgmgr"

// Lenses returns the package's five Personal-Lens declarations
// (edge-showcase-app-design.md §3.2) — the first real `nats-subject` /
// Personal Lens package in the repo (the adapter plumbing shipped latent in
// Fire 0; this is its first production consumer). Every lens is
// Personal:true (Refractor's cross-vertex fan-out re-executes the cypher
// once per reachable identity, binding $actorKey to that identity's own
// key — personal-secure-lens-design.md §3.3), delivers over the shared
// `lattice.sync.user.<actor>` subject (SubjectPrefix/Stream), and keys its
// rows under the reserved `manifest.` namespace via IntoKey's dot-join
// (edge/store.go's ApplyUpsert/ApplyDelete carry a matching exemption for
// this prefix, since a `manifest.*` key is a projection-row key, not a
// Contract #1 key).
//
// $actorKey is NOT the "current identity" for actor-aggregate lenses (those
// bind $actorKey to whichever vertex was mutated); for a Personal:true lens
// it is always the enumerated recipient identity's own key — every cypher
// below anchors `MATCH (identity:identity {key: $actorKey})` on exactly
// that basis.
//
// v1 scope-downs (named, not silent — each is a reasonable narrowing the
// engine or the data model makes convenient to defer, not a correctness
// gap in what IS built): edgeIdentity's anchors/roles arrays carry only
// {key, ...} — no human-readable location type/label (the engine has no
// vertex-type-from-key function outside nanoIdFromKey, and no string
// concatenation to synthesize one); edgeCatalog covers only the
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
const edgeIdentitySpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:residesIn]->(loc)
OPTIONAL MATCH (loc)-[:containedIn]->(container)
RETURN
  identity.key AS anchor,
  "manifest.me" AS ns,
  identity.key AS identityKey,
  identity.name.data.value AS displayName,
  (identity.state.data.value = "claimed") AS claimed,
  collect(DISTINCT {key: role.key, name: role.canonicalName.data.value}) AS roles,
  collect(DISTINCT {key: loc.key, container: container.key}) AS anchors
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
  op.dispatch.data.contextParams AS dispatchContextParams,
  op.dispatch.data.reads AS dispatchReads,
  op.sensitive.data.value AS sensitive
`

// edgeTasksSpec projects one `manifest.task.<taskId>` row per task directly
// assignedTo the actor and still open (Contract #10 §10.1 link-sourced
// shape, mirrored from orchestration-base's myTasksSpec). v1 scope: direct
// assignedTo only — FR28 role-queued tasks are deferred (see the package
// doc comment above).
const edgeTasksSpec = `
MATCH (identity:identity {key: $actorKey})<-[:assignedTo]-(task:task)
WHERE task.data.status = "open"
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
RETURN
  task.key AS anchor,
  "manifest.task" AS ns,
  nanoIdFromKey(task.key) AS entityId,
  task.key AS taskKey,
  identity.key AS assignee,
  op.key AS forOperationKey,
  op.data.operationType AS operationType,
  tgt.key AS scopedTo,
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
