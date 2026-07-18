package loom_test

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

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/opstatus"
	"github.com/asolgan/lattice/internal/substrate"
)

// --- Embedded NATS + provisioning -------------------------------------------

func startNATS(t *testing.T) *nats.Conn {
	t.Helper()
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	t.Cleanup(s.Shutdown)
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	return nc
}

const (
	coreKVBucket    = "core-kv"
	loomStateBucket = "loom-state"
	eventsStream    = "core-events"
	opsStream       = "core-operations"
	loomActorKey    = "vtx.identity.LoomServiceActor123abc" // fixture actor key (fake processor does not auth)
)

func provision(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()
	// loom-state must allow atomic publish: Loom's per-transition AtomicBatch
	// (Contract #10 §10.3) requires it, exactly as bootstrap provisions it.
	for _, b := range []string{coreKVBucket, loomStateBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b, LimitMarkerTTL: time.Second})
		require.NoError(t, err)
		stream, err := js.Stream(ctx, "KV_"+b)
		require.NoError(t, err)
		cfg := stream.CachedInfo().Config
		cfg.AllowAtomicPublish = true
		_, err = js.UpdateStream(ctx, cfg)
		require.NoError(t, err)
	}
	_, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
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

// --- Fake Processor ---------------------------------------------------------
//
// A minimal stand-in for the Processor that reproduces exactly the contract
// seams Loom depends on (Contract #10 §10.6 + §10.9), no more. It publishes the
// FULL Event envelope shape the outbox publishes: a top-level `requestId` plus a
// `payload` object — Loom reads requestId top-level (completion correlation) and
// the business fields under payload (the patternStarted trigger).
//
//   - StartLoomPattern (event-only) → emits events.loom.patternStarted with
//     payload {instanceId=requestId, patternRef, subjectKey}. No mutation.
//   - CompletePattern / FailPattern (event-only) → emits
//     events.loom.patternCompleted / events.loom.patternFailed. No mutation.
//   - any other op (systemOp) → writes the Contract #4 vtx.op.<requestId>
//     tracker (dedup), replies "accepted", and publishes ONE business event
//     whose top-level requestId is the committed terminal Loom correlates on. A
//     repeat requestId replies "duplicate" and publishes NO second event
//     (idempotent — the exactly-once guarantee).
//   - an operationType in rejectOps → replies "rejected" with NO tracker and NO
//     event (the off-stream failed terminal, AC #4).
type fakeProcessor struct {
	conn      *substrate.Conn
	logger    *slog.Logger
	eventFor  func(op string) string // systemOp operationType → full business event class
	rejectOps map[string]struct{}
	submitted int64 // count of accepted (non-duplicate) systemOp commits, for exactly-once
	gate      <-chan struct{}

	// taskOps is the set of bound-op operationTypes that, on commit, simulate the
	// commit-path auto-complete: they emit TaskCompleted(taskKey) read from the
	// op's authContext.task (the task-auth path). Empty for systemOp-only tests.
	taskOps map[string]struct{}

	// externalTask fixtures. These stand in for the real instanceOp/
	// replyOp DDLs and the bridge:
	//   - instanceOps: operationTypes that, on commit, mint the claim vertex
	//     vtx.<claimType>.<payload.instanceKey> in Core KV (a package-chosen
	//     NON-service type proves the engine names no type — invariant a), write
	//     the Contract #4 tracker, and emit NO completion event (the real op
	//     would emit external.<adapter> for the bridge; here replyOp models the
	//     bridge's reply directly).
	//   - replyOps: operationTypes that, on commit, model the bridge posting the
	//     result back — record the outcome as an ASPECT on the claim vertex
	//     (vtx.<claimType>.<handle>.<replyAspect>; the root data stays minimal —
	//     invariant b / D5) and emit orchestration.externalTaskCompleted carrying
	//     payload.externalRef = the handle (the uniform orchestration-domain
	//     completion signal, symmetric to a userTask's taskCompleted; §10.5/§10.6).
	instanceOps map[string]struct{}
	replyOps    map[string]struct{}
	claimType   string // claim-vertex type the instanceOp mints (e.g. "widget"); non-"service"
	replyAspect string // aspect localName the replyOp writes the outcome under
	replyEvent  string // completion event class the replyOp emits (orchestration.externalTaskCompleted)

	mu              sync.Mutex
	createdTasks    map[string]struct{} // taskKey → minted (CreateTask)
	createTasks     int                 // count of accepted (non-duplicate) CreateTask commits
	createdInstance int                 // count of accepted (non-duplicate) instanceOp commits
}

