# Backlog — App Verticals (Stream 1)

Stream 1 = app-vertical packages + FEs (LoftSpace, Clinic). Advanced by the **Vertical Steward**; demand
filed by the **Vertical PO** (file-only). Index + cross-lane rules: [../backlog.md](../backlog.md).
**Row discipline** (one item = one row; State = token + ref + one-line next; detail lives in the design
doc + git, never narrated in the cell): see [lattice.md → How this board works](lattice.md).

**Scales.** Imp ★/★★/★★★ · Size XS–XL. **State.** 📋 ready · 🏗️ building · 📐 awaiting-Andrew ·
✅ ratified (designed, not built) · 🚧 blocked (`blocked-on:` / Andrew-gated).

## Vertical demand backlog (PO discovery)

Open items only — shipped demand is in the Done log. The PO files (tagged vertical + owner: FE = Sally +
FE Engineer · pkg = Package Designer · platform = component owner + Lattice lane); the Steward + FE
Engineer build. **No-paper-over:** a missing platform *primitive* routes to [lattice.md](lattice.md) and
the row is `🚧 blocked-on:` it (a missing *lens* is package work, built here).

| Item | What it is (PO view) | Vertical | Owner | Imp | Size | State |
|---|---|---|---|---|---|---|
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks, panes arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. Covenant structurally enforced by `lint-facet-discovery` (CI). | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 🚧 blocked-on: full Xcode (this host has CommandLineTools only) · [design §7.12](../../implementation-artifacts/edge-showcase-app-design.md) · every non-iOS increment shipped |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **No way to demonstrate that Facet survives going offline** | Offline-first is the Edge's headline claim and nothing lets a viewer see it — the mirror only serves a disconnected world during a real NATS outage nobody can stage. Per [UX §6](../../implementation-artifacts/facet-app-ux.md) the honest offline story is a host↔NATS drop, not the browser going offline, so a truthful toggle disconnects the host engine: reads keep serving from bbolt, writes queue and drain on reconnect. | Cross-vertical | Sally + FE + platform | ★★ | M | 📋 designer · needs a fenced control surface |
| **clinic-domain's README still under-documents 4 shipped op surfaces** | The README's Operations/Inventory never mention `BindProviderIdentity` (+ its `identityClaim`/`providerClaim` guard aspects) or `CreatePatient`'s `identityClaim`/`patientClaim` guard, and the appointment op docs omit `CreateAppointment`'s `leaseAppKey`/`site` params and `SetAppointmentStatus`'s `noShowFeeCents` + terminal-status finality rule (all confirmed live in `ddls.go`). | Clinic | pkg | ★ | S | 📋 ready |
| **An assignee can never retire their own task** | `CompleteTask` grants only `operator` (orchestration-base `permissions.go:68`), so a signed lease leaves its "Sign lease" userTask `open` forever while the FE's `completeTask()` swallows the `AuthDenied` into a `console.warn`. Fix shape has precedent: a `scope: "self"` grant plus a script-side assignee guard (clinic-domain `permissions.go:107`). | Cross-vertical | pkg | ★★★ | S | 📋 ready |
| **Portfolio pulse counts landlords, not units** | `summarizePortfolioPulse` folds `read_landlord_units` rows, but that lens fans a co-managed unit out to one row per manager by its own doc — the Riverside front desk reads "100% occupied (8/8 leased)" over 2 distinct units, and `available` is structurally 0 while `/api/listings` shows 8 available. | LoftSpace | FE | ★★ | S | 📋 ready |
| **A leased unit with no manager falls out of every staff view** | Riverside Unit 1 carries no `manages` link, so its signed, rent-paying resident is absent from `read_landlord_units` / `read_landlord_lease_applications` and therefore from search, unit-applications, landlord applications and portfolio pulse — and nothing flags the unmanaged unit. | LoftSpace | pkg | ★★ | M | 📋 ready |
| **The renewal machinery can't fire in the live demo** | The seeded tenancy runs to 2027-09-08 with `renewalOpensAt` 2027-07-10, so `leaseExpiry` never violates, no cycle opens, and the Renewals tab is empty for every hat — the goal-authored `renewalComplete` target is reachable only under the `leaseshortwindow` build tag. | LoftSpace | pkg | ★★ | S | 📋 ready |
| **A tenant can see rent owed but can't pay it** | `/api/ledger` shows the resident $1,900 outstanding, yet `renderLedgerRecordForm` is gated on the landlord's `canRecord`, so no tenant-side payment exists — café already ships the resident-side counterpart ("Settle My Tab", `cafe-app/web/app.js:829`). | LoftSpace | FE + pkg | ★★ | M | 📋 ready |
| **The café's Manage Menu panel AuthDenies its own staff** | The shipped staff tab cannot work: both menu-catalog ops grant `operator` only, while cafe-app submits as the signed-in actor. | Café | pkg | ★★ | S | ✅ ratified 2026-08-02 · grant `frontOfHouse`, workplace-confined in `menuItemDDLScript` (loftspace-ledger idiom; both helpers already in `tabDDLScript`) |

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

