# Clinic — "A visit inside a provider's later-declared time-off is reminded, then swept to no-show" (2026-09-17)

**Filed (PO, 2026-09-17).** `SetProviderTimeOff` writes the provider only; the reminder's gate reads status alone,
the patient's card hides the conflict, "the sweep closes it as a no-show". Record the displacement on the visit at
the time-off write; reminder + sweep read it; the patient told. Live: 0 providers on time-off. Winston-adjudicated
(implementation-level; no contract surface, no fork).

## Grounding

- **Premise correction — the sweep does NOT close a displaced visit.** `MarkPastDueNoShow` reads the provider's
  live `.timeOff` and no-ops on an overlap ([ddls.go:4362](../../packages/clinic-domain/ddls.go)): "the gap stays
  open (Weaver keeps retrying, harmlessly) and front desk closes it". The `pastDueAppointments` playbook declares no
  budget, so Weaver's default 3 dispatches spend on a no-op and `GapBudgetExhausted` latches the anchor; the visit
  sits `scheduled` past its end until the desk acts. The PO's harm list, corrected: (1) the 24 h reminder goes out for
  a visit the provider cannot attend; (2) the patient is never told; (3) the sweep's three dispatches are wasted and
  the latch stands; (4) the patient's own card hides what the desk's card shows. The recorded desk-closes-it posture
  at :4362 is kept — this design does not decide a displaced visit's terminal fate.
- **The PO's prescription (a synchronous walk inside `SetProviderTimeOff` over `kv.Links(provider, "withProvider",
  "in")`) is the unbounded-hub shape `lint-links-page-limit`'s header names**: a provider hub carries every
  appointment ever booked with them (the live corpus: 119 appointments over 3 providers, 67 of them noShow), the
  walk would read `.schedule` + `.status` per candidate, and the wall (250 ms live) binds before the 60,000-unit
  budget ([lint-links-page-limit.go](../../scripts/lint-links-page-limit.go); arrears replay, 2026-09-16). A page
  bound is a claim about every writer of the relation, and the relation grows without bound. Rejected.
- **A lens cannot compute the overlap** (no UNWIND / ANY over the `ranges` array — the PO's grounding,
  `visitor.go:146, 651`), but a lens CAN read a linked vertex's aspect field: both appointment deadline lenses
  already hop `OPTIONAL MATCH (a)-[:withProvider]->(pr:provider)` ([lenses.go:208](../../packages/clinic-reminders/lenses.go),
  [pastdue.go:120](../../packages/clinic-reminders/pastdue.go)), and a Weaver playbook may declare
  `row.<column>.<aspect>` on a non-anchor column (cafe `row.tabKey.status`, clinic-ledger
  `row.appointmentKey.status`). So a level-triggered gap per appointment — *"the provider's time-off has changed
  since this visit was last checked against it"* — is expressible with no new primitive, and pages the walk across
  dispatches by construction (one op per appointment). The "evaluate and record on the entity" precedent is
  `EvaluateClinicArrears` ([clinic-ledger lenses.go:37](../../packages/clinic-ledger/lenses.go)).
- **The consumers.** `appointmentReminders` gates on `remindedFor <> startsAt AND lapse ≥ remindAt AND
  nonTerminal AND NOT ended` (:222–224); `pastDueAppointments` on `nonTerminal AND <> checkedIn AND lapse ≥ endsAt`
  (:131) and its `freshUntil` arms the timer whose fired marker is the "visit is over" fact every sibling reads —
  that CASE must keep arming for a displaced visit. `appointmentChangeNotices` (:309–328) carries two
  level-triggered gaps keyed on a recorded stamp with `RecordAppointmentChangeNotice{kind, changeRef}` re-checking
  the live aspect (`StaleChange`) and writing `.changeNotice.<kind>For` ([changenotice.go:267–290](../../packages/clinic-reminders/changenotice.go)).
- **The card.** The desk's card computes the conflict client-side (`timeOffConflict`, [app.js:2425](../../cmd/clinic-app/web/app.js))
  and deliberately shares that predicate with the Availability editor's preview (:2303) and the Follow-ups
  reschedule worklist (:3523) "so the badge and the worklist membership never disagree"; the patient's card
  (`opts.asSelf`) renders nothing (:5388–5399). `changeNoticeSentAt` already renders "📣 Patient told" on both hats.
- **Engine**: `=` on a null operand is false (nil-false); `<>` is the two-valued null test. Census (PO, 2026-09-17):
  119 live appointments, 0 on time-off; `clinic-domain` 0.40.0, `clinic-reminders` 0.13.0.

## Verdict — *a time-off write is a recorded event; each visit it covers records its displacement; the reminder and the sweep stand down and the patient is told once*

1. **`.timeOff` gains `setAt`.** `SetProviderTimeOff` stamps `setAt = time.rfc3339_utc(op.submittedAt)` on every
   write, a clear (`ranges: []`) included — the event the per-visit check is keyed on. Re-stamped on every write,
   dies with the aspect. A legacy `.timeOff` with no `setAt` triggers nothing (live: none exist).
2. **A `.displacement` aspect on the appointment** (class `appointmentDisplacement`, declared in clinic-domain
   beside `appointmentStatus`; `PermittedCommands: [CreateAppointment, RescheduleAppointment,
   EvaluateAppointmentDisplacement]`): `{displaced: bool, checkedFor?: <the .timeOff.setAt it was evaluated
   against>, at, from?, to?, reason?}`. Lifetime: created by whichever writer runs first; `checkedFor` follows the
   provider's latest `setAt`; `at` stamps when `displaced` FLIPS and is carried when it does not (the notice keys on
   it — a provider re-saving their time-off must not re-tell a still-displaced patient); `from/to/reason` are the
   covering range when displaced, absent otherwise; dies with the appointment. Three writers, one arbitration:
   `CreateAppointment` and `RescheduleAppointment` write `{displaced: false, checkedFor: <setAt if the provider's
   .timeOff carries one>, at}` as a bare upsert right after their `enforce_time_off` passes (they have just proved
   the visit clear of the current ranges, and a displaced visit the desk moves to a free slot must read clear at
   once, not at the next time-off edit); `EvaluateAppointmentDisplacement` writes the evaluated truth. The
   Processor serializes the three on the key; a stale evaluation is corrected by the level gap on the next
   projection. `time_off_overlap` is refactored over a `time_off_aspect(provider)` helper so the writers read the
   aspect once for both the overlap and `setAt` (the existing `# read-posture: (c)` config read, unchanged).
