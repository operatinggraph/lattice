# appsession — production sign-in: the OIDC code-flow relying party in the kit

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-07-25 · board row: *[appsession] The
production IdP posture cannot open a session* (`backlog/lattice.md`, Security & trust boundary)

## For Andrew

Today the kit's "production posture" (verify-only JWT) can verify a session cookie but nothing can ever
**set** one — and `POST /api/session/refresh` 404s, which every FE's write path depends on for its raw
bearer (`sessionWriteToken` in every `cmd/*/web/app.js`), so production sign-in **and all writes** are
unreachable. This design makes the kit itself the OIDC **Authorization Code relying party** (the
token-mediating-backend pattern of the IETF OAuth browser-apps BCP): the kit redirects to the deployment's
IdP, exchanges the code server-side, verifies the ID token through the existing shared verifier (Contract
#11 opaque binding — already fully shipped Gateway-side, JWKS poller included), sets it as the session
cookie, and holds the IdP refresh token in an HttpOnly `__Host-` cookie so the existing
`/api/session/refresh` contract keeps working.

**Two things need your eye beyond the ratification itself:**

1. **The fork (resolved, my recommendation):** the RP lives in the **kit** — over SPA-side PKCE (reworks
   all five FE shells, tokens exposed to JS, BCP-inferior) and over an auth proxy (cannot serve the
   browser-direct Gateway/NATS bearer the parity invariant §6.2 requires). §4 has the full trade-off.
2. **One deployment-shaped decision I resolved and want confirmed (§4.2):** **one OIDC client
   registration for all five apps** (five redirect URIs), so a single `aud` pins every token — and the
   **Gateway refuses to boot when an external key source is configured with no `GATEWAY_JWT_AUDIENCE`**.
   Without that pin, *any* ID token from the trusted issuer — including one minted for an unrelated
   relying party on a shared IdP like Google — is a valid Lattice write bearer. That refusal is a
   fail-closed hardening to shipped Gateway code; it cannot affect dev/demo (dev-key posture) but it is a
   posture change on a security boundary, so it is called out rather than slipped in.

**No frozen-contract change.** Builds *to* Contract #11 (§11.2 profile, §11.3 opaque binding, §11.4
resolution). The backlog row's "needs a per-path origin-gate exemption (`form_post`)" premise **dissolves**:
the code flow's callback is a cross-site top-level GET navigation (OIDC Core returns code responses via the
**query string**), which `metadataAdmits` already admits — verified in code, with a test pinning that the
gate still blocks a *scripted* cross-origin fetch of the same path. No gate change, no exemption surface.
(The admitting condition gained a third term after this design was written: a cross-origin navigation must
also carry `Sec-Fetch-Dest: document`, so a NESTED navigation — an iframe pointed at the callback — is
refused. A real IdP redirect is top-level and carries it, so the conclusion is unchanged; a test simulating
the callback must set the header, or it will assert a 403.)

**Size: L** (the row estimated M; the honest size after the §10 adversarial pass is L — three fires).

---

## 1. Problem

`internal/appsession` (design of record: persona-worlds-design.md §5, Fire P2) documents two postures. The
demo posture works end to end. The production posture (`<PREFIX>_JWT_PUBLIC_KEY` + `_JWT_ISSUER`) builds a
verifier but:

- `setCookie` is only reachable from `handleDevLogin`/`handleRefresh`, both gated on a non-nil `Signer`
  (`session.go:394,636`) — with `Signer == nil` nothing in-process can ever place the cookie. 401 everywhere.
