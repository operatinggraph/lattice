---
title: Story 4.6 Implementation Handoff Brief
story: 4.6 — Capability Package Format + Installer + Identity Hygiene as First Package
model_tier: Opus (locked)
token_budget: ~180K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-19
predecessor: Story 4.5 (Staff-Approved Identity Merge, shipped at b314677); PHASE-1-COURSE-CORRECTION.md (2026-05-19 audit)
---

# Story 4.6 — Capability Package Format + Installer + Identity Hygiene: Handoff Brief

## Your Role

Introduce the **Capability Package** — a Lattice-native way to add optional platform behavior post-bootstrap by submitting an atomic bundle of Core KV writes. Use it to redesign Epic 4's duplicate-detection / merge feature according to "operations write, lenses read":

1. Define the package directory format + manifest schema.
2. Implement a Go installer (`cmd/lattice-pkg/`) that reads a package and writes its contents to Core KV via `substrate.AtomicBatch` (operator-credentialed).
3. Author the first real package: `packages/identity-hygiene/` — provides `MergeIdentity` op, Duplicate Candidates Lens, Levenshtein cypher executor UDFs.
4. Walk back the Epic 4 read-as-op accommodations: delete `ApproveIdentityMerge` + `ScanIdentityDuplicates` ops (their replacements are the lens + a CLI verb that reads the lens KV directly), revert `ContextHint.ScanPrefixes`, retire `state.keys_with_prefix` Starlark builtin, tighten `OperationReply.Detail` semantics.

This story does NOT shrink the bootstrap kernel — that's Story 4.7. This story proves the package mechanism works *on top of* the existing 154-OK bootstrap.

After 4.6 ships, identity-hygiene is the first installable package and the duplicate-detection feature works via the architecturally-correct lens-based path.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **No worktree.** Work directly in `/Users/andrewsolgan/Documents/GitHub/Lattice` against branch `main`. Verify with `pwd` at startup.
- **No commits, no pushes.** Winston commits + pushes after review.
- **Planning artifacts are read-only.** Drift → append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue. Do NOT edit `data-contracts.md`, `epics.md`, `lattice-architecture.md`. Course-correction artifacts (`PHASE-1-COURSE-CORRECTION.md`) are read-only.
- **Token budget is for tracking only, NOT a halt threshold.** Estimate ~180K. Record outer-telemetry actual at session close.
- **Halt and escalate** on stuck-loop patterns.
- **Checkpoint every 8-10 tool calls OR after any deliverable OR after any file read >25KB.**
- **Model tier:** Opus only.
- **Architecture binding:** Contract #1 (key shapes), Contract #2 (operation envelope), Contract #3 (Starlark return shape), Contract #6 (Capability KV); `PHASE-1-COURSE-CORRECTION.md` §C7 (Story 4.6 scope); `docs/components/_packages.md` (you author this — see deliverable §1).
- **Lint watch:** golangci-lint v2 on CI flags unused helpers. Run `/Users/andrewsolgan/go/bin/golangci-lint run ./...` before declaring done.
- **Andrew has authorized autonomous proceed.**

## What's Already in Place (do NOT redo)

- **`substrate.AtomicBatch`** (Story 1.4) — single-bucket all-or-nothing batch. The installer uses this against `core-kv`.
- **`substrate.PublishBatch`** (Story 1.8) — non-conditional event batch.
- **Bootstrap-seeded identity DDL** (Story 4.1) at `internal/bootstrap/identity_ddl.go` with `permittedCommands: [CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity, FlagIdentityForReview, ApproveIdentityMerge, MergeIdentity, TombstoneIdentity, ScanIdentityDuplicates]`. **You will trim this** to `[CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity]` and remove the corresponding script branches. `MergeIdentity` moves into the identity-hygiene package's own DDL.
- **Capability KV authorization** (Story 3.3) — your installed package's permissions must integrate with this.
- **Refractor full openCypher engine** (Stories 3.1a, 3.1b-i, 3.1b-ii) — Levenshtein UDFs extend the executor.
- **Refractor `kv.Watch` on `vtx.meta.>`** (Story 2.1) — the Duplicate Candidates Lens spec aspect activates via the same watcher path the Capability Lens uses. (Story 2.4b will migrate this to durable consumer; both options work; nothing in 4.6 depends on the choice.)
- **3.7 Vector #1 defense** — Refractor reprojection overwrites direct KV writes. Applies to your new lens's output bucket too; no new defense needed.
- **`OperationReply.Detail` field** (Story 4.2) at `internal/processor/envelope.go:160`. You **tighten its semantics** (commit-trace only, not business data) by convention; the field stays.

