# Descriptor-declared `kv.Links` enumerations — the missing declaration channel

**Status: ✅ Winston-ratified — build-ready** (2026-08-29). Every open question here is an implementation
call (`agents/steward/SKILL.md` §0); none touches a frozen contract or an architectural fork. Contract #2
§2.5 already specifies `contextHint.enumerations` and does **not** enumerate which surfaces may populate it,
so adding a third populating surface builds *to* the frozen contract rather than changing it.

Board row: `[Pkgmgr] A descriptor-dispatched op cannot declare a kv.Links walk`
(`backlog/lattice.md`, Component maintenance).

## 1. The gap

Contract #2 §2.5 class (e) sanctions a bounded, paged, live `kv.Links` walk **provided the walk is declared**
as `{hub, relation, direction}` in `contextHint.enumerations`. Three surfaces can populate it today:

| surface | hub grammar | resolver | validation |
|---|---|---|---|
| Loom `StepSpec.Enumerations` | `subject`, `subject.<aspect>` | `systemOpEnumerations` → `resolveSubjectTemplate` (`internal/loom/engine.go:932`) | `validateEnumerations` at pattern load (`internal/loom/pattern.go:149`) |
| Weaver `GapActionSpec.Enumerations` | literal or `row.<column>` | `resolveReadKey` (`internal/weaver/strategist.go:356`) | `validateGapEnumerations` at install **and** engine load (`internal/weaver/registry.go:774`) |
| a hand-built envelope / a client posting through the Gateway or Loupe | concrete key | — | `opwire` envelope parse (`internal/processor/opwire/opwire.go:307`) |

`pkgmgr.OpDispatchSpec` — the op meta's machine-readable submission recipe, and the surface every ordinary
descriptor-driven client dispatches from — carries `Reads` and `OptionalReads` but **no `Enumerations`**
(`internal/pkgmgr/definition.go:872`). An ordinary descriptor-dispatched op therefore cannot declare a walk
at all. The read-drift census measured the consequence: of 139 baselined walk shapes, all but three sit on
ops with no declaration channel — a missing vocabulary, not script debt
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

**D1 — Hub grammar is the surface's own Reads grammar.** `EnumerationSpec`'s own doc already states the rule:
"Hub's template grammar belongs to the surface carrying it … each the same grammar that surface's Reads use"
(`internal/pkgmgr/definition.go:569`). So a dispatch enumeration's Hub takes the `OpDispatchSpec.Reads`
vocabulary — `{actor}`, `{service}`, `{scopedTo}`, `{payload.*}`, `{me.<type>}`, the `:id` modifier, or a
literal key. No new grammar is invented, and `opdispatchtemplates.go`'s existing validator is reused verbatim.

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
