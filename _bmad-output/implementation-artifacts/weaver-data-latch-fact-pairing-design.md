# Weaver — the `data:` latch is read-driven: pair every clear to its fact

**Status:** ✅ Winston-ratified — build-ready (2026-08-25). No frozen-contract change: Contract #5 §5.2
fixes the issue *document* shape (severity / code / message / since), never the engine's internal key
space, and no severity or code changes here.

**Item:** `backlog/lattice.md` — *[Weaver] The `data:` latch is read-driven, so entries strand and
`planGap`'s log cannot be damped* (★★ · M).

**Scope sentence.** Re-pair every clear in the `data:` issue family to the fact it retires, and pace the
per-pass record `planGap` writes for a parked gap on a CLOCK rather than on latch presence.

---

## 1. The mechanism, grounded

`issueKeyDataEntity(targetID, entityID, col)` (`evaluator.go:1437`) keys "a value in THIS row is not the
§10.2 shape the reader expects". Its doc claims a closed loop: *"the read that raises an entry also
retires it"*. Two things break that claim.

### 1.1 One key, two facts — the gap column's own read erases the template fact

`planGap` raises `TemplateDataError` at `issueKeyDataEntity(targetID, entityID, col)` where `col` is the
**gap column** (`evaluator.go:565`). `boolColumn` raises `RowDataError` at the *same* key for the same
column's own type (`evaluator.go:972`). They are different facts — *this row's template references
resolve null* vs *`missing_x` is not a bool* — sharing one latch.

The erasure is not a race, it is the ordinary path. Every delivery that reaches `planGap` for `col` has
already read `col` as a bool at least twice, and each read clears the key:

| order | site | effect on `data:<t>.<e>.<col>` |
|---|---|---|
| 1 | `handleRow` → `clearClosedMarks` → `boolColumn(col)` (`evaluator.go:827`) | cleared (value parses) |
| 2 | `handleRow` → `openGapColumns` → `boolColumn(col)` (`evaluator.go:1311`) | cleared (value parses) |
| 3 | `dispatchGap` → `planGap` → `errData` (`evaluator.go:565`) | re-raised |

`issueCache.clear` deletes the `since` entry with the issue (`health.go:118`), so step 3 re-stamps
`since` to *now* on **every delivery**. Contract #5 §5.5 wants "open since it first arose"; the operator
reads a fault that has stood for a week as one that arose seconds ago. It also makes every raise an
ARRIVAL, which is exactly what defeats `alertStanding`'s arrival-vs-repeat test (`evaluator.go:1362`) —
the damping seam shipped for the same class one item earlier (`057286f`).

### 1.2 A column whose read stops happening strands its entry

`boolColumn` / `intColumn` retire an entry only when someone reads that column again. Four columns have
raise paths that stop being reached while the entry stands:

| column | read sites | the read stops when |
|---|---|---|
| `inflight_<g>` | `evaluator.go:394`, `:1133` | the gap closes — `markCandidateColumns` (`:1283`) enumerates `missing_*` **only**, so no later pass reads the companion |
| `maxretries_<g>` | `evaluator.go:1136`, `:1164` | same; also skipped on any pass where `inflight_<g>` is true (`:1133` returns first) |
| `priority` | `evaluator.go:587` | the entity has no open gap (`admitGap` is per-dispatch), or the target's `Admission` block is removed (`:584` short-circuits) |
| `missing_<g>` (template fact) | — | §1.1: retired by an unrelated read, which is the opposite defect |

A stranded entry holds for the process's lifetime, one per (entity, column), and is re-sorted into every
heartbeat document — the exact multiplication the `data:` split's dossier entry (`docs/components/weaver.md`)
was minted for. The three existing teardowns do not reach it: the entity-delete prefix clear
(`evaluator.go:824`) needs a tombstone, and `issueKeyTargetPrefixes` (`:1395`) needs the target to leave.

### 1.3 `planGap`'s per-pass record cannot be damped by latch presence

For a **parked** gap, `planGap` runs once per reconciler sweep pass (`defaultSweepInterval = time.Minute`,
`reconciler.go:21`) from `reclaim` (`reconciler.go:1040`), the count leg's re-arm (`:728`), the goal
leg-advance (`:887`) and `escalateExhaustedGap` (`evaluator.go:1273`), for the dispatch-count TTL's whole
life (`dispatchCountTTLBackstopFactor = 256` × a 30-min lease ≈ 128h, `state.go:64`) — **~7,680 passes per
parked `(target, entity, gap)`**. Each failing pass writes a loud record unconditionally:
`errTransient` → `Warn` (`evaluator.go:557`), `errConfig` → `alert` → `Error` (`:568`).

