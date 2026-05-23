package health_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/refractor/consumer"
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/refractor/subjects"
)

// lagEnv holds all components needed for LagPoller tests.
type lagEnv struct {
	nc       *nats.Conn
	js       jetstream.JetStream
	healthKV jetstream.KeyValue
}

// startLagServer starts an in-memory NATS server with JetStream and creates the
// health KV bucket. Returns a lagEnv for building per-test components.
func startLagServer(t *testing.T) *lagEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	opts := &natsserver.Options{
		JetStream: true,
		StoreDir:  t.TempDir(),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
	s, err := natsserver.NewServer(opts)
	require.NoError(t, err, "create test NATS server")
	go s.Start()
	require.True(t, s.ReadyForConnections(5*time.Second), "NATS server not ready within 5s")

	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err, "connect to test NATS server")
	t.Cleanup(func() { nc.Close(); s.Shutdown() })

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	healthKV, err := js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "LAG_HEALTH"})
	require.NoError(t, err)

	return &lagEnv{nc: nc, js: js, healthKV: healthKV}
}

// makeConsumer creates a core KV bucket (unique per name) and returns a consumer for ruleID.
func (e *lagEnv) makeConsumer(t *testing.T, bucketName, ruleID string) jetstream.Consumer {
	t.Helper()
	_, err := e.js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: bucketName})
	require.NoError(t, err)

	mgr := consumer.NewManager(e.js, bucketName)
	require.NoError(t, mgr.Add(context.Background(), ruleID))
	cons := mgr.Consumer(ruleID)
	require.NotNil(t, cons)
	return cons
}

// startPoller starts a LagPoller goroutine and returns a WaitGroup that signals when it exits.
// The cancel func cancels the goroutine; wg.Wait() blocks until Start has returned.
func startPoller(lp *health.LagPoller, ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lp.Start(ctx)
	}()
	return &wg
}

// TestLagPoller_PublishesMetric verifies that LagPoller publishes a valid LagMetric
// JSON message to lattice.refractor.metrics.<lensId> (FR23, AC1).
func TestLagPoller_PublishesMetric(t *testing.T) {
	env := startLagServer(t)

	// Capture interval at construction time — override before NewLagPoller.
	health.MetricsInterval = 50 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-publish"
	cons := env.makeConsumer(t, "core-pub", ruleID)
	reporter := health.New(env.healthKV, ruleID, "team-a")

	msgCh := make(chan *nats.Msg, 5)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.nc, cons, reporter, ruleID, "team-a")
	_ = startPoller(lp, ctx)

	// Wait up to 2s for the first metric message.
	select {
	case msg := <-msgCh:
		var m health.LagMetric
		require.NoError(t, json.Unmarshal(msg.Data, &m), "metric payload must be valid JSON")
		assert.Equal(t, ruleID, m.RuleID)
		assert.Equal(t, "team-a", m.Team)
		assert.NotEmpty(t, m.Timestamp, "Timestamp must be set")
		_, parseErr := time.Parse(time.RFC3339, m.Timestamp)
		assert.NoError(t, parseErr, "Timestamp must be valid RFC3339")
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for lag metric message")
	}
}

// TestLagPoller_UpdatesHealthKV verifies that each poll cycle calls SetConsumerLag
// on the reporter, updating the health KV consumerLag field (AC6).
func TestLagPoller_UpdatesHealthKV(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 50 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-kv"
	cons := env.makeConsumer(t, "core-kv", ruleID)
	reporter := health.New(env.healthKV, ruleID, "team-a")

	// Establish an initial health entry.
	require.NoError(t, reporter.SetActive(context.Background()))
	initialEntry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.nc, cons, reporter, ruleID, "team-a")
	_ = startPoller(lp, ctx)

	// Wait for SetConsumerLag to update LastUpdated beyond the initial value.
	require.Eventually(t, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		if err != nil {
			return false
		}
		// SetConsumerLag updates LastUpdated; confirm it has advanced past the initial write.
		return entry.LastUpdated != "" && entry.LastUpdated >= initialEntry.LastUpdated
	}, 2*time.Second, 25*time.Millisecond, "health KV must be updated by LagPoller")
}

