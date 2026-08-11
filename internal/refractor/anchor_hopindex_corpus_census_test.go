// Affected-anchor index corpus census — auth-plane-projection-latency-design.md
// §4.7, §16.2, §18.1.
//
// label_derivation_corpus_census_test.go pins what each shipped cypher's LABEL
// derivation earns, which is what bounds delivery. This file pins the other
// derivation over the same clause shapes: whether `AnchorHopIndex` can answer
// "which anchors does this event affect" for that cypher at all, and when it
// cannot, WHICH conjunct declined.
//
// It is pinned because the two directions are not symmetric. A cypher that
// stops being indexable only falls back to the shipped ActorEnumerator BFS —
// slower, never wrong. A cypher that STARTS being indexable is acted on: the
// pipeline reprojects the anchors the walk derives and no others, so a lens
// arriving here by accident (an edit that happens to satisfy every conjunct
// without anyone judging that it should) turns every anchor the walk misses
// into a row that stops updating. On the auth plane that is a grant outliving
// its revocation, and it is silent.
//
// Only the cyphers that PIN AN ANCHOR are pinned. A cypher with no
// `{key: $actorKey}` position is not a question this index is ever asked — the
// plain arm never consults it — and pinning the sixty of them would bury the
// rows that matter under rows that cannot move for an interesting reason.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: read the conjunct that moved,
// not the table. A row moving to `hopIndexed` is the direction that needs an
// argument — satisfy yourself the walk really reaches every anchor whose row
// that cypher's inputs can change — and a row moving off it only needs the new
// conjunct to be the true one.
package refractor_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The completeness predicate's conjuncts, as the substring an operator reads in
// `pipeline: anchor derivation cannot act on this lens` and in Health KV.
// Matched by containment so a reason may keep naming the specific variable or
// position it refused on.
const (
	// hopIndexed is the answer, not a refusal: the pattern graph is
	// authoritative and the pipeline seeds from it instead of enumerating.
	hopIndexed        = ""
	hopVarLengthHop   = "pattern carries a variable-length relationship"
	hopUntypedHop     = "pattern carries an untyped relationship"
	hopUngroundedSeed = "not reached from the anchor"
	hopMultiAnchor    = "several pattern positions bind $actorKey"
	hopExpandedAnchor = "taxonomy-expansion sigil"

	// The two WITH-scope conjuncts. hopWithDropped is the hazard itself — a
	// name a WITH let go of and a later clause still uses, which rebinds by
	// bucket scan. hopWithUnmodelled is everything about a WITH's projection
	// list the index declines to model rather than guess at.
	hopWithDropped    = "a WITH dropped"
	hopWithUnmodelled = "the WITH scope walk cannot model"

	// hopUnmodelledExpr is addExpr's default-deny arm. No shipped cypher
	// reaches it — every Expr the parser produces today is modelled — but the
	// vocabulary is named here so the arm's reason is a pinned conjunct on the
	// day some new AST node does reach it, rather than an unreadable row.
	hopUnmodelledExpr = "is not modelled by the hop index"
)

