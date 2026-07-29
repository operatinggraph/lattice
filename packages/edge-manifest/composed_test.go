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
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// emComposedLenses returns the package's composed lens specs — the sixteen
// data lenses with their compiled reachability prefixes plus the three
// generated read-grant producers, in install order.
func emComposedLenses(t *testing.T) []pkgmgr.LensSpec {
	t.Helper()
	expanded, err := Package.ExpandReadGrantWalks()
	require.NoError(t, err, "the package's read-grant walks must compile")
	return expanded.Lenses
}

// emComposedSpec returns one composed lens's cypher by canonical name — for a
// single-walk lens. A multi-walk lens (SpecBranches set, Spec empty) fails
// this assertion; use emComposedSpecBranch for one of those.
func emComposedSpec(t *testing.T, canonicalName string) string {
	t.Helper()
	for _, l := range emComposedLenses(t) {
		if l.CanonicalName == canonicalName {
			require.NotEmpty(t, l.Spec, "%s composed to an empty spec (multi-walk? use emComposedSpecBranch)", canonicalName)
			return l.Spec
		}
	}
	t.Fatalf("no composed lens named %q", canonicalName)
	return ""
}

// emComposedSpecBranch returns one branch of a multi-walk lens's composed
// cypher, in Walks declaration order (branch 0 is the first-declared Walk).
func emComposedSpecBranch(t *testing.T, canonicalName string, branch int) string {
	t.Helper()
	for _, l := range emComposedLenses(t) {
		if l.CanonicalName == canonicalName {
			require.NotEmptyf(t, l.SpecBranches, "%s composed to a single spec, not branches", canonicalName)
			require.Greaterf(t, len(l.SpecBranches), branch, "%s has only %d branch(es), no branch %d", canonicalName, len(l.SpecBranches), branch)
			return l.SpecBranches[branch]
		}
	}
	t.Fatalf("no composed lens named %q", canonicalName)
	return ""
}

// emSpecTexts returns every executable cypher text a composed lens carries —
// its single Spec, or every SpecBranches entry for a multi-walk lens.
func emSpecTexts(l pkgmgr.LensSpec) []string {
	if len(l.SpecBranches) > 0 {
		return l.SpecBranches
	}
	return []string{l.Spec}
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
			{"edgeTasksQueued", frozenEdgeTasksQueuedSpec},
			{"edgeStaffWorkOrders", frozenEdgeStaffWorkOrdersSpec},
		} {
			before := f.emProjectedRowSet(t, c.frozen, actor)
			after := f.emProjectedRowSet(t, emComposedSpec(t, c.name), actor)
			require.NotEmpty(t, before, "%s: the frozen cypher projected nothing — the equality claim would be vacuous", c.name)
			require.Equal(t, before, after, "%s: composed cypher changed the projected row set", c.name)
		}

		// edgeCatalog's role Walk (branch 1, §13.7 build order (c)) replaces
		// the retired edgeCatalogRoles sibling — same row-set assertion,
		// against that branch instead of a standalone composed lens.
		catalogBefore := f.emProjectedRowSet(t, frozenEdgeCatalogRolesSpec, actor)
		catalogAfter := f.emProjectedRowSet(t, emComposedSpecBranch(t, "edgeCatalog", 1), actor)
		require.NotEmpty(t, catalogBefore, "edgeCatalog role branch: the frozen cypher projected nothing — the equality claim would be vacuous")
		require.Equal(t, catalogBefore, catalogAfter, "edgeCatalog role branch: composed cypher changed the projected row set")

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

	for _, name := range []string{"edgeTasks", "edgeInstances", "edgeTasksQueued"} {
		require.Emptyf(t, f.project(t, emComposedSpec(t, name), f.key("bare")),
			"%s: an unreached actor must project no row, not a degenerate all-null one", name)
	}
	require.Emptyf(t, f.project(t, emComposedSpecBranch(t, "edgeCatalog", 1), f.key("bare")),
		"edgeCatalog role branch: an unreached actor must project no row, not a degenerate all-null one")
}

// --- Migration assertion 2: producer document equality minus `via` -----------

