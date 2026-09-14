// Package natsfixture provides the canonical embedded NATS + JetStream fixture for
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
// # Restarting a server on its own store
//
// A server from Options/StartServer/Server gets a private JetStream file store
// that is born and dies with it, so such a test only ever observes a server that
// has been up since its store was empty. Some server behaviour is only reachable
// the other way round: JetStream does work at RECOVERY that it does not do while
// running — rebuilding TTL state off the store, then acting on deadlines that
// fell due while nothing was serving. RestartableServer (see
// StartRestartableServer) is the seam for that: one store directory, owned for
// the whole test, which a server can be brought down from and a fresh server
// brought back up on, so a test can observe what recovery itself does.
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
//
// A restart is held to the same line. It belongs in a test whose PROPOSITION is
// about recovery — the store surviving the server is the thing being asserted
// about. It is never a way to give a failing assertion another go: cycling the
// server under a flaky expectation hides the same real bugs a retry does, and
// costs a process restart to do it.
package natsfixture

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

// Options returns the canonical embedded-server options: loopback-only,
// JetStream on, a private file store that outlives Shutdown safely (see
// internal/jsstore), no logging, no signal handlers, and an OS-assigned port.
//
// Port MUST stay RANDOM_PORT: it resolves to a bind on :0, so the kernel — not
// the process — picks a free port, which is what keeps concurrent test packages
// from colliding under `go test -p N`.
//
// Host is pinned to loopback so a test server is never reachable off-box and
// never trips the macOS firewall prompt.
func Options(t testing.TB) *natsserver.Options {
	t.Helper()
	return optionsOn(jsstore.Dir(t))
}

// optionsOn is the canonical option set over an already-allocated JetStream
// store directory, so a store can be handed to more than one server in turn.
func optionsOn(storeDir string) *natsserver.Options {
	return &natsserver.Options{
		Host:      "127.0.0.1",
		JetStream: true,
		StoreDir:  storeDir,
		NoLog:     true,
		NoSigs:    true,
		Port:      natsserver.RANDOM_PORT,
	}
}

// StartServer starts an embedded NATS server with JetStream and registers its
// teardown. Use it when the test needs to control its own client options;
// otherwise prefer Server.
func StartServer(t testing.TB) *natsserver.Server {
	t.Helper()
	return StartServerWith(t, Options(t))
}

// StartServerWith starts an embedded server from caller-supplied options,
// registering the same teardown. Callers that need a non-default option (a
// custom auth block, say) should start from Options(t) and amend it, so the
// port and store-dir invariants above are preserved.
func StartServerWith(t testing.TB, opts *natsserver.Options) *natsserver.Server {
	t.Helper()
	s, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("natsfixture: new embedded NATS server: %v", err)
	}
	go s.Start()
	if !s.ReadyForConnections(readyTimeout) {
		s.Shutdown()
		t.Fatalf("natsfixture: embedded NATS server not ready for connections within %v", readyTimeout)
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
func Server(t testing.TB) (*natsserver.Server, *nats.Conn) {
	t.Helper()
	s := StartServer(t)
	return s, Connect(t, s.ClientURL())
}

// RestartableServer is an embedded server together with the JetStream store
// directory it runs on, where the store belongs to the TEST rather than to any
// one server: the running server can be stopped, time can pass with nothing
// serving, and a fresh server can be started on the same store. That makes
// JetStream's recovery path observable — TTL state rebuilt from disk, deadlines
// that fell due while nothing was running acted on after the fact — which no
// single-server fixture can reach.
//
// A restarted server is a DIFFERENT server with a DIFFERENT client URL, because
// the port invariant is RANDOM_PORT and the kernel picks again. Every *nats.Conn
// held across a Stop is therefore dead, and dialling the old URL is at best a
// connection to nothing: take the server Start returns and obtain a fresh
// connection from Connect.
//
// It is scoped to tests whose subject is recovery. It is not a way to re-run an
// assertion that failed; see this package's "What this does NOT do".
type RestartableServer struct {
	t        testing.TB
	storeDir string
	running  *natsserver.Server
}

// StartRestartableServer allocates a JetStream store directory that outlives any
// single server, starts a server on it, and returns the handle. Teardown of each
// server and of the store is registered via t.Cleanup, the store's removal last
// (jsstore.Dir absorbs JetStream's post-shutdown flush), so a caller never has a
// removal path of its own to run.
func StartRestartableServer(t testing.TB) *RestartableServer {
	t.Helper()
	r := &RestartableServer{t: t, storeDir: jsstore.Dir(t)}
	r.Start()
	return r
}

// Server returns the server currently running on the store, or nil between a
// Stop and the next Start.
func (r *RestartableServer) Server() *natsserver.Server {
	return r.running
}

// StoreDir returns the JetStream store directory shared by every server this
// handle starts.
func (r *RestartableServer) StoreDir() string {
	return r.storeDir
}

// Stop brings the running server down and does not return until it is fully
// down, so nothing of it overlaps the next server on the same store. Connections
// to it are finished at that point. Calling it with nothing running is a no-op.
func (r *RestartableServer) Stop() {
	r.t.Helper()
	if r.running == nil {
		return
	}
	// Shutdown only kicks out the accept loop and starts closing clients;
	// WaitForShutdown is the documented barrier for it having finished, and
	// here it is also the barrier for the store's file locks being released.
	r.running.Shutdown()
	r.running.WaitForShutdown()
	r.running = nil
}

// Start boots a server on the store and returns it. The returned server is the
// only valid source of a client URL from here on: it listens on a freshly
// assigned port, so a connection made before the preceding Stop cannot be
// carried over and must be replaced via Connect.
//
// Two servers must never hold the same store at once, so starting one while
// another is running fails the test rather than corrupting the store.
func (r *RestartableServer) Start() *natsserver.Server {
	r.t.Helper()
	if r.running != nil {
		r.t.Fatalf("natsfixture: a server is already running on store %s — Stop it before starting another", r.storeDir)
	}
	r.running = StartServerWith(r.t, optionsOn(r.storeDir))
	return r.running
}

// Connect dials url with a stall-tolerant handshake budget and a bounded retry,
// closing the connection via t.Cleanup. Extra options are appended after the
// defaults, so a caller can override the timeout deliberately.
func Connect(t testing.TB, url string, opts ...nats.Option) *nats.Conn {
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
	return fmt.Sprintf(`natsfixture: could not connect to the embedded NATS server after %d attempts (%v each): %v

This is the embedded-NATS FIXTURE HANDSHAKE signature, not a defect in the code under test.
The fixture dials a server this very test just started, over loopback. A failure here means the
host denied that handshake CPU for tens of seconds (memory pressure/swap, a saturated box, a full
Docker stack running alongside `+"`go test ./... -p 4`"+`) — it does not mean the package's logic is wrong.

Triage: re-run THIS package alone (`+"`go test ./<pkg>/ -count=1`"+`). If it passes, the failure was host
contention. Check `+"`vm_stat`"+` / `+"`sysctl vm.swapusage`"+` for pressure before suspecting code.
Do NOT loosen an assertion or add a retry around one to make a suite run go green.`,
		attempts, connectTimeout, err)
}
