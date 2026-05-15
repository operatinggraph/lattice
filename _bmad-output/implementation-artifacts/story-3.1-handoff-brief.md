---
title: Story 3.1 Implementation Handoff Brief
story: 3.1 — openCypher `full` Engine Integration into Refractor
model_tier: Opus (locked)
token_budget: ~135K
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-15
predecessor: Story 2.3 pipeline key-shape adaptation (Deviation 13 resolved)
---

# Story 3.1 — openCypher `full` Engine Integration into Refractor: Handoff Brief

## Your Role

You add Refractor's **`full` openCypher engine** alongside the existing simple Materializer-derived engine so the bootstrap-seeded Capability Lens query can parse and execute. This is the first Epic 3 story and the architectural hinge for the authorization perimeter: Story 3.2 cannot activate live Capability Lenses until this engine boundary exists.

Story 2.3 resolved the blocker from the gap analysis: the projection pipeline now consumes Contract-correct `vtx.<type>.<id>` keys via substrate parsers. Assume Lattice key shape is live. Do not revive any legacy `node_<label>_<id>` compatibility.

## MANDATORY OPERATING RULES (READ FIRST)

**Pattern across Phase 1: sub-agents have self-reported tokens 30-50% under outer telemetry.** Story 3.1 is parser + execution architecture, so the risk is slow context creep.

- **At every checkpoint (every 8-10 tool calls OR after any deliverable OR after any file read >25KB):** send a checkpoint message with deliverables done, deliverables remaining, honest token estimate (lower bound, rounded UP).
- **Halt unconditionally if you estimate > 140K used** (about 5% over budget).

Other rules:
- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **No git commits by you.** Winston + Andrew commit.
- **Architecture binding:** `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 and `_bmad-output/planning-artifacts/epics.md` Story 3.1 are source of truth.
- **No Contract amendments expected.** If one emerges, append to `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` and halt for Winston/Andrew.
- **Do not edit planning artifacts silently.** The only planned planning edit is documenting the pinned openCypher dependency in `_bmad-output/planning-artifacts/lattice-architecture.md`. If p95 exceeds budget in the bootstrap query test, update `_bmad-output/planning-artifacts/refractor-gap-analysis.md` with the bottleneck per AC.
- **Token tracker:** add Row 3.1 at session close — honest estimate, round UP.

## Story Scope (Authoritative)

From `_bmad-output/planning-artifacts/epics.md` Story 3.1:

As a platform engineer, Refractor must support a new `full` openCypher rule engine alongside the existing `simple` engine so Lenses requiring WHERE clauses, variable-length path quantifiers, map literals, `WITH`, aggregation, list concatenation, and inbound traversal can execute. The immediate proof is the bootstrap-seeded Capability Lens query in `internal/bootstrap/lenses.go`.

The `full` engine is not "parse-only." It must parse, compile/translate into Refractor-native execution structures, execute against Core KV + Adjacency KV, and produce projection rows suitable for existing adapters.

## What Must Change

Current state:
- `internal/refractor/engine/` is a single package containing simple parser, AST, compiler, plan, and evaluator.
- `engine.Parse` supports `MATCH`, `OPTIONAL MATCH`, simple path patterns, and `RETURN`.
- `engine.Compile` emits a `QueryPlan`.
- `engine.Evaluate` executes that plan using Adjacency KV for edge traversal and Core KV for node property reads.
- Callers directly invoke `engine.Parse` + `engine.Compile`: `cmd/refractor/main.go`, `internal/refractor/lens/schema.go`, `internal/refractor/control/service.go`, `internal/refractor/fixture/runner.go`, `internal/refractor/pipeline/pipeline_test.go`, and e2e/tests.

Required state:
- Existing simple behavior is isolated under `internal/refractor/ruleengine/simple/`.
- New full behavior lives under `internal/refractor/ruleengine/full/`.
- Shared engine contracts live in a neutral package, recommended: `internal/refractor/ruleengine/`.
- Both engines implement one common interface:
  - `Parse(ruleBody string) (CompiledRule, error)` or equivalent
  - `Execute(ctx context.Context, compiledRule, eventContext) (ProjectionResult, error)` or equivalent
- Existing pipelines can keep using a compiled plan shape if you introduce an adapter layer, but the public caller surface must stop depending on the old monolithic `engine.Parse` / `engine.Compile` split.
- Engine selection is per Lens through a new Lens definition field `ruleEngine` with values `simple` or `full`.

## Architectural Decisions Already Made (Winston)

1. **The current engine becomes `simple`.** Preserve its behavior and tests. This story is a package-boundary refactor plus new engine work, not a rewrite of the simple parser.

2. **The `full` engine uses vendored generated parser files copied from `github.com/jtejido/go-opencypher`.** Andrew/Winston decision after brief creation: do not depend on the repo as a Go module and do not regenerate ANTLR output during Story 3.1. The needed generated files are already copied into `internal/refractor/ruleengine/full/cypher/`:
   - `cypher_lexer.go`
   - `cypher_parser.go`
   - `cypher_listener.go`
   - `cypher_base_listener.go`
   - `Cypher.g4` for provenance

   The copied grammar has been regenerated locally with ANTLR 4.13.1. The generated files declare `package cypher` and import `github.com/antlr4-go/antlr/v4`; `go.mod` pins `github.com/antlr4-go/antlr/v4 v4.13.1`. Do not add `github.com/jtejido/go-opencypher` as a module dependency unless Winston explicitly reverses this decision.

3. **ANTLR does not leak past `full`.** Generated parser types stay inside `internal/refractor/ruleengine/full/`. The rest of Refractor consumes Lattice-native interfaces and structs.

4. **Do not use the library as an evaluator.** Use it for parse-tree construction only. Refractor owns semantics because reads must go through Adjacency KV and Core KV exactly as Contract #1 / Story 3.1 specify.

5. **Traversal boundary is strict.** Edge lookups go through Refractor's Adjacency KV. Vertex and aspect data reads go through Core KV. The executor does not read vertex/aspect state from Adjacency KV.

6. **Lens activation engine selection:**
   - explicit `ruleEngine: simple` -> simple only;
   - explicit `ruleEngine: full` -> full only;
   - absent `ruleEngine` -> try simple first, then full on grammar failure;
   - if both fail, activation is rejected with `InvalidRule` and both parser errors are returned.

7. **Resolved engine must be observable.** Log the selected engine and record it in Lens lifecycle health emission. If the existing health schema needs a field addition, keep it additive and test it.

8. **Capability Lens query is the acceptance oracle.** The exact query lives in `internal/bootstrap/lenses.go` (`CapabilityLensDefinition`). Do not hand-copy a simplified version and declare victory.

9. **One Lens = one RETURN = one target.** Do not add multi-output engine behavior. The secondary role index remains a second Lens.

10. **No legacy key fallback.** Story 2.3 closed Deviation 13. Full-engine tests must use `vtx.*` and `lnk.*` Contract key shapes.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/planning-artifacts/epics.md` Story 3.1 only | Authoritative ACs |
| `_bmad-output/planning-artifacts/refractor-gap-analysis.md` §2.1, §2.2, §2.5, Appendix A row 3.1 | Why this story exists and what changed after 2.3 |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 §6.10 and §6.2 | Required Capability Lens semantics and output shape |
| `internal/bootstrap/lenses.go` | Bootstrap Capability Lens and role-index Lens query bodies |
| `internal/refractor/engine/*` | Current simple parser/compiler/evaluator to isolate and preserve |
| `internal/refractor/lens/schema.go` | Lens YAML schema; add `ruleEngine` and validation semantics |
| `cmd/refractor/main.go` | Live activation path: parse/compile/build/start pipeline |
| `internal/refractor/control/service.go` | Validate path must use the same engine selection |
| `internal/refractor/pipeline/pipeline.go` | Execution boundary and event context shape |
| `internal/refractor/adjacency/*` | Edge lookup API; do not bypass it |
| `internal/refractor/health/*` | Where selected-engine health signal belongs |
| `internal/refractor/ruleengine/full/cypher/*` | Vendored generated openCypher lexer/parser/listener source from `jtejido/go-opencypher` |
| `go.mod` / `go.sum` | ANTLR runtime dependency for the vendored generated files |

