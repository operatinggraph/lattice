// Package control implements the Weaver control plane (FR30): a NATS
// Services responder exposing list/disable/enable/revoke operator commands
// for Weaver convergence targets, plus the cmd/lattice/weaver CLI's server
// side. Depends on internal/weaver only for the four engineControl methods
// and the weaver.TargetSummary type — internal/weaver never imports this
// package (one-way dependency, mirrors internal/refractor/control being a
// sibling of internal/refractor/{pipeline,lens,...}).
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/weaver"
)

// engineControl is the minimal interface this package needs from
// *weaver.Engine — list/disable/enable/revoke/resetConfidence. Defining it
// here (rather than depending on the full *weaver.Engine) keeps the dependency
// surface to exactly these five methods plus weaver.TargetSummary.
type engineControl interface {
	ListTargets(ctx context.Context) ([]weaver.TargetSummary, error)
	Disable(ctx context.Context, targetID string) error
	Enable(ctx context.Context, targetID string) error
	Revoke(ctx context.Context, targetID string) error
	ResetConfidence(ctx context.Context, targetID string) (int, error)
}

// subjectPrefix is the wildcard subject pattern the control endpoints are
// registered under. "list" is registered on the exact subject
// subjectPrefix+".list"; disable/enable/revoke/resetConfidence are registered on
// subjectPrefix+".*.<op>" — wildcards let one endpoint handler serve all
// target IDs, since the Weaver does not know the full set of target IDs at
// registration time.
const subjectPrefix = "lattice.ctrl.weaver"

// handlerTimeout bounds each control handler's engine call so a blocked KV op
// (KV unavailable, a slow list/delete loop) fails the request with an error
// reply instead of wedging the responder goroutine and leaving the operator's
// CLI to time out with no server-side cancellation.
const handlerTimeout = 5 * time.Second

// ControlResponse is the JSON payload returned by the control service.
// On success (list op): Targets is present.
// On success (disable op): Disable is present.
// On success (enable op): Enable is present.
// On success (revoke op): Revoke is present.
// On success (resetConfidence op): ResetConfidence is present.
// On error: only Error is present.
type ControlResponse struct {
	Targets         []weaver.TargetSummary `json:"targets,omitempty"`
	Disable         *DisableResult         `json:"disable,omitempty"`
	Enable          *EnableResult          `json:"enable,omitempty"`
	Revoke          *RevokeResult          `json:"revoke,omitempty"`
	ResetConfidence *ResetConfidenceResult `json:"resetConfidence,omitempty"`
	Error           string                 `json:"error,omitempty"`
}

// DisableResult is the synchronous acknowledgement returned by the
// "disable" op. Disabled is always true when the op succeeds.
type DisableResult struct {
	Disabled bool `json:"disabled"`
}

// EnableResult is the synchronous acknowledgement returned by the "enable"
// op. Enabled is always true when the op succeeds.
type EnableResult struct {
	Enabled bool `json:"enabled"`
}

// RevokeResult is the synchronous acknowledgement returned by the "revoke"
// op. Revoked is always true when the op succeeds.
type RevokeResult struct {
	Revoked bool `json:"revoked"`
}

// ResetConfidenceResult is the synchronous acknowledgement returned by the
// "resetConfidence" op. WindowsDeleted is how many `__effect` confidence
// windows the reset removed — zero is a success (nothing to drain), and a
// window a concurrent booking changed mid-pass is skipped rather than
// clobbered, so a rerun can report more.
type ResetConfidenceResult struct {
	WindowsDeleted int `json:"windowsDeleted"`
}

// listOp and the disable/enable/revoke/resetConfidence per-target ops
// registered as individual NATS Services endpoints.
const (
	opList            = "list"
	opDisable         = "disable"
	opEnable          = "enable"
	opRevoke          = "revoke"
	opResetConfidence = "resetConfidence"
)

// targetOps enumerates the per-target (wildcard-subject) ops registered
// under subjectPrefix+".*.<op>".
var targetOps = []string{opDisable, opEnable, opRevoke, opResetConfidence}