Swapping those for `alertStanding` does **not** damp them, because both keys are cleared by a path that is
not evidence the fact ended:

- the `errData` key is erased by the gap column's own read (§1.1);
- the `errConfig` / `errTransient` key is `issueKeyGapConfig(targetID, col)` — **target-scoped** — and
  `clearClosedMarks` clears it whenever **any one entity's** column stops being reported
  (`evaluator.go:839`). On a mixed population one entity's close retires the fact another entity's parked
  gap is still raising, so the next pass is an arrival again.

`errConfig` and `errTransient` **survive a row write** — a row projection does not fix a broken playbook or
an unresolved reference — so the latch's absence is not evidence of repair, and an arrival test built on it
reports an arrival every pass. The damping memory must therefore be a **clock** the clear cannot erase.

## 2. Decisions (Winston, this fire)

1. **Split by fact, not by scope.** The template fact gets its own key family
   (`issueKeyTemplateEntity`, prefix `template:`), mirroring the `issueKeyGap` → `GapEntity`/`GapConfig`
   split (`ac7cd921`). The alternative — teaching `boolColumn` not to clear what it did not raise — puts a
   code-aware condition inside a shared reader and leaves two facts racing one `since`.
2. **A companion column's data error is retired by the gap close**, because the close is what ends the
   read. `priority` is retired when the entity's last open gap closes (the same pass), never per-gap: a
   per-gap clear on a multi-gap entity would flap.

   > **AMENDED 2026-08-25 (two cold reviews).** "The gap close" named one leg where there are **three**,
   > and this section's §1.2 census listed the raise *sites* without asking which of them can run
   > *after* a retirement. `clearClosedMarks` runs on a DELIVERY; the sweep observes the same ending at
   > `deleteMark`'s gap-closed arm, at `deleteCount`, and at the row-gone arm — and for a row that has
   > gone quiet the sweep is the only leg that ever runs at all. Two consequences, both shipped as
   > fixes: a quiet row retired nothing, and a sweep holding a row at its own revision re-raised the
   > companions *after* lane-1's clear, with nothing to retire them again.
   >
   > **The retirement set is not one set — that was a first-cut error, caught by the close pass.** Two
   > helpers, split by which fact the leg has actually witnessed. `retireGapPlanIssues` (the template
   > fact + the two companions) retires what is raised only on the way to a dispatch, and is what the
   > ORPHAN routes (`orphanColumn`, `targetRemoved`) may retire: a playbook that drops a gap has not
   > ended anything about the row. `retireClosedGapIssues` adds `issueKeyGapEntity` and belongs only to
   > a leg that saw the column go false or the row vanish — clearing that latch on an orphan column
   > fights lane 1, which keeps raising `GapBudgetExhausted` there for exactly that column
   > (`openGapColumns` enumerates every true `missing_*`, playbook or not), making the latch flap and
   > re-stamping the `since` of a fact that never stopped holding. `reconciler.go`'s count leg and
   > `escalateExhaustedGap`'s surface arm both already said this in words; the first cut broke it
   > anyway. The corrupt-count arm is excluded for the mirror reason: it sits below the column check,
   > so the gap is still open there.
3. **Log pacing is call-site scoped and clock-keyed.** A new `alertPaced` seam serves `planGap`'s failure
   switch only. `alert` and `alertStanding` are untouched — the shared-primitive edit was tried and
   reverted one item ago (`weaver-exhausted-gap-durable-stop-design.md` §3.2b), and the reason still
   holds: `TimerDataError` is event-shaped and must stay loud per occurrence.
4. **The pace memory is not the fact.** The Health issue is still `set` on every pass (latch semantics
   unchanged, message always current); only the *log* is paced. A severity or code change at the same key
   is a different fact and arrives loudly immediately.
5. **`logPaceInterval = time.Hour`.** At the 1-minute sweep cadence this turns ~7,680 loud records per
   parked gap into ~128 — a 60× reduction — while never letting a standing fault go quiet in the log for
   more than an hour. Intermediate passes log at `Debug`, never nothing (the §3.2b regression: a record
   dropped entirely is a record nobody can recover).

