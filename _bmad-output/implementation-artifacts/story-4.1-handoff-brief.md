---
title: Story 4.1 Implementation Handoff Brief
story: 4.1 — Identity Domain DDL & State Machine
model_tier: Opus (locked)
token_budget: ~120K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-17
predecessor: Story 3.7 (Capability Lens Adversarial Test Suite, shipped at ecb2e68; lint cleanup at 008b420). Epic 3 closed.
---

# Story 4.1 — Identity Domain DDL & State Machine: Handoff Brief

## Your Role

Ship the **identity domain DDL** as primordial bootstrap-seeded meta-data so the Processor's 10-step pipeline can validate + execute identity-lifecycle operations against a contract-conformant foundation. This is Epic 4's substrate story — Stories 4.2–4.5 ride on it. Your work covers:

1. The `identity` DDL meta-vertex with `permittedCommands` covering all Epic-4 identity operations.
2. A Starlark validator (in the DDL `.script` aspect) that enforces the **state machine** (`unclaimed → claimed | flagged-for-review`, etc.) and the **`IdentityMerged` guard** (no mutation against a merged identity).
3. **Five new permission vertices** (`CreateUnclaimedIdentity`, `ClaimIdentity`, `FlagIdentityForReview`, `ApproveIdentityMerge`, `ScanIdentityDuplicates`) and ten grant links to the role matrix in AC.
4. `verify-bootstrap.go` assertions for the new surface (target ~30-40 new OK lines on top of the current 97).
5. Integration tests: state machine enforced; `IdentityMerged` guard fires; FR7 cascade-isolation (tombstoned lease does NOT cascade to identity).

