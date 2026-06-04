// Processor step-3 Capability KV authorization.
//
// CapabilityAuthorizer performs a single NATS KV GET against `capability-kv`
// per Contract #6 §6.2 and dispatches per §6.4-6.8.
//
// Hot-path invariants:
//   - exactly one KV GET per Authorize call
//   - O(N) sequential scan over the doc's three permission arrays (Phase 1
//     actor counts are < 100 — premature maps would only obscure the code)
//   - injected Clock so ephemeralGrant expiry is deterministic in tests
//   - no caching (Phase 2 optimization, post-NFR-P3 conformance data)
package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Clock is the injectable wall clock the CapabilityAuthorizer uses for
// `ephemeralGrants[].expiresAt` comparisons. SystemClock is the production
// implementation; tests pass a fixed clock so grant-expiry assertions are
// deterministic.
type Clock interface {
	Now() time.Time
}

// SystemClock returns time.Now() at every call.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now() }

// CapabilityReader is the minimal NATS KV surface the CapabilityAuthorizer
// needs. The `*substrate.Conn` returned by `substrate.Connect` satisfies
// it; tests pass a fake reader that returns canned bytes for a fixed key.
type CapabilityReader interface {
	KVGet(ctx context.Context, bucket, key string) (*substrate.KVEntry, error)
}

// CapabilityAuthorizerConfig bundles the tuneables. Zero values are NOT
// safe — use DefaultCapabilityAuthorizerConfig().
type CapabilityAuthorizerConfig struct {
	// NFRP3 is the per-operation latency target (Contract NFR-P3 = 500ms).
	// Retained as the documented latency target for the Authorize call.
	NFRP3 time.Duration
	// LatencyBufferSize sizes the ring buffer used to summarise per-call
	// latency at heartbeat tick. Default 128 (matches Refractor's pattern).
	LatencyBufferSize int
}

// DefaultCapabilityAuthorizerConfig returns the production defaults.
func DefaultCapabilityAuthorizerConfig() CapabilityAuthorizerConfig {
	return CapabilityAuthorizerConfig{
		NFRP3:             500 * time.Millisecond,
		LatencyBufferSize: 128,
	}
}

// AuthAlertEmitter is the minimal Health KV alert surface. Implementations
// write to `health.alerts.security.<code>` per Contract #5 alert
// convention. The `stub-auth-active` alert (see StubAuthorizer) is the sole
// security alert code; the capability authorizer itself emits no alerts.
type AuthAlertEmitter interface {
	EmitAlert(ctx context.Context, code string, details map[string]any)
}

// noopAlertEmitter is the default when the caller doesn't wire an emitter.
// Useful for unit tests that don't care about alert side effects.
type noopAlertEmitter struct{}

func (noopAlertEmitter) EmitAlert(_ context.Context, _ string, _ map[string]any) {}

// CapabilityAuthorizer implements Authorizer by reading
// `cap.<actor-suffix>` from Capability KV and dispatching per Contract #6
// §6.4-6.8.
type CapabilityAuthorizer struct {
	reader CapabilityReader
	bucket string
	clock  Clock
	cfg    CapabilityAuthorizerConfig
	logger *slog.Logger

	// Health KV samples — read by HealthHeartbeater at tick.
	latency *latencyRing
}

