# Descriptor-declared `kv.Links` enumerations — the missing declaration channel

**Status: ✅ Winston-ratified — build-ready** (2026-08-29). Every open question here is an implementation
call (`agents/steward/SKILL.md` §0); none touches a frozen contract or an architectural fork. Contract #2
§2.5 already specifies `contextHint.enumerations` and does **not** enumerate which surfaces may populate it,
so adding a fourth populating surface builds *to* the frozen contract rather than changing it.

Board row: `[Pkgmgr] A descriptor-dispatched op cannot declare a kv.Links walk`
(`backlog/lattice.md`, Component maintenance).

## 1. The gap

Contract #2 §2.5 class (e) sanctions a bounded, paged, live `kv.Links` walk **provided the walk is declared**
as `{hub, relation, direction}` in `contextHint.enumerations`. Three surfaces can populate it today — four
declaration *sites*, since Weaver validates a gap's own entry and a goal-catalog entry's separately
(`ActionCatalogEntrySpec.Enumerations`, `internal/pkgmgr/definition.go:617-621`, through the same
`validateGapEnumerations`):

| surface | hub grammar | resolver | validation |
|---|---|---|---|
| Loom `StepSpec.Enumerations` | `subject`, `subject.<aspect>` | `systemOpEnumerations` → `resolveSubjectTemplate` (`internal/loom/engine.go:932`) | `validateEnumerations` at pattern load (`internal/loom/pattern.go:149`) |
| Weaver `GapActionSpec.Enumerations` | literal or `row.<column>` | `resolveReadKey` (`internal/weaver/strategist.go:356`) | `validateGapEnumerations` at install **and** engine load (`internal/weaver/registry.go:774`) |
| a hand-built envelope / a client posting through the Gateway or Loupe | concrete key | — | `opwire` envelope parse (`internal/processor/opwire/opwire.go:307`) |

`pkgmgr.OpDispatchSpec` — the op meta's machine-readable submission recipe, and the surface every ordinary
descriptor-driven client dispatches from — carries `Reads` and `OptionalReads` but **no `Enumerations`**
(`internal/pkgmgr/definition.go:872`). An ordinary descriptor-dispatched op therefore cannot declare a walk
at all. The read-drift census measured the consequence: the baseline carries **139** walk shapes
(`grep -c "^walk" internal/testutil/read_drift_baseline.txt`, re-run live), and the census attributes all of
them but the three ops that declare through Loom/Weaver/a hand-built envelope to having no declaration
channel — a missing vocabulary, not script debt
(`internal/testutil/read_drift_baseline.txt`, the "WHAT THE WALK ROWS ACTUALLY ARE" comment block;
`docs/reviews/verticals-designer-triage-2026-08-27.md` §"no declaration channel exists").

## 2. What this builds

The fourth surface, end to end, and one real op family declaring through it so the channel is not inert
plumbing:

```
OpDispatchSpec.Enumerations          (package definition, validated at install)
  → opDispatchBody                   ("enumerations" on the `.dispatch` aspect body)
  → opCatalog / edgeCatalog lens     (dispatchEnumerations column)
  → cmd/<x>-app op_catalog.go        (DispatchEnumerations → descriptor)
  → descriptorform / facet client    (hub templates substituted → envelope.enumerations)
  → Gateway POST /v1/operations      (already accepts `enumerations`, unchanged)
  → ContextHint.Enumerations         (already parsed + shape-validated, unchanged)
```

## 3. Decisions (Winston, all implementation-level)

**D1 — A hub is a whole vertex key a descriptor-driven client can actually resolve.** `EnumerationSpec`'s doc
states the general rule: "Hub's template grammar belongs to the surface carrying it … each the same grammar
that surface's Reads use" (`internal/pkgmgr/definition.go:569`). The dispatch surface's hub grammar is
therefore drawn from `OpDispatchSpec.Reads`' vocabulary, validated by `opdispatchtemplates.go`'s existing
validator rather than a second one — but it is a **strict subset** of it, on two counts.

*Server-resolvable only.* The client-only `{me.<type>}` form (legal in `OptionalReads` alone, e.g.
`packages/cafe-domain/opmetas.go:131`) is refused. Contract #2 §2.5 buys *static* classification of an op's
read posture, and a hub that resolves for a caller with an `edgeIdentity` projection and vanishes for one
without would make the envelope's declared posture caller-dependent — the walk still runs, now undeclared,
for exactly the callers the declaration was meant to cover.

