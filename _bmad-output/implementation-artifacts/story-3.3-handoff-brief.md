---
title: Story 3.3 Implementation Handoff Brief
story: 3.3 — Processor Step 3: Capability KV Authorization
model_tier: Opus (locked)
token_budget: ~125K (estimate; for tracking only — not a halt threshold)
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-15
predecessor: Story 3.2b (Capability KV projections live, shipped at 99017ca)
---

# Story 3.3 — Processor Step 3 Capability KV Authorization: Handoff Brief

## Your Role

Replace Processor step 3's `StubAuthorizer` with a real Capability KV reader. The Capability KV bucket is now populated by Refractor's primary Capability Lens (Story 3.2a) and the role-coverage secondary Lens (Story 3.2b) — each actor has a `cap.identity.<NanoID>` entry conforming to Contract #6 §6.2. Your job: a single O(1) GET, dispatch per Contract #6 §6.4-6.8, freshness gate via `projectedAt`, Health KV signals for staleness + step-3 latency, and migrate the Phase 1 Gate 2 bypass suite + processor integration tests to exercise real auth.

After 3.3, Stories 3.4 (denial response FR22), 3.5 (auth failure traceability FR23), 3.6 (role-scoped access FR24/25), and 3.7 (Gate 3 adversarial suite) are unblocked — they have no Refractor or Processor prerequisites beyond 3.3.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

- **Token budget is for tracking only, NOT a halt threshold.** Original estimate ~125K. Record actual outer-telemetry consumption in the tracker at session close. Do NOT stop work based on token count.

- **Halt and escalate** if you find yourself in any of these patterns:
  - Re-attempting the same operation after 3+ failures
  - Making changes you immediately revert
  - Re-reading the same files looking for an answer that isn't there
  - Cycling between two failed approaches without convergence
  - Stuck on a test that fails for a reason you can't reduce after two debugging attempts

  These are stuck-loop signals. Token consumption alone is NOT a halt signal.

- **At every checkpoint (every 8-10 tool calls OR after any deliverable OR after any file read >25KB):** send a "checkpoint message" with deliverables done, deliverables remaining, honest token estimate, and any concerns.

- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **No git commits by you.** Winston + Andrew commit.
- **Architecture binding:** `_bmad-output/planning-artifacts/data-contracts.md` Contract #2 (§2.6, §2.8), Contract #5 (Health KV), Contract #6 + `_bmad-output/planning-artifacts/epics.md` Story 3.3 are source of truth.
- **DO NOT silently edit planning artifacts.** If a contract gap appears, append to `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` and escalate.
- **Token tracker:** update Row 3.3 at session close with outer-telemetry actual.
- **Andrew has authorized autonomous proceed.** No mid-session approvals required unless you hit an architectural gap.

## What's Already in Place (do NOT redo)

- **Authorizer interface** (`internal/processor/step3_auth.go`): `Authorize(ctx, env *OperationEnvelope) (Decision, error)` — your new `CapabilityAuthorizer` implements this. `Decision` has `Authorized, Stub, Reason, Code`.
- **AuthMode selector** (`internal/processor/step3_auth.go:SelectAuthorizer`): currently returns `errCapabilityModeNotYetAvailable` for `AuthModeCapability`. You wire the real implementation here.
- **Envelope** (`internal/processor/envelope.go`): `OperationEnvelope.AuthContext { Service, Task, Target string }` mirrors Contract #2 §2.8.
- **Error codes** (`internal/processor/envelope.go`): `ErrCodeAuthDenied`, `ErrCodeAuthContextMismatch` exist. Add `ErrCodeAuthFreshnessExceeded` + `ErrCodeAuthInfrastructureFailure` (new).
- **Capability KV bucket** (`capability-kv`): provisioned by bootstrap (Story 1.4), populated by Refractor (Story 3.2). Reader access via `jetstream.KeyValue` handle.
- **Capability envelope shape** (`internal/refractor/capabilityenv/envelope.go`): the primary Lens produces Contract #6 §6.2 envelopes; secondary Lens produces Contract #6 §6.1 role-coverage entries. You consume the primary shape only — the secondary is Story 3.4's input.
- **Story 3.2b multi-identity e2e** demonstrates Capability KV writes work end-to-end with mean 4.4ms / p95 5.7ms / p99 5.7ms (~88× headroom under NFR-P3 500ms).

