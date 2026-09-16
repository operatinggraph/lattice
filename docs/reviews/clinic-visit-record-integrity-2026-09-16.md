# Clinic — a visit's record stays true: an amended note, a moved visit, a patient's own confirmation (2026-09-16)

**Filed (PO, 2026-09-15), three rows, one fire — each a way the appointment's record drifts from what happened:**

1. *A clinical note is rewritten without a trace* — `RecordEncounter` blind-upserts `.encounter` + `.documentation`
   and re-stamps `documentedAt` ([ddls.go:4130-4224](../../packages/clinic-domain/ddls.go)); "Edit documentation"
   says so ([app.js:6122](../../cmd/clinic-app/web/app.js)). An edit is an addendum: prior text kept,
   `documentedAt` preserved, `amendedAt` recorded, the card says amended.
2. *A moved visit keeps its arrival* — `RescheduleAppointment` leaves `.status` untouched
   ([ddls.go:3602-3606, 3667](../../packages/clinic-domain/ddls.go)) and the desk card offers Reschedule on
   `checkedIn`/`confirmed` ([app.js:5385-5424](../../cmd/clinic-app/web/app.js)): a checked-in visit moved to next
   week reads "checked in" a week early, the sweep exempts it, so a later no-show is never recorded or billed. A move
   returns it to `scheduled`.
3. *A patient cannot confirm their own visit* — the reminder goes out 24 h ahead and `confirmed` exists, but the
   consumer self grant on `SetAppointmentStatus` is cancel-only ([ddls.go:3718-3720](../../packages/clinic-domain/ddls.go));
   0 of 24 live scheduled visits are confirmed. Widen the self grant to `confirmed` before start; "Confirm" on the
   patient card.

Winston-adjudicated (implementation-level; no contract surface, no fork — every edit sits inside the package's own
op scripts, lenses and the app, each on a shipped pattern named below).

## Grounding

- **The clinical record is split along the sensitivity boundary.** `.encounter` (class `appointmentEncounter`,
  `Sensitive: true`, DEK custodied on the `clinicalRecord` retention class, [ddls.go:1156-1190](../../packages/clinic-domain/ddls.go))
  holds `{summary, assessment, plan}` — every key always written, an unfilled one as `""`, because
  `clinicEncountersRead`'s three secure columns each name one plaintext field and the decryptor treats a MISSING
  field as a spec/DDL mismatch: Terminal, column redacted, privacy alarm
  ([secure.go:275-289](../../internal/refractor/pipeline/secure.go)). `.documentation` (class
  `appointmentDocumentation`, plain, [ddls.go:1206-1240](../../packages/clinic-domain/ddls.go)) holds the operational
  `{documentedAt, followUpRequested, followUpDate?}` that `clinicAppointments` / `clinicAppointmentsRead` /
  `providerAppointmentsRead` project ([lenses.go:680-682, 1026-1028, 1066-1068](../../packages/clinic-domain/lenses.go))
  and `clinic-reminders`' `followUpReminders` reads. `clinicEncountersRead`'s row set is
  `WHERE a.documentation.data.documentedAt <> null` ([lenses.go:1098-1112](../../packages/clinic-domain/lenses.go)).
