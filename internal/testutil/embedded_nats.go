// Package testutil provides embedded NATS and Processor-pipeline helpers
// for test packages outside internal/processor that need the same fixture
// infrastructure. Helpers here are test-only (callers must pass *testing.T)
// but live in non-test files so external test packages can import them.
package testutil

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/asolgan/lattice/internal/processor"
)

// StartEmbeddedNATS spins up an in-process JetStream-enabled NATS server
// and returns its client URL. The server is shut down via t.Cleanup.
//
// Each call allocates a unique StoreDir under t.TempDir() so that
// concurrently running test packages do not share the JetStream file store.
// Without an explicit StoreDir, NATS defaults to os.TempDir()/jetstream,
// which is shared across all processes and causes cross-package contamination
// when tests run in parallel.
func StartEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		s.Shutdown()
		_ = server.VERSION
	})
	return s.ClientURL()
}

// TestLogger returns a slog Logger configured at WARN level for tests.
func TestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// DriveOne runs the consumer loop until exactly one message is handled
// by `cp`, asserts the outcome matches `want` (if non-empty), and returns
// the observed outcome. Mirrors the processor-internal `driveOne` test
// helper used by Story 4.x integration tests.
func DriveOne(t *testing.T, ctx context.Context, cp *processor.CommitPath, cons jetstream.Consumer, want processor.MessageOutcome) processor.MessageOutcome {
	t.Helper()
	got := make(chan processor.MessageOutcome, 1)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		outcome := cp.HandleMessage(ctx, m)
		select {
		case got <- outcome:
		default:
		}
	})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer cc.Stop()
	select {
	case outcome := <-got:
		if want != "" && outcome != want {
			t.Fatalf("outcome mismatch: got %q want %q", outcome, want)
		}
		// Brief drain to let JetStream flush the ack.
		time.Sleep(100 * time.Millisecond)
		return outcome
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for outcome (want %q)", want)
		return ""
	}
}

// GenReqID synthesizes a 20-char NanoID-alphabet request id from a label.
// Pads/truncates to 20 chars using only safe alphabet characters
// (Contract #1: no I/O/l/0).
func GenReqID(label string) string {
	const safe = "ABCDEFGHJKMNPQRSTUVW"
	out := make([]byte, 20)
	for i := 0; i < 20; i++ {
		if i < len(label) && isSafeReqIDChar(label[i]) {
			out[i] = label[i]
		} else {
			out[i] = safe[i%len(safe)]
		}
	}
	return string(out)
}

func isSafeReqIDChar(b byte) bool {
	// Mirror substrate.Alphabet check without taking a direct dependency
	// from this file. The alphabet is the canonical 58-char set:
	// ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789
	for _, c := range []byte("ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz123456789") {
		if c == b {
			return true
		}
	}
	return false
}
