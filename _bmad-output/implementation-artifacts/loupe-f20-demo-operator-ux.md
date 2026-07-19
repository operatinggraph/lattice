# Loupe F20 — Hosted-demo read-only operator

**Status:** ✅ adjudicated (Winston, 2026-07-19 — Andrew-delegated for the Loupe program).
§6–§7 (the F20.5 / F20.2 build designs) added + adjudicated same day, second pass (§4.1).
Lane: [backlog/loupe.md](../planning-artifacts/backlog/loupe.md). Companion:
[deploy/demo/README.md](../../deploy/demo/README.md) (the hosted-demo deployment as it stands today).

**Exposure is Andrew-gated.** This design covers what must be *true* before Loupe can be a public
demo surface, and builds the Loupe-side half. Actually pointing a subdomain at the console is a
separate, explicit decision at the demo's public-launch phase — nothing here exposes anything.

## 1. What Andrew asked for

Loupe in the public demo as the behind-the-scenes view: a `demoOperator` role stripped to
inspect-only grants, so **every write surface is capability-denied at the platform** — proving "even
the console is capability-scoped" live, in front of a visitor. Plus a one-tap demo login mirroring
Facet's persona posture, its own subdomain, and a visitor disclaimer.

Today the demo deliberately does **not** expose Loupe: `deploy/demo/README.md` says "the operator
inspector is deliberately not exposed — reach Loupe via `ssh -L 7777:127.0.0.1:7777`". F20 is the
work that would make exposing it defensible.

## 2. Grounding — what the console actually does today

Read against `cmd/loupe` at `4b2e5946`.

- **One front door.** `srv.requireOperator(mux)` (`main.go:191`) gates *every* route — static UI and
  `/api/*` alike — except `/login` and the three credential-exchange endpoints.
- **One named operator.** `verifyOperatorToken` (`readauth.go:352`) denies any token whose
  `ActorID != s.operatorActorKey`. The console is a *named* identity's login, not "anyone the
  trusted key will sign". `operatorActorKey` comes from `LOUPE_OPERATOR_ACTOR_KEY`, **defaulting to
  the bootstrap `adminActor`** (`main.go:150`).
- **The minter is fixed-subject.** `POST /api/operator/dev-token` (`readauth.go:381`) mints for the
  configured `operatorActorKey` only, never a caller-supplied subject, and sets the session cookie —
  exactly the shape a one-tap demo login needs. Point `LOUPE_OPERATOR_ACTOR_KEY` at the demo identity
  and `/login`'s existing dev-login button *is* the persona card — but it does not work behind a
  proxy as things stand; see §2.3.
- **Writes are uniformly POST/DELETE.** Every mutating route method-gates, so "not a read method"
  catches every write. It is not the converse, though — see §2.2.

### 2.1 ⚠️ The finding that shapes this design

`setupOperatorAuth(logger, isLoopbackHost(bindHost))` (`main.go:162`, `readauth.go:127`) refuses
dev-auth off a loopback bind, with the stated rationale: "a misconfigured non-local bind with
dev-auth would let any network caller mint itself an operator token."

**Behind a reverse proxy that guard does not hold.** The demo binds Loupe to `127.0.0.1` and fronts
it with Caddy. The bind *is* loopback, so dev-auth is permitted — while every request arriving is a
public visitor's. The check tests the **bind host, not the peer**. So under the F20 deployment:

> any internet visitor can `POST /api/operator/dev-token` and be minted the console's fully
> configured operator credential.

This is not a bug to fix — it is F20's actual thesis, made explicit. In the demo that outcome is
**intended**: the one-tap login is *supposed* to hand every visitor the operator credential. What
makes it safe is that the credential names an identity whose **platform grants permit nothing but
reads**. Which yields the design's governing rule:

> **The security boundary is the platform's capability grants on the demo identity. Nothing in
> Loupe's own process is a boundary.** Loupe-side read-only enforcement is defense in depth and a
> UX contract — never the thing being relied on.

Everything below follows from taking that sentence literally — including, per §2.2, knowing exactly
where it stops being true.

### 2.2 ⚠️ Where the grants are NOT the boundary (adversarial review, 2026-07-19)

The rule above holds for the write surface: every op-submit relays through the Gateway under the
visitor's own operator token, and control-plane mutates carry the operator actor, so the platform
decides. Three-layer review found two places it does **not** hold, both caught before ship:

