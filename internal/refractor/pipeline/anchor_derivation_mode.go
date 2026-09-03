package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// How the pattern-directed affected-anchor derivation participates in an
// actor-aware fan-out (auth-plane-projection-latency-design.md §17).
//
// The three modes are not a boolean plus an off switch. `shadow` runs the
// derivation beside the ActorEnumerator BFS and counts the difference while the
// BFS's answer decides the event; `act` lets the derivation's answer decide and
// never runs the BFS when it has one. Those spend opposite amounts: shadow pays
// for both walks to learn something, act pays for neither of the BFS's breadth.
// `off` is the third because an operator needs a way back to the shipped
// behaviour that costs nothing at all.
//
// What `off` restores is the ENUMERATOR, and since 2026-09-01 the enumerator is
// itself pattern-scoped (walkscope.go): it follows only the relations of pattern
// hops incident to a position admitting the type it is standing on. So `off`
// alone no longer reaches the relation-blind walk — REFRACTOR_WALK_SCOPE=off is
// the separate lever for that, and an operator rolling back to pre-§5.1
// behaviour needs both.
type DerivationMode int

const (
	// DerivationModeUnset means "take the package default". It is the zero value
	// deliberately: the per-pipeline override is an atomic whose unset state is
	// zero, so zero has to mean unset rather than any real mode.
	DerivationModeUnset DerivationMode = iota
	DerivationModeOff
	DerivationModeShadow
	DerivationModeAct
)

func (m DerivationMode) String() string {
	switch m {
	case DerivationModeOff:
		return "off"
	case DerivationModeShadow:
		return "shadow"
	case DerivationModeAct:
		return "act"
	default:
		return "unset"
	}
}

// ParseDerivationMode maps an operator-supplied string onto a mode. It rejects
// rather than guessing: a typo that silently resolved to `off` would disable the
// derivation on a lens whose latency someone was watching, and nothing would say
// so.
func ParseDerivationMode(s string) (DerivationMode, error) {
	switch s {
	case "off":
		return DerivationModeOff, nil
	case "shadow":
		return DerivationModeShadow, nil
	case "act":
		return DerivationModeAct, nil
	default:
		return DerivationModeUnset, fmt.Errorf("pipeline: unknown anchor-derivation mode %q (want off, shadow or act)", s)
	}
}

// defaultDerivationMode is what a pipeline uses when it has no override of its
// own. It is package-level because the operator knob is one process-wide
// decision (cmd/refractor reads REFRACTOR_ANCHOR_DERIVATION once) while
// pipelines are built in two separate places — the static rule loader and the
// dynamic lens installer — and threading a startup flag through both would
// make it possible for one of them to be missed.
var defaultDerivationMode atomic.Int64

// SetDefaultAnchorDerivationMode sets the mode every pipeline without its own
// override will use. DerivationModeUnset restores the built-in default.
func SetDefaultAnchorDerivationMode(m DerivationMode) {
	defaultDerivationMode.Store(int64(m))
}

// DefaultAnchorDerivationMode reports the mode a pipeline without its own
// override will use, resolved to a real mode rather than to Unset — so a host
// can state at boot which arm will decide its reprojections.
func DefaultAnchorDerivationMode() DerivationMode {
	if m := DerivationMode(defaultDerivationMode.Load()); m != DerivationModeUnset {
		return m
	}
	return builtinDerivationMode
}

// SetAnchorDerivationMode overrides the mode for this pipeline alone.
// DerivationModeUnset returns it to the package default.
func (p *Pipeline) SetAnchorDerivationMode(m DerivationMode) {
	p.derivMode.Store(int64(m))
}

// derivationMode resolves this pipeline's effective mode.
func (p *Pipeline) derivationMode() DerivationMode {
	if m := DerivationMode(p.derivMode.Load()); m != DerivationModeUnset {
		return m
	}
	return DefaultAnchorDerivationMode()
}

