package edgemanifest

// Rule-engine proof of the staff workplace-spine lenses (edgeStaffWorkOrders
// and the work-order branch edgeManifestStaffReadGrants grants them), driven
// through the `full` engine — the one activation selects via engine:"full" —
// against an embedded NATS Core/Adjacency KV. Same harness shape as
// wellness-domain / clinic-domain's lens cypher tests.
//
// These exist because this package's other tests check only STRUCTURE (spec
// literals, adapter kinds, parse success), and the thing that actually needed
// proving here cannot be seen that way: edgeStaffWorkOrders walks
// `(work)<-[:containedIn*0..]-(place)` — a variable-length hop in the INBOUND
// direction, which no shipped lens in this repo had used before. Parsing says
// nothing about whether it enumerates children. The showcase topology is
// exactly the case that matters: the tech worksAt the BUILDING, the work order
// is at a UNIT inside it.
//
//	building A ←containedIn— unit A1 ←locatedAt— workorder "wo-unit"   (1 hop down)
//	building A ←locatedAt— workorder "wo-bldg"                          (0 hops)
//	building B ←locatedAt— workorder "wo-other"                         (not reachable)

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

type emFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newEmFixture(t *testing.T) *emFixture {
	adjKV, coreKV := lenstest.KVs(t)
	return &emFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *emFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := lenstest.NanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
}

// tombstone marks an already-seeded vertex deleted, leaving its adjacency
// edges intact — the state a retire op leaves behind, and the one a lens
// walking to that vertex must drop rather than project.
func (f *emFixture) tombstone(t *testing.T, name string) {
	t.Helper()
	key := f.key(name)
	body := map[string]any{"key": key, "class": f.types[f.ids[name]], "isDeleted": true, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *emFixture) key(name string) string {
	return "vtx." + f.types[f.ids[name]] + "." + f.ids[name]
}

func (f *emFixture) aspect(t *testing.T, ownerName, local, class string, data map[string]any) {
	t.Helper()
	owner := f.key(ownerName)
	k := owner + "." + local
	body := map[string]any{"key": k, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), k, raw)
	require.NoError(t, err)
}

func (f *emFixture) edge(t *testing.T, name, fromName, toName string) {
	t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[fromName], f.ids[toName]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := "lnk." + fromType + "." + fromID + "." + name + "." + toType + "." + toID
	edgeID := name + "_" + fromID + "_" + toID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: name, Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType}))
}

// project runs a personal-lens spec for one actor.
func (f *emFixture) project(t *testing.T, spec, actorKey string) []ruleengine.ProjectionResult {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err, "edge-manifest lens cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    actorKey,
		"now":         now,
		"projectedAt": now,
	}}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

// emStaffWorld builds the topology in this file's header comment and returns
// the staff actor's key.
func emStaffWorld(t *testing.T) *emFixture {
	f := newEmFixture(t)
	f.vtx(t, "tech", "identity")
	f.vtx(t, "bldgA", "building")
	f.vtx(t, "bldgB", "building")
	f.vtx(t, "unitA1", "unit")
	f.vtx(t, "woUnit", "workorder")
	f.vtx(t, "woBldg", "workorder")
	f.vtx(t, "woOther", "workorder")

	f.aspect(t, "bldgA", "presentation", "locationPresentation", map[string]any{"name": "Riverside Building"})
	f.aspect(t, "unitA1", "presentation", "locationPresentation", map[string]any{"name": "Unit 1"})
	f.aspect(t, "woUnit", "report", "workOrderReport", map[string]any{
		"summary": "Basement riser valve is weeping", "priority": "urgent", "reportedAt": "2026-07-21T09:00:00Z"})
	f.aspect(t, "woBldg", "report", "workOrderReport", map[string]any{
		"summary": "Lobby door sticks", "priority": "normal", "reportedAt": "2026-07-21T10:00:00Z"})
	f.aspect(t, "woOther", "report", "workOrderReport", map[string]any{
		"summary": "Lift is out at B", "priority": "urgent", "reportedAt": "2026-07-21T11:00:00Z"})

	f.edge(t, "worksAt", "tech", "bldgA")
	f.edge(t, "containedIn", "unitA1", "bldgA")
	f.edge(t, "locatedAt", "woUnit", "unitA1")
	f.edge(t, "locatedAt", "woBldg", "bldgA")
	f.edge(t, "locatedAt", "woOther", "bldgB")
	return f
}

func emRowsByEntity(rows []ruleengine.ProjectionResult) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, r := range rows {
		if id, _ := r.Values["entityId"].(string); id != "" {
			out[id] = r.Values
		}
	}
	return out
}

// TestEdgeStaffWorkOrders_WalksDownFromTheWorkplace is the load-bearing
// vector: the INBOUND variable-length hop must enumerate the workplace's
// children, or a tech who works at a building sees none of the work inside it
// — which is every work order in the showcase.
func TestEdgeStaffWorkOrders_WalksDownFromTheWorkplace(t *testing.T) {
	f := emStaffWorld(t)
	rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeStaffWorkOrders"), f.key("tech")))

	unitRow, ok := rows[f.ids["woUnit"]]
	require.True(t, ok, "work order at a UNIT inside the workplace must project (the *0.. inbound hop)")
	require.Equal(t, "Basement riser valve is weeping", unitRow["summary"])
	require.Equal(t, "urgent", unitRow["priority"])
	require.Equal(t, "Unit 1", unitRow["placeName"], "the row names its own place, not the workplace")
	require.Equal(t, f.key("bldgA"), unitRow["workplaceKey"])
	require.Equal(t, "manifest.work", unitRow["ns"])
	require.Equal(t, "open", unitRow["status"])

	bldgRow, ok := rows[f.ids["woBldg"]]
	require.True(t, ok, "work order at the workplace ITSELF must project (the zero-hop case)")
	require.Equal(t, "Riverside Building", bldgRow["placeName"])

	_, leaked := rows[f.ids["woOther"]]
	require.False(t, leaked, "a work order at a building this actor does NOT work at must not project")
	require.Len(t, rows, 2)
}

