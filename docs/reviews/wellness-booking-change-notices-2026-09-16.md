# Wellness — "A member seated from the waitlist, moved, or called off hears nothing" (2026-09-16)

**Filed (PO, 2026-09-16):** only the 24 h reminder and the arrears reminder ever go out; a promotion (`promotedAt`),
a time move, and a call-off change the member's seat, charge, or refund in silence. Live: Alex was seated + charged
$10 at 20:41Z for a Sep 20 class, next word due Sep 19 15:00Z. Winston-adjudicated (implementation-level; no
contract surface, no fork).

## Grounding

- **The reminder is the only notification the seat has, and it is a timer.** `wellnessBookingReminders`
  ([lenses.go:106-170](../../packages/wellness-reminders/lenses.go)) arms `freshUntil = remindAt`, the fired lapse
  opens `missing_reminder`, `RecordBookingReminder` writes `.reminder = {sentAt, remindedFor}` and emits
  `external.notification` off its own outbox ([ddls.go:290-294](../../packages/wellness-reminders/ddls.go)) to the
  bridge's `notification` adapter; `RecordBookingReminderNotification` records the outcome
  ([notifications.go](../../packages/wellness-reminders/notifications.go)). Nothing else on a booking ever notifies.
- **The three changes are recorded facts, but two of them are level, not time.** A promotion writes
  `.status.promotedAt` at both promotion sites ([ddls.go:5294-5300](../../packages/wellness-domain/ddls.go) and the
  `PromoteWaitlistedBookings` upsert) and never changes again. A time move rewrites the session's
  `.schedule.startsAt` ([ddls.go:4103-4123](../../packages/wellness-domain/ddls.go)) and records nothing about the
  move — but the booking recorded the time it was claimed for: `.status.classStartsAt`, snapshotted by
  CreateBooking/JoinWaitlist and carried by every later writer. So "moved since the member was told" is
  `startsAt <> (the last time we told them, else classStartsAt)` — a pure function of stored data, no clock, no
  new stamp on the session. A call-off is `TombstoneSession`, after which `wellnessOrphanedBookingSettlement`
  dispatches `ReleaseOrphanedBooking`, which **tombstones the booking** ([ddls.go:5662-5730](../../packages/wellness-domain/ddls.go)).
- **The call-off leg cannot be a lens gap.** A gap anchored on the booking opens on the same CDC event that opens
  `missing_release`; the two directOps race, and once the release lands the row is deleted and a not-yet-dispatched
  notice is gone. Ordering the release behind the notice would make the domain's drain wait on an upper package's
  marker (an inversion, and a class that never drains when `wellness-reminders` is absent). The only place the
  call-off notice is guaranteed is the release's own batch — the transactional outbox, the event recorded where it
  happens.
- **The engine's null test is `<> null`** (two-valued: `null <> 'x'` is true, `null <> null` false —
  [values.go:121](../../internal/refractor/ruleengine/full/values.go); `IS NOT NULL` mis-evaluates,
  [edge-manifest/lenses.go:578](../../packages/edge-manifest/lenses.go)); `coalesce` is supported
  ([cafe-domain/lenses.go:302](../../packages/cafe-domain/lenses.go)).
- **Live census (Core KV, 2026-09-16):** 76 live `.status` aspects; 16 `booked` carry no `classStartsAt` (claimed
  before the snapshot existed), 3 `booked` carry `promotedAt`. The processor has no "parent vertex alive" constraint
  on an aspect write (`step6_validate.go` constraint set), so an audit aspect can land on a tombstoned booking.
- **Facet + FE:** `wellnessBookings` ([lenses.go:507-528](../../packages/wellness-domain/lenses.go)) projects
  `reminderSentAt` and `promotedAt`; `bookingRow` carries both ([bookings.go:55-77](../../cmd/wellness-app/bookings.go));
  `reminderBadge` / `promotedBadge` render them ([app.js:1446-1463](../../cmd/wellness-app/web/app.js)).

## Verdict — *every change to a seat is told once, keyed on the change itself*

1. **Two level-triggered gaps in `wellness-reminders`.** A new weaver-target lens `wellnessBookingChangeNotices`
   (anchor booking, no `freshUntil` — the gap is a state, not a deadline) with
   - `missing_promotion_notice` = `promotedAt <> null AND changeNotice.promotedFor <> promotedAt AND status = 'booked'
     AND NOT class-over`;
   - `missing_move_notice` = `classStartsAt <> null AND startsAt <> coalesce(changeNotice.movedFor, classStartsAt)
     AND status = 'booked' AND NOT class-over`;
   `class-over` is the sibling `pastDueBookings` fired marker, exactly as the reminder lens reads it. A booked seat
   claimed before the snapshot existed (`classStartsAt` absent) gets no move notice rather than a false one — the
   16 live legacy seats stay quiet; the 3 live promoted seats are told once on install (a true fact, late).
