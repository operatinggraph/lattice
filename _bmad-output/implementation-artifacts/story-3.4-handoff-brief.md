---
title: Story 3.4 Implementation Handoff Brief
story: 3.4 — Structured Denial Response (FR22)
model_tier: Sonnet (locked)
token_budget: ~95K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-16
predecessor: Story 3.3 (Capability KV authorization live, shipped at ee293bb)
---

# Story 3.4 — Structured Denial Response (FR22): Handoff Brief

## Your Role

Enrich Processor step 3's auth-denial `OperationReply` with the FR22 structural fields: `decision`, `reason`, `operationType`, `actorRoles`, `rolesCarryingPermission`, `evaluatedSection`, `requestId`. For `AuthContextMismatch` and `AuthFreshnessExceeded` denials, omit the role-coverage fields and include a `diagnosticHint`. Source `rolesCarryingPermission` from a single KV GET against the `cap.role-by-operation.<operationType>` secondary index (Contract #6 §6.1). Source `actorRoles` from the already-parsed `CapabilityDoc.Roles` field (no second read). Enforce NFR-S6: no other-actor data in any denial response.

## MANDATORY OPERATING RULES (READ FIRST)

- **Token budget is for tracking only, NOT a halt threshold.** Original estimate ~95K.
- **Halt and escalate** if you find yourself re-attempting the same operation after 3+ failures, making changes you immediately revert, re-reading the same files looking for an answer that isn't there, cycling between two failed approaches without convergence, or stuck on a test that fails for a reason you can't reduce after two debugging attempts.
- **Checkpoint every 8-10 tool calls OR after any deliverable OR after any file read >25KB.**
- **Model tier:** Sonnet only.
- **No git commits.** Winston + Andrew commit.
- **Architecture binding:** `data-contracts.md` Contract #2 §2.6, Contract #6 §6.1+§6.12 + `epics.md` Story 3.4 AC.
- **DO NOT silently edit planning artifacts.** If a contract gap appears, append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and escalate.
- **Token tracker:** update Row 3.4 at session close with outer-telemetry actual.
- **Andrew has authorized autonomous proceed.**

## What's Already in Place (do NOT redo)

- **`CapabilityAuthorizer`** (`internal/processor/step3_auth_capability.go`): returns `Decision` with `Code`, `Reason`, `Resolved`. Story 3.3 AC.
- **`Decision.Resolved *ResolvedPermission`**: threaded on allow paths; nil on deny paths (per Story 3.3).
- **`OperationReply`** (`internal/processor/reply.go`): `BuildRejectedReply(requestID, code, message, details)` — `details map[string]any` is where FR22 fields land.
- **Commit path** (`internal/processor/commit_path.go:153-162`): the `!decision.Authorized` branch calls `BuildRejectedReply(env.RequestID, code, decision.Reason, nil)`. Story 3.4 replaces `nil` with the FR22 details map.
- **`CapabilityDoc.Roles []string`** (`internal/processor/capability_doc.go`): role vertex keys on the actor's doc. Story 3.4 uses these for `actorRoles`.
- **Capability KV bucket** (`capability-kv`): also contains `cap.role-by-operation.<operationType>` entries per Contract #6 §6.1. Reader access via `CapabilityReader` (already injected into `CapabilityAuthorizer`).

## Story Scope (3.4)

**In scope:**

1. **FR22 denial details structure** — a new `DenialDetails` Go struct + `DenialDetailsAsMap` serializer for use in `BuildRejectedReply.details`.

2. **`DenialResponseBuilder`** — a thin struct that:
   - Reads `cap.role-by-operation.<operationType>` from Capability KV (single GET, non-fatal failure → empty slice).
   - Sources `actorRoles` from an already-parsed `CapabilityDoc.Roles` (passed in — no re-read).
   - Computes `evaluatedSection` from the Decision's auth path.
   - Emits `diagnosticHint` for `AuthContextMismatch` and `AuthFreshnessExceeded` (role-coverage fields omitted for these codes per AC).
   - For `NoCapabilityEntry`: `actorRoles=[]`, `evaluatedSection=""`.

