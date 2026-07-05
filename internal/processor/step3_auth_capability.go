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

	"github.com/asolgan/lattice/internal/capabilitykv"
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

// CapabilityAuthorizer implements Authorizer by selecting an auth path from
// authContext, reading that path's single disjoint Capability-KV key, and
// running the path's matcher kind (Contract #2 §2.8 / Contract #6 §6.4-6.8).
type CapabilityAuthorizer struct {
	reader   CapabilityReader
	bucket   string
	clock    Clock
	cfg      CapabilityAuthorizerConfig
	logger   *slog.Logger
	registry []authEntry

	// Health KV samples — read by HealthHeartbeater at tick.
	latency *latencyRing
}

// capabilityAuthorizerOptions carries the optional dispatch-wiring inputs to
// NewCapabilityAuthorizer that go beyond the always-required reader/bucket.
type capabilityAuthorizerOptions struct {
	// extraEntries adds package-declared dispatch entries to the core seeds.
	// They are guarded fail-closed at registration (see buildAuthRegistry).
	extraEntries []authEntry
	// platformKeysDerivation overrides the platform entry's key-list derivation.
	// Nil keeps the default single-key cap.<actor>. When rbac-domain is
	// installed, core passes the class-aware derivation (system actor →
	// [cap.<actor>, cap.roles.<actor>] union, ordinary actor →
	// [cap.roles.<actor>]).
	platformKeysDerivation func(string) ([]string, error)
}

// NewCapabilityAuthorizer constructs the production authorizer. `reader`
// is typically a `*substrate.Conn`. `bucket` is the Capability KV bucket
// name (`bootstrap.CapabilityKVBucket` = `capability-kv`). Nil clock falls
// back to SystemClock; nil logger uses slog.Default(). `extraEntries` adds
// package-declared dispatch entries to the core seed entries; an extra that
// overlaps a core path, claims the always-true platform catch-all, or reuses a
// core path name is rejected (fail-closed) with an error.
func NewCapabilityAuthorizer(reader CapabilityReader, bucket string, clock Clock, cfg CapabilityAuthorizerConfig, logger *slog.Logger, extraEntries ...authEntry) (*CapabilityAuthorizer, error) {
	return newCapabilityAuthorizer(reader, bucket, clock, cfg, logger,
		capabilityAuthorizerOptions{extraEntries: extraEntries})
}

func newCapabilityAuthorizer(reader CapabilityReader, bucket string, clock Clock, cfg CapabilityAuthorizerConfig, logger *slog.Logger, opts capabilityAuthorizerOptions) (*CapabilityAuthorizer, error) {
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
	registry, err := buildAuthRegistry(opts.extraEntries, opts.platformKeysDerivation)
	if err != nil {
		return nil, err
	}
	return &CapabilityAuthorizer{
		reader:   reader,
		bucket:   bucket,
		clock:    clock,
		cfg:      cfg,
		logger:   logger,
		registry: registry,
		latency:  newLatencyRing(cfg.LatencyBufferSize),
	}, nil
}

