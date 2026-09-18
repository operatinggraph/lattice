# Wellness — "A moved class strands its members' hour" + "A class handed to a sub, un-led, or moved to another room tells nobody" (2026-09-18)

**Filed (PO, 2026-09-18), two rows on one mechanism.** (A) `ReassignSession` migrates the studio's and the
instructor's slot cells on a time move and leaves the bookers': the old hour stays `BookerConflict` for every seated
member with no booking to show for it, the new hour is unguarded, and cancel/release tombstone cells on the current
span only. Live: Riley Chen refused on Sep 19 18:00 (Notice Probe, moved 18→19, called off). (B) `missing_move_notice`
keys on `startsAt` alone; `/api/bookings` carries no instructor, so My Classes cannot even show a sub. Live: PO Probe
Swap lost Sam Okafor, Riley's row read `missing_move_notice: false`. Winston-adjudicated (implementation-level; no
contract surface, no fork).

## Grounding

- **Three hubs hold cells for one class; the op migrates two.** A session's span claims a `studioSlotClaim` per
  15-minute cell on the studio hub, an `instructorSlotClaim` on the instructor hub, and — at `CreateBooking` /
  `JoinWaitlist` — a `bookerSlotClaim` on each booker's identity hub (`prepare_booking_common`,
  [ddls.go:5709-5711](../../packages/wellness-domain/ddls.go)); all three are `{}` existence markers keyed on
  `<hub>.slot<cellcode>`. `ReassignSession` releases/claims the studio's and the instructor's by symmetric difference
  ([ddls.go:4696-4730](../../packages/wellness-domain/ddls.go)) and never enumerates the bookers.
  `ReassignSessionSeries` has the identical omission ([ddls.go:4387-4406](../../packages/wellness-domain/ddls.go)).
  `CancelBooking` / `ReleaseOrphanedBooking` derive the cells to release from the session's *current* schedule, so
  after a move they tombstone keys that were never claimed and leave the claimed ones live.
- **The bookings of one session are enumerable, bounded, and the package already does it.**
  `collect_waitlist_candidates` ([ddls.go:5083-5160](../../packages/wellness-domain/ddls.go)) walks the session's
  `forSession` in-links (`kv.Links(session, "forSession", "in", cursor, 50)`, class (e), up to 4 pages) and reads each
  booking's `.status` for `value` + `booker`. The booker key is already a `.status` scalar, so no second link walk is
  needed. A page walk that does not reach the end is a stated outcome (`reached_end`).
- **One batch is at most 998 business mutations** (`SERIES_MOVE_MAX_MUTATIONS = 990`,
  [ddls.go:3024-3036](../../packages/wellness-domain/ddls.go), refusing `SeriesTooLarge`). A 1 h class = 4 cells;
  migrating N bookers clear of their old cells costs 8N mutations, so ~120 bookers saturate one batch; a capacity-200
  class cannot be moved atomically, and its walk (200 `.status` reads) sits near the 250 ms script wall. **Census
  (read model, 2026-09-18): 1 live future seat on 1 session; max class size 1; 0 live seats on a moved class.**
- **Who leads and where it meets are links, and the move notice reads neither.** `wellnessBookingChangeNotices`
  ([lenses.go:259-279](../../packages/wellness-reminders/lenses.go)) opens `missing_move_notice` on
  `startsAt <> coalesce(changeNotice.movedFor, status.classStartsAt)` — level-triggered off a claim-time snapshot. A
  booking snapshots no instructor or studio; `ReassignSession` swaps `ledBy` / `atStudio` by tombstone-old + revive-new
  and records nothing about the swap. `wellnessBookings` ([lenses.go:562-585](../../packages/wellness-domain/lenses.go))
  walks `atStudio` but not `ledBy`; `bookingRow` has no instructor; `movedBadge` is the only change badge
  ([app.js:1490](../../cmd/wellness-app/web/app.js)).
- **The engine's `>` on a null operand is false; `<>` against null is true** (`compareAny`, `equalsAny`,
  [values.go:121-160](../../internal/refractor/ruleengine/full/values.go)); a walk to a tombstoned neighbour binds
  nothing. `.status.bookedAt` is stamped at claim and carried by every later writer; a legacy seat has none.

