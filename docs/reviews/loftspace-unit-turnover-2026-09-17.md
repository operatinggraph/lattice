# LoftSpace — a unit turns over cleanly: availability follows the recorded end, a terminal application frees the pair (2026-09-17)

**Filed (PO, 2026-09-16, `16af6c9d`):** "`missing_relist` flips status only
([tenancy_end_targets.go:54](../../packages/lease-signing/tenancy_end_targets.go)); `availableFrom` keeps the old
move-in and a bare application is approved from it ([scripts.go:982](../../packages/lease-signing/scripts.go)) — a
tenancy starting before the last ended, back-billed. Live: 50 Riverside Walk reads 2025-09-06 under a 2027-01-31
notice. Relist stamps `endedAt`, a notice markets from `moveOutAt`, approval floors it." ★★ M, pkg + FE.

**Filed (Winston, 2026-09-17, `ddc31a04`, out of the residence-spine build):** "The `(applicant, unit)` guard link
`lnk.identity.<a>.appliedToUnit.unit.<u>` is freed only by `WithdrawLeaseApplication` and `ReassignLeaseUnit`
([scripts.go:640](../../packages/lease-signing/scripts.go)); `EndTenancy`, `DecideLeaseApplication{declined}` and
`RecordApplicationLoss` leave it alive, so `CreateLeaseApplication` refuses `DuplicateApplication` forever. A
terminal application frees the pair (the withdraw precedent)." ★★ S, pkg.

One fire: both rows are `packages/lease-signing` turnover mechanics on the same anchor (`tenancyEnd` / the leaseapp's
terminal states), and the second is what lets the first's re-applicant exist. Winston-adjudicated (implementation-level;
no contract surface, no fork: a new loftspace-domain op mirrors `SetListingStatus`, a new `tenancyEnd` gap mirrors
`missing_relist`, the guard release mirrors `ReassignLeaseUnit`'s vacated-pair tombstone).

## Grounding (verified live 2026-09-17)

- **The listing's `availableFrom` has exactly one writer, and it is the landlord.** `SetListing` stores it verbatim
  ([ddls.go:436](../../packages/loftspace-domain/ddls.go)); `SetListingStatus` rewrites `status` alone and copies
  every other field ([ddls.go:482-529](../../packages/loftspace-domain/ddls.go)) — and is a **no-op when the status
  already matches** (line 518), so the relist gap (`unitStatus = 'leased'`,
  [tenancy_end_lenses.go](../../packages/lease-signing/tenancy_end_lenses.go)) cannot carry a date: an already-available
  unit (50 Riverside Walk today) is never re-dispatched. The date is projected by three lenses
  (`availableListingsSpec` [lenses.go:262](../../packages/loftspace-domain/lenses.go); `leaseApplicationsRead` /
  `landlordLeaseApplicationsRead` [lenses.go:1156, 1519](../../packages/lease-signing/lenses.go)) and rendered as
  "available <date>" on the Browse card ([app.js:1747](../../cmd/loftspace-app/web/app.js)); Browse lists every
  status and offers Apply only on `available`.
- **The term's end is a recorded fact the lens already computes.** `tenancyEnd` projects `endedAt`, `moveOutAt` and
  `termEnd = CASE WHEN moveOutAt < leaseEnd THEN moveOutAt ELSE leaseEnd END`, reads `u.listing.data.status` off the
  same `.listing` aspect, and guards every unit-fact gap with `unitKey <> null` + `otherLiveTenancyCount`
  ([tenancy_end_lenses.go](../../packages/lease-signing/tenancy_end_lenses.go)). `EndTenancy` records `endedAt =
  termEnd` ([scripts.go:1562-1649](../../packages/lease-signing/scripts.go)); `GiveNotice` records `moveOutAt` as a
  midnight-UTC instant. The engine orders strings lexically and answers FALSE on a null side
  ([values.go:170-200](../../internal/refractor/ruleengine/full/values.go)); `availableFrom` may be a bare
  `YYYY-MM-DD` (seed data) or an RFC3339 instant (the FE sends `T00:00:00Z`).
- **Approval commits to the applicant's reviewed terms — never clamped.** `DecideLeaseApplication`'s approve arm takes
  `move_in = .terms.moveInDate else listing.availableFrom` and the comment records the 2026-09-14 decision: "never
  silently clamped to the listing's `availableFrom`" ([scripts.go:934-1004](../../packages/lease-signing/scripts.go)).
  The `.listing` read there is a class-(e) follow-up off `leaseapp_unit()`; `.terms` is a declared OptionalRead.
  `CreateLeaseApplication` normalizes `moveInDate` and already reads `unit.listing` as a declared OptionalRead
  ([scripts.go:668-700](../../packages/lease-signing/scripts.go)); `BackfillLeaseTerms` carries an existing
  `moveInDate` through, never mints one. Seeds and the convergence harness apply at or after `availableFrom`
  (`seed-showcase.go:1158/1167`, `tenancy_end_convergence_test.go:81/43`).