// Authorize implements Authorizer. Hot path:
//  1. select the dispatch entry from authContext BEFORE any read — each entry
//     owns exactly one disjoint Capability-KV key, so path selection is a
//     pure function of authContext (Contract #2 §2.8 / Contract #6 §6.4-6.8).
//  2. derive that entry's single key and issue exactly one KV GET
//     (ErrKeyNotFound → denial with the entry's absent-key code; any other
//     error → return error so the commit path naks for retry).
//  3. parse the doc.
//  4. run the entry's matcher kind.
//
// The registry is walked in precedence order (task → service → platform);
// exactly one entry's predicate matches a given authContext, so exactly one
// key is read. The dispatcher never loops issuing reads and never merges docs.
//
// FR56 grants live in the package-owned `cap.ephemeral.<actor>` entry produced
// by the orchestration-base `capabilityEphemeral` lens; the `cap.<actor>` doc
// carries roles/permissions/service access only. A task-path no-match denies
// with AuthContextMismatch, which the denial builder emits without
// `actorRoles`, so there is NO `cap.<actor>` second read on the task path.
func (a *CapabilityAuthorizer) Authorize(ctx context.Context, env *OperationEnvelope) (Decision, error) {
	start := a.clock.Now()
	defer func() {
		a.latency.record(a.clock.Now().Sub(start))
	}()

	ac := env.AuthContext

	// Both task+service set → invalid auth declaration. Contract #2 §2.8's
	// dispatch table doesn't admit this combination. Decided BEFORE any read
	// and before path selection (otherwise the task entry would shadow it).
	if ac != nil && ac.Service != "" && ac.Task != "" {
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthContextMismatch,
			Reason:     "authContext: service and task are mutually exclusive",
		}, nil
	}

	entry := a.selectEntry(ac)
	if entry == nil {
		// The platform seed entry's predicate is always-true, so no match is
		// only reachable if the registry was built without it — fail closed.
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthDenied,
			Reason:     "no auth path selected for authContext",
		}, nil
	}

	// Lane gate (Contract #2 §2.3) — a non-default lane is a standing-identity
	// privilege. Only the platform path carries a doc-projected lane grant
	// (`doc.Lanes`, checked after the doc is parsed, below); EVERY other path is
	// a scoped, business-level grant that confers the `default` lane only. So any
	// non-platform path is rejected here BEFORE the read on a non-default lane
	// (no extra KV GET). Deny-by-default on the path kind — a future non-platform
	// path inherits default-only fail-closed, never an unchecked lane.
	if entry.coverage.kind != pathPlatform && env.Lane != LaneDefault {
		return Decision{
			Authorized: false,
			Code:       ErrCodeLaneUnauthorized,
			Reason:     "lane " + string(env.Lane) + ": " + entry.name + "-path grants the default lane only",
		}, nil
	}

	keys, derr := entry.deriveKeys(env.Actor)
	if derr != nil {
		// Malformed actor key would have been rejected at step 1, but keep the
		// defensive branch so a programming bug here surfaces as a typed denial
		// rather than a panic.
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthDenied,
			Reason:     "InvalidActorKey: " + derr.Error(),
		}, nil
	}

	doc, key, err := a.readAndMergeDoc(ctx, keys)
	if err != nil {
		// Genuine infrastructure failure — return error so commit path naks for
		// redelivery (existing authorizer-error branch in commit_path.go).
		return Decision{}, err
	}
	if doc == nil {
		// Contract #6 §6.8 — every member key absent denies with the path's own
		// denial code (deny-closed by construction: absence never grants). A
		// soft-tombstoned key is not absent (the GET returns its body), but it
		// carries an empty grant body, so the matcher below finds no grant and
		// denies all the same — there is no isDeleted inspection here.
		a.logger.Info("step 3: no Capability KV entry for actor on auth path",
			"requestId", env.RequestID, "actor", env.Actor, "path", entry.name, "keys", keys)
		return Decision{
			Authorized: false,
			Code:       entry.absentKeyCode,
			Reason:     entry.absentKeyReason,
		}, nil
	}

	// Platform-path lane gate (Contract #2 §2.3) — the actor's standing
	// capability doc is the lane authority. Checked AFTER parse and BEFORE the
	// operationType matcher ("before any further processing"), reading
	// doc.Lanes from the doc the platform read already fetched (no extra GET).
	// Fail-closed: an absent/empty doc.Lanes grants NO lane, including default.
	if entry.coverage.kind == pathPlatform && !laneGranted(env.Lane, doc.Lanes) {
		return Decision{
			Authorized: false,
			Code:       ErrCodeLaneUnauthorized,
			Reason:     "lane " + string(env.Lane) + ": not in the actor's granted lanes",
		}, nil
	}

	// doc.ProjectedAt is recorded as provenance for the auth-trace; it is no
	// longer compared against any freshness ceiling.
	resolved := &ResolvedPermission{
		CapKey:      key,
		ProjectedAt: doc.ProjectedAt,
	}

	dec := entry.kind(a, env, doc, resolved)
	if dec.Authorized {
		dec.Resolved = resolved
	} else if entry.threadsDocOnDenial {
		// Thread the doc through the denial for FR22 response construction
		// (actorRoles sourced from doc.Roles without an additional KV read).
		// The task path leaves this off: its denial carries no roles.
		dec.Doc = doc
	}
	return dec, nil
}

