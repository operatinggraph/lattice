package full

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// seedProvCorpus writes the graph every provenance vector below reads. It is
// one graph rather than one per vector so a vector's assertion is about the
// keys the rule reached, not about which vertices exist:
//
//   - alice is the actor; bob is a second identity a scan-seeded pattern
//     fetches and a property predicate rejects.
//   - alice worksAt four vertices: two admitted orgs, one tombstoned org, and
//     a vendor the `:org` label rejects — the four outcomes one hop's candidate
//     set can have.
//   - acme and globex sit in different cities (a grouping vector needs two
//     members that read different vertices) and share one HQ city (a memo
//     vector needs a second row served from the first row's read).
//   - paris reaches a region and berlin does not, so a two-hop comprehension
//     in a WHERE genuinely partitions the rows.
//   - acme's lead is tombstoned and globex's is live, so one branch of a hop
//     dies beside a surviving sibling; nothing has a live escalatesTo, so the
//     whole of that walk dies.
func seedProvCorpus(t *testing.T, reg *fixtureRegistry, adjKV, coreKV *substrate.KV) {
	t.Helper()
	putVertex(t, reg, coreKV, "alice", "identity", map[string]any{"status": "active"})
	putVertex(t, reg, coreKV, "bob", "identity", map[string]any{"status": "inactive"})
	putVertex(t, reg, coreKV, "acme", "org", map[string]any{"tier": "gold", "name": "Northwind"})
	putVertex(t, reg, coreKV, "globex", "org", map[string]any{"tier": "silver", "name": "Northwind"})
	putVertex(t, reg, coreKV, "ghost", "org", map[string]any{"isDeleted": true})
	putVertex(t, reg, coreKV, "vendorco", "vendor", nil)
	putVertex(t, reg, coreKV, "paris", "city", map[string]any{"country": "EU"})
	putVertex(t, reg, coreKV, "berlin", "city", map[string]any{"country": "EU"})
	putVertex(t, reg, coreKV, "europe", "region", nil)
	putVertex(t, reg, coreKV, "lena", "person", nil)
	putVertex(t, reg, coreKV, "deadlead", "person", map[string]any{"isDeleted": true})
	putVertex(t, reg, coreKV, "deadmgr", "person", map[string]any{"isDeleted": true})
	putVertex(t, reg, coreKV, "deadteam", "team", map[string]any{"isDeleted": true})
	putVertex(t, reg, coreKV, "deadmayor", "person", map[string]any{"isDeleted": true})
	putAspect(t, reg, coreKV, "acme", "presentation", map[string]any{"name": "Acme Ltd"})

	for _, to := range []string{"acme", "globex", "ghost", "vendorco"} {
		putEdge(t, reg, adjKV, "worksAt", "alice", to)
	}
	putEdge(t, reg, adjKV, "in", "acme", "paris")
	putEdge(t, reg, adjKV, "in", "globex", "berlin")
	putEdge(t, reg, adjKV, "hq", "acme", "paris")
	putEdge(t, reg, adjKV, "hq", "globex", "paris")
	putEdge(t, reg, adjKV, "within", "paris", "europe")
	putEdge(t, reg, adjKV, "hasLead", "acme", "deadlead")
	putEdge(t, reg, adjKV, "hasLead", "globex", "lena")
	putEdge(t, reg, adjKV, "escalatesTo", "acme", "deadmgr")
	putEdge(t, reg, adjKV, "manages", "alice", "deadteam")
	putEdge(t, reg, adjKV, "mayor", "paris", "deadmayor")
}

// runProv evaluates body and returns the rows, checking on every vector that
// no result carries the reserved binding entry into the values an adapter
// renders onto the wire.
func runProv(
	t *testing.T,
	e *Engine,
	body string,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) []ruleengine.ProjectionResult {
	t.Helper()
	cr, err := e.Parse(body)
	require.NoError(t, err, "spec must parse:\n%s", body)
	out, err := e.ExecuteWith(context.Background(), cr, ec, adjKV, coreKV)
	require.NoError(t, err)
	for _, r := range out {
		require.NotContains(t, r.Values, provBindingKey,
			"the provenance chain reached a projected row's values")
		require.NotContains(t, r.Key, provBindingKey,
			"the provenance chain reached a projected row's key")
	}
	return out
}

