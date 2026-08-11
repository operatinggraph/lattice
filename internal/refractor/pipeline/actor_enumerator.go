// Cross-vertex fan-out lives in the pipeline, not the engine. When a CDC
// event arrives on a non-actor vertex (e.g. a role, permission, service,
// location, or task vertex), the pipeline enumerates the set of actor
// (identity) vertices reachable from the mutated vertex via the topology
// relations the Capability Lens cares about, then re-executes the cypher
// rule with `$actorKey` bound to each affected actor.
//
// The enumeration is depth-bounded (matching the executor's
// variable-length traversal cap, default 10) and actor-cap-bounded
// (default 10_000) so a runaway traversal can't stall the pipeline. Both caps
// are configurable per Pipeline. Reaching the actor cap refuses the event
// rather than shortening the answer — see ActorEnumerator's Caps section.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/operatinggraph/lattice/internal/refractor/adjacency"
	"github.com/operatinggraph/lattice/internal/refractor/failure"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// ErrActorSetTooWide is what an exceeded actor-set cap returns instead of a
// shortened answer. It is wrapped Structural, so the event stays pending and the
// lens pauses for an operator rather than acking a reprojection that silently
// skipped anchors — see the maxActors bullet on ActorEnumerator for why a
// subset is the one answer this arm must never give.
var ErrActorSetTooWide = fmt.Errorf("pipeline: actor enumerator: actor-set cap reached")

// ActorEnumerator finds the set of actor (identity) vertex keys
// reachable from eventVertexKey via undirected adjacency BFS. The
// returned slice contains FULL Contract #1 vertex keys
// (e.g. "vtx.identity.<NanoID>"). An eventVertexKey that already carries
// the target type is one of the answers rather than the whole answer: it
// is unioned into the walk's result, never returned in place of it. A
// pattern may bind the actor type at a NON-anchor position —
// `capabilityEphemeral`'s `report`, reached by
// (identity)<-[:reportsTo]-(report:identity) — and an anchor that binds
// the changed vertex there has to be reprojected as well. Answering with
// the changed vertex alone is the narrower of the two answers, and on the
// auth plane a narrow answer is a stale grant or a missed retraction.
//
// A pipeline whose compiled pattern proves the actor type binds at exactly
// one position, the anchor, may still take the one-key answer; that proof
// lives where the pattern and the healer do (Pipeline.oneKeyAnswerSound), not
// here, because this type sees no pattern.
//
// The BFS stops at the first actor it reaches on any path — once an
// actor is found, capability is computed from that actor's own outbound
// topology, and continuing past it unrestricted would double-count
// actors via shared neighbours (e.g. two actors in the same location).
// The one addition to that rule: every found actor also gets a single,
// non-recursive, directional hop along actorHierarchyRelation (reportsTo)
// to the actor it reports to, since `capabilityEphemeral`'s reportsTo
// 2-hop branch means that actor's own projection depends on this one's
// direct assignments too. This mirrors the cypher exactly — it is one
// fixed hop, not a chain — so a report's report (if any) is never pulled
// in on that report's account.
//
// Caps:
//   - maxDepth: BFS depth bound. Per Decision #3 the default mirrors the
//     executor's variable-length traversal cap (10).
//   - maxActors: bound on the actor set. Exceeding it is a REFUSAL, not a
//     truncation: Enumerate returns ErrActorSetTooWide and the event fails
//     loudly. A truncated set is a silent subset of the affected anchors, and
//     the caller cannot tell it apart from a complete one — which on the auth
//     plane is a retraction dropped with nothing said. walkToAnchors applies the
//     same rule to the identical hazard on its own read cap and states the
//     reason (anchor_derivation.go's DefaultDerivationReadCap); the two arms
//     must not disagree about what an exceeded bound means. A cap's SIZE and
//     its failure MODE are separate decisions: this states only what exceeding
//     the bound means, whatever number it is set to (§18.6 owns the sizing).
//
// adjKV and coreKV are the live KV handles, passed through to every
// adjacency.Neighbors call this type makes. coreKV is required whenever a
// lookup reaches an overflow-marked node: Neighbors' Core KV fallback read
// enumerates that node's link keyspace directly, and a nil coreKV there is
// an error, never a silently short edge list (see adjacency.Neighbors).
type ActorEnumerator struct {
	adjKV     *substrate.KV
	coreKV    *substrate.KV
	actorType string
	maxDepth  int
	maxActors int
}