- **A script reads a sensitive aspect as plaintext.** Step 4 hydration and the lazy `kv.Read` seam both run
  `decryptSensitiveDoc` for a sensitive class ([sensitive_decrypt.go:100-140](../../internal/processor/sensitive_decrypt.go)),
  so `RecordEncounter` can read the prior `.encounter` and re-write it with the prior text carried; step 6.5
  re-encrypts the whole map. The egress guard only binds an op emitting `external.*` events — this op emits
  `clinic.appointmentEncounterRecorded`. A record whose class key was destroyed faults the declared read at step 4
  (the `_packages.md` dossier's first entry): an amendment cannot preserve a record it cannot read, and refusing is
  the honest outcome. The patient's own `ShredIdentityKey` never reaches this DEK (custody is the retention class).
- **The pre-split corpus.** [`scripts/backfill-clinic-encounter-documentation.go`](../../scripts/backfill-clinic-encounter-documentation.go)
  records that a `.encounter` written before the split held `{documentedAt, followUpRequested, summary}` in the
  clear with NO `.documentation` sibling, and that the repair is a plain re-submit. "A record exists" is therefore
  `.documentation` live with `documentedAt`, never `.encounter` presence — the lens's own presence rule.
- **The shipped "keep the overwritten value on the aspect" pattern is `correctedFrom`** on `.status`
  ([ddls.go:3888-3892, 973](../../packages/clinic-domain/ddls.go)); `SupersedeClause` (semantic-contracts) mints a
  new vertex per revision. A new vertex per note would need its own custody declaration and a second secure lens;
  the aspect-internal shadow is the precedent this package already carries, and inside an encrypted map a history
  list costs nothing on the sensitivity axis.
- **`RescheduleAppointment` already reads `.status`** — `(d)`, declared by the dispatcher and by `derive_reads`
  ([ddls.go:3252-3262](../../packages/clinic-domain/ddls.go)) — refusing a terminal value and otherwise leaving it
  ("`.status` untouched — the move keeps the same provider / patient", [ddls.go:3660](../../packages/clinic-domain/ddls.go)).
  A declared key's bare update is auto-conditioned on the hydrated revision (Contract #3 §3.2), so a `.status`
  write beside the `.schedule` upsert rides the same OCC. `MarkPastDueNoShow`'s sweep and the `pastDueAppointments`
  gap exempt `checkedIn` ([ddls.go:3945-3953](../../packages/clinic-domain/ddls.go),
  [pastdue.go:96-102](../../packages/clinic-reminders/pastdue.go)) — and pastdue's own doc comment already names
  "a later checkedIn → scheduled/confirmed move re-opens the gap on the next projection with no clearing write."
  `.schedule`'s `remindAt` is re-derived on every move ([ddls.go:3651-3656](../../packages/clinic-domain/ddls.go)),
  so `appointmentReminders` re-arms for the new date.
- **`SetAppointmentStatus`'s self path** ([ddls.go:3708-3735](../../packages/clinic-domain/ddls.go)): the value
  restriction (`status != "cancelled" → AuthDenied`) sits ahead of the ownership binding (`identifiedBy` probe +
  `require_matching_patient`); the patient-self clock (`self_visit_clock`, [ddls.go:3020](../../packages/clinic-domain/ddls.go))
  is read on the terminal branch off `.schedule` ([ddls.go:3785-3812](../../packages/clinic-domain/ddls.go)). The
  descriptor already declares `.schedule` REQUIRED and the `identifiedBy` probe OPTIONAL for every hat
  ([opmetas.go:262-276](../../packages/clinic-domain/opmetas.go)); the app's `setStatus` declares `.schedule` + the
  endpoint links only on a terminal transition ([app.js:5595-5606](../../cmd/clinic-app/web/app.js)) and never the
  `identifiedBy` probe (served by the lazy seam today). The self grant's note says "status=cancelled only"
  ([permissions.go:155-160](../../packages/clinic-domain/permissions.go)); the descriptor's `status` description says
  "Self-service patients may only cancel" ([opmetas.go:237, 244](../../packages/clinic-domain/opmetas.go)).
- **The FE.** The self card offers Reschedule (clock `open`) and Cancel (`open`/`late`), nothing on `started`
  ([app.js:5385-5424](../../cmd/clinic-app/web/app.js)); `lifecycleTransitions` is the staff table
  ([app.js:5545-5554](../../cmd/clinic-app/web/app.js), pinned by `lifecycle_transitions_test.go`); `encounterSummary`
  renders "✓ Visit documented · <date>" ([app.js:5509-5517](../../cmd/clinic-app/web/app.js)); the button label
  reads `a.documentedAt ? "Edit documentation" : "Document visit"` at two sites ([app.js:5177, 5457](../../cmd/clinic-app/web/app.js));
  `openEncounter`'s context line says "re-documenting replaces the prior note" and blocks Save while the prior
  note is unloaded ([app.js:6147-6179](../../cmd/clinic-app/web/app.js)); `submitEncounter` declares
  `[appt, appt.schedule]` + optional `[appt.status]` ([app.js:6234](../../cmd/clinic-app/web/app.js)) and caches the
  optimistic note ([app.js:6250-6252](../../cmd/clinic-app/web/app.js)). The appointment rows reach the FE through
  `protectedAppointmentRow` + two SQL selects ([appointments.go:133-187, 291](../../cmd/clinic-app/appointments.go))
  and the unprotected `clinicAppointments` decode ([appointments.go:26-70](../../cmd/clinic-app/appointments.go)).
- **Other dispatchers of `RecordEncounter`:** `scripts/seed-showcase.go:1361-1374` (two envelopes), the backfill
  script above, `scripts/verify-package-clinic-domain.go` (ddlCheck only — no new op, counts unchanged), and the
  Facet descriptor (`opmetas.go:425-468`, `OptionalReads` = `.status`). `lint-seed-declared-reads` requires every
  `scripts/` envelope to carry each descriptor `OptionalReads` marker.

## Verdict — *an edit is an addendum inside the encrypted record; a move puts the visit back on the schedule; a patient confirms before the visit starts*

1. **`RecordEncounter` becomes record-or-amend.** It reads `.encounter` and `.documentation` (both OPTIONAL,
   declared by every dispatcher + the descriptor; `# read-posture: (d)`). **No record** (`.documentation` absent,
   deleted, or without `documentedAt` — the pre-split corpus included) → today's write, plus `superseded: []` on
   `.encounter` so the plaintext shape is fixed from the first write. **A record exists** → an amendment:
   `.encounter = {summary, assessment, plan, superseded: prior.superseded + [{summary, assessment, plan, recordedAt}]}`
   where `recordedAt` is the instant the superseded text was recorded (the prior `.documentation.amendedAt` if
   present, else its `documentedAt`) and a legacy plaintext lacking `superseded` reads as `[]`;
   `.documentation = {documentedAt: prior.documentedAt (PRESERVED), amendedAt: rfc3339_utc(op.submittedAt),
   followUpRequested, followUpDate?}` — the follow-up signals are the payload's, as today (the form re-sends them;
   `followUpReminders` keeps reading the same two keys). **An amendment that changes nothing** — same three texts,
   same `followUpRequested`, same normalized `followUpDate` — writes nothing and emits nothing (`{"mutations": []}`,
   `MarkPastDueNoShow`'s idiom): "amended" is a claim that something changed. **Past `MAX_ENCOUNTER_AMENDMENTS = 50`**
   superseded versions the op refuses `AmendmentLimit` (a bounded aspect; 50 × 12 KB stays under the NATS payload).
   Both aspects still land in one batch; `.documentation`'s bare update is OCC-conditioned on the hydrated revision
   because it is declared, so two concurrent amendments cannot both build on the same prior. `documentedAt` is now
   "when the visit was FIRST documented"; `amendedAt` "when the current text was recorded" — the DDL text says so.
2. **Projection: `amendedAt` only.** `clinicAppointments` (NATS KV), `clinicAppointmentsRead` and
   `providerAppointmentsRead` (Postgres, `amended_at text`) gain the column beside `documentedAt`; `/api/my-appointments`,
   `/api/my-schedule` and the unprotected decode thread `amendedAt`. `clinicEncountersRead` is **untouched**: a
   secure column over `superseded` would Terminal on every pre-fire row (the decryptor's missing-field rule), and the
   provider reads the current text; the history is retained in the record, not rendered — a later fire that wants
   to render it adds a column with a backfill, which is its own design. A count column is not added either
   (no numeric-typed Postgres lens column exists in the corpus; the cap is the script's own bound).
3. **The FE says amended.** `encounterSummary` appends `· amended <YYYY-MM-DD>` off `a.amendedAt`; the button reads
   "Amend documentation" once `a.documentedAt`; `openEncounter`'s context says "an amendment keeps the prior note in
   the record — documented <date>, amended <date>" (the unloaded-note guard stays: the CURRENT text would still be
   replaced by what is typed); `submitEncounter` declares the two new optional reads and, on `AmendmentLimit`, says
   the record has reached its amendment bound. The three courtesy declarations gain the new code (app.js +
   the descriptor's `(facet)` line); the modal's `#encounter-title` reads "Document visit" / "Amend documentation".
4. **`RescheduleAppointment` returns a `confirmed` / `checkedIn` visit to `scheduled`.** After the terminal refusal,
   when `cur_val in ("confirmed", "checkedIn")` the batch adds `make_aspect_upsert(appt_key, "status",
   "appointmentStatus", {"value": "scheduled"})` — a bare update on a declared key (OCC on the hydrated revision), the
   note dropped (it belongs to the transition that recorded it; neither of these carries a fee). `scheduled` and
   absent stay untouched. The `clinic.appointmentRescheduled` event gains `statusReset: true` on that arm. The
   patient-self path is the same: a confirmed visit the patient moves needs confirming again for its new date, which is
   what the re-armed reminder asks. `pastDueAppointments` then re-opens on the next projection (its comment's own
   promise), so a moved visit that is missed is swept and billed like any other.
5. **The FE tells the mover.** `submitReschedule`'s success toast reads "Appointment rescheduled — back to scheduled"
   when `a.status` was `confirmed`/`checkedIn` (the desk sees the status it just undid); the reschedule modal's
   context line says so before submit for a checked-in visit. Reschedule stays offered on both states — the row's
   ask is that the move be coherent, not that it be refused.
6. **A patient confirms their own visit.** The self path's value restriction widens to `status in ("cancelled",
   "confirmed")`; the `AuthDenied` text names both verbs. A self `confirmed` is further gated, after the ownership
   binding and the current-status read: `cur_val == "checkedIn"` → `AuthDenied` (the desk has already checked the
   patient in; a self write must not undo it — the same "write into someone else's building" posture the staff
   guard names); `self_visit_clock(...) == "started"` → `VisitStarted` (a visit cannot be confirmed after it began;
   `late` is fine — confirming inside 24 h is exactly what the reminder invites). A re-confirm is idempotent
   (`confirmed → confirmed`). The self-confirm reads `.schedule` for the clock — declared `(a)` by the self dispatcher
   as the descriptor already does. No fee, no cells move. Staff paths unchanged. The grant note and both descriptor
   descriptions read "cancel or confirm".
7. **"Confirm" on the patient card.** The self card gains a primary "Confirm" button when `a.status === "scheduled"`
   (absent or `scheduled`) and the clock is not `started`; it dispatches `setStatus(a, "confirmed", …, {asSelf})`.
   `setStatus` on the self path always sends `provider` + `patient` (the schema requires them) and declares
   `.schedule`, both endpoint links and the `identifiedBy` probe — the descriptor's own declaration, per hat — so the
   self cancel stops leaning on the lazy seam too. The toast says "Appointment confirmed." (`STATUS_PAST` already
   carries it); a `confirmed` self card shows no Confirm, and its Cancel / Reschedule stay as they are.
8. **Version + text.** clinic-domain `0.36.2 → 0.37.0` (`Version` + manifest); the `.encounter` and `.documentation`
   DDL descriptions, field descriptions and examples say the new shape; `RecordEncounter`'s `ExpectedOutcome` says
   record-or-amend; the `RescheduleAppointment` description says a confirmed / checked-in visit returns to scheduled;
   `SetAppointmentStatus`'s says a patient may cancel or confirm.

### Fire brief (build note, 2026-09-16)

1. **Scope** (the three board rows, verbatim above). Green bar: (i) a second `RecordEncounter` keeps the first text
   under `superseded`, preserves `documentedAt`, records `amendedAt`; an identical re-submit writes nothing; the card
   says amended; (ii) a `checkedIn` / `confirmed` visit that is rescheduled reads `scheduled`, and `pastDueAppointments`
   re-opens on it; (iii) a patient confirms their own scheduled visit before it starts, is refused after it starts or
   once checked in, and the patient card offers Confirm; every gate green; live on the shared stack.
2. **Touch-list (verified live):** clinic-domain `ddls.go:1156-1190` (`.encounter` DDL — schema + text + example
   gain `superseded`) · `:1206-1240` (`.documentation` — `amendedAt`) · `:3078-3079` (constants; `MAX_ENCOUNTER_AMENDMENTS`
   beside them) · `:3602-3606, 3651-3672` (Reschedule — the status arm + event) · `:3708-3735, 3754-3758, 3785-3812`
   (SetStatus — the widened restriction, the self-confirm gate after `cur_val`, the clock read) · `:4130-4224`
   (RecordEncounter — the two optional reads, the arm split, the no-op, the cap) · `:522-599` (appointment DDL
   descriptions for the three ops) · `lenses.go:197, 254` (+`amended_at`) · `:680, 1026, 1066` (RETURN
   `a.documentation.data.amendedAt`) · `opmetas.go:237, 244` (text) · `:425-468` (`OptionalReads` + `.encounter`,
   `.documentation`; the `(facet)` courtesy line + `AmendmentLimit`) · `permissions.go:155-160` (note) ·
   `package.go:148` + `manifest.yaml:2` · tests `integration_test.go:1122-1215` (`TestClinic_RecordEncounter` —
   the "replaced whole" half becomes the addendum pins), `:413-415` (`clRecordEncounterReads` gains the two optional
   keys), `late_cancel_test.go:145-330` (the self-cancel harness the self-confirm vectors mirror),
   `status_clock_guard_test.go:142, 202` (reschedule + past-due pins), `lens_cypher_test.go`, `protected_lens_test.go`
   (`amended_at` on both protected specs). scripts `seed-showcase.go:1361, 1374` + `backfill-clinic-encounter-documentation.go`
   (the two optional reads). clinic-app `appointments.go:159, 187, 226, 291, 325` (+ `AmendedAt`) and the
   unprotected decode `:26-70` · `web/app.js:5177, 5457` (label) · `:5385-5424` (Confirm on the self card) ·
   `:5509-5517` (`encounterSummary`) · `:5583-5664` (`setStatus` — self declarations, confirm) · `:5816-5910`
   (reschedule context + toast) · `:6147-6263` (`openEncounter` / `submitEncounter`) · `web/index.html:455-460`
   (modal title) · tests `ledger_ui_test.go` / `lifecycle_transitions_test.go` (goja pins: `encounterSummary` with
   and without `amendedAt`; the self-card Confirm gate as a pure predicate).
3. **Precedents:** `correctedFrom` (the aspect-internal shadow, `ddls.go:3888-3892`); `MarkPastDueNoShow`'s empty
   batch (`:3945-3953`); the self-cancel block's ownership binding + clock (`:3708-3735, 3795-3812`) for the
   self-confirm; `TestClinic_SelfCancel_*` (`late_cancel_test.go`) for its vectors; `clDecryptEncounter`
   (`integration_test.go:212-219`) for reading the superseded text under the TestVault; the arrears fire's
   column-add on both protected lenses (`0771d77e`) + the `reference_protected_lens_provision_readpath` procedure;
   `submitCorrectStatus`'s `sent`/`confirmed` staging (already in `setStatus`).
4. **Increments:** (1) clinic-domain — the three scripts, DDL text, lenses, opmeta, permissions, version; tests:
   first record writes `superseded: []` and no `amendedAt`; amendment keeps the first text with its `recordedAt`,
   preserves `documentedAt`, stamps `amendedAt`, follow-up re-evaluated; a second amendment appends (two entries,
   ordered); identical re-submit → accepted, no mutation (revision unchanged); a legacy `.encounter` with no
   `.documentation` takes the first-record arm; the cap refuses at 50 (seed `superseded` directly under the vault
   helper rather than 50 submits); an undeclared submitter still amends (lazy seam decrypts); reschedule from
   `checkedIn` and from `confirmed` → `scheduled` + `statusReset`, from `scheduled`/absent → no status write, terminal
   still refused; `pastDueAppointments` cypher pin: a moved-back-to-scheduled row past its end is violating again;
   self confirm accepted (clock open AND late), refused `VisitStarted` at start, refused on `checkedIn`, idempotent
   re-confirm, a stranger's identity refused, `completed` via self still `AuthDenied`, staff confirm unchanged. Green:
   `go test ./packages/clinic-domain/ ./packages/clinic-reminders/ -count=1`, `go test ./internal/refractor/ -run
   'Corpus|Census' -count=1`. (2) clinic-app + scripts — the Go rows, the FE surfaces, the goja pins, the seed/backfill
   declarations. Green: `go test ./cmd/clinic-app/ -count=1`, `go build ./...`, `make vet`, `golangci-lint run ./...`,
   every `scripts/lint-*.go` (STRICT=1), `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`. (3) live:
   `make refresh-clinic` (chains `provision-readpath`), then `lattice lens health` on `clinicAppointmentsRead` /
   `providerAppointmentsRead` (clear a latch with `lens resume <bareId>` as the Loupe operator); cycle
   `bin/clinic-app`; amend a documented visit as its provider and read `amendedAt` off `/api/my-schedule`; move a
   checked-in visit and read `scheduled`; confirm a scheduled visit as its patient through `/v1/operations`.
5. **Gotchas:** one package edit ⇒ one bump + `Version`; a lens RETURN edit is a corpus edit (refractor census
   pins); `lint-refusal-courtesy` — `AmendmentLimit` at every `RecordEncounter` site (app.js + `(facet)`), and the
   self-confirm's `VisitStarted` / `AuthDenied` are existing codes already declared at `setStatus`; `lint-seed-declared-reads`
   — both new markers in every `scripts/` envelope; `lint-live-read-pinned-mutation` — the `.status` write in
   Reschedule and the `.documentation` write in RecordEncounter both follow `(d)`-annotated declared reads (hydrated,
   out of scope) — annotate the new reads on their own lines; `lint-app-op-descriptors` on every app comment;
   a protected column-add can LATCH between install and provision (memory 2026-09-15) — check `lens lag` after the
   refresh. `_packages.md` dossier: *a sensitive aspect whose holder is shredded FAILS the op* (decision: refusing
   an amendment of an unreadable record is correct; the first-record arm never reads plaintext); *a recorded value
   is read as the FACT it records* (`documentedAt` = first documented; `amendedAt` = current text recorded;
   `recordedAt` per superseded entry — the DDL says each); *the "leg" of a guard is every writer of the guarded
   value* — `.status` gains a THIRD writer (Reschedule) beside SetStatus / Correct / MarkPastDue: grep every
   `status.data.value` conjunct — `pastDueAppointments`, `appointmentReminders`, `clinicNoShowSettlement`, the FE's
   `ACTIVE_STATUSES` — and decide each (a reset to `scheduled` is the non-terminal state every one already handles);
   *a mirror that drops a precedent's check drops its invariant* — the self-confirm keeps the self-cancel's
   ownership binding, `require_matching_patient` and the clock, drops only the fee. `vertical-apps.md` dossier:
   *a count / label the FE promises applies the op's own predicate* — the Confirm button's gate is
   `status === scheduled && clock !== started`, the script's exact conjuncts, goja-pinned; *a staff-leg descriptor
   context that passes no `me`* — n/a (hand-built submit; the self path passes `authContext` via `submitOp`);
   *a transport throw after a destructive submit* — `setStatus`'s staging already covers confirm. Standing checklist
   #1 (the `superseded` list's lifetime: created on first record, appended on each amendment, never reset, bounded by
   the cap; `.status` reset is a state-machine edge, not new state) · #2 (the "24 scheduled, 0 confirmed" and
   "no numeric pg column" counts re-run at build) · #3 (every new conjunct revert-proven; `amendedAt` asserted equal
   to the lens column at the producer) · #5 (`.status`: three writers already, each on its own verb — the reset arm
   fires only on a value no other writer is racing to set) · #6 (the self-cancel path's undeclared `identifiedBy`
   read is debt this fire stops mirroring on the self path).
6. **Adjacent finds:** none beyond the self-path declaration debt above (absorbed).
7. **Non-goals:** rendering the superseded history (verdict 2); a provider-facing "restore prior version";
   refusing Reschedule on a checked-in visit; a self `checkedIn`; a desk "confirm on the patient's behalf" (staff
   already set any status); the reminder's own text; the other Clinic rows.
