# Clinic — "A patient with 38 no-shows books like anyone else" (2026-09-17)

**Filed (PO, 2026-09-17).** The booking prompt reads arrears only; no read model counts a patient's recent
no-shows. "A `noShowCount` over the last 90 days on the roster row and in the booking prompt." Live: Riley Chen 38
in 90 d, 17 booked ahead. Winston-adjudicated (implementation-level; no contract surface, no fork).

## Grounding

- **The roster row already walks every appointment of the patient.** `clinicPatientsReadSpec`
  ([lenses.go:959](../../packages/clinic-domain/lenses.go)) matches `(p)<-[:forPatient]-(a:appointment)` to
  aggregate `buildingAnchors` in one `WITH p, id, collect(DISTINCT …)`; the aggregate boundary is where a count
  belongs, and the walk fans out over the provider/site hops, so a count must be `count(DISTINCT …)`. Precedents:
  `count(DISTINCT CASE WHEN … THEN bk.key ELSE null END)` ([edge-manifest lenses.go:946](../../packages/edge-manifest/lenses.go)),
  `max(tx.key)` ([clinic-ledger lenses.go:170](../../packages/clinic-ledger/lenses.go)); `max`/`min` are engine
  aggregates ([expr_eval.go:164](../../internal/refractor/ruleengine/full/expr_eval.go)).
- **"The last 90 days" needs a clock in the lens**, and a lens reads no clock — every deadline lens in the corpus
  compares recorded facts to recorded facts (the `appointmentReminders` doc block, `lens_clock_reference` census).
  The need behind the window is "the desk sees a habitual no-show at booking"; a count plus the most recent
  no-show's date says it without a clock, and the desk reads recency off the date.
- **No-shows are two populations** — the sweep's documentation lapse (`by: sweep`, 60 of the live 67; unbilled by
  PO ruling) and the desk's observed miss (`by: staff`). `.status.by` is stamped only since `0.40.0` (today), so the
  live 67 carry no author and the split is not computable on the corpus; the desk can repair a wrong no-show via
  `CorrectAppointmentStatus`. Count every `noShow`.
- **The FE.** `protectedPatientRow` ([patients.go:30](../../cmd/clinic-app/patients.go)) + `selectPatientsSQL` /
  `selectPatientsFilteredSQL` (:56/:68) + the `Scan` (:105); `staff_patients_rls_test.go:16` mirrors the column
  list; `overdueBookingPrompt(name, row)` ([app.js:3935](../../cmd/clinic-app/web/app.js)) is the pure confirm
  message `submitBook` (:3165) gates on, pinned by `TestOverdueBookingPrompt` ([ledger_ui_test.go:133](../../cmd/clinic-app/ledger_ui_test.go));
  the patient switcher's option text appends `arrearsBadgeText` (:874) and `renderPatientContact` (:715) shows it
  beside the switcher.

## Verdict — *the roster row counts the patient's no-shows and dates the latest; the desk is asked before booking a repeat no-show*

1. **`clinicPatientsRead` gains `no_show_count` (integer) and `last_no_show_at` (text).** In the existing WITH:
   `count(DISTINCT CASE WHEN a.status.data.value = 'noShow' THEN a.key ELSE null END) AS noShowCount`,
   `max(CASE WHEN a.status.data.value = 'noShow' THEN a.schedule.data.startsAt ELSE null END) AS lastNoShowAt`
   (canonical UTC, lexical max = latest). A patient with no appointments reads 0 / null. Columns +2; the
   `provision-readpath` `ADD COLUMN IF NOT EXISTS` path lands them live (`amendedAt` precedent).
2. **`protectedPatientRow` carries `noShowCount` + `lastNoShowAt`** (both SQL constants, the `Scan`, the RLS
   test's column list).
3. **The desk is asked.** A pure `noShowBookingPrompt(name, patientRow)` → `"<name> has <N> recorded no-show(s),
   the last on <local date>. Book them anyway?"` when `noShowCount >= 2`, `""` otherwise (one no-show is not a
   habit; the threshold is a UI courtesy, not a rule — `CreateAppointment` enforces nothing). `submitBook` asks it
   after the arrears prompt, same `window.confirm` posture; the patient switcher's option text and
   `renderPatientContact` append `"· N no-shows"` via a pure `noShowBadgeText(row)` beside the arrears badge. Goja
   pins for both, in `ledger_ui_test.go`'s idiom. Staff surfaces only — `asSelf` books nothing for others and the
   prompt is a desk courtesy.

Alternatives priced: **do nothing** — the desk has no signal at the one moment it could act; **a rolling 90-day
window** — a clock in a lens; **the FE counting from the appointments list** — the desk's grid is scoped to the
selected patient's future visits and paged; the roster row is the one slice every booking surface already holds.

### Fire brief (build note, 2026-09-17)

1. **Scope** (row, verbatim): *no read model counts a patient's recent no-shows … A `noShowCount` over the last 90
   days on the roster row and in the booking prompt.* Narrowed: all-time count + latest date (no clock). Green bar:
   the roster row projects `no_show_count` / `last_no_show_at`; the confirm fires at ≥ 2 with the date; the badge
   renders beside arrears.
2. **Touch-list:** `packages/clinic-domain/lenses.go:379–386` (Columns) + `:966–977` (WITH + RETURN);
   `protected_lens_test.go:494–560` (+ pins: 0 / 2 no-shows with the latest date, a cancelled visit not counted);
   `package.go` + `manifest.yaml` (0.41.0 → 0.42.0, after the displacement fire's bump); `cmd/clinic-app/patients.go:30–36,
   :56–72, :105`; `staff_patients_rls_test.go:16–23`; `web/app.js:715, :874, :3165, :3935`; `ledger_ui_test.go`
   (+ `TestNoShowBookingPrompt`, `TestNoShowBadgeText`).
3. **Precedents:** the aggregates above; the column path `08888209`; `overdueBookingPrompt` + `arrearsBadgeText`.
4. **Increments:** (1) lens + columns + pins → `go test ./packages/clinic-domain/ -count=1`, refractor census;
   (2) app → `go test ./cmd/clinic-app/ -count=1`, `go build ./...`, `make vet`, `golangci-lint run ./...`, the
   STRICT lint loop, `gofmt -l`; (3) live: `make refresh-clinic`, cycle `bin/clinic-app`, read `/api/staff/patients`
   for Riley Chen.
5. **Gotchas:** version bump + `Version` mirror; a lens RETURN edit is a corpus edit (`grouping_reduction` /
   `branch_decomposition` censuses may pin this spec's shape — run them); `clinic_patients_read_retraction_test.go`
   pins the WITH boundary binds `p` — the aggregates ride the same WITH; the DISTINCT is load-bearing (the hops fan
   out). Dossier (`vertical-apps.md`): *a count the FE promises must apply the op's own predicate* — the count is
   the lens's `= 'noShow'`, the prompt states "recorded no-shows", never "missed visits".
6. **Adjacent finds:** none.
7. **Non-goals:** a rolling window; splitting sweep vs desk no-shows; any refusal on booking; the patient's own view.
