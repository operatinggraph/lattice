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

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

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
	id := cNanoID(name)
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
