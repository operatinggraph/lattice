package pipeline

// Event-seeded delta evaluation on a plain lens
// (refractor-footprint-reduction-design.md §D2 Phase 1): an event on the
// lens's ANCHOR type re-derives only that anchor's rows, so a sibling anchor's
// projected row is not merely rewritten with identical bytes — it is never
// read, never written, and its target revision does not move. Every other
// event (a neighbor-type vertex, an aspect or link endpoint of a referenced
// non-anchor type) keeps today's whole-row-set recompute, and every lens the
// eligibility predicate excludes behaves exactly as it did.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedUnitsSpec is the unanchored whole-type-scan business shape: anchor
// `unit`, one row per anchor, a root-body column (name) and an aspect-derived
// one (status) so a vertex event's refresh is observable.
const seedUnitsSpec = `
MATCH (u:unit)
WHERE u.listing.data.status <> null
RETURN u.key AS key, u.name AS name, u.listing.data.status AS status
`

// seedServicesSpec walks from its anchor to a referenced neighbor type, so a
// neighbor (identity) event exercises the unseeded full-recompute path.
const seedServicesSpec = `
MATCH (svc:service)
MATCH (svc)-[:providedTo]->(id:identity)
RETURN svc.key AS key, id.status AS holderStatus
`

// seedMultiRowSpec produces one row per (anchor, neighbor) pair — several rows
// for one anchor — keyed by both, the shape §D2 must not collapse: seeding
// constrains the anchor BINDING, not pattern expansion.
const seedMultiRowSpec = `
MATCH (svc:service)
MATCH (svc)-[:providedTo]->(id:identity)
RETURN nanoIdFromKey(svc.key) AS svc_id, nanoIdFromKey(id.key) AS holder_id
`

// seedVertexBody writes a Contract #1 vertex root body and returns the raw
// bytes to drive its CDC event with.
func seedVertexBody(t *testing.T, coreKV *substrate.KV, key, class string, extra map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"key": key, "class": class, "isDeleted": false,
		"createdAt": "2026-08-01T10:00:00Z", "lastModifiedAt": "2026-08-01T10:00:00Z",
		"data": map[string]any{},
	}
	for k, v := range extra {
		body[k] = v
	}
	return putBody(t, coreKV, key, body)
}

// handleVertexEvent drives one vertex-root CDC event through the pipeline.
func handleVertexEvent(t *testing.T, p *Pipeline, key string, body []byte, seq uint64) {
	t.Helper()
	dec, err := p.handle(context.Background(), substrate.Message{
		Subject: "$KV.CORE." + key, Body: body, Sequence: seq,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
}

// targetRevision returns the current revision of a projected row, failing the
// test if the row is absent. A revision is the observable proof that a row was
// (or was not) written: an identical-value rewrite still moves it.
func targetRevision(t *testing.T, targetKV *substrate.KV, key string) uint64 {
	t.Helper()
	entry, err := targetKV.Get(context.Background(), key)
	require.NoError(t, err)
	return entry.Revision
}

func targetRow(t *testing.T, targetKV *substrate.KV, key string) map[string]any {
	t.Helper()
	entry, err := targetKV.Get(context.Background(), key)
	require.NoError(t, err)
	var row map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &row))
	return row
}

