package weaver_test

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
	"github.com/asolgan/lattice/internal/substrate"
	"github.com/asolgan/lattice/internal/weaver"
	"github.com/asolgan/lattice/packages/augur"
	semanticcontracts "github.com/asolgan/lattice/packages/semantic-contracts"
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
	coreKVBucket        = "core-kv"
	weaverTargetsBucket = "weaver-targets"
	weaverStateBucket   = "weaver-state"
	healthKVBucket      = "health-kv"
	opsStream           = "core-operations"
	schedulesStream     = "core-schedules"
	weaverActorKey      = "vtx.identity.WeaverServiceActor1abc" // fixture actor key (no Processor in these tests)
)

func provision(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()
	// TTL-capable buckets mirror bootstrap provisioning; weaver-targets stays
	// plain (durable projections, no per-key TTLs), history 1.
	for _, b := range []string{coreKVBucket, weaverStateBucket, healthKVBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: b, LimitMarkerTTL: time.Second})
		require.NoError(t, err)
	}
	_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: weaverTargetsBucket})
	require.NoError(t, err)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name: opsStream, Subjects: []string{"ops.>"},
	})
	require.NoError(t, err)
	// core-schedules mirrors internal/bootstrap/primordial.go: AllowMsgSchedules
	// (NATS-native @at scheduling) + MaxMsgsPerSubject 1 (per-subject rollup
	// storage bound), file storage, limits retention.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:              schedulesStream,
		Subjects:          []string{"schedule.>"},
		Storage:           jetstream.FileStorage,
		Retention:         jetstream.LimitsPolicy,
		MaxMsgsPerSubject: 1,
		AllowMsgSchedules: true,
	})
	require.NoError(t, err)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// --- Fixture installs ---------------------------------------------------------
//
// The "fixture target Lens" of AC 1 is the TEST writing §10.2-shaped rows
// directly into weaver-targets; Refractor wiring of a real target Lens is the
// lease-signing package's job (Epic 11). Meta-vertices are written the way the
// Processor write path lands them (vertex envelope + spec aspect envelope), so
// the registry CDC source loads them exactly as in production.

func installWeaverTarget(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexID string, target map[string]any) {
	t.Helper()
	vtxKey := "vtx.meta." + vertexID
	vtxBody, _ := json.Marshal(map[string]any{"class": "meta.weaverTarget", "data": map[string]any{}})
	_, err := conn.KVPut(ctx, coreKVBucket, vtxKey, vtxBody)
	require.NoError(t, err)

	specEnvelope, _ := json.Marshal(map[string]any{"class": "weaverTargetSpec", "data": target})
	_, err = conn.KVPut(ctx, coreKVBucket, vtxKey+".spec", specEnvelope)
	require.NoError(t, err)
}

func tombstoneMetaVertex(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexID string) {
	t.Helper()
	require.NoError(t, conn.KVDelete(ctx, coreKVBucket, "vtx.meta."+vertexID))
}

func installLoomPattern(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexID, patternID string) {
	t.Helper()
	vtxKey := "vtx.meta." + vertexID
	vtxBody, _ := json.Marshal(map[string]any{"class": "meta.loomPattern", "data": map[string]any{}})
	_, err := conn.KVPut(ctx, coreKVBucket, vtxKey, vtxBody)
	require.NoError(t, err)
	spec := map[string]any{
		"patternId":   patternID,
		"subjectType": "identity",
		"steps":       []map[string]any{{"kind": "systemOp", "operation": "StepA"}},
	}
	specEnvelope, _ := json.Marshal(map[string]any{"class": "loomPatternSpec", "data": spec})
	_, err = conn.KVPut(ctx, coreKVBucket, vtxKey+".spec", specEnvelope)
	require.NoError(t, err)
}

func installOpMeta(t *testing.T, ctx context.Context, conn *substrate.Conn, vertexID, operationType string) {
	t.Helper()
	vtxKey := "vtx.meta." + vertexID
	vtxBody, _ := json.Marshal(map[string]any{
		"class": "meta.ddl.vertexType",
		"data":  map[string]any{"operationType": operationType},
	})
	_, err := conn.KVPut(ctx, coreKVBucket, vtxKey, vtxBody)
	require.NoError(t, err)
}

// putRow writes one §10.2-shaped row into weaver-targets under
// <targetId>.<entityId>.
func putRow(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID, entityID string, row map[string]any) {
	t.Helper()
	if _, ok := row["projectedAt"]; !ok {
		row["projectedAt"] = substrate.FormatTimestamp(time.Now())
	}
	body, _ := json.Marshal(row)
	_, err := conn.KVPut(ctx, weaverTargetsBucket, targetID+"."+entityID, body)
	require.NoError(t, err)
}

// --- Engine + observation helpers --------------------------------------------

