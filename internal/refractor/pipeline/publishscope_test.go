package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// scopeAnchorA/B/C are valid 20-char NanoIDs, so the anchor keys built from
// them are the Contract #1 vertex keys ParseVertexKey actually accepts — a
// scope test over keys the parser rejects would assert nothing about the match.
const (
	scopeAnchorA = "Hj4kPmRtw9nbCxz5vQ2y"
	scopeAnchorB = "Kx3TmZpq7RvwNsY2Hc9L"
	scopeAnchorC = "Nb7RvwKx3TmZpq2Hc9Ls"
)

// scopeRow builds the shape Admits reads: a non-delete result whose "anchor"
// alias carries the anchor vertex key, exactly as a personal lens's cypher
// aliases it.
func scopeRow(anchorKey string) ruleengine.EvalResult {
	return ruleengine.EvalResult{Row: map[string]any{"anchor": anchorKey}}
}

// scopeAlphabet is the NanoID alphabet's lower-case run, which excludes the
// characters Contract #1 forbids (I, l, O, 0) — so an id built from it is one
// substrate.IsValidNanoID accepts.
const scopeAlphabet = "abcdefghijkmnopqrstuvwxyz"

// scopeIDs generates n distinct valid NanoIDs, for the bound-crossing vectors.
func scopeIDs(n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, "ScopeAnchorAAAAAA"+
			string(scopeAlphabet[i/len(scopeAlphabet)])+
			string(scopeAlphabet[i%len(scopeAlphabet)])+"z")
	}
	return ids
}

func TestPublishScope_Admits(t *testing.T) {
	keyA := substrate.VertexKey("lease", scopeAnchorA)
	keyB := substrate.VertexKey("lease", scopeAnchorB)

	cases := []struct {
		name  string
		scope PublishScope
		row   ruleengine.EvalResult
		want  bool
	}{
		{"the zero value admits every row", PublishScope{}, scopeRow(keyA), true},
		{"ScopeAll admits every row", ScopeAll(), scopeRow(keyA), true},
		{"ScopeAll admits a row with no anchor at all", ScopeAll(), ruleengine.EvalResult{}, true},
		{"ScopeNone admits nothing", ScopeNone(), scopeRow(keyA), false},
		{"ScopeAnchors admits a named anchor", ScopeAnchors([]string{scopeAnchorA}), scopeRow(keyA), true},
		{"ScopeAnchors declines an unnamed anchor", ScopeAnchors([]string{scopeAnchorA}), scopeRow(keyB), false},
		{"ScopeAnchors admits any member of the set", ScopeAnchors([]string{scopeAnchorA, scopeAnchorB}), scopeRow(keyB), true},
		{"an unparseable anchor is not admitted under ScopeAnchors", ScopeAnchors([]string{scopeAnchorA}), scopeRow("lease/" + scopeAnchorA), false},
		{"an absent anchor is not admitted under ScopeAnchors", ScopeAnchors([]string{scopeAnchorA}), ruleengine.EvalResult{}, false},
		{"an empty anchor alias is not admitted under ScopeAnchors", ScopeAnchors([]string{scopeAnchorA}), scopeRow(""), false},
		{"an empty constructor set is ScopeAll, never 'admit nothing'", ScopeAnchors(nil), scopeRow(keyA), true},
		{"a set of blank tokens is ScopeAll", ScopeAnchors([]string{"", ""}), scopeRow(keyA), true},
		// A token no anchor key can ever parse to matches no row, so a scope
		// built only of them would be ScopeNone wearing ScopeAnchors' name —
		// the one reading the constructor must never produce by accident.
		{"a set of malformed tokens is ScopeAll", ScopeAnchors([]string{"nope", "vtx.lease." + scopeAnchorA}), scopeRow(keyA), true},
		{"a malformed token is dropped from a set that keeps a real one", ScopeAnchors([]string{"nope", scopeAnchorB}), scopeRow(keyB), true},
		{"and dropping it does not widen the set that survives", ScopeAnchors([]string{"nope", scopeAnchorB}), scopeRow(keyA), false},
		{"an over-bound constructor set is ScopeAll", ScopeAnchors(scopeIDs(MaxScopedAnchors + 1)), scopeRow(keyA), true},
		{"an unparseable anchor is still admitted under ScopeAll", ScopeAll(), scopeRow("not-a-key"), true},
		{"an unparseable anchor is still declined under ScopeNone", ScopeNone(), scopeRow("not-a-key"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.scope.Admits(tc.row))
		})
	}
}

