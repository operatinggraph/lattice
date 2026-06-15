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
		},
		{
			name:               "service",
			selects:            func(ac *AuthContext) bool { return ac != nil && ac.Service != "" },
			kind:               matchServiceAccessKind,
			keyDerivation:      capabilityKeyFromActor,
			absentKeyCode:      ErrCodeAuthDenied,
			absentKeyReason:    "NoCapabilityEntry",
			threadsDocOnDenial: true,
		},
	}
}

// seedPlatformEntry is the core catch-all: its predicate is always-true, so it
// is the final fallback for any authContext no specific or package path
// claimed. It must remain LAST in the registry.
func seedPlatformEntry() authEntry {
	return authEntry{
		name:               "platform",
		selects:            func(ac *AuthContext) bool { return true },
		kind:               matchPlatformPermissionKind,
		keyDerivation:      capabilityKeyFromActor,
		absentKeyCode:      ErrCodeAuthDenied,
		absentKeyReason:    "NoCapabilityEntry",
		threadsDocOnDenial: true,
	}
}

// buildAuthRegistry assembles the dispatch registry in precedence order:
// core specific entries (task → service), then package-declared extras, then
// the always-true platform catch-all LAST. Placing the catch-all last lets a
// package add a NEW selective path without it being shadowed by platform.
// Duplicate path names are rejected — the one-key-per-path invariant is
// structural: a path can never fan into N reads because at most one entry owns
// it. An extra may not reuse a core path name (task/service/platform), so it
// can only add a new path, never shadow a core path.
func buildAuthRegistry(extra []authEntry) ([]authEntry, error) {
	specific := seedSpecificEntries()
	entries := make([]authEntry, 0, len(specific)+len(extra)+1)
	entries = append(entries, specific...)
	entries = append(entries, extra...)
	entries = append(entries, seedPlatformEntry())

	seen := make(map[string]struct{}, len(entries))
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
	}
	return entries, nil
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
