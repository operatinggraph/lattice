package pipeline

// The sweep-verdict regression for expiry-as-a-recorded-fact: the test that
// proves the payoff, at the seam where the harm was.
//
// The sweep's deep verify recomputes a stored row and hands the pair to
// classifyDivergence. A convergence lens that computes its gap column and its
// freshUntil from $now makes that verdict a CLOCK READING: two passes over an
// UNCHANGED graph, one before a deadline and one after, produce different rows,
// so the sweep reports a divergence and "heals" a projection nothing broke.
// Every such heal is a write, an alert-shaped log line, and a claim about
// correctness that carries no information.
//
// This drives the REAL shipped pastDueAppointments cypher — the corpus, through
// pkgregistry, not a fixture cypher — twice over one unchanged appointment, at
// two wall-clock instants straddling its endsAt, and asserts the two rows
// classify divergenceNone.
//
// The second test is the DISCRIMINATION half, and it is what stops the first
// from degenerating into "any two rows are equal": a clock-reading form of the
// same lens
//
//	(a.schedule.data.endsAt <= $now)                          -- gap column
//	CASE WHEN … AND (a.schedule.data.endsAt > $now) THEN …    -- freshUntil
//
// over the SAME fixture at the SAME two instants flips
// missing_noshow_transition false→true AND freshUntil endsAt→null, and
// classifyDivergence answers divergenceContent. Both halves are measured here,
// so neither the recorded-fact verdict nor the comparator is taken on trust.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/pkgregistry"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// The two deep-verify instants, and the deadline they straddle.
const (
	sweepEndsAt   = "2026-06-30T12:00:00Z"
	sweepBefore   = "2026-06-30T11:00:00Z"
	sweepAfter    = "2026-06-30T13:00:00Z"
	sweepAnchorID = "SweepPastDueApptAnc1"
)

// shippedPastDueAppointmentsSpec returns the cypher clinic-reminders actually
// installs. Reading it from the registry rather than restating it is what makes
// this a regression on the corpus: a lens edited back to a clock fails here.
func shippedPastDueAppointmentsSpec(t *testing.T) string {
	t.Helper()
	for _, def := range pkgregistry.All() {
		for _, l := range def.Lenses {
			if l.CanonicalName == "pastDueAppointments" {
				require.NotEmpty(t, l.Spec, "pastDueAppointments must ship a cypher")
				return l.Spec
			}
		}
	}
	require.FailNow(t, "the installed corpus must declare a pastDueAppointments lens")
	return ""
}

// seedSweepAppointment writes one non-terminal appointment whose visit ends at
// sweepEndsAt, with NO freshnessExpiry marker: the state a real anchor sits in
// between two sweep laps when nothing about it has changed.
func seedSweepAppointment(t *testing.T, coreKV *substrate.KV) string {
	t.Helper()
	key := substrate.VertexKey("appointment", sweepAnchorID)
	writeCollisionVertex(t, coreKV, key, "appointment", map[string]any{})
	writeSweepAspect(t, coreKV, key, "schedule", "appointmentSchedule", map[string]any{
		"startsAt": "2026-06-30T11:30:00Z", "endsAt": sweepEndsAt,
	})
	writeSweepAspect(t, coreKV, key, "status", "appointmentStatus", map[string]any{"value": "scheduled"})
	return key
}

func writeSweepAspect(t *testing.T, coreKV *substrate.KV, ownerKey, local, class string, data map[string]any) {
	t.Helper()
	key := ownerKey + "." + local
	raw, err := json.Marshal(map[string]any{
		"key": key, "class": class, "vertexKey": ownerKey, "localName": local,
		"isDeleted": false, "data": data,
	})
	require.NoError(t, err)
	_, err = coreKV.Put(context.Background(), key, raw)
	require.NoError(t, err)
}

// projectAtInstant runs one deep-verify pass: the spec, the anchor, and the
// wall-clock instant the pass happened to run at, supplied exactly as
// executeFullForActor supplies it.
func projectAtInstant(t *testing.T, spec, anchorKey, now string, adjKV, coreKV *substrate.KV) map[string]any {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(spec)
	require.NoError(t, err)
	out, err := eng.ExecuteWith(context.Background(), cr, ruleengine.EventContext{Parameters: map[string]any{
		"actorKey":    anchorKey,
		"now":         now,
		"projectedAt": now,
	}}, adjKV, coreKV)
	require.NoError(t, err)
	require.Len(t, out, 1, "the anchored lens projects exactly one row")
	return out[0].Values
}

