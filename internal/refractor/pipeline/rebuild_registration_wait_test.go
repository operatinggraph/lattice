package pipeline_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
)

// TestRebuild_WaitsForTheConsumerRegistration pins the boot-window race: Run
// creates the durable server-side before the supervisor manages it, so a
// rebuild issued in that window — here, before Run has even begun — must wait
// for the registration rather than be told "not managed" and abandon.
func TestRebuild_WaitsForTheConsumerRegistration(t *testing.T) {
	env := startPipelineEnv(t)

	eng, cr := compileFullRule(t, "MATCH (b:book) RETURN b.key AS key, b.title AS title", []string{"key"})
	_, adpt := newTargetKV(t, env, "rebuild-waits-target", []string{"key"})

	const ruleID = "rebuild-waits-lens"
	p, err := pipeline.New(ruleID, "nats_kv", coreKVBucket, env.adjKV, env.coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)
	p.RunOn(env.conn, specFor(ruleID))

	// Issued BEFORE Run: the only way this rebuild can succeed is by waiting
	// for the registration Run performs.
	rebuilt := make(chan error, 1)
	go func() { rebuilt <- p.Rebuild(context.Background(), false) }()

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); wg.Wait() })

	select {
	case err := <-rebuilt:
		require.NoError(t, err, "a rebuild racing Run's registration must wait for it, not abandon")
	case <-time.After(10 * time.Second):
		t.Fatal("the rebuild neither completed nor failed within 10s")
	}
	waitConsumerSettled(t, env, "refractor-"+ruleID)
}
