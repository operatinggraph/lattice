// Personal-lens derivation corpus census —
// personal-lens-derivation-licence-design.md §4.4, §10.
//
// anchor_hopindex_corpus_census_test.go pins whether each anchored cypher's
// pattern graph can ANSWER "which anchors does this event affect". This file
// pins the other half for the personal plane: which lenses are personal at all,
// and whether the narrowing licence's STATIC conjuncts — the ones that are
// properties of the declaration and the cypher, not of a running process — admit
// them.
//
// WHAT IS AND IS NOT PINNED HERE, stated exactly, because a census that implied
// more than it holds would be worse than none:
//
//   - conjunct 0 (this is a personal lens) is pinned from the DECLARATION, the
//     same source cmd/refractor's install switch dispatches on;
//   - conjunct 4 (the row references neither $now nor $projectedAt) is pinned by
//     calling pipeline.PersonalDerivationRuleRefusal — the runtime predicate
//     itself, not a restatement of it, so a conjunct added there is pinned here
//     for free and neither side can answer a question the other would answer
//     differently;
//   - index readiness is pinned from what `derivationIndex` will actually READ
//     for the lens, which for a multi-walk lens is NOT its branches' own
//     AnchorHopIndex. `ruleinstall.go` publishes no `anchorHops` at all when a
//     rule compiles to more than one branch, so what the derivation reads there
//     is the ZERO HopIndex and the lens can never act however well each branch
//     indexes on its own. The per-branch fact is pinned separately, in its own
//     column, because it is the thing Increment 3 changes.
//
// Conjuncts 1, 2, 3 and 5 are about a PROCESS — what the host wired, what the
// standing healer's last pass achieved, how many Refractor instances are live —
// and no corpus census can speak for them. Their vectors are
// TestPersonalDerivationLicence_Conjuncts in the pipeline package.
//
// The directions are asymmetric, as in every sibling census. A lens moving OFF
// licensable only costs the enumerator's breadth. A lens moving ON to it is
// acted on: the pipeline reprojects the anchors the walk derives and no others,
// and on the personal plane an anchor the walk misses is a device still holding
// a row it may no longer be entitled to read.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: read the column that moved. A
// `personal` flag appearing is a new lens on the plane this licence governs and
// needs §4.4's argument re-read for it. A `staticRefusal` clearing is a cypher
// that just became licensable. A `staticRefusal` appearing is only a lens
// falling back, and needs the new reason to be the true one.
//
// DERIVATION COMMAND (re-run this, do not trust a number in a build note):
//
//	go test ./internal/refractor/ -run TestCorpusPersonalDerivation -count=1 -v
package refractor_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The static licence's refusal vocabulary, matched by containment so a reason
// may keep naming the parameter it refused on. Default-denied by
// TestCorpusPersonalDerivation_EveryReasonIsAKnownConjunct: a conjunct added to
// PersonalDerivationRuleRefusal without a constant here would otherwise land in
// the table as whichever existing substring happened to match.
const (
	// personalStaticClear is the answer, not a refusal: nothing about this
	// cypher stops the licence, and whether it is actually granted is a question
	// about the process the pipeline package's vectors hold.
	personalStaticClear    = ""
	personalStaticClock    = "depends on $"
	personalStaticUnproven = "could not be proven free of $"
	personalStaticNotFull  = "not a full-engine rule"
)

// The index column's closed vocabulary: "" means derivationIndex will find a
// usable index for this cypher's LENS, and every other value names the conjunct
// that will refuse it.
const (
	personalIndexReady = ""
	// personalIndexNoBranchIndex is pipeline's own constant, not a respelling:
	// the census pins the string an operator reads in the refusal log, so the
	// two must be the same object or the pin is about a different message.
	personalIndexNoBranchIndex = pipeline.DerivationNoBranchIndexRefusal
)