// DefaultActorMaxDepth mirrors the executor's variable-length traversal
// cap (Decision #3 / scope guard).
const DefaultActorMaxDepth = 10

// DefaultActorMaxSet is the default cap on the affected-actor set per
// Decision #3 / scope guard. Reaching it returns ErrActorSetTooWide.
const DefaultActorMaxSet = 10_000

// actorHierarchyRelation is the link name a found actor gets one extra
// outbound hop along, per the ActorEnumerator doc comment above.
const actorHierarchyRelation = "reportsTo"

// NewActorEnumerator constructs an enumerator with the given KV handles
// and target actor type (e.g. "identity").
func NewActorEnumerator(adjKV, coreKV *substrate.KV, actorType string) *ActorEnumerator {
	return &ActorEnumerator{
		adjKV:     adjKV,
		coreKV:    coreKV,
		actorType: actorType,
		maxDepth:  DefaultActorMaxDepth,
		maxActors: DefaultActorMaxSet,
	}
}

// WithCaps overrides the default depth and actor-set caps. Returns the
// receiver to allow fluent configuration at wire-up time.
func (e *ActorEnumerator) WithCaps(maxDepth, maxActors int) *ActorEnumerator {
	if maxDepth > 0 {
		e.maxDepth = maxDepth
	}
	if maxActors > 0 {
		e.maxActors = maxActors
	}
	return e
}

