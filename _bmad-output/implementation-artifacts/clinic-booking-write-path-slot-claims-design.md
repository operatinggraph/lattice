# Clinic booking constraint — write-path per-slot claim keys (15-minute grid)

**Status: ✅ Winston-ratified — build-ready.** No frozen-contract touch (no new platform primitive —
`kv.Read`, `create`/`update`(OCC)/`tombstone` are all that's used, exactly the existing
`appliedToUnit` vocabulary). No architectural fork. Pure package-level (`clinic-domain`) Starlark
rewrite, decided per §0/§2.5 of the Steward skill (implementation-level design call, no open question
routes to Andrew).
**Owner:** clinic-domain package. **Size:** M. **Ref board row:** "Clinic — booking constraint as
write-path slot claims (15-min grid)" (★★, Clinic, `verticals.md`).

---

## 1. Why this fire exists (provenance)

The shipped double-book guard (`packages/clinic-domain/ddls.go`) enumerates a hub's (provider's /
patient's) `hasBooking` links via `kv.Links` and serializes on a `.bookingGuard` scalar epoch
(op-time-bounded-link-enumeration-design.md, "Fire 2 SHIPPED"). That build **diverged from its own
design's ratification banner**: the 2026-06-28 banner explicitly withdrew the inverted, hub-sourced
`hasBooking` link ("Drop `hasBooking`; keep the existing §1.1-correct links... enumerate them
inbound") in favor of enumerating the already-correct `withProvider`/`forPatient` links — but Fire 2
built the withdrawn shape anyway, reintroducing the exact §1.1 direction violation the banner had
struck. Andrew caught this from memory at ratification review of the follow-on hard-delete-verb design
(2026-07-02) — that design's own *demand* (bound `kv.Links`' unbounded LIST-cost growth) was itself
grounded in the violating shape, so it was a symptom of the wrong mechanism, not real platform demand.
See `hard-delete-mutation-verb-design.md` (shelved) and
`_bmad-output/implementation-artifacts/agentic-ops-swimlanes-design.md` / memory
`feedback-ratification-banner-rewrites-body`.

Rather than patch the banner divergence in place (re-author `hasBooking` inbound, keep the epoch),
**Andrew redirected the whole mechanism** (2026-07-02): move the double-book constraint off
read-time link enumeration entirely and onto **write-path deterministic-key claims**, mirroring the
`appliedToUnit` / email-uniqueness precedent that already retired the *other* clinic key-list-in-aspect
guard. The enabling product decision: **the clinic's booking grid is exactly 15 minutes** — a package
constraint, not a platform one. `op-time-bounded-link-enumeration-design.md` §6 alternative D
("discretize time into fixed slots + a deterministic guard link per slot") had been **rejected** for
being "lossy for arbitrary-duration overlapping intervals" — but that rejection rested on an
*unstated* product assumption (arbitrary-duration bookings) that Andrew is free to change, and did.
With a mandatory 15-minute grid, discretization is **lossless**: every legal appointment boundary sits
exactly on a cell edge, so no interval can straddle a cell without a covered cell being generated for
it.

This design **supersedes** the shipped Fire-2 mechanism: it drops `hasBooking` links, the
`.bookingGuard` epoch aspects, and `kv.Links`/`kv.Links`-enumeration entirely from `clinic-domain`. It
does **not** touch `kv.Links` the platform primitive (Fire 1) — it becomes an unconsumed builtin once
this ships (see §8, neighbor note).

---

## 2. The shape

### 2.1 Data model

**Dropped:** `providerBookingGuard` / `patientBookingGuard` aspect-type DDLs, the `.bookingGuard`
`{epoch:int}` aspects (and their `CreateProvider`/`CreatePatient` init), the `hasBooking` links,
`assert_no_overlap` / `bump_guard` / `tombstone_booking_links`.

**Added:** a deterministic **slot-claim aspect**, one per occupied 15-minute grid cell, on **both**
the provider and patient hub vertices (the same dual-hub symmetry the old mechanism had for
`SlotConflict` vs `PatientDoubleBook`):

```
vtx.provider.<p>.slot<cellcode> = {}   # class providerSlotClaim
vtx.patient.<pt>.slot<cellcode> = {}   # class patientSlotClaim
```

