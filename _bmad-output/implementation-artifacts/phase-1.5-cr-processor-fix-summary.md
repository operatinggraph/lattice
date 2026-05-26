# Processor CR Fix Summary — Phase 1.5

All fixes applied. `go build ./...`, `go vet`, and `go test ./internal/processor/... -p 1 -count=1` pass cleanly.

---

## P1 Fixes

| Finding | File:Line | Description |
|---------|-----------|-------------|
| P1-001 | `internal/processor/steps_4_10_stub.go:41-56`, `step8_commit.go:192`, `step9_publish.go:83` | `CommitAck` now carries `Events EventList`; `EventPublisher.Publish` signature changed to accept `EventList`; `CommitterImpl.Commit` builds the list once and returns it; `step9` uses it directly — no second `NewNanoID()` call |
| P1-002 | `internal/processor/commit_path.go:147-151,293-314,459-540` | Added `maybeRepublishEvents` + `markEventsPublished` helpers; dedup short-circuit (step-2 hit) calls `maybeRepublishEvents` before acking; same call added to the concurrent-redelivery dedup path; step-9 success writes `eventsPublishedAt` to tracker via `KVPut`; misleading "step 9 will run again from a clean state" comment replaced with accurate description |
| P1-003 | `internal/processor/step1_consume.go:41-50` | `applyDefaults` `FilterSubjects` changed from `["ops.default.>", ...]` to `["ops.default", "ops.urgent", "ops.system", "ops.meta"]` — two-segment form matching all production publishers |
| P1-004 | `internal/processor/health.go:101-109`, `cmd/processor/main.go:107-109` | Added `HealthHeartbeater.SetInterval(d time.Duration)` method; `main.go` now calls `hb.SetInterval(...)` on the correctly-wired heartbeater from `MakePipeline` instead of constructing a new broken one |

---

## P2 Fixes

| Finding | File:Line | Description |
|---------|-----------|-------------|
| P2-003 | `internal/processor/ddl_cache.go:301-321` | `Invalidate` now acquires the write lock once at the top and holds it across the KV read and map update — eliminates the TOCTOU window |
| P2-004 | `internal/processor/commit_path.go:159` | Auth infrastructure failures now use `ErrCodeAuthInfrastructureFailure` instead of `ErrCodeInternalError` |
| P2-005 | `internal/processor/envelope.go:193` | Removed `dec.DisallowUnknownFields()` from `ParseEnvelope`; corresponding test renamed to `TestParseEnvelope_ToleratesUnknownFields` and inverted to assert leniency |
| P2-006 | `internal/processor/step9_publish.go:155-169` | Increments `attempt` immediately after failure; breaks out of retry loop before sleeping when `attempt >= MaxRetries` — eliminates 800ms dead sleep after final attempt |

---

## Nit Fixes

| Finding | File:Line | Description |
|---------|-----------|-------------|
| Nit-001 | `cmd/processor/main.go:72` | No change needed — default filter CSV was already `ops.default,...` (two-segment), now consistent with updated `applyDefaults` |
| Nit-002 | `internal/processor/operation_context.go:11,42` | Removed `import "context"` and `var _ = context.Background`; rewrote file header as current-state |
| Nit-003 | `internal/processor/starlark_builtins.go:160-165` | Strengthened `constant_time_equal` comment to make explicit that both operands must be fixed-length and that variable-length secrets must not use this builtin |
| Nit-004 | `internal/processor/reply.go:25-34` | Removed `BuildAcceptedReplyWithRevisions` (dead production code) |
| Nit-005 | `internal/processor/commit_path.go:553-556` | Replaced misleading "stub" + "backwards compatibility" doc on `MakeStubPipeline` with accurate description — "stub" now refers to absent Capability KV integration, not stub implementations |

---

## History Comments

All `Story X.Y` references removed or rewritten as current-state across all processor source files:

| File | Change |
|------|--------|
| `tracker.go:25-29` | Rewrote tracker comment as current-state (full Contract #4 shape including `eventsPublishedAt`) |
| `script_context.go:45-48` | Rewrote MetaVertex comment — removed "Story 1.10 will expand" (DDL cache already exists) |
| `step3_auth.go` | Removed all Story X.Y annotations; rewrote informational ones as current-state |
| `step4_hydrate.go` | Removed all Story X.Y annotations |
| `step5_execute.go` | Removed Story X.Y header |
| `step6_validate.go` | Removed Story X.Y header |
| `step7_events.go` | Removed Story X.Y annotations |
| `step8_commit.go` | Removed Story X.Y annotations |
| `step9_publish.go` | Updated `PublicationError` comment to reflect actual dedup-hit re-publish behavior |
| `step10_ack.go` | Removed Story X.Y annotations |
| `commit_path.go` | Removed ~15 Story X.Y inline annotations; rewritten as current-state |
| `ddl_cache.go` | Removed Story X.Y annotations |
| `health.go` | Removed Story X.Y annotations |
| `health_alerts.go` | Rewrote file header |
| `latency_ring.go` | Rewrote file header |
| `starlark_builtins.go` | Removed Story X.Y annotation |
| `starlark_runner.go` | Updated nolint comment to remove story ref |
| `step3_auth_capability.go` | Removed Story X.Y annotations |
| `step3_auth_trace.go` | Removed Story X.Y file header and inline refs |
| `step3_denial_response.go` | Removed Story X.Y file header; updated inline comment |
| `steps_4_10_stub.go` | Rewrote interface + stub comments as current-state |
| `operation_context.go` | Rewrote entire file header |
| `envelope.go` | Removed Story X.Y from `Class` field doc and `ErrorCode` comments |
| `reply.go` | Removed Story X.Y from function docs |
| `capability_doc.go` | Removed Story X.Y reference |
| `doc.go` | Replaced "Story 1.5 scope" stub description with accurate 10-step commit path listing |
| `cmd/processor/main.go` | Removed Story X.Y from package doc and inline comment |
| `step3_auth.go:107` (log string) | Stripped story refs: now `"STUB AUTH: allow-all; set LATTICE_AUTH_MODE=capability to enable Capability KV auth"` |

---

## Deferred (per mandate)

- P2-001 `OperationReply.Revisions` — deferred to Story #11
- P2-002 DDL cache no-evict on tombstone — deferred to Story #12

---

## Test changes

- `internal/processor/envelope_test.go`: `TestParseEnvelope_RejectsUnknownFields` renamed to `TestParseEnvelope_ToleratesUnknownFields`; inverted to assert leniency (matches P2-005)
- `internal/processor/step9_publish_test.go`: Updated 3 test call sites to pass `EventList` to `Publish` (matches new interface)
- `internal/processor/nfr_r1_test.go:601`: Updated `nfrEventPub.Publish` signature to accept `EventList` (matches new interface)
