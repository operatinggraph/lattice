# Expiry is a fact on the check, not a clock in the lens

**Status: ✅ RATIFIED by Andrew 2026-09-01 — §0.4 row 3: ONE fire, ordered Inc 1 (the per-target marker) → Inc 2 (the neighbour-hosted family: the background-check instance becomes its own anchor) → Inc 3 (the ten anchor-hosted declarations) → Inc 4 (the role-(c) guards). Size L. No frozen-contract edit. Winston's adjudications at ratification: one `freshnessExpiry` aspect carrying a `byTarget` map with `expiredAt` as its monotone maximum; the `>=` comparison over the presence test; `create`-when-absent / `update`-when-present, OCC-conditioned by the declared read; the engine-level 4-deep aspect pin lands in Inc 0 before any cypher converts; `capabilityEphemeral` and `cafeStaleTabSettlement` leave the design as their own rows (§15).** · Designer fire 2026-09-01 · Stream 2 (Lattice) ·
board row: *[Refractor/Weaver] Expiry is a fact on the CHECK, not a clock in the lens — sweep every
`$now` lens*

> **Rewritten after two adversarial passes against it.** Everything below is the corrected design; no
> refuted draft is preserved above a banner, because a superseded body is how a later fire builds the
> withdrawn shape. §14 records what each pass refuted — several of those findings are the reason the
> recommendation is what it is.

---

## 0. For Andrew

### 0.1 Your ask, clause by clause

Filed on the board as:

> *"the fired timer records `expiredAt` on the service instance's outcome; lenses count `completed AND
> expiredAt = null`; the application keeps its own lifecycle marker. Sweep the 8 `$now` lens files."*

and fixed in direction by your own sentence in chat:

> *"sweep all uses of $now in the lens files, prefer shape B (expiry belongs to the check, the
> application may have its own expiration - cancel after inactivity, or something like that)"*

| Clause | Answer |
|---|---|
| **"the fired timer records `expiredAt`…"** | **Already true, and read by one build-tagged harness only.** `MarkExpired` writes `vtx.<type>.<id>.freshnessExpiry = {expiredAt}` today; no cypher and no production Go reads it (§2.4). The missing half is the read *the lens* does. |
| **"…on the service instance's outcome"** | **The two words I dropped in my first reading are the expensive ones.** `inst.outcome.data.validUntil`, on the **background-check instance** — a neighbour of every lens that reads it, not the anchor. The timer payload carries the lens's **anchor**, never the neighbour whose window lapsed (§2.5), so honouring this clause literally costs a **new convergence lens + a new Weaver target** so the check becomes its own anchor and records its own lapse (§5.5). That is the whole neighbour-hosted family — four lenses, and the family where every *measured* payoff sits (§0.2). It is buildable in this fire; it is not free, and it is the family the increments put **second or later**, because the per-target marker (§5.3) is its prerequisite. |
| **"lenses count `completed AND expiredAt = null`"** | **Adopted in substance, departed from in form.** A presence test reads "expired" forever after a re-arm; `expiredAt >= <the deadline the row is about>` self-corrects with no clear (§5.2). |
| **"the application keeps its own lifecycle marker"** | **Adopted, untouched** — and it is the half of Shape B that costs nothing: no vertical's `.status` moves (§10, alternative 5). |
| **"sweep the 8 `$now` lens files"** | **Low by half.** 16 lens declarations across 11 files (§2.1). Fourteen of the sixteen are in scope; two leave the design with their own rows (§2.1, §15). |

### 0.2 What I have to correct — in the row, and in my own first draft

**The row's harms are all three real — and all three land on one lens.** *"No audit, no derivation, a
corpus rescan per neighbour event"* is true, jointly and by measurement, for **`leaseApplicationsRead`**;
it is false for the anchor-hosted majority, where `auditEnrolment` refuses actor-aggregate lenses at
`audit.go:955` **before** the `$now` conjunct at `:989`, and the actor-aware derivation carries no `$now`
conjunct at all.

The rescan half is not a rhetorical flourish, and it is not retired. `plainDerivationLicence` refuses the
per-anchor derivation on exactly this parameter —
*"it returns $now, which a per-anchor evaluation reproduces differently from the whole-corpus rescan it
replaces"* (`internal/refractor/pipeline/anchor_derivation_plain.go:332-339`) — and the licence's own doc
records the consequence: a refusal *"keeps today's unseeded whole-corpus rescan"* (`:346-352`). The sibling
design **observed it live on 2026-09-01**: after its Postgres `RowReader` landed,
`refractor-hub-walk-and-periodic-load-design.md` §5.2 records `leaseApplicationsRead` **still refused** —
the reader moved the refusal to the next conjunct, `$now` — and hands the `$now`-dependent Postgres rows to
this design. The board's own Done-log entry for that fire says the same in five words: *"pair's cost is the
`$now` rescan"* (`backlog/lattice.md:143`). `landlordLeaseApplicationsRead` stays refused **independently**
of `$now` and is not unblocked by this design: it is `Protected` + `DiffRetraction` with three
`SecureColumns` (`lease-signing/lenses.go:283-319`), and both the audit (`audit.go:963`, `:975`) and the
plain licence (`anchor_derivation_plain.go:329`) refuse on those grounds first.

**The consequence for how this design is sized, stated where it cannot be missed:** the anchor-hosted
conversion buys a **verifiable sweep verdict at `warning` severity** — real, and the reason the row exists
— but *every measured payoff* (audit enrolment, the plain per-anchor derivation, and the elimination of the
whole-corpus rescan on the Postgres pair) lives in the **neighbour-hosted family**. A fire that converts
only the anchor-hosted set ships the soundness repair and none of the measured cost reduction. §0.4 is
therefore a scope question about which family, not whether.

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
  targets today**, and `task`, `booking` and `leaseapp` of two each (`task`'s third target,
  `orphanedTaskGrants`, declares neither `freshUntil` nor `$now` —
  `orchestration-base/lenses.go:106` — so it holds no deadline and takes no marker). One slot, three
  deadlines, unconditioned overwrite — the predicate reads another target's lapse. Shipping, not
  hypothetical.

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

That makes the anchor-hosted conversion's payoff **non-zero, and still the `warning`-severity half**: a
sweep verdict that means something on ten declarations. The audit enrolment, the per-anchor derivation
and the whole-corpus rescan are all on the other side of the census (§0.2), which is why §0.4's fork is
about *which family first*, not about whether the sweep verdict is worth having.

### 0.4 The fork — scope

The increments the work decomposes into (§15) are fixed, and Increment 0's premises gate all of them.
The fork is **which of Increments 1–4 are one fire, and in what order**.

| | Scope | Cost | My read |
|---|---|---|---|
| **1. Do not do this** | — | 0 | A genuine option, priced first in §10. The harm is narrower than the row's wording suggests on the anchor-hosted side: a healer whose verdict is uninterpretable, at **`warning`** severity, no over-grant. On the neighbour-hosted side it is not narrow — the Postgres pair keeps its whole-corpus rescan, measured live (§0.2). |
| **2. The anchor-hosted set only** | Inc 1 (per-target marker) + Inc 3 (ten declarations) + Inc 4 (the role-(c) guards) | **M** | Ships the soundness repair for every lens driving a reminder or a past-due sweep, and **none** of the measured payoff. Leaves the `$now` rescan the sibling fire just handed us exactly where it is. |
| **3. Everything, as ONE fire** ✅ | Inc 1 per-target marker → **Inc 2 the neighbour-hosted family** (the background-check instance becomes its own anchor; the shared `readinessWithItems` fragment is **one** edit read by three lenses; `renewalComplete` joins them on the same instance class) → Inc 3 the ten anchor-hosted declarations → Inc 4 the role-(c) guards | **L** | The whole sweep Andrew asked for, with the lapse recorded on the check. Inc 1 is the prerequisite of both families, so it is paid once either way; the only mechanism still open for Inc 2 is a Phase-0 registry check (§5.5), not a design gap. |
| **4. Neighbour-hosted family first, anchor-hosted set later** | Inc 1 + Inc 2 now; Inc 3 + Inc 4 as a follow-on row | **M** | Buys the measured payoff first and defers the soundness repair. Defensible if the fire must be smaller than L — but it splits a sweep whose whole value to a reader is that no `$now` is left, and it pays Inc 1's review cost for two lenses. |

**Winston recommends row 3.** Andrew's directive was the *whole* sweep with the lapse recorded on the
check's own outcome — row 3 is the only row that answers both halves of that sentence. The measured
Refractor cost lives in the neighbour-hosted family (§0.2), so a fire that omits it ships no measurable
improvement; Inc 1 is common to every row, so splitting only duplicates its posture review; the
house rule is fewer, larger fires; and the one mechanism Inc 2 leaves open is a Phase-0 registry check
(does the registry accept a target whose playbook declares no gaps entry — §5.5), not an unresolved
design. Row 4 is the fallback if the fire must be capped at M.

**Decision (Andrew, 2026-09-01): row 3.** Presented in the ratify session as "Option 2 — everything as one fire, ordered marker first, then the lease-signing family, then the ten, then the reminder-op guards"; his reply: *"agreed - Option 2"*. Rows 2 and 4 stay recorded as the alternatives he declined.

### 0.5 Fork / contract check

- **Architectural fork: YES — §0.4, scope.**
- **Frozen contract: NONE, any row.** §10.2's `freshUntil` clause stays true verbatim (the freshness
  rule stays in the cypher; the engine never computes the window). The marker's per-target keying is
  `orchestration-base`'s own aspect shape. The merge's read is Contract #2 §2.5 class (d)
  `optionalReads`, already sanctioned. Nothing is staged uncommitted. The neighbour-hosted family
  needs **lenses, not a contract edit** — a new convergence lens, a new target spec, its own output
  descriptor and package permissions (§5.5).

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

**Scope membership — the sixteen split three ways, and the split is the design.**

| Family | Count | Members | Treatment |
|---|---|---|---|
| **Anchor-hosted** | **10 declarations** | `pastDueAppointments`, `pastDueBookings`, `visitSeriesDue`, `followUpReminders`, `unroutedTasks`, `staleAssignedTasks`, `leaseExpiry`, the `remindAt` term of `appointmentReminders` and of `wellnessBookingReminders` (role (b)); `clauseSatisfaction` (role (a)) | Convert in place, gap column **and** `freshUntil`. Inc 3. |
| **Neighbour-hosted** | **4 lenses** | `leaseApplicationComplete`, `leaseApplicationsRead`, `landlordLeaseApplicationsRead`, `renewalComplete` | The check becomes its own anchor; all four read its recorded lapse (§5.5). Inc 2. |
| **Leaves the design** | **2 lenses** | `cafeStaleTabSettlement` (a shipped café bug this design must not paper over — §7); `capabilityEphemeral` (a set-inclusion filter with no fact to record — §2.6) | Two rows filed, neither converted here. |

10 + 4 + 2 = 16. The membership test is **not** "where does the deadline live on the page" but *"is the
deadline a single scalar the pattern binds, so the marker can land on the anchor the timer already
names"* — §7 states it and shows why the wellness pair passes it while `renewalComplete` does not.

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
entity hold independent timer slots"* (`temporal.go:16-22`). **The marker is not**: the key is built as
`entity_key + ".freshnessExpiry"` (`mark_expired.go:208`) and the mutation writes the whole document
under it (`:216-220`), unconditioned, with `targetId` *"provenance only"* (`:88`). **This is the finding
that decides the design's shape** (§5.3).

### 2.3 C3 — the derivation acts on the actor-aggregate members

`anchor_hopindex_corpus_census_test.go` pins `hopIndexed` for them. (My first draft read this census
against a lens list containing two names carrying no `$now` — corrected in §2.1.)

### 2.4 C4 — one reader, and it is a build-tagged convergence harness

```bash
grep -rn "freshnessExpiry" packages/ internal/ cmd/ --include="*.go" | grep -v '_test.go'
```

Every hit outside `orchestration-base/mark_expired.go` is a doc comment: no cypher reads the marker, and
no production Go reads it. **The grep's `-v '_test.go'` is the whole error in the marker's own
justification, and in my first draft's.** Dropping the filter finds a reader that constrains the merge:

- `internal/leaseconvergence/harness_test.go:945-972` — `freshnessMarker(appKey)` does a `KVGet` on
  `<appKey>.freshnessExpiry` and decodes `data.expiredAt`, returning it with the aspect's KV revision;
- `internal/leaseconvergence/convergence_test.go:540-546` — asserts, per cycle, that the revision bumps
  **and** `gotExpiredAt > beforeExpiredAt`, as the *causal* proof that a new `MarkExpired` committed
  rather than an incidental CDC touch re-running the cypher.

It is `//go:build leaseshortwindow` (`make test-lease-convergence`), which is why `go test ./...` never
compiles it and why a whole-tree grep with the conventional test filter reports zero readers. So
`expiredAt` is retained for **compatibility with a shipped witness**, not for legibility, and §5.3's merge
inherits a hard constraint: `expiredAt` must remain the **monotone maximum over `byTarget`**, or that
harness's strict-advance assertion goes red for the wrong reason.

