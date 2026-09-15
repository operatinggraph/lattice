# Wellness — "A waitlister seated inside the late-cancel window is charged and cannot back out" (2026-09-15)

**Filed (PO, 2026-09-15):** `CancelBooking` promotes in-batch and the `wellnessWaitlistPromotion` gap runs until
`startsAt`, so a seat handed 63 min out is charged under the 2 h forfeit; nothing records the promotion, so My
Classes flips Waitlisted → Booked silently. Live: two forfeits, $20, a 1-seat class ran empty. The PO offered two
shapes — *stop promotion at the window, or restart the clock at seating* — and asked that the seating be badged.
Winston-adjudicated (implementation-level; no contract surface, no fork).

## Grounding

- **The window is a rule about NOTICE, measured from one clock.** `is_late_cancel = submitted >= startsAt − 2h`
  ([ddls.go:4800-4801](../../packages/wellness-domain/ddls.go)), applied whoever submits; inside it a priced seat is
  kept live as `forfeited` and no refund marker mints ([ddls.go:4865-4871, 4961-4963](../../packages/wellness-domain/ddls.go)).
  The rationale the script records is *"a member intending to skip would always cancel rather than no-show"* — a
  member who held the seat through the window and gave no notice. A member seated 63 min out never had the two hours
  to give.
- **Both promotion paths write the same upsert and record no time.** CancelBooking's promotion upsert
  ([ddls.go:4877-4879](../../packages/wellness-domain/ddls.go)) and `PromoteWaitlistedBookings`'
  ([ddls.go:5583-5585](../../packages/wellness-domain/ddls.go)) write `{value: booked, rate, seat, booker, session,
  className, classStartsAt}`; `wellness.waitlistPromoted` is emitted and consumed by nothing. `bookingStatus`'s schema
  ([ddls.go:1227-1228](../../packages/wellness-domain/ddls.go)) carries no `promotedAt`; the `wellnessBookings` lens
  ([lenses.go:495-515](../../packages/wellness-domain/lenses.go)) projects `status / rate / waitlistSlot` and nothing
  about how the seat was obtained; the FE's `isLateCancel` ([app.js:1116](../../cmd/wellness-app/web/app.js)) and its
  cancel confirm ([app.js:1295](../../cmd/wellness-app/web/app.js)) read the same one clock.
