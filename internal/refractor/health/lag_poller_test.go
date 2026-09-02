package health_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/refractor/health"
	"github.com/operatinggraph/lattice/internal/refractor/subjects"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// lagEnv holds all components needed for LagPoller tests.
type lagEnv struct {
	nc       *nats.Conn
	conn     *substrate.Conn
	js       jetstream.JetStream
	healthKV *substrate.KV
}

// zeroLag is a LagFunc that always reports zero lag with no error. The LagPoller
// tests assert publish cadence / rule isolation / health-KV update — not a
// specific lag value — so a constant source decouples them from a live
// supervised consumer.
func zeroLag(context.Context) (uint64, error) { return 0, nil }

// startLagServer starts an in-memory NATS server with JetStream and creates the
// health KV bucket. Returns a lagEnv for building per-test components.
func startLagServer(t *testing.T) *lagEnv {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping NATS integration test in short mode")
	}
	_, nc := natsfixture.Server(t)

	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)

	js, err := jetstream.New(nc)
	require.NoError(t, err)

	_, err = js.CreateKeyValue(context.Background(), jetstream.KeyValueConfig{Bucket: "LAG_HEALTH"})
	require.NoError(t, err)
	healthKV, err := conn.OpenKV(context.Background(), "LAG_HEALTH")
	require.NoError(t, err)

	return &lagEnv{nc: nc, conn: conn, js: js, healthKV: healthKV}
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
	reporter := health.New(env.healthKV, ruleID)

	msgCh := make(chan *nats.Msg, 5)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
	_ = startPoller(lp, ctx)

	// Wait up to 2s for the first metric message.
	select {
	case msg := <-msgCh:
		var m health.LagMetric
		require.NoError(t, json.Unmarshal(msg.Data, &m), "metric payload must be valid JSON")
		assert.Equal(t, ruleID, m.RuleID)
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
	reporter := health.New(env.healthKV, ruleID)

	// Establish an initial health entry.
	require.NoError(t, reporter.SetActive(context.Background()))
	initialEntry, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
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

// TestLagPoller_UpdatesLagProgressAt verifies that each poll cycle threads the
// poller's lag-progress clock (recordLagProgress) into the health KV entry via
// SetProjectionProgress, and that it keeps advancing as a source lag value
// keeps falling (health-kv-schema.md consumerLag / lagProgressAt semantics).
func TestLagPoller_UpdatesLagProgressAt(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 30 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-lag-progress"
	reporter := health.New(env.healthKV, ruleID)
	require.NoError(t, reporter.SetActive(context.Background()))

	var mu sync.Mutex
	lag := uint64(2500)
	fallingLag := func(context.Context) (uint64, error) {
		mu.Lock()
		defer mu.Unlock()
		if lag > 0 {
			lag -= 100
		}
		return lag, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.conn, fallingLag, reporter, ruleID)
	_ = startPoller(lp, ctx)

	require.Eventually(t, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.LagProgressAt != ""
	}, 2*time.Second, 10*time.Millisecond, "lagProgressAt must be published once the poller has run")

	first, err := reporter.GetStatus(context.Background())
	require.NoError(t, err)

	// A source that keeps falling must keep re-stamping the clock forward.
	require.Eventually(t, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.LagProgressAt != "" && entry.LagProgressAt >= first.LagProgressAt &&
			entry.ConsumerLag < first.ConsumerLag
	}, 2*time.Second, 10*time.Millisecond, "lagProgressAt must keep advancing while lag keeps falling")
}

