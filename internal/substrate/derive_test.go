package substrate

import (
	"crypto/sha256"
	"encoding/binary"
	"math/rand/v2"
	"strings"
	"testing"
)

// deriveViaDocumentedSeedWiring independently reconstructs the seed derivation
// SHA256NanoID's doc-comment specifies — a PCG seeded from the first 16 bytes
// of SHA-256(s), big-endian — so the cross-check pins the wiring rather than
// trivially re-calling the implementation.
func deriveViaDocumentedSeedWiring(s string) string {
	sum := sha256.Sum256([]byte(s))
	pcg := rand.NewPCG(binary.BigEndian.Uint64(sum[0:8]), binary.BigEndian.Uint64(sum[8:16]))
	return NanoIDFromPCG(pcg, NanoIDLength)
}

// The derive.go primitives are the platform's deterministic-id backbone:
// DeriveNanoID collapses a retried op onto one vtx.op.<requestId> tracker
// (bridge reply-op, Loom token), and SHA256NanoID is the content-addressed
// object identity. Their doc-comments make hard claims — every output is a
// valid Contract #1 NanoID (dot-free, 20 chars, alphabet-only, so it passes
// ClassifyKey's NanoID-segment gate) and the generation is byte-identical to
// the Starlark crypto builtins. These tests pin those invariants so a refactor
// of the alphabet order, the 6-bit masking, or the seed derivation can never
// silently change an id and break op de-dup / object addressing across the
// fleet.

// allFromAlphabet reports whether every byte of s is in the canonical alphabet.
func allFromAlphabet(s string) bool {
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(Alphabet, s[i]) < 0 {
			return false
		}
	}
	return true
}

func TestDeriveNanoID_DeterministicAndValid(t *testing.T) {
	cases := []struct{ ns, input string }{
		{"bridge:reply:", "req-123"},
		{"loom:token:", "pattern-abc/instance-xyz"},
		{"", ""},
		{"x", strings.Repeat("y", 4096)},
		{"unicode:", "café—naïve—日本語"},
	}
	for _, c := range cases {
		got := DeriveNanoID(c.ns, c.input)

		// Determinism: a fresh replica / restart must compute the same id —
		// this is what lets a retried op collapse without shared state.
		if again := DeriveNanoID(c.ns, c.input); again != got {
			t.Errorf("DeriveNanoID(%q,%q) not deterministic: %q != %q", c.ns, c.input, got, again)
		}
		// Output is a valid Contract #1 NanoID: 20 chars, alphabet-only, and
		// dot-free so it is a legal op requestId segment.
		if len(got) != NanoIDLength {
			t.Errorf("DeriveNanoID(%q,%q) length = %d, want %d", c.ns, c.input, len(got), NanoIDLength)
		}
		if !IsValidNanoID(got) {
			t.Errorf("DeriveNanoID(%q,%q) = %q is not a valid NanoID", c.ns, c.input, got)
		}
		if strings.Contains(got, ".") {
			t.Errorf("DeriveNanoID(%q,%q) = %q contains a dot (not a legal op requestId)", c.ns, c.input, got)
		}
		// It must classify as a vertex/aspect segment, i.e. survive ClassifyKey
		// as the 3rd segment of a vtx key — the property the doc-comment claims.
		if ClassifyKey("vtx.op."+got) != KindVertex {
			t.Errorf("vtx.op.%s did not classify as a vertex key", got)
		}
	}
}

func TestDeriveNanoID_NamespaceIsolation(t *testing.T) {
	// The namespace prefix keeps disjoint derivations from colliding for the
	// same input — distinct namespaces (the real usage: distinct prefix
	// constants) must yield distinct ids.
	const input = "req-123"
	a := DeriveNanoID("bridge:reply:", input)
	b := DeriveNanoID("loom:token:", input)
	c := DeriveNanoID("object:attach:", input)
	if a == b || a == c || b == c {
		t.Errorf("namespace isolation broken: %q / %q / %q", a, b, c)
	}

	// Different inputs under one namespace also diverge.
	if DeriveNanoID("ns:", "a") == DeriveNanoID("ns:", "b") {
		t.Error("DeriveNanoID collapsed two distinct inputs under one namespace")
	}
}

