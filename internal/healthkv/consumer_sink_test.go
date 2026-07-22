package healthkv

import (
	"context"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/substrate"
)

const testHealthBucket = "health-kv"

func startEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = jsstore.Dir(t)
	s := natsserver.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

func setupHarness(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := startEmbeddedNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "healthkv-test"})
	if err != nil {
		t.Fatalf("healthkv: Connect: %v", err)
	}
	t.Cleanup(conn.Close)

	_, err = conn.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:         testHealthBucket,
		LimitMarkerTTL: time.Second, // enables AllowMsgTTL so Reporter's KVPutWithTTL works in tests
	})
	if err != nil {
		t.Fatalf("healthkv: create health-kv bucket: %v", err)
	}
	return ctx, conn
}

func TestConsumerSink_PauseRestoreRoundTrip(t *testing.T) {
	for _, reason := range []substrate.PauseReason{substrate.PauseInfra, substrate.PauseStructural, substrate.PauseManual} {
		t.Run(string(reason), func(t *testing.T) {
			ctx, conn := setupHarness(t)
			states := NewConsumerStateCache()
			sink := NewConsumerSink(conn, testHealthBucket, "loom", "tgt-x", states)

			if err := sink.SetPaused(ctx, reason, "boom"); err != nil {
				t.Fatalf("SetPaused: %v", err)
			}

			// A fresh sink instance (simulating a restart) restores from KV.
			fresh := NewConsumerSink(conn, testHealthBucket, "loom", "tgt-x", NewConsumerStateCache())
			status, gotReason, err := fresh.Load(ctx)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if status != substrate.StatusPaused {
				t.Fatalf("status = %v, want StatusPaused", status)
			}
			if gotReason != reason {
				t.Fatalf("reason = %q, want %q", gotReason, reason)
			}

			want := map[substrate.PauseReason]string{
				substrate.PauseInfra:      "pausedInfra",
				substrate.PauseStructural: "pausedStructural",
				substrate.PauseManual:     "pausedManual",
			}[reason]
			if got := states.Snapshot()["tgt-x"]; got != want {
				t.Fatalf("original sink's cache = %q, want %q (SetPaused seeds the caller's own cache)", got, want)
			}
		})
	}
}

func TestConsumerSink_ActiveRoundTrip(t *testing.T) {
	ctx, conn := setupHarness(t)
	states := NewConsumerStateCache()
	sink := NewConsumerSink(conn, testHealthBucket, "weaver", "tgt-y", states)

	if err := sink.SetActive(ctx); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	fresh := NewConsumerSink(conn, testHealthBucket, "weaver", "tgt-y", NewConsumerStateCache())
	status, reason, err := fresh.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if status != substrate.StatusActive {
		t.Fatalf("status = %v, want StatusActive", status)
	}
	if reason != "" {
		t.Fatalf("reason = %q, want empty", reason)
	}
	if got := states.Snapshot()["tgt-y"]; got != "running" {
		t.Fatalf("cache = %q, want running", got)
	}
}

func TestConsumerSink_Load_MissingEntry(t *testing.T) {
	ctx, conn := setupHarness(t)
	states := NewConsumerStateCache()
	sink := NewConsumerSink(conn, testHealthBucket, "bridge", "never-written", states)

	status, reason, err := sink.Load(ctx)
	if err != nil {
		t.Fatalf("Load on absent key: %v", err)
	}
	if status != substrate.StatusActive || reason != "" {
		t.Fatalf("Load(absent) = (%v, %q), want (StatusActive, \"\")", status, reason)
	}
	if got := states.Snapshot()["never-written"]; got != "running" {
		t.Fatalf("cache seeded on absent key = %q, want running", got)
	}
}

func TestConsumerSink_Load_MalformedEntry(t *testing.T) {
	ctx, conn := setupHarness(t)
	states := NewConsumerStateCache()
	sink := NewConsumerSink(conn, testHealthBucket, "loom", "bad-doc", states)

	if _, err := conn.KVPut(ctx, testHealthBucket, sink.key, []byte("not json")); err != nil {
		t.Fatalf("seed malformed entry: %v", err)
	}

	status, reason, err := sink.Load(ctx)
	if err != nil {
		t.Fatalf("Load on malformed entry must not error: %v", err)
	}
	if status != substrate.StatusActive || reason != "" {
		t.Fatalf("Load(malformed) = (%v, %q), want (StatusActive, \"\")", status, reason)
	}
}

