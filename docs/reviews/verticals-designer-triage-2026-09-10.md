# Verticals designer triage — the four `📐 needs designer pass` rows (2026-09-10)

**2026-09-10, Winston (Andrew-directed session).** Mandate, verbatim: *"verticals backlog. Address all
items marked for designer pass. Be critical. oftentimes there is a simple solution and/or existing
pattern."* Method: the [2026-08-27 pass](verticals-designer-triage-2026-08-27.md)'s — every row's
`no-pattern:` claim re-run against today's tree, briefed to falsify; two read-only grounding agents run
concurrently with the draft against the load-bearing claims (§7); lead reads of every file:line cited.

**Outcome: none of the four rows needs the mechanism its `no-pattern:` names.** Two dissolve into a
package edit that *copies a shipped shape* (`📋 ready`, S each); one dissolves into a recovery path the
platform already has (`🗄️ shelved`, revive trigger recorded with the would-be design); one is a bounded,
self-healing install transient that no runtime mechanism can shorten without breaching a design-of-record
(documented, closed). No architectural fork, no frozen-contract change — the whole pass is
Winston-adjudicated under the 2026-08-20 delegation.

| Row | Filed `no-pattern:` | Verdict |
|---|---|---|
| Café — two debtors the desk cannot name (§2) | a tab that records the café it was opened at | The lease's unit was reaped out from under it; the shipped shape is the **repair op** (`SetMenuItemLocation`, `ReassignSession newStudio`): `ReassignLeaseUnit` + a seed backfill; the roster lens needs nothing → **📋 ready · S** |
| Wellness — a guest's debt outlives the booking (§3) | a charge that records the studio it was posted at | The no-show twin already keeps the vertex live under a terminal status; a forfeiting late cancel does the same (`status=forfeited`, no seat) instead of tombstoning → **📋 ready · S+S** |
| LoftSpace — a just-minted applicant on no roster (§4) | an identity that records the workplace it was minted at | Zero live instances; the recovery exists (re-mint → duplicate flag → hygiene merge); the "third sighting" mechanism retires this row alone → **🗄️ shelved** (revive + design recorded) |
| Cross-vertical — first Weaver dispatch races the grant projection (§5) | a projection-aware first dispatch | One install commit, two Refractor projections, a fire-and-forget actuator: the ≤30 min first-dispatch lag is bounded and self-healing, and every shortening breaches a design-of-record → **documented, closed** |

## 1. The recurring cause, named once

The LoftSpace row was filed as the **"third sighting"** of one class — *"an identity that records the
workplace it was minted at (third sighting: the café tab + wellness charge rows)"* — and the consolidation
heuristic that paid off twice on 2026-08-27 (*same-component rows share one root*) invited a single
`mintedAtSite` mechanism across all three. Grounding refutes the consolidation: the three rows share a
**symptom** (a person a desk cannot reach), not a root. The café identity is unreachable because a seed
script reaped its lease's unit (a data repair); the wellness guest because one branch of `CancelBooking`
deletes where its no-show twin keeps (a doctrine split); the LoftSpace applicant because a bounded ceremony
window has no roster row (a recovery path that already exists). Priced per §3.7's payoff column, the shared
mechanism retires **only the zero-instance LoftSpace row**: it would name café identities only if minted at
the café's desk and only from now on (no backfill), and it would not reach the wellness arrears grid at all,
whose coverage is `wellnessMembers ∪ wellnessBookers` unioned app-side
([residents.go:247-264](../../cmd/wellness-app/residents.go)) — an identities-lens arm is not in that
union. **A consolidation by symptom is a hypothesis; run the payoff column per row before believing it.**

## 2. Café — "The front desk is sent to collect from two residents it cannot name"

**Filed (7664ab7b, PO run 2026-09-04):** `read_cafe_identities` holds 118 rows, a front-desk staffer reads
46; both unresolvable debtors' rows carry only their self-anchor because their lease's unit is tombstoned
(`WAMQkYYu5fxivfeAUGUK`, `21EEgBtYFxqy1Rz8qzBp`); `cafe-lease-workplaces` already flags both
`missingLocation:true`, `cafeIdentitiesRead` has no such fallback.

**Grounding.**

- The name is RLS-gated on `authz_anchors`, and the workplace token comes from one walk —
  `[(i)<-[:applicationFor]-(l:leaseapp)-[:appliesToUnit]->(u)-[:containedIn*0..7]->(c) | …]`
  ([cafe-domain/lenses.go:426-434](../../packages/cafe-domain/lenses.go)). Contract #1 filters a
  tombstoned vertex out of every walk, so a dead `u` binds nothing and the row keeps its self-anchor.
  There is no "every front desk" token to fall back on: `staffReadGrants` issues one token per building a
  `frontOfHouse` actor `worksAt` ([service-location/lenses.go:199-207](../../packages/service-location/lenses.go))
  and nothing else. The lease-side rescue (`097aa843`) worked because `cafe-lease-workplaces` is a plain
  NATS-KV read model the app filters itself; a Postgres RLS model cannot be rescued app-side.