// builtinDerivationMode is `act`: the derivation has earned the decision, and
// a built-in default of `shadow` would ship the cost of the flip with none of
// its benefit.
const builtinDerivationMode = DerivationModeAct

// affectedAnchors returns the anchor keys one CDC event must reproject, and is
// the single place the two answers to that question are chosen between.
//
// derive is the pattern-directed walk; it reports ok == false when it declines,
// which is never an error — it means the shape is one the derivation cannot
// resolve and the caller must keep the shipped behaviour. enumerate is that
// shipped behaviour, the ActorEnumerator BFS, and it takes the walk's posture:
// true for the lens's own pattern-scoped walk, false for the relation-blind one.
//
// Only the SHADOW arm passes false, and it must. Shadow's whole output is the
// derived set measured against the widest answer this pipeline can give —
// NarrowedAnchors is "anchors the derivation spared" and DivergentEvents is "the
// derivation reached an anchor the trusted superset did not". Measuring against
// a walk that is itself narrowed would make the first understate the saving and
// the second fire on anchors the scope pruned rather than on a real
// disagreement. `act` and `off` both take the scoped walk, which is what the
// pipeline really runs.
//
// The invariant across every path: an event is reprojected against the BFS's
// answer unless the derivation produced one, and the derivation produces one
// only where §17.2's conjuncts hold. A derivation bug therefore degrades to
// today's breadth, never to silence.
func (p *Pipeline) affectedAnchors(
	ctx context.Context,
	rs ruleState,
	eventKey string,
	derive func() ([]string, bool, error),
	enumerate func(scoped bool) ([]string, error),
) ([]string, error) {
	mode := p.derivationMode()
	switch mode {
	case DerivationModeOff:
		return enumerate(true)
	case DerivationModeShadow:
		// The relation-blind walk, by construction — see the enumerate
		// parameter's doc above. Shadow also ACTS on this wider answer, which is
		// the safe direction and is what "the BFS's answer is what the pipeline
		// acts on, unchanged" has always meant on this arm.
		anchors, err := enumerate(false)
		if err != nil {
			return nil, err
		}
		p.shadowAnchorDerivation(rs, eventKey, anchors, derive)
		return anchors, nil
	case DerivationModeAct:
		// fall through to the act path below
	default:
		// An out-of-range mode reaches here only through the exported setters,
		// which take an unvalidated DerivationMode. Falling through to `act`
		// would resolve a garbage value to the most permissive behaviour
		// silently, so it resolves to the shipped one instead and says so.
		slog.Warn("pipeline: unknown anchor-derivation mode; using the enumerator",
			"ruleId", p.ruleID, "mode", int(mode))
		return enumerate(true)
	}

	if _, ready, licenceRefusal := p.derivationIndexForAct(rs); !ready {
		// NOT counted as a fall-back. This refusal is a property of the LENS,
		// fixed for the life of a ruleState — a lens whose pattern the index
		// cannot resolve, or one the narrowing licence refuses, would otherwise
		// report a fall-back on every event forever and drown the ratio that
		// makes the tally worth reading. It is logged once instead, with the
		// reason, so the lens is still accounted for.
		p.noteStaticDerivationRefusal(rs, licenceRefusal)
		return enumerate(true)
	}
	// The OTHER edge of the same transition. A granted licence emits nothing of
	// its own, so "the refusal is gone" — this narrowing's whole payoff claim —
	// would be provable only by the ABSENCE of a line, which reads the same as a
	// lens that stopped receiving events.
	p.notePersonalDerivationLicensed()
	derived, ok, err := derive()
	if err != nil {
		// A walk that errored says nothing about the event: adjacency is the
		// same store the BFS is about to read, so the honest response is to run
		// the BFS and let ITS error, if any, be the event's outcome.
		slog.Warn("pipeline: anchor derivation failed; falling back to the enumerator",
			"ruleId", p.ruleID, "eventKey", eventKey, "err", err)
		p.recordDerivationFellBack(p.walkIsScoped(rs))
		return enumerate(true)
	}
	if !ok {
		p.recordDerivationFellBack(p.walkIsScoped(rs))
		return enumerate(true)
	}
	p.recordDerivationActed(len(derived), p.walkIsScoped(rs))
	return derived, nil
}

