package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/micro"

	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/gateway/auth"
	"github.com/asolgan/lattice/internal/refractor/control/controlwire"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/personalinterest"
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/vault"
)

// ErrRuleNotRegistered is returned by NullifyRow (and other per-ruleID lookups)
// when ruleID has no live registration. Distinguished from a real Delete failure
// so callers can tell "the lens hasn't started yet" (retry-eligible) apart from
// "the row could not be nullified" (privacy-critical, no retry).
var ErrRuleNotRegistered = errors.New("control: rule not registered")

// validateTimeout bounds the total time allowed for the validate op (NFR10 — CI-practical latency).
const validateTimeout = 5 * time.Second

// hydrateTimeout bounds the "hydrate" op (personal-secure-lens-design.md
// §3.5, Fire PL.4): a cold bulk projection scoped to one identity's slice,
// larger than a single KV read/write (register/deregister) but bounded to
// one actor rather than a whole-lens rebuild.
const hydrateTimeout = 30 * time.Second

// sessionKeyTimeout bounds the "sessionkey" op (edge-lattice-full-design.md
// §3.6, EDGE.4): one piiKey point-read plus one Vault crypto call, closer in
// shape to register/deregister than to the bulk hydrate op.
const sessionKeyTimeout = 10 * time.Second

// authorizeTimeout bounds the capability check every dispatchEndpoint op runs
// before dispatch (a single Capability KV GET, mirrors Weaver/Loom control's
// handlerTimeout).
const authorizeTimeout = 5 * time.Second

// Resumer is implemented by any component that can be unblocked from a structural or manual pause.
// *pipeline.Pipeline satisfies this interface via its Resume method.
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Resumer interface {
	Resume(ctx context.Context)
}

// Pauser is implemented by any component that can be manually paused.
// *pipeline.Pipeline satisfies this interface via its Pause method.
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Pauser interface {
	Pause(ctx context.Context)
}

// RuleGetter is a read-only interface for looking up loaded rules by ID.
// *lens.CoreKVSource satisfies this via its Get method (formerly *lens.Loader).
type RuleGetter interface {
	Get(ruleID string) (*lens.Rule, bool)
}

// coreKVGetter is the single point-read the "sessionkey" op needs from Core
// KV (the identity's piiKey aspect) — mirrors
// internal/refractor/pipeline.coreKVGetter; duplicated rather than imported
// so this package does not depend on internal/refractor/pipeline (the same
// architecture boundary Resumer/Pauser/Hydrator above already keep).
// *substrate.KV satisfies it; tests inject a fake.
type coreKVGetter interface {
	Get(ctx context.Context, key string) (*substrate.KVEntry, error)
}

// Rebuilder is implemented by any component that can perform an in-place rebuild
// of the rule's target store. *pipeline.Pipeline satisfies this via its Rebuild method.
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Rebuilder interface {
	Rebuild(ctx context.Context, truncate bool) error
}

// Deleter is implemented by any component that can cleanly stop a rule and remove
// its associated NATS resources. Typically implemented as an orchestrator closure that:
//  1. Cancels the pipeline's run context and waits for Run() to return.
//  2. Removes the rule's durable consumer (the pipeline's supervisor owns it).
//  3. Calls health.Reporter.Delete(ctx) to remove the health KV entry.
//
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type Deleter interface {
	Delete(ctx context.Context) error
}

// Hydrator is implemented by any component that can run a cold bulk
// projection for one identity and return the high-water revision it was
// projected through (personal-secure-lens-design.md §3.5, Fire PL.4).
// *pipeline.Pipeline satisfies this via its Hydrate method. Defined here so
// internal/control does not import internal/pipeline (architecture boundary).
type Hydrator interface {
	Hydrate(ctx context.Context, identityID string) (revision uint64, err error)
}

// RowNullifier is implemented by any component that can remove ONE projected row
// (by its Into.Key values) from a rule's target store, independent of the rule's
// own CDC stream. *pipeline.Pipeline satisfies this via its Delete method (the
// same adapter.Delete path a vertex tombstone takes). Used by the Refractor
// KeyShredded nullification listener (vault-crypto-shredding-design.md §2.4) to
// scrub a shredded identity's row out-of-band.
// Defined here so internal/control does not import internal/pipeline (architecture boundary).
type RowNullifier interface {
	Delete(ctx context.Context, keys map[string]any, projectionSeq uint64) error
}

