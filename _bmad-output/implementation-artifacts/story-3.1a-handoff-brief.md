---
title: Story 3.1a Implementation Handoff Brief
story: 3.1a — Engine Boundary + Simple-Engine Isolation + Lens Engine Selection
model_tier: Opus (locked)
token_budget: ~70K
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-15
predecessor: Story 3.1 spike (vendored ANTLR parser + handoff brief committed at 3cdd8a0; first 3.1 sub-agent halted at Phase A with reasoned escalation)
follow-up: 3.1b will add the full visitor + executor + bootstrap Capability Lens e2e
---

# Story 3.1a — Engine Boundary + Simple-Engine Isolation + Lens Engine Selection: Handoff Brief

## Your Role

You deliver Phases B + C of Story 3.1. Concretely:
- Isolate the existing Materializer-derived parser/compiler/evaluator under a new `internal/refractor/ruleengine/simple/` package, preserving every behavior and test.
- Define the shared `RuleEngine` interface and a small engine registry in `internal/refractor/ruleengine/`.
- Add the `ruleEngine` field to the Lens schema with fallback semantics (explicit `simple`, explicit `full`, absent = simple-then-full-fallback).
- Ship a **stub `full` engine** that compiles cleanly but ALWAYS returns `InvalidRule` with a clear "not yet implemented" message. Story 3.1b will replace the stub with the real visitor + executor.
- Lifecycle health/log signal records the resolved engine.

This is a pure refactor + selection-logic pass. NO visitor work, NO executor work, NO cypher parsing into AST. The vendored ANTLR parser at `internal/refractor/ruleengine/full/cypher/` stays untouched in 3.1a.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

**Pattern across Phase 1: sub-agents have self-reported tokens 30-50% under outer telemetry.** This is a boundary refactor — moderate risk for context creep across ~7 caller packages.

- **At every checkpoint (every 8-10 tool calls OR after any deliverable OR after any file read >25KB):** send a "checkpoint message" with deliverables done, deliverables remaining, honest token estimate (lower bound, rounded UP).
- **Halt unconditionally if you estimate > 75K used** (about 7% over budget).
- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **No git commits by you.** Winston + Andrew commit.
- **Architecture binding:** `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 and `_bmad-output/planning-artifacts/epics.md` Story 3.1 are source of truth.
- **DO NOT silently edit planning artifacts.** Use `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` (append).
- **Token tracker:** add Row 3.1a at session close — HONEST estimate, round UP.

## What Was Already Done (do NOT redo)

- Vendored ANTLR parser at `internal/refractor/ruleengine/full/cypher/` (Cypher.g4 + cypher_lexer.go + cypher_parser.go + cypher_listener.go + cypher_base_listener.go + README.md).
- `github.com/antlr4-go/antlr/v4 v4.13.1` in go.mod/go.sum.
- `.golangci.yml` excludes `internal/refractor/ruleengine/full/cypher` from lint.
- `Makefile`'s `vet` target excludes the cypher package.
- The first 3.1 sub-agent confirmed `go build` and `make vet` green; ANTLR runtime pin verified; parser surface (`OC_Cypher`, `OC_Match`, `OC_With`, `OC_Return`, etc.) sufficient for the visitor work scheduled for 3.1b.

You inherit a clean tree. Verify with `go build ./...` and `make vet` as your first action.

## Architectural Decisions Already Made (Winston)

1. **The current `internal/refractor/engine/` package becomes the `simple` engine** at `internal/refractor/ruleengine/simple/`. PRESERVE behavior verbatim. Update import paths in callers; do not modify logic.

2. **Shared types live in `internal/refractor/ruleengine/`** (a new neutral package — not a sub-package of `simple` or `full`). At minimum it exports:
   ```go
   // RuleEngine is the common interface both simple and full engines satisfy.
   type RuleEngine interface {
       Name() string
       Parse(ruleBody string) (CompiledRule, error)
       Execute(ctx context.Context, cr CompiledRule, ec EventContext) (ProjectionResult, error)
   }

   // Registry: simple parse/exec failures vs full parse/exec failures distinct, so
   // both-fail activation can return both errors per Decision #6 of the parent 3.1 brief.
   type Registry interface {
       Get(name string) (RuleEngine, bool)
       List() []string
       SelectForLens(lens LensDefinition) (resolved RuleEngine, attempted []string, errs []ParseError, ok bool)
   }
   ```
   You may add helper types (`CompiledRule`, `EventContext`, `ProjectionResult`, `ParseError`) in this package as well. Keep ANTLR types OUT of this package — that's Decision #3 of the parent brief.

3. **Engine selection per Lens** via a new YAML/JSON field `ruleEngine` with values `simple` or `full`. Resolution rules (parent brief Decision #6):
   - explicit `simple` → simple only; if simple parse fails, activation rejected with `InvalidRule` carrying the simple parse error.
   - explicit `full` → full only; in 3.1a the full engine always returns its "stub: not yet implemented" error, so explicit `full` activation always fails with `InvalidRule`. This is the EXPECTED 3.1a behavior — selection-logic tests for this path verify the stub fails cleanly. 3.1b will make this path succeed.
   - absent → try simple first, then full on grammar failure. In 3.1a, this means: simple succeeds → use simple; simple fails → full also fails (stub) → activation rejected with BOTH parser errors returned.

4. **Stub `full` engine implementation:** lives at `internal/refractor/ruleengine/full/full.go` (or similar). One file. Implements the `RuleEngine` interface. `Parse` returns `(nil, &ParseError{Engine: "full", Message: "full engine not yet implemented (Story 3.1b)"})`. `Execute` panics with "full engine not yet implemented — Parse() should have failed" (defensive; reachable only if a caller bypassed `Parse`). Add a `// Story 3.1b will replace this stub with the real visitor + executor.` comment at the top.

