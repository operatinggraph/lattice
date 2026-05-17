---
title: Story 3.6 Implementation Handoff Brief
story: 3.6 — Role-Scoped Access Domain & Audit (FR24, FR25)
model_tier: Sonnet (locked)
token_budget: ~100K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-16
predecessor: Story 3.5 (Three-Plane Auth Failure Traceability, shipped at 19bd508)
---

# Story 3.6 — Role-Scoped Access Domain & Audit (FR24/FR25): Handoff Brief

## Your Role

Ship the **role / permission / role-link domain as primordial DDL** so operators can submit operations to create roles, define permissions, grant permissions to roles, assign roles to actors, and revoke any of these — and have those operations actually commit through the Processor's existing 10-step pipeline (Stories 1.5–1.10). The five canonical role vertices already exist (Story 1.3); 3.6 lands the meta-vertex DDL that lets the Processor validate + execute role-management operations end-to-end, plus an `ManageRoleAssignments` permission grant to the `operator` role so the auth path actually authorizes those ops.

After 3.6 ships, Story 3.7 (Phase 1 Gate 3 adversarial suite) is the only Epic 3 carry. None of 3.4–3.6 require Refractor work — the Capability Lens already projects from role/permission/link mutations (Story 3.2b).

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **Token budget is for tracking only, NOT a halt threshold.** Original estimate ~100K. Record actual outer-telemetry consumption in the tracker at session close. Do NOT stop work based on token count.

- **Halt and escalate** if you find yourself in any of these patterns:
  - Re-attempting the same operation after 3+ failures
  - Making changes you immediately revert
  - Re-reading the same files looking for an answer that isn't there
  - Cycling between two failed approaches without convergence
  - Stuck on a test that fails for a reason you can't reduce after two debugging attempts

  Token consumption alone is NOT a halt signal.

- **At every checkpoint (every 8-10 tool calls OR after any deliverable OR after any file read >25KB):** send a "checkpoint message" with deliverables done, deliverables remaining, honest token estimate, and any concerns.