- **The guard link's lifecycle.** `CreateLeaseApplication` creates or revives `lnk.identity.<a>.appliedToUnit.unit.<u>`
  and refuses `DuplicateApplication` while it is alive ([scripts.go:640-649](../../packages/lease-signing/scripts.go));
  `WithdrawLeaseApplication` tombstones it unconditioned ([scripts.go:1181-1186](../../packages/lease-signing/scripts.go));
  `ReassignLeaseUnit` resolves the applicant off the app's OWN `applicationFor` link (a `(e)` enumeration), reads the
  vacated pair's guard as a `(e)` follow-up and tombstones it under OCC
  ([scripts.go:1236-1280](../../packages/lease-signing/scripts.go)). No lens walks `appliedToUnit`; the only readers are
  the create/reassign refusals. `EndTenancy` walks nothing today; `DecideLeaseApplication` walks `appliesToUnit`
  (`leaseapp_unit`); `RecordApplicationLoss` walks `appliesToUnit`. Dispatchers: Decide — the staff FE, the landlord
  self FE, Facet; EndTenancy — the `tenancyEnd` playbook (+ operator by hand); RecordApplicationLoss — the
  `leaseApplicationComplete` playbook (+ operator).
- **Live:** stack up, `bin/loftspace-app` :7788. 50 Riverside Walk: `availableFrom = 2025-09-06`, Jordan Ellis under
  notice `moveOutAt = 2027-01-31`. lease-signing 0.42.0, loftspace-domain 0.14.0.

## Decisions (Winston)

1. **Availability is FLOORED at the recorded end, by a new loftspace-domain op `FloorListingAvailability{unit,
   availableFrom}`** (operator `any`; Weaver's service actor reaches it on the standing path exactly as
   `SetListingStatus`). Reads `unit` + `unit.listing` (declared at the playbook); refuses `NoListing`; computes
   `floored = max(instant(existing.availableFrom), instant(payload.availableFrom))` with the bare-date → midnight-UTC
   normalization (`as_rfc3339_instant` + `time.rfc3339_utc`, the lease-signing idiom) and **writes `.listing` with
   `availableFrom = floored` and every other field verbatim when `floored` differs from the stored STRING**, else the
   empty no-op (the `SetListingStatus` idempotent shape). Writing on string inequality — not instant inequality — is
   load-bearing: the lens compares strings, and a bare `2027-01-31` reads below `2027-01-31T00:00:00Z`, so an
   instant-equal no-op would leave that gap open forever; the rewrite to canonical form closes it in one pass.
   Monotone-max is order-free: every ended tenancy on a unit floors to its own end and the unit converges to the
   latest, and a landlord's LATER date is never lowered; a date set BELOW the recorded end is re-raised on every
   evaluation (changing a notice date is out of scope, §Non-goals). **`SetListing` validates and normalizes
   `availableFrom` at the mint** (`required_instant`: an RFC3339 instant or a bare `YYYY-MM-DD`, stored as the
   canonical UTC instant, else `InvalidArgument`) — the field is load-bearing for a Weaver gap, and a free-text
   value would wedge it (the dossier's validate-at-the-mint class, third sighting).
2. **One new level-triggered gap on `tenancyEnd`, `missing_availabilityFloored`:** the lens projects
   `unitAvailableFrom = u.listing.data.availableFrom` and `marketFrom = coalesce(endedAt, CASE WHEN moveOutAt <> null
   THEN termEnd ELSE null END)` — an ended term markets from its recorded end, a live term under notice from the
   notice, a live term with no notice from nothing (exactly the filing's two clauses; a bare `leaseEnd` is a renewal
   question, not an availability). Gap: `(marketFrom <> null) AND (unitKey <> null) AND (unitAvailableFrom < marketFrom)`
   — the null-side FALSE rule keeps a unit with no listing closed; `marketFrom <> null` and `unitKey <> null` are the
   `Params` conjuncts (`lint-gap-column-declaration`). Playbook: `directOp FloorListingAvailability{unit: row.unitKey,
   availableFrom: row.marketFrom}`, `Reads: [row.unitKey, row.unitKey.listing]`. Independent of status: an
   already-available unit (the live case) is floored without a relist; the relist gap is untouched.
3. **Approval REFUSES a move-in before availability — `MoveInBeforeAvailable` — it does not clamp.** The filing's
   verb ("approval floors it") would silently rewrite the reviewed terms the 2026-09-14 decision made binding; a
   refusal keeps the approval a commitment to what the landlord read. In `DecideLeaseApplication`'s approve arm, after
   `lease_start` is derived: when `.terms.moveInDate` supplied it AND `lease_start` falls on an earlier **UTC calendar
   day** than `availableFrom` (both canonicalized, compared on `[:10]`) → `fail("MoveInBeforeAvailable: …")`. A day,
   not an instant: seeds and Facet's `datetime-local` landlord form store a time-of-day `availableFrom`, and every
   surface (the FE `min`, the refusal text) promises the day. The bare application (`move_in = availableFrom`) is equal
   by construction.
   The same refusal at the terms' WRITER (`CreateLeaseApplication`, when `moveInDate` is supplied and the declared
   `.listing` is present with an `availableFrom`) — the dossier's leg rule; `BackfillLeaseTerms` mints no date. The
   `.listing` read Decide already performs is unchanged in class.
