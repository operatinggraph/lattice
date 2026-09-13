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
| **The executed lease still doesn't name its tenant** | The applicant's name is a sensitive aspect on a linked identity, out of reach of the subject-rooted egress template, so the document renders the identity key. Andrew's 2026-09-01 fallback direction is sound Loom/Processor-side but the bridge can't decrypt a non-identity-holder `$sensitiveRef` — a second primitive gap. | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) bridge egress row (✅ ratified 2026-09-12 · seq: Inc 1) · [design](../../implementation-artifacts/retention-class-egress-envelope-design.md) |
| **29 of 63 appointments carry no site and `clinicSiteBackfill` closes the gap for none** | 16 have a provider at EXACTLY ONE site — the shape the op documents as convergent — yet stay unlinked; a Weaver engine gap, not a query bug. | Clinic | platform | ★★ | S–M | 🚧 Andrew-gated: run `lattice weaver replay-target clinicSiteBackfill --actor <loupe-operator.json actor>` from the main checkout (2026-09-06: the unattended session's classifier refuses the live verb) · [§17](../../implementation-artifacts/weaver-decline-retry-substrate-native-design.md) |
| **8 bookings stand `booked` on live classes that ended weeks ago** | `pastDueBookings` flipped one booking 1s after its class ended yet has never dispatched 8 older ones (`endsAt` 2026-07-20…08-23) — the same already-Acked-forever shape the clinic site row names. | Wellness | platform | ★ | S | 🚧 Andrew-gated: same verb as the clinic row, target `pastDueBookings` · [§17](../../implementation-artifacts/weaver-decline-retry-substrate-native-design.md) |
| **A protected identity can be booked but never cancelled** | The seed's "$15 guest" `gk12KR…sb5c` is the primordial admin identity (`data.protected`): `CreateBooking` accepts it, `CancelBooking` is refused at commit (`ProtectedKey` on its slot-cell tombstone); 5 live bookings can only age out. | Wellness | pkg | ★ | S | 📐 needs designer pass · no-pattern: a package-side refusal of a `data.protected` root as an op's subject, or a kernel rule letting a package release the cells it wrote · [triage §3 close](../../../docs/reviews/verticals-designer-triage-2026-09-10.md) |
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

- **Rotation to date:** LoftSpace ×31, Clinic ×31, Café ×22, Wellness ×19.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-09-02:** Clinic — drove front-desk/provider/patient hats through book/document/complete/follow-up/pay; a patient can't pay their own bill and the follow-up worklist is always empty; filed 3 + 1 platform.
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
- **Next:** LoftSpace.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-09-13 · `5f9888b1` · A recurring class moves to a new weekday/time in one act — `ReassignSessionSeries` shifts every still-upcoming occurrence at the confirmed studio by one delta, cells one batch per hub, pinned to the anchor the desk saw.
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
- 2026-09-06 · `eca8a407` · An automated refund is recorded as money handed back — third `refund` reason on the ledger enum, passed by `wellnessRefundSettlement`, refused on a self-scoped credit, badged "(refunded)".
- 2026-09-06 · `859e7503` · The New-instructor form is offered only to the operator hat that can submit it — `/api/staff-hats` reports `isOperator`, the FE gates the form and admits an operator-only session to Studios.
- 2026-09-06 · `e0c1b99c` · A waitlisted member is seated the moment their class has room — `wellnessWaitlistPromotion` target → `PromoteWaitlistedBookings` (all promotable, lowest slot first); live: capacity 1→3 seated the waitlist in 4 s.
- 2026-09-06 · `6b913413` · A follow-up documented without a target date leads the clinic worklist until one is set — never auto-addressed, "Set date" opens the documentation modal; live: Riley Chen's 7 August visits surface.
- 2026-09-05 · `d49f77ed` · Clinic self-pay cap's `.balance` is hydrated by the script's own `derive_reads`; legacy replay only for a self-pay, after the ownership proof; whole-cents money (found by the café unit, fixed same run).
- 2026-09-05 · `e01be391` · A house-tab payment can never exceed what is owed — every `CreditCafeAccount` leg capped against a platform-hydrated `.balance` cache (clinic mirror + `derive_reads`); refusals proven live.
- 2026-09-05 · `b2c4ea38` · A menu item can be renamed or repriced in place — `UpdateMenuItem` rewrites `.price` under OCC, Manage Menu gets an Edit form; proven live via the Gateway and restored.
- 2026-09-05 · `2e73d122` · A resident who paid ahead is no longer chased for charges the credit covered — `deriveStatement` carries the surplus forward and prepays later debits in order; six never-in-credit live debtors unchanged.
- 2026-09-05 · `84298db5` · A settled café charge can be refunded — `RefundCafeCharge` posts a credit that `reverses` the charge (same account, debit only, CAS-pinned cap, never self-scoped); statement badge + desk Refund button; proven live.

- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
