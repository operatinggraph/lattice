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
| **Wellness vertical** (classes) | `wellness-domain` (studio + class/session + booking) reusing clinic-domain's generic slot-claim guard (`slot_cells`/`claim_cell`; only hub + aspect names change) + thin FE (schedule grid · roster · my-classes). **Resident-rate** payoff: CreateBooking reads the booker's live lease (`contextHint.reads`; verify `heldBy` the booker → no over-grant) then applies the member rate + writes `booking residentRate lease`. | Wellness | pkg + FE | ★★ | L | 🏗️ building · [design](../../implementation-artifacts/wellness-vertical-design.md) · next: FE |
| **Mixed-use composition surfaces** | The "more than the sum" beats across lenses the one-liner omitted: **front-desk** unified resident context (lease + visit + open tab + booked class in one lookup, surfaced before asked) and **operations** portfolio-pulse aggregate (occupancy + service-attach-rate across packages) — views that exist only because the packages share one graph. Aggregate lenses + FE across both apps + Loupe. | Café/Wellness | FE + pkg | ★★★ | M | 📋 ready (after Wellness) |
| **Care→Wellness referral** | Post-visit, the clinic worklist offers a bookable wellness class (the clinic+wellness emergence — shared scheduling shape); a clinic→wellness handoff that opens a booking from the appointment context. | Clinic/Wellness | pkg + FE | ★ | S | 📋 ready (after Wellness) |
| **Clinic patient picker doesn't scale** | Front-desk booking (`#patient` select, `app.js:340`) + `GET /api/staff/patients` (`patients.go:84`) both return/render the FULL unfiltered roster with no name-filter/search — every booking starts by scrolling a raw `<select>` of every patient the clinic has ever seen. Fine at demo scale, a real front-desk workflow blocker past a few dozen patients. Add a name-ILIKE query param to `queryPatients` + a debounced-input/typeahead replacing the plain select (same pattern, lower urgency, for `#provider`). | Clinic | pkg + FE | ★★ | S | 📋 ready |

