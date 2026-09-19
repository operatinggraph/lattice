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
| **A tenant's Facet has no Report-an-issue it can submit** | `ReportIssue` carries one descriptor (`standing`, `{me.workplace}`) and a `consumer` self grant; `edgeCatalog` withholds a self grant on a standing descriptor, so a tenant's Facet offers nothing; the tenant leg lives in loftspace-app alone. One op-meta needs a dispatch variant per scope (self: `{me.residence}`, target = me). | Cross-vertical | platform | ★ | M | 📐 needs designer pass · no-pattern: per-grant-scope dispatch variants on one op-meta · [design](../../../docs/reviews/loftspace-maintenance-loop-2026-09-17.md) |
| **A queued work order is claimable from any building** | A role queue is role-only (`lnk.task.<id>.queuedFor.role.<r>`); `ClaimTask` checks `holdsRole` and `ResolveWorkOrder`'s task path skips the workplace walk, so `backOfHouse` at building B claims and resolves building A's order. Live: one building. A queue scoped to a location. | Cross-vertical | platform | ★★ | M | 📐 needs designer pass · no-pattern: location-scoped role queue (queuedFor a role AT a place) · [design](../../../docs/reviews/loftspace-maintenance-loop-2026-09-17.md) |
| **Contract #10 weaver: `assignTask` takes `assignee \| queue`** | The runtime accepts a role-queue endpoint on an `assignTask` gap (maintenance's `missing_task` dispatches it); the action-table row in `docs/contracts/10-orchestration-weaver.md` is edited in `main`, uncommitted — the diff is the proposal. | Cross-vertical | platform | ★★ | XS | 📐 awaiting-Andrew · ratify + commit the contract edit · [design §2](../../../docs/reviews/loftspace-maintenance-loop-2026-09-17.md) |
| **A provider's hours and time-off live in UTC, not the clinic's clock** | `enforce_hours` compares `time.weekday`/`time.seconds_of_day` in UTC ([ddls.go:3010](../../../packages/clinic-domain/ddls.go:3010)) and the editor says so ([index.html:252](../../../cmd/clinic-app/web/index.html:252)): a Pacific clinic's 8–4 shifts an hour at each DST change, and a "day off" starts at 5 pm the evening before. A `timeZone` on the site, hours and time-off evaluated in it. | Clinic | pkg + FE | ★★ | M | 🚧 blocked-on: [lattice.md → Starlark time builtins have no zone](lattice.md) |
| **The house limit resets on settle** | `house_tab_limit` bounds one tab's `totalCents` ([ddls.go:1612](../../../packages/cafe-domain/ddls.go:1612)); a resident settles (no payment changes hands) and reopens in two taps, and the credit hold arms only after a reminded 30-day due date. Bound the open balance + tab, or a per-day tally. Live: settle 20:35:54 → new tab 20:44. | Café | pkg | ★★ | S | 🏗️ building · [amendment](../../../docs/reviews/cafe-house-tab-limit-2026-09-16.md) · next: Inc 1 package |
| **The POS picks a resident from 58 leases in NanoID order** | `computeLeases` sorts by lease key ([leases.go:46](../../../cmd/cafe-app/leases.go:46)) and `fillLeaseSelect` renders one flat `<select>` ([app.js:873](../../../cmd/cafe-app/web/app.js:873)): 49 disabled seed rows interleaved with 6 usable, no open-tabs-first, no type-to-find. Live: the one open tab sat 19th. | Café | FE | ★★ | S | 🏗️ building · [brief](../../../docs/reviews/cafe-house-tab-limit-2026-09-16.md) · next: Inc 2 app |
| **The café statement stamps every entry in raw UTC** | `postedAt` renders verbatim ([app.js:2515](../../../cmd/cafe-app/web/app.js:2515)) beside a localized *Due 9/30/2026* and *Opened Sep 17, 2026, 1:40 PM*; the `localDateTime` formatter covers the cards only. | Café | FE | ★ | XS | 🏗️ building · [brief](../../../docs/reviews/cafe-house-tab-limit-2026-09-16.md) · next: Inc 2 app |
| **A running series cannot start rolling** | `rolling` rides `CreateSessionSeries` only ([ddls.go:664](../../../packages/wellness-domain/ddls.go:664)); a series minted before it, or created without it, is retyped from scratch to keep going. Live: Evening Flow with Sam — Riverside's only series — ends Sep 23 with `extendAt: null`; the studio card's "runs out" is the whole remedy. | Wellness | pkg + FE | ★★ | S | 📋 ready · [rolling design](../../../docs/reviews/wellness-rolling-series-2026-09-16.md) |
| **The resident rate is any live tenancy, not one at the studio's building** | `CreateBooking`/`JoinWaitlist` grant `rate=resident` on a live lease + tenancy + `applicationFor` link and never walk the lease's unit to the studio's `locatedAt` building ([ddls.go:5756](../../../packages/wellness-domain/ddls.go:5756)). Live: Priya Raman's lease at building J5Bu… booked Riverside (A9jn…) at $0. | Wellness | pkg | ★ | S | 📋 ready |
| **A class of ~120+ seated members cannot move atomically** | Migrating N bookers' cells costs 8N mutations against the 998-per-batch ceiling and walks N `.status` reads near the 250 ms script wall; `MoveTooLarge` names the bound. Census 2026-09-18: max live class size 1. | Wellness | platform | ★ | M | 📐 needs designer pass · no-pattern: a paged multi-batch mutation of one class's seat guards · [design](../../../docs/reviews/wellness-moved-class-2026-09-18.md) |
| **An instructor has no time-off, and a rolling series keeps minting their classes** | No availability concept exists (grep: 0); `ExtendSessionSeries` claims the instructor's cells into any absence, and the desk clears or subs each class by hand. Clinic's provider time-off + displacement is the precedent. | Wellness | pkg + FE | ★ | M | 📋 ready · [clinic precedent](../../../docs/reviews/clinic-time-off-displacement-2026-09-17.md) |
| **A co-applicant is a name on the form, never a resident** | `SubmitQualificationProfile` stores `coApplicantName`/`coApplicantContact` as strings ([scripts.go:1582](../../../packages/lease-signing/scripts.go:1582)); the second adult on the lease has no identity, no `residesIn` (so no Report-an-issue, no services), no statement. Mint an unclaimed identity at approval + wire the residence spine, as `seedTenant` does. | LoftSpace | pkg + FE | ★★ | M | 📋 ready · [residence spine](../../../docs/reviews/loftspace-residence-spine-2026-09-16.md) |

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

