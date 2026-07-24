// Package controlauthz is the control-plane authorization Capability
// Package (FR30 Fire 1b, control-plane-capability-authz-design.md §3.5). It
// grants the `ctrl.<component>.<verb>` platform permissions — one per
// Weaver/Loom/Refractor control-plane op (§2(c)) — to a new `control-operator`
// role, so internal/controlauth.CapabilityKVChecker has something to check
// once its cmd/{weaver,refractor,loom} wiring flips LATTICE_AUTH_MODE to
// `capability`.
//
// `control-operator` is a package-installed role deliberately DISTINCT from
// the kernel-primordial `operator` role (`vtx.role.<RoleOperatorID>`,
// canonicalName "operator" — internal/bootstrap/primordial.go): granting to
// the primordial role would make every control grant indistinguishable from
// root-equivalent kernel-meta privilege, and `bootstrap.SystemActorKeys`
// specifically discovers holders of THAT role to route them to the
// system-actor union read — an identity meant only to hold control-plane
// grants must stay an ordinary (non-system) actor so it reads
// cap.roles.<actor> alone, exactly like any other rbac-projected grant.
//
// Package installers cannot seed an identity vertex or a holdsRole link
// (only Definition.Roles/Permissions — role + permission + grantedBy data —
// are installer-native; internal/bootstrap/primordial.go is the only place
// that seeds identity+holdsRole today, and that is kernel-bootstrap code, not
// a package install). So this package ships the GRANT data only; provisioning
// the actual control-operator identity is a one-time post-install op
// sequence an operator runs once (CreateUnclaimedIdentity + AssignRole
// "control-operator" — both ordinary identity-domain/rbac-domain verbs), then
// points LOUPE_OPERATOR_ACTOR_KEY / each CLI credential file's actorKey at it.
// (Design §7 already defers "per-operator human identity provisioning" as a
// Stream-3 decision — this package doesn't reopen that scope.)
//
// Install via `lattice-pkg install packages/control-authz` (after
// rbac-domain). No DDLs or lenses — a grant-only package (mirrors
// packages/privacy-operator-grant's shape).
package controlauthz

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "control-authz",
	Version:     "0.7.0",
	Description: "Grants ctrl.<component>.<verb> control-plane permissions to a new control-operator role, plus the five identity-bound Personal Lens ops to consumer / frontOfHouse / backOfHouse / provider (§3.4-confined).",
	Depends:     []string{"rbac-domain", "identity-domain"},
	Permissions: Permissions(),
	Roles: []pkgmgr.RoleSpec{
		{
			CanonicalName: "control-operator",
			Description:   "Holds every ctrl.<component>.<verb> platform permission — the Weaver/Loom/Refractor control-plane operator role.",
		},
	},
}