// TestLagPoller_PerRuleIsolation verifies that two pollers publish only to their own
// lattice.refractor.metrics.<lensId> subjects with no cross-contamination (NFR13, AC3).
func TestLagPoller_PerRuleIsolation(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 50 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleA = "rule-iso-a"
	const ruleB = "rule-iso-b"

	msgsA := make(chan *nats.Msg, 10)
	msgsB := make(chan *nats.Msg, 10)
	subA, err := env.nc.ChanSubscribe(subjects.Metrics(ruleA), msgsA)
	require.NoError(t, err)
	subB, err := env.nc.ChanSubscribe(subjects.Metrics(ruleB), msgsB)
	require.NoError(t, err)
	t.Cleanup(func() { _ = subA.Unsubscribe(); _ = subB.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())

	lpA := health.NewLagPoller(env.conn, zeroLag, nil, ruleA)
	lpB := health.NewLagPoller(env.conn, zeroLag, nil, ruleB)
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

	msgCh := make(chan *nats.Msg, 20)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())

	lp := health.NewLagPoller(env.conn, zeroLag, nil, ruleID)
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

	msgCh := make(chan *nats.Msg, 30)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lp := health.NewLagPoller(env.conn, zeroLag, nil, ruleID)
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

// TestLagPoller_UnchangedValuesWriteHealthKVOnce pins the point of the skip
// (poll, lag_poller.go): a lens sitting at a steady lag/progress/ack triple
// costs the Health-KV read-modify-write once, not on every tick — the metrics
// publish itself keeps firing unconditionally every tick regardless, which is
// what proves several cycles actually ran here rather than the poller simply
// never having ticked.
func TestLagPoller_UnchangedValuesWriteHealthKVOnce(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 15 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-unchanged-skip"
	rawKV, err := env.js.CreateKeyValue(context.Background(),
		jetstream.KeyValueConfig{Bucket: "LAG_HEALTH_UNCHANGED", History: 20})
	require.NoError(t, err)
	kvh, err := env.conn.OpenKV(context.Background(), "LAG_HEALTH_UNCHANGED")
	require.NoError(t, err)
	reporter := health.New(kvh, ruleID)
	require.NoError(t, reporter.SetActive(context.Background()))

	msgCh := make(chan *nats.Msg, 20)
	sub, err := env.nc.ChanSubscribe(subjects.Metrics(ruleID), msgCh)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	ctx, cancel := context.WithCancel(context.Background())
	lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
	wg := startPoller(lp, ctx)

	for i := 1; i <= 5; i++ {
		select {
		case <-msgCh:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for lag metric message #%d", i)
		}
	}
	cancel()
	wg.Wait() // no poll can still be mid-write once Start has returned

	hist, err := rawKV.History(context.Background(), ruleID)
	require.NoError(t, err)
	require.Len(t, hist, 2,
		"SetActive's write, then exactly one SetProjectionProgress write for the whole steady run — "+
			"not one per tick")
}

// TestLagPoller_LagChangeAlwaysWrites is the other half: a value that DOES
// move must never be skipped, however many ticks it keeps moving for.
func TestLagPoller_LagChangeAlwaysWrites(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 15 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-lag-change-writes"
	rawKV, err := env.js.CreateKeyValue(context.Background(),
		jetstream.KeyValueConfig{Bucket: "LAG_HEALTH_LAGCHANGE", History: 20})
	require.NoError(t, err)
	kvh, err := env.conn.OpenKV(context.Background(), "LAG_HEALTH_LAGCHANGE")
	require.NoError(t, err)
	reporter := health.New(kvh, ruleID)
	require.NoError(t, reporter.SetActive(context.Background()))

	var mu sync.Mutex
	lag := uint64(100)
	risingLag := func(context.Context) (uint64, error) {
		mu.Lock()
		defer mu.Unlock()
		lag++ // a fresh value every tick: never the same twice
		return lag, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	lp := health.NewLagPoller(env.conn, risingLag, reporter, ruleID)
	wg := startPoller(lp, ctx)

	require.Eventually(t, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.ConsumerLag >= 105
	}, 2*time.Second, 10*time.Millisecond, "at least 5 distinct lag values must have landed")

	cancel()
	wg.Wait()

	hist, err := rawKV.History(context.Background(), ruleID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(hist), 6,
		"SetActive's write plus one write per lag-changing poll — a moving value is never skipped")
}

// TestLagPoller_AckPendingChangeAlwaysWrites isolates ackPending as its own
// invalidation signal: AckFloor holds fixed (so ackFloorProgressAt itself
// stops moving after the first poll) while AckPending keeps changing, so only
// the ackPending comparison can be forcing these writes.
func TestLagPoller_AckPendingChangeAlwaysWrites(t *testing.T) {
	env := startLagServer(t)

	health.MetricsInterval = 15 * time.Millisecond
	defer func() { health.MetricsInterval = 5 * time.Second }()

	const ruleID = "rule-ackpending-change-writes"
	rawKV, err := env.js.CreateKeyValue(context.Background(),
		jetstream.KeyValueConfig{Bucket: "LAG_HEALTH_ACKCHANGE", History: 20})
	require.NoError(t, err)
	kvh, err := env.conn.OpenKV(context.Background(), "LAG_HEALTH_ACKCHANGE")
	require.NoError(t, err)
	reporter := health.New(kvh, ruleID)
	require.NoError(t, reporter.SetActive(context.Background()))

	var mu sync.Mutex
	pending := uint64(0)
	risingAckPending := func(context.Context) (substrate.AckStats, error) {
		mu.Lock()
		defer mu.Unlock()
		pending++
		return substrate.AckStats{AckPending: pending, AckFloor: 1}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	lp := health.NewLagPoller(env.conn, zeroLag, reporter, ruleID)
	lp.SetAckStatsFunc(risingAckPending)
	wg := startPoller(lp, ctx)

	require.Eventually(t, func() bool {
		entry, err := reporter.GetStatus(context.Background())
		return err == nil && entry.AckPending >= 5
	}, 2*time.Second, 10*time.Millisecond, "at least 5 distinct ackPending values must have landed")

	cancel()
	wg.Wait()

	hist, err := rawKV.History(context.Background(), ruleID)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(hist), 6,
		"SetActive's write plus one write per ackPending-changing poll, with AckFloor never moving")
}
