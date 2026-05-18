// Story 3.2a — Capability Lens live activation e2e.
//
// This test exercises the production wiring landed in Story 3.2a:
//
//   - Bootstrap seeds the two Capability Lenses (capability,
//     capabilityRoleIndex) as `meta.lens` vertices + spec aspects
//     carrying the LensSpec JSON body.
//   - Refractor's CoreKVSource watch activates both lenses; for the
//     primary `capability` lens the engine resolves to `full` and the
//     pipeline routes through full.Engine.ExecuteWith with live
//     EventContext.Parameters (`$actorKey`, `$now`, `$projectedAt`).
//   - The Capability KV envelope wrapper (capabilityenv.NewWrapper)
//     wraps each projection row into the Contract #6 §6.2 shape
//     before the adapter writes.
//   - A single fixture identity + role/permission/service-availability
//     topology is seeded directly to Core KV (Story 3.2a Decision #6 —
//     the test must NOT go through the Processor; that is Story 3.3).
//   - The expected `cap.identity.<NanoID>` entry must appear in
//     capability-kv with three sections (platformPermissions,
//     serviceAccess, ephemeralGrants) and the roles list populated.
//
// Out of scope (defer to 3.2b): multi-identity (3 actors),
// byte-level contract conformance, tombstone re-projection,
// NFR-P3 latency emission for capability.
package refractor_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/capabilityenv"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

