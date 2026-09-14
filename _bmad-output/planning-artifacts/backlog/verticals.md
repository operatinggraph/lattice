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
| **The café can't say what it sold today** | POS is per-lease and the desk shows open tabs + arrears; no surface aggregates settled tabs. Staff `/api/tabs` already carries every tab's `lines` + `settledAt` ([tabs.go:65](../../../cmd/cafe-app/tabs.go:65)), so a Today panel (tabs, gross, by item, voids) is FE-only. Live: 23 charged tabs, no total anywhere. | Café | FE | ★★ | S | 📋 ready · desk Today panel folded from `/api/tabs` |
| **A moved-out resident can still run a house tab** | `OpenTab` reads `.decision` alone ([ddls.go:1061](../../../packages/cafe-domain/ddls.go:1061)); `.tenancy.leaseEnd` is never read, though rent now stops at it (`67799a7d`). Live: 0 of 6 approved leases expired — no instance yet. | Café | pkg | ★ | XS | 📋 ready · optionalRead `.tenancy`, refuse `TenancyEnded` past `leaseEnd` |
| **A class that already ran can be called off** | `TombstoneSession` has no started guard ([ddls.go:2892](../../../packages/wellness-domain/ddls.go:2892)) though the series call-off skips history for this reason ([:3087](../../../packages/wellness-domain/ddls.go:3087)); the desk offers it on all 48 past classes ([app.js:2051](../../../cmd/wellness-app/web/app.js:2051)). Live: a Sep 4 class called off Sep 14, accepted; with bookings the orphan sweep refunds a class that ran. | Wellness | pkg + FE | ★★ | S | 📋 ready · refuse `SessionStarted` past `startsAt`; hide the button on history |
| **The desk books a debtor with no word about the debt** | Roster cards + member/guest pickers carry no arrears badge and the arrears rows are inert text ([app.js:1877](../../../cmd/wellness-app/web/app.js:1877), [:2783](../../../cmd/wellness-app/web/app.js:2783)); the page already holds `/api/frontdesk-arrears`. Live: Sam Okafor ($60, 3 days overdue) and a $15 guest booked with no prompt. | Wellness | FE | ★★ | S | 📋 ready · "owes $X · N days" badge on picker + card, confirm on overdue; arrears row click selects the member |
| **"Booked." then nothing for 25 s** | The schedule re-renders ONCE at 700 ms ([app.js:925](../../../cmd/wellness-app/web/app.js:925)) while `wellnessBookings` lands ~24 s after commit on this stack (cancel: 0.2 s); the card stays "0/12 · Book" until a manual Refresh. `awaitProjectedStatus` ([:1994](../../../cmd/wellness-app/web/app.js:1994)) is the in-app precedent. | Wellness | FE | ★ | XS | 📋 ready · hold the booked state + poll the row into view, as attendance does |
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

