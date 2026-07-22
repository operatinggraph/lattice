package consoleoperator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/controlauth"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestPackage_ManifestMatchesDefinition confirms the on-disk manifest.yaml
// cross-validates against the exported Definition (the most common package-
// authoring drift bug), before any NATS integration.
func TestPackage_ManifestMatchesDefinition(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	m, err := pkgmgr.ParseManifest(filepath.Join(wd, "manifest.yaml"))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if err := m.VerifyAgainstDefinition(Package); err != nil {
		t.Fatalf("manifest <-> Definition drift: %v", err)
	}
}

// TestPackage_NoDDLsExactlyOneReadGrantLens pins the package's shape: no
// DDLs (an ordinary rbac/grant package, mirrors packages/control-authz,
// packages/privacy-operator-grant), plus exactly ONE lens — its own read-side
// wildcard-grant producer (consoleOperatorReadGrants), never a business
// read model.
func TestPackage_NoDDLsExactlyOneReadGrantLens(t *testing.T) {
	if len(Package.DDLs) != 0 {
		t.Fatalf("package must declare no DDLs; got %d", len(Package.DDLs))
	}
	if len(Package.Lenses) != 1 {
		t.Fatalf("package must declare exactly 1 lens (its read-grant producer); got %d", len(Package.Lenses))
	}
	lens := Package.Lenses[0]
	if lens.CanonicalName != "consoleOperatorReadGrants" {
		t.Errorf("lens CanonicalName = %q, want %q", lens.CanonicalName, "consoleOperatorReadGrants")
	}
	if !lens.GrantTable {
		t.Error("the lens must be a GrantTable producer, not a business read model")
	}
	if lens.Protected || lens.Public {
		t.Error("a GrantTable lens must not also be Protected/Public — those are business-read-model concerns")
	}
	deps := map[string]bool{}
	for _, d := range Package.Depends {
		deps[d] = true
	}
	for _, want := range []string{"rbac-domain", "identity-domain", "privacy-base", "objects-base"} {
		if !deps[want] {
			t.Errorf("Depends = %v, want to include %q", Package.Depends, want)
		}
	}
}

// TestPackage_ReadGrantLensMatchesConsoleOperatorOnly pins the cypher's
// exclusivity: it must key on the consoleOperator role specifically, never
// the primordial `operator` role (that's the kernel's
// capabilityReadWildcardGrants lens's job) — a package granting read-side
// wildcard access to primordial-root holders would be a package-vocabulary
// walk into kernel territory, the exact layering violation the kernel lens's
// own doc comment says to avoid.
func TestPackage_ReadGrantLensMatchesConsoleOperatorOnly(t *testing.T) {
	spec := Package.Lenses[0].Spec
	if !strings.Contains(spec, "'consoleOperator'") {
		t.Errorf("lens spec must match role.canonicalName = 'consoleOperator': %s", spec)
	}
	if strings.Contains(spec, "'operator'") {
		t.Errorf("lens spec must not reference the primordial 'operator' role — that's the kernel lens's job: %s", spec)
	}
	if !strings.Contains(spec, "'*'") {
		t.Errorf("lens spec must grant the wildcard anchor '*': %s", spec)
	}
}

// TestPackage_DeclaresConsoleOperatorRoleDistinctFromPrimordialOperator pins
// the naming decision the package.go doc explains at length: the new role is
// "consoleOperator", never "operator" (the kernel-primordial, root-equivalent
// role every SystemActorKeys() holder is discovered by).
func TestPackage_DeclaresConsoleOperatorRoleDistinctFromPrimordialOperator(t *testing.T) {
	if len(Package.Roles) != 1 {
		t.Fatalf("Roles = %d, want exactly 1", len(Package.Roles))
	}
	role := Package.Roles[0]
	if role.CanonicalName != "consoleOperator" {
		t.Fatalf("role CanonicalName = %q, want %q (must not collide with the primordial %q role)",
			role.CanonicalName, "consoleOperator", "operator")
	}
}

// TestPackage_OnlyAllowlistedPkgLifecycleOpsGranted pins mechanism C's core
// safety property (scoped-privileged-lane-grants-design.md §7 item 3): this
// package grants EXACTLY the allowlisted pkg-lifecycle trio
// (InstallPackage/UninstallPackage/UpgradePackage) at the `meta` lane, each
// carrying its own per-op Lanes:["meta"] (never a doc-level grant), and
// nothing else privileged — CreateMetaVertex/UpdateMetaVertex/
// TombstoneMetaVertex and any urgent/system lane stay ungranted. Any
// deviation here would be either a regression back to root-required (missing
// the trio) or a privilege-escalation surface (extra ops/lanes).
func TestPackage_OnlyAllowlistedPkgLifecycleOpsGranted(t *testing.T) {
	wantPrivileged := map[string]bool{
		"InstallPackage":   true,
		"UninstallPackage": true,
		"UpgradePackage":   true,
	}
	neverGranted := map[string]bool{
		"CreateMetaVertex":    true,
		"UpdateMetaVertex":    true,
		"TombstoneMetaVertex": true,
	}
	got := make(map[string]pkgmgr.PermissionSpec, len(Package.Permissions))
	for _, p := range Package.Permissions {
		if neverGranted[p.OperationType] {
			t.Errorf("console-operator must never grant %q", p.OperationType)
		}
		if wantPrivileged[p.OperationType] {
			got[p.OperationType] = p
		} else if len(p.Lanes) != 0 {
			t.Errorf("%s: unexpected privileged Lanes=%v on a non-pkg-lifecycle op", p.OperationType, p.Lanes)
		}
	}
	for op := range wantPrivileged {
		p, ok := got[op]
		if !ok {
			t.Errorf("missing allowlisted pkg-lifecycle grant %q", op)
			continue
		}
		if len(p.Lanes) != 1 || p.Lanes[0] != "meta" {
			t.Errorf("%s: Lanes = %v, want [meta]", op, p.Lanes)
		}
		if p.Scope != "any" {
			t.Errorf("%s: scope = %q, want any", op, p.Scope)
		}
		if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "consoleOperator" {
			t.Errorf("%s: GrantsTo = %v, want [consoleOperator]", op, p.GrantsTo)
		}
	}
}

