// One-key-answer corpus census — auth-plane-projection-latency-design.md §18.1
// (second bullet), §18.5.
//
// anchor_hopindex_corpus_census_test.go pins whether the affected-anchor index
// can answer for a cypher at all. This file pins the question the enumerator
// asks on top of that answer: when a CDC event lands on a vertex of the lens's
// OWN actor type, may the pipeline reply with that one vertex, or must it walk?
//
// Reply-with-one is the narrowing, and it is the direction that needs an
// argument. An anchor's row is a function of the subgraph its pattern binds from
// that anchor, and `{key: $actorKey}` pins the anchor position to one vertex, so
// when the actor type binds at exactly ONE pattern position and that position IS
// the anchor, no other anchor's evaluation can bind the changed vertex. Where
// that does not hold — a second position binding the actor type, or an index
// that refused and therefore knows no positions — the one-key answer is a
// SUBSET of the affected anchors, and on the auth plane a subset is a stale
// grant or a missed retraction.
//
// So the two directions are asymmetric, exactly as the sibling censuses are. A
// lens moving to `walk` only costs reprojection work. A lens moving to `onekey`
// stops reprojecting anchors it used to, and if the pattern argument does not
// really hold for it, the rows it stops updating are grants that never retract.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: read the verdict that moved.
// `walk` → `onekey` is the one that needs you to satisfy yourself the actor type
// really binds nowhere but the anchor; the other direction only needs the new
// reason to be the true one.
//
// DERIVATION COMMAND (re-run this, do not trust a number in a build note):
//
//	go test ./internal/refractor/ -run TestCorpusActorOneKey -count=1 -v
package refractor_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgmgr"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The two verdicts, plus the reasons a `walk` carries. Reasons are matched by
// equality, not containment: this census's vocabulary is small and closed, and
// TestCorpusActorOneKey_EveryReasonIsKnown default-denies additions.
const (
	// oneKey: the actor type binds at exactly one position, the anchor. An
	// event on such a vertex reprojects it and nobody else.
	oneKey = "onekey"
	// walkIncompleteIndex: the index refused, so its position set is a floor
	// rather than the truth and the count licenses nothing.
	walkIncompleteIndex = "walk: index incomplete"
	// walkMultiPosition: the actor type binds somewhere besides the anchor, so
	// some other anchor's row really can render this vertex.
	walkMultiPosition = "walk: actor type binds off-anchor"
	// walkNoHealer: nothing would converge a row the narrowing stops
	// incidentally reprojecting. Checked FIRST, as the runtime checks it, and
	// it is a property of the lens CLASS rather than of its cypher — the
	// detail column still reports what the pattern half said.
	walkNoHealer = "walk: no standing healer"
)