Do not read the whole planning corpus. Do not re-open Materializer. The brief plus the listed files are enough.

## Required Cypher Feature Support

The `full` engine is complete only when these features execute correctly against the bootstrap Capability Lens query:

- Map literal expressions in `RETURN`, e.g. `{operationType: perm.operationType, scope: perm.scope}`
- `collect()` aggregation with `DISTINCT`
- `WITH` clauses for intermediate aggregation
- `OPTIONAL MATCH`
- Variable-length path patterns using `*`, including `*0..` and the Phase 1 two-hop `reportsTo` manager-delegation limit
- List concatenation, e.g. `collect(...) + collect(...)`
- Inbound traversal syntax, e.g. `<-[:reportsTo]-`
- `WHERE` filters needed by the bootstrap query, including negated existence/anti-pattern behavior for service exclusions
- Parameter references used by bootstrap queries, including `$actorKey`, `$now`, and `$projectedAt`

If one of these cannot be supported cleanly in this story, halt and escalate. Do not land a "full" engine that parses the query but silently drops semantic clauses.

## Suggested Sequence

**Phase A — Vendored Parser + Boundary Spike (<= 15K tokens):**
1. Compile-check the copied `internal/refractor/ruleengine/full/cypher` package and preserve the ANTLR runtime pin (`github.com/antlr4-go/antlr/v4 v4.13.1`).
2. Inspect generated package names and parse entrypoints (`package cypher`, generated from `Cypher.g4` by ANTLR 4.13.1).
3. Send a checkpoint: vendored parser compile status, runtime dependency version, any licensing/provenance concern.
4. Sketch the shared `ruleengine` interfaces before moving files.