2. **One marker, one op.** `RecordBookingChangeNotice{bookingKey, sessionKey, kind: promoted|moved, changeRef}`
   (Weaver-actor only, mirroring `RecordBookingReminder`) re-checks the change against the live aspect (`promotedAt
   = changeRef` / `schedule.startsAt = changeRef`, else refuses `StaleChange` — a stale row is refused, not trusted),
   OCC-upserts `.changeNotice = {promotedFor?, movedFor?, sentAt}` (class `bookingChangeNotice`; the other kind's
   field carried, so two concurrent kinds converge), and emits `external.notification` with
   `instanceKey = idempotencyKey = externalRef = <bookingKey>:<kind>:<changeRef>` — a second move mints a new key
   and sends again, a redelivery dedups at the adapter. Params carry `bookingKey, changeType, changeRef, sessionKey,
   startsAt, className`.
3. **The call-off notice is emitted by `ReleaseOrphanedBooking` itself** (wellness-domain), in the batch that
   tombstones the booking: `instanceKey = <bookingKey>:calledOff:<sessionKey>`, params `bookingKey, changeType:
   calledOff, sessionKey, status (the value drained), className, classStartsAt`. Booked, waitlisted and noShow are all
   told — each loses something the release reverses or frees.
4. **One audit replyOp, owned by the domain.** `RecordBookingChangeNotification{externalRef, status, result?}` in
   wellness-domain (reads nothing; splits `externalRef` on `:` into booking key, kind, changeRef) bare-upserts
   `.changeNotification = {kind, changeRef, status, sentAt}` (class `bookingChangeNotification`, latest outcome
   wins, audit only, never gates a lens). It lands on a live booking for a promotion/move and on the tombstoned
   booking for a call-off — both are a record of a send, not a fact a reader converges on. The lower package owns the
   replyOp so both emitters (domain's release, reminders' notice op) name an op that is always installed beneath them.
5. **The seat says what it was told.** `wellnessBookings` projects `changeNoticeSentAt`, `movedFor`; `/api/bookings`
   carries them; My Classes and the roster badge *Told of the move · <time>* beside the seating badge. The
   promotion notice needs no new badge — the seating badge already says the seat was handed over; the notice is the
   member hearing it.

### Fire brief (build note, 2026-09-16)

1. **Scope** (board row, verbatim): *Only the 24 h reminder and the arrears reminder ever go out; a promotion
   (`promotedAt`), a time move, and a call-off change the member's seat, charge, or refund in silence. … a
   booking-change lens + notification target beside `wellnessBookingReminders`.* Green bar: a promoted seat, a moved
   class, and a called-off class each produce exactly one `external.notification` per booking per change, recorded
   on the booking, and a second move sends again.
