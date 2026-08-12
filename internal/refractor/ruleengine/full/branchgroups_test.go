// The analysis's refusal arms, one per §4.2/§4.3 clause.
//
// Every refusal here is paired with its POSITIVE VECTOR: the same query with
// only the refusing element removed, asserted to decompose. Without that pair a
// refusal test pins nothing — an analysis that refused everything, or one that
// could not see the shape at all, would pass it just as well.
package full

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// branchPlans parses body and returns the per-stage branch-group analysis.
func branchPlans(t *testing.T, body string) []*stagePlan {
	t.Helper()
	cr, err := New().Parse(body)
	require.NoErrorf(t, err, "must parse:\n%s", body)
	return analyseBranchGroups(cr.(*CompiledRule).Query)
}

// lastPlan is the analysis for the query's RETURN stage, which is where every
// query in this file does its work.
func lastPlan(t *testing.T, body string) *stagePlan {
	t.Helper()
	plans := branchPlans(t, body)
	require.NotEmpty(t, plans)
	return plans[len(plans)-1]
}

// deferredVars renders a plan's deferred subtrees as their variable sets, so an
// assertion reads the shape rather than an index.
func deferredVars(p *stagePlan) []string {
	out := make([]string, 0, len(p.deferred))
	for _, d := range p.deferred {
		out = append(out, strings.Join(d.vars, ","))
	}
	return out
}

// twoBranchBody is the base shape every refusal below mutates: two independent
// OPTIONAL branches off one anchor, each read only through its own
// collect(DISTINCT …).
const twoBranchBody = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN identity.key AS actorKey,
  collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT bk.key) AS bookings`

// TestBranchGroups_TwoIndependentBranchesFold is the positive vector every
// refusal test below is measured against.
func TestBranchGroups_TwoIndependentBranchesFold(t *testing.T) {
	p := lastPlan(t, twoBranchBody)
	require.Empty(t, p.Refusal)
	require.Equal(t, 2, p.Groups)
	require.Equal(t, []string{"task", "bk"}, deferredVars(p))
}

// TestBranchGroups_NonDistinctAggregatorRefusesTheWholeStage is §4.2. The
// non-DISTINCT count reads only ONE branch, and the branch it does not read is
// still refused — because folding that branch away would change this count's
// value, which is exactly what makes the precondition global rather than per
// group. clauseSatisfaction is the live lens of this shape.
func TestBranchGroups_NonDistinctAggregatorRefusesTheWholeStage(t *testing.T) {
	refused := lastPlan(t, twoBranchBody+`,
  count(bk.key) AS bookingCount`)
	require.Truef(t, strings.HasPrefix(refused.Refusal, refuseMultiplicitySensitive),
		"want the §4.2 refusal, got %q", refused.Refusal)
	require.Empty(t, refused.deferred)
	require.Equal(t, 2, refused.Groups,
		"a refused stage must still report the sibling groups it holds — the census reads that number")

	// The positive vector: DISTINCT is the only thing that changed.
	allowed := lastPlan(t, twoBranchBody+`,
  count(DISTINCT bk.key) AS bookingCount`)
	require.Empty(t, allowed.Refusal)
	require.Equal(t, []string{"task", "bk"}, deferredVars(allowed))
}

// TestBranchGroups_NonAggregatingItemPinsItsOwnBranchOnly is §4.3(1): a
// grouping term over a branch means a row per binding is the intended output
// cardinality there, so that branch stays in the product — and only that one.
func TestBranchGroups_NonAggregatingItemPinsItsOwnBranchOnly(t *testing.T) {
	p := lastPlan(t, twoBranchBody+`,
  task.key AS taskKey`)
	require.Empty(t, p.Refusal)
	require.Equal(t, 2, p.Groups)
	require.Equal(t, []string{"bk"}, deferredVars(p),
		"the pinned branch stays in the product; its sibling still folds")

	// The positive vector is TestBranchGroups_TwoIndependentBranchesFold: the
	// same query without the grouping term folds both.
}

// TestBranchGroups_OneCallSpanningTwoBranchesRefusesBoth is §4.3(2), and the
// reason the condition binds at the aggregator CALL rather than the projection
// item: a single call reading two branches could be fed from neither branch's
// rows alone.
func TestBranchGroups_OneCallSpanningTwoBranchesRefusesBoth(t *testing.T) {
	spanning := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN identity.key AS actorKey,
  collect(DISTINCT {t: task.key, b: bk.key}) AS both`)
	require.Empty(t, spanning.Refusal, "the stage is analysable; it is the CALL that spans")
	require.Equal(t, 2, spanning.Groups)
	require.Empty(t, spanning.deferred, "a call reading two branches puts both back in the product")

	// The positive vector: the SAME two branches, the same one projection item,
	// split into two calls — the composed shape every read-grant producer and
	// both orchestration lenses take.
	split := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN identity.key AS actorKey,
  collect(DISTINCT {t: task.key}) + collect(DISTINCT {b: bk.key}) AS both`)
	require.Empty(t, split.Refusal)
	require.Equal(t, []string{"task", "bk"}, deferredVars(split))
	require.Len(t, split.foldGroup, 2, "one stamp per call, not per item")
}

// TestBranchGroups_ClauseReadingTwoSiblingsRefusesTheStage is §4.3(3). A clause
// whose WHERE reads ONE branch is parented under it and shares its binding
// stream; a clause whose WHERE reads TWO SIBLING branches has no such stream to
// belong to, and the whole stage falls back to the product.
func TestBranchGroups_ClauseReadingTwoSiblingsRefusesTheStage(t *testing.T) {
	refused := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
  WHERE inst.key <> task.key AND inst.key <> bk.key
RETURN identity.key AS actorKey,
  collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT bk.key) AS bookings,
  collect(DISTINCT inst.key) AS instances`)
	require.Truef(t, strings.HasPrefix(refused.Refusal, refuseUnanchoredGroupSplit),
		"want the sibling-spanning refusal, got %q", refused.Refusal)
	require.Empty(t, refused.deferred)

	// The positive vector: the same WHERE, reading only ONE branch. `inst` is
	// then a child of the task branch and folds inside it.
	allowed := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
