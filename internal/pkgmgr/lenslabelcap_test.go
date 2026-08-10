package pkgmgr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/operatinggraph/lattice/internal/refractor/subjects"
)

// capLensSpec builds a lens body referencing `concrete` distinct concrete
// labels plus each name in expansions carrying the `*` taxonomy sigil. Every
// node pattern is labeled and every relationship is single-hop, so
// full.ReferencedLabels answers EXHAUSTIVE — which is the precondition the cap
// gate turns on, and pinning it in the fixture is what makes each refusal below
// attributable to the arithmetic rather than to an accidentally-broad spec.
func capLensSpec(concrete int, expansions ...string) string {
	var b strings.Builder
	for i := 0; i < concrete; i++ {
		fmt.Fprintf(&b, "MATCH (c%d:concrete%d)\n", i, i)
	}
	for i, name := range expansions {
		fmt.Fprintf(&b, "MATCH (e%d:%s*)\n", i, name)
	}
	b.WriteString("RETURN e0.key AS key\n")
	return b.String()
}

// nonExhaustiveCapLensSpec is capLensSpec plus one variable-length hop, which
// is what clears exhaustiveness. The hop clause goes BEFORE the RETURN, since a
// MATCH after a RETURN does not parse at all — and an unparseable spec is
// skipped by the cap gate for an entirely different reason, which would let the
// false-refusal guard pass while proving nothing about exhaustiveness.
func nonExhaustiveCapLensSpec(concrete int, expansions ...string) string {
	base := capLensSpec(concrete, expansions...)
	return strings.Replace(base, "RETURN e0.key",
		"MATCH (e0)-[:containedIn*0..]->(far:place)\nRETURN e0.key", 1)
}

// capLensDef is a package declaring one lens carrying spec, and optionally the
// abstract types that lens expands (budget 0 leaves LeafBudget undeclared,
// which takes leafBudgetDefault).
func capLensDef(pkgName, spec string, abstracts map[string]int) Definition {
	def := Definition{
		Name:        pkgName,
		Version:     "0.1.0",
		Description: "narrowed-filter label cap test package",
		Lenses: []LensSpec{{
			CanonicalName: "capLens",
			Class:         "meta.lens",
			Adapter:       "nats-kv",
			Bucket:        pkgName + "-targets",
			Engine:        "full",
			Spec:          spec,
		}},
	}
	for name, budget := range abstracts {
		d := abstractDDL(name)
		d.LeafBudget = budget
		def.DDLs = append(def.DDLs, d)
	}
	return def
}

// capHarness is newInstallerHarness with the spec parser wired, which is what
// every production entry point does (cmd/lattice-pkg, cmd/loupe) and what
// arms the gate under test.
func capHarness(t *testing.T) (context.Context, *Installer) {
	t.Helper()
	c, _, i := newInstallerHarness(t)
	i.SpecParser = fullCypherParser{}
	return c, i
}

// THE POSITIVE VECTOR, and the boundary in the same shot. A lens whose only
// label is the abstract one sits at K=0, and location's UNDECLARED LeafBudget
// takes the whole cap: 0 + 8 == 8, exactly at maxNarrowedFilterLabels, which
// installs. Every refusal below differs from this fixture in the label count
// alone, so a refusal there cannot be blamed on the lens, the DDL, or the
// harness.
func TestLensLabelCap_AtCapWithDefaultBudget_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-at-default", capLensSpec(0, "location"), map[string]int{"location": 0})
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("K=0 against the default LeafBudget is exactly at the cap (%d) and must install: %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
}

// One concrete label past the positive vector. The default LeafBudget IS the
// cap, so a single concrete label alongside an abstract one that declares no
// budget already overruns — the finding this test pins as behaviour rather than
// leaving to be discovered by the first lens author who writes `(l:location*)`.
func TestLensLabelCap_OneOverCapWithDefaultBudget_Refused(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-over-default", capLensSpec(1, "location"), map[string]int{"location": 0})
	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=1 plus an undeclared LeafBudget is %d against a cap of %d and must be refused; got %v",
			1+leafBudgetDefault, subjects.MaxNarrowedFilterLabels, err)
	}
	for _, want := range []string{"capLens", "LeafBudget 8"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal must name %q so the author knows what to fix; got %v", want, err)
		}
	}
}

