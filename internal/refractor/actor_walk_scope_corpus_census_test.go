// Actor-walk relation-scope corpus census —
// refractor-hub-walk-and-periodic-load-design.md §5.1.
//
// actor_onekey_corpus_census_test.go pins WHETHER an actor-type event may be
// answered with one key. This file pins what the walk does when it does run:
// standing on a vertex of type T, which adjacency relations does the lens's own
// compiled pattern entitle it to follow?
//
// The scope is the narrowing, and it is the direction that needs an argument.
// The claim is the pattern's: an actor A whose row depends on event vertex V is
// bound to V by a path of pattern hops through positions that admit each
// intermediate's type, so a walk following only those relations still reaches A.
// Where the pattern graph cannot be read — an untyped relationship, an
// ungrounded head, a `*` whose expansion did not resolve — no relation set
// describes what the walk may cross, and the lens keeps the relation-blind walk
// it has always had.
//
// The second conjunct is §4.2's standing healer, and it is a property of the
// lens's INSTALL rather than of its cypher, so this census supplies the install
// production supplies: an actorAggregate lens gets whatever SweepPlan
// projection.InstallActorAggregate's own enrolment gate installs for it, and a
// Personal lens gets the personal-plane healer flag cmd/refractor sets at its
// grantReprojector.RegisterPersonal call. The `healer` column records which arm
// each lens landed on, because that gate can refuse warn-only: a lens that
// silently loses its sweep enrolment loses the scope with it, and this column is
// where that shows. No corpus lens is currently unhealed, so the census cannot
// itself demonstrate the refusal — the pipeline package's
// TestWalkScope_NoStandingHealerRefusesTheScope family is that vector.
//
// So the two directions are asymmetric, exactly as the sibling censuses are.
// A lens moving to `nil` only costs reads. A lens moving from `nil` to a scope,
// or losing a relation from one it has, stops crossing edges it used to — and
// if the §5.1 argument does not really hold for it, the anchors it stops
// reaching are grants that never retract.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: read the digest that moved. A
// relation LEAVING a type's set, or a `nil` becoming a scope, is the one that
// needs you to satisfy yourself the pattern really binds every path the walk has
// to run in reverse; the other direction only needs the new refusal to be true.
//
// ONE LIMIT ON WHAT THIS PINS, structural to a per-cypher census: the rows are
// per EXECUTABLE CYPHER, so a multi-walk lens's `name#N` rows each pin that
// BRANCH's scope. The runtime scope of such a lens is the union over its
// branches, with one unreadable branch refusing the whole scope
// (pipeline.deriveWalkScope) — TestWalkScope_MultiWalkUnionsEveryBranch and
// TestWalkScope_OneUnreadableBranchRefusesTheWholeScope in the pipeline package
// pin that composition directly.
//
// DERIVATION COMMAND (re-run this, do not trust a number in a build note):
//
//	go test ./internal/refractor/ -run TestCorpusActorWalkScope -count=1 -v
package refractor_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// scopeNil is the digest of a lens the derivation refused: the relation-blind
// walk, unchanged. The reason is pinned separately, in the detail column and
// against pipeline's own closed refusal vocabulary.
const scopeNil = "nil"

// refusalNoHealer is pipeline's §4.2 refusal constant, spelled here because the
// census sits outside that package. Matched by equality like every other
// refusal in the pinned table below.
const refusalNoHealer = "no standing healer"

// corpusWalkScopeMinimum is the population floor. A census that silently swept
// no lens would pass every equality below, so the count is asserted on its own —
// and the exact pinned set is asserted in both directions besides.
const corpusWalkScopeMinimum = 55