## Verdict

1. **`ReassignSession` moves every live booker with the class, in the same batch.** When the span changes, the op
   walks the session's `forSession` in-links exactly as `collect_waitlist_candidates` does (the session script gets
   its own copy — the cross-script duplication the two scripts already accept for `claim_cell`), takes every booking
   whose `.status.value` is `booked` or `waitlisted` (both hold cells), de-duplicates by booker, and for each booker
   tombstones the old cells not in the new span and `claim_cell`s the new cells not in the old — the same-hub
   reschedule delta the studio already gets. A member who holds the new hour elsewhere refuses the whole move
   `BookerConflict` naming that member's hub; the desk resolves it, nothing is half-moved. A walk that does not reach
   the end refuses `BookingWalkBound` (a partial migration is the bug in a new coat), and a move whose assembled batch
   exceeds the ceiling refuses `MoveTooLarge` — the same count `ReassignSessionSeries` keeps, now in both ops.
   `bookerSlotClaim` admits `ReassignSession` and `ReassignSessionSeries`. `ReassignSessionSeries` gets the same
   per-booker delta, grouped by booker across the run (a regular who holds three weeks moves three weeks; a shift by
   the run's own interval writes only the difference), under its existing `SeriesTooLarge` ceiling.
2. **A swap or a room change is recorded where it happens.** `ReassignSession` stamps `.schedule.instructorChangedAt`
   when `newInstructor` / `clearInstructor` changes who leads, and `.schedule.studioChangedAt` when `newStudio`
   changes where it meets — the op's own `submittedAt`, a time fact recorded on the entity by the event that made it.
   Both are carried forward by every later `.schedule` rewrite (`ReassignSession`, `ReassignSessionSeries`); a minted
   occurrence has neither. No booking snapshot changes and no dispatcher gains a read: the notice op and the lens
   already read `.schedule`.
3. **Two more level-triggered gaps on the same lens, two more kinds on the same op.**
   `missing_instructor_notice` = `instructorChangedAt <> null AND bookedAt <> null AND instructorChangedAt >= bookedAt
   AND changeNotice.instructorFor <> instructorChangedAt AND status = 'booked' AND session alive AND NOT class-over`;
   `missing_room_notice` the same over `studioChangedAt` / `roomFor`. A member who booked after the change is not
   told of it (`>=` keeps a same-second booking told — true, at worst redundant); a legacy seat with no `bookedAt`
   stays quiet; a second swap advances the stamp and reopens. `RecordBookingChangeNotice` gains kinds `instructor`
   and `room`, `changeRef` = the stamp, `StaleChange` when the live stamp differs; the marker carries all four
   `*For` fields. The lens projects `instructorName` (`ledBy` walk) and `studioName` (`atStudio` walk) so the
   notification says who leads now / where it meets; an un-led class names no instructor.
4. **The seat says who leads it and what it was told.** `wellnessBookings` adds `instructorKey`, `instructorName`,
   `instructorChangedAt`, `studioChangedAt`, `instructorFor`, `roomFor`; `/api/bookings` carries them; My Classes
   shows *Led by <name>* / *No instructor yet*, and *Told of the instructor change · <time>* / *Told of the room
   change · <time>* beside the move badge, each shown only while the told stamp equals the current one.

**Rejected:** a per-booking migration gap in `wellness-domain` (a Refractor-latency window with members unguarded,
and a `BookerConflict` that surfaces as a latched gap nobody reads instead of a refusal the desk sees) · a
claim-time `classLedBy` snapshot on the booking (a live `ledBy` read on every `CreateBooking`, and a sentinel for
"un-led at claim") · a session-level `changedAt` for the time move too (the shipped level-triggered move notice
already reads a snapshot; two idioms for one fact) · repairing Riley's leaked 18:00 cells (a released booking on a
tombstoned session — no op may write a booker cell for it, and the hour it blocks is past on 2026-09-19).