// TestEdgeStaffWorkOrders_ResolvedStatusDerivesFromTheAspect pins that the
// mirror flips to resolved off the SAME `.resolution` marker ResolveWorkOrder
// writes — so a resolve that drains after a device reconnects needs no second
// write to model it.
func TestEdgeStaffWorkOrders_ResolvedStatusDerivesFromTheAspect(t *testing.T) {
	f := emStaffWorld(t)
	f.aspect(t, "woUnit", "resolution", "workOrderResolution", map[string]any{
		"notes": "Repacked the gland.", "resolvedAt": "2026-07-21T12:00:00Z",
		"resolvedBy": f.key("tech")})

	rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeStaffWorkOrders"), f.key("tech")))
	require.Equal(t, "resolved", rows[f.ids["woUnit"]]["status"])
	require.Equal(t, "Repacked the gland.", rows[f.ids["woUnit"]]["resolutionNotes"])
	require.Equal(t, "open", rows[f.ids["woBldg"]]["status"], "an unresolved sibling stays open")
}

// TestEdgeStaffWorkOrders_NoWorkplaceProjectsNothing is the fail-closed half:
// the row set is derived from `worksAt`, so an actor without one has no
// workplace world at all. Unwire it and the rows go away with it.
func TestEdgeStaffWorkOrders_NoWorkplaceProjectsNothing(t *testing.T) {
	f := emStaffWorld(t)
	f.vtx(t, "resident", "identity")
	require.Empty(t, f.project(t, emComposedSpec(t, "edgeStaffWorkOrders"), f.key("resident")))
}

// TestStaffReadGrants_CoverTheWorkOrderAnchors is the same-commit grant
// discipline entity-browse F4 established, as a test rather than a promise: a
// manifest row whose anchor carries no read grant is silently dropped by
// Refractor's D1 gate, so the lens and its anchors ship together or the whole
// view is invisible for reasons nothing reports.
func TestStaffReadGrants_CoverTheWorkOrderAnchors(t *testing.T) {
	f := emStaffWorld(t)
	f.vtx(t, "maintRole", "role")
	f.edge(t, "holdsRole", "tech", "maintRole")

	rows := f.project(t, emComposedSpec(t, "edgeManifestStaffReadGrants"), f.key("tech"))
	require.Len(t, rows, 1)

	granted := map[string]bool{}
	anchors, _ := rows[0].Values["readableAnchors"].([]any)
	require.NotEmpty(t, anchors)
	for _, a := range anchors {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := m["anchorId"].(string); id != "" && m["anchorType"] == "workorder" {
			granted[id] = true
		}
	}
	require.True(t, granted[f.ids["woUnit"]], "the unit's work order must be readable")
	require.True(t, granted[f.ids["woBldg"]], "the building's own work order must be readable")
	require.False(t, granted[f.ids["woOther"]], "another building's work order must NOT be granted")
}

// emMultiHatWorld builds persona-worlds-design.md §3.4's acceptance human: one
// identity who lives in a unit, works a desk at a building, and teaches yoga —
// three bindings by three different relations, which is the whole point.
//
//	unitB2  ←residesIn—  sam  —worksAt→  bldgA
//	                     sam  ←identifiedBy—  instructor "Sam Okafor"
func emMultiHatWorld(t *testing.T) *emFixture {
	f := newEmFixture(t)
	f.vtx(t, "sam", "identity")
	f.vtx(t, "bldgA", "building")
	f.vtx(t, "unitB2", "unit")
	f.vtx(t, "samInstructor", "instructor")

	f.aspect(t, "bldgA", "presentation", "locationPresentation", map[string]any{"name": "Riverside Building"})
	f.aspect(t, "unitB2", "presentation", "locationPresentation", map[string]any{"name": "Unit 2"})
	f.aspect(t, "samInstructor", "profile", "instructorProfile", map[string]any{"displayName": "Sam Okafor"})

	f.edge(t, "residesIn", "sam", "unitB2")
	f.edge(t, "worksAt", "sam", "bldgA")
	f.edge(t, "identifiedBy", "samInstructor", "sam")
	return f
}

// emAnchorsByRelation indexes a manifest.me row's anchors by their relation
// stamp, dropping the degenerate {key:null} entries an OPTIONAL MATCH that
// found nothing contributes — the same shape the renderer drops client-side.
func emAnchorsByRelation(t *testing.T, rows []ruleengine.ProjectionResult) map[string][]map[string]any {
	t.Helper()
	require.Len(t, rows, 1, "edgeIdentity is anchored on the identity itself, so exactly one row")
	raw, _ := rows[0].Values["anchors"].([]any)
	out := map[string][]map[string]any{}
	for _, a := range raw {
		m, ok := a.(map[string]any)
		if !ok {
			continue
		}
		if k, _ := m["key"].(string); k == "" {
			continue
		}
		rel, _ := m["relation"].(string)
		out[rel] = append(out[rel], m)
	}
	return out
}