// runProvFlat is runProv with branch decomposition off, for a vector whose
// subject is what the product path does with a branch it discards. The
// decomposed path reaches the same rows by construction, which
// TestBranchDecomposition_RandomizedCorporaDifferential compares directly.
func runProvFlat(
	t *testing.T,
	body string,
	ec ruleengine.EventContext,
	adjKV, coreKV *substrate.KV,
) []ruleengine.ProjectionResult {
	t.Helper()
	e := New()
	cr, err := e.Parse(body)
	require.NoError(t, err, "spec must parse:\n%s", body)
	compiled, ok := cr.(*CompiledRule)
	require.True(t, ok)
	out, err := e.ExecuteWith(
		context.Background(), withoutBranchDecomposition(compiled), ec, adjKV, coreKV)
	require.NoError(t, err)
	return out
}

// provFor returns the provenance of the single row whose `anchor` column is
// want, failing when no such row was projected.
func provFor(t *testing.T, results []ruleengine.ProjectionResult, want string) []string {
	t.Helper()
	for _, r := range results {
		if fmt.Sprint(r.Values["anchor"]) == want {
			return r.Provenance
		}
	}
	t.Fatalf("no row with anchor %q in %d results", want, len(results))
	return nil
}

func actorEC(reg *fixtureRegistry) ruleengine.EventContext {
	return ruleengine.EventContext{Parameters: map[string]any{"actorKey": vtxKey(reg, "alice")}}
}

// TestProvenance_BoundAndRejectedCandidates pins the closed candidate set one
// hop's rows depend on: the point-seeded head, the two endpoints it binds, the
// endpoint it finds tombstoned and the endpoint the `:org` label rejects. All
// four outcomes are read on the way to both rows, so both rows carry all four
// — the equality is over the whole set, not a containment check that a wider
// record would also satisfy.
func TestProvenance_BoundAndRejectedCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	want := []string{
		vtxKey(reg, "acme"), vtxKey(reg, "alice"), vtxKey(reg, "ghost"),
		vtxKey(reg, "globex"), vtxKey(reg, "vendorco"),
	}
	for _, mode := range []struct {
		name string
		eng  *Engine
	}{{"batched", New()}, {"per-key", prefetchOff(New())}} {
		t.Run(mode.name, func(t *testing.T) {
			results := runProv(t, mode.eng,
				`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)
				 RETURN o.key AS anchor`,
				actorEC(reg), adjKV, coreKV)
			require.Len(t, results, 2)
			for _, name := range []string{"acme", "globex"} {
				require.ElementsMatch(t, want, provFor(t, results, vtxKey(reg, name)),
					"row %s", name)
			}
		})
	}
}

// TestProvenance_SeededAnchorNarrowsTheRecord pins that provenance follows the
// candidate set the evaluation actually built: an anchor seeded to alice reads
// alice alone, where the same rule scanning the type would have read bob too.
func TestProvenance_SeededAnchorNarrowsTheRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	body := `MATCH (i:identity)-[:worksAt]->(o:org) RETURN o.key AS anchor`

	seeded := runProv(t, New(), body,
		ruleengine.EventContext{SeedAnchor: vtxKey(reg, "alice")}, adjKV, coreKV)
	require.Len(t, seeded, 2)
	prov := provFor(t, seeded, vtxKey(reg, "acme"))
	require.Contains(t, prov, vtxKey(reg, "alice"))
	require.NotContains(t, prov, vtxKey(reg, "bob"))

	scanned := runProv(t, New(), body, ruleengine.EventContext{}, adjKV, coreKV)
	require.Contains(t, provFor(t, scanned, vtxKey(reg, "acme")), vtxKey(reg, "bob"),
		"an unseeded scan reads every identity, and every row it derives depends on that")
}

// TestProvenance_ScanCandidateRejectedByProperty pins the scan arm's own
// rejection: bob is fetched and excluded by the pattern's property predicate,
// and the surviving row depends on that exclusion — bob turning active adds a
// row, bob's own body is what says he does not.
func TestProvenance_ScanCandidateRejectedByProperty(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	results := runProv(t, New(),
		`MATCH (i:identity {status: "active"}) RETURN i.key AS anchor`,
		ruleengine.EventContext{}, adjKV, coreKV)
	require.Len(t, results, 1)
	require.Contains(t, provFor(t, results, vtxKey(reg, "alice")), vtxKey(reg, "bob"))
}

