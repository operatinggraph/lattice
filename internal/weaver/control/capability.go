package control

import (
	"context"
	"errors"
	"log/slog"
)

// CapabilityChecker authorizes a control-plane operation against
// Lattice's Capability KV (Contract #6). Production wires the real
// controlauth.CapabilityKVChecker (cmd/weaver aborts startup if that
// construction fails), so the control plane enforces the Capability KV.
// The interface lives here so the control service can be swapped to a real
// checker without touching handler bodies. Mirrors
// internal/refractor/control.CapabilityChecker.
type CapabilityChecker interface {
	// Authorize returns nil if the given actor may invoke op on the given
	// target. Returns a non-nil error when the operation must be denied.
	Authorize(ctx context.Context, actor, op, targetID string) error
}

// ErrCheckerUnconfigured is returned by the deny-all default checker that
// guards a nil/misconfigured CapabilityChecker. It fails closed: a control
// plane with no configured checker denies every operation rather than
// silently allowing them.
var ErrCheckerUnconfigured = errors.New("control: capability checker unconfigured; denying")

// StubCapabilityChecker allow-alls every request and logs it. It is an
// explicit dev/test opt-in (allow-all), NOT the nil default — NewService's
// nil fallback uses the fail-closed denyAllChecker. Mirrors
// internal/refractor/control.StubCapabilityChecker.
type StubCapabilityChecker struct {
	Logger *slog.Logger
}

// NewStubCapabilityChecker constructs a permissive checker.
func NewStubCapabilityChecker(logger *slog.Logger) *StubCapabilityChecker {
	if logger == nil {
		logger = slog.Default()
	}
	return &StubCapabilityChecker{Logger: logger}
}

// Authorize always returns nil and logs the call.
func (s *StubCapabilityChecker) Authorize(ctx context.Context, actor, op, targetID string) error {
	s.Logger.Info("weaver control capability stub: ALLOW", "actor", actor, "op", op, "targetId", targetID)
	return nil
}

// denyAllChecker is the fail-closed default used when NewService is handed a
// nil CapabilityChecker: it denies every operation and logs the denial at
// Warn so an unconfigured control checker is loud, not quiet. Production never
// hits it (cmd/weaver wires a real controlauth.CapabilityKVChecker), so a
// denial from it signals a wiring regression.
type denyAllChecker struct {
	logger *slog.Logger
}

// newDenyAllChecker constructs the fail-closed default checker.
func newDenyAllChecker(logger *slog.Logger) *denyAllChecker {
	if logger == nil {
		logger = slog.Default()
	}
	return &denyAllChecker{logger: logger}
}

// Authorize denies every request and logs the denial at Warn.
func (d *denyAllChecker) Authorize(_ context.Context, actor, op, targetID string) error {
	d.logger.Warn("weaver control capability checker unconfigured: DENY",
		"actor", actor, "op", op, "targetId", targetID)
	return ErrCheckerUnconfigured
}
