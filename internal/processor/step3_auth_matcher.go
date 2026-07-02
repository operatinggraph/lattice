// Step-3 auth-hook dispatch registry (Contract #2 §2.8 Phase-2 amendment +
// Contract #6 §6.1/§6.4-6.8).
//
// The dispatcher is data-driven: each authEntry binds an authContext path
// predicate to a core matcher kind and the single disjoint Capability-KV key
// that path reads. Path selection happens BEFORE the read, and each path maps
// to exactly one key — so exactly one KV GET fires per Authorize call
// (one-key-per-path invariant). Two entries selecting the same path is a
// configuration error, rejected at authorizer construction.
//
// Core owns the matcher *kinds* (the three logics below); the path → kind →
// key *wiring* is data. A package extends the dispatch by contributing an
// authEntry (a value), never by editing the dispatch decision or shipping
// matcher code.
package processor

import "fmt"

// pathKind classifies the authContext slice an authEntry handles. The three
// kinds partition the authContext space: a task-set context, a service-set
// context, and everything else (the platform fallback). It is the structural
// basis for the registration-time overlap guard — a package extra may not
// claim a core path-kind cell, and the always-true platform catch-all may not
// be reclaimed by a package.
type pathKind int

const (
	// pathPlatform is the catch-all fallback: neither task nor service set.
	pathPlatform pathKind = iota
	// pathTask is the ephemeral-grant (task) path: authContext.Task set.
	pathTask
	// pathService is the service-access path: authContext.Service set.
	pathService
)

// authCoverage declares, structurally, which dispatch cell an authEntry
// claims. Overlap is a set-intersection test over these declarations — not a
// best-effort comparison of opaque predicate closures. The declared coverage
// is cross-checked against the entry's predicate via a representative
// authContext probe matrix at registration (a predicate that matches a cell it
// does not declare, or matches every cell, is rejected fail-closed).
type authCoverage struct {
	// kind is the path-kind cell this entry claims.
	kind pathKind
	// catchAll is true only for the core platform fallback, whose predicate is
	// always-true. A package extra is never permitted to set this — an extra
	// claiming the always-true platform cell would siphon the platform read
	// onto a package-controlled key.
	catchAll bool
	// scopeTag narrows a platform-kind entry to a disjoint slice (e.g. a
	// dedicated actor-class read). Two platform entries with the same scopeTag
	// (or either unscoped) overlap. Empty for the core platform catch-all and
	// for task/service core paths.
	scopeTag string
}

