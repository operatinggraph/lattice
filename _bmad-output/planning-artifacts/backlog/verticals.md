# Backlog — App Verticals (Stream 1)

Stream 1 = app-vertical packages + FEs (LoftSpace, Clinic). Advanced by the **Vertical Steward**; demand
filed by the **Vertical PO** (file-only). Index + cross-lane rules: [../backlog.md](../backlog.md).
**Row discipline** (one item = one row; State = token + ref + one-line next; detail lives in the design
doc + git, never narrated in the cell): see [lattice.md → How this board works](lattice.md).

**Scales.** Imp ★/★★/★★★ · Size XS–XL. **State.** 📋 ready · 🏗️ building · 📐 awaiting-Andrew ·
✅ ratified (designed, not built) · 🚧 blocked (`blocked-on:` / Andrew-gated) · 🗄️ shelved (`revive:`).

## Vertical demand backlog (PO discovery)

Open items only — **shipped demand is in the Done log**. The PO files (tagged vertical + owner: FE = Sally +
FE Engineer · pkg = Package Designer · platform = component owner + Lattice lane); the Steward + FE
Engineer build. **No-paper-over:** a missing platform *primitive* routes to [lattice.md](lattice.md) and
the row is `🚧 blocked-on:` it (a missing *lens* is package work, built here).

| Item | What it is (PO view) | Vertical | Owner | Imp | Size | State |
|---|---|---|---|---|---|---|
| **Rent is billed from the day of approval to forever, never from leaseStart to leaseEnd** | The rent clause is minted at approval and charges on a null `chargeValidUntil`, then every lapse ([lenses.go:269](../../../packages/semantic-contracts/lenses.go:269)) — nothing reads `.tenancy`. Live: Priya owes $4,800 before her 09-08 move-in; Jordan is billed past his 09-06 term end. | LoftSpace | pkg | ★★★ | M | 🏗️ building · [design §13](../../implementation-artifacts/semantic-contracts-executable-paper-design.md) · one fire with the renewal row · next: Inc 1 builtin |
| **A signed renewal's new rent never reaches the bill** | `SignRenewal` extends `.tenancy.leaseEnd` only ([renewal_scripts.go:489](../../../packages/lease-signing/renewal_scripts.go:489)); the clause keeps its original `amountCents` and `.terms.rentAmount` is read by the display lens alone. Live: Jordan's renewal quotes $2,125 against a $2,050 clause. | LoftSpace | pkg | ★★★ | S | 🏗️ building · [design §13](../../implementation-artifacts/semantic-contracts-executable-paper-design.md) · folded into the term fire · next: Inc 2 coverage gap |
| **A tenant with an assigned signing task is refused at the signature** | `SignRenewal` fails closed on a leaseapp with no `.applicationSignals` ([renewal_scripts.go:462](../../../packages/lease-signing/renewal_scripts.go:462)); the FE's `renewalReady` enables the button off terms alone ([app.js:2725](../../../cmd/loftspace-app/web/app.js:2725)). 5 of 6 live tenancies have no profile. Live: Jordan Ellis, task assigned → `ApplicationSignalsMissing`. | LoftSpace | pkg + FE | ★★★ | S | 📋 ready · the renewal asks its own guarantor question, or the FE routes to the profile first |
| **The API lets a tenant withdraw an executed lease** | `WithdrawLeaseApplication` tombstones the leaseapp with no decision guard ([scripts.go:1062](../../../packages/lease-signing/scripts.go:1062)); only the FE hides the button after approval ([app.js:1898](../../../cmd/loftspace-app/web/app.js:1898)). The account (`heldFor`), its balance and the rent clause dangle off the tombstone and the unit stays `leased`. | LoftSpace | pkg | ★★ | S | 📋 ready · refuse `AlreadyDecided` |
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks, panes arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 🗄️ shelved (revive: new PO/Andrew demand — Fires 0–5 + satellites re-grounded 2026-08-30, nothing unbuilt) · [design §7](../../implementation-artifacts/edge-showcase-app-design.md) |
| **Facet on a literal iOS device** | The SwiftUI renderer builds + runs as a macOS proxy only. A real iOS/simulator build proves platform packaging (App Store viability), which FORK-1's freeze no longer waits on. Also unblocks real `swift test` in place of the hand-mirrored `swift run` harness. | Cross-vertical | Sally + FE Engineer | ★ | M | 🗄️ shelved (revive: a machine with full Xcode — this host has CommandLineTools only) · [design §7.10](../../implementation-artifacts/edge-showcase-app-design.md) |
| **A just-minted applicant with no application is on no staffer's roster** | `applicantRosterRead` anchors an identity on its own key + the landlord/buildings of units it has a live application against ([lenses.go:250](../../../packages/loftspace-domain/lenses.go:250)); a claim secret lost before the first application has no roster row to re-issue from. Live: 0. | LoftSpace | pkg | ★ | S | 🗄️ shelved (revive: a PO-observed instance or a second lens needing a mint-site anchor; re-mint → merge recovers it) · [triage §4](../../../docs/reviews/verticals-designer-triage-2026-09-10.md) |

