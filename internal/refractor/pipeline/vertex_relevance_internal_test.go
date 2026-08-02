package pipeline

// KindVertex relevance-gate coverage: a plain (non-actor-aware) lens whose
// compiled patterns provably cannot bind a vertex-root event's type skips the
// re-execute outright (plainVertexRelevant), mirroring the aspect/link arms'
// existing plainReactsTo skip but for handle's KindVertex path, which has no
// per-type dispatch of its own.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// vrgID pads base into a valid Contract #1 20-char NanoID (VRG = "vertex
// relevance gate", keeping these fixtures visually distinct from other
// suites sharing the same embedded NATS alphabet rules). Padding
// programmatically avoids a hand-counted literal silently landing on the
// wrong length or on one of the alphabet's excluded I/l/O/0 characters.
func vrgID(t *testing.T, base string) string {
	t.Helper()
	id := "VRG" + base
	require.LessOrEqual(t, len(id), 20, "base %q too long for a 20-char NanoID", base)
	for len(id) < 20 {
		id += "A"
	}
	require.Len(t, id, 20)
	return id
}

// TestPlainVertexRelevant_Table pins plainVertexRelevant's contract directly,
// independent of any real compiled rule. The default MUST flip relative to
// plainReactsTo's: plainReactsTo's false case only skips a SPECIAL
// reprojection arm (harmless — the aspect/link event has no other pending
// write), whereas this gate's false case drops a vertex-root CDC event with
// no fallback, so every uncertain case here must default to relevant
// (evaluate), never to skip.
func TestPlainVertexRelevant_Table(t *testing.T) {
	unitOnly := map[string]struct{}{"unit": {}}

	cases := []struct {
		name       string
		engineKind string
		all        bool
		labels     map[string]struct{}
		vertexType string
		want       bool
	}{
		{
			name: "non-full engine falls through regardless of labels",
			want: true, vertexType: "meta", labels: unitOnly,
		},
		{
			name: "non-full engine falls through even when labels would exclude the type",
			want: true, vertexType: "unit", labels: unitOnly,
			// engineKind left zero-value ("") — deliberately not EngineFull.
		},
		{
			name: "exhaustive set excludes the type", engineKind: ruleengine.EngineFull,
			labels: unitOnly, vertexType: "meta", want: false,
		},
		{
			name: "exhaustive set includes the type", engineKind: ruleengine.EngineFull,
			labels: unitOnly, vertexType: "unit", want: true,
		},
		{
			name: "plainReprojectAll evaluates every type", engineKind: ruleengine.EngineFull,
			all: true, vertexType: "meta", want: true,
		},
		{
			name: "empty vertex type falls through defensively", engineKind: ruleengine.EngineFull,
			labels: unitOnly, vertexType: "", want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pipeline{
				engineKind:           tc.engineKind,
				plainReprojectAll:    tc.all,
				plainReprojectLabels: tc.labels,
			}
			require.Equal(t, tc.want, p.plainVertexRelevant(tc.vertexType))
		})
	}
}

// TestHandle_VertexEvent_IrrelevantTypeSkipped proves the KindVertex gate
// itself: a vertex event of a type neither the anchor nor any referenced
// neighbor of servicedIdentitiesSpec (service, identity) is Ack'd with NO
// evaluation and NO adapter write. A matching (service, identity) pair is
// seeded first so an ungated evaluate would have a real row to
// (re-)project — asserting "zero adapter calls" against an empty graph would
// pass vacuously even with the gate missing entirely.
func TestHandle_VertexEvent_IrrelevantTypeSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	coreKV, adjKV, _ := newCollisionKVs(t)

	eng := full.New()
	cr, err := eng.Parse(servicedIdentitiesSpec)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"key"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	adpt := &recordingAdapter{}
	p, err := New("vertex-relevance-skip", "recording", "CORE", adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	require.False(t, p.plainReprojectAll, "servicedIdentitiesSpec has an exhaustive label set")
	require.False(t, p.plainVertexRelevant("unit"), "unit is neither the anchor nor a referenced neighbor")

	svcID := vrgID(t, "svcA")
	idID := vrgID(t, "idnA")
	svcKey := "vtx.service." + svcID
	idKey := "vtx.identity." + idID
	writeCollisionVertex(t, coreKV, svcKey, "service", map[string]any{})
	writeCollisionVertex(t, coreKV, idKey, "identity", map[string]any{})
	buildCollisionEdge(t, adjKV, "providedTo", "service", svcID, "identity", idID)

	unitID := vrgID(t, "unitA")
	unitKey := "vtx.unit." + unitID
	unitBody, merr := json.Marshal(map[string]any{
		"key": unitKey, "class": "unit", "isDeleted": false,
		"createdAt": "2026-07-31T10:00:00Z", "lastModifiedAt": "2026-07-31T10:00:00Z",
		"data": map[string]any{},
	})
	require.NoError(t, merr)

	dec, herr := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + unitKey, Body: unitBody, Sequence: 1})
	require.NoError(t, herr)
	require.Equal(t, substrate.Ack, dec)
	require.Empty(t, adpt.upserts, "an irrelevant-type vertex event must never reach evaluate/write")
	require.Empty(t, adpt.deletes)
}