// corpusActorWalkScopeDigests pins the relation scope every actor-aware cypher
// the installed corpus ships derives.
//
// The digest is `type:rel,rel|type:rel|any:rel` — the per-type relation sets
// sorted by type, then the relations followable at EVERY type (an unlabeled
// pattern position, or a variable-length hop, whose expansion stands on
// unlabeled intermediates). A type whose entry is `*` follows every relation,
// which an untyped hop at a labelled position would produce and which no
// shipped cypher reaches: AnchorHopIndex refuses an untyped relationship
// outright, so such a lens lands on `nil` instead.
//
// `none` AND `nil` ARE OPPOSITE ANSWERS, and objectLiveness is the row that
// makes the difference load-bearing. `nil` is a REFUSED scope — the walk stays
// relation-blind and crosses everything. `none` is a DERIVED and empty one: a
// scope that follows no relation from any type, because no pattern path exists
// at all, so following nothing still reaches every anchor the pattern can
// reach. A row reading `none` is the scope's own soundness argument at k = 0,
// not a refusal by another name.
//
// THE `service:…instanceOf…` ENTRIES ARE THE KNOWN RESIDUAL. Four rows below —
// capabilityServiceAccess, edgeInstances, edgeManifestProviderReadGrants and
// edgeProviderQueue — name `instanceOf` at the `service` type, because their own
// patterns bind that hop between two `service` positions. The scope is keyed by
// TYPE, so it cannot tell "instance → its template" from "template → its other
// instances", and those lenses still expand the type descriptor. That is an
// over-approximation of the pattern rather than a defect in it: only a
// position-directed walk removes it, which is what the affected-anchor
// derivation already is. Every OTHER lens's absence of an `instanceOf` entry is
// the descriptor hop being pruned, which is what this scope buys.
var corpusActorWalkScopeDigests = map[string]string{
	"applicantOnboarding":               "identity:applicationFor,scopedTo|leaseapp:applicationFor,appliesToUnit|meta:forOperation|task:forOperation,scopedTo|unit:appliesToUnit",
	"appointmentReminders":              "appointment:forPatient,withProvider|patient:forPatient|provider:withProvider",
	"augurDispatchPending":              "none",
	"backgroundCheckFreshness":          "none",
	"cafeStaleTabSettlement":            "none",
	"cafeTabSettlement":                 "cafetransaction:settles|leaseapp:chargedTo,openFor|tab:chargedTo,openFor,settles",
	"capability":                        "identity:holdsRole|role:holdsRole",
	"capabilityAuthorPending":           "none",
	"capabilityEphemeral":               "identity:assignedTo,holdsRole,reportsTo|role:holdsRole,queuedFor|task:assignedTo,forOperation,queuedFor,scopedTo|any:forOperation,scopedTo",
	"capabilityRead":                    "none",
	"capabilityRoles":                   "identity:holdsRole|permission:grantedBy|role:grantedBy,holdsRole",
	"capabilityServiceAccess":           "building:availableAt,containedIn,residesIn|identity:residesIn|property:availableAt,containedIn,residesIn|service:availableAt,instanceOf,permitsOperation,unavailableAt|unit:availableAt,containedIn,residesIn|any:containedIn,permitsOperation,unavailableAt",
	"clauseSatisfaction":                "account:chargesTo|clause:authorizedBy,chargesTo,conditionedOn,requiresInspectionBy|identity:requiresInspectionBy|transaction:authorizedBy|any:conditionedOn",
	"clinicNoShowSettlement":            "appointment:forPatient,settles|clinicaccount:heldFor|clinictransaction:reverses,settles|patient:forPatient,heldFor",
	"clinicSiteBackfill":                "appointment:atSite|building:atSite",
	"edgeCatalog#0":                     "identity:residesIn|meta:permitsOperation|service:availableAt,permitsOperation|any:availableAt,containedIn,residesIn",
	"edgeCatalog#1":                     "identity:holdsRole|meta:forOperation,permitsOperation|permission:forOperation,grantedBy|role:grantedBy,holdsRole|service:permitsOperation",
	"edgeCatalog#2":                     "identity:assignedTo|meta:forOperation,permitsOperation|service:permitsOperation|task:assignedTo,forOperation",
	"edgeEntityBookings":                "booking:bookedBy,forSession|identity:bookedBy|instructor:ledBy|session:atStudio,forSession,ledBy|studio:atStudio",
	"edgeEntityMenuItems":               "identity:residesIn|menuitem:servedAt|any:containedIn,residesIn,servedAt",
	"edgeEntityProviders":               "identity:residesIn|provider:practicesAt|any:containedIn,practicesAt,residesIn",
	"edgeEntitySessions#0":              "identity:residesIn|instructor:ledBy|session:atStudio,ledBy|studio:atStudio,locatedAt|any:containedIn,locatedAt,residesIn",
	"edgeEntitySessions#1":              "identity:identifiedBy|instructor:identifiedBy,ledBy|session:atStudio,ledBy|studio:atStudio",
	"edgeEntityStudios":                 "identity:worksAt|studio:locatedAt|any:containedIn,locatedAt,worksAt",
	"edgeEntityTabs":                    "identity:applicationFor|leaseapp:applicationFor,appliesToUnit,openFor|tab:openFor|unit:appliesToUnit",
	"edgeIdentity":                      "identity:applicationFor,holdsRole,identifiedBy,residesIn,worksAt|instructor:identifiedBy|leaseapp:applicationFor|patient:identifiedBy|provider:identifiedBy|role:holdsRole|serviceprovider:identifiedBy|any:containedIn,residesIn,worksAt",
	"edgeInstances":                     "identity:providedTo|service:instanceOf,providedTo",
	"edgeManifestProviderReadGrants":    "appointment:withProvider|identity:identifiedBy|instructor:identifiedBy,ledBy|provider:identifiedBy,withProvider|service:instanceOf,providedBy|serviceprovider:identifiedBy,providedBy|session:ledBy",
	"edgeManifestReadGrants":            scopeNil,
	"edgeManifestStaffReadGrants":       scopeNil,
	"edgeProviderQueue":                 "identity:identifiedBy|service:instanceOf,providedBy|serviceprovider:identifiedBy,providedBy",
	"edgeProviderSchedule":              "appointment:withProvider|identity:identifiedBy|provider:identifiedBy,withProvider",
	"edgeServices":                      "identity:residesIn|service:availableAt,providedBy|any:availableAt,containedIn,providedBy,residesIn",
	"edgeStaffPanes":                    "identity:holdsRole|meta:offeredTo|role:holdsRole,offeredTo",
	"edgeStaffWorkOrders":               "identity:worksAt|workorder:locatedAt|any:containedIn,locatedAt,worksAt",
	"edgeTasks#0":                       "identity:assignedTo|task:assignedTo,forOperation,scopedTo|unit:appliesToUnit|any:appliesToUnit,forOperation,scopedTo",
	"edgeTasks#1":                       "identity:assignedTo,holdsRole|role:holdsRole,queuedFor|task:assignedTo,forOperation,queuedFor,scopedTo|unit:appliesToUnit|any:appliesToUnit,forOperation,scopedTo",
	"followUpReminders":                 "appointment:forPatient,withProvider|patient:forPatient|provider:withProvider",
	"identityAnchors":                   "identity:identifiedBy,manages,residesIn,worksAt|any:containedIn,identifiedBy,manages,residesIn,worksAt",
	"identityErasureResidue":            "identity:boundTo,duplicateOf,indexes|any:boundTo,duplicateOf,indexes",
	"leaseApplicationComplete":          "identity:applicationFor,manages,providedTo,scopedTo|leaseapp:applicationFor,appliesToUnit,providedTo,scopedTo,signedLease|meta:forOperation|object:signedLease|service:providedTo|task:forOperation,scopedTo|unit:appliesToUnit,manages",
	"leaseExpiry":                       "identity:manages|leaseapp:appliesToUnit,renews|renewal:renews|unit:appliesToUnit,manages",
	"leaseRentSettlement":               "clause:governs|leaseapp:governs",
	"myTasks":                           "identity:assignedTo,holdsRole|role:holdsRole,queuedFor|task:assignedTo,forOperation,queuedFor,scopedTo|any:forOperation,scopedTo",
	"objectAttachments":                 scopeNil,
	"objectLiveness":                    "none", // Derived-and-empty, not refused; see above.
	"orphanedTaskGrants":                "task:forOperation|any:forOperation",
	"pastDueAppointments":               "appointment:forPatient,withProvider|patient:forPatient|provider:withProvider",
	"pastDueBookings":                   "booking:bookedBy,forSession|identity:bookedBy|session:forSession",
	"renewalComplete":                   "identity:applicationFor,manages,providedTo|leaseapp:applicationFor,appliesToUnit,renews|renewal:renews|service:providedTo|unit:appliesToUnit,manages",
	"staleAssignedTasks":                "identity:assignedTo|task:assignedTo",
	"staleUserTasks":                    "identity:scopedTo|leaseapp:scopedTo|meta:forOperation|renewal:scopedTo|task:forOperation,scopedTo",
	"unroutedTasks":                     "role:queuedFor|task:queuedFor",
	"visitSeriesDue":                    "patient:forPatient|provider:withProvider|visitseries:forPatient,withProvider",
	"visitSeriesSiteBackfill":           "building:atSite|provider:withProvider|visitseries:atSite,withProvider",
	"wellnessBookingReminders":          "booking:bookedBy,forSession|identity:bookedBy|session:forSession",
	"wellnessClassPriceSettlement":      "booking:bookedBy,forSession,settlesClassPrice|identity:bookedBy,heldFor|session:forSession|wellnessaccount:heldFor|wellnesstransaction:settlesClassPrice",
	"wellnessNoShowSettlement":          "booking:bookedBy,settles|identity:bookedBy,heldFor|wellnessaccount:heldFor|wellnesstransaction:settles",
	"wellnessOrphanedBookingSettlement": "booking:forSession|session:forSession",
	"wellnessRefundSettlement":          "wellnessrefund:settlesRefund|wellnesstransaction:settlesRefund",
}

