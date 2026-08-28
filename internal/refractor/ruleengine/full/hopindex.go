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
// Lockstep is a checkable claim, not a sentiment: all three visit MATCH's
// patterns and its WHERE, RETURN's items, and WITH's items AND its WHERE
// (relations.go's clause switch, labels.go's per-segment walk). A pattern can
// appear in any of them, and the two WITH positions are the easy ones to miss —
// a revocation filter staged behind a WITH is written there and nowhere else.
// Missing one is not a pessimisation here the way it is for the other two: they
// would report a set too SMALL and their callers reproject anyway, while this
// one would report Complete with the relation absent from Hops, and
// AnchorSideSeeds' empty answer on a complete index is read as "no anchor can
// change".
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

	// Min and Max are the hop's length range, already CLAMPED to what the
	// executor will actually walk: addPattern applies rel_traverse.go's own
	// clamp (an open or over-long Max becomes maxVarLengthHops, a negative Min
	// becomes 0) before recording the hop, so a consumer never has to re-derive
	// the bound. A fixed single hop — the overwhelming majority — carries
	// Min == Max == 1.
	//
	// Max >= Min >= 0 holds because addPattern refuses a lower bound above one
	// hop outright, on a seeding argument stated at that refusal: clamping Max
	// down without clamping Min would otherwise leave Min > Max, a range that
	// admits nothing while still costing a full frontier walk.
	//
	// The clamp is what makes a ranged hop safe to walk: the derivation is
	// complete with respect to what the EXECUTOR will evaluate, not with
	// respect to the graph. A path crossing more than maxVarLengthHops of this
	// relation produces no row, because the executor's own frontier stops
	// there, so a derivation that stops there misses nothing that could have
	// changed.
	Min int
	Max int

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
// distance from the anchor over the BINDING hops only, which is what tells a
// link event which of its two endpoints sits anchor-side. Counting a
// non-binding hop here would be a real defect rather than a pessimisation: a
// WHERE-NOT shortcut to the anchor can make the far endpoint look nearer, and
// the walk would then be seeded at the endpoint that can only reach the anchor
// by crossing the very edge a tombstone just removed.
//
// -1 is the INCOMPARABLE sentinel, and it carries two distinct meanings that a
// reader must not conflate:
//
//   - the position has NO binding path to the anchor at all — every hop
//     incident to it is a filter or a projection; or
//   - a binding path exists, but at least one hop on it is variable-length, so
//     the path's length is an INTERVAL and no single distance is comparable.
//
// Both take the same value because AnchorSideSeeds' `consider` treats them the
// same way and must: it drops the endpoint whose distance is the larger, so a
// distance that is not a single number cannot be allowed to lose that
// comparison. The sentinel's branch seeds BOTH endpoints, which only widens
// the derived set.
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

	// LabelExpand marks which positions carry the `*` taxonomy-expansion
	// sigil (NodePattern.LabelExpand), parallel to Labels — the AST-derived
	// half of the same generalization the four §5.1 sites already carry.
	// Nil (or an all-false slice) is exactly today's shape: every position's
	// Labels[i] is read as bare equality.
	LabelExpand []bool

	// Expanded is Labels[i]'s taxonomy-resolved concrete downward closure
	// for every position where LabelExpand[i] is true — nil until
	// WithLabelExpansion populates it. A LabelExpand position with no entry
	// here (because WithLabelExpansion was never called, or the resolver had
	// nothing for that label) admits NOTHING rather than falling back to
	// Labels[i]'s bare (and for an abstract label, meaningless) string.
	//
	// Admitting nothing is fail-closed for a matcher and fail-OPEN-downward
	// for a walk, so a consumer whose invariant is a superset must not run
	// against such an index at all: UnresolvedExpansionPosition is the test,
	// and pipeline.derivationIndex is the consumer that declines on it.
	Expanded []map[string]struct{}
}

