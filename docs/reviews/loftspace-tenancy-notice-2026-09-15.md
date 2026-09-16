# LoftSpace — a tenant gives notice, a lease ends early (2026-09-15)

**Filed (PO, 2026-09-15, `fdc1cdde`):** "`EndTenancy` refuses `NotYetEnded` ahead of `leaseEnd` and nothing records a
move-out date, so a tenant who leaves mid-term is billed by the rent clause to `validUntil` and the unit stays `leased`.
A recorded notice / early end (tenant self, landlord `manages`) shortens the clause's term (`SupersedeClause`), ends the
tenancy at the recorded date, relists." ★★ L. Winston-adjudicated (implementation-level; no contract surface, no fork:
every mechanism is package-owned and mirrors a shipped pattern — the recorded-lapse timer, the `manages` / `applicationFor`
self probes, `BackfillClauseTerm`'s in-place term write).

## Grounding

- **The term's end is already a recorded fact with a timer on it.** `tenancyEnd`
  ([tenancy_end_lenses.go:147-174](../../packages/lease-signing/tenancy_end_lenses.go)) arms `freshUntil = leaseEnd`,
  opens `missing_tenancyEnded` once the recorded lapse reaches it with no open renewal, and `missing_relist` once
  `endedAt` is set; `EndTenancy` ([scripts.go:1505-1575](../../packages/lease-signing/scripts.go)) records
  `endedAt = leaseEnd`, refuses `NotYetEnded` before it, reads `.tenancy` as a REQUIRED declared read
  ([tenancy_end_targets.go:46](../../packages/lease-signing/tenancy_end_targets.go)). Every consumer of an ended term
  (the applicant gaps, `renewalComplete`'s open gate, `SignRenewal`'s `TenancyEnded`, the cards) keys on `endedAt`,
  not on `leaseEnd` — so an early end that lands as `endedAt = moveOutAt` is terminal everywhere for free.
- **The rent clause is termed `[termStart, leaseEnd)` and bills fail-closed on ONE recorded value.**
  `leaseRentSettlement.missing_clause` mints it with `validFrom: termStart, validUntil: leaseEnd`
  ([lenses.go:179-209](../../packages/semantic-contracts/lenses.go), [targets.go:145-150](../../packages/semantic-contracts/targets.go));
  `clauseSatisfaction` bills only while `periodStart < validUntil` where `periodStart = coalesce(.status.chargeValidUntil,
  validFrom)` — the recorded NEXT due ([lenses.go:344, 362-368](../../packages/semantic-contracts/lenses.go)); `DebitAccount`
  refuses `TermExhausted` at `period_start >= validUntil`, caps the final `periodEnd` at `validUntil` and marks the clause
  `completed` ([loftspace-ledger/scripts.go:1420-1431, 1504-1527](../../packages/loftspace-ledger/scripts.go)). Neither
  reads `.status.state`; **`validUntil` is the only lever that stops billing**, and shortening it is read by both.