// TestSweepVerdict_StraddlingADeadlineIsNotADivergence is the regression. It
// asserts on the CLASSIFICATION, not on the row: the row converges either way
// once the marker lands, which is what makes a row-only assertion a false pass.
// What the sweep reports on a quiet, unchanged anchor is the thing that moved.
func TestSweepVerdict_StraddlingADeadlineIsNotADivergence(t *testing.T) {
	kvs := newTestKVs(t, "SWEEPADJ", "SWEEPCORE")
	adjKV, coreKV := kvs[0], kvs[1]
	anchorKey := seedSweepAppointment(t, coreKV)
	spec := shippedPastDueAppointmentsSpec(t)

	stored := projectAtInstant(t, spec, anchorKey, sweepBefore, adjKV, coreKV)
	recomputed := projectAtInstant(t, spec, anchorKey, sweepAfter, adjKV, coreKV)

	require.Equal(t, divergenceNone, classifyDivergence(stored, recomputed),
		"two deep-verify passes straddling a deadline, over an UNCHANGED graph, must agree — "+
			"a lens that reads a clock reports a divergence here and the sweep 'heals' a projection nothing broke")

	// Name the columns a clock moves, so a reader can see what the classification
	// is standing in for.
	require.Equal(t, stored["missing_noshow_transition"], recomputed["missing_noshow_transition"],
		"the gap column is a function of the subgraph, not of when the pass ran")
	require.Equal(t, stored["freshUntil"], recomputed["freshUntil"],
		"and so is freshUntil — converting only the gap column leaves this half moving")
}

// TestSweepVerdict_ClockReadingFormStillDiverges is the discrimination half. It
// runs the same fixture and the same two instants against a clock-reading form
// of the same lens; classifyDivergence must answer divergenceContent.
//
// Without it the assertion above proves nothing: two rows from a lens with no
// time-dependent column at all would satisfy it just as well, and so would a
// comparator that had stopped comparing.
func TestSweepVerdict_ClockReadingFormStillDiverges(t *testing.T) {
	kvs := newTestKVs(t, "SWEEPADJ", "SWEEPCORE")
	adjKV, coreKV := kvs[0], kvs[1]
	anchorKey := seedSweepAppointment(t, coreKV)

	const clockReadingSpec = `MATCH (a:appointment {key: $actorKey})
RETURN
  a.key AS actorKey,
  a.key AS entityKey,
  a.schedule.data.endsAt AS endsAt,
  a.status.data.value AS status,
  CASE WHEN (a.status.data.value <> 'completed') AND (a.status.data.value <> 'cancelled') AND (a.status.data.value <> 'noShow') AND (a.schedule.data.endsAt > $now) THEN a.schedule.data.endsAt ELSE null END AS freshUntil,
  ((a.status.data.value <> 'completed') AND (a.status.data.value <> 'cancelled') AND (a.status.data.value <> 'noShow') AND (a.schedule.data.endsAt <= $now)) AS missing_noshow_transition,
  ((a.status.data.value <> 'completed') AND (a.status.data.value <> 'cancelled') AND (a.status.data.value <> 'noShow') AND (a.schedule.data.endsAt <= $now)) AS violating`

	stored := projectAtInstant(t, clockReadingSpec, anchorKey, sweepBefore, adjKV, coreKV)
	recomputed := projectAtInstant(t, clockReadingSpec, anchorKey, sweepAfter, adjKV, coreKV)

	require.Equal(t, divergenceContent, classifyDivergence(stored, recomputed),
		"a clock-reading form must diverge over an unchanged graph — otherwise the assertion "+
			"in the sibling test is satisfied by a fixture with nothing time-dependent in it")
	require.Equal(t, false, stored["missing_noshow_transition"])
	require.Equal(t, true, recomputed["missing_noshow_transition"],
		"the gap column flipped on the clock alone, with no write to the graph between the two passes")
	require.Equal(t, sweepEndsAt, stored["freshUntil"])
	require.Nil(t, recomputed["freshUntil"],
		"and freshUntil flips with it — which is why a gap column alone is not the whole clock reading")
}
