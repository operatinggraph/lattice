package full

// The grouping branch of executor.projectItems partitions its input rows by
// RENDERING every non-aggregating item of the clause, once per row, into the
// row's grouping key. An accumulator a clause carries forward — the
// `WITH identity, grantSlice0, collect(DISTINCT {…}) AS grantSlice1` shape a
// generated read-grant producer emits per walk — is therefore re-rendered once
// per row of the stage still running, and normalizeForKey walks a collected
// list element by element. That is a term proportional to the size of the
// result already accumulated times the row count of the stage still running.
//
// Every one of those renderings is the same value: the clause that produced
// `grantSlice0` emitted exactly one row per group, so `grantSlice0` is a
// FUNCTION of that clause's grouping key. Where the whole of that key is still
// in this clause's key, the accumulator can neither split a group nor merge
// two — it has no discriminating power at all, and rendering it is pure cost.
//
// analyseGroupingRedundancy names exactly those items, per clause, once at
// Parse. The executor still EVALUATES a redundant item — its value is what the
// group row projects, and evaluating it is what keeps this evaluation's
// read-surface footprint bit-identical — and only skips the key fragment.
//
// Bit-identical, and specifically not SMALLER, is the constraint. An auth-plane
// caller validates an evaluation by re-reading the keys its footprint NAMES
// (pipeline.footprintValid), with no assertion that the footprint covered
// everything it should have. So a footprint that lost an entry does not
// mismatch — it validates less and passes, and the drift it can no longer see
// is a cap-read slice assembled across two instants and then trusted. Skipping
// the evaluation of a redundant item is therefore not a harmless shortcut with
// a visible cost; it is a silent reduction in what the auth plane checks.
//
// # The claim
//
// Let W_j be a projecting clause with non-aggregating alias set K_j and
// aggregating alias set A_j, and W_k (k > j) a later projecting clause. If an
// alias a ∈ A_j appears in W_k as a BARE CARRY — a ProjectionItem whose Expr is
// *VariableRef{Name: a} and whose effective alias is also a — and every alias
// of W_j's effective key is likewise a bare carry in W_k, then removing a from
// W_k's grouping key leaves the partition of W_k's input rows unchanged.
//
// W_j emits one row per group, so a = f(K_j). Between W_j and W_k a binding can
// be added to, filtered, or dropped at a WITH — it is never OVERWRITTEN: a
// pattern's first node whose variable is already bound filters the row instead
// of rebinding it (executor.matchPath), a traversal's destination variable that
// is already bound must arrive at the same node or the row is dropped
// (executor.traverseRel), seedNodes assigns only on the branch where the name
// was unbound, and nullBindNewVars assigns only names not already present. So
// every row reaching W_k inherits its (K_j, a) pair unchanged from exactly one
// W_j output row, and a stays a function of K_j. Since all of K_j is retained
// in W_k's key, a adds nothing: dropping it can neither split nor merge a
// group.
//
// # Why bare carries only
//
// The restriction is load-bearing twice. It is what makes the inheritance
// argument above hold at all — a rename (`WITH identity AS ident`) carries the
// value under a name the dependence chain does not follow. And it is what keeps
// the key's DIAGNOSTIC power intact: the nodes memo exists because a
// non-repeatable read would make one column yield two values, which SPLITS a
// group, which surfaces as two rows sharing one output key and the pipeline's
// collision guard failing the actor closed. A bare *VariableRef is a map lookup
// on the binding, never a KV read, so there is no read to be non-repeatable —
// while every item that does read Core KV stays in the key and keeps that
// power. Generalizing to a PropertyAccess would trade it away.
//
// # Fail-closed
//
// A wrong redundancy decision MERGES two groups, which on a read-grant producer
// is an over-grant. So every shape the walk cannot prove falls through to
// today's behaviour — nothing redundant, every item rendered — and names the
// reason it did, rather than returning a bare bool: the structural gate over
// the generated producers reads that reason.

