# Story 7.1 — `orchestration-base` package + `task` DDL + CreateTask (assignee required)

**Status:** SPEC — Winston-authored + adjudicated. §0 is the binding build target; where it elaborates or overrides the epics AC or older notes, §0 wins.
**Tier:** Opus (DDL + package + new op + new lens + **security-plane capability migration** + test migration). This is the FIRST Phase 2 story — it sets package + lens-migration patterns.
**Epic spec:** `_bmad-output/planning-artifacts/epics/phase-2-epics.md` → "Story 7.1" (line ~12). Read it for the user-story framing.
**Binding grounding (FROZEN — read these, do not redefine):** `docs/contracts/10-orchestration-surfaces.md` §10.1, §10.6 (auto-complete is NOT in 7.1 — see scope), §10.7; `docs/contracts/06-capability-kv.md` §6.6 (Phase-2 amendment) + §6.1 (disjoint-prefix multi-lens pattern). Contract #1 §1.1 (link direction). P4 (single-op invariants). D5 (Capability-Lens-read fields on root `data`).
**Depends on:** 1.5.5 (InstallPackage kernel op). Interacts with 1.5.12 (default hard delete — see A3).
**Workflow:** you are the DS sub-agent. Repo root, no worktree. Do **NOT** commit/push or edit planning artifacts (`epics/*.md`, `data-contracts.md`/`docs/contracts/*` are FROZEN contracts — do not edit; `lattice-architecture.md`; `MORPH-DEVIATIONS.md`). You MAY edit `/docs/components/*`. Questions back to Winston → append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and continue with a different deliverable.

---

## 0. ADJUDICATION — FINAL (Winston). DS builds to THIS.

### 0.0 What this story delivers (scope boundary)

Stand up the **task substrate** + **re-source ephemeral grants** out of the bootstrap god-cypher. In scope:
1. New `packages/orchestration-base/` package (mirror `packages/rbac-domain/` layout: `manifest.yaml`, `ddls.go`, `package.go`, `package_test.go`, tests).
2. The generic **`task` DDL** (§10.1).
3. The **`CreateTask`** op (assignee required + validated; no-orphan by construction, P4).
4. The package-owned **`capabilityEphemeral` lens** → disjoint key `cap.ephemeral.<actor-suffix>` (§10.7 / §6.6).
5. **Bootstrap capability cypher migration** (`internal/bootstrap/lenses.go`): drop the two `task` OPTIONAL MATCHes.
6. **Step-3 task-branch re-source** (`internal/processor/step3_auth_capability.go`): task path reads `cap.ephemeral.<actor>`.
7. **Test/fixture migration** (field-shaped task → link shape; cap-lens conformance + Gate 2/3 bypass tests that read `ephemeralGrants`).

**OUT of scope (do NOT build — later stories):** task lifecycle ops `ReAssign/Complete/Cancel` + my-tasks lens + **auto-complete on commit** (Story 7.2); service-actor bootstrap + Weaver `StartLoomPattern @ scope:any` seed (Story 7.3 / Weaver epic); any Loom/Weaver engine. 7.1 creates tasks and authorizes via them; it does not complete them.

### 0.1 A1 — Step-3 task branch is a SINGLE GET, NO `cap.<actor>` fallback (contract overrides the epics AC)

The epics AC line 26 says the task branch "falls back to `cap.<actor>` for `roles` on a task-path denial." **That is stale.** Frozen Contract #10 §10.7 + the 2026-06-03 coherence pass (revision-history line) corrected it: a task-path no-match denies with `AuthContextMismatch`, and the denial builder returns early for that code **without `actorRoles`** — so **no `cap.<actor>` second read is needed or wanted.** The task dispatch path is a **single GET of `cap.ephemeral.<actor>`, no fallback.** Build to the contract, not the AC line. (Record as a Winston correction of the epics AC.)

