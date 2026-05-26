package substrate

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ConnectOpts configures the NATS connection substrate establishes on
// behalf of callers. Only the URL is required; the remaining fields have
// sensible defaults for Lattice components.
//
// substrate intentionally exposes a small surface — callers who need more
// nats.Option flexibility should construct their own nats.Conn and pass it
// to Wrap.
type ConnectOpts struct {
	// URL is the NATS server URL (e.g. "nats://localhost:4222").
	URL string
	// Name is the connection name reported to NATS (helpful for debugging
	// component identity in NATS monitoring).
	Name string
	// MaxReconnects controls the reconnect retry budget. Zero means
	// "use the nats.go default". Set to -1 for unlimited.
	MaxReconnects int
	// ReconnectWait controls the delay between reconnect attempts.
	// Zero means "use the nats.go default".
	ReconnectWait time.Duration
}

// Conn is substrate's opinionated NATS handle. It owns the underlying
// nats.Conn and jetstream.JetStream context and lazily caches
// jetstream.KeyValue handles per bucket.
//
// Callers obtain KV operations via the package-level KV* helpers (e.g.
// KVGet, KVPut). The internal layering is hidden so that downstream
// stories can switch transports or wrappers without touching call sites.
type Conn struct {
	nc *nats.Conn
	js jetstream.JetStream

	mu      sync.Mutex
	buckets map[string]jetstream.KeyValue
}

// Connect establishes a new NATS + JetStream connection using opts.
// The returned *Conn must be closed with Close when no longer needed.
func Connect(ctx context.Context, opts ConnectOpts) (*Conn, error) {
	if opts.URL == "" {
		opts.URL = nats.DefaultURL
	}
	natsOpts := []nats.Option{}
	if opts.Name != "" {
		natsOpts = append(natsOpts, nats.Name(opts.Name))
	}
	if opts.MaxReconnects != 0 {
		natsOpts = append(natsOpts, nats.MaxReconnects(opts.MaxReconnects))
	}
	if opts.ReconnectWait > 0 {
		natsOpts = append(natsOpts, nats.ReconnectWait(opts.ReconnectWait))
	}
	nc, err := nats.Connect(opts.URL, natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("substrate: nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("substrate: jetstream context: %w", err)
	}
	// nats.Connect does not accept a context; context-based cancellation of the
	// initial dial is not possible via the nats.go API. Callers that need to bound
	// connection time should set ConnectOpts.MaxReconnects and ReconnectWait to
	// limit the retry loop rather than relying on context deadlines.
	_ = ctx
	return &Conn{nc: nc, js: js, buckets: make(map[string]jetstream.KeyValue)}, nil
}

// Wrap adapts an existing *nats.Conn into a substrate *Conn. Useful when
// callers need custom nats.Options beyond ConnectOpts.
func Wrap(nc *nats.Conn) (*Conn, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("substrate: jetstream context: %w", err)
	}
	return &Conn{nc: nc, js: js, buckets: make(map[string]jetstream.KeyValue)}, nil
}

// NATS returns the underlying *nats.Conn. Provided as an escape hatch for
// operations substrate does not yet wrap (subscription, raw JetStream).
// Callers should prefer the typed helpers when one exists.
func (c *Conn) NATS() *nats.Conn { return c.nc }

// JetStream returns the underlying jetstream.JetStream context. Escape
// hatch — prefer the typed helpers.
func (c *Conn) JetStream() jetstream.JetStream { return c.js }

// Close shuts down the connection. Safe to call multiple times.
func (c *Conn) Close() {
	if c.nc != nil {
		c.nc.Close()
	}
}

// bucket returns a cached jetstream.KeyValue handle for the named bucket.
// On the first call per bucket the handle is opened (not created); the
// bucket must already exist (provision via the bootstrap path).
//
// The lock is held across the KeyValue() open call to prevent a TOCTOU
// race where two concurrent first-callers both pass the cache miss check
// and open duplicate handles. Lock contention is negligible because buckets
// are opened once per process.
func (c *Conn) bucket(ctx context.Context, name string) (jetstream.KeyValue, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if kv, ok := c.buckets[name]; ok {
		return kv, nil
	}
	kv, err := c.js.KeyValue(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("substrate: open KV bucket %q: %w", name, err)
	}
	c.buckets[name] = kv
	return kv, nil
}
