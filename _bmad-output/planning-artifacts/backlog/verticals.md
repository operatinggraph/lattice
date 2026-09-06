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
| **The executed lease still doesn't name its tenant** | The applicant's name is a sensitive aspect on a linked identity, out of reach of the subject-rooted egress template, so the document renders the identity key. Andrew's 2026-09-01 fallback direction is sound Loom/Processor-side but the bridge can't decrypt a non-identity-holder `$sensitiveRef` at all — a real second primitive gap. | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md] bridge egress retention-class gap (filed 2026-09-02) |
| **29 of 63 appointments carry no site and `clinicSiteBackfill` closes the gap for none** | 16 have a provider at EXACTLY ONE site — the shape the op documents as convergent — yet stay unlinked; a Weaver engine gap, not a query bug. | Clinic | platform | ★★ | S–M | 🚧 blocked-on: live `ReplayTarget clinicSiteBackfill` run · fix + fresh binaries confirmed 2026-09-05, needs an operator holding `ctrl.weaver.replayTarget` · [§17](../../implementation-artifacts/weaver-decline-retry-substrate-native-design.md) |
| **8 bookings stand `booked` on live classes that ended weeks ago** | `pastDueBookings` flipped one booking 1s after its class ended yet has never dispatched 8 older ones (`endsAt` 2026-07-20…08-23) — the same already-Acked-forever shape the clinic site row names. | Wellness | platform | ★ | S | 🚧 blocked-on: live `ReplayTarget pastDueBookings` run · same fix + operator-grant gap as the clinic row above · [§17](../../implementation-artifacts/weaver-decline-retry-substrate-native-design.md) |
| **A submitted application is invisible to applicant AND landlord, and the app calls the projection healthy** | A live `CreateLeaseApplication` committed 12:54Z was absent from `read_lease_applications` and `read_landlord_lease_applications` at 13:03Z; both answered `projectionHealthy: true`. False-healthy symptom fixed `d8cf4144`; the applicant lens licence-fixed since, landlord lens not. | LoftSpace | FE + pkg | ★★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `landlordLeaseApplicationsRead` partitionability row (old pointer was dangling) · applicant half needs a live re-test |
| **Front desk has no way to see which booked appointments a provider's time off now conflicts with** | `SetProviderTimeOff` writes only `.timeOff` — nothing tells front desk which of a provider's existing bookings now need a reschedule call. | Clinic | pkg | ★ | S | 📋 ready · sketch: walk the provider's appointments (`kv.Links(providerKey,"withProvider","in")`) at `SetProviderTimeOff`, mark overlapping non-terminal ones, project the marker for an FE badge |
| **The front desk is the only hat that sees "New instructor", and the op refuses it every time** | The instructors panel sits inside the Studios tab, gated `isStaff()` = worksAt + frontOfHouse ([app.js:270](../../../cmd/wellness-app/web/app.js:270)), while `CreateInstructor` is operator-only by design (mirroring clinic's `CreateProvider`). Live: 1 instructor for 2 studios, 31 of 48 sessions instructor-less. | Wellness | FE | ★ | XS | 📋 ready · mirror clinic's shipped `isOperator` hat (`ac4add98`): expose it from `/api/staff-hats`, gate the panel on it |
| **A waitlisted member is stranded the moment the desk makes room for them** | `find_promotion_candidate` runs in `CancelBooking` alone ([ddls.go:3870](../../../packages/wellness-domain/ddls.go:3870)), so raising a full class's capacity seats nobody. Live on `vtx.session.aMPYzLZWRXkBEFrxuwav`: capacity 1→5, 4 seats free, `waitlistSlot 1` unchanged; the member's button stays disabled and a desk `CreateBooking` for them is refused `BookerConflict`. | Wellness | pkg + FE | ★★ | S | 📋 ready · free-seat × live-waitlist target mirroring `wellnessOrphanedBookingSettlement` |
| **A recurring class is created in one act and can only be undone six** | `CreateSessionSeries` mints every occurrence eagerly, but no op accepts a `vtx.sessionseries` key and `wellnessSessions` projects no series field — so the occurrences render as indistinguishable one-offs. Live: a 6-week series scheduled in one submit took 6 separate `TombstoneSession` submits to call off, and a term-wide time move would take 6 more. | Wellness | pkg + FE | ★★ | S–M | 📋 ready · project the `partOf` parent onto the session lens, then a series-scoped tombstone/reassign |
| **An automated refund is recorded as cash the member handed over** | `wellnessLedgerHistory`'s `reason` enum is `payment`/`waiver` only ([ddls.go:144](../../../packages/wellness-ledger/ddls.go:144)), so `wellnessRefundSettlement`'s reversal posts `reason: "payment"` (live `vtx.wellnesstransaction.WKNvWBNLH4a8CRCxpKef`) and the FE badges only `waiver`. Takings cannot be told apart from money given back. | Wellness | pkg + FE | ★ | XS | 📋 ready · a third `refund` reason the settlement passes and the statement badges |
| **`wellnessMemberAccounts` anchors on the booking it does not key on** | Anchored on `bk:booking`, keyed on `id.key`, so it partitions by nothing the engine can seed — every event is a whole-corpus rescan plus a whole-bucket diff, and no Refractor conjunct can admit it. Re-anchor on `id:identity` with `bookedBy` as an existence test and drop `DiffRetraction`. | Wellness | pkg | ★ | XS | 📋 ready · [why](../../implementation-artifacts/anchor-partitioned-plain-lens-retraction-design.md) §8 row 2 |
| **A guest's debt outlives the booking that made them visible** | Lease-less coverage comes from `wellnessBookers` (live bookings only) and `CancelBooking` tombstones the booking ([ddls.go:3872](../../../packages/wellness-domain/ddls.go:3872)), so a late-cancel forfeit stands on someone no desk can reach. Census 2026-09-05: 4 debtors, 0 in this shape; an absence-routed fallback was refused at review (fail-open, and it hands the picker a booking write). | Wellness | pkg | ★ | S | 📐 needs designer pass · no-pattern: a charge that records the studio it was posted at |
| **Nothing ever tells a resident they owe the café money** | Café's only Weaver targets are `cafeTabSettlement` + `cafeStaleTabSettlement`, while clinic carries `appointmentReminders` + `followUpReminders`, wellness `wellnessBookingReminders` and LoftSpace `leaseExpiry`. 3 of 7 café debtors sit 12–19 days past the 15-day net term with nothing sent to either the resident or the desk. | Café | pkg | ★ | S–M | 📋 ready · an arrears convergence target off `cafeLedgerHistory`'s aged balance, mirroring `wellnessBookingReminders` |
| **7 of the clinic's 8 follow-up requests will never come due, and nothing surfaces them** | Live: 7 completed August visits carry `followUpRequested` with no `followUpDate`, and the worklist only ever matches on the date — no hat can find them. The repair is reachable (`RecordEncounter` is a re-runnable upsert, [ddls.go:3418](../../../packages/clinic-domain/ddls.go:3418)) but nothing names the population needing it. | Clinic | pkg + FE | ★★ | S | 📋 ready · a requested-but-undated gap on the follow-up worklist |
| **The front desk is sent to collect from two residents it cannot name** | A tombstoned unit empties `cafeIdentitiesRead`'s workplace fan-out ([lenses.go:429](../../../packages/cafe-domain/lenses.go:429)), so the row keeps only its self-anchor and a staffer reads 46 of 118 names. Live: two debtors ($4.50/23d, $10.00/30d) render as truncated NanoIDs — the leases `cafeLeaseWorkplaces` already rescues via `missingLocation` (`097aa843`). | Café | pkg | ★★ | S | 📐 needs designer pass · no-pattern: a tab that records the café it was opened at (the wellness guest-debt row's gap) |
| **The 24h class reminder is sent and nothing anywhere says so** | 14 live bookings carry a `.reminder.data.sentAt` marker (14 a `.reminderNotification` too), but `wellnessBookings` projects no reminder column ([lenses.go:344](../../../packages/wellness-domain/lenses.go:344)) and `cmd/wellness-app/web/app.js` holds no reminder string at all — clinic badges "🔔 Reminder sent" on every appointment card from its own column ([app.js:4008](../../../cmd/clinic-app/web/app.js:4008)). | Wellness | pkg + FE | ★ | XS | 📋 ready · project `sentAt`, badge it like clinic |
| **Two of four tenants can never sign in, the roster already knows, and nothing re-issues their claim secret** | The roster serves `state` ([staff_identities.go:31](../../../cmd/loftspace-app/staff_identities.go:31)) but the FE reads it for names only ([app.js:848](../../../cmd/loftspace-app/web/app.js:848)); the mint shows the one-time secret once ([app.js:1030](../../../cmd/loftspace-app/web/app.js:1030)) and `RotateClaimKey` has no FE anywhere. Live: Jordan Ellis + Priya Raman. | LoftSpace | FE | ★★ | S | 📋 ready · offer `RotateClaimKey` |
| **A mis-keyed house-tab payment is unbounded, uncorrectable, and hides the resident from collections** | `CreditCafeAccount`'s descriptor promises the amount cannot exceed what is owed, "server-verified" ([opmetas.go:63](../../../packages/cafe-ledger/opmetas.go:63)); it is not. Live: $50 posted against $14.25, account now −$35.75 and off the arrears grid, with no op that undoes it. | Café | pkg + FE | ★★ | S | 🏗️ building · next: cap every `CreditCafeAccount` leg at a maintained `.balance` cache (clinic `c3af15a3` mirror) |
| **A patient registered with a name alone can never be connected to a login** | The FE mints an identity only when an email or phone is typed ([app.js:887](../../../cmd/clinic-app/web/app.js:887)); with a name alone `CreatePatient` writes `fullName` on the patient vertex and no `identifiedBy`, and nothing writes that link later — a provider gets `BindProviderIdentity` for this exact repair. The name then sits outside the identity's sensitive `.name` aspect, so a shred never reaches it. Live: Classic Demo Patient. | Clinic | pkg | ★★ | S | 📋 ready · a `BindPatientIdentity` |

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