// The control-plane wire format is defined in
// internal/refractor/control/controlwire, a leaf package that depends on
// nothing but the standard library. It is re-exported here because it reads as
// control vocabulary at every server-side call site, and because importing this
// package means linking a NATS client + the micro service — which a
// browser-hosted Edge engine (edge-browser-node-design.md §3.2) must not do to
// call an endpoint through the Gateway. Code that needs only the payloads
// imports internal/refractor/control/controlwire directly.
//
// These are aliases, not new types: a controlwire.ControlRequest IS a
// control.ControlRequest, so the Edge client and this service marshal one
// definition rather than two that can drift (edge-lattice-full-design.md §8.1
// RR-4).

// ControlRequest is the JSON payload sent to control endpoints.
type ControlRequest = controlwire.ControlRequest

// ControlResponse is the JSON payload returned by the control service.
type ControlResponse = controlwire.ControlResponse

// RebuildResult is the async acknowledgement returned by the "rebuild" op.
type RebuildResult = controlwire.RebuildResult

// PauseResult is the synchronous acknowledgement returned by the "pause" op.
type PauseResult = controlwire.PauseResult

// ResumeResult is the synchronous acknowledgement returned by the "resume" op.
type ResumeResult = controlwire.ResumeResult

// DeleteResult is the synchronous acknowledgement returned by the "delete" op.
type DeleteResult = controlwire.DeleteResult

// PersonalRegisterResult is the acknowledgement returned by the "register" op.
type PersonalRegisterResult = controlwire.PersonalRegisterResult

// PersonalDeregisterResult is the acknowledgement returned by the "deregister" op.
type PersonalDeregisterResult = controlwire.PersonalDeregisterResult

// PersonalHydrateResult is the acknowledgement returned by the "hydrate" op.
type PersonalHydrateResult = controlwire.PersonalHydrateResult

// PersonalSessionKeyResult is the acknowledgement returned by the "sessionkey" op.
type PersonalSessionKeyResult = controlwire.PersonalSessionKeyResult

// ValidateResult is returned by the "validate" op.
type ValidateResult = controlwire.ValidateResult

// FieldReport describes the presence of one referenced field in the Core KV sample.
type FieldReport = controlwire.FieldReport

// Service coordinates control operations (pause, resume, health query, validate, rebuild, delete).
// It maintains a registry of active pipeline interfaces keyed by ruleID.
// The orchestrator (cmd/refractor) registers each pipeline when it starts and unregisters it when it stops.
//
// Zero-downtime migration pattern (FR32): two rules with different IDs may run simultaneously.
// Register both rules before cutting over application traffic, then delete the old rule when
// the migration is complete and correctness has been verified.
// CONSTRAINT: two rules targeting the same table is undefined behavior — write order across
// independent pipelines is non-deterministic. Only rules targeting different tables may safely
// run in parallel.
type Service struct {
	mu                   sync.Mutex
	resumerByRuleID      map[string]Resumer
	pauserByRuleID       map[string]Pauser
	rebuilderByRuleID    map[string]Rebuilder
	deleterByRuleID      map[string]Deleter
	rowNullifierByRuleID map[string]RowNullifier
	reporters            map[string]*health.Reporter
	microSvc             micro.Service // set by StartNATSListener; nil until started
	ruleGetter           RuleGetter    // set via SetRuleGetter; used by validate op
	// personalInterestKV backs the "register"/"deregister" ops (Personal Lens
	// Interest Set, personal-secure-lens-design.md §3.3). nil until
	// SetPersonalInterestKV is called; those two ops fail closed until then.
	personalInterestKV *substrate.KV
	// personalHydrator backs the "hydrate" op (personal-secure-lens-design.md
	// §3.5, Fire PL.4). nil until SetPersonalHydrator is called; the op fails
	// closed until then. There is exactly one Personal Lens pipeline per
	// deployment, so — like personalInterestKV — this is a single handle, not
	// a per-ruleID registry.
	personalHydrator Hydrator
	// coreKV backs the "sessionkey" op's piiKey envelope point-read
	// (edge-lattice-full-design.md §3.6, EDGE.4). nil until SetCoreKV is
	// called; the op fails closed until then.
	coreKV coreKVGetter
	// vault backs the "sessionkey" op's IssueSessionKey call. nil until
	// SetVault is called (a deployment without a configured Vault backend
	// leaves EDGE.4 unreachable, same posture as a lens with SecureColumns
	// and no vaultBackend); the op fails closed until then.
	vault vault.Vault
	// capability authorizes every dispatchEndpoint op (FR30,
	// control-plane-capability-authz-design.md). Defaults to the fail-closed
	// denyAllChecker (deny-all + loud Warn) so the pre-set window fails closed;
	// SetCapabilityChecker swaps in the real controlauth checker without
	// touching handler bodies (cmd/refractor sets it before serving).
	capability CapabilityChecker
	// actorVerifier lifts HeaderActor from self-asserted to a verified JWT
	// (Fire 2, control-plane-capability-authz-design.md). nil (the default)
	// keeps Fire 1 behavior; set via SetActorVerifier.
	actorVerifier *controlauth.ActorVerifier
}

