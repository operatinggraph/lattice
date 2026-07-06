# Contract #10 (Loom) — pattern definition, step completion, lifecycle ops

> **A part of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)** (the index +
> shared revision history). Section numbers **§10.5 / §10.6 / §10.9** are the canonical Contract-#10
> identifiers, unchanged by this shard. The `externalTask` step kind (§10.5 / §10.6) is Loom's surface
> for the external-I/O **bridge**, whose own adapter/envelope contract is
> [`docs/components/bridge.md`](../components/bridge.md).

## 10.5 Loom pattern definition (package data)

A `meta.loomPattern` meta-vertex (loaded via CDC like a Lens def). A pattern declares a single
**`subjectType`** — the vertex the instance runs for; the trigger op supplies the subject id.
Guards and step operations are relative to the subject.

**Starting an instance** is the op **`StartLoomPattern{ patternRef, subjectKey }`** (`subjectKey`
must be a vertex of `subjectType`). Authorization is per-pattern via **`authContext.target =
vtx.meta.loomPattern.<patternId>`** + capability scope (Weaver = `scope: any`; external/per-pattern =
`scope: specific` / task grant) — full contract in **§10.8 "`triggerLoom` authorization"**. This is
the pattern-*start* auth (distinct from the per-step auth of §10.6/§10.7).

```
{
  "patternId":   "onboarding",
  "subjectType": "identity",
  "completionDomains": ["orchestration"],
  "steps": [
    { "kind": "userTask", "operation": "SetName",
      "guard": { "absent": "subject.profile.data.name" } },
    { "kind": "userTask", "operation": "SetPhone",
      "guard": { "absent": "subject.profile.data.phone" } },
    { "kind": "userTask", "operation": "SetAddress" }
  ]
}
```