*Client-resolvable only, and a whole key.* Close review established two further gaps the general grammar
would have admitted. `{scopedTo}` and `{service}` pass the Reads vocabulary, and **the two shipped
descriptor-driven renderers disagree on them**: `cmd/facet/web/app.js` resolves both, while
`internal/descriptorform/form.mjs` throws `unrecognized read template` — a throw that escapes `submit()`, so
the op becomes unsubmittable from every descriptorform staff app, with a developer string in front of a
person. One descriptor, two meanings, is exactly what a declared read posture cannot be, so install refuses
the hub. And the `:id` modifier,
though documented and accepted by the general grammar, is *inert-but-wrong* on a hub: `{actor:id}` resolves
to a bare NanoID, passes the whole-key check, and lands on the envelope, but `kv.Links` walks from a full
vertex key, so the declaration can never match the walk it names and retiring its baseline row would redden
with no visible cause. Refused, along with any mid-segment placeholder.

The admitted hub grammar is therefore: `{actor}`, `{payload.<field>}`, or a literal key. No shipped walk
shape wants more — the 39 actor-role walks are all `{actor}`, and the rest hang off payload keys or
link-discovered vertices (§5.6).

**D8 — An NFR-S6 operation may not declare an enumeration, and install says so.** `descriptor_floor.go`
refuses *every* `contextHint.enumerations` entry for the equalized-rejection set (`ClaimIdentity`,
`CompleteCredentialLink`) terminally, at the head of step 4. Before this fire that refusal's stated reason was
structural — no descriptor could name an enumeration, so the case was unreachable — and this fire makes it
reachable. Left alone, a later author applying the actor-role pattern to `ClaimIdentity` would install
cleanly and take identity claiming down completely and un-attributably, since NFR-S6 collapses the resulting
fault into a generic `ClaimKeyInvalid` with nil details, on every redelivery. Install refuses the declaration
instead (calling `processor.IsNFRS6Operation`, never duplicating the set), and the floor's now-false
rationale — and the test case name asserting it — are rewritten to the true reason: the equalization closes
these operations' declared read set, so an enumeration a submitter adds prices work the equalization has no
subject for.

**D2 — Refuse a malformed declaration at install, not at dispatch.** Mirrors `validateGapEnumerations`'s
stated doctrine: the Processor refuses the *whole envelope* on a malformed enumeration, terminally, so a bad
declaration does not degrade the op — it kills it on every redelivery. Install is the loud failure point.
Held to exactly the shape `opwire`'s envelope parse enforces: hub and relation non-empty, direction `"out"`
or `"in"`, plus D1's hub-template check.

**D3 — Hub is a *whole key* template, and an unresolvable one drops the entry.** `substituteTemplates` in
`form.mjs` already drops a read whose placeholder does not resolve (`form.test.mjs:529`); an enumeration
follows it. Metadata that cannot be resolved is better dropped than submitted malformed, since a malformed
enumeration is a terminal envelope refusal (D2's same argument, from the client side). The walk still runs;
the guard then reports it as undeclared, which is the truthful outcome.

**D4 — Metadata, never hydration.** Identical to both precedents: declaring a walk does not change how the
script runs it (bounded, paged, live). What the declaration buys is Contract #2 §2.5's two stated payoffs —
the Edge mirror-coverage gate and static classification of the op's read posture — plus admission by
`ReadDriftGuard` without a baseline row.

**D5 — The proof set is the actor-role confinement walk.** `vtx.identity.<id> holdsRole out` is 39 of the
139 baselined walk shapes (28%), one uniform shape, and its hub is always the submitting actor
(`kv.Links(actor_key, "holdsRole", "out", …)` — e.g. `packages/cafe-domain/ddls.go:596,1594`), i.e. exactly
`{actor}`. This fire declares it for **cafe-domain's** ops and retires precisely those baseline rows; the
remaining shapes are swept as their own units (§6), not filed as a deferral.

**D7 — The AI-authored artifact surface gains `enumerations` and nothing else.** `OpDispatchArtifact`
(`capabilitymaterializer_starlark.go`) is the surface an AI-authored capability proposal may declare, and its
comment claimed a field-for-field mirror of `OpDispatchSpec` that it never was — `ClassChoices` and
`VisibleWhen` are absent from both the struct and `knownDispatchFields`, so an authored op declaring either is
refused as a smuggled key. The build found this and briefly closed it; that was reverted.

`Enumerations` is admitted because it is this item's subject and grants **no production authority** — envelope
metadata that hydrates no key, moves no permission, and reaches no authorizer decision (traced at close
through `starlark_kv.go`, `opwire.go`, `descriptor_floor.go` and `ddl_cache.go`, which decodes only
`reads`/`optionalReads` off the aspect). It is not literally inert: D4's payoff *is* admission by the
test-time `ReadDriftGuard` without a baseline row. That admission covers the declared walk only — the guard
builds its follow-up-read allowance from `record.EnumeratedVertices`, what the script observably walked, never
from the declaration — so a declaration cannot launder an undeclared read past it.
`ClassChoices` and `VisibleWhen` stay out: the evidence admits two
readings (forgotten, or a deliberate narrowing of what AI may author), nothing in the tree distinguishes them,
and this plane carries a shelved ★★★ admission-model row (`[capability-author] Authored-artifact admission
holes`). Widening a security-sensitive surface on an unsourceable premise is not this item's work. The false
comment is fixed instead, naming the exclusions, and the test that pinned the *mirror* is replaced by one that
pins the **subset**: the artifact's field set equals `OpDispatchSpec`'s minus a named exclusion list, and
every admitted field has a `knownDispatchFields` entry and vice versa. That is the stronger gate — it makes
the narrowing explicit and reviewable, and it mechanizes the struct-vs-allowlist drift that bit this fire.
No prior test pinned the artifact's field set at all (close review deepened the clone to 791 commits and
found the struct was introduced already lacking `VisibleWhen`), so this adds a gate where none existed rather
than replacing one.