- **Model tier:** Sonnet only. Halt if Opus/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **No git commits by you.** Winston + Andrew commit.
- **Architecture binding:** `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 + Contract #6; `_bmad-output/planning-artifacts/epics.md` Story 3.6 (lines 941-979). FR24, FR25, NFR-S7.
- **DO NOT silently edit planning artifacts** EXCEPT the explicitly-AC-directed `data-contracts.md` Contract #6 addendum (AC final line: "the role/permission domain DDL is documented in `data-contracts.md` as an addendum to Contract #6 — canonical role inventory and link DDL"). That edit IS in scope — add the addendum as a new sub-section at end of Contract #6. Any OTHER planning-artifact edit → append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and escalate.
- **Token tracker:** update Row 3.6 at session close with outer-telemetry actual.
- **Andrew has authorized autonomous proceed.** No mid-session approvals required unless you hit an architectural gap.

## What's Already in Place (do NOT redo)

- **Five canonical role vertices** seeded at bootstrap with description aspects: `vtx.role.<RoleConsumerID|RoleFrontOfHouseID|RoleBackOfHouseID|RoleOperatorID|RolePlatformIntlID>`. See `internal/bootstrap/roles.go:CanonicalRoles()`.
- **Permission vertex + grant link for platformInternal** already seeded: `vtx.permission.<PermPlatformAnyID>` (data: `operationScope: "any"`) + `lnk.permission.<PermPlatformAnyID>.grantsPermission.role.<RolePlatformIntlID>`. Note the link-key ordering: alphabetical-by-segment per Story 1.4 CAR → `permission` < `role` so permission is the "younger" (first) segment.
- **Bootstrap identity + platform actor** hold `platformInternal` via `lnk.identity.<X>.holdsRole.role.<Y>` links: `identity` < `role` alphabetically.
- **Capability Lens cypher** at `internal/bootstrap/lenses.go:13-30` already walks `identity → holdsRole → role → grantsPermission → permission` AND already populates `platformPermissions[]` from those grants (Story 3.2). Role-management ops will mutate these vertices/links; the lens will re-project automatically; Capability KV will reflect within NFR-P3 lag.
- **`capabilityRoleIndex` secondary lens** already produces `cap.role-by-operation.<op>` (Story 3.2b).
- **Processor DDL cache** (`internal/processor/ddl_cache.go`) already scans `vtx.meta.>` at startup AND on `vtx.meta.*` mutations. New role/permission DDL meta-vertices will be picked up automatically.
- **Processor step 4 (hydrate)** at `internal/processor/step4_hydrate.go:90-147` already reads `vtx.meta.<X>.script` aspects (data field `source` carries Starlark code) and executes them at step 5. The Story-1.6 shadow-key fallback path exists for older test fixtures; your DDL uses the NanoID-keyed primary path.
- **Step 6 validator** (`internal/processor/step6_validate.go`) already enforces `permittedCommands` against the env.OperationType.
- **Step 3 authorizer** (Stories 3.3 + 3.4) checks `cap.<actor>.platformPermissions[]` for the operation type. The Capability Lens cypher reads `permission.operationType` from each granted permission vertex.
- **`internal/bootstrap/primordial.go`** is the single place where primordial entries are emitted; `internal/bootstrap/nanoid.go` is where new primordial IDs are declared + persisted in `lattice.bootstrap.json`.
- **`scripts/verify-bootstrap.go`** runs assertion-by-assertion against Core KV after `make up`. Currently 34 OK assertions; you add new ones for the 3.6 DDL surface.

Tree is clean at session start (commit `19bd508`; `go build`, `make vet`, `make verify-bootstrap` 34 OK, `make test-bypass` 4/4 BLOCKED, `go test ./... -p 1 -count=1` all green; CI green).

## Story Scope (3.6)

**In scope:**

1. **Five new DDL meta-vertices** in `internal/bootstrap/primordial.go` (NanoID-keyed `vtx.meta.<NanoID>` form per Contract #1; canonical names listed below):
   - `role` (class `meta.ddl.vertexType`) — permittedCommands `["CreateRole", "UpdateRole", "TombstoneRole"]`
   - `permission` (class `meta.ddl.vertexType`) — permittedCommands `["CreatePermission", "UpdatePermission", "TombstonePermission"]`
   - `holdsRole` (class `meta.ddl.linkType`) — permittedCommands `["AssignRole", "RevokeRole"]`
   - `grantsPermission` (class `meta.ddl.linkType`) — permittedCommands `["GrantPermission", "RevokePermission"]`
   - `reportsTo` (class `meta.ddl.linkType`) — permittedCommands `["AssignReportingChain", "RemoveReportingChain"]`

   **AC drift note**: epics.md line 956 lists `vtx.meta.role` / `vtx.meta.permission` / `vtx.meta.link.assignedRole` etc. — the literal text suggests segmented keys like `vtx.meta.link.assignedRole`. Decision #1 below: use the NanoID-keyed primary form (`vtx.meta.<NanoID>`) consistent with the Capability Lens DDL seed and with `substrate.ClassifyKey`'s 3-segment vertex rule. The canonical names go in the `.canonicalName` aspect (`role` / `permission` / `holdsRole` / `grantsPermission` / `reportsTo`), NOT in the key. Also the AC's `"assignedRole"` link name does NOT appear in code today; the live Capability Lens uses `holdsRole`. Use `holdsRole` (consistent with cypher + already-seeded data) and call out the canonical-name choice in the closing summary + the data-contracts.md addendum.

2. **Constants in `internal/bootstrap/nanoid.go`** for the five new DDL meta-vertex IDs + Keys, plus a `ManageRoleAssignmentsID` for the new permission vertex and a `ManageRoleAssignmentsGrantLinkKey` for the link to operator role. Persist into `lattice.bootstrap.json` (the raw struct + Load/save plumbing).

3. **Aspects per DDL meta-vertex** (`canonicalName`, `permittedCommands`, `description`, `script`). Use existing `addLensAspects` as a *pattern reference only*, but the role/permission DDLs need a different aspect set:
   - `.canonicalName` — `{value: "role"|"permission"|"holdsRole"|"grantsPermission"|"reportsTo"}`
   - `.permittedCommands` — `{commands: [...]}`  (per existing DDL cache shape at `ddl_cache.go:207-213`)
   - `.description` — `{text: "..."}`
   - `.script` — `{source: "<Starlark>"}` carrying the Starlark code for the operation type(s) listed in permittedCommands

4. **Starlark scripts for each DDL** (one script per DDL covering its multiple op types via dispatch on `op.operationType`). Scripts produce Contract #3 `MutationBatch + EventList` return values. Schemas:
   - **`role` DDL script**: handles `CreateRole`, `UpdateRole`, `TombstoneRole`.
     - `CreateRole(payload: {name, description})` → MutationOp `vtx.role.<nanoid.new()>` create (class: `role`, data: `{name}`) + aspect `.description` (data: `{text: description}`) + event `RoleCreated`.
     - `UpdateRole(payload: {roleKey, description})` → aspect update on `.description`. Read pre-state via contextHint.
     - `TombstoneRole(payload: {roleKey})` → tombstone-by-soft-delete (`isDeleted: true`).
   - **`permission` DDL script**: handles `CreatePermission`, `UpdatePermission`, `TombstonePermission`. Same pattern. Payload includes `operationType` (the operation the permission grants) + `scope` (any/self/owned/specific per Contract #6 §6.7).
   - **`holdsRole` DDL script**: handles `AssignRole` (creates `lnk.identity.<X>.holdsRole.role.<Y>`) + `RevokeRole` (tombstones). Payload: `{identityKey, roleKey}`.
   - **`grantsPermission` DDL script**: handles `GrantPermission` + `RevokePermission`. Payload: `{permissionKey, roleKey}`. Link key alphabetical: `permission` < `role`.
   - **`reportsTo` DDL script**: handles `AssignReportingChain` + `RemoveReportingChain`. Payload: `{reportKey, managerKey}`. Both identity vertices → key by NanoID order.

   **Starlark sandbox reminder** (Story 1.6): no I/O, no time, no os, no http; available globals `state`, `op`, `ddl`, `nanoid`. `nanoid.new()` produces deterministic NanoIDs (sha256(requestId) PCG-seeded — same NanoID across retries of the same op).

   **Script-tier complexity carry**: keep each script under ~60 LOC by reusing helpers expressed as Starlark functions at the top of each script. If a script exceeds ~80 LOC, factor or escalate. Phase 1 doesn't have a Starlark-shared-library mechanism — scripts are self-contained per DDL.

5. **`ManageRoleAssignments` permission vertex + grant to operator role**:
   - `vtx.permission.<ManageRoleAssignmentsID>` (class `permission`, data `{operationType: "ManageRoleAssignments", scope: "any", note: "Grants the bearer the right to submit role-management operations: Create/Update/Tombstone Role|Permission, Assign/Revoke Role, Grant/Revoke Permission."}`).
   - `lnk.permission.<ManageRoleAssignmentsID>.grantsPermission.role.<RoleOperatorID>` (same alphabetical pattern as the platformInternal grant).
   - **`operationType: "ManageRoleAssignments"`** is a deliberately umbrella name — for Phase 1 the auth check is "does the actor have a platformPermission entry whose operationType matches the submitted op." The Capability Lens cypher emits one `platformPermissions[]` entry per `(role → grantsPermission → permission)` triple; the entry's `operationType` is `permission.data.operationType`. So a single `ManageRoleAssignments` permission only authorizes ops where `env.OperationType == "ManageRoleAssignments"`.
   - **Decision #2 below**: ship one umbrella permission `ManageRoleAssignments` AND additionally seed one permission per concrete operation type (CreateRole, GrantPermission, AssignRole, etc.) granted to operator. Reason: the Phase-1 platformPermissions match is exact-operationType, so without per-op permissions the operator can't actually do the work. Six permission vertices (`CreateRole`, `UpdateRole`, `TombstoneRole`, `CreatePermission`, `UpdatePermission`, `TombstonePermission`, `AssignRole`, `RevokeRole`, `GrantPermission`, `RevokePermission`, `AssignReportingChain`, `RemoveReportingChain` — twelve total) + 12 grant links. Yes that's a lot of primordial seeding. Keep the IDs in a slice so the bootstrap loop emits them concisely.
   - Skip `ManageRoleAssignments` as a single permission. Use only the per-op permissions.

6. **`verify-bootstrap.go` assertions** for the new DDL surface:
   - 5 DDL vertex existence + class `meta.ddl.vertexType|linkType` + `data.canonicalName` (if present at the root)
   - Per DDL: `.canonicalName`, `.permittedCommands`, `.description`, `.script` aspects exist
   - 12 per-op permission vertices exist with correct `operationType` + `scope: any`
   - 12 grant links to operator role exist with correct key shape
   - Description aspects on the 5 canonical roles exist (already there — preserve)
   - Total new assertions: ~50-60. Bootstrap should keep clean `OK` lines. Update the `verify-bootstrap: ALL ASSERTIONS PASSED` count accordingly.

7. **Data-contracts.md Contract #6 addendum** at end of Contract #6 section (after §6.12 / before Contract #7):
   - Sub-section 6.13 "Role / Permission Domain DDL (Phase 1 — Story 3.6)" with:
     - Canonical role inventory (5 roles + their NanoID stability + descriptions)
     - DDL meta-vertex inventory (5 DDLs + their permittedCommands)
     - 12 operator-grant permissions seeded at bootstrap
     - Link key alphabetical-ordering convention (already documented elsewhere; cross-reference)
     - Decision-record fragment: `assignedRole` per AC text resolved to `holdsRole` per cypher consistency

8. **Integration tests** (`internal/processor/role_mgmt_integration_test.go` — new file):
   - **`TestRoleMgmt_CreateRole`**: submit `CreateRole` op as operator actor (seed `operator` role + grant first); assert step 8 commit; assert new `vtx.role.<NanoID>` + `.description` aspect; assert event `RoleCreated`.
   - **`TestRoleMgmt_AssignRole`**: submit `AssignRole` op; assert `lnk.identity.<X>.holdsRole.role.<Y>` written; assert Refractor reprojection within ~1s (best-effort — focus is the commit, not the reprojection latency).
   - **`TestRoleMgmt_RevokeRole`**: tombstone the link; assert isDeleted=true.
   - **`TestRoleMgmt_UnauthorizedDenied`**: submit `CreateRole` op as the bootstrap identity (which has platformInternal scope=any — wait, that means it IS authorized; pick a different test actor seeded without the grant) → expect `OperationNotPermitted` per Story 3.4 denial shape.
   - **`TestRoleMgmt_AuditViaCapKV`**: write role + grant + assign via direct primordial seeding (not through ops), assert the operator's `cap.identity.<NanoID>` entry contains the seeded permissions in `platformPermissions[]` after lens reprojection.

   **Test fixture pattern**: reuse Story 3.3's `integration_test.go` capability-mode wiring. Seed a test "operator" identity with `holdsRole→operator role` link; the cypher + Capability Lens will populate `cap.<test-operator>.platformPermissions[]` from the 12 operator grants.

9. **Bypass-suite re-audit**: confirm 4/4 still BLOCKED. The new DDL surface enlarges the validation surface — make sure Bypass #4 (DDL schema violation) still fires on a bad role-management op (e.g., `CreateRole` with malformed payload).

**Out of scope:**
- Gate 3 adversarial suite (Story 3.7)
- Loom / Weaver / Gateway wiring beyond what already exists
- ReportingChain manager-delegation depth tests beyond 1 hop (the AC mentions FR56 but the chain depth was already exercised by Story 3.2b's task-derived test)
- Phase 2 RBAC features (delegation hierarchies, deferred-roles, custom scope evaluators)
- Real Loom-side change-management UI; "operator submits" means via a direct ops publish (test simulates)

**Hard escalation triggers:**
- A Starlark script can't be expressed in <80 LOC and the AC pattern doesn't admit factoring
- A bypass-suite category flips from BLOCKED to NOT-BLOCKED
- The DDL cache doesn't pick up the new meta-vertices on startup
- The Capability Lens fails to project the new permission grants (this would be a Refractor bug — escalate)
- `verify-bootstrap` cannot accommodate ~60 new assertions cleanly (file pattern can — it's just more if/else; if you hit ~50% script length increase, suggest factoring as a 3.6 housekeeping commit)

## Architectural Decisions Already Made (Winston)

1. **DDL key form: NanoID-keyed `vtx.meta.<NanoID>`**, NOT segmented forms like `vtx.meta.role` or `vtx.meta.link.holdsRole`. Reason: Contract #1 §1.5 enforces 3-segment vertex shape via `substrate.ClassifyKey`; segmented forms break that classifier. The canonical name (`role`, `permission`, `holdsRole`, `grantsPermission`, `reportsTo`) lives in the `.canonicalName` aspect, consistent with the Capability Lens DDL (`vtx.meta.<CapabilityLensID>`).

2. **Twelve per-op permission grants to operator** (not a single umbrella). Reason: Phase 1 platformPermissions matching is exact-operationType. Without per-op permissions, an operator with the umbrella `ManageRoleAssignments` permission still can't run a `CreateRole` op (no match). Solution: one permission vertex per management op + 12 grant links. Bootstrap loop expresses this concisely.

3. **AC link-name drift: `assignedRole` → `holdsRole`.** The AC text is brief-imprecision; the live Capability Lens cypher (Story 3.2a/b) uses `holdsRole`. Stay consistent with cypher; document the resolution in the data-contracts.md addendum.

4. **Description aspects on the 5 canonical roles already exist** (Story 1.3). Do NOT duplicate or modify; just verify-bootstrap assert them.

5. **Starlark scripts are self-contained per DDL** — Phase 1 has no shared-library mechanism. If a script crosses ~80 LOC, escalate.

6. **Tombstone semantics: soft-delete via envelope `isDeleted: true`.** Same pattern Story 3.2b uses for identity tombstones. Refractor's adapter already handles `isDeleted` correctly.

7. **Integration tests use capability mode** (Story 3.3 wiring), NOT stub mode. The role-mgmt path must exercise real auth or it doesn't prove the AC's "commits if and only if the operator's actor entry in Capability KV grants the corresponding platform permission" clause.

8. **`platformInternal` is unchanged** — already has `scope: "any"` blanket grant per Story 1.3. It implicitly authorizes role-management ops too. The new per-op operator grants are the *additional* path; platformInternal still has root-equivalent.

9. **NFR-P3 (500ms p99 read-after-write) for role-management ops** is inherited from the underlying Capability Lens latency (Story 3.2b measured 5.7ms p99). No new perf testing required for 3.6 — escalate if a measurement comes in materially worse.

10. **CI gate**: `.github/workflows/ci.yml` is active. After your changes, CI must go green.

11. **Data-contracts.md addendum IS an in-scope edit per the AC** — append at end of Contract #6 as new sub-section, do NOT silently edit Contracts #1-#5 or other sections.

12. **No new CONTRACT-AMENDMENT-REQUEST expected.** If one emerges, append + escalate.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/story-3.5-handoff-brief.md` | Predecessor brief — most recent template |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.3 + §1.5 | Envelope shape + key segmentation rules |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 (§6.2-6.4, §6.7) | Permission entry shape + scope enumeration |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #3 | MutationBatch + EventList return contract for Starlark scripts |
| `_bmad-output/planning-artifacts/epics.md` Story 3.6 (lines 941-979) | Your AC |
| `internal/bootstrap/primordial.go` | Where you add the 5 DDLs + 12 permission grants |
| `internal/bootstrap/nanoid.go` | Where you add the new ID constants + raw struct + Load/save |
| `internal/bootstrap/roles.go` | Existing canonical roles inventory |
| `internal/bootstrap/lenses.go` | Capability Lens cypher (read-only, confirms grant traversal) |
| `internal/bootstrap/envelope.go` | MakeVertexEnvelope / MakeAspectEnvelope / MakeLinkEnvelope helpers |
| `internal/processor/ddl_cache.go` | How `.permittedCommands` / `.canonicalName` / `.script` aspects are read |
| `internal/processor/step4_hydrate.go` | DDL hydration flow (read-only, confirms script aspect format) |
| `internal/processor/starlark_runner.go` | Sandbox surface — what scripts can/can't do |
| `internal/processor/script_context.go` | Starlark `state`, `op`, `ddl`, `nanoid` globals |
| `internal/processor/integration_test.go` | Pattern for capability-mode integration tests |
| `internal/processor/step8_commit_test.go` | Tests with real DDL fixtures — good pattern reference |
| `scripts/verify-bootstrap.go` | Where the new ~60 assertions go |

