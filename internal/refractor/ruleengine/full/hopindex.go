package full

// AnchorHopIndex derives the compiled query's PATTERN GRAPH: the positions its
// patterns can bind, the typed relationship hops between them, and which
// position is the `$actorKey` anchor. It is the structural half of
// auth-plane-projection-latency-design.md §4.7 — the half that reads only the
// AST, so the data walk that consumes it can be seeded by the changed element
// instead of crawling adjacency undirected from a neighbour.
//
// It is the third derivation over the same clause and expression shapes as
// ReferencedLabels and ReferencedRelations, and it walks them in lockstep
// deliberately: those two bound which endpoint TYPES and which RELATIONS a link
// mutation can reach, this one bounds WHERE in the pattern each of them sits.
// A pattern position one derivation reads and another does not would make the
// three disagree about the same link key.
//
// Complete == false means the index is NOT authoritative and the caller must
// not use it at all — neither to derive a set nor to skip. "Not in the index"
// and "not indexable" are different answers, and only the first one licenses a
// skip.

import "fmt"

// PatternHop is one typed relationship in the pattern graph, oriented from From
// to To. Dir describes the arrow as written between them: DirOut means the link
// runs From→To, DirIn means To→From, DirBoth means either satisfies the pattern.
type PatternHop struct {
	Rel  string
	From int
	To   int
	Dir  Direction

	// Binding is true when this hop comes from a MATCH clause's own pattern —
	// the only source that BINDS a variable. A hop written inside a WHERE, a
	// negated pattern, or a RETURN comprehension is a filter or a projection:
	// it can be walked (so it stays in Hops, and must — an unavailableAt create
	// is a revocation), but it never establishes a position's binding, so it
	// must not be counted when asking how far a position sits from the anchor.
	Binding bool
}

// HopIndex is the pattern graph. Positions are indices into Labels: each is an
// equivalence class of pattern node positions, merged by variable name where
// the pattern gives one. Labels[i] is that position's vertex-type label, or ""
// when the position is unlabeled (which binds ANY type — the property that lets
// the derivation reach `capabilityEphemeral`'s three unlabeled targets).
//
// Anchor is the position bound to `$actorKey`. Dist[i] is position i's hop
// distance from the anchor over the BINDING hops only (-1 when it has none),
// which is what tells a link event which of its two endpoints sits anchor-side.
// Counting a non-binding hop here would be a real defect rather than a
// pessimisation: a WHERE-NOT shortcut to the anchor can make the far endpoint
// look nearer, and the walk would then be seeded at the endpoint that can only
// reach the anchor by crossing the very edge a tombstone just removed.
//
// Hops carries every typed relationship, from the required patterns and the
// OPTIONAL / WHERE / RETURN pattern sources alike; a relation appearing at
// several hops appears several times, and the consumer walks all of them.
type HopIndex struct {
	Labels   []string
	Hops     []PatternHop
	Dist     []int
	Anchor   int
	Complete bool

	// Incomplete records why Complete is false — for the health surface, and so
	// a test can assert on the REASON rather than only on the verdict.
	Incomplete string
}

// AnchorHopIndex builds the pattern graph for cr. See §16.2 of the design for
// the completeness predicate; each conjunct states its own reason below.
func (cr *CompiledRule) AnchorHopIndex() HopIndex {
	if cr == nil || cr.Query == nil {
		return HopIndex{Anchor: -1, Incomplete: "no compiled query"}
	}

	// A WITH rebuilds every binding from its projection items alone, so a
	// variable it does not carry is unbound downstream and an unlabeled pattern
	// node re-using that name re-seeds through the whole-bucket scan
	// (labels.go's scope argument). That re-seeded position depends on its
	// BUCKET, not on any link, so an adjacency walk cannot see the dependency
	// and would report a SMALLER affected set than the truth. Refusing the
	// whole query is the only fail-closed answer: the derivation's invariant is
	// a superset, and this is the one shape that breaks it downward.
	for _, c := range cr.Query.Clauses {
		if _, isWith := c.(*With); isWith {
			return HopIndex{Anchor: -1, Incomplete: "query carries a WITH — a dropped variable re-seeds by bucket scan, not by link"}
		}
	}

	b := &hopIndexBuilder{byVar: make(map[string]int), anchor: -1}
	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			for _, p := range cl.Patterns {
				b.addPattern(p, true)
			}
			b.addExpr(cl.Where)
		case *Return:
			for _, it := range cl.Items {
				b.addExpr(it.Expr)
			}
		}
	}

	idx := HopIndex{Labels: b.labels, Hops: b.hops, Anchor: b.anchor}
	idx.Dist = idx.distances()

	// Ordered so the most fundamental refusal wins: without an anchor, nothing
	// downstream — grounding included — means anything.
	switch {
	case b.anchor < 0:
		idx.Incomplete = "no pattern position binds $actorKey"
	case b.multiAnchor:
		idx.Incomplete = "several pattern positions bind $actorKey"
	case b.reject != "":
		idx.Incomplete = b.reject
	case b.ungrounded != "":
		// The conjunct whose omission is unsound. Every other one widens the
		// derived set; this one would shrink it to empty and license a skip
		// that drops a live revocation.
		idx.Incomplete = b.ungrounded
	}
	idx.Complete = idx.Incomplete == ""
	return idx
}