// NewCapabilityAuthorizer constructs the production authorizer. `reader`
// is typically a `*substrate.Conn`. `bucket` is the Capability KV bucket
// name (`bootstrap.CapabilityKVBucket` = `capability-kv`). Nil clock falls
// back to SystemClock; nil logger uses slog.Default().
func NewCapabilityAuthorizer(reader CapabilityReader, bucket string, clock Clock, cfg CapabilityAuthorizerConfig, logger *slog.Logger) *CapabilityAuthorizer {
	if reader == nil {
		panic("processor: CapabilityAuthorizer requires a CapabilityReader")
	}
	if bucket == "" {
		panic("processor: CapabilityAuthorizer requires a bucket name")
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if cfg.NFRP3 <= 0 {
		cfg.NFRP3 = 500 * time.Millisecond
	}
	if cfg.LatencyBufferSize <= 0 {
		cfg.LatencyBufferSize = 128
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CapabilityAuthorizer{
		reader:  reader,
		bucket:  bucket,
		clock:   clock,
		cfg:     cfg,
		logger:  logger,
		latency: newLatencyRing(cfg.LatencyBufferSize),
	}
}

// Authorize implements Authorizer. Hot path:
//  1. derive the path from authContext BEFORE the read (Contract #10 §10.7):
//     the task-dispatch branch reads the DISJOINT `cap.ephemeral.<actor>`
//     key (it needs only grants — a single GET, no fallback); the
//     role/service/platform path reads `cap.<actor>` as before.
//  2. KV GET (ErrKeyNotFound → denial; any other error → return error so
//     commit path naks for retry)
//  3. parse the doc
//  4. dispatch per Contract #2 §2.8 / Contract #6 §6.4-6.8
//
// FR56 grants live in the package-owned `cap.ephemeral.<actor>` entry
// produced by the orchestration-base `capabilityEphemeral` lens; the
// `cap.<actor>` doc carries roles/permissions/service access only. A
// task-path no-match denies with AuthContextMismatch, which the denial
// builder emits without `actorRoles`, so there is NO `cap.<actor>` second
// read on the task path.
func (a *CapabilityAuthorizer) Authorize(ctx context.Context, env *OperationEnvelope) (Decision, error) {
	start := a.clock.Now()
	defer func() {
		a.latency.record(a.clock.Now().Sub(start))
	}()

	ac := env.AuthContext
	serviceSet := ac != nil && ac.Service != ""
	taskSet := ac != nil && ac.Task != ""

	// Both task+service set → invalid auth declaration. Contract #2 §2.8's
	// dispatch table doesn't admit this combination. Decided BEFORE any read.
	if serviceSet && taskSet {
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthContextMismatch,
			Reason:     "authContext: service and task are mutually exclusive",
		}, nil
	}

	if taskSet {
		return a.authorizeTaskPath(ctx, env)
	}
	return a.authorizeCapabilityPath(ctx, env, serviceSet)
}

// authorizeTaskPath reads the disjoint `cap.ephemeral.<actor>` key and runs
// matchEphemeralGrant. Single GET, no `cap.<actor>` fallback (Contract #10
// §10.7). Both an absent key AND an empty-grants doc are denial
// (AuthContextMismatch) — absence = denial, Contract #6 §6.8 / A3.
func (a *CapabilityAuthorizer) authorizeTaskPath(ctx context.Context, env *OperationEnvelope) (Decision, error) {
	ephKey, derr := ephemeralKeyFromActor(env.Actor)
	if derr != nil {
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthDenied,
			Reason:     "InvalidActorKey: " + derr.Error(),
		}, nil
	}

	entry, err := a.reader.KVGet(ctx, a.bucket, ephKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// No ephemeral entry for the actor = no grant. A3: absent key is
			// denial; the task path denies with AuthContextMismatch (carries
			// no actorRoles), so there is no second read.
			a.logger.Info("step 3: no ephemeral Capability KV entry for actor (task path)",
				"requestId", env.RequestID, "actor", env.Actor, "ephKey", ephKey)
			return Decision{
				Authorized: false,
				Code:       ErrCodeAuthContextMismatch,
				Reason:     "no ephemeral grant entry for actor",
			}, nil
		}
		return Decision{}, fmt.Errorf("capability kv read %q: %w", ephKey, err)
	}

	doc, err := ParseCapabilityDoc(entry.Value)
	if err != nil {
		return Decision{}, fmt.Errorf("capability kv parse %q: %w", ephKey, err)
	}

	resolved := &ResolvedPermission{
		CapKey:      ephKey,
		ProjectedAt: doc.ProjectedAt,
	}
	dec := a.matchEphemeralGrant(env, doc, resolved)
	if dec.Authorized {
		dec.Resolved = resolved
	}
	// On a task-path denial the code is AuthContextMismatch; the denial
	// builder returns early for that code without actorRoles, so we do NOT
	// thread doc (the ephemeral doc carries no roles anyway).
	return dec, nil
}

