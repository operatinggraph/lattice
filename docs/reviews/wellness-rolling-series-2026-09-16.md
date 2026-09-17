# Wellness — "A recurring class stops when its count runs out" (2026-09-16)

**Filed (PO, 2026-09-16):** `occurrenceCount` (≤52) is the series' whole life; Evening Flow with Sam ends Sep 23 and
Riverside's schedule is then empty until the desk retypes the series. A rolling horizon mints the next occurrence as
the last approaches; the studio card's "runs out in N days" is the only guard today. Winston-adjudicated
(implementation-level; no contract surface, no fork).

## Grounding

- **The series is an eager, bounded batch by design, and the DDL names extension a non-goal.**
  `sessionSeriesVertexTypeDDL` ([ddls.go:504-523](../../packages/wellness-domain/ddls.go)) chose the eager batch over
  "a lens + Weaver directOp rolling series" because every occurrence's shape is known up front, and closed with
  *"extending an open-ended series later is a deliberate non-goal — re-run CreateSessionSeries anchored on the last
  occurrence's end"*. The PO row is the product decision that supersedes that non-goal for the series the desk marks
  rolling; the eager batch stays the mechanism (a rolling series is the same batch, minted one occurrence at a time
  once the window moves).
- **The series records its authored shape and nothing about its frontier.** `.definition = {name, capacity,
  priceCents?, residentPriceCents?, intervalDays, occurrenceCount, firstStartsAt, firstEndsAt}` is written once
  ([ddls.go:3224-3239](../../packages/wellness-domain/ddls.go)) and never rewritten (`ReassignSessionSeries` leaves it
  "as the minted fact", [:588](../../packages/wellness-domain/ddls.go)). The instructor is not on it — it is a `ledBy`
  link per occurrence. A lens cannot do date arithmetic and `wellnessSessions` has no aggregate, so "the last
  occurrence" is not projectable from the occurrences; the frontier has to be a **recorded fact on the series**.
- **The deadline mechanism is shipped and clockless.** `wellnessWaitlistPromotion`
  ([lenses.go:711-732](../../packages/wellness-domain/lenses.go)) arms `freshUntil` on a stored instant; the fired
  `MarkExpired` records `.freshnessExpiry.byTarget.<target>` = the deadline on the anchor
  ([weaver.md § temporal lane](../components/weaver.md)); the gap reads `lapsedAt >= deadline`. A past `freshUntil` fires
  at once, so a horizon that lapsed while the stack was down still catches up. Params templated off a null column
  are a Weaver data error ([strategist.go:800-811](../../internal/weaver/strategist.go)) — a nullable instructor
  cannot ride one playbook's `Params`.
- **A cell collision is detected by a read, not only at commit.** `claim_cell` reads the cell and refuses
  `StudioConflict`/`InstructorConflict` on a live one ([ddls.go:2839-2851](../../packages/wellness-domain/ddls.go));
  `derive_reads` computes CreateSession's cells from the payload alone ([:2964-3075](../../packages/wellness-domain/ddls.go)).
- **A declared-read write is bare, not OCC** — `applyHydratedRevisions` conditions it on the hydrated revision and
  re-executes in-process on conflict; an explicit `expectedRevision` on a hydrated key is an unretried rejection.
- **FE:** `studioGridWarning` ([app.js:3699-3719](../../cmd/wellness-app/web/app.js)) computes "runs out in N days"
  from the studio's upcoming rows; the schedule form's `Number of classes` > 1 builds a `CreateSessionSeries`
  ([:4175-4200](../../cmd/wellness-app/web/app.js)); the roster's series call-off and move dispatches declare
  `reads: [seriesKey, …]` ([:2727](../../cmd/wellness-app/web/app.js), [:2780](../../cmd/wellness-app/web/app.js)).
- **Census (2026-09-16, live stack):** 0 series carry a horizon (the aspect does not exist); the install changes no
  existing series — a series is rolling only when the desk asks.

