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

	"github.com/operatinggraph/lattice/internal/jsstore"
	"github.com/operatinggraph/lattice/internal/processor"
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
	opts.StoreDir = jsstore.Dir(t)
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

// driveFetchWait bounds a single synchronous pull. It is a safety ceiling, not
// a timing assumption: every caller publishes its op (waiting for the PubAck)
// before driving, so the message is already durable in the stream and a healthy
// fetch returns the instant the server responds. The ceiling is generous to
// absorb CI full-suite contention (many test packages, each with its own
// embedded server); hitting it means a real defect, not a slow runner.
const driveFetchWait = 30 * time.Second

// DriveOne pulls exactly one message from `cons`, runs it through `cp`, asserts
// the outcome matches `want` (if non-empty), and returns the observed outcome.
// Mirrors the processor-internal `driveOne` helper.
//
// It uses synchronous pull (Fetch) rather than push (Consume) deliberately.
// Consume holds a long-lived background subscription whose Stop is asynchronous:
// when a test drives one delivery and then another on the SAME durable consumer,
// the first Stop's teardown can still be in flight when the second op is
// published, so the abandoned callback steals and acks/terminates that op while
// the live drive blocks until its deadline. Fetch carries no background
// machinery — each call is self-contained, so repeated sequential drives on one
// consumer cannot race the teardown.
func DriveOne(t *testing.T, ctx context.Context, cp *processor.CommitPath, cons jetstream.Consumer, want processor.MessageOutcome) processor.MessageOutcome {
	t.Helper()
	batch, err := cons.Fetch(1, jetstream.FetchMaxWait(driveFetchWait))
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var (
		outcome processor.MessageOutcome
		got     bool
	)
	for m := range batch.Messages() {
		outcome = cp.HandleMessage(ctx, m)
		got = true
	}
	if err := batch.Error(); err != nil {
		t.Fatalf("Fetch batch error: %v", err)
	}
	if !got {
		t.Fatalf("no message delivered within %s (want outcome %q)", driveFetchWait, want)
	}
	if want != "" && outcome != want {
		t.Fatalf("outcome mismatch: got %q want %q", outcome, want)
	}
	return outcome
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
