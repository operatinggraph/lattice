// Package identitydomain is the identity-domain Capability Package. It
// provides CreateUnclaimedIdentity, UpdateIdentityState, and ClaimIdentity
// operations.
//
// Install via `lattice-pkg install packages/identity-domain`. The
// install runs in two stages:
//
//  1. PreInstall (seed.go): atomic batch creating the 3 user-facing
//     roles (consumer, frontOfHouse, backOfHouse) so the subsequent
//     grant links can reference them. Idempotent via a
//     `vtx.roleindex.<sha256NanoID(rolecanonical:<name>)>` probe.
//  2. Main atomic batch: identity DDL + 3 permission vertices + 5
//     grantedBy links from those permissions to the 4 relevant roles
//     (frontOfHouse, backOfHouse, operator, consumer).
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
	PreInstall:  PreInstall,
}
