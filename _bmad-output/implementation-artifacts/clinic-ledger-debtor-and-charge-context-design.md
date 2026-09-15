# Clinic — the desk sees a debtor, and a charge names its visit

**Status:** ✅ **SHIPPED** `0771d77e` (2026-09-14; ratified + built by the Vertical Steward the same day, build note §6). No frozen-contract change, no
architectural fork: every mechanism is package-owned (`packages/clinic-ledger`, `cmd/clinic-app`) and mirrors a
shipped pattern in a sibling vertical — wellness's `frontdesk-arrears` desk badge (`cmd/wellness-app/ledger.go:479`,
`web/app.js:2915`) and café's `reversesKey`-before-FIFO statement aging (`cmd/cafe-app/ledger.go:234`, `5e6e08a0`).
One PO row (`fad66d4c`, ★★ M, pkg + FE) builds as one fire.

## 1. Problem (PO-filed, live-observed)

- `/api/staff/patients` (`cmd/clinic-app/patients.go:132`) carries no balance; the only balance on the platform is
  `/api/ledger`'s whole-history sum, one patient at a time. The desk cannot see who owes before booking them. Live:
  Riley Chen owes 2500 cents across ~100 lines and nothing on the roster says so.
- `/api/ledger` (`ledger.go:198`) is a flat chronological list: no line says whether a charge is still open, when it
  fell due, or how long it has been overdue. Café and wellness age their statements; clinic does not.
- `clinicLedgerHistory` (`packages/clinic-ledger/lenses.go:179`) never walks `reverses`, so the 2026-09-14
  "Fee reversal (corrected)" credit projects no `reversesKey` — a waiver names no charge in the history the desk reads,
  and the FE's Waive button (`app.js:3989`) never lets the desk say which charge it forgives. A hand waiver of a
  no-show fee followed by a status correction is therefore credited twice: the human waiver carries no `reverses`
  link, so `missing_reversal` (`lenses.go:125`) still counts `reversalCount = 0` and dispatches its own.
- `ClinicDebitAccount`'s only visit reference is `appointmentRef` → the `settles` link (`scripts.go:497`), which
  `clinicNoShowSettlement` reads as "this transaction IS the fee this appointment's status carries". The op validates
  the appointment alive and nothing else, so a desk copay that names a completed visit through it opens
  `missing_reversal` (`feeCents null ∧ txCount = 1 ∧ reversalCount = 0`) and is credited straight back. The 08-30
  copays name no visit at all because the FE's charge form has no way to say one.

## 2. Decisions

### 2.1 A charge names its visit through its own relation — `visitRef` → `forVisit`

