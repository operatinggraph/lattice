// Package demooperator is the scoped demo-operator Capability Package
// (loupe-f20-demo-operator-ux.md §3.1). It declares a `demoOperator` role
// holding INSPECT-ONLY grants — the non-mutating control-plane reads
// (ctrl.weaver.read, ctrl.loom.read, ctrl.refractor.read) plus this role's
// read-side wildcard grant (demoOperatorReadGrants) — and NOTHING that
// writes. It is packages/console-operator minus every write.
//
// This package is the platform-side boundary that lets Loupe be exposed as a
// public read-only demo. The demonstration's whole claim — "even the console
// is capability-scoped" — rests on the demo identity holding no write grant:
// every mutating op it could submit (ShredIdentityKey, RevokeActor,
// AttachObject/DetachObject, the pkg-lifecycle trio, any mutating
// ctrl.<component>.<verb>, vault decrypt, a generic op-submit) is
// capability-denied at the Processor because the grant simply does not exist,
// not because Loupe's own process refused it. Loupe's LOUPE_DEMO_MODE
// (loupe-f20-demo-operator-ux.md §3.2) is defense in depth layered on top of
// this boundary, never a substitute for it — see §3.1: "Until it exists,
// Loupe must not be exposed."
//
// The three ctrl.*.read grants are the ONLY grants beyond the read lens.
// Each is Read: true in internal/controlauth's op tables (a single
// ctrl.<component>.read grant authorizes every non-mutating op that component
// exposes: weaver `list`; loom `list`/`consumers`/`inspect`; refractor
// `health`/`validate`), so they surface the "behind the scenes" inspection
// views a demo visitor is meant to see while every mutating control verb
// (disable/enable/revoke, pause/resume, rebuild/delete/register/…) stays
// ungranted and therefore denied.
//
// `demoOperator` is a package-installed role deliberately DISTINCT from the
// kernel-primordial `operator` role (`vtx.role.<RoleOperatorID>`,
// canonicalName "operator" — internal/bootstrap/primordial.go): granting to
// the primordial role would make the demo identity root-equivalent, and
// `bootstrap.SystemActorKeys` discovers holders of THAT role to route them to
// the system-actor union read — a scoped demo identity must stay an ordinary
// (non-system) actor so it reads cap.roles.<actor> alone, at the `default`
// lane only, exactly like any other rbac-projected grant. It also carries no
// boot-snapshot dependency: a demoOperator seeded after Processor boot
// authorizes immediately.
//
// Package installers cannot seed an identity vertex or a holdsRole link (only
// Definition.Roles/Permissions — role + permission + grantedBy data — are
// installer-native); provisioning the actual demo identity (or re-scoping an
// identity onto this role) is a separate Loupe-lane wiring step gated on the
// demo's public-launch phase (loupe-f20-demo-operator-ux.md §3.3), not this
// package's scope. This package ships the grant data.
//
// demoOperatorReadGrants is this role's read-side half: a
// capabilityReadWildcardGrants sibling (internal/bootstrap/lenses.go), same
// posture (Andrew's ratified M5 decision, read-path-authorization-d1-
// design.md §8 — wildcard grant through RLS, never a bypass), disjoint
// grant_source ('cap-read.demoOperator'). Without it, re-scoping an identity
// onto demoOperator would silently zero out its Postgres reads (RLS-denied,
// indistinguishable from empty) the moment it stopped holding a
// read-granting role — the same reason console-operator ships its own.
//
// Install via `lattice-pkg install packages/demo-operator` (after
// rbac-domain, identity-domain, privacy-base, objects-base).
package demooperator

import "github.com/asolgan/lattice/internal/pkgmgr"

// demoOperatorReadGrantsSpec mirrors internal/bootstrap/lenses.go's
// CapabilityReadWildcardGrantsLensDefinition cypher exactly, substituting the
// package-installed role name for the primordial one.
const demoOperatorReadGrantsSpec = `MATCH (identity:identity)-[:holdsRole]->(role:role)
WHERE role.canonicalName.data.value = 'demoOperator'
RETURN
  nanoIdFromKey(identity.key) AS actor_id,
  '*'                      AS anchor_id,
  'cap-read.demoOperator'  AS grant_source
`

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "demo-operator",
	Version:     "0.1.0",
	Description: "Grants a scoped demoOperator role the non-mutating ctrl.*.read control-plane grants plus its read-side wildcard grant, and nothing that writes — the platform boundary for a public read-only Loupe demo.",
	Depends:     []string{"rbac-domain", "identity-domain", "privacy-base", "objects-base"},
	Permissions: Permissions(),
	Roles: []pkgmgr.RoleSpec{
		{
			CanonicalName: "demoOperator",
			Description:   "Scoped inspect-only Loupe demo operator: the non-mutating ctrl.*.read control-plane grants + a read-side wildcard grant. Holds no write grant of any kind — not root, no anchor, no privileged lane.",
		},
	},
	Lenses: []pkgmgr.LensSpec{
		{
			CanonicalName: "demoOperatorReadGrants",
			Class:         "meta.lens",
			Adapter:       "postgres",
			GrantTable:    true,
			Engine:        "full",
			Spec:          demoOperatorReadGrantsSpec,
		},
	},
}
