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
