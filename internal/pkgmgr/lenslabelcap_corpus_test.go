package pkgmgr_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
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
		Columns:    facts.Columns,
	}
}

// lensBodies returns the bodies the gate reads for one lens, selected EXACTLY as
// pkgmgr's lensCapCandidates selects them: SpecBranches when a multi-walk lens
// compiled to N independent queries, otherwise the single Spec, and nothing at
// all for an eventStream lens (no Spec, no branches — it has no Core KV consumer
// to narrow). Reading Spec alone would skip precisely the branch-compiled shape
// a read-grant lens takes, which is most of the corpus's `*`-eligible surface.
func lensBodies(l pkgmgr.LensSpec) []string {
	switch {
	case len(l.SpecBranches) > 0:
		return l.SpecBranches
	case l.Spec != "":
		return []string{l.Spec}
	default:
		return nil
	}
}

// sigilLens is one corpus lens carrying the `*` sigil somewhere in its bodies,
// with the label facts merged across branches the way the runtime merges them
// for the one consumer they share (referenced/expansion UNION, exhaustiveness a
// CONJUNCTION).
type sigilLens struct {
	pkg   string
	lens  string
	facts pkgmgr.SpecLabels
}

// sigilCorpus drives EVERY registered package's composed definition through the
// same body selection and label derivation the install-time gate uses, and
// returns the lenses that carry the taxonomy-expansion sigil.
//
// The whole registry, not one package: "only one shipped lens carries the
// sigil" is the premise every other assertion in this file rests on, and a
// sweep narrowed to a single import can only ever confirm that import — a
// second package adopting `(l:location*)` would leave the premise asserted and
// false. Driven off pkgregistry.Names()/Lookup() it is a matcher-derived
// standing fact that re-derives itself on every run, the same shape
// internal/pkgregistry's own drift test and Refractor's auth-plane narrowing
// census use.
//
// ExpandReadGrantWalks first, because a read-grant lens's SHIPPED body is the
// composed head + walk chain + tail the installer writes, not the presentation
// tail its package declared — and the walk chain binds most of the labels.
func sigilCorpus(t *testing.T) []sigilLens {
	t.Helper()
	var out []sigilLens
	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		if !ok {
			t.Fatalf("pkgregistry.Names() returned %q but Lookup does not know it", name)
		}
		expanded, err := def.ExpandReadGrantWalks()
		if err != nil {
			t.Fatalf("%s: ExpandReadGrantWalks: %v", name, err)
		}
		for _, l := range expanded.Lenses {
			bodies := lensBodies(l)
			if len(bodies) == 0 {
				continue
			}
			merged := pkgmgr.SpecLabels{
				Referenced: map[string]struct{}{},
				Expansion:  map[string]struct{}{},
				Exhaustive: true,
			}
			for _, body := range bodies {
				facts := specLabels(t, body)
				for lbl := range facts.Referenced {
					merged.Referenced[lbl] = struct{}{}
				}
				for lbl := range facts.Expansion {
					merged.Expansion[lbl] = struct{}{}
				}
				merged.Exhaustive = merged.Exhaustive && facts.Exhaustive
			}
			if len(merged.Expansion) == 0 {
				continue
			}
			out = append(out, sigilLens{pkg: name, lens: l.CanonicalName, facts: merged})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].pkg != out[b].pkg {
			return out[a].pkg < out[b].pkg
		}
		return out[a].lens < out[b].lens
	})
	return out
}

