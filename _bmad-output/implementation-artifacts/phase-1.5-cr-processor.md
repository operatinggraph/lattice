# Processor CR Report — Phase 1.5

## Summary
- Files reviewed: 27 non-test `.go` files under `internal/processor/` and `cmd/processor/main.go`
- P0 findings: 0
- P1 findings: 4
- P2 findings: 6
- Nit findings: 5
- History comments: pervasive (see section below)

---

## P1 Findings

### [P1-001] Event IDs in tracker diverge from event IDs published to `core-events`
**File:** `internal/processor/step8_commit.go:103` and `internal/processor/step9_publish.go:85`
**What:** `BuildEventList` is called independently in both `CommitterImpl.Commit` (step 8, for tracker `data.eventClasses`) and `EventPublisherImpl.Publish` (step 9, for the actual NATS publish). Each call invokes `substrate.NewNanoID()` which uses `crypto/rand` — fully random on every call. The two invocations produce entirely different `eventId` values for the same operation.
**Impact:** The idempotency tracker (`vtx.op.<requestId>.data.eventClasses`) records event class names but the eventId values that land in `core-events` are different from anything stored in the tracker. Operators cannot correlate "which event IDs correspond to this operation" from the tracker alone. The compensating-operation contract (Story 5.3, `OperationReply.Revisions`) cannot be cross-referenced against published events. Event deduplication and audit are undermined.
**Suggested fix:** Build the `EventList` exactly once — either in step 7 (returned as part of the validated result) or once in step 8 and pass the built list to step 9, never rebuilding. A `ValidationResult` struct carrying `ScriptResult + EventList` threads this cleanly through the pipeline.

---

