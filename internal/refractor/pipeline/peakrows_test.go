package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
)

// TestPeakRowsRingBuffer_EmptyWindowSaysNothing pins the "no sample" case the
// publisher depends on: an empty window must be distinguishable from a window
// holding a genuine zero, or a restart would blank a real observation.
func TestPeakRowsRingBuffer_EmptyWindowSaysNothing(t *testing.T) {
	b := NewPeakRowsRingBuffer(8)
	require.Equal(t, PeakRowsStats{}, b.Snapshot())
	require.Zero(t, b.Snapshot().Count)

	b.Record(0)
	require.Equal(t, PeakRowsStats{Count: 1, Peak: 0}, b.Snapshot(),
		"a recorded zero is a real sample, not an empty window")
}

// TestPeakRowsRingBuffer_ReportsTheMaximumAndDoesNotClear pins the two
// LatencyRingBuffer semantics the gauge inherits: the reported value is the
// window's maximum, and reading leaves the window intact.
func TestPeakRowsRingBuffer_ReportsTheMaximumAndDoesNotClear(t *testing.T) {
	b := NewPeakRowsRingBuffer(8)
	for _, n := range []int{3, 91, 12, 4} {
		b.Record(n)
	}
	require.Equal(t, PeakRowsStats{Count: 4, Peak: 91}, b.Snapshot())
	require.Equal(t, PeakRowsStats{Count: 4, Peak: 91}, b.Snapshot(),
		"reads must not clear the window — successive publishes see the same samples")
}

// TestPeakRowsRingBuffer_SpikeAgesOut is the lifetime assertion: the window is
// ROLLING, so a single pathological evaluation stops being reported once
// enough newer evaluations have displaced it. An all-time monotonic maximum
// would keep answering 5000 forever and would be useless as a gauge.
func TestPeakRowsRingBuffer_SpikeAgesOut(t *testing.T) {
	const capacity = 4
	b := NewPeakRowsRingBuffer(capacity)

	b.Record(5000)
	require.Equal(t, 5000, b.Snapshot().Peak)

	// One window's worth of quiet evaluations, the last of which evicts the
	// spike (the buffer fills first, then overwrites oldest-first).
	for i := 0; i < capacity; i++ {
		b.Record(7)
	}
	snap := b.Snapshot()
	require.Equal(t, capacity, snap.Count, "the window never grows past its capacity")
	require.Equal(t, 7, snap.Peak, "the spike must age out of a rolling window")
}

// TestPeakRowsRingBuffer_SpikeSurvivesUntilDisplaced pins the other half of the
// same lifetime: the spike is not forgotten early. A window that dropped it
// after one newer sample would under-report the cost an operator is chasing.
func TestPeakRowsRingBuffer_SpikeSurvivesUntilDisplaced(t *testing.T) {
	const capacity = 4
	b := NewPeakRowsRingBuffer(capacity)
	b.Record(5000)
	for i := 0; i < capacity-1; i++ {
		b.Record(7)
		require.Equal(t, 5000, b.Snapshot().Peak,
			"the spike stays reported while it is still inside the window")
	}
}

// TestPeakRowsRingBuffer_DefaultCapacity pins the zero/negative fallback.
func TestPeakRowsRingBuffer_DefaultCapacity(t *testing.T) {
	b := NewPeakRowsRingBuffer(0)
	for i := 0; i < DefaultPeakRowsBufferSize+10; i++ {
		b.Record(i)
	}
	require.Equal(t, DefaultPeakRowsBufferSize, b.Snapshot().Count)
}

// TestPipeline_PeakBindingRowsIsSilentUntilAnEvaluationRuns pins the source
// hook's contract: New installs a window, and a lens that has not evaluated
// reports "nothing to say" rather than a peak of zero.
func TestPipeline_PeakBindingRowsIsSilentUntilAnEvaluationRuns(t *testing.T) {
	p := &Pipeline{peakRowsBuf: NewPeakRowsRingBuffer(8)}
	rows, ok := p.PeakBindingRows()
	require.False(t, ok)
	require.Zero(t, rows)

	p.recordPeakBindingRows(ruleengine.EvalStats{PeakBindingRows: 42})
	rows, ok = p.PeakBindingRows()
	require.True(t, ok)
	require.Equal(t, uint64(42), rows)
}