`ClinicDebitAccount` gains an optional `visitRef` (`vtx.appointment.<NanoID>`, validated alive from `state` →
`UnknownAppointment`, the same `vertex_alive` check `appointmentRef` runs, and validated as **this account's
patient's** visit → `WrongPatient`, through the account's `heldFor` walk and the `forPatient` link as its `(e)`
follow-up — the descriptor declares the enumeration) and writes
`lnk.clinictransaction.<t>.forVisit.appointment.<a>` — the transaction is the later-arriving vertex, so it is the
source (Contract #1 §1.1); the sentence is "this transaction is for this visit". `visitRef` and `appointmentRef`
are mutually exclusive (`InvalidArgument`): one line either *is* the fee an appointment's status carries or is
*for* a visit, never both. `forVisit` is never read by `noShowSettlementSpec`, so a copay on a no-show appointment
neither counts as its fee (`missing_charge` still fires) nor opens a reversal.

**Dispatchers, declared (Contract #2 §2.5):** the descriptor (`opmetas.go:62`) gains `visitRef` in its
`InputSchema` and `"{payload.visitRef}"` in `Reads` — `form.mjs`'s `substituteTemplates` drops a template whose
payload field is absent (`internal/descriptorform/form.mjs:680`), so a plain charge declares nothing extra. The
Weaver dispatch (`targets.go:48`) never sends `visitRef`. No seed or verify script submits `ClinicDebitAccount`.

### 2.2 `appointmentRef` is refused on an appointment whose status carries no fee — `NoFeeToSettle`

`post_entry` reads `appointmentRef`'s `.status` and refuses `NoFeeToSettle` unless `noShowFeeCents > 0`. This makes
`noShowSettlementSpec`'s own premise ("the only way a fee-less status and a live `settles` link coexist is a
correction") true at the write path instead of assumed. `.status` is hydrated by the script's own `derive_reads`
(`scripts.go:646` — the platform-owned channel that already guarantees `.balance`), returned as an **optionalRead**
(`{appointmentRef}.status`) whenever the payload carries a well-formed appointment key; absence → no fee → refused.
`targets.go`'s `missing_charge` also declares it statically (`OptionalReads: row.appointmentKey.status`) so the
dispatch's read set documents itself. A dispatch that races a status correction (the gap was open when Weaver read
the row, the correction landed before the op ran) is now refused instead of charged-then-reversed; the next
projection reads `feeCents = null ∧ txCount = 0` and the gap is simply closed.

### 2.3 The history projects `reversesKey` and one visit per line

`ledgerHistorySpec` gains `OPTIONAL MATCH (t)-[:reverses]->(rt:clinictransaction)` → `rt.key AS reversesKey`
(wellness `lenses.go:421`, café `lenses.go:127` — the same column name both statements age on) and
`OPTIONAL MATCH (t)-[:forVisit]->(v:appointment)`. The visit columns become
`coalesce(appt.key, v.key) AS appointmentKey`, `coalesce(appt.schedule.data.startsAt, v.schedule.data.startsAt) AS
visitStartsAt` (wellness's `coalesce(nsbk.key, cpbk.key)` shape) plus `(appt.key <> null) AS settlesFee`, so a reader
has one "the visit this line concerns" and one boolean saying whether the line is that visit's fee. The
`clinicLedgerHistory` rows of the corpus-census pins (`internal/refractor/*_corpus_census_test.go`) re-pin
deliberately.

### 2.4 The waiver names the charge it forgives — `reversesRef` reaches the desk

`ClinicCreditAccount` already accepts `reversesRef` and writes `reverses`; the build confines it — the named debit
must be posted to **this account** (`WrongAccount`, the deterministic `postedTo` link derived by `derive_reads`) and
the **self-pay leg refuses it** (`AuthDenied` — a patient reversing their own fee would disarm `missing_reversal`), and
a credit carrying `appointmentRef` is `InvalidArgument`. The desk's Waive button gains a
"Charge to waive" picker over the statement's still-open debits (2.5's `openCents > 0`), prefilling the amount with
the open remainder and capping it there (`max` — the sibling-form courtesy). `reversesRef` is optional on the wire
(a waiver of a general balance stays expressible). A hand waiver that names a no-show fee now carries the `reverses`
link `missing_reversal` counts, so a later correction reverses nothing twice. The descriptor (`opmetas.go:97`) gains
`reversesRef` in `InputSchema` and `"{payload.reversesRef}"` in `Reads`; the self-scoped patient path never sends it.

### 2.5 The statement is aged, per charge — `deriveStatement` mirrored from café, extended to the line

`cmd/clinic-app/ledger.go` gains café's `deriveStatement` (`cmd/cafe-app/ledger.go:234–310`: a `reversesKey`
credit retires its named debit before the FIFO, surplus prepays, a 15-day grace from the oldest open debit's
`postedAt`; `statementGraceDays = 15` as a package-local constant, the wellness mirror's shape) **carrying the
transaction key through the open queue** so each debit row is annotated: `openCents` (its unpaid remainder),
`dueAt` (`postedAt + 15 d`, debits only), `isOverdue`, `daysOverdue`. The statement carries `dueDate`,
`isOverdue`, `daysOverdue` from the oldest open debit, as both precedents do. A credit row carries `reversesKey`
through. `/api/ledger`'s rows gain `reversesKey`, `settlesFee`, `openCents`, `dueAt`, `isOverdue`, `daysOverdue`.

### 2.6 The desk sees a debtor — `GET /api/staff/arrears`

Mirrors `handleFrontDeskArrears` (`cmd/wellness-app/ledger.go:479`): `authenticateRead`, then the actor's
RLS-visible roster (`queryPatients(ctx, pool, actor, "")` — the same protected `clinicPatientsRead` gate
`handleStaffPatients` and `patientVisibleToActor` already stand on; a patient session sees only its own row, a
staff wildcard the whole roster), one `KVListKeys` of `clinic-ledger-history`, grouped by `patientKey`,
`deriveStatement` per patient, rows with `balanceCents > 0` sorted worst-first (`isOverdue desc, daysOverdue desc,
balanceCents desc`) → `{"arrears": [{patientKey, accountKey, balanceCents, dueDate, isOverdue, daysOverdue}]}`.
P5: lens read models only.

**FE** (`app.js`): `loadArrears()` alongside `loadPatients()` for a front-desk session into `state.arrears`
(`Map<patientKey,row>`); `arrearsBadgeText(row)` → `"owes $X · N days overdue"` (wellness `app.js:2915`,
goja-pinned) appended to `populatePatientSelect`'s option text (`:828`), `renderPatientContact`'s line (`:705`) and
the ledger balance header; `submitBook` (`:3059`) confirms first when the selected patient is overdue (wellness
`bookSelectedGuest`, `app.js:1673`). The ledger list renders each debit's `openCents`/`dueAt`/overdue state and a
credit's "reverses <the charge's memo · date>"; the Charge form gains a "For visit" picker over the patient's
non-cancelled, non-no-show appointments already in `state.appts` (a courtesy narrowing — the op accepts any alive
visit of the patient; no clock, since a checked-in visit carries none), sending `visitRef` through the descriptor's
`prefill`; the Waive picker pre-selects the charge when exactly one is open.

## 3. Not changed

`settles`' meaning, `noShowSettlementSpec`'s three gaps, `.balance` and the self-pay cap, the fee posture, the
`TERMINAL_STATUSES` of clinic-domain, wellness's and café's own statements. No new lens: the history lens already
carries every column the aging needs once `reversesKey` is projected.

## 4. Consumers of the changed shapes

| shape | consumer | arm |
|---|---|---|
| `forVisit` link | `ledgerHistorySpec` only | 2.3 |
| `settles` on a fee-less appointment | refused at the op (2.2) | `noShowSettlementSpec`'s comment now holds |
| `reverses` on a hand waiver | `missing_reversal`'s `reversalCount` | 2.4 — counted, no double credit |
| `appointmentKey`/`visitStartsAt` (now coalesced) | `cmd/clinic-app/ledger.go` row, `renderLedger` | unchanged rendering + `settlesFee` |
| `clinicLedgerHistory` MATCH | refractor corpus census pins | re-pin |

## 5. Fire brief (build note, 2026-09-14)

**1. Scope sentence (board row, verbatim):** *The clinic desk can't see a debtor, and a charge can't name its visit —
The roster carries no balance and the ledger is a flat list — no per-charge open/overdue state, a waiver names no
charge, a copay names no visit (wellness `frontdesk-arrears` + café `reversesKey` aging are the precedent).
`appointmentRef` means "no-show fee": a desk copay attached to a completed visit opens `missing_reversal` and is
credited back.* Green bar: §2.1–2.6 built, every gate green, proven live on :7799.

**2. Verified touch-list (checked live 2026-09-14 at `e6ae6617`).**
- `packages/clinic-ledger/ddls.go:149–267` `transactionDDL` — Description, `InputSchema` (`appointmentRef` :186),
  `FieldDescription`, Examples; `scripts.go:341` `post_entry` (`appointmentRef` :497–504, `reversesRef` :514–523,
  links :578–589), `derive_reads` :646–689; `lenses.go:179–196` `ledgerHistorySpec` (`noShowSettlementSpec` :125–159
  read-only); `opmetas.go:62–96` (`ClinicDebitAccount`), `:97–134` (`ClinicCreditAccount`); `targets.go:48–75`
  `missing_charge`; `manifest.yaml:2` + `package.go:90` (`0.4.0` → `0.5.0`); `README.md`; `ledger_test.go`
  (`TestDebitAccount_AppointmentRefWritesSettlesLink` :640 seeds a *scheduled* appointment and will need a fee-bearing
  status); `lens_cypher_test.go`.
- `internal/refractor/{actor_walk_scope,grouping_reduction,…}_corpus_census_test.go` — `clinicLedgerHistory` rows.
- `cmd/clinic-app/ledger.go:17–47` projection/row structs, `:49–95` `computeLedgerHistory`, `:198–283` `handleLedger`;
  `patients.go:132–156` `handleStaffPatients` + `queryPatients`; `server.go:75,84` routes; `ledger_test.go`,
  `ledger_rls_test.go:24`, `staff_patients_rls_test.go`.
- `cmd/clinic-app/web/app.js:753` `loadPatients`, `:828` `populatePatientSelect`, `:705` `renderPatientContact`,
  `:3059` `submitBook`, `:3185` `loadAppts` (`state.appts`), `:3866` `loadLedger`, `:3897` `renderLedger`, `:3989`
  `submitLedgerEntry`; `web/index.html:120–131` the ledger panel; `followup_addressed_test.go:19` the goja pin shape.

**3. Precedents to mirror.** `visitRef`/`forVisit` ← `appointmentRef`/`settles` in the same script (:497, :578);
`derive_reads` optionalRead ← its own `.balance` derivation (:646); `reversesKey` + coalesced visit ←
`packages/wellness-ledger/lenses.go:400–421`; `deriveStatement` ← `cmd/cafe-app/ledger.go:234–310` (+ the
`ledgerEntryRow.ReversesKey` it reads); `/api/staff/arrears` ← `cmd/wellness-app/ledger.go:479–542`; badge + confirm ←
`cmd/wellness-app/web/app.js:2915–2923`, `:1673–1681`; the visit/charge pickers ← the descriptor `prefill` path
`submitLedgerEntry` already uses (:4021). Greenfield: per-charge `openCents`/`dueAt` — neither precedent annotates the
line; the walk already holds the open queue, so the annotation is the queue keyed by transaction.

**4. Increment order + green checks.**
- **Inc 1 — package** (`opus`: a new refusal + a new link + a derived read): §2.1–2.4 op/lens/descriptor/targets,
  version bump, README. Green: `go test ./packages/clinic-ledger/ -count=1` incl. new pins — `visitRef` writes
  `forVisit` and no `settles`; both refs → `InvalidArgument`; `appointmentRef` on a `scheduled` appointment →
  `NoFeeToSettle`, on a `noShow` (fee) appointment → accepted (positive vector first); an envelope with an EMPTY
  `contextHint` still refuses `NoFeeToSettle` (derive_reads hydrates `.status`); `reversesKey`/`settlesFee`/coalesce
  cypher pins; `TestDeriveReads_*` extended; `go test ./internal/refractor/ -run 'Corpus|Census' -count=1` re-pinned;
  `go run ./scripts/lint-package-version.go` (`DIFF_BASE=e6ae6617`), `lint-conventions`, `lint-opmeta-required-fields`,
  `lint-seed-declared-reads`, `lint-gap-column-declaration`.
- **Inc 2 — app Go** (`sonnet`): §2.5–2.6. Green: `go test ./cmd/clinic-app/ -count=1` — `deriveStatement` vectors
  (reversal pre-pass retires its named debit only; surplus prepays; per-charge remainder; 15-day due; overdue days;
  malformed `postedAt` fails closed), the arrears handler's grouping + worst-first order, the RLS boundary test for
  the new route (`ledger_rls_test.go` shape).
- **Inc 3 — FE** (`sonnet`): §2.4 picker, §2.6 FE. Green: goja pins for `arrearsBadgeText` and the per-line label
  helper; `node --check`; `go run ./scripts/lint-app-op-descriptors.go`, `lint-markup-escaping`,
  `lint-ceremony-throw-path`; `go test ./cmd/clinic-app/ -count=1`.
- **Close:** rebuild `bin/clinic-app`, `make reinstall-package PKG=clinic-ledger` (hot-reload, no restart), cycle
  `bin/clinic-app`, prove live: `curl /api/staff/arrears` names Riley Chen with `balanceCents 2500`; `/api/ledger`
  rows carry `openCents`/`dueAt`; a charge with `visitRef` lands `forVisit` and the history row names the visit; an
  `appointmentRef` against the completed 09-15 visit is refused `NoFeeToSettle`.

**5. In-scope gotchas + dossier entries.** Package edit ⇒ manifest + `Version` bump. Lens `Spec` edit ⇒ corpus census
re-pin. Descriptor field added ⇒ `lint-opmeta-required-fields`. `cmd/<app>` Go comments naming an op ⇒
`lint-app-op-descriptors`. Native `confirm()` cannot be driven unattended — prove the op by curl, screenshot the render.
Standing checklist: #1 (the arrears Map's lifetime: reset on patient reload, refreshed after every ledger entry),
#2 (every census here is a premise), #3 (a negative test needs its positive vector; the `NoFeeToSettle` test must
first ACCEPT a fee-bearing `appointmentRef`; the `.status` hydration is proven by the empty-`contextHint` vector),
#4 (n/a — nothing removed), #5 (`forVisit` is written by one op), #6 (café's `deriveStatement` is the verified
shape — `5e6e08a0`'s own test pins it). Dossier (`docs/components/_packages.md`): *A guard's OCC rests on whoever
writes its read declaration* — a key a script's correctness depends on being hydrated is returned by the script's own
`derive_reads`, and one test submits with an empty `contextHint`; *A recorded value … a hydrated aspect's absence is
two facts* — `CreateAppointment` always writes `.status` (`clinic-domain/ddls.go:3420`), so absence means undeclared
→ fail closed; *A lens that reads a RECORDED fact … for each gap conjunct, name the op-side read that answers the
same question* — `missing_charge`'s `feeCents > 0` ↔ `NoFeeToSettle`'s read; *A link key's type segment* — pin the
literal `forVisit` key string against `vtx.appointment`; *A "by construction" exclusion is a claim about the OP's
refusals* — 2.2 supplies the refusal `noShowSettlementSpec`'s comment assumed. Dossier (`vertical-apps.md`): *A
server-side refusal added to one form leaves its sibling a dead end* — the Waive picker caps at the open remainder;
*A count the FE promises must apply the op's predicate — when one rule lives in two languages, pin BOTH copies* —
`deriveStatement` lives in Go only, the FE renders its output; *Two courtesy surfaces name the same instant in
different zones* — `dueAt` is an instant (`postedAt + 15 d`), rendered local like `postedAt` beside it; *A transport
throw after a destructive submit* — `submitLedgerEntry`'s `sent`/`confirmed` staging stays; *A value reaches markup
unescaped* — the picker option text and the reverses label are `textContent`.