func TestPublishScope_KindAndZeroValue(t *testing.T) {
	assert.Equal(t, ScopeKindAll, PublishScope{}.Kind(),
		"the zero value must be ScopeAll: a caller that forgets a scope publishes the whole actor")
	assert.Equal(t, ScopeKindAll, ScopeAll().Kind())
	assert.Equal(t, ScopeKindNone, ScopeNone().Kind())
	assert.Equal(t, ScopeKindAnchors, ScopeAnchors([]string{scopeAnchorA}).Kind())
	assert.Equal(t, ScopeKindAll, ScopeAnchors(nil).Kind(), "an empty set names no anchors, so it is All")
	assert.Equal(t, ScopeKindAll, ScopeAnchors([]string{"nope"}).Kind(),
		"a set that empties out under the NanoID filter names no anchors either, so it is All and never 'admit nothing'")
	assert.Equal(t, "anchors(1):"+scopeAnchorA, ScopeAnchors([]string{"nope", scopeAnchorA}).String(),
		"the malformed token is dropped from the set, not carried in it")

	assert.Equal(t, "all", ScopeAll().String())
	assert.Equal(t, "none", ScopeNone().String())
	assert.Equal(t, "anchors(2):"+scopeAnchorA+","+scopeAnchorB,
		ScopeAnchors([]string{scopeAnchorB, scopeAnchorA}).String(),
		"the printed set is sorted, so two equal scopes print identically")
}

func TestPublishScope_Merge(t *testing.T) {
	anchorsA := ScopeAnchors([]string{scopeAnchorA})
	anchorsB := ScopeAnchors([]string{scopeAnchorB})

	cases := []struct {
		name     string
		left     PublishScope
		right    PublishScope
		wantKind ScopeKind
		// admits, when non-empty, is the anchor set the merged scope must admit.
		admits []string
		// declines, when non-empty, is what it must not.
		declines []string
	}{
		{name: "All ⊔ None = All", left: ScopeAll(), right: ScopeNone(), wantKind: ScopeKindAll},
		{name: "None ⊔ All = All", left: ScopeNone(), right: ScopeAll(), wantKind: ScopeKindAll},
		{name: "All ⊔ Anchors = All", left: ScopeAll(), right: anchorsA, wantKind: ScopeKindAll},
		{name: "Anchors ⊔ All = All", left: anchorsA, right: ScopeAll(), wantKind: ScopeKindAll},
		{name: "All ⊔ All = All", left: ScopeAll(), right: ScopeAll(), wantKind: ScopeKindAll},
		{name: "None ⊔ None = None", left: ScopeNone(), right: ScopeNone(), wantKind: ScopeKindNone},
		{
			name: "None ⊔ Anchors = Anchors", left: ScopeNone(), right: anchorsA,
			wantKind: ScopeKindAnchors, admits: []string{scopeAnchorA}, declines: []string{scopeAnchorB},
		},
		{
			name: "Anchors ⊔ None = Anchors", left: anchorsA, right: ScopeNone(),
			wantKind: ScopeKindAnchors, admits: []string{scopeAnchorA}, declines: []string{scopeAnchorB},
		},
		{
			name: "Anchors(A) ⊔ Anchors(B) = Anchors(A ∪ B)", left: anchorsA, right: anchorsB,
			wantKind: ScopeKindAnchors, admits: []string{scopeAnchorA, scopeAnchorB}, declines: []string{scopeAnchorC},
		},
		{
			name: "the union de-duplicates", left: anchorsA, right: ScopeAnchors([]string{scopeAnchorA}),
			wantKind: ScopeKindAnchors, admits: []string{scopeAnchorA}, declines: []string{scopeAnchorB},
		},
		{
			name:     "a union AT the bound stays scoped",
			left:     ScopeAnchors(scopeIDs(MaxScopedAnchors - 1)),
			right:    anchorsA,
			wantKind: ScopeKindAnchors, admits: []string{scopeAnchorA}, declines: []string{scopeAnchorB},
		},
		{
			name:     "a union PAST the bound widens to All",
			left:     ScopeAnchors(scopeIDs(MaxScopedAnchors)),
			right:    anchorsA,
			wantKind: ScopeKindAll, admits: []string{scopeAnchorA, scopeAnchorB},
		},
		{
			name: "the zero value merges as All", left: PublishScope{}, right: anchorsA,
			wantKind: ScopeKindAll, admits: []string{scopeAnchorB},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.left.Merge(tc.right)
			require.Equal(t, tc.wantKind, got.Kind(), "merged scope: %s", got)
			for _, id := range tc.admits {
				assert.True(t, got.Admits(scopeRow(substrate.VertexKey("lease", id))),
					"the merge must admit %s", id)
			}
			for _, id := range tc.declines {
				assert.False(t, got.Admits(scopeRow(substrate.VertexKey("lease", id))),
					"the merge must not admit %s", id)
			}
		})
	}
}