// Service is the Weaver control-plane NATS responder. It wraps an
// engineControl (in production, *weaver.Engine) and a CapabilityChecker.
// Production wires the real controlauth.CapabilityKVChecker (cmd/weaver
// aborts startup if that construction fails); a nil checker falls back to the
// fail-closed denyAllChecker, which denies every operation.
type Service struct {
	engine     engineControl
	capability CapabilityChecker
	logger     *slog.Logger

	mu       sync.Mutex
	microSvc micro.Service // set by StartNATSListener; nil until started
	// actorVerifier lifts HeaderActor from self-asserted to a verified JWT
	// (Fire 2, control-plane-capability-authz-design.md). nil (the default)
	// keeps Fire 1 behavior; set via SetActorVerifier.
	actorVerifier *controlauth.ActorVerifier
}

// NewService constructs a Service wrapping engine. capability may be nil — the
// fail-closed denyAllChecker is used in that case (deny every op + loud Warn),
// so a nil/misconfigured checker fails closed rather than allowing. Pass an
// explicit StubCapabilityChecker for dev/test allow-all. logger may be nil —
// slog's default logger is used in that case.
func NewService(engine engineControl, capability CapabilityChecker, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if capability == nil {
		capability = newDenyAllChecker(logger)
	}
	return &Service{engine: engine, capability: capability, logger: logger}
}

// SetActorVerifier wires JWT verification onto the control plane's
// HeaderActor value (Fire 2). Thread-safe; may be called at any time before
// or after StartNATSListener. A nil verifier (the default) preserves Fire
// 1's self-asserted-header behavior.
func (s *Service) SetActorVerifier(v *controlauth.ActorVerifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actorVerifier = v
}

// resolveActor reads the current actorVerifier under lock and resolves req's
// actor against it (nil verifier = Fire 1 self-asserted passthrough).
func (s *Service) resolveActor(ctx context.Context, req micro.Request) (string, error) {
	s.mu.Lock()
	v := s.actorVerifier
	s.mu.Unlock()
	return controlauth.ResolveActor(ctx, req, v)
}

// StartNATSListener registers the Weaver control plane as a NATS
// micro-service named "weaver-control". Four endpoints are added: "list" on
// the exact subject subjectPrefix+".list", and "disable"/"enable"/"revoke"
// on the wildcard subjectPrefix+".*.<op>" so a single handler instance
// serves every target ID without prior knowledge.
//
// All endpoints share the default queue group ("q") so multiple Weaver
// instances distribute load. The service framework auto-registers the
// standard $SRV.PING / $SRV.STATS / $SRV.INFO introspection endpoints.
//
// The service is stopped when ctx is cancelled. Returns an error if the
// service cannot be created or if already started.
func (s *Service) StartNATSListener(ctx context.Context, nc *nats.Conn) error {
	s.mu.Lock()
	if s.microSvc != nil {
		s.mu.Unlock()
		return fmt.Errorf("control: NATS listener already started")
	}
	s.mu.Unlock()

	svc, err := micro.AddService(nc, micro.Config{
		Name:        "weaver-control",
		Version:     "1.0.0",
		Description: "Weaver control plane endpoints (lattice.ctrl.weaver.*)",
	})
	if err != nil {
		return fmt.Errorf("control: micro.AddService: %w", err)
	}

	if err := svc.AddEndpoint("weaver-control-"+opList,
		micro.HandlerFunc(func(req micro.Request) { s.handleList(req) }),
		micro.WithEndpointSubject(subjectPrefix+"."+opList)); err != nil {
		_ = svc.Stop()
		return fmt.Errorf("control: AddEndpoint %q: %w", opList, err)
	}

	for _, op := range targetOps {
		op := op // capture for closure
		subj := subjectPrefix + ".*." + op
		if err := svc.AddEndpoint("weaver-control-"+op,
			micro.HandlerFunc(func(req micro.Request) { s.dispatchEndpoint(op, req) }),
			micro.WithEndpointSubject(subj)); err != nil {
			_ = svc.Stop()
			return fmt.Errorf("control: AddEndpoint %q on %q: %w", op, subj, err)
		}
	}

	s.mu.Lock()
	s.microSvc = svc
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		if err := svc.Stop(); err != nil {
			s.logger.Error("control: stop micro service", "err", err)
		}
	}()
	return nil
}