- **`SupersedeClause` is the wrong lever (the filing's mechanism is solution-shaped).** It mints a FRESH clause with
  `.status = {state: active}` and no `chargeValidUntil` ([scripts.go:280-312, 233](../../packages/semantic-contracts/scripts.go)):
  the replacement's `periodStart` falls back to `validFrom`, its `freshnessExpiry` marker is empty, so the timer re-arms
  at the term's START and the first period bills AGAIN — the dossier's "a recorded value is read as the fact it records"
  class, on a fresh vertex. `BackfillClauseTerm` is the precedent that edits `.terms` IN PLACE and completes the clause
  when the recorded due reaches `validUntil` ([scripts.go:380-409](../../packages/semantic-contracts/scripts.go)); the
  shortening op mirrors it.
- **Two self probes exist for a leaseapp-targeted op.** Tenant: the deterministic
  `lnk.leaseapp.<app>.applicationFor.identity.<actor>` read on `authContextTarget` (`SetApplicantProfile`,
  [scripts.go:1291-1300](../../packages/lease-signing/scripts.go), declared `{actor:id}` at
  [permissions.go:676](../../packages/lease-signing/permissions.go)). Landlord: `require_manages(leaseapp_unit(app))`
  ([scripts.go:490-531](../../packages/lease-signing/scripts.go)) — binds only the platform-validated self path.
  `WithdrawLeaseApplication` carries the operator `any` + consumer `self` grant pair
  ([permissions.go:125-134](../../packages/lease-signing/permissions.go)).
- **A renewal is the other writer of `leaseEnd`.** `SignRenewal` rewrites `.tenancy` wholesale
  ([renewal_scripts.go:461-467](../../packages/lease-signing/renewal_scripts.go)) and refuses `TenancyEnded`;
  `leaseExpiry.missing_renewalCycle` opens the cycle at `renewalOpensAt` ([renewal_lenses.go:190-202](../../packages/lease-signing/renewal_lenses.go));
  `tenancyEnd` holds the end under an OPEN cycle (`openRenewalCount = 0`). A notice is the tenant's answer to the
  renewal question — every one of these legs must read it (the dossier's "leg" class).
- **Live:** stack up, `bin/loftspace-app` :7788; Jordan Ellis renewed to 2027-09-06 at $2,125; Priya Raman approved
  (`b56bc0f0` / `c44e1f44` Done entries). No `.notice` aspect exists on any leaseapp (new aspect, no migration).

## Decisions (Winston)

1. **A notice is a recorded fact on the leaseapp: `.notice` = `{moveOutAt, givenAt, givenBy}`** (class `tenancyNotice`),
   written once by **`GiveNotice{leaseAppKey, moveOutDate}`** (lease-signing). Its own aspect, not a `.tenancy` field:
   `.tenancy` already has two whole-aspect writers (`DecideLeaseApplication`, `SignRenewal`) and a third would put the
   notice under `SignRenewal`'s rewrite. `moveOutDate` is a bare `YYYY-MM-DD` or an RFC3339 instant; the recorded `moveOutAt` is a DATE-ONLY fact —
   the instant's UTC calendar day at midnight UTC (an offset instant is read as its UTC day, never stored mid-day: a
   mid-day `validUntil` would bill a whole rent period for a few hours); `givenBy ∈ {tenant,
   landlord, operator}` from which probe admitted the caller; `givenAt = submittedAt`.
   Refusals (state codes, each a `refusal-courtesy` at every dispatch site): `NoTenancy` (no `.tenancy` — not an
   approved lease), `LeaseNotSigned` (no `.signature`), `TenancyEnded` (`endedAt` set), `NoticeAlreadyGiven` (`.notice`
   exists — a change of date is out of scope), `MoveOutBeforeToday` (`moveOutAt` < the UTC calendar day of `submittedAt`),
   `MoveOutBeforeStart` (`moveOutAt <= leaseStart`), `MoveOutAfterEnd` (`moveOutAt >= leaseEnd` — the term ends on its
   own date; nothing to record). Same-day move-out is admitted (the tenant who already left records today).
   Grants: operator `any` + consumer `self`; in-script the self path admits the **tenant** (declared-optional
   `applicationFor` link keyed on the actor, present ⇒ tenant) else the **landlord** (`require_manages` off the
   `appliesToUnit` walk) else `AuthDenied`. Under an OPEN renewal cycle the notice is admitted (the tenant declines by
   leaving); the cycle CLOSES on the notice (§4) and its assigned `SignRenewal` task is cancelled.
2. **The term's effective end is `termEnd = CASE WHEN moveOutAt <> null AND moveOutAt < leaseEnd THEN moveOutAt ELSE
   leaseEnd END`, carried in BOTH languages.** `tenancyEnd` arms `freshUntil = termEnd`, opens `missing_tenancyEnded` on
   `lapsedAt >= termEnd AND (openRenewalCount = 0 OR moveOutAt <> null)` — a notice overrides the open-cycle hold —
   and projects `termEnd` / `moveOutAt`. `EndTenancy` reads `.notice` as a declared OptionalRead (descriptor, target
   `row.entityKey.notice`, every dispatcher) and records **`endedAt = termEnd`**, `NotYetEnded` against `termEnd`. Both
   copies are pinned over one fixture set (notice before / at / after `leaseEnd`, no notice).
3. **The rent clause is shortened in place by `ShortenClauseTerm{clauseKey, leaseAppKey}`** (semantic-contracts,
   operator `any`, the `BackfillClauseTerm` shape): reads the clause + `.terms` + the lease + `.notice` (required),
   `.status` (optional-hydrated, `InvalidState` if absent), verifies the clause governs THIS lease off its own `governs`
   walk, refuses `NotTermed` (no `validFrom`), no-ops (empty mutations, the `EndTenancy` idempotent shape) when
   `validUntil <= max(moveOutAt, validFrom)`, else writes `.terms` with **`validUntil = max(moveOutAt, validFrom)`** (every other field
   kept) and marks `.status` `completed` when the recorded due (`chargeValidUntil`, else `validFrom`) `>=` the new
   `validUntil`. A term collapsed to `validFrom` is the recorded fact that the clause bills nothing (a renewal clause
   whose term starts after the move-out); the mint rule `validUntil > validFrom` is a mint-time argument check and its
   DDL text says so. **The period containing the move-out is billed whole** — rent is monthly, no proration; a period
   billed AFTER the shortening records the capped `validUntil` as its `periodEnd` (the statement reads "covers … –
   <move-out>"); a period billed before the notice keeps the `periodEnd` it recorded, and the statement's "Lease ends
   <date> (notice given)" line is what tells the tenant.
   Dispatched by a new `leaseRentSettlement` gap **`missing_termShortened`**: `overrunClauseKey = max(CASE WHEN monthly AND
   unconditioned AND validFrom <> null AND moveOutAt <> null AND validUntil > moveOutAt AND validUntil > validFrom AND
   status.state <> 'completed' THEN c.key)`, one clause per pass (the `missing_term` idiom) — the last two conjuncts are
   what lets a collapsed or already-completed clause DROP OUT, so the gap closes and `max()` reaches the next
   overrunning clause (without them a collapsed renewal clause re-opened the gap forever and could starve the original
   clause — caught cold by two reviewers); the op's own no-op guard is `validUntil <= max(moveOutAt, validFrom)` and it
   never re-stamps a recorded `completedAt`, `Params {clauseKey: row.overrunClauseKey, leaseAppKey: row.entityKey}`. Every
   clause on the lease — original and renewal — converges alone.
4. **Every leg that reads the renewal question reads the notice.** `SignRenewal` refuses **`NoticeGiven`** (`.notice`
   an OptionalRead at its descriptor — the renewal target's `signRenewal` leg is an `assignTask`, which carries no
   OptionalReads, so the descriptor is the task-completion declaration — the FE reads the descriptor, and every seed
   declares it). The refusal reads a key `SignRenewal` never writes, so `GiveNotice` also re-stamps `.tenancy`
   UNCHANGED under OCC (`expectedRevision` = the hydrated revision): the two ops serialize on `.tenancy`'s revision
   instead of racing past each other's hydration. `renewalComplete`'s `open` conjoins `noticeMoveOutAt = null` (the
   notice answers the renewal question — the planner stops assigning legs toward a signature the op refuses) and
   `staleUserTasks` cancels a `SignRenewal` task on a lease under notice or ended; `leaseExpiry.missing_renewalCycle`
   conjoins `moveOutAt = null` and `freshUntil` goes null on a notice (no cycle to open); `renewalsRead` and both
   application read lenses project `noticeMoveOutAt / noticeGivenAt / noticeGivenBy` (snake_case where the lens's
   siblings are). The applicant gaps stay keyed on `endedAt` (a notice is not an end). **Café reads the recorded end, not
   `leaseEnd`:** `endedAt = leaseEnd` was an invariant that made `leaseEnd` a faithful proxy in café's `OpenTab`
   `TenancyEnded` guard and the `cafeLeaseWorkplaces` / `frontDeskLeaseDetails` lenses; an early end breaks the proxy,
   so those read `.tenancy.endedAt` first (the same declared aspect) and the café app's `tenancyEnded` reads it.
5. **FE (`cmd/loftspace-app`).** Tenant lease card: **Give notice** (date, min = the later of today UTC and the day after `leaseStart`, max = the day before
   `leaseEnd` — the script's three refusals mirrored — confirm) → after: "Notice given <date> · moving out <date>"; the renewal card hides Renew/Sign and says
   why (`renewalReady`, `renewalTaskStale` → `notice`). Landlord application card of a live lease: **End lease early**
   (same op, same form) and the "Notice · moving out <date>" chip on the row, the by-unit console and search. Dates are
   date-only facts: rendered by `fmtUTCDate`, the form value sent as `YYYY-MM-DD`. Facet: `GiveNotice` descriptor
   (`AuthContext: self`, `x-entityRef` leaseapp, `moveOutDate` `format: date`) with `(facet)` courtesy lines.
6. **Proof.** Package tests per op; goja + cypher pins of `termEnd`; `internal/leaseconvergence` gains
   `TestLeaseConvergence_NoticeEndsTheTenancyEarly` (leaseshortwindow): approve + sign, `GiveNotice` for a date inside
   the term, the overdue `@at`, `MarkExpired`, `EndTenancy` once with `endedAt = moveOutAt`, the relist, the clause
   shortened + completed, `SignRenewal` refused `NoticeGiven`. Live: Jordan Ellis gives notice through the running
   app; the tenancy ends, the unit relists, the clause completes, the ledger shows the last period.

## Alternatives rejected

| alternative | why not |
|---|---|
| Delete the thing — let the tenant simply stop paying / the landlord relist by hand | The clause keeps billing to `validUntil` and arrears reminders fire (`c44e1f44`); "Relist" is only offered on an ENDED tenancy. The filed harm is the billing. |
| `SupersedeClause` with a shorter `validUntil` (the filing's mechanism) | Fresh vertex, no recorded due, empty marker ⇒ re-bills period 0 (grounding §3). |
| `clauseSatisfaction` reads the lease's `.notice` through `governs` and stops at `min(validUntil, moveOutAt)` | Two readers (`DebitAccount` reads `.terms` from state, not the lease) would carry the rule in two places; one recorded `validUntil` is what both already read. |
| Notice as a field on `.tenancy` | Third whole-aspect writer under `SignRenewal`'s rewrite; a separate aspect has one writer. |
| Prorate the final period | New ledger arithmetic; the PO filed billing PAST the move-out, not the last month's amount. Filed nothing — a PO row if observed. |
| A minimum notice period | No product rule filed; the recorded date is the rule. |

## Fire brief (build note, 2026-09-15)

**1. Scope sentence** — verbatim above: a recorded notice / early end (tenant self, landlord `manages`) shortens the
clause's term, ends the tenancy at the recorded date, relists. Green bar: the Decisions' proof (§6).

**2. Verified touch-list** (live at `53af02d4`):
- `packages/semantic-contracts/scripts.go:280-456` (`SupersedeClause`, `BackfillClauseTerm` → add `ShortenClauseTerm` after
  Backfill), `lenses.go:179-209` (`leaseRentSettlementSpec` + BodyColumns `:61-62`), `targets.go:110-167` (the
  leaseRentSettlement target), `ddls.go:41,60-65,82-97,289-292` (PermittedCommands, term text, clauseKey field, the
  `validUntil > validFrom` sentence), `permissions.go` (grant + OpMeta), `manifest.yaml` + `package.go` Version, tests
  beside `clause_term_test.go`.
- `packages/lease-signing/scripts.go:1505-1575` (`EndTenancy`), `:633-650` (`moveInDate` normalization to mirror),
  `:1291-1300` (tenant probe), `:490-531` (`require_manages`), `tenancy_end_lenses.go:38-40,147-174`,
  `tenancy_end_targets.go:46`, `renewal_scripts.go:436-467` (`SignRenewal`), `renewal_lenses.go:190-202` (`leaseExpiry`),
  `:438-457` (`renewalsRead`), `lenses.go:1112,1466-1512` (both app read lenses), `ddls.go:105,204-221` (PermittedCommands +
  aspect DDLs), `permissions.go:125-134,221-224,536-573` (grants + EndTenancy OpMeta), `manifest.yaml` + `package.go`,
  tests beside `end_tenancy_ops_test.go` / `tenancy_end_lens_test.go` / `renewal_ops_test.go`.
- `scripts/verify-package-lease-signing.go:130` (ddlCheck ops list).
- `internal/leaseconvergence/tenancy_end_convergence_test.go` (new vector).
- `cmd/loftspace-app/web/app.js:1928,2148,2572,3200-3318,4443-4473` (tenant + landlord cards), `renewals.go:41`,
  `applications.go:54`, `unit_applications.go:43,89`, `applicationsource.go:75` (row structs), `lease_term_ui_test.go`,
  `rival_task_ui_test.go` (goja pin shapes), `search_columns_test.go`.

**3. Precedents.** `BackfillClauseTerm` (in-place `.terms` + `.status` completion; `governs` walk page 8);
`missing_term`'s `max(CASE …)` one-per-pass dispatch; `EndTenancy` (required declared read from `state`, idempotent no-op,
OCC pin on the hydrated revision); `SetApplicantProfile` (tenant self link) + `DecideLeaseApplication` (landlord
`require_manages`); `CreateLeaseApplication` `moveInDate` normalization; `RecordApplicationLoss` (a third terminal fact
every consumer reads); the `tenancyEnd`/`leaseExpiry` recorded-lapse idiom; `fmtUTCDate` + `lease_term_ui_test.go` for
date-only rendering; `refusal-courtesy` lines at every FE site + `(facet)` lines in the OpMeta.

**4. Increments + green checks.**
- **Inc 1 (semantic-contracts, sonnet):** `ShortenClauseTerm` + `missing_termShortened` + target + DDL/OpMeta/grant +
  version bump. `go test ./packages/semantic-contracts/ -count=1`; `STRICT=1 go run ./scripts/lint-gap-column-declaration.go`;
  `lint-opmeta-required-fields`; `lint-derive-reads-bare-vector`; `lint-package-version`.
- **Inc 2 (lease-signing, opus — new aspect + timer-gate changes + a new refusal on `SignRenewal`):** `GiveNotice`,
  `.notice` DDL, grants + OpMeta, `termEnd` in `tenancyEnd` + `EndTenancy`, `leaseExpiry` conjunct, `SignRenewal`
  `NoticeGiven`, three read lenses, verify script, version bump. `go test ./packages/lease-signing/ -count=1`;
  `lint-seed-declared-reads`; `lint-refusal-courtesy` (will fail until Inc 3 declares the FE sites — expected);
  `go test ./internal/refractor/ -run 'Corpus|Census'`.
- **Inc 3 (leaseconvergence e2e + FE, sonnet after 1+2):** the convergence vector under `-tags leaseshortwindow`
  (`make test-lease-convergence`); the FE + courtesy + goja pins; `go test ./cmd/loftspace-app/ -count=1`;
  `lint-refusal-courtesy`, `lint-app-op-descriptors`, `lint-markup-escaping`, `lint-stale-render-guard`.
- **Close:** `go build ./... && make vet && golangci-lint run ./... && gofmt -l scripts/`; every `scripts/lint-*.go`
  STRICT; `go test ./packages/... ./cmd/loftspace-app/... ./internal/refractor/... ./internal/leaseconvergence/...`;
  `make refresh-loftspace` + `make verify-package-lease-signing` on the live stack; rebuild + cycle `bin/loftspace-app`;
  live proof as Jordan Ellis.

**5. In-scope gotchas.** Package edits bump manifest + `Version` (both packages). A new `missing_*` column must be
declared in the target's gaps (`lint-gap-column-declaration`). Every dispatcher of `EndTenancy` and `SignRenewal` gains
the `.notice` OptionalRead (targets, descriptors, tests, seeds — `lint-seed-declared-reads`). `EndTenancy`'s `.tenancy`
stays a REQUIRED read from `state`; `.notice` is optional but its ABSENCE on a submitter that never declared it is the
undeclared case — the target + descriptor declare it, and a test submits WITHOUT it to pin the (documented) fallback to
`leaseEnd`. No `$now` in any cypher — "today" is the op's `submittedAt` and the lens compares recorded stamps. The FE
dates render by UTC slice; the refusal texts slice the same stamp (`[:10] + " (UTC)"`). No `// Story …` / history
comments. Dossier entries walked (from `_packages.md` + `vertical-apps.md`): *a recorded value is read as the FACT it
records* (the SupersedeClause trap, §3); *the "leg" of a guard is every op that WRITES the guarded value and every
conjunct that READS it* (`leaseEnd`'s writers: Decide, SignRenewal; readers: tenancyEnd, leaseExpiry, EndTenancy);
*a consumer's exclusion is grounded in the predicate that ENFORCES it* (the open-renewal override names
`SignRenewal`'s `NoticeGiven` refusal); *a mirror that drops a precedent's branch drops its invariant* (Backfill's
`completed` mark + `InvalidState` on an unhydrated `.status` both kept); *a dispatch declaration must name what the
runtime binds* (`row.overrunClauseKey` is non-null whenever the gap opens); *a new terminal state is a census of every
status switch and render gate* (the notice reaches the banner, the terms panel, `applicationStatus`, the decide gate,
the renewal card, the by-unit console, search); *two courtesy surfaces name the same instant in different zones*;
*a count the FE promises applies the op's own predicate* (the date input's min/max mirror the script's refusals).
Standing checklist: new state's LIFETIME — `.notice` is written once, never reset, carried through `SignRenewal`
(refused) and `EndTenancy` (read), tombstoned with the leaseapp; every census above re-run live; a negative test's
positive vector first, fixes proven by revert; one deterministic key one writer (`.notice` ← `GiveNotice` only);
precedent debt checked (`BackfillClauseTerm`'s `{"op":"update"}` writes carry no `expectedRevision` — the shortening
op pins `.terms` to the hydrated revision, the `EndTenancy` shape).

**6. Adjacent finds.** None filed at scoping. (Proration and a minimum-notice rule are product rules the PO did not
file — noted in the alternatives table, not rows.)

**7. Non-goals.** Changing a recorded notice; proration; a notice period; the deposit row (its return rides this
`endedAt`); café/wellness leases (the `TenancyEnded` café refusal reads `endedAt`, which this design sets — no edit).

### Build note (2026-09-15)

Shipped `6e6ea0f3` (merge of `0c13785e`; CI green on the re-run — the first run failed
`TestRenewalConvergence_InflightIsLegScopedAcrossTheStaticCheck`'s two-mark-lease `Never` window while the bridge nak'd a
PII envelope for 8 s under host load; 3/3 local reruns + two full local suite runs green; no change of this fire touches
the bgcheck dispatch that window counts); brief `2c4b369f`. Live on the shared stack (lease-signing 0.40.0,
semantic-contracts 0.6.0, cafe-domain 0.15.1, front-desk 0.4.1 diff-applied; `bin/loftspace-app` / `bin/cafe-app`
cycled): Jordan Ellis's renewed lease (`MhZY2unHEAhNv61HUpb9`, ends 2027-09-06) took `GiveNotice` for 2027-01-31 as the
operator; within a second `leaseRentSettlement.missing_termShortened` dispatched `ShortenClauseTerm` and the renewal
clause `BS9KrFCi1f6qKTHywm7E` reads `validUntil 2027-01-31T00:00:00Z` (still active — its recorded due 2026-10-06 bills
Oct–Jan, then completes); `tenancyEnd` re-armed `freshUntil = termEnd = 2027-01-31`; `renewalsRead` and both application
read models carry `notice_move_out_at 2027-01-31 · given_by operator`; the tenant home reads "Notice given · moving out
Jan 31, 2027". The end + relist path is proven by the e2e (same-day move-out), not live — ending a showcase tenant's
lease today was not the PO's ask.

Ops lesson (not a defect of this build): a protected read lens that gains a column pauses `structural` the moment the
package installs and LATCHES after three self-heal probes (~30 s) if `make provision-readpath` has not run yet —
`renewalsRead` and `leaseApplicationsRead` latched, `landlordLeaseApplicationsRead` recovered on its third try.
`lattice lens resume <id> --actor <loupe operatorActorKey>` clears a latch (the primordial admin lacks the control
grant). Run `provision-readpath` in the same breath as the install.

Deviations from the brief (all folded into the body above): the self-path predicate is `authTargetValidated AND
authContextTarget == actor`; `moveOutAt` is truncated to its UTC calendar day; `GiveNotice` re-stamps `.tenancy`
unchanged under OCC (the `SignRenewal` race); `renewalComplete` closes and `staleUserTasks` cancels on a notice; the
renewal target's `assignTask` leg carries no OptionalReads (descriptor-declared); `missing_termShortened` conjoins
`validUntil > validFrom AND state <> completed`; café's `OpenTab` guard + both lease lenses + `seed-classic-demo` read
`endedAt` first; `lint-app-op-descriptors`' loftspace ceiling 18 → 19; the by-unit console joins the notice off the
landlord read model (`leaseApplicationComplete` projects none); the e2e installs loftspace-ledger + semantic-contracts
and proves the clause shortened + completed, the earlier-instant re-arm, and the cycle closing on the notice.

Review classification (three cold layers over the whole diff, one fix round): **design-gap** — the collapsed-clause
livelock (the design's own conjunct; two reviewers), the advisory `NoticeGiven` race, the planner running under a
notice (`_packages.md`: a new "advisory refusal" class displacing the closed taxonomy-migration entry; sightings on the
cadence, "leg" and recorded-fact entries); **brief-gap** — café's `leaseEnd` proxy (the census stopped at LoftSpace;
recorded-fact sighting), the date-only instant; **implementation-bug** — the FE's one-hat declared read
(`vertical-apps.md` sighting); **review-over-reach** — the "at leaseEnd" fixture row (inert in both languages, kept
with the reason stated). Adjacent finds: none open — the seed's proxy read was fixed in the fire.
