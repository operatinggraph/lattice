// The PERSONAL plane's narrowing licence
// (personal-lens-derivation-licence-design.md §4.4).
//
// A personal lens's published row is not a function of its compiled pattern
// alone. Two inputs sit outside it — D1's `cap-read` read gate and the
// per-device Interest Set — and both are read live inside personalEnvelopeFn,
// written by other pipelines and other planes, and reach this lens through no
// CDC event of its own. That is why derivationIndexForAct refused every
// personal lens outright: a derived anchor set that correctly excludes an
// anchor whose PATTERN did not change still skips a row whose GRANT did.
//
// The refusal was correct while those inputs were silent. It stops being
// correct once each of them has a change edge, once something standing
// re-asks both gates on a schedule, and once the deployment cannot have put
// the producer and the consumer in two different processes. This file is
// where those become conjuncts instead of prose.
//
// Two halves, the same split the plain arm takes (anchor_derivation_plain.go):
// derivationIndex answers "can the derivation ANSWER for this lens", and the
// licence answers "is a SMALLER answer safe on this lens". Either refusing
// keeps the shipped ActorEnumerator walk, so a personal lens is narrowed only
// where both hold.
//
// WHERE EACH CONJUNCT COMES FROM. Conjuncts 0–2 are about the HOST's wiring,
// which InstallPersonalLens cannot see and this package cannot derive: whether
// a capability-KV handle was threaded, whether a reprojector exists in this
// process, whether every cap-read producer installed here got a sink, whether
// the Interest Set's writers announce. cmd/refractor knows all four at the
// registration call and asserts them there, exactly as it already asserts the
// standing healer — and the zero value of the assertion is REFUSAL, so a host
// that says nothing narrows nothing. Conjuncts 3–5 are read LIVE, through a
// one-method accessor the host injects, because they move: the healer reaches
// a verdict, goes stale, and reaches another. They are deliberately not
// snapshotted onto ruleState — both halves of the wiring are installed AFTER
// useFullEngineBranches publishes the rule, which is the same reason
// standingHealerInstalled is read live (walkscope.go).
//
// WHY THE VERDICT IS A VERDICT. An earlier draft read the sweeper's progress
// STAMP. publishProgress ran unconditionally after the batch loop and
// reprojectActor logged-and-continued on a per-lens failure, so a Capability-KV
// outage that failed every reprojection of every actor still advanced the stamp
// every 60 s — a predicate reading healthy through the very condition it exists
// to detect. The healer therefore reports what its pass actually achieved, and
// the licence refuses on never-passed, on any failure, on a population it could
// not enumerate, and on staleness.
package pipeline

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// PersonalHealerStaleCycles is how many of the personal sweeper's own
// intervals may elapse between pass verdicts before the licence treats the
// standing healer as no longer standing.
//
// It is the personal plane's counterpart of the plain arm's auditorStaleCycles
// and it is pinned against health.DefaultCapabilitySweepStallCycles by a
// cross-package test, the way pipeline.IdleSweepBackoffEvery is
// (health/idle_sweep_backoff_test.go). Two bounds, in opposite directions:
//
//   - it must be comfortably ABOVE one, or ordinary tick jitter — a pass that
//     runs a second late behind a slow Core-KV listing — revokes the narrowing
//     on fifteen lenses at once for no reason;
//   - it must sit well INSIDE the platform's own definition of a stalled sweep,
//     so the narrowing is already gone by the time an operator is told the
//     healer stopped. A licence that outlives the alert about its own healer is
//     a licence nobody is holding to account.
const PersonalHealerStaleCycles = 5

