package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParsePublicOrigin_Accepts(t *testing.T) {
	cases := []struct {
		raw      string
		hostname string
		port     string
	}{
		{"https://loupe.demo.example", "loupe.demo.example", "443"},
		{"https://loupe.demo.example/", "loupe.demo.example", "443"},
		{"https://loupe.demo.example:8443", "loupe.demo.example", "8443"},
		{"  https://LOUPE.Demo.Example  ", "loupe.demo.example", "443"},
		{"HTTPS://loupe.demo.example", "loupe.demo.example", "443"},
	}
	for _, tc := range cases {
		p, err := parsePublicOrigin(tc.raw)
		if err != nil {
			t.Errorf("parsePublicOrigin(%q) errored: %v", tc.raw, err)
			continue
		}
		if p == nil {
			t.Errorf("parsePublicOrigin(%q) = nil, want a declaration", tc.raw)
			continue
		}
		if p.hostname != tc.hostname || p.port != tc.port {
			t.Errorf("parsePublicOrigin(%q) = %s:%s, want %s:%s", tc.raw, p.hostname, p.port, tc.hostname, tc.port)
		}
	}
}

// TestParsePublicOrigin_Unset pins that the default posture is undeclared —
// this is what makes every other behavior in this file a no-op by default.
func TestParsePublicOrigin_Unset(t *testing.T) {
	for _, raw := range []string{"", "   "} {
		p, err := parsePublicOrigin(raw)
		if err != nil || p != nil {
			t.Errorf("parsePublicOrigin(%q) = (%v, %v), want (nil, nil)", raw, p, err)
		}
	}
}

// TestParsePublicOrigin_RefusesMalformed pins the fail-closed parse: a typo
// must stop the process, never read as "undeclared" and silently take the
// origin gate and the Secure cookie down with it.
func TestParsePublicOrigin_RefusesMalformed(t *testing.T) {
	for _, raw := range []string{
		"http://loupe.demo.example",       // plain HTTP cannot carry a Secure cookie
		"loupe.demo.example",              // no scheme
		"https://",                        // no host
		"https://loupe.demo.example/path", // path
		"https://loupe.demo.example/?a=1", // query
		"https://loupe.demo.example/#frag",
		"https://user:pw@loupe.demo.example",
		"https://loupe.demo.example:notaport",
		"wss://loupe.demo.example",
		"yes",
		// url.Parse only checks a port is digits. An out-of-range one would
		// boot a declaration no browser Origin can ever equal — a permanent
		// 403 on every login, with no boot-time signal.
		"https://loupe.demo.example:0",
		"https://loupe.demo.example:99999",
	} {
		if p, err := parsePublicOrigin(raw); err == nil {
			t.Errorf("parsePublicOrigin(%q) = (%v, nil); a malformed origin must fail closed", raw, p)
		}
	}
}