3. **One level-triggered lens in `clinic-reminders`: `appointmentDisplacements`** (anchor appointment,
   `actorAggregate`, `weaver-targets`, no `freshUntil`): `missing_displacement_check = pr.timeOff.data.setAt <> null
   AND a.displacement.data.checkedFor <> pr.timeOff.data.setAt AND nonTerminalAppointment AND NOT
   (freshnessExpiry.byTarget.pastDueAppointments >= endsAt)`. Columns `entityKey, providerKey, timeOffSetAt,
   checkedFor, displaced, startsAt, endsAt, status`, the gap, `violating`. `withProvider` is the same 0..1 hop the
   sibling lenses walk; the gap's own `setAt <> null` conjunct makes `providerKey` and `timeOffSetAt` non-null on
   every violating row, which is what licenses templating them (`Params` only on columns the gap's conjunct
   requires non-null). The gap closes on the op's write (`checkedFor = setAt`), on the visit going terminal, and on
   the recorded end; a closed column retires the latch. Cadence: one dispatch per (time-off write × live future
   visit of that provider); a new booking never dispatches (its writer stamps `checkedFor`).
4. **One op in clinic-domain: `EvaluateAppointmentDisplacement{appointmentKey, providerKey, checkedFor}`**
   (operator grant, the `MarkPastDueNoShow` idiom — dispatched by Weaver's service actor; playbook `Reads
   [row.entityKey, row.entityKey.schedule, row.providerKey, row.providerKey.timeOff]`, `OptionalReads
   [row.entityKey.status, row.entityKey.displacement]`). Liveness-guards the appointment (`UnknownAppointment`,
   `WrongClass`); resolves the provider live off `withProvider` (`appointment_provider`, the bounded (e) read the
   sweep uses) and refuses `ProviderMismatch` when the param names another; a terminal status is the empty batch
   (nothing to displace). Evaluates against the LIVE `.timeOff` — `checkedFor` is informational (a time-off
   re-written between projection and dispatch is evaluated as it now stands; the truth written converges faster
   than a refusal would, and the write is idempotent) — with `time_off_overlap`'s half-open test over
   `.schedule.{startsAt, endsAt}`. Writes `.displacement = {displaced, checkedFor: <live setAt, omitted when the
   aspect is absent or carries none>, at: <stamp on flip, carry otherwise>, from/to/reason when displaced}` as a
   create when absent, bare update otherwise (the hydrated key conditions it). Emits `clinic.appointmentDisplaced`
   / `clinic.appointmentReinstated` only on a flip. Mints no vertex.
5. **The reminder and the sweep stand down.** `appointmentReminders` adds `AND NOT (a.displacement.data.displaced
   = true)` to `missing_reminder` and `violating` (not to `freshUntil` — the @at still arms, so a visit reinstated
   by a time-off edit or a move is reminded on the lapse already recorded); `pastDueAppointments` adds the same
   conjunct to `missing_noshow_transition` and `violating` and NOT to `freshUntil` (the recorded end is the fact
   every sibling reads). Nil-false: a visit with no `.displacement` reads `null = true` false → `NOT false` → the
   gates behave exactly as today. `MarkPastDueNoShow`'s live overlap no-op stays as the race guard (a displacement
   evaluated after the sweep's projection). A displaced visit therefore stays open for the desk, as the :4362
   posture records — unswept, unreminded.
6. **The patient is told once: a third gap on `appointmentChangeNotices`.** `missing_displaced_notice =
   a.displacement.data.displaced = true AND a.displacement.data.at <> null AND a.changeNotice.data.displacedFor <>
   a.displacement.data.at AND nonTerminalAppointment AND NOT (…pastDueAppointments >= endsAt)`; columns add
   `displaced, displacedAt, displacedFor`. The playbook gains `missing_displaced_notice → RecordAppointmentChangeNotice
   {appointmentKey: row.entityKey, kind: displaced, changeRef: row.displacedAt}`, `Reads [row.entityKey,
   row.entityKey.status, row.entityKey.schedule]`, `OptionalReads [row.entityKey.changeNotice,
   row.entityKey.displacement]`. The op's `kind` enum gains `displaced`; its re-check: `.displacement.displaced ==
   True AND .displacement.at == changeRef` on a non-terminal status, else `StaleChange`; it writes `displacedFor`
   carrying the other two kinds' fields; the notification params carry `changeType: displaced` plus the covering
   `from`/`to`. A reinstatement (displaced → false) is not told — the reminder resumes and says the visit stands
   (non-goal, recorded). A displaced-then-cancelled visit gets the cancel notice only (`nonTerminal`). A
   displaced-then-moved visit reads `displaced: false` from the move's write and gets the move notice only.
7. **The card says so on both hats.** The three appointment lenses project `displaced`, `displacedFrom`,
   `displacedTo`; `protectedAppointmentRow` carries them; `renderApptCard` renders, when `displaced` is true, one
   line for every hat: "⛔ Provider unavailable · <from – to local> — the clinic will reschedule" (a pure
   `displacementLabel(row)`, goja-pinned). The desk's client-side `conflict` badge is left as is — it is coupled to
   the worklist by a shared predicate and previews an unsaved edit, which the recorded fact cannot; the recorded
   line is the patient's, and the desk sees both agree once the evaluation lands. "📣 Patient told" already covers
   the displaced notice via `changeNoticeSentAt`.

Alternatives priced: **do nothing** — every harm stands, none is client-fixable (the reminder and the sweep are
Weaver's); **the synchronous walk** — rejected above (unbounded hub); **the sweep closes a displaced visit as
`cancelled` by `sweep`** — a terminal-fate decision the recorded :4362 posture declines, and a PO call, not this
fire's; **suppress the reminder by reading the provider's live `.timeOff` in `RecordAppointmentReminder`** —
fixes one harm, tells nobody, and hides the fact from every lens.

### Fire brief (build note, 2026-09-17)

1. **Scope** (board row, verbatim): *`SetProviderTimeOff` writes the provider only; the reminder's gate reads status
   alone, the sweep closes it as a no-show. Record the displacement on the visit at the time-off write; reminder +
   sweep read it; the patient told via a third `appointmentChangeNotices` gap.* Narrowed per the grounding: the
   displacement is recorded on the visit BY a level-triggered evaluation the time-off write triggers, not inside
   the write. Green bar: a time-off write covering a live future visit produces exactly one `.displacement
   {displaced: true}` on it and exactly one `external.notification` `<key>:displaced:<at>`; the reminder gap and the
   sweep gap read false on it; a time-off clear or a move out of the range reads `displaced: false` and re-arms the
   reminder; a re-saved time-off that still covers the visit re-tells nothing; a visit with no `.displacement`
   behaves as before on every gate; the patient's card renders the line.
2. **Touch-list (verified live):** `packages/clinic-domain/`: `ddls.go` `:2306–2361` (SetProviderTimeOff — `setAt`
   on the `:2357` mutation, DDL prose `:431/:450/:463/:1139` says so), `:3045–3078` (`time_off_aspect` helper +
   `time_off_overlap`), `:3619` + `:3741–3759` (Create — the `.displacement` write), `:3857` + `:3968–4004`
   (Reschedule — same), new `EvaluateAppointmentDisplacement` branch beside `MarkPastDueNoShow` `:4308–4383` (+
   `PermittedCommands` `:528`, InputSchema/FieldDescription/Examples on the appointment vertexType DDL), new
   `displacementAspectTypeDDL()` in the `:121` list; `permissions.go:176` (+1 operator grant, the MarkPastDueNoShow
   note idiom); `lenses.go` `:684–706`, `:1033–1061`, `:1077–1101` (+ `displaced`, `displaced_from`, `displaced_to`;
   Protected `Columns`); `lens_cypher_test.go`, `protected_lens_test.go`, `integration_test.go` (Create writes
   `.displacement`), `package_test.go` counts; `package.go:150` + `manifest.yaml:2` (0.40.0 → 0.41.0).
   `packages/clinic-reminders/`: new `displacement.go` (lens spec + `LensSpec` + target; the `pastdue.go` file shape),
   `lenses.go` `:70–98` (+ the lens entry; `appointmentChangeNotices` `BodyColumns` +3), `:222–224` + `:325–328`
   (the two conjuncts, the third gap), `pastdue.go:131–132`, `targets.go:60–75` (+ the third gap, `:80` + the new
   target), `changenotice.go` `:87` (enum), `:267–290` (re-check), the marker write (+ `displacedFor` carry), the
   event params, DDL prose; `permissions.go` (+0 — the new op is clinic-domain's), `package.go:94` +
   `manifest.yaml:2` (0.13.0 → 0.14.0), `package_test.go` counts, `changenotice_cypher_test.go` /
   `changenotice_op_test.go` (+ the displaced vectors), new `displacement_cypher_test.go` (harness
   `integration_test.go:104–189, 384–397`). `scripts/verify-package-clinic-domain.go:197+` (+1 `ddlCheck`
   aspectType, the op in the permission census `:451–542`); `scripts/verify-package-clinic-reminders.go:107–112`
   (no new DDL there — the lens/target are not DDLs in that census; verify by reading the census). `cmd/clinic-app/`:
   `appointments.go:142–165` (+3 fields), `:186–194` + the provider/staff SQL + every `Scan`,
   `appointments_rls_test.go` / `provider_schedule_rls_test.go` / `staff_appointments_rls_test.go` column lists;
   `web/app.js` `:5290–5314` (the line beside "Patient told"), new `displacement_ui_test.go` (goja, mirror
   `status_clock_ui_test.go`).
3. **Precedents:** the stamp → `SetProviderHours`/`SetProviderTimeOff`'s whole-aspect upsert + `stamp_status`'s
   flip-vs-carry (`:3219`); the evaluate-and-record op → `MarkPastDueNoShow` (`:4308`: liveness, live provider,
   terminal no-op, declared reads) and `EvaluateClinicArrears`; the lens → `pastDueAppointmentsSpec` (the
   `withProvider` hop, `nonTerminalAppointment`, the sibling-lapse conjunct); the playbook → `pastDueAppointmentsTarget`
   + cafe `row.tabKey.status` for the non-anchor column read; the third gap + kind → the `moved` gap/kind
   end-to-end (`changenotice.go:267–290`, `targets.go:68–75`); the column path → `statusAt` (`8fd1f4d5`); the card →
   the "Patient told" line (`app.js:5309`); the goja pin → `status_clock_ui_test.go`.
4. **Increments:** **(1) clinic-domain**: `setAt`; aspect DDL; the helper; Create/Reschedule writes; the op + grant;
   the three lenses' columns; version. Tests: `setAt` equals `op.submittedAt` (clear included); Create/Reschedule
   write `displaced: false` with `checkedFor` when the provider has `setAt`, without when not; the op: covered →
   `{displaced: true, from, to, at = submittedAt}`, clear → false, re-eval unchanged carries `at`, flip re-stamps,
   terminal → empty batch, mismatched provider refused, tombstoned refused, non-operator denied; cypher pins for
   the three columns. Green: `go test ./packages/clinic-domain/ -count=1`, `go test ./internal/refractor/ -run
   'Corpus|Census' -count=1`. **(2) clinic-reminders**: the lens + target; the two stand-down conjuncts; the third
   gap + kind. Cypher pins: setAt with no marker → gap; `checkedFor = setAt` → closed; no timeOff → closed;
   terminal / ended → closed; reminder gap false on displaced, unchanged on no-marker; sweep gap false on displaced,
   `freshUntil` still projects on a displaced visit; displaced untold → third gap; told → closed; re-eval same `at`
   → closed; displaced-then-cancelled → cancel gap only. Op pins: `displaced` kind writes `displacedFor` + emits
   `<key>:displaced:<at>` carrying `from/to`, carries `cancelledFor`/`movedFor`; `StaleChange` on `displaced: false`
   / another `at`; the `moved` kind carries `displacedFor`. Green: `go test ./packages/clinic-reminders/ -count=1`,
   the refractor census. **(3) clinic-app**: fields, SQL, tests, the card line, goja pin. Green: `go test
   ./cmd/clinic-app/ -count=1`, `go build ./...`, `make vet`, `golangci-lint run ./...`, `for f in
   scripts/lint-*.go; do STRICT=1 go run "$f" || echo "FAIL $f"; done`, `gofmt -l scripts/`. **(4) live** (main
   checkout): `make refresh-clinic`, `lattice lens lag`, `make verify-package-clinic-domain` +
   `make verify-package-clinic-reminders`, cycle `bin/clinic-app`; drive: book a visit, declare time-off over it →
   one `.displacement`, one notice, reminder/sweep rows read false; clear the time-off → `displaced: false`.
5. **Gotchas:** version bumps + `Version` mirrors; a lens RETURN edit is a corpus edit (refractor census pins);
   `verify-package-clinic-domain.go` `ddlCheck` for the new aspectType + the op's permission (the `command count`
   red); `lint-date-field-normalized` binds payload date fields — `checkedFor` IS a payload date field on the new
   op (normalize it through `time.rfc3339_utc` or exempt with the `date-field-exempt:` idiom at `:2325`, stating
   why); `lint-derive-reads-bare-vector` — a new op with declared reads needs its `Test*UndeclaredSubmitter*` vector
   if it appears in a script's `derive_reads`; `lint-seed-declared-reads` for any `mustAccepted` seed submitting
   Create/Reschedule (they gain a WRITE, not a read — verify no new read); `lint-links-page-limit` on any new
   `kv.Links` (mirror `appointment_provider`'s limit 1); `lint-refusal-courtesy`: no client dispatches the new op;
   the two new event classes go through the abstract-event gate — mirror `clinic.appointmentStatusSet`'s shape.
   **Dossier (`_packages.md`):** *the "leg" of a guard is every op that WRITES the guarded value* — `.displacement`
   has three writers, each decided above; the reminder and sweep conjuncts read it, `MarkPastDueNoShow` keeps its
   live read; *a dispatch declaration must name what the runtime actually binds* — `Params` on `providerKey` /
   `timeOffSetAt` are licensed by the gap's own `setAt <> null`; every `(a)`/`(d)` annotation in the new op resolves
   to the playbook's `Reads`/`OptionalReads`, and one lens pin seeds the anchor with the `withProvider` walk missing
   (gap closed); *a gap's budget and cadence are derived from its WHOLE loop* — pin the gap FALSE over the state every
   arm leaves (op: covered / clear / terminal; neighbours: Create, Reschedule, SetProviderTimeOff clear, the sweep,
   a cancel), and prove the FIRST dispatch closes it; *a refusal that reads a key the op never WRITES is advisory* —
   the op reads `.timeOff` and writes `.displacement`: a time-off edit landing between hydration and commit leaves
   `checkedFor` ≠ the new `setAt`, and the level gap re-dispatches (converges; recorded as accepted). **Dossier
   (`vertical-apps.md`):** *a new fact on a row is a census of every render gate* — `renderApptCard` is the one card
   for both hats; the goja pin holds displaced / not / legacy-null. **Standing checklist:** #1 lifetime table above
   (§2); #2 the "sweep closes it" premise was FALSE — re-run any count before relying on it; #3 every new conjunct
   and the `StaleChange` re-check revert-proven, `setAt`/`at` asserted equal to `op.submittedAt` at the producer;
   #5 three writers of `.displacement` with the arbitration stated (Processor serialization + the level gap);
   #6 the `moved` kind's marker write is bare — mirror the shipped code.
6. **Adjacent finds:** none new. Sibling rows already on the board (hours in UTC — blocked; no-show count — the
   batch's next unit) are not residuals of this fire.
7. **Non-goals:** deciding a displaced visit's terminal fate (the desk closes it — recorded at :4362); telling the
   patient about a reinstatement; replacing the desk card's client-side conflict badge or the worklist's predicate;
   a backfill of `setAt` on a legacy `.timeOff`; a real vendor adapter; the time-zone row.