// WithLabelExpansion returns a copy of ix carrying exp as the resolved
// concrete set for every LabelExpand position — mirroring
// CompiledRule.WithLabelExpansion's copy-on-write shape (§4.3): called once
// per activation/re-derivation (pipeline.useFullEngineBranches, which
// already has exp in hand from resolving ExpansionLabels), never per event,
// and never mutates ix in place.
//
// A position's own admits-nothing-on-no-entry behavior (see the Expanded
// field doc) is what keeps this fail-closed when exp does not cover a label
// this index needs — which cannot happen on the path
// useFullEngineBranches actually takes (it refuses activation before
// reaching this call whenever any needed label is unresolved), but must
// still be safe for a caller that builds a HopIndex directly, as the tests
// do.
func (ix HopIndex) WithLabelExpansion(exp map[string]map[string]struct{}) HopIndex {
	if len(ix.LabelExpand) == 0 {
		return ix
	}
	next := ix
	expanded := make([]map[string]struct{}, len(ix.Labels))
	any := false
	for i, isExpand := range ix.LabelExpand {
		if !isExpand {
			continue
		}
		if set, ok := exp[ix.Labels[i]]; ok {
			expanded[i] = set
			any = true
		}
	}
	if any {
		next.Expanded = expanded
	}
	return next
}

// UnresolvedExpansionPosition returns the first `*` position with no resolved
// concrete set, or -1 when every one of them has one. It is the question
// "is this index's taxonomy half answerable at all", asked once per rule
// state rather than per edge.
//
// The per-edge behaviour of an unresolved position (admitsType and
// PatternStep.ToExpanded alike) is to admit nothing, which PRUNES the walk —
// and pruning is the unsound direction for a derivation whose invariant is a
// superset: a pruned far end is an affected anchor the caller never
// reprojects, i.e. a revocation skipped. Fail-closed for a MATCHER and
// fail-closed for a DERIVATION are opposite motions, so the derivation must
// decline the whole index up front and let its caller fall back to the BFS,
// rather than walk one that silently narrows.
//
// A present-but-EMPTY concrete set is the same answer as a missing one, and is
// reported as unresolved for exactly that reason. An expansion resolving to
// nothing is a real state rather than an error — ruleinstall.go warns and
// degrades the lens to its broad consumer filter — but admitsType then admits
// no type at either expanded position, so the walk builds ZERO seeds and
// returns an empty derived set with ok == true. The caller reads that as "no
// anchor changes" and reprojects nobody. On a lens that mints cap.svc.<actor>
// the dropped event is a revocation, so the empty set must decline the index
// and take the BFS, not answer.
func (ix HopIndex) UnresolvedExpansionPosition() int {
	for i, isExpand := range ix.LabelExpand {
		if !isExpand {
			continue
		}
		if i >= len(ix.Expanded) || len(ix.Expanded[i]) == 0 {
			return i
		}
	}
	return -1
}

// admitsType reports whether position pos — labeled ix.Labels[pos], and
// taxonomy-expanded when ix.LabelExpand[pos] is true — admits vertexType.
// The single predicate PositionsBinding and AnchorSideSeeds' admits closure
// both reduce to, so the two can never disagree about the same position.
func (ix HopIndex) admitsType(pos int, vertexType string) bool {
	l := ix.Labels[pos]
	if l == "" {
		return true
	}
	if pos < len(ix.LabelExpand) && ix.LabelExpand[pos] {
		if pos >= len(ix.Expanded) {
			return false
		}
		set := ix.Expanded[pos]
		if set == nil {
			return false
		}
		_, hit := set[vertexType]
		return hit
	}
	return l == vertexType
}