- **Rotation to date:** LoftSpace ×20, Clinic ×18, Café ×9, Wellness ×6.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-31:** Clinic — drove patient, provider + front-desk hats live; no seeded visit ever completes and the provider hat can act on nothing; filed 4.
- **2026-07-31:** Café — drove resident self-order/settle + front-desk hats live; the front desk composes nothing and one-bill has no reader; filed 4 (1 → lattice).
- **2026-08-01:** Wellness — drove member, instructor + front-desk hats live; My Classes hides the studio, and there is no waitlist, recurrence or reminder; filed 5.
- **2026-08-01:** LoftSpace — drove tenant, landlord + front-desk hats live; browsable inventory is seven copies of one flat, rent never bills, six duplicate staff; filed 4 + broadened 1.
- **2026-08-01:** Clinic — drove patient, provider + front-desk hats live; a booked visit hides its site, no provider is bookable, past visits never close out; filed 4.
- **2026-08-02:** Café — drove resident self-order/void/settle + front-desk hats live; two leases are permanently unsettleable, void + menu curation reach no UI; filed 5.
- **2026-08-02:** Wellness — drove member, instructor + front-desk hats live; the ledger is dormant and the slot lock guards only the room; filed 3.
- **2026-08-02:** LoftSpace — drove tenant, landlord + front-desk hats live; pulse counts landlords not units, a done task never retires, an unmanaged unit vanishes; filed 5.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-08-02 · `fc15f83d` · A wellness studio can finally onboard its own instructors — Studios tab gains an add/edit surface wired to CreateInstructor/SetInstructorProfile, mirroring CreateStudio's form+grid.
- 2026-08-02 · `9e9de865` · An instructor/booker can no longer be double-booked across studios — instructorSlotClaim + bookerSlotClaim mirror clinic's provider/patientSlotClaim. wellness-domain 0.21.0

