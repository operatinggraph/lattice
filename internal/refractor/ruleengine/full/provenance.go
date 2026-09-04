package full

import (
	"sort"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// provBindingKey is the binding entry a row's provenance chain hangs off. The
// NUL byte is what keeps it out of the variable namespace: no Cypher
// identifier and no WITH alias can carry one, so the entry can never collide
// with a name a rule binds, and every place that renders a whole binding —
// the two DISTINCT sites and RETURN's values copy — strips it rather than
// carrying it into a grouping key or onto the wire.
const provBindingKey = "\x00prov"

// provNode is one link of a binding's provenance chain: the Core KV keys read
// while that binding was being built, plus the links back to the bindings it
// descends from. A clone points at its source instead of copying it, so a
// head's candidate set is held once however many rows descend from it. parent
// carries the single ancestor a clone has; merged carries the several a
// grouped row folds together.
type provNode struct {
	parent *provNode
	merged []*provNode
	keys   []string
}

// provChain returns the chain b records onto, or nil for a binding built
// outside an evaluation that records — the read-free key-resolution executor,
// which fetches nothing.
func provChain(b binding) *provNode {
	n, _ := b[provBindingKey].(*provNode)
	return n
}

// provParent returns the chain of the binding b was cloned from.
func provParent(b binding) *provNode {
	n := provChain(b)
	if n == nil {
		return nil
	}
	return n.parent
}

// provRoot is the chain the empty binding every evaluation starts from
// carries. Every other chain descends from it, so a read taken before any
// pattern is seeded still lands somewhere.
func provRoot() *provNode { return &provNode{} }

// provAbsorb hands src's reads to dst — what a binding that will project no
// row of its own owes the rows that stand in for it. Absorbing a chain takes
// its ancestors with it, since a chain names them; absorbing one that already
// descends from dst therefore costs nothing but a link, and the fold's
// visited set is what keeps that from being walked twice.
func provAbsorb(dst, src *provNode) {
	if dst == nil || src == nil || dst == src {
		return
	}
	dst.merged = append(dst.merged, src)
}

// recordProv notes that this evaluation read key on behalf of the row the
// current recording target names: the row cursor when one is set — a
// projection item, a WHERE, a pattern comprehension and an existence
// predicate all evaluate under one — and otherwise the head chain the MATCH
// walk currently expanding is bound to. With neither set nothing is recorded.
func (ex *executor) recordProv(key string) {
	target := ex.provCursor
	if target == nil {
		target = ex.provHead
	}
	if target == nil {
		return
	}
	target.keys = appendProvVertexKeys(target.keys, key)
}

// provPushRow makes n the row every read records on until the returned value
// is handed back to provPopRow. A cursor already set STANDS: an inner
// evaluation — a pattern comprehension's walk, a decomposed branch's MATCH —
// reads on behalf of the row that triggered it, and the clones it discards
// must not absorb those reads.
func (ex *executor) provPushRow(n *provNode) *provNode {
	prev := ex.provCursor
	if prev == nil {
		ex.provCursor = n
	}
	return prev
}

// provPopRow restores the cursor provPushRow returned.
func (ex *executor) provPopRow(prev *provNode) { ex.provCursor = prev }

// provRowTarget is the chain one row's expressions record on: an enclosing
// row's when there is one, this row's own otherwise. It is provPushRow's rule
// for a caller that sets the cursor across a loop body rather than around a
// single call.
func provRowTarget(outer *provNode, b binding) *provNode {
	if outer != nil {
		return outer
	}
	return provChain(b)
}

// appendProvVertexKeys folds one Core KV key onto the VERTEX keys a row's
// provenance names it by — the granularity the CDC arms publish at — and
// appends them to dst: a vertex key stands for itself, an aspect key for its
// parent vertex, a link key for both of its endpoints. A key of no Contract #1
// shape names no vertex and is dropped rather than guessed at.
func appendProvVertexKeys(dst []string, key string) []string {
	switch substrate.ClassifyKey(key) {
	case substrate.KindVertex:
		return append(dst, key)
	case substrate.KindAspect:
		if vtx, _, _, _, ok := substrate.ParseAspectKey(key); ok {
			return append(dst, vtx)
		}
	case substrate.KindLink:
		if t1, id1, _, t2, id2, ok := substrate.ParseLinkKey(key); ok {
			return append(dst, substrate.VertexKey(t1, id1), substrate.VertexKey(t2, id2))
		}
	}
	return dst
}

// provVertexKeys returns the sorted, deduplicated vertex keys n's chain
// records — the row's Provenance. The walk is over a DAG: a chain shared by
// many rows (a head's candidate set, a group member folded into several
// aggregates) is visited once per row, and once a node's own answer is
// memoized every later walk that reaches it takes that answer instead of its
// ancestors.
func (ex *executor) provVertexKeys(n *provNode) []string {
	if n == nil {
		return nil
	}
	if folded, ok := ex.provFolded[n]; ok {
		return folded
	}
	set := make(map[string]struct{})
	seen := make(map[*provNode]struct{})
	stack := []*provNode{n}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if cur == nil {
			continue
		}
		if _, dup := seen[cur]; dup {
			continue
		}
		seen[cur] = struct{}{}
		if folded, ok := ex.provFolded[cur]; ok {
			for _, k := range folded {
				set[k] = struct{}{}
			}
			continue
		}
		for _, k := range cur.keys {
			set[k] = struct{}{}
		}
		if cur.parent != nil {
			stack = append(stack, cur.parent)
		}
		stack = append(stack, cur.merged...)
	}
	var out []string
	if len(set) > 0 {
		out = make([]string, 0, len(set))
		for k := range set {
			out = append(out, k)
		}
		sort.Strings(out)
	}
	if ex.provFolded != nil {
		ex.provFolded[n] = out
	}
	return out
}

// provStripped renders row without its provenance chain — the map identity
// DISTINCT compares and RETURN copies into a result's values. Rendering the
// chain instead would key every row on a pointer, so two rows differing only
// in what they read would stop deduplicating, and a chain would reach the
// wire in a projected row's data.
func provStripped(row binding) map[string]any {
	if _, carried := row[provBindingKey]; !carried {
		return map[string]any(row)
	}
	out := make(map[string]any, len(row)-1)
	for k, v := range row {
		if k == provBindingKey {
			continue
		}
		out[k] = v
	}
	return out
}