// The boundary with a DECLARED budget, which is the case §10.2 was written for:
// the abstract's owner promises at most 5 leaves, the lens spends 3 labels of
// its own, and 3 + 5 == 8 installs.
func TestLensLabelCap_AtCapWithDeclaredBudget_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-at-declared", capLensSpec(3, "location"), map[string]int{"location": 5})
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("K=3 against a declared LeafBudget of 5 is exactly at the cap and must install: %v", err)
	}
}

// One label past that boundary. Paired with the test above, the two differ by a
// single concrete label, so together they pin the comparison as <= rather than
// < — an off-by-one in either direction fails exactly one of them.
func TestLensLabelCap_OneOverCapWithDeclaredBudget_Refused(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-over-declared", capLensSpec(4, "location"), map[string]int{"location": 5})
	if _, err := inst.Install(ctx, def); !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=4 against a declared LeafBudget of 5 is 9 against a cap of %d and must be refused; got %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
}

// THE FALSE-REFUSAL GUARD, and the one that matters most. A lens whose label
// arithmetic is wildly over the cap (6 concrete labels plus a budget-5
// abstract, a worst case of 11) still installs, because a variable-length
// relationship clears exhaustiveness (full's ReferencedLabels: MinHops != 1 ||
// MaxHops != 1) and a non-exhaustive lens takes the broad consumer filter
// whatever it labels. Refusing it would refuse an install for a footprint
// regression that cannot happen.
//
// This is not a hypothetical shape: it is the shape of the only lens in the
// shipped corpus carrying the sigil (packages/service-location's
// capabilityServiceAccess, two `[:containedIn*0..]` walks), pinned against the
// real spec by TestLensLabelCap_ShippedSigilLensIsExempt in the external test.
func TestLensLabelCap_NonExhaustiveLens_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	spec := nonExhaustiveCapLensSpec(6, "location")
	// The fixture must be non-exhaustive AND parseable: an unparseable spec is
	// skipped by the gate for a completely different reason, and would let this
	// guard pass while proving nothing. Asserted here rather than assumed,
	// because a clause in the wrong position is exactly how that happens.
	facts, err := fullCypherParser{}.Parse(spec)
	if err != nil {
		t.Fatalf("the guard's fixture must PARSE — an unparseable spec is skipped for another reason entirely: %v", err)
	}
	if facts.Exhaustive {
		t.Fatalf("the guard's fixture must be non-exhaustive; referenced=%v", facts.Referenced)
	}

	def := capLensDef("cap-nonexhaustive", spec, map[string]int{"location": 5})
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a NON-EXHAUSTIVE lens never narrows and must install however large its label arithmetic: %v", err)
	}
}

// The mechanism the fixture above leans on, asserted directly rather than
// inferred from an install succeeding: the same spec WITHOUT the variable-length
// hop is exhaustive and IS refused. Without this, a fixture that had gone
// non-exhaustive for some unrelated reason would still pass the guard above.
func TestLensLabelCap_NonExhaustiveGuardHasAPositiveVector(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-nonexhaustive-positive", capLensSpec(6, "location"), map[string]int{"location": 5})
	if _, err := inst.Install(ctx, def); !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("the same 6-concrete-label lens WITHOUT a variable-length hop is exhaustive and must be refused; got %v", err)
	}
}

// Two abstract labels charge BOTH budgets, because the runtime unions both
// resolved sets into the one label set its single consumer filter is derived
// from. 2 + 3 + 3 == 8 installs; the test below takes it one over.
func TestLensLabelCap_TwoAbstractLabels_SumsBothBudgets_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-two-abstract", capLensSpec(2, "location", "party"),
		map[string]int{"location": 3, "party": 3})
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("K=2 plus two budget-3 abstracts is exactly at the cap and must install: %v", err)
	}
}

