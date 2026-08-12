package full

// A stage of sibling OPTIONAL MATCH branches off one anchor is evaluated as
// their CROSS PRODUCT: executor.run threads one []binding slice through the
// clause list and applyMatch expands EVERY inbound row through the next
// clause's patterns, so N sibling branches off one identity materialize
// Π fan-out_i rows to build lists that each read exactly one branch.
//
// This file's analysis names, once at Parse, the branches whose variables reach
// the projection ONLY through a multiplicity-insensitive aggregator over that
// one branch. projectItems evaluates those against the base row set separately,
// folding each into its own aggregator, so peak rows fall from the product of
// the branches to the largest single branch.
//
// # "branch" here, and "branch" in branchplan.go
//
// branchplan.go and pipeline/branchmerge.go own the word "branch" for a
// DIFFERENT thing: the N independently-compiled SPEC branches of a multi-walk
// Personal lens (LensSpec.SpecBranches), each its own whole cypher, merged at
// the pipeline. A "branch group" here is entirely within ONE cypher: a sibling
// OPTIONAL MATCH clause of one stage, plus every later clause of that stage
// that continues off a variable it introduced. The two never interact — a spec
// branch is compiled and executed on its own, and this analysis runs inside
// each of them.
//
// # The stage, the group, the pinned frontier
//
// A STAGE is the clauses between two WITHs (or between the last WITH and
// RETURN), together with the aliases the previous WITH carried in. Its BASE is
// those carried aliases plus everything its required MATCHes bind.
//
// A branch GROUP is one OPTIONAL MATCH clause whose pattern references only
// base variables, plus every later OPTIONAL MATCH clause of the stage that
// references a variable the group introduced. Groups are disjoint, and a group
// is a TREE, not a list: one branch off the anchor can carry several
// independent subtrees hanging off the variable IT introduced.
//
// A group's PINNED FRONTIER is the clauses whose variables a NON-aggregating
// projection item reads, together with every clause on the path from the group
// root down to one of them. A non-aggregating item is a grouping term
// (projectItems' key loop), so a row per binding is the intended output
// cardinality there and the clause has to stay in the product. Everything BELOW
// the frontier is judged on its own — which is what makes the widest stages in
// the corpus earn anything at all: their group ROOTS are 0-or-1 bindings a
// column projects by key, and the fan-out lives in the subtrees below them, so
// a whole-group verdict would deliver nothing exactly where it is needed.
//
// # Why the multiplicity precondition is GLOBAL
//
// §4.2 is a stage-level refusal, not a per-group test. An UNREFERENCED branch
// still inflates every OTHER branch's multiplicity-sensitive aggregator: a
// non-DISTINCT `count(t.key)` over one branch of a four-branch stage counts
// |t| × |a| × |cond| × |insp|, and a live lens in the corpus does exactly that
// — correctly, but only because three of those bind 0-or-1 by domain, a fact no
// compile-time walk can know. Folding the unreferenced branches away would
// silently change that count. So ONE non-DISTINCT collect/count anywhere in the
// stage's projection keeps the whole stage on today's path.
//
// # Why a cross-group reference refuses the stage
//
// A clause whose pattern or WHERE reaches into two groups is refused outright,
// and that refusal is what keeps ReferencedLabels (labels.go) sound. Its
// optional-label scope accumulates in CLAUSE ORDER and excuses an unlabeled
// sighting that FOLLOWS it, because a clause's paths thread into one binding
// stream. Decomposition splits that stream per branch group — so a later
// group's unlabeled node re-referencing an earlier group's labeled variable
// would no longer be the same binding at all. That shape is exactly a
// cross-group reference, and it is refused here, so every unlabeled sighting a
// label still excuses is one inside its own group or off the base, where the
// stream is intact. ReferencedLabels needs no change and its corpus census is
// unmoved.
//
// # Why the query's ANCHOR clause is never deferred
//
// An evaluation seeded by one event carries an armed seed anchor
// (seedAnchorFor), and the executor consumes it at the FIRST candidate set it
// builds by scan — which is the query's anchor pattern, the first node of the
// first MATCH clause. Deferring that clause would move the scan behind every
// clause that stayed in the product, and the seed would narrow a DIFFERENT
// pattern: a different candidate set, a different projection.
//
// Every other clause is safe to move, because arming requires the anchor
// pattern to be a labeled node with no `key` property (seedAnchorBinds), and
// the anchor clause therefore always reaches takeSeedAnchor on its own first
// pattern. Holding just that one clause in the product is what makes the armed
// seed land on the same scan under both configurations. In the corpus the
// anchor is always a required MATCH, which is never deferred anyway; the guard
// is for the query whose first clause is an OPTIONAL MATCH.
//
// # Fail-safe
//
// A wrong decomposition is a silently wrong grant document — a collect() short
// by the elements of a branch it stopped feeding, on the security plane a
// revocation that never lands. So every arm defaults to "do not decompose":
// CollectVariableRefs returning unknown, an aggregate expression the fold tree
// does not mirror, a required MATCH after an optional one, a clause spanning
// two groups, an unrecognised aggregator. Each names the shape it could not
// prove rather than returning a bare bool, so a corpus census can pin it.

