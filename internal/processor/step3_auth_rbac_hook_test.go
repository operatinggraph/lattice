package processor

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// rolesDoc builds a cap.roles.identity.<id> doc carrying role-derived platform
// permissions — the projection rbac-domain's capabilityRoles lens produces for
// an ordinary actor.
func rolesDoc(nanoID string, perms ...PlatformPermission) *CapabilityDoc {
	actorKey := "vtx.identity." + nanoID
	return &CapabilityDoc{
		Key:                 "cap.roles.identity." + nanoID,
		Actor:               actorKey,
		Version:             "1.0",
		ProjectedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		Lanes:               []string{"default"},
		PlatformPermissions: perms,
		Roles:               []string{"vtx.role.someRole"},
	}
}

// anchorDoc builds a cap.identity.<id> doc carrying the kernel root grants —
// the projection the core primordial anchor produces for a system actor: the
// rbac-independent floor (all four privileged lanes + whatever bootstrap ops
// perms carries).
func anchorDoc(nanoID string, perms ...PlatformPermission) *CapabilityDoc {
	actorKey := "vtx.identity." + nanoID
	return &CapabilityDoc{
		Key:                 "cap.identity." + nanoID,
		Actor:               actorKey,
		Version:             "1.0",
		ProjectedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		Lanes:               []string{"default", "meta", "urgent", "system"},
		PlatformPermissions: perms,
	}
}

func rbacAuthorizer(t *testing.T, systemActorKeys []string, docs ...*CapabilityDoc) *CapabilityAuthorizer {
	t.Helper()
	reader := &fakeReader{entries: map[string][]byte{}}
	for _, d := range docs {
		raw, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal cap doc: %v", err)
		}
		reader.entries[d.Key] = raw
	}
	a, err := newCapabilityAuthorizer(reader, "capability-kv", &fakeClock{now: time.Now()},
		DefaultCapabilityAuthorizerConfig(), capTestLogger(),
		capabilityAuthorizerOptions{platformKeysDerivation: classAwarePlatformKey(systemActorKeys)})
	if err != nil {
		t.Fatalf("newCapabilityAuthorizer: %v", err)
	}
	return a
}

func platformEnv(actor, op string) *OperationEnvelope {
	// Lane is parse-validated at step 1, so a real envelope always carries a
	// valid lane; default is the universal grant (the rolesDoc/anchorDoc fixtures
	// grant it).
	return platformEnvLane(actor, op, LaneDefault)
}

func platformEnvLane(actor, op string, lane Lane) *OperationEnvelope {
	return &OperationEnvelope{RequestID: "r-" + actor, Actor: actor, OperationType: op, Lane: lane}
}

// TestRbacHook_OrdinaryActorReadsRolesKey proves an ordinary actor's platform
// authorize reads cap.roles.<actor> (rbac-domain's projection) via the
// class-aware platform key derivation, with exactly one KV GET (AC-A2/A4).
func TestRbacHook_OrdinaryActorReadsRolesKey(t *testing.T) {
	const ordinaryID = "ordinaryActor00000001"
	doc := rolesDoc(ordinaryID, PlatformPermission{OperationType: "CreateTask", Scope: "any"})
	a := rbacAuthorizer(t, []string{"vtx.identity.systemAdmin000000001"}, doc)

	dec, err := a.Authorize(context.Background(), platformEnv("vtx.identity."+ordinaryID, "CreateTask"))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("ordinary actor must authorize via cap.roles key; got denial %q/%q", dec.Code, dec.Reason)
	}
	reader := a.reader.(*fakeReader)
	if len(reader.gets) != 1 {
		t.Fatalf("expected exactly one KV GET (one-key-per-path); got %v", reader.gets)
	}
	if want := "cap.roles.identity." + ordinaryID; reader.gets[0] != want {
		t.Fatalf("ordinary actor must read %q; read %q", want, reader.gets[0])
	}
}