// PersonalHealerVerdict is one pass of the personal plane's standing healer,
// reported as what that pass ACHIEVED rather than as the fact that it ran.
//
// grantchange.PersonalSweeper produces it and this package consumes it; the
// type is declared HERE because pipeline cannot import grantchange (grantchange
// reaches pipeline through projection), so the value has to be shaped by the
// consumer and filled in by the producer.
//
// The zero value is a refusal in every direction that matters: CompletedAt is
// zero (never passed), PopulationReadable is false (nothing enumerated), and
// InstanceCountReadable is false (the deployment's cardinality is unknown).
type PersonalHealerVerdict struct {
	// StartedAt and CompletedAt bracket the pass this verdict describes. Zero
	// CompletedAt means no pass has finished in this process — the state every
	// restart starts in, and the reason the sweeper runs one pass immediately
	// rather than waiting out its first tick.
	//
	// StartedAt is not decoration: a lens that registers into an ALREADY-SWEPT
	// plane would otherwise inherit a clean verdict from a pass that never drove
	// it, and the licence would narrow it on a healer that has demonstrably not
	// covered it once. The licence compares StartedAt against the lens's own
	// registration time, so every lens earns its own first pass.
	StartedAt   time.Time
	CompletedAt time.Time
	// Interval is the healer's own cadence, carried on the verdict so the
	// staleness window is measured against the clock the healer actually runs
	// on rather than against a constant this package would have to keep in step
	// with a bound the deployment can override.
	Interval time.Duration
	// Attempted and Failed count the actor-reprojections of that pass. Failed
	// is what makes this a verdict: a pass in which every reprojection failed
	// healed nothing, and the licence must read that as "no standing healer",
	// not as "the healer ran".
	Attempted int
	Failed    int
	// PopulationReadable is false when the pass could not enumerate the
	// identity population at all. A healer that cannot see its own population
	// is not covering it, whatever it did to the actors it did see.
	PopulationReadable bool
	// InstanceCount is how many Refractor instances the pass found live in
	// Health KV, and InstanceCountReadable is false when it could not tell.
	// Read once per pass, off the healer's clock — never per event: the
	// question is a property of the deployment, and asking it per CDC event
	// would put a KV listing on the path this whole design exists to shorten.
	InstanceCount         int
	InstanceCountReadable bool
	// EdgeSpansDeployment mirrors grantchange's own declaration about its
	// change edge. While the edge is an in-process function call the instance
	// count is a real conjunct; once a durable signal replaces it the count
	// stops being the question, and this is the one field that says so.
	EdgeSpansDeployment bool
}

// The verdict's own summary vocabulary, published on every personal lens's
// health entry and closed so that an operator reading one, a test pinning one,
// and the licence refusing on one are all naming the same set.
const (
	PersonalHealerVerdictClean                = "clean"
	PersonalHealerVerdictNeverPassed          = "never-passed"
	PersonalHealerVerdictFailed               = "failed"
	PersonalHealerVerdictPopulationUnreadable = "population-unreadable"
	PersonalHealerVerdictInstancesUnreadable  = "instance-count-unreadable"
	// PersonalHealerVerdictInstancesImpossible is a count of ZERO read back as
	// readable. It is a distinct token from "unreadable" because the cause is
	// different and so is what an operator does about it: zero is SELF-REFUTING
	// — the process performing the census is itself a live Refractor — so a
	// readable zero means the census is broken (the bucket purged or
	// re-provisioned under a running Refractor, heartbeat writes failing while
	// listings succeed, a permission change, a key-shape drift), not that the
	// deployment is empty.
	PersonalHealerVerdictInstancesImpossible = "instance-count-impossible"
	PersonalHealerVerdictMultipleInstances   = "multiple-instances"
	// PersonalHealerVerdictStale is never written by the sweeper — a stalled
	// sweeper writes nothing at all, which is precisely the state it names. It
	// is written by the ONE other periodic per-lens writer (health.LagPoller),
	// which runs on its own clock and escalates the stored token when the
	// healer's last pass has aged past the licence's window. See
	// health.PersonalSweepVerdictOwnership for the field's two-writer rule.
	PersonalHealerVerdictStale = "stale"
)

