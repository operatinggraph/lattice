// Production-path proof for Fire 1 of the negative-filter-retraction
// projection design: a doc-mode actorAggregate lens whose anchor MATCH itself
// carries a filtering WHERE (the orphanedTaskGrants shape,
// packages/orchestration-base/lenses.go) must retract its previously-projected
// row once the anchor stops matching, even though the cypher then returns
// ZERO rows for that actor rather than one degenerate row a RealnessFilter
// could empty. Runs the REAL InstallActorAggregate + a live pipeline consumer
// against embedded NATS, mirroring refractor_business_sweep_e2e_test.go's
// scaffolding.
package refractor_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/projection"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
	"github.com/operatinggraph/lattice/internal/testutil"
)

// zeroRowE2EBucket is this test's disjoint output bucket.
const zeroRowE2EBucket = "zerorow-e2e-targets"

// zeroRowE2ELensRule is a throwaway doc-mode actorAggregate lens whose anchor
// MATCH itself carries the filtering WHERE — the orphanedTaskGrantsSpec shape
// (packages/orchestration-base/lenses.go) — with NO RealnessFilter, so the
// only code that can ever retract its row once the anchor stops matching is
// the zero-row-retraction transport under test.
func zeroRowE2ELensRule(t *testing.T) *lens.Rule {
	t.Helper()
	eng := full.New()
	cr, err := eng.Parse(`
MATCH (t:task {key: $actorKey})
  WHERE t.data.status = 'open'
RETURN t.key AS actorKey, t.key AS taskKey
`)
	require.NoError(t, err)
	return &lens.Rule{
		ID:             "lens-zerorow-retraction-e2e",
		CanonicalName:  "zeroRowTaskGrants",
		ProjectionKind: projection.ActorAggregateKind,
		ResolvedEngine: ruleengine.EngineFull,
		CompiledRule:   cr,
		Into: lens.IntoConfig{
			Target: "nats_kv",
			Bucket: zeroRowE2EBucket,
			Key:    lens.KeyField{"key"},
		},
		Output: &lens.OutputDescriptorSpec{
			AnchorType:       "task",
			OutputKeyPattern: "zeroRowTaskGrants.{actorSuffix}",
			BodyColumns:      []string{"taskKey"},
			EmptyBehavior:    string(projection.EmptyDelete),
			Freshness:        "auto",
		},
	}
}

func TestRefractor_ZeroRowRetraction_IncrementalCDCPath_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping zero-row retraction e2e in -short mode")
	}

	s := natsfixture.StartServer(t)

	nc := natsfixture.Connect(t, s.ClientURL())
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	testutil.EnsurePrimordials(t)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	_, err = conn.JetStream().CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: zeroRowE2EBucket})
	require.NoError(t, err)
	targetKV, err := conn.OpenKV(ctx, zeroRowE2EBucket)
	require.NoError(t, err)

	rule := zeroRowE2ELensRule(t)
	adpt, err := adapter.New(targetKV, rule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	p, err := pipeline.New(rule.ID, "nats_kv", bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), rule.CompiledRule)

	projectionRevision := func(k string) uint64 {
		entry, getErr := coreKV.Get(ctx, k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}

	// Enrolment through the REAL install gate: what is under test includes the
	// decision to arm zero-row retraction for this descriptor shape
	// (EntryKeyColumn=="" and EmptyBehavior:"delete" — desc.RequiresGuardedTombstone()).
	require.True(t, projection.InstallActorAggregate(p, adpt, rule, projectionRevision, adjKV, coreKV, logger),
		"a doc-mode actorAggregate lens with emptyBehavior:delete must install")
	require.True(t, p.ZeroRowRetractionArmed(),
		"a doc-mode delete/softDelete descriptor must arm the zero-row-retraction transport")

	p.RunOn(conn, e2eSpec(rule.ID, bootstrap.CoreKVBucket))
	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() { defer close(doneCh); p.Run(pipelineCtx) }()
	t.Cleanup(func() { pipelineCancel(); <-doneCh })

	taskID := stableNanoID("zerorow-retraction-task")
	taskKey := substrate.VertexKey("task", taskID)
	const provenanceAt = "2026-07-25T10:00:00Z"
	writeStatus := func(status string) uint64 {
		body := map[string]any{
			"key": taskKey, "class": "task", "isDeleted": false,
			"createdAt": provenanceAt, "lastModifiedAt": provenanceAt,
			"data": map[string]any{"status": status},
		}
		raw, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		rev, perr := coreKV.Put(ctx, taskKey, raw)
		require.NoError(t, perr)
		return rev
	}

	desc, err := projection.ParseOutputDescriptor(rule.Output)
	require.NoError(t, err)
	expectedKey := desc.BuildKey(taskKey)

	// --- PROJECT: the open task's violating row projects normally. ---
	writeStatus("open")
	require.Eventually(t, func() bool {
		entry, gErr := targetKV.Get(ctx, expectedKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		return env["taskKey"] == taskKey
	}, 30*time.Second, 100*time.Millisecond, "the open task did not project within 30s")

	// --- RETRACT: closing the task makes the anchor MATCH's own WHERE stop
	// matching, so the cypher returns ZERO rows for this actor (never a
	// degenerate one) — only the zero-row-retraction transport can retract
	// this row. Assert the guarded tombstone lands with a projectionSeq at
	// least as new as the flip event's own stream sequence. ---
	flipRev := writeStatus("closed")
	require.Eventually(t, func() bool {
		entry, gErr := targetKV.Get(ctx, expectedKey)
		if gErr != nil || entry == nil || len(entry.Value) == 0 {
			return false
		}
		var env map[string]any
		if json.Unmarshal(entry.Value, &env) != nil {
			return false
		}
		isDel, _ := env["isDeleted"].(bool)
		seq, _ := env["projectionSeq"].(float64)
		return isDel && uint64(seq) >= flipRev
	}, 30*time.Second, 100*time.Millisecond,
		"closing the task did not retract the projected row via zero-row retraction")
}
