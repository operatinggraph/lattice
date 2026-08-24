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
| **`AttachObject`/`DetachObject` stay hand-built with a named fix path unbuilt** | The attach pair needs an upload-ceremony affordance + owner-anchored read surface (`signInMethods`-pane precedent). Baselined in `appOpDebt`. (`CreateLocation`'s `ClassChoices` half shipped `aa04a7a5` — Done log.) | Cross-vertical | pkg + FE | ★ | S | 📋 ready · [design §7](../../implementation-artifacts/staff-descriptor-rendering-design.md) |
| **Wellness guest-booking has no name-search, only a typed raw key** | Split off a shipped D4 sweep: a walk-in guest has no lease anywhere, so every sibling identity lens' lease→unit→building anchor can't back a search picker for one — a new un-anchored staff-searchable identity lens, security-relevant. | Wellness | pkg (design) | ★ | S | 📐 needs designer pass · no-pattern: un-anchored staff identity search · [brief §7](../../../docs/reviews/vertical-app-descriptor-audit-2026-08-20.md) |
| **A task card's only discriminator was a raw key, now removed** | D4 forbade the unlabeled key spans loftspace's application/task cards used to show — correctly removed, but two open tasks for the same op, both with no due date, now render identically. Needs a real per-instance label, not a mechanical mirror — no app resolves an arbitrary-typed entity key to a name today. (Renewal-card half shipped `cca9b4d4`.) | LoftSpace | pkg + FE (design) | ★ | S | 📐 needs designer pass · no-pattern: entity-key-to-name resolution for cards |
| **The executed lease still doesn't name its tenant** | `/api/lease-document` renders `Tenant: vtx.identity.edu97ix…` — the applicant's real name is never assembled (`doc.TenantName`), a sensitive link-discovered aspect with no egress-declaration path. The landlord party now resolves via the unit's `manages` link (shipped `d46ab947`). | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `[Loom] externalTask subject-only egress` |
| **A qualification profile stays rewritable after the lease is approved and signed** | `SetApplicantProfile` is an "UNCONDITIONED upsert (re-submittable)" with no terminal-state guard ([ddls.go:114](../../../packages/lease-signing/ddls.go)) — the fair-housing record the landlord decided on is never preserved. | LoftSpace | pkg (design) | ★ | S | 📐 needs designer pass · no-pattern: profile-terminal-state vs renewal reuse (a naive guard breaks SignRenewal's live consumer, grounded 2026-08-23) |
| **A DDL script reading an op's own `targetField` can outrun its declared `Reads` undetected** | Only `orchestration-base` has a guard test pinning each op's script-read keys against its declared `Reads`/`OptionalReads`; the other 4 packages don't — wellness's `SetInstructorProfile` shipped the gap live, caught only by manual review. | Cross-vertical | pkg | ★★ | S | 📐 needs designer pass · no-pattern: helper-aware drift-guard · [design §16](../../implementation-artifacts/staff-descriptor-rendering-design.md) |
| **The executed lease document generates but never attaches** | `missing_leaseDocAttach`'s directOp submits `AttachObject` as the Weaver service actor and the Processor rejects it `AuthDenied: no matching platformPermission` (verified live end-to-end). `docStoreName`/`docDigest` land, `leaseDocAttached` stays false, and `/api/lease-document` answers "being generated — try again shortly" permanently. | LoftSpace | platform | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `[Bootstrap] re-bootstrap strands prior-epoch operator grants` |
| **Weaver ignores lease-signing's inflight_onboarding/inflight_signature suppression** | `leaseApplicationComplete`'s lens computes both markers to suppress re-dispatch while a RecordIdentityPII/SignLease task is open (tested, `lens_cypher_test.go`), but Weaver's `InflightActionMismatch` check trusts external-dispatch gaps only (Contract #10 §10.3) and now ignores both — suppression may not actually be firing. | LoftSpace | pkg | ★★ | S | 🚧 blocked-on: [lattice.md](lattice.md) `[Weaver] inflight_<g>` |
| **29 of 63 appointments carry no site and `clinicSiteBackfill` closes the gap for none** | 16 have a provider at EXACTLY ONE site — the shape the op documents as convergent — yet stay unlinked (a hand-submitted `SetAppointmentSite` committed first try); the other 13 have a zero-site or roster-unresolvable provider, and `violating` carries no status/remediability term, so Weaver redispatches long-past noShow/cancelled rows forever. | Clinic | pkg | ★★ | S–M | 📋 ready · [lenses.go:743](../../../packages/clinic-domain/lenses.go) |
| **A recurring visit series started without an end date can never be ended** | `activeUntil` is write-once at `StartVisitSeries`; the only later levers are Pause/Resume, and the FE's own "Ended" state is reachable only by a decision made at booking time. All 5 live series are open-ended, 3 for patients the roster no longer lists — a discharged or departed patient stays armed for re-arming forever. | Clinic | pkg + FE | ★★ | S | 📋 ready · [visitseries.go](../../../packages/clinic-reminders/visitseries.go) |
| **An automated lapse sweep bills patients $25 with no staff decision** | `MarkPastDueNoShow` has "no human caller" yet writes the same billable `noShowFeeCents: 2500` a staff-observed no-show does — 45 of 63 live appointments auto-no-showed, $1,125 charged, one patient at $950. An appointment nobody at the desk closed is a documentation lapse, not a missed visit; PO ruling: the sweep marks without a fee. | Clinic | pkg | ★★ | S | 📋 ready · [design](../../implementation-artifacts/clinic-noshow-fee-design.md) |
| **Weaver's dispatch loop redispatches 5 gap/action pairs on `leaseApplicationComplete` without ever observing a close** | `LensEffectMismatch` (standing since 2026-08-21) on the missing_site/bgcheck/onboarding/payment/signature columns — per `weaver-planner-mandate-design.md` §3.4 points at a stale/wrong guard, a wrong lens column, or a no-op remediation; two also `GapBudgetExhausted`. May share a root with the row above. | LoftSpace | pkg | ★★ | S–M | 🚧 blocked-on: [lattice.md](lattice.md) `[Weaver] inflight_<g>` |

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

- **Rotation to date:** LoftSpace ×24, Clinic ×23, Café ×13, Wellness ×10.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-08-05:** Wellness — drove member + instructor hats live through book/cancel; billing is unlabeled, no class is priced, studios duplicate; filed 3.
- **2026-08-06:** LoftSpace — drove applicant + landlord hats live; the applicant lens is paused dead, the health signal can't fire, renewals never opened; filed 4 + 2 platform.
- **2026-08-06:** Clinic — drove patient, provider + front-desk hats live; no visit says which site, a $750 bill is 28 identical lines, no patient can pay; filed 4.
- **2026-08-07:** Café — drove resident self-order→settle + front-desk hats live; no resident pays their own tab, no line names its orderer; filed 3 + 1 platform.
- **2026-08-07:** Wellness — drove member + front-desk hats live through schedule/book/bill; a retired studio strands its classes, no member self-pays, no fee names its class; filed 4.
- **2026-08-07:** LoftSpace — drove applicant, landlord + staff hats live through profile/decide/renew/search; 3 listings are unleasable, a renewal never names its tenant; filed 3.
- **2026-08-07:** Clinic — drove patient, provider + front-desk hats live; 27 of 60 appointments have no location, no no-show fee is reversible; filed 3 + 1 platform.
- **2026-08-08:** Café — drove resident open→self-order→settle→post live; no self-pay works on any vertical, names are dead, a new order reads $0; filed 3 + 1 platform.
- **2026-08-22:** Wellness — drove member, instructor + front-desk hats live through book/cancel/bill; the resident rate charges standard price, a class is uneditable once scheduled, the orphan-studio repair has no caller; filed 3.
- **2026-08-23:** LoftSpace — drove applicant + landlord hats live through apply/profile/PII/sign/approve; 13 rivals still asked to sign a leased unit, the executed lease never attaches, the storefront is empty; filed 3 + 1 platform.
- **2026-08-23:** Clinic — drove patient + operator hats live through book/site/series/ledger; 29 appointments have no site and none can converge, a series can never end, a sweep bills $1,125 unasked; filed 3.
- **Next:** Café.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-08-23 · `aa04a7a5` · [location-domain] `CreateLocation` gets a full descriptor via a new `Dispatch.ClassChoices` field — location-domain 0.4.0, edge-manifest 0.17.0; `AttachObject`/`DetachObject` stay open debt
- 2026-08-23 · `fee0621b` · Clinic /api/sites + roster stop advertising verify artifacts — reapNonCanonicalSites/Patients allowlist by pinned id (name-marker missed some); reaped 10 patients + 5 buildings live.
- 2026-08-23 · `533f639e` · The wellness class schedule stops advertising agent-verify litter — reapVerifyLitter reaps "Verify"/"Discovery"-named studios/sessions (5 sessions + 3 studios reaped live).
- 2026-08-23 · `d46dd59f` · The applicant storefront stops going permanently empty — seed-classic-demo's backfillBareListings gives every live listing-less unit an available listing (0→5 live), mirroring the file's own reap helpers.
- 2026-08-23 · `c7a8e222` · A losing rival on a leased unit stops being dispatched RecordIdentityPII/SignLease — the applicant gaps gain the `unitStatus <> 'leased'` term missing_listingLeased already used. lease-signing 0.31.7
- 2026-08-23 · `8f49c13b` · StartVisitSeries's "no descriptor" row closed by verification — visitSeriesOpMetas already ships a full OpMetaSpec (that commit), lint-app-op-descriptors reports 0 issues; no new code.
- 2026-08-23 · `69823fbe` · The 6 sessions stuck on "Studio needs reassignment" finally have a way out — reassign form gains a studio picker wired to the operator-only newStudio repair path.
- 2026-08-23 · `c5aabf68` · A full or mispriced class no longer needs TombstoneSession + recreate — ReassignSession edits name/capacity/priceCents/residentPriceCents in place, front-desk Reassign form gains the fields. wellness-domain 0.22.5
- 2026-08-23 · `7e6b1ab3` · A waived no-show fee no longer overstates cash collected — ClinicCreditAccount gains reason (payment|waiver), front-desk/operator only, rejected on a self-scoped patient credit. clinic-ledger 0.2.12
- 2026-08-23 · `abe579ca` · A verified resident finally pays less than a walk-in — sessions gain residentPriceCents, classPriceSettlementSpec CASE WHENs on rate. wellness-domain 0.22.4, wellness-ledger 0.2.11
- 2026-08-22 · `4fae046b` · The clinic patient roster, empty for EVERY actor, finally projects — pre-2026-08-08 patients get a one-time BackfillPatientRegistration. clinic-domain 0.34.5
- 2026-08-22 · `4a56c575` · A clinic staffer can finally fill in an appointment's missing site — new SetAppointmentSite op (17→16 live), CreateOnly guard aspect closes a concurrent-write race an adversarial review caught. clinic-domain 0.34.4
- 2026-08-22 · `18c867d1` · The LoftSpace storefront's 8 duplicate listings reap to one — the landlord converges on its existing link instead of co-manager-minting a fresh one per rerun (12 accrued → 1). seed-classic-demo.go
- 2026-08-22 · `be2588d8` · A wellness class whose studio was retired finally has a way out — ReassignSession gains an operator-only newStudio repair path; orphaned classes flag `missingStudio` instead of a blank location. wellness-domain 0.22.3
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
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
