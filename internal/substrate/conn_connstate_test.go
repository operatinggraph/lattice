package substrate

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/operatinggraph/lattice/internal/natsfixture"
)

// severingProxy forwards to backend and can, on demand, sever every live
// connection and refuse new ones until restored — a connection loss whose
// length the test controls. Same shape as this file's sibling helpers
// (stallProxy/resetProxy in conn_timeout_test.go), standing in for a
// different host-level fault: not a slow or reset handshake, but an
// established connection going away underneath a running component.
type severingProxy struct {
	url string

	mu      sync.Mutex
	live    []net.Conn
	blocked bool
}

func newSeveringProxy(t *testing.T, backend string) *severingProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	p := &severingProxy{url: "nats://" + ln.Addr().String()}
	go func() {
		for {
			cc, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			blocked := p.blocked
			p.mu.Unlock()
			if blocked {
				_ = cc.Close()
				continue
			}
			sc, err := net.Dial("tcp", backend)
			if err != nil {
				_ = cc.Close()
				continue
			}
			p.mu.Lock()
			p.live = append(p.live, cc, sc)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(sc, cc); _ = sc.Close() }()
			go func() { _, _ = io.Copy(cc, sc); _ = cc.Close() }()
		}
	}()
	return p
}

func (p *severingProxy) sever() {
	p.mu.Lock()
	live := p.live
	p.live = nil
	p.blocked = true
	p.mu.Unlock()
	for _, c := range live {
		_ = c.Close()
	}
}

func (p *severingProxy) restore() {
	p.mu.Lock()
	p.blocked = false
	p.mu.Unlock()
}

// TestOnConnectionStateChange_EveryListenerSeesBothEdges is the whole reason
// the fan-out exists rather than each component setting nats.go's handler
// itself. SetDisconnectErrHandler/SetReconnectHandler are SINGLE slots on the
// *nats.Conn, and every Lattice binary shares one *Conn across its
// components — so a second registrant would silently unregister the first,
// and the component that lost the slot would keep serving as though nothing
// had happened. Two listeners registered here must BOTH see the loss and both
// see the restoration, in that order.
func TestOnConnectionStateChange_EveryListenerSeesBothEdges(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	proxy := newSeveringProxy(t, s.Addr().String())
	conn, err := Connect(ctx, ConnectOpts{
		URL:           proxy.url,
		MaxReconnects: -1,
		ReconnectWait: 25 * time.Millisecond,
	})
	require.NoError(t, err)
	defer conn.Close()
	require.True(t, conn.Connected())

	first := make(chan bool, 8)
	second := make(chan bool, 8)
	conn.OnConnectionStateChange(func(connected bool) { first <- connected })
	conn.OnConnectionStateChange(func(connected bool) { second <- connected })

	proxy.sever()
	for name, ch := range map[string]chan bool{"first": first, "second": second} {
		select {
		case connected := <-ch:
			require.False(t, connected, "%s listener: the loss edge must arrive as false", name)
		case <-ctx.Done():
			t.Fatalf("%s listener never saw the connection loss", name)
		}
	}
	require.False(t, conn.Connected(), "Connected must track the same fact the edge reports")

	proxy.restore()
	for name, ch := range map[string]chan bool{"first": first, "second": second} {
		select {
		case connected := <-ch:
			require.True(t, connected, "%s listener: the restoration edge must arrive as true", name)
		case <-ctx.Done():
			t.Fatalf("%s listener never saw the reconnect", name)
		}
	}
	require.Eventually(t, conn.Connected, 20*time.Second, 10*time.Millisecond)
}

// TestOnConnectionStateChange_CloseIsNotADisconnect pins the other boundary,
// and it is not the free one it looks like. nats.go's Conn.Close calls
// close(CLOSED, !Opts.NoCallbacksAfterClientClose, nil); that option defaults
// false and substrate never sets it, so a deliberate shutdown pushes the SAME
// DisconnectedErrCB an outage does.
//
// A listener cannot tell those apart from inside the callback, and guessing
// wrong is paid at exactly the worst moment: in cmd/refractor the deferred
// conn.Close runs BEFORE the deferred context cancel (LIFO), so a teardown
// would fire a full corpus re-derivation — activations, retry budgets and all
// — against a connection that can no longer carry one request. Close
// therefore latches the fan-out off before closing.
func TestOnConnectionStateChange_CloseIsNotADisconnect(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := Connect(ctx, ConnectOpts{URL: s.ClientURL()})
	require.NoError(t, err)

	edges := make(chan bool, 4)
	conn.OnConnectionStateChange(func(connected bool) { edges <- connected })

	conn.Close()

	require.Never(t, func() bool { return len(edges) > 0 }, 500*time.Millisecond, 10*time.Millisecond,
		"Close is a shutdown, not a connection loss — a listener told otherwise degrades against a "+
			"connection that is already gone, during teardown")
	require.False(t, conn.Connected())
}

// TestOnConnectionStateChange_NoEdgeForTheInitialConnect pins the boundary of
// what the signal reports: a caller holding a *Conn is by construction already
// connected, so an initial-connect edge would be a phantom event a listener
// would have to filter out — and a listener that armed itself on a `true` it
// had not earned is exactly the failure the signal exists to prevent.
func TestOnConnectionStateChange_NoEdgeForTheInitialConnect(t *testing.T) {
	s := natsfixture.StartServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := Connect(ctx, ConnectOpts{URL: s.ClientURL()})
	require.NoError(t, err)
	defer conn.Close()

	edges := make(chan bool, 4)
	conn.OnConnectionStateChange(func(connected bool) { edges <- connected })

	require.Never(t, func() bool { return len(edges) > 0 }, 300*time.Millisecond, 10*time.Millisecond,
		"a healthy, never-interrupted connection must report no state edges at all")
}