1. **Reveals ride reads.** `GET /api/objects/<oid>?decrypt=true` (`objects.go:456`) unwraps the CEK
   and serves plaintext. It is a GET, so a method rule never sees it — *and* `objectcrypto.UnwrapKey`
   issues the vault RPC on **Loupe's own NATS credentials with no `Lattice-Actor` header**, so the
   demo identity's grants are not consulted at all. A demo visitor could have read decrypted PII with
   nothing in the stack denying it. **Fixed in F20.1** at the decrypt branch's own condition. This is
   a place where Loupe's process *is* the only control — recorded so a later fire does not relax it
   believing the grants have its back.
2. **The read surface is not grant-narrowed.** Every read handler serves from Loupe's own credentials
   (all of Core KV, every vertex, the shred roster). "Inspect-only grants" constrain what the demo
   operator can *do*, not what the console will *show*. The banner copy was corrected to promise only
   what the process enforces — writes and reveals refused — rather than implying narrowed reads.

Corollary for §3.1: the demo identity's grants must be narrow because the minted token is a **real
actor JWT** the visitor holds and can replay directly at the Gateway, entirely outside this console.
That is by design, and it is why Layer 1 is the boundary — but only for what the platform mediates.

### 2.3 ⚠️ The one-tap login does not survive a reverse proxy (unowned, blocks F20.4)

`crossOriginBlocked` (`server.go:168`) requires the request's `Origin` host to be loopback or exactly
the configured bind host. All three credential-exchange endpoints call it. Behind Caddy the browser
sends `Origin: https://loupe.demo.example` while `bindHost` is `127.0.0.1` — neither match, so
**every login and logout 403s** and no visitor can get in. (The §2.1 minting hazard survives this: a
curl client sends no `Origin` at all.)

Related, same deployment: `setOperatorSessionCookie` sets `Secure` from `!isLoopbackHost(bindHost)`
(`readauth.go:236`), so a loopback bind behind a TLS proxy ships the session cookie **without
`Secure`** on a public HTTPS site.

Both need a configured public-origin/trusted-proxy posture. Not built here — changing a
rebinding-hardened gate belongs in its own fire, not bundled with the demo posture. Filed as **F20.5**,
added to the exposure checklist, and **designed in §6**.

## 3. Design

Three layers, deliberately unequal in weight.

### 3.1 Layer 1 (the boundary, cross-lane) — the `demoOperator` grant scoping

A demo identity holding read grants only: no `InstallPackage`/`UpgradePackage`/`UninstallPackage`,
no `ReviewProposal`, no `ShredIdentityKey`, no `RevokeActor`, no `lattice.vault.decrypt`, no
generic op-submit. The F15 precedent is `packages/console-operator` (its own read-grant lens +
persisted identity, wired by `up-full`) — the demo role is that, minus every write.

**Cross-lane:** `packages/**` is not the Loupe lane. Filed to the Lattice lane; F20's exposure
gate depends on it. **Until it exists, Loupe must not be exposed** — see §3.2's boot guard, which
enforces exactly that precondition rather than documenting it.

### 3.2 Layer 2 (in-lane, built now) — `LOUPE_DEMO_MODE`

A process-wide demo posture in `cmd/loupe`, default off.

**(a) Fail-closed write denial.** One middleware wrapping the mux, **default-deny by method**: every
request that is not `GET`/`HEAD` is refused `403` with a stable, visitor-legible reason, except a
three-path credential-exchange allowlist (`dev-token`, `session`, `logout` — a visitor must be able
to log in and out).

Method-based rather than a path list because it is **fail-closed for routes that do not exist yet**:
a write endpoint added by a future fire is denied in demo mode without anyone remembering to update a
list. A path allowlist fails open on exactly that case.

It over-denies, accepted deliberately: the control plane tunnels three pure reads through POST (loom
`inspect`, refractor `health` and `validate`), so a demo visitor loses those inspection replies —
some of the more compelling "behind the scenes" surfaces. Restoring them means teaching the rule
which control ops are reads, a classification living in `control.go` that would fail **open** if it
drifted. Tracked as F20.2 rather than traded for a stale allowlist.

**Reveals are a separate axis** and are denied at their own call sites, not by this rule — see §2.2.

**(b) A boot guard, not a warning.** Demo mode **refuses to start** unless
`LOUPE_OPERATOR_ACTOR_KEY` is set explicitly *and* differs from the bootstrap `adminActor`.