const replyInboxHeader = "Lattice-Reply-Inbox"

// startOpStatusResponder hosts the lattice.op.status RPC (Processor-hosted in
// production; op-status-read-surface-design.md) on conn, backed by
// coreKVBucket — the same bucket fakeProcessor.trackOnce writes the Contract
// #4 tracker to — so Loom's deadline-probe trackerExists (an RPC as of
// Fire 3, never a direct KVGet) has something to answer it in these embedded-
// NATS tests. Started once per fakeProcessor, alongside its op-commit loop.
func startOpStatusResponder(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	svc := opstatus.NewService(conn, coreKVBucket, testLogger())
	require.NoError(t, svc.StartNATSListener(ctx, conn.NATS()))
}

func (f *fakeProcessor) run(ctx context.Context, t *testing.T) {
	startOpStatusResponder(t, ctx, f.conn)
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
		RequestID     string          `json:"requestId"`
		OperationType string          `json:"operationType"`
		Payload       json.RawMessage `json:"payload"`
		AuthContext   struct {
			Task string `json:"task"`
		} `json:"authContext"`
	}
	if err := json.Unmarshal(msg.Data(), &env); err != nil {
		return
	}
	inbox := msg.Headers().Get(replyInboxHeader)

	reply := func(status, code string) {
		if inbox == "" {
			return
		}
		body := map[string]any{"requestId": env.RequestID, "status": status}
		if code != "" {
			body["error"] = map[string]string{"code": code, "message": code}
		}
		b, _ := json.Marshal(body)
		_ = f.conn.NATS().Publish(inbox, b)
	}

	// publishEvent emits the full Event envelope the outbox produces.
	publishEvent := func(class string, payload map[string]any) {
		ev := map[string]any{
			"eventId":   mustNanoIDStr(),
			"requestId": env.RequestID,
			"eventType": class,
			"payload":   payload,
			"timestamp": substrate.FormatTimestamp(time.Now()),
		}
		eb, _ := json.Marshal(ev)
		_, _ = f.conn.JetStream().Publish(context.Background(), "events."+class, eb)
	}

	if _, rej := f.rejectOps[env.OperationType]; rej {
		// Off-stream failed terminal: no tracker, no event.
		reply("rejected", "AuthContextMismatch")
		return
	}

	// Event-only lifecycle ops (Contract #10 §10.9): no tracker accounting in
	// `submitted` (that counter is for systemOp exactly-once), but still dedup on
	// the tracker so a redelivery does not double-emit.
	switch env.OperationType {
	case "StartLoomPattern":
		var p struct {
			PatternRef string `json:"patternRef"`
			SubjectKey string `json:"subjectKey"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if !f.trackOnce(ctx, env.RequestID) {
			reply("duplicate", "")
			return
		}
		reply("accepted", "")
		publishEvent("loom.patternStarted", map[string]any{
			"instanceId": env.RequestID, // §10.9: instanceId = StartLoomPattern requestId
			"patternRef": p.PatternRef,
			"subjectKey": p.SubjectKey,
		})
		return
	case "CompletePattern", "FailPattern":
		var p struct {
			InstanceID string `json:"instanceId"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if !f.trackOnce(ctx, env.RequestID) {
			reply("duplicate", "")
			return
		}
		reply("accepted", "")
		class := "loom.patternCompleted"
		if env.OperationType == "FailPattern" {
			class = "loom.patternFailed"
		}
		publishEvent(class, map[string]any{"instanceId": p.InstanceID})
		return
	}

	// CreateTask: mint vtx.task.<suppliedTaskId> + tracker (dedup), reply
	// accepted, emit NO completion event (the task is created, not completed).
	if env.OperationType == "CreateTask" {
		var p struct {
			TaskID string `json:"taskId"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if !f.trackOnce(ctx, env.RequestID) {
			reply("duplicate", "")
			return
		}
		reply("accepted", "")
		taskKey := "vtx.task." + p.TaskID
		f.mu.Lock()
		if f.createdTasks == nil {
			f.createdTasks = map[string]struct{}{}
		}
		f.createdTasks[taskKey] = struct{}{}
		f.createTasks++
		f.mu.Unlock()
		return
	}

	// instanceOp (externalTask): mint the claim vertex
	// vtx.<claimType>.<payload.instanceKey> (a package-chosen NON-service type —
	// the engine supplied only the bare handle), write the tracker (dedup), reply
	// accepted, emit NO completion event (the real op emits external.<adapter>
	// for the bridge; replyOp models the reply directly).
	if _, ok := f.instanceOps[env.OperationType]; ok {
		var p struct {
			InstanceKey string `json:"instanceKey"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if !f.trackOnce(ctx, env.RequestID) {
			reply("duplicate", "")
			return
		}
		reply("accepted", "")
		// The claim-vertex root data stays MINIMAL (at most a lifecycle scalar) —
		// the outcome lands in an aspect via replyOp (D5 / invariant b).
		claimKey := "vtx." + f.claimType + "." + p.InstanceKey
		vtxBody, _ := json.Marshal(map[string]any{
			"class": f.claimType,
			"data":  map[string]any{"status": "pending"},
		})
		_, _ = f.conn.KVPut(ctx, coreKVBucket, claimKey, vtxBody)
		f.mu.Lock()
		f.createdInstance++
		f.mu.Unlock()
		return
	}

	// replyOp (externalTask): model the bridge posting the result
	// back — record the outcome as an ASPECT on the claim vertex (root data left
	// minimal — D5) and emit orchestration.externalTaskCompleted carrying
	// payload.externalRef = the bare handle (the uniform completion signal).
	if _, ok := f.replyOps[env.OperationType]; ok {
		var p struct {
			ExternalRef string         `json:"externalRef"`
			Outcome     map[string]any `json:"outcome"`
		}
		_ = json.Unmarshal(env.Payload, &p)
		if !f.trackOnce(ctx, env.RequestID) {
			reply("duplicate", "")
			return
		}
		reply("accepted", "")
		outcome := p.Outcome
		if outcome == nil {
			outcome = map[string]any{"result": "ok"}
		}
		aspectKey := "vtx." + f.claimType + "." + p.ExternalRef + "." + f.replyAspect
		aspectBody, _ := json.Marshal(map[string]any{
			"class":     f.claimType + "." + f.replyAspect,
			"vertexKey": "vtx." + f.claimType + "." + p.ExternalRef,
			"localName": f.replyAspect,
			"data":      outcome,
		})
		_, _ = f.conn.KVPut(ctx, coreKVBucket, aspectKey, aspectBody)
		publishEvent(f.replyEvent, map[string]any{"externalRef": p.ExternalRef})
		return
	}

	// A bound op (task-authorized): on commit, simulate the commit-path
	// auto-complete — emit orchestration.taskCompleted(taskKey) read from
	// authContext.task.
	if _, ok := f.taskOps[env.OperationType]; ok {
		if !f.trackOnce(ctx, env.RequestID) {
			reply("duplicate", "")
			return
		}
		reply("accepted", "")
		if env.AuthContext.Task != "" {
			publishEvent("orchestration.taskCompleted", map[string]any{"taskKey": env.AuthContext.Task})
		}
		return
	}

	// systemOp.
	if !f.trackOnce(ctx, env.RequestID) {
		reply("duplicate", "")
		return
	}
	atomic.AddInt64(&f.submitted, 1)
	reply("accepted", "")

	class := f.eventFor(env.OperationType)
	publish := func() { publishEvent(class, map[string]any{"op": env.OperationType}) }
	if f.gate != nil {
		go func() {
			select {
			case <-f.gate:
				publish()
			case <-ctx.Done():
			}
		}()
		return
	}
	publish()
}

// trackOnce writes the Contract #4 tracker; returns false if it already exists
// (a duplicate / crash re-attempt collapsed on the tracker).
func (f *fakeProcessor) trackOnce(ctx context.Context, requestID string) bool {
	_, err := f.conn.KVCreate(ctx, coreKVBucket, "vtx.op."+requestID, []byte(`{"class":"op","data":{}}`))
	return err == nil
}

// taskCreated reports whether CreateTask minted taskKey.
func (f *fakeProcessor) taskCreated(taskKey string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.createdTasks[taskKey]
	return ok
}

// createTaskCount is the number of accepted (non-duplicate) CreateTask commits.
func (f *fakeProcessor) createTaskCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createTasks
}