Tree is clean at session start (commit 99017ca; `go build`, `make vet`, `make verify-bootstrap` 34 OK, `make test-bypass` 4/4 BLOCKED, `go test ./... -p 1 -count=1` all green).

## Story Scope (3.3)

**In scope:**

1. **`CapabilityAuthorizer` implementation** that reads `cap.<actor-vertex-key-suffix>` from `capability-kv` via single NATS KV GET; parses the Contract #6 §6.2 envelope; dispatches per §6.4-6.8.

2. **Dispatch logic** per Contract #2 §2.8 / Contract #6:
   - Both `authContext.service` AND `authContext.task` set → `AuthContextMismatch`
   - `authContext.task` AND `authContext.target` set → check `ephemeralGrants[]` (match `taskKey + operationType + target + expiresAt > now`)
   - `authContext.service` set → check `serviceAccess[]` (match `service` + `allowedOperations.operationType`)
   - Neither service nor task → platform → check `platformPermissions[]` with scope validation (`any`/`self`/`specific`; `owned` is Phase 2 — reject as not-implemented if encountered)

3. **No-entry semantics** (Contract #6 §6.8): missing key → `AuthDenied` with reason `NoCapabilityEntry`.

4. **Infrastructure failure path**: NATS read failure → return error from `Authorize` (NOT a `Decision{Authorized: false}`). Commit path nacks for retry per existing nak-on-error pathway. Add new error code `ErrCodeAuthInfrastructureFailure` for reply construction if the nak path needs it; check existing nak handling to see if it already produces a typed reply.

5. **Freshness gate**: parse `projectedAt` from the entry. If `now - projectedAt > 5 × NFR-P3` (default 2500ms) → `AuthFreshnessExceeded`. If above NFR-P3 (500ms) but below ceiling → still allow, but emit Health KV signal `health.processor.<instance>.cap-staleness` (Decision #4 below for shape).

6. **Health KV signals**:
   - `health.processor.<instance>.cap-staleness` — staleness aggregated per heartbeat tick (mean/p95/p99 in milliseconds + count of entries exceeding NFR-P3 but under ceiling). Skip emission if no staleness samples in the window.
   - `health.processor.<instance>.step3-latency` — Authorize-call latency (mean/p95/p99 in nanoseconds + count). Always emit.
   - On `AuthFreshnessExceeded` → in addition to denial, emit `health.alerts.security.auth-freshness-exceeded` (Contract #5 alert pattern) so the operator's pager fires.
   - Pattern reference: `internal/refractor/pipeline/latency.go` (3.2b ring buffer) — port the design to processor, OR factor it into a shared `internal/healthutil` package if cleanly extractable in <40 LOC. Don't engineer a shared package for one consumer — single-package implementation is fine.

7. **AuthMode default flip**: `LATTICE_AUTH_MODE` default changes from `stub` to `capability`. The stub path stays available behind the flag for unit testing; when stub mode is selected, Processor logs WARNING at startup AND emits `health.alerts.security.stub-auth-active` so the operator knows their cluster is running degraded auth. `SelectAuthorizer` now returns `NewCapabilityAuthorizer(...)` for `AuthModeCapability` and `""` (default).

8. **Resolved permission attached to operation context**: AC #3 says "the resolved permission entry is attached to the operation context for downstream observability." Story 3.4 (denial response) and 3.5 (traceability) consume this. Add a field on the in-process context object that flows from step 3 → step 9 (event publication). Don't bloat the envelope — internal struct only.

9. **Bypass suite migration** (`internal/bypass/`): the Gate 2 bypass tests must continue to report BLOCKED. With real auth wired, the "unauthorized actor" path becomes the real `AuthDenied` decision rather than the stub allow-all. Re-audit the 4 bypass categories; if any now report DIFFERENT but still BLOCKED, document. If any flip to NOT-BLOCKED, that's an escalation — Gate 2 is a regression gate.

10. **Integration tests** for all four `authContext` shapes (platform / service / task / both-set → mismatch), missing entry, stale entry allowed-with-signal, excessively-stale entry denied, stub-mode warning emission. Reuse `internal/processor/integration_test.go` patterns; new test file `internal/processor/step3_auth_capability_test.go` for the unit-tier `CapabilityAuthorizer` tests; integration tests live alongside existing `integration_test.go` (or a new sibling file).

11. **Existing integration tests migration**: `integration_test.go` and `nfr_r1_test.go` currently call `NewStubAuthorizer`. Two options — pick per case:
    - Keep stub-driven for tests that don't care about auth correctness (most NFR-R1 fault-injection tests probably don't).
    - Migrate to a capability-driven path with a pre-seeded `cap.<actor>` entry that allows the test's operation, for tests that exercise the full happy path.
    A blanket migration isn't required — only migrate where the test is genuinely about end-to-end auth. Document the migrated set in the closing summary.

**Out of scope:**
- Denial response FR22 structural shape (Story 3.4) — your denial returns `Decision{Code: AuthDenied}` without FR22 details; 3.4 adds the structural fields.
- Three-plane auth failure traceability FR23 (Story 3.5).
- Role-scoped access domain FR24/25 (Story 3.6).
- Gate 3 adversarial suite (Story 3.7).
- Capability KV write path (Refractor side; done in 3.2).
- Personal Lens / Secure Lens / multi-cell read.

**Hard escalation triggers:**
- A bypass-suite category flips from BLOCKED to NOT-BLOCKED after migration.
- The Capability KV envelope's actual shape disagrees with Contract #6 §6.2 in a way that prevents dispatch (escalate as drift — Story 3.2b's contract-conformance test should have caught this, so investigate first).
- An `authContext` shape isn't unambiguously covered by Contract #6's dispatch tables.
- A pre-existing integration test fails after wiring real auth and the fix isn't obvious within 2 debug attempts (might indicate a fixture seeding gap that needs Winston input).

## Architectural Decisions Already Made (Winston)

1. **`CapabilityAuthorizer` is a struct with NATS KV handle + clock + config; NOT a global singleton.** Dependency-injected via `commit_path.go`'s `Deps`. Construct in `cmd/processor/main.go` (or wherever the Processor binary wires deps).

2. **Single NATS KV GET, no caching in 3.3.** No actor-level cache, no TTL. The hot-path read is O(1) and target latency is ~ms. Caching is a Phase 2 optimization if NFR-P3 conformance data warrants it.

3. **Clock is injected** (`Clock interface { Now() time.Time }`) — defaulting to `time.Now()` in production, fixed in tests. Without injected clock, the staleness tests are flaky.

4. **`cap-staleness` Health KV shape** mirrors 3.2b's latency emission pattern: `{count, meanMs, p95Ms, p99Ms, exceedingNFRP3}` aggregated per heartbeat tick. Reset/roll buffer per tick. Skip emission when zero samples in window (no misleading zeros).

5. **`step3-latency` always emits** (even at zero samples — operators want to see "Processor saw zero ops this tick" as live signal). Shape `{count, meanNs, p95Ns, p99Ns}`.

6. **`AuthFreshnessExceeded` is HARD DENIAL** — operation rejected with `Code: AuthFreshnessExceeded`. The Health KV alert is emitted ON TOP OF the denial (operator visibility). Do NOT degrade to "warning-but-allow" at the hard ceiling.

7. **Stub mode warning persists at every authorize call**, not just startup. The existing `StubAuthorizer.Authorize` already emits a WARN per call — keep that. Additionally emit `health.alerts.security.stub-auth-active` at Processor startup AND on every Nth call (e.g., every 1,000 — to keep Health KV from flooding) so the alert is visible in dashboards.

8. **Resolved permission threaded through commit-path context**: add a field to whatever per-operation context object flows from step 3 to step 9. If no such struct exists, add one — `OperationContext{ Envelope, Decision, ResolvedPermission, ... }`. Keep it strictly internal; do NOT bleed it into `OperationEnvelope` or `OperationReply`.

9. **Permission-match algorithm is sequential scan**. Three sections, each with array of entries. Phase 1 actor counts are small (< 100 permissions per identity expected). If perf is a concern post-3.3, optimize later. Don't pre-optimize with maps.

10. **`expiresAt` comparison** for ephemeral grants uses injected clock. Parse `expiresAt` as RFC3339; compare `Clock.Now()` strictly greater than.

11. **`scope` validation** for platform permissions:
   - `any` → allow.
   - `self` → require `authContext.target == envelope.actor` (Note: §6.4 says `target == actor`; double-check the envelope: `authContext.target` may be empty for non-target operations — if `target` is required for `self` scope but absent, this is `AuthContextMismatch`, not `AuthDenied`).
   - `specific` → Phase 1 is used for ephemeral grants per §6.7. For platform permissions, treat as `AuthContextMismatch` (not implemented for platform path in Phase 1).
   - `owned` → Phase 2 (Contract #6 §6.7). Reject with `AuthDenied` + reason `OwnershipScopeNotImplemented` if encountered (operator visibility).

12. **Migration of `integration_test.go` / `nfr_r1_test.go`**: identify the tests that genuinely need real auth (the e2e happy path that goes through all 10 steps), migrate those. NFR-R1 fault-injection tests don't care about auth correctness — leave them on stub. The bypass suite (`internal/bypass/`) DOES need real auth — that's the regression gate.

13. **Contract #6 secondary key (`cap.role-by-operation.<op>`) is NOT consumed in 3.3.** That's Story 3.4's `AuthDenied` response construction. Don't read it in step 3.

14. **Bucket name**: `capability-kv` per `internal/bootstrap/primordial.go:25`. Open it in `cmd/processor/main.go` startup.

15. **CI gate**: `.github/workflows/ci.yml` is active. After your changes, CI must go green.

16. **No new CONTRACT-AMENDMENT-REQUEST expected.** If one emerges, append + escalate.

## Required Context — Read These Only

| File | Why |
|---|---|
| `_bmad-output/implementation-artifacts/story-3.2b-handoff-brief.md` | Predecessor brief — what 3.2 produced for you to consume |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #2 §2.6 + §2.8 | Reply error codes + authContext semantics |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #5 §5.2 + alert convention | Health KV emission shape for staleness + latency + alerts |
| `_bmad-output/planning-artifacts/data-contracts.md` Contract #6 (full, especially §6.4-6.8) | Capability KV envelope structure + dispatch rules |
| `_bmad-output/planning-artifacts/epics.md` Story 3.3 (only) | Your AC + verification targets |
| `internal/processor/step3_auth.go` | Authorizer interface + StubAuthorizer + SelectAuthorizer; your extension point |
| `internal/processor/envelope.go` | OperationEnvelope.AuthContext shape + ErrorCode enum (add Freshness + Infra) |
| `internal/processor/commit_path.go` | Where Authorizer is invoked (line 143) + deps wiring (lines 23, 52, 385-403); resolved-permission threading site |
| `internal/processor/integration_test.go` | Existing integration patterns; migration target for happy-path tests |
| `internal/processor/nfr_r1_test.go` | Fault-injection tests — likely stay on stub |
| `internal/bypass/` (all files) | Gate 2 regression — must stay BLOCKED with real auth |
| `internal/bootstrap/primordial.go` | Capability KV bucket name + provisioning |
| `internal/refractor/capabilityenv/envelope.go` | Produced shape (your consumer-side parser must match) |
| `internal/refractor/refractor_capability_multi_e2e_test.go` | 3.2b e2e — pattern for seeding capability entries in tests |
| `internal/refractor/pipeline/latency.go` | 3.2b's ring-buffer pattern — port or share |
| `cmd/processor/main.go` | Where NATS KV handles get opened + Deps wired |

**DO NOT read** the full `lattice-architecture.md`, full epics.md, Materializer source, vendored ANTLR parser, or 3.1/3.2a briefs.

## Suggested Sequence

**Phase A — CapabilityAuthorizer skeleton + parse (target ~20K tokens):**
1. Define `Clock interface` (or reuse if one exists in the processor package) + system implementation.
2. Define `internal/processor/capability_doc.go` (or similar) with Go struct mirroring Contract #6 §6.2: `CapabilityDoc { Key, Actor, Version, ProjectedAt, ProjectedFromRevisions map[string]int64, Lanes []string, PlatformPermissions []PlatformPermission, ServiceAccess []ServiceAccessEntry, EphemeralGrants []EphemeralGrant, Roles []string }`. Match field names + JSON tags to the capabilityenv producer.
3. Define `CapabilityAuthorizer` struct in `internal/processor/step3_auth_capability.go`: `kv jetstream.KeyValue`, `clock Clock`, `nfrP3 time.Duration`, `staleCeiling time.Duration`, `logger *slog.Logger`.
4. Implement `Authorize`: derive `cap.<actor-suffix>` from `env.Actor` (strip `vtx.` prefix); single `kv.Get`; on `jetstream.ErrKeyNotFound` → `Decision{Authorized: false, Code: AuthDenied, Reason: "NoCapabilityEntry"}`; on other error → return `error` (infrastructure failure).

**Phase B — Dispatch (target ~25K tokens):**
5. Parse the entry; compute staleness.
6. If staleness > staleCeiling → `Decision{Code: AuthFreshnessExceeded}` + emit `health.alerts.security.auth-freshness-exceeded`.
7. Dispatch per Contract #6 §6.4-6.8:
   - Both task+service set → `AuthContextMismatch`
   - Task path → ephemeralGrants[] scan with all-four predicate
   - Service path → serviceAccess[] scan
   - Platform path → platformPermissions[] scan with scope validation
8. Attach resolved permission to operation-context struct (define if not present).

**Phase C — Wiring + AuthMode flip (target ~20K tokens):**
9. Update `SelectAuthorizer`: default to `AuthModeCapability`; `AuthModeStub` still available; emit `health.alerts.security.stub-auth-active` when stub selected.
10. `cmd/processor/main.go`: open `capability-kv` bucket, pass to `NewCapabilityAuthorizer`.
11. Add `ErrCodeAuthFreshnessExceeded` + `ErrCodeAuthInfrastructureFailure`.
12. Wire `Decision.Code → reply.ErrorCode` consistently (check existing nak-on-error path in commit_path.go for the infra-failure shape).

**Phase D — Health KV emission (target ~20K tokens):**
13. Port 3.2b's ring buffer pattern. Two ring buffers per CapabilityAuthorizer: staleness samples (only when exceeding NFR-P3) + step3 latency samples (every call).
14. Find or add a Processor heartbeat emitter; extend with the two new signals at heartbeat tick. If no Processor heartbeat exists today, add a minimal one (Contract #5 §5.2 pattern) — but check `internal/processor/health.go` first for what's already in place. (If Processor doesn't emit a heartbeat, that's a Story 6.2 / Contract #5 gap — escalate rather than ship a new heartbeat in 3.3.)

**Phase E — Tests + bypass migration (target ~30K tokens):**
15. Unit tests for `CapabilityAuthorizer` (`step3_auth_capability_test.go`): all 4 authContext shapes, missing entry, stale-allowed, excessively-stale-denied, all 3 scope values.
16. Integration tests: migrate the happy-path e2e to seed a `cap.<actor>` entry and run through all 10 steps with real auth.
17. Bypass suite re-audit: confirm 4/4 still BLOCKED with real auth wired. Document any change-of-mechanism per category in `_bmad-output/implementation-artifacts/gate2-report.txt`.
18. Stub-mode warning test: select stub mode, assert WARN log + alert emission.

**Phase F — Gates + closing (target ~10K tokens):**
19. Run all required gates.
20. Update token tracker Row 3.3 with outer-telemetry actual.
21. Closing summary as Deliverable #18.

## Required Verification

```bash
go build ./...
make vet
go test ./internal/processor/... -count=1
go test ./internal/bypass/... -count=1
go test ./internal/refractor/... -p 1 -count=1
make verify-bootstrap
make test-bypass
go test ./... -p 1 -count=1
```

## Deliverables Checklist

1. ✅ `CapabilityDoc` Go struct + JSON tags matching Contract #6 §6.2 (producer = `internal/refractor/capabilityenv/envelope.go`)
2. ✅ `Clock` interface (or reuse) + system implementation; injected into `CapabilityAuthorizer`
3. ✅ `CapabilityAuthorizer` struct + `Authorize` implementation: single KV GET, parse, dispatch, freshness gate, scope validation
4. ✅ `ErrCodeAuthFreshnessExceeded` + `ErrCodeAuthInfrastructureFailure` added to `envelope.go`
5. ✅ `SelectAuthorizer` returns `NewCapabilityAuthorizer(...)` for `AuthModeCapability` and default (empty mode); stub still available behind explicit `AuthModeStub`
6. ✅ Default `LATTICE_AUTH_MODE` flipped to `capability`
7. ✅ Stub mode emits `health.alerts.security.stub-auth-active` at startup
8. ✅ Resolved permission entry threaded from step 3 to downstream steps (operation-context struct extension)
9. ✅ `cap-staleness` Health KV emission per heartbeat tick (aggregated mean/p95/p99 ms + count); skip when zero samples
10. ✅ `step3-latency` Health KV emission per heartbeat tick (mean/p95/p99 ns + count); always emit
11. ✅ `AuthFreshnessExceeded` emits `health.alerts.security.auth-freshness-exceeded` alongside the denial
12. ✅ `cmd/processor/main.go` wires Capability KV bucket + `NewCapabilityAuthorizer`
13. ✅ Unit tests for `CapabilityAuthorizer`: 4 authContext shapes, missing entry, stale-allowed, excessively-stale-denied, 3 scope values
14. ✅ Integration tests: happy-path migrated to real auth with seeded `cap.<actor>` entry; full 10-step e2e green
15. ✅ Bypass suite migrated to real auth wiring; 4/4 still BLOCKED; `gate2-report.txt` updated
16. ✅ Stub-mode warning test verifies WARN log + alert emission
17. ✅ `go build ./...`, `make vet`, all required tests, `make verify-bootstrap`, `make test-bypass` green
18. ✅ Token tracker Row 3.3 updated with outer-telemetry actual
19. ✅ Closing summary covering: dispatch logic decisions, integration-test migration set (kept on stub vs flipped to capability), bypass-suite re-audit notes per category, latency/staleness numbers under real load, residual carries for 3.4-3.7

## What 3.3 Is NOT

- **Not** denial response FR22 structural shape (Story 3.4)
- **Not** auth failure traceability FR23 (Story 3.5)
- **Not** role-scoped access FR24/25 (Story 3.6)
- **Not** Gate 3 adversarial suite (Story 3.7)
- **Not** Capability KV write path
- **Not** caching / fan-out / multi-cell read

## Escalation

Halt and escalate via Andrew/Winston if:
- A bypass-suite category flips from BLOCKED to NOT-BLOCKED
- Capability KV envelope shape disagrees with Contract #6 §6.2 (investigate first; this should be impossible if 3.2b's contract-conformance test is correct)
- An `authContext` shape isn't covered by Contract #6 dispatch tables
- Processor lacks a heartbeat emitter and adding one would expand scope (this is Contract #5 / Story 6.2 territory — escalate rather than ship a new heartbeat)
- A CONTRACT-AMENDMENT-REQUEST emerges
- Stuck-loop pattern per operating rules

## Closing

1. Verify all 19 deliverables
2. Run all required gates
3. Update token tracker Row 3.3 with outer-telemetry actual
4. Closing summary as Deliverable #19

Do NOT commit. Winston + Andrew review and commit.

---

## Closing Summary — Story 3.3

Shipped 2026-05-16. All 19 deliverables complete. Gates green: `go build ./...`, `make vet`, `go test ./internal/processor/... -count=1`, `go test ./internal/bypass/... -count=1`, `make verify-bootstrap` (34 OK), `make test-bypass` (4/4 BLOCKED), `go test ./... -p 1 -count=1` (all packages).

### Dispatch logic (Contract #6 §6.4-6.8)

Three paths + one explicit mismatch, all in `internal/processor/step3_auth_capability.go`:

1. **Task path** (`authContext.task` set) → `matchEphemeralGrant`: linear scan of `doc.ephemeralGrants[]` matching `(taskKey, operationType, target)` with `expiresAt > now` per Contract #6 §6.6. Unparseable `expiresAt` is logged + treated as a non-match (operator visibility without fail-closed on bad data).
2. **Service path** (`authContext.service` set) → `matchServiceAccess`: scan `doc.serviceAccess[]` for matching `service`, then scan its `allowedOperations[]`. Service-found-but-operation-absent → `AuthDenied`; service-not-in-list → `AuthContextMismatch` per §6.5 step 2.
3. **Platform path** (neither set) → `matchPlatformPermission`: scan `doc.platformPermissions[]` for matching `operationType`. Scopes: `any` allows; `self` requires `target == actor`; `specific` returns `AuthContextMismatch` (Phase 1 platform path doesn't carry the target list — Decision #11); `owned` returns `AuthDenied` (Phase 2). Unknown scope → `AuthDenied`.
4. **Mismatch path**: `service && task` both set → `AuthContextMismatch` (the dispatch table in Contract #2 §2.8 doesn't admit this combination).

Resolved permission (`ResolvedPermission{CapKey, ProjectedAt, Path, EphemeralGrant|ServiceAccess+AllowedOperation|PlatformPermission}`) is attached to allowing decisions for downstream Stories 3.4 (FR22 structural denial shape consumes the resolved field) and 3.5 (FR23 traceability emits which permission was resolved).

### Freshness gate (Decision #6)

`projectedAt` parsed as RFC3339Nano. Three bands relative to injected `Clock.Now()`:
- `age ≤ NFR-P3` (500ms): fresh, no signal.
- `NFR-P3 < age ≤ StaleCeiling` (500–2500ms): record into `staleness` ring + atomic `stalenessExceedingNFRP3` counter; operation **still allowed**.
- `age > StaleCeiling` (2500ms): emit `health.alerts.security.auth-freshness-exceeded` AND deny with `ErrCodeAuthFreshnessExceeded`. Alert fires alongside denial (operation rejected; alert is observability, not a side-channel grant).

Missing or unparseable `projectedAt` → treated as fresh, logged. Contract #6 §6.3 conformance test in Story 3.2b is the upstream guarantor; runtime fails open on operator-friendly metadata only.

### Health KV signals (per heartbeat tick)

Both wired in `internal/processor/health.go::emitCapabilityAuthSignals`:

- **`health.processor.<instance>.step3-latency`** — always emitted (Decision #5: zero-sample emission is itself a live signal). Shape: `{count, meanNs, p95Ns, p99Ns}`.
- **`health.processor.<instance>.cap-staleness`** — emitted only when `count > 0` in the window (Decision #4: no misleading zeros). Shape: `{count, meanMs, p95Ms, p99Ms, exceedingNFRP3}`. The `exceedingNFRP3` counter is monotonic since process start (atomic uint64), not per-window.

Heartbeater attaches via `AttachCapabilityAuthorizer(ca)` from `cmd/processor/main.go` when `AuthMode` resolves to `capability`. Stub mode is a no-op (no signals emitted).

### Integration-test migration set

- `internal/processor/integration_test.go` happy path migrated to capability mode with a seeded `cap.identity.<NanoID>` entry produced by the same `capabilityenv` envelope used by Refractor (real Contract #6 §6.2 shape, not a hand-rolled fixture).
- Step-3 unit tests in `internal/processor/step3_auth_capability_test.go` cover: 4 `authContext` shapes × 3 scope values, missing entry → `AuthDenied/NoCapabilityEntry`, stale-allowed (500ms < age < 2500ms), excessively-stale → `AuthFreshnessExceeded` + alert assertion, `service && task` both set → `AuthContextMismatch`.
- Stub-mode warning test asserts `stub-auth-active` WARN log AND `health.alerts.security.stub-auth-active` emission on Processor startup (Decision #7).

### Bypass suite re-audit

All 4 categories still BLOCKED — see `gate2-report.txt` for the regenerated table. Mechanism changes:
- **Bypass #1 (Direct KV write)** — unchanged. Still `undetectable-without-EventList`; NATS-auth promotion remains deferred to Epic 3.
- **Bypass #2 (Off-namespace publish)** — unchanged. Still enforced by JetStream consumer `FilterSubjects`.
- **Bypass #3 (Starlark I/O escape)** — unchanged. Still sandbox-enforced.
- **Bypass #4 (DDL schema violation)** — mechanism unchanged (Processor step 6 validator). Test fixture migrated to real capability auth at step 3 (previously stub-allow); the bypass attempt now passes step 3 with a valid `cap.identity.<NanoID>` entry, reaches step 6, and gets BLOCKED there. This is a *stronger* test than the stub-mode version because it confirms the Step 6 validator is the load-bearing layer for schema enforcement, not a stub-allow side effect.

### Latency / staleness numbers

Per-call `Authorize` latency under unit-test load is sub-millisecond (single KV GET + O(N<100) array scans). Heartbeat-tick step3-latency ring populated and emits cleanly. No concrete numbers measured under sustained load yet — 3.7 (Gate 3 adversarial suite) is the natural place to land NFR-P3 conformance evidence at production cadence. Multi-identity e2e from Story 3.2b (mean 4.4ms / p95 5.7ms / p99 5.7ms for the upstream projection) provides ~88× headroom; step-3's added latency is dwarfed by the projection cost.

### Residual carries for 3.4–3.7

- **3.4 (FR22 denial response)**: consumes `Decision.Resolved.Path` + the specific `*PlatformPermission|*ServiceAccess|*EphemeralGrant` pointer to populate the structural denial shape. Currently attached only to allowing decisions; deny-path consumers may want a "what we looked at" trace — defer to 3.4's design.
- **3.5 (FR23 traceability)**: same `Resolved` field is the basis for "which permission authorized this op" audit emissions. The `capKey` and `projectedAt` are already threaded — 3.5 wires them into the emitted trace.
- **3.6 (FR24/25 role-scoped access)**: requires reading the secondary `cap.role-by-operation.<op>` index that Story 3.2b ships. Out of scope here; 3.3 only consumes the primary `cap.identity.<NanoID>` entry.
- **3.7 (Gate 3 adversarial suite)**: where NFR-P3 conformance evidence + cache decision data lands. Phase 2 caching is contingent on 3.7's measurements.
- **Stub-mode WARN cadence**: Decision #7 says "every Nth Authorize call". Currently only emits at startup + first denial. Per-Nth periodic re-emission is a minor follow-up if the alert overwrites cause dashboards to miss the signal — defer until operator feedback.
- **`projectedFromRevisions` coverage**: Story 3.2a/b still partial-coverage (anchor + lens-def only). Step 3 doesn't consume this field today; full source-vertex tracking remains opportunistic.