**Phase B — Simple Engine Isolation (<= 25K tokens):**
5. Move or wrap current `internal/refractor/engine` behavior into `internal/refractor/ruleengine/simple`.
6. Preserve current parser/compiler/evaluator tests under the new package.
7. Add a compatibility shim only if needed to keep the refactor reviewable; remove it before close unless Winston approves keeping it.
8. Run focused simple-engine tests.

**Phase C — Lens Engine Selection (<= 20K tokens):**
9. Add `ruleEngine` to the Lens schema with explicit/auto selection behavior.
10. Update activation, control validation, fixture runner, and tests to use the shared engine registry.
11. Add lifecycle health/log signal for resolved engine.
12. Test explicit simple, explicit full parse failure, absent-field fallback, and both-fail error reporting.

**Phase D — Full Parse Visitor (<= 30K tokens):**
13. Build `full/visitor.go` to translate ANTLR parse trees into Refractor-native query structures.
14. Keep ANTLR types private to `full`.
15. Add parser tests for each required feature and the exact bootstrap Capability Lens query.

**Phase E — Full Executor (<= 35K tokens):**
16. Execute full-engine compiled rules against Core KV + Adjacency KV.
17. Implement aggregation, `WITH`, optional matching, variable-length traversal, inbound traversal, map literals, list concatenation, and required parameter resolution.
18. Preserve the soft-delete rule: readers filter `isDeleted` independently.
19. Add representative seeded-graph tests for Capability Lens semantics from Contract #6 §6.10.

**Phase F — Integration + Gates (<= 10K tokens):**
20. Add integration tests for simple-only, full-only, auto-fallback, both-fail activation rejection, and bootstrap Capability Lens query.
21. Record mean and p95 execution latency for the bootstrap query test. If p95 threatens the NFR-P3 budget, update the gap analysis with bottleneck + mitigation.
22. Run required gates.

## Deliverables Checklist

1. Vendored parser compile spike completed; copied parser files remain isolated under `internal/refractor/ruleengine/full/cypher/`.
2. `go.mod` / `go.sum` include `github.com/antlr4-go/antlr/v4 v4.13.1`, which is required by the vendored generated files.
3. Current simple parser/compiler/evaluator isolated as `simple` with behavior preserved.
4. Shared `RuleEngine` interface and engine registry added.
5. Lens schema supports `ruleEngine` with explicit simple/full and absent-field fallback semantics.
6. Lens activation rejects invalid rules with `InvalidRule` and useful parser errors.
7. Resolved engine is logged and emitted in Lens lifecycle health.
8. `full` parser visitor supports all required Story 3.1 constructs.
9. `full` executor uses Adjacency KV for edges and Core KV for vertex/aspect reads.
10. Bootstrap Capability Lens query parses and executes against a representative seeded graph.
11. Capability projection output conforms to Contract #6 three-section shape for the tested graph.
12. Mean and p95 execution latency recorded in closing summary.
13. Tests cover simple-only, full-only, auto-fallback, both-fail, and bootstrap Capability Lens query.
14. No full-engine code leaks into simple package and no ANTLR types leak outside `full`.
15. Token tracker Row 3.1 added — honest estimate, round UP.

## Required Verification

Run before closing:

```bash
go test ./internal/refractor/ruleengine/... -count=1
go test ./internal/refractor/lens ./internal/refractor/control ./internal/refractor/fixture ./internal/refractor/pipeline -count=1
go test ./internal/refractor/... -count=1 -p 1
make verify-bootstrap
make test-bypass
go test ./... -p 1 -count=1
```

If dependency download or module verification requires network, ask Andrew for approval through the tool flow. The generated parser files are already vendored; do not regenerate them unless Winston explicitly chooses that path.

## What Story 3.1 Is NOT

- Not Capability Lens activation in `make up` as the production auth source — Story 3.2 owns live activation and Capability KV projection hardening.
- Not Processor step 3 auth — Story 3.3.
- Not denial-response shaping — Story 3.4.
- Not Capability Lens adversarial suite — Story 3.7.
- Not NATS account-level sole-writer enforcement for Capability KV — Phase 2+ deployment hardening.
- Not a generalized Cypher database. Implement the Story 3.1 / Contract #6 feature set; avoid speculative GQL completeness.

## Escalation

Halt and escalate via Andrew/Winston if:
- The vendored parser files cannot be compiled cleanly with the ANTLR runtime.
- The generated parser cannot expose enough parse-tree detail for the required visitor.
- Supporting the bootstrap query requires a new adapter contract or data-contract amendment.
- Full-engine execution cannot preserve the Adjacency KV / Core KV boundary.
- The bootstrap query p95 exceeds the NFR-P3 budget and there is no localized mitigation.
- Token estimate exceeds 140K.

## Closing

1. Verify all deliverables above.
2. Run all required gates.
3. Update token tracker Row 3.1.
4. Closing summary must include: ANTLR runtime version, package-boundary changes, supported feature matrix, bootstrap query latency mean/p95, tests/gates run, and any residual risks for Story 3.2.

Do NOT commit. Winston + Andrew review and commit.
