# Contract #10 Amendment Requests (Story 8.1 — Loom)

These amendments to the **FROZEN** Contract #10 (`docs/contracts/10-orchestration-surfaces.md`)
were adjudicated in a structural architecture session during Story 8.1 (Loom walking skeleton),
2026-06-06. They change frozen shapes (§10.3, §10.5, §10.6) and add a new section (§10.9), so they
require ratification by the contract owner (Andrew) + a Contract #10 revision-history entry — they are
**not** in-flight edits. Story 8.1's working tree must be reconciled to the ratified text before its
code review resumes (deltas listed at the end).

**Origin:** Story 8.1 implementation surfaced four gaps the frozen contract did not resolve:
(1) the `pendingToken → instance` correlation index was specced **in-memory**, which silently assumes a
**single** Loom engine instance; (2) the per-domain consumer set (D2) has **no declared binding**
source — the pattern shape declares none and operation DDL emits its event-classes at runtime, not
statically; (3) the contract defines the `StartLoomPattern` op + its auth but never how a **committed**
trigger reaches the engine; (4) `<Flow>Complete` had no pinned producer or subject. The session resolved
all four coherently around two principles already in the architecture: **engine ships zero domain
knowledge**, and **P2 — the Processor/outbox is the sole Core-KV writer / event producer**.

---

## Request 1: §10.3 — `loom-state` keys + durable co-located correlation index (atomic batch)

**Location:** §10.3 "`loom-state` — per-instance Loom cursor" (key/value + the "Rebuildable (D3)" bullet).

**Current text:**
- key `<instanceId>`; value `{ instanceId, patternRef, subjectKey, cursor, pendingToken, status, retryCount }`.
- "The §10.6 correlation `pendingToken → instance` is an **in-memory index Loom rebuilds from the
  persisted `pendingToken` fields on startup** — no secondary KV index."

**Requested text:** `loom-state` holds **two disjoint-prefixed key shapes** (the same one-bucket /
disjoint-prefix pattern capability-kv §6.1 uses for `cap.ephemeral.*`):

```
key:  instance.<instanceId>     value: { instanceId, patternRef, subjectKey, cursor, pendingToken, status, retryCount }
key:  token.<pendingToken>      value: { instanceId }          # thin reverse pointer
```

- `status ∈ {running, complete, failed}`.
- The `pendingToken → instance` correlation is a **durable co-located reverse index** (the `token.`
  pointer), resolved by a **direct GET** on completion — **not** an in-memory index. This is
  **multi-instance-safe**: any engine replica resolves any token via the bucket.
- Each step transition is a **single `substrate.AtomicBatch` on `loom-state`** (all ops target one
  bucket — `internal/substrate/batch.go`): update `instance.<id>` (`cursor`, `pendingToken`), write the
  new `token.<newToken> → instanceId`, delete the prior `token.<oldToken>`. All-or-nothing — the same
  construct the Processor uses for the mutation-batch + tracker at commit step 8.
- "No secondary KV index" is **reinterpreted**: it forbids a **separate index bucket** (dual-write
  atomicity / drift); a co-located disjoint-prefix index in the *same* bucket, written in the same
  atomic batch, is sanctioned and stronger.
- **Provisioning (binding):** `loom-state` **must** be provisioned with **`AllowAtomicPublish: true`** on
  its underlying stream — the same flag `core-kv` gets. Today `enableAtomicPublish` is gated to
  `CoreKVBucket` only (`internal/bootstrap/primordial.go:99`); extend it to `loom-state` (alongside the
  existing bucket-create + the `verify-kernel` assertion). Without it, `Conn.AtomicBatch` on `loom-state`
  is rejected.

**Rationale:** The in-memory index silently assumes a single engine instance (a second replica never
learns of an instance created on another replica after its startup scan → a completion landing on the
wrong replica fails to correlate). AtomicBatch makes the durable index a single atomic fact, preserves
write-ahead, and **retires** the startup rebuild and crash-safety invariant 3 (see Request 3). Keeps
Loom partitionable/scalable later (Andrew: "domains here are similar to lanes in core-operations").

---

## Request 2: §10.5 — `completionDomains` (the declared step→domain binding)

**Location:** §10.5 "Loom pattern definition" — the pattern JSON shape.

**Current text:** `{ patternId, subjectType, steps:[ { kind, operation, guard? } ] }` — no domain binding.

**Requested text:** add an optional **`completionDomains: ["<domain>", …]`** to the pattern:

```
{ patternId, subjectType, completionDomains?: [<domain>...], steps:[ { kind, operation, guard? } ] }
```

- `completionDomains` = the set of `events.<domain>.>` the engine reconciles a **durable per-domain
  consumer** for (D2). A **domain** is the **first segment of an event class** — the `<domain>` in
  `events.<domain>.>`, a single **dot-free** token (the outbox subjects events as `events.<sanitizedClass>`,
  e.g. class `tenant.created` → domain `tenant`; this keeps `loom-<domain>` a valid durable name).