// AnchorHopIndex builds the pattern graph for cr. See §16.2 of the design for
// the completeness predicate; each conjunct states its own reason below.
func (cr *CompiledRule) AnchorHopIndex() HopIndex {
	if cr == nil || cr.Query == nil {
		return HopIndex{Anchor: -1, Incomplete: "no compiled query"}
	}

	// Evaluated before the builder because the builder's own position merging
	// assumes what this answers: that two sightings of one variable name are
	// the same executor binding. Across a WITH boundary that holds only while
	// the WITH carried the name (withScopeReject).
	withReject := withScopeReject(cr.Query.Clauses)

	// root: -1 even though this builder never sets wantRoot (AnchorHopIndex
	// has no terminus of its own here) — the zero value 0 is a VALID position
	// index, so a future reader of b.root that forgets to check wantRoot
	// would silently treat position 0 as a real terminus instead of failing
	// loud.
	b := &hopIndexBuilder{byVar: make(map[string]int), anchor: -1, root: -1}
	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			for _, p := range cl.Patterns {
				b.addPattern(p, true)
			}
			b.addExpr(cl.Where)
		case *With:
			// A WITH's items and its WHERE are general expressions, and either
			// may hold a PatternExpr or a PatternComprehension — a revocation
			// filter (`WITH a, r WHERE NOT (r)-[:revokedBy]->(v:revocation)`) or
			// a projected walk (`WITH a, [(a)-[:holdsRole]->(x:role) | x.key]`).
			// They reach the graph as NON-BINDING patterns, the same posture a
			// MATCH's own WHERE takes: existsAsPredicate and
			// evalPatternComprehension call matchPath and discard the bindings,
			// so those hops can be walked but establish no position's binding.
			//
			// Omitting this arm is not a pessimisation. A relation named ONLY
			// here would contribute no hop while the index still reported
			// Complete, so AnchorSideSeeds would answer "no anchor can change"
			// for that relation's events — a strict SUBSET, which §4.7 licenses
			// the caller to act on as a skip.
			for _, it := range cl.Items {
				b.addExpr(it.Expr)
			}
			b.addExpr(cl.Where)
		case *Return:
			for _, it := range cl.Items {
				b.addExpr(it.Expr)
			}
		}
	}

	idx := HopIndex{Labels: b.labels, Hops: b.hops, Anchor: b.anchor, LabelExpand: b.expand}
	idx.Dist = idx.distances()

	// Ordered so the most fundamental refusal wins: without an anchor, nothing
	// downstream — grounding included — means anything.
	switch {
	case b.anchor < 0:
		idx.Incomplete = "no pattern position binds $actorKey"
	case b.multiAnchor:
		idx.Incomplete = "several pattern positions bind $actorKey"
	case b.expand[b.anchor]:
		// walkToAnchors constructs the anchor's own vertex KEY from a single
		// literal prefix ("vtx." + Labels[Anchor] + "."), because the anchor
		// is pinned by {key: $actorKey} to exactly one vertex whose Contract
		// #1 type the walk otherwise has no way to learn — a vertex reached
		// by the walk carries only a bare NanoID (adjacency.EdgeEntry does
		// not thread OtherType through seededNode), not its concrete type.
		// An expanded anchor's actual instances can be ANY of several
		// concrete types, so one literal prefix cannot be right for all of
		// them. Refusing here — rather than guessing a prefix or threading
		// per-vertex types through the walk — is the same fail-closed
		// posture every other shape this builder cannot resolve takes: fall
		// back to the shipped ActorEnumerator BFS, unchanged.
		idx.Incomplete = "the anchor pattern position carries the `*` taxonomy-expansion sigil — the derivation cannot construct a single anchor key prefix for an expanded set"
	case b.reject != "":
		idx.Incomplete = b.reject
	case withReject != "":
		// Reported ahead of b.ungrounded because the two describe the same
		// hazard — a position the executor seeds from its BUCKET — and this one
		// names the boundary that caused it. Where both fire, the grounding
		// conjunct is the SYMPTOM: `ground` merges a post-WITH sighting onto the
		// pre-WITH position and can therefore call a stranded head grounded, so
		// its silence is not evidence and this conjunct must be evaluated
		// independently of it.
		idx.Incomplete = withReject
	case b.ungrounded != "":
		// The conjunct whose omission is unsound. Every other one widens the
		// derived set; this one would shrink it to empty and license a skip
		// that drops a live revocation.
		idx.Incomplete = b.ungrounded
	}
	idx.Complete = idx.Incomplete == ""
	return idx
}

