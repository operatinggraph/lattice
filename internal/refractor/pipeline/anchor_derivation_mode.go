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
// shipped behaviour, the ActorEnumerator BFS.
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
	enumerate func() ([]string, error),
) ([]string, error) {
	mode := p.derivationMode()
	switch mode {
	case DerivationModeOff:
		return enumerate()
	case DerivationModeShadow:
		anchors, err := enumerate()
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
		return enumerate()
	}

	if _, ready := p.derivationIndexForAct(rs); !ready {
		// NOT counted as a fall-back. This refusal is a property of the LENS,
		// fixed for the life of a ruleState — a personal lens, or one whose
		// pattern the index cannot resolve, would otherwise report a fall-back
		// on every event forever and drown the ratio that makes the tally worth
		// reading. It is logged once instead, with the reason, so the lens is
		// still accounted for.
		p.noteStaticDerivationRefusal(rs)
		return enumerate()
	}
	derived, ok, err := derive()
	if err != nil {
		// A walk that errored says nothing about the event: adjacency is the
		// same store the BFS is about to read, so the honest response is to run
		// the BFS and let ITS error, if any, be the event's outcome.
		slog.Warn("pipeline: anchor derivation failed; falling back to the enumerator",
			"ruleId", p.ruleID, "eventKey", eventKey, "err", err)
		p.recordDerivationFellBack()
		return enumerate()
	}
	if !ok {
		p.recordDerivationFellBack()
		return enumerate()
	}
	p.recordDerivationActed(len(derived))
	return derived, nil
}

// noteStaticDerivationRefusal logs, at most once per distinct reason, why this
// lens can never act. Keyed on the reason rather than latched outright so a hot
// reload that changes the pattern reports the new verdict — a lens that becomes
// derivable, or stops being, is exactly what an operator wants told.
func (p *Pipeline) noteStaticDerivationRefusal(rs ruleState) {
	reason := "the lens's row depends on inputs outside its compiled pattern"
	switch {
	case !p.patternClosedOutput:
	case p.sweeper == nil:
		reason = "no convergence sweep plan is installed, so nothing would heal a missed row"
	case !rs.anchorHops.Complete:
		reason = rs.anchorHops.Incomplete
	case rs.anchorHops.UnresolvedExpansionPosition() >= 0:
		reason = fmt.Sprintf("pattern position %d carries the `*` taxonomy-expansion sigil with no resolved concrete set — the walk would prune far ends it cannot confirm, which under-approximates",
			rs.anchorHops.UnresolvedExpansionPosition())
	default:
		reason = "the anchor position's label is not the enumerator's actor type"
	}

	p.derivShadow.mu.Lock()
	repeat := p.derivShadow.staticRefusal == reason
	p.derivShadow.staticRefusal = reason
	p.derivShadow.mu.Unlock()
	if repeat {
		return
	}
	slog.Info("pipeline: anchor derivation cannot act on this lens; using the enumerator",
		"ruleId", p.ruleID, "reason", reason)
}

// derivationIndexForAct is derivationIndex plus the two conjuncts that
// distinguish "the derivation can answer" from "a smaller answer is safe on
// this lens" (§17.2). Both are act-only: shadow mode must keep observing the
// lenses acting would refuse, because how far the derivation would have missed
// on them is exactly what the observation is for.
func (p *Pipeline) derivationIndexForAct(rs ruleState) (full.HopIndex, bool) {
	// A row that depends on an input the compiled pattern does not bind can
	// change with no pattern edge changing, so a derived set that correctly
	// excludes that anchor would still skip a real change. §4.4 names the class:
	// every personal lens, which carries the D1 read gate and the Interest Set
	// outside its pattern — and projection/personal.go installs an
	// ActorEnumerator on each, so these arms are live there.
	if !p.patternClosedOutput {
		return full.HopIndex{}, false
	}
	// Acting removes the incidental reprojection that today happens to heal a
	// lost row; the convergence sweep is the standing healer that replaces it.
	// A lens with no installed plan must not lose the accident as well.
	if p.sweeper == nil {
		return full.HopIndex{}, false
	}
	return p.derivationIndex(rs)
}