// TestEdgeIdentity_AnchorsCarryEveryHatWithItsRelation is the §3.4 green bar at
// the lens layer: all three spines must reach the renderer as anchors, each
// stamped with the relation that says WHICH world it is. `selfAnchors` carries
// the same bindings typed and keyed for `{me.<type>}` dispatch but nameless and
// relation-less, which is not enough to group a person's hats by provenance —
// that is what these entries are for.
func TestEdgeIdentity_AnchorsCarryEveryHatWithItsRelation(t *testing.T) {
	f := emMultiHatWorld(t)
	byRel := emAnchorsByRelation(t, f.project(t, emComposedSpec(t, "edgeIdentity"), f.key("sam")))

	require.Len(t, byRel["residesIn"], 1, "the home hat")
	require.Equal(t, "Unit 2", byRel["residesIn"][0]["name"])

	require.Len(t, byRel["worksAt"], 1, "the work hat")
	require.Equal(t, "Riverside Building", byRel["worksAt"][0]["name"])

	require.Len(t, byRel["identifiedBy"], 1, "the services hat")
	bound := byRel["identifiedBy"][0]
	require.Equal(t, f.key("samInstructor"), bound["key"])
	require.Equal(t, "Sam Okafor", bound["name"], "the instructor's name lives on .profile.displayName")
	require.Equal(t, "instructor", bound["type"],
		"the renderer resolves the hat's ops by TYPE, so the walk must stamp it rather than parse the key")
}

// TestEdgeIdentity_UnboundIdentityCarriesNoServicesHat is the scoping negative:
// a resident with no provider binding must not acquire a services hat from the
// three OPTIONAL MATCHes that found nothing.
func TestEdgeIdentity_UnboundIdentityCarriesNoServicesHat(t *testing.T) {
	f := newEmFixture(t)
	f.vtx(t, "riley", "identity")
	f.vtx(t, "unitA1", "unit")
	f.aspect(t, "unitA1", "presentation", "locationPresentation", map[string]any{"name": "Unit 1"})
	f.edge(t, "residesIn", "riley", "unitA1")

	byRel := emAnchorsByRelation(t, f.project(t, emComposedSpec(t, "edgeIdentity"), f.key("riley")))
	require.Len(t, byRel["residesIn"], 1)
	require.Empty(t, byRel["identifiedBy"], "an unbound identity holds no provider hat")
	require.Empty(t, byRel["worksAt"], "and no work hat")
}

// TestEdgeIdentity_BoundProviderProfileNameProjects pins the per-domain profile
// field: a clinic provider's display name is `fullName`, not the `displayName`
// the other two domains chose, so reading one field for all three would leave
// the richest hat nameless.
func TestEdgeIdentity_BoundProviderProfileNameProjects(t *testing.T) {
	f := newEmFixture(t)
	f.vtx(t, "osei", "identity")
	f.vtx(t, "oseiProvider", "provider")
	f.aspect(t, "oseiProvider", "profile", "providerProfile",
		map[string]any{"fullName": "Dr. Amara Osei", "specialty": "Cardiology"})
	f.edge(t, "identifiedBy", "oseiProvider", "osei")

	byRel := emAnchorsByRelation(t, f.project(t, emComposedSpec(t, "edgeIdentity"), f.key("osei")))
	require.Len(t, byRel["identifiedBy"], 1)
	require.Equal(t, "Dr. Amara Osei", byRel["identifiedBy"][0]["name"])
	require.Equal(t, "provider", byRel["identifiedBy"][0]["type"])
}

// TestEdgeIdentity_MultiHatTopologyYieldsOneAnchorPerRelation pins the anchors
// column against a cartesian blow-up. Every OPTIONAL MATCH in this lens
// multiplies the row set the collects run over — three roles, two led sessions
// and a lease application between them fan one identity out to a dozen rows —
// and the only thing collapsing that back down is `collect(DISTINCT ...)` over
// identical maps. If DISTINCT ever stops deduping map values, a person's home
// renders as several identical chips, which is the kind of defect that reads
// as a data problem and is really an engine one.
func TestEdgeIdentity_MultiHatTopologyYieldsOneAnchorPerRelation(t *testing.T) {
	f := newEmFixture(t)
	for name, typ := range map[string]string{
		"sam": "identity", "bldgA": "building", "unitB2": "unit", "studio": "studio",
		"sessMon": "session", "sessThu": "session", "samInstructor": "instructor", "app1": "leaseapp",
	} {
		f.vtx(t, name, typ)
	}
	for _, r := range []string{"roleConsumer", "roleFrontOfHouse", "roleProvider"} {
		f.vtx(t, r, "role")
		f.edge(t, "holdsRole", "sam", r)
	}
	f.aspect(t, "bldgA", "presentation", "locationPresentation", map[string]any{"name": "Riverside Building"})
	f.aspect(t, "unitB2", "presentation", "locationPresentation", map[string]any{"name": "Unit 2"})
	f.aspect(t, "samInstructor", "profile", "instructorProfile", map[string]any{"displayName": "Sam Okafor"})

	f.edge(t, "residesIn", "sam", "unitB2")
	f.edge(t, "containedIn", "unitB2", "bldgA")
	f.edge(t, "worksAt", "sam", "bldgA")
	f.edge(t, "identifiedBy", "samInstructor", "sam")
	f.edge(t, "teachesAt", "samInstructor", "studio")
	f.edge(t, "ledBy", "sessMon", "samInstructor")
	f.edge(t, "ledBy", "sessThu", "samInstructor")
	f.edge(t, "applicationFor", "app1", "sam")

	byRel := emAnchorsByRelation(t, f.project(t, emComposedSpec(t, "edgeIdentity"), f.key("sam")))
	require.Len(t, byRel["residesIn"], 1, "one home, however many roles and classes hang off the identity")
	require.Len(t, byRel["worksAt"], 1, "one workplace")
	require.Len(t, byRel["identifiedBy"], 1, "one binding")
	require.Equal(t, "Sam Okafor", byRel["identifiedBy"][0]["name"])
}

