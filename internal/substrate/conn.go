package substrate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

const (
	// defaultConnectTimeout is the per-attempt handshake budget Connect uses
	// when ConnectOpts.Timeout is zero, replacing nats.go's own 2s default
	// (processConnectInit pins the whole INFO+CONNECT+PING/PONG exchange to
	// this under one conn.SetDeadline). nats.go applies Opts.Timeout to
	// EVERY dial alike — the initial connect and every automatic reconnect
	// (doReconnect calls createConn, which applies it identically) — so
	// this is deliberately smaller than internal/natsfixture's 20s: a test
	// fixture connects once and never reconnects, but a long-running
	// component's reconnect cadence (e.g. cmd/processor's
	// ReconnectWait: 1s) would otherwise slow by the same multiple on every
	// cycle against a genuinely unreachable server, not just a loaded one.
	defaultConnectTimeout = 10 * time.Second

	// connectAttempts bounds the retry on the INITIAL connect only —
	// reconnects after a connection has been established once are nats.go's
	// own loop (MaxReconnects/ReconnectWait), never this one. A stall that
	// resets the socket outright during the first dial is not covered by a
	// longer Timeout alone (a reset returns immediately, however generous
	// the budget), so the first attempt gets a bounded retry too. Matches
	// internal/natsfixture's connectAttempts.
	connectAttempts = 4

	// connectBackoff is the pause between initial-connect retries. Matches
	// internal/natsfixture's connectBackoff.
	connectBackoff = 250 * time.Millisecond
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
	// Timeout bounds each dial's handshake — the initial connect AND every
	// automatic reconnect alike (nats.go applies the same budget to both;
	// see connectWithRetry). Zero means "use substrate's own default"
	// (defaultConnectTimeout) — deliberately NOT "use the nats.go default":
	// nats.go's bare 2s default is exactly the trap this field exists to
	// avoid. The INITIAL connect additionally gets a small bounded retry
	// (connectWithRetry) that MaxReconnects/ReconnectWait do not apply to;
	// a ctx passed to Connect bounds that retry loop between attempts, not
	// Timeout itself.
	Timeout time.Duration
	// MaxReconnects controls the reconnect retry budget. Zero means
	// "use the nats.go default". Set to -1 for unlimited.
	MaxReconnects int
	// ReconnectWait controls the delay between reconnect attempts.
	// Zero means "use the nats.go default".
	ReconnectWait time.Duration

	// NKeySeedFile is the path to a per-component NKey seed file used to
	// authenticate the connection (challenge-response; no shared secret on
	// the wire). When set, Connect signs the server's nonce with the seed.
	// Empty ⇒ anonymous connect (the embedded test harness and any
	// unauthenticated server). At most one of NKeySeedFile / CredsFile is set.
	NKeySeedFile string
	// CredsFile is the path to a NATS user credentials file (a chained
	// JWT + seed, for decentralized/operator mode). Empty ⇒ anonymous
	// connect. At most one of NKeySeedFile / CredsFile is set.
	CredsFile string
	// Token is a bearer token presented as the CONNECT `auth_token` — the
	// untrusted Edge connection's credential under the NATS auth-callout
	// boundary (per-identity-nats-subscribe-acl-design.md §3.1a: "the bearer
	// token arrives in `token`"), verified server-side by
	// internal/gateway/natsauth. Empty ⇒ no token. At most one of
	// NKeySeedFile / CredsFile / Token / TokenHandler is set.
	Token string
	// TokenHandler, when set, is called for the current bearer token on
	// every (re)connect attempt instead of replaying the fixed Token string.
	// The credential this boundary accepts is exp-bounded (the same JWT
	// `exp` natsauth caps its issued grant to), so a long-lived connection
	// whose caller can locally re-mint a fresh token needs this to survive
	// past that expiry — nats.go's reconnect loop otherwise re-presents the
	// original (now-expired) Token forever and gives up after two identical
	// auth errors on the same server (its own processAuthError). At most one
	// of NKeySeedFile / CredsFile / Token / TokenHandler is set.
	TokenHandler func() string
	// InboxPrefix, when non-empty, scopes every request-reply inbox this
	// connection creates under "<InboxPrefix>.<nuid>" instead of the
	// client-wide default (nats.go's InboxPrefix option). The per-identity
	// subscribe-ACL template (per-identity-nats-subscribe-acl-design.md
	// §3.3) grants subscribe on the caller's own inbox namespace only — a
	// shared default prefix would let one identity's connection collide
	// with (and be denied access to) another's replies. Empty ⇒ nats.go's
	// default "_INBOX" prefix.
	InboxPrefix string
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

	mu           sync.Mutex
	buckets      map[string]jetstream.KeyValue
	objectStores map[string]jetstream.ObjectStore
}