- **How the unit died:** `seed-classic-demo.go`'s `reapDuplicateListings` tombstoned every "12 Classic Demo
  Ave" duplicate via `TombstoneLocation`, unconditionally, until `9a3a7807` added its `unitsWithLiveLeaseApps`
  keep-list — *"a tenancy TombstoneLocation has no way to see (location-domain is vertical-agnostic; only
  loftspace-domain knows what appliesToUnit means)"* ([seed-classic-demo.go:1046-1060, 1414-1420](../../scripts/seed-classic-demo.go)).
  `TombstoneLocation` itself guards only `vertex_alive`
  ([location-domain/ddls.go:464-472](../../packages/location-domain/ddls.go)); it is operator-only
  ([permissions.go:21-24](../../packages/location-domain/permissions.go)).
- **Nothing re-points a lease.** Writers of `appliesToUnit` repo-wide: `CreateLeaseApplication` creates it
  ([lease-signing/scripts.go:629](../../packages/lease-signing/scripts.go)), `WithdrawLeaseApplication`
  tombstones it (`:994`), and the package's own comment records *"minted with a live appliesToUnit link and
  nothing ever tombstones"* (`:1241`). lease-signing's convergence lens already treats a dead unit as
  *terminal-not-violating* ([lenses.go:446-452](../../packages/lease-signing/lenses.go)) — the platform's
  stance on such a lease is "nothing left to remediate", while the ledger account `heldFor` it keeps its debt.
- **The shipped shape.** "X outlived its place → a repair op re-points X" ships twice in this codebase, both
  minted from verticals rows: café's `SetMenuItemLocation` (`opmetas.go:298`; granted `operator, frontOfHouse`,
  [permissions.go:112-115](../../packages/cafe-domain/permissions.go)) with the seed's idempotent
  `backfillMenuItemLocations` loop ([seed-classic-demo.go:420-445](../../scripts/seed-classic-demo.go)), and
  wellness's `ReassignSession newStudio` (operator-only regardless of hat,
  [permissions.go:137](../../packages/wellness-domain/permissions.go)). Both exist because
  `TombstoneLocation`/`TombstoneStudio` do not cascade — the no-cascade doctrine the whole corpus honours —
  and the consumer-side flag (`missingLocation`, `missingStudio`) names the gap the repair closes.

**Verdict — `ReassignLeaseUnit` (lease-signing, operator-only) + the seed backfill; the identities lens is
untouched.** Live population 2026-09-10 (§7): **10** stranded leases over **7** dead units, all reaped
2026-08-23T03:03Z, every `appliesToUnit` link live and every target unit tombstoned. Once the lease points at a live unit, `cafeIdentitiesRead`'s existing walk binds the building
and the two debtors are named; `cafeLeaseWorkplaces`' `missingLocation` clears by the same edit; every other
`leaseapp→unit→containedIn` walk (wellnessMembers, loftspace `applicantRosterRead`,
`landlordLeaseApplicationsRead`) heals for free.

