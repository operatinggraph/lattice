package projection

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
	servicelocation "github.com/operatinggraph/lattice/packages/service-location"
)

// compileSpec parses a real shipped package lens's cypher body into a
// *full.CompiledRule the classifier consumes — mirroring how
// internal/refractor/ruleengine/full's own tests compile a bare cypher
// string (executor_test.go's parseExec / full.New().Parse).
func compileSpec(t *testing.T, spec string) *full.CompiledRule {
	t.Helper()
	cr, err := full.New().Parse(spec)
	require.NoError(t, err, "spec must parse")
	fcr, ok := cr.(*full.CompiledRule)
	require.True(t, ok, "full engine must produce a *full.CompiledRule")
	return fcr
}

func specByName(t *testing.T, lenses []pkgmgr.LensSpec, canonicalName string) string {
	t.Helper()
	for _, l := range lenses {
		if l.CanonicalName == canonicalName {
			require.NotEmpty(t, l.Spec, "lens %q must declare a cypher spec", canonicalName)
			return l.Spec
		}
	}
	require.FailNowf(t, "lens not found", "canonicalName=%q", canonicalName)
	return ""
}

// TestHasMultiBindingConjunctUnit_PinnedCensus reproduces
// refractor-evaluation-consistency-design.md §13.3's census-reproduction
// table against the REAL shipped package cypher (not a hand-written
// fixture, which would prove only itself) — the classifier's acceptance
// criteria: it must agree with the ratified §3 census row for row, or the
// classifier is wrong, not the census.
func TestHasMultiBindingConjunctUnit_PinnedCensus(t *testing.T) {
	cases := []struct {
		name    string
		spec    string
		want    bool
		explain string
	}{
		{
			name:    "capabilityEphemeral",
			spec:    specByName(t, orchestrationbase.Lenses(), "capabilityEphemeral"),
			want:    true,
			explain: "multi-binding — {task,op,tgt} per branch",
		},
		{
			name:    "capabilityServiceAccess",
			spec:    specByName(t, servicelocation.Lenses(), "capabilityServiceAccess"),
			want:    true,
			explain: "multi-binding — {svc,loc} before its comprehension is even counted",
		},
		{
			name:    "staffReadGrants",
			spec:    specByName(t, servicelocation.Lenses(), "staffReadGrants"),
			want:    true,
			explain: "multi-binding — U0={staff,building}",
		},
		{
			name:    "capabilityRoles",
			spec:    specByName(t, rbacdomain.Lenses(), "capabilityRoles"),
			want:    false,
			explain: "single-key perm.data entry, single-key role.key entry",
		},
		{
			name:    "capabilityRoleIndex",
			spec:    specByName(t, rbacdomain.Lenses(), "capabilityRoleIndex"),
			want:    false,
			explain: "single-key perm.data.operationType, single-key role.canonicalName",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr := compileSpec(t, tc.spec)
			got := hasMultiBindingConjunctUnit(cr)
			require.Equalf(t, tc.want, got, "%s: %s", tc.name, tc.explain)
		})
	}
}

// --- fail-closed vectors (§13.3) ---

// TestHasMultiBindingConjunctUnit_GeneratedActorAggregateProducers exercises
// the REAL generated cap-read producers (internal/pkgmgr/anchorwalk.go's
// ExpandReadGrantWalks, packages/edge-manifest's ReadGrantDomains) — the
// staged-WITH shape this file's WITH-resolution logic exists for. Every
// grant entry in every one of these producers is a single-binding MapLiteral
// (one anchor per walk), so all three must classify false, exactly like the
// hand-authored producers above.
func TestHasMultiBindingConjunctUnit_GeneratedActorAggregateProducers(t *testing.T) {
	expanded, err := edgemanifest.Package.ExpandReadGrantWalks()
	require.NoError(t, err, "edge-manifest's read-grant walks must compile")

	for _, name := range []string{
		"edgeManifestReadGrants",
		"edgeManifestStaffReadGrants",
		"edgeManifestProviderReadGrants",
	} {
		t.Run(name, func(t *testing.T) {
			spec := specByName(t, expanded.Lenses, name)
			cr := compileSpec(t, spec)
			require.False(t, hasMultiBindingConjunctUnit(cr),
				"%s: every grant entry is a single-binding MapLiteral scoped to its own walk", name)
		})
	}
}

