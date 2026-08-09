# clinic-domain

The bookable foundation of the **clinic vertical** — a self-contained Capability Package that owns
three vertex types (patient · provider · appointment), their aspects and links, and the projection
lenses a clinic FE reads. It is the demand-driver vertical: it forces the deferred platform planes
(`@every` recurring schedules, the Vault / crypto-shred PHI plane) into existence without
implementing them itself.

Unlike `loftspace-domain` (which decorates `location-domain`'s units with aspects), clinic-domain is
**self-contained** — no package dependency — mirroring `location-domain`'s "own your domain's vertex
types" precedent.

Install: `lattice-pkg install packages/clinic-domain` (or `make install-clinic` onto a running stack).
Design: [`_bmad-output/implementation-artifacts/clinic-domain-design.md`](../../_bmad-output/implementation-artifacts/clinic-domain-design.md).

## Inventory

| Kind | Canonical names |
|---|---|
| **Vertex types** (3) | `patient`, `provider`, `appointment` |
| **Aspect types** (12) | `patientDemographics`, `providerProfile`, `providerHours`, `providerTimeOff`, `providerSlotClaim`, `patientSlotClaim`, `appointmentSchedule`, `appointmentStatus`, `appointmentEncounter`, `identityPatientClaim` (guard, on the linked identity), `providerIdentityClaim` (guard, on the provider), `identityProviderClaim` (guard, on the linked identity) |
| **Links** (3) | `forPatient` (appointment → patient), `withProvider` (appointment → provider), `identifiedBy` (patient/provider → identity, optional — links a pre-minted `vtx.identity` carrying sensitive contact) |
| **Operations** (13) | `CreatePatient` · `TombstonePatient` · `CreateProvider` · `TombstoneProvider` · `SetProviderProfile` · `SetProviderHours` · `SetProviderTimeOff` · `BindProviderIdentity` · `CreateAppointment` · `RescheduleAppointment` · `SetAppointmentStatus` · `RecordEncounter` · `TombstoneAppointment` |
| **Projection lenses** (6) | `clinicAppointments` → `clinic-appointments` · `clinicProviders` → `clinic-providers` · `clinicPatients` → `clinic-patients` (all `nats-kv`, `full` engine) · `clinicAppointmentsRead` / `providerAppointmentsRead` / `clinicPatientsRead` (all `postgres`, `full` engine, **Protected** — Contract #6 §6.14 RLS, D1.5: patient-self / provider-self / patient-self-plus-workplace-plus-staff-wildcard) |

Every op is granted to the `operator` role at `scope: any` (`permissions.go`) — no new capability
surface; the trusted-tool operator already holds standing permission, identical to `loftspace-domain`.

## Key shapes (Contract #1)

```
vtx.patient.<id>      class=patient      root {}   .demographics {fullName}   (NO contact PII — see identifiedBy below)
vtx.provider.<id>     class=provider     root {}   .profile  {fullName, specialty, credentials?, bio?}
                                                   .hours    {windows:[{day 0-6 (Sun=0), openSec, closeSec}]}   (opt-in)
                                                   .timeOff  {ranges:[{from, to, reason?}]}                      (opt-in)
                                                   .slot<cellcode> {}   (class providerSlotClaim — one per occupied 15-min cell, on demand)
vtx.appointment.<id>  class=appointment  root {}   .schedule {startsAt, endsAt, remindAt, reason?}
                                                   .status   {value ∈ scheduled|confirmed|checkedIn|completed|cancelled|noShow, note?}
                                                   .encounter      {summary, assessment, plan}   (SENSITIVE — encrypted, DEK on the
                                                                    clinicalRecord retention class; read back only through
                                                                    clinicEncountersRead, decrypted at projection)
                                                   .documentation  {documentedAt, followUpRequested, followUpDate?}  (operational, projected)

lnk.appointment.<id>.forPatient.patient.<id>      (appointment → patient — the later-arriving vertex is the source, §1.1)
lnk.appointment.<id>.withProvider.provider.<id>   (appointment → provider)
lnk.patient.<id>.identifiedBy.identity.<id>       (patient → identity — optional, wired by CreatePatient's identityKey)
```

The patient hub carries the symmetric `.slot<cellcode>` claim aspect too (class `patientSlotClaim`,
same on-demand shape, omitted above only to avoid repeating the row).

Sentences: "appointment forPatient patient", "appointment withProvider provider", "patient identifiedBy
identity". The forPatient/withProvider link keys are deterministic (`CreateOnly`), so the schedule guards
and reschedule re-read them by key. The booking **double-book constraint** is a write-path claim, not a
read-time enumeration: `CreateAppointment`/`RescheduleAppointment` discretize `[startsAt, endsAt)` into
its covered 15-minute grid cells and claim a deterministic `.slot<cellcode>` aspect per cell on **both**
the provider and patient hub (`<cellcode>` = the cell's canonical whole-second UTC start with `-`/`:`
stripped, lowercased — e.g. `2026-07-03T09:00:00Z` → `slot20260703t090000z`). The key itself — identical
across two competing bookings for the same cell — is the lock: `CreateOnly`/`expectedRevision`
conditioning at commit rejects the loser (`RevisionConflict` → re-hydrate → re-read → fail closed), so
there is no serialization epoch and no bounded-prefix enumeration to maintain
(`clinic-booking-write-path-slot-claims-design.md`).

Root data is minimal (D5: `{}` on every root); all business data lives in aspects, all relationships
in links. Instants are normalized to **canonical whole-second UTC** (`time.rfc3339_utc`, a pure
builtin — no clock read) so RFC3339 strings compare lexically == chronologically, which is what the
half-open overlap tests and the convergence lens's `remindAt` compare rely on.

## Operations

### Patient

- **`CreatePatient`** — `{fullName, identityKey?, patientId?}`. Mints `vtx.patient.<id>` +
  `.demographics {fullName}`. No slot-claim init needed — a `.slot<cellcode>` claim is created on demand
  per booking (see *Conflict detection* below), never pre-seeded. An optional `identityKey` (validated
  alive + class=identity, via `contextHint.reads`) wires the `identifiedBy` link to a pre-minted
  `vtx.identity` carrying the patient's sensitive contact — no contact PII lives on `.demographics`
  itself, and claims a `CreateOnly` `.patientClaim` guard aspect (class `identityPatientClaim`) on the
  linked identity — a global existence marker rejecting a **second, different** patient from ever
  claiming the same identity. Returns `primaryKey`.
- **`TombstonePatient`** — `{patientKey}`. Soft-deletes the patient **root only** (no cascade — see
  *Tombstone semantics*).

### Provider

- **`CreateProvider`** — `{fullName, specialty, credentials?, bio?, providerId?}`. Mints
  `vtx.provider.<id>` + `.profile`. Returns `primaryKey`.
- **`SetProviderProfile`** — `{providerKey, fullName, specialty, credentials?, bio?}`. Full-replace
  upsert of the whole `.profile` (the editor seeds the form from `clinicProviders`, which projects
  every editable field). `fullName` + `specialty` stay required so the provider never drops out of the
  roster lens (`WHERE fullName <> null`) or the booking picker.
- **`SetProviderHours`** — `{providerKey, windows:[{day 0-6, openSec 0-86400, closeSec}]}`. Upserts the
  **opt-in** weekly-recurring availability `.hours` (UTC seconds-of-day; `openSec < closeSec`). Pass
  `windows:[]` to clear. Not OCC-guarded — hours are config, not a concurrency point.
- **`SetProviderTimeOff`** — `{providerKey, ranges:[{from, to, reason?}]}`. Upserts the **opt-in**
  date-specific blackout `.timeOff` (RFC3339 UTC, `from < to`, normalized to canonical UTC). The
  exception layer *on top of* `.hours`. Pass `ranges:[]` to clear.
- **`TombstoneProvider`** — `{providerKey}`. Soft-deletes the provider root only.
- **`BindProviderIdentity`** — `{providerKey, identityKey}`. Binds a provider to its login identity: both
  endpoints validated alive + typed, mints the `identifiedBy` link (provider → identity), claims a
  `CreateOnly` guard aspect on **each** side (`.identityClaim` — class `providerIdentityClaim` — on the
  provider; `.providerClaim` — class `identityProviderClaim` — on the identity) so the bind is mutually
  exclusive (a second bind on either side is rejected), and idempotently grants the identity the
  `provider` role via `holdsRole`. Returns `primaryKey` (the `identifiedBy` link key).

### Appointment

- **`CreateAppointment`** — `{patient, provider, startsAt, endsAt, reason?, appointmentId?}`. Validates
  both endpoints alive + correctly typed, runs the full guard chain (below), then mints
  `vtx.appointment.<id>` + `.schedule` (with a precomputed `remindAt = startsAt − 24h`) +
  `.status{scheduled}` + the forPatient/withProvider links, and claims a `.slot<cellcode>` aspect per
  covered 15-minute cell on both the provider and patient hub. No `contextHint.reads` declaration is
  needed for the slot claims — each cell's `kv.Read` is **lazy** (§2.5): it only picks the mutation verb
  (create / CAS-revive / reject); the safety property is the atomic batch's `CreateOnly`/`expectedRevision`
  conditioning at commit, not the read. Also accepts an optional `leaseAppKey` (mirrors
  `wellness-domain`'s `CreateBooking` resident-rate check): when the leaseapp is alive, carries a
  `.tenancy` aspect, and its applicant identity matches the patient's own `identifiedBy` identity, a
  `residentVisit` link (appointment → leaseapp) is written — a mismatch or absent lease falls through
  **silently**, never a hard failure. Also accepts an optional `site` (`vtx.building.<NanoID>`, a
  `location-domain` building carrying a clinic-domain `.site` profile): when supplied, the building must
  be alive + `class=location` **and** the provider must `practicesAt` it (the `clinicSiteAssignment`
  link) — a wrong-class building or a provider not assigned to that site is **REJECTED**
  (`UnknownSite`/`NotALocation`/`ProviderNotAtSite`; unlike `leaseAppKey` this is a hard requirement once
  supplied, not a silent fall-through), and an `atSite` link (appointment → building) is written. Omitted
  `site` records no site.
- **`RescheduleAppointment`** — `{appointmentKey, provider, patient, startsAt, endsAt, reason?}`. Rewrites
  the `.schedule` with new times (re-deriving `remindAt` so the `@at` reminder re-arms). `provider` and
  `patient` are **required and validated** to be the appointment's actual endpoints (via the
  deterministic link keys) so the move is conflict-checked against the right books — without them a move
  could silently land in an occupied slot, bypassing the double-book defense. Releases the grid cells the
  appointment no longer needs and claims the newly-covered ones in the same atomic batch — a rejected
  claim leaves the original booking's cells fully intact.
- **`SetAppointmentStatus`** — `{appointmentKey, status, note?, noShowFeeCents?}`. Upserts `.status`.
  `status` ∈ `scheduled|confirmed|checkedIn|completed|cancelled|noShow`. `note` is an optional audit
  reason (cancel / no-show, for billing + records), distinct from the `.schedule` visit `reason`; an
  omitted note clears any prior one (the note belongs to the transition it was recorded with).
  Transitioning to `noShow` also stores a `noShowFeeCents` amount on `.status` — caller-supplied
  (must be `> 0`) or a **2500** default when omitted — which `clinic-ledger`'s `clinicNoShowSettlement`
  lens reads to post a `DebitAccount` charge. The **terminal** statuses `{cancelled, completed, noShow}`
  are **final**: re-setting the same terminal value is idempotent, but changing a terminal status to a
  *different* one is rejected (`TerminalStatus`) so a finished / cancelled visit can never silently
  revert; non-terminal statuses move freely.
- **`RecordEncounter`** — `{appointmentKey, summary, assessment?, plan?, followUpRequested?, followUpDate?}`.
  Upserts the post-visit record as two sibling aspects, split along the sensitivity boundary because step 6.5
  encrypts a whole aspect's `data` map. `.encounter` holds `summary`/`assessment`/`plan` — RAW PHI, **SENSITIVE**,
  its DEK custodied on the package's own `clinicalRecord` retention class rather than the patient's identity, and
  readable only through **`clinicEncountersRead`**, the Secure Lens that decrypts them at projection for the
  treating provider. All three keys are always written — an unfilled optional as `""` — so the plaintext shape
  matches that lens's per-field secure columns. `.documentation` holds `documentedAt` (server-stamped) and the
  follow-up fields — OPERATIONAL, non-PHI signals that `clinicAppointments` / `clinicAppointmentsRead` /
  `providerAppointmentsRead` / clinic-reminders' `followUpReminders` project (documentation presence +
  follow-up scheduling), never the clinical content itself.
- **`TombstoneAppointment`** — `{appointmentKey}`. Soft-deletes the appointment root only.

## Conflict detection & availability (Capability-KV §06 — "the operation's own Starlark logic")

`CreateAppointment` and `RescheduleAppointment` enforce five guards at op time, in order, before any
mutation. Capability-KV §06 (FROZEN) explicitly defers temporal availability and double-book rejection
to "a Phase 2 mechanism or the operation's own Starlark logic" — these guards are that logic. The clinic's
booking grid is a **mandatory 15-minute cadence** (`:00`/`:15`/`:30`/`:45`), which turns double-book
detection from a range-overlap problem into a finite set of write-path key claims — no enumeration, no
serialization epoch (see `clinic-booking-write-path-slot-claims-design.md`, which superseded an earlier
`hasBooking`-link + `.bookingGuard`-epoch design; `TestPackage_NoScans` still forbids raw prefix scans, and
every guard read here is a known-key `kv.Read`, §2.5).

| Guard | Rejects with | How |
|---|---|---|
| **Grid alignment** | `SlotGridViolation` | `startsAt`/`endsAt` must each be a canonical whole-second UTC instant landing on a 15-minute boundary. |
| **Future** | `ScheduleInPast` | `startsAt > op.submittedAt`. A **soft** guard — `submittedAt` is caller-supplied (the host clock is intentionally not exposed to Starlark), appropriate to the trusted single-identity posture. Also guards `endsAt > startsAt` (`InvalidArgument`); span capped at 96 cells / 24h (`AppointmentTooLong`). |
| **Business hours** | `OutsideHours` | The booking `[start, end]` must sit inside **one** `.hours` window on its UTC weekday (`time.weekday`, `time.seconds_of_day` — pure builtins). Opt-in: no `.hours` ⇒ unrestricted. |
| **Time-off** | `ProviderUnavailable` | The booking's half-open `[start, end)` must not overlap any `.timeOff` blackout range — enforced **even inside** the weekly hours (a booking must satisfy both layers). Opt-in. |
| **Provider double-book** | `SlotConflict` | `[startsAt, endsAt)` is discretized into its covered 15-minute cells; each cell claims `vtx.provider.<id>.slot<cellcode>` (`CreateOnly`/CAS-revive if the prior claim there is tombstoned) — a still-live claim on any covered cell is a conflict. |
| **Patient double-book** | `PatientDoubleBook` | The symmetric claim against `vtx.patient.<id>.slot<cellcode>` — catches a patient booked with **two different providers** at the same instant (a per-provider claim set alone cannot). |

The write-path claim **is** the lock: two concurrent `CreateAppointment`s for the same provider+cell both
observe the cell absent, both attempt `CreateOnly` on the same key, and the commit accepts exactly one —
the loser's whole batch rejects `RevisionConflict`, the Processor retries, and the retry's `kv.Read` now
sees the winner's live claim → fails closed with `SlotConflict`/`PatientDoubleBook`. There is no
serialization epoch to bump and nothing to enumerate. `RescheduleAppointment` releases the cells the
appointment no longer needs and claims the newly-covered ones in the **same atomic batch**, so a rejected
move leaves the original booking's claims fully intact. `SetAppointmentStatus`'s terminal transitions
(`cancelled`/`completed`/`noShow`) and `TombstoneAppointment` release all cells the appointment held, by
recomputing the cell set from `.schedule` + the deterministic `forPatient`/`withProvider` link keys (three
point reads, not an enumeration) — freeing them for a later booking.

## Projection lenses (P5 — the only application query surface)

A clinic FE reads these projected read models, **never Core KV** (lattice-architecture.md P5). All six
are flat (no `WITH`/aggregation) `full`-engine projections. The first three are unprotected NATS-KV; the
next three (below) are the RLS-protected Postgres equivalents a real deployment's FE should read instead.

- **`clinicAppointments`** → `clinic-appointments`. One row per appointment (keyed by the appointment
  key), joined `OPTIONAL` to patient + provider — `0..1 × 0..1 = 1`, the §10.2 one-row-per-anchor
  shape (the op writes exactly one of each link). Projects schedule, status (+ `statusNote`),
  `patientKey`/`providerKey` (for client-side "my appointments" / "provider schedule" scoping by opaque
  key), `providerName`/`providerSpecialty` (the deliberately-public provider directory), and
  `reminderSentAt` — a **null-safe soft read** of the appointment's `.reminder` aspect written by the
  sibling `clinic-reminders` package (null until a reminder fires, and null whenever clinic-reminders is
  not installed — a surfacing, never a build dependency). The patient's **name is not projected here** —
  it is PHI and lives only in the Protected `clinicAppointmentsRead` / `clinicPatientsRead` lenses.
- **`clinicProviders`** → `clinic-providers`. The human-readable roster / booking picker — one row per
  **named** provider (`WHERE profile.fullName <> null`). Projects name / specialty / credentials / bio
  (so the editor can read-modify-write the full profile) plus the `timeOff` and `hours` arrays verbatim
  (non-scalar JSON columns) so the booking UI can compute open slots and the managers can
  read-modify-write the current ranges/windows. The op stays the authority; this is the display surface.
- **`clinicPatients`** → `clinic-patients`. One row per **named** patient, projected by **opaque key
  only**. This open (unauthenticated) roster carries patient keys for key-based scoping; a patient's
  **name is PHI** — the fact a named person is a patient here is itself a disclosure — and is projected
  ONLY into the Protected, RLS-scoped `clinicPatientsRead` lens (staff-anchored). DOB / email / phone
  likewise never enter an open read model.

### Protected read models (D1.5, Contract #6 §6.14 RLS)

Three more lenses project the SAME data through a **Postgres, RLS-enforced** read model instead of the
unprotected NATS-KV buckets above — closing the "any caller can pass `?patient=<any patient>`" vector the
unprotected lenses left open. Each row's `authz_anchors` set is a bare-NanoID match token; a reading actor's
JWT-derived grants must intersect it or the row simply does not appear (fail-closed, RLS-enforced, not an
app-layer filter).

- **`clinicAppointmentsRead`** → `read_clinic_appointments`. **Patient-self** audience (`cmd/clinic-app`'s
  `handleMyAppointments`) — `forPatient` is a REQUIRED anchor walk, so an appointment with no patient link
  projects no row. Also read by clinic-wide staff views via the reserved wildcard grant (no separate staff
  projection needed).
- **`providerAppointmentsRead`** → `read_provider_appointments`. **Provider-self** audience ("My Schedule") —
  `withProvider` is the REQUIRED anchor walk, mirroring `clinicAppointmentsRead`.
- **`clinicPatientsRead`** → `read_clinic_patients`. Clinic-wide patient-context switcher. Each row anchors on
  its own patient NanoID (its self-anchor — the whole ROSTER still has no single-row owner) plus every
  workplace building a provider practises at, for a provider this patient has an appointment with — mirroring
  `clinicAppointmentsRead`'s own `practicesAt` anchor one hop further out. A worksAt-anchored front-desk actor
  therefore reads every patient touching its building via service-location's `staffReadGrants`
  (`cap-read.staff`), a WildcardAnchor holder reads the whole roster, and a patient reads only their own row
  (`patientIdentityReadGrants`). `email`/`phone` are **Secure-Lens** columns (Contract #3 §3.10, Vault Fire 5,
  mirroring `landlordLeaseApplicationsRead`): decrypted at projection from the patient's optional
  `identifiedBy` identity, null for a patient with no linked identity or a shredded one — display enrichment
  only, never a row gate.

## Reminders, recurring schedules, and the sibling package

`CreateAppointment` precomputes `remindAt = startsAt − 24h` on the `.schedule` aspect. The sibling
**`clinic-reminders`** package owns the appointment-reminder convergence lens + its `directOp`
playbook (it reads this `remindAt`, projects it as `freshUntil` so the `@at` temporal lane fires a
reminder ~24h ahead, and writes back the `.reminder.sentAt` that `clinicAppointments` surfaces).
clinic-domain itself stays **projection-only** — it owns no Weaver convergence lens.

One-shot `@at` reminders are built; **recurring `@every`** availability / follow-ups are *not* (`@every`
has no consumer; §10.4 ships `@at` one-shot) — that remains a deferred platform item this vertical forces.

## Out of scope (the deferred items this vertical forces, not implements)

- **PHI / Vault / crypto-shred.** All aspects directly on `patient`/`provider`/`appointment` are
  non-sensitive and stored plain under the trusted-tool posture (none of these is an identity vertex, so
  step-6's `sensitiveAspectScope` would forbid a sensitive aspect there anyway). The open
  `clinicPatients`/`clinicAppointments` lenses project the patient by **opaque key only** — a patient's
  name is PHI (a named person being a patient here is itself a disclosure) and is projected solely into
  the Protected, RLS-scoped `clinicPatientsRead` / `clinicAppointmentsRead` lenses. Sensitive **contact** (email/phone)
  lives on the linked `identifiedBy` identity instead, and its display + right-to-be-forgotten are fully
  wired via the Vault plane (Fire 5's Secure-Lens `clinicPatientsRead` columns + the platform's
  `ShredIdentityKey`) — clinic was that plane's forcing function. The **clinical record**
  (`RecordEncounter`'s `.encounter` aspect — summary/assessment/plan) is encrypted at rest under a
  *retention-class* holder, not the patient's identity: its obligation outlives any one patient's erasure, so
  `ShredIdentityKey` leaves it readable and the operator-driven `ShredRetentionClassKey` destroys it. It is
  projected by exactly one lens — `clinicEncountersRead`, provider-anchored, which decrypts it at projection
  for the treating provider (and a WildcardAnchor holder), never the front desk. The record's survival is not
  yet pseudonymous while `.demographics`' plaintext `fullName` outlives the shred.
- **Cascade-on-tombstone.** `Tombstone{Patient,Provider,Appointment}` soft-deletes the named vertex
  **root only** — its aspects and incident links are left in place. The projection lenses anchor on the
  live root, so a tombstoned vertex drops from the read model and its orphaned aspects are not surfaced.
  This matches `location-domain` / `lease-signing`: there is no platform owner-tombstone-cascade trigger
  (it is the deferred GC item), so no package builds a bespoke one. (Note: a `full`-engine lens that
  keys on a *surviving* aspect can still re-project a tombstoned anchor — a known Refractor seam tracked
  in the Lattice backlog, not a package bug.)
- **Recurring `@every` scheduling** (see *Reminders* above).
