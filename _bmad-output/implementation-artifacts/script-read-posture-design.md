# Design — Script-read posture: declared reads as the norm, bounded enumeration, and Processor-side guarded operations

**Status: ✅ Andrew-ratified 2026-07-01** (reshaped over a ratification working session + two `bmad-party-mode` adversarial rounds — see §0). **Fires 1–2 contract surface (Contract #2 `optionalReads` + `enumerations` + read-posture) committed; the guard surface (Contract #2 `guard`/`correlationToken`, #3 `operation.guardSkipped`, #10 §10.5/§10.6) is staged at Fire 3 (build-deferred, and #03 currently carries an unrelated uncommitted edit).**
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