// personalDerivationVerdict is one corpus cypher's row.
type personalDerivationVerdict struct {
	// personal is the DECLARATION's answer, read from the same field
	// cmd/refractor's install switch dispatches on.
	personal bool
	// staticRefusal is pipeline.PersonalDerivationRuleRefusal's verdict.
	staticRefusal string
	// branchIndexReady is whether THIS CYPHER's own pattern graph can answer —
	// complete, no unresolved `*` expansion, and an anchor position labelled
	// with the personal plane's actor type. It is a property of the cypher.
	branchIndexReady bool
	// indexRefusal is what `derivationIndex` will read for the LENS this cypher
	// belongs to: "" when it will find a usable index, and the named conjunct
	// otherwise.
	//
	// The two columns differ for exactly one class today, and that class is the
	// headline: a MULTI-WALK lens's branches each index perfectly
	// (branchIndexReady true) while `ruleinstall.go` publishes no index for the
	// lens at all, so `rs.anchorHops` is the zero HopIndex and the derivation
	// refuses (indexRefusal set). Pinning only the per-branch fact would have
	// recorded edgeCatalog — the 128 k backlog this design's payoff table leads
	// with — as ready to act while at runtime it derives nothing.
	//
	// Increment 3 arms a per-branch index and re-pins these rows to "". That
	// flip is the payoff record for the multi-walk half of the design, which is
	// why the column exists rather than the rows simply being omitted.
	indexRefusal string
}

// corpusPersonalLensNames is the population this licence governs, pinned by
// name. All fifteen are the edge-manifest package's Personal lenses; the kernel
// ships none, and no package outside edge-manifest declares one.
//
// Pinned as an exact SET with a floor, not as a count: a census whose
// enumeration silently reached nothing reads identically to a table of unchanged
// rows, and a new personal lens is precisely the event that needs §4.4's
// argument read again.
var corpusPersonalLensNames = []string{
	"edgeCatalog#0", "edgeCatalog#1", "edgeCatalog#2",
	"edgeEntityBookings",
	"edgeEntityMenuItems",
	"edgeEntityProviders",
	"edgeEntitySessions#0", "edgeEntitySessions#1",
	"edgeEntityStudios",
	"edgeEntityTabs",
	"edgeIdentity",
	"edgeInstances",
	"edgeProviderQueue",
	"edgeProviderSchedule",
	"edgeServices",
	"edgeStaffPanes",
	"edgeStaffWorkOrders",
	"edgeTasks#0", "edgeTasks#1",
}

