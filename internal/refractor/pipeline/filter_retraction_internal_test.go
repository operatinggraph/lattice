package pipeline

// Filter-retraction + plain-lens aspect/link reprojection coverage.
//
// A plain (non-actor-aware) projection lens must (a) reproject on an
// aspect/link-only mutation — refreshing aspect-derived fields promptly rather
// than waiting for an unrelated vertex-root event — and (b) retract its row
// when the anchor's own mutation drops it out of the matched set (a WHERE
// predicate flip via keyed-aspect deletion, or a required-link removal),
// which the upsert-only re-scan path never does. The retraction Delete is
// derived read-free by AnchorProjectionKey and is emitted only for a
// one-row-per-anchor anchor-keyed lens; a neighbor-keyed composite lens falls
// through to the pre-existing linger behaviour (pinned below — closing it
// needs the target-diff mechanism, the design's deferred Fire 3).

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// listedUnitsSpec is the availableListings shape: an unanchored whole-type
// scan whose WHERE keys on an aspect's presence — the live-lens pattern whose
// predicate an aspect-only mutation flips.
const listedUnitsSpec = `
MATCH (u:unit)
WHERE u.listing.data.status <> null
RETURN u.key AS key, u.listing.data.status AS status, u.address.data.city AS city
`

// newRetractionPipeline stands up a full-engine plain pipeline over embedded
// NATS with a NATS-KV target and returns the pipeline + core/adj/target KVs.
func newRetractionPipeline(t *testing.T, spec string, keyCols []string) (*Pipeline, *substrate.KV, *substrate.KV, *substrate.KV) {
	t.Helper()
	// The otherwise-unused HEALTH bucket serves as the projection target so
	// target reads can never collide with vtx.* input keys in CORE.
	coreKV, adjKV, targetKV := newCollisionKVs(t)

	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	if len(keyCols) > 0 {
		fullCR, isFull := cr.(*full.CompiledRule)
		require.True(t, isFull)
		fullCR.KeyColumns = keyCols
		require.NoError(t, fullCR.ValidateKeyColumns())
	}

	adpt, err := adapter.New(targetKV, keyColsOrDefault(keyCols), adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := New("filter-retraction", "nats_kv", "CORE", adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	return p, coreKV, adjKV, targetKV
}

func keyColsOrDefault(cols []string) []string {
	if len(cols) == 0 {
		return []string{"key"}
	}
	return cols
}

// putBody marshals and PUTs a Core KV body, returning the raw bytes for use
// as a CDC message payload.
func putBody(t *testing.T, kv *substrate.KV, key string, body map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = kv.Put(context.Background(), key, raw)
	require.NoError(t, err)
	return raw
}

func aspectBody(vertexKey, localName string, data map[string]any, isDeleted bool) map[string]any {
	return map[string]any{
		"key": vertexKey + "." + localName, "class": localName, "vertexKey": vertexKey,
		"localName": localName, "isDeleted": isDeleted, "data": data,
	}
}

// TestPlainLens_AspectReprojection_RefreshAndRetract drives the full
// aspect-event lifecycle through handle(): a listed unit projects, an
// aspect-only field change refreshes the row (the Fire-1 freshness fix — the
// plain pipeline previously ack-skipped every KindAspect event), a soft
// tombstone of the predicate aspect retracts the row (the Fire-2
// filter-retraction — the WHERE flips false), and re-listing re-projects it.
func TestPlainLens_AspectReprojection_RefreshAndRetract(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, _, targetKV := newRetractionPipeline(t, listedUnitsSpec, []string{"key"})

	const unitID = "FRunitAAAAAAAAAAAAAA"
	unitKey := "vtx.unit." + unitID
	unitBody := putBody(t, coreKV, unitKey, map[string]any{
		"key": unitKey, "class": "unit", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, unitKey+".listing", aspectBody(unitKey, "listing", map[string]any{"status": "active"}, false))
	putBody(t, coreKV, unitKey+".address", aspectBody(unitKey, "address", map[string]any{"city": "Lisbon"}, false))

	// 1. Vertex-root event → the listed unit projects.
	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + unitKey, Body: unitBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	entry, err := targetKV.Get(ctx, unitKey)
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	require.Equal(t, "Lisbon", row["city"])

	// 2. Aspect-only mutation (address city changes) → the row refreshes on
	// the aspect event itself, with no vertex-root event.
	addrRaw2 := putBody(t, coreKV, unitKey+".address", aspectBody(unitKey, "address", map[string]any{"city": "Porto"}, false))
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + unitKey + ".address", Body: addrRaw2, Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	entry, err = targetKV.Get(ctx, unitKey)
	require.NoError(t, err)
	row = nil
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	require.Equal(t, "Porto", row["city"], "an aspect-only mutation must refresh the projected row")

	// 3. Soft-tombstone the predicate aspect (isDeleted:true PUT — the
	// Processor's tombstone shape) → the WHERE flips false → the row is
	// RETRACTED from the target.
	listingGone := putBody(t, coreKV, unitKey+".listing", aspectBody(unitKey, "listing", map[string]any{"status": "active"}, true))
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + unitKey + ".listing", Body: listingGone, Sequence: 3})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	_, err = targetKV.Get(ctx, unitKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"a WHERE flip via keyed-aspect tombstone must retract the projected row")

	// 4. Re-list → the anchor re-matches and re-projects.
	listingBack := putBody(t, coreKV, unitKey+".listing", aspectBody(unitKey, "listing", map[string]any{"status": "active"}, false))
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + unitKey + ".listing", Body: listingBack, Sequence: 4})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	entry, err = targetKV.Get(ctx, unitKey)
	require.NoError(t, err)
	row = nil
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	require.Equal(t, "active", row["status"], "a re-matching anchor re-projects")
}

