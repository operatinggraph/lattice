package simple

import (
	"fmt"
	"strings"
)

// Compile translates a parsed *Query and the rule's key fields into a *QueryPlan.
// keyFields lists the RETURN aliases designated as key columns (from Rule.Into.Key).
// Returns an error if:
//   - any RETURN item has an empty alias
//   - any keyField is not present as a RETURN alias
//   - any RETURN expression cannot be parsed as "variable.property"
func Compile(q *Query, keyFields []string) (*QueryPlan, error) {
	// 1. Defensive alias check (parser enforces AS, but guard for direct callers).
	for _, item := range q.Return.Items {
		if item.Alias == "" {
			return nil, fmt.Errorf("compile: all RETURN columns must be aliased; bare expression %q has no AS alias", item.Expression)
		}
	}

	// 2. Collect required variables (variables introduced in non-optional MATCH clauses).
	requiredVars := map[string]bool{}
	for _, m := range q.Matches {
		if !m.Optional {
			for _, p := range m.Patterns {
				for _, n := range p.Nodes {
					if n.Variable != "" {
						requiredVars[n.Variable] = true
					}
				}
			}
		}
	}

	// 3. Build traversal steps from all MATCH clauses in order.
	var steps []TraversalStep
	for _, m := range q.Matches {
		for _, p := range m.Patterns {
			for i, edge := range p.Edges {
				steps = append(steps, TraversalStep{
					FromVariable: p.Nodes[i].Variable,
					FromLabel:    p.Nodes[i].Label,
					EdgeType:     edge.Type,
					Direction:    edge.Direction,
					ToVariable:   p.Nodes[i+1].Variable,
					ToLabel:      p.Nodes[i+1].Label,
					Optional:     m.Optional,
				})
			}
		}
	}

	// 4. AnchorLabel and AnchorVariable: first node of the first required MATCH clause.
	var anchorLabel, anchorVariable string
	for _, m := range q.Matches {
		if !m.Optional && len(m.Patterns) > 0 && len(m.Patterns[0].Nodes) > 0 {
			anchorLabel = m.Patterns[0].Nodes[0].Label
			anchorVariable = m.Patterns[0].Nodes[0].Variable
			break
		}
	}
	if anchorLabel == "" {
		return nil, fmt.Errorf("compile: query must contain at least one required MATCH clause")
	}

	// 5. Build key-field lookup set.
	keySet := make(map[string]bool, len(keyFields))
	for _, k := range keyFields {
		keySet[k] = true
	}

	// 6. Build column projections from RETURN items.
	columns := make([]Column, 0, len(q.Return.Items))
	for _, item := range q.Return.Items {
		parts := strings.SplitN(item.Expression, ".", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("compile: RETURN expression %q must be in \"variable.property\" form", item.Expression)
		}
		variable, property := parts[0], parts[1]
		columns = append(columns, Column{
			Alias:      item.Alias,
			Expression: item.Expression,
			Variable:   variable,
			Property:   property,
			Nullable:   !requiredVars[variable],
			IsKey:      keySet[item.Alias],
		})
	}

	// 7. Validate all keyFields are present as column aliases.
	aliasSet := make(map[string]bool, len(columns))
	for _, col := range columns {
		aliasSet[col.Alias] = true
	}
	for _, k := range keyFields {
		if !aliasSet[k] {
			return nil, fmt.Errorf("compile: key field %q is not a RETURN alias", k)
		}
	}

	return &QueryPlan{
		AnchorLabel:    anchorLabel,
		AnchorVariable: anchorVariable,
		Steps:          steps,
		Columns:        columns,
	}, nil
}
