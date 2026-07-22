---
title: Story 1.2 Implementation Handoff Brief
story: 1.2 — Starlark Execution Spike
model_tier: Sonnet (locked — do NOT use Opus or Haiku)
token_budget: ~65K (input + output combined)
session: Fresh implementation session — first action is reading this brief
architecture_lead: Winston (any architectural question routes to Winston via Andrew)
date: 2026-05-13
---

# Story 1.2 — Starlark Execution Spike: Implementation Handoff Brief

## Your Role

You are the implementing engineer for Story 1.2. Winston (the architect) and Andrew (the product owner) have completed the planning workflow and locked the story's scope and AC. Your job is to deliver the spike code and the written report defined by the AC. **You do not have authority to change story scope, AC, or architectural contracts during implementation.** If you discover something that contradicts the contracts or the story AC, stop and surface it — do not improvise.

## Operating Rules (Lattice Project)

- **Model tier is locked:** Sonnet only. If the session is on a different tier, halt and inform Andrew.
- **No PRs.** After implementation + Winston review, commit directly to `main`.
- **Architecture is binding:** `_bmad-output/planning-artifacts/lattice-architecture.md` + `_bmad-output/planning-artifacts/data-contracts.md` are the sources of truth. Story AC is the immediate target.
- **Token tracking:** at session close, record actual token usage in `_bmad-output/implementation-artifacts/token-usage-tracker.md` per the procedure documented there.
- **Dependencies:** `go get` and `go mod tidy` are now permitted in your session (Andrew has enabled Bypass permissions). Use them as needed; pin to the latest stable `go.starlark.net` release at spike time.

## Story Scope (Copy from `epics.md` — Authoritative)

> As a platform engineer,
> I want a validated spike report on Starlark execution in Go — covering sandbox correctness, ergonomic API design, and order-of-magnitude local performance — so that the Processor's script execution layer is designed on verified foundations before implementation begins.

**Recommended model tier:** Sonnet
**Estimated token budget:** ~65K

**Acceptance Criteria (from `epics.md`):**

Given a Go module using `go.starlark.net/starlark` (or the best available Go Starlark library at spike time), when the spike harness executes a series of targeted tests, then a written report is produced covering all three areas:

1. **Sandbox correctness**: A Starlark script that attempts each of the four forbidden operations — external HTTP call, filesystem read, `os.Getenv`, and a non-deterministic call (e.g., `time.Now` equivalent) — is rejected at parse or execution time with a clear error; no forbidden operation succeeds; sandbox configuration required to achieve this is documented.

2. **API ergonomics**: A prototype `ScriptContext` struct is designed and implemented showing how Lattice passes hydrated vertex data into a Starlark script (input shape), how the script emits a `MutationBatch` proposal (output shape), and how Processor validates the return value; the API is minimal — the spike is not production implementation, but the interface must be usable as-is by Story 1.6.

3. **Order-of-magnitude local performance**: The harness executes 1,000 sequential Starlark invocations against a realistic script (one vertex hydration, one conditional branch, one mutation proposal) on a development machine and records mean and p95 execution time; the purpose is to confirm the `< 100ms p99` target is achievable in principle — not to validate absolute production numbers (Mac performance is not representative of cloud environments).

**And** the report includes a **Go/No-Go recommendation** on the Starlark approach with rationale.
**And** if p95 local execution exceeds 100ms, the report includes a proposed architecture adjustment for commit path step 5 (e.g., pre-compilation, caching, pooling).
**And** all spike code is committed to `internal/spike/starlark/` with a README summarizing findings.
**And** if any finding contradicts the architecture contracts in `data-contracts.md`, the specific contract section and recommended amendment are called out explicitly via `internal/spike/starlark/CONTRACT-AMENDMENT-REQUEST.md`.

## Required Context — Read Before Implementing

The spike's three areas all map to specific architectural assumptions made during planning. To know whether a finding *contradicts* a contract, you need to know what the contracts assume.

