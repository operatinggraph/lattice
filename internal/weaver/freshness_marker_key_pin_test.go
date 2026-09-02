package weaver

import (
	"strings"
	"testing"

	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
)

// TestFreshnessMarkerAspectSuffix_MatchesTheDDLItDeclares is the CROSS-BOUNDARY
// pin for the one key this engine names on another package's behalf.
//
// The temporal lane declares `p.EntityKey + freshnessMarkerAspectSuffix` in the
// op's contextHint.optionalReads, and the freshnessMarker DDL's script derives
// the very same key to merge the standing marker. Neither side can see the
// other's spelling, and a mismatch is silent in the worst way: the key is never
// hydrated, so the script's `marker_key in state` is false on EVERY fire, it
// takes the create branch forever, and the second lapse is rejected on a key
// that already exists — a freshness cycle that stops after one, with nothing in
// the logs naming the suffix.
//
// A pin on either side alone cannot catch that, because each would restate its
// own spelling. This one relates them: the suffix against the aspect DDL's own
// canonicalName (which is what makes the composed key a Contract #1 4-segment
// aspect key), and the composed derivation against the shipped script.
//
// It lives HERE rather than beside the DDL because the dependency runs one way:
// every packages/ definition imports the engine's wire types, so only an engine
// test can import the package back.
func TestFreshnessMarkerAspectSuffix_MatchesTheDDLItDeclares(t *testing.T) {
	aspect := orchestrationbase.FreshnessExpiryAspectDDL()
	if want := "." + aspect.CanonicalName; freshnessMarkerAspectSuffix != want {
		t.Fatalf("freshnessMarkerAspectSuffix = %q, want %q — the suffix must be the aspect DDL's own "+
			"canonicalName, or the declared key is not the marker's Contract #1 aspect key",
			freshnessMarkerAspectSuffix, want)
	}

	script := orchestrationbase.MarkExpiredDDL().Script
	derivation := `entity_key + "` + freshnessMarkerAspectSuffix + `"`
	if !strings.Contains(script, derivation) {
		t.Fatalf("the freshnessMarker script does not derive its marker key as %s — the temporal lane "+
			"declares that key in contextHint.optionalReads, and a key the script does not name is "+
			"hydrated for nothing while the merge never sees the standing document", derivation)
	}
}