// NewService creates a new Service with empty registries. The control plane
// starts fail-closed (a denyAllChecker that denies every op with a loud Warn);
// call SetCapabilityChecker to wire real Capability KV enforcement before
// serving. The fail-closed default governs only the pre-set window / a
// misconfiguration, never normal production traffic.
func NewService() *Service {
	return &Service{
		resumerByRuleID:      make(map[string]Resumer),
		pauserByRuleID:       make(map[string]Pauser),
		rebuilderByRuleID:    make(map[string]Rebuilder),
		deleterByRuleID:      make(map[string]Deleter),
		rowNullifierByRuleID: make(map[string]RowNullifier),
		reporters:            make(map[string]*health.Reporter),
		capability:           newDenyAllChecker(nil),
	}
}

// SetActorVerifier wires JWT verification onto the control plane's
// HeaderActor value (Fire 2). Thread-safe; may be called at any time. A nil
// verifier (the default) preserves Fire 1's self-asserted-header behavior.
// dispatchEndpoint reads it under the same lock as capability (both are
// needed together at that single call site).
func (s *Service) SetActorVerifier(v *controlauth.ActorVerifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actorVerifier = v
}

// SetCapabilityChecker registers the capability checker every control op is
// authorized against. Thread-safe; may be called at any time. A nil checker
// resets to the fail-closed denyAllChecker (deny-all + loud Warn) rather than
// leaving the field nil (dispatchEndpoint calls it unconditionally) — a
// cleared checker must fail closed, never open.
func (s *Service) SetCapabilityChecker(c CapabilityChecker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c == nil {
		c = newDenyAllChecker(nil)
	}
	s.capability = c
}

// SetRuleGetter registers the rule lookup interface used by the validate op.
// *lens.CoreKVSource satisfies RuleGetter. Thread-safe; may be called at any time.
func (s *Service) SetRuleGetter(rg RuleGetter) {
	s.mu.Lock()
	s.ruleGetter = rg
	s.mu.Unlock()
}

// SetPersonalInterestKV registers the personal-lens-interest KV handle the
// "register"/"deregister" ops write through. Thread-safe; may be called at
// any time. Until called, both ops fail closed with an error response.
func (s *Service) SetPersonalInterestKV(kv *substrate.KV) {
	s.mu.Lock()
	s.personalInterestKV = kv
	s.mu.Unlock()
}

// SetPersonalHydrator registers the Hydrator the "hydrate" op dispatches to
// (personal-secure-lens-design.md §3.5, Fire PL.4). Thread-safe; may be
// called at any time. Until called, the op fails closed with an error
// response. Pass nil to clear (e.g. on lens teardown).
func (s *Service) SetPersonalHydrator(h Hydrator) {
	s.mu.Lock()
	s.personalHydrator = h
	s.mu.Unlock()
}

// SetCoreKV registers the Core KV handle the "sessionkey" op reads the
// requesting identity's piiKey envelope through (edge-lattice-full-design.md
// §3.6, EDGE.4). Thread-safe; may be called at any time. Until called, the
// op fails closed with an error response.
func (s *Service) SetCoreKV(kv coreKVGetter) {
	s.mu.Lock()
	s.coreKV = kv
	s.mu.Unlock()
}

// SetVault registers the Vault backend the "sessionkey" op mints transient
// session keys through. Thread-safe; may be called at any time. Until
// called, the op fails closed with an error response. Callers must not pass
// a nil-valued concrete pointer wrapped in a non-nil vault.Vault interface
// (check the concrete pointer for nil before calling, as cmd/refractor does
// for vaultBackend) — SetVault(nil) is the only way to clear it.
func (s *Service) SetVault(v vault.Vault) {
	s.mu.Lock()
	s.vault = v
	s.mu.Unlock()
}

