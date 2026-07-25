package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/operatinggraph/lattice/internal/appsession"
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

// The console's declaration is parsed by the shared kit (appsession.
// ParsePublicOrigin), and the origin gate itself is appsession.OriginGate — the
// same decision the vertical apps get. Loupe kept its own copy of both until the
// kit exposed a gate independent of its session Manager; the copy had already
// drifted (it accepted an empty host label the kit rejects), which is the
// argument against keeping a second one.

// sessionCookieSecure computes the session cookie's Secure flag: set when a
// public origin is declared (https by construction, appsession.ParsePublicOrigin)
// or the bind is non-loopback.
//
// The declaration term is the load-bearing one. Derived from the bind alone —
// which is what this used to be — it fails OPEN in exactly the deployment shape
// this posture exists for: a loopback bind behind a TLS-terminating proxy reads
// as "not public", so the session cookie ships without Secure on a public HTTPS
// site. It lives here as a named function rather than inline at the one call
// site so a test can pin the derivation itself, not just the cookie plumbing.
func sessionCookieSecure(origin *appsession.PublicOrigin, bindHost string) bool {
	return origin != nil || !appsession.IsLoopbackHost(bindHost)
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