- **Rotation to date:** LoftSpace ×31, Clinic ×31, Café ×22, Wellness ×18.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-09-02:** LoftSpace — drove landlord + applicant hats through listings/applications/renewals/tasks/ledger/one-bill/documents/search; finished work still sits in the inbox and the portfolio hides $10k of arrears; filed 2.
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
- **Next:** Wellness.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-09-05 · `b2c4ea38` · A menu item can be renamed or repriced in place — `UpdateMenuItem` rewrites `.price` under OCC, Manage Menu gets an Edit form; proven live via the Gateway and restored.
- 2026-09-05 · `2e73d122` · A resident who paid ahead is no longer chased for charges the credit covered — `deriveStatement` carries the surplus forward and prepays later debits in order; six never-in-credit live debtors unchanged.
- 2026-09-05 · `84298db5` · A settled café charge can be refunded — `RefundCafeCharge` posts a credit that `reverses` the charge (same account, debit only, CAS-pinned cap, never self-scoped); statement badge + desk Refund button; proven live.
- 2026-09-05 · `e9cffe9e` · A voided café item drops off the permanent statement — `itemsMemo` is derived from the tab's live non-voided lines at Charge/Void/Settle; live: 3 croissants, line 1 voided, posted `Croissant, Croissant` for $7.00.
- 2026-09-05 · `42c8eae8` · The desk is shown the claim secret it mints for a new patient (clinic) or guest (wellness) — the shared ceremony overlay on an accepted `CreateUnclaimedIdentity` reply; proven in-browser.
- 2026-09-05 · `8731eac5` · A called-off class releases every booking it strands — the orphan lens drops the aspect-anchor conjunct, the release op enumerates `forSession`/`bookedBy`; both legacy $25 fees refunded live.
- 2026-09-05 · `8731eac5` · A guest is visible to the desk of the class they booked — new booking-anchored `wellnessBookers` lens unioned into every front-desk confinement; live: member list, `/api/ledger`, arrears grid (3 → 4 debtors).
- 2026-09-05 · `13bb0329` · The tenant's Sign renewal button is enabled by their assigned `SignRenewal` task, not the write guard alone — platform half live via Weaver `89b61556` (task assigned 22:09Z, term ends 09-06).
- 2026-09-05 · `97f4a5cc` · The Care→Wellness referral picker offers only classes that can still be booked (CreateBooking's `submitted < startsAt`), soonest-first — live 48 rows → 3.
- 2026-09-05 · (re-measure at head) · LoftSpace landlord `SetRenewalTerms` 20/20, 0 timeouts, median 28.5 ms, max 85 — §9 PASS; cause = the fixed Refractor flood days; `step5-latency` tripwire lands with Inc A (lattice lane).
- 2026-09-05 · (re-measure at head) · Café staff `Charge` 20/20, 0 timeouts, median 86.5 ms, max 193 (resident 41 ms; staff `VoidCharge` ×30 median 55) — §9 PASS, thinnest margin of the three.
- 2026-09-05 · (re-measure at head) · Clinic front desk `CreateAppointment` 7/7 + `SetAppointmentStatus` 8/8, 0 timeouts, medians 30/33 ms — §9 PASS.
- 2026-09-05 · (not reproducible at head) · Café picker duplicates — both pickers already fetch the lease-confined, deduped `/api/menu?leaseAppKey=` (`0653c57a`): census 45 leases → 2 items, 10 → 0 (`missingLocation`), none doubled.
- 2026-09-05 · `a2a449c1` · A registered patient is visible to their desk at once (`registeredAtSite` links + roster anchor) and a visit series on a tombstoned patient stops advancing (`visitSeriesDue` requires `forPatient`).
- 2026-09-05 · `45271d83` · An approved lease with no agreed rent is billed — `leaseRentSettlement` raises `missing_terms` → `BackfillLeaseTerms`; Riley Chen's lease backfilled $1,900 live within a minute.
- 2026-09-05 · `feb800de` · The executed lease is served only once approved (declined/undecided 404) and the managing landlord can download it — landlord lens carries the doc pointers.
- 2026-09-05 · (live-stack op run) · Orphaned-unit lease `fuW7HyE8At2DkH7xx28t` withdrawn + the 3 remaining stale cluster-A onboarding tasks cancelled, via the admin app session (CLI `op submit` stays classifier-refused unattended).
- 2026-09-03 · `097aa843` · Café debt stops vanishing when a lease's unit is tombstoned — `cafeLeaseWorkplaces`'s new `missingLocation` flag routes it to every front-desk staffer instead of denying it to all of them.
- 2026-09-03 · `3a1351cd` · The landlord portfolio card finally shows rent owed — `/api/portfolio-pulse` now folds every occupied lease's ledger balance into a worst-first arrears column, no more pulling leases one at a time.
- 2026-09-03 · `cd16409c` · Front desk can finally take a payment against a landlord-unapproved café lease — `fillLeaseSelect`'s approval gate now scopes to the POS/OpenTab picker only, never the Resident-tab ledger+payment picker.

- 2026-09-03 · `9fa0a8bf` · A stale lease-signing userTask now cancels itself once its gap closes elsewhere — new `staleUserTasks` target mirrors `orphanedTaskGrants`.

- 2026-09-03 · `c3af15a3` · Self-pay clinic accounts can finally pay — `.balance` is now a maintained O(1) cache, not a full-history replay that blew the 250ms wall 9/10 times.

- 2026-09-03 · `ac4add98` · Front desk no longer sees the provider/site admin surface it holds no grant to use — `/api/staff-hats` now reports the operator hat; the FE gates on it, not `isFrontDesk()`.

- 2026-09-03 · `411c27d4` · The follow-up worklist no longer treats an earlier, still-before-due-date booking as having addressed the follow-up — `hasLaterVisit` now requires the later visit to land on/after `followUpDate`.

- 2026-09-02 · `46268dee` · A corrected clinic no-show now reverses its stranded fee — `clinicNoShowSettlement`'s new `missing_reversal` gap auto-credits it back once `CorrectAppointmentStatus` moves the appointment off `noShow`.

- 2026-09-02 · `70c73ac1` · A corrected wellness no-show now refunds its fee — `SetBookingAttendance` mints a wellnessrefund marker on a noShow→attended re-mark, mirroring `ReleaseOrphanedBooking`.

- 2026-09-02 · (observation, no code) · The executed lease document's `AttachObject` grant fix (`2026-08-27` live) converged — Weaver's reclaim sweep has attached all known-affected leases, none failing since; no code change needed.

- 2026-09-02 · `1b4bff22` · A wellness booking the auto-no-show sweep flipped before its class was called off can finally be released and its $25 fee refunded — `ReleaseOrphanedBooking` now accepts `noShow`.
- 2026-09-02 · `a03ca337` · The staff Manage Menu grid stops hiding a building-level item behind a same-named unit-level one, and front desk gets a standalone worst-first arrears list — visibility that used to vanish the moment a tab settled.
- 2026-09-02 · `b569fd2c` · The café stale-tab auto-settle timer finally arms — `cafeStaleTabSettlement`'s BodyColumns now project the `freshUntil` its cypher already computed.
- 2026-09-02 · `d9d7a4fd` · A staff Settle can finally close another resident's café tab — the chargedTo backfill is confirmed via a live link read, not a declared one keyed on the caller's own (nonexistent, for staff) lease.
- 2026-09-02 · `c3c91c79` · The clinic auto-no-show sweep no longer blames a patient for a provider's own time off — `MarkPastDueNoShow` no-ops when `.timeOff` covers the visit.
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
