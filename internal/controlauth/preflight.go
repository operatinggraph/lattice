package controlauth

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/operatinggraph/lattice/internal/capabilitykv"
)

var (
	errNoEntry = errors.New("no capability kv entry")
	errNoGrant = errors.New("no ctrl.<component>.* grant in entry")
)

// Preflight checks, at startup, whether the configured operator actor already
// has a resolvable control grant for this component. It NEVER blocks startup
// or changes enforcement — CapabilityKVChecker.Authorize is already
// fail-closed on a missing/absent grant regardless of this check — its only
// job is to make an impending flag-day lockout LOUD instead of silent (design
// §3.3/R2): a missing grant emits a startup Health KV alert and a logged
// error so operators see the problem before their first denied request does.
//
// operatorActor is the seeded control-operator identity's actor key (e.g. the
// LATTICE_CONTROL_OPERATOR_ACTOR_KEY / LOUPE_OPERATOR_ACTOR_KEY value). An
// empty operatorActor means no operator identity has been provisioned yet
// (control-authz ships the grant DATA at install time — §3.5 — but package
// installers cannot seed an identity vertex, so the identity itself is a
// one-time post-install op an operator runs); Preflight logs that and returns
// nil rather than guessing.
func Preflight(ctx context.Context, checker *CapabilityKVChecker, operatorActor string, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if checker.mode != AuthModeCapability {
		return
	}
	if operatorActor == "" {
		logger.Warn("controlauth preflight: no configured operator actor; skipping grant-presence check " +
			"(capability mode is enforced regardless — an unresolvable grant already denies)")
		return
	}
	keys, err := checker.keysFor(operatorActor)
	if err != nil {
		logger.Error("controlauth preflight: malformed operator actor key", "actor", operatorActor, "error", err)
		return
	}
	// Use the checker's own read path so the preflight sees exactly what
	// Authorize would see (same reader, same bucket, same key routing).
	probeErr, grantErr := checker.probeGrant(ctx, keys)
	if probeErr != nil {
		// A timeout/read failure is NOT "no grant" — conflating the two would
		// send an on-call engineer to fix RBAC data when the real problem is
		// KV latency/availability. Distinct code, distinct log line.
		logger.Error("controlauth preflight: could not probe the operator's grant (infra error, not a grant verdict)",
			"component", checker.component, "actor", operatorActor, "error", probeErr)
		checker.alerts.EmitAlert(ctx, "control-preflight-probe-failed", map[string]any{
			"component": checker.component,
			"actor":     operatorActor,
			"error":     probeErr.Error(),
		})
		return
	}
	if grantErr == nil {
		return
	}
	logger.Error("controlauth preflight: operator actor has no resolvable control grant — "+
		"every control request will be denied until control-authz is installed and the actor is granted",
		"component", checker.component, "actor", operatorActor, "reason", grantErr)
	checker.alerts.EmitAlert(ctx, "control-operator-grant-unresolved", map[string]any{
		"component": checker.component,
		"actor":     operatorActor,
		"reason":    grantErr.Error(),
	})
}

// probeGrant reports whether operatorActor's merged doc carries AT LEAST ONE
// ctrl.<component>.* grant (any verb) — a coarser check than Authorize's
// per-op match, since preflight is a presence signal, not a per-op decision.
// The two return values are DISTINCT failure classes: probeErr is an infra
// failure (the read itself could not complete — timeout, KV unavailable);
// grantErr is a definitive "checked and found nothing" verdict (errNoEntry /
// errNoGrant). Callers must not conflate the two.
func (c *CapabilityKVChecker) probeGrant(ctx context.Context, keys []string) (probeErr, grantErr error) {
	doc, _, err := capabilitykv.ReadAndMerge(ctx, c.reader, c.bucket, keys)
	if err != nil {
		return err, nil
	}
	if doc == nil {
		return nil, errNoEntry
	}
	prefix := "ctrl." + c.component + "."
	for _, p := range doc.PlatformPermissions {
		if strings.HasPrefix(p.OperationType, prefix) && p.Scope == "any" {
			return nil, nil
		}
	}
	return nil, errNoGrant
}