// DerivationNoBranchIndexRefusal is the reason a branch-carrying lens can never
// act on a derived anchor set: the publication resolved no pattern graph for its
// walks, so there is nothing to union. Two shapes reach it — a branch that is
// not a full-engine compiled rule, and a rule state whose graph set went missing
// between publication and read — and both leave the derivation holding no index
// at all rather than an index that says no.
//
// It has a name because the empty string is not a reason: a zero HopIndex
// carries Complete false and Incomplete EMPTY, so this arm would otherwise log a
// blank reason — or, worse, be swallowed entirely by a refusal latch whose zero
// value is also the empty string, leaving the biggest personal lenses silently
// on the enumerator while the operator log read as though every personal lens
// had been licensed.
//
// It is in the derivation censuses' closed vocabulary, and no shipped lens
// reaches it: the corpus's multi-walk lenses all resolve a graph per walk. It is
// named anyway because the population is whatever carries branches, not those
// three — nothing restricts a branches spec to a generated personal lens.
const DerivationNoBranchIndexRefusal = "the lens compiles to several walks and the derivation holds no per-branch index"

// noteStaticDerivationRefusal logs, at most once per distinct reason, why this
// lens can never act. Keyed on the reason rather than latched outright so a hot
// reload that changes the pattern reports the new verdict — a lens that becomes
// derivable, or stops being, is exactly what an operator wants told.
//
// licenceRefusal is the personal narrowing licence's own reason, handed down
// from derivationIndexForAct rather than recomputed here: recomputing it would
// run a second verdict snapshot and a second walk of the compiled rule's
// parameters on every CDC event of every refused personal lens, which is fifteen
// lenses on a shipped stack.
//
// It is reported ONLY for a lens the host declared personal. A licence asked of
// any other lens refuses at its class conjunct, and printing "it is not a
// personal lens" for a plain or actor-aggregate lens whose real refusal is that
// its row depends on an unbound input would send an operator somewhere there is
// nothing to fix.
func (p *Pipeline) noteStaticDerivationRefusal(rs ruleState, licenceRefusal string) {
	reason := "the lens's row depends on inputs outside its compiled pattern"
	switch {
	case licenceRefusal != "" && p.assertedPersonalLens():
		reason = licenceRefusal
	case !p.patternClosedOutput && !p.assertedPersonalLens():
	case !p.standingHealerInstalled():
		reason = "no standing healer is installed, so nothing would heal a missed row"
	default:
		// The index's own conjunct, from the SAME predicate derivationIndexes
		// gated on, so what an operator reads is what actually refused. This arm
		// is reached only once the licence and the healer have cleared, which
		// leaves the pattern as the only thing that can have refused — but the
		// empty string is not a reason under any argument (the latch's own zero
		// value is ""), so it is named rather than trusted to be impossible.
		reason = p.derivationIndexRefusal(rs)
		if reason == "" {
			reason = derivationUnnamedIndexRefusal
		}
	}

	p.derivShadow.mu.Lock()
	// The latch compares against what was last REPORTED, and its zero value is
	// the empty string — so a reason that ever came out empty would read as a
	// repeat of a report that never happened and be swallowed on its first and
	// every subsequent occurrence. That is how the multi-walk arm's blank reason
	// stayed invisible: it was not a badly-worded line, it was no line.
	//
	// staticRefusalSet is DEFENCE FOR A FUTURE ARM, and is unreachable today —
	// no arm of the switch above can now yield an empty reason, which
	// TestStaticDerivationRefusal_EveryArmNamesItself sweeps and pins. It is
	// kept because the class is what bit, not the instance: the next conjunct
	// added without a name logs a blank line, which is visible and fixable,
	// rather than nothing at all.
	repeat := p.derivShadow.staticRefusalSet && p.derivShadow.staticRefusal == reason
	p.derivShadow.staticRefusal = reason
	p.derivShadow.staticRefusalSet = true
	p.clearPersonalLicensedLatchLocked()
	p.derivShadow.mu.Unlock()
	if repeat {
		return
	}
	slog.Info("pipeline: anchor derivation cannot act on this lens; using the enumerator",
		"ruleId", p.ruleID, "reason", reason)
}