This is the load-bearing piece. Without it, `LOUPE_DEMO_MODE=1` on a stock stack would silently run
the demo posture **as the bootstrap admin** — read-only in Loupe's own process, and omnipotent to
anything that reaches the platform another way. Per the house rule that a confinement guarantee must
never rest on an advisory precondition, the precondition that "the configured operator is a
scoped demo identity" is enforced at boot, and the process exits if it is not met. It is the
mechanism by which §3.1 cannot be skipped.

*(The guard checks the identity is distinct and explicit — it cannot verify the grants are
actually narrow, which only the platform knows. It closes the stock-stack footgun, not every
misconfiguration.)*

The flag itself is parsed fail-closed for the same reason: `LOUPE_DEMO_MODE` set to anything not
recognizable as a boolean (`=enabled`, `=Y`) **refuses to boot** rather than reading as false. A typo
that silently disables the posture also silently skips this guard, and the result is a fully writable
admin console on a public URL — the exact outcome the guard exists to prevent.

**(c) Honesty surface.** `GET /api/demo` reports the posture, and the shell renders a persistent
visitor banner: this is a live operator console, in read-only demo mode, write actions are denied by
the platform's capability grants. The banner is the disclaimer Andrew asked for; it states the
*platform* is the reason, not a UI toggle — which is both true and the point being demonstrated.

### 3.3 Layer 3 (cross-lane, launch-gated) — exposure

A `deploy/demo` Caddy site block for the Loupe subdomain, and the README's "deliberately not
exposed" paragraph rewritten. `deploy/**` is out of the Loupe lane; and exposure is Andrew's call at
public-launch regardless. **Not built.**

## 4. Adjudication (Winston, 2026-07-19)

Forks resolved:

1. **Deny by method vs. by path** → **by method** (§3.2a). Fail-closed for future routes; the
   no-read-only-POST enumeration makes it exact today.
2. **Warn vs. refuse to boot on an unscoped operator** → **refuse** (§3.2b). A demo posture whose
   safety rests on an env var someone remembered to set is not a posture.
3. **Suppress write affordances vs. let them 403** → **let them 403 for now**, with the banner
   setting expectation. The 403 is honest and, at a demo, arguably the more persuasive artifact —
   the visitor *sees* the denial. Suppression is polish, not safety; filed as F20.2 rather than
   ballooning a security-adjacent fire across ~10 view modules.
4. **Does Loupe's read-only mode count as the guarantee?** → **No for writes**, which the platform
   mediates (§2.1, §3) — but **yes for reveals**, where the vault RPC carries no actor and this
   process is the only control (§2.2). Recorded in both directions so a later fire neither promotes
   the middleware to a guarantee nor relaxes the reveal denial assuming grants cover it.

Deliberately **not** done: "fixing" the loopback/reverse-proxy gap in `setupOperatorAuth`. It is
correct for the dev posture it guards, and under F20 the minting-for-everyone behavior is intended.
Narrowing it would break the one-tap login this design depends on. (§6.4 adds the complementary
boot coupling: a *declared* public origin with dev-auth enabled refuses to run outside demo mode.)

### 4.1 Adjudication, second pass (Winston, 2026-07-19) — the §6/§7 build designs

F20.5 was filed as a problem statement (§2.3), not a design; F20.2 carried an open fail-open
classification question. Both are resolved below; forks adjudicated:

1. **Where the rate limiter lives** → **Loupe-side** (§6.5). The apt-installed Caddy has no HTTP
   rate-limit handler (third-party xcaddy module — verified upstream, see the Caddy row in
   [docs/vendors.md](../../docs/vendors.md)), so an edge limiter means a custom Caddy build in the
   bootstrap; and a Loupe-side limiter is in-lane, unit-testable, and holds under any future proxy.
   F20.4 may *add* an edge limiter later; it would be complementary, not a replacement.
2. **Detect the proxy vs. declare it** → **declare** (§6.1). A process behind a reverse proxy cannot
   distinguish the proxy's requests from a direct local caller's; `LOUPE_PUBLIC_ORIGIN` is the
   explicit declaration, every §6 behavior keys off it, and unset means byte-for-byte today's
   behavior.
3. **Dev-auth on a declared public origin** → **demo mode required, refuse boot otherwise** (§6.4).
   The same exposure `setupOperatorAuth` already refuses on a non-loopback *bind*, arriving through
   the proxy door instead — it gets the same refusal, keyed on the declaration.
4. **SSE cap on a public URL** → **raise in demo (32), env-tunable, visitor-legible message** (§6.6)
   rather than keep-at-4 or per-peer accounting. The live pulse is the demo's most compelling
   surface; a global bound is the resource protection, and per-peer bookkeeping buys little at this
   scale.
