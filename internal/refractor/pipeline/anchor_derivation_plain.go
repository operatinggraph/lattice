// The plain arm's own affected-anchor derivation
// (plain-lens-neighbour-anchor-derivation-design.md, Increment 2). A plain
// (non-actor-anchored) lens's neighbour event — a vertex, aspect owner, or
// link endpoint of a type that is not the lens's own anchor pattern — reaches
// seedAnchorFor's empty-seed branch (evaluate.go) and today re-derives the
// lens's WHOLE row set. This file gives that branch a second producer,
// mirroring anchor_derivation.go's actor-aware trio (deriveAnchorsFor
// {Vertex,Aspect,Link}) but reading rs.rootHops (Increment 1's
// ScanRootHopIndex) instead of rs.anchorHops, and re-entering
// evaluatePlainFromVertex — the SAME entry point the anchor-typed arms
// already use — once per derived anchor instead of running the unseeded
// evaluation.
//
// This fire (§12: Incs 1+2) does NOT build Increment 3's licence (§5:
// per-anchor closure, an enrolled Auditor, no $now/$projectedAt, no secure
// decryptor, the auth-plane exclusion). plainDerivationIndexForAct below
// therefore always declines, so `act` mode can never let this derivation
// decide an event's outcome — only `shadow` mode's measurement runs the walk
// at all, through the SAME three-way mode switch and the SAME
// derivationShadow counters the actor-aware arm's affectedAnchors already
// uses (anchor_derivation_mode.go, anchor_derivation_shadow.go). A pipeline
// is one lens and is either plain or actor-anchored, never both, so sharing
// that counter state introduces no new state and cannot let the two
// measurements collide.
package pipeline

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// DefaultPlainDerivedAnchorCap bounds the number of anchor vertices the plain
// arm's derivation may return before the caller falls back to today's
// unseeded evaluation — a FALLBACK trigger, not a truncation, mirroring
// DefaultDerivationReadCap's own contract (anchor_derivation.go): a derived
// set this large costs more in K seeded evaluations than one whole-corpus
// rescan saves, so acting on it would cost more than it wins.
//
// Its unit is DERIVED ROOT VERTICES, never projected rows: for a lens keyed
// on a neighbour variable, K root bindings can produce a single output row,
// so this bound is a bound on WORK. §4.2's own obligation (iii) is that the
// measurement report both distributions rather than conflate the two.
const DefaultPlainDerivedAnchorCap = 64

// defaultPlainDerivedAnchorCap is the package-wide override, mirroring
// defaultDerivationMode's shape (anchor_derivation_mode.go): one process-wide
// knob because pipelines are built in more than one place, and a startup flag
// threaded through only one of them could be missed.
var defaultPlainDerivedAnchorCap atomic.Int64

// SetDefaultPlainDerivedAnchorCap sets the cap every pipeline without its own
// override uses. n <= 0 restores DefaultPlainDerivedAnchorCap.
func SetDefaultPlainDerivedAnchorCap(n int) {
	defaultPlainDerivedAnchorCap.Store(int64(n))
}

// SetPlainDerivedAnchorCap overrides the derived-anchor cap for this pipeline
// alone. n <= 0 returns it to the package default. Mirrors
// SetAnchorDerivationReadCap's shape (anchor_derivation.go).
func (p *Pipeline) SetPlainDerivedAnchorCap(n int) {
	p.plainDerivedAnchorCapOverride.Store(int64(n))
}

// plainDerivedAnchorCap resolves this pipeline's effective cap: its own
// override, else the package default, else DefaultPlainDerivedAnchorCap.
func (p *Pipeline) plainDerivedAnchorCap() int {
	if n := p.plainDerivedAnchorCapOverride.Load(); n > 0 {
		return int(n)
	}
	if n := defaultPlainDerivedAnchorCap.Load(); n > 0 {
		return int(n)
	}
	return DefaultPlainDerivedAnchorCap
}