// Summary reduces the verdict to the single token published on health, in the
// order the licence asks the conjuncts this verdict can speak for.
//
// WHAT IT IS NOT: the reason a particular lens is refused. One shared sweeper
// fans one pass verdict onto every personal lens, so this token is PLANE-WIDE —
// it says what the healer achieved and how many Refractors are live, and nothing
// at all about conjuncts 0-2 (the host's wiring) or about conjunct 3's per-lens
// "a pass begun after this lens registered" clause. A lens can read `clean` here
// and still be refused. The per-lens answer is the control plane's `health` op,
// which calls Pipeline.PersonalDerivationLicence live and returns the conjunct
// by name (controlwire.PersonalDerivationStatus).
func (v PersonalHealerVerdict) Summary() string {
	switch {
	case v.CompletedAt.IsZero():
		return PersonalHealerVerdictNeverPassed
	case !v.PopulationReadable:
		return PersonalHealerVerdictPopulationUnreadable
	case v.Failed > 0:
		return PersonalHealerVerdictFailed
	case v.EdgeSpansDeployment:
		return PersonalHealerVerdictClean
	case !v.InstanceCountReadable:
		return PersonalHealerVerdictInstancesUnreadable
	case v.InstanceCount == 0:
		return PersonalHealerVerdictInstancesImpossible
	case v.InstanceCount > 1:
		return PersonalHealerVerdictMultipleInstances
	}
	return PersonalHealerVerdictClean
}

// PersonalHealerVerdictFn hands this package the healer's latest verdict.
//
// A bare func rather than a named interface for the reason §4.2's interest sink
// takes one: the contract is one method, the producer lives in a package this
// one may not import, and a third package holding the interface would be a
// second spelling of a one-method contract with nothing else in it.
type PersonalHealerVerdictFn func() PersonalHealerVerdict

// PersonalDerivationWiring is what the host asserts about the process a
// personal lens is registered into. Every field is a fact only cmd/refractor
// holds, and every zero value is the refusing answer.
//
// TWO OF THEM ARE ACCESSORS RATHER THAN BOOLEANS, and that distinction is
// load-bearing. A cap-read producer can install AFTER this lens registered — a
// hot lens install, a package added at runtime — and so can an
// InterestReconciler, which cmd/refractor constructs inside the very activation
// arm that registers the first personal lens. A boolean sampled at registration
// would answer about a process that no longer exists, in the fail-OPEN
// direction, so those two are read live at every gate evaluation exactly as the
// healer verdict is. A nil accessor is a refusal, never a pass: "nobody wired a
// way to check" and "checked, and it is fine" are different answers.
type PersonalDerivationWiring struct {
	// PersonalLens is the class conjunct: this pipeline carries the personal
	// envelope and publishes a key-set frame. It is asserted rather than
	// derived because a licence made only of wiring conjuncts would say nothing
	// about the class the whole argument was made about — the plain licence
	// opens with p.authPlane for exactly the same reason.
	PersonalLens bool
	// ReadGateWired is InstallPersonalLens's capKV != nil. A personal lens
	// installed with a nil handle runs its D1 gate open; narrowing what it
	// reprojects on top of that would be narrowing an unguarded projection.
	ReadGateWired bool
	// GrantReprojectorWired is whether a grantchange.Reprojector exists in this
	// process at all, i.e. whether input 1 has an edge HERE.
	GrantReprojectorWired bool
	// SinklessCapReadProducers lists, LIVE, the cap-read producers installed in
	// this process with no grant-change sink. A qualifying producer with no sink
	// installs deliberately (refusing it would turn a host omission into an
	// auth-plane outage) and warns; this is where that warning becomes a
	// refusal, because a producer whose withdrawals push nothing leaves the
	// consumer's whole argument resting on the sweeper alone.
	//
	// An accessor rather than a boolean because a producer can install after
	// this lens did. nil refuses.
	SinklessCapReadProducers func() []string
	// InterestFilterInstalled is InstallPersonalLens's interestKV != nil. A
	// lens with no interest filter has only ONE out-of-pattern input, so it
	// owes no interest edge and this whole conjunct is inapplicable.
	InterestFilterInstalled bool
	// InterestEdgeArmed reports, LIVE, whether EVERY writer of the Interest Set
	// announces onto the reprojector in this process — the control plane's
	// register/deregister/hydrate arms and every InterestReconciler constructed
	// here. An accessor rather than a boolean because cmd/refractor builds the
	// reconciler inside the same activation arm that registers the first
	// personal lens, and a value sampled at registration would speak for a
	// process one line younger than the one that runs. nil refuses.
	InterestEdgeArmed func() bool
	// RegisteredAt is when this lens joined the standing healer's registry. The
	// licence requires a pass BEGUN after it: a lens registering into an
	// already-swept plane must not inherit a clean verdict from a pass that
	// never drove it.
	RegisteredAt time.Time
}