// servicedIdentitiesSpec requires a link: a service row exists only while the
// providedTo relationship stands.
const servicedIdentitiesSpec = `
MATCH (svc:service)
MATCH (svc)-[:providedTo]->(id:identity)
RETURN svc.key AS key, id.key AS holder
`

// TestPlainLens_LinkReprojection_RequiredLinkRemovalRetracts proves the plain
// KindLink arm: a link tombstone re-executes from both endpoints and the
// anchor whose required MATCH the removal breaks is retracted read-free.
func TestPlainLens_LinkReprojection_RequiredLinkRemovalRetracts(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, servicedIdentitiesSpec, []string{"key"})

	const svcID = "FRserviceAAAAAAAAAAA"
	const idID = "FRidentityAAAAAAAAAA"
	svcKey := "vtx.service." + svcID
	idKey := "vtx.identity." + idID
	svcBody := putBody(t, coreKV, svcKey, map[string]any{
		"key": svcKey, "class": "service", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, idKey, map[string]any{
		"key": idKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	// Seed the edge exactly as the dedicated adjacency consumer does (the
	// link key is the EdgeID), so the pipeline's own tombstone Build removes
	// the same entry.
	linkKey := "lnk.service." + svcID + ".providedTo.identity." + idID
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: "providedTo",
		Direction: "outbound", NodeID: svcID, OtherNodeID: idID, OtherType: "identity",
	}))
	require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
		CoreKvKey: linkKey, EdgeID: linkKey, Name: "providedTo",
		Direction: "inbound", NodeID: idID, OtherNodeID: svcID, OtherType: "service",
	}))

	// 1. Vertex-root event → the serviced row projects.
	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + svcKey, Body: svcBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	_, err = targetKV.Get(ctx, svcKey)
	require.NoError(t, err)

	// 2. The link's soft-tombstone CDC event arrives BEFORE the dedicated
	// adjacency consumer has removed the edge (the cross-consumer race). The
	// plain pipeline previously ack-skipped it; now it idempotently applies
	// the tombstone to adjacency itself, reprojects from both endpoints, and
	// retracts the service row whose required MATCH broke.
	linkTombstone, err := json.Marshal(map[string]any{"key": linkKey, "isDeleted": true})
	require.NoError(t, err)
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + linkKey, Body: linkTombstone, Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	_, err = targetKV.Get(ctx, svcKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"a required-link removal must retract the anchor's projected row")
}