**Out (needs a designer pass):** a class of ~120+ seated members cannot move atomically — the migration exceeds one
batch and its walk approaches the script wall. `MoveTooLarge` names the bound; a paged multi-batch move of one
logical change has no precedent (`📐 no-pattern: a paged multi-batch mutation of one class's seat guards`). Live
max class size is 1; the row carries the census.

### Fire brief (build note, 2026-09-18)

1. **Scope** (both board rows, verbatim): *`ReassignSession` migrates the studio's and instructor's slot cells and
   leaves the bookers': the old hour stays `BookerConflict` for every seated member with no booking to show for it,
   the new hour is unguarded, and cancel/release tombstone cells on the current span only.* · *`missing_move_notice`
   keys on `startsAt` alone; `/api/bookings` carries no instructor, so My Classes cannot even show the change.*
   Green bar: after a time move every live booker's cells sit on the new span and only there (a booking on the old
   hour is admitted, one on the new hour is refused, a cancel releases the new cells); a member who holds the new
   hour elsewhere refuses the move; a swap, a clear and a room change each produce exactly one
   `external.notification` per booked seat, once, a second change sending again; `/api/bookings` names the instructor.
2. **Touch-list (verified live):** `packages/wellness-domain/`: `ddls.go:4696-4730` (ReassignSession cell delta; the
   booker walk + delta + `BookerConflict`/`BookingWalkBound`/`MoveTooLarge`), `:4520-4560` (the instructor / studio
   change branches — the two stamps), `:4770-4796` (`new_sched` carries), `:4340-4380` (ReassignSessionSeries
   per-occurrence loop — booker groups + carries), `:3036` (the ceiling constant, shared), a session-script copy of
   `collect_waitlist_candidates` `:5083-5160` restricted to live cell holders, `:1077` (`bookerSlotClaim`
   PermittedCommands), `:800-840` (sessionSchedule DDL schema + FieldDescription: `instructorChangedAt`,
   `studioChangedAt`), `lenses.go:562-585` (`wellnessBookings` + 6 columns), `package.go:155` + `manifest.yaml:2`
   (0.30.0 → 0.31.0), `integration_test.go:1862-1990` (the two ReassignSession tests — siblings for the booker delta).
   `packages/wellness-reminders/`: `lenses.go:259-279` (+ two walks, + 6 columns, + 2 gaps), `:57` (BodyColumns),
   `changenotice.go:143-160` (aspect schema: `instructorFor`, `roomFor`), `:197-330` (`CHANGE_KINDS`, the stamp
   check, the carry list, the params), `targets.go:82-98` (+ 2 gaps), `package.go:77` + `manifest.yaml:2`
   (0.4.1 → 0.5.0), `changenotice_cypher_test.go`, `changenotice_op_test.go`, `package_test.go:36` (structure pins).
   `cmd/wellness-app/`: `bookings.go:65-91` (+ 6 fields), `web/app.js:1490-1530` (badges + `myClassCard`), `:3140-3200`
   (`reassignSession`: `forSession` enumeration on a time move), `web_moved_badge_test.go` (goja pin siblings).
   `scripts/verify-package-wellness-domain.go:229` (`bookerSlotClaim` count 4 → 6).
3. **Precedents:** the walk → `collect_waitlist_candidates` (the `(e)` annotation, `seen`, `reached_end`); the delta →
   the same-hub studio branch `:4696-4700`; the ceiling → `SERIES_MOVE_MAX_MUTATIONS` `:4447`; the stamps →
   `remindAt`'s carry-forward in both `new_sched` builders; the gaps → `missing_move_notice` (the `<> null` session
   guard, the `pastDueBookings` class-over conjunct, `violating` repeating both); the kinds → `kind == "promoted"`'s
   `StaleChange` branch and the carry loop; the lens walk → `wellnessSessionsSpec`'s `ledBy` OPTIONAL MATCH; the FE
   badge → `movedBadge` (shown only while `movedFor === startsAt`); the FE enumeration → the `newStudio` branch's
   `enumerations`.
