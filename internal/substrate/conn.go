package substrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
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

	// publishAsyncTimeout bounds how long an ASYNC JetStream publish waits for
	// its store ack before the client resolves the future with
	// ErrAsyncPublishTimeout. nats.go leaves async acks unbounded by default —
	// a publish the server accepted but never acknowledged (a stream leader
	// lost between accept and ack) would otherwise leave the future unresolved
	// forever, hanging whichever PublishPipeline.Flush awaits it and holding
	// its slot in the connection's pending budget for the life of the process.
	// The value mirrors the SYNCHRONOUS path's own bound — nats.go applies a 5s
	// default API timeout to a Publish whose ctx carries no deadline — so
	// pipelining a write loop changes when acks are awaited, not how long a
	// stuck one can hold a caller.
	publishAsyncTimeout = 5 * time.Second

	// PublishAsyncMaxPending caps the unacknowledged async publishes ONE
	// JetStream context will hold, replacing nats.go's 4,000 default. The cap
	// is per connection and a process runs a single substrate.Conn, so it is
	// the budget every PublishPipeline open at that moment draws on at once —
	// each personal lens's write step holds two (rows and audit entries), and a
	// device hydrate or grant-change reprojection opens another. 8,192 funds 64
	// simultaneously draining pipelines at DefaultPublishPipelineWindow, which
	// covers the whole corpus with room to spare; crossing it is not a slow
	// path but a failure, since the publisher stalls 200ms and then returns
	// ErrTooManyStalledMsgs. That failure is fail-closed for whichever caller
	// hits it and for nobody else: the stalled publish is recorded on its own
	// pipeline and surfaces at that pipeline's Flush, so a hydrate returns an
	// error and the device re-attaches, and a row batch Naks and redelivers.
	// It never wedges a caller and never lets one ack a write that was refused.
	PublishAsyncMaxPending = 8192

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

	// connStateMu guards connStateListeners and closing. A dedicated mutex, not mu
	// above: mu is held across a KeyValue open (a network round trip), and a
	// connection-state event arriving mid-open must not queue behind it —
	// the whole value of the disconnect edge is that it reaches its
	// listeners promptly.
	connStateMu sync.Mutex
	// connStateListeners are the callbacks OnConnectionStateChange
	// registered, in registration order. Add-only for the connection's life:
	// every caller is a process-lifetime component whose listener outlives
	// each use, so a deregister would be a way to get the edge silently
	// dropped, not a feature.
	connStateListeners []func(connected bool)
	// closing latches when Close is called, so the disconnect edge nats.go
	// pushes for a deliberate shutdown never reaches a listener
	// (OnConnectionStateChange's doc). Guarded by connStateMu, which is what
	// makes the latch and the fan-out's read of it non-interleaving.
	closing bool
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
		if err := credsFilePreflight(opts.CredsFile); err != nil {
			return nil, err
		}
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
	js, err := jetstream.New(nc, publishAsyncOpts()...)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("substrate: jetstream context: %w", err)
	}
	return newConn(nc, js), nil
}

// newConn builds the substrate handle around an established connection and
// installs the one pair of nats.go connection-state handlers every
// OnConnectionStateChange listener is fanned out from.
//
// The indirection is what makes the signal shareable. nats.go's
// SetDisconnectErrHandler/SetReconnectHandler are SINGLE slots on the
// *nats.Conn — a second component setting one silently unregisters the
// first, and every Lattice binary shares one *Conn across its components.
// Owning the slots here and fanning out is the only shape in which two
// callers can both learn the connection dropped.
func newConn(nc *nats.Conn, js jetstream.JetStream) *Conn {
	c := &Conn{
		nc:           nc,
		js:           js,
		buckets:      make(map[string]jetstream.KeyValue),
		objectStores: make(map[string]jetstream.ObjectStore),
	}
	nc.SetDisconnectErrHandler(func(*nats.Conn, error) { c.reportConnectionState(false) })
	nc.SetReconnectHandler(func(*nats.Conn) { c.reportConnectionState(true) })
	return c
}

