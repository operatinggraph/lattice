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
| **Facet surfaces ops whose dispatch target can't resolve** | Live-verified in-browser: wellness "Book a class" from the template sheet fails (`session: required vtx.session.<NanoID>; got vtx.identity.…`) — `dispatch.targetField` expects "the entity being viewed," but Facet has no session/provider browse surface. Hide/disable ops whose targetField can't resolve. | Facet | FE + pkg | ★★ | M | ✅ gate shipped (declared `dispatch.targetType`) · tail: browse surface fills `ctx.entityKey` so booking is reachable |
| **Self-anchor parameterization is declared, not name-guessed** | Andrew's principle: the JWT names the actor, and the actor's own vertices parameterize ops — declared, not inferred. The shipped auto-fill matches `<type>Key` field names; upgrade to vocabulary: `manifest.me` typed self-anchors + `{me.<anchor>}` in `dispatch.contextParams` (mirrors `{actor}`); renderer fills + hides; server-side self-scope checks already prove the param. Shares me-row work with the display-name design. | Facet | FE + pkg | ★★ | S–M | 📋 ready |
| **Display names across the graph ("fewer NanoIDs")** | Andrew PO pass: raw NanoIDs were the primary label across Facet (header, Unit/Building chips, leaseapp tasks, "Unnamed" Me). Four display classes + the floor rule "a bare NanoID is never a primary label" — see design. | Cross-vertical | FE + pkg | ★★★ | M–L | 🏗️ N1–N3 shipped, live tail open · [design §N3 tail](../../implementation-artifacts/display-name-convention-design.md) · engine + compiled-rule both cleared; next: capture the actual SYNC delta at the personal-envelope / `manifestFrame` seam |
| **Facet for staff — front-desk/operations worlds** | Andrew's question: can Facet serve the site demo's Front-Desk/Operations personas? Yes — a staff world derives from `worksAt`/`holdsRole` as residents' from `residesIn`; renderer + descriptors are identity-generic. Missing: a staff-manifest package (worklist row kinds), descriptor metadata on operator-scope ops, the cross-identity read spine (edgeManifestReadGrants + Protected-lens PII). Same binary, staff lenses — no second app. | Cross-vertical | Sally + FE + pkg | ★★★ | XL | 🚧 needs design (staff manifest + UX) |

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
- 2026-07-18 · `6e7d742a` · Facet demo-persona login posture (FACET_DEMO_PERSONAS) — persona cards, fenced dev-login, claim gate; the hosted-demo surface (deploy/demo, `83397459`), live-verified
- 2026-07-18 · `a14f67d` · Facet 2nd-renderer spike Inc 3 — descriptor-form renderer, live-verified confirmed write — [design §7.12](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `339f0a1` · Facet 2nd-renderer spike Inc 2 — write path (enqueue), live-verified confirmed write — [design §7.11](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `f29bb89` · Facet 2nd-renderer spike Inc 1 — SwiftUI client, live-verified — [design §7.10](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `16ef550` · Landlord sign-in reload race — `init()` awaits `loadIdentities()` before `applyMode()`; live-verified
- 2026-07-18 · `86f8c76` · Café self-order — `menuitem` DDL + `menuCatalog` lens; self-Charge derives amountCents from the catalog, never the caller; Resident item picker; verified via embedded-NATS tests, not live-browser (new entity)
- 2026-07-18 · `—` · Facet Fire 5 residual (2) closed — café resident self-service (OpenTab/Settle asSelf) proven live end-to-end via Core KV, not just the FE toast — [design §7.9](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-18 · `934c487` · `make seed-classic-demo` — one leaseapp/listing/appointment/tab, live-verified via loftspace-app/clinic-app/cafe-app read APIs
- 2026-07-17 · `c8962ff` · Facet Fire 5 Inc 5 — café-app real per-resident self-service auth (Me bar), exercises OpenTab/Settle scope=self in-browser — [design §7.8](../../implementation-artifacts/edge-showcase-app-design.md)
- 2026-07-17 · `8ec52ff` · Facet Fire 5 Inc 4 — café descriptor metadata + catalog-path wiring: `cafe-domain.OpMetas()` + `service-domain` `cafe` family + "Riverside Café" showcase template, live-verified on the SSE manifest feed
- 2026-07-17 · `a5828c5` · Facet Fire 5 — café self-scope adoption Inc 1: `OpenTab`/`Settle` consumer scope=self grant via the lease's applicationFor link; package layer only, FE checkpointed
- 2026-07-17 · `97db230` · Facet Fire 5 — wellness catalog-path wiring: `wellness` family + Riverside Wellness Studio template availableAt/permitsOperation-wired to CreateBooking/CancelBooking, live-verified on the SYNC manifest feed
- 2026-07-17 · `0d16c34` · Facet Fire 5 — wellness edge-manifest descriptor metadata (CreateBooking/CancelBooking; first real use of `dispatch.contextParams`)
- 2026-07-17 · `a5f7ee9` · Fixed clinic's Fire 5 Inc 1 `Dispatch.Class` bug (was "clinic", should be DDL CanonicalName "appointment") — confirmed live, regression-guarded
- 2026-07-17 · `fa8ccee` · Wellness self-service booking CLOSED — wellness-app "Me" bar (sign in, no claim ceremony needed) + fixed a pre-existing booker/bookedBy read-posture gap; live-verified
- 2026-07-17 · `ac21a6c` · Wellness self-service booking Inc 1 — `CreateBooking`/`CancelBooking` consumer scope=self grant, mirrors clinic-domain's self-service pattern; package layer only, FE checkpointed
- 2026-07-17 · `27dc762` · Clinic no-show fee auto-charge — `clinicNoShowSettlement` Weaver playbook + `DebitAccount` `appointmentRef`, self-contained in `clinic-ledger` — [design](../../implementation-artifacts/clinic-noshow-fee-design.md)
- 2026-07-17 · `72dc9ed` · Facet Fire 5 Inc 2 — clinic catalog-path wiring: `clinic` family + Riverside Clinic template availableAt/permitsOperation-wired, live-verified
- 2026-07-17 · `cd8696d` · Facet Fire 5 Inc 1 — edge-manifest descriptor metadata (presentation/inputSchema/dispatch) on clinic-domain's 3 self-scope consumer ops; café/wellness have no self-scope ops yet (named, not built)
- 2026-07-17 · `9f46554` · Facet Inc 4 — Fire-2 fidelity tail: R3 touchedKey/pending-chip, dead Retry removed, connectivity-keyed offline banner, Outbox history collapse, degraded-render regression test
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
