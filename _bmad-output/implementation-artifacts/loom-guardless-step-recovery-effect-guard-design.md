# Loom guardless-step recovery — the effect-guard (recovery-idempotency for guardless steps) — design

**Status:** 🗄️ **BACKUP — shelved by Andrew (2026-06-29), NOT for build.** Andrew declined to widen Loom's
Core-KV reads: *"I don't like Loom or any other non-Processor component reading from Core-KV. Guards are the only
exception (waiting for a better architectural approach) … I don't like that we keep piling on to it. Keep this
design as a backup but try to find a different way — even if we have to redesign Loom entirely (hopefully not).
Not urgent, don't rush."* The effect-guard adds a **new** Loom Core-KV read (effect-probing) — a widening of the
very guard-read exception he wants held/shrunk. **This design is retained as the fallback;** the active task is a
**non-Loom-read approach (Processor-side, or a Loom redesign)** — see **§9**. The §10.5/§10.6 contract edit was
**reverted from the working tree** (proposed text preserved in §3 + the For-Andrew block); not committed.
**Component:** Loom (`internal/loom`) · **Stream:** Lattice (Stream 2) · **Size:** S–M (Fire 1 buildable now; Fire 2 behind the ratified `kv.Links` substrate seam).
**Designer fire:** Winston, 2026-06-29 · **Builds on:** Contract #10 §10.5 (loom pattern step shape + the guard grammar) · §10.6 (Crash-safety invariant 2, "guardless steps complete only via their token") · the documented-bound doctrine (Contract #10 ~"documented rare-double") · the declarative guard evaluator (`internal/loom/guard.go` + `guard_eval.go`) · the §10.6 read-before-act probe precedent ("Loom reads Core KV, never writes it").
**Sibling precedent:** [`weaver-reclaim-check-before-act-probe-design.md`](weaver-reclaim-check-before-act-probe-design.md) (the higher-stakes per-sweep sibling — its *grounding methodology* is mirrored here, but its conclusion is **inverted**, see §2.1).
**Contract change (proposed, NOT applied):** Contract #10 §10.5 (additive optional `effectGuard` step field) + a §10.6 invariant-2 refinement. The proposed text is preserved in §3 + the For-Andrew block; the working-tree edit was **reverted** when the design was shelved (2026-06-29). A non-Loom-read redesign would likely need a *different* contract surface (or none).

---

## For Andrew

