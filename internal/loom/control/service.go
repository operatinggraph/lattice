// Package control implements the Loom control plane: a NATS Services
// responder exposing list/consumers/inspect/pause/resume operator commands for
// Loom instances and managed consumers, plus the cmd/lattice/loom CLI's server
// side. Depends on internal/loom only for the five engineControl methods and the
// loom summary types — internal/loom never imports this package (one-way
// dependency, mirrors internal/weaver/control being a sibling of internal/weaver
// and internal/refractor/control a sibling of internal/refractor/{pipeline,...}).
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/loom"
)

// engineControl is the minimal interface this package needs from *loom.Engine —
// the read ops (list/consumers/inspect) and the two safe consumer toggles
// (pause/resume). Defining it here (rather than depending on the full
// *loom.Engine) keeps the dependency surface to exactly these five methods plus
// the loom summary types.
type engineControl interface {
	ListInstances(ctx context.Context) ([]loom.InstanceSummary, error)
	ListConsumers(ctx context.Context) ([]loom.ConsumerStatus, error)
	InspectInstance(ctx context.Context, instanceID string) (loom.InstanceDetail, error)
	PauseConsumer(ctx context.Context, name string) (string, error)
	ResumeConsumer(ctx context.Context, name string) error
}

// subjectPrefix is the subject namespace the control endpoints are registered
// under. "list" and "consumers" are registered on the exact subjects
// subjectPrefix+".list" / subjectPrefix+".consumers"; inspect/pause/resume are
// registered on subjectPrefix+".*.<op>" — a single-token wildcard for the
// instance/consumer name lets one endpoint handler serve every name, since Loom
// does not know the full set of instance ids / consumer names at registration
// time.
const subjectPrefix = "lattice.ctrl.loom"

// handlerTimeout bounds each control handler's engine call so a blocked KV op
// (KV unavailable, a slow list/get loop) fails the request with an error reply
// instead of wedging the responder goroutine and leaving the operator's CLI to
// time out with no server-side cancellation.
const handlerTimeout = 5 * time.Second