func newEngine(conn *substrate.Conn, instance string, opts ...func(*weaver.Config)) *weaver.Engine {
	cfg := weaver.Config{
		CoreKVBucket:        coreKVBucket,
		WeaverTargetsBucket: weaverTargetsBucket,
		WeaverStateBucket:   weaverStateBucket,
		HealthKVBucket:      healthKVBucket,
		ActorKey:            weaverActorKey,
		Lane:                "system",
		Instance:            instance,
		Logger:              testLogger(),
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return weaver.NewEngine(conn, cfg)
}

func mustNanoID(t *testing.T) string {
	t.Helper()
	id, err := substrate.NewNanoID()
	require.NoError(t, err)
	return id
}

// capturedOp is the decoded view of one ops.<lane> publish.
type capturedOp struct {
	RequestID     string         `json:"requestId"`
	Lane          string         `json:"lane"`
	OperationType string         `json:"operationType"`
	Actor         string         `json:"actor"`
	Payload       map[string]any `json:"payload"`
	AuthContext   struct {
		Target string `json:"target"`
	} `json:"authContext"`
}

func subscribeOps(t *testing.T, nc *nats.Conn) *nats.Subscription {
	t.Helper()
	sub, err := nc.SubscribeSync("ops.system")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	return sub
}

func nextOp(t *testing.T, sub *nats.Subscription, timeout time.Duration) *capturedOp {
	t.Helper()
	msg, err := sub.NextMsg(timeout)
	require.NoError(t, err, "expected an op on ops.system")
	var op capturedOp
	require.NoError(t, json.Unmarshal(msg.Data, &op))
	return &op
}

func requireNoOp(t *testing.T, sub *nats.Subscription, window time.Duration) {
	t.Helper()
	msg, err := sub.NextMsg(window)
	if err == nil {
		t.Fatalf("expected no op on ops.system, got: %s", string(msg.Data))
	}
	require.ErrorIs(t, err, nats.ErrTimeout)
}

func waitConsumer(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) {
	t.Helper()
	js := conn.JetStream()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := js.Consumer(ctx, "KV_"+weaverTargetsBucket, durable); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("durable %q never appeared on KV_%s", durable, weaverTargetsBucket)
}

func waitConsumerGone(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) {
	t.Helper()
	js := conn.JetStream()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := js.Consumer(ctx, "KV_"+weaverTargetsBucket, durable); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("durable %q was never deleted from KV_%s", durable, weaverTargetsBucket)
}

func consumerExists(t *testing.T, ctx context.Context, conn *substrate.Conn, durable string) bool {
	t.Helper()
	_, err := conn.JetStream().Consumer(ctx, "KV_"+weaverTargetsBucket, durable)
	return err == nil
}

// waitStreamConsumer waits for a durable to appear on an arbitrary stream
// (the lane-3 weaver-temporal durable lives on core-schedules, not on the
// weaver-targets backing stream waitConsumer is bound to).
func waitStreamConsumer(t *testing.T, ctx context.Context, conn *substrate.Conn, stream, durable string) {
	t.Helper()
	js := conn.JetStream()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := js.Consumer(ctx, stream, durable); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("durable %q never appeared on %s", durable, stream)
}

// waitScheduleHeader waits until the pending schedule message at subject
// carries the given @at header value (proving the schedule — or its
// replacement — is armed).
func waitScheduleHeader(t *testing.T, ctx context.Context, conn *substrate.Conn, subject, wantAt string) {
	t.Helper()
	stream, err := conn.JetStream().Stream(ctx, schedulesStream)
	require.NoError(t, err)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if msg, err := stream.GetLastMsgForSubject(ctx, subject); err == nil {
			if msg.Header.Get(substrate.ScheduleHeader) == "@at "+wantAt {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("schedule message at %q never carried %q", subject, "@at "+wantAt)
}

// waitFiredMessage waits until a fired-timer message lands in core-schedules at
// subject (the NATS scheduler has republished the payload at the target subject).
func waitFiredMessage(t *testing.T, ctx context.Context, conn *substrate.Conn, subject string) {
	t.Helper()
	stream, err := conn.JetStream().Stream(ctx, schedulesStream)
	require.NoError(t, err)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := stream.GetLastMsgForSubject(ctx, subject); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("a fired-timer message never landed at %q", subject)
}

func waitMark(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := conn.KVGet(ctx, weaverStateBucket, key); err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("mark %q never appeared in %s", key, weaverStateBucket)
}

func waitMarkGone(t *testing.T, ctx context.Context, conn *substrate.Conn, key string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := conn.KVGet(ctx, weaverStateBucket, key); err != nil {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("mark %q was never cleared from %s", key, weaverStateBucket)
}

// --- Tests --------------------------------------------------------------------

// TestWeaverE2E_HappyPath proves AC 1/3/4/5/6/7: a meta.weaverTarget install
// brings up the per-target lane-1 durable; a violating fixture row CAS-creates
// the §10.3 mark and fires the remediation op (triggerLoom → StartLoomPattern
// with pattern-as-target auth, the deterministic episode requestId, and the
// payload-carried expectedRevision); flipping the row to violating:false
// level-reconciles the mark away with no further ops.
func TestWeaverE2E_HappyPath(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "onboarding")

	targetID := "fixtureComplete"
	targetVtx := mustNanoID(t)
	installWeaverTarget(t, ctx, conn, targetVtx, map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_onboarding": map[string]any{
				"action": "triggerLoom", "pattern": "onboarding", "subject": "row.applicant",
			},
		},
	})

	engine := newEngine(conn, "e2e-happy-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)

	entityID := mustNanoID(t)
	applicant := "vtx.identity." + mustNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":          entityKey,
		"violating":          true,
		"missing_onboarding": true,
		"applicant":          applicant,
	})

	op := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "StartLoomPattern", op.OperationType)
	require.Equal(t, "system", op.Lane)
	require.Equal(t, weaverActorKey, op.Actor)
	require.True(t, substrate.IsValidNanoID(op.RequestID), "episode requestId must be a 20-char NanoID")
	require.Equal(t, "vtx.meta."+patternVtx, op.AuthContext.Target,
		"authContext.target must be the resolved pattern meta-vertex (pattern-as-target)")
	require.Equal(t, "vtx.meta."+patternVtx, op.Payload["patternRef"])
	require.Equal(t, applicant, op.Payload["subjectKey"])
	require.NotZero(t, op.Payload["expectedRevision"], "payload must carry the row's OCC revision-condition")

	markKey := targetID + "." + entityID + ".missing_onboarding"
	waitMark(t, ctx, conn, markKey)
	entry, err := conn.KVGet(ctx, weaverStateBucket, markKey)
	require.NoError(t, err)
	var mk map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &mk))
	require.Equal(t, targetID, mk["targetId"])
	require.Equal(t, entityKey, mk["entityKey"])
	require.Equal(t, "missing_onboarding", mk["gap"])
	require.Equal(t, "triggerLoom", mk["action"])
	require.NotEmpty(t, mk["claimedAt"])

	// Gap closes: the Lens flips the flags via upsert; Weaver stops acting and
	// the mark is cleared (level-reconciled, AC 7).
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":          entityKey,
		"violating":          false,
		"missing_onboarding": false,
		"applicant":          applicant,
	})
	waitMarkGone(t, ctx, conn, markKey)
	requireNoOp(t, ops, 2*time.Second)
}