Tree is clean at session start (commit `6b2e1d7` after C1/C2/C3 course-correction edits; verify-bootstrap 154 OK; test-bypass 4/4 BLOCKED; test-capability-adversarial 4/4 DEFENDED).

## Story Scope (4.6)

### 1. Package format specification (~15K tokens, docs)

Create `docs/components/_packages.md` (~250 lines). Contents:

- **What a package is**: a versioned, atomic bundle of Core KV writes that adds optional platform behavior after bootstrap. Examples: identity-hygiene (this story), future business domains (lease-signing, work-order, payment-reconciliation).
- **Directory layout**:
  ```
  packages/<package-name>/
    manifest.yaml            # name, version, dependencies, declared canonical names
    ddls.go                  # Go literal definitions of DDL meta-vertices + Starlark scripts
    lenses.go                # Go literal definitions of Lens meta-vertices + cypher source
    permissions.go           # Permission vertices + grants
    README.md                # human-facing description
  ```
- **manifest.yaml schema** (full YAML example included in the doc):
  ```yaml
  name: identity-hygiene
  version: 0.1.0
  description: Duplicate-identity detection + operator-approved merge.
  depends:
    - identity-domain    # if installed; for 4.6 this is the in-bootstrap identity DDL
  declares:
    ddls:
      - canonicalName: identityHygiene
        class: meta.ddl.vertexType
    lenses:
      - canonicalName: duplicateCandidates
        adapter: nats-kv
        bucket: duplicate-candidates
        engine: full
    permissions:
      - operationType: MergeIdentity
        scope: any
        grantsTo: [operator]
  ```
- **Installation semantics**:
  - Installer reads manifest + dependency check (Phase 1: warn + proceed if missing; Phase 2 enforce strict)
  - Installer constructs all Core KV writes: DDL meta-vertex + 4 aspects per declared DDL, Lens meta-vertex + ≥3 aspects per declared Lens, permission vertex per declared permission, grant link per `grantsTo` entry, plus one `vtx.package.<NanoID>` vertex with `.manifest` aspect carrying the full manifest JSON
  - One `substrate.AtomicBatch` call commits everything on the `core-kv` bucket
  - Refractor + Processor auto-pick up the new meta-vertices via existing CDC watches (no restart)
  - **Idempotency**: installer reads `vtx.package.<canonicalName-NanoID>` first; if present with same version, no-op. Different version → for Phase 1 refuse + log; Phase 2 adds upgrade path.
- **Uninstall semantics**:
  - Installer enumerates every Core KV key written by the package (recoverable from the `vtx.package.<NanoID>.manifest` aspect's value, which lists all declared canonical names → NanoIDs)
  - Soft-deletes each via `substrate.AtomicBatch` with `isDeleted: true` envelopes
  - The Refractor reprojects (lens output disappears; permissions removed from cap entries within NFR-P3 lag)
  - `vtx.package.<NanoID>` itself soft-deleted last
- **Phase 1 limitations** (document explicitly):
  - Installer uses substrate directly, not the operation envelope path. This is the "skeleton install" — operator credential is just the admin NanoID read from `lattice.bootstrap.json`. Phase 2 / Story 5.3 follow-up will replace the installer internals with `CreateMetaVertex` operations submitted through the Processor (capability-authorized, rollback-able via compensating ops).
  - No dependency resolution graph (just warn-and-proceed)
  - No upgrade path (refuse on version mismatch)
- **What a package CANNOT do** (Phase 1):
  - Mutate other packages' DDLs (no `UpdateMetaVertex` of another package's vertex)
  - Reach into substrate-level surfaces (streams, buckets, JetStream configs)
  - Override bootstrap-seeded primordial data

This is the canonical reference. Future packages follow this spec.

### 2. Installer binary: `cmd/lattice-pkg/` (~35K tokens)