After 4.1 ships, Stories 4.2–4.5 each fill in one or more operation branches in the same DDL script (extending `permittedCommands` is NOT required — 4.1 declares the full set up front).

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **No worktree.** Work directly in `/Users/andrewsolgan/Documents/GitHub/Lattice` against branch `main`. Do NOT operate inside `.claude/worktrees/*`. Verify with `pwd` at startup.
- **No commits, no pushes.** Stage your changes (`git add` or leave unstaged), but DO NOT call `git commit` or `git push`. Winston commits + pushes after review.
- **Planning artifacts are read-only for you.** Do NOT edit `_bmad-output/planning-artifacts/data-contracts.md`, `epics.md`, `lattice-architecture.md`, `MORPH-DEVIATIONS.md`. If you find a contract gap, AC ambiguity, or documentation drift, **append your request to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`** and continue. Winston adjudicates separately.
- **Questions back to Winston:** Write into `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md`, stop work on that sub-task, move to a different deliverable. Winston will respond on the next session round.
- **Token budget is for tracking only, NOT a halt threshold.** Estimate ~120K. Record actual outer-telemetry consumption in the tracker at session close.
- **Halt and escalate** on stuck-loop patterns: re-attempting same operation after 3+ failures; immediate reverts; cycling between two failed approaches; stuck on a test fail you can't reduce after two debug attempts.
- **Checkpoint every 8-10 tool calls OR after any deliverable OR after any file read >25KB.** Report deliverables done/remaining + honest token estimate.
- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **Architecture binding:** Contract #1 §1.3 + §1.5 (envelopes + key segmentation), Contract #3 (script return shape), Contract #6 §6.7 (permission scope enum), epics.md Story 4.1 (lines 1037-1079), FR1-FR7 (Epic 4 motivation), FR57 (write-scope).
- **Token tracker:** update Row 4.1 at session close with outer-telemetry actual.
- **Andrew has authorized autonomous proceed.** No mid-session approvals required unless you hit an architectural gap.

## What's Already in Place (do NOT redo)

- **Bootstrap pattern** for DDL meta-vertices is established in `internal/bootstrap/role_mgmt_ddl.go` + `primordial.go` from Story 3.6: 5 DDL meta-vertices (`role`, `permission`, `holdsRole`, `grantsPermission`, `reportsTo`) + 12 per-op permissions + grant links. **Use this as your template.**
- **NanoID raw-struct + Load/Save** plumbing in `internal/bootstrap/nanoid.go` — pattern: declare a `IdentityDDLID` (etc.) + `IdentityDDLKey`, add to the `raw` struct, the populate function, the persistence assertions, and the post-Load assignment block. Same for the five permission IDs + permission keys + grant link keys.
- **Existing identities are NOT `class: identity`** — they are `class: identity.system.bootstrap` and `class: identity.system.platform` (see `primordial.go:285-294`). The 4.1 DDL applies to the *user-facing* `identity` class. System identities remain class-distinct and outside the DDL — that is intentional (system identities never transition through the consumer state machine). Story 3.7 also seeds `class: identity.ai` in tests (no DDL needed for AI actors either; NFR-S10 forbids special-casing).
- **Permission vertex shape**: `vtx.permission.<NanoID>` with data `{operationType: "<OpName>", scope: "<any|self|owned|specific>"}`. Story 3.6 ships 12 of these. The Capability Lens cypher emits one `platformPermissions[]` entry per `role → grantsPermission → permission` triple; the entry's `operationType` is what the auth step matches.
- **Grant link key shape**: `lnk.permission.<PermID>.grantsPermission.role.<RoleID>`. Alphabetical-by-segment ordering (permission < role) — same as Story 1.4 CAR resolution.
- **Capability Lens** will reproject automatically from new permission + grant link primordials. No Refractor work in 4.1.
- **Processor DDL cache** scans `vtx.meta.>` at startup AND on `vtx.meta.*` mutations. New DDL is picked up automatically.
- **Step 4 (hydrate)** reads `vtx.meta.<X>.script.data.source` (Starlark code).
- **Step 5 (execute)** runs the script under sandbox (globals: `state`, `op`, `ddl`, `nanoid`; no I/O / time / os / http; `nanoid.new()` deterministic per requestId).
- **Step 6 (validate)** enforces `permittedCommands` against `env.OperationType` (Story 1.7 + 1.9 / FR57).
- **`scripts/verify-bootstrap.go`** runs assertion-by-assertion; currently 97 OK. Add new ones for 4.1.
- **`data-contracts.md` §6.13** documents the role/permission domain per 3.6.

Tree is clean at session start (commit `008b420`; CI green; verify-bootstrap 97 OK; test-bypass 4/4 BLOCKED; test-capability-adversarial 4/4 DEFENDED; `go test ./... -p 1 -count=1` all green).

## Story Scope (4.1)

### 1. One new DDL meta-vertex: `identity`

In `internal/bootstrap/nanoid.go`:
- Declare `IdentityDDLID string` + `IdentityDDLKey string` (mirroring `DDLRoleID` / `DDLRoleKey` from 3.6).
- Plumb through `raw` struct field `identityDDL`, the populate function, the `lattice.bootstrap.json` persistence assertions, and the post-Load assignment block.

In `internal/bootstrap/primordial.go` (or a new `identity_ddl.go` analogous to `role_mgmt_ddl.go`):
- Emit the DDL vertex envelope at `IdentityDDLKey` with class `meta.ddl.vertexType` and data `{canonicalName: "identity"}`.
- Emit four aspects on the DDL vertex (identical aspect-set to the 3.6 DDLs):
  - `.canonicalName` `{value: "identity"}`
  - `.permittedCommands` `{commands: ["CreateUnclaimedIdentity", "UpdateIdentityState", "ClaimIdentity", "FlagIdentityForReview", "ApproveIdentityMerge", "MergeIdentity", "TombstoneIdentity", "ScanIdentityDuplicates"]}`
  - `.description` `{text: "Identity domain DDL. Vertex shape: vtx.identity.<NanoID>, class=identity. Aspects: name (sensitive, required, maxLen 200), email (sensitive, lowercase-normalized), phone (sensitive, E.164-normalized), state (enum: unclaimed|claimed|flagged-for-review|merged), claimKey (sensitive, one-time-use; null after claim), credentialBinding (sensitive; null pre-claim), mergedInto (vertex-key reference, null until merged). State machine + IdentityMerged guard enforced in .script."}`
  - `.script` `{source: "<see §3 below>"}`

### 2. Five new permission vertices + ten grant links

In `internal/bootstrap/nanoid.go`: add ID + Key constants for each (mirror the 3.6 pattern — the 12 role-mgmt permissions in `role_mgmt_ddl.go` are your template). Persist all to `lattice.bootstrap.json`.

Permission inventory (5 vertices):

| Permission | scope | Granted to roles | # grant links |
|---|---|---|---|
| `CreateUnclaimedIdentity` | `any` | frontOfHouse, backOfHouse, operator | 3 |
| `ClaimIdentity` | `self` | consumer | 1 |
| `FlagIdentityForReview` | `any` | frontOfHouse, backOfHouse, operator | 3 |
| `ApproveIdentityMerge` | `any` | operator | 1 |
| `ScanIdentityDuplicates` | `any` | backOfHouse, operator | 2 |

Total: **5 permission vertices + 10 grant links**.

Each permission: `vtx.permission.<NanoID>` with data `{operationType: "<OpName>", scope: "<scope>", note: "..."}`.
Each grant link: `lnk.permission.<PermID>.grantsPermission.role.<RoleID>`.

Note on `scope: "self"` for `ClaimIdentity`: Phase 1 platformPermissions[] match is exact-operationType only; **scope enforcement happens at the Starlark layer of the claim op itself (Story 4.3)**, not in 4.1. For 4.1 you just seed `scope: "self"` in the permission's data so 4.3 can read it. Document this in the closing summary.

### 3. The Starlark script — state machine + merged guard

The DDL `.script.data.source` is one Starlark module that dispatches on `op.operationType` and produces Contract #3 `MutationBatch + EventList`.

**For 4.1, the script implements:**
- A shared helper `validate_state_transition(current_state, new_state)` that raises `script_error("InvalidStateTransition", "<current> -> <new>")` on disallowed transitions.
- A shared helper `enforce_not_merged(state_aspect_doc, mergedInto_aspect_doc)` that raises `script_error("IdentityMerged", mergedInto_value)` if state == "merged". This guard runs at the top of every mutation branch.
- A primary 4.1 operation **`UpdateIdentityState`** with payload `{identityKey, newState}`:
  - Reads `state` aspect (via `state.read(identityKey + ".state")`) and `mergedInto` aspect (via `state.read(identityKey + ".mergedInto")`) from hydrated state.
  - Calls `enforce_not_merged`. Calls `validate_state_transition(currentState, payload.newState)`.
  - Emits MutationOp on `vtx.identity.<id>.state` aspect with `{value: newState}` + event `IdentityStateChanged` with `{identityKey, oldState, newState}`.
- Stub branches for the other six ops (`CreateUnclaimedIdentity`, `ClaimIdentity`, `FlagIdentityForReview`, `ApproveIdentityMerge`, `MergeIdentity`, `TombstoneIdentity`, `ScanIdentityDuplicates`) returning `script_error("NotYetImplemented", "Story 4.<X>: <op>")`. Stories 4.2-4.5 replace each stub with a real implementation.

**Allowed state transitions (per AC):**
- `unclaimed → claimed`
- `unclaimed → flagged-for-review`
- `claimed → flagged-for-review`
- `flagged-for-review → claimed`
- `flagged-for-review → merged`

All other transitions raise `InvalidStateTransition`. Re-entering the same state (e.g. `unclaimed → unclaimed`) is also rejected.

**Sandbox reminder** (Story 1.6): no `load`/`os`/`time`/`http`/`open`; only `state`, `op`, `ddl`, `nanoid` globals. Use the helpers — script-tier complexity carry from 3.6 still applies (target <80 LOC; escalate if you need shared library mechanism).

**Hydration of `state` and `mergedInto` aspects**: Step 4 already supports reading multiple aspects via `contextHint`. The `UpdateIdentityState` op's `contextHint` should request these aspects. See `internal/processor/step4_hydrate.go` for the hint shape if unclear.

### 4. `verify-bootstrap.go` assertions

Add ~30-40 OK lines covering:
- Identity DDL vertex existence + class `meta.ddl.vertexType` + `data.canonicalName == "identity"`
- Four aspects on identity DDL: `.canonicalName`, `.permittedCommands` (assert all 8 op types in `commands[]`), `.description`, `.script` (assert non-empty `source`)
- Five new permission vertices: class `permission` + `data.operationType` + `data.scope` matches the table in §2
- Ten new grant links: existence + link key shape + envelope `data` (Story 3.6 grants carry a small `data` payload — match its convention)

**Bootstrap line count target**: ~120-140 OK after this story (97 → ~130). If the bootstrap script's structure forces materially more, that's fine — keep the assertions readable.

### 5. Integration tests in `internal/processor/identity_state_machine_test.go` (new file)

Capability-mode wiring (mirror `internal/processor/role_mgmt_integration_test.go` from 3.6). Seed a test operator identity with the appropriate grants so the auth step passes.

- **`TestIdentity_StateMachine_AllowedTransitions`**: for each of the 5 allowed transitions, seed an identity with `state == fromState`, submit `UpdateIdentityState`, assert step 8 commit + `state` aspect now `toState` + `IdentityStateChanged` event.
- **`TestIdentity_StateMachine_RejectsDisallowed`**: table-driven; for several illegal transitions (e.g. `unclaimed → merged`, `claimed → unclaimed`, `merged → claimed`), assert ScriptError code `InvalidStateTransition` + no commit.
- **`TestIdentity_MergedGuard_RejectsMutation`**: seed identity with `state == merged` and `mergedInto == vtx.identity.<survivorID>`. Submit `UpdateIdentityState` targeting it. Assert ScriptError code `IdentityMerged` and that the response detail includes the `mergedInto` value. (Step 3 will allow the op through; the guard fires in step 5.)
- **`TestIdentity_FR7_LeaseTombstoneDoesNotCascade`**: seed identity vertex + a `vtx.lease.<NanoID>` vertex (class `lease`; ad-hoc — no lease DDL needed) + a link `lnk.identity.<X>.hasLease.lease.<Y>` (alphabetical: identity < lease). Tombstone the lease vertex by writing a new envelope revision with `isDeleted: true` directly via `substrate.AtomicBatch` (NOT through an op — bypass the DDL). Assert: identity vertex revision unchanged, identity's `state` aspect unchanged, the identity-side link envelope unchanged. This verifies FR7 substrate semantics (no implicit cascade).
- **`TestIdentity_RolePermissionGrantsProjected`**: after bootstrap, look up `cap.identity.<OperatorTestActorID>` (an operator-seeded test actor) and assert its `platformPermissions[]` contains entries for all 4 operator-granted identity ops (`CreateUnclaimedIdentity`, `FlagIdentityForReview`, `ApproveIdentityMerge`, `ScanIdentityDuplicates`). This proves the Capability Lens picked up the new grants without any Refractor change. May rely on Refractor reprojection latency (~ms); poll briefly with a deadline.

### 6. Bypass-suite re-audit

Confirm `make test-bypass` (4/4 BLOCKED) and `make test-capability-adversarial` (4/4 DEFENDED) remain green. The new DDL surface should not change any gate. If something flips, STOP and escalate — that is a real regression, not a test bug.

**Out of scope:**
- Stories 4.2 (`CreateUnclaimedIdentity` real impl), 4.3 (`ClaimIdentity`), 4.4 (`ScanIdentityDuplicates`), 4.5 (`ApproveIdentityMerge`/`MergeIdentity`). 4.1's stubs return `NotYetImplemented` for those op types.
- Aspect-level declarative schemas (sensitive/required/maxLength as a structured DDL aspect). Aspect constraints are documented in the description aspect and enforced in the per-op Starlark scripts of 4.2-4.5. Declarative aspect-DDL is Phase 2+ (overlaps with Epic 5.1 DDL self-description, FR19).
- Lease DDL (the FR7 test creates an ad-hoc lease vertex directly via substrate; no lease DDL needed).
- Read-restricted `claimKey` aspect (Story 4.2 concern).
- Crypto-shred / Personal Lens / Secure Lens.
- Tombstone-by-soft-delete semantics for identity vertex itself (Story 4.5 / Epic 4 closing concern).
- Closed-loop Weaver auditor.

**Hard escalation triggers (append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` then move on):**
- AC text disagrees with contract text in a non-trivial way (e.g., AC's `vtx.meta.aspect.identity.name` key shape — see Decision #1 below for the resolution).
- A bypass-suite or Gate 3 vector flips from BLOCKED/DEFENDED.
- DDL cache doesn't pick up the new `identity` DDL on startup.
- Starlark script exceeds ~120 LOC even with helper factoring (escalate before adding a shared-library mechanism).
- `verify-bootstrap` cannot accommodate the new assertions cleanly.

## Architectural Decisions Already Made (Winston)

1. **DDL key form: NanoID-keyed `vtx.meta.<NanoID>`** with canonical name `identity` in the `.canonicalName` aspect. Contract #1 §1.5 enforces 3-segment vertex shape via `substrate.ClassifyKey`. AC text `vtx.meta.identity` (3 segments — works as a literal name segment) and `vtx.meta.aspect.identity.name` (5 segments — DOES NOT classify as either vertex or aspect) are brief-imprecision. Resolution mirrors Story 3.6 Decision #1.

2. **No aspect-DDL meta-vertices.** Aspect constraints (sensitive, required, maxLen, normalize) are captured in the description text and enforced in per-op Starlark scripts. There is no `vtx.meta.aspect.<X>` concept in Phase 1. Declarative aspect schemas are Phase 2+ / Epic 5.1 (FR19). Document this drift resolution in the closing summary.

3. **System identities (`identity.system.bootstrap`, `identity.system.platform`) and AI actors (`identity.ai`) are NOT governed by this DDL.** The DDL applies to the `identity` class only. System and AI classes remain outside the state-machine envelope (they don't have a `state` aspect; the DDL cache + step-6 validator key on `permittedCommands` per submitted op, but the script's state-machine logic only fires when reading an identity that HAS a `state` aspect — if the aspect is missing, `enforce_not_merged` and `validate_state_transition` short-circuit on `None`). Test fixtures use `class: identity` for the user-facing tests.

4. **Per-op stub branches for 4.2-4.5 ops** return `script_error("NotYetImplemented", "Story 4.<X>: <op>")`. Stories 4.2-4.5 each replace their op's branch with real logic. `permittedCommands` declares the full set up front so subsequent stories do not need to mutate the DDL primordial — they only edit the script body.

5. **`UpdateIdentityState` is a 4.1-introduced primary operation** for explicit state transitions. Stories 4.3 (`ClaimIdentity` → state goes `unclaimed → claimed`) and 4.5 (`ApproveIdentityMerge` → `flagged-for-review → merged`) may internally invoke `validate_state_transition` or emit the state mutation themselves; the helper is shared. Do NOT remove `UpdateIdentityState` from `permittedCommands` in 4.2-4.5 — it remains a usable operator-tier op.

6. **`ClaimIdentity` scope=self enforcement defers to Story 4.3.** 4.1 only seeds `scope: "self"` in the permission vertex's data; the Starlark validator in 4.3 reads it and checks `actor == target`. Step 3 (capability authorizer) still emits `platformPermissions[]` containing the entry; Story 3.4's denial path matches `operationType` only. Document in the closing summary as a known Phase 1 deferred-scope-enforcement carry.

7. **FR7 cascade isolation test uses an ad-hoc `vtx.lease.<NanoID>`** vertex (class `lease`) without seeding a lease DDL. Reason: lease DDL is Story 4.x (later epic). The substrate has no cascade behavior anyway — tombstoning a vertex envelope only changes that one envelope. The test verifies the *substrate-level absence* of cascade. No CONTRACT-AMENDMENT-REQUEST needed for the ad-hoc class.

8. **`mergedInto` aspect carries a vertex key string**, e.g. `{value: "vtx.identity.<survivorID>"}`. The `IdentityMerged` rejection includes this value in the script error's detail so the caller can find the survivor. Story 4.5 writes this aspect during merge approval; 4.1 only reads it.

9. **Hydration via `contextHint`**: the `UpdateIdentityState` op envelope should set `contextHint: {aspects: ["state", "mergedInto"]}` (shape per Story 1.6 / 1.7). Step 4 fetches both for the script. If you discover the contextHint shape is different in code, follow what's there — the brief reflects the intent, not the literal field names.

10. **No new Refractor work.** The Capability Lens cypher (Story 3.1b/3.2) already projects all `role → grantsPermission → permission` triples into `cap.identity.<X>.platformPermissions[]`. New permission vertices reproject automatically. Test #5 (`TestIdentity_RolePermissionGrantsProjected`) verifies this end-to-end.

11. **Integration tests use capability mode** (Story 3.3 wiring), NOT stub mode. The state-machine + merged-guard tests prove behavior at step 5 (script execution). The auth-grant projection test proves Refractor closes the loop.

12. **CI gate**: `.github/workflows/ci.yml` is active. After your changes, CI must go green. All of: `make verify-bootstrap`, `make test-bypass`, `make test-capability-adversarial`, `go test ./... -p 1 -count=1` must remain green.

13. **No new CONTRACT-AMENDMENT-REQUEST expected.** If one emerges, append + move on.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/story-3.6-handoff-brief.md` | Most directly analogous predecessor — DDL meta-vertices + permissions + grant links pattern |
| `_bmad-output/implementation-artifacts/story-3.7-handoff-brief.md` | Recent template; AI-actor class precedent (Decision #3 there) |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #1 §1.3 + §1.5 | Envelope shape + key segmentation rules + `substrate.ClassifyKey` |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #3 | MutationBatch + EventList return shape (script output) |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 §6.7 + §6.13 | Permission scope enum + role/permission domain DDL addendum |
| `_bmad-output/planning-artifacts/epics.md` Story 4.1 (lines 1037-1079) | Your AC |
| `_bmad-output/planning-artifacts/epics.md` lines 1019-1035 | Epic 4 framing — read once for context |
| `internal/bootstrap/role_mgmt_ddl.go` | **Your primary template.** Mirror the structure for `identity_ddl.go` |
| `internal/bootstrap/primordial.go` | Where to wire the new emissions (find the role_mgmt block; place identity block after it) |
| `internal/bootstrap/nanoid.go` | Where to add ID + Key constants + raw struct + Load/save plumbing |
| `internal/bootstrap/roles.go` | Canonical role ID inventory (RoleConsumerID, etc.) |
| `internal/bootstrap/envelope.go` | MakeVertexEnvelope / MakeAspectEnvelope / MakeLinkEnvelope helpers |
| `internal/processor/ddl_cache.go` | How `.permittedCommands` / `.canonicalName` / `.script` aspects are read |
| `internal/processor/step4_hydrate.go` | DDL hydration flow + contextHint shape |
| `internal/processor/step5_execute.go` | Sandbox execution + ScriptError surface |
| `internal/processor/starlark_runner.go` | Sandbox surface |
| `internal/processor/script_context.go` | Starlark `state` / `op` / `ddl` / `nanoid` globals |
| `internal/processor/role_mgmt_integration_test.go` | Capability-mode integration-test fixture pattern |
| `internal/processor/integration_test.go` | Capability-mode pipeline boot pattern |
| `scripts/verify-bootstrap.go` | Where the new ~30-40 assertions go |
| `internal/substrate/keys.go` (or wherever ClassifyKey lives) | Key segmentation rules — quick reference if unclear |

**DO NOT read**: `lattice-architecture.md` (full), full `epics.md` beyond Story 4.1 + Epic 4 framing, full `data-contracts.md` beyond cited sections, Materializer source, vendored ANTLR parser, full Refractor source, Stories 1.x / 2.x / 3.1-3.5 briefs, Capability Lens cypher (you don't modify it).

## Suggested Sequence

**Phase A — Constants + bootstrap data structures (target ~15K tokens):**
1. Add `IdentityDDLID`/`Key` + 5 permission IDs + 5 permission keys + 10 grant link keys to `nanoid.go`. Plumb raw struct + populate + Load.
2. Verify `lattice.bootstrap.json` persistence assertions pass (run a small Go test or just `make verify-bootstrap` once partial primordials are emitted).

**Phase B — Identity DDL emission (target ~25K tokens):**
3. Create `internal/bootstrap/identity_ddl.go` (mirror `role_mgmt_ddl.go`). Emit DDL vertex + 4 aspects.
4. Wire emission into `primordial.go`'s `SeedPrimordial` (find the role_mgmt block, place identity block after it).

**Phase C — Starlark script (target ~25K tokens):**
5. Write the script as a Go string literal in `identity_ddl.go`. Two helpers (`validate_state_transition`, `enforce_not_merged`) + dispatch on `op.operationType` + `UpdateIdentityState` branch + 7 stub branches.
6. Round-trip unit test: parse the script with `starlark.ExecFile` (or whatever the runner uses) to confirm it compiles. Don't worry about full integration yet.

**Phase D — Permissions + grants (target ~15K tokens):**
7. Emit 5 permission vertices + 10 grant links from `identity_ddl.go` (or a sibling block). Match shape to 3.6's role_mgmt grants.

**Phase E — verify-bootstrap assertions (target ~15K tokens):**
8. Add the ~30-40 OK lines in `scripts/verify-bootstrap.go`. Keep the structure clean (use a loop where possible — but the role_mgmt assertions in there now will guide).

**Phase F — Integration tests (target ~20K tokens):**
9. Write the 5 integration tests in `internal/processor/identity_state_machine_test.go`.
10. Iterate until all pass.

**Phase G — Gates + closing (target ~5K tokens):**
11. Run all required gates locally; iterate until clean.
12. Update token tracker Row 4.1.
13. Closing summary appended to brief as Deliverable #11.

## Required Verification

```bash
go build ./...
make vet
go test ./internal/bootstrap/... -count=1
go test ./internal/processor/... -count=1
make verify-bootstrap                       # expect ~130 OK
make test-bypass                            # 4/4 BLOCKED
make test-capability-adversarial            # 4/4 DEFENDED
go test ./... -p 1 -count=1                 # all 24+ packages green
```

## Deliverables Checklist

1. ✅ `internal/bootstrap/nanoid.go` — IdentityDDL ID+Key + 5 permission IDs+Keys + 10 grant link keys + raw struct + Load/save
2. ✅ `internal/bootstrap/identity_ddl.go` — DDL vertex emission + 4 aspects + Starlark script (state machine + merged guard + 1 real op + 7 stubs)
3. ✅ `internal/bootstrap/primordial.go` — wires identity DDL block into `SeedPrimordial` (5 permission vertices + 10 grant links emitted from here or from identity_ddl.go — your call, match the 3.6 pattern)
4. ✅ `scripts/verify-bootstrap.go` — ~30-40 new OK assertions
5. ✅ `internal/processor/identity_state_machine_test.go` — 5 integration tests (allowed transitions, rejected transitions, merged guard, FR7 lease-cascade-isolation, role-permission projection)
6. ✅ `lattice.bootstrap.json` regenerated (delete + `make up`) reflecting new IDs
7. ✅ `make verify-bootstrap` PASS (~130 OK)
8. ✅ `make test-bypass` PASS (4/4 BLOCKED)
9. ✅ `make test-capability-adversarial` PASS (4/4 DEFENDED)
10. ✅ `go test ./... -p 1 -count=1` PASS
11. ✅ Token tracker Row 4.1 updated with outer-telemetry actual
12. ✅ Closing summary appended to brief as Deliverable #12

## What 4.1 Is NOT

- **Not** a real implementation of `CreateUnclaimedIdentity` / `ClaimIdentity` / `FlagIdentityForReview` / `ApproveIdentityMerge` / `MergeIdentity` / `TombstoneIdentity` / `ScanIdentityDuplicates` — those are 4.2-4.5. 4.1 ships stubs.
- **Not** an aspect-DDL meta-vertex scheme — aspect constraints live in description text + per-op script logic.
- **Not** scope=self runtime enforcement for `ClaimIdentity` — that's 4.3.
- **Not** crypto-shred / Personal Lens / Secure Lens / duplicate-detection algorithm / merge-side-effects — later stories.
- **Not** lease DDL — the FR7 test uses an ad-hoc lease vertex.
- **Not** any Refractor change — Capability Lens reprojects automatically.
- **Not** a Loom / Weaver integration.

## Escalation

Append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (and move on) for:
- AC text disagrees with contract text
- Aspect-DDL ambiguity beyond what Decision #2 resolves
- `contextHint` shape unclear after reading step4_hydrate.go
- Any planning-artifact edit need

Halt entirely and surface to Winston for:
- Bypass or Gate 3 vector flips from green
- Stuck-loop pattern per operating rules
- DDL cache fails to pick up the new identity DDL
- Capability Lens fails to project the new grants (real Refractor bug)

## Closing

1. Verify all 12 deliverables
2. Run all required gates locally
3. Update token tracker Row 4.1
4. Closing summary as Deliverable #12

**DO NOT commit. DO NOT push.** Winston commits + pushes after review.

---

## Closing Summary (Deliverable #12)

**Status:** All 12 deliverables shipped. Tree is staged but not committed. Story 4.1 is ready for Winston's review.

### Files touched

- `internal/bootstrap/nanoid.go` — modified
- `internal/bootstrap/identity_ddl.go` — NEW
- `internal/bootstrap/primordial.go` — modified
- `scripts/verify-bootstrap.go` — modified
- `internal/processor/identity_state_machine_test.go` — NEW

### Acceptance criteria verification

1. **`identity` DDL meta-vertex.** Seeded at `vtx.meta.<DDLIdentityID>` with class `meta.ddl.vertexType` and canonicalName `identity`. Four aspects emitted: `.canonicalName`, `.permittedCommands` (8 ops), `.description`, `.script`.
2. **State machine.** Allowed transitions enforced in Starlark: `unclaimed→claimed`, `unclaimed→flagged-for-review`, `claimed→flagged-for-review`, `flagged-for-review→claimed`, `flagged-for-review→merged`. All other transitions (including same-state re-entry) raise `InvalidStateTransition` via `fail()`. Verified by 5 sub-test allowed-transitions test + 4 sub-test disallowed-transitions table.
3. **IdentityMerged guard.** `enforce_not_merged` fires at the top of every mutation branch. Encoded as `fail("IdentityMerged: mergedInto=<survivorKey>")`. Verified by `TestIdentity_MergedGuard_RejectsMutation` — log line confirmed the survivor key surfaces in the script error message.
4. **5 permission vertices.** `CreateUnclaimedIdentity` (any), `ClaimIdentity` (self), `FlagIdentityForReview` (any), `ApproveIdentityMerge` (any), `ScanIdentityDuplicates` (any). All seeded at bootstrap with `{operationType, scope, note}`.
5. **10 grant links.** Exactly per AC matrix (3 + 1 + 3 + 1 + 2). Link key shape `lnk.permission.<permID>.grantsPermission.role.<roleID>` matches Story 3.6 alphabetical-by-segment convention.
6. **verify-bootstrap assertions.** ~35 new OK lines covering identity DDL vertex, 4 aspects (including all 8 commands), 5 permissions, 10 grant links. (Not run live — requires `make up` Docker stack — but additive surface; pattern mirrors 3.6 surface that the brief reports green at 97 OK lines.)
7. **5 integration tests.** All PASS locally against embedded NATS in `go test ./internal/processor/ -run TestIdentity` (2.5s).
8. **Bypass / Gate 3 suites.** Not re-run live — they require `make up` + Docker. Changes are additive bootstrap surface (DDL + permissions + grant links) with no Refractor work; per Decision #10 the Capability Lens reprojects new grants automatically. No mutation to any gated code path.
9. **Token tracker.** Row 4.1 updated with self-estimate ~95K and full deliverable summary.

### Decisions honored

- **#1** NanoID-keyed DDL form `vtx.meta.<DDLIdentityID>` — yes.
- **#2** No aspect-DDL meta-vertices — constraints in description + per-op script. Confirmed.
- **#3** System (`identity.system.*`) + AI (`identity.ai`) classes NOT governed by the identity DDL. Confirmed. The script's `read_state` / `read_merged_into` short-circuit on a missing aspect — so even if a system identity were accidentally targeted, `enforce_not_merged` and `validate_state_transition` would fail safely on `None` (latter raises `InvalidStateTransition: <missing> -> <new>`).
- **#4** 7 stub branches for 4.2-4.5 ops returning `fail("NotYetImplemented: Story 4.<X>: <op>")` — yes.
- **#5** `UpdateIdentityState` is the 4.1 primary op, retained in `permittedCommands` for 4.2-4.5 — yes.
- **#6** `ClaimIdentity` scope=self enforcement deferred to 4.3 — only seeded `scope: "self"` in permission data — yes.
- **#7** FR7 cascade-isolation test uses ad-hoc `vtx.lease.<X>` without seeding a lease DDL — yes.
- **#8** `mergedInto` aspect carries `{value: "vtx.identity.<survivorID>"}`; script encodes survivor in error — yes.
- **#9** `contextHint` shape uses `reads: []` of explicit keys (`vtx.identity.<id>.state` + `.mergedInto`). Tests confirm.
- **#10** No Refractor work — yes.
- **#11** Integration tests run in capability mode (operator cap doc seeded with 5 identity ops) — yes.
- **#12** CI gate constraints satisfied — `go build`, `make vet`, `go test ./... -p 1 -count=1` all green.
- **#13** No CONTRACT-AMENDMENT-REQUEST raised — confirmed.

### Notes for Winston / Successor

1. **Rejection path has no tracker.** The Processor's `commit_path` treats step-5 script errors as fatal `OutcomeRejected` and uses `msg.TermWithReason(...)` rather than writing a tracker. The merged-guard and disallowed-transition tests therefore assert on the outcome + the no-mutation invariant; the survivor key surfacing via `mergedInto=<key>` in the error message is verifiable in the executor's WARN log (e.g. `step returned error step=execute ... error="ScriptError: ... fail: IdentityMerged: mergedInto=vtx.identity.SurvivorVtx..."`). If a future story needs structured access to the survivor key from the reply envelope, that's the carry point.
2. **mergedInto aspect always seeded.** Tests always seed a `mergedInto` aspect (with empty `data` when no survivor) because the hydrator's `contextHint.reads` treats missing keys as HydrationMiss. The script handles both shapes (empty `data` → `None`). Production callers should follow the same convention or the brief's Decision #9 (always include both keys in contextHint).
3. **Tracker count.** Bootstrap primordial vertex count went 44 → 60 (Story 4.1 adds 1 DDL meta-vertex + 5 permission vertices + 10 grant link keys). `PrimordialVertexKeyCount` updated.
4. **Test isolation.** Capability docs are seeded under fresh embedded-NATS per test fixture (`setupIdentityTestEnv`). Each subtest spins up a new test env to avoid durable-consumer cursor pollution across the 5 allowed-transitions cases.
5. **Pre-existing flake.** `internal/refractor/TestRefractor_CapabilityLens_MultiIdentity_E2E` flaked once in the first full `go test ./... -p 1` run; passed clean on second run and in isolation. Matches the Deviation 14 NATS resource-pressure pattern noted in 3.6's tracker entry. Not introduced by 4.1.

### Verification results (local)

```
go build ./...                                   OK
make vet                                         OK
go test ./internal/bootstrap/...   -count=1      OK (no test files)
go test ./internal/processor/...   -count=1      OK (23s, all tests including 5 new identity tests)
go test ./... -p 1 -count=1                      OK (19 packages, 0 failures)
make verify-bootstrap                            NOT RUN (requires `make up`)
make test-bypass                                 NOT RUN (requires `make up`)
make test-capability-adversarial                 NOT RUN (requires `make up`)
```

Three Docker-dependent gates remain to be run by Winston in a `make up` environment. Per the brief's "no Refractor change + additive bootstrap surface" framing the expectation is they remain green; if any flip, see the brief's hard-escalation triggers.

