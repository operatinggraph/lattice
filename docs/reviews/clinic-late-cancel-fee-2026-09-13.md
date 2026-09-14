# Clinic — "A patient who cancels after the visit started owes nothing, and the desk can't bill it after" (2026-09-13)

**Filed (PO, 2026-09-13):** the patient-self cancel / reschedule path reads no cutoff, and
`CorrectAppointmentStatus` → `noShow` writes no `noShowFeeCents`, so `clinicNoShowSettlement` never charges
the correction. Wellness's `forfeited` late cancel (`ae197820`, [triage §3](verticals-designer-triage-2026-09-10.md))
is the precedent. Winston-adjudicated (implementation-level; no contract surface, no fork).

## Grounding

- **The self path has no clock.** `SetAppointmentStatus`'s `op.authContextTarget != ""` block restricts a
  self-scoped caller to `cancelled` and proves ownership, nothing more
  ([ddls.go:3550-3577](../../packages/clinic-domain/ddls.go)); `enforce_started` returns at once for any status
  but `completed`/`noShow` ([ddls.go:2944-2962](../../packages/clinic-domain/ddls.go)) — *"Cancel carries no
  clock — it is the legitimate before-the-visit terminal."* `RescheduleAppointment`'s self path binds ownership
  and then applies `enforce_future` to the NEW time only ([ddls.go, `if ot == "RescheduleAppointment"`
  +55..+80](../../packages/clinic-domain/ddls.go)): a visit whose start has passed can be moved out of
  `MarkPastDueNoShow`'s reach by its own patient, for free. Both are one gap: the patient controls whether a
  missed visit is ever billable.
- **The correction writes no fee.** `CorrectAppointmentStatus` upserts `{value, note, correctedFrom}` only
  ([ddls.go:3697-3698](../../packages/clinic-domain/ddls.go)); its descriptor even claims *"Does not reverse a
  no-show fee already charged — use a manual credit"* ([opmetas.go:282](../../packages/clinic-domain/opmetas.go)),
  which the lens's own `missing_reversal` gap contradicts.
- **The ledger bills a STATUS, not a fee.** Every `clinicNoShowSettlement` gap is conjoined on
  `status = 'noShow'` ([clinic-ledger/lenses.go:118-151](../../packages/clinic-ledger/lenses.go)); the fee field is a
  second conjunct. The lens comment records why: *"the charge itself is minted only while status IS noShow"* —
  a premise this fire retires.