// personalLicenceWiring is the pair the host installs at the registration call,
// held behind one atomic pointer so a reader can never see the wiring of one
// registration beside the verdict accessor of another.
type personalLicenceWiring struct {
	wiring  PersonalDerivationWiring
	verdict PersonalHealerVerdictFn
}

// SetPersonalDerivationLicence records the host's assertion of the licence's
// wiring conjuncts and the accessor its live conjuncts read.
//
// Called from the same site that registers the standing healer, and for the
// same reason SetPersonalPlaneHealer is: registration is what makes these
// facts true, and a deployment that wires a personal lens without them must
// read as unlicensed rather than be inferred to be licensed from the envelope
// shape it happens to carry.
//
// ASSERTED ONCE, AT ACTIVATION, and not re-asserted afterwards: cmd/refractor's
// startPipeline is registerPersonalHealer's only caller, and a hot reload
// (reload.go's update path) re-publishes the rule without re-running it. That is
// correct rather than a gap, and the division is what makes it so — everything
// asserted here is about the PROCESS, which a rule edit does not change, and the
// two members that CAN change after activation (the producer-sink and
// Interest-Set censuses) are accessors read live rather than values captured
// now. The one conjunct that is about the rule body, the $now/$projectedAt
// clock, is not here at all: it rides ruleState and is re-derived by the very
// publication a reload performs, so an edited body is still caught.
//
// RegisteredAt therefore records the lens's FIRST registration and keeps it
// across reloads, which is the reading conjunct 3 wants: the healer's coverage
// of this lens does not lapse because its cypher was edited.
//
// A nil verdict accessor leaves the live conjuncts with nothing to read, which
// the licence treats as a healer that has never completed a pass — the
// fail-closed answer, and the same one a real never-passed healer produces.
func (p *Pipeline) SetPersonalDerivationLicence(w PersonalDerivationWiring, verdict PersonalHealerVerdictFn) {
	p.personalLicence.Store(&personalLicenceWiring{wiring: w, verdict: verdict})
}

// personalLicenceState reads the host's assertion. The zero value — a host that
// never called the setter — refuses at conjunct 0.
func (p *Pipeline) personalLicenceState() personalLicenceWiring {
	if v := p.personalLicence.Load(); v != nil {
		return *v
	}
	return personalLicenceWiring{}
}

// assertedPersonalLens reports whether the host declared this pipeline a
// personal lens. It is read by the refusal note so a personal lens prints the
// conjunct that actually refused it rather than the generic out-of-pattern
// sentence, and by nothing that decides a projection.
func (p *Pipeline) assertedPersonalLens() bool {
	return p.personalLicenceState().wiring.PersonalLens
}

// PersonalDerivationLicence reports whether this lens's narrowing licence holds
// right now, and the conjunct refusing it otherwise.
//
// It exists so the host's wiring can be verified against the RUNNING predicate
// rather than against a restatement of it, and so an operator surface can ask
// one lens why it is not narrowing without waiting for a CDC event to produce
// the refusal note. It re-derives on every call, which is the same cost the gate
// pays per event; it is not a cached verdict.
func (p *Pipeline) PersonalDerivationLicence() (licensed bool, refusal string) {
	return p.personalDerivationLicence(p.ruleState())
}

// PersonalHealerVerdictNow reports the verdict the licence would read right
// now, or the zero verdict when no accessor is installed. Exported for the
// operator surfaces and the tests that need to state what the licence is
// looking at without re-deriving it.
func (p *Pipeline) PersonalHealerVerdictNow() PersonalHealerVerdict {
	st := p.personalLicenceState()
	if st.verdict == nil {
		return PersonalHealerVerdict{}
	}
	return st.verdict()
}