func describeLabel(l string) string {
	if l == "" {
		return "unlabeled"
	}
	return l
}

// distances returns each position's undirected distance from the anchor over
// the BINDING hops, or -1 where it has no binding path. Undirected is right:
// the question is whether the executor's binding of that position is
// established by walking from the anchor's, and a hop binds in whichever
// direction the walk crosses it.
func (ix HopIndex) distances() []int {
	d := make([]int, len(ix.Labels))
	for i := range d {
		d[i] = -1
	}
	if ix.Anchor < 0 || ix.Anchor >= len(d) {
		return d
	}
	d[ix.Anchor] = 0
	queue := []int{ix.Anchor}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, h := range ix.Hops {
			if !h.Binding {
				continue
			}
			var next int
			switch {
			case h.From == cur:
				next = h.To
			case h.To == cur:
				next = h.From
			default:
				continue
			}
			if d[next] < 0 {
				d[next] = d[cur] + 1
				queue = append(queue, next)
			}
		}
	}
	return d
}

type hopIndexBuilder struct {
	labels      []string
	hops        []PatternHop
	byVar       map[string]int
	anchor      int
	multiAnchor bool
	reject      string

	// grounded holds the positions the executor reaches by walking FROM the
	// anchor. A pattern whose head is not already grounded when the clause is
	// reached is seeded by a bucket scan instead (executor.matchPath seeds
	// Nodes[0] only), so every vertex of that type binds and every anchor's row
	// depends on it — the cartesian case §16.2's fifth conjunct exists to
	// refuse. Reachability in the hop graph is NOT the same property and does
	// not establish it: one optional or negated hop back to the anchor makes a
	// scan-seeded position look connected while it still binds by scan.
	grounded   map[int]bool
	ungrounded string
}

// position returns the index of the equivalence class for a pattern node,
// creating it if new. A named node merges with every other occurrence of that
// name; an unnamed node is always its own class. Merging can only ADD hops
// between existing positions, and an added hop only widens the derived set, so
// merging is safe in the invariant's direction — it is what joins
// `capabilityEphemeral`'s separate OPTIONAL MATCH clauses into one graph.
//
// A position's label is fixed at its FIRST occurrence and is never upgraded by
// a later one. The first occurrence is what BINDS the variable; a later label
// is a re-reference filter, and in an OPTIONAL MATCH, a negated pattern, or a
// comprehension a failed filter restores the row with the original binding
// intact (executor.nullBindNewVars nulls only variables not already bound). So
// a later label does not constrain what the position can hold, and adopting it
// would narrow PositionsBinding below what the executor really binds — the one
// direction this derivation must never move in.
func (b *hopIndexBuilder) position(n NodePattern) int {
	if n.Variable == "" {
		b.labels = append(b.labels, n.Label)
		return len(b.labels) - 1
	}
	i, seen := b.byVar[n.Variable]
	if !seen {
		b.labels = append(b.labels, n.Label)
		i = len(b.labels) - 1
		b.byVar[n.Variable] = i
	}
	return i
}

func (b *hopIndexBuilder) addPattern(p PathPattern, binding bool) {
	// Property maps are general expressions the executor really evaluates
	// (propsAllMatch -> evalExpr), and one may hold a PatternExpr or
	// PatternComprehension binding a position no other clause mentions — which
	// a complete graph has to contain.
	for _, n := range p.Nodes {
		for _, v := range n.Properties {
			b.addExpr(v)
		}
	}
	for _, r := range p.Rels {
		for _, v := range r.Properties {
			b.addExpr(v)
		}
	}

	positions := make([]int, len(p.Nodes))
	for i, n := range p.Nodes {
		positions[i] = b.position(n)
		if nodePinsActor(n) {
			b.noteAnchor(positions[i])
		}
	}
	b.ground(positions)

	// Rels[i] joins Nodes[i] and Nodes[i+1] BY INDEX (executor.go's pattern
	// walk), independent of whether either node carries a variable.
	for i, r := range p.Rels {
		if i+1 >= len(positions) {
			// len(Rels) == len(Nodes)-1 by construction; a malformed pattern is
			// refused rather than silently half-indexed.
			b.rejectOnce("pattern has more relationships than node gaps")
			return
		}
		switch {
		case r.Type == "":
			// An untyped hop matches any relation, so it cannot be indexed by
			// relation name — the arm ReferencedRelations fails exhaustiveness
			// on, for the same reason.
			b.rejectOnce("pattern carries an untyped relationship")
		case r.MinHops != 1 || r.MaxHops != 1:
			// The intermediate NODES are the problem here, not the relation: a
			// walk crossing a variable-length hop cannot be stepped
			// hop-by-hop, because the number of adjacency reads between the two
			// bound positions is not known from the pattern.
			b.rejectOnce("pattern carries a variable-length relationship")
		default:
			b.hops = append(b.hops, PatternHop{Rel: r.Type, From: positions[i], To: positions[i+1], Dir: r.Direction, Binding: binding})
		}
	}
}