// plainDerivationIndex returns rs.rootHops and whether this pipeline may
// derive from it at all — the plain arm's mirror of derivationIndex
// (anchor_derivation.go), with the plain pipeline's own conjuncts (§4.2 of
// the design):
//
//   - this IS a plain pipeline (no ActorEnumerator, no envelope of either
//     shape) — an actor-aware/personal evaluation's "anchor" is the actor
//     $actorKey names, not a vertex this walk could seed from, and seeding
//     it here would evaluate the wrong entity (mirrors seedAnchorFor's own
//     first conjunct, pipeline.go);
//   - a single-branch lens — a multi-walk lens has N independent queries,
//     each with its own scan root, and one graph cannot speak for all of
//     them (mirrors anchorHops' own multi-walk exclusion, useFullEngineBranches);
//   - rs.rootHops.Complete — every shape ScanRootHopIndex itself refuses
//     (hopindex.go) is not this derivation's to second-guess;
//   - no unresolved `*` position — pruning a far end the taxonomy cannot yet
//     confirm is the unsound direction for a derivation (HopIndex's own doc
//     on UnresolvedExpansionPosition);
//   - !p.diffRetraction — a per-anchor seeded row set would read to
//     applyDiffRetraction as "every OTHER anchor's rows are gone" (§3's
//     grounding ledger; the same conjunct seedAnchorFor already enforces at
//     pipeline.go's seedAnchorFor, inherited here rather than re-derived).
//
// There is no "anchor label == enumerator's actor type" conjunct to carry
// from derivationIndex: the terminus's OWN label IS the anchor label the
// caller seeds from — one derivation, so the two cannot disagree.
func (p *Pipeline) plainDerivationIndex(rs ruleState) (full.HopIndex, bool) {
	if p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil {
		return full.HopIndex{}, false
	}
	if len(rs.branches) > 1 {
		return full.HopIndex{}, false
	}
	if !rs.rootHops.Complete {
		return full.HopIndex{}, false
	}
	if rs.rootHops.UnresolvedExpansionPosition() >= 0 {
		return full.HopIndex{}, false
	}
	if p.diffRetraction {
		return full.HopIndex{}, false
	}
	return rs.rootHops, true
}

// plainDerivationIndexForAct is plainDerivationIndex plus Increment 3's
// licence (§5 of the design: per-anchor closure via AnchorProjectionKey's ok
// contract, an enrolled Auditor, ReferencesParam excluding $now/$projectedAt,
// no secure decryptor, and projection.IsAuthPlane threaded onto the plain
// pipeline). That licence is a separate, not-yet-built increment — this fire
// is §12's "Incs 1+2" row alone, sequenced ahead of the divergence Auditor
// the licence depends on.
//
// It therefore ALWAYS declines, unconditionally, until Increment 3 lands and
// rewrites this function to consult the real licence. Consequently `act`
// mode can never let the plain derivation decide an event's outcome this
// fire — only `shadow` mode's measurement (shadowPlainDerivation) runs the
// walk at all, through plainDerivationIndex directly, which carries none of
// these extra conjuncts. This is what makes the fire shadow-only by
// construction rather than by a mode default an operator could override:
// REFRACTOR_ANCHOR_DERIVATION=act changes nothing for a plain lens until
// Increment 3 ships.
func (p *Pipeline) plainDerivationIndexForAct(rs ruleState) (full.HopIndex, bool) {
	return full.HopIndex{}, false
}

// deriveAnchorsForPlainVertex returns the anchor keys whose projection a
// mutation of (vertexType, vertexKey) can change, under the plain arm's own
// scan-root graph. ok == false means the derivation declined and the caller
// must fall back to today's unseeded evaluation. Mirrors
// deriveAnchorsForVertex (anchor_derivation.go) exactly, substituting
// plainDerivationIndex for derivationIndex; walkToAnchors is reused
// unchanged (§4.1: one index type, two termini, zero duplicated consumers).
func (p *Pipeline) deriveAnchorsForPlainVertex(ctx context.Context, rs ruleState, vertexKey, vertexType string) ([]string, bool, error) {
	idx, ready := p.plainDerivationIndex(rs)
	if !ready {
		return nil, false, nil
	}
	_, id, parsed := substrate.ParseVertexKey(vertexKey)
	if !parsed {
		return nil, false, nil
	}
	var seeds []seededNode
	for _, pos := range idx.PositionsBinding(vertexType) {
		seeds = append(seeds, seededNode{pos: pos, id: id})
	}
	return p.walkToAnchors(ctx, idx, seeds)
}