5. **`/login` disclaimer transport** → **server-side injection** (§7.3), not an unauthenticated
   `/api/demo` exemption. A static string does not justify widening the pre-auth API surface.

Vendor grounding for §6: Caddy's default forwarded-header semantics were verified against
<https://caddyserver.com/docs/caddyfile/directives/reverse_proxy> (incoming `X-Forwarded-*` from an
untrusted client is **ignored** — anti-spoofing — and `X-Forwarded-For` is set from the immediate
peer; `Host` passes through), and the rate-limit module status against its own repo. Caddy now has a
row in `docs/vendors.md`.

## 5. Fires

| Fire | Scope | Lane | State |
|---|---|---|---|
| **F20.1** | §3.2 — `LOUPE_DEMO_MODE`: method default-deny middleware, fail-closed flag + boot guard, reveal denial (§2.2), `/api/demo` + visitor banner | Loupe | ✅ SHIPPED 2026-07-19 |
| **F20.2** | §7 — demo polish: suppress write affordances per view, restore the three read-only control POSTs, `/login` disclaimer | Loupe | 📋 build-ready (§7; not a safety item; after F20.5) |
| **F20.3** | §3.1 — the `demoOperator` grant package | Lattice (cross-lane) | filed (lattice.md row, 📋 ready) |
| **F20.4** | §3.3 — Caddy subdomain + README | deploy (cross-lane) | 🚧 Andrew-gated on public launch |
| **F20.5** | §6 — the public-origin posture: `LOUPE_PUBLIC_ORIGIN` (origin gate + `Secure` cookie), the dev-auth⇒demo boot coupling, the credential-exchange rate limiter, the SSE cap posture. **Blocks F20.4** — without it no visitor can log in | Loupe | 📋 build-ready (§6; build first) |

**Exposure checklist** — every line must hold before Loupe is reachable publicly:

1. F20.3 shipped, the demo identity provisioned, and its grants spot-checked live (a denied write
   observed against the deployed stack, not inferred).
2. F20.5 shipped — otherwise login 403s and the session cookie is not `Secure`.
3. `LOUPE_DEMO_MODE=1` with `LOUPE_OPERATOR_ACTOR_KEY` naming the demo identity — the boot guard
   proves both, and a malformed flag now refuses to boot rather than failing open — and
   `LOUPE_PUBLIC_ORIGIN` set to the exact public origin (§6.1; the origin gate, the `Secure`
   cookie, and the limiter's peer keying all key off it).
4. The credential-exchange rate limiter observed live: repeated mints from one client answer `429`
   (§6.5 ships it in-lane with F20.5; this line is the deployed proof, not a decision).
5. The live feed observed with more than four concurrent visitors (§6.6 resolves the cap: demo
   default 32, `LOUPE_EVENT_STREAM_MAX` override, visitor-legible at-cap message).
6. Andrew's explicit go-ahead. Exposure is his call, not a fire's.

## 6. F20.5 — the public-origin posture (build design)

Everything the console's browser-facing machinery derives from the **bind host** must instead honor
a **declared public origin** when one is configured — without changing anything when it is not. All
of §6 is gated on the new env being set; unset, every code path below is byte-for-byte today's.

Grounding (all at `232a6bf1`): the three bind-host derivations are `setupOperatorAuth`'s loopback
gate (`main.go:174` — stays as is, §4/§6.4), `crossOriginBlocked` (`server.go:174` — fails CLOSED
behind a proxy, logins 403), and the session cookie's `Secure` (`readauth.go:236,250` — fails OPEN,
cookie ships without `Secure` on a public HTTPS site). `r.Host` has exactly one consumer
(`crossOriginBlocked`); nothing else in `cmd/loupe` builds URLs from the bind address. Facet has no
same-origin gate at all, so there is no precedent to mirror — the pattern source is Loupe's own
shipped gate (loupe-operator-auth-lift + the F9 same-origin gate), extended.

### 6.1 The declaration: `LOUPE_PUBLIC_ORIGIN`

The exact external origin the console is served at through a TLS-terminating reverse proxy, e.g.
`https://loupe.demo.example`. Parsed at boot, **fail-closed**: the scheme must be `https`, the host
non-empty, and nothing else present (no path, query, fragment, or userinfo; an explicit port is
allowed). Anything malformed — including an `http://` origin — **refuses to boot**, same philosophy
as `demoModeEnabled`: a typo must stop the process, not silently disable the posture. A plain-HTTP
public deployment is not a supported shape (Caddy terminates TLS for free; `Secure` cookies would
not survive it anyway).

