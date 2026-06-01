// Capability Lens aspect fan-out e2e — an aspect-only mutation (e.g. identity
// .state, role .description) must drive a fan-out reprojection of the affected
// actors even though it carries no vertex-root change and no topology change.
//
// This exercises the dispatch added for the aspect fan-out: processMsg routes
// KindAspect CDC events into evaluateAspectFanOut when an ActorEnumerator is
// installed, seeding the reprojection from the aspect's PARENT vertex. When the
// parent is itself an actor (identity .state) the enumerator returns it as a
// singleton; when the parent is a non-actor vertex (role .description) the
// enumerator walks adjacency to the actors that reach it.
//
// Because the seeded capability cypher does not read these particular aspect
// values, the cap doc CONTENT is unchanged by the reprojection. The observable
// proof that the fan-out fired is therefore a Capability KV revision bump (the
// adapter writes unconditionally), asserted while the permission set stays
// correct. Without the fix, aspect CDC is acked-and-dropped and no reprojection
// ever occurs, so the revision never advances.
package refractor_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
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
)

// stableAspectFanID returns a deterministic NanoID for an aspect-fan-out fixture.
func stableAspectFanID(role string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 14695981039346656037
	for _, b := range []byte("aspectfanout:" + role) {
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

// TestRefractor_CapabilityLens_AspectFanOut_E2E projects an identity that holds
// a role granting a permission, then mutates aspects (role .description, then
// identity .state) and asserts each aspect-only mutation re-emits the identity's
// capability doc (revision bump) while the permission set stays intact.
func TestRefractor_CapabilityLens_AspectFanOut_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping aspect-fan-out capability e2e test in -short mode")
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

	// --- adjacency bootstrapper (with link-bridge) ---
	boots := consumer.NewBootstrapper(js, bootstrap.CoreKVBucket, adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	manager := consumer.NewManager(js, bootstrap.CoreKVBucket)

	// --- CoreKVSource activation: collect the capability lens ---
	src := lens.NewCoreKVSource(conn, bootstrap.CoreKVBucket, logger)
	loaded := make(chan *lens.Rule, 8)
	src.SetLoadCallback(func(r *lens.Rule) { loaded <- r })
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(ctx))

	var capabilityRule *lens.Rule
	deadline := time.Now().Add(15 * time.Second)
	for capabilityRule == nil {
		if time.Now().After(deadline) {
			t.Fatal("did not load the capability lens within 15s")
		}
		select {
		case r := <-loaded:
			if r.CanonicalName == "capability" {
				capabilityRule = r
			}
		case <-time.After(200 * time.Millisecond):
		}
	}

	// --- primary capability pipeline (full engine + envelope + fan-out) ---
	fullEngine := full.New()
	projectionRevision := func(k string) uint64 {
		entry, gErr := coreKV.Get(ctx, k)
		if gErr != nil || entry == nil {
			return 0
		}
		return entry.Revision()
	}

	capTargetKV, err := js.KeyValue(ctx, capabilityRule.Into.Bucket)
	require.NoError(t, err)
	capAdpt, err := adapter.New(capTargetKV, capabilityRule.Into.Key)
	require.NoError(t, err)

	capP, err := pipeline.New(capabilityRule.ID, "nats_kv",
		nil, bootstrap.CoreKVBucket, adjKV, coreKV, capAdpt, nil)
	require.NoError(t, err)
	require.Equal(t, ruleengine.EngineFull, capabilityRule.ResolvedEngine)
	require.NotNil(t, capabilityRule.CompiledRule)
	capP.UseFullEngine(fullEngine, capabilityRule.CompiledRule)
	capP.SetEnvelopeFn(capabilityenv.NewWrapper("vtx.meta."+capabilityRule.ID, projectionRevision))
	capP.SetActorEnumerator(pipeline.NewActorEnumerator(adjKV, coreKV, capabilityenv.IdentityType))

	require.NoError(t, manager.Add(ctx, capabilityRule.ID))
	capCons := manager.Consumer(capabilityRule.ID)
	require.NotNil(t, capCons)

	pipelineCtx, pipelineCancel := context.WithCancel(ctx)
	capDone := make(chan struct{})
	go func() { defer close(capDone); capP.Run(pipelineCtx, capCons) }()
	t.Cleanup(func() {
		pipelineCancel()
		<-capDone
	})

	// --- fixture identity / role / permission ---
	identityID := stableAspectFanID("agent")
	roleID := stableAspectFanID("author")
	permID := stableAspectFanID("create-book")

	identityKey := substrate.VertexKey("identity", identityID)
	roleKey := substrate.VertexKey("role", roleID)
	permKey := substrate.VertexKey("permission", permID)

	const provenanceAt = "2026-05-15T10:00:00Z"
	// Domain fields live under the `data` envelope, mirroring how the Processor
	// commits a vertex (provenance fields top-level, script document under data).
	// Lens cypher rules read them as node.data.<field>.
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
	writeLink := func(srcType, srcID, name, dstType, dstID string) {
		linkKey := substrate.LinkKey(srcType, srcID, name, dstType, dstID)
		envelope := map[string]any{
			"key":           linkKey,
			"class":         name,
			"isDeleted":     false,
			"youngerVertex": substrate.VertexKey(srcType, srcID),
			"olderVertex":   substrate.VertexKey(dstType, dstID),
			"localName":     name,
		}
		body, jerr := json.Marshal(envelope)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, linkKey, body)
		require.NoError(t, perr)
	}
	// writeAspect writes a 4-segment aspect key (KindAspect) for a vertex.
	writeAspect := func(vtxKey, localName string, value map[string]any) {
		aspectKey := vtxKey + "." + localName
		data, jerr := json.Marshal(value)
		require.NoError(t, jerr)
		_, perr := coreKV.Put(ctx, aspectKey, data)
		require.NoError(t, perr)
	}

	// Seed role + permission, the identity, and the topology so the identity's
	// cap doc carries the create/book permission.
	writeVertex(roleKey, "role", map[string]any{"canonicalName": "author"})
	writeVertex(permKey, "permission", map[string]any{
		"operationType": "create",
		"scope":         "book",
	})
	writeVertex(identityKey, "identity", map[string]any{"name": "agent"})
	writeLink("permission", permID, "grantedBy", "role", roleID)
	writeLink("identity", identityID, "holdsRole", "role", roleID)

	capKey := "cap.identity." + identityID
	hasCreateBook := func(env map[string]any) bool {
		pp, _ := env["platformPermissions"].([]any)
		for _, e := range pp {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if m["operationType"] == "create" && m["scope"] == "book" {
				return true
			}
		}
		return false
	}
	getEntry := func() (map[string]any, uint64, bool) {
		entry, gErr := capabilityKV.Get(ctx, capKey)
		if gErr != nil || entry == nil || len(entry.Value()) == 0 {
			return nil, 0, false
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value(), &env); jerr != nil {
			return nil, 0, false
		}
		return env, entry.Revision(), true
	}

	// The identity cap doc must project WITH the create/book permission via the
	// holdsRole → grantedBy chain (links are fanned out by the link path).
	require.Eventually(t, func() bool {
		env, _, ok := getEntry()
		return ok && hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must carry create/book before any aspect mutation")

	// Settle: capture the revision after projection has quiesced so a later
	// bump is unambiguously attributable to the aspect mutation under test.
	settle := func() uint64 {
		var last uint64
		require.Eventually(t, func() bool {
			_, rev, ok := getEntry()
			if !ok {
				return false
			}
			if rev == last {
				return true // two stable reads in a row
			}
			last = rev
			return false
		}, 20*time.Second, 150*time.Millisecond, "cap doc revision did not settle")
		return last
	}

	// (a) Non-actor aspect fan-out: mutate the role's .description aspect. The
	// parent vertex is a role (not an actor), so the fan-out must walk adjacency
	// holdsRole → identity and reproject it. Cap doc must be re-emitted
	// (revision bump) with the permission set unchanged.
	revBeforeRole := settle()
	writeAspect(roleKey, "description", map[string]any{"value": "Authors may create books"})
	require.Eventually(t, func() bool {
		env, rev, ok := getEntry()
		return ok && rev > revBeforeRole && hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"role .description aspect mutation must reproject the holder's cap doc (revision bump), permission intact")

	// (b) Actor aspect fan-out: mutate the identity's own .state aspect. The
	// parent vertex IS the actor, so the enumerator returns it as a singleton
	// and only that actor is reprojected. Cap doc must be re-emitted, permission
	// still intact.
	revBeforeState := settle()
	writeAspect(identityKey, "state", map[string]any{"value": "claimed"})
	require.Eventually(t, func() bool {
		env, rev, ok := getEntry()
		return ok && rev > revBeforeState && hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"identity .state aspect mutation must reproject the actor's own cap doc (revision bump), permission intact")
}