// TestPipeline_PeakBindingRowsNilBufferIsInert pins that a directly
// constructed Pipeline (no New) records nothing and reports nothing rather
// than panicking.
func TestPipeline_PeakBindingRowsNilBufferIsInert(t *testing.T) {
	p := &Pipeline{}
	p.recordPeakBindingRows(ruleengine.EvalStats{PeakBindingRows: 9})
	rows, ok := p.PeakBindingRows()
	require.False(t, ok)
	require.Zero(t, rows)
}

// peakFanOutCypher expands one anchor across its tasks and folds them into a
// single row: the binding set grows to the fan-out and the projection collapses
// back to one, so the pipeline's recorded sample is unambiguously the peak.
const peakFanOutCypher = `MATCH (i:identity {key: $actorKey}) ` +
	`OPTIONAL MATCH (i)<-[:assignedTo]-(t:task) ` +
	`RETURN i.key AS key, collect(DISTINCT t.key) AS tasks`

// TestExecuteFullForActor_RecordsPeakOnSuccessAndOnRefusal pins the pipeline
// half of the operator consumer, in both directions:
//
//	the positive vector — an evaluation that succeeds records the peak its
//	binding set actually reached, not the one row it projected; and
//	the case the gauge exists for — an evaluation REFUSED by the binding-set
//	cap still lands its peak in the window before the pipeline disposes of the
//	error, so the number survives the failure that made it interesting.
func TestExecuteFullForActor_RecordsPeakOnSuccessAndOnRefusal(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping NATS-backed test in short mode")
	}
	coreKV, adjKV, _ := newCollisionKVs(t)
	ctx := context.Background()

	const identityID = "Tcc3JdentityPeakRow1"
	identityKey := "vtx.identity." + identityID
	writeCollisionVertex(t, coreKV, identityKey, "identity", map[string]any{})

	const fanOut = 6
	taskIDs := []string{
		"Tcc3JtaskPeakRowsA11", "Tcc3JtaskPeakRowsA22", "Tcc3JtaskPeakRowsA33",
		"Tcc3JtaskPeakRowsA44", "Tcc3JtaskPeakRowsA55", "Tcc3JtaskPeakRowsA66",
	}
	require.Len(t, taskIDs, fanOut)
	for _, id := range taskIDs {
		writeCollisionVertex(t, coreKV, "vtx.task."+id, "task", map[string]any{})
		buildCollisionEdge(t, adjKV, "assignedTo", "task", id, "identity", identityID)
	}

	eng := full.New()
	cr, err := eng.Parse(peakFanOutCypher)
	require.NoError(t, err)
	nodeProps := map[string]any{"lastModifiedAt": "2026-05-15T10:00:00Z"}

	newPipeline := func(engine *full.Engine) *Pipeline {
		return &Pipeline{
			ruleID:      "rule-peak-rows",
			coreKV:      coreKV,
			adjKV:       adjKV,
			engineKind:  ruleengine.EngineFull,
			fullEngine:  engine,
			fullCR:      cr,
			peakRowsBuf: NewPeakRowsRingBuffer(8),
		}
	}

	ok := newPipeline(eng)
	results, err := ok.executeFullForActor(ctx, ok.ruleState(), identityKey, nodeProps, "")
	require.NoError(t, err)
	require.Len(t, results, 1, "the lens projects one aggregated row per anchor")
	gotPeak, has := ok.PeakBindingRows()
	require.True(t, has, "a completed evaluation must leave a sample in the window")
	require.Equal(t, uint64(fanOut), gotPeak,
		"the recorded peak is the widest binding set (%d), not the projected row count (1)", fanOut)

	const rowCap = 2
	refused := newPipeline(eng.WithMaxBindings(rowCap))
	_, err = refused.executeFullForActor(ctx, refused.ruleState(), identityKey, nodeProps, "")
	require.Error(t, err, "a %d-row fan-out must be refused under a cap of %d", fanOut, rowCap)
	refusedPeak, has := refused.PeakBindingRows()
	require.True(t, has, "a REFUSED evaluation must still leave its peak in the window")
	require.Equal(t, uint64(fanOut), refusedPeak,
		"the refusal must report the row count that tripped the cap, not zero")
}
