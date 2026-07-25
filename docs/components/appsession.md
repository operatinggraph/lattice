# appsession — the shared browser-session kit

**Component reference** | Audience: operators + implementers

> `internal/appsession` is a **platform-internal library**, not a binary — it has no frozen interface
> contract of its own. It builds to Contract #11 (external actor authN — subject binding) and reaches
> the platform only through the Gateway's own external `/v1/actor` door. Its design of record is
> `_bmad-output/implementation-artifacts/persona-worlds-design.md` §5 (Fire P2). Update this page in
> the same commit as the code; drift between page and code is a documentation bug.

---

## Overview

Every Lattice front-end binary signs users in the same way, and this kit is that way: a login page, a
demo-posture login endpoint fenced to a persona list, an HttpOnly session cookie carrying the **same
JWT the Gateway already verifies on every write**, a sliding-session refresh endpoint, logout, and the
middleware that resolves each request to a signed-in identity.

Adopters: `cmd/facet`, `cmd/loftspace-app`, `cmd/clinic-app`. Each wires the kit in `run()` and hands
its whole route table to `RequireSession` on one line (`registerRoutes` in each `server.go`), so the
kit's gates apply to every route an app has, including its own.

**The kit owns the capability of signing in; each app owns the UX around it** — its own login page
bytes, its own extra exempt paths, its own post-logout cleanup. It never reads a platform bucket: its
only outbound call is to the Gateway's `/v1/actor`, so an adopting app gains no new data-plane
dependency.

**What it is not.** The session cookie is not the security boundary. The Gateway verifies every write
independently, and Refractor's RLS bounds every protected read; the cookie only selects **which
already-entitled identity** a request runs as.

---

## The two auth postures

| | Demo (dev auth) | Production (verify-only) |
|---|---|---|
| Configured by | `<PREFIX>_DEV_AUTH=1` | `<PREFIX>_JWT_PUBLIC_KEY` + `<PREFIX>_JWT_ISSUER` |
| `Signer` | non-nil — mints in-process | **nil** |
| `POST /api/dev-login` | mints + sets the cookie | 404 |
| `POST /api/session/refresh` | mints a fresh token | 404 — nothing here minted the cookie |
| Verifier | shared checked-in dev key | the external IdP's PEM key, pinned to its issuer |

`NewDevSigner` refuses dev auth outright off a **loopback bind**: an in-process minter signs whatever
subject its caller names, so it must never be reachable remotely. `NewAuthenticators` builds the strict
per-request verifier plus the grace-tolerant one that backs only the refresh endpoint (`RefreshGrace`).

Neither configured ⇒ every verifier is nil and a session-gated read fails closed, which is the correct
default for an unprovisioned boundary.

**Known gap.** In the verify-only posture nothing in-process can place the cookie: `setCookie` runs only
under a non-nil `Signer`. The posture is reachable for *verification* but not for *sign-in* — how an
externally-issued token becomes a session cookie (a token-exchange endpoint, an OIDC code flow, or a
proxy that sets it) is an open design question, tracked on the Lattice board.

---

## The cross-origin gate (CSRF)

`RequireSession` runs `CrossOriginBlocked` **ahead of everything else, including the auth exemptions** —
the session endpoints are exempt from *needing* a session, not from proving their origin; a forced login
or logout is a state change too.

**Why the cookie's own flags are not enough.** The cookie is `HttpOnly` + `SameSite=Strict`, but cookies
are scoped by **host, not port**. Every app bound to `127.0.0.1` shares one cookie jar host, so
loupe :7777, facet :7810, loftspace :7788 and clinic :7799 are same-**site** to one another: a page
served by any one of them can call another with `credentials: 'include'` and the victim app's session
cookie rides along, cross-**origin**, with SameSite honored.

Two independent signals must both hold:

1. **`Sec-Fetch-Site`** (authoritative when the browser sends it), read as a resource-isolation policy:
   - `same-origin` — this app's own page — and `none` (no initiator: a typed URL or bookmark, which no
     page can forge) are admitted.
   - A cross-origin **top-level GET navigation** (`Sec-Fetch-Mode: navigate`) is admitted: an ordinary
     link between two co-hosted apps, and the user's own act.
   - Everything else is refused — every cross-origin subresource or scripted fetch, and every
     cross-origin state change. `same-site` is exactly the cross-port case above.
2. **`Origin`**, when present, must name this app. A browser attaches it to every request whose method
   is not GET/HEAD, so it is what catches a state-changing request from a browser too old to send
   Fetch Metadata (Safari before 16.4).