`<cellcode>` is the cell's canonical whole-second UTC start, stripped of `-`/`:` and lowercased —
e.g. `2026-07-03T09:00:00Z` → `slot20260703t090000z`. This is a **localName**, not a vertex/link
`<id>` segment — Contract #1's "deterministic readable IDs are NOT permitted in primary keys" binds
`<id>` (the NanoID segment), not `<localName>` (aspect/link names are inherently deterministic and
readable by design — `.demographics`, `.schedule`, `.bookingGuard` all already are). Grammar-legal:
`[a-z][a-zA-Z0-9]*`.

**Why an aspect, not a link.** The claim's job is "does *any* booking already hold provider `<p>`'s
09:00 cell" — the key itself must be **identical** across two different, concurrently-created
appointments competing for the same cell, so they collide at commit. A link's target segment
`<type2>.<id2>` must be a real vertex id (Contract #1 §1.1: "express every vertex→vertex
relationship as a link"), and the appointment vertex doesn't exist (and its NanoID differs per
booking) until the create commits — so two concurrent claims would target *different* link keys and
never collide. An aspect anchored on the hub (whose id is fixed and already exists) with a
**cell-derived, not appointment-derived**, localName is the only shape where the key is invariant
across competing writers. Its `data` is `{}` — a pure existence marker, **no relationship field**
(no `appointment: <key>` back-reference): storing another vertex's key in aspect `data` is exactly the
`.bookings`/`.leaseApplications` paper-over CLAUDE.md and `lattice-architecture.md:587` forbid. The
cancel/reschedule path (§2.3) **recomputes** the cell set from the appointment's own `.schedule` +
link-derived provider/patient, the same way `WithdrawLeaseApplication` recomputes the deterministic
`appliedToUnit` guard-link key from its `{unit, applicant}` payload instead of storing a back-pointer.

Two new aspect-type DDLs (`providerSlotClaim`, `patientSlotClaim`; `PermittedCommands:
[CreateAppointment, RescheduleAppointment, SetAppointmentStatus]`; non-sensitive). **No `CreateProvider`/
`CreatePatient` init needed** — unlike `.bookingGuard`, a slot-claim aspect is created on demand per
booking, never pre-seeded, so there is no "must exist before declared read" constraint and no live-stack
migration hole for vertices minted under the old package version (§6).

### 2.2 The 15-minute grid constraint (new validation, both Create and Reschedule)

```python
GRID_MINUTES_STR = ["00", "15", "30", "45"]

def enforce_grid(starts_at, ends_at):
    # Canonical whole-second UTC is fixed-width: YYYY-MM-DDTHH:MM:SSZ (20 chars).
    # Slice the minute/second fields directly rather than adding a new time
    # builtin -- the package already treats the canonical string form as the
    # stable contract (time.rfc3339_utc / rfc3339_add).
    for label, t in [("startsAt", starts_at), ("endsAt", ends_at)]:
        if len(t) != 20:
            fail("SlotGridViolation: " + label + ": must be a canonical whole-second UTC instant; got " + t)
        if t[17:19] != "00" or t[14:16] not in GRID_MINUTES_STR:
            fail("SlotGridViolation: " + label + " must align to the clinic's 15-minute booking grid (:00/:15/:30/:45); got " + t)
```

Called right after the existing `endsAt > startsAt` guard in `CreateAppointment` / `RescheduleAppointment`.
Because **both** endpoints are grid-aligned, the duration is automatically a whole multiple of 15
minutes — no separate divisibility check needed.

### 2.3 Cell enumeration (bounded, fail-closed — mirrors the existing `MAX_BOOKING_PAGES` idiom)

```python
GRID_STEP = "15m"
MAX_SLOT_CELLS = 96  # 24h of 15-minute cells -- a generous backstop, not an expected ceiling

def slot_cells(starts_at, ends_at):
    cells = []
    cur = starts_at
    for _i in range(MAX_SLOT_CELLS + 1):
        if not (cur < ends_at):
            return cells
        cells.append(cur)
        cur = time.rfc3339_add(cur, GRID_STEP)
    fail("AppointmentTooLong: appointment spans more than " + str(MAX_SLOT_CELLS) + " 15-minute slots (24h); shorten the interval")

def slot_cellcode(cell_start):
    return cell_start.replace("-", "").replace(":", "").lower()
```

A 30-minute appointment `[09:00, 09:30)` covers `{09:00, 09:15}` — 2 cells. A back-to-back next
appointment `[09:30, 10:00)` covers `{09:30, 09:45}` — no shared cell, so "back-to-back touching is
allowed" (an existing test case) holds without a special-case: the half-open interval property that
used to require an explicit overlap-compare is now structural (disjoint cell sets = no conflict, same
cell = conflict), which is *why* the grid stopped being lossy.

