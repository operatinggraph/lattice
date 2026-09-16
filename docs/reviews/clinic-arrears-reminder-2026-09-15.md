# Clinic — "A clinic debtor is never reminded" (2026-09-15)

**Filed (PO, 2026-09-15):** overdue is derived per read off the app clock
([ledger.go:204](../../cmd/clinic-app/ledger.go)) — no `.arrears` on the account, no reminder, no episode; the
patient learns only by opening Billing. Live: Riley Chen $25.00, 22 days overdue. Precedent:
[wellness](wellness-arrears-reminder-2026-09-15.md) · [loftspace](loftspace-rent-arrears-2026-09-15.md); a clinic
flags at check-in rather than refusing care — the design decides the hold. Winston-adjudicated (implementation-level;
no contract surface, no fork — the café mechanism is the ratified pattern, applied to one more ledger).

## Grounding

- **The clinic ledger is café's shape, not wellness's.** `vtx.clinicaccount` carries a maintained `.balance`
  (`clinicAccountBalance`, [ddls.go:106-139](../../packages/clinic-ledger/ddls.go)) that `post_entry` keeps in
  lockstep with every entry ([scripts.go:466-500, 663-672](../../packages/clinic-ledger/scripts.go)) — with a
  legacy branch for an account minted under < 0.3.0 that carries none (`balance_cents = None` unless a self-pay
  backfills it). Café's arrears mechanism was written against exactly this shape; wellness's copy dropped the
  `.balance` branches because wellness stores no balance, and the `_packages.md` dossier records that dropping them
  moved the episode boundary to the evaluation and fused two episodes inside one Weaver window. **Mirror café,
  not wellness** — every branch.
- **The account → patient relation is `heldFor`**, 0..1, account is the source
  ([scripts.go:100-112, 334-349](../../packages/clinic-ledger/scripts.go), `held_for_patient_id`); the `postedTo`
  replay with its page budget already exists as `backfill_balance` ([scripts.go:296-332](../../packages/clinic-ledger/scripts.go)).
  `reverses` is one hop (credit → charge), café's own shape — no marker vertex in between.
- **The café mechanism, end to end** (the precedent every edit site mirrors): `.arrears` aspect
  ([cafe-ledger/ddls.go:269-311](../../packages/cafe-ledger/ddls.go)); `EvaluateCafeArrears` op DDL on the account
  vertexType ([ddls.go:27-110](../../packages/cafe-ledger/ddls.go)); `arrears_entries` / `arrears_head` /
  `carry_arrears` + the three constants ([scripts.go:30, 151-300](../../packages/cafe-ledger/scripts.go)); the
  evaluate block ([scripts.go:380-564](../../packages/cafe-ledger/scripts.go)) — actor pinned to
  `primordialActor["weaver"]`, `historyTooLong` degrade, `remindedFor` on every due evaluation, `sentAt` once per
  episode, `external.notification` keyed `accountKey:dueAt`; the account-DDL `derive_reads`
  ([scripts.go:347-375](../../packages/cafe-ledger/scripts.go)); `post_entry`'s four `.arrears` branches — legacy →
  stale-only, `≤0 → >0` opens `{dueAt, evaluatedAt}`, `→ ≤0` ends `{evaluatedAt}`, partial → carry + stale
  ([scripts.go:1660-1735](../../packages/cafe-ledger/scripts.go)); `cafeArrearsReminders` target lens + declaration
  ([lenses.go:66-83, 152-252](../../packages/cafe-ledger/lenses.go)); the three informational columns on the
  per-lease row ([lenses.go:137-151](../../packages/cafe-ledger/lenses.go)); the playbook with its `Enumerations`
  ([targets.go:55-78](../../packages/cafe-ledger/targets.go)); the notification replyOp as an idempotent
  OVERWRITE ([notifications.go](../../packages/cafe-ledger/notifications.go), whole); the operator grant
  ([permissions.go:102-106](../../packages/cafe-ledger/permissions.go)); opmeta `OptionalReads` carrying `.arrears`
  on every entry op ([opmetas.go:121, 192, 257](../../packages/cafe-ledger/opmetas.go)). Wellness added a
  `maxretries_evaluation` column ([wellness-ledger/lenses.go:163, 278](../../packages/wellness-ledger/lenses.go))
  — the package's target idiom; clinic's `clinicNoShowSettlement` declares none, so neither does this target.
