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
| **Aspect types** (9) | `patientDemographics`, `patientBookingGuard`, `providerProfile`, `providerBookingGuard`, `providerHours`, `providerTimeOff`, `appointmentSchedule`, `appointmentStatus`, `appointmentEncounter` |
| **Links** (2) | `forPatient` (appointment → patient), `withProvider` (appointment → provider) |
| **Operations** (12) | `CreatePatient` · `TombstonePatient` · `CreateProvider` · `TombstoneProvider` · `SetProviderProfile` · `SetProviderHours` · `SetProviderTimeOff` · `CreateAppointment` · `RescheduleAppointment` · `SetAppointmentStatus` · `RecordEncounter` · `TombstoneAppointment` |
| **Projection lenses** (6) | `clinicAppointments` → `clinic-appointments` · `clinicProviders` → `clinic-providers` · `clinicPatients` → `clinic-patients` (all `nats-kv`, `full` engine) · `clinicAppointmentsRead` / `providerAppointmentsRead` / `clinicPatientsRead` (all `postgres`, `full` engine, **Protected** — Contract #6 §6.14 RLS, D1.5: patient-self / provider-self / staff-wildcard-only) |

Every op is granted to the `operator` role at `scope: any` (`permissions.go`) — no new capability
surface; the trusted-tool operator already holds standing permission, identical to `loftspace-domain`.

## Key shapes (Contract #1)

```
vtx.patient.<id>      class=patient      root {}   .demographics {fullName, dob?, email?, phone?}
                                                   .bookingGuard  {epoch:int}   (the per-patient OCC serialization scalar)
vtx.provider.<id>     class=provider     root {}   .profile  {fullName, specialty, credentials?, bio?}
                                                   .bookingGuard {epoch:int}    (the per-provider OCC serialization scalar)
                                                   .hours    {windows:[{day 0-6 (Sun=0), openSec, closeSec}]}   (opt-in)
                                                   .timeOff  {ranges:[{from, to, reason?}]}                      (opt-in)
vtx.appointment.<id>  class=appointment  root {}   .schedule {startsAt, endsAt, remindAt, reason?}
                                                   .status   {value ∈ scheduled|confirmed|checkedIn|completed|cancelled|noShow, note?}
                                                   .encounter {summary, assessment?, plan? (RAW PHI, never projected);
                                                               documentedAt, followUpRequested, followUpDate? (operational, projected)}

lnk.appointment.<id>.forPatient.patient.<id>      (appointment → patient — the later-arriving vertex is the source, §1.1)
lnk.appointment.<id>.withProvider.provider.<id>   (appointment → provider)
lnk.provider.<id>.hasBooking.appointment.<id>     (provider → appointment — HUB-sourced, so lnk.provider.<id>.hasBooking.> is a bounded enumeration prefix)
lnk.patient.<id>.hasBooking.appointment.<id>      (patient → appointment — hub-sourced)
```

Sentences: "appointment forPatient patient", "appointment withProvider provider", "provider hasBooking
appointment", "patient hasBooking appointment". The forPatient/withProvider link keys are deterministic
(`CreateOnly`), so the schedule guards and reschedule re-read them by key. The booking **topology** lives
in the hub-sourced `hasBooking` links (enumerated at write time via `kv.Links`, Contract #2 §2.5.1 — a
bounded prefix); the `.bookingGuard` scalar is **only** the OCC serialization lock (the Contract #1-clean
split: topology→links, lock→scalar epoch). Authoring a `hasBooking` link with the **hub as source** is the
sanctioned §2.5.1 directional choice that keeps the enumeration bounded by the hub's degree.

Root data is minimal (D5: `{}` on every root); all business data lives in aspects, all relationships
in links. Instants are normalized to **canonical whole-second UTC** (`time.rfc3339_utc`, a pure
builtin — no clock read) so RFC3339 strings compare lexically == chronologically, which is what the
half-open overlap tests and the convergence lens's `remindAt` compare rely on.

## Operations

### Patient

- **`CreatePatient`** — `{fullName, dob?, email?, phone?, patientId?}`. Mints `vtx.patient.<id>` +
  `.demographics` + a **`.bookingGuard {epoch:0}`** (initialized so the declared key is always present —
  see *Conflict detection* below). Returns `primaryKey`.
- **`TombstonePatient`** — `{patientKey}`. Soft-deletes the patient **root only** (no cascade — see
  *Tombstone semantics*).

### Provider

- **`CreateProvider`** — `{fullName, specialty, credentials?, bio?, providerId?}`. Mints
  `vtx.provider.<id>` + `.profile` + a **`.bookingGuard {epoch:0}`**. Returns `primaryKey`.
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

### Appointment

- **`CreateAppointment`** — `{patient, provider, startsAt, endsAt, reason?, appointmentId?}`. Validates
  both endpoints alive + correctly typed, runs the full guard chain (below), then mints
  `vtx.appointment.<id>` + `.schedule` (with a precomputed `remindAt = startsAt − 24h`) +
  `.status{scheduled}` + the forPatient/withProvider links + the two hub-sourced `hasBooking` links, and
  bumps both `.bookingGuard` epochs OCC-guarded.
  **The caller must declare `<provider>.bookingGuard` and `<patient>.bookingGuard` in `contextHint.reads`** —
  the OCC serialization points; a declared read of an absent key is a fatal `HydrationMiss`.
- **`RescheduleAppointment`** — `{appointmentKey, provider, patient, startsAt, endsAt, reason?}`. Rewrites
  the `.schedule` with new times (re-deriving `remindAt` so the `@at` reminder re-arms). `provider` and
  `patient` are **required and validated** to be the appointment's actual endpoints (via the
  deterministic link keys) so the move is conflict-checked against the right books — without them a move
  could silently land in an occupied slot, bypassing the double-book defense. Same `contextHint.reads`
  requirement as create.
