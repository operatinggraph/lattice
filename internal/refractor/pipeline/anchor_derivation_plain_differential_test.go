package pipeline

// The plain arm's own superset proof, mirroring
// anchor_derivation_differential_test.go's method for the actor-aware arm:
// never state an expected set, execute the REAL evaluation for every anchor
// before and after each mutation, take the anchors whose projected row
// genuinely differs as ground truth, and require the derivation to have
// named all of them. Ground truth here is evaluatePlainFromVertex's own
// SEEDED evaluation per anchor — not a raw engine call — because that is the
// same entry point Increment 2's re-entry path (evaluatePlainDerivedAnchors)
// uses, so a mismatch between "what the derivation would seed" and "what the
// anchor's own seeded evaluation produces" cannot hide behind two different
// code paths agreeing by accident.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// plainDiffFixture is diffFixture's plain-arm twin: no $actorKey, no
// ActorEnumerator, and "ground truth for one anchor" is a SEEDED plain
// evaluation of that one vertex rather than an actor-parameterised execute.
type plainDiffFixture struct {
	t      *testing.T
	coreKV *substrate.KV
	adjKV  *substrate.KV
	p      *Pipeline
	roots  []string // anchor-type vertex keys (e.g. every provider)
	ids    map[string]string
	types  map[string]string
}

func newPlainDiffFixture(t *testing.T, spec, anchorType string) *plainDiffFixture {
	t.Helper()
	coreKV, adjKV, _ := newCollisionKVs(t)
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)

	p := &Pipeline{ruleID: "plainDifferential", coreKV: coreKV, adjKV: adjKV}
	p.UseFullEngine(eng, cr)

	return &plainDiffFixture{
		t: t, coreKV: coreKV, adjKV: adjKV, p: p,
		ids: map[string]string{}, types: map[string]string{},
	}
}

func (f *plainDiffFixture) vertex(name, vtype string, data map[string]any) string {
	f.t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(f.t, err)
	f.ids[name] = id
	f.types[id] = vtype
	key := substrate.VertexKey(vtype, id)
	f.writeVertex(key, vtype, data)
	return key
}

func (f *plainDiffFixture) writeVertex(key, vtype string, data map[string]any) {
	f.t.Helper()
	body := map[string]any{
		"key": key, "class": vtype, "isDeleted": false,
		"createdAt": "2019-01-01T00:00:00Z", "lastModifiedAt": "2019-01-01T00:00:00Z",
		"data": data,
	}
	raw, err := json.Marshal(body)
	require.NoError(f.t, err)
	_, err = f.coreKV.Put(context.Background(), key, raw)
	require.NoError(f.t, err)
}

func (f *plainDiffFixture) key(name string) string {
	id := f.ids[name]
	require.NotEmpty(f.t, id, "fixture: %q not registered", name)
	return substrate.VertexKey(f.types[id], id)
}

func (f *plainDiffFixture) linkKey(rel, from, to string) string {
	fromID, toID := f.ids[from], f.ids[to]
	require.NotEmpty(f.t, fromID, "fixture: %q not registered", from)
	require.NotEmpty(f.t, toID, "fixture: %q not registered", to)
	return fmt.Sprintf("lnk.%s.%s.%s.%s.%s", f.types[fromID], fromID, rel, f.types[toID], toID)
}

func (f *plainDiffFixture) applyLink(rel, from, to string, deleted bool) string {
	f.t.Helper()
	ctx := context.Background()
	fromID, toID := f.ids[from], f.ids[to]
	fromType, toType := f.types[fromID], f.types[toID]
	linkKey := f.linkKey(rel, from, to)
	for _, evt := range []adjacency.CoreKVEvent{
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "outbound",
			NodeID: fromID, OtherNodeID: toID, OtherType: toType, IsDeleted: deleted},
		{CoreKvKey: linkKey, EdgeID: linkKey, Name: rel, Direction: "inbound",
			NodeID: toID, OtherNodeID: fromID, OtherType: fromType, IsDeleted: deleted},
	} {
		require.NoError(f.t, adjacency.Build(ctx, f.adjKV, evt))
	}
	return linkKey
}