- **Rotation to date:** LoftSpace ×36, Clinic ×35, Café ×26, Wellness ×23.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-09-14:** Café — drove desk + resident hats through open/self-order/off-menu/void/settle/post/self-pay/menu; an amount-only void settles a tab below its receipt, an overdue debtor opens a tab unasked; filed 4.
- **2026-09-15:** Wellness — drove member + desk hats through schedule/book/waitlist/promotion/late-cancel/call-off/retire/billing; a waitlister seated inside the forfeit window pays and cannot back out, Retire is unguarded; filed 4.
- **2026-09-15:** LoftSpace — drove landlord + tenant + applicant hats through portfolio/queue/ledger/renewals/tasks/browse/edit-listing; the landlord's ledger buttons and Edit listing are refused, no notice, no deposit; filed 5.
- **2026-09-15:** Clinic — drove desk/provider/patient hats through schedule/reschedule/status/document/series/ledger/arrears; a moved visit keeps its check-in, a debtor is never reminded, a note is rewritten without trace; filed 5.
- **2026-09-16:** Café — drove resident + desk hats through self-order/open/charge/settle/posting/counter-payment/arrears/menu; a self-order is half a loop, paying at the counter is refused until the posting; filed 5.
- **2026-09-16:** Wellness — drove member + desk hats through book/hold/pay/waitlist/promotion/reprice/resize/walk-in/attendance/call-off; a promoted member hears nothing, a series runs out, a walk-in is refused; filed 5.
- **2026-09-16:** LoftSpace — drove landlord + tenant hats through portfolio/queue/renewals/ledger/browse/maintenance/residence; a lease never moves the tenant in, a reported issue is never queued; filed 5.
- **2026-09-17:** Clinic — drove desk/provider/patient hats through schedule/status/time-off/reminders/series/encounters/arrears; a displaced visit is swept to no-show, a desk cancel tells nobody, hours live in UTC; filed 5 (+1 Lattice).
- **2026-09-17:** Café — drove resident + desk + staff-resident hats through open/self-order/serve/settle/re-open/write-off/POS/statement; a settled tab bills an unmade order, a staffer waives their own debt; filed 5.
- **2026-09-18:** Wellness — drove member + desk hats through book/waitlist/call-off/instructor-clear/resident-rate/series-horizon; a moved class strands its members' hour, a sub tells nobody, a series cannot start rolling; filed 5.
- **2026-09-18:** LoftSpace — drove landlord + tenant hats through portfolio/rent-owed/ledger/maintenance/renewals/browse; a tenant's report vanishes, a reversal ages like a payment, lateness is free; filed 5.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-09-18 · `de5a9f26` · A reversal retires the charge it names — `CreditAccount{reversesRef}` + `LinkReversal`; head, statement, console net it; live ([design](../../../docs/reviews/loftspace-ledger-reversal-and-late-fee-2026-09-18.md)).
- 2026-09-18 · `de5a9f26` · Lateness costs what the lease says — `SetLateFee` on the lease, a `perArrearsEpisode` lateFee clause minted by convergence, billed once per episode on the reminder's commit; Riley Chen's $50 term live.
- 2026-09-18 · `a8d26b2e` · A tenant's report stays on their card — a `reportedBy` link (legacy backfilled), a reporter-anchored read, a resolved notice; live ([design](../../../docs/reviews/loftspace-maintenance-loop-closes-2026-09-18.md)).
- 2026-09-18 · `a8d26b2e` · A landlord closes a work order at their own unit — `ResolveWorkOrder` self leg on the manages link, the queued task cancelled by convergence, who reported it + Resolve on the console; live.
- 2026-09-18 · `3abf4b97` · A moved class carries its members' hour with it — both Reassign ops move every booker's cells in the batch, `BookerConflict` refuses a clash; live ([design](../../../docs/reviews/wellness-moved-class-2026-09-18.md)).
- 2026-09-18 · `3abf4b97` · A class handed to a sub, un-led, or moved to another room tells its members once — two stamps on the schedule, two more notice kinds, My Classes says who leads; live. `727abcad` mints `lint-gap-params-optional-hop`.
- 2026-09-18 · `5e86a342` · Nobody clears their own debt from the desk — `SelfClearing` on a waiver/refund/payout of one's own account, café + clinic + wellness; live ([design](../../../docs/reviews/cafe-self-clearing-2026-09-18.md)).
- 2026-09-18 · `a97c193c` · A tab closes only over made orders — `Settle` refuses `UnservedLines` on both human legs, the 24 h sweep voids the unmade lines; live ([design](../../../docs/reviews/cafe-settle-unserved-lines-2026-09-18.md)).
- 2026-09-17 · `14db5c33` · A repeat no-show is named at booking — `no_show_count` + `last_no_show_at` on the roster row, the desk asked at two; live ([design](../../../docs/reviews/clinic-no-show-history-2026-09-17.md)).
- 2026-09-17 · `3464e817` · A visit inside a provider's later-declared time-off records its displacement — reminder and sweep stand down, the patient told once; live ([design](../../../docs/reviews/clinic-time-off-displacement-2026-09-17.md)).
- 2026-09-17 · `fa8c1e53` · A deposit comes back net of its deductions, a credit balance is paid out — `RecordDepositDeduction` · `PayOutBalance`; live ([design](../../../docs/reviews/loftspace-security-deposit-2026-09-15.md)).
- 2026-09-17 · `8df02fde` · A visit the desk cancels or moves tells the patient once — `appointmentChangeNotices` keyed on the recorded stamps; live ([design](../../../docs/reviews/clinic-status-clock-and-change-notices-2026-09-17.md)).
- 2026-09-17 · `8df02fde` · An appointment's status carries its moment and its author — `.status {at, by}` stamped at a value change, carried on a same-value write; "Checked in 12 min ago" on the card.
- 2026-09-17 · `b9e02daa` · A unit turns over cleanly — availability floored at the recorded end, `MoveInBeforeAvailable`, a terminal application frees its pair; live ([design](../../../docs/reviews/loftspace-unit-turnover-2026-09-17.md)).
- 2026-09-17 · `ea15ca76` · A reported problem becomes work its tenant can raise and its landlord can see — `missing_task` queues it, a tenant self leg, a landlord panel ([design](../../../docs/reviews/loftspace-maintenance-loop-2026-09-17.md)).
- 2026-09-17 · `a8299552` · An approved lease moves the tenant in — `residesIn` wired at approval, released at `endedAt`; Jordan Ellis reaches Maple Laundry live ([design](../../../docs/reviews/loftspace-residence-spine-2026-09-16.md)).
- 2026-09-16 · `4a43c82d` · A recurring class keeps rolling — `.horizon` + a timer gap mints the next class as the window's earliest starts; `StopSessionSeries` stops it ([design](../../../docs/reviews/wellness-rolling-series-2026-09-16.md)).
- 2026-09-16 · `d98faf8e` · The desk seats a walk-in until the class ends — desk leg admits to `endsAt`, `.status.bookedAt` closes the reminder gate for a seat claimed after start; live.
- 2026-09-16 · `d98faf8e` · A class cannot shrink under a claimed seat — `CapacityBelowSeated` on the highest claimed index, the form floors at the seated count; refused live.
- 2026-09-16 · `d98faf8e` · A seat pays the price it was claimed at — `.status.priceCents` at claim + seating, `coalesce` in both lenses, the refund reads it; $12 held through a $15 reprice live.
- 2026-09-16 · `f139d128` · A seat promoted, moved, or called off tells its member — `wellnessBookingChangeNotices` + the release's own call-off notice; live ([design](../../../docs/reviews/wellness-booking-change-notices-2026-09-16.md)).
- 2026-09-16 · `bd722499` · Every ledger reminds a debtor of any history length — the arrears replay pages across dispatches; Riley Chen reminded live ([design](../../../docs/reviews/verticals-arrears-resumable-replay-2026-09-16.md)).
- 2026-09-16 · `71b71bc5` · A house tab has a recorded limit — `SetCafePolicy` on the location, tightest on the chain; self leg refused `TabLimitExceeded`, the desk rings past ([design](../../../docs/reviews/cafe-house-tab-limit-2026-09-16.md)).
- 2026-09-16 · `1144aec5` · The desk settles and takes the cash at once — `Settle{paidCents}`, `missing_payment` posts the tab-tied credit after the debit; Settle & pay; live ([design](../../../docs/reviews/cafe-counter-payment-2026-09-16.md)).
- 2026-09-16 · `1144aec5` · The arrears row takes a payment — *Take payment* beside *Write off*, a confirmed `CreditCafeAccount{reason: payment}` prefilled to the balance shown.

- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