**Explicitly descoped (ambitious-PO pass, 2026-07-09):** structured diagnosis/procedure coding (ICD/CPT),
vitals, and e-prescribing were considered and deliberately NOT filed — a certified EHR is out of scope for a
reference vertical whose job is demonstrating platform mechanics, not clinical-coding/DEA compliance. Flagging
the boundary so it reads as a decision, not an oversight.

**Spec** = the go-live composition demo (public-presence site, `localhost:7900/#demo`) — four lenses × package
toggles. PO ruling: all composition is **package-level, no Lattice block** (ledger `heldFor` anchor · generic
`claim_cell` · `contextHint.reads` — precedent: `DebitAccount`→clause; file:line grounding in the commit).
Build against the real key shapes, not the demo's: keys are **NanoIDs** (Contract #1) and the account→lease
relation is `heldFor` (the demo's `ACC88`/`BK7`/`L204` + `billedWith` are cosmetic).

## PO notes (dated — drives rotation)

Compact rotation memory only — PO *findings* are filed as demand rows above + the Done log; the verbose
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic ↔ Café ↔ Wellness, staggered from the Steward.
**Wellness joined** 2026-07-09 (`cmd/wellness-app` shipped, live on :7802) — fold it into rotation; see
[agents/vertical-po/SKILL.md](../../../agents/vertical-po/SKILL.md) §1.

- **Rotation to date:** LoftSpace ×32, Clinic ×31, Café ×22, Wellness ×19.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-09-03:** Café — drove front-of-house + resident hats through menu/POS/charge/void/settle/ledger/arrears; the desk can't take a payment it is already chasing, and $14.50 is invisible to all staff; filed 4.
- **2026-09-03:** Wellness — drove member + front-desk hats through schedule/book/waitlist/capacity/series/roster/ledger/arrears; a capacity raise strands the waitlist and a term takes six ops to call off; filed 3.
- **2026-09-04:** LoftSpace — drove landlord + tenant hats through portfolio/listings/apply/renewal/tasks/ledger/one-bill/self-pay/search; a tenant cannot sign a renewal that ends in 2 days and one live tenancy bills nothing; filed 2 + 1 platform.
- **2026-09-04:** Clinic — drove patient/provider/front-desk hats through appointments/encounters/visit-series/follow-ups/ledger/wellness-referral; four live cadences run on deleted patients and the referral picker is 92% dead classes; filed 3.
- **2026-09-04:** Café — drove front-of-house + resident hats through menu/POS/open/charge/void/settle/ledger/payment/arrears; two debtors have no name and every menu item shows twice; filed 3.
- **2026-09-04:** Wellness — drove member + front-desk hats through schedule/bookings/roster/attendance/reminders/ledger/arrears; a guest's $15 is unsettleable and two $25 no-show fees outlive the fix written for them; filed 3.
- **2026-09-05:** LoftSpace — drove landlord + applicant hats through listings/apply/sign/decide/withdraw/renewals/tasks/ledger/one-bill/documents/search; a declined applicant keeps an executed lease and the landlord can't read one; filed 3.
- **2026-09-05:** Clinic — drove front-desk/provider/patient hats through register/book/status/encounters/series/follow-ups/ledger; a just-registered patient is invisible to the desk and can never claim their login; filed 3.
- **2026-09-05:** Café — drove front-of-house + resident hats through menu/POS/open/charge/void/settle/ledger/payment/refund/arrears; a credit balance ages later charges as long-overdue and a mis-keyed payment cannot be undone; filed 3.
- **2026-09-06:** Wellness — drove member + front-desk hats through schedule/book/cancel/roster/attendance/instructors/studios/ledger/arrears; a resident is quoted the walk-in price and a retired studio locks the desk out of its own classes; filed 4.
- **2026-09-13:** LoftSpace — drove landlord + tenant hats through portfolio/applications/renewals/tasks/ledger/withdraw; rent bills before move-in and past term end, a renewal's new rent never bills, the tenant's signature is refused; filed 4.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-09-13 · `ee354831` · Vertical-apps dossier censused — a `rentCurrency` stored XSS, 4 secret-losing ceremony catches, 5 stale-render writes fixed; four classes gated ([census](../../../docs/reviews/verticals-dossier-census-2026-09-13.md)).
- 2026-09-13 · `64ee009c` · Three twice-seen `_packages.md` dossier classes become CI gates — `lint-link-target-count`, the retry-cap rule in `lint-gap-column-declaration`, `lint-loupe-console-grants` (one live gap pinned); dossier 14 → 12.
- 2026-09-13 · `7d9111b7` · Café's FE gates reach their siblings — wellness `esc()` escapes both quotes, goja-pinned; the `KNOWN_CATALOG_OPS` coverage test lands in wellness + clinic as a closed-set classifier; two dossier classes retire.
- 2026-09-13 · `3658dadb` · A kernel root is not a member — `CreateBooking`/`JoinWaitlist` refuse a `data.protected` booker (`ProtectedBooker`) before any slot cell lands on its hub; 0.27.1 refreshed live, both ops refuse the admin.
- 2026-09-13 · `fc2c1f68` · 15 appointments carry no site — `BackfillAppointmentSite` counts LIVE sites (a `practicesAt` link to a tombstoned building is not a second site); 0.34.26 refreshed live, the replay backfilled all 15.
- 2026-09-13 · `d51c950a` · 8 bookings `booked` on ended classes — resolved live before any replay: `pastDueBookings` shows 0 violating, every 07-20…08-23 booking is `noShow`; the gate was the fire classifier's, not Andrew's.
- 2026-09-13 · `a87e06b3` · The executed lease names its tenant — `SignLease` snapshots the name under the `executedLeaseRecord` class, docGen egresses it as a top-level ref, the floor tolerates a legacy signed app; live: `Tenant: Priya Raman`.
- 2026-09-13 · `5f9888b1` · A recurring class moves to a new weekday/time in one act — `ReassignSessionSeries` shifts every still-upcoming occurrence at the studio by one delta, cells one batch per hub, pinned to the anchor the desk saw.
- 2026-09-13 · `f655e9c8` · The front desk sees which booked visits a provider's time off overlaps — derived in the FE from held projections (`timeOffConflict`, goja-pinned): reschedule worklist leads Follow-ups, card badge, editor preview.
- 2026-09-13 · `ae197820` · A booking that still owes stays — a late cancel on a priced class keeps the booking live as `forfeited` (no seat), the desk keeps the guest's name and the charge; the Facet's booking tail filters it; proven live.
- 2026-09-13 · `3e2e65c4` · A rogue-claimed patient login has an operator undo — `RevokeIdentityClaim` cuts every credential, returns the identity to unclaimed, re-issues the secret; clinic "Reset login"; refresh re-resolves; proven live.
- 2026-09-12 · `6ae40061` + `5b379b42` · The front desk can name every resident it collects from — `ReassignLeaseUnit` re-points a lease whose unit was tombstoned; the seed backfill re-pointed all 10 strays live (`missingLocation` 10 → 0).
- 2026-09-12 · `18d32074` · The desk-minted-login trust model is accepted for a bound chart (Andrew) — the registrar holds the claim secret by ratified doctrine; a rogue claim leaves three traces; the missing operator undo filed as its own row.
- 2026-09-10 · `5495fdc` · A submitted application is visible to applicant AND landlord within a second — `landlordLeaseApplicationsRead` seeds on its own leaseapp's events; live-verified after cycling the Refractor.
- 2026-09-10 · `2a8d135e` · A new target's first dispatches before its grant projects are a bounded, self-healing install lag (≤ one mark lease; `Revoke`→`Enable` clears it at once) — documented in `_packages.md`; no mechanism.
- 2026-09-06 · `b5c8e7a8` + `bdfb344d` · A resident who owes the café is told so — `cafeArrearsReminders` sends one reminder per arrears episode off a recorded `.arrears` due fact; live: 5 sent, 2 timers armed, grid + statement show it.
- 2026-09-06 · `ac80bf74` · A name-only patient can be connected to a login — `BindPatientIdentity` (unclaimed identity only, name moved onto the identity's `.name`), `UnbindPatientIdentity` as the operator repair, FE Connect-a-login ceremony.
- 2026-09-06 · `82e26df5` · Staff can re-issue an unclaimed applicant's claim secret — Applicants & tenants panel offers `RotateClaimKey` via the shared ceremony; New-applicant button follows its grant (staff, not landlord).
- 2026-09-06 · `beb0fc12` · The desk reaches a retired studio's classes — `wellnessSessions` falls back to the session's `atLocation` snapshot, `ReassignSession` re-snapshots on a move, repair form operator-only.
- 2026-09-06 · `beb0fc12` · The class card quotes the price the seat will charge — `cardPriceCents` applies CreateBooking's own rate rule (approved lease ⇔ `.tenancy`), "· resident rate" only when it differs.
- 2026-09-06 · `beb0fc12` · The desk's class picker leads with what is still to run, past classes behind an optgroup, nothing dropped.
- 2026-09-06 · `beb0fc12` · A manual charge needs a memo — `post_entry` refuses a ref-less debit without one, the staff descriptor requires it, the FE refuses a blank note like a zero amount.
- 2026-09-06 · `28259217` · A recurring class is called off in one act — `wellnessSessions` projects `seriesKey`; `TombstoneSessionSeries` cancels every still-upcoming occurrence at the confirmed studio; 52 occurrences in 36 ms.
- 2026-09-06 · `656fc802` · The 24h class reminder shows where it was sent — `wellnessBookings` projects `reminderSentAt`, My Classes + roster badge it; `TombstoneSession`'s op-meta now declares its required reads.
- 2026-09-06 · `ef12f478` · `wellnessMemberAccounts` anchors on the identity it keys on — per-anchor seeding/retraction, `DiffRetraction` dropped, five refractor pins moved to closed/derivation; live 6 rows → 6.

- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