import (
	"fmt"
	"sort"
)

// groupingPlan is one projecting clause's answer.
type groupingPlan struct {
	// Clause is the *With or *Return this plan describes.
	Clause Clause

	// Redundant is indexed by projection item: true where the item's value is
	// determined by the aliases still in the clause's grouping key, so
	// rendering it into that key cannot change the partition. Always
	// len(items) long, and all-false whenever Refusal is set.
	Redundant []bool

	// Key is the clause's EFFECTIVE grouping key — the aliases of its
	// non-aggregating items, minus the redundant ones — sorted, so a caller
	// asserting on it reads the same slice on every run over the same query.
	Key []string

	// Grouping is true when at least one item aggregates, which is when the
	// executor partitions the clause's input rows by Key at all. A clause with
	// no aggregator projects row for row and never renders a key; its Key and
	// Refusal still describe the dependence bookkeeping the NEXT clause
	// inherits.
	Grouping bool

	// Refusal names the shape the walk could not prove, and is "" when the
	// clause was analysed. A refused clause keeps every non-aggregating item in
	// its key, which is exactly what the executor does with no analysis at all.
	Refusal string
}

// analyseGroupingRedundancy returns the redundant-item mask for every clause
// that has one, keyed by the clause pointer the executor holds while it
// projects. A clause with nothing redundant is absent, and a query with nothing
// redundant anywhere yields a nil map — which is also what a *CompiledRule
// built by hand carries, so both take the executor's unreduced path.
//
// Computed once, at Parse, over the query the compiled rule owns; never
// mutated afterwards, so a rule shared across concurrent evaluations stays
// immutable at execute time.
func analyseGroupingRedundancy(q *Query) map[Clause][]bool {
	var out map[Clause][]bool
	for _, p := range analyseGrouping(q) {
		// A clause with no aggregator projects row for row and renders no key at
		// all, so a mask for it describes nothing the executor does. Storing one
		// anyway would be a column waiting to be dropped the day that branch
		// learns to read the mask.
		if !p.Grouping || !anyRedundant(p.Redundant) {
			continue
		}
		if out == nil {
			out = make(map[Clause][]bool)
		}
		out[p.Clause] = p.Redundant
	}
	return out
}

// GroupingClauseReduction is one projecting clause's grouping-reduction
// verdict, in a vocabulary a caller outside this package can act on — the
// diagnostic form of the analysis, the same shape ReferencedLabels and
// ClassifyBranchReturnColumns take for their own compile-time walks.
type GroupingClauseReduction struct {
	// Grouping is true when the clause aggregates, which is when the executor
	// partitions its input rows by Key at all.
	Grouping bool
	// Key is the clause's effective grouping key, sorted.
	Key []string
	// Redundant marks, per projection item, the ones whose rendering into the
	// key is skipped because the aliases still in that key determine them.
	Redundant []bool
	// Refusal names the shape the walk could not prove, and is "" when it did.
	Refusal string
}

// GroupingReduction returns the per-clause verdict of the compile-time grouping
// analysis, in the order the executor applies the clauses.
//
// It exists so a corpus census can pin what every shipped lens earns. The
// population of this reduction was first stated by reading cypher by eye, and
// that reading was wrong in both directions; a census can only be executable if
// the verdict can be asked for from outside the package that computes it.
func (cr *CompiledRule) GroupingReduction() []GroupingClauseReduction {
	if cr == nil {
		return nil
	}
	plans := analyseGrouping(cr.Query)
	out := make([]GroupingClauseReduction, 0, len(plans))
	for _, p := range plans {
		out = append(out, GroupingClauseReduction{
			Grouping:  p.Grouping,
			Key:       p.Key,
			Redundant: p.Redundant,
			Refusal:   p.Refusal,
		})
	}
	return out
}