### 2.4 `CreateAppointment` — claim algorithm

Per hub in `[(provider, "SlotConflict"), (patient, "PatientDoubleBook")]`, for each cell in
`slot_cells(starts_at, ends_at)`:

```python
def claim_cell(hub, cellcode, cls, conflict_code, who):
    key = hub + "." + "slot" + cellcode
    existing = kv.Read(key)
    if existing != None and not existing.isDeleted:
        fail(conflict_code + ": " + who + " " + hub + " slot " + cellcode + " is already booked")
    if existing != None and existing.isDeleted:
        return make_aspect_upsert_occ(hub, "slot" + cellcode, cls, {}, existing.revision)
    return make_aspect(hub, "slot" + cellcode, cls, {})
```

`kv.Read` here is **lazy** (§2.5, the same idiom `SetAppointmentStatus`'s terminal-guard already uses
for `.status`) — it decides *which mutation verb* to emit (create / CAS-revive / reject), it is **not**
itself the safety property. The safety property is the atomic batch's `CreateOnly`/`expectedRevision`
conditioning at commit (Contract #2 §2.5's documented backstop: "a concurrent create that wins between
step 4 and step 8 is caught by the `CreateOnly` backstop (`RevisionConflict` → re-hydrate → now present
→ no-op)"). Two concurrent `CreateAppointment`s for the same provider+cell: both `kv.Read` the cell as
absent, both emit `op:create`; the atomic batch commits exactly one (`CreateOnly` on a NATS KV key
with revision 0 succeeds once); the loser's whole batch rejects with `RevisionConflict`, the Processor
retries (re-hydrates, re-runs the script), the retry's `kv.Read` now sees the winner's live cell →
`fail("SlotConflict...")`. **No serialization epoch, no enumeration, no bound-maintenance bookkeeping**
— the deterministic key *is* the lock, exactly as `appliedToUnit` already established for existence-
uniqueness; this design is the same trick generalized from one key to N (one per grid cell), which
the 15-minute-grid product decision makes lossless.

Mutations (replacing the `hasBooking` links + `bump_guard` calls):

```python
mutations = [
    make_vtx(appt_key, "appointment", {}),
    make_aspect(appt_key, "schedule", "appointmentSchedule", sched),
    make_aspect(appt_key, "status", "appointmentStatus", {"value": "scheduled"}),
    make_link(for_patient_lnk, appt_key, patient, "forPatient", "forPatient", {}),
    make_link(with_provider_lnk, appt_key, provider, "withProvider", "withProvider", {}),
] + [claim_cell(provider, slot_cellcode(c), "providerSlotClaim", "SlotConflict", "provider") for c in cells] \
  + [claim_cell(patient, slot_cellcode(c), "patientSlotClaim", "PatientDoubleBook", "patient") for c in cells]
```

(Starlark has no list comprehensions with a trailing `for`/`+` chain across statements in this form —
the actual implementation loops with `for c in cells: mutations.append(claim_cell(...))`, shown here
compactly for exposition.)

### 2.5 `RescheduleAppointment` — release-old / claim-new, deduped

Unlike Create, Reschedule must **free** the cells the appointment no longer needs and **claim** the
ones it newly needs, in the **same atomic batch** so a rejected reschedule never partially loses the
old slot. The op does not currently read the appointment's own `.schedule`; it now must (to know the
old interval):

```python
old_sched = kv.Read(appt_key + ".schedule")
old_starts = old_sched.data.get("startsAt")
old_ends = old_sched.data.get("endsAt")
old_cells = slot_cells(old_starts, old_ends)
new_cells = slot_cells(starts_at, ends_at)  # the new, already-grid-validated interval

to_release = [c for c in old_cells if c not in new_cells]
to_claim = [c for c in new_cells if c not in old_cells]
# cells present in BOTH old and new are already held -- no mutation, no re-read.
```

For each `hub, cls, conflict_code` pair: `to_release` cells become `make_tombstone(hub + ".slot" +
slot_cellcode(c))` (no read needed — they're known-live from `old_cells`, and Reschedule already reads
the appointment's `.schedule` at step 4 as a declared read, which is what conditions the tombstone's
`expectedRevision`... practically: since the release targets are the *provider/patient* cell aspects,
not the schedule aspect, an unconditioned `tombstone` — like the existing `.status`-terminal path used
for `hasBooking` links — is sufficient; a stale-tombstone race here can only ever *free* a cell a step
early, never silently keep two live claims, so it is not a correctness hole). `to_claim` cells run
through `claim_cell` exactly as Create does (reject / revive / create). If any `to_claim` cell
collides, the **whole batch** — including the `to_release` tombstones — is atomically rejected, so a
failed reschedule leaves the original booking fully intact (the property the design doc's title trades
for: "cancel tombstones claims, rebook restores").