- **`SetAppointmentStatus`** — `{appointmentKey, status, note?}`. Upserts `.status`. `status` ∈
  `scheduled|confirmed|checkedIn|completed|cancelled|noShow`. `note` is an optional audit reason
  (cancel / no-show, for billing + records), distinct from the `.schedule` visit `reason`; an omitted
  note clears any prior one (the note belongs to the transition it was recorded with).
- **`RecordEncounter`** — `{appointmentKey, summary, assessment?, plan?, followUpRequested?, followUpDate?}`.
  Upserts `.encounter` — the post-visit clinical record. `summary`/`assessment`/`plan` are RAW PHI, captured
  plaintext-for-now under the trusted-tool posture and **never projected** into a read model (the deferred
  Vault plane owns clinical-content display). `documentedAt` (server-stamped) and the follow-up fields are
  OPERATIONAL, non-PHI signals — `clinicAppointments` / `clinicAppointmentsRead` / `providerAppointmentsRead`
  project them (documentation presence + follow-up scheduling), never the clinical content itself.
- **`TombstoneAppointment`** — `{appointmentKey}`. Soft-deletes the appointment root only.

## Conflict detection & availability (Capability-KV §06 — "the operation's own Starlark logic")

`CreateAppointment` and `RescheduleAppointment` enforce four guards at op time, in order, before any
mutation. Capability-KV §06 (FROZEN) explicitly defers temporal availability and double-book rejection
to "a Phase 2 mechanism or the operation's own Starlark logic" — these guards are that logic. The double-
book guards enumerate the hub's `hasBooking` links via the **one sanctioned bounded enumeration**
`kv.Links` (Contract #2 §2.5.1) — not a raw prefix scan: `TestPackage_NoScans` still forbids the raw scan
helpers, and every other read is a known-key `kv.Read` (§2.5).

| Guard | Rejects with | How |
|---|---|---|
| **Future** | `ScheduleInPast` | `startsAt > op.submittedAt`. A **soft** guard — `submittedAt` is caller-supplied (the host clock is intentionally not exposed to Starlark), appropriate to the trusted single-identity posture. Also guards `endsAt > startsAt` (`InvalidArgument`). |
| **Business hours** | `OutsideHours` | The booking `[start, end]` must sit inside **one** `.hours` window on its UTC weekday (`time.weekday`, `time.seconds_of_day` — pure builtins). Opt-in: no `.hours` ⇒ unrestricted. |
| **Time-off** | `ProviderUnavailable` | The booking's half-open `[start, end)` must not overlap any `.timeOff` blackout range — enforced **even inside** the weekly hours (a booking must satisfy both layers). Opt-in. |
| **Provider double-book** | `SlotConflict` | The provider's `hasBooking` links are enumerated (`kv.Links`, paged); each live link's target appointment vertex + status + schedule is read via `kv.Read`; a link-tombstoned, terminal (`cancelled`/`completed`/`noShow`), or vertex-tombstoned candidate is skipped and doesn't block; a still-live overlap (half-open `[start, end)`, back-to-back allowed) is a conflict. |
| **Patient double-book** | `PatientDoubleBook` | The symmetric check enumerating the patient's `hasBooking` links — catches a patient booked with **two different providers** at the same instant (a per-provider enumeration alone cannot). |

The `.bookingGuard` epochs are the **concurrency serialization points**: each is bumped under its snapshot
revision (`make_aspect_upsert_occ`), so two simultaneous bookings for the same provider (or patient) both
snapshot the epoch at the same revision and the second commit `RevisionConflict`s → re-hydrates →
re-enumerates the now-committed `hasBooking` link → catches the overlap — fail-closed, never a silent
double-book. **Bound-maintenance:** a terminal transition / `TombstoneAppointment` eagerly tombstones the
appointment's `hasBooking` links, so the guard's `isDeleted` fast-skip bounds the per-op `kv.Read` fan-out
to the **live** book. (The tombstoned link keys persist — true keyspace reclaim awaits a hard-delete
mutation verb, a separate platform follow-on; live correctness does not depend on it.)

## Projection lenses (P5 — the only application query surface)

A clinic FE reads these projected read models, **never Core KV** (lattice-architecture.md P5). All six
are flat (no `WITH`/aggregation) `full`-engine projections. The first three are unprotected NATS-KV; the
next three (below) are the RLS-protected Postgres equivalents a real deployment's FE should read instead.

- **`clinicAppointments`** → `clinic-appointments`. One row per appointment (keyed by the appointment
  key), joined `OPTIONAL` to patient + provider — `0..1 × 0..1 = 1`, the §10.2 one-row-per-anchor
  shape (the op writes exactly one of each link). Projects schedule, status (+ `statusNote`),
  `patientKey`/`providerKey` (for client-side "my appointments" / "provider schedule" scoping),
  `patientName`/`providerName`/`providerSpecialty`, and `reminderSentAt` — a **null-safe soft read** of
  the appointment's `.reminder` aspect written by the sibling `clinic-reminders` package (null until a
  reminder fires, and null whenever clinic-reminders is not installed — a surfacing, never a build
  dependency).
