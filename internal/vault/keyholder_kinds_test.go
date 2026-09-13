package vault_test

import (
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/vault"
)

// TestKeyHolderKinds_MatchesTheCustodyKindsInstallAdmits cross-checks the
// closed set against its only source of truth: the custody kinds
// internal/pkgmgr admits at install (pkgmgr.CustodyKinds, which
// validateCustodyScope itself reads). Every admitted kind resolves a holder
// whose VERTEX TYPE must be in KeyHolderKinds() — otherwise a package could
// declare custody the egress gates then refuse, or (worse) the set could carry
// a kind no install can produce and admit a ciphertext the commit path never
// wrote.
//
// The key set is ENUMERATED from pkgmgr.CustodyKinds(), so a third kind added
// there fails here for want of a holder-vertex-type mapping rather than
// passing a hand-written pair of entries. The values are the kind →
// holder-vertex-type resolution internal/processor's keyHolderFor performs:
// retentionClass custody names its class holder (vertex type
// RetentionClassVertexType), and identity custody derives the aspect's own
// anchoring identity vertex. The identity side is spelled as a literal because
// pkgmgr exports no identity-vertex-type constant — CustodyKindIdentity is the
// camelCase DECLARED kind string, which definition.go is explicit is a
// different thing from a type segment even where the two spellings coincide.
// This test lives in package vault_test so it may import pkgmgr at all: pkgmgr
// imports vault, so the in-package test binary could not.
func TestKeyHolderKinds_MatchesTheCustodyKindsInstallAdmits(t *testing.T) {
	t.Parallel()

	holderVertexTypeOf := map[string]string{
		pkgmgr.CustodyKindIdentity:       "identity",
		pkgmgr.CustodyKindRetentionClass: pkgmgr.RetentionClassVertexType,
	}
	holderVertexTypeFor := make(map[string]string, len(holderVertexTypeOf))
	for _, custodyKind := range pkgmgr.CustodyKinds() {
		vertexType, mapped := holderVertexTypeOf[custodyKind]
		if !mapped {
			t.Errorf("pkgmgr.CustodyKinds() admits %q, which this test knows no holder vertex type for — name the holder kind it resolves to and pin it, or the egress boundary will meet it first",
				custodyKind)
			continue
		}
		holderVertexTypeFor[custodyKind] = vertexType
	}

	kinds := vault.KeyHolderKinds()
	if len(kinds) != len(holderVertexTypeFor) {
		t.Fatalf("KeyHolderKinds() = %v, want exactly the %d holder vertex types the admitted custody kinds resolve to (%v)",
			kinds, len(holderVertexTypeFor), holderVertexTypeFor)
	}
	inSet := make(map[string]bool, len(kinds))
	for _, kind := range kinds {
		inSet[kind] = true
	}
	for custodyKind, vertexType := range holderVertexTypeFor {
		if !inSet[vertexType] {
			t.Errorf("custody kind %q resolves a %q holder, which KeyHolderKinds() %v does not carry",
				custodyKind, vertexType, kinds)
		}
		if !vault.IsKeyHolderKind(vertexType) {
			t.Errorf("IsKeyHolderKind(%q) = false, want true for the holder custody kind %q resolves", vertexType, custodyKind)
		}
	}
}

// TestIsKeyHolderKind_RefusesEverythingElse: the set is closed, so a vertex
// type outside it — including the camelCase DECLARED custody kind, which is
// never a Contract #1 type segment — is not a key-holder kind.
func TestIsKeyHolderKind_RefusesEverythingElse(t *testing.T) {
	t.Parallel()

	for _, vertexType := range []string{"", "foo", "leaseapp", "appointment", pkgmgr.CustodyKindRetentionClass} {
		if vault.IsKeyHolderKind(vertexType) {
			t.Errorf("IsKeyHolderKind(%q) = true, want false — the set is closed to %v", vertexType, vault.KeyHolderKinds())
		}
	}
}

// TestKeyHolderKinds_CallerCannotWidenTheSet: the set is returned by value per
// call, so a caller that writes into the slice it got cannot widen what the
// next caller (the other egress gate) sees.
func TestKeyHolderKinds_CallerCannotWidenTheSet(t *testing.T) {
	t.Parallel()

	got := vault.KeyHolderKinds()
	got[0] = "foo"
	if vault.IsKeyHolderKind("foo") {
		t.Fatal("IsKeyHolderKind(\"foo\") = true after a caller overwrote its own copy of the set")
	}
}
