# Gateway

**Component reference** | Audience: operators + implementers

> The Gateway is a **platform binary** (`cmd/gateway`) — it has no frozen interface contract of its
> own; it *builds to* Contract #2 (the operation envelope's `actor` field), #6 (Capability KV),
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

It is the *authentication* seam that closes actor impersonation, working with the NATS account-level
write restriction (transport-authZ — only the Processor + bootstrap may publish `$KV.core-kv.>`, so no
actor can fabricate a Core-KV write and bypass the ledger; live via `#75` Fire 2) and the Capability KV
(actor-authZ, step-3 lookup of the now unforgeable actor). Note the transport restriction is on *direct
KV writes*, **not** on `core-operations` publish — every sanctioned actor (the engines, the vertical
apps, the CLI, Loupe) submits ops; the Gateway is the external door, not the sole ops publisher.

**In scope:** the write-path translator (`POST /v1/operations`, Fire 1) plus the read-path front
(`GET /v1/<name>`, Fire 3) — one binary, one auth+revocation seam, two directions of the same trust
boundary. Internal service actors (Loom / Weaver / Bridge / object-store-manager / admin tooling /
Loupe) keep their sanctioned direct-submit path — the Gateway is the external door, not a re-route for
internal traffic.

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

## Read path — `GET /v1/<name>`

The read-front (Fire 3) fronts a **protected Postgres read model** (Contract #6 §6.14, D1.3's
`actor_read_grants` + FORCE-RLS enforcement) for external callers, the read-side sibling of the write
path's same auth+stamp discipline: authenticate, then let RLS do the filtering.

```
external client                 Gateway                                  read_<x> (RLS-protected)
──────────────                  ───────                                  ───────────────────────
GET /v1/<name>                  1. Bearer-authenticate (auth.Authenticator)
  Authorization: Bearer <JWT>      → verified actor, or 401/403
                                 2. BEGIN; set_config('lattice.actor_id',
                                    <verified actor.Subject>, true)      ──▶
                                 3. run the registered SELECT (no WHERE —
                                    RLS scopes every row)                ──▶
                                 4. COMMIT (discards the txn-local var)  ◀── rows
                                 5. serve {rows: […], count: N}
```

- **Every read-model is operator-configured, not compiled.** `GATEWAY_READ_MODELS_DIR` points at a
  directory of `<name>.sql` files; each file's base name becomes the `GET /v1/<name>` path segment and
  its trimmed content is the **fixed** SELECT that model runs — no caller-supplied predicate, ever. The
  Gateway carries no compiled knowledge of any vertical's row shape (mirrors the bridge's type-agnostic
  adapter seam, D5): rows are scanned by column name (`pgx` `FieldDescriptions()`+`Values()`) and served
  as JSON verbatim — `internal/gateway/read.go`. `deploy/gateway-read-models/landlordApplications.sql`
  is the first registered model (the D1.3 landlord/property-manager view,
  `read_landlord_lease_applications`).
- **RLS does the authorization; the Gateway does none.** The handler never adds a `WHERE`, never
  filters, never 403s a query — a non-landlord-role subject reading `landlordApplications` gets `200`
  with zero rows (no 403-vs-404 authorization oracle), identically to `cmd/loftspace-app`'s own
  authenticated read boundary, which serves the same underlying protected table today.
