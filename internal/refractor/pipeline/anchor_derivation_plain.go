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
// The narrowing LICENCE (§5: per-anchor closure, an Auditor that is enrolled,
// unsuppressed and not stale, no $now/$projectedAt, no secure decryptor, the
// auth-plane exclusion) lives here too, as plainDerivationLicence, and
// plainDerivationIndexForAct is what consults it: in `act` mode a derived
// anchor set decides a plain lens's neighbour event only on a lens the licence
// admits, and every lens it refuses keeps today's whole-corpus rescan. Both
// arms run through the SAME three-way mode switch and the SAME derivationShadow
// counters the actor-aware arm's affectedAnchors already uses
// (anchor_derivation_mode.go, anchor_derivation_shadow.go). A pipeline is one
// lens and is either plain or actor-anchored, never both, so sharing that
// counter state introduces no new state and cannot let the two measurements
// collide.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
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

// plainDerivationLicence reports whether this plain lens may let a derived
// anchor set DECIDE a neighbour event's outcome — §5 of the design — and, when
// it may not, the reason. Every conjunct is fail-closed, and the refusal is
// returned rather than logged here so the caller decides whether it is a
// once-per-lens note or a per-event fact.
//
// It is evaluated per event off LIVE pipeline fields rather than snapshotted at
// install, for the reason seedAnchorFor and actorAwareNarrowingLabels already
// state (pipeline.go): activation installs components in stages, so a snapshot
// taken during installation reads a later stage's component as absent — and for
// a licence, absent reads as satisfied, which is the fail-open direction.
//
// The conjuncts, in the order the body asks them (auditEnrolment's own ordering
// discipline — the field reads before the two expression walks):
//
//   - not auth-plane. An auth-plane lens projects an authorization surface, so
//     a stale row is an over-grant in one direction or the other. The
//     actor-aware precedent required a repair-capable healer proven end to end
//     before narrowing that plane; this mechanism gives detection only, so the
//     plane is excluded. p.authPlane is the activation path's record of
//     projection.IsAuthPlane (cmd/refractor's startPipeline), which is what
//     makes this conjunct able to fire on a plain pipeline at all.
//   - an enrolled Auditor that is RUNNING. The ratified actor-aware licence
//     requires a Sweeper — "something standing will re-test this row". For a
//     plain lens the only standing thing that can is the divergence Auditor, so
//     that is the conjunct: after narrowing, a diverged row is detected, named
//     and alarmed rather than maybe-silently rewritten by the next neighbour
//     event. Enrolled alone is not enough, and reading only it would be
//     fail-open: it is the INSTALL-TIME verdict, fixed by InstallAudit and never
//     revised, while every pass re-runs the enrolment conjuncts and the pause /
//     rebuild checks (Auditor.suppressed) and publishes the outcome as
//     AuditStatus.Suppression. A lens whose audit is suppressed indefinitely —
//     an operator pause, a hot-reload that moved its anchor to an expanded set,
//     the deployment kill switch thrown after activation — still reads
//     Enrolled, and nothing is re-testing its rows. So both are read, from ONE
//     status snapshot: no auditor at all, a refused one, and a suppressed one
//     all refuse. Suppression is "" before the first pass, which is the honest
//     reading of a freshly activated auditor: enrolled, running, nothing
//     reported.
//   - an audit whose last verdict is RECENT — Auditor.Stale, i.e. less than
//     auditorStaleCycles of the audit's own Interval since AuditStatus.LastPassAt.
//     Enrolled and Suppression together still leave one fail-open state, and it is
//     the worst one: both fields are written BY the tick loop, so a loop that has
//     stopped running at all — crashed, wedged, blocked forever inside a pass —
//     leaves Enrolled true and Suppression empty for the life of the process. A
//     licence reading only those two would take a dead audit for a healthy quiet
//     one indefinitely, and narrow every write behind it. LastPassAt is the one
//     field that ages without anyone writing it, so it is the one that can answer
//     "is something re-testing this lens NOW" rather than "was something once".
//     The window is scaled off the lens's own cadence rather than a second
//     duration, and a zero LastPassAt — an auditor that has never completed a pass
//     — reads as stale, which is the correct fail-closed answer for a write
//     licence: not yet proven is not licensed. That is deliberately the OPPOSITE
//     disposition to the heartbeat's own audit-stall detector, which must not
//     alarm on a freshly activated lens and so rebases its clock at first sight;
//     the two mechanisms share no state and no constant, only a default value
//     chosen to keep them legible together (see auditorStaleCycles).
//   - a full-engine compiled rule, without which neither closure nor the
//     parameter walk can be asked at all.
//   - a target that can read a row back (adapter.RowReader). Required twice
//     over: the Auditor's own enrolment needs it, and so does the zero-row
//     Delete probe the derived retraction class rests on (§6) — so it is a
//     conjunct of the mechanism, not merely inherited from enrolment.
//   - no secure decryptor. A Secure Lens's columns are decrypted before results
//     reach any write path, and evaluatePlainDerivedAnchors re-enters an
//     evaluation path that decrypts on its own results while itself running
//     inside the outer wrapper that decrypts again — this conjunct is what
//     keeps that double-decrypt unreachable (see that function's own note).
//   - no $now / $projectedAt. $now is wall-clock and $projectedAt derives from
//     the EVENT vertex's provenance, so a per-anchor re-evaluation produces a
//     different value for them than the whole-corpus rescan it replaces. A
//     non-exhaustive walk is a REFUSAL, never a pass: (referenced=false,
//     exhaustive=false) means the accessor could not rule the parameter out,
//     and reading that as absence is the read-the-declaration-not-the-matcher
//     mistake the exhaustive flag exists to prevent.
//   - per-anchor closure (§5.1): the lens's rows PARTITION by anchor,
//     full.CompiledRule.ProjectsOneRowPerAnchor — every key column resolves from
//     the anchor's binding alone AND one of them identifies WHICH anchor the row
//     is for. This is the conjunct the audit's read-only enrolment does NOT have
//     and a WRITE licence cannot do without: a lens grouping on a non-root
//     variable, or on a key several anchors share, computes a TRUNCATED row
//     under a seeded evaluation. Both halves are load-bearing — a literal key
//     column is anchor-only by vacuity, so the first half alone would license a
//     `RETURN 'all' AS key, collect(...)` lens whose every row aggregates across
//     roots. It is deliberately sufficient rather than necessary: a
//     neighbour-keyed lens whose rows happen to be partitionable by anchor is
//     refused too, and keeps today's behaviour.
//
// The conjuncts the audit shares are asked in auditEnrolment's own order, so a
// lens the audit also refuses reports the same reason both predicates publish.
// The conjuncts with no counterpart there sit outside that ordering, at the two
// ends and for opposite reasons. The auth plane is asked FIRST because it is a
// single bool and because it is the one exclusion no other conjunct can stand in
// for — a lens on that plane is refused whatever its audit is doing, and reading
// any other reason for it would misdirect an operator. The auditor's own health
// — enrolled, unsuppressed, not stale — follows, ahead of everything derived from
// the rule, because it is the next-cheapest (field reads off one status snapshot
// and a clock) and is the conjunct most likely to MOVE under a lens that is
// otherwise permanently eligible. Closure is asked LAST because it is a fixed
// property of the query: reporting "not keyed by its anchor alone" only for a
// lens that is otherwise fully eligible is the reading an operator can act on.
//
// The §4.2 conjuncts (plain pipeline, single branch, complete and resolved
// rootHops, no diffRetraction) are plainDerivationIndex's, not restated here:
// this predicate answers "may a derived set be acted on", the index answers
// "is there a derived set at all".
func (p *Pipeline) plainDerivationLicence(rs ruleState) (licensed bool, refusal string) {
	if p.authPlane {
		return false, "it projects onto the auth plane, which narrows only behind a repair-capable healer proven end to end"
	}
	auditor := p.Auditor()
	if auditor == nil {
		return false, "no divergence audit is enrolled on it, so nothing standing would re-test a row a narrowed reprojection left behind"
	}
	st := auditor.Status()
	switch {
	case !st.Enrolled:
		reason := "no divergence audit is enrolled on it, so nothing standing would re-test a row a narrowed reprojection left behind"
		if st.Refusal != "" {
			reason += " (" + st.Refusal + ")"
		}
		return false, reason
	case st.Suppression != "":
		return false, "its divergence audit is suppressed (" + st.Suppression + "), so nothing is re-testing its rows while that holds"
	}
	// Every refusal string below is STABLE for as long as the state producing it
	// holds — no elapsed duration is interpolated into one. The rule is stated for
	// what follows because the two arms above interpolate the AUDITOR's own
	// published reason (st.Refusal, st.Suppression) rather than a string this
	// function composes: those are only as stable as the auditor makes them, and a
	// suppression reason carrying a live err.Error() is the auditor's to keep
	// steady (audit.go's noteSuppressed). The caller latches on
	// the reason string to log a refusal at most once (noteStaticPlainDerivationRefusal),
	// and a staleness window lasts hours, so a reason carrying a per-second
	// elapsed would defeat that latch precisely where it matters most and emit a
	// line per neighbour event. The duration is the LOG's to carry as its own
	// field, never the reason's. A never-passed auditor gets its own sentence
	// rather than an elapsed measured from the zero time, which is not a duration
	// anyone can read.
	if stale, _ := auditor.Stale(time.Now()); stale {
		if st.LastPassAt.IsZero() {
			return false, "its divergence audit has not reached a verdict since it was installed, and an audit that has proven nothing yet licenses nothing yet"
		}
		return false, fmt.Sprintf("its divergence audit has not reached a verdict in %d of its own cadence intervals, so nothing is proven to be re-testing its rows",
			auditorStaleCycles)
	}
	fullCR, isFull := rs.cr.(*full.CompiledRule)
	if !isFull || fullCR == nil {
		return false, "its compiled rule is not a full-engine rule, so its closure and parameters cannot be derived"
	}
	if _, ok := p.currentAdapter().(adapter.RowReader); !ok {
		return false, "its target adapter cannot read a row back, which both the audit and the derived path's own presence probe require"
	}
	if p.secureDecryptor != nil {
		return false, "it is a Secure Lens, whose columns a per-anchor re-entry would decrypt twice over"
	}
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		if !exhaustive {
			return false, "its query shape could not be proven free of $" + param + ", which a per-anchor evaluation reproduces differently"
		}
		if referenced {
			return false, "it returns $" + param + ", which a per-anchor evaluation reproduces differently from the whole-corpus rescan it replaces"
		}
	}
	if !fullCR.ProjectsOneRowPerAnchor() {
		return false, "its rows do not partition by anchor (no key column both resolves from the anchor alone and identifies it), so a per-anchor evaluation would compute a truncated row"
	}
	return true, ""
}