// deriveAnchorsForPlainAspect is deriveAnchorsForPlainVertex seeded by the
// aspect's PARENT vertex — an aspect mutation changes what the parent's node
// properties render, and the pattern binds the parent, never the aspect key
// itself. Mirrors deriveAnchorsForAspect (anchor_derivation.go).
func (p *Pipeline) deriveAnchorsForPlainAspect(ctx context.Context, rs ruleState, aspectKey string) ([]string, bool, error) {
	parentVtx, parentType, _, _, ok := substrate.ParseAspectKey(aspectKey)
	if !ok {
		return nil, false, nil
	}
	return p.deriveAnchorsForPlainVertex(ctx, rs, parentVtx, parentType)
}

// deriveAnchorsForPlainLink returns the anchor keys a link create or
// tombstone can affect, seeded at the ANCHOR-SIDE endpoint of every pattern
// hop the link can bind (mirrors deriveAnchorsForLink, anchor_derivation.go).
// For the clinicProviders shape this is what collapses the neighbour
// endpoint's derivation into a duplicate of the anchor endpoint's own seed,
// with zero adjacency reads (§4.2's worked payoff trace) — built here for API
// parity with the shipped trio and for Increment 4b's future use; the live
// shadow seam this fire wires (evaluate.go) reaches every plain neighbour
// event, link endpoints included, through deriveAnchorsForPlainVertex alone,
// because by the time a link endpoint reaches that seam (via
// evaluatePlainFromVertex) it is already reduced to "evaluate from this one
// vertex" and is indistinguishable from a genuine vertex-root event —
// restructuring evalPlainLinkReprojection to call this instead, and skip the
// now-redundant per-endpoint evaluation, is Increment 4a/4b's write-behaviour
// change, not this shadow-only fire's.
func (p *Pipeline) deriveAnchorsForPlainLink(ctx context.Context, rs ruleState, linkKey string) ([]string, bool, error) {
	idx, ready := p.plainDerivationIndex(rs)
	if !ready {
		return nil, false, nil
	}
	srcType, srcID, rel, dstType, dstID, ok := substrate.ParseLinkKey(linkKey)
	if !ok {
		return nil, false, nil
	}
	var seeds []seededNode
	for _, s := range idx.AnchorSideSeeds(srcType, rel, dstType) {
		id := dstID
		if s.SrcIsAnchorSide {
			id = srcID
		}
		seeds = append(seeds, seededNode{pos: s.Pos, id: id})
	}
	return p.walkToAnchors(ctx, idx, seeds)
}

