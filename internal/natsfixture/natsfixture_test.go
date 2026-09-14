package natsfixture_test

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
)

// TestServerIsUsable proves the fixture returns a live JetStream-capable server
// on a loopback port the kernel assigned.
func TestServerIsUsable(t *testing.T) {
	s, nc := natsfixture.Server(t)

	require.True(t, nc.IsConnected())
	addr, ok := s.Addr().(*net.TCPAddr)
	require.True(t, ok, "embedded server should listen on TCP")
	require.True(t, addr.Port >= 1024, "kernel-assigned port, got %d", addr.Port)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	_, err = js.CreateKeyValue(t.Context(), jetstream.KeyValueConfig{Bucket: "natsfixture-selftest"})
	require.NoError(t, err)
}

// TestDistinctServersGetDistinctPorts guards the RANDOM_PORT invariant that lets
// packages run concurrently under `go test -p N`.
func TestDistinctServersGetDistinctPorts(t *testing.T) {
	a := natsfixture.StartServer(t)
	b := natsfixture.StartServer(t)
	require.NotEqual(t, a.Addr().(*net.TCPAddr).Port, b.Addr().(*net.TCPAddr).Port)
}

// TestRestartableServerKeepsItsStore is the self-test for the recovery seam: a
// stream written on the first server is recovered by the second, which proves
// the store outlived the server rather than being reallocated, and the new
// server is reached at a NEW client URL — the fact that makes any connection
// held across the Stop dead.
func TestRestartableServerKeepsItsStore(t *testing.T) {
	r := natsfixture.StartRestartableServer(t)
	firstURL := r.Server().ClientURL()

	kv, err := jetstream.New(natsfixture.Connect(t, firstURL))
	require.NoError(t, err)
	bucket, err := kv.CreateKeyValue(t.Context(), jetstream.KeyValueConfig{Bucket: "natsfixture-restart"})
	require.NoError(t, err)
	_, err = bucket.Put(t.Context(), "survives", []byte("across the restart"))
	require.NoError(t, err)

	r.Stop()
	second := r.Start()
	require.NotEqual(t, firstURL, second.ClientURL(),
		"a restarted server gets a kernel-assigned port of its own, so the old URL is stale")

	recovered, err := jetstream.New(natsfixture.Connect(t, second.ClientURL()))
	require.NoError(t, err)
	after, err := recovered.KeyValue(t.Context(), "natsfixture-restart")
	require.NoError(t, err, "the second server must recover the first server's store")
	entry, err := after.Get(t.Context(), "survives")
	require.NoError(t, err)
	require.Equal(t, "across the restart", string(entry.Value()))
}

// stallProxy forwards to backend but withholds the server->client direction for
// stall, standing in for a host that denied the handshake CPU.
func stallProxy(t *testing.T, backend string, stall time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			cc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer cc.Close()
				sc, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer sc.Close()
				go func() { _, _ = io.Copy(sc, cc) }()
				time.Sleep(stall)
				_, _ = io.Copy(cc, sc)
			}()
		}
	}()
	return "nats://" + ln.Addr().String()
}

// TestConnectAbsorbsHostStall is the regression proof for this package's whole
// reason to exist: a handshake stall that blows nats.go's 2s default — the
// documented `read tcp ...: i/o timeout` signature — is absorbed by
// natsfixture.Connect. The bare-default connect is asserted to fail first, so this
// test cannot silently stop proving anything if the default ever changes.
func TestConnectAbsorbsHostStall(t *testing.T) {
	const stall = 4 * time.Second
	s := natsfixture.StartServer(t)
	url := stallProxy(t, s.Addr().String(), stall)

	// A bare connect gets one 2s window and no retry.
	start := time.Now()
	bare, err := nats.Connect(url)
	if err == nil {
		bare.Close()
		t.Fatalf("precondition broken: a %v stall no longer blows the nats.go default handshake "+
			"deadline (connected in %v) — re-derive the timeout budget in natsfixture", stall, time.Since(start))
	}
	require.Contains(t, err.Error(), "i/o timeout", "expected the documented handshake signature")

	// The hardened fixture connect absorbs the same stall.
	nc := natsfixture.Connect(t, url)
	require.True(t, nc.IsConnected())
}

// TestConnectRespectsCallerOptions proves caller options are applied after the
// defaults, so a test can still pin its own timeout when that is the point.
func TestConnectRespectsCallerOptions(t *testing.T) {
	s := natsfixture.StartServer(t)
	nc := natsfixture.Connect(t, s.ClientURL(), nats.Name("caller-named"))
	require.True(t, nc.IsConnected())
	require.Equal(t, "caller-named", nc.Opts.Name)
}

// TestCleanupFullyStopsServer proves teardown waits for the server to finish
// shutting down rather than leaving it draining into the next test.
func TestCleanupFullyStopsServer(t *testing.T) {
	var addr string
	t.Run("inner", func(t *testing.T) {
		s := natsfixture.StartServer(t)
		addr = s.Addr().String()
	})
	// The inner subtest's cleanup has run; the port must no longer serve NATS.
	_, err := nats.Connect("nats://"+addr, nats.Timeout(500*time.Millisecond), nats.RetryOnFailedConnect(false))
	require.Error(t, err, "server should be fully down after cleanup")
	require.False(t, strings.Contains(err.Error(), "i/o timeout"),
		"a fully-stopped server refuses the dial; a hang would mean Shutdown returned while still draining")
}
