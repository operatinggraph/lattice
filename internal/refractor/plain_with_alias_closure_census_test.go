// Plain-lens WITH-alias closure census —
// with-alias-anchor-closure-design.md §2.1, §10.1.
//
// plain_scanroot_corpus_census_test.go pins, per plain lens, whether
// ScanRootHopIndex can answer "which anchors can a neighbour event affect" and
// — where that question is meaningful — whether per-anchor closure holds. This
// file asks the closure question of the WHOLE plain corpus, meaningful or not,
// and splits the answer by what the verdict RESTS ON: a key column that names
// the anchor's own binding outright, one that reaches it only by resolving
// through a WITH boundary's aliases, or one that genuinely binds something
// else.
//
// The split is what makes the alias resolution's reach measurable rather than
// argued. It calls the real predicates (HasAnchorOnlyKeyColumns,
// ProjectsOneRowPerAnchor) over the real installed corpus through
// forEachCorpusCypher, with each lens's declared Into.Key threaded exactly as
// activation threads it — never a re-implementation of the predicate and never
// a grep of cypher text, per docs/components/refractor.md's standing rule.
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: read the bucket that moved.
// A lens arriving in F or leaving B is a lens whose retraction, narrowing
// licence and divergence audit all changed answer at once — the three
// consumers share this one predicate.
package refractor_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The buckets, as the string each lens is pinned to below. They partition the
// plain corpus: every lens lands in exactly one, and a lens the classifier
// cannot place fails rather than defaulting into a neighbour.
const (
	// closureA — the key columns name the anchor's binding directly, and one of
	// them identifies it. The set the three consumers already acted on.
	closureA = "A closed and identifying"
	// closureA2 — closed, but no key column identifies the anchor, so the
	// write licence's own conjunct (ProjectsOneRowPerAnchor) still refuses.
	closureA2 = "A2 closed, not identifying"
	// closureB — refused, with no WITH boundary anywhere in the query: the
	// refusal is about the key columns alone and alias resolution is not part
	// of the answer either way.
	closureB = "B refused, no WITH involved"
	// closureF — the lens carries a WITH and is admitted anyway: its key
	// columns reach the anchor only through the alias resolution, so this
	// bucket IS the resolution's reach. Without it the key column names a
	// projected alias that no pattern binds.
	closureF = "F the WITH is the sole blocker"
	// closureG — the lens carries a WITH and is still refused: resolving the
	// aliases leaves a key column binding a variable that is not the anchor,
	// which is a genuine N-rows-per-anchor shape rather than a naming one.
	closureG = "G a key column binds a non-anchor variable"
)

// withAliasClosureBuckets pins every plain lens the installed corpus ships.
//
// The two memberships the design asserts and this table has to hold are F —
// exactly the three lenses whose retraction, licence and audit the alias
// resolution unblocks — and G, the two whose refusal it must NOT unblock,
// because their rows really do not partition by anchor.
var withAliasClosureBuckets = map[string]string{
	"applicantRosterRead":            closureA,
	"augurProposals":                 closureA,
	"availableListings":              closureA,
	"cafeIdentitiesRead":             closureA,
	"cafeLeaseAccounts":              closureA,
	"cafeLeaseWorkplaces":            closureA,
	"cafeLedgerHistory":              closureA,
	"capabilityAuthorContext":        closureA,
	"capabilityAuthorPackages":       closureA,
	"capabilityProposals":            closureA,
	"capabilityReadGrants":           closureA,
	"capabilityReadWildcardGrants":   closureA,
	"capabilityRoleIndex":            closureB,
	"clinicAppointments":             closureA,
	"clinicAppointmentsRead":         closureA,
	"clinicEncountersRead":           closureA,
	"clinicIdentitiesRead":           closureA,
	"clinicLedgerHistory":            closureA,
	"clinicPatientAccounts":          closureA,
	"clinicPatientReadGrants":        closureA,
	"clinicPatients":                 closureA,
	"clinicPatientsRead":             closureF,
	"clinicProviderReadGrants":       closureA,
	"clinicProviders":                closureA,
	"clinicSites":                    closureA,
	"consoleOperatorReadGrants":      closureA,
	"demoOperatorReadGrants":         closureA,
	"duplicateCandidates":            closureB,
	"frontDeskBookingHistory":        closureA,
	"frontDeskBookings":              closureA,
	"frontDeskLeaseDetails":          closureA,
	"frontDeskVisits":                closureA,
	"identityCredentialBindingsRead": closureA,
	"identityCredentialsRead":        closureA,
	"identityIndexHint":              closureA,
	"landlordLeaseApplicationsRead":  closureG,
	"landlordUnitsRead":              closureB,
	"leaseAccounts":                  closureA,
	"leaseApplicationsRead":          closureF,
	"ledgerHistory":                  closureA,
	"menuCatalog":                    closureA,
	"objectIdentityAttachmentsRead":  closureB,
	"oneBillCafeEntries":             closureA,
	"oneBillClinicEntries":           closureA,
	"oneBillRentEntries":             closureA,
	"oneBillWellnessEntries":         closureA,
	// Closed, and the only lens in the corpus that is closed WITHOUT
	// identifying: its key column is the anchor's own root `data.operationType`
	// rather than a key, so the retraction resolves while the write licence's
	// identifying conjunct does not.
	"opCatalog":                  closureA2,
	"patientIdentityReadGrants":  closureB,
	"piiKeyEnvelope":             closureA,
	"providerAppointmentsRead":   closureA,
	"providerIdentityReadGrants": closureB,
	"providerSites":              closureB,
	"renewalsRead":               closureF,
	"retentionKeyStatus":         closureA,
	"shredStatus":                closureA,
	"staffReadGrants":            closureB,
	"visitSeriesRead":            closureA,
	"wellnessBookings":           closureA,
	"wellnessIdentitiesRead":     closureA,
	"wellnessInstructors":        closureA,
	"wellnessLedgerHistory":      closureA,
	"wellnessMemberAccounts":     closureG,
	"wellnessMembers":            closureA,
	"wellnessSessions":           closureA,
	"wellnessStudios":            closureA,
}