// personalDerivationLicence answers whether a personal lens may act on a
// derived anchor set, and names the conjunct that refused when it may not.
//
// ORDER. The wiring conjuncts come first: they are field reads, they hold for
// the life of the registration, and a lens refused by one of them would be
// refused whatever its healer or its query were doing — reporting anything else
// for it would send an operator to fix the wrong thing. The healer's verdict
// follows, because it is the conjunct most likely to MOVE under a lens that is
// otherwise permanently eligible, and because conjunct 5 rides the same
// snapshot (the deployment's cardinality is read once per pass, on the healer's
// clock, never per event). The query's own parameters are asked LAST: it is the
// dearest of the conjuncts — a walk of the compiled rule — and it is a fixed
// property of the cypher, so reporting it only for a lens that is otherwise
// fully eligible is the reading an operator can act on.
//
// STABILITY. Every refusal string below is fixed for as long as the state
// producing it holds — no elapsed duration, no live count, is interpolated into
// one. The caller latches on the reason string to log a refusal at most once
// (noteStaticDerivationRefusal), and a staleness window is minutes, so a reason
// carrying a per-second elapsed would defeat that latch precisely where it
// matters most and emit a line per CDC event. The measurement belongs in the
// LOG's own fields, never in the reason.
func (p *Pipeline) personalDerivationLicence(rs ruleState) (licensed bool, refusal string) {
	st := p.personalLicenceState()

	if !st.wiring.PersonalLens {
		return false, "it is not a personal lens, and this licence speaks for the personal plane alone"
	}
	if !st.wiring.ReadGateWired {
		return false, "the D1 read gate is not wired on it, so its rows are not read-authorized in the first place"
	}
	if !st.wiring.GrantReprojectorWired {
		return false, "no grant-change reprojector is wired in this process, so a withdrawn read grant reaches it through nothing"
	}
	if st.wiring.SinklessCapReadProducers == nil {
		return false, "nothing in this process can report whether the cap-read producers carry their grant-change sinks, and an uncheckable premise is not a satisfied one"
	}
	if len(st.wiring.SinklessCapReadProducers()) > 0 {
		return false, "a cap-read producer is installed with no grant-change sink, so some of the grants it reads are withdrawn silently"
	}
	if st.wiring.InterestFilterInstalled {
		if st.wiring.InterestEdgeArmed == nil {
			return false, "nothing in this process can report whether the Interest Set's writers announce, and an uncheckable premise is not a satisfied one"
		}
		if !st.wiring.InterestEdgeArmed() {
			return false, "the Interest Set has no change edge, so a device that narrows what it wants changes nothing until the sweep comes round"
		}
	}

	if st.verdict == nil {
		return false, "the personal-plane healer has never completed a pass, and a healer that has repaired nothing yet licenses nothing yet"
	}
	v := st.verdict()
	switch {
	case v.CompletedAt.IsZero():
		return false, "the personal-plane healer has never completed a pass, and a healer that has repaired nothing yet licenses nothing yet"
	case !v.PopulationReadable:
		return false, "the personal-plane healer could not enumerate its population on its last pass, so nothing is standing behind these rows"
	case v.Failed > 0:
		return false, "the personal-plane healer's last pass failed for at least one actor, so it healed less than the whole population it walked"
	case !v.StartedAt.After(st.wiring.RegisteredAt):
		// A lens registering into an already-swept plane would otherwise
		// inherit a clean verdict from a pass that BEGAN before it joined the
		// registry and therefore never drove it. The healer's guarantee is
		// per-lens; so is the evidence for it.
		return false, "the personal-plane healer has not completed a pass begun after this lens registered, so its own rows have not been covered once"
	}
	if stale, window := personalHealerStale(v, time.Now()); stale {
		return false, fmt.Sprintf("the personal-plane healer has not completed a pass in %d of its own sweep intervals, so nothing is proven to be re-testing these rows", window)
	}

	// Conjunct 5. The grant-change edge is an in-process function call, so on a
	// second instance a producer's announcement never reaches a personal
	// pipeline hosted elsewhere — while every wiring conjunct above stays TRUE
	// on every instance. That is a fail-open at exactly the transition, which is
	// why the cardinality is tested rather than narrated.
	//
	// The count is a BACKSTOP with a bounded exposure window, not the primary
	// defence, and the two staleness directions are why. A crashed instance
	// leaving an unexpired entry OVER-counts and refuses the licence —
	// pessimisation, safe, and asserted as correct by a test so a later
	// "optimisation" that trusts freshness has something standing in front of
	// it. A newly started second instance UNDER-counts, and the licence stays on
	// meanwhile. THE WINDOW IS NOT THE HEARTBEAT INTERVAL: this value is
	// re-derived once per SWEEP pass, so the exposure runs until the first sweep
	// pass that BEGINS after the second instance's first heartbeat lands — up to
	// one sweep interval on top of the heartbeat itself, not one heartbeat. The
	// primary defence against that window is the build-time gate
	// (scripts/lint-refractor-single-instance.go), which refuses the deployment
	// affordance itself while the edge is still process-local.
	if !v.EdgeSpansDeployment {
		if !v.InstanceCountReadable {
			return false, "the live Refractor instance count is unreadable, and this licence rests on the grant-change edge reaching every process that holds a personal lens"
		}
		if v.InstanceCount == 0 {
			// ZERO IS SELF-REFUTING and must never license. The process asking
			// the question is itself a live Refractor, so a census that finds
			// none has not found an empty deployment — it has found a broken
			// census, and a broken census is exactly what two instances whose
			// heartbeats are not landing look like. Both would read clean and
			// both would narrow, which is the fail-open this conjunct exists to
			// close.
			//
			// The sweeper already fails closed on an empty listing. This is the
			// second assertion, on the consuming side, so a later edit there
			// cannot reopen it silently: the conjunct is about the NUMBER, and
			// the number zero is not a number of Refractors any live deployment
			// has.
			return false, "the live Refractor instance count reads zero, which is self-refuting — the process asking is itself live, so the census is broken rather than the deployment empty"
		}
		if v.InstanceCount > 1 {
			return false, "more than one Refractor instance is live, and the grant-change edge is in-process — a producer on one instance announces to no personal lens on another"
		}
	}

	// Conjunct 4, read off the published rule state rather than re-derived. It
	// is a fixed property of the cypher — PersonalDerivationRuleRefusal walks
	// the compiled rule's clauses twice, once per clock parameter — so
	// useFullEngineBranches computes it at publication and a reload replaces it
	// with the body it describes. Asked LAST of all, for the reason the plain
	// licence asks closure last: reporting a query-shape refusal only for a lens
	// that is otherwise fully eligible is the reading an operator can act on.
	return rs.personalClockRefusal == "", rs.personalClockRefusal
}

