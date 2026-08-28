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

## 10. Build note — item 6, ceremony plumbing (2026-08-28)

Fire brief for §2 work-list item 6. Scope sentence: *the shared descriptorform module and the four
staff apps' op-catalog proxy carry an op's mint-and-reveal ceremony end to end, so a catalog-driven
`CreateUnclaimedIdentity`/`RotateClaimKey` form no longer needs a hand-built client.*

**Verified touch-list:**

- `packages/edge-manifest/lenses.go:671-673` — **premise corrected during the build**: those lines are
  `edgeCatalogTail`'s RETURN, not `opCatalogSpec`'s. `opCatalogSpec` (the lens the four staff apps
  actually read) began at line 721 and carried none of the three ceremony columns — `7bd18e4a` is an
  unrelated `CreateLocation`/`ClassChoices` fix; the ceremony columns landed on `edgeCatalogTail` alone
  in `0e4911d8`. Layer 1 of the triage's "three layers" was **not** already done. Fixed as part of this
  item: 3 columns added to `opCatalogSpec`'s RETURN, `edge-manifest` version-bumped 0.17.4→0.17.5, a
  mutation-checked assertion added to `lens_cypher_test.go`.
- `cmd/{cafe,clinic,loftspace,wellness}-app/op_catalog.go` — `opCatalogProjection` carries no
  ceremony fields today (checked all four; two are byte-identical, the other two differ only in
  doc-comment wording and a local KV-get helper). Add 3 fields + a `Ceremony *opCeremony` on
  `opDescriptor`, mirroring the existing `DispatchContextParams` plumbing (same optionality: omit
  the whole object when `MintedSecretHashField == ""`).
- `internal/descriptorform/form.mjs` — no ceremony support at all today. Needs: a
  `ceremonySupported()` gate mirroring `cmd/facet/web/app.js:1991` (refuse to OFFER the op when the
  runtime lacks `crypto.subtle`, per `OpCeremonySpec`'s Go doc contract — never fall back to
  rendering the hash field); mint+hash mirroring `app.js:1998-2012` (32 CSPRNG bytes → hex
  plaintext, sha256 hex digest — `attachments.mjs` is the module's existing precedent that crypto
  belongs here, though its own `deriveNanoID` is a different primitive and not reused directly);
  exclude the hash field from the rendered field list (same filter `targetField`/`contextParams`
  fields already get); `submit()` becomes async and returns the envelope plus a pending reveal
  (`{title, help, plaintext}` or none) rather than the envelope alone — a **caller-visible contract
  change**, not additive, because every existing caller destructures the return today.
- 12 `handle.submit()` call sites across the four apps' `web/app.js` (cafe ×3, clinic ×5, loftspace
  ×1, wellness ×3) all need `await` plus a reveal check gated on an **affirmative**
  `reply.status === "accepted"` — the cold adversarial review at admit caught a first pass that gated
  on the weaker "not rejected" (see Outcome below). loftspace already has a claim-secret modal
  (`#claim-overlay`); the other three have none. Rather than four divergent hand-rolled modals, the
  module exports one self-contained `showCeremonyReveal(title, help, plaintext)` plus a
  `revealCeremonySecret(reveal, reply)` that decides and performs the reveal in one place for all
  four apps — its own DOM/inline-style overlay appended to `document.body`, no dependency on a host
  app's modal markup (the same self-containment `attachments.mjs` already follows) — so the 12 call
  sites stay mechanical.

