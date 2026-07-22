package demooperator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asolgan/lattice/internal/controlauth"
	"github.com/asolgan/lattice/internal/pkgmgr"
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

// TestPackage_NoDDLsExactlyOneReadGrantLens pins the package's shape: no DDLs
// (an ordinary rbac/grant package, mirrors packages/console-operator,
// packages/control-authz), plus exactly ONE lens — its own read-side
// wildcard-grant producer (demoOperatorReadGrants), never a business read
// model.
func TestPackage_NoDDLsExactlyOneReadGrantLens(t *testing.T) {
	if len(Package.DDLs) != 0 {
		t.Fatalf("package must declare no DDLs; got %d", len(Package.DDLs))
	}
	if len(Package.Lenses) != 1 {
		t.Fatalf("package must declare exactly 1 lens (its read-grant producer); got %d", len(Package.Lenses))
	}
	lens := Package.Lenses[0]
	if lens.CanonicalName != "demoOperatorReadGrants" {
		t.Errorf("lens CanonicalName = %q, want %q", lens.CanonicalName, "demoOperatorReadGrants")
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

// TestPackage_ReadGrantLensMatchesDemoOperatorOnly pins the cypher's
// exclusivity: it must key on the demoOperator role specifically, never the
// primordial `operator` role (that's the kernel's capabilityReadWildcardGrants
// lens's job) and never `consoleOperator` (a copy-paste hazard from the
// package it was cloned from) — a package granting read-side wildcard access
// to the wrong role is a silent over- or under-grant.
func TestPackage_ReadGrantLensMatchesDemoOperatorOnly(t *testing.T) {
	spec := Package.Lenses[0].Spec
	if !strings.Contains(spec, "'demoOperator'") {
		t.Errorf("lens spec must match role.canonicalName = 'demoOperator': %s", spec)
	}
	if strings.Contains(spec, "'operator'") {
		t.Errorf("lens spec must not reference the primordial 'operator' role — that's the kernel lens's job: %s", spec)
	}
	if strings.Contains(spec, "'consoleOperator'") {
		t.Errorf("lens spec must not reference 'consoleOperator' (clone hazard): %s", spec)
	}
	if !strings.Contains(spec, "'cap-read.demoOperator'") {
		t.Errorf("lens spec must tag its own disjoint grant_source 'cap-read.demoOperator': %s", spec)
	}
	if !strings.Contains(spec, "'*'") {
		t.Errorf("lens spec must grant the wildcard anchor '*': %s", spec)
	}
}

// TestPackage_DeclaresDemoOperatorRoleDistinctFromPrimordialOperator pins the
// naming decision: the new role is "demoOperator", never "operator" (the
// kernel-primordial, root-equivalent role every SystemActorKeys() holder is
// discovered by).
func TestPackage_DeclaresDemoOperatorRoleDistinctFromPrimordialOperator(t *testing.T) {
	if len(Package.Roles) != 1 {
		t.Fatalf("Roles = %d, want exactly 1", len(Package.Roles))
	}
	role := Package.Roles[0]
	if role.CanonicalName != "demoOperator" {
		t.Fatalf("role CanonicalName = %q, want %q (must not collide with the primordial %q role)",
			role.CanonicalName, "demoOperator", "operator")
	}
}

// TestPackage_GrantsExactlyTheThreeReadOnlyCtrlOps is the load-bearing test:
// the demoOperator surface is EXACTLY {ctrl.weaver.read, ctrl.loom.read,
// ctrl.refractor.read} — no more, no less — each granted only to demoOperator,
// scope any, with no privileged Lanes. Any addition here is a privilege-
// escalation surface on a role whose entire point is that it can only inspect.
func TestPackage_GrantsExactlyTheThreeReadOnlyCtrlOps(t *testing.T) {
	want := []string{"ctrl.weaver.read", "ctrl.loom.read", "ctrl.refractor.read"}
	if len(Package.Permissions) != len(want) {
		t.Fatalf("Permissions = %d, want %d (%v)", len(Package.Permissions), len(want), want)
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
		if len(p.Lanes) != 0 {
			t.Errorf("%s: unexpected privileged Lanes=%v — the demo role must never touch a privileged lane", op, p.Lanes)
		}
		if len(p.GrantsTo) != 1 || p.GrantsTo[0] != "demoOperator" {
			t.Errorf("%s: GrantsTo = %v, want [demoOperator]", op, p.GrantsTo)
		}
	}
}

// TestPackage_NeverGrantsAnyWriteOp pins §3.1's boundary directly: none of the
// write surfaces the demo posture must deny may appear as a grant here. This
// is the "minus every write" invariant stated as an explicit deny-list so a
// future edit that copies one more line from console-operator fails HERE, not
// in production behind a public URL.
func TestPackage_NeverGrantsAnyWriteOp(t *testing.T) {
	// Every write op console-operator grants, plus the other §3.1-named
	// denials (vault decrypt, generic op-submit stand-ins). If any of these
	// is ever granted, the demo identity can mutate the platform.
	forbidden := map[string]bool{
		"ShredIdentityKey":    true,
		"RevokeActor":         true,
		"UnrevokeActor":       true,
		"AttachObject":        true,
		"DetachObject":        true,
		"InstallPackage":      true,
		"UninstallPackage":    true,
		"UpgradePackage":      true,
		"ReviewProposal":      true,
		"CreateMetaVertex":    true,
		"UpdateMetaVertex":    true,
		"TombstoneMetaVertex": true,
		"ctrl.weaver.disable": true,
		"ctrl.weaver.enable":  true,
		"ctrl.weaver.revoke":  true,
		// A confidence reset deletes engine state — narrow and advisory-only,
		// but a write. F20.3's inspect-only boundary keeps it out.
		"ctrl.weaver.resetConfidence": true,
		"ctrl.loom.pause":             true,
		"ctrl.loom.resume":            true,
		"ctrl.refractor.rebuild":      true,
		"ctrl.refractor.pause":        true,
		"ctrl.refractor.resume":       true,
		"ctrl.refractor.delete":       true,
		"ctrl.refractor.register":     true,
		"ctrl.refractor.deregister":   true,
		"ctrl.refractor.hydrate":      true,
		"ctrl.refractor.sessionkey":   true,
		// ctrl.refractor.syncgap is Read: true in controlauth, so it is a read,
		// not a write — it is deliberately not granted (the demo doesn't need
		// it and Loupe never invokes it) but it belongs to the read-only
		// invariant in TestPackage_EveryGrantedCtrlVerbIsReadOnly, not this
		// write deny-list.
	}
	for _, p := range Package.Permissions {
		if forbidden[p.OperationType] {
			t.Errorf("demo-operator must never grant the write op %q — that breaks the read-only demo boundary (§3.1)", p.OperationType)
		}
	}
}

// TestPackage_EveryGrantedCtrlVerbIsReadOnly derives the read/mutate
// classification DIRECTLY from internal/controlauth's op tables (the source of
// truth) and asserts every ctrl.* verb this package grants maps ONLY to
// Read: true ops, and never to a Read: false (mutating) op. This is the drift
// guard: if a future controlauth change reclassified `read` to cover a
// mutating op, or this package gained a mutating verb, it fails HERE.
func TestPackage_EveryGrantedCtrlVerbIsReadOnly(t *testing.T) {
	opsByComponent := map[string]map[string]controlauth.OpMeta{
		"weaver":    controlauth.WeaverOps,
		"loom":      controlauth.LoomOps,
		"refractor": controlauth.RefractorOps,
	}

	// readVerbs[component] = the set of verbs that are exclusively Read: true.
	// mutateVerbs[component] = any verb that resolves to a Read: false op.
	readVerbs := map[string]map[string]bool{}
	mutateVerbs := map[string]map[string]bool{}
	for comp, ops := range opsByComponent {
		readVerbs[comp] = map[string]bool{}
		mutateVerbs[comp] = map[string]bool{}
		for _, meta := range ops {
			if meta.Read {
				readVerbs[comp][meta.Verb] = true
			} else {
				mutateVerbs[comp][meta.Verb] = true
			}
		}
	}

	for _, p := range Package.Permissions {
		parts := strings.Split(p.OperationType, ".")
		if len(parts) != 3 || parts[0] != "ctrl" {
			t.Fatalf("unexpected non-ctrl permission on an inspect-only package: %q", p.OperationType)
		}
		comp, verb := parts[1], parts[2]
		if mutateVerbs[comp][verb] {
			t.Errorf("%s: verb %q resolves to a Read: false (mutating) op in controlauth — must not be granted", p.OperationType, verb)
		}
		if !readVerbs[comp][verb] {
			t.Errorf("%s: verb %q does not resolve to any Read: true op in controlauth — an orphaned, dead grant", p.OperationType, verb)
		}
	}
}
