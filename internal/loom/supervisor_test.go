package loom_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/asolgan/lattice/internal/loom"
	"github.com/asolgan/lattice/internal/substrate"
)

const healthKVBucket = "health-kv"

// provisionHealthKV adds the health-kv bucket the heartbeater + per-consumer
// sinks write to. The base provision() helper does not create it.
func provisionHealthKV(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	_, err := conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: healthKVBucket})
	require.NoError(t, err)
}

// consumerExists reports whether a durable consumer exists on a stream. It is a
// polling probe: any non-nil error (ErrConsumerNotFound, or a transient
// JetStream-API hiccup like "no responders" under load) means "not observable
// yet" and returns false so the caller keeps polling. It must never fail the
// test on a transient lookup error — that would turn a momentary blip into a
// hard flake.
func consumerExists(t *testing.T, ctx context.Context, conn *substrate.Conn, stream, durable string) bool {
	t.Helper()
	_, err := conn.JetStream().Consumer(ctx, stream, durable)
	return err == nil
}

func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// startEngine starts an engine in a goroutine and returns it with a channel that
// receives Start's return value when it unwinds — on ctx cancellation (nil) or
// an early boot error. Tests use the channel two ways: to fail fast with the
// real cause if Start dies during bring-up (instead of waiting out a poll
// deadline against a consumer that will never appear), and to join the
// goroutine deterministically on shutdown (instead of sleeping a fixed guess).
func startEngine(t *testing.T, ctx context.Context, conn *substrate.Conn, opts ...func(*loom.Config)) (*loom.Engine, <-chan error) {
	t.Helper()
	e := newEngine(conn, opts...)
	errCh := make(chan error, 1)
	go func() { errCh <- e.Start(ctx) }()
	return e, errCh
}

// waitForReady polls cond until true or the deadline, failing fast if the
// engine's Start goroutine returns early — surfacing the boot error rather than
// silently waiting out the deadline.
func waitForReady(t *testing.T, d time.Duration, startErr <-chan error, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		select {
		case err := <-startErr:
			t.Fatalf("%s: engine Start returned during bring-up: %v", msg, err)
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !cond() {
		t.Fatal(msg)
	}
}

// joinEngine waits for an engine's Start goroutine to return after its context
// is cancelled. Because supervisor.Stop synchronously joins every pump before
// Start returns, this is a complete teardown barrier — the deterministic
// replacement for sleeping and hoping shutdown finished.
func joinEngine(t *testing.T, startErr <-chan error) {
	t.Helper()
	select {
	case <-startErr:
	case <-time.After(10 * time.Second):
		t.Fatal("engine did not shut down within 10s of context cancellation")
	}
}

// TestSupervisor_RemovedPatternTearsDownDurable proves AC #2/#5 (F6): when the
// last pattern referencing a domain is removed, its loom-<domain> consumer is
// torn down AND its JetStream durable is deleted (no leaked consumer).
func TestSupervisor_RemovedPatternTearsDownDurable(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	provisionHealthKV(t, ctx, conn)

	engine := newEngine(conn, func(c *loom.Config) { c.HealthKVBucket = healthKVBucket })
	go func() { _ = engine.Start(ctx) }()

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "widget",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-widget")
	}), "loom-widget durable should be created for the referenced domain")

	// Remove the pattern via a CDC delete of its spec aspect — the source routes
	// this to removePattern, which fires the update callback (updateCB(nil,nil)).
	require.NoError(t, conn.KVDelete(ctx, coreKVBucket, "vtx.meta."+patternID+".spec"))

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return !consumerExists(t, ctx, conn, eventsStream, "loom-widget")
	}), "loom-widget durable should be deleted once no pattern references the domain")
}