// createdInstanceCount is the number of accepted (non-duplicate) instanceOp
// commits (the externalTask exactly-once witness).
func (f *fakeProcessor) createdInstanceCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createdInstance
}

func mustNanoIDStr() string {
	id, _ := substrate.NewNanoID()
	return id
}

// --- Pattern install + trigger ----------------------------------------------

// installPattern writes a meta.loomPattern vertex + spec aspect to Core KV the
// way the Processor write path would, so Loom's CDC pattern source loads it.
func installPattern(t *testing.T, ctx context.Context, conn *substrate.Conn, patternID string, p loom.Pattern) {
	t.Helper()
	vtxKey := "vtx.meta." + patternID
	vtxBody, _ := json.Marshal(map[string]any{"class": "meta.loomPattern", "data": map[string]any{}})
	_, err := conn.KVPut(ctx, coreKVBucket, vtxKey, vtxBody)
	require.NoError(t, err)

	specBody, _ := json.Marshal(p)
	specEnvelope, _ := json.Marshal(map[string]any{"class": "loomPatternSpec", "data": json.RawMessage(specBody)})
	_, err = conn.KVPut(ctx, coreKVBucket, vtxKey+".spec", specEnvelope)
	require.NoError(t, err)
}

// submitStartLoomPattern publishes a real StartLoomPattern op to ops.<lane>; the
// fake Processor commits it and emits the events.loom.patternStarted trigger.
// Returns the instanceId (= the StartLoomPattern requestId, §10.9). The submit
// is retried until the fake replies accepted, so it races the engine startup
// gracefully.
func submitStartLoomPattern(t *testing.T, ctx context.Context, conn *substrate.Conn, patternRef, subjectKey string) string {
	t.Helper()
	requestID := mustNanoID(t)
	payload, _ := json.Marshal(map[string]string{"patternRef": patternRef, "subjectKey": subjectKey})
	env, _ := json.Marshal(map[string]any{
		"requestId":     requestID,
		"lane":          "system",
		"operationType": "StartLoomPattern",
		"actor":         loomActorKey,
		"submittedAt":   substrate.FormatTimestamp(time.Now()),
		"payload":       json.RawMessage(payload),
		"authContext":   map[string]string{"target": patternRef},
	})

	inbox := nats.NewInbox()
	sub, err := conn.NATS().SubscribeSync(inbox)
	require.NoError(t, err)
	defer func() { _ = sub.Unsubscribe() }()

	msg := &nats.Msg{Subject: "ops.system", Data: env, Header: nats.Header{replyInboxHeader: []string{inbox}}}
	_, err = conn.JetStream().PublishMsg(ctx, msg)
	require.NoError(t, err)

	reply, err := sub.NextMsgWithContext(ctx)
	require.NoError(t, err)
	var r struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(reply.Data, &r))
	require.Contains(t, []string{"accepted", "duplicate"}, r.Status, "StartLoomPattern must commit")
	return requestID
}

