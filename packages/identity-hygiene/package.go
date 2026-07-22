// Package identityhygiene is an installable Capability Package providing:
//
//   - MergeIdentity operation (gated by a new DDL `identityHygiene`)
//   - duplicateCandidates Lens (cypher matching exact-email,
//     exact-phone, levenshteinRatio >= 0.85 on names)
//   - MergeIdentity permission + grant to the operator role
//
// Install via `lattice-pkg install packages/identity-hygiene`.
// See docs/components/_packages.md for the install contract.
package identityhygiene

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle. cmd/lattice-pkg imports
// this variable when dispatching `install identity-hygiene`.
var Package = pkgmgr.Definition{
	Name:        "identity-hygiene",
	Version:     "0.2.0",
	Description: "Duplicate-identity detection + operator-approved merge.",
	Depends:     []string{"identity-domain"},
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
