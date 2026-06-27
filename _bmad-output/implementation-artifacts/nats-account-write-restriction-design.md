# NATS account-level write restriction (transport-authorization / Core-KV write isolation) — design

**Status: ✅ Andrew-ratified (2026-06-27).** Fork resolved: **Path A** (static `nats-server.conf` +
per-component NKey users) now, **Path B** (decentralized JWT/operator) as the documented Edge/multi-tenant
evolution — confirmed against the official NATS auth docs (static config = simplest, centralized,
restart-to-change-permissions; JWT = dynamic/resolver-based/multi-tenant — the exact A/B tradeoff). One
fold-in from that doc check: **request-reply / `allow_responses`** for the control-plane responders (§3.4),
without which the control plane goes silent under enforcement. (`auth_callout` — the 6th NATS mechanism —
is external-IdP delegation for the Gateway/D1 *authN* layer, not this transport-*authZ* primitive; not a
missed path.) Author: Winston (Designer fire, 2026-06-27).
Backlog row: `planning-artifacts/backlog/lattice.md` → *Security & trust boundary → NATS
account-level write restriction* (★★, M). Grounds in `lattice-architecture.md` (P2 sole-writer,
the internal-service-actor model, the deferred "internal service actor key provisioning
mechanism" / "mTLS auth config" decisions, the KV Bucket Taxonomy), brainstorming **#75**
(network/transport hardening — mTLS + NATS auth) and **#118** (the `Lattice-Actor` JWT trust
model), the bypass suite (`internal/bypass/bypass_direct_kv_test.go`,
`capadv_direct_kv_write_test.go`), the connection seam (`internal/substrate/conn.go`), the
primordial seeder (`internal/bootstrap/primordial.go`), and the two sibling security designs it
underpins — **Control-plane Capability authorization (FR30)** (which names this as its mandatory
"Fire 1c" companion) and **Read-path authorization (D1)** (which builds the *authentication* seam
this *authorization* primitive complements). An adversarial self-review ran as part of this fire;
its findings (JS-API over-restriction, the backing-stream side-channel, dynamically-named
read-model buckets, the embedded-harness exemption, the residual actor-impersonation surface) are
folded into §3–§8.

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Today NATS runs **fully open** (`docker-compose.yml`: `nats:2.14`
started with `-js`, no accounts, no users) and `substrate.Connect` passes **no credentials** — so
*any* process that can reach the bus can write *any* KV bucket directly, including **Core KV**,
side-stepping the Processor (violates P2 at the transport layer). This design gives each component
its **own scoped NATS user** so that **only the Processor's connection may publish `$KV.core-kv.>`**
(and only Refractor may write the lens/auth/read-model buckets). It is the **transport-authorization**
half of the trust model — the complement to the *authentication* half (verified `Lattice-Actor`/JWT)
that D1 and the control-plane design build.

**Why it matters (it closes a hole the other two security designs assume is already closed).**
- **Bypass #1** (`internal/bypass/bypass_direct_kv_test.go`) is *today* only "BLOCKED" by the soft
  *undetectable-without-EventList* argument — the rogue Core-KV write **succeeds at the NATS layer**;
  the test's own log says *"NATS-auth promotion deferred to Epic 3."* This **is** that promotion: the
  rogue write is **rejected at the door** with a permissions violation.
- **Gate 3** (`capadv_direct_kv_write_test.go`) defends a fabricated Capability-KV permission by
  **reprojection** (the bad write lands, then Refractor overwrites it within a CDC cycle). With this
  restriction the rogue can't write `$KV.capability-kv.>` *at all* — a strictly stronger,
  *no-window* defense.
- The **control-plane authz** design explicitly states *"no deployment enables control enforcement
  without either the account restriction (Fire 1c) or verified-actor (Fire 2)."* That account
  restriction is **this** work, generalized across every protected subject.

**The honest security claim (read this).** This design closes the **direct-write / state-fabrication**
surface — a bus client can no longer mint a vertex, forge a Capability-KV grant, or corrupt a lens
target by writing KV directly. It does **not**, by itself, close **actor impersonation**: a client
that can publish to `core-operations` can still *submit an operation carrying a forged `Lattice-Actor`
header*. That residual is closed by the **authentication** seam (verified JWT → `lattice.actor_id`)
built as **D1 increment 1** + the control-plane design's Fire 2 — which this primitive sits beneath.
The two are complementary: **transport-authZ** says *which connection may publish which subject*;
**actor-authN** says *whom an operation is from*. Shipping this first removes the cruder of the two
holes (direct state forgery) and is a hard prerequisite the others already lean on.