import (
	"fmt"
	"sort"
	"strings"
)

// branchGroupBase is the routing tag for a fold that reads no deferred branch:
// it is fed the base row once, rather than once per row of any branch.
const branchGroupBase = -1

// Refusal codes. Each is a stable leading token of a stagePlan.Refusal, so a
// corpus census pins WHICH shape stopped the walk without pinning the prose
// around it.
const (
	refuseNoAggregate           = "no-aggregating-item"
	refuseMultiplicitySensitive = "multiplicity-sensitive-aggregator"
	refuseUnmirroredAggregate   = "unmirrored-aggregate-expression"
	refuseUnenumerable          = "unenumerable-expression"
	refuseRequiredAfterOptional = "required-match-after-optional"
	refuseUnanchoredGroupSplit  = "clause-spans-sibling-subtrees"
	refuseForwardReference      = "forward-reference"
)

// deferredBranch is one candidate subtree the executor evaluates against the
// base row set separately: its clauses in source order, applied as a unit.
type deferredBranch struct {
	// clauses are applied to each base row through applyMatch, unchanged — the
	// same OPTIONAL null-binding, the same WHERE handling, the same
	// checkBindings guard the flat path uses.
	clauses []*Match

	// vars are the variables the subtree introduces, sorted. Diagnostic only:
	// routing is by fold stamp, not by name.
	vars []string
}

// stagePlan is one projecting clause's branch-decomposition answer.
type stagePlan struct {
	// Clause is the *With or *Return this plan describes.
	Clause Clause

	// deferred are the candidate subtrees projectItems expands per base row.
	// Empty for a stage that was analysed and earns nothing, which is the same
	// executor path as a refused one.
	deferred []deferredBranch

	// foldGroup stamps each aggregating CALL of the projection with the
	// deferred subtree whose rows feed it, or branchGroupBase. The condition
	// binds at the CALL and not at the item: `collect(DISTINCT A_g1) +
	// collect(DISTINCT B_g2)` is ONE item and TWO calls, and it is the shape
	// every read-grant producer and both orchestration lenses take.
	foldGroup map[*FunctionCall]int

	// Groups is how many sibling branch groups the stage holds, and Optional how
	// many OPTIONAL MATCH clauses. Both are the census's raw material: the
	// design stated "fourteen lenses with two or more sibling groups" by eye,
	// and an eye-census of this shape has already been wrong twice.
	Groups   int
	Optional int

	// Refusal names the shape the walk could not prove, and is "" when the stage
	// was analysed. A refused stage defers nothing, which is exactly what the
	// executor does with no analysis at all.
	Refusal string
}

// BranchStageDecomposition is one projecting clause's decomposition verdict in
// a vocabulary a caller outside this package can act on — the diagnostic form
// of the analysis, the same shape GroupingReduction and ReferencedLabels take
// for their own compile-time walks.
type BranchStageDecomposition struct {
	// Groups is the number of sibling branch groups in the stage.
	Groups int
	// Optional is the number of OPTIONAL MATCH clauses in the stage.
	Optional int
	// Deferred names each candidate subtree the executor folds separately, by
	// its variables comma-joined; the subtrees are ordered by that rendering, so
	// a caller asserting on it reads the same slice on every run.
	Deferred []string
	// Refusal names the shape the walk could not prove, and is "" when it did.
	Refusal string
}

