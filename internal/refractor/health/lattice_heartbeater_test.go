// Verify per-Lens latency snapshots reach Health KV under
// `health.refractor.<instance>` (in the doc's metrics.lensLatency
// sub-map per Decision #5).
package health_test

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
	"github.com/asolgan/lattice/internal/refractor/health"
	"github.com/asolgan/lattice/internal/substrate"
)

func TestLatticeHeartbeater_EmitsLensLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}

	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	defer s.Shutdown()
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	js := conn.JetStream()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "health-kv"})
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	hb := health.NewLatticeHeartbeater(conn, "health-kv", "rfx-test", 10*time.Second, logger)
	hb.LensLatencyProvider = func() map[string]health.LensLatencySnapshot {
		return map[string]health.LensLatencySnapshot{
			"capability": {
				Count: 42,
				Mean:  5 * time.Millisecond,
				P95:   8 * time.Millisecond,
				P99:   12 * time.Millisecond,
			},
			"capabilityRoleIndex": {
				Count: 7,
				Mean:  3 * time.Millisecond,
				P95:   4 * time.Millisecond,
				P99:   4 * time.Millisecond,
			},
		}
	}

	// Run the heartbeater in the background; it emits an initial "starting"
	// document immediately, which is enough for us to verify the shape.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go hb.Run(hbCtx)

	// Poll for the Health KV entry.
	kv, err := js.KeyValue(ctx, "health-kv")
	require.NoError(t, err)

	deadline := time.Now().Add(10 * time.Second)
	var doc map[string]any
	for time.Now().Before(deadline) {
		entry, gErr := kv.Get(ctx, "health.refractor.rfx-test")
		if gErr == nil && entry != nil && len(entry.Value()) > 0 {
			if jerr := json.Unmarshal(entry.Value(), &doc); jerr == nil {
				if hasLensLatency(doc) {
					break
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, doc, "no health doc landed")

	metrics, _ := doc["metrics"].(map[string]any)
	require.NotNil(t, metrics, "metrics map missing")
	ll, _ := metrics["lensLatency"].(map[string]any)
	require.NotNil(t, ll, "lensLatency sub-map missing")

	cap, _ := ll["capability"].(map[string]any)
	require.NotNil(t, cap, "capability latency entry missing")
	require.EqualValues(t, 42, asInt(cap["count"]))
	require.EqualValues(t, (5 * time.Millisecond).Nanoseconds(), asInt(cap["meanNs"]))
	require.EqualValues(t, (8 * time.Millisecond).Nanoseconds(), asInt(cap["p95Ns"]))
	require.EqualValues(t, (12 * time.Millisecond).Nanoseconds(), asInt(cap["p99Ns"]))

	idx, _ := ll["capabilityRoleIndex"].(map[string]any)
	require.NotNil(t, idx, "capabilityRoleIndex latency entry missing")
	require.EqualValues(t, 7, asInt(idx["count"]))
}

func TestLatticeHeartbeater_SkipsZeroSampleLenses(t *testing.T) {
	if testing.Short() {
		t.Skip("requires NATS JetStream")
	}
	opts := &natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: jsstore.Dir(t)}
	s := natstest.RunServer(opts)
	defer s.Shutdown()
	nc, err := nats.Connect(s.ClientURL())
	require.NoError(t, err)
	defer nc.Close()
	conn, err := substrate.Wrap(nc)
	require.NoError(t, err)
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	js := conn.JetStream()
	_, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "health-kv-zero"})
	require.NoError(t, err)
	hb := health.NewLatticeHeartbeater(conn, "health-kv-zero", "rfx-zero", 10*time.Second, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	// Provider returns a zero-count snapshot — the heartbeater must
	// SKIP this Lens (per Decision #5: don't report misleading zeros).
	hb.LensLatencyProvider = func() map[string]health.LensLatencySnapshot {
		return map[string]health.LensLatencySnapshot{
			"capability": {Count: 0},
		}
	}

	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go hb.Run(hbCtx)

	kv, err := js.KeyValue(ctx, "health-kv-zero")
	require.NoError(t, err)

	deadline := time.Now().Add(5 * time.Second)
	var doc map[string]any
	for time.Now().Before(deadline) {
		entry, gErr := kv.Get(ctx, "health.refractor.rfx-zero")
		if gErr == nil && entry != nil && len(entry.Value()) > 0 {
			if jerr := json.Unmarshal(entry.Value(), &doc); jerr == nil {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, doc)
	metrics, _ := doc["metrics"].(map[string]any)
	require.NotNil(t, metrics)
	// lensLatency must be absent (no non-zero entries to publish).
	_, hasLL := metrics["lensLatency"]
	require.False(t, hasLL, "lensLatency should be absent when all lenses have zero samples")
}

func hasLensLatency(doc map[string]any) bool {
	m, _ := doc["metrics"].(map[string]any)
	if m == nil {
		return false
	}
	_, ok := m["lensLatency"].(map[string]any)
	return ok
}

func asInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	}
	return 0
}
