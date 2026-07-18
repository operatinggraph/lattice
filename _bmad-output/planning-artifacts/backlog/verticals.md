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
| **Account settings — manage sign-in methods** | Live-verified: LoftSpace has no account/profile surface at all today (grepped `app.js`/`index.html` — only qualification-profile, no identity page). Page for the applicant to see linked credentials (`whoami`), link another (`InitiateCredentialLink`/`CompleteCredentialLink`), and remove one (`UnlinkCredential`, platform refuses removing the last). | LoftSpace | FE + pkg | ★★ | S | ✅ shipped `25623d9` |
| **Care→Wellness referral** | Post-visit, the clinic worklist offers a bookable wellness class (the clinic+wellness emergence — shared scheduling shape); a clinic→wellness handoff that opens a booking from the appointment context. | Clinic/Wellness | pkg + FE | ★ | S | ✅ shipped `e86ab45` |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **No-show doesn't cost anything** | `SetAppointmentStatus(status=noShow)` is purely a status flip — no consequence. Corrected 2026-07-12: the claimed `clinic-reminders` precedent doesn't hold (that gap never touches the ledger); real shape mirrors `cafe-domain`'s `missing_charge → directOp(DebitAccount)` — needs a new package spanning `clinic-domain`+`clinic-ledger`, a `DebitAccount` back-ref param, and a fee-amount decision. | Clinic | pkg | ★ | M | ✅ shipped · [design](../../implementation-artifacts/clinic-noshow-fee-design.md) |
| **Clinic is a single-location, single-specialty silo** | `location-domain` is unused by `clinic-domain` (explicit in its own docs, unlike `loftspace-domain`); a provider has exactly one `specialty` and no site. A real multi-site practice group needs provider↔location + per-location scheduling — mirror `loftspace-domain`'s already-proven `location-domain` integration pattern. Bigger structural lift; sequence after the other Clinic items land. | Clinic | pkg | ★★ | L | ✅ shipped · [design](../../implementation-artifacts/clinic-multisite-design.md) |
| **Booking is provider-first, no specialty-based search** | Booking form now has a specialty filter + a "soonest available" panel computing each matching provider's earliest open slot. | Clinic | FE | ★ | S | ✅ shipped `8315a88` |
| **Café tab: no guard against a 2nd concurrent open tab per lease** | Live-verified: `OpenTab` (`cafe-domain/ddls.go:225`) mints unconditionally, no dedup — unlike this package's own `cafeLedgerAccountGuard` "one account per lease" precedent. POS's `renderPos` (`app.js:169`) picks one via `find()`, silently ignoring the other — a real revenue leak, not just a UI quirk. | Café | pkg | ★★ | S | ✅ shipped `3def314` |
| **Facet cold-start races the cap projection** | Fresh `make up-facet` could fail facet on cold start: `seed-edge-demo` printed the tenant handoff before its `AssignRole`'s `ctrl.refractor.register` grant (packages/control-authz) finished re-projecting into `cap.roles.<tenant>` → `controlauth: no capability kv entry for actor`. | Facet | pkg | ★ | S | ✅ shipped `ef45e83` |
| **Café self-order (catalog-bound self-Charge)** | Live-verified: a resident can OpenTab/Settle their own tab (self scope-checked correctly, incl. cross-lease + Charge denial), but Charge stays operator-only (`permissions.go`) — no menu/item catalog exists to bound a self-submitted `amountCents`. Unlike CreateBooking/CreateAppointment's catalog-bound slot, café residents can't ring up their own order. | Café | pkg | ★★ | M | ✅ shipped `86f8c76` |
| **Classic vertical demo data has no seed path** | Live-verified: shared dev stack's Core KV holds zero `leaseapp`/`listing`/`appointment`/`tab` vertices — only `seed-edge-demo`/`seed-showcase` exist, and both mint Facet catalog scaffolding only (no `CreateLeaseApplication`/`SetListing`/`CreateAppointment` call in either). Blocks live PO discovery of classic (non-Facet) flows out of the box. | Cross-vertical | pkg | ★★ | S | ✅ shipped `934c487` |
| **Landlord sign-in races the identity roster, can hard-fail** | Live-verified: reload in Landlord mode with an already-claimed applicant can 400 `InvalidStateTransition: RotateClaimKey requires state=unclaimed, got claimed` — `applyMode()` fires before `loadIdentities()` resolves, so `identityState()`'s already-claimed short-circuit (`app.js:474`) reads a still-empty roster. Fix: await roster load first. | LoftSpace | FE | ★★ | XS | ✅ shipped `16ef550` |

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

