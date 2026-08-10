package substrate

import (
	"context"
	"testing"

	nats "github.com/nats-io/nats.go"
)

// TestConnect_MaxReconnectsOverride_Threaded proves a nonzero
// ConnectOpts.MaxReconnects actually reaches the underlying *nats.Conn's
// options rather than being silently dropped — the field cmd/refractor's
// two-hour outage traced back to (internal/substrate/conn.go's `if
// opts.MaxReconnects != 0` guard). Covers both directions a caller declares:
// -1 (a daemon's "reconnect forever") and a small positive value (a bounded
// CLI's explicit "fail fast").
func TestConnect_MaxReconnectsOverride_Threaded(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)

	for _, want := range []int{-1, 1, 5} {
		c, err := Connect(context.Background(), ConnectOpts{URL: url, MaxReconnects: want})
		if err != nil {
			t.Fatalf("Connect(MaxReconnects: %d): %v", want, err)
		}
		if got := c.NATS().Opts.MaxReconnect; got != want {
			t.Errorf("Connect(MaxReconnects: %d): underlying nats.Conn.Opts.MaxReconnect = %d, want %d", want, got, want)
		}
		c.Close()
	}
}

// TestConnect_MaxReconnectsZero_InheritsNatsDefault pins the sentinel
// behavior every cmd/** call site must now declare around (the reason
// lint-conventions.go's max-reconnects gate exists): ConnectOpts.MaxReconnects
// left at its Go zero value is indistinguishable from "never mentioned", so
// Connect does not pass nats.MaxReconnects(0) at all and the resulting
// connection inherits nats.go's OWN default (nats.DefaultMaxReconnect, 60
// attempts) instead of "no reconnects". A caller cannot spell "0 reconnects"
// through ConnectOpts today — the smallest value it can actually express is 1
// — which is why cmd/lattice-pkg, cmd/lattice's CLI, and cmd/facet's
// reapSyncDurable declare 1 rather than 0 for their fail-fast connections.
func TestConnect_MaxReconnectsZero_InheritsNatsDefault(t *testing.T) {
	t.Parallel()
	url := startEmbeddedNATS(t)

	c, err := Connect(context.Background(), ConnectOpts{URL: url})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Close()
	if got := c.NATS().Opts.MaxReconnect; got != nats.DefaultMaxReconnect {
		t.Errorf("Connect with ConnectOpts.MaxReconnects unset: underlying nats.Conn.Opts.MaxReconnect = %d, want nats.DefaultMaxReconnect (%d)",
			got, nats.DefaultMaxReconnect)
	}
}
