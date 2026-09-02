package main

import (
	"github.com/operatinggraph/lattice/internal/refractor/grantchange"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// registerPersonalHealer enrols a Personal Lens's pipeline with the grant-change
// edge's consumer registry and tells the pipeline it now has a standing healer.
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
func registerPersonalHealer(reprojector *grantchange.Reprojector, ruleID string, p *pipeline.Pipeline) {
	reprojector.RegisterPersonal(ruleID, p)
	p.SetPersonalPlaneHealer(true)
}
