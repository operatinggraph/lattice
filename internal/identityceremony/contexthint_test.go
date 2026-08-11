package identityceremony

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/processor"
)

// The builders exist to keep one security property — every rejection cause on
// an NFR-S6 ceremony renders the same wire shape — from being re-decided by
// each dispatcher. These pin the two decisions that property rests on.

// TestBuilders_DeclareNothingRequired: a required read's absence is a
// HydrationMiss fault carrying the probed key in details.missingKey, so no
// builder may put a target-derived key in Reads. One leaked key here re-opens
// the enumeration oracle for every client calling that builder.
func TestBuilders_DeclareNothingRequired(t *testing.T) {
	const target = "vtx.identity.Aa1Bb2Cc3Dd4Ee5Ff6Gg"
	cases := []struct {
		name      string
		hint      *processor.ContextHint
		wantCount int
	}{
		{"claim", ClaimContextHint(target), 3},
		{"complete-credential-link", CompleteCredentialLinkContextHint(target), 4},
		{"initiate-credential-link", InitiateCredentialLinkContextHint(target), 2},
	}
	for _, tc := range cases {
		if tc.hint == nil {
			t.Fatalf("%s: nil hint for a well-formed target", tc.name)
		}
		if len(tc.hint.Reads) != 0 {
			t.Fatalf("%s: Reads = %v, want empty — a required read's absence faults HydrationMiss and echoes the key back",
				tc.name, tc.hint.Reads)
		}
		if len(tc.hint.OptionalReads) != tc.wantCount {
			t.Fatalf("%s: OptionalReads = %v, want %d keys", tc.name, tc.hint.OptionalReads, tc.wantCount)
		}
		for _, k := range tc.hint.OptionalReads {
			if !strings.HasPrefix(k, target) {
				t.Fatalf("%s: declared %q, which is not derived from the target", tc.name, k)
			}
		}
	}
}

// TestBuilders_DeclineMalformedTargets: a declared key the Contract #1 grammar
// rejects raises an InvalidReadKey hydration fault BEFORE the script runs — a
// third wire shape on a path that owes exactly one. Declaring nothing lets the
// script reach its own branch and render the ordinary generic refusal.
//
// "vtx.identity.x" is the case that matters: it passes the `startswith` test
// the scripts themselves use, so only the full grammar catches it.
func TestBuilders_DeclineMalformedTargets(t *testing.T) {
	bad := []string{
		"",
		"notakey",
		"vtx.identity.x",
		"vtx.identity.",
		"vtx.identity.Aa1Bb2Cc3Dd4Ee5Ff6Gg.state", // an aspect key, not a vertex key
		"vtx.task.Aa1Bb2Cc3Dd4Ee5Ff6Gg",           // well-formed, wrong vertex type
		"lnk.identity.Aa1Bb2Cc3Dd4Ee5Ff6Gg.knows.identity.Bb1Bb2Cc3Dd4Ee5Ff6Gg",
	}
	for _, key := range bad {
		if h := ClaimContextHint(key); h != nil {
			t.Fatalf("ClaimContextHint(%q) = %+v, want nil", key, h)
		}
		if h := CompleteCredentialLinkContextHint(key); h != nil {
			t.Fatalf("CompleteCredentialLinkContextHint(%q) = %+v, want nil", key, h)
		}
		if h := InitiateCredentialLinkContextHint(key); h != nil {
			t.Fatalf("InitiateCredentialLinkContextHint(%q) = %+v, want nil", key, h)
		}
	}
}
