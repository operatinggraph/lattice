// Story 3.3 — Processor step 3 Capability KV authorization.
//
// CapabilityAuthorizer replaces StubAuthorizer's allow-all behaviour with
// a single NATS KV GET against `capability-kv` per Contract #6 §6.2 +
// dispatch per §6.4-6.8.
//
// Hot-path invariants:
//   - exactly one KV GET per Authorize call
//   - O(N) sequential scan over the doc's three permission arrays (Phase 1
//     actor counts are < 100 — premature maps would only obscure the code)
//   - injected Clock so freshness gate is deterministic in tests
//   - no caching (Phase 2 optimization, post-NFR-P3 conformance data)
package processor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/asolgan/lattice/internal/substrate"
)

// Clock is the injectable wall clock the CapabilityAuthorizer uses for
// `projectedAt` staleness checks and `ephemeralGrants[].expiresAt`
// comparisons. SystemClock is the production implementation; tests pass
// a fixed clock so freshness assertions are deterministic.
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
	// Staleness above this but below StaleCeiling emits a Health KV signal
	// but still allows the operation. Staleness above StaleCeiling denies.
	NFRP3 time.Duration
	// StaleCeiling is the hard denial threshold for projection age.
	// Default: 5 × NFR-P3 = 2500ms (Decision #6 in the Story 3.3 brief).
	StaleCeiling time.Duration
	// LatencyBufferSize sizes the ring buffer used to summarise per-call
	// latency at heartbeat tick. Default 128 (matches Refractor's pattern).
	LatencyBufferSize int
}

// DefaultCapabilityAuthorizerConfig returns the production defaults.
func DefaultCapabilityAuthorizerConfig() CapabilityAuthorizerConfig {
	return CapabilityAuthorizerConfig{
		NFRP3:             500 * time.Millisecond,
		StaleCeiling:      2500 * time.Millisecond,
		LatencyBufferSize: 128,
	}
}

// AuthAlertEmitter is the minimal Health KV alert surface. Implementations
// write to `health.alerts.security.<code>` per Contract #5 alert
// convention. The CapabilityAuthorizer emits two alert codes:
//   - `auth-freshness-exceeded` — on hard-denial freshness failures
//   - `stub-auth-active` — see StubAuthorizer; not emitted by capability mode
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
	reader  CapabilityReader
	bucket  string
	clock   Clock
	cfg     CapabilityAuthorizerConfig
	emitter AuthAlertEmitter
	logger  *slog.Logger

	// Health KV samples — read by HealthHeartbeater at tick.
	latency   *latencyRing
	staleness *latencyRing

	// Atomic counters for cross-tick visibility (Decision #4).
	stalenessExceedingNFRP3 atomic.Uint64
}

