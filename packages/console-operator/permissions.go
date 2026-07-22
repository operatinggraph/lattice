package consoleoperator

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions grants the consoleOperator role the default-lane console ops
// (shred/revoke/object) plus the ctrl.<component>.<verb> control-plane ops —
// loupe-operator-auth-lift-design.md §3.4/§4 (mechanism B) — plus the
// allowlisted pkg-lifecycle trio at the `meta` lane
// (scoped-privileged-lane-grants-design.md §7 item 3, mechanism C). The
// default-lane ops are granted at the `cap.roles` default lane, the same
// tier every ordinary role reads (no anchor, no `SystemActorKeys`, no
// boot-snapshot dependency); the pkg-lifecycle trio carries its own per-op
// `Lanes:["meta"]`, honored by the Processor only because
// `{InstallPackage/UninstallPackage/UpgradePackage, meta}` is on the core
// privileged-lane allowlist — consoleOperator stays an ordinary `cap.roles`
// actor even with this grant.
//
// The five default-lane ops are additive alongside their existing `operator`
// grants (privacy-operator-grant, identity-domain, objects-base) — this
// package does not touch those; it gives the SAME ops to a second, narrower
// role so an actor need not hold the root-equivalent primordial `operator`
// role to use the console. The ctrl.* set mirrors packages/control-authz's
// control-operator grants verbatim (componentPermissions here, not imported,
// so this package's dependency graph doesn't couple to control-authz).
func Permissions() []pkgmgr.PermissionSpec {
	perms := []pkgmgr.PermissionSpec{
		{
			OperationType: "ShredIdentityKey",
			Scope:         "any",
			Note:          "Authorizes consoleOperator to submit ShredIdentityKey (right-to-erasure) without holding root.",
			GrantsTo:      []string{"consoleOperator"},
		},
		{
			OperationType: "RevokeActor",
			Scope:         "any",
			Note:          "Authorizes consoleOperator to submit the Gateway revoke kill-switch without holding root.",
			GrantsTo:      []string{"consoleOperator"},
		},
		{
			OperationType: "UnrevokeActor",
			Scope:         "any",
			Note:          "Authorizes consoleOperator to reverse a token revocation without holding root.",
			GrantsTo:      []string{"consoleOperator"},
		},
		{
			OperationType: "AttachObject",
			Scope:         "any",
			Note:          "Authorizes consoleOperator to submit AttachObject without holding root.",
			GrantsTo:      []string{"consoleOperator"},
		},
		{
			OperationType: "DetachObject",
			Scope:         "any",
			Note:          "Authorizes consoleOperator to submit DetachObject without holding root.",
			GrantsTo:      []string{"consoleOperator"},
		},
	}
	perms = append(perms, componentPermissions("weaver", []string{"read", "disable", "enable", "revoke", "resetConfidence"})...)
	perms = append(perms, componentPermissions("loom", []string{"read", "pause", "resume"})...)
	perms = append(perms, componentPermissions("refractor", []string{"read", "rebuild", "pause", "resume", "delete", "register", "deregister", "hydrate", "sessionkey", "syncgap"})...)
	perms = append(perms, pkgLifecyclePermissions()...)
	return perms
}

// pkgLifecyclePermissions grants consoleOperator the allowlisted
// pkg-lifecycle trio at the `meta` lane
// (scoped-privileged-lane-grants-design.md §7 item 3): the Processor's core
// privileged-lane allowlist honors exactly this {operationType, lane} set for
// a package-projected `cap.roles` grant, so this does not confer root — every
// other privileged lane (urgent/system) and every other meta-lane op
// (CreateMetaVertex/UpdateMetaVertex/TombstoneMetaVertex) stays ungranted.
func pkgLifecyclePermissions() []pkgmgr.PermissionSpec {
	trio := []string{"InstallPackage", "UninstallPackage", "UpgradePackage"}
	perms := make([]pkgmgr.PermissionSpec, 0, len(trio))
	for _, op := range trio {
		perms = append(perms, pkgmgr.PermissionSpec{
			OperationType: op,
			Scope:         "any",
			Lanes:         []string{"meta"},
			Note:          "Authorizes consoleOperator to submit " + op + " at the meta lane (core privileged-lane allowlist, mechanism C).",
			GrantsTo:      []string{"consoleOperator"},
		})
	}
	return perms
}

func componentPermissions(component string, verbs []string) []pkgmgr.PermissionSpec {
	perms := make([]pkgmgr.PermissionSpec, 0, len(verbs))
	for _, verb := range verbs {
		perms = append(perms, pkgmgr.PermissionSpec{
			OperationType: "ctrl." + component + "." + verb,
			Scope:         "any",
			Note:          "Authorizes the consoleOperator role to invoke the " + component + " control plane's " + verb + " op.",
			GrantsTo:      []string{"consoleOperator"},
		})
	}
	return perms
}