// BranchDecomposition returns the per-stage verdict of the compile-time
// branch-group analysis, in the order the executor applies the projecting
// clauses.
//
// It exists so a corpus census can pin what every shipped lens earns. The
// population of this decomposition was first stated by counting OPTIONAL MATCH
// clauses by eye, which counts CLAUSES where the mechanism turns on GROUPS —
// the widest lens in the corpus has nine optionals and three groups — and a
// census can only be executable if the verdict can be asked for from outside
// the package that computes it.
func (cr *CompiledRule) BranchDecomposition() []BranchStageDecomposition {
	if cr == nil {
		return nil
	}
	plans := analyseBranchGroups(cr.Query)
	out := make([]BranchStageDecomposition, 0, len(plans))
	for _, p := range plans {
		v := BranchStageDecomposition{Groups: p.Groups, Optional: p.Optional, Refusal: p.Refusal}
		for _, d := range p.deferred {
			v.Deferred = append(v.Deferred, strings.Join(d.vars, ","))
		}
		sort.Strings(v.Deferred)
		out = append(out, v)
	}
	return out
}

// analyseBranchDecomposition returns the plan for every stage that defers at
// least one subtree, keyed by the projecting clause the executor holds, plus
// the set of OPTIONAL MATCH clauses run() must SKIP because a plan expands them
// per base row instead.
//
// A query that defers nothing yields two nil maps — which is also what a
// *CompiledRule built by hand carries, so both take the executor's flat path.
//
// Computed once, at Parse, over the query the compiled rule owns; never mutated
// afterwards, so a rule shared across concurrent evaluations stays immutable at
// execute time.
func analyseBranchDecomposition(q *Query) (map[Clause]*stagePlan, map[*Match]struct{}) {
	var stages map[Clause]*stagePlan
	var deferred map[*Match]struct{}
	for _, p := range analyseBranchGroups(q) {
		// Refused OR empty: both are the product path, and the pairing of a
		// refusal WITH a deferred branch is the one combination that would be
		// wrong rather than merely unhelpful — run() would skip a clause whose
		// rows nothing folds. Stated here once, so no arm of the analysis has to
		// be trusted to clear the other field.
		if p.Refusal != "" || len(p.deferred) == 0 {
			continue
		}
		if stages == nil {
			stages = make(map[Clause]*stagePlan)
			deferred = make(map[*Match]struct{})
		}
		stages[p.Clause] = p
		for _, d := range p.deferred {
			for _, m := range d.clauses {
				deferred[m] = struct{}{}
			}
		}
	}
	return stages, deferred
}

// analyseBranchGroups partitions q into stages and judges each one, in the
// order the executor projects them.
//
// The order mirrors ExecuteWithFootprint exactly: MATCH clauses accumulate into
// the stage still open, a WITH closes one where it stands, and the query's LAST
// RETURN closes the final stage over whatever they produced. An earlier RETURN
// is recorded and skipped by the executor — it never touches a binding — so it
// contributes no clause to any stage here either.
func analyseBranchGroups(q *Query) []*stagePlan {
	if q == nil {
		return nil
	}
	lastReturn := -1
	var anchor *Match
	for i, c := range q.Clauses {
		if _, isReturn := c.(*Return); isReturn {
			lastReturn = i
		}
		if m, isMatch := c.(*Match); isMatch && anchor == nil {
			anchor = m
		}
	}

	var plans []*stagePlan
	var pending []*Match
	carried := map[string]struct{}{}

	for _, c := range q.Clauses {
		switch cl := c.(type) {
		case *Match:
			pending = append(pending, cl)
		case *With:
			plans = append(plans, analyseBranchStage(cl, cl.Items, pending, carried, anchor))
			pending = nil
			carried = withCarriedAliases(cl)
		case *Return:
			// Judged below, and only if it is the query's last.
		default:
			// A clause shape this walk has no case for could bind or filter
			// anything. Close the stage against it by dropping the clauses it
			// would have to reason about: an empty pending set defers nothing,
			// which is the flat path.
			pending = nil
			carried = map[string]struct{}{}
		}
	}
	if lastReturn >= 0 {
		r := q.Clauses[lastReturn].(*Return)
		plans = append(plans, analyseBranchStage(r, r.Items, pending, carried, anchor))
	}
	return plans
}