// TestEdgeCatalog_RoleBranchCarriesTheServiceJoin is the POSITIVE vector for
// the shared-row-key repair (refractor-shared-keyspace-arbitration-
// design.md §13.7 build order (c)): an op reachable BOTH by role grant and
// by service permitsOperation must project viaServices from the role
// branch too, or the two former sibling lenses' last-writer race erases the
// service join for any multi-hat actor. viaServices is anchor-derived (a
// pattern comprehension off `op` alone), so both branches compute it
// identically by construction — an empty-result pass on a linkless fixture
// proves nothing.
func TestEdgeCatalog_RoleBranchCarriesTheServiceJoin(t *testing.T) {
	f := newEmFixture(t)
	f.vtx(t, "staff", "identity")
	f.vtx(t, "role", "role")
	f.vtx(t, "perm", "permission")
	f.vtx(t, "opMeta", "meta")
	f.vtx(t, "tpl", "service")
	f.aspect(t, "role", "canonicalName", "canonicalName", map[string]any{"value": "frontOfHouse"})
	f.edge(t, "holdsRole", "staff", "role")
	f.edge(t, "grantedBy", "perm", "role")
	f.edge(t, "forOperation", "perm", "opMeta")
	f.edge(t, "permitsOperation", "tpl", "opMeta")

	rows := emRowsByEntity(f.project(t, emComposedSpecBranch(t, "edgeCatalog", 1), f.key("staff")))
	row, ok := rows[f.ids["opMeta"]]
	require.True(t, ok, "the role-granted op must project")
	via, ok := row["viaServices"].([]any)
	require.True(t, ok, "viaServices must be a list, got %T (%v)", row["viaServices"], row["viaServices"])
	require.Len(t, via, 1, "the permitsOperation service must be collected")
	require.Equal(t, f.key("tpl"), via[0])
	require.Equal(t, f.key("role"), row["viaRole"], "viaRole must carry the granting role")
}

// TestEdgeCatalog_TaskBranchProjectsAnUngrantedOp is the positive vector for
// the own-task Walk: an op-meta reachable ONLY through a task assigned to the
// actor — no held role, no service permitsOperation — still projects a
// manifest.op row, so a task-scoped submission (identity-domain/ddls.go's
// RecordIdentityPII resource-bound guard, authorized off the actor's
// cap.ephemeral task grant rather than any cap.roles permission) has a
// descriptor to open. viaRole nulls out exactly as it does on the
// service-template branch, proving the tail's unbound-variable handling
// covers this Walk too, not just the two it was written against.
func TestEdgeCatalog_TaskBranchProjectsAnUngrantedOp(t *testing.T) {
	f := newEmFixture(t)
	f.vtx(t, "resident", "identity")
	f.vtx(t, "task", "task")
	f.vtx(t, "opMeta", "meta")
	f.edge(t, "assignedTo", "task", "resident")
	f.edge(t, "forOperation", "task", "opMeta")

	rows := emRowsByEntity(f.project(t, emComposedSpecBranch(t, "edgeCatalog", 2), f.key("resident")))
	row, ok := rows[f.ids["opMeta"]]
	require.True(t, ok, "the task-assigned op must project with no role or service reach at all")
	require.Empty(t, row["viaRole"], "no role granted this op — viaRole must null out, not error")
}

// TestEdgeEntitySessions_ProjectsTheLeadingInstructorKey proves the shared
// tail's bridging OPTIONAL MATCH: a resident's residence-anchored session row
// (emResidentWorld, coverage_proof_test.go), reached via the domainBase
// branch, carries the session's own ledBy instructor as instructorKey once
// one is wired — a plain KEY projection (safe from a tail-local OPTIONAL
// MATCH regardless of the walk-prefix aspect-hydration caveat —
// reference_lens_tail_binding_rules.md). verticals.md "Entity detail
// attaches cross-hat ops": app.js's crossHatMismatch compares this column
// against the viewer's own {me.instructor} anchor, so a resident who is ALSO
// bound as some OTHER instructor doesn't get a live "Cancel class" button on
// a session they don't lead.
func TestEdgeEntitySessions_ProjectsTheLeadingInstructorKey(t *testing.T) {
	f := emResidentWorld(t)
	f.vtx(t, "instr", "instructor")
	f.edge(t, "ledBy", "sess", "instr")

	rows := emRowsByEntity(f.project(t, emComposedSpecBranch(t, "edgeEntitySessions", 0), f.key("resident")))
	row, ok := rows[f.ids["sess"]]
	require.True(t, ok, "the residence-reached session must project")
	require.Equal(t, f.key("instr"), row["instructorKey"])
}