// evaluatePlainDerivedAnchors re-enters evaluatePlainFromVertex once per
// derived anchor — the same entry point the anchor-typed arms already use —
// and returns the combined, deduplicated result set. Unreachable this fire
// (plainDerivationIndexForAct always declines above), built and unit-tested
// directly so Increment 3 has a working, proven re-entry path to license
// rather than a stub to write from scratch.
//
// Dedupe is hoisted here (§4.2 obligation i): today only the link arm dedupes
// across its two endpoint evaluations (evalPlainLinkReprojection's own
// dedupeKeyFor loop); with K derived anchors every arm now carries K anchors'
// results and needs the same treatment.
//
// Error disposition (§4.2 obligation ii): the FIRST error aborts the WHOLE
// event, matching the link arm's shipped behaviour — a widening for the
// vertex/aspect call sites, which previously ran exactly one evaluation and
// so had no "some derived anchors succeeded, one didn't" case to decide.
// Redelivery re-runs all K, which is idempotent: each of the K is itself a
// plain evaluation through the pipeline's normal write path (upsert, or the
// presence-check Delete, itself idempotent against an already-absent key).
//
// FOR INCREMENT 3: evaluatePlainFromVertex calls the OUTER evaluateForEntry
// wrapper, which runs applySecureDecrypt on its own results. If this
// function is ever reached from evaluateForEntryRaw's own neighbour-event
// path (which is itself inside that same outer wrapper), a Secure Lens's
// columns would be decrypted once per derived anchor here AND AGAIN by the
// outer wrapper on return — a double-decrypt. It is genuinely unreachable
// THIS fire (plainDerivationIndexForAct always declines, so evaluatePlainNeighbourEvent
// never calls this from within evaluateForEntryRaw), and Increment 3's own
// §5.1 licence is supposed to exclude Secure lenses before anything can
// reach `act` — but verify that exclusion actually blocks this path (not
// just the licence's stated conjunct list) before flipping any mode that
// makes this function reachable from inside evaluateForEntryRaw.
func (p *Pipeline) evaluatePlainDerivedAnchors(ctx context.Context, rs ruleState, anchors []string, anchorLabel string) ([]ruleengine.EvalResult, error) {
	var combined []ruleengine.EvalResult
	seen := make(map[string]bool, len(anchors))
	for _, anchorKey := range anchors {
		results, err := p.evaluatePlainFromVertex(ctx, rs, anchorKey, anchorLabel)
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			id := dedupeKeyFor(r)
			if seen[id] {
				continue
			}
			seen[id] = true
			combined = append(combined, r)
		}
	}
	return combined, nil
}

// evaluatePlainNeighbourEvent decides how to answer a plain lens's neighbour
// event — one whose vertex / aspect-owner / link-endpoint is not the lens's
// own anchor type, so seedAnchorFor (evaluate.go) returned "" and today's
// shipped behaviour recomputes the lens's whole row set via an unseeded
// evaluation. It is the plain arm's own producer into the SAME three-way
// derivation-mode switch the actor-aware arm's affectedAnchors already uses
// (anchor_derivation_mode.go) — off/shadow/act govern both arms identically,
// and the shadow counters are the SAME derivationShadow struct (a pipeline is
// one lens and is either plain or actor-anchored, never both, so the fields
// cannot collide between the two measurements).
//
// Because plainDerivationIndexForAct always declines this fire (Increment
// 3's licence is unbuilt), `act` mode never lets this derivation decide an
// event's outcome — every path below that is not the shadow measurement
// itself returns EXACTLY what calling executeFullForActor with an empty seed
// would have returned before this increment existed. That is the fire's own
// invariant, stated in code: shadow decides nothing.
func (p *Pipeline) evaluatePlainNeighbourEvent(ctx context.Context, rs ruleState, entry ruleengine.NodeEntry) ([]ruleengine.EvalResult, error) {
	unseeded := func() ([]ruleengine.EvalResult, error) {
		return p.executeFullForActor(ctx, rs, entry.CoreKVKey, entry.Properties, "")
	}
	derive := func() ([]string, bool, error) {
		return p.deriveAnchorsForPlainVertex(ctx, rs, entry.CoreKVKey, entry.NodeLabel)
	}

	switch p.derivationMode() {
	case DerivationModeOff:
		return unseeded()
	case DerivationModeShadow:
		results, err := unseeded()
		if err != nil {
			return nil, err
		}
		p.shadowPlainDerivation(rs, derive)
		return results, nil
	case DerivationModeAct:
		// fall through to the act path below
	default:
		slog.Warn("pipeline: unknown anchor-derivation mode; using today's unseeded evaluation",
			"ruleId", p.ruleID, "mode", int(p.derivationMode()))
		return unseeded()
	}

	idx, ready := p.plainDerivationIndexForAct(rs)
	if !ready {
		// NOT counted as a fall-back, mirroring affectedAnchors' own static-
		// refusal treatment: this refusal is a property of the LENS (or, this
		// fire, of every lens — Increment 3 is unbuilt), fixed for the life
		// of a ruleState, and counting it every event would drown the ratio
		// the tally exists to report.
		p.noteStaticPlainDerivationRefusal(rs)
		return unseeded()
	}
	anchorLabel := idx.Labels[idx.Anchor]
	anchors, ok, err := derive()
	if err != nil {
		// A walk that errored says nothing about the event: adjacency is the
		// same store the unseeded evaluation is about to read, so the honest
		// response is to run it and let ITS error, if any, be the event's
		// outcome — mirroring affectedAnchors' own disposition.
		slog.Warn("pipeline: plain anchor derivation failed; falling back to today's unseeded evaluation",
			"ruleId", p.ruleID, "eventKey", entry.CoreKVKey, "err", err)
		p.recordDerivationFellBack()
		return unseeded()
	}
	if !ok {
		p.recordDerivationFellBack()
		return unseeded()
	}
	if cap := p.plainDerivedAnchorCap(); len(anchors) > cap {
		// A fallback, not a truncation (§4.2's caps, plural): the derived set
		// is real and correct, but K seeded evaluations this large cost more
		// than the one whole-corpus rescan they would replace.
		slog.Warn("pipeline: plain anchor derivation exceeded the derived-anchor cap; using today's unseeded evaluation",
			"ruleId", p.ruleID, "eventKey", entry.CoreKVKey, "derivedCount", len(anchors), "cap", cap)
		p.recordDerivationFellBack()
		return unseeded()
	}
	p.recordDerivationActed(len(anchors))
	return p.evaluatePlainDerivedAnchors(ctx, rs, anchors, anchorLabel)
}

