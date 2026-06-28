# Personal / Secure Lens (Edge fan-out) — design

**Status: ✅ Andrew-ratified (DESIGN; build sequenced behind D1 + a real consumer) — 2026-06-27.**
Author: Winston (Designer fire, 2026-06-27).

> **Ratification decisions (Andrew, 2026-06-27):** the **design shape is ratified** (it's sound + ready);
> the **build is sequenced**, not started now.
> 1. **Fork 1 (transport) — NATS-subject JetStream** is the transport-agnostic core; WS/push-bridge
>    deferred to the Gateway epic.
> 2. **Fork 2 (sequencing) — REVISED: defer the whole feature behind D1 *and* a concrete consumer.** The
>    original "build the transport/projector/hydration dark *now* under the trusted-single-identity posture"
>    is **dropped.** A renewed-skill re-review caught it as the same over-incrementalism corrected on the
>    control plane and Vault: Personal Lens is itself a non-Processor reader whose security **is** D1's
>    `readableAnchors` (stubbed pre-D1), and there is **no consumer** yet (the Edge node is XL/unstarted;
>    Loupe is the all-access inspector, not a filtered-stream consumer). Machinery with no consumer *and*
>    stubbed security is dead scaffolding — so build it only once **D1 has landed** and **a real consumer
>    exists** (the Edge node, or a filed real-time-delta-streaming demand). The design stays on the shelf,
>    ready.
> 3. **Fork 3 (subject subscribe-ACL) — rides the ratified NATS account-write-restriction (#1)** (per-user
>    NATS subscribe permissions on `lattice.sync.user.<id>`), now on firm ground.
>
> **Reconciliation fix folded in:** §3.3's security filter referenced D1's *pre-decomposition* single
> `cap-read.<actor>` doc; updated to the **decomposed/unioned** model (union across `cap-read.*.<actor>` /
> the `actor_read_grants` table) ratified for D1 today. **No frozen-contract change** (this design adds
> none; it builds-to D1 §6.14 + Vault §3.10).
Backlog row: `planning-artifacts/backlog/lattice.md` → *Edge & personal lenses → Personal / Secure Lens*
(★★, L) — **subsumes** the sibling row *NATS-subject publish-events adapter* (★★, S–M), which is this
design's Fire 1. Grounds in: `lattice-architecture.md` (D1 / Edge / Refractor decisions), `docs/components/refractor.md`
(lens + adapter + per-actor fan-out machinery), the frozen contracts (#1 addressing, #6 Capability KV),
brainstorming #61/#74/#499/#572/#655, the Obsidian vault *Edge Lattice/Personal Lens.md* +
*Edge Lattice/Edge Lattice.md* + *Lens and Refractor/The Refractor.md*, and the two upstream designs this
composes onto: **`read-path-authorization-d1-design.md`** (the security filter source) and
**`vault-crypto-shredding-design.md`** (ciphertext-at-rest + transient key).

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Turns the Refractor from a *warehouse* of shared read-models into a
*projector* of per-identity **filtered delta streams**: a new `nats_subject` lens target adapter publishes
each authorized change to a user's private subject `lattice.sync.user.<id>`, an **Interest Set** narrows
the stream to what a device cares about, and a **Hydration** hook bulk-loads a cold device — the cloud-side
half of the Edge Lattice (the Edge subscribes, applies deltas to its local VAL, decrypts sensitive aspects
with a transient key). Storage stays **O(total entities)**, not O(users×devices) — Personal Lenses are
*filters, not clones* (vault *Personal Lens.md*).

**The one thing to understand before ratifying: Personal Lens is a read-path *delivery channel*, so its
security IS D1.** The per-user filter is exactly D1's `readableAnchors` (`cap-read.<actor>`). Built without
D1, the only narrowing is the Interest Set — a *relevance* filter a malicious Edge could set to anyone's
data — i.e. a brand-new **unauthenticated push channel for the whole graph**, the NFR-S2 gap *widened*. So
this design **builds the mechanism now** (adapter + projector + Interest Set + hydration, verified under the
**trusted-single-identity posture Loupe already uses**) and **gates the untrusted multi-identity Edge
exposure on D1** (the real filter + authenticated subject access). The plumbing is shippable + green
pre-D1; the security door stays shut until D1.

**Three forks I designed through — your call on all three:**

1. **Edge delivery transport (architectural).** The *core mechanism* is a JetStream stream over
   `lattice.sync.user.<id>` (short-retention "ledger replay", re-hydrate on gap) — transport-agnostic and
   what I recommend building. Native NATS clients consume it directly; **browsers/mobile** need a
   **WebSocket + push-notification bridge** (vault *Edge Lattice.md §3* — "devices can't keep a constant
   TCP connection"). That bridge is a **Gateway concern** (same deferral as D1's full internet-facing
   Gateway). **Rec: build the NATS-subject JetStream fan-out now; defer the WS/push edge-delivery to the
   Gateway epic.** Alternatives: WS-first (couples Edge delivery to an unbuilt Gateway) or a bespoke
   push-bridge now (premature). *Fork = whether to pull any WS/push work into this design — I say no.*

2. **Security sequencing vs. D1 (the gate).** **Rec: build Fires 1–2 now** (adapter + projector +
   Interest Set + hydration) **dark, under the trusted-single-identity carve-out** (no untrusted Edge),
   and **gate Fire 3** (wire D1's `readableAnchors` as the per-user filter + authenticated subject access)
   **on D1 ratification.** Alternative: hold the *entire* feature until D1 — needlessly idles the (large,
   independently-valuable, security-inert) transport/projector machinery. The fork is *how much ships
   pre-D1*, not *whether the filter is D1* — that part is settled (it is).

3. **Subject subscribe-authorization (security dependency, not a redesign).** Once D1 says *who* the
   reader is, *who may subscribe to `lattice.sync.user.X`* is enforced by **per-connection NATS-account
   subscribe permissions** — the read-side twin of the **NATS account-level write restriction** design
   (already 📐 awaiting-Andrew). **Rec: ride that design's NATS-user model** (identity X's NATS user may
   `SUB lattice.sync.user.X.>` only); until it lands, JetStream-stream access is the trusted-tool boundary.
   *Fork = confirm Personal Lens's subscribe-ACL rides the NATS-account-auth design rather than inventing
   its own.*

**Frozen-contract change: NONE.** The `nats_subject` targetType, the delta-envelope wire shape, the
Interest Set, and the hydration control op are all **component-level** surfaces documented in
`docs/components/refractor.md` (the lens/adapter machinery is a component doc, not a frozen contract). The
security filter is **build-to D1 §6.14** (`readableAnchors`, itself uncommitted/awaiting-Andrew); the
sensitive-aspect path is **build-to Vault §3.10** (ciphertext-at-rest, also uncommitted/awaiting-Andrew).
No `docs/contracts/*` edit is staged by this fire. (Dependency, not contract change: this design assumes
D1 §6.14 and Vault Phase A land — it sequences behind them, see §7.)

---

## 1. Problem & intent

**The gap.** Lattice's read path delivers **shared, multi-tenant read models** (NATS-KV buckets, the
future Postgres tables). There is no way to give a *single identity* a **live, security-filtered, minimal
stream of just their slice** — the prerequisite the Refractor doc names plainly under "What's deferred":
*"Personal Lens / Secure Lens — Phase 3 — Requires per-identity lens scoping."* Without it there is no
Edge Lattice (a sovereign per-user device node): the device has nothing to subscribe to and nothing to
hydrate from. Loupe is explicitly the *trusted-tool precursor* to the Edge node, and the backlog frames
this whole row as **"the path Loupe grows into."**

**The intent (the vision this serves).** The vault is precise about the end-state
(*Edge Lattice/Personal Lens.md*): a Personal Lens *"is not a unique database; it is a parameterized,
security-filtered stream that treats the Edge Lattice as the primary storage for that user's view."* The
cloud stays a **Projector, not a Warehouse**: it stores the global truth once (thin vertices, ciphertext
aspects, directional links) and *filters on the fly* per identity. Three named mechanisms make it
efficient: the **Interest Set** (the device's watchlist), the **Delta Projector** (CDC — evaluate only the
aspect that moved against the active Interest Sets), and the **Hydration Hook** (cold-start bulk load,
then incremental). The brainstorm pins the concrete shapes: **#655** — *"Personal Lens target: NATS stream
with subject `lattice.sync.user.<user-id>`"*; **#499/#572** — *"the Personal Lens NATS-subject target is a
brand-new adapter, not a config change"*; **#61** — the Refractor "Secure Lens" type (vault-decrypted
sensitive aspects); **#74** — Edge Lattice depends on "Stream 2 having Personal Lens projections working."

