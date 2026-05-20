package full

import (
	"testing"
)

// TestLevenshteinDistance covers the canonical Wagner-Fischer edge cases
// the UDF dispatch in evalFunctionCall delegates to. Pure-Go unit test
// (no NATS required) — runs under `go test -short`.
func TestLevenshteinDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"", "abc", 3},
		{"kitten", "sitting", 3},
		{"flaw", "lawn", 2},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		// Multi-byte rune: "é" is one code point, two bytes. The helper
		// operates on runes so distance is 1, not 2.
		{"café", "cafe", 1},
	}
	for _, tt := range tests {
		got := levenshteinDistance(tt.a, tt.b)
		if got != tt.want {
			t.Fatalf("levenshteinDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

// TestParse_LevenshteinUDF asserts that cypher source referencing the new
// UDFs parses cleanly. Execution requires NATS-backed adapter fixtures
// (see executor_test.go); the parse layer is the cheap smoke test.
func TestParse_LevenshteinUDF(t *testing.T) {
	parse(t, `MATCH (a:identity), (b:identity) WHERE levenshteinRatio(a.name, b.name) >= 0.85 RETURN a.key, b.key`)
	parse(t, `MATCH (a:identity), (b:identity) WHERE levenshteinDist(a.name, b.name) <= 2 RETURN a.key, b.key`)
}