// ControlResponse is the JSON payload returned by the control service.
// On success (list op): Instances is present.
// On success (consumers op): Consumers is present.
// On success (inspect op): Instance is present.
// On success (pause op): Pause is present.
// On success (resume op): Resume is present.
// On error: only Error is present.
type ControlResponse struct {
	Instances []loom.InstanceSummary `json:"instances,omitempty"`
	Consumers []loom.ConsumerStatus  `json:"consumers,omitempty"`
	Instance  *loom.InstanceDetail   `json:"instance,omitempty"`
	Pause     *PauseResult           `json:"pause,omitempty"`
	Resume    *ResumeResult          `json:"resume,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

// PauseResult is the synchronous acknowledgement returned by the "pause" op.
// Paused is always true when the op succeeds. Note is the advisory text the
// engine composes for the paused consumer: always the persistence contract (a
// manual pause is sticky across an engine restart, health-kv backed, until an
// explicit resume), plus — for a per-domain completion consumer — a warning that
// in-flight instances awaiting that domain stall until resume.
type PauseResult struct {
	Paused bool   `json:"paused"`
	Note   string `json:"note,omitempty"`
}

// ResumeResult is the synchronous acknowledgement returned by the "resume" op.
// Resumed is always true when the op succeeds.
type ResumeResult struct {
	Resumed bool `json:"resumed"`
}

// The op tokens registered as NATS Services endpoints. list and consumers are
// exact-subject; inspect/pause/resume are per-name wildcard-subject ops.
const (
	opList      = "list"
	opConsumers = "consumers"
	opInspect   = "inspect"
	opPause     = "pause"
	opResume    = "resume"
)

// exactOps enumerates the exact-subject ops registered under
// subjectPrefix+".<op>" (no name token in the subject).
var exactOps = []string{opList, opConsumers}

// nameOps enumerates the per-name (wildcard-subject) ops registered under
// subjectPrefix+".*.<op>".
var nameOps = []string{opInspect, opPause, opResume}

// Service is the Loom control-plane NATS responder. It wraps an engineControl
// (in production, *loom.Engine) and a CapabilityChecker. Production wires the
// real controlauth.CapabilityKVChecker (cmd/loom aborts startup if that
// construction fails); a nil checker falls back to the fail-closed
// denyAllChecker, which denies every operation.
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

// StartNATSListener registers the Loom control plane as a NATS micro-service
// named "loom-control". Endpoints: "list" and "consumers" on the exact subjects
// subjectPrefix+".list" / subjectPrefix+".consumers", and "inspect"/"pause"/
// "resume" on the wildcard subjectPrefix+".*.<op>" so a single handler instance
// serves every instance id / consumer name without prior knowledge.
//
// All endpoints share the default queue group ("q") so multiple Loom instances
// distribute load. The service framework auto-registers the standard
// $SRV.PING / $SRV.STATS / $SRV.INFO introspection endpoints.
//
// The service is stopped when ctx is cancelled. Returns an error if the service
// cannot be created or if already started.
func (s *Service) StartNATSListener(ctx context.Context, nc *nats.Conn) error {
	s.mu.Lock()
	if s.microSvc != nil {
		s.mu.Unlock()
		return fmt.Errorf("control: NATS listener already started")
	}
	s.mu.Unlock()

	svc, err := micro.AddService(nc, micro.Config{
		Name:        "loom-control",
		Version:     "1.0.0",
		Description: "Loom control plane endpoints (lattice.ctrl.loom.*)",
	})
	if err != nil {
		return fmt.Errorf("control: micro.AddService: %w", err)
	}

	for _, op := range exactOps {
		op := op // capture for closure
		if err := svc.AddEndpoint("loom-control-"+op,
			micro.HandlerFunc(func(req micro.Request) { s.handleExact(op, req) }),
			micro.WithEndpointSubject(subjectPrefix+"."+op)); err != nil {
			_ = svc.Stop()
			return fmt.Errorf("control: AddEndpoint %q: %w", op, err)
		}
	}

	for _, op := range nameOps {
		op := op // capture for closure
		subj := subjectPrefix + ".*." + op
		if err := svc.AddEndpoint("loom-control-"+op,
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

// handleExact serves the exact-subject ops (list / consumers), which carry no
// name token in the subject.
func (s *Service) handleExact(op string, req micro.Request) {
	defer s.recoverHandler(op, req)
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	actor, aerr := s.resolveActor(ctx, req)
	if aerr != nil {
		s.respondMicro(req, ControlResponse{Error: aerr.Error()})
		return
	}
	if err := s.capability.Authorize(ctx, actor, op, ""); err != nil {
		s.respondMicro(req, ControlResponse{Error: err.Error()})
		return
	}
	switch op {
	case opList:
		instances, err := s.engine.ListInstances(ctx)
		if err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Instances: instances})
	case opConsumers:
		consumers, err := s.engine.ListConsumers(ctx)
		if err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Consumers: consumers})
	default:
		// Unreachable — exactOps gates the endpoint registration.
		s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("unknown operation: %s", op)})
	}
}

// dispatchEndpoint is the entry point for the per-name endpoints (inspect /
// pause / resume). It extracts the name from the subject, authorizes the
// operation, dispatches by op, and writes the JSON response.
func (s *Service) dispatchEndpoint(op string, req micro.Request) {
	defer s.recoverHandler(op, req)
	subject := req.Subject()
	name, ok := nameFromSubject(subject)
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
	if err := s.capability.Authorize(ctx, actor, op, name); err != nil {
		s.respondMicro(req, ControlResponse{Error: err.Error()})
		return
	}

	switch op {
	case opInspect:
		detail, err := s.engine.InspectInstance(ctx, name)
		if err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Instance: &detail})
	case opPause:
		note, err := s.engine.PauseConsumer(ctx, name)
		if err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Pause: &PauseResult{Paused: true, Note: note}})
	case opResume:
		if err := s.engine.ResumeConsumer(ctx, name); err != nil {
			s.respondMicro(req, ControlResponse{Error: err.Error()})
			return
		}
		s.respondMicro(req, ControlResponse{Resume: &ResumeResult{Resumed: true}})
	default:
		// Unreachable — nameOps gates the endpoint registration.
		s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("unknown operation: %s", op)})
	}
}

// recoverHandler is the last-resort panic guard for a control handler. The micro
// framework runs each handler in the NATS async-subscription goroutine and does
// NOT recover panics, so an unrecovered panic in a handler (or in the engine call
// it makes) would crash the whole Loom process. This converts a recovered panic
// into a structured error reply — the operator's request fails cleanly instead of
// taking the process down — and logs it (with a stack) at error level. There is
// no reachable panic on the current paths; this is defence in depth.
func (s *Service) recoverHandler(op string, req micro.Request) {
	r := recover()
	if r == nil {
		return
	}
	s.logger.Error("control: recovered panic in handler",
		"op", op, "subject", req.Subject(), "panic", r, "stack", string(debug.Stack()))
	s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("control: internal error handling %q", op)})
}

// nameFromSubject extracts the instance id / consumer name from a per-name
// control subject. The expected shape is "lattice.ctrl.loom.<name>.<op>" —
// exactly 5 dot-separated tokens. Returns ok=false on any deviation. This parse
// is necessary but not sufficient: it does not by itself reject crafted names
// like "*" or ">" (a literal token survives the 5-token split) — safety rests on
// the downstream not-found (inspect) and not-managed (pause/resume) guards in the
// engine. Mirrors internal/weaver/control.targetIDFromSubject.
func nameFromSubject(subject string) (string, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 5 {
		return "", false
	}
	if parts[0] != "lattice" || parts[1] != "ctrl" || parts[2] != "loom" {
		return "", false
	}
	if parts[3] == "" {
		return "", false
	}
	return parts[3], true
}

// ListSubject returns the canonical request subject for the "list" op. Exposed
// for tests and tooling.
func ListSubject() string {
	return subjectPrefix + "." + opList
}

// ConsumersSubject returns the canonical request subject for the "consumers" op.
// Exposed for tests and tooling.
func ConsumersSubject() string {
	return subjectPrefix + "." + opConsumers
}

// NameSubject returns the canonical request subject for the given instance id /
// consumer name + op (inspect/pause/resume). Exposed for tests and tooling.
func NameSubject(name, op string) string {
	return subjectPrefix + "." + name + "." + op
}

// respondMicro marshals v to JSON and sends it as the micro reply. On a marshal
// failure it still sends a structured error reply (never returns silently), so
// the client sees an error rather than a request timeout.
func (s *Service) respondMicro(req micro.Request, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		s.logger.Error("control: marshal response", "err", err)
		// Marshal a minimal hand-built error envelope — a plain {"error":...}
		// string cannot itself fail to marshal.
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