// shadowPlainDerivation runs derive on a sampled fraction of events (the SAME
// 1-in-N sampler shadowAnchorDerivation uses, derivShadow.shouldSample) and
// records the derived-set SIZE — the measurement §11 of the design asks for —
// into the SAME derivationShadow counters the actor-aware arm's shadow uses.
// Unlike shadowAnchorDerivation there is no second, independently-computed
// set to diff against (a plain lens's shipped behaviour is a whole-corpus
// re-scan, not an enumerated anchor-key list), so only Sampled/DerivedAnchors
// and the Plain* fields below are meaningful for a plain pipeline; the
// actor-aware-only fields (Agreed, Narrowed*, Divergent*, BFSAnchors) simply
// stay at their zero value, which is safe because a pipeline is one lens and
// never reports both kinds of measurement.
//
// The three ways this can fail to answer are recorded under DISTINCT causes
// rather than one shared Declined, and the derived-set SIZE is recorded even
// when it is the reason for the refusal (PlainOverCapSize): folding "ready
// but too big" into a bare declined-with-no-size would make the derived-set
// -size distribution circular — truncated exactly at the cap it exists to
// justify, which is precisely the number §11 asks the fire to report.
//
// It never returns anything and never changes the event's outcome: a
// derivation failure here is an observation about the derivation, not about
// the event — the caller has already computed and will return the unseeded
// evaluation's results regardless.
func (p *Pipeline) shadowPlainDerivation(rs ruleState, derive func() ([]string, bool, error)) {
	if !p.derivShadow.shouldSample() {
		return
	}
	if _, ready := p.plainDerivationIndex(rs); !ready {
		p.recordPlainShadowNotReady()
		return
	}
	anchors, ok, err := derive()
	if err != nil {
		slog.Warn("pipeline: plain anchor-derivation shadow: walk failed",
			"ruleId", p.ruleID, "err", err)
		p.recordPlainShadowWalkDeclined()
		return
	}
	if !ok {
		p.recordPlainShadowWalkDeclined()
		return
	}
	if cap := p.plainDerivedAnchorCap(); len(anchors) > cap {
		// The derived set is real and correct — recorded in PlainOverCapSize,
		// never dropped — but K seeded evaluations this large would cost more
		// than the whole-corpus rescan they would replace, so it is still a
		// fallback (act mode would fall back here too; see
		// evaluatePlainNeighbourEvent's own cap check).
		slog.Warn("pipeline: plain anchor-derivation shadow: derived set exceeds the cap",
			"ruleId", p.ruleID, "derivedCount", len(anchors), "cap", cap)
		p.recordPlainShadowOverCap(len(anchors))
		return
	}
	p.recordPlainShadowAnswered(len(anchors))
}

