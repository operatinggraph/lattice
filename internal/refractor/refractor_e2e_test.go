// Package refractor_test contains the cross-package end-to-end test for
// the Refractor projection engine — Story 2.1 AC #10: 100 mutations land
// in the projection target with p99 latency < 500ms (NFR-P3).
//
// Test architecture (Story 2.1b):
//
//	embedded NATS  →  Core KV  →  Refractor pipeline  →  NATS KV target
//	                                                  (projection)
//
// The test exercises the same machinery `cmd/refractor` wires up at
// runtime:
//   - Adjacency bootstrapper drains to lag=0 (`Ready()` closes).
//   - Lens loaded via CoreKVSource (the standard Story 2.1 lens-activation
//     path) using a `vtx.meta.<NanoID>` vertex with envelope class
//     `meta.lens` per data-contracts.md §1.2 line 70 (Story 2.1b Gap 1).
//   - 100 contract upserts written to Core KV; the pipeline projects each
//     into the target NATS KV bucket.
//   - Per-mutation latency (write → projected) is captured and the
//     distribution (p50/p95/p99/max) is asserted against NFR-P3.
//
// Notes on test design:
//
//  1. The write path bypasses the Processor (no `ops.default.<reqId>`
//     submission). Reason: a Processor write path for class `contract`
//     would require additionally seeding a `meta.ddl.contract` DDL +
//     `meta.script` Starlark for contract creation — a yak-shave that
//     exceeds the 2.1b scope. The Refractor's NFR-P3 budget is measured
//     from the CDC arrival; the Processor's ack-loop adds constant
//     overhead orthogonal to what we're measuring.
//
//  2. Story 2.3 (Deviation 13 RESOLVED): the pipeline now uses
//     substrate.ParseVertexKey to parse Contract #1 §1.5 vertex key
//     shape `vtx.<type>.<NanoID>`. This test was updated from the legacy
//     `node_<label>_<id>` shape to the Lattice shape.
//
//  3. Target is NATS KV (not Postgres). The latency budget is dominated
//     by CDC fan-out + cypher evaluation, both adapter-independent. NATS
//     KV makes the test hermetic — no Docker dependency, no flakiness
//     from Postgres connection setup, no CI service dependency. A
//     Postgres-target variant is deferred to Story 2.2.
//
// The substance of AC #10 — "100 mutations project end-to-end and the
// pipeline holds NFR-P3" — is preserved.
package refractor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/refractor/adapter"
	"github.com/operatinggraph/lattice/internal/refractor/consumer"
	"github.com/operatinggraph/lattice/internal/refractor/lens"
	"github.com/operatinggraph/lattice/internal/refractor/pipeline"
	"github.com/operatinggraph/lattice/internal/refractor/ruleengine/full"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// e2eContractNanoID returns a deterministic 20-character NanoID (Contract #1 alphabet)
// for a given integer index, used to build vtx.contract.<NanoID> test keys.
// Format: "E2eContract" (11 chars) + 9-char base-58 index suffix.
func e2eContractNanoID(i int) string {
	const (
		alpha  = substrate.Alphabet
		n      = len(alpha)
		prefix = "E2eContract" // 11 valid alphabet chars
		digits = 9             // encodes up to 58^9 unique values
	)
	buf := make([]byte, digits)
	x := i
	for d := digits - 1; d >= 0; d-- {
		buf[d] = alpha[x%n]
		x /= n
	}
	return prefix + string(buf)
}