func newEngine(conn *substrate.Conn, opts ...func(*loom.Config)) *loom.Engine {
	cfg := loom.Config{
		CoreKVBucket:    coreKVBucket,
		LoomStateBucket: loomStateBucket,
		EventsStream:    eventsStream,
		ActorKey:        loomActorKey,
		Lane:            "system",
		Logger:          testLogger(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return loom.NewEngine(conn, cfg)
}

// --- Tests ------------------------------------------------------------------

// TestLoomE2E_RunsToCompletion proves AC #3/#4/#8/#9: a real StartLoomPattern
// submission emits the trigger event that drives instance creation; the pattern
// runs step → committed-event → advance → next step → exhaustion, each step's op
// committed exactly once, and events.loom.patternStarted → patternCompleted is
// the lifecycle.
func TestLoomE2E_RunsToCompletion(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	startedSub, err := nc.SubscribeSync("events.loom.patternStarted")
	require.NoError(t, err)
	completedSub, err := nc.SubscribeSync("events.loom.patternCompleted")
	require.NoError(t, err)

	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		eventFor:  func(string) string { return "identity.stepDone" },
	}
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "identity",
		Steps: []loom.Step{
			{Kind: "systemOp", Operation: "StepA"},
			{Kind: "systemOp", Operation: "StepB"},
		},
	})

	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	subjectKey := "vtx.identity." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// The trigger event was emitted (drove instance creation).
	_, err = startedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "events.loom.patternStarted must be emitted")

	// Lifecycle completion event.
	_, err = completedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "events.loom.patternCompleted must be emitted")

	// Instance must reach status=complete with cursor exhausted (2 steps).
	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 2, inst.Cursor, "cursor must advance to exhaustion")

	// Exactly the two systemOp steps committed.
	require.Equal(t, int64(2), atomic.LoadInt64(&fp.submitted),
		"each systemOp step committed exactly once")
}