// Connect establishes a new NATS + JetStream connection using opts.
// The returned *Conn must be closed with Close when no longer needed.
func Connect(ctx context.Context, opts ConnectOpts) (*Conn, error) {
	if opts.URL == "" {
		opts.URL = nats.DefaultURL
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultConnectTimeout
	}
	natsOpts := []nats.Option{nats.Timeout(timeout)}
	if opts.Name != "" {
		natsOpts = append(natsOpts, nats.Name(opts.Name))
	}
	if opts.MaxReconnects != 0 {
		natsOpts = append(natsOpts, nats.MaxReconnects(opts.MaxReconnects))
	}
	if opts.ReconnectWait > 0 {
		natsOpts = append(natsOpts, nats.ReconnectWait(opts.ReconnectWait))
	}
	if opts.InboxPrefix != "" {
		natsOpts = append(natsOpts, nats.CustomInboxPrefix(opts.InboxPrefix))
	}
	credCount := 0
	for _, set := range []bool{opts.NKeySeedFile != "", opts.CredsFile != "", opts.Token != "", opts.TokenHandler != nil} {
		if set {
			credCount++
		}
	}
	if credCount > 1 {
		return nil, fmt.Errorf("substrate: ConnectOpts has more than one of NKeySeedFile/CredsFile/Token/TokenHandler set; exactly one credential may be supplied")
	}
	if opts.NKeySeedFile != "" {
		nkeyOpt, err := nats.NkeyOptionFromSeed(opts.NKeySeedFile)
		if err != nil {
			return nil, fmt.Errorf("substrate: load NKey seed %q: %w", opts.NKeySeedFile, err)
		}
		natsOpts = append(natsOpts, nkeyOpt)
	}
	if opts.CredsFile != "" {
		natsOpts = append(natsOpts, nats.UserCredentials(opts.CredsFile))
	}
	if opts.Token != "" {
		natsOpts = append(natsOpts, nats.Token(opts.Token))
	}
	if opts.TokenHandler != nil {
		natsOpts = append(natsOpts, nats.TokenHandler(opts.TokenHandler))
	}
	nc, err := connectWithRetry(ctx, opts.URL, natsOpts...)
	if err != nil {
		return nil, fmt.Errorf("substrate: nats connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("substrate: jetstream context: %w", err)
	}
	return &Conn{nc: nc, js: js, buckets: make(map[string]jetstream.KeyValue), objectStores: make(map[string]jetstream.ObjectStore)}, nil
}

// connectWithRetry dials url with a bounded retry, giving the INITIAL
// handshake the same stall-tolerance internal/natsfixture gives test
// connects: the per-attempt Timeout (already in opts) absorbs a slow
// handshake, and the retry absorbs a stall that resets the socket outright
// (which no timeout, however generous, can absorb — a reset returns
// immediately). An auth-time rejection is never retried (permanentConnectError).
//
// ctx bounds the retry LOOP — a new attempt is not started once ctx is done
// — but never shrinks Timeout itself. nats.Connect is a blocking call with
// no context hook (the nats.go API has none), so an attempt already under
// way cannot be interrupted early; and Timeout is baked into the resulting
// *nats.Conn and reused by nats.go's own reconnect loop for that
// connection's whole remaining life (createConn, called from both Connect
// and doReconnect, always reads it off Opts), so deriving it from a
// transient ctx's remaining budget would leave a long-lived connection
// permanently under-budgeted long after that ctx is gone. A caller with a
// tight ctx deadline and a single stalled attempt can still see one
// over-budget dial; only the retry beyond it is bounded by ctx.
func connectWithRetry(ctx context.Context, url string, opts ...nats.Option) (*nats.Conn, error) {
	var err error
	for attempt := 1; ; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if err != nil {
				return nil, fmt.Errorf("%w (retry stopped: %w)", err, ctxErr)
			}
			return nil, ctxErr
		}
		var nc *nats.Conn
		if nc, err = nats.Connect(url, opts...); err == nil {
			return nc, nil
		}
		if permanentConnectError(err) || attempt == connectAttempts {
			return nil, err
		}
		select {
		case <-time.After(connectBackoff):
		case <-ctx.Done():
			return nil, fmt.Errorf("%w (retry stopped: %w)", err, ctx.Err())
		}
	}
}

// permanentConnectError reports whether err is an auth-time rejection a
// retry cannot fix — the same credential presented again is rejected
// again, so retrying only turns one fast, clear denial into several slower
// ones (and, for TokenHandler, calls the handler needlessly on each).
func permanentConnectError(err error) bool {
	return errors.Is(err, nats.ErrAuthorization) ||
		errors.Is(err, nats.ErrAuthExpired) ||
		errors.Is(err, nats.ErrAuthRevoked) ||
		errors.Is(err, nats.ErrAccountAuthExpired)
}

// Wrap adapts an existing *nats.Conn into a substrate *Conn. Useful when
// callers need custom nats.Options beyond ConnectOpts.
func Wrap(nc *nats.Conn) (*Conn, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("substrate: jetstream context: %w", err)
	}
	return &Conn{nc: nc, js: js, buckets: make(map[string]jetstream.KeyValue), objectStores: make(map[string]jetstream.ObjectStore)}, nil
}

// NATS returns the underlying *nats.Conn. Provided as an escape hatch for
// operations substrate does not yet wrap (subscription, raw JetStream).
// Callers should prefer the typed helpers when one exists.
func (c *Conn) NATS() *nats.Conn { return c.nc }

// JetStream returns the underlying jetstream.JetStream context. Escape
// hatch — prefer the typed helpers.
func (c *Conn) JetStream() jetstream.JetStream { return c.js }

// Request performs a NATS request-reply call on subject with body, returning
// the reply payload. It is the substrate-level RPC seam for components that
// must not hold a raw *nats.Conn of their own (e.g. internal/loom's no-raw-NATS
// boundary) but still need point-to-point request-reply, mirroring the shape
// of the typed KV helpers below.
func (c *Conn) Request(ctx context.Context, subject string, body []byte) ([]byte, error) {
	msg, err := c.nc.RequestWithContext(ctx, subject, body)
	if err != nil {
		return nil, fmt.Errorf("substrate: request %q: %w", subject, err)
	}
	return msg.Data, nil
}

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
