package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeClock drives the limiter's elapsed time deterministically — the bucket
// refills on a wall clock, so a sleeping test would be both slow and flaky.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)}
}

// TestCredentialLimiter_PerPeer pins the per-peer tier: the burst is allowed,
// the request past it is refused, and the bucket refills over time.
func TestCredentialLimiter_PerPeer(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)

	for i := 0; i < credLimitPerPeerPerMin; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d refused inside the per-peer burst", i+1)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("request past the per-peer burst was allowed")
	}

	// A different peer is unaffected — per-peer is fairness, so one noisy
	// client must not starve the login for everyone else.
	if !l.allow("5.6.7.8") {
		t.Fatal("a second peer was refused because the first exhausted its own bucket")
	}

	// One token per 6s at 10/min.
	clk.advance(6 * time.Second)
	if !l.allow("1.2.3.4") {
		t.Fatal("bucket did not refill")
	}
	if l.allow("1.2.3.4") {
		t.Fatal("refill handed out more than the elapsed time earned")
	}
}

// TestCredentialLimiter_GlobalCeiling pins that the global tier bounds the
// aggregate even when every request comes from a fresh peer — the distributed
// case per-peer accounting alone cannot bound.
func TestCredentialLimiter_GlobalCeiling(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)

	allowed := 0
	for i := 0; i < credLimitGlobalPerMin+50; i++ {
		// A distinct peer every time, so the per-peer tier never fires.
		if l.allow(peerIP(i)) {
			allowed++
		}
	}
	if allowed != credLimitGlobalPerMin {
		t.Fatalf("allowed %d requests, want the global ceiling of %d", allowed, credLimitGlobalPerMin)
	}
}

// TestCredentialLimiter_NoTokenSpentOnDenial pins the two-phase check: a
// request the global ceiling refuses must not burn the peer's token, or a
// throttled-by-global peer would come back throttled by its own tier too.
func TestCredentialLimiter_NoTokenSpentOnDenial(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)

	// Drain the global ceiling with other peers.
	for i := 0; i < credLimitGlobalPerMin; i++ {
		l.allow(peerIP(i))
	}
	if l.allow("9.9.9.9") {
		t.Fatal("global ceiling did not hold")
	}

	// Refill the global tier only; the fresh peer must still hold its full burst.
	clk.advance(time.Minute)
	for i := 0; i < credLimitPerPeerPerMin; i++ {
		if !l.allow("9.9.9.9") {
			t.Fatalf("peer request %d refused; the globally-denied attempt spent a peer token", i+1)
		}
	}
}

// TestCredentialLimiter_PeerMapBounded pins that tracking cannot grow without
// limit, and that a peer which cannot be tracked is still admitted under the
// global ceiling — denying instead would let a distributed flood lock every new
// visitor out of the login by filling the map.
func TestCredentialLimiter_PeerMapBounded(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)

	for i := 0; i < credLimitPeerCapacity+500; i++ {
		l.allow(peerIP(i))
		// Keep the global tier from being the thing that denies.
		clk.advance(time.Second)
	}
	l.mu.Lock()
	tracked := len(l.peers)
	l.mu.Unlock()
	if tracked > credLimitPeerCapacity {
		t.Fatalf("peer map grew to %d, past the %d cap", tracked, credLimitPeerCapacity)
	}
}

// TestCredentialLimiter_UntrackablePeerRidesGlobal pins the defensive branch a
// bounded map creates: the map is full of peers too FRESH to evict, so a new
// arrival gets no bucket of its own. It must still be admitted under the global
// ceiling — denying would let a flood lock every new visitor out of the login
// simply by filling the map.
//
// Note on reachability: since the global ceiling is now checked BEFORE any peer
// is inserted, this state is not reachable through allow() in production. Only
// ~300 peers can be admitted per minute against a 10-minute idle window, so the
// map cannot hold 4096 un-evictable entries. The branch is kept as a bound that
// does not depend on that arithmetic staying true, and is driven directly here
// rather than through a flood that can no longer produce it.
func TestCredentialLimiter_UntrackablePeerRidesGlobal(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)

	l.mu.Lock()
	for i := 0; i < credLimitPeerCapacity; i++ {
		l.peers[peerIP(i)] = newTokenBucket(credLimitPerPeerPerMin, clk.now())
	}
	l.lastSweep = clk.now()
	l.mu.Unlock()

	// Nothing is idle enough to evict, so the newcomer cannot be tracked.
	if !l.allow("fresh.peer") {
		t.Fatal("an untrackable peer was denied outright rather than admitted under the global ceiling")
	}
	l.mu.Lock()
	_, got := l.peers["fresh.peer"]
	after := len(l.peers)
	l.mu.Unlock()
	if got || after != credLimitPeerCapacity {
		t.Fatalf("the untrackable peer was tracked anyway (map now %d); the cap must hold", after)
	}
}

