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
| **`internal/descriptorform`'s vocabulary can't express 6 op shapes, stranding ~13 ops hand-built** | No array/scheduling-widget/multiline+conditional field kinds, no ceremony/compound-op support, no computed-reads or typed-template+contextParams resolution, no auto-derived-field precedent — each new module semantics, no shipped precedent. | Cross-vertical | pkg + FE | ★★ | L | 📐 needs designer pass · no-pattern: descriptorform field-kind vocabulary · [design §15-17](../../implementation-artifacts/staff-descriptor-rendering-design.md) |
| **`CreateLocation`/`AttachObject`/`DetachObject` stay hand-built with a named fix path unbuilt** | Class-choice needs a `Dispatch.ClassChoices` enum field; the attach pair needs an upload-ceremony affordance + owner-anchored read surface (`signInMethods`-pane precedent). Baselined in `appOpDebt`. | Cross-vertical | pkg + FE | ★ | M | 📋 ready · [design §7](../../implementation-artifacts/staff-descriptor-rendering-design.md) |
| **Wellness guest-booking has no name-search, only a typed raw key** | Split off a shipped D4 sweep: a walk-in guest has no lease anywhere, so every sibling identity lens' lease→unit→building anchor can't back a search picker for one — a new un-anchored staff-searchable identity lens, security-relevant. | Wellness | pkg (design) | ★ | S | 📐 needs designer pass · no-pattern: un-anchored staff identity search · [brief §7](../../../docs/reviews/vertical-app-descriptor-audit-2026-08-20.md) |
| **A task card's only discriminator was a raw key, now removed** | D4 forbade the unlabeled key spans loftspace's application/task cards used to show — correctly removed, but two open tasks for the same op, both with no due date, now render identically. Needs a real per-instance label, not a mechanical mirror — no app resolves an arbitrary-typed entity key to a name today. (Renewal-card half shipped `cca9b4d4`.) | LoftSpace | pkg + FE (design) | ★ | S | 📐 needs designer pass · no-pattern: entity-key-to-name resolution for cards |
| **The executed lease still doesn't name its tenant** | `/api/lease-document` renders `Tenant: vtx.identity.edu97ix…` — the applicant's real name is never assembled (`doc.TenantName`), a sensitive link-discovered aspect with no egress-declaration path. The landlord party now resolves via the unit's `manages` link (shipped `d46ab947`). | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `[Loom] externalTask subject-only egress` |
| **Retiring a studio strands its classes with no location at all** | `TombstoneStudio` soft-deletes with no cascade ([ddls.go:111](../../../packages/wellness-domain/ddls.go)) and `reapDuplicateStudios` ([seed-classic-demo.go:428](../../../scripts/seed-classic-demo.go)) tombstoned two, so 6 sessions still `atStudio`-linked there project a null studio and the card renders no location line ([app.js:705](../../../cmd/wellness-app/web/app.js)). `ReassignSession` already exists; mirror the `missing_manager` convergence flag (`c643cf06`). | Wellness | pkg | ★★ | S | 📋 ready |
| **Three of the eight storefront listings can never be leased** | All 8 are the same "12 Classic Demo Ave" $2200/1bd unit: 3 carry no `manages` landlord so an application to them is undecidable ([permissions.go:135](../../../packages/lease-signing/permissions.go)), 7 are pre-pin duplicates the `unitID` pin never reaped, and the pinned one accrued 10 co-managers from the unguarded per-run mint ([seed-classic-demo.go:73,127](../../../scripts/seed-classic-demo.go)). Reap precedent: `8f9b0633`. | LoftSpace | pkg | ★★ | S | 📋 ready |
| **A qualification profile stays rewritable after the lease is approved and signed** | `SetApplicantProfile` is an "UNCONDITIONED upsert (re-submittable)" with no terminal-state guard ([ddls.go:114](../../../packages/lease-signing/ddls.go)) — verified live against an approved + signed application, so the record the landlord decided on is never preserved as a fair-housing record. `DecideLeaseApplication`'s own `DecisionFinal` ([:95](../../../packages/lease-signing/ddls.go)) is the in-package precedent. | LoftSpace | pkg | ★ | S | 📋 ready |
| **The unauthenticated class schedule advertises agent-verify artifacts** | `/api/studios` + `/api/sessions` are public-read ([server.go:29](../../../cmd/wellness-app/server.go)) and 3 of 5 studios plus 5 of 12 upcoming classes are "PO Discovery"/"Steward Verify"/"Recurrence Verify" litter — verify fires mint demo-visible entities and never reap them. Reap precedent: `8f9b0633`. | Wellness | pkg | ★ | S | 📋 ready |
| **27 already-booked appointments still carry no site; nothing lets a human correct one** | FE half fixed (new booking can no longer go site-less). `BackfillAppointmentSite` is orchestration-internal by design, never guesses ([package.go:140](../../../packages/clinic-domain/package.go)) — needs a NEW staff `SetAppointmentSite` op mirroring `RescheduleAppointment`'s grant shape ([permissions.go:112](../../../packages/clinic-domain/permissions.go)). New op + grant — full 3-layer review at build. | Clinic | pkg + FE | ★★ | S | 📋 ready |
| **An auto-charged no-show fee can never be reversed** | `MarkPastDueNoShow` debits $25 whenever a visit ends without a status update (live: 34 of Riley Chen's 48 appointments, an $825 balance), but clinic-ledger ships only debit + `ClinicCreditAccount` "record a payment" ([ddls.go:143,167](../../../packages/clinic-ledger/ddls.go)) and the FE only "+ Record charge"/"+ Record payment" ([index.html:140](../../../cmd/clinic-app/web/index.html)). A waiver booked as a payment overstates cash received. | Clinic | pkg + FE | ★★ | S | 📋 ready |
| **The patient-facing site picker offers a verify artifact as a clinic** | `/api/sites` returns 2 sites, one of them "PO Discovery Test Site", and the roster carries 5 duplicate "Classic Demo Patient" rows plus "Grid Snap Test"/"Post-Merge Verify"/"Inc2a Live Verify" patients — clinic verify fires mint demo-visible entities and never reap them. Reap precedent: `8f9b0633`; the Wellness row above is the same defect in its own seed. | Clinic | pkg | ★ | S | 📋 ready |
| **A DDL script reading an op's own `targetField` can outrun its declared `Reads` undetected** | Only `orchestration-base` has a guard test pinning each op's script-read keys against its declared `Reads`/`OptionalReads`; the other 4 packages don't — wellness's `SetInstructorProfile` shipped the gap live, caught only by manual review. | Cross-vertical | pkg | ★★ | S | 📐 needs designer pass · no-pattern: helper-aware drift-guard · [design §16](../../implementation-artifacts/staff-descriptor-rendering-design.md) |
| **`StartVisitSeries` carries no descriptor at all** | Referenced only from tests and `permissions.go` — invisible to the op catalog and to `lint-app-op-descriptors`'s registered-op scan alike. Needs an Inc-0-style descriptor sweep entry (mechanical, mirrors the 15-op sweep already done) before it's even catalog-visible, let alone migratable. | Clinic | pkg | ★ | XS | 📋 ready · [design §15](../../implementation-artifacts/staff-descriptor-rendering-design.md) |
| **Weaver ignores lease-signing's inflight_onboarding/inflight_signature suppression** | `leaseApplicationComplete`'s lens computes both markers to suppress re-dispatch while a RecordIdentityPII/SignLease task is open (tested, `lens_cypher_test.go`), but Weaver's `InflightActionMismatch` check trusts external-dispatch gaps only (Contract #10 §10.3) and now ignores both — suppression may not actually be firing. | LoftSpace | pkg | ★★ | S | 📋 ready · `health.weaver.weaver-qabvcNp3GnfCxPfpADUm` · [targets.go](../../../packages/lease-signing/targets.go) |

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

- 2026-08-22 · `f6734e6c` · Two café ghost leases held by the admin identity finally drop out of the POS lease picker — reapGhostLeases withdraws verify-fire litter live.
- 2026-08-22 · `4a16fa22` · A clinic patient's RLS anchor array stops growing by one entry per visit — clinicPatientsRead's workplace fan-out now folds via WITH+collect(DISTINCT). clinic-domain 0.34.3
- 2026-08-22 · `7d8ba666` · A café self-order no longer flashes $0.00 — renderResident overlays a pending charge until the cafeTabs projection catches up.
- 2026-08-22 · `8dea6284` · Three Protected authz lenses drop their unbounded containedIn walk for a single fixed hop (typed-relation-signatures' zero-platform replacement). loftspace-domain 0.11.1, lease-signing 0.31.5
- 2026-08-22 · `cca9b4d4` · A landlord setting renewal terms finally sees the tenant's name, not a raw key — renewalsRead gains a SECURE tenant_name column. lease-signing 0.31.4
- 2026-08-22 · `b757bab0` · Clinic's front-desk header and 7 toasts stop showing raw keys; loftspace drops 3 unlabeled reference codes. clinic-domain 0.34.1
- 2026-08-22 · `cde1826b` · A wellness no-show fee finally names its class — wellnessLedgerHistory walks settles→forSession to the session's display name, mirroring clinic-ledger's `15f628f4`. wellness-ledger 0.2.10
- 2026-08-21 · `89802f62` · A wellness member's account row finally retracts after their last booking is deleted — wellnessMemberAccounts gains DiffRetraction, mirroring landlordUnitsRead's structural-walk shape. wellness-ledger 0.2.9
- 2026-08-21 · `300ed530` · The foreground `run-{cafe,wellness,loftspace}-app` targets can finally see Postgres — mirrored `run-clinic-app`'s env wiring; `loadIdentities` no longer swallows a roster failure silently.
- 2026-08-21 · `15c46beb` · Self-credit no longer `ScriptTimeout`s — Fire 1's live-read collapse already fixed it; row closed by live re-verification (café + wellness, real 7/8-row accounts), no new code.
- 2026-08-21 · `fc15558f` · Staff descriptor rendering closes its Inc 1-4 decomposition — lint-app-op-descriptors gains the per-app op-literal ceiling ratchet. Residuals filed above.

- 2026-08-07 · `130d3958` · A wellness member can finally pay down their own balance — WellnessCreditAccount gains a consumer scope=self grant, ownership + amount capped server-side. wellness-ledger 0.2.6
- 2026-08-07 · `15209753` · A shared café house tab finally names who ordered what — Charge stamps orderedBy (op.actor) on each .status.lines entry, distinguishing a resident's self-order from a staff ring-up on the receipt. cafe-domain 0.11.18
- 2026-08-07 · `80a1f76c` · A café resident can finally pay down their own house tab — CreditCafeAccount gains a consumer scope=self grant, ownership + amount capped server-side. cafe-ledger 0.3.9
- 2026-08-07 · `dbe9e65e` · Appointments finally carry a site (new BackfillAppointmentSite op) and Riley Chen's no-show billing stops resetting daily — fixed, non-recurring appointment ids. clinic-domain 0.28.18
- 2026-08-20 · `0be26875` · Twelve staff ops self-describing (appOpDebt 15→4; residuals folded into staff-descriptor-rendering Inc 0/1/3); five packages bumped; gate gains keyed-op detector
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
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