OPTIONAL MATCH (identity)<-[:providedTo]-(inst:service)
  WHERE inst.key <> task.key
RETURN identity.key AS actorKey,
  collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT bk.key) AS bookings,
  collect(DISTINCT inst.key) AS instances`)
	require.Empty(t, allowed.Refusal)
	require.Equal(t, []string{"inst,task", "bk"}, deferredVars(allowed),
		"a clause reading one branch joins that branch's own subtree")
}

// TestBranchGroups_UnenumerableItemRefusesTheStage is §4.3(4): the analysis is
// CollectVariableRefs' second consumer and inherits its default-deny arm.
// `head(xs).key` is a property access on a function call, a chain
// variableRefChainRoot cannot trace a binding through, and it is reachable from
// cypher an author can really write.
func TestBranchGroups_UnenumerableItemRefusesTheStage(t *testing.T) {
	const body = `
MATCH (n:task)
WITH n, collect(DISTINCT n.key) AS xs
OPTIONAL MATCH (n)-[:forOperation]->(op)
OPTIONAL MATCH (n)-[:scopedTo]->(tgt)
RETURN %s AS z,
  collect(DISTINCT op.key) AS ops,
  collect(DISTINCT tgt.key) AS tgts`

	refused := lastPlan(t, strings.Replace(body, "%s", "head(xs).key", 1))
	require.Truef(t, strings.HasPrefix(refused.Refusal, refuseUnenumerable),
		"want the unenumerable refusal, got %q", refused.Refusal)
	require.Empty(t, refused.deferred)

	// The positive vector: the same column, carried as a bare reference the walk
	// CAN enumerate.
	allowed := lastPlan(t, strings.Replace(body, "%s", "xs", 1))
	require.Empty(t, allowed.Refusal)
	require.Equal(t, []string{"op", "tgt"}, deferredVars(allowed))
}

// TestBranchGroups_RequiredMatchAfterOptionalRefusesTheStage: a required MATCH
// standing after an optional one can DROP rows the optional produced, so the
// base row set would be a function of a branch — the one thing decomposition
// assumes it is not.
func TestBranchGroups_RequiredMatchAfterOptionalRefusesTheStage(t *testing.T) {
	refused := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
MATCH (identity)-[:holdsRole]->(role:role)
RETURN identity.key AS actorKey,
  collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT role.key) AS roles`)
	require.Truef(t, strings.HasPrefix(refused.Refusal, refuseRequiredAfterOptional),
		"want the ordering refusal, got %q", refused.Refusal)
	require.Empty(t, refused.deferred)

	// The positive vector: the same two clauses, required one first.
	allowed := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
