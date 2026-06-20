package bridge_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/bridge"
	"github.com/asolgan/lattice/internal/substrate"
)

// The bridge-only FR58 proof. A bridge engine on embedded NATS consumes fixture
// external.<adapter> events and posts result ops; a fixture "Processor" on
// ops.<lane> models ONLY the Contract #4 dedup the bridge depends on (write the
// vtx.op.<requestId> tracker once; a repeat requestId is a no-op). The headline
// assertions are SideEffects == 1 under (a) event redelivery and (b)
// mid-flight-failure recovery, exercised with a NON-service claim token
// (invariant a — a hardcoded type would break these tests).

// --- Fixtures ---------------------------------------------------------------

const (
	coreKVBucket   = "core-kv"
	eventsStream   = "core-events"
	opsStream      = "core-operations"
	healthKVBucket = "health-kv"
	bridgeLane     = "system"
	// bridgeActorKey is a fixture service-actor key (the fixture Processor does
	// not auth; it mirrors loom_e2e_test's fixture actor).
	bridgeActorKey = "vtx.identity.BridgeServiceActor12abc"

	// Fixture external-call constants. The claim TYPE is NON-service (invariant
	// a): the bridge treats the instanceKey opaquely, so a widget token proves no
	// type is parsed. These mirror 13.2's external_e2e_test fixtures.
	fixtureAdapter    = "stripe"
	fixtureReplyOp    = "ResolveCharge"
	fixtureClaimTyp   = "widget" // non-service — invariant a
	fixtureAsyncName  = "asyncCheck"
	fixtureDispatchOp = "RecordWidgetDispatch" // the pending-marker op (non-service — invariant a)
)

func startNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir()}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

func provision(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()
	_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: coreKVBucket, LimitMarkerTTL: time.Second})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: healthKVBucket, LimitMarkerTTL: time.Second})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: opsStream, Subjects: []string{"ops.>"},
	})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: eventsStream, Subjects: []string{"events.>"},
		Retention: jetstream.LimitsPolicy, MaxAge: time.Hour, AllowAtomicPublish: true,
	})
	require.NoError(t, err)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// --- Fixture Processor ------------------------------------------------------
//
// A minimal stand-in for the Processor reproducing ONLY the Contract #4 dedup
// the bridge depends on: on a replyOp envelope, GET-or-create the tracker
// vtx.op.<requestId>. First time → write the tracker, count one resultMutation
// (and, modeling 14.4's replyOp DDL as a bonus, record the outcome as an aspect
// on the claim vertex while leaving its root data minimal — D5). A repeat
// requestId → tracker present → NO second mutation (the exactly-once guarantee
// the deterministic result-op requestId buys).
type fakeProcessor struct {
	conn        *substrate.Conn
	logger      *slog.Logger
	claimType   string // claim-vertex type for the bonus aspect write (non-service)
	replyAspect string // aspect localName the replyOp writes the outcome under

	resultMutations int64 // accepted (non-duplicate) replyOp commits — the exactly-once witness

	mu          sync.Mutex
	seenReqID   map[string]int // requestId → times seen (across redelivery)
	lastRef     map[string]string
	lastStatus  map[string]string // requestId → payload.status as posted by the bridge
	lastOp      map[string]string // requestId → operationType as posted by the bridge
	lastVendor  map[string]string // requestId → payload.vendorRef (set by the dispatch op only)
	dispatchOps int64             // accepted (non-duplicate) dispatchOp commits — the pending-marker witness
}

func newFakeProcessor(conn *substrate.Conn) *fakeProcessor {
	return &fakeProcessor{
		conn:        conn,
		logger:      testLogger(),
		claimType:   fixtureClaimTyp,
		replyAspect: "outcome",
		seenReqID:   map[string]int{},
		lastRef:     map[string]string{},
		lastStatus:  map[string]string{},
		lastOp:      map[string]string{},
		lastVendor:  map[string]string{},
	}
}