// TestCredentialLimiter_IdleEviction pins that idle buckets are reclaimed, so a
// long-running console does not accumulate every peer it ever saw. The sweep is
// driven directly: it only fires at capacity, which allow() can no longer reach
// (see TestCredentialLimiter_UntrackablePeerRidesGlobal).
func TestCredentialLimiter_IdleEviction(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)

	l.mu.Lock()
	for i := 0; i < 500; i++ {
		l.peers[peerIP(i)] = newTokenBucket(credLimitPerPeerPerMin, clk.now())
	}
	l.mu.Unlock()

	clk.advance(credLimitPeerIdle + time.Minute)

	l.mu.Lock()
	live := newTokenBucket(credLimitPerPeerPerMin, clk.now())
	l.peers["live.peer"] = live
	l.sweepIdleLocked(clk.now())
	tracked := len(l.peers)
	_, liveKept := l.peers["live.peer"]
	l.mu.Unlock()

	if tracked != 1 || !liveKept {
		t.Fatalf("idle sweep left %d buckets (live kept: %v), want only the live peer", tracked, liveKept)
	}
}

// TestCredentialLimiter_SweepThrottleHolds pins the throttle itself: a second
// sweep inside the interval is a no-op even when there is idle work to do.
func TestCredentialLimiter_SweepThrottleHolds(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)

	l.mu.Lock()
	l.sweepIdleLocked(clk.now()) // consume the interval
	l.peers["stale"] = newTokenBucket(credLimitPerPeerPerMin, clk.now().Add(-2*credLimitPeerIdle))
	l.sweepIdleLocked(clk.now())
	within := len(l.peers)
	l.mu.Unlock()

	if within != 1 {
		t.Fatalf("a second sweep inside the throttle interval ran anyway (map %d)", within)
	}

	clk.advance(credLimitSweepInterval + time.Second)
	l.mu.Lock()
	l.sweepIdleLocked(clk.now())
	after := len(l.peers)
	l.mu.Unlock()

	if after != 0 {
		t.Fatalf("sweep past the throttle interval left %d stale buckets", after)
	}
}