Shape (mirrors `ReassignSession`'s studio branch, [wellness-domain/ddls.go:3195-3225](../../packages/wellness-domain/ddls.go)):

- Payload `{leaseAppKey, newUnitKey}`; both declared `reads`; `require_live_typed` on each (`leaseapp`,
  `unit`). Operator-only (`scope: any`, `GrantsTo: [operator]`) — a tenancy's unit is not a front-desk call,
  and the reap case is an operator repair by definition.
- One bounded `kv.Links(leaseAppKey, "appliesToUnit", "out")` (class (e), 0..1 by construction) → tombstone
  the live link (whatever its target's state; the repair is for exactly the dead-target case, but a live
  target is an ordinary move); `make_link_create_or_revive` the new
  `lnk.leaseapp.<id>.appliesToUnit.unit.<new>` (a lease moved back to a unit it once held revives its
  tombstone — the same reason `ReassignSession` revives). A no-op when the live link already names `newUnitKey`.
- The per-(applicant, unit) guard: one bounded `kv.Links(leaseAppKey, "applicationFor", "out")` (0..1) for the
  applicant; on `lnk.identity.<a>.appliedToUnit.unit.<new>` copy `CreateLeaseApplication`'s three-way block
  verbatim ([scripts.go:596-620](../../packages/lease-signing/scripts.go)): alive → `DuplicateApplication`
  (the applicant already has a live application there — refuse, the operator withdraws one first); absent →
  create; tombstoned → `make_link_revive_occ`. The old pair's guard link stays: a dead unit accepts no
  application, and the guard's contract is per live pair.
- Event `lease.unitReassigned {leaseAppKey, oldUnitKey, newUnitKey}`; response `{primaryKey: leaseAppKey}`.
- Op-meta: `Dispatch.Enumerations` `{payload.leaseAppKey} appliesToUnit out` + `applicationFor out`;
  `OptionalReads` for the new link + guard keys; descriptor with two `x-entityRef` pickers (lease, unit).
  Manifest version + `Version` constant bump (`lint-package-version`).
- Seed: `backfillLeaseUnits(ctx, conn, adminKey, unitKey)` beside `backfillMenuItemLocations` — every live
  `leaseapp` whose `appliesToUnit` target is dead (the `servedAtIsLive` shape) is re-pointed at the canonical
  unit. Idempotent; the live proof runs it against the dev stack.

**Executable censuses (Phase-0 of the build):**

```sh
# the stranded population — live 2026-09-10: 10 rows (097aa843 said 11; one leaseapp has since been tombstoned), 7 dead units
nats kv ls cafe-lease-workplaces | while read k; do nats kv get cafe-lease-workplaces "$k" --raw; done | grep -c '"missingLocation":true'
# writers of appliesToUnit (expected: 1 create, 1 tombstone, 0 re-point)
grep -rn "appliesToUnit" --include='*.go' packages | grep -v _test | grep -i "make_link\|tombstone\|revive"
```

**Alternatives.**

| Option | Why not |
|---|---|
| **Do not have this thing** — leave the 10 leases stranded; an operator (WildcardAnchor) reads the names and relays | ★★ live harm on the desk's own collection surface; the unattributable rescue was explicitly *"visibility to notice the debt and escalate to an operator, who can fix the underlying data"* (`097aa843`) — and there is no verb for that operator today |
| Lease records its building at application (`leaseapp coveredBy location` snapshot, the wellness `atLocation` idiom) + a `u.key = null`-gated fallback in every lease-anchored walk + `require_workplace`'s write-side mirror | Touches four packages' lenses and the write-side walk to make a lease *survive* its unit's death; still needs an op to backfill the 10 (their units are dead). The repair op is one package, ~60 lines, and restores the invariant instead of tolerating its breach |
| `TombstoneLocation` refuses a unit under a live lease | Layering the seed comment already names — location-domain does not know `appliesToUnit`; and the doctrine is no-cascade + consumer flag + repair, never refuse (`SetMenuItemLocation`, `ReassignSession` exist *because* tombstones are allowed). A guard would also leave the 10 unrepaired |
| A role-level "every front desk" read token so an unattributable identity's name resolves anywhere | Security-plane widening (a new `cap-read.staff` producer shape) to paper over a data gap |
| The row's own `no-pattern:` — a tab that records the café it was opened at | Solution-shaped: the identity's coverage would then depend on a tab existing; the debt lives on the account, not the tab; and the 10 leases stay dead |

**Contract surface:** none — a package op; builds to Contract #1 §1.1 (link direction unchanged).
**Test strategy:** the op's own pipeline test (dead-unit re-point; live-unit move; `DuplicateApplication`;
revive-on-return; no-op), fixtures resolving the declared enumerations via `testutil.DeclaredEnumerations`;
live proof = the seed backfill on the dev stack, then `read_cafe_identities` for the front-desk staffer
(46 → 46 + the repaired leases' applicants) and the two debtors named in the arrears grid.
**Size S · `📋 ready` · Winston-adjudicated.**

## 3. Wellness — "A guest's debt outlives the booking that made them visible"

**Filed (33901636, residual of `8731eac5`):** lease-less coverage comes from `wellnessBookers` (live
bookings only) and `CancelBooking` tombstones the booking, so a late-cancel forfeit stands on someone no desk
can reach. Census 2026-09-05: 4 debtors, 0 in this shape; an absence-routed fallback was refused at review.

**Grounding.**

- `wellnessBookersSpec` is booking-anchored with **no status filter** — `MATCH (bk:booking)`,
  `bk.status.data.value AS status`, coverage = `forSession → atLocation → containedIn*0..7`
  ([wellness-domain/lenses.go:336-342](../../packages/wellness-domain/lenses.go)); the app's
  `computeCoveredBookers` never reads the status either ([residents.go:210-233](../../cmd/wellness-app/residents.go)).
  A live booking in *any* status covers its booker.
- `CancelBooking` tombstones **unconditionally** — `mutations = [make_tombstone(book_key)]` in both branches
  ([ddls.go:4237, 4254](../../packages/wellness-domain/ddls.go)); `is_late_cancel` (computed at `:4219`,
  before either branch) is read once, at `:4323`, to skip the refund mint and emit
  `wellness.lateCancelForfeited` instead. Its own comment names the twin: *"the same standing charge a
  no-show leaves (SetBookingAttendance never reverses one either)"*.
- **The twin keeps the vertex.** `SetBookingAttendance` upserts `.status {value: noShow, …}` on a live
  booking ([opmetas.go:849-891](../../packages/wellness-domain/opmetas.go)); `wellnessNoShowSettlement`
  converges on it; the no-show guest stays in every desk surface through `wellnessBookers`, and stays
  nameable through `wellnessIdentitiesRead`'s booking fan-out — *"a guest is visible to the desk of the class
  they booked"* (Done log, `8731eac5`) is the designed reach, and it is permanent for `attended`/`noShow`
  today. Clinic's cancel is a status flip on a live appointment too
  ([clinic-domain/opmetas.go:229](../../packages/clinic-domain/opmetas.go)). The late-cancel forfeit is the
  one standing-charge outcome in the corpus recorded as a *deletion* — and the deletion is the whole gap.
- The row's `no-pattern:` (record the studio on the charge) is **incomplete on its own terms**: a
  tx-anchored coverage lens would list the debt but not the name — `wellnessIdentitiesRead` decrypts a
  guest's name for the desk only through a live *booking* ([lenses.go:730](../../packages/wellness-domain/lenses.go)) —
  which is the café row's exact symptom re-created one vertical over. One liveness fact that every consumer
  already reads beats a second coverage source that each consumer must be taught.

**Verdict — a forfeiting late cancel keeps its vertex under a terminal `forfeited` status.** In the
`value == "booked"` branch, when `is_late_cancel`, replace `make_tombstone(book_key)` with
`make_aspect_upsert_occ(book_key, "status", "bookingStatus", {value: "forfeited", rate, booker, session,
className, classStartsAt}, status.revision)` — the attendance carry-forward set **without `seat`**: the seat
is released or handed to the promoted waitlister exactly as today, and `waitlistPromotionSpec` counts
`seat <> null` ([lenses.go:672](../../packages/wellness-domain/lenses.go)), so capacity frees the moment the
status is written (its comment at `:621-623` is reworded from "the moment its booking dies"). The waitlisted
branch and the non-late booked branch keep tombstoning — nothing is owed on either. Rule, in one line: *a
booking that still owes stays.* Whether the class-price charge had posted yet does not enter the predicate:
with `status <> 'booked'` the settlement lens never posts one, so a kept-but-uncharged forfeit owes nothing,
same as today. **The token is `forfeited`, not `cancelled`:** the wellness FE already uses `cancelled` for
*"the studio called off this class"* (`app.js:1273`, `const cancelled = !b.sessionName`), and no `cancelled`
status value exists anywhere in `packages/wellness-*` (grep: zero hits).

**Consumer census — every reader of a wellness booking's liveness or status** (run by the falsifier
against today's tree, §7; the build's Phase-0 re-runs it). Six sites **must change** in the same fire;
the rest are confirmed inert or intended.

