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
| **A visit inside a provider's later-declared time-off is reminded, then swept to no-show** | `SetProviderTimeOff` writes the provider only; the reminder's gate reads status alone; the sweep's no-op latches the anchor. Record the displacement on the visit; reminder + sweep read it; the patient told via a third `appointmentChangeNotices` gap. Live: 0. | Clinic | pkg + FE | ★★ | M | 🏗️ building · [design](../../../docs/reviews/clinic-time-off-displacement-2026-09-17.md) · next: Inc 1 clinic-domain |
| **A provider's hours and time-off live in UTC, not the clinic's clock** | `enforce_hours` compares `time.weekday`/`time.seconds_of_day` in UTC ([ddls.go:3010](../../../packages/clinic-domain/ddls.go:3010)) and the editor says so ([index.html:252](../../../cmd/clinic-app/web/index.html:252)): a Pacific clinic's 8–4 shifts an hour at each DST change, and a "day off" starts at 5 pm the evening before. A `timeZone` on the site, hours and time-off evaluated in it. | Clinic | pkg + FE | ★★ | M | 🚧 blocked-on: [lattice.md → Starlark time builtins have no zone](lattice.md) |
| **A patient with 38 no-shows books like anyone else** | The booking prompt reads arrears only ([app.js:3935](../../../cmd/clinic-app/web/app.js:3935)); no read model counts a patient's recent no-shows (`count(CASE …)` precedent: [cafe-domain lenses.go:450](../../../packages/cafe-domain/lenses.go:450)). A `noShowCount` over the last 90 days on the roster row and in the booking prompt. Live: Riley Chen 38 in 90 d, 17 booked ahead. | Clinic | pkg + FE | ★ | S | 📋 ready |

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

- **Rotation to date:** LoftSpace ×35, Clinic ×35, Café ×25, Wellness ×22.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-09-13:** Wellness — drove member + desk hats through schedule/book/cancel/roster/guest/release/call-off/billing/arrears/studios; a class that ran can be called off and a refund ages the wrong charge; filed 4.
- **2026-09-14:** LoftSpace — drove landlord + tenant + rival-applicant hats through portfolio/queue/apply/renewals/tasks/ledger/one-bill/browse; approval ignores the reviewed terms, an ended lease never frees its unit; filed 5.
- **2026-09-14:** Clinic — drove desk/provider/patient hats through roster/schedule/status/document/follow-ups/series/ledger; a checked-in patient is swept to no-show, a phantom visit takes a note, a follow-up is cleared by any visit, the desk can't see a debtor; filed 4.
- **2026-09-14:** Café — drove desk + resident hats through open/self-order/off-menu/void/settle/post/self-pay/menu; an amount-only void settles a tab below its receipt, an overdue debtor opens a tab unasked; filed 4.
- **2026-09-15:** Wellness — drove member + desk hats through schedule/book/waitlist/promotion/late-cancel/call-off/retire/billing; a waitlister seated inside the forfeit window pays and cannot back out, Retire is unguarded; filed 4.
- **2026-09-15:** LoftSpace — drove landlord + tenant + applicant hats through portfolio/queue/ledger/renewals/tasks/browse/edit-listing; the landlord's ledger buttons and Edit listing are refused, no notice, no deposit; filed 5.
- **2026-09-15:** Clinic — drove desk/provider/patient hats through schedule/reschedule/status/document/series/ledger/arrears; a moved visit keeps its check-in, a debtor is never reminded, a note is rewritten without trace; filed 5.
- **2026-09-16:** Café — drove resident + desk hats through self-order/open/charge/settle/posting/counter-payment/arrears/menu; a self-order is half a loop, paying at the counter is refused until the posting; filed 5.
- **2026-09-16:** Wellness — drove member + desk hats through book/hold/pay/waitlist/promotion/reprice/resize/walk-in/attendance/call-off; a promoted member hears nothing, a series runs out, a walk-in is refused; filed 5.
- **2026-09-16:** LoftSpace — drove landlord + tenant hats through portfolio/queue/renewals/ledger/browse/maintenance/residence; a lease never moves the tenant in, a reported issue is never queued; filed 5.
- **2026-09-17:** Clinic — drove desk/provider/patient hats through schedule/status/time-off/reminders/series/encounters/arrears; a displaced visit is swept to no-show, a desk cancel tells nobody, hours live in UTC; filed 5 (+1 Lattice).
- **Next:** Café.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-09-16 · `6e038e54` · Tab and visit times render as the viewer's local date-time — one `localDateTime` formatter on every card's `openedAt`/`settledAt`/`startsAt`; goja-pinned.
- 2026-09-16 · `0cfa1e11` · A self-order says when it was ordered and whether it was served — `orderedAt`, `servedAt`, `MarkLineServed`, the desk's Orders panel; live ([design](../../../docs/reviews/cafe-self-order-fulfilment-2026-09-16.md)).
- 2026-09-16 · `0f769e58` · A self-booking patient holds one open visit per provider per day — `patientSelfDayClaim` on the self path, moved by a reschedule, freed on every terminal transition; `SelfBookingLimit`; desk unrestricted; refused live.
- 2026-09-16 · `99615546` · A clinical note is amended, not rewritten — the prior text stays in the encrypted record, `documentedAt` preserved, `amendedAt` projected ([design](../../../docs/reviews/clinic-visit-record-integrity-2026-09-16.md)).
- 2026-09-16 · `99615546` · A moved visit returns to scheduled — `RescheduleAppointment` re-stamps a live non-terminal `.status`; `MarkPastDueNoShow` is a no-op before the visit's end and its playbook declares its reads; live.
- 2026-09-16 · `99615546` · A patient confirms their own visit — the consumer self grant admits `confirmed` before start (refused over a check-in, once started; a re-confirm writes nothing); Confirm on the patient card; live via the Gateway.
- 2026-09-16 · `e880c00f` · A clinic debtor is reminded — `.arrears` + `EvaluateClinicArrears` + a Weaver target; the desk sees the debt beside Check in, no op refuses care ([design](../../../docs/reviews/clinic-arrears-reminder-2026-09-15.md)).
- 2026-09-15 · `ec274abe` · A lease takes a security deposit — `depositAmount` on the listing, `.deposit` at approval, a `oneTime` clause billed, returned on `endedAt` ([design](../../../docs/reviews/loftspace-security-deposit-2026-09-15.md)).
- 2026-09-15 · `6e6ea0f3` · A tenant gives notice and a lease ends early — `GiveNotice` records `.notice`, the term ends at `termEnd`, the renewal closes; live ([design](../../../docs/reviews/loftspace-tenancy-notice-2026-09-15.md)).
- 2026-09-15 · `c44e1f44` · "Rent owed" says how late — `.arrears` on the rent account, one reminder per episode after a 5-day grace; N days overdue on every surface ([design](../../../docs/reviews/loftspace-rent-arrears-2026-09-15.md)).

- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
