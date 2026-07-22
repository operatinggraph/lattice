package bootstrap_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/bootstrap"
)

// requireNATSStack connects to the real Docker NATS stack and skips if it is
// not reachable. The embedded test server runs the NATS scheduler too (the
// internal/weaver e2e suite exercises the full @at firing loop on it); this
// smoke test deliberately stays pinned to the production-shaped Docker stack
// (make up) as the real-deployment check.
func requireNATSStack(t *testing.T) *nats.Conn {
	t.Helper()
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = "nats://localhost:4222"
	}
	nc, err := nats.Connect(url, nats.Timeout(2*time.Second))
	if err != nil {
		t.Skipf("NATS stack not reachable (%v) — run `make up` first", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

// TestCoreSchedulesSmoke exercises the core-schedules stream end-to-end
// against the real Docker NATS stack (nats://localhost:4222).
//
// Four assertions (Contract #10 §10.4, ADR-51):
//
//	(a) Stream exists with AllowMsgSchedules: true.
//	(b) An @at scheduled message republishes to the chosen target subject.
//	(c) Payload round-trips correctly.
//	(d) Re-publishing to the same schedule subject replaces the prior schedule
//	    (exactly one firing per subject).
//
// Implementation note: the NATS scheduler fires by storing the payload back
// into the core-schedules stream at the target subject (not as a plain NATS
// core message). The target subject MUST be within the stream's subject space
// (schedule.>). Components consume fired messages via JetStream consumers
// filtered on their target subject prefix. See cmd/bootstrap/CONTRACT-AMENDMENT-REQUEST.md
// for the contract-10 §10.4 annotation regarding this ADR-51 behavior.
//
// Timing: @at <now+3s> with a 15-second receive window. The 3-second lead
// time gives the NATS scheduler enough margin to process the @at; the 15-second
// window absorbs scheduler precision jitter (typically ±1–2s on a loaded host).
func TestCoreSchedulesSmoke(t *testing.T) {
	nc := requireNATSStack(t)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// Generate a fresh bootstrap ID set for this test run.
	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err := bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	require.NoError(t, seeder.ProvisionBuckets(ctx))

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	// --- Assertion (a): stream exists with AllowMsgSchedules: true ---
	stream, err := js.Stream(ctx, bootstrap.CoreSchedulesStreamName)
	require.NoError(t, err, "core-schedules stream must exist after ProvisionBuckets")
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.True(t, info.Config.AllowMsgSchedules,
		"core-schedules stream must have AllowMsgSchedules: true")

	// entityID uses a short unique suffix to prevent cross-test pollution when
	// running against a persistent Docker stack.
	entityID := fmt.Sprintf("smoke%d", time.Now().UnixNano())
	schedSubject := "schedule.test.timer." + entityID

	// The NATS scheduler delivers the fired message by storing it back into
	// the core-schedules stream at the target subject. The target MUST be within
	// the stream's subject space (schedule.>). Components use JetStream consumers
	// filtered on their target subject prefix to receive their fired messages.
	targetSubject := "schedule.test.timer.fired." + entityID

	// Create a JetStream consumer on the target subject before publishing.
	// This is event-driven: the consumer fetches when the fired message arrives.
	consumer, err := js.CreateOrUpdateConsumer(ctx, bootstrap.CoreSchedulesStreamName, jetstream.ConsumerConfig{
		FilterSubject: targetSubject,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	require.NoError(t, err, "must be able to create consumer on target subject")
	t.Cleanup(func() {
		// Best-effort cleanup of ephemeral consumer.
		_ = js.DeleteConsumer(context.Background(), bootstrap.CoreSchedulesStreamName, consumer.CachedInfo().Name)
	})

	// Payload carries the full entity key in the body (dots allowed in JSON;
	// dots are forbidden in the NATS subject position per Contract #10 §10.4).
	entityKey := "vtx.op." + entityID
	payload := []byte(`{"entityKey":"` + entityKey + `"}`)

	// --- Assertion (d) setup: publish TWICE to the same schedule subject ---
	// Both publishes schedule @at the SAME near-future instant on the same
	// schedule subject. With one-schedule-per-subject (rollup + MaxMsgsPerSubject:1)
	// the second publish replaces the first, so exactly ONE message fires to the
	// target. If replace were broken, BOTH would fire and the second fetch below
	// would observe the duplicate — so this isolates replace semantics rather
	// than relying on a far-future schedule that wouldn't fire within the window.
	fireAt := "@at " + time.Now().Add(3*time.Second).UTC().Format(time.RFC3339)

	// JSSchedulePattern = "Nats-Schedule" (server constant natsserver.JSSchedulePattern)
	// JSScheduleTarget  = "Nats-Schedule-Target" (server constant natsserver.JSScheduleTarget)
	msg1 := &nats.Msg{
		Subject: schedSubject,
		Header:  nats.Header{},
		Data:    payload,
	}
	msg1.Header.Set(natsserver.JSSchedulePattern, fireAt)
	msg1.Header.Set(natsserver.JSScheduleTarget, targetSubject)
	_, err = js.PublishMsg(ctx, msg1)
	require.NoError(t, err, "first schedule JetStream publish must not error")

	// Second publish to the same schedule subject — replaces the first.
	msg2 := &nats.Msg{
		Subject: schedSubject,
		Header:  nats.Header{},
		Data:    payload,
	}
	msg2.Header.Set(natsserver.JSSchedulePattern, fireAt)
	msg2.Header.Set(natsserver.JSScheduleTarget, targetSubject)
	_, err = js.PublishMsg(ctx, msg2)
	require.NoError(t, err, "second (replacing) schedule JetStream publish must not error")

	// --- Assertion (b): wait for the fired message (event-driven via JetStream consumer) ---
	fetchCtx, fetchCancel := context.WithTimeout(ctx, 15*time.Second)
	defer fetchCancel()

	// Fetch waits up to 15 seconds for the scheduled message to fire and land
	// in the stream at the target subject, then returns exactly that message.
	msgs, err := consumer.Fetch(1, jetstream.FetchMaxWait(15*time.Second))
	require.NoError(t, err, "@at scheduled message must fire within 15s window")
	if fetchCtx.Err() != nil {
		t.Fatal("timeout: @at scheduled message did not fire on target subject within 15s")
	}

	var receivedMsg jetstream.Msg
	for m := range msgs.Messages() {
		receivedMsg = m
		require.NoError(t, m.Ack())
		break
	}
	require.NotNil(t, receivedMsg, "must have received a message from the consumer")
	require.NoError(t, msgs.Error(), "consumer fetch must not error")

	// --- Assertion (c): payload round-trips ---
	require.Equal(t, payload, receivedMsg.Data(),
		"fired message payload must match published payload")

	// --- Assertion (d): replace semantics — exactly one firing ---
	// Attempt to fetch a second message. The replaced (far-future) schedule was
	// purged by the rollup when the second publish replaced it, so no second
	// firing should occur. A short max-wait of 2s confirms no spurious double-fire.
	msgs2, err := consumer.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	require.NoError(t, err)
	var count int
	for range msgs2.Messages() {
		count++
	}
	require.Equal(t, 0, count,
		"re-publishing to the same schedule subject must replace the prior schedule (no second firing)")
}

// TestCoreSchedulesStream_Provisioned verifies that ProvisionBuckets creates
// the core-schedules stream with AllowMsgSchedules: true on the embedded NATS
// server (scheduler not required; stream creation only).
func TestCoreSchedulesStream_Provisioned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err := bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, seeder.ProvisionBuckets(ctx))

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	stream, err := js.Stream(ctx, bootstrap.CoreSchedulesStreamName)
	require.NoError(t, err, "core-schedules stream must exist after ProvisionBuckets")
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.True(t, info.Config.AllowMsgSchedules,
		"core-schedules stream must have AllowMsgSchedules: true")
	require.Equal(t, jetstream.FileStorage, info.Config.Storage)
	require.Equal(t, jetstream.LimitsPolicy, info.Config.Retention)
	require.Equal(t, int64(1), info.Config.MaxMsgsPerSubject)
	require.Equal(t, []string{bootstrap.SchedulesWildcardSubject}, info.Config.Subjects)
}

// TestCoreSchedulesStream_Idempotent verifies that calling ProvisionBuckets
// twice does not error and the stream retains AllowMsgSchedules: true.
func TestCoreSchedulesStream_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires embedded NATS")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	nc := startBootstrapNATS(t)

	bsJSONPath := t.TempDir() + "/lattice.bootstrap.json"
	_, err := bootstrap.LoadOrGenerate(bsJSONPath)
	require.NoError(t, err)

	seeder, err := bootstrap.NewSeeder(nc, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	require.NoError(t, seeder.ProvisionBuckets(ctx), "first ProvisionBuckets must not error")
	require.NoError(t, seeder.ProvisionBuckets(ctx), "second ProvisionBuckets (re-run) must not error")

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	stream, err := js.Stream(ctx, bootstrap.CoreSchedulesStreamName)
	require.NoError(t, err, "core-schedules stream must still exist after re-provision")
	info, err := stream.Info(ctx)
	require.NoError(t, err)
	require.True(t, info.Config.AllowMsgSchedules,
		"core-schedules stream must retain AllowMsgSchedules: true after re-provision")
}