// The false-refusal guard, aimed at the REAL shipped corpus rather than a
// fixture shaped like it. A lens carrying the `*` sigil is exempt from the
// install-time label-cap gate exactly when it is NON-exhaustive: its
// variable-length walks clear exhaustiveness, so it takes the broad consumer
// filter whatever it labels and the arithmetic can never refuse it
// (capabilityServiceAccess). An EXHAUSTIVE sigil lens narrows, so the gate
// applies to it, and the arithmetic must hold against the abstract's declared
// LeafBudget: cafe-domain's cafeHousePolicies is that lens — a bare
// `MATCH (loc:location*)` with no concrete label beside it, so it prices at
// 0 + location's LeafBudget (5) against the cap (8). Any OTHER exhaustive
// sigil lens is a new fact a human checks against its abstract's budget
// before pinning it here.
func TestLensLabelCap_ShippedSigilLensesAreExempt(t *testing.T) {
	corpus := sigilCorpus(t)
	exhaustivePinned := map[string]bool{"cafe-domain/cafeHousePolicies": true}
	for _, sl := range corpus {
		name := sl.pkg + "/" + sl.lens
		if sl.facts.Exhaustive {
			if !exhaustivePinned[name] {
				t.Errorf("%s carries the `*` sigil and is EXHAUSTIVE — it now narrows, so the install-time "+
					"label-cap gate applies to it; check its arithmetic against the abstract's LeafBudget, then pin it here", name)
				continue
			}
			if !pkgmgr.LensNeedsCapCheckForTest(sl.facts) {
				t.Errorf("%s is exhaustive and carries an expansion label, so the label-cap gate must apply to it", name)
			}
			for l := range sl.facts.Referenced {
				if _, isExpansion := sl.facts.Expansion[l]; !isExpansion {
					t.Errorf("%s names the concrete label %q beside its `*` sigil — its pinned arithmetic is 0 concrete labels + the abstract's LeafBudget; re-price it", name, l)
				}
			}
			continue
		}
		if pkgmgr.LensNeedsCapCheckForTest(sl.facts) {
			t.Errorf("%s must be exempt from the label-cap gate: it is non-exhaustive and can never narrow", name)
		}
	}
}

// The positive vector that keeps the guard above from passing vacuously: the
// sigil is actually present in the corpus, in exactly the lenses that own it.
// A corpus sweep asserting a property of an empty set proves nothing, and the
// day capabilityServiceAccess drops its abstract labels, cafeHousePolicies
// gains a concrete label, or some other package adopts the sigil — the
// coordinates in every comment around here go stale and a human should
// re-read them.
func TestLensLabelCap_SigilCorpusIsExactlyServiceLocation(t *testing.T) {
	corpus := sigilCorpus(t)
	if len(corpus) == 0 {
		t.Fatal("no shipped lens carries the `*` sigil — the exemption guard asserts nothing; " +
			"either capabilityServiceAccess dropped its abstract labels or the lens moved")
	}
	want := []string{"cafe-domain/cafeHousePolicies", "service-location/capabilityServiceAccess"}
	var owners []string
	for _, sl := range corpus {
		owners = append(owners, sl.pkg+"/"+sl.lens)
	}
	if strings.Join(owners, ",") != strings.Join(want, ",") {
		t.Errorf("sigil-bearing lenses = %v, want exactly %v — a lens gained or lost the `*` sigil; "+
			"re-read the label-cap gate's corpus reasoning against it", owners, want)
	}
	t.Logf("sigil-bearing lenses: %s", strings.Join(owners, ", "))
}

// The mechanism the exemption leans on, asserted directly rather than inferred
// from an install succeeding: the exact shipped spec with its variable-length
// hops and unlabeled binding positions closed IS exhaustive and DOES engage the
// gate. Without this, a lens that had gone non-exhaustive for some unrelated
// reason would still satisfy the guard, and the guard would be pinning the
// wrong mechanism.
func TestLensLabelCap_ShippedSigilLensExemptionIsTheWalks(t *testing.T) {
	def, ok := pkgregistry.Lookup("service-location")
	if !ok {
		t.Fatal("service-location is not registered")
	}
	expanded, err := def.ExpandReadGrantWalks()
	if err != nil {
		t.Fatalf("ExpandReadGrantWalks: %v", err)
	}
	var spec string
	for _, l := range expanded.Lenses {
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
