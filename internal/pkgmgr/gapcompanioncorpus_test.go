package pkgmgr_test

import (
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
)

// The false-refusal guard for the Contract #10 §10.3 companion-pair gate, aimed
// at the REAL shipped corpus rather than a fixture shaped like it.
//
// A gate wired into validateAll runs on every install, upgrade and capability
// apply, so a package it refuses cannot be installed at all. The unit tests
// beside it prove the refusal fires on a violating fixture; only this proves it
// does NOT fire on the packages the platform actually ships — the half a
// synthetic fixture can never cover, and the half whose failure mode is a red
// integration job rather than a red unit test.
//
// It lives in the external test package because the internal one cannot reach
// the corpus: every package under packages/ imports pkgmgr, so `package pkgmgr`
// importing pkgregistry back would cycle.
func TestGapCompanionPair_NoShippedPackageIsRefused(t *testing.T) {
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			t.Fatalf("pkgregistry.Names() returned %q but Lookup does not know it", name)
		}
		if err := pkgmgr.ValidateWeaverTargetsForTest(def); err != nil {
			t.Errorf("shipped package %q is refused by the install-time weaver-target validations: %v",
				name, err)
		}
	}
}

// The positive vector that keeps the guard above from passing vacuously. A
// corpus sweep asserting a property of an empty set proves nothing: if no
// shipped gap were external-classed, or none declared a companion column, the
// guard would pass with the gate never having run a single predicate.
//
// It asserts the two populations the gate needs to be non-trivial:
//
//   - at least one shipped gap whose action is one the gate classifies as
//     statically external (directOp), so the predicate is reached;
//   - at least one shipped lens declaring a maxretries_<g> body column that
//     pairs with such a gap, so the satisfying branch is exercised on real data.
//
// The day the corpus stops containing either, this fails and a human re-reads
// whether the guard above still means anything.
func TestGapCompanionPair_CorpusExercisesTheGate(t *testing.T) {
	externalGaps, satisfiedPairs := 0, 0
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			t.Fatalf("pkgregistry.Names() returned %q but Lookup does not know it", name)
		}
		for _, target := range def.WeaverTargets {
			body := shippedBodyColumns(def, target.LensRef)
			for col, ga := range target.Gaps {
				if ga.Action != "directOp" {
					continue
				}
				externalGaps++
				g := strings.TrimPrefix(col, "missing_")
				if body["maxretries_"+g] {
					satisfiedPairs++
				}
			}
		}
	}
	if externalGaps == 0 {
		t.Error("no shipped gap is statically external-classed — the companion-pair gate's predicate is " +
			"never reached, so the corpus guard beside this proves nothing")
	}
	if satisfiedPairs == 0 {
		t.Error("no shipped external gap declares a maxretries_<g> companion — the gate's satisfying " +
			"branch is never exercised against real data")
	}
}

// shippedBodyColumns resolves a target's LensRef against its own Definition and
// returns the columns the resolved lens puts into the projected row body. An
// unresolvable ref (a NanoID naming a lens installed by another package) or a
// lens with no Output descriptor yields an empty set — the same two shapes the
// gate itself skips, so this mirrors what the gate sees rather than
// second-guessing it.
//
// It mirrors the gate on the other two points as well: the row body is the union
// of BodyColumns and StaticEmptyColumns (Refractor's projection driver writes
// both into the same envelope), and a canonicalName declared twice resolves
// last-wins, the way the batch build's canonicalName→id map does.
func shippedBodyColumns(def pkgmgr.Definition, lensRef string) map[string]bool {
	cols := map[string]bool{}
	if lensRef == "" {
		return cols
	}
	for _, l := range def.Lenses {
		if l.CanonicalName != lensRef || l.Output == nil {
			continue
		}
		cols = map[string]bool{}
		for _, c := range l.Output.BodyColumns {
			cols[c] = true
		}
		for _, c := range l.Output.StaticEmptyColumns {
			cols[c] = true
		}
	}
	return cols
}
