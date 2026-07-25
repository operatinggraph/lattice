package appsession

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The cross-origin gate on state-changing requests — the kit's CSRF defense.
//
// A session cookie's SameSite=Strict stops a cross-SITE request from carrying
// it, but cookies are scoped by HOST, not by port: every app bound to
// 127.0.0.1 shares one cookie jar host, so loupe :7777, loftspace :7788 and
// clinic :7799 are same-SITE to one another. A page served by any one of them
// can POST to another with `credentials: 'include'` and the victim app's
// session cookie rides along, cross-ORIGIN, with SameSite honored. Same for a
// browser that ignores SameSite, and for a co-hosted deployment behind one
// public hostname.
//
// So a request must prove it came from the app's OWN origin. Two independent
// signals say so, and both must hold:
//
//   - Sec-Fetch-Site, when the browser sends it, is authoritative, read as a
//     resource-isolation policy: "same-origin" (this app's own page) and "none"
//     (no initiator — a typed URL or bookmark, which no page can forge) are
//     admitted, and so is a cross-origin TOP-LEVEL GET navigation, an ordinary
//     link between two co-hosted apps. Everything else — any cross-origin
//     subresource or scripted fetch, and any cross-origin state change — is
//     refused. "same-site" is exactly the cross-port case above.
//   - Origin, when present, must name this app. A browser attaches it to every
//     request whose method is not GET/HEAD, so it is what catches a
//     state-changing request from a browser too old to send Sec-Fetch-Site
//     (Safari before 16.4).
//
// A request with NEITHER header is not browser-driven (curl, a Go client, a
// test) and carries no ambient cookie of anyone else's, so it passes.
//
// The gate covers safe methods too, because a GET is not automatically
// side-effect-free: Facet's GET /api/feed acquires the session identity's sync
// engine, which MINTS that identity's credential. A cross-origin GET cannot
// read the response, but a bare `<img src>` on a sibling app's page is a
// same-site subresource that carries the cookie and sends no Origin at all, so
// the side effect alone is the exposure. Fetch Metadata is what distinguishes
// that from a link the user clicked; the honest limit is a browser that sends
// no Sec-Fetch-Site, where a cookie-bearing cross-origin subresource GET is
// indistinguishable from a same-origin one.

// PublicOrigin is the exact external origin an app is served at, declared by
// the operator rather than detected.
//
// Everything a process derives from its BIND address is wrong once a
// TLS-terminating reverse proxy sits in front: the browser's Origin names the
// public site while the bind stays loopback. Two derivations then fail in
// opposite directions —
//
//   - CrossOriginBlocked fails CLOSED: the proxied Origin matches neither
//     loopback nor the bind host, so every state-changing request 403s.
//   - the session cookie's Secure flag fails OPEN: a loopback bind reads as
//     "not public", so the cookie ships without Secure over a public HTTPS
//     site.
//
// A process cannot tell the proxy's requests from a direct local caller's, so
// the origin is declared. Unset leaves both paths exactly as a loopback
// deployment needs them.
type PublicOrigin struct {
	hostname string // normalized: lowercased, unrooted
	port     string // always populated; 443 when the declaration omits it
}

// String renders the origin back in wire form, for log lines and errors. An
// IPv6 host is re-bracketed: hostname holds the unbracketed form that
// url.Hostname returns, and concatenating that raw would print
// "https://::1:8443" — neither a parseable URL nor recognizably what the
// operator configured, which is the worst thing to show someone debugging a
// 403.
func (p *PublicOrigin) String() string {
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

// normalizeOriginHost canonicalizes a hostname for comparison: lowercased,
// with a single FQDN-rooting trailing dot removed. "app.demo.example." and
// "app.demo.example" address the same site and a browser sends the form the
// user typed, so treating them as different origins would 403 every write with
// no boot-time signal.
func normalizeOriginHost(h string) string {
	return strings.ToLower(strings.TrimSuffix(h, "."))
}

// originComponents validates an absolute https origin and returns its
// normalized host and 443-defaulted port.
//
// Both ParsePublicOrigin and PublicOrigin.matches go through this one function
// deliberately: if the declaration side enforced rules the comparison side did
// not, the two would disagree about what "is this origin" means, and the looser
// side is the one an attacker reaches.
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
	host = normalizeOriginHost(u.Hostname())
	if host == "" {
		return "", "", errors.New("has no host")
	}
	// normalizeOriginHost unroots exactly one trailing dot, so anything still
	// ending in one is not a hostname a browser can put in an Origin — and the
	// same is true of an empty label anywhere ("app..example"). Accepting either
	// would boot a declaration nothing can ever equal: a permanent 403 on every
	// write, which is the failure the port range-check below also exists to
	// prevent.
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return "", "", fmt.Errorf("has an empty label in its host (%q)", host)
		}
	}
	// url.Parse only checks that a port is all digits, with no range check, so
	// ":0" and ":99999" parse. Accepting one would boot a declaration no
	// browser Origin can ever equal: a permanent 403 on every write.
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

