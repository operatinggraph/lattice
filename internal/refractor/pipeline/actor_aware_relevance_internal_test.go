package pipeline

// Actor-aware relevance-gate coverage: the aspect / link / vertex fan-out arms
// skip an event whose vertex types the lens's compiled patterns provably cannot
// bind, gated by the §4.2 conjunction
// (auth-plane-projection-latency-design.md).
//
// The conjunct table is the security surface: every conjunct must independently
// force the pipeline back to its unconditional fan-out, because a wrong
// "irrelevant" on the auth plane is a stale grant.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// aargID pads base into a valid Contract #1 20-char NanoID (AARG = "actor-aware
// relevance gate"), mirroring vrgID so these fixtures stay distinct from the
// plain-gate suite's.
func aargID(t *testing.T, base string) string {
	t.Helper()
	id := "AARG" + base
	require.LessOrEqual(t, len(id), 20, "base %q too long for a 20-char NanoID", base)
	for len(id) < 20 {
		id += "A"
	}
	require.Len(t, id, 20)
	// The alphabet excludes I/l/O/0. A base carrying one yields a key
	// ClassifyKey demotes to KindUnknown, which would make a "no write"
	// assertion pass for the wrong reason.
	for _, c := range id {
		require.Contains(t, substrate.Alphabet, string(c),
			"fixture id %q uses a character outside the Contract #1 alphabet", id)
	}
	return id
}

// identityRoleLabels is evalDriftCypher's referenced-label set: the anchor
// (identity) plus the one traversed neighbour type (role).
func identityRoleLabels() map[string]struct{} {
	return map[string]struct{}{"identity": {}, "role": {}}
}

// eligiblePipeline returns a Pipeline satisfying every §4.2 conjunct, so a test
// can knock exactly one out and observe the fallback.
func eligiblePipeline(t *testing.T) *Pipeline {
	t.Helper()
	p := &Pipeline{
		ruleID:               "actor-aware-relevance",
		engineKind:           ruleengine.EngineFull,
		plainReprojectLabels: identityRoleLabels(),
		plainReprojectAll:    false,
		patternClosedOutput:  true,
		actorEnumerator:      NewActorEnumerator(nil, nil, "identity"),
	}
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	labels, ok := p.ActorAwareNarrowingLabels()
	require.True(t, ok, "the fixture itself must be eligible or every negative case is vacuous")
	require.Equal(t, identityRoleLabels(), labels)
	return p
}

// TestActorAwareNarrowingLabels_EveryConjunctFailsClosed knocks out one
// conjunct at a time from an otherwise-eligible pipeline and requires each to
// restore the broad, unconditional fan-out.
func TestActorAwareNarrowingLabels_EveryConjunctFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		knockOut func(p *Pipeline)
	}{
		{
			name:     "not actor-aware — the plain arms own their own gates",
			knockOut: func(p *Pipeline) { p.actorEnumerator = nil },
		},
		{
			name:     "non-full engine has no ReferencedLabels to trust",
			knockOut: func(p *Pipeline) { p.engineKind = "" },
		},
		{
			name:     "non-exhaustive label set — any type may bind",
			knockOut: func(p *Pipeline) { p.plainReprojectAll = true },
		},
		{
			name:     "output is not pattern-closed (a personal lens's read gate / interest set)",
			knockOut: func(p *Pipeline) { p.patternClosedOutput = false },
		},
		{
			name:     "no convergence sweep — narrowing would leave the lens healer-less",
			knockOut: func(p *Pipeline) { p.sweeper = nil },
		},
		{
			name: "anchor type outside the label set — the anchor's soft-delete would never retract",
			knockOut: func(p *Pipeline) {
				p.actorEnumerator = NewActorEnumerator(nil, nil, "service")
			},
		},
		{
			name: "a secure lens whose identity key type is outside the label set",
			knockOut: func(p *Pipeline) {
				p.secureDecryptor = &SecureDecryptor{}
				p.plainReprojectLabels = map[string]struct{}{"identity": {}, "role": {}}
				delete(p.plainReprojectLabels, "identity")
				p.actorEnumerator = NewActorEnumerator(nil, nil, "role")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := eligiblePipeline(t)
			tc.knockOut(p)
			labels, ok := p.ActorAwareNarrowingLabels()
			require.False(t, ok, "a failed conjunct must restore the broad fan-out")
			require.Nil(t, labels)
			require.True(t, p.actorAwareFanOutRelevant("unit"),
				"an ineligible pipeline treats every type as relevant")
		})
	}
}

