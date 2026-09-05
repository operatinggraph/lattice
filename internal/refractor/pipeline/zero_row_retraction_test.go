package pipeline

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// zeroRowActorDeleteKey mirrors orphanedTaskGrantsSpec's doc-mode output key
// shape (packages/orchestration-base/lenses.go): orphanedTaskGrants.<type>.<id>.
func zeroRowActorDeleteKey(actorKey string) string {
	return "orphanedTaskGrants." + strings.TrimPrefix(actorKey, "vtx.")
}

// zeroRowAnchorFromKey is zeroRowActorDeleteKey's inverse, the pair the
// convergence sweep needs to recover the anchor a target key was built for.
func zeroRowAnchorFromKey(targetKey string) (string, bool) {
	rest, ok := strings.CutPrefix(targetKey, "orphanedTaskGrants.")
	if !ok {
		return "", false
	}
	actorKey := "vtx." + rest
	vtxType, _, parsed := substrate.ParseVertexKey(actorKey)
	if !parsed || vtxType != "task" {
		return "", false
	}
	return actorKey, true
}

// newZeroRowPipeline builds an actor-aggregate pipeline over a REAL compiled
// cypher whose anchor MATCH itself carries a filtering WHERE — the
// orphanedTaskGrants shape (packages/orchestration-base/lenses.go): the query
// returns ZERO rows, not one degenerate row, once the task's status leaves
// 'open'. The envelope is a plain passthrough (the shape under test is the
// zero-row-retraction wiring installed directly via SetZeroRowRetraction, not
// driver.go's EnvelopeFn/realness-filter path, which never engages for a
// query with no RealnessFilter).
func newZeroRowPipeline(t *testing.T, adpt adapter.Adapter) *Pipeline {
	t.Helper()
	coreKV, adjKV := newDeleteKeyKV(t)
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (t:task {key: $actorKey})
  WHERE t.data.status = 'open'
RETURN t.key AS actorKey, t.key AS taskKey
`)
	require.NoError(t, err)
	fullCR, isFull := cr.(*full.CompiledRule)
	require.True(t, isFull)
	fullCR.KeyColumns = []string{"actorKey"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	p := &Pipeline{
		ruleID:          "zero-row-rule",
		coreKV:          coreKV,
		adjKV:           adjKV,
		engineKind:      ruleengine.EngineFull,
		fullEngine:      eng,
		fullCR:          fullCR,
		actorEnumerator: NewActorEnumerator(adjKV, coreKV, "task"),
		adpt:            adpt,
	}
	p.SetEnvelopeFn(func(row, keys, params map[string]any) (map[string]any, map[string]any, error) {
		return row, keys, nil
	})
	p.SetActorDeleteKey(zeroRowActorDeleteKey)
	p.SetZeroRowRetraction(true)
	return p
}

// writeTaskAnchor seeds a live task vertex with the given status, carrying the
// commit provenance the universal Core KV envelope records (Contract #1 §1.3)
// so projectedAtFromProvenance can derive a deterministic value.
func writeTaskAnchor(t *testing.T, p *Pipeline, taskKey, status string) {
	t.Helper()
	body := map[string]any{
		"key": taskKey, "class": "task",
		"data":           map[string]any{"status": status},
		"createdAt":      "2026-07-25T00:00:00Z",
		"lastModifiedAt": "2026-07-25T00:00:00Z",
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = p.coreKV.Put(context.Background(), taskKey, data)
	require.NoError(t, err)
}

// TestZeroRowRetraction_NoRowEverProjected_DoesNotManufactureATombstone is the
// no-manufacture case: an anchor whose filtering WHERE never once matched —
// the exact shape activation replay (DeliverLastPerSubject over every anchor
// subject) hands this evaluation for every task that was never 'open'. The
// target never held a row (present:false), so zeroRowDeleteKey's presence
// check must decline to emit a delete rather than manufacture one.
func TestZeroRowRetraction_NoRowEverProjected_DoesNotManufactureATombstone(t *testing.T) {
	const zeroRowTask = "vtx.task.Tzr1TaskaaaaaaaaaaaZ"
	adpt := &recordingAdapter{present: false}
	p := newZeroRowPipeline(t, adpt)
	writeTaskAnchor(t, p, zeroRowTask, "closed")

	results, err := p.executeFullForActor(context.Background(), p.ruleState(), zeroRowTask,
		map[string]any{"lastModifiedAt": "2026-07-25T00:00:00Z"}, "")
	require.NoError(t, err)
	require.Empty(t, results, "an anchor that never held a row must not manufacture a tombstone key")
	require.Empty(t, adpt.deletes)
	require.Equal(t, 1, adpt.getCalled, "the presence check must have run")
}

// TestZeroRowRetraction_ArmedOnlyWhenTheFlagIsSet proves the mechanism is
// strictly opt-in: a pipeline whose evaluation returns zero rows but which
// never armed SetZeroRowRetraction must leave the existing key (and every
// other lens's behavior) untouched.
func TestZeroRowRetraction_ArmedOnlyWhenTheFlagIsSet(t *testing.T) {
	const zeroRowTask = "vtx.task.Tzr3TaskaaaaaaaaaaaZ"
	adpt := &recordingAdapter{present: true, stored: map[string]any{"key": zeroRowActorDeleteKey(zeroRowTask)}}
	p := newZeroRowPipeline(t, adpt)
	p.SetZeroRowRetraction(false)
	writeTaskAnchor(t, p, zeroRowTask, "closed")

	results, err := p.executeFullForActor(context.Background(), p.ruleState(), zeroRowTask,
		map[string]any{"lastModifiedAt": "2026-07-25T00:00:00Z"}, "")
	require.NoError(t, err)
	require.Empty(t, results, "an unarmed pipeline must not retract on zero rows")
	require.Zero(t, adpt.getCalled, "the presence check must not even run when the flag is off")
}

// TestZeroRowRetraction_ConvergenceSweep_HealsAStaleRowViaReproject is the
// sweep deep-verify heal case: a stale row already sits in the target for a
// task whose anchor is LIVE but no longer matches (status flipped away from
// 'open'). No CDC event ever reaches this pipeline for it — RunSweep never
// consumes the Core KV stream at all, only the sweep's own per-actor
// Reproject re-evaluates directly against Core KV — so only the convergence
// sweep's Reproject path can discover and heal it.
func TestZeroRowRetraction_ConvergenceSweep_HealsAStaleRowViaReproject(t *testing.T) {
	const zeroRowTask = "vtx.task.Tzr2TaskaaaaaaaaaaaZ"
	staleKey := zeroRowActorDeleteKey(zeroRowTask)
	adpt := &listingAdapter{keys: []string{staleKey}}
	adpt.present = true
	adpt.stored = map[string]any{"key": staleKey, "taskKey": zeroRowTask}
	p := newZeroRowPipeline(t, adpt)
	writeTaskAnchor(t, p, zeroRowTask, "closed")
	// The ordering token a real consumer would have established by having
	// applied at least one event; captured BEFORE Reproject re-evaluates,
	// exactly as capability-projection-reconciliation-design.md §3.1 requires.
	p.recordAppliedSeq(555)

	p.SetSweepPlan(SweepPlan{
		AnchorType:    "task",
		AnchorFromKey: zeroRowAnchorFromKey,
		KeyPrefix:     "orphanedTaskGrants.",
		Interval:      time.Hour, // ticks are driven explicitly by this test
		Batch:         10,
	})
	sw := p.Sweeper()
	require.NotNil(t, sw)

	sw.pass(context.Background())

	require.Len(t, adpt.deletes, 1, "the sweep's Reproject path must tombstone the stale row")
	require.Equal(t, map[string]any{"key": staleKey}, adpt.deletes[0].keys)
	require.Equal(t, uint64(555), adpt.deletes[0].seq)
	require.Equal(t, uint64(1), sw.Status().Reconciled)
	require.Zero(t, sw.Status().FailingActors)
}