// TestSeededAnchorEvent_LeavesSiblingRowsCompletelyUntouched is §D2's headline
// claim (a): an anchor-labeled event writes only that anchor's row. The
// sibling assertion is deliberately the strongest available form — an
// unchanged target REVISION, which no rewrite of any value can satisfy —
// because the whole point of Phase 1 is removing the redundant EVALUATION, not
// just the redundant Put. Disable the seeding arm (make seedAnchorFor return
// "") and this assertion fails: the unseeded recompute re-derives and rewrites
// every sibling row on every event.
func TestSeededAnchorEvent_LeavesSiblingRowsCompletelyUntouched(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	p, coreKV, _, targetKV := newRetractionPipeline(t, seedUnitsSpec, []string{"key"})

	const unitAID = "SEEDunitAAAAAAAAAAAA"
	const unitBID = "SEEDunitBBBBBBBBBBBB"
	unitAKey := "vtx.unit." + unitAID
	unitBKey := "vtx.unit." + unitBID

	unitABody := seedVertexBody(t, coreKV, unitAKey, "unit", map[string]any{"name": "Loft A"})
	putBody(t, coreKV, unitAKey+".listing", aspectBody(unitAKey, "listing", map[string]any{"status": "active"}, false))
	unitBBody := seedVertexBody(t, coreKV, unitBKey, "unit", map[string]any{"name": "Loft B"})
	putBody(t, coreKV, unitBKey+".listing", aspectBody(unitBKey, "listing", map[string]any{"status": "active"}, false))

	// Each anchor projects from its OWN vertex event — the per-key CDC delivery
	// (including DeliverLastPerSubject replay) that per-anchor recompute
	// composes with.
	handleVertexEvent(t, p, unitAKey, unitABody, 1)
	handleVertexEvent(t, p, unitBKey, unitBBody, 2)
	require.Equal(t, "Loft A", targetRow(t, targetKV, unitAKey)["name"])
	require.Equal(t, "Loft B", targetRow(t, targetKV, unitBKey)["name"])

	revBBefore := targetRevision(t, targetKV, unitBKey)

	// A real mutation of anchor A: its row must refresh...
	unitABody2 := seedVertexBody(t, coreKV, unitAKey, "unit", map[string]any{"name": "Loft A (renamed)"})
	handleVertexEvent(t, p, unitAKey, unitABody2, 3)
	require.Equal(t, "Loft A (renamed)", targetRow(t, targetKV, unitAKey)["name"],
		"the event anchor's own row must still refresh")

	// ...and anchor B's row must be untouched: not re-evaluated, not re-read,
	// not re-written.
	require.Equal(t, revBBefore, targetRevision(t, targetKV, unitBKey),
		"an anchor-labeled event must not rewrite a sibling anchor's row")
	require.Equal(t, "Loft B", targetRow(t, targetKV, unitBKey)["name"])
}

// TestSeededAspectEvent_RetractsItsAnchorAndSparesSiblings is claim (c) on the
// aspect arm: the owner vertex is the anchor, so the re-execute is seeded; the
// seeded evaluation yields zero rows for the flipped anchor and the
// filter-retraction presence check (AnchorProjectionKey + resultsContainKeys —
// already scoped to the event anchor) still emits its Delete. The sibling's
// revision proves the retraction cost nothing elsewhere.
func TestSeededAspectEvent_RetractsItsAnchorAndSparesSiblings(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, _, targetKV := newRetractionPipeline(t, seedUnitsSpec, []string{"key"})

	const unitAID = "SEEDwhereAAAAAAAAAAA"
	const unitBID = "SEEDwhereBBBBBBBBBBB"
	unitAKey := "vtx.unit." + unitAID
	unitBKey := "vtx.unit." + unitBID

	unitABody := seedVertexBody(t, coreKV, unitAKey, "unit", map[string]any{"name": "Loft A"})
	putBody(t, coreKV, unitAKey+".listing", aspectBody(unitAKey, "listing", map[string]any{"status": "active"}, false))
	unitBBody := seedVertexBody(t, coreKV, unitBKey, "unit", map[string]any{"name": "Loft B"})
	putBody(t, coreKV, unitBKey+".listing", aspectBody(unitBKey, "listing", map[string]any{"status": "active"}, false))
	handleVertexEvent(t, p, unitAKey, unitABody, 1)
	handleVertexEvent(t, p, unitBKey, unitBBody, 2)
	revBBefore := targetRevision(t, targetKV, unitBKey)

	// Soft-tombstone A's predicate aspect: the WHERE flips false.
	gone := putBody(t, coreKV, unitAKey+".listing",
		aspectBody(unitAKey, "listing", map[string]any{"status": "active"}, true))
	dec, err := p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + unitAKey + ".listing", Body: gone, Sequence: 3,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	_, err = targetKV.Get(ctx, unitAKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"a WHERE flip on a seeded anchor must still retract its row")
	require.Equal(t, revBBefore, targetRevision(t, targetKV, unitBKey),
		"retracting one anchor must not rewrite a sibling's row")
}