// ground marks a pattern's positions as anchor-grounded when its HEAD already
// is. executor.matchPath seeds only Nodes[0] and reaches every later node by
// traversal, so a pattern headed by an already-bound variable extends the
// grounded set, and one headed by anything else starts a fresh bucket scan.
// Clause order is load-bearing and matches the executor's.
func (b *hopIndexBuilder) ground(positions []int) {
	if len(positions) == 0 || b.anchor < 0 {
		return
	}
	if b.grounded == nil {
		b.grounded = map[int]bool{b.anchor: true}
	}
	if !b.grounded[positions[0]] {
		if b.ungrounded == "" {
			b.ungrounded = fmt.Sprintf("pattern headed by position %d (%s) is not reached from the anchor — it binds by bucket scan, so it affects every anchor", positions[0], describeLabel(b.labels[positions[0]]))
		}
		return
	}
	for _, pos := range positions {
		b.grounded[pos] = true
	}
}

// noteAnchor records the $actorKey position. Two distinct positions binding it
// is refused rather than resolved: the walk has one destination, and guessing
// which is under-approximation by another name.
func (b *hopIndexBuilder) noteAnchor(pos int) {
	if b.anchor >= 0 && b.anchor != pos {
		b.multiAnchor = true
		return
	}
	b.anchor = pos
}

func (b *hopIndexBuilder) rejectOnce(reason string) {
	if b.reject == "" {
		b.reject = reason
	}
}

// nodePinsActor reports whether n is pinned to exactly one vertex by
// `{key: $actorKey}` — the shape every actorAggregate lens opens with, and the
// only shape the walk's two load-bearing steps are entitled to assume.
//
// The property name and the expression are both checked exactly, and neither
// check is pedantry. `key` is the one property the executor point-reads on
// (executor.seedNodes); any other property makes the position a label-prefix
// SCAN filtered by that property, so it binds many vertices — which would break
// both the "never expand from the anchor" rule and the vtx.<type>.<id> key this
// walk mints. And `$actorKey` must be the whole expression, not merely
// contained in it: `{key: $actorKey + '-shadow'}` pins a DIFFERENT vertex.
func nodePinsActor(n NodePattern) bool {
	v, ok := n.Properties["key"]
	if !ok {
		return false
	}
	ref, isParam := v.(*ParameterRef)
	return isParam && ref.Name == "actorKey"
}

func (b *hopIndexBuilder) addExpr(e Expr) {
	switch x := e.(type) {
	case nil:
	case *PropertyAccess:
		b.addExpr(x.Target)
	case *BinaryOp:
		b.addExpr(x.Left)
		b.addExpr(x.Right)
	case *AndOr:
		for _, op := range x.Operands {
			b.addExpr(op)
		}
	case *Not:
		b.addExpr(x.Operand)
	case *PatternExpr:
		b.addPattern(x.Pattern, false)
	case *PatternComprehension:
		b.addPattern(x.Pattern, false)
		b.addExpr(x.Where)
		b.addExpr(x.Projection)
	case *FunctionCall:
		for _, a := range x.Args {
			b.addExpr(a)
		}
	case *MapLiteral:
		for _, v := range x.Values {
			b.addExpr(v)
		}
	case *ListLiteral:
		for _, el := range x.Elements {
			b.addExpr(el)
		}
	case *CaseExpr:
		for _, alt := range x.Alternatives {
			b.addExpr(alt.When)
			b.addExpr(alt.Then)
		}
		b.addExpr(x.Else)
	}
}

// PositionsBinding returns every position whose label admits vertexType — the
// node-seeded entry point (§4.7's 3b). An UNLABELED position matches any type,
// which is what lets the derivation reach lenses whose label set is not
// exhaustive and which Increments 1–2 therefore cannot narrow.
func (ix HopIndex) PositionsBinding(vertexType string) []int {
	var out []int
	for i, l := range ix.Labels {
		if l == "" || l == vertexType {
			out = append(out, i)
		}
	}
	return out
}

