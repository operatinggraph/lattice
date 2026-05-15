package simple

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/refractor/adjacency"
)

// NodeEntry describes one Core KV node entry received from a rule consumer.
type NodeEntry struct {
	CoreKVKey  string         // full Core KV key, e.g. "node:agreement:abc123"
	NodeLabel  string         // label of this node, e.g. "agreement"
	IsDeleted  bool           // true when the "isDeleted" JSON field is true
	Properties map[string]any // all JSON fields from the payload (including "isDeleted")
}

// EvalResult is the evaluation output for one anchor entity.
type EvalResult struct {
	Delete bool           // true = issue a hard delete to the adapter
	Keys   map[string]any // key column values (always populated)
	Row    map[string]any // all projected column values; nil when Delete is true
}

// evalBinding holds the variable-to-node mapping for one traversal path in progress.
type evalBinding struct {
	varKeys  map[string]string         // variable → Core KV key ("" = NULL binding)
	varProps map[string]map[string]any // variable → properties (nil = NULL binding)
}

// Evaluate evaluates plan against the given Core KV entry, traversing the graph
// using adjKV (for edge lookups) and coreKV (for node property fetches).
//
// Returns:
//   - anchor node with isDeleted=true → one Delete result (no traversal)
//   - anchor node with isDeleted=false → one Upsert result per matched path
//   - non-anchor node → results for all affected anchor nodes via reverse traversal
func Evaluate(ctx context.Context, plan *QueryPlan, entry NodeEntry, adjKV, coreKV jetstream.KeyValue) ([]EvalResult, error) {
	if entry.NodeLabel == plan.AnchorLabel {
		return evaluateAnchor(ctx, plan, entry.CoreKVKey, entry.Properties, adjKV, coreKV)
	}
	// Non-anchor: reverse-traverse to find all affected anchor nodes.
	anchorKeys, err := reverseTraverse(ctx, plan, entry, adjKV)
	if err != nil {
		return nil, err
	}
	var results []EvalResult
	for _, anchorKey := range anchorKeys {
		props, isDeleted, err := fetchNodeProps(ctx, coreKV, anchorKey)
		if err != nil {
			return nil, fmt.Errorf("evaluate: fetch anchor %s: %w", anchorKey, err)
		}
		if props == nil {
			// Anchor no longer exists in core KV (deleted externally); skip silently.
			continue
		}
		if isDeleted {
			results = append(results, deleteResult(plan, props))
			continue
		}
		res, err := evaluateAnchor(ctx, plan, anchorKey, props, adjKV, coreKV)
		if err != nil {
			return nil, err
		}
		results = append(results, res...)
	}
	return results, nil
}

// evaluateAnchor performs forward traversal starting from anchorKey/anchorProps and
// projects all matched paths into EvalResults.
func evaluateAnchor(ctx context.Context, plan *QueryPlan, anchorKey string, anchorProps map[string]any, adjKV, coreKV jetstream.KeyValue) ([]EvalResult, error) {
	// Check isDeleted on the anchor itself.
	isDeleted, _ := anchorProps["isDeleted"].(bool)
	if isDeleted {
		return []EvalResult{deleteResult(plan, anchorProps)}, nil
	}

	initial := evalBinding{
		varKeys:  map[string]string{plan.AnchorVariable: anchorKey},
		varProps: map[string]map[string]any{plan.AnchorVariable: anchorProps},
	}
	current := []evalBinding{initial}

	for _, step := range plan.Steps {
		var next []evalBinding
		for _, b := range current {
			fromKey := b.varKeys[step.FromVariable]

			// If the FROM variable is NULL (from a prior optional miss), propagate NULL for TO.
			if fromKey == "" && step.Optional {
				nb := copyBinding(b)
				nb.varKeys[step.ToVariable] = ""
				nb.varProps[step.ToVariable] = nil
				next = append(next, nb)
				continue
			}
			// Required step with NULL from variable (cascaded from prior optional miss): skip.
			if fromKey == "" {
				continue
			}

			// Look up edges from the FROM node.
			neighbors, err := adjacency.Neighbors(adjKV, fromKey)
			if err != nil {
				return nil, fmt.Errorf("evaluateAnchor: neighbors(%s): %w", fromKey, err)
			}
			matching := filterEdges(neighbors, step.EdgeType, step.Direction)

			if len(matching) == 0 {
				if step.Optional {
					// OPTIONAL MATCH with no edge: bind ToVariable to NULL.
					nb := copyBinding(b)
					nb.varKeys[step.ToVariable] = ""
					nb.varProps[step.ToVariable] = nil
					next = append(next, nb)
				}
				// Required step with no edge: this binding produces no result — skip.
				continue
			}

			// Fan-out: one new binding per matching edge.
			for _, edge := range matching {
				neighborProps, _, err := fetchNodeProps(ctx, coreKV, edge.OtherNodeID)
				if err != nil {
					return nil, fmt.Errorf("evaluateAnchor: fetch neighbor %s: %w", edge.OtherNodeID, err)
				}
				nb := copyBinding(b)
				nb.varKeys[step.ToVariable] = edge.OtherNodeID
				nb.varProps[step.ToVariable] = neighborProps
				next = append(next, nb)
			}
		}
		current = next
		if len(current) == 0 {
			return nil, nil
		}
	}

	// Project each binding into a result row.
	results := make([]EvalResult, 0, len(current))
	for _, b := range current {
		row := make(map[string]any, len(plan.Columns))
		keys := make(map[string]any)
		for _, col := range plan.Columns {
			var val any
			props := b.varProps[col.Variable]
			if props != nil {
				if v, ok := props[col.Property]; ok {
					val = v
				}
				// absent property → val stays nil (FR36a)
			}
			// nil props (optional miss) → val stays nil
			row[col.Alias] = val
			if col.IsKey {
				keys[col.Alias] = val
			}
		}
		results = append(results, EvalResult{Delete: false, Keys: keys, Row: row})
	}
	return results, nil
}