// TestActorAwareNarrowingLabels_SecureLensNarrowsWhenItSeesIdentity is the
// positive half of the decryptor conjunct: a secure lens is not excluded as a
// class, only one whose label set cannot see vtx.identity.<id>.piiKey.
func TestActorAwareNarrowingLabels_SecureLensNarrowsWhenItSeesIdentity(t *testing.T) {
	p := eligiblePipeline(t)
	p.secureDecryptor = &SecureDecryptor{}
	labels, ok := p.ActorAwareNarrowingLabels()
	require.True(t, ok, "identity is in the label set, so the shred is still delivered")
	require.Equal(t, identityRoleLabels(), labels)
}

// TestActorAwareFanOutRelevant_Semantics pins the variadic contract the link arm
// depends on: relevant when ANY type binds, and defensively relevant for an
// empty type or no types at all.
func TestActorAwareFanOutRelevant_Semantics(t *testing.T) {
	p := eligiblePipeline(t)

	require.True(t, p.actorAwareFanOutRelevant("identity"))
	require.True(t, p.actorAwareFanOutRelevant("role"))
	require.False(t, p.actorAwareFanOutRelevant("unit"))

	require.True(t, p.actorAwareFanOutRelevant("identity", "role"))
	require.True(t, p.actorAwareFanOutRelevant("unit", "role"),
		"a link is skipped only when NEITHER endpoint can bind")
	require.True(t, p.actorAwareFanOutRelevant("identity", "unit"))
	require.False(t, p.actorAwareFanOutRelevant("unit", "building"))

	require.True(t, p.actorAwareFanOutRelevant(""), "an unparsed type must never be skipped")
	require.True(t, p.actorAwareFanOutRelevant("unit", ""))
	require.True(t, p.actorAwareFanOutRelevant())
}

// newGatedFanOutPipeline builds a live, eligible actor-aware pipeline over the
// evalDrift fixture graph, recording every adapter write.
//
// The unit vertex is deliberately wired INTO adjacency, one `occupies` hop from
// the identity: the enumerator is relation-blind, so without the gate every
// unit event — vertex, aspect, or link — reaches the identity and writes its
// row. That is what makes "no upsert" a proof of the skip rather than a proof
// that the graph had no actor to reach, and it is exactly the vacuous-pass trap
// the plain-gate suite already calls out.
func newGatedFanOutPipeline(t *testing.T) (*Pipeline, *recordingAdapter, string) {
	t.Helper()
	coreKV, adjKV, _ := newCollisionKVs(t)

	idID := aargID(t, "actr")
	roleID := aargID(t, "rbac")
	unitID := aargID(t, "unit")
	eng, cr, idKey, _ := evalDriftFixture(t, coreKV, adjKV, idID, roleID, "v0")
	writeCollisionVertex(t, coreKV, "vtx.unit."+unitID, "unit", map[string]any{})
	buildCollisionEdge(t, adjKV, "occupies", "identity", idID, "unit", unitID)

	adpt := &recordingAdapter{}
	p := &Pipeline{
		ruleID:               "actor-aware-relevance-live",
		coreKVBucket:         "CORE",
		coreKV:               coreKV,
		adjKV:                adjKV,
		engineKind:           ruleengine.EngineFull,
		fullEngine:           eng,
		fullCR:               cr,
		envelopeFn:           evalDriftEnvelopeFn,
		plainReprojectLabels: identityRoleLabels(),
		plainReprojectAll:    false,
		patternClosedOutput:  true,
		actorEnumerator:      NewActorEnumerator(adjKV, coreKV, "identity"),
		adpt:                 adpt,
	}
	p.SetSweepPlan(SweepPlan{AnchorType: "identity", KeyPrefix: "cap.identity."})
	_, ok := p.ActorAwareNarrowingLabels()
	require.True(t, ok, "the live fixture must be eligible or the skip assertions are vacuous")
	return p, adpt, idKey
}

func vertexBody(t *testing.T, key, class string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"key": key, "class": class, "isDeleted": false,
		"createdAt": "2026-05-15T10:00:00Z", "lastModifiedAt": "2026-05-15T10:00:00Z",
		"data": map[string]any{"name": "v1"},
	})
	require.NoError(t, err)
	return body
}

// TestHandle_ActorAware_VertexEvent_IrrelevantTypeSkipped proves the KindVertex
// arm: an eligible actor-aware lens Acks a vertex event of a type its patterns
// cannot bind with no enumeration and no write, while a referenced type still
// fans out and projects.
func TestHandle_ActorAware_VertexEvent_IrrelevantTypeSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, adpt, _ := newGatedFanOutPipeline(t)

	unitKey := "vtx.unit." + aargID(t, "unit")
	dec, err := p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + unitKey, Body: vertexBody(t, unitKey, "unit"), Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Empty(t, adpt.upserts, "an unbindable vertex type must never reach the fan-out")
	require.Empty(t, adpt.deletes)

	roleKey := "vtx.role." + aargID(t, "rbac")
	dec, err = p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + roleKey, Body: vertexBody(t, roleKey, "role"), Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Len(t, adpt.upserts, 1, "a referenced type must still fan out to its actors")
}

