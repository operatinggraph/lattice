package full

// The two shipped multi-`WITH` lenses OUTSIDE edge-manifest that the grouping
// analysis reaches, each pinned by name.
//
// The design's §3 census asserted the three generated read-grant producers were
// the entire population of the carried-accumulator shape, and that every other
// multi-`WITH` lens in the corpus carried only node references. That was wrong
// in both directions, which is what the corpus census in
// grouping_corpus_census_test.go now makes impossible to repeat: privacy-base's
// erasure-residue lens chains five aggregating clauses carrying int64 counts,
// and cafe-domain's tab-settlement lens is a multi-`WITH` the analysis refuses.
// Neither had a test, so a change to the first one's grouping key could land
// with nothing watching.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	cafedomain "github.com/operatinggraph/lattice/packages/cafe-domain"
	privacybase "github.com/operatinggraph/lattice/packages/privacy-base"
)

// lensSpecNamed returns the shipped spec of one lens, straight from its
// package's own Definition — never a copy pasted into the test, which would
// stop tracking the lens the day it is edited.
func lensSpecNamed(t testing.TB, lenses []pkgmgr.LensSpec, canonical string) string {
	t.Helper()
	for _, l := range lenses {
		if l.CanonicalName == canonical {
			require.NotEmptyf(t, l.Spec, "%s carries no Spec", canonical)
			return l.Spec
		}
	}
	t.Fatalf("lens %q is no longer declared by its package", canonical)
	return ""
}

// TestGroupingReduction_ErasureResidueLensProjectsIdenticalRows is the
// equivalence proof for privacy-base's identityErasureResidue — the corpus's
// OTHER carried-accumulator lens, and the one the design's census missed.
//
// Its five staged clauses each carry every prior `count(DISTINCT …)` forward as
// a bare int64 carry, so stages 2-5 all arm a reduction. A merged grouping key
// there would ADD residue to a count, driving `violating` true — fail-closed,
// but only by luck: nothing was checking.
func TestGroupingReduction_ErasureResidueLensProjectsIdenticalRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	spec := lensSpecNamed(t, privacybase.Package.Lenses, "identityErasureResidue")

	// Every stage must be armed, or this test degrades into comparing one code
	// path with itself the moment the lens or the analysis changes shape.
	q := mustParseQuery(t, spec)
	staged := 0
	for _, p := range analyseGrouping(q) {
		if !p.Grouping {
			continue
		}
		staged++
		require.Emptyf(t, p.Refusal, "stage %d of identityErasureResidue was refused: %s", staged, p.Refusal)
		require.Equalf(t, []string{"i"}, p.Key,
			"every stage must reduce to the erased identity alone, not %v", p.Key)
	}
	require.Equal(t, 5, staged, "the lens chains five aggregating clauses")
	require.Len(t, analyseGroupingRedundancy(q), 4,
		"stages 2-5 each shed the counts they carry; stage 1 has nothing to shed")

	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	putVertex(t, reg, coreKV, "erased", "identity", nil)
	putAspect(t, reg, coreKV, "erased", "erasureRequested",
		map[string]any{"requestedAt": "2026-08-11T00:00:00Z"})

	// Residue on all five relations, with a DIFFERENT count on each, so a
	// merged group could not coincidentally reproduce the right numbers.
	residue := []struct {
		rel, class string
		count      int
		inbound    bool
	}{
		{"boundTo", "credential", 3, true},
		{"boundTo", "credential", 2, false},
		{"indexes", "identityindex", 4, true},
		{"duplicateOf", "identity", 1, false},
		{"duplicateOf", "identity", 5, true},
	}
	want := map[string]int64{}
	for ri, r := range residue {
		for i := 0; i < r.count; i++ {
			name := r.rel + string(rune('a'+ri)) + string(rune('0'+i))
			putVertex(t, reg, coreKV, name, r.class, nil)
			if r.inbound {
				putEdge(t, reg, adjKV, r.rel, name, "erased")
			} else {
				putEdge(t, reg, adjKV, r.rel, "erased", name)
			}
		}
	}
	want["boundInResidue"] = 3
	want["boundOutResidue"] = 2
	want["indexResidue"] = 4
	want["duplicateOutResidue"] = 1
	want["duplicateInResidue"] = 5

	rows := executeBothWays(t, spec, vtxKey(reg, "erased"), adjKV, coreKV)
	require.Len(t, rows, 1, "one row per erasure-requested identity")
	for column, count := range want {
		require.EqualValuesf(t, count, rows[0].Values[column],
			"%s must be the residue actually present — a merged grouping key inflates it", column)
	}
	require.Equal(t, true, rows[0].Values["violating"],
		"residue on every relation is an incomplete erasure")
}

// TestGroupingReduction_CafeTabSettlementIsRefused pins the OTHER multi-`WITH`
// lens outside edge-manifest, and pins it as a REFUSAL with its reason: the
// first clause groups on `l` among others, and the second does not carry `l`
// at all, so the dependence chain ends there. Nothing about this lens is
// reduced, and if that ever changes it is a deliberate decision, not a drift.
func TestGroupingReduction_CafeTabSettlementIsRefused(t *testing.T) {
	spec := lensSpecNamed(t, cafedomain.Package.Lenses, "cafeTabSettlement")
	q := mustParseQuery(t, spec)

	require.Empty(t, analyseGroupingRedundancy(q),
		"cafeTabSettlement must arm no reduction at all")

	plans := analyseGrouping(q)
	require.GreaterOrEqual(t, len(plans), 2)
	require.Empty(t, plans[0].Refusal, "the first clause is analysable; it simply determines only txCount")
	require.Contains(t, plans[0].Key, "l",
		"the coalesced leaseapp is part of the first clause's grouping key")
	require.Contains(t, plans[1].Refusal, `"l"`,
		"the second clause drops `l`, which is what ends the chain")
}