// TestCrossOriginGate_PublicOriginBranch is the gate matrix: the declared
// origin is accepted through the proxy (where Host is the internal bind and
// would never agree), and everything else still fails closed.
func TestCrossOriginGate_PublicOriginBranch(t *testing.T) {
	declared, err := parsePublicOrigin("https://loupe.demo.example")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	s := &server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		bindHost:     "127.0.0.1",
		publicOrigin: declared,
	}

	cases := []struct {
		name    string
		origin  string
		host    string
		blocked bool
	}{
		// The F20.5 case: Caddy passes Host through as the public name, but the
		// point is the branch does not depend on Host agreeing at all.
		{"declared origin, public host", "https://loupe.demo.example", "loupe.demo.example", false},
		{"declared origin, internal host", "https://loupe.demo.example", "127.0.0.1:7777", false},
		{"declared origin, explicit :443", "https://loupe.demo.example:443", "loupe.demo.example", false},
		{"declared origin, case-insensitive host", "https://LOUPE.Demo.Example", "loupe.demo.example", false},
		// Rebinding hardening survives: the attacker controls Origin AND Host,
		// and they agree by construction — but neither equals the declaration.
		{"rebound attacker origin agreeing with host", "https://evil.example", "evil.example", true},
		{"neighbor subdomain", "https://other.demo.example", "loupe.demo.example", true},
		{"declared host over plain http", "http://loupe.demo.example", "loupe.demo.example", true},
		{"declared host, wrong port", "https://loupe.demo.example:8443", "loupe.demo.example", true},
		{"null origin", "null", "loupe.demo.example", true},
		// Unchanged branches — the new one is additive, so both pre-existing
		// acceptance paths must still work with a declaration in place.
		{"loopback origin", "http://127.0.0.1:7777", "127.0.0.1:7777", false},
		{"no origin (curl)", "", "loupe.demo.example", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/operator/dev-token", nil)
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			if got := s.crossOriginBlocked(rec, req); got != tc.blocked {
				t.Errorf("crossOriginBlocked = %v, want %v", got, tc.blocked)
			}
		})
	}

	// The configured-bind-host branch, with a declaration present: a
	// non-loopback bind still accepts its own host, so declaring a public
	// origin does not narrow the pre-existing opt-in.
	bound := &server{
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		bindHost:     "10.0.0.5",
		publicOrigin: declared,
	}
	bindCases := []struct {
		name    string
		origin  string
		host    string
		blocked bool
	}{
		{"bind host origin", "http://10.0.0.5:7777", "10.0.0.5:7777", false},
		{"declared origin on a non-loopback bind", "https://loupe.demo.example", "10.0.0.5:7777", false},
		{"other host on a non-loopback bind", "https://evil.example", "evil.example", true},
	}
	for _, tc := range bindCases {
		t.Run("bind/"+tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/operator/dev-token", nil)
			req.Host = tc.host
			req.Header.Set("Origin", tc.origin)
			rec := httptest.NewRecorder()
			if got := bound.crossOriginBlocked(rec, req); got != tc.blocked {
				t.Errorf("crossOriginBlocked = %v, want %v", got, tc.blocked)
			}
		})
	}
}

// TestPublicOrigin_HostNormalization pins that a trailing FQDN dot addresses
// the same site on both sides. Treating the two forms as different origins
// would 403 every login — exactly the failure this posture exists to remove —
// with no boot-time signal that anything was wrong.
func TestPublicOrigin_HostNormalization(t *testing.T) {
	rooted, err := parsePublicOrigin("https://loupe.demo.example.")
	if err != nil {
		t.Fatalf("a trailing-dot declaration was refused: %v", err)
	}
	bare, err := parsePublicOrigin("https://loupe.demo.example")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rooted.hostname != bare.hostname {
		t.Fatalf("trailing dot not normalized: %q vs %q", rooted.hostname, bare.hostname)
	}
	for _, o := range []string{"https://loupe.demo.example", "https://loupe.demo.example."} {
		if !rooted.matches(o) || !bare.matches(o) {
			t.Errorf("origin %q did not match across the dot forms", o)
		}
	}
}

// TestPublicOrigin_MatchesIsNotLaxerThanParse pins that the wire side enforces
// the same rules as the config side. Browsers do not emit these shapes, but an
// asymmetry where the comparison is looser than the declaration is the kind of
// gap a later refactor turns into a real hole.
func TestPublicOrigin_MatchesIsNotLaxerThanParse(t *testing.T) {
	p, err := parsePublicOrigin("https://loupe.demo.example")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, origin := range []string{
		"https://evil.com@loupe.demo.example",
		"https://loupe.demo.example/path",
		"https://loupe.demo.example?x=1",
		"https://loupe.demo.example#@evil.com",
		"http://loupe.demo.example",
		"https://loupe.demo.example:0",
		// Unicode simple-case folding maps U+017F to "s"; a host comparison
		// must not be broader than the identity it asserts.
		"https://ſmart.example",
		"null",
		"",
	} {
		if p.matches(origin) {
			t.Errorf("matches(%q) = true; the wire side must be no laxer than the parser", origin)
		}
	}
}

