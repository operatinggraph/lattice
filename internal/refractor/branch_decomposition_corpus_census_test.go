// Branch-decomposition corpus census —
// full-engine-independent-branch-decomposition-design.md §2 / §7.
//
// The design stated this mechanism's population by reading cypher by eye:
// "fourteen lenses have two or more sibling branch groups in one stage". That
// number was never pinned by anything executable, and an eye-census of exactly
// this shape has already been wrong twice in this package — the grouping-key
// design's was wrong in BOTH directions, and this one counts a quantity the eye
// systematically mis-reads: capabilityEphemeral has NINE OPTIONAL MATCH clauses
// and THREE branch groups, because chained clauses collapse into one group.
//
// So it gets mechanized. This file asks the analysis ITSELF what every shipped
// cypher earns and pins the answer — never a grep of cypher text, never a
// reimplementation of the predicate, which would only agree with a broken gate.
// A lens that starts or stops decomposing, or whose branches regroup, fails here
// and forces a deliberate re-reading rather than a silent change to what the
// engine materializes.
//
// It shares forEachCorpusCypher with the label and grouping censuses on purpose
// — read the label census's note on why a second sweep of its own would quietly
// cover a different corpus and pin a different thing.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: the verdict is what to review,
// not the table. `g3/o9` is three sibling branch groups across nine OPTIONAL
// MATCH clauses in that stage; `[a,b;c]` names each subtree the executor now
// folds separately, by its variables; `!code` is the shape the analysis would
// not prove, and every one of those is the fail-safe product path. A lens
// GAINING a `[…]` is a lens whose branches this engine now evaluates apart —
// satisfy yourself its projected rows cannot move (a decomposition equivalence
// test in ruleengine/full is what does that), then record it.
package refractor_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// branchVerdict is one executable cypher's pinned branch-decomposition answer.
type branchVerdict struct {
	// stages is the per-projecting-clause verdict in clause order, space-joined.
	stages string
	// folded is how many candidate subtrees the executor evaluates separately
	// across every stage — the count that is zero for a lens this mechanism does
	// not touch at all.
	folded int
	// groups is the most sibling branch groups any ONE stage holds, which is the
	// quantity the design's "fourteen lenses" claim is about.
	groups int
}

// branchStageVerdict renders one projecting clause's answer.
func branchStageVerdict(s full.BranchStageDecomposition) string {
	out := fmt.Sprintf("g%d/o%d", s.Groups, s.Optional)
	if s.Refusal != "" {
		out += "!" + strings.SplitN(s.Refusal, ":", 2)[0]
	}
	if len(s.Deferred) > 0 {
		out += "[" + strings.Join(s.Deferred, ";") + "]"
	}
	return out
}

func branchVerdictOf(cr *full.CompiledRule) branchVerdict {
	v := branchVerdict{}
	parts := []string{}
	for _, s := range cr.BranchDecomposition() {
		parts = append(parts, branchStageVerdict(s))
		v.folded += len(s.Deferred)
		if s.Groups > v.groups {
			v.groups = s.Groups
		}
	}
	v.stages = strings.Join(parts, " ")
	return v
}

// corpusBranchDecomposition derives the verdict for every executable cypher the
// corpus ships, through the real analysis and nothing else.
func corpusBranchDecomposition(t *testing.T) map[string]branchVerdict {
	t.Helper()
	eng := full.New()
	got := map[string]branchVerdict{}
	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		_, duplicate := got[name]
		require.Falsef(t, duplicate, "two corpus cyphers share the name %q", name)
		got[name] = branchVerdictOf(fullCR)
	})
	return got
}

// TestCorpusBranchDecomposition_PinnedVerdicts is the census. Every executable
// cypher is pinned, both the ones that decompose and the ones that do not — an
// unpinned lens fails, and so does a pinned one whose answer moved.
func TestCorpusBranchDecomposition_PinnedVerdicts(t *testing.T) {
	got := corpusBranchDecomposition(t)
	require.Greaterf(t, len(got), 100,
		"the corpus enumeration collapsed to %d cyphers — this census is only worth what it covers", len(got))

	for name, want := range corpusBranchVerdicts {
		have, present := got[name]
		if !assert.Truef(t, present,
			"pinned lens %q is no longer installed — remove its row if the lens was retired", name) {
			continue
		}
		require.Equalf(t, want.stages, have.stages,
			"%s's branch-decomposition verdict moved. The engine now materializes a different binding "+
				"set for this lens, which is a change to what it evaluates, not a refactor", name)
		require.Equalf(t, want.folded, have.folded, "%s folds a different number of subtrees than pinned", name)
		require.Equalf(t, want.groups, have.groups, "%s's widest stage holds a different number of sibling groups than pinned", name)
	}
	for name, have := range got {
		_, pinned := corpusBranchVerdicts[name]
		require.Truef(t, pinned,
			"lens %q ships with no pinned branch-decomposition verdict (derived: stages=%q folded=%d groups=%d) — "+
				"review it, then record it in corpusBranchVerdicts",
			name, have.stages, have.folded, have.groups)
	}
}