// corpusAnchorIndexVerdicts pins the conjunct for every anchored cypher the
// installed corpus ships.
//
// No row here is refused by either WITH conjunct, and that is the shipped
// corpus's own shape rather than a property of the predicate: a staging WITH
// exists to collapse an arm's fan-out back to one row per anchor before the
// next arm fans out (packages/privacy-base/lenses.go states the measurement),
// so it carries the anchor and lets the spent arm go, and no later clause names
// the arm again. The refusal cases are held by
// ruleengine/full's TestAnchorHopIndex_WithScope, which does not need a lens to
// be written badly in order to cover them.
var corpusAnchorIndexVerdicts = map[string]string{
	"appointmentReminders":              hopIndexed,
	"augurDispatchPending":              hopIndexed,
	"cafeStaleTabSettlement":            hopIndexed,
	"cafeTabSettlement":                 hopIndexed,
	"capability":                        hopIndexed,
	"capabilityAuthorPending":           hopIndexed,
	"capabilityEphemeral":               hopIndexed,
	"capabilityRead":                    hopIndexed,
	"capabilityRoles":                   hopIndexed,
	"capabilityServiceAccess":           hopVarLengthHop,
	"clauseSatisfaction":                hopIndexed,
	"clinicNoShowSettlement":            hopIndexed,
	"clinicSiteBackfill":                hopIndexed,
	"edgeCatalog#0":                     hopVarLengthHop,
	"edgeCatalog#1":                     hopIndexed,
	"edgeEntityBookings":                hopIndexed,
	"edgeEntityMenuItems":               hopVarLengthHop,
	"edgeEntityProviders":               hopVarLengthHop,
	"edgeEntitySessions#0":              hopVarLengthHop,
	"edgeEntitySessions#1":              hopIndexed,
	"edgeEntityStudios":                 hopVarLengthHop,
	"edgeEntityTabs":                    hopIndexed,
	"edgeIdentity":                      hopIndexed,
	"edgeInstances":                     hopIndexed,
	"edgeManifestProviderReadGrants":    hopIndexed,
	"edgeManifestReadGrants":            hopVarLengthHop,
	"edgeManifestStaffReadGrants":       hopVarLengthHop,
	"edgeProviderQueue":                 hopIndexed,
	"edgeProviderSchedule":              hopIndexed,
	"edgeServices":                      hopVarLengthHop,
	"edgeStaffPanes":                    hopIndexed,
	"edgeStaffWorkOrders":               hopVarLengthHop,
	"edgeTasks#0":                       hopIndexed,
	"edgeTasks#1":                       hopIndexed,
	"followUpReminders":                 hopIndexed,
	"identityAnchors":                   hopIndexed,
	"identityErasureResidue":            hopIndexed,
	"leaseApplicationComplete":          hopIndexed,
	"leaseExpiry":                       hopIndexed,
	"leaseRentSettlement":               hopIndexed,
	"myTasks":                           hopIndexed,
	"objectAttachments":                 hopUntypedHop,
	"objectLiveness":                    hopUntypedHop,
	"orphanedTaskGrants":                hopIndexed,
	"pastDueAppointments":               hopIndexed,
	"pastDueBookings":                   hopIndexed,
	"renewalComplete":                   hopIndexed,
	"unroutedTasks":                     hopIndexed,
	"visitSeriesDue":                    hopIndexed,
	"wellnessBookingReminders":          hopIndexed,
	"wellnessClassPriceSettlement":      hopIndexed,
	"wellnessNoShowSettlement":          hopIndexed,
	"wellnessOrphanedBookingSettlement": hopIndexed,
	"wellnessRefundSettlement":          hopIndexed,
}

// noAnchorPosition is the one verdict this census deliberately does not pin: a
// cypher with no `{key: $actorKey}` node never reaches the derivation. It is
// spelled out rather than matched loosely so a cypher that LOSES its anchor
// shows up as an unpinned-lens failure below rather than vanishing from the
// census.
const noAnchorPosition = "no pattern position binds $actorKey"

func corpusAnchorIndexDerivation(t *testing.T) map[string]string {
	t.Helper()
	eng := full.New()
	got := map[string]string{}
	forEachCorpusCypher(t, func(name, spec string) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		ix := fullCR.AnchorHopIndex()
		if ix.Incomplete == noAnchorPosition {
			return
		}
		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = ix.Incomplete
	})
	return got
}

