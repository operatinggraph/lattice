package pkgmgr_test

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	servicelocation "github.com/operatinggraph/lattice/packages/service-location"
)

// specLabels parses one lens body through the engine and returns the pkgmgr
// view of its label facts — the same conversion every production adapter does
// (cmd/lattice-pkg, cmd/loupe, internal/testutil).
func specLabels(t *testing.T, body string) pkgmgr.SpecLabels {
	t.Helper()
	facts, err := full.SpecLabels(body)
	if err != nil {
		t.Fatalf("parse lens spec: %v", err)
	}
	return pkgmgr.SpecLabels{
		Referenced: facts.Referenced,
		Exhaustive: facts.Exhaustive,
		Expansion:  facts.Expansion,
	}
}

// The false-refusal guard, aimed at the REAL source rather than a fixture
// shaped like it. packages/service-location's capabilityServiceAccess is the
// only lens in the shipped corpus carrying the `*` sigil, and it is exempt from
// the install-time label-cap gate for a reason that has nothing to do with its
// label count: its two `[:containedIn*0..]` walks clear exhaustiveness, so it
// takes the broad consumer filter whatever it labels.
//
// Read off the package's own Definition, so the day someone rewrites that
// cypher into an exhaustive shape this test stops asserting a stale fact and
// the arithmetic starts applying — which is exactly when a human should look.
func TestLensLabelCap_ShippedSigilLensIsExempt(t *testing.T) {
	def, err := servicelocation.Package.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("ExpandReadGrantWalks: %v", err)
	}

	sigilBearing := 0
	for _, l := range def.Lenses {
		if l.Spec == "" {
			continue
		}
		facts := specLabels(t, l.Spec)
		if len(facts.Expansion) == 0 {
			continue
		}
		sigilBearing++
		if facts.Exhaustive {
			t.Errorf("lens %q carries the `*` sigil and is EXHAUSTIVE — it now narrows, so the install-time "+
				"label-cap gate applies to it; check its arithmetic against the abstract's LeafBudget",
				l.CanonicalName)
		}
		if pkgmgr.LensNeedsCapCheckForTest(facts) {
			t.Errorf("lens %q must be exempt from the label-cap gate: it is non-exhaustive and can never narrow",
				l.CanonicalName)
		}
	}
	if sigilBearing == 0 {
		t.Fatal("service-location declares no lens carrying the `*` sigil — this test asserts nothing; " +
			"either the lens moved or capabilityServiceAccess dropped its abstract labels")
	}
}

// The positive vector for the test above: the exact shipped spec with its two
// variable-length hops made single-hop IS exhaustive and DOES engage the gate.
// Without this, a capabilityServiceAccess that had gone non-exhaustive for some
// unrelated reason (the unlabeled `exLoc`, the unlabeled `op` in the pattern
// comprehension) would still satisfy the guard, and the guard would be pinning
// the wrong mechanism.
func TestLensLabelCap_ShippedSigilLensExemptionIsTheWalks(t *testing.T) {
	def, err := servicelocation.Package.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("ExpandReadGrantWalks: %v", err)
	}
	var spec string
	for _, l := range def.Lenses {
		if l.CanonicalName == "capabilityServiceAccess" {
			spec = l.Spec
		}
	}
	if spec == "" {
		t.Fatal("capabilityServiceAccess not found in the shipped Definition")
	}

	// Every source of non-exhaustiveness closed at once — the two `*0..` hops
	// and the two unlabeled binding positions — so what remains is a spec whose
	// only remaining broad-maker would be the label arithmetic itself.
	exhaustive := strings.NewReplacer(
		"[:containedIn*0..]", "[:containedIn]",
		"(exLoc)", "(exLoc:location*)",
		"(op)", "(op:opmeta)",
	).Replace(spec)

	facts := specLabels(t, exhaustive)
	if !facts.Exhaustive {
		t.Fatalf("the shipped spec with its variable-length hops and unlabeled positions closed must be EXHAUSTIVE — "+
			"if it is not, this test is not proving what makes the real lens exempt (referenced=%v)", facts.Referenced)
	}
	if !pkgmgr.LensNeedsCapCheckForTest(facts) {
		t.Fatal("an exhaustive spec carrying the `*` sigil must engage the label-cap gate")
	}
}