// ParsePublicOrigin parses the <PREFIX>_PUBLIC_ORIGIN declaration envName
// names. Empty yields (nil, nil) — the undeclared loopback posture.
//
// Fail-closed: anything malformed refuses to boot rather than reading as
// undeclared. A typo that silently dropped the declaration would take the
// origin gate and the Secure cookie down together, on the one deployment shape
// where both matter. The scheme must be https for the same reason: a Secure
// cookie does not survive plain HTTP, so an http:// public origin is a posture
// this kit cannot honor rather than a weaker one to accept quietly.
func ParsePublicOrigin(envName, raw string) (*PublicOrigin, error) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return nil, nil
	}
	host, port, err := originComponents(v)
	if err != nil {
		return nil, fmt.Errorf("%s=%q %w; it must be the exact external origin the app is served at, "+
			"e.g. https://loftspace.demo.example", envName, raw, err)
	}
	return &PublicOrigin{hostname: host, port: port}, nil
}

// matches reports whether an Origin header names exactly this public origin,
// compared componentwise: scheme https, hostname equal, port with 443
// defaulting on both sides (browsers omit the default port).
//
// It deliberately does NOT consult the request's Host header. Equality against
// a constant configured at boot is strictly stronger than the Origin↔Host
// agreement the loopback branch needs, and it keeps the gate's DNS-rebinding
// hardening intact: under rebinding the attacker's Origin carries the
// attacker's own hostname, which equals neither loopback, the bind host, nor
// this.
//
// A nil receiver (nothing declared) matches nothing, so callers need no
// separate nil check. Comparison is plain equality over already-normalized
// values, deliberately NOT strings.EqualFold: EqualFold does full Unicode
// simple-case folding, under which U+017F (ſ) folds to "s", so a declared
// "smart.example" would match an Origin of "ſmart.example". No browser emits
// that (IDN is punycoded before Origin is set), but a host comparison should
// not be broader than the identity it asserts.
func (p *PublicOrigin) matches(rawOrigin string) bool {
	if p == nil || rawOrigin == "" {
		return false
	}
	host, port, err := originComponents(rawOrigin)
	if err != nil {
		return false
	}
	return host == p.hostname && port == p.port
}

