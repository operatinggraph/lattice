package full

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeUnknownExpr is a full.Expr this package's walkers were never updated
// for — used to exercise the fail-closed unknown=true path.
type fakeUnknownExpr struct{}

func (fakeUnknownExpr) isExpr() {}

func TestCollectVariableRefs_VariableRef(t *testing.T) {
	names, unknown := CollectVariableRefs(&VariableRef{Name: "svc"})
	require.False(t, unknown)
	require.Equal(t, map[string]bool{"svc": true}, names)
}

func TestCollectVariableRefs_PropertyAccessChain(t *testing.T) {
	names, unknown := CollectVariableRefs(&PropertyAccess{
		Target: &VariableRef{Name: "anchor"},
		Key:    "title",
	})
	require.False(t, unknown)
	require.Equal(t, map[string]bool{"anchor": true}, names)
}

func TestCollectVariableRefs_LiteralAndParameterContributeNothing(t *testing.T) {
	names, unknown := CollectVariableRefs(&BinaryOp{
		Op:    "+",
		Left:  &Literal{Value: "x"},
		Right: &ParameterRef{Name: "now"},
	})
	require.False(t, unknown)
	require.Empty(t, names)
}

func TestCollectVariableRefs_BinaryOpUnionsBothSides(t *testing.T) {
	names, unknown := CollectVariableRefs(&BinaryOp{
		Op:    "+",
		Left:  &VariableRef{Name: "svc"},
		Right: &VariableRef{Name: "role"},
	})
	require.False(t, unknown)
	require.Equal(t, map[string]bool{"svc": true, "role": true}, names)
}

func TestCollectVariableRefs_UnrecognisedFormFailsClosed(t *testing.T) {
	names, unknown := CollectVariableRefs(fakeUnknownExpr{})
	require.True(t, unknown)
	require.Empty(t, names)
}

// twoWalkBranches compiles two independent per-walk queries sharing one
// RETURN tail — the shape composeDataLensSpec emits for a 2-entry Walks
// lens (refractor-shared-keyspace-arbitration-design.md §13.2): branch 0
// owns `svc`, branch 1 owns `role`, both share `actor`/`anchor`.
func twoWalkBranches(t *testing.T, tail string) []*CompiledRule {
	t.Helper()
	eng := New()
	q0 := `MATCH (actor:identity {key: $actorKey})-[:hasService]->(svc:service)-[:for]->(anchor:op) ` + tail
	q1 := `MATCH (actor:identity {key: $actorKey})-[:hasRole]->(role:role)-[:for]->(anchor:op) ` + tail
	cr0, err := eng.Parse(q0)
	require.NoError(t, err)
	cr1, err := eng.Parse(q1)
	require.NoError(t, err)
	return []*CompiledRule{cr0.(*CompiledRule), cr1.(*CompiledRule)}
}

func TestClassifyBranchReturnColumns_WalkOwnedAndAnchorDerived(t *testing.T) {
	branches := twoWalkBranches(t,
		`RETURN anchor.key AS anchorKey, svc.name AS viaServices, role.name AS viaRoles, anchor.title AS title`)
	plan, err := ClassifyBranchReturnColumns(branches)
	require.NoError(t, err)
	require.Len(t, plan, 4)

	byAlias := map[string]ReturnColumnPlan{}
	for _, p := range plan {
		byAlias[p.Alias] = p
	}
	require.Equal(t, ColumnAnchorDerived, byAlias["anchorKey"].Ownership)
	require.Equal(t, ColumnAnchorDerived, byAlias["title"].Ownership)
	require.Equal(t, ColumnWalkOwned, byAlias["viaServices"].Ownership)
	require.Equal(t, 0, byAlias["viaServices"].OwnerBranch)
	require.Equal(t, ColumnWalkOwned, byAlias["viaRoles"].Ownership)
	require.Equal(t, 1, byAlias["viaRoles"].OwnerBranch)
}

// TestClassifyBranchReturnColumns_PatternComprehensionLocalVarIsNotADependency
// probes a shape twoWalkBranches's shared literal tail cannot express: an
// anchor-derived column computed via a pattern comprehension whose own
// pattern introduces a fresh, comprehension-local node (`psvc`, never bound
// by EITHER branch's own MATCH/OPTIONAL MATCH clauses — unlike `svc`, which
// twoWalkBranches' branch 0 already binds as its real walk variable)
// alongside the real dependency (`anchor`, common to every branch) — the
// shape packages/edge-manifest's catalog unification needs for
// `viaServices` (refractor-shared-keyspace-arbitration-design.md §13.7
// build order (c)). Before the fix, `psvc` was indistinguishable from a
// genuine cross-walk variable and refused the column as ambiguous.
func TestClassifyBranchReturnColumns_PatternComprehensionLocalVarIsNotADependency(t *testing.T) {
	branches := twoWalkBranches(t,
		`RETURN anchor.key AS anchorKey, [(anchor)<-[:offers]-(psvc:service) | psvc.key] AS viaServices, role.name AS viaRole`)
	plan, err := ClassifyBranchReturnColumns(branches)
	require.NoError(t, err)

	byAlias := map[string]ReturnColumnPlan{}
	for _, p := range plan {
		byAlias[p.Alias] = p
	}
	require.Equal(t, ColumnAnchorDerived, byAlias["anchorKey"].Ownership)
	require.Equal(t, ColumnAnchorDerived, byAlias["viaServices"].Ownership)
	require.Equal(t, ColumnWalkOwned, byAlias["viaRole"].Ownership)
	require.Equal(t, 1, byAlias["viaRole"].OwnerBranch)
}

func TestClassifyBranchReturnColumns_MixedWalkVarsRefused(t *testing.T) {
	branches := twoWalkBranches(t,
		`RETURN anchor.key AS anchorKey, svc.name + role.name AS combined`)
	_, err := ClassifyBranchReturnColumns(branches)
	require.Error(t, err)
	require.Contains(t, err.Error(), `"combined"`)
	require.Contains(t, err.Error(), "more than one walk")
}

func TestClassifyBranchReturnColumns_MismatchedAliasListsRefused(t *testing.T) {
	eng := New()
	cr0, err := eng.Parse(`MATCH (actor:identity {key: $actorKey})-[:hasService]->(svc:service)-[:for]->(anchor:op) RETURN anchor.key AS anchorKey, svc.name AS viaServices`)
	require.NoError(t, err)
	cr1, err := eng.Parse(`MATCH (actor:identity {key: $actorKey})-[:hasRole]->(role:role)-[:for]->(anchor:op) RETURN anchor.key AS anchorKey, role.name AS viaRoles`)
	require.NoError(t, err)

	_, err = ClassifyBranchReturnColumns(
		[]*CompiledRule{cr0.(*CompiledRule), cr1.(*CompiledRule)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match branch 0's")
}

func TestClassifyBranchReturnColumns_NeedsAtLeastTwoBranches(t *testing.T) {
	branches := twoWalkBranches(t, `RETURN anchor.key AS anchorKey`)
	_, err := ClassifyBranchReturnColumns(branches[:1])
	require.Error(t, err)
	require.Contains(t, err.Error(), "at least 2 branches")
}