**Read these sections (do NOT read whole files — token budget is tight):**

| File | Section | Why |
|---|---|---|
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #3 — MutationBatch and EventList (Starlark Return Contract) | Defines the EXACT shape the Starlark script must return; your `ScriptContext` output type must conform to this contract |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #2 — Operation Envelope (esp. §2.8 Auth Context) | The script receives the operation payload; understand the envelope structure |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #1 — Addressing Model & Document Envelope | Vertex/aspect/link keys the script will reference in its MutationBatch output |
| `_bmad-output/planning-artifacts/lattice-architecture.md` | Sections referencing Starlark execution, the 10-step commit path (esp. steps 4-5), NFR-P4 (< 100ms p99 budget), NFR-S3 (sandbox security) | The architectural envelope this spike validates |
| `_bmad-output/planning-artifacts/epics.md` | Story 1.2 (the canonical AC — confirm no drift between this brief and master) | Source of truth |

## Library / Environment

- **Go version:** 1.26.1 (confirmed via root `go.mod`)
- **Go module:** `github.com/operatinggraph/lattice` (root `go.mod` exists; this spike is `internal/spike/starlark/` within it)
- **Library:** `go.starlark.net` (the canonical Go Starlark interpreter from Google). Run `go get go.starlark.net@latest` (Bypass permissions enabled; this should succeed). Pin the exact version in the report.
- **No NATS in this spike** — Story 1.2 is purely about Starlark execution. NATS is irrelevant here (Story 1.1 already validated NATS atomic batch separately).

## Starlark Background (For Implementer Context)

If you haven't worked with Starlark before:

- **Origin:** Google's deterministic Python-syntax embedded scripting language, originally for Bazel build files. Now widely used as a sandboxed configuration / rule language.
- **Syntax:** Python-like (functions, conditionals, lists, dicts, strings, integers, booleans). No classes, no `import` of arbitrary modules, no I/O, no time access, no `os` module by default.
- **Sandbox model:** What the script can do is controlled by what bindings you put in its global environment. By default it can do arithmetic, string manipulation, conditionals, function calls, and list/dict construction — and nothing else.
- **Determinism guarantees:** No hidden non-determinism. Iteration over dicts is in insertion order. No random. No clock. No goroutines.
- **API in Go:** `starlark.Thread` runs a `starlark.Program` (compiled from source) against a `starlark.StringDict` (globals). Output is a `starlark.Value` (typed) which you convert back to Go types.

## What "Sandbox Correctness" Means for This Spike

The four forbidden operations from the AC are about Lattice's specific safety requirements (per NFR-S3 and FR9). In Starlark terms:

1. **External HTTP call** — Starlark has no native `http` module; if it tries `load("net/http", ...)` or similar, it should fail. Verify.
2. **Filesystem read** — Starlark has no native `open()`, no `os.read()`, no `io.file`. Verify by attempting `open("/etc/passwd")` style calls.
3. **`os.Getenv` equivalent** — Starlark has no native `os` module. The implementer might add one carelessly; verify that the default library set does NOT expose env vars.
4. **Non-deterministic call** — `time.Now()` equivalent. Starlark has no native time module. Some toolchains optionally add `time` — verify Lattice's spike does NOT enable it.

The test should attempt each forbidden operation explicitly in a Starlark script and confirm it fails at parse or execution time. Document the exact error message and the sandbox configuration (Go-side code) that produces the rejection.

## What "API Ergonomics" Means for This Spike

The `ScriptContext` struct is what Story 1.6 (Processor — Starlark Sandbox & JIT Hydration) will hand to the Starlark script. Per Contract #3, the script returns a `MutationBatch`. Your job is to design the input/output shapes such that Story 1.6's implementing engineer can use them with minimal further design work.