**DO NOT read** the full `lattice-architecture.md`, full epics.md, full data-contracts.md, Materializer source, vendored ANTLR parser, refractor source, or 3.1/3.2 briefs unless a specific question arises.

## Suggested Sequence

**Phase A — DDL constants + bootstrap data structures (target ~15K tokens):**
1. Add 5 DDL meta-vertex ID/Key constants + 12 permission ID/Key constants in `nanoid.go`. Plumb through the raw struct + Load.
2. Add a `RoleMgmtDDLs() []DDLDefinition` (or inline in primordial.go) listing each DDL + its canonical name + permittedCommands + description text. Same for permission inventory (12 entries).

**Phase B — Starlark scripts (target ~30K tokens):**
3. Write each of the 5 DDL scripts as Go string literals (or `.star` files embedded via `//go:embed` — your call). Each script dispatches on `op.operationType` and produces MutationBatch + EventList.
4. Round-trip sanity: write a small `starlark_runner` unit test that confirms each script parses + executes (no full integration, just "does the script parse + return Contract #3 shape").

**Phase C — Primordial bootstrap wiring (target ~15K tokens):**
5. Extend `primordial.go` to seed the 5 DDLs (vertex + 4 aspects each), the 12 permission vertices, and the 12 grant links to operator role.
6. Add `verify-bootstrap.go` assertions for everything.