// analyseGrouping walks q's clauses in the order the executor applies them,
// returning one plan per clause that actually projects, in that order.
//
// It carries two alias sets between clauses: key, the aliases currently in the
// effective grouping key, and det, the aliases functionally determined by key.
// They must stay disjoint — an alias in both is a name the walk has two readings
// of, which is how a redundancy claim becomes an over-merge — and
// finaliseGroupingSets enforces that at every exit rather than trusting it.
//
// The order mirrors ExecuteWithFootprint exactly: MATCH and WITH clauses run
// where they stand, and the query's LAST RETURN runs at the end, over whatever
// they produced. An earlier RETURN is recorded and skipped by the executor —
// it never touches a binding — so it is skipped here too, rather than folded
// into the sets a later clause inherits.
func analyseGrouping(q *Query) []groupingPlan {
	if q == nil {
		return nil
	}
	lastReturn := -1
	for i, c := range q.Clauses {
		if _, isReturn := c.(*Return); isReturn {
			lastReturn = i
		}
	}

	key := map[string]struct{}{}
	det := map[string]struct{}{}

	var plans []groupingPlan
	for _, c := range q.Clauses {
		switch cl := c.(type) {
		case *Match:
			// A MATCH adds bindings, filters rows, or drops rows; it cannot
			// rebind a name already bound, so it cannot invalidate a
			// dependence. Both sets survive it untouched.
		case *With:
			var plan groupingPlan
			plan, key, det = analyseGroupingClause(c, cl.Items, key, det)
			plans = append(plans, plan)
		case *Return:
			// Judged below, after every WITH, whichever position it holds.
		default:
			// A clause shape this walk has no case for could bind or rebind
			// anything. Drop every dependence rather than reason about it: with
			// det empty the next clause claims nothing redundant, and rebuilds
			// both sets from its own items — which is sound whatever ran
			// before, because an aggregating clause's own output is one row per
			// group of its own key.
			key = map[string]struct{}{}
			det = map[string]struct{}{}
		}
	}
	if lastReturn >= 0 {
		r := q.Clauses[lastReturn].(*Return)
		plan, _, _ := analyseGroupingClause(r, r.Items, key, det)
		plans = append(plans, plan)
	}
	return plans
}

