package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// The public-origin posture (LOUPE_PUBLIC_ORIGIN,
// loupe-f20-demo-operator-ux.md §6). Everything the console's browser-facing
// machinery derives from the BIND host is wrong once a TLS-terminating reverse
// proxy sits in front: the browser's Origin names the public site, not the
// bind, and the bind is loopback while the site is public HTTPS. Two derivations
// fail in opposite directions —
//
//   - crossOriginBlocked fails CLOSED: the proxied Origin matches neither
//     loopback nor the bind host, so every login and logout 403s and no visitor
//     can get in at all.
//   - the session cookie's Secure flag fails OPEN: a loopback bind reads as
//     "not public", so the cookie ships without Secure on an HTTPS site.
//
// A process behind a reverse proxy cannot distinguish the proxy's requests from
// a direct local caller's, so the origin is DECLARED rather than detected. Unset
// leaves every path below byte-for-byte as it was.

// publicOrigin is a parsed, normalized LOUPE_PUBLIC_ORIGIN. Port is always
// populated (443 when the declaration omits it) so comparison is componentwise
// and never string-shaped.
type publicOrigin struct {
	hostname string // lowercased
	port     string
}

// String renders the origin back in wire form, for log lines and errors. An
// IPv6 host is re-bracketed: hostname holds the unbracketed form (url.Hostname
// strips them), and concatenating that raw would print "https://::1:8443",
// which is neither a parseable URL nor recognizably what the operator
// configured — the worst possible thing to show someone debugging a 403.
func (p *publicOrigin) String() string {
	if p == nil {
		return ""
	}
	host := p.hostname
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if p.port == "443" {
		return "https://" + host
	}
	return "https://" + host + ":" + p.port
}

// normalizeHost canonicalizes a hostname for comparison: lowercased, with a
// single FQDN-rooting trailing dot removed. "loupe.demo.example." and
// "loupe.demo.example" address the same site, and a browser sends the form the
// user typed — so treating them as different origins would 403 every login
// with no boot-time signal, which is precisely the failure this posture exists
// to eliminate.
func normalizeHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// originComponents validates an absolute https origin and returns its
// normalized host and port (443-defaulted).
//
// Both parsePublicOrigin and publicOrigin.matches go through this one
// function, deliberately: if the declaration side enforced rules the
// comparison side did not, the two would disagree about what "is this origin"
// means, and the looser side is the one an attacker reaches.
func originComponents(raw string) (host, port string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("is not a URL: %w", err)
	}
	if u.Opaque != "" {
		return "", "", errors.New("is not an absolute origin")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", "", errors.New("must use the https scheme (a plain-HTTP public deployment cannot carry a Secure session cookie)")
	}
	if u.User != nil {
		return "", "", errors.New("must not carry userinfo")
	}
	if u.Path != "" && u.Path != "/" {
		return "", "", errors.New("must not carry a path")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", "", errors.New("must not carry a query")
	}
	if u.Fragment != "" {
		return "", "", errors.New("must not carry a fragment")
	}
	host = normalizeHost(u.Hostname())
	if host == "" {
		return "", "", errors.New("has no host")
	}
	// url.Parse only checks a port is all digits, with no range check — so
	// ":0" and ":99999" parse. Accepting one would boot a declaration no
	// browser Origin can ever equal: a permanent 403 on every login.
	port = u.Port()
	if port == "" {
		port = "443"
	} else {
		n, convErr := strconv.Atoi(port)
		if convErr != nil || n < 1 || n > 65535 {
			return "", "", fmt.Errorf("has an out-of-range port (%s)", port)
		}
	}
	return host, port, nil
}

// parsePublicOrigin parses LOUPE_PUBLIC_ORIGIN. Empty yields (nil, nil) — the
// undeclared posture, which is the default and changes nothing.
//
// Fail-closed, like demoModeEnabled: anything malformed refuses to boot rather
// than reading as undeclared. A typo that silently disables the declaration
// takes the origin gate and the Secure cookie down with it, on the one
// deployment shape where both matter.
//
// The scheme must be https: Secure cookies do not survive plain HTTP, so an
// http:// public origin is a posture this design cannot honor rather than a
// weaker one it should accept quietly. Userinfo, query, and fragment are
// rejected as evidence of a misunderstanding about what an origin is; a bare
// trailing slash is accepted, being the empty path written out.
func parsePublicOrigin(raw string) (*publicOrigin, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	host, port, err := originComponents(v)
	if err != nil {
		return nil, fmt.Errorf("LOUPE_PUBLIC_ORIGIN=%q %w; it must be the exact external origin "+
			"the console is served at, e.g. https://loupe.demo.example", raw, err)
	}
	return &publicOrigin{hostname: host, port: port}, nil
}