RETURN identity.key AS actorKey,
  collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT role.key) AS roles`)
	require.Empty(t, allowed.Refusal)
	require.Equal(t, []string{"task"}, deferredVars(allowed))
}

// TestBranchGroups_NoAggregatorMeansNoDecomposition: with nothing aggregating,
// projectItems emits one row per binding and the branch product IS the intended
// output cardinality. Folding a branch away there would change the row count.
func TestBranchGroups_NoAggregatorMeansNoDecomposition(t *testing.T) {
	p := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN identity.key AS actorKey, task.key AS taskKey, bk.key AS bookingKey`)
	require.Truef(t, strings.HasPrefix(p.Refusal, refuseNoAggregate),
		"want the no-aggregator refusal, got %q", p.Refusal)
	require.Empty(t, p.deferred)
	require.Equal(t, 2, p.Groups)
}

// TestBranchGroups_PinnedFrontierFoldsTheSubtreeBelowIt is §4.1's load-bearing
// case, in leaseApplicationComplete's shape: `id` is pinned by `id.key AS
// applicant`, and the subtree hanging BELOW it is where the fan-out lives. A
// whole-group verdict would have declared this stage non-foldable and delivered
// nothing.
func TestBranchGroups_PinnedFrontierFoldsTheSubtreeBelowIt(t *testing.T) {
	p := lastPlan(t, `
MATCH (app:leaseapp {key: $actorKey})
OPTIONAL MATCH (app)-[:applicationFor]->(id:identity)
OPTIONAL MATCH (id)<-[:providedTo]-(inst:service)
OPTIONAL MATCH (id)<-[:scopedTo]-(onbTask:task)
OPTIONAL MATCH (onbTask)-[:forOperation]->(onbOp)
RETURN app.key AS entityKey,
  id.key AS applicant,
  count(DISTINCT inst.key) AS instances,
  count(DISTINCT onbOp.key) AS onbOps`)
	require.Empty(t, p.Refusal)
	require.Equal(t, 1, p.Groups, "one group: every clause continues off `id`")
	require.Equal(t, []string{"inst", "onbOp,onbTask"}, deferredVars(p),
		"the pinned root stays in the product and each subtree below it folds on its own")
}

// TestBranchGroups_ReReferenceKeepsOneBindingStream is the label-derivation
// obligation, discharged as a property of the analysis rather than as a claim.
//
// ReferencedLabels accumulates an OPTIONAL MATCH's labels in CLAUSE ORDER and
// lets them excuse an unlabeled sighting that FOLLOWS, sound because a clause's
// paths thread into ONE binding stream. Decomposition splits that stream per
// branch — so the two shapes that could break it must be impossible:
//
//  1. a later clause RE-REFERENCING an earlier optional's variable lands in that
//     branch's OWN subtree, one applyMatch chain, one stream;
//  2. a later clause reaching across two SIBLING branches refuses the stage
//     outright (TestBranchGroups_ClauseReadingTwoSiblingsRefusesTheStage).
//
// So every unlabeled sighting a label still excuses is inside its own branch or
// off the base, where the stream is intact, and labels.go needs no change.
func TestBranchGroups_ReReferenceKeepsOneBindingStream(t *testing.T) {
	// The third clause's `(role)` is UNLABELED and exhaustive only because the
	// second clause's `role:role` excuses it — the precise dependence the
	// obligation is about.
	const body = `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)
OPTIONAL MATCH (identity)-[:delegatesRole]->(role)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN identity.key AS actorKey,
  collect(DISTINCT role.key) AS roles,
  collect(DISTINCT bk.key) AS bookings`
	p := lastPlan(t, body)
	require.Empty(t, p.Refusal)
	require.Equal(t, []string{"role", "bk"}, deferredVars(p))
	require.Len(t, p.deferred[0].clauses, 2,
		"the re-reference and the clause it re-references land in ONE subtree — the same binding "+
			"stream ReferencedLabels' optional-label scope assumes, applied as one applyMatch chain")

	// And the label verdict is what it always was: an unlabeled re-reference
	// inside the branch that labeled the variable stays exhaustive.
	cr, err := New().Parse(body)
	require.NoError(t, err)
	labels, exhaustive := cr.(*CompiledRule).ReferencedLabels()
	require.True(t, exhaustive)
	require.Equal(t, map[string]struct{}{
		"identity": {}, "role": {}, "booking": {},
	}, labels)
}

