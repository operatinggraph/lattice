package loftspacedomain

// Rule-engine proof of the landlordUnitsRead cypher (portfolio-pulse Inc 2,
// mixed-use-composition-design.md). Driven through the `full` engine against
// an embedded NATS Core/Adjacency KV, the same harness lens_cypher_test.go and
// front-desk/one-bill's lens tests use.
//
//   - TestLandlordUnitsRead_ProjectsManagedUnit: a unit with a `manages` link
//     from a landlord projects one row, status/rent/currency carried from the
//     `.listing` aspect, authz_anchors = [landlord's bare NanoID].
//   - TestLandlordUnitsRead_ProjectsUnlistedUnitAsNullStatus: a managed unit
//     with NO `.listing` aspect still projects a row (unlike availableListings,
//     which excludes it) — unit_status is null, the occupancy handler's
//     "not yet listed" bucket, not an error.
//   - TestLandlordUnitsRead_ExcludesUnmanagedUnit: a unit with no `manages`
//     link projects nothing — the MATCH requires the link.
//   - TestLandlordUnitsRead_FansOutPerLandlordForCoManagedUnit: a unit managed
//     by two landlords projects two rows, one per landlord, each anchored to
//     only that landlord.
//   - TestLandlordUnitsRead_BuildingFanOut: a managed unit covered by a
//     building projects authz_anchors = [landlord, building] — the same
//     `[landlordKey] + [containedIn building tokens]` shape
//     landlordLeaseApplicationsRead already anchors on, so a front-desk
//     staffer's worksAt-building grant resolves this model too (the
//     portfolio-pulse composition gap: landlordUnitsRead used to be
//     landlord-self-anchored only).
//   - TestLandlordUnitsRead_UsesComprehensionNotLiteralElement: the workplace
//     token must be a pattern comprehension (yields [] when absent), never a
//     literal array element (yields a null element that fails the whole row's
//     Protected-adapter upsert — see lease-signing's identical guard).

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/lenstest"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// luFixture extends the identity-only lensFixture with generic typed vertices
// and links, needed to build unit/landlord/manages graphs.
type luFixture struct {
	*lensFixture
	types map[string]string // bare NanoID -> vertex type
}

func newLuFixture(t *testing.T) *luFixture {
	f := newLensFixture(t)
	return &luFixture{lensFixture: f, types: map[string]string{}}
}

func (f *luFixture) vtx(t *testing.T, name, typ string) string {
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

func (f *luFixture) unitAspect(t *testing.T, name, local, class string, data map[string]any) {
	t.Helper()
	owner := "vtx." + f.types[f.ids[name]] + "." + f.ids[name]
	key := owner + "." + local
	body := map[string]any{"key": key, "class": class, "vertexKey": owner, "localName": local, "isDeleted": false, "data": data}
	raw, _ := json.Marshal(body)
	_, err := f.coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

func (f *luFixture) manages(t *testing.T, landlordName, unitName string) {
	t.Helper()
	ctx := context.Background()
	landlordID, unitID := f.ids[landlordName], f.ids[unitName]
	landlordType, unitType := f.types[landlordID], f.types[unitID]
	linkKey := "lnk." + landlordType + "." + landlordID + ".manages." + unitType + "." + unitID
	edgeID := "manages_" + landlordID + "_" + unitID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: "manages", Direction: "outbound", NodeID: landlordID, OtherNodeID: unitID, OtherType: unitType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: "manages", Direction: "inbound", NodeID: unitID, OtherNodeID: landlordID, OtherType: landlordType}))
}

// containedIn mirrors manages: it must read luFixture's OWN types map (the
// field luFixture declares, shadowing the embedded lensFixture.types), the
// same map vtx()/manages() populate — the inherited lensFixture.edge() reads
// the embedded map instead and silently resolves empty types here.
func (f *luFixture) containedIn(t *testing.T, unitName, buildingName string) {
	t.Helper()
	ctx := context.Background()
	unitID, buildingID := f.ids[unitName], f.ids[buildingName]
	unitType, buildingType := f.types[unitID], f.types[buildingID]
	linkKey := "lnk." + unitType + "." + unitID + ".containedIn." + buildingType + "." + buildingID
	edgeID := "containedIn_" + unitID + "_" + buildingID
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: "containedIn", Direction: "outbound", NodeID: unitID, OtherNodeID: buildingID, OtherType: buildingType}))
	require.NoError(t, adjacency.Build(ctx, f.adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: edgeID, Name: "containedIn", Direction: "inbound", NodeID: buildingID, OtherNodeID: unitID, OtherType: unitType}))
}

