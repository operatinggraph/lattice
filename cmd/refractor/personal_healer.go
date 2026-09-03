package main

import (
	"time"

	"github.com/operatinggraph/lattice/internal/refractor/control"
	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
)

// registerPersonalHealer enrols a Personal Lens's pipeline with the grant-change
// edge's consumer registry, tells the pipeline it now has a standing healer, and
// asserts the personal derivation licence's wiring conjuncts
// (personal-lens-derivation-licence-design.md §4.4c).
//
// Registration is what makes the personal plane's standing healer real for this
// lens — grantchange.PersonalSweeper walks the identity population over exactly
// this registry, and the D1 grant-change edge re-drives through it. The pipeline
// is told at the call that does it, rather than inferring it from the envelope
// shape: a deployment that installed personal lenses without the reprojector has
// no healer, and pipeline.walkScopeFor must read that truthfully (it withholds
// the pattern-scoped actor walk from a lens with no healer, per §4.2's conjunct).
// One function for both halves so main and its wiring test cannot drift apart on
// the pair or its order.
//
// The LICENCE is asserted from the same site for the same reason, one step
// further on. Its wiring conjuncts are facts only the host holds — was a
// capability-KV handle threaded into this lens, does a reprojector exist in this
// process, does every cap-read producer installed here announce, do the Interest
// Set's writers announce — and none of them is derivable from inside the
// pipeline package. Its live conjuncts read the sweeper's own pass verdict
// through a one-method accessor, because pipeline cannot import grantchange.
// Every zero value refuses, so a host that says nothing narrows nothing.
func registerPersonalHealer(
	reprojector *grantchange.Reprojector,
	sweeper *grantchange.PersonalSweeper,
	control *control.Service,
	ruleID string,
	p *pipeline.Pipeline,
	wiring pipeline.PersonalDerivationWiring,
) {
	reprojector.RegisterPersonal(ruleID, p)
	p.SetPersonalPlaneHealer(true)

	// The two conjuncts that are live CENSUSES rather than boot-time constants,
	// wired as accessors the licence calls at every gate evaluation rather than
	// as values sampled here. Both name things that can come into existence
	// after this lens registered — a cap-read producer through a hot lens
	// install, an InterestReconciler a few statements further down this very
	// activation arm — and a sample taken now would answer about a process that
	// no longer exists, in the fail-open direction.
	wiring.SinklessCapReadProducers = projection.ReadGrantProducersWithoutSink
	wiring.RegisteredAt = time.Now()

	// A nil sweeper is a deployment with no personal standing healer at all. It
	// leaves the licence's live conjuncts with nothing to read, which the
	// predicate treats as a healer that has never completed a pass — the same
	// fail-closed answer a real never-passed healer produces.
	var verdict pipeline.PersonalHealerVerdictFn
	if sweeper != nil {
		verdict = sweeper.Verdict
	}
	p.SetPersonalDerivationLicence(wiring, verdict)

	// The operator surface for both halves of "is this lens narrowing": the
	// licence and the lens's own pattern index, each with the conjunct refusing
	// it. Without it the only evidence of a refusal is a log line emitted at
	// most once per distinct reason, so a lens quietly on the enumerator hours
	// later has nothing that says why — and the health KV entry cannot answer
	// it, since its personalSweepVerdict is the shared sweeper's plane-wide pass
	// verdict rather than this lens's conjunct. Registered here, at the same
	// call that asserts the licence, so the two cannot drift about which lenses
	// have one.
	if control != nil {
		control.RegisterPersonalDerivationStatus(ruleID, p.PersonalDerivationStatus)
	}
}
