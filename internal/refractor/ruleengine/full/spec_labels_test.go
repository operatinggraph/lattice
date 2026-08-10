package full

import (
	"sort"
	"strings"
	"testing"
)

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SpecLabels must answer with the SAME numbers a caller would get by compiling
// the body itself and calling the two derivations — that equivalence is the
// whole reason it exists, since an install-time budget gate reading it is
// predicting what the runtime will count.
func TestSpecLabels_MatchesTheCompiledRulesOwnDerivations(t *testing.T) {
	body := `MATCH (a:unit)
MATCH (b:location*)
OPTIONAL MATCH (a)-[:containedIn]->(c:building)
RETURN a.key AS key`

	facts, err := SpecLabels(body)
	if err != nil {
		t.Fatalf("SpecLabels: %v", err)
	}
	compiled, err := New().Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cr := compiled.(*CompiledRule)
	wantReferenced, wantExhaustive := cr.ReferencedLabels()
	if got, want := strings.Join(sortedKeys(facts.Referenced), ","), strings.Join(sortedKeys(wantReferenced), ","); got != want {
		t.Errorf("Referenced = %q, want %q", got, want)
	}
	if facts.Exhaustive != wantExhaustive {
		t.Errorf("Exhaustive = %v, want %v", facts.Exhaustive, wantExhaustive)
	}
	if got, want := strings.Join(sortedKeys(facts.Expansion), ","), strings.Join(sortedKeys(cr.ExpansionLabels()), ","); got != want {
		t.Errorf("Expansion = %q, want %q", got, want)
	}
}

// The sigil-bearing label appears in BOTH sets, and a reader that subtracts is
// the only one that counts correctly. Pinned here because the install-time gate
// derives its K by exactly that subtraction, and a change making ReferencedLabels
// skip a `*` label would silently halve that gate's arithmetic.
func TestSpecLabels_ExpansionLabelIsAlsoReferenced(t *testing.T) {
	facts, err := SpecLabels("MATCH (a:unit)\nMATCH (b:location*)\nRETURN a.key AS key")
	if err != nil {
		t.Fatalf("SpecLabels: %v", err)
	}
	if _, ok := facts.Referenced["location"]; !ok {
		t.Fatalf("Referenced must carry the sigil-bearing label's raw text; got %v", sortedKeys(facts.Referenced))
	}
	if _, ok := facts.Expansion["location"]; !ok {
		t.Fatalf("Expansion must carry the sigil-bearing label; got %v", sortedKeys(facts.Expansion))
	}
	if len(facts.Referenced) != 2 || len(facts.Expansion) != 1 {
		t.Fatalf("want |Referenced|=2 and |Expansion|=1 (so K = 1); got %v / %v",
			sortedKeys(facts.Referenced), sortedKeys(facts.Expansion))
	}
}

// A variable-length hop clears exhaustiveness, which is the single property the
// install-time gate turns on: without it the gate would refuse installs for
// lenses that take the broad filter regardless.
func TestSpecLabels_VariableLengthHopClearsExhaustiveness(t *testing.T) {
	facts, err := SpecLabels("MATCH (a:unit)-[:containedIn*0..]->(b:location*)\nRETURN a.key AS key")
	if err != nil {
		t.Fatalf("SpecLabels: %v", err)
	}
	if facts.Exhaustive {
		t.Fatal("a variable-length hop must clear exhaustiveness")
	}
}

// A body that does not compile returns the parse error and no half-filled
// facts — a caller must never read an empty label set as "nothing to check".
func TestSpecLabels_ParseErrorCarriesNoFacts(t *testing.T) {
	facts, err := SpecLabels("MATCH (((( RETURN")
	if err == nil {
		t.Fatal("a malformed body must return an error")
	}
	if facts.Referenced != nil || facts.Expansion != nil || facts.Exhaustive {
		t.Fatalf("a failed parse must return the zero LabelFacts; got %+v", facts)
	}
}