// TestRefractor_E2E_P99 is the Story 2.1 AC #10 end-to-end latency test.
// It drives 100 mutations through the morphed Refractor pipeline and
// asserts p99 projection latency < 500ms (NFR-P3).
//
// Skipped in `-short` mode (default `go test ./... -short` skips this);
// always runs in the default suite (and in CI).
func TestRefractor_E2E_P99(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e p99 test in -short mode")
	}

	const (
		coreBucket   = "core-kv"
		adjBucket    = "refractor-adjacency"
		targetBucket = "contract-view"
		lensID       = "RfxE2eP99TestLensXYZ" // 20-char NanoID, alphabet-valid
		mutations    = 100
		p99Budget    = 500 * time.Millisecond
	)

	// --- embedded NATS ---
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	js := conn.JetStream()

	// --- provision buckets analogous to `make up` + verify-bootstrap ---
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: coreBucket})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: adjBucket})
	require.NoError(t, err)
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: targetBucket})
	require.NoError(t, err)
	coreKV, err := conn.OpenKV(ctx, coreBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, adjBucket)
	require.NoError(t, err)
	targetKV, err := conn.OpenKV(ctx, targetBucket)
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// --- adjacency bootstrapper (`cmd/refractor` main.go pattern) ---
	boots := consumer.NewBootstrapper(conn, coreBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- pipeline + lens setup ---
	// Build the projection plan: MATCH (c:contract) RETURN c.id, c.name
	eng := full.New()
	cr, err := eng.Parse("MATCH (c:contract {key: $actorKey}) RETURN c.id AS contract_id, c.name AS name")
	require.NoError(t, err)
	fullCR, ok := cr.(*full.CompiledRule)
	require.True(t, ok)
	fullCR.KeyColumns = []string{"contract_id"}
	require.NoError(t, fullCR.ValidateKeyColumns())

	adpt, err := adapter.New(targetKV, []string{"contract_id"}, adapter.DeleteModeHard)
	require.NoError(t, err)

	p, err := pipeline.New(lensID, "nats_kv", coreBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(eng, cr)

	p.RunOn(conn, e2eSpec(lensID, coreBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Run(pipelineCtx)
	}()
	t.Cleanup(func() {
		pipelineCancel()
		wg.Wait()
	})

	// --- Lens activation via CoreKVSource (Story 2.1b Gap 1 path) ---
	// Write the meta-lens vertex first (vtx.meta.<id> with class meta.lens)
	// + the spec aspect. Verifies the corrected key-shape end-to-end.
	src := lens.NewCoreKVSource(conn, coreBucket, "test", logger)
	lensActivated := make(chan struct{}, 1)
	src.SetLoadCallback(func(r *lens.Rule) {
		select {
		case lensActivated <- struct{}{}:
		default:
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	metaVertexKey := "vtx.meta." + lensID
	specKey := metaVertexKey + ".spec"
	vertexDoc := map[string]any{
		"class": "meta.lens",
		"key":   metaVertexKey,
		"data":  map[string]any{},
	}
	vertexJSON, _ := json.Marshal(vertexDoc)
	_, err = coreKV.Put(ctx, metaVertexKey, vertexJSON)
	require.NoError(t, err)

	spec := lens.LensSpec{
		ID:            lensID,
		CanonicalName: "lens.e2e-p99-contract-view",
		TargetType:    "nats_kv",
		CypherRule:    "MATCH (c:contract {key: $actorKey}) RETURN c.id AS contract_id, c.name AS name",
		TargetConfig:  json.RawMessage(`{"bucket":"` + targetBucket + `","key":["contract_id"]}`),
	}
	specJSON, _ := json.Marshal(spec)
	_, err = coreKV.Put(ctx, specKey, specJSON)
	require.NoError(t, err)

	select {
	case <-lensActivated:
	case <-time.After(5 * time.Second):
		t.Fatal("CoreKVSource did not activate the lens within 5s of writes")
	}

	// --- 100 contract mutations + latency capture ---
	type sample struct {
		id        string
		startedAt time.Time
		landedAt  time.Time
	}
	samples := make([]sample, mutations)

	for i := 0; i < mutations; i++ {
		id := fmt.Sprintf("contract%04d", i)
		// Lattice Contract #1 §1.5 vertex key shape: vtx.<type>.<NanoID>.
		// Deviation 13 is now closed by Story 2.3 — pipeline uses substrate.ParseVertexKey.
		// NanoID is deterministic: prefix "E2eContract" (11 chars) + 9-char base-58 index.
		nanoID := e2eContractNanoID(i)
		key := "vtx.contract." + nanoID
		now := time.Now().UTC().Format(time.RFC3339)
		body := map[string]any{
			"id":             id,
			"name":           fmt.Sprintf("Contract %d", i),
			"isDeleted":      false,
			"createdAt":      now,
			"lastModifiedAt": now,
		}
		bodyJSON, _ := json.Marshal(body)

		samples[i].id = id
		samples[i].startedAt = time.Now()
		_, err = coreKV.Put(ctx, key, bodyJSON)
		require.NoError(t, err)
	}

	// Poll the target bucket for each contract; record landing time.
	const pollEvery = 5 * time.Millisecond
	const pollDeadline = 30 * time.Second
	deadline := time.Now().Add(pollDeadline)
	for i := 0; i < mutations; i++ {
		for {
			entry, getErr := targetKV.Get(ctx, samples[i].id)
			if getErr == nil && entry != nil && len(entry.Value) > 0 {
				samples[i].landedAt = time.Now()
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("contract %q did not project within %s (samples done: %d/%d)",
					samples[i].id, pollDeadline, i, mutations)
			}
			time.Sleep(pollEvery)
		}
	}

	// --- compute latency distribution ---
	latencies := make([]time.Duration, mutations)
	for i, s := range samples {
		latencies[i] = s.landedAt.Sub(s.startedAt)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	pct := func(p float64) time.Duration {
		idx := int(float64(len(latencies)-1) * p)
		return latencies[idx]
	}

	p50 := pct(0.50)
	p95 := pct(0.95)
	p99 := pct(0.99)
	max := latencies[len(latencies)-1]

	t.Logf("AC #10 e2e p99 latency distribution (n=%d): p50=%s p95=%s p99=%s max=%s budget=%s",
		mutations, p50, p95, p99, max, p99Budget)

	// Print to stdout too so the summary is visible in CI logs.
	fmt.Printf("\n=== Refractor AC #10 e2e p99 summary ===\n"+
		"  mutations: %d (all projected)\n"+
		"  p50:  %s\n"+
		"  p95:  %s\n"+
		"  p99:  %s\n"+
		"  max:  %s\n"+
		"  budget (NFR-P3): %s\n"+
		"========================================\n\n",
		mutations, p50, p95, p99, max, p99Budget)

	require.Lessf(t, p99, p99Budget,
		"NFR-P3 violated: p99=%s exceeds budget=%s (p50=%s p95=%s max=%s)",
		p99, p99Budget, p50, p95, max)
}
