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

// An expansion label naming a type nothing declares is skipped, never refused:
// install ORDER is unconstrained, so a lens package may legally land before the
// package declaring the abstract it expands. Refusing here would invent an
// ordering the platform does not have — and the runtime is the fail-closed
// point for it (useFullEngineBranches refuses to ACTIVATE a `*` lens whose
// expansion is unresolvable).
func TestLensLabelCap_UnresolvableExpansionLabel_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	def := capLensDef("cap-unresolvable", capLensSpec(9, "nosuchtype"), nil)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("an expansion label naming no declared type carries no budget to enforce: %v", err)
	}
}

// A `*` on a CONCRETE type is legal (§3.4/amendment A5) and carries no budget —
// LeafBudget is refused on a non-abstract DDL, so there is no declaration to
// price and no fix available to an author refused over one.
func TestLensLabelCap_ConcreteExpansionTarget_Installs(t *testing.T) {
	ctx, inst := capHarness(t)

	owner := Definition{
		Name:    "concrete-owner",
		Version: "0.1.0",
		DDLs:    []DDLSpec{minimalDDL("place", ddlClassVertexType, false)},
	}
	if _, err := inst.Install(ctx, owner); err != nil {
		t.Fatalf("install the concrete type's owning package: %v", err)
	}

	def := capLensDef("cap-concrete-star", capLensSpec(9, "place"), nil)
	if _, err := inst.Install(ctx, def); err != nil {
		t.Fatalf("a `*` on a concrete type has no LeafBudget to charge: %v", err)
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