5. **Resolved engine in health + log:** when a Lens activates, log at INFO with fields `lensId`, `resolvedEngine`, `attemptedEngines`. Emit the resolved engine name to the Lens lifecycle health emission (additive — extend the existing schema with a `ruleEngine` field if needed; do not break consumers). If the existing health emission code can't be cleanly extended, halt and escalate — do NOT silently drop this requirement.

6. **No code/type leakage:** ANTLR types stay inside `internal/refractor/ruleengine/full/cypher/`. The stub `full` engine in `internal/refractor/ruleengine/full/full.go` does NOT import the cypher package in 3.1a — there's nothing for it to do with the parser yet. (3.1b will add the cypher import once the visitor exists.)

7. **Tests for engine selection (mandatory in 3.1a):**
   - simple-only Lens, simple parse succeeds → activation succeeds, resolved engine logged as `simple`
   - simple-only Lens, simple parse FAILS → activation rejected with InvalidRule, error includes simple parse error
   - full-only Lens (stub fails by design) → activation rejected with InvalidRule, error includes the stub "not yet implemented" message
   - absent ruleEngine, simple succeeds → activation succeeds, resolved engine logged as `simple`
   - absent ruleEngine, simple fails (intentionally malformed rule) → activation rejected with InvalidRule, error includes BOTH the simple parse error AND the full-stub error

8. **No CONTRACT-AMENDMENT-REQUEST expected.** If one emerges, append to `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` and escalate.

9. **CI gate:** `.github/workflows/ci.yml` is active. After your changes, CI must go green. Verification commands at the bottom of this brief.

10. **Compatibility shim policy:** the parent brief allows a compatibility shim if needed for review-ability but prefers removal before close. Winston decision for 3.1a: shim allowed during refactor; MUST be removed before closing. If removal becomes impractical, halt and escalate — don't leave dead code.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/story-3.1-handoff-brief.md` | Parent brief — the source of all 10 Architectural Decisions in 3.1. Read this for context but operate under THIS 3.1a brief's narrower scope. |
| `_bmad-output/planning-artifacts/epics.md` Story 3.1 | ACs that 3.1a partially satisfies (engine boundary + selection); 3.1b satisfies the rest. |
| `internal/refractor/engine/` (all files) | Code you're isolating into `simple/`. |
| `internal/refractor/lens/schema.go` | Where the `ruleEngine` field lands. |
| `cmd/refractor/main.go` | Caller of `engine.Parse` / `engine.Compile` — needs import path updates. |
| `internal/refractor/control/service.go` | Validation caller. |
| `internal/refractor/fixture/runner.go` | Test fixture caller. |
| `internal/refractor/pipeline/pipeline.go` + `pipeline_test.go` | Execution boundary; pipeline may consume the compiled-plan shape from simple — adapter layer probably belongs in `ruleengine/` shared types. |
| `internal/refractor/health/` (skim — find the lens lifecycle emit site) | Where the resolved-engine signal lands. |

**DO NOT read** the cypher vendored package, the bootstrap lenses file, Contract #6, or anything else 3.1b-related. This story doesn't need them.

