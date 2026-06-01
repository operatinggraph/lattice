// Package full's executor walks the Refractor-native AST against Core KV
// (vertex/aspect data) and Adjacency KV (edges) to produce projection rows.
//
// The design stays close to the AST — there is no separate "plan" stage
// between Parse and Execute. Execution proceeds clause-by-clause over a
// list of bindings; each binding maps variable names to either a *nodeRef
// (graph node) or any other value (post-WITH alias).
//
// All Core KV reads filter `isDeleted: true` per Contract #1. All edge
// lookups go through Adjacency KV via the adjacency package.
package full

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/substrate"
)

// maxVarLengthHops is the sanity cap for variable-length traversals when the
// AST records MaxHops=-1 (unbounded). Without a cap a cycle-free but deep
// graph could trigger pathological BFS.
const maxVarLengthHops = 10

// nodeRef is the executor's in-memory handle to a Core KV vertex.
// A nil nodeRef represents an OPTIONAL MATCH null binding.
type nodeRef struct {
	key   string
	props map[string]any
}

// binding maps variable names to values. Variable bindings established by
// MATCH/OPTIONAL MATCH carry *nodeRef values; WITH aliases may carry any
// scalar, list, map, or *nodeRef.
type binding map[string]any

// executor carries per-call mutable state for one Execute invocation.
type executor struct {
	ctx    context.Context
	adjKV  jetstream.KeyValue
	coreKV jetstream.KeyValue
	params map[string]any
}

// ExecuteWith runs cr against the given Core and Adjacency KVs, binding
// `$name` references from ec.Parameters. Called by the pipeline for each
// CDC event on the full-engine path.
//
// Returns one ProjectionResult per result row. Empty result => zero rows.
func (e *Engine) ExecuteWith(
	ctx context.Context,
	cr ruleengine.CompiledRule,
	ec ruleengine.EventContext,
	adjKV, coreKV jetstream.KeyValue,
) ([]ruleengine.ProjectionResult, error) {
	compiled, ok := cr.(*CompiledRule)
	if !ok {
		return nil, fmt.Errorf("full engine: expected *CompiledRule, got %T", cr)
	}
	if compiled.Query == nil {
		return nil, errors.New("full engine: compiled rule has nil query")
	}

	ex := &executor{
		ctx:    ctx,
		adjKV:  adjKV,
		coreKV: coreKV,
		params: ec.Parameters,
	}

	bindings := []binding{{}}
	var lastReturn *Return

	for _, clause := range compiled.Query.Clauses {
		switch c := clause.(type) {
		case *Match:
			next, err := ex.applyMatch(bindings, c)
			if err != nil {
				return nil, err
			}
			bindings = next
		case *With:
			next, err := ex.applyWith(bindings, c)
			if err != nil {
				return nil, err
			}
			bindings = next
		case *Return:
			lastReturn = c
		}
	}

	if lastReturn == nil {
		return nil, errors.New("full engine: query missing RETURN clause")
	}
	return ex.applyReturn(bindings, lastReturn)
}

// Execute satisfies ruleengine.RuleEngine. It is the single-row convenience
// Execute satisfies ruleengine.RuleEngine but cannot operate on a real graph
// because the engine-neutral signature does not carry KV handles. The pipeline
// calls ExecuteWith directly. Returning a typed error keeps the contract honest.
func (e *Engine) Execute(_ context.Context, _ ruleengine.CompiledRule, _ ruleengine.EventContext) (ruleengine.ProjectionResult, error) {
	return ruleengine.ProjectionResult{}, errors.New(
		"full engine: Execute requires KV handles — call ExecuteWith from the pipeline")
}

// --- MATCH ---

func (ex *executor) applyMatch(bindings []binding, m *Match) ([]binding, error) {
	var out []binding
	for _, b := range bindings {
		expanded, err := ex.matchPatterns(b, m.Patterns, m.Optional)
		if err != nil {
			return nil, err
		}
		// Apply WHERE. For OPTIONAL MATCH, WHERE filters MATCH'd rows but if
		// all matches are filtered out, the optional null-binding preserves
		// the original binding (Cypher OPTIONAL MATCH ... WHERE semantics).
		var passing []binding
		hadNonNullMatch := false
		for _, nb := range expanded {
			// "Non-null match" = at least one newly introduced pattern var
			// is bound to a real *nodeRef (not the null sentinel) in this
			// expansion that wasn't bound in b.
			if isNonNullExpansion(b, nb, m.Patterns) {
				hadNonNullMatch = true
				if m.Where != nil {
					v, err := ex.evalExpr(nb, m.Where)
					if err != nil {
						return nil, err
					}
					if !truthy(v) {
						continue
					}
				}
				passing = append(passing, nb)
			} else {
				// Null-preserving row — keep regardless of WHERE for OPTIONAL,
				// drop for required (which shouldn't produce null expansions).
				if m.Optional {
					passing = append(passing, nb)
				}
			}
		}
		if m.Optional && hadNonNullMatch {
			// Drop the null-preserving fallback rows when at least one real
			// match exists for THIS source binding.
			filtered := passing[:0]
			for _, nb := range passing {
				if isNonNullExpansion(b, nb, m.Patterns) {
					filtered = append(filtered, nb)
				}
			}
			// If all real matches got filtered by WHERE, restore the null fallback.
			if len(filtered) == 0 {
				for _, nb := range expanded {
					if !isNonNullExpansion(b, nb, m.Patterns) {
						filtered = append(filtered, nb)
						break
					}
				}
			}
			passing = filtered
		}
		out = append(out, passing...)
	}
	return out, nil
}

