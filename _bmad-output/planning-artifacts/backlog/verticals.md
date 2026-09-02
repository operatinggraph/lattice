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
| **The executed lease still doesn't name its tenant** | `/api/lease-document` renders `Tenant: vtx.identity.edu97ix…` — the applicant's real name is never assembled (`doc.TenantName`), a sensitive link-discovered aspect with no egress-declaration path. The landlord party now resolves via the unit's `manages` link (shipped `d46ab947`). | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) Loom link-discovered egress · refuted at build 2026-08-27 · [evidence](../../implementation-artifacts/lease-tenant-name-fire-brief.md) |
| **The executed lease document generates but never attaches** | `missing_leaseDocAttach`'s directOp submits `AttachObject` as the Weaver service actor — was `AuthDenied`, now fixed (grant restored live 2026-08-27, [lattice.md](lattice.md) done log). Weaver's own sweep reclaims the stale claim + re-fires on its normal lease-expiry schedule; not yet observed converged. | LoftSpace | platform | ★★ | S | 🏗️ converging · permission fix landed 2026-08-27, awaiting Weaver's next reclaim (no code change needed) |
| **29 of 63 appointments carry no site and `clinicSiteBackfill` closes the gap for none** | 16 have a provider at EXACTLY ONE site — the shape the op documents as convergent — yet stay unlinked; a Weaver engine gap, not a query bug. | Clinic | platform | ★★ | S–M | 🚧 blocked-on: live `ReplayTarget clinicSiteBackfill` run · fix design [§17](../../implementation-artifacts/weaver-decline-retry-substrate-native-design.md) `81a1c94`+`0fd3e8f` · sandbox refuses the run this fire |
| **Every front-desk POS write fails about half the time, mid-order** | A barista gets `ScriptTimeout: script exceeded wall budget 250ms`, no retry. The staff path walks confinement live inside the wall ([ddls.go:765](../../../packages/cafe-domain/ddls.go:765)); the resident's identical op is `authTargetValidated` and skips it — paced 1/s, same host: staff Charge 9/20 vs resident 2/20. `b997ff2a` already removed the clinic-shaped redundancy here. | Café | platform | ★★ | S–M | 🚧 blocked-on: [lattice.md](lattice.md) confinement-walk batched-read row · fork resolved PLATFORM |
| **8 bookings stand `booked` on live classes that ended weeks ago** | `pastDueBookings` flipped one booking 1s after its class ended yet has never dispatched 8 older ones (`endsAt` 2026-07-20…08-23) — the same already-Acked-forever shape the clinic site row names. | Wellness | platform | ★ | S | 🚧 blocked-on: live `ReplayTarget pastDueBookings` run · same fix design [§17](../../implementation-artifacts/weaver-decline-retry-substrate-native-design.md) as the clinic row · sandbox-blocked this fire |
| **The landlord's only renewal action fails 9 times in 12** | `SetRenewalTerms` returns `ScriptTimeout: wall budget 250ms` on 9 of 12 paced live submits (café's is 9/20). Its landlord-authority walk is 2 `kv.Links` + a per-candidate `kv.Read` over renewal→leaseapp→unit→`manages` ([renewal_scripts.go:195](../../../packages/lease-signing/renewal_scripts.go:195)) — not a confinement walk. | LoftSpace | platform | ★★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) confinement-walk wall row — widen it past `worksAt_covers` |
| **The front desk cannot book, cancel or re-status a single appointment** | 0 of 20 live staff `CreateAppointment` / `SetAppointmentStatus` submits returned `ScriptTimeout: wall budget 250ms`, while the same patient self-books 4/6 and staff `ClinicDebitAccount` is 4/4 — `workplace_exempt()` ([ddls.go:2053](../../../packages/clinic-domain/ddls.go:2053)) skips the walk for the self path only. Café's is 9/20. | Clinic | platform | ★★★ | S–M | 🚧 blocked-on: [lattice.md](lattice.md) confinement-walk batched-read row |
| **One lease's unit was reaped by the demo seed while its tenancy was live — unbillable** | `vtx.leaseapp.fuW7HyE8At2DkH7xx28t` (12 Classic Demo Ave, approved 2026-07-29, tenancy 2026-08-01→2027-08-01, listed $2,200/mo) had its unit tombstoned 2026-08-23 by `reapDuplicateListings` before the live-tenancy guard existed (`9a3a7807`); `BackfillLeaseTerms` permanently refuses it `UnitNoLongerAvailable`. | LoftSpace | pkg | ★ | XS | 📋 ready · withdraw the orphaned lease, or mint a replacement unit and re-point it |
| **A submitted application is invisible to applicant AND landlord, and the app calls the projection healthy** | A live `CreateLeaseApplication` committed 12:54Z was absent from `read_lease_applications` and `read_landlord_lease_applications` at 13:03Z; both endpoints answered `projectionHealthy: true`. False-healthy symptom fixed `d8cf4144`; the underlying zero-progress lens bug is not. | LoftSpace | FE + pkg | ★★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) Postgres-lens zero-progress row |
| **A corrected no-show leaves its no-show fee stranded, and the original wrong-call reason is destroyed** | `CorrectAppointmentStatus` writes only `.status`: a `noShow→completed` correction drops `noShowFeeCents` and never reverses an already-posted charge (no lens re-opens, gated on `status='noShow'`), and the mandatory correction note overwrites the original. | Clinic | pkg | ★ | S | 📋 ready · reverse/credit the stale fee on correction; consider projecting `correctedFrom` |
| **Front desk has no way to see which booked appointments a provider's time off now conflicts with** | `SetProviderTimeOff` writes only `.timeOff` — nothing tells front desk which of a provider's existing bookings now need a reschedule call. | Clinic | pkg | ★ | S | 📋 ready · sketch: walk the provider's appointments (`kv.Links(providerKey,"withProvider","in")`) at `SetProviderTimeOff`, mark overlapping non-terminal ones, project the marker for an FE badge |
| **The 4 stale cluster-A onboarding cards need a live CancelTask pass** | Inc 2 of the onboarding re-anchor: 4 open `RecordIdentityPII` tasks predate the Inc 1 fix and won't self-clear. Grounded 2026-09-01: the identity is a stranded pre-rotation fossil, not live admin — safe to cancel. | LoftSpace | platform | ★ | XS | 🚧 blocked-on: live-op execution · commands ready in [design](../../implementation-artifacts/duplicate-human-task-fanout-design.md) build note |
| **A staff Settle is permanently unsettleable for every descriptor-driven client** | The `Settle` descriptor templates its `chargedTo` optionalRead on `{me.leaseapp}` — the CALLER's lease, not the tab's — so a staff settle hydrates nothing, the read-before-create branch fires against a live link, and every retry returns `RevisionConflict … wrong last sequence`. Live: 4 refusals, then committed naming the tab's own lease. | Café | pkg | ★★★ | XS–S | 📋 ready · template it on the tab's lease ([opmetas.go](../../../packages/cafe-domain/opmetas.go)) |
| **The Manage Menu grid never lists the two items a third of the house is actually sold** | `dedupeMostSpecific` runs on the workplace-confined MANAGEMENT grid, not just the offer pickers, so a building-level item collapses behind a same-named unit-level one. Live: the grid shows 2 unit rows; the 22-of-62 leases outside that unit buy 2 building-level vertices no staffer can reprice or retire. | Café | FE | ★★ | XS | 📋 ready · collapse offer sets only ([menu.go](../../../cmd/cafe-app/menu.go:207)) |
| **The front desk can only see who owes the café money while that person has an open tab** | `/api/frontdesk-balances` rides as a badge on open-tab cards ([app.js](../../../cmd/cafe-app/web/app.js:1055)) and nowhere else, so arrears vanish at exactly the moment the tab settles. Live: 5 residents owe $73.25, 3 overdue up to 18 days, 0 open tabs — the Front Desk renders "No open tabs" and nothing else. Wellness's `99233d11` is a standalone worst-first list. | Café | FE | ★★ | S | 📋 ready · give café the standalone arrears list wellness already has |

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

