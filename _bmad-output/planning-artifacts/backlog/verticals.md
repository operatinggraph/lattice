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
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks, panes arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 📋 next: offline-demo control surface (row below) · entity-ref picker + FORK-1 trigger CLOSED [design §7.13](../../implementation-artifacts/edge-showcase-app-design.md) |
| **Facet on a literal iOS device** | The SwiftUI renderer builds + runs as a macOS proxy only. A real iOS/simulator build proves platform packaging (App Store viability), which FORK-1's freeze no longer waits on. Also unblocks real `swift test` in place of the hand-mirrored `swift run` harness. | Cross-vertical | Sally + FE Engineer | ★ | M | 🗄️ shelved (revive: a machine with full Xcode — this host has CommandLineTools only) · [design §7.10](../../implementation-artifacts/edge-showcase-app-design.md) |
| **No way to demonstrate that Facet survives going offline** | Offline-first is the Edge's headline claim and nothing lets a viewer see it — the mirror only serves a disconnected world during a real NATS outage nobody can stage. Per [UX §6](../../implementation-artifacts/facet-app-ux.md) the honest offline story is a host↔NATS drop, not the browser going offline, so a truthful toggle disconnects the host engine: reads keep serving from bbolt, writes queue and drain on reconnect. | Cross-vertical | Sally + FE + platform | ★★ | M | 📋 designer · needs a fenced control surface |
| **The executed lease still doesn't name its tenant** | `/api/lease-document` renders `Tenant: vtx.identity.edu97ix…` — the applicant's real name is never assembled (`doc.TenantName`), a sensitive link-discovered aspect with no egress-declaration path. The landlord party now resolves via the unit's `manages` link (shipped `d46ab947`). | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `[Loom] externalTask subject-only egress` |
| **No resident, member, patient or tenant can actually pay their own balance** | All four `*-ledger` self-credit branches recompute the owed balance by walking `kv.Links(postedTo)` + a `kv.Read` per transaction ([scripts.go:524](../../../packages/cafe-ledger/scripts.go)) — live, Café and Wellness both reject `ScriptTimeout` 3/3 over an 8-row account, while the same op with no self-scope commits. CI's 5 s `internal/testutil` wall-widening hides it. | Cross-vertical | pkg | ★★★ | S | 📋 ready · unblocked by `15c46beb` |
| **The shared stack serves Café and Wellness with their name service dead** | `run-cafe-app` / `run-wellness-app` / `run-loftspace-app` omit `*_PG_DSN` ([Makefile:1656](../../../Makefile)) where `run-clinic-app` sets it, so the additive path onto an already-up stack leaves `cafeIdentitiesRead` 502ing and every resident, orderer and receipt line renders as a raw key — and `loadIdentities` swallows it to `[]` ([app.js:340](../../../cmd/cafe-app/web/app.js)), so nothing says the roster is down. | Café + Wellness | pkg + FE | ★★ | XS | 📋 ready |
| **A just-placed self-order reads $0.00 for the next ~40 seconds** | The Charge commits, then `setTimeout(renderResident, 700)` ([app.js:1119](../../../cmd/cafe-app/web/app.js)) re-reads the `cafeTabs` lens, measured live at ~40s to project — so an empty $0.00 tab follows a success toast and a second tap is the obvious response. The app already ships the affordance one hop later ("Pending posting", [app.js:937](../../../cmd/cafe-app/web/app.js)); the charge hop has none. | Café | FE | ★★ | S | 📋 ready |
| **Two ghost leases sit in the POS lease picker under a raw key** | Both are seed/verify leftovers held by the platform admin identity, which `cafeIdentitiesRead` never names, so `app.js:425` degrades to `shortKey`; one resolves no unit at all (blank address, $0 rent) in `frontdesk-lease-details`. Reap precedent: `8f9b0633` / `c643cf06`. | Café | pkg | ★★ | S | 📋 ready |
| **Retiring a studio strands its classes with no location at all** | `TombstoneStudio` soft-deletes with no cascade ([ddls.go:111](../../../packages/wellness-domain/ddls.go)) and `reapDuplicateStudios` ([seed-classic-demo.go:428](../../../scripts/seed-classic-demo.go)) tombstoned two, so 6 sessions still `atStudio`-linked there project a null studio and the card renders no location line ([app.js:705](../../../cmd/wellness-app/web/app.js)). `ReassignSession` already exists; mirror the `missing_manager` convergence flag (`c643cf06`). | Wellness | pkg | ★★ | S | 📋 ready |
| **A no-show fee never names the class it charges for** | `wellnessLedgerHistory` projects the bare literal `'No-show fee' AS memo` ([lenses.go:188](../../../packages/wellness-ledger/lenses.go)), so a member's history is N indistinguishable $25 debits with nothing to dispute. Clinic precedent `15f628f4` walks the `settles` link for the visit date. | Wellness | pkg | ★★ | S | 📋 ready |
| **Three of the eight storefront listings can never be leased** | All 8 are the same "12 Classic Demo Ave" $2200/1bd unit: 3 carry no `manages` landlord so an application to them is undecidable ([permissions.go:135](../../../packages/lease-signing/permissions.go)), 7 are pre-pin duplicates the `unitID` pin never reaped, and the pinned one accrued 10 co-managers from the unguarded per-run mint ([seed-classic-demo.go:73,127](../../../scripts/seed-classic-demo.go)). Reap precedent: `8f9b0633`. | LoftSpace | pkg | ★★ | S | 📋 ready |
| **Two Protected authz lenses stay broad for want of a single-hop rewrite** | `landlordUnitsRead` + `applicantRosterRead` carry `containedIn*1..` as their sole narrowing blocker, and every live wiring is unit→building at depth 1 — rewriting to `[:containedIn]` narrows both under existing machinery. Then assess the `*0..` `coveringLocations` family (cafe/wellness) per lens. | LoftSpace | pkg | ★★ | S | 📋 ready · origin: [typed-relation-signatures held](../../implementation-artifacts/typed-relation-signatures-design.md) |
| **A landlord sets renewal terms without ever seeing whose renewal it is** | `renewalsReadSpec` binds the tenant identity but projects only `tenant.key` ([renewal_lenses.go:305](../../../packages/lease-signing/renewal_lenses.go)), so the card renders address + cycle end + a short key ([app.js:2334](../../../cmd/loftspace-app/web/app.js)) and Set terms / Decline act on an unnamed person. `landlordLeaseApplicationsRead` already names its applicant via a SECURE column ([lenses.go:269](../../../packages/lease-signing/lenses.go)) — mirror it. | LoftSpace | pkg | ★★ | S | 📋 ready |
| **A qualification profile stays rewritable after the lease is approved and signed** | `SetApplicantProfile` is an "UNCONDITIONED upsert (re-submittable)" with no terminal-state guard ([ddls.go:114](../../../packages/lease-signing/ddls.go)) — verified live against an approved + signed application, so the record the landlord decided on is never preserved as a fair-housing record. `DecideLeaseApplication`'s own `DecisionFinal` ([:95](../../../packages/lease-signing/ddls.go)) is the in-package precedent. | LoftSpace | pkg | ★ | S | 📋 ready |
| **The unauthenticated class schedule advertises agent-verify artifacts** | `/api/studios` + `/api/sessions` are public-read ([server.go:29](../../../cmd/wellness-app/server.go)) and 3 of 5 studios plus 5 of 12 upcoming classes are "PO Discovery"/"Steward Verify"/"Recurrence Verify" litter — verify fires mint demo-visible entities and never reap them. Reap precedent: `8f9b0633`. | Wellness | pkg | ★ | S | 📋 ready |
| **An appointment gets a location only if its provider works at exactly one site** | 27 of 60 carry no site: `submitBook` falls back to `providerSoleSite`, "" for a 0-or-2+-site provider ([app.js:790,2074](../../../cmd/clinic-app/web/app.js)); `#book-site` reads as a *provider filter*, not the venue ([index.html:52](../../../cmd/clinic-app/web/index.html)). `BackfillAppointmentSite` refuses to guess and takes no explicit site ([ddls.go:2797](../../../packages/clinic-domain/ddls.go)), so no human can fix one. | Clinic | pkg + FE | ★★★ | S | 📋 ready |
| **An auto-charged no-show fee can never be reversed** | `MarkPastDueNoShow` debits $25 whenever a visit ends without a status update (live: 34 of Riley Chen's 48 appointments, an $825 balance), but clinic-ledger ships only debit + `ClinicCreditAccount` "record a payment" ([ddls.go:143,167](../../../packages/clinic-ledger/ddls.go)) and the FE only "+ Record charge"/"+ Record payment" ([index.html:140](../../../cmd/clinic-app/web/index.html)). A waiver booked as a payment overstates cash received. | Clinic | pkg + FE | ★★ | S | 📋 ready |
| **A patient's RLS anchor array grows by one entry per visit, forever** | `clinicPatientsReadSpec`'s workplace comprehension emits one token per appointment path, not per distinct building ([lenses.go:669](../../../packages/clinic-domain/lenses.go)) — live, Riley Chen's row holds 67 anchors / 3 distinct, RLS-`unnest`ed on every staff roster read. Its doc comment rules out a MATCH, but `edge-manifest`'s `personalManifest` folds the same fan-out with `collect(DISTINCT …)` ([lenses.go:518](../../../packages/edge-manifest/lenses.go)). | Clinic | pkg | ★★ | S | 📋 ready |
| **The patient-facing site picker offers a verify artifact as a clinic** | `/api/sites` returns 2 sites, one of them "PO Discovery Test Site", and the roster carries 5 duplicate "Classic Demo Patient" rows plus "Grid Snap Test"/"Post-Merge Verify"/"Inc2a Live Verify" patients — clinic verify fires mint demo-visible entities and never reap them. Reap precedent: `8f9b0633`; the Wellness row above is the same defect in its own seed. | Clinic | pkg | ★ | S | 📋 ready |

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

- **Rotation to date:** LoftSpace ×23, Clinic ×22, Café ×13, Wellness ×9.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-08-03:** LoftSpace — drove applicant, landlord + front-desk hats live; the executed lease names neither party, screening progress is invisible, an exhausted check dead-ends; filed 3 + 1 platform.
- **2026-08-04:** Clinic — drove patient, provider + front-desk hats live; billing denies every hat, a booked visit hides its site, a documented note is unreadable; filed 3.
- **2026-08-04:** Café — drove self-order→settle + front-desk hats live; the one bill omits clinic/wellness, the staff menu grid is unconfined, the visit badge is last-wins; filed 4.
- **2026-08-05:** Wellness — drove member + instructor hats live through book/cancel; billing is unlabeled, no class is priced, studios duplicate; filed 3.
- **2026-08-06:** LoftSpace — drove applicant + landlord hats live; the applicant lens is paused dead, the health signal can't fire, renewals never opened; filed 4 + 2 platform.
- **2026-08-06:** Clinic — drove patient, provider + front-desk hats live; no visit says which site, a $750 bill is 28 identical lines, no patient can pay; filed 4.
- **2026-08-07:** Café — drove resident self-order→settle + front-desk hats live; no resident pays their own tab, no line names its orderer; filed 3 + 1 platform.
- **2026-08-07:** Wellness — drove member + front-desk hats live through schedule/book/bill; a retired studio strands its classes, no member self-pays, no fee names its class; filed 4.
- **2026-08-07:** LoftSpace — drove applicant, landlord + staff hats live through profile/decide/renew/search; 3 listings are unleasable, a renewal never names its tenant; filed 3.
- **2026-08-07:** Clinic — drove patient, provider + front-desk hats live; 27 of 60 appointments have no location, no no-show fee is reversible; filed 3 + 1 platform.
- **2026-08-08:** Café — drove resident open→self-order→settle→post live; no self-pay works on any vertical, names are dead, a new order reads $0; filed 3 + 1 platform.
- **Next:** Wellness.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-08-07 · `130d3958` · A wellness member can finally pay down their own balance — WellnessCreditAccount gains a consumer scope=self grant, ownership + amount capped server-side. wellness-ledger 0.2.6
- 2026-08-07 · `15209753` · A shared café house tab finally names who ordered what — Charge stamps orderedBy (op.actor) on each .status.lines entry, distinguishing a resident's self-order from a staff ring-up on the receipt. cafe-domain 0.11.18
- 2026-08-07 · `80a1f76c` · A café resident can finally pay down their own house tab — CreditCafeAccount gains a consumer scope=self grant, ownership + amount capped server-side. cafe-ledger 0.3.9
- 2026-08-07 · `dbe9e65e` · Appointments finally carry a site (new BackfillAppointmentSite op) and Riley Chen's no-show billing stops resetting daily — fixed, non-recurring appointment ids. clinic-domain 0.28.18
- 2026-08-06 · `15f628f4` · A no-show fee finally names the visit that caused it — clinicLedgerHistory walks the settles link to project appointmentKey + visit date, billing history shows "(visit <date>)". clinic-ledger 0.2.9
- 2026-08-06 · `d46ab947` · An executed lease finally names its real landlord, not a hardcoded placeholder — doc.landlordKey resolves off the unit's `manages` link. lease-signing 0.27.16
- 2026-08-06 · `ea68207b` · A clinic patient can finally pay down their own balance — ClinicCreditAccount gains a consumer scope=self grant, ownership + amount proven server-side. clinic-ledger 0.2.8
- 2026-08-06 · `1019c68d` · An exhausted screening check finally reads "Escalated" instead of "To do" forever — leaseApplicationsRead gains escalated_bgcheck/escalated_payment off the augurproposal forCandidate link. lease-signing 0.27.15
- 2026-08-06 · `9982e740` · A Facet `x-entityRef` field finally gets a search-and-pick control instead of a raw-key text field, mirroring `app.js` (§7.13 residual closed).
- 2026-08-06 · `c97b784f` · The renewal cycle finally has a live instance — seedRenewalDemoTenancy mints a fifth, back-dated unit+lease so renewalOpensAt is already past on approval; read_renewals gained its first open row.
- 2026-08-06 · ops (no commit) · Every applicant can see "My Applications" again — leaseApplicationsRead resumed after its declined-column structural pause; read_lease_applications repopulated, verified as a signed-in applicant.
- 2026-08-06 · `5aa05ad0` · A co-managed unit no longer shows the landlord one application card per co-manager — landlordLeaseApplicationsRead dedupes by entity_key.
- 2026-08-06 · `44ea340c` · A paused projection finally reads as paused, not empty — pkgmgr.LensID resolves the lens NanoID Health KV is actually keyed by; all 12 withProjectionHealth call sites fixed.
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
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
