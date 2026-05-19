---
title: Story 2.4a Implementation Handoff Brief
story: 2.4a — Refractor Token Eviction (Mechanical)
model_tier: Sonnet (locked)
token_budget: ~90K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-19
predecessor: Stories 4.6 + 4.7 (post-realignment); Story 2.1 morph; Story 2.3 pipeline-key adaptation
---

# Story 2.4a — Refractor Token Eviction: Handoff Brief

## Your Role

Mechanical rename pass across `internal/refractor/` and `cmd/refractor/`: every `materializer` token in subject namespaces, durable consumer names, stream names, KV bucket defaults, and source comments gets replaced with the appropriate `refractor` / `lattice.refractor.*` shape. No behavior change. Pure rename + delete + comment-rewrite.

This is the audit-cleanup story for concern 5a (PHASE-1-COURSE-CORRECTION.md §A.5). Sister story 2.4b handles the design-bearing pieces (durable consumer for lens defs + NATS Services control plane).

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **No worktree.** Work in `/Users/andrewsolgan/Documents/GitHub/Lattice` against `main`.
- **No commits, no pushes.** Winston commits + pushes after review.
- **Planning artifacts are read-only.**
- **Token budget tracking-only.** Estimate ~90K.
- **Model tier:** Sonnet only.
- **Behavior preservation:** this story changes NO behavior. All tests that pass before must pass after with identical results (mod the renamed string values).
- **Andrew has authorized autonomous proceed.**

## What's in Place

- **`internal/refractor/`** 13 packages, all post-morph, all still carrying `materializer` tokens in: subject literals, durable names, stream names, KV bucket defaults, source comments.
- **`cmd/refractor/main.go`** — uses these tokens transitively via the subjects package.
- **The subjects package** at `internal/refractor/subjects/subjects.go` is the canonical place that constructs `materializer.*` subject strings. Single-file edit captures most subject literals.
- **The `team` field** (Deviation 4) is vestigial — present on the Lens struct but always empty. This story removes it.

Tree clean post-4.7 (commit pending; assumes 4.6 + 4.7 have shipped — Winston coordinates).

## Story Scope (2.4a)

### 1. Subject namespace rename (~15K tokens)

File-by-file in `internal/refractor/subjects/subjects.go`:

| Old subject | New subject | Notes |
|---|---|---|
| `materializer.rules.<team>.<lensId>` | DELETED | Lens defs live in Core KV (`vtx.meta.<NanoID>`), not on a dedicated subject; the rules-stream loader is going away in 2.4b but the subject namespace eviction happens here. |
| `materializer.health.<lensId>` | DELETED | Replaced by `health.refractor.<instance>.lens.<lensId>` (Story 3.2b) — already in use; this is removing the legacy subject namespace. |
| `materializer.audit.<lensId>` | `lattice.refractor.audit.<lensId>` | Audit subjects retained but renamed. |
| `materializer.metrics.<lensId>` | `lattice.refractor.metrics.<lensId>` | Lag/metrics subjects retained but renamed. |
| `materializer.dlq.<team>.<lensId>` | `lattice.refractor.dlq.<lensId>` | Team segment removed (Deviation 4). |
| `materializer.control` | UNCHANGED IN 2.4a | Story 2.4b migrates this to NATS Services. Leave the QueueSubscribe pattern in place; just don't rename the subject yet. |

After this rename, the `subjects` package's exported functions return the new strings. All callers in `internal/refractor/` are recompiled.

### 2. Stream name eviction (~5K tokens)

In `internal/refractor/lens/loader.go`:
- Delete the `rulesStreamName = "MATERIALIZER_RULES"` constant + `rulesSubjectFilter = "materializer.rules.>"` constant + `loaderDurableName = "materializer-rule-loader"` constant.
- Delete the entire `loader.go` file. Lens definitions live in Core KV and are consumed via `corekv_source.go` (Story 2.1). The legacy JetStream-rule-stream loader is dead code post-morph and was retained only as morph-provenance. Time to remove.
- Delete `loader_test.go`.
- Delete any callers in `cmd/refractor/main.go` (Story 2.1 should have already removed these; verify).

