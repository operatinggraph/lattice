// Package privacybase is the privacy-base Capability Package. It declares
// the `piiKey` aspect-type DDL — the wrapped-DEK envelope reference
// (Contract #3 §3.10, vault-crypto-shredding-design.md §2.1) that backs
// crypto-shred for sensitive aspects — and the destruction verb of each
// holder kind: ShredIdentityKey erases a person's key on request, and
// ShredRetentionClassKey erases a retention class's key on expiry
// (retention-class-key-custody-design.md §4.3). The two are siblings by
// design: a record custodied on a class outlives its subject's erasure, which
// is the whole reason retained data has a home.
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
// The package also owns the erasure plane's anchor: the erasureRequested
// marker aspect and the SealIdentityForErasure op that writes it
// (erasure-orchestration-design.md §6). Unlike piiKey, that one IS
// script-dispatched — it is the operation whose commit closes an erased
// person's write path, so that the residue an erasure is judged by belongs to
// a set nothing can grow.
//
// And it owns the erasure's shape: the identityErasure Loom pattern (§5), which
// declares the ordered spine, and the identityErasureComplete Weaver target
// over the identityErasureResidue lens (§7), which drives the convergent tail
// the spine cannot express. Step 1 of the pattern binds ShredIdentityKey, whose
// grant this package deliberately does not ship — see permissions.go.
//
// Install via `lattice-pkg install packages/privacy-base`. No dependencies
// — the DDLs attach to identity vertices by convention (Contract #1
// key-shape), not by an install-order coupling with identity-domain. The
// write-path gates that READ the erasureRequested marker live in
// identity-domain and identity-hygiene; the marker is the contract between
// them, which is why it is an explicit aspect rather than a flag on piiKey.
package privacybase

import "github.com/operatinggraph/lattice/internal/pkgmgr"

// Package is the static, install-time bundle.
var Package = pkgmgr.Definition{
	Name:          "privacy-base",
	Version:       "0.15.4",
	Description:   "The key-custody envelope (piiKey) backing crypto-shred for both holder kinds — an identity, erased on request, and a retention class, erased on expiry — the erasureRequested marker that closes an erased identity's write path, the identityErasure Loom pattern that orders the whole erasure, and the attestation that closes the cycle.",
	DDLs:          DDLs(),
	Lenses:        Lenses(),
	Permissions:   Permissions(),
	WeaverTargets: WeaverTargets(),
	LoomPatterns:  LoomPatterns(),
}