// landlordShapeSpec is the neighbor-keyed composite shape (the landlord
// lease-applications lens): the second key column binds a NON-anchor variable,
// so the read-free retraction key is underivable by construction.
const landlordShapeSpec = `
MATCH (app:leaseapp)
MATCH (app)-[:appliesToUnit]->(u:unit)
MATCH (u)<-[:manages]-(landlord:identity)
RETURN nanoIdFromKey(app.key) AS app_id, nanoIdFromKey(landlord.key) AS landlord_id
`

// TestPlainLens_NeighborKeyedComposite_FallsThroughToLinger pins the safety
// boundary: when a lens's key columns are not anchor-derivable
// (AnchorProjectionKey ok=false), a drop from the matched set emits NO Delete
// — the row lingers, exactly today's behaviour, never a wrong or partial
// Delete. (Closing this linger for neighbor-keyed lenses is the design's
// deferred target-diff Fire 3 — the Vault 5b manages-unassign case.)
func TestPlainLens_NeighborKeyedComposite_FallsThroughToLinger(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, _ := newRetractionPipeline(t, landlordShapeSpec, []string{"app_id", "landlord_id"})

	const appID = "FRLeaseappAAAAAAAAAA"
	const unitID = "FRunitBBBBBBBBBBBBBB"
	const llID = "FRLandLordAAAAAAAAAA"
	appKey := "vtx.leaseapp." + appID
	putBody(t, coreKV, appKey, map[string]any{
		"key": appKey, "class": "leaseapp", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, "vtx.unit."+unitID, map[string]any{
		"key": "vtx.unit." + unitID, "class": "unit", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, "vtx.identity."+llID, map[string]any{
		"key": "vtx.identity." + llID, "class": "identity", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	buildCollisionEdge(t, adjKV, "appliesToUnit", "leaseapp", appID, "unit", unitID)
	buildCollisionEdge(t, adjKV, "manages", "identity", llID, "unit", unitID)

	appEntry := ruleengine.NodeEntry{
		CoreKVKey: appKey, NodeLabel: "leaseapp",
		Properties: map[string]any{"lastModifiedAt": "2026-07-02T10:00:00Z"},
	}
	results, err := p.evaluateForEntry(ctx, appEntry)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Delete)

	// Unassign: the manages link leaves adjacency. Re-evaluating the anchor
	// yields zero rows AND zero Deletes — the composite (app_id, landlord_id)
	// key cannot be derived from the anchor alone, so the presence check must
	// fall through rather than guess.
	edgeID := "manages:" + llID + ":" + unitID
	for _, d := range []struct{ dir, nodeID, otherID, otherType string }{
		{"outbound", llID, unitID, "unit"},
		{"inbound", unitID, llID, "identity"},
	} {
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: "lnk.identity." + llID + ".manages.unit." + unitID, EdgeID: edgeID,
			Name: "manages", Direction: d.dir, NodeID: d.nodeID,
			OtherNodeID: d.otherID, OtherType: d.otherType, IsDeleted: true,
		}))
	}
	results, err = p.evaluateForEntry(ctx, appEntry)
	require.NoError(t, err)
	for _, r := range results {
		require.False(t, r.Delete,
			"a neighbor-keyed composite lens must fall through (linger), never emit a derived Delete")
	}
	require.Empty(t, results, "the dropped anchor re-derives no rows")
}