// The sum's teeth: each abstract fits on its own (3 + 3 <= 8) and the pair does
// not. A per-abstract reading of §10.2's `K + leafBudget` passes this lens; the
// runtime takes it broad. Summing is what closes that.
func TestLensLabelCap_TwoAbstractLabels_SumOverCap_Refused(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-two-abstract-over", capLensSpec(3, "location", "party"),
		map[string]int{"location": 3, "party": 3})
	if _, err := inst.Install(ctx, def); !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=3 plus two budget-3 abstracts is 9 against a cap of %d and must be refused; got %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
}

// The budget is read from the INSTALLED kernel, not only from the batch: the
// abstract type ships in one package and the consuming lens in a second, which
// is the cross-package coupling the whole taxonomy exists to allow. This def
// declares no DDL at all, so it also exercises the gate's own scan trigger —
// needsTaxonomyScan is false for it and the key list is fetched anyway.
func TestLensLabelCap_CrossPackageBudget_Refused(t *testing.T) {
	ctx, inst := capHarness(t)

	abstract := abstractDDL("location")
	abstract.LeafBudget = 5
	owner := Definition{Name: "location-domain", Version: "0.1.0", DDLs: []DDLSpec{abstract}}
	if _, err := inst.Install(ctx, owner); err != nil {
		t.Fatalf("install the abstract's owning package: %v", err)
	}

	def := capLensDef("cap-consumer", capLensSpec(4, "location"), nil)
	if _, err := inst.Install(ctx, def); !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("the consuming lens must be priced against the INSTALLED abstract's declared budget; got %v", err)
	}
}

// The same cross-package consumer one label under the cap installs, so the
// refusal above is the arithmetic and not merely "the abstract was installed
// elsewhere".
func TestLensLabelCap_CrossPackageBudget_UnderCapInstalls(t *testing.T) {
	ctx, inst := capHarness(t)

	abstract := abstractDDL("location")
	abstract.LeafBudget = 5
	owner := Definition{Name: "location-domain", Version: "0.1.0", DDLs: []DDLSpec{abstract}}
	if _, err := inst.Install(ctx, owner); err != nil {
		t.Fatalf("install the abstract's owning package: %v", err)
	}

	def := capLensDef("cap-consumer-ok", capLensSpec(3, "location"), nil)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("K=3 against a declared budget of 5 is exactly at the cap and must install: %v", err)
	}
}

// An expansion label naming a type nothing declares carries NO charge: install
// ORDER is unconstrained, so a lens package may legally land before the package
// declaring the abstract it expands, and inventing a budget for it would invent
// an ordering the platform does not have. K=8 is exactly at the cap, so this
// installs only because the unresolvable label contributed zero — had it taken
// the default budget the total would be 16.
func TestLensLabelCap_UnresolvableExpansionLabel_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-unresolvable", capLensSpec(8, "nosuchtype"), nil)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("an expansion label naming no declared type carries no budget to enforce: %v", err)
	}
}

// The same lens one concrete label further along is refused on K ALONE, and the
// refusal has to say so. A lens whose own concrete labels already exceed the cap
// can never narrow in any world — every expansion label contributes at least
// itself once it resolves — so leniency about the unresolvable label is not
// leniency about the lens. What the message owes the author is honesty about
// which number it computed: the un-priced label is NAMED and the total is called
// a floor, so nobody goes hunting for a budget to shrink that was never charged.
func TestLensLabelCap_UnresolvableExpansionLabel_StillPricesK(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-unresolvable-over", capLensSpec(9, "nosuchtype"), nil)
	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=9 alone is over the cap of %d whatever the expansion resolves to; got %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
	for _, want := range []string{"nosuchtype", "floor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("a partially-priced count must name what it did NOT price and call itself a floor (missing %q); got %v", want, err)
		}
	}
}

// A partially-priced expansion SET, which is the shape the message above exists
// for: one abstract resolves and is charged, one label resolves to nothing at
// all. The arithmetic runs over a proper subset, so the refusal must name both
// halves — the budget it charged and the label it could not price.
func TestLensLabelCap_PartiallyPricedExpansion_NamesBothHalves(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-partial", capLensSpec(4, "location", "nosuchtype"), map[string]int{"location": 5})
	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=4 plus a budget-5 abstract is 9 against a cap of %d and must be refused; got %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
	for _, want := range []string{"location (abstract, LeafBudget 5)", "nosuchtype", "floor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q so the author is not sent to shrink the wrong budget; got %v", want, err)
		}
	}
}