- **Default `[subjectType]`** when omitted (covers the common same-domain flow). A flow whose steps
  complete in a domain other than the subject's **must list it explicitly**; the §10.6 per-step
  completion **timeout** is the not-silent backstop for an omitted/mis-declared domain (FR29
  never-silently-drop).
- The engine reads `completionDomains` — it does not *know* domains. Per-step granularity is
  unnecessary: correlation is domain-independent (Request 3), so the **set** of domains is sufficient.

**Rationale:** D2 mandates per-domain consumers, but no declared binding existed — the frozen pattern
shape carried none and operation DDL emits event-classes at *runtime* (Starlark, processor step 7), not
statically. A pattern-level declaration is the lane-analogous **explicit** partition, on the package
data the engine interprets, keeping the engine domain-agnostic.

---

## Request 3: §10.6 — correlation via the durable token index; crash-safety restated

**Location:** §10.6 "Step completion & instance correlation" (the correlation table + "Crash-safety
invariants").

**Current text:** correlation via in-memory index; invariants (1) write-ahead `pendingToken`,
(2) guardless-step-token-only, (3) **completion watch suspended until the in-memory index finishes
rebuilding**.

**Requested text:**
- **Correlation is a durable `token.<token>` GET** (Request 1), **domain-independent**: a consumed
  `core-events` message on *any* subscribed domain whose body `requestId`/`taskId` matches a live
  `token.` pointer is the **committed** terminal → advance via the atomic batch. The per-domain
  consumer only decides *which events Loom sees* (the partition), never *which instance* — that's the
  pointer. **Idempotency** (at-least-once redelivery): the `token.` pointer's **presence is the guard**
  — pointer gone (step already advanced, pointer deleted in the batch) → drop/ack, no re-advance.
- **failed/rejected** terminal is **off-stream** (a rejected op writes no tracker/event) — from the
  `ops.<lane>` submit reply or a bounded **per-step timeout**; the timeout also backstops a mis-declared
  `completionDomains` (Request 2) → alert, never a silent wedge.
- **Crash-safety invariants:** (1) write-ahead — the atomic batch persists the `token.` pointer +
  instance update **before** the side effect (op submit); (2) guardless-step-token-only — **retained**;
  (3) watch-suspended-until-rebuild — **REMOVED** (no in-memory index to rebuild; a redelivered
  completion resolves against the durable pointer regardless of engine age).

**Rationale:** Follows from Requests 1–2. The durable pointer removes the rebuild barrier entirely; the
domain-independence is what lets `completionDomains` be a pure scaling/isolation lever.

---

## Request 4: §10.9 (NEW) — Pattern trigger & lifecycle via `loom`-domain ops

**Location:** new section §10.9 (the contract defines the `StartLoomPattern` op + auth in §10.5/§10.8
but never how a *committed* trigger reaches the engine, nor how `<Flow>Complete` is produced).

**Principle constraint (binding):** a Loom instance is **operational state** (loom.md state table) — it
lives **only in `loom-state`** (P1) and gets **no Core-KV business vertex**. Its lifecycle is announced
on the **event plane** (`core-events`), **not** projected as Core-KV business state. These ops emit
their `loom.*` events the **ordinary way**: at commit the faithful `EventList` is persisted as the
**outbox aspect `vtx.op.<requestId>.events`** — alongside the universal `vtx.op.<requestId>` tracker, in
the same step-8 atomic batch — and the outbox CDC consumer publishes from it
(`internal/processor/outbox/consumer.go`). So each writes the **standard tracker + outbox-events aspect**;
the only distinguishing property is that it creates **no business-domain vertex** — the instance's sole
durable home is the `loom-state instance.<instanceId>` cursor (Request 1).

**Requested text:** Three lifecycle ops (shipped by `orchestration-base`; engine stays generic), each →
outbox → `events.loom.*` (**P2: never a direct publish**):

| Op | Posted by | Business vertex | Emits (body: `instanceId, patternRef, subjectKey, requestId`) |
|----|-----------|-----------------|------|
| `StartLoomPattern{patternRef, subjectKey}` | **caller** (Weaver `scope:any` / client / fixture) | none | `loom.patternStarted` |
| `CompletePattern{instanceId}` | **Loom** (`identity:loom`) | none | `loom.patternCompleted` |
| `FailPattern{instanceId, reason?}` | **Loom** (`identity:loom`) | none | `loom.patternFailed` |

(Each also writes the universal `vtx.op.<requestId>` tracker + the `…events` outbox aspect — that is how
the event is emitted; none writes a business vertex.)

- **`instanceId` = the `StartLoomPattern` `requestId`** (already a NanoID) — no minting, and redelivery
  dedup is automatic (Loom's `loom-state` cursor keyed on it → already present → skip).
- Loom runs a **fixed durable consumer on `events.loom.patternStarted`** (always-on, **independent of
  `completionDomains`**). On the event: validate `patternRef` against the loaded pattern registry, create
  the `loom-state instance.<instanceId>` cursor, submit step 0.
- The engine's **internal** completion/failure is a **`loom-state` status transition** (operational);
  the `CompletePattern`/`FailPattern` op is the *outward announcement* (loop closure + nesting), the
  terminal Actuator op of an exhausted/failed pattern.
- **Idempotency needs no new machinery:** `StartLoomPattern`'s Contract #4 tracker dedups a duplicate
  trigger op at the Processor; Loom dedups at-least-once event redelivery on the `instanceId`.
- **`loom` is a first-class domain:** Loom *consumes* `patternStarted` (trigger) and *emits*
  `patternCompleted`/`patternFailed`. A Loom completion is therefore itself a consumable completion
  event — so a Phase-3 **nested** pattern (a step that runs a sub-flow and waits) simply lists `loom` in
  its `completionDomains` and correlates on the sub-instance's token, with **no new machinery**.
- **Queryability** ("which flows are running") is served by **Loom's control plane** — analogous to
  Refractor's (`internal/refractor/control/service.go`), reading `loom-state` — **not** Core KV. It is its
  own (future) control-plane story; Weaver gets the analogous one (Story 9.4 control-API). A Refractor lens
  over the `loom.*` event stream remains an option for a durable read model if one is later wanted.

**Rationale:** Closes the trigger gap and the `events.loom` write-only-inbox smell (start in /
complete out, symmetric on the event plane) **without** persisting operational instance state in Core KV
(P1 honored). The op ledger (`core-operations`) + the durable `loom.*` event stream are the audit trail.
Cost: three lifecycle ops in `orchestration-base` — no new Core-KV vertex type, no per-transition Core-KV
business write (only the standard tracker + outbox aspect every op already produces).

**No special Processor capability needed.** Event emission already rides the outbox aspect
(`vtx.op.<requestId>.events`) written in the commit batch, so a lifecycle op is an ordinary op that emits
events and writes no business vertex. (Sanity-check only: an op whose `result.Mutations` is empty but
whose `result.Events` is non-empty still commits the tracker + `…events` aspect and publishes — confirm
no upstream guard rejects an empty *business*-mutation set.)

---

## Request 5: §10.3 + §10.6 — Loom command outbox (durable op relay) + timeout/probe failed terminal

**Status:** PENDING ratification. Requests 1–4 were ratified 2026-06-06 (now in the frozen contract +
revision-history; working tree reconciled). **Request 5 is new** — raised 2026-06-06 from the Story 8.1
adversarial code review (finding **F2**), after Requests 1–4 had already been reconciled.

**Origin:** `internal/loom/engine.go` `submitStep` commits the `loom-state` AtomicBatch (cursor +
write-ahead token) and **then** submits the op to `core-operations` — a **dual write**. A transport
failure *after* the batch commits wedges the instance permanently: on redelivery the consumed token is
already deleted, so the completion correlates to nothing, the next-step op was never submitted, and
nothing rescues it. The synchronous submit-reply meant to catch rejection (a) **blocks the completion-
consumer goroutine** and (b) **forces a raw `nats.go` handle into `internal/loom`** (AC#8 violation,
finding F1). This is the same shape the Processor already solved with its transactional **event** outbox;
Loom needs the symmetric **command** outbox. The fix also subsumes finding **F5** (the lifecycle
`CompletePattern`/`FailPattern` announce, which has the identical dual-write).

**Location:** §10.3 (`loom-state` keys) + §10.6 (correlation-table failed terminal + crash-safety
invariant 1).

### 5a — §10.3: command-outbox record + deadline key (two new disjoint prefixes)

`loom-state` gains an `outbox.` and a `deadline.` key shape alongside `instance.`/`token.`:

```
key: instance.<instanceId>   value: { instanceId, patternRef, subjectKey, cursor, pendingToken, status, retryCount }
key: token.<pendingToken>    value: { instanceId }                                          # committed-path reverse pointer
key: outbox.<token>          value: { requestId, operation, payload, target, lane, actor }  # command-outbox record (new)
key: deadline.<instanceId>   value: { }   with a per-key TTL = the step deadline            # timeout backstop (new, see 5c)
```

The per-step transition stays **one `substrate.AtomicBatch`**, now: update `instance.<id>`, write
`token.<newToken>`, delete `token.<oldToken>`, **write `outbox.<newToken>`**, and **reset
`deadline.<instanceId>`** (a PUT with a fresh per-step TTL — same key name, so it overwrites/re-arms
rather than write-then-delete). All-or-nothing — the submission *intent* and the step's deadline are part
of the same atomic fact as the cursor advance, so there is **no dual write**.

#### Why per-instance (not per-step), and its lifecycle

The `deadline.` key is keyed on **`instanceId`** even though a deadline is conceptually per-step. That is
correct **because the interpreter is linear (§10.5): exactly one step is pending per instance at any
time**, so a single key always denotes "the current step's clock." (A future DAG/parallel interpreter
would need a per-step key such as `deadline.<instanceId>.<cursor>` — out of scope for Phase 8.)

| Event | Action on `deadline.<instanceId>` | Where |
|-------|-----------------------------------|-------|
| **Created** | written with TTL = step-0 deadline | the trigger handler's create-instance + submit-step-0 atomic batch |
| **Reset** (per advance) | PUT (overwrite) with a fresh TTL = the next step's deadline → re-arms the clock, cancelling the prior step's | the advance atomic batch |
| **Deleted** | removed (no pending step remains) | the `complete` / `fail` terminal atomic batch |
| **Expires** | NATS auto-deletes → MaxAge delete-marker → the step-deadline-exceeded handler runs (below) | — (the only path that is *not* an explicit Loom write) |

So a step that completes in time never lets its deadline fire (advance re-arms it / terminal deletes it);
only a step that overruns its TTL trips the handler. The key's value is thin (e.g. `{ setAt }` for
observability) — it is **not** load-bearing, because the handler reconstructs everything from
`instance.<instanceId>` (the expiry marker carries only the subject, hence the instanceId).

### 5b — the relay (durable count `2 + N` → `3 + N`)

A durable consumer on the `loom-state` backing stream filtered to `outbox.>` (mirroring
`internal/processor/outbox/consumer.go`): on each `outbox.<token>` PUT → **fire-and-forget publish** the
op to `core-operations` → on JetStream publish-ack, **delete `outbox.<token>`** + ack. Re-publish is
idempotent (deterministic `requestId` + the Contract #4 `vtx.op.<requestId>` tracker collapse the
duplicate). A crash between batch and publish → the relay re-publishes on resume. **The relay uses only a
publish — no request-reply** — which is what lets `internal/loom` drop its raw `nats.go`/`jetstream`
handle (closes F1/AC#8). The §10.9 lifecycle ops route through the same outbox (closes F5).

Durable count becomes **`3 + N`**: pattern source (1) + trigger (1) + **command-outbox relay (1)** +
per-domain completion consumers (N).

### 5c — §10.6: failed/rejected terminal = bounded deadline + read-before-act probe (synchronous reply REMOVED)

The synchronous `ops.<lane>` submit-reply terminal is **removed** from §10.6 / AC#4. The three step
outcomes become orthogonal:

- **committed** — a `core-events` body `requestId` matches a live `token.` pointer → advance (unchanged).
- **crash / transient** — **not a terminal**: the outbox relay re-publishes and the durable consumers
  resume from their ack floor. No special-casing. *(The outbox owns crash-recovery; the timeout does not.)*
- **rejected / failed / unseen** — **off-stream**, via a **crash-safe bounded per-step deadline**. A
  rejected op writes no tracker and emits no event (Processor denies before commit step 8), so it is
  invisible on `core-events`; the deadline is the FR29 never-silently-drop backstop. On deadline for the
  instance's `pendingToken == T` (status `running`), a **read-before-act probe** does `GET vtx.op.<T>`:
  - tracker **present** → the op committed; the completion event was missed (mis-declared
    `completionDomains` / lost) → treat as the **committed** terminal → **advance + alert**.
  - tracker **absent**, `outbox.<T>` still present → not yet relayed → **extend the deadline**.
  - tracker **absent**, no `outbox.<T>` → **rejected → `status=failed` / `retryCount`** per policy.

  The deadline is set **≫ expected op latency** (weaver-state precedent); a late commit after a false-fail
  finds the pointer gone → dropped (a bounded, alerted divergence). This is the symmetric analog of
  Weaver's read-before-act recovery (§10.3 `weaver-claims`: "checks Core-KV for an already-landed resolve
  before re-executing; the Core-KV resolve is the authoritative truth").

**Deadline mechanism — preferred (pending capability verification): per-key TTL + CDC MaxAge marker.**
The `deadline.<instanceId>` key carries a NATS **per-key TTL** (ADR-43 message TTL / ADR-48 KV
extension). On step completion the advance batch deletes it; if it instead **expires**, the loom-state
backing-stream CDC observes a delete-marker with **reason = MaxAge** on subject `…deadline.<instanceId>`
→ the instanceId is known **from the subject** → `GET instance.<instanceId>` → run the probe above.
Purely event-driven, crash-safe, no polling, no scheduler coupling, **no dual write** — the same TTL-
backstop pattern `weaver-state` already uses.
- **The TTL'd key MUST be keyed on `instanceId`, not on the token.** A TTL on `token.<token>` would lose
  the `{instanceId}` value on expiry (a delete-marker carries the subject but **not** the old value),
  breaking the reverse mapping and forcing a scan. Keying on `instanceId` keeps it recoverable from the
  marker subject.
- **Sanctioned fallback:** if the project's NATS server/client does not expose per-key TTL **and** a
  CDC-observable MaxAge marker, an **active reconciler sweep** (periodic `instance.*` scan for overdue
  `running` instances, deadline read from `instance.<id>`) is the contract-equivalent fallback. The
  contract mandates the **semantics** (crash-safe bounded deadline → read-before-act probe); TTL-marker
  is preferred, reconciler is the fallback. *(Scheduler-driven via §10.4 is explicitly rejected — it
  reintroduces a dual write/publish.)*

**Step-deadline-exceeded handler — what the actor MUST do.** When the deadline fires (the MaxAge marker
on `deadline.<instanceId>`, or the reconciler-fallback detecting an overdue instance), the handler for
instance `I`:

1. **GET `instance.<I>`.** If absent or `status != running` → **ack/no-op** (already terminal, or a stale
   marker). This is the idempotency + multi-replica guard — re-reading current state, never acting on the
   marker alone.
2. Let `T = instance.pendingToken`. **Read-before-act probe: GET the Contract #4 op tracker `vtx.op.<T>`**
   (a Core-KV read — Loom reads, never writes Core-KV; symmetric to Weaver's recovery read):
   - **tracker PRESENT** → the op committed; its completion event was missed (mis-declared
     `completionDomains` / lost). → **advance exactly as the committed terminal would** (the advance
     atomic batch: bump `cursor`, delete `token.<T>`, write the next step's `token`/`outbox`/`deadline`,
     or run `complete` if exhausted) **and emit an alert** ("completion recovered via deadline probe —
     check `completionDomains` for pattern `P`"). The flow stays live; never-silently-drop is honoured.
   - **tracker ABSENT, `outbox.<T>` PRESENT** → the relay has not delivered yet (backed up / mid-retry).
     → **re-arm**: PUT `deadline.<I>` with a fresh TTL; do **not** fail. (Optionally nudge the relay.)
   - **tracker ABSENT, `outbox.<T>` ABSENT** → the op was published but did not commit → **rejected**. →
     per `retryCount` policy either re-submit the step (write a fresh `outbox.<T>` + re-arm
     `deadline.<I>`, `retryCount++`, up to a max) **or** transition `instance.<I>` to `status=failed` in
     an atomic batch that also deletes `token.<T>` + `deadline.<I>`, then submit `FailPattern{instanceId,
     reason}` (event-only, via the outbox). **Alert.** *(Walking-skeleton default: fail on first
     rejection; retry policy is a later refinement.)*
3. Every branch re-reads current `instance` state and is CAS-on-`running`, so a redelivered marker or a
   second replica finds the work already done → no-op.

**Crash-safety invariant 1 (restated).** Write-ahead is the atomic batch, which now *includes the
`outbox.<token>` record*; the side effect (the op reaching `core-operations`) is the relay's decoupled,
idempotent, re-tryable publish. The batch and the side effect are no longer a dual write — invariant 1
is satisfied by construction rather than by ordering discipline.

**Rationale:** Retires F2 (dual-write wedge), the C2 blocking-callback, and F5 (lost lifecycle announce)
in one move, and lets `internal/loom` shed its raw `nats.go` handle (F1). It introduces **no new
pattern** — it reuses the Processor transactional outbox and the `weaver-state` TTL backstop, both
already in the frozen contract.

---

## Working-tree reconciliation (Story 8.1 deltas, after ratification)

The dev implementation must be brought to the ratified text before review resumes:

1. **`internal/loom/state.go`** — replace bare-`<instanceId>` keys with `instance.<id>` + `token.<token>`
   thin pointer; make each transition a single `substrate.AtomicBatch`.
2. **`internal/loom/engine.go`** — delete the in-memory `pendingIndex`, `rebuildIndex`, and the
   suspend-watch gate; correlate by `GET token.<requestId>`; drop the "resume still-pending on rebuild"
   path (the durable pointer + Contract #4 tracker cover redelivery).
3. **`internal/loom/pattern.go`** — rename the invented `eventDomains` → sanctioned `completionDomains`
   (default `[subjectType]`); keep `Domains()`.
4. **Trigger** — replace the direct-Go `StartInstance` call with the fixed `events.loom.patternStarted`
   consumer; add the **lifecycle** `StartLoomPattern`/`CompletePattern`/`FailPattern` ops in
   `orchestration-base` (no `vtx.loomInstance` vertex — instance stays in `loom-state`); the dev's
   configurable `CompletionOperation` becomes `CompletePattern`. `instanceId` = `StartLoomPattern` `requestId`.
5. **e2e** — the fixture may still drive via a real `StartLoomPattern` submission (now producing the
   trigger event); assert the `loom.patternStarted/Completed` lifecycle and mid-run-restart exactly-once
   against the durable token pointer (no in-memory index).
6. **Story 8.1 ACs** — update AC #2/#4/#6 to the durable-index + `completionDomains` + §10.9 trigger model
   (the current AC text reflects the pre-amendment frozen design).

> Items 1–6 above (Requests 1–4) are **DONE** — ratified and reconciled in the working tree.

### Request 5 reconciliation (PENDING ratification — to do after Andrew ratifies)

R5-1. `internal/loom/state.go` — add the `outbox.<token>` + `deadline.<instanceId>` key shapes; the
  transition batch writes/deletes them alongside `instance.`/`token.` (one `substrate.AtomicBatch`).
R5-2. `internal/loom/actuator.go` → a fire-and-forget **relay**: a durable consumer on the loom-state
  backing stream `outbox.>` that publishes the op to `core-operations` and deletes the record on
  publish-ack. **Remove the synchronous reply path.** Add a `substrate` publish-op helper so
  `internal/loom` drops the raw `nats.go`/`jetstream` import; **tighten `boundary_test.go` to forbid
  `nats-io/*`** (closes F1 / AC#8).
R5-3. `internal/loom/engine.go` — remove the reply-driven `fail`; write the `deadline.<instanceId>` TTL
  key in the transition batch; add the loom-state CDC MaxAge-marker watcher (or the reconciler fallback)
  → read-before-act probe (`GET vtx.op.<token>`) → advance / extend / fail.
R5-4. **Story 8.1 ACs/Tasks** — update AC#3/#4/#5/#6 and Tasks 4/5/8 to the command-outbox + timeout/probe
  model (drop the synchronous-reply terminal; add the `outbox.`/`deadline.` keys + the relay).
R5-5. `docs/components/loom.md` / `_bmad-output/planning-artifacts/lattice-architecture.md` — durable
  count `2+N → 3+N`; Actuator → command-outbox relay; failure-modes (deadline + probe); state table
  (+`outbox.`/`deadline.`).
R5-6. **Verify** NATS per-key TTL (ADR-48) + a CDC-observable MaxAge delete-marker **and** that a PUT
  (overwrite) **re-arms/resets** the per-key TTL (so per-advance re-arm cancels the prior step's clock).
  Do this **before** committing to the preferred mechanism. If overwrite does not reset TTL → use an
  explicit delete+put or a per-step `deadline.<instanceId>.<cursor>` key; if per-key TTL/MaxAge is absent
  entirely → the reconciler-sweep fallback. (The exceeded-handler procedure in §5c is identical under
  either mechanism — only the *trigger* differs.)
R5-7. **Frozen-contract edits (post-ratification):** apply 5a/5b/5c to §10.3 + §10.6 of
  `docs/contracts/10-orchestration-surfaces.md` and add a revision-history entry (same process used for
  Requests 1–4).

> Findings **F4** (trigger Nak-storm → use `NakWithDelay` + `MaxDeliver`/`Term`) and **F6** (per-domain
> consumer teardown on pattern removal) are **out of scope for Request 5** — they remain independent
> fix-forward items in the Story 8.1 Senior Review triage.

---

# Contract #10 Amendment Requests (Story 8.2 — userTask steps)

Story 8.2 (userTask step kind) surfaced three doc/code drifts in the **FROZEN** §10.5/§10.6, plus the
deeper finding that the whole event taxonomy was domain-less. Per CLAUDE.md "Frozen contracts," these
were **not** in-flight edits — they were raised here for Andrew's ratification + a revision-history entry.

**STATUS: RATIFIED 2026-06-07.** Requests 6–9 are ratified and **superseded/absorbed by the broader
event-domain model** (every event class is `<domain>.<eventName>`, enforced at commit step 7; see
Contract #3 §3.4 revision 2026-06-07 + Contract #10 §10.5/§10.6 revision 2026-06-07). The original
surgical R6/R7 ("raise a CAR note, do not rename the class") are now consequences of the model: the
class **is** renamed (`TaskCompleted` → `orchestration.taskCompleted`), so a userTask completes on the
**`orchestration`** domain (not the fictional `TaskCompleted` domain). The frozen contracts have been
amended; the working tree builds to the ratified shapes.

## Request 6: §10.5 — the example onboarding `completionDomains` is misleading

**Location:** §10.5 "Loom pattern definition," the `onboarding` example JSON.

**Current text:** the example pattern declares `"completionDomains": ["identity"]` for an all-`userTask`
onboarding flow.

**Problem:** a userTask completes via the **`TaskCompleted`** core-event (the commit-path auto-complete,
§10.6). `EventSubject("TaskCompleted")` → subject `events.TaskCompleted`
(`internal/processor/outbox/publisher.go`), whose **domain is `TaskCompleted`** (the first dot-free
segment, §10.5). A pattern declaring `completionDomains: ["identity"]` reconciles a `loom-identity`
consumer on `events.identity.*` and **never sees** `events.TaskCompleted` — the flow waits forever
(now that a userTask arms no bounded deadline backstop, AC#6, there is no probe to recover it). The
correct value for an all-userTask onboarding pattern is **`completionDomains: ["TaskCompleted"]`**.

**Requested text:** change the §10.5 example to `"completionDomains": ["TaskCompleted"]` and add a note:
*a userTask step completes on the `TaskCompleted` domain, regardless of the subject's type; a pattern
mixing userTask + systemOp steps lists every domain it completes on.*

## Request 7: §10.6 — the `TaskCompleted` correlation key is `taskKey` and rides `payload`, not a bare `taskId` in the "body"

**Location:** §10.6 the step-completion correlation table (userTask row) + "Completing a userTask."

**Current text:** "userTask … the **`taskId`** of the task it created … `TaskCompleted` core-event →
body carries `taskId`."

**Problem (two parts):**
1. The implemented `TaskCompleted` event carries **`taskKey`** = the full `vtx.task.<id>` key
   (`internal/processor/autocomplete.go`, `packages/orchestration-base/ddls.go` `transition_task`), not
   a bare `taskId`. Loom write-aheads `token.<vtx.task.<id>>` and correlates on the full `taskKey`.
2. The field is nested under the Event envelope's **`payload`** object, **not** a top-level `data`/`body`
   field. `BuildEventList` (`internal/processor/step7_events.go`) maps an op's `EventSpec.Data` →
   `Event.payload`, so the wire shape is `{ requestId, eventType:"TaskCompleted", payload:{ taskKey },
   … }`. The systemOp correlation reads the **top-level** `requestId`; the userTask correlation reads
   **`payload.taskKey`**. (The §10.6 prose "body carries `taskId`" reads as a top-level field — it is
   neither top-level nor named `taskId`.)

**Requested text:** the userTask row reads "the **`taskKey`** (`vtx.task.<id>`) of the task it created
… `TaskCompleted` core-event → **`payload.taskKey`** → live `token.<taskKey>` GET → instance." Note
that all event business fields ride the envelope `payload` (Contract #3 §3.4), so the two structural
correlation keys Loom tries are top-level `requestId` (systemOp) and `payload.taskKey` (userTask).

## Request 8: §10.6 crash-safety invariant 1 — the userTask write-ahead requires a caller-controlled task id

**Location:** §10.6 crash-safety invariant 1 (write-ahead) + the §10.6 userTask narrative.

**Problem:** invariant 1 requires the `token.<token>` pointer persisted **before** the side effect. For
a userTask the token is the task's `taskKey`, but `CreateTask` minted `task_id = nanoid.new()`
**internally**, so Loom could not know the `taskKey` ahead of commit and could not write-ahead the
pointer. The §10.6 narrative ("a task is closed by `taskId` … no new envelope field, no Contract #2
change") tacitly assumed the engine could not (and need not) control the id — which contradicts
invariant 1 for userTask.

**Resolution implemented (Story 8.2, Winston-adjudicated):** `CreateTask` gains an **optional**
caller-supplied `taskId` (`packages/orchestration-base/ddls.go`): present → used verbatim (validated as
a bare NanoID), absent → `nanoid.new()` (every existing admin/manual caller unaffected). Loom derives a
deterministic `taskId` from `(instanceId, cursor)` (a sibling of the systemOp `deriveRequestID`), passes
it to `CreateTask`, and write-aheads `token.<vtx.task.<taskId>>` in the transition batch. A crash-retry
re-submits the same `CreateTask` (same op `requestId`) and collapses on the Contract #4 tracker — no
duplicate task. The `task` DDL is **package data**, not a frozen `docs/contracts/*` contract, so this is
a backward-compatible package change; it is logged here only because it is the seam §10.6 invariant 1
implicitly requires.

**Requested text:** §10.6 invariant 1's userTask clause notes that the engine supplies the task id (so
the `taskKey` is known write-ahead), via `CreateTask`'s optional `taskId`. No Contract #2 envelope
change; the grant/auth path (§10.7) is unchanged.

## Request 9: §10.6 deadline+probe — now also covers the userTask creation path

**Location:** §10.6 step-deadline-exceeded handler (the deadline+probe) + the userTask narrative.

**Problem:** §10.6 specifies the deadline+probe for a **systemOp** step (a bounded machine action whose
committed event may be missed/rejected/lost). A userTask wait is unbounded (a human may take days), so
the original implementation armed NO deadline for a userTask and the deadline handler no-op'd any
`vtx.task.` token. But the userTask step is really **two** waits in sequence: a **bounded** wait for the
task to be CREATED (a machine action — `CreateTask` commits in milliseconds), then an **unbounded** wait
for the human to act on it. With no backstop on the first wait, a **rejected/lost `CreateTask`** (e.g.
the subject identity is dead/absent → `CreateTask`'s no-orphan validation rejects it, or a taskId
collision) parks `token.<taskKey>` **forever** with no recovery and no alert — the silent wedge §10.6
forbids.

**Resolution implemented (Story 8.2, Winston-adjudicated):** the userTask step now arms a **bounded
creation-deadline** (`CreateTaskTimeout`, sized ≫ any `CreateTask` commit latency, NOT a human-response
window). When it fires, a read-before-act probe runs the userTask analog of the §10.6 systemOp probe:
GET the task vertex `vtx.task.<taskId>` from Core KV — **present** → the task was created and the flow is
now in the legitimate **unbounded human wait** → **disarm** the deadline (cursor/token untouched) and stop
(the human may take days); **absent** → probe the `CreateTask` op's Contract #4 tracker / `outbox` record
exactly like the systemOp path (tracker present → committed-but-raced → re-arm; outbox present → relay
not yet delivered → re-arm; neither → `CreateTask` **rejected/lost** → `FailPattern` + Warn alert). Every
branch is CAS-on-`running`, mirroring the systemOp `onDeadline`. Loom only **READs** Core KV here (the
task-vertex GET, like the existing tracker GET) — it never writes Core KV, and the module boundary
(substrate-only) is unchanged.

**Requested text:** §10.6 notes the deadline+probe applies to the userTask **creation** path as well — a
bounded creation-deadline that **disarms once the task vertex exists** (after which the human wait is
unbounded), so a rejected/lost `CreateTask` fails the instance instead of wedging it (§10.6: "never a
silent wedge"). No envelope/contract shape change.

## Request 10: §10.3 — fifth `loom-state` key shape: the per-instance pattern definition pin

**Location:** §10.3 "`loom-state` — per-instance Loom cursor + co-located reverse index" (the
four-key-shape enumeration and the "four disjoint-prefixed key shapes" framing).

**Current text:** "`loom-state` holds **four disjoint-prefixed key shapes** in the one bucket",
enumerating `instance.<instanceId>` / `token.<pendingToken>` / `outbox.<token>` /
`deadline.<instanceId>`.

**Problem:** the engine resolved a running instance's pattern definition **live** from the pattern
source on every transition, and `instance.<instanceId>` carries only a `patternRef` — no copy. A
pattern update mid-flight (steps reordered/inserted, a guard changed) therefore silently mis-indexed
the durable `cursor` against the NEW step list: the cursor is a step *index*, and the contract gives
it no stable definition to index into. (Surfaced by the Story 8.3 review, finding F2.)

**Resolution implemented (post-8.3 fix-forward, Winston-adjudicated):** definitions **bind at
instance start**. The trigger consumer writes a full copy of the pattern — as loaded at trigger
time — to `instance.<instanceId>.pattern` in the **same `AtomicBatch`** that creates
`instance.<instanceId>` (both CreateOnly); every subsequent step resolution (advance, completion,
deadline recovery) reads the pinned copy, never the live source. The pin is deleted in the same
terminal batch that flips `status` to `complete`/`failed`, so listing `instance.*.pattern` yields
exactly the live-instance set — which feeds the per-domain consumer reconcile as the second leg of
a union (current-snapshot domains ∪ pinned domains of live instances), letting an in-flight
instance survive its pattern being removed/updated-away and letting the domain consumer drain when
its last live instance completes. Pattern updates affect NEW instances only; disaster recovery
(total `loom-state` loss → fresh `StartLoomPattern`, the shipped 8.3 narrow semantics) re-binds to
the CURRENT definition. Event-embedded pins were analyzed and rejected (`core-events` `MaxAge=7d`
vs unbounded userTask waits — a pin riding events would evaporate mid-instance).

**Requested text:** §10.3 enumerates **five** key shapes, adding
`key: instance.<instanceId>.pattern   value: <the full pattern definition as loaded at trigger time>`,
written atomically with the instance create and deleted in the terminal batch. The
"disjoint-prefixed" framing is qualified: the pin deliberately shares the `instance.` prefix as a
sub-key of its instance (instanceIds are NanoIDs, so the `.pattern` suffix is unambiguous); the
other four prefixes remain disjoint. A note records that definitions bind at instance start and
that the per-domain consumer set (D2/§10.9) is derived from the union of current definitions and
live-instance pins.