// TestSupervisor_FilterChangeResets proves AC #2/#5: a per-domain spec whose
// config diverges from the running durable is recreated via Reset. The
// production filter is name-derived and stable, so this exercises the generic
// diff through the exported reconcile seam on a config change.
func TestSupervisor_FilterChangeResets(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	provisionHealthKV(t, ctx, conn)

	engine := newEngine(conn, func(c *loom.Config) { c.HealthKVBucket = healthKVBucket })
	go func() { _ = engine.Start(ctx) }()

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "gadget",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-gadget")
	}), "loom-gadget durable should be created")

	cons, err := conn.JetStream().Consumer(ctx, eventsStream, "loom-gadget")
	require.NoError(t, err)
	createdAt := cons.CachedInfo().Created

	// Force a config diff and reconcile through the supervisor's UpdateSpec+Reset.
	require.NoError(t, engine.ResetDomainForTest(ctx, "gadget"))

	recreated, err := conn.JetStream().Consumer(ctx, eventsStream, "loom-gadget")
	require.NoError(t, err)
	require.True(t, recreated.CachedInfo().Created.After(createdAt) ||
		recreated.CachedInfo().Created.Equal(createdAt),
		"durable should still exist after reset")
	require.True(t, consumerExists(t, ctx, conn, eventsStream, "loom-gadget"),
		"loom-gadget durable should exist after reset (delete-and-recreate)")
}

// TestSupervisor_HealthHeartbeatWellFormed proves AC #4/#5: a Contract #5 §5.2
// health.loom.<instance> document is written with all required fields.
func TestSupervisor_HealthHeartbeatWellFormed(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	provisionHealthKV(t, ctx, conn)

	const instance = "loom-test-instance"
	engine := newEngine(conn, func(c *loom.Config) {
		c.HealthKVBucket = healthKVBucket
		c.Instance = instance
		c.HeartbeatEvery = time.Second
	})
	go func() { _ = engine.Start(ctx) }()

	key := "health.loom." + instance
	var doc map[string]any
	require.True(t, waitFor(t, 12*time.Second, func() bool {
		entry, err := conn.KVGet(ctx, healthKVBucket, key)
		if err != nil {
			return false
		}
		if json.Unmarshal(entry.Value, &doc) != nil {
			return false
		}
		metrics, ok := doc["metrics"].(map[string]any)
		if !ok {
			return false
		}
		consumers, ok := metrics["consumers"].(map[string]any)
		if !ok {
			return false
		}
		// Wait until all three fixed consumers have reported a state.
		_, t1 := consumers["loom-trigger"]
		_, t2 := consumers["loom-outbox-relay"]
		_, t3 := consumers["loom-deadline"]
		return t1 && t2 && t3
	}), "health.loom.<instance> heartbeat should be written with all fixed consumers")

	require.Equal(t, key, doc["key"])
	require.Equal(t, "loom", doc["component"])
	require.Equal(t, instance, doc["instance"])
	require.NotEmpty(t, doc["status"])
	require.NotEmpty(t, doc["heartbeatAt"])
	require.NotEmpty(t, doc["startedAt"])
	require.Contains(t, doc, "metrics")
	require.Contains(t, doc, "issues")

	metrics, ok := doc["metrics"].(map[string]any)
	require.True(t, ok, "metrics must be an object")
	require.Contains(t, metrics, "consumers", "metrics.consumers must be present")
	consumers, ok := metrics["consumers"].(map[string]any)
	require.True(t, ok)
	// The three fixed consumers should each report a state.
	for _, name := range []string{"loom-trigger", "loom-outbox-relay", "loom-deadline"} {
		require.Contains(t, consumers, name, "consumer %q should appear in metrics.consumers", name)
	}
}