// TestHasMultiBindingConjunctUnit_StagedVsFlatParity pins the fix's
// correctness target directly: a staged producer (one WITH per walk, folding
// that walk's collect(...) into its own grantSliceN before the RETURN
// concatenates them — internal/pkgmgr/anchorwalk.go's generateProducerSpec)
// must classify IDENTICALLY to the flat equivalent (every OPTIONAL MATCH in
// one run, `collect(...) + collect(...)` inline in the RETURN).
func TestHasMultiBindingConjunctUnit_StagedVsFlatParity(t *testing.T) {
	staged := `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:worksAt]->(loc:location)
WITH identity,
  collect(DISTINCT {anchorType: 'location', anchorId: nanoIdFromKey(loc.key), via: ['worksAt']}) AS grantSlice0
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)-[:offeredTo]->(pane:pane)
WITH identity, grantSlice0,
  collect(DISTINCT {anchorType: 'pane', anchorId: nanoIdFromKey(pane.key), via: ['holdsRole', 'offeredTo']}) AS grantSlice1
RETURN
  identity.key AS actorKey,
  grantSlice0 + grantSlice1 AS readableAnchors
`
	flat := `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:worksAt]->(loc:location)
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)-[:offeredTo]->(pane:pane)
RETURN
  identity.key AS actorKey,
  collect(DISTINCT {anchorType: 'location', anchorId: nanoIdFromKey(loc.key), via: ['worksAt']}) +
  collect(DISTINCT {anchorType: 'pane', anchorId: nanoIdFromKey(pane.key), via: ['holdsRole', 'offeredTo']}) AS readableAnchors
`
	stagedVerdict := hasMultiBindingConjunctUnit(compileSpec(t, staged))
	flatVerdict := hasMultiBindingConjunctUnit(compileSpec(t, flat))
	require.False(t, stagedVerdict, "staged producer: every grant entry is single-binding")
	require.Equal(t, flatVerdict, stagedVerdict, "staged and flat forms of the same producer must classify identically")
}

// TestHasMultiBindingConjunctUnit_WithAlias_GenuineMultiBinding_StillValidates
// confirms WITH resolution does not launder a REAL multi-binding unit through
// an alias: a WITH item that combines two distinct bindings into one
// non-aggregate expression, referenced by the RETURN, must still validate —
// exactly the shape hasMultiBindingConjunctUnit exists to catch, just one
// hop away through a WITH instead of written directly in the RETURN.
func TestHasMultiBindingConjunctUnit_WithAlias_GenuineMultiBinding_StillValidates(t *testing.T) {
	spec := `
MATCH (a:foo)
MATCH (b:bar)
WITH a.x + b.y AS combined
RETURN combined AS result
`
	require.True(t, hasMultiBindingConjunctUnit(compileSpec(t, spec)))
}

// TestHasMultiBindingConjunctUnit_ReturnUnresolvedByAnyWith_OrdinaryMultiBinding
// confirms WITH resolution is a no-op for names no WITH clause defines — the
// existing ordinary-binding classification must keep working when a RETURN
// references two plain bindings that were never routed through a WITH alias
// at all (or through one that doesn't mention them).
func TestHasMultiBindingConjunctUnit_ReturnUnresolvedByAnyWith_OrdinaryMultiBinding(t *testing.T) {
	spec := `
MATCH (a:foo)
MATCH (b:bar)
WITH a
RETURN a AS x, b AS y
`
	require.True(t, hasMultiBindingConjunctUnit(compileSpec(t, spec)))
}

// TestHasMultiBindingConjunctUnit_FailClosed_WithAliasCycle pins the
// resolveWithAliases recursion-depth cap: two WITH aliases defined in terms
// of each other (x's definition names y, y's definition names x) can never be
// fully resolved. Built directly on the AST — real Cypher's scoping rules
// couldn't produce this shape through the parser — the same way the other
// fail-closed vectors in this file construct a *full.CompiledRule by hand.
func TestHasMultiBindingConjunctUnit_FailClosed_WithAliasCycle(t *testing.T) {
	cr := &full.CompiledRule{Query: &full.Query{Clauses: []full.Clause{
		&full.With{Items: []full.ProjectionItem{
			{Expr: &full.VariableRef{Name: "y"}, Alias: "x"},
		}},
		&full.With{Items: []full.ProjectionItem{
			{Expr: &full.VariableRef{Name: "x"}, Alias: "y"},
		}},
		&full.Return{Items: []full.ProjectionItem{
			{Expr: &full.VariableRef{Name: "x"}, Alias: "result"},
		}},
	}}}
	require.True(t, hasMultiBindingConjunctUnit(cr))
}