// TestWeaverE2E_AssignTask proves the assignTask action contract: the gap
// resolves CreateTask with forOperation resolved from the live op meta-vertex
// index, the templated assignee/target substituted from the row, and the
// episode-deterministic taskId supplied in the payload.
func TestWeaverE2E_AssignTask(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	opVtx := mustNanoID(t)
	installOpMeta(t, ctx, conn, opVtx, "SignFixture")

	targetID := "fixtureSign"
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_signature": map[string]any{
				"action": "assignTask", "operation": "SignFixture",
				"assignee": "row.applicant", "target": "row.entityKey",
			},
		},
	})

	engine := newEngine(conn, "e2e-task-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)

	entityID := mustNanoID(t)
	applicant := "vtx.identity." + mustNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":         entityKey,
		"violating":         true,
		"missing_signature": true,
		"applicant":         applicant,
	})

	op := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "CreateTask", op.OperationType)
	require.Equal(t, applicant, op.Payload["assignee"])
	require.Equal(t, "vtx.meta."+opVtx, op.Payload["forOperation"],
		"forOperation must resolve to the live op meta-vertex")
	require.Equal(t, entityKey, op.Payload["scopedTo"])
	require.Equal(t, entityKey, op.AuthContext.Target)
	taskID, _ := op.Payload["taskId"].(string)
	require.True(t, substrate.IsValidNanoID(taskID), "taskId must be a 20-char NanoID")
	require.NotEmpty(t, op.Payload["expiresAt"])
	require.NotZero(t, op.Payload["expectedRevision"])
}

// TestWeaverE2E_AntiStorm proves the §10.8 anti-storm OCC: re-upserting the
// SAME violating row (a fresh CDC delivery) finds the in-flight mark and
// fires no second op.
func TestWeaverE2E_AntiStorm(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "fixtureFlow")

	targetID := "fixtureStorm"
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_step": map[string]any{
				"action": "triggerLoom", "pattern": "fixtureFlow", "subject": "row.applicant",
			},
		},
	})

	engine := newEngine(conn, "e2e-storm-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)

	entityID := mustNanoID(t)
	row := map[string]any{
		"entityKey":    "vtx.leaseApp." + entityID,
		"violating":    true,
		"missing_step": true,
		"applicant":    "vtx.identity." + mustNanoID(t),
	}
	putRow(t, ctx, conn, targetID, entityID, row)
	first := nextOp(t, ops, 15*time.Second)
	waitMark(t, ctx, conn, targetID+"."+entityID+".missing_step")

	// CDC re-delivery of the same violating state: the mark exists → no second op.
	putRow(t, ctx, conn, targetID, entityID, row)
	requireNoOp(t, ops, 2*time.Second)

	// And the first op was a real dispatch.
	require.Equal(t, "StartLoomPattern", first.OperationType)
}

// TestWeaverE2E_ReconcileTeardownAndReinstall proves AC 3's reconcile
// semantics: tombstoning the meta.weaverTarget Removes the consumer AND
// deletes its JetStream durable; a re-install brings up a fresh consumer that
// replays existing rows via DeliverLastPerSubject.
func TestWeaverE2E_ReconcileTeardownAndReinstall(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "fixtureFlow")

	targetID := "fixtureCycle"
	target := map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_step": map[string]any{
				"action": "triggerLoom", "pattern": "fixtureFlow", "subject": "row.applicant",
			},
		},
	}
	targetVtx := mustNanoID(t)
	installWeaverTarget(t, ctx, conn, targetVtx, target)

	engine := newEngine(conn, "e2e-cycle-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + targetID
	waitConsumer(t, ctx, conn, durable)

	// Tombstone the registry vertex: the consumer is Removed and the JetStream
	// durable deleted (assert via consumer-info absence).
	tombstoneMetaVertex(t, ctx, conn, targetVtx)
	waitConsumerGone(t, ctx, conn, durable)

	// A violating row lands while the target is uninstalled.
	entityID := mustNanoID(t)
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":    "vtx.leaseApp." + entityID,
		"violating":    true,
		"missing_step": true,
		"applicant":    "vtx.identity." + mustNanoID(t),
	})

	// Re-install: a fresh consumer replays the row (DeliverLastPerSubject) and
	// dispatches.
	installWeaverTarget(t, ctx, conn, mustNanoID(t), target)
	waitConsumer(t, ctx, conn, durable)
	op := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "StartLoomPattern", op.OperationType)
}

