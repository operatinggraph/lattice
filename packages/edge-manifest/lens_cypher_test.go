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
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

func emCypherKVs(t *testing.T) (adjKV, coreKV *substrate.KV) {
	t.Helper()
	_, nc := natstest.Server(t)
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	ctx := context.Background()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "adj-edgemanifest-cypher-test"})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "core-edgemanifest-cypher-test"})
	require.NoError(t, err)
	adjKV, err = conn.OpenKV(ctx, "adj-edgemanifest-cypher-test")
	require.NoError(t, err)
	coreKV, err = conn.OpenKV(ctx, "core-edgemanifest-cypher-test")
	require.NoError(t, err)
	return adjKV, coreKV
}

// emNanoID returns a deterministic 20-char Contract #1 NanoID from a logical
// name (the wellness-domain helper's derivation).
func emNanoID(name string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte(name) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

type emFixture struct {
	adjKV, coreKV *substrate.KV
	ids           map[string]string
	types         map[string]string
}

func newEmFixture(t *testing.T) *emFixture {
	adjKV, coreKV := emCypherKVs(t)
	return &emFixture{adjKV: adjKV, coreKV: coreKV, ids: map[string]string{}, types: map[string]string{}}
}

func (f *emFixture) vtx(t *testing.T, name, typ string) string {
	t.Helper()
	id := emNanoID(name)
	f.ids[name] = id
	f.types[id] = typ
	key := "vtx." + typ + "." + id
	body := map[string]any{"key": key, "class": typ, "isDeleted": false, "data": map[string]any{}}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return key
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