// corpusActorWalkScopeRefusals pins WHY each `nil` lens is relation-blind.
//
// The strings are pipeline's own published refusal constants, read off the
// running pipeline (WalkScopeRefusal) rather than re-derived here, and matched
// by EQUALITY: the vocabulary is small and closed, and
// TestCorpusActorWalkScope_EveryRefusalIsKnown default-denies an addition, so a
// conjunct added to the derivation without a constant lands here as a string
// nobody reviewed.
var corpusActorWalkScopeRefusals = map[string]string{
	"edgeManifestReadGrants":      "a branch's pattern graph is incomplete",
	"edgeManifestStaffReadGrants": "a branch's pattern graph is incomplete",
	"objectAttachments":           "a branch's pattern graph is incomplete",
}

// corpusWalkScopeDerivation installs every actor-aware cypher the corpus ships
// the way production installs it, and reads the scope off the RUNNING pipeline.
//
// It goes through forEachCorpusCypher — the one enumeration every census in this
// package agrees on — rather than sweeping the registry its own way, and through
// corpusInstalledPipeline, so the scope it reports is the scope the deployment
// would run.
//
// detail is the pattern graph's OWN account of a refusal, logged beside the
// pinned constant. It is not pinned — the vocabulary a census default-denies has
// to be closed, and HopIndex.Incomplete is free text — but it is what a reader
// checking a `nil` verdict actually needs.
func corpusWalkScopeDerivation(t *testing.T) (digests, refusals, detail, healers map[string]string) {
	t.Helper()
	eng := full.New()
	digests, refusals, detail, healers = map[string]string{}, map[string]string{}, map[string]string{}, map[string]string{}
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, declaredActorAggregate, declaredPersonal bool) {
		if !declaredActorAggregate && !declaredPersonal {
			// A plain lens holds no ActorEnumerator, so it has no walk for a
			// scope to bound. Its rule state still carries one; nothing reads it.
			return
		}
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		rule.CompiledRule = cr
		p := corpusInstalledPipeline(t, name, eng, cr, rule)

		byType, anyType, scoped := p.WalkScope()
		_, dup := digests[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		healers[name] = installedHealer(declaredPersonal, p.Sweeper() != nil)
		if !scoped {
			digests[name] = scopeNil
			refusals[name] = p.WalkScopeRefusal()
			fullCR, isFull := cr.(*full.CompiledRule)
			require.Truef(t, isFull, "%s must compile to the full engine", name)
			detail[name] = fullCR.AnchorHopIndex().Incomplete
			return
		}
		digests[name] = walkScopeDigest(byType, anyType)
		refusals[name] = ""
	})
	return digests, refusals, detail, healers
}