In `internal/refractor/pipeline/pipeline.go` (and `pipeline_test.go`):
- Replace `MATERIALIZER_DLQ_RULE-TERMINAL` → `REFRACTOR_DLQ_TERMINAL` (stream name change; functional behavior identical).
- Replace `materializer-rule-infra` durable consumer name → `refractor-lens-infra`.
- Replace `materializer-rule-resume-infra` → `refractor-lens-resume-infra`.

### 3. KV bucket eviction (~5K tokens)

In `internal/refractor/config/config.go`:
- The default `HealthKVBucket = "materializer-health"` is DELETED. The bucket was a legacy side-channel; Story 3.2b's Health KV per Contract #5 is canonical. Refractor doesn't need a separate health bucket.
- Delete the `HealthKVBucket` field from the config struct entirely.
- Delete `config_test.go`'s test for default value.
- Audit all callers — any code that wrote to `materializer-health` bucket gets deleted; emissions go through Health KV.

In `internal/refractor/control/service_test.go`:
- Test fixtures use buckets like `"materializer-health-ctrl"`, `"materializer-health-5-1"`, etc. — rename to neutral test-bucket names (e.g., `"refractor-test-ctrl"`). Pure cosmetic; tests still work.

### 4. Durable consumer name eviction (~5K tokens)

In `internal/refractor/consumer/bootstrap.go`:
- `adjConsumerName = "materializer-adjacency"` → `refractor-adjacency`.

(Other durable names already covered in §2 above.)

### 5. Source comment sweep (~30K tokens)

Bulk rewrite comments across `internal/refractor/`:
- Comments that reference "Materializer-derived" or "ported from Materializer" for code lineage: **keep** (factual provenance is fine).
- Comments referring to behavior or contracts as if Materializer is the current name: **rewrite** to use "Refractor". E.g., `// Materializer pipeline machinery` → `// Refractor pipeline machinery`.
- Comments referencing legacy subject names: update if the subject in question was renamed in §1, delete if the subject is gone.
- The `internal/refractor/lens/schema.go` line 83 doc-comment `// Rule is the parsed, validated representation of a Materializer rule.` → `// Rule is the parsed, validated representation of a Lens (formerly Materializer-domain "Rule").`

Run `grep -rni "materializer" internal/refractor/ cmd/refractor/` after the sweep. The remaining hits should be either:
- Morph-provenance comments (acceptable)
- Test fixture names that are deliberately legacy-named (e.g., simple-engine test fixtures)
- `internal/spike/` directory (frozen reference per `WINSTON-RESUME.md`; do not edit)

If a hit doesn't fall into one of those three categories, evict it.

### 6. `team` field cleanup (Deviation 4 carry) (~10K tokens)

In `internal/refractor/lens/schema.go`:
- Remove the `Team` field from the `Lens` struct (formerly `Rule`).
- Remove any YAML parsing for `team:`.

In `internal/refractor/subjects/subjects.go`:
- Subject builders that interpolated team get their team parameter removed:
  - `Audit(team, lensId)` → `Audit(lensId)`
  - `Dlq(team, lensId)` → `Dlq(lensId)`
  - (Rules subject is gone per §1)

Update all callers (cmd/refractor/main.go, pipeline.go, control/service.go, health/audit_writer.go, etc.) — drop the team argument.

### 7. Verification (~10K tokens)

Beyond the standard build/lint/test gates:

**The deployment-grep audit**: `grep -rni "materializer" internal/refractor/ cmd/refractor/` after all edits must return ONLY:
- Morph-provenance comments (matching regex `Materializer-derived|formerly.*Materializer|ported from Materializer`)
- `internal/spike/` directory hits (Story 1.1/1.2 frozen reference)
- Test fixture string literals that ARE Materializer-domain test data (intentional)

Document the final hit count and categories in the closing summary.

## Architectural Decisions Already Made (Winston)

1. **No behavior change.** Pure rename. If a test breaks beyond the renamed string assertions, halt and surface.

2. **`materializer-health` bucket is deleted, not renamed.** Health KV per Contract #5 is canonical; the side-channel bucket was always vestigial.

3. **The legacy JetStream-rules-stream `loader.go` is deleted entirely.** Post-Story-2.1 it's been dead code; this is the cleanup.

4. **Comments referencing morph provenance (e.g., "Materializer-derived") are KEPT.** They are factual. Comments referring to behavior as if Materializer is the current platform are REWRITTEN.