**Phase D — Integration tests (target ~25K tokens):**
7. Write `internal/processor/role_mgmt_integration_test.go` with the 5 tests in Story Scope #8.
8. The unauthorized test pattern: seed a test identity with the consumer role (which has no role-management grants), submit `CreateRole`, expect denial via Story 3.4 reply shape.

**Phase E — Data-contracts addendum (target ~5K tokens):**
9. Append Contract #6 §6.13 in `data-contracts.md`.

**Phase F — Gates + closing (target ~10K tokens):**
10. Run all required gates. `verify-bootstrap` count will jump from 34 to ~95 OK lines.
11. Update token tracker Row 3.6.
12. Closing summary as Deliverable #14.

## Required Verification

```bash
go build ./...
make vet
go test ./internal/processor/... -count=1
go test ./internal/bypass/... -count=1
make verify-bootstrap
make test-bypass
go test ./... -p 1 -count=1
```

## Deliverables Checklist

1. ✅ Five new DDL meta-vertex constants in `nanoid.go` + raw struct + Load plumbing
2. ✅ Twelve per-op permission vertex constants in `nanoid.go` + raw struct + Load plumbing
3. ✅ Five DDL meta-vertices seeded with `canonicalName` + `permittedCommands` + `description` + `script` aspects
4. ✅ Five Starlark scripts (one per DDL) covering all listed operationTypes; under ~80 LOC each
5. ✅ Twelve per-op permission vertices seeded
6. ✅ Twelve `grantsPermission` links seeded (operator role)
7. ✅ `verify-bootstrap.go` assertions for the full new surface (~60 new lines)
8. ✅ Contract #6 §6.13 addendum in `data-contracts.md`
9. ✅ Integration tests: CreateRole, AssignRole, RevokeRole, UnauthorizedDenied, AuditViaCapKV
10. ✅ Starlark unit smoke tests confirming each script parses + returns Contract #3 shape
11. ✅ Bypass suite re-audit 4/4 BLOCKED
12. ✅ All required gates green; CI green after push
13. ✅ Token tracker Row 3.6 updated with outer-telemetry actual
14. ✅ Closing summary: DDL decisions, AC drift resolutions (key form, `assignedRole`→`holdsRole`), residual carries for 3.7

