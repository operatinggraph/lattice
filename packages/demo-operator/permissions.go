package demooperator

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions grants the demoOperator role ONLY the non-mutating
// control-plane reads — ctrl.weaver.read, ctrl.loom.read, ctrl.refractor.read
// (loupe-f20-demo-operator-ux.md §3.1). Every one resolves to a Read: true op
// in internal/controlauth's WeaverOps/LoomOps/RefractorOps tables, so this set
// is exactly the inspect-only control surface: a single ctrl.<component>.read
// grant authorizes every non-mutating op that component exposes (weaver
// `list`; loom `list`/`consumers`/`inspect`; refractor `health`/`validate`),
// while every mutating verb (disable/enable/revoke, pause/resume,
// rebuild/delete/register/deregister/hydrate/sessionkey/syncgap) is granted to
// no one here and is therefore denied.
//
// This package grants NO write op — not the default-lane console ops
// (ShredIdentityKey/RevokeActor/UnrevokeActor/AttachObject/DetachObject), not
// the pkg-lifecycle trio, not a generic op-submit, not vault decrypt. That
// absence IS the demo boundary: a public visitor holding the demo identity
// can inspect the running platform but cannot mutate it, because the
// capability grant does not exist. Contrast packages/console-operator, which
// is this role plus the full write surface.
func Permissions() []pkgmgr.PermissionSpec {
	perms := make([]pkgmgr.PermissionSpec, 0, 3)
	perms = append(perms, componentReadPermission("weaver"))
	perms = append(perms, componentReadPermission("loom"))
	perms = append(perms, componentReadPermission("refractor"))
	return perms
}

// componentReadPermission returns the single non-mutating ctrl.<component>.read
// grant. The `read` verb is the one internal/controlauth maps every Read: true
// op of that component to (checker.go: `want := "ctrl." + component + "." +
// meta.Verb`), so this one grant covers the component's whole inspect surface
// without opening any mutating verb.
func componentReadPermission(component string) pkgmgr.PermissionSpec {
	return pkgmgr.PermissionSpec{
		OperationType: "ctrl." + component + ".read",
		Scope:         "any",
		Note:          "Authorizes the demoOperator role to invoke the " + component + " control plane's non-mutating (Read: true) ops only.",
		GrantsTo:      []string{"demoOperator"},
	}
}