// renderResults canonicalises an EvalResult slice into a sorted, comparable
// string slice — order inside a result set is not part of the projection's
// meaning, so it must not be part of the comparison either.
func renderResults(t *testing.T, results []ruleengine.EvalResult) []string {
	t.Helper()
	rendered := make([]string, 0, len(results))
	for _, r := range results {
		k, err := json.Marshal(r.Keys)
		require.NoError(t, err)
		v, err := json.Marshal(r.Row)
		require.NoError(t, err)
		rendered = append(rendered, fmt.Sprintf("%t|%s|%s", r.Delete, k, v))
	}
	sort.Strings(rendered)
	return rendered
}

// rows executes the real, SEEDED plain evaluation for one anchor via
// evaluatePlainFromVertex — the same entry point Increment 2's re-entry path
// uses — and renders the result set as a comparable string.
func (f *plainDiffFixture) rows(anchorKey, anchorLabel string) string {
	f.t.Helper()
	results, err := f.p.evaluatePlainFromVertex(context.Background(), f.p.ruleState(), anchorKey, anchorLabel)
	require.NoError(f.t, err)
	out, err := json.Marshal(renderResults(f.t, results))
	require.NoError(f.t, err)
	return string(out)
}

func (f *plainDiffFixture) snapshot(anchorLabel string) map[string]string {
	out := make(map[string]string, len(f.roots))
	for _, a := range f.roots {
		out[a] = f.rows(a, anchorLabel)
	}
	return out
}

// requireUnionEqualsUnseeded is §11's own required property, literally: "the
// union of the seeded evaluations equals the UNSEEDED evaluation's rows for
// those anchors" — the soundness precondition for Increment 4's eventual
// act-mode substitution of K seeded evaluations for today's one unseeded
// whole-corpus scan. eventKey/eventLabel identify the triggering CDC entry —
// the vertex the unseeded scan runs for TODAY, exactly as
// evaluatePlainNeighbourEvent's own unseeded() closure builds it
// (entry.CoreKVKey, entry.Properties, seedAnchor ""). anchors is the derived
// set for that same event.
func (f *plainDiffFixture) requireUnionEqualsUnseeded(t *testing.T, eventKey, eventLabel, anchorLabel string, anchors []string) {
	t.Helper()
	ctx := context.Background()
	rs := f.p.ruleState()

	eventProps, err := f.p.fetchVertexProps(ctx, eventKey)
	require.NoError(t, err)
	require.NotNil(t, eventProps, "the event vertex must exist for the unseeded comparison")

	unseeded, err := f.p.executeFullForActor(ctx, rs, eventKey, eventProps, "")
	require.NoError(t, err)

	anchorSet := make(map[string]struct{}, len(anchors))
	for _, a := range anchors {
		anchorSet[a] = struct{}{}
	}
	var unseededForAnchors []ruleengine.EvalResult
	for _, r := range unseeded {
		k, _ := r.Keys["key"].(string)
		if _, in := anchorSet[k]; in {
			unseededForAnchors = append(unseededForAnchors, r)
		}
	}

	seededUnion, err := f.p.evaluatePlainDerivedAnchors(ctx, rs, anchors, anchorLabel)
	require.NoError(t, err)

	require.Equal(t, renderResults(t, unseededForAnchors), renderResults(t, seededUnion),
		"the union of the seeded per-anchor evaluations must equal the unseeded evaluation's rows for those anchors — this is what makes Increment 4's eventual substitution sound")
}

