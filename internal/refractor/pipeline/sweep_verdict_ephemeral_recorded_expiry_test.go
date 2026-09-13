package pipeline

// The sweep-verdict regression on the AUTH plane, the sibling of
// sweep_verdict_recorded_expiry_test.go's convergence-lens vector.
//
// capabilityEphemeral is the one lens in the corpus whose divergence
// escalation reaches `error` rather than `warning` (health/lattice_heartbeater),
// because its rows are grants. So the deep verify's verdict on it is the one
// that must not be a clock reading: two passes over an unchanged subgraph, one
// before a deadline and one after, have to agree, or the sweep escalates on a
// projection nothing broke.
//
// This drives the REAL shipped capabilityEphemeral cypher, read from
// pkgregistry rather than restated, over one identity with one directly
// assigned open task whose deadline is sweepEndsAt and whose lapse is recorded.
// The `now` parameter is still supplied at both instants — inert, and passed on
// purpose so the agreement below is evidence the lens ignores it rather than
// evidence nobody offered it.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const (
	sweepEphActorID = "SweepEphActorAnchor1"
	sweepEphTaskID  = "SweepEphTaskAnchor12"
)

// shippedCapabilityEphemeralSpec returns the cypher orchestration-base actually
// installs. Reading it from the registry rather than restating it is what makes
// this a regression on the corpus: a lens edited back to a clock fails here.
func shippedCapabilityEphemeralSpec(t *testing.T) string {
	t.Helper()
	for _, def := range pkgregistry.All() {
		for _, l := range def.Lenses {
			if l.CanonicalName == "capabilityEphemeral" {
				require.NotEmpty(t, l.Spec, "capabilityEphemeral must ship a cypher")
				return l.Spec
			}
		}
	}
	require.FailNow(t, "the installed corpus must declare a capabilityEphemeral lens")
	return ""
}

// seedSweepEphemeralGrant writes the actor, one open task assigned to it whose
// deadline is sweepEndsAt, and the freshnessExpiry marker its routing-shape
// target's @at commits when that deadline fires. It returns the actor key.
func seedSweepEphemeralGrant(t *testing.T, coreKV, adjKV *substrate.KV, withMarker bool) string {
	t.Helper()
	actorKey := substrate.VertexKey("identity", sweepEphActorID)
	taskKey := substrate.VertexKey("task", sweepEphTaskID)
	writeCollisionVertex(t, coreKV, actorKey, "identity", map[string]any{"name": "sweep-eph"})
	writeCollisionVertex(t, coreKV, taskKey, "task", map[string]any{
		"status": "open", "expiresAt": sweepEndsAt,
	})
	if withMarker {
		writeSweepAspect(t, coreKV, taskKey, "freshnessExpiry", "freshnessExpiry", map[string]any{
			"expiredAt": sweepEndsAt,
			"byTarget":  map[string]any{"staleAssignedTasks": sweepEndsAt},
		})
	}
	// task = source, identity = target (Contract #1 §1.1).
	buildCollisionEdge(t, adjKV, "assignedTo", "task", sweepEphTaskID, "identity", sweepEphActorID)
	return actorKey
}

// TestSweepVerdict_EphemeralGrantStraddlingADeadlineIsNotADivergence is the
// regression. It asserts on the CLASSIFICATION — what the sweep would report on
// a quiet, unchanged actor — and names the grants column beside it, so a reader
// can see what the verdict stands for.
func TestSweepVerdict_EphemeralGrantStraddlingADeadlineIsNotADivergence(t *testing.T) {
	kvs := newTestKVs(t, "SWEEPEPHADJ", "SWEEPEPHCORE")
	adjKV, coreKV := kvs[0], kvs[1]
	actorKey := seedSweepEphemeralGrant(t, coreKV, adjKV, true)
	spec := shippedCapabilityEphemeralSpec(t)

	stored := projectAtInstant(t, spec, actorKey, sweepBefore, adjKV, coreKV)
	recomputed := projectAtInstant(t, spec, actorKey, sweepAfter, adjKV, coreKV)

	require.Equal(t, divergenceNone, classifyDivergence(stored, recomputed, nil),
		"two deep-verify passes straddling the task's deadline, over an UNCHANGED graph, must agree — "+
			"on this lens a divergent streak escalates to `error`, so a clock reading here is an alert "+
			"about a projection nothing broke")
	require.Equal(t, stored["ephemeralGrants"], recomputed["ephemeralGrants"],
		"the grants column is a function of the subgraph, not of when the pass ran")
}

// TestSweepVerdict_EphemeralGrantIsAPureFunctionOfTheSubgraph is the half that
// stops the agreement above from being satisfied by a row with nothing in it.
// The same marker makes the grant ABSENT at both instants, and its absence is
// the marker's doing rather than either clock's: remove the marker and the same
// two passes list the grant at both instants instead.
func TestSweepVerdict_EphemeralGrantIsAPureFunctionOfTheSubgraph(t *testing.T) {
	spec := shippedCapabilityEphemeralSpec(t)

	realGrants := func(row map[string]any) []string {
		grants, _ := row["ephemeralGrants"].([]any)
		var keys []string
		for _, g := range grants {
			m, _ := g.(map[string]any)
			if tk, ok := m["taskKey"].(string); ok && tk != "" {
				keys = append(keys, tk)
			}
		}
		return keys
	}

	t.Run("lapse recorded: absent at both instants", func(t *testing.T) {
		kvs := newTestKVs(t, "SWEEPEPHADJM", "SWEEPEPHCOREM")
		adjKV, coreKV := kvs[0], kvs[1]
		actorKey := seedSweepEphemeralGrant(t, coreKV, adjKV, true)

		require.Empty(t, realGrants(projectAtInstant(t, spec, actorKey, sweepBefore, adjKV, coreKV)),
			"the recorded lapse retracts the grant BEFORE the wall clock reaches the deadline too — "+
				"the marker decides, so the row does not wait for a clock to catch up")
		require.Empty(t, realGrants(projectAtInstant(t, spec, actorKey, sweepAfter, adjKV, coreKV)),
			"and it is still absent after")
	})

	t.Run("no lapse recorded: listed at both instants", func(t *testing.T) {
		kvs := newTestKVs(t, "SWEEPEPHADJU", "SWEEPEPHCOREU")
		adjKV, coreKV := kvs[0], kvs[1]
		actorKey := seedSweepEphemeralGrant(t, coreKV, adjKV, false)
		taskKey := substrate.VertexKey("task", sweepEphTaskID)

		require.Equal(t, []string{taskKey}, realGrants(projectAtInstant(t, spec, actorKey, sweepBefore, adjKV, coreKV)))
		require.Equal(t, []string{taskKey}, realGrants(projectAtInstant(t, spec, actorKey, sweepAfter, adjKV, coreKV)),
			"with no lapse recorded the grant is still LISTED past its own deadline — the clock-reading form "+
				"dropped it here, and this is the divergence the conversion removes (the delivery axis stays "+
				"closed at the Processor's lookup-time expiresAt check)")
	})
}
