package edgemanifest

// The package declares each non-self-anchored Personal lens's actor→anchor
// reachability once, as an AnchorWalk, and pkgmgr compiles both the lens's own
// cypher and its read-grant producer from it. So every test that needs a real
// executable cypher — or the producers at all — consumes the EXPANDED
// Definition, exactly as the installer does.
//
// This file also carries the migration proof for the walk conversion. Two of
// the shapes pkgmgr composes are not a verbatim splice of what a hand author
// wrote before:
//
//   - five lenses fused their chain into a REQUIRED `MATCH` with an inline
//     `WHERE`; the compiler normalizes every chain clause to OPTIONAL MATCH and
//     the filter moves to the tail. A filter lost in that move would be
//     invisible to Refractor's D1 gate (the anchors are granted either way), so
//     it is pinned here by row-set equality against the pre-conversion cypher.
//   - the staff producer fused `holdsRole` into its head as a required MATCH,
//     so an identity with a workplace but NO role got no staff slice at all and
//     had its edgeStaffWorkOrders rows silently dropped. The generated producer
//     is all-OPTIONAL and grants them, which is the lens's own reachability —
//     asserted deliberately, not smuggled.

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// emComposedLenses returns the package's composed lens specs — the fourteen
// data lenses with their compiled reachability prefixes plus the three
// generated read-grant producers, in install order.
func emComposedLenses(t *testing.T) []pkgmgr.LensSpec {
	t.Helper()
	expanded, err := Package.ExpandReadGrantWalks()
	require.NoError(t, err, "the package's read-grant walks must compile")
	return expanded.Lenses
}

// emComposedSpec returns one composed lens's cypher by canonical name.
func emComposedSpec(t *testing.T, canonicalName string) string {
	t.Helper()
	for _, l := range emComposedLenses(t) {
		if l.CanonicalName == canonicalName {
			require.NotEmpty(t, l.Spec, "%s composed to an empty spec", canonicalName)
			return l.Spec
		}
	}
	t.Fatalf("no composed lens named %q", canonicalName)
	return ""
}

// emComposedLens returns one composed LensSpec by canonical name.
func emComposedLens(t *testing.T, canonicalName string) pkgmgr.LensSpec {
	t.Helper()
	for _, l := range emComposedLenses(t) {
		if l.CanonicalName == canonicalName {
			return l
		}
	}
	t.Fatalf("no composed lens named %q", canonicalName)
	return pkgmgr.LensSpec{}
}

// emProjectedRowSet projects a spec and returns its rows as a sorted set of
// canonical JSON, so two cyphers can be compared for row-set equality.
func (f *emFixture) emProjectedRowSet(t *testing.T, spec, actorKey string) []string {
	t.Helper()
	rows := f.project(t, spec, actorKey)
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		raw, err := json.Marshal(r.Values)
		require.NoError(t, err)
		out = append(out, string(raw))
	}
	sort.Strings(out)
	return out
}

// --- Migration assertion 1: per-lens data row-set equality -------------------