// withCarriedAliases returns the names a WITH's items bind in the next stage.
func withCarriedAliases(w *With) map[string]struct{} {
	out := make(map[string]struct{}, len(w.Items))
	for i, it := range w.Items {
		a := it.Alias
		if a == "" {
			a = projectionAutoAlias(it.Expr, i)
		}
		out[a] = struct{}{}
	}
	return out
}

// branchClause is one OPTIONAL MATCH clause inside a stage's branch forest.
type branchClause struct {
	match *Match
	// vars are the variables this clause introduces (bound by none of the
	// clauses before it, and not carried in).
	vars []string
	// refs are the already-bound variables its patterns and WHERE read.
	refs []string
	// parent indexes the clause that bound the deepest variable this one reads,
	// or -1 when it reads only base variables and so roots a group.
	parent int
	// group indexes the clause at the root of this one's group.
	group int
	// pinned is true when this clause has to stay in the product: a
	// non-aggregating projection item reads its variables or those of a clause
	// below it, or it is the query's anchor clause, whose scan is where an armed
	// seed anchor lands.
	pinned bool
	// candidate indexes the deferred subtree this clause belongs to, or -1.
	candidate int
}

// analyseBranchStage judges one stage: its projecting clause, the MATCH clauses
// that feed it, and the aliases carried in from the previous WITH.
func analyseBranchStage(project Clause, items []ProjectionItem, matches []*Match, carried map[string]struct{}, anchor *Match) *stagePlan {
	plan := &stagePlan{Clause: project}

	bound := make(map[string]struct{}, len(carried))
	for v := range carried {
		bound[v] = struct{}{}
	}

	// The base: every required MATCH's variables. A required MATCH that FOLLOWS
	// an optional one can drop rows the optional produced, which makes the base
	// row set a function of a branch — so the stage is refused rather than
	// decomposed around it.
	var optionals []*Match
	for _, m := range matches {
		if m.Optional {
			optionals = append(optionals, m)
		}
	}
	plan.Optional = len(optionals)

	sawOptional := false
	for _, m := range matches {
		if m.Optional {
			sawOptional = true
			continue
		}
		if sawOptional {
			plan.Refusal = refuseRequiredAfterOptional +
				": a required MATCH stands after an OPTIONAL MATCH, so the base row set depends on a branch"
			return plan
		}
		for _, p := range m.Patterns {
			for _, v := range patternVariables(p) {
				bound[v] = struct{}{}
			}
		}
	}
	if len(optionals) == 0 {
		plan.Refusal = refuseNoAggregate + ": the stage holds no OPTIONAL MATCH clause"
		return plan
	}

	// Which optional clause introduces each name, computed before any of them is
	// judged, so a reference can be told apart from a FORWARD reference.
	//
	// A name a LATER clause binds is the hazard: cypher permits it (Parse runs no
	// scope check and evalExpr answers an unbound variable with nil), the flat
	// path evaluates the reading clause's WHERE against the value the later
	// clause bound, and decomposition would evaluate it against nil. Neither
	// parenting it nor refusing on it would happen if the walk simply skipped
	// names that are not bound YET, which is why the pre-pass exists rather than
	// a bound-check inside the loop below.
	introducedAt := map[string]int{}
	{
		seen := make(map[string]struct{}, len(bound))
		for v := range bound {
			seen[v] = struct{}{}
		}
		for i, m := range optionals {
			for _, p := range m.Patterns {
				for _, v := range patternVariables(p) {
					if _, already := seen[v]; already {
						continue
					}
					seen[v] = struct{}{}
					introducedAt[v] = i
				}
			}
		}
	}

	// The forest is built BEFORE the §4.2 precondition is judged, so a stage the
	// precondition refuses still reports how many sibling groups it holds. That
	// population is the census's whole subject, and its widest refused member is
	// only visible from a stage §4.2 turns away.
	owner := map[string]int{} // variable -> index into clauses, absent = base
	clauses := make([]branchClause, 0, len(optionals))
	for idx, m := range optionals {
		bc := branchClause{match: m, parent: -1, candidate: -1, pinned: m == anchor}
		var newVars []string

		// addRef records one name this clause READS. A name bound before it is a
		// reference the forest parents on; a name a later clause binds refuses the
		// stage; a name no clause of the stage binds at all is either local to the
		// predicate that named it (a PatternExpr's own bindings are discarded
		// rather than threaded into the row) or unbound everywhere — both evaluate
		// identically whichever path runs, so both are ignored.
		forward := ""
		addRef := func(n string) {
			if _, already := bound[n]; already {
				bc.refs = append(bc.refs, n)
				return
			}
			if at, introduced := introducedAt[n]; introduced && at > idx && forward == "" {
				forward = n
			}
		}

		for _, p := range m.Patterns {
			for _, v := range patternVariables(p) {
				if _, already := bound[v]; already {
					bc.refs = append(bc.refs, v)
					continue
				}
				if !containsName(newVars, v) {
					newVars = append(newVars, v)
				}
			}
			for _, e := range patternPropertyExprs(p) {
				names, unknown := CollectVariableRefs(e)
				if unknown {
					plan.Refusal = refuseUnenumerable +
						": an OPTIONAL MATCH pattern property carries an expression shape this walk cannot enumerate the variable references of"
					return plan
				}
				for n := range names {
					addRef(n)
				}
			}
		}
		if m.Where != nil {
			names, unknown := CollectVariableRefs(m.Where)
			if unknown {
				plan.Refusal = refuseUnenumerable +
					": an OPTIONAL MATCH WHERE carries an expression shape this walk cannot enumerate the variable references of"
				return plan
			}
			for n := range names {
				addRef(n)
			}
		}
		if forward != "" {
			plan.Refusal = refuseForwardReference +
				fmt.Sprintf(": the OPTIONAL MATCH reads %q, which a LATER clause of this stage binds", forward)
			return plan
		}
		sort.Strings(newVars)
		bc.vars = newVars
		bc.refs = dedupSorted(bc.refs)

		// Parent = the deepest clause that bound one of the variables this one
		// reads. Every OTHER clause it reads from must be an ancestor of that
		// one, or the clause joins two sibling subtrees and the tree the
		// decomposition rests on does not exist.
		parent := -1
		for _, r := range bc.refs {
			if o, isBranch := owner[r]; isBranch && o > parent {
				parent = o
			}
		}
		for _, r := range bc.refs {
			o, isBranch := owner[r]
			if !isBranch {
				continue // a base variable is bound on every row
			}
			if o != parent && !isAncestor(clauses, o, parent) {
				plan.Refusal = refuseUnanchoredGroupSplit +
					fmt.Sprintf(": the OPTIONAL MATCH reading %q joins two sibling branch subtrees", r)
				return plan
			}
		}
		bc.parent = parent
		if parent < 0 {
			bc.group = len(clauses)
			plan.Groups++
		} else {
			bc.group = clauses[parent].group
		}

		clauses = append(clauses, bc)
		for _, v := range newVars {
			bound[v] = struct{}{}
			owner[v] = idx
		}
	}

	// §4.2 — the multiplicity precondition, judged over the whole stage and
	// before any group is looked at individually.
	calls, refusal := stageAggregatorCalls(items)
	if refusal != "" {
		plan.Refusal = refusal
		return plan
	}
	if len(calls) == 0 {
		// With nothing aggregating, projectItems emits one row per binding and
		// the branch product IS the intended output cardinality.
		plan.Refusal = refuseNoAggregate + ": the stage's projection aggregates nothing"
		return plan
	}

	// §4.3(1) — the pinned frontier. A non-aggregating item's variables stay in
	// the product, and so does every clause on the path down to them.
	for i, it := range items {
		if containsAggregator(it.Expr) {
			continue
		}
		names, unknown := CollectVariableRefs(it.Expr)
		if unknown {
			plan.Refusal = refuseUnenumerable +
				fmt.Sprintf(": non-aggregating item %q carries an expression shape this walk cannot enumerate the variable references of",
					itemAliasAt(items, i))
			return plan
		}
		for n := range names {
			o, isBranch := owner[n]
			if !isBranch {
				continue
			}
			for c := o; c >= 0; c = clauses[c].parent {
				clauses[c].pinned = true
			}
		}
	}

	// The candidates: every maximal unpinned subtree. Ancestor propagation above
	// makes them maximal by construction — a pinned descendant pins its whole
	// path — so no candidate nests inside another.
	descendants := map[int][]int{}
	for i := range clauses {
		if clauses[i].pinned {
			continue
		}
		if clauses[i].parent >= 0 && !clauses[clauses[i].parent].pinned {
			continue // inside another candidate
		}
		cand := len(plan.deferred)
		sub := subtreeOf(clauses, i)
		vars := []string{}
		for _, s := range sub {
			clauses[s].candidate = cand
			vars = append(vars, clauses[s].vars...)
		}
		sort.Strings(vars)
		descendants[cand] = sub
		plan.deferred = append(plan.deferred, deferredBranch{vars: vars})
	}
	if len(plan.deferred) == 0 {
		return plan
	}

	// §4.3(2) — a fold may read ONE candidate. `collect(DISTINCT A_g1) +
	// collect(DISTINCT B_g2)` is one item and two calls, and each call is judged
	// alone; a SINGLE call reading two candidates could be fed from neither
	// branch's rows, so both go back into the product. Dropping a candidate only
	// turns its variables back into product variables, which can never create a
	// new violation, so one pass over the calls settles it.
	//
	// §4.3(3) — a WHERE or later clause reading two groups' variables — is
	// enforced upstream, by the forest itself: a clause reading a variable a
	// branch bound is PARENTED under that branch, so a WHERE that reaches across
	// two sibling subtrees fails the ancestor check and refuses the whole stage
	// (refuseUnanchoredGroupSplit) rather than reaching here.
	callRefs := make([]map[string]bool, len(calls))
	live := make([]bool, len(plan.deferred))
	for i := range live {
		live[i] = true
	}
	touched := func(names map[string]bool) []int {
		var out []int
		for n := range names {
			o, isBranch := owner[n]
			if !isBranch {
				continue
			}
			c := clauses[o].candidate
			if c < 0 || containsInt(out, c) {
				continue
			}
			out = append(out, c)
		}
		return out
	}
	for i, call := range calls {
		names, unknown := CollectVariableRefs(call)
		if unknown {
			// The candidates are already built at this point, so they have to be
			// thrown away with the verdict: a plan carrying both a refusal and a
			// deferred branch would have run() skip a clause no fold ever feeds.
			plan.deferred, plan.foldGroup = nil, nil
			plan.Refusal = refuseUnenumerable +
				fmt.Sprintf(": aggregator %q carries an expression shape this walk cannot enumerate the variable references of", call.Name)
			return plan
		}
		callRefs[i] = names
		if t := touched(names); len(t) > 1 {
			for _, c := range t {
				live[c] = false
			}
		}
	}

	// Re-index the survivors and stamp each fold leaf with the branch whose rows
	// feed it.
	remap := make([]int, len(plan.deferred))
	kept := make([]deferredBranch, 0, len(plan.deferred))
	for c, branch := range plan.deferred {
		if !live[c] {
			remap[c] = -1
			continue
		}
		remap[c] = len(kept)
		for _, s := range descendants[c] {
			branch.clauses = append(branch.clauses, clauses[s].match)
		}
		kept = append(kept, branch)
	}
	plan.deferred = kept
	if len(plan.deferred) == 0 {
		return plan
	}

	plan.foldGroup = make(map[*FunctionCall]int, len(calls))
	for i, call := range calls {
		stamp := branchGroupBase
		for _, c := range touched(callRefs[i]) {
			if remap[c] >= 0 {
				stamp = remap[c]
			}
		}
		plan.foldGroup[call] = stamp
	}
	return plan
}