// Register records a Resumer and health.Reporter for the given ruleID.
// Overwrites any prior registration for the same ruleID (safe for hot-reload).
// Panics if r is nil — a nil Resumer would cause a runtime panic in ResumeRule.
func (s *Service) Register(ruleID string, r Resumer, reporter *health.Reporter) {
	if r == nil {
		panic("control: Register: Resumer must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumerByRuleID[ruleID] = r
	if reporter != nil {
		s.reporters[ruleID] = reporter
	}
}

// Unregister removes all registry entries (Resumer, Pauser, Rebuilder, Deleter,
// RowNullifier, Reporter) for ruleID. No-op for any map that does not contain ruleID.
func (s *Service) Unregister(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.resumerByRuleID, ruleID)
	delete(s.pauserByRuleID, ruleID)
	delete(s.rebuilderByRuleID, ruleID)
	delete(s.deleterByRuleID, ruleID)
	delete(s.rowNullifierByRuleID, ruleID)
	delete(s.reporters, ruleID)
}

// RegisterRebuilder records a Rebuilder for the given ruleID.
// Overwrites any prior registration (safe for hot-reload).
// Panics if r is nil.
func (s *Service) RegisterRebuilder(ruleID string, r Rebuilder) {
	if r == nil {
		panic("control: RegisterRebuilder: Rebuilder must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuilderByRuleID[ruleID] = r
}

// UnregisterRebuilder removes the Rebuilder entry for ruleID.
// No-op if ruleID is not registered.
func (s *Service) UnregisterRebuilder(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rebuilderByRuleID, ruleID)
}

// RegisterPauser records a Pauser for the given ruleID.
// Overwrites any prior registration (safe for hot-reload).
// Panics if p is nil.
func (s *Service) RegisterPauser(ruleID string, p Pauser) {
	if p == nil {
		panic("control: RegisterPauser: Pauser must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pauserByRuleID[ruleID] = p
}

// UnregisterPauser removes the Pauser entry for ruleID.
// No-op if ruleID is not registered.
func (s *Service) UnregisterPauser(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pauserByRuleID, ruleID)
}

// RegisterDeleter records a Deleter for the given ruleID.
// Overwrites any prior registration (safe for hot-reload).
// Panics if d is nil.
func (s *Service) RegisterDeleter(ruleID string, d Deleter) {
	if d == nil {
		panic("control: RegisterDeleter: Deleter must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleterByRuleID[ruleID] = d
}

// UnregisterDeleter removes the Deleter entry for ruleID.
// No-op if ruleID is not registered.
func (s *Service) UnregisterDeleter(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.deleterByRuleID, ruleID)
}

// RegisterRowNullifier records a RowNullifier for the given ruleID.
// Overwrites any prior registration (safe for hot-reload).
// Panics if n is nil.
func (s *Service) RegisterRowNullifier(ruleID string, n RowNullifier) {
	if n == nil {
		panic("control: RegisterRowNullifier: RowNullifier must not be nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rowNullifierByRuleID[ruleID] = n
}

// UnregisterRowNullifier removes the RowNullifier entry for ruleID.
// No-op if ruleID is not registered.
func (s *Service) UnregisterRowNullifier(ruleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rowNullifierByRuleID, ruleID)
}

// ResumeRule unblocks a structural pause for the given ruleID.
// Returns an error if ruleID is not registered.
// Pipeline.Resume sets health KV to active internally; this method does not touch health KV directly.
func (s *Service) ResumeRule(ctx context.Context, ruleID string) error {
	s.mu.Lock()
	r, ok := s.resumerByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("control: rule %q not registered", ruleID)
	}
	r.Resume(ctx)
	return nil
}

// PauseRule halts the given ruleID's fetch loop (FR30 control surface), the
// in-process equivalent of the "pause" control-plane op. Used by the KeyShredded
// nullification listener to halt a lens on a privacy-critical failure — the
// pause is unconditional (no automatic resume): an operator must investigate and
// call ResumeRule/the "resume" op once the row is verified scrubbed.
// Returns an error if ruleID is not registered. Pipeline.Pause sets health KV to
// paused/manual internally; this method does not touch health KV directly.
func (s *Service) PauseRule(ctx context.Context, ruleID string) error {
	s.mu.Lock()
	p, ok := s.pauserByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("control: rule %q: %w", ruleID, ErrRuleNotRegistered)
	}
	p.Pause(ctx)
	return nil
}

// NullifyRow deletes ONE projected row (by its Into.Key values) from ruleID's
// target store via its registered RowNullifier (*pipeline.Pipeline.Delete — the
// same adapter.Delete path a vertex tombstone takes). Returns an error if ruleID
// is not registered as a RowNullifier, or if the underlying Delete fails.
func (s *Service) NullifyRow(ctx context.Context, ruleID string, keys map[string]any, projectionSeq uint64) error {
	s.mu.Lock()
	n, ok := s.rowNullifierByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("control: rule %q: %w", ruleID, ErrRuleNotRegistered)
	}
	return n.Delete(ctx, keys, projectionSeq)
}

// controlSubjectPrefix is the wildcard subject pattern the control
// endpoints are registered under. Each endpoint adds two trailing
// tokens: <lensId>.<op>. Wildcards in micro endpoint subjects let one
// endpoint handler serve all lens IDs — necessary because the Refractor
// does not know the full set of lens IDs at startup (handoff brief
// Decision #6).
const controlSubjectPrefix = controlwire.SubjectPrefix

// supportedOps enumerates the per-op endpoint suffixes registered under
// the NATS Services framework. The op name is taken from the trailing
// subject token; see opFromSubject.
var supportedOps = []string{"health", "validate", "rebuild", "pause", "resume", "delete", "register", "deregister", "hydrate", "sessionkey"}

// StartNATSListener registers the Refractor control plane as a NATS
// micro-service named "refractor-control". One endpoint is added per
// supportedOps entry, all sharing the wildcard subject pattern
// "lattice.ctrl.refractor.*.<op>" so a single handler instance serves
// every lens ID without prior knowledge. The "register"/"deregister"/"hydrate"
// ops (Personal Lens Interest Set + hydration) reuse the same wildcard:
// callers address them at the fixed "lattice.ctrl.refractor.personal.<op>"
// subject — "personal" is a pseudo-lensId, not a real lens.
//
// All endpoints share the default queue group ("q") so multiple
// Refractor instances distribute load — replaces the explicit
// "refractor-control" QueueSubscribe group used pre-2.4b.
//
// The service framework auto-registers the standard $SRV.PING /
// $SRV.STATS / $SRV.INFO introspection endpoints. Operators can
// discover the service with `nats micro list` or `$SRV.PING.refractor-control`.
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
		Name:        "refractor-control",
		Version:     "1.0.0",
		Description: "Refractor control plane endpoints (lattice.ctrl.refractor.<lensId>.<op>)",
	})
	if err != nil {
		return fmt.Errorf("control: micro.AddService: %w", err)
	}

	for _, op := range supportedOps {
		op := op // capture for closure
		subj := controlSubjectPrefix + ".*." + op
		err := svc.AddEndpoint(
			"refractor-control-"+op,
			micro.HandlerFunc(func(req micro.Request) { s.dispatchEndpoint(op, req) }),
			micro.WithEndpointSubject(subj),
		)
		if err != nil {
			// Best effort: stop the partially-registered service to
			// avoid leaking subscriptions, then surface the error.
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
			slog.Error("control: stop micro service", "err", err)
		}
	}()
	return nil
}