- **Rotation to date:** LoftSpace ×15, Clinic ×12, Café ×4.
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801/:7802), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All four apps exist + are exercisable live (`:7788` / `:7799` / `:7801` / `:7802`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-09:** LoftSpace — exercised Browse&Apply live; found + root-caused self-service identity never claims (blocks CreateLeaseApplication for every applicant); filed.
- **2026-07-10:** Clinic — drove staff booking/schedule/ledger live; found + confirmed `/api/ledger` unauthenticated (any caller reads any patient's billing history); filed.
- **2026-07-10:** Café — drove POS OpenTab/Charge/Settle live; found stale post-write state, mirrored LoftSpace's existing fix; filed.
- **2026-07-10 — REQUEST fulfilled:** LoftSpace — live-verified no account surface exists; filed "manage sign-in methods", blocked-on multi-credential design Fires 2+4.
- **2026-07-11:** Clinic — drove booking/schedule/ledger live; booking form is provider-first with no specialty search, filed FE-only fix; no platform block.
- **2026-07-11:** Café — drove OpenTab/Charge/Settle live; found no per-lease open-tab guard (2 concurrent open tabs same lease), filed pkg fix; no platform block.
- **2026-07-11:** LoftSpace — Apply rejected for every applicant; root-caused to the demo skipping the claim ceremony (app-side, not platform), filed.
- **2026-07-12:** Clinic — drove booking/My Appointments live + code-verified permission pins; found self-service patients can book but never reschedule/cancel themselves (operator-only ops), filed.
- **2026-07-17:** Café — hand-minted a lease + drove OpenTab/Charge/Settle + self-service scope=self live (open/settle-own-lease ✅, cross-lease + Charge correctly denied ✅); found no classic demo seed data + no self-order catalog, filed both.
- **2026-07-18:** LoftSpace — drove Applicant Browse/Apply/My Applications live (clean) + Landlord console; caught a live reload race hard-failing sign-in with `RotateClaimKey requires state=unclaimed, got claimed`, root-caused + filed.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-07-17 · `0d55b2a` · Facet Inc 3 — Me-screen claim/link UX + revocation UX; linked credential now signs in via `/v1/actor` (G10's first consumer); sign-out purges the local mirror
- 2026-07-16 · `2696321` · Showcase dataset (§7.3) CLOSED — honest family enum + `RetireServiceTemplate` + idempotent `make seed-showcase` loader; retired both mislabeled live templates
- 2026-07-16 · `ef45e83` · Facet cold-start cap-projection race CLOSED — `seed-edge-demo` waits for `ctrl.refractor.register` in `cap.roles.<tenant>` before the tenant handoff
- 2026-07-13 · `e86ab45` · Care→Wellness referral — clinic worklist "Book wellness class" CTA + `/api/wellness/sessions` proxy, `CreateBooking` submit
- 2026-07-13 · `25623d9` · LoftSpace account settings — new `identityCredentialsRead` Secure Lens + Account tab (list/link/unlink); live-verify pending, dev-stack installs broken
- 2026-07-13 · `f5b3031` · Edge showcase app (Facet) Fire 2 — `cmd/facet` dev host + PWA renderer, live-verified — [UX](../../implementation-artifacts/facet-app-ux.md)
- 2026-07-13 · `—` · Edge showcase app (Facet) Fire 2 UX spec — descriptor-form renderer + widget vocabulary mapping, Outbox/conflict UX, `cmd/facet` binary shape — [UX](../../implementation-artifacts/facet-app-ux.md)
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