| Consumer | Reads | On a live `forfeited` booking |
|---|---|---|
| `wellnessBookers` (lenses.go:336), `computeCoveredBookers` (residents.go:210) | no filter | keeps covering the guest — **the payoff** |
| `wellnessIdentitiesRead` booking fan-out (lenses.go:730) | no filter | the desk of the booked class keeps the name decrypt — the `noShow`/`attended` posture, and what makes the debt collectable |
| **`SetBookingAttendance`** (ddls.go:4474-4476) | refuses only `waitlisted` | **must refuse `forfeited`** — today's guard would re-mark a forfeited booking `noShow` and write a status with neither `seat` nor `waitlistSlot`, the shape `:4469-4471` says the op exists to prevent |
| **`sessions.go:150` `BookedCount`** and `app.js:1460` `bookedCount` | `status != waitlisted` | **must exclude `forfeited`** — otherwise the freed seat still counts and `app.js:1004/1513` refuse bookings at capacity |
| **`ATTENDANCE_MARKS`** (app.js:2400-2403), My Classes card (`:1273-1291`), `cancelDisabled` (`:1277`), "Release seat" label (`:2428`), billability (`:1064`) | status | **must add** a `forfeited` badge/card ("cancelled inside the late window — class price forfeited"), disable Cancel, drop the release action |
| **`CancelBooking`** re-cancel (ddls.go:4193) | `value ∉ {booked, waitlisted}` → `AttendanceRecorded` | refused already; **must reword** to name `forfeited` |
| **`bookingStatus` aspect DDL** `InputSchema` enum (ddls.go:1147) | not enforced at commit (`step6_validate.go:53-62` reads no schema) | **must add** `forfeited` — for the descriptor and the reader, since nothing refuses it |
| **`front-desk/lenses.go:157` `bookingHistorySpec`** | no filter, projects status | history shows the forfeit — correct; **its FE must render** the value |
| `wellnessClassPriceSettlement` (wellness-ledger/lenses.go:282), `wellnessNoShowSettlement` (`:194`), `wellnessBookingReminders` (wellness-reminders/lenses.go:147), `pastDueBookings` (pastdue.go:125) | `= 'booked'` / `= 'noShow'` | inert — correct |
| `orphanedBookingSettlement` (lenses.go:605), `ReleaseOrphanedBooking` (ddls.go:4604) | `booked|waitlisted|noShow` | never released — correct (nothing held; a forfeit on a later called-off class stands, as today's tombstone leaves it); the vertex is as permanent as an `attended` one |
| `collect_waitlist_candidates` (`:3629`), `PromoteWaitlistedBookings` (`:4914`) | `= 'waitlisted'` | skipped |
| double-book guard (`:4018-4025`, released `:4345-4348`) | the `.bkr<id>` cell, not status | re-booking mints a new booking key — no collision |
| wellness-ledger `scripts.go:228, 238` | `vertex_alive` only | a staff debit may reference it — parity with `attended`/`noShow` |
| `memberAccounts` (wellness-ledger/lenses.go:433), `ledgerHistory` (`:386-387`) | existence | account row kept; the forfeit's `settlesClassPrice` hop now resolves in history — an improvement |
| `wellness.bookingCancelled` event | — | zero consumers repo-wide |

**Alternatives.**

| Option | Why not |
|---|---|
| **Do not have this thing** — the guest re-enters coverage at their next booking; wellness has no reminder target, so no surface could act on the debt sooner anyway | Defensible at ★ and zero instances, and stated so. Rejected because the fix adds no *machinery* — it swaps one mutation for the shape its twin already uses — and closes a doctrine split a reviewer will otherwise re-find; the six touch-points are the price of any terminal status and are one-liners |
| The row's `no-pattern:` — the charge records the studio (a `wellnesstransaction atLocation` snapshot + a tx-anchored coverage lens + an app-side union) | Lists the debt without the name (see grounding); adds a lens, a bucket and a union for the same payoff; keeps the deletion that caused the gap |
| Keep **every** booked cancel live (clinic parity) | Wider than the need; the roster would carry cancel history nobody asked for. The `forfeited` value shipped here is the seed if a later row wants it |

**Contract surface:** none. **Test strategy:** the CancelBooking pipeline test gains the late/booked branch
asserting a live vertex, `status=forfeited`, no `seat`, seat released/promoted, `lateCancelForfeited` emitted;
`SetBookingAttendance` refuses a forfeited booking; a `wellnessBookers` pin that a `forfeited` row projects
coverage; a `waitlistPromotion` pin that it is not seated; a `sessions.go` pin that it is not counted. Live
proof: late-cancel a guest's booked seat on the dev stack → the guest stays in the arrears grid, named, with
the standing charge, and the seat is bookable.
**Size S (pkg) + S (FE) · `📋 ready` · Winston-adjudicated.**

## 4. LoftSpace — "A just-minted applicant with no application is on no staffer's roster"

**Filed (1a8f3c62, the roster-reach designer row):** `applicantRosterRead` anchors an identity on its own key
+ the landlord/buildings of units it has a live application against, so a claim secret lost before the first
application can be re-issued by nobody. Live: both unclaimed tenants are reachable.

**Grounding.**

- The arms are as filed ([loftspace-domain/lenses.go:250-259](../../packages/loftspace-domain/lenses.go));
  the Re-issue-secret offer (`RotateClaimKey`, shipped `82e26df5`) is rendered per roster row
  ([app.js:922-965](../../cmd/loftspace-app/web/app.js)), so no row ⇒ no offer.
- **The window is the desk's own ceremony.** `CreateUnclaimedIdentity`'s reveal says it: *"if it is lost,
  the identity needs a fresh one issued"* ([identity-domain/opmetas.go:94-96](../../packages/identity-domain/opmetas.go)).
  A fresh one *can* be issued: the op **creates** on a duplicate contact and flags it — *"Duplicate detection
  rides the identity.created event's data.duplicate flag, not the reply"* (`:173`) — and identity-hygiene's
  `duplicateCandidates` lens lists `unclaimed`/`claimed` pairs for the operator's `MergeIdentity`
  ([identity-hygiene/lenses.go:44-45](../../packages/identity-hygiene/lenses.go)). Re-mint, hand over the new
  secret, and the orphan is either merged or sits inert (no links, no claim).
- **Zero instances**, by the row's own census, and the population that can ever enter the window is
  "identities a desk minted and did not immediately attach to an application" — the LoftSpace desk mints on
  the Applicants panel and the applicant applies self-scoped (`CreateLeaseApplication` is operator/any +
  consumer/self, [lease-signing/permissions.go:68-78](../../packages/lease-signing/permissions.go)).

**Verdict — dissolve; `🗄️ shelved`.** The recovery path is the platform's own duplicate-registration
design, and the row's harm is the cost of one re-mint plus an optional merge. A design that records a fact
on every identity in every vertical to spare that re-mint does not clear the demand bar at zero instances.

**Revive trigger:** a PO-observed instance (a person re-minted because the desk could not find them), or a
second lens that needs a mint-site anchor (the café/wellness rows do not — §1). **The design, if revived, is a
transplant, not new:** `CreateUnclaimedIdentity` records `identity mintedAtSite <location>` from one bounded
`{actor} worksAt out` enumeration — `registration_site_mutations` verbatim
([clinic-domain/ddls.go:1521-1569](../../packages/clinic-domain/ddls.go)), declared on the op-meta as
`Dispatch.Enumerations` ([clinic-domain/opmetas.go:506-518](../../packages/clinic-domain/opmetas.go)) — and
`applicantRosterRead` gains a third comprehension `[(i)-[:mintedAtSite]->(b:building) | nanoIdFromKey(b.key)]`.
Cost the revive must price: identity-domain DDL + manifest version + op-meta; **29 `_test.go` files submit
`CreateUnclaimedIdentity` and none resolves its enumerations today** (`grep -rln '"CreateUnclaimedIdentity"'
--include='*_test.go' packages cmd internal scripts | wc -l` → 29; the read-drift guard reds every hand-built
envelope — [feedback: clinic-ledger `9390fa76`](../../agents/designer/SKILL.md)); the descriptor drift
baseline; and a service-minted identity (`cmd/lattice identity provision`, the seeds) records nothing — the
arm's floor is the self-anchor, as clinic's.

## 5. Cross-vertical — "A new op's first Weaver dispatch races its permission's projection"

**Filed (9a0f5633, from the café Inc 4 close):** *"the first eight dispatches fired within a second of the
install and were `AuthDenied` because the capability lens had not yet projected the new grant — the
30-minute mark lease healed it"* ([cafe-ledger-design.md:345-352](../../_bmad-output/implementation-artifacts/cafe-ledger-design.md)).
`no-pattern:` a projection-aware first dispatch (install ordering, or a decline class for a grant projected
after the deny).

**Grounding — the mechanism, end to end** (the falsifier corrected three of my first-draft premises; the
corrected reading is what follows, §7 has the record).

1. **One commit.** `InstallPackage` and `UpgradePackage` each carry the target's `meta.weaverTarget` vertex,
   the permission vertex and the `grantedBy` link in a single step-8 batch (`buildManifestBatch`,
   [pkgmgr/installer.go:97-99, 298-312](../../internal/pkgmgr/installer.go); `upgrade.go:115`; `Apply`
   delegates to both). The installer has no seam between "grant committed" and "target committed"; making one
   is a Contract #8 atomicity change.
2. **Three legs of one commit, no ordering.** Target *registration* is zero lens hops — Weaver's
   `targetSource` watches Core KV `vtx.meta.>` directly ([registry.go:386-387, 493-499](../../internal/weaver/registry.go)).
   The target's *rows* are one lens hop (the convergence lens into `weaver-targets`, over whatever entities
   already match). The *grant* is one lens hop through `capabilityRoles`
   ([rbac-domain/lenses.go:36](../../packages/rbac-domain/lenses.go)) into capability-kv, which the Processor
   reads at step 3 as `cap.<actor> ∪ cap.roles.<actor>` for the Weaver system actor
   ([cmd/weaver/main.go:89, 166-168](../../cmd/weaver/main.go)). Each Refractor lens advances its own durable
   independently ([refractor.md:1278-1279](../components/refractor.md) names the property); over nine
   pre-existing accounts the rows won eight times.