Subcommands:
- `lattice-pkg install <path-to-package-dir>` — read manifest + ddls.go + lenses.go + permissions.go, build the atomic-batch op list, submit
- `lattice-pkg uninstall <package-canonical-name>` — read `vtx.package.<canonical-name-NanoID>.manifest`, enumerate, soft-delete
- `lattice-pkg list` — read all `vtx.package.>` entries, list installed packages

Implementation:
- `cmd/lattice-pkg/main.go` (~200 LOC) — CLI entry + dispatch
- `internal/pkgmgr/` (NEW Go package, ~400 LOC) — installer logic, manifest parsing, atomic-batch construction, uninstall enumeration
- Reads admin actor NanoID from `lattice.bootstrap.json` (filesystem-bound credential for Phase 1)
- Connects to NATS via substrate
- All writes go through `substrate.AtomicBatch` on `core-kv` bucket
- Provenance: every primordial-bootstrap-style aspect carries `createdBy: <admin-actor-key>`, `createdByOp: "pkg-install:<package-name>"` (Phase 1 substitutes for real op envelope's traceId)

### 3. Levenshtein cypher executor UDFs (~15K tokens)

Move Levenshtein from the Starlark `strings.*` module (Story 4.4) into the cypher executor:

- Add to `internal/refractor/ruleengine/full/executor.go`:
  - `evaluateFunctionCall` (or wherever function calls are dispatched) gains two cases: `levenshteinDist(a, b) -> int`, `levenshteinRatio(a, b) -> float`
  - Implementation copied from `internal/processor/starlark_builtins.go` (the `levenshteinDistance` + `levenshteinRatio` helpers — they're pure Go already)
  - Both functions take two string parameters; type-check inputs; return appropriate types
  - Pure / deterministic / O(N²) time + O(min) space
- Add to `internal/refractor/ruleengine/full/parse_test.go` and a new `executor_test.go` cases: parse + execute a cypher snippet using each function
- **Retire** the Starlark `strings.*` module: delete `stringsModule()` from `starlark_builtins.go`, remove its registration from `starlark_runner.go`, delete the `strings.levenshtein*` tests. This is part of the Epic 4 walk-back; do it cleanly.

### 4. Identity-hygiene package: `packages/identity-hygiene/` (~30K tokens)

Contents per the format spec (§1):

- **manifest.yaml** — as the example above
- **ddls.go** — declares the `identityHygiene` DDL meta-vertex:
  - `canonicalName: "identityHygiene"`
  - `class: "meta.ddl.vertexType"` (targets identity vertices)
  - `permittedCommands: ["MergeIdentity"]`
  - `.description`: plain-language description
  - `.script`: Starlark script handling `MergeIdentity` with the corrected logic:
    - Pre-flight: primary != secondary; both exist; both not tombstoned; both flagged or one+other flagged (or relax — see open question A in this brief's §6); `duplicateOf` link exists between them
    - Inbound link enumeration: NOT via `lnk.` global scan. Use `state.read(adjacency.<secondaryKey>)` via a NEW hydrator path (see §4a below). For each adjacency entry's EdgeID (which IS the link key per Story 3.2b), enumerate.
    - Outbound link enumeration: narrow scan of `lnk.identity.<secondaryId>.` via a NEW `LinkScans []string` `ContextHint` field (see §4b below)
    - Build MutationBatch: tombstone old + create new for each link involving secondary; secondary `.state` → "merged"; secondary `.mergedInto` → primary's key; skip self-loops (tombstone only)
    - `linkCollisionsMerged` count for cases where the new link key already exists
    - Optional `aspectConflictResolution` for {name, email, phone}
    - Pre-flight reject if total mutations > 999 with `MergeBatchTooLarge`
    - One `IdentityMerged` event
    - `ResponseDetail`: commit-trace shape `{mutationCount, linksMigrated, linkCollisionsMerged, eventCount}` — NO business data (no name/email leak)
- **lenses.go** — declares the `duplicateCandidates` Lens:
  - `canonicalName: "duplicateCandidates"`
  - `class: "meta.lens"`
  - `.spec`: cypher rule body matching:
    ```cypher
    MATCH (a:identity), (b:identity)
    WHERE a.key < b.key
      AND a.state IN ['unclaimed', 'claimed']
      AND b.state IN ['unclaimed', 'claimed']
      AND (
        (a.email = b.email AND a.email IS NOT NULL)
        OR (a.phone = b.phone AND a.phone IS NOT NULL)
        OR levenshteinRatio(a.name, b.name) >= 0.85
      )
    RETURN a.key AS aKey, b.key AS bKey,
           {name: a.name, email: a.email, phone: a.phone, state: a.state} AS primaryDetail,
           {name: b.name, email: b.email, phone: b.phone, state: b.state} AS secondaryDetail,
           CASE
             WHEN a.email = b.email THEN 'exact-email'
             WHEN a.phone = b.phone THEN 'exact-phone'
             ELSE 'levenshtein-name'
           END AS criterion
    ```
  - `adapter: nats-kv`, `bucket: duplicate-candidates`, `engine: full`
  - Output key shape: `flagged.identity.<aKey-NanoID>.identity.<bKey-NanoID>` (lexicographic order)
  - Refractor auto-creates the bucket on first projection per existing pattern
- **permissions.go** — declares:
  - `MergeIdentity` permission vertex (scope: any), grants to operator role (currently `RoleOperatorID` from bootstrap; the grant link is built at install time using the operator role's NanoID, which the installer reads from Core KV)
- **README.md** — short description for package consumers

### 4a. Hydrator adjacency-read path (~10K tokens)

Add to `internal/processor/step4_hydrate.go`:
- New `ContextHint.AdjacencyReads []string` field — list of vertex keys whose adjacency entries to load
- Hydrator queries `refractor-adjacency` bucket for each (read-only access; Processor was not previously a reader of this bucket)
- For each entry, the hydrator loads the corresponding link envelope from `core-kv` (the EdgeID == link key per Story 3.2b)
- Adds an entry to the hydrated state map keyed by the link key
- Hard cap: 200 adjacency entries per vertex (Phase 1 bound; if exceeded, return `HydrationError("adjacency-too-large", count)`)

In `internal/substrate/`: add the helper `(*Conn).AdjacencyForNode(nodeKey string) -> ([]string, error)` that reads the `refractor-adjacency` bucket and returns EdgeIDs. Adjacency entries today store the EdgeID (= link key) as the value; their key format is `<srcNodeKey>:<edgeId>` or similar per Story 3.2b — verify in code before implementing.

### 4b. Hydrator narrow LinkScans (~5K tokens)

Add to `internal/processor/step4_hydrate.go`:
- New `ContextHint.LinkScans []string` field — list of 4-segment prefixes like `lnk.identity.<id>.`
- For each prefix, hydrator scans Core KV (`substrate.KVListKeys`) and loads matching link envelopes
- Hard cap: 100 keys per prefix (Phase 1 bound; identity out-degree is small)
- Reject prefixes that aren't 4-segment-with-trailing-dot (`HydrationError("link-scan-not-narrow", prefix)`)

### 5. Revert deprecated Processor surfaces (~10K tokens)

- **Remove** `ContextHint.ScanPrefixes` field from `internal/processor/envelope.go` (it's no longer used after step §6 below removes the only callers)
- **Remove** the `hydrateScanPrefix` function from `internal/processor/step4_hydrate.go`
- **Remove** the `keys_with_prefix` method from the `state` global in `starlark_runner.go`
- **Remove** the `strings.*` module entirely from `starlark_runner.go` + `starlark_builtins.go` (Levenshtein now in cypher executor)
- **Verify** no test references these surfaces (run `grep -r "ScanPrefixes\|keys_with_prefix\|strings\.levenshtein" internal/` and clean up)

### 6. Identity DDL trim (~10K tokens)

In `internal/bootstrap/identity_ddl.go`:
- **Trim** `permittedCommands` to `[CreateUnclaimedIdentity, UpdateIdentityState, ClaimIdentity]`
- **Remove** the `MergeIdentity`, `ApproveIdentityMerge`, `ScanIdentityDuplicates`, `FlagIdentityForReview`, `TombstoneIdentity` branches from the Starlark script
- **Keep** the state machine for the 3-state shape: `unclaimed → claimed → merged`. The `merged` state stays in core because `enforce_not_merged` is a core data-integrity invariant. (The hygiene package's MergeIdentity script is what *sets* state to merged; without the package installed, nothing can set it, but if something somehow did, the guard correctly rejects further ops.)
- **Remove** `flagged-for-review` from the state machine entirely. No more `unclaimed → flagged-for-review`, `claimed → flagged-for-review`. The flagged state was an Epic-4-drift accommodation; with the lens projecting candidates, no stored state is needed.
- **Remove** the seeded `MergeIdentity` and related stub permissions (they were never granted; just unused vertices in the DDL's permittedCommands list)
- **Keep** all Epic 4 integration tests that exercise `CreateUnclaimedIdentity` and `ClaimIdentity`. Tests that exercise `ScanIdentityDuplicates`, `ApproveIdentityMerge`, `MergeIdentity`, `FlagIdentityForReview`, `TombstoneIdentity` get **DELETED** (their replacements live in the package's test fixture set, §7 below).
- **Update** `internal/bootstrap/identity_ddl.go`'s `.description` aspect text to reflect the new 3-state shape
- **Update** `verify-bootstrap` expected count: the seeded identity DDL drops several permissions/grants. New count likely ~140 OK (down from 154). Update `scripts/verify-bootstrap.go` accordingly.

### 6a. Revert `pendingReview` capabilityenv field (~3K tokens)

In `internal/refractor/capabilityenv/envelope.go`:
- Remove the `pendingReview` field injection (Story 4.4 added it)
- Remove the wrapper unit test that asserted `pendingReview` presence
- The Capability Lens cypher's identity-state filter handles "merged" exclusion already; no need for the extra projected field

### 7. Integration tests (~25K tokens)

Two test surfaces:

**Surface A — `internal/pkgmgr/installer_test.go`** (~5 tests):
- TestInstaller_HappyPath: install identity-hygiene from `packages/identity-hygiene/`; assert DDL meta-vertex + Lens meta-vertex + permission vertex + grant link all present in Core KV; package vertex at `vtx.package.<NanoID>` exists with `.manifest` aspect
- TestInstaller_Idempotent: install twice; second is no-op (same version detected)
- TestInstaller_RefusesDifferentVersion: install v0.1.0, then bump manifest to v0.2.0 and attempt → refuse
- TestInstaller_Uninstall: install, then uninstall; assert package vertex + all declared entries soft-deleted; cap KV reprojection removes MergeIdentity from operator's cap entry within 1s
- TestInstaller_ListShowsInstalled: list with one installed → shows it; uninstall → no longer shown

**Surface B — `packages/identity-hygiene/integration_test.go`** (~8 tests):
- TestHygiene_LensProjection_ExactEmail: seed 2 identities with same email → wait for projection → assert `flagged.identity.<lo>.identity.<hi>` exists with criterion=exact-email
- TestHygiene_LensProjection_LevenshteinName: seed 2 identities with similar names → projection → criterion=levenshtein-name
- TestHygiene_LensProjection_SkipsMerged: seed merged identity + match → not flagged
- TestHygiene_LensProjection_NFR_P3: 100 mutations → p99 < 500ms (mirror Story 3.2b)
- TestMerge_HappyPath: install hygiene, seed 2 flagged identities, run MergeIdentity → link migration verified, state machine respected, response detail has commit-trace-only data (no business-data leak)
- TestMerge_RejectsNonDuplicate: pre-flight rejects merge of identities not in the candidates lens
- TestMerge_AdjacencyBasedInboundEnum: identity has both outbound and inbound links → merge picks up both
- TestMerge_PostMergeRedirect_FR4: post-merge ops against secondary fail with IdentityMerged (via core enforce_not_merged guard, which stays in core)

Tests for the deleted Starlark surfaces (ScanPrefixes, keys_with_prefix, strings module) get deleted, not migrated.

### 8. Verify-bootstrap + bypass + Gate 3 re-audit (~5K tokens)

- Update `scripts/verify-bootstrap.go` to expected count (likely ~140 OK)
- Run `make verify-bootstrap` — green
- Run `make test-bypass` — 4/4 still BLOCKED (Levenshtein move doesn't affect bypass surface)
- Run `make test-capability-adversarial` — 4/4 still DEFENDED. New consideration: a fabricated `flagged.identity.<x>.identity.<y>` entry in the duplicate-candidates bucket is corrected by the existing Refractor reprojection mechanism (Story 3.7 Vector #1 defense applies generically to all lens output buckets). Confirm via spot-check; no new test required.

## Architectural Decisions Already Made (Winston)

1. **Package directory at repo root: `packages/<name>/`.** Not under `_bmad-output/` (those are planning artifacts) and not in `internal/` (those are private to the Go module). Repo-rooted because packages are first-class platform artifacts.

2. **Manifest is YAML, package definitions are Go.** Go for `ddls.go`/`lenses.go`/`permissions.go` because Starlark script literals + cypher source need string-formatted multi-line code that's painful in YAML. The installer parses Go via standard `go/parser` package OR (simpler) the package's Go files declare exported variables `Package = pkgmgr.Definition{...}` that the installer imports directly. Pick the import path — it's mechanically simpler and avoids reflection. The cost is `cmd/lattice-pkg/` becomes the place that imports `packages/identity-hygiene/`; this is fine for Phase 1.

3. **One atomic batch per install.** All package writes in a single `substrate.AtomicBatch` call against `core-kv`. Cross-bucket atomicity isn't supported by NATS atomic batch (Story 1.1), so packages that need writes to other buckets (e.g., seed entries into `capability-kv`) are NOT supported in Phase 1 — flag in the spec.

4. **Operator credential from filesystem.** `lattice.bootstrap.json` contains the admin actor NanoID. Installer reads it. Phase 2 replaces with real NATS auth.

5. **Levenshtein moves to cypher executor**, not retained as a Starlark `strings.*` module. The Starlark sandbox is for write-path DDL scripts; pure search/math functions belong in the read-path cypher engine where lenses run. Net: smaller Starlark surface, cleaner separation.

6. **`pendingReview` cap field reverts.** Lens consumers (CLI `lattice candidates list`) read the `duplicate-candidates` KV bucket directly; they don't need the cap entry to carry a flag. The Capability Lens cypher's identity-state filter already excludes `merged` identities.

7. **`flagged-for-review` state machine entry deleted entirely.** Without ScanIdentityDuplicates writing it and without `ApproveIdentityMerge` reading it, the state is unused. Lens-based candidate detection doesn't write any state on the identity vertex.

8. **3-state machine: `unclaimed → claimed → merged`.** `merged` is set only by the hygiene package's MergeIdentity script. Core invariant `enforce_not_merged` stays.

9. **`MergeIdentity` package permission grants to operator only.** Same as the pre-correction unsealed grant; this is the first time it's seeded as a real perm + grant pair (Story 4.5 left it unseeded as a Phase-1-carry-to-be-addressed-here).

10. **Identity-hygiene depends on identity-domain.** Today's "identity-domain" is the bootstrap-seeded identity DDL (not yet a package). Manifest declares the dependency for future Story-4.7 enforcement; the installer for 4.6 warns-and-proceeds.

11. **Hydrator `AdjacencyReads` field is new Processor↔Refractor coupling.** Processor was previously not a reader of the adjacency bucket. This crosses a soft boundary — Processor used to be Core-KV-only — but the alternative (read all `lnk.>` and filter) is exactly the pattern we're walking back from. The narrow adjacency-read is the architecturally-correct shape. Document the new coupling in `docs/components/processor.md` (a small update — append to the In-contracts section).

12. **`LinkScans` accepts only 4-segment prefixes ending with `.`.** Hard-bounded scan from a specific source vertex's outbound link prefix. Wider patterns are rejected at hydrate time.

13. **Package atomicity contract: install OR fail entire.** Substrate atomic batch gives this for free; document explicitly.

14. **Uninstall is soft-delete only.** Tombstone vertices remain queryable for audit; physical removal is out of scope.

15. **No real-NATS-auth in 4.6.** Installer uses bootstrap-admin-NanoID-from-filesystem. Real auth is Phase 2 + Story 5.3 follow-up.

16. **`OperationReply.Detail` semantics:** convention-enforced via a comment block at `envelope.go:160` describing what's allowed (commit-trace) vs forbidden (business data). No code enforcement; brief authors + reviewers maintain.

17. **The Duplicate Candidates Lens cypher uses `levenshteinRatio(a.name, b.name) >= 0.85`** as the hard-coded threshold for Phase 1. Operator-configurable threshold is a Phase 2 feature (would require a `parameters` aspect on the Lens meta-vertex and executor support for parameterized cypher — both deferred).

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/PHASE-1-COURSE-CORRECTION.md` | The big picture; especially §C7 + the package-format discussion in the open-questions exchange |
| `_bmad-output/implementation-artifacts/story-4.5-handoff-brief.md` | Predecessor — adjacency mentioned in carries; merge logic to reuse |
| `_bmad-output/implementation-artifacts/story-4.4-handoff-brief.md` | Hydrator ScanPrefixes pattern (you're reverting it) |
| `_bmad-output/implementation-artifacts/story-4.1-handoff-brief.md` | Identity DDL helpers; state machine; mergedInto aspect |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.3 + §1.5 | Envelope + key shapes |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 §6.2 | Cap envelope shape (you do NOT modify this) |
| `_bmad-output/planning-artifacts/epics.md` Story 4.5 + post-correction Epic 4 header | Original Epic 4 framing; updated header |
| `internal/bootstrap/identity_ddl.go` | **Edit this** — trim ops, simplify state machine, remove flagged-for-review |
| `internal/processor/step4_hydrate.go` | **Edit this** — remove ScanPrefixes path, add AdjacencyReads + LinkScans |
| `internal/processor/envelope.go` | **Edit this** — remove ScanPrefixes field, add AdjacencyReads + LinkScans fields, add Detail-semantics comment block |
| `internal/processor/starlark_runner.go` | **Edit this** — remove strings module + keys_with_prefix |
| `internal/processor/starlark_builtins.go` | **Edit this** — delete stringsModule + levenshtein helpers |
| `internal/processor/starlark_builtins_test.go` | Delete strings.levenshtein* tests |
| `internal/refractor/ruleengine/full/executor.go` | **Edit this** — add Levenshtein UDFs |
| `internal/refractor/ruleengine/full/executor_test.go` | Add UDF tests |
| `internal/refractor/capabilityenv/envelope.go` | **Edit this** — remove pendingReview field |
| `internal/refractor/capabilityenv/envelope_test.go` | Remove pendingReview test |
| `internal/substrate/kv.go` | **Edit this** — add AdjacencyForNode helper |
| `internal/refractor/consumer/bootstrap.go` | Read-only — confirm adjacency KV key format for AdjacencyForNode |
| `scripts/verify-bootstrap.go` | **Edit this** — adjust assertion count for trimmed identity DDL |

**DO NOT read**: `lattice-architecture.md` (full), full `epics.md`, Materializer source, vendored ANTLR parser, Stories 1.x/2.x/3.x briefs.

## Suggested Sequence

**Phase A — Package format spec (target ~15K tokens):**
1. Author `docs/components/_packages.md`.

**Phase B — Substrate adjacency helper (target ~10K tokens):**
2. Add `(*Conn).AdjacencyForNode` to `internal/substrate/kv.go`. Unit test.

**Phase C — Levenshtein UDF migration (target ~15K tokens):**
3. Add UDFs to cypher executor. Tests.
4. Delete Starlark `strings.*` module + tests.

**Phase D — Hydrator narrow scans (target ~15K tokens):**
5. Add `AdjacencyReads` + `LinkScans` ContextHint fields. Implement in step4_hydrate.go. Remove ScanPrefixes path entirely.

**Phase E — Identity DDL trim (target ~15K tokens):**
6. Trim `permittedCommands`. Simplify state machine. Delete Scan/Approve/Merge/Flag/Tombstone script branches. Remove `pendingReview` capabilityenv field. Update verify-bootstrap count.

**Phase F — Installer (target ~35K tokens):**
7. Author `internal/pkgmgr/` (manifest parse, atomic-batch builder, uninstall enumerator).
8. Author `cmd/lattice-pkg/main.go`.
9. Tests in `internal/pkgmgr/installer_test.go`.

**Phase G — Identity-hygiene package (target ~30K tokens):**
10. Author `packages/identity-hygiene/{manifest.yaml,ddls.go,lenses.go,permissions.go,README.md}`.

**Phase H — Integration tests (target ~25K tokens):**
11. `packages/identity-hygiene/integration_test.go` — install + lens projection + MergeIdentity merge path.

**Phase I — Gates + closing (target ~15K tokens):**
12. Run all required gates locally.
13. Update token tracker Row 4.6.
14. Closing summary as Deliverable #16.

## Required Verification

```bash
go build ./...                                # incl. new cmd/lattice-pkg
make vet
/Users/andrewsolgan/go/bin/golangci-lint run ./...   # 0 issues
go test ./internal/processor/... -count=1
go test ./internal/refractor/... -count=1
go test ./internal/substrate/... -count=1
go test ./internal/pkgmgr/... -count=1        # new
go test ./packages/identity-hygiene/... -count=1     # new
make verify-bootstrap                         # new expected count ~140 OK
make test-bypass                              # 4/4 BLOCKED
make test-capability-adversarial              # 4/4 DEFENDED
go test ./... -p 1 -count=1                   # all packages green
```

## Deliverables Checklist

1. ✅ `docs/components/_packages.md` — package format spec
2. ✅ `cmd/lattice-pkg/main.go` — installer binary
3. ✅ `internal/pkgmgr/` — installer logic
4. ✅ `internal/substrate/kv.go` — `AdjacencyForNode` helper
5. ✅ `internal/refractor/ruleengine/full/executor.go` — Levenshtein UDFs
6. ✅ `internal/processor/envelope.go` + `step4_hydrate.go` — AdjacencyReads + LinkScans fields; ScanPrefixes removed
7. ✅ `internal/processor/starlark_runner.go` + `starlark_builtins.go` — strings module removed; keys_with_prefix removed
8. ✅ `internal/refractor/capabilityenv/envelope.go` — pendingReview field removed
9. ✅ `internal/bootstrap/identity_ddl.go` — trimmed permittedCommands; 3-state machine; flagged-for-review removed
10. ✅ `scripts/verify-bootstrap.go` — updated assertion count
11. ✅ `packages/identity-hygiene/{manifest.yaml,ddls.go,lenses.go,permissions.go,README.md}`
12. ✅ `internal/pkgmgr/installer_test.go` — 5 installer tests
13. ✅ `packages/identity-hygiene/integration_test.go` — 8 integration tests
14. ✅ All required gates green
15. ✅ Token tracker Row 4.6 updated
16. ✅ Closing summary appended to this brief

## What 4.6 Is NOT

- Not the bootstrap kernel minimization (Story 4.7)
- Not the rbac-domain or identity-domain package authoring (Story 4.7)
- Not the Refractor token eviction (Story 2.4a)
- Not the durable-consumer migration for lens source (Story 2.4b)
- Not real NATS auth
- Not Phase 2 dependency-resolution graph
- Not package versioning / upgrade

## Open Questions to resolve mid-flight (raise CAR + continue)

A. **MergeIdentity pre-flight: do both identities need to be in the candidates lens, or is the `duplicateOf` link sufficient?** The pre-Story-4.5 design (and the 4.5 brief) used the `duplicateOf` link as the gate. With the lens, the `duplicateOf` link is no longer written by anything (ScanIdentityDuplicates is deleted). Options:
   - (i) Pre-flight reads the candidates lens KV for the pair; rejects if not present. Tighter coupling between package's op and package's lens, but architecturally correct.
   - (ii) Pre-flight reads either-the-lens OR the-`duplicateOf`-link (which migrates to a Phase-2 audit trail).
   - Default: (i). Cleaner.

B. **`duplicate-candidates` bucket creation**: who creates the KV bucket? The bootstrap pre-provisions `capability-kv`; for new lens output buckets, the Refractor adapter's startup code creates-or-opens on first use. Confirm in `cmd/refractor/main.go`'s `buildAdapter` — if it create-or-opens, no installer work. Else the installer needs to provision the bucket (which crosses the substrate-level boundary from Decision #3 — flag as a real architectural question).

C. **Manifest version comparison**: simple lexicographic? semver? Phase 1 default: simple string equality (`installed.version == package.version` → no-op; mismatch → refuse).

## Escalation

Append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` for:
- Adjacency KV key format differs from Story 3.2b's documented EdgeID == link-key claim
- substrate.AtomicBatch op-list ordering matters in a way not documented
- Refractor adapter create-or-open doesn't handle new buckets at install time (open question B)
- Bootstrap-seeded operator role's NanoID can't be reliably read at install time (the installer needs it for the grant link)

Halt for:
- Bypass / Gate 3 vector flips from green
- Stuck-loop pattern
- Total identity DDL script crosses ~250 LOC (post-trim — much smaller than pre-trim)

## Closing

1. Verify all 16 deliverables
2. Run all required gates locally
3. Update token tracker Row 4.6
4. Closing summary as Deliverable #16

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.
