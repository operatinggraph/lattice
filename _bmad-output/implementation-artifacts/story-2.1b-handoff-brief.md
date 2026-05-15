---
title: Story 2.1b Implementation Handoff Brief (Story 2.1 Fix-List Pass)
story: 2.1b — Refractor Morph Correctness Pass
model_tier: Opus (locked)
token_budget: ~95K
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-14
predecessor: Story 2.1 (commit pending — code in working tree, NOT yet committed to main)
---

# Story 2.1b — Refractor Morph Correctness Pass: Handoff Brief

## Your Role

Story 2.1 landed ~22 of 25 deliverables — the heavy lift (copy + rename + package wiring + 12 deviations logged + adapter tombstones + control service stub + heartbeater) is done and in the working tree. **You close four specific gaps** that block declaring Story 2.1 complete. The code is uncommitted; you finish the work, then Winston + Andrew commit a single clean Story 2.1 commit covering both passes.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

**Pattern across Phase 1: sub-agents have self-reported tokens 30-50% under outer telemetry.** Story 2.1's predecessor sub-agent was the first to come in at 9% — possibly because the brief was very specific. This brief is similarly tight.

- **At every checkpoint (every 8-10 tool calls OR after any deliverable OR after any file read >25KB):** send a "checkpoint message" with deliverables completed, deliverables remaining, honest token estimate (lower bound, rounded UP).
- **Halt unconditionally if you estimate > 100K used** (5% over budget). Wait for explicit Winston greenlight.

Other rules:
- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **Architecture binding:** `_bmad-output/planning-artifacts/data-contracts.md` is source of truth.
- **DO NOT silently edit planning artifacts.** Use `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` (Story 2.1 already created this; append).
- **All KV/JetStream ops through `internal/substrate`.**
- **No git commits by you.** Winston + Andrew commit.
- **Token tracker:** update Row 2.1 at session close to reflect 2.1 + 2.1b combined token spend; round UP. Do NOT create a separate Row 2.1b — this is a continuation of 2.1.
- **MORPH-DEVIATIONS.md:** when you RESOLVE a deviation here, mark it RESOLVED with a "Resolution:" subsection appended to that deviation; do NOT delete the deviation entry (audit trail).

## Predecessor State (Story 2.1, uncommitted)

The working tree contains Story 2.1's output:
- `internal/refractor/` (12 packages, ~80 files copied + renamed from Materializer)
- `cmd/refractor/` binary + schema + CONTRACT-AMENDMENT-REQUEST.md
- `docs/refractor-failure-tiers.md`
- `_bmad-output/planning-artifacts/MORPH-DEVIATIONS.md` (12 deviations logged)
- `internal/bootstrap/primordial.go` modified (refractor-adjacency bucket added)
- `scripts/verify-bootstrap.go` updated
- `Makefile` rewired (refractor-stub removed, real refractor wired)
- `cmd/refractor-stub/` deleted
- `internal/testdata/` fixtures added
- `go.mod` / `go.sum` updated (pgx, yaml.v3, testify, mock)

**Build + vet are GREEN.** All 12 refractor package tests pass. Bypass + bootstrap regression suites pass.

## The Four Gaps You Close

### Gap 1 (HIGHEST PRIORITY): Lens key shape is wrong

**Predecessor's choice:** `vtx.lens.<NanoID>` (3-segment, inventing a new top-level type `lens`).
**Contract-correct shape:** `vtx.meta.<NanoID>` (3-segment, type `meta`) with the document's `class` field set to `"meta.lens"`.

**Authority (data-contracts.md):**
- §1.2 line 64: "`meta` — schema and configuration meta-entities (DDL, Lens definitions, event schemas, system configuration). Distinguished by `class` field."
- §1.2 line 70: "`lens`, `event`, `ddl`, `actor` — these are *flavors of `meta`*, distinguished by the document's `class` field (`meta.lens`, `meta.event.<name>`, `meta.ddl.vertexType`, etc.)"

**Why this matters:** the predecessor brief's Decision #5 said `vtx.meta.lens.<NanoID>` — that's a 4-segment key (= aspect shape per §1.5), which is also wrong. The predecessor sub-agent caught the 4-segment issue but landed on a 3-segment-but-wrong-type shape. Neither is contract-compliant.

**What to fix:**

1. **CDC filter and routing in `internal/refractor/lens/corekv_source.go`:**
   - Watch on `vtx.meta.>` (NOT `vtx.lens.>`)
   - Filter delivered events by inspecting the value document's `class` field; route only those with `class == "meta.lens"` to the Lens loader. Other meta classes (`meta.ddl.*`, `meta.event.*`) are skipped silently in 2.1b (they'll be routed by future stories).
   - Use the same value-first / key-shape-second pattern from brief Decision #4a.