func (f *luFixture) projectUnits(t *testing.T) []ruleengine.ProjectionResult {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(landlordUnitsReadSpec)
	require.NoError(t, err, "landlordUnitsRead cypher must parse on the full engine")
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{}, f.adjKV, f.coreKV)
	require.NoError(t, err)
	return out
}

func TestLandlordUnitsRead_ProjectsManagedUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	unitKey := f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.unitAspect(t, "u1", "listing", "listing", map[string]any{"status": "leased", "rentAmount": 1500.0, "rentCurrency": "USD"})

	rows := f.projectUnits(t)
	require.Len(t, rows, 1)
	v := rows[0].Values
	require.Equal(t, unitKey, v["unit_key"])
	require.Equal(t, "leased", v["unit_status"])
	require.Equal(t, 1500.0, v["unit_rent"])
	require.Equal(t, "USD", v["unit_currency"])
	anchors, ok := v["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", v["authz_anchors"])
	require.Equal(t, []any{f.ids["larry"]}, anchors, "authz_anchors must carry exactly the managing landlord's bare NanoID")
}

func TestLandlordUnitsRead_ProjectsUnlistedUnitAsNullStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	// No .listing aspect written — the unit was created but never listed.

	rows := f.projectUnits(t)
	require.Len(t, rows, 1, "a managed-but-unlisted unit still projects a row")
	require.Nil(t, rows[0].Values["unit_status"], "unit_status is null, not an excluded row")
}

func TestLandlordUnitsRead_ExcludesUnmanagedUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "u1", "unit")
	f.unitAspect(t, "u1", "listing", "listing", map[string]any{"status": "available"})
	// No manages link written.

	rows := f.projectUnits(t)
	require.Empty(t, rows, "a unit with no manages link has no landlord to anchor the row on")
}

func TestLandlordUnitsRead_FansOutPerLandlordForCoManagedUnit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "linda", "identity")
	f.vtx(t, "u1", "unit")
	f.manages(t, "larry", "u1")
	f.manages(t, "linda", "u1")
	f.unitAspect(t, "u1", "listing", "listing", map[string]any{"status": "pending"})

	rows := f.projectUnits(t)
	require.Len(t, rows, 2, "a co-managed unit fans out to one row per landlord")
	byLandlord := map[string][]any{}
	for _, r := range rows {
		anchors := r.Values["authz_anchors"].([]any)
		byLandlord[r.Values["landlord_key"].(string)] = anchors
	}
	require.Equal(t, []any{f.ids["larry"]}, byLandlord["vtx.identity."+f.ids["larry"]])
	require.Equal(t, []any{f.ids["linda"]}, byLandlord["vtx.identity."+f.ids["linda"]])
}

func TestLandlordUnitsRead_BuildingFanOut(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	f := newLuFixture(t)
	f.vtx(t, "larry", "identity")
	f.vtx(t, "u1", "unit")
	f.vtx(t, "tower", "building")
	f.manages(t, "larry", "u1")
	f.containedIn(t, "u1", "tower")
	f.unitAspect(t, "u1", "listing", "listing", map[string]any{"status": "leased"})

	rows := f.projectUnits(t)
	require.Len(t, rows, 1)
	anchors, ok := rows[0].Values["authz_anchors"].([]any)
	require.True(t, ok, "authz_anchors must be a list, got %T", rows[0].Values["authz_anchors"])
	require.ElementsMatch(t, []any{f.ids["larry"], f.ids["tower"]}, anchors,
		"authz_anchors must carry the managing landlord PLUS every building covering the unit, mirroring landlordLeaseApplicationsRead")
}

// TestLandlordUnitsRead_UsesComprehensionNotLiteralElement mirrors
// lease-signing's TestWorkplaceAnchor_UsesComprehensionNotLiteralElement: a
// literal array element (not a pattern comprehension) yields a null element
// for a building-less unit, which the Protected adapter's toStringSlice
// rejects — failing the row's entire upsert and losing it for the landlord
// too. A missing building must cost the row its staff visibility, never its
// existence.
func TestLandlordUnitsRead_UsesComprehensionNotLiteralElement(t *testing.T) {
	spec := landlordUnitsReadSpec

	if !strings.Contains(spec, "[(u)-[:containedIn]->(b:building) | nanoIdFromKey(b.key)]") {
		t.Fatal("the workplace anchor must be a pattern comprehension (yields [] when absent), not an array element (yields a null element)")
	}
	if !strings.Contains(spec, "[nanoIdFromKey(landlord.key)] +") {
		t.Error("the landlord anchor must remain the first, unconditional element — staff visibility is additive to it, never a replacement")
	}
	if strings.Contains(spec, "[nanoIdFromKey(landlord.key), nanoIdFromKey(") {
		t.Error("two-element array literal reintroduces the null-element hazard: a building-less unit would fail the whole row's upsert")
	}
}