// TestMigration_GeneratedProducersGrantTheSameAnchors pins that each generated
// producer's readableAnchors document still contains, entry for entry once
// `via` is dropped, everything the hand-authored producer it replaces granted —
// and that whatever it grants BEYOND that set is exactly the anchor types the
// lenses added since have declared, never an unexplained widening. `via` is
// derived from the declared chain now (the full relation list, in order) rather
// than hand-typed, which is a deliberate change: it is audit-only, and
// capabilityread.IsReadable matches NanoID to NanoID without ever reading it.
//
// The frozen specs are historical: a lens added after the migration declares an
// anchor type they could not have granted, so `addedTypes` is where that arrives
// — naming it here is what keeps the claim a pin rather than a shrug. The
// forward direction (nothing PROJECTED goes ungranted) is coverage_proof_test's.
func TestMigration_GeneratedProducersGrantTheSameAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, c := range []struct {
		producer   string
		frozen     string
		world      func(*testing.T) *emFixture
		actor      string
		addedTypes []string
	}{
		{"edgeManifestReadGrants", frozenBaseReadGrantsSpec, emResidentWorld, "resident", []string{"menuitem", "tab"}},
		{"edgeManifestStaffReadGrants", frozenStaffReadGrantsSpec, emStaffWorldFull, "tech", []string{"studio"}},
		{"edgeManifestProviderReadGrants", frozenProviderReadGrantsSpec, emProviderWorld, "providerId", nil},
	} {
		t.Run(c.producer, func(t *testing.T) {
			f := c.world(t)
			actor := f.key(c.actor)
			before := emAnchorEntries(t, f.project(t, c.frozen, actor))
			after := emAnchorEntries(t, f.project(t, emComposedSpec(t, c.producer), actor))
			require.NotEmpty(t, before, "the frozen producer granted nothing — the containment claim would be vacuous")
			require.Subset(t, after, before, "generated producer dropped an anchor the hand-authored one granted")

			kept := map[string]bool{}
			for _, e := range before {
				kept[e] = true
			}
			addedTypes := []string{}
			for _, e := range after {
				if !kept[e] {
					addedTypes = append(addedTypes, strings.SplitN(e, "/", 2)[0])
				}
			}
			sort.Strings(addedTypes)
			want := append([]string{}, c.addedTypes...)
			sort.Strings(want)
			require.Equal(t, want, addedTypes,
				"generated producer grants an anchor type no declared lens accounts for")
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
OPTIONAL MATCH (op)<-[:permitsOperation]-(psvc:service)
WITH op, role, collect(DISTINCT psvc.key) AS viaSvcKeys
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
  op.dispatch.data.visibleWhen AS dispatchVisibleWhen,
  op.sensitive.data.value AS sensitive,
  role.key AS viaRole,
  role.canonicalName.data.value AS viaRoleName,
  viaSvcKeys AS viaServices
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

// --- Migration assertion 3 (driver level): the emptied slice is DELETED ------

// TestMigration_EmptySliceIsDeletedByTheDriver drives the generated producer's
// row through the projection driver, which is the only place the realness filter
// actually decides anything. Asserting the cypher grants nothing (above) does
// not prove the KEY goes away — and "the emptied slice is deleted rather than
// left as a placeholder-only document" is a claim about the driver, not the
// cypher. A binding-less identity must yield ErrDeleteProjection keyed at its
// own cap-read key; a reachable identity must yield a real envelope.
func TestMigration_EmptySliceIsDeletedByTheDriver(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	producer := emComposedLens(t, "edgeManifestStaffReadGrants")
	desc, err := projection.ParseOutputDescriptor(&lens.OutputDescriptorSpec{
		AnchorType:       producer.Output.AnchorType,
		OutputKeyPattern: producer.Output.OutputKeyPattern,
		BodyColumns:      producer.Output.BodyColumns,
		EmptyBehavior:    producer.Output.EmptyBehavior,
		RealnessFilter:   producer.Output.RealnessFilter,
		Freshness:        producer.Output.Freshness,
		Lanes:            producer.Output.Lanes,
	})
	require.NoError(t, err, "the generated Output descriptor must parse Refractor-side")
	envelope := desc.EnvelopeFn("lens-def-key", func(string) uint64 { return 1 })
	params := map[string]any{"projectedAt": "2026-07-24T00:00:00Z"}

	t.Run("binding-less identity deletes its key", func(t *testing.T) {
		f := newEmFixture(t)
		f.vtx(t, "loner", "identity")
		rows := f.project(t, producer.Spec, f.key("loner"))
		require.Len(t, rows, 1, "the producer projects one row per actor, placeholders included")

		_, keys, err := envelope(rows[0].Values, nil, params)
		require.ErrorIs(t, err, pipeline.ErrDeleteProjection,
			"a placeholder-only slice must retract the key, not persist an all-null document")
		require.Equal(t, desc.BuildKey(f.key("loner")), keys["key"],
			"the delete must be keyed at this actor's own cap-read key")
	})

	t.Run("reachable identity writes a real envelope", func(t *testing.T) {
		f := emStaffWorldFull(t)
		rows := f.project(t, producer.Spec, f.key("tech"))
		require.Len(t, rows, 1)

		out, _, err := envelope(rows[0].Values, nil, params)
		require.NoError(t, err, "a reachable actor must not be retracted")
		anchors, _ := out["readableAnchors"].([]any)
		require.NotEmpty(t, anchors, "the realness filter must keep the real entries")
		for _, a := range anchors {
			m, ok := a.(map[string]any)
			require.True(t, ok)
			require.NotEmpty(t, m["anchorId"],
				"the realness filter must have dropped every placeholder entry")
		}
	})
}

// TestMigration_GeneratedProducersEmitNoDuplicateAnchors pins the size property
// the auth plane depends on. A generated producer is several INDEPENDENT
// OPTIONAL MATCH branches off one actor, so the bindings its RETURN aggregates
// over are the branches' CROSS PRODUCT; each branch's `collect(DISTINCT ...)`
// is the only thing collapsing that back to what the branch actually reached.
//
// Without it a document's entry count is the PRODUCT of every branch's
// cardinality rather than their sum — multiplicative in the number of walks a
// domain declares, so it grows fastest for exactly the well-connected actors.
// A `cap-read.<domain>.<actor>` document that outgrows NATS's max payload can
// never be written again: its actor's grants freeze at whatever was last
// storable, and no reprojection or convergence sweep can repair it.
func TestMigration_GeneratedProducersEmitNoDuplicateAnchors(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	for _, c := range []struct {
		producer string
		world    func(*testing.T) *emFixture
		actor    string
	}{
		{"edgeManifestReadGrants", emResidentWorld, "resident"},
		{"edgeManifestStaffReadGrants", emStaffWorldFull, "tech"},
		{"edgeManifestProviderReadGrants", emProviderWorld, "providerId"},
	} {
		t.Run(c.producer, func(t *testing.T) {
			f := c.world(t)
			rows := f.project(t, emComposedSpec(t, c.producer), f.key(c.actor))

			// Identity is the WHOLE entry, `via` included. Two branches that
			// both reach one anchor legitimately contribute an entry each,
			// differing in the justifying path; dedup is per branch, and
			// collapsing those would discard a justification (§6.14 makes the
			// effective set a union and `via` audit-only).
			seen := map[string]int{}
			total := 0
			for _, row := range rows {
				anchors, _ := row.Values["readableAnchors"].([]any)
				for _, a := range anchors {
					m, ok := a.(map[string]any)
					if !ok {
						continue
					}
					if id, _ := m["anchorId"].(string); id == "" {
						continue
					}
					total++
					seen[fmt.Sprintf("%v|%v|%v", m["anchorType"], m["anchorId"], m["via"])]++
				}
			}
			require.NotZero(t, total, "granted nothing — the no-duplicate claim would be vacuous")

			for e, n := range seen {
				require.Equal(t, 1, n,
					"%s: entry %s emitted %d times — a branch's DISTINCT did not bind, so the "+
						"document carries the cross product of the branches instead of their union",
					c.producer, e, n)
			}
		})
	}
}
