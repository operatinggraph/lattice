// Package natstest provides the canonical embedded NATS + JetStream fixture for
// Lattice tests: one server per caller, on an OS-assigned loopback port, with a
// private JetStream store, a connected client, and complete teardown.
//
// # Why this package exists
//
// The fixture used to be copy-pasted into every test file that needed a server.
// Each copy inherited nats.go's DEFAULT connect options, and that default is a
// trap on a loaded machine: processConnectInit pins the WHOLE initial handshake
// (INFO read + CONNECT/PING + PONG) to Opts.Timeout — 2 seconds — with a single
// conn.SetDeadline, and an initial connect is NOT retried. So a fixture connect
// gets exactly one 2-second window, and any host stall longer than that which
// lands inside it fails the test with
//
//	read tcp 127.0.0.1:<client>-><server>: i/o timeout
//
// in whatever package happened to be connecting at that moment — routinely a
// package the author never touched. On a memory-pressured box (swap thrashing,
// a full Docker stack alongside `go test ./... -p 4`) a multi-second stall is a
// normal tail event, while the vulnerable window is only about a millisecond
// wide per fixture. Across the thousands of fixture connects in a full suite run
// that adds up to a rare-but-recurring failure that costs a triage cycle every
// time, because the symptom is indistinguishable from a real bug at a glance.
//
// This package closes that gap by giving the handshake a generous explicit
// budget and a bounded retry, and by naming the signature in the failure message
// so a triaging reader does not have to rediscover it.
//
// # What this does NOT do
//
// Nothing here relaxes a gate, and nothing here retries a test.
//
// The hardening applies ONLY to bringing up a server the test itself starts and
// obtaining a connection to it — setup, never a proposition under test. If the
// server truly cannot start or serve, the test still fails, just later and with
// a clearer message. No assertion, no poll for an eventually-consistent result,
// and no operation whose outcome is being asserted is retried or loosened by
// this package. Do not generalise these retries into "retry the flaky test":
// a retry around an assertion hides real bugs, which is a different thing
// entirely from tolerating a stalled TCP handshake to a server we just booted.
package natstest

import (
	"fmt"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/jsstore"
)

const (
	// readyTimeout bounds how long we wait for the embedded server's accept
	// loop. Generous because a stalled host delays startup as readily as it
	// delays a handshake; a server that never comes up still fails the test.
	readyTimeout = 20 * time.Second

	// connectTimeout is the per-attempt handshake budget, replacing nats.go's
	// 2s default. Sized to swallow a host stall, not to hide a broken server.
	connectTimeout = 20 * time.Second

	// connectAttempts bounds the retry. A long timeout covers a stall that
	// delays the handshake; a retry covers a stall that kills the socket
	// outright (reset/EOF), which no timeout can absorb.
	connectAttempts = 4

	connectBackoff = 250 * time.Millisecond
)

// Options returns the canonical embedded-server options: JetStream on, a
// private file store that outlives Shutdown safely (see internal/jsstore), no
// logging, no signal handlers, and an OS-assigned port.
//
// Port MUST stay RANDOM_PORT: it resolves to a bind on :0, so the kernel — not
// the process — picks a free port, which is what keeps concurrent test packages
// from colliding under `go test -p N`.
func Options(t *testing.T) *natsserver.Options {
	t.Helper()
	return &natsserver.Options{
		JetStream: true,
		StoreDir:  jsstore.Dir(t),
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
}

// StartServer starts an embedded NATS server with JetStream and registers its
// teardown. Use it when the test needs to control its own client options;
// otherwise prefer Server.
func StartServer(t *testing.T) *natsserver.Server {
	t.Helper()
	return StartServerWith(t, Options(t))
}

// StartServerWith starts an embedded server from caller-supplied options,
// registering the same teardown. Callers that need a non-default option (a
// custom auth block, say) should start from Options(t) and amend it, so the
// port and store-dir invariants above are preserved.
func StartServerWith(t *testing.T, opts *natsserver.Options) *natsserver.Server {
	t.Helper()
	s, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("natstest: new embedded NATS server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(readyTimeout) {
		s.Shutdown()
		t.Fatalf("natstest: embedded NATS server not ready for connections within %v", readyTimeout)
	}
	// Shutdown only kicks out the accept loop and starts closing clients;
	// WaitForShutdown is the documented barrier for it having finished. Waiting
	// keeps a dying server from overlapping the next test's server.
	t.Cleanup(func() {
		s.Shutdown()
		s.WaitForShutdown()
	})
	return s
}

// Server starts an embedded NATS server with JetStream and returns it together
// with a connected client. Both are torn down via t.Cleanup.
func Server(t *testing.T) (*natsserver.Server, *nats.Conn) {
	t.Helper()
	s := StartServer(t)
	return s, Connect(t, s.ClientURL())
}

// Connect dials url with a stall-tolerant handshake budget and a bounded retry,
// closing the connection via t.Cleanup. Extra options are appended after the
// defaults, so a caller can override the timeout deliberately.
func Connect(t *testing.T, url string, opts ...nats.Option) *nats.Conn {
	t.Helper()
	nc, err := connect(url, opts...)
	if err != nil {
		t.Fatal(handshakeFailure(connectAttempts, err))
	}
	t.Cleanup(nc.Close)
	return nc
}

func connect(url string, opts ...nats.Option) (*nats.Conn, error) {
	all := append([]nats.Option{nats.Timeout(connectTimeout)}, opts...)
	var err error
	for attempt := 1; ; attempt++ {
		var nc *nats.Conn
		if nc, err = nats.Connect(url, all...); err == nil {
			return nc, nil
		}
		if attempt == connectAttempts {
			return nil, err
		}
		time.Sleep(connectBackoff)
	}
}

// handshakeFailure names the signature so a reader triaging a suite failure can
// classify it in one glance instead of re-deriving it.
func handshakeFailure(attempts int, err error) string {
	return fmt.Sprintf(`natstest: could not connect to the embedded NATS server after %d attempts (%v each): %v

This is the embedded-NATS FIXTURE HANDSHAKE signature, not a defect in the code under test.
The fixture dials a server this very test just started, over loopback. A failure here means the
host denied that handshake CPU for tens of seconds (memory pressure/swap, a saturated box, a full
Docker stack running alongside `+"`go test ./... -p 4`"+`) — it does not mean the package's logic is wrong.

Triage: re-run THIS package alone (`+"`go test ./<pkg>/ -count=1`"+`). If it passes, the failure was host
contention. Check `+"`vm_stat`"+` / `+"`sysctl vm.swapusage`"+` for pressure before suspecting code.
Do NOT loosen an assertion or add a retry around one to make a suite run go green.`,
		attempts, connectTimeout, err)
}
