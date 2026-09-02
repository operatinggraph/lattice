package pipeline

import (
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// walkScope bounds which adjacency relations the ActorEnumerator's BFS follows
// while standing on a vertex of a given type — refractor-hub-walk-and-periodic-
// load-design.md §5.1.
//
// The soundness argument is the pattern's. Let A be an actor whose row depends
// on event vertex V. The compiled pattern binds a path
// A = X₀ –h₁– X₁ – … –h_k– X_k = V where every hᵢ is a pattern hop and every Xᵢ
// is bound at a position admitting type(Xᵢ) (a variable-length hop expands into
// a chain of same-relation edges through unlabeled intermediates). The BFS runs
// that path in reverse: standing on Xᵢ it crosses hᵢ to reach Xᵢ₋₁. So a walk
// that, at a vertex of type T, follows only the relations of hops incident to
// some position admitting T still reaches A. Every hop of every branch is
// collected, so the scope is a superset of what any single path needs.
//
// It exists because the relation-blind walk crosses Contract #1's `instanceOf`
// descriptor edge: a type descriptor holds one link per instance of that type,
// so one event on one instance expands every instance of the same type and then
// every actor attached to any of them. No pattern passes THROUGH a descriptor
// position, so for a lens whose hops do not name `instanceOf` the scope prunes
// that edge without a per-type special case.
//
// It is an OVER-approximation, and one residual is worth naming: byType is keyed
// by vertex TYPE, not by pattern position, so a lens whose own pattern binds
// `instanceOf` between two `service` positions (edgeInstances, edgeProviderQueue,
// edgeManifestProviderReadGrants, capabilityServiceAccess) still follows that
// relation at every service — instance to template, and back out to the
// template's sibling instances and their holders. Only a POSITION-directed walk
// removes that, which is what the affected-anchor derivation already is
// (walkToAnchors); no per-type scope can, because at a service the two ends of
// the hop are the same type. The scope is the fallback arm's improvement, not a
// replacement for the derivation.
//
// A nil *walkScope allows everything and is the fail-closed answer: every shape
// the derivation cannot resolve keeps the relation-blind walk exactly as it is,
// so a scope bug degrades to today's breadth rather than to a missed anchor.
//
// THE PATTERN ARGUMENT IS NOT THE WHOLE PREDICATE, for the same reason
// oneKeyAnswerSound's is not. Scoping the walk is a NARROWING: it stops
// reprojecting an actor merely because an out-of-pattern neighbour changed, and
// that incidental reprojection is today the only thing that converges a row left
// stale by something no CDC event will name — an out-of-band delete, a
// Capability-KV grant flip. So auth-plane-projection-latency-design.md §4.2's
// second conjunct governs here too: a lens may give up the accident only where a
// standing healer will still repair the row. Two healers count, one per plane,
// and standingHealerInstalled is where both are read:
//
//   - the auth/business plane's convergence sweep — p.sweeper, installed by
//     projection.InstallActorAggregate's enrolment gate (projection/driver.go),
//     which may refuse warn-only, so a lens's own install decides it;
//   - the personal plane's grantchange.PersonalSweeper — "the personal plane's
//     standing healer" (grantchange/sweeper.go) — plus the D1 grant-change edge
//     (personal-lens-grant-change-trigger-design.md §4.3), read through
//     Pipeline.personalPlaneHealer, which cmd/refractor sets at its
//     grantReprojector.RegisterPersonal call. A Personal Lens never receives a
//     SweepPlan, so without this half every one of them would read as unhealed.
//
// An actor-aware lens with NEITHER keeps the relation-blind walk and its
// accident, and WalkScopeRefusal says so.
//
// The hierarchy hop is deliberately outside all of this: it reads only
// `reportsTo` whatever the scope says, because that hop never followed anything
// else and so never depended on the accident. Narrowing its READ removes no
// reprojection.
//
// LIFETIME: the PATTERN half is derived once per rule publication
// (useFullEngineBranches) and published on ruleState under ruleMu with the rest
// of the compiled rule, so a hot reload can never leave a previous rule body's
// scope standing; it is never mutated after publication — readers alias the
// published maps. The HEALER half is evaluated per event (walkScopeFor), not
// snapshotted, because both healers are installed AFTER useFullEngineBranches
// runs — SetSweepPlan from InstallActorAggregate, the personal flag from the
// host's registration — so a snapshot taken at publication would read every lens
// as unhealed. oneKeyAnswerSound reads p.sweeper live for exactly this reason.
type walkScope struct {
	// byType maps a vertex type onto the relations the walk may follow while
	// standing on a vertex of that type. A type with no entry admits no
	// relation beyond anyType: no pattern position binds it, so no pattern path
	// stands on it.
	byType map[string]map[string]struct{}

	// anyType holds relations followable at EVERY vertex type. A hop incident
	// to an unlabeled position (which binds any type) contributes here, as does
	// a variable-length hop, whose expansion stands on unlabeled intermediates.
	anyType map[string]struct{}

	// wildcard holds the vertex types that may follow every relation, which an
	// UNTYPED hop incident to a position admitting those types produces: such a
	// hop matches any relation, so nothing about the relation is knowable. An
	// untyped hop at an unlabeled position makes the whole scope wildcard, and
	// is represented as a nil *walkScope rather than an entry here.
	wildcard map[string]struct{}
}

// The conjuncts that refuse a scope, in a closed vocabulary. They are published
// on the rule state and read by the corpus census, which default-denies an
// unpinned one: a new refusal added to the derivation without a constant here
// would reach the census as a string nobody reviewed.
const (
	// The two INSTALL-level refusals, checked ahead of every cypher-level one
	// because they are properties of how the lens is wired rather than of what
	// it says, and they hold for the life of that wiring. A lens carrying either
	// may have a perfectly readable pattern graph; telling an operator about its
	// cypher would send them to edit the wrong thing.
	//
	// The operator knob is read first of all: someone who turned the scope off
	// must be told that, not told about a healer.
	walkScopeRefusalOperatorOff = "disabled by operator"
	walkScopeRefusalNoHealer    = "no standing healer"

	// walkScopeRefusalNoRulePublished closes the vocabulary's one remaining
	// hole: a pipeline that has never activated carries the zero ruleState,
	// whose scope is nil and whose refusal is "". Without this it could report
	// "not scoped" with no reason at all — and "no reason" is the one answer a
	// closed vocabulary must never contain.
	walkScopeRefusalNoRulePublished = "no rule has been published on this pipeline"

	// The cypher-level refusals. NotFullEngine and NoRule are unreachable from
	// useFullEngineBranches, which is deriveWalkScope's only production caller
	// (it always passes EngineFull and a non-empty rule list); they are kept
	// because TestDeriveWalkScope_NonFullEngineRefuses calls the derivation
	// directly and pins both, so neither is an unexercised string.
	walkScopeRefusalNotFullEngine       = "the pipeline is not running the full engine"
	walkScopeRefusalNoRule              = "no compiled rule"
	walkScopeRefusalNotFullRule         = "a branch did not compile to the full engine"
	walkScopeRefusalIncompleteIndex     = "a branch's pattern graph is incomplete"
	walkScopeRefusalUnresolvedExpansion = "a branch carries a `*` position with no resolved expansion"
	walkScopeRefusalUntypedHopUnlabeled = "a branch carries an untyped relationship at an unlabeled position"
	walkScopeRefusalMalformedIndex      = "a branch's pattern graph names a position it does not hold"
	walkScopeWildcardRelation           = "*"
)

// allows reports whether the walk may follow an edge named rel while standing
// on a vertex of vertexType. A nil scope allows everything.
//
// It is the RULE the BFS loop applies. relationsAt below is the read narrowing
// derived from the same data — the store is asked for fewer edges when it can
// be — and this predicate still decides what is followed, so a read that
// over-returns can never widen the walk.
func (s *walkScope) allows(vertexType, rel string) bool {
	if s == nil {
		return true
	}
	if _, ok := s.anyType[rel]; ok {
		return true
	}
	if _, ok := s.wildcard[vertexType]; ok {
		return true
	}
	rels, ok := s.byType[vertexType]
	if !ok {
		return false
	}
	_, ok = rels[rel]
	return ok
}

// relationsAt returns the exact relation set the walk may follow at a vertex of
// vertexType, and whether that set is finite. A non-finite answer (a nil scope,
// or a type an untyped hop made wildcard) means the caller must read the whole
// node, because no relation set describes what it may follow.
//
// An EMPTY finite set is the answer that pays: no pattern position admits this
// vertex type and no hop is unlabeled or ranged, so the walk crosses nothing
// from here and the store is never read at all.
func (s *walkScope) relationsAt(vertexType string) (map[string]struct{}, bool) {
	if s == nil {
		return nil, false
	}
	if _, ok := s.wildcard[vertexType]; ok {
		return nil, false
	}
	out := make(map[string]struct{}, len(s.anyType)+len(s.byType[vertexType]))
	for r := range s.anyType {
		out[r] = struct{}{}
	}
	for r := range s.byType[vertexType] {
		out[r] = struct{}{}
	}
	return out, true
}

// deriveWalkScope builds the scope from EVERY compiled branch's pattern graph,
// returning nil and the conjunct that refused when any of them cannot be read.
//
// Every branch, not the single-walk ruleState.anchorHops: a multi-walk lens
// evaluates N independent queries and its walk has to serve all of them, so a
// scope taken from one branch would prune the relations another branch
// traverses — an anchor never reprojected, which on the auth plane is a
// retraction that never fires. One unreadable branch refuses the whole scope
// for the same reason.
//
// expansion is the taxonomy-resolved concrete set for every `*` label, threaded
// in exactly as the anchor graph threads it: a `*` position admits the types in
// its resolved closure, and an unresolved one refuses the whole index rather
// than admitting nothing (admitting nothing PRUNES, which is the unsound
// direction for a walk).
func deriveWalkScope(engineKind string, rules []ruleengine.CompiledRule, expansion map[string]map[string]struct{}) (*walkScope, string) {
	if engineKind != ruleengine.EngineFull {
		return nil, walkScopeRefusalNotFullEngine
	}
	if len(rules) == 0 {
		return nil, walkScopeRefusalNoRule
	}
	s := &walkScope{
		byType:   map[string]map[string]struct{}{},
		anyType:  map[string]struct{}{},
		wildcard: map[string]struct{}{},
	}
	for _, c := range rules {
		fullCR, isFull := c.(*full.CompiledRule)
		if !isFull {
			return nil, walkScopeRefusalNotFullRule
		}
		ix := fullCR.AnchorHopIndex().WithLabelExpansion(expansion)
		if !ix.Complete {
			// An incomplete index stopped indexing at the shape it could not
			// read, so its hop list is a floor rather than the truth and a
			// relation missing from it is not evidence of absence.
			return nil, walkScopeRefusalIncompleteIndex
		}
		if ix.UnresolvedExpansionPosition() >= 0 {
			return nil, walkScopeRefusalUnresolvedExpansion
		}
		if refusal := s.addIndex(ix); refusal != "" {
			return nil, refusal
		}
	}
	return s, ""
}

// addIndex folds one branch's pattern graph into the scope, returning "" on
// success or the refusal that makes the whole scope unusable.
//
// Both refusals it can return are reachable only from a HopIndex built directly
// (the unit tests do): AnchorHopIndex refuses a pattern carrying an untyped
// relationship, the caller declines an incomplete index before reaching here,
// and its builder never emits a hop naming a position outside Labels. They are
// handled rather than assumed away so the rule this type states holds for any
// index it is handed.
func (s *walkScope) addIndex(ix full.HopIndex) string {
	// Both refusals are decided over the WHOLE index before anything is folded
	// in, so a refused index leaves the scope untouched rather than half-built.
	// deriveWalkScope discards it either way; being all-or-nothing is what lets
	// this function be reasoned about on its own.
	for _, h := range ix.Hops {
		for _, pos := range [2]int{h.From, h.To} {
			if pos < 0 || pos >= len(ix.Labels) {
				// A hop naming a position the label slice does not hold is an
				// index this function cannot read. Refusing the whole scope is
				// the only safe answer: skipping the hop would silently drop
				// the relations it contributes, and a relation missing from the
				// scope is an edge the walk stops crossing — the narrowing
				// direction, and the one this unit must never take on a guess.
				return walkScopeRefusalMalformedIndex
			}
			if h.Rel == "" {
				if _, admitsAny := admittedTypes(ix, pos); admitsAny {
					// Any relation, at any type: nothing is left to scope.
					return walkScopeRefusalUntypedHopUnlabeled
				}
			}
		}
	}

	for _, h := range ix.Hops {
		for _, pos := range [2]int{h.From, h.To} {
			types, admitsAny := admittedTypes(ix, pos)
			switch {
			case h.Rel == "":
				for _, t := range types {
					s.wildcard[t] = struct{}{}
				}
			case admitsAny:
				s.anyType[h.Rel] = struct{}{}
			default:
				for _, t := range types {
					s.addRelation(t, h.Rel)
				}
			}
		}
		// A ranged hop's expansion stands on the unlabeled intermediates
		// between its two bound positions, so the walk crosses this relation at
		// vertices of a type no position names. Min == Max == 1 — the
		// overwhelming majority — has no intermediate and adds nothing.
		if h.Rel != "" && h.Max > 1 {
			s.anyType[h.Rel] = struct{}{}
		}
	}
	return ""
}

func (s *walkScope) addRelation(vertexType, rel string) {
	rels, ok := s.byType[vertexType]
	if !ok {
		rels = map[string]struct{}{}
		s.byType[vertexType] = rels
	}
	rels[rel] = struct{}{}
}

// admittedTypes returns the concrete vertex types position pos admits, or
// admitsAny for a position that binds every type.
//
// A `*` position with no resolved concrete set reports admits-any rather than
// admits-nothing. Admitting nothing is fail-closed for a MATCHER and fail-OPEN
// for a walk — it would prune a vertex the pattern really binds — so the two
// motions are opposite and this one takes the wider reading. Its caller refuses
// such an index outright anyway (UnresolvedExpansionPosition); this keeps the
// wider answer for any caller that does not.
func admittedTypes(ix full.HopIndex, pos int) (types []string, admitsAny bool) {
	label := ix.Labels[pos]
	if label == "" {
		return nil, true
	}
	if pos < len(ix.LabelExpand) && ix.LabelExpand[pos] {
		if pos >= len(ix.Expanded) || len(ix.Expanded[pos]) == 0 {
			return nil, true
		}
		out := make([]string, 0, len(ix.Expanded[pos]))
		for t := range ix.Expanded[pos] {
			out = append(out, t)
		}
		return out, false
	}
	return []string{label}, false
}

// SetPersonalPlaneHealer records whether this pipeline is registered with the
// personal plane's standing healer — see Pipeline.personalPlaneHealer for why
// the host sets it at the registration call rather than deriving it here.
func (p *Pipeline) SetPersonalPlaneHealer(registered bool) {
	p.personalPlaneHealer.Store(registered)
}

// standingHealerInstalled reports whether SOMETHING would converge a row this
// lens stops incidentally reprojecting — §4.2's conjunct, with one arm per
// plane. Read live rather than snapshotted: both arms are installed after the
// rule is published.
func (p *Pipeline) standingHealerInstalled() bool {
	return p.sweeper != nil || p.personalPlaneHealer.Load()
}

// walkScopeFor returns the scope this pipeline's walk actually runs under,
// together with the refusal that emptied it. It is the one place the pattern
// half (published on the rule state) and the healer half (live pipeline state)
// are combined, so the walk, the accessors and the tally can never disagree
// about which posture a lens is in.
//
// The healer is checked first: it is a property of the lens's install, it holds
// for the life of that install, and reporting a cypher-level refusal for a lens
// that would be relation-blind anyway would send a reader to fix the wrong
// thing.
func (p *Pipeline) walkScopeFor(rs ruleState) (*walkScope, string) {
	if !p.walkScopeEnabled() {
		return nil, walkScopeRefusalOperatorOff
	}
	if !p.standingHealerInstalled() {
		return nil, walkScopeRefusalNoHealer
	}
	if rs.walkScope == nil && rs.walkScopeRefusal == "" {
		// Nothing has been published on this pipeline, so there is no pattern
		// graph to have refused. Named rather than left empty: a scoped==false
		// with no reason reads as a bug in this file.
		return nil, walkScopeRefusalNoRulePublished
	}
	return rs.walkScope, rs.walkScopeRefusal
}

// WalkScope reports the relation scope this pipeline's actor walk runs under:
// the per-type relation sets, the relations followable at every type, and
// whether a scope was derived at all. scoped == false is the relation-blind
// walk, and WalkScopeRefusal names the conjunct that produced it.
//
// The slices are sorted copies, so a caller cannot reach the published maps.
// A vertex type an untyped hop made wildcard is reported with the single
// relation "*" (walkScopeWildcardRelation), which is not a legal Contract #1
// relation name and so cannot collide with a real one.
//
// It exists so the corpus census asks the RUNNING derivation rather than
// restating it: a census that re-derives what it pins goes green while the two
// drift, and the direction that drifts silently here is a lens quietly gaining
// a narrower walk.
func (p *Pipeline) WalkScope() (byType map[string][]string, anyType []string, scoped bool) {
	scope, _ := p.walkScopeFor(p.ruleState())
	if scope == nil {
		return nil, nil, false
	}
	byType = make(map[string][]string, len(scope.byType)+len(scope.wildcard))
	for t, rels := range scope.byType {
		byType[t] = sortedRelations(rels)
	}
	for t := range scope.wildcard {
		byType[t] = []string{walkScopeWildcardRelation}
	}
	return byType, sortedRelations(scope.anyType), true
}

// WalkScopeRefusal names the conjunct that left this pipeline's walk
// relation-blind, or "" when a scope was derived.
func (p *Pipeline) WalkScopeRefusal() string {
	_, refusal := p.walkScopeFor(p.ruleState())
	return refusal
}

func sortedRelations(rels map[string]struct{}) []string {
	out := make([]string, 0, len(rels))
	for r := range rels {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// walkIsScoped reports whether THIS event's walk ran under a derived scope —
// the tally's own view of the posture, taken from the same rule snapshot the
// event was evaluated against rather than from a second read that could
// straddle a hot reload. It asks walkScopeFor, so it carries the healer
// conjunct as well as the pattern one.
func (p *Pipeline) walkIsScoped(rs ruleState) bool {
	if p.actorEnumerator == nil {
		return false
	}
	scope, _ := p.walkScopeFor(rs)
	return scope != nil
}

// WalkScopeMode selects whether this process's actor walks run pattern-scoped
// at all — the operator's way back from §5.1.
//
// It is a knob rather than a constant for the same reason PeerAnchorMode is:
// the narrowing's blast radius is bounded by an ARGUMENT rather than by
// anything the code checks. If a pattern really does bind an actor to an event
// vertex by a path this derivation does not see, the scope stops reaching that
// actor, and on the auth plane an anchor never reprojected is a grant that never
// retracts. `REFRACTOR_ANCHOR_DERIVATION=off` is NOT a way back: it routes to
// the enumerator, which is the arm the scope narrows. Without this knob there is
// none.
//
// `on` is the built-in, and turning it `off` reinstates the relation-blind walk
// — every lens back to the descriptor-hub expansion this design exists to end.
// It is a containment lever for an operator watching a lens miss anchors, not a
// posture to deploy in; and like every other lever here it bounds the NEXT
// event, healing nothing already stale (that is `lattice lens rebuild`'s job, or
// the sweep's).
type WalkScopeMode int

const (
	// WalkScopeModeUnset means "take the package default", and is the zero
	// value deliberately: the per-pipeline override is an atomic whose unset
	// state is zero, so zero must mean unset rather than a real mode.
	WalkScopeModeUnset WalkScopeMode = iota
	WalkScopeModeOff
	WalkScopeModeOn
)

func (m WalkScopeMode) String() string {
	switch m {
	case WalkScopeModeOff:
		return "off"
	case WalkScopeModeOn:
		return "on"
	default:
		return "unset"
	}
}

// ParseWalkScopeMode maps an operator-supplied string onto a mode, rejecting
// rather than guessing — a typo resolving silently to `off` would put every
// lens back on the relation-blind walk with nothing saying so.
func ParseWalkScopeMode(s string) (WalkScopeMode, error) {
	switch s {
	case "on":
		return WalkScopeModeOn, nil
	case "off":
		return WalkScopeModeOff, nil
	default:
		return WalkScopeModeUnset, fmt.Errorf("pipeline: unknown walk-scope mode %q (want on or off)", s)
	}
}

// defaultWalkScopeMode is the process-wide posture every pipeline without its
// own override uses. Package-level for the same reason defaultPeerAnchorMode
// is: the operator decision is one per process (cmd/refractor reads
// REFRACTOR_WALK_SCOPE once) while pipelines are built in two separate places,
// and threading a startup flag through both makes it possible to miss one.
//
// LIFETIME: written once at boot and by tests; read per event. It is an operator
// posture, not evaluation state, so it is deliberately NOT reset or re-derived
// at rebuild, replay, reconnect, tombstone or rule hot-reload — a rule swap
// silently re-arming a narrowing an operator had turned off is the failure this
// placement avoids. It does not survive the process, which is correct: the env
// var is re-read at the next boot.
var defaultWalkScopeMode atomic.Int64

// SetDefaultWalkScopeMode sets the posture every pipeline without its own
// override uses. WalkScopeModeUnset restores the built-in.
func SetDefaultWalkScopeMode(m WalkScopeMode) { defaultWalkScopeMode.Store(int64(m)) }

// DefaultWalkScopeMode reports that posture resolved to a real mode rather than
// to Unset, so a host can state at boot which behaviour it runs.
func DefaultWalkScopeMode() WalkScopeMode {
	if m := WalkScopeMode(defaultWalkScopeMode.Load()); m != WalkScopeModeUnset {
		return m
	}
	return WalkScopeModeOn
}

// SetWalkScopeMode overrides the posture for this pipeline alone — a host
// quarantining one lens, and the form tests use so they never mutate package
// state. WalkScopeModeUnset returns it to the package default.
func (p *Pipeline) SetWalkScopeMode(m WalkScopeMode) { p.walkScopeMode.Store(int64(m)) }

func (p *Pipeline) walkScopeEnabled() bool {
	if m := WalkScopeMode(p.walkScopeMode.Load()); m != WalkScopeModeUnset {
		return m == WalkScopeModeOn
	}
	return DefaultWalkScopeMode() == WalkScopeModeOn
}