**The one fork — your call (§9).** *Which NATS security model?*
- **Path A — static `nats-server.conf` with per-component NKey users (my recommendation).** A
  declarative server config defines one user per component, each with an explicit
  publish/subscribe permission set; NKey (challenge-response, no shared secret on the wire,
  rotatable) is the credential. Ships the full enforcement now with the least machinery, and the
  *permission boundaries* are written so a later move to Path B is a credential-distribution swap,
  not a redesign. Resolves the architecture's long-deferred "internal service actor key
  provisioning mechanism (config file / boot-time / Vault-stored)" decision toward *config file +
  per-component NKey*.
- **Path B — NATS decentralized JWT/operator mode (`nsc` operator → accounts → users).** Substrate-
  native signed user JWTs, account isolation, import/export — the strategic end-state for
  multi-tenant / Edge and the natural home for per-identity Edge nodes. Heavier (operator keyring,
  JWT resolver) and unnecessary to *close the hole*. Recommended as the **later** evolution; the
  Path-A permission matrix transfers verbatim.
- **Path C — hold until the full Gateway epic.** Rejected: leaves the platform's crudest hole (direct
  Core-KV fabrication) open for the entire D1/Gateway horizon, and strands the control-plane design's
  Fire 1c.

**Frozen-contract change: NONE.** NATS account/permission configuration is **deployment + substrate**
(arch: *"Stream 0 owns … mTLS/auth config"*), governed by no `docs/contracts/*` section. The **KV
Bucket Taxonomy** (in `lattice-architecture.md`, a *planning artifact* owned by the planning lead,
**not** a frozen contract) already documents the per-bucket ownership this enforces — this design
*implements* that table, it does not change it, and I have **not** edited it. Contract #5 (Health KV
— "all components write `health.<component>.<instance>`") is honored: every component user keeps
`publish health.>`. **No `docs/contracts/*` edit is staged.**

---

## 1. Problem & intent

### 1.1 The gap

`P2` (`lattice-architecture.md`) is unconditional: *"The Processor is the sole writer to Core KV —
no exceptions … a serialization point that eliminates write-write conflicts, guarantees schema
validation, enforces auth, and produces the total-ordered ledger."* The **anti-pattern table** lists
*"Direct KV writes outside Processor → Violates P2"*.

But P2 is enforced today only by **convention and code paths**, not by the **transport**. The bus has
no authorization:

```yaml
# docker-compose.yml
nats:
  image: nats:2.14-alpine
  command: ["-js", "-m", "8222"]   # no -c config, no accounts, no users — fully open
```

```go
// internal/substrate/conn.go — ConnectOpts carries URL/Name/reconnect only; NO credential field.
nc, err := nats.Connect(opts.URL, natsOpts...)   // anonymous, unrestricted
```

Consequently any process on the bus can:
- **forge state** — `KVPut("core-kv", "vtx.identity.<id>", {...})` mints a vertex the Processor never
  validated (bypass #1);
- **forge auth** — `KVPut("capability-kv", "cap.roles.<actor>", {platformAdmin})` injects a privilege
  (Gate-3 vector; today survives until reprojection);
- **corrupt a read model** — write a lens-target bucket Refractor owns, making queries lie.

The bypass suite documents this as an accepted *Phase-1 window*. `bypass_direct_kv_test.go` literally
asserts the rogue write **succeeds** and rationalizes it as "detectable, therefore BLOCKED," noting
*"NATS-auth promotion deferred to Epic 3."* Epic 3 never closed it; the flywheel surfaced it here.

### 1.2 The intent (the vision source)

Brainstorm **#75** — *"Network/transport hardening (mTLS between services, NATS auth)"* — is the
seed. The brainstorm decomposition assigns **Stream 0 (Substrate)** ownership of *"mTLS/auth config"*
and lists *"internal service actor key provisioning mechanism (config file, boot-time generation,
Vault-stored)"* among the **deferred** decisions. This design **resolves** that deferred decision and
delivers the *NATS auth* half of #75. (The *mTLS* half — transport **encryption** — is orthogonal
transport hardening; see §8.4 — adjacent, recommended in the same `nats-server.conf`, not on this
critical path.)

