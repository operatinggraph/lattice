# Edge Lattice (full) — the sovereign per-user node — design

**Status: ✅ Andrew-ratified (2026-06-29)** — FORK-A resolves to **A′ (predictive read-only local execution, never commits; cloud Processor stays sole authority)**, FORK-B = A (Go node first), FORK-C = A (reconcile-by-revision); no frozen-contract change; EDGE.1+2 buildable now (co-built with Personal Lens PL.1/2). A′ needs an **edge Starlark sandbox** (⑥'s `internal/starlarksandbox` with a local-mirror-backed read-only `kv` binding). **`bmad-party-mode` pre-build pass RUN (2026-06-29)** — 8 findings folded in (see the *Ratified + party-mode findings* block + §3.4/§3.5/§3.6 + §8). One cross-cutting finding (F8 — "scripts reading Core KV is the smell") flagged separately for Andrew. Author: Winston (Designer fire, 2026-06-29).

Backlog row: `planning-artifacts/backlog/lattice.md` → *Edge & personal lenses → Edge Lattice (full)*
(★★, XL). The **device-side capstone** that consumes the already-designed cloud seams: it is the concrete
**consumer** the Personal Lens build was waiting for, the per-user evolution Loupe was built to precede, and
the integration north-star for D1 + Vault + Gateway.

Grounds in: the Obsidian vault *Edge Lattice/Edge Lattice.md* + *Edge Lattice/Personal Lens.md* (the vision
of record), `docs/components/loupe.md` + `cmd/loupe/*` (the shipped trusted-tool precursor),
`lattice-architecture.md` (P2/P5/P1 invariants, the Read-Your-Own-Writes overlay note line 89/138, the
internal-service-actor + CDC-reactive convergence model), the frozen contracts #1 (key/subject shapes) /
#2 (operation envelope + OCC) / #6 (Capability KV) READ-only, and the **four upstream designs this composes
onto** — all ratified or awaiting-Andrew:

- **`personal-secure-lens-design.md`** (✅ ratified) — the cloud→Edge `nats_subject` delta-stream fan-out,
  Interest Set, Hydration Hook, ciphertext deltas. *This design is its missing consumer.*
- **`read-path-authorization-d1-design.md`** (✅ ratified, partly built) — the per-user read filter +
  `internal/gateway/auth` JWT read-actor seam (D1.2 shipped) the untrusted Edge authenticates with.
- **`vault-crypto-shredding-design.md`** (✅ ratified) — ciphertext-at-rest + the transient session-key path
  the Edge uses to decrypt sensitive aspects locally without ever holding the master key.
- **`gateway-external-trust-boundary-design.md`** (📐 awaiting-Andrew) — the HTTP→NATS verify-and-stamp
  translator the Edge submits its offline-queued ops through, and the WS/push edge-delivery bridge.

---

## For Andrew (one-look ratification block)

**What it does, in two lines.** Turns Loupe's trusted-tool local-first *posture* into a real **sovereign
per-user node**: a device holds a **local VAL mirror** of just its authorized slice (hydrated + kept fresh by
the Personal-Lens delta stream, **reconciled by revision**), serves the user zero-latency / offline-first,
and **queues its mutations as intents** that submit through the Gateway and reconcile against the cloud — the
cloud staying the single **Projector + sole writer**, the device staying the user's stateful, private "Personal
Slice." It is the device half of the same machinery Personal Lens projects from the cloud.

**The one thing to understand before ratifying — this design keeps P2 inviolate.** The vault vision
(*Edge Lattice.md §2*) describes an **authoritative local Edge Processor** that runs Starlark against a local
DDL and *commits* `PENDING` operations on-device. That would put a **second authoritative writer** on the
platform and duplicate the entire validation/DDL plane on every device — squarely against **P2 (the Processor
is the sole writer to Core KV — no exceptions)**, the platform's load-bearing serialization invariant. This
design instead builds the Edge as an **optimistic-overlay node**: the device applies a mutation **locally and
optimistically** for instant UX, **queues the intent**, and submits it through the **existing core-operations →
Processor** path on reconnect; the cloud Processor remains the **sole authority**, and a conflict
(`RevisionConflict`) drives a **local re-audit**, not a local commit. Offline-first and zero-latency are fully
delivered; P2 is untouched. The vault's authoritative-local-Processor (local Starlark / zero-knowledge proofs)
is preserved as a **deferred v2 layer** (EDGE.6, design-only) — *not built* until the overlay node is proven
and Andrew ratifies the P2-relaxation it would require.

**Frozen-contract change: NONE.** Like its parent Personal Lens design, the Edge node is a **pure consumer of
ratified seams**: it subscribes to the Personal-Lens delta stream (a `refractor.md` component surface), submits
ops through the existing Contract #2 envelope + OCC (build-to, no change), authenticates via the existing
`internal/gateway/auth` JWT seam (D1.2, built), and decrypts via the Vault transient-key RPC (a Vault addition,
designed there). The local VAL mirror, the intent queue, and the reconcile-by-revision protocol are all
**Edge-internal** (a new `cmd/edge` binary + `internal/edge/*`), invisible to the platform contracts. No
`docs/contracts/*` edit is staged by this fire.

**Three architectural forks I designed through — your call on all three (recommendations given):**

1. **Edge authority model (the headline P2 fork).** **A — optimistic-overlay node (RECOMMENDED):** local apply
   is advisory UX; the cloud Processor stays sole authority; conflicts re-audit. **B — authoritative local Edge
   Processor (the vault vision):** local Starlark + local DDL + on-device `PENDING` commit. *Rec A* — it
   delivers the same offline-first UX while preserving P2 and avoiding a second validation plane per device; B
   is a genuine architecture change (two authoritative writers, DDL distribution, cross-writer reconciliation)
   that should be a **separate, later, explicitly-ratified** evolution once the overlay node has proven the loop.
   See §3.4 + §6 FORK-A.

2. **First-node host + runtime.** **A — a Go reference node `cmd/edge` (RECOMMENDED):** the direct extension of
   Loupe (Go, native NATS, an embedded `bbolt` local store), proving the full local-first loop against the real
   cloud seams headlessly. **B — a browser/mobile PWA first** (WASM + SQLite/IndexedDB + a WS/push bridge). *Rec
   A* — it builds on the shipped Loupe pattern and the native NATS client, and **does not** wait on the unbuilt
   Gateway WS/push bridge; the browser node (B) is a later increment (EDGE.5) gated on that bridge. See §6 FORK-B.

3. **Offline write + conflict reconciliation protocol.** **A — an intent queue + reconcile-by-revision LWW +
   re-audit-on-conflict (RECOMMENDED), build-to the existing OCC + op-tracker + the architecture's RYOW overlay
   note.** **B — a CRDT / operational-transform merge layer.** *Rec A* — the platform is single-writer-authority
   (the Processor totally-orders every write), so there is exactly one truth to reconcile *to*; a CRDT merge
   layer solves multi-writer convergence the platform deliberately does not have. See §3.5 + §6 FORK-C.

**Sequencing — co-build EDGE.1 with Personal Lens PL.1 to avoid dead scaffolding.** The honest dependency:
the Edge can only mirror what the cloud projects, and the Personal-Lens SYNC stream (PL.1) is itself unbuilt.
So **EDGE.1 co-builds with Personal Lens PL.1/PL.2** as one initiative — PL.1 *produces* the stream, EDGE.1
*consumes* it; neither is the assumed-unbuilt-producer hazard, and together they are a complete, demoable,
trusted-posture offline-first loop. This **also un-gates Personal Lens** (which was explicitly waiting on "D1 +
a real consumer exists" — the Edge node is that consumer). Everything past EDGE.2 is gated on D1 / Vault /
Gateway exactly as Personal Lens is. See §7.

---

## Ratified + party-mode findings (Andrew, 2026-06-29)

**Decisions.** **FORK-A = A′** (predictive read-only local execution; the Edge runs the op's Starlark **locally to
predict** the optimistic overlay but **never commits** — the cloud Processor stays sole authority; its
streamed-back result replaces the prediction). **FORK-B = A** (Go reference node first). **FORK-C = A**
(reconcile-by-revision LWW + re-audit, not CRDT). **No frozen-contract change.** **EDGE.1 + EDGE.2 buildable now**,
co-built with Personal Lens PL.1/PL.2 (un-gates Personal Lens). A′ requires an **edge Starlark sandbox** — ⑥'s
shared `internal/starlarksandbox` leaf with the impure `kv` builtin swapped for a **local-mirror-backed, read-only**
binding (the Edge becomes the leaf's **third consumer**, after the Processor and the deferred Loom guard).

**P2 verdict (the headline the party stress-tested):** A′ holds P2 inviolate — a prediction is never an
authoritative write (the local store is P1 operational state; the cloud ledger is untouched), and no local commit
escapes the cloud Processor. The review was adversarial, not a rubber-stamp; 8 findings folded in:

- **F1 — speculation chain (§3.4/§3.5).** Predictions persist locally and chain (op #2 predicts off op #1's
  *unconfirmed* result). Model pending predictions as a **DAG rooted at confirmed state** (`pendingDeps`); a
  rejected intent **invalidates its whole downstream subtree** and re-audits it in order.
- **F2 — predicted events are inert (§3.4).** The script emits `{mutations, events}`; a **predicted** event is
  **local-UI-only** — never published, never triggers an external side-effect. Only the cloud's authoritative
  commit emits real events.
- **F3 — the A′ gating rule (§3.4, load-bearing).** Predict an op **iff its declared read-set
  (`contextHint.reads`) ⊆ the local mirror; else degrade to a pending-state (pure-A) for that op.** This single
  rule covers the missing-key case, the cross-slice-inference case, and (with F4) `kv.Links`.
- **F4 — enumeration isn't locally predictable (§3.4).** `kv.Links` is an open enumeration; the mirror **cannot
  know it holds *all* of a relation's links**, so a `kv.Links`-bearing op is **not** locally predictable unless
  the relation is **provably user-private** (all its links in the slice by construction) — else pending.
- **F5 — security holds, accuracy degrades (§3.4).** Predictive execution **cannot read beyond the slice** (the
  partial mirror *is* the security boundary — an unauthorized key simply isn't present, returns absent); a
  cross-slice read yields a **wrong-but-corrected prediction, not a leak**. So F3's degrade is about *prediction
  accuracy*, never *safety*.
- **F6 — ciphertext predictions are sensitive (§3.6).** An op over a decrypted aspect is **un-predictable
  offline** (needs the transient session key); online, the predicted overlay is plaintext-derived → it is
  **itself sensitive** (in-memory / encrypted-at-rest, never plaintext-persisted, TTL-discarded; shred composes).
- **F7 — conflict re-present is a SET (§3.5/§7).** A conflict invalidates a **subtree** of dependent edits, so
  "re-present the intent" is inherently a group. EDGE.2 needs a **conflict-presentation model** (resolve the
  root conflict first, then re-evaluate dependents — some may then apply cleanly); auto-retry only the
  provably-commutative class, which shrinks the subtree that ever reaches the user.
- **F8 — "scripts read Core KV" is the root smell (CROSS-CUTTING — flagged for Andrew, not folded here).** Live
  `kv.get`/`kv.Links` is the **common root** of *both* A′'s partiality *and* the #3 Loom-guard-read problem. If
  scripts were **pure functions of (declared read-set, op)**, A′ would be exact and Loom guards wouldn't need
  engine Core-KV reads. The clean platform posture: **declared+hydrated reads everywhere; live `kv.get`
  deprecatable as debt; `kv.Links` (enumeration) the irreducible hard case.** This connects #3/#6/#9/#10 and is a
  platform-direction call, not an Edge detail — see the board flag.

---

## 1. Problem & intent

**The gap.** Lattice today has a cloud graph and a set of **server-resident** read surfaces (lens projections,
Loupe's direct-Core-KV inspector view). There is **no node that lives on the user's device** — nothing that
holds the user's slice locally, serves it at zero latency, works offline, and reconciles with the cloud when it
reconnects. Loupe is explicitly framed as the *trusted-tool precursor* to this node and the backlog frames the
whole row as **"the path Loupe grows into,"** but Loupe is local-first only in *posture*: it reads **live Core
KV directly each request** (the sanctioned inspector exception, `docs/components/loupe.md` "P5 inspector
exception") — it holds **no local mirror, does no reconcile-by-revision, and cannot work offline**. The
device-side node is unbuilt.

**The intent (the vision of record).** The vault is precise (*Edge Lattice/Edge Lattice.md*): the Edge Lattice
is *"a sovereign, federated instance of the Lattice architecture running directly on a user's device … a
functional node of the graph that can operate independently of the cloud while remaining eventually consistent
with the global State of Truth."* Its mission is **Zero-Latency Interaction** + **Offline-First Sovereignty**:
the user owns their **"Personal Slice,"** while the cloud is the **secure, compliant Vault** for high-stakes
identity data + long-term retention. Five named mechanisms (vault §1–§5): the **Local VAL Store** (resident
graph, partitioned like NATS KV for byte-compatibility), the **Edge Processor** (local determinism), the **Sync
Manager** (ledger-replay reconciliation by revision + Personal-Lens subscription), the **Federated Weaver**
(local nudges + intent uploading + GC), and the **Vault Proxy** (transient-key decryption without holding the
master key).

**The architectural principle that makes it tractable.** *Personal Lenses are filters, not clones*
(*Personal Lens.md*): the cloud stores the global truth **once** (O(total entities)) and **projects** each
identity's authorized slice as a delta stream; the **Edge is the stateful store for the user** (O(user
activity)). So the Edge node is **not** a new graph engine — it is a **subscriber + local applier + intent
queuer** on top of mechanisms that already exist or are designed: it consumes the Personal-Lens stream,
applies deltas to a local KV mirror keyed by the **same Contract #1 shapes**, and pushes mutations back as
**ordinary operations** through the **ordinary write path**. The whole node is an *integration* of ratified
seams, not an invention — which is exactly why it carries **no frozen-contract change** and why its hard work
is **sequencing + the P2-preserving overlay protocol**, not new platform primitives.

---

## 2. Grounding — the patterns this extends (do not redesign them)

**2.1 Loupe is the shipped precursor — extend its shape, don't greenfield.** `cmd/loupe` is a Go server that
owns **all** NATS I/O behind a thin view, connects as a trusted single identity, binds `127.0.0.1`, and drives
the platform via the existing control planes + op submission (`docs/components/loupe.md`). The Edge reference
node (`cmd/edge`) is the **same shape** with three additions Loupe lacks: a **local VAL mirror** (instead of
live Core-KV reads), a **Sync Manager** (subscribe + reconcile-by-revision), and an **intent queue** (offline
writes). The trusted-single-identity + loopback posture, the Go-owns-NATS-I/O pattern, and the
`make run-loupe`-style launch are all reused verbatim.

**2.2 The cloud→Edge stream already designed (Personal Lens).** `personal-secure-lens-design.md` defines the
`nats_subject` adapter publishing a **delta envelope** (`{op, key, kind, class, anchor, revision,
projectionSeq, encrypted, data}`) to `lattice.sync.user.<id>` over a short-retention JetStream `SYNC` stream,
plus the **Interest Set** (relevance filter), the **Hydration Hook** (cold-start bulk + `hydrationComplete`
high-water), and **ciphertext deltas** for sensitive aspects. The Edge **consumes** this verbatim — it is the
"device subscribes, applies deltas to its local VAL, decrypts sensitive aspects with a transient key" half that
design's For-Andrew block names. **No new wire shape is invented here.**

**2.3 The write path is already the way mutations flow (P2).** `lattice-architecture.md` P2: every mutation is
an operation → `core-operations` → Processor → atomic batch → Core KV, with **revision-condition OCC** (step 8,
`expectedRevision`) + the **idempotency tracker** `vtx.op.<requestId>`. The architecture already names the
client side: a **Read-Your-Own-Writes mitigation** — *"the Processor's response includes the `vtx.op` tracker
ID, and the client polls the tracker until the projection catches up"* (line 138), with the full overlay
deferred to Stream 5 (line 89). The Edge's offline write path **builds directly to this**: queue the intent,
submit it as an ordinary op on reconnect, poll the tracker, reconcile by revision. The Gateway design adds the
verify-and-stamp translator the Edge submits through once it is an untrusted external actor.

**2.4 The auth + confidentiality seams are designed producers.** D1.2 shipped `internal/gateway/auth` (verify a
signed JWT → `actor_id` = full vertex key) + `internal/gateway/revocation` (per-request kill-switch) — the
Edge's identity proof. The Vault design ships the `Vault` interface (`Encrypt/Decrypt/ShredKey`) +
ciphertext-at-rest (§3.10); Personal Lens Fire 5 adds the **transient session-key issuance RPC** the Edge calls
to decrypt locally. The Edge **consumes** these; it invents no crypto and holds no master key (vault §5).

**Invariants this design inherits.**
- **P2 (sole writer) — preserved by the optimistic-overlay choice (§3.4).** The Edge writes **no** Core KV and
  is **not** an authoritative writer; it submits ordinary operations. This is the single most important property
  of the whole design.
- **P5 (lenses are the only query surface) — the Edge consumes the Personal-Lens stream**, a projection read;
  it never reads cloud Core KV (it is **not** Loupe's inspector exception — the Edge is a *user* node, not the
  admin console).
- **P1 (operational vs business state) — the local mirror, the intent queue, the revision cursor, and the
  Interest Set are device-local operational state**, never the cloud ledger.
- **Contract #1 — the local store is keyed by the same `vtx.*` / aspect / `lnk.*` shapes** (the vault's
  "byte-for-byte compatibility" — a local key is a cloud key), so an applied delta and a queued intent are
  expressed in the platform's own vocabulary with zero translation.

---

## 3. The shape

A new module: a Go reference node **`cmd/edge`** (the binary) + **`internal/edge/*`** (the engine), mirroring
the Loupe layout. Five internal components map 1:1 to the vault's five (§1):

| Vault component | `internal/edge/*` | Role |
|---|---|---|
| Local VAL Store | `store/` | embedded local KV mirror (bbolt), keyed by Contract #1 shapes |
| Sync Manager | `sync/` | subscribe Personal-Lens stream, apply deltas LWW-by-revision, hydrate |
| Edge Processor | `overlay/` | optimistic local apply + UI-discovery (advisory, NOT authoritative) |
| Federated Weaver | `agent/` | local nudges + the intent queue uploader + local GC |
| Vault Proxy | `vault/` | request a transient session key, decrypt sensitive aspects locally |

### 3.1 The Local VAL Store (`store/`)

An embedded, transactional local KV (**`bbolt`** — pure-Go, no cgo, single-file, the natural Go-reference
choice; SQLite/IndexedDB only enter with the browser node, EDGE.5). It mirrors Core KV's **partitioned, keyed**
shape: keys are the exact Contract #1 strings (`vtx.<type>.<id>`, `vtx.<type>.<id>.<localName>`,
`lnk.<typeA>.<idA>.<rel>.<typeB>.<idB>`), values carry the projected VAL fragment **plus the cloud
`revision`** that produced them (the reconcile cursor). Two design properties from the vault:

- **Sovereign aspects (device-only).** The store may hold aspects the user creates locally that are **never**
  published (drafts, private notes) — kept in a `local:` key namespace the Sync Manager never uploads. (v1
  scaffolds the namespace; the publish/no-publish policy per-aspect is an EDGE.2 author concern.)
- **Shadow vertices.** The store keeps thin "shadow" anchors of remote entities the user's links point at,
  without downloading the whole remote dataset — the local key exists with whatever fragment the Personal Lens
  has delivered, no more. This is automatic: the store only ever holds what the stream delivered.

### 3.2 The Sync Manager (`sync/`) — reconcile-by-revision

The heart of the node. A durable consumer on the Personal-Lens `SYNC` JetStream stream (native NATS for the Go
node; WS for the browser node, EDGE.5):

- **Apply (inbound).** For each delta envelope (§2.2): **last-writer-wins by `revision`** — apply iff the
  delta's `revision` ≥ the locally-stored revision for that key (a stale/duplicate/reordered delta is dropped;
  JetStream delivers at-least-once + can reorder, exactly what the by-revision rule absorbs — the same
  duplicate/reorder tolerance the cloud CDC-reactive components use, `lattice-architecture.md:103`). `op:upsert`
  writes the fragment + revision; `op:delete` tombstones the local key.
- **Cursor + gap.** The Sync Manager persists its **revision cursor** (last applied stream sequence) in the
  local store. On a brief disconnect it **resumes** the durable consumer from the cursor (the vault's "resume
  from last revision"). On a long disconnect past the SYNC stream's short retention, the resume **gaps** → it
  triggers a **Hydration** (§3.3) rather than replaying a backlog (the vault's "ephemerality": re-hydrate, don't
  backlog-replay).
- **Conflict signal (with the write path, §3.5).** When an inbound delta lands on a key that has a
  **locally-pending optimistic mutation** (§3.4), the Sync Manager marks that intent **conflicted** for the
  agent's re-audit (§3.5) instead of blindly overwriting the user's in-flight edit.

### 3.3 Hydration (cold start) — consume the Personal-Lens Hydration Hook

On first boot or after a gap, the node calls the Personal-Lens **`personal.hydrate`** control RPC
(`{identityId, deviceId, sinceRevision?}`, designed in Personal Lens Fire 4): the cloud Refractor bulk-projects
the actor's current authorized + interested slice to `lattice.sync.user.<id>`, ending with a
`hydrationComplete` marker at the high-water revision. The node ingests the bulk deltas into the local store,
sets its cursor to the high-water, and **reverts to incremental**. The node also **registers its Interest Set**
(`personal.register`, Personal Lens Fire 2) — the device's watchlist of the vertex types / anchors it cares
about — so the cloud streams only the relevant slice. (Pre-Personal-Lens, EDGE.1's trusted-posture variant
hydrates via a one-time bounded read of the trusted stream — see §7 EDGE.1.)

### 3.4 The Edge "Processor" (`overlay/`) — optimistic, advisory, **not** authoritative (FORK-A: A′)

> **Ratified refinement (Andrew, 2026-06-29): A′, not pure-A.** The overlay **predicts** the result via a
> **read-only local Starlark run** in the **edge sandbox** (⑥'s `internal/starlarksandbox` leaf with a
> local-mirror-backed, read-only `kv` binding), gated by the **F3 rule — predict iff the op's declared
> `contextHint.reads ⊆ the local mirror`, else degrade to a pending-state** (F4: a `kv.Links`/enumeration op is
> predictable only if its relation is provably user-private). Predicted **events are inert** (F2 — local-UI only,
> never published); predictions **chain as a DAG** rooted at confirmed state and a rejected intent invalidates
> its whole downstream subtree (F1); predictive execution **cannot read beyond the slice** — the partial mirror
> *is* the security boundary, so a missed read costs *accuracy*, never *safety* (F5). See the *Ratified +
> party-mode findings* block. The pure-A "render the payload directly" path below is the **degrade fallback** for
> ops whose read-set isn't locally satisfiable.

This is the P2-preserving core decision (For-Andrew fork 1). When the user triggers a mutation:

1. **Optimistic local apply.** The node writes the intended fragment to the local store under a
   **`pending:` overlay** keyed by the target key, with a fresh client-side `requestId` — the UI immediately
   reflects the change (zero-latency, offline-capable). This is the "Read-Your-Own-Writes" overlay
   (architecture line 89/138) realized locally: the user sees their own write before the cloud confirms it.
2. **Queue the intent.** The mutation is enqueued (durably, in the local store) as a fully-formed **operation
   envelope** (Contract #2 shape) — *not* committed, *not* validated as authoritative. The local store can run a
   **best-effort advisory validation** (shape / required fields) for UX, but it is explicitly **not** a
   substitute for the cloud Processor's authoritative validation — it never decides truth.
3. **UI discovery (the vault's "UI Discovery").** The overlay traverses the **local** graph (the user's own
   links / ReBAC slice as delivered by the stream) to tell the front-end which actions/components to render —
   purely a presentation read over local state, no authority.

The overlay **never** writes Core KV and **never** commits an authoritative op; it is a UX accelerator + an
intent producer. **P2 is untouched.** (FORK-A option B — the authoritative local Edge Processor that *commits*
locally — is designed-through in §6 and deferred to EDGE.6.)

### 3.5 The intent uploader + reconcile-by-revision (`agent/`) — offline writes (FORK-C: A)

The outbound half — the vault's "Intent Uploading" + the architecture's RYOW overlay:

- **Submit on reconnect.** When connectivity returns, the agent dequeues each pending intent and **submits it as
  an ordinary operation** to `core-operations` — through the **Gateway** once the Edge is an external actor
  (the Gateway verify-and-stamps `env.Actor`, so the Edge's claimed identity is unforgeable — gateway design),
  or directly under the trusted posture pre-Gateway (EDGE.1/2). The Processor commits (or rejects) it as the
  **sole authority**.
- **Track + confirm (RYOW).** The submit response carries the `vtx.op.<requestId>` tracker (architecture line
  138). The agent **polls the tracker** (or waits for the corresponding inbound delta on the SYNC stream — the
  cloud will project the committed change back) and, on confirmation, **clears the `pending:` overlay** (the
  authoritative cloud value, now in the local store via the stream, replaces the optimistic one).
- **Reconcile on conflict (the only hard case).** If the op **rejects** (`RevisionConflict` at step-8 OCC, or
  an auth/validation rejection) — i.e. the cloud state moved under the offline edit — the agent **re-audits**:
  it **re-hydrates the affected anchor** (a bounded `personal.hydrate sinceRevision` for that anchor), discards
  the stale optimistic overlay, and **re-presents the intent to the user** against fresh state (auto-retry only
  for a provably-commutative class, e.g. an idempotent set-membership add; never silently for a value
  overwrite). This is **reconcile-by-revision to a single authoritative truth** — *not* a multi-writer merge
  (FORK-C option B, CRDT/OT, rejected in §6: the platform has exactly one authoritative writer, so there is one
  truth to reconcile to and a merge layer is solving a problem the architecture doesn't have).
- **Local GC + nudges (the vault's Federated Weaver).** The agent prunes confirmed overlays + applied-and-aged
  deltas + stale shadow vertices to bound device storage (vault §4 "Garbage Collection"), and can raise
  **local-only nudges** over the local slice (vault §4 "Local Nudging") — a thin device-local analog of the
  cloud Weaver, with **no** cloud notification. (v1: GC + intent upload; local nudges are an EDGE.2+ nicety.)

### 3.6 The Vault Proxy (`vault/`) — transient-key local decryption (the vault's §5)

Sensitive aspects arrive as **ciphertext deltas** (`encrypted: true`, Personal Lens §3.1 / Vault §3.10) — the
cloud is a **blind projector**, it never puts plaintext on the wire. To display one (e.g. the user's own SSN on
a form), the node requests a **transient session key** from the cloud (the Personal-Lens Fire 5 /
`IssueSessionKey(identity, aspectScope, ttl)` RPC), decrypts **locally + in-memory** (never persisting
plaintext to the local store), and discards the key on TTL. **Crypto-shred composes for free** (vault §5
"Remote Shredding"): after `ShredIdentityKey`, the cloud's `IssueSessionKey` returns nothing and the local
ciphertext is permanent gibberish — "all local copies become unrecoverable" with zero extra Edge work. The node
**holds no master key** and **persists no plaintext** — the GDPR/compliance posture the vault demands.

### 3.7 Read path (P5) / write path (P2) / state-class (P1) summary

| Concern | Mechanism | Invariant |
|---|---|---|
| **Read (device serves the user)** | local VAL mirror, kept fresh by the Personal-Lens `SUB lattice.sync.user.<id>` + hydration | P5 — the stream is a projection; the Edge never reads cloud Core KV |
| **Write (user mutates)** | optimistic local overlay → intent queue → ordinary op via Gateway → Processor | P2 — the Edge is **not** a writer; the cloud Processor stays sole authority |
| **Local state (mirror / queue / cursor / Interest Set)** | device-local `bbolt`, never uploaded | P1 — operational, never the cloud ledger |
| **Identity** | JWT via `internal/gateway/auth` (D1.2); Gateway stamps `env.Actor` | unforgeable external-actor identity (gateway design) |
| **Security filter (which slice)** | Personal Lens `readableAnchors` (D1 §6.14) on the cloud side | fail-closed: no `cap-read` grant ⇒ empty stream (the Edge receives only what it may read) |
| **Confidentiality** | ciphertext deltas + transient-key **local** decrypt; no persisted plaintext | blind projection — cloud never decrypts for the Edge; shred composes |

---

## 4. Contract surface

| Contract / doc | Change vs. build-to | What |
|---|---|---|
| **`docs/contracts/*`** | **NO CHANGE** | The Edge node introduces no platform key/subject/op shape. It consumes the Personal-Lens delta envelope (a `refractor.md` surface), submits ordinary Contract #2 ops (build-to), authenticates via the D1.2 JWT seam (built), decrypts via the Vault RPC (a Vault surface). All Edge-internal state is device-local operational KV. No `docs/contracts/*` edit is staged by this fire. |
| **#2 operation envelope + OCC §2 / §3.7** | **build-to** | Offline intents submit as ordinary ops; conflict reconciliation rides the existing step-8 `expectedRevision` OCC + the `vtx.op.<requestId>` tracker (RYOW, architecture line 138). The Edge adds nothing to the envelope. |
| **#6 Capability KV §6.14** | **build-to (via Personal Lens)** | The Edge receives only its `readableAnchors` slice; the filter is wholly the cloud's (D1). The Edge enforces nothing — it cannot see what it is not sent. |
| **#1 addressing** | **build-to** | The local store + the queued intents use the platform's own dotted key shapes (the vault's byte-for-byte compatibility). |
| **Personal Lens (`refractor.md` component surface)** | **build-to** | The delta envelope, the `SYNC` stream, and the `personal.register`/`personal.hydrate` control RPCs are the Edge's consumption interface — defined in the Personal Lens design, documented in `refractor.md` at build. |
| **Vault transient session-key RPC** | **build-to (Vault/Personal-Lens Fire 5 addition)** | `IssueSessionKey(identity, aspectScope, ttl)` is a Vault surface designed in Personal Lens Fire 5; the Edge calls it, adds nothing to it. |
| **`docs/components/edge.md`** | **DOC ADD (committed with the build, not now)** | A new component doc (the Edge is an application like Loupe, not a frozen-contract engine): the local-first loop, the five sub-components, the trusted→untrusted posture progression, the offline-write/reconcile protocol, the P2-preserving overlay decision. |

**Net: zero frozen-contract changes** — identical posture to the parent Personal Lens design. The Edge node's
only "contract" dependencies are the upstream designs it sequences behind (Personal Lens, D1, Vault, Gateway),
each of which carries its own already-staged edits. Nothing for Andrew to ratify as a contract diff *for this
feature*.

---

## 5. Migration, compatibility, test strategy

**Migration / compatibility.**
- **Purely additive — a new module.** `cmd/edge` + `internal/edge/*` + a new local-store dependency (`bbolt`,
  recorded in `docs/vendors.md` at build). Nothing existing changes; no app, no platform binary, no contract is
  touched. The current Loupe / app / cloud posture is byte-identical.
- **Trusted-single-identity first (the Loupe carve-out).** EDGE.1/EDGE.2 run as **one trusted identity** under
  the same posture Loupe + Personal Lens PL.1/PL.2 use — verifiable end-to-end (a `cmd/edge` node mirrors a live
  trusted slice, applies deltas, queues + submits an offline intent, reconciles a conflict) **without** D1 /
  Gateway / Vault. The untrusted multi-identity exposure (EDGE.3+) is the explicit gate, never a silent default.
- **No untrusted exposure pre-gate.** The design forbids an untrusted Edge connecting until EDGE.3 (D1 filter +
  Gateway JWT + the NATS subscribe-ACL) lands — enforced operationally (the Go node binds loopback / a trusted
  network, like Loupe) and stated as the gating condition on the board row.

**Test strategy.**
- **Unit (`internal/edge/*`):** the by-revision LWW applier (apply-newer / drop-stale / drop-duplicate /
  tombstone-on-delete); the cursor + gap→hydrate trigger; the overlay (optimistic write → pending key →
  cleared-on-confirm); the intent queue (durable enqueue / dequeue / requestId determinism); the reconcile path
  (conflict → re-audit, commutative-auto-retry vs present-to-user); the local GC bounds. Mirror Loupe's
  pure-function + httptest discipline.
- **Ephemeral-stack e2e (the proving ground — pair with Personal Lens's `internal/refractor/*_e2e_test.go`):**
  - *Mirror convergence:* mutate a cloud aspect → the delta lands → the `cmd/edge` local store converges to the
    new value at the right revision; a `delete` op → the local key tombstones.
  - *Cold-start hydration:* a fresh node `personal.hydrate` → bulk current-state into the local store →
    `hydrationComplete` high-water → reverts to incremental.
  - *Offline write + reconcile:* node goes offline, user mutates (optimistic overlay shows it) → reconnect →
    intent submits → tracker confirms → overlay clears, authoritative value arrives via the stream.
  - *Conflict:* the cloud value moves while the node is offline → the offline intent rejects `RevisionConflict`
    → the node re-audits (re-hydrates the anchor, re-presents the intent) — **no** local authoritative commit,
    **no** silent overwrite.
  - *Gap → re-hydrate:* a disconnect past SYNC retention → resume gaps → hydration path triggers (no backlog
    replay).
- **Gate-3 (security, when EDGE.3 lands — joins `make test-capability-adversarial`):** the Edge twins of
  Personal Lens's read-bypass suite — a node for actor A **never** receives B's deltas even with B's anchors in
  its Interest Set (the cloud security filter wins); a node with no `cap-read` grant gets an empty stream
  (fail-closed); a revoked JWT (D1 revocation) cannot submit an intent or hold a subscription; **(EDGE.4)** a
  shredded identity's sensitive deltas are ciphertext + `IssueSessionKey` denies → the node can never decrypt,
  and persists no plaintext to disk.

---

## 6. Risks & alternatives — the three forks designed through

**FORK-A — Edge authority model (the headline).**
- **A (RECOMMENDED) — optimistic-overlay node.** Local apply is advisory UX; the cloud Processor is sole
  authority; conflicts re-audit. *Pros:* preserves **P2** exactly (the platform's load-bearing invariant); no
  second validation/DDL plane per device; offline-first + zero-latency fully delivered via the local overlay +
  intent queue; builds entirely to existing OCC + tracker. *Cons:* an offline mutation is **provisional** until
  the cloud confirms — a conflicting edit is re-audited, not auto-merged. (This is correct, not a defect: there
  is one authoritative truth.)
- **B — authoritative local Edge Processor (the vault vision §2).** Local Starlark + local DDL + on-device
  `PENDING` commit; zero-knowledge proofs. *Pros:* fullest sovereignty (the device decides locally); enables
  the vault's ZK-proof privacy ("prove sufficient funds, send only the result"). *Cons:* it puts a **second
  authoritative writer** on the platform — directly against **P2** — and requires **distributing + versioning
  the DDL to every device**, a **local validation that must byte-match the cloud** (or diverge silently), and a
  genuine **cross-writer reconciliation** model. That is an architecture change, not an Edge feature.
- **Recommendation: A now; B as a separate, later, explicitly-ratified evolution (EDGE.6, design-only).** The
  optimistic-overlay node delivers the mission (zero-latency + offline-first sovereignty) while keeping P2
  inviolate; once it has proven the loop end-to-end, B can be layered as a *bounded local-authority* extension
  (e.g. local commit only for a whitelisted, provably-commutative, device-private op class) **if** a concrete
  need (ZK proofs, true disconnected-authority) justifies relaxing P2 — Andrew's call at that point. *I
  recommend not building B until that need is real.*

**FORK-B — first-node host + runtime.**
- **A (RECOMMENDED) — Go reference node `cmd/edge`** (native NATS, embedded `bbolt`). *Pros:* the direct
  extension of the shipped Loupe pattern; consumes the real cloud seams headlessly + testably; **does not wait**
  on the unbuilt Gateway WS/push bridge; proves the entire local-first loop before any browser complexity.
  *Cons:* not itself the end-user mobile device.
- **B — browser/mobile PWA first** (WASM + SQLite/IndexedDB + WS/push bridge). *Pros:* the real per-user device.
  *Cons:* couples the first Edge increment to the **unbuilt** Gateway WS/push bridge + a WASM build + a
  different local store — three new variables before the core loop is even proven.
- **Recommendation: A first; B is EDGE.5, gated on the Gateway WS/push bridge.** Prove the loop in Go against the
  real cloud, then port the *same* `internal/edge` engine to a browser host with a SQLite/IndexedDB store + WS
  transport. The engine is host-agnostic by construction (the store + transport are interfaces).

> **Edge Starlark runtime feasibility — VERIFIED (empirically, 2026-07-02).** The A′ edge sandbox runs the same
> `go.starlark.net` interpreter the Processor uses (pure Go, no cgo, AOT tree-walking — no runtime codegen, so
> Apple's no-JIT rule is a non-issue). Verified on every host FORK-B contemplates: **Go node** — native execution
> (trivial, same library). **Browser/PWA (EDGE.5)** — compiles for `js/wasm` *and* `wasip1/wasm`; the `js/wasm`
> binary **executed correctly under V8** (Android Chrome's engine; iOS WebKit runs the same engine-agnostic
> `wasm_exec.js` path); interpreter-only module ≈ 4.9 MB raw / 1.3 MB gzipped. **Native apps** —
> `GOOS=ios GOARCH=arm64` `c-archive` builds with an exported C symbol (the exact artifact `gomobile bind`
> wraps into an `.xcframework`), and the same-family macOS archive was **called from a C host end-to-end**
> (embedded interpreter ran a script and returned the right value); `GOOS=android GOARCH=arm64` builds pure-Go
> with no NDK (the NDK is only the JNI-shim C toolchain for `.aar` packaging — nothing in the interpreter can
> fail there). **One open item, native-iOS route only:** App Store guideline 2.5.2 (downloaded interpreted code
> that changes app functionality) scrutinizes exactly the cloud-synced-DDL-script pattern — a store-policy
> question, not a runtime one; it does not apply to the PWA route or to Android. Resolve it only if a native
> iOS app is ever proposed.

**FORK-C — offline write + conflict reconciliation protocol.**
- **A (RECOMMENDED) — intent queue + reconcile-by-revision LWW + re-audit-on-conflict.** Build-to the existing
  OCC (`expectedRevision`) + the `vtx.op` tracker + the architecture's RYOW overlay note. *Pros:* there is
  exactly **one** authoritative truth (the Processor totally-orders every write), so reconciliation is a
  well-defined "rebase my intent onto the latest cloud revision"; cheap, deterministic, contract-free. *Cons:* a
  conflicting offline value-overwrite needs the user (or a commutative-class auto-retry), not a silent merge.
- **B — CRDT / operational-transform merge layer.** *Pros:* automatic multi-replica convergence. *Cons:*
  **solves a problem the platform deliberately does not have** — multi-writer convergence. With a single
  authoritative writer, a CRDT layer is dead weight + a second consistency model competing with the ledger's
  total order; it would also fight OCC. Rejected.
- **Recommendation: A.** It is the honest realization of the deferred RYOW overlay (Stream 5) for the Edge.

**Other risks.**
- **R1 — dead-scaffolding / assumed-unbuilt-producer.** The Edge can only mirror what the cloud projects, and
  the Personal-Lens stream is unbuilt. *Mitigation:* **co-build EDGE.1 with Personal Lens PL.1** as one
  initiative (§7) — producer + consumer land together, each is the other's reason to exist, and the pair
  un-gates Personal Lens (which was waiting on "a real consumer"). This is the anti-thrash discipline (walk the
  full path to the demo; sequence one-way; never hand off a consumer that assumes an unbuilt producer).
- **R2 — local store / cloud drift (silent divergence).** A bug in the LWW applier could leave the mirror
  permanently stale. *Mitigation:* the cursor + periodic / on-demand **re-hydration** is the authoritative
  reset (the mirror is disposable cache, the cloud is truth); a `hydrationComplete` high-water is the
  convergence checkpoint asserted in e2e.
- **R3 — optimistic overlay shows a write that never commits.** *Mitigation:* the overlay is visibly
  *provisional* (a pending state in the UI) and is **cleared by the authoritative cloud value**, not by local
  fiat; a permanent reject re-audits + re-presents. The user never silently loses or silently keeps a rejected
  edit.
- **R4 — transient-key plaintext leak to disk.** *Mitigation:* §3.6 — plaintext is **in-memory only**, never
  persisted to the local store; the key is TTL-discarded; shred composes. A Gate-3 vector asserts no plaintext
  hits the `bbolt` file.
- **R5 — security depends entirely on the cloud filter (the Edge enforces nothing).** *Mitigation:* correct by
  design — the Edge receives only its `readableAnchors` slice (D1); it cannot leak what it is never sent. EDGE.3
  is the gate that turns the real filter on; pre-gate, only the trusted single identity connects.

**Alternatives rejected (beyond the forks).**
- *Make Loupe itself the Edge node (add a local mirror to `cmd/loupe`).* Rejected: Loupe is the **admin
  inspector** (Core-KV direct-read exception, full-graph, no per-user filter) — the opposite of a per-user
  sovereign node. The Edge is a **new** application that reuses Loupe's *shape*, not its *privileges*.
- *Stream raw Core-KV CDC to the device.* Rejected: that is the unfiltered whole-graph push channel Personal
  Lens exists to prevent (NFR-S2). The Edge consumes the **filtered** Personal-Lens stream only.

---

## 7. Decomposition for the Steward (fire-by-fire, each independently shippable + green)

> **🏗️ CHECKPOINT (2026-07-10, Steward).** EDGE.1 + EDGE.2 CLOSED — the offline-first read loop AND the
> optimistic write path are done. **EDGE.1 done:** `internal/edge/store` (§3.1, the Local VAL Store) —
> bbolt-backed, Contract #1-keyed, `ApplyUpsert`/`ApplyDelete` LWW-by-revision, persisted `Cursor`,
> scaffolded `local:` sovereign namespace (`1783f10`). `internal/edge/sync` (§3.2, the Sync Manager) —
> `substrate.RunDurableConsumer` on the `SYNC` stream (per-actor `lattice.sync.user.<id>` filter, stable
> per-device durable name), a locally re-declared `deltaEnvelope` mirroring `natssubject.go`'s wire shape,
> `store.ApplyUpsert`/`ApplyDelete` per delivered delta + `store.SetCursor` advancing on every applied
> message, cold-start/gap-triggered `personal.register`+`personal.hydrate` control RPCs, warm resume
> otherwise. `cmd/edge` — the binary wiring `store`+`sync` (mirrors `cmd/loupe`'s flat layout).
> **EDGE.2 done:** `internal/edge/overlay` (§3.4, pure-A this increment — no local Starlark prediction;
> A′ is gated on the edge Starlark sandbox and not built here) — `Apply` installs the caller-supplied
> intended value as a pending overlay (baseline = the confirmed revision at apply-time) over `store`;
> `Read` merges pending-over-confirmed and lazily retires an overlay once the confirmed entry's revision
> advances past that baseline (R3: cleared by the authoritative value from ANY source, never local submit
> success alone — F1/F7's chained-prediction DAG invalidation is deferred to A′, since pure-A has no
> chaining to invalidate); `Discard` drops a rejected intent's overlay; `Links` (new `store.ScanPrefix`)
> answers "UI Discovery" over confirmed+pending link keys. `internal/edge/agent` (§3.5) — durable
> `store`-backed intent queue (`EnqueueIntent`/`ListIntents`/`DeleteIntent`, FIFO by bbolt
> `NextSequence`); `Drain` submits queued envelopes to `core-operations` (a locally-reproduced submit
> helper mirroring `cmd/lattice/output.SubmitOp`, per the `internal/pkgmgr` no-cmd-dependency
> precedent), stopping at the first transport failure so a later `Drain` resumes; `RevisionConflict` (the
> only hard case — cloud state moved under the offline edit) triggers `sync.Manager.Rehydrate` (new
> export reusing the existing full `personal.hydrate` call — no anchor-scoped hydrate RPC ships, so no
> narrower primitive invented) before discarding the overlay; any other rejection discards without
> re-hydrating; `GC` sweeps overlays a `Read` never revisited. `cmd/edge` wires overlay+agent, draining +
> GC-sweeping on a fixed interval. Unit tests (embedded NATS + a fake Processor responder for `agent`)
> cover accept/duplicate/conflict/other-rejection/transport-failure/malformed-intent/FIFO-order.
> `docs/components/edge.md` updated in the same commit.
> **EDGE.3 CLOSED (2026-07-12).** `internal/edge/agent` now depends on a pluggable `Submitter`
> (agent.go): `GatewaySubmitter` (submit_gateway.go) POSTs to the Gateway's `/v1/operations` with
> `EDGE_TOKEN` as Bearer, mirroring `cmd/loupe/gatewayrelay.go`'s wire shape — the Gateway re-verifies
> the token and stamps `env.Actor` itself, never trusting a client-asserted identity; `NATSSubmitter`
> (submit_nats.go, the extracted EDGE.1/2 direct-submit path) remains for tests / fully-trusted
> deployments. `cmd/edge` now wires `GatewaySubmitter` as its production Submitter (`EDGE_GATEWAY_URL`,
> default `http://localhost:8080`) — the last of the three untrusted-posture legs (Gateway-verified JWT,
> PL.3 fan-out, subscribe-ACL) is live. The Gate-3 Edge read-bypass suite (§5) proving the write side —
> `internal/gateway/edge_gate3_e2e_test.go`'s `TestEdgeGate3_ValidTokenSubmitsThroughGateway` /
> `TestEdgeGate3_RevokedTokenNeverSubmits` — wires a real `gateway.Server` + `auth.Authenticator` (not a
> fake HTTP stub) in front of a real `agent.Agent`, proving a valid token's envelope reaches submit with
> the Gateway-stamped actor and a revoked token is denied before ever reaching it, leaving the intent
> queued rather than discarded. `docs/components/edge.md` updated in the same commit. **Next: EDGE.4**
> (Vault Proxy, gated on Vault Phase A + PL.5) or EDGE.5 (browser node, gated on the Gateway WS bridge).
>
> **🏗️ EDGE.4 increment 1 SHIPPED** — the identity-bound `sessionkey` control RPC
> (`lattice.ctrl.refractor.personal.sessionkey`), the server-side half of §3.6's `IssueSessionKey`
> call: mirrors `register`/`deregister`/`hydrate` exactly — `dispatchEndpoint`'s §3.4 binding confines
> `body.IdentityID` to the verified actor for `sessionkey` too, so a `scope=any` grant to `consumer`
> never lets one identity mint another's session key (proven by a Gate-3-style identity-binding test,
> the hydrate vector's twin). Refractor already holds its own `*vault.LocalBackend` in-process (the
> Secure-Lens decryptor's vault) — no new NATS RPC to the Processor-hosted `lattice.vault.*` subjects
> was needed; `personalSessionKey` reads the caller's own `piiKey` envelope off its existing `coreKV`
> handle and calls `IssueSessionKey` directly. Grants added in lockstep at all 3 required places
> (`internal/controlauth.RefractorOps`, `packages/control-authz` + `packages/console-operator`
> manifests, `internal/gateway/natsauth.controlRPCs`) — `TestPackage_GrantedCtrlVerbsMatchControlauthOpTables`
> catches a future miss here automatically. **§8's "dedicated review fire before EDGE.4" point (d) is
> now addressed for the trust-boundary half**: the transient-key path's authorization is proven at the
> control layer against a real verified-actor JWT binding. **Not yet built**: the `internal/edge/vault`
> client package (request + TTL-cache the session key, decrypt ciphertext deltas in-memory, never
> persist plaintext, discard on TTL) and its wiring into the Edge node's local read path — that's
> increment 2. §8 points (b)/(e) remain open for a future review pass.

Ordered so the security-inert local-first loop lands first (co-built with its cloud producer), the security
turn-on is its own gated fire, and confidentiality + the real device extend it. **Dependency gates explicit.**

1. **EDGE.1 — the Go reference node: local VAL mirror + Sync Manager (reconcile-by-revision) + hydration.**
   *Co-built with Personal Lens PL.1 (+ PL.2 trusted-posture fan-out).* `cmd/edge` + `internal/edge/store`
   (bbolt, Contract #1 keys) + `internal/edge/sync` (subscribe the SYNC stream, LWW-by-revision apply, cursor,
   gap→hydrate) + `personal.hydrate`/`personal.register` consumption. **Trusted single identity, no security
   filter** (the same carve-out Loupe + PL.1/PL.2 use). *Green:* the mirror-convergence + cold-hydration +
   gap→re-hydrate e2e (§5) against a live trusted slice. **Delivers the offline-first read loop end-to-end and
   un-gates Personal Lens (it is the real consumer PL was waiting for).** *Depends on: Personal Lens PL.1/PL.2
   (co-built).*

2. **EDGE.2 — the optimistic write path: overlay + intent queue + reconcile-by-revision.** `internal/edge/overlay`
   (optimistic local apply + pending overlay + UI-discovery over the local slice) + `internal/edge/agent`
   (durable intent queue, submit-on-reconnect via the existing `core-operations` path, tracker poll/confirm,
   conflict→re-audit, local GC). **Trusted posture** (direct op submit, pre-Gateway). *Green:* the offline-write
   + conflict + confirm e2e (§5). **Delivers offline-first zero-latency writes with P2 preserved.** *Depends on:
   EDGE.1.*

3. **EDGE.3 — untrusted multi-identity (the security turn-on).** The node authenticates via
   `internal/gateway/auth` (JWT — Contract #11's frozen profile + subject binding, ratified 2026-07-10) +
   submits intents through the **Gateway** (verify-and-stamp `env.Actor`); the SYNC stream is
   **security-filtered** by Personal Lens PL.3 (`readableAnchors`, ✅ shipped); the connection is scoped by
   the **per-identity subscribe-ACL** (✅ CLOSED, all 3 fires — [design](per-identity-nats-subscribe-acl-design.md)).
   Add the Gate-3 Edge read-bypass suite (§5). *Green:* A never sees B's slice; no-grant ⇒ empty; revoked
   JWT ⇒ no submit/subscribe. **✅ CLOSED (2026-07-12)** — `GatewaySubmitter` + the write-side Gate-3 suite
   shipped (see the checkpoint above); the read-side vectors (A/B isolation, no-grant⇒empty) were already
   proved by PL.3's own e2e suite, which the same live subscribe-ACL now scopes. *Depends on: EDGE.2.*

4. **EDGE.4 — the Vault Proxy (sensitive aspects).** `internal/edge/vault` — request a transient session key,
   decrypt ciphertext deltas **in-memory** (no persisted plaintext), TTL-discard; the Gate-3 shred vector. *Green:*
   a sensitive delta is ciphertext on the wire + in the store; a transient key decrypts it for display; a
   shredded identity can never decrypt. **🚧 GATED on Vault Phase A + Personal Lens PL.5 (the `IssueSessionKey`
   RPC).** *Depends on: EDGE.1 (mirror) + EDGE.3 (an authenticated identity to scope the key to).*

5. **EDGE.5 — the browser/mobile node (the real per-user device).** Port the *same* `internal/edge` engine to a
   WASM/browser host: a SQLite/IndexedDB store implementation + a **WebSocket transport** + **push-notification
   wake** (the device-can't-hold-TCP problem). *Green:* the browser node runs the same convergence + offline
   e2e against the WS bridge. **🚧 GATED on the Gateway WS/push edge-delivery bridge.** *Depends on: EDGE.3
   (the engine is host-agnostic; this swaps store + transport).*

6. **EDGE.6 (deferred, design-only) — the authoritative local Edge Processor (FORK-A option B).** Local Starlark
   + distributed DDL + bounded local-authority commit + zero-knowledge proofs (vault §2 full vision).
   **NOT built by this design** — it relaxes P2 and is a separate architecture decision; recorded here so the
   Steward doesn't re-discover it, and flagged to **not build until the overlay node is proven and Andrew
   ratifies the P2-relaxation.** *Gated on: a concrete ZK/disconnected-authority need + Andrew.*

**Build-now vs. gated.** **EDGE.1 + EDGE.2 + EDGE.3 are done** — the complete offline-first loop
(security-inert, trusted posture) plus the untrusted multi-identity security turn-on (Gateway-verified
submit + PL.3 fan-out + subscribe-ACL). **EDGE.4 on Vault Phase A + PL.5 (both since shipped) behind
EDGE.3; EDGE.5 on the Gateway WS bridge; EDGE.6 is design-only.** This mirrors the ratify-now /
build-as-foundations-land posture already accepted for Multi-cell, HA-NATS, and Personal Lens itself.

---

## 8. Adversarial review note

**✅ `bmad-party-mode` pre-build pass RUN (Andrew present, 2026-06-29)** — discharging the Designer-lane obligation
before EDGE.1. Lenses: Winston (architect), Mary (root-cause), Amelia (impl), Quinn (QA/break-it), Sally (UX),
Barry (lean). **8 findings folded in** (F1 speculation-DAG, F2 inert predicted-events, F3 the predict-iff-reads⊆mirror
gate, F4 enumeration-not-predictable, F5 security-holds/accuracy-degrades, F6 ciphertext-prediction-is-sensitive,
F7 conflict-re-present-as-a-set, F8 the cross-cutting "scripts-read-Core-KV smell" flagged for Andrew) — see the
*Ratified + party-mode findings* block. **EDGE.3 shipped 2026-07-12 (Steward fire) with targeted tests, not a
full `bmad-party-mode` re-review**: `TestEdgeGate3_ValidTokenSubmitsThroughGateway` /
`TestEdgeGate3_RevokedTokenNeverSubmits` (`internal/gateway/edge_gate3_e2e_test.go`) prove points **(a)**-partial
(no local commit — every accepted path still routes through the real Gateway+Processor reply contract) and
**(c)** (a revoked/invalid token is denied before any envelope reaches submit) against a real
`gateway.Server`+`auth.Authenticator`, plus the full `go test ./...` suite green. **The full multi-persona
adversarial pass this note calls for — (b) reconcile-by-revision soundness, (d) the transient-key path, (e) the
PL.1 co-build seam — is still open**; flag for a dedicated review fire before EDGE.4 (which composes the
transient-key path directly onto this boundary). Highest-leverage things to attack: **(a)** the P2 boundary — is there *any* path,
including the optimistic overlay + the intent queue + a conflict re-audit, where the Edge becomes an
authoritative writer or a local commit escapes the cloud Processor? (the whole FORK-A rationale rests on "no");
**(b)** reconcile-by-revision soundness — can a stale/reordered/duplicate delta or an OCC-conflicting offline
intent leave the local mirror permanently divergent or silently lose/keep a rejected user edit? (R2/R3/FORK-C);
**(c)** the trusted→untrusted boundary — can the SYNC stream or an intent submission leak past the trust
boundary before EDGE.3 + the subscribe-ACL land? (R5); **(d)** the transient-key path — any route where
plaintext is persisted, a session key outlives a shred, or the cloud decrypts for the Edge? (R4 / §3.6);
**(e)** the co-build seam with Personal Lens PL.1 — does the consumer assume anything the producer doesn't
deliver (envelope fields, hydration markers, retention)? Fold findings in before the gated fires.

---

*Designer fire 2026-06-29 — Winston. One design, flagged for Andrew. Builds-to Personal Lens + D1 (§6.14) +
Vault (§3.10) + the Gateway; **no frozen-contract change of its own**. The headline call is FORK-A
(optimistic-overlay, P2-preserving) — recommended A, designed B through, deferred to EDGE.6. Board: 🏗️ designing
→ 📐 awaiting-Andrew.*