// corpusActorOneKeyVerdicts pins the verdict for every anchored cypher the
// installed corpus ships.
//
// The `personal` lenses are in here on the same footing as the actorAggregate
// ones deliberately, and every one of them — eighteen cyphers, counting a
// multi-walk lens's branches separately — pins to walkNoHealer. That is not a
// property of their cyphers: projection/driver.go:417-421 records that a
// Personal Lens "simply never gets a plan", so nothing would converge a row the
// one-key answer stops incidentally reprojecting. Their pattern verdict is in
// the detail column, and for most of them it is pattern-eligible — which is
// exactly why the healer has to be the conjunct rather than an afterthought.
//
// ONE THING THIS CENSUS CANNOT SEE. sweepEnrolment (driver.go:426) can REFUSE an
// actor-aggregate lens its plan at install time, warn-only, on properties of its
// descriptor and adapter rather than of its cypher. Such a lens runs with a nil
// sweeper and takes the walk, while this table — which has only the cypher —
// reads it as `onekey`. So `onekey` here means "eligible on everything a cypher
// can decide", and the runtime withholds it from strictly more lenses, never
// fewer. That direction is the safe one.
var corpusActorOneKeyVerdicts = map[string]string{
	// (onbTask)-[:forOperation]->(onbOp:meta) is label-typed, so onbOp can
	// never bind the identity actor type; the identity anchor type binds only
	// at the anchor across the whole pattern.
	"applicantOnboarding":            oneKey,
	"appointmentReminders":           oneKey,
	"augurDispatchPending":           oneKey,
	"backgroundCheckFreshness":       oneKey,
	"cafeStaleTabSettlement":         oneKey,
	"cafeTabSettlement":              oneKey,
	"capability":                     oneKey,
	"capabilityAuthorPending":        oneKey,
	"capabilityEphemeral":            walkMultiPosition,
	"capabilityRead":                 oneKey,
	"capabilityServiceAccess":        walkIncompleteIndex,
	"clauseSatisfaction":             walkMultiPosition,
	"clinicNoShowSettlement":         oneKey,
	"clinicSiteBackfill":             oneKey,
	"capabilityRoles":                oneKey,
	"edgeCatalog#0":                  walkNoHealer,
	"edgeCatalog#1":                  walkNoHealer,
	"edgeCatalog#2":                  walkNoHealer,
	"edgeEntityBookings":             walkNoHealer,
	"edgeEntityMenuItems":            walkNoHealer,
	"edgeEntityProviders":            walkNoHealer,
	"edgeEntitySessions#0":           walkNoHealer,
	"edgeEntitySessions#1":           walkNoHealer,
	"edgeEntityStudios":              walkNoHealer,
	"edgeEntityTabs":                 walkNoHealer,
	"edgeIdentity":                   walkNoHealer,
	"edgeInstances":                  walkNoHealer,
	"edgeManifestProviderReadGrants": oneKey,
	"edgeManifestReadGrants":         walkIncompleteIndex,
	"edgeManifestStaffReadGrants":    walkIncompleteIndex,
	"edgeProviderQueue":              walkNoHealer,
	"edgeProviderSchedule":           walkNoHealer,
	"edgeServices":                   walkNoHealer,
	"edgeStaffPanes":                 walkNoHealer,
	"edgeStaffWorkOrders":            walkNoHealer,
	"edgeTasks#0":                    walkNoHealer,
	"edgeTasks#1":                    walkNoHealer,
	"followUpReminders":              oneKey,
	"identityAnchors":                walkMultiPosition,
	"identityErasureResidue":         walkMultiPosition,
	"leaseApplicationComplete":       oneKey,
	"leaseExpiry":                    oneKey,
	"leaseRentSettlement":            oneKey,
	"myTasks":                        walkMultiPosition,
	"objectAttachments":              walkIncompleteIndex,
	"objectLiveness":                 oneKey, // PositionsBinding("object") is exactly {Anchor}.
	"orphanedTaskGrants":             walkMultiPosition,
	"pastDueAppointments":            oneKey,
	"pastDueBookings":                oneKey,
	"renewalComplete":                oneKey,
	"staleAssignedTasks":             oneKey,
	// (t)-[:forOperation]->(op:meta) is label-typed, so op can never bind the
	// task actor type — every hop off t (identity/leaseapp/renewal/meta) now
	// excludes task, and the task anchor type binds only at the anchor.
	"staleUserTasks":                    oneKey,
	"unroutedTasks":                     oneKey,
	"visitSeriesDue":                    oneKey,
	"visitSeriesSiteBackfill":           oneKey,
	"wellnessBookingReminders":          oneKey,
	"wellnessClassPriceSettlement":      oneKey,
	"wellnessNoShowSettlement":          oneKey,
	"wellnessOrphanedBookingSettlement": oneKey,
	"wellnessRefundSettlement":          oneKey,
}

// corpusOneKeyLens is one anchored cypher plus the actor type the RUNTIME pairs
// its enumerator with — which is not always the anchor position's own label, and
// reading it off the cypher instead of off the installation would answer a
// different question than the pipeline asks.
type corpusOneKeyLens struct {
	name      string
	spec      string
	actorType string
	personal  bool
}

// forEachCorpusActorAwareCypher walks every installed lens that would hold an
// ActorEnumerator, pairing each with the actor type its own install path
// configures: projection/driver.go's InstallActorAggregate uses the §6.13 Output
// descriptor's AnchorType, and projection/personal.go's InstallPersonalLens
// always uses PersonalActorType ("identity"), whatever the cypher's anchor
// happens to be labelled.
func forEachCorpusActorAwareCypher(t *testing.T, visit func(corpusOneKeyLens)) {
	t.Helper()
	addLens := func(l pkgmgr.LensSpec) {
		actorType := ""
		switch {
		case l.Personal:
			actorType = "identity"
		case l.Output != nil:
			actorType = l.Output.AnchorType
		default:
			return
		}
		if actorType == "" {
			return
		}
		if len(l.SpecBranches) > 0 {
			for i, b := range l.SpecBranches {
				visit(corpusOneKeyLens{fmt.Sprintf("%s#%d", l.CanonicalName, i), b, actorType, l.Personal})
			}
			return
		}
		if l.Spec == "" {
			return
		}
		visit(corpusOneKeyLens{l.CanonicalName, l.Spec, actorType, l.Personal})
	}

	for _, name := range pkgregistry.Names() {
		def, ok := pkgregistry.Lookup(name)
		require.Truef(t, ok, "registered package %q must resolve", name)
		expanded, err := def.ExpandReadGrantWalks()
		require.NoErrorf(t, err, "%s read-grant walks must compose", name)
		for _, l := range expanded.Lenses {
			addLens(l)
		}
	}
	for _, l := range []bootstrap.LensDefinition{
		bootstrap.CapabilityLensDefinition(),
		bootstrap.CapabilityReadLensDefinition(),
		bootstrap.CapabilityReadGrantsLensDefinition(),
		bootstrap.CapabilityReadWildcardGrantsLensDefinition(),
	} {
		if l.Output == nil || l.Output.AnchorType == "" {
			continue
		}
		visit(corpusOneKeyLens{l.CanonicalName, l.CypherRule, l.Output.AnchorType, false})
	}
}