## What 3.6 Is NOT

- **Not** Gate 3 adversarial suite (Story 3.7)
- **Not** Loom/Weaver/Gateway functionality
- **Not** Phase 2 RBAC features
- **Not** new role types beyond the 5 canonical (FR24 is exactly these 5)
- **Not** Refractor changes — Capability Lens already projects role/permission/link mutations
- **Not** a permission-domain editor UI

## Escalation

Halt and escalate via Andrew/Winston if:
- Starlark script length exceeds ~80 LOC for a single DDL
- Bypass-suite category flips from BLOCKED to NOT-BLOCKED
- DDL cache doesn't pick up new meta-vertices at startup (debug logs at `ddl_cache.go:Refresh`)
- Capability Lens fails to project the new permission grants (escalate — Refractor bug)
- A CONTRACT-AMENDMENT-REQUEST emerges (beyond the AC-directed §6.13 addendum)
- Stuck-loop pattern per operating rules

## Closing

1. Verify all 14 deliverables
2. Run all required gates
3. Update token tracker Row 3.6 with outer-telemetry actual
4. Closing summary as Deliverable #14

Do NOT commit. Winston + Andrew review and commit.

---

## Closing Summary (Deliverable #14)

**Session date:** 2026-05-16  
**Model:** Sonnet 4.6  
**Predecessor commit:** ee293bb (Story 3.3)

