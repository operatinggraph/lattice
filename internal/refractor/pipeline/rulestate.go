package pipeline

import (
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// ruleState is a coherent snapshot of everything useFullEngineBranches
// rewrites — the fields ruleMu guards. Taken once per consumer entry and
// threaded down, so a MATCH hot-reload landing mid-event cannot show one gate
// the new rule and the next gate the old one.
//
// Its maps and slices alias the Pipeline's own, which is safe precisely
// because that publication is copy-on-write (see ruleMu). Nothing here may be
// mutated.
type ruleState struct {
	gen                 uint64
	engineKind          string
	engine              *full.Engine
	cr                  ruleengine.CompiledRule
	branches            []ruleengine.CompiledRule
	walkOwnedColumns    map[string]int
	reprojectLabels     map[string]struct{}
	reprojectAll        bool
	reprojectRelations  map[string]struct{}
	relationsExhaustive bool
	seedAnchorLabels    map[string]struct{}
	anchorHops          full.HopIndex
	// walkScope bounds which relations the ActorEnumerator's BFS follows at
	// each vertex type, nil for the relation-blind walk — see walkScope.
	//
	// Derived over EVERY branch, not the single-walk anchorHops beside it: the
	// enumerator serves the whole lens, so a scope taken from one branch of a
	// multi-walk lens would prune the relations its other branches traverse.
	walkScope *walkScope
	// walkScopeRefusal names the conjunct that left walkScope nil, "" when a
	// scope was derived. It rides the snapshot for the same reason
	// narrowingBlocked does: it is a property OF this compiled rule, derived
	// where the decision is made and read where it is reported, published
	// atomically with the scope it explains.
	walkScopeRefusal string
	// rootHops is the plain arm's own scan-root pattern graph — see the
	// Pipeline field of the same name, which this publishes into, and §10 of
	// plain-lens-neighbour-anchor-derivation-design.md for its lifetime.
	rootHops full.HopIndex
	// declaresActorAnchor is true when this rule's cypher pins a pattern
	// position with `{key: $actorKey}` — the lens's DECLARED projection kind,
	// as against p.actorEnumerator, which is what the host has INSTALLED so
	// far. ConsumerFilter compares the two: declared actor-anchored with no
	// enumerator is an install that has not finished, and the only input in
	// that derivation whose absence is otherwise indistinguishable from a
	// genuinely plain lens.
	//
	// Derived over every branch, not only the single-walk arm anchorHops takes:
	// a multi-walk Personal lens's branches each carry their own anchor, and
	// one branch declaring it is enough to make the lens actor-anchored. It
	// rides the rule snapshot for the same reason narrowingBlocked does — it is
	// a property OF this compiled rule, published atomically with it, so a
	// reload cannot leave a previous rule body's declaration standing.
	declaresActorAnchor bool
	// labelExpansion is the taxonomy expansion this rule state's matcher,
	// anchor graph and seed set were all built from — see the Pipeline field
	// of the same name, which this publishes into.
	labelExpansion map[string]map[string]struct{}
	// narrowingBlocked is why reprojectAll is set — one of health's
	// FilterBroadReason values, "" when the label set IS exhaustive. It rides
	// the rule snapshot rather than living in its own state because the cause
	// is a property OF this compiled rule and of nothing else: it is derived
	// where the decision is made (useFullEngineBranches) and read where the
	// decision is reported (ConsumerFilter), with a rule swap replacing it
	// wholesale under the same atomic publication as the label set it explains.
	// A separate latch would be a second thing to keep in sync with the very
	// value it describes.
	//
	// A never-compiled pipeline (no engine wired, or a non-full one — nothing
	// but useFullEngineBranches ever publishes a rule) carries the zero value
	// "", which ConsumerFilter reports as not-eligible rather than as a missing
	// reason.
	narrowingBlocked string
}

// ruleState returns the pipeline's current compiled rule as one snapshot.
//
// Callers must take it ONCE at the top of an operation and pass it down rather
// than re-reading per gate. The reason is COHERENCE, not lock safety: this
// function holds ruleMu only for the struct copy and releases before it
// returns, so a second call is always sequential and cannot deadlock. What a
// second call costs is the guarantee — two snapshots in one operation can
// straddle a hot-reload, which is exactly the incoherence ruleMu exists to
// remove.
//
// The returned maps are the live published ones, safe to read outside the lock
// only because publication is copy-on-write. No caller may mutate them — that
// includes what ActorAwareNarrowingLabels and NarrowedFilterEligible hand back.
func (p *Pipeline) ruleState() ruleState {
	p.ruleMu.RLock()
	defer p.ruleMu.RUnlock()
	return ruleState{
		gen:                 p.ruleGen,
		engineKind:          p.engineKind,
		engine:              p.fullEngine,
		cr:                  p.fullCR,
		branches:            p.fullCRBranches,
		walkOwnedColumns:    p.fullCRWalkOwnedColumns,
		reprojectLabels:     p.plainReprojectLabels,
		reprojectAll:        p.plainReprojectAll,
		reprojectRelations:  p.plainReprojectRelations,
		relationsExhaustive: p.plainRelationsExhaustive,
		seedAnchorLabels:    p.seedAnchorLabels,
		anchorHops:          p.anchorHops,
		walkScope:           p.walkScope,
		walkScopeRefusal:    p.walkScopeRefusal,
		rootHops:            p.rootHops,
		declaresActorAnchor: p.declaresActorAnchor,
		labelExpansion:      p.labelExpansion,
		narrowingBlocked:    p.plainNarrowingBlocked,
	}
}

// carriedLabelExpansion returns the expansion the currently published rule
// state matches against — this pipeline's last known good answer, nil when no
// publication has ever resolved one.
//
// It is read exactly once, by useFullEngineBranches' live-re-derivation
// StatusUnknown arm, BEFORE that call publishes anything: keeping the matcher
// on the set it is already using is what makes an unresolvable taxonomy a
// delivery widening rather than a projection blackout. That arm checks the
// map COVERS every label the rule in hand expands (labelsWithoutExpansion)
// before using it — this returns whatever was last published, which is not by
// itself a promise about any particular rule's labels.
func (p *Pipeline) carriedLabelExpansion() map[string]map[string]struct{} {
	p.ruleMu.RLock()
	defer p.ruleMu.RUnlock()
	return p.labelExpansion
}

// publishRuleState installs a freshly-derived rule as one atomic swap.
func (p *Pipeline) publishRuleState(rs ruleState) {
	p.ruleMu.Lock()
	defer p.ruleMu.Unlock()
	p.ruleGen++
	p.engineKind = rs.engineKind
	p.fullEngine = rs.engine
	p.fullCR = rs.cr
	p.fullCRBranches = rs.branches
	p.fullCRWalkOwnedColumns = rs.walkOwnedColumns
	p.plainReprojectLabels = rs.reprojectLabels
	p.plainReprojectAll = rs.reprojectAll
	p.plainReprojectRelations = rs.reprojectRelations
	p.plainRelationsExhaustive = rs.relationsExhaustive
	p.seedAnchorLabels = rs.seedAnchorLabels
	p.anchorHops = rs.anchorHops
	p.walkScope = rs.walkScope
	p.walkScopeRefusal = rs.walkScopeRefusal
	p.rootHops = rs.rootHops
	p.declaresActorAnchor = rs.declaresActorAnchor
	p.labelExpansion = rs.labelExpansion
	p.plainNarrowingBlocked = rs.narrowingBlocked
}

// seedAnchorFor returns the vertex key an event on (eventLabel, eventKey) may
// seed this lens's evaluation with — narrowing it to that one anchor — or ""
// when the evaluation must recompute the lens's whole row set as it always
// has. It is the pipeline half of refractor-footprint-reduction-design.md
// §D2's eligibility; the engine independently re-derives that the key's own
// type matches the compiled anchor pattern's label before narrowing anything.
//
// Every conjunct is a correctness requirement, not a heuristic:
//
//   - eventLabel is a member of seedAnchorLabels — only a mutation of the
//     anchor ITSELF (or, for a `*`-suffixed anchor, one of its
//     taxonomy-resolved concrete subtypes) bounds the change to one anchor.
//     A neighbor (referenced non-anchor type) event can affect any number of
//     anchors through the walk, and deriving which ones is §D2 Phase 2; it
//     keeps the full recompute.
//   - no ActorEnumerator and no envelope — an actor-aware/personal evaluation
//     is already scoped to one actor, and its "anchor" is that actor, not the
//     event vertex; seeding it with an event key would evaluate the wrong
//     entity.
//   - DiffRetraction off — that retraction diffs the target's FULL live key
//     set against the evaluation's row set, so a single-anchor row set would
//     read as "every other anchor's rows are gone" and retract them all. This
//     conjunct is what makes applyDiffRetraction unreachable from a seeded
//     evaluation.
func (p *Pipeline) seedAnchorFor(rs ruleState, eventLabel, eventKey string) string {
	if eventKey == "" {
		return ""
	}
	if _, ok := rs.seedAnchorLabels[eventLabel]; !ok {
		return ""
	}
	if p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil {
		return ""
	}
	if p.diffRetraction {
		return ""
	}
	return eventKey
}

// plainReactsTo reports whether the plain aspect/link reprojection arms should
// re-execute this lens for an event whose owner/endpoint vertex has the given
// type. A lens with an exhaustive label set reprojects only for types its
// patterns can bind.
func (rs ruleState) plainReactsTo(vertexType string) bool {
	if rs.engineKind != ruleengine.EngineFull {
		return false
	}
	if rs.reprojectAll {
		return true
	}
	_, ok := rs.reprojectLabels[vertexType]
	return ok
}

// linkRelationReactsTo reports whether a link event on the given relation can
// affect this lens at all. It is the RELATION half of a link key's judgment,
// and every link arm pairs it with an ENDPOINT-TYPE half: the plain arm's
// plainReactsTo on each endpoint (evalPlainLinkReprojection), the actor-aware
// arm's §4.2 label set (actorAwareLinkRelevant). That half asks whether either
// endpoint type can bind, this one whether the relation between them is one the
// lens's patterns actually traverse. A link satisfying only the endpoint test —
// `lnk.service.<id>.providedTo.identity.<id>` reaching a lens whose sole
// relationship pattern is `(pr)-[:identifiedBy]->(id:identity)` — cannot appear
// in any traversal, and re-executing for it is pure cost.
//
// ONE predicate for both arms is what entitles ConsumerFilter to pin the
// relation segment of a narrowed filter's link subjects: the server then
// withholds exactly the links some arm skips anyway, rather than making a
// second, independently-fallible judgment about them.
//
// The false case lands in the SAME already-sanctioned skip class as the
// endpoint test's: no reprojection, and no adjacency self-apply, whose
// authoritative writer is the dedicated whole-stream adjacency consumer rather
// than any lens pipeline.
//
// Every uncertain case defaults to relevant, exactly as plainReactsTo does: a
// non-full engine, an empty/unparsed relation, or a non-exhaustive relation set
// (an untyped or variable-length relationship anywhere in the lens) all
// reproject.
func (rs ruleState) linkRelationReactsTo(relation string) bool {
	if rs.engineKind != ruleengine.EngineFull {
		return true
	}
	if !rs.relationsExhaustive || relation == "" {
		return true
	}
	_, ok := rs.reprojectRelations[relation]
	return ok
}

// plainVertexRelevant reports whether a plain (non-actor-aware) lens's
// KindVertex handling should evaluate a vertex-root event of the given type,
// or skip-and-Ack it as irrelevant. It shares plainReactsTo's label data but
// NOT its default: plainReactsTo's false case only tells the aspect/link
// arms not to run their OWN special-cased reprojection, which is always safe
// because the caller has no other write path to lose — whereas this gate's
// false case drops the vertex-root CDC event outright, with no fallback. A
// wrong "irrelevant" here would blind the lens to real writes, so every
// uncertain case must default to relevant: a non-full engine (plainReactsTo
// itself only exists for the full engine's label data, so an engine that
// isn't full has none to trust), an empty/unrecognized vertex type, or a
// non-exhaustive referenced-label set (plainReprojectAll) all fall through
// to evaluation — this gate only ever narrows a full-engine lens's exhaustive
// label set, it never re-scopes what any other lens evaluates. Only a
// full-engine lens with an exhaustive label set that provably excludes
// vertexType is skipped.
func (rs ruleState) plainVertexRelevant(vertexType string) bool {
	if rs.engineKind != ruleengine.EngineFull {
		return true
	}
	if rs.reprojectAll || vertexType == "" {
		return true
	}
	_, ok := rs.reprojectLabels[vertexType]
	return ok
}

// ActorAwareNarrowingLabels reports whether this actor-aware pipeline's fan-out
// arms may skip an event whose vertex types its compiled patterns provably
// cannot bind, and if so the exhaustive label set to judge against. It is the
// conjunction in auth-plane-projection-latency-design.md §4.2, and every
// conjunct is fail-closed: any one of them failing yields the pipeline's
// existing unconditional fan-out.
//
// It is evaluated per event rather than snapshotted at installation, mirroring
// seedAnchorFor. Activation installs these components in stages —
// UseFullEngineBranches, then projection.InstallActorAggregate, then
// SetSecureDecryptor (cmd/refractor/main.go) — so a snapshot taken during
// installation would read a later stage's component as absent. For the
// decryptor conjunct specifically that would narrow every Secure Lens, which is
// the one case the conjunct exists to refuse.
func (p *Pipeline) ActorAwareNarrowingLabels() (map[string]struct{}, bool) {
	return p.actorAwareNarrowingLabels(p.ruleState())
}

// actorAwareNarrowingLabels is ActorAwareNarrowingLabels against a snapshot the
// caller already holds — the form every in-pipeline caller uses, since taking a
// second snapshot mid-event would reintroduce exactly the incoherence ruleMu
// exists to remove.
func (p *Pipeline) actorAwareNarrowingLabels(rs ruleState) (map[string]struct{}, bool) {
	// Not actor-aware: the plain arms own their own gates.
	if p.actorEnumerator == nil {
		return nil, false
	}
	// ReferencedLabels only exists for the full engine, and only an exhaustive
	// set proves a type cannot bind.
	if rs.engineKind != ruleengine.EngineFull || rs.reprojectAll {
		return nil, false
	}
	// An input outside the compiled pattern breaks the closure claim.
	if !p.patternClosedOutput {
		return nil, false
	}
	// Narrowing removes an incidental reprojection that today happens to heal a
	// lost row. The convergence sweep is the standing healer that replaces it,
	// and sweepEnrolment may refuse with only a warning (projection/driver.go),
	// so a lens without a plan must not also lose the accident.
	//
	// This proves the plan is INSTALLED, not that a healer is turning: the sweep
	// runs only where the host also calls RunSweep (cmd/refractor does). A host
	// that installs lenses without starting sweeps gets narrowing with no
	// standing healer, and Sweeper.suppressed additionally idles the tick while
	// the lens is non-active or rebuilding.
	if p.sweeper == nil {
		return nil, false
	}
	// The anchor's own soft-delete arrives as a vertex event of the anchor's
	// type. A lens that cannot see that type would never retract the anchor's
	// row, and on the auth plane a missed retraction is an over-grant.
	if _, ok := rs.reprojectLabels[p.actorEnumerator.actorType]; !ok {
		return nil, false
	}
	// A key holder is not implied by the pattern: the decryptor resolves custody
	// from the ciphertext's own keyId, and a holder vertex may not be one the
	// cypher binds at all. The in-band scrub is a CDC event on <holder>.piiKey,
	// so a narrowed lens that cannot see a declared holder type would never be
	// delivered that holder's destruction and would keep projecting decrypted
	// plaintext. Judge against what the lens DECLARED — the declaration is the
	// only place a holder type is knowable without parsing compiled cypher.
	//
	// This conjunct guards a combination that cannot exist yet, and claims
	// nothing about the lenses that ship today: reaching here at all requires an
	// actorEnumerator (narrowedFilterEligible), and a secure lens is refused on
	// any non-empty projectionKind at translate time, so every shipped secure
	// lens takes the PLAIN branch, which carries no holder-type conjunct. What
	// contains the exposure meanwhile is pkgmgr's custody-scope gate, which
	// refuses a non-identity holder at install. Whoever lifts either ban owns
	// carrying this requirement onto the arm they open.
	if p.secureDecryptor != nil {
		for _, holderType := range p.secureDecryptor.HolderTypes() {
			if _, ok := rs.reprojectLabels[holderType]; !ok {
				return nil, false
			}
		}
	}
	return rs.reprojectLabels, true
}

// actorAwareFanOutRelevant reports whether the actor-aware fan-out arms must run
// for an event touching the given vertex types — the counterpart of
// plainReactsTo/plainVertexRelevant for the arms D1 and Fire 3 excluded. An
// event is relevant when ANY of the types it touches is in the label set, so a
// link is skipped only when NEITHER endpoint can bind.
//
// Every uncertain case defaults to relevant: an ineligible pipeline, an empty
// or unparsed type, or no types at all.
func (p *Pipeline) actorAwareFanOutRelevant(rs ruleState, types ...string) bool {
	if len(types) == 0 {
		return true
	}
	labels, ok := p.actorAwareNarrowingLabels(rs)
	if !ok {
		return true
	}
	return anyTypeBindable(labels, types...)
}

// actorAwareLinkRelevant reports whether the actor-aware link fan-out must run
// for a link event on the given relation between the given endpoint types. It
// is actorAwareFanOutRelevant's link form, carrying the conjunct the aspect and
// vertex arms have no key segment for: a link is relevant when the lens's
// patterns TRAVERSE its relation AND either endpoint type can bind.
//
// The two conjuncts arm together, behind the one §4.2 eligibility answer. That
// is what makes the pair the exact client-side counterpart of a
// relation-narrowed filter's link subjects, which pin a (label, relation) pair
// in each direction: a lens that fails §4.2 keeps the unconditional fan-out on
// both axes and takes the broad filter, and a lens that clears it skips on both
// axes and has its server-side subjects pinned on both.
//
// The relation conjunct is sound for the same reason the endpoint one is, and
// on the same terms: an event that cannot enter any traversal cannot move a row
// that is a function of the pattern-bound subgraph, which is exactly what
// §4.2's patternClosedOutput conjunct asserts and what a Personal lens's
// out-of-pattern read gate denies. The fan-out's idempotent adjacency
// pre-apply is lost with either skip, and that is sound for both: it exists to
// stop THIS pipeline's reprojection racing ahead of its own trigger edge
// (evaluateLinkFanOut), and there is no reprojection left to order.
//
// The enumerator's own breadth is untouched. It walks adjacency
// relation-blind, including the fixed reportsTo hop, and adjacency is written
// by the dedicated whole-stream consumer rather than by this pipeline's
// deliveries — so a skipped link narrows nothing about WHICH anchors a later
// relevant event reaches.
func (p *Pipeline) actorAwareLinkRelevant(rs ruleState, relation, typeA, typeB string) bool {
	labels, ok := p.actorAwareNarrowingLabels(rs)
	if !ok {
		return true
	}
	if !rs.linkRelationReactsTo(relation) {
		return false
	}
	return anyTypeBindable(labels, typeA, typeB)
}

// anyTypeBindable reports whether any of types is one the label set can bind.
// It skips only on a POSITIVE proof that no named type is in the set, so an
// empty list and any type that failed to parse both read as bindable.
func anyTypeBindable(labels map[string]struct{}, types ...string) bool {
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if t == "" {
			return true
		}
		if _, in := labels[t]; in {
			return true
		}
	}
	return false
}