// TestHandle_ActorAware_AspectEvent_IrrelevantParentSkipped proves the KindAspect
// arm gates on the aspect's PARENT vertex type.
func TestHandle_ActorAware_AspectEvent_IrrelevantParentSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, adpt, idKey := newGatedFanOutPipeline(t)

	unitAspect := "vtx.unit." + aargID(t, "unit") + ".listing"
	dec, err := p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + unitAspect, Body: []byte(`{"data":{}}`), Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Empty(t, adpt.upserts, "an aspect on an unbindable parent type must never fan out")

	dec, err = p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + idKey + ".state", Body: []byte(`{"data":{}}`), Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Len(t, adpt.upserts, 1, "an aspect on a referenced parent type must still fan out")
}

// TestHandle_ActorAware_LinkEvent_NeitherEndpointSkipped proves the KindLink arm
// skips only when NEITHER endpoint type can bind — a link with one bindable
// endpoint must still drive the fan-out.
func TestHandle_ActorAware_LinkEvent_NeitherEndpointSkipped(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	p, adpt, _ := newGatedFanOutPipeline(t)

	unrelated := substrate.LinkKey("unit", aargID(t, "unit"), "partOf", "building", aargID(t, "bdng"))
	dec, err := p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + unrelated, Body: []byte(`{"isDeleted":false}`), Sequence: 1})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Empty(t, adpt.upserts, "a link neither of whose endpoints can bind must never fan out")

	oneBindable := substrate.LinkKey("identity", aargID(t, "actr"), "occupies", "unit", aargID(t, "unit"))
	dec, err = p.handle(ctx, substrate.Message{
		Subject: "$KV.CORE." + oneBindable, Body: []byte(`{"isDeleted":false}`), Sequence: 2})
	require.NoError(t, err)
	require.Equal(t, substrate.Ack, dec)
	require.Len(t, adpt.upserts, 1, "one bindable endpoint is enough to keep the fan-out")
}

// TestHandle_ActorAware_IneligiblePipelineStaysBroad is the fail-closed proof at
// the arm level, and the non-vacuousness proof for the three tests above: the
// SAME three unbindable events they assert are skipped must each reach the
// fan-out and write once a single conjunct fails.
func TestHandle_ActorAware_IneligiblePipelineStaysBroad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	ctx := context.Background()
	unitKey := "vtx.unit." + aargID(t, "unit")

	cases := []struct {
		arm  string
		msg  substrate.Message
		body func(t *testing.T) []byte
	}{
		{arm: "vertex", msg: substrate.Message{Subject: "$KV.CORE." + unitKey},
			body: func(t *testing.T) []byte { return vertexBody(t, unitKey, "unit") }},
		{arm: "aspect", msg: substrate.Message{Subject: "$KV.CORE." + unitKey + ".listing"},
			body: func(*testing.T) []byte { return []byte(`{"data":{}}`) }},
		{arm: "link", msg: substrate.Message{
			Subject: "$KV.CORE." + substrate.LinkKey("unit", aargID(t, "unit"), "partOf", "building", aargID(t, "bdng"))},
			body: func(*testing.T) []byte { return []byte(`{"isDeleted":false}`) }},
	}
	for _, tc := range cases {
		t.Run(tc.arm, func(t *testing.T) {
			p, adpt, _ := newGatedFanOutPipeline(t)
			p.patternClosedOutput = false

			msg := tc.msg
			msg.Body = tc.body(t)
			msg.Sequence = 1
			dec, err := p.handle(ctx, msg)
			require.NoError(t, err)
			require.Equal(t, substrate.Ack, dec)
			require.Len(t, adpt.upserts, 1,
				"an ineligible actor-aware pipeline keeps its unconditional fan-out on every type")
		})
	}
}

// TestVertexEventRelevant_DispatchesByPipelineShape pins that the one gate the
// KindVertex arm now consults still routes a plain lens through
// plainVertexRelevant — the actor-aware conjunction must not have re-scoped what
// any plain lens evaluates.
func TestVertexEventRelevant_DispatchesByPipelineShape(t *testing.T) {
	plain := &Pipeline{
		engineKind:           ruleengine.EngineFull,
		plainReprojectLabels: identityRoleLabels(),
	}
	require.True(t, plain.vertexEventRelevant("identity"))
	require.False(t, plain.vertexEventRelevant("unit"))
	require.True(t, plain.vertexEventRelevant(""))

	actorAware := eligiblePipeline(t)
	require.True(t, actorAware.vertexEventRelevant("identity"))
	require.False(t, actorAware.vertexEventRelevant("unit"))

	// A non-full engine has no label data for either shape.
	require.True(t, (&Pipeline{}).vertexEventRelevant("unit"))
}