## Suggested Sequence

**Phase A — Boundary spike confirmation (≤ 5K tokens):**
1. `go build ./...` and `make vet` — green.
2. Scan `internal/refractor/engine/` and list the exported symbols + their callers (use `grep -rn "engine\." internal/refractor cmd/refractor`).
3. Send a checkpoint with the symbol/caller list and your intended package split.

**Phase B — Simple engine isolation (≤ 30K tokens):**
4. Create `internal/refractor/ruleengine/` (shared types: `RuleEngine` interface, registry, `CompiledRule`, `EventContext`, `ProjectionResult`, `ParseError`).
5. Move `internal/refractor/engine/` → `internal/refractor/ruleengine/simple/`. Update package declarations and import paths in moved files. Wire the simple engine to implement `RuleEngine`.
6. Update ~7 callers (cmd/refractor, lens, control, fixture, pipeline + tests) to use the shared interface where appropriate; direct `simple.New()` only where the caller specifically needs the simple engine.
7. `go build ./...` green. Existing engine tests run under the new path.

**Phase C — Stub full engine + Lens selection (≤ 25K tokens):**
8. Create `internal/refractor/ruleengine/full/full.go` — stub engine per Decision #4.
9. Add `ruleEngine` field to Lens schema; update the translator/validator.
10. Wire selection logic per Decision #3 (explicit simple, explicit full, absent fallback).
11. Add resolved-engine log + health emission per Decision #5.
12. Add the 5 selection tests per Decision #7.

**Phase D — Gates + closing (≤ 10K tokens):**
13. Run `go test ./internal/refractor/ruleengine/... -count=1`.
14. Run `go test ./internal/refractor/... -p 1 -count=1`.
15. Run `make verify-bootstrap`, `make test-bypass`.
16. Run `go test ./... -p 1 -count=1`.
17. Remove any compatibility shim per Decision #10.
18. Update token tracker Row 3.1a — round UP.
19. Closing summary.

## Deliverables Checklist

1. ✅ Phase-A spike checkpoint sent (symbol/caller inventory + package-split plan)
2. ✅ `internal/refractor/ruleengine/` shared package with `RuleEngine` interface, registry, and supporting types
3. ✅ `internal/refractor/engine/` moved/renamed to `internal/refractor/ruleengine/simple/`; tests preserved; behavior unchanged
4. ✅ All ~7 callers updated to use the new package boundary
5. ✅ Stub `full` engine at `internal/refractor/ruleengine/full/full.go` (always returns InvalidRule with "not yet implemented" message)
6. ✅ Lens schema supports `ruleEngine` field
7. ✅ Selection logic implements explicit-simple / explicit-full / absent-fallback per Decision #3
8. ✅ Resolved engine logged + emitted in lens lifecycle health per Decision #5
9. ✅ 5 selection tests pass (simple-only success, simple-only fail, full-only fail-by-design, absent-fallback success, absent-both-fail)
10. ✅ Compatibility shim (if used) removed before close
11. ✅ ANTLR types remain isolated to `internal/refractor/ruleengine/full/cypher/`
12. ✅ `go build ./...`, `make vet`, `go test ./... -p 1 -count=1`, `make verify-bootstrap`, `make test-bypass` all green
13. ✅ Token tracker Row 3.1a added — HONEST estimate, round UP
14. ✅ Closing summary

## What Story 3.1a Is NOT

- **Not** the full visitor (Story 3.1b)
- **Not** the full executor (Story 3.1b)
- **Not** the bootstrap Capability Lens e2e test (Story 3.1b)
- **Not** latency measurement (Story 3.1b)
- **Not** any change to data-contracts.md or planning artifacts beyond the token tracker

## Escalation

Halt and escalate via Andrew/Winston if:
- The existing engine package has a hidden coupling that makes isolation non-trivial
- The Lens schema can't accept an additive field without breaking existing serialization
- The lens-lifecycle health emission code can't be cleanly extended (Decision #5)
- Token estimate exceeds 75K
- A CONTRACT-AMENDMENT-REQUEST emerges

## Closing

1. Verify all 14 deliverables
2. Full reset: `make down && make up && make verify-bootstrap && make test-bypass && go test ./... -p 1 -count=1` all green
3. Update token tracker Row 3.1a — round UP
4. Closing summary: deliverables status, caller-update count, selection-test results matrix, any residual risks for Story 3.1b, honest token estimate (rounded UP)

Do NOT commit. Winston + Andrew review and commit.