// stateChanging reports whether a method can change state. GET/HEAD/OPTIONS are
// the methods the HTTP spec defines as safe; every kit and app handler enforces
// its own method anyway, so an unlisted method (PUT, PATCH, DELETE, or anything
// exotic) is treated as changing state.
func stateChanging(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// metadataAdmits applies the resource-isolation policy to a request's
// Sec-Fetch-Site, which the caller has already found present.
//
// The one cross-origin shape it admits is a TOP-LEVEL GET navigation: a link,
// bookmark or redirect that lands the user on a page. Refusing those would break
// ordinary navigation between the co-hosted apps.
//
// Top-level is established with Sec-Fetch-Dest, not Sec-Fetch-Mode alone.
// `navigate` is sent for a NESTED navigation too, so mode alone admits
// `<iframe src="http://127.0.0.1:7788/…">` on a co-hosted app's page: same-site,
// so the victim app's session cookie rides (SameSite=Strict is site-scoped and
// port-agnostic), RequireSession authenticates, and the authenticated app
// renders inside a hostile frame — with every side-effecting GET it exposes
// reachable from there, such as the /api/feed credential mint this file's header
// already names. Sec-Fetch-Dest is the only header separating `document` from
// `iframe`/`frame`/`object`/`embed`.
func metadataAdmits(site string, r *http.Request) bool {
	switch strings.ToLower(site) {
	case "same-origin", "none":
		return true
	case "same-site", "cross-site":
		return !stateChanging(r.Method) &&
			strings.EqualFold(r.Header.Get("Sec-Fetch-Mode"), "navigate") &&
			strings.EqualFold(r.Header.Get("Sec-Fetch-Dest"), "document")
	}
	// An unknown or empty token is a value this policy cannot reason about;
	// fail closed rather than guess it is benign.
	return false
}

// OriginGate is the cross-origin decision on its own, with no session surface
// attached. It is what an app that does NOT use this kit's Manager — the Loupe
// console, whose operator auth is its own — calls to get exactly the gate the
// vertical apps get, instead of keeping a copy that drifts.
//
// It reports the refusal reason rather than writing a response, so each caller
// renders its own error shape. Manager.CrossOriginBlocked is the thin wrapper
// that writes the kit's.
type OriginGate struct {
	// PublicOrigin is the declared external origin of a proxied deployment;
	// nil is the undeclared loopback posture.
	PublicOrigin *PublicOrigin
	// BindHost is the host the app listens on, accepted as an origin the app is
	// legitimately served from (the warned-about non-loopback opt-in). Empty
	// means only loopback and PublicOrigin qualify.
	BindHost string
}

// Blocked reports whether r came from somewhere other than this app's own
// origin, and why. RequireSession calls it ahead of everything else, including
// the auth-exempt session endpoints: a forced login or logout is a state change
// a hostile page must not be able to trigger either.
//
// Two independent signals say a request came from the app itself, and both must
// hold. Sec-Fetch-Site, when the browser sends it, is authoritative, and is what
// catches a cross-origin GET — which no Origin header would, since a browser
// attaches Origin only to methods other than GET/HEAD. Origin, when present,
// must name this app, which is what catches a state change from a browser too
// old to send Fetch Metadata (Safari before 16.4). A request with NEITHER header
// is not browser-driven (curl, a Go client, a test), carries nobody's ambient
// cookie, and passes.
//
// Matching Origin against r.Host alone is rebindable — under DNS rebinding both
// headers carry the attacker's name and agree by construction — so the Origin's
// host must ALSO be one the app is legitimately served from: a loopback host,
// the explicitly-configured bind host, or the declared public origin. Origin
// "null" (a sandboxed iframe, some redirects) has no host and fails closed.
//
// Honest limit: browsers send Sec-Fetch-* only from a potentially-trustworthy
// origin. Loopback qualifies, and so does anything behind a declared HTTPS
// public origin — but a plain-HTTP non-loopback bind gets no Fetch Metadata at
// all, leaving only the Origin check, where a cross-origin subresource GET is
// indistinguishable from a same-origin one.
func (g OriginGate) Blocked(r *http.Request) (reason string, blocked bool) {
	// Presence of the header KEY is what says "this browser speaks Fetch
	// Metadata", so an empty value blocks rather than falling through to the
	// weaker Origin check — a GET carries no Origin, so falling through would
	// pass it unconditionally. More than one value is a shape no browser
	// produces (Sec-* is a forbidden header name), so it is refused rather than
	// resolved by taking the first, which Header.Get would do silently.
	if vals, ok := r.Header["Sec-Fetch-Site"]; ok {
		if len(vals) != 1 || !metadataAdmits(vals[0], r) {
			return "cross-origin request blocked (Sec-Fetch-Site " + strings.Join(vals, ", ") + ")", true
		}
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return "", false
	}
	if g.PublicOrigin.matches(origin) {
		return "", false
	}
	// Both scheme forms of r.Host are accepted. Pinning the scheme to the
	// connection (r.TLS) would 403 every write on a non-loopback bind behind a
	// TLS-terminating proxy that preserves Host — the browser's Origin says
	// https while the proxied hop arrives as plain http — and buys nothing: the
	// host must still be loopback or the configured bind host, which is where
	// the rebinding hardening lives. Neither arm consults X-Forwarded-Proto; a
	// forwarding header is set by whoever spoke to us and is never trusted.
	if origin == "http://"+r.Host || origin == "https://"+r.Host {
		if u, err := url.Parse(origin); err == nil {
			if h := normalizeOriginHost(u.Hostname()); h != "" {
				if IsLoopbackHost(h) || (g.BindHost != "" && h == normalizeOriginHost(g.BindHost)) {
					return "", false
				}
			}
		}
	}
	return "cross-origin request blocked (Origin " + origin + ")", true
}

// CrossOriginBlocked applies this app's OriginGate, writing the 403 when the
// request is refused.
func (m *Manager) CrossOriginBlocked(w http.ResponseWriter, r *http.Request) bool {
	reason, blocked := OriginGate{
		PublicOrigin: m.cfg.PublicOrigin,
		BindHost:     m.cfg.BindHost,
	}.Blocked(r)
	if blocked {
		m.writeError(w, http.StatusForbidden, reason)
	}
	return blocked
}