// TestMigration_RewrittenLensesProjectTheSameRows pins the five lenses whose
// chain was fused into a required MATCH with an inline WHERE: the composed
// all-OPTIONAL cypher with the filter hoisted into the tail must project
// exactly the rows the pre-conversion cypher did. This is the one hazard D1
// structurally cannot catch — a data-side filter lost in the rewrite grants the
// same anchors and drops nothing, it just shows rows it should not.
func TestMigration_RewrittenLensesProjectTheSameRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}

	t.Run("resident", func(t *testing.T) {
		f := emResidentWorld(t)
		// A CLOSED task assigned to the same actor makes the hoisted
		// status filter load-bearing in both cyphers: it must project in
		// neither, and the open one must project in both.
		f.vtxData(t, "doneTask", "task", map[string]any{"status": "done"})
		f.edge(t, "assignedTo", "doneTask", "resident")

		actor := f.key("resident")
		for _, c := range []struct {
			name        string
			frozen      string
			composedFor string
		}{
			{"edgeTasks", frozenEdgeTasksSpec, "edgeTasks"},
			{"edgeInstances", frozenEdgeInstancesSpec, "edgeInstances"},
		} {
			before := f.emProjectedRowSet(t, c.frozen, actor)
			after := f.emProjectedRowSet(t, emComposedSpec(t, c.composedFor), actor)
			require.NotEmpty(t, before, "%s: the frozen cypher projected nothing — the equality claim would be vacuous", c.name)
			require.Equal(t, before, after, "%s: composed cypher changed the projected row set", c.name)
		}

		// The closed task must be absent from both — the positive vector
		// above already proves the lens projects, so this is a real negative.
		openID := f.ids["openTask"]
		doneID := f.ids["doneTask"]
		rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeTasks"), actor))
		require.Contains(t, rows, openID, "the open task must project")
		require.NotContains(t, rows, doneID, "the hoisted status filter must still exclude a closed task")
	})

	t.Run("staff", func(t *testing.T) {
		f := emStaffWorldFull(t)
		// A CLOSED queued task makes edgeTasksQueued's hoisted status filter
		// load-bearing: with only an open task seeded, deleting the filter
		// would leave both cyphers projecting the identical row.
		f.vtxData(t, "doneQueued", "task", map[string]any{"status": "done"})
		f.edge(t, "queuedFor", "doneQueued", "maintRole")
		// A scopedTo work order routes scopedName's non-null branch through the
		// WITH projection the conversion introduced — a variable dropped there
		// would collapse every task row's label to a bare NanoID.
		f.aspect(t, "woUnit", "report", "workOrderReport", map[string]any{
			"summary": "Basement riser valve is weeping", "priority": "urgent"})
		f.edge(t, "scopedTo", "queuedTask", "woUnit")

		actor := f.key("tech")
		for _, c := range []struct{ name, frozen string }{
			{"edgeCatalogRoles", frozenEdgeCatalogRolesSpec},
			{"edgeTasksQueued", frozenEdgeTasksQueuedSpec},
			{"edgeStaffWorkOrders", frozenEdgeStaffWorkOrdersSpec},
		} {
			before := f.emProjectedRowSet(t, c.frozen, actor)
			after := f.emProjectedRowSet(t, emComposedSpec(t, c.name), actor)
			require.NotEmpty(t, before, "%s: the frozen cypher projected nothing — the equality claim would be vacuous", c.name)
			require.Equal(t, before, after, "%s: composed cypher changed the projected row set", c.name)
		}

		queued := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeTasksQueued"), actor))
		require.Contains(t, queued, f.ids["queuedTask"], "the open queued task must project")
		require.NotContains(t, queued, f.ids["doneQueued"],
			"the hoisted status filter must still exclude a closed queued task")
		require.Equal(t, "Basement riser valve is weeping", queued[f.ids["queuedTask"]]["scopedName"],
			"scopedName must survive the WITH projection the conversion introduced")
	})
}

// TestMigration_DegenerateRowIsSuppressed covers what the required→OPTIONAL
// conversion newly makes possible: the four lenses whose chain was a required
// MATCH now emit one all-null row for an actor the walk does not reach, where
// before they emitted none. Each tail must filter it — a lens that leaks it
// would publish a row with a null anchor, and the only thing left standing
// between that and a device is the Personal-lens envelope's own decline.
func TestMigration_DegenerateRowIsSuppressed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newEmFixture(t)
	// An identity holding a role but reached by none of the four walks: no
	// assignedTo task, no queued task, no providedTo instance, no granted op.
	f.vtx(t, "bare", "identity")
	f.vtx(t, "bareRole", "role")
	f.edge(t, "holdsRole", "bare", "bareRole")

	for _, name := range []string{"edgeTasks", "edgeInstances", "edgeCatalogRoles", "edgeTasksQueued"} {
		require.Emptyf(t, f.project(t, emComposedSpec(t, name), f.key("bare")),
			"%s: an unreached actor must project no row, not a degenerate all-null one", name)
	}
}

// --- Migration assertion 2: producer document equality minus `via` -----------

// TestMigration_GeneratedProducersGrantTheSameAnchors pins that each generated
// producer's whole readableAnchors document matches the hand-authored producer
// it replaces, entry for entry, once `via` is dropped. `via` is derived from the
// declared chain now (the full relation list, in order) rather than hand-typed,
// which is a deliberate change: it is audit-only, and capabilityread.IsReadable
// matches NanoID to NanoID without ever reading it.
func TestMigration_GeneratedProducersGrantTheSameAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, c := range []struct {
		producer string
		frozen   string
		world    func(*testing.T) *emFixture
		actor    string
	}{
		{"edgeManifestReadGrants", frozenBaseReadGrantsSpec, emResidentWorld, "resident"},
		{"edgeManifestStaffReadGrants", frozenStaffReadGrantsSpec, emStaffWorldFull, "tech"},
		{"edgeManifestProviderReadGrants", frozenProviderReadGrantsSpec, emProviderWorld, "providerId"},
	} {
		t.Run(c.producer, func(t *testing.T) {
			f := c.world(t)
			actor := f.key(c.actor)
			before := emAnchorEntries(t, f.project(t, c.frozen, actor))
			after := emAnchorEntries(t, f.project(t, emComposedSpec(t, c.producer), actor))
			require.NotEmpty(t, before, "the frozen producer granted nothing — the equality claim would be vacuous")
			require.Equal(t, before, after, "generated producer changed the granted anchor set")
		})
	}
}

