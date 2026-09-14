# LoftSpace — the lease is signed on the applicant's terms, and a lease that ends frees its unit

**Status:** ✅ **Winston-ratified — build-ready** (Vertical Steward, 2026-09-14). No frozen-contract change, no
architectural fork: every mechanism is package-owned (`packages/lease-signing`, `cmd/loftspace-app`) and mirrors
a shipped pattern (`leaseExpiry`'s recorded-lapse timer, `SetListingStatus`'s cross-package directOp, the
protected read lenses' column set). Three PO rows filed 2026-09-14 (`79ec0365`) build as one fire:

- **R1 — Approval signs a lease on terms the applicant never reviewed** (★★★ M).
- **R2 — A lease that ends without renewal never frees the unit** (★★★ M).
- **R4 — A renewed tenant's home still shows the old lease** (★★ S) — folds in: R1's read-lens columns are the
  projection R4 needs, and R2's ended state lives on the same card.

## 1. Problem (PO-filed, live-observed)

- `DecideLeaseApplication(approved)` stamps `.tenancy {leaseStart, leaseEnd, renewalOpensAt}` from the **unit
  listing's** `availableFrom` + `leaseTermMonths` (`packages/lease-signing/scripts.go` ≈878) and never reads the
  application's own `.terms {moveInDate, leaseTermMonths, requestedRent}`; the rent clause bills
  `coalesce(.tenancy.rentAmount, .terms.requestedRent)` (`packages/semantic-contracts/lenses.go:189`). Every live
  listing is available-from 2026-08-23, so an applicant asking to move in 2026-09-15 is billed from 08-23.
- The only listing flip is `missing_listingLeased → SetListingStatus(leased)` (`targets.go:124`). Nothing records
  that a term ended: past `leaseEnd` with a cancelled (or never-opened) renewal the unit stays `leased`, hidden
  from Browse, with no Relist on the landlord's card, the tenant's card silent, a new applicant refused.
- No tenant-facing lens projects `.tenancy`; the application card shows application-time terms and the Renewed
  card's "term ends" is the renewal's `cycleEnd` — the OLD end.

## 2. Decisions

### 2.1 R1 — `.tenancy` is derived from `.terms`, and records the rent

On the first approve, `DecideLeaseApplication` reads `.terms` (declared `optionalReads` at the descriptor and
every dispatcher — absent is the bare applicant+unit application) and derives:

| field | source | fallback (no `.terms`) |
|---|---|---|
| `leaseStart` | `.terms.moveInDate` | `.listing.availableFrom` |
| `leaseEnd` | `leaseStart + .terms.leaseTermMonths` (calendar months) | `+ .listing.leaseTermMonths` |
| `renewalOpensAt` | `leaseEnd − renewalWindow` (unchanged) | — |
| `rentAmount` | `.terms.requestedRent` | `.listing.rentAmount` (omitted when neither exists) |

- **No clamp to `availableFrom`.** A silent `max(moveInDate, availableFrom)` would re-create the bug (a lease on
  terms the applicant never reviewed). The landlord's decide surface states the terms the approval commits to; the
  approval is the check.
- **`moveInDate` is accepted as an RFC3339 instant OR a bare `YYYY-MM-DD`** (the DDL says RFC3339; the FE
  normalizes to `T00:00:00Z`; `seed-showcase` / `seed-classic-demo` write bare dates and Priya Raman's pending
  application carries one live). A bare date is read as midnight UTC. `time.rfc3339_utc` rejects a bare date, so
  the script normalizes before calling it.
- `rentAmount` on `.tenancy` is the field `SignRenewal` already writes; `leaseRentSettlement`'s
  `coalesce(.tenancy.rentAmount, .terms.requestedRent)` therefore bills the same number it bills today (the
  fallback chain is identical to `CreateLeaseApplication`'s own `requestedRent` fallback), and its
  `coalesce(termStart, leaseStart)` is untouched — an original term still carries no `termStart`.
- `.tenancy` stays **create-only** at Decide (a re-approve never re-derives it).

### 2.2 R2 — the term's end is a recorded fact, and the recorded fact frees the unit

**Mirror `leaseExpiry` exactly** (`renewal_lenses.go` / `renewal_targets.go`): a new `weaver-targets` lens +
target **`tenancyEnd`** (TargetID == lens canonical name == output-key prefix), anchored on every leaseapp,
projecting `freshUntil = leaseEnd` until the leaseapp's `freshnessExpiry.data.byTarget.tenancyEnd` marker records
a lapse at or after it. Weaver's temporal lane arms the `@at`, the platform's `MarkExpired` records the lapse on
the leaseapp, the lens reads two stored values — **no `$now` in any cypher** (Andrew, 2026-09-01).

Two gaps, both frozen-table `directOp`s under Weaver's service actor (the `SetListingStatus` precedent):

- **`missing_tenancyEnded`** = `landlordDecision = 'approved' AND signedAt <> null AND leaseEnd <> null AND
  endedAt = null AND lapsedAt >= leaseEnd AND openRenewalCount = 0` → **`EndTenancy{leaseAppKey: row.entityKey}`**
  (new, lease-signing, operator-granted). `openRenewalCount` counts live `renews` renewals with
  `status = 'open' AND cycleEnd = leaseEnd` — **an OPEN renewal holds the tenancy past its end** (the parties are
  mid-negotiation; the landlord's `CancelRenewal` is the way out, and a signed renewal extends `leaseEnd` so the
  timer re-arms on the new end). A cancelled or never-opened renewal does not hold it.
- **`missing_relist`** = `endedAt <> null AND unitKey <> null AND unitStatus = 'leased' AND
  otherLiveTenancyCount = 0` → `SetListingStatus{unit: row.unitKey, status: "available"}`.
  `otherLiveTenancyCount` counts OTHER approved applications on the same unit that are not ended (with or without
  a `.tenancy` — amended at build, 2026-09-14: a pre-`.tenancy` approval claims the unit through
  `missing_listingLeased`, so it holds the relist too) — **the load-bearing conjunct**: once the unit re-leases to a new tenant the old ended row must never flip
  it back. (`Reads: [row.unitKey, row.unitKey.listing]`, as `missing_listingLeased` declares.)
- `violating = missing_tenancyEnded OR missing_relist`.

**`EndTenancy{leaseAppKey}`** — reads `leaseAppKey` + `{leaseAppKey}.tenancy` (both **required** declared
reads: the gap only opens on a leaseapp with a tenancy). Refuses `NoTenancy` (absent/deleted), `NotYetEnded` when
`submittedAt < leaseEnd` (write-path honesty — the op does not trust the dispatcher's clock), and is an
**idempotent no-op** (Accepted, zero mutations) when `endedAt` is already set. Writes `.tenancy` with every
existing field preserved plus `endedAt = leaseEnd` (the term ended on its own end date — the fact the cards and
refusals name), **pinned to the hydrated `.tenancy` revision** (`make_aspect_update_occ`, the clinic-domain
helper — `SignRenewal` rewrites the same aspect). Operator-only grant (`mk("EndTenancy")`, GrantsTo operator).
Emits `leaseapp.tenancyEnded`.

**Consumers of the lifted state (the census, traced forward — every reader of `.tenancy`/approved+signed):**

| consumer | today | after |
|---|---|---|
| `leaseApplicationComplete` — 4 applicant gaps, `missing_listingLeased`, `violating` | approved+signed+relisted unit ⇒ `missing_listingLeased` re-opens and Weaver re-leases; a stale bgcheck re-opens `missing_bgcheck` for an ended tenant | new column `tenancyEndedAt`; conjunct `(tenancyEndedAt = null)` on the four applicant gaps and on `missing_listingLeased`; an ended tenancy is terminal-not-violating, the decline's shape |
| `leaseExpiry` — `missing_renewalCycle` | an ended lease with no renewal ever opened re-opens a cycle | conjunct `(endedAt = null)`; `freshUntil` null once ended |
| `renewalComplete` | anchored on renewals; the lens never dispatches an end under an open renewal, but the operator-callable op admits one | `open` / `missing_renewalComplete` / `violating` conjoin `(tenancyEndedAt = null)` (amended at build, 2026-09-14) |
| `applicantOnboarding` | counts approved leaseapps regardless of `endedAt` | conjunct `(endedAt = null)` in the count (amended at build, 2026-09-14) |
| clinic `residentVisit` · wellness resident rate | `.tenancy` presence only | read `endedAt` — a moved-out tenant is not a resident (amended at build, 2026-09-14) |
| `leaseRentSettlement` (semantic-contracts) | bills `[termStart, leaseEnd)` — already bounded by the recorded end | untouched |
| `leaseApplicationsRead` / `landlordLeaseApplicationsRead` / `renewalsRead` | no `.tenancy` columns | §2.3 |
| `staleUserTasks` | tasks close on the aspect the op writes | untouched |

Landlord double-approval (two approved applications on one unit, both with a `.tenancy`): the second's
`.tenancy` is stamped at approval today and stays inert until the unit is not `leased`. After the first ends,
`missing_relist` is held by `otherLiveTenancyCount = 1` and the second's `missing_listingLeased` by
`unitStatus = 'leased'` — the landlord's manual **Relist** (§2.4) breaks the tie by leasing to the second. Stated
here as the v1 behaviour; a landlord approving two applicants for one unit was already absorbed by idempotency
(listing-status design §Edge cases) and is not re-modelled.

### 2.3 R4 — the read lenses project the tenancy

`leaseApplicationsRead` and `landlordLeaseApplicationsRead` gain `tenancy_lease_start`, `tenancy_lease_end`,
`tenancy_term_start`, `tenancy_rent_amount`, `tenancy_ended_at` (RETURN aliases `tenancyLeaseStart` …
`tenancyEndedAt`); `renewalsRead` gains `lease_end` (the leaseapp's CURRENT `.tenancy.leaseEnd`). A live
protected table takes the new columns on `make refresh-loftspace` (`ALTER TABLE … ADD COLUMN IF NOT EXISTS`,
verified 2026-09-13).

### 2.4 FE (Sally's UX note — the FE Engineer builds it)

- **Terms panel** (`renderLeaseTermsPanel`, `app.js` ≈1919): before approval it states what the signature
  commits to — "Lease: from <moveIn>, N months, $<rent>/month" where rent is the offered rent when present, else the
  listing's (the same chain the op walks), and the head reads "Lease terms — review before signing". Once
  `.tenancy` is recorded it states the recorded lease instead — "Lease: <leaseStart> → <leaseEnd> · $<rentAmount>/
  month", and after a renewal "current term from <termStart>". **Every `.tenancy` stamp renders by its UTC
  calendar date** (`YYYY-MM-DD` slice, not `toLocaleDateString` — a midnight-UTC stamp reads as the day before
  west of Greenwich, the café `TenancyEnded` class) — and so does the pre-approval ask, the listing's `availableFrom`
  and a renewal's `cycleEnd`: every one is a midnight-UTC instant (amended at build, 2026-09-14).
- **Application status banner** (≈1817): `tenancyEndedAt` ⇒ "Lease ended <date>" (terminal; wins over every
  other banner), still showing the terms panel.
- **Renewed card** (≈2874): "term ends <cycleEnd>" → "renewed the term ending <cycleEnd> · new term ends
  <leaseEnd>".
- **Landlord decide surface** (`renderApplicantRow` / `decideApplication` ≈4404): the approve control states the
  terms the approval signs (the same three facts); an approved row shows the recorded lease; an ended row reads
  "Lease ended <date>".
- **Landlord unit card** (≈4261): a `leased` unit on which ANY approved tenancy has ended, or none is live, renders
  **Relist** → `SetListingStatus(available)` (`relistOffered`; amended at build, 2026-09-14) — the manual path for
  §2.2's double-approval tie-break (the button says an approved applicant with a live lease re-takes the unit
  automatically) and for a unit whose automatic relist has not landed yet; a `leased` unit with only live
  tenancies renders no status button (a manual relist there would be flipped straight back by
  `missing_listingLeased`). A decided or ended row never re-offers Approve/Decline (`decisionOffered`).
- Every dispatcher of `DecideLeaseApplication` declares `{leaseAppKey}.terms` in `optionalReads` (the decide
  form, `seed-classic-demo`; `lint-seed-declared-reads` pins the seed).

## 3. Non-goals

- The listing's `availableFrom` is not rewritten on relist (`SetListingStatus` is status-only; the landlord edits
  the listing). Browse shows the relisted unit as it is.
- No holdover / month-to-month tenancy: an open renewal holds the term; an ended one is ended.
- No backfill of `.tenancy.rentAmount` onto already-approved leases (the clause's coalesce covers them).
- The losing-rival row (★★ S, "never told the unit went to someone else") is a separate row — not built here.
- No change to `SetListingStatus`, `SignRenewal`, `OpenRenewal`, `CancelRenewal`.

## 4. Fire brief (build note, 2026-09-14)

**1. Scope sentence** — the three rows verbatim (§0): *Approval signs a lease on terms the applicant never
reviewed* · *A lease that ends without renewal never frees the unit* · *A renewed tenant's home still shows the old
lease*. Green bar: `go build ./...` · `make vet` · `golangci-lint run ./...` · `STRICT=1 go run
./scripts/lint-conventions.go` · every `scripts/lint-*.go` · `go test ./packages/lease-signing/
./packages/semantic-contracts/ ./packages/loftspace-domain/ ./cmd/loftspace-app/ ./internal/refractor/ -count=1` ·
`make verify-package-lease-signing` against the live stack · `make test-lease-convergence` · live: Priya Raman's
pending application approved on her 2026-09-15 ask, a seeded ended tenancy relists its unit.

**2. Verified touch-list**

| file | what |
|---|---|
| `packages/lease-signing/scripts.go:869-886` | Decide's tenancy derivation → `.terms` first, `rentAmount`, bare-date normalization |
| `packages/lease-signing/scripts.go` (leaseapp DDL script) | new `EndTenancy` branch + `make_aspect_update_occ` helper (mirror `packages/clinic-domain/ddls.go:1322`) |
| `packages/lease-signing/ddls.go:105,225-230,283-295` | `PermittedCommands` += `EndTenancy`; Decide's descriptor `OptionalReads` += `{payload.leaseAppKey}.terms`; `EndTenancy` descriptor (`Reads: leaseAppKey, leaseAppKey.tenancy`) + example |
| `packages/lease-signing/permissions.go` | `mk("EndTenancy")` operator grant |
| `packages/lease-signing/renewal_lenses.go:172-193` | `leaseExpirySpec` += `endedAt = null` conjunct; new `tenancyEndSpec` + lens entry (sibling file `tenancy_end_lenses.go` acceptable) |
| `packages/lease-signing/renewal_targets.go:39-54` | new `tenancyEndTarget()` (two gaps) registered beside `leaseExpiryTarget` |
| `packages/lease-signing/lenses.go:51,485-660,1350-1437,1496+` | `leaseApplicationComplete`: `tenancyEndedAt` column + conjuncts; `leaseApplicationsRead` / `landlordLeaseApplicationsRead`: 5 tenancy columns |
| `packages/lease-signing/renewal_lenses.go:100-130` | `renewalsRead` += `lease_end` |
| `packages/lease-signing/package.go:92` + `manifest.yaml` | `0.34.0 → 0.35.0` |
| `scripts/verify-package-lease-signing.go:130` | `ddlCheck` += `EndTenancy` |
| `scripts/seed-classic-demo.go:202` | Decide envelope `OptionalReads` += `.terms` |
| `cmd/loftspace-app/applications.go:28-141`, `landlord_applications.go:56-164`, `renewals.go:28-67`, `rls_columns_test.go:9-88` | new columns → structs, SELECTs, column pins |
| `cmd/loftspace-app/web/app.js:1817-1833, 1919-1973, 2862-2927, 4261-4297, 4404-4467` | §2.4 |
| `internal/refractor/*_corpus_census_test.go` (`anchor_hopindex…:165`, `branch_decomposition…:90`) | re-pin `leaseExpiry` / new `tenancyEnd` deliberately |

**3. Precedents** — `leaseExpirySpec` + `leaseExpiryTarget` (recorded-lapse timer, `freshUntil`, `byTarget`);
`missing_listingLeased`'s directOp entry (`targets.go:124`); `SignRenewal`'s `.tenancy` rewrite
(`renewal_scripts.go:453`); `make_aspect_update_occ` (clinic-domain); `TenancyEnded`'s courtesy pair (café,
`1402d742`) for the ended-state rendering; `lease_expiry_lens_test.go`'s `seedSignedTenancy` harness for the new
lens's pins; `renewal_ops_test.go` for the op vectors.

**4. Increment order**
1. **Inc 1 (package, R1 + R4 columns; `sonnet`)** — Decide derivation + descriptor/dispatcher reads; the five
   read-lens columns + `renewalsRead.lease_end`; version bump. Green: `go test ./packages/lease-signing/
   ./packages/semantic-contracts/ -count=1`, a Decide vector whose `.terms` and listing disagree pins
   `leaseStart = moveInDate`, `rentAmount = requestedRent`, a bare-date vector, a no-terms fallback vector;
   revert-proof each.
2. **Inc 2 (package, R2; `opus` — new enforcement point + state)** — `tenancyEnd` lens/target, `EndTenancy`,
   the `leaseApplicationComplete` + `leaseExpiry` conjuncts, verify-package pin. Green: lens pins (armed /
   lapsed-ended / open-renewal-holds / cancelled-renewal-ends / relist-only-this-lessee / re-leased-unit-never-
   flips-back / ended-row-terminal-in-leaseApplicationComplete / ended-never-reopens-renewalCycle), op vectors
   (NoTenancy / NotYetEnded / idempotent / preserves termStart+rentAmount / OCC conflict), corpus census re-pin,
   `make test-lease-convergence`.
3. **Inc 3 (`cmd/loftspace-app`, `sonnet`; parallel with Inc 2 on disjoint files)** — handlers + column pins +
   §2.4 FE; goja pin for the UTC-date rendering + the Relist gate + the banner precedence.
4. Live: `make refresh-loftspace`, rebuild + cycle `bin/loftspace-app`, approve Priya's ask, seed one ended
   tenancy and watch `tenancyEnd` → `EndTenancy` → relist.

**5. In-scope gotchas** — package edit ⇒ version bump + `Version` constant; a new op ⇒ `verify-package`'s
`ddlCheck` count; a new refusal/read on an existing op ⇒ `lint-seed-declared-reads` (seed-classic-demo's Decide);
`lint-board` STRICT before the board commit; corpus census tests re-pin by lens name; `renewalWindow` short-tag
build (`freshness_window_short.go`) has no analogue here — `leaseEnd` is data. Dossier entries copied in
(`_packages.md`): *a lens that reads a RECORDED fact depends on whoever arms the timer — couple the two in one
fragment* (pin the `tenancyEnd` freshUntil/lapse fragment on the shipped spec, and name the marker's reader);
*every conjunct of `missing_<g>` must count the population the op's own test reads* (the open-renewal conjunct is
dispatch-gated only — state it, as `SignRenewal`'s bgcheck freshness is); *a shared-vertex repoint needs a
content-and-revision gate against every other writer* (`SignRenewal` rewrites `.tenancy` — OCC on the hydrated
revision); *a guard's OCC rests on whoever writes its read declaration* (`.tenancy` is a REQUIRED read of
`EndTenancy`; the empty-`contextHint` vector is refused, not lazily read); *a recorded value is read as the fact it
records* (`endedAt = leaseEnd`, never the fire instant); *a playbook `Params` bound to an OPTIONAL-hop column is a
dispatch refusal where the hop misses* (`missing_relist` conjoins `unitKey <> null`). (`vertical-apps.md`): *two
courtesy surfaces for one refusal name the same instant in different zones* (UTC calendar date everywhere a
`.tenancy` stamp renders; `NotYetEnded` names the same slice); *a server-side refusal added to one form leaves its
sibling a dead end* (Relist renders only where the automatic flip would not undo it); *a transport throw after a
destructive submit* (Decide's catch already gated). Standing checklist: state table for `endedAt` (created by
`EndTenancy` only · never reset · carried by `SignRenewal`? — a signed renewal cannot follow an end, and
`SignRenewal`'s whole-aspect rewrite would drop it, so `SignRenewal` refuses `TenancyEnded` when `endedAt` is
set — one added conjunct, pinned) · every count re-run live · positive vector before negative · one writer per
deterministic key (`.tenancy`: Decide creates, `SignRenewal`/`EndTenancy` update under OCC).

**6. Adjacent finds** — (a) `moveInDate` written as a bare date by both seeds and read as RFC3339 by the DDL:
absorbed (the op accepts both; the seeds are not rewritten). (b) A landlord approving a second applicant on a
leased unit stamps a second `.tenancy` and bills them (`leaseRentSettlement`) — pre-existing, outside these rows;
filed to the board only if the close pass finds it live (it is not: 0 double-approved units today).

**7. Non-goals** — §3.

**Scope-diff gate:** every touch above traces to R1 (Decide + descriptor/dispatchers + terms panel + decide
surface), R2 (`tenancyEnd` + `EndTenancy` + relist + the two conjunct sets + ended state + Relist) or R4 (five
columns + `lease_end` + the card). The `SignRenewal` `TenancyEnded` refusal is R2's state-table obligation
(carry-vs-drop of `endedAt`), not a widening. Nothing substitutes an adjacent mechanism; no contract is touched.

## 5. Build note (2026-09-14) — shipped

**Commits:** `d629b87a` (Inc 1, R1 + read-lens columns) · `89b940e8` (Inc 2, `tenancyEnd` + `EndTenancy` + the
consumer conjuncts) · `9b00f28a` (Inc 3, the app) · `ce28fa9f` (clinic/wellness resident checks read `endedAt`) ·
`b56bc0f0` (the cold review's fix round + the end-to-end convergence proof). Live: lease-signing 0.36.1,
loftspace-domain 0.12.3, clinic-domain 0.35.1, wellness-domain 0.27.5; Priya Raman's 9 Backfill Ave application
approved through the Gateway as the landlord — `.tenancy` = `{leaseStart 2026-09-15, leaseEnd 2027-09-15,
rentAmount 1800}` (her ask; the listing said 08-23 / 2050) and the unit flipped `leased`.

**Deviations from §2 (each amended where it stands above, dated here):**
- §2.2 `otherLiveTenancyCount` counts every OTHER approved, not-ended application on the unit — **not** only those
  with a `.tenancy`: a pre-`.tenancy` approval claims the unit through `missing_listingLeased` (which needs only
  `tenancyEndedAt = null`), so counting it as live is what keeps the two targets from a relist/lease ping-pong.
- §2.2 consumer table: `renewalComplete` is NOT untouched — an operator may `EndTenancy` under an open renewal (the
  op walks no renewals, by design), so `open` / `missing_renewalComplete` / `violating` conjoin
  `(tenancyEndedAt = null)`; `applicantOnboarding` conjoins it too. Clinic's `residentVisit` and wellness's
  resident rate read `endedAt` (a moved-out tenant is not a resident).
- §2.1 malformed terms are refused where they are minted: `CreateLeaseApplication` stores `moveInDate` as the
  normalized RFC3339 instant and refuses `InvalidTerms` (term < 1 month or non-integer, non-positive or
  non-numeric rent, unparseable date); `DecideLeaseApplication` refuses a stored zero term and lets a
  non-positive offer fall through to the listing rent.
- §2.4 Relist on a `leased` unit renders when ANY tenancy on it has ended (`relistOffered`), so the landlord's
  manual relist is the double-approval tie-break §2.2 names (with a live approval still present the button says
  the approved applicant re-takes the unit automatically); a decided or ended row never re-offers Approve/Decline
  (`decisionOffered`); the by-unit console and search carry the `ended` disposition; the pre-approval ask, the
  listing's available-from and a renewal's cycle end render by their UTC calendar date too (the "keeps
  `fmtDate`" clause is struck — every `.terms`/`.tenancy`/`cycleEnd`/`availableFrom` stamp is a midnight-UTC
  instant); rent lines honour the listing currency; both read lenses project the listing's `availableFrom` /
  `leaseTermMonths` so the landlord's approval hint has its fallback.

**Known cost (measured live, not a defect):** `tenancyEnd`'s `other` fan is O(applications-per-unit²) per event on
a unit — on 12 Classic Demo Ave (52 seed-litter rival applications) an event re-anchors 52 rows at ≈6 s each on
a swapping host; a real unit carries a handful. The litter is the losing-rival row's subject, not this design's.

**Accounting of what the reviews found:** every finding fixed in `b56bc0f0` except two NITs left as stated
behaviour — the OCC conflict is pinned at the script level (the pipeline harness has no seam to interpose a
write between hydration and commit), and an operator hand-marking a unit `leased` with only ended tenancies is
flipped back on every evaluation (`withdrawn` is the off-platform hold; stated in the target description).