// TestProvenance_MatchWhereWalksUnderTheRowCursor pins MATCH's own WHERE: a
// two-hop comprehension inside it binds its second hop only in clones the
// comprehension discards, so the region it reaches is recorded on the row that
// evaluated the predicate or nowhere at all.
func TestProvenance_MatchWhereWalksUnderTheRowCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	results := runProv(t, New(),
		`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)
		 WHERE (o)-[:in]->(c:city)-[:within]->(:region)
		 RETURN o.key AS anchor`,
		actorEC(reg), adjKV, coreKV)
	require.Len(t, results, 1, "the predicate must actually filter")
	prov := provFor(t, results, vtxKey(reg, "acme"))
	require.Contains(t, prov, vtxKey(reg, "paris"))
	require.Contains(t, prov, vtxKey(reg, "europe"))
}

// TestProvenance_WithWhereWalksUnderTheRowCursor is the same predicate at the
// other site, where the row is a projected one and no MATCH walk is in flight
// to record on its behalf.
func TestProvenance_WithWhereWalksUnderTheRowCursor(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	results := runProv(t, New(),
		`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)
		 WITH i, o
		 WHERE (o)-[:in]->(c:city)-[:within]->(:region)
		 RETURN o.key AS anchor`,
		actorEC(reg), adjKV, coreKV)
	require.Len(t, results, 1, "the predicate must actually filter")
	prov := provFor(t, results, vtxKey(reg, "acme"))
	require.Contains(t, prov, vtxKey(reg, "paris"))
	require.Contains(t, prov, vtxKey(reg, "europe"))
}

// TestProvenance_OptionalMissRecordsTheTombstone pins the OPTIONAL null
// binding: nothing the walk reached survives into a row of its own, and the
// tombstoned vertex that made the walk miss is exactly what the row's
// emptiness depends on.
func TestProvenance_OptionalMissRecordsTheTombstone(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	t.Run("one hop", func(t *testing.T) {
		results := runProv(t, New(),
			`MATCH (i:identity {key: $actorKey})
			 OPTIONAL MATCH (i)-[:manages]->(t:team)
			 RETURN i.key AS anchor, t.key AS team`,
			actorEC(reg), adjKV, coreKV)
		require.Len(t, results, 1)
		require.Nil(t, results[0].Values["team"])
		require.Contains(t, provFor(t, results, vtxKey(reg, "alice")), vtxKey(reg, "deadteam"))
	})

	// Two hops: the tombstone is read from a frontier binding the walk then
	// abandons, so it reaches the null row only because the abandoned frontier
	// hands its reads back to the binding the walk started from.
	t.Run("two hops", func(t *testing.T) {
		results := runProvFlat(t,
			`MATCH (i:identity {key: $actorKey})
			 OPTIONAL MATCH (i)-[:worksAt]->(o:org)-[:escalatesTo]->(m:person)
			 RETURN i.key AS anchor, m.key AS mgr`,
			actorEC(reg), adjKV, coreKV)
		require.Len(t, results, 1)
		require.Nil(t, results[0].Values["mgr"])
		require.Contains(t, provFor(t, results, vtxKey(reg, "alice")), vtxKey(reg, "deadmgr"))
	})

	// Three hops: the tombstone is two frontiers deep, so it reaches the null
	// row only by the abandoned frontier carrying its own ancestors with it.
	t.Run("three hops", func(t *testing.T) {
		results := runProvFlat(t,
			`MATCH (i:identity {key: $actorKey})
			 OPTIONAL MATCH (i)-[:worksAt]->(o:org)-[:in]->(c:city)-[:mayor]->(p:person)
			 RETURN i.key AS anchor, p.key AS mayor`,
			actorEC(reg), adjKV, coreKV)
		require.Len(t, results, 1)
		require.Nil(t, results[0].Values["mayor"])
		require.Contains(t, provFor(t, results, vtxKey(reg, "alice")), vtxKey(reg, "deadmayor"))
	})
}

// TestProvenance_AbandonedSiblingBranchReachesTheSurvivingRow pins the other
// half of the same rule: acme's lead is tombstoned and globex's is not, so the
// walk abandons acme's frontier while globex's produces the row. The
// tombstoned lead is what the surviving row would have to be re-derived over
// if it came back, and the frontier that read it hands it to the binding both
// branches share.
func TestProvenance_AbandonedSiblingBranchReachesTheSurvivingRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	results := runProvFlat(t,
		`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)-[:hasLead]->(l:person)
		 RETURN o.key AS anchor, l.key AS lead`,
		actorEC(reg), adjKV, coreKV)
	require.Len(t, results, 1)
	require.Contains(t, provFor(t, results, vtxKey(reg, "globex")), vtxKey(reg, "deadlead"))
}