4. **Increments:** (1) wellness-domain — the walk + delta + refusals in `ReassignSession`, the grouped delta in
   `ReassignSessionSeries`, `bookerSlotClaim` admits both, the two stamps + carries, `wellnessBookings` columns,
   0.31.0; tests: a moved class re-homes a booked and a waitlisted member's cells (old cell claimable, new cell
   refused, cancel releases the new cells), a member holding the new hour elsewhere refuses `BookerConflict`, a
   series shift moves a regular's three weeks, a swap stamps `instructorChangedAt` and a room move `studioChangedAt`,
   a later name edit carries both. Green: `go test ./packages/wellness-domain/ -count=1`,
   `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`. (2) wellness-reminders — the two gaps + columns,
   the two kinds, the target rows, 0.5.0; cypher pins: swap-untold → gap; told → closed; booked after the swap →
   closed; legacy no-`bookedAt` → closed; second swap → reopens; room the same; op: both kinds write their field and
   carry the other three, `StaleChange` on a stale stamp. Green: `go test ./packages/wellness-reminders/ -count=1`
   + the refractor census. (3) app — fields, badges, the enumeration, goja pins. Green:
   `go test ./cmd/wellness-app/ -count=1`, `go build ./...`, `make vet`, `golangci-lint run ./...`,
   `for f in scripts/lint-*.go; do STRICT=1 go run "$f" || echo "FAIL $f"; done`, `gofmt -l scripts/`. (4) live:
   `make refresh-wellness` from the main checkout, `make verify-package-wellness-domain`, `make cycle-wellness`;
   drive: book two members, move the class, book the old hour (admitted) and the new (refused), swap the instructor,
   read the bridge log + `.changeNotice`, `/api/bookings`.
5. **Gotchas:** package edits ⇒ version bumps + `Version` mirrors; a lens RETURN edit is a corpus edit (refractor
   census pins + `BodyColumns`); `verify-package-wellness-domain.go` `ddlCheck` count for `bookerSlotClaim`;
   `lint-links-page-limit` (the new `kv.Links` call states its limit); `lint-live-read-pinned-mutation` (the
   booker cells are read then written — `claim_cell`'s existing OCC-on-dead shape); `lint-refusal-courtesy` (three
   new codes on `ReassignSession` — `BookerConflict`, `BookingWalkBound`, `MoveTooLarge` — need a courtesy line at
   the FE's dispatch site and Facet's descriptor); the `_packages.md` dossier: *the leg of a guard is every writer of
   the guarded value* (the guarded values: the booker cells — writers CreateBooking / JoinWaitlist / CancelBooking /
   ReleaseOrphanedBooking / now both Reassign ops; `instructorChangedAt` / `studioChangedAt` — one writer, two
   carriers, mint paths omit), *a dispatch declaration must name what the runtime binds* (the two new gaps read
   `.schedule` + `.status`, already declared; the FE's time-move enumeration), *a refusal that reads a key the op
   never writes is advisory* (`BookerConflict` is on the written cell — commit-time CreateOnly, not advisory), *a
   mirror that drops one of the precedent's checks drops the invariant* (`seen` de-dup, `isDeleted` skip,
   `reached_end`); the vertical-apps dossier: *a new fact on a row is a census of every render gate* (`myClassCard`,
   `rosterCard`), *a new key's census walks the `cmd/<app>` struct pair* (`bookingRow` + the lens columns, the pin
   built from the wire struct), *an instant renders locale everywhere* (the two `sentAt` badges through
   `fmtDay`/`fmtTime`). Standing checklist #1 (the two stamps: created by the change, carried by every rewrite,
   never reset, absent on a mint; the four `*For` fields: created at first notice, carried per kind, die with the
   booking) · #3 (each new conjunct and refusal revert-proven) · #5 (one writer per stamp and per marker field).
6. **Adjacent finds:** `ReassignSessionSeries` carries the same omission — absorbed (verdict 1). The ~120-seat
   atomic-move bound — the designer out, filed with the census (verdict, *Out*). Riley's leaked 18:00 cells — a
   released booking on a tombstoned session, past on 2026-09-19; recorded, not repaired (verdict, *Rejected*).
7. **Non-goals:** telling a waitlisted member of a swap (no seat); a claim-time instructor snapshot on the booking; a
   session-level stamp for the time move; the roster's view of who leads (the session card already says *with
   <name>*); repairing cells stranded before this fire on tombstoned sessions.