// The healer arms, one per plane, plus the absence that refuses the scope.
const (
	healerSweep    = "sweep"
	healerPersonal = "personal"
	healerNone     = "none"
)

// installedHealer names which §4.2 arm the install left standing. A Personal
// lens is judged on its host registration and an actorAggregate lens on its own
// sweep enrolment, which is exactly the split pipeline.standingHealerInstalled
// reads.
func installedHealer(personal, hasSweepPlan bool) string {
	switch {
	case personal:
		return healerPersonal
	case hasSweepPlan:
		return healerSweep
	default:
		return healerNone
	}
}

// walkScopeDigest renders one scope as the single sorted string the table pins.
func walkScopeDigest(byType map[string][]string, anyType []string) string {
	types := make([]string, 0, len(byType))
	for vt := range byType {
		types = append(types, vt)
	}
	sort.Strings(types)
	parts := make([]string, 0, len(types)+1)
	for _, vt := range types {
		parts = append(parts, vt+":"+strings.Join(byType[vt], ","))
	}
	if len(anyType) > 0 {
		parts = append(parts, "any:"+strings.Join(anyType, ","))
	}
	if len(parts) == 0 {
		// A complete pattern graph with no hops at all: the anchor's own vertex
		// and nothing else, so the walk crosses nothing from any type.
		return "none"
	}
	return strings.Join(parts, "|")
}

