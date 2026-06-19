// Package bypass contains the Phase 1 Gate 2 adversarial test suite.
// These tests prove that all four architectural bypass categories are
// impossible against the 9-step Processor commit path (Stories 1.3-1.9).
//
// Run via: make test-bypass
package bypass

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/substrate"
)

const (
	bypassCoreBucket   = "core-kv"
	bypassHealthBucket = "health-kv"
	bypassOpsStream    = "core-operations"

	// Stable test NanoIDs — all 20 chars, all from the Lattice alphabet
	// (ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789).
	// These are fixed so test output is deterministic across runs.
	bypassNanoID1 = "vrLcWB6X8aUWR3VZaJXw"
	bypassNanoID2 = "bt4C9pk7s7UP9K8B3e2Q"
	bypassNanoID3 = "iXeNFpA4iznhXTf8KVKr"
)

// bypassLogger returns a minimal logger for tests.
func bypassLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// startBypassNATS spins up an in-process JetStream-enabled NATS server
// for the bypass test suite. Each test gets a fresh server.
func startBypassNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		// Remove JetStream storage dir to prevent state leakage between
		// tests that reuse the same port (port -1 → OS-assigned ephemeral).
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
		s.Shutdown()
	})
	return s.ClientURL()
}

// setupBypassHarness connects to NATS and provisions Core KV, Health KV,
// and the core-operations stream. Returns context + conn.
func setupBypassHarness(t *testing.T) (context.Context, *substrate.Conn) {
	t.Helper()
	url := startBypassNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, err := substrate.Connect(ctx, substrate.ConnectOpts{URL: url, Name: "bypass-test"})
	if err != nil {
		t.Fatalf("bypass: Connect: %v", err)
	}
	t.Cleanup(conn.Close)

	provisionBypassInfra(t, ctx, conn)
	return ctx, conn
}

// provisionBypassInfra creates the KV buckets and streams needed by bypass tests.
func provisionBypassInfra(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	js := conn.JetStream()

	// Core KV + Health KV buckets.
	for _, bucket := range []string{bypassCoreBucket, bypassHealthBucket} {
		_, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
			Bucket:         bucket,
			LimitMarkerTTL: time.Second,
		})
		if err != nil {
			t.Fatalf("bypass: create KV bucket %q: %v", bucket, err)
		}
	}

	// AllowAtomicPublish on Core KV's underlying stream.
	streamName := "KV_" + bypassCoreBucket
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		t.Fatalf("bypass: get stream %q: %v", streamName, err)
	}
	cfg := stream.CachedInfo().Config
	cfg.AllowAtomicPublish = true
	if _, err := js.UpdateStream(ctx, cfg); err != nil {
		t.Fatalf("bypass: enable AllowAtomicPublish: %v", err)
	}

	// core-operations stream: all ops.> subjects.
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     bypassOpsStream,
		Subjects: []string{"ops.>"},
	})
	if err != nil {
		t.Fatalf("bypass: create core-operations stream: %v", err)
	}
}

// provisionEventsStream creates the core-events stream (needed when the
// full commit path including the outbox publish is exercised).
func provisionEventsStream(t *testing.T, ctx context.Context, conn *substrate.Conn) {
	t.Helper()
	_, err := conn.JetStream().CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:               "core-events",
		Subjects:           []string{"events.>"},
		AllowAtomicPublish: true,
	})
	if err != nil {
		t.Fatalf("bypass: create core-events stream: %v", err)
	}
}

// kvPresent returns true if the key exists in the named bucket.
func kvPresent(ctx context.Context, conn *substrate.Conn, bucket, key string) bool {
	_, err := conn.KVGet(ctx, bucket, key)
	return err == nil
}

// marshalJSON is a convenience wrapper.
func marshalJSON(v interface{}) []byte {
	b, _ := json.Marshal(v)
	return b
}
