package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func relationsOf(t *testing.T, body string) (map[string]struct{}, bool) {
	t.Helper()
	cr, err := New().Parse(body)
	require.NoError(t, err)
	compiled, isFull := cr.(*CompiledRule)
	require.True(t, isFull)
	return compiled.ReferencedRelations()
}

func relationNames(rels map[string]struct{}) []string {
	out := make([]string, 0, len(rels))
	for r := range rels {
		out = append(out, r)
	}
	return out
}

// TestReferencedRelations pins the derivation the narrowed consumer filter and
// the plain link gate are both built on: which relations a lens can traverse,
// and whether that answer is authoritative.
func TestReferencedRelations(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		want       []string
		exhaustive bool
	}{
		{
			name:       "the live clinicProviders shape — one typed OPTIONAL hop",
			body:       `MATCH (pr:provider) WHERE pr.profile.data.fullName <> null OPTIONAL MATCH (pr)-[:identifiedBy]->(id:identity) RETURN pr.key AS key, id.key AS identityKey`,
			want:       []string{"identifiedBy"},
			exhaustive: true,
		},
		{
			// The strongest possible answer, and the one a caller must not
			// confuse with "no data": NO link can affect this lens.
			name:       "no relationship pattern at all yields an EXHAUSTIVE empty set",
			body:       `MATCH (p:patient) WHERE p.demographics.data.fullName <> null RETURN p.key AS key`,
			want:       nil,
			exhaustive: true,
		},
		{
			name:       "several typed hops across several MATCH clauses",
			body:       `MATCH (pr:provider)-[:practicesAt]->(b:building) MATCH (pr)-[:identifiedBy]->(i:identity) RETURN pr.key AS key, b.key AS siteKey, i.key AS identityKey`,
			want:       []string{"practicesAt", "identifiedBy"},
			exhaustive: true,
		},
		{
			name:       "an inbound hop is still a named relation",
			body:       `MATCH (u:unit)<-[:manages]-(l:identity) RETURN u.key AS key, l.key AS mgr`,
			want:       []string{"manages"},
			exhaustive: true,
		},
		{
			// An untyped hop matches any relation, so no relation may be
			// excluded on its account.
			name:       "an untyped hop is not exhaustive",
			body:       `MATCH (a:unit)-[]->(b:identity) RETURN a.key AS key`,
			want:       nil,
			exhaustive: false,
		},
		{
			name:       "a variable-bound but untyped hop is not exhaustive either",
			body:       `MATCH (a:unit)-[r]->(b:identity) RETURN a.key AS key`,
			want:       nil,
			exhaustive: false,
		},
		{
			// The named relation is still collected: the caller reads the set
			// only when exhaustive, but a partial set must never be WRONG.
			name:       "one typed and one untyped hop: not exhaustive, typed still collected",
			body:       `MATCH (a:unit)-[:manages]->(b:identity) MATCH (b)-[]->(c:provider) RETURN a.key AS key`,
			want:       []string{"manages"},
			exhaustive: false,
		},
		{
			// Unlike ReferencedLabels, a var-length hop costs NOTHING here:
			// traverseRel re-applies rel.Type at every hop of the walk, so a
			// typed var-length walk traverses that relation and no other.
			name:       "a TYPED variable-length hop is still exhaustive",
			body:       `MATCH (a:unit)-[:manages*1..3]->(b:identity) RETURN a.key AS key`,
			want:       []string{"manages"},
			exhaustive: true,
		},
		{
			name:       "an UNTYPED variable-length hop is not exhaustive",
			body:       `MATCH (a:unit)-[*1..3]->(b:identity) RETURN a.key AS key`,
			want:       nil,
			exhaustive: false,
		},
		{
			// A property map is a general expression the executor evaluates
			// (propsAllMatch -> evalExpr), so a pattern nested in one really
			// does traverse a relation. Missing it would report an exhaustive
			// set that omits the relation, and the consumer would never be
			// told the link changed.
			name:       "a relation inside a NODE property map counts",
			body:       "MATCH (b:book {owners: [(b)-[:borrowedBy]->(a:author) | a.key]}) RETURN b.key AS key",
			want:       []string{"borrowedBy"},
			exhaustive: true,
		},
		{
			name:       "a relation reached only through a WHERE pattern predicate counts",
			body:       `MATCH (pr:provider) WHERE (pr)-[:identifiedBy]->(:identity) RETURN pr.key AS key`,
			want:       []string{"identifiedBy"},
			exhaustive: true,
		},
		{
			name:       "a relation in a later WITH segment counts",
			body:       `MATCH (pr:provider) WITH pr MATCH (pr)-[:practicesAt]->(b:building) RETURN pr.key AS key, count(b) AS sites`,
			want:       []string{"practicesAt"},
			exhaustive: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rels, exhaustive := relationsOf(t, tc.body)
			require.Equal(t, tc.exhaustive, exhaustive)
			require.ElementsMatch(t, tc.want, relationNames(rels))
		})
	}
}

