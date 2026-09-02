# Expiry is a fact on the check, not a clock in the lens

**Status: 📐 awaiting-Andrew (ratification)** · Designer fire 2026-09-01 · Stream 2 (Lattice) ·
board row: *[Refractor/Weaver] Expiry is a fact on the CHECK, not a clock in the lens — sweep every
`$now` lens*

> **Rewritten after its own adversarial pass returned five blockers against the first draft.**
> Everything below is the corrected design; the refuted draft is not preserved above a banner, because
> a superseded body is how a later fire builds the withdrawn shape. §14 records what was refuted —
> three of the five are the reason the recommendation is what it is.

---

## 0. For Andrew

### 0.1 Your ask, clause by clause

> *"the fired timer records `expiredAt` on the service instance; lenses count `completed AND
> expiredAt = null`; the application keeps its own lifecycle marker. 8 lens files."*

| Clause | Answer |
|---|---|
| **"the fired timer records `expiredAt`…"** | **Already true, and unread.** `MarkExpired` writes `vtx.<type>.<id>.freshnessExpiry = {expiredAt}` today; nothing anywhere reads it (§2.4). The missing half is the read. |
| **"…on the service instance"** | **Needs a mechanism the platform does not have.** The timer payload carries the lens's **anchor**, never the neighbour whose window lapsed (§2.5). The answer is to make the expiring entity its own anchor — but where the window lives on a neighbour that is a new lens *and* a new target, not a rename (§5.5). |
| **"lenses count `completed AND expiredAt = null`"** | **Adopted in substance, departed from in form.** A presence test reads "expired" forever after a re-arm; `expiredAt >= <the deadline the row is about>` self-corrects with no clear (§5.2). |
| **"the application keeps its own lifecycle marker"** | **Adopted, untouched.** |
| **"8 lens files"** | **Low.** 16 lens declarations across 11 files (§2.1). |

### 0.2 What I have to correct — in the row, and in my own first draft

**The row's harms.** *"No audit, no derivation"* is true for **one** lens (`leaseApplicationsRead`) and
false for the other fifteen: `auditEnrolment` refuses actor-aggregate lenses at `audit.go:955`
**before** the `$now` conjunct at `:989`, and the actor-aware derivation carries no `$now` conjunct at
all. *"A corpus rescan per event"* has no code behind it keyed on `$now`.

**My own first draft** then failed five ways (§14). Two changed the design, not the prose:

- **Its payoff was zero.** I exempted `freshUntil` from conversion, reasoning about its *purpose* ("an
  instruction to a timer, not a fact about the world") and never checking its *storage*. It is a
  **stored body column** (`BodyColumns: [… "freshUntil" …]`, `orchestration-base/lenses.go:76`), it
  sits inside `classifyDivergence`, and every lens computes it from the *same deadline* with the
  *complementary* comparison — `CASE WHEN deadline > $now THEN deadline ELSE null END` beside
  `(deadline <= $now) AS missing_x`. It flips at the **identical instant**. Converting the gap column
  and leaving `freshUntil` leaves the sweep diverging at exactly the same moments.
- **The marker cannot carry what I asked of it.** Timers are keyed `(targetId, entityId)`; the marker
  key is `entityKey + ".freshnessExpiry"` with **no target segment**, and `targetId` is documented
  *"provenance only — it is NOT used to construct any key"*. **`appointment` is the anchor of three
  targets today**, `task` of three, `booking` and `leaseapp` of two. One slot, three deadlines,
  unconditioned overwrite — the predicate reads another target's lapse. Shipping, not hypothetical.

### 0.3 The corrected shape, and the finding that makes it work

Fixing the first bullet fixes a worse, unnoticed problem for free:

**Convert `freshUntil` as well**, to `CASE WHEN <recorded lapse> THEN null ELSE deadline END`. Then a
deadline **already past at first projection** — a same-day appointment, a rescheduled-earlier booking, a
late-advanced visit series — projects `freshUntil = <that past instant>`, and `temporal.go:142-148` says:

> *"A past instant is published verbatim: nats-server stores an overdue `@at` and fires it immediately,
> which is correct level semantics (the deadline has passed, the freshness expiry should fire now)."*

The timer fires at once, the marker lands, the gap opens. **Without converting `freshUntil` the
conversion is not merely pointless — it silently breaks the short-notice path**, because today's
`freshUntil` nulls itself on a past deadline, no timer ever arms, no marker is ever written, and
`expiredAt >= deadline` is false forever: a reminder never sent, a no-show never swept. One edit closes
the payoff gap and the regression together.

### 0.4 The fork — scope

| | Scope | Cost | My read |
|---|---|---|---|
| **1. Do not do this** | — | 0 | Genuine option, priced first in §10. The harm is narrower than the row claimed: a healer whose verdict is uninterpretable on 13 lenses, at **`warning`** severity, no over-grant. |
| **2. The anchor-hosted majority** ✅ | 10 lenses whose deadline already sits on the anchor, plus a per-target marker key | **M** | The whole payoff for the lenses driving every reminder and past-due sweep. No new lens, no new target, no contract change. |
| **3. Everything, incl. the neighbour-hosted windows** | + `leaseApplicationComplete` and the two Postgres FE read models that **share its cypher fragment**, via a new instance-anchored lens + target | **L–XL** | The three share `readinessWithItems` deliberately, *"so a readiness-rule change lands in ONE place"*. Two have no timer and no marker path, so they cannot take the conversion at all; forking the fragment re-opens the divergence hazard it was written to close. |

**I recommend scope 2, with scope 3 filed as its own row** and the trigger *"scope 2 shipped and its
sweep verdict verified live"*. The neighbour-hosted family is a larger problem wearing the same words;
bundling it is what made my first draft unsizable.

### 0.5 Fork / contract check

- **Architectural fork: YES — §0.4, scope.**
- **Frozen contract: NONE, either scope.** §10.2's `freshUntil` clause stays true verbatim (the
  freshness rule stays in the cypher; the engine never computes the window). The marker's per-target
  keying is `orchestration-base`'s own aspect shape. The merge's read is Contract #2 §2.5 class (d)
  `optionalReads`, already sanctioned. Nothing is staged uncommitted. **This corrects my first draft,
  which implied scope 3 would need a contract edit; it does not — it needs lenses.**

---

## 1. The mechanism today

1. The lens projects `freshUntil` — computed **in the cypher** from a stored deadline and `$now`.
2. Weaver turns it into an `@at` keyed `<targetId>.<entityId>` (`temporal.go:88-160`).
3. The timer fires; `handleFiredTimer` submits `MarkExpired{entityKey, targetId, expiredAt}` (`temporal.go:290-305`).
4. `MarkExpired` writes `vtx.<type>.<id>.freshnessExpiry = {expiredAt}`, unconditioned (`mark_expired.go:196-215`).
5. The write bumps the KV revision → CDC → reprojection → the cypher re-runs **with a fresh `$now`** → the gap flips → Weaver dispatches.

The marker's own doc comment: *"The marker aspect is read by NOTHING the lens projects — it exists only
to trigger the anchor reprojection."* **The op's entire purpose is to deliver a clock tick as a write.**

---

## 2. Grounding ledger

Every count ships as its command and its result at `a3bccca5`. An independent census was briefed **to
falsify** the row's "8 lens files"; an independent adversarial pass was briefed to break the draft. Both
did.

### 2.1 C1 — 16 lens declarations, 11 files

Enumerated by the **declaration** (every `pkgmgr.LensSpec` and the cypher const it points at) across all
files under `packages/`, not by a `lenses.go` glob; doc-comment `$now` excluded.

| # | Lens | File:line | Consumer | Anchor |
|---|---|---|---|---|
| 1 | `cafeStaleTabSettlement` | `cafe-domain/lenses.go:358` | Weaver `directOp` ×2 | `tab` |
| 2 | `followUpReminders` | `clinic-reminders/followups.go:285` | Weaver `directOp` | **`appointment`** |
| 3 | `appointmentReminders` | `clinic-reminders/lenses.go:114` | Weaver `directOp` | **`appointment`** |
| 4 | `pastDueAppointments` | `clinic-reminders/pastdue.go:90` | Weaver `directOp` | **`appointment`** |
| 5 | `visitSeriesDue` | `clinic-reminders/visitseries.go:1092` | Weaver `directOp` | `visitseries` |
| 6 | `leaseApplicationComplete` | `lease-signing/lenses.go:739` | Weaver, every action type | **`leaseapp`** |
| 7 | `leaseApplicationsRead` | `lease-signing/lenses.go:1008` | `cmd/loftspace-app` directly | postgres, **plain** |
| 8 | `landlordLeaseApplicationsRead` | `lease-signing/lenses.go:1148` | `cmd/loftspace-app` directly | postgres, **plain + Secure** |
| 9 | `leaseExpiry` | `lease-signing/renewal_lenses.go:144` | Weaver `directOp` | **`leaseapp`** |
| 10 | `renewalComplete` | `lease-signing/renewal_lenses.go:215` | Weaver, `Mode: "planned"` | `renewal` |
| 11 | `unroutedTasks` | `orchestration-base/lenses.go:212` | Weaver `surface` | **`task`** |
| 12 | `staleAssignedTasks` | `orchestration-base/lenses.go:240` | Weaver `surface` | **`task`** |
| 13 | `capabilityEphemeral` | `orchestration-base/lenses.go:422` | **the Processor's auth step** | `identity`, capability-kv |
| 14 | `clauseSatisfaction` | `semantic-contracts/lenses.go:203` | Weaver `directOp` + `assignTask` | `clause` |
| 15 | `wellnessBookingReminders` | `wellness-reminders/lenses.go:80` | Weaver `directOp` | **`booking`** |
| 16 | `pastDueBookings` | `wellness-reminders/pastdue.go:96` | Weaver `directOp` | **`booking`** |

**Two names in my first draft do not exist in this corpus** and are struck: `myTasks` (its `$now` is a
doc comment introducing the *next* const — the precise error §2.1 warns about, committed by me one
section later) and `cafeLeaseWorkplaces` (a plain lens with no `$now`; the real one is
`cafeStaleTabSettlement`).

**Generated lenses contribute zero**, structurally: `generateProducerSpec`
(`internal/pkgmgr/anchorwalk.go:548-580`) emits only link/label/property patterns and a key-slice
`RETURN`; no `$`-parameter can enter a generated body. **Primordials contribute zero.**

### 2.2 C2 — multiple targets share an anchor type, and the marker has no target segment

```bash
grep -rn "AnchorType:" packages/*/[a-z]*.go | grep -v '_test.go'
```

| Anchor type | Targets projecting `freshUntil` on it | Distinct deadlines |
|---|---|---|
| **`appointment`** | `appointmentReminders`, `followUpReminders`, `pastDueAppointments` | `remindAt`, `followUpDate`, `endsAt` |
| **`task`** | `unroutedTasks`, `staleAssignedTasks` | two `expiresAt` readings |
| **`booking`** | `wellnessBookingReminders`, `pastDueBookings` | `remindAt`, `endsAt` |
| **`leaseapp`** | `leaseApplicationComplete`, `leaseExpiry` | `validUntil`, `renewalOpensAt` |

Timers are keyed per `(targetId, entityId)` — *"two targets projecting a freshness deadline for the same
entity hold independent timer slots"* (`temporal.go:16-22`). **The marker is not**: the key is
`entity_key + ".freshnessExpiry"` (`mark_expired.go:212`), written unconditioned, with `targetId`
*"provenance only"* (`:88`). **This is the finding that decides the design's shape** (§5.3).

### 2.3 C3 — the derivation acts on the actor-aggregate members

`anchor_hopindex_corpus_census_test.go` pins `hopIndexed` for them. (My first draft read this census
against a lens list containing two names carrying no `$now` — corrected in §2.1.)

### 2.4 C4 — nothing reads the marker

```bash
grep -rn "freshnessExpiry" packages/ internal/ cmd/ --include="*.go" | grep -v '_test.go'
```

Every hit outside `orchestration-base/mark_expired.go` is a doc comment. No cypher, no Go reader.

### 2.5 C5 — the timer payload carries the anchor

`temporal.go:290-296` sends `{entityKey: p.EntityKey, targetId, expiredAt: p.FireAt}` where `EntityKey`
is the row's **anchor**. No channel names the neighbour whose window lapsed.

### 2.6 C6 — `capabilityEphemeral` is the thesis already proven

The one `$now` lens with no `freshUntil`, no timer, no marker and no Weaver target. Its `$now` is a
`WHERE` inclusion filter; its consumer is `internal/processor/step3_auth_capability.go`, which
**re-checks the grant's own recorded `expiresAt` at authorization time** against an injectable clock
(`:349-358`, *"Expired — Contract #6 §6.6: `expiresAt > now`"*). A stale projected grant is therefore
**not** an over-grant: the authoritative clock read already lives in the operation.

That is this design's placement argument, already shipped, on the most security-sensitive lens in the
corpus — and it makes lens 13 the cheapest member: it drops its `$now` with **no fact to record**.

### 2.7 C7 — the severity claim, corrected

My draft said a recurring divergence escalates to `error`, citing `sweep.go:63-68`. That comment governs
the **capability** path. The business path is `evalLensSweep`, whose own doc states the difference
(`health/lattice_heartbeater.go:2105-2112`): *"and — the substantive difference — never escalates past
`warning`."* Escalation lives in the `CapabilityLensStatus` loop (`:1131-1137`); the
`LensLivenessStatus` path raises a hardcoded `severity: "warning"` (`:1742-1746`). Every lens writing
`weaver-targets` takes the warning path. **`capabilityEphemeral` is the only member for which the error
escalation is reachable — the one my draft's census had dropped.** Citing a doc comment as a ledger
fact, on the design's own severity argument, is the rule my skill states and I broke.

---

## 3. Why `$now` in a projection is the problem

A lens row is **stored**. A predicate over `$now` produces a value true at the instant of the write and
not re-derived until something writes again.

1. **The stored row is a claim about the past.** Between the deadline passing and the timer firing the
   read-model says "not due" while the world says "due". `MarkExpired` shrinks that window; it does not
   close it.
2. **`patternClosedOutput` is asserted for these lenses** (`projection/driver.go:502`) and means *"the
   row is a function solely of the subgraph its compiled pattern binds"*. For a `$now` lens that is
   false, and the assertion is made honest only by the poke manufacturing a CDC event at the flip
   instant. `MarkExpired` is structurally a **prosthesis for a false closure claim**.
3. **Nothing can verify the row** — §4.

`$now` is legitimate in an **operation** (executes once, at a real instant; output is a recorded fact —
§2.6) and in a **timer**. It is the *projection* that cannot hold it.

---

## 4. The harm — the sweep's verdict is a clock reading

The convergence sweep is the standing healer for every actor-aggregate lens. Its deep verify re-executes
the projection per anchor (`sweep.go:554` → `Reproject` → `executeFullForActorOnce`, which binds
`"now": time.Now().UTC()` fresh on every evaluation, `evaluate.go:659-663`) and classifies the result
against the stored row with only `projectedAt` excluded as volatile (`reproject.go:301`, `:361-375`).

Clock-derived columns — the gap flags **and `freshUntil`** — sit inside that comparison. So the sweep
cannot distinguish *"this projection is broken"* from *"time passed"*, in either direction: it heals at
every deadline crossing and reports a divergence, while a genuine defect on the same lens is camouflaged
by the same signal.

Two mechanisms are licensed on that healer:

| Mechanism | Licence | Site |
|---|---|---|
| affected-anchor derivation (`act`) | `p.sweeper != nil` — *"nothing would heal a missed row"* | `anchor_derivation_mode.go:205-207` |
| consumer-filter narrowing | `p.sweeper == nil ⇒ refuse` — *"must not also lose the accident"* | `rulestate.go:315-327` |

Both give up an incidental reprojection **in exchange for** that healer. On these lenses the exchange is
made against a healer that cannot form an opinion. **A soundness repair, not a performance one** — and
per §2.7 a `warning`-severity one on the business plane, not an outage. That is the honest size.

Two lenses are excluded even from the licence half: `leaseApplicationComplete` and `clauseSatisfaction`
bind unlabeled variables, so their label sets are non-exhaustive and they take the **broad** filter
regardless — there is no narrowing licence to make honest on those two.

---

## 5. The shape

### 5.1 Read the fact that is already written

```
-  ((a.schedule.data.remindAt <= $now) AND …) AS missing_reminder
+  ((a.freshnessExpiry.data.byTarget.<targetId> >= a.schedule.data.remindAt) AND …) AS missing_reminder
```

Both operands are stored graph data, so the row becomes a pure function of the subgraph — exactly what
`patternClosedOutput` already asserts.

**The null semantics are the ones the rewrite needs, verified rather than assumed.** `compareAny`
(`ruleengine/full/values.go:167-169`) returns **`false`** whenever either operand is nil, for every
ordering operator:

| State | Predicate | Result | Matches today? |
|---|---|---|---|
| no marker yet | `null >= remindAt` | **false** — not expired | ✅ as `remindAt <= $now` is false before the deadline |
| no deadline stored | `expiredAt >= null` | **false** | ✅ byte-identical to today (`clinic-reminders/lenses.go:104`) |

This matters because the engine's null handling is **not** uniform: `equalsAny` (`values.go:143-146`)
makes `null <> X` **true** — the documented `remindedFor <> startsAt` idiom three lines away in the same
cypher. `>=` is the only family that fails closed in both directions. An absent aspect binds nil rather
than dropping the row (`values.go:73-79`), so `EmptyBehavior: "delete"` is not triggered.

### 5.2 `expiredAt >= <deadline>`, not `expiredAt = null`

Your form is a **presence** test on a marker whose lifetime is *"PERMANENT and OVERWRITTEN in place on
every fire — never tombstoned"* (`mark_expired.go:52-61`), justified by *"the marker outliving a
converged entity is harmless (it is read by nothing)"*. **This design makes that sentence false**, and
it is rewritten in the increment that falsifies it.

With `= null` a re-armed entity carries a stale marker and reads "expired" forever; closing that needs a
clear on a path that has none, with its own ordering question against the re-arm. The comparison form
needs none: the marker records **which deadline fired**, the row asks *"has a timer fired at or after
the deadline I am about?"*, and a re-arm moves the deadline past the recorded instant **with no write at
all**.

### 5.3 The marker must be keyed per target

`appointment` carries three targets and one marker slot (§2.2). `MarkExpired` must record **per
target**, and it already receives `targetId` — today provenance-only.

The marker's `data` becomes `{expiredAt: <latest any target>, byTarget: {<targetId>: <instant>}}`, and
the lens reads `byTarget.<its own targetId>`. `expiredAt` is retained for operator legibility, not
compatibility (nothing reads it, §2.4).

This makes the write a **read-modify-write**: `MarkExpired` must hydrate the marker to merge, which it
does not do today (it hydrates only the entity root, `temporal.go:299`). The marker key goes into
`contextHint.optionalReads` — absence is a legitimate branch on the first fire — which is Contract #2
§2.5 class (d), already sanctioned, **no contract change**. The unconditioned-update posture stays: the
merge is per-target-key, so two targets firing concurrently can lose each other's *latest* under a race
but never write a *lower* value, and the monotone comparison makes a lost update **under-report**
expiry, which is fail-closed. **§12 owns that vector; it is the sharpest test in the plan.**

The alternative that avoids the read-modify-write — a per-target aspect localName
(`freshnessExpiry_<targetId>`) — is priced and rejected in §10.

### 5.4 `freshUntil` converts too — the edit that makes the design work

`freshUntil` becomes `CASE WHEN <recorded lapse for this target> THEN null ELSE <deadline> END`, with no
`$now`. Two blockers close together:

- **The payoff becomes real.** `freshUntil` is a stored body column inside `classifyDivergence`, and it
  flips at the same instant as the gap column; leaving it converted nothing (§0.2).
- **The past-deadline hole closes.** Today `freshUntil` nulls itself on an already-past deadline, so no
  timer arms and — once the gap column stops reading `$now` — the gap would **never open**. Under the
  converted form the deadline is projected verbatim and `temporal.go:142-148` fires an overdue `@at`
  **immediately, by explicit design**. The short-notice appointment, the rescheduled-earlier booking and
  the late-advanced visit series all recover on their own.

### 5.5 The neighbour-hosted family (scope 3 only)

`leaseApplicationComplete`, `leaseApplicationsRead` and `landlordLeaseApplicationsRead` share one cypher
fragment, `readinessWithItems` (`lease-signing/lenses.go:730`), carrying
`inst.outcome.data.validUntil > $now`. Its own comment says the sharing is deliberate — *"so a
readiness-rule change lands in ONE place instead of drifting between two hand-copied lenses"*. Two of
the three are Postgres FE read models with **no `freshUntil`, no target, no timer and no marker path**,
so they cannot take the conversion at all; forking the fragment re-opens the divergence hazard it closes.

The only coherent answer is scope 3: give the **background-check instance** its own convergence lens +
target with `freshUntil = outcome.validUntil`, so the instance records its own lapse and all three
readers read that fact. New lens, new target spec, own output descriptor, own package permissions —
which is why it is a separate scope and a separate row, not a bullet.

### 5.6 What is untouched

Every vertical's lifecycle aspect (`.status`, `.reminder`, `.outcome`, `.progress`); §10.2's `freshUntil`
contract meaning; the `@at` keying; the deterministic requestId; the anti-storm machinery.

---

## 6. State-lifetime table — the marker becomes load-bearing

| Boundary | Today (unread) | After |
|---|---|---|
| created | first fire, unconditioned update | first fire, **merge** into `byTarget` (`optionalReads`, absent on first fire) |
| updated | overwrite `expiredAt` | merge this target's entry; `expiredAt` keeps latest-any-target |
| **re-arm** | irrelevant | **self-corrects** — the recorded instant falls behind the new deadline; no write |
| **deadline moved EARLIER** | irrelevant | the recorded instant may now be **≥** the new deadline ⇒ reads expired. **Correct**: a timer did fire at or after it. §12 pins it. |
| **two targets on one entity** | irrelevant | isolated by `byTarget` — the defect §0.2 names |
| **concurrent fires, two targets** | irrelevant | the merge race can lose the *later* of two instants for one target; the next fire repairs it, and the monotone comparison makes a loss **under-report** expiry (fail-closed). §12. |
| entity tombstoned | survives, harmless | harmless — a soft-deleted vertex does not bind, so no row |
| entity revived, same deadline | survives | reads expired. **Correct** — the deadline did lapse. Pinned deliberately in §12. |
| replay / redelivery | requestId collapses on the Contract #4 tracker | unchanged |
| **`MarkExpired` REJECTED** | benign — any later CDC touch re-reads `$now` and the gap opens | **the gap stays shut** — the marker is the only evidence. See §8. |
| rebuild / truncate | Core-KV data, untouched | unchanged — **and the fact survives a rebuild where a `$now` read does not** |

---

## 7. Per-role treatment

The unit of work is the **`$now` role**, not the file: one lens carries three.

**Role (a) — freshness lapse** (`validUntil`, `chargeValidUntil`): `clauseSatisfaction` and
`renewalComplete` convert in place. `leaseApplicationComplete` + the two FE read models are **scope 3**
(§5.5).

**Role (b) — due date on the anchor** (`remindAt`, `endsAt`, `nextDueAt`, `followUpDate`, `expiresAt`,
`staleAt`, `renewalOpensAt`): `pastDueAppointments`, `pastDueBookings`, `visitSeriesDue`,
`followUpReminders`, `unroutedTasks`, `staleAssignedTasks`, `cafeStaleTabSettlement`, `leaseExpiry`, plus
the `remindAt` term of the two reminder lenses. One predicate **pair** each — gap column **and**
`freshUntil`.

`cafeStaleTabSettlement`'s `staleAt` reads syntactically like (a); its own comment calls it *"the
café-domain analog of pastDueAppointments"*. **The (a)/(b) split is semantic — where the instant lives
and who can move it — never syntactic.**

**Role (c) — the guard** (`startsAt > $now`; `appointmentReminders`, `wellnessBookingReminders`): *"never
remind for an appointment that has already started."* No timer arms at `startsAt`, and under §5.3 a
second deadline still has no schedule slot, so the guard cannot be recorded.

**It does not need to be — the guard belongs in the dispatched op, and the sibling already does it.**
`clinic-domain`'s `BookAppointment` and `RescheduleAppointment` compare against `op.submittedAt`
(`ddls.go:2701`, `:2910`) using the house idiom `time.rfc3339_utc(op.submittedAt)` (*"so a downstream
lexical compare is sound"*). Role (c) is a **precedent to follow, not a licence to invent** — my draft
proposed a novel guard *and* a new lint for an idiom shipping in ~15 packages, and that lint would have
failed on `BookAppointment` itself. Struck.

Two mechanical requirements, both of which my draft got wrong:

- **Read the deadline from state, not the payload.** `RecordAppointmentReminder` declares
  `ContextHint.Reads = [appointmentKey]` — the vertex **root** — so `.schedule.startsAt` is not
  hydrated. The op must add the `.schedule` aspect to its declared reads. Guarding on the *optional*
  `remindedFor` payload field instead **fails open** when a dispatch omits it.
- **Normalize.** Weaver sets `submittedAt` to RFC3339**Nano** (`actuator.go:97`); the deadline is
  whatever the vertical stored. A raw lexical compare mis-answers for the first second after the instant
  (`"…00Z"` sorts above `"…00.12Z"`). Use `time.rfc3339_utc`, as the sibling does.

---

## 8. What this design does not close

- **The poke stays.** Op volume unchanged. What changes is that the row it produces is checkable.
- **A rejected `MarkExpired` becomes a stuck gap.** `handleFiredTimer`'s own comment: *"A MarkExpired
  rejected at the Processor is not re-attempted — the freshness flip then waits for the next CDC touch
  of the entity."* Benign today; after the change there is nothing to re-derive, and a re-published timer
  for the same deadline derives the same requestId and collapses on the tracker. Reachable via the
  script's `NotFound` liveness fail on a momentarily-tombstoned parent. **This is a real new failure mode
  and the design's largest residual.** The candidate mitigation — the sweep's own re-projection
  re-derives `freshUntil`, re-arming the timer with a *new* fire instant and so a *new* requestId — is
  stated as a **hypothesis to verify in Increment 0**, not asserted.
- **First projection to first fire remains a window** for a target with no recorded lapse yet.
- **Two lenses have no narrowing licence to make honest** (§4).

---

## 9. Reconciliation

**"Didn't we already handle this?"** The temporal lane turned time into an op — that half shipped. The
consumption half did not: the op writes a marker that exists only to bump a revision. This is the second
half of a mechanism already present.

**"Does it contradict a pattern?"** No — it removes an exception to one, and follows two shipped
precedents (§2.6's Processor-side check; `BookAppointment`'s `op.submittedAt` guard) rather than
inventing.

**"New state?"** One new field on an existing aspect (`byTarget`), with §6's lifetime.

**In-flight check** (committed docs **and** the dirty tree): four uncommitted `docs/contracts/*` edits
belong to Processor/Loom/Weaver designs; none touches §10.2 or §10.4. The **`[Weaver] One per-target
issue budget`** design (📐 awaiting-Andrew) touches `temporal.go`'s sibling issue machinery, not the
lane. A parallel fire is editing `refractor-hub-walk-and-periodic-load-design.md`; this design touches
no Refractor code.

---

## 10. Alternatives

**Row 1 — do not do this.** The world unchanged: 16 lenses whose stored rows are claims about a past
instant, whose only healer cannot form a verdict, and two narrowings licensed on that healer. The case
against it is **narrower than the row claimed** — `warning` severity on the business plane, no outage,
no over-grant (§2.6). What it costs is that the platform cannot tell a broken projection from a moving
clock on the lenses driving every reminder and past-due sweep, and that `MarkExpired` stays a prosthesis
for an assertion that is not true. A defensible "not now"; not a defensible "never".

**2 — Exclude clock-derived columns from `classifyDivergence`.** Treat the symptom. Rejected: the
comparison would have to know which columns are clock-derived, which is not derivable without the
whole-lens `ReferencesParam` analysis that already refuses at lens granularity — and it would make the
sweep *blind* to real defects in those columns rather than able to judge them.

**3 — Per-target aspect localName (`freshnessExpiry_<targetId>`) instead of a `byTarget` map.** Avoids
§5.3's read-modify-write and its race entirely. Rejected: it mints an **unbounded aspect keyspace per
entity keyed by a Weaver config value**, so adding a target silently adds an aspect to every entity of
that type and no reader can enumerate them without knowing the target set. The merge's race is
fail-closed and self-repairing; an unbounded keyspace is neither. **Re-open this if §12's concurrent-fire
vector proves harder than it looks** — these two are the real trade, and I would rather the build
discovers that with the alternative written down than re-derive it.

**4 — Bundle the neighbour-hosted family (scope 3).** Rejected as scope, not as work: a new lens, a new
target, and a change to a surface `cmd/loftspace-app` reads directly. Bundled, it made my first draft
unsizable. Filed as its own row.

**5 — Let `MarkExpired` flip the application's `.status`.** Rejected against your own clause: the
application keeps its own lifecycle marker, and a lapsed freshness window is not an expired appointment.

**Running each rejection back against the recommendation:** alternative 2 was rejected for blinding the
sweep to a column class — the recommendation makes every column verifiable; alternative 3 was rejected
for an unbounded keyspace — the recommendation adds one field to one existing aspect; alternative 5 was
rejected for writing a vertical's lifecycle — the recommendation writes only the platform's marker.

---

## 11. Contract + document surface

- **`docs/contracts/*` — untouched, both scopes.** §10.2's `freshUntil` clause stays true. The marker's
  shape is `orchestration-base`'s own. The merge's read is Contract #2 §2.5 class (d) `optionalReads`.
- **`packages/orchestration-base/mark_expired.go`** — *"read by NOTHING"* and *"harmless… it is read by
  nothing"* become false and are rewritten in the increment that falsifies them.
- **No new lint.** My draft proposed an `op.submittedAt` gate; §7 shows the idiom is shipped in ~15
  packages including `BookAppointment`, so that gate would fail on the existing corpus and its "narrow
  licence" framing was wrong. What remains is a **doc line** in `docs/components/weaver.md` recording
  that a Weaver-dispatched op's `submittedAt` is the platform's dispatch instant — a fact, not a rule.
- **`docs/components/{refractor,weaver}.md`** — the temporal-lane narrative gains the fact-reading half.
- **Package version bumps** for every edited package + the mirroring `Version` constant.

---

## 12. Test strategy

Every test owned by a named increment.

- **Per-lens cypher units** — marker absent / behind / at / ahead of the deadline. **Mandatory
  vectors**: the **re-arm** (record, move the deadline forward, assert not-expired **with no clearing
  write**) — the whole argument of §5.2; and §6's **deadline-moved-earlier** and
  **revived-same-deadline** rows, asserted deliberately with the reason in the test name so a later
  reader does not "fix" them.
- **The past-deadline vector, per role-(b) lens** — a row whose deadline is already past at first
  projection; assert `freshUntil` carries the past instant, the timer arms, the gap opens. **This is the
  regression the first draft would have shipped**: it must FAIL on a version that converts only the gap
  column. Write it first and watch it go red.
- **Concurrent two-target fire** (§5.3/§6) — two targets on one `appointment` firing in the same window;
  assert both `byTarget` entries survive and that a lost update can only under-report.
  **Mutation-proven**: make the merge an overwrite and it must go red.
- **Sweep-verdict regression — the test that proves the payoff.** Two deep-verify passes straddling a
  deadline: on `main` the second classifies `divergenceContent`; after, `divergenceNone`. **Assert on the
  classification, not the row** — the row converges either way, which is what makes a row-only assertion
  a false pass. **And run it once with `freshUntil` left unconverted, asserting it still fails** — that
  is the evidence for §5.4 and it is cheap.
- **Role (c) op guard** — started appointment refused; future one accepted; non-Weaver actor refused
  *before* the clock is read; and `.schedule` present in the declared reads (the fail-open of §7).
- **Corpus census, same fire.** `grouping_reduction_corpus_census_test.go` **will move**:
  `clauseSatisfaction` (`:87`) and `leaseExpiry` (`:143`) compute their gap in a `RETURN` over scalars
  carried through an aggregating `WITH`, and reading the marker adds a scalar to that grouping key. Its
  header says a moved key *"forces a deliberate re-reading"* — so this is an assertion to move
  deliberately, with the reason recorded, **not a table to patch**. Re-run and pin `label_derivation`,
  `anchor_hopindex`, `actor_walk_scope`, `actor_onekey`, `branch_decomposition` ×2, `plain_scanroot`,
  `rel_projection` and `ruleengine/full/grouping_corpus_lens_test.go` — **nine censuses, not the five my
  draft named**. The label/relation/filter pins do **not** move (a `*ParameterRef` is an inert leaf,
  `labels.go:178-185`; `CoreKVVertexFilter`'s `vtx.<type>.>` already covers the marker aspect;
  `projectedFromRevisions` rejects 4-segment keys, `projection/freshness.go:70-75`) — **assert that, do
  not assume it.**
- **Gates**: `go build ./...` · `make vet` · `golangci-lint run ./...` · `make verify-kernel` · the
  edited packages' `verify-package-*` · every `scripts/lint-*.go` · `lint-package-version` · and the
  build-tagged harnesses these packages reach — `make test-lease-convergence`,
  `make test-augur-convergence`, `make test-unrouted-convergence` at minimum, since `unroutedTasks` and
  `staleAssignedTasks` are converted. `.github/workflows/ci.yml` is the authority.

---

## 13. Measurement and acceptance

| Signal | Where | Expected |
|---|---|---|
| `sweep: healed a divergent projection` on a quiet converged lens | refractor log | **stops** — only once **both** the gap column and `freshUntil` are converted (§5.4) |
| `SweepStatus.Divergences` over a lap with no graph change | health entry | **0** |
| Short-notice reminder (deadline past at creation) | e2e | **still fires** — the regression guard |
| Projected gap columns | `weaver-targets` | byte-identical at the same graph state and clock |
| `MarkExpired` op volume | op stream | **unchanged** (§8) |

**Premise to re-derive at Phase 0**: the sweep-divergence flapping is argued from code and has **not**
been observed live in this fire. Confirm it on the running stack before Increment 1 — a converged
reminder lens should show sweep heals at deadline crossings. If it does not, open `SweepStatus` and find
out why before building; that signal is the design's headline.

---

## 14. Adversarial pass — run, and it reshaped the design

Five blockers and six majors against the first draft. All were mine; three changed the design rather
than the prose.

1. **The payoff was zero.** I exempted `freshUntil`, reasoning about its *purpose* and never checking
   its *storage*. It is a body column inside `classifyDivergence`, computed from the same deadline with
   the complementary comparison, flipping at the identical instant. → §5.4.
2. **The conversion introduced a silent under-fire class.** A deadline already past at first projection
   nulls `freshUntil`, arms no timer, writes no marker — so with the gap column converted the gap would
   **never open**: a reminder never sent, a no-show never swept. My §6 table had no row for it. Fixed by
   the same edit as (1), because an overdue `@at` fires immediately by design. → §5.4, §12.
3. **The marker cannot carry what I asked of it.** One slot per entity; four anchor types carry two or
   three targets today. → §5.3.
4. **The severity argument was a doc comment governing a different path.** Business lenses never escalate
   past `warning`. → §2.7.
5. **My census named two lenses carrying no `$now`** (`myTasks`, `cafeLeaseWorkplaces`) and omitted two
   that do (`capabilityEphemeral`, `leaseExpiry`) — including the only one for which the error escalation
   is real. I wrote the doc-comment-vs-cypher warning into §2.1 and committed exactly that error one
   section later. → §2.1.
6. **The proposed lint would have failed on the shipped corpus**, and the "narrow licence" it protected
   is an idiom in ~15 packages. → §7, §11.
7. Plus: the shared `readinessWithItems` fragment (→ scope 3); the rejected-`MarkExpired` stuck-gap
   residual (→ §8); the grouping-reduction census that does move (→ §12); the guard's fail-open on an
   optional payload field and its RFC3339Nano normalization (→ §7).

**Claims that survived**: §4's mechanism links (deep verify → `Reproject` → fresh `$now` bind → columns
inside the comparison); the two retirements in §0.2; §2.4; the null semantics; and that
`ReferencedLabels`, the consumer filter, `projectedFromRevisions` and footprint validation are all
unmoved by reading a new aspect.

---

## 15. Decomposition for the Steward

**Increment 0 — confirm the premises** *(XS)*: §13's live check, and §8's stuck-gap mitigation
hypothesis. Gate on both.

**Increment 1 — the per-target marker** *(S–M)*: `MarkExpired` merges `byTarget` under `optionalReads`;
the `mark_expired.go` comment rewrite; the concurrent-fire test. **Ships alone, changes no lens**, and is
the prerequisite every conversion depends on. Posture-changing — it changes an op's write shape.

**Increment 2 — role (b), the anchor-hosted deadlines** *(M)*: the eight role-(b) lenses plus
`capabilityEphemeral` (which needs no marker at all, §2.6). **Both the gap column and `freshUntil`**, per
lens. Cypher units incl. the re-arm and past-deadline vectors; the sweep-verdict regression; the nine
corpus censuses. Posture-changing.

**Increment 3 — role (a) in place** *(S)*: `clauseSatisfaction`, `renewalComplete`.

**Increment 4 — role (c), the guard** *(S)*: the two reminder ops, following `BookAppointment`'s shipped
idiom; the `.schedule` read declaration; normalization.

**Not in scope — its own row**: the neighbour-hosted family (§5.5), trigger *"scope 2 shipped and its
sweep verdict verified live"*.

Review depth beyond the two posture-changing increments is the Steward's sizing.