func TestHasMultiBindingConjunctUnit_FailClosed_NilCompiledRule(t *testing.T) {
	require.True(t, hasMultiBindingConjunctUnit(nil))
}

func TestHasMultiBindingConjunctUnit_FailClosed_NilQuery(t *testing.T) {
	require.True(t, hasMultiBindingConjunctUnit(&full.CompiledRule{Query: nil}))
}

func TestHasMultiBindingConjunctUnit_FailClosed_NoReturnClause(t *testing.T) {
	cr := &full.CompiledRule{Query: &full.Query{Clauses: []full.Clause{
		&full.Match{Patterns: []full.PathPattern{{
			Nodes: []full.NodePattern{{Variable: "i", Label: "identity"}},
		}}},
	}}}
	require.True(t, hasMultiBindingConjunctUnit(cr))
}

// TestHasMultiBindingConjunctUnit_FailClosed_UnrecognisedExprNode documents
// (rather than exercises) the "unrecognised expression form → validate"
// fail-closed rule: full.Expr's isExpr() marker method is unexported, so a
// test in this DIFFERENT package cannot implement full.Expr to construct a
// genuinely-foreign node type — cross-package unknown-node injection isn't
// constructible today. The `default:` branches in collectBindingsInto,
// findMapUnits, and propertyChainRoot all return unknown=true (validate) for
// exactly this case; they are covered indirectly by every real spec never
// tripping them (a regression there would need a NEW full.Expr
// implementation, at which point this comment is the pointer to update).
func TestHasMultiBindingConjunctUnit_FailClosed_UnrecognisedExprNode_NotConstructible(t *testing.T) {
	t.Skip("full.Expr.isExpr() is unexported — a foreign implementation cannot be constructed from outside package full; see doc comment")
}

// TestHasMultiBindingConjunctUnit_ParamOnlyTuple_NotMultiBinding confirms
// literals/params never inflate the count: zero bindings is not "≥2
// bindings", so a tuple whose only column is a $param must NOT validate.
func TestHasMultiBindingConjunctUnit_ParamOnlyTuple_NotMultiBinding(t *testing.T) {
	cr := &full.CompiledRule{Query: &full.Query{Clauses: []full.Clause{
		&full.Return{Items: []full.ProjectionItem{
			{Expr: &full.ParameterRef{Name: "now"}, Alias: "ts"},
		}},
	}}}
	require.False(t, hasMultiBindingConjunctUnit(cr))
}

// TestHasMultiBindingConjunctUnit_MultipleReturnBranches_UnionsVerdicts pins
// the defensive multi-RETURN-branch rule (engine UNION, not reachable
// through today's grammar, but specified defensively): classify each branch
// and OR the verdicts together.
func TestHasMultiBindingConjunctUnit_MultipleReturnBranches_UnionsVerdicts(t *testing.T) {
	paramOnly := &full.Return{Items: []full.ProjectionItem{
		{Expr: &full.ParameterRef{Name: "now"}, Alias: "ts"},
	}}
	multiBinding := &full.Return{Items: []full.ProjectionItem{
		{Expr: &full.VariableRef{Name: "a"}, Alias: "a"},
		{Expr: &full.VariableRef{Name: "b"}, Alias: "b"},
	}}

	t.Run("one branch multi-binding", func(t *testing.T) {
		cr := &full.CompiledRule{Query: &full.Query{Clauses: []full.Clause{paramOnly, multiBinding}}}
		require.True(t, hasMultiBindingConjunctUnit(cr))
	})
	t.Run("neither branch multi-binding", func(t *testing.T) {
		cr := &full.CompiledRule{Query: &full.Query{Clauses: []full.Clause{paramOnly, paramOnly}}}
		require.False(t, hasMultiBindingConjunctUnit(cr))
	})
}