// TestBranchDecomposition_AccessorMirrorsThePlan pins the public diagnostic
// accessor against the analysis it reports, since the corpus census reads the
// corpus through it and nothing else.
func TestBranchDecomposition_AccessorMirrorsThePlan(t *testing.T) {
	cr, err := New().Parse(twoBranchBody)
	require.NoError(t, err)
	stages := cr.(*CompiledRule).BranchDecomposition()
	require.Len(t, stages, 1)
	require.Equal(t, BranchStageDecomposition{
		Groups: 2, Optional: 2, Deferred: []string{"bk", "task"},
	}, stages[0])
}

// TestBranchDecomposition_HandBuiltRuleTakesTheProductPath: a *CompiledRule
// constructed directly, as several tests in this package and every pre-Parse
// caller do, carries no analysis and must therefore evaluate exactly as it
// always did.
func TestBranchDecomposition_HandBuiltRuleTakesTheProductPath(t *testing.T) {
	cr := &CompiledRule{Query: &Query{}}
	require.Nil(t, cr.branchStages)
	require.Nil(t, cr.branchDeferred)
	require.Empty(t, cr.BranchDecomposition())
}

// TestBranchGroups_AnchorClauseIsNeverDeferred: an evaluation seeded by one
// event consumes its armed seed anchor at the FIRST candidate set built by scan
// — the query's anchor pattern. Deferring that clause would move the scan
// behind every clause that stayed in the product and narrow a different
// pattern, so the anchor clause stays put even when nothing else pins it.
//
// The corpus never reaches this: every installed lens opens on a required
// MATCH, which is never deferred anyway. The guard is for the query whose first
// clause is an OPTIONAL MATCH — the shape seedAnchorFor still arms, because
// anchorPattern reads the first MATCH clause of any kind.
func TestBranchGroups_AnchorClauseIsNeverDeferred(t *testing.T) {
	p := lastPlan(t, `
OPTIONAL MATCH (task:task)
OPTIONAL MATCH (bk:booking)
RETURN collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT bk.key) AS bookings`)
	require.Empty(t, p.Refusal)
	require.Equal(t, 2, p.Groups)
	require.Equal(t, []string{"bk"}, deferredVars(p),
		"the anchor clause stays in the product; its sibling still folds")

	// The positive vector: give the query a required anchor MATCH and the same
	// two optional clauses both fold, because neither is the anchor any more.
	anchored := lastPlan(t, `
MATCH (identity:identity {key: $actorKey})
OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
OPTIONAL MATCH (identity)<-[:bookedBy]-(bk:booking)
RETURN collect(DISTINCT task.key) AS tasks,
  collect(DISTINCT bk.key) AS bookings`)
	require.Equal(t, []string{"task", "bk"}, deferredVars(anchored))
}

// TestBranchGroups_UnenumerableAggregatorArgumentRefusesAfterCandidatesExist is
// the refusal arm that fires AFTER the candidate subtrees have been built: the
// fold-tree shape is fine and the aggregator is multiplicity-insensitive, so
// §4.2 admits it, and only the argument's own expression defeats the reference
// walk. The plan must come back carrying no deferred branch at all — one that
// carried both a refusal and a branch would have run() skip a clause no fold
// ever feeds, which is a projection short by that branch's whole contribution.
func TestBranchGroups_UnenumerableAggregatorArgumentRefusesAfterCandidatesExist(t *testing.T) {
	const body = `
MATCH (n:task)
WITH n, collect(DISTINCT n.key) AS xs
OPTIONAL MATCH (n)-[:forOperation]->(op)
OPTIONAL MATCH (n)-[:scopedTo]->(tgt)
RETURN n.key AS taskKey,
  collect(DISTINCT %s) AS ops,
  collect(DISTINCT tgt.key) AS tgts`

	refused := lastPlan(t, strings.Replace(body, "%s", "head(xs).key", 1))
	require.Truef(t, strings.HasPrefix(refused.Refusal, refuseUnenumerable),
		"want the unenumerable refusal, got %q", refused.Refusal)
	require.Empty(t, refused.deferred, "a refused stage must defer nothing")
	require.Empty(t, refused.foldGroup)

	cr, err := New().Parse(strings.Replace(body, "%s", "head(xs).key", 1))
	require.NoError(t, err)
	require.Empty(t, cr.(*CompiledRule).branchDeferred,
		"no clause may be skipped by run() for a stage the analysis refused")

	// The positive vector: the same two branches, the same two calls, an
	// argument the walk can enumerate.
	allowed := lastPlan(t, strings.Replace(body, "%s", "op.key", 1))
	require.Empty(t, allowed.Refusal)
	require.Equal(t, []string{"op", "tgt"}, deferredVars(allowed))
}
