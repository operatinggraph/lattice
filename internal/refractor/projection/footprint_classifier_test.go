package projection

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
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