3. **Fire-and-forget.** `fire` publishes to `ops.<lane>` with no reply subject
   ([actuator.go:53-61, 113](../../internal/weaver/actuator.go); [evaluator.go:1314-1339](../../internal/weaver/evaluator.go))
   and branches only on the publish error — the Processor's `AuthDenied` is delivered to nobody. The §10.3 mark
   stays; `defaultMarkLease` is 30 min ([reconciler.go:17](../../internal/weaver/reconciler.go)); the sweep's
   `reclaim` re-dispatches at expiry. That 30 min *is* the row's figure, and it is the only **automatic** path:
   a rejected op mints no Contract #4 tracker (`NewTracker` runs on the commit path only,
   [commit_path.go:471](../../internal/processor/commit_path.go); [04-idempotency-tracker.md:45](../contracts/04-idempotency-tracker.md)
   — "always `committed`"), and the ratified decline-retry taxonomy classifies rows Weaver *declines to
   dispatch*, not ops the Processor rejects after a publish (its 17-row table is lane-1 exits only;
   `AuthDenied` has zero hits in the design).
4. **What DOES exist, and my first draft missed:** (a) every step-3 denial writes a Health-KV auth-trace
   `health.processor.<instance>.auth-trace.<requestId>` with `AuthOutcome: denied` and a 1 h TTL
   ([step3_auth_trace.go:159-199](../../internal/processor/step3_auth_trace.go); `commit_path.go:273`), and
   Weaver's requestId is deterministic (`deriveEpisodeRequestID`, `evaluator.go:1315`) — a durable, level-readable
   rejection record outliving the lease; (b) Weaver already holds a Capability-KV reader —
   `controlauth.NewCapabilityKVChecker` for its `ctrl.*` verbs ([cmd/weaver/main.go:191-196](../../cmd/weaver/main.go);
   [controlauth/checker.go:48-54](../../internal/controlauth/checker.go)) — and `natsperm` gates writes only,
   reads are unrestricted ([matrix.go:47-49, 373-402](../../internal/natsperm/matrix.go)); Contract #6 §6.1's
   "Processor reads only" is a write-monopoly sentence that predates the control-plane checker; (c) an
   **operator cure exists today**: `Revoke` deletes every in-flight mark of the target and its durable
   (`marks.deleteByTargetPrefix`, [control.go:191-233](../../internal/weaver/control.go)), `Enable` recreates
   the durable and every row re-delivers markless (`:170-186`) — seconds, not 30 min. `ReplayTarget` alone does
   **not** help: a live mark takes the anti-storm Ack (`control.go:451-452`, `evaluator.go:655-673`).