### 2.6 `SetAppointmentStatus` (terminal transition) — release

Today this op only touches the appointment's own `.status` aspect and (old mechanism) tombstones its
two `hasBooking` links via a cheap `kv.Links(appt_key, "hasBooking", "in", ...)` walk. The new
mechanism has no such reverse index, so freeing the cells requires discovering the provider/patient +
interval directly:

```python
if status in TERMINAL_STATUSES:
    sched = kv.Read(appt_key + ".schedule")
    wp = kv.Read(with_provider_lnk_for(appt_id))   # deterministic key, recomputed
    fp = kv.Read(for_patient_lnk_for(appt_id))
    if sched != None and not sched.isDeleted and wp != None and not wp.isDeleted and fp != None and not fp.isDeleted:
        cells = slot_cells(sched.data.get("startsAt"), sched.data.get("endsAt"))
        provider_id = wp.targetVertex split -> id
        patient_id = fp.targetVertex split -> id
        mutations += [make_tombstone("vtx.provider." + provider_id + ".slot" + slot_cellcode(c)) for c in cells]
        mutations += [make_tombstone("vtx.patient." + patient_id + ".slot" + slot_cellcode(c)) for c in cells]
```

Three point reads (`.schedule`, `withProvider`, `forPatient`) replace the old link-walk — still O(1)
per appointment, not proportional to a hub's booking history (the exact cost profile
`kv.Links`/`bookingGuard` existed to bound, now moot because there is no enumeration at all).

---

## 3. Concurrency / safety summary

| Race | Outcome |
|---|---|
| Two concurrent `CreateAppointment`, same provider, same/overlapping cells | Exactly one commits; the other's batch rejects `CreateOnly` on the shared cell key → `RevisionConflict` → Processor retry → `kv.Read` now sees it live → `SlotConflict`/`PatientDoubleBook`. |
| Two concurrent `CreateAppointment`, same provider, disjoint cells | Both commit independently — no shared key, no contention (an improvement over the epoch design, which serialized ALL of a provider's bookings through one epoch even when disjoint). |
| Reschedule racing a fresh Create for the vacated cell | Whichever's atomic batch (revive-vs-create, or claim-vs-claim) commits first wins the `CreateOnly`/`expectedRevision` check; the loser retries and re-observes the new state. |
| Reschedule whose new cells conflict | Whole batch rejects — old cells (not in `to_release`... they ARE in `to_release`, but the batch is atomic) never actually tombstone. Original booking intact. |
| Terminal transition (cancel/complete/no-show) races a new booking for the freed cell | The tombstone and the new claim are different operations; if the new claim's `kv.Read` observes the cell still live (cancel hasn't committed yet), it fails closed and the caller retries — never a silent double-free race, because a tombstone is one atomic write, not a read-modify-write the claimant can interleave inside. |

No serialization epoch is needed anywhere — this is the substantive simplification over the
`kv.Links`/`bookingGuard` mechanism: a **set** guard (which cells does the hub hold) needs a lock
*because* the read that discovers the set is not itself a commit-time condition, but an **exact-key**
guard's read exists only to pick a mutation verb — the write itself, `CreateOnly`/`expectedRevision`,
*is* the lock, on every one of the N keys independently. This is the same reduction the design that
first introduced `kv.Links` earned for existence-uniqueness (`appliedToUnit`) — the grid product
decision lets the *range* constraint reduce to a **finite union of existence-uniqueness constraints**,
which is why the previously-necessary enumeration primitive is no longer needed for this consumer.

---

## 4. Non-goals / unchanged

- Provider `.hours` (business-hours) and `.timeOff` opt-in checks (`enforce_hours`, `enforce_time_off`)
  — already shipped on top of the old mechanism, untouched by this redesign (they run before the claim
  algorithm, exactly as before).
- The `forPatient`/`withProvider` appointment-sourced links — unchanged; they still serve the
  appointment's own projections and the reschedule `WrongProvider`/`WrongPatient` validation.