func TestCorpusAnchorHopIndex_PinnedConjuncts(t *testing.T) {
	got := corpusAnchorIndexDerivation(t)

	for name, want := range corpusAnchorIndexVerdicts {
		have, present := got[name]
		if !present {
			t.Errorf("pinned lens %q no longer reaches the anchor derivation — "+
				"remove its row if the lens was retired, and review it if it merely lost its $actorKey position", name)
			continue
		}
		if want == hopIndexed {
			require.Emptyf(t, have,
				"%s stopped being indexable (%s) — that is a fallback to the BFS, not a defect, but the pin has to say so", name, have)
			continue
		}
		require.Containsf(t, have, want,
			"%s's declining conjunct moved; a move TO indexable means the pipeline now acts on this lens's derived anchor set", name)
	}
	for name := range got {
		_, pinned := corpusAnchorIndexVerdicts[name]
		require.Truef(t, pinned,
			"anchored lens %q ships with no pinned anchor-index conjunct (derived: complete=%v reason=%q) — "+
				"review the verdict, then record it in corpusAnchorIndexVerdicts",
			name, got[name] == "", got[name])
	}
}

// TestCorpusAnchorHopIndex_CompleteIndexHoldsEveryReferencedRelation is the
// mechanical half of this census, and it covers the EDIT path the pinned table
// above structurally cannot.
//
// That table is keyed by lens name and pins WHICH CONJUNCT declined. An edit to
// a lens that is already `hopIndexed` — staging a revocation filter behind a
// WITH, say — leaves `Incomplete` empty, so the pin does not move and the row
// stays green while the graph silently loses that relation's hops. The pin
// gates a lens ARRIVING at indexable; nothing gates one already there.
//
// The invariant that does: AnchorHopIndex and ReferencedRelations walk the same
// clauses (hopindex.go's header states the lockstep and names them), so
// wherever the relation set is exhaustive, every relation it names must sit on
// some hop. A relation in the set and absent from Hops means some clause the
// second derivation reads the first does not — and the consequence is not a
// slower answer but a wrong one: AnchorSideSeeds returns empty for that
// relation, and on a COMPLETE index anchor_derivation reads empty as "no anchor
// can change" and skips the reprojection.
//
// A non-exhaustive relation set is skipped rather than failed: it means an
// untyped or variable-length hop, and both refuse the index on their own
// conjunct.
func TestCorpusAnchorHopIndex_CompleteIndexHoldsEveryReferencedRelation(t *testing.T) {
	eng := full.New()
	checked := 0
	forEachCorpusCypher(t, func(name, spec string) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		ix := fullCR.AnchorHopIndex()
		if !ix.Complete {
			return
		}
		rels, exhaustive := fullCR.ReferencedRelations()
		if !exhaustive {
			return
		}
		indexed := map[string]struct{}{}
		for _, h := range ix.Hops {
			indexed[h.Rel] = struct{}{}
		}
		for rel := range rels {
			require.Containsf(t, indexed, rel,
				"%s indexes completely but carries no hop for `%s`, which its own pattern reads — "+
					"a %s event would derive no anchor and be SKIPPED, not fall back to the enumerator", name, rel, rel)
		}
		checked++
	})
	// The gate is worthless if the corpus stops reaching it: "0 lenses checked"
	// reads identically to "every lens passed".
	require.Greaterf(t, checked, 20,
		"only %d indexable cyphers reached this gate — the corpus enumeration or the derivation moved", checked)
}

// TestCorpusAnchorHopIndex_EveryReasonIsAKnownConjunct default-denies the
// vocabulary above. A conjunct added to the predicate without a constant here
// would otherwise land in the table as whichever existing substring happened to
// match — or, worse, as a row nobody could read — and the census would keep
// reporting green while the reason an operator sees in the log means something
// nobody pinned.
func TestCorpusAnchorHopIndex_EveryReasonIsAKnownConjunct(t *testing.T) {
	known := []string{
		hopVarLengthHop,
		hopUntypedHop,
		hopUngroundedSeed,
		hopMultiAnchor,
		hopExpandedAnchor,
		hopWithDropped,
		hopWithUnmodelled,
		hopUnmodelledExpr,
	}
	for name, reason := range corpusAnchorIndexDerivation(t) {
		if reason == "" {
			continue
		}
		matched := false
		for _, k := range known {
			if strings.Contains(reason, k) {
				matched = true
				break
			}
		}
		require.Truef(t, matched,
			"%s declines with %q, which matches no conjunct constant in this file — name it before pinning it", name, reason)
	}
}
