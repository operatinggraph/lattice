// The structural gate on the grouping-key reduction: what binds the generator
// and the engine together.
//
// The reduction pays off only while every generated read-grant producer keeps
// emitting staging clauses whose effective grouping key is the actor alone. It
// is a pure function of the generated cypher, so it fails the moment
// generateProducerSpec emits a shape the analysis cannot prove — a renamed
// carry, an extra column nothing determines — AND the moment the analysis
// itself regresses. A timing or allocation assertion would be the flaky, weaker
// version of the same claim.
package full

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgmgr"
	edgemanifest "github.com/operatinggraph/lattice/packages/edge-manifest"
)

// generatedReadGrantProducers pulls every generated producer spec straight from
// packages/edge-manifest's own Definition through the production compiler, so
// the gate reads the CURRENT generator's actual output rather than a pinned
// approximation of it.
func generatedReadGrantProducers(t testing.TB) map[string]string {
	t.Helper()
	expanded, err := edgemanifest.Package.ExpandReadGrantWalks()
	require.NoError(t, err, "the shipped edge-manifest package must expand cleanly")
	out := map[string]string{}
	for _, l := range expanded.Lenses {
		if !strings.HasSuffix(l.CanonicalName, "ReadGrants") {
			continue
		}
		require.NotEmptyf(t, l.Spec, "%s must carry a generated Spec", l.CanonicalName)
		out[l.CanonicalName] = l.Spec
	}
	require.Equal(t,
		[]string{"edgeManifestProviderReadGrants", "edgeManifestReadGrants", "edgeManifestStaffReadGrants", "edgeManifestTaskReadGrants"},
		sortedNames(namesOf(out)),
		"one producer per declared read-grant domain")
	return out
}

func namesOf(m map[string]string) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

// stagingGroupingKeyFailure returns "" when every GROUPING clause of q reduces
// to exactly the aliases want, and otherwise the reason it did not — the
// diagnosing form a bare bool could not give.
func stagingGroupingKeyFailure(q *Query, want []string) string {
	plans := analyseGrouping(q)
	grouping := 0
	for i, p := range plans {
		if !p.Grouping {
			continue
		}
		grouping++
		if p.Refusal != "" {
			return fmt.Sprintf("grouping clause %d was refused: %s", i, p.Refusal)
		}
		if !equalStringSlices(p.Key, want) {
			return fmt.Sprintf("grouping clause %d groups on %v, not %v", i, p.Key, want)
		}
	}
	if grouping == 0 {
		return "the query has no grouping clause at all"
	}
	return ""
}

// countRedundant totals the items the analysis marks redundant across q.
func countRedundant(q *Query) int {
	n := 0
	for _, mask := range analyseGroupingRedundancy(q) {
		for _, r := range mask {
			if r {
				n++
			}
		}
	}
	return n
}

// countGroupingClauses totals q's clauses that actually partition their rows.
func countGroupingClauses(q *Query) int {
	n := 0
	for _, p := range analyseGrouping(q) {
		if p.Grouping {
			n++
		}
	}
	return n
}

// producerNamePerDomain maps each declared read-grant domain to the canonical
// name of the producer lens the generator emitted for it, read off the EXPANDED
// definition rather than re-derived from the domain name.
//
// The generator honours ReadGrantDomainSpec.CanonicalName when a domain sets
// one, so a gate that rebuilt `<domain>ReadGrants` itself would key its map on
// a name no producer carries the moment a domain overrides. The join is the one
// thing a producer's identity is truly pinned to: its Output key pattern is
// `cap-read.<domain>.{actorSuffix}` whatever the lens is called.
func producerNamePerDomain(t testing.TB) map[string]string {
	t.Helper()
	expanded, err := edgemanifest.Package.ExpandReadGrantWalks()
	require.NoError(t, err, "the shipped edge-manifest package must expand cleanly")
	out := make(map[string]string, len(expanded.ReadGrantDomains))
	for _, d := range expanded.ReadGrantDomains {
		want := "cap-read." + d.Name + ".{actorSuffix}"
		for _, l := range expanded.Lenses {
			if l.ProjectionKind != pkgmgr.ActorAggregateProjectionKind || l.Output == nil {
				continue
			}
			if l.Output.OutputKeyPattern != want {
				continue
			}
			require.NotContainsf(t, out, d.Name,
				"two actorAggregate lenses claim domain %q: %s and %s", d.Name, out[d.Name], l.CanonicalName)
			out[d.Name] = l.CanonicalName
		}
		require.Containsf(t, out, d.Name,
			"domain %q declares no producer writing %s", d.Name, want)
	}
	return out
}