// Enumerate returns the set of actor vertex keys reachable from
// eventVertexKey by undirected adjacency BFS. The traversal is bounded
// by maxDepth and maxActors. eventVertexType is the type segment of
// the event vertex; when it equals e.actorType the event vertex is
// recorded as an affected actor in its own right AND the walk runs, so
// the answer covers the anchors that bind it at a non-anchor position.
// ctx is propagated to adjacency KV reads.
func (e *ActorEnumerator) Enumerate(ctx context.Context, eventVertexKey, eventVertexType string) ([]string, error) {
	// Recover the event vertex's NanoID for the BFS frontier; adjacency
	// is keyed by NanoID per `subjects.AdjKey`.
	_, eventID, ok := substrate.ParseVertexKey(eventVertexKey)
	if !ok {
		return nil, fmt.Errorf("pipeline: actor enumerator: not a Contract #1 vertex key: %q", eventVertexKey)
	}

	visited := map[string]struct{}{eventID: {}}
	actors := map[string]struct{}{}
	type frontierEntry struct {
		nodeID   string
		nodeType string
		depth    int
	}
	frontier := []frontierEntry{{nodeID: eventID, nodeType: eventVertexType, depth: 0}}
	overWide := false

	// addActor records id (already known to be e.actorType) in actors.
	// Returns false when the cap was reached, which aborts the enumeration
	// rather than shortening its answer.
	addActor := func(id string) bool {
		actorKey := substrate.VertexPrefix + "." + e.actorType + "." + id
		if _, exists := actors[actorKey]; exists {
			return true
		}
		if len(actors) >= e.maxActors {
			overWide = true
			return false
		}
		actors[actorKey] = struct{}{}
		return true
	}

	// An event on a vertex of the actor type affects that actor for certain,
	// whatever the walk goes on to find. Recorded before the walk, so a cap the
	// one-key answer never consulted cannot be reached on this one first.
	if eventVertexType == e.actorType {
		addActor(eventID)
	}

	// addHierarchyManager looks up reportID's own adjacency and adds every
	// actor it reports to (an outbound actorHierarchyRelation edge) — the
	// single, non-recursive hop the type doc comment describes. It does not
	// itself call addHierarchyManager on what it finds.
	addHierarchyManager := func(reportID string) error {
		edges, _, err := adjacency.Neighbors(ctx, e.adjKV, e.coreKV, reportID)
		if err != nil {
			return fmt.Errorf("pipeline: actor enumerator: hierarchy neighbours of %q: %w", reportID, err)
		}
		for _, edge := range edges {
			if edge.Name != actorHierarchyRelation || edge.Direction != "outbound" || edge.OtherType != e.actorType {
				continue
			}
			addActor(edge.OtherNodeID)
		}
		return nil
	}

	for len(frontier) > 0 {
		cur := frontier[0]
		frontier = frontier[1:]
		if cur.depth >= e.maxDepth {
			continue
		}
		edges, _, err := adjacency.Neighbors(ctx, e.adjKV, e.coreKV, cur.nodeID)
		if err != nil {
			return nil, fmt.Errorf("pipeline: actor enumerator: neighbours of %q: %w", cur.nodeID, err)
		}
		for _, edge := range edges {
			other := edge.OtherNodeID
			otherType := edge.OtherType
			if otherType == "" {
				// Legacy edge event with no OtherType — best-effort lookup
				// via Core KV. We don't FAIL on missing/typeless edges;
				// such edges simply don't contribute to the actor set.
				continue
			}
			if _, seen := visited[other]; seen {
				continue
			}
			visited[other] = struct{}{}

			if otherType == e.actorType {
				if addActor(other) {
					if err := addHierarchyManager(other); err != nil {
						return nil, err
					}
				}
				// No further traversal from an actor beyond the single
				// hierarchy hop just above — see the type doc comment.
				continue
			}

			frontier = append(frontier, frontierEntry{nodeID: other, nodeType: otherType, depth: cur.depth + 1})
		}
	}

	if overWide {
		slog.Error("pipeline: actor enumerator: actor-set cap reached; refusing to answer with a subset",
			"eventVertex", eventVertexKey, "cap", e.maxActors)
		return nil, failure.Structural(fmt.Errorf(
			"%w: %d actors reached from %q at the cap of %d — raise the enumerator's cap (WithCaps / DefaultActorMaxSet) or narrow the lens",
			ErrActorSetTooWide, len(actors), eventVertexKey, e.maxActors))
	}

	out := make([]string, 0, len(actors))
	for k := range actors {
		out = append(out, k)
	}
	return out, nil
}

// enumerateAnchors is the pipeline's single entry to the ActorEnumerator BFS —
// the three fan-out arms (vertex, link endpoint, aspect parent) all reach the
// enumerator through here, so the one-key answer is licensed in one place or in
// none.
//
// An event on a vertex of the actor type is answered with that vertex alone
// only where oneKeyAnswerSound proves no other anchor can bind it, or where an
// operator has turned the widening off. Everywhere else the enumerator walks —
// the wider answer §4.7 requires, since an anchor the one-key shortcut omits is
// a retraction that never reaches its holder.
func (p *Pipeline) enumerateAnchors(ctx context.Context, rs ruleState, vertexKey, vertexType string) ([]string, error) {
	if vertexType == p.actorEnumerator.actorType &&
		(!p.peerAnchorsEnabled() || p.oneKeyAnswerSound(rs)) {
		return []string{vertexKey}, nil
	}
	return p.actorEnumerator.Enumerate(ctx, vertexKey, vertexType)
}