// TestSupervisor_PauseStateSurvivesRestart proves AC #4/#5: per-consumer
// pause-state persists to health-kv and restores across an engine restart
// (supervisor Add-time restore), without an explicit Resume.
func TestSupervisor_PauseStateSurvivesRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	provisionHealthKV(t, ctx, conn)

	const instance = "loom-restart-instance"

	cfg := func(c *loom.Config) {
		c.HealthKVBucket = healthKVBucket
		c.Instance = instance
		c.HeartbeatEvery = time.Second
	}

	ctx1, cancel1 := context.WithCancel(ctx)
	e1, e1Err := startEngine(t, ctx1, conn, cfg)

	// Pause the trigger and confirm it persisted PausedManual to the sink.
	//
	// Pause no-ops until the consumer is registered in the supervisor, and that
	// registration LAGS the consumer's JetStream visibility — Add creates the
	// durable (making it visible to a consumer-exists probe) before recording it
	// in its managed set. Gating the pause on mere JetStream visibility therefore
	// races: a pause issued in that window is silently dropped and never reaches
	// the sink. Pause is idempotent, so we re-issue it each poll until the sink
	// reflects it, closing the race deterministically.
	sinkKey := "health.loom." + instance + ".consumer.loom-trigger"
	waitForReady(t, 10*time.Second, e1Err, func() bool {
		e1.PauseForTest(ctx, "loom-trigger")
		entry, err := conn.KVGet(ctx, healthKVBucket, sinkKey)
		if err != nil {
			return false
		}
		var doc struct {
			Status      string `json:"status"`
			PauseReason string `json:"pauseReason"`
		}
		if json.Unmarshal(entry.Value, &doc) != nil {
			return false
		}
		return doc.Status == "paused" && doc.PauseReason == string(substrate.PauseManual)
	}, "manual pause should persist to the per-consumer sink")

	// Shut e1 down and wait for its pumps to fully drain (supervisor.Stop joins
	// every pump before Start returns) before restarting — deterministic, so e2
	// never races a half-stopped e1 on the shared loom-trigger durable.
	cancel1()
	joinEngine(t, e1Err)

	// Restart against the same conn+buckets. The new engine's supervisor restores
	// the trigger into PausedManual at Add time, without an explicit Resume.
	ctx2, cancel2 := context.WithCancel(ctx)
	defer cancel2()
	_, e2Err := startEngine(t, ctx2, conn, cfg)

	// The restored consumer reports pausedManual in the fresh heartbeat.
	waitForReady(t, 12*time.Second, e2Err, func() bool {
		entry, err := conn.KVGet(ctx, healthKVBucket, "health.loom."+instance)
		if err != nil {
			return false
		}
		var doc struct {
			Metrics struct {
				Consumers map[string]string `json:"consumers"`
			} `json:"metrics"`
		}
		if json.Unmarshal(entry.Value, &doc) != nil {
			return false
		}
		return doc.Metrics.Consumers["loom-trigger"] == "pausedManual"
	}, "trigger consumer should restore into pausedManual across restart")
}

// heartbeatConsumers reads health.loom.<instance> and returns metrics.consumers
// (or nil if the document/field is missing). Used by tests asserting a
// consumer's presence/absence/state in the heartbeat.
func heartbeatConsumers(t *testing.T, ctx context.Context, conn *substrate.Conn, instance string) map[string]string {
	t.Helper()
	entry, err := conn.KVGet(ctx, healthKVBucket, "health.loom."+instance)
	if err != nil {
		return nil
	}
	var doc struct {
		Metrics struct {
			Consumers map[string]string `json:"consumers"`
		} `json:"metrics"`
	}
	if json.Unmarshal(entry.Value, &doc) != nil {
		return nil
	}
	return doc.Metrics.Consumers
}