// isNonNullExpansion reports whether nb is a "real" match expansion of b
// (i.e. at least one newly introduced variable from patterns is bound to a
// non-nil *nodeRef in nb but absent in b).
func isNonNullExpansion(b, nb binding, patterns []PathPattern) bool {
	for _, p := range patterns {
		for _, n := range p.Nodes {
			if n.Variable == "" {
				continue
			}
			if _, had := b[n.Variable]; had {
				continue
			}
			if ref, ok := nb[n.Variable].(*nodeRef); ok && ref != nil {
				return true
			}
		}
		for _, r := range p.Rels {
			if r.Variable == "" {
				continue
			}
			if _, had := b[r.Variable]; had {
				continue
			}
			if ref, ok := nb[r.Variable].(*nodeRef); ok && ref != nil {
				return true
			}
		}
	}
	return false
}

// matchPatterns expands a binding across all comma-separated patterns in a
// single MATCH/OPTIONAL MATCH clause. For OPTIONAL MATCH that yields zero
// expansions, the original binding is preserved with null assignments for
// any newly introduced variables.
func (ex *executor) matchPatterns(b binding, patterns []PathPattern, optional bool) ([]binding, error) {
	current := []binding{b}
	for _, p := range patterns {
		var next []binding
		for _, cb := range current {
			expansions, err := ex.matchPath(cb, p)
			if err != nil {
				return nil, err
			}
			if len(expansions) == 0 && optional {
				// Null-bind every new variable introduced by this path.
				nb := cloneBinding(cb)
				for _, n := range p.Nodes {
					if n.Variable != "" {
						if _, has := nb[n.Variable]; !has {
							nb[n.Variable] = (*nodeRef)(nil)
						}
					}
				}
				for _, r := range p.Rels {
					if r.Variable != "" {
						if _, has := nb[r.Variable]; !has {
							nb[r.Variable] = (*nodeRef)(nil)
						}
					}
				}
				next = append(next, nb)
				continue
			}
			next = append(next, expansions...)
		}
		current = next
	}
	return current, nil
}

// matchPath expands binding b across one PathPattern. Returns zero or more
// new bindings — one per matched path.
func (ex *executor) matchPath(b binding, p PathPattern) ([]binding, error) {
	if len(p.Nodes) == 0 {
		return []binding{b}, nil
	}

	// First node: either bound (existing variable) or seed by scan.
	first := p.Nodes[0]
	var heads []binding
	if first.Variable != "" {
		if existing, ok := b[first.Variable]; ok {
			ref, _ := existing.(*nodeRef)
			if ref == nil {
				// Null binding cannot extend.
				return nil, nil
			}
			if !ex.nodeMatches(ref, first) {
				return nil, nil
			}
			if propsMatchErr := ex.checkProps(ref, first); propsMatchErr != nil {
				return nil, propsMatchErr
			}
			ok, err := ex.propsAllMatch(b, ref, first)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, nil
			}
			heads = []binding{b}
		}
	}
	if heads == nil {
		// Need to seed: scan Core KV for nodes matching label + props.
		seeds, err := ex.seedNodes(b, first)
		if err != nil {
			return nil, err
		}
		for _, s := range seeds {
			nb := cloneBinding(b)
			if first.Variable != "" {
				nb[first.Variable] = s
			}
			heads = append(heads, nb)
		}
	}

	// Walk relationships.
	for i, rel := range p.Rels {
		toNode := p.Nodes[i+1]
		var next []binding
		for _, h := range heads {
			fromRef := ex.currentNode(h, p.Nodes[i])
			if fromRef == nil {
				continue
			}
			reached, err := ex.traverseRel(h, fromRef, rel, toNode)
			if err != nil {
				return nil, err
			}
			next = append(next, reached...)
		}
		heads = next
	}
	return heads, nil
}

// currentNode resolves the *nodeRef bound to nodePattern's variable (after
// seeding). For unnamed pattern nodes we fall back to the rel's "from" side
// in traverseRel, so this returns nil only when the variable name doesn't
// resolve.
func (ex *executor) currentNode(b binding, n NodePattern) *nodeRef {
	if n.Variable == "" {
		return nil
	}
	r, _ := b[n.Variable].(*nodeRef)
	return r
}

// nodeMatches checks label match. Resolves the node's label from (in order):
//  1. the parsed `vtx.<type>.<id>` prefix of its key (Contract #1 keys),
//  2. a `class` property in its stored JSON,
//  3. a `label` property in its stored JSON.
// Returns true when the pattern label is empty.
func (ex *executor) nodeMatches(ref *nodeRef, n NodePattern) bool {
	if n.Label == "" {
		return true
	}
	if ref == nil {
		return false
	}
	if vtype, _, ok := substrate.ParseVertexKey(ref.key); ok {
		if vtype == n.Label {
			return true
		}
	}
	if c, ok := ref.props["class"].(string); ok && c == n.Label {
		return true
	}
	if l, ok := ref.props["label"].(string); ok && l == n.Label {
		return true
	}
	return false
}