// corpusPersonalDerivationVerdicts pins every personal cypher's static verdict.
//
// Only the PERSONAL rows are pinned. A non-personal cypher's static refusal is
// "it is not a personal lens" by construction and pinning the sixty of them
// would bury the rows that can move under rows that cannot.
var corpusPersonalDerivationVerdicts = map[string]personalDerivationVerdict{
	// Every shipped personal cypher is clock-free and its own pattern graph is
	// hop-indexed. The clock conjunct is therefore LATENT across the whole
	// corpus, which is exactly why it is pinned: the day an author writes $now
	// into a personal cypher, this table moves rather than the narrowing
	// silently starting to publish a row only the sweep would refresh.
	//
	// The seven `#N` rows below whose indexRefusal is personalIndexNoBranchIndex
	// are the THREE multi-walk lenses — edgeCatalog (the 128 k backlog),
	// edgeTasks, edgeEntitySessions. Each branch indexes; the LENS holds no
	// index, because ruleinstall.go publishes none for a rule with more than one
	// branch. Increment 2's licence alone therefore delivers them nothing, and
	// Increment 3 is what re-pins these seven to ready.
	"edgeCatalog#0":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexNoBranchIndex},
	"edgeCatalog#1":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexNoBranchIndex},
	"edgeCatalog#2":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexNoBranchIndex},
	"edgeEntityBookings":   {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeEntityMenuItems":  {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeEntityProviders":  {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeEntitySessions#0": {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexNoBranchIndex},
	"edgeEntitySessions#1": {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexNoBranchIndex},
	"edgeEntityStudios":    {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeEntityTabs":       {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeIdentity":         {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeInstances":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeProviderQueue":    {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeProviderSchedule": {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeServices":         {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeStaffPanes":       {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeStaffWorkOrders":  {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexReady},
	"edgeTasks#0":          {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexNoBranchIndex},
	"edgeTasks#1":          {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, indexRefusal: personalIndexNoBranchIndex},
}

// corpusPersonalDerivation runs the REAL predicates over every executable corpus
// cypher, through the enumeration every census in this package shares.
func corpusPersonalDerivation(t *testing.T) map[string]personalDerivationVerdict {
	t.Helper()
	eng := full.New()
	got := map[string]personalDerivationVerdict{}
	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, declaredPersonal bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)

		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = personalDerivationVerdict{
			personal:         declaredPersonal,
			staticRefusal:    pipeline.PersonalDerivationRuleRefusal(cr),
			branchIndexReady: indexReadyForPersonal(fullCR),
		}
	})

	// The lens-level column, filled in a second pass because it is a property of
	// the LENS and the enumeration is per executable cypher. A canonical name
	// carrying a `#N` suffix is one branch of a multi-walk rule — but only when
	// its lens has more than one, since a rule declaring exactly one SpecBranch
	// is still enumerated as `name#0` and ruleinstall.go's exclusion is
	// `len(branches) > 1`. Counting rather than testing for the suffix is what
	// keeps this census reading the same rule the installer does.
	branches := map[string]int{}
	for name := range got {
		branches[lensNameOf(name)]++
	}
	for name, v := range got {
		if branches[lensNameOf(name)] > 1 {
			v.indexRefusal = personalIndexNoBranchIndex
		} else if !v.branchIndexReady {
			v.indexRefusal = personalIndexBranchRefused
		}
		got[name] = v
	}
	return got
}

// personalIndexBranchRefused stands for "this single-walk cypher's own pattern
// graph cannot answer". No shipped personal lens is in this state, so it is
// latent — and named rather than left as a bare bool so a lens that arrives here
// is a pinned verdict rather than an unreadable row.
const personalIndexBranchRefused = "the cypher's own pattern graph cannot answer for its anchor"

// lensNameOf strips the `#N` branch suffix forEachCorpusCypher appends, so the
// per-cypher rows can be grouped back into the lens the installer sees.
func lensNameOf(cypherName string) string {
	if i := strings.LastIndex(cypherName, "#"); i >= 0 {
		return cypherName[:i]
	}
	return cypherName
}

// indexReadyForPersonal answers derivationIndex's three rule-level conjuncts for
// a personal lens: a complete pattern graph, no unresolved `*` expansion, and an
// anchor position whose label is the actor type projection.InstallPersonalLens
// configures the enumerator with.
//
// It reads the SAME full.HopIndex derivationIndex reads and the SAME actor-type
// symbol InstallPersonalLens passes, rather than re-deriving either.
func indexReadyForPersonal(fullCR *full.CompiledRule) bool {
	ix := fullCR.AnchorHopIndex()
	if !ix.Complete || ix.UnresolvedExpansionPosition() >= 0 {
		return false
	}
	return ix.Labels[ix.Anchor] == projection.PersonalActorType
}

func TestCorpusPersonalDerivation(t *testing.T) {
	got := corpusPersonalDerivation(t)

	// The population, as an exact set. A name appearing is a new lens on the
	// plane this licence governs; a name disappearing is a lens retired or a
	// declaration that stopped saying Personal, and both need a look.
	var personal []string
	for name, v := range got {
		if v.personal {
			personal = append(personal, name)
		}
	}
	sort.Strings(personal)
	want := append([]string(nil), corpusPersonalLensNames...)
	sort.Strings(want)
	require.Equal(t, want, personal,
		"the personal-lens population moved — every name here is governed by the derivation licence, so read §4.4 for the one that arrived before pinning it")

	// The MULTI-WALK sub-population, as an exact set. It is the class the two
	// index columns disagree about, so a grouping bug — `lensNameOf` failing to
	// strip the branch suffix, say — would silently collapse every row to
	// single-walk and pin the three biggest personal lenses as ready to act
	// while at runtime they derive nothing. Asserting the class directly is what
	// keeps this table's all-important seven rows from passing by accident.
	var multiWalk []string
	for name, v := range got {
		if v.personal && v.indexRefusal == personalIndexNoBranchIndex {
			multiWalk = append(multiWalk, name)
		}
	}
	sort.Strings(multiWalk)
	require.Equal(t, []string{
		"edgeCatalog#0", "edgeCatalog#1", "edgeCatalog#2",
		"edgeEntitySessions#0", "edgeEntitySessions#1",
		"edgeTasks#0", "edgeTasks#1",
	}, multiWalk,
		"the multi-walk personal population moved — these are the lenses Increment 2's licence alone delivers NOTHING to, and Increment 3 is what re-pins them")

	// And a floor on the count, so an enumeration that silently reached nothing
	// cannot read as a table of unchanged rows.
	require.GreaterOrEqualf(t, len(personal), 15,
		"only %d personal cyphers reached this census — the corpus enumeration has moved", len(personal))

	for name, want := range corpusPersonalDerivationVerdicts {
		have, present := got[name]
		require.Truef(t, present, "pinned lens %q no longer reaches this census — remove its row if the lens was retired", name)
		require.Equalf(t, want.personal, have.personal,
			"%s's Personal declaration moved; the install switch dispatches on this field, so it decides which arm the lens runs on", name)
		require.Equalf(t, want.branchIndexReady, have.branchIndexReady,
			"%s's own pattern graph changed what it can answer", name)
		require.Equalf(t, want.indexRefusal, have.indexRefusal,
			"%s's LENS-level index verdict moved — this is what derivationIndex will actually read, and a move to ready means a licensed lens now acts on a derived set", name)
		if want.staticRefusal == personalStaticClear {
			require.Emptyf(t, have.staticRefusal,
				"%s picked up a static licence refusal (%s) — a fallback, not a defect, but the pin has to say so", name, have.staticRefusal)
			continue
		}
		require.Containsf(t, have.staticRefusal, want.staticRefusal,
			"%s's declining static conjunct moved; a move TO clear means this lens is one process-wiring assertion away from being narrowed", name)
	}

	for name, v := range got {
		if !v.personal {
			continue
		}
		_, pinned := corpusPersonalDerivationVerdicts[name]
		require.Truef(t, pinned,
			"personal lens %q ships with no pinned derivation verdict (static refusal %q, branch index ready %v, lens index refusal %q) — review it against §4.4, then record it in corpusPersonalDerivationVerdicts",
			name, v.staticRefusal, v.branchIndexReady, v.indexRefusal)
	}
}

// TestCorpusPersonalDerivation_EveryReasonIsAKnownConjunct default-denies the
// vocabulary above, the way the anchor-index census default-denies its own: a
// conjunct added to the static licence without a constant here would land in the
// table as whichever existing substring happened to match, and the census would
// keep reporting green while the reason an operator reads means something nobody
// pinned.
func TestCorpusPersonalDerivation_EveryReasonIsAKnownConjunct(t *testing.T) {
	known := []string{personalStaticClock, personalStaticUnproven, personalStaticNotFull}
	for name, v := range corpusPersonalDerivation(t) {
		if v.staticRefusal == "" {
			continue
		}
		matched := false
		for _, k := range known {
			if strings.Contains(v.staticRefusal, k) {
				matched = true
				break
			}
		}
		require.Truef(t, matched,
			"%s declines with %q, which matches no conjunct constant in this file — name it before pinning it", name, v.staticRefusal)
	}
}

// TestCorpusPersonalDerivation_TheClockConjunctIsLatentNotAbsent is the
// anti-vacuity case. Every shipped personal cypher clears the static licence, so
// the table above is all-clear and would look identical if the predicate had
// been replaced with `return ""`. This runs the SAME predicate over a cypher
// that does reference the clock.
func TestCorpusPersonalDerivation_TheClockConjunctIsLatentNotAbsent(t *testing.T) {
	for _, param := range []string{"$now", "$projectedAt"} {
		cr, err := full.New().Parse(
			"MATCH (identity:identity {key: $actorKey})-[:mayRead]->(x:unit)\nRETURN x.key AS anchor, " + param + " AS asOf")
		require.NoError(t, err)
		refusal := pipeline.PersonalDerivationRuleRefusal(cr)
		require.Containsf(t, refusal, personalStaticClock,
			"a personal cypher returning %s must be refused the licence: after this narrowing only the sweep would refresh that row", param)
	}
}
