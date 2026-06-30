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
| Recurring visit series (the `@every` clinic consumer) | A genuinely-recurring clinic need: a patient on a standing cadence (chronic-care monthly check-ins / weekly PT) — keep a "next visit due" worklist gap rolling forward on its own. | Clinic | pkg + FE | ★★★ | M | ✅ ratified · [design](../../implementation-artifacts/clinic-recurring-visit-series-design.md) · steward impl-ratified the package-vs-lattice fork → **package rolling-`@at` convergence series, NO new primitive** (a per-series `@every` is the wrong tool; recurring = a derived read-model projection re-arming its own `freshUntil`, the rolling generalization of the one-shot follow-up). `@every`/`ScheduleEvery` stays reserved for singleton platform cadences. Build: Inc 1 (series state + `visitSeriesDue` lens + `AdvanceVisitSeries` directOp) → Inc 2 (FE worklist + start/pause). No contract change. (steward-verticals 2026-06-30) |
| Clinic — encounter / visit documentation | `RecordEncounter` captures the post-visit clinical record; raw content stays unprojected (Vault discipline). | Clinic | pkg + FE | ★★★ | M | 🏗️ building · Inc 1 (capture op) + Inc 2 (FE) shipped; raw-content encryption → Vault (deferred) |
| LoftSpace — per-landlord RLS view as the rich decision surface (D1.5 landlord cutover) | The protected `/api/landlord/applications` RLS read renders only a scope-count banner; the rich decision view (signals + Approve/Decline) is still the trusted-all-units operator console reading `weaver-targets` (§10.2 old pattern). Project the qualification signals into `landlordLeaseApplicationsRead` + render rich RLS-scoped rows, retiring the console — mirrors the applicant-side D1.3 Fire 3 cutover. | LoftSpace | pkg + FE | ★★ | M | 📐 awaiting-Andrew · [design](../../implementation-artifacts/loftspace-d1.5-landlord-rls-decision-surface-design.md) · PO's "6 cols + wire" UNDER-scoped: a console-retiring cutover needs applicant **readiness** (the Approve-gate), which clones the convergence aggregation (a load-bearing core projection) → one architecture decision (§4). Rec **C**: ship the non-PII signal-rich RLS view NOW (zero clone), defer the readiness clone + applicant-name display + console retirement to the **Vault** milestone (shared gate). `$now`-in-protected-path verified buildable. |
| Clinic — tombstoned provider/patient/appointment LINGER in the FE | A soft-deleted clinic entity stays pickable/visible because the full-engine lens re-projects it while its keyed aspect survives. | Clinic | platform (Refractor) + FE | ★★ | S | 🚧 blocked-on lattice [full-engine tombstone retraction](lattice.md) (Read-model section) |
| Clinic — patient contact (email/phone) captured but never projected | `CreatePatient` stores `.demographics.{email,phone}` but the `clinicPatients` lens projects only `name` — staff can't see contact info, and a real reminder channel has no address to send to. | Clinic | pkg + FE | ★★ | S | 🚧 blocked-on Vault — `.demographics.{email,phone}` are PHI; `clinicPatients` is NAME-ONLY by deliberate, **test-enforced** discipline (clinic is the Vault forcing function; no lens may project a raw sensitive aspect). Surfacing them = the display half of the lattice [Vault + crypto-shredding](lattice.md) item (✅ ratified, seq behind D1). NOT 📋 ready as filed — PII-projection is not a vertical-steward call. |
| LoftSpace — applicant contact (email/phone) captured but never projected to the landlord | `CreateUnclaimedIdentity` stores `.email`/`.phone`, but neither the `/api/identities` picker nor the landlord `unit-applications` disposition surfaces them — a landlord deciding on an applicant has no way to contact them. | LoftSpace | pkg + FE | ★★ | S | 🚧 blocked-on Vault — `id.{email,phone}` are `sensitive=true` identity aspects (`identity-domain/ddls.go`); same display gate as the Clinic patient-contact row → the lattice [Vault + crypto-shredding](lattice.md) item. The only sensitive read in any lens is `id.ssn` as a *presence test*, never a projected value. NOT 📋 ready as filed. |

## PO notes (dated — drives rotation)

