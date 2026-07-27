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