// TestPublishScope_MergeDoesNotMutateItsOperands pins the immutability the
// coalescing queue rests on: a scope already handed to a reprojection must not
// widen underneath it when the queue merges a later signal into a copy.
func TestPublishScope_MergeDoesNotMutateItsOperands(t *testing.T) {
	left := ScopeAnchors([]string{scopeAnchorA})
	right := ScopeAnchors([]string{scopeAnchorB})

	merged := left.Merge(right)

	require.True(t, merged.Admits(scopeRow(substrate.VertexKey("lease", scopeAnchorB))))
	assert.False(t, left.Admits(scopeRow(substrate.VertexKey("lease", scopeAnchorB))),
		"the left operand must still name only its own anchor")
	assert.False(t, right.Admits(scopeRow(substrate.VertexKey("lease", scopeAnchorA))),
		"and the right operand only its own")
}

// TestPublishScope_BoundVectorsUseRealNanoIDs keeps the bound vectors honest:
// the generated ids must be the shape ParseVertexKey accepts, or the
// bound-crossing rows above would be declined for the wrong reason.
func TestPublishScope_BoundVectorsUseRealNanoIDs(t *testing.T) {
	for _, id := range scopeIDs(MaxScopedAnchors + 1) {
		require.True(t, substrate.IsValidNanoID(id), "generated anchor id %q is not a NanoID", id)
	}
}

// scopeVertexA/B/C are the vertex keys a CDC arm hands ScopeVertices.
var (
	scopeVertexA = substrate.VertexKey("lease", scopeAnchorA)
	scopeVertexB = substrate.VertexKey("lease", scopeAnchorB)
	scopeVertexC = substrate.VertexKey("identity", scopeAnchorC)
)

// provenanceRow builds the shape ScopeVertices reads: a non-delete result
// carrying the vertex keys its evaluation recorded.
func provenanceRow(vertexKeys ...string) ruleengine.EvalResult {
	return ruleengine.EvalResult{Row: map[string]any{"anchor": scopeVertexA}, Provenance: vertexKeys}
}

// scopeVertexKeys generates n distinct valid vertex keys, for the bound vectors.
func scopeVertexKeys(n int) []string {
	keys := make([]string, 0, n)
	for _, id := range scopeIDs(n) {
		keys = append(keys, substrate.VertexKey("lease", id))
	}
	return keys
}

