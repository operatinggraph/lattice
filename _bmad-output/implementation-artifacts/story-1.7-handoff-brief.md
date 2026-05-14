---
title: Story 1.7 Implementation Handoff Brief
story: 1.7 — Processor — DDL Validation & Atomic Batch (Steps 6-8)
model_tier: Opus (locked)
token_budget: ~145K
session: Fresh implementation session
architecture_lead: Winston
date: 2026-05-14
---

# Story 1.7 — Processor Steps 6-8: Implementation Handoff Brief

## Your Role

You implement steps 6 (DDL Validation), 7 (EventList Construction), and 8 (Atomic Batch Commit) of the Processor commit path. This story replaces Story 1.5's stubbed `Validator` and `Committer` (the atomic batch part of step 8) and Story 1.6 deferred the DDL cache to you. After Story 1.7, the Processor's hot path is real end-to-end through step 8 — only step 9 (event publication) and step 10 (ack) remain stubbed for Story 1.8.

## 🔴 MANDATORY OPERATING RULES (READ FIRST)

**A pattern across Stories 1.1, 1.5, 1.6: sub-agents have self-reported tokens 30-50% under actual.** Your self-estimate is NOT reliable enough to enforce a budget rule on its own. New protocol:

- **At every checkpoint (every 8-10 tool calls, OR any time you complete a deliverable, OR any time you read a file >25KB):** send a status message containing: deliverables completed so far, deliverables remaining, your honest token estimate, AND mark it explicitly as a "checkpoint message" so the parent can map it to outer telemetry.
- **Treat your self-estimate as a LOWER BOUND, not an actual value.** When in doubt, round up. If you "feel like" you've used 80K, report 110K.
- **Halt unconditionally if you estimate > 150K used** (about 5% over budget — generous threshold). Wait for explicit Winston greenlight.

