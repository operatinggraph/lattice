package control

import (
	"context"
	"log/slog"
)

// CapabilityChecker authorizes a control-plane operation against
// Lattice's Capability KV (Contract #6). Story 2.1 ships a stub
// implementation that allow-all and logs every call — full integration
// is Epic 3 work. The interface lives here so the control service can
// be swapped to a real checker without touching handler bodies.
type CapabilityChecker interface {
	// Authorize returns nil if the given actor may invoke op on the given
	// lens. Returns a non-nil error when the operation must be denied.
	Authorize(ctx context.Context, actor, op, lensID string) error
}

// StubCapabilityChecker is the default Story 2.1 implementation: allow
// every request and log it. Mirrors `internal/processor/StubAuthorizer`.
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