**`completionDomains?: ["<domain>", …]`** (optional) — the set of `events.<domain>.>` the engine
reconciles a **durable per-domain consumer** for (D2). A **domain** is the **first segment of an event
class** — the `<domain>` in `events.<domain>.>`. Every event class is `<domain>.<eventName>`
(Contract #3 §3.4, validated at commit step 7), so this model is **true codebase-wide**, not
illustrative: e.g. class `identity.created` → domain `identity`, class `orchestration.taskCompleted` →
domain `orchestration`; `loom-<domain>` is always a valid durable name. **Defaults to `[subjectType]`**
when omitted (covers the common same-domain flow). A flow whose steps complete in a domain other than
the subject's **must list it explicitly**; the §10.6 per-step completion **deadline** is the not-silent
backstop for an omitted/mis-declared domain (FR29 never-silently-drop). The engine reads
`completionDomains` — it does not *know* domains; per-step granularity is unnecessary because
correlation is domain-independent (§10.6), so the **set** of domains is sufficient.

**A userTask completes on the `orchestration` domain.** A userTask step completes via the
`orchestration.taskCompleted` event (the §10.6 commit-path auto-complete), regardless of the subject's
type — so an all-userTask onboarding pattern over `identity` subjects declares
`completionDomains: ["orchestration"]` (NOT `["identity"]`, which would reconcile a `loom-identity`
consumer that never sees the completion). A pattern mixing userTask + systemOp steps lists every domain
it completes on.

**Step shape:** `{ kind, operation, guard? }` for `userTask`/`systemOp` — completion is implicit
(§10.6), no per-step event. The `externalTask` kind (below) is **two-op-shaped** and carries its own
fields `{ kind, adapter, params, replyOp, instanceOp }`.
- `kind` ∈ `userTask` (engine creates a task with links `assignedTo` → the subject,
  `forOperation` → the step's op, `scopedTo` → **the subject** — a Loom `userTask` scopes its grant
  to the instance subject; the frozen step shape carries no separate target field; UI renders from
  the op's self-describing DDL via the `forOperation` link) | `systemOp` (engine submits the op
  directly) | **`externalTask`** (engine submits the `instanceOp`, then parks awaiting the external
  result — see below).
- **Linear only** — no branches/loops/fan-out. A compound *path* is a Weaver signal. The
  `externalTask`'s two ops (submit-instanceOp → park) are **one logical step**, not a branch/fan-out.

**`externalTask` (Amended 2026-06-18 — 13.1, External I/O Bridge).** A step that dispatches an idempotent
external call and **waits for its result** — symmetric to a `userTask` (dispatch to an async completer,
then park; the completer is a human for userTask, the bridge for externalTask). Shape:

```
{ "kind": "externalTask", "adapter": "<name>", "params": { ... }, "replyOp": "<ResolveOp>", "instanceOp": "<CreateInstanceOp>" }
```

- The engine submits the **`instanceOp`**, whose DDL (a) creates the **claim vertex** (Core KV business
  state — the FR58 "visible claim before the call", §10.3; its **type is package-chosen** — the lease
  demo uses `service.<x>.instance`, but the bridge is **type-agnostic**) and (b) emits the
  `external.<adapter>` event via **that op's transactional outbox**. The `external` domain is
  **ordinary** — no Contract #3 change (the open `<domain>.<eventName>` model, no Processor allowlist);
  the `instanceOp` DDL declares + emits the event-type as package data. The bridge
  (`docs/components/bridge.md`) consumes `events.external.>`, calls the adapter idempotently, and posts
  `replyOp` back.
- The engine **then PARKS** on `token.<instanceKey>` (§10.6) — the instance key it **mints write-ahead**
  and passes to `instanceOp` as a caller-supplied id (exactly as it supplies `CreateTask`'s
  deterministic `taskId` write-ahead, §10.6 invariant 1).
- `adapter` is the external adapter name; `params` are **subject** templates — each value is either a
  literal or a `subject.<aspect>.data.<field>` / `subject.data.<field>` path (the §10.5 guard-path
  grammar), **resolved against the subject's current Core-KV state when the instanceOp runs** (a
  null/absent resolution is a data error — surface, do not dispatch). Resolution is a **write-path read**:
  the submitter (Loom) declares the subject's aspect keys in the instanceOp's `contextHint.reads`, and the
  instanceOp resolves the templates from that JIT-hydrated state as it emits the `external.<adapter>`
  event — so an adapter receives the real subject fields it needs (a vendor's legal-name / DOB / address)
  without any reader touching a lens read-model. The **`row.<column>`** half of §10.8 templating is the **Weaver actuator's**
  resolution (`subject`/`assignee`/`target` selection from a violation row) and is **not** reachable on
  the Loom write path: by the time the instanceOp runs the violation row is gone, the write path must not
  read the lagging `weaver-targets` read-model, and `triggerLoom{pattern, subject}` carries no row. (A
  field on a *linked* vertex is reached by the instanceOp DDL's own §2.5 `kv.Read`, not a `params`
  template.) `replyOp` is the result-op type the bridge posts back (carrying
  `payload.externalRef = instanceKey`, §10.6) — **its DDL records the external outcome as aspect(s) on
  the claim vertex** (**D5**: business data lives in aspects; the vertex root `data` stays minimal — at
  most a justified lifecycle scalar such as `status`), **not** as fat root `data`; `instanceOp` is the
  DDL op that mints the claim vertex + emits the event.
- **Loom stays pure:** the event rides the **`instanceOp`'s transactional outbox** (the op Loom submits
  through the command-outbox relay), **not** a Loom-held NATS handle — the `internal/loom`
  substrate-only boundary is unchanged.
- **Completion is symmetric to a userTask** (amended 2026-06-18). Besides recording the outcome
  aspect(s), the `replyOp` DDL **emits `orchestration.externalTaskCompleted` carrying
  `payload.externalRef = instanceKey`** — the uniform orchestration-domain completion signal Loom
  correlates on, the analog of `orchestration.taskCompleted{taskKey}` for a userTask (§10.6). An
  externalTask pattern therefore declares **`completionDomains: ["orchestration"]`** (exactly like an
  all-userTask pattern), and Loom's existing `loom-orchestration` consumer advances it. The event is
  **emitted by the purpose-built `replyOp`** rather than platform-injected the way `taskCompleted` is for
  a userTask's *oblivious* bound op — the emission mechanism differs to match the completer (a deliberate
  result op vs an ordinary business op), the outcome (an orchestration-domain completion correlated by a
  token) is identical. The wait for that event is **unbounded** once the `instanceOp` commits (§10.6),
  exactly as a userTask's human wait is unbounded once its task vertex exists.