// handleList serves the "list" op, registered on the exact subject
// subjectPrefix+".list" (no targetID in the subject).
func (s *Service) handleList(req micro.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	actor, aerr := s.resolveActor(ctx, req)
	if aerr != nil {
		s.respondMicro(req, ControlResponse{Error: aerr.Error()})
		return
	}
	if err := s.capability.Authorize(ctx, actor, opList, ""); err != nil {
		s.respondMicro(req, ControlResponse{Error: err.Error()})
		return
	}
	targets, err := s.engine.ListTargets(ctx)
	if err != nil {
		s.respondMicro(req, ControlResponse{Error: err.Error()})
		return
	}
	s.respondMicro(req, ControlResponse{Targets: targets})
}

// dispatchEndpoint is the entry point for the disable/enable/revoke/
// resetConfidence endpoints. It extracts the target ID from the subject, authorizes the
// operation, dispatches by op, and writes the JSON response.
func (s *Service) dispatchEndpoint(op string, req micro.Request) {
	subject := req.Subject()
	targetID, ok := targetIDFromSubject(subject)
	if !ok {
		s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("invalid control subject %q", subject)})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	actor, aerr := s.resolveActor(ctx, req)
	if aerr != nil {
		s.respondMicro(req, ControlResponse{Error: aerr.Error()})
		return
	}
	if err := s.capability.Authorize(ctx, actor, op, targetID); err != nil {
		s.respondMicro(req, ControlResponse{Error: err.Error()})
		return
	}

	switch op {
	case opDisable:
		if err := s.engine.Disable(ctx, targetID); err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Disable: &DisableResult{Disabled: true}})
	case opEnable:
		if err := s.engine.Enable(ctx, targetID); err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Enable: &EnableResult{Enabled: true}})
	case opRevoke:
		if err := s.engine.Revoke(ctx, targetID); err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Revoke: &RevokeResult{Revoked: true}})
	case opResetConfidence:
		deleted, err := s.engine.ResetConfidence(ctx, targetID)
		if err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{
			ResetConfidence: &ResetConfidenceResult{WindowsDeleted: deleted}})
	default:
		// Unreachable — targetOps gates the endpoint registration.
		s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("unknown operation: %s", op)})
	}
}

// targetIDFromSubject extracts the target ID from a control subject. The
// expected shape is "lattice.ctrl.weaver.<targetId>.<op>" — exactly 5
// dot-separated tokens. Returns ok=false on any deviation. Mirrors
// internal/refractor/control.lensIDFromSubject.
func targetIDFromSubject(subject string) (string, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 5 {
		return "", false
	}
	if parts[0] != "lattice" || parts[1] != "ctrl" || parts[2] != "weaver" {
		return "", false
	}
	if parts[3] == "" {
		return "", false
	}
	return parts[3], true
}

// ListSubject returns the canonical request subject for the "list" op.
// Exposed for tests and tooling.
func ListSubject() string {
	return subjectPrefix + "." + opList
}

// TargetSubject returns the canonical request subject for the given target
// ID + op (disable/enable/revoke). Exposed for tests and tooling.
func TargetSubject(targetID, op string) string {
	return subjectPrefix + "." + targetID + "." + op
}

// respondMicro marshals v to JSON and sends it as the micro reply. On a
// marshal failure it still sends a structured error reply (never returns
// silently), so the client sees an error rather than a request timeout.
func (s *Service) respondMicro(req micro.Request, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.logger.Error("control: marshal response", "err", err)
		// Marshal a minimal hand-built error envelope — a plain
		// {"error":...} string cannot itself fail to marshal.
		fallback, fErr := json.Marshal(ControlResponse{Error: "control: failed to marshal response: " + err.Error()})
		if fErr != nil {
			// Should be unreachable (a string-only struct). Reply with a raw
			// literal so the client still gets a response, not a timeout.
			fallback = []byte(`{"error":"control: response marshal failure"}`)
		}
		if rErr := req.Respond(fallback); rErr != nil {
			s.logger.Error("control: send error response", "err", rErr)
		}
		return
	}
	if err := req.Respond(data); err != nil {
		s.logger.Error("control: send response", "err", err)
	}
}
