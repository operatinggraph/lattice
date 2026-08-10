package pipeline_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// parkingAdapter blocks inside Upsert until released. The pipeline's handler
// calls the adapter inline on the supervised pump goroutine, so a blocked Upsert
// is a parked pump: it cannot act on a reopen request until the handler returns.
type parkingAdapter struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newParkingAdapter() *parkingAdapter {
	return &parkingAdapter{entered: make(chan struct{}, 8), release: make(chan struct{})}
}

func (a *parkingAdapter) Upsert(context.Context, map[string]any, map[string]any, uint64) error {
	select {
	case a.entered <- struct{}{}:
	default:
	}
	<-a.release
	return nil
}

func (a *parkingAdapter) Delete(context.Context, map[string]any, uint64) error { return nil }
func (a *parkingAdapter) Probe(context.Context) error                         { return nil }
func (a *parkingAdapter) Close() error                                        { return nil }
func (a *parkingAdapter) releaseAll()                                         { a.once.Do(func() { close(a.release) }) }

// TestRebuild_HoldsUntilTheConsumerPumpHasReopened pins the one line that makes
// a rebuild's concurrency slot span the whole durable handover.
//
// Rebuild recreates the durable and then has to wait for the pump to re-open
// against the replacement, because the caller's slot — taken so the server is
// not asked for many simultaneous durable transitions — is released the moment
// Rebuild returns. A Rebuild that returned after the recreate alone would hand
// that slot back mid-handover, and the bound would cover less than it names.
//
// Nothing else in the suite sees this: every other rebuild test is satisfied by
// the recreated durable, which a non-waiting reset produces just as well. So the
// assertion has to be the BLOCKING itself. The adapter parks the pump inside a
// projection write, which is the one state in which "recreated" and "reopened"
// are far apart in time; Rebuild must not return until the pump is freed.
func TestRebuild_HoldsUntilTheConsumerPumpHasReopened(t *testing.T) {
	env := startPipelineEnv(t)
	const ruleID = "rule-reopen-barrier"

	eng, cr := compileFullRule(t,
		"MATCH (a:agreement {key: $actorKey}) RETURN a.id AS agreement_id",
		[]string{"agreement_id"})

	adpt := newParkingAdapter()

	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	startPipeline(t, env, p, ruleID)
	// Registered AFTER startPipeline so it runs BEFORE the pipeline teardown:
	// that teardown waits for the pump goroutine, which cannot exit while the
	// adapter holds it. Without this ordering a failing assertion below would
	// hang the package instead of reporting.
	t.Cleanup(adpt.releaseAll)

	// Park the pump: one matching vertex, whose projection write never returns.
	putNode(t, env.coreKV, "vtx.agreement."+sentinelAgreementErr1,
		map[string]any{"id": "parked", "isDeleted": false})
	select {
	case <-adpt.entered:
	case <-time.After(15 * time.Second):
		t.Fatal("the pipeline never reached the adapter, so the pump is not parked")
	}

	done := make(chan error, 1)
	go func() { done <- p.Rebuild(context.Background(), false) }()

	select {
	case err := <-done:
		t.Fatalf("Rebuild returned (%v) while the consumer's pump was parked and could not have re-opened — "+
			"its slot is being handed back mid-handover", err)
	case <-time.After(1500 * time.Millisecond):
	}

	adpt.releaseAll()

	select {
	case err := <-done:
		require.NoError(t, err, "a rebuild whose pump re-opened must succeed")
	case <-time.After(30 * time.Second):
		t.Fatal("Rebuild never returned after the pump was free to re-open")
	}
}