4. **Every terminal state frees the `(applicant, unit)` guard pair.** A shared helper `free_applied_to_unit_guard(app_key,
   unit_key)` mirrors `ReassignLeaseUnit`: applicant off the app's own `applicationFor` link (`(e)` enumeration,
   `LEASEAPP_UNIT_PAGE_LIMIT`), guard key read as a `(e)` follow-up, tombstone under OCC when alive, `[]` otherwise.
   Called by `DecideLeaseApplication` on the **declined** arm only (approved keeps the pair — the executed lease),
   `RecordApplicationLoss` on its recording arm (not the idempotent no-op arm), `EndTenancy` on its ending arm (not
   the already-ended no-op arm). A re-apply after any of them revives the tombstone through the existing
   `make_link_revive_occ` path; the terminal leaseapp itself stays alive and its own read-model rows keep their
   `endedAt` / `decision`. **The other two writers of the pair follow the same rule:** `WithdrawLeaseApplication`
   frees the guard only for an UNDECIDED application (a decided one had its pair freed at the terminal decision, so an
   alive guard belongs to a later application on the same key), and `ReassignLeaseUnit` reads `.decision` + `.tenancy`
   (declared) and on a terminal application re-points the `appliesToUnit` link only — no vacated-pair tombstone, no
   new-pair guard (both caught in review: decline → re-apply → withdraw / reassign the OLD one took the NEW
   application's guard). The ended application's `sameApplicantLiveTenancyCount` / `otherLiveTenancyCount` conjuncts read
   `approved + endedAt = null`, so a re-applicant's fresh approval is the residence design's "re-approval on the same
   unit → revive" case, already pinned.
5. **FE (`cmd/loftspace-app`).** Apply form: `#moveInDate` `min` = the listing's `availableFrom` day (the `cap`
   courtesy for `MoveInBeforeAvailable`); the landlord's approve site declares `none` (the date is the applicant's; the
   refusal toast is the answer); Facet `(facet)` lines on both descriptors. The Browse card's "available <date>" and
   both application lenses render the floored value with no change.
6. **Proof.** Lens pins over one fixture set: ended + stale → `marketFrom = endedAt`, gap TRUE; ended + landlord date
   later → FALSE; live under notice + stale → `marketFrom = moveOutAt`, TRUE; live no notice → FALSE; no listing →
   FALSE; unit tombstoned → FALSE; after the op's write → FALSE (the arm-pin rule). Op tests: floor raises / keeps a
   later date / canonicalizes a bare equal date / `NoListing`; approval refuses `MoveInBeforeAvailable` and admits the
   bare + equal + later cases; create refuses it; each of the three terminal ops tombstones the guard and a re-apply
   is admitted; the no-op arms leave the guard alone. `verify-package-loftspace-domain` counts the new op on both
   DDLs. Live: 50 Riverside Walk floors to 2027-01-31 within a minute of the upgrade (the reactivation re-evaluates
   every retained row).

## Alternatives rejected

- **Delete the thing — drop `availableFrom` from the listing and derive availability in the lenses.** Rejected: the
  date is landlord-authored marketing data (a renovation gap after the end is a legitimate later date) and three
  lenses plus the FE read it as a stored field; a derived column cannot carry the landlord's later date.
