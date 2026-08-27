# Verticals designer triage — the seven `📐 needs designer pass` rows

**2026-08-27, Winston (Andrew-directed session).** Mandate: go over every verticals row tagged
`📐 needs designer pass`, skeptically — is the problem real or imagined? — preferring simple,
package-only resolutions over new mechanisms, especially cross-lane ones. Method: seven read-only
grounding agents, each briefed to **falsify** its row's premise (not confirm it), plus lead
spot-checks of every load-bearing claim before adjudication.

**Outcome: six of the seven rows dissolve to `📋 ready` with no design doc — their `no-pattern:`
claims are falsified by shipped precedent.** The seventh (the reads drift-guard) consolidates into
one Lattice-lane row because its smallest honest mechanism is a ~30-line Processor test seam. No
architectural forks, no frozen-contract changes surfaced — the whole pass is Winston-adjudicated
under the 2026-08-20 delegation.

| Row | Filed premise | Verdict |
|---|---|---|
| descriptorform vocabulary (§2) | 6 shapes / ~13 ops stranded, no precedent | Stale both directions: 2 gaps shipped, 1 op unblocked, computed-reads dissolves into `derive_reads`; residue = precedented increments + 2 field kinds specced here → **📋 ready** |
| Wellness guest search (§3) | needs an un-anchored identity lens (security fork) | Falsified: anchored multi-arm lens + create-not-search covers it → **📋 ready** |
| Task-card discriminator (§4) | needs entity-key-to-name resolution | Single-consumer today; FE-side join suffices; platform shape recorded with a revive trigger → **📋 ready** |
| Profile terminal-state (§5) | needs a guard pattern vs renewal reuse | Guard shape empirically falsified already; snapshot-at-decision resolves it, package-only → **📋 ready** |
| Reads drift-guard (§6) | needs a helper-aware static extractor | Static extractor is the expensive wrong shape; dynamic actual-reads guard needs one small Processor test seam → **consolidated into lattice.md** |
| Café front-desk definition (§7) | needs "one definition of front-desk staff" | The one definition exists and is enforced everywhere but the app hat; 3-line-shaped app fix ×3 apps → **📋 ready** |
| Dispatch reads (§8) | needs a link-key reads template | The template grammar already ships in 5 packages; census re-run: zero ops need the missing engine primitive → **📋 ready** |

## 1. The recurring cause, named once

Five of seven `no-pattern:` tags were filed from **instance-scoped build-fire grounding that went
stale** — in two cases falsified by the *filer's own design's later increments* (`contextParams`
shipped in Inc 3d five days after §17 recorded it missing; the link-key read template the
SetAppointmentStatus row asks for was already shipping in five packages, including the same file's
`RemoveProviderSite`). A `no-pattern:` claim is a census nobody re-ran. This pass re-ran them.

## 2. `internal/descriptorform` vocabulary (was: "can't express 6 op shapes, ~13 ops stranded")

**Corrected census: ~10 shapes, ~23 stranded ops — but the design surface shrinks, not grows,
because most shapes have shipped precedent and the one "genuine design core" dissolves.**

Already false in the row:

- **`contextParams` SHIPPED** end-to-end in `77aab58c` (Inc 3d): `packages/edge-manifest/lenses.go:742`,
  all four apps' `op_catalog.go:52/:98`, `form.mjs:463/:494/:560-566`, drift-gate markers.
- **`ClassChoices` SHIPPED** (`form.mjs:252-279`) — form.mjs is *ahead* of Facet here.
- **"Auto-derived field" is not a shape** — `CreateStudio` already declares
  `ContextParams: {"location": "{me.workplace}"}` (`packages/wellness-domain/opmetas.go:510`); the
  need collapses into the typed self-anchor template. Café's `CreateMenuItem` just needs the same
  one-line `ContextParams` declaration first.
