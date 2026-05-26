package processor

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// AuthMode selects the step-3 Authorizer wired into the commit path.
// Empty mode and `capability` both resolve to the real Capability KV
// authorizer. `stub` is available for unit-tier tests that don't need auth
// correctness (fault injection, dedup tests, etc.) — production deployments
// selecting it emit a security alert at every Authorize call AND at startup.
type AuthMode string

const (
	// AuthModeStub is the always-allow authorizer for test/dev use.
	// Production deploys must use AuthModeCapability (the default).
	AuthModeStub AuthMode = "stub"
	// AuthModeCapability is the Capability KV authorizer.
	// Empty AuthMode also resolves to this mode.
	AuthModeCapability AuthMode = "capability"
)

// Decision is the outcome of an Authorizer.Authorize call.
type Decision struct {
	Authorized bool
	// Stub is true when the decision came from StubAuthorizer (helps
	// downstream logging and bypass-test assertions distinguish a real
	// allow from a stubbed allow).
	Stub bool
	// Reason carries a short human-readable explanation. Empty when
	// Authorized=true.
	Reason string
	// Code is set when Authorized=false. Maps to Contract #2 §2.6 reply
	// error codes (LaneUnauthorized, AuthDenied, AuthContextMismatch,
	// AuthFreshnessExceeded).
	Code ErrorCode
	// Resolved is the per-operation permission entry that matched at step 3.
	// Nil on denials and on the StubAuthorizer path. Threaded through the
	// commit-path for downstream observability (denial response, auth trace).
	// Strictly internal — never bled into OperationEnvelope or OperationReply.
	Resolved *ResolvedPermission
	// Doc is the parsed CapabilityDoc from the Capability KV GET. Set on all
	// non-NoCapabilityEntry denial paths so DenialResponseBuilder can access
	// doc.Roles for actorRoles without an additional KV read. Nil on
	// StubAuthorizer, NoCapabilityEntry, and infrastructure-failure paths.
	// Strictly internal.
	Doc *CapabilityDoc
}

// Authorizer is the step-3 interface. CapabilityAuthorizer is the production
// implementation; StubAuthorizer is for unit tests.
type Authorizer interface {
	Authorize(ctx context.Context, env *OperationEnvelope) (Decision, error)
}

// StubAuthorizer always returns Authorized=true, Stub=true. For unit-tier
// tests only; production deploys must select AuthModeCapability (the default).
// Each Authorize call emits a WARN log and — once every stubAlertEveryNCalls
// calls — a Health KV alert under `health.alerts.security.stub-auth-active`
// so the degradation is visible in dashboards without flooding the bucket.
type StubAuthorizer struct {
	logger  *slog.Logger
	emitter AuthAlertEmitter
	counter atomic.Uint64
}

// stubAlertEveryNCalls — every Nth Authorize call additionally emits a
// Health KV alert. Decision #7: avoid flooding while keeping the signal
// alive between heartbeats.
const stubAlertEveryNCalls uint64 = 1000

// NewStubAuthorizer constructs the stub. Pass a logger so warnings are
// emitted on each Authorize call (auditability — operators must be able
// to see when their cluster is running the stub).
func NewStubAuthorizer(logger *slog.Logger) *StubAuthorizer {
	return NewStubAuthorizerWithEmitter(logger, nil)
}

// NewStubAuthorizerWithEmitter wires the Health KV alert emitter so operators
// see `health.alerts.security.stub-auth-active` markers in dashboards.
func NewStubAuthorizerWithEmitter(logger *slog.Logger, emitter AuthAlertEmitter) *StubAuthorizer {
	if logger == nil {
		logger = slog.Default()
	}
	if emitter == nil {
		emitter = noopAlertEmitter{}
	}
	return &StubAuthorizer{logger: logger, emitter: emitter}
}

// Authorize implements Authorizer.
func (s *StubAuthorizer) Authorize(ctx context.Context, env *OperationEnvelope) (Decision, error) {
	s.logger.Warn("STUB AUTH: allow-all; set LATTICE_AUTH_MODE=capability to enable Capability KV auth",
		"requestId", env.RequestID,
		"actor", env.Actor,
		"operationType", env.OperationType,
		"lane", string(env.Lane),
	)
	n := s.counter.Add(1)
	if n == 1 || n%stubAlertEveryNCalls == 0 {
		s.emitter.EmitAlert(ctx, "stub-auth-active", map[string]any{
			"callCount":     n,
			"requestId":     env.RequestID,
			"actor":         env.Actor,
			"operationType": env.OperationType,
		})
	}
	return Decision{Authorized: true, Stub: true}, nil
}

// SelectAuthorizer returns the Authorizer implementation matching mode.
// Empty and `capability` both resolve to CapabilityAuthorizer; `stub` is
// opt-in for tests. `capability` mode requires a non-nil CapabilityReader
// + bucket — pass them via SelectAuthorizerArgs. The two-arg form is a thin
// wrapper that falls back to StubAuthorizer for tests without Capability KV.
func SelectAuthorizer(mode AuthMode, logger *slog.Logger) (Authorizer, error) {
	return SelectAuthorizerArgs(SelectAuthorizerOpts{
		Mode:   mode,
		Logger: logger,
	})
}

// SelectAuthorizerOpts bundles the inputs to SelectAuthorizerArgs.
type SelectAuthorizerOpts struct {
	Mode             AuthMode
	Logger           *slog.Logger
	Reader           CapabilityReader
	CapabilityBucket string
	Clock            Clock
	Emitter          AuthAlertEmitter
	Config           CapabilityAuthorizerConfig
}

// SelectAuthorizerArgs is the production wiring entry point.
func SelectAuthorizerArgs(opts SelectAuthorizerOpts) (Authorizer, error) {
	switch opts.Mode {
	case AuthModeStub:
		// Explicit stub — emit startup alert + per-call alerts so
		// operators see the degraded state.
		if opts.Emitter != nil {
			opts.Emitter.EmitAlert(context.Background(), "stub-auth-active", map[string]any{
				"event": "processor-startup",
			})
		}
		if opts.Logger != nil {
			opts.Logger.Warn("LATTICE_AUTH_MODE=stub: Processor running with allow-all authorizer (NOT FOR PRODUCTION)")
		}
		return NewStubAuthorizerWithEmitter(opts.Logger, opts.Emitter), nil
	case "", AuthModeCapability:
		// Default + explicit capability → the real authorizer. Without a
		// reader we have nothing to read from; fail loudly at startup.
		if opts.Reader == nil || opts.CapabilityBucket == "" {
			return nil, errCapabilityModeRequiresReader
		}
		cfg := opts.Config
		if cfg.NFRP3 == 0 && cfg.StaleCeiling == 0 && cfg.LatencyBufferSize == 0 {
			cfg = DefaultCapabilityAuthorizerConfig()
		}
		return NewCapabilityAuthorizer(opts.Reader, opts.CapabilityBucket, opts.Clock, cfg, opts.Emitter, opts.Logger), nil
	default:
		return nil, errUnknownAuthMode(opts.Mode)
	}
}