// recordPlainShadowNotReady tallies a sampled event where plainDerivationIndex
// itself was not ready — a §4.2 conjunct refused before the walk ever ran.
func (p *Pipeline) recordPlainShadowNotReady() {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.PlainNotReady++
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// recordPlainShadowWalkDeclined tallies a sampled event where the walk itself
// declined (ok == false, including DefaultDerivationReadCap exhaustion) or
// errored.
func (p *Pipeline) recordPlainShadowWalkDeclined() {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.PlainWalkDeclined++
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// recordPlainShadowOverCap tallies a sampled event where the walk answered
// but the derived set exceeded DefaultPlainDerivedAnchorCap. derivedCount is
// added to PlainOverCapSize (never DerivedAnchors), so the two distributions
// — "sizes that fit under the cap" and "sizes that didn't" — stay separable.
func (p *Pipeline) recordPlainShadowOverCap(derivedCount int) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.PlainOverCap++
	p.derivShadow.stats.PlainOverCapSize += int64(derivedCount)
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// recordPlainShadowAnswered tallies a sampled event where the derivation
// answered within the cap, adding derivedCount to the shared DerivedAnchors
// total (DerivedAnchors / answered-count is the mean derived-set size §11's
// measurement asks for; the answered count is Sampled - PlainNotReady -
// PlainWalkDeclined - PlainOverCap).
func (p *Pipeline) recordPlainShadowAnswered(derivedCount int) {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.Sampled++
	p.derivShadow.stats.DerivedAnchors += int64(derivedCount)
	snapshot := p.derivShadow.stats
	p.derivShadow.mu.Unlock()
	p.logPlainSummaryIfDue(snapshot)
}

// logPlainSummaryIfDue emits the running plain-lens tally at the same
// interval logSummaryIfDue uses, reporting only the fields meaningful for a
// plain pipeline rather than the actor-aware fields that stay zero for one.
func (p *Pipeline) logPlainSummaryIfDue(st DerivationShadowStats) {
	if st.Sampled == 0 || st.Sampled%derivationShadowSummaryEvery != 0 {
		return
	}
	slog.Info("pipeline: plain anchor-derivation shadow tally",
		"ruleId", p.ruleID,
		"sampled", st.Sampled, "notReady", st.PlainNotReady, "walkDeclined", st.PlainWalkDeclined,
		"overCap", st.PlainOverCap, "overCapSize", st.PlainOverCapSize, "derivedAnchors", st.DerivedAnchors)
}

// noteStaticPlainDerivationRefusal logs, at most once per distinct reason,
// why this plain lens can never act — mirroring noteStaticDerivationRefusal
// (anchor_derivation_mode.go) and sharing its keyed-on-change latch
// (derivShadow.staticRefusal), safe for the same reason every other shared
// field is: a pipeline is one lens, never both.
func (p *Pipeline) noteStaticPlainDerivationRefusal(rs ruleState) {
	reason := "increment 3's licence (closure/RowReader/auditor/auth-plane) is not built by this fire — the plain derivation observes in shadow only, and never acts"
	p.derivShadow.mu.Lock()
	repeat := p.derivShadow.staticRefusal == reason
	p.derivShadow.staticRefusal = reason
	p.derivShadow.mu.Unlock()
	if repeat {
		return
	}
	slog.Info("pipeline: plain anchor derivation cannot act on this lens; using today's unseeded evaluation",
		"ruleId", p.ruleID, "reason", reason)
}