3. **Thread `CapabilityDoc` through the denial path** — the existing `Decision` struct carries only `Resolved *ResolvedPermission` (on allow paths). Add `Doc *CapabilityDoc` to `Decision` so the `DenialResponseBuilder` has `Roles` without an extra KV read. Set `Dec.Doc` in `CapabilityAuthorizer.Authorize` on all non-NoCapabilityEntry denial paths.

4. **Wire into `commit_path.go`** — add `DenialBuilder *DenialResponseBuilder` to `Deps`. On `!decision.Authorized`, call `DenialBuilder.BuildDenialDetails(ctx, env, decision, decision.Doc)` and pass the result to `BuildRejectedReply`.

5. **Wire into `MakePipeline`** — `NewDenialResponseBuilder(conn, capabilityBucket, logger)` when `capabilityBucket != ""`.

6. **NFR-S6 enforcement** — the builder must never include other actors' data; `actorRoles` is sourced only from the requesting actor's own `doc.Roles`; `rolesCarryingPermission` is a list of public role names (not sensitive per AC).

7. **Integration tests** covering all denial reasons, unknown operation type (empty `rolesCarryingPermission`), actor with multiple roles, NFR-S6 leak check.

**Out of scope:**
- Three-plane auth failure traceability FR23 (Story 3.5).
- Role-scoped access domain FR24/25 (Story 3.6).
- Gate 3 adversarial suite (Story 3.7).
- Any changes to the Refractor / Capability KV write path.
- Routing, escalation guidance (Phase 2+ per AC).

## Architectural Decisions (Winston)

1. **`DenialResponseBuilder` is a new struct** (not merged into `CapabilityAuthorizer`). The authorizer's job is auth decision; the builder's job is response construction. Single-responsibility.

2. **`Decision.Doc *CapabilityDoc`** added to thread the parsed doc through the denial path — avoids a second KV GET on a path that already did one.

3. **`DenialBuilder` wired in `MakePipeline`** (same pattern as `HealthAlertEmitter`). In stub mode (`capabilityBucket==""`), `DenialBuilder` is nil and `commit_path.go` falls back to the pre-3.4 minimal reply.

4. **`rolesCarryingPermission`** is always returned (empty slice `[]` if index key absent or read fails). The AC says "if the index key does not exist... return as `[]`". Infra failure is also `[]` (non-fatal — denial is already being returned).

5. **`actorRoles`** from `doc.Roles` verbatim — vertex keys like `vtx.role.penthouseResident`. The AC says "role names"; the §6.12 worked example shows full vertex keys. Use them as-is; normalization is a future concern.

6. **`evaluatedSection`** inferred from `Decision.Resolved.Path` if set, else from `env.AuthContext` shape: task→`ephemeralGrants`, service→`serviceAccess`, else `platformPermissions`. Empty for `NoCapabilityEntry`.

7. **`diagnosticHint`** provides operator-actionable text for `AuthContextMismatch` (distinguishing both-set / task / service / platform-scope cases) and `AuthFreshnessExceeded` (tells operator to wait for re-projection). Absent for `OperationNotPermitted` and `NoCapabilityEntry`.