// TestWeaverE2E_InstallValidations proves the §10.8 install-time validations
// (AC 1) and the FR29 config-error alert path: a gaps key without the
// missing_ prefix rejects the target (no consumer); a duplicate targetId
// rejects the later registration; a true missing_* column with no playbook
// entry alerts via Health KV and dispatches nothing.
func TestWeaverE2E_InstallValidations(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	// (a) gaps key without the missing_ prefix → rejected.
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": "fixtureBadGaps",
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"signature_missing": map[string]any{"action": "directOp", "operation": "NoOp"},
		},
	})

	// (b) duplicate targetId → keep the first, reject the later.
	dupID := "fixtureDup"
	firstVtx := mustNanoID(t)
	installWeaverTarget(t, ctx, conn, firstVtx, map[string]any{
		"targetId": dupID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_a": map[string]any{"action": "directOp", "operation": "FixA"},
		},
	})

	// (c) a valid target whose row will carry an unmapped gap column.
	noPlaybookID := "fixtureNoPlaybook"
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": noPlaybookID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_known": map[string]any{"action": "directOp", "operation": "FixKnown"},
		},
	})

	instance := "e2e-valid-" + mustNanoID(t)
	engine := newEngine(conn, instance, func(c *weaver.Config) { c.HeartbeatEvery = 200 * time.Millisecond })
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	waitConsumer(t, ctx, conn, "weaver-target-"+dupID)
	waitConsumer(t, ctx, conn, "weaver-target-"+noPlaybookID)
	require.False(t, consumerExists(t, ctx, conn, "weaver-target-fixtureBadGaps"),
		"a target with a non-missing_ gaps key must be rejected (no consumer)")

	// The duplicate arrives while the first is registered: rejected + alerted.
	dupVtx := mustNanoID(t)
	installWeaverTarget(t, ctx, conn, dupVtx, map[string]any{
		"targetId": dupID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_b": map[string]any{"action": "directOp", "operation": "FixB"},
		},
	})

	// (c) row with a true missing_* column that has no playbook entry.
	entityID := mustNanoID(t)
	putRow(t, ctx, conn, noPlaybookID, entityID, map[string]any{
		"entityKey":       "vtx.leaseApp." + entityID,
		"violating":       true,
		"missing_unknown": true,
	})

	// No dispatch for any of the three conditions.
	requireNoOp(t, ops, 2*time.Second)

	// The first dup registration still owns the consumer; the duplicate and the
	// unmapped gap surfaced as Contract #5 issues on the heartbeat doc.
	deadline := time.Now().Add(15 * time.Second)
	var issues []map[string]any
	var status string
	for time.Now().Before(deadline) {
		entry, err := conn.KVGet(ctx, healthKVBucket, "health.weaver."+instance)
		if err == nil {
			var doc struct {
				Status string           `json:"status"`
				Issues []map[string]any `json:"issues"`
			}
			if json.Unmarshal(entry.Value, &doc) == nil {
				issues = doc.Issues
				status = doc.Status
				if hasIssue(issues, "TargetRejected") && hasIssue(issues, "GapWithoutPlaybook") {
					break
				}
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	require.True(t, hasIssue(issues, "TargetRejected"),
		"rejected targets must surface a Health KV issue, got: %v", issues)
	require.True(t, hasIssue(issues, "GapWithoutPlaybook"),
		"a true gap with no playbook entry must surface a Health KV issue, got: %v", issues)
	// Contract #5 §5.3: error-severity issues (TargetRejected, GapWithoutPlaybook)
	// must drive status to "unhealthy" — never false-healthy alongside open issues.
	require.Equal(t, "unhealthy", status,
		"a heartbeat carrying error issues must report status:unhealthy, got %q with issues %v", status, issues)
}

func hasIssue(issues []map[string]any, code string) bool {
	for _, i := range issues {
		if i["code"] == code {
			return true
		}
	}
	return false
}

// putDeadMark writes a §10.3 mark directly into weaver-state — the state an
// Actuator that died after its CAS-create (before publishing) leaves behind —
// and returns its revision.
func putDeadMark(t *testing.T, ctx context.Context, conn *substrate.Conn,
	targetID, entityID, gapColumn, action string, leaseExpiresAt time.Time) uint64 {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"targetId":       targetID,
		"entityKey":      "vtx.leaseApp." + entityID,
		"gap":            gapColumn,
		"action":         action,
		"claimedAt":      substrate.FormatTimestamp(time.Now()),
		"leaseExpiresAt": substrate.FormatTimestamp(leaseExpiresAt),
		"heldBy":         "dead-instance",
	})
	require.NoError(t, err)
	rev, err := conn.KVCreate(ctx, weaverStateBucket, targetID+"."+entityID+"."+gapColumn, body)
	require.NoError(t, err)
	return rev
}

// TestWeaverE2E_MidFlightKill proves the crash-recovery AC: an Actuator that
// died after CAS-creating its mark but before publishing (a dead episode with
// a live lease) wedges nothing — the lane-1 fresh delivery anti-storm-drops
// (the F5 coalesce angle: a re-observed violating row alone does NOT re-fire),
// and once the lease expires the reconciler sweep reclaims the mark and
// re-dispatches a fresh episode. Exactly one op lands and the mark is
// re-created under this instance with a live lease.
func TestWeaverE2E_MidFlightKill(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	patternVtx := mustNanoID(t)
	installLoomPattern(t, ctx, conn, patternVtx, "fixtureFlow")
	targetID := "fixtureKill"
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_step": map[string]any{
				"action": "triggerLoom", "pattern": "fixtureFlow", "subject": "row.applicant",
			},
		},
	})

	// The dead episode: mark present, op never published, lease expiring soon.
	entityID := mustNanoID(t)
	deadRev := putDeadMark(t, ctx, conn, targetID, entityID, "missing_step", "triggerLoom",
		time.Now().Add(8*time.Second))
	row := map[string]any{
		"entityKey":    "vtx.leaseApp." + entityID,
		"violating":    true,
		"missing_step": true,
		"applicant":    "vtx.identity." + mustNanoID(t),
	}
	putRow(t, ctx, conn, targetID, entityID, row)

	instance := "e2e-kill-" + mustNanoID(t)
	engine := newEngine(conn, instance, func(c *weaver.Config) {
		// The reconciler sweep is driven by a durable @every schedule whose floor
		// is 1s (a sub-second cadence cannot fire), so the fixture runs on a 1s
		// sweep with an 8s lease window — long enough that the post-reclaim no-op
		// assertions complete before the reclaimed episode's fresh lease expires.
		c.SweepInterval = time.Second
		// Match the dead mark's 8s lease: a real crash-recovery engine reclaims
		// marks it issued under its own MarkLease, so base == lease and the first
		// reclaim fires at lease-expiry (the userTask reclaim-backoff floor equals
		// the lease-expiry threshold). The default 30m would set the backoff base
		// far above this fixture's deliberately-short 8s lease.
		c.MarkLease = 8 * time.Second
	})
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)

	// While the lease lives: the replayed fresh delivery anti-storm-drops and
	// the sweep leaves the in-flight episode alone.
	requireNoOp(t, ops, time.Second)

	// Lease expiry → the sweep reclaims and re-dispatches a fresh episode.
	op := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "StartLoomPattern", op.OperationType)
	require.NotZero(t, op.Payload["expectedRevision"])

	markKey := targetID + "." + entityID + ".missing_step"
	entry, err := conn.KVGet(ctx, weaverStateBucket, markKey)
	require.NoError(t, err, "the reclaim must re-create the mark")
	require.NotEqual(t, deadRev, entry.Revision, "the reclaimed mark must be a fresh episode")
	var mk map[string]any
	require.NoError(t, json.Unmarshal(entry.Value, &mk))
	require.Equal(t, instance, mk["heldBy"], "the fresh mark is held by this instance")
	leaseExp, err := time.Parse(time.RFC3339Nano, mk["leaseExpiresAt"].(string))
	require.NoError(t, err)
	require.True(t, leaseExp.After(time.Now()), "the fresh mark must carry a live lease")

	// Exactly one re-attempt — never a storm.
	requireNoOp(t, ops, 2*time.Second)

	// F5 coalesce angle: a fresh re-upsert of the still-violating row alone
	// does not re-fire (the fresh episode is in flight).
	putRow(t, ctx, conn, targetID, entityID, row)
	requireNoOp(t, ops, 2*time.Second)
}

