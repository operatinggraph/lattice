# Design — Script-read posture: declared reads as the norm, bounded enumeration, and Processor-side guarded operations

**Status: ✅ Andrew-ratified 2026-07-01** (reshaped over a ratification working session + two `bmad-party-mode` adversarial rounds — see §0). **Fires 1–2 contract surface (Contract #2 `optionalReads` + `enumerations` + read-posture) committed; the guard surface (Contract #2 `guard`/`correlationToken`, #3 `operation.guardSkipped`, #10 §10.5/§10.6) is staged at Fire 3 (build-deferred, and #03 currently carries an unrelated uncommitted edit).** **Fires 1–2 BUILT 2026-07-06 on branch `claude/fable-model-qb9o6s`, awaiting Andrew's review/merge — see §12 build checkpoint.**
**Author: Winston (Designer fire).**
**Backlog row:** `planning-artifacts/backlog/lattice.md` → *Refinements & ops* → "Script-read posture".
**Origin:** the Edge Lattice party-mode finding **F8** ("scripts reading Core KV is the root smell").
**Builds on / relates to:** Contract #2 §2.5 (context-hint) + §2.5.1 (`kv.Links`, ✅ 2026-06-28) · the **shelved** [effect-guard](loom-guardless-step-recovery-effect-guard-design.md) · the **held** [Starlark guards, Piece 2](loom-starlark-guards-design.md) · externalTask **Mechanism 2** (`Loom declares, the Processor hydrates`).

---

## For Andrew (one-look ratification)

**What it does.** Establishes the platform **read posture** for the write path — *a script is, to the extent its reads are declared, a pure function of `(op payload, declared+hydrated read-set)`* — and delivers three mechanisms that address **both** live-read problems, and relocate guard evaluation off the engine into the Processor as a **generic operation feature**.

**The three pieces (the umbrella):**
1. **`contextHint.optionalReads`** — a declared, absence-tolerant read that folds *read-before-create / dedup* into the declared-and-hydrated norm (today forced to a live `kv.Read` because `reads` faults on a missing key). **Fully solves** that class.
2. **Declared enumeration as metadata** — `kv.Links` stays **paged and bounded** (never hydrated — a high-degree hub's set must never be materialised), but the op **declares** the enumeration `{hub, relation, direction}`. The declaration is **metadata, not hydration**: it gives the Edge a *mirror-coverage gate* (resolving F4) and marks the op class-(e) for the lint. It **manages** the enumeration; it does not eliminate the live paged read (honest scope, §0).
3. **Processor-side guarded operations** — a generic Contract #2 envelope `guard` the Processor evaluates against declared+hydrated reads for *any* op, **inside the commit retry loop**. Guard-true → the op runs and commits as today; guard-false → the Processor commits a single outbox event **`operation.guardSkipped{correlationToken}`** and no mutations. Retires the lone non-Processor Core-KV reader (Loom's `evalGuard`) by superseding it; **Loom is the first consumer, not the owner.** **Build-deferred to its first guarded-pattern consumer** — the *shippable* umbrella is Fires 1–2.

**Frozen-contract changes (staged UNCOMMITTED — the diff is the proposal):**
- **Contract #2 §2.5 / §2.2** — read-posture subsection + **`optionalReads`** (drafted; class-(e) framing corrected per §0). Additive.
- **Contract #2 (new §2.x) + §2.2** — the generic **`guard`** field + the grammar **relocated from §10.5**; + **`correlationToken`** (opaque, echoed on skip). Grammar unchanged.
- **Contract #3** — the primordial **`operation.guardSkipped`** event type (new `operation` domain, a *generic lifecycle* event); the **`contextHint.enumerations`** metadata shape. Additive.
- **Contract #10 §10.5 / §10.6** — a Loom step-guard *is* the Contract #2 `guard` on the step's dispatched op, evaluated Processor-side; Loom advances off `operation.guardSkipped` via existing token correlation. No grammar change.

**No architectural fork.** Every read here is the write path reading its own Core KV (P2/P5 place no bar); the posture *removes* a non-Processor reader.

**Two design forks, recommendation given (§6):** (a) enumerate-then-write concurrency — a best-effort **companion-epoch lint** backed by **Weaver detect+recover** (recommended) vs a platform conditioned-enumeration primitive (rejected — over-invests in prevention); (b) the skip event — a **fixed primordial `operation.guardSkipped`** (recommended) vs a caller-declared event-type meta-vertex (rejected — spoofing + schema-cook + YAGNI, §0).

---

## 0. What changed and why (this revision — grounding session + two party rounds)

1. **`kv.Links` is not "irreducible" — but it is *managed*, not *solved*.** The prior draft quarantined class-(e). Corrected: it is addressed by **declared enumeration as metadata** (§3.2) — Edge-gate + lint — **without hydration**. But `kv.Links` stays a live paged read; only *read-before-create* is fully solved. The headline no longer claims parity.
2. **Never hydrate an enumeration.** Hydrating the result-set at step-4 is **unbounded** for a high-degree hub — reintroducing exactly what `kv.Links`' paging prevents. The declaration is metadata only.
3. **The two "live reads" are different layers.** The *engine* read (Loom `evalGuard`, `guard_eval.go:204`) is the P-alignment concern — and is **dormant** (no shipped package guard; grammar exercised only in `internal/loom/*_test.go`). *Script* reads (`kv.Read`, `kv.Links`) run **in the Processor sandbox** — not a P-violation; their gaps are latency + Edge-predictability.
4. **Guards are a generic Processor feature; Loom is event-driven; events are outboxed.** A guard-false is a **normal minimal outbox commit** emitting a generic event Loom consumes like any terminal — not a query RPC, not a "no-op / no-tracker" outcome.
5. **Guard-eval must run *inside* the commit retry loop** (party #9). A §3.2 `RevisionConflict` re-hydrates + re-executes; if the guard is evaluated once *before* the loop, a race that makes the guard's condition flip (e.g. read-before-create where the key now exists) would run the op against a stale-true guard. The guard re-decides each attempt.
6. **The skip event is a *fixed, generic* `operation.guardSkipped`, not caller-declared** (party round 2). The meta-vertex/caller-declared-event idea (to "keep the Processor generic") was considered and **rejected**: it opened an **event-spoofing surface** (a dispatcher naming `clinic.appointmentCreated` as its skip type → a phantom business event), a **schema-cookability** coupling (Contract #3 step-7 validates the payload → a rich declared schema the Processor can't fill → wedge), and served **no real second consumer** (Loom is the only one, and dormant). A fixed `operation.guardSkipped` is a **generic operation-lifecycle event** (like the `vtx.op` tracker) — *not* business knowledge — so it already satisfies "keep the Processor generic," structurally closes the spoofing surface, and owns one schema. The caller-declared variant (via the event-type vertex's Starlark aspect) is **shelved** for a real future consumer that needs a custom skip event.
7. **Correlation uses one echoed opaque token** (party #1). The skip event carries an **echoed `correlationToken`**; Loom passes its natural per-step token (systemOp→`requestId`, userTask→`taskKey`, externalTask→`externalRef`) → parks **one** token, correlates run-xor-skip on it, deletes one. No two-token orphan cleanup; all three step types guardable.
8. **Enumerate-then-write concurrency is best-effort; Weaver detect+recover is the enforcer** (Andrew, #2). In an eventually-consistent system, prevention is imperfect by design — a stronger prevention primitive (conditioned enumeration) chases a guarantee the write path doesn't make. The companion-epoch lint reduces contention; the actual invariant-enforcer is a Weaver convergence lens (detect the violation) + a remediation (recover) — the platform's existing level-reconcile machinery (the longer story, out of scope here).

---

## 1. Problem & intent

### 1.1 The root (F8) and two distinct symptoms

- **The engine read (`evalGuard`).** Loom evaluates step guards *in `internal/loom`*, point-reading Core KV (`guard_eval.go:204`). The only non-Processor Core-KV business read on the orchestration path — the genuine P-alignment violation Andrew shelved the effect-guard over ("guards are the only exception, waiting for a better architectural approach"). **This is that approach.** It is also **dormant** (no shipped consumer).
- **The script reads (`kv.Read`, `kv.Links`).** Run **inside the Processor's Starlark sandbox** — not a P-violation. Gaps: (a) exec-time latency when lazy/undeclared, (b) Edge un-predictability.

If a script were a pure function of its declared read-set + payload, the Processor could hydrate that set and evaluate (including a guard), and the Edge could check it against its mirror and predict. That is the posture.

### 1.2 A grounding correction (framing)

"Live `kv.get` as deprecatable debt" is **too strong**. `kv.Read` on a **declared** key is already a pure hydrated-cache hit. Live reads split into five classes (§2), only one of which is debt. The posture maximises the declared class, adds the missing primitive (`optionalReads`), and **names** the two legitimate live classes (deliberate config reads; enumeration) as bounded exceptions.

### 1.3 Inventory (grounded)

| Surface | Reads | Class | Verdict |
|---|---|---|---|
| **Loom `evalGuard`** (`guard_eval.go:204`) | subject + guard aspects (keys known) | (a) | Lone engine read — **dormant**; **superseded** by the generic Processor guard (§3.3) on the first guarded pattern. |
| Loom externalTask params + `external_params.go` | subject aspect, declared | (a) | The posture already working — the model to mirror. |
| Weaver reconciler/temporal | `weaver-targets` (lens) + `weaver-state` | — | P5/P1-clean; no Core-KV business read. |
| clinic `kv.Links(provider hasBooking)` + per-candidate `kv.Read` | booking topology + candidate schedule/status | (e) | Bounded paged enumeration — declare it (§3.2), keep it paged; serialise on the declared `.bookingGuard` epoch (best-effort). |
| clinic `kv.Read(.hours / .timeOff)` | config, known, live on purpose | (c) | Out of OCC so config edits don't conflict. Sanctioned, annotated. |
| `orchestration-base` CreateTask `kv.Read(task_key)` + `.availability` | to-be-created key + availability | (d) | **Migrate to `optionalReads`** (Fire 1's real consumer; re-inventory `.availability`, added by FR28 Fire 2). |

---

## 2. The read posture — the five-class classification

**Drive everything toward (a); fold (d) into (a) via `optionalReads`; declare (e) as bounded metadata; keep (c) annotated; lint (b) as debt.**

| # | Class | Key known? | OCC-snapshotted? | Replay-stable? | Edge-predictable? | Disposition |
|---|---|---|---|---|---|---|
| **(a)** | Declared exact-key | yes | yes | **yes** | **yes** | **The norm.** |
| **(b)** | Declarable-but-undeclared lazy `kv.Read` | yes | no | no | no | **The only debt.** Lint → (a). |
| **(c)** | Deliberately-unsnapshotted config read | yes | **no (deliberate)** | no | no | Sanctioned, **annotated**. |
| **(d)** | Read-before-create / dedup | yes | yes (via `optionalReads`) | **yes** | **yes** | **`optionalReads`** folds into (a). |
| **(e)** | Enumeration (`kv.Links`) + follow-up | **no** (data-derived, unbounded) | no | no | **gated** (§3.2) | **Bounded paged live read, declared as metadata.** Not hydrated. |

**Determinism (§2.1).** Class-(a)/(d)-only ⇒ replay-stable. Any (b)/(c)/(e) ⇒ not replay-stable — **tolerated**: the Processor (deterministic id + OCC + `CreateOnly`/`RevisionConflict`), not replay determinism, is the idempotency authority. The posture makes the non-stable reads **few, named, statically visible** (§3.4).

---

## 3. The shape — three mechanisms

### 3.1 `contextHint.optionalReads` (read-before-create — class d)

```jsonc
"contextHint": {
  "reads":         [ "vtx.identity.<id>" ],       // REQUIRED — absent ⇒ HydrationMiss (fail-closed)
  "optionalReads": [ "vtx.task.<derivedTaskId>" ]  // TOLERATED — absent ⇒ known-absent sentinel (None)
}
```

- Hydrator hydrates `optionalReads` like `reads`, **except** `ErrKeyNotFound` is **not** `HydrationMiss` (grounded: `reads` *does* fault — `step4_hydrate.go:154`): the key is recorded *known-absent*, `kv.Read`→`None` from cache.
- **Load-bearing invariant (party #8):** the known-absent sentinel must drive §3.2 **create-only** conditioning — a create off an absent `optionalReads` key is auto-conditioned on absence (`expectedRevision` = the step-4-observed absence), so a concurrent create that wins between step 4 and step 8 is caught by `RevisionConflict` → re-hydrate → now present → the script re-branches no-op. **Explicit AC + the same-commit-race test.**
- **Authoring rule (fail-closed).** A key whose *absence is a correctness error* stays in `reads`. `optionalReads` is only for a read whose absence is a legitimate branch. (Absence-tolerance is an idempotency branch, not an authz boundary.)

### 3.2 Declared enumeration as metadata (`kv.Links` — class e)

`kv.Links` (§2.5.1) is **paged and bounded**; this design **does not hydrate** it. The op declares the enumeration as metadata:

```jsonc
"contextHint": {
  "reads": [ "vtx.provider.<p>.bookingGuard", "vtx.patient.<q>.bookingGuard" ],
  "enumerations": [ { "hub": "vtx.provider.<p>", "relation": "hasBooking", "direction": "out" } ]
}
```

What the declaration buys, without materialising anything:
- **Edge gate (resolves F4).** The Edge checks *"is `relation` from `hub` fully in my Interest-Set mirror?"* → **predict iff yes, else degrade to pending.** A high-degree hub degrades (correct); a bounded mirrored relation predicts.
- **Static classification** for the §3.4 lint + the Edge predictability flag.
- **Concurrency: best-effort, not a guarantee.** An enumerate-then-write should serialise concurrent writers on a **companion epoch** declared in `reads` (clinic's `.bookingGuard`, bumped by every booking mutator). The §3.4 lint nudges the *reader* to declare it. **But prevention is best-effort by design** (Andrew): the epoch reduces contention; it does not guarantee (a new mutator that forgets to bump it, or an unavoidable race, still slips through). **The invariant-enforcer is Weaver detect + recover** — a convergence lens flags the double-book violation, a remediation resolves it (the platform's level-reconcile machinery — the longer story, out of scope). The design does **not** add a stronger prevention primitive; that chases a guarantee the write path deliberately doesn't make.

### 3.3 Processor-side guarded operations (generic `guard`)

A **guard** is a precondition on an operation: *run the op iff the predicate holds against the op's declared+hydrated reads; else skip.* A **generic Contract #2 envelope feature**, evaluated by the Processor — not a Loom concept.

- **The envelope carries a `guard`** (rides like `contextHint`) — the §10.5 grammar `{absent|present|equals}` / `{allOf|anyOf|not}`, **relocated verbatim** to Contract #2 and generalised to "any declared key + path." Every atom is **absence-tolerant** (`guard_eval.go`), so the guard's read-set is declared in **`optionalReads`** — §3.1 is *literally* what lets a guard declare its reads without faulting. The pieces interlock.
- **The Processor evaluates the guard pre-script, inside the commit retry loop** (party #9 — `commit_path.go:290`): after step-4 hydrate, before execute, **re-evaluated each attempt**. The `evalGuard` resolver logic relocates `internal/loom` → `internal/processor`, now pure over the hydrated map. The guard shares the op's step-4 OCC snapshot, so guard + mutation are one unit — *closing* the guard→commit TOCTOU window today's live-read guard has.
- **Two branches, both ordinary outbox commits** (events are outbox'd into the step-8 batch `vtx.op.<id>.events`, published step 9 — emitted iff the commit lands):
  - **guard-true** → run the script → `{ business mutations, terminal event }` (as today).
  - **guard-false** → the Processor **synthesizes** the result `{ mutations: [], events: [ operation.guardSkipped{correlationToken} ] }` (party #3 — the script does not run, so this EventList is *Processor-authored*, a small new commit-path branch — not "reuses everything unchanged"). A **normal minimal commit**: the outbox event + the normal `vtx.op` tracker, zero mutations. **No new op outcome, no "no-tracker" special case.**
- **The skip event is fixed and generic — `operation.guardSkipped`** (party #6): a primordial core event in a new **`operation`** domain (a *lifecycle* event, not business knowledge). It carries an **echoed opaque `correlationToken`** (party #1) the dispatcher supplied on the envelope. Any dispatcher of a guarded op reacts; a client could guard an ordinary op for a conditional write. *(The caller-declared-event-type / event-vertex-Starlark-aspect variant is shelved — it opened a spoofing surface + a schema-cook coupling for no real second consumer.)*
- **Trust-boundary dependency (party #5):** the skip signal's integrity rests on `core-events` being **Processor-only-writable** — a forged `operation.guardSkipped{live-token}` would advance a consumer past an unrun step. This is the same surface the **NATS account-write-restriction** item closes; named as a dependency.

**Loom as the first consumer (one token, no engine-logic change).** Loom advancement is event-driven (`submitStep` fire-and-forget + a parked durable token; the committed event correlated back — `handleCompletion`, `engine.go:681`). A guarded step attaches the `guard` to its dispatched op and passes **its natural per-step token as `correlationToken`**:
- guard-true → the op's terminal event (carries the token) → advance;
- guard-false → `operation.guardSkipped{correlationToken}` (echoes the token) → advance past.

Loom is **domain-ignorant** (`engine.go:665` — "it does not know which event is which, it tries each key against the durable token store"), so it correlates the skip event by token with no type-matching. It parks **one** token (systemOp→`requestId`, userTask→`taskKey`, externalTask→`externalRef`), and exactly one of {run, skip} fires → correlate + delete one. All three step types guardable. Wiring: widen the `core-events` subscription to see the `operation` domain; add `payload.correlationToken` to `correlationKeys` (re-assert the single-live-pointer invariant). The dormant `evalGuard` is **removed when the first guarded pattern ships** — which also unblocks the held **Starlark-guards Piece 2** (a Starlark guard becomes a pure predicate over hydrated reads in the Processor's sandbox).

### 3.4 Static read-classification (the conformance hook)

A `lint-conventions` check: `kv.Read(<literal>)` declared → (a); a knowable literal **undeclared** → (b) **debt** (flagged); `kv.Read(<expr>)` / `kv.Links` → (e) (must carry an `enumerations` declaration; an enumerate-then-write is *nudged* to declare a companion epoch — best-effort, not a guarantee, §3.2); a config read → (c) (must be annotated). Same posture as `TestPackage_NoScans`, extended from "no raw scans" to "declare your declarable reads." The Edge predictability flag falls out of the same classification.

---

## 4. Contract surface

| Doc / § | Change | What |
|---|---|---|
| **Contract #2 §2.5 + §2.2** | staged | Read-posture subsection + **`optionalReads`** (absence-tolerant; the create-only invariant; the required-stays-in-`reads` rule). |
| **Contract #2 (new §2.x) + §2.2** | to stage | The generic **`guard`** field (grammar relocated from §10.5, generalised) + **`correlationToken`** (opaque, echoed). Evaluated Processor-side, in the retry loop. |
| **Contract #2 §2.5.1** (`kv.Links`) | build-to | Ratified; the `enumerations` declaration references it. No change to the primitive. |
| **Contract #3** | to stage | The primordial **`operation.guardSkipped`** event type + the **`operation`** domain (a generic lifecycle event; core-registered, Processor-emitted); the **`enumerations`** metadata shape. |
| **Contract #10 §10.5 / §10.6** | to stage | A Loom step-guard *is* the Contract #2 `guard`; evaluated Processor-side; Loom advances off `operation.guardSkipped` via token correlation. Grammar unchanged; §10.6 crash-safety preserved (guard re-evaluated per advancement; the outbox makes the skip durable-iff-committed). |
| `internal/processor` | build-to | `optionalReads` sentinel + create-only conditioning; the relocated guard evaluator **in the retry loop**; the Processor-synthesized guard-false result. |
| `internal/loom` | build-to | Widen the `core-events` subscription; add `payload.correlationToken` to correlation; attach the `guard` + pass the token; remove `evalGuard` **on the first guarded pattern**. |
| `packages/orchestration-base` CreateTask | build-to | Migrate the dedup reads → `optionalReads`. |
| `lint-conventions` | build-to | class-(b) debt gate; class-(c) annotation; the enumerate-then-write epoch nudge. |

**Convention friction flagged.** §2.5 calls `contextHint` a pure *optimization*. The posture **elevates** declared exact-key reads to the **expected norm** — a genuine stance change (non-breaking; undeclared reads still execute lazily, flagged debt). The heart of the staged edit.

---

## 5. Reconciliation ("but didn't we…?")

- **`kv.Links` already ratified as the bounded exception?** Yes — kept exactly; the umbrella adds *declaration metadata* (Edge gate + lint), not a change, and does **not** hydrate it.
- **The shelved effect-guard re-opened?** No — that *added* a Loom read; this *removes* it. The recovery-idempotency problem stays out of scope but becomes re-expressible once guards are Processor-evaluated.
- **Duplicate of externalTask Mechanism 2?** It **generalises** it (params → guards → any op).
- **Does "solves both" over-claim?** Corrected — solves read-before-create; **manages** the enumeration (Edge-gate + lint), which is still a live paged read.
- **New op outcome / query RPC for the guard?** No — event-driven + outboxed makes it a normal event-emitting commit.
- **Auth / P5 / P2?** Untouched and reinforced; a non-Processor reader is removed.

---

## 6. Forks (recommendations given)

**Fork A — enumerate-then-write concurrency.** **Recommend the best-effort companion-epoch lint + Weaver detect/recover** (Andrew): prevention is best-effort in an eventually-consistent system; the epoch reduces contention, Weaver enforces the invariant. *Rejected: a platform "conditioned enumeration" primitive* — over-invests in a write-time guarantee the architecture deliberately doesn't make; more substrate machinery + a re-LIST per commit.

**Fork B — the skip event.** **Recommend a fixed, primordial `operation.guardSkipped{correlationToken}`** (generic lifecycle event; one owned schema; structurally no spoofing surface). *Rejected: a caller-declared skip-event-type meta-vertex* — a dispatch-time spoofing surface + a schema-cookability coupling for no real second consumer; the event-vertex-Starlark-aspect variant is shelved for a real future need.

---

## 7. Migration & test strategy

Additive + backward-compatible. `optionalReads`/`enumerations`/`guard`/`correlationToken` are new optional fields. The lint lands warn→block. The guard ships with its first consumer.

- **`optionalReads`:** present ⇒ cache hit; absent ⇒ sentinel, `kv.Read`→`None`, no `HydrationMiss`; a `reads` key still faults. **The create-only conditioning off the sentinel** + the same-commit-race suite; two CreateTask same `taskId` → exactly one.
- **Enumeration:** an enumerate-then-write missing its epoch → lint nudge; a `kv.Links` op carries a matching `enumerations` declaration; `TestPackage_NoScans` green. *(Weaver detect/recover of an actual double-book is a separate story.)*
- **Guarded ops:** guard-true → runs + commits; guard-false → `{[], operation.guardSkipped}` (empty mutations, tracker present, one outbox event, echoed token); the relocated evaluator reproduces `guard_eval_test.go` over a hydrated map; **the retry-loop-flips-guard case** (a race makes the guard flip → the op skips on retry). Loom (first pattern): guard-false → advance past via token; assert **no Core-KV business read remains in `internal/loom`**; the single-live-pointer invariant holds with `correlationToken` added. Ephemeral-stack e2e: a guarded pattern converges Processor-side.
- **Gates:** `go build`, `make vet`, `golangci-lint`, STRICT `lint-conventions`, `make verify-kernel`, `go test -race`, the loom guard/external e2e (guard fire).

---

## 8. Risks

1. **`optionalReads` softening a required read.** Contained by the §3.1 authoring rule + lint + review; the create-only invariant is the correctness anchor.
2. **Enumerate-then-write race.** Best-effort epoch lint + **Weaver detect/recover** is the real net (accepted, §3.2).
3. **Guard-eval outside the retry loop → wrong result under contention.** Closed by evaluating in-loop, per attempt (party #9).
4. **Skip-signal forgery.** Closed by core-events being Processor-only-writable (NATS-write-restriction dependency, party #5).
5. **Guard build before a consumer = dead scaffolding.** Avoided — build-deferred to the first guarded pattern (§9).
6. **Guard needing an enumeration atom** (a future `linkPresent`). Out of scope; flagged for whoever revives it.

---

## 9. Decomposition (build only after ✅ Andrew-ratified)

**The shippable umbrella is Fires 1–2.** Fire 3 (guards) is **ratified-shelf, build-deferred**.

- **Fire 1 — `optionalReads` + CreateTask (S–M; full review).** Contract #2 edit; Hydrator sentinel + create-only; CreateTask dedup migrated. *Green:* the dedup + same-commit-race suites pass declared.
- **Fire 2 — classification lint + the `enumerations` declaration (S–M; thorough lead review).** Contract #3 `enumerations`; the class-(b)/(c) gates + the enumerate-then-write epoch nudge; annotate the clinic/config reads. *Green:* tree passes; `TestPackage_NoScans` unaffected.
- **Fire 3 — generic Processor-side guarded operations (M; FULL 3-layer review). BUILD-DEFERRED to the first guarded-pattern consumer.** Contract #2 `guard`+`correlationToken` + Contract #3 `operation.guardSkipped` + §10.5/§10.6; the Processor guard evaluator (in-loop) + the synthesized guard-false result; Loom's subscription + correlation + attaching the guard + removing `evalGuard`; the first real guarded pattern. Unblocks Starlark-guards Piece 2. *Green:* the pattern converges Processor-side; no Core-KV business read in `internal/loom`.
- **Fire 4 (optional, XS) — Edge A′ predictability flag** (once Edge is in build): consumes the §3.4 classification + the `enumerations` gate. A beneficiary, not on the critical path.

*Order: 1 → 2 firm. Fire 3 gated on a real guarded-pattern consumer.*

---

## 10. Open questions — resolved

- **Debt?** Only class-(b). (§1.2/§2.)
- **New field vs overload `reads`?** Distinct `optionalReads`. (§3.1.)
- **Hydrate `kv.Links`?** No — unbounded; declared as metadata, stays paged. (§3.2.)
- **Enumerate-then-write safety?** Best-effort epoch lint + **Weaver detect/recover**; no stronger prevention primitive. (§3.2/§6.)
- **Where/what is a guard?** A generic Contract #2 `guard`, Processor-evaluated **in the retry loop**; guard-false → a fixed `operation.guardSkipped{correlationToken}`. (§3.3.)
- **New op outcome / query RPC?** No — event-driven + outboxed → normal event-emitting commit. (§3.3/§6.)
- **Caller-declared skip event?** No — fixed generic event; the meta-vertex variant is shelved (spoofing/schema/YAGNI). (§0/§6.)
- **Correlation for guarded userTask/externalTask?** One echoed `correlationToken`; Loom parks one token. (§3.3.)
- **Pre-build anything dead?** No — Fire 3 build-deferred to a consumer. (§9.)

---

## 11. Adversarial review — party-mode findings folded in

Two `bmad-party-mode` rounds (Andrew-requested) surfaced, and this revision incorporates: **#1** the guarded userTask/externalTask token-correlation gap → the echoed `correlationToken` (§3.3); **#2** enumerate-then-write prevention is best-effort → Weaver detect/recover (§3.2/§6); **#3** the guard-false result is Processor-*synthesized* (honest, §3.3); **#4→#6** the platform-event provenance → a fixed generic `operation.guardSkipped`, after the caller-declared meta-vertex was rejected for a spoofing surface + schema-cook + YAGNI (§0/§6); **#5** the core-events write-integrity trust-boundary dependency (§3.3/§8); **#8** the `optionalReads` create-only conditioning invariant (§3.1); **#9** guard-eval inside the retry loop (§3.3). Self-adversarial checks folded earlier: enumeration unboundedness, the outbox/no-outcome correction, the engine-vs-script distinction, guard dormancy. **The Designer-lane adversarial gate is discharged** (recorded run); Fire 3's build carries the full 3-layer review.

---

*Designer fire — Winston. Awaits Andrew's ratification of the staged Contract #2/#3/#10 edits before the Steward builds Fires 1–2 (Fire 3 on its first guarded-pattern consumer).*

---

## 12. Build checkpoint — Fires 1–2 SHIPPED (2026-07-06, remote Steward fire)

**Scope built:** Fires 1 + 2 exactly; Fire 3 (guards) untouched per the Andrew-routed assignment. All
code + these docs ride branch `claude/fable-model-qb9o6s` (per the mid-fire protocol change: **no merge
to main, no main pushes — Andrew's local agent reviews the branch and merges**). Commits:
`697c0b0` (processor optionalReads), `0809d89` (Loom/Weaver/orchestration-base dispatch migration),
`0a6ae40` (read-posture lint + annotations + enumerations exemplar), `112327b` (gofmt fixup), plus this
checkpoint + the board-row edit.

**Fire 1 — what shipped.**
- `internal/processor`: `ContextHint.OptionalReads` + `EnumerationHint` on the envelope (enumerations
  shape-validated at `ParseEnvelope`: hub/relation required, direction ∈ {out,in}); Hydrator hydrates
  optionalReads like reads except not-found → `ScriptContext.KnownAbsent` (never `HydrationMiss`; a key
  in both lists keeps fail-closed `reads` semantics); `kv.Read` serves a known-absent key as `None`
  from the step-4 snapshot with **no live GET**; commit retry loop grows the (A′) attribution —
  `absentConditionedCreates` × `materializedAbsentKeys`: a create off a known-absent key whose key now
  exists is retry-eligible (re-hydrate → present → script re-branches no-op), while undeclared
  create-once collisions surface unretried exactly as before (pinned by the existing
  `CreateOnceCollisionSurfacesWithoutRetry` test). Same-commit-race e2e over real
  Hydrator/Starlark/Committer proves the §3.1 invariant end-to-end (winner's doc survives).
- Dispatchers: Loom `outboxRecord`/relay/`buildOutbox` carry optionalReads; `submitUserTask` declares
  `[taskKey, subject.availability]` (`userTaskOptionalReads`, drift-guarded). Weaver `plan` grows a
  claimID-keyed `optionalReads` closure (dedup key derives from the claimId-seeded stable taskId —
  payload-vs-declaration equality pinned by `TestBuildPlan_AssignTask_OptionalReadsMatchPayload`);
  `actuator.submit` threads it. orchestration-base DDL comments updated to the declared posture;
  `TestCreateTask_DeclaredOptionalReads_CreateThenDedup` covers absent→create and re-dispatch→no-op.

**Fire 2 — what shipped.**
- `lint-conventions`: read-posture classification for `kv.Read`/`kv.Links` call sites in `packages/`
  non-test files — `# read-posture: (c|d|e)` annotation (same line or ≤8 lines above); unannotated ⇒
  class-(b) debt finding; kv.Links must be (e) with `relation=`; enumerate-then-write (e) without
  `epoch=` gets the companion-epoch nudge. **All read-posture findings are ADVISORY** (`warn:` prefix,
  never fail `--strict`) — the §7 warn-first landing; flip to blocking is a later one-line decision
  once the debt list is worked down (55 warnings at ship).
- Annotations: ClaimTask queuedFor enumeration (e, epoch = the task root its OCC-asserted update
  contends), identity-hygiene + rbac-domain tombstone-guard enumerations + follow-up reads (e,
  `epoch=none (accepted)` — read-only guards; Weaver detect+recover is the enforcer), clinic
  `.hours`/`.timeOff` (c), CreateTask dedup + availability (d).
- `enumerations` end-to-end: MergeIdentity's pipeline test declares its open-tasks-guard enumeration
  (the first declared kv.Links op); parse-validation unit tests in `envelope_test.go`.

**Grounded corrections (code moved since the design was written).**
1. §1.3's inventory row "clinic `kv.Links(provider hasBooking)`" no longer exists — clinic moved to
   write-path slot claims (see the hard-delete row's shelve note). Today's kv.Links consumers are
   ClaimTask (queuedFor out), identity-hygiene and rbac-domain tombstone guards (in). The §3.2
   companion-epoch exemplar therefore became ClaimTask's task-root OCC (a real epoch: every queuedFor
   mutator commits through the task root), and the two read-only tombstone guards record an explicit
   `epoch=none` acceptance.
2. §4's "Contract #3 — `enumerations` metadata shape, to stage": the committed Contract #2 §2.5 text
   already specifies the shape in full; no Contract #3 edit is needed for Fires 1–2 (and none was
   made). The #3 edit remains grouped with the Fire-3 guard surface (`operation.guardSkipped`).
3. The §3.1 "expectedRevision = the step-4-observed absence" phrasing is realized structurally:
   `CreateOnly` *is* the absence assertion at commit (no new mutation field); the design's real delta
   was making that collision **retry-attributable**, which is what (A′) adds.

**Held / not built:** Fire 3 (guard/correlationToken/guardSkipped/§10.5 relocation, `evalGuard`
removal) — build-deferred to the first guarded-pattern consumer, contracts staged then. Fire 4 (Edge
predictability flag) — awaits Edge build. Migrating the remaining 55 class-(b) call sites (lease-
signing, augur, capability-author, clinic dedup reads, ClaimTask's speculative reads …) to
optionalReads/annotations — the lint's debt list is the worklist; flip the lint to blocking after.

**Gates at ship:** `go build ./...`, `go vet ./...` clean; full `go test ./...` green on the branch
(embedded-NATS; Postgres-gated tests skip — no adapter surface touched); `lint-conventions` STRICT
exit 0 (0 issues, 55 advisory read-posture warnings = the intended debt list); board lint clean.
`golangci-lint` could NOT run in the remote container (its binary is built on Go 1.25, the repo
targets 1.26.1 — accepted environment limitation): **CI's golangci-lint is the authority** for that
gate on this branch. Review: adversarial self-review, three lenses (acceptance vs Fire 1–2 scope;
blind bug-hunt over the full diff on crash/idempotency/ordering; validation + error-path edges).
Outcome: **no BUG-severity findings.** Fixed: the lint hook mislabeled advisory read-posture warnings
as CI-blocking (`27b4145`). Dismissed with grounds: (a) `materializedAbsentKeys` counting a
coincidentally-existing key — impossible, a create on that key in the batch means the CreateOnly
genuinely conflicted, and retry is safe regardless (full re-derivation, bounded by
MaxCommitAttempts); (b) tombstoned-key false-materialization — Conn.KVGet returns not-found for
deleted keys; (c) Weaver's double `deriveStableTaskID` derivation drifting — deterministic fn,
equality pinned by test; (d) old-binary/new-record skew on the Loom outbox optionalReads field —
degrades to the lazy-read fallback, benign; (e) lint annotation-leak between adjacent calls within
the 8-line window — advisory-only surface, closest-annotation-wins mitigates.

**Addendum (2026-07-06, post-close): the background blind-hunt reviewer's report landed after the
fire closed** (the child was running, not lost — the inline lens re-run above stands, but the full
report went deeper). Reconciliation, fixes applied on the fire branch pre-merge:

- **CONFIRMED (new, not an overturned dismissal): logical-delete create-wedge in CreateTask.**
  Dismissal (b) above covered *hard* tombstones only (KVGet not-found → true self-heal). For a
  *logically*-deleted task (present doc, `isDeleted=true`) the script's create can NEVER commit —
  `step8_commit.go` sets `CreateOnly=true` unconditionally, the still-present key always conflicts,
  the conflict is structurally non-retry-attributable (hydrates as present, not known-absent), and a
  Weaver reclaim reproduces it every episode (bounded: each attempt Terms honestly; no hot loop).
  **Pre-existing** (the lazy `kv.Read` path had identical semantics) and the same self-heal claim
  sits in the frozen §10.3 text ("logical delete ⇒ present-but-isDeleted ⇒ create") — a contract
  truth-drift, not a Fire 1–2 defect. Fixed here: the ddls.go comment now states the real behavior
  instead of re-asserting the broken claim. **Follow-up to file post-merge (component-maintenance
  row):** decide resurrect-vs-suppress for the logical-delete branch (e.g. revision-conditioned
  update on `isDeleted` docs) + stage the §10.3 truth reconciliation.
- **Fixed (cosmetic):** `fire`'s doc comment re-attached (planOptionalReads had been inserted
  between comment and func, orphaning the followUp/Nak paragraph); duplicate `optionalReads`
  absent-key now skips the second live GET (knownAbsent checked before KVGet).
- **Recorded, no code change (advisory surfaces, accepted):** (i) `recordCommitConflict` now also
  counts absorbed declared-dedup create races into the §3.2 lane-misassignment Health signal —
  signal dilution, not correctness; split the counter when the signal is next consumed. (ii) lint
  annotation window (8 lines, closest-wins) can silently inherit across an intervening call/function
  boundary — false-negative vector on an advisory-only lint; tighten (reset at intervening
  kv.* call) when the debt list is worked down. (iii) `fileMutates` is file-granular — coarse but
  conservative; same revisit point as (ii).

---

## 13. Sweep worklist — verified inventory + plan (2026-07-09)

**Provenance.** Andrew flagged that agents keep copying the class-(b) pattern despite the advisory
lint, and asked for a verified inventory + sweep plan. This section is the durable record; the sweep
board rows on both lanes ([verticals](../planning-artifacts/backlog/verticals.md) ·
[lattice](../planning-artifacts/backlog/lattice.md)) point here. The authoritative *list* is always
`go run ./scripts/lint-conventions.go` — line numbers below drift as files change; the dispositions
are what to keep.

**The accrual fact.** 55 warnings at Fire 2's landing (2026-07-06) → **65 today**; the +10 are all
`wellness-domain`, whose first commit is 2026-07-07 — **one day after the advisory lint landed**. The
warn-only gate demonstrably does not deter new debt (the `orchestration-base` ClaimTask reads predate
the lint, 2026-06-30 FR28 Fire 1 — pre-existing, not accrual). Interim mitigation shipped 2026-07-09:
a CLAUDE.md read-posture convention bullet all agents load.

**End-state (restating §2/§7 — unchanged).** Migrate/annotate every class-(b) site, then flip the
read-posture findings advisory→blocking (STRICT CI fails). `kv.Read` and `kv.Links` **stay in the
sandbox**: (c) config reads and (e) enumerations are sanctioned live classes, and the runtime lazy
fallback remains (enforcement is CI-static, per Contract #2 §2.5 "undeclared reads still execute").
Capability *removal* is not the ratified plan and would need a Contract #2 §2.5/§2.5.1 amendment; the
Edge design does not require it either — its gate (`contextHint.reads ⊆ mirror`, F4 for enumerations)
degrades undeclared ops to pending rather than depending on the capability being absent.

**Sequencing (three fires):**
1. **Verticals fire (★★★ · M).** The 44 vertical-package sites below + the declarations at their
   dispatchers (app FE envelopes in `cmd/{loftspace,clinic,wellness}-app/web/app.js` — wellness
   CreateBooking/CancelBooking already pass `reads`, the precedent — plus the Weaver renewal/listing
   directOps in `packages/lease-signing/renewal_targets.go` etc.).
2. **Platform fire (★★★ · S–M).** The 21 platform-package sites + dispatcher declarations; **add the
   lint's missing `(a) <declared-by>` annotation form** (the regex accepts only `(c|d|e)`, so sites
   that are ALREADY dispatcher-declared — wellness :810/:872, augur :470/:472,
   `external_params.go:54` — have no honest silencing path); annotate the missed (e) follow-up
   (`orchestration-base/ddls.go:441`); reword the editor-hook's "classify … when convenient" to name
   the coming block; add **production `contextHint.enumerations` declarations for the four (e)
   enumeration ops** — today only the MergeIdentity pipeline test declares one, so the Edge
   mirror-coverage gate has zero production data.
3. **The flip (lattice, one line, after 1+2).** Read-posture findings advisory→blocking. Deliberately
   NOT a baseline-ratchet (block-new-only) — both fires are expected to land within days at ★★★;
   ratchet machinery would outlive its usefulness. **Closing step (Andrew, 2026-07-09): un-block the
   Edge Lattice row** — Andrew moved Edge to near-term predicated on this sweep; when the flip ships,
   clear the Edge row's `🚧 seq:` and EDGE.1 (edge design §7 — the trusted-posture local mirror + sync
   loop; its PL.1/PL.2 co-build dependency already shipped) becomes the lane's next ratified pick.

**Verticals-fire progress.** Fire 1 (2026-07-09, `5263c2b`) migrated clinic-domain's 8 sites +
shipped the Gateway `optionalReads` wire path. Fire 2 (2026-07-09, `41e3bcf`) migrated
**wellness-domain's all 10 sites** (`ddls.go` annotations + `cmd/wellness-app/web/app.js`
reads/optionalReads + the `integration_test.go` dispatcher declarations — a `seatKeys`/
`wdSlotClaimKeys` helper pair mirrors clinic's `slotClaimKeys` grid-discretization idiom) and
**loftspace-domain's all 3 sites** (`SetListingStatus` + `AssignUnitOwner`/`RemoveUnitOwner`), and
**resolved hard case 4**: unit.listing economics take ONE posture across all touch points —
declarable everywhere (the key is always payload/row-derivable), split only by required (a) vs
absence-tolerant (d) per call semantics, never treated as (c) live config. That closed
`lease-signing/scripts.go`'s DecideLeaseApplication tenancy-stamp block (lines ~448/460/463, the
FIRST-approve `.tenancy`/`.listing` reads) and SetApplicantProfile's rent lookup (line ~591,
reclassified from the originally-proposed (c) to (d)), plus the **Weaver-dispatched**
`missing_listingLeased` directOp's `unit.listing` read — which needed a small, backward-compatible
extension to `internal/weaver/strategist.go`'s row-templating (`resolveReadKey`): a NEW
`row.<column>.<aspect>` form falls back to joining a resolved row column's key with a trailing
aspect segment (mirroring the Starlark `unit + ".listing"` idiom) when the exact `row.<column>`
lookup fails, tried only for `Reads` (Params/Target/Operation are unaffected) —
`TestBuildPlan_DirectOp_ReadsRowColumnAspectSuffix` pins it, and
`TestLeaseSigning_PlaybookColumnsMatchLens` was extended to accept the derived-suffix form (base
column must still be a lens BodyColumn). Fire 2 also fixed a **live bug** found while instrumenting
this exact path: `cmd/loftspace-app/web/app.js`'s `decideApplication` never sent `unit` in the
DecideLeaseApplication payload, so every FIRST landlord approval would have hard-failed
`InvalidArgument: unit: required` — fixed alongside the read declaration (the Go DDL test suite
already covered `unit` correctly; only the FE dispatcher had drifted).

**Fire 3 (verticals, this fire) closes lease-signing: all 19 sites** — `scripts.go`'s 7
(CreateLeaseApplication's duplicate-guard, DecideLeaseApplication's `.decision`/`.signature`,
Withdraw/SetApplicantProfile's validation links) + `renewal_scripts.go`'s 12 renewal-cycle sites
(OpenRenewal's `.tenancy`, SetRenewalTerms/CancelRenewal's `.renewalSignature`,
VerifyGuarantor/SignRenewal's `renews`/`applicationFor` links + `.profile`/`.terms`/
`.guarantorVerification`/`.tenancy`) — annotated + declared at every dispatcher
(`cmd/loftspace-app/web/app.js`'s `submitProfile`/`withdrawApplication`/`decideApplication`/
`submitComplete`, `renewal_targets.go`'s `leaseExpiryTarget` Weaver directOp via the
`row.entityKey.tenancy` template form, and the matching Go test `ContextHint` declarations in
`lease_signing_test.go`/`renewal_ops_test.go`). One caveat surfaced verifying live: a class-(a)
required validation-link read that is legitimately absent (a caller-supplied wrong unit/applicant/
leaseApp) now faults a raw platform `HydrationMiss` at hydrate, pre-empting the script's own nicer
structured rejection (`UnitMismatch`/`ApplicantMismatch`/`LeaseAppMismatch`) — the outcome (Rejected)
is unchanged, only the error shape degrades; this is the same tradeoff already accepted by Fire 1/2's
identical `validation link → (a), absence = caller error` classification (verified live on
`TestWithdrawLeaseApplication`'s wrong-applicant case, pre-existing before this fire), not a new
defect — noted here since the flip (sequencing item 3) will make this the PERMANENT error shape for
these callers.

**Residual found on ship (out of scope for this fire): clinic-domain has 5 sites Fire 1's commit
message claimed closed but did not migrate** — `ddls.go`'s `require_matching_provider`/
`require_matching_patient` (the `withProvider`/`forPatient` validation links, table rows
clinic:1455/1464) and three `appt.schedule` reads (Reschedule/terminal/Tombstone, table rows
clinic:1663/1760/1823) are still unannotated `kv.Read` (verified via
`STRICT=1 go run ./scripts/lint-conventions.go` post-Fire-3: 5 clinic-domain warnings, 0
lease-signing). Fire 1's "clinic-domain sweep 8/8" was inaccurate — only `864`/`1440`-area's two
class-(d) dedup sites landed. A subsequent Verticals fire closes these 5 (same `validation link → a`
/ `appt.schedule cell-release → a` pattern this fire's Withdraw/SignRenewal sites already mirror) —
line numbers drift, re-verify against the lint's live list, not the table.

The Platform annotation-form gap (§13 sequencing item 2) is ALREADY shipped (`d439919`,
platform-package sweep 21/21) — once the clinic-domain residual lands, only the flip (sequencing
item 3) is outstanding.

**Fire 4 (verticals) closes the clinic-domain residual — the vertical-package sweep (sequencing
item 1) is now COMPLETE.** All 5 sites annotated `(a)`: `require_matching_provider`/
`require_matching_patient`'s `withProvider`/`forPatient` link reads (declared by every caller's
dispatcher — RescheduleAppointment's `submitReschedule` and SetAppointmentStatus's `setStatus` in
`cmd/clinic-app/web/app.js`, both already declaring these keys in `reads`; TombstoneAppointment has
no FE dispatcher, so its declared-by is `integration_test.go`'s `clSubmit` calls, its only caller —
those 3 call sites (`dbtomb0001`/`tvtomb0001`/`tvtomb0002`) were under-declaring `reads` and are now
fixed to match) and the three `appt.schedule` cell-release reads (Reschedule/SetAppointmentStatus's
terminal branch/Tombstone). `STRICT=1 go run ./scripts/lint-conventions.go` now reports **0 warnings
repo-wide** — sequencing items 1 (verticals) and 2 (platform, `d439919`) are both done; **item 3 (the
advisory→blocking flip) is the only remaining step**, and per the closing-step note above, shipping
it also unblocks the Edge Lattice row.

**Proposed dispositions** (rollup: **38 (a) · 24 (d) · 1 (c) · 1 (e) · 1 chained**; the sweep fire
re-verifies each against the §3.1 fail-closed rule — required key → `reads`, never `optionalReads`):

| site | op / script | key read | class | note |
|---|---|---|---|---|
| augur:407/420/429 | ReviewProposal revalidate | `proposal.{proposed,confidence,gap}` | a | from externalRef; absence = error |
| augur:470/472 | CreateAugurReasoningClaim | `target_key` / `entity_key` | a | Weaver directOp already declares — annotate only |
| augur:522 | RecordProposal | `proposal.gap` | a | absence = wiring fault |
| augur:643/719 | ReviewProposal / RecordProposalDispatch | `proposal.review` | a | absence = error |
| cap-author:386 | RecordCapabilityProposal | `claim.target` | a | from externalRef |
| cap-author:393 | RecordCapabilityProposal | `proposal.request` | **chained** | key from :386's result — see hard case 1 |
| cap-author:525/613/635/643 | Review/MarkApplied | `proposal.{review,target}` + `pkg.manifest` | a | from payload ids |
| cap-author:794 | CreateAuthoringClaim | `subject_key` | a | from payload |
| clinic:864 | CreatePatient claim_identity | `identity.patientClaim` | d | read-before-create dedup |
| clinic:1440 | Create/Reschedule claim_cell | `hub.slot<cell>` | d | slot claim; absent→claim |
| clinic:1455/1464 | require_matching_{provider,patient} | `lnk withProvider/forPatient` | a | validation links, derivable |
| clinic:1663/1760/1823 | Reschedule / terminal / Tombstone | `appt.schedule` | a | required for cell release |
| clinic:1732 | SetAppointmentStatus | `appt.status` | d | absent = legit first-set |
| identity:511 | ProvisionConsumerIdentity | `target_actor_key` | d | read-before-create |
| identity:529 | ProvisionConsumerIdentity | `consumer_role_key` | a | pinned literal; absence = error |
| renewal:173/325 | OpenRenewal / SignRenewal | `app.tenancy` | a | renewable app always has tenancy |
| renewal:206/354 | SetTerms / Cancel | `renewal.renewalSignature` | d | absent = normal unsigned |
| renewal:246/252/299/305 | VerifyGuarantor / SignRenewal | `lnk renews / applicationFor` | a | validation links |
| renewal:260/314 | VerifyGuarantor / SignRenewal | `app.profile` | d | absent = no-guarantor branch |
| renewal:309/317 | SignRenewal | `renewal.{terms,guarantorVerification}` | d | ordering / conditional states |
| scripts:305/524 | Create/Withdraw LeaseApplication | `lnk appliedToUnit` guard | d | guard read-before-create/tombstone |
| scripts:395/416/448 | DecideLeaseApplication | `app.{decision,signature,tenancy}` | d | first-decision / unsigned / create-once |
| scripts:460/502/509/556 | Decide/Withdraw/SetProfile | `lnk appliesToUnit / applicationFor` | a | validation links |
| scripts:463 | DecideLeaseApplication | `unit.listing` | a | hard-fails NoListing — see hard case 4 |
| scripts:584 | SetApplicantProfile | `unit.listing` | c | author-stated config-live — see hard case 4 |
| loftspace ddls:408 | SetListingStatus | `unit.listing` | a | directOp declares `[unit]`; add `.listing` — hard case 4 |
| ownership:191/217 | Assign/RemoveUnitOwner | `lnk manages` | d | create/revoke idempotency branches |
| orch-base:430 | ClaimTask | `assigned` link (this actor) | d | self-claim idempotency |
| orch-base:441 | ClaimTask | `holdsRole` link | **e** | per-candidate follow-up of the :419 kv.Links — annotate |
| external_params:54 | externalTask resolve_subject_params | declared subject key | a | Loom already declares — annotate only |
| privacy:164 | ShredIdentityKey | `identity.piiKey` | d | absent = never-sensitive branch |
| wellness:579 | CreateSession claim_cell | `hub.slot<cell>` | d | slot claim |
| wellness:589/771 | Tombstone/Cancel validations | `lnk atStudio / forSession` | a | validation links |
| wellness:656 | TombstoneSession | `session.schedule` | a | cell release |
| wellness:790 | CreateBooking | `session.seat<n>` | d | first-free-seat claim |
| wellness:810/872 | Create/CancelBooking | `session.schedule` / `booking.status` | a | FE already declares — annotate only |
| wellness:824/833/835 | CreateBooking resident-rate | `leaseapp{,.tenancy}` + `lnk applicationFor` | d | absent → standard rate |

**Hard cases (resolve at build):**
1. **`capability-author/ddls.go:393` — chained.** `proposal_key` is resolved from the :386 read's
   result; the Bridge reply dispatcher knows only `externalRef`, so the key is *undeclarable*.
   Sanction with an explicit annotation + why (recommended), or restructure the claim indirection to
   carry `proposalKey` (breaks the opaque-externalRef choice — not recommended).
2. **`orchestration-base/ddls.go:441`** — the one (e) follow-up read missed when :416 was annotated;
   mirror `rbac-domain:214`.
3. **No-production-dispatcher ops** (ClaimTask, augur ReviewProposal, TombstoneAppointment,
   RemoveUnitOwner — test-only today): declarations land in the test envelopes now; the future
   UI/dispatcher inherits them when wired.
4. **Listing-read posture split**: `scripts.go:463` + `loftspace/ddls.go:408` hard-fail on a missing
   `unit.listing` (→ a) while `scripts.go:584` treats the same read as deliberately-live (c). Pick one
   posture for listing economics and apply it to all three.

**kv.Links status.** 4 production sites, all `(e)`-annotated: `orchestration-base/ddls.go:419`
(queuedFor out, epoch = task root) · `identity-hygiene/ddls.go:156` · `rbac-domain/ddls.go:210` (both
in, `epoch=none (accepted)`) · `lease-signing/leasedoc_scripts.go:91`. Dispatch-side `enumerations`
declared in production: **zero** (fire 2 closes this). The clinic kv.Links consumer no longer exists
(write-path slot claims, 2026-07-02).

**Dispatcher map (where the declarations go):** augur ops ← Weaver strategist directOps + Bridge
replyOp (`internal/bridge/augur_proposal.go`); capability-author ← Bridge replyOp + `cmd/lattice`
CLI + Loom externalTask; clinic/wellness/loftspace/lease-signing ops ← their app FEs
(`cmd/*-app/web/app.js`) + Weaver renewal/listing directOps (`packages/lease-signing/renewal_targets.go`);
ProvisionConsumerIdentity ← Gateway auto-provision (`internal/gateway/gateway.go`) + bootstrap;
ShredIdentityKey ← Loupe UI + Refractor keyshredded re-submit; externalTask instanceOps ← Loom
(engine-declared reads).

*(Ancillary, this session: Contract #10 §10.3's revive parenthetical — "lazy on-demand, not a
contextHint read" — reconciled to the shipped `optionalReads` dispatch; staged UNCOMMITTED for
Andrew.)*