// emAnchorEntries flattens a producer projection's readableAnchors into a
// sorted set of {anchorType, anchorId} pairs — the whole entry minus `via`.
func emAnchorEntries(t *testing.T, rows []ruleengine.ProjectionResult) []string {
	t.Helper()
	out := []string{}
	for _, row := range rows {
		anchors, _ := row.Values["readableAnchors"].([]any)
		for _, a := range anchors {
			m, ok := a.(map[string]any)
			if !ok {
				continue
			}
			id, _ := m["anchorId"].(string)
			if id == "" {
				continue
			}
			ty, _ := m["anchorType"].(string)
			out = append(out, ty+"/"+id)
		}
	}
	sort.Strings(out)
	return out
}

// --- Migration assertion 3: the realness / empty-delete delta ---------------

// TestMigration_GeneratedProducersDeclareTheRealnessFilter pins the one
// producer-shape change that is not a no-op: none of the hand-authored
// producers declared a realness filter, so the driver's empty-delete branch
// never ran and an all-OPTIONAL producer left a placeholder-only document for
// every identity with no binding at all. The generated producers declare it on
// `anchorId`, so EmptyBehavior "delete" actually fires. No security change — a
// null anchorId never matched anything in IsReadable's union.
func TestMigration_GeneratedProducersDeclareTheRealnessFilter(t *testing.T) {
	for _, name := range []string{
		"edgeManifestReadGrants",
		"edgeManifestStaffReadGrants",
		"edgeManifestProviderReadGrants",
	} {
		l := emComposedLens(t, name)
		require.NotNil(t, l.Output, "%s: generated producer has no Output descriptor", name)
		require.Equal(t, "anchorId", l.Output.RealnessFilter,
			"%s: without a realness filter on anchorId the driver's empty-delete never fires", name)
		require.Equal(t, "delete", l.Output.EmptyBehavior, "%s", name)
		require.Equal(t, "identity", l.Output.AnchorType, "%s", name)
		require.Equal(t, []string{"readableAnchors"}, l.Output.BodyColumns, "%s", name)
		require.Equal(t, "auto", l.Output.Freshness, "%s", name)
		require.Equal(t, []string{"default"}, l.Output.Lanes, "%s", name)
	}
}

// TestMigration_BindinglessIdentityGrantsNothing is the realness delta's live
// half: an identity reachable by no walk at all collects only placeholder
// entries, so every granted-anchor set is empty and the driver deletes the
// document rather than writing a placeholder-only one.
func TestMigration_BindinglessIdentityGrantsNothing(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newEmFixture(t)
	f.vtx(t, "loner", "identity")
	for _, name := range []string{
		"edgeManifestReadGrants",
		"edgeManifestStaffReadGrants",
		"edgeManifestProviderReadGrants",
	} {
		require.Empty(t, emAnchorEntries(t, f.project(t, emComposedSpec(t, name), f.key("loner"))),
			"%s: a binding-less identity must be granted no anchor", name)
	}
}

// TestMigration_StaffSliceNoLongerRequiresARole is the staff producer's
// deliberate widening, asserted rather than smuggled: the hand-authored
// producer fused `holdsRole` into a required head MATCH, so a workplace-only
// identity produced NO staff slice and had every edgeStaffWorkOrders row
// silently dropped by D1 — even though that lens's own reachability is
// `worksAt`, not `holdsRole`. The generated producer is all-OPTIONAL, so the
// grant now matches the lens.
func TestMigration_StaffSliceNoLongerRequiresARole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := emStaffWorld(t) // tech worksAt bldgA, holds no role
	actor := f.key("tech")

	require.Empty(t, emAnchorEntries(t, f.project(t, frozenStaffReadGrantsSpec, actor)),
		"the pre-conversion producer required a role, so it granted nothing here")

	granted := emAnchorEntries(t, f.project(t, emComposedSpec(t, "edgeManifestStaffReadGrants"), actor))
	require.Contains(t, granted, "workorder/"+f.ids["woUnit"],
		"the generated producer must grant the work orders the lens projects")
	require.Contains(t, granted, "workorder/"+f.ids["woBldg"])
}