// dispatchEndpoint is the single entry point for every per-op endpoint.
// It extracts the lens ID from the subject (the second-to-last token),
// authorizes the operation, decodes the request body (legacy ControlRequest
// shape for Truncate support), dispatches by op, and writes the JSON
// response via micro.Request.Respond. Authorization runs BEFORE the body is
// decoded, so an unauthorized caller learns nothing about a malformed body.
func (s *Service) dispatchEndpoint(op string, req micro.Request) {
	subject := req.Subject()
	lensID, ok := lensIDFromSubject(subject)
	if !ok {
		s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("invalid control subject %q", subject)})
		return
	}

	s.mu.Lock()
	capability := s.capability
	verifier := s.actorVerifier
	s.mu.Unlock()

	authCtx, authCancel := context.WithTimeout(context.Background(), authorizeTimeout)
	actor, actorErr := controlauth.ResolveActor(authCtx, req, verifier)
	if actorErr != nil {
		authCancel()
		s.respondMicro(req, ControlResponse{Error: actorErr.Error()})
		return
	}
	authErr := capability.Authorize(authCtx, actor, op, lensID)
	authCancel()
	if authErr != nil {
		s.respondMicro(req, ControlResponse{Error: authErr.Error()})
		return
	}

	// Decode body for op-specific fields (Truncate). Empty body is fine
	// for ops that don't need it (health, pause, resume, delete, validate).
	var body ControlRequest
	if len(req.Data()) > 0 {
		if err := json.Unmarshal(req.Data(), &body); err != nil {
			s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("invalid request: %s", err.Error())})
			return
		}
	}

	// Personal-lens identity binding (per-identity-nats-subscribe-acl-
	// design.md §3.4): once an ActorVerifier is configured, body.IdentityID
	// is a client-asserted payload field the transport ACL cannot fix (the
	// subject is shared across identities) — bind it to the verified actor
	// instead of trusting it. A non-empty body value that disagrees with the
	// verified actor is a client bug, not a routing hint, and is rejected.
	// No verifier configured (dev/e2e fixtures) preserves today's
	// self-asserted-body behavior.
	if verifier != nil {
		switch op {
		case "register", "deregister", "hydrate", "sessionkey":
			boundIdentityID, err := controlauth.BareIdentityID(actor)
			if err != nil {
				s.respondMicro(req, ControlResponse{Error: err.Error()})
				return
			}
			if body.IdentityID != "" && body.IdentityID != boundIdentityID {
				s.respondMicro(req, ControlResponse{Error: "personal: identityId does not match the verified actor"})
				return
			}
			body.IdentityID = boundIdentityID
		}
	}

	switch op {
	case "health":
		s.respondMicro(req, s.getHealth(context.Background(), lensID))
	case "validate":
		ctx, cancel := context.WithTimeout(context.Background(), validateTimeout)
		defer cancel()
		s.respondMicro(req, s.validateRule(ctx, lensID))
	case "rebuild":
		s.respondMicro(req, s.rebuildRule(lensID, body.Truncate))
	case "pause":
		s.respondMicro(req, s.pauseRule(context.Background(), lensID))
	case "resume":
		s.respondMicro(req, s.resumeRule(context.Background(), lensID))
	case "delete":
		s.respondMicro(req, s.deleteRule(context.Background(), lensID))
	case "register":
		s.respondMicro(req, s.personalRegister(context.Background(), body))
	case "deregister":
		s.respondMicro(req, s.personalDeregister(context.Background(), body))
	case "hydrate":
		hydrateCtx, hydrateCancel := context.WithTimeout(context.Background(), hydrateTimeout)
		defer hydrateCancel()
		s.respondMicro(req, s.personalHydrate(hydrateCtx, body))
	case "sessionkey":
		skCtx, skCancel := context.WithTimeout(context.Background(), sessionKeyTimeout)
		defer skCancel()
		s.respondMicro(req, s.personalSessionKey(skCtx, body))
	default:
		// Unreachable — supportedOps gates the endpoint registration.
		s.respondMicro(req, ControlResponse{Error: fmt.Sprintf("unknown operation: %s", op)})
	}
}