// ScanRootHopIndex builds the same pattern graph AnchorHopIndex does, over the
// same clauses, differing in exactly one place: which position is recorded as
// the terminus. Where AnchorHopIndex terminates at the `{key: $actorKey}`
// position, ScanRootHopIndex terminates at the ANCHOR PATTERN — the first
// MATCH clause's first node (anchorPattern, anchor_delete.go) — the position a
// seeded plain evaluation pins to one vertex (executor's key-property point
// read / pointCandidate). It is the plain arm's own scan root: a plain lens
// has no `$actorKey` position at all (AnchorHopIndex.Anchor is always -1 for
// one), so this is a SECOND index sharing HopIndex's struct and builder
// machinery, not a wider Anchor field. Widening Anchor to mean "terminus"
// would break DeclaresActorAnchor (Anchor >= 0 IS the lens's declared
// $actorKey binding, read by pipeline.ConsumerFilter's install-completeness
// guard) — every plain lens would report as "declared actor-aware with no
// enumerator installed" and take the broad filter forever.
//
// Every downstream consumer — Dist, AnchorSideSeeds, StepsFrom,
// PositionsBinding, admitsType, walkToAnchors — reads HopIndex.Anchor as "the
// terminus" and is reused verbatim; only which position ends up there
// differs, so none of them need a second, independently fallible twin.
//
// Complete/Incomplete describe THIS index alone — nothing reads them across
// AnchorHopIndex and ScanRootHopIndex.
//
// Cost: one extra AST walk per rule publication (useFullEngineBranches),
// never per event — the same budget AnchorHopIndex already documents for
// itself.
func (cr *CompiledRule) ScanRootHopIndex() HopIndex {
	if cr == nil || cr.Query == nil {
		return HopIndex{Anchor: -1, Incomplete: "no compiled query"}
	}

	// Evaluated up front for the same reason AnchorHopIndex evaluates it up
	// front: the builder's position-merging assumes two sightings of one
	// variable name are the same executor binding, which a WITH boundary can
	// break.
	withReject := withScopeReject(cr.Query.Clauses)

	// rootHasKey is read directly off the AST rather than through the
	// builder: a node pinned by its own `key` property is already a point
	// read (the executor's key-property fast path), so there is no scan for
	// this index to remove — the check does not care WHICH expression pins
	// it (unlike nodePinsActor, which insists on exactly `$actorKey`).
	rootNode, foundRoot := anchorPattern(cr.Query)
	_, rootHasKey := rootNode.Properties["key"]

	b := &hopIndexBuilder{byVar: make(map[string]int), anchor: -1, root: -1, wantRoot: true}
	for _, c := range cr.Query.Clauses {
		switch cl := c.(type) {
		case *Match:
			for _, p := range cl.Patterns {
				b.addPattern(p, true)
			}
			b.addExpr(cl.Where)
		case *With:
			for _, it := range cl.Items {
				b.addExpr(it.Expr)
			}
			b.addExpr(cl.Where)
		case *Return:
			for _, it := range cl.Items {
				b.addExpr(it.Expr)
			}
		}
	}

	idx := HopIndex{Labels: b.labels, Hops: b.hops, Anchor: b.root, LabelExpand: b.expand}
	idx.Dist = idx.distances()

	// Ordered so the most fundamental refusal wins, mirroring
	// AnchorHopIndex's own ordering rationale: without a labeled terminus,
	// nothing downstream — grounding included — means anything.
	switch {
	case !foundRoot || b.root < 0 || idx.Labels[b.root] == "":
		// Covers both "no MATCH clause at all" (foundRoot false) and "the
		// anchor pattern's node carries no label": either way no single
		// vtx.<label>. prefix can be minted (walkToAnchors), and an
		// unlabeled seed is refused anyway (executor's seedAnchorBinds).
		idx.Incomplete = "the anchor pattern position carries no label"
	case rootHasKey:
		idx.Incomplete = "the anchor pattern is pinned by its own key"
	case b.expand[b.root]:
		// Same shape AnchorHopIndex refuses at its own anchor for the same
		// reason: an expanded terminus's instances may be any of several
		// concrete types, and walkToAnchors needs one literal key prefix.
		idx.Incomplete = "the anchor pattern position carries the `*` taxonomy-expansion sigil — the derivation cannot construct a single anchor key prefix for an expanded set"
	case b.reject != "":
		idx.Incomplete = b.reject
	case withReject != "":
		// withScopeReject is the SAME general variable-scope walk
		// AnchorHopIndex uses, unmodified, and its verdict transfers as-is:
		// a drop-then-re-reference is a bucket-scan rebind regardless of
		// which position is the terminus. Its own doc licenses dropping the
		// $actorKey PARAMETER because the anchor is not a row column there;
		// that licence does not apply here — a root terminus routinely IS a
		// row column (`RETURN pr.key`) — but nothing has to special-case it:
		// if the root's own variable is dropped and never referenced again,
		// withScopeReject already reads that as harmless, and if it IS
		// referenced again, the same drop-then-re-reference refusal already
		// fires. Only this justification is root-specific; the mechanism and
		// its returned reason are shared whole.
		idx.Incomplete = withReject
	case b.ungrounded != "":
		// Reachable here in a way it never is for AnchorHopIndex on a plain
		// lens: a plain query has no $actorKey position, so AnchorHopIndex's
		// own b.anchor < 0 case fires first and swallows this conjunct for
		// every one of them. With a root terminus every plain lens HAS a
		// terminus, so a second MATCH headed by a variable the first one
		// never bound is now the real, load-bearing refusal — it re-scans a
		// bucket and really does affect every derived anchor.
		idx.Incomplete = b.ungrounded
	}
	// b.multiAnchor has no counterpart here and is never checked: it tracks
	// how many positions pin `{key: $actorKey}`, which this index does not
	// use at all — the root is one position by construction (the first MATCH
	// clause's first node names exactly one AST position, however many times
	// $actorKey is pinned elsewhere in the same query).
	idx.Complete = idx.Incomplete == ""
	return idx
}

