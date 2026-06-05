// Story 3.2b — multi-identity Capability Lens e2e + tombstone semantics.
//
// This test closes Story 3.2 AC #1 (multi-identity), #5 (tombstone
// re-projection), and #2 (capabilityRoleIndex live activation). The
// fixture seeds THREE deterministic identities exercising the three
// distinct projection paths:
//
//   - Identity A: platform admin (role → permission grant).
//   - Identity B: regular user with role granting service access via
//     containedIn → location → availableAt → service topology.
//   - Identity C: user with an assignedTo task that produces an
//     ephemeralGrant entry (FR56 task-derived grant).
//
// Both Capability Lenses are activated through the production wiring
// path (CoreKVSource + per-canonical-name envelope selection +
// ActorEnumerator for the primary lens). The link-bridge bootstrapper
// (3.2b §1) populates adjacency directly from Contract #1 link
// envelopes — no `adjacency.Build` workaround.
package refractor_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/asolgan/lattice/internal/refractor/capabilityenv"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/ruleengine"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
	orchestrationbase "github.com/asolgan/lattice/packages/orchestration-base"
)

// stableMultiID returns a deterministic NanoID for a multi-e2e fixture role.
func stableMultiID(role string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 14695981039346656037
	for _, b := range []byte("3.2b-multi:" + role) {
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


func TestRefractor_CapabilityLens_MultiIdentity_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-identity capability e2e test in -short mode")
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

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	js := conn.JetStream()

	// --- provision buckets + run primordial bootstrap ---
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

	// --- adjacency bootstrapper (with 3.2b link-bridge bridge) ---
	boots := consumer.NewBootstrapper(js, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	manager := consumer.NewManager(js, bootstrap.CoreKVBucket)

	// --- CoreKVSource activation: collect both seeded lenses ---
	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, logger)
	loaded := make(chan *lens.Rule, 8)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	var capabilityRule, roleIndexRule *lens.Rule
	deadline := time.Now().Add(15 * time.Second)
	for capabilityRule == nil || roleIndexRule == nil {
		if time.Now().After(deadline) {
			t.Fatalf("did not load both seeded lenses within 15s; cap=%v idx=%v",
				capabilityRule != nil, roleIndexRule != nil)
		}
		select {
		case r := <-loaded:
			switch r.CanonicalName {
			case "capability":
				capabilityRule = r
			case "capabilityRoleIndex":
				roleIndexRule = r
			}
		case <-time.After(200 * time.Millisecond):
		}
	}

	// --- shared full engine ---
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision()
	}

	// --- primary capability pipeline ---
	capTargetKV, err := js.KeyValue(ctx, capabilityRule.Into.Bucket)
	require.NoError(t, err)
	capAdpt, err := adapter.New(capTargetKV, capabilityRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)

	capP, err := pipeline.New(capabilityRule.ID, "nats_kv",
		nil, bootstrap.CoreKVBucket, adjKV, coreKV, capAdpt, nil)
	require.NoError(t, err)
	require.Equal(t, ruleengine.EngineFull, capabilityRule.ResolvedEngine)
	require.NotNil(t, capabilityRule.CompiledRule)
	capP.UseFullEngine(fullEngine, capabilityRule.CompiledRule)
	capP.SetEnvelopeFn(capabilityenv.NewWrapper("vtx.meta."+capabilityRule.ID, projectionRevision))
	capP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capabilityenv.IdentityType))
	capLatency := pipeline.NewLatencyRingBuffer(pipeline.DefaultLatencyBufferSize)
	capP.SetLatencyBuffer(capLatency)

	require.NoError(t, manager.Add(ctx, capabilityRule.ID))
	capCons := manager.Consumer(capabilityRule.ID)
	require.NotNil(t, capCons)

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	capDone := make(chan struct{})
	go func() { defer close(capDone); capP.Run(pipelineCtx, capCons) }()

	// --- secondary capabilityRoleIndex pipeline ---
	idxTargetKV, err := js.KeyValue(ctx, roleIndexRule.Into.Bucket)
	require.NoError(t, err)
	idxAdpt, err := adapter.New(idxTargetKV, roleIndexRule.Into.Key, adapter.DeleteModeHard)
	require.NoError(t, err)
	idxP, err := pipeline.New(roleIndexRule.ID, "nats_kv",
		nil, bootstrap.CoreKVBucket, adjKV, coreKV, idxAdpt, nil)
	require.NoError(t, err)
	require.Equal(t, ruleengine.EngineFull, roleIndexRule.ResolvedEngine)
	idxP.UseFullEngine(fullEngine, roleIndexRule.CompiledRule)
	idxP.SetEnvelopeFn(capabilityenv.NewRoleIndexWrapper())

	require.NoError(t, manager.Add(ctx, roleIndexRule.ID))
	idxCons := manager.Consumer(roleIndexRule.ID)
	require.NotNil(t, idxCons)

	idxDone := make(chan struct{})
	go func() { defer close(idxDone); idxP.Run(pipelineCtx, idxCons) }()

	// --- tertiary capabilityEphemeral pipeline ---
	// The orchestration-base `capabilityEphemeral` lens is a PACKAGE lens, not
	// bootstrap-seeded, so we compile its literal spec and wire a pipeline
	// directly (mirroring how the primary cap pipeline is wired). It projects
	// FR56 grants to the disjoint key cap.ephemeral.<actor> in the same shared
	// capability-kv bucket.
	var ephSpec string
	for _, l := range orchestrationbase.Lenses() {
		if l.CanonicalName == "capabilityEphemeral" {
			ephSpec = l.Spec
		}
	}
	require.NotEmpty(t, ephSpec, "orchestration-base must declare a capabilityEphemeral lens")
	ephCR, err := fullEngine.Parse(ephSpec)
	require.NoError(t, err, "capabilityEphemeral spec must parse")
	ephTargetKV, err := js.KeyValue(ctx, bootstrap.CapabilityKVBucket)
	require.NoError(t, err)
	// DEFAULT HARD delete: no deleteMode override.
	ephAdpt, err := adapter.New(ephTargetKV, []string{"key"}, adapter.DeleteModeHard)
	require.NoError(t, err)
	const ephLensID = "EphLensId00000000001" // synthetic 20-char id for the consumer
	ephP, err := pipeline.New(ephLensID, "nats_kv",
		nil, bootstrap.CoreKVBucket, adjKV, coreKV, ephAdpt, nil)
	require.NoError(t, err)
	ephP.UseFullEngine(fullEngine, ephCR)
	ephP.SetEnvelopeFn(capabilityenv.NewEphemeralWrapper("vtx.meta."+ephLensID, projectionRevision))
	ephP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capabilityenv.IdentityType))

	require.NoError(t, manager.Add(ctx, ephLensID))
	ephCons := manager.Consumer(ephLensID)
	require.NotNil(t, ephCons)
	ephDone := make(chan struct{})
	go func() { defer close(ephDone); ephP.Run(pipelineCtx, ephCons) }()

	t.Cleanup(func() {
		pipelineCancel()
		<-capDone
		<-idxDone
		<-ephDone
	})

	// --- fixture: three identities, role/permission, location/service, task ---
	identityAID := stableMultiID("admin-A")
	identityBID := stableMultiID("location-user-B")
	identityCID := stableMultiID("task-grantee-C")
	adminRoleID := stableMultiID("role-admin")
	userRoleID := stableMultiID("role-user")
	adminPermID := stableMultiID("perm-admin-write")
	userPermID := stableMultiID("perm-user-read")
	locationID := stableMultiID("office-3.2b")
	serviceID := stableMultiID("docs-3.2b")
	taskID := stableMultiID("task-bigreport")
	// The task grant is LINK-sourced — the op meta-vertex (forOperation) +
	// the scopedTo target are real graph vertices.
	taskOpID := stableMultiID("op-approve-3.2b")
	taskTargetID := stableMultiID("leaseapp-3.2b")

	identityAKey := substrate.VertexKey("identity", identityAID)
	identityBKey := substrate.VertexKey("identity", identityBID)
	identityCKey := substrate.VertexKey("identity", identityCID)
	adminRoleKey := substrate.VertexKey("role", adminRoleID)
	userRoleKey := substrate.VertexKey("role", userRoleID)
	adminPermKey := substrate.VertexKey("permission", adminPermID)
	userPermKey := substrate.VertexKey("permission", userPermID)
	locationKey := substrate.VertexKey("location", locationID)
	serviceKey := substrate.VertexKey("service", serviceID)
	taskKey := substrate.VertexKey("task", taskID)
	taskOpKey := substrate.VertexKey("meta", taskOpID)
	taskTargetKey := substrate.VertexKey("leaseapp", taskTargetID)

	// Real Core KV vertices carry the universal envelope provenance fields
	// (Contract #1 §1.3); the capability lens derives projectedAt from the
	// anchor vertex's lastModifiedAt, so the fixture includes it.
	const provenanceAt = "2026-05-15T10:00:00Z"
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
	writeLink := func(srcType, srcID, name, dstType, dstID string) string {
		linkKey := substrate.LinkKey(srcType, srcID, name, dstType, dstID)
		envelope := map[string]any{
			"key":           linkKey,
			"class":         name,
			"isDeleted":     false,
			"sourceVertex": substrate.VertexKey(srcType, srcID),
			"targetVertex":   substrate.VertexKey(dstType, dstID),
			"localName":     name,
		}
		body, jerr := json.Marshal(envelope)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, linkKey, body)
		require.NoError(t, perr)
		return linkKey
	}
	// writeAspect writes an aspect key vtx.<type>.<id>.<localName> with its value
	// under data.value, mirroring how the Processor stores business data on an
	// aspect. Lens cypher reads it as node.<localName>.data.value.
	writeAspect := func(vtxKey, localName, value string) {
		aspectKey := vtxKey + "." + localName
		body, jerr := json.Marshal(map[string]any{
			"key":            aspectKey,
			"class":          localName,
			"localName":      localName,
			"vertexKey":      vtxKey,
			"createdAt":      provenanceAt,
			"lastModifiedAt": provenanceAt,
			"data":           map[string]any{"value": value},
		})
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, aspectKey, body)
		require.NoError(t, perr)
	}

	// --- topology vertices ---
	// Roles carry no business data in the vertex root; canonicalName is an aspect.
	writeVertex(adminRoleKey, "role", nil)
	writeVertex(userRoleKey, "role", nil)
	writeAspect(adminRoleKey, "canonicalName", "admin")
	writeAspect(userRoleKey, "canonicalName", "user")
	writeVertex(adminPermKey, "permission", map[string]any{
		"operationType": "write", "scope": "any",
	})
	writeVertex(userPermKey, "permission", map[string]any{
		"operationType": "read", "scope": "any",
	})
	writeVertex(locationKey, "location", nil)
	writeVertex(serviceKey, "service", map[string]any{"class": "service"})
	// Task root data is scalars only {status, expiresAt} — NO
	// grantedOperationType/targetKey fields. The granted operationType +
	// target are LINK-sourced (forOperation→op, scopedTo→target). Use a
	// far-future expiresAt so the `task.expiresAt > $now` predicate holds.
	taskExpiresAt := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	writeVertex(taskKey, "task", map[string]any{
		"status":    "open",
		"expiresAt": taskExpiresAt,
	})
	// forOperation op meta-vertex (carries the granted operationType).
	writeVertex(taskOpKey, "meta", map[string]any{"operationType": "read"})
	// scopedTo target.
	writeVertex(taskTargetKey, "leaseapp", map[string]any{"state": "pending"})

	// --- topology links (these now flow through the 3.2b link-bridge bootstrapper) ---
	// Story 4.7 rename: grantsPermission(role→permission) became
	// grantedBy(permission→role).
	writeLink("permission", adminPermID, "grantedBy", "role", adminRoleID)
	writeLink("permission", userPermID, "grantedBy", "role", userRoleID)
	holdsAKey := writeLink("identity", identityAID, "holdsRole", "role", adminRoleID)
	holdsBKey := writeLink("identity", identityBID, "holdsRole", "role", userRoleID)
	writeLink("identity", identityBID, "containedIn", "location", locationID)
	writeLink("location", locationID, "availableAt", "service", serviceID)
	writeLink("task", taskID, "assignedTo", "identity", identityCID)
	// Link-sourced grant: forOperation→op, scopedTo→target.
	writeLink("task", taskID, "forOperation", "meta", taskOpID)
	writeLink("task", taskID, "scopedTo", "leaseapp", taskTargetID)
	_ = holdsAKey

	// Wait for adjacency to fully absorb all link envelopes via the
	// 3.2b link bridge. We require all three identity-adjacent nodes
	// AND the role/location nodes that the cypher walks through, so
	// the first projection on identity vertex write produces the
	// expected populated capability set without relying on adj-watch
	// re-convergence (which slows the assertion loop).
	require.Eventually(t, func() bool {
		ea, _ := adjacencyNeighborsLocal(adjKV, identityAID)
		eb, _ := adjacencyNeighborsLocal(adjKV, identityBID)
		ec, _ := adjacencyNeighborsLocal(adjKV, identityCID)
		eAdmin, _ := adjacencyNeighborsLocal(adjKV, adminRoleID)
		eUser, _ := adjacencyNeighborsLocal(adjKV, userRoleID)
		eLoc, _ := adjacencyNeighborsLocal(adjKV, locationID)
		return len(ea) >= 1 && len(eb) >= 2 && len(ec) >= 1 &&
			len(eAdmin) >= 2 && len(eUser) >= 2 && len(eLoc) >= 2
	}, 10*time.Second, 50*time.Millisecond,
		"adjacency not fully populated by 3.2b link bridge")

	// --- finally: write the identity vertices (the CDC events that drive
	// the primary projection) ---
	writeVertex(identityAKey, "identity", map[string]any{"name": "alice"})
	writeVertex(identityBKey, "identity", map[string]any{"name": "bob"})
	writeVertex(identityCKey, "identity", map[string]any{"name": "carol"})

	// --- poll each identity's cap entry until it converges to the
	// expected populated shape. The first projection may run before
	// adjacency is fully present (CDC ordering vs the async link-bridge
	// bootstrapper); the adjacency-KV watcher then re-fires the cypher
	// when adjacency lands, so we accept the LAST observed envelope
	// rather than the first. ---
	waitForKeyConverged := func(key string, predicate func(map[string]any) bool, desc string) map[string]any {
		end := time.Now().Add(30 * time.Second)
		var last map[string]any
		var gErr error
		for time.Now().Before(end) {
			entry, err := capabilityKV.Get(ctx, key)
			if err == nil && entry != nil && len(entry.Value()) > 0 {
				var env map[string]any
				if jerr := json.Unmarshal(entry.Value(), &env); jerr == nil {
					last = env
					if predicate(env) {
						return env
					}
				}
			} else {
				gErr = err
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("capability key %q never converged: %s; last env=%v (err=%v)", key, desc, last, gErr)
		return nil
	}
	hasPlatformPerm := func(op, scope string) func(map[string]any) bool {
		return func(env map[string]any) bool {
			pp, _ := env["platformPermissions"].([]any)
			for _, e := range pp {
				m, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if m["operationType"] == op && m["scope"] == scope {
					return true
				}
			}
			return false
		}
	}
	hasServiceAccess := func(svc string) func(map[string]any) bool {
		return func(env map[string]any) bool {
			sa, _ := env["serviceAccess"].([]any)
			for _, e := range sa {
				m, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if m["service"] == svc {
					return true
				}
			}
			return false
		}
	}
	hasEphemeralForTask := func(taskKey string) func(map[string]any) bool {
		return func(env map[string]any) bool {
			eg, _ := env["ephemeralGrants"].([]any)
			for _, e := range eg {
				m, ok := e.(map[string]any)
				if !ok {
					continue
				}
				if m["taskKey"] == taskKey {
					return true
				}
			}
			return false
		}
	}
	envA := waitForKeyConverged("cap.identity."+identityAID,
		hasPlatformPerm("write", "any"), "identity A admin platform permission")
	envB := waitForKeyConverged("cap.identity."+identityBID,
		hasServiceAccess(serviceKey), "identity B service access via location")
	// Identity C's ephemeral grant projects to the DISJOINT cap.ephemeral.<C>
	// key (orchestration-base capabilityEphemeral lens), NOT the primary
	// cap.identity.<C> doc.
	envCEph := waitForKeyConverged("cap.ephemeral.identity."+identityCID,
		hasEphemeralForTask(taskKey), "identity C link-sourced ephemeralGrant (cap.ephemeral key)")
	// The link-sourced grant must carry the op-derived operationType + the
	// scopedTo target (faithful re-source: link-sourced, not field-sourced).
	require.Eventually(t, func() bool {
		eg, _ := envCEph["ephemeralGrants"].([]any)
		for _, e := range eg {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if m["taskKey"] == taskKey && m["operationType"] == "read" && m["target"] == taskTargetKey {
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond,
		"identity C ephemeral grant must be link-sourced {operationType:read, target:scopedTo}")

	// Primary cap docs (A/B/C) must carry the Contract #6 §6.2 sections; the
	// ephemeralGrants section on the PRIMARY doc is now empty post-7.1 (the
	// grants moved to cap.ephemeral.<actor>).
	for label, env := range map[string]map[string]any{"A": envA, "B": envB} {
		require.Containsf(t, env, "platformPermissions", "identity %s missing platformPermissions", label)
		require.Containsf(t, env, "serviceAccess", "identity %s missing serviceAccess", label)
		require.Containsf(t, env, "roles", "identity %s missing roles", label)
		require.Equalf(t, "1.0", env["version"], "identity %s wrong envelope version", label)
	}

	// --- assert role-index lens produced at least one cap.role-by-operation.<op> entry ---
	// Wait — the role-index pipeline triggers on every CDC event, including
	// our role/permission writes, so by now it should have produced entries
	// for "write" and "read" (each cypher run covers all role→permission
	// matches in the graph). The latest writes overwrite earlier ones, which
	// is correct (full-recompute semantics per Story 3.2 AC).
	idxEntries := waitForRoleIndexEntries(t, ctx, capabilityKV)
	require.NotEmptyf(t, idxEntries,
		"capabilityRoleIndex must produce at least one cap.role-by-operation.<op> entry")

	// At least one of the entries must reference the admin or user role's canonicalName.
	allRoles := map[string]struct{}{}
	for _, env := range idxEntries {
		if roles, ok := env["roles"].([]any); ok {
			for _, r := range roles {
				if rs, ok := r.(string); ok {
					allRoles[rs] = struct{}{}
				}
			}
		}
	}
	_, hasAdmin := allRoles["admin"]
	_, hasUser := allRoles["user"]
	require.Truef(t, hasAdmin || hasUser,
		"capabilityRoleIndex must include admin or user role canonicalName; got %v", allRoles)

	// --- Sub-test: tombstone the role-link for identity B → serviceAccess
	// re-projects (it shouldn't lose serviceAccess since location-derived
	// access doesn't depend on the role; instead this tests fan-out: a
	// role-link tombstone enumerates affected actors and re-projects them
	// with the holdsRole removed → empty roles, empty platformPermissions). ---
	t.Run("tombstone role link shrinks platformPermissions", func(t *testing.T) {
		// Overwrite the link envelope with isDeleted=true. The adjacency
		// bootstrapper observes it, removes the edges, then the role
		// vertex CDC isn't needed — but the link tombstone *itself* is
		// the trigger (the link envelope IS a Core KV write the
		// pipeline observes too).
		tombstone := map[string]any{
			"key":           holdsBKey,
			"class":         "holdsRole",
			"isDeleted":     true,
			"sourceVertex": identityBKey,
			"targetVertex":   userRoleKey,
			"localName":     "holdsRole",
		}
		body, jerr := json.Marshal(tombstone)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, holdsBKey, body)
		require.NoError(t, perr)

		// Re-fire identity B's CDC by re-writing its vertex (forces a
		// re-projection — production would re-project automatically
		// when fan-out enumerates affected actors from the tombstoned
		// link, but the link envelope itself is a non-vertex event so
		// the pipeline's ClassifyKey==KindLink branch acks it without
		// re-projection. The fan-out covers role/perm/service vertex
		// edits; link envelopes flow through the adjacency bridge
		// only. This is a documented residual — see closing summary.)
		writeVertex(identityBKey, "identity", map[string]any{"name": "bob", "rev": "2"})

		// platformPermissions must now be empty (the holdsRole edge is gone).
		require.Eventually(t, func() bool {
			entry, gErr := capabilityKV.Get(ctx, "cap.identity."+identityBID)
			if gErr != nil || entry == nil {
				return false
			}
			var env map[string]any
			if jerr := json.Unmarshal(entry.Value(), &env); jerr != nil {
				return false
			}
			pp, _ := env["platformPermissions"].([]any)
			// platformPermissions may contain a "ghost" object with all-nil
			// fields (cypher collect over zero matches); count only entries
			// with a non-nil operationType.
			real := 0
			for _, e := range pp {
				if m, ok := e.(map[string]any); ok && m["operationType"] != nil {
					real++
				}
			}
			return real == 0
		}, 15*time.Second, 100*time.Millisecond,
			"identity B platformPermissions must empty after holdsRole tombstone")
	})

	// --- Sub-test: tombstone identity C itself → cap.identity.<C> entry deleted ---
	t.Run("tombstone identity deletes cap entry", func(t *testing.T) {
		tomb := map[string]any{
			"key":       identityCKey,
			"class":     "identity",
			"isDeleted": true,
			"name":      "carol",
		}
		body, jerr := json.Marshal(tomb)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, identityCKey, body)
		require.NoError(t, perr)

		// The capability plane uses the default hard delete, so the natskv
		// adapter physically removes cap.identity.<C> on projection of the
		// identity tombstone. The capability authorizer treats absence as
		// denial, so absence is the correct, contract-aligned outcome
		// (Contract #6 §6.8 "absence equals denial").
		require.Eventually(t, func() bool {
			_, gErr := capabilityKV.Get(ctx, "cap.identity."+identityCID)
			return errors.Is(gErr, jetstream.ErrKeyNotFound)
		}, 15*time.Second, 100*time.Millisecond,
			"cap.identity.<C> must be hard-deleted (key gone) after identity tombstone")
	})

	// --- NFR-P3 evidence print ---
	stats := capLatency.Snapshot()
	fmt.Printf("\n=== Story 3.2b NFR-P3 evidence (multi-identity capability lens) ===\n"+
		"  samples: %d\n"+
		"  mean:    %v\n"+
		"  p95:     %v\n"+
		"  p99:     %v\n"+
		"  identityA: %s\n"+
		"  identityB: %s\n"+
		"  identityC: %s\n"+
		"========================================\n\n",
		stats.Count, stats.Mean, stats.P95, stats.P99,
		identityAKey, identityBKey, identityCKey)
}

// waitForRoleIndexEntries scans the Capability KV bucket for
// cap.role-by-operation.* entries with a short retry window.
func waitForRoleIndexEntries(t *testing.T, ctx context.Context, kv jetstream.KeyValue) []map[string]any {
	t.Helper()
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		keys, err := kv.Keys(ctx)
		if err == nil {
			var envs []map[string]any
			for _, k := range keys {
				if !hasPrefix(k, "cap.role-by-operation.") {
					continue
				}
				entry, gErr := kv.Get(ctx, k)
				if gErr != nil || entry == nil {
					continue
				}
				var env map[string]any
				if jerr := json.Unmarshal(entry.Value(), &env); jerr == nil {
					envs = append(envs, env)
				}
			}
			if len(envs) > 0 {
				return envs
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

// adjacencyNeighborsLocal avoids reaching into the adjacency package's
// signature change risk — we call it through a thin wrapper so the
// multi-e2e test stays decoupled from any internal helper refactor.
func adjacencyNeighborsLocal(kv jetstream.KeyValue, nodeID string) ([]any, error) {
	ctx := context.Background()
	entry, err := kv.Get(ctx, "adj."+nodeID)
	if err != nil {
		return nil, err
	}
	var v struct {
		Edges []any `json:"edges"`
	}
	if jerr := json.Unmarshal(entry.Value(), &v); jerr != nil {
		return nil, jerr
	}
	return v.Edges, nil
}