// TestProvenance_MemoServedReadRecordsOnEverySecondRow pins that a read is
// recorded where the evaluation USES the key, not where it first fetched it:
// both orgs' HQ is paris, so the second row's hop is served from the memo (and,
// with batching on, from the staging map before it) and depends on paris
// exactly as the first row does.
func TestProvenance_MemoServedReadRecordsOnEverySecondRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	for _, mode := range []struct {
		name string
		eng  *Engine
	}{{"batched", New()}, {"per-key", prefetchOff(New())}} {
		t.Run(mode.name, func(t *testing.T) {
			results := runProv(t, mode.eng,
				`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)-[:hq]->(c:city)
				 RETURN o.key AS anchor, c.key AS city`,
				actorEC(reg), adjKV, coreKV)
			require.Len(t, results, 2)
			for _, name := range []string{"acme", "globex"} {
				require.Contains(t, provFor(t, results, vtxKey(reg, name)), vtxKey(reg, "paris"),
					"row %s", name)
			}
		})
	}
}

// TestProvenance_SiblingRowsDoNotShareABranchsReads is the precision half of
// the head-chain rule, and the property the whole mechanism exists for: acme's
// row read paris and globex's read berlin, so neither row names the other's
// city. A record kept on the binding both rows descend from would satisfy
// every containment assertion above while publishing both rows for either
// city's event.
func TestProvenance_SiblingRowsDoNotShareABranchsReads(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	results := runProv(t, New(),
		`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)-[:in]->(c:city)
		 RETURN o.key AS anchor, c.key AS city`,
		actorEC(reg), adjKV, coreKV)
	require.Len(t, results, 2)

	acme := provFor(t, results, vtxKey(reg, "acme"))
	require.Contains(t, acme, vtxKey(reg, "paris"))
	require.NotContains(t, acme, vtxKey(reg, "berlin"))

	globex := provFor(t, results, vtxKey(reg, "globex"))
	require.Contains(t, globex, vtxKey(reg, "berlin"))
	require.NotContains(t, globex, vtxKey(reg, "paris"))
}

// TestProvenance_AspectDereferenceFoldsToItsVertex pins the fold: a
// projection item reading an aspect records the aspect's PARENT vertex, which
// is the granularity a CDC arm names, and never the aspect key itself. Both
// read paths reach it — batched, where the aspect is staged ahead of the
// projection and promoted at use, and per-key, where it is a point read.
func TestProvenance_AspectDereferenceFoldsToItsVertex(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	aspectKey := substrate.AspectKey(vtxKey(reg, "acme"), "presentation")
	var byMode [][]string
	for _, mode := range []struct {
		name string
		eng  *Engine
	}{{"batched", New()}, {"per-key", prefetchOff(New())}} {
		t.Run(mode.name, func(t *testing.T) {
			results := runProv(t, mode.eng,
				`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)
				 WHERE o.tier = "gold"
				 RETURN o.key AS anchor, o.presentation.data.name AS displayName`,
				actorEC(reg), adjKV, coreKV)
			require.Len(t, results, 1)
			require.Equal(t, "Acme Ltd", results[0].Values["displayName"])
			prov := provFor(t, results, vtxKey(reg, "acme"))
			require.Contains(t, prov, vtxKey(reg, "acme"))
			require.NotContains(t, prov, aspectKey,
				"an aspect key names no vertex a CDC arm publishes")
			byMode = append(byMode, prov)
		})
	}
	require.Len(t, byMode, 2)
	require.ElementsMatch(t, byMode[0], byMode[1],
		"batching an aspect read must not change what the row depends on")
}

// TestProvenance_ComprehensionsAndExistencePredicate pins the three shapes
// that walk a pattern whose bindings never leave the expression: a one-hop
// comprehension, a two-hop one whose inner clones are discarded at the second
// hop, and an existence predicate.
func TestProvenance_ComprehensionsAndExistencePredicate(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{
			name: "one hop comprehension",
			body: `MATCH (i:identity {key: $actorKey})
			       RETURN i.key AS anchor, [(i)-[:worksAt]->(o:org) | o.key] AS orgs`,
			want: []string{"acme", "globex", "ghost", "vendorco"},
		},
		{
			name: "two hop comprehension",
			body: `MATCH (i:identity {key: $actorKey})
			       RETURN i.key AS anchor,
			              [(i)-[:worksAt]->(o:org)-[:in]->(c:city) | c.key] AS cities`,
			want: []string{"paris", "berlin"},
		},
		{
			name: "two hop existence predicate",
			body: `MATCH (i:identity {key: $actorKey})
			       WITH i
			       WHERE (i)-[:worksAt]->(o:org)-[:in]->(:city)
			       RETURN i.key AS anchor`,
			want: []string{"paris", "berlin"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := runProv(t, New(), tc.body, actorEC(reg), adjKV, coreKV)
			require.Len(t, results, 1)
			prov := provFor(t, results, vtxKey(reg, "alice"))
			for _, name := range tc.want {
				require.Contains(t, prov, vtxKey(reg, name), "vertex %s", name)
			}
		})
	}
}