// checkProps is a thin alias retained for readability.
func (ex *executor) checkProps(_ *nodeRef, _ NodePattern) error { return nil }

// propsAllMatch evaluates each property predicate in n against ref.
func (ex *executor) propsAllMatch(b binding, ref *nodeRef, n NodePattern) (bool, error) {
	if len(n.Properties) == 0 {
		return true, nil
	}
	for k, vexpr := range n.Properties {
		want, err := ex.evalExpr(b, vexpr)
		if err != nil {
			return false, err
		}
		got, ok := ref.props[k]
		if !ok {
			// Try "key" alias against Core KV key itself.
			if k == "key" {
				got = ref.key
			} else {
				return false, nil
			}
		}
		if !equalsAny(got, want) {
			return false, nil
		}
	}
	return true, nil
}

// seedNodes returns all Core KV vertices matching n's label + properties.
// For property predicates that include "key" with a literal/parameter, we
// short-circuit to a point lookup. Otherwise we scan the bucket and filter.
func (ex *executor) seedNodes(b binding, n NodePattern) ([]*nodeRef, error) {
	// Fast path: property "key" with a resolvable expression → point read.
	if keyExpr, ok := n.Properties["key"]; ok {
		val, err := ex.evalExpr(b, keyExpr)
		if err != nil {
			return nil, err
		}
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("full engine: node property 'key' must resolve to string, got %T", val)
		}
		ref, err := ex.fetchNode(s)
		if err != nil {
			return nil, err
		}
		if ref == nil {
			return nil, nil
		}
		if !ex.nodeMatches(ref, n) {
			return nil, nil
		}
		ok, err = ex.propsAllMatch(b, ref, n)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		return []*nodeRef{ref}, nil
	}

	// Generic path: scan the Core KV bucket. Filters by label first when set.
	keys, err := ex.coreKV.Keys(ex.ctx)
	if err != nil {
		// An empty bucket may surface ErrNoKeysFound; treat as no seeds.
		return nil, nil
	}
	var refs []*nodeRef
	for _, k := range keys {
		// Filter early when key is a Contract #1 shape: only KindVertex.
		if cls := substrate.ClassifyKey(k); cls != substrate.KindVertex && cls != substrate.KindUnknown {
			continue
		}
		ref, err := ex.fetchNode(k)
		if err != nil {
			return nil, err
		}
		if ref == nil {
			continue
		}
		if !ex.nodeMatches(ref, n) {
			continue
		}
		ok, err := ex.propsAllMatch(b, ref, n)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// fetchNode reads a Core KV vertex, returning nil for missing or soft-deleted.
func (ex *executor) fetchNode(key string) (*nodeRef, error) {
	entry, err := ex.coreKV.Get(ex.ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("full engine: get %q: %w", key, err)
	}
	var props map[string]any
	if err := json.Unmarshal(entry.Value(), &props); err != nil {
		return nil, fmt.Errorf("full engine: unmarshal %q: %w", key, err)
	}
	// A JSON "null" body unmarshals to a nil map. Treat as absent/tombstone —
	// a null-body entry is likely a corrupted or transitional write.
	if props == nil {
		return nil, nil
	}
	if deleted, _ := props["isDeleted"].(bool); deleted {
		return nil, nil
	}
	props["key"] = key
	return &nodeRef{key: key, props: props}, nil
}

// traverseRel expands one relationship hop (possibly variable-length).
func (ex *executor) traverseRel(b binding, from *nodeRef, rel RelPattern, to NodePattern) ([]binding, error) {
	minHops := rel.MinHops
	maxHops := rel.MaxHops
	if maxHops < 0 || maxHops > maxVarLengthHops {
		maxHops = maxVarLengthHops
	}
	if minHops < 0 {
		minHops = 0
	}

	type frontier struct {
		node *nodeRef
		seen map[string]struct{}
	}
	starts := []frontier{{node: from, seen: map[string]struct{}{from.key: {}}}}

	var matched []*nodeRef
	// Hop 0 means "from itself" — admit if minHops==0 and to filters allow.
	admit := func(ref *nodeRef) (bool, error) {
		if !ex.nodeMatches(ref, to) {
			return false, nil
		}
		ok, err := ex.propsAllMatch(b, ref, to)
		if err != nil {
			return false, err
		}
		return ok, nil
	}

	if minHops == 0 {
		ok, err := admit(from)
		if err != nil {
			return nil, err
		}
		if ok {
			matched = append(matched, from)
		}
	}

	current := starts
	for hop := 1; hop <= maxHops; hop++ {
		var nextFrontier []frontier
		for _, f := range current {
			// Adjacency KV is indexed by bare NodeID, not full Contract #1
			// vertex keys. When f.node.key is a Contract #1 vtx key, extract
			// the NodeID; otherwise treat the key as a bare NodeID (test /
			// legacy Materializer fixture path).
			adjLookupID := f.node.key
			if _, nodeID, ok := substrate.ParseVertexKey(f.node.key); ok {
				adjLookupID = nodeID
			}
			edges, err := adjacency.Neighbors(ex.ctx, ex.adjKV, adjLookupID)
			if err != nil {
				return nil, fmt.Errorf("full engine: neighbors(%s): %w", adjLookupID, err)
			}
			for _, e := range edges {
				if rel.Type != "" && e.Name != rel.Type {
					continue
				}
				if !directionMatches(e.Direction, rel.Direction) {
					continue
				}
				// Reconstruct the OTHER endpoint's Core KV key. If the edge
				// carries OtherType (Contract #1 link convention), build the
				// full vtx key; otherwise the OtherNodeID itself is the
				// Core KV key (Materializer-style fixture path).
				otherCoreKey := e.OtherNodeID
				if e.OtherType != "" {
					otherCoreKey = substrate.VertexPrefix + "." + e.OtherType + "." + e.OtherNodeID
				}
				if _, seen := f.seen[otherCoreKey]; seen {
					continue
				}
				neighbor, err := ex.fetchNode(otherCoreKey)
				if err != nil {
					return nil, err
				}
				if neighbor == nil {
					continue
				}
				if hop >= minHops {
					ok, err := admit(neighbor)
					if err != nil {
						return nil, err
					}
					if ok {
						matched = append(matched, neighbor)
					}
				}
				// Extend frontier for next hop.
				ns := make(map[string]struct{}, len(f.seen)+1)
				for k := range f.seen {
					ns[k] = struct{}{}
				}
				ns[neighbor.key] = struct{}{}
				nextFrontier = append(nextFrontier, frontier{node: neighbor, seen: ns})
			}
		}
		current = nextFrontier
		if len(current) == 0 {
			break
		}
	}

	// Deduplicate matched by key — same target reachable via multiple paths.
	seen := map[string]bool{}
	var unique []*nodeRef
	for _, m := range matched {
		if seen[m.key] {
			continue
		}
		seen[m.key] = true
		unique = append(unique, m)
	}

	out := make([]binding, 0, len(unique))
	for _, n := range unique {
		// If the destination variable is already bound in this binding, the
		// traversal must arrive at the same node (constrained-target case,
		// e.g. `(report)<-[:reportsTo]-(identity)` where identity is already
		// bound from a prior clause).
		if to.Variable != "" {
			if existing, ok := b[to.Variable]; ok {
				ex, _ := existing.(*nodeRef)
				if ex == nil || ex.key != n.key {
					continue
				}
			}
		}
		nb := cloneBinding(b)
		if to.Variable != "" {
			nb[to.Variable] = n
		}
		out = append(out, nb)
	}
	return out, nil
}

// directionMatches compares an Adjacency edge direction string against the
// AST's Direction. The Adjacency builder records each edge under both
// endpoints with direction="outbound" on the source side and "inbound" on
// the target side. DirOut wants outbound; DirIn wants inbound; DirBoth wants
// either.
func directionMatches(adjDir string, want Direction) bool {
	switch want {
	case DirOut:
		return adjDir == "outbound"
	case DirIn:
		return adjDir == "inbound"
	case DirBoth:
		return true
	}
	return false
}

// --- WITH ---

func (ex *executor) applyWith(bindings []binding, w *With) ([]binding, error) {
	projected, err := ex.projectItems(bindings, w.Items)
	if err != nil {
		return nil, err
	}
	if w.Where != nil {
		var filtered []binding
		for _, b := range projected {
			v, err := ex.evalExpr(b, w.Where)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				filtered = append(filtered, b)
			}
		}
		projected = filtered
	}
	return projected, nil
}