// vertexEventRelevant is the single gate the KindVertex arm consults for both
// pipeline shapes: a plain lens judges by plainVertexRelevant, an actor-aware
// one by §4.2's conjunction.
func (p *Pipeline) vertexEventRelevant(rs ruleState, vertexType string) bool {
	if p.actorEnumerator != nil {
		return p.actorAwareFanOutRelevant(rs, vertexType)
	}
	return rs.plainVertexRelevant(vertexType)
}

// linkEventRelevant is the single gate both link arms consult, the KindLink
// counterpart of vertexEventRelevant: an actor-aware lens judges by §4.2's
// conjunction plus the traversed-relation set, a plain lens by plainReactsTo on
// each endpoint plus the same relation set. False means the link cannot appear
// in any of the lens's traversals, so the arm acks and skips it.
func (p *Pipeline) linkEventRelevant(rs ruleState, relation, typeA, typeB string) bool {
	if p.actorEnumerator != nil {
		return p.actorAwareLinkRelevant(rs, relation, typeA, typeB)
	}
	return rs.linkRelationReactsTo(relation) &&
		(rs.plainReactsTo(typeA) || rs.plainReactsTo(typeB))
}

// LinkEventRelevant reports whether this pipeline's link arm would do any work
// for a link CDC event on a Contract #1 lnk.<typeA>.<idA>.<relation>.<typeB>.
// <idB> key — the CLIENT-side half of the decision whose SERVER-side half is the
// link subjects ConsumerFilter derives.
//
// It exists so a census can assert the two halves against each other on the real
// shipped corpus rather than assuming they agree. It answers off the same gate
// the delivery path runs, never a restatement of it: a probe that reimplemented
// the condition would agree with a broken gate.
func (p *Pipeline) LinkEventRelevant(relation, typeA, typeB string) bool {
	return p.linkEventRelevant(p.ruleState(), relation, typeA, typeB)
}
