# Wellness vertical, Increment 1 — `wellness-domain` (studio / session / booking)

**Status:** ✅ Winston-ratified — build-ready. Pure implementation decisions (package shape, naming),
no frozen-contract change, no architectural fork — decided per CLAUDE.md / steward §0 and built this fire.

**Inc 2 shipped (2026-07-09, `a7f5b52`):** `cmd/wellness-app` — a thin FE mirroring `cmd/cafe-app`'s
shape exactly (vanilla HTML/CSS/JS, three views: Schedule / Roster / My Classes), reading the three P5
lenses below plus the shared `leaseApplicationComplete` convergence lens for the resident/lease picker
(the "who" dimension, mirroring `cafe-app`'s lease picker). Wired into the Path-A NATS permission matrix
(`deploy/gen-dev-nkeys`) alongside the other three vertical apps. Build/vet/lint/lint-conventions(P5)
all green, CI green. **Live lens reads verified (2026-07-09):** wellness-app on `:7802` authenticates to NATS and all four lens endpoints (`studios`/`sessions`/`bookings`/`residents`) return — `residents` is populated from the shared graph; `studios`/`sessions`/`bookings` await demo data the PO will create. Loading wellness-app's Path-A nkey requires the shared NATS container to read the current `deploy/nats-server.conf`; on a long-running dev stack a stale single-file bind-mount is only refreshed by a `docker restart lattice-nats` (a SIGHUP reload cannot fix a torn mount), not a code change.

## Scope of this increment

`verticals.md`'s Wellness row is ★★/L and bundles `wellness-domain` (studio + class/session + booking)
+ a thin FE (schedule grid · roster · my-classes) + the **resident-rate** payoff (CreateBooking reads
the booker's live lease, verifies residency, applies the member rate). Mirroring the Café precedent
(ledger-first, FE second), this increment ships **`wellness-domain` only** — the package's vertex
types, ops, and P5 projection lenses. The thin FE (Inc 2) and the mixed-use composition surfaces that
consume it (verticals.md, gated "after Wellness") land in follow-up fires.

## Ground: mirrors `clinic-domain`'s slot-claim guard, `loftspace-domain`'s `applicationFor` link

Read `packages/clinic-domain/{ddls.go,lenses.go,permissions.go,package.go}` in full before writing
this — clinic-domain is SELF-CONTAINED (owns its own vertex types, no package dependency beyond
declaring links onto identity), exactly the shape wellness-domain needs. Two precedents this
increment reuses **verbatim in mechanism, renamed in name**:

1. **The double-book write-path guard (`clinic-domain/ddls.go` `slot_cells`/`slot_cellcode`/`claim_cell`,
   lines ~1409-1445).** A 15-minute-grid discretization of `[startsAt, endsAt)` into deterministic cell
   codes, each claimed as a `CreateOnly`/`expectedRevision`-conditioned existence-marker aspect on a hub
   vertex — the write-path key collision at commit IS the double-book lock (Capability-KV §06, "the
   operation's own Starlark logic"), never a read-time enumeration. **Reused unchanged, renamed**: the
   hub is `vtx.studio.<id>` (was `vtx.provider.<id>`), the aspect class is `studioSlotClaim` (was
   `providerSlotClaim`). Wellness has **no patient-side symmetric claim** — clinic's `patientSlotClaim`
   exists to catch one patient double-booked across two *different* providers at the same instant; a
   wellness session has no such per-booker double-booking rule (a resident may hold two simultaneous
   class enrollments — that's a business choice, not a platform gap, and nothing in the PO's demand row
   asks for it — YAGNI).

2. **A NEW extension of the same mechanism, not a new mechanism: capacity-bounded seat claims.**
   Clinic's slot-claim is a *binary* exclusivity lock (1 claimant per cell). A wellness class session has
   **N-capacity** enrollment (a roster, not a 1:1 booking), which the binary lock doesn't cover on its
   own. The clean extension — **not a paper-over, not a new primitive** — is the *identical*
   `CreateOnly`-key-collision idiom applied over an **enumerated seat-index dimension** instead of a
   time-cell dimension: pre-name `N` deterministic candidate keys (`vtx.session.<id>.seat1` ..
   `.seat<capacity>`), and `CreateBooking` walks them in a **bounded `for`-range** (Starlark has no
   while-loop; `MAX_SESSION_CAPACITY` is a generous backstop, same idiom as clinic's `MAX_SLOT_CELLS`
   bounded enumeration, `ddls.go` line ~1418), claiming the **first cell it reads absent** via
   `CreateOnly`. This is mechanically the SAME safety property clinic's `claim_cell` already has (a
   lazy `kv.Read` picks the mutation verb; the atomic batch's `CreateOnly`/`expectedRevision`
   conditioning at commit is what actually serializes concurrent claimants — two callers racing for the
   last open seat both read it absent, both emit `op:create` for the identical key, and `CreateOnly` at
   revision 0 commits exactly once). No enumeration read, no serialization epoch, no new engine
   capability — a bounded loop over caller-computable keys is exactly what the appointment DDL's own
   `slot_cells` already does. `capacity` is a `SetSession`-time input, `1..MAX_SESSION_CAPACITY`
   (`=200`, matching clinic's 24h/96-cell backstop philosophy: generous, not an expected ceiling).

3. **Residency check — no new link needed.** The board row says "CreateBooking reads the booker's live
   lease (`contextHint.reads`; verify `heldBy` the booker → no over-grant)". Grounding in
   `packages/lease-signing/ddls.go`: a `leaseapp` already carries `lnk.leaseapp.<id>.applicationFor.identity.<id>`
   (the later-arriving `leaseapp` is the source, Contract #1 §1.1 — "leaseapp applicationFor identity"),
   written by `CreateLeaseApplication` and living for the lease's lifetime. **No `heldBy` link needs
   inventing** — `applicationFor` already IS "this lease belongs to this identity"; `CreateBooking`
   reads it by known key (`kv.Read`, the same lazy-read-picks-the-verb idiom as `claim_cell` /
   `require_matching_patient`) to verify the caller-supplied `booker` identity matches the
   `leaseAppKey`'s actual applicant before applying the resident rate — over-granting the discount to a
   caller who merely *names* someone else's lease is exactly what this check closes.
   `contextHint.reads: [leaseAppKey]` on the client's op submission (`cmd/loftspace-app/op.go` line 61
   precedent: `env.ContextHint = &processor.ContextHint{Reads: reads}`) is a pre-fetch **optimization**
   (`internal/processor/starlark_kv.go` §2.5 — "a pre-fetch optimisation, not a gate"), not itself the
   safety property; the gate is the script's own `kv.Read` + link check, same as everywhere else in this
   codebase.

## Shape

Package `wellness-domain`, **self-contained except for the one cross-package read above** (reads a
`lease-signing` `leaseapp`'s `applicationFor` link by known key — the same "reads another package's
vertex by known key, no declared dependency" idiom `loftspace-ledger`'s `DebitAccount`/`heldFor` and
`cafe-domain`'s `cafeTabSettlement` lens already use for `leaseapp`). `Depends: [lease-signing]` for
install-order/documentation honesty, mirroring `loftspace-ledger`'s own `Depends` comment.

- **`studio`** vertex type (DDL `studio`) — `CreateStudio{name}` mints `vtx.studio.<NanoID>` (root
  `{}`, D5) + `.profile` aspect `{name}`. `TombstoneStudio` soft-deletes. Minimal on purpose — a studio
  is a bookable *room*, not a business entity with hours/staff (no `.hours`/`.timeOff` clinic-style
  layer this increment; YAGNI until a demand row asks for it).
- **`session`** vertex type (DDL `session`) — `CreateSession{studio, name, startsAt, endsAt, capacity}`
  validates the studio alive + class=`studio`, discretizes `[startsAt,endsAt)` into 15-minute cells
  (same grid discipline as clinic, `SlotGridViolation`/`SessionTooLong` at 96 cells), claims one
  `studioSlotClaim` per cell (`StudioConflict` on collision — no two overlapping sessions in the same
  studio), and writes the session vertex (root `{}`) + `.schedule` aspect `{name, startsAt, endsAt,
  capacity}` + `atStudio` link (session→studio, later-arriving source). `TombstoneSession` soft-deletes
  the session AND releases its held `studioSlotClaim` cells (mirrors clinic's terminal-status release,
  simplified to unconditional since wellness sessions have no multi-state status lifecycle this
  increment — a session exists or is tombstoned, no scheduled/confirmed/cancelled state machine; YAGNI,
  the PO demand row doesn't ask for one).
- **`booking`** vertex type (DDL `booking`) — `CreateBooking{session, booker, leaseAppKey?}` validates
  the session alive + class=`session` and the booker alive + class=`identity`, reads the session's
  `.schedule.capacity` to bound the seat-claim loop, claims the first free `vtx.session.<id>.seat<n>`
  (`SessionFull` if all `capacity` seats are claimed), and writes the booking vertex (root `{}`) +
  `.status` aspect `{value: "booked", rate: "resident"|"standard"}` + `forSession` link (booking→session)
  + `bookedBy` link (booking→identity). **Resident-rate**: `leaseAppKey` is optional — when supplied,
  `CreateBooking` reads `lnk.leaseapp.<leaseId>.applicationFor.identity.<bookerId>` (known key); a live
  link sets `rate: "resident"` and writes `lnk.booking.<id>.residentRate.leaseapp.<leaseId>` (the
  ratifying link the future one-bill-style composition lens can walk, mirroring `cafe-domain`'s
  `tabRef` audit-link idiom); an absent/mismatched link — the named lease doesn't belong to this booker
  — is **not a hard failure** (a resident is still allowed to book, just at `rate: "standard"`, and a
  non-resident may book at all — wellness classes are not lease-gated) but is a `rate: "standard"`
  fall-through, never a silent over-grant. `CancelBooking{bookingKey}` soft-deletes the booking and
  releases its held seat cell.
- **Lenses (P5, plain `nats-kv`, no PHI/RLS layer needed — unlike clinic, nothing here is
  sensitive/PII):**
  - `wellnessStudios` — one row per studio (bucket `wellness-studios`), the studio picker.
  - `wellnessSessions` — one row per session, joined to its studio name + a derived `bookedCount`
    (**NOT** a stored counter — computed the same way `cafeTabSettlement` derives its gap booleans:
    the lens engine has no aggregate `COUNT`, so `bookedCount` is deferred to Inc 2's FE, which reads
    `wellnessBookings` client-side and counts rows per `sessionKey` — mirrors `cmd/loftspace-app`'s
    client-side `computeTabs`-style aggregation, not a new lens capability) (bucket `wellness-sessions`).
  - `wellnessBookings` — one row per booking, joined to session + booker (bucket `wellness-bookings`),
    the roster / my-classes query surface.
- **Permissions** — every op granted to `operator`, scope `any` (mirrors clinic-domain /
  cafe-domain exactly; the trusted-tool operator already holds standing permission, no new capability
  surface).

## Adversarial review finding, fixed before merge

An independent review of Inc 1 (before admit) found the residency check as first written only verified
the `applicationFor` link was live — it never checked whether the named lease had actually been
**approved**. Grounding in `packages/lease-signing/scripts.go`: a **declined** application keeps both
the leaseapp and the `applicationFor` link alive (only a `.decision` aspect is written); a **pending**
(never-decided) application is likewise still alive+linked. Either would have wrongly qualified for the
resident rate under the original check. Fixed: `CreateBooking` now additionally requires (a) the
leaseapp itself alive and (b) its `.tenancy` aspect present — CreateOnly-stamped only on a leaseapp's
**first** `DecideLeaseApplication` approve (`lease-signing/scripts.go` line ~473), the one signal that
distinguishes an active tenancy from a merely-applied-for one. A new regression test,
`TestCreateBooking_PendingLeaseFallsBackToStandardRate`, seeds exactly this pending shape (live link, no
`.tenancy`) and asserts the fallback — verified to fail against the pre-fix logic before being folded
into the fire (reverted the fix locally, confirmed the test catches it, restored).

## Next (this design doc's checkpoint)

- **Inc 2 — thin FE shipped** (`cmd/wellness-app`, `a7f5b52`). Live-data browser verify is pending the
  one-time NATS reload noted above (a stack-infra step, not a code gap).
- **Mixed-use composition surfaces** (verticals.md, gated "after Wellness") — front-desk / operations
  aggregate views, once Wellness's own lenses are live.