func TestDeriveNanoID_Golden(t *testing.T) {
	// Regression anchor: a change to the alphabet order, the digest expansion,
	// or the modulo mapping would shift these. The same byte sequence must be
	// reproduced by any future refactor — a moved id silently breaks every
	// already-issued op tracker and object pointer.
	golden := map[[2]string]string{
		{"bridge:reply:", "req-123"}: "EjraDYAJJPP3GXkv8ooM",
		{"", ""}:                     "5CYJnWeWpVNco5MnqAH6",
	}
	for in, want := range golden {
		if got := DeriveNanoID(in[0], in[1]); got != want {
			t.Errorf("DeriveNanoID(%q,%q) = %q, want golden %q", in[0], in[1], got, want)
		}
	}
}

func TestNanoIDFromPCG_LengthDeterminismAlphabet(t *testing.T) {
	for _, n := range []int{0, 1, 8, 20, 64} {
		out := NanoIDFromPCG(rand.NewPCG(7, 11), n)
		if len(out) != n {
			t.Errorf("NanoIDFromPCG(_, %d) length = %d, want %d", n, len(out), n)
		}
		if !allFromAlphabet(out) {
			t.Errorf("NanoIDFromPCG(_, %d) = %q has a char outside the alphabet", n, out)
		}
		// Same seed → byte-identical output (the property the Starlark
		// nanoid.new() builtin relies on for Go/script agreement).
		if again := NanoIDFromPCG(rand.NewPCG(7, 11), n); again != out {
			t.Errorf("NanoIDFromPCG(_, %d) not deterministic for a fixed seed: %q != %q", n, out, again)
		}
	}

	// Distinct seeds yield distinct streams (sanity, at full key length).
	if NanoIDFromPCG(rand.NewPCG(1, 2), 20) == NanoIDFromPCG(rand.NewPCG(3, 4), 20) {
		t.Error("distinct PCG seeds produced identical NanoIDs")
	}
}

func TestNanoIDFromPCG_Golden(t *testing.T) {
	if got := NanoIDFromPCG(rand.NewPCG(1, 2), 20); got != "SWyYbas5Vv1ghH4zQ7JC" {
		t.Errorf("NanoIDFromPCG(seed 1,2, 20) = %q, want golden %q", got, "SWyYbas5Vv1ghH4zQ7JC")
	}
}

func TestSHA256NanoID_DeterministicValidAndDistinct(t *testing.T) {
	inputs := []string{"object:sha256:abc", "object:sha256:def", "", "email:user@example.com", "phone:+15551234567"}
	seen := make(map[string]string, len(inputs))
	for _, s := range inputs {
		got := SHA256NanoID(s)
		if again := SHA256NanoID(s); again != got {
			t.Errorf("SHA256NanoID(%q) not deterministic: %q != %q", s, got, again)
		}
		if len(got) != NanoIDLength || !IsValidNanoID(got) {
			t.Errorf("SHA256NanoID(%q) = %q is not a valid 20-char NanoID", s, got)
		}
		// Content-addressed: distinct inputs must not collide (the prefix in
		// "email:"/"phone:" inputs is what prevents cross-type collisions).
		if prev, dup := seen[got]; dup {
			t.Errorf("SHA256NanoID collision: %q and %q both → %q", prev, s, got)
		}
		seen[got] = s
	}
}

func TestSHA256NanoID_MatchesPCGSeedWiring(t *testing.T) {
	// SHA256NanoID is documented as byte-identical to the Starlark
	// crypto.sha256NanoID(s) builtin because both route through NanoIDFromPCG
	// over a PCG seeded from the first 16 bytes of SHA-256(s), big-endian. This
	// pins that wiring: a future change to the seed split (e.g. little-endian,
	// or a different byte window) would diverge the Go and in-script ids and is
	// caught here.
	for _, s := range []string{"object:sha256:abc", "", "anything"} {
		want := SHA256NanoID(s)
		got := deriveViaDocumentedSeedWiring(s)
		if got != want {
			t.Errorf("SHA256NanoID(%q) = %q but documented seed wiring gives %q", s, want, got)
		}
	}
}

func TestSHA256NanoID_Golden(t *testing.T) {
	golden := map[string]string{
		"object:sha256:abc": "BBjn5cfM1fZNJs8VbL9y",
		"":                  "MdrFPZfqQHhpgo5BVzHW",
	}
	for s, want := range golden {
		if got := SHA256NanoID(s); got != want {
			t.Errorf("SHA256NanoID(%q) = %q, want golden %q", s, got, want)
		}
	}
}