// DeclaresActorAnchor reports whether this compiled query pins a pattern
// position with `{key: $actorKey}` — the shape every actor-anchored lens
// (actorAggregate and Personal alike) opens with. It is the lens's DECLARED
// projection kind, read off the cypher the package author wrote, so answering it
// needs no installed component, no setter and no state of its own.
//
// Cheap to REASON about, not cheap to CALL: it builds the whole pattern graph,
// distances() included, every time. What makes that irrelevant is where it is
// called — once per rule publication (pipeline.useFullEngineBranches), with the
// answer published onto the rule snapshot and read from there afterwards. Do not
// put it on a per-event path without caching it.
//
// It is deliberately independent of Complete. noteAnchor fires while
// addPattern walks a pattern's NODES, ahead of every hop-level and
// grounding-level refusal, and the completeness switch only writes Incomplete —
// so a query this index cannot walk (an untyped or variable-length hop, a
// re-reference across a WITH, an ungrounded pattern head) still reports its
// declaration truthfully. Which is exactly the question the caller asks:
// pipeline.ConsumerFilter needs to know whether the lens was WRITTEN to be
// actor-anchored, not whether the affected-anchor derivation can run on it.
// Reading Anchor only when Complete would report the shapes that refuse for
// some other reason — objectAttachments, capabilityServiceAccess and half the
// edge-manifest corpus among them — as plain.
//
// The one shape it under-reports is a `{key: $actorKey}` node buried inside an
// Expr addExpr does not model: that arm default-denies WITHOUT descending, so
// the position is never created and no anchor is recorded. It is the same blind
// spot every other consumer of this index already has, and no shipped lens is in
// that shape — but the consequence lands on the CALLER, so it is spelled out
// where the soundness claim is made (pipeline.ConsumerFilter's doc) rather than
// only here.
func (cr *CompiledRule) DeclaresActorAnchor() bool {
	if cr == nil || cr.Query == nil {
		return false
	}
	return cr.AnchorHopIndex().Anchor >= 0
}

func describeLabel(l string) string {
	if l == "" {
		return "unlabeled"
	}
	return l
}