// matches reports whether an Origin header names exactly this public origin,
// compared componentwise: scheme https, hostname case-insensitively equal, and
// port with 443-defaulting on both sides (browsers omit the default port).
//
// It deliberately does NOT consult the request's Host header. Equality against
// a constant configured at boot is strictly stronger than the Origin↔Host
// agreement the loopback branch needs, and it keeps the gate's DNS-rebinding
// hardening intact: under rebinding the attacker's Origin carries the
// attacker's hostname, which equals neither loopback, the bind host, nor this.
//
// A nil receiver (no origin declared) matches nothing, so the caller needs no
// separate nil check.
//
// It validates the incoming Origin through originComponents — the same rules
// the declaration was parsed with — so the wire side can never be laxer than
// the config side. Comparison is then plain equality over already-normalized
// values, deliberately NOT strings.EqualFold: EqualFold does full Unicode
// simple-case folding, under which U+017F (ſ) folds to "s", so a declared
// "smart.example" would match an Origin of "ſmart.example". No browser emits
// that (IDN is punycoded before Origin is set), but a host comparison should
// not be broader than the identity it is asserting.
func (p *publicOrigin) matches(rawOrigin string) bool {
	if p == nil || rawOrigin == "" {
		return false
	}
	host, port, err := originComponents(rawOrigin)
	if err != nil {
		return false
	}
	return host == p.hostname && port == p.port
}

// sessionCookieSecure computes the session cookie's Secure flag: set when a
// public origin is declared (https by construction, parsePublicOrigin) or the
// bind is non-loopback.
//
// The declaration term is the load-bearing one. Derived from the bind alone —
// which is what this used to be — it fails OPEN in exactly the deployment shape
// this posture exists for: a loopback bind behind a TLS-terminating proxy reads
// as "not public", so the session cookie ships without Secure on a public HTTPS
// site. It lives here as a named function rather than inline at the one call
// site so a test can pin the derivation itself, not just the cookie plumbing.
func sessionCookieSecure(origin *publicOrigin, bindHost string) bool {
	return origin != nil || !isLoopbackHost(bindHost)
}

// publicOriginAuthGuard refuses a declared public origin combined with
// dev-auth outside demo mode.
//
// Dev-auth mints the fully-configured operator credential for anyone who asks
// (readauth.go's fixed-subject mint) — on a public URL that hands the console's
// identity to every internet visitor. That is only sane when the identity is
// the demo operator whose grants permit nothing, i.e. demo mode. A writable
// proxied console must use a real IdP (LOUPE_JWT_PUBLIC_KEY), which is exactly
// what setupOperatorAuth already demands of a non-loopback BIND: same exposure
// arriving through the proxy door instead, so it gets the same refusal.
//
// Honest limit: Loupe cannot detect an UNDECLARED proxy. This closes the
// misconfiguration where the declaration exists but the operator forgot what
// dev-auth means on a public URL — not the one where nobody declared anything.
func publicOriginAuthGuard(originDeclared, devAuthEnabled, demoMode bool) error {
	if !originDeclared || !devAuthEnabled || demoMode {
		return nil
	}
	return fmt.Errorf("LOUPE_PUBLIC_ORIGIN with LOUPE_DEV_AUTH requires LOUPE_DEMO_MODE: " +
		"dev-auth mints the configured operator credential for every caller, so a publicly-served " +
		"console may only run it as the read-only demo operator; use LOUPE_JWT_PUBLIC_KEY for a " +
		"writable public deployment")
}

const (
	// defaultEventStreamClients bounds concurrent SSE tails for the ordinary
	// posture: Loupe is a loopback single-operator tool and a handful of tabs
	// is the ceiling, not a fleet.
	defaultEventStreamClients = 4
	// demoEventStreamClients is the demo posture's bound. The live pulse is the
	// demo's most persuasive behind-the-scenes surface, and each tail costs one
	// ephemeral ordered consumer plus one goroutine — negligible beside the full
	// stack already on the demo box — so starving concurrent visitors at 4 would
	// gut the demo for no meaningful resource saving.
	demoEventStreamClients = 32
)

// maxEventStreamCeiling bounds LOUPE_EVENT_STREAM_MAX. Two reasons, and the
// second is why it is this low rather than merely int32-safe:
//
//   - Truncation. The slot counter is an atomic.Int32, so a value past 2^31
//     truncates NEGATIVE, which refuses every tail and takes the live feed down
//     completely while boot reports success.
//   - Resources. Each admitted tail is one goroutine plus one ephemeral ordered
//     JetStream consumer. A fat-fingered extra zero should not silently commit
//     the demo box to tens of thousands of consumers; 1024 is already two
//     orders of magnitude above the demo default of 32.
//
// Rejecting at boot keeps the fail-closed parse rule honest: a value this knob
// cannot actually honor must stop the process, not be quietly reinterpreted.
const maxEventStreamCeiling = 1024

// eventStreamMax resolves the SSE client bound: LOUPE_EVENT_STREAM_MAX when
// set, else the posture default. Malformed refuses to boot, the same
// fail-closed parse rule as every other knob in this posture.
func eventStreamMax(raw string, demoMode bool) (int, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		if demoMode {
			return demoEventStreamClients, nil
		}
		return defaultEventStreamClients, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 || n > maxEventStreamCeiling {
		return 0, fmt.Errorf("LOUPE_EVENT_STREAM_MAX=%q must be a positive integer no greater than %d", raw, maxEventStreamCeiling)
	}
	return n, nil
}