// TestNeighborVertexEvent_StillRecomputesEveryAnchor is claim (b): a
// referenced NON-anchor type's event says nothing about which anchors it
// affects (deriving that is §D2 Phase 2), so it keeps the full recompute and
// dependent rows still refresh promptly.
func TestNeighborVertexEvent_StillRecomputesEveryAnchor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, seedServicesSpec, []string{"key"})

	const svc1ID = "SEEDsvcAAAAAAAAAAAAA"
	const svc2ID = "SEEDsvcBBBBBBBBBBBBB"
	const aliceID = "SEEDidAAAAAAAAAAAAAA"
	const bobID = "SEEDidBBBBBBBBBBBBBB"
	svc1Key := "vtx.service." + svc1ID
	svc2Key := "vtx.service." + svc2ID
	aliceKey := "vtx.identity." + aliceID

	svc1Body := seedVertexBody(t, coreKV, svc1Key, "service", nil)
	svc2Body := seedVertexBody(t, coreKV, svc2Key, "service", nil)
	seedVertexBody(t, coreKV, aliceKey, "identity", map[string]any{"status": "onCall"})
	seedVertexBody(t, coreKV, "vtx.identity."+bobID, "identity", map[string]any{"status": "onCall"})
	buildCollisionEdge(t, adjKV, "providedTo", "service", svc1ID, "identity", aliceID)
	buildCollisionEdge(t, adjKV, "providedTo", "service", svc2ID, "identity", bobID)

	handleVertexEvent(t, p, svc1Key, svc1Body, 1)
	handleVertexEvent(t, p, svc2Key, svc2Body, 2)
	require.Equal(t, "onCall", targetRow(t, targetKV, svc1Key)["holderStatus"])

	// The neighbor changes. Its own vertex event is the only signal the lens
	// gets, and the dependent row must refresh from it.
	aliceBody := seedVertexBody(t, coreKV, aliceKey, "identity", map[string]any{"status": "away"})
	handleVertexEvent(t, p, aliceKey, aliceBody, 3)
	require.Equal(t, "away", targetRow(t, targetKV, svc1Key)["holderStatus"],
		"a neighbor-type event must still refresh the rows that depend on it")
}

// TestSeededLinkEndpoint_RetractsAnchorWhileNeighborEndpointRecomputes covers
// the link arm's asymmetry. A link tombstone reprojects from BOTH endpoints:
// the anchor endpoint is seeded (its seeded evaluation yields zero rows, and
// the presence check retracts its row) while the referenced non-anchor
// endpoint keeps the full recompute.
//
// Both endpoint evaluations must run. The full recompute from the neighbor
// endpoint refreshes the sibling anchors' rows, but it can never emit the
// event anchor's Delete: the presence check derives its key from the entry
// being evaluated, so only the anchor endpoint's own evaluation retracts it.
// Skipping the seeded endpoint as "subsumed" by the full one would silently
// drop that retraction.
func TestSeededLinkEndpoint_RetractsAnchorWhileNeighborEndpointRecomputes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, seedServicesSpec, []string{"key"})

	const svc1ID = "SEEDLnkSvcAAAAAAAAAA"
	const svc2ID = "SEEDLnkSvcBBBBBBBBBB"
	const aliceID = "SEEDLnkPersonAAAAAAA"
	const bobID = "SEEDLnkPersonBBBBBBB"
	svc1Key := "vtx.service." + svc1ID
	svc2Key := "vtx.service." + svc2ID

	svc1Body := seedVertexBody(t, coreKV, svc1Key, "service", nil)
	svc2Body := seedVertexBody(t, coreKV, svc2Key, "service", nil)
	seedVertexBody(t, coreKV, "vtx.identity."+aliceID, "identity", map[string]any{"status": "onCall"})
	seedVertexBody(t, coreKV, "vtx.identity."+bobID, "identity", map[string]any{"status": "onCall"})

	// The dedicated adjacency consumer's own shape: the link key is the EdgeID,
	// so the pipeline's tombstone Build removes exactly this entry.
	linkKey := "lnk.service." + svc1ID + ".providedTo.identity." + aliceID
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: "providedTo", Direction: "outbound",
			NodeID: svc1ID, OtherNodeID: aliceID, OtherType: "identity"},
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: "providedTo", Direction: "inbound",
			NodeID: aliceID, OtherNodeID: svc1ID, OtherType: "service"},
	} {
		require.NoError(t, adjacency.Build(ctx, adjKV, evt))
	}
	buildCollisionEdge(t, adjKV, "providedTo", "service", svc2ID, "identity", bobID)

	handleVertexEvent(t, p, svc1Key, svc1Body, 1)
	handleVertexEvent(t, p, svc2Key, svc2Body, 2)
	_, err := targetKV.Get(ctx, svc1Key)
	require.NoError(t, err, "both anchors project before the link is removed")

	linkTombstone, err := json.Marshal(map[string]any{"key": linkKey, "isDeleted": true})
	require.NoError(t, err)
	dec, err := p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + linkKey, Body: linkTombstone, Sequence: 3,
	})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)

	_, err = targetKV.Get(ctx, svc1Key)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound,
		"the seeded anchor endpoint must still emit its required-link-removal Delete")
	require.Equal(t, "onCall", targetRow(t, targetKV, svc2Key)["holderStatus"],
		"the sibling anchor's row survives the other anchor's retraction")
}