// TestEdgeEntitySessions_NoLedByLeavesInstructorKeyNil — a session nobody
// leads yet must not project a stray instructorKey (a nil OPTIONAL MATCH
// costs the column, never the row) — emResidentWorld's own session has no
// ledBy link at all.
func TestEdgeEntitySessions_NoLedByLeavesInstructorKeyNil(t *testing.T) {
	f := emResidentWorld(t)
	rows := emRowsByEntity(f.project(t, emComposedSpecBranch(t, "edgeEntitySessions", 0), f.key("resident")))
	row, ok := rows[f.ids["sess"]]
	require.True(t, ok)
	require.Nil(t, row["instructorKey"])
}

// TestEdgeEntitySessions_ProviderBranchProjectsTheLeadingInstructorKey — the
// domainProvider branch (the former edgeInstructorSessions sibling, folded
// into edgeEntitySessions' second Walk) also carries instructorKey via the
// shared tail's bridging OPTIONAL MATCH, off the ALREADY walk-bound `instr`
// rather than a fresh binding.
func TestEdgeEntitySessions_ProviderBranchProjectsTheLeadingInstructorKey(t *testing.T) {
	f := newEmFixture(t)
	f.vtx(t, "teacher", "identity")
	f.vtx(t, "instr", "instructor")
	f.vtx(t, "sess", "session")
	f.aspect(t, "sess", "schedule", "wellnessSessionSchedule", map[string]any{"name": "Evening Flow", "startsAt": "2026-08-01T18:00:00Z"})
	f.edge(t, "identifiedBy", "instr", "teacher")
	f.edge(t, "ledBy", "sess", "instr")

	rows := emRowsByEntity(f.project(t, emComposedSpecBranch(t, "edgeEntitySessions", 1), f.key("teacher")))
	row, ok := rows[f.ids["sess"]]
	require.True(t, ok, "the instructor's own led session must project")
	require.Equal(t, f.key("instr"), row["instructorKey"])
}

// TestEdgeEntityBookings_ProjectsItsSessionsInstructorKey — a booking's row
// carries its session's ledBy instructor one hop further out (the same
// column SetBookingAttendance's {me.instructor} needs proof against), via
// its own tail-local OPTIONAL MATCH chained off the booking's forSession hop
// — emResidentWorld's booking carries no forSession link of its own, so this
// wires one on top of the shared world.
func TestEdgeEntityBookings_ProjectsItsSessionsInstructorKey(t *testing.T) {
	f := emResidentWorld(t)
	f.vtx(t, "instr", "instructor")
	f.edge(t, "forSession", "booking", "sess")
	f.edge(t, "ledBy", "sess", "instr")

	rows := emRowsByEntity(f.project(t, emComposedSpec(t, "edgeEntityBookings"), f.key("resident")))
	row, ok := rows[f.ids["booking"]]
	require.True(t, ok, "the booker's own booking must project")
	require.Equal(t, f.key("instr"), row["instructorKey"])
}

// ---- opCatalog: the staff-plane op-descriptor read model ----
//
// opCatalog is this package's one PLAIN lens (staff-descriptor-rendering-
// design.md §2.1): one open `op-catalog` row per op meta, keyed by
// operationType, carrying the descriptor vocabulary a staff application
// renders an operation's form from. Four of its properties are invisible from
// reading the cypher, and each is pinned below with the MUTATION that breaks
// it, executed rather than described:
//
//	the OPTIONAL role join    — required, a zero-permission op vanishes entirely
//	the no-WITH rule          — with one, the tombstone Delete silently stops
//	the operationType filter  — without it, every DDL/lens/pane meta leaks in
//	the anchor-derived key    — an aspect-sourced key would never retract
//
// A mutation asserted only in prose is a mutation nobody ran; each test below
// executes both the shipped spec and its mutant, and fails if the mutant
// behaves the same.

// The op metas below are seeded with `vtxData` (coverage_proof_test.go), which
// carries their operationType on the vertex ROOT's `data` envelope — the shape
// pkgmgr writes (internal/pkgmgr/build.go's
// docVertex(opMetaClass, {"operationType": …})). That placement is what lets
// opCatalog key on `op.data.operationType` and still resolve it read-free from
// a tombstoned body.

// emBody reads a seeded vertex's stored root body back — the same bytes a CDC
// event carries as its Properties, and what the retraction path resolves the
// row key from.
func (f *emFixture) emBody(t *testing.T, name string) map[string]any {
	t.Helper()
	entry, err := f.coreKV.Get(context.Background(), f.key(name))
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &out))
	return out
}

// tombstoneKeepingBody soft-deletes a vertex the way a real commit does: the
// prior document is carried over WHOLE and only `isDeleted` flips
// (internal/processor/step8_commit.go, buildMutationValue's tombstone arm).
// The distinction IS the retraction mechanism — the delete key is resolved
// read-free from the tombstoned body alone, so the sibling `tombstone` helper
// above (which blanks `data`) would prove the opposite of what it looked like.
// Returns the tombstoned body, which is what the CDC event delivers.
func (f *emFixture) tombstoneKeepingBody(t *testing.T, name string) map[string]any {
	t.Helper()
	body := f.emBody(t, name)
	body["isDeleted"] = true
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), f.key(name), raw)
	require.NoError(t, err)
	return body
}