- **The app already renders arrears off a derivation.** `handleLedger` returns `dueDate/isOverdue/daysOverdue`
  from `deriveStatement` ([ledger.go:498-587](../../cmd/clinic-app/ledger.go)); `handleStaffArrears` builds the
  desk grid the same way ([ledger.go:597-679](../../cmd/clinic-app/ledger.go)); `state.arrears` (a `patientKey →
  row` map off `/api/staff/arrears`, [app.js:797-806](../../cmd/clinic-app/web/app.js)) feeds `arrearsBadgeText` on
  the roster select + the patient-contact line ([app.js:711, 870, 3899](../../cmd/clinic-app/web/app.js)) and
  `overdueBookingPrompt`'s confirm before a desk booking ([app.js:3160, 3914](../../cmd/clinic-app/web/app.js));
  `renderLedger`'s balance line says `due` / `N days overdue` ([app.js:4153-4170](../../cmd/clinic-app/web/app.js)).
  Nothing says a reminder went out, and the desk's appointment card — where **Check in** renders
  (`lifecycleButtons`, [app.js:5629](../../cmd/clinic-app/web/app.js)) — carries no arrears at all. The recorded-wins
  handler shape is wellness's `recordedOrDerivedDueDate` / `computeOverdue`
  ([wellness-app/ledger.go:100-135, 506-531, 616-627](../../cmd/wellness-app/ledger.go)).