### 2.1 The pace memory's lifetime (checklist item 1 — state needs a lifetime)

| boundary | rule |
|---|---|
| created | first paced emission at a key |
| refreshed | every loud emission (arrival, severity/code change, interval expiry) |
| carried | across `issues.clear` — that is the point; a clear is not evidence the fact ended |
| retired (subject gone) | the same prefix teardowns that retire the issue families: entity tombstone, `Revoke`, `reconcileConsumers` |
| retired (age) | pruned at the heartbeat when older than 2× the interval — such an entry would emit loudly on its next raise anyway, so pruning it is behaviour-neutral and bounds the map by "keys raised in the last two intervals" |
| crash / restart | empty, like `issueCache` itself; the first post-restart raise is an arrival, which is correct |

## 3. Increments

**Inc 1 — fact-scoped template key.** Add `issueKeyTemplateEntity` + `issuePrefixTemplate`; move
`evaluator.go:565`'s raise and `:546`'s clear onto it; add the new prefix to `issueKeyTargetPrefixes`
(`:1395`) and to the entity-tombstone prefix clear (`:824`); add the per-column clear at the gap-close
site (`:838`). Green: `go test ./internal/weaver/...` plus new tests — a delivery cycle does not move the
template fact's `since`; the gap closing retires it; the entity tombstone and `Revoke` retire it.

**Inc 2 — companion-column re-pairing.** In `clearClosedMarks`'s closed-column arm, retire
`data:<t>.<e>.inflight_<g>` and `data:<t>.<e>.maxretries_<g>`; retire `data:<t>.<e>.priority` when no
candidate column stayed open. Green: a raise on each column followed by the gap close leaves no entry;
a multi-gap entity with one gap still open keeps its `priority` entry.

**Inc 3 — `alertPaced`.** `issueCache` gains the pace map + `shouldLogPaced(key, severity, code, now)`
and `prunePaced(now)`; `Engine.alertPaced` logs loud on arrival / severity-code change / interval expiry
and `Debug` otherwise, always `issues.set`; `planGap`'s three failure arms call it. Green: a fact
re-raised every minute for two hours logs loudly twice; a clear between passes does not make it loud; a
code change does; the Health entry is set on every pass; the pace map is bounded.

**Close.** Cumulative adversarial pass over the whole diff; classify findings into
`docs/components/weaver.md`'s dossier.

## 4. Non-goals

`alert` / `alertStanding` semantics; `issueKeyGapConfig`'s target-vs-entity scope (the clear at
`evaluator.go:839` is the only retirement a config fact can reach when a column stops being reported —
narrowing it is a dispatch-semantics change, and §1.3's flap is addressed by the clock instead); the
`freshUntil` family (already level-paired at every arm, `temporal.go:107/140`); Health-KV document shape.

---

## 5. Fire brief (build note, 2026-08-25)

**Scope sentence** — §0 above, verbatim.

**Verified touch-list** (checked live at `c4a5673`): `internal/weaver/evaluator.go` — `:87/:89`
(entityKey pair, unchanged reference), `:546`, `:565`, `:824`, `:827`, `:838-839`, `:960-1017`
(`boolColumn`/`intColumn`), `:1283` (`markCandidateColumns`), `:1334-1369` (`alert`/`alertStanding`),
`:1371-1401` (prefixes + `issueKeyTargetPrefixes`), `:1437`. `internal/weaver/health.go` — `:76-164`
(`issueCache`), heartbeater. Read-only context: `internal/weaver/reconciler.go:21`, `:728`, `:887`,
`:1040`; `internal/weaver/state.go:64`; `internal/weaver/control.go:237`; `internal/weaver/engine.go:514`;
`internal/weaver/temporal.go:97-170`.

**Precedents to mirror** — the `issueKeyGap` entity/config split (`ac7cd921`) for a fact-scoped key and
its teardown wiring; `alertStanding` (`evaluator.go:1362`) for the damping seam's shape and narrowness;
`issueCache.clearPrefix` (`health.go:136`) for prefix teardown; `markStore.deleteByTargetPrefix`'s
trailing-separator rule for prefix safety.