// stageAggregatorCalls returns every aggregating CALL of the stage's
// projection, or the §4.2 refusal that keeps the whole stage on today's path.
//
// The walk mirrors newAggFold exactly — a FunctionCall folds its own argument,
// a BinaryOp folds each side — because the routed add() descends that same
// tree. An aggregating item of any other shape is refused here rather than
// stamped against a fold tree that does not exist.
func stageAggregatorCalls(items []ProjectionItem) ([]*FunctionCall, string) {
	var calls []*FunctionCall
	for i, it := range items {
		if !containsAggregator(it.Expr) {
			continue
		}
		found, ok := aggregateCallTree(it.Expr)
		if !ok {
			return nil, refuseUnmirroredAggregate +
				fmt.Sprintf(": item %q composes aggregators in a shape the fold tree does not mirror", itemAliasAt(items, i))
		}
		for _, c := range found {
			if reason := multiplicityInsensitive(c); reason != "" {
				return nil, reason
			}
		}
		calls = append(calls, found...)
	}
	return calls, ""
}

// aggregateCallTree returns the aggregating calls of one projection item, in
// the shape newAggFold builds a fold tree from, and false for any other shape.
func aggregateCallTree(e Expr) ([]*FunctionCall, bool) {
	switch x := e.(type) {
	case *FunctionCall:
		if aggregatorName(x.Name) == "" {
			return nil, false
		}
		return []*FunctionCall{x}, true
	case *BinaryOp:
		left, ok := aggregateCallTree(x.Left)
		if !ok {
			return nil, false
		}
		right, ok := aggregateCallTree(x.Right)
		if !ok {
			return nil, false
		}
		return append(left, right...), true
	}
	return nil, false
}