- `RecordEncounter` and everything after it in `ddls.go` — untouched.
- A *non-grid-aligned* booking becomes permanently unrepresentable (`SlotGridViolation`). This is the
  accepted product trade Andrew made 2026-07-02 (grid = package constraint), not a platform limitation.

---

## 5. Migration

Per F-004, a same-version package reinstall does not hot-upgrade existing vertices; a live stack with
providers/patients minted under the old package version simply has no `.bookingGuard`/`hasBooking`
state to migrate *away from* the new mechanism needs — the new mechanism creates its slot-claim
aspects lazily per booking, so there is **no pre-existing-*vertex* migration hole** (unlike the old
mechanism, which needed `.bookingGuard` seeded by `CreateProvider`/`CreatePatient` and would
`HydrationMiss` on a pre-capability vertex). The clinic package version bumps (`0.11.0` → `0.12.0`); a
fresh stack (`make down && make up-clinic`) is still the clean path for exercising it live, consistent
with the `appliedToUnit` (3704324) and Fire-2 (this redesign's predecessor) migration notes.

**Caveat — pre-existing *bookings*, not just vertices (flagged by review, 2026-07-03).** A live-only
bookable window has a real gap this migration does not close: an appointment that was already
scheduled/confirmed/checkedIn *before* this package version installed has no `providerSlotClaim`/
`patientSlotClaim` aspects (they never existed under the retired mechanism), so a brand-new
`CreateAppointment` for the same provider+time can claim that cell with no `SlotConflict` — the old
booking's slot is invisible to the new mechanism until *that* appointment itself is touched (a
`RescheduleAppointment` or a terminal `SetAppointmentStatus`, both of which read/write its cells).
There is no backfill script in this fire: on the trusted-tool, pre-launch, single-cell-MVP posture (no
real production bookings yet), a fresh bootstrap has zero pre-existing appointments and the gap is
moot. **If this ever needs to reach a stack carrying real live bookings, a one-time backfill (walk every
non-terminal appointment, `slot_cells` its `.schedule`, `claim_cell` on both hubs) must run before older
appointments regain double-book protection — file that as its own package-level item at that time; it
is out of scope for this fire.**

---

## 6. Surfaces touched

- `packages/clinic-domain/ddls.go` — drop `providerBookingGuard`/`patientBookingGuard` DDLs,
  `assert_no_overlap`/`bump_guard`/`tombstone_booking_links`, the `hasBooking` link authoring, and the
  `BOOKING_PAGE_LIMIT`/`MAX_BOOKING_PAGES` constants; add `providerSlotClaim`/`patientSlotClaim` DDLs,
  `enforce_grid`/`slot_cells`/`slot_cellcode`/`claim_cell`, and rewrite the `CreateAppointment` /
  `RescheduleAppointment` / `SetAppointmentStatus` bodies per §2.4–§2.6. `CreateProvider`/
  `CreatePatient` lose their `.bookingGuard` init (net simplification).
- `packages/clinic-domain/manifest.yaml` / `package.go` — version bump.
- `scripts/verify-package-clinic-domain.go` — assert the new aspect DDLs + `PermittedCommands`,
  assert the dropped DDLs are gone.
- `cmd/clinic-app/web/app.js` — the booking submit path currently lists `provider+'.bookingGuard'` /
  `patient+'.bookingGuard'` in `contextHint.reads` (grep hit, §Provenance); update to whatever reads
  the new mechanism actually needs (likely none beyond the existing provider/patient liveness reads —
  the slot-claim reads are lazy `kv.Read`, not declared). Surface `SlotGridViolation` /
  `AppointmentTooLong` as user-facing rejections alongside the existing `SlotConflict`/
  `PatientDoubleBook`/`OutsideHours`/`ProviderUnavailable` ones; if the FE's time picker doesn't
  already default to 15-minute increments, snap it to the grid so a user cannot construct a
  request the package will reject.
- `packages/clinic-reminders/integration_test.go` — sibling test referencing `hasBooking`/
  `bookingGuard` fixtures; update to the new mechanism.
- Tests — `integration_test.go`: rewrite the double-book / patient-double-book / reschedule /
  cancel-frees-slot / back-to-back-touching / `endsAt<=startsAt` suites against the new mechanism;
  add `SlotGridViolation` (misaligned start/end) and `AppointmentTooLong` (> 96 cells) cases; a
  **concurrency** assertion (two `CreateAppointment` racing the same cell → exactly one Accepted, one
  `SlotConflict` via the `CreateOnly` retry path, mirroring the Fire-2 concurrency test this replaces).
  `package_test.go`: DDL/permission assertions for the new aspect types, dropped-DDL assertions,
  `TestPackage_NoScans` stays green (no scans introduced or removed by this change — `kv.Links` was
  never a "scan" either).

