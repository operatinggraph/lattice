# LoftSpace — a lost application is a recorded fact, not a live derivation

**Status:** ✅ **Winston-ratified — build-ready** (Vertical Steward, 2026-09-14). No frozen-contract change, no
architectural fork: every mechanism is package-owned (`packages/lease-signing`, `cmd/loftspace-app`,
`cmd/wellness-app`) and mirrors the shipped recorded-fact pattern in the same package — `tenancyEnd` →
`EndTenancy` ([tenancy-end design §2.2](loftspace-lease-term-and-tenancy-end-design.md)): a convergence gap opens on
the observed state, a Weaver-dispatched directOp under the service actor records the fact on the anchor, every
consumer reads the recorded fact. The board row's `no-pattern: recorded terminal loss on a leaseapp` is therefore
falsified — a steward `📋`, not a designer `📐` (`agents/steward/SKILL.md` §4: a precedent in the touched file).

PO row (★★ M, filed 2026-09-14 at `84c43c25`): **A relisted unit revives every losing rival.**

## 1. Problem (live-observed)

`lost_to_rival` is `(unitStatus = 'leased') AND (landlordDecision = null)` in both application read lenses
(`packages/lease-signing/lenses.go:1500`, `:1654`) — a live derivation over the unit's **mutable** listing status.
The four applicant gaps and `missing_decision` are held shut by the same term (`:1142-1145`, `:1171`, `:1245`,
`:1488-1491`; `applicantOnboarding` the same). When the winner's tenancy ends and `missing_relist` flips the unit
back to `available`, every rival's gaps re-open, the applicant's card reverts to "In review", the landlord's queue
re-offers Approve/Decline on them, and Weaver re-dispatches `SignLease` / `RecordIdentityPII` user tasks — 52 live
applications on 12 Classic Demo Ave. The anchor is walked through a mutable relation, so history re-anchors on
every change; the fix is to record the fact at the event.

## 2. Decisions

### 2.1 Product: a lost application is closed, never revived (PO hat, Winston)

The unit going to someone else ends this application. A relist is a new opportunity the rival applies for afresh —
the same path a **declined** applicant already has: **Withdraw** (offered on every non-approved card,
`app.js:1969`) frees the per-(applicant, unit) guard link and a re-apply revives it (`scripts.go` Withdraw /
Create). Reviving months-old applications with stale terms, stale profile snapshots and expired background checks
against a relisted unit is the wrong product, and it is what the live derivation does today. No un-loss op.

### 2.2 The recorded fact — `.decision {value: "lost", decidedAt}`

The loss is a third terminal value of the **existing** `.decision` aspect, beside `approved` / `declined`. Every
`.decision`-keyed guard already treats a recorded value as terminal: `DecideLeaseApplication`'s `DecisionFinal`
refuses a different value (`scripts.go:839-843`), `missing_decision` needs `landlordDecision = null`,
`declined` / `landlordApproved` / `landlordDeclined` compare against their own literal. No `reason`, no
`.decidedProfileSnapshot` (that snapshot records what the **landlord** saw at decision time; nobody decided this
one). `decidedAt = time.rfc3339_utc(op.submittedAt)` — the instant the platform recorded the loss, the same stamp
`DecideLeaseApplication` writes.

### 2.3 The gap + op — `missing_lossRecorded` → `RecordApplicationLoss{leaseAppKey}`

**Gap**, in `leaseApplicationComplete` (the anchor already projects every input):
`missing_lossRecorded = (unitKey <> null) AND (unitStatus = 'leased') AND (landlordDecision = null)` — the exact
premise the banner already tells the applicant. Folded into `violating`. Target row in `targets.go`, the
`missing_tenancyEnded` shape: `directOp RecordApplicationLoss`, `Params {leaseAppKey: row.entityKey}`,
`Reads [row.entityKey]`, `OptionalReads [row.entityKey.decision]`, `Enumerations [{row.entityKey appliesToUnit out}]` (the
walk the script runs, on the envelope per Contract #2 §2.5 — the `missing_retirement` gap's shape) (absent is the whole point of the gap; the declared
absence makes the write a CreateOnly assertion so a landlord decision racing the dispatch conflicts and the retry
re-reads it as decided — a no-op, §2.3 below).

