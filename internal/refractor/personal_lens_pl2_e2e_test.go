// Package refractor_test — end-to-end proof for personal-secure-lens-design.md
// Fire 2 (PL.2): a "personal: true" nats_subject lens installs the
// ActorEnumerator cross-vertex fan-out, so a mutation on a NON-actor vertex
// (a lease) reaches every identity reachable via adjacency — the recipient is
// injected by the pipeline, not RETURNed by the lens's own cypher (PL.1's
// direct shape). The Interest Set relevance filter (personal-lens-interest)
// is exercised end-to-end through the real control-plane register/deregister
// RPCs.
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
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/refractor/adapter"
	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/control"
	"github.com/asolgan/lattice/internal/refractor/lens"
	"github.com/asolgan/lattice/internal/refractor/personalinterest"
	"github.com/asolgan/lattice/internal/refractor/pipeline"
	"github.com/asolgan/lattice/internal/refractor/projection"
	"github.com/asolgan/lattice/internal/refractor/ruleengine/full"
	"github.com/asolgan/lattice/internal/substrate"
)

// pl2Harness bundles the embedded-NATS fixtures shared by the PL.2 tests
// (and, via capKV, reused by the PL.3 security-gate suite in
// personal_lens_pl3_e2e_test.go — same package, same fixtures).
type pl2Harness struct {
	ctx        context.Context
	conn       *substrate.Conn
	js         jetstream.JetStream
	coreKV     *substrate.KV
	adjKV      *substrate.KV
	interestKV *substrate.KV
	capKV      *substrate.KV
	logger     *slog.Logger
}

func newPL2Harness(t *testing.T) *pl2Harness {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	t.Cleanup(conn.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	js := conn.JetStream()
	for _, bucket := range []string{"core-kv", "refractor-adjacency", "personal-lens-interest", "capability-kv"} {
		_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket})
		require.NoError(t, err)
	}
	coreKV, err := conn.OpenKV(ctx, "core-kv")
	require.NoError(t, err)
	adjKV, err := conn.OpenKV(ctx, "refractor-adjacency")
	require.NoError(t, err)
	interestKV, err := conn.OpenKV(ctx, "personal-lens-interest")
	require.NoError(t, err)
	capKV, err := conn.OpenKV(ctx, "capability-kv")
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	boots := consumer.NewBootstrapper(conn, "core-kv", adjKV)
	go func() { _ = boots.Run(ctx) }()
	select {
	case <-boots.Ready():
	case <-time.After(10 * time.Second):
		t.Fatal("adjacency bootstrapper did not reach Ready within 10s")
	}

	return &pl2Harness{ctx: ctx, conn: conn, js: js, coreKV: coreKV, adjKV: adjKV, interestKV: interestKV, capKV: capKV, logger: logger}
}

// activatePersonalLens writes a "personal: true" nats_subject lens spec and
// wires the real Refractor pipeline through the same path cmd/refractor's
// startPipeline uses: CoreKVSource → translateSpec → projection.IsPersonalLens
// → projection.InstallPersonalLens. capKV is threaded straight to
// InstallPersonalLens — nil from the PL.2 tests below (fan-out/Interest Set
// only, D1 gate disabled); the PL.3 suite passes h.capKV seeded with real
// grants (personal_lens_pl3_e2e_test.go).
func activatePersonalLens(t *testing.T, h *pl2Harness, lensID, cypher string, businessKeys []string, capKV *substrate.KV) (*pipeline.Pipeline, *adapter.NatsSubjectAdapter) {
	t.Helper()
	const subjectPrefix = "lattice.sync.user"
	const syncStream = "SYNC"

	adpt, err := adapter.NewNatsSubjectAdapter(h.ctx, h.conn, subjectPrefix, syncStream,
		append([]string{adapter.PersonalActorKeyField}, businessKeys...))
	require.NoError(t, err)

	p, err := pipeline.New(lensID, "nats_subject", "core-kv", h.adjKV, h.coreKV, adpt, nil)
	require.NoError(t, err)

	src := lens.NewCoreKVSource(h.conn, "core-kv", "test", h.logger)
	activated := make(chan *lens.Rule, 1)
	src.SetLoadCallback(func(r *lens.Rule) {
		select {
		case activated <- r:
		default:
		}
	})
	src.SetUpdateCallback(func(_, _ *lens.Rule, _ lens.UpdateKind) {})
	require.NoError(t, src.Start(h.ctx))

	metaVertexKey := "vtx.meta." + lensID
	specKey := metaVertexKey + ".spec"
	vertexJSON, _ := json.Marshal(map[string]any{"class": "meta.lens", "key": metaVertexKey, "data": map[string]any{}})
	_, err = h.coreKV.Put(h.ctx, metaVertexKey, vertexJSON)
	require.NoError(t, err)

	keyField := append([]string{adapter.PersonalActorKeyField}, businessKeys...)
	keyJSON, _ := json.Marshal(keyField)
	spec := lens.LensSpec{
		ID:            lensID,
		CanonicalName: "lens.e2e-personal-lens-pl2",
		TargetType:    "nats_subject",
		CypherRule:    cypher,
		TargetConfig: json.RawMessage(`{"subjectPrefix":"` + subjectPrefix + `","stream":"` + syncStream +
			`","personal":true,"key":` + string(keyJSON) + `}`),
	}
	specJSON, _ := json.Marshal(spec)
	_, err = h.coreKV.Put(h.ctx, specKey, specJSON)
	require.NoError(t, err)

	var r *lens.Rule
	select {
	case r = <-activated:
	case <-time.After(5 * time.Second):
		t.Fatal("CoreKVSource did not activate the personal lens within 5s of writes")
	}

	require.True(t, projection.IsPersonalLens(r), "lens must be recognized as a Fire-2 personal lens")
	require.True(t, ruleUsesFullEngine(t, r))
	p.UseFullEngine(fullEngineSingleton, r.CompiledRule)
	require.True(t, projection.InstallPersonalLens(p, r, h.adjKV, h.coreKV, h.interestKV, capKV, h.logger))

	p.RunOn(h.conn, e2eSpec(lensID, "core-kv"))
	pipelineCtx, pipelineCancel := context.WithCancel(h.ctx)
	go p.Run(pipelineCtx)
	t.Cleanup(pipelineCancel)

	return p, adpt
}