Other rules (unchanged from prior briefs):
- **Model tier:** Opus only. Halt if Sonnet/Haiku.
- **No PRs.** Direct commit to `main` after Winston review.
- **Architecture binding:** `_bmad-output/planning-artifacts/data-contracts.md` source of truth.
- **DO NOT silently edit planning artifacts.** Use `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` (append, don't overwrite existing sections).
- **All KV operations through `internal/substrate`.**
- **No git commits by you.** Winston + Andrew commit.
- **Token tracker:** update Row 1.7 at session close.

## Story Scope (from `epics.md` — authoritative)

> As a platform engineer, I want steps 6 through 8 of the Processor commit path — MutationBatch validation, EventList construction, and NATS atomic batch commit — implemented and integration-tested, so that every committed operation is validated against DDL constraints and durably written atomically.

**Recommended model tier:** Opus
**Estimated token budget:** ~145K

### Acceptance Criteria (from `epics.md`)

**Given** a Starlark script has produced a proposed `MutationBatch`
**When** step 6 (MutationBatch validation) runs
**Then** each mutation in the batch is validated against its DDL meta-vertex: permitted op types from `permittedCommands` are enforced; sensitive aspect write-scope is enforced (sensitive aspects may only attach to identity vertices); the key pattern of each mutation target matches Contract #1; any DDL violation terminates the commit path with a `DDLViolation` error carrying the specific violated constraint.

**Given** the MutationBatch passes step 6 validation
**When** step 7 (EventList construction) runs
**Then** an ordered EventList is produced where each event has: `eventId` (NanoID), `requestId` (from original operation), `eventType`, `targetKey`, `payload`, and `timestamp`; the EventList contains exactly the events corresponding to the validated mutations.

**Given** a validated MutationBatch and EventList are ready
**When** step 8 (atomic batch commit) executes
**Then** a single NATS `PublishBatch` is submitted containing: all Core KV mutations, the Idempotency Tracker entry with 24h per-key TTL, and the `vtx.op.<requestId>` tracker key; the batch commits atomically; if any revision condition in the batch fails, the entire batch is rejected and the commit path returns a `ConflictError` with the conflicting key.

**And** on successful batch commit, the `vtx.op.<requestId>` tracker key is written with a `committed: true` value (replacing Story 1.5's stub which always wrote `committed: true` even without mutations).

**And** the DDL mutations lane (`ops.meta.>`) triggers synchronous Processor cache invalidation so subsequent operations see the new DDL immediately.

**And** integration tests cover: clean commit, DDL violation rejected, revision conflict rejected with ConflictError, mixed TTL + non-TTL batch behavior (per Story 1.1 spike).

## Required Context — Read These Only

| File | Section | Why |
|---|---|---|
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #1 §1.3 + §1.4 + §1.5 (key patterns, reserved names, envelope) + §1.7 (DDL/class lookup) | Validates each mutation conforms to §1.5; DDL cache pattern in §1.7 |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #3 (full — MutationBatch & EventList) | Authoritative shape for what step 7 emits |
| `_bmad-output/planning-artifacts/data-contracts.md` | Contract #4 (Idempotency Tracker — full) | Tracker entry shape and TTL behavior in atomic batch |
| `_bmad-output/planning-artifacts/epics.md` | Story 1.7 (canonical AC) | Source of truth |
| `internal/spike/nats-batch/README.md` | All 4 tests + API Discovery Note | Direct reference for atomic batch behavior |
| `internal/substrate/batch.go` | `Conn.AtomicBatch` + `BatchOp` | The substrate API you'll drive |
| `internal/processor/steps_4_10_stub.go` | `Validator`, `Committer`, `EventPublisher` interfaces | Interface contracts you must satisfy |
| `internal/processor/commit_path.go` | Whole file | How steps 6/7/8 plug in |
| `internal/processor/tracker.go` + `step2_dedup.go` | The existing tracker write path | What you upgrade |
| `internal/processor/reply.go` | Reply construction | Update `accepted-stub` → real shape after step 8 succeeds |
| `internal/bootstrap/lenses.go` + `internal/bootstrap/roles.go` | Whole files | Concrete DDL meta-vertex examples to validate against |
| `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` | Story 1.6 sections (DDL shadow keys, envelope.class) | Open amendments YOU must resolve in this story — read carefully |

## 🔴 Two OPEN Contract Amendments You MUST Resolve

Story 1.6 raised two amendments that block clean DDL validation in Story 1.7. Winston's directive: **resolve both as part of this story**, don't kick them further down the road.

### Amendment A: DDL shadow key `vtx.meta.<class>` (Issue 1 from Story 1.6 amendment)

**Resolution: bring the DDL cache forward into Story 1.7.** Story 1.6 used shadow keys (`vtx.meta.identity` etc.) as a temporary measure. Story 1.7 cannot validate DDLs that way — the validator must read DDL meta-vertices by their canonical NanoID-keyed location and consult their full aspect surface.

**What to build:**
- Add `internal/processor/ddl_cache.go` — at Processor startup, scan `vtx.meta.>` keys in Core KV, build an in-memory `map[canonicalName]MetaVertexRef` where `MetaVertexRef` includes the real NanoID key, the `permittedCommands` aspect, the `script` aspect, the sensitivity flag, and any other DDL-relevant aspects.
- DDL cache is refreshed on CDC events affecting `vtx.meta.*` (synchronous invalidation per the AC's "DDL mutations lane synchronous cache invalidation" requirement).
- Step 4 (Hydrator) is updated to consult the cache instead of building shadow keys: `class → MetaVertexRef → real meta-vertex key for hydration`.
- Step 6 (Validator) consults the cache for each mutation's DDL.

**Cleanup:** the `vtx.meta.<class>` shadow keys written by Story 1.3's bootstrap and Story 1.6's test harness can either (a) be removed once the cache is built from real NanoID-keyed DDLs, OR (b) treated as aliases. Recommend (a) for cleanliness. The bootstrap's primordial DDLs (capability lens, role-index lens, identity DDL when it lands) are already NanoID-keyed; only Story 1.6's test fixtures need migration.

### Amendment B: Top-level `class` field on `OperationEnvelope` (Issue 2 from Story 1.6 amendment)

**Resolution: keep `class` as an optional hint, document it as Phase-1-transient.** The DDL cache (Amendment A's work) will let Hydrator derive class from `operationType` going forward — at that point `class` becomes redundant but harmless. Leave the field in place (it's `omitempty`), add a doc-comment marking it deprecated-on-arrival of full DDL cache, but don't remove it in Story 1.7. Removal can come in Story 1.10 or whenever the cache covers 100% of operation type → class derivation.

**No Contract #2 amendment needed** — the field is already in the Go struct from Story 1.6 (it's a wire-format addition that's `omitempty`, so existing clients are unaffected). Document the disposition in `data-contracts.md` Contract #2 §2.1 as a "Phase 1 transitional field" addendum — this IS a planning-artifact edit, do it via `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` first (mark both amendments RESOLVED with your disposition).

## Architectural Decisions Already Made (Winston)

1. **Use `substrate.AtomicBatch`** for step 8. Story 1.1's spike validated this; Story 1.4 wrapped it. Do NOT roll your own batch headers.
2. **Tracker entry upgrade in step 8:** Story 1.5 wrote a stub tracker with `committed: true` even before mutations. Story 1.7's step 8 writes the SAME tracker key with the SAME 24h TTL but now ALONGSIDE the real mutations in the same atomic batch. The pre-mutation tracker write in Story 1.5's `Committer` stub goes away; the batch in step 8 IS the single tracker write.
3. **Sensitive aspect write-scope (NFR-S3):** sensitive aspects (declared via `sensitive: true` on the DDL aspect schema) may attach ONLY to identity vertices. Validate at step 6 by inspecting the mutation's `key` segment 2 (`vtx.<type>.<id>.<localName>` — type must be `identity`).
4. **Key pattern validation:** every mutation's key must parse cleanly via `substrate.ParseVertexKey` / `ParseAspectKey` / `ParseLinkKey`. Substrate panics on invalid input — DON'T use those builders in validation hot paths; instead use the parsers (which return errors).
5. **EventList event IDs:** generate via `substrate.NewNanoID()` per event. Event order matches the mutation order from the script. Each event's `targetKey` is its corresponding mutation's key.
6. **DDL violation surfaces a `DDLViolation` error type** with `ViolatedConstraint`, `MutationKey`, `OperationRequestID`. Maps to a `decision: "rejected", reason: "DDLViolation"` reply.
7. **Revision conflict surfaces a `ConflictError`** with `ConflictingKey`, `ExpectedRevision`, `OperationRequestID`. Substrate's `AtomicBatch` already returns typed conflicts — wrap and propagate.
8. **DDL meta-vertex synchronous cache invalidation:** when a step 8 commit contains a mutation to `vtx.meta.>`, the Processor's DDL cache must invalidate the affected entry within the same step (before the reply is sent). This is straightforward — the Committer can call `ddlCache.Invalidate(class)` right after a successful batch commit if any mutation key starts with `vtx.meta.`.
9. **`ops.<lane>.>` consumer filter alignment:** Story 1.6 left the consumer filter as single-segment. Update `internal/processor/step1_consume.go` to `["ops.default.>", "ops.urgent.>", "ops.system.>"]` so consumers receive multi-segment publishes. Test by publishing `ops.default.<requestId>` and confirming the Processor consumes it.
10. **Reply envelope post-1.7:** remove Story 1.5's `decision: "accepted-stub"` marker. On step 8 success, return `decision: "committed", trackerKey: "vtx.op.<requestId>", revisions: {<key>: <revision>, …}` (the revisions returned by the atomic batch — useful for client read-your-own-writes).

## Suggested Layout

```
internal/processor/
├── (existing files)
├── ddl_cache.go            NEW: in-memory canonicalName→MetaVertexRef
├── ddl_cache_test.go       NEW
├── step6_validate.go       NEW: ValidatorImpl
├── step6_validate_test.go  NEW
├── step7_events.go         NEW: EventList construction
├── step7_events_test.go    NEW
├── step8_commit.go         NEW: CommitterImpl (replaces stub)
└── step8_commit_test.go    NEW
```

Modifies: `commit_path.go`, `step1_consume.go` (filter), `step4_hydrate.go` (uses cache), `reply.go` (new shape), `tracker.go` (tracker is now batch-side), `steps_4_10_stub.go` (drops Validator + Committer stubs; keeps step 9/10 stubs).

## Deliverables Checklist

1. ✅ DDL cache module + tests
2. ✅ `step6_validate.go` ValidatorImpl + tests (permittedCommands, sensitive aspect scope, key pattern parsing)
3. ✅ `step7_events.go` EventList construction + tests
4. ✅ `step8_commit.go` CommitterImpl (substrate.AtomicBatch wiring; tracker write in batch; synchronous DDL cache invalidation on `vtx.meta.>` mutations) + tests
5. ✅ Story 1.5's stubbed Committer's pre-mutation tracker write REMOVED (single tracker write in step 8 batch)
6. ✅ Story 1.6's shadow-key DDL access REMOVED; Hydrator + Validator both consult DDL cache
7. ✅ `cmd/processor/CONTRACT-AMENDMENT-REQUEST.md` updated: Story 1.6's two sections marked RESOLVED with disposition notes
8. ✅ `data-contracts.md` Contract #2 §2.1 addendum documenting `class` as Phase-1-transitional field (small, scoped edit — flag explicitly in your closing summary)
9. ✅ `step1_consume.go` consumer filter updated to `ops.<lane>.>`
10. ✅ `reply.go` updated: post-1.7 successful reply shape replaces `accepted-stub`
11. ✅ Integration tests: clean commit; DDL violation (forbidden op, sensitive on non-identity, key pattern mismatch); revision conflict; mixed-TTL batch; multi-subject batch within Core KV (per Story 1.1 finding); DDL cache invalidation on meta-vertex mutation
12. ✅ `make verify-bootstrap` still passes 30+ assertions
13. ✅ `go build ./...`, `go vet ./...`, `go test ./internal/processor/...` exit 0
14. ✅ Token tracker Row 1.7 updated with HONEST estimate (round UP)
15. ✅ Closing summary

## What Story 1.7 Is NOT

- **Not** event publication (step 9) — Story 1.8
- **Not** the full FR57 write-scope enforcement story — Story 1.9 covers per-DDL `permittedCommands` enforcement holistically (with its own test suite). Story 1.7 validates `permittedCommands` at step 6 but doesn't ship the dedicated FR57 test suite.
- **Not** the bypass test suite (Story 1.10).

## Escalation

- Halt and escalate to Winston via Andrew if:
  - NATS atomic batch behavior differs from spike findings
  - DDL cache design has a structural problem you can't resolve
  - Token estimate exceeds 150K
  - Either open amendment requires more than the disposition documented in this brief

## Closing

1. Verify all 15 deliverables
2. Full e2e cycle: `make down && make up && make verify-bootstrap` + processor test suite all green
3. Manual e2e: publish a real operation through `cmd/processor`, observe steps 1→8 in logs with real mutations committed to Core KV
4. Verify revisions are returned in the reply
5. Update token tracker — round your estimate UP
6. Closing summary: deliverables present, e2e observations, amendments resolved (yes), tokens (honest), open questions

Do NOT commit. Winston + Andrew review and commit.
