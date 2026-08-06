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
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks, panes arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 📋 next: entity-ref candidate picker (row below) · FORK-1 trigger CLOSED [design §7.13](../../implementation-artifacts/edge-showcase-app-design.md) |
| **Facet's entity-ref fields have no candidate picker** | `DescriptorForm.swift`'s `.entityRef` fields (e.g. `RescheduleAppointment`'s `provider`) render as a plain text field — the visitor must type a raw `vtx.<type>.<id>` key by hand instead of searching/picking like the PWA's `entityRefCandidates`. `ManifestStore` doesn't track `manifest.ent.*` rows at all yet. | Cross-vertical | FE Engineer | ★ | M | 📋 ready · [design §7.13](../../implementation-artifacts/edge-showcase-app-design.md) |
| **Facet on a literal iOS device** | The SwiftUI renderer builds + runs as a macOS proxy only. A real iOS/simulator build proves platform packaging (App Store viability), which FORK-1's freeze no longer waits on. Also unblocks real `swift test` in place of the hand-mirrored `swift run` harness. | Cross-vertical | Sally + FE Engineer | ★ | M | 🗄️ shelved (revive: a machine with full Xcode — this host has CommandLineTools only) · [design §7.10](../../implementation-artifacts/edge-showcase-app-design.md) |
| **No way to demonstrate that Facet survives going offline** | Offline-first is the Edge's headline claim and nothing lets a viewer see it — the mirror only serves a disconnected world during a real NATS outage nobody can stage. Per [UX §6](../../implementation-artifacts/facet-app-ux.md) the honest offline story is a host↔NATS drop, not the browser going offline, so a truthful toggle disconnects the host engine: reads keep serving from bbolt, writes queue and drain on reconnect. | Cross-vertical | Sally + FE + platform | ★★ | M | 📋 designer · needs a fenced control surface |
| **The executed lease names neither party** | `/api/lease-document` serves both parties a legal instrument reading `Tenant: vtx.identity.edu97ix…` and `Landlord: LoftSpace property management` — a hardcoded literal (`internal/bridge/docgen_adapter.go:232`); the identity that actually `manages` the unit is never resolved, and `doc.TenantName` is never assembled. | LoftSpace | pkg | ★★ | M | 🚧 blocked-on: [lattice.md](lattice.md) `[Loom] externalTask subject-only egress` (tenant name) · landlord party unblocked |
| **An exhausted screening check still renders "To do" forever** | `leaseApplicationsRead` now carries missing/inflight/declined per gap (row above shipped) but no `exhausted_bgcheck`/`exhausted_payment` column — Weaver's dispatch-count-vs-budget state is deliberately not lens-readable — so `stepState` can't tell "escalated to Augur" from "not started". residual of `ab971faa` below. | LoftSpace | pkg + FE | ★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `[Weaver] gap retry-budget exhaustion has no lens-readable signal` |
| **App-server GET endpoints apply no per-lease/patient ownership filter** | `GET /api/ledger?leaseAppKey=` was the ONE unguarded sibling — clinic/café/wellness ledger reads already enforce ownership; loftspace-app's `one_bill.go` had already flagged it. Fixed below (Done log). Closed as scoped, not app-wide. | Cross-vertical | pkg | ★ | M | ✅ shipped `92e8e1f0` |
| **Seven indistinguishable "Classic Demo Studio" rows survive the studio pin** | seed-classic-demo pins the studio id but has no `reapDuplicateStudios` beside its `reapDuplicateProviders`/`reapDuplicateMenuItems` — `/api/studios` serves 11 studios, 7 of them the same name, to the Studios tab and the CreateSession picker. | Wellness | pkg | ★ | S | ✅ shipped `8f9b0633` |
| **A documented visit can never be read back** | `RecordEncounter` captures `{summary, assessment?, plan?}` but the `.encounter` aspect is by design never projected (`clinic-domain/ddls.go:496`), so the read model carries only `documentedAt`/`followUpRequested` — neither the patient nor the authoring provider can ever see the note. | Clinic | FE + pkg | ★★ | M | 🚧 blocked-on: [lattice.md](lattice.md) `[Vault] Sensitive aspects are identity-anchored` |

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

- **Rotation to date:** LoftSpace ×21, Clinic ×20, Café ×11, Wellness ×8.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-08-02:** LoftSpace — drove tenant, landlord + front-desk hats live; pulse counts landlords not units, a done task never retires, an unmanaged unit vanishes; filed 5.
- **2026-08-02:** Clinic — drove patient, provider + front-desk hats live; no appointment view resolves at all and the no-show never bills; filed 5.
- **2026-08-03:** Café — drove resident + front-desk hats live through open/charge/void/settle; the catalog is 8 copies of 2 items, no charge can be named or itemized, and rent never bills; filed 4.
- **2026-08-03:** Wellness — drove member, instructor + front-desk hats live; no past class closes out, no studio retires, no balance settles; filed 5.
- **2026-08-03:** LoftSpace — drove applicant, landlord + front-desk hats live; the executed lease names neither party, screening progress is invisible, an exhausted check dead-ends; filed 3 + 1 platform.
- **2026-08-04:** Clinic — drove patient, provider + front-desk hats live; billing denies every hat, a booked visit hides its site, a documented note is unreadable; filed 3.
- **2026-08-04:** Café — drove self-order→settle + front-desk hats live; the one bill omits clinic/wellness, the staff menu grid is unconfined, the visit badge is last-wins; filed 4.
- **2026-08-05:** Wellness — drove member + instructor hats live through book/cancel; billing is unlabeled, no class is priced, studios duplicate; filed 3.
- **Next:** LoftSpace.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-08-06 · `8f9b0633` · The seven duplicate "Classic Demo Studio" rows are finally reaped — seed-classic-demo gains reapDuplicateStudios, mirroring reapDuplicateProviders/reapDuplicateMenuItems.
- 2026-08-06 · `92e8e1f0` · A signed-in resident can no longer read another lease's ledger — `/api/ledger` gains leaseVisibleToActor (tenant OR managing landlord, RLS-backed).
- 2026-08-06 · `979f9fb9` · A wellness member's bill finally itemizes and names its charges — My Classes gains the ledger-list itemization Roster already had; wellnessNoShowSettlement/wellnessRefundSettlement gain a memo. wellness-ledger 0.2.5
- 2026-08-05 · `5beeb585` · Seeded wellness classes finally carry a real price — Vinyasa Flow/Evening Flow gain priceCents so wellnessClassPriceSettlement + the transitively-starved wellnessRefundSettlement get a live instance.
- 2026-08-05 · `d40ce942` · Loftspace/café/wellness reads now signal a paused projection — 6 handlers gain projectionHealthy mirroring clinic-app; loftspace-app FE distinguishes paused from empty on Applications/Renewals/landlord RLS.
- 2026-08-05 · `2b8af63d` · A café tab opened before staleAt shipped can finally auto-settle — missing_staleat gap + BackfillTabStaleAt backfills the 11 stranded legacy tabs. cafe-domain 0.11.17
- 2026-08-05 · `11bc39df` · clinic-domain's README finally documents BindProviderIdentity, its identityClaim/providerClaim guards, CreatePatient's patientClaim guard, leaseAppKey/site, and noShowFeeCents + terminal-status finality.
- 2026-08-05 · `99055858` · A cancelled priced class finally refunds — wellnessrefund marker + wellnessRefundSettlement convergence. wellness-domain 0.21.1, wellness-ledger 0.2.4
- 2026-08-05 · `5d6db584` · A café tab can finally produce an itemized receipt — `.status.lines` + lineId-targeted VoidCharge, POS gains a per-line Void button. cafe-domain 0.11.16
- 2026-08-05 · `018bc6d7` · A lease applicant can finally see their own progress — leaseApplicationsRead gains the four-gate stepper state, mirroring D1.5's landlord-side pattern. lease-signing 0.27.14
- 2026-08-05 · `3294be37` · A paused clinic projection no longer reads as "no results" — `internal/projectionhealth` + clinic-app's 6 RLS reads gain `projectionHealthy`; FE shows "data paused" not empty.
- 2026-08-05 · `c643cf06` · An unmanaged leased unit no longer vanishes from every staff view — seed fix + a missing_manager convergence flag (human-gated, no auto-dispatch). lease-signing 0.27.13
- 2026-08-05 · `2d8604f4` · The one-bill statement now covers clinic + wellness, not just rent + café — oneBillClinicEntries/oneBillWellnessEntries added, live-verified against a real Riverside lease. one-bill 0.3.0
- 2026-08-05 · `a587b245` · A LoftSpace tenant can finally pay their own rent — CreditAccount grants a consumer scope=self, ownership + balance checked. loftspace-ledger 0.4.7
- 2026-08-05 · `305fd25c` · A wellness member's balance can finally be settled — WellnessDebitAccount/WellnessCreditAccount grant frontOfHouse; Roster gains a Billing panel (member picker + record charge/payment). wellness-ledger 0.2.3
- 2026-08-05 · `af3e472e` · The café Manage Menu panel no longer AuthDenies its own staff — CreateMenuItem/RetireMenuItem now grant frontOfHouse too, workplace-confined like the tab ops. cafe-domain 0.11.13
- 2026-08-05 · `2c231189` · The café front-desk card now shows a resident's next visit, not their last — keepSoonest picks the earliest startsAt per lease instead of a last-write-wins map overwrite.
- 2026-08-05 · `7a440030` · The café Manage Menu grid no longer shows every property's catalog — menuCatalogSpec projects per-item coveringLocations, handleMenu confines staff to their own workplace's items. cafe-domain 0.11.12
- 2026-08-04 · `ab971faa` · A screening budget that runs out no longer dead-ends in silence — leaseApplicationComplete escalates "exhausted" gaps to Augur reasoning instead of an unread GapBudgetExhausted warning. lease-signing 0.27.12
- 2026-08-04 · `02b812c4` · The renewal cycle finally opens in the live demo — seedResidentTenancies' move-in pushed back 11 months (was 2) so leaseEnd lands inside the 60-day renewal window on the seed's first tick.
- 2026-08-04 · `f836a533` · A booked visit finally says where to go — submitBook falls back to the provider's own sole practicesAt site when the site filter is left on "Any site".
- 2026-08-04 · `329dc7d2` · The clinic front desk can finally record a charge or payment — DebitAccount/CreditAccount renamed Clinic{Debit,Credit}Account + frontOfHouse granted; the patient hat no longer sees Record charge. clinic-ledger 0.2.7
- 2026-08-04 · `5afb72f7` · Pre-pin clinic-provider + café-menu-item duplicates finally reaped — seed-classic-demo.go tombstones every stray "Dr. Classic Demo"/Latte/Croissant, name+location filtered.
- 2026-08-04 · `38dd7d18` · An off-menu café charge can finally be named — POS charge form gains an optional description input, mirroring loftspace/clinic-app's memo `trim() || undefined` idiom.
- 2026-08-04 · `b5b30940` · My Classes now leads with the next class, not history — renderMyClasses splits Upcoming/Past, mirroring clinic-app's My Appointments.

- 2026-08-03 · `69851ea5` · A wellness studio can finally be retired — studioCard gains a Retire button wired to TombstoneStudio, mirroring cafe-app's RetireMenuItem.
- 2026-08-03 · `0be2511d` · Portfolio pulse now counts distinct units, not landlord fan-out rows — summarizePortfolioPulse dedupes read_landlord_units by UnitKey before counting.
- 2026-08-03 · `1841a5b3` · A past class finally closes out — wellness-reminders gains pastDueBookings, mirroring clinic-reminders' pastDueAppointments; wellnessNoShowSettlement now has a producer. wellness-reminders 0.2.0
- 2026-08-03 · `c79f9b5f` · The café catalog stops duplicating on every seed rerun — CreateMenuItem's Latte/Croissant gain pinned ids + an `alive()` guard, mirroring patient/provider/unit/studio. Pre-pin copies remain (row above).
- 2026-08-03 · `2dc82ca0` · A no-show fee for an account-less patient now opens one — clinicNoShowSettlement gains a missing_account gap (ClinicCreateAccount), mirroring café's lazy account-open. clinic-ledger 0.2.6
- 2026-08-03 · `a52a3422` · A signed lease finally bills its rent — semantic-contracts' leaseRentSettlement target bootstraps the ledger account + a recurring monthly clause for every approved lease; seed-showcase's one-off manual hack retired.
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
