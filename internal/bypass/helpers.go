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

	"github.com/operatinggraph/lattice/internal/natsfixture"
	"github.com/operatinggraph/lattice/internal/substrate"
)

// bypassLogger returns a minimal logger for tests.
func bypassLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// startBypassNATS spins up an in-process JetStream-enabled NATS server
// for the bypass test suite. Each test gets a fresh server.
func startBypassNATS(t *testing.T) string {
	t.Helper()
	s := natsfixture.StartServer(t)
	return s.ClientURL()
}

// kvPresent returns true if the key exists in the named bucket.
func kvPresent(ctx context.Context, conn *substrate.Conn, bucket, key string) bool {
	_, err := conn.KVGet(ctx, bucket, key)
	return err == nil
}
