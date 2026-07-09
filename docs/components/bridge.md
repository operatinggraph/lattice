# Bridge

**Component reference** | Audience: implementers + architects

> Data shapes are frozen in `docs/contracts/10-orchestration-surfaces.md` — §10.5/§10.6 (Loom's
> `externalTask` step + the `payload.externalRef` correlation key), §10.4 (the `@at` schedule
> convention the async poll/timeout lane uses), and §10.3 (the pinned FR58-determinism invariant).
> The `external.<adapter>` event envelope spec lives **here** (it is
> package + bridge data, not a contract amendment — the `external` domain is ordinary, §10.5). Update
> this page in the same commit as the code; drift between page and code is a documentation bug.

---

## Overview

The bridge is the platform's **generic, trusted-infra egress** — the one component that makes
**outbound** calls to external systems (payment, background check, …). It is a **durable consumer on
`events.external.>`** that, for each event, dispatches to a **named adapter**, calls it **idempotently**,
and posts a **result op** back to `core-operations`. It owns the **adapter registry**.

The bridge is **vertex-type-agnostic.** It treats `instanceKey`/`externalRef` as an **opaque
correlation token** and the `replyOp` as package-data DDL; it never parses or assumes the claim vertex's
type. The lease demo's `service.<x>.instance` is **one package's** modeling choice — not a bridge
constraint. A package could anchor its external-call claim on any vertex type its DDL defines.

The bridge exists because external I/O does not belong in either orchestration engine. Weaver
*detects* divergence; Loom *executes* deterministic procedures. Embedding outbound calls in Weaver's
convergence lane would smear I/O into detection and force Weaver to re-implement the durable-claim /
idempotency / recovery machinery Loom already has. The bridge isolates **all** external I/O in one
purpose-built, trusted component, and keeps Loom and Weaver pure and event-driven — consistent with
Lattice's CDC / event-sourced spine.

External calls become **event-driven and symmetric to userTasks**: Loom dispatches to an async
completer and parks; the completer is a **human** (userTask) or the **bridge** (`externalTask`). See
`docs/components/loom.md` → "External steps (`externalTask`)".

The bridge is an **internal service actor** at root-equivalent capability (a third primordial identity
alongside Loom and Weaver — see "Service actor" below). It **submits operations through the Processor**
(the result op); it never writes Core KV directly.

---

## The `external.<adapter>` event envelope