// selectEntry returns the first registry entry whose predicate matches the
// authContext, walking in precedence order. Path selection is a pure function
// of authContext — no KV read happens here.
func (a *CapabilityAuthorizer) selectEntry(ac *AuthContext) *authEntry {
	for i := range a.registry {
		if a.registry[i].selects(ac) {
			return &a.registry[i]
		}
	}
	return nil
}

// deriveKeys returns the entry's disjoint Capability-KV key(s) for actor.
// Every entry but the core platform entry derives exactly one key
// (keyDerivation); keysDerivation, when set, overrides with a key-list union
// read (Contract #6 §6.1's bounded system-actor platform-path carve-out).
func (e *authEntry) deriveKeys(actor string) ([]string, error) {
	if e.keysDerivation != nil {
		return e.keysDerivation(actor)
	}
	k, err := e.keyDerivation(actor)
	if err != nil {
		return nil, err
	}
	return []string{k}, nil
}

// readAndMergeDoc delegates to internal/capabilitykv.ReadAndMerge — the
// shared read+route+merge the control-plane capability checker also uses
// (control-plane-capability-authz-design.md §3.3), so both read the identical
// projection through the identical key set for a given actor. Behavior is
// byte-identical to the pre-factor inline implementation (proven by this
// package's existing step-3 auth tests).
func (a *CapabilityAuthorizer) readAndMergeDoc(ctx context.Context, keys []string) (*CapabilityDoc, string, error) {
	return capabilitykv.ReadAndMerge(ctx, a.reader, a.bucket, keys)
}

func (a *CapabilityAuthorizer) matchEphemeralGrant(env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	ac := env.AuthContext
	if ac == nil {
		// This kind reads ac.Task/ac.Target. A nil authContext yields the same
		// denial a non-matching grant produces, never a panic.
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthContextMismatch,
			Reason:     "no matching ephemeralGrant",
		}
	}
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
	if ac == nil {
		// This kind reads ac.Service. A nil authContext yields the same denial a
		// service not in serviceAccess[] produces, never a panic.
		return Decision{
			Authorized: false,
			Code:       ErrCodeAuthContextMismatch,
			Reason:     "service not in serviceAccess",
		}
	}
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

// laneGranted reports whether `lane` is in the actor's granted lane set
// (Contract #2 §2.3). It is the platform-path lane authority: a lane the
// standing capability doc does not list is denied. An empty/nil granted set
// grants nothing — fail-closed (auth correctness = projection correctness).
func laneGranted(lane Lane, granted []string) bool {
	for _, g := range granted {
		if g == string(lane) {
			return true
		}
	}
	return false
}

// capabilityKeyFromActor converts `vtx.identity.<NanoID>` →
// `cap.identity.<NanoID>` per Contract #6 §6.1 + producer logic in
// `internal/refractor/capabilityenv/envelope.go:capabilityKey`. Delegates to
// internal/capabilitykv (shared with the control-plane capability checker).
func capabilityKeyFromActor(actor string) (string, error) {
	return capabilitykv.CapabilityKeyFromActor(actor)
}

// rolesKeyFromActor converts `vtx.identity.<NanoID>` →
// `cap.roles.identity.<NanoID>` — the disjoint key rbac-domain's
// capabilityRoles lens projects an ordinary actor's role-derived grants into
// (Contract #6 §6.1). It is the platform path's key for ordinary actors when
// rbac-domain is installed. Delegates to internal/capabilitykv (shared with
// the control-plane capability checker).
func rolesKeyFromActor(actor string) (string, error) {
	return capabilitykv.RolesKeyFromActor(actor)
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

// serviceKeyFromActor converts `vtx.identity.<NanoID>` →
// `cap.svc.identity.<NanoID>` — the disjoint key service-location's
// capabilityServiceAccess lens projects an actor's residence-derived service
// access into (Contract #6 §6.1). It is the service-dispatch path's key. The
// service path is unconditional on this key (system actors never set
// ac.Service), so a service op denies by absence when no cap.svc.<actor>
// projection exists (Contract #6 §6.8).
func serviceKeyFromActor(actor string) (string, error) {
	if actor == "" {
		return "", errors.New("empty actor")
	}
	rest, ok := strings.CutPrefix(actor, substrate.VertexPrefix+".")
	if !ok {
		return "", fmt.Errorf("actor %q lacks %q prefix", actor, substrate.VertexPrefix+".")
	}
	return "cap.svc." + rest, nil
}