- **Rotation to date:** LoftSpace ×32, Clinic ×32, Café ×23, Wellness ×20.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-09-04:** Clinic — drove patient/provider/front-desk hats through appointments/encounters/visit-series/follow-ups/ledger/wellness-referral; four live cadences run on deleted patients and the referral picker is 92% dead classes; filed 3.
- **2026-09-04:** Café — drove front-of-house + resident hats through menu/POS/open/charge/void/settle/ledger/payment/arrears; two debtors have no name and every menu item shows twice; filed 3.
- **2026-09-04:** Wellness — drove member + front-desk hats through schedule/bookings/roster/attendance/reminders/ledger/arrears; a guest's $15 is unsettleable and two $25 no-show fees outlive the fix written for them; filed 3.
- **2026-09-05:** LoftSpace — drove landlord + applicant hats through listings/apply/sign/decide/withdraw/renewals/tasks/ledger/one-bill/documents/search; a declined applicant keeps an executed lease and the landlord can't read one; filed 3.
- **2026-09-05:** Clinic — drove front-desk/provider/patient hats through register/book/status/encounters/series/follow-ups/ledger; a just-registered patient is invisible to the desk and can never claim their login; filed 3.
- **2026-09-05:** Café — drove front-of-house + resident hats through menu/POS/open/charge/void/settle/ledger/payment/refund/arrears; a credit balance ages later charges as long-overdue and a mis-keyed payment cannot be undone; filed 3.
- **2026-09-06:** Wellness — drove member + front-desk hats through schedule/book/cancel/roster/attendance/instructors/studios/ledger/arrears; a resident is quoted the walk-in price and a retired studio locks the desk out of its own classes; filed 4.
- **2026-09-13:** LoftSpace — drove landlord + tenant hats through portfolio/applications/renewals/tasks/ledger/withdraw; rent bills before move-in and past term end, a renewal's new rent never bills, the tenant's signature is refused; filed 4.
- **2026-09-13:** Clinic — drove desk/provider/patient hats through schedule/status/series/follow-ups/reschedule/ledger; a visit can be completed before it starts and a recurring visit is credited on the clock; filed 4.
- **2026-09-13:** Café — drove desk + resident hats through open/charge/void/self-order/settle/refund/arrears/menu; debt can't be written off, a refund is spent twice, no sales view; filed 4.
- **2026-09-13:** Wellness — drove member + desk hats through schedule/book/cancel/roster/guest/release/call-off/billing/arrears/studios; a class that ran can be called off and a refund ages the wrong charge; filed 4.
- **Next:** LoftSpace.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-09-14 · `50987c67` · A credit says why it was posted — `CreditCafeAccount` gains `reason` (payment|waiver, waiver staff-only, capped at owed), the lens projects it, the desk writes off from the arrears row; proven live.
- 2026-09-14 · `50987c67` · Cash handed back is its own debit — `PayoutCafeCredit` pays out a credit (confined, never self-scoped, never refundable); credit may never exceed cash paid in (`cashCents`); Riley Chen paid out live.
- 2026-09-13 · `5e6e08a0` · A reversal is aged against the charge it reverses — a `reversesKey` credit retires its debit before the FIFO, lockstep across both app statements and café's `arrears_head`; orphan double refund closed; live.
- 2026-09-13 · `bd07bed1` · A patient's late cancel owes the no-show fee and the desk's correction bills it — a 24h self-path clock, the correction carries the fee, the ledger bills the fee's presence; proven live incl. the waiver reversal.
- 2026-09-13 · `a2724692` · A recurring visit is credited by a booked visit, never by the clock — the gap opens only on a qualifying visit, the advance re-anchors on it (`StaleRow`+OCC), the Book calendar carries the due floor; proven live.
- 2026-09-13 · `8a046814` · The renewal chain asks the tenant for a profile before a signature — a `submitProfile` leg gates `signRenewal`'s `pre`, three `staleUserTasks` arms, the inbox opens the profile form; the refused task signed live.
- 2026-09-13 · `8a046814` · `SetApplicantProfile`'s descriptor declares `references` as the DDL's string array, not an integer count the script dropped to zero.
- 2026-09-13 · `b1ad58ae` · A visit is completed or missed only once it has started — `SetAppointmentStatus` + `CorrectAppointmentStatus` refuse `completed`/`noShow` `NotYetStarted` before `startsAt`; the desk's buttons wait; proven live.
- 2026-09-13 · `b1ad58ae` · A final visit is never moved — `RescheduleAppointment` reads `.status` and refuses `TerminalStatus` before the cell diff; proven live on a cancelled appointment.
- 2026-09-13 · `0ec264a5` · An executed lease is never withdrawn — `WithdrawLeaseApplication` refuses `AlreadyApproved` on an approved `.decision` (an optionalRead at every dispatcher); a declined one stays withdrawable; proven live.
- 2026-09-13 · `67799a7d` · Rent bills from leaseStart to leaseEnd — the clause carries its term, dues walk the anniversary grid; six live clauses termed by `BackfillClauseTerm`, four pre-/post-term charges reversed.
- 2026-09-13 · `67799a7d` · A signed renewal's rent reaches the bill — `SignRenewal` records `termStart`/`rentAmount` on `.tenancy` and `leaseRentSettlement` mints the renewal's own clause for [termStart, leaseEnd).
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

- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
