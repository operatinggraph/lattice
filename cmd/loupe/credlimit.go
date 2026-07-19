package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The credential-exchange rate limiter (loupe-f20-demo-operator-ux.md §6.5).
//
// The three requireOperator-exempt POST endpoints (dev-token, session, logout)
// are the console's only unauthenticated handlers. On a public URL they are the
// whole pre-auth attack surface, and the mint does RSA signing per call.
//
// Sizing honesty: the mint is FIXED-SUBJECT (readauth.go's handleOperatorDevToken
// mints for the configured operatorActorKey, never a caller-supplied one), so N
// tokens are N copies of the SAME one credential. This limiter therefore does
// not bound credential proliferation — one mint is already full demo capability,
// which is the intended demo posture — it bounds unauthenticated compute and log
// churn. Do not mistake it for an authorization boundary; that is the demo
// operator's grants (F20.3).
//
// It lives here rather than at the edge because the apt-installed Caddy ships no
// HTTP rate-limit handler (it is a third-party xcaddy module — see the Caddy row
// in docs/vendors.md), so an edge limiter would mean a custom Caddy build in the
// bootstrap. In-lane it is unit-testable and holds under any future proxy.
const (
	// credLimitPerPeerPerMin is ~30x a real visitor's need: one mint per
	// 30-minute token TTL.
	credLimitPerPeerPerMin = 10
	// credLimitGlobalPerMin is the actual abuse bound — five signs a second is
	// trivial CPU while capping a flood. Per-peer is fairness on top of it, so
	// one noisy client cannot starve the login for everyone else.
	credLimitGlobalPerMin = 300
	// credLimitPeerCapacity bounds the peer map so tracking cannot grow
	// unboundedly under a distributed flood.
	credLimitPeerCapacity = 4096
	// credLimitPeerIdle is how long an untouched peer bucket survives a sweep.
	credLimitPeerIdle = 10 * time.Minute
	// credLimitSweepInterval throttles the eviction scan (sweepIdleLocked).
	credLimitSweepInterval = time.Minute
)

// credLimitMessage is what a throttled caller sees. Visitor-legible: on the
// demo this is a member of the public who clicked the login button twice.
const credLimitMessage = "too many login attempts — wait a moment and try again"

// tokenBucket is a continuous-refill bucket. Refill and consumption are split
// so a two-tier check can require BOTH tiers to have a token before either is
// spent — a per-peer token must not be burned by a request the global ceiling
// is about to refuse.
type tokenBucket struct {
	tokens    float64
	perSecond float64
	capacity  float64
	last      time.Time
}

func newTokenBucket(perMinute float64, now time.Time) *tokenBucket {
	return &tokenBucket{
		tokens:    perMinute,
		perSecond: perMinute / 60,
		capacity:  perMinute,
		last:      now,
	}
}

func (b *tokenBucket) refill(now time.Time) {
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens += elapsed.Seconds() * b.perSecond
		if b.tokens > b.capacity {
			b.tokens = b.capacity
		}
		b.last = now
	}
}

func (b *tokenBucket) has() bool { return b.tokens >= 1 }

func (b *tokenBucket) take() { b.tokens-- }

// credentialLimiter is the two-tier limiter over the credential-exchange
// endpoints. now is injected so tests drive elapsed time deterministically
// rather than sleeping.
type credentialLimiter struct {
	mu     sync.Mutex
	global *tokenBucket
	peers  map[string]*tokenBucket
	now    func() time.Time
	// lastSweep throttles evictIdleLocked. Without it, a full map of peers all
	// too fresh to evict makes every subsequent new-peer arrival run a full
	// scan under mu — so a caller rotating its peer key turns the limiter into
	// an amplifier costing more than the signing it exists to shield.
	lastSweep time.Time
}

func newCredentialLimiter(now func() time.Time) *credentialLimiter {
	if now == nil {
		now = time.Now
	}
	t := now()
	return &credentialLimiter{
		global:    newTokenBucket(credLimitGlobalPerMin, t),
		peers:     make(map[string]*tokenBucket),
		now:       now,
		lastSweep: t,
	}
}