// emOpCatalogWorld seeds the four descriptor shapes the catalog has to tell
// apart, each with the exact aspect local names + field names pkgmgr's
// build.go writes (presentation / inputSchema / fieldDescriptions / dispatch /
// sensitive), plus the two install-time grant edges it joins over:
//
//	fullOp      "ResolveWorkOrder"  whole vocabulary, granted by TWO roles
//	bareOp      "RecordLeaseDocOutcome"  operationType and nothing else (an engine leg)
//	ungrantedOp "OpenRenewal"       described, but NO granting permission at all
//	ddlMeta     —                   a :meta vertex that is not an op (a DDL meta)
//
// `permission forOperation meta` and `permission grantedBy role` are the two
// links internal/pkgmgr/build.go mints at install; the role's name lives on a
// `.canonicalName` aspect, never on the role root.
func emOpCatalogWorld(t *testing.T) *emFixture {
	f := newEmFixture(t)

	f.vtxData(t, "fullOp", "meta", map[string]any{"operationType": "ResolveWorkOrder"})
	f.aspect(t, "fullOp", "presentation", "presentation", map[string]any{
		"title":       "Resolve a work order",
		"shortLabel":  "Resolve",
		"description": "Record what you did.",
		"icon":        "wrench",
		"tone":        "primary",
		"submitLabel": "Mark resolved",
		"group":       "Maintenance",
	})
	f.aspect(t, "fullOp", "inputSchema", "inputSchema", map[string]any{
		"schema": `{"type":"object","properties":{"workOrderKey":{"type":"string"},"notes":{"type":"string"}},"required":["workOrderKey","notes"]}`,
	})
	f.aspect(t, "fullOp", "fieldDescriptions", "fieldDescriptions", map[string]any{
		"fieldDescriptions": map[string]any{
			"workOrderKey": "Filled by the client from the task, not typed.",
			"notes":        "What you actually did.",
		},
	})
	f.aspect(t, "fullOp", "dispatch", "dispatch", map[string]any{
		"class":         "workOrder",
		"authContext":   "task",
		"targetField":   "workOrderKey",
		"targetType":    "workorder",
		"reads":         []any{"{payload.workOrderKey}"},
		"optionalReads": []any{"{payload.workOrderKey}.resolution"},
	})
	f.aspect(t, "fullOp", "sensitive", "sensitive", map[string]any{"value": true})

	f.vtxData(t, "bareOp", "meta", map[string]any{"operationType": "RecordLeaseDocOutcome"})

	f.vtxData(t, "ungrantedOp", "meta", map[string]any{"operationType": "OpenRenewal"})
	f.aspect(t, "ungrantedOp", "presentation", "presentation", map[string]any{"title": "Open a renewal"})

	// A meta-vertex that is NOT an op: pkgmgr mints one of these for every DDL,
	// lens, pane and package it installs, so they outnumber the op metas in a
	// live corpus by a wide margin.
	f.vtxData(t, "ddlMeta", "meta", map[string]any{"canonicalName": "workOrder"})

	f.vtx(t, "backOfHouse", "role")
	f.aspect(t, "backOfHouse", "canonicalName", "canonicalName", map[string]any{"value": "backOfHouse"})
	f.vtx(t, "operator", "role")
	f.aspect(t, "operator", "canonicalName", "canonicalName", map[string]any{"value": "operator"})

	f.vtxData(t, "permResolveBoh", "permission", map[string]any{"operationType": "ResolveWorkOrder", "scope": "any"})
	f.edge(t, "grantedBy", "permResolveBoh", "backOfHouse")
	f.edge(t, "forOperation", "permResolveBoh", "fullOp")

	f.vtxData(t, "permResolveOp", "permission", map[string]any{"operationType": "ResolveWorkOrder", "scope": "any"})
	f.edge(t, "grantedBy", "permResolveOp", "operator")
	f.edge(t, "forOperation", "permResolveOp", "fullOp")

	f.vtxData(t, "permBare", "permission", map[string]any{"operationType": "RecordLeaseDocOutcome", "scope": "any"})
	f.edge(t, "grantedBy", "permBare", "operator")
	f.edge(t, "forOperation", "permBare", "bareOp")

	return f
}

// emCatalogRows indexes an opCatalog projection by operationType, which is the
// lens's own IntoKey — so a duplicate here is a real key collision, not a test
// convenience, and is asserted as one.
func emCatalogRows(t *testing.T, rows []ruleengine.ProjectionResult) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for _, r := range rows {
		op, _ := r.Values["operationType"].(string)
		if op == "" {
			continue
		}
		require.NotContainsf(t, out, op,
			"two rows for operationType %q would race for one op-catalog key", op)
		out[op] = r.Values
	}
	return out
}

