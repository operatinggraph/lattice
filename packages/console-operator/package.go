// Package consoleoperator is the scoped console-operator Capability Package
// (loupe-operator-auth-lift-design.md mechanism B,
// scoped-privileged-lane-grants-design.md §3.4 "consoleOperator stays an
// ordinary actor", §7 item 3). It declares a `consoleOperator` role and
// grants it the default-lane console ops (ShredIdentityKey, RevokeActor,
// UnrevokeActor, AttachObject, DetachObject) plus the
// ctrl.<component>.<verb> control-plane ops plus the allowlisted
// pkg-lifecycle trio (InstallPackage/UninstallPackage/UpgradePackage) at the
// `meta` lane — everything a Loupe operator needs for routine console use,
// including running the package-lifecycle tab, without root.
//
// `consoleOperator` is a package-installed role deliberately DISTINCT from
// the kernel-primordial `operator` role (`vtx.role.<RoleOperatorID>`,
// canonicalName "operator" — internal/bootstrap/primordial.go): granting to
// the primordial role would make every console operator indistinguishable
// from root-equivalent kernel-meta privilege, and `bootstrap.SystemActorKeys`
// specifically discovers holders of THAT role to route them to the
// system-actor union read — a scoped console identity must stay an ordinary
// (non-system) actor so it reads cap.roles.<actor> alone, at the `default`
// lane only, exactly like any other rbac-projected grant. This also removes
// the boot-snapshot dependency the primordial `operator` anchor carries: a
// consoleOperator seeded after Processor boot authorizes immediately.
//
// Package installers cannot seed an identity vertex or a holdsRole link
// (only Definition.Roles/Permissions — role + permission + grantedBy data —
// are installer-native); provisioning the actual console-operator identity
// (or re-scoping Loupe's existing seeded operator identity onto this role)
// is a separate Loupe-lane wiring step
// (loupe-operator-auth-lift-design.md §7 decomposition items 4-6), not this
// package's scope. This package ships the grant data.
//
// The pkg-lifecycle ops (InstallPackage/UninstallPackage/UpgradePackage) ARE
// granted here, at the `meta` lane, via mechanism C
// (scoped-privileged-lane-grants-design.md §7 item 3): the Processor's core
// privileged-lane allowlist honors exactly this {op, lane} set for a
// package-projected `cap.roles` grant, so consoleOperator can run the pkg tab
// without holding the root-equivalent primordial `operator` role.
//
// consoleOperatorReadGrants is this package's read-side half: a
// capabilityReadWildcardGrants sibling (internal/bootstrap/lenses.go), same
// posture (Andrew's ratified M5 decision, read-path-authorization-d1-
// design.md §8 — wildcard grant through RLS, never a bypass), disjoint
// grant_source ('cap-read.consoleOperator' vs the kernel's 'cap-read.root').
// The kernel lens deliberately keys on the primordial `operator` role only
// ("not a package-vocabulary walk" — its own doc comment) and cannot cover a
// package-installed role without crossing that layering boundary; this lens
// is where that boundary is meant to be crossed instead — a package granting
// its OWN role's read-side counterpart to its OWN write-side grants, exactly
// like clinic-domain's clinicPatientReadGrants sits beside its patient
// permissions. Without this, re-scoping Loupe's operator identity onto
// consoleOperator (mechanism B, below) would silently zero out its Postgres
// reads (RLS-denied, indistinguishable from empty) the moment it stopped
// holding the primordial `operator` role.
//
// Install via `lattice-pkg install packages/console-operator` (after
// rbac-domain, identity-domain, privacy-base, objects-base).
package consoleoperator

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// consoleOperatorReadGrantsSpec mirrors internal/bootstrap/lenses.go's
// CapabilityReadWildcardGrantsLensDefinition cypher exactly, substituting the
// package-installed role name for the primordial one.
const consoleOperatorReadGrantsSpec = `MATCH (identity:identity)-[:holdsRole]->(role:role)
WHERE role.canonicalName.data.value = 'consoleOperator'
RETURN
  nanoIdFromKey(identity.key) AS actor_id,
  '*'                         AS anchor_id,
  'cap-read.consoleOperator'  AS grant_source
`

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "console-operator",
	Version:     "0.5.0",
	Description: "Grants a scoped consoleOperator role the default-lane console ops + ctrl.* control-plane ops + the allowlisted pkg-lifecycle trio at meta, without root, plus its read-side wildcard grant.",
	Depends:     []string{"rbac-domain", "identity-domain", "privacy-base", "objects-base"},
	Permissions: Permissions(),
	Roles: []pkgmgr.RoleSpec{
		{
			CanonicalName: "consoleOperator",
			Description:   "Scoped Loupe console operator: default-lane console ops (shred/revoke/object) + ctrl.* control-plane ops + the allowlisted pkg-lifecycle trio at meta. Not root — no anchor, no other privileged lane.",
		},
	},
	Lenses: []pkgmgr.LensSpec{
		{
			CanonicalName: "consoleOperatorReadGrants",
			Class:         "meta.lens",
			Adapter:       "postgres",
			GrantTable:    true,
			Engine:        "full",
			Spec:          consoleOperatorReadGrantsSpec,
		},
	},
}