- **`clinicProviders`** → `clinic-providers`. The human-readable roster / booking picker — one row per
  **named** provider (`WHERE profile.fullName <> null`). Projects name / specialty / credentials / bio
  (so the editor can read-modify-write the full profile) plus the `timeOff` and `hours` arrays verbatim
  (non-scalar JSON columns) so the booking UI can compute open slots and the managers can
  read-modify-write the current ranges/windows. The op stays the authority; this is the display surface.
- **`clinicPatients`** → `clinic-patients`. The patient-context switcher — one row per **named** patient.
  **NAME ONLY**: DOB / email / phone are PHI the deferred Vault plane owns and are deliberately **not**
  projected into a read model.

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
- **`clinicPatientsRead`** → `read_clinic_patients`. Clinic-wide patient-context switcher, **staff-wildcard-only**
  (no per-patient self-anchor — the whole roster has no single-row owner). NAME ONLY, same PHI discipline as
  `clinicPatients`.

## Reminders, recurring schedules, and the sibling package

`CreateAppointment` precomputes `remindAt = startsAt − 24h` on the `.schedule` aspect. The sibling
**`clinic-reminders`** package owns the appointment-reminder convergence lens + its `directOp`
playbook (it reads this `remindAt`, projects it as `freshUntil` so the `@at` temporal lane fires a
reminder ~24h ahead, and writes back the `.reminder.sentAt` that `clinicAppointments` surfaces).
clinic-domain itself stays **projection-only** — it owns no Weaver convergence lens.

One-shot `@at` reminders are built; **recurring `@every`** availability / follow-ups are *not* (`@every`
has no consumer; §10.4 ships `@at` one-shot) — that remains a deferred platform item this vertical forces.

## Out of scope (the deferred items this vertical forces, not implements)

- **PHI / Vault / crypto-shred.** All aspects here are non-sensitive and stored plain under the
  trusted-tool posture (a `patient` is not an identity vertex, so step-6's `sensitiveAspectScope` would
  forbid a sensitive aspect on it anyway). Real PHI handling + right-to-be-forgotten is the deferred
  **Vault plane** — clinic is its forcing function (patient-record deletion is its validating flow).
  This is why the lenses project patient **name only**, and why `RecordEncounter`'s `.encounter` aspect
  captures the clinical record (summary/assessment/plan) but never projects it into any lens — only the
  operational documentation/follow-up presence signals are projected; clinical-note **display** stays
  gated on Vault.
- **Cascade-on-tombstone.** `Tombstone{Patient,Provider,Appointment}` soft-deletes the named vertex
  **root only** — its aspects and incident links are left in place. The projection lenses anchor on the
  live root, so a tombstoned vertex drops from the read model and its orphaned aspects are not surfaced.
  This matches `location-domain` / `lease-signing`: there is no platform owner-tombstone-cascade trigger
  (it is the deferred GC item), so no package builds a bespoke one. (Note: a `full`-engine lens that
  keys on a *surviving* aspect can still re-project a tombstoned anchor — a known Refractor seam tracked
  in the Lattice backlog, not a package bug.)
- **Recurring `@every` scheduling** (see *Reminders* above).