- **Rotation to date:** LoftSpace ×28, Clinic ×28, Café ×19, Wellness ×15.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-08-28:** LoftSpace — drove applicant + landlord hats live through browse/apply/sign/decide/renew/statement; expired tasks read as live work, the one bill is 97 ungrouped lines, 33 of them quote an internal PO ruling; filed 3.
- **2026-08-29:** Clinic — drove patient, provider + front-desk hats live through book/cancel/document/bill; staff writes time out ~1 in 10 while the same ops self-serve in a third the time, and the descriptors publish only the patient's slice; filed 2.
- **2026-08-29:** Café — drove resident + front-of-house + backOfHouse hats live through menu/tab/charge/void/settle/pay; every staff write blows the 250ms script wall, the balance never becomes a bill; filed 2 + 1 platform.
- **2026-08-29:** Wellness — drove member/instructor/two staff hats live through schedule/book/roster/ledger/bill; nobody at the desk can mark attendance and 25 of 25 bookings are no-shows; filed 3.
- **2026-08-30:** LoftSpace — drove applicant + two landlord hats live through browse/apply/sign/decide/renew/bill; 4 of 8 signed leases never bill rent, a new application reaches neither party; filed 4 + 1 platform.
- **2026-08-30:** Clinic — drove patient, provider + front-desk hats live through book/status/document/bill; the front desk can't book at all and the patient's bill loses lines; filed 3.
- **2026-08-30:** Café — drove frontOfHouse + resident hats through menu/tab/ledger/front-desk on a saturated stack; front-desk reads fail quiet, overdue is browser-only, menu doubles; filed 3.
- **2026-08-31:** Wellness — drove member/front-desk hats live through schedule/series/book/waitlist/promote/cancel/refund/bill; the waitlist bills before it seats and the desk can neither cancel a class nor see who owes; filed 4.
- **2026-09-01:** LoftSpace — drove landlord + applicant hats live through listings/apply/decide/renew/tasks/ledger/one-bill/account; the attach KPI is dark and nothing names the sign-in you are on; filed 2 + 1 platform.
- **2026-09-01:** Clinic — drove patient/provider/front-desk hats live through book/status/document/follow-up/time-off/ledger; a terminal status is uncorrectable and the follow-up net self-clears on no-shows; filed 3.
- **2026-09-02:** Café — drove front-of-house + resident hats live through menu/tab/charge/void/settle/ledger; a staff settle is unsettleable and arrears hide when the tab closes; filed 3.
- **Next:** Wellness.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-09-02 · `c3c91c79` · The clinic auto-no-show sweep no longer blames a patient for a provider's own time off — `MarkPastDueNoShow` no-ops when `.timeOff` covers the visit.
- 2026-09-02 · `785b446b` · A no-show no longer silently clears the clinic follow-up worklist, and a requested follow-up now always carries a date — `RecordEncounter` requires `followUpDate` whenever `followUpRequested` is true.
- 2026-09-01 · `e1c1a6a0`+`9d49a3b1` · Front desk can finally correct a wrong terminal appointment status (e.g. a no-show who actually showed) — new `CorrectAppointmentStatus` op.
- 2026-09-01 · `a0b6264b` · An applicant with N applications now gets one onboarding task, not N — `missing_onboarding` re-anchored onto a new identity-scoped target (lease-signing 0.31.16).
- 2026-09-01 · `df4517bf` · Account settings can finally tell your own sign-in method apart — session now carries which credential authenticated it (JWT `cred_id` claim, `internal/appsession`), separate from the identity it resolved to.
- 2026-09-01 · (live-stack op run) · Riley Chen's standing auto-no-show charges waived, $125 credited reason:waiver — census corrected from the filed 6 (live count was 5; the 6th debit carries no automated-sweep memo, left untouched).
- 2026-09-01 · `9a3a7807` · Lease-terms backfill run to convergence (2/3 fixed); seed's duplicate-unit reap now guards live tenancies — 3rd lease's unit was already gone, filed above.
- 2026-09-01 · (live-stack op run) · wellness-reminders refreshed to 0.3.3 live + 19 auto-charged no-show fees refunded, $475 credited reason:waiver — census corrected from the filed 23 (live count was 19, all Weaver-charged).
- 2026-09-01 · `0e779e16` · Landlord service-attach KPI degrades per-source now — one absent lens bucket (e.g. no wellness booking ever written) no longer zeroes the other, readable half too.
- 2026-09-01 · `ed25f080` · Café front-desk bookings/lease-details/visits now fail loud (502) on a real KVGet error instead of silently dropping the row, closing the gap `aa29c41e`'s balances fix left.
- 2026-09-01 · `b3436086` · A member no longer eats the cost of a class the studio cancels — ReleaseOrphanedBooking now mints the same wellnessrefund marker CancelBooking does, unconditionally (no late-cancel window, since the studio caused it).
- 2026-09-01 · `77e196a5` · Wellness can no longer cancel free at the door — CancelBooking forfeits the class price inside a 2h window instead of refunding it.
- 2026-09-01 · `99233d11` · Wellness front desk can finally see who owes the studio money — new `/api/frontdesk-arrears`, worst-first, mirrors the café pattern.
- 2026-09-01 · `6df9b708` · Café front desk can finally see who owes the café money — new `/api/frontdesk-balances` mirrors the resident's own overdue statement onto the front-desk grid.
- 2026-09-01 · `786c51ab` · Wellness front desk can finally cancel a class off the grid — `TombstoneSession` now grants frontOfHouse, workplace-confined off the session's own studio.
- 2026-09-01 · `baf4e9b3` · Wellness's class-price charge now gates on `status='booked'` — a waitlisted booking was charged like a confirmed one and, absent promotion, never refunded.
- 2026-08-31 · `0653c57a` · Café's POS/self-order picker collapses a building-level + unit-level item sharing a name to the more specific one, instead of listing both with nothing to tell them apart.
- 2026-08-31 · `7491f3e4` · Café's three front-desk list handlers (bookings/lease-details/visits) now 502 on a real KVListKeys failure instead of silently answering an empty grid — same shape as `c791574b`/`aa29c41e`'s ledger fix.
- 2026-08-31 · `aa29c41e` · Café/wellness ledger reads now fail loud on a KVGet error instead of silently dropping the line — `c791574b` fixed the identical shape in clinic/loftspace but missed these two.
- 2026-08-30 · `d8cf4144` · The four vertical apps' projection-health check now catches a stalled (not just paused) lens — closes the false `projectionHealthy:true` PO found on LoftSpace.
- 2026-08-30 · `c791574b` · Clinic/LoftSpace ledger reads fail loud on a KVGet error instead of silently dropping the row and skewing the balance.
- 2026-08-30 · `a096df6f` · Portfolio's service-attach-rate counts usage in a 30-day window (startsAt/settledAt), not raw existence. front-desk 0.3.1 adds `frontDeskBookingHistory`.
- 2026-08-30 · `de326532`+`9eee9bcd` · café/clinic/wellness's `/api/op-catalog` narrows via `?types=` to just the ops each app renders; loftspace stays unfiltered (task-bound ops can name any op meta).
- 2026-08-30 · `5be4d4ee` · Facet's Me screen gets a demo-only reversible offline-pause toggle (`FACET_DEMO_CONTROLS`), env-gated. [Design §11](../../implementation-artifacts/facet-app-ux.md).
- 2026-08-30 · `a5077b9a` · Front desk can finally mark a member present — `SetBookingAttendance` grants frontOfHouse, workplace-confined exactly like `CancelBooking`. wellness-domain 0.22.14.
- 2026-08-29 · `75b05007`+`6f3b6212` · No-show ledger lines now name the class's date, not just its (repeating) name — classStartsAt was already served, just never rendered. wellness-app FE only.
- 2026-08-29 · `02d007a2` · Wellness studio grid-dry warning (client-side, no lens change) + a bare-entity-key guard on every app's ledger-memo render fixes the live remediation memo with no data mutation.
- 2026-08-29 · `681f36f1`+`71aba1b4` · Wellness front desk gets a debounced guest name-search + "＋ New guest" modal, replacing the raw identity-key input. wellness-domain 0.22.13.
- 2026-08-29 · `b3af3268` · Café/clinic/wellness staff nav now hides front-desk tabs for a worksAt-only, non-frontOfHouse staffer instead of hard-403ing on click — new GET /api/staff-hats exposes the server-resolved hat.
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