func TestPauseReasonFromString(t *testing.T) {
	cases := map[string]substrate.PauseReason{
		"manual":     substrate.PauseManual,
		"structural": substrate.PauseStructural,
		"infra":      substrate.PauseInfra,
		"":           substrate.PauseInfra,
		"unknown":    substrate.PauseInfra,
	}
	for in, want := range cases {
		if got := pauseReasonFromString(in); got != want {
			t.Errorf("pauseReasonFromString(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConsumerState(t *testing.T) {
	cases := []struct {
		paused bool
		reason substrate.PauseReason
		want   string
	}{
		{false, "", "running"},
		{true, substrate.PauseManual, "pausedManual"},
		{true, substrate.PauseStructural, "pausedStructural"},
		{true, substrate.PauseInfra, "pausedInfra"},
		{true, substrate.PauseReason("bogus"), "paused"},
	}
	for _, c := range cases {
		if got := consumerState(c.paused, c.reason); got != c.want {
			t.Errorf("consumerState(%v, %q) = %q, want %q", c.paused, c.reason, got, c.want)
		}
	}
}

func TestConsumerSink_Delete(t *testing.T) {
	ctx, conn := setupHarness(t)
	states := NewConsumerStateCache()
	sink := NewConsumerSink(conn, testHealthBucket, "loom", "tgt-z", states)

	if err := sink.SetPaused(ctx, substrate.PauseManual, "boom"); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	if err := sink.Delete(ctx); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := states.Snapshot()["tgt-z"]; ok {
		t.Fatalf("cache entry must be removed after Delete")
	}

	// Idempotent: deleting again (key already gone) must not error.
	if err := sink.Delete(ctx); err != nil {
		t.Fatalf("Delete on already-absent key must be a no-op: %v", err)
	}

	// A re-add after Delete restores active (no stale pause).
	fresh := NewConsumerSink(conn, testHealthBucket, "loom", "tgt-z", NewConsumerStateCache())
	status, _, err := fresh.Load(ctx)
	if err != nil {
		t.Fatalf("Load after Delete: %v", err)
	}
	if status != substrate.StatusActive {
		t.Fatalf("status after Delete+re-add = %v, want StatusActive (no stale pause)", status)
	}
}

func TestConsumerStateCache_ConcurrencySafe(t *testing.T) {
	c := NewConsumerStateCache()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			c.set("consumer", "running")
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = c.Snapshot()
		}(i)
	}
	wg.Wait()
}

func TestConsumerStateCache_SnapshotIsACopy(t *testing.T) {
	c := NewConsumerStateCache()
	c.set("a", "running")
	snap := c.Snapshot()
	snap["a"] = "mutated"
	if got := c.Snapshot()["a"]; got != "running" {
		t.Fatalf("mutating the returned snapshot must not affect the cache; got %q", got)
	}
}

func TestConsumerSink_KeyNamespacing(t *testing.T) {
	ctx, conn := setupHarness(t)
	sink := NewConsumerSink(conn, testHealthBucket, "weaver", "tgt-x", NewConsumerStateCache())
	if err := sink.SetActive(ctx); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	const wantKey = "health.weaver.consumer-state.tgt-x"
	if _, err := conn.KVGet(ctx, testHealthBucket, wantKey); err != nil {
		t.Fatalf("expected key %q to be written, KVGet: %v", wantKey, err)
	}
}

// TestConsumerSink_RestoreAcrossInstanceRestart proves the bug fix this key
// re-key was for: pause-state is keyed by consumer name alone (no instance
// segment), so a restart under a brand-new instance ID still finds the same
// key and restores the pause — unlike the old instance-scoped key, which a
// fresh instance ID would never revisit.
func TestConsumerSink_RestoreAcrossInstanceRestart(t *testing.T) {
	ctx, conn := setupHarness(t)

	// "Before restart": an instance pauses a consumer.
	before := NewConsumerSink(conn, testHealthBucket, "bridge", "poison-lane", NewConsumerStateCache())
	if err := before.SetPaused(ctx, substrate.PauseStructural, "poison message"); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}

	// "After restart": a fresh process, necessarily a fresh instance ID
	// (Contract #5 §5.1 — instance IDs are per-process, never reused), but
	// NewConsumerSink takes no instance argument at all — the same
	// consumer-scoped key is addressed regardless.
	after := NewConsumerSink(conn, testHealthBucket, "bridge", "poison-lane", NewConsumerStateCache())
	status, reason, err := after.Load(ctx)
	if err != nil {
		t.Fatalf("Load after restart: %v", err)
	}
	if status != substrate.StatusPaused {
		t.Fatalf("status after restart = %v, want StatusPaused (restore-across-restart must survive an instance-ID change)", status)
	}
	if reason != substrate.PauseStructural {
		t.Fatalf("reason after restart = %q, want %q", reason, substrate.PauseStructural)
	}
}