func TestPublishScope_VerticesAdmits(t *testing.T) {
	cases := []struct {
		name  string
		scope PublishScope
		row   ruleengine.EvalResult
		want  bool
	}{
		{"a row whose provenance meets the set is admitted",
			ScopeVertices([]string{scopeVertexA}), provenanceRow(scopeVertexA), true},
		{"a row whose provenance misses the set is withheld",
			ScopeVertices([]string{scopeVertexA}), provenanceRow(scopeVertexB), false},
		{"one member of the provenance meeting the set is enough",
			ScopeVertices([]string{scopeVertexB}), provenanceRow(scopeVertexA, scopeVertexC, scopeVertexB), true},
		{"one member of the SET being met is enough",
			ScopeVertices([]string{scopeVertexB, scopeVertexC}), provenanceRow(scopeVertexC), true},
		// The fail-open reading, and the one this arm's correctness rests on:
		// a result nothing recorded provenance for must publish as it does
		// today, not be silenced.
		{"a row with NO provenance is admitted",
			ScopeVertices([]string{scopeVertexA}), provenanceRow(), true},
		{"a row with nil provenance is admitted",
			ScopeVertices([]string{scopeVertexA}), ruleengine.EvalResult{}, true},
		{"an empty constructor set is ScopeAll, never 'admit nothing'",
			ScopeVertices(nil), provenanceRow(scopeVertexB), true},
		{"a set of blank tokens is ScopeAll",
			ScopeVertices([]string{"", ""}), provenanceRow(scopeVertexB), true},
		// A token provenance can never carry is ScopeNone wearing this arm's
		// name — the reading the constructor refuses to produce by accident.
		{"a set of non-vertex tokens is ScopeAll",
			ScopeVertices([]string{"nope", substrate.LinkKey("identity", scopeAnchorA, "holds", "lease", scopeAnchorB)}),
			provenanceRow(scopeVertexB), true},
		{"a non-vertex token is dropped from a set that keeps a real one",
			ScopeVertices([]string{"nope", scopeVertexB}), provenanceRow(scopeVertexB), true},
		{"and dropping it does not widen the set that survives",
			ScopeVertices([]string{"nope", scopeVertexB}), provenanceRow(scopeVertexA), false},
		{"an over-bound constructor set is ScopeAll",
			ScopeVertices(scopeVertexKeys(MaxScopedAnchors + 1)), provenanceRow(scopeVertexC), true},
		{"ScopeNone withholds a row whatever its provenance",
			ScopeNone(), provenanceRow(scopeVertexA), false},
		{"ScopeAll admits a row whatever its provenance",
			ScopeAll(), provenanceRow(scopeVertexA), true},
		// The two set arms answer different questions and must never read each
		// other's set: an anchor scope judges the "anchor" alias, which every
		// provenanceRow here carries as scopeVertexA.
		{"ScopeAnchors ignores provenance entirely",
			ScopeAnchors([]string{scopeAnchorA}), provenanceRow(scopeVertexB), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.scope.Admits(tc.row))
		})
	}
}

func TestPublishScope_VerticesKindAndString(t *testing.T) {
	assert.Equal(t, ScopeKindVertices, ScopeVertices([]string{scopeVertexA}).Kind())
	assert.Equal(t, ScopeKindAll, ScopeVertices(nil).Kind(), "an empty set names no vertices, so it is All")
	assert.Equal(t, ScopeKindAll, ScopeVertices([]string{"nope"}).Kind(),
		"a set that empties out under the vertex-key filter is All too, never 'admit nothing'")
	assert.Equal(t, "vertices(2):"+scopeVertexA+","+scopeVertexB,
		ScopeVertices([]string{scopeVertexB, scopeVertexA, scopeVertexB}).String(),
		"the printed set is sorted and de-duplicated, so two equal scopes print identically")
}