// derivationIndexForAct is derivationIndexes plus the two conjuncts that
// distinguish "the derivation can answer" from "a smaller answer is safe on
// this lens" (§17.2). Both are act-only: shadow mode must keep observing the
// lenses acting would refuse, because how far the derivation would have missed
// on them is exactly what the observation is for.
//
// licenceRefusal carries the personal licence's own reason out to the refusal
// note, for the reason plainDerivationIndexForAct threads its own: recomputing
// it there would repeat a verdict snapshot and a compiled-rule walk per event.
// It is empty whenever the licence had nothing to say — the lens is pattern-
// closed and was never asked, or it was asked and admitted.
func (p *Pipeline) derivationIndexForAct(rs ruleState) (idxs []full.HopIndex, ready bool, licenceRefusal string) {
	// Acting removes the incidental reprojection that today happens to heal a
	// lost row; a standing healer is what replaces it. Two count, one per plane
	// — the auth/business convergence sweep and the personal plane's own
	// PersonalSweeper plus the D1 edge — and standingHealerInstalled is where
	// both are read. Reading p.sweeper alone here made this the one consumer of
	// that question that could never see the personal arm.
	//
	// Asked FIRST because it is two field reads and holds for the life of the
	// install: a lens with no healer is refused whatever its pattern or its
	// licence are doing.
	if !p.standingHealerInstalled() {
		return nil, false, ""
	}
	// The INDEX before the LICENCE, mirroring plainDerivationIndexForAct and for
	// the same two reasons it gives. The licence is far the dearer of the two —
	// four mutex-guarded process censuses, a verdict snapshot and a clock read —
	// while the index is a handful of field reads off the published rule state.
	// And a lens whose index refuses has no derived set for a licence to speak
	// about: a lens whose pattern cannot answer would otherwise pay the whole
	// predicate on every CDC event to be refused on its graph anyway.
	//
	// Ordering it this way is also what makes the operator log right: an index
	// refusal is reported as the conjunct of the lens's own pattern that
	// declined, not as whichever licence conjunct happened to be evaluated first
	// and masked it.
	idxs, ready = p.derivationIndexes(rs)
	if !ready {
		return nil, false, ""
	}
	// A row that depends on an input the compiled pattern does not bind can
	// change with no pattern edge changing, so a derived set that correctly
	// excludes that anchor would still skip a real change. §4.4 names the class:
	// every personal lens, which carries the D1 read gate and the Interest Set
	// outside its pattern — and projection/personal.go installs an
	// ActorEnumerator on each, so these arms are live there.
	//
	// The personal licence is the exception the class earned: a personal lens
	// whose two out-of-pattern inputs both have change edges, whose plane has a
	// standing healer reporting a clean recent verdict, and whose deployment
	// cannot have split producer from consumer, has nothing left outside its
	// pattern that no mechanism announces. patternClosedOutput itself stays
	// FALSE for such a lens — it is a claim about the lens read by two
	// predicates with different tolerances and different rollback shapes, and
	// this narrowing is only entitled to change one of them.
	if !p.patternClosedOutput {
		licensed, refusal := p.personalDerivationLicence(rs)
		if !licensed {
			return nil, false, refusal
		}
	}
	return idxs, true, ""
}