// OnConnectionStateChange registers fn to be called with false whenever the
// underlying NATS connection is lost (the start of nats.go's RECONNECTING
// window) and true whenever it is re-established. It never fires for the
// initial connect — a caller holding a *Conn is by construction connected —
// and never once Close has been called, which is a shutdown, not a loss.
//
// The Close suppression is Close's own doing, not nats.go's. nats.go's
// Conn.Close calls close(CLOSED, !Opts.NoCallbacksAfterClientClose, nil), and
// that option defaults false, so a deliberate shutdown pushes the SAME
// DisconnectedErrCB an outage does. A listener taking that at face value acts
// on "the connection is gone, degrade" during teardown — for a listener whose
// degrade path is a corpus-wide re-derivation, against a connection that can
// no longer carry a single request. Close therefore latches the fan-out off
// before it closes the connection (see Close).
//
// The signal is a CONNECTION fact, not a data-currency one. A listener that
// needs "am I up to date" must establish that separately (ConsumerCaughtUp):
// true here means only that bytes can flow again, while whatever landed in
// the stream during the outage is still undelivered at that instant. Reading
// the reconnect edge as "caught up" is exactly how a consumer comes back
// claiming a freshness it does not have.
//
// fn runs on nats.go's async-callback goroutine, which dispatches these in
// order and one at a time, so listeners are never re-entered and never race
// each other — and must be PROMPT for the same reason: a listener that
// blocks delays every later connection-state callback in the process,
// including its own reconnect. Register before the work that depends on the
// signal; there is no deregister (see connStateListeners).
func (c *Conn) OnConnectionStateChange(fn func(connected bool)) {
	if fn == nil {
		return
	}
	c.connStateMu.Lock()
	defer c.connStateMu.Unlock()
	c.connStateListeners = append(c.connStateListeners, fn)
}

// reportConnectionState fans one nats.go connection-state edge out to every
// registered listener, in registration order. The listener slice is copied
// under the lock and the callbacks run outside it, so a listener that
// registers another listener (or takes a lock of its own that some other
// goroutine holds while registering) cannot deadlock against this fan-out.
func (c *Conn) reportConnectionState(connected bool) {
	c.connStateMu.Lock()
	if c.closing {
		c.connStateMu.Unlock()
		return
	}
	listeners := make([]func(bool), len(c.connStateListeners))
	copy(listeners, c.connStateListeners)
	c.connStateMu.Unlock()
	for _, fn := range listeners {
		fn(connected)
	}
}

// Connected reports whether the underlying connection is currently
// established. It is a local read of nats.go's own connection status — no
// round trip — so it is cheap enough to consult on a poll loop, and is the
// right pre-check before an operation that would otherwise sit in the
// reconnect buffer waiting to time out.
func (c *Conn) Connected() bool { return c.nc != nil && c.nc.IsConnected() }

