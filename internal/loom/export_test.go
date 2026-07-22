package loom

import (
	"context"
	"encoding/json"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// ParseGuardForTest exposes parseGuard to the external loom_test package so the
// e2e absence-semantics table test can build guard ASTs without re-deriving the
// grammar. The returned *guard is opaque to the caller (it holds it only to pass
// to EvalGuardForTest).
func ParseGuardForTest(raw string) (*guard, error) {
	return parseGuard(json.RawMessage(raw))
}

// EvalGuardForTest exposes evalGuard to the external loom_test package for the
// pinned-absence-semantics table test against a real Core KV.
func EvalGuardForTest(ctx context.Context, conn *substrate.Conn, coreKVBucket, subjectKey string, g *guard) (bool, error) {
	return evalGuard(ctx, conn, coreKVBucket, subjectKey, g)
}

// PauseForTest manually pauses a managed consumer via the supervisor. Test-only
// seam: Loom exposes no operator Pause/Resume control surface in production
// (that is a future control-plane story), but the supervisor API is callable for
// the pause-restore test.
func (e *Engine) PauseForTest(ctx context.Context, name string) {
	e.supervisor.Pause(ctx, name)
}

// ResumeForTest manually resumes a consumer the test paused via PauseForTest.
// Test-only seam (same rationale as PauseForTest): it lets the externalTask
// re-arm test pause the outbox relay so a deadline fires with the instanceOp
// still undelivered, then resume delivery to prove the probe re-armed rather
// than failed.
func (e *Engine) ResumeForTest(ctx context.Context, name string) {
	e.supervisor.Resume(ctx, name)
}

// ResetDomainForTest forces a config-divergence Reset of a per-domain consumer
// through the supervisor (UpdateSpec + Reset), exercising the reconcile diff's
// Reset branch — which production never reaches because the per-domain filter is
// name-derived and stable.
func (e *Engine) ResetDomainForTest(ctx context.Context, domain string) error {
	spec := e.domainSpec(domain)
	if err := e.supervisor.UpdateSpec(spec.Name, func(s *substrate.ConsumerSpec) { *s = spec }); err != nil {
		return err
	}
	return e.supervisor.Reset(ctx, spec.Name)
}