**In-scope gotchas:** `submit()`'s return-shape change is a real breaking change to every existing
caller, not a purely additive one — every call site must be updated in the same commit or the build
fails; there is no partial-adoption state. The reveal must never show for anything short of a
confirmed commit (mirrors Facet's own ceremony). Ceremony-unsupported runtimes must refuse to OFFER,
not degrade to a raw hash textbox (`OpCeremonySpec`'s normative contract,
`internal/pkgmgr/definition.go:676-699`).

**Non-goals:** migrating loftspace's existing hand-built `submitNewApplicant` ceremony onto the
catalog-driven path — that stays hand-built (its own form UI, name/email/phone fields the schema
doesn't map 1:1); this item only unblocks the catalog-driven surface (a future task-based
`CreateUnclaimedIdentity`/`RotateClaimKey` completion, and §3's guest-create form) from having to
hand-roll ceremony support again. No change to `packages/identity-domain` — its `OpCeremonySpec`
declarations are already correct and unchanged.

**Outcome (shipped `<pending-sha>`):** built as scoped, plus the lens-premise fix above. A cold
adversarial security review at admit found the first pass gated the reveal on `status !== "rejected"`
— which a Gateway reply-timeout (HTTP 202, no `status` field — `internal/gateway/gateway.go:535-545`)
and a `duplicate` reply both satisfy without the write being confirmed, so either could have shown a
person a secret for a write that never landed. Fixed to gate on `status === "accepted"` exclusively,
centralized in the module's new `revealCeremonySecret` so no host app re-derives the check; 9
mutation-proven regression vectors added (reverting the gate to the weaker check fails 6 of them).
Also added: a `ceremony` member to `scripts/lint-facet-renderer-drift.go`'s vocabulary table (the
gate had no coverage for this vocabulary at all — Swift is exempt, the shelved macOS-proxy spike
reaches no ceremony-bearing op). Two residuals surfaced and were NOT built here — both pre-existing,
neither introduced by this change: (a) Refractor's live-Core-KV re-evaluation assumes read-after-write
monotonicity that a clustered (R>1) `core-kv` stream's direct-get could violate — the current
deployment is R=1 so this doesn't bite today, but nothing pins it; a Lattice-lane design question if
the platform ever clusters that stream. (b) a lens's aspect-column read order in its RETURN clause is
memoized first-read, so a same-batch op-meta *upgrade* that reorders which aspect a script reads first
could in principle produce a torn intermediate — shared with the pre-existing `sensitive` column, no
test pins the order today. Neither blocks this item; noting them here rather than filing a row for
each, since both are cross-cutting platform observations, not vertical-app demand.

**Review depth:** security-plane change (client-side secret minting/reveal) — full 3-layer
adversarial at admit regardless of size, per §4 of the Steward routine.

## 11. Build note — item 7, typed self-anchor `{me.<type>}` (2026-08-28)

Fire brief for §2 work-list item 7. Scope sentence: *`internal/descriptorform/form.mjs`'s
`substituteTemplate` stops throwing on `{me.<type>}` and adopts the `?` OPTIONAL marker, mirroring
Facet's own `selfAnchorKey`/`templateIsOptional` and `internal/pkgmgr/definition.go:722-781`'s
normative contract for both.*

**Verified touch-list:** `internal/descriptorform/form.mjs` — `substituteTemplate` gains an
`expr.startsWith("me.")` branch resolving against a new `selfAnchorKey(context, type)` helper
(filters `context.selfAnchors`, a `{type,key}` array, to the single match — zero or several
resolves to `undefined`, never a guess); the contextParams loop gains `templateIsOptional`/
`stripOptionalMarkers` (mirroring Facet's `app.js:2288-2296`) so an optional param that doesn't
resolve is omitted from the payload rather than throwing. `internal/descriptorform/form.test.mjs`
— the now-false `"an unadopted \`?\` optional marker refuses"` test replaced with the real silent-
omit behavior; four new tests added (resolve-to-single-match, refuse-on-0-or-2+ for a required
template, omit-on-0-or-2+/fill-on-1 for an optional one, `:id` composition).

**Ground check:** `context.selfAnchors`'s shape (`{type,key}`, degenerate `{key:null}` dropped
client-side) verified live against `packages/edge-manifest/lenses.go:508-571`'s `edgeIdentitySpec`
— found its own doc comment stale (said "five types ship", omitting the already-shipped `patient`
type `CreateAppointment`'s `{me.patient}` addresses); corrected in the same commit.

**Non-goal, deliberately:** wiring `selfAnchors` into any of the four apps' `/api/whoami` and
migrating a live op (OpenTab, CreateBooking/JoinWaitlist/ReassignSession/SetBookingAttendance,
CreateAppointment — all of which already declare `{me.<type>}`/`{me.<type>?}` ContextParams per a
live grep of `packages/*/opmetas.go`) onto `renderOpForm`. Checked live: every one of those ops is
still rendered by hand-built app code today (`cafe-app`'s `OpenTab` submit, `wellness-app`'s
`bookMemberIn`/`SetBookingAttendance` handlers, `clinic-app`'s `CreateAppointment` submit) — none is
broken by form.mjs's prior gap, since none goes through form.mjs yet. Wiring whoami plumbing with no
caller would be exactly the premature-abstraction CLAUDE.md rules against; this item closes the
module-side gap so a *future* per-op migration (§2 item 9's drift-gate convergence, or a vertical PO
row asking for one of these forms to gain the module's other benefits — ceremony, conditional
visibility, etc.) is unblocked without inventing unused code now. Revive trigger: the first PO/build
item that proposes migrating one of the six named ops onto `renderOpForm`.

**Outcome (shipped `634cf8d4`):** built and tested exactly as scoped; `node --test
internal/descriptorform/*.test.mjs` 78/78 green (was 74 before this item — the replaced test still
counts once). `go build ./...`, `make vet`, `STRICT=1 lint-conventions`, `golangci-lint run ./...`,
`make verify-kernel` all clean. Non-security, non-capability-plane, mechanical + fully precedented
against Facet's shipped implementation and the Go-side contract doc — lead review at admit (XS/S
per §4), not full 3-layer.

**Item 7 is CLOSED.** §2's remaining work-list items: 8 (`derive_reads` adoption) and 9 (template-
grammar convergence note / drift-gate vocabulary entry, which can now cite items 4 and 7 both closed).

## 12. Build note — item 8, `derive_reads` adoption (2026-08-28)

Fire brief for §2 work-list item 8. Scope sentence, as re-grounded during the build: *wellness-domain's
`CreateSession`/`CreateSessionSeries` and clinic-domain's `CreateAppointment`/`RescheduleAppointment`
declare their studioSlotClaim/instructorSlotClaim/providerSlotClaim/patientSlotClaim cells via the DDL's
own `derive_reads(op)`, so the four hand-built app.js dispatchers stop computing them.*

**Scope correction found during the build: `ReassignSession` is excluded, not adopted.** `derive_reads`
runs pre-hydration with `kv`/`state`/`ddl` hard-stubbed to fail on any access (Contract #2 §2.5) — a pure
function of `{operationType, actor, payload}` only. `ReassignSession`'s "edit only what's supplied, carry
the rest forward unchanged" semantics mean a studio-only move, an instructor-only swap, or a time-move
that doesn't touch the instructor all need the session's CURRENT studio/instructor/schedule to compute
which cells to claim — state genuinely absent from the payload in each of those shapes (verified against
every branch of `ReassignSession`'s `execute()`, not assumed). No payload shape makes the op fully
derivable. The FE (`cmd/wellness-app/web/app.js`) keeps declaring its optionalReads for this one op;
`slotCellKeys` stays in place (still used there and by three other call sites). `CreateAppointment` and
`RescheduleAppointment` have no such "carry forward unchanged" branches — both required fields the cells
key on (`provider`/`patient`/`startsAt`/`endsAt`) are always present in payload — so both migrate cleanly.

**Cold adversarial review (opus, independent of the implementer) verified the derived read-sets are exact
supersets of every `kv.Read` the scripts' `claim_cell` actually calls** — RFC3339 normalization parity,
the `CreateSessionSeries` occurrence-offset loop byte-matched against `execute()`'s own, bounds mirroring
`required_int`'s `[2,52]`/`[1,365]`, and the `ReassignSession` exclusion's premise (it also found the
exclusion was slightly *under*-stated: even a call supplying both `newStudio` and an explicit
`startsAt`/`endsAt` still resolves its FINAL instructor from a KV-read `old_instructor` fallback when the
instructor itself isn't also named — folded into the doc comment). Two real findings, both fixed in the
same commit: raw `getattr` where the scripts' own `optional_string` trims + type-checks (a
malformed/whitespace payload would otherwise `DeriveReadsInvalid` an operation `execute()` would have
accepted — objects-base's own `derive_reads` sets the `optional_string` precedent, now followed here too);
and `derive_reads` calling the `fail()`-raising `slot_cells` past the 24h/96-cell ceiling instead of
bounding and deferring to `execute()`'s own clean `SessionTooLong`/`AppointmentTooLong`. Also swept four
now-stale doc comments (two in `wellness-domain/opmetas.go`, one each in the two packages'
`claim_cell` functions) that asserted the old "a descriptor-driven caller falls to the commit-time
RevisionConflict" behavior — no longer true for any caller shape.

**Coverage gap closed in the same fire:** `CreateSessionSeries` had no execution test anywhere in the
suite (only permission-table/opmeta references) — its 3-occurrence offset loop had never actually run.
Added its first execution test plus two tests proving `derive_reads` alone (zero client-declared
optionalReads) is sufficient for both the accept and the `StudioConflict`-reject paths. (Also had to add
`CreateSessionSeries` to the test fixture's own granted-permissions list — the op had never been submitted
in this suite before, so nothing had surfaced its absence.)

**Outcome (shipped `883e2875`):** `go build ./...`, `make vet`, `STRICT=1 lint-conventions`,
`golangci-lint run ./...`, `lint-package-version` (wellness-domain 0.22.8→0.22.9, clinic-domain
0.34.9→0.34.10), `make verify-kernel` all clean; `go test` on wellness-domain, clinic-domain,
clinic-reminders, clinic-ledger, wellness-ledger all green. Non-security, non-capability-plane (Contract
#2 §2.5 hygiene, not a new enforcement point) — lead review at admit plus the one cold adversarial pass
above, not a full 3-layer panel.

**Item 8 is CLOSED for `CreateSession`/`CreateSessionSeries`/`CreateAppointment`/`RescheduleAppointment`.**
`ReassignSession` stays client-declared, by design, per the exclusion above. §2's remaining work-list item:
9 (template-grammar convergence note / drift-gate vocabulary entry for `{me.<type>}`/`{entity.*}`/
`{service}`/`{scopedTo}`, which can now cite items 4, 7, and this one).

## 13. Build note — item 9, drift-gate vocabulary entry (2026-08-28)

Scope, re-grounded: only `{me.<type>}` (item 7) and `{entity.<column>}`/`{context.<field>}` (item 4) are
live in any staff-plane descriptor — a census of every `packages/*/opmetas.go`/`ddls.go` found no
`{service}` or `{scopedTo}` template on the form.mjs side, so those two forms had nothing to pin. Added
both as `vocabMember`s in `scripts/lint-facet-renderer-drift.go`. `entityColumn` is Swift-exempt:
`DescriptorForm.swift`'s `DescriptorContext` carries no row/entity field, and its one production call
site (`DescriptorFormSheet.swift:175`) constructs it with only `actorIdentityKey` — no caller anywhere
threads a viewed row in, so the template can never resolve there regardless of source support (unbuilt
scope in the shelved macOS-proxy spike, same class as the existing `ceremony` exemption). `selfAnchor`
needed no exemption — all three renderers carry the case. Also corrected `docs/components/edge-manifest.md`'s
stale "six field kinds plus contextParams" line (already undercounting `textarea`/`ceremony` before this
fire).

**Outcome (shipped `ec63ae71`):** `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 lint-conventions`, `STRICT=1 lint-facet-renderer-drift` all clean. No package/DDL touched (no
version bump). Lead review only — mechanical marker-parity addition to an existing gate, no new
enforcement point.

**§2's work-list (items 1–9) is now CLOSED.** The two specced-but-unconsumed field kinds (per-field
conditional visibility beyond `RecordEncounter`, array-of-objects) stay build-ready with no open row
demanding them — revive when a consumer needs one.