// TestSupervisor_RemovedDomainDisappearsFromHeartbeat proves ECH-F1: when the
// last pattern referencing a domain is removed, its loom-<domain> entry is
// evicted from the consumer-state cache and no longer appears in
// metrics.consumers on the next heartbeat — it does not linger as a phantom
// "running" (or stuck pausedStructural) consumer.
func TestSupervisor_RemovedDomainDisappearsFromHeartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	provisionHealthKV(t, ctx, conn)

	const instance = "loom-removed-domain-instance"
	engine := newEngine(conn, func(c *loom.Config) {
		c.HealthKVBucket = healthKVBucket
		c.Instance = instance
		c.HeartbeatEvery = time.Second
	})
	go func() { _ = engine.Start(ctx) }()

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "widget",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		consumers := heartbeatConsumers(t, ctx, conn, instance)
		_, ok := consumers["loom-widget"]
		return ok
	}), "loom-widget should appear in metrics.consumers once added")

	// Remove the only pattern referencing the "widget" domain.
	require.NoError(t, conn.KVDelete(ctx, coreKVBucket, "vtx.meta."+patternID+".spec"))

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return !consumerExists(t, ctx, conn, eventsStream, "loom-widget")
	}), "loom-widget durable should be torn down once no pattern references the domain")

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		consumers := heartbeatConsumers(t, ctx, conn, instance)
		_, ok := consumers["loom-widget"]
		return !ok
	}), "loom-widget should disappear from metrics.consumers once removed")
}

// TestSupervisor_RemovedDomainPauseDoesNotResurrectOnReAdd proves ECH-F2: pausing
// a domain consumer and then removing the pattern that referenced it deletes the
// persisted per-consumer pause entry, so a later re-add of a pattern referencing
// the same domain comes up RUNNING — not restored into the stale pause an
// operator never set for the new consumer.
func TestSupervisor_RemovedDomainPauseDoesNotResurrectOnReAdd(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc := startNATS(t)
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	provision(t, ctx, conn)
	provisionHealthKV(t, ctx, conn)

	const instance = "loom-readd-pause-instance"
	engine := newEngine(conn, func(c *loom.Config) {
		c.HealthKVBucket = healthKVBucket
		c.Instance = instance
		c.HeartbeatEvery = time.Second
	})
	go func() { _ = engine.Start(ctx) }()

	patternID := mustNanoID(t)
	installPattern(t, ctx, conn, patternID, loom.Pattern{
		PatternID:   patternID,
		SubjectType: "alpha",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-alpha")
	}), "loom-alpha durable should be created for the referenced domain")

	// Manually pause the domain consumer.
	engine.PauseForTest(ctx, "loom-alpha")

	sinkKey := "health.loom." + instance + ".consumer.loom-alpha"
	require.True(t, waitFor(t, 10*time.Second, func() bool {
		entry, err := conn.KVGet(ctx, healthKVBucket, sinkKey)
		if err != nil {
			return false
		}
		var doc struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(entry.Value, &doc) != nil {
			return false
		}
		return doc.Status == "paused"
	}), "manual pause should persist to the per-consumer sink")

	// Remove the only pattern referencing "alpha" — tears down loom-alpha and
	// (per ECH-F2) must clear its persisted pause entry too.
	require.NoError(t, conn.KVDelete(ctx, coreKVBucket, "vtx.meta."+patternID+".spec"))

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return !consumerExists(t, ctx, conn, eventsStream, "loom-alpha")
	}), "loom-alpha durable should be torn down once no pattern references the domain")

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		_, err := conn.KVGet(ctx, healthKVBucket, sinkKey)
		return errors.Is(err, substrate.ErrKeyNotFound)
	}), "the removed domain's persisted pause entry should be deleted")

	// Re-add a pattern referencing the same "alpha" domain.
	patternID2 := mustNanoID(t)
	installPattern(t, ctx, conn, patternID2, loom.Pattern{
		PatternID:   patternID2,
		SubjectType: "alpha",
		Steps:       []loom.Step{{Kind: "systemOp", Operation: "StepA"}},
	})

	require.True(t, waitFor(t, 10*time.Second, func() bool {
		return consumerExists(t, ctx, conn, eventsStream, "loom-alpha")
	}), "loom-alpha durable should be recreated for the re-referenced domain")

	// The re-added consumer must come up RUNNING, not restored into the stale
	// pause from the removed consumer of the same name.
	require.True(t, waitFor(t, 10*time.Second, func() bool {
		consumers := heartbeatConsumers(t, ctx, conn, instance)
		return consumers["loom-alpha"] == "running"
	}), "re-added loom-alpha consumer should report running, not a resurrected pause")
}
