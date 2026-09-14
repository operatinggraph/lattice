# Clinic — recurring visit series (the `@every` clinic consumer) — design

**Status:** ✅ ratified (steward impl-ratified — no contract change, no fork; per CLAUDE.md rule #2 a
no-contract / no-fork design is the lead's to impl-ratify). Build-ready for a Vertical build fire.
**Owner:** Vertical Steward (pkg = clinic-reminders + clinic-domain; FE = Sally + FE Engineer).
**Backlog row:** verticals.md → "Recurring `@every` schedules — the clinic forcing function" (★★★ M).
**Grounds in:** Contract #10 §10.4 (FROZEN temporal lane), the `freshUntil` convention (§10 lines 169–171),
`packages/clinic-reminders/lenses.go` + `followups.go` (the established convergence pattern),
`packages/clinic-domain/package.go:58–67` (the deferral this closes).

---

## 1. The open question this design resolves

The backlog row was filed `🚧 needs-design`, NOT build-ready, with one explicit fork:

> a clinic consumer needs a design call (package `@at`-rolling-deadline pattern **vs** a new Weaver
> package-recurring lane = lattice), not a blind build.

This doc makes that call. **Decision: the recurring clinic need is built as a package-level
rolling-`@at` convergence series. NO new platform primitive. The `@every` substrate primitive
(`ScheduleEvery`, Fire 1) stays reserved for *singleton platform-wide* cadences.** Rationale below
(§3). This is pure vertical/package work within the frozen §10.4 lane — no lattice gap to file, no
contract change, no paper-over.

## 2. What "recurring" actually means for the clinic

The board names two candidate recurring needs. Ground both before designing:

- **Recurring *availability*** (a provider's weekly hours, e.g. "every Mon/Wed 09:00–17:00"). This is
  **already shipped and is NOT a timer** — `clinic-domain` stores `.hours = {windows: [{day, openSec,
  closeSec}]}` as a static weekly template (`ddls.go:138`), enforced at `CreateAppointment` /
  `RescheduleAppointment` time. It needs no `@every`. The `package.go:67` note ("the
  recurring-availability case still needs @every") is **stale** — weekly hours are a declarative
  template, not a scheduled fire. (This design corrects that comment.)

- **A recurring *visit series*** — the genuine, un-served recurring need. A patient on a standing
  cadence: chronic-care monthly check-ins, weekly physical-therapy, a quarterly review. The clinic
  wants the system to keep a **"next visit in the series is due"** worklist gap rolling forward on its
  own — when occurrence *N* is handled, occurrence *N+1* (one interval later) arms itself, until the
  series ends or is paused. **This is the consumer this design builds.**

A recurring visit series is structurally a **rolling generalization of the one-shot follow-up**
(`followups.go`): a follow-up fires once at a single `followUpDate`; a series re-arms its own next
deadline each time it converges. Same convergence machinery (aspect + op + `freshUntil`-armed `@at`
lens + `directOp` playbook), made to roll.

## 3. Why rolling-`@at`, not a per-series `@every`

> **Amended 2026-09-13 (§9):** the lens no longer arms an `@at` at all — an occurrence is credited by a
> qualifying *visit* (a level-triggered read of the appointment corpus), never by the deadline passing. The
> argument below against a per-series `@every` still holds; the rolling-`@at` it argued *for* is gone.

`@every` (`substrate.ScheduleEvery`) publishes ONE durable, singleton schedule message that re-fires
into one fired-subject forever (§10.4 "Recurring schedules"). It is the right tool for Weaver's single
global sweep (`schedule.weaver.sweep`, the only recurring consumer today). It is the **wrong** tool for
a per-entity recurring series, for four reasons:

1. **Entity-scoped vs singleton.** A clinic runs thousands of independent series, each with its own
   cadence, start, end, and pause state. Arming a distinct `@every` NATS schedule per series is a
   per-entity proliferation of substrate schedules — the exact "state lives in timers" anti-pattern the
   lattice avoids. In the lattice idiom, **state lives in the read model; timers are *derived* from it.**

2. **Level-reconcile safety / self-healing.** The rolling-`@at` lens **derives** the next deadline from
   persisted state (`lastOccurrenceAt`, `intervalDays`, `activeUntil`), so a lost, delayed, or
   redelivered fire just re-projects the same gap — Weaver's sweep re-arms it. A raw per-series `@every`
   has no such derivation: if its schedule message is lost, that patient's series silently stops, with
   nothing to reconcile against. The convergence model is the whole point of building *on* Lattice.

3. **Lifecycle is a state edit, not a schedule dance.** Pause / resume / end of a series is an aspect
   edit the lens already reflects (deadline projects `null` while paused / past `activeUntil` → no fire;
   resumes when the aspect flips back). A raw `@every` requires explicit `CancelSchedule` + re-arm per
   series, off the convergence path, with its own crash-safety burden.

4. **Zero new platform seam.** Rolling-`@at` is fully expressible with the *shipped, contract-blessed*
   `freshUntil` → `@at` seam (§10 lines 169–171) + a convergence lens + an "advance" op. There is no
   missing primitive → no `lattice.md` gap, no paper-over. (Contrast: a *blind* per-series `@every`
   would still need a brand-new package-facing `@every` seam — a lattice add — to even reach the
   substrate. Rolling-`@at` is both more correct *and* cheaper.)

### 3.1 The boundary — when a package `@every` seam WOULD be justified (and why we don't file it now)

A package-facing `@every` seam (a real lattice add) is justified **only** for a *singleton,
clinic-wide, stateless* cadence — same shape as the Weaver sweep. Example: "every morning project a
clinic-wide day-ahead digest." That is **not** a recurring *series* (no per-entity state, one fire for
the whole clinic). No such consumer exists today, and the No-Paper-Over rule cuts both ways: we do
**not** file a phantom `lattice.md` gap for a primitive with no consumer. If/when a singleton package
cadence is genuinely demanded, *that* fire files the seam (a thin `Lens → contextHint`-style "arm an
`@every` for this target" declaration, mirroring how `freshUntil` arms `@at`). Recorded here so the
boundary is explicit, not rediscovered.

## 4. Build plan (increments — for the build fire)

All in **clinic-reminders** (the convergence-owning package; `clinic-domain` stays projection-only per
`package.go:68–70`) except the series-state aspects, which clinic-domain mints on the patient. Mirror
`followups.go` exactly — it is the closest precedent.

### Inc 1 — series state + the convergence lens + the advance op (package, no FE)

> **Amended 2026-09-13 (§9):** `freshUntil` / the `@at` and the fixed-grid roll from `dueFor` below are
> superseded — the gap opens on a qualifying visit (`handledAt`, `startsAt >= nextDueAt`) and the advance
> re-anchors `nextDueAt = handledAt + intervalDays`. §9 is the text of record for both.

- **Aspect (clinic-domain, on the patient — or a new lightweight `vtx.visitseries.<id>`; pick patient
  to avoid a new vertex type unless a patient can hold >1 concurrent series → then a series vertex).**
  `recommend: a dedicated vtx.visitseries.<NanoID>` keyed to a patient+provider, so multiple
  concurrent cadences per patient are first-class. Aspects:
  - `.series = {patientKey, providerKey, intervalDays, startAt, activeUntil?, reason?}` (the cadence
    definition; `intervalDays` an int, dates RFC3339 UTC normalized to 09:00:00Z like `followUpDate`).
  - `.progress = {lastOccurrenceAt, occurrenceCount}` (advanced by the op; absent until first fire).
  - `.paused = {value: bool}` (optional lifecycle toggle).
- **Ops (clinic-domain or clinic-reminders — keep the *write* of `.series`/`.paused` in clinic-domain
  as domain state; keep the *advance* in clinic-reminders as convergence):**
  - `StartVisitSeries{patientKey, providerKey, intervalDays, startAt, activeUntil?}` → mints the series
    vertex + `.series`. (clinic-domain.)
  - `PauseVisitSeries` / `ResumeVisitSeries` → toggle `.paused`. (clinic-domain.)
  - `AdvanceVisitSeries{seriesKey, dueFor}` → the **directOp the playbook dispatches**: stamps
    `.progress = {lastOccurrenceAt: dueFor, occurrenceCount+1}`, read-guarded on `[seriesKey]`,
    UNCONDITIONED update (idempotent under at-least-once — the `RecordFollowUpReminder` idiom).
    (clinic-reminders.)
- **Lens `visitSeriesDue` (weaver-target, full engine).** One row per active series. The rolling
  deadline:
  - `nextDueAt = (.progress.lastOccurrenceAt ?? .series.startAt) + intervalDays·days` (cypher-computed;
    null-safe — first occurrence anchors on `startAt`).
  - `freshUntil = CASE WHEN active AND nextDueAt > $now THEN nextDueAt ELSE null END` — arms an `@at`
    at `nextDueAt` while it is future; goes null once the deadline passes (the OPEN-the-gap polarity,
    exactly like `appointmentRemindersSpec`, `lenses.go:39`).
  - `series_due = active AND nextDueAt <= $now` — the violating row the playbook converges.
  - `active = NOT paused AND (activeUntil IS null OR nextDueAt <= activeUntil)` — a series past
    `activeUntil` projects no deadline and no gap (clean termination, no cancel needed).
- **Playbook (targets.go):** `series_due → directOp(AdvanceVisitSeries, dueFor: row.nextDueAt)`.
  Exactly the `missing_followup_reminder → directOp(RecordFollowUpReminder)` shape.
- **Convergence semantics:** advance stamps `lastOccurrenceAt = nextDueAt` (NOT `$now` — keeps the
  cadence on a fixed grid, immune to fire latency drift), `nextDueAt` rolls forward one interval,
  `freshUntil` re-arms the next `@at`. Exactly-one fire per occurrence; a catch-up after downtime fires
  each missed occurrence in turn (level-reconcile), which is the desired "don't silently skip a
  patient's check-in" behavior. (If skip-to-latest is ever wanted, the advance op can fast-forward
  `lastOccurrenceAt` to the most recent past grid point — note, don't build, until a PO asks.)
- **Tests:** mirror `followups_cypher_test.go` + `integration_test.go` — first-occurrence-from-startAt,
  roll-forward, pause/resume (deadline drops + re-arms), `activeUntil` termination, catch-up after a
  gap. `make verify-package-clinic-reminders` (DDL/keys touched).

### Inc 2 — FE (Sally → FE Engineer): the series worklist + start/pause

- A clinic-wide **"Visit series due"** worklist (reads the `visitSeriesDue` lens read-model — P5, never
  Core KV), grouped by urgency like the follow-ups worklist; one-click **Book** the due visit (reuses
  the existing booking flow, pre-filling patient+provider) which is what *handles* the occurrence.
- On the patient view: **Start a recurring series** (interval + start + optional end) and
  **Pause/Resume**. In-browser verified per the FE Engineer playbook (cycle `bin/clinic-app`).

## 5. Contract / lattice impact

**None.** Stays entirely within frozen §10.4 + the `freshUntil` seam. No contract edit, no `lattice.md`
gap (§3.1). Self-ratified. The only cross-file touch outside the package is correcting the stale
`clinic-domain/package.go:64–67` deferral comment when Inc 1 lands.

## 6. Why this is the right altitude

This closes a ★★★ deferral with the *simplest extension of existing state* (the Designer-blind-spot
rule): no new mechanism, just the follow-up convergence made to roll. It also demonstrates the
lattice's central claim — that "recurring business process" is a derived read-model projection, not an
external scheduler — on a real vertical need, which is precisely the kind of forcing function the
clinic vertical exists to provide.

## 7. Fire brief — front-desk (`frontOfHouse`) grant + workplace confinement

**1 · Scope sentence (verbatim, verticals board):** *"`StartVisitSeries` (Follow-ups tab) is
`operator`-only in the clinic-reminders package (separate version + confinement helpers from
clinic-domain); a real `frontOfHouse` session gets `AuthDenied`. Add the `frontOfHouse` grant +
workplace confinement (mirror clinic-domain's `CreateAppointment`), consumer: front-desk Follow-ups
tab."* Grant rationale: persona-worlds-design.md §7.1 (clinic front-desk hats include Follow-ups).

**Scope decision (Winston — intent-preserving widening, recorded).** The named consumer is *the
Follow-ups tab*, and that one staff surface submits three visit-series ops — `submitStartSeries` →
`StartVisitSeries` and `toggleSeries` → `PauseVisitSeries`/`ResumeVisitSeries`
(`cmd/clinic-app/web/app.js:2728,2778`); the patient view (`handleMyVisitSeries`) is read-only.
Fixing only `Start` would leave a real `frontOfHouse` session still `AuthDenied` on Pause/Resume from
the *same tab* — the identical bug class. So the grant + confinement land on **Start + Pause + Resume**
(don't-over-split-coupled-work). `AdvanceVisitSeries` stays operator-only — it is Weaver's directOp,
dispatched under the service-actor, never a front-desk action.

**2 · The security divergence from `CreateAppointment` (the load-bearing decision).** clinic-domain's
`workplace_exempt()` exempts `op.authContextTarget == op.actor` — safe *there* only because a
downstream `identifiedBy` patient-binding check backstops the self-book path
(`clinic-domain/ddls.go:2072-2079`, and its own comment says a scope=any caller forging `target==actor`
"gains nothing" *because of* that second check). `StartVisitSeries`/Pause/Resume are **staff-only**
(no `consumer`/`self` grant, no ownership backstop), so copying that exemption verbatim would let a
`frontOfHouse` actor forge `target==actor` (the Gateway forwards authContext verbatim; step-3
authorizes scope=any without inspecting target) and skip workplace confinement outright. **Therefore
clinic-reminders' `require_workplace` is OPERATOR-EXEMPT ONLY — the `authContextTarget == op.actor`
line is dropped.** A dedicated forged-target vector (frontOfHouse, cross-building provider,
`target==actor`) asserts `Rejected`, pinning this divergence.

**3 · Touch-list (file:line, mirror source in parens):**
- `packages/clinic-reminders/visitseries.go` — `visitSeriesScript` gains the confinement helpers
  `actor_holds_operator` / `worksAt_covers` / `sites_for_provider` (verbatim, incl. `# read-posture: (e)`
  annotations, from `clinic-domain/ddls.go:1608-1757`), a `series_provider(series_key)` withProvider
  resolver (mirrors `appointment_provider`, `ddls.go:1723-1736`), and an **operator-exempt-only**
  `require_workplace`. Confinement calls: `StartVisitSeries` → `require_workplace(sites_for_provider(
  provider_key), …)` after the provider-alive check; Pause/Resume →
  `require_workplace(sites_for_provider(series_provider(series_key)), …)` after the liveness guard.
  `visitSeriesPermissions()` special-cases Start/Pause/Resume to `{operator, frontOfHouse}` (Advance
  stays `{operator}`). Confinement reads are all live class-(e) enumerations — **no `contextHint`
  change**, no FE change.
- `packages/clinic-reminders/manifest.yaml` — Start/Pause/Resume `grantsTo: [operator, frontOfHouse]`;
  `version: 0.6.0`.
- `packages/clinic-reminders/package.go` — `Version: "0.6.0"`.
- `packages/clinic-reminders/package_test.go` — `TestPackage_Permissions` expects Start/Pause/Resume
  `[operator, frontOfHouse]`, the rest operator-only.
- `packages/clinic-reminders/frontdesk_confinement_test.go` (NEW) — mirrors clinic-domain's: two-building
  topology, frontOfHouse `worksAt` A only; vectors = same-building accept / cross-building reject /
  operator-unconfined / **forged-`target==actor` reject** / Pause+Resume confined.

**4 · Gates:** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go test ./packages/clinic-reminders/...`. `make
verify-package-clinic-reminders` (stack gate) pins only the RecordAppointmentReminder permission — no
edit needed. Live: `make reinstall-package PKG=clinic-reminders` (0.5.0→0.6.0 diff-applies in place, no
teardown); the running stack picks up the new grants; no binary rebuild (package-only change).

**5 · Non-goals:** no FE change (hat-gating is persona-worlds W1 in flight); no `AdvanceVisitSeries`
grant; no `contextHint` change; no contract text; no self/consumer path.

**Scope-diff gate: PASS** — every touch traces to the grant+confinement sentence; the one widening
(Start→Start/Pause/Resume) is intent-preserving (the named "Follow-ups tab" consumer submits all three)
and recorded above, never a substitution of an adjacent mechanism; the operator-exempt-only divergence
is a *narrowing* of the mirrored precedent (fewer exemptions), grounded in the missing ownership backstop.

## 8. Build note — the fused `active` boolean splits into a three-state read (Winston, 2026-07-27)

**Scope sentence** (verbatim, verticals.md): *"A naturally-ended visit series still shows a working Resume
button — `active` fuses 'not paused' with 'not past `activeUntil`', so an ended-not-paused series shows
Resume; it submits and succeeds but changes nothing observable. Needs a raw `paused`/`activeUntil` column +
a 3-state badge."* Surfaced as a named residual of facet-entity-browse-design.md §9 once Facet made the
Pause/Resume pair reachable from its staff worklist; the same fusion was always live in `cmd/clinic-app`'s
own patient-self view (§4's own formula, above), just never rendered a button on the *ended* branch there.

**What shipped.** `visitSeriesReadSpec` (the FE-facing Postgres lens; `visitSeriesDueSpec` — Weaver's own
convergence question, "should this fire next" — is untouched, a different question) replaces `active BOOLEAN`
with `series_status TEXT ∈ {"active","paused","ended"}`, a sequential `CASE`: paused is judged first (a
series paused before its own end still reads "paused", never "ended" — pausing is what the human did, and
wall-clock catching up to a frozen `activeUntil` comparison shouldn't reclassify it out from under them),
then "ended" (`activeUntil` set AND `nextDueAt` past it), else "active" — mirroring the raw-enum idiom
`appointmentStatus`/`bookingStatus`/loftspace listing-status already use, not a new pattern.

Every consumer of the old boolean moved to the new field: `clinic-reminders`'s Pause/Resume op-metas'
`dispatch.visibleWhen` (`{field:"active"}` → `{field:"series_status", equals:"active"/"paused"}`) — which is
also what Facet's staff worklist pane reads, no app change needed there; `edge-manifest`'s `panes.go`
`visitSeries` section column (`active` badge/boolean → `series_status` badge/text, default `"ended"` — the
safer bias when a row hasn't yet re-projected under the new shape, matching "offer nothing" over "offer the
wrong toggle"); `cmd/clinic-app`'s `protectedVisitSeriesRow`/`renderMySeriesCard`/`renderSeriesCard`/
`seriesUrgency`/`toggleSeries`. `renderMySeriesCard` (the patient-view card the bug named) now renders NO
toggle at all once a series is `"ended"` — the direct fix — while the staff worklist's "Paused / ended"
urgency bucket keeps grouping both non-active states together, unchanged, since scheduling urgency doesn't
care which.

**Versions:** clinic-reminders 0.7.1→0.7.2, edge-manifest 0.14.5→0.14.7 (0.14.6 landed the unrelated
`instructorKey` provenance column in the same fire; see persona-worlds-design.md §7 "Resolved").

**Gates:** `go build ./...`, `go vet ./...`, `golangci-lint run ./...`, `STRICT=1 lint-conventions`,
`lint-lens-anchors`, `lint-package-standard`, `lint-package-version`, full `go test ./... -p 4` (shared
edge-manifest change) all clean; `go test ./packages/clinic-reminders/... ./cmd/clinic-app/... ./cmd/facet/...`
plus the full facet `node --test *.test.mjs` (156 vectors, extended for the third `visibleWhen` state and the
`series_status` pane fixture) green.

**Non-goals.** No change to `visitSeriesDueSpec`/Weaver's own convergence gate. No backend rejection added to
`ResumeVisitSeries` on an ended series — it stays a harmless idempotent no-op; the FE now simply never offers
it, which is the named fix. No Postgres migration script — the read model is Refractor-projected and
re-derives its full row shape on the next CDC event, per the Protected-lens convention.

## 9. Fire brief — an occurrence is credited by a visit, never by the clock (build note, 2026-09-13)

**Scope sentence** (verbatim, verticals.md): *"A recurring visit is credited on the clock, never on a booking —
`AdvanceVisitSeries` rolls at `nextDueAt` whether or not a visit was booked; the design says Book handles the
occurrence but nothing records it, so 'Due now' empties at Weaver latency. Live: Riley's series at occurrence 2,
no visit at the 08-15 grid point."* Green bar: *"an occurrence stays a worklist row until a visit on/after its due
date exists."*

**Decisions (Winston, §0 — recorded here, amending §3/§4 body text where they conflict, dated).**

1. **The gap is level-triggered on a recorded fact, not a clock lapse.** `missing_series_advance` opens iff the
   series is active AND a *qualifying visit* exists: an `appointment` `forPatient` the series' patient, whose
   `.status.data.value` is neither `cancelled` nor `noShow` (null-safe `<>`, the `.paused` idiom), whose
   `.schedule.data.startsAt >= .progress.nextDueAt`, and — when the series has a `withProvider` — with that same
   provider (a provider-less series accepts any provider). The row projects `handledAt = min(...startsAt)` over
   that set (nulls dropped by the fold, `aggregate.go`); the gap is `handledAt <> null`. **`freshUntil` and the
   `freshnessExpiry.byTarget.visitSeriesDue` conjunct are deleted** — §4's "freshUntil re-arms the next `@at`"
   and §3's rolling-`@at` framing described the clock-credit; a deadline passing now changes nothing on the
   platform (the desk's "Due now" bucket is the FE's own `nextDueAt < today`, `seriesUrgency`). Per the
   `_packages.md` dossier: a `freshUntil` with no marker reader is deleted, not kept.
2. **The advance re-anchors the cadence on the crediting visit.** `AdvanceVisitSeries` gains a required
   `handledAt` (the qualifying visit's `startsAt`, supplied by the playbook from `row.handledAt`) and writes
   `.progress = {lastOccurrenceAt: handledAt, nextDueAt: handledAt + intervalDays, occurrenceCount+1}` — "every
   N days from the last visit", not §4's fixed grid from `dueFor` (whose rationale was dispatch-latency drift,
   moot once the base is a recorded visit instant). `dueFor` stays as the audit fact (`occurredFor` in the
   event) and the op refuses `InvalidArgument` when `handledAt < dueFor`. Consumption falls out of the
   arithmetic: after the advance `nextDueAt > handledAt`, so the same visit never re-qualifies; a second visit
   booked at/after the new `nextDueAt` credits N+1 on the next evaluation (two visits booked ahead credit two
   occurrences — "a visit on/after its due date exists", per row).
3. **A series overdue with no visit is a worklist row, not a violating row.** Weaver converges gaps the
   platform can close; "book the patient" is the desk's, so `violating` is false there. The FE's
   `seriesUrgency` already buckets it "Due now" off `nextDueAt` and now the row *stays* (nothing advances it).
4. **FE:** `bookSeriesOccurrence` carries the floor — booking a day before `nextDueAt` would not credit the
   occurrence, so the Book calendar blocks those days with a reason and the lead line says "on or after <date>";
   the floor clears on submit / patient change / leaving Book. Pure predicate goja-pinned.

**Amended at build (2026-09-13, from the cold review — supersedes decisions 1–2 where they differ).**
- **The run is credited by its LATEST qualifying visit** (`handledAt = max(...)`, not `min`): Weaver's episode
  model treats a gap that stays violating with changed params as one stuck episode (the anti-storm mark Acks the
  re-projected row, the 30-min mark lease reclaims it, the 3-attempt directOp budget wedges the series at the 4th
  visit booked ahead — `GapBudgetExhausted`). One advance therefore consumes the whole booked run:
  `nextDueAt = latest + intervalDays` leaves every booked visit `< nextDueAt`, the gap closes, the mark clears.
  Decision 2's "two visits booked ahead credit two occurrences one evaluation apart" is withdrawn — they credit
  ONE occurrence, due `intervalDays` after the last of them ("every N days from the last visit").
- **The op reads `.progress` and refuses a stale row.** The playbook declares `row.entityKey.progress`; the op
  refuses `StaleRow` unless `.progress.nextDueAt == dueFor` and writes with `make_aspect_upsert_occ` pinned to
  the hydrated revision — a redelivered older row can no longer roll the series back. A same-params replay after
  the advance landed is refused `StaleRow` (the Processor dedups a same-requestId redelivery before the script).
- **FE floor is instant-granular where it matters:** the calendar blocks whole days before the floor; the slot
  list, the "Soonest available" picker and `#startsAt`'s `min` drop slots before the floor instant
  (`nextDueAt` carries the crediting visit's time-of-day after the first advance); `submitBook` refuses a
  `startsAt < floor` as the backstop (`beforeSeriesFloor`, goja-pinned).
- **Known platform ceiling (not a defect of this package):** the `apr` hop makes the provider a transit position
  for Refractor's affected-anchor derivation — one appointment event re-derives every series of every patient
  sharing that provider (past the 2000-read cap, the BFS fallback with the same walk scope). Negligible on the
  demo box; O(all series) per appointment event in a real clinic. The fix is a Lattice primitive (a walk seeded
  at (position X, vertex V) never needs to re-enter (X, V′≠V)) — chip filed for the Surveyor to row it in
  `lattice.md`; this lane cannot write that file.
- Live-upgrade residue: `@at` timers already armed under the old `freshUntil` fire once at their old `nextDueAt`
  and write a `freshnessExpiry.byTarget.visitSeriesDue` entry nothing reads (one harmless reprojection each).

**Verified touch-list** (live 2026-09-13):
- `packages/clinic-reminders/visitseries.go` — `visitSeriesDueSpec` :1114–1129 (add
  `OPTIONAL MATCH (p)<-[:forPatient]-(a:appointment)` + `OPTIONAL MATCH (a)-[:withProvider]->(apr:provider)`,
  `WITH … min(CASE WHEN … THEN a.schedule.data.startsAt ELSE null END) AS handledAt`, gap = active AND
  handledAt <> null; drop `freshUntil`); `visitSeriesDueLens()` :1046–1063 (`BodyColumns`: −`freshUntil`
  +`handledAt`); `AdvanceVisitSeries` branch :890–915 (+`handledAt`); its descriptor/param docs :99–103,
  :138, :151–152, :185–189; `.progress` DDL doc :261–290; the package-doc header :7–47 (rolling-`@at` framing);
  `visitSeriesDueTarget()` :1279–1300 (+`"handledAt": "row.handledAt"`, description).
- `packages/clinic-reminders/manifest.yaml` :2 + `package.go` :79 — `0.10.10 → 0.11.0` (semantic change).
- `packages/clinic-reminders/visitseries_cypher_test.go` — the freshness-lapse vectors (:79–260, :360–440)
  become visit vectors (below); `integration_test.go` :697–736 `TestAdvanceVisitSeries_RollsForward` (+`handledAt`).
- `cmd/clinic-app/web/app.js` — `bookSeriesOccurrence` :4000–4011, `dayBlockedReason` :2547–2561,
  `renderSlotCalendar` :2565, `#book-lead` (index.html :42), `submitBook` reset :3037, `setPatient`; new goja pin
  in `cmd/clinic-app/` mirroring `lifecycle_transitions_test.go`.
- `docs/components/_packages.md` dossier (close pass) · this doc §3/§4 body (dated strike, decision 1–2).

**Precedents to mirror.** Reverse walk from a walked neighbour: `packages/clinic-domain/lenses.go:938`
(`(p)<-[:forPatient]-(a:appointment)`); a gap gated on a neighbour-side existence with an aggregate pulled out of
the group: `packages/clinic-ledger/lenses.go:100–140` (`noShowSettlementSpec`, `max(tx.key)`, `count(DISTINCT)`);
`CASE WHEN … ELSE null END` inside an aggregate: `packages/lease-signing/lenses.go:859`; null-safe status test:
`s.paused.data.value <> true` (visitseries.go:1126); RFC3339 string ordering: visitseries.go:1128 (both sides
`time.rfc3339_utc`-normalized — `visitseries.go:764`, `clinic-domain/ddls.go:3213`). Re-derivation of the series
anchor on an appointment event is the actor-aware pipeline's pattern-directed derivation
(`internal/refractor/pipeline/anchor_derivation.go`, fallback `walkscope.go` BFS) — the same transport
`noShowSettlement` relies on for `settles` links; the business-plane sweep is the standing healer. FE calendar
block reason: `dayBlockedReason` :2547 (returns a string reason, `""` = open). Goja pin: `lifecycle_transitions_test.go`.

**Increment order + green checks.**
- **Inc 1 (package, `sonnet` builder):** lens + op + playbook + docs + version bump. Lens vectors
  (`visitseries_cypher_test.go`): no visit → not violating, `handledAt` null; visit `>= nextDueAt` same provider →
  violating, `handledAt` = its `startsAt`; visit before `nextDueAt` → not; `cancelled` / `noShow` → not; other
  provider when the series has one → not; provider-less series + any provider → yes; two qualifying → earliest;
  paused / past-`activeUntil` with a qualifying visit → not; `ReferencesNoClockParameter` kept. Op vectors
  (`integration_test.go`): advance writes `lastOccurrenceAt = handledAt`, `nextDueAt = handledAt + interval`;
  same-`handledAt` replay idempotent; `handledAt < dueFor` refused. Green: `go test ./packages/clinic-reminders/
  -count=1`, `go test ./internal/refractor/ -run 'Corpus|Census' -count=1` (re-pin by lens name — the lens gains
  a reverse hop + an aggregate), `DIFF_BASE=main go run ./scripts/lint-package-version.go`, `go run
  ./scripts/lint-lens-anchors.go`, `STRICT=1 go run ./scripts/lint-conventions.go`.
- **Inc 2 (FE, `sonnet` builder):** the booking floor. Green: `go test ./cmd/clinic-app/ -count=1` (new pin),
  `node --check cmd/clinic-app/web/app.js`, `go run ./scripts/lint-app-op-descriptors.go`, `lint-markup-escaping`.
- **Live (Winston):** `make refresh-clinic` (or `reinstall-package PKG=clinic-reminders`) on the running stack;
  read `weaver-targets` `visitSeriesDue.<riley>`: not violating, `handledAt` null, `nextDueAt` in the past; book a
  visit for Riley with Dr Osei at/after `nextDueAt` through `:7799/api/op`; watch the row flip violating →
  advanced (`occurrenceCount` +1, `nextDueAt` = visit + 30d); `pkill -x clinic-app` → rebuild → relaunch (Makefile
  recipe) → the series worklist shows the row Upcoming. Cumulative cold review (opus) over the whole diff at close.

**In-scope gotchas** (dossier + checklist, walked before the first edit):
- `_packages.md` retired entry, still binding: *a lens MATCH edit is a corpus edit* — `internal/refractor/*_corpus_census_test.go`
  fail by lens name on any `Spec` edit; run them and re-pin deliberately.
- *A playbook `Params` entry bound to an OPTIONAL-hop column is a dispatch refusal on every row where the hop
  misses* — `handledAt` rides an OPTIONAL walk but is non-null by construction whenever the gap is open (the gap
  IS `handledAt <> null`); one lens pin seeds the anchor with the walk missing and asserts the gap shut.
- *Every conjunct of `missing_<g>` must count the population the op's own test reads* — the op reads only the row;
  its one guard (`handledAt >= dueFor`) is implied by the lens's `startsAt >= nextDueAt`; pin both.
- *For every `freshUntil` a lens projects, name the reader of the marker — none ⇒ delete the column* (decision 1).
- *A link key's type segment is what an outbound walk rebuilds the far endpoint from* — fixtures build
  `lnk.appointment.<id>.forPatient.patient.<id>` through the `edge` helper from the real vertex types.
- *A recorded value is read as the FACT it records* — `lastOccurrenceAt` now means "the crediting visit's start";
  say so in the DDL doc; nothing else reads it (grep).
- `packages/` content edit ⇒ manifest + `Version` bump; `lint-package-version` with `DIFF_BASE`.
- vertical-apps dossier: *an op name in any `cmd/<app>` Go comment is a UI reference to `lint-app-op-descriptors`*;
  *a value reaches markup unescaped* — the floor reason is `textContent`/`title` only; *a "the person can also do it
  from X" claim is a claim about X's render gate* — the floor is set by the one entry point (`bookSeriesOccurrence`).
- Standing checklist: #3 (revert-prove: delete the `handledAt` conjunct and the "no visit" vector must fail; delete
  the re-anchor and the op vector must fail), #5 (one writer of `.progress` — the op; unchanged), #6 (the
  `noShowSettlement` precedent's `max(tx.key)` comment says `collect()+index` is unsupported — the same reason this
  brief uses `min(CASE …)` rather than an ordered pick).

**Adjacent finds.** (a) `visitseries.go` header + `package.go`'s "@every" note describe the clock model —
rewritten in Inc 1 (same fire). (b) A crediting visit cancelled *after* the advance stays credited — the
occurrence was recorded off a booking that never happened. Non-goal here (the recorded fact is the booking; the
desk re-books, and the replacement credits the next occurrence only if it lands on/after the new `nextDueAt`);
named so the PO sees the boundary, not filed — no missing pattern (an un-credit is an ordinary op; nobody has
asked). (c) No adjacent defects found by the scout.

**Non-goals.** No change to `visitSeriesRead` / the Postgres read model (its columns are unchanged; the FE's
"Due now" derivation stays FE-side). No `fulfils` link from appointment to series — the recorded `lastOccurrenceAt`
+ the `>= nextDueAt` arithmetic is the consumption record; a link would add a second writer of relationship state
for no reader. No skip-to-latest / catch-up semantics. No change to `PauseVisitSeries` / `EndVisitSeries` /
`StartVisitSeries`. No new op.

**Shipped 2026-09-13 · `a2724692` (merge `e7593e03`).** Live: `make reinstall-package` 0.10.10→0.11.0 at 19:35:47; within a second Weaver dispatched `AdvanceVisitSeries` for Riley's series off its already-booked 09-17 16:00Z visit with Dr Osei (`.progress` → `lastOccurrenceAt 2026-09-17T16:00:00Z`, `nextDueAt 2026-10-17T16:00:00Z`, `occurrenceCount 3`), the row closed (`violating false`, `handledAt null`, no `freshUntil`); `bin/clinic-app` cycled, the desk's Book flow shows the floor lead line, the calendar blocks pre-10-17 days with the reason, `#startsAt.min` = the floor. Close-pass classes: Weaver-episode wedge (design-gap → `_packages.md` budget entry, second shape); stale-row rollback (convention → OCC entry, sixth sighting); floor granularity (implementation-bug, fixed); derivation transit hub (platform ceiling → Surveyor chip).