**6. Adjacent finds.** None filed: the scout surfaced nothing outside the row's four sentences.

**7. Non-goals.** No payment rail, no insurance claim lifecycle, no statement PDF, no per-patient arrears reminder
lens (café's `cafeArrearsReminders` is a separate item, not filed here), no change to the fee amount or the sweep.

**Scope-diff gate:** every touch above traces to one of the row's four clauses (roster balance → 2.6; per-charge state
→ 2.5; waiver names its charge → 2.3/2.4; copay names its visit / `appointmentRef` trap → 2.1/2.2). Nothing widened;
2.2 is the write-path half of the fourth clause, not an adjacent mechanism.

## 6. Build note (2026-09-14, `0771d77e`)

Built as briefed, three increments + one cold review + one fix round. Live on :7799 with clinic-ledger 0.5.0
diff-applied (`make refresh-clinic`): `/api/staff/arrears` names Riley Chen `2500 · 21 days overdue`; the picker
reads "Riley Chen — owes $25.00 · 21 days overdue"; the 09-14 "Fee reversal (corrected)" line reads "reverses
Late-cancellation fee of Sep 13"; a 1¢ copay posted with `visitRef` against the completed 09-15 visit projects
`appointmentKey` + `visitStartsAt` with `settlesFee` false and was waived back naming it (`reversesKey` projected,
both lines "settled"); `appointmentRef` against that same fee-less visit was refused `NoFeeToSettle`.

**Deviations from §2 (amended above where they stand):** the review added three op refusals the design had left at
alive-validation — `WrongPatient` (visitRef), `WrongAccount` + the self-leg `AuthDenied` (reversesRef) — and
`InvalidArgument` for `appointmentRef` on a credit; the visit picker dropped its start-time clock; the Waive picker
pre-selects a lone open charge. `internal/testutil/read_drift_baseline.txt` records the `forPatient` follow-up as a
walk-resolved `(e)` read no dispatcher can name.

**Review classification (one cold pass, 5 SHOULD + 3 NIT, all fixed):** S3/S4 — design-gap, `_packages.md` (a
mirrored optional ref kept the precedent's liveness check and dropped its ownership check; the self leg reached a
field added for the staff form); S2 — design-gap, `vertical-apps.md` (an FE courtesy filter narrower than the op's
predicate excluded the primary flow); S5 — design-gap, FE default; S1/N1/N2 — convention / implementation.