// stableNanoID returns a deterministic 20-char Contract #1 NanoID for
// the given fixture role (test-only — production NanoIDs come from
// substrate.NewNanoID).
func stableNanoID(role string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 1469598103934665603
	for _, b := range []byte("3.2a-e2e:" + role) {
		seed ^= uint64(b)
		seed *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[seed%uint64(len(alphabet))]
		seed = seed*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

// TestRefractor_CapabilityLens_E2E exercises the full Refractor
// activation path against the bootstrap-seeded Capability Lens for
// a single identity + role/permission/service-availability topology.
func TestRefractor_CapabilityLens_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping capability e2e test in -short mode")
	}

	// --- embedded NATS ---
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()}
	s := natstest.RunServer(opts)
	defer s.Shutdown()

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	js := conn.JetStream()

	// --- provision buckets + run primordial bootstrap ---
	// Generate a fresh primordial key space per test (the bootstrap
	// JSON path is t.TempDir-scoped so tests stay hermetic).
	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err = bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)
	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)
	require.NoError(t, seeder.ProvisionBuckets(ctx))
	require.NoError(t, seeder.SeedPrimordial(ctx))

	coreKV, err := js.KeyValue(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := js.KeyValue(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := js.KeyValue(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	// --- adjacency bootstrapper ---
	boots := consumer.NewBootstrapper(js, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	manager := consumer.NewManager(js, bootstrap.CoreKVBucket)

	// --- CoreKVSource activation (drives the primary capability lens) ---
	// We capture the activated *lens.Rule so we can construct the
	// pipeline using the same engine routing cmd/refractor's
	// startPipeline performs.
	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, logger)
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	// Collect both primordial-seeded lenses (capability + capabilityRoleIndex).
	timeout := time.After(10 * time.Second)
	var capabilityRule *lens.Rule
	for capabilityRule == nil {
		select {
		case r := <-loaded:
			if r.CanonicalName == "capability" {
				capabilityRule = r
			}
		case <-timeout:
			t.Fatal("did not activate capability lens within 10s")
		}
	}

	// --- pipeline for the capability lens (full engine + envelope) ---
	require.Equal(t, ruleengine.EngineFull, capabilityRule.ResolvedEngine,
		"capability lens must resolve to the full engine")
	require.NotNil(t, capabilityRule.CompiledRule)

	targetKV, err := js.KeyValue(ctx, capabilityRule.Into.Bucket)
	require.NoError(t, err)
	adpt, err := adapter.New(targetKV, capabilityRule.Into.Key)
	require.NoError(t, err)

	p, err := pipeline.New(capabilityRule.ID, capabilityRule.Team, "nats_kv",
		nil, bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), capabilityRule.CompiledRule)
	projectionRevision := func(k string) uint64 {
		entry, getErr := coreKV.Get(ctx, k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision()
	}
	lensDefKey := "vtx.meta." + capabilityRule.ID
	p.SetEnvelopeFn(capabilityenv.NewWrapper(lensDefKey, projectionRevision, nil))

	require.NoError(t, manager.Add(ctx, capabilityRule.ID))
	cons := manager.Consumer(capabilityRule.ID)
	require.NotNil(t, cons)

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		p.Run(pipelineCtx, cons)
	}()
	t.Cleanup(func() {
		pipelineCancel()
		<-doneCh
	})

	// --- fixture topology: identity + role + permission + service+location ---
	identityID := stableNanoID("alice")
	roleID := stableNanoID("editor")
	permID := stableNanoID("read-any")
	locationID := stableNanoID("office")
	serviceID := stableNanoID("docs")

	identityKey := substrate.VertexKey("identity", identityID)
	roleKey := substrate.VertexKey("role", roleID)
	permKey := substrate.VertexKey("permission", permID)
	locationKey := substrate.VertexKey("location", locationID)
	serviceKey := substrate.VertexKey("service", serviceID)

	// 1. Write vertices to Core KV. Each carries `class` so the
	// executor's nodeMatches sees the cypher label.
	writeVertex := func(key, class string, extra map[string]any) {
		body := map[string]any{"key": key, "class": class}
		for k, v := range extra {
			body[k] = v
		}
		data, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, data)
		require.NoError(t, perr)
	}
	writeVertex(roleKey, "role", map[string]any{"canonicalName": "editor"})
	writeVertex(permKey, "permission", map[string]any{
		"operationType": "read",
		"scope":         "any",
	})
	writeVertex(locationKey, "location", nil)
	writeVertex(serviceKey, "service", map[string]any{
		"class": "service",
	})

	// 2. Build adjacency directly (the production CDC → adjacency path
	// is gated on edge-event payloads with a `nodeId` field; primordial
	// link envelopes don't carry that — see closing summary residual
	// risk for 3.2b: bridge Contract #1 link envelopes through the
	// adjacency bootstrapper).
	buildEdge := func(name, fromType, fromID, toType, toID string) {
		linkKey := substrate.LinkKey(fromType, fromID, name, toType, toID)
		edgeID := name + ":" + fromID + ":" + toID
		require.NoError(t, adjacency.Build(adjKV, adjacency.CoreKVEvent{
			CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
			Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
		}))
		require.NoError(t, adjacency.Build(adjKV, adjacency.CoreKVEvent{
			CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
			Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
		}))
	}
	buildEdge("holdsRole", "identity", identityID, "role", roleID)
	buildEdge("grantsPermission", "role", roleID, "permission", permID)
	buildEdge("containedIn", "identity", identityID, "location", locationID)
	buildEdge("availableAt", "location", locationID, "service", serviceID)

	// 3. Finally write the identity vertex — this is the CDC event the
	// capability lens projects on. We write it last so adjacency is
	// already in place when the projection runs (NFR-P3 ordering is
	// best-effort but production drains in order via the durable
	// consumer; for a one-shot test this avoids racing the projection
	// against half-built adjacency).
	writeVertex(identityKey, "identity", map[string]any{"name": "alice"})

	// --- poll capability-kv for the projection ---
	expectedKey := "cap.identity." + identityID
	deadline := time.Now().Add(20 * time.Second)
	var entry jetstream.KeyValueEntry
	for time.Now().Before(deadline) {
		entry, err = capabilityKV.Get(ctx, expectedKey)
		if err == nil && entry != nil && len(entry.Value()) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, entry, "capability projection did not land within deadline; last err=%v", err)

	// --- assert Contract #6 §6.2 envelope shape ---
	var env map[string]any
	require.NoError(t, json.Unmarshal(entry.Value(), &env))

	require.Equal(t, expectedKey, env["key"])
	require.Equal(t, identityKey, env["actor"])
	require.Equal(t, "1.0", env["version"])
	require.NotEmpty(t, env["projectedAt"])

	lanes, _ := env["lanes"].([]any)
	require.ElementsMatch(t, []any{"default"}, lanes)

	revs, _ := env["projectedFromRevisions"].(map[string]any)
	require.NotEmpty(t, revs, "projectedFromRevisions must include at least anchor + lens-def revisions")
	// Anchor revision must be present.
	require.Contains(t, revs, identityKey)

	// Three sections + roles must be present (may be non-empty given the seeded topology).
	require.Contains(t, env, "platformPermissions")
	require.Contains(t, env, "serviceAccess")
	require.Contains(t, env, "ephemeralGrants")
	require.Contains(t, env, "roles")

	// platformPermissions should include the editor's read-any.
	pp, _ := env["platformPermissions"].([]any)
	foundRead := false
	for _, e := range pp {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["operationType"] == "read" && m["scope"] == "any" {
			foundRead = true
			break
		}
	}
	require.True(t, foundRead, "platformPermissions must include read/any: %v", pp)

	// roles should include the editor role key.
	roles, _ := env["roles"].([]any)
	foundRole := false
	for _, r := range roles {
		if r == roleKey {
			foundRole = true
			break
		}
	}
	require.True(t, foundRole, "roles must include editor: %v", roles)

	// serviceAccess should reference the docs service.
	sa, _ := env["serviceAccess"].([]any)
	foundSvc := false
	for _, e := range sa {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if m["service"] == serviceKey {
			foundSvc = true
			break
		}
	}
	require.True(t, foundSvc, "serviceAccess must include docs service: %v", sa)

	fmt.Printf("\n=== Story 3.2a capability lens e2e ===\n"+
		"  capability key: %s\n"+
		"  actor:          %s\n"+
		"  revisions:      %v\n"+
		"  roles:          %v\n"+
		"========================================\n\n",
		expectedKey, identityKey, revs, roles)
}
