package substrate

import (
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
)

// DeriveNanoID is the shared deterministic NanoID derivation: a stable hash over
// namespace+input expanded across the canonical alphabet. The namespace prefix
// keeps disjoint derivations from colliding for the same input. The derivation
// is pure (no stored state), so a fresh replica or a restart computes the
// identical id from the same inputs — which is what makes a retried op collapse
// on the Contract #4 vtx.op.<requestId> tracker without shared state (the
// bridge result-op + Loom token idiom).
//
// The output is a bare 20-char NanoID over the canonical Lattice alphabet
// (Contract #1), so it is a valid dot-free op requestId.
func DeriveNanoID(namespace, input string) string {
	sum := sha256.Sum256([]byte(namespace + input))
	id := make([]byte, NanoIDLength)
	// Expand the 32-byte digest across the id by re-hashing as needed.
	digest := sum[:]
	di := 0
	for i := 0; i < NanoIDLength; i++ {
		if di >= len(digest) {
			next := sha256.Sum256(digest)
			digest = next[:]
			di = 0
		}
		id[i] = Alphabet[int(digest[di])%len(Alphabet)]
		di++
	}
	return string(id)
}

// NanoIDFromPCG emits an n-character NanoID from Alphabet using rejection
// sampling against a 6-bit mask, driven by src. It is the deterministic
// generator the Processor's nanoid.new() builtin and SHA256NanoID share, so a
// Go-side caller and a Starlark script derive byte-identical ids from the same
// PCG seed.
func NanoIDFromPCG(src *rand.PCG, n int) string {
	const mask = 63
	out := make([]byte, n)
	written := 0
	for written < n {
		// PCG.Uint64 yields 64 bits; chew through them 6 at a time.
		v := src.Uint64()
		for i := 0; i < 10 && written < n; i++ {
			b := byte(v & mask)
			v >>= 6
			if int(b) < len(Alphabet) {
				out[written] = Alphabet[b]
				written++
			}
		}
	}
	return string(out)
}

// PackageEntityNanoID derives the Contract #1 NanoID a Capability Package
// entity is keyed by, from the package name and an entity tag: the salt is
// `lattice-pkg:<name>:<tag>` and each id character indexes Alphabet with a pair
// of digest bytes. The tag is the entity's logical identity within the package
// ("package", "ddl:<canonicalName>", "lens:<canonicalName>",
// "perm:<operationType>:<scope>", …); a caller wanting a version-scoped id — an
// op requestId rather than an entity key — folds the version into the tag.
//
// A version is deliberately not part of an entity key (Contract #8 §8.1), so
// the same logical entity keeps its key across versions and an upgrade is an
// in-place update rather than a re-mint that would orphan every NanoID
// cross-reference.
//
// This is a different scheme from SHA256NanoID, which seeds a PCG from the
// digest and rejection-samples the alphabet. Every installed package in every
// deployment is addressed by the mapping below, so the mapping is fixed: two
// independent readers — the installer minting a key, and the Processor scoping
// a package-lifecycle mutation to the package that owns it — must compute the
// identical id from the identical name.
func PackageEntityNanoID(name, tag string) string {
	sum := sha256.Sum256([]byte("lattice-pkg:" + name + ":" + tag))
	out := make([]byte, NanoIDLength)
	for i := 0; i < NanoIDLength; i++ {
		hi := sum[(i*2)%len(sum)]
		lo := sum[((i*2)+1)%len(sum)]
		idx := (int(hi)<<8 | int(lo)) % len(Alphabet)
		out[i] = Alphabet[idx]
	}
	return string(out)
}

// SHA256NanoID derives a valid 20-char Contract #1 NanoID from SHA-256(s) — the
// content-addressed identity primitive. It seeds a PCG with the first 16 bytes
// of the digest and rejection-samples the alphabet, so it is deterministic
// (same input → same id, always) and produces an id that satisfies
// substrate.ClassifyKey's NanoID-segment requirement (a raw hex digest does
// not). It is byte-identical to the Starlark crypto.sha256NanoID(s) builtin —
// both call NanoIDFromPCG over the same seed — so a Go-side client (Loupe, the
// GC manager) and the in-script id agree.
func SHA256NanoID(s string) string {
	sum := sha256.Sum256([]byte(s))
	seed := [2]uint64{
		binary.BigEndian.Uint64(sum[0:8]),
		binary.BigEndian.Uint64(sum[8:16]),
	}
	pcg := rand.NewPCG(seed[0], seed[1])
	return NanoIDFromPCG(pcg, NanoIDLength)
}
