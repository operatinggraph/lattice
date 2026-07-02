# Retire the legacy `simple` rule engine — decouple the shared types, delete the dead parser + its parked invalidation scaffolding

**Status: ✅ Andrew-ratified (2026-06-30) — D1 = delete the dead invalidation-forest scaffolding, as recommended. Build-ready for the Lattice Steward.**
**Author:** Winston (Designer fire, 2026-06-30)
**Backlog:** Stream-2 Component maintenance — *[Refractor] Retire the legacy `simple` engine (full-engine is universal)* (★★, M–L; Surveyor-filed 2026-06-30)
**Owning components:** `internal/refractor/ruleengine/{simple,,full}`, `internal/refractor/{pipeline,projection,control,lens,fixture}`, `cmd/refractor/main.go`. Docs: `docs/components/refractor.md`.

---

## For Andrew

**What it does (two lines).** Every one of the 20 installed lenses (all packages + both primordial bootstrap lenses) declares `engine: full` — the v1 `simple` recursive-descent parser/compiler/evaluator (~2.8 kLOC incl. tests) is dead in production. But it can't just be deleted: the `simple/` package **owns the pipeline's engine-neutral row/entry carrier types** (`EvalResult`, `NodeEntry`) that every write path threads through *including the full-engine path*, and it hosts a **full-engine-fed invalidation-forest analyzer** that borrows simple's plan/traversal types. This design does the decouple-then-delete cleanly: (1) move the two neutral carrier types up into the engine-neutral `ruleengine` package; (2) delete the parked invalidation-forest scaffolding; (3) delete the simple engine and every now-dead branch that routed to it.

**Architectural fork:** **none** (no Gateway / read-path-auth / Vault / multi-cell / HA-NATS surface). Pure internal refactor of the Refractor read side. No new vertex/aspect/link/lens/op, no new bucket, no read-path (P5) or write-path (P2) change — the live projection path (full-engine + broad-BFS fan-out) is byte-for-byte unchanged.

**Frozen-contract change:** **none.** I grepped `docs/contracts/*` — no frozen contract mentions the simple engine, the `ruleEngine`/`engine` field, or the simple-then-full fallback. The engine model is documented only in the **non-frozen** `docs/components/refractor.md`, which this design updates. **Do not stage any contract edit for this item.**

**The one judgment call for you (a decision, not a fork) — D1: delete the dead invalidation-forest scaffolding.** To delete `simple/` I must resolve what happens to `invalidation_coverage.go` + `invalidation_plan.go` — a ~500-LOC full-engine analyzer *misfiled in the simple package* that borrows simple's `QueryPlan`/`TraversalStep`/`reverseTraverse`. It is **dead at runtime**: its reverse-walk (`AffectedAnchors`) is never called on any live path, and its coverage gate — nominally "fail-closed on the auth plane" — is **overridden to a warning + broad BFS** in `driver.go:168-182`. Both ratified reprojection designs ([link-aspect-triggered-reprojection](link-aspect-triggered-reprojection-plain-lenses-design.md) §4 and [negative-filter-retraction](negative-filter-retraction-projection-design.md)) **explicitly chose broad BFS over this forest** and call it a "precise-future the live pipeline ignores." It is textbook dead scaffolding: built in Epic 12 (stories 12.2/12.3) for a precise-invalidation consumer that was never built and that every current design has declined. **My recommendation: delete it** (Option 1 below) — it is the specific thing blocking a clean `simple` deletion, BFS is the *sound superset* so deleting the precise path carries zero correctness/security risk, and git preserves it verbatim if "precise invalidation" is ever prioritized (it would be re-authored against the full AST directly — cleaner than today's simple-type-borrowing form). The fallback (Option 2: relocate the forest to a neutral package) keeps ~650 LOC of consumer-less machinery alive and requires a delicate port of simple's traversal internals — more risk, less house-consistent. **This is the lead's call and I've decided it (delete); flagged here for your veto.** Everything else in this design is mechanical and non-controversial.

---

## 1. Problem & intent

### 1.1 The vision tie-in