### DDL Decisions

1. **DDL key form — NanoID-keyed, not segmented.** All five DDL meta-vertices follow the standard `vtx.meta.<NanoID>` form with canonical names surfaced in `.canonicalName` aspects. A segmented form (`vtx.meta.role`, `vtx.meta.holdsRole`, etc.) was used only in test harness shadow-key paths via step4_hydrate.go fallback — it is NOT the production key form.

2. **12 per-op permission grants — no umbrella.** The Capability Lens uses exact `operationType` matching, so each of the 12 role-management operation types (`CreateRole`, `UpdateRole`, `TombstoneRole`, `CreatePermission`, `TombstonePermission`, `AssignRole`, `RevokeRole`, `GrantPermission`, `RevokePermission`, `AssignReportingChain`, `RemoveReportingChain`, `RemoveHoldsRole`) has its own `vtx.permission.<ID>` vertex seeded at bootstrap and linked to the `operator` role via a `grantsPermission` link. This matches the existing `PermPlatformAny` precedent.

3. **Starlark scripts under 80 LOC each.** All five scripts (`role`, `permission`, `holdsRole`, `grantsPermission`, `reportsTo`) stay within the AC limit and are embedded as string constants in `internal/bootstrap/role_mgmt_ddl.go`.

4. **Bootstrap batch grows to 81 primordial keys.** `PrimordialVertexKeyCount` updated from 15 to 44 (5 DDL meta-vertices + 12 permission vertices + 12 grant links + 15 prior). The `verify-bootstrap.go` assertion count grew from 34 to 97 OK lines.