// analyseGroupingClause judges one projecting clause against the incoming
// key/det sets, returning its plan and the two sets as the next clause sees
// them.
func analyseGroupingClause(c Clause, items []ProjectionItem, key, det map[string]struct{}) (groupingPlan, map[string]struct{}, map[string]struct{}) {
	aliases := make([]string, len(items))
	aggregating := make([]bool, len(items))
	nonAgg := map[string]struct{}{}
	agg := map[string]struct{}{}
	// carried holds the aliases whose item is a BARE carry: `WITH a` or its
	// no-op `WITH a AS a`, and nothing else.
	carried := map[string]struct{}{}

	duplicate := ""
	seen := map[string]struct{}{}
	unmodelled := ""
	grouping := false

	for i, it := range items {
		a := it.Alias
		if a == "" {
			a = projectionAutoAlias(it.Expr, i)
		}
		aliases[i] = a
		if _, repeat := seen[a]; repeat && duplicate == "" {
			duplicate = a
		}
		seen[a] = struct{}{}

		// CollectVariableRefs' own default-deny arm: an expression form it does
		// not recognise may depend on a binding this walk cannot see, so the
		// clause is refused rather than judged on a short dependency set.
		if _, unknown := CollectVariableRefs(it.Expr); unknown && unmodelled == "" {
			unmodelled = a
		}

		aggregating[i] = containsAggregator(it.Expr)
		if aggregating[i] {
			agg[a] = struct{}{}
			grouping = true
			continue
		}
		nonAgg[a] = struct{}{}
		if vr, isVar := it.Expr.(*VariableRef); isVar && vr.Name == a {
			carried[a] = struct{}{}
		}
	}

	plan := groupingPlan{Clause: c, Redundant: make([]bool, len(items)), Grouping: grouping}
	switch {
	case duplicate != "":
		// Two items under one alias: the second overwrites the first in the
		// projected row, so which expression an alias names downstream is not
		// what this walk would read off it.
		plan.Refusal = fmt.Sprintf("the projection list names %q twice", duplicate)
	case unmodelled != "":
		plan.Refusal = fmt.Sprintf("item %q carries an expression shape this walk cannot enumerate the variable references of", unmodelled)
	default:
		if missing := firstNotIn(key, carried); missing != "" {
			// The prior key alias is gone, renamed, or arrives as a computed
			// value. Either way the dependence chain that made this clause's
			// carries determined is broken here.
			plan.Refusal = fmt.Sprintf("the prior grouping key alias %q is not carried forward as a bare %q", missing, missing)
		}
	}

	if plan.Refusal != "" {
		// A refused clause renders every non-aggregating item, so its key is the
		// whole non-aggregating alias set. What it may claim DETERMINED turns on
		// whether each of those aliases names exactly one item.
		//
		// With unique aliases the alias set IS the executor's key, so every
		// aggregating alias is one value per group and stays determined — a
		// clause refused for an unrelated reason still hands its successor a
		// usable fact.
		//
		// With a duplicated alias, nothing is. The alias set is COARSER than the
		// executor's item-indexed key, so an aggregate is not a function of it;
		// and the duplicated alias's own row value is whichever item wrote it
		// last, which is not the value the analysis recorded under that name.
		// Keeping the aggregating aliases minus the non-aggregating ones would
		// satisfy disjointness and still be wrong — see
		// TestAnalyseGrouping_DuplicateNonAggAliasAlsoUndeterminesTheCleanAggregate.
		nextDet := agg
		if duplicate != "" {
			nextDet = map[string]struct{}{}
		}
		return finaliseGroupingSets(plan, nonAgg, nextDet)
	}

	nextKey := map[string]struct{}{}
	nextDet := map[string]struct{}{}
	for a := range agg {
		// An aggregating item's value is one per group, so it is determined by
		// whatever this clause groups on.
		nextDet[a] = struct{}{}
	}
	for i := range items {
		if aggregating[i] {
			continue
		}
		a := aliases[i]
		_, determined := det[a]
		_, isCarry := carried[a]
		if determined && isCarry {
			plan.Redundant[i] = true
			nextDet[a] = struct{}{}
			continue
		}
		nextKey[a] = struct{}{}
	}
	return finaliseGroupingSets(plan, nextKey, nextDet)
}

// finaliseGroupingSets is the single exit from analyseGroupingClause, and it
// enforces `key ∩ det = ∅` as CODE rather than as a claim in a comment.
//
// An alias in both sets is a name whose value the analysis has two readings of,
// which is exactly how a redundancy claim becomes an over-merge: the next
// clause reads it as determined, drops it from the key, and merges groups the
// executor keeps apart. Every path that reaches here is argued to keep the two
// sets disjoint — and "by construction" is a claim already found to be
// construction-dependent once, on the duplicate-alias path above. So a
// violation collapses to the safe state, where nothing is determined and the
// next clause can claim no redundancy at all, instead of proceeding on a
// premise that has just been shown false.
func finaliseGroupingSets(plan groupingPlan, key, det map[string]struct{}) (groupingPlan, map[string]struct{}, map[string]struct{}) {
	if firstIn(det, key) != "" {
		det = map[string]struct{}{}
	}
	plan.Key = sortedNames(key)
	return plan, key, det
}

// firstNotIn returns the lexically first name of need that is absent from have,
// or "" when have covers need. Lexically first rather than first-encountered so
// a refusal reads the same on every run over the same query — map iteration
// order does not.
func firstNotIn(need, have map[string]struct{}) string {
	miss := ""
	for n := range need {
		if _, ok := have[n]; ok {
			continue
		}
		if miss == "" || n < miss {
			miss = n
		}
	}
	return miss
}

func sortedNames(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func anyRedundant(mask []bool) bool {
	for _, r := range mask {
		if r {
			return true
		}
	}
	return false
}