Lattice's decision-of-record is **one openCypher engine** (`_bmad-output/planning-artifacts/project_architectural_decisions.md`: "parser strategy"; `docs/components/refractor.md` Principles: *"openCypher full engine is canonical for new lenses; the simple engine is legacy-fixture support only"*). The simple engine was the v1 Materializer-derived parser carried forward as a bridge while the full ANTLR-vendored engine matured. That migration is complete: the full engine is the canonical engine, all 20 lenses use it, and the simple engine is a maintenance liability — it owns shared types (so it can't be ignored), it's a second code path every refactor must reason about, and it advertises a capability (a legacy-fixture engine, a "simple validate" control endpoint) that no longer does anything. Retiring it is lean-architecture hygiene: **delete the bridge once you're across it.**

### 1.2 The grounded state (verified, 2026-06-30)

Every lens spec explicitly declares `engine: full`:

```
packages/{identity-hygiene,clinic-domain,orchestration-base,clinic-reminders,
         service-location,rbac-domain,loftspace-domain,augur,lease-signing,
         objects-base}/manifest.yaml   →  engine: full   (all lens entries)
internal/bootstrap/primordial.go:1066,1119                →  "engine": "full"
```

There is **no** lens with `engine: simple` and **no** lens with the field absent. So the registry's simple-then-full *fallback* (`ruleengine.SelectForLens` `""` case) is never exercised by any installed lens, and the simple engine's `Parse`/`Compile`/`Evaluate` are never reached in production. The simple engine is confirmed dead in prod.

### 1.3 Why it isn't a one-line delete — the coupling map

The blocker is **type ownership**. `git grep` of every cross-package reference into `ruleengine/simple` (outside the package itself) sorts into three categories:

| Category | Symbols | Who references them | Disposition |
|---|---|---|---|
| **A — truly simple-engine-only** | `parser.go` (recursive-descent → simple AST), `ast.go` (`Query`, `NodePattern`, …), `compiler.go` (`Compile` → `QueryPlan`), `evaluator.go` (`Evaluate`, `reverseTraverse`, `deleteResult`, …), `adapter.go` (`Engine`/`New`/`CompiledRule`/`Parse`/`Execute`), `plan.go` (`QueryPlan`, `TraversalStep`, `EdgeDirection`, `Column`) | pipeline default-branch (`simple.Evaluate`), `cmd/refractor/main.go` (simple plan-build + hot-reload), `control/service.go` (simple `validate`), `lens/schema.go` (registry + simple post-parse validate), `fixture/runner.go` (legacy fixtures) | **DELETE** (Fire 3) |
| **B — engine-neutral carrier types** | `EvalResult{Delete,Keys,Row,ProjectionSeq}`, `NodeEntry{CoreKVKey,NodeLabel,IsDeleted,Properties}` | the **whole pipeline**, on the **full** path too (`evaluate.go`, `pipeline.go`) | **MOVE up to `ruleengine`** (Fire 1) |
| **C — full-fed invalidation analyzer misfiled in simple** | `invalidation_coverage.go` (`AnalyzeInvalidationCoverage`, walks `full.Query` AST), `invalidation_plan.go` (`InvalidationForest`, `CompileInvalidationForest`, `AffectedAnchors`, `MaxBranchLen`, `mapFullDirection`) — parses via `full.New()` but borrows simple's `QueryPlan`/`TraversalStep`/`EdgeDirection`/`reverseTraverse` | `projection/plan.go` `Compile` (activation-time only) | **DELETE as dead scaffolding** (Fire 2) — see D1 |

The subtlety the board row flagged (`own the shared EvalResult/QueryPlan types`) is Category **B** and the borrowed types under **C**. Neither can survive a naive `rm -r simple/`. The design is precisely: relocate B, delete C, then delete A.

### 1.4 Category C is dead at runtime — the evidence