**Spec** = the go-live composition demo (public-presence site, `localhost:7900/#demo`) — four lenses × package
toggles. PO ruling: all composition is **package-level, no Lattice block** (ledger `heldFor` anchor · generic
`claim_cell` · `contextHint.reads` — precedent: `DebitAccount`→clause; file:line grounding in the commit).
Build against the real key shapes, not the demo's: keys are **NanoIDs** (Contract #1) and the account→lease
relation is `heldFor` (the demo's `ACC88`/`BK7`/`L204` + `billedWith` are cosmetic).

## PO notes (dated — drives rotation)

Compact rotation memory only — PO *findings* are filed as demand rows above + the Done log; the verbose
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic, staggered from the Steward.

- **Rotation to date:** LoftSpace ×11, Clinic ×9 (last: Clinic 2026-07-09, drove CreateProvider→CreatePatient→CreateAppointment live end-to-end incl. the double-book guard; filed the patient-picker scale gap).
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. Both apps exist + are exercisable live (`:7788` / `:7799`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-06:** Enriched Café+Wellness → 4 grounded, sequenced rows (Café first) + verified no platform block; spec = the go-live composition demo.
- **Next:** LoftSpace.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-07-07 · `—` · Café vertical CLOSED — Inc1-3 shipped; Refractor-restart live-verified `one-bill-history` — [design](../../implementation-artifacts/cafe-ledger-design.md)
- 2026-07-07 · `7556f62` · Café vertical Inc 3 — `packages/one-bill` combined-statement lens (two Lenses, one bucket, no cypher UNION), live-reproject pending a Refractor restart — [design](../../implementation-artifacts/cafe-ledger-design.md)
- 2026-07-07 · `8de14dd` · Café vertical Inc 2b — `cafe-app` thin FE (POS/front-desk/resident), live-verify pending — [design](../../implementation-artifacts/cafe-ledger-design.md)
- 2026-07-07 · `5d065db` · Café vertical Inc 2a — `cafe-domain` tab lifecycle + Weaver-posted settlement — [design](../../implementation-artifacts/cafe-ledger-design.md)
- 2026-07-07 · `317fbe9` · Café vertical Inc 1 — `cafe-ledger` house-tab payment ledger — [design](../../implementation-artifacts/cafe-ledger-design.md)
- 2026-07-07 · `37f3a6a` · LoftSpace+Clinic browser-direct writes through the Gateway CLOSED — real-actor-write-auth-e2e Phase 1 item 5, live-verified — [design](../../implementation-artifacts/real-actor-write-auth-e2e-design.md)
- 2026-07-07 · `921fda4` · LoftSpace consumer-scope op grant (real allow/deny) CLOSED — built cross-lane in the Lattice Phase-1 e2e fire (`CreateLeaseApplication` → consumer scope=self); board was stale, reconciled here
- 2026-07-05 · `—` · LoftSpace lease renewal → MOVED to the [lattice lane](lattice.md) at ratification (anti-ping-pong) — [design](../../implementation-artifacts/loftspace-lease-renewal-goal-authored-target-design.md)
- 2026-07-05 · `e3cd7da` · Steward continuous-improvement — hardened the RLS regression test for beds/baths/move-in (seeded + asserted the 3 columns; verified the guard fails against a reverted SELECT/Scan)
- 2026-07-05 · `b663c1c` · LoftSpace My Applications beds/baths/move-in CLOSED — `selectApplicationsSQL` now selects the 3 columns `selectApplicationByKeySQL` already did
- 2026-07-05 · `7eb3330` · LoftSpace D1.5 landlord RLS decision surface CLOSED — stale block label; already fully built (5b-ii/-ii-b/-ii-c) — [design](../../implementation-artifacts/loftspace-d1.5-landlord-rls-decision-surface-design.md)
- 2026-07-05 · `a710c7a` · LoftSpace applicant email/phone to landlord CLOSED — stale block (was `blocked-on Vault 5b`); subsumed by the same Secure-Lens columns, live-verified in the RLS card's contact line
- 2026-07-05 · `109f59a` · Clinic patient picker empty CLOSED — stale block (was `blocked-on bootstrap staleness`); fix shipped Lattice-side, live re-verified: fresh install + CreatePatient + staff-wildcard read now returns it
- 2026-07-03 · `3e05e2f` · Clinic patient/provider self-service reads CLOSED — `cap-read.clinic.{patient,provider}` GrantTable self-anchor lenses; fixes My Appointments + My Schedule + Visit Series
- 2026-07-03 · `29def5e` · Clinic identity cross-patient claim guard CLOSED — `identityKey` now globally exclusive (`identityPatientClaim` CreateOnly aspect)
- 2026-07-03 · `ce15916` · Steward continuous-improvement (doc sweep) — loftspace-ledger + clinic-ledger package READMEs, stale defect-comment fix (both demand rows still blocked-on Vault 5b)
- 2026-07-03 · `7ac8a83` · Clinic patient contact CLOSED — `clinicPatientsRead` Secure-Lens columns ([plan](../../implementation-artifacts/vault-crypto-shredding-design.md))
- 2026-07-03 · `b105cf5` · LoftSpace front-of-house unified search CLOSED — FE (grouped People/Units cards), backend was `b045497` ([design](../../implementation-artifacts/search-target-adapter-design.md))
- 2026-07-02 · `f37bb82` · Clinic booking write-path slot claims CLOSED — 15-min-grid double-book guard, `kv.Links`/`.bookingGuard` retired ([design](../../implementation-artifacts/clinic-booking-write-path-slot-claims-design.md))
- 2026-07-02 · `cc9c311` · bespoke-contracts Fire V4 CLOSED — self-amendment + ledger FE, V1-V4 all shipped ([design](../../implementation-artifacts/bespoke-contracts-executable-paper-design.md))
- 2026-07-02 · `47ba7c6` · bespoke-contracts Fire V3 — recurring monthly + prorated clauses, no rounding UDF needed ([design](../../implementation-artifacts/bespoke-contracts-executable-paper-design.md) checkpoint)
- 2026-07-02 · `e9408e7` · bespoke-contracts Fire V2 — conditioned + judgment clauses, assignTask(InspectPremises) ([design](../../implementation-artifacts/bespoke-contracts-executable-paper-design.md) checkpoint)
- 2026-07-02 · `8209e9e` · LoftSpace ledger shared-NanoID fix CLOSED — independent NanoID + guard aspect + lookup lens, mirrors clinic-ledger (749d7c2) ([design](../../implementation-artifacts/adjacency-shared-nanoid-collision-design.md))
- 2026-07-02 · `6938e51` · LoftSpace post-listing CLOSED — `AssignUnitOwner` wired into the post-listing chain, freshly posted units now visible to their landlord (both operator console + RLS boundary), verified live end-to-end
- 2026-07-02 · `749d7c2` · Clinic patient payment ledger Inc 2 CLOSED — billing-history FE; fixed a shared-NanoID Contract #1 bug in CreateAccount ([design](../../implementation-artifacts/adjacency-shared-nanoid-collision-design.md))
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