// TestProvenance_SurvivesNonAggregatingWith pins the boundary every corpus
// lens's tail opens with: the projected row is a fresh binding holding the
// items' values alone, and it descends from the row it was projected from — so
// the traversal's whole candidate set is still what it depends on.
func TestProvenance_SurvivesNonAggregatingWith(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	results := runProv(t, New(),
		`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)
		 WITH o.key AS anchor, o.tier AS tier
		 RETURN anchor, tier`,
		actorEC(reg), adjKV, coreKV)
	require.Len(t, results, 2)
	prov := provFor(t, results, vtxKey(reg, "acme"))
	for _, name := range []string{"alice", "acme", "globex", "ghost", "vendorco"} {
		require.Contains(t, prov, vtxKey(reg, name), "vertex %s", name)
	}
}

// TestProvenance_GroupUnionsItsMembers pins the aggregating arms at both
// sites: acme's row read paris and globex's read berlin, and the one row the
// group projects is derived from both, so it depends on both. Neither city is
// on the chain the two members share, so only the union puts them there.
func TestProvenance_GroupUnionsItsMembers(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	for _, tc := range []struct{ name, body string }{
		{
			name: "aggregating WITH",
			body: `MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)-[:in]->(c:city)
			       WITH i.key AS anchor, collect(c.key) AS cities
			       RETURN anchor, cities`,
		},
		{
			name: "RETURN grouping",
			body: `MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)-[:in]->(c:city)
			       RETURN i.key AS anchor, collect(c.key) AS cities`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := runProv(t, New(), tc.body, actorEC(reg), adjKV, coreKV)
			require.Len(t, results, 1)
			prov := provFor(t, results, vtxKey(reg, "alice"))
			require.Contains(t, prov, vtxKey(reg, "paris"))
			require.Contains(t, prov, vtxKey(reg, "berlin"))
		})
	}
}

// TestProvenance_ExcludedExpansionReachesTheGroup pins the MATCH WHERE's own
// discard: globex is excluded because the predicate walked to berlin and found
// no region there, and the aggregate over the survivors is what berlin's
// answer decides. The excluded expansion projects no row, so the binding the
// survivors share is what carries what its predicate reached.
func TestProvenance_ExcludedExpansionReachesTheGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	results := runProv(t, New(),
		`MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)
		 WHERE (o)-[:in]->(c:city)-[:within]->(:region)
		 RETURN i.key AS anchor, collect(o.key) AS orgs`,
		actorEC(reg), adjKV, coreKV)
	require.Len(t, results, 1)
	require.Equal(t, []any{vtxKey(reg, "acme")}, results[0].Values["orgs"],
		"the predicate must actually exclude globex")
	require.Contains(t, provFor(t, results, vtxKey(reg, "alice")), vtxKey(reg, "berlin"))
}

// TestProvenance_DecomposedBranchReadsOnTheBaseRow pins the cursor's
// precedence over a nested walk: the OPTIONAL branch is expanded one base row
// at a time inside the projection, and its rows are folded into an aggregator
// and thrown away. Everything the branch reads — its predicate's walk
// included — therefore has to record on the base row the fold belongs to.
func TestProvenance_DecomposedBranchReadsOnTheBaseRow(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	body := `MATCH (i:identity {key: $actorKey})
	         OPTIONAL MATCH (i)-[:worksAt]->(o:org)
	         WHERE (o)-[:in]->(c:city)-[:within]->(:region)
	         OPTIONAL MATCH (i)-[:manages]->(t:team)
	         RETURN i.key AS anchor,
	                collect(DISTINCT o.key) AS orgs,
	                collect(DISTINCT t.key) AS teams`

	eng := New()
	cr, err := eng.Parse(body)
	require.NoError(t, err)
	require.NotEmpty(t, cr.(*CompiledRule).branchStages,
		"this vector is about the decomposed path; the rule must decompose its branch")

	results := runProv(t, eng, body, actorEC(reg), adjKV, coreKV)
	require.Len(t, results, 1)
	prov := provFor(t, results, vtxKey(reg, "alice"))
	require.Contains(t, prov, vtxKey(reg, "paris"))
	require.Contains(t, prov, vtxKey(reg, "europe"))
	require.Contains(t, prov, vtxKey(reg, "berlin"))
}