**The architectural principle that makes it tractable.** *"Everything derived from Core KV is a Lens"*
and *"the Refractor doesn't just move data; it moves permissions"* (vault *The Refractor.md*). The
write-path solved O(1) authz by projecting actor→grants (Capability KV). D1 projects the **read** mirror
(actor→`readableAnchors`). Personal Lens is the **third move with the same source**: take each CDC delta,
ask *which authorized identities care about this*, and push a tiny packet to each one's private subject —
the ReBAC traversal already runs **once** in the Refractor; the per-user step is a cheap fan-publish. It is
not a new projection model; it is the existing per-actor fan-out (the Capability pipeline's
`ActorEnumerator`) aimed at a subject target instead of a KV bucket.

---

## 2. Grounding — the patterns this extends (do not redesign them)

**2.1 Per-actor fan-out already exists (the Capability pipeline).** `internal/refractor/pipeline/`
already does *exactly* the reverse-lookup Personal Lens needs. On a CDC event the capability pipeline
installs an **`ActorEnumerator`** (`actor_enumerator.go`) that does an **undirected adjacency BFS** from
the mutated vertex to find every reachable actor (depth-bounded 10, actor-cap-bounded 10 000), then
**re-executes the cypher per affected actor** (`reprojectActors`), and on a pure *link* mutation it does
the same fan-out (`evaluateLinkFanOut`, with an idempotent adjacency self-apply so the reprojection never
races ahead of the edge that triggered it). Personal Lens reuses this verbatim: *mutated anchor → affected
actors → (filter) → publish to each actor's subject*. **No new traversal engine.**

