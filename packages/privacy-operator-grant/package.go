// Package privacyoperatorgrant is a grant-only Capability Package: it grants
// the ShredIdentityKey right-to-erasure operation to the `operator` role.
//
// privacy-base deliberately ships NO grant for ShredIdentityKey — right-to-
// erasure is a deployment decision, not a platform default (see
// packages/privacy-base/permissions.go). This package IS that deployment
// decision, made explicit and installable on its own: install it to authorize
// every operator-role holder (the primordial admin — Loupe's operator actor —
// and the platform engine service actors) to submit ShredIdentityKey. Shipping
// it separately keeps privacy-base a pure mechanism and the grant a deliberate,
// revertible step (uninstall revokes it), traced through graph topology per
// Contract #7 §7.7 rather than baked into the mechanism package as a silent
// default.
//
// Decision: Andrew, 2026-07-04 — grant to the operator role (accepting the
// engine service actors' inclusion over a narrower Loupe-only role). One of the
// [Vault→Loupe] surface enablers unblocking Loupe F12's crypto-shred proof
// (loupe-platform-edges-ux.md §6 ask F).
//
// Install via `lattice-pkg install packages/privacy-operator-grant` (after
// rbac-domain + privacy-base). No DDLs or lenses — a grant only.
package privacyoperatorgrant

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "privacy-operator-grant",
	Version:     "0.1.0",
	Description: "Grants the ShredIdentityKey right-to-erasure op to the operator role.",
	Depends:     []string{"rbac-domain", "privacy-base"},
	Permissions: Permissions(),
}