// distances returns each position's undirected distance from the anchor over
// the BINDING hops, or the incomparable sentinel -1 (HopIndex.Dist's field doc
// names its two meanings). Undirected is right: the question is whether the
// executor's binding of that position is established by walking from the
// anchor's, and a hop binds in whichever direction the walk crosses it.
//
// It runs in two passes, because a ranged hop has no single length:
//
//  1. exact distances over the FIXED binding hops only (Min == Max == 1);
//  2. a two-state BFS over (position, has-crossed-a-ranged-hop) that poisons
//     every non-anchor position reachable from the anchor over binding hops
//     using at least one ranged hop.
//
// The poison is applied to every such position, not only to positions
// reachable ONLY across a ranged hop, and the asymmetry is deliberate. A
// position holding both a fixed path of length L and a ranged path whose true
// length may be shorter would keep L, which OVER-STATES its distance — and
// AnchorSideSeeds' `consider` drops the endpoint whose distance is the larger,
// so an over-stated distance drops a seed, which is the under-approximating
// direction this whole unit refuses. Over-poisoning only makes `consider` seed
// both endpoints, which merely widens the derived set.
//
// The anchor itself keeps 0: it is pinned by `{key: $actorKey}` to exactly one
// vertex, so it is never the endpoint a distance comparison has to place.
func (ix HopIndex) distances() []int {
	d := make([]int, len(ix.Labels))
	for i := range d {
		d[i] = -1
	}
	if ix.Anchor < 0 || ix.Anchor >= len(d) {
		return d
	}

	// Pass 1 — exact distances over fixed binding hops.
	d[ix.Anchor] = 0
	queue := []int{ix.Anchor}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, h := range ix.Hops {
			if !h.Binding || h.Min != 1 || h.Max != 1 {
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

	// Pass 2 — the poison set, as a BFS over (position, usedRanged) so the
	// "an interval was crossed" bit is carried by the search rather than
	// re-derived from the shape of the graph.
	type rangedState struct {
		pos        int
		usedRanged bool
	}
	seen := map[rangedState]struct{}{{pos: ix.Anchor}: {}}
	states := []rangedState{{pos: ix.Anchor}}
	for len(states) > 0 {
		cur := states[0]
		states = states[1:]
		for _, h := range ix.Hops {
			if !h.Binding {
				continue
			}
			var next int
			switch {
			case h.From == cur.pos:
				next = h.To
			case h.To == cur.pos:
				next = h.From
			default:
				continue
			}
			ns := rangedState{pos: next, usedRanged: cur.usedRanged || h.Min != 1 || h.Max != 1}
			if _, dup := seen[ns]; dup {
				continue
			}
			seen[ns] = struct{}{}
			states = append(states, ns)
			if ns.usedRanged && next != ix.Anchor {
				d[next] = -1
			}
		}
	}
	return d
}

type hopIndexBuilder struct {
	labels      []string
	expand      []bool // parallel to labels; n.LabelExpand at each position's first occurrence
	hops        []PatternHop
	byVar       map[string]int
	anchor      int
	multiAnchor bool
	reject      string

	// wantRoot/root are ScanRootHopIndex's terminus, alongside anchor/
	// multiAnchor/noteAnchor which remain AnchorHopIndex's. Both sets of
	// fields are populated by the same addPattern walk regardless of which
	// index is being built (nodePinsActor is checked unconditionally), so a
	// query with both a `{key: $actorKey}` position and an anchor pattern
	// updates both — but only one of the two termini is what ground() and the
	// returned HopIndex.Anchor read for a given build, selected by wantRoot.
	// root is -1 until the anchor pattern's own node is reached.
	wantRoot bool
	root     int

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
		b.expand = append(b.expand, n.LabelExpand)
		return len(b.labels) - 1
	}
	i, seen := b.byVar[n.Variable]
	if !seen {
		b.labels = append(b.labels, n.Label)
		b.expand = append(b.expand, n.LabelExpand)
		i = len(b.labels) - 1
		b.byVar[n.Variable] = i
	}
	return i
}

func (b *hopIndexBuilder) addPattern(p PathPattern, binding bool) {
	// ScanRootHopIndex's terminus must be recorded from THIS pattern's
	// Nodes[0] before anything below walks a property-map expression: a
	// PatternExpr in a property map (checked right below, for every node of
	// this pattern) reaches ground() through addExpr, and ground() reads
	// b.terminus() — which must already be b.root by the time that happens,
	// or a nested pattern in the anchor node's own property map would be
	// judged ungrounded for the wrong reason (b.root still -1).
	//
	// rootHere is true on exactly the FIRST binding addPattern call this
	// builder ever makes — the first MATCH clause's first pattern, the same
	// one anchorPattern (anchor_delete.go) names — because the outer clause
	// loop in ScanRootHopIndex visits clauses in order and this is a
	// wantRoot-only branch. positions[0] below reuses b.root rather than
	// calling position() a second time: position() merges a second sighting
	// of a NAMED node onto the first, but mints a fresh class for an unnamed
	// one, so a second call here would silently orphan the terminus for an
	// unnamed anchor pattern.
	rootHere := b.wantRoot && b.root < 0 && binding && len(p.Nodes) > 0
	if rootHere {
		b.root = b.position(p.Nodes[0])
	}

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
		if rootHere && i == 0 {
			// Reuse b.root rather than calling position() again: for an
			// UNNAMED anchor node position() would mint a second, fresh
			// class instead of returning the one rootHere already recorded
			// above, silently orphaning the terminus. Do not "simplify" this
			// to a plain position(n) call.
			positions[0] = b.root
		} else {
			positions[i] = b.position(n)
		}
		if nodePinsActor(n) {
			b.noteAnchor(positions[i])
		}
	}
	b.ground(positions, binding)

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
		case r.MinHops > 1:
			// A ranged hop whose LOWER bound exceeds 1 is refused, and the
			// reason is in the SEEDING rather than in the walk.
			//
			// A changed link on a fixed hop binds its two pattern positions
			// exactly, so AnchorSideSeeds' two endpoint seeds are exact. On a
			// ranged hop the changed link is an INTERMEDIATE edge of the
			// expansion: the real bindings are `a →^i u -r-> v →^j b` with
			// i+j+1 in [Min,Max], so the nodes bound at the From position are
			// every node reaching u within Max-1 steps, not u. Those ancestors
			// are recovered only by the walk bouncing back across the hop from
			// the To seed, which covers From-offsets [1-Max, 1-Min]; together
			// with the direct seed at offset 0 that covers the required
			// [1-Max, 0] only while Min <= 2. At a higher lower bound the
			// offset -1 binding is reachable only through a second bounce,
			// which a node near the edge of its component does not have the
			// graph room for — so the derivation returns ok == true having
			// dropped an anchor, which on the auth plane is a revocation that
			// never fires.
			//
			// Refusing costs nothing the corpus uses (every shipped ranged hop
			// is `*0..`, `*1..` or `*0..7`) and leaves those shapes on the BFS,
			// exactly as before this arm existed. Widening the seed set to the
			// [1..Max] neighbourhood of both endpoints is the alternative, and
			// it is a larger change with a much larger derived set.
			b.rejectOnce("pattern carries a variable-length relationship whose lower bound exceeds one hop")
		case r.MinHops != 1 || r.MaxHops != 1:
			// A variable-length hop is recorded as a RANGED hop, carrying the
			// same clamped bounds traverseRel walks it under. The number of
			// adjacency reads between the two bound positions is not a single
			// number, but it is an interval, and an interval is walkable: the
			// consumer expands a bounded frontier instead of taking one edge.
			//
			// Clamping here — rather than leaving the raw AST range for each
			// consumer to bound — is the soundness invariant, not a
			// convenience. The derivation is complete with respect to what the
			// executor will evaluate, not with respect to the graph: an anchor
			// whose path crosses more than maxVarLengthHops of this relation
			// cannot produce a row, because rel_traverse.go's frontier stops
			// at exactly that bound. Sharing the constant is what makes
			// "stops there too" equal "misses nothing".
			minHops, maxHops := r.MinHops, r.MaxHops
			if maxHops < 0 || maxHops > maxVarLengthHops {
				maxHops = maxVarLengthHops
			}
			if minHops < 0 {
				minHops = 0
			}
			b.hops = append(b.hops, PatternHop{
				Rel: r.Type, From: positions[i], To: positions[i+1], Dir: r.Direction,
				Min: minHops, Max: maxHops, Binding: binding,
			})
		default:
			b.hops = append(b.hops, PatternHop{
				Rel: r.Type, From: positions[i], To: positions[i+1], Dir: r.Direction,
				Min: 1, Max: 1, Binding: binding,
			})
		}
	}
}

