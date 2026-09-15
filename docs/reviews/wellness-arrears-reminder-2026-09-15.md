# Wellness — "A wellness debtor is never told, never reminded, and books unasked" (2026-09-15)

**Filed (PO, 2026-09-13):** the member statement says "Balance owed" with no due date or overdue flag
([app.js:488](../../cmd/wellness-app/web/app.js)) though `deriveStatement` computes both; no arrears reminder
op / notification exists (café precedent: [notifications.go:53](../../packages/cafe-ledger/notifications.go)); a
3-day-overdue member self-books with no word (live: Alex Kim, $20). Mirror café: reminder recorded + shown,
`CreateBooking` refuses `CreditHold` off it. Winston-adjudicated (implementation-level; no contract surface, no
fork — the café mechanism is the ratified pattern, applied to one more ledger).

## Grounding

- **Wellness-ledger stores no balance and no arrears state.** The balance is derived by summing `.entry` rows
  ([lenses.go:5](../../packages/wellness-ledger/lenses.go), a design decision, not a gap) and the account carries
  no `.arrears` aspect at all. `deriveStatement` ([ledger.go:210-306](../../cmd/wellness-app/ledger.go)) recomputes
  a FIFO head + a 15-day term (`statementGraceDays`, `ledger.go:22`) on every read, server-side, for the desk's
  `/api/frontdesk-arrears` grid only; the member's own `/api/ledger` returns `{accountKey, transactions,
  balanceCents}` ([ledger.go:445](../../cmd/wellness-app/ledger.go)) and `ledgerBalanceLine` renders
  "Balance owed: $X" ([app.js:488-493](../../cmd/wellness-app/web/app.js)). Nothing is recorded, so nothing can be
  reminded for or held on.
- **The café mechanism is the pattern, end to end.** `cafeArrearsReminders`
  ([cafe-ledger/lenses.go:153-252](../../packages/cafe-ledger/lenses.go)) — one row per account, `freshUntil = dueAt`
  arms Weaver's `@at`, the fired timer's recorded lapse (`freshnessExpiry.byTarget.<target>`) opens
  `missing_evaluation`, the playbook dispatches `directOp(EvaluateCafeArrears{accountKey: row.entityKey})` with
  `OptionalReads: [row.entityKey.arrears]` ([targets.go:70-72](../../packages/cafe-ledger/targets.go)); the op
  replays the account's own `postedTo` history paged (`ARREARS_PAGE_LIMIT`/`ARREARS_MAX_PAGES`,
  [scripts.go:152-153](../../packages/cafe-ledger/scripts.go)), nets refunds against their charge, stamps
  `.arrears = {evaluatedAt, dueAt, remindedFor, sentAt}` and emits `external.notification` off its own outbox keyed
  `accountKey:dueAt` ([scripts.go:470-534](../../packages/cafe-ledger/scripts.go)); the bridge's `notification`
  adapter replies with `RecordCafeArrearsReminderNotification` → `.arrearsNotification` as an idempotent OVERWRITE
  because episodes recur ([notifications.go:1-30](../../packages/cafe-ledger/notifications.go)); `post_entry` on
  a LEGACY account with no `.balance` "marks what already exists STALE and mints nothing where nothing exists"
  ([scripts.go:1681-1691](../../packages/cafe-ledger/scripts.go)); `cafeLeaseAccounts` projects the three arrears
  columns for the statement + grid ([lenses.go:137-151](../../packages/cafe-ledger/lenses.go)); cafe-domain's
  `require_no_credit_hold` walks `heldFor` in-links to the account, `(e)`-reads `.arrears`, checks the class, and
  refuses `CreditHold` on `sentAt` present — on every leg, "because the debt is the lease's, not the caller's"
  ([cafe-domain/ddls.go:970-999](../../packages/cafe-domain/ddls.go)); the POS gates with
  `openTabGate(balance)` = hold → panel replaces the button / overdue → confirm / open
  ([cafe-app app.js:813-817, 1055-1060](../../cmd/cafe-app/web/app.js)).
- **Wellness-ledger already replays its own `postedTo` history.** The self-credit cap recomputes the balance from
  the account's transactions, paged (`SELF_CREDIT_PAGE_LIMIT`, [scripts.go:334-345](../../packages/wellness-ledger/scripts.go))
  — the `arrears_entries` walk is the same enumeration with the same `(e)` posture. `heldFor` is 0..1
  (account → identity, [scripts.go:102-112](../../packages/wellness-ledger/scripts.go)), so both walks are bounded.
- **The guarded value is "a new claim on a session by a held member", and it has two writers.** `CreateBooking` and
  `JoinWaitlist` share `prepare_booking_common` ([wellness-domain/ddls.go:4624](../../packages/wellness-domain/ddls.go));
  a waitlisted claim is seated by `PromoteWaitlistedBookings` / CancelBooking's in-batch promotion with no further
  gate, so a hold on `CreateBooking` alone is a side door through the waitlist (the `_packages.md` dossier's *the
  "leg" is every writer of the guarded VALUE*). Promotion of a claim that predates the hold is NOT a leg: it seats
  a claim the member already held, the same way an open café tab keeps charging under a hold.
- **Notification transport exists for wellness.** `wellness-reminders` fires `external.notification` off its
  outbox to the bridge's `notification` adapter and records the outcome with `RecordBookingReminderNotification`
  ([notifications.go:11-35](../../packages/wellness-reminders/notifications.go)) — create-only, which is right for a
  booking reminded about once and wrong for a recurring episode (café's doc comment records exactly this).
- **Facet:** `CreateBooking` / `JoinWaitlist` descriptors ([opmetas.go:170, 255](../../packages/wellness-domain/opmetas.go))
  are self-anchored on the session row; a hold is a property of the ACTOR (like `ProtectedBooker`), which no
  session lens column expresses — `lint-refusal-courtesy` gains a `CreditHold` pair per site.
- **No `scripts/verify-package-wellness-ledger.go`** exists (no `ddlCheck` pin to extend); the wellness-domain one
  gains no command.

## Verdict — *the wellness ledger records its episodes the way the café ledger does, and a reminded debtor claims no new seat*

1. **`.arrears` on `vtx.wellnessaccount`** (class `wellnessAccountArrears`) = `{evaluatedAt, dueAt?, remindedFor?,
   sentAt?, stale?, historyTooLong?}` — café's aspect, café's lifecycle (recorded facts, no clock in the lens).
   Lifetime: minted by `EvaluateWellnessArrears`; `post_entry` (every debit/credit) carries + marks `stale` when it
   exists and mints nothing when absent — wellness-ledger stores no balance, so café's legacy branch is the ONLY
   branch here, and the never-evaluated gap (`evaluatedAt = null`) is what first evaluates every account, incl.
   the ones standing at install; the episode ends (`{evaluatedAt}` alone) when the evaluation finds no open head;
   `sentAt` is carried across every write of a live episode and dropped only there. Never tombstoned (an account
   is never tombstoned).
2. **`EvaluateWellnessArrears{accountKey}`** — café's op: paged `postedTo` replay (`ARREARS_PAGE_LIMIT` 50 ×
   `ARREARS_MAX_PAGES` 10, `historyTooLong` degrade recorded, never a refusal), `reverses` netting, FIFO head, a
   **15-day term** (`ARREARS_GRACE_DURATION = "360h"` — the app's `statementGraceDays` is 15; one rule in two
   languages, pinned equal by a Go test), `remindedFor` stamped on every due evaluation, `sentAt` once per EPISODE,
   `external.notification` keyed `accountKey:dueAt` with `{identityKey, dueAt, balanceCents}` where the identity is
   resolved by the op from the `heldFor` out-walk (never a Params hop — the dossier's optional-hop refusal), a
   forged `sentAt`/`remindedFor` in the payload refused. Operator-granted (Weaver's service actor), never
   frontOfHouse/consumer.
3. **`wellnessArrearsReminders`** weaver-target lens + playbook: café's cypher verbatim on `wellnessaccount` (no
   `heldFor` hop needed — the identity is the op's to resolve), `missing_evaluation → directOp(EvaluateWellnessArrears)`
   with `Params {accountKey: row.entityKey}` and `OptionalReads [row.entityKey.arrears]`.
   **`RecordWellnessArrearsReminderNotification`** → `.arrearsNotification` (class `wellnessAccountArrearsNotification`)
   as an idempotent overwrite, café's reasoning verbatim: episodes recur.
4. **`wellnessMemberAccounts` projects `arrearsDueAt`, `arrearsRemindedFor`, `arrearsReminderSentAt`**; `/api/ledger`
   and `/api/frontdesk-arrears` carry `dueDate` / `isOverdue` / `daysOverdue` / `reminderSentAt` with the RECORDED
   `dueAt` winning when the row has one and `deriveStatement`'s derivation only in its absence (a stamp that names
   a date reads the recorded one when it exists).
5. **`CreditHold` in `prepare_booking_common`** — binds `CreateBooking` AND `JoinWaitlist`, both legs (consumer
   self and frontOfHouse), placed after the workplace confinement (a staffer at another building learns nothing
   about a member's debt) and before the schedule read: `heldFor` in-walk from the booker (`LIVE_LINK_PAGE_LIMIT`,
   first live, 0..1), `(e)` follow-up read of `.arrears`, class refused if not `wellnessAccountArrears`, `sentAt`
   present ⇒ `CreditHold: this member owes a balance a reminder went out for on <date>; it must be paid or written
   off before a new class is booked`. `dueAt` alone (overdue, not yet reminded) is not a hold — that is the
   desk's existing confirm.
6. **The FE says so.** Member statement: `Balance owed: $X · due <date>` / `· N days overdue` / `· a reminder was
   sent <date> — booking is on hold until it is paid` (one goja-pinned `statementLine`); the schedule card's
   `bookingGate(ledger)` = `hold` replaces Book / Join waitlist with an on-hold note, `confirm` keeps today's
   overdue confirm, `open` otherwise; the desk's member picker + guest search badge `· credit hold` and the
   Book button becomes the hold note (`hide`); `refusal-courtesy: CreateBooking|JoinWaitlist/CreditHold: hide` at
   both app sites, `none — property of the actor` at both Facet descriptors.
7. **The descriptors + DDL text state the rule**; wellness-ledger 0.2.24 → 0.2.25, wellness-domain 0.27.11 → 0.27.12.

### Fire brief (build note, 2026-09-15)

1. **Scope** (board row, verbatim): *The member statement says "Balance owed" with no due date or overdue flag
   though `deriveStatement` computes both; no arrears reminder op/notification exists (café precedent); a 3-day-overdue
   member self-books with no word. Mirror café: reminder recorded + shown, `CreateBooking` refuses `CreditHold` off
   it.* Green bar: an account whose FIFO head is 15+ days old is evaluated by Weaver, stamped `sentAt`, notified once
   per episode; that member's `CreateBooking` and `JoinWaitlist` are refused `CreditHold` on both legs; their
   statement and the desk grid say due / overdue / reminded; a payment to zero ends the episode and lifts the hold.
2. **Touch-list (verified live):** wellness-ledger `scripts.go:212` (`post_entry`) + `:334-345` (the `postedTo` walk
   to mirror) + `:428` (`execute`) · `ddls.go` (aspect DDL + op DDL) · new `notifications.go` · `lenses.go:82-88`
   (`wellnessMemberAccounts`) + `:440-459` (spec) + new target lens · `targets.go` · `permissions.go` · `opmetas.go`
   · `package.go:103` + `manifest.yaml:2` · tests `ledger_test.go`, `lens_cypher_test.go`, `member_accounts_lens_test.go`.
   wellness-domain `ddls.go:4624-4680` (`prepare_booking_common`; the hold goes after `require_workplace`) + the
   CreateBooking/JoinWaitlist DDL descriptions · `opmetas.go:170-254`, `:255+` (courtesy lines) · `package.go:153` +
   `manifest.yaml:2` · tests `integration_test.go` (both ops × both legs, plus the dueAt-only positive vector).
   wellness-app `ledger.go:146-152` (`balanceRow`), `:210-306` (`deriveStatement`), `:369-450` (`handleLedger`),
   `:479-544` (`handleFrontDeskArrears`) · `app.js:488-493`, `:838-851`, `:940`, `:1148-1215`, `:1548-1560`,
   `:1755-1770`, `:2008-2012`, `:2046-2052`, `:2069-2078`, `:3022-3050` · `web_arrears_test.go`, `ledger_test.go`.
3. **Precedents:** cafe-ledger `scripts.go:152-330` (`arrears_entries`/`arrears_head`/`carry_arrears`), `:377-618`
   (the evaluate block), `:1660-1720` (post_entry's legacy branch), `lenses.go:137-252`, `targets.go:60-75`,
   `notifications.go` whole, tests `TestArrears_*` (`ledger_test.go:2156-3030`), `TestCafeArrears_*`
   (`lens_cypher_test.go:407-700`); cafe-domain `ddls.go:950-999` + `TestOpenTab_RefusesCreditHold{,_SelfLeg}`;
   cafe-app `openTabGate`/`arrearsBadge`/`renderCreditHoldPanel` + `web_hold_test.go`; wellness-app's own
   `promotedBadge` / `web_status_test.go` for the goja pin shape.
4. **Increments:** (1) wellness-ledger — aspect + op + post_entry carry/stale + notification replyOp + target lens
   + playbook + member-accounts columns + permissions + opmeta + version; tests mirror café's set (episode open by
   evaluation, second charge leaves the head, partial payment → stale, payment to zero ends the episode, one send per
   episode, refund netting, forged send refused, history past the budget degrades, undeclared submitter, notification
   forged ref refused; lens pins never-evaluated / pending / due / sent / stale / cleared / new episode after old
   lapse / sibling-target lapse / historyTooLong / member-accounts columns). Green: `go test ./packages/wellness-ledger/
   -count=1`, `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`. (2) wellness-domain — the hold in
   `prepare_booking_common`, DDL + descriptor text, Facet courtesy lines, version; tests: both ops × both legs
   refused, dueAt-only accepted, wrong class refused, no account accepted. Green: `go test ./packages/wellness-domain/
   -count=1`. (3) wellness-app — `balanceRow.ReminderSentAt`, recorded-wins in both handlers, `/api/ledger`'s four
   fields, `statementLine` + `bookingGate` + badges + hold notes + courtesy lines; goja pins for the line (each
   suffix), the gate (hold/confirm/open), the badge, the grace-equality Go pin. Green: `go test ./cmd/wellness-app/
   -count=1`, every `scripts/lint-*.go` (STRICT=1 where honoured), `golangci-lint run ./...`, `make vet`. (4) live:
   `make refresh-wellness` (both packages), cycle `bin/wellness-app`; every standing account evaluates once; Alex Kim
   ($20, overdue) → `sentAt` + notification; Alex's `CreateBooking` refused `CreditHold`; pay to zero → hold lifts.
5. **Gotchas:** two package edits ⇒ two version bumps + `Version` constants; a lens RETURN edit is a corpus edit
   (`internal/refractor` census pins — `wellnessMemberAccounts` and the new target); `lint-links-page-limit` on both
   new walks; `lint-live-read-pinned-mutation` — the `.arrears` upsert is OCC on the declared read's revision, and the
   hold's `(e)` read mutates nothing; `lint-seed-declared-reads` — `grep -rn "CreateBooking\|JoinWaitlist" scripts/`
   for any seed envelope (the hold is walk-resolved, so no declaration changes, but a `mustAccepted` seed booking a
   member with a held account would now be refused); `lint-refusal-courtesy` — `CreditHold` at the schedule block
   (`app.js:838`, both ops), the desk block (`:2046`), the guest site, both Facet descriptors; the Weaver playbook's
   `Params` names only `row.entityKey` (dossier: an optional-hop Params column is a dispatch refusal); the
   `_packages.md` dossier's *recorded value read as the fact it records* (fourth sighting: `.arrears.sentAt` is the
   SEND INTENT — say "a reminder was sent" only where the outbox event was committed, and name `.arrearsNotification`
   as the delivery fact), *a mirrored back-reference keeps liveness and drops ownership* (the hold resolves the account
   from the BOOKER's own `heldFor`, never a payload key), *a confinement guard tested only as the operator has never
   run* (one frontOfHouse vector per op for the hold); the vertical-apps dossier's *two courtesy surfaces name the
   same instant* (the hold's `<date>` is `sentAt[:10]` in the script — render the FE's from the same UTC slice, and
   say so) and *a new terminal state is a census of every render gate* (schedule card, desk picker, guest search,
   roster book form, the statement, the pay form — the pay form stays offered under a hold, paying is how it lifts).
   Standing checklist #1 (the lifetime table above) · #3 (each refusal conjunct and each lens conjunct revert-proven;
   the threaded `reminderSentAt` asserted equal to its source at the producer) · #5 (`.arrears` has one writer per
   verb — evaluation mints/rewrites, post_entry only carries+stales; `.arrearsNotification` one writer) · #6 (café's
   `post_entry` `.balance` branches are NOT mirrored — wellness has no balance by design).
6. **Adjacent finds:** none surfaced by the scouts.
7. **Non-goals:** a stored `.balance` on the wellness account; holding promotion of a pre-hold waitlist claim; a
   Facet lens column for the hold; the no-show fee row (this batch's second unit, its own doc); the desk's reminder
   re-send verb; the café mechanism itself.
