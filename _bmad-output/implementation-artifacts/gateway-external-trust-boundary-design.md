# Gateway — the external trust boundary (design)

**Status:** ✅ **Andrew-ratified (2026-06-29)** · Designer fire (Winston, 2026-06-29) · Lattice lane (Security & trust boundary)

---

## For Andrew

**What it does (two lines).** A thin Go **translator** (`internal/gateway/gateway.go` + a `cmd/gateway`
binary) terminates external HTTP requests, verifies the IdP-signed JWT with the **already-built**
`internal/gateway/auth` Verifier+Authenticator, **stamps the verified actor** into the operation
envelope (write path) and propagates it as `lattice.actor_id` to the read boundary (read path), then
publishes to `core-operations` / proxies the lens read — making `env.Actor` **unforgeable for external
actors**. It is the *authentication* seam that closes actor impersonation, the complement to the ratified
NATS-write-restriction (#75, *transport*-authZ) and the Capability KV (*actor*-authZ).

**It builds to existing contracts — no frozen-contract change is required.** Contract #2 §2 already
*reserves* this exact stamping (line 39: "*Short forms … reserved for HTTP headers in Phase 2 (Gateway
translates to full key before envelope submission)*"), the architecture already *ratifies* the topology
(§"Gateway Architecture Decision": reverse-proxy + thin translator), and D1.2 already *built* the verifier.
This design fills the one undesigned gap: the **translator + its actor-trust model + the dev/prod IdP
story + the build sequencing** — i.e. the architectural core that the "defer the full Gateway to ops"
note in the D1 design left unspecified, separated cleanly from the genuinely-ops parts (NGINX tuning, TLS
certs, DDoS).

**Architectural forks — designed through, with my recommendation; the call is yours:**

| Fork | Options | My recommendation |
|---|---|---|
| **F2 — actor-trust model** *(the core fork)* | **(A)** Gateway verifies + stamps; Processor *trusts* `env.Actor` because transport-authZ (#75) lets only the Gateway's NATS user publish `core-operations`. **(B)** Every op carries the JWT; the **Processor verifies** it at step-3 (brainstorm #118). | **A.** It is the ratified architecture's model, adds **zero hot-path crypto**, needs **no Contract #2 change**, and #75 Fire 2 (already ratified) is *exactly* the structural guarantee A rests on. B is a defensible zero-trust posture but pays commit-path latency + a frozen-contract field for a threat (a compromised in-boundary Gateway) that mTLS + #75 already bound. **Hard sequencing gate: Gateway Fire 1 lands with/after #75 Fire 2** (else A's trust is only "undetectable-without-EventList"). |
| **F3 — IdP posture** | **(A)** External IdP in prod (the deployment's OIDC provider; Gateway loads its JWKS) + a **self-hosted dev signer** for dev/CI. **(B)** Lattice owns an identity provider / signing keys. | **A.** `auth.go` already states "*Lattice does NOT own actor signing keys (external IdP/KMS)*" — B contradicts the architecture, is large scope, and re-assumes key custody Lattice explicitly disowns. |
| **F4 — external read transport** | **(A)** Extend the translator for reads (one binary, one auth seam). **(B)** A separate read-authorization service. | **A.** DRY auth+revocation; one external surface. Sequence the read endpoint **behind D1.3's first protected Postgres read-model** (chain-grounding). |
| **F5 — build sequencing** | **(A)** Build the write-path translator **now** (ratify-now / build-Fire-1, like HA-NATS & D1). **(B)** Ratify the design, shelve the whole build behind a prod driver. | **A.** Fire 1 is buildable now and high-value: it closes external actor impersonation alongside #75 Fire 2 and gives the verticals' apps a real submission front instead of self-asserting. Fires 3–5 sequence behind D1.3 / prod. |

**Frozen-contract change:** **NONE staged.** Build-to #2 (§2.34 actor, §2.39 stamping reservation, §2.3/§2.6
lane-auth), #6 (capability), #9 (claim flow), #5 (health). I considered freezing the **JWT format** as a
new *Contract #11 (Gateway / actor-authN)* and **recommend deferring it** until a second JWT consumer
exists (the Edge node, or control-plane authz Fire 2); for now the format lives in the new
`docs/components/gateway.md` (not frozen). If you want it frozen now, say so and I'll stage the §11 edit
uncommitted.

**Ratified (Andrew, 2026-06-29).** **F2 = A** (Gateway-stamp + transport-trust — no per-op crypto, no Contract #2
change), **F3 = A** (external IdP + fail-closed dev signer), **F4 = A** (extend the one translator for reads),
**F5 = A** (build Fire 1 now). **Contract #11 (JWT format) = deferred** (freeze when a 2nd consumer needs it; for
now in `gateway.md`, not frozen) → **no frozen-contract change.** **Decomposition collapsed (fewer-larger-fires
steer):** the whole buildable-now **write surface is ONE fire** (translator + dev signer + JWKS + claim-front +
health + `gateway.md`); the **read-front is a 2nd fire behind D1.3** (Lattice consuming the Verticals D1.3
protected read-model **one-way**); the prod reverse-proxy is **ops, not a Steward fire.** **Hard build gate:**
the write-surface fire lands **with/after #75 NATS-write-restriction Fire 2b (the enforcement turn-on)** — which
is **NOT yet live** (only #75 Fire 2 inc-1, the offline conformance artifact `2e10ba7`, shipped); until Fire 2b
enforces "only the Gateway may publish `core-operations`," stamping is no better than self-assert. Sequence them
as a pair.

---

## 1. Problem & intent

**The hole.** The operation envelope carries `actor` as a **self-asserted JSON field**
(`internal/processor/envelope.go:54`; Contract #2 §2.34). The Processor's step-3 reads `env.Actor` and
looks it up in Capability KV — but **nothing authenticates that the submitter *is* that actor**. Anyone
who can publish to `core-operations` can submit an op carrying *any* `actor` and inherit its capabilities.

This is brainstorm **#118** verbatim: *"the `Lattice-Actor` header trust model… if only the entry-gateway
'trusts itself' to set the actor, any internal misbehavior = total impersonation. Need: actor claim as a
signed JWT keyed by Identity vertex."* It is also #27 (*actor authentication: signing key → JWT → header*)
and #111 (*token-revocation kill-switch*).

**Why now / why it's grounded, not speculative.** The layered trust model is mostly built or ratified —
the Gateway is the **last unbuilt layer**:

| Question | Mechanism | Status |
|---|---|---|
| *Which connection may publish which subject?* (transport-authZ) | NATS user + permission set | **#75 ratified**, Fire 1 shipped, Fire 2 = the enforcement turn-on |
| **Whom is an operation from?** *(actor-authN)* | **Verified JWT → stamped `env.Actor`** | verifier **built** (D1.2 `internal/gateway/auth`); **translator + write-path stamping = THIS design** |
| *Is the actor allowed the op/read?* (actor-authZ / ReBAC) | Capability KV (write) + Capability-Read Lens / RLS (read) | write **shipped**; read **building** (D1.3) |

D1.2 wired the verifier only for the **read** actor and explicitly deferred "*the full internet-facing
Gateway… to ops" (read-path-authorization-d1-design.md §Fork 2). The **write-path** actor is still
self-asserted, and there is no HTTP front at all (`internal/gateway/gateway.go` confirmed absent). The
Gateway closes the write path **and** becomes the external front for D1.3 read enforcement. Its foundation
(verify + revoke) is already in `main` — so this is a *chain-grounded* design (the producer is built), not
a castle on unbuilt foundations.

**Intent.** Make `env.Actor` unforgeable for external actors with the minimum, ratified-topology surface:
a thin translator that verifies once at the edge and stamps, while internal service actors keep their
sanctioned direct-submit path (architecture line 93). Nothing in the Processor commit path changes.

---

## 2. Grounding — the built foundation and the pattern this mirrors

- **`internal/gateway/auth`** (built, D1.2). `Verifier.Verify(token) → VerifiedActor{ActorID:"vtx.identity.<sub>", …}`
  and `Authenticator.Authenticate(ctx, token)` (verify + revocation, fail-closed). Asymmetric-only
  (RS*/ES*; HS*/`none` refused), `kid`-selected key (no implicit fallback), `exp` required + skew-bounded,
  `iss`/`aud` optional-enforced. **This design consumes it as-is** — it does not re-implement verification.
- **`internal/gateway/revocation`** (built). `Checker.IsRevoked(ctx, actorID)` over the `token-revocation`
  bucket (`config.yaml` `gateway.tokenRevocationBucketName`). Already composed into the Authenticator.
- **Architecture "Gateway Architecture Decision"** (lattice-architecture.md §"Gateway Architecture
  Decision"): **reverse proxy (NGINX/Envoy)** owns TLS/rate-limit/CORS/DDoS as *infra config* in `deploy/`;
  the **thin Go translator** (`internal/gateway/`) owns *Lattice-specific* JWT validate + `Lattice-Actor`
  stamp + revocation check + HTTP→NATS publish. **This design builds to that split** (it does not
  re-litigate it — a custom internet-facing Go gateway is the rejected alternative, §7).
- **Contract #2** — §2.34 `actor` field; **§2.39 reserves header→full-key stamping by the Gateway**;
  §2.3/§2.6 lane-auth + `LaneUnauthorized` (already enforced, shipped). The Gateway *stamps the field the
  contract already specifies* — build-to, not change.
- **Contract #9** — Identity Claim Flow (the claim plaintext never enters Lattice; the client submits only
  `sha256(plaintext)`). The Gateway fronts `CreateUnclaimedIdentity`/`ClaimIdentity` without touching that
  invariant (§3.3).
- **NATS-write-restriction design (#75)** — the layered-trust framing this design slots into: *"a client
  that can publish to `core-operations` can still submit a forged `Lattice-Actor` header. That residual is
  closed by the authentication seam (verified JWT → `lattice.actor_id`)."* That residual **is this design**.
- **Precedent mirrored:** Loupe is the established "trusted single-identity, binds 127.0.0.1, no authN"
  HTTP-over-substrate app (`cmd/loupe`); the Gateway is its **authenticated multi-identity sibling** — same
  HTTP→`substrate.Conn`→`core-operations` shape, plus the verify-and-stamp seam. The translator reuses
  Loupe's `writeJSON`/`requireConn`/route-mux idiom and its loopback-guard discipline.

---

## 3. The shape

### 3.1 Write path — `POST /v1/operations` (the keystone)

```
external client                 Gateway translator                     core-operations → Processor
──────────────                  ──────────────────                     ───────────────────────────
HTTP POST /v1/operations        1. Authenticate(Bearer JWT)            (unchanged) step 3 reads
  Authorization: Bearer <JWT>      → VerifiedActor.ActorID              env.Actor, looks up Capability KV
  body: { operationType,        2. parse envelope; STRIP any
          payload, lane,           client-supplied `actor`
          authContext,          3. STAMP env.Actor = ActorID
          requestId }           4. publish core-operations
                                   (Gateway's NATS user)  ───────────▶
                                5. relay the reply ◀───────────────────  accepted | rejected | duplicate
```

- The client **never sets a trusted actor.** Whatever `actor` the body carries is **unconditionally
  overwritten** with the verified full vertex key (`vtx.identity.<sub>`). (Overwrite, not reject — clients
  shouldn't send it, but tolerating-and-ignoring is friendlier and the Gate-3 vector proves a forged value
  can never win, §6.)
- `authContext`, `operationType`, `payload`, `lane`, `requestId` **remain client-supplied** — and that is
  safe (adversarial reasoning in §10 A3): `authContext` only *selects which auth path* the Processor
  evaluates; the **verified actor** is what gets matched against Capability KV / `serviceAccess[]` /
  `ephemeralGrants[]`. A forged `authContext.service` still requires the *verified* actor to actually hold
  that service grant. Lane is likewise gated downstream (§2.3 `LaneUnauthorized`, shipped).
- **The Processor is untouched.** It reads `env.Actor` exactly as today. The *trust* now rests on
  transport-authZ: after #75 Fire 2 only the **Gateway's** NATS user (and the sanctioned service users) may
  publish `core-operations`, so a stamped actor on that subject provably came from the Gateway. **P2 holds**
  — the Gateway publishes *operations*, never KV writes.
- **Transport:** request-reply on `core-operations` (the Processor already replies `accepted|rejected|
  duplicate`, Contract #2 §2 reply table) with a bounded timeout; on timeout return `202 Accepted` with the
  `requestId` for async reconciliation (mirrors the bridge's async-reply posture). HTTP status maps from the
  reply: accepted→200, rejected (`AuthDenied`/`LaneUnauthorized`/validation)→4xx, internal→5xx.

### 3.2 Read path — `GET /v1/<readmodel>` (behind D1.3)

- Authenticate the Bearer JWT → actor; open a Postgres session, `SET LOCAL lattice.actor_id = <actor>`,
  run the query against the **RLS-protected** table (the §6.14 set-membership policy filters rows; D1.3
  built the provisioning + grant table). The Gateway **authenticates; it does not filter** — RLS filters.
- This composes on D1.3 (verticals lane) and on the ratified read-path design (the Gateway translator *is*
  the "read boundary" D1.2/D1.3 anticipated). **Sequenced behind D1.3's first live protected read-model**
  so it is not dead scaffolding (chain-grounding).
- Transitional NATS-KV read-models (pre-RLS) are **not** fronted with row-filtering by the Gateway — per
  the D1 ruling a `protected` model must target Postgres (lint-failable); the Gateway read-front serves
  only RLS-backed Postgres models + explicitly-`public` NATS-KV models.

### 3.3 Identity-claim front (Contract #9) — SUPERSEDED 2026-07-04, see below

> **This section's original plan (an unauthenticated `POST /v1/claim` front) is retired, not built.**
> Re-grounding (`gateway-claim-flow-identity-provisioning-design.md`) found it would not have fixed
> anything: `CreateUnclaimedIdentity` already routes correctly through Fire 1 (§3.1) for its
> staff-role-holding callers, and `ClaimIdentity`'s `scope: self` permission is a hard step-3 gate
> requiring the calling actor to **already** hold `consumer` — unreachable by any actor, authenticated or
> not, because nothing in the platform ever grants a fresh actor its first role. An unauthenticated HTTP
> door changes who calls the endpoint, not whether the resulting envelope's actor holds a capability
> grant, so it does not touch the real gap. The resolution (a Gateway-submitted `ProvisionConsumerIdentity`
> op under a narrow new system-actor role, closing the "first role" gap directly) lives entirely in the
> linked design, **📐 awaiting Andrew's ratification** with a recommendation to ratify-but-shelve (no
> current vertical needs self-service consumer signup) — not part of this fire's build either way. The
> original bullets below are struck and kept only for history; do not build them.
>
> ~~`CreateUnclaimedIdentity` and `ClaimIdentity` are how an actor *gets* an identity, so the claim itself
> cannot require a pre-existing verified token. Design: a bounded, rate-limited, unauthenticated surface
> `POST /v1/claim` that admits only those two operationTypes (an allow-list, fail-closed — any other op on
> that path is 403). The claiming op is stamped with the unclaimed identity's own key per the existing
> claim DDL, not a verified actor.~~
>
> ~~Contract #9 is honored untouched: the client already submits only `sha256(plaintext)` in the payload;
> the Gateway forwards the envelope verbatim and never sees, logs, or stores the plaintext (there is no
> plaintext on the wire to it). The `/v1/claim` handler must carry the same do-not-log discipline as the
> rest of the gateway (no body logging on the claim path).~~

### 3.4 Service actors bypass the Gateway (unchanged)

- Loom / Weaver / Bridge / object-store-manager / admin tooling keep their **sanctioned direct-submit
  path** (architecture line 93: *"submit ops directly to the ledger, bypassing Gateway, using
  pre-provisioned signing keys"*). Their trust is the #75 per-service NATS user, not a JWT. **The Gateway is
  the external door only** — it does not front internal automation. Loupe (trusted single-identity admin
  inspector, 127.0.0.1) is likewise out of scope; the Gateway is the *multi-identity, internet-reachable*
  front.

---

## 4. The forks, designed through

### F2 — the actor-trust model *(the core decision)*

**Option A — Gateway-stamp + transport-trust (RECOMMENDED).** The Gateway verifies once and stamps;
the Processor trusts `env.Actor` because #75 guarantees only the Gateway/service NATS users can publish
`core-operations`.
- *Pros:* zero hot-path crypto (the commit path stays the one-GET capability lookup — preserves the
  Refractor/Processor latency NFRs); **no Contract #2 change**; verification runs once at the edge; it is
  *literally* the architecture's ratified "thin translator stamps `Lattice-Actor`" decision; reuses the
  built `Authenticator` verbatim.
- *Cons:* the Processor trusts the Gateway + the transport. A *compromised* Gateway could impersonate — but
  it sits **inside** the trust boundary (mTLS between services, architecture line 38), the same posture as
  trusting the Processor itself; and the blast radius is bounded by #75 (a compromised Gateway still cannot
  write KV directly). **Hard dependency:** without #75 Fire 2 the "only the Gateway can publish" guarantee
  is only "undetectable-without-EventList," so **Gateway Fire 1 must land with/after #75 Fire 2.**

**Option B — Processor verifies the JWT (brainstorm #118 literal).** Every op carries the JWT; step-3
verifies it before the capability lookup.
- *Pros:* zero-trust — no Gateway-trust, no transport dependency; the Processor is the single chokepoint
  (matches #118's "verified by Processor" wording).
- *Cons:* **crypto on the hot commit path** (signature verify per op, against the 10–100 ops/sec target and
  the step-3 latency budget); the JWT must **ride the envelope** → a **new Contract #2 field**
  (frozen-contract change); token TTL vs long-lived/redelivered ops (re-verify on every JetStream
  redelivery, or cache — re-introducing trust state); and it duplicates the verifier the Gateway already
  runs.

**Recommendation: A.** It is the ratified model, adds no latency, touches no frozen contract, and its one
real dependency (#75 Fire 2) is already ratified and on the build queue. B's zero-trust benefit guards
against a compromised-in-boundary Gateway — a threat mTLS + #75 already bound — at the cost of commit-path
latency and a contract change. *Hybrid note:* A now, with an **optional per-op detached signature** as a
clean v2 if a true zero-trust (multi-tenant / hostile-co-tenant) posture later becomes a requirement —
additive, no rework of A.

### F3 — IdP posture

**Option A (RECOMMENDED): external IdP in prod + a self-hosted dev signer.** `auth.go` already declares
Lattice does not own signing keys. Prod loads the deployment's OIDC provider **JWKS** (kid→public-key map;
the Verifier already keys on `kid`). Dev/CI get a **tiny self-hosted dev signer** (a `cmd/gateway dev-token`
subcommand or a `deploy/dev-idp` helper) that mints RS256 tokens with a **dev keypair checked into
`deploy/` (clearly marked non-secret/dev-only)**; the Gateway loads the dev public key **only under the dev
profile**. **Key rotation** is config-free: a JWKS poll/refresh (kid-keyed) so a rotated key is picked up
without a Gateway restart.

**Option B (rejected): Lattice owns an identity provider.** Contradicts the architecture + `auth.go`'s
explicit "Lattice does NOT own actor signing keys," is large scope, and re-assumes key custody Lattice
disowns. Rejected.

**Fail-closed gate:** the **dev key must never load in prod.** Key loading is profile-gated; a prod profile
with **no** configured JWKS/IdP **refuses to start the external write surface** (no IdP ⇒ no external
writes, by design — not a silent anonymous fallback). This mirrors the #75 Fire-1 "fail before the dial,
never silent-degrade" discipline and the `c1a8901` "empty DSN + unset env = hard error, no silent
localhost default" precedent.

### F4 — external read transport

**Extend the translator (RECOMMENDED)** over a separate read service — one binary, one auth+revocation
seam, one external surface to harden. The read endpoint **composes on D1.3** and is **sequenced behind**
D1.3's first live protected Postgres read-model (so it has a real consumer — chain-grounding).

### F5 — build sequencing

The **write-path translator is buildable now** and high-value: it closes external actor impersonation
(complementing #75 Fire 2) and gives the verticals' apps a real op-submission front instead of
self-asserting an actor. **Recommendation:** build **Fire 1 now**, sequenced to land **with/after #75 Fire 2**
(the transport guarantee A rests on); read-front (Fire 3) behind D1.3; prod IdP/JWKS + NGINX hardening as
ops fires. This is the same **ratify-now / build-Fire-1** pattern already accepted for HA-NATS and D1.

---

## 5. Contract surface

**No frozen-contract edit is staged.** The Gateway is build-to:

| Contract | Section | Build-to (not change) |
|---|---|---|
| #2 Operation Envelope | §2.34 `actor`; **§2.39 (header→full-key stamping by Gateway — already reserved)**; §2.3/§2.6 lane-auth + `LaneUnauthorized` | The Gateway stamps the `actor` field the contract already specifies; lane-auth already enforced (shipped). |
| #6 Capability KV | §6.x | The stamped actor is looked up unchanged at step-3 (shipped). |
| #9 Identity Claim Flow | §9.1 (plaintext never enters Lattice) | **No `/v1/claim` front** (§3.3 superseded) — `CreateUnclaimedIdentity`/`ClaimIdentity` reach the Processor only through the standard authenticated `POST /v1/operations` (§3.1), like every other op; Contract #9's plaintext-never-enters-Lattice invariant is unaffected either way. |
| #5 Health KV | §5.2–5.5 | A `gateway` heartbeat component (status/issues), the seventh self-reporter; Loupe system-map node. |

**New (not frozen):** `docs/components/gateway.md` — present-tense component doc (mandate, the
verify-and-stamp seam, the reverse-proxy/translator split, the trusted-boundary posture, the dev-IdP +
fail-closed key gate), mirroring `loupe.md`. Ships with Fire 1.

**Deferred (NOT staged now):** a **Contract #11 — Gateway / actor-authN (JWT format)** freezing the
`{sub, exp, iss, aud, kid, jti}` claim set + the `vtx.identity.<sub>` binding. Recommend freezing it only
when a **second** consumer needs it stable (the Edge node, or control-plane-authz Fire 2 — which already
names "a `Lattice-Actor` header" + the JWT seam). Until then the format is documented in `gateway.md`. If
Andrew prefers freezing now, I'll stage the §11 edit uncommitted on request.

---

## 6. Migration & test strategy

- **Fire 1 ships dark-compatible.** The translator is **additive** — service-actor direct submission,
  Loupe's trusted-identity path, and every existing client are untouched. No existing caller is *forced*
  through the Gateway in Fire 1; it is a new external door, not a re-route.
- **Actor-stamping adversarial gates (the security crux):**
  - a request whose body carries a **forged `actor`** → the response op commits under the **verified**
    actor (forgery silently ignored);
  - an **unauthenticated** request → 401, no publish;
  - a **revoked** actor (valid unexpired token) → 403 (`ErrTokenRevoked`), no publish;
  - an `HS*`/`none`/wrong-`kid` token → 401 (the built verifier's posture, re-asserted at the HTTP layer).
- **Gate-3 vector (new):** an external op through the Gateway carrying a forged `Lattice-Actor` body whose
  claimed actor *does* hold a capability the verified actor does **not** → the **stamped (verified) actor
  wins** → `AuthDenied` → **BLOCKED**. Proves stamping defeats impersonation end-to-end through the real
  Processor.
- **Dev-signer round-trip (Fire 2):** mint a dev token → Gateway authenticates → op commits under the
  expected actor; a token signed by a *non-trusted* key → 401.
- **Fail-closed config:** prod profile with no IdP/JWKS configured → the external write surface refuses to
  start (assert the startup error); dev key never loads under a prod profile (assert).
- **Health:** the `gateway` heartbeat emits Contract #5 status/issues; a `bmad` adversarial pass on the
  startup/config path.
- **Sequencing assertion:** Fire 1's CI/e2e battery runs **with #75 Fire-2 enforcement on** (the transport
  guarantee Option A depends on) — the two land together.

---

## 7. Risks & alternatives

- **Trusting the Gateway (F2-A).** Bounded by mTLS (in-boundary) + #75 (no direct KV write) + the
  documented sequencing gate. Documented, not hand-waved; v2 per-op signature is the escape hatch.
- **Read-front before D1.3 = dead scaffolding.** Mitigated by sequencing Fire 3 behind D1.3's first live
  protected read-model (the chain-grounding rule — never hand the Steward a consumer assuming an unbuilt
  producer).
- **Dev signer leaking to prod = catastrophic.** Mitigated by profile-gated, fail-closed key loading
  (prod-with-dev-key = startup refusal).
- **Without #75 Fire 2, F2-A's trust is only "undetectable-without-EventList."** Mitigated by the hard
  sequencing gate (Fire 1 lands with/after #75 Fire 2).
- **Rejected alternative — a custom internet-facing Go gateway** (TLS, rate-limit, DDoS in Go). Rejected by
  the architecture's ratified Gateway Architecture Decision: that surface is a standard reverse proxy
  (NGINX/Envoy) as infra config; the Go translator is *thin* and Lattice-specific only.
- **Rejected alternative — Processor-verifies-JWT as the primary model (F2-B).** See F2; kept as the
  designed-through alternative, not the recommendation.

---

## 8. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable + green. **Build only after ✅ Andrew-ratified.** **Ratified collapse
(Andrew, 2026-06-29, fewer-larger-fires):** Fires 1+2 below shipped as **one fire** (the buildable-now write
surface — translator + dev signer + JWKS + health + doc); **Fire 4 (claim-front) was correctly held back**
once its own premise was found self-contradictory at build time and is now **retired** (§3.3, §8 item 4 —
re-grounded 2026-07-04, not built in any form). Fire 3 (read-front) is the **next** fire, behind D1.3,
**one-way** (Lattice consumes the Verticals D1.3 read-model); Fire 5 (reverse-proxy) is **ops**, not a
Steward fire. **Hard gate: the write-surface fire lands with/after #75 Fire 2b (enforcement, not yet live).**

1. **Fire 1 — write-path translator (the keystone; buildable now; full 3-layer security review).**
   `internal/gateway/gateway.go` + a `cmd/gateway` binary: HTTP `POST /v1/operations`, Bearer auth via the
   built `Authenticator`, **strip + stamp** `env.Actor`, envelope validation, publish `core-operations`
   request-reply, relay the reply (timeout → `202` + `requestId`). Profile-gated key config (dev public key
   under the dev profile; fail-closed in prod with no IdP). `gateway` health heartbeat (Contract #5) +
   Loupe system-map node. `docs/components/gateway.md`. Adversarial: forged-actor-ignored, 401/403, lane-auth
   enforced downstream, Gate-3 impersonation vector. **Lands with/after #75 Fire 2.**
2. **Fire 2 — dev signer + key/JWKS config.** A `cmd/gateway dev-token` subcommand (or `deploy/dev-idp`)
   minting RS256 dev tokens; Gateway loads the dev key in dev, **JWKS poll** for prod (kid-keyed rotation).
   `docs/vendors.md` row if a JWKS lib is added. e2e: dev-token → op commits; non-trusted key → 401.
3. **Fire 3 — read-path front (behind D1.3; full 3-layer, read-enforcement).** `GET /v1/<readmodel>`:
   authenticate → open Postgres session → `SET LOCAL lattice.actor_id` → RLS-filtered read. Composes on
   D1.3's first live protected Postgres read-model. "A sees only A" e2e through the Gateway.
4. **Fire 4 — RETIRED as originally conceived (2026-07-04); not a Steward fire.** The unauthenticated
   `POST /v1/claim` front never gets built (§3.3). `CreateUnclaimedIdentity`/`ClaimIdentity` already reach
   the Processor via Fire 1's authenticated `POST /v1/operations`. The real, distinct gap this row's
   original grounding surfaced (nothing ever grants a fresh actor its first `consumer` role) is designed in
   `gateway-claim-flow-identity-provisioning-design.md`, **📐 awaiting Andrew's ratification** with a
   recommendation to ratify-but-shelve (sequenced behind a real self-service-signup driver; none exists
   today) — not part of this epic's numbered fires either way.
5. **Fire 5 — prod reverse-proxy hardening (OPS, not platform code).** `deploy/nginx.conf` (TLS termination,
   rate limit, request-size limits, CORS, IP allowlist), prod IdP/OIDC integration. Infra config per the
   ratified Gateway Architecture Decision — **not** Go code; sized/owned by ops, not the Steward.

---

## 9. Self-adversarial pass (folded in)

Run as a focused adversarial review (the substantial-design gate; party-mode reserved for the Fire-1 build
per §8). Findings folded above:

- **A1 — Option A's trust collapses without #75 Fire 2.** → Promoted to a **hard sequencing gate** (F2, §6,
  Fire 1): the Gateway is no better than self-assert until only-the-Gateway-can-publish is enforced.
- **A2 — stamping must *overwrite*, not *merge*, the client actor.** A precedence bug would let a forged
  actor survive. → **Unconditional overwrite** (§3.1) + the Gate-3 vector proving the forged value never
  wins (§6).
- **A3 — is client-supplied `authContext` an escalation vector?** **No.** `authContext` only *selects* an
  auth path; the **verified** actor is matched against Capability KV / `serviceAccess[]` / `ephemeralGrants[]`.
  A forged `authContext.service` still requires the verified actor to hold that grant. → `authContext` stays
  client-supplied, safely (§3.1).
- **A4 — per-request revocation vs CDC-lag on capability changes.** Accepted as the **D1 posture** (M3:
  short JWT TTL is the backstop; vector-clock fence = v2). Consistent, not re-opened.
- **A5 — client-supplied `requestId` replay.** Bounded by the Contract #4 idempotency tracker (a replay
  dedups); the Gateway forwards it verbatim.
- **A6 — dev signer in prod.** → Profile-gated, **fail-closed** key loading; prod-with-dev-key = startup
  refusal (F3, §6).
- **A7 — does the Gateway widen P2?** **No.** It publishes *operations*, never KV writes; the Processor
  stays the sole KV writer. P5 is likewise untouched (read-front serves lens/RLS targets, never Core KV).

---

## 10. Decisions resolved (no TBDs)

- **F2 = Option A** (Gateway-stamp + transport-trust), recommended; the fork is flagged for Andrew but the
  build is fully specified against A. **F3 = Option A** (external IdP + dev signer). **F4 = Option A**
  (extend the translator). **F5 = Option A** (build Fire 1 now, with/after #75 Fire 2).
- **No frozen-contract edit** (build-to #2/#6/#9/#5); Contract #11 (JWT format) deferred to a second
  consumer, not staged.
- **Overwrite (not reject) a client-supplied actor.** **`authContext` stays client-supplied.** **Read-front
  serves only RLS-Postgres + explicit-public models.** **Service actors keep the direct-submit bypass.**

The Lattice Steward builds **Fire 1 first**, only after ✅ Andrew-ratified, sequenced with/after NATS
write-restriction Fire 2.
