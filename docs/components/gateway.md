# Gateway

**Component reference** | Audience: operators + implementers

> The Gateway is a **platform binary** (`cmd/gateway`) — it has no frozen interface contract of its
> own; it *builds to* Contract #2 (§2.34 `actor`, §2.39 header→full-key stamping), #6 (Capability KV),
> #9 (Identity Claim Flow), and #5 (Health KV). Its design of record is
> `_bmad-output/implementation-artifacts/gateway-external-trust-boundary-design.md`. Update this page in
> the same commit as the code; drift between page and code is a documentation bug.

---

## Overview

The Gateway is the **external write-path translator** — the trust boundary between an external actor
and the platform. It terminates external HTTP requests, verifies the caller's IdP-signed JWT with the
`internal/gateway/auth` Authenticator (built by D1.2), **strips any client-supplied `actor`** from the
request body, and **stamps the verified actor** into the operation envelope before publishing to
`core-operations`. It never writes Core KV directly — like every other actor, it mutates state only by
submitting operations (P2: the Processor is the sole writer).

It is the *authentication* seam that closes actor impersonation, the complement to the NATS
account-level write restriction (transport-authZ — only the Gateway's NATS user may publish
`core-operations`, live via `#75` Fire 2) and the Capability KV (actor-authZ, step-3 lookup of the now
unforgeable actor).

**In scope for Fire 1:** the write-path translator only. Internal service actors (Loom / Weaver /
Bridge / object-store-manager / admin tooling / Loupe) keep their sanctioned direct-submit path — the
Gateway is the external door, not a re-route for internal traffic.

---

## Write path — `POST /v1/operations`

```
external client                 Gateway                                  core-operations → Processor
──────────────                  ───────                                  ───────────────────────────
HTTP POST /v1/operations        1. Bearer-authenticate (auth.Authenticator)
  Authorization: Bearer <JWT>      → verified actor, or 401/403
  body: {operationType,         2. parse body (no `actor` field to bind — a
         lane, class, payload,     forged one is silently dropped)
         contextHint/reads,     3. STAMP env.Actor = verified actor
         authContext}           4. publish core-operations (Gateway's NATS user) ──▶
                                5. relay the Processor's reply ◀────────────────────  accepted | rejected | duplicate
```

- The client **never controls the trusted actor.** `operationRequest` (the wire struct) has no `actor`
  field — a client-supplied `actor` key in the raw JSON body is simply not bound during unmarshal, so
  it can never reach the envelope regardless of what the request contains.
- `requestId` is client-suppliable (forwarded verbatim, per Contract #4 idempotency) or generated when
  omitted. `lane`, `operationType`, `class`, `payload`, `contextHint.reads`/`reads`, and `authContext`
  are all client-supplied and forwarded as-is — safe, because the **verified** actor (not anything else
  in the request) is what step-3 matches against Capability KV / `serviceAccess[]` / `ephemeralGrants[]`.
- **HTTP status mapping:** `accepted`/`duplicate` → 200; `rejected` → 403 for
  `AuthDenied`/`LaneUnauthorized`/`AuthContextMismatch`, 500 for `InternalError`/
  `AuthInfrastructureFailure`, 400 otherwise. A Processor-reply timeout returns `202` + `requestId` for
  async reconciliation (mirrors the bridge's async-reply posture) — the caller polls Core KV for
  read-your-own-writes.
- Auth failures: missing/malformed `Authorization` header, an unverifiable token (bad signature, wrong
  `kid`, unsupported algorithm, expired, wrong issuer/audience) → **401**. A structurally-valid but
  **revoked** actor → **403**.

---

## Fail-closed JWT key loading

The external write surface **refuses to start** unless at least one trusted public key is configured —
"no IdP ⇒ no external writes," never a silent anonymous fallback.

- `GATEWAY_JWT_KEYS_DIR` — a directory of `<kid>.pem` SubjectPublicKeyInfo files, the prod posture: a
  static snapshot of the deployment's IdP JWKS. (Full JWKS HTTP polling with live kid-keyed rotation is
  a follow-up fire; today an operator refreshes the snapshot and restarts.)
- `GATEWAY_DEV_MODE=true` — **additionally** trusts the checked-in dev key
  (`deploy/gateway-dev-key/`, kid `"dev"`, DEV-ONLY like the NATS dev nkeys). Mint a token:
  `bin/gateway dev-token -sub <identityNanoID>`. **Never set in production** — the dev key only loads
  when this flag is explicitly true.
- Neither configured → `run()` returns an error before the HTTP listener starts.

---

## Health

The Gateway writes a Contract #5 §5.2 heartbeat to `health.gateway.<instance>` every 10s
(`internal/gateway.Heartbeater`) with `requests_total` / `auth_failures_total` / `ops_submitted_total`
metrics — Loupe's system-map / health dashboard picks it up like every other component.

---

## Implementation status

**Built (Fire 1).** `internal/gateway` (Server: `POST /v1/operations` strip-and-stamp translator,
Heartbeater) + `cmd/gateway` (wiring, fail-closed key loading, the `dev-token` subcommand). A dedicated
NATS user (`deploy/nkeys/gateway.nk`) grants `ops.>` / `health-kv.>` publish, denying `core-kv.>` /
`capability-kv.>` — the same shape as every other op-submitting actor. Gate-3 adversarial vector #14
(forged-actor-never-wins) proves the strip-and-stamp defeats impersonation.

**Deferred (follow-up fires, per the design's decomposition):**
- **Fire 2 remainder** — full JWKS HTTP polling with live kid-keyed rotation (today: a static
  `GATEWAY_JWT_KEYS_DIR` snapshot, operator-refreshed).
- **Fire 3** — the read-path front (`GET /v1/<readmodel>`), sequenced behind D1.3's first live
  protected Postgres read-model (chain-grounding — not dead scaffolding).
- **Fire 4** — the identity-claim front (`POST /v1/claim`, Contract #9's
  `CreateUnclaimedIdentity`/`ClaimIdentity` allow-list, unauthenticated + rate-limited).
- **Fire 5 (ops, not platform code)** — the prod reverse-proxy (`deploy/nginx.conf`: TLS termination,
  rate limiting, CORS, IP allowlisting) per the ratified Gateway Architecture Decision.
