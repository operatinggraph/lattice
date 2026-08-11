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
		[]string{"edgeManifestProviderReadGrants", "edgeManifestReadGrants", "edgeManifestStaffReadGrants"},
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
			require.GreaterOrEqual(t, stages, 3, "%s should stage one WITH per declared walk", name)
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