// TestCorpusBranchDecomposition_DecomposingLensesAreTheKnownPopulation names the
// lenses this mechanism actually changes the evaluation of, as a list rather
// than as a property of the table above. Losing one would leave every other row
// in that table untouched: the regression reads as an unchanged census with one
// fewer decomposing lens, which is exactly how the neighbouring design's census
// went wrong.
func TestCorpusBranchDecomposition_DecomposingLensesAreTheKnownPopulation(t *testing.T) {
	got := corpusBranchDecomposition(t)
	names := []string{}
	for name, v := range got {
		if v.folded > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	require.Equal(t, decomposingCorpusLenses, names,
		"the population of lenses whose sibling branches this engine evaluates apart has changed. "+
			"A lens joining this list needs an equivalence proof before it ships; one leaving it means "+
			"the decomposition stopped applying where the design says it does")
	require.GreaterOrEqual(t, len(names), 18,
		"the decomposing population collapsed — an empty enumeration would otherwise read as a table of unchanged rows")
}

// TestCorpusBranchDecomposition_SiblingGroupPopulationIsTheDesignsClaim pins the
// design's own §2 number. It claimed FOURTEEN lenses with two or more sibling
// branch groups in one stage; the fire brief's independent coarse scan found
// thirty-two cypher literals with two or more OPTIONAL MATCH clauses in one
// stage, and named that an upper bound because chained clauses collapse. Neither
// number was executable. This is: the real count is what the analysis derives,
// and the list is what a future reader argues with.
func TestCorpusBranchDecomposition_SiblingGroupPopulationIsTheDesignsClaim(t *testing.T) {
	got := corpusBranchDecomposition(t)
	names := []string{}
	for name, v := range got {
		if v.groups >= 2 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	require.Equal(t, siblingBranchGroupLenses, names,
		"the population of lenses with two or more SIBLING branch groups in one stage has changed")

	// Of that population, the ones the mechanism cannot help are the ones §4.2
	// refuses (a non-DISTINCT collect/count anywhere in the stage) and the ones
	// whose stage aggregates nothing (the branch product IS the intended output
	// cardinality there). Naming them keeps the difference between "wide" and
	// "wide and foldable" visible in the census rather than in a comment.
	refused := 0
	for _, name := range names {
		if got[name].folded == 0 {
			refused++
		}
	}
	require.Equal(t, len(names)-len(multiGroupDecomposingLenses), refused,
		"the wide lenses that decompose and the wide lenses that do not must partition the population")
}

// TestCorpusBranchDecomposition_FootprintClassifierVerdictsUnchanged is §7's
// pinned classifier census. hasMultiBindingConjunctUnit
// (projection/footprint_classifier.go) reads the compiled AST, not the
// executor's row sets, so decomposition cannot move its verdict — and that is
// exactly the kind of "cannot" the §12 fire already shipped a regression
// against. The verdict gates whether an actor-aggregate evaluation is validated
// against its read-surface footprint at all, so a silent flip in the exempt
// direction is a lens that stops being checked for mid-evaluation drift.
//
// Only actorAggregate lenses are pinned: they are the population
// projection.Compile builds a plan for, and the only one the classifier gates.
func TestCorpusBranchDecomposition_FootprintClassifierVerdictsUnchanged(t *testing.T) {
	eng := full.New()
	got := map[string]bool{}
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, declaredActorAggregate, _ bool) {
		if !declaredActorAggregate {
			return
		}
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		rule.CompiledRule = cr
		plan, err := projection.Compile(rule)
		require.NoErrorf(t, err, "%s must compile to a projection plan", name)
		got[name] = plan.RequiresFootprintValidation
	})

	require.Equal(t, footprintValidationVerdicts, got,
		"an actorAggregate lens's footprint-validation verdict moved. The classifier reads the AST and "+
			"nothing the executor materializes, so decomposition cannot be the cause — read the cypher change")
	require.GreaterOrEqual(t, len(got), 15,
		"the actorAggregate enumeration collapsed — an empty map would otherwise read as an unchanged census")
}