- **Notification transport exists for the clinic** (`clinic-reminders`, create-only outcome aspects — right for a
  visit reminded once, wrong for a recurring episode; café's doc comment records exactly this). The bridge's
  `notification` adapter takes free-form params ([fake_notification.go:41-51](../../internal/bridge/fake_notification.go)).
- **No `scripts/verify-package-clinic-ledger.go`** exists; no `ddlCheck` pin to extend. `clinic-domain` reads no
  clinic account today.

## Verdict — *the clinic ledger records its episodes the way the café ledger does; the desk sees the debtor at check-in; nobody is refused care*

1. **The hold: none.** A clinic is not a café. The PO's own filing says "flags at check-in rather than refusing
   care", and every clinic write path stays open to a debtor — `CreateAppointment`, `SetAppointmentStatus(checkedIn)`,
   `RecordEncounter` all untouched, `clinic-domain` untouched. What the reminder changes is what the desk and the
   patient SEE: the desk's appointment card names the debt beside Check in, the roster badge says a reminder went
   out, the statement says so too. The existing overdue booking confirm stays as it is.
2. **`.arrears` on `vtx.clinicaccount`** (class `clinicAccountArrears`) = `{evaluatedAt, dueAt?, remindedFor?,
   sentAt?, stale?, historyTooLong?}` — café's aspect, café's lifecycle, recorded facts only (no clock in any lens).
   Lifetime: `post_entry` opens an episode on a debit that takes a `.balance` of `≤ 0` to `> 0` (`dueAt = postedAt +
   term`, `evaluatedAt`), ends one on any entry that leaves `≤ 0` (`{evaluatedAt}` alone), carries + marks `stale` on
   a partial payment or a refund that leaves a balance, and on a LEGACY account (no `.balance`, not backfilled) marks
   what exists stale and mints nothing; `EvaluateClinicArrears` mints / rewrites it on every outcome. Never
   tombstoned. One writer per verb (evaluation mints/recomputes; `post_entry` opens/ends/carries).
3. **`EvaluateClinicArrears{accountKey}`** on the `clinicaccount` vertexType DDL — café's op verbatim: actor pinned
   to `primordialActor["weaver"]` as the first statement; paged `postedTo` replay (`ARREARS_PAGE_LIMIT` 50 ×
   `ARREARS_MAX_PAGES` 10, `historyTooLong` recorded, never a refusal); one-hop `reverses` netting; FIFO head over
   `(postedAt, key)`; a **15-day term** (`ARREARS_GRACE_DURATION = "360h"`, `ArrearsGraceDays = 15` exported and
   pinned equal to `cmd/clinic-app`'s `statementGraceDays` by a Go test); `remindedFor` on every due evaluation;
   `sentAt` once per EPISODE; `external.notification` keyed `accountKey:dueAt` with params `{accountKey,
   reminderType: "clinicArrears", dueAt, balanceCents, patientKey?}` where the patient is resolved LIVE off the
   account's own `heldFor` (never the payload; absent → still evaluated, the fact is the account's). Operator-granted
   (Weaver's service actor), no consoleOperator / frontOfHouse / consumer grant.
4. **`clinicArrearsReminders`** weaver-target lens + playbook: café's cypher on `clinicaccount` with the `heldFor`
   hop to the patient carried as an informational `patientKey` column (0..1, OPTIONAL, never a `Params` entry);
   `missing_evaluation → directOp(EvaluateClinicArrears)` with `Params {accountKey: row.entityKey}`, `Reads
   [row.entityKey]`, `OptionalReads [row.entityKey.arrears]`, `Enumerations` for `postedTo in` + `heldFor out`.
   **`RecordClinicArrearsReminderNotification`** → `.arrearsNotification` (class
   `clinicAccountArrearsNotification`) as an idempotent overwrite, café's reasoning verbatim: episodes recur.
5. **`clinicPatientAccounts` projects `arrearsDueAt`, `arrearsRemindedFor`, `arrearsReminderSentAt`** — the
   per-patient row both app handlers already read. `/api/ledger` gains `reminderSentAt`; `/api/staff/arrears` rows
   gain `reminderSentAt`; in both, the RECORDED `dueAt` wins when the row has one and `deriveStatement`'s derivation
   stands only in its absence, with `isOverdue/daysOverdue` recomputed against whichever date is shown
   (wellness's `recordedOrDerivedDueDate` + `computeOverdue`, copied — a stamp that names a date reads the recorded
   one when it exists). `.arrears.sentAt` is the SEND INTENT: the FE says "a reminder was sent <date>" off it, as
   wellness does, and the delivery fact is `.arrearsNotification`.
6. **The FE says so — three surfaces, no gate.** (a) `arrearsBadgeText` appends `· reminded <YYYY-MM-DD>` when the
   row carries `reminderSentAt` (roster select + patient-contact line pick it up for free). (b) The staff appointment
   card (`renderApptCard`, not `opts.asSelf`) gains a `meta arrears` line from `state.arrears.get(a.patientKey)` —
   `💳 owes $X · N days overdue · reminded <date>` — rendered above the actions block so the desk reads it beside
   Check in; absent for a patient who owes nothing. (c) `renderLedger`'s balance line appends `· a reminder was sent
   <date>` when `data.reminderSentAt`. Dates are the UTC `slice(0, 10)` of the recorded stamp, the same slice the
   script would print — one instant, one rendering. Each is a goja-pinned pure function.
7. **The descriptors + DDL text state the rule**; clinic-ledger 0.5.3 → 0.6.0 (`Version` + manifest), `Depends +=
   orchestration-base` (the lapse marker's writer); `clinic-ledger` opmetas' `OptionalReads` gain
   `{payload.accountKey}.arrears` on both entry ops; `derive_reads` returns it on both, and the account-DDL script
   gains café's `derive_reads` for the evaluate op.

### Fire brief (build note, 2026-09-15)

1. **Scope** (board row, verbatim): *Overdue is derived per read off the app clock — no `.arrears` on the account,
   no reminder, no episode; the patient learns only by opening Billing. Live: Riley Chen $25.00, 22 days overdue.
   Precedent: wellness · loftspace; a clinic flags at check-in rather than refusing care — the design decides the
   hold.* Green bar: an account whose FIFO head is 15+ days old is evaluated by Weaver, stamped `sentAt`, notified once
   per episode; the patient's statement, the roster badge and the desk's appointment card say due / overdue /
   reminded; a payment to zero ends the episode; no clinic op refuses a debtor.
2. **Touch-list (verified live):** clinic-ledger `ddls.go:23-63` (accountDDL — `PermittedCommands` gains the evaluate
   op; new aspect DDL beside `:106-139`) · `scripts.go:1-60` (accountDDLScript — helpers + `derive_reads` + the
   evaluate branch in its `execute`) · `:288-297` (constants beside `BALANCE_BACKFILL_*`) · `:296-349` (the walks to
   mirror) · `:466-500` (`.balance` read — the `.arrears` read sits beside it) · `:663-672` (the `.balance` write —
   the four `.arrears` branches follow it) · `:753-800` (`derive_reads` gains `acct_key + ".arrears"`) · new
   `notifications.go` · `lenses.go:41-56` (declarations) + `:213-223` (`patientAccountsSpec` RETURN) + new target
   const/spec · `targets.go` (second `WeaverTargetSpec`) · `permissions.go:56-79` (append) · `opmetas.go:106, 143`
   (`OptionalReads`) + a bare `OpMetaSpec` for the evaluate op · `package.go:90, 106` + `manifest.yaml:2, 33` · tests
   `ledger_test.go` (env installs `orchestration-base` before clinic-ledger — wellness `ledger_test.go:82`),
   `lens_cypher_test.go`. clinic-app `ledger.go:168` (`statementGraceDays`) · `:321-330` (`patientAccountProjection`
   gains the three columns) · `:336-370` (`resolvePatientAccount(s)` return the projection, not the bare key) ·
   `:374-381` (`arrearsRow.ReminderSentAt`) · `:396+` (`computeArrears` applies recorded-wins per row) · `:498-587`
   (`handleLedger` — recorded-wins + `reminderSentAt`) · `:597-679` · `web/app.js:3899-3906` (`arrearsBadgeText`) ·
   `:5217-5330` (`renderApptCard` — the arrears line) · `:4153-4170` (`renderLedger`) · tests `ledger_ui_test.go`
   (`ledgerUIVM` :61, `TestArrearsBadgeText` :90), `ledger_test.go`.
3. **Precedents:** every café anchor in Grounding; wellness-app `recordedOrDerivedDueDate` / `computeOverdue` /
   `lookupMemberArrears` (`ledger.go:83-135`) and its `statementLine` (`app.js:505-520`, the reminder suffix);
   clinic-app's own `arrearsBadgeText` / `overdueBookingPrompt` goja pins (`ledger_ui_test.go:90-160`); café tests
   `TestArrears_*` (`ledger_test.go:2156-3030`, incl. `evaluateArrears` :2052 submitting as
   `bootstrap.WeaverIdentityKey`) and `TestCafeArrears_*` (`lens_cypher_test.go:407-700`).
4. **Increments:** (1) clinic-ledger — aspect + op + constants + helpers + `post_entry`'s four branches + notification
   replyOp + target lens + playbook + patient-accounts columns + permissions + opmetas + `derive_reads` + version +
   Depends; tests mirror café's set (first charge opens, second charge leaves the head, partial → stale, pay to zero
   ends, debit on a credit balance opens none, evaluate sends once then nothing, re-arms on a covered head, nets a
   refund, one notification per episode not per head — **the mandated vector: the reminded charge paid EXACTLY while
   a later charge keeps the episode open, no second send**, forged send refused, non-Weaver actor refused, patient
   resolved from account state, no-heldFor still evaluates, legacy account only ever marks stale, history past the
   budget degrades, FIFO matches the statement, undeclared submitter still hydrates `.arrears`, notification reply
   overwrites + refuses a non-clinicaccount ref; lens pins never-evaluated / pending / due / sent / stale / cleared /
   new episode after old lapse / sibling-target lapse / historyTooLong quiet + reopens / no patient still projects /
   patient-accounts columns). Green: `go test ./packages/clinic-ledger/ -count=1`, `go test ./internal/refractor/
   -run 'Corpus|Census' -count=1`. (2) clinic-app — projection fields, recorded-wins in both handlers,
   `reminderSentAt` on both responses, the grace-equality pin, `arrearsBadgeText` suffix, the card's arrears line,
   the balance line's suffix; goja pins for each (with and without `reminderSentAt`; the card line absent on a
   zero row and on `asSelf`). Green: `go test ./cmd/clinic-app/ -count=1`, `go build ./...`, `make vet`,
   `golangci-lint run ./...`, every `scripts/lint-*.go` (STRICT=1 where honoured). (3) live: `make refresh-clinic`
   (or `reinstall-package PKG=clinic-ledger`), cycle `bin/clinic-app`; every standing account evaluates once; Riley
   Chen ($25, overdue) → `sentAt` + `.arrearsNotification`; `/api/ledger` reads `reminderSentAt`; the desk card
   shows the line; a payment to zero → `{evaluatedAt}` alone.
5. **Gotchas:** one package edit ⇒ one version bump + `Version` constant (`DIFF_BASE=<sha> go run
   ./scripts/lint-package-version.go`); a lens RETURN edit is a corpus edit (`internal/refractor` census pins —
   `clinicPatientAccounts` and the new target); `lint-links-page-limit` on every new `kv.Links`; `lint-live-read-
   pinned-mutation` — the `.arrears` upsert is OCC on the declared read's revision (hence `derive_reads`);
   `lint-gap-column-declaration` for the new target's gap column; `lint-seed-declared-reads` — `grep -rn
   "ClinicDebitAccount\|ClinicCreditAccount" scripts/` for any seed envelope that now needs `.arrears` declared;
   `lint-refusal-courtesy` — the evaluate op is Weaver-only (no dispatch site; no courtesy lines) but `post_entry`
   gains a new `InvalidState` (wrong `.arrears` class) reaching both entry ops' dispatch sites — declare it `none`
   where the gate asks; the target's `Params` names only `row.entityKey` (an optional-hop `patientKey` column is
   informational, never a param). `_packages.md` dossier: *a mirror that drops one of the precedent's checks or
   branches drops the INVARIANT it enforced* (mirror café's four `post_entry` branches AND its `.balance → 0`
   boundary — do not import wellness's evaluation-time approximation; run the mandated exact-payment vector);
   *a recorded value is read as the FACT it records* (`sentAt` = send intent; `.arrearsNotification` = delivery);
   *a dispatch declaration must name what the runtime binds* (`Enumerations` on the playbook for both walks);
   *the "leg" is every writer of the guarded VALUE* — there is no guard here, by decision 1; *a confinement guard
   tested only as the operator has never run* — the evaluate op's actor pin needs the NON-Weaver negative vector.
   `vertical-apps.md` dossier: *two courtesy surfaces name the same instant* (every FE date is the UTC `slice(0,10)`
   of the recorded stamp — badge, card, statement alike); *a new terminal state is a census of every render gate*
   (`state.arrears` consumers: roster select, contact line, booking confirm, ledger header — plus the new card line;
   the patient's own `asSelf` card stays silent, the statement speaks for them). Standing checklist #1 (the
   lifetime in verdict 2) · #3 (each `post_entry` branch and each lens conjunct revert-proven; the threaded
   `reminderSentAt` asserted equal to the lens column at the producer) · #5 (`.arrears` — evaluation vs `post_entry`
   split by verb; `.arrearsNotification` one writer) · #6 (café's `lease_for_account` becomes a patient walk; its
   `leaseAppKey` param is NOT copied).
6. **Adjacent finds:** none surfaced by the scouts.
7. **Non-goals:** any refusal of a clinic op on debt (`CreditHold` does not exist in the clinic); a clinic-domain
   read of `.arrears`; a Facet descriptor change (no self-anchored op gains a courtesy); the desk's reminder re-send
   verb; `maxretries_evaluation`; the café / wellness / loftspace mechanisms themselves; the other four Clinic rows
   (their own units).