Emitted by the **`externalTask`'s `instanceOp` DDL** via the Processor's transactional event outbox —
**not** by Loom directly (Loom stays pure; the instance commits *before* the event is publishable, so
NFR-S11 "visible claim before the call" holds structurally). The `external` domain is **ordinary** (the
open `<domain>.<eventName>` model — no Processor allowlist, no Contract #3 amendment); the event-type is
declared as package DDL.

```
class:   external.<adapter>                 # e.g. external.backgroundCheck, external.stripe
payload: {
  "instanceKey":    "<handle>",             # opaque correlation token (a bare handle in the reference vertical); Loom minted it write-ahead and the instanceOp's DDL forms the claim-vertex key vtx.<type>.<handle> from it (package-chosen type; demo: vtx.service.<id>). The bridge never parses it.
  "adapter":        "<name>",               # which registered adapter to dispatch to
  "params":         { … },                  # adapter call inputs (resolved from the Loom step's row/subject templates)
  "replyOp":        "<ResolveOp>",          # the result-op type the bridge posts on a TERMINAL (Resolved) outcome
  "dispatchOp":     "<PendingOp>",          # (async only) the op that records the create-only pending marker on a Pending outcome; empty ⇒ the task is sync-only
  "idempotencyKey": "<instanceKey>",        # = instanceKey; handed to the adapter so IT dedups the real external action
  "externalRef":    "<instanceKey>"         # = instanceKey; echoed back on the result op so Loom's correlationKeys resolves
}
```

`idempotencyKey` and `externalRef` are both the **instance key** — one claim vertex = one external
call. The instance key is the single binding token across the whole loop: the claim, the adapter's
dedup key, the result holder, and Loom's park handle. The bridge treats it as an **opaque token** — it
never parses the type segment or assumes what the claim vertex is.

---

## The flow (end-to-end)

```
Weaver detects a stale/absent gap  →  triggerLoom the execution pattern
  Loom pattern: … → externalTask:
    Loom submits the step's instanceOp (instanceKey minted write-ahead)
      └─ instanceOp DDL: (a) CREATE the claim vertex — package-chosen type, Core KV
                             (the lease demo uses vtx.service.<id>, class service.<x>.instance)
                         (b) EMIT external.<adapter>{…} via the op's transactional outbox
    Loom PARKS on token.<instanceKey>                         (the externalRef correlation key, §10.6)
  ┌─────────────────────────────────────────────────────────────────────────────────────────────┐
  │ BRIDGE: durable consumer on events.external.>  (instanceKey/externalRef are opaque tokens)    │
  │   → (optional) skip redundant call on redelivery: GET vtx.op.<deterministic-reqId> tracker    │
  │   → dispatch to the named adapter, idempotencyKey = instanceKey                               │
  │   → post replyOp to core-operations:                                                          │
  │        requestId = deterministic(instanceKey)            (redelivery collapses on Contract #4) │
  │        payload   = { externalRef: instanceKey, <outcome fields> }                             │
  └─────────────────────────────────────────────────────────────────────────────────────────────┘
  replyOp DDL commits → records the outcome as ASPECT(s) on the claim vertex (D5; root data stays
                        minimal) → emits orchestration.externalTaskCompleted{externalRef}
    → Loom correlationKeys: payload.externalRef → live token.<instanceKey> GET → instance → ADVANCE
    → the actorAggregate convergence lens reprojects (the claim's outcome aspect changed) → gap clears
```

A later Loom step may branch on the outcome (this is a genuine wait-for-completion, not
fire-and-forget).

---

## Idempotency & FR58 (the hard invariant)

External calls must be **at-most-once-effective** under at-least-once event redelivery and crash/retry.
Three mechanisms — the first two are the load-bearing guarantees (pinned by Contract #10 §10.3); the
third is an optimization:

1. **Deterministic result-op requestId (pinned invariant).** The bridge's result-op `requestId` **MUST**
   be `deterministic(idempotencyKey = instanceKey)`. A redelivered `external.*` event therefore produces
   the **same** result-op requestId, which collapses on the Contract #4 `vtx.op.<requestId>` tracker
   (`internal/processor/step2_dedup.go`) → **exactly one** result mutation. Without it a redelivery
   double-writes the result. This is the event-plane analog of the §10.4 deterministic-requestId rule
   for the fired-timer→op path. **Generic** — the op tracker is the same key shape for every op.
2. **Adapter `idempotencyKey` dedup.** The adapter is called with `idempotencyKey = instanceKey` and
   **must** dedup the real external action on it (a contract requirement of every adapter). So even a
   redelivered event that re-reaches the adapter produces **no** duplicate external action. Correctness
   holds via (1) + (2) **without** the bridge reading any vertex.
3. **(Optional) skip the redundant call on redelivery — generic, no typed read.** Before dispatching,
   the bridge MAY GET the **Contract #4 op tracker for its own deterministic result-op `requestId`**
   (`vtx.op.<deterministic-reqId>`): present (and not tombstoned) → the result already landed → ack
   without re-calling. This uses the **generic** op tracker (same key shape for all ops), **not** a read
   of the typed claim vertex — so the bridge stays vertex-type-agnostic. Purely an optimization to avoid
   a redundant adapter round-trip that (2) would dedup anyway.

**The claim vertex IS the visible claim — structurally, before the bridge acts.** FR58 / NFR-S11 ("a
visible claim recorded before the external call") is satisfied structurally: the claim vertex is
created by the `instanceOp` **before** the `external.*` event is even publishable (the event rides that
op's post-commit outbox), so the claim is **always** visible before the bridge consumes the event — the
bridge needs **no read** to guarantee it. The vertex
unifies the claim + the result holder + the audit record into **one auditable business vertex** in Core
KV; its **type is package-chosen** (the lease demo's `service.<x>.instance`), and its **outcome lives in
aspect(s)** per **D5**, not fat root `data`.

The FR58 crash/retry idempotency proof runs on a **bridge-only harness**: `FakeStripe.FailUntil` /
`SideEffects == 1` under event redelivery + mid-flight-failure recovery.

---

## Async adapters — submit-then-resolve-later

Not every vendor answers inline. The **Adapter SPI is two-method** — `Execute` (dispatch a call) and
`Poll` (probe a previously-submitted call) — and an adapter's `Dispatch` reply carries a
**Disposition**:

- **Resolved** — the call finished; the `Dispatch` carries a terminal `Result{Status, Detail}`
  (`completed | failed`). The bridge posts the **`replyOp`** and Loom's token advances. This is the
  synchronous path above; a synchronous adapter (`AdapterFunc`) only ever returns `Resolved`, so its
  `Poll` is unreachable.
- **Pending** — the vendor accepted the call and it will resolve **later** (a poll or a webhook); the
  `Dispatch` carries an opaque vendor `Ref`, no `Result`. The bridge records a **create-only pending
  marker** (the vendor ref) via the envelope's **`dispatchOp`** and posts **no terminal outcome** —
  the token stays parked.

`dispatchOp` (the pending-marker op) is distinct from `replyOp` (the terminal-outcome op). An
envelope with an empty `dispatchOp` is **sync-only**: a `Pending` from its adapter is a config error
(handled like a missing adapter — Ack + a Health issue), never a hot Nak loop.

### The poll/timeout schedule lane (Contract #10 §10.4)

When a call goes `Pending`, the bridge arms **two `@at` schedules** keyed on the bare claim handle (a
dot-free NanoID) on the `core-schedules` stream:

```
schedule.bridge.poll.<handle>      @ nextPollAt  → fires schedule.bridge.poll.fired.<handle>
schedule.bridge.timeout.<handle>   @ deadline    → fires schedule.bridge.timeout.fired.<handle>
```

A single **fixed durable** (`bridge-schedule`) consumes `schedule.bridge.*.fired.>` — its ack floor
is the missed-while-down recovery, exactly like Weaver's lane-3. The fired handler:

- **poll fired** → `Poll` the vendor. `Resolved` → post the `replyOp` (same shape as the sync
  resolve). Still `Pending` → **re-arm** `schedule.bridge.poll.<handle>` at `now + PollInterval` (a
  self-rescheduling `@at` chain). Transient probe error → `NakWithDelay`.
- **timeout fired** → post a **terminal `failed` reply** — the deadline backstop for a call that
  never resolved.

The routing (`vendorRef` / `adapter` / `replyOp`) rides each **schedule payload**, so the fired
handler stays **type-agnostic** — it never synthesizes or reads a typed claim-vertex key.

**Resolution stays idempotent.** Every resolution (sync resolve, poll resolve, timeout) posts the
`replyOp` under the **same** `deriveReplyRequestID(handle)`, so an at-least-once redelivery collapses
on the Contract #4 op-tracker. A **read-before-act** guard (probe the reply op-tracker) suppresses a
poll/timeout firing once any resolution has landed, and the result op's **create-only `.outcome`** is
the first-writer-wins backstop for a timeout racing a late success. Production adapters ship
synchronous; the async SPI is exercised end-to-end by the reference `FakeAsyncCheck` adapter.

### The Augur dispatch path

The Augur — Weaver's AI reasoning tier (the L3 evaluator) — reaches its model **through the bridge as
an ordinary adapter**. The `augur` adapter's `Result.Detail` carries an **`AugurProposal`** (the
model's structured output: the escalation verdict + confidence), which Weaver picks up off the result
op. The bridge treats it like any other adapter payload — opaque, copied verbatim into the result op,
no AI knowledge in the bridge itself. See `augur-design.md` + `augur-dispatch-pickup-design.md`.

---

## Service actor (a third primordial identity)

The bridge posts its result ops under a **bootstrap-provisioned service actor** —
`identity.system.bridge`, operator-equivalent, established purely by a `holdsRole → operator` edge,
exactly like Loom and Weaver (`docs/components/service-actors.md`). Consequences:

- It is a **third** primordial service identity → it **moves the `verify-kernel` assertion count** and
  the bootstrap-file identity set. The bootstrap-file `version` bumps (a hard mismatch → `make down &&
  make up`, no in-place migration — see service-actors.md), and **both** kernel-verify enumerations
  (`scripts/verify-kernel.go`, `internal/bootstrap/verify.go`) update **in lockstep**.
- `protected: true` (a package uninstall must never tombstone a kernel service actor).
- When lane enforcement lands, its capability projection must include the `system` lane (same carry as
  Loom/Weaver).

---

## What this component owns

| Path | Role |
|------|------|
| `internal/bridge/` | Engine: durable `events.external.>` consumer, adapter registry, the two-method **Adapter SPI** (`Execute` / `Poll`; `Resolved` / `Pending` dispositions; `Request.RawParams` carries the event's params verbatim for adapters with structured inputs), idempotent dispatch (deterministic result-op requestId + adapter `idempotencyKey` dedup; optional generic op-tracker skip-on-redelivery), the **poll/timeout schedule lane** (`bridge-schedule` fixed durable, `schedule.go`/`actuator.go`), and result-op submission to `ops.<lane>`. Also the reference `Fake*` adapters — sync `FakeBackgroundCheck` / `FakeStripe`, async `FakeAsyncCheck`, `FakeAugur` (`AugurProposal` structured output), and `FakeDocGen` (the reference legal-document vendor: renders the executed-lease artifact from the event's resolved doc fields and **writes its bytes to the `core-objects` store** — the bridge's one byte-plane side-effect; the bytes stay inert until an `AttachObject` op anchors them); real vendor integrations are Phase 3 |
| `cmd/bridge/` | Binary entry point (extractable; shares only `substrate/*`); pins `ActorKey = bootstrap.BridgeIdentityKey` and registers the reference adapters (`FakeDocGen` constructed with the conn + the `core-objects` bucket + the `OBJECTS_MAX_UPLOAD_BYTES` write cap) |

**Engine vs package:** the consumer, registry, dispatch, recovery, and result-op submission are
**engine code**. Which adapters exist, the `external.<adapter>` event-type DDL, the `instanceOp` /
`replyOp` DDLs, and the Loom patterns that emit them are **package data**.

---

## In / Out contracts

| Direction | Contract | Notes |
|-----------|----------|-------|
| In | `events.external.>` durable consumer | one fixed durable; the envelope above; domain is ordinary (no allowlist) |
| In (optional) | the Contract #4 op tracker `vtx.op.<deterministic-reqId>` | generic skip-on-redelivery probe (same key shape for all ops) — **not** a read of the typed claim vertex; the bridge stays type-agnostic |
| In (async) | `schedule.bridge.*.fired.>` fixed durable (`bridge-schedule`) | the fired poll/timeout lane (§10.4); routing rides the schedule payload so the handler needs no typed read |
| Out | `replyOp` result op via `core-operations` | `requestId = deterministic(instanceKey)`; `payload.externalRef = instanceKey` + outcome fields; its DDL records the outcome as **aspect(s)** on the claim vertex (D5) **and emits `orchestration.externalTaskCompleted{externalRef}`** (the uniform Loom completion signal, §10.6); submitted under the bridge service actor |
| Out (async) | `dispatchOp` pending-marker op via `core-operations` | on a `Pending` outcome: records the create-only vendor-`ref` marker, **no** terminal outcome (token stays parked) |
| Out (async) | `@at` schedules `schedule.bridge.{poll,timeout}.<handle>` on `core-schedules` | arm the poll (self-rescheduling `@at` chain) + timeout (deadline backstop) for a pending call |
| Out | adapter calls | the actual external I/O — the only component that makes them; `idempotencyKey = instanceKey` |
| Out | `ObjectPut` → `core-objects` (docGen adapter) | the off-graph blob plane: the reference doc-gen vendor streams the rendered artifact's bytes under a deterministic, application-derived store name (a re-render overwrites; the ObjectPut is the adapter's `idempotencyKey`-deduped side-effect). Bytes are **inert until an `AttachObject` op anchors them** — the bridge account is one of the four sanctioned object-plane writers (`$O.core-objects.>`, pinned by `internal/natsperm` `TestObjectStoreWriteAccess`) and this grant carries no actor authority |
| Out | Health (Contract #5) | heartbeat at `health.bridge.<instance>`; an unregistered adapter / unparseable envelope surfaces an issue, never a silent skip |

---

## State & crash-safety

The bridge holds **no durable bucket of its own** — a deliberate simplification. Its durable state is:

- the **claim vertex** (Core KV, written by the `instanceOp`; outcome recorded as aspect(s) by the
  result op) — the claim + outcome + audit, **type package-chosen**;
- the **`events.external.>` consumer ack floor** — missed-while-down recovery resumes from it;
- the **Contract #4 op tracker** for the deterministic result-op requestId — the dedup record.

Crash points and their recovery:

| Crash point | Recovery |
|-------------|----------|
| After event consumed, before adapter call | redelivery from the ack floor → the optional op-tracker probe finds no result → call proceeds (or, without the probe, it proceeds and the adapter dedups) |
| After adapter call, before result op | redelivery → the adapter dedups on `idempotencyKey` → the re-call is a no-op → the result op posts (deterministic requestId) |
| After result op published, before ack | redelivery → the deterministic result-op requestId collapses on the Contract #4 tracker → exactly one mutation |
| Result op rejected at the Processor | off-stream; Loom's `externalTask` deadline backstop (§10.6) probes the instanceOp tracker and re-arms / fails — the same path as a systemOp |

---

## Failure modes

| Mode | Behavior |
|------|----------|
| Unregistered adapter named in an event | `errConfig` posture — Ack + a Health issue (redelivery can never fix a name the registry lacks); never a silent skip |
| Adapter call fails transiently | re-attempt on the same `idempotencyKey` (the adapter dedups); bounded-cadence redelivery, never a hot loop |
| Adapter panics | panic-contained — the framework, not the adapter, is the safety boundary; the event is re-drivable, the dispatch goroutine survives |
| Never-completing external call | Loom's `externalTask` per-step deadline (§10.6) is the backstop on the *waiting* side — the bridge itself does not wedge Loom |
| Poison event | head-of-line blocks the `external` consumer only (domain-scoped blast radius) |

---

## Principles that apply

- **P2** — the Processor is the sole Core KV writer / event producer; the bridge is a client (the result
  op goes through the ledger; the `external.*` event is emitted by the `instanceOp`'s outbox, not a
  bridge publish).
- **P1** — the claim vertex is business state (Core KV; outcome in aspect(s) per D5); the bridge keeps no
  operational bucket.
- **Decision #10 / "everything is a package"** — the bridge engine is generic; adapters + event-types +
  patterns are package data. A new external integration is **just a new adapter registration + a Loom
  pattern** — no new component.
- **Module boundary** — `bridge` imports only `substrate/*`; it talks to the Processor / Loom via NATS,
  never Go calls.

---

## Implementation status

**Built (Phase 2).** The bridge is fully implemented and CI-gated: the engine (`internal/bridge/` +
`cmd/bridge/`), the adapter registry and contract types (`Adapter` / `Registry` / `Request` / `Result`),
the reference `Fake*` adapters, the deterministic result-op `requestId`
(`deriveReplyRequestID(instanceKey)`), and the FR58 crash/retry idempotency proof on a bridge-only
harness. The bridge holds no durable bucket of its own. The bootstrap-provisioned
`identity.system.bridge` service actor is seeded primordially (see `docs/components/service-actors.md`).
External I/O is reached as `triggerLoom` of an `externalTask` executed by this component — `internal/weaver`
holds no adapter and makes no external call. End-to-end Loom → bridge convergence on a real `externalTask`
is exercised by the `lease-signing` reference vertical.

The **async result path** (Phase 3) is also built and CI-gated: the two-method Adapter SPI
(`Execute` / `Poll`, `Resolved` / `Pending`), the `dispatchOp` pending marker, and the poll/timeout
schedule lane (`bridge-schedule` durable) — proven end-to-end by `FakeAsyncCheck` (`pending_e2e_test.go`
/ `schedule_e2e_test.go`). The **Augur dispatch path** (the `augur` adapter returning an
`AugurProposal`) rides the same SPI (`augur_proposal.go`, `fake_augur.go`). The **docGen path**
(`docgen_adapter.go`) adds the byte-plane egress: the reference legal-document vendor renders the
executed-lease artifact and `ObjectPut`s it to `core-objects`; its completed `Detail` is the JSON
document-pointer set the `RecordLeaseDocOutcome` replyOp records on the claim's `.outcome` aspect,
from which the lease-signing convergence lens + Weaver's `AttachObject` playbook anchor the artifact.

**Deferred (Phase 3+).**

- Real external adapters (Stripe, background-check) — the platform ships the substrate-only `Fake*`
  adapters; production integrations are a Phase 3 concern.
- Generic egress as a first-class platform feature beyond the reference integrations — the bridge is
  built generic, but its broad reuse outside the reference vertical is a Phase 3 concern.
- The `system` lane on the bridge's capability projection, once lane enforcement lands (the same carry
  as Loom / Weaver).