// TestLoomE2E_MidRunRestartExactlyOnce proves AC #6/#9: a mid-run engine restart
// resumes to the SAME completion exactly once — no double submission — correlated
// against the durable token.<token> pointer (no in-memory index). Step A's
// committed event is held until after the restart, so the restart happens with
// step A still pending; the durable consumer redelivers it from its ack floor
// and the durable pointer carries the run to completion.
func TestLoomE2E_MidRunRestartExactlyOnce(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	gate := make(chan struct{})
	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		gate:      gate,
		eventFor:  func(string) string { return "identity.stepDone" },
	}
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "identity",
		Steps: []loom.Step{
			{Kind: "systemOp", Operation: "StepA"},
			{Kind: "systemOp", Operation: "StepB"},
		},
	})

	subjectKey := "vtx.identity." + mustNanoID(t)

	// --- Engine generation 1: start the instance, then crash it. ---
	e1Ctx, e1Cancel := context.WithCancel(ctx)
	_, e1Err := startEngine(t, e1Ctx, conn)
	waitForReady(t, 5*time.Second, e1Err, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "gen-1 trigger consumer never registered")

	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	// Wait until step A is persisted as the pending token (write-ahead) + its
	// durable token pointer exists, THEN until the systemOp has actually reached
	// the processor. The write-ahead pointer is durably persisted BEFORE the op
	// submission round-trips, so `submitted` still trails `waitPending` by the
	// async submit — poll it to a floor of 1 rather than reading it once (the
	// gated committed event is held behind `gate`, so step A stays pending).
	waitPending(t, ctx, conn, instanceID)
	waitSubmitted(t, fp, 1)

	// Crash generation 1, THEN release step A's committed event. Join the Start
	// goroutine (deterministic: supervisor.Stop has synchronously drained every
	// pump by the time it returns) rather than sleeping a fixed guess — gen-1
	// must be fully stopped before the release, so only gen-2's redelivery can
	// observe it.
	e1Cancel()
	joinEngine(t, e1Err)
	close(gate)

	// --- Engine generation 2: restart. The durable consumer redelivers step A's
	// committed event from its ack floor; correlation is the durable token GET;
	// the run carries to completion. ---
	e2 := newEngine(conn)
	e2Ctx, e2Cancel := context.WithCancel(ctx)
	defer e2Cancel()
	go func() { _ = e2.Start(e2Ctx) }()

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 2, inst.Cursor)

	// Exactly-once: 2 systemOp steps across BOTH generations. Any double
	// submission would push this above 2 (the fake counts only non-duplicate
	// commits; a redelivery collapsing on the tracker does not inflate it).
	require.Equal(t, int64(2), atomic.LoadInt64(&fp.submitted),
		"mid-run restart must not double-submit any step (exactly once)")
}

