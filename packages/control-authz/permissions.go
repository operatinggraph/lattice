package controlauthz

import "github.com/asolgan/lattice/internal/pkgmgr"

// Permissions returns the 14 ctrl.<component>.<verb> platform permissions,
// each granting `scope: any` (v1 — the only working platform scope,
// control-plane-capability-authz-design.md §2(a)) to the control-operator
// role. The op→verb tables here MUST stay in lockstep with
// internal/controlauth's WeaverOps/LoomOps/RefractorOps (both are read off
// the same source: each component's control/service.go dispatch table).
func Permissions() []pkgmgr.PermissionSpec {
	perms := []pkgmgr.PermissionSpec{}
	perms = append(perms, componentPermissions("weaver", []string{"read", "disable", "enable", "revoke"})...)
	perms = append(perms, componentPermissions("loom", []string{"read", "pause", "resume"})...)
	perms = append(perms, componentPermissions("refractor", []string{"read", "rebuild", "pause", "resume", "delete", "register", "deregister"})...)
	return perms
}

func componentPermissions(component string, verbs []string) []pkgmgr.PermissionSpec {
	perms := make([]pkgmgr.PermissionSpec, 0, len(verbs))
	for _, verb := range verbs {
		perms = append(perms, pkgmgr.PermissionSpec{
			OperationType: "ctrl." + component + "." + verb,
			Scope:         "any",
			Note:          "Authorizes the control-operator role to invoke the " + component + " control plane's " + verb + " op.",
			GrantsTo:      []string{"control-operator"},
		})
	}
	return perms
}