// authorizeCapabilityPath reads `cap.<actor>` and dispatches the
// service / platform paths (unchanged from Phase 1, minus the task branch
// which now lives in authorizeTaskPath).
func (a *CapabilityAuthorizer) authorizeCapabilityPath(ctx context.Context, env *OperationEnvelope, serviceSet bool) (Decision, error) {
	capKey, derr := capabilityKeyFromActor(env.Actor)
	if derr != nil {
		// Malformed actor key would have been rejected at step 1, but
		// keep the defensive branch so a programming bug here surfaces
		// as a typed denial rather than a panic.
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthDenied,
			Reason:     "InvalidActorKey: " + derr.Error(),
		}, nil
	}

	entry, err := a.reader.KVGet(ctx, a.bucket, capKey)
	if err != nil {
		if errors.Is(err, substrate.ErrKeyNotFound) {
			// Contract #6 §6.8 — absence equals denial.
			a.logger.Info("step 3: no Capability KV entry for actor",
				"requestId", env.RequestID, "actor", env.Actor, "capKey", capKey)
			return Decision{
				Authorized: false,
				Code:       ErrCodeAuthDenied,
				Reason:     "NoCapabilityEntry",
			}, nil
		}
		// Genuine infrastructure failure — return error so commit path
		// naks for redelivery (existing authorizer-error branch in
		// commit_path.go).
		return Decision{}, fmt.Errorf("capability kv read %q: %w", capKey, err)
	}

	doc, err := ParseCapabilityDoc(entry.Value)
	if err != nil {
		// Parse failure indicates a producer / contract drift — should be
		// caught by conformance tests long before runtime. Surface as internal
		// error rather than denial so operators see the real problem.
		return Decision{}, fmt.Errorf("capability kv parse %q: %w", capKey, err)
	}

	// Resolved permission threaded through context (Decision #8).
	// doc.ProjectedAt is recorded as provenance for the auth-trace; it is
	// no longer compared against any freshness ceiling.
	resolved := &ResolvedPermission{
		CapKey:      capKey,
		ProjectedAt: doc.ProjectedAt,
	}

	var dec Decision
	if serviceSet {
		dec = a.matchServiceAccess(env, doc, resolved)
	} else {
		dec = a.matchPlatformPermission(env, doc, resolved)
	}
	if dec.Authorized {
		dec.Resolved = resolved
	} else {
		// Thread the doc through the denial for FR22 response construction
		// (actorRoles sourced from doc.Roles without an additional KV read).
		dec.Doc = doc
	}
	return dec, nil
}

func (a *CapabilityAuthorizer) matchEphemeralGrant(env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	ac := env.AuthContext
	now := a.clock.Now()
	for i := range doc.EphemeralGrants {
		g := &doc.EphemeralGrants[i]
		if g.TaskKey != ac.Task {
			continue
		}
		if g.OperationType != env.OperationType {
			continue
		}
		if g.Target != ac.Target {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, g.ExpiresAt)
		if err != nil {
			// Treat unparseable expiry as a mismatch — operator visibility
			// via log so corrupt grant entries surface.
			a.logger.Warn("capability: ephemeralGrant.expiresAt unparseable; skipping",
				"taskKey", g.TaskKey, "value", g.ExpiresAt, "error", err)
			continue
		}
		if !now.Before(expiresAt) {
			// Expired — Contract #6 §6.6: `expiresAt > now`.
			continue
		}
		resolved.Path = "task"
		resolved.EphemeralGrant = g
		return Decision{Authorized: true}
	}
	return Decision{
		Authorized: false,
		Code:       ErrCodeAuthContextMismatch,
		Reason:     "no matching ephemeralGrant",
	}
}