**2.2 The adapter SPI is a 4-method interface (`internal/refractor/adapter/adapter.go`).** `Upsert`,
`Delete`, `Probe`, `Close` (+ optional `Truncater`). Two adapters ship: `NatsKVAdapter`,
`PostgresAdapter`. A new target is: implement the interface + add a `case` to the targetType switch in
`cmd/refractor/main.go:211` (`"nats_kv"` / `"postgres"` → add `"nats_subject"`) + a `TargetConfig` shape
in `internal/refractor/lens/corekv_source.go:73` (`LensSpec.TargetType` / `TargetConfig`;
`TargetNATSKVConfig` / `TargetPostgresConfig` → add `TargetNATSSubjectConfig`) + the validation `case` at
`corekv_source.go:400`. **This is the entire Fire-1 surface** — the "brand-new adapter, not a config
change" the brainstorm called out.

**2.3 D1 is the security filter source (build-to, do not duplicate).**
`read-path-authorization-d1-design.md` defines the `capabilityRead` lens projecting per-actor
`cap-read.<actor>` with `readableAnchors[]` (a flat, pre-resolved set of `<anchorType>.<anchorId>` the
actor may read, with `via` provenance) and the **`authzAnchor`** column convention on protected lenses.
D1 itself names this design as **"Path C — Personal/Secure Lens fan-out … the Edge end-state … D1's
Capability-Read Lens is exactly the input it needs."** Personal Lens **consumes** `readableAnchors`; it
does not re-walk authorization. (This is why Personal Lens's security == D1, per the For-Andrew block.)

**2.4 Vault is the confidentiality source (build-to, do not duplicate).**
`vault-crypto-shredding-design.md` Phase A makes `sensitive: true` aspects **ciphertext at rest** in Core
KV (§3.10) and ships a `Vault` interface (`Encrypt/Decrypt/ShredKey`). The Refractor already "projects
ciphertext as-is" (Vault design §4 affected-consumers). So Personal Lens ships sensitive deltas **as
ciphertext** — a **blind projection** (the architecture's blind-projection rule: the cloud projector never
sees plaintext for the Edge path) — and the Edge decrypts locally with a **transient session key** (vault
*Edge Lattice.md §5*). This is *lighter* than Vault's cloud "Secure Lens" (Phase B, which decrypts into
queryable plaintext server-side and is D1-gated) — see §3.6 for why the Edge path needs only Vault Phase A,
not Phase B.

