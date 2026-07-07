package control

import (
	"context"
	"errors"
	"log/slog"
)

// CapabilityChecker authorizes a control-plane operation against
// Lattice's Capability KV (Contract #6). Production wires the real
// controlauth.CapabilityKVChecker via SetCapabilityChecker (cmd/refractor
// aborts startup if that construction fails), so the control plane enforces
// the Capability KV. The interface lives here so the control service can be
// swapped to a real checker without touching handler bodies.
type CapabilityChecker interface {
	// Authorize returns nil if the given actor may invoke op on the given
	// lens. Returns a non-nil error when the operation must be denied.
	Authorize(ctx context.Context, actor, op, lensID string) error
}

// ErrCheckerUnconfigured is returned by the deny-all default checker that
// guards the pre-set / nil-reset window before SetCapabilityChecker installs
// the real checker. It fails closed: a control plane with no configured
// checker denies every operation rather than silently allowing them.
var ErrCheckerUnconfigured = errors.New("control: capability checker unconfigured; denying")

// StubCapabilityChecker allow-alls every request and logs it. It is an
// explicit dev/test opt-in (allow-all), NOT the default — NewService and
// SetCapabilityChecker(nil) install the fail-closed denyAllChecker.
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
func (s *StubCapabilityChecker) Authorize(ctx context.Context, actor, op, lensID string) error {
	s.Logger.Info("control capability stub: ALLOW", "actor", actor, "op", op, "lensId", lensID)
	return nil
}

// denyAllChecker is the fail-closed default: it denies every operation and
// logs the denial at Warn so an unconfigured control checker is loud, not
// quiet. It governs the window between construction and the
// SetCapabilityChecker(real) call, and any SetCapabilityChecker(nil) reset.
// Production installs a real controlauth.CapabilityKVChecker before serving
// (cmd/refractor), so a denial from this checker signals a misconfiguration.
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
func (d *denyAllChecker) Authorize(_ context.Context, actor, op, lensID string) error {
	d.logger.Warn("refractor control capability checker unconfigured: DENY",
		"actor", actor, "op", op, "lensId", lensID)
	return ErrCheckerUnconfigured
}