// declaredWalkCounts maps each generated producer to the number of Walks its
// domain collects, read from the same Definition the generator compiles. It is
// the exact form of "one staging clause per declared walk": a floor would let a
// producer that silently lost a walk still pass, and the triangular shedding
// equality beside it is vacuous at a single stage.
func declaredWalkCounts(t testing.TB) map[string]int {
	t.Helper()
	producer := producerNamePerDomain(t)
	out := make(map[string]int, len(producer))
	for _, name := range producer {
		out[name] = 0
	}
	for _, l := range edgemanifest.Package.Lenses {
		for _, w := range l.Walks {
			name, declared := producer[w.GrantDomain]
			require.Truef(t, declared,
				"lens %s names GrantDomain %q, which declares no producer", l.CanonicalName, w.GrantDomain)
			out[name]++
		}
	}
	return out
}

// armedReadGrantProducerNames is generatedReadGrantProducers filtered to the
// ones whose staging actually sheds an accumulator. A domain with a single
// Walk stages one clause with nothing carried before it to drop, so forcing
// its reduction off and comparing runs one code path against itself —
// executeBothWays's own guard refuses that differential outright, which is
// why the equivalence tests below only run it over the armed population.
func armedReadGrantProducerNames(t *testing.T, specs map[string]string) []string {
	t.Helper()
	names := []string{}
	for _, name := range sortedNames(namesOf(specs)) {
		if countRedundant(mustParseQuery(t, specs[name])) > 0 {
			names = append(names, name)
		}
	}
	return names
}

// TestGeneratedReadGrantProducers_GroupOnTheActorAlone is the gate. Each
// generated producer's staging clauses must group on `identity` and nothing
// else, and stage k must shed all k accumulators it carries — so the total
// number of renderings the reduction removes is exactly the triangular number
// of the walk count, and no accumulator is ever rendered into a key.
func TestGeneratedReadGrantProducers_GroupOnTheActorAlone(t *testing.T) {
	for name, spec := range generatedReadGrantProducers(t) {
		t.Run(name, func(t *testing.T) {
			q := mustParseQuery(t, spec)
			require.Emptyf(t, stagingGroupingKeyFailure(q, []string{"identity"}),
				"%s no longer reduces to an actor-only grouping key — either the generator "+
					"emits a shape the analysis cannot prove, or the analysis regressed:\n%s",
				name, spec)

			stages := countGroupingClauses(q)
			require.Equalf(t, declaredWalkCounts(t)[name], stages,
				"%s stages one WITH per declared walk", name)
			require.Equalf(t, stages*(stages-1)/2, countRedundant(q),
				"stage k of %s carries k accumulators and every one of them must be shed", name)
		})
	}
}

// TestGeneratedReadGrantProducers_GateFailsOnAPerturbedProducer is the gate's
// own positive vector: a gate that cannot fail pins nothing. Each perturbation
// below is a shape the generator could plausibly drift into, applied to the
// real generated spec, and each must be caught.
func TestGeneratedReadGrantProducers_GateFailsOnAPerturbedProducer(t *testing.T) {
	spec := generatedReadGrantProducers(t)["edgeManifestReadGrants"]
	require.NotEmpty(t, spec)
	require.Empty(t, stagingGroupingKeyFailure(mustParseQuery(t, spec), []string{"identity"}),
		"the unperturbed producer must pass, or the perturbations below prove nothing")

	for _, tc := range []struct {
		name        string
		from, to    string
		wantFailure string
	}{
		{
			name:        "the actor carry is renamed",
			from:        "WITH identity, grantSlice0,",
			to:          "WITH identity AS actor, grantSlice0,",
			wantFailure: "refused",
		},
		{
			name:        "an accumulator is renamed",
			from:        "WITH identity, grantSlice0,",
			to:          "WITH identity, grantSlice0 AS acc,",
			wantFailure: "groups on",
		},
		{
			name:        "a column nothing determines joins the key",
			from:        "WITH identity, grantSlice0,",
			to:          "WITH identity, identity.key AS extra, grantSlice0,",
			wantFailure: "groups on",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Perturb the FIRST staging clause that carries an accumulator;
			// the rewrite must actually land, or the subtest proves nothing.
			perturbed := strings.Replace(spec, tc.from, tc.to, 1)
			require.NotEqualf(t, spec, perturbed,
				"the generated producer no longer contains %q, so this perturbation rewrote nothing", tc.from)

			failure := stagingGroupingKeyFailure(mustParseQuery(t, perturbed), []string{"identity"})
			require.NotEmptyf(t, failure,
				"the gate must catch this drift, and did not:\n%s", perturbed)
			require.Contains(t, failure, tc.wantFailure)
		})
	}
}
