package processor

import (
	"context"
	"log/slog"
	"sync/atomic"
)

// AuthMode selects the step-3 Authorizer wired into the commit path.
//
// Story 3.3 default-flip: empty mode and `capability` both resolve to
// the real Capability KV authorizer. `stub` remains available behind an
// explicit env knob for unit-tier tests that don't care about auth
// correctness (NFR-R1 fault injection, dedup, etc.) — production
// deployments selecting it emit a security alert at every Authorize
// call AND at startup so operators see the degradation in their
// dashboards.
type AuthMode string

const (
	// AuthModeStub is the Story 1.5 always-allow authorizer. After Story
	// 3.3 it is a test/dev-only mode; production deploys must use
	// AuthModeCapability (the default).
	AuthModeStub AuthMode = "stub"
	// AuthModeCapability is the Story 3.3 Capability KV authorizer.
	// Empty AuthMode also resolves to this mode (Story 3.3 default flip).
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
	// Resolved is the per-operation permission entry that matched at
	// step 3 (Story 3.3 AC #3 / Decision #8). Nil on denials and on the
	// StubAuthorizer path. Consumers in Stories 3.4 (denial response)
	// and 3.5 (auth failure traceability) thread this through the
	// commit-path to step 9 event publication for downstream
	// observability. Strictly internal — never bled into
	// OperationEnvelope or OperationReply.
	Resolved *ResolvedPermission
}

// Authorizer is the step-3 interface. Story 3.3 lights up the real
// CapabilityAuthorizer behind it; StubAuthorizer remains for unit tests.
type Authorizer interface {
	Authorize(ctx context.Context, env *OperationEnvelope) (Decision, error)
}

// StubAuthorizer always returns Authorized=true, Stub=true. Reserved for
// unit-tier tests after Story 3.3 — production deploys must select
// AuthModeCapability (the new default).
//
// Story 3.3 Decision #7: each Authorize call still emits a WARN log AND
// — once every stubAlertEveryNCalls calls — a Health KV alert under
// `health.alerts.security.stub-auth-active` so the degradation is
// visible in dashboards without flooding the bucket.
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

// NewStubAuthorizerWithEmitter is the Story 3.3 constructor that also
// wires the Health KV alert emitter so operators see
// `health.alerts.security.stub-auth-active` markers.
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
	s.logger.Warn("STUB AUTH: allow-all (Story 1.5; replaced by Capability KV in Story 3.3)",
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
// Story 3.3 default flip: empty AND `capability` resolve to the real
// CapabilityAuthorizer; `stub` is opt-in behind explicit env knob.
//
// `capability` mode requires a non-nil CapabilityReader + bucket; pass
// them via SelectAuthorizerArgs. The legacy two-arg form is retained as
// a thin wrapper that returns the StubAuthorizer for backwards
// compatibility with tests that don't care about Capability KV.
func SelectAuthorizer(mode AuthMode, logger *slog.Logger) (Authorizer, error) {
	return SelectAuthorizerArgs(SelectAuthorizerOpts{
		Mode:   mode,
		Logger: logger,
	})
}

// SelectAuthorizerOpts is the Story-3.3 widened constructor input.
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