// TestHandle_VertexEvent_AnchorTypeEvaluated proves the gate always lets the
// anchor label through: AnchorProjectionKey/AnchorDeleteResult require
// eventType == anchorLabel, so the anchor label is always in ReferencedLabels
// by construction (it carries an explicit label) — but this pins it as a
// behavior, not an inference.
func TestHandle_VertexEvent_AnchorTypeEvaluated(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, servicedIdentitiesSpec, []string{"key"})
	require.True(t, p.plainVertexRelevant("service"), "the anchor label must always be relevant")

	svcID := vrgID(t, "svcB")
	idID := vrgID(t, "idnB")
	svcKey := "vtx.service." + svcID
	idKey := "vtx.identity." + idID
	svcBody := putBody(t, coreKV, svcKey, map[string]any{
		"key": svcKey, "class": "service", "isDeleted": false,
		"createdAt": "2026-07-31T10:00:00Z", "lastModifiedAt": "2026-07-31T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, idKey, map[string]any{
		"key": idKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-07-31T10:00:00Z", "lastModifiedAt": "2026-07-31T10:00:00Z",
		"data": map[string]any{},
	})
	buildCollisionEdge(t, adjKV, "providedTo", "service", svcID, "identity", idID)

	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + svcKey, Body: svcBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	_, err = targetKV.Get(ctx, svcKey)
	require.NoError(t, err, "an anchor-type vertex event must evaluate and project the row")
}

// TestHandle_VertexEvent_ReferencedNonAnchorTypeEvaluated proves the gate
// consults the lens's WHOLE referenced-label set, not just the anchor: an
// "identity" vertex event (referenced via the required providedTo link, but
// not the anchor) must still trigger a re-execute. The service row is never
// created by its own event here — only the identity's — so the only way it
// can appear in the target is if the identity event evaluated the
// (unanchored w.r.t. trigger identity) whole-scan.
func TestHandle_VertexEvent_ReferencedNonAnchorTypeEvaluated(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, servicedIdentitiesSpec, []string{"key"})
	require.True(t, p.plainVertexRelevant("identity"), "identity is referenced by the required link")

	svcID := vrgID(t, "svcC")
	idID := vrgID(t, "idnC")
	svcKey := "vtx.service." + svcID
	idKey := "vtx.identity." + idID
	putBody(t, coreKV, svcKey, map[string]any{
		"key": svcKey, "class": "service", "isDeleted": false,
		"createdAt": "2026-07-31T10:00:00Z", "lastModifiedAt": "2026-07-31T10:00:00Z",
		"data": map[string]any{},
	})
	idBody := putBody(t, coreKV, idKey, map[string]any{
		"key": idKey, "class": "identity", "isDeleted": false,
		"createdAt": "2026-07-31T10:00:00Z", "lastModifiedAt": "2026-07-31T10:00:00Z",
		"data": map[string]any{},
	})
	buildCollisionEdge(t, adjKV, "providedTo", "service", svcID, "identity", idID)

	_, err := targetKV.Get(ctx, svcKey)
	require.ErrorIs(t, err, substrate.ErrKeyNotFound, "the service row must not already be live")

	// Only the identity's OWN vertex-root event is dispatched — the lens
	// never sees a "service" event at all.
	dec, err := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + idKey, Body: idBody, Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	_, err = targetKV.Get(ctx, svcKey)
	require.NoError(t, err, "a referenced-but-non-anchor-type vertex event must still evaluate")
}