// ground marks a pattern's positions as anchor-grounded when its HEAD already
// is. executor.matchPath seeds only Nodes[0] and reaches every later node by
// traversal, so a pattern headed by an already-bound variable extends the
// grounded set, and one headed by anything else starts a fresh bucket scan.
// Clause order is load-bearing and matches the executor's.
//
// Two things it must NOT do, both of which return a set smaller than the truth
// rather than larger — the one direction this whole unit exists to refuse:
//
//   - Treat a pattern reached before the anchor as neither grounded nor
//     refused. Nothing upstream has pinned anything yet, so matchPath binds its
//     head from that head's own bucket; every anchor's row then depends on the
//     whole bucket while the walk, having no hop from those positions to the
//     anchor, derives the empty set and licenses a skip.
//   - Let a NON-BINDING pattern extend the grounded set. existsAsPredicate and
//     evalPatternComprehension both call matchPath and DISCARD its bindings, so
//     a variable a WHERE pattern or a RETURN comprehension introduces is still
//     unbound in the outer row. A later MATCH headed by it is therefore a fresh
//     bucket scan, and counting it as grounded would pass exactly the shape the
//     conjunct exists to catch. Such a pattern must still REQUIRE a grounded
//     head — a WHERE pattern headed by an unbound variable scans a bucket per
//     row just as a MATCH does.
func (b *hopIndexBuilder) ground(positions []int, binding bool) {
	if len(positions) == 0 {
		return
	}
	term := b.terminus()
	if term < 0 {
		b.markUngrounded(positions[0])
		return
	}
	if b.grounded == nil {
		b.grounded = map[int]bool{term: true}
	}
	if !b.grounded[positions[0]] {
		b.markUngrounded(positions[0])
		return
	}
	if !binding {
		return
	}
	for _, pos := range positions {
		b.grounded[pos] = true
	}
}

// terminus returns the position ground() must reach a pattern's head from —
// b.root for ScanRootHopIndex, b.anchor for AnchorHopIndex. The two builders
// share ground() (and every other walk below it) because "grounded relative
// to the terminus" is the same question for either one; only which position
// is the terminus differs.
func (b *hopIndexBuilder) terminus() int {
	if b.wantRoot {
		return b.root
	}
	return b.anchor
}

