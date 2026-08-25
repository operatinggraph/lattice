package identityceremony

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/processor"
	identitydomain "github.com/operatinggraph/lattice/packages/identity-domain"
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

// TestBuilders_EmitExactlyTheirDescriptorsSet is the positive vector for the
// Processor's closed declared-read set: for an NFR-S6 operation an envelope
// naming anything the op's own descriptor does not name is REFUSED before
// hydration (internal/processor/descriptor_floor.go). Every shipped dispatcher
// builds its hint here, so if a builder and its descriptor ever disagree by one
// key, every claim and every credential link from every client fails — and the
// refusal answers with the same generic reply as a wrong secret, so nothing
// upstream would say why.
//
// The comparison is key for key, both directions, against the descriptor read
// out of the package itself with the one placeholder those templates use
// substituted. A template carrying any OTHER placeholder fails the test rather
// than being skipped: the Processor would compile it to something this
// substitution cannot predict, and skipping it would leave the pin passing over
// a key nobody checked.
func TestBuilders_EmitExactlyTheirDescriptorsSet(t *testing.T) {
	const target = "vtx.identity.Aa1Bb2Cc3Dd4Ee5Ff6Gg"
	cases := []struct {
		op   string
		hint *processor.ContextHint
	}{
		{"ClaimIdentity", ClaimContextHint(target)},
		{"CompleteCredentialLink", CompleteCredentialLinkContextHint(target)},
	}
	for _, tc := range cases {
		if tc.hint == nil {
			t.Fatalf("%s: nil hint for a well-formed target", tc.op)
		}
		want := descriptorKeys(t, tc.op, target)
		got := append(append([]string{}, tc.hint.Reads...), tc.hint.OptionalReads...)
		if len(tc.hint.EgressReads) != 0 || len(tc.hint.Enumerations) != 0 {
			t.Fatalf("%s: hint declares egressReads %v / enumerations %v — a descriptor can name neither, so the Processor refuses both outright",
				tc.op, tc.hint.EgressReads, tc.hint.Enumerations)
		}
		assertSameKeys(t, tc.op, got, want)
	}
}

// descriptorKeys resolves an op's Dispatch read templates against one target,
// which is the whole vocabulary these two descriptors use.
func descriptorKeys(t *testing.T, operationType, target string) []string {
	t.Helper()
	var dispatch *pkgmgr.OpDispatchSpec
	for i := range identitydomain.Package.OpMetas {
		if identitydomain.Package.OpMetas[i].OperationType == operationType {
			dispatch = identitydomain.Package.OpMetas[i].Dispatch
		}
	}
	if dispatch == nil {
		t.Fatalf("%s: no op-meta with a Dispatch descriptor — with no descriptor the Processor admits NO declared key for this op and every submission is refused", operationType)
	}
	var keys []string
	for _, tpl := range append(append([]string{}, dispatch.Reads...), dispatch.OptionalReads...) {
		resolved := strings.ReplaceAll(tpl, "{payload.targetIdentityKey}", target)
		if strings.ContainsAny(resolved, "{}") {
			t.Fatalf("%s: template %q carries a placeholder this test cannot resolve; the Processor compiles it to a key or a shape that no builder here can be checked against", operationType, tpl)
		}
		keys = append(keys, resolved)
	}
	if len(keys) == 0 {
		t.Fatalf("%s: the descriptor names no key, so the closed set admits nothing", operationType)
	}
	return keys
}

func assertSameKeys(t *testing.T, operationType string, got, want []string) {
	t.Helper()
	inWant := map[string]bool{}
	for _, k := range want {
		inWant[k] = true
	}
	inGot := map[string]bool{}
	for _, k := range got {
		inGot[k] = true
		if !inWant[k] {
			t.Fatalf("%s: the builder declares %q, which the descriptor does not name — the Processor refuses this envelope before hydration (declared %v, descriptor %v)",
				operationType, k, got, want)
		}
	}
	for _, k := range want {
		if !inGot[k] {
			t.Fatalf("%s: the descriptor names %q and the builder does not declare it — the key the script adjudicates on is left to a live undeclared read (declared %v, descriptor %v)",
				operationType, k, got, want)
		}
	}
}
