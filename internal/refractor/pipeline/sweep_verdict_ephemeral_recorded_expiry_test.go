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
	orchestrationbase "github.com/operatinggraph/lattice/packages/orchestration-base"
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
			"byTarget":  map[string]any{orchestrationbase.StaleAssignedTasksTarget: sweepEndsAt},
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
			"with no lapse recorded the grant is LISTED past its own deadline at both instants — a clock-reading "+
				"form drops it at the later one, which is the divergence this lens does not have (the delivery "+
				"axis stays closed at the Processor's lookup-time expiresAt check)")
	})
}

// ephemeralClockReadingSpec is the three-arm shipped pattern with the deadline
// verdict taken from the CLOCK instead of the recorded lapse. It exists to
// measure the comparator: without it the two tests above are satisfied just as
// well by a lens with nothing time-dependent in it, or by a classifyDivergence
// that has stopped comparing.
const ephemeralClockReadingSpec = `
MATCH (identity:identity {key: $actorKey})

OPTIONAL MATCH (identity)<-[:assignedTo]-(task:task)
  WHERE task.data.status = 'open' AND task.data.expiresAt > $now
OPTIONAL MATCH (task)-[:forOperation]->(op)
OPTIONAL MATCH (task)-[:scopedTo]->(tgt)

OPTIONAL MATCH (identity)<-[:reportsTo]-(report:identity)<-[:assignedTo]-(task2:task)
  WHERE task2.data.status = 'open' AND task2.data.expiresAt > $now
OPTIONAL MATCH (task2)-[:forOperation]->(op2)
OPTIONAL MATCH (task2)-[:scopedTo]->(tgt2)

OPTIONAL MATCH (identity)-[:holdsRole]->(role:role)<-[:queuedFor]-(task3:task)
  WHERE task3.data.status = 'open' AND task3.data.expiresAt > $now
OPTIONAL MATCH (task3)-[:forOperation]->(op3)
OPTIONAL MATCH (task3)-[:scopedTo]->(tgt3)

RETURN
  identity.key AS actorKey,
  collect(DISTINCT {
    source: "task",
    taskKey: task.key,
    operationType: op.data.operationType,
    target: tgt.key,
    expiresAt: task.data.expiresAt
  }) + collect(DISTINCT {
    source: "task",
    taskKey: task2.key,
    operationType: op2.data.operationType,
    target: tgt2.key,
    expiresAt: task2.data.expiresAt
  }) + collect(DISTINCT {
    source: "task",
    taskKey: task3.key,
    operationType: op3.data.operationType,
    target: tgt3.key,
    expiresAt: task3.data.expiresAt
  }) AS ephemeralGrants
`

// TestSweepVerdict_EphemeralClockReadingFormStillDiverges is the DISCRIMINATION
// half. It runs the same fixture and the same two instants against a
// clock-reading form of this lens; classifyDivergence must answer
// divergenceContent, and the grants column must be the thing that moved — on an
// unchanged graph, with no write between the two passes.
//
// The fixture carries NO marker here: the clock form has no marker to read, and
// the population it disagrees about is precisely the open-and-lapsed task.
func TestSweepVerdict_EphemeralClockReadingFormStillDiverges(t *testing.T) {
	kvs := newTestKVs(t, "SWEEPEPHADJC", "SWEEPEPHCOREC")
	adjKV, coreKV := kvs[0], kvs[1]
	actorKey := seedSweepEphemeralGrant(t, coreKV, adjKV, false)
	taskKey := substrate.VertexKey("task", sweepEphTaskID)

	stored := projectAtInstant(t, ephemeralClockReadingSpec, actorKey, sweepBefore, adjKV, coreKV)
	recomputed := projectAtInstant(t, ephemeralClockReadingSpec, actorKey, sweepAfter, adjKV, coreKV)

	require.Equal(t, divergenceContent, classifyDivergence(stored, recomputed, nil),
		"a clock-reading form must diverge over an unchanged graph — otherwise the agreement asserted in "+
			"the sibling tests is satisfied by a fixture with nothing time-dependent in it, or by a "+
			"comparator that compares nothing")

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
	require.Equal(t, []string{taskKey}, realGrants(stored),
		"before the deadline the clock form lists the grant")
	require.Empty(t, realGrants(recomputed),
		"after it the same form drops the grant on the clock alone, with no write to the graph between "+
			"the two passes — that flip is what escalates to `error` on this lens's plane")
}