Suggested minimal shape (refine to fit Contract #3 exactly):

```go
type ScriptContext struct {
    // Inputs available to the script as starlark globals or attribute access
    Operation  OperationEnvelope       // per Contract #2
    Hydrated   map[string]VertexDoc    // JIT-hydrated vertices per contextHint
    DDLLookup  map[string]MetaVertex   // class -> meta-vertex for validation context
}

type ScriptResult struct {
    Mutations []MutationOp  // per Contract #3 §3.2
    Events    []EventSpec   // per Contract #3 §3.4 (EventList)
    // Possibly: rejection reason if the script chose to reject the operation rather than fail
}
```

Show one realistic example script (the conditional branch + mutation proposal scenario from AC #3) using your API, and verify Story 1.6's implementer would have everything they need to wire this into the Processor.

## What "Order-of-Magnitude Perf" Means for This Spike

The AC explicitly says **not to validate absolute production numbers** (Mac perf is not representative of cloud). The goal is:

- Confirm 1,000 sequential invocations complete in a small-enough total time that you have *order-of-magnitude confidence* the 100ms p99 target is achievable in production.
- Record mean and p95 for transparency.
- If p95 on your dev machine exceeds 100ms, propose mitigations (program pre-compilation, caching, thread pooling) in the report — but treat this as an architectural input, not a blocker. The final p99 validation is deferred to production environment per the architecture's locked decision.

Use Go's `time.Now()` (Go-side, NOT inside Starlark) for measurement. Histogram or sort-and-pick-95th-percentile is fine — `go.opencensus.io` etc. is overkill for a spike.

## Deliverables Checklist

At session close, the following MUST exist:

1. ✅ `internal/spike/starlark/main.go` (or organized into multiple files like `sandbox_correctness.go`, `api_ergonomics.go`, `perf.go`) — runnable harness covering all three test areas
2. ✅ `internal/spike/starlark/README.md` — the **written report**; structured as three sections (one per AC area), each with: test description, observed behavior, raw output / numbers, interpretation, contract implication (if any). Go/No-Go recommendation at the top.
3. ✅ Updated root `go.mod` and `go.sum` reflecting `go.starlark.net` dependency
4. ✅ `ScriptContext` prototype API (Go types + one realistic example script) visible in the spike code; usable as-is by Story 1.6
5. ✅ Mean and p95 execution latency recorded in the report
6. ✅ If any finding contradicts `data-contracts.md`: `internal/spike/starlark/CONTRACT-AMENDMENT-REQUEST.md` naming the specific contract section, the contradiction, and the recommended amendment text
7. ✅ Updated row in `_bmad-output/implementation-artifacts/token-usage-tracker.md` with actual token usage, model used, session date, and any notes
8. ✅ All code must compile clean (`go build ./...` exits 0) and `go vet ./...` must pass

## What This Spike Is NOT

- **Not** a Processor implementation — that comes in Stories 1.5-1.8
- **Not** a production-grade ScriptContext API — prototype is sufficient
- **Not** an absolute perf validation — Mac-relative order-of-magnitude only
- **Not** a NATS-touching test — NATS is out of scope here; the spike runs purely in-process

## Escalation Path

If during implementation you find:

- A Starlark API doesn't support what the AC assumes → **STOP, write a finding, escalate to Winston via Andrew.** Do not improvise an alternative.
- An architecture contract appears wrong (e.g., MutationBatch shape in Contract #3 can't be expressed in Starlark's value system) → **document the contradiction in `CONTRACT-AMENDMENT-REQUEST.md`** and continue with the spike as scoped.
- Token usage trending past 78K (20% over budget) → **flag it in your next message before continuing.** Andrew may decide to scope down or accept overrun.

## Closing the Session

Before ending the session:

1. Verify all 8 deliverables above
2. Run `go build ./...` and `go vet ./...` from repo root — must pass
3. Run the spike harness end-to-end and confirm it exits cleanly with all three test areas passing
4. Update the token tracker
5. Return a summary message listing all deliverables, the Go/No-Go recommendation headline, mean/p95 perf numbers, and any open questions
6. Do NOT commit. Winston + Andrew will review and commit.