### [P1-002] Step-9 nak → redelivery → step-2 short-circuit: events permanently never published
**File:** `internal/processor/commit_path.go:296–301` (comment at line 289–293)
**What:** The comment at line 289 claims "step 9 will run again from a clean state" on redelivery. This is factually wrong. After step 8 commits (tracker written), a step-9 failure causes a nak. JetStream redelivers. Step 2 runs `CheckDedup` → `DedupDuplicate` (tracker exists) → sends a `duplicate` reply and ACKs. Step 9 **never runs again**. The 3 attempts inside one delivery are the only attempts ever made.
**Impact:** Any persistent `core-events` outage lasting longer than the MaxRetries window (~1 050ms total) permanently loses the event fan-out for a committed operation. The mutation is durable but the events for that operation are silently dropped. No health marker is emitted for this condition; the caller receives a `duplicate` reply (not an `accepted` reply) and has no signal that events were skipped.
**Suggested fix (options):**
1. Before step-2 short-circuit, check if the tracker has a "events published" flag; if not, re-run step 9 before acking. This requires a tracker schema addition.
2. Change the dedup short-circuit path to attempt a best-effort re-publish of events (using the tracker's `eventClasses`) before acking.
3. Document explicitly that post-commit event loss is accepted until a dedicated event-replay mechanism lands (M5/M6). Remove the misleading "will run again" comment.

---

### [P1-003] `applyDefaults` filter subjects do not match the actual publish subject pattern
**File:** `internal/processor/step1_consume.go:49`
**What:** `applyDefaults` sets `FilterSubjects = []string{"ops.default.>", "ops.urgent.>", "ops.system.>", "ops.meta.>"}`. Every production publisher (submit.go:43, candidates.go:234, lens.go, examples/hello-lattice, testutil/pipeline.go:182) publishes to `"ops." + lane` — a **two-segment** subject (`ops.default`, `ops.urgent`, etc.). NATS wildcard `>` matches **one or more** trailing segments; `ops.default.>` matches `ops.default.anything` but **not** `ops.default` itself. Any caller of `EnsureConsumer` with an empty `FilterSubjects` (relying on `applyDefaults`) gets a consumer that silently discards all operations.
**Impact:** `applyDefaults` fires when `FilterSubjects` is left empty. Currently `cmd/processor/main.go` always passes a non-empty filter (so production is unaffected). But any test or external package that calls `EnsureConsumer` with `ConsumerConfig{FilterSubjects: nil}` — expecting the defaults — will create a consumer that never receives any messages. Two integration tests (`nfr_r1_test.go`) pass explicit `FilterSubjects: []string{"ops.default"}` which is correct; the defaults are simply wrong.
**Suggested fix:** Change `applyDefaults` to use `[]string{"ops.default", "ops.urgent", "ops.system", "ops.meta"}` (without `.>`), or — if multi-segment subjects are an intended future design — update all publishers to use `ops.default.<operationType>` and update `main.go`'s default CSV accordingly.

---

### [P1-004] `HEALTH_INTERVAL_SEC > 10` silently runs a broken heartbeater (nil metrics, no CapabilityAuthorizer)
**File:** `cmd/processor/main.go:108–123`
**What:** When `HEALTH_INTERVAL_SEC > 10`, line 109 reassigns `hb` to a freshly constructed `HealthHeartbeater` with `nil` metrics and no `capAuthorizer` attached. `_ = hb` on line 113 is a no-op (the variable is still the new heartbeater). Line 123 then runs `hb.Run(ctx)` — the new broken one. The correctly-wired heartbeater from `MakePipeline` is **never started**. Result: `ops_committed_total`, `ops_rejected_total`, and all other Processor metrics report zero in Health KV for the lifetime of the process; step-3 latency and cap-staleness signals are also absent.
**Impact:** Any operator who sets `HEALTH_INTERVAL_SEC=30` (or any value above 10) to reduce KV write frequency gets a broken health plane. Metrics dashboards show permanently-zero counters. NFR-O1 observability is silently degraded.
**Suggested fix:** Either export a `SetInterval` method on `HealthHeartbeater` so the interval can be adjusted without replacement, or expose `Deps.Metrics` so the replacement heartbeater can be wired correctly. Simplest: expose `(*HealthHeartbeater).Interval` as a field and set it before calling `Run`.

---

## P2 Findings

### [P2-001] `OperationReply.Revisions` is always nil — schema promises unfulfilled
**File:** `internal/processor/reply.go:25–34`, `internal/processor/commit_path.go:313`
**What:** `OperationReply` documents `Revisions map[string]uint64` as "per-key revision map returned by the substrate after a successful atomic batch. Useful for client RYOW polling." `BuildAcceptedReplyWithRevisions` exists to populate it. However, `commit_path.go` calls `BuildAcceptedReplyWithDetail` (not `BuildAcceptedReplyWithRevisions`), and `substrate.BatchAck` does not carry per-key revisions (only stream/sequence/batchID). The field is always nil. `BuildAcceptedReplyWithRevisions` is dead code in production.
**Impact:** Clients that rely on `reply.Revisions` for RYOW polling (as documented in the reply schema) silently get nil and must fall back to polling by `OpTrackerKey`. The compensating-operation spec (Story 5.3) references revisions as inputs to compensating op templates — this gap must be resolved before Story 5.3 ships.
**Suggested fix:** Either: (a) remove the `Revisions` field until `substrate.AtomicBatch` returns per-key revisions, (b) have the Committer perform a follow-up read to recover the committed revisions, or (c) document the gap explicitly as M5 work and make `BuildAcceptedReplyWithRevisions` obviously dead (unexport or remove it).

---

### [P2-002] DDL cache does not evict tombstoned meta-vertices after `TombstoneMetaVertex` commit
**File:** `internal/processor/ddl_cache.go:150–255` (`loadMetaVertex`), `internal/processor/step8_commit.go:174–183`
**What:** After a `TombstoneMetaVertex` operation commits at step 8, `CommitterImpl` calls `DDLs.Invalidate(ctx, "vtx.meta.<id>")`. `Invalidate` calls `loadMetaVertex`, which reads the root doc. The tombstoned doc has `isDeleted: true` but `loadMetaVertex` does not check `isDeleted`. If `canonicalName` is still present on the doc, the entry is re-loaded with all its DDL constraints intact — the cache retains the tombstoned DDL indefinitely.
**Impact:** Operations targeting a tombstoned class continue to be hydrated and executed (the script source is still cached) rather than receiving `NoDDLForClass`. This is the documented M5/M6 gap, confirmed at the code level.
**DDL cache read locations (M5/M6 invalidation gap inventory):**
- `step4_hydrate.go:83`: `h.DDLs.Lookup(class)` — class resolution
- `step6_validate.go:112`: `v.DDLs.Lookup(class)` — permittedCommands + sensitive scope check
- `step8_commit.go:174`: `hasMetaVertexMutation` → `DDLs.Invalidate` — post-commit refresh
- `ddl_cache.go:258`: `c.byName[canonicalName]` — `Lookup`
- `ddl_cache.go:272`: `c.byMetaPK[metaKey]` — `LookupByMetaKey`
**Suggested fix (M5):** In `loadMetaVertex`, after reading the root doc, check `rootDoc.isDeleted`; if true, return `(ref, false, nil)` so the entry is evicted from the cache.

---

### [P2-003] DDL cache `Invalidate` has a TOCTOU window between the prior-name read and the re-insert
**File:** `internal/processor/ddl_cache.go:301–320`
**What:** `Invalidate` acquires the mutex to read `priorName`, releases it, performs a (lock-free) `loadMetaVertex` KV read, then re-acquires the mutex to update the maps. If two concurrent `Invalidate` calls target the same key, both read the same `priorName`, both `loadMetaVertex` at slightly different times, and both re-insert. The second write wins. This is safe when both KV reads see the same committed state. It is NOT safe if a second DDL mutation commits between the two KV reads: the cache ends up with the intermediate state from the first read's perspective but indexed under the new canonical name.
**Impact:** Rare race in M5/M6 DDL-heavy scenarios. Low probability but could produce a cache entry pointing to stale script source after a rapid DDL update-then-update sequence.
**Suggested fix:** Hold the write lock for the entire `Invalidate` operation (including the KV read), or use a single-writer channel/goroutine for cache updates.

---

### [P2-004] Auth infrastructure failures reported as `InternalError` instead of `AuthInfrastructureFailure`
**File:** `internal/processor/commit_path.go:158–163`
**What:** When `Authorizer.Authorize` returns a non-nil error (e.g., NATS KV unreachable), `commit_path.go` builds a rejected reply with `ErrCodeInternalError`. `ErrCodeAuthInfrastructureFailure` exists precisely for this case (see comment at `envelope.go:95–102`) but is never used. The distinction matters for clients: `InternalError` signals "retry later" while `AuthInfrastructureFailure` could signal "the auth plane is down".
**Suggested fix:** Use `ErrCodeAuthInfrastructureFailure` in the authorizer-error branch at `commit_path.go:160`.

---

### [P2-005] `ParseEnvelope` uses `DisallowUnknownFields` — forward-incompatible with contract-additive fields
**File:** `internal/processor/envelope.go:193`
**What:** `ParseEnvelope` sets `dec.DisallowUnknownFields()`. Any future contract-additive envelope field (a new top-level key added to the `OperationEnvelope` schema) will cause all existing Processor deployments to reject the envelope with `EnvelopeMalformed` until they are upgraded. `CapabilityDoc` explicitly comments that it does NOT use `DisallowUnknownFields` for this reason (capability_doc.go:15). The envelope is the highest-traffic hot path.
**Impact:** Rolling upgrades and client-ahead-of-processor deployments are fragile. Any new field added to `OperationEnvelope` requires a synchronized deploy of all Processor instances before any client can use it.
**Suggested fix:** Remove `DisallowUnknownFields`. Enforce strictness in the conformance test layer (contract tests), not the runtime hot path.

---

### [P2-006] Step-9 backoff sleeps 800 ms **after** the final failed attempt before returning
**File:** `internal/processor/step9_publish.go:111–153`
**What:** The retry loop increments `attempt` and sleeps **before** checking the loop condition again. On the third (final) attempt failure (`attempt == 2`), the code sleeps `BackoffSchedule[2] = 800ms` then increments to `attempt = 3`, then exits the loop. The 800ms sleep is dead time — no further attempt will be made.
**Impact:** A permanent `core-events` outage causes the commit path to wait an extra 800ms before returning `OutcomeRetryable`, increasing nak-latency and stalling the consumer's MaxAckPending window unnecessarily.
**Suggested fix:** Break out of the loop immediately after the final retry fails, before the last sleep. Standard pattern: check `if attempt+1 < p.MaxRetries` before sleeping.

---

## Nit Findings

### [Nit-001] `main.go` default filter CSV inconsistent with `applyDefaults` format
**File:** `cmd/processor/main.go:72`
**What:** `PROCESSOR_FILTER` default is `"ops.default,ops.urgent,ops.system,ops.meta"` (no `.>` suffix). `applyDefaults` uses `"ops.default.>"` etc. These produce different filter subjects. The comment at `main.go:16` in the env-variable doc block also omits the `.>`. Whoever reads both will be confused about which form is authoritative.

---

### [Nit-002] `operation_context.go` keeps an import live with `var _ = context.Background`
**File:** `internal/processor/operation_context.go:42`
**What:** `var _ = context.Background` is used to keep the `"context"` import from being removed by goimports. This is the wrong approach — if the package legitimately doesn't need `context`, the import should be removed. If it anticipates needing it, the comment should say so.

---

### [Nit-003] `crypto.constant_time_equal` leaks string length via early return
**File:** `internal/processor/starlark_builtins.go:162–163`
**What:** `subtle.ConstantTimeCompare` compares content in constant time, but the implementation returns `False` immediately on length mismatch. This leaks the length of the secret (one operand) to callers who can time the comparison. The comment acknowledges this but marks it "acceptable Phase 1" without a tracking note. For claim-key comparison (its primary use), both operands are fixed-length NanoIDs so this is low-risk; document the constraint more explicitly to prevent future misuse with variable-length secrets.

---

### [Nit-004] `BuildAcceptedReplyWithRevisions` is dead production code
**File:** `internal/processor/reply.go:25–34`
**What:** This function is never called in production code paths. It exists in anticipation of a `substrate.AtomicBatch` API that returns per-key revisions (which doesn't exist yet). Should be either marked `// TODO(M5): wire when substrate returns per-key revisions` or removed until needed, to avoid appearing in code searches as a live code path.

---

### [Nit-005] `MakeStubPipeline` name is a misnomer post-Story-1.8
**File:** `internal/processor/commit_path.go:477`
**What:** `MakeStubPipeline` is documented as retaining the name "for backwards compatibility" but wires the real Hydrator, Executor, Validator, and Committer. Only the event publisher uses a stub. The name misleads reviewers into thinking the pipeline is partially mocked when it is nearly production-identical. The `_ = hb` comment inside `main.go` references "Story 1.5" further reinforcing an outdated mental model.

---

## History Comments

The processor codebase has pervasive `// Story X.Y` annotations throughout. By the criteria in the review mandate:

**Actively misleading (P2 severity):**
- `internal/processor/commit_path.go:289–293` — Comment says "step 9 will run again from a clean state" on redelivery. This is factually wrong (see P1-002). The misleading comment elevates to P1.
- `internal/processor/tracker.go:27` — "Story 1.6/1.7 will replace this with the full Contract #4 shape" — the full shape is implemented; the comment reads as if the work is pending.
- `internal/processor/script_context.go:46–47` — "Story 1.10 will expand this when the DDL cache lands" — the DDL cache landed in Story 1.7.

**Informational (Nit severity):** All other `// Story X.Y` annotations — there are roughly 80+ occurrences across `step3_auth.go`, `step4_hydrate.go`, `step6_validate.go`, `step8_commit.go`, `step9_publish.go`, `step10_ack.go`, `ddl_cache.go`, `commit_path.go`, `health_alerts.go`, `envelope.go`, `reply.go`, `steps_4_10_stub.go`, `latency_ring.go`, `starlark_runner.go`, and `cmd/processor/main.go`. These are contextual breadcrumbs that become dead weight as the project matures. No immediate action required, but a one-time `// Story` comment purge is recommended before M5 to reduce cognitive overhead.

Notable sub-cases:
- `step3_auth.go:107`: `"STUB AUTH: allow-all (Story 1.5; replaced by Capability KV in Story 3.3)"` — this string leaks into production log output on every stub-mode Authorize call. The story references are harmless in logs but the message itself is correct.
- `cmd/processor/main.go:112`: `"// constant 10s for Story 1.5."` — stale; the story is complete and the comment misdirects the reader about the heartbeat bug described in P1-004.

---

## NFR-S10 Verification: AI agents use same commit path as humans

**Confirmed.** `internal/aiagent/` is out of scope for this CR but the Processor's `commit_path.go` has no AI-agent-specific branches. All messages arrive via JetStream from `ops.*` subjects regardless of origin. The auth check at step 3 (`Authorizer.Authorize`) is invoked unconditionally for every envelope. No shortcut, bypass flag, or header-based auth skip exists. NFR-S10 compliance confirmed for the Processor.

---

## Acceptance Auditor: Reply Schema Completeness

### OperationReply fields vs `submit.go` consumption
`cmd/lattice/output/submit.go` JSON-unmarshals the reply into `processor.OperationReply`. All fields are present.

| Status | Fields populated | Gap |
|--------|-----------------|-----|
| `accepted` | `requestId`, `opTrackerKey`, `status`, `committedAt`, `decision="committed"`, `detail` (when script returns response) | `revisions` always nil — see P2-001 |
| `duplicate` | `requestId`, `opTrackerKey`, `status`, `originalCommittedAt` | None |
| `rejected` | `requestId`, `opTrackerKey=""`, `status`, `error.{code, message, details}` | `opTrackerKey` is empty string (not nil) — wire-shape ok but slightly inconsistent |

### `requestId` echoing
Confirmed correct on all paths: `BuildAcceptedReply`, `BuildDuplicateReply`, `BuildRejectedReply`, and `maybeReplyMalformed` all echo `env.RequestID`. The best-effort path (`maybeReplyMalformed`) uses `extractRequestIDBestEffort` which may return `""` — callers receive `requestId: ""` in that case (documented behavior, not a bug).

### Story 5.3 compensating-op contract (Revisions)
`OperationReply.Revisions` is documented as the source of per-key revision data for compensating op templates. It is never populated (P2-001). Story 5.3 depends on this field; the gap must be resolved before that story ships.

---

## Adversarial Coverage Notes

**Covered:**
- Race between hydrate (step 4) and commit (step 8): not a Processor-internal race — the substrate's atomic batch handles CAS; unconditioned updates are documented as a known gap (Story 1.9+, see step8_commit.go:139).
- Reply not sent on validation failure: all `handleStubFailure` paths call `replyTo` before `TermWithReason`. Confirmed for DDLViolation (line 229), hydration, execute, and validate branches.
- Reply after commit but before step-10 ack: `replyTo` is called at line 313, `acker.Ack` at line 317. If ack fails, caller already has the reply (OutcomeAccepted returned, comment at line 319 documents this).
- Empty envelope: `parseEnvelopeFromMessage` checks `len(m.Data()) == 0` → `EnvelopeMalformed`. `ParseEnvelope` additionally validates all required fields. Correct.
- Missing `Class` field: `resolveClass` returns error → `HydrationError{Code: "MissingClass"}` → `handleStubFailure` → rejected reply.
- Idempotency collision (concurrent redelivery): commit_path.go:246–255 probes tracker post-rejection and emits duplicate reply + ack. Correct.
- Auth freshness exactly at ceiling: `checkFreshness` uses `age > a.cfg.StaleCeiling` (strictly greater). A request arriving exactly at 2500ms is allowed. Borderline intentional — operators see the `cap-staleness` signal. Documented.
- `ops.meta` TombstoneMetaVertex mid-flight against same key: no special handling — the atomic batch uses revision conditions. If two operations target the same key concurrently, one gets `RevisionConflict`. Correct.
- Consumer redelivery after partial processing: step-2 tracker dedup handles this for post-step-8 redeliveries. Pre-step-8 redeliveries are idempotent (no state was written).
- Goroutine leak (`TraceEmitter`): goroutines use `context.WithTimeout(context.Background(), 5s)` — bounded lifetime, not a leak. Up to 5s drain after process shutdown.
- `replySubject` when neither header nor msg.Reply() is set: returns `""` → `replyTo` is a no-op (line 419 `if subject == ""`). Silent — fire-and-forget publishers get no reply. Correct per contract.

**Not fully covered (deferred to M5/M6):**
- DDL cache stale-hit (substrate-direct package install bypassing Processor): fully a known gap, documented at two locations in `ddl_cache.go`. No in-band signal exists; an out-of-band `ddls.Refresh()` call or process restart is the current mitigation.
- Cross-operation race for the same business key (two operations targeting `vtx.identity.<id>` concurrently): the revision condition in step 8 handles this at the substrate level; the Processor has no application-level lock. Correct per architecture, not a Processor bug.