2. **Lens key generation in the translator and the hardcoded bootstrap lens (`internal/refractor/lens/bootstrap.go`):**
   - Bootstrap lens key: `vtx.meta.<deterministic-NanoID-for-bootstrap-lens>` (you can use a fixed sentinel NanoID baked into the binary; recommend reading `internal/bootstrap/primordial.go` for the pattern Lattice already uses for primordial fixed-id constants).
   - Bootstrap lens document MUST include `"class": "meta.lens"` in its envelope.

3. **Hardcoded bootstrap lens activation gate:** unchanged from 2.1 (env var `REFRACTOR_BOOTSTRAP_LENS=1`).

4. **MORPH-DEVIATIONS.md Deviation 12:** append a RESOLVED subsection: "Resolution: corrected to `vtx.meta.<NanoID>` with `class: \"meta.lens\"` per data-contracts.md §1.2 line 70. CDC routing now filters by document class, not by key prefix."

5. **`cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` Request 3:** append a RESOLVED note pointing at §1.2 line 70 as the authoritative resolution — `vtx.meta.<NanoID>` with `class: "meta.lens"` is the correct shape. No amendment to data-contracts.md is needed; the predecessor brief Decision #5 wording was just imprecise (it said `vtx.meta.lens.<NanoID>` when it should have said `vtx.meta.<NanoID>` with class `meta.lens`).

6. **Any tests that hardcode `vtx.lens.<id>`:** update to `vtx.meta.<id>`.

### Gap 2: AC #10 e2e p99 integration test

**Predecessor's state:** test not authored; sub-agent recommended "Story 2.2 first task." Winston rejects that — AC #10 is load-bearing and 2.2 is gap analysis, not implementation.

**What to deliver:**

`internal/refractor/refractor_e2e_test.go` (or similar — preserve the predecessor's test-file conventions). The test:

1. Uses the existing `internal/refractor/fixture/` infrastructure (embedded NATS + Postgres handle) — if Postgres-via-Docker is required, the test may use `testing.Short()` to skip in `-short` mode but MUST run by default. CI runs the full suite, so it executes there.

2. Setup:
   - Start embedded NATS (or use the live `make up` Docker stack — pick whichever matches `internal/refractor/fixture/` conventions; if both options exist, prefer Docker stack for fidelity)
   - Provision the dev harness via the existing bootstrap binary OR programmatic equivalent
   - Start a Processor instance (in-process via `internal/processor` package or as a subprocess — pick whatever is conventional; in-process is simpler)
   - Start a Refractor instance (in-process via `internal/refractor` package, NOT via the binary)
   - Write a `vtx.meta.<NanoID>` document with `class: "meta.lens"` through the Processor (the standard write path — operation submitted on `ops.meta.<requestId>` lane), with this Lens definition: watch `vtx.contract.>`, project to Postgres table `contract_view` with columns matching one aspect of the contract entity.

3. Run:
   - Loop 100 times: submit one `CreateContract` operation through the Processor on `ops.default.<requestId>`. Each operation creates one `vtx.contract.<NanoID>` + one `vtx.contract.<NanoID>.canonicalName` aspect (minimal — adjust to whatever the trivial Lens needs).
   - For each operation, record:
     - `t0` = wall-clock at operation publish
     - `t1` = wall-clock at the moment the projected row appears in Postgres (poll `SELECT * FROM contract_view WHERE id = $1` with a 1-second deadline)
   - Compute latency `t1 - t0` per operation.

4. Assert:
   - All 100 rows appear (no drops)
   - p99 of `t1 - t0` < 500ms (per NFR-P3)
   - Print the p50, p95, p99, max latencies and the count of rows as a test summary line

5. **If p99 ≥ 500ms:** halt with a `t.Fatalf` and a clear message. Do NOT silently mark as passing. If the test consistently fails with p99 marginally above 500ms, escalate — the morph might have introduced a latency regression that requires investigation, not budget tweaking.

6. **If Postgres cannot be set up from the test harness:** halt and escalate. Do NOT skip the test silently. This is the AC, not a nice-to-have.

7. **Update `MORPH-DEVIATIONS.md`:** if you discover a deviation from the morph plan during the e2e test setup, log it. Otherwise, no new entries.

### Gap 3: Substrate refactor completion (Deviation 5)

**Predecessor's state:** substrate boundary exists at `cmd/refractor` + new files (`corekv_source.go`, `bootstrap.go`, `lattice_heartbeater.go`, `capability.go`). Inner packages still use raw nats.go.

**What to deliver:**

Walk `internal/refractor/` and migrate every raw `nats.go` and `nats.go/jetstream` use to `internal/substrate` equivalents:

- `nats.JetStream`, `nats.JetStreamContext` → `substrate.Conn.JetStream()`
- `js.KeyValue(bucket)` → `substrate.Conn.KV` helpers
- `js.Stream(name)`, `js.AddStream(...)`, `js.UpdateStream(...)` → currently bootstrap-only; refractor should not need these. If any refractor code creates streams, that's a bug — halt and escalate.
- `js.Publish(...)`, `js.PublishAsync(...)` → `substrate.Conn.Publish` helpers (or batch helpers if available)
- `js.Subscribe(...)`, `js.Consume(...)` → wherever a substrate helper exists, use it. If substrate doesn't expose what you need, ADD the helper to substrate rather than reaching around it (per brief operating rules). Document the new substrate addition in the closing summary.

**Scope guard:** if a refactor would require touching >20 files, halt and propose a deviation. The goal is "every NATS-touching call goes through substrate," not "rewrite every line that mentions NATS in comments."

**Update Deviation 5 in MORPH-DEVIATIONS.md:** append RESOLVED with a brief note on what was migrated and any net-new substrate helpers added.

### Gap 4: `go test ./...` requires `-p 1` (CI implication)

**Predecessor's state:** parallel test execution causes NATS port collisions (likely test fixtures starting embedded NATS on the same port without random-port allocation).

**What to deliver:**

Two acceptable approaches:

(a) **Random-port allocation in fixtures.** If `internal/refractor/fixture/` (or wherever embedded NATS is started in tests) hardcodes a port, change it to ephemeral (port 0 → kernel-assigned). This is the long-term-correct fix.

(b) **Document `-p 1` requirement.** If (a) is too invasive, update `Makefile`'s `test` target to use `-p 1`, update `.github/workflows/ci.yml` to use `-p 1`, and log a Deviation entry noting the constraint and recommending random-port fixes for Phase 2.

Prefer (a). If (a) requires >10 file touches, fall back to (b).

## Housekeeping

- **Stray binary at `./refractor`:** the predecessor left a compiled `refractor` binary in the repo root. DELETE it. Verify it's gitignored (add to `.gitignore` if needed — Lattice's pattern is `bin/` for binaries, so a stray top-level binary should be excluded).
- **Verify `make verify-bootstrap`** still passes (33+ assertions including the new `refractor-adjacency` bucket).
- **Verify `make test-bypass`** still passes (Epic 1 Gate 2 regression).