// TestOpCatalog_FullVocabularyOpProjectsEveryColumn is the positive vector: a
// staff renderer needs the WHOLE descriptor off one row — no column may be
// quietly dropped in the copy from edgeCatalogTail, because a missing
// dispatch column is a form that renders and then submits a malformed
// envelope.
func TestOpCatalog_FullVocabularyOpProjectsEveryColumn(t *testing.T) {
	f := emOpCatalogWorld(t)
	rows := emCatalogRows(t, f.project(t, opCatalogSpec, ""))

	row, ok := rows["ResolveWorkOrder"]
	require.True(t, ok, "the fully-described op must project; got %v", rows)

	require.Equal(t, f.key("fullOp"), row["opMetaKey"])
	require.Equal(t, "Resolve a work order", row["title"])
	require.Equal(t, "Resolve", row["shortLabel"])
	require.Equal(t, "Record what you did.", row["description"])
	require.Equal(t, "wrench", row["icon"])
	require.Equal(t, "primary", row["tone"])
	require.Equal(t, "Mark resolved", row["submitLabel"])
	require.Equal(t, "Maintenance", row["group"])

	require.Contains(t, row["inputSchema"], `"workOrderKey"`,
		"inputSchema rides as the declared JSON-schema STRING — the renderer parses it client-side")
	fd, isMap := row["fieldDescriptions"].(map[string]any)
	require.True(t, isMap, "fieldDescriptions must project as a map, got %T", row["fieldDescriptions"])
	require.Equal(t, "What you actually did.", fd["notes"])

	require.Equal(t, "workOrder", row["dispatchClass"],
		"an envelope with no class is unconditionally rejected — this column is not decoration")
	require.Equal(t, "task", row["dispatchAuthContext"])
	require.Equal(t, "workOrderKey", row["dispatchTargetField"])
	require.Equal(t, "workorder", row["dispatchTargetType"])
	require.Equal(t, []any{"{payload.workOrderKey}"}, row["dispatchReads"])
	require.Equal(t, []any{"{payload.workOrderKey}.resolution"}, row["dispatchOptionalReads"])
	require.Equal(t, true, row["sensitive"],
		"the masking rule the modal keys on — absent, a client renders an SSN in the clear")
}

// TestOpCatalog_BareOpMetaProjectsWithNullSchema pins the fail-closed
// degradation: an op that adopted none of the vocabulary still gets a ROW,
// with null descriptor columns. It must not be filtered out — a missing row is
// indistinguishable from a lagging projection, whereas a row with no
// inputSchema is a positive statement the renderer acts on by declining to
// offer the op.
func TestOpCatalog_BareOpMetaProjectsWithNullSchema(t *testing.T) {
	f := emOpCatalogWorld(t)
	rows := emCatalogRows(t, f.project(t, opCatalogSpec, ""))

	row, ok := rows["RecordLeaseDocOutcome"]
	require.True(t, ok, "a bare op meta must still project a row; got %v", rows)
	require.Equal(t, f.key("bareOp"), row["opMetaKey"])
	require.Nil(t, row["inputSchema"], "nothing to render from — the renderer's not-offerable signal")
	require.Nil(t, row["title"])
	require.Nil(t, row["dispatchClass"])
	require.Nil(t, row["sensitive"])
	require.Equal(t, []any{"operator"}, row["grantedToRoles"],
		"a bare op still carries its grant topology — visibility is derivable even when the form is not")
}

// TestOpCatalog_RoleGrantedOpCarriesItsGrantingRoleNames pins the join a staff
// client curates its offered set from: the ROLE NAMES, collapsed onto the op's
// single row. Two granting roles must not fan the op into two rows — they
// would race for one op-catalog key, and whichever landed last would erase the
// other's roles.
func TestOpCatalog_RoleGrantedOpCarriesItsGrantingRoleNames(t *testing.T) {
	f := emOpCatalogWorld(t)
	rows := emCatalogRows(t, f.project(t, opCatalogSpec, ""))

	roles, isList := rows["ResolveWorkOrder"]["grantedToRoles"].([]any)
	require.True(t, isList, "grantedToRoles must be a list, got %T", rows["ResolveWorkOrder"]["grantedToRoles"])
	require.ElementsMatch(t, []any{"backOfHouse", "operator"}, roles,
		"both granting roles collapse onto the one row")
	for _, r := range roles {
		require.NotContains(t, r, "vtx.role.",
			"the names a client filters on — a vertex key here surfaces a NanoID where a role name belongs")
	}
}

// TestOpCatalog_UngrantedOpStillProjects is the OPTIONAL MATCH's own vector,
// with the mutation that motivates it. An op meta no permission grants — an
// engine leg, or an op whose grant ships in a later package version — must
// degrade to an empty grantedToRoles, never disappear: a catalog that silently
// omits ops is worse than one that says "nobody holds this yet", because the
// omission is unattributable at the client.
func TestOpCatalog_UngrantedOpStillProjects(t *testing.T) {
	f := emOpCatalogWorld(t)

	rows := emCatalogRows(t, f.project(t, opCatalogSpec, ""))
	row, ok := rows["OpenRenewal"]
	require.True(t, ok, "an op with no granting permission must still project; got %v", rows)
	require.Equal(t, "Open a renewal", row["title"])
	require.Empty(t, row["grantedToRoles"],
		"a bare-property collect over an unmatched OPTIONAL binding is empty, not [null]")

	// MUTATION — make the role join required. The op vanishes outright.
	required := strings.Replace(opCatalogSpec,
		"OPTIONAL MATCH (op)<-[:forOperation]-", "MATCH (op)<-[:forOperation]-", 1)
	require.NotEqual(t, opCatalogSpec, required, "the mutation must actually change the spec")
	mutated := emCatalogRows(t, f.project(t, required, ""))
	require.NotContains(t, mutated, "OpenRenewal",
		"the mutant must LOSE the ungranted op — if it still projects, this test is not proving the OPTIONAL is load-bearing")
	require.Contains(t, mutated, "ResolveWorkOrder",
		"and must keep the granted one, so the mutation is narrowing the join rather than breaking the query")
}