// TestHandle_VertexEvent_NonExhaustiveLabelSetEvaluatesEverything proves the
// plainReprojectAll fallback: a lens whose referenced-label set is not
// exhaustive (an unlabeled node pattern, mirroring
// TestReferencedLabels_Contract's own example) must evaluate on ANY vertex
// type — even a type with no textual relationship to the lens at all.
func TestHandle_VertexEvent_NonExhaustiveLabelSetEvaluatesEverything(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	const unownedUnitSpec = `
MATCH (owner)-[:owns]->(u:unit)
RETURN u.key AS key
`
	ctx := context.Background()
	p, coreKV, adjKV, targetKV := newRetractionPipeline(t, unownedUnitSpec, []string{"key"})
	require.True(t, p.plainReprojectAll, "an unlabeled node pattern must disable the exhaustive skip")
	require.True(t, p.plainVertexRelevant("anyTypeAtAll"))

	gadgetID := vrgID(t, "gadgetE")
	unitID := vrgID(t, "unitE")
	gadgetKey := "vtx.gadget." + gadgetID
	unitKey := "vtx.unit." + unitID
	gadgetBody := putBody(t, coreKV, gadgetKey, map[string]any{
		"key": gadgetKey, "class": "gadget", "isDeleted": false,
		"createdAt": "2026-07-31T10:00:00Z", "lastModifiedAt": "2026-07-31T10:00:00Z",
		"data": map[string]any{},
	})
	putBody(t, coreKV, unitKey, map[string]any{
		"key": unitKey, "class": "unit", "isDeleted": false,
		"createdAt": "2026-07-31T10:00:00Z", "lastModifiedAt": "2026-07-31T10:00:00Z",
		"data": map[string]any{},
	})
	buildCollisionEdge(t, adjKV, "owns", "gadget", gadgetID, "unit", unitID)

	// The trigger is the "gadget" vertex — a type absent from the query text
	// entirely (the anchor pattern is an unlabeled variable).
	dec, herr := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + gadgetKey, Body: gadgetBody, Sequence: 1})
	require.NoError(t, herr)
	require.Equal(t, substrate.Ack, dec)
	_, err := targetKV.Get(ctx, unitKey)
	require.NoError(t, err, "a non-exhaustive label set must evaluate on every vertex type")
}

// TestHandle_VertexEvent_ActorAwarePipelineIgnoresThePlainGate proves the
// KindVertex arm never judges an actor-aware pipeline by plainVertexRelevant.
// Forcing a referenced-label set that EXCLUDES the triggering type ("role")
// makes the plain gate say "skip", and the fan-out must still reach and project
// the identity actor's row.
//
// The claim is scoped to the PLAIN gate. An actor-aware pipeline has a gate of
// its own (ActorAwareNarrowingLabels, §4.2), and this fixture is ineligible for
// it — it declares no pattern-closed output and installs no sweep plan — so the
// event reaches the fan-out here whichever gate is asked. What the actor-aware
// gate does to an ELIGIBLE pipeline is pinned in
// actor_aware_relevance_internal_test.go.
func TestHandle_VertexEvent_ActorAwarePipelineIgnoresThePlainGate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	coreKV, adjKV, _ := newCollisionKVs(t)

	idID := vrgID(t, "actorD")
	roleID := vrgID(t, "rgD")
	eng, cr, _, _ := evalDriftFixture(t, coreKV, adjKV, idID, roleID, "v0")

	adpt := &recordingAdapter{}
	p := &Pipeline{
		ruleID:       "vertex-relevance-actor-aware",
		coreKVBucket: "CORE",
		coreKV:       coreKV,
		adjKV:        adjKV,
		engineKind:   ruleengine.EngineFull,
		fullEngine:   eng,
		fullCR:       cr,
		envelopeFn:   evalDriftEnvelopeFn,
		// Deliberately excludes "role": if plainVertexRelevant gated this
		// path, this event would be skipped. actorEnumerator below is what
		// must save it — the plain gate never applies to an actor-aware
		// pipeline.
		plainReprojectLabels: map[string]struct{}{"identity": {}},
		plainReprojectAll:    false,
		actorEnumerator:      NewActorEnumerator(adjKV, coreKV, "identity"),
		adpt:                 adpt,
	}
	require.False(t, p.plainVertexRelevant("role"),
		"the gate itself would exclude role — the actor-aware bypass is what must save this event")

	roleKey := "vtx.role." + roleID
	roleBody, merr := json.Marshal(map[string]any{
		"key": roleKey, "class": "role", "isDeleted": false,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": map[string]any{"name": "v1"},
	})
	require.NoError(t, merr)

	dec, herr := p.handle(ctx, substrate.Message{Subject: "$KV.CORE." + roleKey, Body: roleBody, Sequence: 1})
	require.NoError(t, herr)
	require.Equal(t, substrate.Ack, dec)
	require.Len(t, adpt.upserts, 1,
		"an actor-aware pipeline must fan out on a non-actor vertex event regardless of plainVertexRelevant")
	require.Equal(t, "cap.identity."+idID, adpt.upserts[0].keys["key"])
}