var fullEngineSingleton = full.New()

// pl2NanoID returns a deterministic 20-char NanoID (Contract #1 alphabet) for
// a given fixture seed string, mirroring stableLinkFanID in the capability
// link-fan-out e2e test.
func pl2NanoID(seed string) string {
	alphabet := substrate.Alphabet
	var h uint64 = 14695981039346656037
	for _, b := range []byte("pl2:" + seed) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	var out [20]byte
	for i := 0; i < 20; i++ {
		out[i] = alphabet[h%uint64(len(alphabet))]
		h = h*1099511628211 + 0x9E3779B97F4A7C15
	}
	return string(out[:])
}

func ruleUsesFullEngine(t *testing.T, r *lens.Rule) bool {
	t.Helper()
	_, ok := r.CompiledRule.(*full.CompiledRule)
	return ok
}

// writePL2Vertex writes a vertex with FLAT top-level properties (mirroring
// PL.1's e2e test convention — the personal lens cypher below reads l.<field>
// directly, not the nested node.data.<field> aspect-projection shape).
func writePL2Vertex(t *testing.T, h *pl2Harness, key, class string, fields map[string]any) {
	t.Helper()
	body := map[string]any{
		"key":            key,
		"class":          class,
		"createdAt":      "2026-07-04T00:00:00Z",
		"lastModifiedAt": "2026-07-04T00:00:00Z",
		"isDeleted":      false,
	}
	for k, v := range fields {
		body[k] = v
	}
	b, err := json.Marshal(body)
	require.NoError(t, err)
	_, err = h.coreKV.Put(h.ctx, key, b)
	require.NoError(t, err)
}

func writePL2Link(t *testing.T, h *pl2Harness, srcType, srcID, name, dstType, dstID string) {
	t.Helper()
	linkKey := substrate.LinkKey(srcType, srcID, name, dstType, dstID)
	envelope := map[string]any{
		"key": linkKey, "class": name, "isDeleted": false,
		"sourceVertex": substrate.VertexKey(srcType, srcID),
		"targetVertex": substrate.VertexKey(dstType, dstID),
		"localName":    name,
	}
	b, err := json.Marshal(envelope)
	require.NoError(t, err)
	_, err = h.coreKV.Put(h.ctx, linkKey, b)
	require.NoError(t, err)
}

// TestPersonalLens_PL2_E2E_VertexFanOutReachesLinkedIdentity is Fire 2's
// stated green bar: mutating a NON-actor vertex (a lease) that an identity
// reaches via adjacency re-executes the personal lens for that identity and
// publishes the updated delta to lattice.sync.user.<identity> — the
// recipient is injected by the pipeline; the lens cypher never RETURNs
// "__actor" itself (unlike PL.1's direct shape).
func TestPersonalLens_PL2_E2E_VertexFanOutReachesLinkedIdentity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("vertexfan-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("vertexfan-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)

	cypher := `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.monthlyRent AS monthlyRent`
	_, adpt := activatePersonalLens(t, h, pl2NanoID("vertexfan-lens"), cypher, []string{"entityId"}, nil)

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl2-1", "monthlyRent": 2000})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	cons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	// The link write itself fans out (evaluateLinkFanOut) and should already
	// deliver a first delta with the initial rent.
	msg, err := cons.Next(jetstream.FetchMaxWait(15 * time.Second))
	require.NoError(t, err, "the identity must receive a delta from the holds-link fan-out")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	require.Equal(t, "upsert", env["op"])
	data, ok := env["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(2000), data["monthlyRent"])

	// Now mutate the NON-actor lease vertex directly — Fire 2's actual ask:
	// evaluateFanOut must enumerate the identity via the already-built
	// adjacency edge and re-execute + republish.
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl2-1", "monthlyRent": 2400})

	require.Eventually(t, func() bool {
		msg, err := cons.Next(jetstream.FetchMaxWait(2 * time.Second))
		if err != nil {
			return false
		}
		var env map[string]any
		if json.Unmarshal(msg.Data(), &env) != nil {
			return false
		}
		data, ok := env["data"].(map[string]any)
		return ok && data["monthlyRent"] == float64(2400)
	}, 20*time.Second, 200*time.Millisecond,
		"a lease-vertex mutation must fan out to the linked identity via ActorEnumerator")

	require.NotNil(t, adpt, "adapter must have been constructed")
}