**Async resolution (Amended 2026-06-19 — Phase 3, async external-reply).** The bridge's adapter call
MAY resolve **asynchronously**: a real vendor returns a *pending reference* on submit and the true
result lands later (webhook or status-poll, minutes–days). This rides the **already-unbounded wait**
above with no change to the completion model — the bridge posts `replyOp` (hence
`externalTaskCompleted`) whenever resolution arrives. While pending, the bridge records an interim
**pending marker** — a package-chosen aspect on the claim vertex written via a package `dispatch` op,
analogous to the `.outcome` aspect (D5) — and drives **re-poll + a give-up timeout** through its §10.4
schedule lane (`schedule.bridge.>`). A timeout posts a terminal `replyOp` with a `failed` status, so a
never-answered call **converges** rather than parking forever. The §10.6 step `deadline.<instanceId>`
TTL — the off-stream backstop — is sized to the external SLA (per-adapter) and the bridge's give-up
timeout fires **before** it: the normal path is a clean bridge-posted outcome, and the Loom deadline
catches only a genuinely dead bridge. A synchronous adapter (today's fakes) is unchanged — it resolves
inline and posts `replyOp` immediately, writing no pending marker.

**Guards — pure predicate over the subject's current state.** Absent guard = step always runs.

- **Paths are explicit** (consistent with the 1.5.9 explicit-aspect-navigation principle):
  `subject.<aspect>.data.<field>` (aspect) or `subject.data.<field>` (root). Guards read **only
  the subject + its aspects** — no link-walking (a guard that needs related state is a Weaver
  signal). At step-entry the engine JIT-hydrates the subject (root + referenced aspects) and
  resolves the path with the same `resolveProperty`/aspect-navigation the Refractor executor uses.
- **Declarative grammar (default):** atoms `{absent: <path>}`, `{present: <path>}`,
  `{equals: {path, value}}`, composable with `{allOf|anyOf|not: [...]}` (still one boolean — NOT
  branching). **Pinned semantics (binding, removes ambiguity):** `absent` = the path resolves to
  **null, missing, a soft-deleted aspect, OR (for strings) empty-after-trim**; `present` = not
  absent. An empty-string-after-trim is **absent**; `"0"`/`false`/`0` are **present**.
- **Starlark escape hatch (reserved):** for a predicate the grammar can't express, a guard may be
  `{ "reads": ["<aspect>", ...], "starlark": "def guard(subject): return ..." }` — evaluated by
  the **same verified-pure sandbox** the Processor uses (`Load` nil; no I/O / env / NATS;
  deterministic — confirmed in `starlark_runner.go`). `reads` is the read-hint (which subject
  aspects to hydrate), answering the input-parameter question; the function gets `subject` exactly
  as a script gets `state`, returns a bool. The shared pure-evaluator extraction lands **only when
  the first Starlark guard is authored** (deferred until needed; declarative-only ships without it).
- Either way a guard is **pure declarative data or a pure function** → the instance cursor is
  rebuildable by replaying guards (no side effects, deterministic).

Patterns + step→operation bindings + guards are package data; the engine is a generic interpreter.
**How a step's completion is detected and correlated to its instance → §10.6.**

---


## 10.6 Step completion & instance correlation

A step is correlated to its instance by a **unique token Loom already knows or the completion
event already carries** — concurrent-safe (multiple instances per subject, or many tasks of one
op-type per actor, are unambiguous), with **no topological guessing**.

**Correlation is a durable `token.<token>` GET** (§10.3), **domain-independent**: a consumed
`core-events` message on *any* subscribed domain whose body `requestId`/`taskId` matches a live
`token.` pointer is the **committed** terminal → advance via the atomic batch. The per-domain consumer
only decides *which events Loom sees* (the partition, §10.5 `completionDomains`), never *which instance*
— that is the pointer. **Idempotency** (at-least-once redelivery): the `token.` pointer's **presence is
the guard** — pointer gone (step already advanced, pointer deleted in the batch) → drop/ack, no
re-advance.

| Step kind | Pending token (in `loom-state`) | Completion signal Loom consumes |
|-----------|----------------------------------|----------------------------------|
| **userTask** | the **`taskKey`** (`vtx.task.<id>`) of the task it created | `orchestration.taskCompleted` core-event → **`payload.taskKey`** → live `token.<taskKey>` GET → instance |
| **systemOp** | the **`requestId`** of the op it submitted | a committed business event on a subscribed domain whose top-level `requestId` matches a live `token.<requestId>` → advance via the atomic batch. **failed/rejected** is **off-stream** (a rejected op writes no tracker/event) — learned via the **per-step deadline + a read-before-act probe** (below), never the submit reply → `status=failed` / `retryCount` per policy; the deadline also backstops a mis-declared `completionDomains` (§10.5) → alert, never a silent wedge |
| **externalTask** *(13.1, 2026-06-18; deadline+completion amended 2026-06-18)* | the **`instanceKey`** — an **opaque bare-NanoID handle** Loom mints write-ahead and passes to `instanceOp` as a caller-supplied id (the `instanceOp` DDL prepends its package-chosen type to form the `vtx.<type>.<handle>` claim-vertex key; the engine stays type-agnostic) | **`orchestration.externalTaskCompleted`** core-event → **`payload.externalRef`** → live `token.<instanceKey>` GET → instance — **symmetric to userTask's `orchestration.taskCompleted` → `payload.taskKey`** (emitted by the `replyOp` DDL; see §10.5). The deadline backstops the **`instanceOp` submission only**: once the `instanceOp` commits it **disarms**, and the wait for the bridge's `replyOp` is **unbounded** (like the post-creation human wait) — it **never advances the cursor**. A rejected/lost `instanceOp` → `FailPattern` (the **creation-deadline + instanceOp-tracker probe**, below) |

All event business fields ride the Event envelope's **`payload`** object (Contract #3 §3.4), so Loom's
**three** structural correlation keys are **top-level `requestId`** (systemOp), **`payload.taskKey`**
(userTask), and **`payload.externalRef`** (externalTask — *Amended 2026-06-18, 13.1*). Loom stays
domain-ignorant — it tries each field against the durable token store (`token.<requestId>`,
`token.<taskKey>`, `token.<externalRef>`) and **at most one live pointer resolves** — the one for the
current pending step. Disjointness is **by field + by the single live pointer**, not by the key's type
segment: `externalRef` is the **opaque bare-NanoID handle** Loom minted (the `instanceOp` DDL forms the
`vtx.<type>.<handle>` claim-vertex key *from* it; the handle itself carries no type), so it needs no
fixed shape. The `externalTask`'s write-ahead handle is that **instance key Loom mints** — it does not
own the bridge's later result-op `requestId`, so it parks on a handle it controls and the bridge echoes
it back as `payload.externalRef`.

### systemOp terminals — committed on-stream, failed/rejected off-stream (deadline + probe)

A submitted systemOp has three orthogonal outcomes; separating them is what removes the wedge:

- **committed** — a `core-events` body `requestId` matches a live `token.` pointer → advance. (on-stream)
- **crash / transient** — **not a terminal**: the command-outbox relay re-publishes and the durable
  consumers resume from their ack floor. The outbox owns crash-recovery; the deadline does not.
- **rejected / failed / unseen** — **off-stream** (a rejected op writes no tracker and emits no event,
  Processor denies before commit step 8), learned via the **per-step `deadline.<instanceId>` TTL**
  (§10.3). The synchronous `ops.<lane>` submit-reply is **not** used — it blocks the consumer and forces
  a raw NATS handle into the engine.

**Step-deadline-exceeded handler.** When `deadline.<instanceId>` expires (the loom-state CDC observes
the `KeyValuePurge`/MaxAge marker; or the reconciler fallback detects an overdue instance), the handler
for instance `I`:

1. **GET `instance.<I>`.** Absent or `status != running` → **ack/no-op** (already terminal, or a stale
   marker). Re-reading current state — never acting on the marker alone — is the idempotency +
   multi-replica guard.
2. Let `T = instance.pendingToken`. **Read-before-act probe: GET the Contract #4 op tracker `vtx.op.<T>`**
   (a Core-KV *read* — Loom reads, never writes Core KV; symmetric to the bridge's read-before-act
   recovery on the service-instance vertex, §10.3):
   - **tracker present** → the op committed; its completion event was missed (mis-declared
     `completionDomains` / lost) → **advance** exactly as the committed terminal would, **and alert**
     ("completion recovered via deadline probe — check `completionDomains`"). Flow stays live.
   - **tracker absent, `outbox.<T>` present** → the relay has not delivered yet → **re-arm**
     `deadline.<I>` (fresh TTL); do **not** fail.
   - **tracker absent, `outbox.<T>` absent** → published but did not commit → **rejected** → per
     `retryCount` policy re-submit (fresh `outbox.<T>` + re-arm) or `status=failed` (atomic batch also
     deletes `token.<T>` + `deadline.<I>`) → submit `FailPattern` (§10.9). **Alert.**
3. Every branch re-reads `instance` and is CAS-on-`running`, so a redelivered marker / second replica
   finds the work done → no-op.

The deadline is set **≫ expected op latency** (the `weaver-state` lease precedent); a late commit after a
false-fail finds the pointer gone → dropped (a bounded, alerted divergence, not a silent one).

### userTask creation path — bounded creation-deadline + task-vertex probe

A userTask step is **two waits in sequence**: a **bounded** wait for the task to be *created* (a machine
action — `CreateTask` commits in milliseconds), then an **unbounded** wait for the human to act on it.
The deadline+probe above covers the *systemOp* step; the userTask **creation** wait gets the analogous
backstop so a rejected/lost `CreateTask` (e.g. the subject identity is dead → `CreateTask`'s no-orphan
validation rejects it, or a taskId collision) fails the instance instead of parking `token.<taskKey>`
forever (the silent wedge §10.6 forbids).

- A userTask step arms a **bounded creation-deadline** (`CreateTaskTimeout`, sized ≫ any `CreateTask`
  commit latency — **not** a human-response window).
- When it fires, a read-before-act probe GETs the task vertex **`vtx.task.<taskId>`** from Core KV (a
  Loom *read*, like the systemOp tracker GET):
  - **present** → the task was created; the flow is now in the legitimate **unbounded human wait** →
    **disarm** the deadline (cursor/token untouched) and stop. The human may take days — there is no
    further runtime timeout.
  - **absent** → probe the `CreateTask` op's Contract #4 tracker / `outbox` record exactly like the
    systemOp path (tracker present → committed-but-raced → re-arm; outbox present → relay not yet
    delivered → re-arm; neither → `CreateTask` **rejected/lost** → `FailPattern` + alert).
- Every branch is CAS-on-`running`, mirroring the systemOp handler. Loom only **reads** Core KV here;
  the module boundary (substrate-only) is unchanged.

**Honest nuance:** after the creation-deadline disarms (the task vertex exists), there is **no runtime
timeout** on the human wait — so a *mis-declared userTask `completionDomains`* (one that omits the
`orchestration` domain) is caught by a **load-time warn** when the pattern is loaded, not by a runtime
backstop. The warn is loud; the pattern is not rejected (a future userTask completion domain could
differ).

### externalTask creation path — bounded creation-deadline + instanceOp-tracker probe (amended 2026-06-18)

An externalTask step is **two waits in sequence** — exactly like a userTask, with the bridge in the
human's role: a **bounded** wait for the `instanceOp` to *commit* (the machine action that creates the
claim vertex + emits the `external.<adapter>` event), then an **unbounded** wait for the bridge to post
the `replyOp`. It is therefore handled like the **userTask** creation path, **not** like a systemOp: the
deadline backstops the *submission*, never the external round-trip, and it **must not advance the
cursor**. (This corrects the original "exactly like a systemOp" framing, under which a committed-but-not-
yet-replied `instanceOp` would wrongly advance the flow before the external result landed.)

