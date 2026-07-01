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
| Clinic — encounter / visit documentation | `RecordEncounter` captures the post-visit clinical record; raw content stays unprojected (Vault discipline). | Clinic | pkg + FE | ★★★ | M | 🏗️ building · Inc 1 (capture op) + Inc 2 (FE) shipped; raw-content encryption → Vault (deferred) |
| LoftSpace — per-landlord RLS view as the rich decision surface (D1.5 landlord cutover) | The protected `/api/landlord/applications` RLS read shows only a scope-count banner; the rich decision view is still the trusted-all-units console (§10.2). Project signals into `landlordLeaseApplicationsRead`, retiring the console. | LoftSpace | pkg + FE | ★★ | M | 🚧 blocked-on Vault · Rec C shipped ([design](../../implementation-artifacts/loftspace-d1.5-landlord-rls-decision-surface-design.md), `9b042f9`) · readiness/console/name-display deferred |
| Clinic — patient contact (email/phone) captured but never projected | `CreatePatient` stores `.demographics.{email,phone}` but the `clinicPatients` lens projects only `name` — staff can't see contact info, and a real reminder channel has no address to send to. | Clinic | pkg + FE | ★★ | S | 🚧 blocked-on Vault — `.demographics.{email,phone}` are PHI; `clinicPatients` is name-only by test-enforced discipline (the display half of lattice [Vault](lattice.md)); not a vertical-steward call |
| LoftSpace — applicant contact (email/phone) captured but never projected to the landlord | `CreateUnclaimedIdentity` stores `.email`/`.phone`, but neither the `/api/identities` picker nor the landlord `unit-applications` disposition surfaces them — a landlord deciding on an applicant has no way to contact them. | LoftSpace | pkg + FE | ★★ | S | 🚧 blocked-on Vault — `id.{email,phone}` are `sensitive=true` aspects; same display gate as the Clinic patient-contact row → lattice [Vault](lattice.md); not a vertical-steward call |

## PO notes (dated — drives rotation)

Compact rotation memory only — PO *findings* are filed as demand rows above + the Done log; the verbose
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic, staggered from the Steward.

- **Rotation to date:** LoftSpace ×7, Clinic ×5 (last: Clinic 5th run 2026-06-30, reused the up shared stack; drove CreateProvider / CreatePatient / CreateAppointment / SetAppointmentStatus via `/api/op`; filed the status-note FE gap + a platform lens-stall observability gap).
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. Both apps exist + are exercisable live (`:7788` / `:7799`).
- **Live-stack note (2026-06-30):** the shared stack's Refractor lens projection was **stalled** — ops committed to Core KV (Processor `green`, `core-events` published) did not reach *any* clinic read model, though all components self-reported `green`/`active`. Verified via Loupe `/api/vertex` (Core KV updated) vs `/api/appointments` (frozen). Surfaced as the lattice [silent lens-projection stall](lattice.md) item; left for an owner/Lamplighter to remediate (not a PO action). New writes won't project until the Refractor is restarted. Also: Loupe health `overall=yellow` is from un-reaped dead-instance heartbeats (already filed: lattice Health-KV TTL item).
- **Next:** the staler of the two by run-date (LoftSpace).

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

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
- 2026-06-29 · `b81ffcd` · clinic-domain: `RecordEncounter` — capture the post-visit clinical record
- 2026-06-29 · `d772195` · clinic-reminders: package README (doc-only)
- 2026-06-29 · `ce57c10` · loftspace-app: D1.3 Fire 3 — authenticated RLS read boundary (applicant)
- 2026-06-29 · `446567e` · loftspace/lease-signing: D1.3 Fire 2 — protected Postgres lease read-model
- 2026-06-29 · `65e0eb3` · loftspace: aggregated "All my documents" view across an applicant's applications
- 2026-06-28 · `6e5ed75` · loftspace: landlord edit listing + take a unit off-market
- 2026-06-28 · `72cb63e` · loftspace-app: listing photos — landlord upload + applicant Browse gallery
- 2026-06-28 · `9735415` · clinic-domain: `SetProviderProfile` op + Edit-provider in the Availability tab
- 2026-06-28 · `2f9ce49` · clinic-app: move Add-provider into the Availability admin tab
- 2026-06-28 · `f8b9130` · clinic-app: dedicated Availability admin tab — hours + time-off
- 2026-06-28 · `25c0f1c` · clinic-app: seed the provider-hours editor from persisted `.hours`
- 2026-06-28 · `2686369` · clinic-app: My Appointments upcoming/past split + status filter
- *(older entries rolled to [archive/verticals-done.md](archive/verticals-done.md))*
