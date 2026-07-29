package full

import "fmt"

// ColumnOwnership classifies one RETURN column of a multi-walk Personal
// lens's compiled branches (refractor-shared-keyspace-arbitration-design.md
// §13.2).
type ColumnOwnership int

const (
	// ColumnAnchorDerived means every branch computes the column from the
	// shared anchor/actor alone — every branch must agree on its value.
	ColumnAnchorDerived ColumnOwnership = iota
	// ColumnWalkOwned means the column's expression references only one
	// branch's own bound variables — that branch supplies the value, the
	// others carry it null by construction.
	ColumnWalkOwned
)

// ReturnColumnPlan is one column's classification, keyed by its RETURN
// alias.
type ReturnColumnPlan struct {
	Alias     string
	Ownership ColumnOwnership
	// OwnerBranch is the index into the branches slice that owns this
	// column. Valid only when Ownership == ColumnWalkOwned.
	OwnerBranch int
}

// ClassifyBranchReturnColumns classifies every RETURN column of a multi-walk
// Personal lens's N independently-compiled branches
// (refractor-shared-keyspace-arbitration-design.md §13.2/§13.3): anchor-
// derived, or walk-owned by exactly one branch. It needs no separate input
// naming the actor/anchor variables — commonVars (every variable bound by
// EVERY branch's own MATCH/OPTIONAL MATCH patterns) is derived internally as
// the intersection of the branches' bound-variable sets. This is exact
// because pkgmgr's parseWalks forbids two walks from binding the same
// non-anchor variable name (anchorwalk.go's cross-walk disjointness guard) —
// the only names that CAN appear in every branch are the fixed actor
// binding and the lens's one shared AnchorVar, both of which every walk's
// chain is required to bind.
//
// Refuses (a non-nil error naming the offending column) when: fewer than 2
// branches are given, a branch has no compiled query or no RETURN clause,
// the branches' RETURN alias lists are not identical, or a column's
// expression cannot be attributed to the shared (common) variables alone or
// to exactly one branch — including any column CollectVariableRefs cannot
// classify at all. Fail-closed throughout: an unclassifiable column is
// refused, never silently merged at runtime as if it were anchor-derived or
// walk-owned.
func ClassifyBranchReturnColumns(branches []*CompiledRule) ([]ReturnColumnPlan, error) {
	if len(branches) < 2 {
		return nil, fmt.Errorf("full: ClassifyBranchReturnColumns needs at least 2 branches, got %d", len(branches))
	}

	branchOwn := make([]map[string]bool, len(branches))
	branchReturns := make([]*Return, len(branches))
	for i, cr := range branches {
		if cr == nil || cr.Query == nil {
			return nil, fmt.Errorf("full: branch %d has no compiled query", i)
		}
		bound := map[string]bool{}
		var ret *Return
		for _, c := range cr.Query.Clauses {
			switch cl := c.(type) {
			case *Match:
				for _, pat := range cl.Patterns {
					for _, n := range pat.Nodes {
						if n.Variable != "" {
							bound[n.Variable] = true
						}
					}
					for _, r := range pat.Rels {
						if r.Variable != "" {
							bound[r.Variable] = true
						}
					}
				}
			case *Return:
				ret = cl
			}
		}
		if ret == nil {
			return nil, fmt.Errorf("full: branch %d has no RETURN clause", i)
		}
		branchOwn[i] = bound
		branchReturns[i] = ret
	}

	commonVars := map[string]bool{}
	for v := range branchOwn[0] {
		inEvery := true
		for i := 1; i < len(branchOwn); i++ {
			if !branchOwn[i][v] {
				inEvery = false
				break
			}
		}
		if inEvery {
			commonVars[v] = true
		}
	}
	for i, own := range branchOwn {
		trimmed := map[string]bool{}
		for v := range own {
			if !commonVars[v] {
				trimmed[v] = true
			}
		}
		branchOwn[i] = trimmed
	}

	aliases, ok := returnAliasesOfClause(branchReturns[0])
	if !ok {
		return nil, fmt.Errorf("full: branch 0's RETURN clause has no items")
	}
	for i := 1; i < len(branchReturns); i++ {
		other, ok := returnAliasesOfClause(branchReturns[i])
		if !ok || !equalStringSlices(aliases, other) {
			return nil, fmt.Errorf(
				"full: branch %d's RETURN alias list %v does not match branch 0's %v — "+
					"every walk must share the lens's one output shape", i, other, aliases)
		}
	}

	plan := make([]ReturnColumnPlan, len(aliases))
	for idx, alias := range aliases {
		item := branchReturns[0].Items[idx]
		refs, unknown := CollectVariableRefs(item.Expr)
		if unknown {
			return nil, fmt.Errorf(
				"full: column %q: cannot classify its expression (unrecognised form) — "+
					"refine the tail or split the column into a walk-owned one", alias)
		}
		ownerBranch := -1
		anchorOnly := true
		ambiguous := false
		for v := range refs {
			if commonVars[v] {
				continue
			}
			anchorOnly = false
			owner := -1
			for i, own := range branchOwn {
				if own[v] {
					owner = i
					break
				}
			}
			if owner == -1 || (ownerBranch != -1 && ownerBranch != owner) {
				ambiguous = true
				break
			}
			ownerBranch = owner
		}
		if ambiguous {
			return nil, fmt.Errorf(
				"full: column %q references variables owned by more than one walk (or by none) — "+
					"the lens is refused: attribute it to the shared anchor alone or to exactly one Walks entry",
				alias)
		}
		if anchorOnly {
			plan[idx] = ReturnColumnPlan{Alias: alias, Ownership: ColumnAnchorDerived}
			continue
		}
		plan[idx] = ReturnColumnPlan{Alias: alias, Ownership: ColumnWalkOwned, OwnerBranch: ownerBranch}
	}
	return plan, nil
}

func returnAliasesOfClause(r *Return) ([]string, bool) {
	if r == nil || len(r.Items) == 0 {
		return nil, false
	}
	aliases := make([]string, len(r.Items))
	for i, it := range r.Items {
		a := it.Alias
		if a == "" {
			a = projectionAutoAlias(it.Expr, i)
		}
		aliases[i] = a
	}
	return aliases, true
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