- **`StartVisitSeries` is unblocked today** — §15's "carries no descriptor at all" is false:
  `packages/clinic-reminders/visitseries.go:1104-1136` is complete and every template/field kind it
  uses renders in `form.mjs` now. Lift it out and migrate it.

**The computed-reads "design core" dissolves into `derive_reads` (Contract #2 §2.5 class (g),
shipped, package-open).** The fork the grounding surfaced — a declarative generator vocabulary
(duplicates script constants into descriptors) vs. a caller escape hatch (re-admits hand-built read
logic) — is a false fork: the platform already has the third option. A DDL MAY define a pure
`derive_reads(op)` over `{operationType, actor, payload}` returning `{"reads", "optionalReads"}`,
merged server-side before hydration (`internal/processor/derive_reads.go`; precedent consumers:
identity-domain, identity-hygiene). Slot-cell enumerations are pure arithmetic over payload fields —
exactly class (g). Consequences:

- wellness `CreateSession`/`CreateSessionSeries`/`ReassignSession`, clinic
  `CreateAppointment`/`RescheduleAppointment` declare their cell claims via `derive_reads` in their
  own DDLs; descriptors and clients stop carrying them entirely.
- The FE mirrors (`slotCellKeys`/`occurrenceCellKeys`/`slotClaimKeys`,
  `cmd/wellness-app/web/app.js:2034-2044`, `cmd/clinic-app/web/app.js:2153-2170`) — the audit's
  worst sub-pattern ("server logic re-implemented client-side to hand-declare reads") — get deleted.
- Seat/waitlist keys need **nothing**: `CreateBooking`'s posture is already correct server-side
  (capacity read on demand; the lock is CreateOnly key-collision at commit,
  `packages/wellness-domain/ddls.go:538`, `opmetas.go:181-189`) — the FE over-declares today.
- Ceiling: derived keys count toward `opwire.MaxDeclaredReads = 1000`; slot-cell sets are ≤96. A
  pathological series exceeding 1000 faults at step 4 — the identical exposure the FE-declared path
  has today, so no new hazard.

**Work-list (each independently shippable; precedents named; Steward sizes review depth):**

1. **Array-refusal guard, first** — `{"type":"array"}` falls through `fieldKind()` to a free-text
   box and would submit a *string* (`form.mjs:76`): a live silent-wrong-submit hazard. Two lines in
   `normalizeCatalogRow` to refuse until a real array kind exists.
2. `StartVisitSeries` migration (nothing missing).
3. Multiline text kind — mirror Facet `app.js:2515-2516` (`maxLength > 120`) + add `maxLength` to
   `RecordEncounter`'s three fields + drift-gate vocabulary entry (three-renderer call: Swift
   deliberately declines multiline, `DescriptorForm.swift:15-17`). **Must ship with per-field
   conditional visibility** (spec below) or `RecordEncounter`'s follow-up-date toggle regresses.