// corpusActorOneKeyDerivation asks pipeline.ActorTypeBindsAnchorOnly — the
// running predicate itself, not a restatement of it — for every installed
// actor-aware cypher, and attributes each `walk` to the conjunct that produced
// it. detail carries the position set behind a `walk`, which is what a reader
// checking a verdict actually needs.
func corpusActorOneKeyDerivation(t *testing.T) (verdicts, detail map[string]string, tally map[string]int) {
	t.Helper()
	eng := full.New()
	verdicts, detail, tally = map[string]string{}, map[string]string{}, map[string]int{}
	forEachCorpusActorAwareCypher(t, func(l corpusOneKeyLens) {
		cr, err := eng.Parse(l.spec)
		require.NoErrorf(t, err, "%s must parse", l.name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", l.name)
		ix := fullCR.AnchorHopIndex()

		// The pattern half, asked of the running predicate itself, plus its
		// attribution. Completeness is asked before the position count because
		// an incomplete index has no trustworthy set to report.
		patternOK := pipeline.ActorTypeBindsAnchorOnly(ix, l.actorType)
		patternWhy := ""
		switch {
		case patternOK:
			patternWhy = "pattern-eligible"
		case !ix.Complete || ix.UnresolvedExpansionPosition() >= 0:
			patternWhy = ix.Incomplete
		default:
			patternWhy = fmt.Sprintf("%s binds at %v, anchor=%d", l.actorType, ix.PositionsBinding(l.actorType), ix.Anchor)
		}

		// The healer half, in the order the runtime asks it (oneKeyAnswerSound
		// tests p.sweeper before the pattern). A personal lens never receives a
		// SweepPlan — SetSweepPlan has one non-test caller, inside
		// InstallActorAggregate — so its verdict is decided here whatever its
		// cypher says, and patternWhy records what that cypher would have
		// earned.
		var verdict string
		why := patternWhy
		switch {
		case l.personal:
			verdict = walkNoHealer
		case patternOK:
			verdict = oneKey
		case !ix.Complete || ix.UnresolvedExpansionPosition() >= 0:
			verdict = walkIncompleteIndex
		default:
			verdict = walkMultiPosition
		}
		_, dup := verdicts[l.name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", l.name)
		verdicts[l.name], detail[l.name] = verdict, why
		tally[verdict]++
		if l.personal {
			tally["personal"]++
		}
	})
	tally["total"] = len(verdicts)
	return verdicts, detail, tally
}

func TestCorpusActorOneKey_PinnedVerdicts(t *testing.T) {
	got, detail, tally := corpusActorOneKeyDerivation(t)

	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		t.Logf("%-40s %-36s %s", n, got[n], detail[n])
	}
	t.Logf("total=%d personal=%d %s=%d %s=%d %s=%d %s=%d",
		tally["total"], tally["personal"],
		oneKey, tally[oneKey],
		walkNoHealer, tally[walkNoHealer],
		walkIncompleteIndex, tally[walkIncompleteIndex],
		walkMultiPosition, tally[walkMultiPosition])

	for name, want := range corpusActorOneKeyVerdicts {
		have, present := got[name]
		if !present {
			t.Errorf("pinned lens %q is no longer actor-aware — remove its row if the lens was retired, "+
				"and review it if it merely lost its Output descriptor or its Personal flag", name)
			continue
		}
		require.Equalf(t, want, have,
			"%s's one-key verdict moved; a move TO %q means the pipeline stops reprojecting anchors it used to", name, oneKey)
	}
	for name := range got {
		_, pinned := corpusActorOneKeyVerdicts[name]
		require.Truef(t, pinned,
			"actor-aware lens %q ships with no pinned one-key verdict (derived: %s) — review it, then record it",
			name, got[name])
	}
}

// TestCorpusActorOneKey_EveryReasonIsKnown default-denies the verdict
// vocabulary: a conjunct added to the predicate without a constant here would
// land in the table as a string nobody pinned.
func TestCorpusActorOneKey_EveryReasonIsKnown(t *testing.T) {
	known := map[string]struct{}{
		oneKey:              {},
		walkNoHealer:        {},
		walkIncompleteIndex: {},
		walkMultiPosition:   {},
	}
	got, _, _ := corpusActorOneKeyDerivation(t)
	for name, verdict := range got {
		_, ok := known[verdict]
		require.Truef(t, ok, "%s derives %q, which matches no verdict constant in this file", name, verdict)
	}
}