func (f *fakeProcessor) run(ctx context.Context, t *testing.T) {
	cons, err := f.conn.JetStream().CreateOrUpdateConsumer(ctx, opsStream, jetstream.ConsumerConfig{
		Durable:       "fake-processor",
		FilterSubject: "ops.>",
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err)
	go func() {
		mc, err := cons.Messages()
		if err != nil {
			return
		}
		go func() { <-ctx.Done(); mc.Stop() }()
		for {
			msg, err := mc.Next()
			if err != nil {
				return
			}
			f.handle(ctx, msg)
			_ = msg.Ack()
		}
	}()
}

func (f *fakeProcessor) handle(ctx context.Context, msg jetstream.Msg) {
	var env struct {
		RequestID     string `json:"requestId"`
		OperationType string `json:"operationType"`
		Payload       struct {
			ExternalRef string `json:"externalRef"`
			Status      string `json:"status"`
			Result      string `json:"result"`
			VendorRef   string `json:"vendorRef"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data(), &env); err != nil {
		return
	}

	f.mu.Lock()
	f.seenReqID[env.RequestID]++
	f.lastRef[env.RequestID] = env.Payload.ExternalRef
	f.lastStatus[env.RequestID] = env.Payload.Status
	f.lastOp[env.RequestID] = env.OperationType
	f.lastVendor[env.RequestID] = env.Payload.VendorRef
	f.mu.Unlock()

	// Contract #4 dedup: write the tracker once; a repeat requestId is a no-op.
	if !f.trackOnce(ctx, env.RequestID) {
		return
	}

	// The pending-marker op (dispatchOp): record the .dispatch aspect
	// {vendorRef} (mirrors the RecordServiceDispatch package op), count a
	// dispatchOp commit, and post NO .outcome — the task stays parked.
	if env.OperationType == fixtureDispatchOp {
		atomic.AddInt64(&f.dispatchOps, 1)
		if env.Payload.ExternalRef != "" {
			aspectKey := "vtx." + f.claimType + "." + env.Payload.ExternalRef + ".dispatch"
			aspectBody, _ := json.Marshal(map[string]any{
				"class":     f.claimType + ".dispatch",
				"vertexKey": "vtx." + f.claimType + "." + env.Payload.ExternalRef,
				"localName": "dispatch",
				"data":      map[string]any{"vendorRef": env.Payload.VendorRef},
			})
			_, _ = f.conn.KVPut(ctx, coreKVBucket, aspectKey, aspectBody)
		}
		return
	}

	atomic.AddInt64(&f.resultMutations, 1)

	// Bonus (mirrors the 14.4 replyOp DDL): record the outcome as an ASPECT on
	// the claim vertex, leaving its root data minimal (D5). The bridge does NOT
	// do this — it only posts the replyOp.
	if env.Payload.ExternalRef != "" {
		aspectKey := "vtx." + f.claimType + "." + env.Payload.ExternalRef + "." + f.replyAspect
		aspectBody, _ := json.Marshal(map[string]any{
			"class":     f.claimType + "." + f.replyAspect,
			"vertexKey": "vtx." + f.claimType + "." + env.Payload.ExternalRef,
			"localName": f.replyAspect,
			"data":      map[string]any{"result": env.Payload.Result},
		})
		_, _ = f.conn.KVPut(ctx, coreKVBucket, aspectKey, aspectBody)
	}
}

// trackOnce writes the Contract #4 tracker; returns false if it already exists
// (a duplicate / redelivery collapsed on the tracker).
func (f *fakeProcessor) trackOnce(ctx context.Context, requestID string) bool {
	_, err := f.conn.KVCreate(ctx, coreKVBucket, "vtx.op."+requestID, []byte(`{"class":"op","data":{}}`))
	return err == nil
}

func (f *fakeProcessor) mutations() int { return int(atomic.LoadInt64(&f.resultMutations)) }

// dispatchMutations is the count of accepted (non-duplicate) dispatchOp commits —
// the pending-marker witness, the analogue of mutations() for the Pending path.
func (f *fakeProcessor) dispatchMutations() int { return int(atomic.LoadInt64(&f.dispatchOps)) }

func (f *fakeProcessor) sawReply(requestID string) (count int, externalRef string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seenReqID[requestID], f.lastRef[requestID]
}

// sawOp returns the operationType the bridge posted under requestID (empty until
// an op with that id has been seen) and the times it was seen across redelivery.
func (f *fakeProcessor) sawOp(requestID string) (op string, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOp[requestID], f.seenReqID[requestID]
}

// sawVendorRef returns the payload.vendorRef the bridge posted under requestID
// (set only by the dispatchOp; empty for a replyOp).
func (f *fakeProcessor) sawVendorRef(requestID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastVendor[requestID]
}

// sawStatus returns the payload.status the bridge posted on the replyOp for
// requestID (empty until a reply has been seen).
func (f *fakeProcessor) sawStatus(requestID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastStatus[requestID]
}

// --- Event publisher --------------------------------------------------------

// publishExternalEvent publishes one external.<adapter> event in the FULL Event
// envelope shape the instanceOp's outbox produces (a top-level requestId plus a
// payload object — the bridge reads the business fields from payload). The
// instanceKey is a NON-service bare handle (invariant a).
func publishExternalEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, adapter, instanceKey, replyOp string, params map[string]any) {
	t.Helper()
	rawParams, _ := json.Marshal(params)
	payload := map[string]any{
		"instanceKey":    instanceKey,
		"adapter":        adapter,
		"params":         json.RawMessage(rawParams),
		"replyOp":        replyOp,
		"idempotencyKey": instanceKey,
		"externalRef":    instanceKey,
	}
	ev := map[string]any{
		"eventId":   mustNanoID(t),
		"requestId": mustNanoID(t), // the instanceOp's own requestId — NOT what the bridge correlates on
		"eventType": "external." + adapter,
		"payload":   payload,
		"timestamp": substrate.FormatTimestamp(time.Now()),
	}
	data, _ := json.Marshal(ev)
	_, err := conn.JetStream().Publish(ctx, "events.external."+adapter, data)
	require.NoError(t, err)
}

// publishAsyncExternalEvent publishes an external.<adapter> event that ALSO
// carries the dispatchOp field — the seam the bridge posts on a Pending outcome.
// Same FULL envelope shape as publishExternalEvent; the instanceKey is a
// NON-service bare handle (invariant a).
func publishAsyncExternalEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, adapter, instanceKey, replyOp, dispatchOp string, params map[string]any) {
	t.Helper()
	rawParams, _ := json.Marshal(params)
	payload := map[string]any{
		"instanceKey":    instanceKey,
		"adapter":        adapter,
		"params":         json.RawMessage(rawParams),
		"replyOp":        replyOp,
		"dispatchOp":     dispatchOp,
		"idempotencyKey": instanceKey,
		"externalRef":    instanceKey,
	}
	ev := map[string]any{
		"eventId":   mustNanoID(t),
		"requestId": mustNanoID(t),
		"eventType": "external." + adapter,
		"payload":   payload,
		"timestamp": substrate.FormatTimestamp(time.Now()),
	}
	data, _ := json.Marshal(ev)
	_, err := conn.JetStream().Publish(ctx, "events.external."+adapter, data)
	require.NoError(t, err)
}

// publishRawExternalEvent publishes an arbitrary body to events.external.<adapter>
// (for the unparseable-envelope case).
func publishRawExternalEvent(t *testing.T, ctx context.Context, conn *substrate.Conn, adapter string, body []byte) {
	t.Helper()
	_, err := conn.JetStream().Publish(ctx, "events.external."+adapter, body)
	require.NoError(t, err)
}

// --- Bridge starter ---------------------------------------------------------

// startBridge constructs and starts a bridge engine with the FakeStripe adapter
// registered as "stripe", returning the engine and the adapter handle. cfgMut
// lets a test tweak the Config (e.g. disable the skip-probe, shorten the
// redelivery floor) before Start.
func startBridge(t *testing.T, ctx context.Context, conn *substrate.Conn, stripe *bridge.FakeStripe, cfgMut func(*bridge.Config)) *bridge.Engine {
	t.Helper()
	skip := true
	cfg := bridge.Config{
		CoreKVBucket:     coreKVBucket,
		EventsStream:     eventsStream,
		HealthKVBucket:   healthKVBucket,
		ActorKey:         bridgeActorKey,
		Lane:             bridgeLane,
		Instance:         "bridge-" + mustNanoID(t),
		HeartbeatEvery:   150 * time.Millisecond, // fast, so the test can read Issues[]
		RedeliveryDelay:  300 * time.Millisecond, // fast NakWithDelay redelivery
		SkipOnRedelivery: &skip,
	}
	if cfgMut != nil {
		cfgMut(&cfg)
	}
	eng := bridge.NewEngine(conn, cfg)
	require.NoError(t, eng.RegisterAdapter(fixtureAdapter, stripe))

	engCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = eng.Start(engCtx) }()
	return eng
}

// startBridgeWithAdapter constructs and starts a bridge engine with a single
// caller-provided adapter registered under name (the async tests register a
// FakeAsyncCheck). Same Config defaults + cfgMut seam as startBridge.
func startBridgeWithAdapter(t *testing.T, ctx context.Context, conn *substrate.Conn, name string, adapter bridge.Adapter, cfgMut func(*bridge.Config)) *bridge.Engine {
	t.Helper()
	skip := true
	cfg := bridge.Config{
		CoreKVBucket:     coreKVBucket,
		EventsStream:     eventsStream,
		HealthKVBucket:   healthKVBucket,
		ActorKey:         bridgeActorKey,
		Lane:             bridgeLane,
		Instance:         "bridge-" + mustNanoID(t),
		HeartbeatEvery:   150 * time.Millisecond,
		RedeliveryDelay:  300 * time.Millisecond,
		SkipOnRedelivery: &skip,
	}
	if cfgMut != nil {
		cfgMut(&cfg)
	}
	eng := bridge.NewEngine(conn, cfg)
	require.NoError(t, eng.RegisterAdapter(name, adapter))

	engCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go func() { _ = eng.Start(engCtx) }()
	return eng
}

// healthInstance extracts the configured instance from a started bridge by
// reading the only health.bridge.* heartbeat doc present (the harness starts one
// bridge per test).
func waitHealthIssue(t *testing.T, ctx context.Context, conn *substrate.Conn, code string) bool {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		keys, err := conn.KVListKeys(ctx, healthKVBucket)
		if err == nil {
			for _, k := range keys {
				if !isBridgeHeartbeatKey(k) {
					continue
				}
				entry, err := conn.KVGet(ctx, healthKVBucket, k)
				if err != nil {
					continue
				}
				var doc struct {
					Issues []struct {
						Severity string `json:"severity"`
						Code     string `json:"code"`
						Message  string `json:"message"`
					} `json:"issues"`
				}
				if json.Unmarshal(entry.Value, &doc) != nil {
					continue
				}
				for _, iss := range doc.Issues {
					if iss.Code == code {
						return true
					}
				}
			}
		}
		time.Sleep(80 * time.Millisecond)
	}
	return false
}

// isBridgeHeartbeatKey reports whether k is a bridge heartbeat doc
// (health.bridge.<instance>) and NOT a per-consumer pause-state entry
// (health.bridge.<instance>.consumer.<name>). The instance is a dot-free token,
// so exactly three dot-separated segments after "health.bridge." identifies the
// heartbeat; a consumer entry has more.
func isBridgeHeartbeatKey(k string) bool {
	const prefix = "health.bridge."
	if len(k) <= len(prefix) || k[:len(prefix)] != prefix {
		return false
	}
	rest := k[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			return false // a sub-key (…​.consumer.<name>), not the heartbeat
		}
	}
	return true
}

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

// nonServiceHandle returns a bare NanoID handle for a NON-service claim type
// (invariant a — the value Loom would mint for vtx.widget.<handle>). The bridge
// treats it opaquely; a hardcoded type assumption would break every test that
// uses it.
func nonServiceHandle(t *testing.T) string { return mustNanoID(t) }