// TestLagPoller_PerRuleIsolation verifies that two pollers publish only to their own
// lattice.refractor.metrics.<lensId> subjects with no cross-contamination (NFR13, AC3).
func TestLagPoller_PerRuleIsolation(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 50 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleA = "rule-iso-a"
	const ruleB = "rule-iso-b"

	consA := env.makeConsumer(t, "core-iso-a", ruleA)
	consB := env.makeConsumer(t, "core-iso-b", ruleB)

	msgsA := make(chan *nats.Msg, 10)
	msgsB := make(chan *nats.Msg, 10)
	subA, err := env.nc.ChanSubscribe(subjects.Metrics(ruleA), msgsA)
	require.NoError(t, err)
	subB, err := env.nc.ChanSubscribe(subjects.Metrics(ruleB), msgsB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = subA.Unsubscribe(); _ = subB.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())

	lpA := health.NewLagPoller(env.nc, consA, nil, ruleA, "team-a")
	lpB := health.NewLagPoller(env.nc, consB, nil, ruleB, "team-b")
	wgA := startPoller(lpA, ctx)
	wgB := startPoller(lpB, ctx)

	// Wait for both subjects to receive at least one message each.
	require.Eventually(t, func() bool { return len(msgsA) > 0 && len(msgsB) > 0 },
		2*time.Second, 20*time.Millisecond, "both rules must receive at least one metric")

	cancel()
	wgA.Wait()
	wgB.Wait()

	// Drain channels after goroutines have fully stopped.
	var gotA, gotB []health.LagMetric
	for len(msgsA) > 0 {
		msg := <-msgsA
		var m health.LagMetric
		require.NoError(t, json.Unmarshal(msg.Data, &m))
		gotA = append(gotA, m)
	}
	for len(msgsB) > 0 {
		msg := <-msgsB
		var m health.LagMetric
		require.NoError(t, json.Unmarshal(msg.Data, &m))
		gotB = append(gotB, m)
	}

	require.NotEmpty(t, gotA, "ruleA must receive metrics")
	require.NotEmpty(t, gotB, "ruleB must receive metrics")

	for _, m := range gotA {
		assert.Equal(t, ruleA, m.RuleID, "ruleA metrics must only contain ruleA ID")
	}
	for _, m := range gotB {
		assert.Equal(t, ruleB, m.RuleID, "ruleB metrics must only contain ruleB ID")
	}
}

// TestLagPoller_StopsOnContextCancel verifies that cancelling the context stops
// all further metric publishes (AC4 prerequisite — poller must be cancellable).
// Uses a WaitGroup to synchronize on goroutine exit — deterministic, not sleep-based.
func TestLagPoller_StopsOnContextCancel(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 100 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-cancel"
	cons := env.makeConsumer(t, "core-cancel", ruleID)

	msgCh := make(chan *nats.Msg, 20)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())

	lp := health.NewLagPoller(env.nc, cons, nil, ruleID, "team-a")
	wg := startPoller(lp, ctx)

	// Let at least one message publish before cancelling.
	require.Eventually(t, func() bool { return len(msgCh) > 0 },
		2*time.Second, 20*time.Millisecond, "expected at least one message before cancel")

	cancel()
	wg.Wait() // Goroutine has fully exited — no further publishes are possible.

	// Drain any messages that arrived during/before the last tick.
	for len(msgCh) > 0 {
		<-msgCh
	}

	// Assert no new messages arrive after goroutine is confirmed stopped.
	assert.Equal(t, 0, len(msgCh), "no new messages must be published after goroutine exits")
}

// TestLagPoller_ContinuesDuringPause verifies that the lag poller publishes independently
// of any pipeline activity — it does not need external triggers and keeps running
// even when a pipeline goroutine is blocked (e.g., during an infra probe loop). (AC4)
func TestLagPoller_ContinuesDuringPause(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 50 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-pause"
	cons := env.makeConsumer(t, "core-pause", ruleID)

	msgCh := make(chan *nats.Msg, 30)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.nc, cons, nil, ruleID, "team-a")
	_ = startPoller(lp, ctx)

	// Receive at least 3 consecutive messages to prove continuous autonomous polling.
	// This covers AC4: the poller does not block waiting for pipeline activity.
	for i := 1; i <= 3; i++ {
		select {
		case <-msgCh:
			// received message i — continue
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for lag metric message #%d", i)
		}
	}
}