// A `*` on a CONCRETE type is legal (§3.4/amendment A5) and IS charged. The
// runtime unions in resolved(e) for every expansion label alike, and a concrete
// closure is reflexive — never smaller than one — so K=8 beside a concrete
// sigil is 9 against a cap of 8, and a gate that charged the sigil zero would
// call it 8 and let it through.
func TestLensLabelCap_ConcreteExpansionTarget_ChargedItsClosure_Refused(t *testing.T) {
	ctx, inst := capHarness(t)

	owner := Definition{
		Name:    "concrete-owner",
		Version: "0.1.0",
		DDLs:    []DDLSpec{minimalDDL("place", ddlClassVertexType, false)},
	}
	if _, err := inst.Install(ctx, owner); err != nil {
		t.Fatalf("install the concrete type's owning package: %v", err)
	}

	def := capLensDef("cap-concrete-star", capLensSpec(8, "place"), nil)
	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=8 plus a concrete `*` whose closure is 1 is 9 against a cap of %d and must be refused; got %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
	for _, want := range []string{"place", "closure 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must show what the concrete sigil was charged (missing %q); got %v", want, err)
		}
	}
}

// The boundary beneath that refusal, so the charge is proven to be ONE and not
// merely "something non-zero large enough to refuse": the same lens with one
// fewer concrete label is exactly at the cap and installs.
func TestLensLabelCap_ConcreteExpansionTarget_UnderCapInstalls(t *testing.T) {
	ctx, inst := capHarness(t)

	owner := Definition{
		Name:    "concrete-owner",
		Version: "0.1.0",
		DDLs:    []DDLSpec{minimalDDL("place", ddlClassVertexType, false)},
	}
	if _, err := inst.Install(ctx, owner); err != nil {
		t.Fatalf("install the concrete type's owning package: %v", err)
	}

	def := capLensDef("cap-concrete-star-ok", capLensSpec(7, "place"), nil)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("K=7 plus a concrete closure of 1 is exactly at the cap and must install: %v", err)
	}
}

// The charge is the CLOSURE, not a flat one — the property a floor of 1 alone
// would hide. `place` here has two concrete subtypes, so the resolver expands
// `(p:place*)` into three types and the gate charges three: K=6 crosses the cap
// and K=5 sits exactly on it. A gate charging a flat 1 passes both.
func TestLensLabelCap_ConcreteExpansionTarget_ChargesTheWholeClosure(t *testing.T) {
	ctx, inst := capHarness(t)

	room := minimalDDL("room", ddlClassVertexType, false)
	room.SubtypeOfRef = "place"
	hallway := minimalDDL("hallway", ddlClassVertexType, false)
	hallway.SubtypeOfRef = "place"
	owner := Definition{
		Name:    "concrete-tree-owner",
		Version: "0.1.0",
		DDLs:    []DDLSpec{minimalDDL("place", ddlClassVertexType, false), room, hallway},
	}
	if _, err := inst.Install(ctx, owner); err != nil {
		t.Fatalf("install the concrete type and its two subtypes: %v", err)
	}

	over := capLensDef("cap-concrete-closure-over", capLensSpec(6, "place"), nil)
	err := func() error { _, e := inst.Install(ctx, over); return e }()
	if !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=6 plus a concrete closure of 3 is 9 against a cap of %d and must be refused; got %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
	if !strings.Contains(err.Error(), "closure 3") {
		t.Errorf("the charge must be the closure size (3), not a flat floor of 1; got %v", err)
	}

	under := capLensDef("cap-concrete-closure-ok", capLensSpec(5, "place"), nil)
	if _, err := inst.Install(ctx, under); err != nil {
		t.Fatalf("K=5 plus a concrete closure of 3 is exactly at the cap and must install: %v", err)
	}
}