// TestSeededAnchorEvent_MultiRowLensEmitsItsWholePerAnchorSet is claim (e):
// seeding constrains the anchor binding, not the walk, so a lens projecting
// several rows per anchor still emits all of them — and still touches no
// sibling anchor's row.
func TestSeededAnchorEvent_MultiRowLensEmitsItsWholePerAnchorSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, seedMultiRowSpec, []string{"svc_id", "holder_id"})

	const svc1ID = "SEEDMuxSvcAAAAAAAAAA"
	const svc2ID = "SEEDMuxSvcBBBBBBBBBB"
	const aliceID = "SEEDMuxPersonAAAAAAA"
	const bobID = "SEEDMuxPersonBBBBBBB"
	const carolID = "SEEDMuxPersonCCCCCCC"
	svc1Key := "vtx.service." + svc1ID
	svc2Key := "vtx.service." + svc2ID

	svc1Body := seedVertexBody(t, coreKV, svc1Key, "service", nil)
	svc2Body := seedVertexBody(t, coreKV, svc2Key, "service", nil)
	for _, id := range []string{aliceID, bobID, carolID} {
		seedVertexBody(t, coreKV, "vtx.identity."+id, "identity", nil)
	}
	buildCollisionEdge(t, adjKV, "providedTo", "service", svc1ID, "identity", aliceID)
	buildCollisionEdge(t, adjKV, "providedTo", "service", svc1ID, "identity", bobID)
	buildCollisionEdge(t, adjKV, "providedTo", "service", svc2ID, "identity", carolID)

	handleVertexEvent(t, p, svc1Key, svc1Body, 1)
	handleVertexEvent(t, p, svc2Key, svc2Body, 2)
	siblingKey := svc2ID + "." + carolID
	revSiblingBefore := targetRevision(t, targetKV, siblingKey)

	// Grow the anchor's expansion between events: a fourth holder joins svc1.
	// The re-fired anchor event's seeded evaluation must surface the NEW
	// (svc1, dave) pair — the discriminating assertion: seeding constrains the
	// anchor BINDING, never pattern expansion, so the whole per-anchor set is
	// re-derived. The pre-existing (svc1, alice/bob) rows re-derive to
	// byte-identical content, which the unguarded adapter skips rather than
	// rewrites, so row EXISTENCE — not revision movement — is what a full
	// expansion proves here.
	const daveID = "SEEDMuxPersonDDDDDDD"
	seedVertexBody(t, coreKV, "vtx.identity."+daveID, "identity", nil)
	buildCollisionEdge(t, adjKV, "providedTo", "service", svc1ID, "identity", daveID)

	handleVertexEvent(t, p, svc1Key, svc1Body, 3)
	for _, holder := range []string{aliceID, bobID, daveID} {
		require.Positivef(t, targetRevision(t, targetKV, svc1ID+"."+holder),
			"a seeded anchor must emit its full per-anchor row set (holder %s missing)", holder)
	}
	// ...and the other anchor's row is untouched.
	require.Equal(t, revSiblingBefore, targetRevision(t, targetKV, siblingKey))
}

