// Capability Lens link fan-out e2e — a pure link mutation (holdsRole,
// grantedBy) must drive a fan-out reprojection of the affected actors even
// though it carries no vertex change.
//
// This exercises the dispatch added for the M5 capability-lens link fan-out:
// processMsg routes KindLink CDC events (create AND tombstone) into
// evaluateLinkFanOut when an ActorEnumerator is installed, seeding the
// reprojection from BOTH link endpoints. The identity is projected FIRST
// (before it holds any role), then the topology links are written — mirroring
// the Hello Lattice M5 sequence (AssignRole/GrantPermission after the actor
// already exists). Without the fix the actor's cap doc never gains the
// permission because nothing reprojects it on a pure link event.
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

// stableLinkFanID returns a deterministic NanoID for a link-fan-out fixture role.
func stableLinkFanID(role string) string {
	alphabet := substrate.Alphabet
	var seed uint64 = 14695981039346656037
	for _, b := range []byte("linkfanout:" + role) {
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

// TestRefractor_CapabilityLens_LinkFanOut_E2E projects an identity first, then
// writes pure link mutations (holdsRole, grantedBy) and asserts the identity's
// capability doc gains the role's permission via link fan-out, then loses it on
// link tombstone (revocation).
func TestRefractor_CapabilityLens_LinkFanOut_E2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping link-fan-out capability e2e test in -short mode")
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
	identityID := stableLinkFanID("agent")
	roleID := stableLinkFanID("author")
	permID := stableLinkFanID("create-book")

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
	writeLink := func(srcType, srcID, name, dstType, dstID string, isDeleted bool) string {
		linkKey := substrate.LinkKey(srcType, srcID, name, dstType, dstID)
		envelope := map[string]any{
			"key":           linkKey,
			"class":         name,
			"isDeleted":     isDeleted,
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

	// Seed role + permission vertices, then the identity LAST so it projects
	// with an EMPTY capability set (no role yet) — mirroring an agent created
	// before any AssignRole/GrantPermission.
	writeVertex(roleKey, "role", map[string]any{"canonicalName": "author"})
	writeVertex(permKey, "permission", map[string]any{
		"operationType": "create",
		"scope":         "book",
	})
	writeVertex(identityKey, "identity", map[string]any{"name": "agent"})

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
	getEnv := func() (map[string]any, bool) {
		entry, gErr := capabilityKV.Get(ctx, capKey)
		if gErr != nil || entry == nil || len(entry.Value()) == 0 {
			return nil, false
		}
		var env map[string]any
		if jerr := json.Unmarshal(entry.Value(), &env); jerr != nil {
			return nil, false
		}
		return env, true
	}

	// The identity cap doc must project (empty permission set).
	require.Eventually(t, func() bool {
		env, ok := getEnv()
		return ok && !hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must project with no create/book permission before any role grant")

	// (b) grantedBy create: permission → role. No actor holds the role yet,
	// so this fans out to zero actors — a correct no-op. The cap doc must
	// still NOT have the permission.
	writeLink("permission", permID, "grantedBy", "role", roleID, false)

	// (a) holdsRole create: identity → role. This pure link mutation must
	// fan out to the identity and reproject it; the cap doc gains the role's
	// permission (create/book) via grantedBy → permission.
	holdsKey := writeLink("identity", identityID, "holdsRole", "role", roleID, false)

	require.Eventually(t, func() bool {
		env, ok := getEnv()
		return ok && hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must gain create/book permission after holdsRole link fan-out")

	// roles must include the role key.
	env, ok := getEnv()
	require.True(t, ok)
	roles, _ := env["roles"].([]any)
	foundRole := false
	for _, r := range roles {
		if r == roleKey {
			foundRole = true
			break
		}
	}
	require.True(t, foundRole, "roles must include the granted role after holdsRole fan-out: %v", roles)

	// (c) holdsRole tombstone (revocation): re-write the link with
	// isDeleted=true. The link fan-out must reproject the identity with the
	// holdsRole edge removed → permission lost.
	writeLink("identity", identityID, "holdsRole", "role", roleID, true)
	_ = holdsKey

	require.Eventually(t, func() bool {
		env, ok := getEnv()
		if !ok {
			return false
		}
		return !hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must lose create/book permission after holdsRole tombstone fan-out")

	// (c') grantedBy tombstone via NATS DEL (empty body). Re-grant the role
	// first so the actor reaches the permission again, then physically delete
	// the grantedBy link key — the empty-body link tombstone must still fan
	// out and shrink the cap doc.
	writeLink("identity", identityID, "holdsRole", "role", roleID, false)
	require.Eventually(t, func() bool {
		env, ok := getEnv()
		return ok && hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must regain create/book after re-grant")

	grantedByKey := substrate.LinkKey("permission", permID, "grantedBy", "role", roleID)
	require.NoError(t, coreKV.Delete(ctx, grantedByKey))

	require.Eventually(t, func() bool {
		env, ok := getEnv()
		if !ok {
			return false
		}
		return !hasCreateBook(env)
	}, 20*time.Second, 100*time.Millisecond,
		"identity cap doc must lose create/book after grantedBy DEL tombstone fan-out")
}