// TestWeaverE2E_SweepOrphanedTargetMarks proves F8 at engine level: a mark
// whose target is no longer installed survives the warm-up window after start
// (registry warm-up guard — the meta replay is asynchronous) and is deleted
// once the window elapses, with no dispatch; re-installing the same targetId
// replays the row via DeliverLastPerSubject and dispatches fresh, unshadowed.
func TestWeaverE2E_SweepOrphanedTargetMarks(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	// A leftover expired mark + violating row for a target that is NOT
	// installed (its vertex was removed while the engine was down).
	targetID := "fixtureOrphan"
	entityID := mustNanoID(t)
	putDeadMark(t, ctx, conn, targetID, entityID, "missing_x", "directOp",
		time.Now().Add(-time.Minute))
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey": "vtx.leaseApp." + entityID,
		"violating": true,
		"missing_x": true,
	})

	instance := "e2e-orphan-" + mustNanoID(t)
	engine := newEngine(conn, instance, func(c *weaver.Config) {
		c.SweepInterval = 2 * time.Second
		c.SweepOrphanWarmup = 2 * time.Second
		c.HeartbeatEvery = 200 * time.Millisecond
	})
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	// Observe the first completed pass via the heartbeat's sweepLastRunAt and
	// assert the warm-up guard left the orphan mark standing.
	markKey := targetID + "." + entityID + ".missing_x"
	deadline := time.Now().Add(15 * time.Second)
	swept := false
	for time.Now().Before(deadline) {
		entry, err := conn.KVGet(ctx, healthKVBucket, "health.weaver."+instance)
		if err == nil {
			var doc struct {
				Metrics map[string]any `json:"metrics"`
			}
			if json.Unmarshal(entry.Value, &doc) == nil && doc.Metrics["sweepLastRunAt"] != nil {
				swept = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.True(t, swept, "the sweep's first pass never completed")
	_, err = conn.KVGet(ctx, weaverStateBucket, markKey)
	require.NoError(t, err, "the first pass must skip the target-uninstalled orphan leg")

	// From the second pass on the orphan is reclaimed — delete only, no op.
	waitMarkGone(t, ctx, conn, markKey)
	requireNoOp(t, ops, time.Second)

	// Re-install the same targetId: DeliverLastPerSubject replays the row and
	// dispatches fresh — not shadowed by the dead mark.
	opVtx := mustNanoID(t)
	installOpMeta(t, ctx, conn, opVtx, "FixX")
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_x": map[string]any{
				"action": "assignTask", "operation": "FixX",
				"assignee": "row.entityKey", "target": "row.entityKey",
			},
		},
	})
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)
	op := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "CreateTask", op.OperationType)
	waitMark(t, ctx, conn, markKey)
}

// --- Temporal lane (lane 3, §10.4) -------------------------------------------

// installFreshTarget installs a fixture target (the caller-supplied targetID)
// whose playbook remediates missing_fresh via directOp.
func installFreshTarget(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID string) {
	t.Helper()
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": targetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_fresh": map[string]any{"action": "directOp", "operation": "FixFresh"},
		},
	})
}

// freshInstant returns now+d truncated to whole seconds (the byte-identical
// header/payload/requestId instant) as an RFC3339 string.
func freshInstant(d time.Duration) string {
	return time.Now().Add(d).UTC().Truncate(time.Second).Format(time.RFC3339)
}

// TestWeaverE2E_TemporalHappyLoop proves AC 1–3: a row carrying a future
// freshUntil arms the per-target-per-entity @at schedule on core-schedules
// (with the §10.4 headers); at expiry the NATS scheduler republishes to the
// fired subject; the weaver-temporal durable converts the firing into exactly
// one MarkExpired op carrying the §10.4-derived deterministic requestId and no
// authContext; the fixture then re-projects the row violating (the CDC leg —
// Refractor wiring is Epic 11) and lane-1 dispatches the remediation.
func TestWeaverE2E_TemporalHappyLoop(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	targetID := "fixtureFresh"
	installFreshTarget(t, ctx, conn, targetID)

	engine := newEngine(conn, "e2e-temporal-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)
	waitStreamConsumer(t, ctx, conn, schedulesStream, "weaver-temporal")

	entityID := mustNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	// A comfortable fuse so the pending schedule is still armed when the target
	// header is read back below (a tight fuse can let the firing roll the pending
	// entry away before the read).
	fireAt := freshInstant(6 * time.Second)
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":  entityKey,
		"violating":  false,
		"freshUntil": fireAt,
	})

	// AC 1: the schedule message is armed with the §10.4 headers.
	schedSubject := "schedule.weaver.timer." + targetID + "." + entityID
	waitScheduleHeader(t, ctx, conn, schedSubject, fireAt)
	stream, err := conn.JetStream().Stream(ctx, schedulesStream)
	require.NoError(t, err)
	sched, err := stream.GetLastMsgForSubject(ctx, schedSubject)
	require.NoError(t, err)
	require.Equal(t, "schedule.weaver.timer.fired."+targetID+"."+entityID,
		sched.Header.Get(substrate.ScheduleTargetHeader),
		"the republish target must be the fired subject within schedule.>")

	// AC 2: at expiry, exactly one MarkExpired op with the deterministic
	// requestId, the {entityKey, targetId, expiredAt} payload, no authContext.
	op := nextOp(t, ops, 20*time.Second)
	require.Equal(t, "MarkExpired", op.OperationType)
	require.Equal(t, weaverActorKey, op.Actor)
	require.Equal(t, weaver.DeriveTimerRequestID(schedSubject, fireAt), op.RequestID,
		"the requestId must derive from the schedule subject + fire instant (§10.4)")
	require.Equal(t, entityKey, op.Payload["entityKey"])
	require.Equal(t, targetID, op.Payload["targetId"])
	require.Equal(t, fireAt, op.Payload["expiredAt"])
	require.Empty(t, op.AuthContext.Target, "MarkExpired carries no authContext")
	requireNoOp(t, ops, 2*time.Second)

	// AC 3: the fixture re-projects the entity violating (CDC leg) and lane-1
	// dispatches the remediation — mark + op, the full time→op→violation→
	// remediation chain.
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":     entityKey,
		"violating":     true,
		"missing_fresh": true,
	})
	rem := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "FixFresh", rem.OperationType)
	waitMark(t, ctx, conn, targetID+"."+entityID+".missing_fresh")
}

