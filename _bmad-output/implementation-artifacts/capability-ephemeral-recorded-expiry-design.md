# `capabilityEphemeral` reads the recorded lapse — the last `$now` leaves the lens corpus

**State: ✅ RATIFIED (Winston-adjudicated, per the 2026-08-20 delegation) — 2026-09-05.**
Designer fire 2026-09-05 (Winston). Board row: `[Refractor/Processor] capabilityEphemeral's $now is a
set-inclusion filter with no fact to record` (Lattice lane, Read-model / projection maturity). Parent:
[`expiry-as-a-recorded-fact-design.md`](expiry-as-a-recorded-fact-design.md) §2.6, which filed this row.

## 0. For Andrew

**What it does, in two lines.** The one lens still reading the clock — the auth-plane `capabilityEphemeral`
— replaces `task.data.expiresAt > $now` with a read of the `freshnessExpiry` marker the two task-anchored
Weaver targets already write on every task at its deadline; the café stale-tab lens converts the same way,
and a corpus-wide census pin then refuses any lens that references `$now`, blocking, with zero debt.

**Fork / contract check — neither, proven:**

- **No architectural fork.** The mechanism is the one you ratified 2026-09-01 (*"sweep all uses of $now in
  the lens files, prefer shape B"*), applied to the member that design set aside; every decision below is
  mechanism-level (which marker field the lens reads, which increment owns which pin).
- **No contract change — builds to Contract #6 §6.6 and Contract #10 §10.7.** The promise both make is
  *"Processor enforces `expiresAt > now` at lookup time"* (`06-capability-kv.md:289`, `:292`;
  `10-orchestration-substrate.md:283-286`) and *"an expired/closed task must not grant"* (`:30`). Nothing
  here moves that check (`internal/processor/step3_auth_capability.go:349-358`, the refusal at `:357`, pinned by
  `TestCapabilityAuthorizer_TaskPath_Expired`, `step3_auth_capability_test.go:459`). Which rows the lens
  *projects* is mechanism the contracts never state — the §6.6 field table lists no status or liveness
  column, and the lens's own doc comment already records that its WHERE *"does not widen or alter §6.6's
  own Processor-side check"* (`packages/orchestration-base/lenses.go:433-437`).
- **The premise correction you should know about (§1.2):** the row says the lens has *"no fact to
  record"*. That was true when the parent design filed it and false by the time the parent's own Increment 3
  shipped (`a2dfd339`): `unroutedTasks` and `staleAssignedTasks` are task-anchored targets whose `@at`
  fires at `expiresAt` and records the lapse on the task. Live census: **52 of 52** open-past-deadline tasks
  carry that marker at or past their deadline (§2, C7). The design is therefore a read of an existing fact,
  not a new mechanism — honest size **S**.
- **Honest payoff (§4):** the marker write already re-projects the actor today, so `cap.ephemeral` carries
  **zero** expired grants on the live stack right now (§2, C8). What changes is that the row becomes a pure
  function of the subgraph — the convergence sweep's verdict on the one lens where its divergence escalation
  reaches `error` stops being a clock reading — and the corpus rule you set becomes a blocking gate instead
  of a convention with one standing exception.

---

## 1. Problem + intent

### 1.1 The direction, quoted

The parent design records your ask verbatim (`expiry-as-a-recorded-fact-design.md` §0.1):

> *"sweep all uses of $now in the lens files, prefer shape B (expiry belongs to the check, the application
> may have its own expiration - cancel after inactivity, or something like that)"*

Clause by clause, against this design:

| Clause | Here |
|---|---|
| **"sweep all uses of `$now` in the lens files"** | Two lens declarations remained after the parent's build: `capabilityEphemeral` (three arms, one WHERE each) and `cafeStaleTabSettlement` (three occurrences, one predicate). Both convert here; the census then reads zero and the pin holds it there (§2 C1, §7). |
| **"prefer shape B — expiry belongs to the check"** | Adopted as the parent adopted it: the fact is recorded on the entity whose deadline lapsed (the task, the tab), by the timer that fired against it. No task `.status` moves; `myTasks` keeps rendering an expired-but-open task to its assignee, by its own recorded reasoning (`lenses.go:344-357`). |
| **"the application may have its own expiration"** | Untouched. `CompleteTask` stays legal past the deadline (`ddls.go` transition_task; `targets.go:16-22`); the two `surface` targets flag, never cancel. |

### 1.2 The row's premise, re-derived

The parent design's §2.6 excluded this lens because *"the fact this lens would need to read is a recorded
expiry on the task, written by some other target's marker — a question about a different entity's
convergence than any increment here answers."* Its own Increment 3 then answered it: the ten anchor-hosted
conversions include the two **task-anchored** targets —

```
$ sed -n 232,245p packages/orchestration-base/lenses.go
var unroutedTasksSpec = fmt.Sprintf(`
MATCH (t:task {key: $actorKey})-[:queuedFor]->(role:role)
WHERE t.data.status = 'open'
RETURN
  t.key AS actorKey,
  t.key AS entityKey,
  nanoIdFromKey(t.key) AS entityId,
  role.key AS queuedRole,
  t.data.expiresAt AS expiresAt,
  (t.freshnessExpiry.data.byTarget.%[1]s >= t.data.expiresAt) AS missing_claim,
  (t.freshnessExpiry.data.byTarget.%[1]s >= t.data.expiresAt) AS violating,
  (CASE WHEN t.freshnessExpiry.data.byTarget.%[1]s >= t.data.expiresAt THEN null ELSE t.data.expiresAt END) AS freshUntil,
  %[2]d AS maxretries_claim
`, UnroutedTasksTarget, maxClaimRetries)

$ sed -n 264,276p packages/orchestration-base/lenses.go
var staleAssignedTasksSpec = fmt.Sprintf(`
MATCH (t:task {key: $actorKey})-[:assignedTo]->(assignee:identity)
WHERE t.data.status = 'open'
RETURN
  t.key AS actorKey,
  t.key AS entityKey,
  nanoIdFromKey(t.key) AS entityId,
  assignee.key AS assignee,
  t.data.expiresAt AS expiresAt,
  (t.freshnessExpiry.data.byTarget.%[1]s >= t.data.expiresAt) AS missing_completion,
  (t.freshnessExpiry.data.byTarget.%[1]s >= t.data.expiresAt) AS violating,
  (CASE WHEN t.freshnessExpiry.data.byTarget.%[1]s >= t.data.expiresAt THEN null ELSE t.data.expiresAt END) AS freshUntil,
  %[2]d AS maxretries_completion