// A concrete type this SAME batch declares is priced too, against the
// declaration landing in this install rather than against the absent installed
// one — the concrete mirror of the batch-local abstract path.
func TestLensLabelCap_BatchLocalConcreteExpansionTarget_Refused(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-batch-concrete", capLensSpec(8, "place"), nil)
	def.DDLs = append(def.DDLs, minimalDDL("place", ddlClassVertexType, false))
	if _, err := inst.Install(ctx, def); !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("a lens expanding a concrete type its OWN package declares must be priced against that declaration; got %v", err)
	}
}

// A lens carrying no `*` anywhere is never priced, however many labels it names:
// with no abstract type in the picture there is no budget, and its label count
// is a static property its own author can already read off the source.
func TestLensLabelCap_NoSigil_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	spec := capLensSpec(12) + ""
	def := capLensDef("cap-no-sigil", strings.Replace(spec, "RETURN e0.key", "RETURN c0.key", 1), nil)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a sigil-free lens has no abstract budget to charge: %v", err)
	}
}

// With no parser wired the gate is silent — the documented posture of an
// unwired installer, and the reason every production entry point wires one.
// Pinned so the silence is a decision on the record rather than an accident
// nobody notices until a lens ships broad.
func TestLensLabelCap_NoParserWired_GateIsSilent(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := capLensDef("cap-no-parser", capLensSpec(4, "location"), map[string]int{"location": 5})
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("an installer with no SpecParser cannot price a lens and must not refuse one: %v", err)
	}
}

// A spec the engine cannot compile is skipped for the cap gate, not turned into
// an install failure: an uncompilable spec never activates a lens, so it can
// never narrow a consumer and can never regress a footprint. Turning this gate
// into a general parse gate would be an unrelated new refusal with the whole
// shipped lens corpus in its blast radius.
func TestLensLabelCap_UnparseableSpec_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-unparseable", "MATCH (((( RETURN", nil)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a spec the engine rejects is skipped by the cap gate, not refused by it: %v", err)
	}
}

// The multi-branch fold: a lens compiled to N independent queries is judged as
// the ONE consumer it becomes, so the label sets union across branches. Neither
// branch alone crosses the cap (2 + 5 and 2 + 5); together they name four
// distinct concrete labels and the pair does.
func TestLensLabelCap_SpecBranchesUnionAcrossBranches_Refused(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-branches", "", map[string]int{"location": 5})
	def.Lenses[0].Spec = ""
	def.Lenses[0].SpecBranches = []string{
		"MATCH (a:alpha)\nMATCH (b:beta)\nMATCH (e0:location*)\nRETURN e0.key AS key\n",
		"MATCH (c:gamma)\nMATCH (d:delta)\nMATCH (e0:location*)\nRETURN e0.key AS key\n",
	}
	if _, err := inst.Install(ctx, def); !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("branch label sets union into one consumer: 4 concrete + budget 5 is 9 against a cap of %d; got %v",
			subjects.MaxNarrowedFilterLabels, err)
	}
}

// One branch of that pair on its own is under the cap and installs, so the
// refusal above is the union and not either branch by itself.
func TestLensLabelCap_SingleBranchUnderCap_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-branches-single", "", map[string]int{"location": 5})
	def.Lenses[0].Spec = ""
	def.Lenses[0].SpecBranches = []string{
		"MATCH (a:alpha)\nMATCH (b:beta)\nMATCH (e0:location*)\nRETURN e0.key AS key\n",
	}
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("one branch naming 2 concrete labels against a budget of 5 is at the cap and must install: %v", err)
	}
}

// Every offending lens in one package is reported in ONE refusal. A package
// declaring three over-budget lenses would otherwise need three install
// attempts to learn about three problems its author can see at once, each
// attempt hiding the next.
func TestLensLabelCap_AllOffendingLensesReportedAtOnce(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-many-offenders", capLensSpec(4, "location"), map[string]int{"location": 5})
	def.Lenses[0].CanonicalName = "capLensOne"
	second := def.Lenses[0]
	second.CanonicalName = "capLensTwo"
	second.Bucket = "cap-many-offenders-two-targets"
	second.Spec = capLensSpec(6, "location")
	def.Lenses = append(def.Lenses, second)

	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("both lenses are over the cap and the install must be refused; got %v", err)
	}
	for _, want := range []string{"capLensOne", "capLensTwo"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("one refusal must name every offending lens (missing %q); got %v", want, err)
		}
	}
}