// TestWeaverE2E_TemporalReplace proves AC 4: re-projecting the entity with a
// NEW freshUntil before expiry re-publishes to the same schedule subject,
// REPLACING the prior timer — no firing at the first instant, exactly ONE
// MarkExpired overall, carrying the second instant. (Both schedules sit inside
// the observation window, so a broken replace fails this test with the
// first-instant firing.)
func TestWeaverE2E_TemporalReplace(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	targetID := "fixtureFresh"
	installFreshTarget(t, ctx, conn, targetID)

	engine := newEngine(conn, "e2e-replace-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)
	waitStreamConsumer(t, ctx, conn, schedulesStream, "weaver-temporal")

	entityID := mustNanoID(t)
	entityKey := "vtx.leaseApp." + entityID
	schedSubject := "schedule.weaver.timer." + targetID + "." + entityID

	// A roomy first fuse so the replace lands before the first instant fires even
	// on a slow runner (both instants sit inside the observation window, so a
	// broken replace still fails this test with the first-instant firing).
	firstAt := freshInstant(5 * time.Second)
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":  entityKey,
		"violating":  false,
		"freshUntil": firstAt,
	})
	waitScheduleHeader(t, ctx, conn, schedSubject, firstAt)

	// Re-done before expiry: a NEW deadline replaces the prior timer.
	secondAt := freshInstant(10 * time.Second)
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":  entityKey,
		"violating":  false,
		"freshUntil": secondAt,
	})
	waitScheduleHeader(t, ctx, conn, schedSubject, secondAt)

	// Exactly one firing, at the SECOND instant.
	op := nextOp(t, ops, 20*time.Second)
	require.Equal(t, "MarkExpired", op.OperationType)
	require.Equal(t, secondAt, op.Payload["expiredAt"],
		"the replaced timer must fire at the re-armed instant, not the original")
	require.Equal(t, weaver.DeriveTimerRequestID(schedSubject, secondAt), op.RequestID)
	requireNoOp(t, ops, 2*time.Second)
}

// TestWeaverE2E_TemporalRestartDurability proves AC 5: the schedule is held by
// NATS, not by the engine — a full engine stop before expiry loses nothing; a
// FRESH engine (same server, fixed weaver-temporal durable) converts the
// firing into exactly one op.
func TestWeaverE2E_TemporalRestartDurability(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	targetID := "fixtureFresh"
	installFreshTarget(t, ctx, conn, targetID)

	engine := newEngine(conn, "e2e-restart-a-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = engine.Start(engCtx); close(done) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)
	waitStreamConsumer(t, ctx, conn, schedulesStream, "weaver-temporal")

	entityID := mustNanoID(t)
	schedSubject := "schedule.weaver.timer." + targetID + "." + entityID
	fireAt := freshInstant(3 * time.Second)
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"violating":  false,
		"freshUntil": fireAt,
	})
	waitScheduleHeader(t, ctx, conn, schedSubject, fireAt)

	// Full stop BEFORE expiry.
	engCancel()
	<-done

	// A fresh engine resumes the fixed durable; the timer fires and converts.
	engine2 := newEngine(conn, "e2e-restart-b-"+mustNanoID(t))
	eng2Ctx, eng2Cancel := context.WithCancel(ctx)
	defer eng2Cancel()
	go func() { _ = engine2.Start(eng2Ctx) }()

	op := nextOp(t, ops, 20*time.Second)
	require.Equal(t, "MarkExpired", op.OperationType)
	require.Equal(t, weaver.DeriveTimerRequestID(schedSubject, fireAt), op.RequestID)
	requireNoOp(t, ops, 2*time.Second)
}