func TestCorpusActorWalkScope_PinnedDigests(t *testing.T) {
	got, refusals, detail, healers := corpusWalkScopeDerivation(t)

	names := make([]string, 0, len(got))
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	scopedCount := 0
	for _, n := range names {
		if got[n] != scopeNil {
			scopedCount++
		}
		t.Logf("%-40s %-8s %s", n, healers[n], digestLine(got[n], refusals[n], detail[n]))
	}
	t.Logf("total=%d scoped=%d nil=%d", len(got), scopedCount, len(got)-scopedCount)

	require.GreaterOrEqualf(t, len(got), corpusWalkScopeMinimum,
		"the census swept %d actor-aware cyphers, below the pinned floor of %d — a sweep that covers nothing agrees with every row below",
		len(got), corpusWalkScopeMinimum)

	for name, want := range corpusActorWalkScopeDigests {
		have, present := got[name]
		if !present {
			t.Errorf("pinned lens %q is no longer actor-aware — remove its row if the lens was retired, "+
				"and review it if it merely lost its Output descriptor or its Personal flag", name)
			continue
		}
		require.Equalf(t, want, have,
			"%s's walk scope moved; a relation LEAVING it stops the walk crossing edges it used to", name)
	}
	for name := range got {
		_, pinned := corpusActorWalkScopeDigests[name]
		require.Truef(t, pinned,
			"actor-aware lens %q ships with no pinned walk scope (derived: %s) — review it, then record it",
			name, got[name])
	}

	// The §4.2 conjunct, stated as an invariant over the whole corpus rather
	// than pinned per lens: an unhealed lens is exactly a relation-blind one.
	// It fails in both directions — a lens that lost its sweep enrolment while
	// keeping a scope, and one that reports the healer refusal while an install
	// really did give it a healer.
	for name, healer := range healers {
		if healer == healerNone {
			require.Equalf(t, scopeNil, got[name],
				"%s has no standing healer, so it must keep the relation-blind walk", name)
			require.Equalf(t, refusalNoHealer, refusals[name],
				"%s is unhealed and must say so", name)
			continue
		}
		require.NotEqualf(t, refusalNoHealer, refusals[name],
			"%s installs the %s healer, so the scope must not refuse on §4.2", name, healer)
	}
}

// TestCorpusActorWalkScope_EveryRefusalIsKnown default-denies the refusal
// vocabulary: a conjunct added to the derivation without a published constant
// would land in the table as a string nobody reviewed. It asks the pipeline for
// each reason rather than re-deriving one, so the two cannot drift.
func TestCorpusActorWalkScope_EveryRefusalIsKnown(t *testing.T) {
	got, refusals, _, _ := corpusWalkScopeDerivation(t)
	for name, digest := range got {
		if digest != scopeNil {
			require.Emptyf(t, refusals[name], "%s derived a scope and must carry no refusal", name)
			continue
		}
		want, pinned := corpusActorWalkScopeRefusals[name]
		require.Truef(t, pinned, "%s is relation-blind with no pinned reason (%q)", name, refusals[name])
		require.Equalf(t, want, refusals[name],
			"%s's refusal moved — the new reason must be the true one", name)
	}
}

func digestLine(digest, refusal, detail string) string {
	if refusal == "" {
		return digest
	}
	return fmt.Sprintf("%-4s %s (%s)", digest, refusal, detail)
}