An `Origin` names this app when it matches the **declared public origin**, or when it equals
`<connection scheme>://<Host>` **and** that host is loopback or the configured `BindHost`. The
second condition is the DNS-rebinding hardening: under rebinding both headers carry the attacker's name
and agree by construction. `Origin: null` has no host and fails closed. The scheme comes from the
connection, never from `X-Forwarded-Proto` — a forwarding header is set by whoever spoke to us.

**Safe methods are gated too**, because a GET is not automatically side-effect-free: Facet's
`GET /api/feed` acquires the session identity's sync engine, which *mints that identity's credential*. A
cross-origin GET cannot read the response, but a bare `<img src>` on a sibling app's page is a same-site
subresource that carries the cookie and sends no `Origin` at all — the side effect alone is the exposure.

A request with **neither** header is not browser-driven (curl, a Go client, a test) and carries no
ambient cookie of anyone else's, so it passes.

**Honest limits.** There is no CSRF token: the defense is header-based, so an intermediary that strips
`Origin` and `Sec-Fetch-*` silently restores the old posture, and a browser predating Fetch Metadata
cannot distinguish a cross-origin subresource GET from a same-origin one. There is also no way to exempt
a path from the gate, which a future cross-site POST callback (OIDC `form_post`, SAML POST binding)
would need.

---

## Proxied deployments — `<PREFIX>_PUBLIC_ORIGIN`

Everything a process derives from its **bind address** is wrong once a TLS-terminating reverse proxy
sits in front: the browser's `Origin` names the public site while the bind stays loopback. Two
derivations then fail in opposite directions —

- the cross-origin gate fails **closed**: the proxied `Origin` matches neither loopback nor the bind
  host, so every state-changing request 403s (sign-in included);
- the cookie's `Secure` flag fails **open**: a loopback bind reads as "not public", so the cookie ships
  without `Secure` over a public HTTPS site.

A process cannot tell the proxy's requests from a direct local caller's, so **the origin is declared,
not detected**. `ParsePublicOrigin` is fail-closed — a malformed value refuses to boot rather than
reading as undeclared, since a typo that silently dropped the declaration would take both paths down at
once. It must be `https` (a `Secure` cookie does not survive plain HTTP), with no userinfo, path, query
or fragment, a port in 1–65535, and no empty label in the host — every rule that would otherwise boot a
declaration no browser `Origin` can ever equal.

**A declared origin plus dev auth requires a persona fence** (`<PREFIX>_DEMO_PERSONAS`), enforced in
`New`. The declaration means the app is served to the internet; the minter signs any subject a caller
names. The bind-address check that otherwise keeps the minter local cannot see this shape at all — the
bind *is* loopback, and the proxy is what makes it public. The hosted demo already runs exactly this
fenced posture (`deploy/demo/demo-up.sh`, which reads the host from the `demo-host` marker
`demo-bootstrap.sh` writes).

Postures that work today: a **loopback bind**, and a **declared public origin** behind a proxy. A plain
non-loopback bind is already sessionless for an unrelated reason — `Secure` is set for any non-loopback
bind and none of these apps serves TLS, so the browser never returns the cookie.

---

## Environment (per adopter, `<PREFIX>` = `FACET` / `LOFTSPACE_APP` / `CLINIC_APP`)

| Variable | Effect |
|---|---|
| `<PREFIX>_DEV_AUTH` | `1` enables the in-process minter behind `/api/dev-login`; loopback bind only |
| `<PREFIX>_DEV_PRIVATE_KEY_PATH` | overrides the shared dev signing key path |
| `<PREFIX>_JWT_PUBLIC_KEY` / `_JWT_ISSUER` (+ `_JWT_KID`, `_JWT_AUDIENCE`) | the verify-only production posture |
| `<PREFIX>_DEMO_PERSONAS` | JSON list fencing sign-in to curated identities; required with `_PUBLIC_ORIGIN` + dev auth |
| `<PREFIX>_PUBLIC_ORIGIN` | the exact external https origin a proxied deployment is served at |

---

## Session resolution

`resolve` verifies the cookie and returns the bare identity it authenticates. A cookie that is
**present but does not verify fails closed** — it must never fall through to the boot fallback, or a
merely-expired session would silently become someone else while the UI kept claiming to be the
signed-in user. An **absent** cookie is the only case `FallbackIdentityID` answers (the single-user boot
posture; `ViaCookie` is what a per-user surface checks so a fallback session cannot reach it).

`handleDevLogin` resolves the credential to the identity it is **bound to** via the Gateway's whoami
beat, so signing in with a linked credential opens *that* identity's world; the persona fence applies to
the resolved identity, not just the credential typed in. `handleRefresh` deliberately does not re-run
that resolution — it renews an already-open session, a decision refresh never revisits.

---

## Health

The kit reports no health of its own; each adopting app owns its `health.<app>.<instance>` card
(Contract #5).
