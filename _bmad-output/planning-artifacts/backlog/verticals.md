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
| LoftSpace — per-landlord RLS view as the rich decision surface (D1.5 landlord cutover) | The protected `/api/landlord/applications` RLS read shows only a scope-count banner; the rich decision view is still the trusted-all-units console (§10.2). Project signals into `landlordLeaseApplicationsRead`, retiring the console. | LoftSpace | pkg + FE | ★★ | M | 🚧 seq Vault Fire 5 (Vault 🎯 build-next in [lattice](lattice.md)) · Rec C shipped ([design](../../implementation-artifacts/loftspace-d1.5-landlord-rls-decision-surface-design.md)) · readiness clone = fallback if Vault stalls |
| Clinic — patient contact (email/phone) captured but never projected | `CreatePatient` stores `.demographics.{email,phone}` but the `clinicPatients` lens projects only `name` — staff can't see contact info, and a real reminder channel has no address to send to. | Clinic | pkg + FE | ★★ | S→M | 📋 re-model half ready NOW (patient `identifiedBy` unclaimed identity; contact → sensitive identity aspects) · display half 🚧 seq Vault Fire 5 · [plan](../../implementation-artifacts/vault-crypto-shredding-design.md) |
| LoftSpace — applicant contact (email/phone) captured but never projected to the landlord | `CreateUnclaimedIdentity` stores `.email`/`.phone`, but neither the `/api/identities` picker nor the landlord `unit-applications` disposition surfaces them — a landlord deciding on an applicant has no way to contact them. | LoftSpace | pkg + FE | ★★ | S | 🚧 seq Vault Fire 5 (Vault 🎯 build-next in [lattice](lattice.md)) — Fire-5 consumer: landlord protected lens gains contact columns ([plan](../../implementation-artifacts/vault-crypto-shredding-design.md)) |
| LoftSpace — ledger account shares the lease's own NanoID (Contract #1 violation) | `loftspace-ledger`'s `CreateAccount` mints the account under the lease's bare NanoID (mirrors clinic-ledger's pre-fix defect) — corrupts Refractor adjacency, so `ledgerHistory` silently projects 0 rows on the first real charge. Mirror clinic-ledger's fix: independent NanoID + a guard aspect on the leaseapp + a lookup lens. | LoftSpace | pkg + FE | ★★★ | M | 📋 ready · [design](../../implementation-artifacts/adjacency-shared-nanoid-collision-design.md) |

## PO notes (dated — drives rotation)

Compact rotation memory only — PO *findings* are filed as demand rows above + the Done log; the verbose
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic, staggered from the Steward.

- **Rotation to date:** LoftSpace ×9, Clinic ×6 (last: LoftSpace 9th run 2026-07-02, reused the shared stack; drove post-listing→apply→AssignUnitOwner live end-to-end; filed the post-listing missing-`manages`-link bug).
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. Both apps exist + are exercisable live (`:7788` / `:7799`).
- **Live-stack note RESOLVED (2026-07-01):** the version-13→14 bootstrap mismatch is fixed; writes confirmed working on both apps.
- **Next:** Clinic.

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-06-30 · `b96dd3d` · Clinic follow-ups Inc 1 — clinic-wide "due follow-ups" worklist (urgency groups + addressed filter + one-click Book-follow-up)
- 2026-06-30 · `—` · Applicant qualification profile CLOSED — capture op + derived signals shipped; landlord sees signals live (operator console + `renderQualification`)
- 2026-06-30 · `—` · Property/Unit/Listing domain CLOSED — Inc 1–3 all shipped (applicant FE intake+terms+leasing+tasks+docs all live)
- 2026-06-29 · `2a02df1` · D1.3 CLOSED — Postgres-RLS read boundary LIVE (revocation-denies proven)
- 2026-06-29 · `e1d540f` · service-domain + service-location: envelope-class discriminator migration
- 2026-06-29 · `2a5087a` · Service-instance envelope-class migration — lease-signing consumer (Row 112)
- 2026-06-29 · `2d5aeae` · Clinic encounter FE (Row 104 Increment 2)
- 2026-06-29 · `2a02df1` · loftspace-app: D1.3 landlord/residence audience — Increment 3 (authenticated RLS reader)
- 2026-06-29 · `e9a81fc` · lease-signing: D1.3 landlord/residence audience — Increment 2 (protected lens)
- 2026-06-29 · `5b672b1` · loftspace-domain: D1.3 landlord/residence audience — Increment 1 (ownership link)
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