// TestDiffRetractionLens_IsNeverSeeded is claim (d). A DiffRetraction lens
// retracts by comparing the target's FULL live key set against the
// evaluation's row set, so a single-anchor row set would read as "every other
// anchor's rows are gone". The eligibility predicate excludes it, and this
// proves the exclusion at the behavioural level: an anchor-labeled event on a
// DiffRetraction lens must leave every sibling row alive.
func TestDiffRetractionLens_IsNeverSeeded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, landlordShapeSpec, []string{"app_id", "landlord_id"})
	require.NoError(t, p.SetDiffRetraction(true))

	const app1ID = "SEEDdiffAppAAAAAAAAA"
	const app2ID = "SEEDdiffAppBBBBBBBBB"
	const unit1ID = "SEEDdiffUnitAAAAAAAA"
	const unit2ID = "SEEDdiffUnitBBBBBBBB"
	const llID = "SEEDdiffLordAAAAAAAA"
	app1Key := "vtx.leaseapp." + app1ID
	app2Key := "vtx.leaseapp." + app2ID

	app1Body := seedVertexBody(t, coreKV, app1Key, "leaseapp", nil)
	app2Body := seedVertexBody(t, coreKV, app2Key, "leaseapp", nil)
	seedVertexBody(t, coreKV, "vtx.unit."+unit1ID, "unit", nil)
	seedVertexBody(t, coreKV, "vtx.unit."+unit2ID, "unit", nil)
	seedVertexBody(t, coreKV, "vtx.identity."+llID, "identity", nil)
	buildCollisionEdge(t, adjKV, "appliesToUnit", "leaseapp", app1ID, "unit", unit1ID)
	buildCollisionEdge(t, adjKV, "appliesToUnit", "leaseapp", app2ID, "unit", unit2ID)
	buildCollisionEdge(t, adjKV, "manages", "identity", llID, "unit", unit1ID)
	buildCollisionEdge(t, adjKV, "manages", "identity", llID, "unit", unit2ID)

	handleVertexEvent(t, p, app1Key, app1Body, 1)
	handleVertexEvent(t, p, app2Key, app2Body, 2)
	for _, appID := range []string{app1ID, app2ID} {
		_, err := targetKV.Get(ctx, appID+"."+llID)
		require.NoErrorf(t, err, "both leaseapp rows project (%s)", appID)
	}

	// An anchor-labeled event. Its evaluation must still derive the COMPLETE
	// row set, or the diff would retract every row it failed to re-derive.
	handleVertexEvent(t, p, app1Key, app1Body, 3)
	for _, appID := range []string{app1ID, app2ID} {
		_, err := targetKV.Get(ctx, appID+"."+llID)
		require.NoErrorf(t, err,
			"a DiffRetraction lens must never be seeded — sibling row %s was retracted", appID)
	}
}