- 2026-08-02 · `7643d0f1` · A wellness class's ledger account finally opens — WellnessCreateAccount grants consumer scope=self, wired into self-service + front-desk booking. wellness-ledger 0.2.2
- 2026-08-02 · `b0f8d09a` · Front desk can now open clinic + loftspace ledger accounts too — ClinicCreateAccount (unconfined) + LoftspaceCreateAccount (workplace-confined) grant frontOfHouse. clinic-ledger 0.2.4, loftspace-ledger 0.4.5
- 2026-08-02 · `329ac6cf` · Front desk can now open a wellness member's ledger account — WellnessCreateAccount grants frontOfHouse. wellness-ledger 0.2.1
- 2026-08-02 · `5ef038d5` · A wellness class booking now carries a price the FE actually sets and shows — CreateSession price field, Schedule/My Classes price display, self-scoped GET /api/ledger + balance panel. wellness-app
- 2026-08-02 · `ad20e036` · unit-applications' weaver-targets read scoped to its own prefix — KVListKeysPrefix, not a whole-bucket list + client-side filter. loftspace-app
- 2026-08-02 · `11c50fd4` · A café credit balance no longer renders as `$-21.59` — ledgerBalanceLine mirrors loftspace-app's owed/credit/paid-in-full split. cafe-app
- 2026-08-02 · `dc7d1983` · POS can now void a mis-tapped charge — Void Charge form wired to VoidCharge, mirroring the off-menu Charge form/handler. cafe-app
- 2026-08-02 · `82613032` · POS lease picker now names the resident, not the raw key — fillLeaseSelect joins /api/residents + /api/frontdesk-lease-details, mirroring frontDeskCard's own join. cafe-app
- 2026-08-02 · `af451062` · Nothing ever chases a tab left open — cafeStaleTabSettlement auto-dispatches SettleStaleTab once an open tab's staleAt (openedAt+24h) passes, mirroring clinic's pastDueAppointments. cafe-domain 0.11.11
- 2026-08-02 · `fdbf16ff` · A house tab can no longer be permanently unsettleable — cafeTabSettlement admits openFor as an open-tab fallback anchor, Settle backfills the missing chargedTo link. cafe-domain 0.11.10
- 2026-08-02 · `b7cf16b3` · A past visit no longer sits open, unbilled, forever — pastDueAppointments Weaver target auto-no-shows + bills it once endsAt passes. clinic-domain 0.28.17, clinic-reminders 0.7.3
- 2026-08-02 · `2dcbfeaf` · A booked class now reminds its booker — new wellness-reminders package mirrors clinic-reminders' ~24h-ahead @at reminder, anchored on booking not session
- 2026-08-02 · `22a40ba4` · Every clinic provider now offers real bookable slots — SetProviderHours seeds a full weekly window for Osei/Patel/classic-demo
- 2026-08-02 · `bc13aa13` · A booked visit now says which clinic to go to — atSite walk added to clinicAppointmentsRead/providerAppointmentsRead/clinicAppointments + card render. clinic-domain 0.28.16
- 2026-08-02 · `21a3b94d` · A no-show charge now says what it's for — clinicNoShowSettlement passes a memo to DebitAccount. clinic-ledger 0.2.3
- 2026-08-02 · `f2183db9` · A full class now offers a real waitlist, not a dead-end — Schedule's Join-waitlist affordance + My Classes position, staff roster's booked/waitlisted split fixed too. wellness-domain 0.19.8
- 2026-08-01 · `4870977c` · A weekly class no longer needs N separate creates — CreateSessionSeries mints occurrenceCount occurrences in one atomic op. wellness-domain 0.19.6
- 2026-08-01 · `58af30ef` · A wellness class booking now has a price — CreateSession's priceCents + a new wellnessClassPriceSettlement Weaver target auto-charge the booker. wellness-domain 0.19.4, wellness-ledger 0.2.0
- 2026-08-01 · `d53640f1` · Convergence targets declare §10.3 retry budgets — maxretries_<gap>=3 on all four weaver targets; orchestration-base 0.7.7, wellness-domain 0.19.3, wellness-ledger 0.1.1
- 2026-08-01 · `d7668f43` · A task orphaned by a tombstoned op now self-cancels — new orphanedTaskGrants Weaver convergence target dispatches CancelTask, mirroring wellness's ReleaseOrphanedBooking; orchestration-base 0.7.6
- 2026-08-01 · `b5563815` · Portfolio card no longer half-populates — `landlordUnitsRead` anchors on covering buildings too, mirroring `landlordLeaseApplicationsRead`; loftspace-domain 0.10.7
- 2026-08-01 · `2cb54ee0` · A signed lease now actually bills — seed-showcase opens Riley's rent account + posts one rent debit; one-bill statement is no longer 100% café
- 2026-08-01 · `b0cf3d62` · seed-classic-demo now pins the unit + studio (unitID/studioID) so reruns stop minting duplicate inventory rows
- 2026-08-01 · `f5551e87` · Staff seed guard no longer self-defeats after sign-in — ensureStaff/ensureMaintenanceTech exclude by residesIn, not consumer; reruns now reuse the same Dana/Theo
- 2026-08-01 · `f9a6a55e` · My Classes now names the studio to show up at — wellnessBookings walks se→atStudio→studio; wellness-domain 0.19.2
- 2026-08-01 · `16becb24` · A wellness no-show now actually costs the member money — new wellness-ledger package + noShowFeeCents auto-charge; wellness-domain 0.19.1
- 2026-07-31 · `3e081e95` · A café tab now shows what was rung up, not just a total — Charge/VoidCharge append a running itemsMemo line the tab card AND the settled ledger entry's memo both render; cafe-domain 0.11.9
- 2026-07-31 · `00cfd10d` · loftspace-app's tenant view now reads the one-bill statement — combined rent+café charges, self-anchored via the protected applications RLS rows; Inc 3's payoff finally reaches a user
- 2026-07-31 · `30f0b467` · Front desk can now compose all three badges for both showcase residents — each tenant gets a backfilled approved lease, a residentVisit appointment, and a resident-rate booking
- 2026-07-31 · `df76a401` · clinic-domain README now documents the live write-path slot-claim booking mechanism instead of the retired `.bookingGuard`/`hasBooking` design
- 2026-07-31 · `adbf2571` · `seed-classic-demo` patient + provider now pin fixed handles (alive()-guarded); appointment id derived per-day so reruns converge instead of duplicating the shared booking picker/roster
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