### 6.2 The origin gate

`crossOriginBlocked` gains one acceptance branch: the request's `Origin`, parsed, **componentwise
equals the configured public origin** — scheme `https`, hostname (case-insensitive), and port with
443-defaulting on both sides (browsers omit `:443`). The existing loopback/bind-host branch is
untouched, and the new branch does not consult `r.Host` at all: equality against a configured
constant is strictly stronger than the Origin↔Host agreement the local branch needs, and it keeps
the gate's rebinding hardening intact — under DNS rebinding the attacker's Origin carries the
attacker's hostname, which equals neither loopback, the bind host, nor the configured public host.
`Origin: null` still fails closed. Empty-Origin requests still pass (curl semantics unchanged — the
§2.1 minting-for-everyone behavior is the demo's intent, bounded by §6.5).

### 6.3 The `Secure` cookie

`Secure` becomes a server field computed once at construction: set when a public origin is
configured **or** the bind is non-loopback (today's term). Both `setOperatorSessionCookie` and
`clearOperatorSessionCookie` use it. §6.1's https-only rule is what makes this unconditional-when-
declared correct. `HttpOnly` and `SameSite=Strict` are unchanged — the login page and the one-tap
button are same-origin under the proxy, and Strict survives a top-level same-site navigation.

### 6.4 The dangerous-combo boot guard

`LOUPE_PUBLIC_ORIGIN` set **and** `LOUPE_DEV_AUTH` enabled **⇒ `LOUPE_DEMO_MODE` must be on, else
refuse to boot.** A proxied dev-auth console hands the fully-configured operator credential to every
internet visitor (§2.1); that is only a sane posture when the credential is the demo identity whose
grants permit nothing (`demoOperatorGuard` + F20.3) — i.e. demo mode. A *writable* proxied console
must use a real IdP (`LOUPE_JWT_PUBLIC_KEY`), exactly as `setupOperatorAuth` already demands for a
non-loopback bind: same exposure, different door, same refusal. Demo mode behind a public origin
with a real IdP (no one-tap login) remains a valid, unconstrained combination.

Honest limit, stated: Loupe cannot *detect* an undeclared proxy — the declaration is configuration,
and the F20.4 deploy material is what sets it (checklist item 3 ties them). What this guard closes
is the misconfiguration where the declaration exists but the operator forgot what dev-auth means on
a public URL.

### 6.5 The credential-exchange rate limiter (checklist #4, resolved)

A token-bucket limiter applied to **exactly the three `requireOperator`-exempt POST endpoints**
(`dev-token`, `session`, `logout`) — the console's only unauthenticated handlers — running **before
any body read or crypto work**. Two tiers:

- **Per-peer**: 10 requests/min, burst 10. Peer identity: when a public origin is declared, the
  **last** `X-Forwarded-For` entry — Caddy ignores incoming `X-Forwarded-*` from untrusted clients
  (anti-spoofing default, verified upstream — vendors.md) and appends the immediate peer, so under
  the single-hop demo topology the last entry is the real client and is not client-forgeable.
  Undeclared: `RemoteAddr`'s host, today's direct-peer truth.
- **Global ceiling**: 300 requests/min across all peers — the actual abuse bound (RSA signing and
  log churn are the cost being bounded); per-peer is fairness so one noisy client cannot starve the
  login for everyone else.

Denial is `429` with a visitor-legible message. The peer map is bounded (idle-evicted; at capacity a
new peer is admitted under the global ceiling only, never tracked unboundedly). Sizing honesty: the
mint is **fixed-subject** (`readauth.go:402`) — N tokens are the *same one credential*, so the
limiter does not bound credential proliferation (one mint is already full demo capability); it
bounds unauthenticated compute and noise. 10/min/peer is ~30× a real visitor's need (one mint per
30-minute TTL); 300/min global is trivial CPU (~ms per RS256 sign) while capping a flood at
five signs/second.

The authenticated read surface is deliberately **not** limited here — a public read-only console
serves the public; the expensive endpoint (`/api/events/stream`) has its own bound (§6.6), the rest
are cheap KV/lens reads bounded by `natsTimeout`, and an edge-level connection limit is F20.4's
option if real traffic ever demands one.

### 6.6 The event-stream cap posture (checklist #5, resolved)

`maxEventStreamClients` becomes a boot-computed value: `LOUPE_EVENT_STREAM_MAX` when set (integer
> 0; anything else **refuses to boot**, same fail-closed parse rule as every §6 knob), else **32 in
demo mode, 4 otherwise**. The at-cap `streamError` message becomes posture-aware: the operator
message stays "close another Loupe tab"; a demo visitor sees "the live feed is at capacity — try
again in a moment". Bounds: each tail is one ephemeral ordered consumer on `core-events` plus one
SSE goroutine — 32 of them is negligible beside the full stack already on the demo box, and the
live pulse is the single most persuasive behind-the-scenes surface, so starving it at 4 would gut
the demo. Visitor #33 gets a graceful message, not a broken page.

### 6.7 Test strategy + increment

All unit/httptest, no stack: origin parse (rejects http/path/garbage; accepts host and host:port);
gate matrix (public origin allowed; attacker/rebound hostname denied; `null` denied; loopback and
bind-host branches unchanged); cookie `Secure` matrix (declared/undeclared × loopback/non-loopback);
the §6.4 combo refusal; limiter (per-peer 429 at 11th, global ceiling, XFF-last keying only when
declared, RemoteAddr otherwise, eviction bound); SSE knob (default 4, demo 32, override, malformed
refusal). Plus the checklist's live proofs post-deploy. One fire — the pieces share the declaration
plumbing and none is independently shippable to the demo without the others.

## 7. F20.2 — demo polish (build design)

Three mechanisms, none safety-bearing (the §3.2 middleware and the platform grants stay the
enforcement); ships after F20.5.

### 7.1 Restore the three read-only control POSTs

The concern that deferred this (§3.2a) was a classification that fails open on drift. Resolved by
making omission deny: `controlComponent` gains a `readOnlyOps` set **co-located with `mutateOps` in
`control.go`** — loom `{inspect}`, weaver `{}` (all three of its ops mutate), refractor
`{health, validate}` (`rebuild`/`pause`/`resume`/`delete` stay denied). The demo rule gains one
carve-out: a POST matching `/api/control/<comp>/<name>/<op>` — parsed with `splitNonEmpty`, the
**same helper `handleControl` routes with** (`server.go:446`), so gate and handler cannot disagree
about which op executes — is allowed iff the shape is exactly three segments and
`op ∈ readOnlyOps[comp]`. Everything else stays method-denied.

Drift analysis, both directions: a future op added to `mutateOps` alone is **denied** in demo
(omission = deny — the fail-open worry is structurally gone); misclassifying a mutate as read-only
requires an affirmative wrong entry in the same table a reviewer reads, and two tests pin it — an
invariant test (`readOnlyOps ⊆ mutateOps`) and an exact-expected-set test, so widening the set is a
conscious, test-visible act. The handler still runs `mutateSubject`'s full validation; the gate only
ever *narrows*.

### 7.2 Suppress write affordances

The shell already fetches `/api/demo` for the banner; expose that posture once in client state (no
per-view refetch). Contract: any element that initiates a platform write or a reveal carries a
shared marker (a `data-demo-hide` attribute or helper), and one shell-level CSS rule hides marked
elements when the posture is on. Views to sweep (the Steward enumerates precisely): op submit
(`#/op` + vertex-page prefills), package install/upgrade/uninstall, lens delete, component-page
control mutates (pause/resume/rebuild/delete/disable/enable/revoke — **not** the three §7.1 restored
reads, which now work and stay visible), review approve/reject/apply, the Graph explorer's reveal
and shred confirms, object upload/detach. Drift direction is cosmetic-only and already adjudicated
acceptable (§4 fork 3): an untagged new affordance stays visible and its click 403s honestly —
never a security regression, since enforcement never moved into the UI. The §2.2 reveal denial is
**untouched** by this fire.

### 7.3 The `/login` disclaimer

`handleLoginPage` injects a demo block server-side when the posture is on: `login.html` gains a
marker comment the handler string-replaces with static copy (what this demo is, writes are denied
by the platform's grants) and demo-appropriate button labeling ("Enter the read-only demo"). No new
unauthenticated endpoint, no client fetch, works with JS disabled, and the page stays self-contained
(no new `requireOperator` exemptions — the §4.1 fork-5 rationale).

### 7.4 Test strategy

Unit: the §7.1 gate matrix (each restored op allowed in demo, every weaver op + refractor `rebuild`
et al. still denied, malformed/short/long paths denied) + the two classification-pin tests; goja
logic tests for the affordance helper's posture gating; an httptest asserting `/login` contains the
disclaimer exactly when demo mode is on. Live: the restored loom `inspect` / refractor `health`
exercised as a demo visitor against the dev stack.