4. `{entity.<column>}` alias onto the existing `{context.<field>}` path (`form.mjs:382`) — two
   lines + the naming-fork call (adopt the alias in the module; don't fork package spellings).
5. `visibleWhen` op-level evaluator — `context.row[field] === equals`; column already plumbed
   (`op_catalog.go:64`), caller precedent `cmd/loftspace-app/web/app.js:2108-2118`. Unblocks the
   three visit-series ops.
6. Ceremony plumbing — three layers, all precedented: `opCatalogSpec` gains the ceremony columns
   `edgeCatalogTail` already projects (`packages/edge-manifest/lenses.go:671-673`), the four proxies
   mirror the `contextParams` plumbing, the module mirrors Facet (`attachments.mjs` already proves
   crypto belongs in this module). Unblocks `CreateUnclaimedIdentity` (and §3's guest-create form).
7. Typed self-anchor `{me.<type>}`(±`:id`, ±`?`) — module half mirrors Facet `app.js:2553`; the app
   half needs the staff apps' whoami to expose `selfAnchors` (the lens already projects it,
   `packages/edge-manifest/lenses.go:508-527`). Includes the OpenTab per-surface question: resident
   self-path uses `{me.leaseapp}`, staff POS picks from a `<select>` — the staff path simply stays
   hand-built (one-click category), no per-surface override mechanism needed.
8. `derive_reads` adoption per §2 above (wellness + clinic packages; FE mirror deletion).
9. Template-grammar convergence note: `form.mjs` throws on `{me.<type>}`/`{entity.*}`/`{service}`/
   `{scopedTo}` that the processor and/or Facet accept — items 4/7 close the throwing forms live
   descriptors actually use; the drift gate should then pin the three-way vocabulary
   (`lint-facet-renderer-drift.go:58-108` currently sees 7 of the ~10 kinds).

**Two field kinds genuinely had no precedent — specced here so they are build-ready, not
design-blocked:**

- **Per-field conditional visibility.** Schema: `x-visibleWhen: {"field": "<sibling>", "equals":
  <json-value>}` on a field's schema (mirrors the op-level `visibleWhen` vocabulary). Renderer: on
  every input/change event of the named sibling, toggle the dependent field's container
  `hidden`; a hidden field contributes nothing to the payload at `read()` (drop, don't send
  empty-string); a hidden-but-required field is exempt from required-validation *while hidden*.
  No chaining in v1 (a `visibleWhen` naming another conditional field is refused at
  `normalizeCatalogRow` — fail loud, the module's house rule). Consumer: `RecordEncounter`'s
  `followUpDate` (visible when `followUpNeeded == true`).
- **Array-of-objects field kind.** Schema: `{"type": "array", "items": {"type": "object",
  "properties": {...}}}` with optional `minItems`/`maxItems`. Renderer: a repeatable fieldset —
  one bordered row per item rendering `items.properties` through the existing `buildField`
  vocabulary (kinds compose; no nested arrays in v1 — refuse), an "Add" button, a per-row
  "Remove"; `read()` returns the array of per-row payloads, skipping fully-empty rows;
  `minItems`/`maxItems` enforced at read. Consumers: `SetProviderHours` (`windows`),
  `SetProviderTimeOff` (`ranges`). The stateful-draft precedent is clinic's hand-built editor
  (`app.js:1436-1483`) — the module version is the same DOM shape minus app state.

**Explicitly descoped, with revive triggers:** the scheduling/slot-picker widget (S8 — needs an
availability *data contract* the module can't invent; revive when a second booking-calendar
consumer appears) and compound/multi-op submission (S10 — above the module's single-envelope
contract; `CreatePatient` keeps its thin hand-built two-op wrapper, which the ceremony increment
already makes mostly shared code). These are contract-of-the-module decisions, not backlog debt.

## 3. Wellness guest search (was: "needs an un-anchored staff identity lens — security-relevant")

**Premise falsified on both halves of its own sentence.**

- "No anchor any staffer's grant resolves against" is false for a returning guest:
  `booking -bookedBy-> identity` and `session -atLocation-> location` are minted unconditionally
  (`packages/wellness-domain/ddls.go:3505-3520`, `:2303-2321` — the session snapshots its studio's
  locations at create time precisely so tombstones can't strand it), and the staffer's grant token
  is a building NanoID (`packages/service-location/lenses.go:199-207`). One added pattern-
  comprehension arm on `wellnessIdentitiesReadSpec` (`lenses.go:436-443`) closes the walk:
  `+ [(i)<-[:bookedBy]-(bk:booking)-[:forSession]->(se:session)-[:atLocation]->(pl)-[:containedIn*0..7]->(c) | nanoIdFromKey(c.key)]`.
- The audit searched for an *un-anchored* precedent (correctly finding none) when the needed
  precedent is an *anchored multi-arm* one — shipped three weeks earlier in
  `applicantRosterReadSpec` (three arms, `packages/loftspace-domain/lenses.go:257`, `5280967a`), and
  the wellness lens under scrutiny is already two-arm. Clinic's `atSite` fallback added two more
  multi-arm shapes four days after the audit.
- **First contact is a create, not a search.** wellness-app has no create-guest form at all — the
  raw-key copy-paste hop is the create-side defect. `CreateUnclaimedIdentity` is already granted to
  `frontOfHouse` (`packages/identity-domain/permissions.go:47-51`); clinic-app's new-patient modal
  (`cmd/clinic-app/web/app.js:752-830`) is the verbatim FE precedent. The create flow returns the
  key directly — no `registeredAt` link, no zero-booking search arm needed (deliberately: a
  persistent location anchor on an identity vertex is the one move here that would deserve a real
  security conversation).
- The multi-arm scopes disclosure *tighter* than any un-anchored lens could: a staffer at building A
  learns nothing about a guest who only attends building B — and the front desk already sees the
  guest's booking row, key, rate and attendance (`cmd/wellness-app/bookings.go:199-227`); the delta
  is the name, which the roster card already tries and fails to render (the standing bare-NanoID D4
  violation this fix also closes).

**Work-list:** (a) create-guest form in wellness-app mirroring clinic's (FE-only; folds into §2's
ceremony increment or ships hand-built first, mirroring clinic's current shape); (b) the anchor arm
+ `wellness-domain` bump + `lens_cypher_test.go:773-824` fixture update (the no-lease case becomes
no-lease-and-no-booking) + `?q=` filter on `cmd/wellness-app/identities.go` mirroring
`cmd/clinic-app/patients.go:66-71` + debounced picker replacing `index.html:66`'s raw-key input.
Passing finding, fix in the same fire: `cmd/loftspace-app/search.go:48-50`'s "WildcardAnchor-only"
comment has been false since `5280967a` — a stale authorization claim.

## 4. Task-card discriminator (was: "needs entity-key-to-name resolution for cards")

The gap is real (two same-op tasks render identically; D4 correctly forbids the old key spans) and
the row's negative claim held up: arbitrary-typed lens-side resolution is genuinely inexpressible
(cypher needs literal relation names per hop; `SecureColumns` requires static `HolderTypes`,
`internal/pkgmgr/bucketguard.go:233-234`), and every shipped name-resolution lens is fixed-type.

**But the platform mechanism is single-consumer today, so the demand-side fix wins.** The correct
end-state — label-at-creation (an optional caller-supplied `label` on `CreateTask`, threaded through
`GapActionSpec`/`userTask` as a row-templated field, mirroring row 27's ratified
snapshot-at-mutation shape) — touches `orchestration-base` + `internal/weaver/strategist.go:190-244`
+ `internal/loom/engine.go:1000-1049` + two packages' targets: a cross-lane build justified only
when a consumer that *can't* do per-type joins needs it (Facet's task pane is the named revive
trigger; the design shape above is the record for that day).

**The fix now (FE-only, XS):** loftspace's task card renderer
(`cmd/loftspace-app/web/app.js:1921-1964`) joins `t.scopedTo` against data the app already loads —
a `leaseapp`-scoped task labels from the applicant's own application row (unit address, the same
label `renderApplicationCard` shows); an identity-self task needs no discriminator; any type the app
holds no data for keeps today's op-name + due-date card (no worse than current). No new state, no
platform surface, D4-compliant.

## 5. Applicant-profile terminal state (was: "profile-terminal-state vs renewal reuse")

The 2026-08-23 grounding (`a9656c27`) already *empirically* falsified the guard shape — implemented,
tested, reverted: renewals live off the original leaseapp's `.applicationSignals`, whose **sole
writer** is `SetApplicantProfile`, and renewal-eligible leaseapps by definition carry `.decision`,
so any `.decision`-keyed guard categorically breaks every renewal. The softened
"unless a renewal is open" variant still rejects the exact regressed pattern (profile written
between decision and `OpenRenewal`, a gap that can span the whole lease term) and needs an
unbounded write-path link walk. No guard variant closes this row.

**Resolved shape — snapshot-at-decision (package-only, `packages/lease-signing`):**
`DecideLeaseApplication` stamps a new create-only aspect (e.g. `.decidedProfileSnapshot`, SENSITIVE,
same `underwritingRecord` custody class as `.profile`) copying the three profile aspects' fields as
they stood at decision time — mirroring the op's own `.tenancy` read-then-create-only idiom
(`scripts.go:796-827`). **On the first `.decision` write of either value** — a decline is the more
fair-housing-salient case, and a declined leaseapp stays just as rewritable. `SetApplicantProfile`
stays an unconditioned upsert; zero renewal surface touched. `decidedAt` is the right anchor
(nothing records "when the landlord looked"; submitted-time is ambiguous under re-submission).
Residual, deliberately not built here: no Secure Lens exists over the `underwritingRecord` class at
all (`retention.go:44-47`), so the preserved record is captured, not yet inspectable — the reader
fire (an authorized fair-housing review surface) is its own item, filed by the PO when that consumer
is real. Preservation now is what this row promises.

## 6. Reads drift-guard (was: "helper-aware drift-guard") — consolidated into lattice.md

The 2026-08-22 grounding correctly refused the regex port; this pass grounds what the honest
mechanism costs. Facts: the runtime records **nothing** about what a script actually read (the lazy
fallthrough at `internal/processor/starlark_kv.go:115-136` charges an anonymous counter; the
read-posture lint checks annotations exist, never that they're true); helper indirection is the
**norm** in vertical packages (`require_live_typed` in four packages, `prepare_booking_common`,
`vertex_alive_of_class`), so any static extractor short of real call-graph + string-flow analysis
reproduces the false-confidence failure the row exists to kill — the "S" static-extractor sizing was
optimistic.

**The smallest honest guard is dynamic:** record the actually-read key set on `ScriptContext`
(a `map[string]struct{}` populated at the ~4 return points of the `kv.Read`/`kv.Links` builtins —
the exact plumbing shape `LiveReads`/`SensitiveReads` already ship twice), expose it through
`internal/testutil`'s pipeline driver, and each dispatch package's **existing** e2e tests assert
`actual ⊆ declared ∪ sanctioned(op)` — where `sanctioned(op)` is a small per-op allowlist naming the
script's annotated class-(c)/(e) live reads, reviewable in diffs. `privacy-base`'s
`TestPurgeDeclaredReadSetMatchesThePatternStep` already closes the descriptor⟷fixture leg. Coverage
is bounded by what tests exercise — and never over-claims, which is the property the regex lacks.
The static extractor is deferred and, if ever built, scoped *from* what the dynamic guard reveals.

Because the seam is `internal/processor`/`internal/testutil` and the assertions are package test
files, this is **one Lattice-lane fire** (the seam without the assertions is dead scaffolding; the
assertions without the seam are impossible). The verticals row is folded into the new lattice.md
row rather than left as a blocked tracking stub.

## 7. Café front-desk definition (was: "one definition of front-desk staff")

**The one definition already exists and is enforced identically on both backend sides:**
`holdsRole frontOfHouse` AND `worksAt` — the `cap-read.staff` grant lens
(`packages/service-location/lenses.go:199-207`, its own commit message calls the conjunction "the
security argument") and every café/clinic/wellness op grant (`GrantsTo: [operator, frontOfHouse]`;
`worksAt` is deliberately topology-not-authority, `packages/service-location/permissions.go`).
A worksAt-only staffer holds **zero** POS op grants — every write 403s — so the app surface the row
describes is dead UI plus an unnamed read grid, and the 3-of-10 role-less `worksAt` identities are
plausibly intentional personas (the seeder's `backOfHouse` maintenance tech creates exactly that
shape on purpose; minting them `frontOfHouse` would mis-promote non-front-desk staff into
resident-PII authority).

The only layer not checking the role is the app-side `isStaff()` — `len(workplaces) > 0`,
byte-identical in **all three** apps (`cmd/cafe-app/readauth.go:67/:137`,
`cmd/clinic-app/readauth.go:76`, `cmd/wellness-app/readauth.go:95`) — an omission from the F6 fire's
scope, not a decision. The role fact is already on the wire: `/v1/actor` returns `Roles[]` and every
app already parses it for the operator check; `frontOfHouse`'s key is a pure derivation
(`"vtx.role." + pkgmgr.RoleID("identity-domain", "frontOfHouse")`).

**Work-list (app-Go, all three apps, one fire):** add a `frontOfHouse` predicate to `subjectHats`
mirroring `isOperator`; gate the front-desk grid/POS/roster surfaces on it (leave any
backOfHouse-relevant surface, e.g. maintenance, on its own role when one exists). No lens change, no
new grant, no seed edit. Rejected: dropping the lens conjunct (would hand every co-located
instructor/tech the residents' names in three verticals — the documented "security heart" exists to
prevent exactly this); granting the role to the 3 (mis-promotion); a role-projection lens (the fact
is already on the wire).

## 8. Dispatch reads (was: "no-pattern: link-key reads template")

**The tag is falsified: the link-key reads template is an established, five-package pattern**, mid-
template `:id` splicing included (`internal/processor/descriptor_floor.go:785-868`; shipped
examples: clinic `RemoveProviderSite` `lnk.provider.{payload.provider:id}.practicesAt.building.{payload.building:id}`,
wellness `TombstoneSession`, orchestration-base `ClaimTask`, service-domain ×2, lease-signing).
Census re-run (the row's "20 across 4" is stale): **25 Dispatch ops across 6 packages lack
`Dispatch.Reads` — 18 are class A** (target+actor only, functionally covered by `form.mjs`'s
auto-push fallback; declarations are §2.5 hygiene), **7 are class B** (need declarations the
existing grammar expresses), **0 are class D** (nobody needs a type-agnostic segment).

- **SetAppointmentStatus's real break is the InputSchema, not the reads:** its `status` enum is
  already `["cancelled"]` — terminal-only — while `provider`/`patient`, which every terminal
  transition requires (`ddls.go:2956-2963`), are missing from the schema, so a descriptor-driven
  client cannot render or submit them. Since the descriptor's own enum scopes the surface to the
  terminal transition, declare `provider`/`patient` **required** — honest for the described surface,
  no lint change needed (`checkReadTemplates` passes), and `RescheduleAppointment` (identical
  four-read set, schema already correct) proves the whole shape ships today.
- CreateBooking is class B for its blocker (one missing `.schedule` read); its computed
  optional-reads tail is §2's `derive_reads` item, and is an optimization, not a break (the FE
  over-declares; the server posture is already correct).
- **The lattice-lane Reads-template row is distinct and correctly scoped** — its "possibly =
  SetAppointmentStatus's row, unconfirmed" resolves to **not the same**: only
  `AttachObject`/`DetachObject` (D7, no `Dispatch` at all — outside this census's population) need
  the type-agnostic segment.

**Work-list (package-mechanical, one sweep fire):** widen `SetAppointmentStatus`'s schema + declare
its four reads; declare the other six class-B ops' reads; class-A hygiene declarations ride along.

## 9. Adjudication

No row in this pass carries an architectural fork or a frozen-contract change; per the 2026-08-20
delegation the verdicts above are **Winston-adjudicated** and the board rows are re-scoped
accordingly. The one cross-lane output is the §6 consolidation (a test-observability seam — no
contract surface). Row 22 (`📋 designer` — the Facet offline-demo fenced control surface) is a
different tag and a genuine design item (Sally + FE + platform, UX §6 frames it); deliberately not
folded into this pass.

Lesson captured (memory + this doc §1): a `no-pattern:` tag names the census someone ran once, at
build-fire altitude, against that instant's tree — the designer pass re-runs it against today's
tree before designing anything, because this codebase ships the missing pattern at a rate that
outruns its own board.
