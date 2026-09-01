# Contract #10 (Loom) — pattern definition, step completion, lifecycle ops

> **A shard of [Contract #10 — Orchestration Surfaces](10-orchestration-surfaces.md)** — §10.5 /
> §10.6 / §10.9 keep their canonical numbers. The external-I/O **bridge**'s own adapter/envelope
> contract is [`docs/components/bridge.md`](../components/bridge.md); this shard covers only Loom's
> `externalTask` surface.

## 10.5 Loom pattern definition (package data)

A `meta.loomPattern` meta-vertex (loaded via CDC like a Lens def). A pattern declares a single
**`subjectType`** — the vertex the instance runs for; the trigger op supplies the subject id.
Guards and step operations are relative to the subject.

**Starting an instance** is the op **`StartLoomPattern{ patternRef, subjectKey }`** (`subjectKey`
must be a vertex of `subjectType`); pattern-*start* authorization is **§10.8 "`triggerLoom`
authorization"** (distinct from the per-step auth of §10.6/§10.7).

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
listens on for this pattern's step completions. A **domain** is the first segment of an event class
(`<domain>.<eventName>`, Contract #3 §3.4). **Defaults to `[subjectType]`** when omitted (covers the
common same-domain flow). A flow whose steps complete in a domain other than the subject's **must
list it explicitly**; the §10.6 per-step completion **deadline** is the not-silent backstop for an
omitted/mis-declared domain (FR29 never-silently-drop). Per-step granularity is unnecessary — the
**set** of domains is sufficient (§10.6 correlation is domain-independent).

**A userTask completes on the `orchestration` domain.** A userTask step completes via the
`orchestration.taskCompleted` event (the §10.6 auto-complete), regardless of the subject's type — so
an all-userTask onboarding pattern over `identity` subjects declares
`completionDomains: ["orchestration"]` (NOT `["identity"]`, on which the completion never arrives).
A pattern mixing userTask + systemOp steps lists every domain it completes on.

**Step shape:** `{ kind, operation, guard? }` for `userTask`, `{ kind, operation, guard?, reads?,
optionalReads? }` for `systemOp` — completion is implicit (§10.6), no per-step event. The
`externalTask` kind (below) is **two-op-shaped** and carries its own fields
`{ kind, adapter, params, replyOp, instanceOp }`.
- `kind` ∈ `userTask` (engine creates a task with links `assignedTo` → the subject,
  `forOperation` → the step's op, `scopedTo` → **the subject** — a Loom `userTask` scopes its grant
  to the instance subject; the frozen step shape carries no separate target field; UI renders from
  the op's self-describing DDL via the `forOperation` link) | `systemOp` (engine submits the op
  directly) | **`externalTask`** (engine submits the `instanceOp`, then parks awaiting the external
  result — see below).
- **Linear only** — no branches/loops/fan-out. A compound *path* is a Weaver signal. The
  `externalTask`'s two ops (submit-instanceOp → park) are **one logical step**, not a branch/fan-out.

**`reads` / `optionalReads` — a `systemOp`'s declared read-set (Amended 2026-08-07).** A `systemOp`'s
bound op is package-authored, so the pattern **declares** its read-set and the engine only resolves it.
Both fields are **`systemOp`-only** — a `userTask`'s read-set is derived from the §10.5
assignee/scopedTo invariant and an `externalTask`'s from its `params`, so a declared set on either kind
is **ignorable**, and a pattern carrying one is **rejected** at install and at load rather than
silently running read-free.

Entries are **subject-relative templates**, never literal keys: the bare token `subject` (the
instance's subject vertex) or `subject.<aspect>`, where `<aspect>` MUST be a Contract #1 **localName**,
validated at install and at load — the rendered key is then a 4-segment aspect key on that vertex. The
engine renders each entry against the running instance's `subjectKey` and passes the result as the
submitted op's `ContextHint.Reads` / `ContextHint.OptionalReads` (Contract #2 §2.5) — `Reads` for keys
whose absence is a correctness error, `OptionalReads` for absence-tolerant reads. A step declaring
neither stays read-free.

**`subjectKey` names a vertex.** §10.9's trigger payload carries a caller-supplied `subjectKey`, and it
is the namespace every subject-relative construct renders against: the engine requires a three-segment
`vtx.<type>.<id>` key and **drops the trigger otherwise**.

**`externalTask` (Amended 2026-06-18 — 13.1, External I/O Bridge).** A step that dispatches an idempotent
external call and **waits for its result** — symmetric to a `userTask` (dispatch to an async completer,
then park; the completer is a human for userTask, the bridge for externalTask). Shape:

```
{ "kind": "externalTask", "adapter": "<name>", "params": { ... }, "replyOp": "<ResolveOp>", "instanceOp": "<CreateInstanceOp>" }
```

- The engine submits the **`instanceOp`**, whose DDL (a) creates the **claim vertex** (Core KV business
  state — the FR58 "visible claim before the call", §10.3; its **type is package-chosen** — the bridge
  is **type-agnostic**) and (b) emits the `external.<adapter>` event from that op's own commit. The
  `external` domain is **ordinary** (the open `<domain>.<eventName>` model); the `instanceOp` DDL
  declares + emits the event-type as package data. The bridge (`docs/components/bridge.md`) consumes
  `events.external.>`, calls the adapter idempotently, and posts `replyOp` back.
- The engine mints the instance's **opaque handle write-ahead** and passes it to `instanceOp` as a
  **caller-supplied id** — the `instanceOp` DDL forms the claim-vertex key from it, and the engine
  stays type-agnostic.
- `adapter` is the external adapter name; `params` are **subject** templates — each value is either a
  literal or a `subject.<aspect>.data.<field>` / `subject.data.<field>` path (the §10.5 guard-path
  grammar), **resolved against the subject's current Core-KV state when the instanceOp runs** (a
  null/absent resolution is a data error — surface, do not dispatch). The submitter declares the
  subject root in the instanceOp's `contextHint.reads` and the template-inferred aspect keys in
  **`contextHint.egressReads`** (Contract #2 §2.5 class (f)) — so an adapter receives the real subject
  fields it needs without any reader touching a lens read-model. A field on a *linked* vertex is not
  reachable by a `params` template (that is the instanceOp DDL's own declared read); §10.8's
  `row.<column>` templating never applies on this path.
- **A template over a `sensitive: true` aspect (§3.10) resolves to a sensitive-ref**
  (`{"$sensitiveRef": {ref, ciphertext, mac, field}}` — the at-rest ciphertext, never plaintext; `mac`
  is the provenance stamp over `{ref, requestId, ciphertext}`, §3.10 ref-provenance rule): plaintext
  PII never enters the `external.<adapter>` event, the claim vertex, or any durable plane. The
  **bridge** is the unwrap point — at dispatch, just before the adapter call, it resolves each
  sensitive-ref via the **ref-verified Vault decrypt** (MAC mandatory; the event's top-level
  `requestId` is part of the verified input) using the identity's **live** key envelope (never a
  stored copy — §3.10 live-envelope rule), so a fabricated or spliced ref and a shredded identity's
  ref both fail closed. An unwrap failure is a data error: a permanent one (unverified / shredded /
  malformed / absent) posts the terminal `replyOp` with a failed outcome so the pattern converges; a
  transient one retries — never a blank field to a vendor.
- `replyOp` is the result-op type the bridge posts back (carrying `payload.externalRef` = the instance
  handle, §10.6) — **its DDL records the external outcome as aspect(s) on the claim vertex** (**D5**:
  business data lives in aspects; the vertex root `data` stays minimal), **not** as fat root `data`;
  `instanceOp` is the DDL op that mints the claim vertex + emits the event.
- **Completion is symmetric to a userTask.** Besides recording the outcome aspect(s), the `replyOp`
  DDL **emits `orchestration.externalTaskCompleted` carrying `payload.externalRef`** — the analog of
  `orchestration.taskCompleted{taskKey}` (§10.6). An externalTask pattern therefore declares
  **`completionDomains: ["orchestration"]`**. The event is emitted by the purpose-built `replyOp` (a
  userTask's is platform-injected); the wait for it is **unbounded** once the `instanceOp` commits
  (§10.6), exactly as a userTask's human wait is.

**Async resolution.** The bridge's adapter call MAY resolve **asynchronously** — the unbounded wait
above absorbs it with no change to the completion model. The pending-marker, re-poll, and **give-up
timeout** obligations are the bridge's contract (`docs/components/bridge.md`, Async adapters): a
timeout posts a terminal `failed` `replyOp`, so **a never-answered call converges rather than parking
forever**. The §10.6 creation deadline bounds the `instanceOp` submission only and disarms at its
commit; a dead bridge surfaces on the bridge's own Contract #5 Health, never a per-instance Loom
timeout.

**Guards — pure predicate over the subject's current state.** Absent guard = step always runs.

- **Paths are explicit** (consistent with the explicit-aspect-navigation principle):
  `subject.<aspect>.data.<field>` (aspect) or `subject.data.<field>` (root). Guards read **only
  the subject + its aspects** — no link-walking (a guard that needs related state is a Weaver
  signal).
- **Declarative grammar (default):** atoms `{absent: <path>}`, `{present: <path>}`,
  `{equals: {path, value}}`, composable with `{allOf|anyOf|not: [...]}` (still one boolean — NOT
  branching). **Pinned semantics (binding, removes ambiguity):** `absent` = the path resolves to
  **null, missing, a soft-deleted aspect, OR (for strings) empty-after-trim**; `present` = not
  absent. An empty-string-after-trim is **absent**; `"0"`/`false`/`0` are **present**.
- **Starlark escape hatch (reserved):** for a predicate the grammar can't express, a guard may be
  `{ "reads": ["<aspect>", ...], "starlark": "def guard(subject): return ..." }` — evaluated in the
  same verified-pure sandbox scripts run in (no I/O / env / network; deterministic). `reads` is the
  read-hint (which subject aspects to hydrate); the function gets `subject` exactly as a script gets
  `state`, returns a bool. Reserved — declarative-only ships without it.
- Either way a guard is **pure declarative data or a pure function** → the instance cursor is
  rebuildable by replaying guards (no side effects, deterministic).

Patterns + step→operation bindings + guards are package data; the engine is a generic interpreter.
**How a step's completion is detected and correlated to its instance → §10.6.**

---


## 10.6 Step completion & instance correlation

A step is correlated to its instance by a **unique token Loom already knows or the completion event
already carries** — concurrent-safe with **no topological guessing**: multiple instances per subject,
and many open tasks of one op-type per actor, are all unambiguous, and no
one-active-instance-per-subject restriction exists. Correlation is **domain-independent**: the
`completionDomains` partition decides only *which events Loom sees*, never *which instance* they
belong to. Redelivery is safe — **a redelivered completion for an already-advanced step is dropped,
never re-advanced**, and recovery never re-runs a step whose completion is still pending (no
double-submit).

| Step kind | Completion signal Loom consumes |
|-----------|----------------------------------|
| **userTask** | `orchestration.taskCompleted` carrying **`payload.taskKey`** — the task the engine created for the step |
| **systemOp** | a **committed** business event on a subscribed domain whose top-level **`requestId`** is the submitted op's. **failed/rejected is off-stream** (a rejected op emits nothing) — detected by the bounded per-step deadline (below), never a submit reply |
| **externalTask** | **`orchestration.externalTaskCompleted`** carrying **`payload.externalRef`** = the instance handle (emitted by the `replyOp` DDL, §10.5) |

All event business fields ride the Event envelope's **`payload`** object (Contract #3 §3.4), so Loom's
**three** structural correlation keys are **top-level `requestId`** (systemOp), **`payload.taskKey`**
(userTask), and **`payload.externalRef`** (externalTask). Loom stays domain-ignorant — at most one
pending step matches any completion.

### Failure detection — bounded machine waits, unbounded async waits, never a silent wedge

- **A systemOp step is bounded end to end.** Its op's commit **is** its completion. A rejected, failed,
  or unseen outcome is detected by the **per-step deadline**; the engine then distinguishes, by
  evidence, and (a) a committed op whose completion event was missed (a mis-declared
  `completionDomains`, a lost event) **advances and alerts** — the flow stays live; (b) a submission
  still in flight extends the wait; (c) a genuinely rejected/lost op **fails the instance per its
  retry policy, with an alert** — never a silent wedge (FR29). A late completion after a declared
  failure is dropped — a bounded, alerted divergence, not a silent one.
- **A userTask / externalTask step is two waits in sequence:** a **bounded** wait for the
  task/claim to be *created* (a machine action — sized to commit latency, not human latency), then an
  **unbounded** wait for the human / the bridge. A rejected or lost creation **fails the instance
  with an alert** instead of parking forever; once creation commits, the deadline **disarms** and the
  async wait has **no runtime timeout** — the human may take days, and a never-answering bridge is
  bounded by the bridge's own give-up obligation (§10.5 Async resolution). The creation deadline
  **never advances the cursor** — only the completion event does.
- **Honest nuance (binding for both async-completer kinds):** because the async wait has no runtime
  timeout, a pattern whose `completionDomains` omits `orchestration` (for a userTask **or** an
  externalTask step) is caught by a **load-time warn**, not a runtime backstop. **The warn is loud;
  the pattern is not rejected** (a future completion domain could differ). This deliberate,
  observable async wait is distinct from the **systemOp** deadline, which *does* advance on proof of
  commit, because for a systemOp the op's own commit **is** the completion.

### Completing a userTask — by `taskKey`, via `orchestration.taskCompleted`

A task is closed by **`taskKey`** (`vtx.task.<id>`; never by inferring actor+op-type — a manager may
hold many open tasks of one op-type for different targets). Completion emits
`orchestration.taskCompleted` carrying **`payload.taskKey`**. No new envelope field, no Contract #2
change — the op already carries `authContext.task` for §10.7 auth.

- **Primary path — auto-complete on the authorizing op's commit.** A task exists to authorize +
  track exactly one op (`forOperation`) on one target (`scopedTo`); performing that op **is**
  fulfilling the task. When an op authorized via `authContext.task = T` commits successfully, T's
  completion (`status → complete` + `orchestration.taskCompleted{taskKey: T}`) commits **in the same
  atomic commit** — platform-injected, no per-op script coupling, no
  "did-the-op-but-task-still-open" wedge.
  - **The injection is conditional on `status == open`.** If T was already completed, the second flip
    is a **no-op** (no double `orchestration.taskCompleted`); if T was **cancelled**, auto-complete
    must **not** resurrect it — the op still commits, but T stays `cancelled` and emits no completion
    event. This also bounds the stale-grant window: a just-closed task that still authorizes through
    a lagging grant projection produces a harmless no-op, never a double-act.
- **`CompleteTask(taskKey)`** — retained only as an explicit admin / out-of-band completion path.
- **`CancelTask(taskKey)`** — for a task that is no longer needed (e.g. its target was withdrawn);
  distinct from completion.

Loom consumes `orchestration.taskCompleted` regardless of which path emitted it.

**`CreateTask` accepts an optional caller-supplied `taskId`** (present → used verbatim; absent →
minted internally, so admin/manual callers are unaffected). The engine supplies a **deterministic**
`taskId` per step, so a crash-retry re-submits the **same** `CreateTask` and collapses on the
Contract #4 `vtx.op.<requestId>` tracker — **a crash never duplicates a task**. The `task` DDL is
package data (not a frozen contract); the grant/auth path (§10.7) is unchanged.

---


## 10.9 Pattern trigger & lifecycle — `loom`-domain ops

§10.5/§10.8 settle the *auth* to start a pattern (`StartLoomPattern` + pattern-as-target) but not how a
**committed** trigger reaches the engine, nor how a pattern's terminal is announced. This section closes
both on the **event plane**, with no Core-KV instance state.

**Instance is operational-only (binding).** A Loom instance is **operational state** — it lives only in
`loom-state` (§10.3) and gets **no Core-KV business vertex**. Its lifecycle is announced on the
**event plane**: these ops emit their `loom.*` events the ordinary way (Contract #3); the
distinguishing property is only that they create **no business-domain vertex**.

**Three lifecycle ops** (shipped by `orchestration-base`; the engine stays generic), each →
`events.loom.*` (**P2: never a direct publish**):

| Op | Posted by | Business vertex | Emits (body: `instanceId, patternRef, subjectKey, requestId`) |
|----|-----------|-----------------|------|
| `StartLoomPattern{patternRef, subjectKey}` | **caller** (Weaver `scope:any` / client / fixture) | none | `loom.patternStarted` |
| `CompletePattern{instanceId}` | **Loom** (`identity:loom`) | none | `loom.patternCompleted` |
| `FailPattern{instanceId, reason?}` | **Loom** (`identity:loom`) | none | `loom.patternFailed` |

- **`instanceId` = the `StartLoomPattern` `requestId`** (already a NanoID) — no minting, and
  redelivery dedup is automatic: a duplicate trigger **op** collapses on the Contract #4 tracker at
  the Processor, and a redelivered trigger **event** collapses on the instance that already exists.
  A re-emitted trigger for an instance that already ran (even to terminal) never re-runs it.
- Loom consumes `loom.patternStarted` **always** (independent of `completionDomains`) and starts the
  instance at step 0 against the pattern definition live at that moment (§10.3 definition binding).
- `CompletePattern` / `FailPattern` are the *outward announcement* of a terminal (loop closure +
  nesting) — a pattern completion is itself a consumable completion event, making `loom` a
  first-class domain: Loom *consumes* `patternStarted` and *emits* `patternCompleted`/`patternFailed`.
- **Queryability** ("which flows are running") is served by **Loom's control plane** — never Core KV.

---