// --- frozen pre-conversion cypher (migration fixtures) ----------------------
//
// The five lenses whose chain was a required MATCH, and the three
// hand-authored producers, exactly as they read before the walk conversion.
// They exist only as the comparison side of the assertions above.

const frozenEdgeTasksSpec = `
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
  (CASE WHEN scopedUnit.presentation.data.name <> null THEN scopedUnit.presentation.data.name
        ELSE tgt.report.data.summary END) AS scopedName,
  task.data.expiresAt AS expiresAt
`

const frozenEdgeInstancesSpec = `
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

const frozenEdgeCatalogRolesSpec = `
MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role)
OPTIONAL MATCH (role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:meta)
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

const frozenEdgeTasksQueuedSpec = `
MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role)<-[:queuedFor]-(task:task)
WHERE task.data.status = "open"
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)
OPTIONAL MATCH (tgt)-[:appliesToUnit]->(scopedUnit:unit)
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

const frozenEdgeStaffWorkOrdersSpec = `
MATCH (identity:identity {key: $actorKey})-[:worksAt]->(work)
OPTIONAL MATCH (work)<-[:containedIn*0..]-(place)<-[:locatedAt]-(wo:workorder)
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

const frozenBaseReadGrantsSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:residesIn]->(home)-[:containedIn*0..]->(container)
OPTIONAL MATCH (container)<-[:availableAt]-(tpl:service)
OPTIONAL MATCH (tpl)-[:permitsOperation]->(op:meta)
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
OPTIONAL MATCH (container)<-[:locatedAt]-(studio:studio)
OPTIONAL MATCH (studio)<-[:atStudio]-(sess:session)
OPTIONAL MATCH (container)<-[:practicesAt]-(prov:provider)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(tpl.key), via: ['availableAt']}) +
  collect(DISTINCT {anchorType: 'meta', anchorId: nanoIdFromKey(op.key), via: ['permitsOperation']}) +
  collect(DISTINCT {anchorType: 'task', anchorId: nanoIdFromKey(task.key), via: ['assignedTo']}) +
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(inst.key), via: ['providedTo']}) +
  collect(DISTINCT {anchorType: 'session', anchorId: nanoIdFromKey(sess.key), via: ['locatedAt', 'atStudio']}) +
  collect(DISTINCT {anchorType: 'provider', anchorId: nanoIdFromKey(prov.key), via: ['practicesAt']}) +
  collect(DISTINCT {anchorType: 'booking', anchorId: nanoIdFromKey(bk.key), via: ['bookedBy']})
  AS readableAnchors
`

const frozenStaffReadGrantsSpec = `
MATCH (identity:identity {key: $actorKey})-[:holdsRole]->(role:role)
OPTIONAL MATCH (role)<-[:grantedBy]-(perm:permission)-[:forOperation]->(op:meta)
OPTIONAL MATCH (role)<-[:queuedFor]-(task:task)
OPTIONAL MATCH (identity)-[:worksAt]->(work)<-[:containedIn*0..]-(place)<-[:locatedAt]-(wo:workorder)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'meta', anchorId: nanoIdFromKey(op.key), via: ['holdsRole', 'grantedBy', 'forOperation']}) +
  collect(DISTINCT {anchorType: 'task', anchorId: nanoIdFromKey(task.key), via: ['holdsRole', 'queuedFor']}) +
  collect(DISTINCT {anchorType: 'workorder', anchorId: nanoIdFromKey(wo.key), via: ['worksAt', 'containedIn', 'locatedAt']})
  AS readableAnchors
`

const frozenProviderReadGrantsSpec = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:identifiedBy]-(pr:provider)<-[:withProvider]-(appt:appointment)
OPTIONAL MATCH (identity)<-[:identifiedBy]-(sp:serviceprovider)<-[:providedBy]-(tpl:service)<-[:instanceOf]-(inst:service)
OPTIONAL MATCH (identity)<-[:identifiedBy]-(instr:instructor)<-[:ledBy]-(sess:session)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'appointment', anchorId: nanoIdFromKey(appt.key), via: ['identifiedBy', 'withProvider']}) +
  collect(DISTINCT {anchorType: 'service', anchorId: nanoIdFromKey(inst.key), via: ['identifiedBy', 'providedBy', 'instanceOf']}) +
  collect(DISTINCT {anchorType: 'session', anchorId: nanoIdFromKey(sess.key), via: ['identifiedBy', 'ledBy']})
  AS readableAnchors
`