// projectItems evaluates each ProjectionItem against the inbound bindings.
// If any item is an aggregating expression (e.g. collect), the result is
// grouped by the non-aggregating items.
func (ex *executor) projectItems(bindings []binding, items []ProjectionItem) ([]binding, error) {
	// Decide aggregating vs non-aggregating per item.
	itemAggregating := make([]bool, len(items))
	anyAggregating := false
	for i, it := range items {
		if containsAggregator(it.Expr) {
			itemAggregating[i] = true
			anyAggregating = true
		}
	}
	itemAlias := func(i int) string {
		if items[i].Alias != "" {
			return items[i].Alias
		}
		return projectionAutoAlias(items[i].Expr, i)
	}

	if !anyAggregating {
		out := make([]binding, 0, len(bindings))
		for _, b := range bindings {
			nb := binding{}
			for i, it := range items {
				v, err := ex.evalExpr(b, it.Expr)
				if err != nil {
					return nil, err
				}
				nb[itemAlias(i)] = v
			}
			out = append(out, nb)
		}
		return out, nil
	}

	// Group: compute the grouping key per row.
	type groupAcc struct {
		key       string
		row       binding
		aggInputs [][]any // per-item input values across the group
		seen      []map[string]struct{} // per-item DISTINCT dedup sets
	}
	groups := map[string]*groupAcc{}
	var order []string
	for _, b := range bindings {
		// Build grouping key
		keyParts := make([]string, 0, len(items))
		groupVals := map[int]any{}
		for i, it := range items {
			if itemAggregating[i] {
				continue
			}
			v, err := ex.evalExpr(b, it.Expr)
			if err != nil {
				return nil, err
			}
			groupVals[i] = v
			keyParts = append(keyParts, fmt.Sprintf("%d=%v", i, normalizeForKey(v)))
		}
		k := strings.Join(keyParts, "|")
		g, ok := groups[k]
		if !ok {
			g = &groupAcc{
				key:       k,
				row:       binding{},
				aggInputs: make([][]any, len(items)),
				seen:      make([]map[string]struct{}, len(items)),
			}
			for i, v := range groupVals {
				g.row[itemAlias(i)] = v
			}
			groups[k] = g
			order = append(order, k)
		}
		// Accumulate aggregating items' inputs.
		for i, it := range items {
			if !itemAggregating[i] {
				continue
			}
			vals, err := ex.evalAggregatorArgs(b, it.Expr)
			if err != nil {
				return nil, err
			}
			for _, v := range vals {
				if isAggregatorDistinct(it.Expr) {
					if g.seen[i] == nil {
						g.seen[i] = map[string]struct{}{}
					}
					sig := fmt.Sprintf("%v", normalizeForKey(v))
					if _, ok := g.seen[i][sig]; ok {
						continue
					}
					g.seen[i][sig] = struct{}{}
				}
				g.aggInputs[i] = append(g.aggInputs[i], v)
			}
		}
	}

	out := make([]binding, 0, len(order))
	for _, k := range order {
		g := groups[k]
		for i, it := range items {
			if !itemAggregating[i] {
				continue
			}
			v, err := ex.finalizeAggregator(it.Expr, g.aggInputs[i])
			if err != nil {
				return nil, err
			}
			g.row[itemAlias(i)] = v
		}
		out = append(out, g.row)
	}
	// If empty (no rows in), still emit one row of nulls when aggregation
	// is requested — Cypher's RETURN collect() yields [] on empty input.
	if len(out) == 0 && anyAggregating {
		row := binding{}
		for i, it := range items {
			if itemAggregating[i] {
				v, err := ex.finalizeAggregator(it.Expr, nil)
				if err != nil {
					return nil, err
				}
				row[itemAlias(i)] = v
			} else {
				// Non-aggregating projection with no rows: NULL.
				row[itemAlias(i)] = nil
				_ = it
			}
		}
		out = append(out, row)
	}
	return out, nil
}