```

(`%[1]s` is the target id, threaded from the one constant so the marker key the cypher reads and the
`TargetID` Weaver fires under cannot drift; `freshUntil` is in both targets' `BodyColumns`, `lenses.go:85`,
`:101` — the omission that stranded the café lens is not repeated here.)

— each projecting `freshUntil = expiresAt` for every open task of its routing shape, so Weaver's temporal
lane arms an `@at` at the task's own deadline (`internal/weaver/temporal.go:104-201`) and, on firing,
submits `MarkExpired{entityKey: <the task>, targetId, expiredAt: fireAt}` (`temporal.go:203-341`; the
instant is the deadline the payload carried, never "now": `:150-158`). `MarkExpired` merges
`vtx.task.<id>.freshnessExpiry = {expiredAt, byTarget:{<targetId>: <instant>}}`, `expiredAt` being *"the
monotone maximum over the merged `byTarget` and the marker's own standing value, so it never moves backwards
no matter which target fires next"* (`packages/orchestration-base/mark_expired.go:39-42`).

**Every task the ephemeral lens projects is in one of those two populations.** The lens's three arms bind a
task through `assignedTo` (direct), `assignedTo` then `reportsTo` (delegation), or `queuedFor` then
`holdsRole` (role queue) — `lenses.go:450-475`. An open task carries exactly one assignment link
(`10-orchestration-substrate.md:35-36`), `assignedTo` or `queuedFor`; `staleAssignedTasks` anchors the
former and `unroutedTasks` the latter, both gated only on `status = 'open'`. So the fact the row said did
not exist is recorded for the whole population, at the exact instant the lens's own predicate asks about.

---

## 2. Grounding ledger — every count run, raw output pasted

| # | Claim | Command / site | Result |
|---|---|---|---|
| C1 | `$now` in shipped package cypher | `grep -rn -F '$now' packages --include='*.go' \| grep -v _test.go \| grep -v ':\s*//' \| grep -v '#'` | **6 lines, 2 lenses**: `orchestration-base/lenses.go:451,459,472` (capabilityEphemeral); `cafe-domain/lenses.go:397,398,401` (cafeStaleTabSettlement). One `visitseries.go:101` hit is a Go string in an op description, not cypher. |
| C2 | `$projectedAt` in shipped package cypher | same grep, `$projectedAt` | **1**: `rbac-domain/lenses.go:114` — `$projectedAt AS projectedAt`, a bare output column the sweep already excludes as volatile (`reproject.go:302`). Stays; §7 pins it by name. |
| C3 | primordial cyphers | same grep over `internal/bootstrap` | 0 (the parent's §2.1 finding holds). |
| C4 | shipped `byTarget` readers | `grep -rn 'freshnessExpiry.data' packages --include='*.go' \| grep -v _test \| grep -v '//' \| wc -l` | 30 (the ten converted declarations + fmt seams) — the read shape is corpus precedent, not new. |
| C5 | production readers of `data.expiredAt` | `grep -rn 'data\.expiredAt' packages internal --include='*.go' \| grep -v _test` | **0**. The only reader is the build-tagged `internal/leaseconvergence` harness (parent §2.4). This design adds the first production reader; the field's constraint (monotone max) is already the parent's Inc 1 acceptance criterion. |
| C6 | engine null semantics | `internal/refractor/ruleengine/full/values.go:170-173` | `compareAny` returns **`false`** when either operand is nil, every ordering op; `truthy(nil)` is false (`:107`). Two-valued, fail-closed both ways — the parent's §5.1 table holds here (§3.2). |
| C7 | live tasks vs markers (stack at `localhost:4222`, `deploy/nkeys/lattice.nk`, 2026-09-05 08:22 UTC) | census script in the fire's scratchpad; 102 `vtx.task.*` roots, 63 `.freshnessExpiry` markers read raw | open **73**: open-past-deadline **52**, of which **52 carry a marker with a `byTarget` entry ≥ `expiresAt`** (42 `staleAssignedTasks`, 10 `unroutedTasks`); open-future **21**, **0** marked. 2 markers with an empty `byTarget` (pre-Inc-1 shape, `{expiredAt}` only) — both on **completed** tasks. Zero open-past task without the fact. |
| C8 | live `cap.ephemeral` | 177 keys in `capability-kv` read raw | 163 empty, 27 grants across 14 docs, max 3 per doc, **0 grants past `expiresAt`** — the marker write already re-projects the actor (§4). |
| C9 | live sweep signal on this lens | `health-kv` → `health.refractor.*` → `metrics.capabilityLens.capabilityEphemeral` | `reconciled: 38`, `auditEnrolled: false` — refusal *"it projects onto the auth plane, whose per-row verdicts are the convergence sweep's"*, i.e. audit enrolment refuses this lens on plane, not on `$now`. |
| C10 | who reads the clock-reference property | `grep -rn 'ReferencesParam(' internal --include='*.go' \| grep -v _test` | `pipeline/audit.go:990` (audit enrolment — plain/business lenses), `anchor_derivation_plain.go:333` (plain licence), `anchor_derivation_personal.go:528` (personal licence). **None applies to an actor-aggregate capability lens** — removing `$now` flips no licence for this lens (§4). |
| C11 | retraction transport | `pipeline/anchor_derivation.go:94-101` | an aspect event derives anchors from its **parent vertex** at every pattern position binding that type; corpus pins: `anchor_hopindex_corpus_census_test.go:86` (`capabilityEphemeral: hopIndexed`), `actor_walk_scope_corpus_census_test.go:120` (`task:assignedTo,forOperation,queuedFor,scopedTo`). |
| C12 | ClaimTask past the deadline | `packages/orchestration-base/ddls.go:398-440` | checks `status == open` only — a lapsed queued task **can** be claimed. Decides §3.3. |
| C13 | the café op's own guard | `packages/cafe-domain/ddls.go:1345-1378` | `SettleStaleTab` re-checks `status == open` only; it never compares `staleAt` — the deadline verdict is the lens's alone, as it is today. |
| C14 | test scaffolding that injects `now` for this lens | `grep -rln capabilityEphemeral … \| xargs grep -ln '"now"'` | 7 files (§10 lists each with its treatment). |

---

## 3. The shape

### 3.1 The lens edit — one predicate, three arms

`capabilityEphemeralSpec` (`lenses.go:448`) stops being a `const` and is built once at package init with
`fmt.Sprintf`, the `unroutedTasksSpec` idiom (`:225-245`), so the marker aspect name is the one constant
`mark_expired.go` owns; each arm's WHERE becomes:

```
-  WHERE task.data.status = 'open' AND task.data.expiresAt > $now
+  WHERE task.data.status = 'open' AND NOT (task.freshnessExpiry.data.expiredAt >= task.data.expiresAt)
```

(and identically for `task2`, `task3`). The RETURN is unchanged: the grant field shape
`{source, taskKey, operationType, target, expiresAt}` is Contract #6 §6.6's and is not touched.

**The row asks "has any timer fired at or after the deadline I am about?"** — which is what `expiredAt`
records: *the latest instant ANY target lapsed on this entity* (`mark_expired.go:23-26`). The lens is not
a convergence target and owns no `byTarget` entry; it is an **observer** of the lapse, and the field the
marker keeps for exactly that reading is the family-wide maximum. §3.3 prices the per-target form.

**Soundness of `expiredAt >= expiresAt`, both directions.** Every instant that reaches `expiredAt` is a
`fireAt` a scheduled message was *delivered* for (`temporal.go:150-158`; `"expiredAt": p.FireAt` at
`:301`), so it is ≤ the wall clock at the moment it was recorded; the merge keeps the maximum, which never
runs ahead of any recorded instant (`mark_expired.go:39-42`); and only `MarkExpired` may write the aspect
(`freshnessExpiry` aspect DDL, `PermittedCommands: ["MarkExpired"]`, `:138`). Therefore
`expiredAt >= expiresAt ⟹ some fire happened at an instant ≥ expiresAt ⟹ now ≥ expiresAt` — **expired,
whichever target fired** — for every instant Weaver's temporal lane records. Conversely, once
`now ≥ expiresAt` the task's own routing-shape target has projected `freshUntil = expiresAt` and armed the
`@at` (an already-past deadline is published verbatim and fires at once, `temporal.go:150-153`), so the fact
arrives after one fire + one commit + one reprojection — the window §5 prices.

**The one writer that is not Weaver (adversarial pass, finding 2).** `MarkExpired` is granted at
`Scope: "any"` to the `operator` role (`packages/orchestration-base/permissions.go:105-114`) — the primordial
admin and the engine service actors (`privacy-operator-grant/permissions.go:10-15`) — and the script only
normalises `expiredAt` (Starlark has no clock to clamp against). An operator can therefore record a
**future** instant, and the monotone fold keeps it: that task's ephemeral grant is retracted for good. The
direction is **closed** (a denial, never a grant), and the operator already holds `CancelTask`
(`permissions.go:81`), so the path confers no authority the role lacks — but it is the asymmetry the
per-target form does not have: under `byTarget.<self>` a poisoned entry under any *other* target id is
inert, under the entity-wide maximum any target id retracts the auth lens. Priced in §8 row 3; §11 carries
it as the re-open trigger.

### 3.1.1 Cost — one point read per candidate task, unbatched (finding 1)

The predicate sits in an **inline `OPTIONAL MATCH … WHERE`**, which the executor evaluates per expansion
row (`ruleengine/full/executor.go:640-660`); aspect prefetch (`prefetchAspects`) is reached only from a
`WITH` stage, so `task.freshnessExpiry` is one Core-KV point read per candidate task, per arm, per actor
reprojection (`values.go:23-60` — an aspect absent from the node's hydrated props is fetched by key). Today
the predicate reads `task.data.expiresAt`, a root-body field already loaded with the node: **zero reads**.

- **The bound.** Candidate tasks per actor = the actor's open direct assignments + its reports' + the open
  tasks queued to roles it holds — the same set the RETURN already dereferences for `op.description` (an
  aspect on a walked node, read the same way). Live: max 3 grants per doc, 27 across 177 docs (C8). The ten
  converted lenses pay exactly this read per row on their anchor (`a2dfd339`); this lens pays it per
  candidate task, which is the same order.
- **Not hoisted.** Moving the three predicates to a `WITH … WHERE` stage would batch the reads but
  re-shape the pattern graph that eleven corpus pins hold (`g3/o9[…]` branch decomposition, the grouping
  reduction, the WITH-carried scalars) for a lens whose live p95 is 17.7 ms at 3 tasks per actor (C9,
  `lensLatency.capabilityEphemeral`). Decided: **inline, measured** — Increment 1's acceptance records
  `lensLatency.capabilityEphemeral` p95 before and after on the dev stack, and re-opens the hoist only if
  the read is visible at the actor's candidate count.
- Each dereferenced marker key enters the evaluation's read-surface footprint (`executor.go:1125-1131`),
  which is what makes the marker write re-validate the row on drift — intended, and now stated.

### 3.2 Null semantics — verified, not assumed (C6)

| Task state | `expiredAt >= expiresAt` | `NOT (…)` | Row in `ephemeralGrants`? | Same as today? |
|---|---|---|---|---|
| open, future deadline, no marker yet | `null >= X` → **false** | true | **yes** | yes (`X > $now` true) |
| open, past deadline, marker landed (`expiredAt ≥ X`) | true | false | **no** | yes |
| open, past deadline, marker not yet landed | false | true | **yes, briefly** | **no** — today excluded on the next evaluation. Processor denies it (§5). |
| open, `expiresAt` absent | `E >= null` → false | true | yes | **no** — today `null > $now` is false. `CreateTask` requires `expiresAt` (`ddls.go:118` `"required"`), so the population is empty by construction; §10 pins the never-written row anyway. |
| complete / cancelled | irrelevant | — | no (`status` gate) | yes |

The absent-aspect case binds nil rather than dropping the row (`values.go:73-79`), so an unmarked task
does not vanish from the OPTIONAL MATCH; and unlike `equalsAny`, where `null <> X` is true, the ordering
family is the one that fails closed in both directions — the reason the parent chose the comparison form
(§5.1 there) and the reason the negation is safe here.

### 3.3 Why the family maximum, not the per-target entry

The convention the parent set is *"a convergence lens reads its own entry"* — `byTarget.<its targetId>` —
because two targets on one anchor re-arm independently and a sibling's later instant would otherwise read
as one's own lapse (parent §5.3). That hazard is a **re-arm** hazard: it needs a target whose deadline
*moves past* a sibling's recorded instant. The ephemeral lens's deadline is `expiresAt` itself, and the
argument in §3.1 shows any recorded instant ≥ `expiresAt` proves expiry regardless of author — so the
per-target isolation buys this reader nothing, and the alternative costs it two things:

- **A coupling to two other lenses' populations.** Arm 1/2 would read `byTarget.staleAssignedTasks`, arm 3
  `byTarget.unroutedTasks`; the constants keep the *names* aligned by construction, but the claim "arm k's
  population is target k's population" is a claim about three WHERE clauses staying aligned forever, and its
  failure mode is silent inclusion of expired grants until task closure (the row's own harm).
- **A window the maximum sometimes closes.** `ClaimTask` admits a lapsed queued task (C12). If the
  `unroutedTasks` `@at` fired **before** the claim, the task carries that entry ≥ `expiresAt` while its
  `staleAssignedTasks` entry does not exist until that target's overdue `@at` fires; a per-arm read would
  include the grant for that interval, the maximum excludes it at once. If the claim landed **first**, the
  `unroutedTasks` row is gone when the timer fires and `handleFiredTimer` drops the firing on the absent row
  (`temporal.go:259-268`), so no entry is written and both forms close on `staleAssignedTasks`' own overdue
  `@at` — the maximum buys nothing there (adversarial pass, finding 7). The coupling bullet above is the
  argument that decides; this one only narrows a window it cannot always remove.

Rejected: per-target entry per arm (§8 row 3).

### 3.4 The café lens — the parent's Inc 3 shape, now that its timer arms

`cafeStaleTabSettlement` left the parent design because its `BodyColumns` omitted `freshUntil`, so no
`@at` ever armed and converting it would have shut the gap permanently. That bug shipped its fix on
2026-09-02 (`b569fd2c`; `cafe-domain/lenses.go:69` now lists `freshUntil`), so the lens is an ordinary
anchor-hosted member and converts exactly as the ten did — it **is** a convergence target, so it reads its
own entry:

```
-  CASE WHEN (open) AND (t.status.data.staleAt > $now) THEN t.status.data.staleAt ELSE null END AS freshUntil,
-  ((open) AND (t.status.data.staleAt <= $now)) AS missing_settle,
+  CASE WHEN (open) AND NOT (t.freshnessExpiry.data.byTarget.cafeStaleTabSettlement >= t.status.data.staleAt) THEN t.status.data.staleAt ELSE null END AS freshUntil,
+  ((open) AND (t.freshnessExpiry.data.byTarget.cafeStaleTabSettlement >= t.status.data.staleAt)) AS missing_settle,
```

and the third occurrence — `violating`'s first disjunct **repeats the expression literally rather than
naming the alias** (`lenses.go:401`), so it is a third edit, not a consequence:

```
-    ((t.status.data.value = 'open') AND (t.status.data.staleAt <= $now))
+    ((t.status.data.value = 'open') AND (t.freshnessExpiry.data.byTarget.cafeStaleTabSettlement >= t.status.data.staleAt))
     OR ((t.status.data.value = 'open') AND (t.status.data.staleAt = null))
