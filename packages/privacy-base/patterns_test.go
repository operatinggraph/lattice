package privacybase

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
)

// TestLoomPatterns_ErasureSpineIsOrderedAndDeclared pins the four things about
// this pattern that are load-bearing and that nothing else would catch: the step
// ORDER, the guard SHAPES, the completion DOMAINS, and the declared READ-SETS.
//
// The order is a legal obligation, not a preference. Key destruction is the
// primary duty and is initiated first; the write-path seal is second, which is
// what makes the residue set monotonically non-increasing and therefore makes a
// residue count provable rather than point-in-time. A reordering that put a
// sweep before the seal would still converge and would still pass every other
// test in this package, while attesting an erasure over a set that was still
// growing.
func TestLoomPatterns_ErasureSpineIsOrderedAndDeclared(t *testing.T) {
	patterns := LoomPatterns()
	if len(patterns) != 1 {
		t.Fatalf("LoomPatterns: got %d, want 1", len(patterns))
	}
	p := patterns[0]
	if p.PatternID != "identityErasure" {
		t.Errorf("PatternID = %q, want identityErasure", p.PatternID)
	}
	if p.SubjectType != "identity" {
		t.Errorf("SubjectType = %q, want identity", p.SubjectType)
	}

	wantOps := []string{
		"ShredIdentityKey",
		"SealIdentityForErasure",
		"UnbindIdentityCredentials",
		"PurgeIdentityDedupFootprint",
	}
	if len(p.Steps) != len(wantOps) {
		t.Fatalf("Steps: got %d, want %d", len(p.Steps), len(wantOps))
	}
	for i, want := range wantOps {
		if p.Steps[i].Kind != "systemOp" {
			t.Errorf("Steps[%d].Kind = %q, want systemOp — no step of this pattern waits on a person or a vendor", i, p.Steps[i].Kind)
		}
		if p.Steps[i].Operation != want {
			t.Errorf("Steps[%d].Operation = %q, want %q", i, p.Steps[i].Operation, want)
		}
	}

	// Steps 0 and 1 are guarded so a re-triggered erasure skips work already
	// done; steps 2 and 3 are guardless because the residue they drain lives in
	// the LINK plane, which the guard grammar cannot address at all.
	if len(p.Steps[1].Guard) == 0 {
		t.Error("step 1 lost its guard — a re-seal on an in-flight erasure would refresh the marker's cycle stamp for no reader, on every re-trigger")
	}
	if len(p.Steps[2].Guard) != 0 || len(p.Steps[3].Guard) != 0 {
		t.Error("a sweep step gained a guard: the guard grammar reads subject.<aspect>.data.<field> and the residue these steps drain is in the link plane, so any guard here adjudicates something other than whether there is work to do")
	}
}