// TestPublishScope_MergeVertices extends the merge law with the vertex arm.
//
// The mixed arm is the one no live path produces today — no producer enqueues a
// vertex scope into the coalescing dirty set — and it is pinned anyway: the law
// has to answer for every pair it can be handed, and All is the only answer
// that admits everything either operand admits when neither set can express the
// other's question.
func TestPublishScope_MergeVertices(t *testing.T) {
	verticesA := ScopeVertices([]string{scopeVertexA})
	verticesB := ScopeVertices([]string{scopeVertexB})
	anchorsA := ScopeAnchors([]string{scopeAnchorA})

	cases := []struct {
		name     string
		left     PublishScope
		right    PublishScope
		wantKind ScopeKind
		admits   []ruleengine.EvalResult
		declines []ruleengine.EvalResult
	}{
		{
			name: "All ⊔ Vertices = All", left: ScopeAll(), right: verticesA,
			wantKind: ScopeKindAll, admits: []ruleengine.EvalResult{provenanceRow(scopeVertexC)},
		},
		{
			name: "Vertices ⊔ All = All", left: verticesA, right: ScopeAll(),
			wantKind: ScopeKindAll, admits: []ruleengine.EvalResult{provenanceRow(scopeVertexC)},
		},
		{
			name: "None ⊔ Vertices = Vertices", left: ScopeNone(), right: verticesA,
			wantKind: ScopeKindVertices,
			admits:   []ruleengine.EvalResult{provenanceRow(scopeVertexA)},
			declines: []ruleengine.EvalResult{provenanceRow(scopeVertexB)},
		},
		{
			name: "Vertices ⊔ None = Vertices", left: verticesA, right: ScopeNone(),
			wantKind: ScopeKindVertices,
			admits:   []ruleengine.EvalResult{provenanceRow(scopeVertexA)},
			declines: []ruleengine.EvalResult{provenanceRow(scopeVertexB)},
		},
		{
			name: "Vertices(V) ⊔ Vertices(W) = Vertices(V ∪ W)", left: verticesA, right: verticesB,
			wantKind: ScopeKindVertices,
			admits:   []ruleengine.EvalResult{provenanceRow(scopeVertexA), provenanceRow(scopeVertexB)},
			declines: []ruleengine.EvalResult{provenanceRow(scopeVertexC)},
		},
		{
			name:     "a vertex union AT the bound stays scoped",
			left:     ScopeVertices(scopeVertexKeys(MaxScopedAnchors - 1)),
			right:    verticesA,
			wantKind: ScopeKindVertices,
			admits:   []ruleengine.EvalResult{provenanceRow(scopeVertexA)},
			declines: []ruleengine.EvalResult{provenanceRow(scopeVertexC)},
		},
		{
			name:     "a vertex union PAST the bound widens to All",
			left:     ScopeVertices(scopeVertexKeys(MaxScopedAnchors)),
			right:    verticesA,
			wantKind: ScopeKindAll,
			admits:   []ruleengine.EvalResult{provenanceRow(scopeVertexC)},
		},
		{
			name: "Vertices ⊔ Anchors = All", left: verticesA, right: anchorsA,
			wantKind: ScopeKindAll, admits: []ruleengine.EvalResult{provenanceRow(scopeVertexC)},
		},
		{
			name: "Anchors ⊔ Vertices = All", left: anchorsA, right: verticesA,
			wantKind: ScopeKindAll, admits: []ruleengine.EvalResult{provenanceRow(scopeVertexC)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.left.Merge(tc.right)
			require.Equal(t, tc.wantKind, got.Kind(), "merged scope: %s", got)
			for _, row := range tc.admits {
				assert.True(t, got.Admits(row), "the merge must admit %v", row.Provenance)
			}
			for _, row := range tc.declines {
				assert.False(t, got.Admits(row), "the merge must not admit %v", row.Provenance)
			}
		})
	}
}

// TestPublishScope_MergeVerticesDoesNotMutateItsOperands is the vertex arm's
// half of the immutability the coalescing queue rests on.
func TestPublishScope_MergeVerticesDoesNotMutateItsOperands(t *testing.T) {
	left := ScopeVertices([]string{scopeVertexA})
	right := ScopeVertices([]string{scopeVertexB})

	merged := left.Merge(right)

	require.True(t, merged.Admits(provenanceRow(scopeVertexB)))
	assert.False(t, left.Admits(provenanceRow(scopeVertexB)),
		"the left operand must still name only its own vertex")
	assert.False(t, right.Admits(provenanceRow(scopeVertexA)),
		"and the right operand only its own")
}