// reverseTraverse finds all anchor Core KV keys affected by a change to a non-anchor node.
// It walks backward through the traversal steps from the changed node's label to the anchor.
func reverseTraverse(ctx context.Context, plan *QueryPlan, entry NodeEntry, adjKV jetstream.KeyValue) ([]string, error) {
	seen := map[string]struct{}{}

	for stepIdx, step := range plan.Steps {
		if step.ToLabel != entry.NodeLabel {
			continue
		}
		// Walk backward from entry through steps [stepIdx, ..., 0] to reach the anchor.
		keys, err := walkBackToAnchor(ctx, plan, stepIdx, []string{entry.CoreKVKey}, adjKV)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			seen[k] = struct{}{}
		}
	}

	result := make([]string, 0, len(seen))
	for k := range seen {
		result = append(result, k)
	}
	return result, nil
}

// walkBackToAnchor recursively walks backward through plan.Steps[0..stepIdx] starting
// from startKeys, reversing each hop, until it reaches the anchor nodes at step 0.
func walkBackToAnchor(ctx context.Context, plan *QueryPlan, stepIdx int, startKeys []string, adjKV jetstream.KeyValue) ([]string, error) {
	step := plan.Steps[stepIdx]
	reverseDir := reverseDirection(step.Direction)

	var prevKeys []string
	for _, nodeKey := range startKeys {
		neighbors, err := adjacency.Neighbors(adjKV, nodeKey)
		if err != nil {
			return nil, fmt.Errorf("walkBack: neighbors(%s): %w", nodeKey, err)
		}
		matching := filterEdges(neighbors, step.EdgeType, reverseDir)
		for _, edge := range matching {
			prevKeys = append(prevKeys, edge.OtherNodeID)
		}
	}

	if len(prevKeys) == 0 {
		return nil, nil
	}

	if stepIdx == 0 {
		// Reached anchor level.
		return prevKeys, nil
	}

	return walkBackToAnchor(ctx, plan, stepIdx-1, prevKeys, adjKV)
}

// filterEdges returns only the edges matching edgeType and direction.
func filterEdges(edges []adjacency.EdgeEntry, edgeType string, dir EdgeDirection) []adjacency.EdgeEntry {
	var result []adjacency.EdgeEntry
	for _, e := range edges {
		if e.Name != edgeType {
			continue
		}
		switch dir {
		case Outbound:
			if e.Direction == "outbound" {
				result = append(result, e)
			}
		case Inbound:
			if e.Direction == "inbound" {
				result = append(result, e)
			}
		case Both:
			result = append(result, e)
		}
	}
	return result
}

// reverseDirection returns the logical opposite direction for reverse-edge lookup.
func reverseDirection(dir EdgeDirection) EdgeDirection {
	switch dir {
	case Outbound:
		return Inbound
	case Inbound:
		return Outbound
	default:
		return Both
	}
}

// fetchNodeProps fetches a node's JSON properties from coreKV by key.
// Returns the properties map, the isDeleted flag, and any fetch/parse error.
func fetchNodeProps(ctx context.Context, kv jetstream.KeyValue, key string) (map[string]any, bool, error) {
	entry, err := kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("fetchNodeProps: get %q: %w", key, err)
	}
	var props map[string]any
	if err := json.Unmarshal(entry.Value(), &props); err != nil {
		return nil, false, fmt.Errorf("fetchNodeProps: unmarshal %q: %w", key, err)
	}
	isDeleted, _ := props["isDeleted"].(bool)
	return props, isDeleted, nil
}

// deleteResult builds a Delete EvalResult by extracting key column values from anchorProps.
func deleteResult(plan *QueryPlan, anchorProps map[string]any) EvalResult {
	keys := make(map[string]any)
	for _, col := range plan.Columns {
		if !col.IsKey {
			continue
		}
		if col.Variable == plan.AnchorVariable {
			keys[col.Alias] = anchorProps[col.Property]
		}
	}
	return EvalResult{Delete: true, Keys: keys, Row: nil}
}

// copyBinding deep-copies an evalBinding's maps so fan-out paths don't share state.
func copyBinding(b evalBinding) evalBinding {
	nk := make(map[string]string, len(b.varKeys))
	for k, v := range b.varKeys {
		nk[k] = v
	}
	np := make(map[string]map[string]any, len(b.varProps))
	for k, v := range b.varProps {
		np[k] = v // shallow copy of props is safe — evaluator never mutates individual props maps
	}
	return evalBinding{varKeys: nk, varProps: np}
}