// TestPackage_EveryOpGrantsOnlyToConsoleOperatorScopeAny pins the full
// default-lane + ctrl.* permission surface and that every one grants only to
// consoleOperator, scope any.
func TestPackage_EveryOpGrantsOnlyToConsoleOperatorScopeAny(t *testing.T) {
	want := []string{
		"ShredIdentityKey", "RevokeActor", "UnrevokeActor", "AttachObject", "DetachObject",
		"ctrl.weaver.read", "ctrl.weaver.disable", "ctrl.weaver.enable", "ctrl.weaver.revoke",
		"ctrl.weaver.resetConfidence",
		"ctrl.loom.read", "ctrl.loom.pause", "ctrl.loom.resume",
		"ctrl.refractor.read", "ctrl.refractor.rebuild", "ctrl.refractor.pause", "ctrl.refractor.resume",
		"ctrl.refractor.delete", "ctrl.refractor.register", "ctrl.refractor.deregister", "ctrl.refractor.hydrate",
		"ctrl.refractor.sessionkey", "ctrl.refractor.syncgap",
		"InstallPackage", "UninstallPackage", "UpgradePackage",
	}
	if len(Package.Permissions) != len(want) {
		t.Fatalf("Permissions = %d, want %d", len(Package.Permissions), len(want))
	}
	got := make(map[string]pkgmgr.PermissionSpec, len(Package.Permissions))
	for _, p := range Package.Permissions {
		got[p.OperationType] = p
	}
	for _, op := range want {
		p, ok := got[op]
		if !ok {
			t.Errorf("missing permission %q", op)
			continue
		}
		if p.Scope != "any" {
			t.Errorf("%s: scope = %q, want any", op, p.Scope)
		}
		if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "consoleOperator" {
			t.Errorf("%s: GrantsTo = %v, want [consoleOperator]", op, p.GrantsTo)
		}
	}
}

// TestPackage_GrantedCtrlVerbsMatchControlauthOpTables is the drift guard
// packages/control-authz's own test names but doesn't share code with (this
// package intentionally doesn't depend on control-authz — see package.go):
// it derives the expected ctrl.<component>.<verb> set DIRECTLY from
// internal/controlauth's WeaverOps/LoomOps/RefractorOps (the source of
// truth) instead of a second hand-maintained literal, so a future op added
// to one table and forgotten here fails HERE.
func TestPackage_GrantedCtrlVerbsMatchControlauthOpTables(t *testing.T) {
	wantByComponent := map[string]map[string]controlauth.OpMeta{
		"weaver":    controlauth.WeaverOps,
		"loom":      controlauth.LoomOps,
		"refractor": controlauth.RefractorOps,
	}
	wantVerbs := map[string]struct{}{}
	for component, ops := range wantByComponent {
		seenVerbs := map[string]struct{}{}
		for _, meta := range ops {
			seenVerbs[meta.Verb] = struct{}{}
		}
		for verb := range seenVerbs {
			wantVerbs["ctrl."+component+"."+verb] = struct{}{}
		}
	}

	gotVerbs := map[string]struct{}{}
	for _, p := range Package.Permissions {
		if p.OperationType == "ShredIdentityKey" || p.OperationType == "RevokeActor" ||
			p.OperationType == "UnrevokeActor" || p.OperationType == "AttachObject" ||
			p.OperationType == "DetachObject" ||
			p.OperationType == "InstallPackage" || p.OperationType == "UninstallPackage" ||
			p.OperationType == "UpgradePackage" {
			continue
		}
		gotVerbs[p.OperationType] = struct{}{}
	}

	for op := range wantVerbs {
		if _, ok := gotVerbs[op]; !ok {
			t.Errorf("internal/controlauth declares an op requiring verb %q, but console-operator grants no such permission (a permanent, ungrantable deny)", op)
		}
	}
	for op := range gotVerbs {
		if _, ok := wantVerbs[op]; !ok {
			t.Errorf("console-operator grants %q, but no internal/controlauth op table resolves to that verb (an orphaned, dead grant)", op)
		}
	}
}