**Refactor shape for `handle` (`step3_auth_capability.go`):** today `handle` does ONE `KVGet(bucket, cap.<actor>)` then dispatches. Restructure so the **path is decided from `authContext` BEFORE the read**:
- `authContext.task` set → GET `cap.ephemeral.<actor>`, parse the ephemeral doc, run `matchEphemeralGrant`. Absent key → `AuthContextMismatch` (no entry = no grant). Single GET.
- role/service path (today's behavior) → GET `cap.<actor>` as now (its doc no longer carries `ephemeralGrants` — see A2).
- the existing `service && task` mutual-exclusion check stays.
Keep `matchEphemeralGrant`'s matching logic **byte-for-byte** (taskKey ∧ operationType ∧ target ∧ expiresAt>now); only its grant **source** changes.

### 0.2 A2 — The migration trio is ATOMIC (one commit; no broken intermediate)

These three MUST be built and land together (Winston commits as one); any intermediate where one is done without the others breaks ALL task-based auth:
- (5) bootstrap cypher drops the `task` OPTIONAL MATCHes → the `cap.<actor>` doc **no longer has an `ephemeralGrants` section** (remove it from the cypher RETURN + the output schema in `lenses.go`).
- (4) `capabilityEphemeral` lens now produces `cap.ephemeral.<actor>`.
- (6) step-3 task branch reads `cap.ephemeral.<actor>`.
`CapabilityDoc.EphemeralGrants` (in `capability_doc.go`) is now populated only from the `cap.ephemeral.<actor>` read, never the `cap.<actor>` doc. Decide: either keep `EphemeralGrant`/`EphemeralGrants` as the parse target for the ephemeral doc (likely cleanest — reuse the struct), or a dedicated minimal doc shape. The ephemeral doc shape must match what the lens projects.

### 0.3 A3 — `capabilityEphemeral` lens inherits DEFAULT HARD delete (Story 1.5.12)

The new lens writes `nats_kv` target, bucket `capability-kv` (the existing primordial bucket; disjoint `cap.ephemeral.` prefix — same contribution pattern as `capabilityRoleIndex`, §6.1). Do **not** set `deleteMode` → it inherits **hard** (1.5.12). Consequence: when an actor's last grant expires/their task goes away, the lens reprojects to **no row → the key is hard-deleted → absent → `AuthContextMismatch`**. This is correct (absence = denial). Ensure the projection produces an absence (or empty-grants) outcome on grant removal, and make the step-3 task branch treat **both** an absent key **and** an empty-grants doc as `AuthContextMismatch` (defensive — either is denial). The lens is per-actor aggregating an **array** of grants (like the old section), keyed `cap.ephemeral.<actor-suffix>` where `<actor-suffix>` derives the same way `cap.<actor>` does.

### 0.4 A4 — `task` DDL + link directions (§10.1, Contract #1 §1.1)

- `vtx.task.<id>` root `data` = **scalars only `{ status, expiresAt }`**; `status ∈ {open, complete, cancelled}`. **NO aspects** (UI renders from the bound op's DDL via `forOperation`; the speculative `presentation`/`params` aspects were dropped — §10.1).
- Relationships are **LINKS** (task = later-arriving **source**; the other vertex pre-exists = **target**):
  - `lnk.task.<id>.assignedTo.identity.<assigneeId>` (who performs it)
  - `lnk.task.<id>.forOperation.meta.<opId>` (the op this task grants)
  - `lnk.task.<id>.scopedTo.<type>.<targetId>` (the grant's target; often ≠ assignee)
- DDL class is the vertex-type meta-vertex (mirror how `rbac` declares `class: meta.ddl.vertexType` in rbac-domain's manifest + `ddls.go`).

### 0.5 A5 — `CreateTask` (no-orphan by construction, P4)

- **Requires an `assignee` identity** param (+ `forOperation` op meta id, `scopedTo` target, `expiresAt`, and the operationType the task grants — note the operationType is now LINK-sourced via `forOperation`→op, NOT a `task.data.grantedOperationType` field; do not reintroduce that field).
- Starlark **JIT-hydrates and validates the assignee identity** (reads the identity vertex), and **rejects with a structured `ScriptError`** if absent/invalid — single-op invariant (P4); no orphan task is ever committed.
- On success, atomically commits `vtx.task.<id>` (`status: open`, `expiresAt`) **+ the three links** in one batch.
- Validate `scopedTo`/`forOperation` targets to the extent the other ops in rbac-domain validate their link endpoints (match the established package idiom; don't over-engineer).

### 0.6 A6 — Install is atomic + idempotent via InstallPackage (1.5.5, Contract #8)

The package installs via the `InstallPackage` kernel op (thin-script/fat-manifest, per 1.5.5). Re-install is a no-op or fails closed. `manifest.yaml` declares the `task` ddl, the `capabilityEphemeral` lens, and the `CreateTask` permission (`grantsTo` — pick the role idiom rbac-domain uses; CreateTask is a staff/operator-grantable op — confirm against how other create ops are granted; if unclear, file a CAR rather than guess the grantee role).

### 0.7 A7 — Test + fixture migration (Gate 2/3 must stay green)

Migrate every field-shaped task fixture (`task.data.grantedOperationType`/`targetKey`) to the link shape + `cap.ephemeral.<actor>` doc. The Phase-1 cap-lens conformance test (§6.6) and any **Gate 2 (`make test-bypass`) / Gate 3 (`make test-capability-adversarial`)** case that constructs or reads `ephemeralGrants` must be migrated and stay all-BLOCKED/all-DEFENDED. Flag every security-test change explicitly in your closing summary so Winston can confirm it's a faithful re-sourcing, not a weakened assertion. If a gate fails in a way that is NOT a faithful field→link / `cap.<actor>`→`cap.ephemeral.<actor>` migration (i.e. an actual auth regression), STOP and file a CAR.

### 0.8 Gates (all must pass before handing back)

`go build ./...` · `make vet` · `golangci-lint run ./...` · `make verify-kernel` (bootstrap regression — the cypher change touches it) · `make test-bypass` (Gate 2, all BLOCKED) · `make test-capability-adversarial` (Gate 3, all DEFENDED) · `go test ./internal/processor/... ./internal/bootstrap/... ./packages/orchestration-base/... -count=1` · the capability E2E suite in `internal/refractor/` (the lens migration must keep it green; run with the docker stack — NATS at `nats://localhost:4222`, Postgres DSN as in the Makefile). The docker stack is currently UP. Flake retry per Deviation 14 allowed; a flake claim without a re-run is a drift signal.

---

## 1. Required reading (DS does the deep reads; do not expect them pre-loaded)

- `docs/contracts/10-orchestration-surfaces.md` §10.1, §10.7 (+ §10.6 only for context on why auto-complete is 7.2, not 7.1).
- `docs/contracts/06-capability-kv.md` §6.6 (Phase-2 amendment: `cap.ephemeral.<actor>` shape + migration notes) + §6.1.
- `packages/rbac-domain/` IN FULL — your package template (`manifest.yaml`, `ddls.go`, `package.go`, `permissions.go`, `*_test.go`). `packages/identity-domain/` for a second example. `docs/components/_packages.md` for the format spec.
- `internal/bootstrap/lenses.go` — the capability cypher you migrate (the `task` OPTIONAL MATCHes + `ephemeralGrants` RETURN section + output schema; lines ~42-77, ~85, ~102). `internal/refractor/lens/` for how a `capabilityEphemeral` full-engine lens spec is shaped (compare `capabilityRoleIndex` in `internal/bootstrap/primordial.go` — the disjoint-key precedent).
- `internal/processor/step3_auth_capability.go` (`handle`, `dispatch`, `matchEphemeralGrant`, `capabilityKeyFromActor`) + `internal/processor/capability_doc.go` (`CapabilityDoc`, `EphemeralGrant`).

## 2. Test plan (concrete)

- `CreateTask`: success commits vertex(status:open)+3 links atomically; **reject when assignee absent/invalid** (ScriptError, no commit); reject on missing required param.
- `task` DDL: installed shape = root scalars only, no aspects; the three link DDLs present with correct directions.
- `capabilityEphemeral` lens: a task assignment projects `{source,taskKey,operationType,target,expiresAt}` to `cap.ephemeral.<actor>` (link-sourced); `reportsTo` 2-hop manager delegation projects; grant removal/expiry → key hard-deleted (absent).
- Step-3 task path: matching grant → authorized; no match / absent key / empty doc → `AuthContextMismatch`; subject-scoping intrinsic (`g.Target == ac.Target`); single GET (assert no `cap.<actor>` read on the task path).
- Bootstrap `cap.<actor>` doc no longer carries `ephemeralGrants`.
- Install idempotency (re-install no-op/fail-closed).
- Migrated Gate 2/3 + §6.6 conformance cases green.

Count delivered tests from the diff. If you judge the story too large for one safe pass, halt and report a split proposal (e.g. 7.1a = package+DDL+CreateTask+lens; 7.1b = the step-3/bootstrap/test migration) rather than landing a broken security-plane intermediate.

## 3. Closing summary (append when done)

Deliverables vs §0 checklist; exact files changed (`git status`); test count (from diff); every gate + result (anything not run + why); every security-test (Gate 2/3 / §6.6) change with a one-line faithful-migration justification; any CAR filed; any deviation. Do NOT commit.

---

### DS closing summary (2026-06-03) — landed in ONE pass, no split

**Decision:** built the full story (incl. the atomic A2 trio) in one pass. The migration trio (drop bootstrap task matches + add `capabilityEphemeral` lens + step-3 reads `cap.ephemeral.<actor>`) is in the working tree together; no broken intermediate. Left uncommitted for Winston.

**Deliverables vs §0 checklist — all done:**
1. ✅ New `packages/orchestration-base/` (package.go, manifest.yaml, ddls.go, lenses.go, permissions.go, package_test.go, create_task_test.go).
2. ✅ `task` DDL (§10.1) — root data scalars only `{status, expiresAt}`, NO aspects; three link DDLs (assignedTo/forOperation/scopedTo), task = source.
3. ✅ `CreateTask` op — assignee required + JIT-validated (vertex_alive); forOperation + scopedTo endpoints validated too; no-orphan by construction (P4); atomic vertex+3-links commit; `TaskCreated` event.
4. ✅ `capabilityEphemeral` lens → disjoint `cap.ephemeral.<actor>`, bucket `capability-kv`, link-sourced (forOperation→op.operationType, scopedTo→target.key, task.data.expiresAt), reportsTo 2-hop. Default HARD delete inherited (no deleteMode override). New envelope wrapper `capabilityenv.NewEphemeralWrapper` + wiring in `cmd/refractor/main.go` (`case "capabilityEphemeral"`, fan-out + latency installed).
5. ✅ Bootstrap cypher migration (`internal/bootstrap/lenses.go`): dropped both `task` OPTIONAL MATCHes + the `ephemeralGrants` RETURN section + removed `ephemeralGrants` from the output schema (required + properties).
6. ✅ Step-3 re-source (`internal/processor/step3_auth_capability.go`): refactored `Authorize` to decide path BEFORE the read. Task branch = single GET of `cap.ephemeral.<actor>` via new `ephemeralKeyFromActor`, NO `cap.<actor>` fallback (A1); absent key AND empty-grants doc both → `AuthContextMismatch` (A3). `matchEphemeralGrant` matching logic unchanged (byte-for-byte). service/platform path unchanged (still reads `cap.<actor>`, still threads doc on denial for FR22).
7. ✅ Test + fixture migration (see security-test list below).

**A6 (grantee role):** `CreateTask` granted to `operator` (the established management-create idiom in rbac-domain; staff/operator actor). No CAR filed — operator is the safest, most-privileged seeded grantee; widening later is additive.

**Files changed (`git status`):**
- New: `packages/orchestration-base/{package.go,manifest.yaml,ddls.go,lenses.go,permissions.go,package_test.go,create_task_test.go}`
- M: `cmd/refractor/main.go`, `internal/bootstrap/lenses.go`, `internal/processor/step3_auth_capability.go`, `internal/refractor/capabilityenv/envelope.go`, `docs/components/_packages.md`
- M (tests): `internal/processor/step3_auth_capability_test.go`, `internal/bypass/capadv_cross_target_bleed_test.go`, `internal/refractor/refractor_capability_multi_e2e_test.go`, `internal/refractor/ruleengine/full/bootstrap_e2e_test.go`, `internal/refractor/ruleengine/full/capability_lens_contract_test.go`
- Auto-regenerated by gate runs (test artifacts, not hand-edited): `_bmad-output/implementation-artifacts/gate2-report.txt`, `gate3-report.txt`

**Test count (from diff):** 16 new test functions — 11 in orchestration-base (4 install/CreateTask + 7 package-shape), 3 new step-3 task-path unit tests (single-GET-no-fallback, absent-ephemeral-key, empty-grants-doc), 2 new full-engine tests (`TestCapabilityEphemeralLens_ContractConformance`, `TestCapabilityEphemeralLens_E2E`). Plus migrated existing tests (below).

**Security-test changes (each a faithful re-source, NOT a weakened assertion):**
- `step3_auth_capability_test.go` — the 3 existing task-path tests (TaskPath_Allows/Expired/TargetMismatch) now seed grants under `cap.ephemeral.<actor>` (helper splits the fixture: grants → ephemeral doc, stripped from primary doc). Same envelopes, same expected outcomes. Added 3 new tests asserting the single-GET/no-fallback + absent/empty denial. Faithful: only the grant *container/key* moved; matching assertions identical.
- `capadv_cross_target_bleed_test.go` (Gate 3 vector #4) — alice/bob grants re-sourced from `cap.identity.<actor>` to `cap.ephemeral.<actor>`; the FR56 direct-read now reads the ephemeral key. Cross-target + cross-manager denial assertions (AuthContextMismatch) unchanged → still all DEFENDED. Faithful: key re-source only.
- `bootstrap_e2e_test.go` — removed the bootstrap cypher's task fixtures + ephemeralGrants assertions (the cypher no longer produces them) and added `require.NotContains(ephemeralGrants)`; added `TestCapabilityEphemeralLens_E2E` exercising the SAME behaviors (task1 direct, task2 via reportsTo, taskexpired filtered) link-sourced against the new lens. Faithful: coverage preserved, moved to the new producer.
- `capability_lens_contract_test.go` (§6.6 conformance) — bootstrap conformance now asserts ephemeralGrants is empty (no real task entry); added `TestCapabilityEphemeralLens_ContractConformance` asserting the `cap.ephemeral.<actor>` envelope with link-sourced operationType+target. Faithful: §6.6 conformance moved with the lens per the amendment.
- `refractor_capability_multi_e2e_test.go` — identity C's task fixture migrated field→link (forOperation→op, scopedTo→target); added a third (`capabilityEphemeral`) pipeline; identity C's ephemeral assertion moved to `cap.ephemeral.<C>` with link-sourced field checks. Faithful: same grant, new producer + key.

No auth regression encountered; every gate failure during dev was either a fixture key-shape bug I fixed (invalid NanoID/type segments — `l`/uppercase in test IDs, `leaseApp`→`leaseapp`) or a pre-existing parallel-run flake (confirmed by isolated re-run). No CAR filed.

**Gates (§0.8) — all pass:**
- `go build ./...` ✅
- `make vet` ✅ (0 issues)
- `golangci-lint run ./...` ✅ (0 issues, repo-wide)
- `make verify-kernel` ✅ (ALL ASSERTIONS PASSED — bootstrap cypher change didn't break the kernel)
- `make test-bypass` (Gate 2) ✅ PASSED 4/4 BLOCKED
- `make test-capability-adversarial` (Gate 3) ✅ PASSED 4/4 (3 DEFENDED, 1 ACCEPTED-WINDOW)
- `go test ./internal/processor/... ./internal/bootstrap/... ./packages/orchestration-base/...` ✅ — one flake: `internal/processor/outbox.TestOutbox_NoDoublePublish` failed under full-package parallel run, PASSED on isolated re-run (Deviation 14; unrelated to this story — transactional-outbox dedup test, touches no changed code).
- capability E2E suite (`internal/refractor/`) ✅ — full package green, incl. the migrated multi-identity e2e (cap.ephemeral.<C> link-sourced projection converges).

**Deviations:** (1) Outbox flake under parallel run, re-run green (Dev 14). (2) `cmd/refractor/main.go` is NOT a planning/frozen artifact — edited to wire the new lens (in scope per §0.0 item 4/6). (3) Docs: promoted orchestration-base into the installed-packages list in `docs/components/_packages.md` (allowed surface).

---

## 4. Winston review + CR (inline) — 2026-06-04

Landed in ONE pass (no split). **Drift-detection + adversarial CR: no blockers.**

**Security-plane (highest risk) — cleared.**
- Step-3 refactor (`step3_auth_capability.go`) is correct: path decided from `authContext` BEFORE the read; `authorizeTaskPath` does a SINGLE GET of `cap.ephemeral.<actor>` with NO `cap.<actor>` fallback (A1); absent key AND empty-grants doc both → `AuthContextMismatch` (A3); `matchEphemeralGrant` logic byte-for-byte unchanged.
- Producer/consumer key derivation MATCH: `capabilityenv.ephemeralKey` and `processor.ephemeralKeyFromActor` both yield `cap.ephemeral.identity.<id>`.
- Bootstrap cypher migration (`lenses.go`): both `task` OPTIONAL MATCHes + the `ephemeralGrants` RETURN + the schema required/properties entries dropped; `cap.<actor>` now carries platformPermissions/serviceAccess/roles only. A2 atomic trio lands together.
- `capabilityEphemeral` lens cypher is link-sourced (operationType←forOperation, target←scopedTo, expiresAt←scalar, direct + reportsTo 2-hop, live-only); disjoint `cap.ephemeral.` prefix in the shared `capability-kv` bucket; default hard delete (no deleteMode).
- Gate 3 vector #4 (cross-target bleed) test migration is a faithful re-source to `cap.ephemeral.<actor>` — grant data + all cross-target/cross-manager denial assertions unchanged; only the now-irrelevant Roles/Platform/Service fields dropped.

**CreateTask / DDL — correct.** Requires + validates the assignee identity (alive check → `ScriptError` reject) plus forOperation + scopedTo endpoints; atomic `vtx.task.<id>`(status:open,expiresAt) + three 6-segment links with task=source (Contract #1 §1.1); link sentences read correctly; returns `primaryKey` only (1.5.7). Task DDL = scalars+links, no aspects (A4). No `task.data.grantedOperationType`/`targetKey` field reintroduced.

**Winston cleanups applied:**
- Stripped the 27 newly-added history comments (Story 7.1 / 1.5.12 / "(a1) extraction") to present-tense per CLAUDE.md (delegated to a focused scrub pass; contract/spec refs retained; pre-existing legacy story tags left out of scope). `grep` for new history comments is clean; build/vet/lint green after.
- Reverted gate-report timestamp churn.

**Gates re-run by Winston (docker stack up):** build ✓ · vet ✓ · lint ✓ (0) · verify-kernel ✓ · Gate 2 (test-bypass) ✓ · Gate 3 (test-capability-adversarial) ✓ 4/4 (3 DEFENDED + 1 ACCEPTED-WINDOW; vector #4 DEFENDED) · orchestration-base + processor + bootstrap unit ✓ · refractor capability/ephemeral/bootstrap E2E ✓. Outbox `TestOutbox_NoDoublePublish` flaked once, green on isolated re-run (Dev 14, untouched package). No forbidden edits; planning artifacts + frozen contracts clean.

**Follow-up (out of scope, not blocking):** the Story 1.5.12 commit (`e92bef2`) shipped a handful of `// Story 1.5.12` history comments before CLAUDE.md existed — a small mechanical scrub to spin off separately.

**Verdict: no blockers. Committing to main + watching CI.**
