package vault

import (
	"fmt"

	"github.com/operatinggraph/lattice/internal/substrate"
)

// KeyHolder returns the vertex key of the key holder whose DEK sealed ct — the
// single answer every decrypt site asks for, so that all of them ask it the
// same way.
//
// The holder is the ciphertext's own keyId, never a value derived from where
// the ciphertext happens to be stored. That is safe for a cryptographic
// reason rather than a conventional one: keyId is the AEAD associated data the
// ciphertext was sealed under (LocalBackend.Encrypt), minted into the Envelope
// under the same string (CreateIdentityKey). Pointing keyId at some other
// holder therefore buys nothing — that holder's real envelope unwraps that
// holder's real DEK, which then fails the GCM tag against a ciphertext sealed
// under a different DEK and a different AAD. keyId is self-authenticating for
// the decrypt path, which is why a decrypt site needs no second, external
// opinion about custody, and why custody can move to a non-anchoring holder
// without any site having to learn how custody is declared.
//
// A keyId that is absent, or is not a well-formed vertex key, is refused.
// There is deliberately no fallback to the aspect's anchor: a fallback would
// open a malformed record under a holder that record never named, and it would
// re-introduce the anchor-derived custody this resolver exists to replace.
//
// Callers wrap the returned error in whatever failure class their plane uses
// (terminal, permanent egress failure, an HTTP 400); the sentinel is
// ErrInvalidEnvelope, matching what a caller gets for presenting an Envelope
// the holder key does not match.
func KeyHolder(ct Ciphertext) (string, error) {
	if ct.KeyID == "" {
		return "", fmt.Errorf("%w: ciphertext carries no keyId, so its key holder is unknowable", ErrInvalidEnvelope)
	}
	if substrate.ClassifyKey(ct.KeyID) != substrate.KindVertex {
		return "", fmt.Errorf("%w: ciphertext keyId %q is not a vertex key", ErrInvalidEnvelope, ct.KeyID)
	}
	return ct.KeyID, nil
}

// KeyHolderKinds is the closed set of key-holder vertex types custody can
// resolve to (Contract #3 §3.10: identity, retentionClass) — the two kinds
// internal/pkgmgr admits at install (custodyscope.go). A ciphertext naming any
// other kind was not written by the Processor's commit path.
func KeyHolderKinds() []string { return []string{"identity", "retentionclass"} }

// IsKeyHolderKind reports whether vertexType is one of KeyHolderKinds — the
// predicate the two external-egress gates share, so the Processor's answer at
// mint and the bridge's at unwrap are one value read twice rather than two
// literals that can drift.
func IsKeyHolderKind(vertexType string) bool {
	for _, kind := range KeyHolderKinds() {
		if vertexType == kind {
			return true
		}
	}
	return false
}

// KeyHolderType returns the vertex-type segment of a key holder key resolved by
// KeyHolder — "identity" for a subject-custodied record, "retentionclass" for
// one whose custody follows a retention obligation instead.
//
// It serves the sites that admit some holder kinds and not others. The two
// egress gates admit exactly the kinds that have a live envelope projection
// (KeyHolderKinds): a third kind is refused at mint with the kind named,
// rather than left to decay into an envelope that never projects — an absence
// the boundary cannot tell from a lagging lens. The wholesale decrypt RPC and
// its console proxy carry neither an actor nor a declared purpose, so the
// Reveal rule (Contract #3 §3.10) admits an identity holder alone there; the
// object-key and session-key RPCs serve identity holders by the object plane's
// own rule (§3.11).
func KeyHolderType(keyHolderKey string) string {
	vertexType, _, ok := substrate.ParseVertexKey(keyHolderKey)
	if !ok {
		return ""
	}
	return vertexType
}