// lensIDFromSubject extracts the lens ID from a control subject. The
// expected shape is "lattice.ctrl.refractor.<lensId>.<op>" — exactly 5
// dot-separated tokens. Returns ok=false on any deviation.
func lensIDFromSubject(subject string) (string, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 5 {
		return "", false
	}
	if parts[0] != "lattice" || parts[1] != "ctrl" || parts[2] != "refractor" {
		return "", false
	}
	if parts[3] == "" {
		return "", false
	}
	return parts[3], true
}

// ControlSubject returns the canonical request subject for the given
// lens ID + op. Exposed for tests and tooling.
func ControlSubject(lensID, op string) string { return controlwire.ControlSubject(lensID, op) }

// getHealth returns the health KV entry for ruleID as a ControlResponse.
// Returns an error response if the rule is not registered or the KV read fails.
func (s *Service) getHealth(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	reporter, ok := s.reporters[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	entry, err := reporter.GetStatus(ctx)
	if err != nil {
		return ControlResponse{Error: err.Error()}
	}
	return ControlResponse{Entry: &entry}
}

// rebuildRule is async: it looks up the registered Rebuilder, launches the rebuild in
// a background goroutine, and returns an ack immediately (AC5, AC6).
// Errors from the background rebuild are logged via slog; they do not surface to the caller.
func (s *Service) rebuildRule(ruleID string, truncate bool) ControlResponse {
	s.mu.Lock()
	r, ok := s.rebuilderByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	go func() {
		if err := r.Rebuild(context.Background(), truncate); err != nil {
			slog.Error("control: rebuild failed", "ruleId", ruleID, "err", err)
		}
	}()
	return ControlResponse{Rebuild: &RebuildResult{Started: true}}
}

// pauseRule calls the registered Pauser for ruleID to halt its fetch loop.
// Synchronous: returns an ack after Pause() returns (FR30, AC1, AC5).
func (s *Service) pauseRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	p, ok := s.pauserByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	p.Pause(ctx)
	return ControlResponse{Pause: &PauseResult{Paused: true}}
}

// resumeRule calls the registered Resumer for ruleID to unblock its pause.
// Synchronous: returns an ack after Resume() returns (FR31, AC2, AC6).
func (s *Service) resumeRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	r, ok := s.resumerByRuleID[ruleID]
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	r.Resume(ctx)
	return ControlResponse{Resume: &ResumeResult{Resumed: true}}
}

