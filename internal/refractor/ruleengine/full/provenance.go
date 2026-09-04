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
	// stage marks a clause's or a pattern's stage node (provStage): the one
	// node linked into EVERY binding that survived a discard, and so the only
	// node one output row's fold reaches that another output row's fold
	// reaches too. It is what the fold memoizes a whole subtree for.
	stage bool
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
// in-flight set is what keeps the link from being followed back into it.
func provAbsorb(dst, src *provNode) {
	if dst == nil || src == nil || dst == src {
		return
	}
	dst.merged = append(dst.merged, src)
}

// provAttachStage links a clause's STAGE into every row that survived it. A
// clause discards rows — a required MATCH that admits no expansion of a source
// binding, a WHERE, a DISTINCT — and a discarded row's chain is a leaf: it
// hangs off its own source binding and no surviving row descends from it, so
// what it read leaves the evaluation with it unless the rows standing in for
// it name it. The stage is that naming, and it is linked once the clause has
// finished discarding, so a clause that discarded nothing links nothing and a
// row the clause itself dropped carries none of it.
//
// Which surviving row stands in for a discarded one is not knowable — an
// aggregate downstream may fold any of them — so the stage reaches all of
// them. That is the mechanism's one deliberate imprecision: a vertex only a
// discarded row read republishes every row of its clause, never a wrong row.
func provAttachStage(rows []binding, stage *provNode) {
	if !provStageReaches(stage) {
		return
	}
	for _, row := range rows {
		provAbsorb(provChain(row), stage)
	}
}

// provStage is the node a clause or a pattern collects its discards on before
// linking it into the bindings that survived.
func provStage() *provNode { return &provNode{stage: true} }

// provStageReaches reports whether a stage holds anything at all — whether the
// clause or the pattern that owns it discarded something. A stage that
// discarded nothing is linked nowhere, so the ordinary case costs the fold no
// traversal.
func provStageReaches(stage *provNode) bool {
	return stage != nil && (len(stage.keys) > 0 || len(stage.merged) > 0)
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
	return AppendProvenanceVertexKeys(dst, key)
}

// AppendProvenanceVertexKeys is the Contract #1 fold a row's provenance names
// a read by — the one the engine records with and the pipeline folds a
// branch's read set with, so the two can never disagree on what vertex a key
// belongs to. A vertex key is appended as itself, an aspect key as its parent
// vertex, a link key as both of its endpoints; a key of no Contract #1 shape
// is dropped. A key equal to the last one appended is not appended again.
func AppendProvenanceVertexKeys(dst []string, key string) []string {
	switch substrate.ClassifyKey(key) {
	case substrate.KindVertex:
		return appendProvVertexKey(dst, key)
	case substrate.KindAspect:
		if vtx, _, _, _, ok := substrate.ParseAspectKey(key); ok {
			return appendProvVertexKey(dst, vtx)
		}
	case substrate.KindLink:
		if t1, id1, _, t2, id2, ok := substrate.ParseLinkKey(key); ok {
			if dst == nil {
				// Both endpoints land in one slice: sized for the pair up
				// front, the second append never grows a one-element array.
				dst = make([]string, 0, 2)
			}
			dst = appendProvVertexKey(dst, substrate.VertexKey(t1, id1))
			return appendProvVertexKey(dst, substrate.VertexKey(t2, id2))
		}
	}
	return dst
}

// appendProvVertexKey appends vtx unless the tail of dst already names it.
// Consecutive reads of one vertex are one dependency however many times the
// evaluation dereferences it — a body followed by its own aspect, a ranged
// hop stepping from the same frontier node on every pass — and dropping the
// repeat keeps a walk's record proportional to the vertices it touched rather
// than to the reads it made. It is a tail check rather than a set because the
// record is appended to on every read: a map per chain would cost more than
// the duplicates it removes.
func appendProvVertexKey(dst []string, vtx string) []string {
	if n := len(dst); n > 0 && dst[n-1] == vtx {
		return dst
	}
	return append(dst, vtx)
}