### 2.5 C5 — the timer payload carries the anchor

`temporal.go:290-296` sends `{entityKey: p.EntityKey, targetId, expiredAt: p.FireAt}` where `EntityKey`
is the row's **anchor**. No channel names the neighbour whose window lapsed.

### 2.6 C6 — `capabilityEphemeral` proves the placement argument, and is not convertible here

The one `$now` lens with no `freshUntil`, no timer, no marker and no Weaver target. Its consumer is
`internal/processor/step3_auth_capability.go`, which **re-checks the grant's own recorded `expiresAt` at
authorization time** against an injectable clock (`:349-358`, *"Expired — Contract #6 §6.6:
`expiresAt > now`"*; the contract's own row: *"Processor enforces `expiresAt > now` at lookup time"*,
`06-capability-kv.md:289`). A stale projected grant is therefore **not** an over-grant: the authoritative
clock read already lives in the operation.

**That is this design's placement argument, already shipped, on the most security-sensitive lens in the
corpus.** It is *not* an argument that this lens converts cheaply — and reading it that way is the error
this section now exists to prevent.

Its `$now` is a **set-inclusion filter**, not a gap column: all three arms of the grant fan-out are
`WHERE task.data.status = 'open' AND task.data.expiresAt > $now`
(`orchestration-base/lenses.go:427`, `:435`, `:448`), so `$now` decides **which tasks enter the
`ephemeralGrants` array at all**. There is no complementary `missing_*` column to convert and no
`freshUntil` to null out; dropping the conjunct with no replacement projects a grant for **every open
task past its `expiresAt`, indefinitely**. That population is not empty by construction and never drains:
an open task past its deadline persists by design — *"an open task the assignee has not completed past its
deadline is flagged, not auto-cancelled — CompleteTask stays legal"* (`:237`). `cap.ephemeral.<actor>`
would grow without bound on the auth plane — the one lens in the corpus where the sweep's escalation
reaches `error` (§2.7), and the one whose row size is read on every dispatch.

The fact this lens would need to read is a **recorded expiry on the task**, written by some other
target's marker — a question about a different entity's convergence than any increment here answers.
So `capabilityEphemeral` **leaves this design** and is filed as its own row (`📐 needs designer pass`).
The tempting reading — that a lens with no fact to record is the cheapest one to convert — is exactly
backwards: it is the one member whose conversion needs a design of its own.

### 2.7 C7 — the severity claim, corrected

My draft said a recurring divergence escalates to `error`, citing `sweep.go:63-68`. That comment governs
the **capability** path. The business path is `evalLensSweep`, whose own doc states the difference
(`health/lattice_heartbeater.go:2105-2112`): *"and — the substantive difference — never escalates past
`warning`."* Escalation lives in the `CapabilityLensStatus` loop (`:1131-1137`); the
`LensLivenessStatus` path raises a hardcoded `severity: "warning"` (`:1742-1746`). Every lens writing
`weaver-targets` takes the warning path. **`capabilityEphemeral` is the only lens in the census for which
the error escalation is reachable — the one my draft's census had dropped, and the one that leaves the
design (§2.6).** Citing a doc comment as a ledger fact, on the design's own severity argument, is the
rule my skill states and I broke.

The consequence for the fourteen that remain: **every severity this design improves is a `warning`.**

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
the projection per anchor (`sweep.go:554` → `Reproject` → `executeFullForActorOnce`,
`internal/refractor/pipeline/evaluate.go:751`, which takes `time.Now().UTC()` at `:752` and binds it as
the `"now"` parameter at `:759` — fresh on every evaluation) and classifies the result against the stored
row with only `projectedAt` excluded as volatile (`reproject.go:302`, `:361-375`).

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

**And it is the honest size of the anchor-hosted half only.** The two mechanisms above are *licences on a
healer*: making them honest is worth having, and it moves no measurement. The mechanisms that do move a
measurement — `auditEnrolment` admitting a lens, `plainDerivationLicence` seeding one affected anchor
instead of rescanning the corpus — refuse the **anchor-hosted** lenses on grounds this design does not
touch (actor-aggregate shape, `audit.go:955`), and refuse **`leaseApplicationsRead`** on `$now` alone
(`anchor_derivation_plain.go:332-339`), which this design does remove. So: **the anchor-hosted conversion
buys a verifiable sweep verdict at `warning` severity, and every measured payoff — audit enrolment, the
per-anchor derivation, the whole-corpus rescan on the Postgres pair — is in the neighbour-hosted family**
(§0.2, §5.5). Both are worth building; only one of them shows up in a number.

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
`patternClosedOutput` already asserts. The right-hand shape is **four navigation hops deep**
(`a.<aspect>.data.byTarget.<targetId>`), one deeper than anything the shipped corpus contains; the
engine resolves it, but that is an argument, not a pin, and §12 makes it one before any cypher converts.

**The null semantics are the ones the rewrite needs, verified rather than assumed.** `compareAny`
(`ruleengine/full/values.go:170-173`) returns **`false`** whenever either operand is nil, for every
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
converged entity is harmless (it is read by nothing)"*. **That justification is already false today** —
`internal/leaseconvergence`'s build-tagged harness reads `data.expiredAt` and asserts it advances (§2.4) —
and this design makes it false a second time, for the lens. The comment is rewritten in the increment
that falsifies it, and the harness's assertion becomes an acceptance criterion of that increment rather
than a casualty of it.

With `= null` a re-armed entity carries a stale marker and reads "expired" forever; closing that needs a
clear on a path that has none, with its own ordering question against the re-arm. The comparison form
needs none: the marker records **which deadline fired**, the row asks *"has a timer fired at or after
the deadline I am about?"*, and a re-arm moves the deadline past the recorded instant **with no write at
all**.

### 5.3 The marker must be keyed per target

`appointment` carries three targets and one marker slot (§2.2). `MarkExpired` must record **per
target**, and it already receives `targetId` — today provenance-only.

The marker's `data` becomes `{expiredAt: <latest any target>, byTarget: {<targetId>: <instant>}}`, and
the lens reads `byTarget.<its own targetId>`. **`expiredAt` is retained for compatibility, and its value
is constrained, not decorative**: it must be the **monotone maximum over `byTarget`**, because
`internal/leaseconvergence`'s harness asserts per cycle that it strictly advances (§2.4). A merge that
left `expiredAt` as "whatever this fire wrote" would let a second target's earlier instant move it
backwards and redden that harness.

This makes the write a **read-modify-write**: `MarkExpired` must hydrate the marker to merge, which it
does not do today (it hydrates only the entity root, `temporal.go:299`). The marker key goes into
`contextHint.optionalReads` — absence is a legitimate branch on the first fire — which is Contract #2
§2.5 class (d), already sanctioned, **no contract change**.