// TestWeaverE2E_TemporalMissedWhileDown proves the ack-floor recovery: a timer
// that fires while NO engine is running leaves its fired message durable in
// core-schedules; the restarted engine's weaver-temporal durable picks it up
// and converts it — exactly one op.
func TestWeaverE2E_TemporalMissedWhileDown(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	targetID := "fixtureFresh"
	installFreshTarget(t, ctx, conn, targetID)

	engine := newEngine(conn, "e2e-missed-a-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { _ = engine.Start(engCtx); close(done) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)
	waitStreamConsumer(t, ctx, conn, schedulesStream, "weaver-temporal")

	entityID := mustNanoID(t)
	schedSubject := "schedule.weaver.timer." + targetID + "." + entityID
	fireAt := freshInstant(2 * time.Second)
	putRow(t, ctx, conn, targetID, entityID, map[string]any{
		"entityKey":  "vtx.leaseApp." + entityID,
		"violating":  false,
		"freshUntil": fireAt,
	})
	waitScheduleHeader(t, ctx, conn, schedSubject, fireAt)

	// Stop, then let the timer fire with no engine running.
	engCancel()
	<-done
	firedSubject := "schedule.weaver.timer.fired." + targetID + "." + entityID
	waitFiredMessage(t, ctx, conn, firedSubject)

	// Restart: the durable resumes from its ack floor and converts the stored
	// firing.
	engine2 := newEngine(conn, "e2e-missed-b-"+mustNanoID(t))
	eng2Ctx, eng2Cancel := context.WithCancel(ctx)
	defer eng2Cancel()
	go func() { _ = engine2.Start(eng2Ctx) }()

	op := nextOp(t, ops, 20*time.Second)
	require.Equal(t, "MarkExpired", op.OperationType)
	require.Equal(t, weaver.DeriveTimerRequestID(schedSubject, fireAt), op.RequestID)
	requireNoOp(t, ops, 2*time.Second)
}

// gapActionFixtureBody renders one pkgmgr.GapActionSpec into the lowerCamelCase
// JSON shape the Weaver registry parses (mirrors the unexported
// pkgmgr.gapActionBody the real installer emits) — read live off the package's
// own WeaverTargets() so this fixture can never drift from what installs.
func gapActionFixtureBody(action, operation, target string, params map[string]string, reads []string) map[string]any {
	body := map[string]any{"action": action}
	if operation != "" {
		body["operation"] = operation
	}
	if target != "" {
		body["target"] = target
	}
	if len(params) > 0 {
		p := make(map[string]any, len(params))
		for k, v := range params {
			p[k] = v
		}
		body["params"] = p
	}
	if len(reads) > 0 {
		r := make([]any, len(reads))
		for i, v := range reads {
			r[i] = v
		}
		body["reads"] = r
	}
	return body
}

// TestWeaverE2E_SemanticContracts_MissingCharge_PayloadCarriesAccountKey pins
// the real semantic-contracts missing_charge playbook (packages/semantic-
// contracts/targets.go), not a hand-copied mirror: it reads the package's own
// WeaverTargets() and installs that exact gap spec as the fixture target, then
// drives a real dispatch. A directOp's Target field ONLY sets
// AuthContext.Target for auth-path scoping (internal/weaver/strategist.go's
// buildPlan, actionDirectOp case) — it is NEVER merged into the dispatched
// op's Payload, which is built exclusively from Params. DebitAccount's script
// (packages/loftspace-ledger/scripts.go post_entry) reads accountKey strictly
// from op.payload, so the gap's Params must template accountKey directly (the
// objects-base / cafe-domain precedent) or every Weaver-dispatched charge
// fails DebitAccount's own required-field validation.
func TestWeaverE2E_SemanticContracts_MissingCharge_PayloadCarriesAccountKey(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	targets := semanticcontracts.WeaverTargets()
	require.Len(t, targets, 1, "semantic-contracts must declare exactly one weaverTarget")
	target := targets[0]
	ga, ok := target.Gaps["missing_charge"]
	require.True(t, ok, "semantic-contracts playbook must declare missing_charge")
	require.Equal(t, "directOp", ga.Action)
	require.Equal(t, "DebitAccount", ga.Operation)

	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": target.TargetID,
		"lensRef":  mustNanoID(t),
		"gaps": map[string]any{
			"missing_charge": gapActionFixtureBody(ga.Action, ga.Operation, ga.Target, ga.Params, ga.Reads),
		},
	})

	engine := newEngine(conn, "e2e-semantic-charge-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()

	durable := "weaver-target-" + target.TargetID
	waitConsumer(t, ctx, conn, durable)

	entityID := mustNanoID(t)
	acctKey := "vtx.account." + mustNanoID(t)
	clauseKey := "vtx.clause." + mustNanoID(t)
	putRow(t, ctx, conn, target.TargetID, entityID, map[string]any{
		"entityKey":      "vtx.clause." + entityID,
		"violating":      true,
		"missing_charge": true,
		"accountKey":     acctKey,
		"amountCents":    int64(4500),
		"clauseKey":      clauseKey,
		"period":         "oneTime",
	})

	op := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "DebitAccount", op.OperationType)
	require.Equal(t, acctKey, op.Payload["accountKey"],
		"the dispatched DebitAccount payload must carry accountKey — Target only sets authContext.target, never the payload")
	require.Equal(t, float64(4500), op.Payload["amountCents"])
	require.Equal(t, clauseKey, op.Payload["clauseRef"])
}

// installAugurDispatchTarget pins the real augur package's augurDispatch
// meta.weaverTarget (packages/augur/targets.go) as the fixture target — same
// "read live off the package's own WeaverTargets(), never hand-copy" precedent
// gapActionFixtureBody documents above — so these two tests can never drift
// from what the package actually installs.
func installAugurDispatchTarget(t *testing.T, ctx context.Context, conn *substrate.Conn) string {
	t.Helper()
	targets := augur.WeaverTargets()
	require.Len(t, targets, 1, "augur must declare exactly one weaverTarget")
	target := targets[0]
	ga, ok := target.Gaps["missing_dispatch"]
	require.True(t, ok, "augur playbook must declare missing_dispatch")
	require.Equal(t, "proposedOp", ga.Action)
	installWeaverTarget(t, ctx, conn, mustNanoID(t), map[string]any{
		"targetId": target.TargetID,
		"lensRef":  target.LensRef,
		"gaps": map[string]any{
			"missing_dispatch": map[string]any{"action": ga.Action},
		},
	})
	return target.TargetID
}

// putAugurDispatchRow writes one augurDispatchPending-lens-shaped row (design
// augur-dispatch-pickup-design.md §10.2, mirrored from
// internal/augurconvergence's harness.putDispatchRow — same row shape, no
// live Processor here) directly into weaver-targets under
// augurDispatch.<handle>, simulating an approved proposal ready for pickup.
func putAugurDispatchRow(t *testing.T, ctx context.Context, conn *substrate.Conn, targetID, handle, candidateKey, action string, params map[string]any) {
	t.Helper()
	putRow(t, ctx, conn, targetID, handle, map[string]any{
		"entityKey":        "vtx.augurproposal." + handle,
		"violating":        true,
		"missing_dispatch": true,
		"proposedAction":   action,
		"proposedParams":   params,
		"candidateKey":     candidateKey,
	})
}