// The refusal's ADVICE, pinned as behaviour because the obvious move is the
// wrong one. Deleting the offending label does not shrink the count — an
// unlabeled node pattern clears the lens's exhaustiveness, which takes the
// consumer filter broad forever. A refusal that recommended it would trade a
// loud failure for the exact silent regression this gate exists to detect, so
// the message must say the two safe moves AND say that removal is not one.
func TestLensLabelCap_RefusalDoesNotAdviseDroppingTheLabel(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-advice", capLensSpec(4, "location"), map[string]int{"location": 5})
	_, err := inst.Install(ctx, def)
	if !errors.Is(err, ErrLensLabelCap) {
		t.Fatalf("K=4 against a declared budget of 5 is over the cap and must be refused; got %v", err)
	}
	for _, want := range []string{
		"rewrite a redundant concrete label",
		"smaller LeafBudget",
		"Do NOT simply remove the label",
		"BROAD",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must carry the safe remedy and the warning against the unsafe one (missing %q); got %v", want, err)
		}
	}
}

// An abstract type that declares no LeafBudget is WARNED about at its own
// install, naming the consequence rather than the omission: the default it
// takes IS the whole label cap, so it leaves a consuming lens room for no other
// concrete label at all. A warning and never a rejection — LeafBudget is
// legally omissible and "no promise ⇒ assume worst case" is the right default;
// the signal exists to make that default's cost visible to the one actor who
// can change it, not to argue with it.
func TestLeafBudget_UndeclaredOnAbstract_WarnsNeverRejects(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	def := Definition{Name: "silent-abstract", Version: "0.1.0", DDLs: []DDLSpec{abstractDDL("location")}}
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("an omitted LeafBudget is legal and must never fail an install: %v", err)
	}
	var warning string
	for _, w := range res.LeafBudgetWarnings {
		if strings.Contains(w, "location") {
			warning = w
		}
	}
	if warning == "" {
		t.Fatalf("an abstract type declaring no LeafBudget must be warned about; got %v", res.LeafBudgetWarnings)
	}
	for _, want := range []string{"declares no LeafBudget", "whole narrowed-filter label cap"} {
		if !strings.Contains(warning, want) {
			t.Errorf("the warning must name the CONSEQUENCE (missing %q); got %q", want, warning)
		}
	}
}

// The negative half, which is what makes the warning above a signal rather than
// noise: an abstract type that DOES declare a budget is silent.
func TestLeafBudget_DeclaredOnAbstract_NoWarning(t *testing.T) {
	ctx, _, inst := newInstallerHarness(t)

	abstract := abstractDDL("location")
	abstract.LeafBudget = 5
	def := Definition{Name: "declared-abstract", Version: "0.1.0", DDLs: []DDLSpec{abstract}}
	res, err := inst.Install(ctx, def)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, w := range res.LeafBudgetWarnings {
		if strings.Contains(w, "declares no LeafBudget") {
			t.Errorf("a declared LeafBudget must produce no undeclared-budget warning; got %q", w)
		}
	}
}

// The narrowed-filter label cap has ONE definition. This is the assertion that
// keeps pkgmgr's install-time arithmetic and Refractor's runtime fallback
// pricing the same number: leafBudgetDefault is the cap, read from the package
// that owns it, and a change on either side moves both together.
func TestLensLabelCap_BudgetDefaultIsTheOneCap(t *testing.T) {
	if leafBudgetDefault != subjects.MaxNarrowedFilterLabels {
		t.Fatalf("leafBudgetDefault (%d) must BE the narrowed-filter label cap (%d) — §10.2 defines it as that value",
			leafBudgetDefault, subjects.MaxNarrowedFilterLabels)
	}
}
