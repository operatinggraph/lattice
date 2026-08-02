// Auth-plane narrowing census — auth-plane-projection-latency-design.md §4.2.
//
// The unit suite in internal/refractor/pipeline hand-assigns a label set to
// prove the predicate. This file closes the other half: it drives the REAL
// shipped cypher of every auth-plane actor-aggregate lens through the same
// UseFullEngineBranches derivation production uses, and pins the resulting
// (eligible, labels) verdict.
//
// It exists because Increment 1's gate arms far wider than the design's own
// corpus paragraph (§4.6) discusses. A cypher edit that quietly changes a
// lens's verdict — narrowing one that must stay broad, or dropping a type out
// of a set the fan-out arms judge against — is an authorization change, and it
// should fail here rather than in Capability KV.
package refractor_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	rbacdomain "github.com/operatinggraph/lattice/packages/rbac-domain"
)

// narrowingVerdict derives the gate's verdict for a cypher + anchor type the way
// activation does: UseFullEngine for the label set, then the two install-time
// declarations InstallActorAggregate makes (pattern-closure and the sweep plan).
// Anything it reports ineligible is ineligible for a reason the label set alone
// cannot express.
func narrowingVerdict(t *testing.T, lensID, cypher, anchorType string) (map[string]struct{}, bool) {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(cypher)
	require.NoError(t, err, "%s must parse", lensID)

	adpt, err := adapter.New(nil, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(lensID, "nats_kv", bootstrap.CoreKVBucket, nil, nil, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	p.SetActorEnumerator(pipeline.NewActorEnumerator(nil, nil, anchorType))
	p.SetPatternClosedOutput(true)
	p.SetSweepPlan(pipeline.SweepPlan{AnchorType: anchorType, KeyPrefix: lensID + "."})
	return p.ActorAwareNarrowingLabels()
}

// TestAuthPlaneLenses_NarrowingVerdict pins the verdict for every auth-plane
// actor-aggregate lens the gate reaches. Each expected label set was checked by
// hand against the lens's cypher: every pattern node carries a label, every
// traversed edge has both endpoint types in the set, every property read is a
// root field or an aspect whose parent type is in the set, and the anchor type
// is in the set — which is what makes the anchor's soft-delete undroppable.
func TestAuthPlaneLenses_NarrowingVerdict(t *testing.T) {
	cases := []struct {
		lensID     string
		cypher     string
		anchorType string
		wantLabels map[string]struct{}
	}{
		{
			// rbac-domain's role-derived grant projection — the lens the design
			// is about.
			lensID:     "CensusRbacRoLes99999",
			cypher:     rbacCapabilityRolesSpec(t),
			anchorType: "identity",
			wantLabels: map[string]struct{}{"identity": {}, "role": {}, "permission": {}},
		},
		{
			// The kernel cap.<actor> doc. Its grant set is a RETURN literal, not
			// a grantedBy/permission walk, so `permission` is legitimately absent
			// — core references no rbac permission vocabulary (Contract #7 §7.7).
			lensID:     "CensusKerneLCap99999",
			cypher:     bootstrap.CapabilityLensDefinition().CypherRule,
			anchorType: "identity",
			wantLabels: map[string]struct{}{"identity": {}, "role": {}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.lensID, func(t *testing.T) {
			labels, eligible := narrowingVerdict(t, tc.lensID, tc.cypher, tc.anchorType)
			require.True(t, eligible,
				"this lens's shipped cypher must still derive an exhaustive label set containing its anchor")
			require.Equal(t, tc.wantLabels, labels,
				"the fan-out arms judge every event against this set — a change here is an authorization change")
		})
	}
}

// TestNonExhaustiveAuthPlaneLenses_StayBroad is the negative half: the auth-plane
// lenses whose patterns carry an unlabeled node must never narrow, whatever else
// their installation declares. capabilityEphemeral and myTasks each leave their
// operation/target positions unlabeled; capabilityServiceAccess reaches
// instanceOf/unavailableAt only through a negated WHERE.
func TestNonExhaustiveAuthPlaneLenses_StayBroad(t *testing.T) {
	for _, name := range []string{"capabilityEphemeral", "myTasks", "capabilityServiceAccess", "orphanedTaskGrants"} {
		t.Run(name, func(t *testing.T) {
			cypher, anchor, found := actorAggregateSpecByName(t, name)
			if !found {
				t.Skipf("%s is not declared in the registry snapshot this test reads", name)
			}
			_, eligible := narrowingVerdict(t, "CensusBroad999999999", cypher, anchor)
			require.False(t, eligible,
				"a non-exhaustive pattern must keep the unconditional fan-out — any type may bind")
		})
	}
}

// rbacCapabilityRolesSpec returns rbac-domain's shipped capabilityRoles cypher.
func rbacCapabilityRolesSpec(t *testing.T) string {
	t.Helper()
	for _, l := range rbacdomain.Lenses() {
		if l.CanonicalName == "capabilityRoles" {
			return l.Spec
		}
	}
	require.FailNow(t, "rbac-domain must declare a capabilityRoles lens")
	return ""
}

// actorAggregateSpecByName finds a shipped actor-aggregate lens's cypher and
// anchor type by canonical name, across every registered package.
func actorAggregateSpecByName(t *testing.T, name string) (cypher, anchorType string, found bool) {
	t.Helper()
	for _, def := range pkgregistry.All() {
		for _, l := range def.Lenses {
			if l.CanonicalName != name || l.Output == nil || l.Spec == "" {
				continue
			}
			return l.Spec, l.Output.AnchorType, true
		}
	}
	return "", "", false
}
