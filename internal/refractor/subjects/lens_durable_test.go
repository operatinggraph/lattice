package subjects

import "testing"

func TestLensDurable_RoundTrips(t *testing.T) {
	const ruleID = "AbCdEfGhJkMnPqRsTuVw"
	name := LensDurable(ruleID)
	if name != "refractor-"+ruleID {
		t.Fatalf("LensDurable(%q) = %q", ruleID, name)
	}
	got, ok := ParseLensDurable(name)
	if !ok || got != ruleID {
		t.Fatalf("ParseLensDurable(%q) = %q, %v; want %q, true", name, got, ok, ruleID)
	}
}

// The names that share the prefix are the whole reason ParseLensDurable
// exists: each one below is a live consumer on the same Core KV stream, and a
// true answer for any of them is a deleted consumer belonging to something
// still running.
func TestParseLensDurable_RejectsPrefixSharers(t *testing.T) {
	cases := []struct {
		name string
		why  string
	}{
		{"refractor-adjacency", "the adjacency bootstrapper's consumer"},
		{"refractor-lens-source-rfx-abc123-CVmVsDREP1FQ8a46Epzz", "the lens source's per-boot durable"},
		{"refractor-", "bare prefix"},
		{"refractor-AbCdEfGhJkMnPqRsTu", "19 chars — one short of a NanoID"},
		{"refractor-AbCdEfGhJkMnPqRsTuVwX", "21 chars — one over"},
		{"refractor-AbCdEfGhJkMnPqRsTuV0", "contains 0, excluded from the alphabet"},
		{"refractor-AbCdEfGhJkMnPqRsTuV.", "contains a subject separator"},
		{"chronicler-defs-chronicler-abc", "another component's consumer"},
		{"processor-outbox", "another component's consumer"},
		{"", "empty"},
	}
	for _, c := range cases {
		if id, ok := ParseLensDurable(c.name); ok {
			t.Errorf("ParseLensDurable(%q) accepted as lens %q — %s", c.name, id, c.why)
		}
	}
}

// A malformed rule ID reaches LensDurable from a lens spec body, so it must
// cost that lens its activation and nothing more — never the process, and
// never a name any reconciliation would act on.
func TestLensDurable_MalformedRuleIDIsNotRecognizedBack(t *testing.T) {
	for _, ruleID := range []string{"Ab.Cd", "not-a-nanoid", ""} {
		name := LensDurable(ruleID)
		if id, ok := ParseLensDurable(name); ok {
			t.Errorf("LensDurable(%q) = %q parsed back as lens %q; a malformed id must not round-trip", ruleID, name, id)
		}
	}
}