// TestPersonalLens_PL2_E2E_InterestSetFiltersThenAdmits proves the Interest
// Set relevance filter end-to-end through the real control-plane
// register/deregister RPCs: a device registered with a non-matching Types
// filter suppresses the delta; deregistering reverts to admit-all.
func TestPersonalLens_PL2_E2E_InterestSetFiltersThenAdmits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in -short mode")
	}
	h := newPL2Harness(t)

	recipient := pl2NanoID("interest-recipient")
	identityKey := substrate.VertexKey("identity", recipient)
	leaseID := pl2NanoID("interest-lease")
	leaseKey := substrate.VertexKey("lease", leaseID)

	cypher := `MATCH (identity {key: $actorKey})-[:holds]->(l:lease) ` +
		`RETURN l.key AS anchor, "lease" AS kind, l.id AS entityId, l.monthlyRent AS monthlyRent`
	_, _ = activatePersonalLens(t, h, pl2NanoID("interest-lens"), cypher, []string{"entityId"}, nil)

	// Register a device interested ONLY in "payment" — a "lease" delta must
	// be suppressed. Drive this through the real control-plane RPC.
	ctrlSvc := control.NewService()
	// Allow-all stub: this e2e drives the personal-lens projection path, not
	// capability enforcement (a nil/unconfigured checker fails closed).
	ctrlSvc.SetCapabilityChecker(control.NewStubCapabilityChecker(nil))
	ctrlSvc.SetPersonalInterestKV(h.interestKV)
	ctrlCtx, ctrlCancel := context.WithCancel(h.ctx)
	t.Cleanup(ctrlCancel)
	require.NoError(t, ctrlSvc.StartNATSListener(ctrlCtx, h.conn.NATS()))

	registerData, err := json.Marshal(control.ControlRequest{
		IdentityID: recipient, DeviceID: "deviceX", Types: []string{"payment"},
	})
	require.NoError(t, err)
	reply, err := h.conn.NATS().Request(control.ControlSubject("personal", "register"), registerData, 5*time.Second)
	require.NoError(t, err)
	var regResp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &regResp))
	require.Empty(t, regResp.Error)
	require.NotNil(t, regResp.PersonalRegister)
	require.True(t, regResp.PersonalRegister.Registered)

	writePL2Vertex(t, h, identityKey, "identity", map[string]any{"name": "recipient"})
	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl2-2", "monthlyRent": 3000})
	writePL2Link(t, h, "identity", recipient, "holds", "lease", leaseID)

	cons, err := h.js.CreateOrUpdateConsumer(h.ctx, "SYNC", jetstream.ConsumerConfig{
		FilterSubject: "lattice.sync.user." + recipient,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckPolicy:     jetstream.AckNonePolicy,
	})
	require.NoError(t, err)

	_, err = cons.Next(jetstream.FetchMaxWait(3 * time.Second))
	require.Error(t, err, "a mismatched Interest Set filter must suppress the lease delta")

	// Sanity: IsRelevant itself agrees (isolates the filter from any fan-out
	// timing flake in the assertion above).
	relevant, rerr := personalinterest.IsRelevant(h.ctx, h.interestKV, recipient, "lease", leaseKey)
	require.NoError(t, rerr)
	require.False(t, relevant)

	// Deregister — the identity must revert to admit-all and receive the
	// delta on the next mutation.
	deregisterData, err := json.Marshal(control.ControlRequest{IdentityID: recipient, DeviceID: "deviceX"})
	require.NoError(t, err)
	reply, err = h.conn.NATS().Request(control.ControlSubject("personal", "deregister"), deregisterData, 5*time.Second)
	require.NoError(t, err)
	var deregResp control.ControlResponse
	require.NoError(t, json.Unmarshal(reply.Data, &deregResp))
	require.Empty(t, deregResp.Error)
	require.True(t, deregResp.PersonalDeregister.Deregistered)

	writePL2Vertex(t, h, leaseKey, "lease", map[string]any{"id": "lease-pl2-2", "monthlyRent": 3100})

	msg, err := cons.Next(jetstream.FetchMaxWait(15 * time.Second))
	require.NoError(t, err, "after deregistering, the identity must receive the next delta")
	var env map[string]any
	require.NoError(t, json.Unmarshal(msg.Data(), &env))
	data, ok := env["data"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, float64(3100), data["monthlyRent"])
}