// TestLoomE2E_RejectedStepFails proves AC #4: a rejected systemOp (off-stream
// terminal via the submit reply) marks the instance failed rather than waiting
// forever for a committed event that can never arrive.
func TestLoomE2E_RejectedStepFails(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	failedSub, err := nc.SubscribeSync("events.loom.patternFailed")
	require.NoError(t, err)

	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{"StepA": {}},
		eventFor:  func(string) string { return "identity.stepDone" },
	}
	fp.run(ctx, t)

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "identity",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})

	// A rejected op is invisible on core-events (no tracker, no event), so the
	// failed terminal is learned off-stream via the per-step deadline + probe
	// (§10.6). Use a short deadline so the test does not wait the 60s default.
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn, func(c *loom.Config) { c.StepTimeout = 2 * time.Second })
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	subjectKey := "vtx.identity." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "failed")
	require.Equal(t, "failed", inst.Status)
	require.Equal(t, int64(0), atomic.LoadInt64(&fp.submitted), "rejected op writes no tracker/event")

	// FailPattern is announced on the event plane.
	_, err = failedSub.NextMsg(15 * time.Second)
	require.NoError(t, err, "events.loom.patternFailed must be emitted")
}

// TestLoomE2E_LivePatternInstallNoRestart proves AC #1/#2: a pattern installed
// AFTER the engine is already running registers via the CDC source's callback
// and reconciles its per-domain consumer — no engine restart.
func TestLoomE2E_LivePatternInstallNoRestart(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)

	fp := &fakeProcessor{
		conn: conn, logger: testLogger(),
		rejectOps: map[string]struct{}{},
		eventFor:  func(string) string { return "lease.stepDone" },
	}
	fp.run(ctx, t)

	// Start the engine BEFORE any pattern exists.
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	_, engErr := startEngine(t, engCtx, conn)
	waitForReady(t, 5*time.Second, engErr, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-trigger")
	}, "trigger consumer never registered")

	// Install a pattern live over a never-before-seen domain (lease).
	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "lease",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})
	require.True(t, waitFor(t, 5*time.Second, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-lease")
	}), "loom-lease consumer never reconciled")

	subjectKey := "vtx.lease." + mustNanoID(t)
	instanceID := submitStartLoomPattern(t, ctx, conn, patternID, subjectKey)

	inst := waitInstanceStatus(t, ctx, conn, instanceID, "complete")
	require.Equal(t, 1, inst.Cursor)
	require.Equal(t, int64(1), atomic.LoadInt64(&fp.submitted), "1 systemOp step committed")
}

// --- helpers ----------------------------------------------------------------

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

func waitInstanceStatus(t *testing.T, ctx context.Context, conn *substrate.Conn, instanceID, status string) *loom.Instance {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if entry, err := conn.KVGet(ctx, loomStateBucket, "instance."+instanceID); err == nil {
			var inst loom.Instance
			if json.Unmarshal(entry.Value, &inst) == nil && inst.Status == status {
				return &inst
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatalf("instance %q never reached status %q", instanceID, status)
	return nil
}

// waitSubmitted blocks until the fake processor has accepted at least n
// non-duplicate systemOp commits, or fails the test on timeout. Deterministic
// sync for the async submit round-trip (the counter trails the durable
// write-ahead pointer that waitPending observes).
func waitSubmitted(t *testing.T, fp *fakeProcessor, n int64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&fp.submitted) >= n {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("fake processor never reached %d submitted systemOp(s) (have %d)", n, atomic.LoadInt64(&fp.submitted))
}

func waitPending(t *testing.T, ctx context.Context, conn *substrate.Conn, instanceID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if entry, err := conn.KVGet(ctx, loomStateBucket, "instance."+instanceID); err == nil {
			var inst loom.Instance
			if json.Unmarshal(entry.Value, &inst) == nil && inst.PendingToken != "" {
				// The durable reverse pointer must also exist.
				if _, perr := conn.KVGet(ctx, loomStateBucket, "token."+inst.PendingToken); perr == nil {
					return
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("instance %q never wrote a pending token + pointer", instanceID)
}
