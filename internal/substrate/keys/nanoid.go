package keys

import (
	"crypto/rand"
	"fmt"
)

// Alphabet is the canonical 58-character custom NanoID alphabet for Lattice.
// Per Contract #1: A-Z, a-z, 0-9 minus the visually ambiguous characters
// I, l, O, 0. The alphabet order is fixed and used both for generation and
// validation. Length is 58 (not a power of two — generation uses
// rejection sampling against a 6-bit mask).
const Alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789"

// NanoIDLength is the canonical primary-key NanoID length (20 chars).
const NanoIDLength = 20

// ShortCodeLength is the human-facing short-code NanoID length (8 chars).
// Per Contract #1 §1.1: short codes are display references only and MUST
// NOT be used as primary keys.
const ShortCodeLength = 8

// alphabetIndex is a lookup table populated at init time. alphabetIndex[c]
// == byte index in Alphabet for valid characters; 0xFF for invalid bytes.
var alphabetIndex [256]byte

func init() {
	if len(Alphabet) != 58 {
		panic(fmt.Sprintf("substrate/keys: Alphabet must be 58 chars, got %d", len(Alphabet)))
	}
	for i := range alphabetIndex {
		alphabetIndex[i] = 0xFF
	}
	for i := 0; i < len(Alphabet); i++ {
		alphabetIndex[Alphabet[i]] = byte(i)
	}
	// Sanity: forbidden characters must NOT be in the alphabet.
	for _, c := range "IlO0" {
		if alphabetIndex[byte(c)] != 0xFF {
			panic(fmt.Sprintf("substrate/keys: forbidden char %q present in Alphabet", c))
		}
	}
}

// NewNanoID returns a freshly generated 20-character NanoID drawn from the
// custom Lattice alphabet (Contract #1). Returns an error only if the
// underlying crypto/rand reader fails, which is treated as an unrecoverable
// host failure by all callers in practice.
func NewNanoID() (string, error) {
	return newNanoIDN(NanoIDLength)
}

// NewShortCode returns a freshly generated 8-character NanoID for human-facing
// display references. Not a primary key (Contract #1 §1.1).
func NewShortCode() (string, error) {
	return newNanoIDN(ShortCodeLength)
}

// newNanoIDN generates an n-character NanoID using rejection sampling against
// a 6-bit mask (covers 0..63; we accept values < 58 = len(Alphabet)).
func newNanoIDN(n int) (string, error) {
	const mask = 63 // 6 bits — covers 0..63; we reject >=58.
	// Step length heuristic: oversample so we rarely need a second read.
	// At 58/64 acceptance, expected reads per char ~= 1.1; allocate 2x.
	step := n * 2
	buf := make([]byte, step)
	id := make([]byte, n)
	i := 0
	for {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("substrate/keys: read entropy: %w", err)
		}
		for j := 0; j < step && i < n; j++ {
			v := buf[j] & mask
			if int(v) < len(Alphabet) {
				id[i] = Alphabet[v]
				i++
			}
		}
		if i >= n {
			return string(id), nil
		}
	}
}

// IsValidNanoID reports whether s is exactly 20 characters drawn entirely
// from the canonical alphabet (Contract #1). It does NOT distinguish
// runtime-generated IDs from fixed primordial IDs — both are 20-char
// strings over the same alphabet.
func IsValidNanoID(s string) bool {
	return isValidNanoIDN(s, NanoIDLength)
}

// IsValidShortCode reports whether s is exactly 8 characters drawn entirely
// from the canonical alphabet.
func IsValidShortCode(s string) bool {
	return isValidNanoIDN(s, ShortCodeLength)
}

func isValidNanoIDN(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if alphabetIndex[s[i]] == 0xFF {
			return false
		}
	}
	return true
}