// TestReferencedRelations_NilSafe pins the same defensive contract
// AnchorLabel/ReferencedLabels carry: a nil rule reports NOT exhaustive, so
// every caller falls back to the broad, relation-blind behaviour rather than
// narrowing on an empty set.
func TestReferencedRelations_NilSafe(t *testing.T) {
	var nilCR *CompiledRule
	rels, exhaustive := nilCR.ReferencedRelations()
	require.False(t, exhaustive)
	require.Empty(t, rels)

	empty := &CompiledRule{}
	rels, exhaustive = empty.ReferencedRelations()
	require.False(t, exhaustive)
	require.Empty(t, rels)
}

// TestReferencedRelations_ExhaustiveEmptyIsNotTheNonExhaustiveCase is the
// distinction the whole narrowing rests on: `nil relations, exhaustive` means
// "no link can ever matter" (subscribe to no link form), while
// `nil relations, NOT exhaustive` means "any link may matter" (subscribe to
// every link form). Collapsing them would either over-deliver forever or
// blind a lens to its own traversals.
func TestReferencedRelations_ExhaustiveEmptyIsNotTheNonExhaustiveCase(t *testing.T) {
	noRels, okA := relationsOf(t, `MATCH (p:patient) RETURN p.key AS key`)
	require.True(t, okA)
	require.Empty(t, noRels)

	anyRel, okB := relationsOf(t, `MATCH (p:patient)-[]->(o:provider) RETURN p.key AS key`)
	require.False(t, okB)
	require.Empty(t, anyRel)
}

// TestPatternInsidePropertyMapIsWalkedByBothDerivations is the positive vector
// proving the property-map descent actually runs, in BOTH derivations. Nothing
// outside the map mentions `author` or `borrowedBy`, so a walk that stopped at
// PathPattern.Nodes/.Rels could not produce either name — the assertions below
// are only satisfiable by descending into NodePattern.Properties.
//
// This matters because the executor really evaluates those expressions
// (propsAllMatch -> evalExpr -> evalPatternComprehension -> matchPath): a set
// reported exhaustive while missing a type or relation the query traverses
// would narrow the consumer past what the lens can read.
func TestPatternInsidePropertyMapIsWalkedByBothDerivations(t *testing.T) {
	const body = "MATCH (b:book {owners: [(b)-[:borrowedBy]->(a:author) | a.key]}) RETURN b.key AS key"
	cr, err := New().Parse(body)
	require.NoError(t, err)
	compiled, isFull := cr.(*CompiledRule)
	require.True(t, isFull)

	labels, labelsOK := compiled.ReferencedLabels()
	require.True(t, labelsOK)
	require.Contains(t, labels, "author",
		"only reachable through NodePattern.Properties — no other clause names this type")
	require.Contains(t, labels, "book")

	rels, relsOK := compiled.ReferencedRelations()
	require.True(t, relsOK)
	require.Contains(t, rels, "borrowedBy",
		"only reachable through NodePattern.Properties — no other clause names this relation")
}
