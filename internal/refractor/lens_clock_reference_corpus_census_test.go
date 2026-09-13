// Clock-reference corpus census — capability-ephemeral-recorded-expiry-design.md
// §3.5.
//
// The corpus's standing rule (`capabilityEphemeral` reads a recorded fact, not
// a clock) holds only for as long as no lens ANYWHERE in the corpus reads
// `$now`. This file is the gate that keeps it true: it enumerates every
// executable cypher the installed corpus ships through `forEachCorpusCypher`
// (`label_derivation_corpus_census_test.go`) and drives each one through the
// REAL parameter-reference analysis, `full.CompiledRule.ReferencesParam`
// (`ruleengine/full/params.go`), rather than a grep of cypher text.
//
// A grep agrees with a broken gate — it cannot tell a live `$now` read apart
// from a comment mentioning `$now`, a doc string quoting the old predicate, or
// a string literal that happens to contain the substring — and it cannot see
// through a `fmt.Sprintf` template the way the real parser does. The census
// pins the ANALYSIS's verdict, so a lens that starts reading the clock again
// fails here by name, and a lens whose cypher merely talks about the clock in
// a comment does not.
//
// There is no allowlist for `$now`: every lens and every branch must come back
// `(referenced=false, exhaustive=true)`. `$projectedAt` is different — one lens,
// `capabilityRoleIndex`, returns it as a bare output column the convergence
// sweep excludes as volatile — so that one name is pinned exactly, in both
// directions, rather than allowed silently.
package refractor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// clockReferenceCorpusFloor is the least number of executable cyphers the
// enumeration must visit for its verdict to mean anything; the live corpus is
// about twice this.
const clockReferenceCorpusFloor = 60

// projectedAtPin is the exact set of lenses whose cypher may reference
// `$projectedAt`, with the reason it is sound to. A name here that
// ReferencesParam stops finding, or a referencing lens missing from here, is
// unpinned drift and fails TestCorpusClockReference_ProjectedAtIsPinnedByName.
var projectedAtPin = map[string]string{
	"capabilityRoleIndex": "a bare `$projectedAt AS projectedAt` output column the convergence sweep excludes as volatile",
}

// TestCorpusClockReference_NoLensReferencesNow is the gate: no lens or branch
// in the installed corpus may read `$now`, anywhere, with no exception.
//
// Expiry is a recorded fact — the `freshnessExpiry` marker a Weaver target
// writes when its own deadline fires (packages/orchestration-base/mark_expired.go)
// — never a clock reading. A lens that reads `$now` re-derives its own verdict
// on every evaluation instead of reading what already happened, so two deep
// verify passes over an unchanged graph disagree and the sweep heals a
// projection nothing broke.
func TestCorpusClockReference_NoLensReferencesNow(t *testing.T) {
	eng := full.New()
	enumerated := 0

	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, _ bool) {
		enumerated++
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)

		referenced, exhaustive := fullCR.ReferencesParam("now")
		require.Truef(t, exhaustive,
			"%s: the $now analysis is not exhaustive over this cypher's shape — "+
				"the walk cannot prove this lens clock-free, which is itself a "+
				"failure: either the walk needs to model the unmodelled clause/"+
				"expression, or the lens needs rewriting into a shape it can "+
				"cover. Expiry is a recorded fact, not a clock reading — read the "+
				"freshnessExpiry marker (packages/orchestration-base/mark_expired.go) "+
				"instead of $now.", name)
		require.Falsef(t, referenced,
			"%s references $now. Expiry is a recorded fact, not a clock reading — "+
				"read the freshnessExpiry marker (packages/orchestration-base/mark_expired.go) "+
				"instead. There is no allowlist for $now in this corpus.", name)
	})

	t.Logf("enumerated %d corpus specs", enumerated)
	require.GreaterOrEqualf(t, enumerated, clockReferenceCorpusFloor,
		"the corpus enumeration collapsed to %d specs — this census is only worth what it covers, "+
			"and an empty (or near-empty) enumeration must not read as a clean table", enumerated)
}

// TestCorpusClockReference_ProjectedAtIsPinnedByName pins `$projectedAt`
// reference to exactly the lenses in projectedAtPin, asserted in both
// directions so the pin cannot rot: a lens that starts referencing it fails by
// name, and a pinned name that stops referencing it fails too, so the pin
// cannot go stale by silently over-covering.
func TestCorpusClockReference_ProjectedAtIsPinnedByName(t *testing.T) {
	eng := full.New()
	referencing := map[string]bool{}

	forEachCorpusCypher(t, func(name, spec string, _ *lens.Rule, _, _ bool) {
		cr, err := eng.Parse(spec)
		require.NoErrorf(t, err, "%s must parse", name)
		fullCR, isFull := cr.(*full.CompiledRule)
		require.Truef(t, isFull, "%s must compile to the full engine", name)

		referenced, exhaustive := fullCR.ReferencesParam("projectedAt")
		require.Truef(t, exhaustive,
			"%s: the $projectedAt analysis is not exhaustive over this cypher's shape — "+
				"the walk cannot prove this lens's $projectedAt posture, so it cannot be "+
				"judged against the pin either way", name)
		if referenced {
			referencing[name] = true
		}
	})

	for name, reason := range projectedAtPin {
		require.Truef(t, referencing[name],
			"%s is pinned as referencing $projectedAt (%s) but the analysis no longer finds it — "+
				"the pin has rotted; either the cypher stopped reading $projectedAt (drop the pin) "+
				"or the analysis regressed", name, reason)
	}
	for name := range referencing {
		_, pinned := projectedAtPin[name]
		require.Truef(t, pinned,
			"%s references $projectedAt but is not in projectedAtPin — a new $projectedAt read "+
				"needs its own argument for why the convergence sweep can exclude it as volatile, "+
				"the way capabilityRoleIndex's bare output column does; add it to the pin with that "+
				"reason, or remove the reference", name)
	}
}