// NewCapabilityAuthorizer constructs the production authorizer. `reader`
// is typically a `*substrate.Conn`. `bucket` is the Capability KV bucket
// name (`bootstrap.CapabilityKVBucket` = `capability-kv`). Nil clock /
// emitter fall back to sensible defaults; nil logger uses slog.Default().
func NewCapabilityAuthorizer(reader CapabilityReader, bucket string, clock Clock, cfg CapabilityAuthorizerConfig, emitter AuthAlertEmitter, logger *slog.Logger) *CapabilityAuthorizer {
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
	if cfg.StaleCeiling <= 0 {
		cfg.StaleCeiling = 5 * cfg.NFRP3
	}
	if cfg.LatencyBufferSize <= 0 {
		cfg.LatencyBufferSize = 128
	}
	if emitter == nil {
		emitter = noopAlertEmitter{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CapabilityAuthorizer{
		reader:    reader,
		bucket:    bucket,
		clock:     clock,
		cfg:       cfg,
		emitter:   emitter,
		logger:    logger,
		latency:   newLatencyRing(cfg.LatencyBufferSize),
		staleness: newLatencyRing(cfg.LatencyBufferSize),
	}
}

// Authorize implements Authorizer. Hot path:
//  1. derive cap-key from env.Actor
//  2. KV GET (ErrKeyNotFound → AuthDenied/NoCapabilityEntry; any other
//     error → return error so commit path naks for retry)
//  3. parse Contract #6 §6.2 doc
//  4. freshness gate (Decision #6 — hard denial above ceiling)
//  5. dispatch per Contract #2 §2.8 / Contract #6 §6.4-6.8
func (a *CapabilityAuthorizer) Authorize(ctx context.Context, env *OperationEnvelope) (Decision, error) {
	start := a.clock.Now()
	defer func() {
		a.latency.record(a.clock.Now().Sub(start))
	}()

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
		// caught by Story 3.2b's conformance test long before runtime.
		// Surface as internal error rather than denial so operators see
		// the real problem.
		return Decision{}, fmt.Errorf("capability kv parse %q: %w", capKey, err)
	}

	// Freshness gate (Decision #6).
	if dec, hit := a.checkFreshness(ctx, env, doc); hit {
		// Doc is available; thread it for FR22 denial response construction.
		dec.Doc = doc
		return dec, nil
	}

	// Resolved permission threaded through context (Decision #8).
	resolved := &ResolvedPermission{
		CapKey:      capKey,
		ProjectedAt: doc.ProjectedAt,
	}

	dec := a.dispatch(env, doc, resolved)
	if dec.Authorized {
		dec.Resolved = resolved
	} else {
		// Thread the doc through the denial for Story 3.4 FR22 response
		// construction (actorRoles sourced from doc.Roles without re-read).
		dec.Doc = doc
	}
	return dec, nil
}

// dispatch implements Contract #2 §2.8 path selection + Contract #6
// §6.4-6.8 matching.
func (a *CapabilityAuthorizer) dispatch(env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	ac := env.AuthContext
	serviceSet := ac != nil && ac.Service != ""
	taskSet := ac != nil && ac.Task != ""

	// Both task+service set → invalid auth declaration. Contract #2 §2.8's
	// dispatch table doesn't admit this combination.
	if serviceSet && taskSet {
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthContextMismatch,
			Reason:     "authContext: service and task are mutually exclusive",
		}
	}

	switch {
	case taskSet:
		return a.matchEphemeralGrant(env, doc, resolved)
	case serviceSet:
		return a.matchServiceAccess(env, doc, resolved)
	default:
		return a.matchPlatformPermission(env, doc, resolved)
	}
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
				// `self` scope requires a target so we can equate it
				// with the actor. Treat absent target as a mismatch
				// per the Story 3.3 brief Decision #11 note.
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

// checkFreshness inspects doc.ProjectedAt against the injected clock.
// Returns (Decision, true) when the operation must be denied for being
// excessively stale; (zero, false) otherwise. Sub-NFR-P3 staleness is
// recorded for heartbeat emission but does not affect the decision.
func (a *CapabilityAuthorizer) checkFreshness(ctx context.Context, env *OperationEnvelope, doc *CapabilityDoc) (Decision, bool) {
	if doc.ProjectedAt == "" {
		// Missing timestamp shouldn't happen given Contract #6 §6.3, but
		// be defensive: treat as fresh enough rather than denying — the
		// conformance test catches missing field, runtime shouldn't fail
		// closed on operator-friendly metadata.
		return Decision{}, false
	}
	projected, err := time.Parse(time.RFC3339Nano, doc.ProjectedAt)
	if err != nil {
		a.logger.Warn("capability: projectedAt unparseable; allowing", "value", doc.ProjectedAt, "error", err)
		return Decision{}, false
	}
	age := a.clock.Now().Sub(projected)
	if age < 0 {
		// Clock skew or test-fixture quirk; treat as fresh.
		return Decision{}, false
	}
	if age > a.cfg.StaleCeiling {
		a.emitter.EmitAlert(ctx, "auth-freshness-exceeded", map[string]any{
			"requestId":   env.RequestID,
			"actor":       env.Actor,
			"capKey":      doc.Key,
			"projectedAt": doc.ProjectedAt,
			"ageMs":       age.Milliseconds(),
			"ceilingMs":   a.cfg.StaleCeiling.Milliseconds(),
		})
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthFreshnessExceeded,
			Reason:     fmt.Sprintf("Capability KV projection age %dms exceeds ceiling %dms", age.Milliseconds(), a.cfg.StaleCeiling.Milliseconds()),
		}, true
	}
	if age > a.cfg.NFRP3 {
		// Above NFR-P3 but below ceiling: record for heartbeat signal.
		a.staleness.record(age)
		a.stalenessExceedingNFRP3.Add(1)
	}
	return Decision{}, false
}

// LatencyStats returns the per-call Authorize latency snapshot. Always
// emitted (Decision #5 — zero-sample emission is itself a live signal).
func (a *CapabilityAuthorizer) LatencyStats() LatencyStats {
	return a.latency.snapshot()
}

// StalenessStats returns the projection-staleness snapshot. The
// HealthHeartbeater skips emission when Count==0 (no misleading zeros —
// Decision #4).
func (a *CapabilityAuthorizer) StalenessStats() LatencyStats {
	return a.staleness.snapshot()
}

// StalenessExceedingNFRP3 returns the total count (since process start)
// of Authorize calls whose Capability KV entry was older than NFR-P3 but
// fresher than the ceiling. Heartbeat reads this as a monotonic counter.
func (a *CapabilityAuthorizer) StalenessExceedingNFRP3() uint64 {
	return a.stalenessExceedingNFRP3.Load()
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