// TestRbacHook_SystemActorUnion_PackageOpOnSystemLane proves the core
// regression the union read fixes (design §6.1): a system actor's
// engine-submitted package op (granted via cap.roles, not the anchor) on a
// privileged lane (granted only by the anchor) authorizes — the union reads
// BOTH keys and merges perms + lanes.
func TestRbacHook_SystemActorUnion_PackageOpOnSystemLane(t *testing.T) {
	const systemID = "weaverActor00000000001"
	systemActor := "vtx.identity." + systemID
	anchor := anchorDoc(systemID) // floor: no package op, but grants the system lane
	roles := rolesDoc(systemID, PlatformPermission{OperationType: "MarkExpired", Scope: "any"})
	a := rbacAuthorizer(t, []string{systemActor}, anchor, roles)

	dec, err := a.Authorize(context.Background(), platformEnvLane(systemActor, "MarkExpired", LaneSystem))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("system actor's package op on the system lane must authorize via the union; got denial %q/%q", dec.Code, dec.Reason)
	}
	reader := a.reader.(*fakeReader)
	if len(reader.gets) != 2 {
		t.Fatalf("expected the union read (2 KV GETs); got %v", reader.gets)
	}
}

// TestRbacHook_SystemActorUnion_FloorOpStillGrants proves the union didn't
// drop the floor (design §6.2): a system actor's bootstrap op (InstallPackage,
// granted only by the anchor) on the meta lane still authorizes even with
// cap.roles present.
func TestRbacHook_SystemActorUnion_FloorOpStillGrants(t *testing.T) {
	const systemID = "systemAdmin000000001"
	systemActor := "vtx.identity." + systemID
	anchor := anchorDoc(systemID, PlatformPermission{OperationType: "InstallPackage", Scope: "any"})
	roles := rolesDoc(systemID, PlatformPermission{OperationType: "CreateTask", Scope: "any"})
	a := rbacAuthorizer(t, []string{systemActor}, anchor, roles)

	dec, err := a.Authorize(context.Background(), platformEnvLane(systemActor, "InstallPackage", LaneMeta))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("floor op on the meta lane must still authorize; got denial %q/%q", dec.Code, dec.Reason)
	}
	reader := a.reader.(*fakeReader)
	if len(reader.gets) != 2 {
		t.Fatalf("expected the union read (2 KV GETs); got %v", reader.gets)
	}
}

// TestRbacHook_SystemActorUnion_RolesAbsentFloorSurvives proves graceful
// degradation (design §6.3): mid rbac-domain-install, cap.roles is absent —
// the floor op still allows, and a package op denies (not a crash).
func TestRbacHook_SystemActorUnion_RolesAbsentFloorSurvives(t *testing.T) {
	const systemID = "systemAdmin000000002"
	systemActor := "vtx.identity." + systemID
	anchor := anchorDoc(systemID, PlatformPermission{OperationType: "InstallPackage", Scope: "any"})
	a := rbacAuthorizer(t, []string{systemActor}, anchor) // cap.roles NOT seeded

	dec, err := a.Authorize(context.Background(), platformEnvLane(systemActor, "InstallPackage", LaneMeta))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !dec.Authorized {
		t.Fatalf("floor op must still allow with cap.roles absent; got denial %q/%q", dec.Code, dec.Reason)
	}

	dec2, err := a.Authorize(context.Background(), platformEnvLane(systemActor, "MarkExpired", LaneSystem))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec2.Authorized {
		t.Fatalf("package op must deny (not crash) when cap.roles is absent")
	}
}

// TestRbacHook_SystemActorUnion_BothAbsentDenies proves fail-closed absence
// (design §6.4): every union member missing denies with the path's
// absentKeyCode, never a panic or a silent allow.
func TestRbacHook_SystemActorUnion_BothAbsentDenies(t *testing.T) {
	const systemID = "systemAdmin000000003"
	systemActor := "vtx.identity." + systemID
	a := rbacAuthorizer(t, []string{systemActor}) // no docs seeded at all

	dec, err := a.Authorize(context.Background(), platformEnvLane(systemActor, "InstallPackage", LaneMeta))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("both keys absent must deny")
	}
	if dec.Code != ErrCodeAuthDenied {
		t.Fatalf("expected AuthDenied (NoCapabilityEntry); got %q/%q", dec.Code, dec.Reason)
	}
}