5. **`team` field removal** is in scope (Deviation 4 carry). Subjects drop the team segment too.

6. **`materializer.control` subject is NOT renamed in 2.4a.** Story 2.4b owns the control plane migration to NATS Services. Leave the QueueSubscribe pattern and subject name in place.

7. **`internal/spike/` is read-only and out of scope.** It's frozen reference per `WINSTON-RESUME.md`.

8. **No new substrate helpers** in 2.4a. Helpers come in 2.4b.

9. **Test fixtures using legacy names**: leave them. Tests that exercise the simple engine, legacy key shapes, or Materializer-domain test data should keep their legacy fixture strings to preserve test provenance. Rename only non-fixture code.

## Required Context — Read These Only

| File | Why |
|---|---|
| `PHASE-1-COURSE-CORRECTION.md` §A.5 + §C5 | Audit findings + Story 2.4 scope |
| `internal/refractor/subjects/subjects.go` | Primary edit target — subject construction |
| `internal/refractor/lens/loader.go` + `loader_test.go` | Delete |
| `internal/refractor/lens/schema.go` | Edit — drop Team field |
| `internal/refractor/pipeline/pipeline.go` + `pipeline_test.go` | Edit — durable + stream names |
| `internal/refractor/config/config.go` + `config_test.go` | Edit — drop HealthKVBucket |
| `internal/refractor/consumer/bootstrap.go` | Edit — durable name |
| `internal/refractor/control/service_test.go` | Edit — test bucket names |
| `internal/refractor/health/audit_writer.go` + `lag_poller.go` | Edit — subject literals in doc + code |
| `cmd/refractor/main.go` | Edit — caller adaptation after team removal |

**DO NOT read**: `lattice-architecture.md`, planning artifacts beyond the course-correction doc, brief from Story 2.4b.

## Suggested Sequence

**Phase A — Subjects + team (target ~20K tokens):**
1. Edit `subjects/subjects.go`: rename namespaces, drop team parameter.
2. Update all callers.
3. Edit `lens/schema.go`: drop Team field.

**Phase B — Streams + durables + buckets (target ~15K tokens):**
4. Delete `lens/loader.go` + `loader_test.go`.
5. Pipeline rename.
6. Consumer rename.
7. Config delete HealthKVBucket.

**Phase C — Comment sweep (target ~25K tokens):**
8. Grep + rewrite comments.

**Phase D — Verification (target ~15K tokens):**
9. Run all gates.
10. Final grep audit; categorize residual hits.

**Phase E — Closing (target ~15K tokens):**
11. Update token tracker Row 2.4a.
12. Closing summary.

## Required Verification

```bash
go build ./...
make vet
/Users/andrewsolgan/go/bin/golangci-lint run ./...
go test ./internal/refractor/... -count=1
make verify-kernel                            # ~33 OK (post-4.7)
make verify-package-rbac                      # ~30 OK
make verify-package-identity                  # ~25 OK
make verify-package-identity-hygiene          # ~20 OK
make test-bypass                              # 4/4 BLOCKED
make test-capability-adversarial              # 4/4 DEFENDED
go test ./... -p 1 -count=1                   # all green

# 2.4a-specific:
grep -rni "materializer" internal/refractor/ cmd/refractor/  # only allowed hits remain
```

## Deliverables Checklist

1. ✅ Subject namespaces renamed; team segment removed
2. ✅ Stream names + durable names renamed; legacy loader deleted
3. ✅ KV bucket `materializer-health` removed; HealthKVBucket field gone
4. ✅ Source comments swept; morph-provenance preserved
5. ✅ All gates green; deployment-grep audit clean
6. ✅ Token tracker Row 2.4a updated
7. ✅ Closing summary appended

## What 2.4a Is NOT

- Not behavior change
- Not control plane migration (2.4b)
- Not lens-source migration (2.4b)
- Not substrate helper additions
- Not test rewriting (only mechanical updates if tests assert on renamed strings)

## Escalation

CAR for:
- A `materializer` token can't be evicted without behavior change (escalate before forcing it)
- A test breaks beyond renamed-string assertions

Halt for:
- Bypass / Gate 3 vector flips
- Stuck-loop pattern

## Closing

1. Verify all 7 deliverables
2. Run all gates
3. Token tracker Row 2.4a
4. Closing summary

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.
