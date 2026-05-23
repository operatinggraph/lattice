// Story 4.7 cleanup — embedded NATS + drive helpers for external test
// packages.
//
// Test packages outside `internal/processor` (e.g.,
// `packages/identity-domain` and `packages/rbac-domain`) need the same
// embedded-NATS fixture + consumer-drive loop that the processor
// integration tests use. They previously lived in
// `internal/processor/integration_test.go` (unexported, _test.go
// scope). Story 4.7 moves the package-scope tests to the packages
// themselves; this file makes the supporting test plumbing reusable.
//
// These helpers are NOT in a _test.go file because external test
// packages can only import non-test files. The helpers are still
// strictly test-only: callers must pass *testing.T.
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
func StartEmbeddedNATS(t *testing.T) string {
	t.Helper()
	opts := natsserver.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	s := natsserver.RunServer(&opts)
	t.Cleanup(func() {
		if jsCfg := s.JetStreamConfig(); jsCfg != nil {
			defer os.RemoveAll(jsCfg.StoreDir)
		}
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