- **`AffectedAnchors` (the forest reverse-walk) is never called on a live path.** `git grep AffectedAnchors` hits only the `simple` package itself + its unit tests. The `ProjectionPlan.Invalidation` field is *compiled and parked*; the live fan-out always uses `pipeline.ActorEnumerator` (broad adjacency BFS).
- **The coverage gate is advisory, not enforcing.** `projection.Compile` returns a `*CompileError` for an auth-plane lens with an "uncovered" MATCH — but `driver.go:168-182` (`InstallActorAggregate`) *catches that error, logs a warning, and registers the lens with the BFS enumerator anyway.* So no lens is ever refused on coverage grounds. The analyzer's only runtime effect is a log line.
- **Both ratified designs chose BFS over the forest.** [link-aspect-triggered-reprojection §4](link-aspect-triggered-reprojection-plain-lenses-design.md): *"The precise-future (the compiled invalidation forest, which the plan already carries but the live pipeline ignores in favor of broad BFS — driver.go:145-149) … this design stays on broad BFS (the sound superset)."*
- **Deleting it is zero-risk.** BFS *over-*reprojects, never *under-*reprojects — it can never miss an affected anchor (`driver.go:145` comment). The forest was only ever a cost optimization (fewer re-executions per event). Removing it changes efficiency, not correctness, and not security.

---

## 2. Shape — the target architecture

### 2.1 Read path (P5) / write path (P2)

**Unchanged.** This design touches neither. Applications still read lens targets; the Processor is still the sole Core-KV writer; Refractor still projects via the full engine and writes lens targets. The only thing that moves is *where two Go structs are declared* and *which dead branches exist in the projection code*. The live full-engine + BFS path is preserved verbatim.

### 2.2 The neutral carrier types (Category B → `ruleengine`)

`ruleengine` is already the engine-neutral home: it defines `RuleEngine`, `CompiledRule`, `EventContext`, `ProjectionResult`, and imports only stdlib. `EvalResult` and `NodeEntry` are plain `map[string]any` structs with no engine dependency; they belong here, not in `simple`. After the move:

- `simple.EvalResult` → `ruleengine.EvalResult`
- `simple.NodeEntry` → `ruleengine.NodeEntry`

Import sites updated: `pipeline/pipeline.go`, `pipeline/evaluate.go`, and their tests. The simple engine's own `Evaluate`/`deleteResult` are re-pointed at `ruleengine.EvalResult`/`ruleengine.NodeEntry` **in Fire 1** (so the package still compiles), then deleted wholesale in Fire 3.

> **Note on `ProjectionResult` vs `EvalResult`.** `ruleengine` will briefly host both the full engine's return row (`ProjectionResult{Key,Values,Delete}`) and the pipeline's carrier (`EvalResult{Delete,Keys,Row,ProjectionSeq}`); the pipeline converts one to the other in `executeFullForActor`. Collapsing them into one type is a **deliberate non-goal** of this design: it would change the full engine's return contract and every write-loop site, adding risk to a mechanical retirement. `EvalResult` = `ProjectionResult` + the pipeline-supplied `ProjectionSeq` guard token; the mild redundancy is acceptable and flagged as an optional future cleanup, not folded in here (avoid over-engineering a retirement).

`simple.Column` is **not** moved — its only consumers are the simple `validate` path and `QueryPlan`, both deleted. It goes with Category A.

### 2.3 The invalidation-forest scaffolding (Category C → deleted; D1)

`projection/plan.go`'s `Compile` loses its two `simple.*` calls (`AnalyzeInvalidationCoverage`, `CompileInvalidationForest`). The `ProjectionPlan.Invalidation` field and `InvalidationPlan` type are removed (nothing consumed them). `InstallActorAggregate` keeps calling `Compile` for the Output-descriptor validation + auth-plane classification it *does* use, minus the forest/coverage branch — its BFS-enumerator wiring is untouched, so **runtime behavior is identical**. Tests that assert forest/coverage behavior (`projection/oracle_test.go`; the coverage/forest cases in `projection/plan_unit_test.go`) are removed; the Output-descriptor / envelope / empty-behavior / soft-delete / guard tests (`driver_test.go`, `softdelete_test.go`, `output.go`, the descriptor cases in `plan_unit_test.go`) **stay** — they test live machinery.

### 2.4 The simple runtime branches (Category A → deleted)

