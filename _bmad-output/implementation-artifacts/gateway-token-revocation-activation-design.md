# Gateway token-revocation kill-switch — activation design

**Status: 📐 awaiting-Andrew (ratification).** Designer fire (Winston, bmad-architect), 2026-07-02.
Owning lane: **Lattice**. Owning component: **Gateway** (`internal/gateway`, `cmd/gateway`) +
**identity-domain** package (the ops) + **bootstrap** (the bucket). Backlog row:
`gateway-revocation-kill-switch-activation` (arch-review intake, `lattice.md`).

---

## For Andrew (one-look ratification)

**What it does (two lines).** Wires the Gateway's dormant token-revocation kill-switch end to end: a
`RevokeActor`/`UnrevokeActor` op emits a `gateway.actorRevoked`/`actorUnrevoked` **event** through the
standard Processor outbox onto `core-events`; the **Gateway consumes `events.gateway.>` into its own
local KV** (the `token-revocation` bucket the `revocation.Checker` already reads) and checks it
fail-closed per request. Revocation is now **populated** (was: nothing wrote the bucket), **auditable**
(a committed op + a durable event), and **fail-closed at startup** (was: a failed bucket-open silently
disabled the whole kill-switch).

**Architectural fork:** **none open.** The three candidate shapes (Loupe-writes-the-bucket ·
Refractor-lens-projects-the-bucket · **Gateway-owned materializer from an outboxed event**) were
resolved by **your steer of 2026-07-02** (`loupe-platform-edges-ux.md` §Adjudication → refines §2.3):
the `RevokeActor` op outboxes an event the Gateway consumes into its own internal-state KV. This design
builds **that** shape; the two superseded alternatives are recorded in §7 for the record only.

