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
| **Edge showcase app (Facet)** | Discovery-driven personal client on the Edge foundation: hardcodes only IdP login + connect; services, ops, forms, tasks arrive as data via `edge-manifest` personal lenses + a descriptor vocabulary (#52/#54/#55). PWA-first. | Cross-vertical | Sally + FE Engineer + pkg | ★★★ | XL | 🏗️ building · [design §7.12](../../implementation-artifacts/edge-showcase-app-design.md) · 2nd-renderer spike Inc 3 shipped (descriptor-form renderer, live-verified confirmed write) · next: literal iOS build only |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **Cancel a booking from Facet** | Riley can book a class but not cancel it: CancelBooking targets a `booking`, and no surface carries the actor's own bookings (`manifest.inst` covers service instances only). Wants an own-bookings projection (bookedBy walk) feeding the same entity-detail path the browse view uses. | Wellness | FE + pkg | ★★ | S | 📋 ready · extends [entity-browse](../../implementation-artifacts/facet-entity-browse-design.md) mechanism |
| **Showcase demo session ages out** | The fixed-handle Vinyasa Flow session seeds 24h out and is never refreshed, so after a day the hosted demo's Nearby view offers only a past class — a demo visitor can't book. Seed wants a rolling re-mint (and/or the renderer hides past sessions). | Wellness | pkg | ★ | XS | 📋 ready · consumer: hosted Facet demo (F20) |
| **Facet for staff — front-desk/operations worlds** | Andrew's question answered YES: the site demo's Front-Desk/Operations personas on the same binary — a staff world derives from `worksAt`/`holdsRole` as residents' from `residesIn`; staff catalog + claimable role-queue over the manifest, PII worklists via the Protected plane; first staff role narrower than root. | Cross-vertical | Sally + FE + pkg | ★★★ | XL | 🏗️ building · [design](../../implementation-artifacts/facet-staff-worlds-design.md) · F1–F3 read spine shipped · next: F3's `/api/staff/*` + Worklist UX |
| **Maintenance work-order producer** | The op set that mints role-queued maintenance work orders (report → `vtx.task` queuedFor the `maintenance` role) — the domain content the staff-worlds offline beat (F5, basement tech) claims and completes; the claim/complete/offline mechanism ships in the staff-worlds fires, this row is only the producer. | Cross-vertical | pkg | ★★ | S | 📋 ready · consumer: [staff-worlds F5](../../implementation-artifacts/facet-staff-worlds-design.md) |

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

- **Rotation to date:** LoftSpace ×15, Clinic ×12, Café ×4, Wellness ×1.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-10:** Clinic — drove staff booking/schedule/ledger live; found + confirmed `/api/ledger` unauthenticated (any caller reads any patient's billing history); filed.
- **2026-07-10:** Café — drove POS OpenTab/Charge/Settle live; found stale post-write state, mirrored LoftSpace's existing fix; filed.
- **2026-07-10 — REQUEST fulfilled:** LoftSpace — live-verified no account surface exists; filed "manage sign-in methods", blocked-on multi-credential design Fires 2+4.
- **2026-07-11:** Clinic — drove booking/schedule/ledger live; booking form is provider-first with no specialty search, filed FE-only fix; no platform block.
- **2026-07-11:** Café — drove OpenTab/Charge/Settle live; found no per-lease open-tab guard (2 concurrent open tabs same lease), filed pkg fix; no platform block.
- **2026-07-11:** LoftSpace — Apply rejected for every applicant; root-caused to the demo skipping the claim ceremony (app-side, not platform), filed.
- **2026-07-12:** Clinic — drove booking/My Appointments live + code-verified permission pins; found self-service patients can book but never reschedule/cancel themselves (operator-only ops), filed.
- **2026-07-17:** Café — hand-minted a lease + drove OpenTab/Charge/Settle + self-service scope=self live (open/settle-own-lease ✅, cross-lease + Charge correctly denied ✅); found no classic demo seed data + no self-order catalog, filed both.
- **2026-07-18:** LoftSpace — drove Applicant Browse/Apply/My Applications live (clean) + Landlord console; caught a live reload race hard-failing sign-in with `RotateClaimKey requires state=unclaimed, got claimed`, root-caused + filed.
- **2026-07-18:** Wellness — first-ever PO exercise (live since 07-09, never driven); found empty studios/sessions, hand-minted one + proved self-service booking/cancel end-to-end live, filed the seed gap + missing studio-admin FE.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-07-19 · `415e18f3` · Facet staff worlds F3 read spine — `staffReadGrants` (cap-read.staff, building-anchored) + workplace anchors on both worklist tables; live wire→read-1 / unwire→read-0
- 2026-07-19 · `c662dc54` · ~~F3 anchors~~ — **this SHA never reached main** (dangling, on no branch); the work was reconstructed in `5c797e03` + `415e18f3`. Left as a marker: a Done-log SHA is not proof the code landed
- 2026-07-19 · `58d165ee` · Facet staff worlds F2 — `permission forOperation meta` + role-derived catalog/queue sibling lenses + staff read-grant slice; frontOfHouse Personal-Lens control grant (staff device synced nothing without it)
- 2026-07-19 · `cc50f86a` · Facet staff worlds F1 — `worksAt` staff spine + staff op set widened onto the shipped `frontOfHouse`; showcase staff persona; live-verified narrower-than-root
- 2026-07-19 · `753637ca` · Descriptor dispatch declares its optional reads — `Dispatch.OptionalReads` + a `:id` bare-id modifier for link keys; café OpenTab/Settle now fully Facet-drivable, open→settle→reopen proven from the declarations
- 2026-07-19 · `212dd3f1` · Self-anchored op params are declared, not name-guessed — `edgeIdentity` projects typed `selfAnchors`; `{me.<type>}` joins the contextParams vocabulary; café OpenTab renders a fieldless form, live-proven in Core KV
- 2026-07-19 · `c3ec584b` · `reinstall-package` same-version edits reach the Processor again — op requestId now folds a mutation digest, so only genuinely identical work dedups; unblocked the live proof of `51a418b5`
- 2026-07-19 · `51a418b5` · Personal Lens keeps its business key columns across a cypher edit — hot-reload threaded none, dropping the executor to its single-key fallback; edgeCatalog's 142k-error retry loop cleared to 0
- 2026-07-19 · `—` · Display-name N3 CLOSED — live-verified on the showcase stack: feed serves decrypted `displayName` "Riley Chen" + named anchors; tail was a stale device mirror, no code defect
- 2026-07-19 · `—` · Display-name N3 tail localized — SYNC delta carries both N3 fields; cloud path clean end-to-end, device mirror serves a stale pre-N3 row (diagnosis only, no code change)
- 2026-07-19 · `2a0af7e3` · Display-name N3 tail narrowed — both design-named candidates falsified (engine resolves both aspect shapes, now pinned; compiled rule is current); omission is downstream of projection
- 2026-07-19 · `93c6064d` · SetLocationPresentation replaces (was create-only → RevisionConflict on any second set, untested) + seed-showcase names an already-seeded world; live-verified on the showcase stack
- 2026-07-19 · `dda7ad98` · Facet dispatch targets resolve by declared type — `dispatch.targetType` vocabulary + renderer gate; unresolvable ops degrade instead of failing at the Processor; live-verified both directions
- 2026-07-19 · `502d3b4d` · Wellness classic-demo seed — `seed-classic-demo` mints a studio + one bookable session (15-min grid, slot-claim optionalReads); both ops accepted live and projected through to `/api/studios` + `/api/sessions`
- 2026-07-19 · `074c0b86` · Facet session idle gate + auth-death bounce — renewal gated on activity (30m window); a terminal 401 or a whoami-confirmed dead cookie closes the feed and bounces to `/login`, both modes; live-verified
- 2026-07-19 · `4185b3c0` · Display names N3 (self-name) — me-lens projects the sealed `name` envelope, `edge/vault.SelfName` decrypts it in-engine on both hosts; fixes a Refractor hot-reload bug that silently refused Personal Lens cypher edits
- 2026-07-19 · `a02784ee` · Display names N2-tail (scoped-target name lens) — edgeTasks walks task→leaseapp→unit, projects the unit's `.presentation` name; Facet task rows read "Unit 1 lease" not a bare NanoID
- 2026-07-18 · `fe2d0e5e` · Display names N2 (renderer floor rule) — typed label ladder (prettify/anchorLabel/identityLabel); no bare NanoID as a primary label, "Unnamed" gone; live-verified on the showcase resident (header/Me/tasks/places)
- 2026-07-18 · `e1ddf8a2` · Facet token-refresh fix — NATS + Gateway-submit + session cookie all now survive past the 30m login JWT (TokenHandler/TokenSource, Acquire dead-conn rebuild, sliding-session refresh endpoint)
- 2026-07-18 · `90f0b7a2` · Display names N1 (pkg) — location `.presentation` aspect + `SetLocationPresentation` op + named seeds + `edgeIdentity` anchor name projection; op path proven e2e + verify-package 38-assert green
- 2026-07-18 · `4cc6e906` · Facet café OpenTab leaseAppKey auto-fill — manifest self-anchored keys indexed by vtx type; a `<type>Key` field with one match fills read-only (hidden input), ambiguous/absent falls back to editable; node vectors
- 2026-07-18 · `c6f2c0d3` · Wellness Studios tab — CreateStudio form + per-studio inline CreateSession (slot-cell optionalReads mirror `slot_cells`); op path proven end-to-end, studio + session committed + projected
- 2026-07-19 · `7341ad73` · Facet entity browse — Nearby view + `manifest.ent` lenses, wellness `locatedAt`; live-verified booking E2E — [design](../../implementation-artifacts/facet-entity-browse-design.md)
- 2026-07-18 · `6e7d742a` · Facet demo-persona login posture (FACET_DEMO_PERSONAS) — persona cards, fenced dev-login, claim gate; the hosted-demo surface (deploy/demo, `83397459`), live-verified
- 2026-07-18 · `a14f67d` · Facet 2nd-renderer spike Inc 3 — descriptor-form renderer, live-verified confirmed write — [design §7.12](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `339f0a1` · Facet 2nd-renderer spike Inc 2 — write path (enqueue), live-verified confirmed write — [design §7.11](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `f29bb89` · Facet 2nd-renderer spike Inc 1 — SwiftUI client, live-verified — [design §7.10](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `16ef550` · Landlord sign-in reload race — `init()` awaits `loadIdentities()` before `applyMode()`; live-verified
- 2026-07-18 · `86f8c76` · Café self-order — `menuitem` DDL + `menuCatalog` lens; self-Charge derives amountCents from the catalog, never the caller; Resident item picker; verified via embedded-NATS tests, not live-browser (new entity)
- 2026-07-18 · `—` · Facet Fire 5 residual (2) closed — café resident self-service (OpenTab/Settle asSelf) proven live end-to-end via Core KV, not just the FE toast — [design §7.9](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `934c487` · `make seed-classic-demo` — one leaseapp/listing/appointment/tab, live-verified via loftspace-app/clinic-app/cafe-app read APIs
- 2026-07-17 · `c8962ff` · Facet Fire 5 Inc 5 — café-app real per-resident self-service auth (Me bar), exercises OpenTab/Settle scope=self in-browser — [design §7.8](../../implementation-artifacts/edge-showcase-app-design.md)
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