## Verdict — *a rolling series keeps `occurrenceCount` classes on the books; the window moves as each class starts*

1. **`rolling` on `CreateSessionSeries`, recorded as a `.horizon`.** An optional boolean; when true the op also mints
   `.horizon` (class `sessionSeriesHorizon`) = `{nextStartsAt, nextEndsAt, extendAt, instructor?, mintedCount}` on the
   series: `next*` is the occurrence after the batch's last, on the cadence; `extendAt = nextStartsAt −
   occurrenceCount·intervalDays` (= `firstStartsAt` at creation — the start of the earliest class in the window);
   `instructor` the series' authored instructor key. Invariant: **a rolling series always has `occurrenceCount`
   occurrences minted ahead of `extendAt`** — the next class is minted the moment the window's earliest class
   starts, so the schedule never runs out.
2. **One lens, two gaps, one op.** `wellnessSeriesHorizon` (anchor `sessionseries`, flat) projects `seriesKey`,
   `studioKey` (`atStudio`), `nextStartsAt`, `nextEndsAt`, `extendAt`, `instructorKey` (`.horizon.instructor`),
   `lapsedAt` (`.freshnessExpiry.byTarget.wellnessSeriesHorizon`); `freshUntil = extendAt` while `extendAt <> null AND
   NOT lapsedAt >= extendAt`; `missing_occurrence` = `extendAt <> null AND studioKey <> null AND instructorKey = null AND
   lapsedAt >= extendAt`, `missing_led_occurrence` the same with `instructorKey <> null` — two gaps because the
   instructor is nullable and a null `Params` column is a refusal; both dispatch `ExtendSessionSeries`, the led one
   passing `instructor: row.instructorKey`. `maxretries_occurrence` = 3.
3. **`ExtendSessionSeries{seriesKey, studio, startsAt, endsAt, instructor?}`** — Weaver's dispatch actor only
   (`op.actor != primordialActor["weaver"]` → `AuthDenied`, the `ReleaseOrphanedBooking` guard). Refuses
   `UnknownSeries`, `WrongStudio` (the series' `atStudio` link, walked), `NotRolling` (no `.horizon.extendAt`),
   `StaleHorizon` (payload `startsAt`/`endsAt`/`instructor` ≠ `.horizon.next*`/`.instructor` — a stale row is refused,
   never trusted). Then **mint-or-skip**: the occurrence is skipped (no session) when `startsAt <
   time.rfc3339_utc(op.submittedAt)` (the class the horizon would mint is already in the past — the stack was away)
   or any of its studio/instructor cells is live (the slot was booked one-off; the run passes that week, exactly as
   the desk would; a retired instructor mints the class unled and the horizon drops them); otherwise it mints one occurrence with `CreateSessionSeries`' per-occurrence shape from
   `.definition` (name, capacity, prices) — the loop body factored into a shared `mint_occurrence`. Either way it
   bare-updates `.horizon` (`next* += intervalDays`, `extendAt += intervalDays`, `mintedCount + 1` when minted) and emits
   `wellness.sessionSeriesExtended {seriesKey, studio, startsAt, sessionKey?, skipped?}`. The write moves `extendAt`
   past the recorded lapse, so the gap closes and `freshUntil` re-arms on the new deadline. `derive_reads` derives the
   studio/instructor roots + the cells from the payload (CreateSession's own arm); the playbook declares
   `Reads: [row.seriesKey, row.seriesKey.definition, row.seriesKey.horizon, row.studioKey]` and enumerations
   `{row.seriesKey atStudio out}`, `{row.studioKey locatedAt out}`.
4. **The run's other verbs keep the horizon true, and the off switch walks nothing.** `ReassignSessionSeries` shifts
   `.horizon.next*` and `extendAt` by its delta (the cadence moved with the classes — a backward move can leave the
   shifted `extendAt` at or before the recorded lapse, so the gap opens without a timer and mints until the cadence is
   `occurrenceCount` slots ahead again; the window counts cadence slots, not live classes, a skipped slot included —
   accepted, not clamped). `TombstoneSessionSeries` stops the roll when its walk succeeds — `.horizon` rewritten
   without `extendAt` (`stoppedAt` recorded) — and a rolling series with nothing left to cancel still stops rather than
   refusing `NoUpcomingOccurrences`. But a rolling run's `partOf` history grows without bound and outgrows both
   walks — the 2×64 page bound, and before it the 250 ms Starlark wall (one read per historic occurrence after the
   walks read `.schedule` first; a daily run reaches the page bound in ~4 months) — so **`StopSessionSeries{seriesKey,
   studio}`** is the off switch that outlives the walk: the same `[operator, frontOfHouse]` grant and workplace
   confinement as the call-off, the series' `atStudio` confirmation (`WrongStudio`), `NotRolling` on a run that is not
   rolling, and one bare write of the stopped `.horizon`. Its remaining classes are called off per class or with the
   call-off while the walk still fits. All three read `.horizon` (and the series' confirmation reads) as
   **server-derived optionalReads** (`derive_reads`, off the payload's `seriesKey`) so every dispatcher — the app,
   Facet, a CLI — hydrates it without declaring it; a client that could omit the declaration would otherwise cancel
   the classes and leave the horizon minting the next window. `.horizon` has five writers, one owner op per
   transition: create, extend, move, stop, call-off.
5. **The desk sees it.** `wellnessSessions` projects `seriesRolling` (`ss.horizon.data.extendAt <> null`); `/api/sessions`
   carries it; the schedule form gains *Keep rolling* beside *Number of classes* (sent as `rolling: true` when the run
   repeats); the studio card's "runs out in N days" is not shown while a rolling run is on its books, and the class
   card's series line says *rolling*, and the roster offers *Stop rolling* (confirmed) beside the call-off.
6. **Non-goals:** changing the cadence or shape of a rolling series in place (`ReassignSession` per occurrence, or stop
   and re-create); a per-occurrence skip notice; a rolling `CreateSession` (a one-off has no cadence).

### Fire brief (build note, 2026-09-16)

1. **Scope** (board row, verbatim): *`occurrenceCount` (≤52) is the series' whole life; Evening Flow with Sam ends Sep
   23 and Riverside's schedule is then empty until the desk retypes the series. A rolling horizon mints the next
   occurrence as the last approaches; the studio card's "runs out in N days" is the only guard today.* Green bar: a
   series created `rolling` carries `.horizon`; the lens arms `freshUntil = extendAt`; the recorded lapse opens the gap
   and one dispatch mints exactly one occurrence on the cadence and moves the horizon by one interval (the gap
   re-projects closed and `freshUntil` re-arms); a collision or a past occurrence is skipped and the horizon still
   moves; the call-off stops it; a move shifts it; a non-rolling series is untouched.
2. **Touch-list (verified live):** `packages/wellness-domain/ddls.go`: `:504-523` (series DDL doc — the non-goal
   sentence is rewritten), `:524-694` (`sessionSeriesVertexTypeDDL` `PermittedCommands` + `ExtendSessionSeries`,
   `rolling` in the params schema/help/examples), `:753-791` (`sessionSeriesDefinitionAspectTypeDDL`, the shape to
   mirror for the new `sessionSeriesHorizonAspectTypeDDL`), `:2350-2360` (helpers; add `make_aspect_upsert`),
   `:2964-3075` (`derive_reads`: `ExtendSessionSeries` cells + roots, `TombstoneSessionSeries`/`ReassignSessionSeries`
   `.horizon`), `:3185-3305` (`CreateSessionSeries`: `rolling`, `.horizon`, the loop body → `mint_occurrence`),
   `:3419-3580` (`TombstoneSessionSeries`: stop), `:3582-3891` (`ReassignSessionSeries`: shift), `:5836` (the actor
   guard to mirror). `lenses.go`: `:58` (target constant), `:189-197` (lens declaration to mirror), `:470-505`
   (`wellnessSessions` RETURN + `seriesRolling`), `:711-732` (the spec to mirror). `targets.go:98-157` (+ target).
   `permissions.go:136-140` / `:228-232` (+ operator grant). `opmetas.go:579-631` (`rolling` in the descriptor).
   `retry_budget.go` (+ `maxOccurrenceRetries`). `package.go:155-156` + `manifest.yaml` (0.29.0 → 0.30.0; the
   Description's series sentence). `scripts/verify-package-wellness-domain.go:212,217,438-447,470-490` (ddlCheck rows,
   the lens, the target). Tests: `lens_cypher_test.go:1490-1560` (the promotion pins + `recordPromotionLapse` to
   mirror), `promote_waitlist_test.go:37-53` (Weaver-actor submission), `derive_reads_test.go:73` (vector shape),
   `integration_test.go:104` (`domainWeaverCapDoc`), `:1104`. `cmd/wellness-app/`: `sessions.go:36-41,56-77,121,249`
   (+ `SeriesRolling`), `web/app.js:1142-1150` + `:2499-2510` (series line), `:3699-3719` (`studioGridWarning`),
   `:3786-3788` (form), `:4175-4200` (payload); `web_retire_studio_test.go` (goja pin of the warning to extend).
3. **Precedents:** the lens → `waitlistPromotionSpec` (`lapsedAt`/`freshUntil`, `fmt.Sprintf` on the target constant,
   the declaration at `:189-197`); the two-gap/one-op target → `wellnessBookingChangeNotices`
   (`wellness-reminders/targets.go:31-90`), `Reads`/`Enumerations` → `waitlistPromotionTarget`; the op's actor guard →
   `ReleaseOrphanedBooking:5836`; the stale-pin refusal → `ReassignSessionSeries`' `AnchorMoved`; the mint →
   `CreateSessionSeries`' loop body verbatim; the bare update → clinic-domain `make_aspect_upsert` (`:1418`); the
   `derive_reads` arm → CreateSession's; the FE checkbox → the form's existing fields; the goja pin →
   `web_retire_studio_test.go`.
4. **Increments:** (1) package: horizon DDL + `rolling` + `mint_occurrence` + `ExtendSessionSeries` + stop/shift +
   `derive_reads` + lens + target + grant + op-meta + versions + verify script; tests: cypher pins (rolling armed / not
   rolling null / lapsed → gap, led vs unled / extended → closed / stopped → null / studio gone → closed), op vectors
   (mint; skip past; skip collision; `StaleHorizon`; `NotRolling`; `WrongStudio`; non-Weaver denied), `rolling` on
   create, stop on call-off (incl. nothing-to-cancel), shift on move, three `UndeclaredSubmitter` vectors. Green:
   `go test ./packages/wellness-domain/ -count=1`, `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`.
   (2) app: `seriesRolling`, the checkbox + payload, the warning gate, the series line, goja pin. Green:
   `go test ./cmd/wellness-app/ -count=1`, `go build ./...`, `make vet`, `golangci-lint run ./...`,
   `for f in scripts/lint-*.go; do STRICT=1 go run "$f" || echo "FAIL $f"; done`, `gofmt -l scripts/`. (3) live:
   `make refresh-wellness` from the main checkout, `make verify-package-wellness-domain`, cycle `bin/wellness-app`,
   create a rolling 2-class run whose first class starts in ~2 min, watch the third class appear and the horizon move.
5. **Gotchas:** package edit ⇒ version bump + `Version` mirror; a lens RETURN edit is a corpus edit; `ddlCheck` rows
   for the two new DDLs + the sessionseries command count (3 → 4); `lint-derive-reads-bare-vector` — every op added to
   `derive_reads` needs its `Test*UndeclaredSubmitter*` vector; `lint-live-read-pinned-mutation` binds live reads only
   — `.horizon` is hydrated (declared / derived) everywhere it is written, so the write is bare; `lint-refusal-courtesy`
   — `ExtendSessionSeries` has no client site; `TombstoneSessionSeries` loses a refusal on one path (no courtesy change);
   `lint-conventions`' primordial-actor rule; the `_packages.md` dossier: *a dispatch declaration must name what the
   runtime binds* (Params off an OPTIONAL hop — `studioKey <> null` is the gap's own conjunct; the instructor split
   is the same rule), *a gap's budget and cadence from its whole loop* (one dispatch closes the gap: the write moves
   `extendAt` past the lapse — pin the re-projection after the op; a refused dispatch stays open and the standing
   `GapBudgetExhausted` retires when the desk stops or moves the run), *the leg of a guard is every op that writes the
   guarded value* (`.horizon` writers: create, extend, move, stop — each enumerated), *a recorded value is read as the
   fact it records* (`extendAt` is the window's earliest start, not "now"; the skip reads `submittedAt`, the dispatch
   instant); the vertical-apps dossier: *a count the FE promises applies the op's predicate* (the series line's
   count is untouched; *rolling* is a label). Standing checklist #1 (`.horizon` lifetime: created at a rolling create,
   advanced by every extension, shifted by a move, closed by the call-off, never reset, dies with nothing — the series
   is never tombstoned) · #3 (each refusal and each gap conjunct revert-proven; the skip's two arms each pinned) · #5
   (`.horizon`: four writers, one owner op per transition, all on the hydrated revision).
6. **Adjacent finds:** none from the scout.
7. **Non-goals:** as §6 above; the reminder/promotion mechanisms; `CreateSession`.

### Build note (2026-09-16)

Shipped `4a43c82d` (`1ce08997` on the fire branch; brief `44db1310`). Live on the shared stack (wellness-domain 0.30.0
diff-applied, `verify-package-wellness-domain` 562/562, `bin/wellness-app` cycled): a rolling 2-class weekly run at
Riverside created 05:07:28Z with its first class at 05:15:00Z — the lens armed `@at 05:15:00Z`, the fire recorded the
lapse, and the third class was minted at 05:15:04Z with the horizon one week on and the timer re-armed at 09-24
05:15:00Z; `/api/sessions` served the three rows `seriesRolling`; `StopSessionSeries` dropped `extendAt` and the row
disarmed; the two upcoming classes were then called off. No Weaver issue raised.

Deviations from the brief: (1) **`StopSessionSeries`** (the BLOCKING cold-review find) — the brief made the call-off
the only stop, whose walk a rolling run's history outgrows; (2) `derive_reads` derives the series' confirmation reads
beside `.horizon` for the three series verbs (`lint-derive-reads-bare-vector` counts only a vector declaring nothing;
weakest-wins keeps the app's required reads required); (3) a retired instructor mints the class unled and the horizon
drops them (`TombstoneInstructor` has no upcoming-classes guard — a refusal would park the gap); (4) two gaps carry
their own `maxretries_<gap>` column (the Weaver reads the gap's suffix); (5) the walks read `.schedule` first.
Review classification: one design gap (1 — the walk bound's premise was stated for an eager series and the design
did not re-derive it for an unbounded one), one brief gap (2), two implementation gaps found by the builder's own
report and the reviewer (3, 4), five convention findings (stale stop-verb attributions, a missing confirm, the
`lint-app-op-descriptors` ceiling), one test gap (the own-studio-for-foreign-series vector), no review over-reach.
Accepted and recorded: a backward move's surplus slots (§4); `ExtendSessionSeries` refused by a cell claimed between
projection and dispatch re-executes in-process on the hydrated-revision conflict and skips. Adjacent finds: none.