// ConnectionGeneration returns a token that changes across every connection
// loss, read SYNCHRONOUSLY from nats.go's own state. It answers a question
// OnConnectionStateChange structurally cannot: "has this connection been up,
// without interruption, between these two instants" — asked by a caller that
// holds a conclusion drawn at the first instant and is about to act on it at
// the second.
//
// The distinction is not academic, because the two signals disagree for a
// window with no upper bound. nats.go's processOpErr flips the status to
// RECONNECTING under the connection lock and only THEN spawns doReconnect,
// which pushes DisconnectedErrCB onto the single serial async-callback queue
// that every other connection callback in the process shares. So the loss is
// visible here — and to Connected — strictly BEFORE the fan-out reports it,
// by however long that queue is busy. A caller that treats "no disarming
// callback has arrived" as "the connection is still the one I measured" is
// reading the slower of two signals.
//
// The pair is what makes it a continuity test rather than a liveness one.
// connected alone misses a drop that has already been repaired; the counter
// alone misses a drop that has not been repaired yet. nats.go increments
// Reconnects (Stats) once per successful re-establishment, under the
// connection lock and before it queues ReconnectedCB, and never resets it —
// so equal counters plus connected at both instants means no re-establishment
// happened in between, and a connection that went down and came back cannot
// come back without one.
//
// The counter is read on both sides of the status read so that a
// re-establishment landing BETWEEN the two reads cannot be missed by both:
// such a read reports not-connected, because a caller cannot be told a
// generation it was never continuously inside. Every ambiguity here resolves
// to not-connected, since the callers are fail-closed by construction.
func (c *Conn) ConnectionGeneration() (generation uint64, connected bool) {
	if c.nc == nil {
		return 0, false
	}
	before := c.nc.Stats().Reconnects
	up := c.nc.IsConnected()
	after := c.nc.Stats().Reconnects
	if before != after {
		return after, false
	}
	return after, up
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

// credsFilePreflight validates credsFile eagerly, the same way
// nats.NkeyOptionFromSeed already validates NKeySeedFile before Connect
// ever reaches connectWithRetry — a missing or malformed file is a
// permanent, pre-dial-detectable error, not the transient stall the retry
// loop exists to absorb; nats.UserCredentials' own userCB/sigCB parse the
// file lazily inside the dial, which connectWithRetry cannot tell apart
// from a stalled handshake.
//
// Only the NKey seed half is actually checked: nkeys.ParseDecoratedJWT
// never returns an error (a file with no delimited JWT section falls back
// to treating its whole raw content as a bare JWT), so a malformed file's
// only detectable failure is nkeys.ParseDecoratedNKey finding no valid
// seed — exactly the failure nats.go's own sigHandler would hit at dial
// time, just surfaced here instead.
func credsFilePreflight(credsFile string) error {
	contents, err := os.ReadFile(credsFile)
	if err != nil {
		return fmt.Errorf("substrate: read creds file %q: %w", credsFile, err)
	}
	defer wipeBytes(contents)
	kp, err := nkeys.ParseDecoratedNKey(contents)
	if err != nil {
		return fmt.Errorf("substrate: creds file %q: parse NKey seed: %w", credsFile, err)
	}
	kp.Wipe()
	return nil
}

// wipeBytes overwrites buf in place — mirrors nats.go's own handling of
// creds/seed file contents (never left sitting in memory longer than
// needed).
func wipeBytes(buf []byte) {
	for i := range buf {
		buf[i] = 'x'
	}
}

// Wrap adapts an existing *nats.Conn into a substrate *Conn. Useful when
// callers need custom nats.Options beyond ConnectOpts.
//
// The wrapped connection's disconnect and reconnect handler slots become
// substrate's (newConn's doc says why they must have exactly one owner), so
// a caller wanting either edge asks for it through OnConnectionStateChange
// rather than setting the nats.go handler itself — a handler set on nc
// before Wrap is replaced, and one set after would replace substrate's and
// silently blind every OnConnectionStateChange listener. scripts/lint-conventions.go
// enforces that, since nothing about the failure is visible at runtime.
//
// Wrapping the SAME *nats.Conn twice is therefore not equivalent to holding
// one Conn twice: the handler slots follow the LAST wrap, so only the newest
// Conn's listeners are fanned out to. internal/bootstrap does exactly this
// (primordial.go and reconcile.go each wrap the caller's connection), which
// is harmless only because neither registers a listener. A caller that needs
// connection-state edges must keep the Conn it registered on, and must not
// let another Wrap of the same *nats.Conn outlive it.
func Wrap(nc *nats.Conn) (*Conn, error) {
	js, err := jetstream.New(nc, publishAsyncOpts()...)
	if err != nil {
		return nil, fmt.Errorf("substrate: jetstream context: %w", err)
	}
	return newConn(nc, js), nil
}

// publishAsyncOpts is the async-publish posture every substrate JetStream
// context takes, in one place so Connect and Wrap cannot drift apart: a bound
// on how long an unacknowledged publish may stay unresolved, and a ceiling on
// how many of them one connection holds. Both matter only to the async path
// (PublishPipeline); a synchronous Publish reaches neither.
func publishAsyncOpts() []jetstream.JetStreamOpt {
	return []jetstream.JetStreamOpt{
		jetstream.WithPublishAsyncTimeout(publishAsyncTimeout),
		jetstream.WithPublishAsyncMaxPending(PublishAsyncMaxPending),
	}
}

// PublishAsyncPending reports how many async publishes this connection has
// issued and not yet had acknowledged, across every PublishPipeline open on it.
// It is the connection-wide view of the budget publishAsyncMaxPending caps.
func (c *Conn) PublishAsyncPending() int {
	return c.js.PublishAsyncPending()
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
//
// The latch is set BEFORE the underlying close, not after: nats.go pushes its
// DisconnectedErrCB from the close path itself, so a latch set afterwards
// would race the very callback it exists to suppress — and the fan-out's
// listeners would act on a phantom outage during teardown
// (OnConnectionStateChange's doc has the mechanism). Nothing un-latches it: a
// closed connection never reconnects, so there is no state after this worth
// reporting.
func (c *Conn) Close() {
	c.connStateMu.Lock()
	c.closing = true
	c.connStateMu.Unlock()
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
