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
| **Mixed-use composition surfaces** | The "more than the sum" beats across lenses the one-liner omitted: **front-desk** unified resident context (lease + visit + open tab + booked class in one lookup, surfaced before asked) and **operations** portfolio-pulse aggregate (occupancy + service-attach-rate across packages) — views that exist only because the packages share one graph. Aggregate lenses + FE across both apps + Loupe. | Café/Wellness | FE + pkg | ★★★ | M | 📋 ready (after Wellness) |
| **Care→Wellness referral** | Post-visit, the clinic worklist offers a bookable wellness class (the clinic+wellness emergence — shared scheduling shape); a clinic→wellness handoff that opens a booking from the appointment context. | Clinic/Wellness | pkg + FE | ★ | S | 📋 ready (after Wellness) |
| **Clinic patient picker doesn't scale** | Front-desk booking (`#patient` select, `app.js:340`) + `GET /api/staff/patients` (`patients.go:84`) both return/render the FULL unfiltered roster with no name-filter/search — every booking starts by scrolling a raw `<select>` of every patient the clinic has ever seen. Fine at demo scale, a real front-desk workflow blocker past a few dozen patients. Add a name-ILIKE query param to `queryPatients` + a debounced-input/typeahead replacing the plain select (same pattern, lower urgency, for `#provider`). | Clinic | pkg + FE | ★★ | S | 📋 ready |
| **Clinical notes are write-only** | `RecordEncounter` PHI (`ddls.go:333-336`) captured, never projected. The cited `clinicPatientsRead` Secure-Lens precedent does NOT extend — that decrypts identity-anchored Vault ciphertext; this is raw plaintext on a non-identity vertex, and that exact shortcut was already REJECTED pre-Vault (`vault-crypto-shredding-design.md` ratification decision #2). | Clinic | pkg | ★★★ | M | 🚧 blocked-on: Vault extended to non-identity content (architectural fork, Andrew) |
| **Billing is self-pay only, no payer dimension** | `clinic-ledger`'s `DebitAccount`/`CreditAccount` (append-only, lens-derived balance) has no concept of an insurance payer — every charge is implicitly self-pay. Add a bounded `billedTo: self｜insurance` + `expectedReimbursement` dimension to a debit entry (NOT real X12 837/835 claims/clearinghouse integration — that's a certified-EHR-scale undertaking, explicitly out of bounds for a reference vertical) so a clinic can at least track what it billed insurance for vs. collected. | Clinic | pkg | ★★ | M | 📋 ready |
| **No-show doesn't cost anything** | `SetAppointmentStatus(status=noShow)` is purely a status flip — no consequence. `clinic-ledger`'s `DebitAccount` + `clinic-reminders`' Weaver gap-remediation pattern (`missing_reminder` → `directOp`) are both already shipped; a `noShow-no-fee-charged` gap closed the same way (`directOp DebitAccount`) auto-protects revenue on the same mechanism reminders already use. | Clinic | pkg | ★ | S | 📋 ready |
| **Clinic is a single-location, single-specialty silo** | `location-domain` is unused by `clinic-domain` (explicit in its own docs, unlike `loftspace-domain`); a provider has exactly one `specialty` and no site. A real multi-site practice group needs provider↔location + per-location scheduling — mirror `loftspace-domain`'s already-proven `location-domain` integration pattern. Bigger structural lift; sequence after the other Clinic items land. | Clinic | pkg | ★★ | L | 📋 ready |
| **Self-service identity creation never claims — consumer ops permanently denied** | LoftSpace `app.js:584` + Clinic `app.js:498` "new applicant/patient" flows call `CreateUnclaimedIdentity` only, never `ClaimIdentity` — every self-created identity stays unclaimed, no `holdsRole→consumer`, so `CreateLeaseApplication`/clinic self-book always 403 `AuthDenied`. Repro'd live on a fresh identity. Fix: call `ClaimIdentity` right after `CreateUnclaimedIdentity`. | Cross-vertical | FE | ★★★ | S | 📋 ready |

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
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic ↔ Café, staggered from the Steward. **Wellness
joins once `cmd/wellness-app` (Inc 2) ships** — today it has a package but no app to exercise; see
[agents/vertical-po/SKILL.md](../../../agents/vertical-po/SKILL.md) §1.

- **Rotation to date:** LoftSpace ×12, Clinic ×9, Café ×1 (2026-07-09: first live exercise — found Weaver tab-settlement posting fails closed on the shared stack (platform bug, blocked-on lattice.md) + no payment-collection UI).
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799/:7801), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. All three apps exist + are exercisable live (`:7788` / `:7799` / `:7801`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **2026-07-06:** Enriched Café+Wellness → 4 grounded, sequenced rows (Café first) + verified no platform block; spec = the go-live composition demo.
- **2026-07-09:** LoftSpace — exercised Browse&Apply live; found + root-caused self-service identity never claims (blocks CreateLeaseApplication for every applicant); filed.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-07-10 · `—` · Read-posture sweep Fire 4 — clinic-domain 5 residual sites, vertical-package sweep CLOSED (0 warnings repo-wide) — [design §13](../../implementation-artifacts/script-read-posture-design.md)
- 2026-07-10 · `b5744a9` · Read-posture sweep Fire 3 — lease-signing 19/19 (scripts.go 7 + renewal_scripts.go 12), closes lease-signing entirely — [design §13](../../implementation-artifacts/script-read-posture-design.md)
- 2026-07-10 · `41e3bcf` · Read-posture sweep Fire 2 — wellness+loftspace 13/44 + hard case 4 — [design §13](../../implementation-artifacts/script-read-posture-design.md)
- 2026-07-10 · `5263c2b` · Read-posture sweep Fire 1 — Gateway optionalReads wiring + clinic-domain 8/44 — [design §13](../../implementation-artifacts/script-read-posture-design.md)
- 2026-07-09 · `441ad1c` · semantic-contracts rename (was `bespoke-contracts`) — package identity + README shipped-status sync — [design](../../implementation-artifacts/semantic-contracts-executable-paper-design.md)
- 2026-07-09 · `1b47e0a` · Clinic reminders notification CLOSED — real `FakeNotification` bridge adapter wired, no Loom pattern needed — [design](../../implementation-artifacts/clinic-reminders-notification-adapter-design.md)
- 2026-07-09 · `ff748ef` · Café payment-collection UI CLOSED — resident-view "Record Payment" form wired to `CreditAccount`, live-verified (balance $35.50→$25.50)
- 2026-07-09 · `—` · Café tab settlement regression CLOSED — re-verified live post-`659c635`; all tabs now `posted:true` — [design](../../implementation-artifacts/cafe-ledger-design.md)
- 2026-07-09 · `86212c9` · Clinic patient self-service booking CLOSED — `cmd/clinic-app` self-book FE, live-verified — [design](../../implementation-artifacts/clinic-patient-self-service-booking-design.md)
- 2026-07-09 · `a7f5b52` · Wellness vertical CLOSED (Inc 1+2 — `wellness-domain` + `cmd/wellness-app` thin FE); live lens reads verified on :7802 — [design](../../implementation-artifacts/wellness-vertical-design.md)
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
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