// AnchorSideSeeds returns the seeds a link `srcType -rel-> dstType` contributes:
// for every hop the link can bind, the hop's ANCHOR-SIDE position paired with
// the endpoint sitting there (§4.7's 3a — "the far endpoint's other edges are
// never traversed"). srcIsAnchorSide says which of the caller's two endpoint
// ids to use.
//
// A hop written DirIn is stored From←To, so the link's source sits at To. A
// DirBoth hop can bind either way round and contributes both readings. Equal
// distances contribute both endpoints, since neither is provably nearer.
//
// An empty result on a COMPLETE index means the link cannot bind any hop, so no
// anchor's output can change — the skip §4.7 licenses. On an incomplete index
// the caller must not read it that way.
func (ix HopIndex) AnchorSideSeeds(srcType, rel, dstType string) []Seed {
	admits := func(pos int, typ string) bool {
		l := ix.Labels[pos]
		return l == "" || l == typ
	}
	var out []Seed
	consider := func(srcPos, dstPos int) {
		ds, dd := ix.Dist[srcPos], ix.Dist[dstPos]
		// A position with no BINDING path to the anchor (its hops are all
		// filters or projections) has no comparable distance, so neither
		// endpoint is provably nearer and both are seeded. Seeding both only
		// widens the derived set, which is the safe direction.
		if ds < 0 || dd < 0 {
			out = append(out,
				Seed{Pos: srcPos, SrcIsAnchorSide: true},
				Seed{Pos: dstPos, SrcIsAnchorSide: false})
			return
		}
		if ds <= dd {
			out = append(out, Seed{Pos: srcPos, SrcIsAnchorSide: true})
		}
		if dd <= ds {
			out = append(out, Seed{Pos: dstPos, SrcIsAnchorSide: false})
		}
	}
	for _, h := range ix.Hops {
		if h.Rel != rel {
			continue
		}
		if (h.Dir == DirOut || h.Dir == DirBoth) && admits(h.From, srcType) && admits(h.To, dstType) {
			consider(h.From, h.To)
		}
		if (h.Dir == DirIn || h.Dir == DirBoth) && admits(h.To, srcType) && admits(h.From, dstType) {
			consider(h.To, h.From)
		}
	}
	return out
}

// Seed is a pattern position the data walk starts from, plus which endpoint of
// the triggering link supplies its vertex id.
type Seed struct {
	Pos             int
	SrcIsAnchorSide bool
}

// StepsFrom returns the moves available while standing at position pos: for
// each incident hop, the position it leads to and the adjacency read that gets
// there. ToLabel is the pattern's label for the far end ("" when unlabeled),
// which prunes an edge whose other end the pattern cannot bind.
func (ix HopIndex) StepsFrom(pos int) []PatternStep {
	var out []PatternStep
	// Two independent tests rather than a switch: a hop whose endpoints merged
	// into ONE position (`(x)-[:r]->(x)`) is standing at both its ends, and a
	// switch would read only the tail direction.
	for _, h := range ix.Hops {
		if h.From == pos {
			// Walking From→To follows the arrow as written, so the edge on the
			// node we are standing on is outbound for DirOut.
			out = append(out, PatternStep{ToPos: h.To, Rel: h.Rel, EdgeDir: edgeDirFor(h.Dir, true), ToLabel: ix.Labels[h.To]})
		}
		if h.To == pos {
			out = append(out, PatternStep{ToPos: h.From, Rel: h.Rel, EdgeDir: edgeDirFor(h.Dir, false), ToLabel: ix.Labels[h.From]})
		}
	}
	return out
}

// PatternStep is one move of the data walk, expressed in the adjacency store's
// own vocabulary: standing on a node bound at some position, read its edges
// named Rel whose Direction is EdgeDir, and step to the other end at ToPos.
type PatternStep struct {
	ToPos   int
	Rel     string
	EdgeDir string
	ToLabel string
}

// edgeDirFor maps a pattern arrow onto the adjacency Direction to read while
// standing on one of its ends. `(a)-[:r]->(b)` walked from a reads a's OUTBOUND
// r edges; walked from b it reads b's INBOUND ones. DirBoth reads either.
func edgeDirFor(d Direction, standingAtTail bool) string {
	switch d {
	case DirBoth:
		return "both"
	case DirOut:
		if standingAtTail {
			return "out"
		}
		return "in"
	default: // DirIn — the arrow runs To→From
		if standingAtTail {
			return "in"
		}
		return "out"
	}
}
