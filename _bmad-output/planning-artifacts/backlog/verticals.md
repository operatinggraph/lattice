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
| LoftSpace — My Applications card always blank for bedrooms/bathrooms/move-in | `read_lease_applications` has `unit_bedrooms`/`unit_bathrooms`/`unit_available_from` populated (live-verified via Postgres); `selectApplicationsSQL` in `cmd/loftspace-app/applications.go` omits all three from its SELECT, so `/api/applications` always returns them null and the FE's beds/baths/move-in line renders blank. | LoftSpace | FE | ★★ | XS | 📋 ready — add the 3 columns to `selectApplicationsSQL` + `queryApplications` Scan (mirrors `selectApplicationByKeySQL`, which already selects them) |
| LoftSpace — lease renewal (first goal-authored Weaver target) | No renewal surface exists; trigger derivable (`.listing` availableFrom+leaseTermMonths; freshUntil precedent). Per-tenant chain — fresh bgcheck if stale, guarantor re-verify if present, rent adjustment (new op), signature — author goal-first (`goal` + op `effects` + the §10.8 doctrine rider). | LoftSpace | pkg + FE | ★★★ | L | 📋 needs-design (Designer; Andrew-routed 2026-07-05) · builds WITH [lattice](lattice.md) Fire 6 Inc3 · [planner design](../../implementation-artifacts/weaver-planner-mandate-design.md) |

## PO notes (dated — drives rotation)

Compact rotation memory only — PO *findings* are filed as demand rows above + the Done log; the verbose
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic, staggered from the Steward.

- **Rotation to date:** LoftSpace ×11, Clinic ×8 (last: LoftSpace 2026-07-04, drove the full apply→sign→approve flow live end-to-end; filed a My-Applications SELECT-omission bug).
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. Both apps exist + are exercisable live (`:7788` / `:7799`).
- **Live-stack note:** a stale bootstrap JSON vs. a recreated Core KV was a recurring dev-loop trap (2026-07-03, 2026-07-04) that silently emptied reads; `make up` now self-heals it (`109f59a`, 2026-07-05) — re-verify empty-read reports as a real product bug first.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-07-01 · `9947f75` · LoftSpace tenant payment ledger Inc 2 CLOSED — payment-history FE (GET /api/ledger + Ledger panel + landlord record charge/payment)
- 2026-07-01 · `12736df` · LoftSpace tenant payment ledger Inc 1 — account/transaction vertex types (CreateAccount/Debit/CreditAccount) + ledgerHistory lens, append-only (no stored balance)
- 2026-07-01 · `—` · Clinic dev-loop D1.5 read-boundary wiring CLOSED — `provision-clinic-role` + DSN/dev-auth wired into `up-clinic`/`refresh-clinic` (mirrors `up-loftspace`); verified live, no more 500s
- 2026-07-01 · `—` · Clinic encounter/visit documentation CLOSED (stale 🏗️) — capture (`b81ffcd`) + FE (`2d5aeae`) done; encryption tracked under [Vault](lattice.md)
- 2026-07-01 · `ec82fd8` · Steward continuous-improvement (doc sweep) — loftspace-domain package README (all demand rows blocked-on Vault/D1 this fire)
- 2026-07-01 · `679fe25` · Clinic tombstone-linger row CLOSED (stale) — anchor-tombstone retraction already fixed this same-day as the PO filing
- 2026-07-01 · `9b042f9` · LoftSpace D1.5 Rec C — landlord RLS view gains the rich qualification-signal decision surface
- 2026-07-01 · `0998f02` · Clinic cancel/no-show reason-note row CLOSED (stale) — verified already shipped 2026-06-26, pre-dating the PO row
- 2026-07-01 · `30a2ec0` · Clinic recurring visit series CLOSED — Inc 2 FE (Series clinic-wide worklist tab + My Appointments start/pause/resume panel), verified end-to-end live
- 2026-07-01 · `5cf84e8` · Clinic recurring visit series Inc 1 — visitseries vertex + Start/Pause/Resume/AdvanceVisitSeries + rolling `visitSeriesDue` lens
- 2026-06-30 · `f8240cd` · Clinic — `SetAppointmentStatus` terminal-status guard (cancelled/completed/noShow final → TerminalStatus; fixes completed→scheduled revert)
- 2026-06-30 · `6674834` · LoftSpace — `DecideLeaseApplication` decision guards (recorded decision terminal → DecisionFinal; approve needs signed → NotReadyToApprove)
- 2026-06-30 · `f70ab18` · Clinic follow-ups CLOSED — Inc 2 at-the-date `@at` follow-up reminder (`followUpReminders` + `RecordFollowUpReminder` + worklist badge)
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
