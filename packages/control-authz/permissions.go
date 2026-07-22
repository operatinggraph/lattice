package controlauthz

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Permissions returns the 18 ctrl.<component>.<verb> platform permissions,
// each granting `scope: any` (v1 — the only working platform scope,
// control-plane-capability-authz-design.md §2(a)) to the control-operator
// role. The op→verb tables here MUST stay in lockstep with
// internal/controlauth's WeaverOps/LoomOps/RefractorOps (both are read off
// the same source: each component's control/service.go dispatch table).
//
// The five identity-bound Personal Lens ops (register/deregister/hydrate/
// sessionkey/syncgap) additionally grant to the consumer role —
// per-identity-nats-subscribe-acl-design.md §3.3/§3.4 (Fire 2);
// edge-lattice-full-design.md §3.6 (EDGE.4) for sessionkey;
// edge-syncgap-control-rpc-design.md for syncgap (warm-resume gap detection).
// Safe only because internal/refractor/control/service.go's dispatchEndpoint
// unconditionally binds these ops' body.IdentityID to the caller's own
// verified actor, confining the effect to the caller's own identity
// regardless of capability scope — see personalLensPermissions.
func Permissions() []pkgmgr.PermissionSpec {
	perms := []pkgmgr.PermissionSpec{}
	perms = append(perms, componentPermissions("weaver", []string{"read", "disable", "enable", "revoke", "resetConfidence"})...)
	perms = append(perms, componentPermissions("loom", []string{"read", "pause", "resume"})...)
	perms = append(perms, componentPermissions("refractor", []string{"read", "rebuild", "pause", "resume", "delete"})...)
	perms = append(perms, personalLensPermissions("register", "deregister", "hydrate", "sessionkey", "syncgap")...)
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

// personalLensPermissions grants the given refractor verbs to control-operator
// and to every role whose holders run a syncing client — the identity-bound
// Personal Lens ops (§3.4 confines each to the caller's own identity
// server-side, so a broad scope=any grant never lets one identity act on
// another's interest set).
//
// `frontOfHouse` and `backOfHouse` are here for the same reason `consumer` is,
// and it is not optional: registering Personal Lens interest is the FIRST thing
// a client does, so without this grant a staff device cannot sync at all — the
// sync manager fails "personal.register: actor lacks the control grant" and
// retries forever, leaving the whole staff world empty rather than partially
// degraded. Any future role whose holders sign into a mirroring client needs
// adding here too — that rule is pinned by a test rather than left to memory.
func personalLensPermissions(verbs ...string) []pkgmgr.PermissionSpec {
	perms := make([]pkgmgr.PermissionSpec, 0, len(verbs))
	for _, verb := range verbs {
		perms = append(perms, pkgmgr.PermissionSpec{
			OperationType: "ctrl.refractor." + verb,
			Scope:         "any",
			Note:          "Authorizes control-operator, or any consumer / frontOfHouse / backOfHouse identity acting on its own Personal Lens interest set (bound server-side, §3.4), to invoke the refractor control plane's " + verb + " op.",
			GrantsTo:      []string{"control-operator", "consumer", "frontOfHouse", "backOfHouse"},
		})
	}
	return perms
}
