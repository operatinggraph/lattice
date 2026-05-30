// Package identitydomain is the identity-domain Capability Package. It
// provides CreateUnclaimedIdentity, UpdateIdentityState, and ClaimIdentity
// operations.
//
// Install via `lattice-pkg install packages/identity-domain`. The install
// is ONE atomic commit routed through the Processor (Story 1.5.5):
//
//   - the 3 user-facing roles (consumer, frontOfHouse, backOfHouse) — role
//     vertex + canonicalName/description aspects + a canonical-name index
//     vertex, with deterministic NanoIDs;
//   - the identity DDL + 3 permission vertices + grantedBy links from those
//     permissions to the relevant roles (frontOfHouse, backOfHouse,
//     operator, consumer).
//
// Everything (roles included) lands in the single install batch and in the
// manifest's declaredKeys, so uninstall reclaims it all (closes F-001).
//
// Depends on rbac-domain being installed first (so the `rbac` DDL can
// govern operator-driven role mutations after install). The installer
// logs a warning but doesn't enforce install order.
package identitydomain

import "github.com/asolgan/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "identity-domain",
	Version:     "0.1.0",
	Description: "Identity vertex creation, claim, and state-machine management.",
	Depends:     []string{"rbac-domain"},
	DDLs:        DDLs(),
	Permissions: Permissions(),
	Roles: []pkgmgr.RoleSpec{
		{CanonicalName: "consumer", Description: "A resident, tenant, or other end-consumer of platform services."},
		{CanonicalName: "frontOfHouse", Description: "Front-of-house staff with visibility into resident-facing operations."},
		{CanonicalName: "backOfHouse", Description: "Back-of-house staff responsible for internal operational tasks."},
	},
}
