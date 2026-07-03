// Package bypass holds the outcome-level, whole-system adversarial residual
// that isn't fully replicated by any single mechanism's colocated white-box
// test — the assembled read-path proof and the Capability Lens composition
// vectors. Run embedded via `go test ./internal/bypass/...`; no destructive
// stack recycle, no marker write.
package bypass

import (
	"context"
	"log/slog"
	"os"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/test"

	"github.com/asolgan/lattice/internal/jsstore"
	"github.com/asolgan/lattice/internal/substrate"
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
	opts.StoreDir = jsstore.Dir(t)
	s := natsserver.RunServer(&opts)
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

// kvPresent returns true if the key exists in the named bucket.
func kvPresent(ctx context.Context, conn *substrate.Conn, bucket, key string) bool {
	_, err := conn.KVGet(ctx, bucket, key)
	return err == nil
}