// TestCredentialPeerKey pins the peer identity source, which is the whole
// reason the limiter is not trivially bypassable behind a proxy — and equally,
// not trivially spoofable without one.
func TestCredentialPeerKey(t *testing.T) {
	cases := []struct {
		name       string
		declared   bool
		xff        string
		remoteAddr string
		want       string
	}{
		// Caddy appends the immediate peer and ignores what an untrusted client
		// sent, so the LAST entry is the real client. The FIRST entry is exactly
		// the one a client can forge.
		{"declared, loopback proxy, forged prefix ignored", true, "1.1.1.1, 2.2.2.2, 203.0.113.7", "127.0.0.1:5555", "203.0.113.7"},
		{"declared, loopback proxy, single hop", true, "203.0.113.7", "127.0.0.1:5555", "203.0.113.7"},
		{"declared, no XFF falls back to peer", true, "", "127.0.0.1:5555", "127.0.0.1"},
		// THE BYPASS: a declaration is not evidence the request came through
		// the proxy. A caller reaching the listener directly gets no say in its
		// own key, or it would mint a fresh full bucket per request.
		{"declared but non-loopback peer ignores XFF", true, "203.0.113.7", "198.51.100.9:4444", "198.51.100.9"},
		{"declared, loopback peer, junk XFF ignored", true, "not-an-ip", "127.0.0.1:5555", "127.0.0.1"},
		{"declared, loopback peer, oversized XFF ignored", true, strings.Repeat("A", 4096), "127.0.0.1:5555", "127.0.0.1"},
		// Undeclared there is no trusted proxy, so a forwarded header is just a
		// client-supplied string and must not be believed.
		{"undeclared ignores XFF", false, "203.0.113.7", "198.51.100.2:4444", "198.51.100.2"},
		{"undeclared ignores XFF even from loopback", false, "203.0.113.7", "127.0.0.1:5555", "127.0.0.1"},
		{"undeclared, no XFF", false, "", "198.51.100.2:4444", "198.51.100.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			if got := credentialPeerKey(req, tc.declared); got != tc.want {
				t.Errorf("credentialPeerKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestLimitCredentialExchange_429 pins the wire behavior: a throttled caller
// gets 429 with a Retry-After and the wrapped handler never runs, so the
// request costs no body read and no signing work.
func TestLimitCredentialExchange_429(t *testing.T) {
	clk := newTestClock()
	s := &server{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		credLimiter: newCredentialLimiter(clk.now),
	}

	reached := 0
	h := s.limitCredentialExchange(func(w http.ResponseWriter, r *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	})

	call := func() int {
		req := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
		req.RemoteAddr = "203.0.113.9:1234"
		rec := httptest.NewRecorder()
		h(rec, req)
		return rec.Code
	}

	for i := 0; i < credLimitPerPeerPerMin; i++ {
		if code := call(); code != http.StatusOK {
			t.Fatalf("call %d = %d, want 200", i+1, code)
		}
	}
	if reached != credLimitPerPeerPerMin {
		t.Fatalf("handler reached %d times, want %d", reached, credLimitPerPeerPerMin)
	}

	req := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
	req.RemoteAddr = "203.0.113.9:1234"
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("throttled call = %d, want 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 carried no Retry-After")
	}
	if reached != credLimitPerPeerPerMin {
		t.Error("the throttled request reached the handler; the limiter must run before any work")
	}
}

// TestLimitCredentialExchange_OnlyChargesPost pins that a GET to a credential
// path is not charged: it is answered 405 without work, and charging it would
// let a browser prefetch spend a real visitor's budget before they log in.
func TestLimitCredentialExchange_OnlyChargesPost(t *testing.T) {
	clk := newTestClock()
	s := &server{
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		credLimiter: newCredentialLimiter(clk.now),
	}
	h := s.limitCredentialExchange(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for i := 0; i < credLimitPerPeerPerMin*3; i++ {
		req := httptest.NewRequest(http.MethodGet, operatorDevTokenPath, nil)
		req.RemoteAddr = "203.0.113.9:1234"
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("GET %d was throttled; only POST should be charged", i+1)
		}
	}
	// The budget must be untouched.
	for i := 0; i < credLimitPerPeerPerMin; i++ {
		req := httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil)
		req.RemoteAddr = "203.0.113.9:1234"
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %d = %d; the GETs spent the peer's budget", i+1, rec.Code)
		}
	}
}

// TestCredentialLimiter_GlobalDenialSkipsPeerWork pins the ordering that makes
// the limiter cheap under attack: once the global ceiling is exhausted, a
// request with a brand-new key must not be inserted into the peer map.
func TestCredentialLimiter_GlobalDenialSkipsPeerWork(t *testing.T) {
	clk := newTestClock()
	l := newCredentialLimiter(clk.now)
	for i := 0; i < credLimitGlobalPerMin; i++ {
		l.allow(peerIP(i))
	}
	l.mu.Lock()
	before := len(l.peers)
	l.mu.Unlock()

	for i := 0; i < 100; i++ {
		if l.allow(fmt.Sprintf("post-exhaustion-%d", i)) {
			t.Fatal("global ceiling did not hold")
		}
	}
	l.mu.Lock()
	after := len(l.peers)
	l.mu.Unlock()
	if after != before {
		t.Errorf("peer map grew from %d to %d on globally-denied requests", before, after)
	}
}

// TestLimitCredentialExchange_NilLimiterPasses pins that a server without a
// limiter (test fixtures) is unaffected.
func TestLimitCredentialExchange_NilLimiterPasses(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	reached := false
	h := s.limitCredentialExchange(func(http.ResponseWriter, *http.Request) { reached = true })
	h(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, operatorDevTokenPath, nil))
	if !reached {
		t.Fatal("a nil limiter blocked the handler")
	}
}

// peerIP builds a distinct peer key per index, well past a /24.
func peerIP(i int) string {
	return fmt.Sprintf("10.%d.%d.%d", i/65536%256, i/256%256, i%256)
}