// withAliasCensusFloor is the number of plain lenses that must reach the
// classifier. It guards the shape an emptied enumeration takes: a census that
// silently swept nothing would otherwise report every membership below as
// satisfied and read as "nothing moved".
const withAliasCensusFloor = 65

// carriesWithClause reports whether q has a WITH boundary at all — read off
// the compiled AST rather than the cypher text, so a lens whose spec is
// assembled from fragments is judged by what it actually compiled to.
func carriesWithClause(q *full.Query) bool {
	if q == nil {
		return false
	}
	for _, c := range q.Clauses {
		if _, isWith := c.(*full.With); isWith {
			return true
		}
	}
	return false
}

// classifyWithAliasClosure is the live classification for one plain lens.
//
// F is read as "carries a WITH and is admitted anyway", which is exactly "the
// alias resolution is what admits it": a key column of a WITH-bearing query
// names the projected alias, and an alias is not a pattern variable — only the
// resolution puts the anchor's own binding back under it. A lens whose key
// column happens to name a variable the WITH carries bare lands here too, and
// belongs here for the same reason: the boundary stands between its RETURN and
// its MATCH, and something has to prove the name still means what it says.
func classifyWithAliasClosure(t *testing.T, eng *full.Engine, name, spec string, rule *lens.Rule) string {
	t.Helper()
	cr, err := eng.Parse(spec)
	require.NoErrorf(t, err, "%s must parse", name)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.Truef(t, isFull, "%s must compile to the full engine", name)

	if cols := closureKeyColumns(fullCR, rule); cols != nil {
		require.NoErrorf(t, projection.ThreadKeyColumns(fullCR, nil, cols), "%s", name)
	} else {
		fullCR.KeyColumns = nil
	}

	closed := fullCR.HasAnchorOnlyKeyColumns()
	identifying := fullCR.ProjectsOneRowPerAnchor()
	withBoundary := carriesWithClause(fullCR.Query)

	switch {
	case closed && withBoundary:
		return closureF
	case closed && identifying:
		return closureA
	case closed:
		return closureA2
	case withBoundary:
		return closureG
	default:
		return closureB
	}
}

// TestPlainWithAliasClosureCensus classifies every plain lens the corpus ships
// and holds the result to the pinned table, in both directions: a lens whose
// bucket moved fails by name, and a lens with no row at all fails rather than
// being counted as unchanged.
func TestPlainWithAliasClosureCensus(t *testing.T) {
	eng := full.New()
	got := map[string]string{}

	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, declaredAggregate, declaredPersonal bool) {
		if declaredAggregate || declaredPersonal {
			return
		}
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		if fullCR.AnchorHopIndex().Incomplete != noAnchorPosition {
			return // anchored — this census's population is the plain corpus
		}
		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = classifyWithAliasClosure(t, eng, name, spec, rule)
	})

	require.GreaterOrEqualf(t, len(got), withAliasCensusFloor,
		"only %d plain lenses reached the classifier — the corpus enumeration or the plain filter moved, "+
			"and every membership below would read as satisfied on an emptied sweep", len(got))

	byBucket := map[string][]string{}
	for name, bucket := range got {
		byBucket[bucket] = append(byBucket[bucket], name)
	}
	for _, names := range byBucket {
		sort.Strings(names)
	}
	for _, bucket := range []string{closureA, closureA2, closureB, closureF, closureG} {
		t.Logf("%s: %d %v", bucket, len(byBucket[bucket]), byBucket[bucket])
	}
	t.Logf("TOTAL: %d", len(got))

	require.Equal(t, []string{"clinicPatientsRead", "leaseApplicationsRead", "renewalsRead"}, byBucket[closureF],
		"F is the set the alias resolution is responsible for — a lens arriving here gains a retraction, a "+
			"narrowing licence and an audit direction at once, and a lens leaving it loses all three")
	require.Equal(t, []string{"landlordLeaseApplicationsRead", "wellnessMemberAccounts"}, byBucket[closureG],
		"G is the set the resolution must NOT admit: both key on a variable that is not their anchor, so a "+
			"per-anchor evaluation would compute a truncated row")

	for name, want := range withAliasClosureBuckets {
		have, present := got[name]
		if !present {
			t.Errorf("pinned plain lens %q no longer reaches this census — remove its row if the lens was "+
				"retired, and review it if it gained a $actorKey position", name)
			continue
		}
		require.Equalf(t, want, have, "%s's closure bucket moved", name)
	}
	for name := range got {
		_, pinned := withAliasClosureBuckets[name]
		require.Truef(t, pinned,
			"plain lens %q ships with no pinned closure bucket (derived: %s) — review it, then record it",
			name, got[name])
	}
}