**Declaring that read is also what conditions the write, and the shipped comment's unconditioned posture
must go with it.** A declared `optionalReads` key is hydrated at step 4, and `applyHydratedRevisions`
(`internal/processor/commit_path.go:632-666`) implements Contract #3 §3.2's default: every `update`
mutation on a hydrated key with no explicit `expectedRevision` **gets one — the revision the key was read
at**. So the moment the marker is read, the merge stops being an unconditioned overwrite; a concurrent
second target's commit is a revision conflict, and `commitPipeline` absorbs it by re-hydrating and
re-executing the script rather than bouncing `RevisionConflict` to the client (`:301-307`). The merge is
therefore **serialized by construction**, not merely fail-closed under a race — and the current
justification for the unconditioned write (`mark_expired.go:30-38`) is rewritten in the same increment
as the *"read by nothing"* comments.

The residual is the **first** fire, and it is a different shape than a lost merge. With the marker
known-absent there is no revision to condition on; an unconditioned `update` would be the only
unserialized write in the op, and the mutation writes the **whole document** (`:216-220`), so a genuinely
lost write would **delete the sibling target's `byTarget` entry**, not "write a lower value". The
prescription follows the shape of the retry the platform already licenses: **the script branches — a
`create` when the marker is known-absent, an `update` when it is present.** `absentConditionedCreates`
(`commit_path.go:668-690`) licenses the in-process retry for exactly a `create` on a declared-absent key,
so a first-fire race resolves as the benign declared-dedup case: the loser re-hydrates, now sees the
marker present, and takes the `update` branch. The `create`-conflicts-on-the-second-lapse hazard the
shipped comment warns about does not return, because the `create` branch is taken only when step 4
recorded the key absent. **§12 pins both branches.**

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

### 5.5 The neighbour-hosted family — the check becomes its own anchor

**Four lenses, one window.** `leaseApplicationComplete`, `leaseApplicationsRead` and
`landlordLeaseApplicationsRead` share one cypher fragment, `readinessWithItems`
(`lease-signing/lenses.go:730`), carrying `inst.outcome.data.validUntil > $now`. Its own comment says the
sharing is deliberate — *"so a readiness-rule change lands in ONE place instead of drifting between two
hand-copied lenses"*. Two of the three are Postgres FE read models with **no `freshUntil`, no target, no
timer and no marker path**, so they cannot take a conversion on their own anchor at all; forking the
fragment re-opens the divergence hazard it closes. `renewalComplete` is the fourth: its
`max(CASE WHEN inst.class = 'service.backgroundCheck.instance' AND inst.outcome.data.status = 'completed'
AND inst.outcome.data.validUntil > $now …)` (`lease-signing/renewal_lenses.go:234`) is an **aggregate over
the same instance class**, so its window lives on the same neighbour even though its anchor is a renewal.

**The mechanism, stated.** Give the **background-check instance** its own convergence lens and Weaver
target, anchored on the instance, projecting `freshUntil = inst.outcome.data.validUntil`. The instance's
own timer arms; the fired `MarkExpired` carries the instance as `entityKey`, so the marker lands **on the
instance** — the entity whose window lapsed — which is precisely the shape §2.5 says the payload cannot
produce for a neighbour. The four readers then stop asking a clock and start reading that fact:

```
-  inst.outcome.data.validUntil > $now                                          -- "still fresh"
+  (inst.freshnessExpiry.data.byTarget.<bgcheckTargetId> >= inst.outcome.data.validUntil) = False
```

— **one** edit inside `readinessWithItems`, read by three lenses, and **one** edit to `renewalComplete`'s
`max` aggregate. The shared fragment stays shared; the divergence hazard it was written to close stays
closed.

**The polarity is the opposite of §5.1's, and that is not cosmetic.** The anchor-hosted lenses convert a
*gap* test, where `compareAny`'s nil-false is exactly the default the row needs (no marker ⇒ not expired).
This family converts a *freshness* test, where the same nil-false lands on the wrong side: writing it as
`inst.outcome.data.validUntil > <marker>` would read **not fresh** for every instance that has never
lapsed. So the recorded-lapse comparison must keep the `>=` operand order and be **negated**, and the
increment picks the negation form from the shipped corpus rather than inventing one — the same discipline
§7 applies to the role-(c) guard. The lapse/fresh vector pair belongs in this increment's cypher units
with the reason in the test name.

**The new target arms a timer even though it never dispatches.** Weaver's row handler calls
`scheduleFreshness` (`internal/weaver/evaluator.go:117` → `internal/weaver/temporal.go:97`) on **every**
delivery, before it reads any gap column and even for a disabled target — the doc calls it the lane-3
*"state-recording bookkeeping"* leg. So a target whose rows never open a gap still arms and re-arms its
`@at`. What that leaves open is a **registry** question, not a runtime one: whether a target spec whose
playbook declares **no gaps entry at all** installs and loads. The registry treats a `missing_*` column
with no `gaps[col]` entry as the `unplannable` escalation trigger (`internal/weaver/registry.go:47`) and
`reArmDeclines` answers a missing entry with a decline rather than a rejection
(`internal/weaver/control.go:572`) — which reads as permissive, but that is an inference from two
adjacent mechanisms. **Confirming it is this increment's Phase-0 check** (§13); the fallback if it
refuses is a `surface` gap the target declares and never opens.

The cost is honest: a new lens, a new target spec, its own output descriptor and its own package
permissions. What it buys is the only measured payoff in the design (§0.2, §4) and the literal reading of
Andrew's *"on the service instance's outcome"*.

**One consumer to carry forward.** `internal/leaseconvergence`'s harness reads the marker on the
**application** (`<appKey>.freshnessExpiry`, §2.4) as its causal witness. Moving the freshness window to
the instance must either leave `leaseApplicationComplete`'s own leaseapp-anchored timer arming as it does
today, or re-point that witness at the instance's marker — a decision this increment makes deliberately,
with the reason in the test name, never by letting the assertion quietly stop advancing.

### 5.6 What is untouched

Every vertical's lifecycle aspect (`.status`, `.reminder`, `.outcome`, `.progress`); §10.2's `freshUntil`
contract meaning; the `@at` keying; the deterministic requestId; the anti-storm machinery.

---

## 6. State-lifetime table — the marker becomes load-bearing