2. **Touch-list (verified live):** `packages/wellness-reminders/`: `lenses.go:19-37` (+ new spec) · `targets.go:39-55`
   (+ new target) · `ddls.go` (new `bookingChangeNoticeOp` vertexType DDL + `bookingChangeNotice` aspectType DDL, the
   script; mirror `RecordBookingReminder` `:1-300`) · `permissions.go:13-29` · `package.go:60` + `manifest.yaml:2`
   (0.3.7 → 0.4.0) · `lens_cypher_test.go` (fixture pins) · `reminder_op_test.go:102-193` (harness: `wrSeedBooking`,
   `wrSeedSession`, `testutil.SubmitAndAwaitReply`). `packages/wellness-domain/`: `ddls.go:5662-5730`
   (`ReleaseOrphanedBooking` emission) · new `bookingChangeNotificationOp` vertexType + `bookingChangeNotification`
   aspectType DDLs (mirror `wellness-reminders/notifications.go`) · `permissions.go` (operator grant for the replyOp)
   · `lenses.go:507-528` (`wellnessBookings` + two columns) · `package.go:153` + `manifest.yaml` (0.27.13 → 0.28.0) ·
   `opmetas.go` (a descriptor for the replyOp, mirroring wellness-reminders' `notificationOpMetas`) ·
   `scripts/verify-package-wellness-domain.go:205-226` (ddlCheck rows for the two new DDLs; wellness-reminders has no
   verify script).
   `cmd/wellness-app/`: `bookings.go:18-77` (+2 fields) · `web/app.js:1446-1506` (badge + card) · `:3087-3110`
   (roster) · a goja pin beside `web_status_test.go`.
3. **Precedents:** the lens → `wellnessBookingRemindersSpec` (the `<>` gate idiom, the `pastDueBookings` class-over
   conjunct, the BodyColumns seam); the op → `recordReminderScript` (Weaver-actor guard, declared reads, the
   `external.notification` event shape); the marker upsert → `make_aspect_upsert_occ` (wellness-domain
   `ddls.go:5294`) — `lint-live-read-pinned-mutation` binds: the op reads `.changeNotice` and writes it, so the write
   is OCC on the read revision, create when absent; the replyOp → `recordReminderNotificationScript` verbatim, upsert
   instead of create; the target → `WeaverTargets()` in wellness-reminders (Params literals as in `pastdue.go`,
   `OptionalReads` as in wellness-domain `targets.go:82`); the FE badge → `promotedBadge`.
4. **Increments:** (1) wellness-domain: the replyOp + its two DDLs + grant; the release emission; `wellnessBookings`
   columns; version bump; tests: the replyOp on a live booking and on a tombstoned one (both commit; the second is
   the call-off case), `ReleaseOrphanedBooking` emits one `external.notification` per drained booking with the stated
   key (assert on the reply's events), `derive_reads` untouched. Green: `go test ./packages/wellness-domain/ -count=1`,
   `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`. (2) wellness-reminders: the lens (cypher pins:
   promoted-untold → gap; promoted-told → closed; moved-untold → gap; moved-told → closed; second move → reopens;
   legacy no-`classStartsAt` → closed; waitlisted → closed; class over → closed), the op (promoted + moved paths,
   `StaleChange` refused, the OCC write carries the other kind's field, the event key), the target, version bump.
   Green: `go test ./packages/wellness-reminders/ -count=1`, the refractor census. (3) app: two fields, the badge,
   goja pin. Green: `go test ./cmd/wellness-app/ -count=1`, `go build ./...`, `make vet`, `golangci-lint run ./...`,
   `for f in scripts/lint-*.go; do STRICT=1 go run "$f" || echo "FAIL $f"; done`, `gofmt -l scripts/`. (4) live:
   `make refresh-wellness` from the main checkout, `make verify-package-wellness-domain`, cycle `bin/wellness-app`,
   drive the PO's sequence (promote via a capacity raise; move a class; call one off) and read the bridge log +
   `.changeNotification`.
5. **Gotchas:** package edits ⇒ version bumps + `Version` mirrors (`lint-package-version`); a lens RETURN edit is a
   corpus edit (refractor census pins); `verify-package-<pkg>.go` `ddlCheck` rows for every new DDL (the
   `command count=N want N-1` red); `lint-derive-reads-bare-vector` only if a script gains `derive_reads` (none
   here); `lint-refusal-courtesy`: no client dispatches the new ops (Weaver / bridge only) — no pairs; the reply
   event class names go through the abstract-event gate (`abstractEventClass`) — mirror the existing
   `wellness.bookingReminderNotificationRecorded` shape; the `_packages.md` dossier entries *every writer of the
   guarded VALUE* (the guarded values: `promotedAt`, `startsAt`, `classStartsAt` — enumerate their writers; the
   forfeit upsert drops nothing the gate reads), *a link two ops walk*, *a gap without a budget* (declare none —
   the default 3 is right for a notice, and a closed column retires the latch); the vertical-apps dossier's *a new
   fact on a row is a census of every render gate* (`myClassCard`, `rosterCard`; counts untouched). Standing
   checklist #1 (lifetime of `.changeNotice`: created at first notice, carried per kind, dies with the booking, never
   reset; `.changeNotification`: latest outcome, may outlive its root) · #3 (each lens conjunct and the `StaleChange`
   guard revert-proven) · #5 (one writer per marker: `.changeNotice` ← the notice op only; `.changeNotification` ←
   the replyOp only).
6. **Adjacent finds:** none from the scout beyond the three sibling ★ rows already on the board (walk-in,
   CapacityBelowSeated, price snapshot) — this run's next batch units, not residuals. A walk-in seated after
   `startsAt` will spend the reminder gap's budget on `ClassAlreadyStarted` refusals until the class ends (a standing
   warning, retired at `endsAt`) — the walk-in unit closes the reminder gate for a seat claimed after start.
7. **Non-goals:** a real vendor adapter (FakeNotification is the sink); notifying a waitlisted member of a move (no
   seat, no charge); a session-level `movedAt`; the reminder mechanism itself; the sibling rows above.