// matcherKind authorizes one grant type against a parsed CapabilityDoc. It
// runs after the entry's disjoint key has been read; it sets resolved.Path and
// the matched-entry pointer on success and returns a typed denial otherwise.
type matcherKind func(a *CapabilityAuthorizer, env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision

// authEntry is one row of the dispatch registry. Exactly one entry matches a
// given authContext; its single key is read once and run through its kind.
type authEntry struct {
	// name identifies the entry for config-error diagnostics. Doubles as the
	// dispatch path identity: two entries with the same name collide.
	name string
	// selects reports whether this entry handles the given authContext. Path
	// selection is computed from authContext alone, before any KV read.
	selects func(ac *AuthContext) bool
	// kind authorizes the grant type once its key has been read.
	kind matcherKind
	// keyDerivation maps the actor key to the single disjoint Capability-KV
	// key this path reads.
	keyDerivation func(actor string) (string, error)
	// absentKeyCode is the denial Code emitted when the disjoint key is absent.
	// Contract #6 §6.8 — no entry equals no access. A soft-tombstoned key is not
	// absent: its GET succeeds and the matcher runs, but the tombstone body carries
	// no grant arrays, so the matcher finds no grant and denies on its own path —
	// the tombstone is never inspected for an isDeleted flag here.
	absentKeyCode ErrorCode
	// absentKeyReason is the denial Reason paired with absentKeyCode.
	absentKeyReason string
	// threadsDocOnDenial controls whether the parsed doc is threaded onto a
	// denial Decision so the FR22 denial builder can source actorRoles without
	// a second read. The task path leaves it false (its denial carries no
	// roles and emits no actorRoles); the capability paths set it true.
	threadsDocOnDenial bool
	// coverage declares which dispatch cell this entry claims, for the
	// registration-time overlap guard.
	coverage authCoverage
}

// seedSpecificEntries returns the core dispatch entries whose predicates are
// specific (task, service) — selected only when authContext names that path.
// They precede package extras in precedence (task → service → … → platform).
func seedSpecificEntries() []authEntry {
	return []authEntry{
		{
			name:            "task",
			selects:         func(ac *AuthContext) bool { return ac != nil && ac.Task != "" },
			kind:            matchEphemeralGrantKind,
			keyDerivation:   ephemeralKeyFromActor,
			absentKeyCode:   ErrCodeAuthContextMismatch,
			absentKeyReason: "no ephemeral grant entry for actor",
			coverage:        authCoverage{kind: pathTask},
		},
		{
			name:            "service",
			selects:         func(ac *AuthContext) bool { return ac != nil && ac.Service != "" },
			kind:            matchServiceAccessKind,
			keyDerivation:   serviceKeyFromActor,
			absentKeyCode:   ErrCodeAuthDenied,
			absentKeyReason: "NoCapabilityEntry",
			// The service path reads the disjoint cap.svc.<actor> key projected
			// by service-location's capabilityServiceAccess lens — the residence
			// grant scheme. That doc carries serviceAccess[] only, no roles, so a
			// service-op denial surfaces no actorRoles (Contract #6 §6.12): the
			// denial is explained by residence/availability, not roles. The doc is
			// therefore NOT threaded onto the denial.
			coverage: authCoverage{kind: pathService},
		},
	}
}

// seedPlatformEntry is the core catch-all: its predicate is always-true, so it
// is the final fallback for any authContext no specific or package path
// claimed. It must remain LAST in the registry.
//
// platformKeyDerivation governs which Capability-KV key the platform read
// targets. When rbac-domain is installed it is the class-aware derivation that
// routes ordinary actors to cap.roles.<actor> (rbac-domain's projection) and
// the kernel-seeded system actors to cap.<actor> (the core primordial anchor);
// when rbac-domain is absent it is the plain cap.<actor> derivation (today's
// behavior — ordinary actors then read a cap.<actor> that no longer carries
// role-derived grants, so they deny by absence per Contract #6 §6.8). Exactly
// one key is read per call either way (one-key-per-path).
func seedPlatformEntry(platformKeyDerivation func(string) (string, error)) authEntry {
	if platformKeyDerivation == nil {
		platformKeyDerivation = capabilityKeyFromActor
	}
	return authEntry{
		name:               "platform",
		selects:            func(ac *AuthContext) bool { return true },
		kind:               matchPlatformPermissionKind,
		keyDerivation:      platformKeyDerivation,
		absentKeyCode:      ErrCodeAuthDenied,
		absentKeyReason:    "NoCapabilityEntry",
		threadsDocOnDenial: true,
		coverage:           authCoverage{kind: pathPlatform, catchAll: true},
	}
}

// classAwarePlatformKey returns a platform key-derivation closure that routes
// the kernel-seeded system actors (systemActorKeys) to their core cap.<actor>
// anchor and every other (ordinary) actor to cap.roles.<actor>. It is the
// single platform entry's class-aware derivation (Q1): one key chosen per
// Authorize call by actor class, never a fan-out. systemActorKeys are the full
// vtx.identity.<id> actor keys of the primordial admin + the kernel-seeded
// service actors (graph-discovered by bootstrap.SystemActorKeys).
func classAwarePlatformKey(systemActorKeys []string) func(string) (string, error) {
	system := make(map[string]struct{}, len(systemActorKeys))
	for _, k := range systemActorKeys {
		if k != "" {
			system[k] = struct{}{}
		}
	}
	return func(actor string) (string, error) {
		if _, isSystem := system[actor]; isSystem {
			return capabilityKeyFromActor(actor)
		}
		return rolesKeyFromActor(actor)
	}
}

// buildAuthRegistry assembles the dispatch registry in precedence order: core
// specific entries (task → service), then package-declared extras, then the
// always-true platform catch-all LAST. Placing the catch-all last lets a
// package add a NEW disjoint path without it being shadowed by platform.
//
// Package extras are guarded structurally (fail-closed) — a package-derived
// entry is the FIRST untrusted dispatch contribution, so name-only dedup is no
// longer sufficient. An extra is REJECTED at registration when:
//
//   - it reuses a core path name (task/service/platform); or
//   - its declared coverage overlaps a core path-kind cell (task or service);
//     or
//   - it claims the always-true platform catch-all (catchAll true, or a
//     platform-kind entry without a scopeTag) — which would siphon the
//     platform read onto a package-controlled key; or
//   - its declared coverage overlaps another entry's (same path-kind + an
//     overlapping platform scopeTag); or
//   - its predicate does not match the declared coverage cell, or matches a
//     cell it does not declare (an always-true or mislabeled predicate hiding
//     behind a narrow declaration) — checked via a representative authContext
//     probe matrix.
//
// The rbac-domain hook is NOT supplied as an extra: it is folded into the
// platform entry's class-aware key derivation (seedPlatformEntry), so it never
// trips this guard and one-key-per-path is preserved (exactly one key chosen
// per Authorize call). The guard governs any genuinely-separate future package
// path supplied via ExtraEntries.
func buildAuthRegistry(extra []authEntry, platformKeyDerivation func(string) (string, error)) ([]authEntry, error) {
	specific := seedSpecificEntries()
	platform := seedPlatformEntry(platformKeyDerivation)

	entries := make([]authEntry, 0, len(specific)+len(extra)+1)
	entries = append(entries, specific...)
	entries = append(entries, extra...)
	entries = append(entries, platform)

	// Mark which entries are package-derived extras (untrusted) so the guard
	// applies the structural coverage checks only to them — the core seeds are
	// trusted by construction and DO claim the core cells.
	isExtra := make(map[int]bool, len(extra))
	for i := range extra {
		isExtra[len(specific)+i] = true
	}

	seen := make(map[string]struct{}, len(entries))
	// claimedScopes tracks platform-kind scopeTags already claimed, so two
	// platform extras with the same tag (or an extra colliding with a future
	// scoped core entry) are rejected.
	claimedScopes := make(map[string]struct{})
	for i := range entries {
		e := &entries[i]
		if e.name == "" {
			return nil, fmt.Errorf("auth registry: entry %d has an empty path name", i)
		}
		if e.selects == nil || e.kind == nil || e.keyDerivation == nil {
			return nil, fmt.Errorf("auth registry: entry %q is missing a predicate, kind, or key derivation", e.name)
		}
		if _, dup := seen[e.name]; dup {
			return nil, fmt.Errorf("auth registry: duplicate path %q — two entries select the same path (one-key-per-path violation)", e.name)
		}
		seen[e.name] = struct{}{}

		// The declared coverage must match the predicate's actual behavior. A
		// predicate that matches a cell it does not declare (or matches every
		// cell while declaring a narrow slice) is rejected fail-closed.
		if err := checkCoverageMatchesPredicate(e); err != nil {
			return nil, err
		}

		if isExtra[i] {
			if err := guardPackageExtra(e, claimedScopes); err != nil {
				return nil, err
			}
		}
	}
	return entries, nil
}

// unconditionalProbes are the bare platform-cell authContexts (no task, no
// service, no target). A predicate that matches any of these is claiming the
// platform path UNCONDITIONALLY — i.e. it is the always-true catch-all (or
// indistinguishable from it). Only the core platform entry may do that.
var unconditionalProbes = []*AuthContext{
	nil,
	{},
}

// foreignCellProbes are representative authContexts for the task and service
// path-kind cells. A package extra (or any non-catch-all entry) that matches
// one of these is overlapping a core specific path and is rejected.
var foreignCellProbes = map[pathKind]*AuthContext{
	pathTask:    {Task: "vtx.task.probe"},
	pathService: {Service: "vtx.service.probe"},
}

// checkCoverageMatchesPredicate validates an entry's declared coverage against
// its predicate using a representative authContext probe matrix (structural,
// not best-effort). The catch-all platform entry is exempt (its predicate is
// always-true by design and it is the trusted core fallback). For every other
// entry the predicate MUST NOT:
//
//   - match an unconditional platform probe (nil / empty authContext) unless it
//     declares the platform cell AND has no scope tag — that is the always-true
//     catch-all, reserved for the core platform entry; or
//   - match a foreign path-kind cell it does not declare (overlapping a core
//     task/service path).
//
// A scoped platform entry (scopeTag set) is required to be conditional: it must
// NOT match the unconditional probes (otherwise it is an always-true claim
// hiding behind a narrow declaration). This lets a genuinely disjoint slice
// (e.g. a specific authContext.target) pass while rejecting a mislabeled
// always-true predicate — without enumerating every possible discriminator
// value.
func checkCoverageMatchesPredicate(e *authEntry) error {
	if e.coverage.catchAll {
		return nil
	}
	// Must not claim any foreign (non-declared) specific cell.
	for kind, ac := range foreignCellProbes {
		if kind == e.coverage.kind {
			continue
		}
		if e.selects(ac) {
			return fmt.Errorf("auth registry: entry %q predicate matches the %v path-kind cell it does not declare "+
				"(declared %v) — rejected fail-closed", e.name, kind, e.coverage.kind)
		}
	}
	// A non-catch-all entry must be conditional: it may not match the bare
	// platform probes (that is the always-true catch-all).
	for _, ac := range unconditionalProbes {
		if e.selects(ac) {
			return fmt.Errorf("auth registry: entry %q predicate matches an unconditional platform context — "+
				"an always-true or mislabeled predicate may not be registered as a scoped entry", e.name)
		}
	}
	return nil
}

// guardPackageExtra enforces the structural overlap rules on a package-derived
// extra (fail-closed).
func guardPackageExtra(e *authEntry, claimedScopes map[string]struct{}) error {
	switch e.name {
	case "task", "service", "platform":
		return fmt.Errorf("auth registry: package entry %q reuses a core path name", e.name)
	}
	switch e.coverage.kind {
	case pathTask, pathService:
		return fmt.Errorf("auth registry: package entry %q overlaps the core %v path — "+
			"a package may not claim a core path-kind cell", e.name, e.coverage.kind)
	case pathPlatform:
		if e.coverage.catchAll || e.coverage.scopeTag == "" {
			return fmt.Errorf("auth registry: package entry %q claims the always-true platform catch-all — "+
				"it would siphon the platform read onto a package-controlled key", e.name)
		}
		if _, dup := claimedScopes[e.coverage.scopeTag]; dup {
			return fmt.Errorf("auth registry: package entry %q reuses platform scope %q already claimed",
				e.name, e.coverage.scopeTag)
		}
		claimedScopes[e.coverage.scopeTag] = struct{}{}
	}
	return nil
}

// matchEphemeralGrantKind is the task matcher kind: it authorizes an operation
// against a matching, unexpired ephemeralGrant (Contract #6 §6.6).
func matchEphemeralGrantKind(a *CapabilityAuthorizer, env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	return a.matchEphemeralGrant(env, doc, resolved)
}

// matchServiceAccessKind is the service matcher kind: it authorizes an
// operation against the actor's serviceAccess[] (Contract #6 §6.5).
func matchServiceAccessKind(a *CapabilityAuthorizer, env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	return a.matchServiceAccess(env, doc, resolved)
}

// matchPlatformPermissionKind is the platform matcher kind: it authorizes an
// operation against the actor's platformPermissions[] (Contract #6 §6.4/§6.7).
func matchPlatformPermissionKind(a *CapabilityAuthorizer, env *OperationEnvelope, doc *CapabilityDoc, resolved *ResolvedPermission) Decision {
	return a.matchPlatformPermission(env, doc, resolved)
}
