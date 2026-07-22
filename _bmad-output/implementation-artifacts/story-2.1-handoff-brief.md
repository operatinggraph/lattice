---
title: Story 2.1 Implementation Handoff Brief
story: 2.1 — Materializer → Refractor Morph (Lift-and-Shift)
model_tier: Opus (locked)
token_budget: ~145K
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-14
---

# Story 2.1 — Materializer → Refractor Morph: Implementation Handoff Brief

## Your Role

You execute the **lift-and-shift of the Materializer codebase into Lattice as Refractor**. This is the largest single story in Phase 1: a working projection engine arrives in the Lattice repo, adjusted only as far as Lattice's data contracts and key patterns mandate. Preservation is the dominant posture — you adjust the seams, not the heart. Story 2.2 will then perform a written gap analysis against Lattice's Phase 1 and Phase 2 needs.

**Source repo (read-only reference):** `/Users/andrewsolgan/Documents/GitHub/Materializer`
**Destination repo:** `/Users/andrewsolgan/Documents/GitHub/Lattice` (current).
**Strategy:** copy + adapt source files into the Lattice tree under `internal/refractor/` and `cmd/refractor/`. Do NOT add Materializer as a Go module dependency — this is a code-import morph, not a library link.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

**Pattern across Stories 1.1, 1.5, 1.6, 1.7, 1.8, 1.10: sub-agents have self-reported tokens 30-50% under outer telemetry.** Story 2.1 is the largest single story in Phase 1 and the highest risk for overrun.