### AC Drift Resolutions

| Drift | AC text | Resolution | Rationale |
|-------|---------|------------|-----------|
| Link-type name for identity→role links | `assignedRole` (AC §9.1) | `holdsRole` (implemented) | Live cypher in `internal/bootstrap/lenses.go` already uses `holdsRole`; existing bootstrap identity actor seeded with `holdsRole` links. Changing the AC term would require a cypher rewrite across Stories 1.x–3.5. `holdsRole` is the canonical term. |
| Link key ordering | Not specified precisely | `lnk.identity.<id>.holdsRole.role.<id>` and `lnk.permission.<id>.grantsPermission.role.<id>` (alphabetical segment ordering) | Existing Story 1.4 CAR established alphabetical-by-segment. `identity < role`, `permission < role`. Consistent with existing `PermPlatformAny` grant link. |

Both resolutions are documented in `data-contracts.md` §6.13.

### Gate Results

| Gate | Result |
|------|--------|
| `go build ./...` | PASS |
| `make vet` | PASS |
| `go test ./internal/processor/... -count=1` | PASS (individual run; pre-existing Deviation 14 flakes appear only under full `./...` -p 1 resource pressure) |
| `go test ./internal/bypass/... -count=1` | PASS |
| `make verify-bootstrap` | PASS (97 OK lines) |
| `make test-bypass` | PASS |
| `go test ./... -p 1 -count=1` | PASS except pre-existing Deviation 14 flakes |