It pairs with brainstorm **#118** — *"the `Lattice-Actor` header trust model … any internal
misbehavior = total impersonation. Need: actor claim as a signed JWT."* #118 is **authentication**
(D1 / Gateway / control-plane Fire 2); **#75 is authorization of the transport** (this). The trust
boundary needs **both**, and #75 is the one currently absent at *every* layer.

### 1.3 Why now / why it ranks

- It is the **substrate floor** two already-designed security features stand on: control-plane authz
  (Fire 1c) and D1 (which assumes direct fabrication is closed before reasoning about read filtering).
- It is **independently shippable and ungated** — it needs no actor-authN, no Gateway, no Postgres,
  no unratified design. It is buildable today against the running stack.
- It converts the platform's **two softest gate findings** (bypass #1; the Gate-3 reprojection
  *window*) into hard, no-window, substrate-enforced denials — measurable, adversarial-test-backed
  security improvement.

---

## 2. The trust model this slots into (precision before shape)

Two orthogonal questions, two mechanisms — do not conflate them:

| Question | Mechanism | Owner | Status |
|---|---|---|---|
| **Which *connection* may publish/write which *subject*?** (transport-authZ) | **NATS user + permission set** (this design) | Substrate / deploy | **missing → this design** |
| **Whom is an *operation* from?** (actor-authN) | Verified **JWT → `lattice.actor_id`** / `Lattice-Actor` | Gateway / D1.1 / control Fire 2 | designed (D1 §, control §3.4), unbuilt |
| **Is the actor *allowed* the operation/read?** (actor-authZ / ReBAC) | **Capability KV** (write) + **Capability-Read Lens** (read, D1) | Processor / Refractor | shipped (write) / designed (read) |