// TestSetDiffRetraction_NonKeyListerAdapter_Refused pins the fail-closed end of
// the opt-in. A lens whose target cannot enumerate its keys can never retract a
// row, and the failure is invisible from the outside — the lens keeps upserting,
// so it looks alive. For a grant producer that inertness IS a stale-access bug
// (proven live 2026-07-19: an unwire left the grant and the ex-staff actor kept
// reading the row). Activation must refuse, so the lens stays dark.
func TestSetDiffRetraction_NonKeyListerAdapter_Refused(t *testing.T) {
	p, err := New("no-keylister", "nats_kv", "CORE", nil, nil, &keyedAdapter{}, nil)
	require.NoError(t, err)

	err = p.SetDiffRetraction(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adapter.KeyLister")
	assert.False(t, p.diffRetraction, "a refused opt-in must not leave the lens half-armed")

	// Disabling is always allowed — no enumeration is required to not retract.
	require.NoError(t, p.SetDiffRetraction(false))
}

// TestApplyDiffRetraction_NonKeyListerAdapter_Errors covers the runtime half of
// the same rule: activation refuses first, so reaching here means the adapter
// was swapped underneath a running pipeline. The projection fails loudly rather
// than passing results through as if retraction had run.
func TestApplyDiffRetraction_NonKeyListerAdapter_Errors(t *testing.T) {
	p, err := New("swapped-adapter", "nats_kv", "CORE", nil, nil, &keyedAdapter{}, nil)
	require.NoError(t, err)

	_, err = p.applyDiffRetraction(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not implement adapter.KeyLister")
}

// TestPlainLens_NeighborKeyedComposite_DiffRetractionDeletes proves Fire 3
// closes exactly the linger the sibling test above pins: the same
// neighbor-keyed composite shape, but with DiffRetraction opted in
// (landlordLeaseApplicationsRead's real posture) and driven through the real
// dispatch (handle) rather than a bare evaluate call. A landlord manages a
// unit a leaseapp applies to (the row projects); the manages link is then
// removed (the manages-unassign that gates Vault 5b's close) via a real
// link-tombstone CDC message — Fire 2 still cannot derive the composite key,
// but Fire 3's target-diff reads the target's live key set, finds the row
// Fire 2 could never retract, and deletes it.
func TestPlainLens_NeighborKeyedComposite_DiffRetractionDeletes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, _, targetKV := newRetractionPipeline(t, landlordShapeSpec, []string{"app_id", "landlord_id"})
	require.NoError(t, p.SetDiffRetraction(true))

	const appID = "FRDiffAppAAAAAAAAAAA"
	const unitID = "FRDiffUnitAAAAAAAAAA"
	const llID = "FRDiffMgrAAAAAAAAAAA"
	appKey := "vtx.leaseapp." + appID
	unitKey := "vtx.unit." + unitID
	llKey := "vtx.identity." + llID
	appBody := putBody(t, coreKV, appKey, map[string]any{
		"key": appKey, "class": "leaseapp", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, unitKey, map[string]any{
		"key": unitKey, "class": "unit", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, llKey, map[string]any{
		"key": llKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	// Both links are established through the real dispatch (handle), not the
	// buildCollisionEdge test shortcut — evalPlainLinkReprojection idempotently
	// applies its OWN adjacency.Build keyed by the link's Core KV key as
	// EdgeID (matching the dedicated adjacency consumer's convention), so the
	// manages-unassign tombstone below must reference the exact same edge.
	applyLinkKey := "lnk.leaseapp." + appID + ".appliesToUnit.unit." + unitID
	manageLinkKey := "lnk.identity." + llID + ".manages.unit." + unitID
	linkBody := func(class string) []byte {
		b, err := json.Marshal(map[string]any{"class": class, "isDeleted": false})
		require.NoError(t, err)
		return b
	}
	_, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + applyLinkKey, Body: linkBody("appliesToUnit"), Sequence: 1})
	require.NoError(t, err)
	_, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + manageLinkKey, Body: linkBody("manages"), Sequence: 2})
	require.NoError(t, err)

	// The leaseapp's own vertex-root event, through the real dispatch —
	// projects the row into the target.
	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + appKey, Body: appBody, Sequence: 3})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	targetKey := appID + "." + llID
	_, err = targetKV.Get(ctx, targetKey)
	require.NoError(t, err, "row must be live in the target after the initial projection")

	// Manages-unassign: a real link-tombstone CDC event on the manages link
	// (landlord -[:manages]-> unit) — the plain-lens link-reprojection path.
	dec, err = p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + manageLinkKey, Body: nil, Sequence: 4})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	_, err = targetKV.Get(ctx, targetKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"the manages-unassign must retract the row via Fire 3's target-diff — Fire 2 cannot derive this composite key")
}

