// Capability Lens (primordial-identity anchor) live activation e2e.
//
// This test exercises the production wiring for the shrunk capability lens:
//
//   - Bootstrap seeds the `capability` Lens as a `meta.lens` vertex + spec
//     aspect carrying the LensSpec JSON body. The role-by-operation index is
//     owned by the rbac-domain package, not the kernel seed.
//   - Refractor's CoreKVSource watch activates the lens; the engine resolves
//     to `full` and the pipeline routes through full.Engine.ExecuteWith with
//     live EventContext.Parameters (`$actorKey`, `$now`, `$projectedAt`).
//   - The compiled Output descriptor's envelope wraps each projection row into
//     the Contract #6 §6.2 shape before the adapter writes.
//   - A single fixture identity holding the primordial `operator` role via a
//     real holdsRole link (Contract #7 §7.7, root-designation-topology-reconverge)
//     is seeded directly to Core KV. The anchor projects the fixed kernel
//     root-grant set for it; serviceAccess + roles are static empty arrays
//     (their producers are the future service package + rbac-domain's
//     capabilityRoles lens).
//   - The expected `cap.identity.<NanoID>` entry must appear in capability-kv
//     with the §6.2 sections present.
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
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
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
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
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

	coreKV, err := conn.OpenKV(ctx, bootstrap.CoreKVBucket)
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, bootstrap.RefractorAdjacencyKV)
	require.NoError(t, err)
	capabilityKV, err := conn.OpenKV(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)

	// --- adjacency bootstrapper ---
	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	// --- CoreKVSource activation (drives the primary capability lens) ---
	// We capture the activated *lens.Rule so we can construct the
	// pipeline using the same engine routing cmd/refractor's
	// startPipeline performs.
	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, "test", logger)
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	// Collect the primordial-seeded capability lens.
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

	targetKV, err := conn.OpenKV(ctx, capabilityRule.Into.Bucket)
	require.NoError(t, err)
	adpt, err := adapter.New(targetKV, capabilityRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)

	p, err := pipeline.New(capabilityRule.ID, "nats_kv",
		bootstrap.CoreKVBucket, adjKV, coreKV, adpt, nil)
	require.NoError(t, err)
	p.UseFullEngine(full.New(), capabilityRule.CompiledRule)
	projectionRevision := func(k string) uint64 {
		entry, getErr := coreKV.Get(ctx, k)
		if getErr != nil || entry == nil {
			return 0
		}
		return entry.Revision
	}
	lensDefKey := "vtx.meta." + capabilityRule.ID
	capDesc, err := projection.ParseOutputDescriptor(capabilityRule.Output)
	require.NoError(t, err, "capability lens must carry a valid Output descriptor")
	p.SetEnvelopeFn(capDesc.EnvelopeFn(lensDefKey, projectionRevision))

	p.RunOn(conn, e2eSpec(capabilityRule.ID, bootstrap.CoreKVBucket))

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)
		p.Run(pipelineCtx)
	}()
	t.Cleanup(func() {
		pipelineCancel()
		<-doneCh
	})

	// --- fixture: an identity holding the primordial operator role ---
	// The capability lens is the primordial-identity anchor: it projects the
	// fixed kernel root-grant set for identities holding `operator` via
	// `holdsRole` only, with no rbac-permission graph walk. Ordinary actors
	// read role-derived grants from rbac-domain's cap.roles.<actor> projection
	// (see the capabilityRoles e2e).
	identityID := stableNanoID("alice")
	identityKey := substrate.VertexKey("identity", identityID)

	// 1. Write vertices to Core KV. Each carries `class` so the
	// executor's nodeMatches sees the cypher label.
	// Real Core KV vertices carry the universal envelope provenance fields
	// (Contract #1 §1.3). The capability lens derives projectedAt from the
	// anchor vertex's lastModifiedAt, so the fixture must include it.
	provenanceAt := "2026-05-15T10:00:00Z"
	// Domain fields live under the `data` envelope, mirroring the Processor's
	// vertex shape; lens cypher rules read them as node.data.<field>.
	writeVertex := func(key, class string, extra map[string]any) {
		body := map[string]any{
			"key":            key,
			"class":          class,
			"createdAt":      provenanceAt,
			"lastModifiedAt": provenanceAt,
			"data":           extra,
		}
		data, jerr := json.Marshal(body)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, key, data)
		require.NoError(t, perr)
	}
	// The identity vertex still carries data.protected (its retained
	// anti-brick meaning, root-designation-topology-reconverge 2026-07-03) —
	// but root is now conferred SOLELY by holding the primordial `operator`
	// role via a real holdsRole link (Contract #7 §7.7), not by this bit.
	writeVertex(identityKey, "identity", map[string]any{"name": "alice", "protected": true})
	holdsRoleLinkKey := substrate.LinkKey("identity", identityID, "holdsRole", "role", bootstrap.RoleOperatorID)
	linkBody, lerr := bootstrap.MakeLinkEnvelope(holdsRoleLinkKey, identityKey, bootstrap.RoleOperatorKey, "holdsRole", "holdsRole", nil)
	require.NoError(t, lerr)
	_, lerr = coreKV.Put(ctx, holdsRoleLinkKey, linkBody)
	require.NoError(t, lerr)

	// --- poll capability-kv for the projection ---
	expectedKey := "cap.identity." + identityID
	deadline := time.Now().Add(20 * time.Second)
	var entry *substrate.KVEntry
	for time.Now().Before(deadline) {
		entry, err = capabilityKV.Get(ctx, expectedKey)
		if err == nil && entry != nil && len(entry.Value) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, entry, "capability projection did not land within deadline; last err=%v", err)

	// --- assert Contract #6 §6.2 envelope shape ---
	var env map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &env))

	require.Equal(t, expectedKey, env["key"])
	require.Equal(t, identityKey, env["actor"])
	require.Equal(t, "1.0", env["version"])
	// projectedAt is deterministic provenance: the anchor identity vertex's
	// lastModifiedAt, not a wall-clock read (Story 1.5.4 §3.2).
	require.Equal(t, provenanceAt, env["projectedAt"])

	// Protected kernel actor: full per-lane submission grant (Contract #2 §2.3).
	lanes, _ := env["lanes"].([]any)
	require.ElementsMatch(t, []any{"default", "meta", "urgent", "system"}, lanes)

	revs, _ := env["projectedFromRevisions"].(map[string]any)
	require.NotEmpty(t, revs, "projectedFromRevisions must include at least anchor + lens-def revisions")
	// Anchor revision must be present.
	require.Contains(t, revs, identityKey)

	// All §6.2 sections must be present. platformPermissions carries the fixed
	// kernel root-grant set; serviceAccess + roles are static empty arrays in
	// the core anchor (their producers are the future service package +
	// rbac-domain's capabilityRoles lens).
	require.Contains(t, env, "platformPermissions")
	require.Contains(t, env, "serviceAccess")
	require.Contains(t, env, "ephemeralGrants")
	require.Contains(t, env, "roles")

	// platformPermissions must include the kernel meta + install root grants.
	pp, _ := env["platformPermissions"].([]any)
	wantOps := map[string]bool{
		"CreateMetaVertex": false, "UpdateMetaVertex": false, "TombstoneMetaVertex": false,
		"InstallPackage": false, "UninstallPackage": false,
	}
	for _, e := range pp {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if op, _ := m["operationType"].(string); op != "" {
			require.Equal(t, "any", m["scope"], "every anchor grant is scope:any")
			if _, known := wantOps[op]; known {
				wantOps[op] = true
			}
		}
	}
	for op, seen := range wantOps {
		require.Truef(t, seen, "protected identity's platformPermissions must include %q: %v", op, pp)
	}

	// serviceAccess + roles are empty in the core anchor.
	sa, _ := env["serviceAccess"].([]any)
	require.Empty(t, sa, "core anchor projects no serviceAccess (Path B): %v", sa)
	roles, _ := env["roles"].([]any)
	require.Empty(t, roles, "core anchor projects no roles (rbac-domain owns them): %v", roles)

	fmt.Printf("\n=== capability primordial-anchor e2e ===\n"+
		"  capability key: %s\n"+
		"  actor:          %s\n"+
		"  revisions:      %v\n"+
		"  platformPerms:  %v\n"+
		"========================================\n\n",
		expectedKey, identityKey, revs, pp)
}