**Invariants this design inherits (CLAUDE.md / `lattice-architecture.md`).** **P2** — Processor is the
sole Core-KV writer; Personal Lens writes *no* Core KV (it's a lens; it publishes to subjects + a
Refractor-owned operational KV, exactly as the adjacency index is Refractor-owned). **P5** — apps read
projections; the Edge consuming `lattice.sync.user.<id>` *is* a projection read. **P1** — per-device
subscription state (the Interest Set) is **operational**, so it lives **outside Core KV** (a
Refractor-owned bucket, like `refractor-adjacency` / Health KV / Weaver state — *not* the immutable
ledger; a device's watchlist is not business truth). **Contract #1** — subjects and keys follow the dotted
shapes; no new vertex/link shapes are introduced for the transport (the Interest Set is operational KV, not
a vertex).

---

## 3. The shape

### 3.1 Fire 1 — the `nats_subject` target adapter (the "brand-new adapter")

A new adapter `internal/refractor/adapter/natssubject.go` implementing the 4-method SPI, mirroring
`NatsKVAdapter`'s construction.

- **`Upsert(keys, row, projectionSeq)`** → publishes a **delta envelope** (below) with `op: "upsert"` to
  the resolved per-user subject(s). **`Delete(keys, projectionSeq)`** → publishes `op: "delete"` (key +
  tombstone, no body). **`Probe`** → checks the backing JetStream stream exists/reachable (for the FR17
  infra-pause loop). **`Close`** → drains the publisher.
- **Subject resolution.** The subject is **not** static (unlike a KV bucket name) — it is *per recipient*.
  So the adapter is **driven by the pipeline's per-actor fan-out** (§3.3): the pipeline calls `Upsert` once
  **per affected+authorized actor**, passing the actor key in `keys["__actor"]` (a reserved key field);
  the adapter resolves `lattice.sync.user.<id>` from it. This keeps the adapter SPI unchanged (it still
  takes `keys`/`row`) and puts the *who-receives-this* decision in the pipeline (where the
  `ActorEnumerator` already lives), not the adapter.
- **`TargetNATSSubjectConfig`** (`corekv_source.go`): `{ "subjectPrefix": "lattice.sync.user",
  "stream": "SYNC", "key": ["__actor", ...businessKeys] }`. The lens is marked **`personal: true`** (the
  analog of the capability lens's `ActorEnumerator` install flag) so `cmd/refractor` wires the per-actor
  fan-out for it.
- **Delta envelope (the wire shape the Edge consumes — documented in refractor.md):**

  ```json
  {
    "op": "upsert",
    "key": "vtx.lease.Op4Nb2mPq6rTwzKxVyP7.terms",
    "kind": "aspect",
    "class": "lease.terms",
    "anchor": "lease.Op4Nb2mPq6rTwzKxVyP7",
    "revision": 10481,
    "projectionSeq": 10481,
    "encrypted": false,
    "data": { "monthlyRent": 2400, "startDate": "2026-07-01" }
  }
  ```

  `data` is the **projected VAL fragment** (the Edge applies it to its local KV — vault *Personal Lens.md
  §2* "applies them to its local KV"; *not* a bare pointer — #655's early "hint/pointer" idea is superseded
  by the later vault doc where the Edge is the stateful store). For a **sensitive** aspect, `encrypted:
  true` and `data` is the **ciphertext envelope** (Vault §3.10); the Edge decrypts via transient key
  (§3.6). `revision`/`projectionSeq` let the Edge's Sync Manager do **last-writer-wins by revision** and
  detect gaps.
- **Guard posture: unguarded.** A subject publish is fire-and-forget append; ordering is the JetStream
  stream sequence within a subject; the Edge dedups/reorders by `revision`. No monotonic CAS watermark
  (that's a KV-bucket concern; a stream is naturally ordered). This mirrors the unguarded `NatsKVAdapter`
  default.

**Independently shippable + green:** a lens that fans projected rows to per-user subjects, e2e-asserted by
subscribing a test consumer and checking the delta envelopes — *with no security filter yet* (trusted
single identity). This is the standalone "NATS-subject publish-events adapter" backlog row, delivered.

### 3.2 Fire 1 (cont.) — the backing JetStream `SYNC` stream (ledger replay)

`lattice.sync.user.>` is captured by a JetStream stream `SYNC` with **short retention** (default
`MaxAge: 24h`, per-subject `MaxMsgsPerSubject` cap) — the vault's "ephemerality" property
(*Personal Lens.md §4.2*): a briefly-offline device **resumes from its last revision** (durable consumer
or `OptStartSeq`); a long-offline device gets a gap and **re-hydrates** (§3.5) rather than replaying a
week of backlog. Stream provisioning belongs to **bootstrap**, not an ad-hoc substrate primitive
(`[[no-substrate-ensurekv]]` — the same ruling that put KV provisioning in bootstrap; `EnsureStream` is
the accepted asymmetric exception the Refractor already uses, so the adapter may `EnsureStream` the SYNC
stream or bootstrap may pre-provision it — recommend bootstrap pre-provision for parity with core-kv).

### 3.3 Fire 2 — per-identity scoping (the Delta Projector + Interest Set)

**The fan-out compute** reuses the capability pipeline's machinery (§2.1). On each CDC delta the
`personal` pipeline:

1. **Enumerate affected actors** from the mutated vertex/link via the existing `ActorEnumerator`
   (adjacency BFS → reachable identities). *No new code* — same enumerator, configured `actorType:
   "identity"`.
2. **Relevance filter (Interest Set).** For each affected actor, intersect with that actor's device
   Interest Set(s). **Absent Interest Set ⇒ stream the full authorized slice** (the Interest Set is an
   efficiency/bandwidth filter, not a correctness one) — so Fire 2 is shippable before Interest-Set
   registration exists.
3. **Security filter (D1 — gated).** Intersect the changed anchor with the actor's **unioned readable
   anchors**. Per the *ratified, decomposed* D1 model (Contract #6 §6.14, 2026-06-27), this is **not** a
   single `cap-read.<actor>` doc — it is the **union across the actor's `cap-read.*.<actor>` slices**
   (core base + each package's `cap-read.<domain>`), i.e. the same set the Postgres `actor_read_grants`
   table unions for RLS. The Personal-Lens filter resolves that union (read the slices, or query
   `actor_read_grants`) and admits the delta only if the changed anchor is in it. **No `cap-read` grant for
   the actor ⇒ no stream** (fail-closed, mirroring §6.8). *This is the single point where Personal Lens's
   security becomes real — and it is wholly D1-derived, which is why the whole feature is gated behind D1.*
4. **Publish** the delta to `lattice.sync.user.<actor>` for each surviving actor (one `Upsert`/`Delete`
   call per actor, §3.1).

**The Interest Set (operational state, P1, Refractor-owned).** A device registers a watchlist with the
Refractor (vault *Personal Lens.md §3* — "registers an Interest Set with the Refractor", not "commits to
the graph"). It is **per-device, ephemeral subscription state — not business truth — so it does NOT go in
Core KV** (P1). It lives in a Refractor-owned **`personal-lens-interest`** KV bucket, keyed
`<identityId>.<deviceId>`, body `{ types: ["lease","payment"], anchors: ["lease.Op4…"], registeredAt,
revisionCursor }`. Registration is a **control RPC** on the existing Refractor control plane:
`lattice.ctrl.refractor.personal.register` (and `.deregister`) — the `micro.Service` precedent
(`internal/refractor/control/`). Refractor writing its own operational KV is sanctioned (the adjacency
index precedent — Refractor is the sole writer of `refractor-adjacency`). **No Core KV op, no P2
violation** (P2 governs *Core* KV; this is Refractor-private operational state).

### 3.4 Fire 3 — wire D1's `readableAnchors` (the security gate) — **gated on D1 ratification**

Replace the §3.3-step-3 `allow-all` stub with a real GET of `cap-read.<actor>` and an
`anchor ∈ readableAnchors` test. This is the **fail-closed** moment: **no `cap-read` entry ⇒ no stream**
(mirrors D1's "no entry = no read" / §6.8). The changed delta's `anchor` field (§3.1) is matched against
`readableAnchors[].{anchorType,anchorId}` — the same `authzAnchor` join D1 defines for row filtering, now
applied to a stream packet. Subject subscribe-authorization (who may `SUB lattice.sync.user.X`) rides the
**NATS-account-write-restriction** design's read-side (Fork 3). Until both land, the feature stays under
the trusted-single-identity carve-out (no untrusted Edge connects).

### 3.5 Fire 4 — the Hydration Hook (cold start)

A control RPC `lattice.ctrl.refractor.personal.hydrate { identityId, deviceId, sinceRevision? }`:

- **Cold (`sinceRevision` absent / gap):** the Refractor runs the personal cypher over the actor's **full
  authorized + interested slice** (a one-time bulk projection — mirrors `Pipeline.Rebuild`'s replay, but
  scoped to one actor's anchors via the enumerator in reverse: anchors → rows), and bulk-publishes the
  current-state deltas to `lattice.sync.user.<id>`, ending with a `hydrationComplete` marker carrying the
  high-water `revision`. The device then reverts to incremental.
- **Warm (`sinceRevision` present, within retention):** no hydration — the device resumes the durable
  consumer from its cursor. Hydration is only the cold/gap path (vault *Personal Lens.md §3* "Hydration
  Hook" + §4.2 "re-hydration").

This mirrors the existing `rebuild`/`replay` control ops (`internal/refractor/control/`) — a per-actor,
bounded replay rather than a whole-lens one.

### 3.6 Fire 5 — Secure Lens for the Edge (ciphertext deltas + transient-key decryption) — **gated on Vault Phase A**

The Edge path's "Secure Lens" is **not** the cloud queryable Secure Lens (Vault Phase B, which decrypts
into RLS-protected server-side plaintext and is D1-gated). For the Edge, the cloud **never decrypts**
(blind projection): sensitive aspects flow as **ciphertext deltas** (`encrypted: true`, §3.1 — Vault
Phase A already stores them as ciphertext, and the Refractor "projects ciphertext as-is"). The **Edge
decrypts locally** with a **transient session key** it requests from the cloud (vault *Edge Lattice.md
§5* — "Transient Decryption … requests a Transient Session Key from the Cloud Processor"). So Personal
Lens's confidentiality dependency is only **Vault Phase A** (ciphertext-at-rest) + a small **transient
session-key issuance RPC** (a Vault addition: `IssueSessionKey(identity, aspectScope, ttl)` → a
short-lived per-identity decryption key, revoked by the same `ShredKey` that kills the master). It does
**not** need Vault Phase B. **Crypto-shred composes for free:** a shredded identity's deltas are
already-garbage ciphertext and `IssueSessionKey` returns nothing → the Edge can never decrypt → "remote
shredding renders all local copies permanent gibberish" (Edge Lattice.md §5), no extra Personal-Lens work.

*This is a sharpening worth Andrew's eye:* the backlog row says "Secure Lens" as one thing; grounding
splits it into **(a)** the cloud queryable Secure Lens (Vault Phase B, D1-gated, server-side plaintext —
*not this design*) and **(b)** the Edge ciphertext-delta + transient-key path (*this design's Fire 5*,
Vault-Phase-A-gated, no server plaintext). They share Vault but are different consumers.

### 3.7 Read path (P5) / write path (P2) summary

| Concern | Mechanism | Invariant |
|---|---|---|
| **Read (Edge consumes)** | `SUB lattice.sync.user.<id>` (JetStream `SYNC`) + hydration RPC | P5 — a projection read; the Edge never reads Core KV |
| **Write (state changes)** | unchanged — ops → Processor → Core KV → CDC → Personal pipeline | P2 — Personal Lens writes **no** Core KV |
| **Subscription state** | `personal-lens-interest` KV (Refractor-owned) + register/hydrate control RPCs | P1 — operational, outside Core KV (adjacency precedent) |
| **Security filter** | D1 `readableAnchors` (build-to §6.14) | fail-closed: no `cap-read` entry ⇒ no stream |
| **Confidentiality** | ciphertext deltas (Vault §3.10) + Edge transient-key decrypt | blind projection — cloud never decrypts for the Edge |

---

## 4. Contract surface

| Contract / doc | Change vs. build-to | What |
|---|---|---|
| **`docs/contracts/*`** | **NO CHANGE** | The transport (subjects, delta envelope), the Interest Set, and the hydration op are **component-level** surfaces — they belong in `refractor.md`, not a frozen contract. No `docs/contracts/*` edit is staged by this fire. |
| **#6 Capability KV §6.14** | **build-to (D1's uncommitted edit)** | Personal Lens consumes `readableAnchors` / `cap-read.<actor>`; it adds nothing to §6.14. (Sequencing dependency on D1 ratification, not a contract change here.) |
| **#1 addressing** | **build-to** | `lattice.sync.user.<id>` follows the dotted subject convention; the Interest Set key `<identityId>.<deviceId>` follows KV key conventions. No new key shapes in Core KV. |
| **Vault §3.10** | **build-to (Vault's uncommitted edit)** | sensitive aspects are ciphertext at rest; Personal Lens forwards ciphertext + `encrypted: true`. The transient session-key RPC is a **Vault** addition (Fire 5), designed there, not a contract change. |
| **`docs/components/refractor.md`** | **DOC ADD (committed with the build, not now)** | New sub-sections: the `nats_subject` adapter, the delta envelope, the personal pipeline fan-out, the Interest Set bucket, the hydration/register control ops, and the SYNC stream. (The Steward writes these as it builds; this is a *component doc*, freely editable — not a frozen contract.) |

**Net: zero frozen-contract changes.** This is a pure build-to design — its only "contract" dependencies
are the two **already-staged-uncommitted** edits (D1 §6.14, Vault §3.10) it sequences behind. Nothing for
Andrew to ratify as a contract diff *for this feature* beyond ratifying D1 + Vault (which carry their own).

---

## 5. Migration, compatibility, test strategy

**Migration / compatibility.**
- **Purely additive + dark-launchable.** A new lens class (`personal: true`), a new adapter, a new stream,
  a new operational bucket, new control RPCs. Nothing existing changes. No app reads it until an Edge node
  exists; the current Loupe/app posture is untouched.
- **Trusted-single-identity first (the Loupe carve-out).** Fires 1–2 run with the security stub
  (allow-all) under the same trusted posture Loupe uses — verifiable end-to-end (a local consumer
  subscribes + asserts deltas) without D1. The **security door (Fire 3) is the only thing that needs D1**;
  it is the explicit gate, not a silent default.
- **No untrusted exposure pre-gate.** The design forbids connecting an untrusted Edge until Fire 3 + the
  subscribe-ACL (Fork 3) land — enforced operationally (don't expose the SYNC stream past the trust
  boundary) and documented as the gating condition on the board row.

**Test strategy.**
- **Unit:** `natssubject.go` adapter (envelope marshalling, subject resolution from `__actor`, upsert vs
  delete vs ciphertext-passthrough); `TargetNATSSubjectConfig` translate/validate; the Interest-Set
  intersection filter; the hydration scope resolver. Mirror `natskv_test.go` structure.
- **Ephemeral-stack e2e (the proving ground — `internal/refractor/*_e2e_test.go` precedent):**
  - *Fan-out:* seed two identities A, B with disjoint residence; mutate an A-anchored aspect; assert a
    delta lands on `lattice.sync.user.A` and **none** on `…user.B`. (With the stub allow-all this asserts
    the *enumerator+relevance* path; with Fire 3 wired it asserts the *security* path.)
  - *Interest Set:* A with an Interest Set of `{lease}` gets lease deltas but not payment deltas; A with
    no Interest Set gets the full authorized slice.
  - *Hydration:* cold device → bulk current-state deltas + `hydrationComplete` at the right high-water
    revision; warm device → resume, no bulk.
  - *Revision LWW / gap:* out-of-order publishes → the consumer's by-revision dedup keeps the latest; a
    retention gap → hydration path triggers.
- **Gate-3 (security, when Fire 3 lands — joins `make test-capability-adversarial`):** the read-bypass
  twins of D1 §5 — **(1)** an Edge for A requesting B's anchor in its Interest Set → **never** receives B's
  deltas (security filter wins over relevance); **(2)** an actor with no `cap-read` entry → empty stream
  (fail-closed); **(3)** a revoked actor (token-revocation / D1) → stream stops after CDC converges; **(4)**
  a stale lower-seq replay must not resurrect a revoked grant's delta. **(5)** *(Fire 5)* a shredded
  identity → deltas are ciphertext + `IssueSessionKey` denies → undecryptable.

---

## 6. Risks & alternatives

**Risks.**
- **R1 — security depends entirely on D1 (the headline).** Personal Lens has no native authorization; its
  filter *is* `readableAnchors`. *Mitigation:* the explicit Fire-3 gate + the trusted-single-identity
  posture for Fires 1–2 + the Gate-3 read-bypass suite. The design refuses to ship untrusted exposure
  before the gate. This is a *sequencing* risk, fully controlled, not an open hole.
- **R2 — fan-out cost / write amplification.** A change touching a high-degree anchor (e.g. a building
  announcement watched by 1 000 tenants) fans to 1 000 publishes. *Mitigation:* the enumerator is already
  actor-cap-bounded (10 000, logs+truncates); the **compute** is done once (evaluate the delta once, then
  fan-publish) — only the publish is per-user. The further **shared-subject multicast** optimization (vault
  §4.3 — 1 000 devices listen to one shared subject) is a *bandwidth* optimization deferred beyond this
  design (it changes the Edge subscribe model and is only worth it at real scale); per-user fan-publish is
  correct and adequate first.
- **R3 — projection staleness on the security plane.** A revoked grant stops the stream only after CDC
  converges (same bounded window as write-path Capability KV + D1). *Mitigation:* identical to D1 (R2) —
  the token-revocation kill-switch is the instant whole-actor cut; per-anchor revocation rides CDC lag
  (acceptable, matches the write path).
- **R4 — Interest Set as a covert read channel.** A malicious Edge could set its Interest Set to anyone's
  anchors. *Mitigation:* the Interest Set is **relevance only**; the **security filter (Fire 3) runs after
  it and overrides it** — an anchor in the Interest Set but not in `readableAnchors` is dropped. R4 is
  exactly why Fire 3 is non-negotiable and the relevance/security filters are *separate, ordered* steps.
- **R5 — offline device + short retention = missed deltas.** *Mitigation:* by design — the device
  re-hydrates on gap (§3.5); the SYNC stream is intentionally ephemeral (vault §4.2). The cost is a
  bulk hydration after a long absence, which is the intended trade (no week-long backlog replay).

**Alternatives considered (and why not).**
- **A stateful per-user projection (a Postgres table / KV bucket per user)** — rejected: O(users×devices)
  storage, the exact "Warehouse" anti-pattern the vault names. Personal Lenses are *filters, not clones*.
- **Decrypt sensitive aspects server-side and stream plaintext** (the cloud Secure Lens, Vault Phase B) —
  rejected for the Edge path: it puts plaintext on the wire + needs D1-protected server-side rows. The
  blind-projection + Edge-transient-key path keeps plaintext off the cloud entirely (§3.6).
- **Register the Interest Set as a Core KV vertex (an op)** — rejected: a per-device watchlist is
  *operational*, not business truth; putting it in the immutable ledger violates P1 and bloats the graph
  with ephemeral subscription churn. Refractor-owned operational KV (adjacency precedent) is the right home.
- **Ship the whole feature only after D1** — rejected: needlessly idles the large, security-inert
  transport/projector/hydration machinery that is independently testable under the trusted posture. Build
  the plumbing now; gate only the door.
- **WebSocket/push-bridge as part of this design** — rejected as scope: it's a Gateway/edge-delivery
  concern (Fork 1); the NATS-subject mechanism is transport-agnostic and the right cloud-side primitive.

---

## 7. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

Ordered so the security-inert machinery lands first under the trusted posture, the security gate is its own
fire (the one D1-gated step), and confidentiality + hydration extend it. **Dependency gates are explicit.**

1. **PL.1 — the `nats_subject` adapter + SYNC stream (the "brand-new adapter").** Implement
   `adapter/natssubject.go` (4-method SPI + ciphertext-passthrough), `TargetNATSSubjectConfig` +
   translate/validate, the `case "nats_subject"` in `cmd/refractor/main.go`, the delta envelope, and the
   SYNC JetStream stream (bootstrap-provisioned). Wire a trivial `personal: true` test lens that fans
   *projected rows* to `lattice.sync.user.<__actor>`. **No security filter, no Interest Set** — trusted
   single identity. *Green:* e2e subscribes a consumer and asserts delta envelopes. **Delivers the
   standalone "NATS-subject publish-events adapter" backlog row.** *No dependency.*

2. **PL.2 — the personal pipeline (per-actor fan-out + Interest Set).** Install the existing
   `ActorEnumerator` on the personal pipeline; per affected actor, intersect with the
   `personal-lens-interest` bucket (absent ⇒ full slice); publish per actor. Add the
   `personal.register`/`.deregister` control RPCs + the Refractor-owned interest bucket. **Security filter
   still stubbed allow-all** (trusted posture). *Green:* the fan-out + Interest-Set e2e (§5). *Depends on
   PL.1.*

3. **PL.3 — wire D1 `readableAnchors` (the security gate).** Replace the allow-all stub with the
   `cap-read.<actor>` GET + `anchor ∈ readableAnchors` fail-closed test; add the Gate-3 read-bypass suite
   (§5 vectors 1–4). Confirm subject subscribe-ACL rides the NATS-account-auth design (Fork 3). *Green:*
   Gate-3 read-bypass DEFENDED; A never sees B's deltas. **🚧 GATED on D1 ratification + (for untrusted
   exposure) the NATS-account-auth design.**

4. **PL.4 — the Hydration Hook.** The `personal.hydrate` control RPC: cold bulk projection +
   `hydrationComplete` high-water; warm resume. *Green:* hydration e2e (§5). *Depends on PL.2 (PL.3 for a
   real security-scoped hydration).*

5. **PL.5 — Secure Lens for the Edge (ciphertext deltas + transient key).** Confirm sensitive aspects fan
   as `encrypted: true` ciphertext (Vault §3.10 passthrough); add the Vault `IssueSessionKey` RPC + the
   Gate-3 shred vector (§5 vector 5). *Green:* a sensitive-aspect delta is ciphertext; a shredded identity
   can't decrypt. **🚧 GATED on Vault Phase A ratification.**

6. **PL.6 (deferred, recorded) — shared-subject multicast dedup** (R2 bandwidth optimization, vault §4.3)
   and the **WebSocket/push-bridge** edge-delivery (Fork 1, Gateway epic). Not built by this design; noted
   so the Steward doesn't re-discover them.

**Build-now vs. gated:** PL.1, PL.2, PL.4 (warm/cold mechanics) are buildable **now** under the trusted
posture. PL.3 gates on **D1**; PL.5 gates on **Vault Phase A**. The Steward can advance the platform
(adapter, fan-out, hydration) immediately on ratification of *this* design, then close the security +
confidentiality gates as D1 + Vault ratify.

---

## 8. Adversarial review note

This is a security-plane, cross-cutting L design composing onto two unratified designs — it warrants a
`bmad-party-mode` / adversarial pass before PL.1, and a re-review before PL.3 (the security gate). Highest-
leverage things to attack: **(a)** the relevance-vs-security filter ordering (§3.3 steps 2→3 / R4 — does
the security filter *provably* run last and override the Interest Set on every path, including hydration
and link-fan-out?); **(b)** the trusted-posture-vs-untrusted-exposure boundary (can the SYNC stream leak
past the trust boundary before PL.3 + the subscribe-ACL land?); **(c)** the ciphertext-passthrough +
shred composition (§3.6 / Gate-3 vector 5 — is there *any* path where the cloud touches plaintext, or
where a transient session key outlives a shred?); **(d)** fan-out write-amplification under a high-degree
anchor + the enumerator cap interacting with the publish loop (R2). Fold findings in before the gated
fires.

---

*Designer fire 2026-06-27 — Winston. One design, flagged for Andrew. Builds-to D1 (§6.14) + Vault
(§3.10); no frozen-contract change of its own. Board: 🏗️ designing → 📐 awaiting-Andrew.*
