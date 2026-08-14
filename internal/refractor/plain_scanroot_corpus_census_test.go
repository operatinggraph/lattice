// Plain-lens scan-root corpus census —
// plain-lens-neighbour-anchor-derivation-design.md §2, §11.
//
// anchor_hopindex_corpus_census_test.go pins, for every ANCHORED cypher, which
// conjunct of AnchorHopIndex's completeness predicate it satisfies or
// declines. This file pins the mirror question for every PLAIN cypher (no
// `{key: $actorKey}` position at all): whether ScanRootHopIndex can answer
// "which anchors can a neighbour event on this lens affect", and — because
// completeness alone is not a write licence (§5.1) — whether
// AnchorProjectionKey's per-anchor closure conjunct holds too, for the lenses
// where completeness makes that question meaningful.
//
// It is the executable form of the design's own §2 census: that census was
// run by a scratch test not kept in the tree, so its numbers (45 exposed of
// 60 plain, 9 variable-length, ≤36 addressable, a 21/12/2/1 distance profile)
// were a snapshot, not a re-derivable fact. This file keeps the measurement
// live instead: TestScanRootCorpusCensus_Partition logs today's actual
// figures rather than asserting the design doc's, per §2's own posture — "a
// refusal that turns out to dominate becomes a filed, grounded follow-on
// rather than a guess made now."
//
// WHEN THIS FAILS ON A LENS YOU ADDED OR EDITED: read the conjunct (or the
// closure verdict) that moved, not the table. A plain lens's row moving
// toward "indexed and closed" is the direction that needs an argument — a
// future increment would start ACTING on that lens's derived anchor set —
// and a row moving away from it only needs the new conjunct to be the true
// one.
package refractor_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// The ScanRootHopIndex completeness predicate's conjuncts, as the substring
// an operator reads in Incomplete — the same default-deny discipline
// anchor_hopindex_corpus_census_test.go's hop* constants keep, so a conjunct
// added to the predicate without a name here fails
// TestScanRootCorpusCensus_EveryReasonIsAKnownConjunct rather than landing as
// an unreadable row.
const (
	// rootIndexed is the answer, not a refusal: ScanRootHopIndex is complete
	// and the pattern graph is authoritative for this lens.
	rootIndexed = ""

	rootNoLabel        = "the anchor pattern position carries no label"
	rootKeyPinned      = "the anchor pattern is pinned by its own key"
	rootExpandedAnchor = "taxonomy-expansion sigil"
	rootUntypedHop     = "pattern carries an untyped relationship"
	rootVarLengthHop   = "pattern carries a variable-length relationship"
	rootWithDropped    = "a WITH dropped"
	rootWithUnmodelled = "the WITH scope walk cannot model"
	rootUngrounded     = "not reached from the anchor"
)

var scanRootConjuncts = []string{
	rootNoLabel, rootKeyPinned, rootExpandedAnchor, rootUntypedHop,
	rootVarLengthHop, rootWithDropped, rootWithUnmodelled, rootUngrounded,
}

// The closure verdict — AnchorProjectionKey's ok contract (§5.1) — for a lens
// where ScanRootHopIndex is complete AND has a neighbour endpoint at all
// (a single-node lens is trivially complete but has nothing this design's
// mechanism ever derives from, so its closure is not a meaningful question).
const (
	closureNA      = ""                // not asked: no neighbour endpoint, or ScanRootHopIndex declined
	closureHolds   = "closure holds"   // AnchorProjectionKey ok == true
	closureRefused = "closure refused" // AnchorProjectionKey ok == false
)

// plainScanRootVerdict is one plain lens's pinned classification.
type plainScanRootVerdict struct {
	// hasNeighbour is false for a single-node lens (no relation referenced
	// anywhere in the cypher) — no neighbour endpoint exists for this
	// design's mechanism to ever be asked about.
	hasNeighbour bool
	// reason is one of the root* conjunct constants above, or rootIndexed
	// ("") when ScanRootHopIndex is Complete.
	reason string
	// closure is closureHolds/closureRefused when reason == rootIndexed &&
	// hasNeighbour (the only case the question is meaningful), else
	// closureNA.
	closure string
}