// TestRbacHook_SystemActorUnion_DenyClosed proves the merge never over-grants
// (design §6.6): an op present in neither slice denies, and the system lane
// for an actor whose merged Lanes lacks it denies with LaneUnauthorized.
func TestRbacHook_SystemActorUnion_DenyClosed(t *testing.T) {
	const systemID = "systemAdmin000000004"
	systemActor := "vtx.identity." + systemID
	anchor := anchorDoc(systemID, PlatformPermission{OperationType: "InstallPackage", Scope: "any"})
	roles := rolesDoc(systemID, PlatformPermission{OperationType: "CreateTask", Scope: "any"})
	a := rbacAuthorizer(t, []string{systemActor}, anchor, roles)

	dec, err := a.Authorize(context.Background(), platformEnvLane(systemActor, "SomeUnrelatedOp", LaneMeta))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("an op present in neither slice must deny")
	}
	if dec.Code != ErrCodeAuthDenied {
		t.Fatalf("expected AuthDenied; got %q/%q", dec.Code, dec.Reason)
	}
}

// TestRbacHook_SystemActorUnion_LaneNotInMergedSetDenies proves the merged
// Lanes union is still deny-closed: a lane neither source grants denies with
// LaneUnauthorized, even though the op itself is granted.
func TestRbacHook_SystemActorUnion_LaneNotInMergedSetDenies(t *testing.T) {
	const systemID = "systemAdmin000000005"
	systemActor := "vtx.identity." + systemID
	// A degenerate anchor granting only "default" (no privileged lanes) — not
	// the real primordial anchor's shape, but isolates the lane-merge assertion.
	anchor := &CapabilityDoc{
		Key: "cap.identity." + systemID, Actor: systemActor, Version: "1.0",
		ProjectedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Lanes:       []string{"default"},
	}
	roles := rolesDoc(systemID, PlatformPermission{OperationType: "MarkExpired", Scope: "any"})
	a := rbacAuthorizer(t, []string{systemActor}, anchor, roles)

	dec, err := a.Authorize(context.Background(), platformEnvLane(systemActor, "MarkExpired", LaneSystem))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("the system lane must deny when neither source grants it")
	}
	if dec.Code != ErrCodeLaneUnauthorized {
		t.Fatalf("expected LaneUnauthorized; got %q/%q", dec.Code, dec.Reason)
	}
}

// TestRbacHook_OrdinaryActorDeniesWhenRolesKeyAbsent proves an ordinary actor
// whose cap.roles.<actor> doc is absent denies by absence (Contract #6 §6.8) —
// the rbac-absent / no-grant degradation.
func TestRbacHook_OrdinaryActorDeniesWhenRolesKeyAbsent(t *testing.T) {
	a := rbacAuthorizer(t, []string{"vtx.identity.systemAdmin000000001"}) // no docs seeded
	dec, err := a.Authorize(context.Background(), platformEnv("vtx.identity.ordinaryActor00000001", "CreateTask"))
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Authorized {
		t.Fatalf("ordinary actor with absent cap.roles must deny by absence")
	}
	if dec.Code != ErrCodeAuthDenied {
		t.Fatalf("expected AuthDenied (NoCapabilityEntry); got %q/%q", dec.Code, dec.Reason)
	}
}

// --- AC-X registry hardening ---

// overlapEntry is a hostile package extra whose predicate matches the platform
// cell (no task, no service) — it would siphon the platform read onto a
// package key. It declares pathPlatform without a scope tag (catch-all-ish).
func TestBuildAuthRegistry_RejectsAlwaysTrueExtra(t *testing.T) {
	extra := authEntry{
		name:          "rogue-catch-all",
		selects:       func(ac *AuthContext) bool { return true },
		kind:          matchPlatformPermissionKind,
		keyDerivation: rolesKeyFromActor,
		coverage:      authCoverage{kind: pathPlatform},
	}
	if _, err := buildAuthRegistry([]authEntry{extra}, nil); err == nil {
		t.Fatal("an always-true package extra claiming the platform cell must be rejected")
	}
}