func projectionAutoAlias(e Expr, idx int) string {
	switch x := e.(type) {
	case *VariableRef:
		return x.Name
	case *PropertyAccess:
		return x.Key
	}
	return fmt.Sprintf("_col%d", idx)
}

// containsAggregator returns true if the expression tree contains a
// recognized aggregator (currently just collect).
func containsAggregator(e Expr) bool {
	found := false
	walkExprAll(e, func(x Expr) {
		if fc, ok := x.(*FunctionCall); ok {
			if strings.EqualFold(fc.Name, "collect") || strings.EqualFold(fc.Name, "count") {
				found = true
			}
		}
	})
	return found
}

// isAggregatorDistinct reports whether the outermost aggregator on e uses DISTINCT.
func isAggregatorDistinct(e Expr) bool {
	// For BinaryOp like "collect(...) + collect(...)" the items are aggregated
	// independently; the binary op is applied to the FINAL aggregated lists.
	if fc, ok := e.(*FunctionCall); ok {
		return fc.Distinct
	}
	return false
}

// evalAggregatorArgs collects the argument values for ONE row.
// For an outer FunctionCall it returns evalExpr of each argument.
// For a BinaryOp (e.g. collect(..) + collect(..)) it returns the per-row
// concatenation of the inner aggregators' inputs, signaling that the binary
// operator is applied during finalize.
func (ex *executor) evalAggregatorArgs(b binding, e Expr) ([]any, error) {
	switch x := e.(type) {
	case *FunctionCall:
		if len(x.Args) == 0 {
			return nil, nil
		}
		v, err := ex.evalExpr(b, x.Args[0])
		if err != nil {
			return nil, err
		}
		return []any{v}, nil
	case *BinaryOp:
		// Treated as two independent aggregations to be concatenated; here we
		// emit a tagged composite the finalize step recognizes.
		left, err := ex.evalAggregatorArgs(b, x.Left)
		if err != nil {
			return nil, err
		}
		right, err := ex.evalAggregatorArgs(b, x.Right)
		if err != nil {
			return nil, err
		}
		return []any{composite{op: x.Op, left: left, right: right}}, nil
	}
	return nil, nil
}

type composite struct {
	op    string
	left  []any
	right []any
}

func (ex *executor) finalizeAggregator(e Expr, inputs []any) (any, error) {
	switch x := e.(type) {
	case *FunctionCall:
		if strings.EqualFold(x.Name, "collect") {
			// Drop nulls (Cypher semantics).
			out := make([]any, 0, len(inputs))
			for _, v := range inputs {
				if v == nil {
					continue
				}
				out = append(out, v)
			}
			return out, nil
		}
		if strings.EqualFold(x.Name, "count") {
			n := 0
			for _, v := range inputs {
				if v != nil {
					n++
				}
			}
			return int64(n), nil
		}
		return nil, fmt.Errorf("full engine: unsupported aggregator %q", x.Name)
	case *BinaryOp:
		// Each input is a `composite` carrying per-row left/right slices.
		var leftAll, rightAll []any
		for _, in := range inputs {
			c, ok := in.(composite)
			if !ok {
				continue
			}
			leftAll = append(leftAll, c.left...)
			rightAll = append(rightAll, c.right...)
		}
		leftVal, err := ex.finalizeAggregator(x.Left, leftAll)
		if err != nil {
			return nil, err
		}
		rightVal, err := ex.finalizeAggregator(x.Right, rightAll)
		if err != nil {
			return nil, err
		}
		if x.Op == "+" {
			ll, _ := leftVal.([]any)
			rr, _ := rightVal.([]any)
			out := make([]any, 0, len(ll)+len(rr))
			out = append(out, ll...)
			out = append(out, rr...)
			return out, nil
		}
		return nil, fmt.Errorf("full engine: unsupported aggregator op %q", x.Op)
	}
	return nil, errors.New("full engine: finalizeAggregator: unsupported expression")
}