**D6 — The baseline's comment block is amended in the same commit as the channel.** Its "the channel that
does NOT exist is the descriptor one" paragraph becomes false the moment Inc 1 lands. The
design-doc-body-stays-true rule (`agents/steward/SKILL.md` §4) binds a measured-residue file's prose the
same way: an unamended comment is a wrong instruction to the next builder, and the guard's own failure
message points builders straight at it.

## 4. Non-goals

- No change to `docs/contracts/*` (§0 above states why none is needed).
- No hydration of an enumerated link set (Contract #2 §2.5 forbids it — unbounded).
- No new grammar for a **link-discovered** hub (a walk whose hub only exists after a prior read resolves it —
  e.g. `vtx.building.<id> containedIn out` reached from a unit's `containedIn` target). Neither precedent
  supports one, and inventing it is a designer's call, not this fire's (§6).
- No sweep of the `read` rows (a different class with a different fix).
- **No `enumerations` member in `lint-facet-renderer-drift`, and no change to the SwiftUI spike renderer.**
  The argument is **precedent**, not charter: every envelope-declaration column is already outside that
  gate's member list — `reads`, `optionalReads`, `classChoices` and `visibleWhen` are all absent, and
  `clients/facet-swiftui-spike` honours `reads` alone. `enumerations` is the next member of that family and is
  handled exactly as its siblings are: the two submitting renderers (`cmd/facet/web/app.js`,
  `internal/descriptorform/form.mjs`) carry it, the spike does not. So this fire neither creates nor widens a
  drift class there; the spike is exactly as complete as it was.

  Do **not** read this as "the gate is only about rendering" — its own doc comment says a member may be
  "a `dispatch` column a renderer has to honour for one descriptor to mean the same thing wherever it is
  rendered", and `contextParams`/`selfAnchor` are members on submission grounds. Close review used that
  standard to find a real disagreement: `{scopedTo}`/`{service}` hubs made `form.mjs` throw and `app.js` drop
  silently. That is closed by narrowing the hub grammar (D1) so no admitted hub can be resolved differently by
  the two renderers — closed at the source rather than policed by the gate.

## 5. Fire brief (build note, 2026-08-29)

### 5.1 Scope sentence (verbatim, board row)

> `OpDispatchSpec` carries `Reads`/`OptionalReads` but no `Enumerations`, so 137 of the 139 baselined walk
> shapes are structurally undeclarable, not debt. Precedent to mirror: Loom `StepSpec.Enumerations`,
> Weaver `GapActionSpec.Enumerations`.

**Green bar:** a package op declaring `{actor} holdsRole out` in its `OpDispatchSpec` reaches
`ContextHint.Enumerations` on the submitted envelope through the descriptor path, and its baseline walk rows
are gone with `ReadDriftGuard` green.

### 5.2 Verified touch-list (`file:line` checked live 2026-08-29)

| # | file:line | what |
|---|---|---|
| 1 | `internal/pkgmgr/definition.go:872-935` | `OpDispatchSpec` — add `Enumerations []EnumerationSpec` |
| 2 | `internal/pkgmgr/definition.go:577` | `EnumerationSpec` — exists, reused unchanged |
| 3 | `internal/pkgmgr/build.go:681-730` | `opDispatchBody` — emit `enumerations` via `enumerationBodies` (`:908`) |
| 4 | `internal/pkgmgr/capabilitymaterializer_starlark.go:374-382` | `OpDispatchArtifact` field-for-field mirror |
| 5 | `internal/pkgmgr/capabilitymaterializer_starlark.go:400-404` | `knownDispatchFields` — admit `"enumerations"` |
| 6 | `internal/pkgmgr/capabilitymaterializer_starlark.go:535-543` | artifact → spec conversion |
| 7 | `internal/pkgmgr/opdispatchtemplates.go` | hub-template validation (D1) |
| 8 | `packages/edge-manifest/lenses.go:671,747` | `op.dispatch.data.enumerations AS dispatchEnumerations` (edgeCatalogTail + opCatalogSpec) |
| 9 | `packages/edge-manifest/manifest.yaml` + `package.go` | version bump (lockstep, `lint-package-version`) |
| 10 | `cmd/{cafe,clinic,loftspace,wellness}-app/op_catalog.go:40,118-128,185-201` | projection field, dispatch struct, `toDescriptor` |
| 11 | `internal/descriptorform/form.mjs:922-923,959-969` | substitute hubs, put `enumerations` on the envelope |
| 12 | `cmd/facet/web/app.js:2300,2811` | the Facet renderer's own envelope build |
| 13 | `internal/refractor/grouping_reduction_corpus_census_test.go:153` | `opCatalog` column census pin (**Postgres-gated**) |
| 14 | `packages/cafe-domain/*` + `manifest.yaml` | D5 declarations + version bump |
| 15 | `internal/testutil/read_drift_baseline.txt:29-37` + its cafe-domain walk rows | D6 amendment + row retirement |

### 5.3 Precedents to mirror

- Struct + doc shape → `internal/weaver/registry.go:125-132` (`GapAction.Enumerations`) and
  `internal/loom/pattern.go:64-79` (`StepSpec.Enumerations`).
- Install/load validation → `internal/weaver/registry.go:774-785` (`validateGapEnumerations`), whose comment
  states D2's doctrine outright.
- Body emission → `internal/pkgmgr/build.go:908` (`enumerationBodies`, already shared by both precedents).
- Envelope-reach test → `internal/weaver/enumerations_envelope_internal_test.go`
  (`TestDirectOpEnumerations_ReachEnvelope`) and `internal/loom/enumerations_envelope_internal_test.go`.
- Lens column + its cypher test → `packages/edge-manifest/lens_cypher_test.go:694` (`dispatchOptionalReads`).
- Descriptor round trip → `cmd/loftspace-app/op_catalog_test.go:20`.
- Client template substitution → `internal/descriptorform/form.test.mjs:515-532`.

### 5.4 Increment order + green checks

1. **Vocabulary (pkgmgr).** Touches 1–7. `go test ./internal/pkgmgr/...`
2. **Projection (lens → app descriptor).** Touches 8–10, 13.
   `go test ./packages/edge-manifest/... ./cmd/cafe-app/... ./cmd/clinic-app/... ./cmd/loftspace-app/... ./cmd/wellness-app/...`
   and, with `POSTGRES_TEST_DSN` set, `go test ./internal/refractor/...`
3. **Client.** Touches 11–12. `node --test internal/descriptorform/*.test.mjs`, `node --check cmd/facet/web/app.js`,
   `go test ./internal/descriptorform/...`
4. **Real declarations + baseline.** Touches 14–15. `go test ./packages/cafe-domain/... ./internal/testutil/...`

Whole-fire gate: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `go run ./scripts/lint-board.go`,
`DIFF_BASE=<base> go run ./scripts/lint-package-version.go`, `go test ./... -p 4` **with
`POSTGRES_TEST_DSN` exported** (REMOTE.md §3 — the suite is falsely green without it).

### 5.5 In-scope gotchas

- **`packages/` content edits bump the manifest version AND `Definition.Version`** — edge-manifest and
  cafe-domain both. `TestPackage_ManifestMatchesDefinition` pins the pair.
- **The refractor corpus census is Postgres-gated** and pins `opCatalog`'s column set as a sorted string
  (`:153`). A new lens column reddens CI's `unit-1` while a local `go test ./...` stays green — the exact
  shape REMOTE.md §3 documents as having shipped before.
- **No history/changelog comments** (CLAUDE.md). Doc comments describe the field as it is now.
- **The guard checks under-declaration only** (`read_drift_guard.go:121-142`) — declaring is always safe;
  removing a baseline row is not, because *every* dispatcher of that op (including hand-built test envelopes)
  must then carry the declaration. Retire a row only after every dispatch path for that op declares.
- **Dossier — `docs/components/pkgmgr.md`:** an artifact struct that mirrors a spec "field-for-field" has a
  companion `knownDispatchFields` allowlist; adding to one without the other silently rejects the field as
  smuggled.
- **Standing checklist #3 (plumbing needs a revert-proof).** A field threaded through structs is exactly the
  shape whose proof gets skipped. Every increment lands a test that asserts the *value* arrives at that
  layer, and D5's real declaration is what makes deleting the whole change fail a test.
- **Standing checklist #2 (every census is a premise).** The 139/137 counts were re-run live:
  `grep -c "^walk" internal/testutil/read_drift_baseline.txt` = 139; the `holdsRole` class = 39.

### 5.6 Adjacent finds

- **Link-discovered hubs stay undeclarable.** The 100 non-actor-role walk rows split two ways: hubs a
  dispatcher *can* resolve (a `{payload.*}` entity key) and hubs only a prior read resolves (e.g.
  `vtx.building.<id> containedIn out`, reached from a unit's `containedIn` target). The split has not been
  counted per row — that census is the sweep's own first step, not a number to assert here. The second
  half is undeclarable on *every* surface, neither precedent declares one, and no ratified pattern extends
  to it: it files as the second out (`📐 needs designer pass · no-pattern: chained/link-discovered
  enumeration hub declaration`).
- **The remaining actor-role declarations** (32 shapes outside cafe-domain) are mechanical applications of
  the pattern this fire ships — this run's own next units, not a deferral row.

### 5.7 Non-goals (drift fence)

`docs/contracts/*`; hydration; the `read` rows; Loom/Weaver's own enumeration surfaces; any change to the
Gateway's or `opwire`'s already-correct acceptance of `enumerations`.

## 6. What this does not close

The link-discovered-hub class (§5.6) is the honest remainder: this fire gives the descriptor surface the same
reach Loom's and Weaver's have, and no more. A walk whose hub is unknown until the script reads something is
undeclarable on *every* surface — that is a platform-wide absent pattern, not a pkgmgr gap.

## 7. Close pass — what the reviews found, classified

Three cold adversarial reviewers (security / capability-plane, end-to-end correctness, conventions +
architecture) ran over the channel's whole diff, independently. One BLOCKING, four MAJOR, six MINOR. Every
finding below was fixed in this run — none was filed.

| # | class | component | finding | the check that now catches it |
|---|---|---|---|---|
| 1 | **brief-gap** | edge / facet | **BLOCKING** — both enqueue hosts dropped `enumerations`: the JS producer and the Go consumer are separate hops and neither decoder rejects unknown keys. The brief's touch-list named `cmd/facet/web/app.js` and neither host. Live once the cafe ops declared. | `TestBuildEnqueueEnvelope_ForwardsEnumerations` + browser twin; envelope construction extracted so the hop is testable |
| 2 | **design-gap** | pkgmgr / processor | An NFR-S6 op declaring an enumeration installs clean and then faults every submission terminally, collapsed to a details-less `ClaimKeyInvalid`. Found by two reviewers independently. | install refuses it (`processor.IsNFRS6Operation`), with a non-member positive control |
| 3 | **design-gap** | pkgmgr / descriptorform | `{scopedTo}`/`{service}` hubs installed clean and the two shipped renderers disagree on them — `app.js` resolves both, `form.mjs` throws and aborts the submit. The new refusal text *recommended* them. | hub grammar narrowed to `{actor}`/`{payload.<field>}`/literal (D1), each refusal executed |
| 4 | **implementation-bug** | pkgmgr | `{actor:id}` on a hub installs, resolves to a bare NanoID, and can never match the walk it declares — a row retired against it would redden with no visible cause. | `:id` and mid-segment placeholders refused on a hub |
| 5 | **convention** | pkgmgr | Two further refusal tails in the shared validator stayed Reads-shaped and now fired for hubs, sending an author to `Dispatch.OptionalReads`/`ContextParams` — fields a hub declaration does not have. A new test *pinned* one of them. | each tail list-aware; tests assert the hub-shaped remedy and the Reads tails unchanged |
| 6 | **brief-gap** | edge-manifest / processor | Doc obligations missed: `edge-manifest.md` is the normative as-built row shape (no `dispatchEnumerations`), and `processor.md` asserted an `OpDispatchSpec` can name no enumeration. | both rewritten; the brief's touch-list carried no `docs/components/` row |
| 7 | **brief-gap** | apps | The `toDescriptor` presence-guard clause had no revert-proof though both sibling clauses did, and three of four apps had no `op_catalog` test at all. | one test per app, plus the enumerations-only guard twin |
| 8 | **review-over-reach** | — | A reviewer held that declaring a walk admits its follow-up reads past the drift guard. It does not: the guard builds that allowance from `record.EnumeratedVertices`, what the script observably walked, never from the declaration. D7's wording was tightened to "no production authority" anyway. | — |

Minor, accepted rather than changed, each with its reason: `unknownOpMetaFields` scans one level into
`dispatch`, so an extra key inside an `enumerations` element is dropped rather than refused (inert —
`EnumerationSpec` has exactly three fields and `enumerationBodies` writes exactly three keys); the Gateway's
`cleanKeys` trims and dedupes reads but not enumerations (pre-existing, and install now refuses the empty hub
a descriptor could contribute); `form.mjs` emits the key only when non-empty while `app.js` always emits it
(each matches its own file's convention, and the Gateway gates on `len > 0`).

**Two lessons routed to dossiers.** `docs/components/edge.md` gains the enqueue-hop class (#1).
`docs/components/pkgmgr.md` gains the downstream-refusal class (#2), displacing the entry that had already
recorded itself as retired into the fire-brief standing checklist. **`pkgmgr.md`'s dossier stands at 15
against its cap of 12** — that is pre-existing and this fire did not worsen it, but it is over, and a
curation pass retiring the entries whose gates have since landed is genuine owed work.

The recurring shape across #1, #6 and #7 is one brief-quality defect, not three: **the touch-list was
compiled from the code that produces a value and not from the code that consumes it.** A brief for a fire
that threads a new field should be built by walking the chain from the far end backwards — from the wire
format that must carry it, back through every host, doc and pin that names its siblings.

## 8. Fire brief — the actor-role sweep outside cafe (build note, 2026-08-30)

§5.6 named the remaining actor-role declarations as "this run's own next units". This is that sweep's
brief. Board row: `[packages] Declare the actor-role confinement walk on the ops outside cafe`.

### 8.1 Scope sentence (verbatim, board row)

> The `{actor} holdsRole out` walk is 39 of the 139 baselined shapes; cafe's 8 ops declare it and their 7
> rows are retired. Same mechanical pattern for the rest (clinic, loftspace, wellness, identity,
> lease-signing), each op's dispatcher and its hand-built test envelopes together.

**Census re-run live (standing checklist #2 — every census is a premise), 2026-08-30 at `9d0bec7`:**
`grep -c "^walk" internal/testutil/read_drift_baseline.txt` = **130** walk rows (down from the design's
139 — cafe's 7 retired, plus two others since); `grep "^walk" | grep holdsRole` = **32**. So the sweep's
remainder is 32 rows, not the row's "same pattern for the rest" read against 39.

**The row's package list is incomplete.** It names five packages; the 32 rows span **nine**, and three of
them (`service-domain`, `maintenance-domain`, `lease-signing`) have no `opmetas.go` at all — their
`OpDispatchSpec` literals live in `ddls.go` / `permissions.go`. Two more owners the row does not name are
`clinic-reminders` (the five visit-series ops, in `visitseries.go`) and `loftspace-ledger`.

**Green bar:** every one of the 32 ops declares `{actor} holdsRole out` on its `OpDispatchSpec` **and** on
every hand-built test envelope that dispatches it, its baseline row is gone, and `ReadDriftGuard` is green
across the whole tree.

### 8.2 Verified touch-list (checked live 2026-08-30)

Ops by owning package, with the file carrying their `OpDispatchSpec` literals:

| package | ops | dispatch-spec file | version (manifest + `package.go` mirror) |
|---|---|---|---|
| clinic-domain | 7 — CreateAppointment, RescheduleAppointment, SetAppointmentStatus, SetAppointmentSite, SetProviderHours, SetProviderTimeOff, RecordEncounter | `opmetas.go` (13 specs) | 0.34.15 |
| wellness-domain | 11 — CancelBooking, CreateBooking, CreateSession, CreateSessionSeries, CreateStudio, JoinWaitlist, ReassignSession, ReleaseOrphanedBooking, SetBookingAttendance, SetInstructorProfile, TombstoneSession | `opmetas.go` (12 specs) | 0.22.11 |
| clinic-reminders | 5 — StartVisitSeries, PauseVisitSeries, ResumeVisitSeries, EndVisitSeries, SetVisitSeriesSite | `visitseries.go:1410,1443,1466,1489,1515` | 0.10.5 |
| loftspace-domain | 2 — AssignUnitOwner, RemoveUnitOwner | `opmetas.go` (4 specs) | 0.12.1 |
| loftspace-ledger | 1 — LoftspaceCreateAccount | `opmetas.go` (3 specs) | 0.5.0 |
| lease-signing | 1 — DecideLeaseApplication | `permissions.go` (9 specs) | 0.31.12 |
| identity-domain | 1 — RecordIdentityPII | `opmetas.go` (6 specs) | 0.20.9 |
| service-domain | 2 — RecordServiceOutcome, SetServiceProviderProfile | `ddls.go` (3 specs) | 0.10.5 |
| maintenance-domain | 2 — ReportIssue, ResolveWorkOrder | `permissions.go` (2 specs) | 0.2.10 |

Baseline rows to retire: the 32 `walk\t<op>\tvtx.identity.<id> holdsRole out` rows,
`internal/testutil/read_drift_baseline.txt:131-252` (non-contiguous; the file is sorted by op).

### 8.3 Precedents to mirror

- Declaration on the spec → `packages/cafe-domain/opmetas.go:111-113` (OpenTab), `:152` (Charge) —
  `Enumerations: []pkgmgr.EnumerationSpec{{Hub: "{actor}", Relation: "holdsRole", Direction: "out"}}`,
  under a comment naming the confinement probe that walks it.
- Declaration on a hand-built **test** envelope → `packages/cafe-domain/integration_test.go:329,423,1641`
  — `Enumerations: []processor.EnumerationHint{{Hub: <actorKey>, Relation: "holdsRole", Direction: "out"}}`.
  Note the two types differ: `pkgmgr.EnumerationSpec` (templated `{actor}`) on the spec,
  `processor.EnumerationHint` (a concrete key) on the envelope.
- Row retirement → the cafe rows removed in `bcc2681`.
- **Per-package shared test submit helpers** are where most envelope edits land, not the call sites:
  `clSubmit`/`clSubmitOpt` (clinic-domain), `wdSubmit` (wellness-domain), `crSubmit`/`crSubmitOpt`
  (clinic-reminders), `mdSubmit` (maintenance-domain), `decide`/`decideReason` (lease-signing),
  `assignUnitOwner`/`removeUnitOwner` (loftspace-domain), `submitOutcome` (service-domain). One edit in a
  helper covers every op it dispatches — which is what makes this sweep a fire rather than 512 edits.

### 8.4 Increment order + green checks

**One increment per package, largest first.** The landing shape is §4's *land each increment on main*, and
the invariant that keeps main correct across boundaries is: **a package's spec declarations, its test-
envelope declarations and its baseline-row retirements land in the SAME commit**, so the baseline and the
corpus are never inconsistent at a boundary. A package not yet swept keeps its rows and stays green.

1. clinic-domain (7) — `go test ./packages/clinic-domain/...`
2. wellness-domain (11) — `go test ./packages/wellness-domain/...`
3. clinic-reminders (5) — `go test ./packages/clinic-reminders/...`
4. loftspace-domain (2) + loftspace-ledger (1) — `go test ./packages/loftspace-...`
5. the tail: lease-signing (1), identity-domain (1), service-domain (2), maintenance-domain (2)

Whole-fire gate: `go build ./...`, `make vet`, `golangci-lint run ./...`,
`STRICT=1 go run ./scripts/lint-conventions.go`, `DIFF_BASE=<base> go run ./scripts/lint-package-version.go`,
`go test ./... -p 4` with **`POSTGRES_TEST_DSN` exported** (REMOTE.md §3), plus the build-tagged harnesses
any touched interface reaches.

### 8.5 In-scope gotchas

- **The guard reads the ENVELOPE, not the spec** (`read_drift_guard.go:121-129`: `env.ContextHint`
  `.Enumerations`). Declaring on `OpDispatchSpec` is inert for a Go test that hand-builds its envelope, so
  a row retired on the strength of the spec edit alone reddens. **The guard's own failure message names the
  op and the shape** — it is the reliable feedback loop for finding every under-declaring dispatcher, and
  is what this fire drives each increment against rather than a static census of 512 test-file mentions.
- **Declaring is always safe; retiring is not** (§5.5). Retire a row only once that package's tests pass
  with it gone.
- **NFR-S6 refuses an enumeration** (D8). The equalized set is `ClaimIdentity` / `CompleteCredentialLink`;
  neither is among the 32, but `identity-domain` owns both, so its increment must not spread the pattern to
  a sibling op by symmetry.
- **`packages/` content edits bump the manifest version AND the `Version` constant** (e.g.
  `packages/clinic-domain/package.go:139`). Nine packages, nine pairs. `lint-package-version` gates it.
- **Two of the 32 have no `OpDispatchSpec`, and each declares on a DIFFERENT surface** (checked live; both
  are deliberate, with the reason recorded beside them):
  - `ReleaseOrphanedBooking` — `wellness-domain/opmetas.go:136-139` states it is dispatched only by the
    `wellnessOrphanedBookingSettlement` Weaver target (`targets.go`), no human path. Its declaring surface
    is therefore **`GapActionSpec.Enumerations`** (the Weaver precedent, §1's second row), not the dispatch
    spec.
  - `RemoveUnitOwner` — `loftspace-domain/opmetas.go:17-19` states it "stays bare: no `cmd/*-app` source
    references it … the trusted admin tool hardcodes its own dispatch." Its declaring surface is that
    **hand-built envelope**, the channel the baseline's own header lists third.

  Both are declarable; neither is a designer gap. A scout pass reported five *further* ops as bare
  (`clinic-reminders`' visit-series family) — that was false: all five carry a full `OpDispatchSpec` at
  `visitseries.go:1410,1443,1466,1489,1515`. Verify a "this op cannot declare" claim against the spec
  literal itself, not against the op-name constant block near the top of the file.
- **No history/changelog comments** (CLAUDE.md); doc comments describe the field as it is now.
- **Dossier — `docs/components/pkgmgr.md`:** an artifact struct that mirrors a spec "field-for-field" has a
  companion `knownDispatchFields` allowlist; adding to one without the other silently rejects the field as
  smuggled. (Not expected to bind here — this fire adds no field — but the sweep touches the same specs.)

### 8.6 Adjacent finds

- **`pkgmgr.md`'s dossier stands at 15 against its cap of 12** (§7 recorded it, owed work, still owed).
- The remaining 98 non-`holdsRole` walk rows are the link-discovered-hub class and the payload-hub class,
  already routed: `📐 needs designer pass · no-pattern: chained/link-discovered enumeration hub declaration`.

### 8.7 Non-goals (drift fence)

`docs/contracts/*`; hydration; the `read` rows; the 98 non-actor-role walk rows; Loom/Weaver's own
enumeration surfaces; any new hub grammar.

### 8.8 Close — what shipped, and the three premises the build corrected

**Shipped:** 31 of the 32 ops declare `{actor} holdsRole out`; the baseline's walk rows fall 130 → 99
and its `holdsRole` rows 32 → 1. SHAs `4daaf0a` (clinic's 12), `e6306a6` (the other 18), `6af0555`
(RemoveUnitOwner), `a138f25` (review fixes), plus `4c3375b` (the helper's own tests).

**Correction 1 — §8.4's increment boundary was wrong, and the build changed it.** The brief said a
package's declarations, envelope edits and row retirements land together. They cannot: a baseline row is
keyed by **operationType**, so retiring one breaks every dispatcher of that op *wherever it lives*.
clinic-ledger and clinic-reminders both book a clinic-domain `CreateAppointment`; wellness-ledger,
loftspace-ledger, privacy-base, semantic-contracts and `internal/leaseconvergence` all dispatch ops they
do not own. **The unit is the OP and every dispatcher of it**, and the commits are grouped that way.

**Correction 2 — the declaration is resolved from the spec, not restated in each fixture.** The cafe
precedent hardcoded the hint in its test envelopes. That agrees with the spec only by coincidence:
deleting a spec's `Enumerations` would leave every fixture green with its baseline row already retired —
the ratchet's coverage silently gone. `internal/testutil/declared_enumerations.go` resolves the hint from
the op's own meta instead, so each fixture is a **revert-proof** of the declaration its retired row rests
on. Proven by experiment at close: deleting `AssignUnitOwner`'s declaration reddens four tests.

**Correction 3 — §8.5's reason for `ReleaseOrphanedBooking` was checkable and wrong.** It said the Weaver
gap-action grammar admits only `row.<column>`, so the dispatching actor is unnameable. A **literal hub is
in that grammar** (`strategist.go:882` `resolveReadKey`). The real blocker is one level down:
`bootstrap.WeaverIdentityKey` is a package-level **var populated at runtime** — each deployment generates
its own primordial ID set on first boot (`internal/bootstrap/nanoid.go:65-81,605`) — so a package
`Definition` built at package-init cannot embed it, and a lens cannot project an identity it has no way to
know. Neither half of the grammar can reach it. **The conclusion stands; the stated reason is replaced.**

**Why the two undeclared ops are treated differently, which is the whole point.** `RemoveUnitOwner` has no
descriptor but a closed, documented dispatcher set, all of them hand-built envelopes — a channel the
baseline's own header sanctions — so declaring there is truthful and its row retires. `ReleaseOrphanedBooking`
*also* has hand-built test dispatchers, and declaring at them would have retired its row too. **That is
refused deliberately:** its real dispatcher is the Weaver, which genuinely cannot declare, so a
fixture-side declaration would delete the guard's coverage of a walk that stays undeclared in production.
A row retired against a declaration production does not send is worse than the row.

The residue is one row and one filed item — `[Weaver] A Weaver-dispatched op cannot declare a walk hubbed
on the dispatching actor`, 🔭 flag-for-Andrew, a Contract #10 templating addition.

**Reviews.** One cold adversarial pass over the item's whole diff: 1 MAJOR + 4 MINOR, all fixed in this
run, none filed. Classified:

| class | component | finding | the check that catches it now |
|---|---|---|---|
| **implementation-bug** | loftspace-domain | MAJOR — the one helper serving an op *without* a spec keyed its hardcoded-hint fallback on "the resolve came back empty", so deleting a *different* op's declaration fell through to the literal and stayed green. The anti-pattern the item exists to kill, reintroduced at its one exception. | fallback keyed on the op; deleting the declaration reddens four tests in that file |
| **implementation-bug** | lease-signing | a submit helper appended through the caller's `ContextHint` pointer, which one test reuses across two submissions — inert only while that op declares nothing | merges into a copy |
| **brief-gap** | scripts | 34 seed-script dispatch sites declared nothing while their ops now do — an inconsistency this item created | both seeds resolve from `pkgregistry` in the submit helper |
| **convention** | testutil / loftspace | a doc comment narrating the build rather than the code; a version bumped twice for one content change | conventions lint; the bump reverted |

**Dossier — one class routed.** `docs/components/pkgmgr.md`: *a fixture that hardcodes a value the
descriptor also declares is not a proof of the declaration — it agrees by coincidence, and the declaration
can be deleted with every test still green. Resolve it from the descriptor so the fixture is a revert-proof.*