## Deliverables Checklist

1. ✅ Gap 1 fixed: lens key shape `vtx.meta.<NanoID>` + `class: "meta.lens"` everywhere; CDC routing by document class; Deviation 12 + CAR Request 3 marked RESOLVED
2. ✅ Gap 2 delivered: `internal/refractor/refractor_e2e_test.go` runs 100 mutations through Processor → Refractor → Postgres; p99 < 500ms asserted; test runs in normal `go test` (not behind a build tag); CI executes it
3. ✅ Gap 3 delivered: substrate refactor inside `internal/refractor/`; Deviation 5 marked RESOLVED
4. ✅ Gap 4 delivered: parallel test collision fixed via random ports OR `-p 1` documented in Makefile + CI
5. ✅ Stray `./refractor` binary removed; `.gitignore` updated
6. ✅ `make verify-bootstrap` green
7. ✅ `make test-bypass` green
8. ✅ `go build ./...`, `go vet ./...`, `go test ./... -count=1` exit 0 (with `-p 1` if chosen approach in Gap 4)
9. ✅ Token tracker Row 2.1 updated: combined 2.1 + 2.1b actual; round UP
10. ✅ MORPH-DEVIATIONS.md: deviations 5 + 12 marked RESOLVED with resolution subsections
11. ✅ Closing summary: 10-item status, gap-by-gap report, e2e p99 number with full latency distribution (p50/p95/p99/max), token estimate (honest)

## What 2.1b Is NOT

- **Not** Deliverable #12 envelope reshape (still deferred — sub-agent in 2.1 correctly flagged that doing it requires adapter interface signature change, violating Decision #8 of the 2.1 brief). Confirm this gap is documented in MORPH-DEVIATIONS.md and gap-analyzed in Story 2.2.
- **Not** any net-new morph deltas (Phase 5 of the morph plan: Personal Lens, Path Projection, Crypto-shred, Secure Lens).
- **Not** parser work (Phases 3-4 of the morph plan).
- **Not** Story 2.2's gap analysis.

## Escalation

Halt and escalate via Andrew if:
- AC #10 e2e p99 fails (any of: rows missing, p99 ≥ 500ms, can't set up Postgres)
- Substrate refactor would require >20 file touches
- Random-port fix in fixtures requires >10 file touches AND `-p 1` documentation has hidden problems
- Token estimate exceeds 100K
- The `vtx.meta.<NanoID>` migration uncovers any further contract ambiguity
- Any CONTRACT-AMENDMENT-REQUEST-worthy issue

## Closing

1. Verify all 11 deliverables
2. Full reset: `make down && make up && make verify-bootstrap && make test-bypass` all green
3. `go test ./... -count=1` (with -p 1 if applicable) exits 0; refractor_e2e_test.go passes with p99 < 500ms
4. Token tracker Row 2.1 reflects combined 2.1 + 2.1b spend; round UP
5. Closing summary: deliverables status, e2e p99 number with distribution, deviations resolved, token estimate (honest), any open questions
6. CI verification: cannot be done until Andrew commits — flag as the final gate

Do NOT commit. Winston + Andrew review both passes together and produce a single Story 2.1 commit.