Compact rotation memory only — PO *findings* are filed as demand rows above + the Done log; the verbose
dated run-logs live in git history. Rotate LoftSpace ↔ Clinic, staggered from the Steward.

- **Rotation to date:** LoftSpace ×7, Clinic ×4 (last: LoftSpace 7th run 2026-06-30, full post-listing→apply→profile→decide flow on a fresh shared stack; filed the decide-guard + applicant-contact gaps).
- **Method:** reuse the already-up shared stack (detect NATS :4222 / app :7788/:7799), drive the real flow via `/api/op` + the lens projections as the product owner, file scored items. Both apps exist + are exercisable live (`:7788` / `:7799`).
- **Next:** the staler of the two by run-date (Clinic).

## Done log — verticals (newest first)

One line per shipped item (`date · SHA · title`). Oldest roll to `archive/` past ~25.

- 2026-06-30 · `f8240cd` · Clinic — `SetAppointmentStatus` terminal-status lifecycle guard (cancelled/completed/noShow are final → TerminalStatus; same-value re-set idempotent; non-terminal moves free) — fixes the verified completed→scheduled revert
- 2026-06-30 · `6674834` · LoftSpace — `DecideLeaseApplication` decision guards (a recorded decision is terminal → DecisionFinal; approve rejected → NotReadyToApprove unless signed). Scope note: the approve floor is *signed*, NOT the full lens-derived `applicantApproved` (ssn+fresh-bgcheck+payment+signature) — reproducing that cross-vertex gate in the write-path op would duplicate read-model logic + risk op↔lens divergence; the convergence lens still blocks the actual lease (residual: a signed-but-not-bg-complete app can read a cosmetic "Approved" that never leases)
- 2026-06-30 · `f70ab18` · Clinic follow-ups CLOSED — Inc 2 at-the-date `@at` follow-up reminder (clinic-reminders `followUpReminders` convergence + `RecordFollowUpReminder` + worklist 🔔 badge; real notify deferred = bridge-adapter, like appointment reminders)
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
- 2026-06-28 · `7c20752` · clinic-app: clinic-wide All-providers schedule view
- 2026-06-28 · `3704324` · lease-signing: replace the `unit.leaseApplications` key-list aspect with a guard LINK
- 2026-06-28 · `e764206` · lease-signing: capture guarantor/co-applicant detail + derived signals
- 2026-06-28 · `587da13` · loftspace-app: rank a unit's competing applicants in the landlord view
- 2026-06-28 · `1c4f94c` · loftspace-app: search/filter/sort bar over Browse listings
- 2026-06-28 · `9d7454b` · clinic-app: booking calendar greys out unbookable days
- 2026-06-28 · `c819d4c` · clinic-app: booking slot picker excludes the patient's cross-provider conflicts
- 2026-06-28 · `7e8a40e` · loftspace-app: post-a-listing modal sends required `availableFrom`
- 2026-06-27 · `82d4e4a` · loftspace-app: render op-rejection messages
- 2026-06-27 · `7b9d7f4` · clinic-app: render op-rejection messages
- 2026-06-27 · `99dd625` · Clinic: guard patient-side double-booking across providers
- 2026-06-27 · `ecad67c` · Clinic: availability-aware booking slot picker (+ name why a date has no slots)
- 2026-06-27 · `bee9533` · LoftSpace: applicant qualification profile + derived landlord signals
- 2026-06-27 · `2475beb` · LoftSpace: capture an optional reason on a landlord decline
- 2026-06-27 · `426f4eb` · LoftSpace signed-lease Inc B: produce + attach the executed lease PDF
- 2026-06-27 · `0b1dc19` · Clinic: provider time-off manager UI + booking blackout warning
- 2026-06-27 · `3c3325b` · Clinic: provider date-specific time-off exceptions
- 2026-06-27 · `04ef20e` · Clinic: reject past-dated appointment bookings
- 2026-06-27 · `db4073a` · LoftSpace landlord surface Inc 3: the landlord FE
- 2026-06-26 · `777d180` · LoftSpace signed-lease Inc A: project lease terms + terms-review panel
- 2026-06-26 · `6c30a10` · LoftSpace landlord surface Inc 2: `DecideLeaseApplication` + lens gating