- An externalTask step arms a **bounded creation-deadline** (the `CreateTaskTimeout` machine-action
  bound, sized ≫ `instanceOp` commit latency — **not** an external-call window).
- When it fires, a read-before-act probe GETs the **`instanceOp`'s Contract #4 tracker
  `vtx.op.<opRequestId>`** (the `opRequestId` Loom derived for the `instanceOp` — a Loom *read*, like the
  systemOp/userTask probes; Loom cannot read the claim vertex itself, whose type is package-chosen, so it
  probes the op tracker it owns):
  - **tracker present** → the `instanceOp` committed: the claim vertex exists and the `external.<adapter>`
    event was emitted, so the bridge will (eventually, at-least-once + idempotent) reply → **disarm** the
    deadline (cursor/token untouched) and stop. The bridge wait is now **unbounded** — no further runtime
    timeout (the bridge's durability is the guarantee; a dead bridge surfaces on the **bridge's own**
    Contract #5 Health, not a per-instance Loom timeout). **The cursor advances only on
    `orchestration.externalTaskCompleted` — never on this probe.**
  - **tracker absent, `outbox.<opRequestId>` present** → the relay has not delivered the `instanceOp` yet
    → **re-arm**; do not fail.
  - **tracker absent, `outbox.<opRequestId>` absent** → the `instanceOp` was **rejected/lost** (the
    external call will never happen) → `status=failed` (the atomic batch also deletes `token.<instanceKey>`
    + `deadline.<I>`) → submit `FailPattern` (§10.9). **Alert.** (FR29 — the submission is never a silent
    wedge.)
- Every branch is CAS-on-`running`, mirroring the systemOp/userTask handlers. Loom only **reads** Core KV
  here; the substrate-only boundary is unchanged.

**Honest nuance (same as userTask):** after the creation-deadline disarms, there is **no runtime timeout**
on the bridge wait — a *mis-declared externalTask `completionDomains`* (one that omits `orchestration`) is
caught by a **load-time warn** (the analog of the userTask warn), not a runtime backstop; a permanently-
dead bridge is closed out-of-band via `FailPattern` (the `CancelTask` analog). This deliberate, observable
async-wait is what both async-completer step kinds (userTask, externalTask) accept — distinct from the
**systemOp** deadline, which *does* advance on a tracker-present probe because for a systemOp the op's own
commit **is** the completion.

### Completing a userTask — by `taskKey`, via `orchestration.taskCompleted` (RESOLVED)

A task is closed by **`taskKey`** (`vtx.task.<id>`; never by inferring actor+op-type — a manager may
hold many open tasks of one op-type for different targets). Completion emits
`orchestration.taskCompleted` carrying **`payload.taskKey`**; Loom correlates `payload.taskKey →
instance` via a live `token.<taskKey>` GET. No new envelope field, no Contract #2 change — the op
already carries `authContext.task` for §10.7 auth.

- **Primary path — auto-complete on the authorizing op's commit.** A task exists to authorize +
  track exactly one op (`forOperation`) on one target (`scopedTo`); performing that op **is**
  fulfilling the task. So when an op authorized via `authContext.task = T` commits successfully, the
  **commit path injects T's completion** (`status → complete` + `orchestration.taskCompleted{taskKey:
  T}`) into the **same atomic batch** — platform-injected, like provenance, in the same code path that
  already matched the grant at step-3. Atomic, no "did-the-op-but-task-still-open" wedge, no per-op
  script coupling.
  - **The injection is conditional on `status == open` (read-and-CAS within the same batch).** This
    closes the race with admin `CompleteTask`/`CancelTask`: if T was already completed, the second
    flip is a **no-op** (no double `orchestration.taskCompleted`); if T was **cancelled**, auto-complete
    must **not** resurrect it (the CAS-on-`open` fails → the op still commits, but T stays `cancelled`
    and emits no completion event). This also bounds the stale-grant window (the cap-lens projection
    lags the status flip, so a just-closed task can still authorize via the stale projection — the CAS
    makes that commit's auto-complete a harmless no-op rather than a double-act).
  - **`orchestration.taskCompleted` consumption at Loom is idempotent** (JetStream is at-least-once): a
    redelivered completion for an already-advanced instance is dropped, not re-advanced.
- **`CompleteTask(taskKey)`** — retained only as an explicit admin / out-of-band completion path.
- **`CancelTask(taskKey)`** — for a task that is no longer needed (e.g. its target was withdrawn);
  distinct from completion.

Loom watches `orchestration.taskCompleted` regardless of which path emitted it.

**The engine supplies the task id (write-ahead requires it).** Crash-safety invariant 1 requires the
`token.<taskKey>` pointer be written **before** the side effect (`CreateTask`), so Loom must know the
`taskKey` ahead of commit. `CreateTask` therefore accepts an **optional caller-supplied `taskId`**
(present → used verbatim; absent → minted internally, so admin/manual callers are unaffected). The
engine derives a deterministic `taskId` from `(instanceId, cursor)` and passes it, making the `taskKey`
(`vtx.task.<taskId>`) known write-ahead. A crash-retry re-submits the **same** `CreateTask` and
collapses on the Contract #4 `vtx.op.<requestId>` tracker — no duplicate task. The `task` DDL is package
data (not a frozen contract); the grant/auth path (§10.7) is unchanged.

### Why this needs NO frozen-contract change

- **systemOp** correlation watches the tracker keyed by the `requestId` Loom itself chose.
- **userTask** correlation watches the generic `orchestration.taskCompleted` event, which carries the
  `taskKey` under `payload` intrinsically.
- Authorization reuses the existing `authContext.{task,target}` + `ephemeralGrants` (§10.7).

### Constraint

`loom-state` maps `{taskKey | requestId} → instance` via the durable co-located `token.<token>`
pointer (§10.3), resolved by direct GET; the instance records its single pending token. Because tokens
are unique per pending step, no one-active-instance-per-subject restriction is needed — concurrent
instances for the same subject are fully distinguishable, and any engine replica resolves any token.

### Crash-safety invariants (binding — "rebuildable" depends on these)

D3 calls the cursor "rebuildable," but rebuildability only holds if these orderings are mandated
(they are contract invariants, **not** Loom-story latitude):

1. **Write-ahead = the atomic batch (retained, now outbox-inclusive).** The `token.<token>` pointer, the
   `instance.<id>` update, **the `outbox.<token>` op record**, and the `deadline.<instanceId>` TTL are
   persisted to `loom-state` in **one `substrate.AtomicBatch`**. For a systemOp the side effect (the op
   reaching `core-operations`) is the **relay's** decoupled, idempotent publish *from* that batch — so
   the batch and the side effect are **no longer a dual write**: invariant 1 holds by construction, not
   by ordering discipline. (For a userTask the side effect is still `CreateTask`, keyed/idempotent — the
   engine supplies the deterministic `taskId` so the `token.<taskKey>` is known write-ahead.) On
   restart, a persisted `pendingToken` whose `outbox.` record still exists is simply re-published by the
   relay; one whose op already committed collapses on the Contract #4 `vtx.op.<requestId>` tracker. A
   crash can no longer orphan a token between side effect and persist.
2. **Guardless steps complete only via their token (retained).** A step with no guard has **no
   guard-replay signal** (guard-replay can't tell a guardless step ran). So a guardless step's
   completion comes **solely** from its `pendingToken` (taskId/requestId); re-drive must **not** re-run
   a step whose token is still pending, or it double-submits. (The §10.5 example ends on a guardless
   `SetAddress`.)
3. **(REMOVED) Completion watch suspended until rebuild.** There is no in-memory index to rebuild — a
   redelivered completion resolves against the durable `token.<token>` pointer (§10.3) regardless of
   engine age or replica. The durable per-domain consumer redelivers from its ack floor, and the
   pointer's presence is the idempotency guard, so no suspend-until-warm gate is needed.

---


## 10.9 Pattern trigger & lifecycle — `loom`-domain ops

§10.5/§10.8 settle the *auth* to start a pattern (`StartLoomPattern` + pattern-as-target) but not how a
**committed** trigger reaches the engine, nor how a pattern's terminal is announced. This section closes
both on the **event plane**, with no Core-KV instance state.

**Instance is operational-only (binding).** A Loom instance is **operational state** — it lives **only
in `loom-state`** (P1, the `instance.<instanceId>` cursor, §10.3) and gets **no Core-KV business
vertex**. Its lifecycle is announced on the **event plane** (`core-events`), **not** projected as
Core-KV business state. These ops emit their `loom.*` events the ordinary way: at commit the faithful
`EventList` is persisted as the **outbox aspect `vtx.op.<requestId>.events`** — alongside the universal
`vtx.op.<requestId>` tracker, in the same step-8 atomic batch — and the outbox CDC consumer publishes
from that aspect (`internal/processor/outbox/consumer.go`, filter `$KV.<bucket>.vtx.op.*.events`). So
each writes the **standard tracker + outbox-events aspect**; the distinguishing property is only that it
creates **no business-domain vertex** — the instance's sole durable home is the `loom-state` cursor.

**Three lifecycle ops** (shipped by `orchestration-base`; the engine stays generic), each → outbox →
`events.loom.*` (**P2: never a direct publish**):

| Op | Posted by | Business vertex | Emits (body: `instanceId, patternRef, subjectKey, requestId`) |
|----|-----------|-----------------|------|
| `StartLoomPattern{patternRef, subjectKey}` | **caller** (Weaver `scope:any` / client / fixture) | none | `loom.patternStarted` |
| `CompletePattern{instanceId}` | **Loom** (`identity:loom`) | none | `loom.patternCompleted` |
| `FailPattern{instanceId, reason?}` | **Loom** (`identity:loom`) | none | `loom.patternFailed` |

(Each also writes the universal `vtx.op.<requestId>` tracker + the `…events` outbox aspect — that is how
the event is emitted; none writes a business vertex.)

- **`instanceId` = the `StartLoomPattern` `requestId`** (already a NanoID) — no minting, and redelivery
  dedup is automatic (Loom's `loom-state instance.<instanceId>` cursor keyed on it → already present →
  skip). The instance's sole durable home is that cursor (§10.3).
- Loom runs a **fixed durable consumer on `events.loom.patternStarted`** (always-on, **independent of
  `completionDomains`**). On the event: validate `patternRef` against the loaded pattern registry, create
  the `loom-state instance.<instanceId>` cursor, submit step 0.
- The engine's **internal** completion/failure is a **`loom-state` status transition** (operational,
  `status ∈ {running, complete, failed}`); the `CompletePattern`/`FailPattern` op is the *outward
  announcement* (loop closure + nesting), the terminal Actuator op of an exhausted/failed pattern.
- **Idempotency needs no new machinery:** `StartLoomPattern`'s Contract #4 tracker dedups a duplicate
  trigger op at the Processor; Loom dedups at-least-once event redelivery on the `instanceId` (the
  `loom-state` cursor presence).
- **`loom` is a first-class domain:** Loom *consumes* `patternStarted` (trigger) and *emits*
  `patternCompleted`/`patternFailed`. A Loom completion is therefore itself a consumable completion
  event — so a Phase-3 **nested** pattern (a step that runs a sub-flow and waits) simply lists `loom`
  in its `completionDomains` (§10.5) and correlates on the sub-instance's token, with **no new
  machinery**.
- **Queryability** ("which flows are running") is served by **Loom's control plane** — analogous to
  Refractor's (`internal/refractor/control/service.go`), reading `loom-state` — **not** Core KV. It is
  its own (future) control-plane story; Weaver gets the analogous one (Story 9.4 control-API). A
  Refractor lens over the `loom.*` event stream remains an option for a durable read model if one is
  later wanted.

**No special Processor capability needed.** Event emission already rides the outbox aspect
(`vtx.op.<requestId>.events`) written in the commit batch, so a lifecycle op is an ordinary op that
emits events and writes no business vertex — nothing in the pipeline special-cases it. (An op whose
`result.Mutations` is empty but whose `result.Events` is non-empty still commits the tracker + the
`…events` aspect and publishes; confirm no upstream guard rejects an empty *business*-mutation set.)

---