// --- RETURN ---

func (ex *executor) applyReturn(bindings []binding, r *Return) ([]ruleengine.ProjectionResult, error) {
	rows, err := ex.projectItems(bindings, r.Items)
	if err != nil {
		return nil, err
	}
	// Deduplicate rows when RETURN DISTINCT is specified. Rows are compared by
	// their JSON-serialised content; order is preserved (first occurrence wins).
	if r.Distinct {
		seen := make(map[string]struct{}, len(rows))
		deduped := rows[:0]
		for _, row := range rows {
			b, _ := json.Marshal(row)
			key := string(b)
			if _, exists := seen[key]; !exists {
				seen[key] = struct{}{}
				deduped = append(deduped, row)
			}
		}
		rows = deduped
	}
	out := make([]ruleengine.ProjectionResult, 0, len(rows))
	for _, row := range rows {
		values := map[string]any{}
		for k, v := range row {
			values[k] = v
		}
		// Use the first projection item as the key when present, mirroring
		// the simple engine's "alias becomes the key column" convention.
		keyMap := map[string]any{}
		if len(r.Items) > 0 {
			alias := r.Items[0].Alias
			if alias == "" {
				alias = projectionAutoAlias(r.Items[0].Expr, 0)
			}
			keyMap[alias] = values[alias]
		}
		out = append(out, ruleengine.ProjectionResult{Key: keyMap, Values: values})
	}
	return out, nil
}

// --- expression evaluation ---

