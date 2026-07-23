// Package identitydomain is the identity-domain Capability Package. It
// provides CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity,
// RotateClaimKey, RecordIdentityPII, ProvisionConsumerIdentity,
// InitiateCredentialLink, CompleteCredentialLink, and UnlinkCredential
// operations.
//
// Install via `lattice-pkg install packages/identity-domain`. The install
// is ONE atomic commit routed through the Processor (Story 1.5.5):
//
//   - the 4 roles (consumer, frontOfHouse, backOfHouse — user-facing;
//     identityProvisioner — system-only, granted to the Gateway's own
//     identity by a one-time ops action, never primordially) — role
//     vertex + canonicalName/description aspects + a canonical-name index
//     vertex, with deterministic NanoIDs;
//   - the identity DDL + the ssn/dob sensitive aspect-type DDLs + the
//     permission vertices + grantedBy links from those permissions to the
//     relevant roles (frontOfHouse, backOfHouse, operator, consumer,
//     identityProvisioner).
//
// Everything (roles included) lands in the single install batch and in the
// manifest's declaredKeys, so uninstall reclaims it all (closes F-001).
//
// Depends on rbac-domain being installed first (so the `rbac` DDL can
// govern operator-driven role mutations after install). The installer
// logs a warning but doesn't enforce install order.
package identitydomain

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "identity-domain",
	Version:     "0.5.0",
	Description: "Identity vertex creation, claim, and state-machine management, plus ProvisionConsumerIdentity — the Gateway's idempotent first-authenticated-touch auto-provisioning op (real-actor-write-auth-e2e Phase 1).",
	Depends:     []string{"rbac-domain"},
	DDLs:        DDLs(),
	Permissions: Permissions(),
	Lenses:      Lenses(),
	Roles: []pkgmgr.RoleSpec{
		{CanonicalName: "consumer", Description: "A resident, tenant, or other end-consumer of platform services."},
		{CanonicalName: "frontOfHouse", Description: "Front-of-house staff with visibility into resident-facing operations."},
		{CanonicalName: "backOfHouse", Description: "Back-of-house staff responsible for internal operational tasks."},
		{CanonicalName: "identityProvisioner", Description: "System role for actors that provision bare consumer identities on first authenticated touch. Not a user-facing role."},
	},
}