// TestWeaverE2E_AugurDispatch_MidFlightKill applies the TestWeaverE2E_
// MidFlightKill crash-recovery pattern to Fire 2b's dispatch episode (design
// augur-dispatch-pickup-design.md §6 residual): an Actuator that died after
// CAS-creating its `proposedOp` mark but before publishing leaves a dead mark
// behind. While its lease lives, nothing dispatches; once it expires, the
// reconciler sweep reclaims and fires the SAME proposal-scoped requestId
// (deriveProposalDispatchRequestID, §3.3) — a fresh episode collapses on the
// deterministic tracker, so exactly one inner op + one dispatched-flip land,
// never a storm, even across a further row re-observe.
func TestWeaverE2E_AugurDispatch_MidFlightKill(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	targetID := installAugurDispatchTarget(t, ctx, conn)

	handle := mustNanoID(t)
	candidateKey := "vtx.identity." + mustNanoID(t)
	deadRev := putDeadMark(t, ctx, conn, targetID, handle, "missing_dispatch", "proposedOp",
		time.Now().Add(8*time.Second))
	putAugurDispatchRow(t, ctx, conn, targetID, handle, candidateKey, "directOp", map[string]any{
		"operation": "SetAvailability",
		"target":    candidateKey,
		"params":    map[string]any{"identity": candidateKey, "available": true},
	})

	instance := "e2e-augur-kill-" + mustNanoID(t)
	engine := newEngine(conn, instance, func(c *weaver.Config) {
		c.SweepInterval = time.Second
		c.MarkLease = 8 * time.Second
	})
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)

	// While the dead mark's lease lives: the replayed fresh delivery
	// anti-storm-drops and the sweep leaves the in-flight episode alone.
	requireNoOp(t, ops, time.Second)

	// Lease expiry → the sweep reclaims and fires the two-op dispatch: the
	// materialised remediation, then the dispatched-flip, in that order
	// (evaluator.fire's publish order).
	remediation := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "SetAvailability", remediation.OperationType)
	require.Equal(t, candidateKey, remediation.AuthContext.Target)

	flip := nextOp(t, ops, 5*time.Second)
	require.Equal(t, "RecordProposalDispatch", flip.OperationType)
	require.Equal(t, handle, flip.Payload["externalRef"])
	require.Equal(t, "dispatched", flip.Payload["outcome"])

	markKey := targetID + "." + handle + ".missing_dispatch"
	entry, err := conn.KVGet(ctx, weaverStateBucket, markKey)
	require.NoError(t, err, "the reclaim must re-create the mark")
	require.NotEqual(t, deadRev, entry.Revision, "the reclaimed mark must be a fresh episode")

	// Exactly one re-attempt — never a storm, and a fresh re-upsert of the
	// still-open row alone does not re-fire (F5 coalesce angle).
	requireNoOp(t, ops, 2*time.Second)
	putAugurDispatchRow(t, ctx, conn, targetID, handle, candidateKey, "directOp", map[string]any{
		"operation": "SetAvailability",
		"target":    candidateKey,
		"params":    map[string]any{"identity": candidateKey, "available": true},
	})
	requireNoOp(t, ops, 2*time.Second)
}

// TestWeaverE2E_AugurDispatch_ScopeEscapeInvalid proves the design's §6
// adversarial claim end-to-end (augur-dispatch-pickup-design.md §6: "a
// FakeAugur proposal that somehow reached approved carrying a directOp on a
// different entity ... is caught at the dispatch-time §5 scope check →
// invalid, never dispatches"): a row whose proposedParams.target names an
// entity OTHER than the row's own trusted candidateKey — the drift a
// compromised or buggy upstream (record/approval-time Starlark legs) could in
// principle let through — is rejected by Weaver's OWN independent Go
// dispatch-time re-validation leg (validateProposedDispatch) the instant it
// reaches the running engine. Only the RecordProposalDispatch{invalid} flip
// fires; the proposed remediation NEVER dispatches.
func TestWeaverE2E_AugurDispatch_ScopeEscapeInvalid(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	ops := subscribeOps(t, nc)

	targetID := installAugurDispatchTarget(t, ctx, conn)

	engine := newEngine(conn, "e2e-augur-invalid-"+mustNanoID(t))
	engCtx, engCancel := context.WithCancel(ctx)
	defer engCancel()
	go func() { _ = engine.Start(engCtx) }()
	waitConsumer(t, ctx, conn, "weaver-target-"+targetID)

	handle := mustNanoID(t)
	candidateKey := "vtx.identity." + mustNanoID(t)
	foreignKey := "vtx.identity." + mustNanoID(t)
	putAugurDispatchRow(t, ctx, conn, targetID, handle, candidateKey, "directOp", map[string]any{
		// target names a DIFFERENT entity than candidateKey — the anchor-field
		// scope-escape validateProposedDispatch's dispatchAnchorField check
		// rejects (never merely "referenced somewhere" — the actual anchor).
		"operation": "SetAvailability",
		"target":    foreignKey,
		"params":    map[string]any{"identity": foreignKey, "available": true},
	})

	// The ONLY op that ever fires is the invalid flip — never the remediation.
	flip := nextOp(t, ops, 15*time.Second)
	require.Equal(t, "RecordProposalDispatch", flip.OperationType)
	require.Equal(t, handle, flip.Payload["externalRef"])
	require.Equal(t, "invalid", flip.Payload["outcome"])
	reason, _ := flip.Payload["reason"].(string)
	require.Contains(t, reason, "does not equal the escalated candidate",
		"the invalid flip must record an auditable scope-escape reason")

	requireNoOp(t, ops, 2*time.Second)
}
