# Clinic — a visit's record says what happened: arrival is never a no-show, a note needs a held visit, a follow-up is closed by its own provider

**Status:** ✅ **Winston-ratified — build-ready** (Vertical Steward, 2026-09-14). No frozen-contract change, no
architectural fork: every mechanism is package-owned (`packages/clinic-domain`, `packages/clinic-reminders`,
`cmd/clinic-app`) and mirrors a shipped pattern in the same package (`MarkPastDueNoShow`'s time-off no-op,
`enforce_started`'s `NotYetStarted`, `visitSeriesDue`'s same-provider qualifying-visit walk). Three PO rows filed
2026-09-14 (`fad66d4c`) build as one fire:

- **R1 — A checked-in patient is swept to no-show** (★★ S, pkg).
- **R2 — A visit that never happened can be documented** (★★ S, pkg).
- **R3 — A follow-up is cleared by any later visit** (★★ S, FE + pkg).

The fourth row of that filing (the desk can't see a debtor / a charge can't name its visit, ★★ M) is a ledger
mechanism with its own precedents and is not part of this fire.

## 1. Problem (PO-filed, live-observed)

- `pastDueAppointments` (`packages/clinic-reminders/pastdue.go:112-119`) opens `missing_noshow_transition` for every
  non-terminal appointment once a lapse at `endsAt` is recorded; `checkedIn` is non-terminal
  (`clinic-domain/ddls.go:3055-3056`), so a patient the desk recorded as arrived is marked `noShow` with the note
  "Auto no-show: appointment ended without a status update" (`ddls.go:3902`) if the provider never closes the visit.
  The overwrite records no prior status — how many of the 63 live `noShow` rows were checked in is unknowable.
- `RecordEncounter` (`ddls.go:4047-4118`) validates alive + class + provider binding and nothing else: a cancelled,
  no-show or future visit takes a clinical note, and `followUpRequested` on it arms `followUpReminders`. Only the FE
  hides the Document button (`app.js:5042`, status === completed). Live: `vtx.appointment.p9rDJNfJgRDRZYUnePst`
  documented 09-03 for a 09-15 visit.
- The follow-ups worklist's `hasLaterVisit` (`app.js:3473-3482`) marks a follow-up addressed by ANY later
  non-cancelled visit of the patient at/after `followUpDate` — any provider, any reason — while
  `followUpReminders` (`followups.go:293-308`) walks no other visit at all and fires at the date regardless. Live:
  Riley's Sports-Medicine follow-up (Dr. Osei, due 09-29) is hidden by a daily badge-check with Dr. Classic Demo
  (Family Medicine); the reminder still fires 09-29.

## 2. Decisions

### 2.1 R1 — recorded arrival is never swept; the sweep's gap and its op both read `checkedIn`

Two layers, the posture `MarkPastDueNoShow` already takes for a provider's time-off (`ddls.go:3883-3896`: never
guess; leave the visit open for the desk):

- **Lens** (`pastdue.go`): `missing_noshow_transition` and `violating` gain the conjunct
  `(a.status.data.value <> 'checkedIn')`. **`freshUntil` keeps arming on `nonTerminalAppointment` unchanged** —
  the timer still fires and the lapse is still recorded at `endsAt` for a checked-in visit, because
  `appointmentReminders` closes its own gate on `byTarget.pastDueAppointments` (`lenses.go:14-27`) and the two
  lenses' terminal set must stay one list. Only the *dispatch* predicate narrows. A checked-in visit past its end
  projects `violating=false, freshUntil=null`; a later `checkedIn → scheduled/confirmed` move (non-terminal moves
  freely, `ddls.go:3670`) re-opens the gap on the next projection — level-triggered, no clearing write.
- **Op** (`MarkPastDueNoShow`): a current status of `checkedIn` is an idempotent no-op (`{"mutations": [], …}`),
  the same branch shape as the terminal no-op above it — write-path honesty against a dispatch racing a late
  check-in (the gap was open when Weaver read the row, the desk checked the patient in before the op ran). The
  `.status` read is already required and declared by the playbook (`pastdue.go:139`).