// oneKeyAnswerSound reports whether an event on a vertex of the actor type may
// be answered with that vertex alone.
//
// The proof is the pattern's: an anchor's row is a function of the subgraph its
// compiled pattern binds from that anchor, and the anchor position is pinned to
// exactly one vertex by `{key: $actorKey}`. So when the actor type binds at
// exactly one pattern position and that position IS the anchor, some other
// anchor's evaluation has nowhere to bind the changed vertex, and its row cannot
// move.
//
// Every input to that proof has to be trustworthy, and each conjunct fails
// toward the walk:
//
//   - an incomplete index stopped indexing at the source it could not read, so
//     its position set is a floor rather than the truth;
//   - a `*` position with no resolved expansion admits NOTHING, so it would drop
//     out of the count and make a multi-position pattern read as
//     single-position — the one direction this predicate must never move in.
//
// The pattern argument is not the whole predicate, because answering with one
// key is a NARROWING: it stops reprojecting peers that the walk used to reach
// incidentally, and that incidental reprojection is today the only thing that
// converges a row left stale by something the pattern cannot see. So the second
// conjunct is §4.2's standing healer — a lens may give up the accident only if a
// convergence sweep will still repair the row. p.sweeper is the exact test:
// SetSweepPlan has one non-test caller (projection/driver.go:435), inside the
// else of an enrolment gate whose refusal is warn-only (:426), so a nil sweeper
// names both a personal lens — which "simply never gets a plan" (:417-421) — and
// an actor-aggregate lens whose enrolment was refused. Both need the accident.
//
// It deliberately does NOT require patternClosedOutput, which the two other
// consumers of that flag (derivationIndexForAct, actorAwareNarrowingLabels) both
// do. That flag governs whether an anchor's row can move with NO pattern edge
// moving; the CROSS-ACTOR question here is narrower, and pattern closure is not
// what decides it. Every out-of-pattern input the tree carries is keyed on the
// EVALUATING actor: a personal lens's two readers both sit in
// personalEnvelopeFn, where capabilityread.IsReadable reads
// `cap-read.<actorType>.<actorID>.<anchorID>` (and its `cap-read.*.…` domain
// form) for the actorKey THIS evaluation is bound to, against an anchor the
// pattern itself produced, and personalinterest.IsRelevant reads `<actorID>.>`
// for that same actor. Neither reads a different identity's vertex or aspects,
// so neither makes anchor Y's row a function of actor X's. What those inputs DO
// create is a row that goes stale with no Core KV event to notice — a
// `cap-read.<Y>` grant landing is a Capability-KV write, not a CDC event — and
// that is the healer's problem, which is why the sweeper conjunct above and not
// this one is what holds the personal corpus on the walk.
//
// It carries the same label/key-type caveat the rest of this unit does
// (auth-plane-projection-latency-design.md §17.6): PositionsBinding matches a
// pattern label against the vertex KEY TYPE, while the executor's nodeMatches
// also binds on a body `class`/`label`. A vertex whose key type and body class
// disagree — which Contract #1 does not permit and no gate enforces — could bind
// a position this count does not see. That hole is one already-filed item across
// every site that reads a label as a key type, not a new one here.
func (p *Pipeline) oneKeyAnswerSound(rs ruleState) bool {
	if p.actorEnumerator == nil {
		return false
	}
	if p.sweeper == nil {
		return false
	}
	return ActorTypeBindsAnchorOnly(rs.anchorHops, p.actorEnumerator.actorType)
}

// ActorTypeBindsAnchorOnly is the PATTERN half of oneKeyAnswerSound: everything
// decidable from the compiled pattern alone, with no pipeline state. It answers
// only the question its name asks — the healer conjunct is the caller's, because
// no cypher carries it. It is exported so the corpus census (internal/refractor's
// actor_onekey_corpus_census_test.go, §18.5's acceptance) asks the RUNNING rule
// rather than restating it: a census that re-derives what it pins goes green
// while the two drift, and the direction that drifts silently here is a lens
// quietly gaining the narrow answer.
func ActorTypeBindsAnchorOnly(idx full.HopIndex, actorType string) bool {
	if !idx.Complete || idx.UnresolvedExpansionPosition() >= 0 {
		return false
	}
	positions := idx.PositionsBinding(actorType)
	return len(positions) == 1 && positions[0] == idx.Anchor
}