- Worse than sign-in: **every FE's write path is dead.** The shells obtain the raw bearer for
  Gateway-direct submits (and Facet's NATS connect) exclusively from `POST /api/session/refresh`
  (`cmd/loftspace-app/web/app.js:274-299`, `cmd/facet/web/boot.mjs:148-158`); in the verify-only posture
  that endpoint 404s. Even a cookie planted by some external means would strand the apps read-only.
- persona-worlds §5 deferred this deliberately: *"the kit is where a real IdP plugs in later (F4)."* This
  design is F4.

Everything downstream of a *token* already ships: the Gateway trusts external IdPs (static PEM dir or live
JWKS with rotation — `cmd/gateway/main.go`, `auth.JWKSPoller`), Contract #11 freezes the opaque
`(iss,sub)` → identity derivation with per-source issuer confinement, first-touch provisioning writes
`.idpBinding` (`gateway.go:626`), revocation and credential→business resolution are profile-complete. The
**only** missing leg is how a browser turns an IdP account into the kit's session cookie.

## 2. Grounding ledger (what exists, verified in code this fire)

| Fact | Where |
|---|---|
| Verify-only posture builds a strict verifier, `ModeOpaque` + pinned issuer + optional audience | `appsession/signer.go:136-164` |
| `setCookie` unreachable without `Signer`; refresh 404s without `Signer` | `session.go:394,636`; `setCookie` called only at `:460,:664` |
| FE write path = `POST /api/session/refresh` → raw token (cookie alone can't serve an `Authorization` header or NATS CONNECT) | `loftspace-app/web/app.js:274-299`, `facet/web/boot.mjs:148-158`, `facet/browserengine.go:145` |
| **The dev posture's cookie carries the *resolved* identity because login RE-MINTS for `U`** | `session.go:437-458` (`:451` re-mint) |
| **App read boundaries consume `appsession.Identity(ctx)` as already-resolved** — stated in their own comments | `cmd/loftspace-app/readauth.go:39-52` (+ clinic/cafe/wellness siblings) |
| Origin gate admits a cross-origin **top-level GET navigation** (`Sec-Fetch-Mode: navigate` **and** `Sec-Fetch-Dest: document`); blocks everything else cross-origin, nested navigations included | `origin.go` `metadataAdmits` / `OriginGate.Blocked` |
| Session cookie is `SameSite=Strict` | `session.go:358-368` |
| `Secure` is conditional on `PublicOrigin != nil \|\| !Loopback` | `session.go:354-356` |
| Gateway trusts external IdPs: `GATEWAY_JWKS_URL`+`_ISSUER` (live rotation) or `GATEWAY_JWT_KEYS_DIR`+`_ISSUER`; **one optional `GATEWAY_JWT_AUDIENCE`** | `cmd/gateway/main.go:55-64,217` |
| **Verifier construction order: `FetchJWKS` → `NewVerifier(keys)` → `NewJWKSPoller(verifier,…)` → `go poller.Run(ctx)`.** `NewVerifier` errors `ErrNoTrustedKeys` on an empty key set; the poller *requires* a built verifier | `auth.go:282-284`, `jwks_poller.go:84,230`, `cmd/gateway/main.go:189-236` |
| Audience enforced only when configured; `aud` may be an array | `auth.go:404,510` |
| Gateway resolves `A → U` itself on the write path | `gateway.go:504` |
| `VerifiedActor` exposes `{ActorID, Subject, TokenID, ExpiresAt, Issuer, RawSubject, VerifiedEmail}` — **no nonce, no raw claims** | `auth.go:215-242` |
| Kit adopters are **five** binaries (`facet`, `loftspace-app`, `clinic-app`, `cafe-app`, `wellness-app`) | `appsession.New(` call sites |
| Kit has **no ctx seam and no goroutine lifecycle** (`New`/`NewAuthenticators` take no ctx; `Manager` has no `Run`) | `session.go:127`, `signer.go:107` |
| Vendor authorities (pinned): OIDC Core 1.0 — code flow returns params **in the query string**; `nonce` MUST be checked when sent; `state` is the CSRF binding to a browser cookie; `offline_access` requests a refresh token. IETF OAuth browser-apps BCP — tokens server-side, HttpOnly+Secure cookies, refresh tokens never readable by browser code | `docs/vendors.md` "External IdP (OIDC)"; fetched this fire |

## 3. The shape

The kit gains one capability — *an externally-governed sign-in that ends in the same cookie the kit already
owns* — expressed as two new fixed routes, one new config block, a production branch inside the existing
refresh handler, and one small piece of in-process state (§3.4). No new binary and no platform-bucket
reads: the kit's only outbound calls remain the Gateway's `/v1/actor` plus, now, the deployment's IdP — an
integration dependency exactly as the architecture frames it (*the actor signing authority Lattice never
owns*, F3).

### 3.1 Configuration (per adopter, `<PREFIX>` as today)

| Variable | Effect |
|---|---|
| `<PREFIX>_OIDC_ISSUER` | enables the posture: the IdP issuer URL. Discovery is fetched from `<issuer>/.well-known/openid-configuration`; the document's own `issuer` MUST equal this value (OIDC Discovery §4.3) and every endpoint URL MUST be `https` unless the bind is loopback — mirroring `validateJWKSURL` (`cmd/gateway/main.go:422-432`) |
| `<PREFIX>_OIDC_CLIENT_ID` | required with `_OIDC_ISSUER`; also pins the ID-token `aud` in the kit's verifier |
| `<PREFIX>_OIDC_CLIENT_SECRET` | optional — set: confidential client (`client_secret_post`); unset: public client (PKCE-only) |
| `<PREFIX>_OIDC_SCOPES` | optional; default `openid offline_access` (see §3.4 on IdPs that use a different refresh-token opt-in) |
| `<PREFIX>_OIDC_PROVIDER_LABEL` | optional login-button label (default "your identity provider") |

Posture resolution in the adopting app's `run()`: dev auth (`_DEV_AUTH`) wins with the existing warn; else
OIDC (`_OIDC_ISSUER`); else static verify-only (`_JWT_PUBLIC_KEY`, which remains for cookie-verification
surfaces and tests but stops being documented as "the production posture" — it never could open a session).
PKCE (S256) is always sent, confidential clients included, per the OAuth Security BCP.

**Boot posture, mirroring the Gateway rather than inventing a stricter one** (`main.go:195-206`): a
discovery/JWKS fetch failure at boot is fatal **only when no other trusted keys are configured**; otherwise
it warns and retries on the poll cadence. Discovery is **re-fetched on that same cadence**, not latched at
boot — endpoint URLs and `jwks_uri` are live IdP state, and a one-time probe of mutable state is the
latching failure this codebase has been bitten by before. Malformed *configuration* (a bad issuer URL, a
missing client id) still refuses to boot, as `ParsePublicOrigin` does — a typo must not read as "not
configured".

**Secure-context requirement.** The new cookies are unconditionally `Secure` + `__Host-`; a browser
silently drops them otherwise, which would surface as an unexplained 403 on every callback. So the OIDC
posture **refuses to boot** unless the deployment is a secure context (a declared `PublicOrigin`, or a
loopback bind for local IdP testing). This is checkable at construction from config the kit already holds.

The kit's verifier is built **`FetchJWKS(discovery.jwks_uri)` → `NewVerifier{Keys, KeyInfo: ModeOpaque
pinned to the issuer, Audience: client_id}` → `NewJWKSPoller(verifier, …)` → `Run(ctx)`** — the Gateway's
own order and machinery, not a parallel implementation. That needs a **lifecycle seam the kit does not have
today**: `NewAuthenticators` gains a ctx-taking OIDC sibling that returns a runnable the app's `run()`
starts alongside its server (all five `cmd/*/main.go` — counted in Fire 1's scope, §9). Without it,
"key rotation" would be a single boot-time fetch and the first IdP rotation 401s every session until
restart. `_JWT_PUBLIC_KEY` may still be set alongside as a static extra key source, merged exactly as the
Gateway merges static + JWKS keys.

### 3.2 `GET /api/oidc/login` — begin

Auth-exempt fixed route (joins the `m.exempt` set; `session.go:171-177` accommodates it unchanged). Builds
the authorization request — `response_type=code`, `scope`, `state` (128-bit random), `nonce` (128-bit
random), `code_challenge` (S256) — and 302s to discovery's `authorization_endpoint`. Before redirecting it
sets the **transient flow cookie** `__Host-<AppName>_oidc`: HttpOnly, Secure, `SameSite=Lax`, `Path=/`,
MaxAge 10m, value = JSON `{state, nonce, verifier, returnTo}`.

- **Lax, not Strict, deliberately:** the callback arrives as a *cross-site* top-level GET navigation
  (redirect from the IdP); a Strict cookie is not attached on that navigation, a Lax one is. The session
  cookie itself stays Strict.
- `returnTo` comes from `?return_to` and is validated to a same-origin path (must start with `/`, must not
  start with `//`, must not contain `\`); anything else collapses to `/`. It is **never** interpolated into
  markup or script text — see §3.3 step 7.
- `redirect_uri` is derived from the **declared `PublicOrigin`**, or from the configured `BindHost` on a
  loopback dev bind. It is **never** derived from `r.Host`: that is an attacker-influenceable header, and
  delegating the check to the IdP's registered-URI list would make a kit invariant depend on an optional
  external precondition. Neither configured ⇒ the posture refuses to boot (same check as the secure-context
  requirement above).
- **Known asymmetry (accepted, documented — and narrower than when this was written):** this route sets a
  cookie on a GET, and `stateChanging()` treats GET as safe, so the gate does not refuse it outright. The
  blast radius is a discarded flow cookie on a browser that has not started a sign-in — no session, no
  token, no state change the user can observe — and it self-heals on the next real `/api/oidc/login`.
  **The `Sec-Fetch-Dest: document` term shipped since (`1a0d1849`) shrinks the reachable shapes:** a
  cross-origin **subresource** GET now carries `dest=image`/`script`/… and is refused by `metadataAdmits`,
  so the bare `<img src>` vector — the class the gate's own doc comment still cites for Facet's
  `GET /api/feed` — no longer reaches this route on a Fetch-Metadata browser. What remains is a genuine
  cross-origin **top-level navigation** (the user's own act, and it lands them on the IdP's page, which is
  self-evident), plus a browser that sends no Fetch Metadata at all — the gate's standing honest limit,
  not something this route adds. Named rather than papered over.

### 3.3 `GET /api/oidc/callback` — complete

Auth-exempt fixed route. The origin gate **already admits it** (GET + `Sec-Fetch-Mode: navigate` +
`Sec-Fetch-Dest: document`, which a real top-level IdP redirect carries; browsers attach no `Origin` to a
GET navigation) — this design adds **no** gate exemption, and a test pins that the gate still blocks a
scripted cross-origin fetch of the callback. The `Dest` term is what refuses a framed callback.

1. Read and **immediately expire** the flow cookie; missing/expired ⇒ 403 "sign-in flow not initiated".
2. `state` equality (constant-time, single-use) against the cookie value; mismatch ⇒ 403. This is the
   OIDC-Core state↔cookie CSRF binding, and on this navigation it is the **only** login-CSRF defense — see
   §5.2 for the residual it does not cover.
3. Exchange `code` at discovery's `token_endpoint` (server-to-server POST: `client_id`, secret when
   confidential, `code_verifier`, `redirect_uri`; 10s timeout, mirroring `resolveActorIdentity`). Steps
   1–2 run **before** any outbound call, so an unauthenticated caller can never make the kit emit
   token-endpoint traffic.
4. Verify the returned **ID token** through the kit's strict `Authn` — signature, alg allow-list, `kid`,
   `exp`, pinned `iss`, `aud` = client id — i.e. Contract #11 §11.2 verbatim, the same code path every
   other surface uses. Then, **on the already-signature-verified token only**, re-decode claims
   (`jwt.ParseUnverified` on a string `Authenticate` has just accepted — the verifier exposes no claim
   handle, `auth.go:215-242`) and check: `nonce` equals the cookie's nonce (mandatory per OIDC Core when a
   nonce was sent), and `azp` equals the client id whenever `aud` is multi-valued (OIDC Core §3.1.3.7 —
   `containsString` accepts array membership, so `azp` is what closes the multi-audience case).
5. Resolve credential→business identity via the Gateway's `/v1/actor` — same call, same deny-safe fallback,
   same persona-fence application as `handleDevLogin`. **Where the answer goes is §3.4** — this is the step
   the first draft left inert.
6. `setCookie` with the **ID token** and its `exp`. When the token response carried a `refresh_token`, set
   `__Host-<AppName>_rt`: HttpOnly, Secure, `SameSite=Strict`, `Path=/`, MaxAge = 30d (or the IdP's stated
   refresh lifetime when present).
7. Respond **200 with the kit's bounce page**, which `location.replace`s to `returnTo`. Not a 302: a server
   redirect continues the *cross-site* navigation chain, on which the Strict session cookie just set would
   not ride; a script-initiated navigation from our own page is same-origin and the cookie applies. The
   page is served with `Cache-Control: no-store` (it carries `Set-Cookie` with the session token — the
   in-tree precedent is `facet/browserengine.go:120-121`), `Referrer-Policy: no-referrer` (the callback URL
   carries the authorization `code`, which would otherwise reach `returnTo` via `Referer`), and a
   `Content-Security-Policy` with no `unsafe-inline`. **`returnTo` is emitted as a JSON-encoded value read
   by a static script, never interpolated into script text** — the naive `location.replace("<returnTo>")`
   is a reflected-XSS hole that the path-shape guard in §3.2 does *not* close (`/x";…//` satisfies every
   rule), and same-origin script on this page could call `/api/session/refresh` and walk off with the raw
   bearer.

**What the session cookie carries: the IdP's own ID token.** The app never becomes a token issuer — an
app-minted session JWT would require the Gateway to trust a per-app signing key, and Contract #11
deliberately makes arbitrary-subject (`nanoid`) trust unreachable from configuration. The same cookie then
authenticates the FE's Gateway-direct submits and Facet's NATS connect with zero translation, exactly as
the dev posture's minted token does today.

### 3.4 Resolution, refresh, and the state the kit must keep

**The resolution problem the minter used to hide.** In the dev posture, `A → U` reaches the app because
login **re-mints the cookie for `U`** (`session.go:451`); the app read boundaries consume
`appsession.Identity(ctx)` as already-resolved and say so in their own comments
(`loftspace-app/readauth.go:39-52`). With no Signer there is no re-mint, so a naive port leaves the cookie
carrying the pre-resolution `A` while the Gateway (`gateway.go:504`) and the NATS auth-callout both resolve
to `U` — **reads as `A`, writes as `U`**, which is precisely the split Contract #11 §11.4 forbids, and it
breaks the shipped credential-linking feature (a linked second sign-in method would open an empty world).

**The fix: an in-process resolution cache the kit's `resolve()` consults in the OIDC branch.** Bounded,
TTL'd, keyed `A → U`, populated at callback (step 5, where the call already happens) and lazily on a miss
(after a restart, or on another instance) through the same `resolveActorIdentity`. Deny-safe exactly as
§11.4 mandates: any miss or error acts as `A`, never an error that blocks authentication. This keeps
resolution in the one place that already owns it, needs no forgeable extra cookie (an unsigned
`resolved-identity` cookie would be a full read-auth bypass, and this posture has no signing key by
definition), and adds no per-request Gateway round trip in steady state. It is operational state in a
process, not platform state — no bucket, no Core KV, P1/P2/P5 untouched. The dev posture is unchanged (its
cookie already carries `U`).

**`POST /api/session/refresh` — the production branch.** The handler's gate becomes *"a renewal path
exists"*: the dev branch (Signer, unchanged) **or** the OIDC branch. OIDC branch: read
`__Host-<AppName>_rt`; absent ⇒ 401 (the FE's existing `onSessionLapsed` handles it); present ⇒
`grant_type=refresh_token` at the token endpoint ⇒ verify the new ID token through `Authn` (same §11.2
path — never trust a token endpoint blindly) ⇒ **assert the new subject equals the session's current
subject** (a mismatch is a silent identity swap; refuse) ⇒ re-set the session cookie ⇒ re-set the RT cookie
when the IdP rotated it ⇒ answer the **unchanged** `{token, expiresAt}` contract. A grant refusal (revoked,
expired, or reused RT) clears the RT cookie and 401s — deny-safe, never retried, landing in the FE's
existing lapse path. The persona-fence narrowing check stays where it is and runs on the verified subject
in both branches. `RefreshAuthn` is irrelevant to the OIDC branch: the RT, not the expiring ID token, is
the renewal credential.

**Refresh-token issuance is IdP-specific and is detected, not assumed.** `offline_access` is the OIDC
scope, but Google (named in `docs/vendors.md`) issues refresh tokens via `access_type=offline` +
`prompt=consent` instead. A first login that yields no refresh token means `/api/session/refresh` 401s and
the FE write path is dead again — the very failure this design exists to end — so the kit **logs a loud,
specific operator error naming the scope/parameter knob** rather than degrading quietly, and the deployment
matrix carries a per-vendor row.

### 3.5 Login page, whoami, logout

`GET /api/login-options` gains `"oidc": {"enabled": true, "label": <provider label>}`. The shared login page
renders one "Continue with …" button (→ `/api/oidc/login?return_to=<current>`) when enabled, in place of
the persona/free-form dev widgets. `handleWhoami`'s hat-hints call already uses the cookie token and works
identically with an ID token. Logout clears all three cookies **and**, when discovery advertises a
`revocation_endpoint`, revokes the refresh token (RFC 7009) — a 30-day credential that survives an explicit
sign-out is a materially worse residual than the deferred IdP-side sign-out below.

**Deliberately not built, each with its named trigger:** RP-initiated logout at the IdP
(`end_session_endpoint`) — trigger: a deployment needing IdP-side sign-out; `prompt=none` silent re-auth as
an RT-less fallback — trigger: an IdP that refuses refresh tokens to a web client; back-channel logout —
trigger: an IdP that mandates it.

### 3.6 What this deliberately does *not* touch

- **The origin gate.** Zero changes. The `form_post` response mode the backlog row anticipated is not
  needed (query mode is the code flow's default and our committed choice) and stays **rejected** — an
  exemption surface on the CSRF gate is exactly the hole the gate's own "no way to exempt a path" note
  warns about. A SAML POST binding, if it ever arrives, files its own design.
- **The session cookie's name and attributes** (`<AppName>_session`, Strict). Migrating *it* to `__Host-`
  belongs to the sibling fixation row (*[appsession] A co-hosted page can plant a session cookie*); this
  design only ensures the **new** cookies are born `__Host-`-prefixed. §5.2 states precisely what that does
  and does not buy.
- **Contract #11.** Built to, not amended: the RP flow is upstream of token verification, and the accepted
  profile, binding, resolution, and revocation are consumed as frozen.

## 4. Forks, designed through

### 4.1 Where does the relying party live? (recommendation: the kit)

| Option | Shape | Verdict |
|---|---|---|
| **A — in the kit (recommended)** | Server-side code flow; kit holds the RT in an HttpOnly cookie; FE refresh contract preserved | The BCP's server-side-custody posture adapted to Lattice's structural need for a browser-held bearer (Gateway-direct writes + NATS connect are the parity invariant §6.2 — a pure BFF that proxies everything would *abolish* browser-direct, a far larger architectural swerve). One implementation serves all five FEs; the client secret and RT never reach JS. |
| B — SPA-side PKCE + a token-exchange endpoint | Each FE runs an OIDC JS client; the kit only verifies a POSTed token | Rejected: reworks five shells, breaks the refresh contract every write path depends on, and lands tokens and RTs in JS-readable storage (the BCP's weakest tier); silent renewal via hidden iframes is dying under third-party-cookie phase-out. The kit exists precisely so sign-in ships once. |
| C — external auth proxy (oauth2-proxy style) | Proxy authenticates, injects headers | Rejected: the proxy's session is not a Gateway-verifiable JWT, and nothing can hand Facet's in-page NATS engine its bearer. Fine for fronting admin tools; wrong for these apps. |

**Which token becomes the cookie:** the **ID token** (JWT by spec, `aud` = client id, profile-complete per
§11.2). The access token is IdP-dependent — opaque at several vendors unless an API audience is configured
— so it is rejected as the default and noted as a per-deployment variant if an IdP's ID-token TTL proves
hostile.

### 4.2 The audience pin (resolved; the one deployment-shaped decision to confirm)

Using the ID token as the Gateway bearer only holds if the Gateway validates `aud`. It has exactly **one**
`GATEWAY_JWT_AUDIENCE` (`cmd/gateway/main.go:217`) and audience checking is opt-in (`auth.go:404`). Five
apps with five client ids would force the audience unset, and then **any** token from the trusted issuer is
a valid Lattice write and NATS-connect credential — on a shared IdP (Google), an unrelated relying party's
ID token for the same user replays into Lattice. Contract #11's confinement is `(iss, sub)`-scoped and does
not close this; it is the classic ID-token-as-API-bearer antipattern.

**Resolved: one OIDC client registration for all five apps** (one `client_id`, five registered redirect
URIs), because *Lattice* is the relying party and the apps are its surfaces — they share one Gateway, one
identity graph, and derive every hat from the graph, so there is no per-app authorization boundary for a
separate client to express. One client ⇒ one `aud` ⇒ `GATEWAY_JWT_AUDIENCE` is set and enforced.
**And the omission fails closed:** the Gateway **refuses to boot when an external key source
(`GATEWAY_JWKS_URL` or `GATEWAY_JWT_KEYS_DIR`) is configured with no `GATEWAY_JWT_AUDIENCE`** — the same
shape as its existing "keys dir configured with no issuer" refusal (`keysource.go:77-80`). Dev/demo is
untouched (dev-key posture, no external source). The escape hatch for a deployment that genuinely needs
per-app clients is a multi-valued `GATEWAY_JWT_AUDIENCE` (accept-any-of), a small extension to
`auth.Config` — designed for, built only on a real driver.

## 5. Reconciliation and honest residuals

### 5.1 Didn't-we-already / duplicate-or-diverge / new state?

- *Didn't we already build production auth?* Everything except this leg. Verification on all surfaces,
  opaque binding + confinement, JWKS rotation, revocation, resolution, first-touch provisioning: shipped.
  What was unreachable is **issuing the session cookie without a Signer**, which persona-worlds §5
  explicitly deferred as F4.
- *Does it duplicate machinery?* No RP exists anywhere in the tree (verified: no
  `authorization_code`/`code_verifier`/`code_challenge`/`openid-configuration` hits). The verifier, JWKS
  fetch + poller, binding, and `/v1/actor` resolution are **reused**; the genuinely new code is the flow
  choreography plus the §3.4 resolution cache.
- *New state?* One bounded in-process resolution cache (§3.4) and two browser cookies (a 10-minute flow
  cookie, the RT). No platform buckets; no Core-KV reads or writes; P2/P5 untouched.
- *In-flight collisions?* Checked the open 📐/🏗️ designs: cap-read (Refractor) — disjoint; the appsession
  *fixation* row — adjacent by component and explicitly fenced in §3.6/§5.2; the Loupe lane — Loupe does
  not adopt the kit (operator tier, own auth).

### 5.2 Residuals, stated rather than claimed away

- **`__Host-` does not close same-host planting.** It closes foreign-host, subdomain, and insecure-origin
  planting. It adds no port or path isolation — it *mandates* `Path=/` — and the kit's own gate exists
  because co-hosted apps share a cookie jar host (`origin.go:12-21`). On a **single-hostname multi-app**
  deployment, a hostile co-hosted page can plant a flow cookie it obtained legitimately and force a victim
  through a callback carrying the attacker's `code`: state matches because the attacker supplied both
  halves, and the victim is signed into the attacker's account. **Therefore the OIDC posture requires one
  hostname per kit-adopting app**, checked at boot against `PublicOrigin` and stated in the deployment
  matrix. (The shipped demo box already gives each app its own hostname —
  `deploy/demo/demo-bootstrap.sh` — and the all-on-127.0.0.1 local posture runs dev auth, not OIDC.)
- **The RT cookie rides every same-site request.** `__Host-` forces `Path=/`, so the refresh token travels
  on every page load and API call to its own app — not just to `/api/session/refresh`. The trade is taken
  knowingly: planting protection is worth more than path scoping here, since `__Secure-` + a scoped path
  would not close same-host planting either. It is HttpOnly and no kit or app handler logs request headers.
- **Page-compromise XSS.** Same-origin script can call `/api/session/refresh` and use the returned bearer
  for the life of the tab — identical to the shipped dev posture's exposure, no widening. §3.3's CSP and
  JSON-encoded `returnTo` exist so this design does not *add* an XSS surface of its own.

## 6. Migration / compatibility

- **Dev/demo:** unchanged. Dev auth wins the posture race; with no `_OIDC_*` set, the new routes are inert.
- **The verify-only PEM posture** keeps working for what it actually does (verify cookies); the component
  page stops calling it "the production posture" and documents a three-way table (dev / OIDC /
  static-verify-only).
- **FE shells:** the refresh, whoami, and login-options **response shapes** are strictly extended (one new
  optional `oidc` field), so nothing breaks by shape — but see the TTL work below, which is a real code
  change, not a claim of zero.
- **Deployment:** a real deployment registers one OIDC client with five redirect URIs, sets each app's
  `_OIDC_*` block, and sets the Gateway's `GATEWAY_JWKS_URL`/`_ISSUER`/`_JWT_AUDIENCE` to the same IdP and
  client — Contract #11 §11.3's "all surfaces bind identically" invariant, operationally.

**The FE refresh cadence is hardcoded to the dev 30-minute TTL and must change.**
`facet/web/boot.mjs:158` and `loftspace-app/web/app.js:311` both hardcode a 20-minute loop derived from
`DevTokenTTL` (`signer.go:27`), and `boot.mjs` never reads `expiresAt` at all. Real IdP ID tokens are
commonly 5–15 minutes: Facet's cached token would go stale mid-interval and its NATS reconnects would
present an expired token, and LoftSpace's session cookie would fail strict verification mid-read and bounce
the user to `/login`. Both loops must be driven by the returned `expiresAt` (renew at a fraction of the
remaining lifetime, floor'd), and the kit logs a loud operator warning when the first verified ID token's
TTL is below a workable floor. This is Fire 2 and is scoped as real work.

## 7. Test strategy

**Fake IdP testkit** (`internal/appsession/oidctest`): an `httptest` OIDC provider serving discovery,
`authorization_endpoint` (immediate 302 back with a code), `token_endpoint` (code→tokens, RT rotation
switch, error injection), `revocation_endpoint`, and JWKS over a throwaway RSA key — no third-party
dependency; the JWKS-poller tests are the in-tree precedent for HTTP-served key fixtures. Deterministic per
house rules (synchronous fake, no sleeps).

**Unit vectors (Fire 1):** happy path (login → callback → cookie → refresh); state mismatch, missing or
expired flow cookie, replayed state ⇒ 403; nonce mismatch rejected; `azp` mismatch on a multi-valued `aud`
rejected; foreign-issuer ID token rejected (confinement); audience mismatch rejected; **nonce/`azp` read
only after `Authenticate` succeeds**; **§11.4 conformance — a bound credential signing in via OIDC yields
the same resolved identity at the app read boundary as the Gateway stamps on a write**; resolution miss ⇒
acts as `A`, never an error; RT rotation honored; RT revoked/reused ⇒ cookie cleared + 401, no retry;
subject-continuity mismatch on refresh refused; `returnTo` open-redirect vectors (`//evil`,
`https://evil`, `\` forms) collapse to `/`; **`returnTo` XSS vectors (`"`, `'`, `</script>`, backtick,
`${}`) render inert**; bounce response carries `no-store` + `no-referrer` + CSP and no token material;
discovery `issuer` mismatch refused; JWKS **key rotation** mid-session verified through the poller; the
origin gate still blocks a scripted cross-origin callback fetch — and a framed one — while admitting the
top-level navigation shape (`Sec-Fetch-Dest: document`).

**e2e (Fire 3, ephemeral stack):** fake IdP + Gateway (trusting the fake's JWKS, audience pinned) + one
vertical app: a browser-shaped client walks login → callback → bounce, `/api/whoami` reports the
opaque-derived identity, a write submits through the Gateway under the cookie token, a protected read
RLS-scopes to the **same** identity, and `/api/session/refresh` returns a rotated token that still writes.
This is what proves the §11.3 "all surfaces bind identically" invariant live, and it is the test that would
have caught the resolution split.

## 8. Risks

- **IdP variance** — scope/parameter names for refresh tokens (§3.4), ID-token TTLs (§6), `kid` presence
  (mandatory on both sides: `auth.go:378-381`, and `jwks.go` skips kid-less entries, so a kid-less IdP
  fails closed at boot), and signing alg (`RS*/ES*` only — an HS256-configured client fails every login).
  All belong in the deployment matrix; the fake pins the spec-shaped contract, and the first real
  deployment (Keycloak, self-hostable) validates variance as a named follow-on.
- **IdP availability** — runtime verification is local (JWKS cached), so existing sessions survive an IdP
  outage until token expiry; only the code exchange and the RT grant need it live. That matches the
  architecture's integration-dependency posture, and §3.1's boot rule no longer over-tightens it.
- **Clock skew** — inherited from the shared verifier's bounded skew; no new time arithmetic beyond cookie
  MaxAge.

## 9. Decomposition for the Steward

- **Fire 1 (M–L) — the RP in the kit.** §3.1–§3.5 mechanism (routes, discovery + `FetchJWKS`/verifier/poller
  wiring **plus the kit lifecycle seam and the five `cmd/*/main.go` wirings**, resolution cache, refresh
  branch, logout revocation), the fake-IdP testkit, the §7 unit vectors, the reference login page, and
  `docs/components/appsession.md` rewritten (the Known-gap paragraph dies; three-way posture table; §5.2's
  residuals; the adopter list corrected to five). *Green:* `go test ./internal/appsession/... ./cmd/...`,
  every §7 unit vector, all existing kit tests untouched-green, `make vet` + `golangci-lint` + the
  `scripts/lint-*.go` gates.
- **Fire 2 (S–M) — the audience pin + the FE cadence.** §4.2's Gateway boot refusal (external key source
  ⇒ `GATEWAY_JWT_AUDIENCE` required) with its vectors; both FE refresh loops driven by `expiresAt`; the
  short-TTL operator warning; the per-app login pages render the OIDC button. *Green:* Gateway auth tests
  incl. the new refusal; the FE loop tests (`feed_source.test.mjs` is the precedent); full suite.
- **Fire 3 (S–M) — proof.** The §7 e2e on the ephemeral stack; the deployment matrix (one client, five
  redirect URIs, the app ↔ Gateway env pairing, per-vendor refresh-token rows, the one-hostname-per-app
  requirement) in the component page. *Green:* e2e in CI, `make verify-kernel`, full suite.

Fire 1 realizes value alone: the posture becomes reachable and provable against the fake IdP, which is a
**consumer in CI from day one** — no dead scaffolding. Fire 2 closes the fail-open audience gap and makes
real IdP TTLs survivable. Fire 3 makes it demonstrable stack-wide.

## 10. Adversarial review (2026-07-25, this fire)

An independent adversarial pass ran against the full draft, grounded in the kit/verifier/FE/contract code.
It **confirmed** the origin-gate analysis (the callback needs no exemption and the `form_post` premise
dissolves), the bounce-page/Strict-cookie reasoning, the flow cookie's Lax choice, the state↔cookie CSRF
binding, the pre-exchange ordering, the exempt-set/route accommodation, and the no-contract-amendment claim.
It returned **1 blocker + 6 majors + 13 minors**, all folded in above:

**(1) Blocker — resolution was inert.** Step 5 called `/v1/actor` with nowhere to put the answer, since
only the dev posture's re-mint carried `A → U`; the app read boundaries would have read as `A` while the
Gateway and NATS callout wrote as `U` — the §11.4 split, breaking shipped credential-linking → §3.4's
in-process resolution cache + the §7 conformance vector + the Fire 3 e2e that proves it.
**(2)** No enforceable audience at the Gateway with five client ids — any issuer token becomes a bearer →
§4.2 (one client registration + a fail-closed boot refusal).
**(3)** "Zero FE changes" was false: two hardcoded 20-minute loops derived from the dev TTL → §6 + Fire 2.
**(4)** The bounce page interpolated `returnTo` into inline script — reflected XSS on the one page that can
reach the bearer → §3.3 step 7 (JSON-encoded value, CSP, `no-store`, `no-referrer`) + XSS vectors.
**(5)** The `__Host-` planting-immunity claim was over-broad (it does not close same-host co-hosting) →
§5.2 + the one-hostname-per-app deployment requirement.
**(6)** The JWKS wiring inverted the only order that compiles, and the kit has no goroutine lifecycle to
run a poller → §3.1 + the five-app wiring counted in Fire 1.
**(7)** The RT cookie cannot be both `__Host-` (`Path=/`) and "only travels to the refresh endpoint" →
§5.2 states the real transmission envelope.
Minors folded: `azp` on multi-valued `aud`; discovery `issuer`/scheme validation; boot fail-closed
over-tightened vs the Gateway precedent + discovery re-fetch instead of a boot latch; `redirect_uri` off
`r.Host`; the secure-context requirement for `__Host-` cookies; the state-changing-GET on
`/api/oidc/login` named rather than hidden; RT revocation at logout; subject continuity on refresh;
`GatewayURL` required in the OIDC posture too; `kid`/alg variance in the deployment matrix; the
`offline_access` non-universality promoted to a loud first-login error; the component page's stale
three-adopter list.