- **The fee's writers, enumerated** (the guarded VALUE's legs, per the `_packages.md` dossier): `CreateAppointment`
  (initial `scheduled`, no fee) · `SetAppointmentStatus` (`noShow` → caller fee or 2500; every other value → none)
  · `CorrectAppointmentStatus` (none, ever) · `MarkPastDueNoShow` (`noShow`, deliberately none — *"a documentation
  lapse, not a charge"*, [ddls.go:3767](../../packages/clinic-domain/ddls.go)).
- **The FE mirrors the clinic's clocks as pure predicates**, goja-pinned: `lifecycleTransitions(status, started)`
  ([app.js](../../cmd/clinic-app/web/app.js), [lifecycle_transitions_test.go](../../cmd/clinic-app/lifecycle_transitions_test.go));
  the self card offers Reschedule + Cancel for any `ACTIVE_STATUSES` row ([app.js:4945-4963](../../cmd/clinic-app/web/app.js)).
  Wellness's mirror is `isLateCancel` + `LATE_CANCEL_WINDOW_MS` ([wellness app.js:1072-1089](../../cmd/wellness-app/web/app.js)).
- **Live:** appointments carry `noShowFeeCents` only under `noShow`; no `.status` carries `lateCancel` (the field
  does not exist). The protected appointment read models project `status` + `status_note` from `.status` and
  nothing else ([lenses.go:1014-1015, 1054-1055](../../packages/clinic-domain/lenses.go)).

## Verdict — *the fee is a fact on the status, and the ledger bills the fact*

Three rules, one constant, one lens predicate.

1. **The self path has a clock, in three states.** `LATE_CANCEL_WINDOW_OFFSET = "-24h"` (the reminder lead:
   the reminder that says "your visit is tomorrow" is the last free-cancel moment). For a self-scoped caller
   (`op.authContextTarget != ""`), against `.schedule.startsAt` and `op.submittedAt` (canonical UTC, lexical ==
   chronological, the `enforce_started` idiom; boundaries inclusive on the stricter side, as wellness):
   - **started** (`submitted >= startsAt`): cancel AND reschedule refused `VisitStarted` — the desk records the
     outcome (`noShow` with its fee, or `completed`). The wellness `SessionStarted` rule.
   - **late** (`startsAt − 24h <= submitted < startsAt`): **cancel is allowed and owes the no-show fee** — the
     status lands as `{value: cancelled, lateCancel: true, noShowFeeCents: 2500, note?}`; **reschedule is
     refused `LateReschedule`** (call the desk) — a free late move is the side door around the fee, and the fee
     lives on `.status`, which a reschedule never writes.
   - **open**: as today.
   Staff paths are untouched: a desk cancel owes nothing (the clinic may be the one calling it off), and the desk
   bills a late phone-cancel through rule 3.
2. **The idempotent same-value re-cancel carries the fee forward.** `SetAppointmentStatus`'s "re-setting the same
   terminal value is idempotent (…lets a noteless re-set clear a prior note)" would silently drop `lateCancel` +
   `noShowFeeCents` and the lens would then *reverse* the charge (rule 4). A `cancelled → cancelled` re-set copies
   both fields from the current `.status` (a declared optionalRead); the note keeps today's clear-on-omit semantics.
   Waiving the fee is `CorrectAppointmentStatus(cancelled, note)` — the correction's own no-fee write, rule 3.
3. **The correction writes the fee exactly as the first transition does.** `CorrectAppointmentStatus` → `noShow`
   takes an optional `noShowFeeCents` (positive; default 2500), the same validation as `SetAppointmentStatus`;
   → `completed` / `cancelled` writes none. The descriptor gains the money field under
   `x-visibleWhen {status = noShow}` (the `followUpDate` precedent), and its false "does not reverse" sentence is
   rewritten to what the lens does.
4. **The ledger bills the fee's presence, not the status.** `clinicNoShowSettlement`'s three gaps become
   `owes = (feeCents <> null) AND (feeCents > 0)`: `missing_account = owes AND accountKey = null`,
   `missing_charge = owes AND accountKey <> null AND txCount = 0`,
   `missing_reversal = NOT owes AND txCount = 1 AND reversalCount = 0`; the memo is
   `CASE WHEN status = 'cancelled' THEN 'Late-cancellation fee' ELSE 'No-show fee' END`. Every writer in the
   grounding's ledger stays correct under the new predicate: `MarkPastDueNoShow`'s fee-less `noShow` still charges
   nothing; a corrected-off charge still reverses; a same-value `noShow` re-set with a different fee still stands
   at the posted amount (today's posture — amount drift is not a gap, and the `txCount = 0` gate never lets a
   second charge mint).

**State table of `.status.{lateCancel, noShowFeeCents}` (checklist #1):** created by rule 1 (self late cancel),
rule 3 (correction → `noShow`) and today's `SetAppointmentStatus(noShow)` · carried by rule 2 (same-value
`cancelled` re-set) and by `RescheduleAppointment` never (a terminal row is not movable, `TerminalStatus`) ·
cleared by a correction to a fee-less value (`completed`, `cancelled`) — which is exactly the waiver, and the
reversal is its observer · reset by nothing else (`MarkPastDueNoShow` only writes over a non-terminal row).

**Consumer census — every reader of a clinic appointment's `.status`** (re-run live at selection):

| Consumer | Reads | On a `cancelled` row carrying `lateCancel` + a fee |
|---|---|---|
| `clinicNoShowSettlement` ([clinic-ledger/lenses.go:144-151](../../packages/clinic-ledger/lenses.go)) | `value`, `noShowFeeCents` | **must change** — rule 4; the payoff |
| `clinic-reminders` `nonTerminalAppointment` ([lenses.go:28](../../packages/clinic-reminders/lenses.go)), `followups.go:306`, `visitseries.go:1184` | `value` only | inert — still `cancelled`: not past-due, not credited, not reminded |
| `clinicAppointments` / `clinicAppointmentsRead` / `providerAppointmentsRead` ([lenses.go:670, 1014, 1054](../../packages/clinic-domain/lenses.go)) | `value`, `note` | project `cancelled` + the note; **no new column** (non-goal below) |
| `ledgerHistorySpec` ([clinic-ledger/lenses.go:172](../../packages/clinic-ledger/lenses.go)) | the `settles` hop | the fee line ties to the visit (`appointmentKey`, `visitStartsAt`) — the patient's and the desk's money surface |
| `RescheduleAppointment` `TerminalStatus` ([ddls.go](../../packages/clinic-domain/ddls.go)) | `value` | refused — correct |
| `SetAppointmentStatus` terminal guard + idempotent re-set | `value` | **must change** — rule 2 |
| `CorrectAppointmentStatus` `NotTerminal` / `correctedFrom` | `value` | **must change** — rule 3; `correctedFrom: cancelled` records the late cancel it overwrote |
| `MarkPastDueNoShow` | `value` | skips any terminal row — inert |
| FE `apptBlocks`, `canCancel`-style filters ([app.js:2445](../../cmd/clinic-app/web/app.js)), `STATUS_LABEL`, badges | `status` | inert — the value is unchanged |
| FE self card Reschedule / Cancel ([app.js:4945-4963](../../cmd/clinic-app/web/app.js)) | `status`, `startsAt` | **must change** — the three-state mirror |
| FE `setStatus` (both hats) ([app.js:5095](../../cmd/clinic-app/web/app.js)) | — | the self late-cancel confirm names the fee; a `VisitStarted` / `LateReschedule` refusal renders as the reason |
| FE Correct-status descriptor form ([app.js:5219](../../cmd/clinic-app/web/app.js)) | the catalog row | gains the fee field from the descriptor, nothing to edit |
| `cmd/clinic-app` Go | `status` column | no reader of the fee; inert |

**Alternatives.**

| Option | Why not |
|---|---|
| **Do not have this thing** — the desk marks `noShow` after the fact; a self-cancel is a courtesy | The gap is exactly that the desk *cannot* mark it: the patient's own cancel/reschedule lands first and the record then reads as a fee-less terminal (or a moved visit); and the correction — the desk's only repair — writes no fee at all. Zero of the three legs bills today |
| Refuse every self cancel inside the window (no fee, "call the desk") | Worse product for the same money: the patient phones, the desk cancels fee-free (the staff path has no fee), and the desk still has to remember to correct it to bill. Wellness's ratified shape is *allowed + forfeits* |
| A `lateCancelled` status value (the wellness `forfeited` token) | Every `cancelled` consumer above (four lenses, the FE filters, the reminders fragment) would need the new token; wellness needed a new token only because its cancel *deleted* the vertex. Clinic's cancel already keeps it — the fact that changes is the fee, so record the fee |
| Widen the lens to `status IN ('noShow','cancelled')` + keep the fee conjunct | Bills a status again; rule 4's fee-presence predicate is one clause and true for every writer in the grounding's ledger, including the fee-less `MarkPastDueNoShow` |
| A self reschedule inside the window allowed with the fee | The fee lives on `.status`, a reschedule writes `.schedule`; billing a move needs a second fee carrier and a second lens gap. Refuse the move, keep one carrier |
| Project `lateCancel` onto the appointment read models for a card badge | A new column on a live protected Postgres table: `provision-readpath` is `CREATE TABLE IF NOT EXISTS` and cannot add it, so the running lens would fault on insert until a hand migration. The money surface is the ledger, which already ties the line to the visit |

**Contract surface:** none. **Test strategy:** clinic-domain pipeline tests for the self path — `VisitStarted`
on cancel and reschedule at `startsAt`, `LateReschedule` at `startsAt − 24h`, a late cancel accepted with
`{cancelled, lateCancel, 2500}` and its cells released, an open cancel with neither field, a same-value re-cancel
carrying both forward, and the staff cancel inside the window fee-less; `CorrectAppointmentStatus` → `noShow`
writes 2500 / a caller fee / refuses a non-positive one, → `completed` writes none; each refusal reverted-proven
(the guard removed, the test fails). clinic-ledger `lens_cypher_test.go` pins: a `cancelled` + fee row violates
`missing_charge` with memo `Late-cancellation fee`, a `cancelled` row without a fee never violates, a charged
`noShow` re-set without a fee opens `missing_reversal`, the existing `NOSHOW_NO_FEE` case still passes. FE: a
goja pin of the three-state self predicate. Live: a seeded patient late-cancels on `:7799` → the ledger shows the
`Late-cancellation fee` line against the visit; a correction to `noShow` on a desk-cancelled visit charges $25.
**Size M (pkg + lens + FE) · Winston-adjudicated.**

### Fire brief (build note, 2026-09-13)

1. **Scope** — the board row's next step, verbatim: *"a late-cancel window on the self path; the correction
   carries the fee"*, built as the four rules above. Green bar: the test strategy's pins + the live proof.
2. **Touch-list (verified live at selection):** `packages/clinic-domain/ddls.go` — `SetAppointmentStatus`
   self block `:3550-3577` and terminal branch `:3621-3631`; `CorrectAppointmentStatus` `:3636-3702`;
   `RescheduleAppointment` self block (+40..+55 from its `if ot ==`); `enforce_started` `:2944`; the
   `appointmentStatus` aspect DDL `:920-951` (schema + descriptions gain `lateCancel`; the fee's "only when
   noShow" wording widens); the op DDL descriptions/examples at `:540, 549, 645, 666, 725`; `TERMINAL_STATUSES`
   `:2987`. `packages/clinic-domain/opmetas.go:279-330` — the correction descriptor (fee field, description fix).
   `packages/clinic-domain/manifest.yaml:2` + `package.go:148` (0.34.27 → 0.35.0: a new field + two new refusals).
   `packages/clinic-ledger/lenses.go:76-151` (rule 4 + its comment) + `manifest.yaml:2` / `package.go:90`
   (0.3.0 → 0.4.0). `cmd/clinic-app/web/app.js` — `renderApptCard` `:4945-4963`, `setStatus` `:5095-5160`, a new
   pure predicate beside `lifecycleTransitions`. Tests: `packages/clinic-domain/status_clock_guard_test.go`,
   `correct_appointment_status_test.go`, `integration_test.go:3157` (self-cancel precedent);
   `packages/clinic-ledger/lens_cypher_test.go:108-161`; `cmd/clinic-app/lifecycle_transitions_test.go`.
3. **Precedents:** wellness `LATE_CANCEL_WINDOW_OFFSET` + `is_late_cancel` + `SessionStarted`
   ([wellness ddls.go:3798, 4660-4676](../../packages/wellness-domain/ddls.go)); `enforce_started`'s compare idiom;
   `SetAppointmentStatus`'s own fee block `:3600-3611` (copied into the correction); `x-visibleWhen` on
   `followUpDate` ([ddls.go:650](../../packages/clinic-domain/ddls.go)); the descriptor form's money kind for a
   `*Cents` field ([form.mjs:91-93, 202-211](../../internal/descriptorform/form.mjs)); `CASE WHEN … END AS` in a
   RETURN ([cafe-domain/lenses.go:328](../../packages/cafe-domain/lenses.go)); wellness FE `isLateCancel` + its
   confirm ([wellness app.js:1085, 1241](../../cmd/wellness-app/web/app.js)); `lifecycleTransitions` as the
   goja-pinned pure predicate shape.
4. **Increments:** (1) clinic-domain script + DDL + descriptor + tests — `go test ./packages/clinic-domain/
   -count=1`; (2) clinic-ledger lens + pins — `go test ./packages/clinic-ledger/ -count=1` and
   `go test ./internal/refractor/ -run 'Corpus|Census' -count=1` (a `Spec` edit is a corpus edit); (3) FE +
   goja pin — `go test ./cmd/clinic-app/ -count=1`, `node --check`, `make lint-web lint-app-op-descriptors`;
   then `go build ./... && make vet && golangci-lint run ./... && STRICT=1 go run ./scripts/lint-conventions.go &&
   DIFF_BASE=<base> go run ./scripts/lint-package-version.go`; live: `make refresh-clinic` from the main checkout.
5. **Gotchas:** both package versions bump (`lint-package-version`); the self-path reads — `.schedule` is already
   in `reads` on the terminal branch and the reschedule path, `.status` in `optionalReads` — so no dispatcher
   declaration moves; `lint-app-op-descriptors` reads Go comments (name ops by role); the dead-conjunct gate
   (`if false &&`) — revert-proofs run in the worktree, never the shared tree; the `_packages.md` dossier: *the
   "leg" is every op that writes the guarded VALUE — grep the aspect's writers before closing* (the fee's four
   writers are enumerated above), *a guard's OCC rests on whoever writes its read declaration* (no new OCC here —
   the status upsert stays unconditioned, and the carry-forward reads a declared optionalRead), *for each gap
   conjunct, name the op-side read that answers the same question* (`owes` ⇔ the script's fee write); the
   `vertical-apps.md` dossier: *when an op gains a server refusal, walk every form that dispatches it and give
   each the courtesy its sibling already has* (Cancel and Reschedule on the self card — both gated by the same
   predicate; the staff card unchanged), *a count/label the FE promises must apply the op's own predicate* (the
   confirm's "$25.00" is the script's 2500 default), *the throw path of an irreversible op says the write may
   have landed* (existing `setStatus` posture, unchanged). Standing checklist walked: #1 the state table above;
   #2 the live census re-run; #3 every refusal reverted-proven, the fee write asserted equal to its source at
   the producer; #4 nothing removed; #5 one writer per key unchanged; #6 the wellness precedent's compare idiom
   verified against `enforce_started`.
6. **Adjacent finds:** the correction descriptor's false "does not reverse" sentence — fixed in this fire.
7. **Non-goals:** no staff-path fee on `SetAppointmentStatus(cancelled)`; no new read-model column; no
   `cancelledAt`; no change to `MarkPastDueNoShow`; no amount-drift convergence; no wellness/café/LoftSpace edit.