// deleteRule stops the rule and cleans up its NATS consumer and health KV entry.
// Synchronous: calls d.Delete(ctx) (which cancels the pipeline, removes the consumer,
// and deletes the health KV entry), then calls Unregister to remove all registrations.
// Returns error response if ruleID is not registered or if Delete fails (FR39, AC1, AC4).
//
// The Deleter is removed from deleterByRuleID under the lock BEFORE d.Delete(ctx) is
// called. This prevents a concurrent second deleteRule call from also retrieving the
// Deleter and running a double-delete in parallel — the second call will see nothing in
// the map and return "not registered" immediately.
func (s *Service) deleteRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	d, ok := s.deleterByRuleID[ruleID]
	if ok {
		// Remove now so a concurrent deleteRule for the same ruleID fails fast.
		delete(s.deleterByRuleID, ruleID)
	}
	s.mu.Unlock()
	if !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not registered", ruleID)}
	}
	if err := d.Delete(ctx); err != nil {
		return ControlResponse{Error: fmt.Sprintf("delete %q: %s", ruleID, err.Error())}
	}
	s.Unregister(ruleID) // cleans remaining four registries (deleterByRuleID already cleared above)
	return ControlResponse{Delete: &DeleteResult{Deleted: true}}
}

// personalRegister upserts a device's Personal Lens Interest Set
// (personal-secure-lens-design.md §3.3, Fire PL.2). Fails closed if
// SetPersonalInterestKV hasn't been called, or if identityId/deviceId are
// missing from the request body.
func (s *Service) personalRegister(ctx context.Context, body ControlRequest) ControlResponse {
	s.mu.Lock()
	kv := s.personalInterestKV
	s.mu.Unlock()
	if kv == nil {
		return ControlResponse{Error: "register: personal-lens-interest KV not configured"}
	}
	if body.IdentityID == "" || body.DeviceID == "" {
		return ControlResponse{Error: "register: identityId and deviceId are required"}
	}
	if err := personalinterest.Register(ctx, kv, body.IdentityID, body.DeviceID, body.Types, body.Anchors,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return ControlResponse{Error: err.Error()}
	}
	return ControlResponse{PersonalRegister: &PersonalRegisterResult{Registered: true}}
}

// personalDeregister removes a device's Personal Lens Interest Set
// registration. Fails closed if SetPersonalInterestKV hasn't been called, or
// if identityId/deviceId are missing from the request body.
func (s *Service) personalDeregister(ctx context.Context, body ControlRequest) ControlResponse {
	s.mu.Lock()
	kv := s.personalInterestKV
	s.mu.Unlock()
	if kv == nil {
		return ControlResponse{Error: "deregister: personal-lens-interest KV not configured"}
	}
	if body.IdentityID == "" || body.DeviceID == "" {
		return ControlResponse{Error: "deregister: identityId and deviceId are required"}
	}
	if err := personalinterest.Deregister(ctx, kv, body.IdentityID, body.DeviceID); err != nil {
		return ControlResponse{Error: err.Error()}
	}
	return ControlResponse{PersonalDeregister: &PersonalDeregisterResult{Deregistered: true}}
}

// personalHydrate runs a cold bulk projection for one identity through the
// registered Hydrator (personal-secure-lens-design.md §3.5, Fire PL.4). Fails
// closed if SetPersonalHydrator hasn't been called, or if identityId is
// missing from the request body. On success, if a deviceId was also given and
// the Interest Set KV is configured, best-effort records the resulting
// revision as that device's cursor (personalinterest.SetRevisionCursor) —
// bookkeeping only, not load-bearing for correctness (§3.5), so a cursor-write
// failure is logged but does not fail the op: the data has already been
// delivered.
func (s *Service) personalHydrate(ctx context.Context, body ControlRequest) ControlResponse {
	s.mu.Lock()
	hydrator := s.personalHydrator
	kv := s.personalInterestKV
	s.mu.Unlock()
	if hydrator == nil {
		return ControlResponse{Error: "hydrate: personal hydrator not configured"}
	}
	if body.IdentityID == "" {
		return ControlResponse{Error: "hydrate: identityId is required"}
	}
	revision, err := hydrator.Hydrate(ctx, body.IdentityID)
	if err != nil {
		return ControlResponse{Error: err.Error()}
	}
	if body.DeviceID != "" && kv != nil {
		if cerr := personalinterest.SetRevisionCursor(ctx, kv, body.IdentityID, body.DeviceID, revision,
			time.Now().UTC().Format(time.RFC3339)); cerr != nil {
			slog.Warn("control: hydrate: record revision cursor", "identityId", body.IdentityID,
				"deviceId", body.DeviceID, "err", cerr)
		}
	}
	return ControlResponse{PersonalHydrate: &PersonalHydrateResult{Hydrated: true, Revision: revision}}
}