// plainDerivationIndexForAct is the gate `act` mode consults before letting a
// derived anchor set decide a plain lens's neighbour event. It carries both
// halves of the question: plainDerivationIndex answers "is there a derived set
// at all" (§4.2), plainDerivationLicence answers "may a derived set be acted
// on" (§5). Either refusing keeps today's unseeded whole-corpus rescan, so a
// lens is narrowed only where both hold.
//
// The licence is asked SECOND, and only once the index is ready, for two
// reasons: it is the dearer of the two — one auditor-status snapshot, a clock
// read, and two walks of the compiled rule (its parameters and its key columns)
// — and a lens whose index refuses has no derived set for a licence to speak
// about.
//
// `shadow` mode never reaches here: shadowPlainDerivation asks
// plainDerivationIndex directly, deliberately without the licence, because how
// far the derivation WOULD have narrowed on a lens acting refuses is exactly
// what the measurement is for.
//
// licenceRefusal is the licence's own reason, handed back rather than discarded
// so the caller's refusal note does not have to re-derive it: recomputing it
// would run a second auditor snapshot and a second pair of compiled-rule walks
// (ReferencesParam twice, ProjectsOneRowPerAnchor) on EVERY neighbour event of
// every lens without an enrolled auditor — which today is most of the corpus. It
// is empty in both of the cases where the licence has nothing to say: the index
// refused first, so the licence was never asked, and the lens was admitted.
func (p *Pipeline) plainDerivationIndexForAct(rs ruleState) (idx full.HopIndex, ready bool, licenceRefusal string) {
	idx, ready = p.plainDerivationIndex(rs)
	if !ready {
		return full.HopIndex{}, false, ""
	}
	licensed, refusal := p.plainDerivationLicence(rs)
	if !licensed {
		return full.HopIndex{}, false, refusal
	}
	return idx, true, ""
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
// with zero adjacency reads (§4.2's worked payoff trace).
//
// It takes a LINK KEY, and no live path holds one by the time derivation is
// asked. evalPlainLinkReprojection splits a link event into its two endpoint
// vertices and evaluates each through evaluatePlainFromVertex, so what reaches
// the derivation seam is already "evaluate from this one vertex" and is
// indistinguishable from a genuine vertex-root event —
// deriveAnchorsForPlainVertex serves every plain neighbour event, link endpoints
// included. This function is the whole-link entry point that answer costs two
// endpoint evaluations to reach: a caller that kept the link key could seed the
// anchor side once and skip the neighbour endpoint's redundant evaluation
// entirely (§4.2's own payoff trace), and it is exercised by the derivation's
// unit and differential tests against that shape.
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
// and returns the combined, deduplicated result set. It is what `act` mode
// substitutes for the unseeded whole-corpus rescan on a licensed lens.
//
// THE ZERO-ROW DELETE PROBE (§6). Each re-entry reaches evaluateForEntryRaw's
// filter-retraction presence check (evaluate.go), which emits a Delete for an
// anchor whose re-derived row set no longer contains it. For a genuine
// anchor-typed event that is exactly right. For a WALK-derived anchor it is
// not: walkToAnchors models the pattern's hops, never its WHERE, so it can
// derive an anchor whose row the lens has never once projected — and an
// unconditional Delete for one is a spurious tombstone, durable under
// soft-delete. So every derived anchor's Delete is asked of the target first
// (derivedRowIsLive) and dropped when the row is CONFIRMED absent. Dropping on
// that answer is silent rather than an error: absence is the answer, not a
// failure.
//
// A probe the target could not ANSWER is the opposite disposition, and the
// difference is the event's second chance. This probe fires on a non-anchor-
// incident neighbour event — for the derived anchor it names, a one-shot event
// that will never recur — so a Delete dropped here has nothing behind it: the
// row becomes an orphan only the detect-only audit will ever name, and only if
// the audit is not itself blind to the same outage. So an unreadable probe ends
// the WHOLE event with its error, exactly as derive()'s own failed walk hands
// the event's outcome to the call it falls back to (evaluatePlainNeighbourEvent),
// and exactly as the upsert half of this same evaluation already behaves on a
// write failure. evaluateForEntryRaw propagates it (evaluate.go) and
// dispositionEvalErr Naks it (pipeline.go), so the event is redelivered rather
// than acked with its retraction silently missing.
//
// Discarding the results already collected from earlier anchors is correct, not
// a loss: redelivery re-runs the whole evaluation from scratch — every derived
// anchor's seeded evaluation again — and each of those is idempotent (an upsert,
// or the probed Delete). Returning a partial set would be the lossy option,
// because it would ack.
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
// THE DOUBLE-DECRYPT SEAM: evaluatePlainFromVertex calls the OUTER
// evaluateForEntry wrapper, which runs applySecureDecrypt on its own results.
// Reached from evaluateForEntryRaw's neighbour-event path — itself inside
// that same outer wrapper — a Secure Lens's columns would be decrypted once
// per derived anchor here AND AGAIN by the outer wrapper on return. What
// holds it shut is plainDerivationLicence's refusal of any pipeline carrying
// a secureDecryptor, which is the ONLY thing applySecureDecrypt acts on (it
// returns immediately when the field is nil, evaluate.go) — so on every
// pipeline the act gate admits, both invocations are no-ops rather than a
// decrypt and a re-decrypt. Any change that makes the licence's Secure
// conjunct advisory re-opens this seam.
func (p *Pipeline) evaluatePlainDerivedAnchors(ctx context.Context, rs ruleState, anchors []string, anchorLabel string) ([]ruleengine.EvalResult, error) {
	var combined []ruleengine.EvalResult
	seen := make(map[string]bool, len(anchors))
	for _, anchorKey := range anchors {
		results, err := p.evaluatePlainFromVertex(ctx, rs, anchorKey, anchorLabel)
		if err != nil {
			return nil, err
		}
		for _, r := range results {
			if r.Delete {
				live, perr := p.derivedRowIsLive(ctx, r.Keys)
				if perr != nil {
					return nil, perr
				}
				if !live {
					continue
				}
			}
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

// derivedRowIsLive reports whether the target presently holds the row keys
// names — the §6 presence probe a walk-derived anchor's Delete must pass
// before it is written.
//
// Only a positively confirmed live row earns the Delete, so the two answers
// that are FACTS ABOUT THE ROW — a confirmed absence, and an adapter that
// cannot be asked at all — return (false, nil) and the caller drops the Delete
// silently, mirroring zeroRowDeleteKey's own disposition (evaluate.go). The
// second is unreachable on a licensed lens (plainDerivationLicence requires
// adapter.RowReader, and this path runs only behind that licence) but is still
// answered fail-safe rather than asserted away.
//
// The FAILED READ is neither, and it is why this returns an error at all. It is
// not a fact about the row: the probe could not tell whether the retraction it
// is gating is a real one, and dropping it would ack an event that will never
// come back for this anchor (see evaluatePlainDerivedAnchors on why the
// zeroRowDeleteKey precedent stops here — that one fires on an evaluation the
// actor gets again). So it is returned for the caller to make the event's
// outcome, and it is ALSO logged and counted (PlainProbeUnreadable): the
// telemetry says a target had a bad minute, which no amount of redelivery makes
// visible on its own.
func (p *Pipeline) derivedRowIsLive(ctx context.Context, keys map[string]any) (bool, error) {
	reader, ok := p.currentAdapter().(adapter.RowReader)
	if !ok {
		return false, nil
	}
	_, present, err := reader.GetRow(ctx, keys)
	if err != nil {
		slog.Warn("pipeline: plain derived anchor: could not read the row back; failing the event rather than dropping its retraction",
			"ruleId", p.ruleID, "keys", keys, "err", err)
		p.recordPlainProbeUnreadable()
		return false, fmt.Errorf("plain derived anchor: presence probe could not read the row back: %w", err)
	}
	return present, nil
}

// recordPlainProbeUnreadable tallies a §6 presence probe the target could not
// answer, which fails its event rather than deciding a retraction.
func (p *Pipeline) recordPlainProbeUnreadable() {
	p.derivShadow.mu.Lock()
	p.derivShadow.stats.PlainProbeUnreadable++
	p.derivShadow.mu.Unlock()
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
// `off` and `shadow` both return EXACTLY what calling executeFullForActor with
// an empty seed returns — the shadow's measurement decides nothing about the
// event it observes. Only `act`, and only on a lens plainDerivationIndexForAct
// admits, substitutes the K seeded per-anchor evaluations for that one rescan;
// every refusal below it falls back to the same unseeded call.
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

	idx, ready, licenceRefusal := p.plainDerivationIndexForAct(rs)
	if !ready {
		// NOT counted as a fall-back, mirroring affectedAnchors' own static-
		// refusal treatment: this refusal is a property of the LENS, not of the
		// event, and counting it every event would drown the ratio the tally
		// exists to report — the per-event walk failures and cap overflows.
		p.noteStaticPlainDerivationRefusal(rs, licenceRefusal)
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

// noteStaticPlainDerivationRefusal logs, at most once per distinct reason, why
// plainDerivationIndexForAct will not let this plain lens act — mirroring
// noteStaticDerivationRefusal (anchor_derivation_mode.go) and sharing its
// keyed-on-change latch (derivShadow.staticRefusal), safe for the same reason
// every other shared field is: a pipeline is one lens, never both.
//
// The gate's index half is re-asked here one conjunct at a time, in the gate's own
// order, so the reason names the conjunct that actually governs. The licence half is
// not re-asked: licenceRefusal is what plainDerivationIndexForAct already computed
// and handed back, and it is non-empty exactly when the licence is what refused.
//
// The licence's refusals sit in the SAME once-per-reason, uncounted bucket as the
// index's, even though its auditor-health conjuncts are live per-event facts rather
// than fixed properties of the lens. An audit that is stale or suppressed stays that
// way for hours, and counting every event under one through recordDerivationFellBack
// would drown the ratio that tally exists to report with a repeated lens-level fact —
// the same reason noteStaticDerivationRefusal's own doc gives. Keying the latch on the
// reason STRING rather than latching outright is what keeps this honest: the moment
// the governing conjunct changes, the new verdict is logged. It is also why every
// reason here is stable while its cause holds — see plainDerivationLicence, which
// keeps varying quantities out of its refusals for this reason.
//
// The audit's last verdict rides along as its own log field rather than inside the
// reason, so an operator reading a refusal can see how old the lens's audit is
// without that timestamp making every event's reason a new one.
func (p *Pipeline) noteStaticPlainDerivationRefusal(rs ruleState, licenceRefusal string) {
	var reason string
	switch {
	case p.actorEnumerator != nil || p.envelopeFn != nil || p.multiEnvelopeFn != nil:
		reason = "it is actor-aggregate or personal, so its anchor is the actor $actorKey names rather than a vertex the walk could seed from"
	case len(rs.branches) > 1:
		reason = "it is a multi-walk lens, and one scan-root graph cannot speak for N independent queries"
	case !rs.rootHops.Complete:
		reason = rs.rootHops.Incomplete
	case rs.rootHops.UnresolvedExpansionPosition() >= 0:
		reason = fmt.Sprintf("pattern position %d carries the `*` taxonomy-expansion sigil with no resolved concrete set — the walk would prune far ends it cannot confirm, which under-approximates",
			rs.rootHops.UnresolvedExpansionPosition())
	case p.diffRetraction:
		reason = "it uses target-diff retraction, which would read a per-anchor row set as every OTHER anchor's rows being gone"
	case licenceRefusal != "":
		reason = licenceRefusal
	default:
		// Reachable only through the INDEX half, never the licence's. Every
		// plainDerivationLicence refusal returns a non-empty reason, so an empty
		// licenceRefusal on a gate that answered "not ready" means the index
		// refused first and the licence was never asked. The index conjuncts read
		// off the ruleState (branches, rootHops) cannot have moved — it is the
		// same value the gate was handed — so what moved is one of the ones read
		// live off the pipeline: its actor/envelope shape, or target-diff
		// retraction.
		reason = "an index conjunct it reads live — its actor or envelope shape, or target-diff retraction — moved between the act gate's answer and this one"
	}

	p.derivShadow.mu.Lock()
	repeat := p.derivShadow.staticRefusal == reason
	p.derivShadow.staticRefusal = reason
	p.derivShadow.mu.Unlock()
	if repeat {
		return
	}
	attrs := []any{"ruleId", p.ruleID, "reason", reason}
	if a := p.Auditor(); a != nil {
		if last := a.Status().LastPassAt; !last.IsZero() {
			attrs = append(attrs, "lastAuditVerdictAt", last)
		}
	}
	slog.Info("pipeline: plain anchor derivation cannot act on this lens; using today's unseeded evaluation", attrs...)
}
