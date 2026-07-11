# Per-identity NATS subscribe-ACL (Edge sync plane) — design

**Status: ✅ RATIFIED (Andrew, 2026-07-10) — fork A: NATS auth callout on the existing static conf.**
B (operator mode) stays the recorded evolution with its revive trigger (NATS account-level tenant
isolation demand); C (gateway proxy) stays rejected (couples EDGE.3 to the deferred EDGE.5 WS bridge).
The #11 §11.1 consumer-table row is committed with this ratification. Ratification DD verified every
vendor claim against the pinned NATS 2.14 module source (issuer/auth_users requirements, fail-closed
callout, expiry-disconnect, filtered-create pinning, NKey bypass, client filtered-create form) — zero
corrections. The Steward builds Fires 1–3; Fire 3 flips EDGE.3 build-ready. · Designer fire 2026-07-10
**🏗️ Fire 1 checkpoint (Steward, 2026-07-11).** Code COMPLETE + tested + gates green in worktree
`/tmp/lattice-worktrees/per-identity-subscribe-acl-fire1` (branch `steward-per-identity-subscribe-acl-fire1`):
`internal/gateway/natsauth` (the responder + §3.3 permission template), `substrate.ConnectOpts.Token`,
`deploy/gen-dev-nkeys` auth_callout rendering, `cmd/gateway` wiring, `internal/natsperm/auth_callout_test.go`
(6 conformance vectors against the real committed conf). Full 3-layer adversarial pass run — one HIGH
finding folded before commit: the design's §3.3 `lattice.ctrl.refractor.personal.*` control-RPC grant
depends on a §3.4 server-side identity-binding override that is Fire 2 scope and does not exist yet
(`internal/refractor/control/service.go`'s personal handlers trust `body.IdentityID` unchecked) — Fire 1
now ships WITHOUT that grant (sync-plane read confinement only); Fire 2 must land the grant and the
override together. Two MEDIUM/LOW findings also folded (identity NanoID-alphabet enforcement at the
resolved-identity check; a length cap on the CONNECT-supplied device name). `go build`/`make vet`/
`golangci-lint`/`STRICT lint-conventions`/`go test` all green; `make verify-kernel` green against the
live shared stack.
**🚧 BLOCKED — not committed — needs Andrew's one-look authorization, not a design question.** The
auth_callout mechanism needs a brand-new ACCOUNT-type NATS NKey seed
(`deploy/nkeys/auth-callout-issuer.nk`) — the same dev-only-seed convention as the 16 already-committed
`deploy/nkeys/*.nk` files (`gen-dev-nkeys`'s own doc: "committed like POSTGRES_PASSWORD: lattice_dev").
The session's auto-mode credential-leakage classifier blocked staging that one new file for commit
(a real, if dev-only, generated private key, in a confirmed-public repo, with no user present in this
unattended fire to authorize it) — it did NOT flag any of the code. Andrew: either (a) explicitly
authorize committing that one seed file (mirrors the 16 existing ones — no new precedent), or (b) it
gets minted fresh + committed together with a future *attended* fire. Nothing else about this fire is
blocked; the worktree is ready to merge the moment the seed is authorized.
**Backlog row:** [lattice.md](../planning-artifacts/backlog/lattice.md) → Security & trust boundary → *Per-identity NATS subscribe-ACL (Edge sync plane)*
**Consumers:** [Edge Lattice EDGE.3](edge-lattice-full-design.md) (§7 — the one open gate leg) · [Personal Lens Fork 3](personal-secure-lens-design.md) (subject subscribe-authorization)
**Contracts:** #11 (external actor authN — build-to, plus one staged consumer-table row, see §6) · #1 (subject shapes — build-to) · #75 design's §3.2 matrix (extends, does not alter)

---

## For Andrew (one-look ratification block)

**What it does (two lines).** An untrusted Edge connection authenticates to NATS itself with its
Contract #11 bearer JWT (via NATS **auth callout**, server-config mode); the connection is issued a
server-side permission set that allows subscribing **only its own** `lattice.sync.user.<id>` sync
subject (+ its own scoped JetStream consumer and inbox), with revocation cutting new connects
immediately and live connections at authorization expiry. This closes the one open EDGE.3 gate leg.

**The architectural fork — DECIDED: A (Andrew, 2026-07-10). How dynamic per-identity NATS authN is
minted (B/C kept as the recorded roads not taken):**

- **A — NATS auth callout on the existing static conf (my recommendation).** Keep the shipped #75
  Path-A posture (16 static NKey users, one `nats-server.conf`) untouched; add the server's
  `auth_callout` delegation for **unrecognized** connections only. A small responder hosted in
  `cmd/gateway` verifies the bearer JWT with the **same** `internal/gateway/auth` verifier +
  `token-revocation` checker + `credentialbinding` resolver every other external surface uses
  (Contract #11 §11.1's "shared verifier" invariant, now extended to the transport), and issues a
  scoped, expiring per-connection permission set. No operator-mode migration, no per-identity key
  distribution, no new state plane.
- **B — operator/JWT mode (decentralized).** The #75 §9 Path-B evolution: full `nsc` operator
  keyring, per-identity signed user JWTs minted and distributed by the Gateway, account revocation
  lists. Strictly more machinery (whole-stack credential migration, a JWT account resolver on the
  server, creds-file distribution to devices) for the same confinement; the callout responder's
  issue logic is exactly what would port to it. Right end-state if Lattice ever needs NATS
  **account-level** tenant isolation; premature for one edge consumer.
- **C — no direct NATS for untrusted edges (Gateway sync proxy).** Terminate untrusted connections
  at a Gateway WebSocket bridge that subscribes on their behalf. Rejected as the *v1*: it couples
  EDGE.3 to the deferred EDGE.5 WS bridge, re-implements JetStream consumer semantics (durables,
  acks, replay) inside a stateful proxy, and idles the shipped native-NATS Go reference node. The
  WS bridge remains the *browser* transport later (EDGE.5) — and when it lands, the bridge itself
  connects as one more callout-authenticated client, so A is not throwaway.

**Frozen-contract change: one row, staged uncommitted in `main`.** Contract #11 §11.1's consumer
table gains the NATS connect surface (the callout responder) as a consumer of the resolved actor +
the revocation key — keeping the "every surface that authenticates an external bearer token"
inventory truthful. No semantic change to §11.2–§11.6; the responder builds to them verbatim.
Everything else (the `auth_callout` server block, the permission template, the natsperm vectors) is
deploy/component surface, not contract.

**Sequencing note.** Fires 1–2 are the platform + hardening work of this design; Fire 3 flips
[EDGE.3](edge-lattice-full-design.md) build-ready. Nothing here builds dark: the consumer
(`cmd/edge` + the SYNC plane) is already shipped and waiting on exactly this boundary.

---

## 1. Problem & intent

**The gap.** The Personal Lens fans each identity's authorized deltas onto its private subject
`lattice.sync.user.<id>` (`subjects.PersonalSync`, `internal/refractor/subjects/subjects.go:43`),
and `cmd/edge` consumes it via a durable JetStream consumer on the `SYNC` stream. But the *filter*
is **client-asserted** (`internal/edge/sync/sync.go:123` — `FilterSubject:
subjects.PersonalSync(m.prefix, m.cfg.IdentityID)`): nothing at the transport binds
`cfg.IdentityID` to the connection's real identity. Every NATS user in the shipped #75 matrix has
`subscribe { allow: [">"] }` — the generator hard-codes it (`deploy/gen-dev-nkeys/main.go:405`);
the `component` struct has no subscribe field at all. So today, *any* connection that can reach the
broker can subscribe *any* identity's sync slice. The Personal Lens's PL.3 security filter decides
*what is published* per identity; nothing decides *who may listen*.

**Why it's filed as its own row (the circular dependency it dissolves).** #75
(NATS account write restriction) **explicitly declined** subscribe lockdown — §3.2: *"v1 does not
lock down subscribe (a future per-identity Edge model — Personal Lens — is where read-side subject
scoping lands)"* — while Personal Lens Fork 3 assumed the ACL *"rides the NATS-account-auth
design"*. Both shipped; neither owns the ACL. The Edge §7 gate re-verification (2026-07-10) caught
the un-owned gap and filed this row. This design is that owner.

**Intent (vision source).** Brainstorm #74 *"Edge Lattice — local-first / personal-lens runtime"*
and the morph-delta *"Personal Lens parameterized per-user NATS subject target"* (brainstorming
inventory L154/L331); the vault's Edge Lattice subdocs frame the untrusted sovereign node. The
architecture's trust model (lattice-architecture.md L38) already names *"JWT actor authentication;
token revocation KV at Gateway"* as the security spine — this design extends that spine from the
HTTP door to the NATS transport, for the first surface where an **untrusted principal holds a raw
NATS connection**.

**What EDGE.3 needs from it (the acceptance shape).** From the Edge design §7: *"the connection is
scoped by the per-identity subscribe-ACL… revoked JWT ⇒ no submit/subscribe"* and the Gate-3 Edge
read-bypass suite: *A never sees B's slice; no-grant ⇒ empty; revoked ⇒ cut.*

## 2. Grounding — the patterns this extends (do not redesign them)

- **#75 Path A is the shipped transport-auth substrate** (`nats-account-write-restriction-design.md`
  §3.2/§9, built through Fire 3): one `deploy/nats-server.conf` rendered by `deploy/gen-dev-nkeys`
  (the authoritative matrix, `main.go:104`), 16 per-component NKey users, publish allow/deny lists,
  proven by `internal/natsperm/conf_test.go` — an embedded server started from the **real committed
  conf** (`startServerFromConf`, conf_test.go:50) asserting per-user denials. `TestConfigParses`
  pins the user count; `TestPersonalSyncPublishAccess` (conf_test.go:307) already pins that **only
  refractor may publish** `lattice.sync.user.>`. This design adds the subscribe side using the same
  generator + the same conformance-test pattern.
- **Contract #11 is the identity boundary** (ratified 2026-07-10): bearer JWT → `VerifiedActor`
  via the shared `internal/gateway/auth` verifier (asymmetric-only, `kid`-selected, per-source
  binding spec; `opaque` derivation `vtx.identity.<SHA256NanoID(iss:sub)>`, dev-only `nanoid`
  passthrough). §11.4: external read boundaries resolve A→U via the shipped
  `internal/gateway/credentialbinding` resolver (deny-safe: miss ⇒ act as A). §11.5: revocation
  keys on pre-resolution A — presence in the `token-revocation` bucket = revoked
  (`internal/gateway/revocation/revocation.go:54`), gateway-only writer
  (`TestGatewayRevocationBucketWriteIsolation`).
- **The control plane already lifted actors to verified JWTs** — `internal/controlauth`'s
  `ActorVerifier` (`verified_actor.go`): the `Lattice-Actor` header carries the **token**, verified
  against the same trust root (`WireActorVerifierFromEnv`), revocation-checked. The sync plane's
  `personal.{register,deregister,hydrate}` control RPCs
  (`lattice.ctrl.refractor.personal.*`, `internal/refractor/control/service.go:476`) ride this
  seam; today `cmd/edge` self-asserts a bare key (`sync.go:243`, trusted posture).
- **The consumer machinery** — `internal/edge/sync` uses `substrate.RunDurableConsumer` → the
  `jetstream` package (**pull** consumers: `CreateOrUpdateConsumer` + `Consume`; AckExplicit),
  durable `edge-sync-<identityID>-<deviceID>`, filter `lattice.sync.user.<identityID>`. The
  identity id is always the **final single token** of the subject (`validateToken` rejects
  `.`/`*`/`>`/whitespace — subjects.go:10).
- **`substrate.ConnectOpts` already carries both credential shapes** (`conn.go:33-42`):
  `NKeySeedFile` (Path A, used everywhere) and `CredsFile` (operator mode, plumbed-but-unused).
  The edge's bearer-token connect adds the third: `Token`.
- **Vendor pins** (docs/vendors.md): **NATS 2.14** (`nats-server v2.14.0`, `nats:2.14-alpine`),
  client `nats.go`; authoritative sources <https://docs.nats.io> + the
  `nats-io/nats-architecture-and-design` ADRs. Auth-callout specifics in §3.1 are cited against
  these (fetched during this fire, not training-prior).

**Parallel-design check (in-flight overlap).** The 📐 global-identity-hyperscale design touches
identity but not the NATS transport; the multi-credential linking row (📋, authN §12.2) touches
A→U *resolution* — this design consumes resolution through the same `credentialbinding` seam, so a
later merge/rebind mechanism slots in without touching the ACL (a live connection's grant is
TTL-bounded, §3.5). No other in-flight design proposes a NATS authN mechanism (grep:
`auth_callout|subscribe-ACL|operator mode` across `_bmad-output/implementation-artifacts/*.md` —
only the four docs already named here reference the gap).

## 3. The shape

### 3.1 The mechanism — NATS auth callout, server-config mode (vendor-grounded)

NATS's **auth callout** delegates connection authorization to an external service: the server sends
each delegated CONNECT to a request subject; the responder returns a signed **user JWT** whose
permissions the server enforces for that connection. Key properties this design relies on (each
verified against the NATS 2.14 pin — citations in §3.1a; **no version fork**: callout is ≥2.10,
the filtered-create form ≥2.9, we pin 2.14):

1. **Config-mode shape.** An `auth_callout` block inside the existing `authorization {}` block:
   `issuer` (a public **account** NKey whose seed the responder holds — required), `auth_users`
   (required non-empty: the config-defined users that **bypass** the callout; NKey users are listed
   by public key), optional `xkey` (x25519 payload encryption). Every connection *not* in
   `auth_users` — i.e. every untrusted edge connect — is delegated; the 16 internal users
   authenticate exactly as today.
2. **Fail-closed.** Responder down/timeout ⇒ unauthorized; empty or error response ⇒ auth
   violation. Omission denies; nothing about a broken callout widens access.
3. **Scoped issue.** The responder receives the CONNECT's options (token/user/pass/name/TLS state)
   inside a server-signed request JWT and answers with a user JWT — signed by the config `issuer`
   key, subject = the server-minted per-connection user nkey (replay-proofed), audience = the
   target account — carrying explicit `allow`/`deny` pub/sub lists (default-deny for everything
   unlisted) and an `exp` the server enforces by **disconnecting the client at expiry**
   (`User Authentication Expired`), which is what makes revocation structural (§3.5).
4. **JS-API permission pinning is server-enforced.** The filtered-create subject form
   `$JS.API.CONSUMER.CREATE.<stream>.<consumer>.<filter>` exists exactly so a permission can pin
   the filter: the server compares the subject's filter tokens against the request body's
   `FilterSubject` and rejects a mismatch (`JSConsumerCreateFilterSubjectMismatchError`). Pull
   fetch is a publish to `$JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>` with delivery to the
   client's own reply inbox.

### 3.1a Vendor citations (NATS 2.14 pin — fetched this fire, per docs/vendors.md)

Authority order per the vendors row: pinned source ≥ docs/ADR prose. Pinned source paths are the
module cache for `nats-server/v2 v2.14.0`, `nats.go v1.52.0`, `jwt/v2 v2.8.1`.

- **Config shape + bypass semantics:** docs
  <https://docs.nats.io/running-a-nats-service/configuration/securing_nats/auth_callout> ("The
  list of user names or nkeys under `account` that are designated auth callout users") + ADR-26
  <https://github.com/nats-io/nats-architecture-and-design/blob/main/adr/ADR-26.md> ("unless
  specified in auth_users the callout service will be called regardless"); pinned
  `server/opts.go` `parseAuthCallout` (issuer = valid public account nkey, required; auth_users
  required non-empty; account defaults `$G`); `server/auth.go` ~303 ("Check for user in users and
  **nkeys** since this is server config") — NKey users are valid `auth_users` entries, matched by
  `c.getRawAuthUser()` = the public key string.
- **Fail-closed:** pinned `server/auth_callout.go` — response timeout leaves `authorized = false`
  (timeout = `auth_timeout`); empty/error responses are auth violations. Reinforced by the
  v2.12.6 release note "Client connections are no longer registered after an auth callout
  timeout (#7932)".
- **Request/response protocol:** ADR-26 + pinned `server/auth_callout.go`
  (`AuthCalloutSubject = "$SYS.REQ.USER.AUTH"`; `ConnectOptions` carries
  `{JWT, Nkey, Token, Username, Password, Name, …}` — the bearer token arrives in `token`;
  response subject/audience/issuer all verified; config mode rejects `issuer_account`). With
  `xkey` set the payload is sealed to the service's curve key (`Nats-Server-Xkey` header).
- **Issued-JWT surface:** pinned `jwt/v2@v2.8.1/user_claims.go` — `UserPermissionLimits{Permissions
  {Pub, Sub Allow/Deny}, Limits, BearerToken, AllowedConnectionTypes}` + `exp`.
- **Expiry-disconnect:** pinned `server/auth_callout.go:322` (`c.setExpiration(arc.Claims(),
  expiration)`) → `server/client.go` `authExpired()` — `sendErrAndDebug("User Authentication
  Expired")` + `closeConnection(AuthenticationExpired)`.
- **Filtered consumer create:** pinned `server/jetstream_api.go` — `JSApiConsumerCreateExT =
  "$JS.API.CONSUMER.CREATE.%s.%s.%s"` ("consumer name and optional filter subject, which when
  part of the subject controls access") + the `FilterSubject != filteredSubject ⇒
  JSConsumerCreateFilterSubjectMismatchError` check; v2.9.0 release note ("secure the creation of
  consumer… `$JS.API.CONSUMER.CREATE.<stream>.<subject>.<filter>` (#3409)"). Client side: pinned
  `nats.go@v1.52.0/jetstream/consumer.go:319` uses exactly this form whenever a single
  `FilterSubject` is set — which is what `substrate.ConsumerSupervisor.createConsumer` sets for
  the edge consumer. Pull delivery lands on the client's reply inbox (ADR-13).
- **Custom inbox prefix:** pinned `nats.go:1571` `CustomInboxPrefix` (every inbox becomes
  `<prefix>.<nuid>`).
- **Client kick (deferred fire):** v2.10.0 release note — `$SYS.REQ.SERVER.<id>.KICK` "disconnect
  a client by id" (#4298); pinned `server/events.go` (`clientKickReqSubj`, `KickClientReq{CID}` —
  CID-only in 2.14). Requires a configured system account; the deferred active-kick fire carries
  that prerequisite.
- **Operator-mode revocation (fork B comparison):** docs
  <https://docs.nats.io/using-nats/nats-tools/nsc/revocation> — account-JWT `revocations` pushed
  via the resolver; 2.14 actively disconnects revoked users on account-claims update (pinned
  `server/accounts.go`, incl. callout-issued users via `calloutIAT`, v2.10.17 #5555).

### 3.2 The callout responder — `internal/gateway/natsauth`, hosted in `cmd/gateway`

A small NATS responder subscribed to the callout request subject, wired into the existing
`cmd/gateway` main (the Gateway already holds every dependency — this is the "simplest extension of
what exists", not a new binary):

```
verify:    internal/gateway/auth.Verifier      — Contract #11 §11.2/§11.3 (the shared verifier)
revoke:    internal/gateway/revocation.Checker — §11.5 (pre-resolution A; presence = revoked)
resolve:   internal/gateway/credentialbinding  — §11.4 (A→U; miss ⇒ act as A, deny-safe)
issue:     a user JWT signed with the callout issuer seed, permissions from §3.3,
           expiry = min(token exp, now + maxAuthzTTL)
```

Flow per delegated CONNECT: extract the bearer token from the client's connect opts → `Verify`
(signature, `kid`, binding spec, `exp`, issuer pin) → `IsRevoked(A)` → resolve `A→U` → build the
§3.3 permission template for `U` → sign + return. Any failure ⇒ reject (deny). The responder never
reads Core KV (P2/P5 untouched: `token-revocation` and `credential-bindings` are gateway-owned
operational/read-model buckets it already consumes on the HTTP path).

**Confinement invariant (the namespace-input rule).** Every subject embedded in the issued
permission set derives from the **verified** actor id — never from a client-asserted field (not the
CONNECT username, not a requested-permissions hint, not the sync config's `IdentityID`). The id is
structurally wildcard-safe: under `opaque` binding it is a `SHA256NanoID` (canonical NanoID
alphabet — no `.`/`*`/`>` possible); under the dev-only `nanoid` binding, `IsValidNanoID` already
rejected anything else at verification. The responder additionally re-asserts
`subjects.PersonalSync`'s `validateToken` on the id before templating — defense in depth at the
point of permission construction.

**Multi-instance.** The responder joins a queue group; N gateways = N interchangeable verifiers
(the callout is plain request-reply). No coordination state.

### 3.3 The per-connection permission template

**A NATS-wildcard constraint shapes this template:** NATS wildcards match whole tokens only — there
is no partial-token wildcard, so a durable *family* like `edge-sync-<U>-*` (one dash-joined token)
cannot be expressed as a permission pattern. The template therefore pins **exact** subjects per
connection: the identity comes from the verified token; the **device id** comes from the CONNECT's
client `name` (which `cmd/edge` sets to its `EDGE_DEVICE_ID`). The device id is *not* a security
input — durables embed the verified `<U>`, so a fabricated device name can only rename a durable
inside the identity's own family — but the responder still validates it as a safe single token
(fail-closed on absence or unsafe characters) before templating.

For a verified credential `A` resolved to effective identity `U` (`= A` when unclaimed) and
declared device `D`, with durable `edge-sync-<U>-<D>` (the shipped `internal/edge/sync` naming):

| | Allow | Why |
|---|---|---|
| **subscribe** | `lattice.sync.user.<U>` | the identity's own sync subject — the whole point |
| | `_INBOX.edge.<U>.>` | its own reply-inbox namespace only (`cmd/edge` sets `nats.CustomInboxPrefix("_INBOX.edge.<U>")`) — a shared `_INBOX.>` grant would let one edge sniff every other client's request-reply traffic. Devices of one identity share the namespace (same trust domain — they already share the sync slice). |
| **publish** | `$JS.API.CONSUMER.CREATE.SYNC.edge-sync-<U>-<D>.lattice.sync.user.<U>` | filtered-create pinning: the consumer **name** must be its own durable AND the **filter** must be its own subject — a foreign filter or foreign-identity durable name is unmatchable, not merely rejected |
| | `$JS.API.CONSUMER.MSG.NEXT.SYNC.edge-sync-<U>-<D>` | pull fetch on its own durable only (a `*` name grant here would allow draining other identities' durables on `SYNC` — over-grant; exact-pinned instead) |
| | `$JS.API.CONSUMER.INFO.SYNC.edge-sync-<U>-<D>` · `$JS.API.CONSUMER.DELETE.SYNC.edge-sync-<U>-<D>` | the `jetstream` client's consumer lookup (`ConsumerSupervisor` resolves before pumping) + its own cleanup (`Remove`/`Reset`) |
| | `$JS.ACK.SYNC.edge-sync-<U>-<D>.>` | AckExplicit acks are publishes to the per-consumer ack subject |
| | `lattice.ctrl.refractor.personal.register` / `.deregister` / `.hydrate` | the three sync-plane control RPCs (§3.4 binds their payload identity server-side) |

Everything else — every other subject, every other `$JS.API` verb, all publishes to
`lattice.sync.user.>` (delta forgery), `core-operations` (EDGE.3 writes go through the Gateway HTTP
door), every KV bucket — is **denied by omission** (allow-list semantics). The template is a
constant in `internal/gateway/natsauth`, unit-tested as data.

### 3.4 The control-RPC identity binding (closing the payload seam)

`personal.{register,hydrate}` requests carry `IdentityID` in the JSON body, and today the Refractor
trusts it (trusted posture). Transport ACL alone can't fix this — the *subject* is shared. Under
this design:

- `cmd/edge` stamps the `Lattice-Actor` header with its **bearer JWT** (not the bare key), the same
  header the control plane's `ActorVerifier` already verifies for Weaver/Loom/Refractor operators.
- The Refractor's personal handler, when an `ActorVerifier` is configured, **overrides**
  `body.IdentityID` with the verified actor's resolved id (and rejects a mismatching non-empty
  body value as a client bug). No verifier configured (dev/e2e fixtures) ⇒ today's behavior.

This mirrors the write path's verify-and-stamp (the Gateway overwrites `env.Actor`; Contract #11
§11.1) — the payload field becomes display/debug, never authority.

### 3.5 Revocation — structural, two-layer

- **New connects:** the responder checks `IsRevoked(A)` per CONNECT — a revoked credential cannot
  re-establish. Immediate.
- **Live connections:** the issued authorization **expires** at `min(token exp, now + maxAuthzTTL)`
  (default `maxAuthzTTL` 15m; deployments with short token TTLs get the tighter bound
  automatically) and the server disconnects at expiry; the reconnect re-runs the callout against
  the revocation bucket. Worst-case revocation latency for an already-open subscription =
  `maxAuthzTTL` — the same "capability-projection lag is backstopped by expiry" posture Contract
  #11 §11.2 already commits to for the HTTP surfaces.
- **Active kick (deferred, named consumer).** A `token-revocation` watcher issuing
  `$SYS.REQ.SERVER.<id>.KICK` (by CID; ≥2.10) would cut live connections in seconds. Deferred by
  the dead-scaffolding test: no current deployment runs token TTLs long enough for the 15m bound
  to matter; the consumer that would justify it is a production posture with long-lived tokens.
  That fire also carries its own prerequisite (a configured NATS system account — the dev conf has
  none today) and composes without touching this design's surface.

Resolution drift (a future §12.2 merge/rebind changing A→U) is bounded the same way: a live
connection holds the old `U`'s grant at most `maxAuthzTTL`.

### 3.6 Read path (P5) / write path (P2) / state (P1) summary

Unchanged, deliberately. The edge still reads **only** the Personal-Lens projection (SYNC stream —
P5); EDGE.3 writes still go Gateway → `core-operations` → Processor (P2); no new state plane (P1):
the responder consumes two existing gateway-owned buckets and holds one new NKey pair (the callout
issuer) in `deploy/`, generated by `gen-dev-nkeys` exactly like the 16 user seeds.

## 4. What this does NOT do (scope fences)

- **No account-level tenant isolation.** Edge connections land in the same (global) account,
  confined by permissions — the fork-B account model stays the documented evolution. The name on
  the backlog row ("NATS-account subscribe-ACL") described the *era* (#75's naming), not the
  mechanism; the row's own text already says "per-identity".
- **No WS/push browser transport** — EDGE.5's bridge, unchanged by this design (and served by it:
  the bridge will connect as a callout-authenticated client per browser identity or as a trusted
  bypass user, its own design's call).
- **No change to internal components' NATS posture** — the 16 NKey users bypass the callout and
  keep `subscribe: ">"`. Tightening internal subscribe scoping stays the #75 §8 "accepted
  intra-trust latitude" note. One deliberate consequence: internal components can still subscribe
  `lattice.sync.user.>` (e.g. Loupe-the-inspector) — the boundary this design draws is
  trusted-vs-untrusted, not internal-vs-internal.
- **No per-token (`jti`) revocation** — §11.5 reserves it; the ACL keys on the actor, like every
  other surface.

## 5. Reconciliation with the existing mental model

- *Didn't #75 already lock the transport down?* Its **publish** side, yes — that's the shipped
  write-isolation matrix. Subscribe was explicitly declined in v1 (§3.2) and every user still holds
  `subscribe: ">"`. This is the read-side twin #75 predicted would land "in the Personal-Lens /
  Edge model".
- *Didn't Personal Lens Fork 3 own this?* It **assumed** it rides #75 ("identity X's NATS user may
  SUB lattice.sync.user.X only") — but #75's users are the 16 static components; there is no
  per-identity user to attach that permission to. The Edge §7 gate re-verification named this
  circularity; this design is the resolution. Fork 3's *intent* (per-connection subscribe
  permission on the exact subject) is delivered verbatim — only the minting mechanism (callout,
  not a static user) is new.
- *Does this duplicate the operator-mode (Path B) evolution?* No — it sequences *toward* it. The
  permission template (§3.3) is the durable artifact; under a later operator migration the same
  template becomes the minted user JWT's claims and the responder becomes the minter. Nothing here
  is thrown away except the `auth_callout` server block itself.
- *New state?* None. The two lookups (revocation, resolution) are existing gateway-owned buckets;
  the issuer key is one more generated seed in `deploy/`.
- *Why is the Gateway the host and not the Refractor (which owns the SYNC plane)?* The decision
  input is **identity**, not projection: verifier + revocation + resolution are all
  `internal/gateway/*` seams the Gateway already wires for the HTTP door. Hosting in the Refractor
  would drag the identity stack into the projection engine and split Contract #11's "shared
  verifier" across components.

## 6. Contract surface

- **Contract #11 — build-to, plus one staged row.** The responder implements §11.2 (profile),
  §11.3 (binding), §11.4 (resolution: it is an external read boundary — it resolves), §11.5
  (revocation on pre-resolution A). Staged **uncommitted** edit: §11.1's consumer table gains one
  row — the NATS connect surface (auth-callout responder) consuming the resolved actor (for the
  permission template) + the revocation key — so the surface inventory stays complete. Affected
  consumers: none (documentation of a new conformer; no existing surface changes).
- **Contract #1 — build-to.** `lattice.sync.user.<id>` and the control subjects are existing
  dotted-subject shapes; the identity token is validated single-segment. No new key shapes.
- **#75's design doc** gets its §3.2 "v1 does not lock down subscribe" sentence a one-line pointer
  to this design at build time (a design doc, freely editable — not a contract).
- **NOT contract surface:** the `auth_callout` conf block, the permission template, the natsperm
  vectors, `ConnectOpts.Token` — all deploy/component code documented in
  `docs/components/gateway.md` + `edge.md` by the building fire.

## 7. Migration & compatibility

- **Internal components: zero change.** All 16 NKey users enter `auth_users` (bypass); their
  connects, permissions, and the entire existing natsperm suite are untouched. `TestConfigParses`'s
  user-count pin is updated in the same fire that regenerates the conf.
- **`cmd/edge` dev flow:** `EDGE_TOKEN` (a dev-key-minted JWT — the existing dev minter; `nanoid`
  binding) replaces the anonymous connect; `EDGE_ACTOR_KEY` self-assertion retires at EDGE.3 per
  the Edge design. `substrate.ConnectOpts` gains `Token string` (third credential, same
  exactly-one-of guard as `conn.go:77`).
- **Embedded-NATS test fixtures** that don't load `deploy/nats-server.conf` see no change (no
  callout configured ⇒ no delegation). `internal/natsperm/conf_test.go` starts the real conf **plus
  an in-process responder** (the same package wiring `cmd/gateway` uses) for the new vectors.
- **Rollback:** removing the `auth_callout` block restores today's posture (edge connects rejected
  by the authorization block, since no anonymous user exists) — fail-closed in both directions.
- **Prod posture:** the callout issuer seed joins the same secret-injection story as the user seeds
  (#75 Fire 4, deferred); `xkey` payload encryption is enabled from day one (one more generated
  key, negligible cost, removes the bearer-token-in-cleartext-payload consideration entirely).

## 8. Test strategy

Extends `internal/natsperm/conf_test.go` (the established embedded-real-conf pattern) with an
in-process responder; plus responder unit tests in `internal/gateway/natsauth`.

Conformance vectors (the security proof, colocated per house rule):

1. **Own-slice subscribe allowed:** token for A ⇒ consumer on `SYNC` filtered
   `lattice.sync.user.<A>` delivers.
2. **Cross-identity denied at every rung:** raw `SUB lattice.sync.user.<B>` denied;
   `CONSUMER.CREATE` with B's filter denied (unroutable); `MSG.NEXT` on B's durable denied.
3. **Fail-closed callout:** responder down ⇒ connect denied; malformed/expired/unknown-`kid`
   token ⇒ denied; empty token ⇒ denied.
4. **Revocation:** key present in `token-revocation` ⇒ connect denied; live connection with a
   short-expiry authorization is disconnected at expiry (short `maxAuthzTTL` in-test).
5. **Bypass intact:** all existing internal-user vectors still green with the callout block
   present (the regression fence for the 16 users).
6. **Delta forgery denied:** the edge token may not publish `lattice.sync.user.<A>` (its own) nor
   `core-operations`.
7. **Inbox isolation:** A may not subscribe `_INBOX.edge.<B>.>`.
8. **Control-RPC binding:** `personal.hydrate` with a verified actor A and a body claiming B is
   served as A (override) — Refractor-side test.

EDGE.3's Gate-3 Edge read-bypass suite (Edge design §5) then composes these end-to-end
(A never sees B's slice; revoked ⇒ no subscribe/submit).

## 9. Risks & alternatives

- **Alternative — per-identity static users (rejected):** identities are unbounded and runtime-born;
  a conf rewrite + reload per provisioned identity is operationally absurd and leaves a
  window-of-no-user. Non-starter; named for completeness.
- **Alternative — application-layer gating only (rejected):** have the Refractor filter
  subscriptions… it cannot — JetStream delivers to whoever holds a consumer; the subscriber is not
  an application seam. Only a server-side permission is a boundary (the same reasoning that made
  #75 substrate-side, §8: a same-trust-domain guard is not a boundary).
- **Alternative — B/C (operator mode / Gateway proxy):** designed through in the For-Andrew fork
  block; both remain evolutions, neither beats A for the current consumer. Re-asked per the
  alternatives discipline: *could a variant win?* — B-lite (mint per-identity **creds files**
  against a static account without full operator mode) was considered: NATS static conf has no
  account-signing-key seam for that (creds files are an operator-mode artifact), so it collapses
  into B proper. C-lite (proxy only the SYNC subscription, keep control RPCs direct) inherits C's
  statefulness for no scope reduction.
- **Risk — callout on the connect path adds a hop:** one request-reply per CONNECT (not per
  message). Bounded: reconnects are `maxAuthzTTL`-paced per device; the responder is queue-grouped
  and stateless. A broker-local cache is unnecessary at any plausible edge population (a CONNECT
  every 15m per device; 10k devices ≈ 11 req/s).
- **Risk — responder availability now gates edge connects:** intentional (fail-closed). The
  Gateway is already the availability spine for edge writes; sync-plane connects joining it is
  posture-consistent, and an established connection is unaffected until expiry.
- **Risk — `auth_users` bypass list drift:** a new internal component added to the matrix but not
  the bypass list would get delegated (and denied — fail-closed, loud in dev). The generator emits
  both from the same `matrix` slice, making drift structurally impossible.
- **Risk — device-count consumer growth:** each (identity, device) pair creates one durable on
  `SYNC`. Bounded operationally by the stream's short retention + consumer inactivity threshold
  (set `InactiveThreshold` on the edge consumer so abandoned devices GC — already the `jetstream`
  package's native mechanism); noted for the building fire, not a new mechanism.

## 10. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

> ✅ Ratified 2026-07-10 (fork A); the #11 §11.1 row committed with the ratification.

1. **Fire 1 — the callout boundary (platform).** `internal/gateway/natsauth` (responder: verify →
   revoke → resolve → issue; permission template as tested data) + `substrate.ConnectOpts.Token` +
   `gen-dev-nkeys`: emit the `auth_callout` block (issuer + xkey keys, `auth_users` = the matrix),
   regenerate `deploy/nats-server.conf` + wire the responder into `cmd/gateway` main. natsperm
   vectors 1–7 (§8). **Green:** full suite + the new vectors; every existing internal-user vector
   unchanged.
2. **Fire 2 — the consumer + the payload seam.** `cmd/edge`: `EDGE_TOKEN` connect,
   `CustomInboxPrefix("_INBOX.edge.<id>")`, JWT-in-`Lattice-Actor` on control RPCs. Refractor
   personal handler: verified-actor override of `body.IdentityID` (+ vector 8). **Green:** edge
   unit/e2e under the authenticated posture (embedded server + in-process responder).
3. **Fire 3 — the EDGE.3 handoff.** Revocation e2e (vector 4 end-to-end against the live dev
   stack), update the Edge design §7 gate + the board (EDGE.3 → build-ready), point #75 §3.2 at
   this design. **Green:** `make up-full` battery with an edge node connected via token.

Deferred, named: **active-kick watcher** (§3.5 — revive on a long-lived-token production posture);
**operator-mode migration** (fork B — revive on account-isolation demand).

## 11. Open questions — resolved

- *Same account or a dedicated edge account?* Same account, permission-confined (v1); account
  isolation is fork B's territory. Cross-account JS import/export for the SYNC stream is real
  machinery with no current payoff.
- *Who mints dev edge tokens?* The existing dev minter (dev key, `nanoid` binding) — no new tooling.
- *Does the responder resolve A→U?* Yes — it is an external read boundary (Contract #11 §11.4
  binds the resolving set to one rule; the sync slice is keyed by the same resolved actor the RLS
  read path uses, so the subscribe grant must match or a claimed user's edge sees nothing).
- *`maxAuthzTTL` default?* 15m (≥ typical dev token TTL; bounded revocation window; ~1 CONNECT per
  device per 15m). Env-tunable on the Gateway (`EDGE_SYNC_AUTHZ_TTL`), floor-clamped at 1m.

## 12. Adversarial pass (run this fire — the §8-style gate is discharged, not deferred)

Findings folded into the body above; recorded here so the gate is visibly closed:

- **Default-open check:** every path enumerated (no token / bad token / responder down / revoked /
  unknown kid / no binding spec) denies. The one default-open candidate found — granting subscribe
  on the client-declared inbox prefix verbatim — was closed by pinning the grant to
  `_INBOX.edge.<U>.>` derived from the *verified* id (§3.3), not the CONNECT payload.
- **Wildcard-mechanics check (a real catch this fire):** the first-draft template used
  `edge-sync-<U>-*` as a durable-family pattern — invalid, since NATS wildcards match whole tokens
  only and the durable name is one token. A naive "fix" of granting `MSG.NEXT.SYNC.*` would have
  been an **over-grant** (drain any identity's durable). Closed by exact per-connection pinning
  with the device declared at CONNECT (§3.3) — the identity, the only security discriminator,
  never comes from the client.
- **Over-grant/retraction check:** no projected grant rows exist (nothing to retract); the only
  standing grant is the in-flight authorization, whose retraction transport is **named** — server
  disconnect at expiry + revocation check on reconnect (§3.5). No upsert-only-shrink hazard.
- **Confinement check:** all namespace inputs (subject id, durable family, inbox segment) bind to
  the §11.3 trust-source derivation; wildcard injection structurally impossible (NanoID alphabet) +
  re-asserted (`validateToken`).
- **Transport check (the assumed-channel reflex):** the control-RPC identity was found to be a
  payload assertion the transport ACL cannot fix — closed as §3.4 (verified-header override), not
  assumed away.
- **Bypass-surface check:** with the callout on, the untrusted path cannot reach `core-operations`,
  any `$KV.*`, stream-admin verbs, or another identity's consumer subjects — allow-list omission,
  vector-pinned (§8 v2/v6).
