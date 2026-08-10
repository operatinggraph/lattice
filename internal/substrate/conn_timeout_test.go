package substrate

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
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

// countingProxy transparently forwards every connection to backend,
// counting how many were accepted — used to prove Connect does or does not
// retry a given failure mode without needing to fake the failure itself
// (the failure comes from the real backend, e.g. a genuine auth rejection).
func countingProxy(t *testing.T, backend string) (url string, attempts func() int64) {
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
			atomic.AddInt64(&count, 1)
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

// TestConnect_DefaultTimeout_AbsorbsHostStall proves Connect's default
// Timeout absorbs a handshake stall that would blow nats.go's bare 2s
// default — the mechanism a component booting on a loaded host depends on.
// The bare-default connect is asserted to fail first (mirroring
// internal/natsfixture_test.go's TestConnectAbsorbsHostStall), so this test
// cannot silently stop proving anything if nats.go's own default ever
// changes.
func TestConnect_DefaultTimeout_AbsorbsHostStall(t *testing.T) {
	t.Parallel()
	const stall = 4 * time.Second
	s := natsfixture.StartServer(t)
	url := stallProxy(t, s.Addr().String(), stall)

	start := time.Now()
	// nats-connect: (reject) this precondition proves nats.go's OWN bare
	// default still fails the stall below, so the test cannot silently stop
	// proving anything if that default ever changes; a hardened budget here
	// would defeat the very comparison this precondition makes.
	bare, err := nats.Connect(url)
	if err == nil {
		bare.Close()
		t.Fatalf("precondition broken: a %v stall no longer blows nats.go's bare default handshake "+
			"deadline (connected in %v) — re-derive the stall duration", stall, time.Since(start))
	}
	if !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatalf("precondition broken: expected the documented handshake-timeout signature, got: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c, err := Connect(ctx, ConnectOpts{URL: url, Name: "stall-absorb-test"})
	if err != nil {
		t.Fatalf("Connect with the default Timeout should absorb a %v stall, got: %v", stall, err)
	}
	t.Cleanup(c.Close)
}

// TestConnect_InitialConnectRetries_RecoversFromReset proves a socket reset
// on the initial dial — the failure mode no Timeout, however generous, can
// absorb — is covered by Connect's own bounded retry.
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

// TestConnect_AuthRejection_DoesNotRetry proves an auth-time rejection is
// not retried: the same credential (here, none — an anonymous connect
// against a server that requires an NKey) is rejected identically on every
// attempt, so retrying would only turn one fast, clear denial into several
// slower ones. Reuses conn_creds_test.go's newUserNKey/
// startEmbeddedNATSWithNKey helpers to produce a real, server-verified
// nats.ErrAuthorization rather than a faked one.
func TestConnect_AuthRejection_DoesNotRetry(t *testing.T) {
	t.Parallel()
	_, pub := newUserNKey(t)
	backend := startEmbeddedNATSWithNKey(t, pub)
	url, attempts := countingProxy(t, strings.TrimPrefix(backend, "nats://"))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	start := time.Now()
	_, err := Connect(ctx, ConnectOpts{URL: url, Name: "auth-rejection-test"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an anonymous Connect against an NKey-required server to be rejected, got nil")
	}
	if !errors.Is(err, nats.ErrAuthorization) {
		t.Fatalf("expected errors.Is(err, nats.ErrAuthorization), got: %v", err)
	}
	if got := attempts(); got != 1 {
		t.Fatalf("an auth rejection must not be retried — expected exactly 1 dial attempt, proxy saw %d", got)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("an auth rejection should fail fast (no retry), took %v", elapsed)
	}
}

// TestConnect_CtxAlreadyDone_SkipsDialEntirely proves an already-cancelled
// ctx stops Connect before it dials even once — a spent budget makes any
// attempt pointless.
func TestConnect_CtxAlreadyDone_SkipsDialEntirely(t *testing.T) {
	t.Parallel()
	s := natsfixture.StartServer(t)
	url, attempts := resetProxy(t, s.Addr().String(), connectAttempts*10)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Connect(ctx, ConnectOpts{URL: url, Name: "cancelled-ctx-test"})
	if err == nil {
		t.Fatal("expected Connect to error against an already-cancelled ctx, got nil")
	}
	if got := attempts(); got != 0 {
		t.Fatalf("an already-cancelled ctx should skip dialing entirely, proxy saw %d attempts", got)
	}
}

// TestConnect_CtxDeadline_StopsRetryingOnceExhausted proves a ctx deadline
// bounds the retry LOOP: once it passes, Connect stops starting new
// attempts rather than running the full connectAttempts regardless — the
// gap a caller with its own tight budget (e.g. cmd/facet's
// reapDurableTimeout) depends on between attempts, even though a single
// attempt already in flight cannot be interrupted early (nats.Connect has
// no context hook).
func TestConnect_CtxDeadline_StopsRetryingOnceExhausted(t *testing.T) {
	t.Parallel()
	s := natsfixture.StartServer(t)
	url, attempts := resetProxy(t, s.Addr().String(), connectAttempts*10)

	// Long enough for a couple of near-instant reset attempts plus backoff,
	// short enough that connectAttempts (4) never all run.
	const budget = 450 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	t.Cleanup(cancel)
	_, err := Connect(ctx, ConnectOpts{URL: url, Name: "ctx-deadline-test"})
	if err == nil {
		t.Fatal("expected Connect to error once ctx's deadline passes, got nil")
	}
	if got := attempts(); got >= connectAttempts {
		t.Fatalf("a %v ctx deadline should stop retrying before all %d attempts run (each reset is near-instant, backoff is %v) — proxy saw %d",
			budget, connectAttempts, connectBackoff, got)
	}
}