- **`SetListingStatus` gains an `availableFrom` param and the relist gap passes `row.endedAt`.** Rejected: the relist
  gap conjoins `unitStatus = 'leased'` and the op no-ops on a matching status, so the live instance (already
  available) is never reached, and a notice (live, leased) has no relist to ride.
- **Clamp at approval (the filing's verb).** Rejected in decision 3: it re-opens the 2026-09-14 "approval ignores the
  reviewed terms" defect from the other side.
- **Floor a live no-notice term to `leaseEnd`.** Rejected: `leaseEnd` is the renewal question's input, not an
  availability; the filing names the two recorded ends and nothing else.
- **Tombstone the terminal leaseapp itself (the withdraw shape).** Rejected: an ended or declined application is the
  record every read lens, the ledger's `heldFor` and the residence release still key on.

## Fire brief (build note, 2026-09-17)

1. **Scope** — the two filed rows above, verbatim, with decision 3's refusal in place of the filing's clamp. Green bar:
   decision 6's pins + `verify-package-loftspace-domain` + live floor of 50 Riverside Walk.
2. **Touch-list (verified live).** `packages/loftspace-domain/{ddls.go:482-529 (new `FloorListingAvailability` arm
   beside SetListingStatus; PermittedCommands :52 + :165; DDL prose), opmetas.go (new OpMetaSpec, operator-only, no
   Facet descriptor — Weaver-dispatched), permissions.go:52 (`mk`), package.go:51 + manifest.yaml (0.14.0 → 0.15.0)}`;
   `scripts/verify-package-loftspace-domain.go:149-152` (both `ops` lists); `packages/lease-signing/{tenancy_end_lenses.go
   (WITH: `unitAvailableFrom`, `marketFrom`; RETURN: both + the gap + the `violating` OR), tenancy_end_targets.go:61
   (new gap), scripts.go (helper after `leaseapp_unit` :525; Decide approve arm after :1001; Decide declined arm;
   Create :668-700; RecordApplicationLoss :1794-1852; EndTenancy :1562-1649), opmetas/permissions (Facet courtesy
   lines on Create + Decide), package.go:117 + manifest.yaml (0.42.0 → 0.43.0)}`; `cmd/loftspace-app/web/app.js`
   (:1785 apply form `min`, :1813 submit site courtesy, the Decide site courtesy); tests per part 6.
3. **Precedents.** Op arm: `SetListingStatus` ([ddls.go:482](../../packages/loftspace-domain/ddls.go)) — read, refuse
   `NoListing`, no-op, `copy_data` + upsert. Gap: `missing_relist` (playbook + lens conjunct shape). Guard release:
   `ReassignLeaseUnit`'s vacated pair ([scripts.go:1236-1280](../../packages/lease-signing/scripts.go)). Instant
   normalization: `as_rfc3339_instant` ([scripts.go:176](../../packages/lease-signing/scripts.go)). Lens pins:
   `tenancy_end_lens_test.go`; op pins: `end_tenancy_ops_test.go`, `record_application_loss_ops_test.go`,
   `reassign_lease_unit_test.go`.
4. **Increments + checks.** (1) loftspace-domain op + verify-script pins → `go test ./packages/loftspace-domain/`.
   (2) lens columns + gap + playbook → `go test ./packages/lease-signing/ -run 'TenancyEnd|Corpus|Census'` and
   `go test ./internal/refractor/ -run 'Corpus|Census'`. (3) `MoveInBeforeAvailable` at Decide + Create, FE `min`,
   courtesy lines → `go test ./packages/lease-signing/ ./cmd/loftspace-app/...`, `STRICT=1 go run
   ./scripts/lint-refusal-courtesy.go`. (4) guard release ×3 + re-apply pins → `go test ./packages/lease-signing/`.
   (5) full gates: `go build ./...`, `make vet`, `golangci-lint run ./...`, every `scripts/lint-*.go` STRICT,
   `DIFF_BASE=main go run ./scripts/lint-package-version.go`, `make test-lease-convergence` (leaseshortwindow),
   `go test ./...`.
5. **Gotchas.** Both manifests + `Version` constants bump (`lint-package-version`). The gap's `Params` columns are
   conjuncts of the gap. The op writes on STRING inequality (decision 1). A lens WITH/RETURN edit is not a MATCH edit,
   but run the corpus census anyway. Every new `fail(` code needs a courtesy line at every JS site + the Facet
   descriptor (`lint-refusal-courtesy`). `lint-seed-declared-reads`: a new refusal on `CreateLeaseApplication` is a
   claim about every seed caller — seeds apply at `availableFrom`. Reads annotations: `(e)` on the enumeration and
   the follow-up guard read; `(a)`/`(d)` only where a dispatcher declares the key. Standing checklist 1–6 walked:
   no new registry (1); the census of `availableFrom` writers is one op, re-run at build (2); every refusal pinned
   with its positive vector, the floor proven by reverting the op's write (3); nothing removed (4); one writer of
   `.listing.availableFrom` beyond the landlord, monotone so no arbitration (5); `SetListingStatus`'s no-op is
   verified against the rule it claims (6). Dossier: *a dispatch declaration names what the runtime binds* (Params
   conjuncts); *for every ARM pin the gap FALSE* (after the floor write; after the landlord's later date); *the leg of
   a guard is every writer* (Create + Decide both refuse; three terminal ops all free); *a refusal that reads a key the
   op never writes* — `MoveInBeforeAvailable` reads `.listing` and writes `.tenancy`; a racing `FloorListingAvailability`
   cannot lower the date, so the window only admits a start the floor would have admitted a moment earlier: accepted,
   stated here.
6. **Adjacent finds.** None filed at scoping.
7. **Non-goals.** Changing a notice date; proration; a landlord-facing "coming available" for a live no-notice term;
   Facet forms for the Weaver-only ops; tombstoning terminal leaseapps.

**Build note (2026-09-17).** Built as briefed with four review-forced amendments, each rewritten into the decisions
above: the refusal compares the UTC calendar DAY (the instant compare refused every seed-showcase application — a
wall-clock `availableFrom` beside a bare same-day `moveInDate` — and Facet's `datetime-local` landlord listing);
`SetListing` normalizes `availableFrom` at the mint; `WithdrawLeaseApplication` frees the pair only for an undecided
application and `ReassignLeaseUnit` re-points a terminal application's link without touching any guard (decline →
re-apply → withdraw / reassign the OLD application took the NEW one's revived guard — the deterministic pair key
records no owner); the Decide test helper binds both declared walks instead of a baseline row. Accepted and stated at
the site: a `Decide{declined}` hydrated before a concurrent loss + re-apply can tombstone the newer guard
(milliseconds, `default` lane > 1 worker); `missing_relist` and `missing_availabilityFloored` on one row both upsert
`.listing` unconditioned — a lost write re-opens its gap. `FloorListingAvailability` carries no `manages`
OptionalRead: it is scope=any only, and `require_manages` returns before its read on the standing path.

**Shipped `b9e02daa` (merge of `4ceedbdd`), CI green, live 2026-09-17.** `reinstall-package` both (loftspace-domain
0.14.0 → 0.15.0, lease-signing 0.42.0 → 0.43.0, no restart); `bin/loftspace-app` rebuilt and cycled. The tenancyEnd
rebuild replays the label-narrowed stream (≈12 s/event on this host), so 50 Riverside Walk's row was reprojected by
hand (`lattice lens reproject W8rrMyB9ktdwm3pEW8rr --actor-key vtx.leaseapp.MhZY2unHEAhNv61HUpb9`): `marketFrom =
2027-01-31T00:00:00Z`, `unitAvailableFrom = 2025-09-06T17:01:31Z`, gap TRUE; Weaver dispatched
`FloorListingAvailability` on the system lane and the listing read `availableFrom = 2027-01-31T00:00:00Z` five
seconds later. `verify-package-lease-signing` 103 OK; `verify-package-loftspace-domain` 98 OK once its role
resolution was fixed (this stack carries a superseded second `operator` role vertex whose grants were tombstoned
2026-08-22; the script pre-resolved `operator` from the bootstrap JSON and then let a `vtx.role.*.canonicalName` scan
overwrite it by map order — the bootstrap id now wins, in the four scripts that shared the scan).

**Review classification (one lead pass + one cold adversarial pass; 2 blocking, 3 should-fix, 4 nits, all fixed).**
Design-gap: day-vs-instant — the grounding checked the seeds' DAY and decision 3 chose instant semantics; the
producers of time-of-day instants (seeds, Facet's date-time control) were asserted safe, not run (sighting appended to
the "recorded value read as the fact it records" entry). Implementation-bug ×2: the two remaining writers of the pair
(Withdraw, Reassign) — the brief's leg walk covered the three terminal ops and not the two ops that already freed the
pair (sighting appended to the "leg of a guard" entry). Convention ×2: `validate at the mint` (fourth sighting →
the date-format sub-rule mechanized, `lint-date-field-normalized`); a class-1 read-drift baseline row copied from a
neighbouring row's debt instead of fixing the fixture. Found and fixed in the same run: the verify scripts' role
resolution (above).
