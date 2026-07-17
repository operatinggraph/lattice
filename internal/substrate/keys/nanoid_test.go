package keys

import (
	"strings"
	"testing"
)

// AC: 10,000 generated NanoIDs verified for length and alphabet compliance.
func TestNewNanoID_10K(t *testing.T) {
	const N = 10_000
	seen := make(map[string]struct{}, N)
	for i := 0; i < N; i++ {
		id, err := NewNanoID()
		if err != nil {
			t.Fatalf("NewNanoID iter %d: %v", i, err)
		}
		if len(id) != NanoIDLength {
			t.Fatalf("NewNanoID iter %d: length=%d want %d (id=%q)", i, len(id), NanoIDLength, id)
		}
		if strings.ContainsAny(id, "IlO0") {
			t.Fatalf("NewNanoID iter %d: id %q contains forbidden char", i, id)
		}
		for j := 0; j < len(id); j++ {
			if !strings.ContainsRune(Alphabet, rune(id[j])) {
				t.Fatalf("NewNanoID iter %d: id %q char %q not in alphabet", i, id, id[j])
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("NewNanoID iter %d: duplicate id %q (collision in 10K)", i, id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != N {
		t.Fatalf("expected %d unique ids, got %d", N, len(seen))
	}
}

func TestAlphabetInvariants(t *testing.T) {
	if len(Alphabet) != 58 {
		t.Fatalf("Alphabet length = %d, want 58", len(Alphabet))
	}
	for _, forbidden := range "IlO0" {
		if strings.ContainsRune(Alphabet, forbidden) {
			t.Fatalf("Alphabet contains forbidden char %q", forbidden)
		}
	}
	// uniqueness
	seen := map[rune]bool{}
	for _, c := range Alphabet {
		if seen[c] {
			t.Fatalf("Alphabet contains duplicate char %q", c)
		}
		seen[c] = true
	}
}

func TestIsValidNanoID(t *testing.T) {
	cases := []struct {
		name string
		s    string
		want bool
	}{
		{"valid runtime", "Hj4kPmRtw9nbCxz5vQ2y", true},
		{"another runtime", "St6mP3qBn4rT8wYxK7Vc", true},
		// Bootstrap fixed IDs are Contract #1-compliant after Story 1.4's
		// Option A regeneration (see CONTRACT-AMENDMENT-REQUEST.md resolved
		// section). Sample primordial ID:
		{"primordial fixed", "c7u2zPUMBuhHpuhL3hYf", true},
		{"empty", "", false},
		{"short", "abc", false},
		{"long", "Hj4kPmRtw9nbCxz5vQ2yX", false},
		{"contains I", "IHj4kPmRtw9nbCxz5vQ2", false},
		{"contains l", "lHj4kPmRtw9nbCxz5vQ2", false},
		{"contains O", "OHj4kPmRtw9nbCxz5vQ2", false},
		{"contains 0", "0Hj4kPmRtw9nbCxz5vQ2", false},
		{"contains punctuation", "Hj4kPmRtw9-bCxz5vQ2y", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidNanoID(tc.s); got != tc.want {
				t.Fatalf("IsValidNanoID(%q) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestShortCode(t *testing.T) {
	id, err := NewShortCode()
	if err != nil {
		t.Fatalf("NewShortCode: %v", err)
	}
	if len(id) != ShortCodeLength {
		t.Fatalf("short code length = %d, want %d", len(id), ShortCodeLength)
	}
	if !IsValidShortCode(id) {
		t.Fatalf("generated short code %q failed IsValidShortCode", id)
	}
	if IsValidNanoID(id) {
		t.Fatalf("8-char short code %q must not pass IsValidNanoID (20-char)", id)
	}
}