- **`GATEWAY_PG_DSN`** is a **non-superuser, SELECT-only** role (`make provision-gateway-role`) — a
  superuser/BYPASSRLS connection would silently skip RLS and leak every actor's rows. Unset/unreachable
  Postgres is non-fatal: every `GET /v1/<name>` 502s "read model unavailable" rather than the Gateway
  refusing to start (mirrors the write path's requireConn discipline).
- Auth failures mirror the write path: missing/malformed Bearer → 401; unverifiable/expired/wrong-`kid`
  token → 401; revoked actor → 403 (via `mapAuthError`, shared with `/v1/operations`).

---

## Fail-closed JWT key loading

The external write surface **refuses to start** unless at least one trusted public key is configured —
"no IdP ⇒ no external writes," never a silent anonymous fallback. Any combination of the three sources
below may be configured; the trusted set is their union.

- `GATEWAY_JWT_KEYS_DIR` — a directory of `<kid>.pem` SubjectPublicKeyInfo files: a **static** snapshot
  of the deployment's IdP JWKS. An operator refreshes the snapshot and restarts to rotate.
- `GATEWAY_JWKS_URL` — a **live** IdP JWKS endpoint (`https://…`; `http://` is refused unless
  `GATEWAY_DEV_MODE=true`, the same profile gate the dev key uses). Fetched once at startup — a failed
  initial fetch with no other key source configured refuses to start (fail-closed) — then polled in the
  background (`GATEWAY_JWKS_POLL_INTERVAL`, default 5m, floor 30s) and **hot-swapped** into the Verifier
  (`auth.JWKSPoller`): a rotated IdP signing key is picked up with **no Gateway restart**. A poll
  failure (network blip, IdP hiccup) logs and **keeps the last-known-good key set** — fail-safe, not
  fail-closed, once already serving traffic. `GATEWAY_JWT_KEYS_DIR`/dev keys are re-merged into every
  poll, so a JWKS response can add or retire IdP keys but can never un-trust an operator-configured key.
- `GATEWAY_DEV_MODE=true` — **additionally** trusts the checked-in dev key
  (`deploy/gateway-dev-key/`, kid `"dev"`, DEV-ONLY like the NATS dev nkeys) and allows a plaintext-HTTP
  `GATEWAY_JWKS_URL`. Mint a token: `bin/gateway dev-token -sub <identityNanoID>`. **Never set in
  production.**
- None configured (and the initial JWKS fetch, if attempted, fails) → `run()` returns an error before
  the HTTP listener starts.

---

## Token-revocation kill-switch

A JWT verifies on signature + expiry alone, so a *compromised* actor keeps access until its short
token expires. The kill-switch (`internal/gateway/revocation.Checker`, consulted per request by
`auth.Authenticator`) is the out-of-band cutoff — design of record:
`gateway-token-revocation-activation-design.md`.

- **Write path.** `RevokeActor{actor, reason?}` / `UnrevokeActor{actor}` (identity-domain, `operator`
  scope:any) are **event-only ops** — no Core-KV mutation. Each outboxes `gateway.actorRevoked` /
  `gateway.actorUnrevoked` onto `core-events` through the standard Processor commit (P2); revocation is
  operational security state, not graph state.
- **Materializer.** The Gateway runs its **own** durable `events.gateway.>` consumer
  (`internal/gateway.StartRevocationMaterializer`) that folds those events into its local
  `token-revocation` KV bucket (put on revoke, delete on unrevoke) — the exact bucket
  `revocation.Checker` reads. This is the same event-only-op → outbox → component-materializes-its-own-
  state loop the Loom lifecycle ops run; the Gateway's kill-switch deliberately does **not** ride a
  Refractor lens, so revocation propagates even if Refractor is degraded.
- **Fail-closed bring-up.** Before the HTTP listener binds, the Gateway opens the `token-revocation`
  bucket, attaches the materializer consumer, and drains its cold-start backlog. Either the bucket
  failing to open or the consumer failing to attach **refuses to start** — there is no more silent
  downgrade to verification-only auth. Once serving, a per-request KV read error still denies
  (fail-closed); a consumer disconnect after startup serves off the last-known-good local set (the short
  JWT TTL is the backstop for that lag window) and surfaces a `revocation.consumerDisconnected` Health
  issue.
- **Auditable.** Each revoke/unrevoke is a committed op (intent-ledger) **and** a durable `core-events`
  event (7d) — `by` (`op.actor`) + `at` (commit timestamp) make it who-revoked-whom-when.

---

## Health

The Gateway writes a Contract #5 §5.2 heartbeat to `health.gateway.<instance>` every 10s
(`internal/gateway.Heartbeater`) with `requests_total` / `auth_failures_total` / `ops_submitted_total`
(write path) and `reads_total` / `read_failures_total` (read path, Fire 3) metrics, plus a `revocation`
block (`consumerConnected`, `revokedCount`, `lastEventSeq`, `lastSyncAt` — the token-revocation
kill-switch's live state, `health-kv-schema.md`) — Loupe's system-map / health dashboard picks it up
like every other component.

---

## Implementation status

**Built (Fire 1).** `internal/gateway` (Server: `POST /v1/operations` strip-and-stamp translator,
Heartbeater) + `cmd/gateway` (wiring, fail-closed key loading, the `dev-token` subcommand). A dedicated
NATS user (`deploy/nkeys/gateway.nk`) grants `ops.>` / `health-kv.>` publish, denying `core-kv.>` /
`capability-kv.>` — the same shape as every other op-submitting actor. Gate-3 adversarial vector #14
(forged-actor-never-wins) proves the strip-and-stamp defeats impersonation.

**Built (Fire 2 remainder).** `internal/gateway/auth` (`ParseJWKS` — a dependency-free RFC 7517/7518 JWK
Set parser for RSA/EC keys; `JWKSPoller` — fetch + background poll + hot-swap into the Verifier via the
new `Verifier.SetKeys`, atomic-pointer-backed for a lock-free hot path) + `cmd/gateway` (`GATEWAY_JWKS_URL`
/ `GATEWAY_JWKS_POLL_INTERVAL` wiring, the https-unless-dev-mode transport gate, fail-closed initial fetch).
No new vendor dependency — JWK parsing uses only `crypto`/`encoding` stdlib packages.

**Built (token-revocation kill-switch, Fire 1 of
`gateway-token-revocation-activation-design.md`).** identity-domain's `RevokeActor`/`UnrevokeActor`
event-only ops + the `gateway.actorRevoked`/`actorUnrevoked` event-type DDLs
(`packages/identity-domain/revocation.go`); the `token-revocation` bucket (bootstrap primordial);
`internal/gateway.StartRevocationMaterializer` (the events.gateway.> consumer + cold-start catch-up +
fail-closed startup, replacing the old best-effort nil-checker path); the Gateway NKey's
`$KV.token-revocation.>` write grant (`deploy/gen-dev-nkeys`, pinned by
`natsperm.TestGatewayRevocationBucketWriteIsolation`).

**Built (token-revocation kill-switch, Fire 2 of
`gateway-token-revocation-activation-design.md`).** The rich `revocation` heartbeat block
(`consumerConnected`/`revokedCount`/`lastEventSeq`/`lastSyncAt`) — `revokedCount` is scanned live off the
`token-revocation` bucket each heartbeat; the other three fields are set by the materializer's handler
(`RecordRevocationSync`) and health sink (`SetRevocationConnected`). Unblocks Loupe F11 (the revoke
console) — done.

**Built (Fire 3).** `internal/gateway/read.go` (the generic `GET /v1/<name>` handler: `PgPool`/
`ReadModel`/`ConfigureReadModels`, the txn-local `set_config('lattice.actor_id', …)` scan, generic
column-name JSON scanning) + `cmd/gateway` (`GATEWAY_PG_DSN`, `GATEWAY_READ_MODELS_DIR` directory
loader) + `deploy/gateway-read-models/landlordApplications.sql` (the first registered model) +
`make provision-gateway-role`. Sequenced behind D1.3's first live protected Postgres read-model
(landlord/property-manager applications, shipped) — chain-grounded, not dead scaffolding. Proven against
a real non-superuser RLS reader role (`internal/gateway/read_rls_test.go`,
`POSTGRES_TEST_DSN`-gated): per-actor row scoping, revoked-grant denial, no pooled-connection actor-var
leak, unauthenticated 401.

**Deferred (follow-up fires, per the design's decomposition):**
- **Fire 4 — retired as originally conceived; re-grounded 2026-07-04.** The design assumed an
  *unauthenticated* `POST /v1/claim` front for `CreateUnclaimedIdentity`/`ClaimIdentity`. Grounding
  (`gateway-claim-flow-identity-provisioning-design.md`) found this would not have fixed anything:
  `CreateUnclaimedIdentity` (staff-role-gated) already routes correctly through Fire 1's authenticated
  `POST /v1/operations` — never a gap. `ClaimIdentity` (`scope: self`, `GrantsTo: consumer`) is a hard,
  pre-Starlark step-3 gate requiring the calling actor to **already** hold `consumer` — unreachable by
  *any* actor, authenticated or not, because nothing in the platform ever grants a fresh actor its first
  role (`AssignRole` is the only path to a `holdsRole` link, and it's operator-only; nothing calls it for
  `consumer`, in any existing or planned flow). An unauthenticated HTTP front changes who calls the
  endpoint, not whether the resulting envelope's actor holds a capability grant — the real gap is
  authorization, not authentication. The design's resolution: a new `ProvisionConsumerIdentity` op the
  Gateway submits under its own bootstrap-seeded system identity (a narrow `identityProvisioner` role, not
  full `operator` — the Gateway is internet-facing in a way Loom/Weaver/objmgr/privacy are not) the first
  time it authenticates a not-yet-seen actor, closing the gap with the same system-actor pattern already
  shipped. **📐 Awaiting Andrew's ratification; recommended: ratify the design, shelve the build** — zero
  current/planned vertical needs self-service consumer signup (both reference verticals grant every op to
  `operator` only); build only once a real driver files. No unauthenticated door will be built.
- **Fire 5 (ops, not platform code)** — the prod reverse-proxy (`deploy/nginx.conf`: TLS termination,
  rate limiting, CORS, IP allowlisting) per the ratified Gateway Architecture Decision.