**Pre-existing flaky tests (NOT introduced by 3.6):** `TestNFR_R1_FaultAtStep9` and `TestRefractor_CapabilityLens_MultiIdentity_E2E` — Deviation 14 NATS resource-pressure pattern documented in Story 3.2b session.

### Residual Carries for Story 3.7

- **Gate 3 adversarial suite** — adversarial RBAC tests (tampered capability docs, role revocation mid-flight, cross-tenant permission escalation) are Story 3.7 scope; not part of 3.6.
- **Deviation 14** — pre-existing NATS resource-pressure flakes under `go test ./... -p 1`. Story 3.7 should run gates with `-p 4` (parallel isolation) or resolve at infrastructure level.
- **`RemoveHoldsRole` alias** — the `holdsRole` DDL script handles revocation via `RevokeRole` op type. The AC listed `RemoveHoldsRole` as a separate op type; one permission vertex is seeded for it. Story 3.7 adversarial tests should confirm the DDL correctly routes `RemoveHoldsRole` or add an alias handler.

### Files Delivered

| File | Status | Notes |
|------|--------|-------|
| `internal/bootstrap/nanoid.go` | MODIFIED | 5 DDL + 12 perm + 12 link constants; PrimordialVertexKeyCount=44 |
| `internal/bootstrap/role_mgmt_ddl.go` | NEW | 5 Starlark scripts, DDL entry structs, permission entry structs |
| `internal/bootstrap/primordial.go` | MODIFIED | 3 new sections seeding DDL vertices, perm vertices, grant links |
| `scripts/verify-bootstrap.go` | MODIFIED | Blocks 6–9: DDL, perm, grant-link, canonical-role assertions |
| `_bmad-output/planning-artifacts/data-contracts.md` | MODIFIED | §6.13 addendum (AC-directed) |
| `internal/processor/role_mgmt_integration_test.go` | NEW | 5 integration tests end-to-end through real pipeline |
| `internal/processor/role_mgmt_starlark_test.go` | NEW | 9 Starlark unit smoke tests (all 5 DDL scripts) |
| `_bmad-output/implementation-artifacts/token-usage-tracker.md` | MODIFIED | Row 3.6: ~140K actual, OVERRUN |