func (b *hopIndexBuilder) markUngrounded(head int) {
	if b.ungrounded != "" {
		return
	}
	b.ungrounded = fmt.Sprintf("pattern headed by position %d (%s) is not reached from the anchor — it binds by bucket scan, so it affects every anchor", head, describeLabel(b.labels[head]))
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
	case *Literal, *ParameterRef, *VariableRef:
		// Terminals: no sub-expression and no pattern inside one, so there is
		// nothing here for the graph. Listed rather than defaulted so the arm
		// below can default-DENY.
	default:
		// An Expr shape this walk does not model may carry a pattern position
		// it never indexes, and PositionsBinding would then return a set
		// SHORTER than the truth. That is the direction every consumer of this
		// index must never move in: pipeline.ActorTypeBindsAnchorOnly reads a
		// short set as "the actor type binds only at the anchor" and licenses
		// the one-key answer, which on the auth plane is a live grant. Refusing
		// the index costs a BFS; guessing costs a grant. Mirrors withscope.go's
		// varScan.expr, which default-denies the same AST for the same reason.
		b.rejectOnce(fmt.Sprintf("expression node %T is not modelled by the hop index", e))
	}
}

// PositionsBinding returns every position whose label admits vertexType — the
// node-seeded entry point (§4.7's 3b). An UNLABELED position matches any type,
// which is what lets the derivation reach lenses whose label set is not
// exhaustive and which Increments 1–2 therefore cannot narrow. A `*` position
// admits vertexType by set membership against its taxonomy-resolved downward
// closure (admitsType) rather than string equality.
func (ix HopIndex) PositionsBinding(vertexType string) []int {
	var out []int
	for i := range ix.Labels {
		if ix.admitsType(i, vertexType) {
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
	admits := ix.admitsType
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
// which prunes an edge whose other end the pattern cannot bind. ToLabelExpand
// + ToExpanded carry the same generalization when the far end is a `*`
// position: the prune becomes set membership instead of string equality.
//
// Min/Max carry the hop's clamped length range through UNCHANGED in both
// readings. A bounded reachability relation is symmetric — "the nodes reaching
// Y in at most k forward steps" is exactly "the nodes reachable from Y in at
// most k reverse steps" — and edgeDirFor has already flipped the direction the
// walk reads, so the reverse reading needs no adjustment to the range itself.
func (ix HopIndex) StepsFrom(pos int) []PatternStep {
	var out []PatternStep
	step := func(h PatternHop, toPos int, standingAtTail bool) PatternStep {
		s := PatternStep{
			ToPos: toPos, Rel: h.Rel, EdgeDir: edgeDirFor(h.Dir, standingAtTail),
			ToLabel: ix.Labels[toPos], Min: h.Min, Max: h.Max,
		}
		if toPos < len(ix.LabelExpand) && ix.LabelExpand[toPos] {
			s.ToLabelExpand = true
			if ix.Expanded != nil {
				s.ToExpanded = ix.Expanded[toPos]
			}
		}
		return s
	}
	// Two independent tests rather than a switch: a hop whose endpoints merged
	// into ONE position (`(x)-[:r]->(x)`) is standing at both its ends, and a
	// switch would read only the tail direction.
	for _, h := range ix.Hops {
		if h.From == pos {
			// Walking From→To follows the arrow as written, so the edge on the
			// node we are standing on is outbound for DirOut.
			out = append(out, step(h, h.To, true))
		}
		if h.To == pos {
			out = append(out, step(h, h.From, false))
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

	// Min and Max are the hop's clamped length range (PatternHop.Min/Max). A
	// fixed hop is Min == Max == 1 and the move is a single edge; anything
	// else is a bounded frontier expansion over Rel/EdgeDir for hops 1..Max,
	// admitting the far end at every hop >= Min, plus the standing node itself
	// when Min == 0.
	Min int
	Max int

	// ToLabelExpand + ToExpanded generalize ToLabel the same way NodePattern.
	// LabelExpand + CompiledRule.LabelExpansion generalize a query pattern's
	// bare label (§5.1): when ToLabelExpand is true, a far-end prune must
	// test membership in ToExpanded rather than string-compare against
	// ToLabel. ToExpanded is nil when the expansion was never resolved for
	// this position — the caller must fail closed on that, not fall back to
	// ToLabel.
	ToLabelExpand bool
	ToExpanded    map[string]struct{}
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