**`RecordApplicationLoss{leaseAppKey}`** — lease-signing, operator-granted (`permissions.go`, the `EndTenancy`
entry's twin), never person-facing:

- `leaseAppKey` required declared read, validated alive; `{leaseAppKey}.decision` declared `optionalReads`.
- **Any recorded decision → idempotent no-op** (Accepted, zero mutations, no event): `lost` is the at-least-once
  re-dispatch; `approved` / `declined` mean the landlord decided it — nothing to record. A refusal here would burn
  Weaver's retry budget on a race the lens has already closed.
- **The unit comes from the application's OWN link, never a payload field** — `leaseapp_unit(app_key)`
  (`scripts.go:463`, the sanctioned `(e)` enumeration `SignLease` / `DecideLeaseApplication` use), then the
  `(e)` follow-up `kv.Read(unit + ".listing")` (`SignLease`'s `:705-708` shape). Refuses `NoUnit` (no live
  appliesToUnit endpoint) and **`UnitNotLeased`** when `.listing` is absent / deleted / `status <> 'leased'` —
  write-path honesty: an operator running it by hand cannot mark an application lost against an available unit
  (the dossier's "by construction" class: the op, not the lens, holds the refusal).
- Writes `.decision {value: "lost", decidedAt}` via `make_aspect` — an op `create`, the `SignLease` `.signature`
  shape — because only a `create` is CreateOnly-conditioned by the declared absence (`commit_path.go` conditions
  `create` ops on a KnownAbsent key; an `upsert` there is an unconditioned Put that would overwrite a landlord
  decision racing the dispatch). Emits `leaseapp.applicationLost{leaseAppKey, unitKey}`.
- DDL: `PermittedCommands` + descriptor prose + example (`ddls.go`, beside `EndTenancy`); the `decision` field
  prose reads `(approved|declined|lost)` — the payload `enum` stays `approved|declined` (a landlord never submits
  `lost`). `scripts/verify-package-lease-signing.go` `ddlCheck` → 9 commands. `Effects`: mirror `EndTenancy`'s
  entry if it carries one.

### 2.4 Consumers of the lifted state (status-value census, `MATCH (x:leaseapp)` repo-wide)

| consumer | today | after |
|---|---|---|
| `lost_to_rival` — `leaseApplicationsRead:1500`, `landlordLeaseApplicationsRead:1654` | live derivation | `(landlordDecision = 'lost') OR (<today's term>)` — the recorded fact holds across a relist; the live term keeps the banner honest in the seconds before Weaver records it |
| four applicant gaps + `violating` — `leaseApplicationComplete:1142-1145,1171`; `leaseApplicationsRead:1488-1491`; `onboardingApps` count `:1245`; `applicantOnboarding` | `((unitStatus <> 'leased') OR (landlordDecision = 'approved'))` re-opens on relist | conjoin `(landlordDecision <> 'lost')` at **every** site of that term (the engine is two-valued: `null <> 'lost'` is true, `values.go:121`) |
| `missing_decision` (both lenses) · `missing_listingLeased` · `missing_manager` · `renewalComplete` · `tenancyEnd` (`otherLiveTenancyCount`) · `leaseRentSettlement` (semantic-contracts `:193`) | `= null` / `= 'approved'` | untouched — `lost` is neither |
| `DecideLeaseApplication` | `DecisionFinal` on a different prior value | untouched — refuses `already lost` |
| `WithdrawLeaseApplication` | refuses only `approved` | untouched — a lost application stays withdrawable (frees the guard; the re-apply path, §2.1) |
| `SignLease` | refuses `UnitNoLongerAvailable` only while the unit is `leased` and the decision is not `approved` — a lost application on a relisted unit could sign under a still-live grant | refuses `UnitNoLongerAvailable` on a recorded `lost` regardless of the unit's status |
| `SetApplicantProfile` / renewal ops | read `.decision` for approved | untouched |
| `cmd/loftspace-app` landlord search (`search.go` `searchLandlordColumns`) · by-unit console `applicationStatus` (`unit_applications.go:91`) | search never selects `lost_to_rival`; the by-unit console reads a lost row as `qualified` / `in_review` and can rank it "best match" | search selects it and the chip names it; `applicationStatus` gains a `lost` arm (`DISPOSITION.lost`, ranked with declined) |
| `cmd/loftspace-app` `decisionOffered` (`app.js:4152`) | hides on `unitLeased` alone | add `!a.lostToRival` — after a relist the unit is available again and Approve/Decline would re-offer a `DecisionFinal` refusal |
| `cmd/loftspace-app` banner / stepper / landlord chip + note / inbox disposition (`taskDisposition`) | all keyed on `lostToRival` | untouched — the boolean now stays true |
| `cmd/wellness-app/residents.go:123` `declinedDecision` | drops `declined` only | drop `lost` too — a lost applicant does not live in the building; keep the allow-nothing-else shape (a set of two) |
| café `cafeLeaseWorkplaces` | filters no decision at all (documented posture) | untouched |

### 2.5 FE (Sally's note — the FE Engineer builds it)

- `decisionOffered` gains `!a.lostToRival` (goja-pinned in `lease_term_ui_test.go`'s shape).
- Applicant card banner for a lost row: "This unit went to another applicant." stays; the Withdraw button's
  purpose text is already "re-apply" — no copy change.
- No new columns; `lost_to_rival` keeps its name and type in `rls_columns_test.go`.

## 3. Non-goals

- No un-loss / reopen op; no landlord "reconsider" on a lost row.
- No automatic tombstone of lost applications and no guard-link release at loss time (Withdraw is the applicant's
  act; a lost application remains a record the landlord's queue names).
- No change to `SetListingStatus`, `EndTenancy`, `missing_relist`, or the double-approval tie-break.
- A unit hand-marked `leased` by the landlord loses its pending applications the same way — `leased` means leased
  (the off-platform hold is `withdrawn`).

## 4. Fire brief (build note, 2026-09-14)

**Scope sentence:** record a losing application's loss as `.decision = lost` via a Weaver-dispatched
`RecordApplicationLoss` directOp, and make every consumer of the application's liveness read the recorded fact so
a relisted unit revives no rival.

**Touch-list (verified live):**

| file | change |
|---|---|
| `packages/lease-signing/lenses.go:1142-1145,1171` | `(landlordDecision <> 'lost')` conjunct on the four gaps + `violating`; new `missing_lossRecorded` column + in `violating` |
| `packages/lease-signing/lenses.go:1245` | same conjunct inside `onboardingApps` |
| `packages/lease-signing/lenses.go:1488-1491,1500` | same conjunct; `lost_to_rival` OR-form |
| `packages/lease-signing/lenses.go:1654` (+ the `:1366` / `:1544` doc comments) | `lost_to_rival` OR-form; comments describe the recorded fact |
| `packages/lease-signing/targets.go` (`missing_listingLeased` at `:124` is the neighbour) | `missing_lossRecorded` gap row |
| `packages/lease-signing/scripts.go` (after `EndTenancy` `:1504-1569`) | `RecordApplicationLoss` branch |
| `packages/lease-signing/ddls.go:105,128-132,203,226,264,391` | PermittedCommands · descriptor prose · example · `decision` prose |
| `packages/lease-signing/permissions.go:220,533` | operator grant (both tables `EndTenancy` sits in) |
| `packages/lease-signing/manifest.yaml`, `package.go` | `0.37.0 → 0.38.0` |
| `scripts/verify-package-lease-signing.go:130` | 9 commands |
| `cmd/loftspace-app/web/app.js:4152` | `!a.lostToRival` |
| `cmd/wellness-app/residents.go:118-123,182` | `lost` disqualifies |
| tests | `record_application_loss_ops_test.go` (mirror `end_tenancy_ops_test.go`: records · idempotent on lost/approved/declined · `UnitNotLeased` · `NoUnit` · non-operator denied · undeclared-decision optional path); `protected_lens_test.go:555` + `landlord_protected_lens_test.go` (recorded `lost` on an **available** unit projects `lost_to_rival = true` and all four gaps + `missing_decision` false); `lens_cypher_test.go` / `tenancy_end_consumers_lens_test.go` (the gap opens on leased+undecided, closes on `lost`); `cmd/loftspace-app` goja pin for `decisionOffered`; `cmd/wellness-app` residents filter pin; `internal/refractor` corpus census re-pin |

**Increment order + green checks:**
1. Package: lens edits + gap + op + DDL + grant + version + verify script; `go test ./packages/lease-signing/ ./packages/... -count=1`, `go test ./internal/refractor/ -run 'Corpus|Census' -count=1`, `go run ./scripts/lint-conventions.go`, `go run ./scripts/lint-gap-column-declaration.go`, `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`, `go run ./scripts/lint-seed-declared-reads.go`.
2. Apps: `app.js` + `residents.go`; `go test ./cmd/loftspace-app/ ./cmd/wellness-app/ -count=1`, `go run ./scripts/lint-app-op-descriptors.go`.
3. Live: `make refresh-loftspace` (or `reinstall-package PKG=lease-signing`) against the running stack; Weaver records the 52 on 12 Classic Demo Ave; `make verify-package-lease-signing`; the read model shows `lost_to_rival = true` with `.decision.value = lost`; cycle `bin/loftspace-app` + `bin/wellness-app`.

**Gotchas (dossier, part 5):**
- _packages: *a "by construction" exclusion is a claim about the OP's refusals* → `UnitNotLeased` lives in the op.
- _packages: *a Params entry bound to an optional-hop column is a dispatch refusal* → the gap's only param is `row.entityKey`; the unit is resolved by the op from the link.
- _packages: *a directOp rewriting a maintained aspect off the Weaver row alone* → the op reads nothing off the row but the key; the premise is re-verified from state.
- _packages: *a recorded value is read as the fact it records* → `lost` is read as "lost", never as "unit leased".
- vertical-apps: *a new terminal state's census spans every surface keyed to the row* (minted 2026-09-14, second sighting the same day) → the FE key is the unchanged `lostToRival` boolean; the one surface keyed on the unit's status instead (`decisionOffered`) takes the conjunct.
- vertical-apps: *a transport throw after a destructive submit* → no new FE submit.
- The `((unitStatus <> 'leased') OR (landlordDecision = 'approved'))` term is inlined **eight** times incl. `violating`; a missed site is a rival that re-opens one gap — grep the literal after the edit, expect zero without the new conjunct.

**Non-goals:** §3.

## 5. Build note (2026-09-14) — shipped

**Code:** `71912135` (package 0.38.0 + both apps); the search-column pin lands with the close commit. CI green.
Reviews: one cold adversarial pass (no BLOCKING; S1–S4 fixed in the round — `SignLease` refuses a recorded `lost`,
the landlord search selects `lost_to_rival`, `applicationStatus` gains a `lost` arm, the gap declares its
`appliesToUnit` enumeration; §2.3/§2.4 amended where they stood).

**Live (shared stack):** `reinstall-package` 0.37.0 → 0.38.0 (created 8, updated 19). Weaver's first 23 dispatches
landed `AuthDenied` one second ahead of the grant's `cap.role-by-operation` row (`_packages.md` §5's known
first-dispatch shape); `lattice weaver revoke` + `enable leaseApplicationComplete` re-armed them and all 23
committed. The other 29 rivals had no `weaver-targets` row (non-violating rows are dropped, and the lens's
reactivation rebuild replays ~3 s/event with lag ≈65k) — `lattice lens reproject` per anchor opened the gap and
Weaver recorded them: **52 / 52 live rivals carry `.decision = lost`** (Core KV), `read_lease_applications`
reads 52 `lost_to_rival` / 52 `landlord_decision = lost`; `verify-package-lease-signing` 93 OK; `bin/loftspace-app`
and `bin/wellness-app` rebuilt and cycled.

**Residuals (stated, not rows):**
- Rivals of a unit that leased and relisted *before* 0.38.0 are outside the gap (`unitStatus = 'leased'` is its
  premise) and their pre-fire revival stands; none observed live — every lost rival sat on a still-leased unit.
- `read_landlord_lease_applications` still reads `landlord_decision` null for the 52 (its rebuild has been in
  flight since the 07:58 reactivation; the landlord surfaces key on `lost_to_rival`, which is already true). The
  `leaseApplicationComplete` rebuild throughput (≈3 s/event, `LensProjectionLagging` 65k,
  `LensSweepStalled` 95 h before this fire) is a Refractor condition for the Lattice lane, reported to Andrew
  in the fire report.

**Close-pass classification:** design-gap (S1 — an op guard keyed on the premise the recorded fact replaces;
new `_packages.md` class) · brief-gap (S2/S3 — third sighting of the vertical-apps terminal-state census class;
search half mechanized by `search_columns_test.go`) · convention (S4 enumeration declaration; N1 the design's
`upsert` claim) · review-over-reach: none.