This design owns **row 1 only**. It deliberately does *not* attempt actor-authN (that needs signed
identity, which is D1's seam). The value of row 1 standing alone: even an *unauthenticated* bus client
— or a compromised component whose *forged actor header* would pass an authN check that isn't built
yet — **cannot fabricate state directly**; it is forced back onto `core-operations` → Processor, where
schema/DDL validation and (eventually) actor-authZ run. It narrows every attacker to the **one
front door** the rest of the security model is built to guard.

---

## 3. The shape

### 3.1 NATS write mechanics (what a "write" actually is)

A NATS KV bucket `<b>` is a JetStream stream `KV_<b>` whose subjects are `$KV.<b>.>`; a `KVPut` is a
**publish to `$KV.<b>.<key>`**. Therefore *"only the Processor may write Core KV"* = *"only the
Processor's user may **publish** `$KV.core-kv.>`."* NATS evaluates per-user **publish/subscribe
allow + deny** lists server-side and rejects a violating publish with a permissions error before it
touches the stream — exactly the door we want.

**Adversarial finding folded in (backing-stream side-channel).** Denying `$KV.core-kv.>` publish is
necessary but not sufficient: a client could instead mutate the backing stream via the JetStream API
(`$JS.API.STREAM.MSG.DELETE.KV_core-kv`, `$JS.API.STREAM.PURGE.KV_core-kv`, or a direct
`$JS.API.STREAM.MSG.*` on `KV_core-kv`). So the protected-bucket rule is **two clauses**: deny
`publish $KV.core-kv.>` **and** deny the stream-admin API verbs on `KV_<protected>` for every
non-owner/non-provisioner user.

### 3.2 The permission matrix (the heart of the design)

**Principle: default-deny on protected buckets; explicit-allow per owner.** "Protected" = buckets
whose integrity is load-bearing: **`core-kv`** (the ledger-derived state, Processor-only),
**`capability-kv`** (the auth projection, Refractor-only), and the **lens read-model / target
buckets** (`weaver-targets.*`, `objects-base`, `rule-kv`, and every package's read model,
Refractor-only). Operational buckets are owner-scoped; Health is shared.

| User (per `cmd/<binary>`) | **Publish allow** | **Publish deny** | Notes |
|---|---|---|---|
| **processor** | `$KV.core-kv.>`, `core-events`, `health.>`, `$JS.API.>` | — | the *only* Core-KV writer; runs the atomic-batch + event outbox |
| **refractor** | `$KV.>`, `health.>`, `$JS.API.>` | **`$KV.core-kv.>`** (+ stream-admin on `KV_core-kv`) | sole lens projector → may write *any* KV target **except** Core KV (it is CDC-**read**-only on Core); covers dynamically-named read models without enumeration (§3.4) |
| **loom** | `core-operations`, `$KV.loom-state.>`, `lattice.ctrl.loom.>`, `health.>`, `$JS.API.>` | `$KV.core-kv.>`, `$KV.capability-kv.>`, `$KV.weaver-targets.>` | mutates Core state only by **submitting ops** (P2); owns `loom-state` |
| **weaver** | `core-operations`, `$KV.weaver-state.>`, `lattice.ctrl.weaver.>`, schedule stream, `health.>`, `$JS.API.>` | `$KV.core-kv.>`, `$KV.capability-kv.>` | owns `weaver-state`; targets are Refractor-written, Weaver-**read** |
| **bridge** | `core-operations`, its claim/operational buckets, `health.>`, `$JS.API.>` | `$KV.core-kv.>`, `$KV.capability-kv.>` | external-I/O egress; replies via ops |
| **object-store-manager** | `core-operations`, `$OBJ.objects-base.>` *(its GC needs object-store writes — see §3.4)*, `health.>`, `$JS.API.>` | `$KV.core-kv.>` | object GC actor |
| **bootstrap / provisioner** | `$KV.>`, `$OBJ.>`, `$JS.API.>`, `core-events`, `health.>` | — | **provisioning-time privileged user** (the sanctioned non-Processor direct Core-KV writer — `primordial.go:2`); seeds the kernel before the Processor exists, creates streams/buckets |
| **lattice-pkg / package-installer** | `core-operations` (+ provisioning verbs it needs — §3.4 verify), `health.>`, `$JS.API.>` | `$KV.core-kv.>` (unless it direct-seeds DDL — then provisioner) | `InstallPackage`/`UninstallPackage` kernel ops |
| **loupe (trusted inspector)** | `core-operations`, `health.>`, `$JS.API.>` (read APIs) | `$KV.core-kv.>`, `$KV.capability-kv.>` | reads **all** KV (subscribe/get); writes state only via ops — even the inspector gets no direct Core-KV write |
| **lattice CLI / verify tools** | `core-operations`, `$JS.API.>`, `health.>` | `$KV.core-kv.>` | operator ops + read |
| **vertical apps** (`loftspace-app`, `clinic-app`) | `core-operations`, `health.>`, `$JS.API.>` (read) | `$KV.core-kv.>`, `$KV.capability-kv.>` | P5 readers; write via ops |

**Subscribe permissions** are permissive for internal components (they must consume `core-events`,
read Core-KV CDC, run control responders, read their buckets); the security value is in the **publish**
restriction. (Control responders additionally need **`allow_responses`** to publish their replies —
see §3.4 "Request-reply / control-plane replies"; the matrix's publish-allow column lists only the
*static* subjects.) v1 does **not** lock down subscribe (a future per-identity Edge model — Personal Lens —
is where read-side subject scoping lands; out of scope here, and that's D1/Edge territory).

The **invariant that does all the work**: *no user except `processor` may publish `$KV.core-kv.>`;
no user except `refractor` may publish `$KV.capability-kv.>` or the lens-target buckets.* Everything
else is convenience scoping.

### 3.3 The credential seam (`substrate.ConnectOpts`)

Add an optional credential to `ConnectOpts`, threaded from each binary's environment. Empty ⇒ today's
anonymous connect (backward-compatible — see §6):

```go
type ConnectOpts struct {
    URL  string
    Name string
    // ... existing fields ...

    // Credential — exactly one populated for an authenticated connect.
    // Empty ⇒ anonymous (embedded-harness / legacy). See nats.Nkey / nats.UserCredentials.
    NKeySeedFile  string // path to a per-component NKey seed (Path A, recommended)
    CredsFile     string // path to a JWT creds file (Path B / operator mode)
    // (UserInfo user/pass intentionally omitted — shared secret on the wire; NKey preferred)
}
```

`Connect` maps these to `nats.Nkey(pub, sigCB)` (from the seed) or `nats.UserCredentials(path)`.
Each `cmd/<binary>/main.go` reads `NATS_NKEY` / `NATS_CREDS` from the environment (mirroring the
existing `NATS_URL` pattern) and passes it through. **One seam, every binary** — no per-component
auth code.

### 3.4 Resolved design details (decide-don't-defer)

- **Dynamically-named read-model buckets (adversarial finding).** Package read models get
  package-chosen bucket names (e.g. `loftspace-...`); they cannot be enumerated in a static conf.
  **Resolution:** Refractor — the *sole* lens projector — is granted `publish $KV.>` with a single
  **deny `$KV.core-kv.>`**. "Refractor may write any KV target except Core KV" is *correct by
  construction* (it never writes Core KV; it writes every other projection) and future-proof (new
  packages need no conf change). Residual: Refractor *could* technically write `weaver-state` /
  `loom-state`; accepted for v1 (Refractor is trusted internal; the high-value bucket, Core KV, is
  denied). A later tightening can prefix all lens targets (`lens.*`) and narrow Refractor to that
  prefix — noted, not forced.
- **JS-API over-restriction (adversarial finding — the top break-risk).** Every internal component
  creates **durable consumers** (`$JS.API.CONSUMER.CREATE.*`) and needs stream info. Narrowly
  scoping `$JS.API.>` per stream is brittle and would break the stack. **Resolution:** v1 grants
  `$JS.API.>` to internal components (consumer/stream management is *not* the attack surface we're
  closing) and concentrates enforcement on the **KV write subjects** + the **stream-admin verbs on
  protected streams** (§3.1). The high-value win — no direct Core-KV/auth/lens fabrication — does not
  require fine-grained JS-API ACLs, which are a deferred hardening.
- **Request-reply / control-plane replies (NATS-docs check, folded in 2026-06-27).** The official
  authorization docs warn explicitly: *"It is important to not break request-reply patterns"* — a
  responder must be able to **publish the reply to the requester's `_INBOX.<id>`**, which is a
  *dynamic* subject the static matrix cannot enumerate. This bites every component that runs a
  request-reply **responder**: the `micro.Service` **control planes** (Loom / Weaver / Refractor —
  they receive on `lattice.ctrl.<comp>.>` *and on the framework's `$SRV.PING/INFO/STATS.>`
  discovery subjects*, then reply to an `_INBOX`). The §3.2 matrix grants the *request* subject but
  **not** the reply publish, so under enforcement the control plane would go silent. **Resolution:**
  grant every responder **`allow_responses: true`** (NATS dynamically authorizes a one-time publish
  to the reply subject of each *received* message) rather than a blanket `publish _INBOX.>` (which
  would let a component publish to *arbitrary* inboxes — strictly weaker). `allow_responses` also
  covers the `$SRV.>` micro-discovery replies for free. Components needing it: **loom, weaver,
  refractor** (control responders), the **bridge** (if it responds to any request), and any future
  `micro.Service`. Pure clients (Processor, apps, CLI) that only *make* requests need no change —
  they publish `$JS.API.*` / `core-operations` and the server replies to their own `_INBOX`, which
  they *subscribe* to (subscribe is already permissive, §3.2). This is a v1 must-have, not a
  deferral — it is the difference between "the stack runs under enforcement" and "the control plane
  is dead on arrival."
- **Object Store buckets.** Large-file blobs live in `$OBJ.objects-base.>` (an Object Store = a
  stream `OBJ_objects-base`). Object writes route through `core-operations`/the object-store path for
  most actors; `object-store-manager` (GC) and the provisioner get `$OBJ.objects-base.>` publish.
  Loupe upload writes go via the sanctioned object-store path, not raw — verify the exact writer in
  Fire 2 and scope accordingly (the matrix above is the intent).
- **`lattice-pkg` write pattern (verify-in-build).** If `InstallPackage` is a kernel op submitted to
  `ops.meta.>` (the P2-correct path), the installer needs only `core-operations` publish. If it
  *direct-seeds* DDL like bootstrap, it takes the **provisioner** user. The Steward confirms the path
  in Fire 2 (`cmd/lattice-pkg`) and assigns the user accordingly; both are expressible in the matrix.
- **Health KV residual (accepted).** Health is `publish health.>` for all (Contract #5). A rogue can
  write *fake* health and mislead the Lamplighter — but this corrupts **operational** state, not Core
  KV, and is detectable (a Health doc from an unexpected instance). Locking Health to per-instance
  keys is a future refinement, not in scope; the contract mandates broad write today.

---

## 4. Read path (P5) & write path (P2)

- **Write path (P2):** *strengthened, not changed.* The legitimate write path is unchanged —
  ops → `core-operations` → Processor → atomic batch (Processor user has the sole `$KV.core-kv.>`
  publish). What changes is that the **illegitimate** path (anyone-writes-Core-KV) is now **rejected
  at the transport**, making P2 an *enforced* invariant rather than a *documented* one. This is the
  first time P2 is true in the substrate, not merely in the code.
- **Read path (P5):** *untouched.* Applications still read lens projections (`KVGet`/`KVListKeys`)
  via permissive subscribe/get rights. No read is gated by this design (read-gating is D1). Loupe-the-
  inspector keeps full read. No new read-model or DDL is introduced — so this is **not** package work
  and introduces **no lens**; it is a pure substrate/deploy hardening.

---

## 5. Contract surface

**No frozen-contract change.** Enumerated against `docs/contracts/*`:

| Contract | Touched? | Why not |
|---|---|---|
| #1 (key shapes / subjects) | No | Subjects/keys unchanged; only *who may publish them* changes (deploy concern). |
| #5 (Health KV) | No — **honored** | "all components write `health.<component>.<instance>`" — every user keeps `publish health.>`. |
| #6 (Capability KV) | No | The auth *shape* is unchanged; this makes "Refractor is the sole Capability-KV writer" *enforced* rather than *assumed*. |
| #2/#4/#10 (ops/ledger/orchestration) | No | Legitimate op submission paths unchanged. |

The **KV Bucket Taxonomy** table (`lattice-architecture.md`) already states the per-bucket *Owner
(writes)* this enforces — it is a **planning artifact** (planning-lead-owned), **not** a frozen
contract, and this design **implements** it without editing it. **Nothing is staged uncommitted.**

---

## 6. Migration & compatibility

The single most important compatibility property: **the embedded NATS test harness stays
anonymous.**

- **Embedded harness (unit/convergence tests).** The in-process NATS server (`internal/substrate/
  harness`, used by `go test ./... -p 4`, lease-convergence, object-gc) runs **no auth config** and
  components connect with **empty creds** → unchanged behavior. This preserves the CI parallelism win
  (`-p 4`, `jsstore.Dir(t)`) and means **zero test churn** outside the bypass suite. Empty-creds
  default is the compatibility hinge.
- **Docker stack (`make up` / `up-full`) + production.** These mount the auth-enabled
  `nats-server.conf` and each binary gets its scoped creds via env. This is where enforcement lives
  and where Gate 2 / Gate 3 now run **with auth on** (so the closure is actually exercised).
- **Rollout order matters (no flag-day).** Fire 1 (creds seam, default-empty) ships first and is a
  no-op until a server demands auth. Fire 2 turns auth on in the Docker conf *and* hands every binary
  its creds in the *same* change — so the stack is never half-authenticated. Dev creds (static NKey
  seeds in `deploy/`) are dev-only, like the existing `POSTGRES_PASSWORD: lattice_dev`; production
  injects real seeds via mounted secrets / Vault (never committed).

---

## 7. Test strategy

The proof is the bypass suite flipping from *soft* to *hard*:

- **Gate 2 — bypass #1 becomes a transport denial.** Rewrite `bypass_direct_kv_test.go`: the rogue
  client connects with a **non-processor** user (e.g. `loupe`/`loom` creds) and the
  `KVPut("core-kv", …)` returns a **NATS permissions-violation error** — asserted as the *reason* for
  BLOCKED, replacing "undetectable-without-EventList." A companion positive test confirms the
  **processor** user *can* write Core KV (no over-restriction regression).
- **Gate 3 — capability fabrication denied at the door.** Augment `capadv_direct_kv_write_test.go`:
  a non-Refractor user's `KVPut("capability-kv", …)` is **rejected**; the existing reprojection test
  is kept as defense-in-depth (proving the no-window primary + the eventual backstop coexist).
- **New conformance check (`verify-nats-permissions`).** A small gate (mirroring `verify-kernel`)
  that, against the live stack, asserts the **negative space**: for each non-owner user, a probe write
  to each protected bucket is rejected; for each owner, the legitimate write succeeds. This is the
  regression fence — a loosened conf fails CI. Runs in the `stack-gates` CI job (the auth-enabled
  Docker stack).
- **Unhindered happy path.** `make up-full` + the full gate battery (verify-kernel, 8× verify-package,
  Gate 2/3, hello-lattice, lease-convergence, object-gc) must pass **with auth on** — the real proof
  that the permission matrix is complete and nothing legitimate is denied.
- **Embedded-test invariance.** `go test ./... -p 4` unchanged (anonymous embedded server).

---

## 8. Risks & alternatives

- **8.1 Over-restriction breaks a legitimate writer (highest risk).** A missing allow entry denies a
  real publish and wedges a component. *Mitigation:* the `verify-nats-permissions` positive cases +
  the full-stack gate battery run with auth on catch this immediately; the matrix is derived from the
  KV Bucket Taxonomy + each component's actual buckets (grounded, §3.2). Fire 2 lands conf + creds
  atomically so there's no partial state.
- **8.2 Residual: actor impersonation over `core-operations`.** Out of scope by design (§2) —
  closed by D1.1 / control Fire 2 authN. Stated honestly in the For-Andrew block; this design is a
  *layer*, not the whole boundary.
- **8.3 Residual: trusted-internal lateral writes.** Refractor's broad `$KV.>`-except-core (§3.4) and
  shared subscribe leave some intra-trust latitude; accepted for v1, tightening path noted.
- **8.4 mTLS is not in this design.** #75 pairs NATS-auth with mTLS (transport *encryption*). They
  are orthogonal: auth = *who may publish*; mTLS = *is the channel encrypted/peer-verified*.
  Recommend mTLS be configured in the same `nats-server.conf` as a deploy concern (Fire 4 / ops), not
  blocking the authZ win. Noted, not forked.
- **Alternative considered — application-layer write guard (a substrate wrapper that refuses Core-KV
  KVPut).** Rejected: it lives in the *same* trust domain as the attacker (a rogue binary skips the
  wrapper and calls `nats.go` directly) — it is not a real boundary. Only **server-side** NATS
  permissions are a true boundary. This is why the design is substrate/deploy, not a Go guard.
- **Alternative considered — single shared user with a deny on `$KV.core-kv.>`.** Rejected: it
  cannot express *Processor-may, others-may-not* (the Processor needs the write the others are denied).
  Per-component users are required to scope the one writer.

---

## 9. The fork (detailed) — NATS security model

The architecture deferred *"internal service actor key provisioning mechanism (config file, boot-time
generation, Vault-stored)"* and *"mTLS certificate management approach"* to Stream 3. This design
forces the choice. The **permission matrix (§3.2) is identical** under every path — the fork is only
*how credentials are minted and distributed*.

| | **A — static conf + NKey users (rec.)** | **B — decentralized JWT/operator mode** | **C — hold for Gateway epic** |
|---|---|---|---|
| Machinery | one `nats-server.conf`, per-user NKey seeds | `nsc` operator keyring, account/user JWTs, JWT resolver | none |
| Closes the hole now? | **yes** | yes (more setup) | **no** |
| Credential | NKey seed (no shared secret, rotatable) | signed user JWT + NKey | n/a |
| Multi-tenant / Edge fit | adequate; users are flat | **strong** — accounts isolate, import/export, per-identity Edge nodes | n/a |
| Effort | M | L | 0 (but leaves the hole) |
| Migration to the other | Path-A → Path-B is a creds-distribution swap; matrix unchanged | — | — |

**Recommendation: A now, design B as the later evolution.** Ship the enforcement with the least
machinery; write the permission *boundaries* (§3.2) as the durable artifact so the eventual operator-
mode migration re-uses them verbatim. This mirrors the platform's established "ship the simple backend
first, keep the seam for the strategic one" pattern (Vault backend; D1 read-enforcement seam). The
credential sub-choice — **NKey vs creds-file vs user/pass** — I resolve to **NKey** (challenge-
response, no on-the-wire secret, rotatable, works in *both* static-conf and operator mode); user/pass
is rejected (shared secret), creds-file (JWT) is the Path-B form. **Andrew's call: A, B, or C** (and,
if A, confirm NKey).

---

## 10. Decomposition for the Steward (fire-by-fire, each shippable + green)

> Build **only after ✅ Andrew-ratified.** Each fire is independently shippable and leaves CI green.

- **Fire 1 — Credential seam in `substrate` (pure plumbing, no enforcement).** Add `NKeySeedFile` /
  `CredsFile` to `ConnectOpts`; map to `nats.Nkey` / `nats.UserCredentials` in `Connect`; thread
  `NATS_NKEY` / `NATS_CREDS` env through every `cmd/<binary>/main.go`. Server stays open; empty creds
  = today's behavior. **Green:** entire suite unchanged (no server demands auth yet). *Backward-
  compatible by construction.*
- **Fire 2 — Auth-enabled dev stack (the enforcement turn-on).** Add `deploy/nats-server.conf` with
  per-component NKey users + the §3.2 permission matrix; mount it in `docker-compose.yml`; generate/
  commit dev NKey seeds in `deploy/`; hand each binary its creds via env in `make up`/`up-full`/the
  package/CLI/app run targets. Confirm `lattice-pkg` + object-store writer paths (§3.4). **Green:**
  `make up-full` + full gate battery pass **with auth on** (the completeness proof).
- **Fire 3 — Flip the gates from soft to hard + the conformance fence.** Rewrite bypass #1 to assert a
  permissions-violation denial; augment the Gate-3 capadv test for denied-at-door; add
  `verify-nats-permissions` (negative + positive matrix probe) to the `stack-gates` CI job. **Green:**
  Gate 2 all BLOCKED (now substrate-enforced), Gate 3 all DEFENDED (no-window primary + reprojection
  backstop), new conformance green.
- **Fire 4 — (deferred, optional) production posture / mTLS / Path-B.** mTLS in `nats-server.conf`;
  secret-injected seeds for prod; *or* the operator/JWT-mode migration if Andrew picks Path B. Flagged
  deferred — the core win lands in Fires 1–3. *Sequence the control-plane design's Fire 1c onto this
  primitive once it exists (it becomes a no-op there — the `lattice.ctrl.>` restriction is already in
  the matrix).*

---

## 11. Open questions — resolved

- *Static conf vs operator mode?* → **Fork for Andrew (§9); recommend A (static-conf NKey) now.**
- *NKey vs creds vs user/pass?* → **NKey** (§9), resolved.
- *Allow-list vs deny-list for Refractor's lens-target writes?* → **`$KV.>` allow + `$KV.core-kv.>`
  deny** (§3.4), resolved (future tightening via a `lens.*` prefix, noted).
- *Lock down subscribe too?* → **No** in v1 (§3.2) — read-side subject scoping is the Personal-Lens /
  Edge concern (D1 territory), not this hardening.
- *Does this need actor-authN to be useful?* → **No** (§2) — it closes direct fabrication standalone;
  authN is the complementary layer above it.
- *Does the embedded test harness need auth?* → **No** (§6) — anonymous in-process server; the
  enforcement is exercised by the Docker-stack gates.
- *Contract change?* → **None** (§5).

---

## Appendix — grounding index

- **P2 / anti-patterns / KV Bucket Taxonomy / Stream-0 owns auth-config / deferred service-actor
  provisioning:** `_bmad-output/planning-artifacts/lattice-architecture.md`.
- **Open bus:** `docker-compose.yml` (`nats … -js`, no `-c`). **Connection seam:**
  `internal/substrate/conn.go` (`ConnectOpts`, `Connect`). **Sanctioned provisioning writer:**
  `internal/bootstrap/primordial.go:2`.
- **Soft bypass #1:** `internal/bypass/bypass_direct_kv_test.go` ("NATS-auth promotion deferred to
  Epic 3"). **Reprojection-window Gate-3 defense:** `internal/bypass/capadv_direct_kv_write_test.go`.
- **Vision:** brainstorm **#75** (NATS auth / mTLS), **#118** (`Lattice-Actor` JWT trust);
  `_bmad-output/brainstorming/brainstorming-session-2026-04-08.md`.
- **Sibling designs this underpins:** `control-plane-capability-authz-design.md` (names this its
  Fire 1c), `read-path-authorization-d1-design.md` (the actor-authN seam this complements).
