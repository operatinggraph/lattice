package substrate

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"

	"github.com/operatinggraph/lattice/internal/natsfixture"
)

// stallProxy forwards to backend but withholds the server->client direction
// for stall, standing in for a host that denied the handshake CPU. Mirrors
// internal/natsfixture_test.go's helper of the same name/shape.
func stallProxy(t *testing.T, backend string, stall time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
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

// resetProxy accepts connections to backend, closing the socket outright
// (no data at all) for the first failUntil of them — the "stall that kills
// the socket outright (reset/EOF), which no timeout can absorb" natsfixture's
// package doc names as retry's reason to exist — then proxying every later
// connection through normally. The returned func reports how many
// connections it has accepted so far (safe to poll from the test goroutine).
func resetProxy(t *testing.T, backend string, failUntil int) (url string, attempts func() int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var count int64
	go func() {
		for {
			cc, err := ln.Accept()
			if err != nil {
				return
			}
			n := atomic.AddInt64(&count, 1)
			if n <= int64(failUntil) {
				cc.Close()
				continue
			}
			go func() {
				defer cc.Close()
				sc, err := net.Dial("tcp", backend)
				if err != nil {
					return
				}
				defer sc.Close()
				go func() { _, _ = io.Copy(sc, cc) }()
				_, _ = io.Copy(cc, sc)
			}()
		}
	}()
	return "nats://" + ln.Addr().String(), func() int64 { return atomic.LoadInt64(&count) }
}

// TestConnect_DefaultTimeout_AbsorbsHostStall is the regression proof for
// this fix: a handshake stall that blows nats.go's bare 2s default is
// absorbed by Connect's own default Timeout. The bare-default connect is
// asserted to fail first (mirroring internal/natsfixture_test.go's
// TestConnectAbsorbsHostStall), so this test cannot silently stop proving
// anything if nats.go's default ever changes.
func TestConnect_DefaultTimeout_AbsorbsHostStall(t *testing.T) {
	t.Parallel()
	const stall = 4 * time.Second
	s := natsfixture.StartServer(t)
	url := stallProxy(t, s.Addr().String(), stall)

	start := time.Now()
	bare, err := nats.Connect(url)
	if err == nil {
		bare.Close()
		t.Fatalf("precondition broken: a %v stall no longer blows nats.go's bare default handshake "+
			"deadline (connected in %v) — re-derive the stall duration", stall, time.Since(start))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "stall-absorb-test"})
	if err != nil {
		t.Fatalf("Connect with the default Timeout should absorb a %v stall, got: %v", stall, err)
	}
	t.Cleanup(c.Close)
}

// TestConnect_InitialConnectRetries_RecoversFromReset proves the other half
// of the fix: a socket reset on the initial dial — the failure mode a
// Timeout cannot absorb — is covered by Connect's own bounded retry.
func TestConnect_InitialConnectRetries_RecoversFromReset(t *testing.T) {
	t.Parallel()
	s := natsfixture.StartServer(t)
	url, attempts := resetProxy(t, s.Addr().String(), connectAttempts-1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "retry-recovery-test"})
	if err != nil {
		t.Fatalf("Connect should retry past a reset socket and succeed on the final attempt, got: %v", err)
	}
	t.Cleanup(c.Close)
	if got := attempts(); got < connectAttempts {
		t.Fatalf("expected %d dial attempts (the first %d reset, the last succeeded), proxy saw %d",
			connectAttempts, connectAttempts-1, got)
	}
}

// TestConnect_ExhaustsBoundedRetries_StillErrors proves the retry is
// bounded, not infinite: a socket that always resets still fails Connect,
// promptly, after exactly connectAttempts dials.
func TestConnect_ExhaustsBoundedRetries_StillErrors(t *testing.T) {
	t.Parallel()
	s := natsfixture.StartServer(t)
	url, attempts := resetProxy(t, s.Addr().String(), connectAttempts*10)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	start := time.Now()
	_, err := Connect(ctx, ConnectOpts{URL: url, Name: "exhaustion-test"})
	if err == nil {
		t.Fatal("expected Connect to error once its bounded retry is exhausted, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("bounded retry against an always-reset socket should fail fast, took %v", elapsed)
	}
	if got := attempts(); got != connectAttempts {
		t.Fatalf("expected exactly connectAttempts=%d dial attempts, proxy saw %d", connectAttempts, got)
	}
}

// TestConnect_TimeoutOverride_Respected proves ConnectOpts.Timeout actually
// reaches the dial rather than being silently dropped in favor of either
// default (substrate's generous one, or nats.go's bare 2s) — mirroring
// internal/natsfixture_test.go's TestConnectRespectsCallerOptions.
func TestConnect_TimeoutOverride_Respected(t *testing.T) {
	t.Parallel()
	const stall = 3 * time.Second
	const shortTimeout = 500 * time.Millisecond
	s := natsfixture.StartServer(t)
	url := stallProxy(t, s.Addr().String(), stall)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	start := time.Now()
	_, err := Connect(ctx, ConnectOpts{URL: url, Timeout: shortTimeout})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("a %v Timeout should not survive a %v stall — the override was not applied", shortTimeout, stall)
	}
	maxExpected := time.Duration(connectAttempts)*shortTimeout + time.Duration(connectAttempts-1)*connectBackoff + 3*time.Second
	if elapsed > maxExpected {
		t.Fatalf("Connect with a %v Timeout took %v to fail — want well under the %v default's absorption of the stall (max expected %v)",
			shortTimeout, elapsed, defaultConnectTimeout, maxExpected)
	}
}
