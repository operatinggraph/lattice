// AC #1 / A3 — root-equivalence projection proof.
//
// After primordial bootstrap seeds the admin + Loom + Weaver + Bridge identities
// and their holdsRole → operator links, the Capability Lens must project a
// cap.identity.<id> doc for EACH that carries the operator role's scope:"any"
// platformPermissions — identical across all four. This proves topology →
// projection works uniformly for the service actors despite their non-plain
// `identity.system.*` class (the cypher + actor pipeline anchor on the
// vtx.identity key-type segment, never on class; Contract #7 §7.7).
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bootstrap"
	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/adjacency"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

func permScopeSet(t *testing.T, env map[string]any) []string {
	t.Helper()
	pp, _ := env["platformPermissions"].([]any)
	out := make([]string, 0, len(pp))
	for _, e := range pp {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, m["operationType"].(string)+":"+m["scope"].(string))
	}
	sort.Strings(out)
	return out
}

// TestRefractor_ServiceActorRootEquivalence_E2E asserts the Loom, Weaver, and
// Bridge cap.* projections carry the SAME platformPermissions set as the admin's.
func TestRefractor_ServiceActorRootEquivalence_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping service-actor projection e2e in -short mode")
	}

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

	boots := consumer.NewBootstrapper(conn, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, "test", logger)
	loaded := make(chan *lens.Rule, 4)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

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
	require.Equal(t, ruleengine.EngineFull, capabilityRule.ResolvedEngine)
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
	wireActorAggregate(t, p, capabilityRule, adjKV, coreKV, projectionRevision)

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

	// Build adjacency for the primordial capability topology. The production
	// CDC → adjacency bridge for primordial link envelopes is out of this
	// test's scope (see refractor_capability_e2e_test.go); build it directly.
	roleID := bootstrap.RoleOperatorID
	buildEdge := func(name, fromType, fromID, toType, toID string) {
		linkKey := substrate.LinkKey(fromType, fromID, name, toType, toID)
		edgeID := name + ":" + fromID + ":" + toID
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
			Direction: "outbound", NodeID: fromID, OtherNodeID: toID, OtherType: toType,
		}))
		require.NoError(t, adjacency.Build(ctx, adjKV, adjacency.CoreKVEvent{
			CoreKvKey: linkKey, EdgeID: edgeID, Name: name,
			Direction: "inbound", NodeID: toID, OtherNodeID: fromID, OtherType: fromType,
		}))
	}

	// holdsRole: each actor → operator role.
	buildEdge("holdsRole", "identity", bootstrap.BootstrapIdentityID, "role", roleID)
	buildEdge("holdsRole", "identity", bootstrap.LoomIdentityID, "role", roleID)
	buildEdge("holdsRole", "identity", bootstrap.WeaverIdentityID, "role", roleID)
	buildEdge("holdsRole", "identity", bootstrap.BridgeIdentityID, "role", roleID)
	// grantedBy: each operator-granted meta/install permission → operator role.
	for _, permID := range []string{
		bootstrap.PermCreateMetaVertexID, bootstrap.PermUpdateMetaVertexID,
		bootstrap.PermTombstoneMetaVertexID, bootstrap.PermInstallPackageID,
		bootstrap.PermUninstallPackageID,
	} {
		buildEdge("grantedBy", "permission", permID, "role", roleID)
	}

	// Re-touch each identity vertex so the pipeline projects it (the CDC
	// event the capability lens anchors on). Re-put the existing primordial
	// envelope unchanged.
	retouch := func(key string) {
		entry, getErr := coreKV.Get(ctx, key)
		require.NoError(t, getErr)
		_, putErr := coreKV.Put(ctx, key, entry.Value)
		require.NoError(t, putErr)
	}
	retouch(bootstrap.BootstrapIdentityKey)
	retouch(bootstrap.LoomIdentityKey)
	retouch(bootstrap.WeaverIdentityKey)
	retouch(bootstrap.BridgeIdentityKey)

	poll := func(capKey string) map[string]any {
		deadline := time.Now().Add(25 * time.Second)
		var entry *substrate.KVEntry
		for time.Now().Before(deadline) {
			entry, err = capabilityKV.Get(ctx, capKey)
			if err == nil && entry != nil && len(entry.Value) > 0 {
				var env map[string]any
				require.NoError(t, json.Unmarshal(entry.Value, &env))
				if pp, _ := env["platformPermissions"].([]any); len(pp) > 0 {
					return env
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("capability projection %q did not materialize with permissions; last err=%v", capKey, err)
		return nil
	}

	adminEnv := poll("cap.identity." + bootstrap.BootstrapIdentityID)
	loomEnv := poll("cap.identity." + bootstrap.LoomIdentityID)
	weaverEnv := poll("cap.identity." + bootstrap.WeaverIdentityID)
	bridgeEnv := poll("cap.identity." + bootstrap.BridgeIdentityID)

	adminPerms := permScopeSet(t, adminEnv)
	require.NotEmpty(t, adminPerms, "admin must project scope:any platformPermissions")

	// Root-equivalence: identical platformPermissions sets.
	require.Equal(t, adminPerms, permScopeSet(t, loomEnv),
		"loom platformPermissions must equal admin's (root-equivalent)")
	require.Equal(t, adminPerms, permScopeSet(t, weaverEnv),
		"weaver platformPermissions must equal admin's (root-equivalent)")
	require.Equal(t, adminPerms, permScopeSet(t, bridgeEnv),
		"bridge platformPermissions must equal admin's (root-equivalent)")

	// All operator perms are scope:any (the root-equivalent shape).
	for _, ps := range adminPerms {
		require.Contains(t, ps, ":any", "operator permission %q must be scope:any", ps)
	}

	// The cap key derives from the vtx.identity key segment, NOT the class —
	// proving the non-plain class does not break projection addressing.
	require.Equal(t, "cap.identity."+bootstrap.LoomIdentityID, loomEnv["key"])
	require.Equal(t, "cap.identity."+bootstrap.WeaverIdentityID, weaverEnv["key"])
	require.Equal(t, "cap.identity."+bootstrap.BridgeIdentityID, bridgeEnv["key"])
}