- **Every later `.status` writer carries a fixed field list forward** — SetBookingAttendance's `merged`
  ([ddls.go:5123-5127](../../packages/wellness-domain/ddls.go)), CancelBooking's `forfeited`
  ([ddls.go:4866-4870](../../packages/wellness-domain/ddls.go)) — so a new fact on the aspect needs a lifetime decision
  at each (standing checklist #1).
- **The other shape — stop promotion at the window — runs the class empty on purpose.** The freed seat cell would
  be tombstoned back open, but the waitlister's own re-book is refused on the guard they already hold (`package.go`,
  PromoteWaitlistedBookings' rationale), so the person who wanted the seat is the one who cannot take it. It also
  needs the `wellnessWaitlistPromotion` gap's `freshUntil` moved off `startsAt` (a second clock on the session) and
  leaves the PO's observed harm — the 1-seat class ran empty — in place. Rejected.
- **Facet:** `edgeEntityBookings` ([edge-manifest/lenses.go:995-1014](../../packages/edge-manifest/lenses.go)) projects
  no status column and excludes `forfeited` by its WHERE; the CancelBooking descriptor's description states the
  forfeit rule ([opmetas.go:312](../../packages/wellness-domain/opmetas.go)). No new refusal code, so
  `lint-refusal-courtesy` gains no pair.

## Verdict — *the seat is a fact with a time, and the window is measured against it*

1. **The promotion is recorded.** Both promotion upserts write `promotedAt = rfc3339_utc(op.submittedAt)` on
   `.status`. It is carried forward by every later writer (attendance mark, forfeit upsert) — a member's record says
   how they got the seat for as long as the booking lives — and dies with the tombstone. Never reset: a promoted
   booking never returns to `waitlisted`.
2. **A seat handed inside the window is cancellable free until the class starts.** `is_late_cancel` becomes
   `submitted >= cutoff AND NOT (promotedAt != None AND promotedAt >= cutoff)`. A promotion that landed *before* the
   cutoff is an ordinary seat (the member had the window to cancel free); a direct `CreateBooking` inside the window
   is unchanged (the member chose the seat with the window disclosed). The exemption turns on the recorded stamp, not
   on who submits — the same posture as the window itself. `SessionStarted` still binds.
3. **The seating is badged.** `wellnessBookings` projects `promotedAt`; `/api/bookings` carries it; My Classes and the
   desk roster badge *Seated from the waitlist · <time>*; when the seating landed inside the window the member's card
   says cancelling is free until the class starts, and `isLateCancel` returns false for it (no forfeit confirm). The
   predicate is one goja-pinned function (`promotedInsideWindow`) beside `isLateCancel`, the two-languages rule of
   the vertical-apps dossier.
4. **The descriptor says so.** CancelBooking's description + the vertex-type / aspect DDL text state the exemption.

### Fire brief (build note, 2026-09-15)

1. **Scope** (board row, verbatim): *`CancelBooking` promotes in-batch and the promotion gap runs until `startsAt`, so
   a seat handed 63 min out is charged under the 2 h forfeit; no `promotedAt` recorded, the flip is silent. Stop
   promotion at the window (or restart the clock at seating); badge the seating.* Green bar: a booking promoted inside
   the window and cancelled inside it is tombstoned with a refund marker; promoted before the cutoff it forfeits as
   today; the row shows when it was seated.
2. **Touch-list (verified live):** `ddls.go:1227-1228` schema + `:1230-1240` field docs + `:1244` example ·
   `:1194-1197`, `:1509-1514`, `:912-924`, `:1040` rule text · `:4800-4801` the predicate · `:4866-4870` forfeit
   carry-forward · `:4877-4879` promotion upsert · `:5123-5127` attendance carry-forward · `:5583-5585` Weaver
   promotion upsert · `lenses.go:495-515` · `opmetas.go:312` · `package.go:149` + `manifest.yaml:2` (0.27.9 → 0.27.10) ·
   `bookings.go:17-69` · `app.js:1107-1120`, `:1295`, `:1355-1380`, `:2860-2930` · `web_status_test.go` ·
   `refund_marker_test.go:1082` (`PromotesAndForfeits`) · `promote_waitlist_test.go:66` (`requirePromoted`) ·
   `lens_cypher_test.go:415` (the reminderSentAt projection pin).
3. **Precedents:** `reminderSentAt` — an optional aspect field projected as a nullable column and rendered as a badge
   (`lenses.go:515`, `bookings.go:44`, `app.js:1347 reminderBadge`); `isLateCancel` + `web_status_test.go` for the goja
   pin; `TestCancelBooking_LateWindowByMinute` for the boundary vectors; `promo_submitted` (`ddls.go:5536`) for the
   stamp's form.
4. **Increments:** (1) package: `promotedAt` written at both sites, carried at both later writers, the exemption in
   `is_late_cancel`, schema/docs/descriptor text, version bump; tests: `requirePromoted` asserts the stamp equals the
   envelope's `submittedAt`, `PromotesAndForfeits` asserts it on the promoted booking, new
   `TestCancelBooking_PromotedInsideWindowRefunds` (charged, promoted inside → tombstoned + marker) and
   `_PromotedBeforeWindowForfeits`, each proven by reverting the conjunct. Green:
   `go test ./packages/wellness-domain/ -count=1`, `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`.
   (2) lens + app: `wellnessBookings.promotedAt`, `bookings.go` field, `promotedInsideWindow(b)` + `isLateCancel`
   exemption + badge on My Classes and the roster; goja pins for the predicate and the badge. Green:
   `go test ./cmd/wellness-app/ -count=1`, `lint-web`, `lint-markup-escaping`, `lint-stale-render-guard`,
   `lint-app-op-descriptors`, `lint-refusal-courtesy`, `lint-package-version`, `lint-conventions`, the full
   `scripts/lint-*.go` set. (3) live: `make refresh-wellness`, cycle `bin/wellness-app`, drive the PO's own sequence
   (1-seat class, book + waitlist, cancel inside the window → promoted; the promoted booking cancels → refund).
5. **Gotchas:** package edit ⇒ version bump + `Version` constant; a lens RETURN edit is a corpus edit
   (`internal/refractor` census pins); `lint-live-read-pinned-mutation` — both upserts stay OCC on the read revision;
   the `_packages.md` dossier's *a link two ops walk* and *every writer of the guarded VALUE* entries (the guarded
   value here is `is_late_cancel`; its legs are CancelBooking alone — ReleaseOrphanedBooking deliberately ignores the
   window); the vertical-apps dossier's *two courtesy surfaces name the same instant* (render `promotedAt` as a local
   instant, it is one) and *a new fact on a row is a census of every render gate* (`isLateCancel`, the cancel confirm,
   the My Classes card, `rosterCard`, `bookedCount`/`forfeitedCount` — the counts are untouched, a promoted booking is
   `booked`). Standing checklist #1 (lifetime: written at promotion, carried by attendance + forfeit, dies with the
   tombstone, never reset) · #3 (each conjunct revert-proven).
6. **Adjacent finds:** none surfaced by the scout. (`wellness.waitlistPromoted` is consumed by nothing — an event, not
   a gap; no row.)
7. **Non-goals:** the promotion gap's `freshUntil`; a promotion notification (the reminder already fires on promotion
   when the class is < 24 h out); the desk's booking-time window warning; the three sibling Wellness rows.