**Increment order + green checks** — §3. Full gate set: `go build ./...`, `make vet`,
`golangci-lint run ./...`, `STRICT=1 go run ./scripts/lint-conventions.go`,
`STRICT=1 go run ./scripts/lint-doc-orphan.go`, `go test ./internal/weaver/...`, then
`POSTGRES_TEST_DSN=… go test ./... -p 4`.

**In-scope gotchas** — the dossier entries this fire trips, from `docs/components/weaver.md`: *a Health
issue key is a LATCH — before adding a CLEAR, enumerate every OTHER leg that raises at that key*;
*segmenting by entity is safe only where a clear site names that exact COLUMN — enumerate the raise
COLUMNS, not the raise functions*; *a per-entity issue is unbounded and the heartbeat is ONE KV value*;
*a leg's arms are a lattice — every RETIRE belongs above every "cannot act" GUARD*; *a target leaves the
registry by more routes than the teardown verb*; *prove each changed line by reverting THAT LINE*. Plus
the standing checklist — item 1 (lifetime: §2.1), item 2 (census: the column table in §1.2 was
re-verified live), item 3 (revert-proof each line), item 6 (precedent may carry debt: the `data:` key doc
at `:1423` asserts a closed loop this fire proves open — amend it).

**Adjacent finds** — none filed: everything this fire surfaced is inside its own scope.

**Non-goals** — §4.

---

## 6. Build note (2026-08-25)

Increments 1–3 built, then a review round on the cold review of Inc 1+2, then a cumulative close pass.
Deviations from §3, each argued at the code:

- **Inc 1** removed `planGap`'s success-arm `data:` clear rather than re-pointing it. A built plan
  disproves the template fact; it says nothing about whether the gap column carries a §10.2 bool, and
  every path into `planGap` has already read that column through `boolColumn` on the same pass.
- **Inc 3** ships **one** seam, not two: `alertPaced` derives its loud level from the caller's own
  severity, so a `warning` cannot be logged at Error. The `errTransient` arm's structured log line
  folded into its Health message, losing the `entityId` attribute — kept deliberately: under pacing one
  pass in N logs loudly, so an entity attribute would name whichever row tripped the window rather than
  the row at fault, on a fact that is target-scoped by construction.
- **The review round** replaced the legs' inline clear-lists with the two shared helpers §2 decision 2's
  amendment describes, reaching all three close-observing legs.
- **§2 decision 4's damping cost more than the design priced.** Making the config fault's log quiet
  without making its age true would have left an operator no surface at all for "how long has this
  stood" — a regression on §1.1's own criterion at the one family the design chose not to split. The
  pace entry is therefore the durable arrival stamp for the families `alertPaced` serves: a raise whose
  clock still holds the same severity and code re-dates the issue from that arrival instead of minting
  a fresh `since`. It carries `lastRaiseAt` (updated on every raise, loud or damped) so the continuity
  only spans a gap shorter than one interval — the memory cannot tell a fault that never stopped from
  one that stopped and restarted inside a single interval, and anything longer starts a fresh `since`.

**Residuals, deliberate and named in the code rather than papered over.** A quiet row whose gap column
holds a non-bool keeps that entry — a true statement about a row that still exists, retired by any later
clean delivery or by the teardowns. The sweep legs do not retire the `priority` entry: they hold one
mark and cannot see the entity's whole candidate set, which is what decision 2's no-flap rule requires;
in practice a gap close is a row write, so lane 1 reaches it. On an orphan column whose row still
projects malformed companions, the orphan delete clears those two entries once and lane 1's next read
re-raises them with a fresh `since` — a single stamp reset on level-driven entries whose reader
re-establishes truth immediately, and one that mints no spurious alert because nothing about them goes
through an arrival test.

**One discovery routes out** (`agents/steward/SKILL.md` §4's designer out): `issueKeyGapConfig` strands on
the orphan-column route. It is target-scoped, and the sweep — which sees one entity — cannot tell "the
column left the target" from "one entity's gap ended", so retiring it needs a primitive that does not
exist: a per-target observed-column set (or a latch TTL). Filed on `backlog/lattice.md` as
`📐 needs designer pass · no-pattern:`, not fixed here.

## 7. Checkpoint

Fire branch `claude/great-lamport-c6lifp`. Increments land as one merge to `main` when the whole item is
green (a partial split would leave a raise and its teardown on different keys).
