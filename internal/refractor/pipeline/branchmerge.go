package pipeline

import (
	"context"
	"fmt"
	"sort"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
)

// executeBranches runs this pipeline's compiled rule(s) for one actor: a
// single ExecuteWithFootprint call for an ordinary lens, or one independent
// call per branch — merged by output key, footprints unioned — for a
// multi-walk Personal lens (refractor-shared-keyspace-arbitration-
// design.md §13.2). Factored out of executeFullForActorOnce so the single-
// branch path stays exactly what it was before branch merging existed.
func (p *Pipeline) executeBranches(
	ctx context.Context, actorKey string, nodeProps map[string]any, params map[string]any,
) ([]ruleengine.ProjectionResult, ruleengine.EvalFootprint, error) {
	ec := ruleengine.EventContext{NodeKey: actorKey, NodeProps: nodeProps, Parameters: params}
	if len(p.fullCRBranches) <= 1 {
		return p.fullEngine.ExecuteWithFootprint(ctx, p.fullCR, ec, p.adjKV, p.coreKV)
	}

	branchOuts := make([][]ruleengine.ProjectionResult, len(p.fullCRBranches))
	footprints := make([]ruleengine.EvalFootprint, len(p.fullCRBranches))
	for i, cr := range p.fullCRBranches {
		out, fp, err := p.fullEngine.ExecuteWithFootprint(ctx, cr, ec, p.adjKV, p.coreKV)
		if err != nil {
			return nil, ruleengine.EvalFootprint{}, fmt.Errorf("pipeline: branch %d: %w", i, err)
		}
		branchOuts[i] = out
		footprints[i] = fp
	}
	merged, err := mergeBranchRows(branchOuts)
	if err != nil {
		return nil, ruleengine.EvalFootprint{}, err
	}
	return merged, mergeFootprints(footprints), nil
}

// mergeBranchRows merges the row sets N independently-evaluated branches of
// a multi-walk Personal lens produced for one actor
// (refractor-shared-keyspace-arbitration-design.md §13.2), keyed by output
// Key. A key appearing in only one branch passes through unchanged — every
// RETURN column a sibling walk owns is already null in that branch by
// construction (pkgmgr's composeDataLensSpec doc comment). A key appearing
// in 2+ branches is merged column-by-column: no non-null value anywhere
// stays the merged value's null; exactly one distinct non-null value across
// the sharing rows is taken as-is (a walk-owned column, or every branch's
// identical anchor-derived computation); two or more DISTINCT non-null
// values is the real defect §13.3's expansion-time classifier exists to
// keep out of a shipped lens — an anchor-derived column whose branches
// disagree — and the merge fails loudly rather than picking a winner.
func mergeBranchRows(branchOuts [][]ruleengine.ProjectionResult) ([]ruleengine.ProjectionResult, error) {
	type group struct {
		rows []ruleengine.ProjectionResult
	}
	byKey := map[string]*group{}
	order := make([]string, 0)
	for _, branch := range branchOuts {
		for _, row := range branch {
			k := serializeRowKey(row.Key)
			g, ok := byKey[k]
			if !ok {
				g = &group{}
				byKey[k] = g
				order = append(order, k)
			}
			g.rows = append(g.rows, row)
		}
	}

	merged := make([]ruleengine.ProjectionResult, 0, len(order))
	for _, k := range order {
		rows := byKey[k].rows
		if len(rows) == 1 {
			merged = append(merged, rows[0])
			continue
		}
		row, err := mergeRowGroup(rows)
		if err != nil {
			return nil, err
		}
		merged = append(merged, row)
	}
	return merged, nil
}

// mergeRowGroup combines 2+ branches' rows sharing one output key into one
// row. Delete wins if any branch marked the key deleted — conservative: a
// walk that no longer reaches this anchor should not resurrect a row a
// sibling walk's branch is retracting. Key is taken from the group (every
// row's Key is identical by construction — it is what grouped them).
func mergeRowGroup(rows []ruleengine.ProjectionResult) (ruleengine.ProjectionResult, error) {
	out := ruleengine.ProjectionResult{Key: rows[0].Key}
	for _, r := range rows {
		if r.Delete {
			out.Delete = true
		}
	}
	if out.Delete {
		return out, nil
	}

	values := map[string]any{}
	seen := map[string]bool{}
	for _, r := range rows {
		for col, v := range r.Values {
			if !seen[col] {
				seen[col] = true
				values[col] = v
				continue
			}
			existing := values[col]
			if v == nil || existing == nil {
				if existing == nil {
					values[col] = v
				}
				continue
			}
			if !valuesEqual(existing, v) {
				return ruleengine.ProjectionResult{}, fmt.Errorf(
					"pipeline: branch merge: column %q disagrees across walks for key %v: %v vs %v — "+
						"an anchor-derived column must compute identically in every branch "+
						"(refractor-shared-keyspace-arbitration-design.md §13.2/§13.3)",
					col, rows[0].Key, existing, v)
			}
		}
	}
	out.Values = values
	return out, nil
}

func valuesEqual(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func serializeRowKey(key map[string]any) string {
	names := make([]string, 0, len(key))
	for k := range key {
		names = append(names, k)
	}
	sort.Strings(names)
	s := ""
	for _, n := range names {
		s += n + "=" + fmt.Sprintf("%v", key[n]) + "\x1f"
	}
	return s
}

// mergeFootprints unions N branches' read-surface footprints into one
// certificate — a validating caller must catch drift on ANY key any branch
// depended on, not just branch 0's.
func mergeFootprints(footprints []ruleengine.EvalFootprint) ruleengine.EvalFootprint {
	out := ruleengine.EvalFootprint{
		NodeRevisions: map[string]uint64{},
		EdgeRevisions: map[string]uint64{},
		EdgeSelectors: map[string]ruleengine.EdgeSelectorFootprint{},
	}
	for _, fp := range footprints {
		for k, v := range fp.NodeRevisions {
			out.NodeRevisions[k] = v
		}
		for k, v := range fp.EdgeRevisions {
			out.EdgeRevisions[k] = v
		}
		for k, v := range fp.EdgeSelectors {
			existing, ok := out.EdgeSelectors[k]
			if !ok {
				existing = ruleengine.EdgeSelectorFootprint{Matched: map[ruleengine.EdgeSelector]map[string]struct{}{}}
			}
			if v.Fallback {
				existing.Fallback = true
			}
			for sel, ids := range v.Matched {
				m, ok := existing.Matched[sel]
				if !ok {
					m = map[string]struct{}{}
					existing.Matched[sel] = m
				}
				for id := range ids {
					m[id] = struct{}{}
				}
			}
			out.EdgeSelectors[k] = existing
		}
	}
	return out
}