// scanRootCorpusVerdicts pins today's verdict for every plain lens the
// installed corpus ships — 60 of them, matching corpusAnchorIndexVerdicts'
// own 54-anchored / 60-plain split (anchor_hopindex_corpus_census_test.go
// §2's "confirmed" row). Measured live, not copied from the design doc's own
// prose numbers: TestScanRootCorpusCensus_PinnedVerdicts is what keeps this
// table honest against the shipped corpus, the same way
// TestCorpusAnchorHopIndex_PinnedConjuncts keeps the anchored table honest.
var scanRootCorpusVerdicts = map[string]plainScanRootVerdict{
	"applicantRosterRead":            {hasNeighbour: true, reason: rootVarLengthHop},
	"augurProposals":                 {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"availableListings":              {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"cafeIdentitiesRead":             {hasNeighbour: true, reason: rootVarLengthHop},
	"cafeLeaseAccounts":              {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"cafeLeaseWorkplaces":            {hasNeighbour: true, reason: rootVarLengthHop},
	"cafeLedgerHistory":              {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"capabilityAuthorContext":        {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"capabilityProposals":            {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"capabilityReadGrants":           {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"capabilityReadWildcardGrants":   {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"capabilityRoleIndex":            {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"clinicAppointments":             {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"clinicAppointmentsRead":         {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"clinicEncountersRead":           {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"clinicLedgerHistory":            {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"clinicPatientAccounts":          {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"clinicPatientReadGrants":        {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"clinicPatients":                 {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"clinicPatientsRead":             {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"clinicProviderReadGrants":       {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"clinicProviders":                {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"clinicSites":                    {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"consoleOperatorReadGrants":      {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"demoOperatorReadGrants":         {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"duplicateCandidates":            {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"frontDeskBookings":              {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"frontDeskLeaseDetails":          {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"frontDeskVisits":                {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"identityCredentialBindingsRead": {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"identityCredentialsRead":        {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"identityIndexHint":              {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"landlordLeaseApplicationsRead":  {hasNeighbour: true, reason: rootVarLengthHop},
	"landlordUnitsRead":              {hasNeighbour: true, reason: rootVarLengthHop},
	"leaseAccounts":                  {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"leaseApplicationsRead":          {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"ledgerHistory":                  {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"menuCatalog":                    {hasNeighbour: true, reason: rootVarLengthHop},
	"oneBillCafeEntries":             {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"oneBillClinicEntries":           {hasNeighbour: true, reason: rootUngrounded},
	"oneBillRentEntries":             {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"oneBillWellnessEntries":         {hasNeighbour: true, reason: rootUngrounded},
	"patientIdentityReadGrants":      {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"piiKeyEnvelope":                 {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"providerAppointmentsRead":       {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"providerIdentityReadGrants":     {hasNeighbour: true, reason: rootUngrounded},
	"providerSites":                  {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"renewalsRead":                   {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"retentionKeyStatus":             {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"shredStatus":                    {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
	"staffReadGrants":                {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"visitSeriesRead":                {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"wellnessBookings":               {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"wellnessIdentitiesRead":         {hasNeighbour: true, reason: rootVarLengthHop},
	"wellnessInstructors":            {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"wellnessLedgerHistory":          {hasNeighbour: true, reason: rootIndexed, closure: closureHolds},
	"wellnessMemberAccounts":         {hasNeighbour: true, reason: rootIndexed, closure: closureRefused},
	"wellnessMembers":                {hasNeighbour: true, reason: rootVarLengthHop},
	"wellnessSessions":               {hasNeighbour: true, reason: rootVarLengthHop},
	"wellnessStudios":                {hasNeighbour: false, reason: rootIndexed, closure: closureNA},
}

// threadsForClosure mirrors cmd/refractor/main.go's threadsKeyColumns /
// isOperationRoleIndexLens: whether rule.Into.Key may be threaded onto a
// compiled rule as RETURN-alias-validated key columns at all. An
// operation-role-index lens (Into.Key == ["operationType"], auth-plane)
// derives its projection key from the envelope at write time, so its
// declared key is deliberately not a RETURN alias — threading it would fail
// a spec that is entirely correct. Every OTHER plain lens (this file's
// population: no `{key: $actorKey}` position at all) is neither an
// actor-aggregate nor a Personal lens by construction (both require that
// position), so it always qualifies.
func threadsForClosure(rule *lens.Rule) bool {
	isOperationRoleIndex := len(rule.Into.Key) == 1 && rule.Into.Key[0] == "operationType" && projection.IsAuthPlane(rule)
	return !isOperationRoleIndex
}

// validColumns reports whether cols would pass fullCR.ValidateKeyColumns(),
// leaving cr.KeyColumns exactly as it found it either way.
func validColumns(fullCR *full.CompiledRule, cols []string) bool {
	prior := fullCR.KeyColumns
	fullCR.KeyColumns = cols
	err := fullCR.ValidateKeyColumns()
	fullCR.KeyColumns = prior
	return err == nil
}

// closureKeyColumns picks the key columns to thread onto fullCR before asking
// AnchorProjectionKey the closure question, mirroring what real activation
// threads (threadsForClosure above) with one addition production does not
// need: corpusLensRule (label_derivation_corpus_census_test.go) builds
// *lens.Rule by hand from the package's declared LensSpec, because the real
// wire encoding (pkgmgr's lensSpecBody / lens.translateSpec) is unexported —
// and it does not replicate translateSpec's GrantTable-specific default (a
// GrantTable lens's real Into.Key defaults to adapter.GrantKeyColumns,
// corekv_source.go:1425; corpusLensRule always defaults to ["key"] instead).
// So a GrantTable-shaped plain lens (RETURN actor_id/anchor_id/grant_source,
// no `key` alias at all — clinicPatientReadGrants is one) fails
// ValidateKeyColumns on the census rule's own default key. This tries the
// real grant key set as a second guess before giving up and leaving
// KeyColumns unset, which drops AnchorProjectionKey to its own unthreaded
// legacy single-key fallback — still a valid, if less complete, question to
// ask, and never a reason to fail the census.
func closureKeyColumns(fullCR *full.CompiledRule, rule *lens.Rule) []string {
	if !threadsForClosure(rule) {
		return nil
	}
	for _, try := range [][]string{rule.Into.Key, adapter.GrantKeyColumns} {
		if len(try) > 0 && validColumns(fullCR, try) {
			return try
		}
	}
	return nil
}

// deriveScanRootVerdict is the live classification for one plain lens: the
// same computation TestScanRootCorpusCensus_Partition aggregates and
// TestScanRootCorpusCensus_PinnedVerdicts compares against
// scanRootCorpusVerdicts.
func deriveScanRootVerdict(t *testing.T, eng *full.Engine, name, spec string, rule *lens.Rule) plainScanRootVerdict {
	t.Helper()
	cr, err := eng.Parse(spec)
	require.NoErrorf(t, err, "%s must parse", name)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.Truef(t, isFull, "%s must compile to the full engine", name)

	// !exhaustive (an untyped relationship anywhere in the query) must also
	// count as "has a neighbour": ReferencedRelations silently drops an
	// untyped hop from rels rather than naming it, so len(rels) > 0 alone
	// would misclassify such a lens as single-node ("no neighbour endpoint
	// at all") instead of reaching ScanRootHopIndex's own rootUntypedHop
	// refusal path. The live corpus happens to carry zero untyped-hop plain
	// lenses today, so this widens no verdict currently pinned — it closes a
	// classification gap the corpus could hit on its next edit.
	rels, exhaustive := fullCR.ReferencedRelations()
	v := plainScanRootVerdict{hasNeighbour: len(rels) > 0 || !exhaustive}

	rx := fullCR.ScanRootHopIndex()
	v.reason = rx.Incomplete
	if !rx.Complete || !v.hasNeighbour {
		return v
	}

	if cols := closureKeyColumns(fullCR, rule); cols != nil {
		require.NoErrorf(t, projection.ThreadKeyColumns(fullCR, nil, cols), "%s", name)
	} else {
		fullCR.KeyColumns = nil
	}
	label, ok := fullCR.AnchorLabel()
	require.Truef(t, ok, "%s: ScanRootHopIndex is complete, so AnchorLabel must resolve too", name)
	_, closureOK := eng.AnchorProjectionKey(cr, syntheticEventKey(label), label, map[string]any{})
	v.closure = closureRefused
	if closureOK {
		v.closure = closureHolds
	}

	// This column's verdict and the narrowing licence's own closure conjunct
	// must stay the same answer. Three routes reach it, and they agree across
	// the whole corpus today:
	//
	//   - AnchorProjectionKey (this column) resolves an EVENT's key, so it
	//     also evaluates the key columns;
	//   - HasAnchorOnlyKeyColumns is the structural half a per-LENS caller can
	//     ask with no event, and is strictly weaker — a key column that is
	//     anchor-only but evaluates to nil, or needs an aspect read no root
	//     body carries, passes it and fails the evaluation;
	//   - ProjectsOneRowPerAnchor is what the WRITE licence reads, and is
	//     strictly stronger — it also requires a key column that IDENTIFIES
	//     the anchor, without which several anchors group into one row that an
	//     evaluation seeded at a single anchor would truncate.
	//
	// A divergence is a lens ARRIVING at (or leaving) the set a future
	// increment acts on while this column still reads its old verdict — the
	// direction §2's header says needs an argument, caught at the lens rather
	// than left to be noticed later.
	require.Equalf(t, closureOK, fullCR.HasAnchorOnlyKeyColumns(),
		"%s: the structural closure predicate and this census's per-event closure verdict disagree — "+
			"decide which one this column should pin before recording a verdict for it", name)
	require.Equalf(t, closureOK, fullCR.ProjectsOneRowPerAnchor(),
		"%s: the write licence's closure conjunct (ProjectsOneRowPerAnchor) and this census's closure verdict "+
			"disagree — the licensed set has moved away from what this column pins, so review the lens before "+
			"recording a verdict for it", name)
	return v
}

// syntheticEventKey is a well-formed, valid-alphabet Contract #1 vertex key
// (internal/substrate/nanoid.go's convention) for a label this census never
// writes anywhere — AnchorProjectionKey resolves it read-free from the
// synthetic root body below, so no live vertex needs to exist.
func syntheticEventKey(label string) string {
	return "vtx." + label + ".S1aaaaaaaaaaaaaaaaaa"
}

// TestScanRootCorpusCensus_PinnedVerdicts pins the ScanRootHopIndex +
// closure verdict for every plain lens the corpus ships, mirroring
// TestCorpusAnchorHopIndex_PinnedConjuncts. A row moving from a refused
// reason to rootIndexed, or from closureRefused to closureHolds, is a lens
// ARRIVING at what a future increment would act on — the direction that
// needs an argument, per this file's header.
func TestScanRootCorpusCensus_PinnedVerdicts(t *testing.T) {
	eng := full.New()
	got := map[string]plainScanRootVerdict{}
	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		ax := fullCR.AnchorHopIndex()
		if ax.Incomplete != noAnchorPosition {
			return // anchored — anchor_hopindex_corpus_census_test.go's own corpus
		}
		_, dup := got[name]
		require.Falsef(t, dup, "two installed lenses share the canonical name %q", name)
		got[name] = deriveScanRootVerdict(t, eng, name, spec, rule)
	})

	for name, want := range scanRootCorpusVerdicts {
		have, present := got[name]
		if !present {
			t.Errorf("pinned plain lens %q no longer reaches the census — "+
				"remove its row if the lens was retired, and review it if it gained a $actorKey position", name)
			continue
		}
		require.Equalf(t, want.hasNeighbour, have.hasNeighbour,
			"%s: whether it references any relation at all moved — review, then update the pin", name)
		if want.reason == rootIndexed {
			require.Emptyf(t, have.reason, "%s: ScanRootHopIndex stopped being complete (%s)", name, have.reason)
		} else {
			require.Containsf(t, have.reason, want.reason,
				"%s's declining conjunct moved; a move to indexed means a future increment would act on this lens's derived anchor set", name)
		}
		require.Equalf(t, want.closure, have.closure,
			"%s's closure verdict moved — a move to closureHolds is a lens a future licence could narrow", name)
	}
	for name := range got {
		_, pinned := scanRootCorpusVerdicts[name]
		require.Truef(t, pinned,
			"plain lens %q ships with no pinned ScanRootHopIndex verdict (derived: %+v) — review it, then record it in scanRootCorpusVerdicts",
			name, got[name])
	}
}

// TestScanRootCorpusCensus_Partition is the mechanical gate: every plain
// lens the corpus ships is classified into exactly one bucket — no-neighbour
// (single-node), a named ScanRootHopIndex conjunct, or addressable (complete
// AND has a neighbour) — and reports the live measurement rather than
// hand-asserting the design doc's own numbers, per §2's posture: "this
// design therefore does not predict the final number… a refusal that turns
// out to dominate becomes a filed, grounded follow-on rather than a guess
// made now." checked guards against an emptied enumeration reading as green,
// mirroring anchor_hopindex_corpus_census_test.go's own guard.
func TestScanRootCorpusCensus_Partition(t *testing.T) {
	eng := full.New()

	var (
		plainCount   int
		noNeighbour  int
		exposedCount int
		byConjunct   = map[string]int{}
		addressable  int
		closureHits  = map[string]int{}
		distHist     = map[int]int{}
	)

	forEachCorpusCypher(t, func(name, spec string, rule *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		ax := fullCR.AnchorHopIndex()
		if ax.Incomplete != noAnchorPosition {
			return
		}
		plainCount++

		rx := fullCR.ScanRootHopIndex()
		v := deriveScanRootVerdict(t, eng, name, spec, rule)
		if !v.hasNeighbour {
			noNeighbour++
			return
		}
		exposedCount++
		if v.reason != rootIndexed {
			byConjunct[v.reason]++
			return
		}
		addressable++
		closureHits[v.closure]++

		maxDist := 0
		for _, d := range rx.Dist {
			if d > maxDist {
				maxDist = d
			}
		}
		distHist[maxDist]++
	})

	require.Greaterf(t, plainCount, 40,
		"only %d plain lenses reached this gate — the corpus enumeration or the noAnchorPosition filter moved", plainCount)

	var declined int
	for _, n := range byConjunct {
		declined += n
	}
	require.Equalf(t, plainCount, noNeighbour+declined+addressable,
		"every plain lens must land in exactly one bucket: no-neighbour (%d) + declined (%d) + addressable (%d) must equal plainCount (%d)",
		noNeighbour, declined, addressable, plainCount)
	require.Equalf(t, addressable, closureHits[closureHolds]+closureHits[closureRefused],
		"every addressable lens must resolve the closure conjunct one way or the other")

	t.Logf("plain lenses (no $actorKey position): %d", plainCount)
	t.Logf("no neighbour endpoint at all (single-node): %d", noNeighbour)
	t.Logf("exposed (references at least one relation): %d", exposedCount)
	for reason, n := range byConjunct {
		t.Logf("  declined — %s: %d", reason, n)
	}
	t.Logf("addressable (ScanRootHopIndex complete AND exposed): %d", addressable)
	t.Logf("  %s: %d", closureHolds, closureHits[closureHolds])
	t.Logf("  %s: %d", closureRefused, closureHits[closureRefused])
	t.Logf("distance profile (max Dist reached from the anchor pattern, among addressable):")
	for d := 0; d <= 10; d++ {
		if n := distHist[d]; n > 0 {
			t.Logf("  distance %d: %d", d, n)
		}
	}
}

// TestScanRootCorpusCensus_EveryReasonIsAKnownConjunct default-denies the
// vocabulary above, mirroring
// TestCorpusAnchorHopIndex_EveryReasonIsAKnownConjunct: a conjunct added to
// ScanRootHopIndex's predicate without a constant here would otherwise land
// in scanRootCorpusVerdicts as whichever existing substring happened to
// match, or as an unreadable row nobody could review.
func TestScanRootCorpusCensus_EveryReasonIsAKnownConjunct(t *testing.T) {
	eng := full.New()
	checked := 0
	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)
		ax := fullCR.AnchorHopIndex()
		if ax.Incomplete != noAnchorPosition {
			return
		}
		checked++
		rx := fullCR.ScanRootHopIndex()
		if rx.Complete {
			return
		}
		matched := false
		for _, k := range scanRootConjuncts {
			if strings.Contains(rx.Incomplete, k) {
				matched = true
				break
			}
		}
		require.Truef(t, matched,
			"%s declines ScanRootHopIndex with %q, which matches no conjunct constant in this file — name it before pinning it",
			name, rx.Incomplete)
	})
	require.Greaterf(t, checked, 40, "only %d plain lenses reached this gate", checked)
}