// multiplicityInsensitive returns "" when c's value is a function of the SET of
// argument values its rows produce, and the §4.2 refusal otherwise.
//
//	collect(DISTINCT x) — set-valued, first-occurrence order preserved
//	count(DISTINCT x)   — cardinality of that set
//	max(x) / min(x)     — the extremum of a multiset is the extremum of its set
//	collect(x)          — the list LENGTH is the multiplicity
//	count(x)            — the count IS the multiplicity
func multiplicityInsensitive(c *FunctionCall) string {
	switch aggregatorName(c.Name) {
	case aggNameMax, aggNameMin:
		return ""
	case aggNameCollect, aggNameCount:
		if c.Distinct {
			return ""
		}
		return refuseMultiplicitySensitive +
			fmt.Sprintf(": %q without DISTINCT reads the branch product's multiplicity", strings.ToLower(c.Name))
	}
	return refuseUnmirroredAggregate +
		fmt.Sprintf(": aggregator %q is one newAggFold rejects at runtime", c.Name)
}

// patternVariables returns every variable a path pattern binds, node and
// relationship alike, in pattern order.
func patternVariables(p PathPattern) []string {
	out := make([]string, 0, len(p.Nodes)+len(p.Rels))
	for i, n := range p.Nodes {
		if n.Variable != "" {
			out = append(out, n.Variable)
		}
		if i < len(p.Rels) && p.Rels[i].Variable != "" {
			out = append(out, p.Rels[i].Variable)
		}
	}
	return out
}

// subtreeOf returns root and every clause below it, in source order.
func subtreeOf(clauses []branchClause, root int) []int {
	out := []int{root}
	for i := root + 1; i < len(clauses); i++ {
		if containsInt(out, clauses[i].parent) {
			out = append(out, i)
		}
	}
	return out
}

// isAncestor reports whether a is on the parent chain above d.
func isAncestor(clauses []branchClause, a, d int) bool {
	for c := d; c >= 0; c = clauses[c].parent {
		if c == a {
			return true
		}
	}
	return false
}

// itemAliasAt returns the effective alias of one projection item.
func itemAliasAt(items []ProjectionItem, i int) string {
	if items[i].Alias != "" {
		return items[i].Alias
	}
	return projectionAutoAlias(items[i].Expr, i)
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func containsInt(xs []int, want int) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func dedupSorted(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	out := names[:1]
	for _, n := range names[1:] {
		if n != out[len(out)-1] {
			out = append(out, n)
		}
	}
	return out
}