// personalHealerStale reports whether the verdict's clock has run past the
// licence's window, and how many of the healer's own intervals that window is.
//
// The window is measured in the healer's INTERVALS rather than in wall time so
// the returned number is the same one PersonalHealerStaleCycles names and the
// refusal string stays stable across a deployment that retunes the cadence. A
// verdict carrying no interval falls back to the shipped default rather than
// treating an unknown cadence as an infinite one.
func personalHealerStale(v PersonalHealerVerdict, now time.Time) (stale bool, cycles int) {
	interval := v.Interval
	if interval <= 0 {
		interval = DefaultPersonalHealerInterval
	}
	return now.Sub(v.CompletedAt) > time.Duration(PersonalHealerStaleCycles)*interval, PersonalHealerStaleCycles
}

// DefaultPersonalHealerInterval is the cadence assumed for a verdict that
// carries none. It mirrors grantchange.DefaultPersonalSweepInterval, which this
// package cannot import, and the two are pinned together by a cross-package
// test rather than left to agree by inspection.
const DefaultPersonalHealerInterval = 60 * time.Second

// personalDerivationRuleRefusal answers the licence's conjunct 4 — the one that
// is a property of the CYPHER alone — or "" when the rule clears it.
//
// It runs at rule PUBLICATION (useFullEngineBranches), never per event: two
// exhaustive walks of the compiled rule's clauses to answer a question whose
// answer cannot change until the rule body does. The licence reads the result
// off ruleState.personalClockRefusal.
//
// A row that moves with the wall clock is the purest out-of-pattern input there
// is, and after this narrowing the only thing that would refresh it is the
// sweeper's own cycle. It mirrors the plain licence's loop exactly, including
// the non-exhaustive arm: "could not be proven free of" and "references" are
// different answers, and only one of them is safe.
//
// No shipped personal lens uses either parameter, so the conjunct is LATENT.
// That is exactly why it exists before one does: the corpus census pins the
// verdict per lens, so the day an author adds a $now to a personal cypher the
// census moves rather than the narrowing silently starting to lie.
func personalDerivationRuleRefusal(fullCR *full.CompiledRule) string {
	for _, param := range []string{"now", "projectedAt"} {
		referenced, exhaustive := fullCR.ReferencesParam(param)
		if !exhaustive {
			return "its query shape could not be proven free of $" + param + ", which only the sweep would refresh once the derivation narrows it"
		}
		if referenced {
			return "the lens's row depends on $" + param + ", which changes with no pattern edge changing and which only the sweep would refresh"
		}
	}
	return ""
}