func (ex *executor) evalExpr(b binding, e Expr) (any, error) {
	switch x := e.(type) {
	case nil:
		return nil, nil
	case *Literal:
		return x.Value, nil
	case *ParameterRef:
		if ex.params == nil {
			return nil, &ruleengine.MissingParameterError{Name: x.Name}
		}
		v, ok := ex.params[x.Name]
		if !ok {
			return nil, &ruleengine.MissingParameterError{Name: x.Name}
		}
		return v, nil
	case *VariableRef:
		if v, ok := b[x.Name]; ok {
			return v, nil
		}
		return nil, nil
	case *PropertyAccess:
		target, err := ex.evalExpr(b, x.Target)
		if err != nil {
			return nil, err
		}
		return ex.resolveProperty(target, x.Key)
	case *BinaryOp:
		l, err := ex.evalExpr(b, x.Left)
		if err != nil {
			return nil, err
		}
		r, err := ex.evalExpr(b, x.Right)
		if err != nil {
			return nil, err
		}
		return evalBinary(x.Op, l, r)
	case *AndOr:
		if x.Op == "AND" {
			for _, op := range x.Operands {
				v, err := ex.evalExpr(b, op)
				if err != nil {
					return nil, err
				}
				if !truthy(v) {
					return false, nil
				}
			}
			return true, nil
		}
		if x.Op == "XOR" {
			trueCount := 0
			for _, op := range x.Operands {
				v, err := ex.evalExpr(b, op)
				if err != nil {
					return nil, err
				}
				if truthy(v) {
					trueCount++
				}
			}
			return trueCount == 1, nil
		}
		// OR
		for _, op := range x.Operands {
			v, err := ex.evalExpr(b, op)
			if err != nil {
				return nil, err
			}
			if truthy(v) {
				return true, nil
			}
		}
		return false, nil
	case *Not:
		// Anti-pattern: NOT (path) — evaluate as existence predicate.
		if pe, ok := x.Operand.(*PatternExpr); ok {
			exists, err := ex.existsAsPredicate(b, pe.Pattern)
			if err != nil {
				return nil, err
			}
			return !exists, nil
		}
		v, err := ex.evalExpr(b, x.Operand)
		if err != nil {
			return nil, err
		}
		return !truthy(v), nil
	case *PatternExpr:
		return ex.existsAsPredicate(b, x.Pattern)
	case *FunctionCall:
		return ex.evalFunctionCall(b, x)
	case *MapLiteral:
		out := make(map[string]any, len(x.Keys))
		for _, k := range x.Keys {
			v, err := ex.evalExpr(b, x.Values[k])
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case *ListLiteral:
		out := make([]any, 0, len(x.Elements))
		for _, el := range x.Elements {
			v, err := ex.evalExpr(b, el)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case *PatternComprehension:
		return ex.evalPatternComprehension(b, x)
	}
	return nil, fmt.Errorf("full engine: unsupported expression %T", e)
}

func (ex *executor) evalFunctionCall(b binding, fc *FunctionCall) (any, error) {
	// During projection without grouping, collect()/count() are evaluated
	// row-locally by projectItems → finalizeAggregator. Outside that path
	// (e.g. inside another expression) treat collect as a no-op wrapper that
	// returns the single arg's value wrapped in a list.
	name := strings.ToLower(fc.Name)
	switch name {
	case "collect":
		if len(fc.Args) == 0 {
			return []any{}, nil
		}
		v, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		if v == nil {
			return []any{}, nil
		}
		return []any{v}, nil
	case "count":
		return int64(1), nil
	case "levenshteindist":
		// levenshteinDist(a, b) → int — classical Wagner-Fischer edit distance.
		// Pure / deterministic / O(N*M) time + O(min(N,M)) space.
		// Both args must be strings; nil args return nil.
		if len(fc.Args) != 2 {
			return nil, fmt.Errorf("full engine: levenshteinDist takes exactly 2 arguments")
		}
		av, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		bv, err := ex.evalExpr(b, fc.Args[1])
		if err != nil {
			return nil, err
		}
		if av == nil || bv == nil {
			return nil, nil
		}
		as, aok := av.(string)
		bs, bok := bv.(string)
		if !aok || !bok {
			return nil, fmt.Errorf("full engine: levenshteinDist arguments must be strings, got %T and %T", av, bv)
		}
		return int64(levenshteinDistance(as, bs)), nil
	case "levenshteinratio":
		// levenshteinRatio(a, b) → float64 in [0.0, 1.0].
		// 1.0 when identical (incl. both empty); 0.0 when one is empty
		// and other is non-empty.
		if len(fc.Args) != 2 {
			return nil, fmt.Errorf("full engine: levenshteinRatio takes exactly 2 arguments")
		}
		av, err := ex.evalExpr(b, fc.Args[0])
		if err != nil {
			return nil, err
		}
		bv, err := ex.evalExpr(b, fc.Args[1])
		if err != nil {
			return nil, err
		}
		if av == nil || bv == nil {
			return nil, nil
		}
		as, aok := av.(string)
		bs, bok := bv.(string)
		if !aok || !bok {
			return nil, fmt.Errorf("full engine: levenshteinRatio arguments must be strings, got %T and %T", av, bv)
		}
		la, lb := len(as), len(bs)
		maxLen := la
		if lb > maxLen {
			maxLen = lb
		}
		if maxLen == 0 {
			return float64(1.0), nil
		}
		dist := levenshteinDistance(as, bs)
		return 1.0 - float64(dist)/float64(maxLen), nil
	}
	return nil, fmt.Errorf("full engine: unsupported function %q", fc.Name)
}

// levenshteinDistance computes the classical Wagner-Fischer edit distance
// between strings a and b. Uses a rolling-row approach for O(min(|a|,|b|))
// space. Cost: insert=1, delete=1, substitute=1. Operates over runes so
// multi-byte UTF-8 sequences count as single characters.
func levenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) > len(rb) {
		ra, rb = rb, ra
	}
	n, m := len(ra), len(rb)
	if n == 0 {
		return m
	}
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if rb[i-1] == ra[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			curr[j] = del
			if ins < curr[j] {
				curr[j] = ins
			}
			if sub < curr[j] {
				curr[j] = sub
			}
		}
		prev, curr = curr, prev
	}
	return prev[n]
}