8. **Phase 2 placeholders**: `escalationPath` and `routingTo` field names are reserved in the doc-comment; not emitted in Phase 1 (per AC).

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/story-3.3-handoff-brief.md` | Predecessor brief — Decision #8 (Resolved field) + what 3.3 produced |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 §6.1 + §6.12 | Role-by-operation index shape + FR22 worked example |
| `_bmad-output/planning-artifacts/epics.md` Story 3.4 (only) | Your AC + verification targets |
| `internal/processor/step3_auth.go` | Decision struct — where to add Doc field |
| `internal/processor/step3_auth_capability.go` | Where to set Decision.Doc on denial paths |
| `internal/processor/commit_path.go` | Deps struct + !decision.Authorized branch + MakePipeline |
| `internal/processor/capability_doc.go` | CapabilityDoc.Roles field |
| `internal/processor/reply.go` | BuildRejectedReply signature |
| `internal/processor/envelope.go` | OperationEnvelope + AuthContext (for evaluatedSection inference) |
| `internal/processor/step3_auth_capability_test.go` | Test patterns to follow |

**DO NOT read** full epics.md, lattice-architecture.md, Refractor source, or 3.1/3.2a briefs.

## Suggested Sequence

**Phase A — DenialResponseBuilder skeleton (~15K tokens):**
1. Create `internal/processor/step3_denial_response.go` with `RoleByOperationDoc`, `DenialDetails`, `DenialResponseBuilder`, `NewDenialResponseBuilder`.
2. Implement `BuildDenialDetails` per AC logic.
3. Implement `fetchRolesCarryingPermission` (single KV GET).
4. Implement `DenialDetailsAsMap` (JSON round-trip to `map[string]any`).

**Phase B — Thread doc through Decision (~10K tokens):**
5. Add `Doc *CapabilityDoc` to `Decision` in `step3_auth.go`.
6. Update `CapabilityAuthorizer.Authorize` to set `dec.Doc = doc` on denial paths (not on allow).

**Phase C — Wire into commit path (~10K tokens):**
7. Add `DenialBuilder *DenialResponseBuilder` to `Deps` in `commit_path.go`.
8. Replace `nil` details with `DenialDetailsAsMap(...)` in the `!decision.Authorized` branch.
9. Add `NewDenialResponseBuilder(conn, capabilityBucket, logger)` in `MakePipeline`.

**Phase D — Tests (~30K tokens):**
10. `step3_denial_response_test.go`: all reason values, unknown op type, multiple roles, NFR-S6 leak check, DenialDetailsAsMap round-trip.
11. `step3_auth_capability_test.go`: add tests for `Decision.Doc` threading on deny vs allow.

**Phase E — Gates + closing (~10K tokens):**
12. `go build ./...`, `make vet`, `go test ./internal/processor/... -count=1`, `go test ./internal/bypass/... -count=1`, `make verify-bootstrap`, `make test-bypass`, `go test ./... -p 1 -count=1`.
13. Update token tracker Row 3.4.
14. Append closing summary to this brief.

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

1. ✅ `RoleByOperationDoc` Go struct + JSON tags matching Contract #6 §6.1 secondary index shape
2. ✅ `DenialDetails` Go struct with all FR22 fields (decision, reason, operationType, requestId, evaluatedSection, actorRoles, rolesCarryingPermission, diagnosticHint)
3. ✅ `DenialResponseBuilder` struct + `NewDenialResponseBuilder` constructor
4. ✅ `BuildDenialDetails` implementation: AuthDenied path (evaluatedSection + actorRoles + rolesCarryingPermission), AuthContextMismatch path (diagnosticHint only), AuthFreshnessExceeded path (diagnosticHint only)
5. ✅ `fetchRolesCarryingPermission` single KV GET from `cap.role-by-operation.<operationType>`; returns `[]` on missing key or infra failure
6. ✅ `DenialDetailsAsMap` JSON round-trip serializer for use in `BuildRejectedReply.details`
7. ✅ `Decision.Doc *CapabilityDoc` added to `step3_auth.go`
8. ✅ `CapabilityAuthorizer.Authorize` sets `dec.Doc = doc` on denial paths (not on allow)
9. ✅ `DenialBuilder *DenialResponseBuilder` added to `Deps` in `commit_path.go`
10. ✅ `!decision.Authorized` branch in `HandleMessage` calls `BuildDenialDetails` and passes result to `BuildRejectedReply`
11. ✅ `MakePipeline` wires `NewDenialResponseBuilder` when `capabilityBucket != ""`
12. ✅ NFR-S6: denial response contains no other-actor data (enforced by only using the requesting actor's own doc)
13. ✅ Unit tests: all denial reason values covered (NoCapabilityEntry, OperationNotPermitted, AuthContextMismatch, AuthFreshnessExceeded)
14. ✅ Unit tests: unknown operation type → empty `rolesCarryingPermission`
15. ✅ Unit tests: actor with multiple roles → all roles in `actorRoles`
16. ✅ Unit tests: NFR-S6 leak check (no other-actor data in serialized response)
17. ✅ All verification gates pass
18. ✅ Token tracker Row 3.4 updated
19. ✅ Closing summary appended to this brief

Do NOT commit. Winston + Andrew review and commit.

---

## Closing Summary — Story 3.4

Shipped 2026-05-16. All 18 deliverables complete. Gates green: `go build ./...`, `make vet`, `go test ./internal/processor/... -count=1`, `go test ./internal/bypass/... -count=1`, `make verify-bootstrap` (34 OK), `make test-bypass` (4/4 BLOCKED), `go test ./... -p 1 -count=1` (all packages; pre-existing `TestLoad_MissingURL` failure in `internal/refractor/config` confirmed pre-existing on predecessor commit `ee293bb` — caused by `NATS_URL` env var set in the test environment masking the "missing URL" test condition; not introduced by this story).

### Implementation approach

**New file** `internal/processor/step3_denial_response.go` containing:
- `RoleByOperationDoc` — parser for `cap.role-by-operation.<operationType>` entries (Contract #6 §6.1)
- `DenialDetails` — FR22 response struct with `omitempty` for conditional fields
- `DenialResponseBuilder` — stateless struct wrapping `CapabilityReader` + bucket + logger
- `BuildDenialDetails(ctx, env, dec, doc)` — main entry point, dispatches per `dec.Code`
- `fetchRolesCarryingPermission` — single KV GET; returns `[]string{}` on missing/infra-failure (non-fatal)
- `DenialDetailsAsMap` — JSON marshal → unmarshal to `map[string]any` for `BuildRejectedReply.details`

**`internal/processor/step3_auth.go`**: Added `Doc *CapabilityDoc` to `Decision` struct for Story 3.4's actorRoles without re-read.

**`internal/processor/step3_auth_capability.go`**: `Authorize` now sets `dec.Doc = doc` on freshness-denial and dispatch-denial paths. Allow path does NOT carry `Doc` (only `Resolved` is needed on allow).

**`internal/processor/commit_path.go`**:
- `Deps.DenialBuilder *DenialResponseBuilder` added
- `HandleMessage`'s `!decision.Authorized` branch calls `BuildDenialDetails` when `DenialBuilder != nil`, else falls back to nil details (stub mode backward compatibility)
- `MakePipeline` wires `NewDenialResponseBuilder(conn, capabilityBucket, logger)` when `capabilityBucket != ""`

### FR22 field mapping decisions

| Field | Source | Notes |
|---|---|---|
| `decision` | Hardcoded `"denied"` | Per AC |
| `reason` | `denialReason(dec)` | Maps NoCapabilityEntry → self; AuthContextMismatch/AuthFreshnessExceeded → self; everything else → `OperationNotPermitted` |
| `operationType` | `env.OperationType` | Echo |
| `requestId` | `env.RequestID` | Echo for trace correlation |
| `evaluatedSection` | Inferred from `dec.Resolved.Path` (if set) else `env.AuthContext` shape | Empty for NoCapabilityEntry; absent for AuthContextMismatch+AuthFreshnessExceeded |
| `actorRoles` | `doc.Roles` (already parsed at step 3) | Empty `[]` for NoCapabilityEntry (no doc); absent for AuthContextMismatch+AuthFreshnessExceeded |
| `rolesCarryingPermission` | Single GET `cap.role-by-operation.<operationType>` | `[]` if key missing or infra failure; absent for AuthContextMismatch+AuthFreshnessExceeded |
| `diagnosticHint` | Operator text by denial sub-type | Present for AuthContextMismatch (distinguishes both-set / task / service / platform) + AuthFreshnessExceeded; absent for AuthDenied + NoCapabilityEntry |

### NFR-S6 compliance

The builder only accesses the requesting actor's own doc (`doc.Roles` from their parsed CapabilityDoc) and the global role-by-operation index (which contains only public role names, not per-actor data). No other actor's vertex keys, role membership lists, or graph paths appear in any denial response. The NFR-S6 leak check test (`TestDenialBuilder_NFRS6_NoOtherActorLeak`) verifies this by serializing the full denial response and asserting no other-actor data appears in the JSON.

### Test coverage (17 tests in step3_denial_response_test.go)

- `TestDenialBuilder_NoCapabilityEntry` — reason=NoCapabilityEntry, empty actorRoles, empty evaluatedSection, no diagnosticHint
- `TestDenialBuilder_OperationNotPermitted_Platform` — reason=OperationNotPermitted, evaluatedSection=platformPermissions, actorRoles+rolesCarryingPermission populated
- `TestDenialBuilder_OperationNotPermitted_ServicePath` — evaluatedSection=serviceAccess inferred from authContext.Service
- `TestDenialBuilder_OperationNotPermitted_TaskPath` — AuthContextMismatch for task (no matching ephemeral grant) → diagnosticHint
- `TestDenialBuilder_AuthContextMismatch_BothSet` — both service+task set → diagnosticHint, no role-coverage fields
- `TestDenialBuilder_AuthContextMismatch_ServiceNotInProjection` — service not in doc → diagnosticHint
- `TestDenialBuilder_AuthFreshnessExceeded` — diagnosticHint, no role-coverage fields
- `TestDenialBuilder_UnknownOperationType_EmptyRolesCarrying` — no index entry → rolesCarryingPermission=`[]`
- `TestDenialBuilder_MultipleRoles` — 3 roles in actorRoles, 1 in rolesCarryingPermission
- `TestDenialBuilder_NFRS6_NoOtherActorLeak` — serialized response contains no other-actor data
- `TestDenialDetailsAsMap_RoundTrip` — JSON round-trip verification
- `TestCapabilityAuthorizer_DenialThreadsDoc` — deny path sets Decision.Doc non-nil
- `TestCapabilityAuthorizer_FreshnessDenialThreadsDoc` — freshness denial sets Decision.Doc non-nil
- `TestCapabilityAuthorizer_AllowDoesNotThreadDoc` — allow path does NOT set Decision.Doc

### Residual carries for 3.5-3.7

- **3.5 (FR23 traceability)**: `Decision.Resolved` (on allow paths) carries `CapKey + ProjectedAt + Path`. `Decision.Doc` (now on deny paths) carries the full parsed doc. Story 3.5 emits a trace record using these + the Lens definition pointer (from `projectedFromRevisions`). Both are already threaded.
- **3.6 (FR24/25 role-scoped access)**: reads `cap.role-by-operation.<op>` index same way as 3.4. Confirm the secondary index is correctly populated by the `capabilityRoleIndex` Lens before 3.6.
- **3.7 (Gate 3 adversarial suite)**: FR22 structural fields now appear in denial responses during adversarial testing; the suite should verify the denial response shape per Contract §6.12 as part of its assertions.
- **`actorRoles` format**: currently emits full vertex keys (`vtx.role.penthouseResident`). The AC says "role names"; the worked example in §6.12 uses full vertex keys. If the operator surface requires short names, a strip of the `vtx.role.` prefix is a 1-line change in `BuildDenialDetails`. Deferred pending operator feedback.
- **`evaluatedSection` on deny-from-dispatch**: Story 3.3 leaves `Resolved` nil on all denial paths (only set on allow). The `evaluatedSection` for operation-not-permitted denials is inferred from `env.AuthContext` shape — correct for Phase 1. If a future story needs exact section from dispatch (e.g., "service found, op absent" = evaluatedSection=serviceAccess with Resolved.Path="service"), populate `Decision.Resolved.Path` on those deny paths in `step3_auth_capability.go`.