func (a *CapabilityAuthorizer) matchServiceAccess(env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	ac := env.AuthContext
	for i := range doc.ServiceAccess {
		entry := &doc.ServiceAccess[i]
		if entry.Service != ac.Service {
			continue
		}
		for j := range entry.AllowedOperations {
			if entry.AllowedOperations[j].OperationType == env.OperationType {
				resolved.Path = "service"
				resolved.ServiceAccess = entry
				resolved.AllowedOperation = &entry.AllowedOperations[j]
				return Decision{Authorized: true}
			}
		}
		// Service matched but operation not in allowedOperations.
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthDenied,
			Reason:     "operationType not in serviceAccess.allowedOperations",
		}
	}
	// Service in authContext but not in serviceAccess[] — Contract #6
	// §6.5 step 2: AuthContextMismatch.
	return Decision{
		Authorized: false,
		Code:       ErrCodeAuthContextMismatch,
		Reason:     "service not in serviceAccess",
	}
}

func (a *CapabilityAuthorizer) matchPlatformPermission(env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	for i := range doc.PlatformPermissions {
		p := &doc.PlatformPermissions[i]
		if p.OperationType != env.OperationType {
			continue
		}
		switch p.Scope {
		case "any":
			resolved.Path = "platform"
			resolved.PlatformPermission = p
			return Decision{Authorized: true}
		case "self":
			ac := env.AuthContext
			target := ""
			if ac != nil {
				target = ac.Target
			}
			if target == "" {
				// `self` scope requires a target so we can equate it with
				// the actor. Treat absent target as a context mismatch.
				return Decision{
					Authorized: false,
					Code:       ErrCodeAuthContextMismatch,
					Reason:     "platform scope=self requires authContext.target",
				}
			}
			if target != env.Actor {
				return Decision{
					Authorized: false,
					Code:       ErrCodeAuthDenied,
					Reason:     "platform scope=self: target != actor",
				}
			}
			resolved.Path = "platform"
			resolved.PlatformPermission = p
			return Decision{Authorized: true}
		case "specific":
			// Phase 1 platform-path doesn't carry the specific-target
			// list. Decision #11: surface as AuthContextMismatch.
			return Decision{
				Authorized: false,
				Code:       ErrCodeAuthContextMismatch,
				Reason:     "platform scope=specific not implemented for platform path in Phase 1",
			}
		case "owned":
			// Phase 2 (Contract #6 §6.7).
			return Decision{
				Authorized: false,
				Code:       ErrCodeAuthDenied,
				Reason:     "OwnershipScopeNotImplemented",
			}
		default:
			return Decision{
				Authorized: false,
				Code:       ErrCodeAuthDenied,
				Reason:     "unknown platformPermission.scope: " + p.Scope,
			}
		}
	}
	return Decision{
		Authorized: false,
		Code:       ErrCodeAuthDenied,
		Reason:     "no matching platformPermission",
	}
}

// LatencyStats returns the per-call Authorize latency snapshot. Always
// emitted (Decision #5 — zero-sample emission is itself a live signal).
func (a *CapabilityAuthorizer) LatencyStats() LatencyStats {
	return a.latency.snapshot()
}

// capabilityKeyFromActor converts `vtx.identity.<NanoID>` →
// `cap.identity.<NanoID>` per Contract #6 §6.1 + producer logic in
// `internal/refractor/capabilityenv/envelope.go:capabilityKey`.
func capabilityKeyFromActor(actor string) (string, error) {
	if actor == "" {
		return "", errors.New("empty actor")
	}
	rest, ok := strings.CutPrefix(actor, substrate.VertexPrefix+".")
	if !ok {
		return "", fmt.Errorf("actor %q lacks %q prefix", actor, substrate.VertexPrefix+".")
	}
	return "cap." + rest, nil
}

// ephemeralKeyFromActor converts `vtx.identity.<NanoID>` →
// `cap.ephemeral.identity.<NanoID>` per Contract #6 §6.6 amendment + the
// producer logic in
// `internal/refractor/capabilityenv/envelope.go:ephemeralKey`. This is the
// disjoint key the task-dispatch branch reads (Contract #10 §10.7).
func ephemeralKeyFromActor(actor string) (string, error) {
	if actor == "" {
		return "", errors.New("empty actor")
	}
	rest, ok := strings.CutPrefix(actor, substrate.VertexPrefix+".")
	if !ok {
		return "", fmt.Errorf("actor %q lacks %q prefix", actor, substrate.VertexPrefix+".")
	}
	return "cap.ephemeral." + rest, nil
}