| Boundary | Today (unread) | After |
|---|---|---|
| created | first fire, unconditioned update | first fire, **`create`** on a marker step 4 recorded absent (`optionalReads`) — conditioned on that observed absence, so a losing racer retries and takes the update branch (`absentConditionedCreates`) |
| updated | overwrite `expiredAt` | **`update`, conditioned on the hydrated revision** (Contract #3 §3.2 default, applied because the key is now declared): merge this target's entry; `expiredAt` = the **monotone max over `byTarget`**, the constraint §2.4's harness imposes |
| **re-arm** | irrelevant | **self-corrects** — the recorded instant falls behind the new deadline; no write |
| **deadline moved EARLIER** | irrelevant | the recorded instant may now be **≥** the new deadline ⇒ reads expired. **Correct**: a timer did fire at or after it. §12 pins it. |
| **two targets on one entity** | irrelevant | isolated by `byTarget` — the defect §0.2 names |
| **concurrent fires, two targets** | irrelevant | **serialized, not merely fail-closed.** The declared read conditions the update on its hydrated revision, so the second writer conflicts and `commitPipeline` re-hydrates + re-executes against the merged document (`commit_path.go:301-307`). Neither `byTarget` entry is lost. On the **first** fire the two racers are `create`s on a declared-absent key: the loser's retry is licensed and lands as an update. §12 pins both. |
| entity tombstoned | survives, harmless | harmless — a soft-deleted vertex does not bind, so no row |
| entity revived, same deadline | survives | reads expired. **Correct** — the deadline did lapse. Pinned deliberately in §12. |
| replay / redelivery | requestId collapses on the Contract #4 tracker | unchanged |
| **`MarkExpired` REJECTED** | benign — any later CDC touch re-reads `$now` and the gap opens | **the gap stays shut** — the marker is the only evidence. See §8. |
| rebuild / truncate | Core-KV data, untouched | unchanged — **and the fact survives a rebuild where a `$now` read does not** |

---

## 7. Per-role treatment

The unit of work is the **`$now` role**, not the file: one lens carries three.

**The criterion that decides which family a role lands in.** Not *"the deadline sits on the anchor"* —
that phrasing is wrong and would have mis-sorted two lenses. The test is: **is the deadline a single
scalar the compiled pattern binds, so the marker can land on the anchor the timer already names?** The
wellness pair reads its deadline off the **session neighbour** (`se.schedule.data.remindAt`,
`wellness-reminders/lenses.go:87-88`; `se.schedule.data.endsAt`, `wellness-reminders/pastdue.go:103`) and
converts perfectly well, because `se` is bound once per row and the marker still lands on the booking.
What fails the test is an **aggregate over a set of neighbours** — `renewalComplete`'s
`max(CASE WHEN inst.class = 'service.backgroundCheck.instance' … )` — where "the deadline" is not a scalar
the anchor's own timer can be armed at. **The split is semantic, never syntactic.**

**Role (a) — freshness lapse** (`validUntil`, `chargeValidUntil`): `clauseSatisfaction` converts in place
— one anchor, one bound scalar. `leaseApplicationComplete`, the two FE read models **and
`renewalComplete`** are the neighbour-hosted family (§5.5): all four resolve their window off a
background-check instance, and `renewalComplete` reaches it through an aggregate, which is exactly the
shape the criterion rules out.

**Role (b) — due date the pattern binds** (`remindAt`, `endsAt`, `nextDueAt`, `followUpDate`, `expiresAt`,
`renewalOpensAt`): `pastDueAppointments`, `pastDueBookings`, `visitSeriesDue`, `followUpReminders`,
`unroutedTasks`, `staleAssignedTasks`, `leaseExpiry`, plus the `remindAt` term of the two reminder lenses.
One predicate **pair** each — gap column **and** `freshUntil`.

**`cafeStaleTabSettlement` leaves the design, because converting it would close a shipped gap forever.**
Its cypher computes `freshUntil` (`cafe-domain/lenses.go:366`) and its doc comment describes the intended
behaviour — *"freshUntil arms a one-shot @at at staleAt while the tab is still open"* (`:334`) — but
`StaleTabSettlementTarget`'s `BodyColumns` **omits `freshUntil`** (`:69`), and `BodyColumns` is a strict
whitelist: the projection driver iterates it and nothing else (`internal/refractor/projection/driver.go:70-87`),
so the column never reaches the row. Weaver's `scheduleFreshness` then takes its absent-column early
return (`internal/weaver/temporal.go:102-108`) and **no `@at` is ever armed**. Today that is survivable
because the gap column still reads `$now`, so any incidental CDC touch of the tab eventually opens
`missing_settle`. Convert the gap column onto a marker that is **never written**, and the auto-settle
never fires at all.

That is a **shipped café bug** — the auto-settle timer does not arm, and settlement waits on an incidental
reprojection — and it is not this design's to fix inside a platform sweep. **Filed as its own row on the
verticals board**; the conversion follows the fix, not the other way round.

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
- **A rejected `MarkExpired` waits for the next CDC touch — the same trigger as today, one hop longer
  (re-derived at Phase 0, 2026-09-02).** `handleFiredTimer`'s own comment: *"A MarkExpired rejected at
  the Processor is not re-attempted — the freshness flip then waits for the next CDC touch of the
  entity."* Reachable via the script's `NotFound` liveness fail on a momentarily-tombstoned parent. The
  first draft's mitigation — a re-projection re-arms with a *new* fire instant and so a *new* requestId —
  is **false**: the timer payload's `fireAt` is the deadline, not now, so a re-projected past deadline
  derives the **same** requestId by design (`temporal.go:135-143`). What actually recovers it is
  Contract #4 §4.4: a rejected operation lands **no tracker**, so the same requestId is not a duplicate
  and re-executes. After conversion `freshUntil` carries the deadline verbatim, so the next delivery of
  the row re-publishes the overdue `@at`, it fires at once, and `MarkExpired` runs again against the
  now-alive parent. The residual is therefore *"until the next CDC touch of the entity"* — unchanged from
  today's — not a permanently stuck gap.
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

**In-flight check** (committed docs **and** the dirty tree). The question is two-sided — *"does an
in-flight design collide with this one"* and, the half that is easy to skip, *"does an in-flight design
**depend** on this one"*.

- **Collisions: none.** Four uncommitted `docs/contracts/*` edits belong to Processor/Loom/Weaver
  designs; none touches §10.2 or §10.4. This design touches no Refractor code.
- **A dependency, in the other direction.** `refractor-hub-walk-and-periodic-load-design.md` §5.2 hands
  its unfinished business here explicitly: after its Postgres `RowReader` shipped, `leaseApplicationsRead`
  is still refused on `$now`, and it names the remaining cost as *"the freshness-marker plane's, not this
  mechanism's"*. That is this design's neighbour-hosted family (§0.2, §5.5), and it is why row 3 of §0.4
  is a **continuation of a fire that just landed** rather than a new front.
- **One in-flight interaction to note, not to fear.** The uncommitted `docs/contracts/10-orchestration-weaver.md`
  edit (the `[Weaver] One per-target issue budget` design, 📐 awaiting-Andrew) rewrites the `surface`
  action to raise **one** issue per (target, gap column) *"carrying the number of rows currently holding
  the column open"*. `unroutedTasks` and `staleAssignedTasks` are both `surface` targets and both convert
  here, so after both land that **count** becomes marker-latency-dependent: between a task's `expiresAt`
  and its `MarkExpired` commit the count reads one lower than a clock would say. It converges on the next
  fire, the severity is `warning`, and the count was already CDC-latency-dependent — worth stating,
  not worth sequencing around.

---

## 10. Alternatives

**Row 1 — do not do this.** The world unchanged: sixteen lens declarations whose stored rows are claims
about a past instant, whose only healer cannot form a verdict, and two narrowings licensed on that
healer. On the **anchor-hosted** side the case against it is narrower than the row's wording suggests —
`warning` severity on the business plane, no outage, no over-grant (§2.6) — and what it costs is that the
platform cannot tell a broken projection from a moving clock on the lenses driving every reminder and
past-due sweep, while `MarkExpired` stays a prosthesis for an assertion that is not true. On the
**neighbour-hosted** side it is not narrow at all: `leaseApplicationsRead` keeps its whole-corpus rescan,
measured live last week and handed here by the fire that measured it (§0.2). The two rows that leave the
design (§2.1) are filed either way. A defensible "not now" on the first half; not a defensible "never" on
the second.

**2 — Exclude clock-derived columns from `classifyDivergence`.** Treat the symptom. Rejected: the
comparison would have to know which columns are clock-derived, which is not derivable without the
whole-lens `ReferencesParam` analysis that already refuses at lens granularity — and it would make the
sweep *blind* to real defects in those columns rather than able to judge them.

**3 — Per-target aspect localName (`freshnessExpiry_<targetId>`) instead of a `byTarget` map.** Avoids
§5.3's read-modify-write entirely. Rejected: it mints an **unbounded aspect keyspace per entity keyed by
a Weaver config value**, so adding a target silently adds an aspect to every entity of that type and no
reader can enumerate them without knowing the target set. It would also strand `expiredAt` — the field
`internal/leaseconvergence`'s harness reads (§2.4) — with no document to live in. And the cost it avoids
is smaller than it looks: declaring the marker read is what makes the update OCC-conditioned, so the
merge is **serialized by the platform's own §3.2 default**, not merely fail-closed under a race (§5.3).
**Re-open this only if §12 finds the script cannot branch `create`-when-known-absent from
`update`-when-present** — that branch, not the concurrent-fire race, is the whole cost of the map form.

**4 — Split the neighbour-hosted family out and ship the anchor-hosted set alone** (§0.4 row 2). This is
the shape my first draft recommended, and it is the wrong one. Rejected: every measured payoff is in the
family it defers (§0.2, §4), so the smaller fire ships a soundness repair with no number attached; the
per-target marker (Inc 1) is the prerequisite of both families, so splitting pays its posture review
twice; and the sibling fire has already handed this family over with the measurement attached (§9). The
honest reason to take row 2 anyway is fire size, which is Andrew's to weigh, not mine — §0.4.

**5 — Let `MarkExpired` flip the application's `.status`.** Rejected against your own clause: the
application keeps its own lifecycle marker, and a lapsed freshness window is not an expired appointment.

**Running each rejection back against the recommendation:** alternative 2 was rejected for blinding the
sweep to a column class — the recommendation makes every column verifiable; alternative 3 was rejected
for an unbounded keyspace — the recommendation adds one field to one existing aspect; alternative 4 was
rejected for shipping a payoff with no number — the recommendation's Inc 2 is the increment that carries
the number, and it is ordered second, immediately behind its one prerequisite; alternative 5 was rejected
for writing a vertical's lifecycle — the recommendation writes only the platform's marker.

---

## 11. Contract + document surface

- **`docs/contracts/*` — untouched, every scope row.** §10.2's `freshUntil` clause stays true. The
  marker's shape is `orchestration-base`'s own. The merge's read is Contract #2 §2.5 class (d)
  `optionalReads`.
- **`packages/orchestration-base/mark_expired.go`** — three shipped justifications become false and are
  rewritten in the increment that falsifies them: *"read by NOTHING"* and *"harmless… it is read by
  nothing"* (§2.4 shows the second was already false), and the **unconditioned-update rationale** at
  `:30-38`, which the declared `optionalReads` read supersedes (§5.3).
- **An observable column-meaning change, with no renderer today.** After conversion `freshUntil` carries
  the deadline **verbatim** — so between the deadline passing and the marker landing it holds a **past**
  instant, where today it holds `null`. That is a real change to what the column means to a consumer, and
  it is deliberate (§5.4: an overdue `@at` is what closes the short-notice hole). The census of readers
  is small and none renders it: `cmd/loftspace-app/applicationsource.go:75` decodes `freshUntil` into its
  row struct and never displays it; Loupe has no reader. Recorded here so a future FE that surfaces the
  column knows a past instant is meaningful, not stale.
- **No new lint.** My draft proposed an `op.submittedAt` gate; §7 shows the idiom is shipped in ~15
  packages including `BookAppointment`, so that gate would fail on the existing corpus and its "narrow
  licence" framing was wrong. What remains is a **doc line** in `docs/components/weaver.md` recording
  that a Weaver-dispatched op's `submittedAt` is the platform's dispatch instant — a fact, not a rule.
- **`docs/components/{refractor,weaver}.md`** — the temporal-lane narrative gains the fact-reading half.
- **Package version bumps** for every edited package + the mirroring `Version` constant.

---

## 12. Test strategy

Every test owned by a named increment.

- **The engine-level expressibility pin (Increment 0/1, before any cypher converts).** The read this
  design puts in every lens — `a.<aspect>.data.byTarget.<targetId>` — is **four navigation hops deep**,
  and **the corpus has no shipped precedent for it.** The mechanism is confirmed at the evaluator: a name
  absent from a vertex's root body resolves as an **aspect point-read**, after which the aspect body is a
  plain map and every further hop is ordinary map navigation
  (`ruleengine/full/values.go:11-22` states exactly that; `resolveProperty` `:23-82`, `propertyOf`
  `:86-105`), and the grammar puts no bound on the lookup chain
  (`oC_Atom ( SP? oC_PropertyLookup )*`, `ruleengine/full/cypher/Cypher.g4:343`). But the deepest shape
  the shipped corpus contains is `x.<aspect>.data.<leaf>` — and the two shapes that *are* pinned were
  pinned in `ruleengine/full/aspect_expression_shapes_test.go` **after a live bug** whose two candidate
  causes could not be separated without exactly such a test. **Add the sibling case there for the 4-deep
  map read first.** An argument from the resolver is not a pin, and this is the one place in the design
  where the difference has already cost something once.
- **The OCC behaviour Increment 1 rests on (Increment 0).** Confirm on the running stack that a
  present-marker revision conflict is **retry-absorbed** rather than surfaced — `commitPipeline`'s doc
  says it re-hydrates and re-executes rather than bouncing `RevisionConflict`
  (`internal/processor/commit_path.go:301-307`); §5.3's serialization claim is that sentence and nothing
  else, so it is checked before it is built on.
- **Per-lens cypher units** — marker absent / behind / at / ahead of the deadline. **Mandatory
  vectors**: the **re-arm** (record, move the deadline forward, assert not-expired **with no clearing
  write**) — the whole argument of §5.2; and §6's **deadline-moved-earlier** and
  **revived-same-deadline** rows, asserted deliberately with the reason in the test name so a later
  reader does not "fix" them.
- **The past-deadline vector, per role-(b) lens** — a row whose deadline is already past at first
  projection; assert `freshUntil` carries the past instant, the timer arms, the gap opens. **This is the
  regression the first draft would have shipped**: it must FAIL on a version that converts only the gap
  column. Write it first and watch it go red.
- **Concurrent two-target fire** (§5.3/§6) — two targets on one `appointment` firing in the same window.
  **Both branches pinned, separately**: (i) *first fire* — both racers see the marker known-absent and
  both emit `create`; assert the loser's retry lands as an `update` and both `byTarget` entries survive;
  (ii) *steady state* — the marker is present, so both emit an `update` conditioned on the hydrated
  revision; assert the conflict is retry-absorbed and neither entry is lost. Assert in both that
  `expiredAt` equals the **max** over `byTarget` and never moves backwards.
  **Mutation-proven**: make the merge a whole-document overwrite and both must go red — the overwrite
  *deletes* the sibling target's entry, so a test that only checks "no lower value" would pass.
- **The shipped witness stays green, deliberately** (Increment 1's acceptance criterion).
  `make test-lease-convergence` exercises `internal/leaseconvergence`'s strict-advance assertion on
  `<appKey>.freshnessExpiry`'s `data.expiredAt` (§2.4). It is not incidental coverage: it is the reason
  `expiredAt` survives the merge as a monotone maximum, and Increment 1 is not done until it passes
  unmodified.
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
  `rel_projection`, `ruleengine/full/grouping_corpus_lens_test.go` — **and the two my second pass found
  missing**: `internal/refractor/auth_plane_narrowing_census_test.go`, which names `capabilityEphemeral`
  in a fixed list of auth-plane lenses that must stay broad (`:266-270`), and
  `internal/refractor/multiwalk_footprint_reachability_census_test.go`, whose header says its pin
  *"fails the moment either one moves"* rather than passing vacuously. **Eleven censuses — not the nine I
  named, and not the five my first draft named.** The label/relation/filter pins do **not** move (a
  `*ParameterRef` is an inert leaf,
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
| `leaseApplicationsRead`'s derivation refusal | refractor log | **gone** — the `$now` conjunct is the last one refusing it (§0.2); this is the measured payoff, and Increment 2 is not done without it |
| `AuditPlan` enrolment for the Postgres pair | audit log | `leaseApplicationsRead` **enrols**; `landlordLeaseApplicationsRead` **still refuses**, on Secure + diff-retraction (§0.2) — assert both, so an enrolment that should not have happened is caught |
| Per-message cost on the Postgres pair | msg/s at steady state | **falls off the corpus-rescan floor** the sibling fire measured on 2026-09-01 |

**Premises to re-derive at Phase 0**, each gating the increment that rests on it:

1. **The sweep-divergence flapping** is argued from code and has **not** been observed live in this fire.
   Confirm it on the running stack before Increment 1 — a converged reminder lens should show sweep heals
   at deadline crossings. If it does not, open `SweepStatus` and find out why before building; that
   signal is the design's headline.
2. **§8's stuck-gap mitigation** — re-derived 2026-09-02: the new-fire-instant hypothesis is false, and the
   gap recovers by the tracker's no-entry-on-rejection rule instead (§8, §16.1).
3. **The registry accepts a target whose playbook declares no gaps entry** (§5.5). Inferred from two
   adjacent mechanisms, not read off an install path. Gates Increment 2; the fallback is a declared
   `surface` gap the target never opens.
4. **A present-marker revision conflict is retry-absorbed** (§12) — the single sentence §5.3's
   serialization claim rests on.

---

## 14. Adversarial passes — both reshaped the design

Two passes have been run against this document. Every finding in both was mine.

### First pass — against the first draft

Five blockers and six majors. Three changed the design rather than the prose.

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
7. Plus: the shared `readinessWithItems` fragment (→ §5.5); the rejected-`MarkExpired` stuck-gap
   residual (→ §8); the grouping-reduction census that does move (→ §12); the guard's fail-open on an
   optional payload field and its RFC3339Nano normalization (→ §7).

### Second pass — against the rewrite

Eleven findings. Four of them moved a lens between families or out of the design; two inverted an
argument I had made in the corrective direction and got backwards anyway.

1. **I retired a harm that is true, and measured.** The row's *"corpus rescan per event"* is real for
   `leaseApplicationsRead` — `plainDerivationLicence` refuses on `$now`
   (`anchor_derivation_plain.go:332-339`), the sibling design observed the refusal live on 2026-09-01,
   and the board's Done-log names the cost. Reinstated, and the payoff split it implies is now stated in
   §0.2, §0.3 and §4: the anchor-hosted half buys a `warning`-severity sweep verdict, the neighbour-hosted
   half carries every measured number.
2. **I dropped two words of Andrew's ask and answered the wrong question.** *"On the service instance's
   **outcome**"* names `inst.outcome.data.validUntil` — the neighbour-hosted family — which my first
   recommendation deferred to a later row. → §0.1, §0.4.
3. **The scope fork was framed as majority-vs-everything; it is an ordering question.** Four rows now,
   with the recommended one building the whole sweep in one fire, neighbour-hosted family second. → §0.4.
4. **The OCC argument was inverted, and I argued the harder case for a smaller problem.** Declaring the
   marker in `optionalReads` **hydrates** it, and `applyHydratedRevisions` then conditions every update on
   the read revision (`commit_path.go:632-666`) — so the write is serialized, not unconditioned. The real
   residual is the **first** fire, where there is no revision to condition on, and the fix is a
   `create`/`update` branch the platform already licenses a retry for (`:668-690`). → §5.3, §6, §10, §12.
5. **"Nothing reads the marker" was my own grep's `-v '_test.go'` talking.**
   `internal/leaseconvergence`'s build-tagged harness reads `data.expiredAt` and asserts it strictly
   advances. `expiredAt` is a compatibility constraint, not operator legibility. → §2.4, §5.2, §5.3, §12.
6. **`cafeStaleTabSettlement` must not be converted — its target never arms a timer.** The cypher
   computes `freshUntil`; `BodyColumns` omits it (`cafe-domain/lenses.go:69`, `:366`); the projection
   driver treats `BodyColumns` as a strict whitelist and Weaver early-returns on the absent column.
   Converting the gap column would close a shipped café bug's last incidental escape. Out of the design,
   filed on the verticals board. → §7, §15.
7. **`capabilityEphemeral` is not the cheapest member; it is a different problem.** Its `$now` is a
   set-**inclusion** filter, so dropping it grows `cap.ephemeral.<actor>` without bound on the auth plane.
   Out of the design, its own designer row. → §2.6, §15.
8. **`renewalComplete` is neighbour-hosted.** Its `max(CASE WHEN inst.class = 'service.backgroundCheck.instance' …)`
   aggregates over the same instance class as `readinessWithItems`. → §7, §15.
9. **The anchor-hosted criterion I wrote was wrong** — *"the deadline sits on the anchor"* would have
   mis-sorted the wellness pair, which reads its deadline off the session neighbour and converts fine.
   The test is a **single bound scalar**, and what fails it is an aggregate over a set. → §7.
10. **The 4-deep read has no shipped precedent.** Confirmed at the evaluator, unpinned in the corpus, and
    the sibling shapes that are pinned were pinned after a live bug. An engine-level pin lands in
    Increment 0/1, before any cypher converts. → §12, §15.
11. Plus: `task` carries **two** freshUntil-projecting targets, not three (`orphanedTaskGrants` declares
    neither, `orchestration-base/lenses.go:106`); the census list is **eleven** files, not nine; the
    in-flight check must ask what **depends** on this design, not only what collides with it; the
    uncommitted Weaver `surface` clause makes two converted targets' issue counts marker-latency-dependent;
    `freshUntil` changes meaning observably (a past instant where there was a null) with no renderer today;
    and four citations were off (`evaluate.go`, `values.go`, `mark_expired.go`, `reproject.go`).

**Claims that survived both passes**: §4's mechanism links (deep verify → `Reproject` → fresh `$now` bind
→ columns inside the comparison); the *"no audit, no derivation"* retirement for the anchor-hosted
majority in §0.2; the null semantics; §5.4's `freshUntil` conversion and the past-deadline hole it closes;
and that `ReferencedLabels`, the consumer filter, `projectedFromRevisions` and footprint validation are
all unmoved by reading a new aspect.

---

## 15. Decomposition for the Steward

Ordered as **§0.4 row 3, ratified by Andrew 2026-09-01** — one fire, the neighbour-hosted family second.
Rows 2 and 4 are recorded in §0.4 as the alternatives he declined.

**Increment 0 — confirm the premises** *(XS)*: §13's four Phase-0 premises — the live sweep-flapping
signal, §8's stuck-gap mitigation hypothesis, the registry's acceptance of a gaps-less target (§5.5), and
the retry-absorption of a present-marker conflict (§12). Plus the one pin that is code rather than
observation: the **engine-level 4-deep aspect read**, `a.<aspect>.data.byTarget.<targetId>`, as a sibling
case in `ruleengine/full/aspect_expression_shapes_test.go` — landed before any cypher converts. Gate on
all five.

**Increment 1 — the per-target marker** *(S–M)*: `MarkExpired` declares the marker key in
`contextHint.optionalReads` and merges `byTarget`, branching **`create` when the key is known-absent and
`update` when present**, with `expiredAt` maintained as the monotone maximum over `byTarget`. Rewrites
three shipped justifications in `mark_expired.go` — the two *"read by nothing"* comments and the
unconditioned-update rationale at `:30-38` (§11). Tests: both concurrent-fire branches, mutation-proven
against a whole-document overwrite. **Acceptance: `make test-lease-convergence` passes unmodified** — its
strict-advance assertion on `data.expiredAt` is the shipped witness this increment must not break (§2.4).
**Ships alone, changes no lens**, and is the prerequisite both families depend on. Posture-changing — it
changes an op's write shape and its conditioning.

**Increment 2 — the neighbour-hosted family** *(M–L)*: a new convergence lens + Weaver target anchored on
the **background-check instance**, projecting `freshUntil = inst.outcome.data.validUntil`, with its own
output descriptor and package permissions (§5.5). One edit to the shared `readinessWithItems` fragment,
read by `leaseApplicationComplete`, `leaseApplicationsRead` and `landlordLeaseApplicationsRead`; one edit
to `renewalComplete`'s `max` aggregate. Decide deliberately whether `internal/leaseconvergence`'s witness
keeps reading the application's marker or re-points at the instance's, with the reason in the test name.
**This is the increment that carries the measured payoff** — §13's derivation-refusal, audit-enrolment and
per-message rows are its acceptance criteria. Posture-changing.

**Increment 3 — the ten anchor-hosted declarations** *(M)*: the nine role-(b) declarations plus role (a)'s
`clauseSatisfaction` (§2.1). **Both the gap column and `freshUntil`**, per lens — never one alone (§5.4).
Cypher units including the re-arm, deadline-moved-earlier, revived-same-deadline and past-deadline
vectors; the sweep-verdict regression; the **eleven** corpus censuses (§12). Posture-changing.

**Increment 4 — role (c), the guards** *(S)*: the two reminder ops' `startsAt` guards, following
`BookAppointment`'s shipped `op.submittedAt` idiom; the `.schedule` read declaration that closes the
fail-open; `time.rfc3339_utc` normalization (§7).

**Two rows leave this design, neither converted here:**

- **Café auto-settle never arms its timer** → the **verticals** board. `StaleTabSettlementTarget`'s
  `BodyColumns` omits the `freshUntil` its cypher computes, so no `@at` is ever scheduled and settlement
  waits on an incidental CDC touch (§7). A shipped bug with its own fix; converting
  `cafeStaleTabSettlement` before it lands would close the gap permanently.
- **`capabilityEphemeral`'s inclusion filter** → the **lattice** board, `📐 needs designer pass`. Its
  `$now` decides set membership, not a gap, and it has no fact to read in its place; the fact it needs
  would live on the task and be written by another target's marker (§2.6).

Review depth across the three posture-changing increments (1, 2, 3) is the Steward's sizing; Increments 1
and 2 carry the platform risk.

---

## 16. Fire brief (build note, 2026-09-02)

**Scope sentence (verbatim, §0.4 row 3 / Andrew 2026-09-01):** *"Everything, as ONE fire — Inc 1 per-target marker → Inc 2 the neighbour-hosted family (the background-check instance becomes its own anchor; the shared `readinessWithItems` fragment is one edit read by three lenses; `renewalComplete` joins them on the same instance class) → Inc 3 the ten anchor-hosted declarations → Inc 4 the role-(c) guards."* Green bar: §13's table — sweep heals on a quiet converged lens stop; `leaseApplicationsRead`'s derivation refusal gone; `landlordLeaseApplicationsRead` still refused on Secure + diff-retraction; `make test-lease-convergence` green; the eleven censuses re-pinned; every `$now` gone from the fourteen in-scope declarations.

**Worktree:** `/tmp/lattice-worktrees/expiry-fact-1788334120` (branch `steward-lattice-expiry-fact`). Landing shape: **land each increment on `main`** when green — every boundary is independently correct (Inc 1 changes no lens; Inc 2–4 each leave the untouched lenses on today's `$now` form, which is the shipped state).

### 16.1 Phase-0 premises — all five re-derived live at `1f89d588`

| # | Premise (§13) | Verdict | Evidence |
|---|---|---|---|
| 1 | sweep-divergence flapping on `$now` lenses, live | **CONFIRMED** | `refractor.log` (2026-09-01/02): `sweep: healed a divergent projection` ×133 on `renewalComplete`, ×27 on `cafeStaleTabSettlement`; `class:"content"` divergences ×117 `unroutedTasks`, ×46 `staleAssignedTasks`, ×69 `renewalComplete` — ids resolved via `vtx.meta.<id>.canonicalName` |
| 2 | §8 stuck-gap mitigation: a re-projection re-arms with a NEW fire instant ⇒ new requestId | **FALSIFIED as stated, conclusion holds by a different mechanism** | `temporal.go:135-143`: the payload's `fireAt` is the **deadline**, so a re-projected past deadline derives the **same** requestId by design. But Contract #4 §4.4 (`04-idempotency-tracker.md:72-73`): a failed commit lands **no tracker**, so `CheckDedup` (`step2_dedup.go:48-64`) finds nothing and the same requestId **re-executes**. Recovery trigger = the next CDC touch of the entity re-delivering the row → `scheduleFreshness` re-publishes the overdue `@at` → fires immediately → `MarkExpired` re-executes. That trigger is exactly today's ("waits for the next CDC touch"), one hop longer. §8 amended below. |
| 3 | the registry accepts a target with no gaps entry | **CONFIRMED** | install validates only declared keys' `missing_` shape (`internal/pkgmgr/orchestrationguard.go:185-188`); load `validateTarget` (`internal/weaver/registry.go:703-754`) same; `handleRow` runs `scheduleFreshness` at `evaluator.go:117` before any gap read and Acks a non-violating row (`:131-134`). No gate requires ≥1 gap. |
| 4 | a present-marker revision conflict is retry-absorbed | **CONFIRMED** | `commit_path.go:298-330` (bounded re-hydrate + re-execute on a §3.2-defaulted conflict), `applyHydratedRevisions` `:632-666`, `absentConditionedCreates` `:668-690` |
| 0 | the engine-level 4-deep aspect read is unpinned | **CONFIRMED unpinned** | `ruleengine/full/aspect_expression_shapes_test.go` deepest pin is `x.<aspect>.data.<leaf>` (`:73`, `:111`); `values.go:23-82` resolves an absent root name as an aspect point-read and then plain map hops — argument, not pin. Inc 0 adds the sibling case. |

**Censuses re-run:** C1 = 16 `$now` declarations / 11 files (the other `$now` hits in `packages/` are doc/script comments: `clinic-domain/ddls.go:2687`, `service-domain/ddls.go:1238`, `clinic-reminders/package.go:41,49`, `wellness-reminders/package.go:29,36`, `lease-signing/scripts.go:1555`, `freshness_window.go:9`, `mark_expired.go:21,50,70,101`). C2 holds (`AnchorType:` grep). C4: no production reader outside `mark_expired.go`; the build-tagged harness at `internal/leaseconvergence/harness_test.go:945-973` + `convergence_test.go:540-545` reads `<appKey>.freshnessExpiry` `data.expiredAt` and asserts strict advance. **Contract #4 census pin (`internal/pkgregistry`) already moved 3→2 in `27250d12`** — unrelated to this fire, noted so the builder does not "fix" it back.

### 16.2 Verified touch-list (file:line live at `1f89d588`)

**Inc 0 (engine pin):** `internal/refractor/ruleengine/full/aspect_expression_shapes_test.go` — add a sibling case mirroring `:57-85` with `data: {byTarget: {<targetId>: <rfc3339>}}` and the read `x.<aspect>.data.byTarget.<targetId>` in a scalar alias **and** inside a `>=` comparison + a `CASE WHEN … THEN null ELSE … END`; pin the nil-false of `compareAny` (`values.go:170-173`) for an absent marker and an absent deadline, and `NOT (…)` over the comparison (the corpus's negation form; no shipped lens uses `= False`).

**Inc 1 (per-target marker):**
- `packages/orchestration-base/mark_expired.go` — script `:189-227`: declare `entity_key + ".freshnessExpiry"` as an **optional read** and branch: absent (`key not in state or state[key] == None`, the `vertex_alive` idiom at `clinic-reminders/ddls.go:161`) ⇒ `create`; present ⇒ `update` with `data = {expiredAt: max(existing.byTarget ∪ {targetId: expired_at}), byTarget: merged}`. `targetId` becomes **required** on the wire (today provenance-only, `:88`) — the Weaver always sends it (`temporal.go:291-296`); a `MarkExpired` with no `targetId` fails closed. Rewrite the three justifications: `:30-38` (unconditioned rationale), the two *"read by nothing"* lines (`:52-61` area and `:110-115`), and the CDC-poke narrative `:21`, `:50`, `:70`, `:101` so they describe the marker as **read by the lens**. Version: `manifest.yaml` + `package.go:48` (`0.7.13` → bump).
- `internal/weaver/temporal.go:291-305` — the dispatcher must declare the marker as an optional read: `reads := []string{p.EntityKey}` gains the companion `optionalReads := []string{p.EntityKey + ".freshnessExpiry"}` through `e.act.submit`'s trailing params (verify the signature in `internal/weaver/actuator.go`; the `nil, nil` after `reads` are the candidates). Rewrite the `:297-303` comment (the marker write is no longer unconditioned).
- Tests: `packages/orchestration-base/*_test.go` — both concurrent-fire branches (first fire two `create`s ⇒ loser retries as `update`; steady state two `update`s ⇒ conflict absorbed), mutation-proven against a whole-document overwrite (the overwrite DELETES the sibling `byTarget` entry — assert the entry survives, not "no lower value"); `expiredAt == max(byTarget)` and never moves backwards. Acceptance: `make test-lease-convergence` passes **unmodified** (harness at `internal/leaseconvergence`, tag `leaseshortwindow`).

**Inc 2 (neighbour-hosted family, lease-signing):**
- NEW lens + target in `packages/lease-signing/renewal_lenses.go` / `renewal_targets.go` (mirror `leaseExpiry` spec `:25-41` + target `renewal_targets.go:39-54`): `CanonicalName: "backgroundCheckFreshness"`, `Adapter: nats-kv`, `Bucket: weaver-targets`, `ProjectionKind: actorAggregate`, `AnchorType: "service"`, `OutputKeyPattern: "backgroundCheckFreshness.{actorSuffix}"`, `BodyColumns: [violating, entityKey, freshUntil]`, `EmptyBehavior: delete`, `KeyColumn: entityId`; cypher: `MATCH (inst) WHERE inst.class = 'service.backgroundCheck.instance' AND inst.outcome.data.status = 'completed'`, `violating = false`, `freshUntil = CASE WHEN inst.freshnessExpiry.data.byTarget.backgroundCheckFreshness >= inst.outcome.data.validUntil THEN null ELSE inst.outcome.data.validUntil END`. Target: `TargetID: "backgroundCheckFreshness"`, `LensRef` same, `Gaps: nil` (premise 3), description states it dispatches nothing. **targetId is free-form but keep it under 20 chars in FIXTURES** (weaver dossier); the production id `backgroundCheckFreshness` is 24 chars — fine in a spec, mind `lint-conventions`'s NanoID heuristic on `…ID` identifiers in tests (use `bgFresh` in fixtures).
- `packages/lease-signing/lenses.go:730-732` `readinessWithItems`: replace `inst.outcome.data.validUntil > $now` with `NOT (inst.freshnessExpiry.data.byTarget.backgroundCheckFreshness >= inst.outcome.data.validUntil)` (polarity §5.5 — nil-false lands on the FRESH side, which is the default a never-lapsed instance needs; pin the lapse/fresh pair with the reason in the test name). Spliced by `fmt.Sprintf` at `:857`, `:1095`, `:1214` — one edit, three readers.
- `packages/lease-signing/renewal_lenses.go:234` `renewalComplete`'s `max(CASE WHEN … validUntil > $now …)` — same negated-marker form.
- `leaseApplicationComplete` (`lenses.go:39`, spec near `:739-860`): **decision (Winston):** its application-hosted `freshUntil` existed only to poke the fragment (`mark_expired.go:21` names exactly this); with the instance recording its own lapse and the instance's marker write re-projecting the application row as a neighbour event, that timer is a prosthesis with no reader — **drop it** (BodyColumns + cypher), leaving `leaseExpiry` as the only leaseapp-anchored timer. `internal/leaseconvergence`'s witness **re-points at the instance's marker** (`<instKey>.freshnessExpiry` `data.byTarget.backgroundCheckFreshness`, or `data.expiredAt` which is its monotone max) — test name states *why*: the lapse is recorded where it happens. If the harness cannot name the instance key, derive it from the `providedTo` link it already drives. `make test-lease-convergence` is Inc 2's acceptance too.
- Package `Permissions()` / output descriptors: mirror whatever `leaseExpiry` needed (scout found no per-lens `weaver-targets` permission entries beyond the lens spec; confirm with `make verify-package-lease-signing`). Version: `manifest.yaml` + `package.go:92` (`0.31.16` → bump).
- Postgres pair: no cypher edit beyond the fragment. **Acceptance = the measured payoff:** after `refresh-loftspace`, `refractor.log` no longer refuses `leaseApplicationsRead` on `$now` (`anchor_derivation_plain.go:337-338`) and `AuditPlan` enrols it; `landlordLeaseApplicationsRead` still refused at `:329` — assert both.

**Inc 3 (ten anchor-hosted declarations) — per lens: gap column AND `freshUntil`, never one alone (§5.4):**
| lens | cypher `$now` lines | target id | deadline |
|---|---|---|---|
| `pastDueAppointments` | `clinic-reminders/pastdue.go:100-101` | `pastDueAppointments` | `a.schedule.data.endsAt` |
| `followUpReminders` | `clinic-reminders/followups.go:298-299` | `followUpReminders` | `a.documentation.data.followUpDate` |
| `appointmentReminders` (remindAt term only; `startsAt > $now` guard is Inc 4) | `clinic-reminders/lenses.go:126-127` | `appointmentReminders` | `a.schedule.data.remindAt` |
| `visitSeriesDue` | `clinic-reminders/visitseries.go:1105-1106` | `visitSeriesDue` | `s.progress.data.nextDueAt` |
| `unroutedTasks` | `orchestration-base/lenses.go:221,223` | `unroutedTasks` | `t.data.expiresAt` |
| `staleAssignedTasks` | `orchestration-base/lenses.go:249,251` | `staleAssignedTasks` | `t.data.expiresAt` |
| `leaseExpiry` | `lease-signing/renewal_lenses.go:161-162` | `leaseExpiry` | `renewalOpensAt` (scalar carried through `WITH`) |
| `clauseSatisfaction` (role a) | `semantic-contracts/lenses.go:231,234` | `clauseSatisfaction` | `chargeValidUntil` |
| `wellnessBookingReminders` (remindAt term only) | `wellness-reminders/lenses.go:92-93` | `wellnessBookingReminders` | `se.schedule.data.remindAt` |
| `pastDueBookings` | `wellness-reminders/pastdue.go:106-107` | `pastDueBookings` | `se.schedule.data.endsAt` |
Form: gap `… AND (<anchor>.freshnessExpiry.data.byTarget.<targetId> >= <deadline>)`; `freshUntil = CASE WHEN <same comparison> THEN null ELSE <deadline> END` (drop the `deadline > $now` conjunct — the past-deadline vector, §5.4, is the regression to write FIRST and watch go red on a gap-only conversion). Where the deadline is a `WITH`-carried scalar (`leaseExpiry`, `clauseSatisfaction`) carry the marker read through the same `WITH`; `grouping_reduction_corpus_census_test.go:87,143` **moves** for exactly those two — move it deliberately with the reason. Per-package cypher tests to mirror: `clinic-reminders/{pastdue,lens,followups,visitseries}_cypher_test.go`, `orchestration-base/lens_cypher_test.go`, `lease-signing/lens_cypher_test.go`, `semantic-contracts/lens_cypher_test.go`, `wellness-reminders/{lens,pastdue}_cypher_test.go`. Mandatory vectors per lens: marker absent / behind / at / ahead; re-arm (deadline moved forward, no clearing write ⇒ not expired); deadline-moved-earlier ⇒ expired (correct, named); revived-same-deadline ⇒ expired (correct, named); past-deadline-at-first-projection ⇒ `freshUntil` carries the past instant. Versions: clinic-reminders `0.10.6`, orchestration-base (already bumped in Inc 1 — bump again), lease-signing (again), semantic-contracts `0.4.5`, wellness-reminders `0.3.3`.
Censuses to re-run + pin (eleven): `grouping_reduction_corpus_census_test.go`, `anchor_hopindex_corpus_census_test.go`, `label_derivation_corpus_census_test.go`, `actor_walk_scope_corpus_census_test.go`, `actor_onekey_corpus_census_test.go`, `branch_decomposition_corpus_census_pins_test.go`, `plain_scanroot_corpus_census_test.go`, `rel_projection_corpus_census_test.go`, `ruleengine/full/grouping_corpus_lens_test.go`, `internal/refractor/auth_plane_narrowing_census_test.go:266-270`, `internal/refractor/multiwalk_footprint_reachability_census_test.go` — assert the label/relation/filter pins do NOT move. *(Corrected at Inc 2's review: the last two need no entry — the multiwalk census enumerates dynamically and keeps only multi-branch lenses; `IsAuthPlane` excludes `weaver-targets` lenses. Nine tables carry pins, not eleven.)*
Sweep-verdict regression (§12): two deep-verify passes straddling a deadline classify `divergenceContent` on main and `divergenceNone` after — assert on the classification (`reproject.go:361-376`), run once with `freshUntil` unconverted and assert it still fails.

**Inc 4 (role-c guards):** `packages/clinic-reminders/ddls.go:174-249` `RecordAppointmentReminder` and `packages/wellness-reminders/ddls.go:175-251` `RecordBookingReminder`: add the `.schedule` aspect (clinic: `<appointmentKey>.schedule`; wellness: the SESSION's `.schedule` — the booking's deadline lives on `se`, so declare the session key the op can name from the row; if the dispatcher cannot template it, read via the declared link the way `SettleStaleTab` does and say so) to `ContextHint.Reads` at both the DDL and every dispatcher (the target `Reads:` in `clinic-reminders/targets.go:26-38`, `wellness-reminders/targets.go:28-40`); guard `time.rfc3339_utc(op.submittedAt) < startsAt` mirroring `enforce_future` (`clinic-domain/ddls.go:2432-2435`, callers `:2701`, `:2910`); refuse a started appointment/session, accept a future one, non-Weaver actor refused BEFORE the clock is read; strip `startsAt > $now` from both lens cyphers (`clinic-reminders/lenses.go`, `wellness-reminders/lenses.go`). Doc line in `docs/components/weaver.md` (temporal lane `:258-323`): a Weaver-dispatched op's `submittedAt` is the platform's dispatch instant (RFC3339Nano, `actuator.go:97`). Read-drift ratchet (`5699325`): any new script read must be declared at every dispatcher or `testutil`'s guard blocks it.

### 16.3 Precedents to mirror
- Optional-read absence branch: `vertex_alive` (`clinic-reminders/ddls.go:161`), `class_of` (`location-domain/ddls.go:366`) — `key not in state` / `state[key] == None`.
- Full-document `update` mutation: `clinic-reminders/ddls.go:224-227`. No merge/patch kind exists — the script builds the merged `data` itself.
- Weaver-target lens spec: `lease-signing/renewal_lenses.go:25-41`; target spec `renewal_targets.go:39-54`; registration `targets.go:160` append + `package.go:123-125`.
- Cypher fixture idiom: `lease-signing/lens_cypher_test.go:41-76` (`newLensFixture`, `vtxWithClass`, `aspect`, `edge`, `project`).
- Guard: `enforce_future` (`clinic-domain/ddls.go:2432-2435`).
- 4-deep pin sibling: `aspect_expression_shapes_test.go:57-85`.

### 16.4 Increment order + green checks
0. pin → `go test ./internal/refractor/ruleengine/full/ -run 'TestAspectExpr' -count=1`
1. marker → `go test ./packages/orchestration-base/ ./internal/weaver/ -count=1` · `make test-lease-convergence` · `make test-unrouted-convergence` · `make verify-package-orchestration-base` (if present; else `make reinstall-package PKG=packages/orchestration-base` on the live stack) → commit.
2. family → `go test ./packages/lease-signing/ ./internal/leaseconvergence/... -count=1` (+ tag) · `make test-lease-convergence` · `make refresh-loftspace` · live `refractor.log` assertion (refusal gone / enrolment) → commit.
3. ten → per-package `go test ./packages/<p>/ -count=1` · `go test ./internal/refractor/... -count=1` (censuses) · `make test-augur-convergence` · `make test-unrouted-convergence` · reinstall each edited package live → commit.
4. guards → `go test ./packages/clinic-reminders/ ./packages/wellness-reminders/ -count=1` · reinstall → commit.
Every commit: `go build ./...` · `make vet` · `golangci-lint run ./...` · `make verify-kernel` · every `scripts/lint-*.go` (`STRICT=1 lint-conventions`, `lint-package-version` with `DIFF_BASE`, `lint-lens-anchors`, `lint-gap-column-declaration`, `lint-board` on board edits) · `.github/workflows/ci.yml` is the authority. Full `go test ./... -p 4` before the close (a shared script + a Weaver dispatcher change reach unedited consumers). Then rebuild + cycle `bin/weaver` (`pkill -x weaver && make orchestration`), `bin/lattice`, and the vertical apps that link the edited packages (derive with `go list -deps`).

### 16.5 In-scope gotchas (+ dossier entries that apply)
- **No changelog comments** — every rewritten `mark_expired.go` comment describes the marker as it is now.
- **Package edit ⇒ manifest + `Version` bump, per edited package, per increment** (`lint-package-version`).
- **Read declarations are lockstep**: DDL `ContextHint` + every dispatcher (`temporal.go` for MarkExpired; the target specs' `Reads` for the reminder ops). The read-drift ratchet blocks an undeclared one.
- **Contract #4 census pin** in `internal/pkgregistry` already moved; leave it.
- **Uncommitted docs in `main`** (`loom-instance-enumeration-bounding-design.md`, `docs/contracts/10-orchestration-substrate.md`) are someone else's — never stage them.
- Live stack is up: use `make reinstall-package` / `refresh-*` (no teardown); never `make down`; cycle only the binaries this fire rebuilds.
- Dossier — weaver: *a shared fixture that always supplies an OPTIONAL input pins only the supplied case* (the marker is optional: one vector per lens must omit it); *prove each changed line by reverting THAT line* (the `freshUntil` CASE and the gap conjunct are two lines — revert each alone); *fixture `targetId` under 20 chars*. Dossier — refractor: *a new per-lens analysis ships its corpus census in the same fire* (the eleven pins); *an upsert-only reprojection retracts nothing whose key drops out* (dropping `leaseApplicationComplete`'s `freshUntil` column: `Freshness: auto` rows are rewritten whole — confirm the column vanishes, and grep readers for a presence test: `cmd/loftspace-app/applicationsource.go:75` decodes only); *a liveness test must run the arm the consumer's `ProjectionKind` selects* (the new bgcheck lens is `actorAggregate` — fixtures take that arm). Dossier — packages: *a column's ABSENCE and its declared FALSE are different inputs* (`violating` on the gap-less target must be projected `false`, not omitted, since `handleRow` reads it); *census the CHECK not the wrapper*; *a declared read of a `Sensitive` aspect decrypts before the script runs* (`.schedule` is not sensitive — confirm in the DDL before declaring). Standing checklist #1–#6 (`agents/fire-brief-template.md`) walked: state table is §6; every census re-run above; negative tests get their positive vector first; the removed application-side timer's obligations enumerated (poke + harness witness, both re-homed); one deterministic key one writer (the marker: `MarkExpired` only, serialized by the declared read); precedents verified (the `update` idiom in `clinic-reminders` is unconditioned — do NOT copy that posture, the declared read conditions ours).

### 16.6 Adjacent finds (scoping, not filing)
- `unroutedTasks`' divergences read *"could not be repaired — the ordering guard declined the write"* (×117, class content). The ordering guard (`stored watermark >= reconciliation token`) refusing a content heal is a Refractor sweep behaviour, not this fire's mechanism; after Inc 3 the `$now`-driven content divergence on that lens should vanish. **Re-measure at close**; if content divergences persist on a converted lens, that is a find this run fixes (it is the sweep's own arm) or files under one of the two outs.
- The two rows §15 names already exist on the boards (café `freshUntil` fix shipped `b569fd2c`; `capabilityEphemeral` 📐 row on lattice). Nothing new to file.

### 16.7 Non-goals
`cafeStaleTabSettlement` and `capabilityEphemeral` (own rows); the `MarkExpired` op volume; any `.status`/lifecycle aspect; the `@at` keying; a new lint for the guard idiom (§11); the Weaver `surface` count clause (the sibling ratified row builds it).

**Scope-diff gate:** parts 2–4 trace item-by-item to the scope sentence; the one narrowing is decided above (drop the application-side `freshUntil` in `leaseApplicationComplete` — a mechanism §5.5 left as a deliberate choice, resolved here, not a substitution). No dependency was found unlisted; the listed one (sibling hub-walk fire) is load-bearing for Inc 2's measurement only.

### 16.8 Checkpoint

- **Inc 0 + Inc 1 LANDED on `main`** (`dafa4aee` + review fix round `aa09db00`; cold review 0 blocking / 2 major / 8 minor, all closed). `bin/weaver` cycled, `bin/lattice` rebuilt, orchestration-base 0.7.15 diff-applied live.
- **Inc 4 built and reviewed** (branch `steward-lattice-expiry-fact-inc4`, `09e96178`, merged into the fire branch) — **held off `main` until Inc 3 lands, by decision:** dropping `startsAt > $now` from the two reminder lenses leaves a started-never-reminded appointment `missing_reminder` with the op refusing each dispatch until the retry budget escalates. The closing term is the design's own mechanism one target over: `NOT (a.freshnessExpiry.data.byTarget.pastDueAppointments >= a.schedule.data.endsAt)` (wellness: `byTarget.pastDueBookings` against the session's `endsAt`) — the sibling past-due target's recorded lapse says the appointment has ended, with no clock. Inc 3 adds that term to both reminder lenses and lands Inc 4 with it.
- **Inc 2 built + cold-reviewed** (0 blocking / 3 major / 8 minor; fix round in flight). Decisions at review: `renewalComplete` drops its own `freshUntil` too (same prosthesis); the deploy-time window where an already-expired check reads fresh until the instance's overdue `@at` fires is accepted and documented — no `surface` gap, because a gap that never opens is no signal; the frozen-contract illustration in `10-orchestration-weaver.md` (`date > now − window`) is staged UNCOMMITTED for Andrew. Deploy artefact to expect: each standing `leaseApplicationComplete` `@at` fires once, writes an unread `byTarget.leaseApplicationComplete` entry, never re-arms.
- **Inc 2 LANDED on `main`** (`8bc15e7d` + fix round `5fa521ec`, CI green). Live at 02:40: lease-signing 0.31.18 + orchestration-base 0.7.16 diff-applied, loftspace-app restarted; the first instance markers carry `byTarget.backgroundCheckFreshness`; the new target's backfill armed ≤2 overdue `@at`s per instance across the whole `vtx.service.*` population (12,294 vertices — the §16.6 sweep-cost note applies: the lens's sweep root is the shared `service` hub type, not the bgcheck subset); after the hot reload no plain-derivation refusal is logged for `leaseApplicationsRead` (every prior activation logged *"it returns $now, which a recomputation cannot reproduce"*); `landlordLeaseApplicationsRead` stays refused on diff-retraction. **Activation verdict after a Refractor cycle (02:46):** `divergence audit enrolled` for `leaseApplicationsRead` (anchor `leaseapp`, 15-min interval) — the §13 enrolment row is met; its plain-derivation licence now reads *"its divergence audit has not reached a verdict since it was installed"*, i.e. it licenses after the first audit pass (re-check at close). `landlordLeaseApplicationsRead`: *"it uses target-diff retraction"* — refused earlier than `$now`, as §0.2 predicted.
- **Inc 3 in flight** (+ Inc 4 landing, + the reminder lenses' recorded-end term, + the auth-plane census closure) on the fire worktree `/tmp/lattice-worktrees/expiry-fact-1788334120` (branch `steward-lattice-expiry-fact`). Next: Inc 2 lands on `main` by cherry-pick; Inc 3 (+ Inc 4) lands as the branch merge; close pass; live `refresh-*` per package; re-measure §16.6.