// allow reports whether this request may proceed, consuming one token from each
// tier when it does.
//
// A peer that cannot be tracked — the map is at capacity and a sweep freed
// nothing — is admitted under the GLOBAL ceiling alone rather than denied
// outright. Denying would let a distributed flood lock every new visitor out of
// the login by filling the map; the global ceiling is the bound that still
// holds in that state, which is the tier that was doing the real work anyway.
// The global ceiling is checked FIRST, before any peer-map work. That ordering
// is what keeps the limiter cheap under attack: a request the global tier is
// going to refuse costs one comparison, never a map insert or an eviction
// sweep. Checked the other way round, a caller rotating its peer key could
// force unbounded map churn under mu for requests that were being denied
// anyway.
func (l *credentialLimiter) allow(peer string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.global.refill(now)
	if !l.global.has() {
		return false
	}

	b := l.peers[peer]
	if b == nil {
		if len(l.peers) >= credLimitPeerCapacity {
			l.sweepIdleLocked(now)
		}
		if len(l.peers) < credLimitPeerCapacity {
			b = newTokenBucket(credLimitPerPeerPerMin, now)
			l.peers[peer] = b
		}
	}

	if b != nil {
		b.refill(now)
		if !b.has() {
			return false
		}
		b.take()
	}
	l.global.take()
	return true
}

// sweepIdleLocked drops peer buckets untouched for credLimitPeerIdle, at most
// once per credLimitSweepInterval. A bucket idle that long has refilled to
// capacity, so forgetting it loses no state a fresh one would not reproduce.
//
// The throttle matters more than the sweep: when every tracked peer is too
// fresh to evict, the scan frees nothing, and running it per-arrival would be
// pure amplification. Skipping it just means the newcomer goes untracked and
// rides the global ceiling — the same outcome the capacity path already has.
func (l *credentialLimiter) sweepIdleLocked(now time.Time) {
	if now.Sub(l.lastSweep) < credLimitSweepInterval {
		return
	}
	l.lastSweep = now
	for k, b := range l.peers {
		if now.Sub(b.last) >= credLimitPeerIdle {
			delete(l.peers, k)
		}
	}
}

// credentialPeerKey identifies the caller for per-peer accounting.
//
// Behind a reverse proxy the immediate peer is the proxy, so RemoteAddr alone
// would collapse every visitor onto one bucket. Caddy IGNORES incoming
// X-Forwarded-* from an untrusted client and appends the immediate peer (its
// anti-spoofing default, verified upstream — see docs/vendors.md), so the LAST
// entry is the real client and is not client-forgeable. The first entry, the
// usual choice, is exactly the one a client CAN forge.
//
// But that only holds for traffic that actually TRAVERSED the proxy, and a
// declaration alone is not evidence of it: a request arriving directly at the
// listener carries whatever X-Forwarded-For its sender chose. Trusting the
// header on the declaration alone would let a direct caller mint a fresh
// full bucket per request and bypass the per-peer tier entirely. So the header
// is believed only when the immediate peer is LOOPBACK — the same-host,
// single-hop topology the demo deploys (Caddy terminating TLS in front of a
// loopback-bound Loupe), and one an off-host client cannot forge, since it
// cannot originate a loopback connection.
//
// The entry must also parse as an IP. Beyond rejecting junk, that bounds the
// map key: an unvalidated header substring is attacker-sized, and 4096 tracked
// peers holding megabyte keys is gigabytes resident.
//
// Known limit, tied to F20.4's topology choice: with MORE than one hop in front
// (a CDN, or a second proxy), the last entry is the nearest edge rather than
// the client, and visitors sharing an edge share a bucket. That degrades to
// over-throttling, never to a bypass — but it means the demo must be deployed
// single-hop, which is on the exposure checklist.
func credentialPeerKey(r *http.Request, originDeclared bool) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if originDeclared && isLoopbackHost(host) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
}

// limitCredentialExchange wraps one of the three unauthenticated credential
// endpoints. It runs before the handler, so a throttled request costs no body
// read and no signing work. A nil limiter is a pass-through.
//
// Only POST is charged — the method these three endpoints actually accept, and
// the only one that mints or verifies anything. A GET is answered 405 by the
// handler without touching a key or a body, and charging for it would let a
// browser prefetch or a link-preview crawler spend a real visitor's budget
// before they ever click Log in.
func (s *server) limitCredentialExchange(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next(w, r)
			return
		}
		if s.credLimiter != nil && !s.credLimiter.allow(credentialPeerKey(r, s.publicOrigin != nil)) {
			w.Header().Set("Retry-After", "60")
			s.writeError(w, http.StatusTooManyRequests, credLimitMessage)
			return
		}
		next(w, r)
	}
}