// TestPlainDerivation_Differential_ClinicProvidersShape is the fire's own
// named payoff, run as a superset proof: on the identifiedBy link and on a
// bare neighbour vertex event, the derived set must cover every provider
// whose row actually changed. Every mutation below keeps the real
// clinicProviders domain's implicit 1:1 provider-identifiedBy cardinality —
// no provider ever gains a SECOND identifiedBy edge — because
// dedupeKeyFor's identity is the target Keys alone (pre-existing, unchanged
// by this fire): two rows sharing one provider's key but different identity
// content is a shape the real lens's own domain model never produces, and
// exercising it here would test dedupeKeyFor's cardinality assumption rather
// than this fire's own soundness property.
func TestPlainDerivation_Differential_ClinicProvidersShape(t *testing.T) {
	f := newPlainDiffFixture(t, providerSpec, "provider")
	ctx := context.Background()

	pr1 := f.vertex("pr1", "provider", nil)
	pr2 := f.vertex("pr2", "provider", nil) // starts with no identity at all
	f.roots = []string{pr1, pr2}
	f.vertex("id1", "identity", map[string]any{"name": "alice"})
	f.vertex("id2", "identity", map[string]any{"name": "bob"})

	f.applyLink("identifiedBy", "pr1", "id1", false)

	rs := f.p.ruleState()
	require.True(t, rs.rootHops.Complete, "clinicProviders-shaped root must be indexable: %s", rs.rootHops.Incomplete)

	// Case 1: a link event — pr2 gains its first (and only) identity.
	before := f.snapshot("provider")
	newLinkKey := f.applyLink("identifiedBy", "pr2", "id2", false)
	after := f.snapshot("provider")
	changed := changedAnchors(before, after)
	require.NotEmpty(t, changed, "fixture defect — no provider's row moved")

	derived, ok, err := f.p.deriveAnchorsForPlainLink(ctx, rs, newLinkKey)
	require.NoError(t, err)
	require.True(t, requireSuperset(t, "link pr2's first identity", derived, ok, changed))
	// §11's own required property: the union of the seeded per-anchor
	// evaluations must equal the unseeded scan's rows for those anchors — the
	// neighbour (identity) endpoint is what today's shipped arm actually runs
	// unseeded for this event.
	f.requireUnionEqualsUnseeded(t, f.key("id2"), "identity", "provider", derived)

	// Case 2: the bare neighbour VERTEX event (id1 changes, no link mutation)
	// — pr1 depends on it and must be named.
	before = f.snapshot("provider")
	f.writeVertex(f.key("id1"), "identity", map[string]any{"name": "alice-renamed"})
	after = f.snapshot("provider")
	changed = changedAnchors(before, after)
	require.NotEmpty(t, changed, "fixture defect — renaming id1 must move a provider row")

	derived, ok, err = f.p.deriveAnchorsForPlainVertex(ctx, rs, f.key("id1"), "identity")
	require.NoError(t, err)
	require.True(t, requireSuperset(t, "rename id1", derived, ok, changed))
	f.requireUnionEqualsUnseeded(t, f.key("id1"), "identity", "provider", derived)

	// Case 3: revoke the link — the provider's row must still be named (it
	// keeps projecting, now with no identity).
	before = f.snapshot("provider")
	revokedKey := f.applyLink("identifiedBy", "pr1", "id1", true)
	after = f.snapshot("provider")
	changed = changedAnchors(before, after)
	require.NotEmpty(t, changed, "fixture defect — revoking the link must move pr1's row")

	derived, ok, err = f.p.deriveAnchorsForPlainLink(ctx, rs, revokedKey)
	require.NoError(t, err)
	require.True(t, requireSuperset(t, "revoke pr1's identity link", derived, ok, changed))
	f.requireUnionEqualsUnseeded(t, f.key("id1"), "identity", "provider", derived)
}

// TestPlainDerivation_Differential_TwoHop is the same proof over the 2-hop
// provider->org->location shape, crossing two adjacency documents.
func TestPlainDerivation_Differential_TwoHop(t *testing.T) {
	f := newPlainDiffFixture(t, providerOrgLocationSpec, "provider")
	ctx := context.Background()

	pr1 := f.vertex("pr1", "provider", nil)
	pr2 := f.vertex("pr2", "provider", nil)
	f.roots = []string{pr1, pr2}
	org1 := f.vertex("org1", "org", nil)
	_ = org1
	f.vertex("loc1", "location", map[string]any{"city": "Springfield"})

	f.applyLink("employedBy", "pr1", "org1", false)
	f.applyLink("locatedIn", "org1", "loc1", false)

	rs := f.p.ruleState()
	require.True(t, rs.rootHops.Complete, "2-hop shape must be indexable: %s", rs.rootHops.Incomplete)

	before := f.snapshot("provider")
	f.writeVertex(f.key("loc1"), "location", map[string]any{"city": "Shelbyville"})
	after := f.snapshot("provider")
	changed := changedAnchors(before, after)
	require.NotEmpty(t, changed, "fixture defect — renaming loc1 must move pr1's row (loc.key is projected)")

	derived, ok, err := f.p.deriveAnchorsForPlainVertex(ctx, rs, f.key("loc1"), "location")
	require.NoError(t, err)
	require.True(t, requireSuperset(t, "rename the two-hop-out location", derived, ok, changed))
	require.NotContains(t, derived, pr2, "pr2 never reaches loc1, so a sound derivation must not name it")
	f.requireUnionEqualsUnseeded(t, f.key("loc1"), "location", "provider", derived)
}