// provVertexKeys returns the sorted, deduplicated vertex keys n's chain
// records — the row's Provenance. Every node the fold completes is memoized,
// not just the one it was asked for: the production caller asks per output
// row, output rows share their ancestors and share nothing else, so a head's
// candidate set is folded on the first row and read back on the rest.
//
// The memo is only sound while the chains are frozen. Every absorb this
// evaluation performs — a discarded expansion, a dropped row — happens while
// its clause runs, and the first fold is asked for after the last clause has,
// so no memoized answer can be widened behind the caller's back.
func (ex *executor) provVertexKeys(n *provNode) []string {
	if n == nil {
		return nil
	}
	if folded, ok := ex.provFolded[n]; ok {
		return folded
	}
	f := provFolder{ex: ex, inFlight: map[*provNode]int{}}
	out, _ := f.fold(n, 0)
	return out
}

// provFoldComplete is the "nothing was cut" answer a fold reports upward: no
// node still in flight was reached beneath it, so what it computed is that
// node's whole closure.
const provFoldComplete = int(^uint(0) >> 1)

// provFolder folds one call of provVertexKeys. inFlight holds the nodes whose
// own fold has not returned yet, mapped to their depth: the chains are a DAG
// with back-links — a head that absorbed a dead descendant, a stage node that
// absorbed a row it discarded — and reaching one of those nodes again has to
// terminate the walk rather than the walk terminating the evaluation.
//
// A node reached while in flight contributes nothing where it is reached; its
// keys are unioned where the fold entered it. That leaves every fold BENEATH
// the entered node short of what the entered node holds, so a fold reports the
// shallowest node it cut back to and only a fold that cut nothing shallower
// than itself is memoized.
type provFolder struct {
	ex       *executor
	inFlight map[*provNode]int
}

// fold returns cur's closure and the depth of the shallowest in-flight node
// cut beneath it (provFoldComplete when it cut none).
func (f *provFolder) fold(cur *provNode, depth int) ([]string, int) {
	if cur == nil {
		return nil, provFoldComplete
	}
	if folded, ok := f.ex.provFolded[cur]; ok {
		return folded, provFoldComplete
	}
	if d, busy := f.inFlight[cur]; busy {
		return nil, d
	}
	f.inFlight[cur] = depth
	inherited, cut := f.fold(cur.parent, depth+1)
	absorbed, absorbedCut := f.absorbed(cur, depth)
	delete(f.inFlight, cur)
	if absorbedCut < cut {
		cut = absorbedCut
	}
	out := provUnion(inherited, cur.keys, absorbed)
	if cut >= depth {
		// Everything cut beneath this fold pointed back at this node or below
		// it, and this node is where what those cuts skipped is unioned — so
		// out is cur's whole closure and stands for every later walk that
		// reaches it.
		if f.ex.provFolded != nil {
			f.ex.provFolded[cur] = out
		}
		cut = provFoldComplete
	}
	return out, cut
}