// TestPlainLens_NeverMatchedAnchor_IdempotentDelete pins the R2 posture: an
// event for an anchor that never matched the lens emits a Delete against its
// derived key — an idempotent no-op on an absent target key, chosen over
// tracking per-anchor projection history.
func TestPlainLens_NeverMatchedAnchor_IdempotentDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, _, targetKV := newRetractionPipeline(t, listedUnitsSpec, []string{"key"})

	const unitID = "FRunitCCCCCCCCCCCCCC"
	unitKey := "vtx.unit." + unitID
	// A unit with NO .listing aspect — never matches the WHERE.
	unitBody := putBody(t, coreKV, unitKey, map[string]any{
		"key": unitKey, "class": "unit", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + unitKey, Body: unitBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec, "a Delete against an absent key is a no-op, not an error")
	_, err = targetKV.Get(ctx, unitKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound)
}

// TestPlainLens_IrrelevantTypeSkipped pins the type-relevance bound: an
// aspect or link event whose owner/endpoint types are not in the lens's
// referenced-label set is acked without a re-execute (a meta-DDL install
// burst must not trigger whole-bucket scans on every read-model lens), and
// the projected state is untouched.
func TestPlainLens_IrrelevantTypeSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, _, targetKV := newRetractionPipeline(t, listedUnitsSpec, []string{"key"})
	require.False(t, p.plainReprojectAll, "listedUnitsSpec has an exhaustive label set")
	require.True(t, p.plainReactsTo("unit"))
	require.False(t, p.plainReactsTo("meta"))

	const unitID = "FRunitDDDDDDDDDDDDDD"
	unitKey := "vtx.unit." + unitID
	unitBody := putBody(t, coreKV, unitKey, map[string]any{
		"key": unitKey, "class": "unit", "isDeleted": false,
		"createdAt": "2026-07-02T10:00:00Z", "lastModifiedAt": "2026-07-02T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, unitKey+".listing", aspectBody(unitKey, "listing", map[string]any{"status": "active"}, false))
	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + unitKey, Body: unitBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	entry, err := targetKV.Get(ctx, unitKey)
	require.NoError(t, err)
	before := entry.Revision

	// A meta-vertex aspect event (the DDL-install shape) and a meta-meta
	// link event: both ack with no target write.
	metaAspect := putBody(t, coreKV, "vtx.meta.FRmetaAAAAAAAAAAAAAA.adapter",
		aspectBody("vtx.meta.FRmetaAAAAAAAAAAAAAA", "adapter", map[string]any{"value": "nats_kv"}, false))
	dec, err = p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE.vtx.meta.FRmetaAAAAAAAAAAAAAA.adapter", Body: metaAspect, Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	dec, err = p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE.lnk.meta.FRmetaAAAAAAAAAAAAAA.governs.meta.FRmetaBBBBBBBBBBBBBB",
		Body:    []byte(`{"isDeleted":false}`), Sequence: 3})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	entry, err = targetKV.Get(ctx, unitKey)
	require.NoError(t, err)
	require.Equal(t, before, entry.Revision, "an irrelevant-type event must not rewrite the target")
}