// evalPatternComprehension implements `[(x)-[:t]->(y) | projection]`.
// It re-walks the pattern starting from the current binding, evaluating the
// projection expression for each match and returning the resulting list.
func (ex *executor) evalPatternComprehension(b binding, pc *PatternComprehension) (any, error) {
	matches, err := ex.matchPath(b, pc.Pattern)
	if err != nil {
		return nil, err
	}
	out := make([]any, 0, len(matches))
	for _, m := range matches {
		if pc.Where != nil {
			v, err := ex.evalExpr(m, pc.Where)
			if err != nil {
				return nil, err
			}
			if !truthy(v) {
				continue
			}
		}
		v, err := ex.evalExpr(m, pc.Projection)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// existsAsPredicate returns true if pattern has at least one match starting
// from the current binding. Used by NOT (path) anti-pattern WHERE.
func (ex *executor) existsAsPredicate(b binding, p PathPattern) (bool, error) {
	matches, err := ex.matchPath(b, p)
	if err != nil {
		return false, err
	}
	return len(matches) > 0, nil
}

// --- helpers ---

func cloneBinding(b binding) binding {
	nb := make(binding, len(b))
	for k, v := range b {
		nb[k] = v
	}
	return nb
}

// propertyOf resolves target.key for various target shapes (nodeRef, map,
// or nil). Returns nil for null targets and missing keys.
// resolveProperty reads property `key` off target, implementing the Lattice
// property model: vertices carry the envelope (key/class/provenance) plus link
// topology; business data lives in aspects (and, by exception, in a vertex's
// own `data` envelope — e.g. permissions).
//
// For a vertex nodeRef, a name present in the root body returns that value
// directly (envelope fields, and root `data`). A name ABSENT from the root body
// is treated as an ASPECT reference: the aspect key <nodeKey>.<key> is
// point-read and its body returned, so a lens rule navigates an aspect-stored
// field explicitly as node.<aspect>.data.<field> (e.g. role.canonicalName.data.value).
// Aspect bodies returned this way are plain maps, so any further navigation uses
// ordinary map access — only the first hop off a vertex resolves an aspect.
func (ex *executor) resolveProperty(target any, key string) (any, error) {
	nr, ok := target.(*nodeRef)
	if !ok || nr == nil {
		return propertyOf(target, key), nil
	}
	if v, present := nr.props[key]; present {
		return v, nil
	}
	if key == "key" {
		return nr.key, nil
	}
	// Absent from the root body → aspect reference: point-read <nodeKey>.<key>.
	aref, err := ex.fetchNode(nr.key + "." + key)
	if err != nil {
		return nil, err
	}
	if aref == nil {
		return nil, nil
	}
	return aref.props, nil
}

func propertyOf(target any, key string) any {
	switch t := target.(type) {
	case nil:
		return nil
	case *nodeRef:
		if t == nil {
			return nil
		}
		if v, ok := t.props[key]; ok {
			return v
		}
		if key == "key" {
			return t.key
		}
		return nil
	case map[string]any:
		return t[key]
	}
	return nil
}

func truthy(v any) bool {
	if v == nil {
		return false
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

func evalBinary(op string, l, r any) (any, error) {
	switch op {
	case "=":
		return equalsAny(l, r), nil
	case "<>":
		return !equalsAny(l, r), nil
	case "<", ">", "<=", ">=":
		return compareAny(op, l, r)
	case "+":
		// String concat or numeric add — defer to numeric when both numeric,
		// otherwise list concat when both lists.
		if ll, ok := l.([]any); ok {
			if rr, ok := r.([]any); ok {
				out := make([]any, 0, len(ll)+len(rr))
				out = append(out, ll...)
				out = append(out, rr...)
				return out, nil
			}
		}
		return numericOp(op, l, r)
	case "-", "*", "/", "%":
		return numericOp(op, l, r)
	}
	return nil, fmt.Errorf("full engine: unsupported binary op %q", op)
}

func equalsAny(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	// Numeric coercion: int64 vs float64.
	if af, aok := toFloat(a); aok {
		if bf, bok := toFloat(b); bok {
			return af == bf
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}

func compareAny(op string, l, r any) (bool, error) {
	if l == nil || r == nil {
		return false, nil
	}
	if lf, ok := toFloat(l); ok {
		if rf, ok := toFloat(r); ok {
			switch op {
			case "<":
				return lf < rf, nil
			case ">":
				return lf > rf, nil
			case "<=":
				return lf <= rf, nil
			case ">=":
				return lf >= rf, nil
			}
		}
	}
	ls, lok := l.(string)
	rs, rok := r.(string)
	if lok && rok {
		switch op {
		case "<":
			return ls < rs, nil
		case ">":
			return ls > rs, nil
		case "<=":
			return ls <= rs, nil
		case ">=":
			return ls >= rs, nil
		}
	}
	return false, nil
}

func numericOp(op string, l, r any) (any, error) {
	lf, lok := toFloat(l)
	rf, rok := toFloat(r)
	if !lok || !rok {
		return nil, fmt.Errorf("full engine: numeric op %q on non-numeric (%T, %T)", op, l, r)
	}
	switch op {
	case "+":
		return lf + rf, nil
	case "-":
		return lf - rf, nil
	case "*":
		return lf * rf, nil
	case "/":
		if rf == 0 {
			return nil, errors.New("full engine: division by zero")
		}
		return lf / rf, nil
	case "%":
		if rf == 0 {
			return nil, errors.New("full engine: modulo by zero")
		}
		return float64(int64(lf) % int64(rf)), nil
	}
	return nil, fmt.Errorf("full engine: unsupported numeric op %q", op)
}

// normalizeForKey produces a stable string representation usable as a map
// group key. Maps are JSON-encoded with sorted keys for determinism.
func normalizeForKey(v any) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(k)
			b.WriteByte(':')
			b.WriteString(normalizeForKey(x[k]))
		}
		b.WriteByte('}')
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, el := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(normalizeForKey(el))
		}
		b.WriteByte(']')
		return b.String()
	case *nodeRef:
		if x == nil {
			return "<nil>"
		}
		return x.key
	}
	return fmt.Sprintf("%v", v)
}

// walkExprAll applies f to every expression node reachable from root.
// Independent of the test-only walker in parse_test.go so production code
// doesn't depend on test helpers.
func walkExprAll(root Expr, f func(Expr)) {
	if root == nil {
		return
	}
	f(root)
	switch e := root.(type) {
	case *AndOr:
		for _, op := range e.Operands {
			walkExprAll(op, f)
		}
	case *Not:
		walkExprAll(e.Operand, f)
	case *BinaryOp:
		walkExprAll(e.Left, f)
		walkExprAll(e.Right, f)
	case *PropertyAccess:
		walkExprAll(e.Target, f)
	case *FunctionCall:
		for _, a := range e.Args {
			walkExprAll(a, f)
		}
	case *MapLiteral:
		for _, k := range e.Keys {
			walkExprAll(e.Values[k], f)
		}
	case *ListLiteral:
		for _, el := range e.Elements {
			walkExprAll(el, f)
		}
	case *PatternComprehension:
		walkExprAll(e.Where, f)
		walkExprAll(e.Projection, f)
	}
}