5. **When it can happen.** Whenever a target's convergence lens projects a violating row before its op's grant
   projects — a property of graph state at install time, not of hot-vs-cold: cold `make up-full` starts
   Weaver *before* `install-packages` ([Makefile:782-785, 1213](../../Makefile)) and is saved only by the graph
   being empty (NATS is ephemeral, the seeds run last). A production install of a new target over existing
   data is the live case.

**Every option, priced.**

| Option | Why not |
|---|---|
| **Do not have this thing** — the lag is bounded (≤ one `MarkLease`, once per new target × op pair over pre-existing rows), self-healing, and cured in seconds by `Revoke`→`Enable` when anyone cares; no Weaver target is on the security plane (grant tables are Refractor lenses) | **Chosen.** The observed instance was arrears *reminders*; the cost of the two mechanisms below is a dispatch-path change or a per-mark sweep read, billed to every future change, for a ★ transient with an instant manual cure |
| **Pre-dispatch held-grant check → the existing config-error decline** (the recorded design if revived): before minting a mark, read the Weaver system actor's held set through the shared `capabilitykv` routing and, if the op is not held at `scope=any`, exit `dispatchGap` through the ratified **config-error** class — Nak on the 5 min floor + a `warning` issue naming the target — which by that design's own rationale "re-evaluates against current config until fixed"; the install race then costs ≤5 min, no mark, no double-dispatch window | The smallest honest mechanism, and it rides an existing class and an existing reader — but it is a second evaluator whose matcher must be the Processor's (`controlauth`'s is `ctrl.*`/scope-any only; the Processor's has class-aware routing and the system-actor union): a false negative turns a live target into a permanent warning-Nak loop, a failure mode that does not exist today. Size M with the adversarial layer. Deferred, not rejected |
| **Sweep-side early reclaim on a denied auth-trace**: `sweepMark` reads `health.processor.*.auth-trace.<requestId>` for a live-lease mark and reclaims at once on `denied` | Cures after a 30-min episode was minted rather than preventing it; needs the Processor instance (or a prefix list) per read, one Health-KV read per in-flight mark per sweep, and a ladder guard so a permanently denied op does not re-fire every minute. Strictly dominated by the row above |
| Install ordering — commit the grant, wait for its projection, then commit the target | Two commits = a Contract #8 change (install atomicity), and `Upgrade`/`Apply` share the batch |
| Weaver consumes the reply / reads the tracker (`lattice.op.status` exists; Weaver lacks the pub-allow, `matrix.go:472`) | The request-reply / outbox shape the actuator design refused, and the tracker records commits only — nothing to read for a rejection |
| A shorter lease for a target's first episode | Timer-shaped; `MarkLease` is sized so a reclaim never double-dispatches a running episode |
| Reorder `up-full` to install packages before starting Weaver | Free and harmless, but it fixes nothing the empty graph does not already fix, and the live case is the hot install |

