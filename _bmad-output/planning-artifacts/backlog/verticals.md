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

- **Rotation to date:** LoftSpace ×33, Clinic ×33, Café ×24, Wellness ×20.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-09-05:** LoftSpace — drove landlord + applicant hats through listings/apply/sign/decide/withdraw/renewals/tasks/ledger/one-bill/documents/search; a declined applicant keeps an executed lease and the landlord can't read one; filed 3.
- **2026-09-05:** Clinic — drove front-desk/provider/patient hats through register/book/status/encounters/series/follow-ups/ledger; a just-registered patient is invisible to the desk and can never claim their login; filed 3.
- **2026-09-05:** Café — drove front-of-house + resident hats through menu/POS/open/charge/void/settle/ledger/payment/refund/arrears; a credit balance ages later charges as long-overdue and a mis-keyed payment cannot be undone; filed 3.
- **2026-09-06:** Wellness — drove member + front-desk hats through schedule/book/cancel/roster/attendance/instructors/studios/ledger/arrears; a resident is quoted the walk-in price and a retired studio locks the desk out of its own classes; filed 4.
- **2026-09-13:** LoftSpace — drove landlord + tenant hats through portfolio/applications/renewals/tasks/ledger/withdraw; rent bills before move-in and past term end, a renewal's new rent never bills, the tenant's signature is refused; filed 4.
- **2026-09-13:** Clinic — drove desk/provider/patient hats through schedule/status/series/follow-ups/reschedule/ledger; a visit can be completed before it starts and a recurring visit is credited on the clock; filed 4.
- **2026-09-13:** Café — drove desk + resident hats through open/charge/void/self-order/settle/refund/arrears/menu; debt can't be written off, a refund is spent twice, no sales view; filed 4.
- **2026-09-13:** Wellness — drove member + desk hats through schedule/book/cancel/roster/guest/release/call-off/billing/arrears/studios; a class that ran can be called off and a refund ages the wrong charge; filed 4.
- **2026-09-14:** LoftSpace — drove landlord + tenant + rival-applicant hats through portfolio/queue/apply/renewals/tasks/ledger/one-bill/browse; approval ignores the reviewed terms, an ended lease never frees its unit; filed 5.
- **2026-09-14:** Clinic — drove desk/provider/patient hats through roster/schedule/status/document/follow-ups/series/ledger; a checked-in patient is swept to no-show, a phantom visit takes a note, a follow-up is cleared by any visit, the desk can't see a debtor; filed 4.
- **2026-09-14:** Café — drove desk + resident hats through open/self-order/off-menu/void/settle/post/self-pay/menu; an amount-only void settles a tab below its receipt, an overdue debtor opens a tab unasked; filed 4.
- **Next:** Wellness.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-09-15 · `fd9cd157` · The desk takes an item off the menu for the day — `SetMenuItemAvailability` sets `.price.available`, both lenses project it, all three pickers grey it, `Charge` refuses `ItemUnavailable`; refused live.
- 2026-09-15 · `fd9cd157` · A posted charge opens to its receipt — `receiptLines` joins a ledger row to its settled tab by `tabKey`; the statement renders the lines (price, who ordered, voids) collapsed, resident and desk alike; live.
- 2026-09-15 · `a0ba03a2` · A reminded debtor opens no new tab — `OpenTab` refuses `CreditHold` off `.arrears.sentAt` (heldFor in-walk, both legs); picker badge "owes $X · N days overdue · credit hold", hold panel, confirm; refused live.
- 2026-09-15 · `a0ba03a2` · A tab's total always equals its live lines — `VoidCharge` voids by `lineId` only (amount-only form retired), itemsMemo re-derived, workplace confined before the line lookup; proven live at $0.
- 2026-09-14 · `aa48d51f` · A live read decides no bare mutation on its key — `lint-live-read-pinned-mutation`; 7 sites pinned; dossier class retired ([census](../../../docs/reviews/verticals-packages-occ-gates-2026-09-14.md)).
- 2026-09-14 · `284ffc66` · Every `derive_reads` op has a bare-envelope vector — `lint-derive-reads-bare-vector` (21 ops); seven derive_reads now derive the vertex root; dossier class retired; live.
- 2026-09-14 · `cfbc2fa0` · `_packages.md` dossier censused over the vertical packages — 33 unlimited `kv.Links` walks fixed; `lint-links-page-limit` gates it ([census](../../../docs/reviews/verticals-packages-dossier-census-2026-09-14.md)).
- 2026-09-14 · `0771d77e` · The desk sees a debtor and a charge names its visit — `visitRef`→`forVisit`, a fee-less `settles` refused, `reversesRef` confined, the statement aged per charge, arrears on the picker; live.
- 2026-09-14 · `7a2809b8` · A follow-up is addressed only by a later visit with its own provider — `followUpReminders` folds `addressedAt` over the patient's visits and gates on it; `hasLaterVisit` carries the same conjuncts, goja-pinned; live.
- 2026-09-14 · `7a2809b8` · A clinical note needs a held visit — `RecordEncounter` reads `.schedule` + `.status` at every dispatcher and refuses `VisitNotHeld` (cancelled / noShow) and `NotYetStarted`; the phantom 09-15 instance refused live.
- 2026-09-14 · `7a2809b8` · Recorded arrival is never swept to no-show — `pastDueAppointments`' gap and `MarkPastDueNoShow` leave `checkedIn` alone (the lapse still records); the worklist's "Arrived, never closed" is the desk's observer.
- 2026-09-14 · `52212ced` · A thrown irreversible submit says the write may have landed — six hand-built catches stage `sent`/`confirmed`; the two-zones class censused 0/39 live, LoftSpace date pins run under a pinned LA zone.
- 2026-09-14 · `71912135` · A lost application is a recorded fact — `RecordApplicationLoss` writes `.decision = lost` off `missing_lossRecorded`; every liveness gate, `SignLease` and every landlord surface read it; 52/52 live rivals recorded.
- 2026-09-14 · `4fe3ecef` · A losing applicant is told the unit went to someone else — both application read lenses project `lost_to_rival`; banner + landlord row read it; expired and lost-application tasks read-only; all 52 live rivals read it.
- 2026-09-14 · `4fe3ecef` · A rent charge names its billing period and due date — `DebitAccount` stamps `periodStart`/`periodEnd`/`dueAt` on the `.entry` from the clause grid; both lenses project them; both statements say "covers … · due …".
- 2026-09-14 · `b56bc0f0` · The lease is signed on the applicant's terms — `.tenancy` derives from `.terms` (listing fallback, malformed terms refused) and records `rentAmount`; both cards state what the signature commits to; Priya approved live.
- 2026-09-14 · `b56bc0f0` · A lease that ends frees its unit — `tenancyEnd` records `endedAt = leaseEnd` via `EndTenancy` at the recorded lapse and relists; an ended tenancy is terminal in every consumer; proven through the real Weaver e2e.
- 2026-09-14 · `b56bc0f0` · A renewed tenant's home states the current lease — both application read lenses + `renewalsRead` project the tenancy, rendered by UTC calendar date; Jordan Ellis reads $2,125 · ends 2027-09-06 live.
- 2026-09-14 · `1402d742` · The desk sees what the café sold today — a Today panel folded from `/api/tabs` (tabs settled on the local day, gross, by item, voids, unitemized remainder), goja-pinned incl. the DST day; live.
- 2026-09-14 · `1402d742` · A moved-out resident's lease stops taking house tabs — `OpenTab` refuses `TenancyEnded` at `.tenancy.leaseEnd`; the POS picker says so first via `frontDeskLeaseDetails`; an open tab charges until the 24 h stale-settle.
- 2026-09-14 · `1b7b255a` · A class that ran is a record — `TombstoneSession` refuses `SessionStarted` once `startsAt <= submittedAt`, the desk hides "Call off this class" on history, the seed's litter reaper keeps started litter; refused live.
- 2026-09-14 · `1b7b255a` · The desk sees a debtor before booking them — "owes $X · N days overdue" on the member picker, guest typeahead and roster card from `/api/frontdesk-arrears`; an overdue booking asks first; live.
- 2026-09-14 · `1b7b255a` · A booking click holds "Booked" until the lens has the row — `awaitProjectedBooking` polls ≈31 s past the 24 s `wellnessBookings` lag, schedule + both desk paths; live.
- 2026-09-14 · `50987c67` · A credit says why it was posted — `CreditCafeAccount` gains `reason` (payment|waiver, waiver staff-only, capped at owed), the lens projects it, the desk writes off from the arrears row; proven live.
- 2026-09-14 · `50987c67` · Cash handed back is its own debit — `PayoutCafeCredit` pays out a credit (confined, never self-scoped, never refundable); credit may never exceed cash paid in (`cashCents`); Riley Chen paid out live.

- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