// TestReferencedLabels_Contract pins the exhaustiveness rules of the
// label-set extraction the relevance skip depends on.
func TestReferencedLabels_Contract(t *testing.T) {
	eng := full.New()
	parseFull := func(spec string) *full.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.True(t, isFull)
		return fullCR
	}

	labels, exhaustive := parseFull(landlordShapeSpec).ReferencedLabels()
	require.True(t, exhaustive)
	require.Equal(t, map[string]struct{}{
		"leaseapp": {}, "unit": {}, "identity": {},
	}, labels)

	_, exhaustive = parseFull(`MATCH (a:unit)-[:x*1..3]->(b:unit) RETURN a.key AS key`).ReferencedLabels()
	require.False(t, exhaustive, "a variable-length relationship binds arbitrary intermediate types")

	_, exhaustive = parseFull(`MATCH (a)-[:x]->(b:unit) RETURN b.key AS key`).ReferencedLabels()
	require.False(t, exhaustive, "an unlabeled node pattern binds any type")

	labels, exhaustive = parseFull(
		`MATCH (svc:service) WHERE NOT (svc)-[:instanceOf]->(tpl:svctemplate) RETURN svc.key AS key`).ReferencedLabels()
	require.True(t, exhaustive)
	require.Contains(t, labels, "svctemplate", "labels inside WHERE pattern expressions count")
}

// TestAnchorProjectionKey_Contract table-tests the shared read-free key
// derivation directly: ok iff the event vertex is the anchor label and every
// key column resolves read-free from the anchor to a scalar.
func TestAnchorProjectionKey_Contract(t *testing.T) {
	eng := full.New()

	parse := func(spec string, cols []string) ruleengine.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		if len(cols) > 0 {
			fullCR, isFull := cr.(*full.CompiledRule)
			require.True(t, isFull)
			fullCR.KeyColumns = cols
			require.NoError(t, fullCR.ValidateKeyColumns())
		}
		return cr
	}

	anchorProps := map[string]any{"lastModifiedAt": "2026-07-02T10:00:00Z"}

	t.Run("anchor-keyed single column resolves", func(t *testing.T) {
		cr := parse(listedUnitsSpec, []string{"key"})
		keys, ok := eng.AnchorProjectionKey(cr, "vtx.unit.U1aaaaaaaaaaaaaaaaa", "unit", anchorProps)
		require.True(t, ok)
		require.Equal(t, map[string]any{"key": "vtx.unit.U1aaaaaaaaaaaaaaaaa"}, keys)
	})

	t.Run("wrong event type falls through", func(t *testing.T) {
		cr := parse(listedUnitsSpec, []string{"key"})
		_, ok := eng.AnchorProjectionKey(cr, "vtx.identity.I1aaaaaaaaaaaaaaaaa", "identity", anchorProps)
		require.False(t, ok, "a non-anchor event type must never derive a retraction key")
	})

	t.Run("neighbor-bound composite key falls through", func(t *testing.T) {
		cr := parse(landlordShapeSpec, []string{"app_id", "landlord_id"})
		_, ok := eng.AnchorProjectionKey(cr, "vtx.leaseapp.L1aaaaaaaaaaaaaaaaaa", "leaseapp", anchorProps)
		require.False(t, ok, "a key column bound to a non-anchor variable must be underivable")
	})

	t.Run("aspect-dependent key column falls through", func(t *testing.T) {
		cr := parse(`MATCH (u:unit) RETURN u.listing.data.status AS key`, []string{"key"})
		_, ok := eng.AnchorProjectionKey(cr, "vtx.unit.U1aaaaaaaaaaaaaaaaa", "unit", anchorProps)
		require.False(t, ok, "a key needing a Core-KV aspect read must be underivable read-free")
	})

	t.Run("nil-valued key column falls through", func(t *testing.T) {
		// data.slug is absent from the anchor's root body → evalExpr resolves
		// nil without error; a nil key value must never become a Delete
		// predicate (its rendering is adapter-dependent).
		cr := parse(`MATCH (u:unit) RETURN u.data.slug AS key`, []string{"key"})
		_, ok := eng.AnchorProjectionKey(cr, "vtx.unit.U1aaaaaaaaaaaaaaaaa", "unit",
			map[string]any{"lastModifiedAt": "2026-07-02T10:00:00Z", "data": map[string]any{}})
		require.False(t, ok, "a nil key value must be underivable")
	})
}