- **Observer** (the removal-needs-an-observer rule, brief checklist #4): the sweep stops touching these rows, so the
  desk must see them. The follow-ups worklist (`renderFollowups`, `app.js:3514`) gains a section **"Arrived, never
  closed"** listing past-`endsAt` `checkedIn` appointments from the same `/api/staff/appointments` fetch, rendered
  with `renderApptCard({cancelable: true})` — the card already offers Complete / No-show for `checkedIn`
  (`lifecycleTransitions`, `app.js:5136`). Same shape as the time-off "Already passed" sub-section beside it.
- Not changed: `TERMINAL_STATUSES`, `nonTerminalAppointment`, the reminder lens, the fee posture (the sweep still
  bills nothing).

### 2.2 R2 — a note needs a held visit: `RecordEncounter` reads `.schedule` + `.status`

`RecordEncounter` gains, after the provider-binding guard and before any write:

- **`VisitNotHeld`** when the current `.status.value` is `cancelled` or `noShow` — the two terminal outcomes that
  say the visit did not take place. `completed` documents normally; `scheduled` / `confirmed` / `checkedIn` past
  the start document normally too (the provider documenting a started visit is the evidence it happened; the
  status is the desk's to close — this op never writes `.status`).
- **`NotYetStarted`** when `time.rfc3339_utc(op.submittedAt) < .schedule.startsAt` — the same inclusive boundary and
  the same soft caller-supplied clock as `enforce_started` (`ddls.go:2964-2982`). Factored: `enforce_started`'s
  compare moves into a `refuse_before_start(appt_key, sched, submitted_at, verb)` helper both call, so the two
  guards cannot drift.
- **Reads, declared at every dispatcher** (Contract #2 §2.5): `.schedule` is a **required** read (`CreateAppointment`
  always writes it, `ddls.go:3406`); `.status` is an **optionalRead** (absent → no status refusal; the clock still
  binds), mirroring `SetAppointmentStatus` / `RescheduleAppointment` (`opmetas.go:266-271, 183-192`). Dispatchers:
  the descriptor (`opmetas.go:445-457`), the FE `submitOp` (`app.js:5794`), `scripts/seed-showcase.go:1363` (which
  must also become `submitOpAt(completedEnd)` — its visit is `futureDayAt(1, 9)`, and the sibling status write
  already submits at `completedEnd`), `scripts/backfill-clinic-encounter-documentation.go:123`, and every
  `clSubmit(… "RecordEncounter" …)` in `integration_test.go`. `lint-seed-declared-reads` pins the scripts.
- FE courtesy (dossier: a server refusal added to one form leaves its sibling a dead end): the Document button
  is already withheld for anything but `completed`, and `completed` is refused `NotYetStarted` since `b1ad58ae`, so
  no new dead end opens. The catch narrates `VisitNotHeld` / `NotYetStarted` through `rejectionMessage` as today.

### 2.3 R3 — a follow-up is addressed by a later visit with the SAME provider, in the lens and the worklist alike

The rule is `visitSeriesDue`'s (`visitseries.go:1178-1188`, ratified 2026-09-13): a qualifying visit is the same
patient's non-cancelled, non-no-show appointment at/after the due point **with the follow-up's own provider** (a
follow-up whose appointment carries no `withProvider` link is addressed by any provider — `(pr.key = null) OR
(gpr.key = pr.key)`, the series' own null-provider arm). The treating provider asked for the follow-up and is the
only reader of its clinical reason; a badge check with another provider is not it.

- **Lens** (`followUpRemindersSpec`): adds `OPTIONAL MATCH (p)<-[:forPatient]-(g:appointment)` +
  `OPTIONAL MATCH (g)-[:withProvider]->(gpr:provider)` and a `WITH a, p, pr, min(CASE WHEN g.key <> a.key AND
  status-qualifying AND g.schedule.data.startsAt >= a.documentation.data.followUpDate AND same-provider THEN
  g.schedule.data.startsAt ELSE null END) AS addressedAt` (the EARLIEST qualifying visit — the one the reminder
  would have asked for). `freshUntil`, `missing_followup_reminder` and `violating` each gain
  `(addressedAt = null)`; `addressedAt` is projected as an informational BodyColumn. Cancelling the addressing
  visit re-arms the timer (a past `followUpDate` fires at once) — level-triggered on the booked visit, the
  `visitSeriesDue` shape. Cost: one bounded walk over the patient's appointments per anchor projection, the same
  walk `visitSeriesDue` already pays.
- **Worklist** (`hasLaterVisit`): gains `g.providerKey === f.providerKey` (or `!f.providerKey` → any), keeping
  the existing status / date conjuncts. The addressed badge names the visit: "Addressed by the <date> visit". The
  rule lives twice — cypher in the package, JS in the app — **deliberately**: projecting `addressedAt` from the
  unanchored `clinicAppointments` read lens would walk every patient's appointments per row on every rebuild, and
  the `followUpReminders` weaver-target row is not RLS-scoped, so the app would need a server-side join to expose
  it. Both copies are pinned by tests over the same four fixtures (same provider addresses · other provider does
  not · cancelled/no-show does not · null-provider follow-up takes any).

### 2.4 Versions

`clinic-reminders` 0.11.0 → 0.12.0 (two lens specs change); `clinic-domain` 0.35.1 → 0.36.0 (script + descriptor).
Manifest and `Version` constant lockstep (`lint-package-version`). Live: `make refresh-clinic` diff-applies both
(hot reload, no restart); the clinic-app binary is rebuilt and cycled for the FE.

## 3. Fire brief (build note, 2026-09-14)

**Scope sentence (verbatim, the three rows):** *A checked-in patient is swept to no-show — the desk records arrival
(`checkedIn`); if the provider never closes the visit, the past-due sweep marks it `noShow` at endsAt and the record
says the patient never came. Recorded arrival is not a documentation lapse the patient caused.* · *A visit that
never happened can be documented — `RecordEncounter` reads neither status nor clock; a cancelled, no-show or future
visit takes a clinical note and a follow-up that arms reminders; only the FE hides the button.* · *A follow-up is
cleared by any later visit — the worklist hides a follow-up once the patient has any later visit (any provider, any
reason) while the reminder fires whether or not one is booked.*

**Verified touch-list (checked live 2026-09-14 at `fad66d4c`):**

| file | site | change |
|---|---|---|
| `packages/clinic-reminders/pastdue.go:112-119, 134` | `pastDueAppointmentsSpec` gap/violating | `<> 'checkedIn'` conjunct on the two bools; `freshUntil` unchanged; doc comment |
| `packages/clinic-reminders/pastdue_cypher_test.go:95-107` | `TestPastDue_CheckedIn` | flips: lapsed + checkedIn → not violating, freshUntil null; add checkedIn-unlapsed → freshUntil = endsAt |
| `packages/clinic-domain/ddls.go:3862-3871` | `MarkPastDueNoShow` terminal no-op | `or cur_val == "checkedIn"` no-op branch |
| `packages/clinic-domain/ddls.go:2964-2982` | `enforce_started` | factor `refuse_before_start` |
| `packages/clinic-domain/ddls.go:4047-4118` | `RecordEncounter` | `.schedule` required read + `.status` optional read; `VisitNotHeld` / `NotYetStarted` |
| `packages/clinic-domain/ddls.go:526-560, 735` | appointment DDL description | name the two refusals |
| `packages/clinic-domain/opmetas.go:445-457` | `RecordEncounter` Dispatch | `Reads` += `.schedule`; `OptionalReads` = `.status` |
| `packages/clinic-domain/integration_test.go:1124-1230` | `TestClinic_RecordEncounter` | declare the reads; fixtures' visits must have started at submit |
| `packages/clinic-domain/status_clock_guard_test.go` | new pins | `RecordEncounter` NotYetStarted (future visit) · VisitNotHeld (cancelled, noShow) · completed/checkedIn past start accepted; `MarkPastDueNoShow` on checkedIn no-ops (positive vector: confirmed → noShow) |
| `packages/clinic-reminders/followups.go:293-308` | `followUpRemindersSpec` + BodyColumns | the same-provider walk, `addressedAt`, the conjunct |
| `packages/clinic-reminders/followups_cypher_test.go` | new pins | the four fixtures of §2.3; existing tests keep passing (no sibling visit seeded) |
| `cmd/clinic-app/web/app.js:3473-3482` | `hasLaterVisit` | provider conjunct |
| `cmd/clinic-app/web/app.js:3624-3629` | addressed badge | names the visit date |
| `cmd/clinic-app/web/app.js:3514-3545` | `renderFollowups` | "Arrived, never closed" section |
| `cmd/clinic-app/web/app.js:5794` | `submitOp("RecordEncounter", …)` | reads `[key, key.schedule]` + optional `[key.status]` (check `submitOp`'s signature for the optional list) |
| `cmd/clinic-app/<new>_test.go` | goja pin of `hasLaterVisit` | the `timeoff_conflict_test.go` extraction shape |
| `scripts/seed-showcase.go:1363` | RecordEncounter dispatch | `submitOpAt(completedEnd)` + reads |
| `scripts/backfill-clinic-encounter-documentation.go:123` | RecordEncounter dispatch | reads |
| `packages/clinic-reminders/{manifest.yaml:2, package.go:80}` · `packages/clinic-domain/{manifest.yaml:2, package.go:148}` | versions | 0.12.0 · 0.36.0 |
| `internal/refractor/*_corpus_census_test.go` | lens-spec census pins | re-pin `pastDueAppointments` / `followUpReminders` deliberately if a census fails by name |

**Precedents to mirror:** `MarkPastDueNoShow` time-off no-op (`ddls.go:3883-3896`) · `enforce_started`
(`ddls.go:2964-2982`) + `TestClinic_NotYetStartedGuard` (`status_clock_guard_test.go:61`) · `RescheduleAppointment`
`TerminalStatus` (`ddls.go:3532`) + `TestClinic_RescheduleTerminalRejected` (`:136`) · `visitSeriesDueSpec`'s
walk + aggregate (`visitseries.go:1178-1188`) + `linkAppointment` fixture (`visitseries_cypher_test.go:69`) ·
`timeOffConflictVM` goja extraction (`timeoff_conflict_test.go:17-38`) · the time-off "Already passed" sub-section
(`app.js:3533-3541`).

**Increment order + green checks:**

1. R1 lens + op + tests → `go test ./packages/clinic-reminders/ -run PastDue -count=1` ·
   `go test ./packages/clinic-domain/ -run 'PastDue|NoShow' -count=1`.
2. R2 script + descriptor + dispatchers + tests → `go test ./packages/clinic-domain/ -count=1` ·
   `go run ./scripts/lint-seed-declared-reads.go` · `go vet ./scripts/...`.
3. R3 lens + tests → `go test ./packages/clinic-reminders/ -count=1` ·
   `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`.
4. FE (R1 observer section, R3 rule + badge, R2 reads) + goja pin → `node --check cmd/clinic-app/web/app.js` ·
   `go test ./cmd/clinic-app/ -count=1` · `go run ./scripts/lint-app-op-descriptors.go` ·
   `go run ./scripts/lint-markup-escaping.go`.
5. Whole: `go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run ./scripts/lint-conventions.go`
   · every `scripts/lint-*.go` · `DIFF_BASE=fad66d4c go run ./scripts/lint-package-version.go`.
6. Live: `make refresh-clinic` from the main checkout; rebuild + cycle `bin/clinic-app`; the phantom instance
   `p9rDJNfJgRDRZYUnePst` re-documented is refused `NotYetStarted` (a `/api/op` probe); Riley's Osei follow-up
   reads outstanding on the worklist.

**In-scope gotchas:** `contextHint.reads` absence is a correctness error — every dispatcher of `RecordEncounter`
declares the new reads (the retired dossier class's outcome half: grep `RecordEncounter` in `scripts/`, `cmd/`,
`packages/`) · `optionalReads` is never a required key · manifest + `Version` lockstep · a lens `Spec` edit is a
corpus edit (refractor census pins) · `time.rfc3339_utc` is whole seconds — the `NotYetStarted` boundary is
inclusive at `startsAt` · the seed's completed visit is in the FUTURE and documents at `completedEnd` · the FE
goja pin extracts the REAL declaration (regex on `function hasLaterVisit(f, all) {`), never a copy · a lint gate
that is not run locally has reddened `main` (`lint-app-op-descriptors` on a `cmd/<app>` comment naming an op).
Standing checklist (`agents/fire-brief-template.md`) walked: #3 binds each new refusal — prove by disabling the
conjunct and watching its pin fail; #4 is why §2.1 adds the observer section; #6 — `hasLaterVisit`'s
`g.startsAt > f.startsAt` conjunct is kept, not re-derived.

**Adjacent finds:** none new — the ledger row (★★ M) was filed with these and stays 📋 for its own fire.

**Non-goals:** a `fulfills` link from a booked visit to the follow-up it answers · auto-completing a stale
`checkedIn` visit · recording the pre-sweep status on an auto no-show · specialty-based addressing · any
`.status` write from `RecordEncounter` · the ledger row.

**Scope-diff gate:** every touch above traces to one of the three scope sentences (R1: rows 1–3 + the observer
section; R2: rows 4–9 + the seed/backfill dispatchers + the FE read declaration; R3: rows 10–14 + the goja pin).
Nothing widens; the observer section is the removal rule's obligation, not a new feature.

## 4. Checkpoint

Worktree `/tmp/lattice-worktrees/verticals-clinic-visit-record-1789419207` (branch
`steward-verticals-clinic-visit-record`). Landing shape: **hold the worktree, merge once when the four increments
are green** — the two packages version-bump together and the FE reads the op's new declaration. Next: Inc 1.