// TestPublicOrigin_IPv6RoundTrip pins that an IPv6 declaration both matches and
// renders back as a parseable origin — the log line is what an operator reads
// while debugging a 403, so it must not print an unbracketed host.
func TestPublicOrigin_IPv6RoundTrip(t *testing.T) {
	p, err := parsePublicOrigin("https://[::1]:8443")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !p.matches("https://[::1]:8443") {
		t.Error("an IPv6 origin did not match its own declaration")
	}
	if got := p.String(); got != "https://[::1]:8443" {
		t.Errorf("String() = %q, want the bracketed form back", got)
	}
	if _, _, err := originComponents(p.String()); err != nil {
		t.Errorf("String() produced an unparseable origin: %v", err)
	}
}

// TestCrossOriginGate_UndeclaredUnchanged pins that with no declaration the
// gate is byte-for-byte its old self — a public-looking Origin is refused.
func TestCrossOriginGate_UndeclaredUnchanged(t *testing.T) {
	s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), bindHost: "127.0.0.1"}
	req := httptest.NewRequest(http.MethodPost, "/api/operator/dev-token", nil)
	req.Host = "loupe.demo.example"
	req.Header.Set("Origin", "https://loupe.demo.example")
	if !s.crossOriginBlocked(httptest.NewRecorder(), req) {
		t.Fatal("an undeclared public origin was accepted; the declaration must be what opens the branch")
	}
}

// TestCookieSecure_Matrix pins the flag across the four postures. The loopback
// bind WITH a declared origin is the one that used to fail open: a Secure-less
// session cookie on a public HTTPS site.
func TestCookieSecure_Matrix(t *testing.T) {
	declared, err := parsePublicOrigin("https://loupe.demo.example")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cases := []struct {
		name     string
		origin   *publicOrigin
		bindHost string
		want     bool
	}{
		{"loopback bind, undeclared (dev)", nil, "127.0.0.1", false},
		{"loopback bind, declared (proxied demo)", declared, "127.0.0.1", true},
		{"non-loopback bind, undeclared", nil, "10.0.0.5", true},
		{"non-loopback bind, declared", declared, "10.0.0.5", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The production derivation itself, not a copy of it — a local
			// re-expression here would keep passing if main.go regressed to
			// the bind-only term this exists to replace.
			got := sessionCookieSecure(tc.origin, tc.bindHost)
			if got != tc.want {
				t.Fatalf("sessionCookieSecure = %v, want %v", got, tc.want)
			}
			// And that the computed value is what actually reaches the cookie.
			s := &server{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), cookieSecure: got}
			rec := httptest.NewRecorder()
			s.clearOperatorSessionCookie(rec)
			cookies := rec.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("expected one cookie, got %d", len(cookies))
			}
			if cookies[0].Secure != tc.want {
				t.Errorf("cookie Secure = %v, want %v", cookies[0].Secure, tc.want)
			}
		})
	}
}

// TestPublicOriginAuthGuard pins the §6.4 refusal: dev-auth on a declared
// public origin hands the operator credential to every internet visitor, which
// is only a sane posture when that credential is the read-only demo identity.
func TestPublicOriginAuthGuard(t *testing.T) {
	cases := []struct {
		name                              string
		declared, devAuth, demo, wantFail bool
	}{
		{name: "declared + dev-auth, no demo mode", declared: true, devAuth: true, wantFail: true},
		{name: "declared + dev-auth + demo mode", declared: true, devAuth: true, demo: true},
		{name: "declared + real IdP (no dev-auth)", declared: true},
		{name: "declared + real IdP + demo", declared: true, demo: true},
		{name: "undeclared + dev-auth (the dev loopback posture)", devAuth: true},
		{name: "undeclared, nothing set"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := publicOriginAuthGuard(tc.declared, tc.devAuth, tc.demo)
			if tc.wantFail && err == nil {
				t.Fatal("guard admitted a dev-auth console on a public origin outside demo mode")
			}
			if !tc.wantFail && err != nil {
				t.Fatalf("guard refused a valid posture: %v", err)
			}
		})
	}
}