**Verdict — documented, closed; the pre-dispatch check is the recorded design.** The `_packages.md`
Installation-semantics note (step 5) states the expectation — first attempts before the grant projects,
re-dispatch within one mark lease, `Revoke`→`Enable` as the instant cure right after such an install (when
every live mark is a denied first attempt, so deleting them double-dispatches nothing). **Revive trigger:** a
second sighting, or a target whose first-dispatch latency is *measured* to matter — then build the config-error
exit above (M), never the two-commit install.

## 6. Adjudication

Per the 2026-08-20 delegation the four verdicts are Winston-adjudicated: no architectural fork, no
frozen-contract change (§2 and §3 are package ops/edits building to Contract #1; §4 shelves; §5 documents).
Board rows rewritten in the same commit as this doc; `STRICT=1 go run ./scripts/lint-board.go` green. The
Steward builds §2 and §3 as ordinary `📋 ready` units (S each; review depth per `agents/steward/SKILL.md` §4 —
§3 touches a money-path op and gets the adversarial layer).

## 7. Falsification record

Two read-only agents ran concurrently with the draft, briefed to falsify the claims each verdict rests on
(not to confirm them). What they overturned is folded above; this is the ledger.

**Verticals agent** (live stack reachable; censuses run against it):

| Claim | Verdict | What changed |
|---|---|---|
| W1 — a late cancel tombstones, the no-show twin keeps the vertex; a `cancelled` status is safe for every consumer | **partial** | Tombstone is unconditional (`is_late_cancel` gates only the refund) — the edit is one branch, not a restructure. Four consumers were unsafe as first drafted: `SetBookingAttendance` refuses only `waitlisted` (would re-mark a forfeit `noShow`); `sessions.go:150`/`app.js:1460` count it as a seat and the FE capacity gate would refuse bookings; `ATTENDANCE_MARKS` renders nothing; `app.js:1273` already means "class called off" by `cancelled`. The schema enum is not enforced at commit (`step6_validate.go:53-62`). `front-desk`'s `bookingHistorySpec` was a fifth package the first census missed. `wellness.bookingCancelled` has zero consumers. → token renamed `forfeited`, six must-change sites named, size S+S |
| C4 — re-pointing `appliesToUnit` re-projects the identity's `cafeIdentitiesRead` row | **holds** | Comprehension labels count (`full/labels.go:157-160`); the walk is non-exhaustive (`*0..7`, unlabelled nodes) so `reprojectAll` stays true and the plain auth-plane pipeline re-derives the whole row set on each of the two link events (`anchor_derivation_plain.go:431` refuses the act-mode shortcut on the auth plane). `cafeLeaseWorkplaces` re-projects narrowly (leaseapp is its anchor) |
| C5 — the stranded population is the `missingLocation:true` rows (11); no op re-points; no tombstone guard | **partial** | Live: **10** rows of 56 (one leaseapp since tombstoned), **7** dead units, every link live, every unit `isDeleted` at 2026-08-23T03:03Z. The row itself carries no unit key — the build's census must read `lnk.leaseapp.*.appliesToUnit.unit.*` in core-kv. Writers and guard as claimed |
| L1 — `CreateUnclaimedIdentity` creates on a duplicate and flags it; hygiene surfaces unclaimed pairs; `MergeIdentity` takes a link-less unclaimed secondary; nothing anchors a fresh identity to its minter | **holds** | `ddls.go:1274-1358` (create + `duplicateOf` link + `identity.created{duplicate}`); `duplicateCandidates` admits `unclaimed` on both arms, 12 rows live; `MergeIdentity` refuses only `merged`/missing/self; `identityAnchors` projects no row for a bindingless identity |

**Platform agent** (Weaver install race):

| Claim | Verdict | What changed |
|---|---|---|
| P1 — fire-and-forget; no early notice of `AuthDenied` | **holds** | `fire` is `evaluator.go:1314-1339` (publish error only); the sanctioned tracker RPC `lattice.op.status` exists and Weaver alone lacks its pub-allow — moot, see P4 |
| P3 — `ReplayTarget` cannot heal a live mark | **holds**, framing falsified | Replay touches no mark and its own doc lists the anti-storm Ack as an accepted decline. **`Revoke` deletes every in-flight mark (`control.go:191-233`) and `Enable` recreates the durable (`:170-186`)** — an operator cure in seconds existed and my draft said none did |
| P4 — no tracker, no durable rejection record | **partial** | No tracker (commit path only; Contract #4 "always committed"). **A Health-KV auth-trace is written on every denial**, keyed by the deterministic requestId, 1 h TTL (`step3_auth_trace.go:159-199`) — a level-readable record; the sweep-side mechanism is constructible (§5 table) |
| P5 — hot-install-only | **falsified** as a category | `up-full` starts Weaver before `install-packages` (`Makefile:782-785`); the invariant is graph state at install time, and the cold path is saved by the empty graph, not by ordering. Restart is safe: capability-kv is file-backed and lens durables keep their floors — but NATS has no named volume, so `make down` wipes it |
| P6 — Weaver cannot read capability-kv; the Processor is the sole evaluator | **falsified**, both halves | `cmd/weaver/main.go:191-196` builds `controlauth.NewCapabilityKVChecker`; `natsperm` gates writes only; Contract #6 §6.1's "Processor reads only" is the write-monopoly sentence. What survives: the control checker's matcher is `ctrl.*`/scope-any, not the Processor's — the obstacle is semantic, not access |
| P7 — the decline-retry taxonomy is silent on Processor rejections | **holds** | Zero `AuthDenied` hits; the 17-row table is lane-1 exits; in-flight rows defer to the mark |
| P8 — one atomic commit; no cross-lens ordering | **holds**, one correction | `Install`/`Upgrade` share `buildManifestBatch`; `Apply` delegates. **Target registration is a direct Core-KV watch (zero lens hops)** — structurally faster than the grant's one hop; rows are the lens leg |

Net: three verdicts unchanged in kind and sharpened in content; the Weaver verdict unchanged but its
reasoning replaced (two false premises struck, two constructible mechanisms recorded, the operator cure
documented). A design that had shipped on my first draft would have carried a `SetBookingAttendance` hole and
told operators no cure existed.
