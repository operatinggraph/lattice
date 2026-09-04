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
//   - index readiness is pinned from what `derivationIndexes` will actually READ
//     for the lens, which for a multi-walk lens is the set of its branches'
//     graphs — pinned by calling pipeline.BranchDerivationRefusal, the runtime
//     predicate itself rather than a restatement of it, so a conjunct added
//     there is pinned here for free. The per-branch fact stays in its own column
//     beside it, because the two are different questions: one walk's graph can
//     answer while the LENS is refused on a conjunct about the set.
//
// THE TAXONOMY EXPANSION IS RESOLVED, NOT SKIPPED. ruleinstall.go builds these
// graphs with the label expansion it resolved over the lens's whole branch set,
// and a `*` position reads UNRESOLVED without one — so a census that passed nil
// would pin a `*`-carrying lens as refused while the installed lens derives.
// Both columns take the same map, from the same armed resolver every sibling
// census in this package uses (corpusTaxonomyResolver, built from the corpus's
// own vertexType DDLs), and the pair of readings for one branch set is pinned
// directly in TestCorpusPersonalDerivation_TheBranchConjunctsAreLatentNotAbsent.
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
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/refractor/taxonomy"
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
	// Every other value is pipeline's own constant, not a respelling: the census
	// pins the string an operator reads in the refusal log, so the two must be
	// the same object or the pin is about a different message.
	//
	// None of the four is reached by the shipped corpus — every multi-walk lens
	// resolves a graph per walk and they agree — so all four are LATENT here,
	// which is why TestCorpusPersonalDerivation_TheBranchConjunctsAreLatentNotAbsent
	// runs the same predicate over branch sets that do reach them. A vocabulary
	// whose every member is unreachable would look identical to a predicate that
	// had been replaced with `return ""`.
	personalIndexNoBranchIndex        = pipeline.DerivationNoBranchIndexRefusal
	personalIndexBranchIncomplete     = pipeline.DerivationBranchIncompleteRefusal
	personalIndexBranchUnresolvedExp  = pipeline.DerivationBranchUnresolvedExpansionRefusal
	personalIndexBranchAnchorDisagree = pipeline.DerivationBranchAnchorDisagreementRefusal
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
	// walks is how many executable cyphers the LENS this row belongs to ships.
	// Pinned rather than derived at assertion time because it is what selects
	// the arm: `> 1` is ruleinstall.go's own gate, and a grouping bug that
	// collapsed the multi-walk lenses to single-walk would make the seven
	// branch rows pass by reading the wrong column.
	walks int
	// indexRefusal is what `derivationIndexes` will read for the LENS this
	// cypher belongs to: "" when it will find a usable index set, and the named
	// conjunct otherwise.
	//
	// The two columns answer different questions, and the difference is the
	// whole point of the second: a walk's own graph can answer while the LENS is
	// refused on a conjunct about the SET — one walk incomplete, one carrying an
	// unresolved `*`, or two walks anchoring on different labels. Pinning only
	// the per-branch fact would record a lens as ready to act while at runtime
	// it derives nothing.
	//
	// The seven multi-walk rows read ready here, and that reading is what the
	// multi-walk half of the design is worth: it is the difference between
	// edgeCatalog — the 128 k backlog the payoff table leads with — deriving a
	// handful of relation-filtered reads per event and re-executing its cypher
	// once per actor an undirected walk reaches.
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
	// The seven `#N` rows below carrying walks > 1 are the THREE multi-walk
	// lenses — edgeCatalog (the 128 k backlog), edgeTasks, edgeEntitySessions.
	// Each walk indexes, they all anchor on the same label, and the derivation
	// walks every one of them and unions what they reach; their indexRefusal is
	// therefore READY. A move OFF ready on one of these is the whole lens
	// dropping to the enumerator, which is the 128 k backlog again.
	"edgeCatalog#0":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 3, indexRefusal: personalIndexReady},
	"edgeCatalog#1":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 3, indexRefusal: personalIndexReady},
	"edgeCatalog#2":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 3, indexRefusal: personalIndexReady},
	"edgeEntityBookings":   {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeEntityMenuItems":  {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeEntityProviders":  {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeEntitySessions#0": {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 2, indexRefusal: personalIndexReady},
	"edgeEntitySessions#1": {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 2, indexRefusal: personalIndexReady},
	"edgeEntityStudios":    {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeEntityTabs":       {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeIdentity":         {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeInstances":        {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeProviderQueue":    {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeProviderSchedule": {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeServices":         {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeStaffPanes":       {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeStaffWorkOrders":  {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 1, indexRefusal: personalIndexReady},
	"edgeTasks#0":          {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 2, indexRefusal: personalIndexReady},
	"edgeTasks#1":          {personal: true, staticRefusal: personalStaticClear, branchIndexReady: true, walks: 2, indexRefusal: personalIndexReady},
}

// corpusPersonalDerivation runs the REAL predicates over every executable corpus
// cypher, through the enumeration every census in this package shares.
func corpusPersonalDerivation(t *testing.T) map[string]personalDerivationVerdict {
	t.Helper()
	eng := full.New()
	got := map[string]personalDerivationVerdict{}
	// The compiled branches of each lens, in the order forEachCorpusCypher
	// enumerates them — which is Walks declaration order, the order
	// ruleinstall.go receives them in. The lens-level verdict is asked of the
	// SET, so the set has to be assembled the way the installer assembles it.
	compiled := map[string][]ruleengine.CompiledRule{}
	perCypher := map[string]*full.CompiledRule{}
	resolver := corpusTaxonomyResolver(t)
	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, declaredPersonal bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)

		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = personalDerivationVerdict{
			personal:      declaredPersonal,
			staticRefusal: pipeline.PersonalDerivationRuleRefusal(cr),
		}
		lensName := lensNameOf(name)
		perCypher[name] = fullCR
		compiled[lensName] = append(compiled[lensName], cr)
	})

	// The lens-level column, filled in a second pass because it is a property of
	// the LENS and the enumeration is per executable cypher. A canonical name
	// carrying a `#N` suffix is one branch of a multi-walk rule — but only when
	// its lens has more than one, since a rule declaring exactly one SpecBranch
	// is still enumerated as `name#0` and ruleinstall.go's arm is
	// `len(branches) > 1`. Counting rather than testing for the suffix is what
	// keeps this census reading the same rule the installer does.
	//
	// The multi-walk verdict comes from pipeline.BranchDerivationRefusal — the
	// shipped predicate, over the same branch set the installer resolves graphs
	// from — rather than from a restatement here. What it cannot speak for is
	// the one conjunct that is not a property of the compiled rules (the anchor
	// label must be the enumerator's actor type, and the enumerator is installed
	// after the rule); that is the branchIndexReady column's own question, and
	// it is folded in below for both arms alike.
	//
	// The TAXONOMY EXPANSION is threaded into both, resolved per LENS over its
	// whole branch set exactly as useFullEngineBranches resolves it. Passing nil
	// instead would answer about a different lens: an expansion-carrying position
	// reads UNRESOLVED with no expansion and resolved with one, so a `*` lens
	// would be pinned as refused here while the installer derived for it.
	for name, v := range got {
		lensName := lensNameOf(name)
		v.walks = len(compiled[lensName])
		expanded := corpusBranchLabelExpansion(t, resolver, compiled[lensName])
		v.branchIndexReady = indexReadyForPersonal(perCypher[name], expanded)
		switch {
		case v.walks > 1:
			v.indexRefusal = pipeline.BranchDerivationRefusal(compiled[lensName], expanded)
			if v.indexRefusal == "" && !allBranchesReadyForPersonal(compiled[lensName], expanded) {
				v.indexRefusal = personalIndexBranchRefused
			}
		case !v.branchIndexReady:
			v.indexRefusal = personalIndexBranchRefused
		}
		got[name] = v
	}
	return got
}

// corpusBranchLabelExpansion resolves the taxonomy expansion ruleinstall.go
// would thread into a lens's pattern graphs, over the same branch set and from
// the same declarations the installed corpus ships.
//
// It mirrors useFullEngineBranches' own arithmetic: union every branch's
// ExpansionLabels, and consult the resolver only when that union is non-empty
// (a sigil-free query never touches the resolver, which is the taxonomy design's
// inertness guarantee). A StatusUnknown answer is what makes activation REFUSE,
// so a census pinning that lens is pinning a lens that does not run — it fails
// here rather than quietly reading the nil-expansion verdict, which is exactly
// the substitution this parameter exists to prevent.
func corpusBranchLabelExpansion(t *testing.T, resolver *taxonomy.Resolver, branches []ruleengine.CompiledRule) map[string]map[string]struct{} {
	t.Helper()
	needed := map[string]struct{}{}
	for _, c := range branches {
		fullCR, isFull := c.(*full.CompiledRule)
		if !isFull {
			continue
		}
		for l := range fullCR.ExpansionLabels() {
			needed[l] = struct{}{}
		}
	}
	if len(needed) == 0 {
		return nil
	}
	expanded, _, status, reason := resolver.Expand(needed)
	require.NotEqualf(t, taxonomy.StatusUnknown, status,
		"the corpus taxonomy cannot expand a `*` this lens carries (%s) — activation would refuse the lens outright, so there is no derivation verdict to pin for it", reason)
	return expanded
}

// allBranchesReadyForPersonal answers the conjunct BranchDerivationRefusal
// deliberately does not: every walk's anchor position must be labelled with the
// personal plane's actor type. The runtime asks it live, off the installed
// ActorEnumerator; a census can only ask it of the declaration.
//
// It takes the same expansion its sibling predicate does, for the same reason:
// two readings of one lens's graphs that disagree about `*` would put the
// per-branch column and the lens column on different lenses.
func allBranchesReadyForPersonal(branches []ruleengine.CompiledRule, expanded map[string]map[string]struct{}) bool {
	for _, cr := range branches {
		fullCR, isFull := cr.(*full.CompiledRule)
		if !isFull || !indexReadyForPersonal(fullCR, expanded) {
			return false
		}
	}
	return true
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
// symbol InstallPersonalLens passes, rather than re-deriving either — including
// the label expansion the installer threads in, without which a `*` position
// reads unresolved and the lens is pinned as refused while the installer derives
// for it.
func indexReadyForPersonal(fullCR *full.CompiledRule, expanded map[string]map[string]struct{}) bool {
	ix := fullCR.AnchorHopIndex().WithLabelExpansion(expanded)
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

	// The MULTI-WALK sub-population, as an exact set, selected by the arm the
	// INSTALLER takes rather than by a verdict. A grouping bug — `lensNameOf`
	// failing to strip the branch suffix, say — would otherwise collapse every
	// row to single-walk and quietly pin the three biggest personal lenses
	// against the wrong arm's predicate. Asserting the class directly is what
	// keeps this table's all-important seven rows from passing by accident.
	// Selected by the branch COUNT rather than by a verdict, so the class stays
	// asserted whatever those rows' verdicts read.
	var multiWalk []string
	for name, v := range got {
		if v.personal && v.walks > 1 {
			multiWalk = append(multiWalk, name)
		}
	}
	sort.Strings(multiWalk)
	require.Equal(t, []string{
		"edgeCatalog#0", "edgeCatalog#1", "edgeCatalog#2",
		"edgeEntitySessions#0", "edgeEntitySessions#1",
		"edgeTasks#0", "edgeTasks#1",
	}, multiWalk,
		"the multi-walk personal population moved — these are the lenses the per-branch union delivers, and the biggest backlogs on the plane")

	// And every one of them acts. This is the payoff record: before the union,
	// each of these seven walks indexed perfectly while the LENS derived nothing.
	for _, name := range multiWalk {
		require.Emptyf(t, got[name].indexRefusal,
			"%s must derive: %s", name, got[name].indexRefusal)
	}

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
		require.Equalf(t, want.walks, have.walks,
			"%s's lens gained or lost a walk — that is what selects the arm the derivation runs, so re-read §4.5 before re-pinning", name)
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
			"personal lens %q ships with no pinned derivation verdict (static refusal %q, branch index ready %v, walks %d, lens index refusal %q) — review it against §4.4, then record it in corpusPersonalDerivationVerdicts",
			name, v.staticRefusal, v.branchIndexReady, v.walks, v.indexRefusal)
	}
}

// TestCorpusPersonalDerivation_EveryReasonIsAKnownConjunct default-denies the
// vocabulary above, the way the anchor-index census default-denies its own: a
// conjunct added to the static licence without a constant here would land in the
// table as whichever existing substring happened to match, and the census would
// keep reporting green while the reason an operator reads means something nobody
// pinned.
func TestCorpusPersonalDerivation_EveryReasonIsAKnownConjunct(t *testing.T) {
	knownStatic := []string{personalStaticClock, personalStaticUnproven, personalStaticNotFull}
	knownIndex := []string{
		personalIndexNoBranchIndex, personalIndexBranchIncomplete,
		personalIndexBranchUnresolvedExp, personalIndexBranchAnchorDisagree,
		personalIndexBranchRefused,
	}
	contains := func(known []string, reason string) bool {
		for _, k := range known {
			if strings.Contains(reason, k) {
				return true
			}
		}
		return false
	}
	for name, v := range corpusPersonalDerivation(t) {
		if v.staticRefusal != "" {
			require.Truef(t, contains(knownStatic, v.staticRefusal),
				"%s declines with %q, which matches no conjunct constant in this file — name it before pinning it", name, v.staticRefusal)
		}
		// The index column is default-denied the same way, and for the sharper
		// reason: its vocabulary is entirely latent on the shipped corpus, so a
		// conjunct added to the multi-walk predicate would land in the table as
		// the first substring that happened to match — or as an unnamed row —
		// with every assertion above still green.
		if v.indexRefusal != "" {
			require.Truef(t, contains(knownIndex, v.indexRefusal),
				"%s's index declines with %q, which matches no conjunct constant in this file — name it before pinning it", name, v.indexRefusal)
		}
	}
}

// TestCorpusPersonalDerivation_TheBranchConjunctsAreLatentNotAbsent is the
// anti-vacuity case for the index column.
//
// Every shipped multi-walk lens clears the multi-walk predicate, so the table
// above would look identical if BranchDerivationRefusal had been replaced with
// `return ""` — and that replacement is precisely the fail-open one: a lens
// whose walks disagree would then act on a union that cannot be right. This runs
// the SAME predicate the census calls over branch sets that do reach each
// conjunct.
func TestCorpusPersonalDerivation_TheBranchConjunctsAreLatentNotAbsent(t *testing.T) {
	eng := full.New()
	parse := func(spec string) ruleengine.CompiledRule {
		cr, err := eng.Parse(spec)
		require.NoError(t, err)
		return cr
	}
	sound := parse("MATCH (identity:identity {key: $actorKey})-[:mayRead]->(x:unit)\nRETURN x.key AS anchor")

	require.Empty(t, pipeline.BranchDerivationRefusal([]ruleengine.CompiledRule{
		sound, parse("MATCH (identity:identity {key: $actorKey})-[:mayBook]->(x:unit)\nRETURN x.key AS anchor"),
	}, nil), "positive vector: two walks that agree must admit, or the negatives below prove nothing")

	require.Contains(t, pipeline.BranchDerivationRefusal([]ruleengine.CompiledRule{
		sound, parse("MATCH (identity:identity {key: $actorKey})-[:mayRead*2..3]->(x:unit)\nRETURN x.key AS anchor"),
	}, nil), personalIndexBranchIncomplete,
		"a walk whose graph cannot answer must refuse the LENS: the derived set is a union, and a union with an unknown in it is a superset of nothing")

	require.Contains(t, pipeline.BranchDerivationRefusal([]ruleengine.CompiledRule{
		sound, parse("MATCH (org:org {key: $actorKey})-[:owns]->(x:unit)\nRETURN x.key AS anchor"),
	}, nil), personalIndexBranchAnchorDisagree,
		"walks anchoring on different labels must refuse — the checkable form of \"each branch carries its own anchor\"")

	// The taxonomy conjunct, and the EXPANSION PARAMETER that decides it. The
	// same branch set reads two ways: refused with no expansion (the reading a
	// pipeline whose resolver could not answer gets — pruning a far end the walk
	// cannot confirm under-approximates, so one such walk refuses the whole
	// lens), and admitted once the label resolves. A door that supplied nil for
	// itself would report the first for a lens the installer runs as the second,
	// so the pair is pinned rather than the parameter trusted.
	expansionWalks := []ruleengine.CompiledRule{
		sound, parse("MATCH (identity:identity {key: $actorKey})-[:manages]->(l:unit*)\nRETURN l.key AS anchor"),
	}
	require.Contains(t, pipeline.BranchDerivationRefusal(expansionWalks, nil), personalIndexBranchUnresolvedExp,
		"a walk carrying an unresolved `*` position must refuse the lens: the walk would prune far ends it cannot confirm")
	require.Empty(t, pipeline.BranchDerivationRefusal(expansionWalks, map[string]map[string]struct{}{
		"unit": {"unit": {}, "studio": {}},
	}), "the SAME walks must admit once the `*` resolves — otherwise threading the installer's expansion through this door changes nothing and the census is answering about a lens that does not run")

	require.Equal(t, personalIndexNoBranchIndex, pipeline.BranchDerivationRefusal(nil, nil),
		"no walks at all is a refusal, never a union over nothing")
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