func TestEventStreamMax(t *testing.T) {
	cases := []struct {
		raw  string
		demo bool
		want int
	}{
		{"", false, defaultEventStreamClients},
		{"", true, demoEventStreamClients},
		{"  ", true, demoEventStreamClients},
		{"12", false, 12},
		{"12", true, 12},
		{"1", false, 1},
	}
	for _, tc := range cases {
		got, err := eventStreamMax(tc.raw, tc.demo)
		if err != nil {
			t.Errorf("eventStreamMax(%q, %v) errored: %v", tc.raw, tc.demo, err)
			continue
		}
		if got != tc.want {
			t.Errorf("eventStreamMax(%q, %v) = %d, want %d", tc.raw, tc.demo, got, tc.want)
		}
	}
	for _, raw := range []string{"0", "-1", "lots", "4.5", "1e3"} {
		if _, err := eventStreamMax(raw, false); err == nil {
			t.Errorf("eventStreamMax(%q) returned no error; a malformed knob must fail closed", raw)
		}
	}

	// Past the ceiling the slot counter's int32 would truncate — and past 2^31
	// it truncates NEGATIVE, refusing every tail and taking the live feed down
	// entirely. That must be refused at boot, not accepted and silently
	// reinterpreted.
	for _, raw := range []string{"1025", "2147483648", "4294967296", "99999999999999999999"} {
		if got, err := eventStreamMax(raw, false); err == nil {
			t.Errorf("eventStreamMax(%q) = %d with no error; a value the int32 counter cannot hold must fail closed", raw, got)
		}
	}
	if _, err := eventStreamMax("1024", false); err != nil {
		t.Errorf("eventStreamMax at the ceiling errored: %v", err)
	}
}

// TestEventStreamCap_NeverNegative walks the accepted range and pins that the
// int32 the slot counter compares against is always positive — the property
// the ceiling exists to guarantee.
func TestEventStreamCap_NeverNegative(t *testing.T) {
	for _, n := range []int{1, 4, 32, maxEventStreamCeiling} {
		s := &server{maxEventStreamClients: n}
		if got := s.eventStreamCap(); got <= 0 || int(got) != n {
			t.Errorf("eventStreamCap() = %d for maxEventStreamClients=%d", got, n)
		}
	}
}

// TestEventStreamCap_ZeroValueFallback pins that a server built without going
// through boot (a test fixture) still admits tails rather than denying every
// one of them.
func TestEventStreamCap_ZeroValueFallback(t *testing.T) {
	s := &server{}
	if got := s.eventStreamCap(); got != defaultEventStreamClients {
		t.Fatalf("eventStreamCap() = %d on a zero-value server, want %d", got, defaultEventStreamClients)
	}
}

// TestAtCapacityMessage pins the posture split: an operator gets an actionable
// instruction, a demo visitor — who cannot close a stranger's tab — gets a
// wait-and-retry.
func TestAtCapacityMessage(t *testing.T) {
	operator := (&server{maxEventStreamClients: 4}).atCapacityMessage()
	if !strings.Contains(operator, "max 4") || !strings.Contains(operator, "close another Loupe tab") {
		t.Errorf("operator at-capacity message = %q", operator)
	}
	visitor := (&server{maxEventStreamClients: 32, demoMode: true}).atCapacityMessage()
	if strings.Contains(visitor, "close another Loupe tab") {
		t.Errorf("demo visitor told to close a tab they do not own: %q", visitor)
	}
	if !strings.Contains(visitor, "capacity") {
		t.Errorf("demo at-capacity message = %q", visitor)
	}
}
