// Package privacybase is the privacy-base Capability Package. It declares
// the `piiKey` aspect-type DDL — the per-identity wrapped-DEK envelope
// reference (Contract #3 §3.10, vault-crypto-shredding-design.md §2.1) that
// backs crypto-shred for sensitive aspects.
//
// piiKey is never written by an operation script: the Processor's commit
// path mints and persists it internally (step 6.5 encrypt-on-write, lazily,
// on an identity's first sensitive-aspect write) and reads it internally
// (step 4 / kv.Read decrypt-on-read). This package exists so the class is
// registered in the DDL cache like every other meta-vertex (Contract #7
// registration + Loupe/tooling introspection), not because any script
// dispatches against it.
//
// piiKey itself is NOT sensitive: it holds only the wrapped (ciphertext)
// DEK, never plaintext key material or PII.
//
// Install via `lattice-pkg install packages/privacy-base`. No dependencies
// — the DDL attaches to identity vertices by convention (Contract #1
// key-shape), not by an install-order coupling with identity-domain.
package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:        "privacy-base",
	Version:     "0.2.0",
	Description: "Per-identity PII key-custody envelope (piiKey) backing crypto-shred.",
	DDLs:        DDLs(),
	Lenses:      Lenses(),
	Permissions: Permissions(),
}