**Frozen-contract change:** **none.** The Gateway has no interface contract of its own (`gateway.md`);
the op rides the existing Contract #2 envelope + Contract #3 §3.4 outbox; the grants are package-declared
into Capability KV (Contract #6) by the standard permission lens. Nothing under `docs/contracts/*` is
edited — this is **build-to**. (The one lattice-architecture.md mention — "Token Revocation KV …
kill-switch for compromised actors" — is a planning-artifact the design *realizes*, not a contract it
changes.)

**Ratify → the Lattice Steward builds it (2 fires, §6).** It unblocks **Loupe F11** (the Gateway revoke
console — a Loupe-lane fire that submits `RevokeActor` via `/api/op` and lists the revoked set).

---

## 1. Problem + intent

The Gateway is the external trust boundary: it verifies an IdP-signed JWT and stamps the verified actor
into every op (Fire 1+2, shipped). A JWT verifies on **signature + expiry alone**, so a *compromised*
actor keeps write (and, under D1, read) access until its short token expires. The **token-revocation
kill-switch** is the out-of-band answer (lattice-architecture.md "Token Revocation KV … kill-switch for
compromised actors"; brainstorm **#111**): a set the Gateway consults per request so a revoked actor is
refused **immediately**, with the short JWT TTL as the backstop for the propagation-lag window.

**The machinery is half-built and inert** (arch-review 2026-07-02, finding #6; `gateway.md` §"revoked
actor → 403"):

- `internal/gateway/revocation.Checker.IsRevoked` **exists** — reads the `token-revocation` bucket,
  presence-of-key = revoked, fail-closed on a read error. It is the per-request gate D1 increment 2
  wired in.
- **Nothing populates the bucket.** No op, no admin surface, no writer of any kind. The kill-switch has
  never been armable.
- **The bucket is provisioned nowhere.** `revocation.BucketName = "token-revocation"` is not in
  bootstrap's primordial set (`internal/bootstrap/primordial.go`), so on a real stack the bucket-open
  fails.
- **A failed bucket-open downgrades to verification-only auth** (`cmd/gateway/main.go:186-218`): the
  checker is left `nil` "best-effort," and `auth.Authenticator` skips the revocation step entirely — a
  **default-open** failure mode. The downgrade is no longer *silent* (a warning log + a
  `GatewayRevocationDisabled` Health-KV issue ship today), but the Gateway still **serves external
  writes with the kill-switch off** — visibility is not enforcement. §2.4 replaces the downgrade with
  refuse-to-start.

**Intent:** arm the kill-switch — a populated, auditable, fail-closed revocation set — with the smallest
Lattice-native shape, using the mechanism Andrew steered.

---

## 2. The shape (grounded in the loom-lifecycle precedent)

The whole design **mirrors one established pattern**: the **event-only op → Processor outbox →
`core-events` → a component materializes its own operational state** loop that Loom's lifecycle already
runs (`packages/orchestration-base/loom_lifecycle.go`: `StartLoomPattern`/`CompletePattern`/`FailPattern`
emit `loom.patternStarted/Completed/Failed`; Loom's fixed consumer folds them into `loom-state`). The
Gateway becomes the analogous materializer for revocation.

```
  Loupe / admin actor                Processor (P2)                   core-events            Gateway (materializer)
  ───────────────────                ──────────────                   ───────────           ─────────────────────
  submit RevokeActor{actor,reason}   step 8: tracker-only atomic      events.gateway.        durable consumer on
    via /api/op  ───────────────▶      batch + outbox aspect          actorRevoked  ───────▶ events.gateway.>  ──▶ writes
                                       (vtx.op.<id>.events)              {actor,at,by,reason}   token-revocation KV
                                       ↓ outbox consumer publishes                              (put on revoke,
                                     ┌──────────────────────────────────────────────────────┐  del on unrevoke)
                                     │ core-events (durable, 7d) — the auditable event stream │        │
                                     └──────────────────────────────────────────────────────┘        ▼
                                                                                          revocation.Checker.IsRevoked
                                                                                          reads the LOCAL bucket
                                                                                          per request → 403
```

### 2.1 Write path (P2) — the ops (identity-domain)

Two **event-only** ops, authored exactly like the loom-lifecycle ops (empty `mutations`, one `events`
entry; the Processor commits a tracker-only atomic batch and the outbox publishes — verified sound by
`processor.TestCommit_ZeroMutationEventOnly`):

| Op | Payload | Emits (`EventSpec.Class`) | Event `data` |
|---|---|---|---|
| `RevokeActor` | `{actor: "vtx.identity.<id>", reason?: string}` | `gateway.actorRevoked` | `{actor, at, by, reason}` |
| `UnrevokeActor` | `{actor: "vtx.identity.<id>"}` | `gateway.actorUnrevoked` | `{actor, at, by}` |

- `at` = commit timestamp; `by` = the submitting actor (`env.Actor`, available to the Starlark script as
  the actor context) — this is what makes the ledger **who-revoked-whom-when**.
- **No business mutation.** Revocation is *operational* security state, not graph state — modelling it as
  an identity aspect (`vtx.identity.<id>.revoked`) would be a Core-KV write that then needs a projector
  and a retraction path (over-grant risk on unrevoke — a dropped composite key doesn't retract). The
  **event is the source of truth**; the Gateway's local bucket and any future history lens are
  projections. This matches the loom-lifecycle "operational-only, no Core-KV vertex" decision exactly.
- **Reversible.** `UnrevokeActor` is the fat-finger undo the F11 UX requires; the materializer deletes
  the local key (§2.3). Revoke/unrevoke fold as last-writer-per-actor.

**Op placement — recommend identity-domain.** These are identity-actor-lifecycle ops keyed on
`vtx.identity.<id>`; identity-domain is **always installed** and owns the identity vertex type, so no new
package (and no repeat of the `privacy-base` install-wiring break that aborted `make up-full`). A
dedicated `gateway-base` package (cleaner ownership) is the alternative — rejected for the extra
manifest/registry/verify-gate surface with no offsetting benefit; the ops are small and identity-scoped.
(A one-line placement call, not a fork — the Steward may move it if identity-domain's schema grows
awkward, but identity-domain is the recommendation.)

### 2.2 Event-type declaration (Contract #3 §3.4)

Declare the two event types in identity-domain's DDL (`gateway.actorRevoked`, `gateway.actorUnrevoked`).
Contract §3.4/§3.8 event-type-DDL **validation is currently a no-op** (unbuilt + separately tracked in
the arch-review intake as `step6-batch-internal-consistency-decision`), so emission does not *require*
the declaration today — but declaring it is forward-compatible (the validator lights up against a
declared type, never an undeclared one) and self-documents the `events.gateway.>` family. Zero cost, so
declare them.

### 2.3 The Gateway materializer (the one new mechanism)

The Gateway gains a **durable `core-events` consumer** filtered to `events.gateway.>`
(`substrate.ConsumerSupervisor`/`RunDurableConsumer` — the same surface Loom/Weaver/the bridge already
use, no new substrate primitive), folding events into the **`token-revocation` bucket** it already reads:

- `gateway.actorRevoked{actor}` → `KVPut(token-revocation, actor, {revokedAt, by, reason})`.
- `gateway.actorUnrevoked{actor}` → `KVDelete(token-revocation, actor)`.
- The existing `revocation.Checker.IsRevoked` (presence = revoked) reads this bucket **unchanged** — the
  per-request check is untouched; only its *writer* is new.

**Why the Gateway owns the bucket (not a Refractor lens projecting into it).** Andrew's steer, and the
reason it's right: the kill-switch is a **security-critical path that must not share fate with the
projection engine.** A Gateway-owned consumer means a revocation propagates even if Refractor is
degraded/paused; the Gateway's local check is independent, self-healing (durable-cursor replay), and
needs no other component live. (A Refractor lens → `token-revocation` NATS-KV target would also give a
"local" read, but couples the kill-switch's liveness to Refractor — the wrong coupling for a security
primitive. Recorded in §7.)

**Cold-start rebuild (correctness).** The local bucket is a *projection* of the durable `core-events`
history, so a fresh Gateway (or one whose bucket was wiped) rebuilds by replaying `events.gateway.>` from
the durable stream — exactly how Weaver's registry and Loom's pattern-source rebuild from their CDC
sources. The startup sequence (§2.4) makes this a **precondition of serving**, closing the cold-start
gap.

### 2.4 Fail-closed startup (fixes the default-open downgrade)

Replace the "best-effort `nil` checker → disabled" path with a **required, fail-closed** bring-up
(mirroring the Gateway's own fail-closed JWT key loading — "no IdP ⇒ no external writes"):

1. Bootstrap provisions `token-revocation` (§2.5) → the bucket **always exists**.
2. At startup, **before the HTTP listener binds**, the Gateway: opens the bucket, attaches the
   `events.gateway.>` durable consumer, and **catches up to the consumer's current end** (an initial
   drain so the local set reflects all events committed before this instance started).
3. If the bucket can't open **or** the consumer can't attach → `run()` returns an error and the Gateway
   **refuses to start.** No silent-disable path remains; the checker is never `nil`.

**Steady-state posture (fail-safe with a bounded window, stated honestly).** Once serving:

- A **per-request KV read error** → `IsRevoked` returns an error → `Authenticate` denies (**fail-closed**,
  already the Checker's behavior). This is what "fail-closed per request" governs.
- A **consumer disconnect** after startup → the local set is *stale but readable* (last-known-good); the
  Gateway keeps serving off it and emits a **Health-KV issue** (`revocation.consumerDisconnected`,
  §2.6). A revocation that arrives *during* the outage is missed until the durable consumer reconnects
  and replays from its ack floor (no event lost — only delayed); the **short JWT TTL is the backstop**
  for that lag window, exactly as `revocation.go`'s package doc already documents for the CDC-lag window.
  Denying *all* traffic on any consumer blip (the only strictly-fail-closed alternative) is unusable and
  not what the model calls for; the propagation-lag window is a known, TTL-bounded property, not a new
  hole.

### 2.5 Bucket provisioning (bootstrap)

Add `token-revocation` to `internal/bootstrap/primordial.go` (a `GatewayRevocationBucket` const reusing
the existing name) + `ProvisionBuckets`, alongside `weaver-targets`/`loom-state`:

- **Durable, no TTL** — it is a materialized set that must survive (rebuildable from events, but not
  ephemeral); `AllowAtomicPublish` off (single-key puts, no atomic batch); default history 1 (a
  compacting set, latest-per-actor). Mirror the `weaver-targets` provisioning test assertions.
- Idempotent re-provision (the established `ProvisionBuckets` contract).

### 2.6 Health surfacing (Loupe F11 enabler)

Extend `internal/gateway.Heartbeater`'s `health.gateway.<instance>` doc with a `revocation` block —
`{consumerConnected: bool, revokedCount: int, lastEventSeq: int, lastSyncAt: rfc3339}` — and aggregate a
`revocation.consumerDisconnected` **issue** (error-severity) into the heartbeat status when the consumer
is down (this also discharges half of the `heartbeat-false-green-aggregation` intake row for the Gateway:
the heartbeat stops being unconditionally green when the kill-switch feed is broken). Loupe F11 reads
this block to render the revoke panel's live state; the panel's revoked-list read is the ordinary P5-ish
Health-KV read Loupe already does — **no new Gateway HTTP surface** (`loupe-platform-edges-ux.md` §2.3
"Loupe write-free").

### 2.7 Grants (Capability KV, Contract #6)

Grant `RevokeActor` + `UnrevokeActor` to the **`operator` role at `scope: any`** (a platform kill-switch
is not self-scoped) via identity-domain's `permissions.go`. This covers **Loupe's operator actor**
(the F11 revoke surface — Andrew-approved in the UX adjudication) and any admin operator. Grants project
into Capability KV by the standard permission lens — no contract edit, no bespoke anchor. Absence of the
grant = deny (Contract #6 §6.8 fail-closed default), which is correct: a non-operator cannot revoke.

### 2.8 Transport grant (NKey matrix)

The Gateway NKey (`deploy/gen-dev-nkeys/main.go`, the `gateway` entry) today grants
`pubAllow: [ops.>, $KV.health-kv.>, $JS.API.>, $JS.ACK.>]`. The materializer must **write its own
bucket** — add `$KV.token-revocation.>` to `pubAllow`. (Consumer attach/ack rides the already-granted
`$JS.API.>`/`$JS.ACK.>`; subscribe on the deliver subject is open in the current matrix.) This is a
sanctioned **operational-state** write (the Gateway's own bucket, like Loom→`loom-state`,
Weaver→`weaver-state`), **not** a Core-KV write — P2 is untouched (P2 governs `core-kv`; the deny on
`$KV.core-kv.>`/`$KV.capability-kv.>` stays). Extend `natsperm` vectors to pin the new grant.

---

## 3. Reconciliation with the existing mental model

- **"Didn't we already build revocation?"** — Half. The *reader* (`revocation.Checker`, D1 increment 2)
  is built and correct; the *writer*, the *provisioning*, and the *fail-closed bring-up* are the gap.
  This design adds exactly those three, and leaves the per-request check untouched.
- **"Isn't an outboxed-event materializer a new pattern?"** — No. It is the loom-lifecycle pattern
  (event-only op → outbox → component materializes its own operational KV) applied to a second component.
  The Gateway joins Loom/Weaver/the bridge as a `core-events`/CDC consumer; no new substrate primitive.
- **"Does this introduce new state we don't already keep?"** — The `token-revocation` bucket already
  exists as a concept (the Checker reads it, config names it); this design makes it *provisioned and
  populated*. The heartbeat `revocation` block is new but rides the existing Health-KV plane.
- **"Where's the audit trail?"** — In the durable `core-events` event (7d) **and** in the committed op
  in the `core-operations` intent-ledger. Once the **Chronicler** ships (ratified, Lattice-lane), an
  `events.gateway.>` history lens is a trivial add (the Chronicler's F2 already consumes
  `events.<domain>.>` eventStream lenses) — but this design **does not depend on it** (no dead
  scaffolding / assumed-consumer): auditability is real today via the durable event + the intent-ledger.

---

## 4. Contract surface

**No frozen-contract edit.** Build-to:

- **Contract #2** (op envelope) — `RevokeActor`/`UnrevokeActor` are ordinary ops; no envelope change.
- **Contract #3 §3.4** (events / outbox) — reuse the `vtx.op.<id>.events` outbox aspect verbatim.
- **Contract #6** (Capability KV) — grants are package-declared + lens-projected; no §-edit.
- **Contract #5** (Health KV) — the `revocation` block is an additive metrics extension under the
  existing `health.gateway.<instance>` shape (§5.2), same as the object-store/gateway counters.
- Gateway has **no interface contract of its own** (`gateway.md` preamble), so the new consumer + startup
  behavior are documented in `gateway.md` (updated in the build commit), not a contract.

---

## 5. Migration / compatibility · test strategy · risks

**Migration.** New bucket (idempotent provision) + new consumer + two new ops. No data migration — an
empty revocation set is the correct initial state. The `revChecker`-becomes-required change is the only
behavior change to shipped code; it is strictly *more* secure (removes a silent-disable). A dev stack
without the new bootstrap seed picks up the bucket on the next `ProvisionBuckets` (idempotent).

**Tests.**
- *Unit* — the ops emit the correct event with zero mutation (mirror `TestCommit_ZeroMutationEventOnly`);
  schema `oneOf` accepts `{actor,reason?}`/`{actor}` and rejects extras; the materializer folds
  revoke→put / unrevoke→del; cold-start replay rebuilds the set from a seeded event history; the
  startup path fails closed when the bucket/consumer is unavailable.
- *e2e (ephemeral stack)* — submit `RevokeActor` → assert `events.gateway.actorRevoked` on `core-events`
  → assert the Gateway's `token-revocation` key appears → a `POST /v1/operations` bearing the revoked
  actor's token returns **403**; then `UnrevokeActor` → the key clears → the next request is accepted.
- *Gate-3 adversarial vector* — add a **revoked-actor-is-refused** vector to the bypass/capability gate
  (a revoked actor's structurally-valid token must not write), sibling to vector #14 (forged-actor).
- *natsperm* — a vector pinning `$KV.token-revocation.>` writable by the Gateway NKey and denied to
  others.

**Risks.**
- *Propagation lag* (revoke → enforced) is bounded by consumer latency (push durable, sub-second
  typical) + the JWT-TTL backstop for a consumer outage — §2.4, accepted and TTL-bounded.
- *Reserved-bucket hygiene* — `token-revocation` is a Gateway-owned operational bucket; it must be on the
  reserved-bucket denylist a package lens cannot target (folds into the separate
  `lens-target-reserved-bucket-guard` intake row — noted, not owned here).

---

## 6. Decomposition for the Steward (2 fires, each green + valuable)

The op and the materializer are **coupled** (an op nobody consumes is inert; a consumer with no producer
is dead) — so **Fire 1 is the whole enforcement loop**, one fire with an internal build order
(per `feedback_fewer_larger_fires`). Fire 2 is the observability richness for the console.

**Fire 1 — arm the kill-switch (M).** ① bootstrap provisions `token-revocation` (§2.5); ② identity-domain
gains `RevokeActor`/`UnrevokeActor` event-only ops + the two event-type DDLs + the `operator`/`any`
grants (§2.1–2.2, 2.7); ③ the Gateway materializer consumer + cold-start catch-up + **fail-closed
startup** replacing the `nil`-checker path (§2.3–2.4); ④ the NKey `$KV.token-revocation.>` grant +
natsperm vector (§2.8); ⑤ the minimal `revocation.consumerDisconnected` Health issue (§2.6, the
fail-safe half); ⑥ the e2e + Gate-3 revoked-actor vector; ⑦ `gateway.md` updated to as-built. **Ships
the full revoke→403→unrevoke loop, independently green.**

**Fire 2 — observability for the console (S).** The rich `revocation` heartbeat block
(`{consumerConnected, revokedCount, lastEventSeq, lastSyncAt}`, §2.6) + its Health-schema completeness
test. **Unblocks Loupe F11's revoke-panel live state.** Independently green; F11 (the revoke UI) is a
**Loupe-lane** fire that lands after this.

---

## 7. Alternatives considered (recorded; the choice is Andrew's steer)

1. **Loupe writes the `token-revocation` bucket directly** (the arch-review row's first sketch). Rejected
   by Andrew's steer — it makes Loupe a *writer* outside op-submit (its first non-op write), and the
   revocation isn't auditable in the ledger. Superseded.
2. **A Refractor lens projects `events.gateway.>` → the `token-revocation` NATS-KV target**, Gateway
   reads it (Checker unchanged, no new Gateway consumer — the *least* new machinery). Rejected by
   Andrew's steer, and rightly: it **couples the security kill-switch's liveness to the projection
   engine** (a paused/degraded Refractor stalls revocation propagation). A security primitive should not
   share fate with the read-model projector. Superseded.
3. **Model revocation as a Core-KV identity aspect** (`vtx.identity.<id>.revoked`) via a mutating op.
   Rejected — it's graph state where operational state belongs, needs a projector *and* a retraction path
   on unrevoke (a dropped key doesn't auto-retract → **over-grant on the security plane**), and buys
   nothing the event doesn't. The event-only shape is strictly simpler and safer.

**Chosen (Andrew's steer): the Gateway-owned materializer from an outboxed event** — §2. Loupe stays
write-free, the revocation is auditable (durable event + intent-ledger), and the kill-switch is
independent of every other component's health.

---

## 8. Adversarial pass (discharged this fire)

Ran the design-skill's fail-closed + transport + retraction checks against this shape (the pre-build gate
I'm obligated to discharge, not defer):

- **Default direction of the authz boundary** — the per-request check denies on read error (fail-closed)
  and the *absence* of a grant denies revoke (Contract #6 §6.8); the **startup** path now denies-by-
  refusing-to-start instead of the old default-open silent-disable. All three boundaries fail closed. ✓
- **Retraction transport** — `UnrevokeActor` emits an explicit `actorUnrevoked` event → an explicit
  `KVDelete` (not an upsert-only reprojection that would leave a dropped key live). Because the bucket is
  a **single-key-per-actor** set, revoke/unrevoke is a single-row overwrite/delete — no composite-key
  row-set shrink, so no silent over-grant. ✓ (This is the exact failure mode §7-alt-3 would have risked.)
- **Write guard per target** — the Gateway is the *sole* writer of `token-revocation` (materialized from
  a single durable-ordered event stream, folded latest-per-actor); there is no concurrent writer to race,
  so no CAS/seq guard is needed on the bucket. The **durable consumer's monotonic delivery** is the
  ordering guarantee (replay is idempotent: put/del by actor key). Named precisely — not "inherits a
  guard." ✓
- **Assumed producer/consumer** — the producer (the ops) and the consumer (the Gateway materializer) ship
  in the *same* Fire 1; neither is dead scaffolding. The Chronicler history lens is explicitly *not*
  depended on (§3). ✓
- **Parallel in-flight designs** — grepped the `📐`/`🏗️` design docs + nearby intake rows for the same
  seam. `heartbeat-false-green-aggregation` touches the Gateway heartbeat (§2.6 discharges its Gateway
  half — flagged so the two don't double-implement); `lens-target-reserved-bucket-guard` should add
  `token-revocation` to its denylist (noted, cross-referenced). No conflicting mechanism. ✓