// TestLoomPatterns_ShredGuardCarriesBothRefusals pins step 0's guard as a whole
// TREE, not by looking for fragments in it, because each of its two conjuncts
// exists for a state the other gets wrong and the nesting is what makes them
// compose. A fragment check passes on a guard with the right atoms wired the
// wrong way round — an anyOf where the allOf belongs turns "refuse a merged
// identity" into "run if it is merged OR unshredded", which is the failure this
// pins against.
//
// The grammar's own semantics — absent/equals/not/allOf/anyOf against real Core
// KV, including that `equals` is false for an absent path so `not` makes it run
// — are proven live by internal/loom's TestGuardEval_PinnedAbsenceSemantics.
// What that test cannot know is which tree this package ships; what this one
// cannot do is evaluate it. Together they cover the step.
//
// The mergedInto conjunct: a merged-away identity's credentials and indexes
// already moved to the survivor, so SealIdentityForErasure refuses it
// (IdentityMerged) — but the seal is step 1, and a step 0 that ran first has
// already destroyed a key irreversibly for the subject the design says to refuse
// outright. Skipping here lets the instance fail at the seal with nothing burned.
//
// The shreddedAt disjunct: an envelope shredded before the finalization-cycle
// change carries shredded=true and no stamp. A guard on `shredded` alone skips
// step 0 forever for that identity, and the seal then refuses ErasureNotShredded
// naming the exact remedy the guard forbids — "re-run ShredIdentityKey to
// restamp it". A re-shred is idempotent, so running it is free and is what
// unwedges the erasure.
func TestLoomPatterns_ShredGuardCarriesBothRefusals(t *testing.T) {
	const want = `{"allOf":[` +
		`{"absent":"subject.mergedInto.data.value"},` +
		`{"anyOf":[` +
		`{"not":{"equals":{"path":"subject.piiKey.data.shredded","value":true}}},` +
		`{"absent":"subject.piiKey.data.shreddedAt"}` +
		`]}` +
		`]}`

	got, err := json.Marshal(LoomPatterns()[0].Steps[0].Guard)
	if err != nil {
		t.Fatalf("marshal step 0 guard: %v", err)
	}
	// Compare as parsed trees so key ORDER in the literal above is not the
	// thing under test — the nesting is.
	var gotTree, wantTree any
	if err := json.Unmarshal(got, &gotTree); err != nil {
		t.Fatalf("unmarshal shipped guard: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &wantTree); err != nil {
		t.Fatalf("unmarshal expected guard: %v", err)
	}
	if !reflect.DeepEqual(gotTree, wantTree) {
		t.Errorf("step 0's guard changed shape.\n   got: %s\n  want: %s\n\n"+
			"Dropping the mergedInto conjunct means an erasure requested against a merged-away identity "+
			"irreversibly destroys its key before the seal at step 1 can reject it. Dropping the shreddedAt "+
			"disjunct wedges an envelope shredded with no stamp: the seal refuses ErasureNotShredded and "+
			"names re-running this very step as the remedy. Loosening allOf to anyOf silently does the first.",
			got, want)
	}
}

// TestLoomPatterns_CompletionDomainsCoverEveryStepsEmission is the check the
// design's §5.1 got wrong on paper: it declared ["privacy"], and step 3's op
// emits identity.unbound. A systemOp advances on its bound op's own domain
// event, so a domain left off does not wedge the instance — the step rides its
// StepTimeout into the op-status probe and advances with a WARN — but it does
// mean every erasure paying a deadline it has no reason to.
func TestLoomPatterns_CompletionDomainsCoverEveryStepsEmission(t *testing.T) {
	p := LoomPatterns()[0]

	// Domain of the event each step's bound op emits, in step order.
	wantDomains := map[string]string{
		"ShredIdentityKey":            "privacy",  // privacy.keyShredded
		"SealIdentityForErasure":      "privacy",  // privacy.erasureRequested
		"UnbindIdentityCredentials":   "identity", // identity.unbound
		"PurgeIdentityDedupFootprint": "privacy",  // privacy.dedupFootprintSwept
	}
	declared := make(map[string]bool, len(p.CompletionDomains))
	for _, d := range p.CompletionDomains {
		declared[d] = true
	}
	for _, s := range p.Steps {
		if !declared[wantDomains[s.Operation]] {
			t.Errorf("%s emits on the %q domain, which completionDomains %v does not carry — that step advances only by deadline probe, once per instance, logging a completionDomains warning against a pattern that is otherwise correct",
				s.Operation, wantDomains[s.Operation], p.CompletionDomains)
		}
	}
}

// TestLoomPatterns_DeclaredReadsMatchWhatTheScriptsRead pins the read-sets
// against what each bound script actually reads, which is the whole reason Fire
// A shipped StepSpec.Reads.
//
// subject.mergedInto on step 1 is the entry that is load-bearing rather than
// hygienic. SealIdentityForErasure reads it from `state`, not through kv.Read,
// and an UNDECLARED key and an ABSENT one are both None there — so dropping it
// silently disarms the IdentityMerged refusal and lets the seal anchor an
// erasure on a merged-away identity, whose credentials and indexes already moved
// to the survivor and whose residue is therefore zero by construction. The
// erasure would attest, correctly by its own arithmetic, that it erased nothing.
func TestLoomPatterns_DeclaredReadsMatchWhatTheScriptsRead(t *testing.T) {
	p := LoomPatterns()[0]

	wantOptional := map[string][]string{
		"ShredIdentityKey":            {"subject.piiKey"},
		"SealIdentityForErasure":      {"subject.mergedInto", "subject.piiKey", "subject.erasureRequested"},
		"UnbindIdentityCredentials":   {"subject.erasureRequested"},
		"PurgeIdentityDedupFootprint": {"subject.erasureRequested"},
	}
	for i, s := range p.Steps {
		// Every one of the four runs vertex_alive(state, subjectKey); an absent
		// subject is a correctness error, never a branch, so it is a required
		// read on every step.
		if len(s.Reads) != 1 || s.Reads[0] != "subject" {
			t.Errorf("Steps[%d] (%s).Reads = %v, want [subject] — the script's vertex_alive guard reads it out of state", i, s.Operation, s.Reads)
		}
		want := wantOptional[s.Operation]
		if len(s.OptionalReads) != len(want) {
			t.Errorf("Steps[%d] (%s).OptionalReads = %v, want %v", i, s.Operation, s.OptionalReads, want)
			continue
		}
		for j := range want {
			if s.OptionalReads[j] != want[j] {
				t.Errorf("Steps[%d] (%s).OptionalReads[%d] = %q, want %q", i, s.Operation, j, s.OptionalReads[j], want[j])
			}
		}
	}
}

// Install-time validation is NOT re-asserted here: every integration test in
// this package routes through testutil.SetupPackageTestEnv →
// InstallPhase1Packages → Installer.Install → preflight → validateAll →
// validateLoomPatterns, so a pattern the installer would reject reds the whole
// package rather than one synthetic case. The installer and the engine validate
// the same grammar in lockstep, which is what keeps a pattern from installing
// cleanly and then running dark at CDC load.

// erasureWalks is the class-(e) kv.Links walk set each erasure op runs, as the
// op's own script runs it, together with how many dispatchers submit that op.
// Both halves are stated per op rather than inferred, so a dispatcher is always
// compared against the OP — two dispatchers agreeing on a wrong set is a
// failure this would otherwise pass — and so an op reachable from one
// dispatcher is not silently held to the same arity as one reachable from two.
//
// The sweeps drain one arm per commit, in cost order, and whichever arm a given
// commit drains depends on what is left; their walk set is therefore the UNION
// of their arms, not whichever branch ran. The seal is the opposite shape: it
// walks all five arms in one commit, in the order the residue lens counts them,
// because it will not attest until every one is clear.
var erasureWalks = map[string]struct {
	walks [][2]string
	// dispatchers is how many surfaces submit this op: the two sweeps run both
	// as steps 3 and 4 of the identityErasure pattern during a live erasure and
	// as the residue target's sweep gaps when the lens finds leftovers, while
	// the seal is dispatched only by the target's missing_erasureSeal gap — the
	// pattern completes without it.
	dispatchers int
}{
	"UnbindIdentityCredentials": {
		walks: [][2]string{
			{"boundTo", "in"},
			{"boundTo", "out"},
		},
		dispatchers: 2,
	},
	"PurgeIdentityDedupFootprint": {
		walks: [][2]string{
			{"indexes", "in"},
			{"duplicateOf", "out"},
			{"duplicateOf", "in"},
		},
		dispatchers: 2,
	},
	"SealIdentityForErasureComplete": {
		walks: [][2]string{
			{"boundTo", "in"},
			{"boundTo", "out"},
			{"indexes", "in"},
			{"duplicateOf", "out"},
			{"duplicateOf", "in"},
		},
		dispatchers: 1,
	},
}

// TestDeclaredEnumerations_MatchEachOpsWalks pins the class-(e) declarations
// (Contract #2 §2.5) every erasure op carries on its envelope, at every
// dispatcher that submits it.
//
// Two failures it exists to catch, and they are different bugs. An op declaring
// its walks under one dispatcher and not the other publishes a different
// envelope for identical work, and whichever half is missing is invisible to
// anyone reading the wire. An op declaring a walk set that is not the one its
// script runs publishes an envelope that describes work nobody does — which no
// amount of dispatcher-to-dispatcher agreement would reveal, so every
// comparison here is against the op's stated set.
//
// The hub differs by dispatcher and that is the point of checking both: a step
// names the subject through the instance-subject template grammar, a gap names
// it through the violation row's entityKey column. Relation and direction are
// literals and must match exactly, in order.
func TestDeclaredEnumerations_MatchEachOpsWalks(t *testing.T) {
	check := func(t *testing.T, where, operation, wantHub string, got []pkgmgr.EnumerationSpec) {
		t.Helper()
		want := erasureWalks[operation].walks
		if len(got) != len(want) {
			t.Errorf("%s %s declares %d enumerations, want %d (%v)", where, operation, len(got), len(want), want)
			return
		}
		for i, w := range want {
			if got[i].Hub != wantHub {
				t.Errorf("%s %s enumerations[%d].Hub = %q, want %q", where, operation, i, got[i].Hub, wantHub)
			}
			if got[i].Relation != w[0] || got[i].Direction != w[1] {
				t.Errorf("%s %s enumerations[%d] = (%s, %s), want (%s, %s)",
					where, operation, i, got[i].Relation, got[i].Direction, w[0], w[1])
			}
		}
	}

	seen := map[string]int{}
	for _, s := range LoomPatterns()[0].Steps {
		if _, walks := erasureWalks[s.Operation]; !walks {
			if len(s.Enumerations) != 0 {
				t.Errorf("step %s declares enumerations but runs no kv.Links walk: %v", s.Operation, s.Enumerations)
			}
			continue
		}
		seen[s.Operation]++
		check(t, "pattern step", s.Operation, "subject", s.Enumerations)
	}

	for col, ga := range WeaverTargets()[0].Gaps {
		if _, walks := erasureWalks[ga.Operation]; !walks {
			if len(ga.Enumerations) != 0 {
				t.Errorf("gap %s declares enumerations but its op %q runs no kv.Links walk: %v",
					col, ga.Operation, ga.Enumerations)
			}
			continue
		}
		seen[ga.Operation]++
		check(t, "gap "+col, ga.Operation, "row.entityKey", ga.Enumerations)
	}

	for op, want := range erasureWalks {
		if seen[op] != want.dispatchers {
			t.Errorf("%s is declared at %d dispatchers, want %d", op, seen[op], want.dispatchers)
		}
	}
}