// TestSeedAnchorFor_EligibilityConjuncts pins the pipeline half of §D2's
// eligibility as a unit, including the two negative cases whose behavioural
// proof would otherwise require a whole live lens: an actor-aware pipeline
// (whose evaluations are already per-actor and whose anchor is the actor, not
// the event vertex) and an enveloped one.
//
// It is also the structural assertion that applyDiffRetraction is unreachable
// from a seeded evaluation: applyDiffRetraction runs only under
// p.diffRetraction, and seedAnchorFor returns "" for exactly that pipeline.
func TestSeedAnchorFor_EligibilityConjuncts(t *testing.T) {
	eng := full.New()
	compile := func(t *testing.T, spec string) ruleengine.CompiledRule {
		t.Helper()
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		return cr
	}
	newPlain := func(t *testing.T, spec string) *Pipeline {
		t.Helper()
		p, err := New("seed-eligibility", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		p.UseFullEngine(eng, compile(t, spec))
		return p
	}

	const anchorKey = "vtx.unit.SEEDpredicateAAAAAAA"

	t.Run("plain single-branch anchor-labeled event seeds", func(t *testing.T) {
		p := newPlain(t, seedUnitsSpec)
		require.Equal(t, anchorKey, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())))
	})

	t.Run("neighbor-labeled event does not seed", func(t *testing.T) {
		p := newPlain(t, seedUnitsSpec)
		require.Empty(t, p.seedAnchorFor(p.ruleState(), "identity", "vtx.identity.SEEDpredicateBBBBBBB", p.partitionArmed(p.ruleState())))
	})

	t.Run("unlabeled anchor disarms seeding", func(t *testing.T) {
		p := newPlain(t, `MATCH (u) RETURN u.key AS key`)
		require.Empty(t, p.seedAnchorLabels)
		require.Empty(t, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())))
	})

	t.Run("DiffRetraction disarms seeding", func(t *testing.T) {
		p := newPlain(t, seedUnitsSpec)
		require.NoError(t, p.SetDiffRetraction(true))
		require.Empty(t, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())),
			"a DiffRetraction lens must recompute its whole row set — its diff retracts everything it fails to re-derive")
	})

	t.Run("ActorEnumerator disarms seeding", func(t *testing.T) {
		p := newPlain(t, seedUnitsSpec)
		p.SetActorEnumerator(NewActorEnumerator(nil, nil, "identity"))
		require.Empty(t, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())))
	})

	t.Run("envelope disarms seeding", func(t *testing.T) {
		p := newPlain(t, seedUnitsSpec)
		p.SetEnvelopeFn(func(row map[string]any, keys map[string]any, _ map[string]any) (map[string]any, map[string]any, error) {
			return row, keys, nil
		})
		require.Empty(t, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())))
	})

	t.Run("multi-envelope disarms seeding", func(t *testing.T) {
		p := newPlain(t, seedUnitsSpec)
		p.SetMultiEnvelopeFn(func(map[string]any, map[string]any, map[string]any) ([]Envelope, error) {
			return nil, nil
		})
		require.Empty(t, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())))
	})

	t.Run("multi-walk branches disarm seeding", func(t *testing.T) {
		p, err := New("seed-eligibility-multiwalk", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		branches := []ruleengine.CompiledRule{compile(t, seedUnitsSpec), compile(t, seedUnitsSpec)}
		p.UseFullEngineBranches(eng, branches[0], branches)
		require.Empty(t, p.seedAnchorLabels,
			"branch merging evaluates N queries; one seed cannot speak for all their anchors")
		require.Empty(t, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())))
	})

	t.Run("a reload back to a single walk re-arms seeding", func(t *testing.T) {
		p, err := New("seed-eligibility-reload", "nats_kv", "CORE", nil, nil, &keyListerAdapter{}, nil)
		require.NoError(t, err)
		branches := []ruleengine.CompiledRule{compile(t, seedUnitsSpec), compile(t, seedUnitsSpec)}
		p.UseFullEngineBranches(eng, branches[0], branches)
		p.UseFullEngine(eng, compile(t, seedUnitsSpec))
		require.Equal(t, anchorKey, p.seedAnchorFor(p.ruleState(), "unit", anchorKey, p.partitionArmed(p.ruleState())))
	})
}

// keyListerAdapter is an inert target that can enumerate its (empty) key set —
// enough for SetDiffRetraction's adapter capability check in the
// eligibility unit above, which never writes or reads a row.
type keyListerAdapter struct{}

func (*keyListerAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	return nil
}
func (*keyListerAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (*keyListerAdapter) Probe(context.Context) error                          { return nil }
func (*keyListerAdapter) Close() error                                         { return nil }
func (*keyListerAdapter) ListKeys(context.Context) ([]map[string]any, error)   { return nil, nil }

var _ adapter.KeyLister = (*keyListerAdapter)(nil)