- **At every checkpoint (every 8-10 tool calls OR after any deliverable OR after any file read >25KB):** send a "checkpoint message" with deliverables completed, deliverables remaining, honest token estimate.
- **Treat self-estimate as LOWER BOUND, round UP.** If you "feel like" 100K, report 140K.
- **Halt unconditionally if you estimate > 150K used** (5% over budget). Wait for explicit Winston greenlight via Andrew.
- **🔴 NEW RULE for this story:** because this is a large morph with many possible directions, **after the FIRST exploration phase (reading the morph plan + scanning Materializer's source tree, no more than 25 tool calls), send a planning checkpoint** describing your intended morph sequence in 8-12 bullets. Wait for confirmation in Winston/Andrew's next message OR proceed if you receive no halt signal — but the checkpoint MUST be in the conversation as a recoverable plan in case the session aborts midway.

Other rules:
- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **Architecture binding:** `_bmad-output/planning-artifacts/data-contracts.md` is source of truth. The morph plan (`materializer-morph-plan.md`) is research guidance, NOT contract.
- **DO NOT silently edit planning artifacts.** Use `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` (create the file as part of Step 1 if you need to raise anything).
- **All KV/JetStream ops through `internal/substrate`.** Materializer's NATS access patterns must be refactored to use substrate's connection/batch/key helpers — this is part of the "adjust the seams" work.
- **CI gate (NEW):** the CI workflow is now active (`.github/workflows/ci.yml`). After your changes, CI must go green before the story is considered done. If CI requires new docker-compose services (e.g., Postgres), update the workflow accordingly.
- **No git commits by you.** Winston + Andrew commit.
- **Token tracker:** update Row 2.1 at session close — HONEST estimate, round UP.
- **🔴 MORPH-DEVIATIONS.md is non-optional:** the AC mandates this file. Every deviation from the morph plan must be recorded as you go — this is the primary input to Story 2.2. Do NOT defer logging until the end.

## Story Scope (from `epics.md` lines 605-649 — authoritative)

> As a platform engineer, I want the Materializer codebase lifted into Lattice as Refractor — adjusted only as needed to conform to Lattice's data contracts and key patterns, preserving all existing Materializer capabilities — so that Lattice has a known-working projection engine running against Core KV before any new capabilities are layered on in Epic 3.

**Recommended model tier:** Opus
**Estimated token budget:** ~145K

### Acceptance Criteria (verbatim distilled — read epics.md lines 613-649 for full text)

1. **Rename + relayout:** all `materializer/*` packages renamed `refractor/*`; binary is `lattice-refractor`; layout follows the morph plan's recommended structure except where `data-contracts.md` mandates otherwise; existing Materializer unit and integration tests pass with only import-path updates.

2. **CDC consumption:** Refractor sources CDC events via named durable JetStream consumers on the `KV_<core-bucket>` backing stream (one durable per active Lens); consumer positions persist across restarts (NFR-R2). CDC events are classified before routing to Lens handlers. **Classification is by value-document inspection, not by key prefix substring matching** — see Decision #4a below for the correct procedure. The key shape is a coarse routing hint only (`vtx.` 3-segment = vertex, `vtx.` 4-segment = aspect, `lnk.` 6-segment = link per Contract #1 §1.5); fine-grained class routing comes from the document's `class` field per §1.5 line 44.

   ⚠️ **Planning-artifact discrepancy:** epics.md AC #2 reads `vtx.*` → vertex, `asp.*` → aspect, `lnk.*` → link. The `asp.*` prefix does NOT exist — aspects are keyed `vtx.<type>.<id>.<localName>` per Contract #1 §1.5 (4-segment `vtx.` prefix). Treat data-contracts.md as binding; epics.md text is stale. Log this in MORPH-DEVIATIONS.md AND append a CONTRACT-AMENDMENT-REQUEST entry recommending the epics.md AC text be corrected (planning-artifact-only fix; no code impact).

3. **Lens definitions from Core KV:** Lens definitions sourced from `vtx.meta.lens.*` (Contract #1 meta-vertex pattern), NOT from a dedicated Materializer-style rules stream. Lens activation/deactivation/modification flows through the standard Processor write path (FR15, FR16); CDC-driven propagation completes within NFR-E1/E2 lag without Refractor restart.

4. **Adapters preserved:** existing target adapter interface, NATS-KV adapter, Postgres adapter PRESERVED and functional; vertex/aspect/link mutations routed per Lens's `target`; tombstones use soft-delete (`isDeleted: true` per Contract #1), NOT physical row delete; envelope fields reshaped to Contract #1 where they appear in projection output.

5. **Control service preserved:** existing control service preserved; endpoints (Lens lifecycle, status, manual replay triggers) continue to function; auth wired to read Capability KV (STUBBED — full integration is Epic 3); control service health → `health.refractor.<instance>.control` (NFR-O1, NFR-O2).

6. **Health and lag:** all health emissions to `health.refractor.<instance>.*` and `health.refractor.<instance>.lens.<lensId>.*`; per-Lens lag emitted ≥ every 10s (NFR-O1, NFR-O3). Failure tier classification audited: crypto-shred → privacy-critical (alert, no silent retry); Capability Lens failures → security-critical (alert, halt affected Lens). Document failure tiers in `docs/refractor-failure-tiers.md`.

7. **Adjacency KV preserved (internal):** Adjacency KV continues as Refractor's internal operational store; key shapes adjusted to Lattice contracts where overlap exists; remains private to Refractor (not externally addressable, not a Lens target).

8. **MORPH-DEVIATIONS.md:** `_bmad-output/planning-artifacts/MORPH-DEVIATIONS.md` — record every deviation from the morph plan with: morph plan section, actual decision, contract section (or other constraint) that drove it, downstream implication. **This artifact is the primary input to Story 2.2 — do NOT skimp.**

9. **Dev harness:** `make up` brings up Refractor as a service alongside NATS + (new) Postgres. Refractor emits `health.refractor.<instance>` within 10s of startup (NFR-O1).

10. **E2E integration test:** activate a trivial Lens (one entity type → one aspect projection → Postgres target) via the standard write path; write a vertex through Processor; projection appears in Postgres within NFR-P3 (< 500ms p99 over a 100-mutation run).

## Required Context — Read These Only

### Lattice side (your destination)

| File | Why |
|---|---|
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.5 (key patterns) + §1.6 (envelope) + §1.7 (DDL / meta-vertex `vtx.meta.lens.*`) | Authoritative shapes you adapt Materializer's expectations to |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #5 (Health KV) | Where Refractor emits health + lag |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 (Capability KV — only the structure, you don't implement Capability KV writes in 2.1) | Context for the auth-stub work (Decision #11 below) |
| `_bmad-output/planning-artifacts/materializer-morph-plan.md` | THE research input. Read whole file — it's 427 lines. Treat §2, §3, §6, §7 as decisive. Treat §4 (preserved-as-is) as your "do not touch" list. |
| `internal/substrate/` (entire package) | Connection, batch, key helpers — all Materializer NATS access refactors through here |
| `internal/bootstrap/primordial.go` | Stream + bucket provisioning — you add Refractor's needs here (consumer registration, new buckets if any). Existing `core-events` stream is already provisioned. |
| `internal/processor/health.go` | Pattern for Health KV writes — Materializer's health code must be refactored to this idiom |
| `Makefile` + `docker-compose.yml` | Where you add Postgres + the new refractor service |
| `cmd/refractor-stub/main.go` | The 2.1 work REPLACES this. Delete the binary as part of the morph (after `make up` is rewired to start the real Refractor). |
| `.github/workflows/ci.yml` | Update if you add Postgres to docker-compose (CI must bring it up too) |

### Materializer side (your source — read-only)

| File | Why |
|---|---|
| `/Users/andrewsolgan/Documents/GitHub/Materializer/cmd/materializer/main.go` | Binary entry point — top-of-stack to morph |
| `/Users/andrewsolgan/Documents/GitHub/Materializer/internal/` (all packages — adapter, adjacency, config, consumer, control, engine, failure, fixture, health, pipeline, rule, subjects) | The codebase you're morphing |
| `/Users/andrewsolgan/Documents/GitHub/Materializer/CLAUDE.md` | Materializer's own conventions (relevant for understanding internals) |
| `/Users/andrewsolgan/Documents/GitHub/Materializer/Makefile` + `Dockerfile` + `config.yaml` | Operational shape you adapt |

**DO NOT load Materializer's `_bmad`/`_bmad-output` planning artifacts** — they're Materializer-internal and not relevant here.

## Architectural Decisions Already Made (Winston)

These are binding. If any of them is unworkable, halt and escalate — do NOT improvise around them silently.

1. **Destination layout** (mirroring `internal/processor/`):
   ```
   cmd/refractor/main.go                Binary entry point. Binary name "refractor" (NOT "lattice-refractor" — Lattice's convention is bare names: bootstrap, processor, refractor-stub. The AC's "lattice-refractor" is a doc convention, not a binary filename. Note this as a non-deviation in MORPH-DEVIATIONS.md with reasoning.)
   internal/refractor/                  All morphed materializer/internal/* packages renamed and refactored
     adapter/                           ← from materializer/internal/adapter/
     adjacency/                         ← from materializer/internal/adjacency/
     control/                           ← from materializer/internal/control/
     engine/                            ← from materializer/internal/engine/
     failure/                           ← from materializer/internal/failure/
     fixture/                           ← from materializer/internal/fixture/
     health/                            ← from materializer/internal/health/
     lens/                              ← from materializer/internal/rule/ (renamed; `Rule` → `Lens`, `RuleID` → `LensID`)
     pipeline/                          ← from materializer/internal/pipeline/
     subjects/                          ← from materializer/internal/subjects/ (rewritten per the morph plan §6 rename table)
   docs/refractor-failure-tiers.md      Failure tier documentation (AC #6)
   ```
   The morph plan's recommended layout in §6 is the reference; deviations get logged.

2. **Module + import path:** all morphed files live under `github.com/operatinggraph/lattice/internal/refractor/...`. No `github.com/asolgan/materializer` references survive.

3. **Adjacency KV bucket:** provision a NEW dedicated KV bucket `refractor-adjacency` in `internal/bootstrap/primordial.go::provisionBuckets`. Do NOT reuse `core-kv` — the adjacency store is Refractor-internal per AC #7, and mixing it with Core KV pollutes the externally-visible namespace. Add the bucket to `verify-bootstrap` assertions.

4. **CDC consumption: durable consumers on `KV_core-kv`.** Per AC #2, Refractor consumes the JetStream backing stream of the `core-kv` KV bucket. NATS JetStream auto-creates this stream as `KV_core-kv` when the bucket is created. Refractor creates one durable consumer PER ACTIVE LENS with `FilterSubjects` matching the Lens's interest (e.g., `$KV.core-kv.vtx.contract.>`). Consumer names: `refractor-lens-<lensId>`. Durability ensures NFR-R2 (sequence persistence across restarts). When a Lens is deactivated, its durable consumer is deleted; when a Lens is modified, the consumer may need replay from start (handled by the existing Materializer hot-reload pattern, adapted).

4a. **CDC event classification — value-document first, key shape second.** When a CDC event arrives:
    1. Parse the value document. The document IS the source of truth (per data-contracts.md repeatedly, and per Andrew's standing architectural principle).
    2. Determine entity kind (vertex / aspect / link) from the key SHAPE — segment count and leading token, as specified by Contract #1 §1.5:
       - `vtx.<type>.<id>` (3 segments) → vertex
       - `vtx.<type>.<id>.<localName>` (4 segments) → aspect
       - `lnk.<type1>.<id1>.<localName>.<type2>.<id2>` (6 segments) → link
       Use `substrate.ClassifyKey` (already exists, used by Processor step 6 in Story 1.7) — DO NOT roll your own prefix-string matching.
    3. Read the document's `class` field (per §1.5 line 44 and §1.6 envelope) for fine-grained routing. The key's `<type>` segment is a coarse routing/filtering category; `class` is the authoritative fine classification.
    4. Route to Lens handlers based on (kind, class) — NOT key prefix substring matching.

    Lens definitions describe their interest by `class` patterns and entity kind, not by raw key globs. Materializer's rule schema may currently use key-glob filters internally; the translator (Decision #6) maps Lens-aspect interest declarations to the appropriate `FilterSubjects` + class-filter pair.

5. **Lens definition source — Core KV watch on `vtx.meta.lens.>`.** Materializer's Loader (rule loader from `MATERIALIZER_RULES` stream) is refactored to a Core KV watch (`Conn.KVWatch(ctx, "core-kv", "vtx.meta.lens.>")`). Each watch event becomes a "lens loaded/updated/deleted" callback. The Loader's downstream interface (`SetUpdateCallback`, `HotReloadInto`, `HotReloadPlan`, `ClassifyUpdate`) is PRESERVED — only the source changes. This is morph plan §2.3 Approach 1 (Adapter approach), explicitly preferred.

6. **Lens schema translator:** Materializer rules are YAML; Lattice Lens definitions are JSON aspect bodies on a `vtx.meta.lens.<NanoID>` vertex with multiple aspects:
   - `canonicalName`: e.g., `"lens.contract-view"`
   - `targetType`: `"postgres"` | `"nats-kv"` (matches existing adapter types)
   - `targetConfig`: JSON object (table/bucket/dsn etc. — adapter-specific)
   - `cypherRule`: the openCypher MATCH/RETURN string (Materializer's existing parser accepts this)
   - `outputSchema`: JSON schema for projection rows (passes through to adapter)

   The translator `internal/refractor/lens/translator.go` reads these aspects and produces a `*Lens` (formerly `*Rule`). DDL validation of the meta-vertex itself happens at Processor step 6 (Story 1.7 already enforces `permittedCommands`) — Refractor trusts what CDC delivers.

7. **Hardcoded bootstrap lens for Phase 1 of MVP:** as a SAFETY NET (and per morph plan Day 3 of Phase 1), embed a single bootstrap Lens hardcoded in Go (`internal/refractor/lens/bootstrap.go`). It activates iff `vtx.meta.lens.>` is empty at startup AND an env var `REFRACTOR_BOOTSTRAP_LENS=1` is set. This is for the e2e test in AC #10 to be runnable before any meta.lens vertex exists in the bootstrap-provisioned Core KV. The bootstrap lens definition is removed (or auto-disabled) once a real Lens lands in Core KV.

   The AC's E2E test (#10) MUST use the standard write path to activate a Lens (write a `vtx.meta.lens.*` via Processor); the hardcoded bootstrap is a development convenience only. Specifically, the e2e test:
   (a) writes a `vtx.meta.lens.*` via Processor → CDC delivers → Refractor activates lens
   (b) writes a `vtx.contract.*` via Processor → CDC delivers → Refractor projects → Postgres row appears
   measures p99 latency over 100 mutations < 500ms.

8. **Adapter interface preserved verbatim:** Materializer's `internal/adapter/adapter.go` interface (`Upsert(keys, row) / Delete(keys) / Probe / Close`) carries over to `internal/refractor/adapter/adapter.go` without signature change. Tombstone handling: `Delete` is reused, BUT the adapter implementation translates "delete" into "soft-delete-with-`isDeleted: true`" per AC #4. For Postgres: UPDATE row SET `is_deleted=true, deleted_at=NOW()` instead of `DELETE FROM`. For NATS-KV: PUT a tombstone document with `isDeleted: true` instead of `KVDelete`.

9. **Document envelope reshape (AC #4):** projection output documents must carry Lattice's Contract #1 envelope fields where they appear. Materializer's `internal/adapter/postgres.go` writes flat rows — minimal reshape required there. Where a doc envelope is materialized (e.g., a `nats-kv` adapter projecting a full vertex document), include `id`, `class`, `createdAt`, `updatedAt`, `createdBy`, `updatedBy`, `isDeleted`, plus any aspect fields. Use `substrate.NewDocumentEnvelope` / `AspectEnvelope` helpers where applicable.

10. **Postgres provisioning:** add a Postgres service to `docker-compose.yml`. Suggested: `postgres:16-alpine`, single database `refractor`, single role `refractor` with password from env. The Postgres adapter reads `REFRACTOR_PG_DSN` env var. Schema management: Refractor's existing migration tooling (if any) is preserved; otherwise, the adapter creates tables on first projection (idempotent CREATE IF NOT EXISTS). Update `make up` to bring Postgres up; update `make down` to tear it down; update `make verify-bootstrap` to assert Postgres connectivity. Update `.github/workflows/ci.yml` so CI has the same Postgres service available.

11. **Capability KV auth stub for control service (AC #5):** Materializer's control service likely has its own auth model. Adapt it to read Capability KV via a `CapabilityChecker` interface; provide a `StubCapabilityChecker` (allow-all + log) that mirrors `internal/processor/StubAuthorizer` from Story 1.5. Full Capability KV integration is Epic 3. Document this in the failure-tier doc.

12. **Refractor in `make up`:** `make up` flow becomes: `docker compose up -d --wait` (now includes Postgres) → build bootstrap binary → build refractor binary → start refractor-stub (REMOVED — DELETE this step) → start REFRACTOR in background → run bootstrap → wait for `health.refractor.<instance>` AND `health.bootstrap.complete` in Health KV → done. Refractor's role replaces refractor-stub's role of "watch for bootstrap readiness" — Refractor naturally satisfies that role by emitting its own health key once started.

13. **Delete `cmd/refractor-stub/` after parity is established.** The directory + binary go away in this story. Update `bin/.gitignore` if it references the stub. The morph plan §5 "DISCARD" list applies here.

14. **NO PARSER WORK in Story 2.1.** The morph plan's Phase 3 (Capability Lens spike) and Phase 4 (parser expansion) are explicitly out of scope. Use Materializer's existing parser AS-IS. If the e2e test (AC #10) requires a parser feature not currently supported, raise a CONTRACT-AMENDMENT-REQUEST and HALT — do not extend the parser yourself in 2.1.

15. **NO net-new morph deltas in 2.1.** Morph plan Phase 5 items (Personal Lens NATS subject adapter, Path Projection/RLS, Crypto-shred listener, Secure Lens type) are all out of scope for 2.1 and folded into Story 2.2's gap analysis as documented gaps.

16. **MORPH-DEVIATIONS.md format:**
    ```markdown
    ## Deviation N: <short title>
    **Morph plan section:** §X.Y (link)
    **Plan said:** <quote or paraphrase>
    **Actual decision:** <what we did>
    **Driver:** <which Lattice contract section, AC clause, or substrate constraint forced this>
    **Downstream implication for Story 2.2 / Phase 2:** <what gets re-examined or extended later>
    ```
    Maintain this file as you go — append, don't batch.

17. **Test preservation (AC #1):** Materializer's existing unit + integration tests are copied into `internal/refractor/...` alongside their packages. Update import paths only. If a test fails for reasons OTHER than import paths, FIX THE MORPH, not the test — the test was passing in Materializer, so a failure post-morph means an adaptation went wrong. Exceptions: tests that exercise removed features (subject taxonomy, Materializer-specific control endpoints) can be deleted, with a corresponding entry in MORPH-DEVIATIONS.md.

18. **Health Heartbeater integration (AC #6):** use Lattice's existing `internal/processor/health.go` Heartbeater pattern as a reference. Add a Refractor-equivalent in `internal/refractor/health/` that emits `health.refractor.<instance>` every 10s and per-Lens lag every 10s. Lag formula per AC #6: `stream_last_seq − consumer_acked_seq` plus an estimated milliseconds value computed from JetStream sequence timestamps.

19. **Subject taxonomy (morph plan §6):** apply the rename table verbatim. The `subjects` package is the chokepoint. After morph, `grep -r "materializer" internal/refractor/ cmd/refractor/` should return zero matches (excluding comments that explicitly cite history).

20. **CI implications:** update `.github/workflows/ci.yml` to include Postgres (mirror the docker-compose service or use a Postgres GitHub Actions service container). The CI step "Bring up Docker stack" already runs `make up` which now includes Postgres — so this may "just work" if docker-compose is updated. Verify by reading the workflow.

## Suggested Morph Sequence

**Phase A — Exploration & plan checkpoint (5-10K tokens):**
1. Read materializer-morph-plan.md fully
2. Scan `/Users/andrewsolgan/Documents/GitHub/Materializer/internal/` tree — `ls -la` each subdir, read each package's top-of-file doc comments
3. **Send planning checkpoint message** with 8-12-bullet morph sequence + your initial token estimate
4. (No halt → proceed)

**Phase B — Skeleton + rename (15-25K tokens):**
5. Create `internal/refractor/` directory tree
6. Copy each Materializer package, batch-rename via the §6 table, update import paths
7. Create `cmd/refractor/main.go` — wired but minimal
8. `go build ./...` green; existing materializer tests run (some failing OK at this stage if they require post-morph wiring)

**Phase C — Substrate refactor + bootstrap + dev harness (25-40K tokens):**
9. Refactor all NATS access in `internal/refractor/` to use `internal/substrate`
10. Provision `refractor-adjacency` bucket + KV consumer config in `internal/bootstrap/primordial.go`
11. Update `verify-bootstrap` for new bucket
12. Add Postgres to `docker-compose.yml`
13. Update `Makefile` (`make up`, `make down`, `make verify-bootstrap`)
14. Update `.github/workflows/ci.yml` if needed
15. Delete `cmd/refractor-stub/`

**Phase D — Lens source migration (20-30K tokens):**
16. Refactor Materializer's Loader: rule-stream consumption → Core KV watch on `vtx.meta.lens.>`
17. Write `internal/refractor/lens/translator.go` (Core KV aspect bundle → `*Lens`)
18. Implement hardcoded bootstrap lens (Decision #7) gated behind env var
19. Tests for the translator + watch integration

**Phase E — Adapter tombstone semantics + envelope reshape (15-20K tokens):**
20. Adapt Postgres adapter: soft-delete instead of DELETE
21. Adapt NATS-KV adapter: tombstone-with-`isDeleted: true` instead of KVDelete
22. Envelope reshape pass through projection output

**Phase F — Health + control service stub + failure tier doc (10-15K tokens):**
23. Refractor Heartbeater (per-instance + per-Lens lag) → `health.refractor.<instance>.*`
24. Control service: keep handlers; swap auth to `StubCapabilityChecker`
25. Write `docs/refractor-failure-tiers.md`

**Phase G — E2E integration test (10-15K tokens):**
26. Write the E2E test (AC #10): activate Lens via Processor → write vertex → assert Postgres row appears within < 500ms p99 over 100 mutations
27. Run it. Fix any gaps.

**Phase H — Wrap-up (5-10K tokens):**
28. Finalize `MORPH-DEVIATIONS.md`
29. Run full gates: `make verify-bootstrap`, `go build`, `go vet`, `go test ./...`, `make test-bypass` (regression check)
30. Update token tracker Row 2.1 — round UP
31. Closing summary

**Total estimated:** 105-165K. If you hit 150K and not yet at Phase G, HALT and escalate.

## Deliverables Checklist

1. ✅ Phase-A planning checkpoint sent
2. ✅ `internal/refractor/` package tree with renamed Materializer code
3. ✅ `cmd/refractor/` binary entry point
4. ✅ All Materializer unit + integration tests passing under new import paths
5. ✅ Substrate-based NATS access (no direct nats.go usage in refractor code outside what substrate already wraps)
6. ✅ `refractor-adjacency` KV bucket provisioned in bootstrap + verify-bootstrap assertion
7. ✅ Postgres service in docker-compose.yml + Makefile wiring + CI workflow updated
8. ✅ Core KV watch on `vtx.meta.lens.>` replacing Materializer's rules-stream loader
9. ✅ Lens translator (Core KV aspect bundle → `*Lens`)
10. ✅ Hardcoded bootstrap lens (env-gated)
11. ✅ Adapter tombstone semantics: Postgres soft-delete, NATS-KV `isDeleted: true` tombstone
12. ✅ Document envelope reshape per Contract #1 wherever projection output materializes a full doc
13. ✅ Refractor health emissions to `health.refractor.<instance>.*` + per-Lens lag every 10s
14. ✅ Control service preserved, auth via `StubCapabilityChecker`
15. ✅ `docs/refractor-failure-tiers.md` (crypto-shred → privacy-critical; Capability Lens → security-critical)
16. ✅ `cmd/refractor-stub/` deleted; `make up` starts real Refractor
17. ✅ Refractor emits `health.refractor.<instance>` within 10s of startup (manual verify in closing summary)
18. ✅ E2E integration test (AC #10): activate Lens via Processor → 100-mutation write → Postgres p99 < 500ms
19. ✅ `MORPH-DEVIATIONS.md` maintained as you go; finalized in Phase H
20. ✅ `make verify-bootstrap` green (now ≥33 assertions including refractor-adjacency bucket)
21. ✅ `make test-bypass` still green (Epic 1 regression)
22. ✅ `go build ./...`, `go vet ./...`, `go test ./... -count=1` exit 0
23. ✅ CI green on push (verify after Andrew commits)
24. ✅ Token tracker Row 2.1 updated — HONEST estimate, round UP
25. ✅ Closing summary including a "morph audit" section: # files copied, # lines added/deleted, # tests preserved vs deleted, # deviations logged

## What Story 2.1 Is NOT

- **Not** the Capability Lens spike (morph plan Phase 3) — that's Epic 3 territory.
- **Not** any parser extension (morph plan Phase 4) — HALT and escalate if needed.
- **Not** Personal Lens NATS-subject adapter (morph plan §2.2) — gap-analyze in Story 2.2.
- **Not** Path Projection / RLS (morph plan §2.1) — gap-analyze.
- **Not** Crypto-shred listener (morph plan §2.4) — gap-analyze.
- **Not** Secure Lens (morph plan §2.5) — gap-analyze.
- **Not** Gap Analysis itself — Story 2.2 (next).

## Escalation (READ TWICE)

Halt and escalate via Andrew if:
- Materializer's parser cannot evaluate the trivial e2e Lens cypher
- Any AC clause requires touching morph plan Phase 3/4/5 work to satisfy
- Postgres provisioning surfaces a non-trivial schema concern
- Materializer's adapter interface needs a signature change to satisfy tombstone semantics
- Token estimate exceeds 150K (per the operating rules)
- The control service's auth model can't be cleanly swapped to `StubCapabilityChecker`
- Any test that was passing in Materializer fails post-morph for a reason you can't reduce to import-path/rename mechanics within 15 minutes of investigation
- You discover a CONTRACT-AMENDMENT-REQUEST-worthy issue — append to `cmd/refractor/CONTRACT-AMENDMENT-REQUEST.md` (create the file)

## Closing

1. Verify all 25 deliverables
2. Full reset: `make down && make up && make verify-bootstrap` green; processor + bypass test suites green; refractor e2e test green with p99 < 500ms over 100 mutations
3. Final pass on `MORPH-DEVIATIONS.md` — is every deviation captured with the four required fields?
4. Update token tracker Row 2.1 — round UP. Note this is the FIRST Opus story whose self-estimate accuracy will be measured against the established 30-50% gap.
5. Closing summary: deliverables status, morph audit (files/lines/tests/deviations), e2e p99 number, token estimate (honest), open questions, escalations
6. Note CI cannot be verified until Andrew commits — flag that as the final gate

Do NOT commit. Winston + Andrew review and commit.