---

## 7. Alternatives considered

| # | Alternative | Verdict |
|---|---|---|
| **A** | **Fix the Fire-2 banner divergence in place** (re-author `hasBooking` inbound per the original ratification, keep the epoch) | Rejected by Andrew (2026-07-02) — patches the letter of the ratified design but keeps the more complex, enumeration-based mechanism (and its unbounded LIST-cost growth the hard-delete-verb design was invented to solve) when a simpler, deterministic-key mechanism is available given the grid decision. |
| **B** | **Keep `kv.Links` enumeration, add the hard-delete verb** to bound its LIST cost | Shelved (Andrew, `hard-delete-mutation-verb-design.md`) — solves a problem this redesign dissolves; a real reclaim driver would have to reappear elsewhere before it's worth reviving. |
| **C** | **Slot-claim as a LINK** (`provider --claimsSlot<cell>--> appointment`) instead of an aspect | Rejected — the target segment must be the appointment's real (NanoID) vertex id, which differs per competing booking, so two concurrent claims for the same cell would never target the same link key and would never collide (§2.1). Only an aspect anchored on the hub with a cell-derived localName gives an invariant key. |
| **D** | **Store the claimed cell list on the appointment** (so cancel/reschedule can look it up instead of recomputing) | Rejected — a list of another vertex's aspect keys in `data` is exactly the relationships-in-data violation this whole lineage (`.bookings`, `hasBooking`) has been retiring; recomputing from `.schedule` + the existing `withProvider`/`forPatient` links (deterministic, already-established idiom) costs three extra point reads on the terminal path and stores no new relationship. |
| **E** | **One slot-claim key covering the whole interval** (e.g. `slot<start>-<end>`) instead of one key per 15-minute cell | Rejected — collapses back into a range-overlap problem (two different-but-overlapping intervals produce different keys, no shared key to collide on) — it's the original reason `kv.Links` was invented. The grid's value is specifically that it reduces a range constraint to a *finite, enumerable set* of point constraints. |

---

## 8. Neighbor reconciliation (flagged, not actioned — `lattice.md` is Lattice-lane-owned)

On ship, `planning-artifacts/backlog/lattice.md` row **"Op-time bounded reverse-link / adjacency read
(`kv.Links`)"** (currently `🏗️ building · ⚠️ build diverged... Fire 3 parked`) goes stale: `kv.Links`
Fire 1 (the platform builtin) stands as shipped and generically available, but Fire 2 (this row's
listed clinic consumer) is being **reverted/replaced** by this design, and Fire 3 (the clinic e2e +
lint-conventions hardening, already parked) no longer has a live consumer to validate against. The
**Vertical Steward does not edit `lattice.md`** (lane discipline) — this is a flag for the next Lattice
Steward fire: `kv.Links` becomes an unconsumed-but-tested primitive (not dead — it shipped with its own
unit test suite per Fire 1, independent of clinic); Fire 2/Fire 3 should be marked accordingly (e.g.
"reverted 2026-07-0X — clinic moved to write-path slot-claims, see
`clinic-booking-write-path-slot-claims-design.md`; `kv.Links` itself stands unconsumed, no other
demand yet") rather than left claiming an in-flight build that this fire supersedes.

`op-time-bounded-link-enumeration-design.md`'s Fire-2 checkpoint block is a historical record (what
was actually shipped) and is **not rewritten** — the ratification-banner lesson this whole provenance
chain is about applies to *ratification revisions*, not to after-the-fact supersession by a later,
independently-ratified redesign. A pointer is added there instead (§9).

---

## 9. Decomposition for the Steward (this design's own build)

**Increment 1 (next fire) — the full mechanism.** Everything in §2 + §6, one package version bump,
one green commit: drop the old mechanism, add the new one, rewrite the three ops, update
`clinic-reminders`' sibling test, update the FE contextHint + grid-snap + new rejection surfacing,
full test rewrite. **Full 3-layer adversarial review** (security/correctness-plane: a regression here
silently permits double-booking) — scoped to the package; no platform risk since no new primitive is
introduced.

No Increment 2 is anticipated — this fully replaces the double-book mechanism in one shot (unlike the
original conflict-detection design, which staged provider-hours into a later increment; that capability
already shipped independently and is untouched here).