// personalSessionKey mints a transient Vault session key for the requesting
// identity's own DEK (edge-lattice-full-design.md §3.6, EDGE.4): the Edge
// node's local, in-memory decrypt path for ciphertext deltas. Fails closed
// if SetCoreKV/SetVault haven't been called, if identityId is missing, or if
// the identity has no piiKey aspect (never minted a sensitive aspect). A
// shredded identity's key surfaces vault.ErrKeyShredded's message verbatim —
// the caller's ciphertext deltas are permanent gibberish by design (§3.6
// "Remote Shredding"), not a bug to retry.
//
// body.IdentityID is bound to the verified actor (dispatchEndpoint, above)
// when an ActorVerifier is configured — this handler never trusts a
// client-asserted identity for key custody.
func (s *Service) personalSessionKey(ctx context.Context, body ControlRequest) ControlResponse {
	s.mu.Lock()
	kv := s.coreKV
	v := s.vault
	s.mu.Unlock()
	if kv == nil || v == nil {
		return ControlResponse{Error: "sessionkey: vault not configured"}
	}
	if body.IdentityID == "" {
		return ControlResponse{Error: "sessionkey: identityId is required"}
	}
	identityKey := auth.IdentityKeyPrefix + body.IdentityID

	envelope, err := s.readPiiKeyEnvelope(ctx, identityKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			return ControlResponse{Error: fmt.Sprintf("sessionkey: identity %s has no piiKey aspect (never wrote a sensitive aspect)", identityKey)}
		}
		return ControlResponse{Error: fmt.Sprintf("sessionkey: read piiKey for %s: %s", identityKey, err.Error())}
	}

	sk, err := v.IssueSessionKey(ctx, identityKey, envelope, body.AspectScope, time.Duration(body.TTLSeconds)*time.Second)
	if err != nil {
		return ControlResponse{Error: err.Error()}
	}
	return ControlResponse{PersonalSessionKey: &PersonalSessionKeyResult{Key: sk.Key, ExpiresAt: sk.ExpiresAt}}
}

// readPiiKeyEnvelope point-reads and parses vtx.identity.<id>.piiKey — the
// same aspect shape the Processor's commit path writes (the aspect
// envelope's `data` field carries the vault.Envelope). Mirrors
// internal/refractor/pipeline.SecureDecryptor.readPiiKeyEnvelope; duplicated
// rather than imported for the same architecture-boundary reason as
// coreKVGetter above. A soft-deleted piiKey aspect is treated as absent.
func (s *Service) readPiiKeyEnvelope(ctx context.Context, identityKey string) (vault.Envelope, error) {
	entry, err := s.coreKV.Get(ctx, identityKey+".piiKey")
	if err != nil {
		return vault.Envelope{}, err
	}
	if entry == nil || len(entry.Value) == 0 {
		return vault.Envelope{}, substrate.ErrKeyNotFound
	}
	var doc struct {
		IsDeleted bool           `json:"isDeleted"`
		Data      vault.Envelope `json:"data"`
	}
	if err := json.Unmarshal(entry.Value, &doc); err != nil {
		return vault.Envelope{}, fmt.Errorf("parse piiKey %s.piiKey: %w", identityKey, err)
	}
	if doc.IsDeleted {
		return vault.Envelope{}, substrate.ErrKeyNotFound
	}
	return doc.Data, nil
}

// validateRule reports that field-level validation is not available. It
// existed to sample Core KV against the now-retired simple engine's parsed
// column list; every lens uses the full openCypher engine, which this
// best-effort sampling approach cannot parse.
func (s *Service) validateRule(ctx context.Context, ruleID string) ControlResponse {
	s.mu.Lock()
	rg := s.ruleGetter
	s.mu.Unlock()

	if rg == nil {
		return ControlResponse{Error: "validate: rule getter not configured"}
	}
	if _, ok := rg.Get(ruleID); !ok {
		return ControlResponse{Error: fmt.Sprintf("rule %q not loaded", ruleID)}
	}

	return ControlResponse{Validate: &ValidateResult{
		SampleSize:   0,
		FieldReports: nil,
		Warnings:     []string{"field-level validation is not available for the openCypher engine"},
	}}
}

// respondMicro marshals v to JSON and sends it as the micro reply.
// Logs marshal failures rather than returning them — the caller cannot
// do anything useful with them.
func (s *Service) respondMicro(req micro.Request, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		slog.Error("control: marshal response", "err", err)
		return
	}
	if err := req.Respond(data); err != nil {
		slog.Error("control: send response", "err", err)
	}
}