// TestProvenance_DistinctIgnoresTheChain pins both DISTINCT sites: paris and
// berlin share a country, so the two rows differ in nothing but what they
// read, and the deduplication is over the projected columns alone. A chain
// rendered into the key would make every row unique — the pointer is part of
// the rendering — and the row that survives still names both cities, because
// the candidate set is shared by the rows that produced them.
func TestProvenance_DistinctIgnoresTheChain(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	adjKV, coreKV := startExecKVs(t)
	reg := newFixtureRegistry()
	seedProvCorpus(t, reg, adjKV, coreKV)

	for _, tc := range []struct{ name, body string }{
		{
			name: "RETURN DISTINCT",
			body: `MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)-[:in]->(c:city)
			       RETURN DISTINCT c.country AS anchor`,
		},
		{
			name: "WITH DISTINCT",
			body: `MATCH (i:identity {key: $actorKey})-[:worksAt]->(o:org)-[:in]->(c:city)
			       WITH DISTINCT c.country AS anchor
			       RETURN anchor`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			results := runProv(t, New(), tc.body, actorEC(reg), adjKV, coreKV)
			require.Len(t, results, 1, "two rows differing only in provenance are one row")
			require.Contains(t, provFor(t, results, "EU"), vtxKey(reg, "alice"))
		})
	}
}

// TestProvenance_ReadFreeExecutorRecordsNothing pins the anchor-tombstone key
// resolution: it builds an executor with no KV handles and resolves the key
// columns off the event body alone. Nothing is fetched, so nothing is
// recorded, and the recording call is inert rather than a nil dereference on
// the one path that never carries a chain.
func TestProvenance_ReadFreeExecutorRecordsNothing(t *testing.T) {
	ex := &executor{ctx: context.Background()}
	b := binding{"anchor": &nodeRef{
		key:   substrate.VertexKey("org", c1NanoID("acme")),
		props: map[string]any{"class": "org"},
	}}

	require.Nil(t, ex.provFolded)
	require.Nil(t, provChain(b))
	require.NotPanics(t, func() { ex.recordProv(substrate.VertexKey("org", c1NanoID("acme"))) })
	require.Nil(t, ex.provVertexKeys(provChain(b)))
	require.Zero(t, ex.pointReads)

	// The production entry point over the same executor: a key column that
	// would need a read is reported unresolvable, and one that resolves off
	// the event body still does.
	eng := New()
	cr, err := eng.Parse(`MATCH (o:org) RETURN o.key AS anchor`)
	require.NoError(t, err)
	keys, ok := eng.AnchorProjectionKey(
		cr, substrate.VertexKey("org", c1NanoID("acme")), "org", map[string]any{"class": "org"})
	require.True(t, ok)
	require.Equal(t, map[string]any{"anchor": substrate.VertexKey("org", c1NanoID("acme"))}, keys)
}

// TestProvenance_FoldsKeyShapes pins the fold itself over the three Contract #1
// shapes plus the one that names no vertex: an aspect folds to its parent, a
// link to BOTH endpoints (a lens dereferencing a link payload depends on the
// pair the key names), and a key of no shape is dropped rather than guessed at.
func TestProvenance_FoldsKeyShapes(t *testing.T) {
	vtx := substrate.VertexKey("identity", c1NanoID("alice"))
	other := substrate.VertexKey("org", c1NanoID("acme"))

	require.Equal(t, []string{vtx}, appendProvVertexKeys(nil, vtx))
	require.Equal(t, []string{vtx}, appendProvVertexKeys(nil, substrate.AspectKey(vtx, "name")))
	require.Equal(t, []string{vtx, other}, appendProvVertexKeys(nil,
		substrate.LinkKey("identity", c1NanoID("alice"), "worksAt", "org", c1NanoID("acme"))))
	for _, bad := range []string{"", "adj.node", "vtx.identity", c1NanoID("alice"), "vtx.identity.short"} {
		require.Empty(t, appendProvVertexKeys(nil, bad), "key %q names no vertex", bad)
	}
}