// PeerAnchorMode selects whether an event on a vertex of the ACTOR type may
// reach anchors other than that vertex — the widening
// auth-plane-projection-latency-design.md §18.1's second bullet closes.
//
// It is an operator knob rather than a constant because the widening's cost is
// bounded by nothing the design fixes: on a lens the pattern argument cannot
// clear, one identity event walks to every identity the adjacency graph reaches
// within maxDepth, which in a real tenant is a shared role at depth 1 and the
// tenant's identity population at depth 2 — capped only by DefaultActorMaxSet.
// `REFRACTOR_ANCHOR_DERIVATION=off` is NOT a way back: it routes to the
// enumerator, which is the walking arm. Without this knob there is none.
//
// `on` is the built-in, and turning it `off` reinstates a known
// under-approximation — a stale grant, or a missed retraction on a tombstone. It
// is a containment lever for an operator watching a lens melt, not a posture to
// deploy in.
type PeerAnchorMode int

const (
	// PeerAnchorModeUnset means "take the package default", and is the zero
	// value deliberately: the per-pipeline override is an atomic whose unset
	// state is zero, so zero must mean unset rather than a real mode.
	PeerAnchorModeUnset PeerAnchorMode = iota
	PeerAnchorModeOff
	PeerAnchorModeOn
)

func (m PeerAnchorMode) String() string {
	switch m {
	case PeerAnchorModeOff:
		return "off"
	case PeerAnchorModeOn:
		return "on"
	default:
		return "unset"
	}
}

// ParsePeerAnchorMode maps an operator-supplied string onto a mode, rejecting
// rather than guessing — a typo resolving silently to `off` would reinstate the
// under-approximation on a lens whose grants someone is watching, and nothing
// would say so.
func ParsePeerAnchorMode(s string) (PeerAnchorMode, error) {
	switch s {
	case "on":
		return PeerAnchorModeOn, nil
	case "off":
		return PeerAnchorModeOff, nil
	default:
		return PeerAnchorModeUnset, fmt.Errorf("pipeline: unknown actor-peer-anchor mode %q (want on or off)", s)
	}
}

// defaultPeerAnchorMode is the process-wide posture every pipeline without its
// own override uses. Package-level for the same reason defaultDerivationMode is:
// the operator decision is one per process (cmd/refractor reads
// REFRACTOR_ACTOR_PEER_ANCHORS once) while pipelines are built in two separate
// places, and threading a startup flag through both makes it possible to miss
// one.
//
// LIFETIME: written once at boot and by tests; read per event. It is an operator
// posture, not evaluation state, so it is deliberately NOT reset or re-derived
// at rebuild, replay, reconnect, tombstone or rule hot-reload — a rule swap
// silently re-arming a widening an operator had turned off is the failure this
// placement avoids. It does not survive the process, which is correct: the env
// var is re-read at the next boot.
var defaultPeerAnchorMode atomic.Int64

// SetDefaultActorPeerAnchorMode sets the posture every pipeline without its own
// override uses. PeerAnchorModeUnset restores the built-in.
func SetDefaultActorPeerAnchorMode(m PeerAnchorMode) { defaultPeerAnchorMode.Store(int64(m)) }

// DefaultActorPeerAnchorMode reports that posture resolved to a real mode rather
// than to Unset, so a host can state at boot which behaviour it runs.
func DefaultActorPeerAnchorMode() PeerAnchorMode {
	if m := PeerAnchorMode(defaultPeerAnchorMode.Load()); m != PeerAnchorModeUnset {
		return m
	}
	return PeerAnchorModeOn
}

// SetActorPeerAnchorMode overrides the posture for this pipeline alone —
// a host quarantining one melting lens, and the form tests use so they never
// mutate package state. PeerAnchorModeUnset returns it to the package default.
func (p *Pipeline) SetActorPeerAnchorMode(m PeerAnchorMode) { p.peerAnchorMode.Store(int64(m)) }

func (p *Pipeline) peerAnchorsEnabled() bool {
	if m := PeerAnchorMode(p.peerAnchorMode.Load()); m != PeerAnchorModeUnset {
		return m == PeerAnchorModeOn
	}
	return DefaultActorPeerAnchorMode() == PeerAnchorModeOn
}