// absorbed unions what cur's merged branches reach. A branch is one of two
// shapes, and each is walked the way its own sharing calls for:
//
//   - A STAGE branch is linked into every binding that survived its clause, so
//     it is the one branch reached once per OUTPUT ROW. It is folded through
//     the shared memo, and every row after the first reads its closure back
//     instead of re-walking the subtree of discards behind it — the cost that
//     otherwise scales as the rows a clause projects times the bindings it
//     dropped. The fold's own in-flight guard is what terminates the back-link
//     from a stage to a binding it absorbed.
//   - Every OTHER branch is reached through one row's own ancestry or through a
//     stage — a grouped row's members, an abandoned frontier — so it is reached
//     once whether or not it carries a memo. Those are walked as ONE traversal
//     rather than a fold each, because a memo per member would materialize the
//     head they share once per member where the traversal visits it once
//     between them. A branch that already carries a memo is taken from it, and
//     nothing the traversal itself computes is memoized.
func (f *provFolder) absorbed(cur *provNode, depth int) ([]string, int) {
	if len(cur.merged) == 0 || provReachesNothing(cur.merged) {
		return nil, provFoldComplete
	}
	cut := provFoldComplete
	var stages []string
	var walk []*provNode
	for _, n := range cur.merged {
		switch {
		case n == nil:
		case n.stage:
			out, c := f.fold(n, depth+1)
			if c < cut {
				cut = c
			}
			stages = provMergeSorted(stages, out)
		default:
			walk = append(walk, n)
		}
	}
	if len(walk) == 0 {
		return stages, cut
	}
	walked, walkedCut := f.walkBranches(walk)
	if walkedCut < cut {
		cut = walkedCut
	}
	return provMergeSorted(stages, walked), cut
}

// walkBranches unions what these branches reach as one traversal, taking each
// of them the way a fold would: own keys, ancestors, and anything they
// absorbed in turn.
func (f *provFolder) walkBranches(branches []*provNode) ([]string, int) {
	cut := provFoldComplete
	set := make(map[string]struct{})
	seen := make(map[*provNode]struct{}, len(branches))
	stack := make([]*provNode, len(branches))
	copy(stack, branches)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		if d, busy := f.inFlight[n]; busy {
			if d < cut {
				cut = d
			}
			continue
		}
		if folded, ok := f.ex.provFolded[n]; ok {
			for _, k := range folded {
				set[k] = struct{}{}
			}
			continue
		}
		for _, k := range n.keys {
			set[k] = struct{}{}
		}
		if n.parent != nil {
			stack = append(stack, n.parent)
		}
		stack = append(stack, n.merged...)
	}
	return provSortedSet(set), cut
}

// provReachesNothing reports whether these branches hold nothing and lead
// nowhere. A hand-off is made wherever a binding is abandoned, including where
// the abandoned binding had read nothing yet — a frontier whose own hop fetched
// from a memo already folded into its ancestors — and such a branch must cost
// the fold no traversal at all.
func provReachesNothing(nodes []*provNode) bool {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if len(n.keys) > 0 || n.parent != nil || len(n.merged) > 0 {
			return false
		}
	}
	return true
}

// provUnion renders one node's answer: the sorted closure it inherits, the raw
// keys it recorded itself, and the sorted closure its merged branches reach.
// The inherited slice is returned as-is when the node adds nothing to it, so a
// row whose own reads are all its ancestors' costs no allocation at all; every
// other case merges sorted runs rather than passing a long closure back
// through a map.
func provUnion(inherited, own, absorbed []string) []string {
	if len(own) == 0 && len(absorbed) == 0 {
		return inherited
	}
	out := provMergeSorted(inherited, provSortedUnique(own))
	return provMergeSorted(out, absorbed)
}

// provSortedUnique sorts and deduplicates a copy of keys, which are appended
// in read order and deduplicated only against the tail as they are recorded.
func provSortedUnique(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, len(keys))
	copy(out, keys)
	sort.Strings(out)
	w := 1
	for i := 1; i < len(out); i++ {
		if out[i] == out[w-1] {
			continue
		}
		out[w] = out[i]
		w++
	}
	return out[:w:w]
}

// provMergeSorted unions two sorted, deduplicated slices. The result's
// capacity is its length, so a caller appending to one node's answer cannot
// write into the memo another node shares with it.
func provMergeSorted(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make([]string, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] < b[j]:
			out = append(out, a[i])
			i++
		case a[i] > b[j]:
			out = append(out, b[j])
			j++
		default:
			out = append(out, a[i])
			i++
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out[:len(out):len(out)]
}

// provSortedSet renders a set of vertex keys as the sorted slice the fold
// carries, nil for an empty one.
func provSortedSet(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
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