// TestBuildAuthRegistry_RejectsOverlappingCorePath rejects a package extra that
// claims a core specific path-kind cell (task/service).
func TestBuildAuthRegistry_RejectsOverlappingCorePath(t *testing.T) {
	extra := authEntry{
		name:          "rogue-service",
		selects:       func(ac *AuthContext) bool { return ac != nil && ac.Service != "" },
		kind:          matchServiceAccessKind,
		keyDerivation: rolesKeyFromActor,
		coverage:      authCoverage{kind: pathService},
	}
	if _, err := buildAuthRegistry([]authEntry{extra}, nil); err == nil {
		t.Fatal("a package extra claiming the core service path must be rejected")
	}
}

// TestBuildAuthRegistry_RejectsMislabeledExtra rejects an extra whose predicate
// matches a cell it does not declare (an always-true predicate hiding behind a
// narrow declared coverage).
func TestBuildAuthRegistry_RejectsMislabeledExtra(t *testing.T) {
	extra := authEntry{
		name:          "mislabeled",
		selects:       func(ac *AuthContext) bool { return true }, // matches every cell
		kind:          matchPlatformPermissionKind,
		keyDerivation: rolesKeyFromActor,
		// Declares a narrow platform scope, but the predicate is always-true.
		coverage: authCoverage{kind: pathPlatform, scopeTag: "narrow-slice"},
	}
	if _, err := buildAuthRegistry([]authEntry{extra}, nil); err == nil {
		t.Fatal("a mislabeled (always-true) extra must be rejected by the probe-matrix cross-check")
	}
}

// TestBuildAuthRegistry_AcceptsDisjointScopedExtra accepts a legitimately
// disjoint package extra: a platform-kind entry with a unique scope tag whose
// predicate matches only a narrow slice of the platform cell (here, a specific
// authContext target) and is NOT always-true.
func TestBuildAuthRegistry_AcceptsDisjointScopedExtra(t *testing.T) {
	const specialTarget = "vtx.thing.x"
	extra := authEntry{
		name: "scoped-platform",
		// Matches only the platform cell AND only the specific target — a
		// strict, non-always-true slice. It never matches task/service cells.
		selects: func(ac *AuthContext) bool {
			return ac != nil && ac.Task == "" && ac.Service == "" && ac.Target == specialTarget
		},
		kind:          matchPlatformPermissionKind,
		keyDerivation: rolesKeyFromActor,
		coverage:      authCoverage{kind: pathPlatform, scopeTag: "special-target"},
	}
	reg, err := buildAuthRegistry([]authEntry{extra}, nil)
	if err != nil {
		t.Fatalf("a disjoint scoped extra must be accepted; got %v", err)
	}
	// The scoped extra must sit BEFORE the always-true platform catch-all so a
	// matching authContext routes to it; platform stays LAST.
	if reg[len(reg)-1].name != "platform" {
		t.Fatalf("platform catch-all must remain last; got %q", reg[len(reg)-1].name)
	}
	var sawScoped bool
	for _, e := range reg {
		if e.name == "scoped-platform" {
			sawScoped = true
		}
	}
	if !sawScoped {
		t.Fatal("the accepted scoped extra must be present in the registry")
	}
}

// TestBuildAuthRegistry_RejectsCoreNameReuse retains the name-collision guard.
func TestBuildAuthRegistry_RejectsCoreNameReuse(t *testing.T) {
	extra := authEntry{
		name:          "platform",
		selects:       func(ac *AuthContext) bool { return ac != nil && ac.Target == "x" },
		kind:          matchPlatformPermissionKind,
		keyDerivation: rolesKeyFromActor,
		coverage:      authCoverage{kind: pathPlatform, scopeTag: "dup-name"},
	}
	if _, err := buildAuthRegistry([]authEntry{extra}, nil); err == nil {
		t.Fatal("a package extra reusing the core 'platform' name must be rejected")
	}
}