**What it does, in two lines.** A guardless Loom step that re-runs on **disaster recovery** (total `loom-state` loss → re-triggered pattern → a *new* `instanceId` → a *new* step token the Contract #4 dedup tracker can't collapse → a **second commit**) can now carry an optional **`effectGuard`** — a predicate, in the *same grammar as the existing guard*, that is **true once this step's own effect has landed in Core KV**. Before running a guardless step the engine evaluates the effectGuard; **true ⇒ skip-as-already-done (advance + alert), false/absent ⇒ run.** It turns "a guardless step always doubles on recovery" into "a guardless step whose effect is Core-KV-observable is recovery-idempotent, like a guarded step."

**The one fork (designed through, recommendation given) — NOT an architectural fork.** Whether the step token should instead be made **generation-independent** (key it on `(patternRef, subjectKey, cursor)` so the *existing* §4 tracker collapses the recovery re-run, no new field). **Recommendation: REJECT it — it is unsound** (it conflates a *legitimate* second episode of the same pattern on the same subject with a recovery generation, silently dropping real re-runs; the fresh `instanceId` per generation is load-bearing precisely to keep those distinct — §2.1). The effect-guard is the sound mechanism. The remaining decision is the **§10.5 contract addition** itself (below) + the **build sequencing** (Fire 1 now / Fire 2 behind the ratified `kv.Links` seam).

**The frozen-contract change (staged uncommitted, the diff is the proposal).** `docs/contracts/10-orchestration-surfaces.md`:
- **§10.5** — the step shape gains an **optional `effectGuard?`** field (same declarative grammar as `guard`; absent ⇒ today's behavior verbatim); a short "Effect guards" subsection states its authoring contract (*false until this step's effect lands, true after* — the dual of a precondition guard's *false-before / true-after*).
- **§10.6 invariant 2** — refined to split the worst case: a guardless step with **no Core-KV-observable effect** (a pure outbound side-effect, e.g. a notification) stays in the documented-bound doctrine (its run/skip is genuinely un-inferable); a guardless step **with** a Core-KV-observable effect may declare an `effectGuard` and become recovery-idempotent.
- Affected consumers: only Loom (`internal/loom` engine + pattern parse) and `internal/pkgmgr` (the `StepSpec` author surface). No other component reads the step shape. Backward-compatible (additive optional field).

**Honest value note (§4).** The *high-value* cases — a **userTask** (a task vertex created but the human hasn't acted) and an **externalTask** (a claim vertex minted but the reply hasn't landed) — need a **link-existence** effect-guard atom, which depends on the **already-ratified** `kv.Links` / `KVListKeysFilter` substrate seam (the [op-time bounded link-enumeration](op-time-bounded-link-enumeration-design.md) item, Fire 1 substrate half not yet built). **Fire 1 lands the mechanism + contract + engine seam on the lower-risk *aspect-predicate* path** (the systemOp case the §10.5 example itself illustrates) and is buildable today; **Fire 2 adds the link-existence atom** and unlocks the userTask/externalTask cases once that seam ships. Item importance is ★ (low urgency — fires only on *total* state loss, not a normal restart), so a two-fire readiness ladder is appropriate.

---

## 1. Problem & intent

### 1.1 The grounded failure (verbatim from the platform's own docs)

`docs/components/loom.md` ("Disaster recovery — guardless steps") and Contract #10 §10.6 (Crash-safety invariant 2) document the gap precisely:

> Total `loom-state` loss (**not** a normal restart) followed by a re-triggered `StartLoomPattern` re-runs every guardless step. A fresh instance (a **new `instanceId`**, since the lost cursor was the old one's key) replays guards from cursor 0: a guarded step whose guard is now false is correctly re-skipped, but **a guardless step has no guard-replay signal** — its run/skip can never be inferred from Core KV (§10.6 invariant 2) — so replay always **lands on and re-runs it**. Because each step's `requestId` derives from `(instanceId, cursor)` (`internal/loom/token.go:deriveID`), the re-run's `requestId` is gen-2's own, **distinct** from gen-1's already-committed one, so the Contract #4 `vtx.op.<requestId>` dedup tracker **cannot see across generations** → the guardless step's op **commits a second time**.

The repo confirms the mechanism end-to-end:
- `internal/loom/token.go:57` — `deriveID(namespace, instanceID, cursor)` hashes `(namespace + instanceID + ":" + cursor)`. The step token is **generation-specific** by `instanceID`.
- `internal/loom/engine.go:854,920,984` — `submitSystemOp` / `submitUserTask` / `submitExternalTask` all derive their op `requestId` (and the userTask `taskId`, the externalTask `instanceKey`) from `(inst.InstanceID, inst.Cursor)`.
- `internal/loom/engine.go:799–827` — `advanceToRunnableStep`: a step with `len(step.Guard) == 0` "always runs" (line 806–808); there is **no second signal** for whether it already ran.

This is the Contract #10 **documented-bound doctrine**: the duplicate is **bounded and operator-visible** (one extra commit per guardless step in the recovery window, never an unbounded loop). loom.md explicitly flags: *"A robust check-before-act variant is Phase-3 hardening."* That variant is this design.

### 1.2 Scope of the blast (why it is ★ low-urgency, but real)

- Fires **only** on **total `loom-state` loss** (the bucket is gone) **followed by a re-trigger** — *not* a normal Loom restart (a restart resumes from the durable cursor; the outbox relay + the §4 tracker make a normal restart exactly-once, Crash-safety invariant 1). So the trigger is a genuine disaster (KV wipe / restore-from-cold / cell rebuild), not routine.
- Mitigated today by **authoring guidance** (loom.md): *"give a guard to any step whose re-run is costly; a guarded step is recovery-idempotent by construction."* But that guidance only helps when the step's effect **flips its own precondition** (see §2.2) — and it is *guidance*, not a guarantee.
- The duplicate is bounded (≤ one extra commit per guardless step) and alerted-able, but for a **non-idempotent** guardless effect (mint a second task, charge a card a second time via an externalTask, send a duplicate notification) the cost is real.

### 1.3 Intent

Give an author a **declarative, optional, engine-enforced** way to make a guardless step **recovery-idempotent** — closing the bounded double for any step whose effect is observable in Core KV — **without** sacrificing the property that makes guardless steps necessary in the first place (a step that *must* run once per legitimate episode and whose run/skip is *not* inferable from a precondition guard).

---

## 2. The shape — the effect-guard

### 2.1 The fork I considered first, and rejected: a generation-independent token

**The tempting fix (mirrors F-004's version-independent NanoIDs):** make the step token deterministic *across recovery generations* by keying `deriveID` on `(patternRef, subjectKey, cursor)` instead of `(instanceId, cursor)`. Then gen-2's re-run derives the *same* `requestId` as gen-1's committed op, and the **existing** Contract #4 `vtx.op.<requestId>` tracker collapses it — no new field, no new read, no contract change. This is the same move that made F-004 package entity keys survive a version bump (`entityNanoID(name, tag)`).

**Why it is UNSOUND here — reject it.** The `instanceId` is generation-specific **by design**, and that is load-bearing. A `(patternRef, subjectKey)` pair can legitimately host **more than one real episode over time** — the same pattern can be re-triggered on the same subject for a genuinely new run (e.g. a second onboarding cycle, a re-application, a periodic review). A generation-independent token would **conflate a legitimate second episode with a recovery generation** and silently collapse the real second run onto the first's tracker — a **silent drop of legitimate work**, which is strictly worse than a bounded, alerted double. The fresh `instanceId` per `StartLoomPattern` (the start op's own `requestId`, `engine.go:387`) is exactly what keeps "a new episode" and "a recovered episode" distinct; the platform **cannot** tell them apart from the token alone (this is the literal content of §10.6 invariant 2). So the token must stay generation-specific, and the recovery double must be closed by a **separate effect-replay signal** — not by collapsing the token namespace.

> **Methodology note (mirroring the Weaver sibling, inverted conclusion).** The Weaver reclaim design ([`weaver-reclaim-check-before-act-probe-design.md`](weaver-reclaim-check-before-act-probe-design.md)) grounded its way *out* of a probe: the *correctness* double there was already closed by §10.3's `claimId` idempotency, leaving only churn, so the fix became a backoff, not a probe. I grounded the same way here and reached the **opposite** conclusion: the Loom correctness double is **genuinely open** — `instanceId`-keyed tokens defeat the §4 tracker across generations, and (unlike Weaver's `claimId`) there is no existing idempotency handle that survives a `loom-state` wipe. So here a check-before-act **is** the right mechanism. Same method, opposite finding — because the grounding facts differ.

### 2.2 The effect-guard is the inverted-skip twin of the precondition guard

A Loom **guard** (§10.5) is a pure predicate over the subject's current Core-KV state; on the **forward** path the engine **skips a step whose guard is FALSE** (precondition not yet met). An **effect-guard** is the *same grammar, same evaluator, inverted skip semantics*: the engine **skips a guardless step whose effect-guard is TRUE** (effect already present).

They answer **different questions** — this is the crux of why an effect-guard is not redundant with a precondition guard:

| | precondition `guard` | `effectGuard` |
|---|---|---|
| Question | "*should* this step run, per business state?" | "*has* this step already run?" |
| Skip when | predicate **false** | predicate **true** |
| Recovery-idempotent **only if** | the step's effect **flips its own precondition** | the step's effect is **Core-KV-observable** |

The gap a precondition guard cannot cover: **a step whose effect does NOT flip its own gating precondition.** Canonical example — a userTask *"collect signature"* whose natural precondition guard is `{equals: {path: subject.lease.data.status, value: "pending"}}`. Creating the task **does not sign the lease** → the precondition stays `pending` → on recovery the precondition guard **re-creates the task**. But the **task vertex's existence** *is* the step's effect-evidence. An `effectGuard` keyed on that effect ("an open signature task already scopedTo this lease exists") correctly skips. For steps whose effect *does* flip the precondition (e.g. `SetAddress`, where `{absent: subject.address.data.line1}` is both the natural precondition and recovery-idempotent), the precondition guard already suffices and an effect-guard adds nothing — and authors should keep using the precondition guard there.

### 2.3 The authoring contract (binding, the dual of the guard's)

An `effectGuard` declares **"this step's effect is present in Core KV."** Its binding authoring contract:

> The `effectGuard` MUST evaluate **false until this step's own effect lands** and **true after** — and it must test **this step's specific effect**, not an incidental aspect another step might also set.

This is the exact dual of the guard-replay idempotency property the doc already relies on ("a guarded step is recovery-idempotent by construction" — its precondition is false-before / true-after). The failure modes and their containment:
- **False-negative** (effect present, predicate misses) → the step re-runs → **today's behavior, no worse** (the bounded double).
- **False-positive** (predicate true before the step ever ran) → a step that should run is **silently skipped** — strictly worse than a double. **Contained three ways:** (1) the authoring contract above (test *your own* effect); (2) the effect-guard is evaluated on **every** guardless-step entry (generation-agnostic), so on a normal *first* run a false-positive predicate would skip the step the *very first time* — caught immediately in the pattern's own e2e, not only on the rare recovery path; (3) **every effect-guard skip emits an operator alert** (§2.5), so a production skip-as-already-done is never silent. The net posture mirrors the guard: a mis-authored guard already silently mis-skips, and the platform accepts that under the same "guards are author-declared pure data" model.

### 2.4 The grammar — one field, two atom families (the fire split)

`effectGuard` reuses the §10.5 guard grammar verbatim (`{absent}`, `{present}`, `{equals}`, composable with `{allOf|anyOf|not}`), parsed by the **existing** `parseGuard` and evaluated by the **existing** `evalGuard` (`internal/loom/guard.go` / `guard_eval.go`) — **zero new evaluation machinery**, same JIT subject hydration, same one-snapshot-per-key dedup, same tombstone-safe `absent` semantics.

The split by what the effect *is*:

- **Aspect-predicate atoms (Fire 1, buildable now).** The effect writes a **subject aspect** → `{present: subject.<aspect>.data.<field>}` (or `{absent}`/`{equals}`). Serves a **systemOp** whose effect writes aspect *X* while its natural precondition reads aspect *Y* (*X ≠ Y*) — the systemOp case the §10.5 example's guardless `SetAddress` illustrates. Buildable on `evalGuard` **today**, no new primitive.

- **Link-existence atom (Fire 2, behind the ratified `kv.Links` seam).** The effect is a **related vertex** (a task / a claim), not a subject aspect → a new atom `{linkPresent: {relation: <rel>, ...}}` that does a **bounded, fail-closed inbound-link enumeration** on the subject (`lnk.*.*.<rel>.<subjectType>.<subjectId>`) and is true iff a live link exists. This is exactly the **already-ratified** `kv.Links(hub, relation, "in", …)` / `substrate.KVListKeysFilter` primitive ([op-time bounded link-enumeration](op-time-bounded-link-enumeration-design.md), Andrew-ratified 2026-06-28). Serves the **userTask** (a task `scopedTo` the subject) and **externalTask** (a claim linked to the subject) cases — the high-value ones (§4). Loom already JIT-reads Core KV in `guard_eval.go` (mirroring the Refractor `resolveProperty`), so it can call the substrate enumeration directly once the seam ships — no module-boundary change (Loom imports `substrate/*` + stdlib only; the enumeration is a substrate call).

### 2.5 Where it slots in the engine (the minimal insertion)

In `advanceToRunnableStep` (`engine.go:799–827`), the guardless branch:

```
step := pattern.Steps[cursor]
if len(step.Guard) == 0 {
    // (NEW) effect-replay signal for guardless steps:
    if len(step.EffectGuard) != 0 {
        eg := parseGuard(step.EffectGuard)               // reuse parseGuard
        present := evalGuard(ctx, conn, coreKV, subject, eg)  // reuse evalGuard
        if present {
            // effect already landed → skip-as-already-done + ALERT, advance
            logger.Warn("loom: guardless step skipped — effect already present "+
                "(recovery-idempotent via effectGuard)", "instanceId", …, "cursor", cursor)
            cursor++
            continue
        }
    }
    return cursor, false, nil   // run (today's behavior when no effectGuard / effect absent)
}
```

- **Pure read.** The effect-guard is the same JIT Core-KV read a guard is — Loom **reads** Core KV, never writes it (the §10.6 read-before-act probe precedent: *"Loom reads, never writes Core KV"*). **P2 preserved** (no write; the step still commits via the Processor when it runs). **P5 untouched** (Core-KV read on the write/orchestration path, not a lens query; this is the sanctioned engine read path, identical to a guard).
- **Generation-agnostic.** The branch runs on **every** guardless-step entry, not "only on recovery" (the engine cannot know it is a recovery generation — that is invariant 2). On a normal first run the effect is absent → run. On recovery gen-1's effect is present → skip. No recovery-detection needed.
- **Alert.** Every skip-as-already-done emits a `Warn` (the documented-bound doctrine requires the recovery to be **operator-visible**) — the analog of the deadline-probe's *"completion recovered via deadline probe"* alert (§10.6).
- **Disjoint from the deadline/token machinery.** The effect-guard is consulted **only** in `advanceToRunnableStep` *before* a step is dispatched; the in-generation redelivery idempotency (the `token.<token>` pointer presence, the outbox relay, the deadline probe) is untouched — those handle the *same-generation* crash; the effect-guard handles the *cross-generation* re-run.

### 2.6 What stays in the documented-bound doctrine

A guardless step with **no Core-KV-observable effect** — a pure outbound side-effect (e.g. a `systemOp` that fires a notification with no Core-KV trace) — **cannot** declare a sound effect-guard (there is nothing to probe; this is the literal worst case of invariant 2). Those remain in the documented-bound doctrine, and the authoring guidance for them is unchanged: *make them idempotent, or give them a precondition guard if their precondition flips.* The effect-guard **narrows** the documented-bound set to exactly this residue; it does not pretend to eliminate it.

---

## 3. Contract surface (exactly which §§ change vs build-to)

| Doc / § | Change vs build-to | What |
|---|---|---|
| **Contract #10 §10.5** (loom pattern step shape + Guards) | **CHANGE — staged uncommitted** | Add optional `effectGuard?` to the step shape (`{ kind, operation, guard?, effectGuard? }` / the externalTask shape likewise). Add a short **"Effect guards"** subsection: same grammar as `guard`; inverted skip (skip when **true**); the authoring contract (false-until-effect-lands / true-after); the Fire-2 `linkPresent` atom forward-referenced to the ratified `kv.Links` seam. |
| **Contract #10 §10.6** invariant 2 | **CHANGE — staged uncommitted** | Refine: split the worst case — a guardless step with no Core-KV-observable effect stays token-only (documented-bound); a guardless step with a Core-KV-observable effect MAY declare an `effectGuard` and become recovery-idempotent. The "guardless steps complete only via their token" wording stays the default; the effect-guard is the named exception. |
| Contract #4 §4.3 (idempotency tracker) | **build-to** (read only) | Unchanged — the design explicitly does **not** touch the tracker (§2.1 rejected the generation-independent-token route that would have). |
| Contract #2 §2.5.1 (`kv.Links` / `KVListKeysFilter`) | **build-to** (Fire 2) | Already ratified + committed (2026-06-28). Fire 2's `linkPresent` atom is a *consumer* of this seam, not a change to it. |
| `internal/pkgmgr` `StepSpec` | build-to (code) | Add `EffectGuard map[string]any` (mirrors `Guard map[string]any`, `definition.go:247`); `lensSpecBody`-style emit into the step's `effectGuard` field, omitted when nil. |
| `internal/loom/pattern.go` `Step` | build-to (code) | Add `EffectGuard json.RawMessage \`json:"effectGuard,omitempty"\`` (mirrors `Guard`, line 31); validate it parses at pattern load (mirror line 223–224). |
| `docs/components/loom.md` | build-to (doc) | Rewrite "Disaster recovery — guardless steps" + the authoring guidance to describe the effect-guard; retire the *"check-before-act variant is Phase-3 hardening"* note (now built). |

**No architectural fork.** No Gateway / read-path-auth / Vault / multi-cell / HA-NATS surface is touched. The single fork (§2.1, generation-independent token) is a *mechanism* fork, designed through and rejected.

---

## 4. Honest value & sequencing assessment

- **Fire 1 (aspect-predicate effect-guard, buildable now)** lands the contract + the engine seam + the parse/validate surface, and closes the double for the **systemOp / aspect-writing** case (the §10.5 example's own `SetAddress` class). Its *standalone* value is **modest** — many systemOp-aspect cases are *already* covered by a precondition guard (when the effect flips its own precondition, §2.2), so Fire 1 only adds the *X ≠ Y* systemOp case. Its real job is to **land the mechanism + contract on the lowest-risk path** so Fire 2 is a pure additive atom.
- **Fire 2 (link-existence atom)** is the **prize**: it closes the double for **userTask** (a created-but-unworked task) and **externalTask** (a minted-but-unreplied claim) — the genuinely non-idempotent, costly guardless effects (a second task; a second external charge/check). It composes on the **already-ratified** `kv.Links` substrate seam, so it carries **no new fork** — it waits only on that seam's Fire 1 (substrate) landing.
- **Net:** because the item is ★ (fires only on *total* state loss), a two-fire readiness ladder is right-sized. If Andrew wants to maximize value-per-fire, Fire 1 + Fire 2 can be built back-to-back as soon as the `kv.Links` substrate half ships — Fire 1 has no other dependency.

---

## 5. Migration & test strategy

**Migration:** purely additive + backward-compatible. An absent `effectGuard` is byte-for-byte today's behavior (the `len(step.EffectGuard) != 0` gate). No existing pattern changes; no bootstrap version bump (no new primordial key — the field rides existing package data, picked up by F-004 in-place package refresh / fresh install). No data migration.

**Tests (Fire 1):**
- **Parse/validate** (reuse `parseGuard`): a well-formed `effectGuard` parses; a malformed one is rejected at pattern load (mirror `pattern.go:223–224`); the externalTask shape carries it too.
- **Eval table** (reuse the `guard_e2e_test.go` harness): `{present}`/`{absent}`/`{equals}`/composites over a real embedded-NATS subject, including the tombstone-safe `absent` semantics.
- **`advanceToRunnableStep` skip-on-effect-present**: a guardless step with `effectGuard` true → cursor advances past it (no submit, no token, no outbox); effect false/absent → runs; alert emitted on skip.
- **Disaster-recovery e2e** (the executable reproduction of the loom.md scenario): start a pattern with a guardless systemOp step carrying an `effectGuard` on its written aspect; let it commit; **wipe `loom-state`**; re-trigger `StartLoomPattern` (new `instanceId`); assert the guardless step is **skipped** (effect present) and the op commits **exactly once** (no second `vtx.op.<requestId>` for the step's effect). Contrast: the same pattern *without* an `effectGuard` still doubles (the documented-bound baseline) — pinning that the field, not a behavior change, is what closes it.

**Tests (Fire 2):** the `linkPresent` atom over the ratified `KVListKeysFilter` (a live inbound link → present; none → absent; bounded/paged; fail-closed on enumeration error → treat as absent ⇒ **run**, never skip-on-uncertainty); the userTask/externalTask disaster-recovery e2e (a task/claim minted gen-1 → recovery gen-2 skips the step, no second task/claim).

**Gates (both fires):** `go build ./...`, `make vet`, `golangci-lint run ./...`, STRICT `lint-conventions`, `go test -race ./internal/loom/... ./internal/pkgmgr/...`, the loom external/guard e2e suites, and `make verify-kernel` (CI; the change is orthogonal to the auth plane).

---

## 6. Risks & alternatives

**Risks:**
1. **False-positive effect-guard ⇒ silent skip** (§2.3) — the one genuinely-worse-than-today failure. Contained by the authoring contract + the first-run-also-skips early-catch + the mandatory skip alert. Same risk posture as a mis-authored precondition guard, which the platform already accepts.
2. **Fire-2 enumeration cost on a high-degree subject** — an inbound `kv.Links` enumeration on a subject with many inbound links of the relation. Bounded/paged by the ratified primitive; the effect-guard only needs *existence* (first live match short-circuits), and it runs once per guardless-step entry, not per message. Low.
3. **Over-adoption** — authors slapping an `effectGuard` on every step. Mitigated by docs: prefer a **precondition guard** when the effect flips its own precondition (the recovery-idempotent-by-construction path); reach for `effectGuard` only when the effect is observable but does *not* flip the precondition (userTask/externalTask, or the X ≠ Y systemOp).

**Alternatives considered:**
- **Generation-independent token (§2.1)** — rejected as unsound (conflates legitimate re-episodes with recovery).
- **Engine auto-derives effect-evidence per step kind** (userTask → auto-probe "task scopedTo subject exists", no author declaration) — rejected: it couples the **generic** engine to the task/claim schema, bakes a link-enumeration into the engine core, and removes author control (the whole §10.5 model is *author-declared pure data*; the engine is a generic interpreter). The declarative `effectGuard` keeps the engine generic and the author in control. (A future ergonomic *could* offer a per-kind default effect-guard as sugar — out of scope here.)
- **Do nothing (stay documented-bound)** — defensible given ★ urgency, but the loom.md note explicitly earmarked this as Phase-3 hardening, and the userTask/externalTask double (a second task / a second external charge) is a real cost the effect-guard cleanly removes.

---

## 7. Fire-by-fire decomposition (for the Lattice Steward)

Each fire independently shippable + green. **Build only after ✅ Andrew-ratified** (and the §10.5/§10.6 contract edit committed by Andrew).

**Fire 1 — declarative aspect effect-guard (buildable now; full review — touches the engine step loop).**
- `internal/loom/pattern.go`: `Step.EffectGuard json.RawMessage` + load-time parse-validate (mirror `Guard`).
- `internal/pkgmgr/definition.go`: `StepSpec.EffectGuard map[string]any` + emit into the step's `effectGuard` field (mirror `Guard`).
- `internal/loom/engine.go` `advanceToRunnableStep`: the guardless-branch effect-guard check (§2.5) — reuse `parseGuard`/`evalGuard`; skip-as-already-done + `Warn` alert on true.
- Tests per §5 (parse, eval, skip, disaster-recovery e2e for a systemOp/aspect-writing step).
- **Review:** full (the engine step loop is the cursor-rebuild safety core; the §10.6 invariant is load-bearing). Acceptance: the disaster-recovery e2e commits the guardless step exactly once; the no-effect-guard baseline still doubles.

**Fire 2 — link-existence effect-guard atom (sequenced behind the ratified `kv.Links` substrate seam).**
- Add the `{linkPresent: {relation, …}}` atom to the guard grammar (`internal/loom/guard.go`), evaluated via `substrate.KVListKeysFilter` (the ratified op-time link-enumeration seam) — bounded, paged, **fail-closed = absent** (uncertainty ⇒ run, never skip).
- userTask/externalTask disaster-recovery e2e (task/claim minted gen-1 → recovery gen-2 skips, no second vertex).
- **Review:** full (security/correctness-adjacent — a skip must never fire on enumeration uncertainty). Acceptance: a live inbound link ⇒ skip; an enumeration error ⇒ run (no silent skip).

**Fire 3 — doc sweep (optional, XS).**
- `docs/components/loom.md`: rewrite "Disaster recovery — guardless steps" + authoring guidance around the effect-guard; retire the "check-before-act is Phase-3 hardening" note.
- (The §10.5/§10.6 contract text is committed by Andrew at ratification; this fire only updates the component doc.)

---

## 8. Open questions — resolved (decide-don't-defer)

- **Generation-independent token vs effect-guard?** → effect-guard (§2.1; the token route is unsound).
- **Declarative author-declared vs engine auto-derived?** → declarative (§6; keeps the engine generic, the author in control).
- **One field or two (`effectGuard` per kind)?** → one field, one grammar, two atom families wired across two fires (§2.4).
- **Evaluated only on recovery, or always?** → always, on every guardless-step entry (§2.5; the engine can't detect recovery — invariant 2 — and "always" makes a false-positive catchable on the first run).
- **Fail-open or fail-closed on enumeration uncertainty (Fire 2)?** → fail-closed = **absent ⇒ run** (§5/§7; never skip on uncertainty — a re-run is the bounded baseline, a wrong skip is the unbounded harm).
- **Contract: new field or overload `guard`?** → a **distinct** `effectGuard` field (§2.2; same grammar, *opposite* skip semantics — overloading `guard` would make the skip direction ambiguous).

---

## 9. Shelved — the architectural redirection (Andrew, 2026-06-29)

At ratification Andrew declined the effect-guard because it **adds a new Loom Core-KV read**. His standing
position: *the Processor is the sole Core-KV reader/writer; Loom's **guard** evaluation is the one tolerated
exception — and even that is provisional ("waiting for a better architectural approach"). Do not pile more engine
Core-KV reads onto it.* The effect-guard, though it reuses `evalGuard`, answers a **new** question ("has this
step already run?") via a **new** read — a widening of the exception, not a free pass.

**Redirection (non-urgent — do not rush).** Find a recovery-idempotency mechanism for guardless steps that does
**not** add a Loom Core-KV read. Leading candidate (the architect's reflex this design should have followed):
**make the read Processor-side.**

- **Processor-side idempotency.** Loom dispatches the guardless step's op *unchanged in shape* but declares the
  step's **effect key(s)** in the op's `contextHint.reads`; the **Processor** JIT-hydrates them (as it already
  does for every op) and the op's DDL/condition **no-ops when its own effect is already present** — so the
  recovery re-run commits nothing. The Core-KV read lives in the Processor (P2-clean); Loom only *declares*,
  exactly as the ratified externalTask-params shape does (Loom declares `contextHint.reads`, the Processor
  hydrates, the instanceOp DDL resolves).
- **The hard part is unchanged and must be solved in the redesign:** distinguishing a *recovery generation* from
  a *legitimate new episode* of the same pattern on the same subject (§2.1 / §10.6 invariant 2). The effect-guard
  pushed that onto the author's "test your own effect" contract; a Processor-side version needs the equivalent
  discrimination expressed as an op condition — an episode-distinct, generation-independent **effect identity**
  the Processor can dedup on, distinct from the generation-specific `requestId`. This is the open design problem
  the next fire must crack; it is **not** dissolved by moving the read, only relocated to the correct layer.
- **Stretch (only if needed):** the same "engine declares, Processor hydrates" move could eventually retire the
  *guard* read exception too (the "better architectural approach" Andrew is waiting for) — guards evaluated
  against Processor-hydrated state rather than a Loom Core-KV read. That is a larger Loom change ("redesign Loom
  entirely (hopefully not)") and is out of scope unless the minimal recovery-idempotency redesign forces it.

**This design (the effect-guard) is the backup** if the Processor-side path proves unworkable. It is sound and
buildable; it is simply on the wrong side of the architectural line Andrew is holding.