// TestOpCatalog_NonOpMetasAreFilteredOut pins the `operationType <> null`
// WHERE, with its mutation. `:meta` is also the label of every DDL, lens, pane
// and package meta-vertex pkgmgr installs; without the filter they all land in
// the catalog keyed on a NULL operationType, which is both a garbage row and a
// key collision waiting to happen among themselves.
func TestOpCatalog_NonOpMetasAreFilteredOut(t *testing.T) {
	f := emOpCatalogWorld(t)

	for _, r := range f.project(t, opCatalogSpec, "") {
		require.NotEqual(t, f.key("ddlMeta"), r.Values["opMetaKey"],
			"a DDL meta-vertex is not an operation and must not reach the catalog")
	}

	// MUTATION — drop the filter. The non-op meta leaks in.
	unfiltered := strings.Replace(opCatalogSpec, "\nWHERE op.data.operationType <> null", "", 1)
	require.NotEqual(t, opCatalogSpec, unfiltered, "the mutation must actually change the spec")
	leaked := false
	for _, r := range f.project(t, unfiltered, "") {
		if r.Values["opMetaKey"] == f.key("ddlMeta") {
			leaked = true
			require.Nil(t, r.Values["operationType"],
				"and it leaks keyed on a null operationType — the row the filter exists to prevent")
		}
	}
	require.True(t, leaked,
		"the mutant must LEAK the DDL meta — if it does not, the filter is not what is keeping the catalog clean")
}

// TestOpCatalog_TombstonedOpMetaRetractsItsRow is the retraction pin, and the
// one the design's adversarial pass (B3) was filed against: the tempting
// copy-paste source, edgeCatalogTail, OPENS with `WITH op, role`, and
// anchorProjectionShape (internal/refractor/ruleengine/full/anchor_delete.go)
// refuses any query carrying a WITH — wholesale, silently, before it ever
// looks at the anchor. The lens would still parse, still project, still pass
// every other test in this file, and simply never retract a retired op's row,
// leaving the catalog describing an operation the Processor no longer accepts.
//
// Two halves, because the mechanism has two:
//
//   - the whole-corpus re-scan stops UPSERTING the tombstoned anchor (that
//     alone leaves the prior row stale forever — it is why a Delete exists);
//   - AnchorDeleteResult resolves the exact row key to DELETE, read-free from
//     the tombstoned body, which is what the key being a ROOT field buys.
func TestOpCatalog_TombstonedOpMetaRetractsItsRow(t *testing.T) {
	f := emOpCatalogWorld(t)
	require.Contains(t, emCatalogRows(t, f.project(t, opCatalogSpec, "")), "ResolveWorkOrder",
		"precondition: the op is in the catalog before it is retired")

	body := f.tombstoneKeepingBody(t, "fullOp")
	require.NotContains(t, emCatalogRows(t, f.project(t, opCatalogSpec, "")), "ResolveWorkOrder",
		"a tombstoned anchor must stop projecting — but that alone only makes the stale row invisible to the scan, never removed")

	eng := full.New()
	compiled, err := eng.Parse(opCatalogSpec)
	require.NoError(t, err)
	cr, isFull := compiled.(*full.CompiledRule)
	require.True(t, isFull)
	cr.KeyColumns = []string{"operationType"}
	require.NoError(t, cr.ValidateKeyColumns(),
		"the lens must activate against its declared IntoKey")

	keys, ok := eng.AnchorDeleteResult(cr, f.key("fullOp"), "meta", body)
	require.True(t, ok,
		"the retired op's row key must resolve read-free from the tombstoned body — no WITH, a labeled :meta anchor, and a key column off the anchor's own ROOT data")
	require.Equal(t, map[string]any{"operationType": "ResolveWorkOrder"}, keys,
		"the Delete must name the row the upsert path wrote, or it retracts nothing (or worse, something else)")

	// MUTATION 1 — the `WITH op, role` opener edgeCatalogTail carries. The
	// query still parses and still projects; the retraction silently dies.
	withOpener := strings.Replace(opCatalogSpec, "\nRETURN\n", "\nWITH op, role\nRETURN\n", 1)
	require.NotEqual(t, opCatalogSpec, withOpener, "the mutation must actually change the spec")
	withCompiled, err := eng.Parse(withOpener)
	require.NoError(t, err, "the mutant must still PARSE — that is precisely what makes this trap silent")
	withCR, isFull := withCompiled.(*full.CompiledRule)
	require.True(t, isFull)
	withCR.KeyColumns = []string{"operationType"}
	_, withOK := eng.AnchorDeleteResult(withCR, f.key("fullOp"), "meta", body)
	require.False(t, withOK,
		"the mutant must LOSE the retraction — if a WITH-carrying spec still retracts, this test is not proving the no-WITH rule")

	// MUTATION 2 — key the lens off an ASPECT field instead of the anchor's
	// root data. It projects identically on a live vertex and resolves to
	// nothing on a tombstoned one, because the aspect read is disabled on the
	// read-free binding. Same silent outcome, a different way in.
	aspectKeyed := strings.Replace(opCatalogSpec,
		"op.data.operationType AS operationType", "op.dispatch.data.targetField AS operationType", 1)
	require.NotEqual(t, opCatalogSpec, aspectKeyed, "the mutation must actually change the spec")
	aspectCompiled, err := eng.Parse(aspectKeyed)
	require.NoError(t, err)
	aspectCR, isFull := aspectCompiled.(*full.CompiledRule)
	require.True(t, isFull)
	aspectCR.KeyColumns = []string{"operationType"}
	_, aspectOK := eng.AnchorDeleteResult(aspectCR, f.key("fullOp"), "meta", body)
	require.False(t, aspectOK,
		"an aspect-sourced key column must LOSE the retraction — the anchor-derived key is a requirement, not an accident of which column read nicely")
}