| Site | Today | After |
|---|---|---|
| `pipeline/evaluate.go` `evaluateForEntry` | `switch p.engineKind { case EngineFull: …; default: simple.Evaluate(…) }` | full-only; the `default` (simple) branch + its envelope-wrap block deleted |
| `pipeline/pipeline.go` | `plan *simple.QueryPlan`, `currentPlan()`, `HotReloadPlan()` fields/methods | removed (full path never touches `plan`) |
| `cmd/refractor/main.go` `startPipeline` | builds a `simple.QueryPlan` when `ResolvedEngine ∈ {simple, ""}` | removed; full lenses need no plan (comment already says so) |
| `cmd/refractor/main.go` hot-reload | `default:` branch re-compiles a simple plan + `HotReloadPlan` | removed; only the full-engine `UseFullEngine` reload remains |
| `control/service.go` `validate` | full lens → "not available" note; simple lens → sample-and-field-report via `simple.Parse/Compile` | always returns the "field-level validation is not available for the openCypher engine" note; `simple.Column`/`buildEmptyValidateResult`/sampling deleted |
| `lens/schema.go` | `NewRegistry(simple.New(), full.New())`; `""`/`simple` accepted in `Parse`'s switch; simple post-parse `Compile` validation | `NewRegistry(full.New())`; `Parse` accepts `full` or `""` (both → full); simple post-parse block deleted |
| `ruleengine.go` `SelectForLens` | explicit-simple / explicit-full / absent-fallback (simple→full) | `""`/`"full"` → full; anything else (incl. `"simple"`) → clear `SelectionError` ("unknown engine %q"). `EngineSimple` constant removed |
| `fixture/` package | legacy `node_<label>_<id>`-keyed simple-engine fixtures (test-only helper) | package **deleted** (it tests only the simple engine; the full engine's coverage lives in `ruleengine/full/*_test.go` + `refractor_e2e_test.go`) |
| `ruleengine/simple/` | 8 source files + tests | directory **deleted** |

### 2.5 Orchestration / precedent mirrored

No orchestration (no Loom pattern, Weaver lens, `@at`/`@every`, or directOp). The precedent this mirrors is the **prior engine-boundary hygiene** already in the codebase: `ruleengine.go`'s package doc already declares the neutral-interface intent (*"supporting types shared by … MUST NOT leak"*) — this design finishes that job by lifting the two leaked carrier types up and removing the second engine. The Category-C deletion mirrors the house **dead-scaffolding discipline** (`feedback_designer_chain_grounding` / `feedback_fewer_larger_fires`): retire machinery whose consumer was never built rather than relocate it.

---

## 3. Contract surface

**No `docs/contracts/*` change.** Verified by grep (§ For-Andrew). The only doc edit is `docs/components/refractor.md` (a component reference, not a frozen contract):

- **§ Rule engine** — collapse the two-engine subsection into a single-engine description; delete "Simple engine (`ruleengine/simple/`)", the "Engine selection algorithm" fallback steps, and the "available only in the full engine" contrasts (there is no other engine to contrast against).
- **§ In-contracts** — the "Lens meta-vertices" row's *"Engine absent = simple-then-full fallback; `"simple"` = simple engine"* becomes *"`engine` must be `"full"` (or absent → full); any other value fails lens validation."*
- **§ Principles** — the "simple engine is legacy-fixture support only" principle is removed (obsolete once it's gone); the "openCypher full engine is canonical" principle stays.
- **What-this-component-owns table** — the `ruleengine/` row drops the `simple/` sub-package mention.

The `lens.Rule.RuleEngine` YAML field is **kept** (accepts `"full"`/absent) for backward compatibility with any hand-authored spec that names `full` explicitly, and to leave a clean seam if a future engine ever lands — but `"simple"` now fails validation with a clear message rather than silently selecting a deleted engine.

---

## 4. Migration & compatibility

- **No data migration.** Lens specs already all say `engine: full`; nothing in Core KV changes. No bucket, no re-projection, no bootstrap version bump.
- **Behavioral compatibility.** The live path is unchanged, so a running stack projects identically before/after. A hypothetical operator-authored `engine: simple` lens would now fail validation at load with `unknown engine "simple"` (instead of selecting the legacy engine) — this is the intended, documented retirement, not a silent regression, and no such lens exists in-repo.
- **Rollback.** Each fire is an independent, green, revertible commit; the whole retirement is `git revert`-able, and the deleted simple/forest code is preserved in history if ever needed.

---

## 5. Test strategy

- **Fire 1 (type move):** the existing pipeline + `ruleengine` + fixture tests must stay green after the `simple.EvalResult/NodeEntry` → `ruleengine.*` rename — this is the proof the move is behavior-preserving. No new tests (pure relocation).
- **Fire 2 (forest delete):** delete `projection/oracle_test.go` + the forest/coverage cases in `plan_unit_test.go`; the retained `projection` tests (`driver_test.go`, `softdelete_test.go`, descriptor cases) must stay green — proving the live actor-aggregate path (envelope, empty-behavior, guard, BFS fan-out) is untouched. Add one assertion that an auth-plane actor-aggregate lens with a previously-"uncovered" MATCH still **registers** (via BFS) — locking in that removing the advisory gate didn't start refusing lenses.
- **Fire 3 (engine delete):** `ruleengine/selection_test.go` reworked to the full-only registry — `""`→full, `"full"`→full, `"simple"`→`SelectionError`. The `refractor_e2e_test.go` + `ruleengine/full/*` suites (the real full-engine coverage) must stay green. `make verify-package-*` for every touched package's DDL is unaffected (no DDL change) but run per the house gate. Full gate set: `go build ./...`, `make vet`, `golangci-lint run ./...`, `make verify-kernel`, `make test-bypass`, `make test-capability-adversarial`, and `go test ./internal/refractor/... ./cmd/refractor/...`.
- **Grep-clean gate.** After Fire 3, `git grep 'ruleengine/simple'` and `git grep -w EngineSimple` return **nothing** — the mechanical proof the retirement is complete.

---

## 6. Risks & alternatives

| Risk | Mitigation |
|---|---|
| A hidden consumer of `simple.*` outside what I grepped (e.g. a generated file, a build tag) | The Fire-3 grep-clean gate + `go build ./...` are exhaustive; the compiler finds every reference. The cross-package survey in §1.3 was `git grep`-derived, not memory. |
| Deleting the coverage gate weakens an auth-plane safety net | It is not a net — `driver.go:168-182` already overrides it to BFS. BFS is the sound superset (never under-reprojects), so the security posture is *stronger* than the precise forest, not weaker. Fire-2's registration test locks this in. |
| The precise-invalidation optimization is wanted later | Out of scope and unbuilt; both ratified designs declined it. git preserves the code; a future build would re-author it against the full AST (cleaner). Documented in D1. |
| Loupe / Health surfaces expect an engine name other than "full" | `reporter.SetRuleEngine(r.ResolvedEngine)` will always emit `"full"` — a valid, existing value; no consumer branches on `"simple"`. |

**Alternative to D1 (Option 2 — relocate the forest).** Move Category C into a new `internal/refractor/invalidation` package that depends only on `full`, porting the borrowed `QueryPlan`/`TraversalStep`/`EdgeDirection`/`reverseTraverse` (~150 LOC) out of simple. *Rejected as the default* because it keeps ~650 LOC of consumer-less machinery alive, requires a delicate port of the simple evaluator's reverse-traversal internals (the exact code being retired), and preserves dead scaffolding against the house rule. Retained here as the fallback if Andrew wants the precise-invalidation groundwork kept rather than git-preserved.

**Alternative: keep the simple engine.** Rejected — it's the maintenance liability this item exists to remove; every future Refractor refactor pays the two-engine tax, and it owns types the pipeline needs (so "just ignore it" isn't available).

---

## 7. Fire-by-fire decomposition (for the Lattice Steward)

Each fire is independently shippable and green. Order is dependency-forced: B before C before A.

> **Checkpoint — Fire 1 SHIPPED (`970585f`).** Fire 2 SHIPPED 2026-07-01: deleted
> `ruleengine/simple/invalidation_{coverage,plan}.go` (+ their tests) and
> `projection/oracle_test.go`; trimmed `plan.go` (`ProjectionPlan.Invalidation`,
> `InvalidationPlan`, `CompileError`, `Logger` all removed — `Compile` now just
> validates the descriptor + classifies auth-plane); `driver.go`'s
> `InstallActorAggregate` simplified to match (no more `errors.As(*CompileError)`
> branch — always BFS, as it already effectively was). Added
> `TestCompile_AuthPlaneUncoveredMatch_StillRegisters` pinning that an auth-plane
> lens with a previously-"uncovered" MATCH still compiles (the §8 adversarial
> finding: the gate was already inert, BFS always won). Full `go test ./...`,
> `go vet`, `golangci-lint`, `lint-conventions` green; grep-clean for the deleted
> symbols. Next: Fire 3 (delete the simple engine itself).

### Fire 1 — Lift the neutral carrier types into `ruleengine`
- Add `EvalResult` + `NodeEntry` to `ruleengine` (package `ruleengine`), verbatim from `simple/evaluator.go`.
- Re-point the simple engine's own `Evaluate`/`deleteResult`/`invalidation_*` at `ruleengine.EvalResult`/`ruleengine.NodeEntry` (keep simple compiling).
- Update `pipeline/{pipeline,evaluate}.go` + their tests: `simple.EvalResult`→`ruleengine.EvalResult`, `simple.NodeEntry`→`ruleengine.NodeEntry`.
- **Green when:** whole `go test ./internal/refractor/...` passes with no behavior change. Pure relocation.

### Fire 2 — Delete the dead invalidation-forest scaffolding (D1)
- Delete `ruleengine/simple/invalidation_coverage.go` + `invalidation_plan.go`.
- Trim `projection/plan.go`: drop the `Invalidation`/`InvalidationPlan`/`Forest` field + the two `simple.*` calls in `Compile`; keep Output-descriptor validation + `AuthPlane` classification.
- Delete `projection/oracle_test.go` + the forest/coverage cases in `plan_unit_test.go`; add the "auth-plane uncovered-MATCH lens still registers via BFS" assertion.
- `InstallActorAggregate` (`driver.go`) unchanged except it no longer references the removed field.
- **Green when:** `projection` + `refractor_e2e` suites pass; live actor-aggregate behavior identical. Removes simple's `QueryPlan`/`TraversalStep`/`reverseTraverse` external consumers.

### Fire 3 — Delete the simple engine
- Remove every Category-A branch per §2.4 (pipeline default + `plan`/`currentPlan`/`HotReloadPlan`; `main.go` plan-build + hot-reload default; `control` simple `validate`; `lens/schema.go` registry + simple validation; `ruleengine.go` selection simplification + `EngineSimple` removal).
- Delete the `internal/refractor/fixture` package and `internal/refractor/ruleengine/simple/` directory.
- Rework `ruleengine/selection_test.go` to the full-only registry.
- Update `docs/components/refractor.md` per §3.
- **Green when:** full gate set passes; `git grep 'ruleengine/simple'` and `git grep -w EngineSimple` are empty.

---

## 8. Adversarial pre-build gate (discharged)

This is a substantial cross-cutting deletion on the read side, so I ran the design's own adversarial pass before calling it build-ready (obligation per the Designer skill), against the two failure modes a retirement most often trips:

1. **"Is a live path secretly using the thing you're deleting?"** — Checked each of the three categories against `git grep` for *runtime* (non-test) callers: Category A's `simple.Evaluate` is reachable only via `ResolvedEngine ∈ {simple,""}`, and no installed lens resolves to either (§1.2). Category C's `AffectedAnchors` has zero non-test callers, and its coverage result is consumed only by an override-to-BFS branch (§1.4). Category B is the *only* genuinely-live surface — hence it is **moved, not deleted**. Verdict: no live path depends on deleted code.
2. **"Does deleting the coverage gate silently over-grant / mis-project on the security plane?"** — The gate's fail-closed is already inert (overridden to BFS in `driver.go`), and BFS is the sound superset — it *cannot* under-reproject, so a capability lens can never miss an authorization change. Removing the gate cannot open an over-grant window (the failure mode for a *retraction* miss); if anything it removes a misleading "fail-closed" comment that doesn't match runtime. Verdict: no security regression; Fire-2's registration test pins the invariant.

Both findings are folded into §2.3 / §6 above. No further party-mode pass required — the change is deletion-shaped with an unchanged live path, not a new mechanism.