```

`missing_staleat` — `staleAt = null` — is untouched and still dispatches `BackfillTabStaleAt`. **The
`staleAt = null` arm's `freshUntil` branch flips** (finding 6): `NOT (marker >= null)` is `NOT false` =
true, so the CASE takes its `THEN` branch where today it takes `ELSE`; the column is still null only because
`THEN t.status.data.staleAt` is itself null. Correct, and pinned by name in §10 so the accident is a
witnessed property rather than a coincidence. `freshUntil` is now the deadline verbatim until the lapse is
recorded, so an already-past `staleAt` arms an overdue `@at` instead of never opening — the parent's §5.4
finding, which this lens inherits. The target id is already a constant the cypher can be built from
(`StaleTabSettlementTarget`, `lenses.go:16`). `SettleStaleTab` never re-checks `staleAt` (C13), so the
lens's verdict is the whole gate, as today.

### 3.5 The gate — a corpus census pin, blocking, zero debt

`internal/refractor/lens_clock_reference_corpus_census_test.go`, reusing `forEachCorpusCypher`
(`label_derivation_corpus_census_test.go:575`) per the Refractor dossier's standing rule — enumerate every
parseable corpus body through the **real** analysis, `CompiledRule.ReferencesParam` (`full/params.go:32`),
never a grep of cypher text:

- `ReferencesParam("now")` must be `(false, true)` — not referenced, exhaustively provable — for **every**
  lens and branch in the corpus. No allowlist.
- `ReferencesParam("projectedAt")` referenced ⇒ the lens is in the pinned set `{capabilityRoleIndex}` with
  its reason (a bare `$projectedAt AS projectedAt` output column, which the sweep excludes as volatile);
  the set is asserted exactly, so a second one fails by name and must argue its own case.
- A floor on the enumerated count (the corpus is > 60 declarations) so an empty enumeration cannot read as
  a clean table.

The package-level `TestTaskDeadlineLenses_ReferenceNoClockParameter` (`orchestration-base/lens_cypher_test.go:605`)
gains `capabilityEphemeral`; its doc comment, which today says the lens *"still read[s] the clock, for
reasons [its] own doc comment[] carr[ies]"*, is rewritten. The corpus pin is the gate; the package pin is
the local witness.

---

## 4. The payoff, honestly sized

- **The rule becomes a gate.** "A lens never reads the clock" is your direction and the parent design's
  thesis; after this fire the corpus census reads zero and the pin refuses the next one, blocking, with no
  standing exception to explain.
- **The sweep verdict on the auth plane stops being a clock reading.** The convergence sweep's deep verify
  re-executes the lens with a fresh `now` (`pipeline/evaluate.go:770-777`) and classifies against the stored
  row (parent §4). On the capability path a divergent streak of **2** consecutive sweeps escalates to
  `error` (`health/lattice_heartbeater.go:1131-1137`, const `:187`) — the one place in the corpus where the
  parent's `warning`-only finding does not hold (parent §2.7). After conversion the row is a pure function of
  the subgraph; a divergence on `cap.ephemeral` means a defect.
- **Retraction posture: unchanged in effect, now by design rather than by accident.** Today the
  `MarkExpired` write on the task *already* re-projects every actor whose grant lists it (C11), and the
  `$now` re-evaluation then excludes it — which is why C8 finds zero expired grants live. That retraction is
  incidental: it rides a write made for another target's row. After conversion the same write is the
  lens's own input, so the retraction is the projection's stated mechanism.
- **What it does not buy, stated so nobody claims it later:** no licence flips (C10 — audit enrolment,
  plain and personal derivation licences do not read this lens; audit enrolment refuses it on plane, C9);
  the sweep's `reconciled: 38` on this lens is not attributable to the clock without a classification the
  sweep does not keep. **What it costs:** one unbatched point read per candidate task per actor
  reprojection (§3.1.1), measured at Increment 1.

---

## 5. The window, and the gate that already covers it

Between `expiresAt` and the marker landing, the converted lens projects a grant whose `expiresAt` is in the
past — a population today's lens excludes at its next evaluation. **Widening a population ⇒ the gate over it
is proven no weaker (designer §2.3 D):**

- The gate is `step3_auth_capability.go:349-358`: `if !now.Before(expiresAt) { continue }` — every grant
  entry is re-checked against the Processor's own clock at every dispatch; a past `expiresAt` is a
  non-match, `AuthContextMismatch`. Pinned by `TestCapabilityAuthorizer_TaskPath_Expired` (`:459`). The
  rule before and after is the same rule; the member the widened population adds (open, lapsed, unmarked)
  is refused by it.
- The window's **length** is one fire + one commit + one reprojection on the routing-shape target's
  overdue `@at`; the parent's §8 residual applies unchanged — a `MarkExpired` rejected at the Processor
  re-executes on the next CDC touch of the entity (Contract #4 §4.4: a rejected op lands no tracker, so the
  deterministic requestId is not a duplicate).
- The window's **width** — how many such grants a doc can hold — is bounded by the count of open, lapsed,
  unmarked tasks reaching the actor, which the census puts at **0 of 52** on a stack that has run the
  targets for days (C7). The row's unbounded-growth harm needs the marker never to land, i.e. a stopped
  temporal lane, which is a standing Health issue of its own (`SchedulePublishError`, `temporal.go:186`).
- **Direction of failure named:** the widened member fails **closed at dispatch** (denied) and **open in
  the read model** (listed). A reader of `cap.ephemeral` that treats presence as authorization without the
  `expiresAt` check would be wrong today too — the parent's §2.6 placement argument, and Contract #6 §6.6's
  own field table, put the clock at lookup. Loupe renders the doc; nothing else consumes it (C9's health
  entry is the only other reader and reads counts).

---

## 6. State table — the marker is the parent's; the lens adds no state

The `freshnessExpiry` marker's lifetime is the parent design's §6, unchanged (created on first fire as a
declared-absent `create`, merged in place, permanent, survives tombstone and rebuild). This design adds
**no state**: no new target, no `@at`, no `byTarget` entry, no cache. Per task, the outcome column:

| Row | Marker | Lens outcome | Note |
|---|---|---|---|
| never lapsed | none | granted | today's behaviour |
| lapsed, fire landed | `expiredAt ≥ expiresAt` | retracted | today's behaviour, now by design |
| lapsed, fire pending / rejected | none or stale | granted until the fire lands; **denied at dispatch** | §5 |
| `expiresAt` moved **later** after a fire | `expiredAt < new expiresAt` | granted again, no write | self-corrects (parent §5.2); the target re-arms its `@at` from the new deadline |
| `expiresAt` moved **earlier** than a recorded instant | `expiredAt ≥ new expiresAt` | retracted | correct — a timer did fire at or after it |
| queued → claimed after the lapse (C12) | `unroutedTasks` entry ≥ `expiresAt` | retracted at once | the maximum closes the per-arm window (§3.3) |
| completed / cancelled, any marker | irrelevant | retracted by `status` | `CompleteTask` carries `expiresAt` forward unchanged; status is the liveness signal (`lenses.go:437-441`) |
| revived (`make_vtx_revive_occ`, `ddls.go:373`), same deadline | survives, ≥ deadline | retracted | correct and deliberate (parent §6 "entity revived") — a revived task needs a new deadline to grant |
| pre-Inc-1 marker `{expiredAt}` with no `byTarget` | `expiredAt` present | reads as today's rule says | C7: both live instances are on completed tasks; the field was a fire instant then too |
| `expiresAt` never written | none | granted | unreachable (`CreateTask` requires it); pinned as a never-written row (§10) |

---

## 7. Reconciliation with the existing model

- **Didn't we already handle this?** The parent design handled the *anchor-hosted* family and filed this
  member out on a premise its own Increment 3 then falsified (§1.2). The `myTasks` lens deliberately projects
  an expired-but-open task and is not a `$now` reader — untouched (`lenses.go:344-357`).
- **Does this contradict "a convergence lens reads its own entry"?** No — it extends it: a convergence
  *target* reads `byTarget.<self>` (the café conversion does); an *observer* with no entry of its own reads
  the family maximum the marker keeps for that purpose (`mark_expired.go:23-26`). The doc comment at
  `mark_expired.go:20-29` gains one sentence saying so (§9), and `docs/components/weaver.md:231-238` (the
  "reads a recorded fact rather than a clock" paragraph) gains the observer case.
- **Does this add state we keep elsewhere?** No. The fact, the timer and the marker are the parent's; the
  lens consumes them.
- **The parent's own severity table.** Its §2.7 says the `error` escalation is reachable only on this lens
  and that it *"leaves the design"*; §4 above is that paragraph coming due.

---

## 8. Alternatives

| # | Alternative | Verdict |
|---|---|---|
| **1** | **Do not do this — keep `$now` in `capabilityEphemeral` (and the café lens).** The world unchanged: zero expired grants projected live (C8), the Processor's clock at lookup, the retraction riding the marker write by accident. What it costs: the corpus rule keeps a standing exception on its most security-sensitive member, so the gate in §3.5 can only ship with an allowlist (a convention, not a gate); the sweep's `error`-reachable verdict on the auth plane stays a clock reading; and the parent design's §2.7/§2.6 stay open. Your direction was *all* uses. **Rejected**, as the smallest fire that closes the sweep. |
| 2 | Drop the `expiresAt` conjunct with no replacement; rely on the Processor's check alone. | The row's own analysis: every open task past its deadline grants indefinitely — an open task past its deadline persists by design — so `cap.ephemeral.<actor>` grows with the actor's history and the doc is scanned on every dispatch. **Rejected** (the parent's §2.6). |
| 3 | Read the per-target entry per arm (`byTarget.staleAssignedTasks` for arms 1/2, `byTarget.unroutedTasks` for arm 3). | Mirrors the convention for targets, and isolates the lens from a poisoned entry under a *foreign* target id (§3.1's operator path); but it couples the lens to two sibling populations, and a poisoned entry under its *own* target id retracts identically, so the isolation is partial while the coupling is total. **Rejected** — the maximum is sound for every Weaver-recorded instant (§3.1), fails closed on the one non-Weaver writer, and needs no coupling. Re-open if a task-anchored target ever records a `fireAt` that is not a delivered deadline, or if the operator path is ever widened below `operator`. |
| 4 | A dedicated `ephemeralGrantExpiry` Weaver target on the task, owning its own `byTarget` entry. | Dead scaffolding: the two shipped targets already arm an `@at` at `expiresAt` for every open task (§1.2); a third would fire the same instant, mint a third entry and a third `MarkExpired` per task, and exist only so the lens could follow the target convention. **Rejected.** |
| 5 | Convert only `capabilityEphemeral`; leave the café lens and ship the gate with a one-entry allowlist. | The café lens's exclusion reason is gone (`b569fd2c`) and its conversion is the parent's Inc 3 shape, S. A gate with an allowlist is a convention; the designer rule is *blocking when the migration leaves zero debt*. **Rejected**; the café conversion is Increment 2. |
| 6 | Exclude `expiresAt`-derived rows from `classifyDivergence` instead. | The parent's alternative 2, rejected there for making the sweep blind to the column class rather than able to judge it. **Rejected** for the same reason. |

**Each rejection run back against the recommendation:** 2 was rejected for unbounded growth — the
recommendation bounds the projected set by the marker, which C7 shows lands for 52/52; 3 was rejected for
coupling — the recommendation reads one field one op owns; 4 for dead scaffolding — the recommendation adds
no target; 5 for an allowlisted gate — the recommendation's gate has none; 6 for blinding the sweep — the
recommendation makes the row verifiable.

---

## 9. Contract + document surface

- **`docs/contracts/*` — untouched; builds to Contract #6 §6.6 and Contract #10 §10.7** (§0). The
  observable promises — the grant field shape, `expiresAt > now` at lookup, `AuthContextMismatch` on
  no-match — are unchanged and stay true.
- `packages/orchestration-base/lenses.go:433-441` — the lens's doc comment paragraph on `$now` is
  rewritten to state the recorded-fact read and the observer/maximum reasoning (§3.3).
- `packages/orchestration-base/mark_expired.go:20-29` — one sentence: an observer lens with no entry of its
  own reads `expiredAt`, the family maximum, which is why the field is constrained rather than decorative.
- `docs/components/weaver.md:231-238` — the observer case, one sentence; `docs/components/refractor.md`
  gains the new corpus census in the list its dossier's standing rule maintains.
- **Package versions:** `orchestration-base` 0.7.18 → 0.7.19, `cafe-domain` 0.11.31 → 0.11.32 — manifest
  **and** the mirroring `Version` constant (`DIFF_BASE=<base> go run ./scripts/lint-package-version.go`).
- No new `scripts/lint-*.go`: the gate is a corpus census test under `go test ./internal/refractor/`, the
  shape the Refractor dossier mandates for per-lens analyses (a grep gate would agree with a broken
  analysis and would trip on the comment mentions C1 filtered out).

---

## 10. Test strategy — every prescribed test owned by an increment (§12)

**Unit, `packages/orchestration-base/lens_cypher_test.go` (fixture `newUnrFixture`, `projectIdentitySpec`
at `:238`) — Inc 1.** The helper stops accepting `now` for `capabilityEphemeralSpec` (passing one *"would
let a clock-reading regression pass unnoticed"* — the file's own rule at `:8-13`). Vectors, one per §6 row:
never lapsed (granted); lapsed with `expiredAt ≥ expiresAt` (retracted); lapsed with **no** marker
(granted — the deliberate fail-direction, asserted with the reason in the test name); `expiresAt` moved
later than a recorded instant (granted); moved earlier (retracted); claimed-after-lapse with only the
`unroutedTasks` entry (retracted — the §3.3 window); completed with a marker (retracted by status); a
marker with an empty `byTarget` and a past `expiredAt` (retracted); **never-written `expiresAt`** (granted;
pinned as the never-written row). Each of the three arms gets the lapsed/unlapsed pair, since one clause
over a multi-shape set has disabled an arm before (designer §2.3 D).

**Population-coverage pin, same file — Inc 1.** For each arm's fixture task (assigned; assigned-to-a-report;
queued), project the task through the routing-shape target's spec (`staleAssignedTasksSpec` /
`unroutedTasksSpec`) and assert a row with `freshUntil = expiresAt` — the executable form of §1.2's
"every projected task is in one of the two populations", so a future WHERE edit on either target that
strands an arm fails here by arm name.

**Corpus + engine, `internal/refractor` — Inc 0 and Inc 3.** Inc 0: the 4-deep read
`x.freshnessExpiry.data.expiredAt` is 3-deep and already covered by `aspect_expression_shapes_test.go`;
the OPTIONAL-MATCH-bound-node aspect read (the marker sits on a *walked* node, not the anchor) gets a
sibling case there before any cypher converts. Inc 3: `lens_clock_reference_corpus_census_test.go` (§3.5).

**Existing vectors that move — Inc 1 (C14).**
- `full/bootstrap_e2e_test.go:184-190` `taskexpired` (*"PAST expiry (must be filtered)"*): gains a marker
  `{expiredAt: past}` to stay filtered, and a sibling `taskexpiredUnmarked` asserted **present** in the doc,
  so the fail-direction is pinned where the old expectation lived.
- `full/hopindex_test.go:99-121` — `shippedCapabilityEphemeral`, a **hand-copied replica** of the spec's
  pattern sources (*"verbatim"*) carrying all three `> $now` clauses; nothing fails when the shipped spec
  changes (finding 4). Updated to the new predicate in Inc 1, and its comment gains the sentence that it is
  a copy the corpus census does not see.
- `full/capability_ephemeral_queued_role_contract_test.go`, `full/capability_lens_contract_test.go`,
  `full/branch_decomposition_equivalence_test.go`, `pipeline/anchor_derivation_differential_test.go`,
  `projection/footprint_classifier_test.go`: future-dated tasks with no marker — unchanged outcome; the
  `now` parameter they pass becomes inert for this lens and is removed where the file passes it for this
  lens alone. `refractor_capability_multi_e2e_test.go:313`'s comment names the old predicate; rewritten.
- The eleven corpus census pins (`branch_decomposition_corpus_census_pins_test.go`,
  `grouping_reduction_…`, `label_derivation_…`, `actor_walk_scope_…`, `anchor_hopindex_…`,
  `actor_onekey_…`, `auth_plane_narrowing_census_test.go`, …): the predicate edit adds an aspect read on
  an already-bound node and no relation, label or grouping — every pin is expected to hold unchanged, and
  the fire's Phase 0 runs them to prove it rather than asserting it.

**Sweep-verdict regression — Inc 1.** The parent pinned *"a converged lens straddling a deadline is
`divergenceNone`"* for the anchor-hosted set (`a2dfd339`); the same vector on `capabilityEphemeral` — an
actor with one grant whose deadline passes between projection and sweep, marker landed — is pinned
`divergenceNone`, with the clock form's `divergenceContent` recorded beside it for the record.

**e2e, `internal/refractor` — Inc 1.** `refractor_claim_batch_real_op_with_ephemeral_e2e_test.go` is the
precedent (a real op + the live `capabilityEphemeral` pipeline on the CDC pump). One sibling: a real
`MarkExpired` submitted against an assigned task whose grant is projected → the actor's `cap.ephemeral`
row loses the grant, via `deriveAnchorsForAspect` from the task — the retraction transport of C11 crossed
end to end, not asserted from the census.

**Café, `packages/cafe-domain/lens_cypher_test.go:107,331` — Inc 2.** The `now` injection for
`staleTabSettlementSpec` goes; vectors mirror the parent's Inc 3 set (not lapsed / lapsed / moved later),
plus the legacy tab — `staleAt` absent → `missing_staleat` true, `missing_settle` false, `violating` true,
`freshUntil` null — named for the branch flip in §3.4 (`TestStaleTab_NoStaleAt_FreshUntilNullByTheThenBranch`
or equivalent), and a lapsed-and-marked vector asserting all three converted occurrences agree.

---

## 11. Risks + residuals

- **The lens's correctness now depends on the temporal lane running.** So does every anchor-hosted
  convergence lens since `a2dfd339`; a stopped lane is a standing `SchedulePublishError` issue
  (`temporal.go:186-192`), and the Processor's lookup check makes the failure mode *listed-but-denied*, not
  an over-grant (§5). Recorded, not mitigated further.
- **The first production reader of `expiredAt`.** The field's monotone-maximum constraint was an acceptance
  criterion of the parent's Inc 1 (`make test-lease-convergence`); it is now load-bearing on the auth plane.
  The `mark_expired.go` comment says so after this fire (§9); the parent's concurrent-fire pins already
  prove neither entry — and therefore the maximum — can be lost.
- **A recorded instant that is not a delivered deadline** breaks §3.1's universal claim. Weaver cannot
  produce one (`scheduleFreshness` is the only timer-payload producer and copies the row's `freshUntil`,
  `temporal.go:150-158`); an **operator** can (§3.1's operator path), and the effect is a permanent
  fail-closed retraction of one task's grant — visible as the task still open in `myTasks` and absent from
  `cap.ephemeral`. Alternative 3's re-open trigger: a task-anchored target that records anything but a
  delivered deadline, or the `MarkExpired` grant widened below `operator`.
- **Not in scope:** `myTasks` (deliberately clock-free and expiry-inclusive); the `$projectedAt` output
  column in `capabilityRoleIndex` (pinned, not converted); any change to `MarkExpired`.

---

## 12. Decomposition for the Steward — one fire, three green increments

**Increment 0 — premises + the engine pin** *(XS)*: re-run C1, C2, C7, C8 at the fire's base SHA; add the
walked-node marker-read case to `aspect_expression_shapes_test.go` (§10). Gate on the census reading
exactly the two lenses and the pin being green before any cypher changes.

**Increment 1 — `capabilityEphemeral` reads the recorded lapse** *(S; posture-changing — it edits the
auth-plane lens)*: the `fmt.Sprintf` rebuild of the spec with the three-arm predicate (§3.1); the doc-comment
rewrites in `lenses.go` and `mark_expired.go` (§9); the unit vectors, the population-coverage pin, the
`bootstrap_e2e` vector split, the sweep-verdict regression and the `MarkExpired` retraction e2e (§10);
`capabilityEphemeral` added to `TestTaskDeadlineLenses_ReferenceNoClockParameter`; orchestration-base
0.7.19. **Acceptance:** all eleven corpus census pins green unchanged; `go test ./internal/refractor/...
./internal/processor/... ./packages/orchestration-base/...`; the `capabilityEphemeral` health entry on the
dev stack shows no `LensProjectionDiverged` after a deadline crossing (MERGED ≠ RUNNING: read the
`cap.ephemeral.<actor>` header after the marker lands, per `reference_read_a_readmodel_row_live`).

**Increment 2 — `cafeStaleTabSettlement`** *(S)*: the predicate + `freshUntil` conversion (§3.4), the
café vectors, cafe-domain 0.11.32. **Acceptance:** the café `lens_cypher_test` clock-free; the stale-tab
`@at` still arms (`b569fd2c`'s witness) and now from the deadline verbatim.

**Increment 3 — the gate + docs** *(XS)*: `lens_clock_reference_corpus_census_test.go` (§3.5) — lands
**last** so it is green on arrival with zero debt and no allowlist for `now`; the `weaver.md` /
`refractor.md` lines (§9). **Acceptance:** the census enumerates every lens (floor asserted), `now`
unreferenced everywhere, `projectedAt` referenced exactly by `capabilityRoleIndex`.

Review depth is the Steward's sizing (`agents/steward/SKILL.md` §4); Increment 1 is the posture-changing
one and carries the platform risk. Dossier entries that apply (copy into the brief): Refractor — *the
standing corpus-census rule* (a claim about the corpus cites the executable pin); *a fail-closed posture
proved on the delivery axis is not proved on the projection axis* (§5 names both axes); *an absence gate over
a resolved-set field asserts both vectors* (the no-marker and empty-`byTarget` rows are both pinned).
Weaver — *a shared fixture that always supplies an optional input pins only the supplied case* (the
`now` parameter is removed from the ephemeral helper rather than defaulted).

---

## 13. Checklist walk (designer/SKILL.md §2.3) — the items that bit

- **A. The demand is a hypothesis.** The row's *"no fact to record"* was a premise inherited from the
  parent's §2.6; opening Inc 3's diff (`a2dfd339`) refuted it before the shape formed, and the live census
  (C7) put a number on the refutation. The filed no-pattern dissolved into a read.
- **A. Ground the mechanism, not the instance.** The harm the row names (unbounded growth) needs the marker
  never to land; the mechanism that lands it is the two targets' `@at`, and its failure is a standing Health
  issue, not a silent one (§5, §11).
- **B. Name the transport and verify it carries the data.** `deriveAnchorsForAspect` seeds the parent
  vertex at every binding position (C11); the e2e in §10 crosses it rather than citing it.
- **B. "Mirrors X" — read the twenty lines above X.** `mark_expired.go:20-35` is where `expiredAt`'s
  meaning and constraint live; reading it is what made the family maximum the right field for an observer
  and the per-target entry the wrong one (§3.3).
- **C. Run every census you write.** C1–C14 above, raw output pasted; C7's 52/52 and C8's zero are the
  numbers the payoff section is sized by, not the row's prose.
- **D. Widening a population ⇒ the gate over it is no weaker.** §5 names the widened member, the gate, its
  pin, and the direction each axis fails.
- **D. Write the state table with an outcome column, including the never-written row.** §6, including
  `expiresAt` never written and the pre-Inc-1 marker shape found live.
- **F. A `no-pattern:` prescription is solution-shaped.** *"a recorded expiry fact on a task readable by an
  auth-plane inclusion filter"* described a fact that existed; re-deriving the need turned a mechanism row
  into a predicate edit.
- **§3 item 7 — row one is "do not have this thing".** §8 row 1, priced with C8's zero in its favour.

## 14. Adversarial pass — run 2026-09-05, eight findings, all folded

Cold reviewer (read-only, `opus`) against the first draft and the cited code. Nothing blocking; two
findings reshaped a section.

| # | Finding | Disposition |
|---|---|---|
| 1 | "No measured cost moves" was unsupported: the inline OPTIONAL-MATCH WHERE is evaluated per expansion row with no aspect prefetch, so the marker read is one unbatched point read per candidate task. | **Folded — §3.1.1.** Bounded by the actor's candidate count (live max 3), same read the ten converted lenses pay per row; hoisting rejected for re-shaping eleven pinned corpus properties; measured at Inc 1. |
| 2 | §3.1's "every instant is a delivered `fireAt`" ignored the operator: `MarkExpired` is `operator@any`, the script cannot clamp, the fold is monotone — a future instant retracts the grant for good; and per-target isolates a foreign poisoned entry where the maximum does not. | **Folded — §3.1, §8 row 3, §11.** Direction named (closed); operator already holds `CancelTask`; the asymmetry priced and the re-open trigger widened. |
| 3 | §1.2's `sed` blocks were edited transcripts (rendered ids, elided `MATCH`). | **Folded** — real output pasted, with the `%[1]s` seam explained. |
| 4 | `hopindex_test.go:99-121` carries a hand-copied replica of the spec with the `$now` clauses; a spec change leaves it modelling a cypher nobody ships. Plus a stale comment in `refractor_capability_multi_e2e_test.go:313`. | **Folded — §10.** |
| 5 | The café diff converted 2 of 3 `$now` occurrences: `violating` repeats the expression literally at `:401`. Inc 3's gate would fail on arrival. | **Folded — §3.4** spells the third edit out. |
| 6 | The café `staleAt = null` arm's `freshUntil` branch flips and stays null only because the `THEN` value is null. | **Folded — §3.4, §10** (pinned by name). |
| 7 | The claim-after-lapse window closes on the maximum only when the `unroutedTasks` fire preceded the claim; otherwise `handleFiredTimer` drops the firing on the absent row and no entry exists. | **Folded — §3.3** weakened; the coupling argument carries the decision. |
| 8 | Cite drift: `mark_expired.go:39-42`, `cafe-domain/lenses.go:69`, `lens_cypher_test.go:605`, `step3:357`. | **Folded.** |

Claims that survived, with the reviewer's evidence: the null semantics and two-valued `NOT`
(`values.go:170-172`, `:107-110`, `:69-79`; `expr_eval.go:85-98`; `executor.go:668-678`); the transport —
no localName filter exists before derivation, narrowing is by vertex-type label and `task` is in the label
set (`pipeline/filter.go:43-47`), aspect events derive from the parent (`anchor_derivation.go:91-100`) with
the adjacency-walk fallback when derivation declines (`evaluate.go:1233-1240`), and `hopIndex` recurses
`*Not`/`*PropertyAccess` (`hopindex.go:913-923`); population coverage (`10-orchestration-substrate.md:35-39`,
`freshUntil` in both `BodyColumns`); the gate's semantics (`params.go:32-61`, all thirteen `Expr` types
cased; `forEachCorpusCypher` enumerates expanded walks, branches and the four bootstrap lenses); the contract
surface — §6.11 (`06-capability-kv.md:385-388`) says the cypher *"evaluates static graph topology only, and
rejection on temporal grounds belongs to the operation's own logic"*, which is this design's placement
argument in the contract's own words; and no lint or text pin keys on the spec (`lint-lens-anchors.go:36-48`
fires only on a ranged hop inside a negation; the cypher carries no literal `%`).