// PersonalDerivationRuleRefusal is personalDerivationRuleRefusal over any
// compiled rule, exported so the corpus census asks the RUNNING conjunct rather
// than restating it. A census that re-derives what it pins goes green while the
// two drift, and the direction that drifts silently here is a personal lens
// quietly becoming licensable.
//
// A rule that is not a full-engine rule answers with the licence's own refusal
// for that case, so the census's verdict vocabulary is closed over both.
func PersonalDerivationRuleRefusal(cr ruleengine.CompiledRule) string {
	fullCR, isFull := cr.(*full.CompiledRule)
	if !isFull || fullCR == nil {
		return "its compiled rule is not a full-engine rule, so its parameters cannot be derived"
	}
	return personalDerivationRuleRefusal(fullCR)
}

// notePersonalDerivationLicensed logs, once per transition into the licensed
// state, that a personal lens has started acting on its derived anchor set.
//
// It exists because a GRANTED licence otherwise logs nothing: the refusal note
// prints a reason, and its absence proves only that some line stopped being
// emitted — which is indistinguishable from the lens no longer receiving
// events, from the mode knob being off, and from the log level having moved.
// The payoff of this whole design is claimed as "the refusal is gone", and a
// claim of that shape is provable only by a POSITIVE verdict read live.
//
// Latched on the transition rather than emitted per event, and it CLEARS the
// refusal latch: a lens that becomes licensed and later loses the licence must
// log the new refusal even when it is the same string it printed before the
// grant.
func (p *Pipeline) notePersonalDerivationLicensed() {
	if !p.assertedPersonalLens() {
		return
	}
	p.derivShadow.mu.Lock()
	repeat := p.derivShadow.personalLicensed
	p.derivShadow.personalLicensed = true
	p.derivShadow.staticRefusal = ""
	p.derivShadow.staticRefusalSet = false
	p.derivShadow.mu.Unlock()
	if repeat {
		return
	}
	v := p.PersonalHealerVerdictNow()
	slog.Info("pipeline: personal-lens derivation licensed; this lens now acts on its derived anchor set",
		"ruleId", p.ruleID,
		"healerVerdict", v.Summary(),
		"healerLastPassAt", v.CompletedAt,
		"refractorInstances", v.InstanceCount)
}

// clearPersonalLicensedLatchLocked clears the positive latch so a later grant
// is reported again. Called from the refusal note, which is the only place that
// observes the transition in the other direction and which already holds
// derivShadow.mu — the two latches move together or a grant/revoke/grant cycle
// logs one of its two edges.
func (p *Pipeline) clearPersonalLicensedLatchLocked() {
	p.derivShadow.personalLicensed = false
}
