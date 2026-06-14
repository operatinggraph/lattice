# Story 12.3: Projection plan compiler + `projectionKind: actorAggregate` marker (D-PIPELINE core)

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As a platform developer,
I want Refractor to compile an actor-aggregate lens into a `ProjectionPlan{Execution, Invalidation, Output}` from declarative contract data, plus a `projectionKind: "actorAggregate"` opt-in marker,
so that per-actor projection behavior becomes **data** (lens-definition aspects) rather than core Go keyed on a lens canonical name — and 12.4 can then delete the per-name `switch` and the bespoke wrappers.

## ⭐ Scope framing — READ THIS FIRST (the single most important thing to get right)

**12.3 ADDS the machinery. It does NOT migrate the built-in lenses onto it. That is 12.4.**

- This story builds: (a) a **projection plan compiler** that turns a `meta.lens` definition into a `ProjectionPlan{Execution, Invalidation, Output}`, and (b) the `projectionKind: "actorAggregate"` aspect + the Output descriptor parsing, and (c) the fail-closed activation policy.
- The existing per-`CanonicalName` `switch` in `cmd/refractor/main.go` (`capability` / `capabilityEphemeral` / `myTasks` / `capabilityRoleIndex`) **STAYS EXACTLY AS IT IS** and **still drives all four built-in lenses unchanged**. The wrappers in `internal/refractor/capabilityenv/` are **NOT deleted**. The broad `ActorEnumerator` BFS is **NOT removed** from any built-in lens's runtime path.
- **There is ZERO behavior change to the built-in lenses in this story.** The new compiler path is added and proven by its own unit/integration tests (the spike's oracle pattern, now wired to the REAL production reverse-walk functions), but it is **not yet wired to replace the wrappers / fan-out**. 12.4 flips them over and deletes the switch.
- Consequence for verification: because built-in behavior is unchanged, **all existing gates (Gate 2 BLOCKED, Gate 3 DEFENDED, conformance, my-tasks E2E) must stay green as a pure regression net.** The NEW value is proven by NEW tests against the NEW compiler.

This framing is load-bearing. A dev agent that "helpfully" wires the compiler into the live pipeline or touches the switch in 12.3 has done 12.4's job and broken the staged migration. Do not.

## Acceptance Criteria

> The backbone is `_bmad-output/planning-artifacts/epics/phase-2-epics.md` § "### Story 12.3". The spike's GO decision and hard inheritances are `docs/decisions/12.2-invalidation-compiler-spike-report.md`. Build TO the RATIFIED Contract #6 §6.13 + §6.3 (`docs/contracts/06-capability-kv.md`).

**AC1 — `ProjectionPlan` type + compiler entry point.**
**Given** a `meta.lens` definition that declares `projectionKind: "actorAggregate"`
**When** Refractor compiles it
**Then** a `ProjectionPlan{Execution, Invalidation, Output}` value is produced where:
- **Execution** = evaluate the lens for a bound `$actorKey` (the existing per-actor full-engine eval via `full.Engine.ExecuteWith`; this story does NOT change that path — it references it).
- **Invalidation** = the compiled reverse-traversal plan from 12.2 (the per-branch forest, below) that derives affected anchors from a changed vertex / link / aspect. In 12.3 this is a compiled, callable artifact + tested; it is NOT yet wired to replace the live BFS for any built-in lens (that is 12.4).
- **Output** = the declarative Output descriptor (AC4).

**AC2 — `projectionKind: "actorAggregate"` marker (Contract #6 §6.13).**
**Given** a `meta.lens` aspect set
**When** the lens definition carries `projectionKind: "actorAggregate"`
**Then** the lens is recognized as an actor-aggregate and routed to the plan compiler. Absence of the marker (or any other value) means the lens is NOT actor-aggregate and the compiler does not claim it — a non-actorAggregate lens is untouched by this story. The marker is a new declarative aspect read from the lens definition; do **not** key any behavior on `canonicalName`.

**AC3 — Invalidation compiler: per-branch FOREST, REAL reverse-walk, fail-closed (the spike's hard inheritances).**
**Given** the full-engine AST of an actor-aggregate lens (parsed via `full.Engine.Parse` over the LIVE spec, not a snapshot)
**When** the invalidation plan is compiled
**Then**:
- (a) **Per-branch segmentation is MANDATORY.** The AST MATCH/OPTIONAL-MATCH patterns are compiled into a **FOREST of per-branch linear anchor→leaf chains** (following From→To variable chaining from the anchor variable), and the reverse-walk is run **per branch** and unioned. A single flat `Steps` slice is **UNSOUND** (the spike's empirically-caught missed-revocation bug on `capabilityEphemeral`/`link_assignedTo`). Branch length is capped at `pipeline.DefaultActorMaxDepth` (10) as the cycle guard.
- (b) **The REAL reverse-walk is wired** — `reverseTraverse` / `walkBackToAnchor` / `filterEdges` / `reverseDirection` (currently UNEXPORTED in `package simple`, in `internal/refractor/ruleengine/simple/evaluator.go`). The compiler MUST call the production functions, NOT a copy. **Do NOT ship the spike's `reverse_copy.go`.** The spike package (`internal/spike/invalidation-compiler/`) stays untouched. See "Where the compiler lives" in Dev Notes for the export-vs-relocate decision.
- (c) **Re-referenced-anchor label backfill.** A re-referenced node in an `OPTIONAL MATCH` (e.g. `(identity)<-[:reportsTo]-(report:identity)…` in `capabilityEphemeral`) carries no inline `NodePattern.Label`; the compiler must recover it from the variable's introducing binding (the spike's `labelOf` map), or reverse-walk direction/label matching is wrong.
- (d) **Direction mapping is load-bearing:** `full.DirOut → simple.Outbound`, `full.DirIn → simple.Inbound`, `full.DirBoth → simple.Both`.
- (e) **Fail-closed on the auth plane:** an **auth-plane** `actorAggregate` lens whose MATCH uses a construct the compiler cannot prove subset-safe (see the covered/not-covered table in Dev Notes) **FAILS ACTIVATION** — the lens is refused, loudly logged; it does NOT silently fall back to BFS. A **non-auth** `actorAggregate` lens with an uncovered construct logs a **fallback-to-BFS warning** and the plan records that it must use broad BFS. (How "auth-plane" is determined is an Open Question — see below; default proposal: a declarative aspect on the lens, not a hardcoded name list.)

**AC4 — Output descriptor read from lens-definition aspects (Contract #6 §6.13 table).**
**Given** the lens definition aspects
**When** the plan compiles
**Then** the Output descriptor is read declaratively and covers every behavior the four Go wrappers (`internal/refractor/capabilityenv/envelope.go`) currently encode:
- `anchorType` — the actor vertex type (or inferred from `MATCH (x:identity {key:$actorKey})`).
- `outputKeyPattern` — a **constrained** key template, e.g. `cap.ephemeral.{actorSuffix}` (the `{actorSuffix}` is the actor key with the `vtx.` prefix stripped — mirrors `capabilityenv.EphemeralKey` / `MyTasksKey`). "Constrained" = a closed set of allowed placeholders (at minimum `{actorSuffix}`), validated at compile time; reject unknown placeholders.
- `bodyColumns` — which RETURN aliases form the document body (mirrors how each wrapper picks `ephemeralGrants` / `openTasks` / etc. into the envelope).
- `emptyBehavior` — one of `delete` | `softDelete` | `emptyDoc` | `skip` (empty-result handling). Maps to the wrappers' current `ErrDeleteProjection` (= `delete`/`softDelete` per bucket deleteMode) / `ErrSkipProjection` (= `skip`) behavior.
- `realnessFilter` — `{ field }`: drop degenerate `collect` artifacts by a non-empty key field. Generalizes `capabilityenv.realEphemeralGrants` / `realOpenTasks` (both filter on non-empty `taskKey`).
- `freshness: auto` — stamp `projectionSeq` (per 12.1, the §6.2 guard) + the widened `projectedFromRevisions` (§6.3, AC5).
The descriptor + its parsing/validation are unit-tested. The descriptor is **data the compiler produces and tests consume**; it is NOT yet driving the live envelope for any built-in lens (12.4).

**AC5 — `projectedFromRevisions` widening (Contract #6 §6.3, Request 6) + the v1 scope decision (MUST be stated).**
**Given** an actor-aggregate plan with `freshness: auto`
**When** the plan's Output is exercised
**Then** `projectedFromRevisions` is widened to cover the **contributing source set the plan read** — the actor's identity vertex, the lens-definition vertex, AND the roles/tasks/services/links that **contributed a binding**. (Phase 1 stamped only actor + lens-def.) This is the **coherence/debug** datum, **NOT** the ordering guard (that is `projectionSeq`, 12.1a — do not conflate).

> **⭐ v1 SCOPE DECISION (stated per the epic AC's requirement) — DEFERRED to a 12.3-follow-up:**
> v1 covers sources that **contributed a binding** (sources the compiled plan actually bound and read into a row). Covering sources that were **read-then-excluded** (e.g. a now-closed task that the executor matched then dropped via a WHERE / realness filter) requires **full-executor touched-then-dropped instrumentation** — the full executor reporting every Core-KV key it touched-then-dropped. That instrumentation is **NOT in scope for 12.3** and is **deferred to a 12.3-follow-up story**.
> **Rationale (flagged for Winston):** the contributing-binding set is derivable from the same bindings the Execution path already materializes (the compiler/executor already knows which neighbor keys it fetched into the winning rows). The read-then-excluded set requires threading a new "touched keys" accumulator through `full`'s executor evaluation — a non-trivial cross-cutting change to the hot eval path, with its own correctness/perf review surface. Bundling it here would balloon a security-plane story. It is a debug/coherence nicety, not a correctness or ordering guarantee, so deferral carries no auth risk. **Winston: confirm deferral, or pull it in if the executor-instrumentation piece turns out small.**

**AC6 — Fail-closed activation enforcement (the auth-plane guarantee).**
**Given** an `actorAggregate` lens whose MATCH uses an unsupported construct
**When** activation/compilation runs
**Then** it **fails activation** if the lens is an auth-plane lens (fail closed — refuse to register), and logs a **fallback-to-BFS warning** if it is not. A missed-revocation risk is never traded for availability — a compile failure on a security lens is loud, never a silent BFS fallback. 12.3 OWNS this enforcement (the spike only recorded the policy).

**AC7 — `emptyBehavior: softDelete` reuses 12.1a's tombstone (one mechanism, not two).**
**Given** `emptyBehavior: softDelete`
**When** the plan signals an empty result
**Then** it reuses the **same** soft-tombstone mechanism already in `internal/refractor/adapter/natskv.go` (`guardedBody` / the `{isDeleted:true, projectionSeq}` soft tombstone via `guardedWrite`). Do NOT introduce a second tombstone path. (Note: the built-in lenses default to HARD delete today; `softDelete` is a descriptor option the compiler maps onto the existing mechanism, exercised by a new test — it does not change any built-in lens's delete mode.)

**AC8 — No behavior change to the built-in lenses; the switch still drives them.**
**Given** this story is "add the machinery, do not migrate"
**Then** the per-`CanonicalName` `switch` in `cmd/refractor/main.go` is **unchanged**, the `internal/refractor/capabilityenv/` wrappers are **not deleted**, the broad BFS still drives the built-in lenses' fan-out, and **every existing gate stays green** (Gate 2 all BLOCKED, Gate 3 all DEFENDED, Contract #6 §6.2/§6.6 conformance, my-tasks E2E). The NEW compiler is proven by NEW unit/integration tests, not by re-wiring production.

## Tasks / Subtasks

- [x] **Task 1 — Decide + execute where the compiler lives, and expose the real reverse-walk (AC3b).** (AC: 3)
  - [x] Adopt the **export-in-`package simple`** approach (see Dev Notes "Where the compiler lives" — recommended): add exported wrappers/aliases in `package simple` for the reverse-walk so an in-package or sibling compiler can call them, OR site the new compiler inside `package simple` so it calls the unexported funcs directly. Do NOT duplicate the algorithm.
  - [x] Confirm the spike package `internal/spike/invalidation-compiler/` is left untouched; the production compiler does not import it.
- [x] **Task 2 — `ProjectionPlan` type + invalidation forest compiler (AC1, AC3).** (AC: 1, 3)
  - [x] Define `ProjectionPlan{Execution, Invalidation, Output}` in the chosen package.
  - [x] Port the spike's `CompilePlan` / `buildBranches` / `mapDirection` / `labelOf` logic to production (parsing the LIVE spec via `full.Engine.Parse`), producing the per-branch forest of `simple.QueryPlan`s.
  - [x] Wire the **production** reverse-walk per branch (Task 1), unioning affected anchors. Re-referenced-anchor label backfill (AC3c). Direction mapping (AC3d). Branch cap = `pipeline.DefaultActorMaxDepth`.
- [x] **Task 3 — `projectionKind` marker + Output descriptor parsing/validation (AC2, AC4).** (AC: 2, 4)
  - [x] Read `projectionKind` from the lens definition aspects; route only `"actorAggregate"` to the compiler.
  - [x] Parse the Output descriptor aspects (`anchorType`, `outputKeyPattern`, `bodyColumns`, `emptyBehavior`, `realnessFilter`, `freshness`) per the §6.13 table. Validate: `outputKeyPattern` placeholders are a closed set; `emptyBehavior` ∈ {delete,softDelete,emptyDoc,skip}; `realnessFilter` names a field; `freshness` is `auto`.
  - [x] Decide where these aspects are sourced (Open Question — `lens.LensSpec` JSON shape vs a new aspect class). Surface the chosen shape clearly; this is what 12.4's lens re-declaration will populate.
- [x] **Task 4 — Fail-closed activation policy (AC3e, AC6).** (AC: 3, 6)
  - [x] Build the covered/not-covered construct check (the Dev Notes table). On an uncovered construct: auth-plane ⇒ fail activation (return error, loud log); non-auth ⇒ warn + mark plan as BFS-fallback.
  - [x] Determine "auth-plane" declaratively (Open Question — proposed: an aspect, not a name list).
- [x] **Task 5 — `projectedFromRevisions` widening to the contributing-binding set (AC5).** (AC: 5)
  - [x] Widen the descriptor's `freshness: auto` output to stamp the contributing source set (actor + lens-def + bound roles/tasks/services/links). Read-then-excluded sources are DEFERRED (state the deferral in code-adjacent docs, not as a history comment).
- [x] **Task 6 — `emptyBehavior: softDelete` ↔ 12.1a tombstone (AC7).** (AC: 7)
  - [x] Map `softDelete` onto the existing `natskv.go` `guardedBody` soft-tombstone path. No second mechanism.
- [x] **Task 7 — Tests: the spike oracle, now against the REAL functions (AC1, AC3, AC4, AC8).** (AC: 1, 3, 4, 8)
  - [x] Port the spike's equivalence oracle to a production test: compiled affected-anchor set ⊆ broad `ActorEnumerator` BFS (strict on ≥1 event), AND contains every actor whose reprojected output changed (reproject-and-diff via `full.Engine.ExecuteWith`), for vertex / link / aspect CDC events, on `myTasks` and `capabilityEphemeral`. Reproduce the `capabilityEphemeral`/`link_assignedTo` `compiled=2, changed-output=2` no-missed-anchor result (the load-bearing row).
  - [x] Unit tests for Output-descriptor parsing/validation, fail-closed activation (auth vs non-auth), `softDelete` tombstone reuse, and the per-branch forest shape (assert the exact branch step lists from the spike report).
  - [x] Negative test: a flat-plan (non-forest) compile would MISS the manager anchor — assert the forest compiler does not (guards against regression to the unsound design).
- [x] **Task 8 — Regression net + DoD gates (AC8).** (AC: 8)
  - [x] Confirm `cmd/refractor/main.go` switch and `capabilityenv/` are untouched.
  - [x] Run the full DoD gate set (Dev Notes → Definition of Done).

## Dev Notes

### Where the compiler lives (export vs relocate) — RECOMMENDATION

The reverse-walk functions (`reverseTraverse`, `walkBackToAnchor`, `filterEdges`, `reverseDirection`) and the plan types (`QueryPlan`, `TraversalStep`, `EdgeDirection`, `Column`) are all in **`package simple`** (`internal/refractor/ruleengine/simple/{evaluator,plan}.go`). The forest compiler consumes the `full` AST (`internal/refractor/ruleengine/full/ast.go`) and emits `[]*simple.QueryPlan`.

**Recommended: site the invalidation forest compiler inside `package simple`** (a new file, e.g. `invalidation_plan.go`) so it calls the existing unexported `reverseTraverse`/`walkBackToAnchor`/`filterEdges`/`reverseDirection` **directly** — zero new exported surface on the reverse-walk, zero algorithm duplication, and `simple` already owns `QueryPlan`/`TraversalStep`. `package simple` importing `package full` is acyclic (full does not import simple). The `ProjectionPlan`/Output-descriptor + `projectionKind`/activation-policy layer (which needs `pipeline`/`adapter`/`lens` concepts) can live in a higher package (e.g. a new `internal/refractor/projection/` package or alongside the wiring) that calls into the `simple`-sited forest compiler for the Invalidation half. **Avoid exporting the reverse-walk** unless a cross-package call genuinely forces it — exporting widens the security-critical surface for no benefit here.

Rationale this beats the alternatives: relocating the reverse-walk OUT of `simple` would churn the live evaluator (`Evaluate` calls `reverseTraverse` at `evaluator.go:74`) for no gain; exporting the four funcs adds public API to a security-plane package that only one caller needs. Co-siting the compiler is the least-surface change. **Winston: confirm.**

### The spike is the spec — what 12.3 inherits (hard requirements)

Source: `docs/decisions/12.2-invalidation-compiler-spike-report.md` (GO, with one mandatory correction). The spike's `internal/spike/invalidation-compiler/compiler.go` is the validated reference for `CompilePlan`/`buildBranches`/`mapDirection`/`labelOf` — **port its logic, do not import it, do not ship its `reverse_copy.go`.**

1. **Per-branch FOREST, never a flat `Steps` slice.** Flattening interleaves disjoint branches and a delegation-branch leaf reverse-walks back through the direct branch → drops the manager anchor → **missed revocation on the auth plane**. The spike caught this empirically. Forest is a HARD requirement, not an optimization.
2. **REAL reverse-walk** (Task 1 above).
3. **Label backfill** for re-referenced anchors (`labelOf`).
4. **Fail-closed on auth** (AC6).
5. **Compile the LIVE specs** via `full.Engine.Parse` over `packages/orchestration-base/lenses.go` (`myTasksSpec`, `capabilityEphemeralSpec`) — not a pinned snapshot.

### Expected compiled forest (assert these in tests — from the spike report)

`myTasks` (2 branches):
- branch 0: `(identity)<-[assignedTo]<-(task)`, `(task)->[forOperation]->(op)`
- branch 1: `(identity)<-[assignedTo]<-(task)`, `(task)->[scopedTo]->(tgt)`

`capabilityEphemeral` (4 branches — direct + delegation, each split by the two leaf hops):
- branch 0: `(identity)<-[assignedTo]<-(task)`, `(task)->[forOperation]->(op)`
- branch 1: `(identity)<-[assignedTo]<-(task)`, `(task)->[scopedTo]->(tgt)`
- branch 2: `(identity)<-[reportsTo]<-(report:identity)`, `(report)<-[assignedTo]<-(task2)`, `(task2)->[forOperation]->(op2)`
- branch 3: `(identity)<-[reportsTo]<-(report:identity)`, `(report)<-[assignedTo]<-(task2)`, `(task2)->[scopedTo]->(tgt2)`

`<-[:assignedTo]-` / `<-[:reportsTo]-` hops are `DirIn → simple.Inbound`; `-[:forOperation]->` / `-[:scopedTo]->` leaf hops are `DirOut → simple.Outbound`.

### Covered vs NOT-covered openCypher constructs (drives fail-closed — from the spike report)

**Covered (subset-safe):** `MATCH`/`OPTIONAL MATCH`; node labels (with re-referenced-anchor backfill); rel names + directions; bounded variable-length hops within the `DefaultActorMaxDepth` cap; **multi-branch patterns ONLY via per-branch segmentation**; narrowing `WHERE` (ignored — dropping it only enlarges toward the BFS superset, never misses an anchor); simple path-preserving `WITH` (pass-through); `collect(...)` / `collect(...) + collect(...)` in RETURN (irrelevant — shapes output, not reachability).

**NOT covered (⇒ fail-closed for auth lenses):** broadening `WHERE` (e.g. `OR` pulling in an unrelated path; a predicate the compiler can't bound); undirected hop (`-[:t]-`, `full.DirBoth`) for auth; aggregation that changes the SOURCE set (`collect` feeding a later MATCH, `count` gating a path); computed/synthetic relationships, `UNION`, subqueries, pattern predicates in WHERE asserting existence of OTHER paths.

### What the four Go wrappers encode (the Output descriptor must reproduce these in 12.4 — read, don't delete, in 12.3)

`internal/refractor/capabilityenv/envelope.go`:
- `NewWrapper` (`capability`): key `cap.identity.<id>` (`capabilityKey`), body = platformPermissions/serviceAccess/ephemeralGrants/roles, `projectedFromRevisions(actor, lensDef)`, drop non-identity / null-actor rows (`ErrSkipProjection`).
- `NewEphemeralWrapper` (`capabilityEphemeral`): key `cap.ephemeral.<id>` (`EphemeralKey`), body = `ephemeralGrants`, `realEphemeralGrants` realness filter on non-empty `taskKey`, zero real grants ⇒ `ErrDeleteProjection` (delete).
- `NewMyTasksWrapper` (`myTasks`): key `my-tasks.identity.<id>` (`MyTasksKey`), body = `openTasks`, `realOpenTasks` realness filter on `taskKey`, zero ⇒ `ErrDeleteProjection`; falls back to `params["actorKey"]` when the collapsed row has null `actorKey` (so the delete is keyed correctly).
- `NewRoleIndexWrapper` (`capabilityRoleIndex`): **NOT an actorAggregate** — keyed by `operationType` (`cap.role-by-operation.<op>`). Out of scope for this story's compiler; gets its own `projectionKind` (e.g. `operationAggregate`) or stays bespoke — **decided in 12.4, not here.**

Map: wrapper key fn → `outputKeyPattern`; envelope body fields → `bodyColumns`; `realEphemeralGrants`/`realOpenTasks` → `realnessFilter:{field:taskKey}`; `ErrDeleteProjection` → `emptyBehavior:delete`(or `softDelete`); `ErrSkipProjection` → `emptyBehavior:skip`; `projectedFromRevisions(...)` → `freshness:auto` (widened, AC5).

### The 12.1a tombstone to reuse (AC7)

`internal/refractor/adapter/natskv.go`: `guardedWrite(ctx, key, row, incomingSeq, delete)` + `guardedBody(row, incomingSeq, delete)`. A guarded delete writes the soft tombstone `{isDeleted:true, projectedAt, projectionSeq}` via the CAS path. `SetGuarded(true)` enables it. `softDelete` maps onto exactly this — do not add a parallel tombstone.

### The BFS the Invalidation plan will (in 12.4) replace — referenced, not removed here

`internal/refractor/pipeline/actor_enumerator.go`: `ActorEnumerator.Enumerate` (undirected adjacency BFS, depth cap `DefaultActorMaxDepth=10`, actor-set cap `DefaultActorMaxSet=10_000`). The spike's oracle uses this as the sound superset to prove `compiled ⊆ BFS`. In 12.3 the BFS stays live for the built-in lenses; the compiled Invalidation plan is built + tested against it but not swapped in.

### Aspect/definition flow (where `projectionKind` + descriptor are read)

- Lens definitions are `vtx.meta.<id>` with `class: "meta.lens"`; the spec aspect lands at `vtx.meta.<id>.spec` and is parsed by `internal/refractor/lens/corekv_source.go` into `lens.LensSpec` (fields: `canonicalName`, `targetType`, `targetConfig`, `cypherRule`, `outputSchema`, `engine`). The runtime `lens.Rule` (`internal/refractor/lens/schema.go`) carries `CanonicalName`, `Match`, `Into`, `ResolvedEngine`, `CompiledRule`.
- The new `projectionKind` + Output-descriptor aspects need a home in this flow. **Open Question** below — propose extending `lens.LensSpec` (and/or `targetConfig`) so the loader surfaces them onto `lens.Rule`, mirroring how `engine` is plumbed. Whatever shape is chosen is what 12.4's lens re-declaration must populate, so make it explicit and stable.
- `full.Engine`: `full.New()` → `Parse(body) (ruleengine.CompiledRule, error)` returning `*full.CompiledRule{Query *full.Query}`; `ExecuteWith(ctx, cr, EventContext{Parameters}, adjKV, coreKV)` is the per-actor eval entry point (the Execution half).

### Code conventions (CLAUDE.md — enforced)

- **No history/changelog comments** (`// Story X`, `// Replaces`, `// Previously`, `// was:`, `// moved from`, etc.). Comments describe what the code does NOW. git blame is the record.
- **Contract #1 key shapes:** aspects `vtx.<type>.<id>.<localName>`; links `lnk.<typeA>.<idA>.<relation>.<typeB>.<idB>`; meta-vertices `vtx.meta.<NanoID>` + `.canonicalName` aspect. Output keys here are projection keys (`cap.*` / `my-tasks.*`), not Contract #1 graph keys.
- **New docs → `/docs`** (close to the code), not `_bmad-output/`.
- **Frozen contracts** (`docs/contracts/*`) are build-to, never edit. A genuine gap → `cmd/<area>/CONTRACT-AMENDMENT-REQUEST.md`. (Contract #6 §6.13/§6.3 are already RATIFIED for this — build to them.)

### Definition of Done — verification gates (CLAUDE.md)

Run and confirm green:
- `go build ./...`
- `make vet`
- `golangci-lint run ./...`
- `make verify-kernel`
- `make test-bypass` — **Gate 2, all BLOCKED** (regression net; built-in behavior unchanged)
- `make test-capability-adversarial` — **Gate 3, all DEFENDED** (regression net)
- Relevant `go test` packages: the NEW compiler package's tests + `internal/refractor/ruleengine/simple/...`, `internal/refractor/ruleengine/full/...`, `internal/refractor/pipeline/...`, and the `my-tasks` E2E (`internal/refractor/refractor_mytasks_e2e_test.go`) + Contract #6 §6.2/§6.6 conformance tests — all green (no behavior change ⇒ no movement).

Because 12.3 adds machinery without rewiring, the gates serve as a **pure regression net**; the NEW compiler is proven by its OWN unit/integration tests (the spike oracle against the REAL functions).

### Project Structure Notes

- New production code: the forest compiler co-sited in `internal/refractor/ruleengine/simple/` (recommended) + the `ProjectionPlan`/descriptor/activation layer in a new package (e.g. `internal/refractor/projection/`) or alongside existing wiring. Confirm the package home with Winston before scattering files.
- DO NOT touch: `cmd/refractor/main.go` (the switch), `internal/refractor/capabilityenv/*` (the wrappers), `internal/spike/invalidation-compiler/*` (the spike), `docs/contracts/*` (frozen), `_bmad-output/planning-artifacts/*`.
- The live evaluator path (`simple.Evaluate` → `reverseTraverse`) must remain behavior-identical; if you co-site the compiler in `package simple`, add a NEW file — do not refactor `evaluator.go`'s existing flow.

### References

- [Source: `_bmad-output/planning-artifacts/epics/phase-2-epics.md`#Story 12.3] — ACs backbone.
- [Source: `docs/decisions/12.2-invalidation-compiler-spike-report.md`] — GO decision, hard inheritances, covered/not-covered table, expected forests, per-event subset numbers.
- [Source: `docs/contracts/06-capability-kv.md`#6.13] — `projectionKind`, Output descriptor table, fail-closed + one-mechanism notes, `capabilityRoleIndex` exclusion.
- [Source: `docs/contracts/06-capability-kv.md`#6.3] — `projectedFromRevisions` widening (Request 6) + contributing-vs-read-then-excluded scope note.
- [Source: `internal/spike/invalidation-compiler/compiler.go`] — reference `CompilePlan`/`buildBranches`/`mapDirection`/`labelOf` (port, do not import).
- [Source: `internal/refractor/ruleengine/simple/evaluator.go`#reverseTraverse] — the REAL reverse-walk to wire (lines ~198–288).
- [Source: `internal/refractor/ruleengine/simple/plan.go`] — `QueryPlan`/`TraversalStep`/`Column`/`EdgeDirection`.
- [Source: `internal/refractor/ruleengine/full/ast.go`] — `Match`/`PathPattern`/`NodePattern`/`RelPattern`/`Direction`.
- [Source: `internal/refractor/capabilityenv/envelope.go`] — the four wrappers the descriptor reproduces (read, don't delete).
- [Source: `internal/refractor/adapter/natskv.go`#guardedBody] — the soft-tombstone to reuse for `softDelete`.
- [Source: `internal/refractor/pipeline/actor_enumerator.go`] — the BFS superset / `DefaultActorMaxDepth`.
- [Source: `cmd/refractor/main.go`#256-343] — the canonical-name switch (UNCHANGED this story).
- [Source: `packages/orchestration-base/lenses.go`] — `myTasksSpec` / `capabilityEphemeralSpec` (the LIVE specs to compile).
- [Source: `internal/refractor/lens/corekv_source.go`#LensSpec, `internal/refractor/lens/schema.go`#Rule] — definition/aspect flow for where `projectionKind` + descriptor are read.
- [Source: `_bmad-output/implementation-artifacts/12-1a-projection-write-guard.md`] — the guard/tombstone implementation 12.3 reuses.

## Open Questions

1. **Where do `projectionKind` + the Output-descriptor aspects live in the lens-definition shape, and how does the loader surface them?** Proposal: extend `lens.LensSpec` (JSON at `vtx.meta.<id>.spec`) and/or `targetConfig` with the §6.13 fields, plumbed onto `lens.Rule` the way `engine` already is. This shape is the contract 12.4's lens re-declaration must populate, so it must be settled in 12.3. (Recommend deciding with Winston before Task 3.)
2. **How is "auth-plane lens" determined for the fail-closed-vs-warn fork (AC6)?** Proposal: a declarative aspect on the lens definition (e.g. `securityPlane: true`), NOT a hardcoded canonical-name list — keying on the name would re-introduce exactly the layering inversion Epic 12 is removing. Needs Winston's sign-off on the aspect name/shape.
3. **`projectedFromRevisions` read-then-excluded coverage — confirm DEFERRAL (AC5).** This story defers the full-executor touched-then-dropped instrumentation to a 12.3-follow-up (rationale in AC5: it's a debug/coherence datum, not auth-critical, and bundling the executor-instrumentation change would balloon a security-plane story). Winston: confirm deferral, or pull it in if the instrumentation proves small.

---

## Adjudication (Winston, 2026-06-14) — build to these

**Rec 1 — `projectedFromRevisions` read-then-excluded → DEFER (confirmed).** v1 covers contributing-binding sources (derivable from what Execution already materializes). The touched-then-dropped executor instrumentation is a 12.3-follow-up: it's a coherence/debug datum, NOT the ordering guard (`projectionSeq`, 12.1a), so deferral carries no auth risk. AC5 states this explicitly. Leave a follow-up note so it isn't lost.

**Rec 2 — site the forest compiler INSIDE `package simple` (confirmed)**, calling the real unexported `reverseTraverse`/`walkBackToAnchor`/`filterEdges`/`reverseDirection` directly. Verified no import cycle: `simple` does not import `full` today and `full` does not import `simple`, so the new `simple→full` edge (to walk the AST) is acyclic — but the dev MUST confirm `go build` stays clean (catches any transitive cycle). If a cycle ever appears, fall back to a narrow export of just those four funcs (NOT relocating them). The `ProjectionPlan`/Output-descriptor/activation layer lives one level up in a new `internal/refractor/projection/` package. Do NOT export the four funcs otherwise and do NOT churn the live evaluator.

**OQ1 — aspect home → extend `lens.LensSpec`, plumbed like `engine`.** Carry `projectionKind` + the §6.13 Output descriptor (`anchorType`/`outputKeyPattern`/`bodyColumns`/`emptyBehavior`/`realnessFilter`/`freshness`) on `LensSpec` (from the `vtx.meta.<id>.spec` aspect), surfaced onto `lens.Rule` exactly as `engine`/`RuleEngine` is. This is the shape 12.4 populates — settle it here.

**OQ2 — auth-plane classification → DERIVE FROM THE TARGET BUCKET, not a new aspect and not a name list.** A lens is **auth-plane ⟺ its target bucket is `capability-kv`** (it projects an authorization surface — `cap.*`, incl. the decomposed `cap.roles.*`/`cap.svc.*` of 12.6/12.7). Auth-plane + uncovered MATCH construct ⇒ **fail activation closed**; non-auth (e.g. the `my-tasks` bucket, correctness plane) ⇒ warn + fall back to broad BFS. Rationale: (a) a canonical-name list re-introduces the exact layering inversion Epic 12 removes — rejected; (b) the proposed explicit `securityPlane` aspect is NOT in the ratified Contract #6 §6.13, and I will not unilaterally add a field to a FROZEN contract — bucket-derivation needs zero contract change and correctly classifies every current + planned auth-plane lens. **Follow-up for Andrew (non-blocking):** if we later want auth-plane to be *explicit* rather than inferred from a bucket name, that's a clean CAR adding a `securityPlane` descriptor field to §6.13 — I'll raise it as a request to ratify before 12.6 if desired; 12.3 does not need it.

**Review depth:** SECURITY-CRITICAL (projection plane) → full 3-layer + Gate 2/3, even though built-in behavior is unchanged (the new compiler is what 12.4 flips the auth lenses onto). Built-in gates are the regression net; the new compiler is proven by the spike's oracle re-run against the REAL functions + new unit/integration tests.

---

## Dev Agent Record

### Completion Notes (Amelia)

12.3 ADDS the machinery only — the built-in lenses stay on the canonical-name switch (12.4 migrates them). No functional change to `cmd/refractor/main.go`'s switch or `internal/refractor/capabilityenv/`; the spike (`internal/spike/`) is untouched and not imported.

- **Forest compiler (AC1, AC3)** lives INSIDE `package simple` (`invalidation_plan.go`) calling the REAL unexported `reverseTraverse`/`walkBackToAnchor`/`filterEdges`/`reverseDirection` directly. `simple→full` confirmed acyclic (`go build ./...` clean). Per-branch FOREST is mandatory: `CompileInvalidationForest` segments the AST into per-branch linear anchor→leaf chains (From→To variable chaining, re-referenced-anchor label backfill via `labelOf`, direction mapping `DirOut→Outbound`/`DirIn→Inbound`/`DirBoth→Both`, branch cap `MaxBranchLen=10` mirroring `pipeline.DefaultActorMaxDepth`). `InvalidationForest.AffectedAnchors` runs the reverse walk per branch and unions.
- **Coverage analyzer (AC3e, AC6)** `invalidation_coverage.go`: walks the full AST for uncovered constructs (undirected `DirBoth` hop, pattern-existence WHERE, aggregation-in-WITH feeding a later MATCH). UNION/CALL/subqueries are rejected at parse by the full engine.
- **`projectionKind` + Output descriptor (AC2, AC4, OQ1)** carried on `lens.LensSpec` + surfaced onto `lens.Rule` exactly like `engine`/`RuleEngine` via `translateSpec`. Descriptor parsing/validation in `projection/output.go`: constrained `{actorSuffix}`-only key pattern, `emptyBehavior ∈ {delete,softDelete,emptyDoc,skip}`, `freshness: auto`, realness filter, body columns.
- **ProjectionPlan + activation policy (AC6, OQ2)** in new package `internal/refractor/projection/` (`plan.go`). Auth-plane ⟺ target bucket `capability-kv` (derived from the bucket, NOT a name list, NOT a new aspect). Auth-plane + uncovered ⇒ `*CompileError` (fail closed); non-auth ⇒ warn + `FallbackToBFS`.
- **`projectedFromRevisions` widening (AC5)** `projection/freshness.go` `ContributingSources`: actor + lens-def + every bound graph key (vtx.*/lnk.*) in the projected rows. Read-then-excluded DEFERRED per adjudication — follow-up note at `docs/decisions/12.3-projected-from-revisions-followup.md`.
- **`softDelete` ↔ 12.1a tombstone (AC7)** `projection/empty.go`: `softDelete`/`delete` map onto the SAME natskv guarded-delete (`{isDeleted:true, projectionSeq}`); no second tombstone path. Proven end-to-end in `softdelete_test.go` against a real guarded `NatsKVAdapter`.
- **Oracle (AC1/AC3 proof)** `projection/oracle_test.go` re-runs the spike's equivalence oracle against the REAL functions + the LIVE orchestration-base specs: compiled ⊆ broad BFS (strict where over-reprojection exists) AND ⊇ every reproject-and-diff-changed actor, for vertex/link/aspect events on `myTasks` + `capabilityEphemeral`. Reproduces the load-bearing `capabilityEphemeral/link_assignedTo compiled=2 changed=2` no-missed-anchor row. `invalidation_plan_test.go` asserts the exact forest shapes (2 / 4 branches) and includes the negative flat-plan-misses-the-manager-anchor regression guard.

### Verification (all green, run inline)

- `go build ./...` — clean (no import cycle).
- `make vet` — clean.
- `golangci-lint run ./...` — 0 issues.
- `make verify-kernel` — ALL ASSERTIONS PASSED.
- `make test-bypass` (Gate 2) — all BLOCKED.
- `make test-capability-adversarial` (Gate 3) — 6/6 (5 DEFENDED, 1 ACCEPTED-WINDOW).
- `go test ./internal/refractor/...` — all packages ok (incl. new `projection`, `simple`, `lens`, and the my-tasks E2E).

### File List

New:
- `internal/refractor/ruleengine/simple/invalidation_plan.go` — in-`simple` per-branch forest compiler + `AffectedAnchors` (calls the real reverse-walk).
- `internal/refractor/ruleengine/simple/invalidation_coverage.go` — covered/not-covered construct analyzer over the full AST.
- `internal/refractor/ruleengine/simple/invalidation_plan_test.go` — forest-shape assertions + flat-plan missed-anchor negative test.
- `internal/refractor/ruleengine/simple/invalidation_coverage_test.go` — coverage analyzer tests.
- `internal/refractor/projection/plan.go` — `ProjectionPlan{Execution,Invalidation,Output}`, `Compile`, auth-plane classification, fail-closed activation.
- `internal/refractor/projection/output.go` — Output descriptor parse/validate, constrained key builder, realness filter.
- `internal/refractor/projection/empty.go` — emptyBehavior → action mapping; softDelete/delete reuse the guarded tombstone.
- `internal/refractor/projection/freshness.go` — `ContributingSources` (`projectedFromRevisions` widening, v1).
- `internal/refractor/projection/oracle_test.go` — equivalence oracle against the REAL functions + LIVE specs.
- `internal/refractor/projection/plan_unit_test.go` — descriptor/activation/provenance unit tests.
- `internal/refractor/projection/softdelete_test.go` — softDelete guarded-tombstone reuse (real adapter).
- `docs/decisions/12.3-projected-from-revisions-followup.md` — AC5 read-then-excluded deferral note.

Modified:
- `internal/refractor/lens/corekv_source.go` — `LensSpec.ProjectionKind` + `LensSpec.Output` (+ `OutputDescriptorSpec`); plumbed onto `Rule` in `translateSpec`.
- `internal/refractor/lens/schema.go` — `Rule.ProjectionKind` + `Rule.Output`.

Untouched (verified by `git diff`): `cmd/refractor/main.go` switch, `internal/refractor/capabilityenv/*`, `internal/spike/*`, `docs/contracts/*`.

### Change Log

- 2026-06-14: Implemented Story 12.3 — projection plan compiler + `projectionKind: actorAggregate` marker (D-PIPELINE core). Machinery only; no built-in-lens migration (12.4). All DoD gates green.

## Code-review triage (Winston, 2026-06-14) — BLOCK; fix-forward required before commit

3-layer review: Acceptance **ACCEPT** (all 8 ACs met, scope/house-rules clean), Blind **BLOCK**, Edge **NON-BLOCKING-FINDINGS** but with two genuine auth-plane soundness gaps. Common theme: the implementation is correct for what its oracle exercises, but **the coverage gate is incomplete** and **the oracle under-tests leaf-vertex invalidation**. Fixes below; all land in 12.3 (the compiler is the security-critical artifact 12.4 + package lenses build on — it must be sound + handle the live lenses before wiring).

**F1 (Edge H2 — must FIX, not fail-close; confirmed against live specs).** The live `capabilityEphemeral`/`myTasks` project leaf data through **unlabeled** leaves (`(task)-[:forOperation]->(op)`, `(task)-[:scopedTo]->(tgt)` — `op`/`tgt` carry no label), so `TraversalStep.ToLabel==""`. `reverseTraverse` skips a step when `ToLabel != entry.NodeLabel`, so a CDC event on an op/tgt vertex yields **zero anchors** → wiring the forest in 12.4 regresses BFS's "actor invalidated" to "missed." Fix: an **empty `ToLabel` matches any label** (unlabeled cypher node = wildcard), still gated by edge-type so it stays precise. Implement so the live simple-engine evaluator's behavior for *labeled* steps is unchanged (regression net = existing `simple` tests); confirm empty-label wildcard doesn't over-match. If touching `reverseTraverse` is too broad, backfill/handle at the forest level — dev picks, but the op-vertex change MUST invalidate the anchor.

**F2 (Blind C1 / Edge M2 — coverage completeness, fail-closed).** A MATCH branch not forward-reachable from the anchor variable is silently dropped from the forest; `analyzeCoverage` has no connectivity check. Add one: every traversal step must appear in ≥1 anchor-rooted branch, else `Covered:false` → auth lens fails activation.

**F3 (Edge H1 / Blind M1 — coverage completeness, fail-closed).** Variable-length hops (`[:rel*m..n]`) are compiled to a single 1-hop step and not flagged. `analyzeCoverage` must inspect `RelPattern.MinHops`/`MaxHops`; anything other than a fixed single hop ⇒ `Covered:false` (fail-closed for auth) unless the reverse-walk provably explores all depths (it doesn't today → reject).

**F4 (Edge M1 — coverage completeness, fail-closed).** RETURN-embedded pattern comprehensions read paths not in any MATCH; the `*full.Return` case is unconditionally "covered." Run `exprHasPattern` over RETURN items → a RETURN pattern comprehension ⇒ `Covered:false`.

**F5 (Blind H1 / Edge L1 — fail-closed).** `MaxBranchLen` (10) truncation silently emits a short branch. On an auth-plane lens, truncation ⇒ `Covered:false` (don't emit an under-covering branch).

**F6 (Edge M4 — availability, don't over-fail-closed).** A sound anchor-only lens (`MATCH (identity {key:$actorKey}) RETURN …`, no rels) currently fails activation ("no traversal branch reaches the anchor"). An anchor-only lens is sound (only the anchor's own vertex change matters, handled by Execution) → it must ACTIVATE, not fail closed.

**F7 (Edge H2-oracle / Blind M2 — oracle non-vacuity).** The oracle's `aspect_task_data` event is byte-identical to `vertex_task`, and NO event mutates an op/tgt leaf vertex — so the `changed ⊆ compiled` (AC3b) assertion never guards the leaf-vertex vector (the F1 bug's blind spot). Add (a) a genuinely-distinct aspect-mutation event, and (b) an **op-vertex (operationType) mutation event** that FAILS against the current code and PASSES after F1 — this is the regression proof for F1.

**F8 (Edge M3 — defensive).** A non-string `realnessFilter` field value silently zeroes the whole projection (over-revocation). Validate the realness field handling / type at descriptor parse or filter time so it's intentional, not accidental.

**F9 (Blind L2/L3 — lint).** `close` shadows the builtin in `validateKeyPattern`; unused `coreKV` binding in the flat-plan test. Trivial.

Routing: through a **dev sub-agent** (security-plane, multi-change, incl. the F1 shared-function fix), then a **focused re-review** (re-run Blind + Edge on the fix — they found the soundness holes) + re-run Gate 2/3 + the new oracle events, before commit. Status stays `review`.
